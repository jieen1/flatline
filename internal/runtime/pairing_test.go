package runtime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"flatline/internal/history"
	"fmt"
	"os"
	"testing"

	"flatline/internal/storage"
)

// legacyCodexPayloads rewrites one Codex session's stored tool payloads into
// the shape an older parser left behind: the response item's own id in
// turn_id, and no call_id at all. That is the state a real database is in — a
// function_call keeps fc_… / ctc_… while its function_call_output keeps
// call_… / fco_… — and it is why the two cannot be matched on their ids.
func legacyCodexPayloads(t *testing.T, db *storage.DB, sessionID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		UPDATE events
		SET payload_json = json_set(
			json_remove(json_remove(payload_json, '$.call_id'), '$.tool_use_id'),
			'$.turn_id',
			replace(replace(json_extract(locator_json, '$.raw_ref'), ':tool_call', ''), ':tool_result', ''))
		WHERE session_id = ? AND event_type IN ('transcript_tool_call', 'transcript_tool_result')`,
		sessionID); err != nil {
		t.Fatalf("rewrite payloads to the legacy shape: %v", err)
	}
	// The derived rows those payloads produced go with them: this is a
	// database that never had the identity in the first place.
	if _, err := db.ExecContext(ctx, `DELETE FROM event_pairs WHERE session_id = ?`, sessionID); err != nil {
		t.Fatalf("clear pairs: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE friction_records SET tool_name = NULL, classifier_version = 'friction/1' WHERE session_id = ?`,
		sessionID); err != nil {
		t.Fatalf("clear friction tool names: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE session_commands SET exit_code = NULL, is_error = NULL WHERE session_id = ?`, sessionID); err != nil {
		t.Fatalf("clear command outcomes: %v", err)
	}
}

// eventDigest is the whole append-only table, in one comparable value: how
// many rows, and a hash over every id and payload. Anything the pairing pass
// wrote to an event would change it.
func eventDigest(t *testing.T, db *storage.DB) (int, string) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT id, event_type, COALESCE(payload_json, ''), COALESCE(locator_json, '')
		FROM events ORDER BY id`)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	defer rows.Close()
	hash := sha256.New()
	count := 0
	for rows.Next() {
		var id int64
		var eventType, payload, locator string
		if err := rows.Scan(&id, &eventType, &payload, &locator); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		fmt.Fprintf(hash, "%d\x1f%s\x1f%s\x1f%s\x1e", id, eventType, payload, locator)
		count++
	}
	return count, hex.EncodeToString(hash.Sum(nil))
}

func TestPairingRecoversToolIdentityWithoutWritingEvents(t *testing.T) {
	app, db := testApp(t)
	_ = importNativeFixtures(t, app)
	ctx := context.Background()
	const sessionID = "codex:codex-main-fixture"

	// Stamp every transcript as already read by this parser, so the pass's
	// step 0 (the versioned re-read, which would now also refresh the stored
	// payloads) skips and the test exercises the pair recovery itself — the
	// scenario it exists for is a database migrated from an older parser whose
	// stamps are current.
	if _, err := db.ExecContext(ctx, `UPDATE native_files SET parser_version = ?`, history.ParserVersion); err != nil {
		t.Fatalf("stamp transcripts: %v", err)
	}
	legacyCodexPayloads(t, db, sessionID)
	beforeCount, beforeDigest := eventDigest(t, db)

	report, err := app.BackfillEventPairs(ctx)
	if err != nil {
		t.Fatalf("pairing: %v", err)
	}
	if report.FilesRead == 0 || report.PairsWritten == 0 {
		t.Fatalf("pairing report = %+v, want the legacy transcript re-read", report)
	}

	afterCount, afterDigest := eventDigest(t, db)
	if afterCount != beforeCount || afterDigest != beforeDigest {
		t.Fatalf("the pairing pass wrote to events: %d/%s -> %d/%s", beforeCount, beforeDigest, afterCount, afterDigest)
	}

	var reparsed, named int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(NULLIF(TRIM(COALESCE(tool_name, '')), ''))
		FROM event_pairs WHERE session_id = ? AND pair_source = 'reparse'`, sessionID).
		Scan(&reparsed, &named); err != nil {
		t.Fatalf("read pairs: %v", err)
	}
	if reparsed < 2 || named < 2 {
		t.Fatalf("reparse pairs = %d (named %d), want the transcript's tool results", reparsed, named)
	}

	// The pass is stamped, so a second start does not read the file again.
	second, err := app.BackfillEventPairs(ctx)
	if err != nil {
		t.Fatalf("second pairing: %v", err)
	}
	if second.FilesRead != 0 {
		t.Fatalf("second pairing re-read %d files; the version stamp must retire them", second.FilesRead)
	}

	if _, err := app.RecomputeAllProjections(ctx); err != nil {
		t.Fatalf("recompute projections: %v", err)
	}
	if _, err := app.ReclassifyFriction(ctx); err != nil {
		t.Fatalf("reclassify: %v", err)
	}

	var withExit int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM session_commands WHERE session_id = ? AND exit_code IS NOT NULL`, sessionID).
		Scan(&withExit); err != nil {
		t.Fatalf("read commands: %v", err)
	}
	if withExit == 0 {
		t.Fatalf("no command recovered an exit code from the recovered pairs")
	}

	var unnamed int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM friction_records
		WHERE session_id = ? AND NULLIF(TRIM(COALESCE(tool_name, '')), '') IS NULL`, sessionID).Scan(&unnamed); err != nil {
		t.Fatalf("read friction: %v", err)
	}
	if unnamed != 0 {
		t.Fatalf("%d friction rows still have no tool name after pairing", unnamed)
	}

	var calls, known, failures int
	if err := db.QueryRowContext(ctx, `
		SELECT calls, known_outcomes, failures FROM tool_call_stats
		WHERE session_id = ? AND tool_name = 'exec_command'`, sessionID).Scan(&calls, &known, &failures); err != nil {
		t.Fatalf("read tool stats: %v", err)
	}
	if calls == 0 || known != calls || failures == 0 {
		t.Fatalf("exec_command stats = calls %d known %d failures %d", calls, known, failures)
	}
}

func TestPairingLeavesASessionWhoseTranscriptIsGoneUnpaired(t *testing.T) {
	app, db := testApp(t)
	config := importNativeFixtures(t, app)
	ctx := context.Background()
	const sessionID = "codex:codex-main-fixture"

	legacyCodexPayloads(t, db, sessionID)

	var path string
	if err := db.QueryRowContext(ctx, `SELECT path FROM native_files WHERE session_id = ?`, sessionID).Scan(&path); err != nil {
		t.Fatalf("read native file: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove transcript: %v", err)
	}
	_ = config

	report, err := app.BackfillEventPairs(ctx)
	if err != nil {
		t.Fatalf("pairing without the transcript: %v", err)
	}
	if report.FilesMissing == 0 || report.FilesRead != 0 {
		t.Fatalf("pairing report = %+v, want the missing transcript counted and nothing read", report)
	}

	var pairs int
	var version sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM event_pairs WHERE session_id = ?),
		       (SELECT pairing_version FROM native_files WHERE path = ?)`, sessionID, path).
		Scan(&pairs, &version); err != nil {
		t.Fatalf("read pairing state: %v", err)
	}
	if pairs != 0 {
		t.Fatalf("session with no transcript got %d pairs", pairs)
	}
	if version.Valid {
		t.Fatalf("a transcript that could not be read was stamped %q", version.String)
	}
}

func TestCodexTurnAbortedIsRecordedAsUserInterrupt(t *testing.T) {
	app, db := testApp(t)
	_ = importNativeFixtures(t, app)

	var kind, category, rule, signature string
	if err := db.QueryRowContext(context.Background(), `
		SELECT friction_kind, COALESCE(category, ''), COALESCE(category_rule, ''), COALESCE(signature, '')
		FROM friction_records
		WHERE session_id = 'codex:codex-subagent-fixture' AND friction_kind = 'user_interrupt'`).
		Scan(&kind, &category, &rule, &signature); err != nil {
		t.Fatalf("read codex interrupt: %v", err)
	}
	if category != "user_interrupt" || signature != "user_interrupt||interrupted" {
		t.Fatalf("codex interrupt = category %q signature %q", category, signature)
	}
	if rule == "" {
		t.Fatalf("codex interrupt has no one-line rule")
	}
}
