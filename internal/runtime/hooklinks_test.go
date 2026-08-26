package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"flatline/internal/assets"
	"flatline/internal/canonical"
	"flatline/internal/history"
	"flatline/internal/storage"
)

// registerFixtureHook puts one hook asset in the registry so a fixture block
// has something to name. It is a synthetic asset; no real hook is read.
func registerFixtureHook(t *testing.T, db *storage.DB, sourcePath string) string {
	t.Helper()
	ctx := context.Background()
	registry := assets.New(db)
	at := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	id, err := registry.Register(ctx, assets.AssetInput{Kind: assets.KindHook, Scope: assets.ScopeUser,
		Name: "fixture:hooks:commit-guard", SourcePath: sourcePath, FirstSeenAt: at})
	if err != nil {
		t.Fatalf("register hook asset: %v", err)
	}
	if _, err := registry.RecordVersion(ctx, assets.VersionInput{AssetID: id,
		Content: []byte("synthetic hook fixture\n"), ObservationLevel: canonical.LevelInvoked,
		ObservedAt: at, ContentRef: "fixture:hook:v1"}); err != nil {
		t.Fatalf("record hook version: %v", err)
	}
	return id
}

// writeHookBlockFixture writes a synthetic Claude Code transcript whose tool
// result is a PreToolUse hook block naming a hook script. It is not a copy of
// any real local session.
func writeHookBlockFixture(t *testing.T, dir, sessionID, hookPath string) string {
	t.Helper()
	path := filepath.Join(dir, sessionID+".jsonl")
	records := []map[string]any{
		{"type": "user", "sessionId": sessionID, "timestamp": "2026-08-20T09:00:00Z", "cwd": dir, "version": "2.15.0",
			"message": map[string]any{"role": "user", "content": "实现提交流程并验证"}},
		{"type": "assistant", "sessionId": sessionID, "timestamp": "2026-08-20T09:01:00Z", "cwd": dir, "version": "2.15.0",
			"message": map[string]any{"id": "m1", "role": "assistant", "model": "fixture-model", "content": []any{
				map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "git commit -m fixture"}},
			}}},
		{"type": "user", "sessionId": sessionID, "timestamp": "2026-08-20T09:02:00Z", "cwd": dir, "version": "2.15.0",
			"message": map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "is_error": true,
					"content": "Command blocked by PreToolUse hook [" + hookPath + "]: commit message does not match the required format."},
			}}},
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatalf("encode fixture: %v", err)
		}
	}
	return path
}

func TestHookBlockBecomesObservedUseParticipationForTheNamedHook(t *testing.T) {
	ctx := context.Background()
	app, db := testApp(t)
	root := t.TempDir()
	hookPath := filepath.Join(root, "hooks", "commit-guard.sh")
	assetID := registerFixtureHook(t, db, hookPath)
	writeHookBlockFixture(t, root, "cc-hook-block", hookPath)

	if _, err := app.ImportNativeHistory(ctx, history.Config{ClaudeRoot: root, ProjectRoot: root}); err != nil {
		t.Fatalf("import: %v", err)
	}

	var links int
	var rule string
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(rule), '') FROM asset_friction_links WHERE asset_id = ?`, assetID).
		Scan(&links, &rule); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if links != 1 {
		t.Fatalf("asset_friction_links = %d, want 1", links)
	}
	if rule == "" {
		t.Fatalf("link carries no rule")
	}

	var signal, level string
	if err := db.QueryRowContext(ctx, `
		SELECT p.participation_signal, p.observation_level
		FROM participations p
		JOIN asset_versions v ON v.id = p.asset_version_id
		WHERE v.asset_id = ? AND p.superseded_at IS NULL`, assetID).Scan(&signal, &level); err != nil {
		t.Fatalf("read participation: %v", err)
	}
	if signal != string(canonical.SignalObservedUse) || level != string(canonical.LevelObservedUse) {
		t.Fatalf("participation = %s/%s, want observed-use/observed-use", signal, level)
	}

	// The block is the opportunity as well as the participation: without a
	// denominator the state machine would still read the hook as never asked.
	var opportunities int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM opportunities WHERE asset_id = ? AND superseded_at IS NULL`, assetID).
		Scan(&opportunities); err != nil {
		t.Fatalf("count opportunities: %v", err)
	}
	if opportunities != 1 {
		t.Fatalf("opportunities = %d, want 1", opportunities)
	}

	decisions, err := app.EvaluateAll(ctx, time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	for _, decision := range decisions {
		if decision.AssetID == assetID && decision.State == "no_opportunity" {
			t.Fatalf("hook with a recorded block is still %s", decision.State)
		}
	}
}

func TestHookBlockThatNamesNoRegisteredHookLinksNothing(t *testing.T) {
	ctx := context.Background()
	app, db := testApp(t)
	root := t.TempDir()
	registerFixtureHook(t, db, filepath.Join(root, "hooks", "commit-guard.sh"))
	// The block carries the hook's own message and nothing that names it.
	writeHookBlockFixture(t, root, "cc-hook-anon", "")

	if _, err := app.ImportNativeHistory(ctx, history.Config{ClaudeRoot: root, ProjectRoot: root}); err != nil {
		t.Fatalf("import: %v", err)
	}
	var links int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM asset_friction_links`).Scan(&links); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if links != 0 {
		t.Fatalf("asset_friction_links = %d, want 0: a block that names no hook must link to none", links)
	}
}

func TestDiscoveryReadsASymlinkedTranscriptOnce(t *testing.T) {
	ctx := context.Background()
	app, db := testApp(t)
	root := t.TempDir()
	real := filepath.Join(root, "real")
	linked := filepath.Join(root, "linked")
	for _, dir := range []string{real, linked} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	path := writeNativeFixture(t, real, "cc-symlinked")
	if err := os.Symlink(path, filepath.Join(linked, "cc-symlinked.jsonl")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	report, err := app.ImportNativeHistory(ctx, history.Config{ClaudeRoot: root, ProjectRoot: root})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if report.FilesSeen != 1 || report.FilesRead != 1 {
		t.Fatalf("report = %+v, want one file seen and read", report)
	}
	var files int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_files`).Scan(&files); err != nil {
		t.Fatalf("count native files: %v", err)
	}
	if files != 1 {
		t.Fatalf("native_files = %d, want 1: the same transcript under two paths is one file", files)
	}
}

func TestPruneLinkedNativeFilesDropsRowsWrittenForASymlink(t *testing.T) {
	ctx := context.Background()
	app, db := testApp(t)
	root := t.TempDir()
	path := writeNativeFixture(t, root, "cc-prune")
	link := filepath.Join(root, "cc-prune-link.jsonl")
	if err := os.Symlink(path, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, recorded := range []string{path, link} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO native_files (path, size, mtime_ns, last_read_at)
			VALUES (?, 1, 1, '2026-08-20T09:00:00.000Z')`, recorded); err != nil {
			t.Fatalf("insert native file: %v", err)
		}
	}
	pruned, err := app.PruneLinkedNativeFiles(ctx)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}
	var remaining string
	if err := db.QueryRowContext(ctx, `SELECT path FROM native_files`).Scan(&remaining); err != nil {
		t.Fatalf("read remaining: %v", err)
	}
	if remaining != path {
		t.Fatalf("remaining = %q, want the real path %q", remaining, path)
	}
}
