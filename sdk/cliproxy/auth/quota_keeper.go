package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	internalcodex "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	log "github.com/sirupsen/logrus"
)

const (
	defaultQuotaKeeperInterval = 30 * time.Minute
	quotaWindowDuration        = 5 * time.Hour
	quotaResetMetadataKey      = "quota_keeper_5h_reset_at"
	quotaWarmMetadataKey       = "quota_keeper_last_warm_at"
)

// StartQuotaKeeper periodically checks Codex OAuth usage. It uses
// the usage endpoint for a low-cost probe and sends one tiny message only when the
// account is full but no 5-hour reset window has started yet.
func (m *Manager) StartQuotaKeeper(parent context.Context, interval time.Duration, model, endpoint string) {
	if m == nil {
		return
	}
	if interval <= 0 {
		interval = defaultQuotaKeeperInterval
	}
	ctx, cancel := context.WithCancel(parent)
	m.mu.Lock()
	if m.quotaKeeperCancel != nil {
		m.quotaKeeperCancel()
	}
	m.quotaKeeperCancel = cancel
	m.mu.Unlock()
	go m.quotaKeeperLoop(ctx, interval, strings.TrimSpace(model), strings.TrimSpace(endpoint))
}

func (m *Manager) StopQuotaKeeper() {
	if m == nil {
		return
	}
	m.mu.Lock()
	cancel := m.quotaKeeperCancel
	m.quotaKeeperCancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *Manager) quotaKeeperLoop(ctx context.Context, interval time.Duration, model, endpoint string) {
	log.Infof("Codex quota keeper running (interval=%s)", interval)
	probe := func() {
		for _, auth := range m.List() {
			if auth == nil || auth.Disabled {
				continue
			}
			typeValue, _ := auth.Metadata["type"].(string)
			if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") && !strings.EqualFold(strings.TrimSpace(typeValue), "codex") {
				continue
			}
			m.probeCodexQuota(ctx, auth, interval, model, endpoint)
		}
	}
	probe()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			probe()
		}
	}
}

func (m *Manager) probeCodexQuota(ctx context.Context, auth *Auth, interval time.Duration, model, endpoint string) {
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if model == "" {
		model = "gpt-5.4"
	}
	now := time.Now().UTC()
	// A reset timestamp returned by wham/usage is also present for an account
	// that has not made a request yet. Only trust it when this keeper has
	// recorded a warmup, otherwise the first full-quota probe must warm the
	// account to start the window.
	if reset := metadataTime(auth.Metadata, quotaResetMetadataKey); reset.After(now) && !metadataTime(auth.Metadata, quotaWarmMetadataKey).IsZero() {
		return
	}
	accessToken := quotaAuthAccessToken(auth)
	accountID := quotaAuthAccountID(auth)
	client := &http.Client{Transport: m.roundTripperFor(auth)}
	snapshot, err := internalcodex.ProbeQuota(probeCtx, client, endpoint, accessToken, accountID)
	if err != nil {
		log.WithError(err).WithField("auth_id", auth.ID).Warn("Codex quota probe failed")
		return
	}
	now = time.Now().UTC()
	if snapshot.UsedPercent == nil {
		return
	}
	if *snapshot.UsedPercent > 0 {
		if snapshot.ResetAt.After(now) {
			m.persistQuotaMetadata(ctx, auth, snapshot.ResetAt, time.Time{})
		}
		return
	}
	lastWarm := metadataTime(auth.Metadata, quotaWarmMetadataKey)
	if !lastWarm.IsZero() && now.Sub(lastWarm) < interval {
		return
	}
	if err = internalcodex.Warmup(probeCtx, client, endpoint, accessToken, accountID, model); err != nil {
		log.WithError(err).WithField("auth_id", auth.ID).Warn("Codex quota warmup failed")
		return
	}
	resetAt := snapshot.ResetAt
	if !resetAt.After(now) {
		resetAt = now.Add(quotaWindowDuration)
	}
	m.persistQuotaMetadata(ctx, auth, resetAt, now)
	log.WithField("auth_id", auth.ID).Info("quota keeper warmed Codex 5-hour window")
}

func quotaAuthAccessToken(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if v, ok := auth.Metadata["access_token"].(string); ok {
		return strings.TrimSpace(v)
	}
	if storage, ok := auth.Storage.(*internalcodex.CodexTokenStorage); ok {
		return strings.TrimSpace(storage.AccessToken)
	}
	return ""
}

func quotaAuthAccountID(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if v, ok := auth.Metadata["account_id"].(string); ok {
		return strings.TrimSpace(v)
	}
	if storage, ok := auth.Storage.(*internalcodex.CodexTokenStorage); ok {
		return strings.TrimSpace(storage.AccountID)
	}
	return ""
}

func metadataTime(meta map[string]any, key string) time.Time {
	if meta == nil {
		return time.Time{}
	}
	s, _ := meta[key].(string)
	t, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(s))
	return t
}

func (m *Manager) persistQuotaMetadata(ctx context.Context, auth *Auth, reset, warm time.Time) {
	if auth == nil {
		return
	}
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	if !reset.IsZero() {
		updated.Metadata[quotaResetMetadataKey] = reset.UTC().Format(time.RFC3339Nano)
	} else {
		delete(updated.Metadata, quotaResetMetadataKey)
	}
	if !warm.IsZero() {
		updated.Metadata[quotaWarmMetadataKey] = warm.UTC().Format(time.RFC3339Nano)
	}
	_, _ = m.Update(ctx, updated)
}
