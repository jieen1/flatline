package eventstore

import (
	"context"
	"path/filepath"
	"testing"

	"flatline/internal/adapters"
	"flatline/internal/adapters/claudecode"
	"flatline/internal/adapters/codex"
	"flatline/internal/friction"
	"flatline/internal/storage"
)

type frictionWant struct {
	kind     string
	toolName string
	category string
	rule     string
}

func TestFrictionReplayLinksToolIdentityAndClassifies(t *testing.T) {
	cases := []struct {
		name    string
		source  adapters.Source
		adapter adapters.Adapter
		dir     string
		want    map[string]frictionWant
	}{
		{
			name: "claude_code", source: adapters.SourceClaudeCode, adapter: claudecode.New(), dir: "claudecode",
			want: map[string]frictionWant{
				"2026-08-21T09:01:30Z": {friction.KindToolError, "Bash", friction.CategoryTestFailure, `工具是测试命令且输出包含 "FAIL"`},
				"2026-08-21T09:02:05Z": {friction.KindToolError, "Read", friction.CategoryFileNotFound, `输出包含 "does not exist"`},
				"2026-08-21T09:03:04Z": {friction.KindToolError, "Bash", friction.CategoryCommandNotFound, `输出包含 "command not found"`},
				"2026-08-21T09:04:00Z": {friction.KindToolError, "", friction.CategoryToolError, "明确记录 is_error=true 且未命中更具体规则"},
				"2026-08-21T09:06:00Z": {friction.KindUserInterrupt, "", friction.CategoryUserInterrupt, `消息文本以 "[Request interrupted by user" 开头`},
			},
		},
		{
			name: "codex", source: adapters.SourceCodex, adapter: codex.New(), dir: "codex",
			want: map[string]frictionWant{
				"2026-08-21T11:01:10Z": {friction.KindToolError, "shell", friction.CategoryNetworkError, `输出包含 "ECONNREFUSED"`},
				"2026-08-21T11:02:05Z": {friction.KindToolError, "apply_patch", friction.CategoryToolInputInvalid, `输出包含 "InputValidationError"`},
				"2026-08-21T11:03:00Z": {friction.KindToolError, "", friction.CategoryNonzeroExit, "明确记录 exit_code=2 且未命中更具体规则"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "friction.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store := New(db)
			meta, events := parseFixture(t, tc.adapter, tc.source, tc.dir, "friction")
			sessionID, err := store.IngestSession(ctx, tc.source, meta)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.IngestEvents(ctx, sessionID, events); err != nil {
				t.Fatal(err)
			}
			inserted, err := store.IngestFriction(ctx, sessionID, events)
			if err != nil {
				t.Fatal(err)
			}
			if inserted != len(tc.want) {
				t.Fatalf("friction inserted = %d, want %d", inserted, len(tc.want))
			}
			if replayed, err := store.IngestFriction(ctx, sessionID, events); err != nil || replayed != 0 {
				t.Fatalf("replayed friction = %d: %v", replayed, err)
			}
			got := loadFrictionRows(t, db, sessionID)
			if len(got) != len(tc.want) {
				t.Fatalf("stored friction rows = %d, want %d (%v)", len(got), len(tc.want), got)
			}
			for at, want := range tc.want {
				row, ok := got[at]
				if !ok {
					t.Fatalf("no friction row at %s; got %v", at, got)
				}
				if row != want {
					t.Errorf("%s = %+v, want %+v", at, row, want)
				}
			}
		})
	}
}

func TestReclassifyFrictionRecomputesStaleRows(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "reclassify.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := New(db)
	meta, events := parseFixture(t, claudecode.New(), adapters.SourceClaudeCode, "claudecode", "friction")
	sessionID, err := store.IngestSession(ctx, adapters.SourceClaudeCode, meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.IngestFriction(ctx, sessionID, events); err != nil {
		t.Fatal(err)
	}
	if recomputed, err := store.ReclassifyFriction(ctx); err != nil || recomputed != 0 {
		t.Fatalf("reclassify with current version = %d: %v", recomputed, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE friction_records SET category = NULL, category_rule = NULL, classifier_version = 'friction/0'`); err != nil {
		t.Fatal(err)
	}
	recomputed, err := store.ReclassifyFriction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows := loadFrictionRows(t, db, sessionID)
	if recomputed != len(rows) {
		t.Fatalf("reclassified = %d, want %d", recomputed, len(rows))
	}
	row, ok := rows["2026-08-21T09:01:30Z"]
	if !ok || row.category != friction.CategoryTestFailure {
		t.Fatalf("reclassified row = %+v, want %s", row, friction.CategoryTestFailure)
	}
	var stale int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM friction_records WHERE classifier_version != ?`, friction.ClassifierVersion).Scan(&stale); err != nil {
		t.Fatal(err)
	}
	if stale != 0 {
		t.Fatalf("rows still on an old classifier version = %d", stale)
	}
}

func loadFrictionRows(t *testing.T, db *storage.DB, sessionID string) map[string]frictionWant {
	t.Helper()
	rows, err := db.Query(`
		SELECT COALESCE(occurred_at, ''), friction_kind, COALESCE(tool_name, ''), COALESCE(category, ''), COALESCE(category_rule, '')
		FROM friction_records WHERE session_id = ?`, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := make(map[string]frictionWant)
	for rows.Next() {
		var id string
		var record frictionWant
		if err := rows.Scan(&id, &record.kind, &record.toolName, &record.category, &record.rule); err != nil {
			t.Fatal(err)
		}
		out[id] = record
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
