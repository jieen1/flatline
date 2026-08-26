package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"flatline/internal/adapters"
	"flatline/internal/eventstore"
)

func TestHealthReportsOneRowPerConfiguredRoot(t *testing.T) {
	db := testAPIDB(t)
	ctx := context.Background()
	root := t.TempDir()
	store := eventstore.New(db)
	if err := store.RegisterSource(ctx, string(adapters.SourceClaudeCode), root, "Claude Code"); err != nil {
		t.Fatalf("register source: %v", err)
	}
	handler := NewServerWithDB(db).Handler()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ingest/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d", rec.Code)
	}
	var health struct {
		Sources []struct {
			Kind       string `json:"kind"`
			Root       string `json:"root"`
			Status     string `json:"status"`
			Configured bool   `json:"configured"`
		} `json:"sources"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	claude := 0
	for _, source := range health.Sources {
		if source.Kind != string(adapters.SourceClaudeCode) {
			continue
		}
		claude++
		if source.Root != root || !source.Configured {
			t.Fatalf("claude_code row = %+v, want the registered root", source)
		}
		if source.Status != "configured" {
			t.Fatalf("claude_code status = %q, want configured before the first probe", source.Status)
		}
	}
	if claude != 1 {
		t.Fatalf("claude_code rows = %d, want 1: the registry row and the stored-session row are one root", claude)
	}
}

// coverageFixtureDB records the same harness rule in two sessions, and gives
// the caller the rule asset's path so a test can decide whether its text
// mentions the mechanism.
