package eventstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"flatline/internal/adapters"
	"flatline/internal/adapters/claudecode"
	"flatline/internal/adapters/codex"
	"flatline/internal/canonical"
	"flatline/internal/storage"
)

func TestAdapterReplayIntoCanonicalStore(t *testing.T) {
	cases := []struct {
		name        string
		source      adapters.Source
		adapter     adapters.Adapter
		dir         string
		first       string
		second      string
		wantAnchors int
	}{
		{"claude_code", adapters.SourceClaudeCode, claudecode.New(), "claudecode", "normal", "version_change", 2},
		{"codex", adapters.SourceCodex, codex.New(), "codex", "normal", "version_change", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "integration.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store := New(db)
			firstMeta, firstEvents := parseFixture(t, tc.adapter, tc.source, tc.dir, tc.first)
			seedAssets(t, db, firstEvents)
			firstID, err := store.IngestSession(ctx, tc.source, firstMeta)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := store.IngestEvents(ctx, firstID, firstEvents); err != nil || got != len(firstEvents) {
				t.Fatalf("first events = %d/%d: %v", got, len(firstEvents), err)
			}
			if got, err := store.IngestEvents(ctx, firstID, firstEvents); err != nil || got != 0 {
				t.Fatalf("duplicate first events = %d: %v", got, err)
			}

			secondMeta, secondEvents := parseFixture(t, tc.adapter, tc.source, tc.dir, tc.second)
			seedAssets(t, db, secondEvents)
			secondID, err := store.IngestSession(ctx, tc.source, secondMeta)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := store.DetectEnvironmentChanges(ctx, secondID); err != nil || got != tc.wantAnchors {
				t.Fatalf("environment anchors = %d, want %d: %v", got, tc.wantAnchors, err)
			}
			if got, err := store.IngestEvents(ctx, secondID, secondEvents); err != nil || got != len(secondEvents) {
				t.Fatalf("second events = %d/%d: %v", got, len(secondEvents), err)
			}

			rows, err := store.EventsForSession(ctx, secondID)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != len(secondEvents)+tc.wantAnchors {
				t.Fatalf("second stored events = %d, want %d", len(rows), len(secondEvents)+tc.wantAnchors)
			}
			for _, row := range rows {
				if !row.Locator.Valid() {
					t.Fatalf("invalid stored locator: %#v", row)
				}
			}
			for _, row := range firstEvents {
				if row.EventType != canonical.EventTypeAssetInvoked {
					continue
				}
				got, err := store.EventByLocator(ctx, row.Locator)
				if err != nil {
					t.Fatal(err)
				}
				if got.SourceEventID != row.SourceEventID {
					t.Fatalf("locator round-trip id = %q, want %q", got.SourceEventID, row.SourceEventID)
				}
				break
			}
		})
	}
}

func parseFixture(t *testing.T, adapter adapters.Adapter, source adapters.Source, dir, name string) (adapters.SessionMeta, []canonical.Event) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", dir, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	meta, events, err := adapter.Parse(adapters.RawSession{Source: source, RawJSON: data})
	if err != nil {
		t.Fatal(err)
	}
	return meta, events
}

func seedAssets(t *testing.T, db *storage.DB, events []canonical.Event) {
	t.Helper()
	for _, event := range events {
		if event.AssetID == "" {
			continue
		}
		if _, err := db.Exec(`INSERT OR IGNORE INTO assets (id, kind, name, first_seen_at) VALUES (?, 'skill', ?, '2026-01-01T00:00:00Z')`, event.AssetID, event.AssetID); err != nil {
			t.Fatal(err)
		}
	}
}
