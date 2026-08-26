package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/adapters/claudecode"
	"flatline/internal/adapters/codex"
	"flatline/internal/assets"
	"flatline/internal/canonical"
	"flatline/internal/history"
	"flatline/internal/storage"
	"flatline/internal/vital"
)

func testApp(t *testing.T) (*App, *storage.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "session-layer.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	registry := adapters.NewRegistry()
	if err := registry.Register(claudecode.New()); err != nil {
		t.Fatalf("register claude adapter: %v", err)
	}
	if err := registry.Register(codex.New()); err != nil {
		t.Fatalf("register codex adapter: %v", err)
	}
	return New(db, registry, vital.DefaultConfig()), db
}

// writeNativeFixture writes a synthetic Claude Code transcript. It is not a
// copy of any real local session.
func writeNativeFixture(t *testing.T, dir, sessionID string) string {
	t.Helper()
	path := filepath.Join(dir, sessionID+".jsonl")
	records := []map[string]any{
		{"type": "user", "sessionId": sessionID, "timestamp": "2026-08-20T09:00:00Z", "cwd": dir, "version": "2.15.0",
			"message": map[string]any{"role": "user", "content": "实现登录页的重构与验证"}},
		{"type": "assistant", "sessionId": sessionID, "timestamp": "2026-08-20T09:01:00Z", "cwd": dir, "version": "2.15.0",
			"message": map[string]any{"id": "m1", "role": "assistant", "model": "fixture-model", "content": []any{
				map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "go test ./..."}},
			}}},
		{"type": "user", "sessionId": sessionID, "timestamp": "2026-08-20T09:02:00Z", "cwd": dir, "version": "2.15.0",
			"message": map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "is_error": true, "content": "exit status 1: command not found"},
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

func TestImportRecordsSessionStatsTagsAndSkipsUnchangedFilesAcrossRestart(t *testing.T) {
	ctx := context.Background()
	app, db := testApp(t)
	root := t.TempDir()
	writeNativeFixture(t, root, "cc-session-layer")

	config := history.Config{ClaudeRoot: root, ProjectRoot: root}
	first, err := app.ImportNativeHistory(ctx, config)
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if first.FilesSeen != 1 || first.FilesRead != 1 || first.FilesSkipped != 0 || first.SessionsIngested != 1 {
		t.Fatalf("first import report = %+v", first)
	}

	var eventCount, messageCount, toolCallCount, toolResultCount, frictionCount, toolErrorCount int
	var firstEventAt, lastEventAt, computedAt string
	if err := db.QueryRowContext(ctx, `
		SELECT event_count, message_count, tool_call_count, tool_result_count, friction_count,
		       tool_error_count, first_event_at, last_event_at, computed_at
		FROM session_stats`).
		Scan(&eventCount, &messageCount, &toolCallCount, &toolResultCount, &frictionCount,
			&toolErrorCount, &firstEventAt, &lastEventAt, &computedAt); err != nil {
		t.Fatalf("read session stats: %v", err)
	}
	if messageCount != 1 || toolCallCount != 1 || toolResultCount != 1 {
		t.Fatalf("transcript counts = messages %d, calls %d, results %d", messageCount, toolCallCount, toolResultCount)
	}
	if frictionCount != 1 || toolErrorCount != 1 {
		t.Fatalf("friction counts = %d / tool errors %d", frictionCount, toolErrorCount)
	}
	if eventCount < 4 || firstEventAt == "" || lastEventAt == "" || computedAt == "" {
		t.Fatalf("stats row = events %d, first %q, last %q, computed %q", eventCount, firstEventAt, lastEventAt, computedAt)
	}

	tags := map[string]string{}
	rows, err := db.QueryContext(ctx, `SELECT tag, kind FROM session_tags`)
	if err != nil {
		t.Fatalf("read session tags: %v", err)
	}
	for rows.Next() {
		var tag, kind string
		if err := rows.Scan(&tag, &kind); err != nil {
			t.Fatalf("scan session tag: %v", err)
		}
		tags[tag] = kind
	}
	rows.Close()
	if tags["implementation"] != "task" {
		t.Fatalf("rule tags = %+v, want implementation as a task tag", tags)
	}
	workspaceTags := 0
	for tag, kind := range tags {
		if kind == "workspace" && len(tag) > len("workspace-") {
			workspaceTags++
		}
	}
	if workspaceTags != 1 {
		t.Fatalf("workspace tags = %+v", tags)
	}

	var files int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_files WHERE session_id IS NOT NULL`).Scan(&files); err != nil {
		t.Fatalf("count native files: %v", err)
	}
	if files != 1 {
		t.Fatalf("native_files rows with a session = %d, want 1", files)
	}

	// A fresh App has an empty in-memory fingerprint map: only the persisted
	// rows can make the restart skip the file.
	restarted, _ := testApp(t)
	restarted.db = db
	restarted.registry = assets.New(db)
	if loaded, err := restarted.LoadNativeFiles(ctx); err != nil || loaded != 1 {
		t.Fatalf("LoadNativeFiles = %d, %v", loaded, err)
	}
	second, err := restarted.ImportNativeHistory(ctx, config)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if second.FilesSeen != 1 || second.FilesSkipped != 1 || second.FilesRead != 0 {
		t.Fatalf("restart report = %+v, want every unchanged file skipped", second)
	}
}

func TestEvaluateIncrementalSkipsWhenNoFactChanged(t *testing.T) {
	ctx := context.Background()
	app, db := testApp(t)
	registry := assets.New(db)
	firstSeen := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"alpha", "beta"} {
		assetID, err := registry.Register(ctx, assets.AssetInput{Kind: assets.KindSkill, Scope: assets.ScopeProject, Name: name, FirstSeenAt: firstSeen})
		if err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		if _, err := registry.RecordVersion(ctx, assets.VersionInput{AssetID: assetID, Content: []byte(name), ObservationLevel: canonical.LevelLoaded, ObservedAt: firstSeen}); err != nil {
			t.Fatalf("version %s: %v", name, err)
		}
	}

	asOf := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	report, _, err := app.EvaluateIncremental(ctx, asOf)
	if err != nil {
		t.Fatalf("first evaluation: %v", err)
	}
	if !report.Full || report.Evaluated != 2 {
		t.Fatalf("first evaluation report = %+v, want a full pass over both assets", report)
	}

	report, decisions, err := app.EvaluateIncremental(ctx, asOf.Add(time.Minute))
	if err != nil {
		t.Fatalf("second evaluation: %v", err)
	}
	if report.Full || report.Evaluated != 0 || report.Skipped != 2 || len(decisions) != 0 {
		t.Fatalf("second evaluation report = %+v, want the whole round skipped", report)
	}

	changedID, err := registry.Register(ctx, assets.AssetInput{Kind: assets.KindSkill, Scope: assets.ScopeProject, Name: "gamma", FirstSeenAt: firstSeen})
	if err != nil {
		t.Fatalf("register gamma: %v", err)
	}
	if _, err := registry.RecordVersion(ctx, assets.VersionInput{AssetID: changedID, Content: []byte("gamma"), ObservationLevel: canonical.LevelLoaded, ObservedAt: firstSeen}); err != nil {
		t.Fatalf("version gamma: %v", err)
	}
	report, decisions, err = app.EvaluateIncremental(ctx, asOf.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("third evaluation: %v", err)
	}
	if report.Full || report.Evaluated != 1 || report.Skipped != 2 || len(decisions) != 1 || decisions[0].AssetID != changedID {
		t.Fatalf("third evaluation report = %+v decisions = %+v", report, decisions)
	}
}
