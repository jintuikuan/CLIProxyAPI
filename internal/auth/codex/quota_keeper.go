package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultQuotaEndpoint = "https://chatgpt.com/backend-api/wham/usage"
const defaultWarmupModel = "gpt-5.4"

// QuotaSnapshot is the subset of the wham usage response needed by the keeper.
type QuotaSnapshot struct {
	UsedPercent *float64
	ResetAt     time.Time
}

type quotaResponse struct {
	RateLimit struct {
		PrimaryWindow struct {
			UsedPercent float64 `json:"used_percent"`
			ResetAt     int64   `json:"reset_at"`
		} `json:"primary_window"`
		Primary struct {
			UsedPercent float64 `json:"used_percent"`
			ResetAt     int64   `json:"reset_at"`
		} `json:"primary"`
	} `json:"rate_limit"`
}

// ProbeQuota checks the Codex usage endpoint using an OAuth access token.
func ProbeQuota(ctx context.Context, client *http.Client, endpoint, accessToken, accountID string) (QuotaSnapshot, error) {
	if client == nil {
		client = http.DefaultClient
	}
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		endpoint = defaultQuotaEndpoint
	}
	if strings.TrimSpace(accessToken) == "" {
		return QuotaSnapshot{}, fmt.Errorf("codex access token is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return QuotaSnapshot{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if accountID = strings.TrimSpace(accountID); accountID != "" {
		req.Header.Set("ChatGPT-Account-ID", accountID)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return QuotaSnapshot{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return QuotaSnapshot{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return QuotaSnapshot{}, fmt.Errorf("quota endpoint returned status %d", resp.StatusCode)
	}
	var parsed quotaResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return QuotaSnapshot{}, err
	}
	p := parsed.RateLimit.PrimaryWindow
	if p.UsedPercent == 0 && p.ResetAt == 0 {
		p = parsed.RateLimit.Primary
	}
	used := p.UsedPercent
	snapshot := QuotaSnapshot{UsedPercent: &used}
	if p.ResetAt > 0 {
		snapshot.ResetAt = time.Unix(p.ResetAt, 0).UTC()
	}
	return snapshot, nil
}

// Warmup sends a minimal non-streaming conversation request to start the 5-hour window.
func Warmup(ctx context.Context, client *http.Client, endpoint, accessToken, accountID, model string) error {
	if client == nil {
		client = http.DefaultClient
	}
	endpoint = strings.TrimSpace(endpoint)
	endpoint = strings.TrimSuffix(endpoint, "/wham/usage") + "/codex/responses"
	if strings.TrimSpace(endpoint) == "" || endpoint == "/codex/responses" {
		endpoint = "https://chatgpt.com/backend-api/codex/responses"
	}
	if model = strings.TrimSpace(model); model == "" {
		model = defaultWarmupModel
	}
	payload := map[string]any{
		"model": model,
		"input": []map[string]any{{"role": "user", "content": []map[string]string{{"type": "input_text", "text": "ping"}}}},
		// Codex ChatGPT accounts require streaming responses on this endpoint.
		// The response body is discarded after the server accepts the request.
		"stream": true,
		"store":  false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if accountID = strings.TrimSpace(accountID); accountID != "" {
		req.Header.Set("ChatGPT-Account-ID", accountID)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("warmup conversation returned status %d", resp.StatusCode)
	}
	return nil
}
