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

// Warmup sends a minimal conversation request to start the 5-hour window.
// Codex returns an SSE stream that may remain open while it waits for more
// events, so a successful HTTP response is sufficient; callers must not wait
// for EOF on the response body.
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
		"input": []map[string]any{{"type": "message", "role": "user", "content": "ping"}},
		// Codex ChatGPT accounts require streaming responses on this endpoint.
		// The response body is discarded after the server accepts the request.
		"stream":            true,
		"store":             false,
		"max_output_tokens": 1,
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
	req.Header.Set("Origin", "https://chatgpt.com")
	req.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	// Do not drain the SSE stream: the upstream can keep it open indefinitely.
	// Closing immediately after headers are received still records the request
	// and starts the quota window, while avoiding a 30-second context timeout.
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("warmup conversation returned status %d", resp.StatusCode)
	}
	return nil
}
