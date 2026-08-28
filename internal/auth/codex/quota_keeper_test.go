package codex

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeQuotaParsesPrimaryWindow(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rate_limit":{"primary_window":{"used_percent":0,"reset_at":4102444800}}}`))
	}))
	defer ts.Close()
	snapshot, err := ProbeQuota(context.Background(), ts.Client(), ts.URL, "token", "acct")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.UsedPercent == nil || *snapshot.UsedPercent != 0 || snapshot.ResetAt.IsZero() {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}

func TestWarmupPostsConversation(t *testing.T) {
	var gotBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/codex/responses") {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		buf, _ := io.ReadAll(r.Body)
		gotBody = string(buf)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	if err := Warmup(context.Background(), ts.Client(), ts.URL+"/wham/usage", "token", "acct", "gpt-5-codex"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"model":"gpt-5-codex"`) {
		t.Fatalf("warmup body missing model: %s", gotBody)
	}
}
