package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"flatline/internal/storage"
)

// ruleLoopFixtureDB seeds one recurring signature across two sessions, with
// payloads that carry a readable tool output line for the brief.
func ruleLoopFixtureDB(t *testing.T) (*storage.DB, string) {
	t.Helper()
	db := testAPIDB(t)
	base := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	signature := "tool_error|exec|apply_patch verification failed: failed to find expected lines"
	for index, sessionID := range []string{"opencode:ses_loop_a", "opencode:ses_loop_b"} {
		started := base.AddDate(0, 0, index)
		exec(t, db, `INSERT INTO sessions
			(id, source, source_session_id, started_at, ended_at, thread_kind, title, project_key)
			VALUES (?, 'opencode', ?, ?, ?, 'main', ?, '/synthetic/loop')`,
			sessionID, "loop-"+string(rune('a'+index)), started.Format(time.RFC3339Nano),
			started.Add(time.Hour).Format(time.RFC3339Nano), "循环夹具 "+string(rune('a'+index)))
		for record := 0; record < 3; record++ {
			at := started.Add(time.Duration(record) * 10 * time.Minute)
			payload := `{"tool_output":"apply_patch verification failed: failed to find expected lines in app.js","tool_input":"{\"patch\":\"*** Update File: app.js\"}"}`
			exec(t, db, `INSERT INTO friction_records
				(session_id, source_event_id, friction_kind, event_type, observation_level, tool_name, category,
				 category_rule, category_rule_en, classifier_version, signature, is_error, payload_json, locator_json, occurred_at, created_at)
				VALUES (?, ?, 'tool_error', 'transcript_tool_result', 'invoked', 'exec', 'tool_error',
				        '补丁上下文对不上', 'patch context mismatch', 'friction/test', ?, 1, ?, '{}', ?, ?)`,
				sessionID, "loop-"+string(rune('a'+index))+"-"+string(rune('0'+record)),
				signature, payload, at.Format(time.RFC3339Nano), at.Format(time.RFC3339Nano))
		}
	}
	return db, signature
}

func TestFrictionSignatureGroupsCarryRuleBriefs(t *testing.T) {
	db, signature := ruleLoopFixtureDB(t)
	handler := NewServerWithDB(db).Handler()
	var response struct {
		Groups []struct {
			Signature string `json:"signature"`
			Brief     *struct {
				Mechanism *struct{ Kind string } `json:"mechanism"`
				Target    struct {
					Kind   string `json:"kind"`
					Reason string `json:"reason"`
				} `json:"target"`
				Evidence struct {
					Count        int      `json:"count"`
					SessionCount int      `json:"session_count"`
					SampleLines  []string `json:"sample_lines"`
					TopProjects  []struct {
						Key   string `json:"key"`
						Count int    `json:"count"`
					} `json:"top_projects"`
				} `json:"evidence"`
				PastePrompt   string `json:"paste_prompt"`
				PastePromptEN string `json:"paste_prompt_en"`
				Criterion     string `json:"criterion"`
			} `json:"brief"`
		} `json:"groups"`
	}
	getJSON(t, handler, "/api/v1/friction?group=signature&from=all", &response)
	var found *struct {
		Signature string `json:"signature"`
		Brief     *struct {
			Mechanism *struct{ Kind string } `json:"mechanism"`
			Target    struct {
				Kind   string `json:"kind"`
				Reason string `json:"reason"`
			} `json:"target"`
			Evidence struct {
				Count        int      `json:"count"`
				SessionCount int      `json:"session_count"`
				SampleLines  []string `json:"sample_lines"`
				TopProjects  []struct {
					Key   string `json:"key"`
					Count int    `json:"count"`
				} `json:"top_projects"`
			} `json:"evidence"`
			PastePrompt   string `json:"paste_prompt"`
			PastePromptEN string `json:"paste_prompt_en"`
			Criterion     string `json:"criterion"`
		} `json:"brief"`
	}
	for index := range response.Groups {
		if response.Groups[index].Signature == signature {
			found = &response.Groups[index]
			break
		}
	}
	if found == nil || found.Brief == nil {
		t.Fatalf("signature group carries no brief; groups=%d", len(response.Groups))
	}
	if found.Brief.Evidence.Count != 6 || found.Brief.Evidence.SessionCount != 2 {
		t.Errorf("brief evidence = %d/%d, want 6 events in 2 sessions",
			found.Brief.Evidence.Count, found.Brief.Evidence.SessionCount)
	}
	if len(found.Brief.Evidence.SampleLines) == 0 ||
		!strings.Contains(found.Brief.Evidence.SampleLines[0], "apply_patch verification failed") {
		t.Errorf("brief sample lines missing the recorded output: %v", found.Brief.Evidence.SampleLines)
	}
	if found.Brief.Target.Kind != "rule" {
		t.Errorf("target kind = %q, want rule for a tool_misuse-style mechanism", found.Brief.Target.Kind)
	}
	if !strings.Contains(found.Brief.PastePrompt, signature) ||
		!strings.Contains(found.Brief.PastePrompt, "2 个会话") {
		t.Errorf("paste prompt missing signature or session count: %.120s", found.Brief.PastePrompt)
	}
	if found.Brief.PastePromptEN == "" || found.Brief.Criterion == "" {
		t.Errorf("brief must carry both languages and its criterion")
	}
}

func TestSignatureWatchRequiresExplicitConfirmation(t *testing.T) {
	db, signature := ruleLoopFixtureDB(t)
	handler := NewServerWithDB(db).Handler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/signature-watches",
		strings.NewReader(`{"signature":"x"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("watch without confirmed = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "confirmed=true") {
		t.Errorf("rejection must name the confirmation rule: %s", rec.Body.String())
	}
	_ = signature
}

func TestSignatureWatchVerifiesOnlyWhenProjectsStillRun(t *testing.T) {
	db, signature := ruleLoopFixtureDB(t)
	handler := NewServerWithDB(db).Handler()

	// Create the watch with a 1-day window…
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/signature-watches",
		strings.NewReader(`{"signature":"`+signature+`","confirmed":true,"window_days":1}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("create watch = %d, body=%s", rec.Code, rec.Body.String())
	}
	// …then backdate it so a full window has passed.
	exec(t, db, `UPDATE signature_watches SET created_at = ?`,
		time.Now().UTC().AddDate(0, 0, -3).Format(time.RFC3339Nano))

	var list struct {
		Watches []struct {
			Status     string `json:"status"`
			WindowDays int    `json:"window_days"`
			Evaluation *struct {
				Status          string `json:"status"`
				PostCount       int    `json:"post_count"`
				WindowCount     int    `json:"window_count"`
				ProjectSessions int    `json:"project_sessions_in_window"`
			} `json:"evaluation"`
		} `json:"watches"`
	}
	getJSON(t, handler, "/api/v1/signature-watches", &list)
	if len(list.Watches) != 1 {
		t.Fatalf("watches = %d, want 1", len(list.Watches))
	}
	watch := list.Watches[0]
	// The fixture sessions are three days old, so the watched project had no
	// session inside the one-day window: unobservable, not verified.
	if watch.Evaluation.Status != "unobservable" {
		t.Errorf("status = %q with no recent project sessions, want unobservable", watch.Evaluation.Status)
	}
	if watch.Evaluation.PostCount != 0 {
		t.Errorf("post count = %d, want 0 (no records after the watch)", watch.Evaluation.PostCount)
	}

	// A session in the watched project inside the window makes the quiet
	// signature verifiable: quiet + project ran => verified.
	now := time.Now().UTC()
	exec(t, db, `INSERT INTO sessions
		(id, source, source_session_id, started_at, ended_at, thread_kind, title, project_key)
		VALUES ('opencode:ses_loop_now', 'opencode', 'loop-now', ?, ?, 'main', '新会话', '/synthetic/loop')`,
		now.Add(-time.Hour).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	getJSON(t, handler, "/api/v1/signature-watches", &list)
	if list.Watches[0].Evaluation.Status != "verified" {
		t.Errorf("status = %q after quiet window with live project, want verified", list.Watches[0].Evaluation.Status)
	}

	// A new occurrence after verification flips the watch back to no_change:
	// the loop never closes permanently, the facts decide.
	at := now.Add(-30 * time.Minute).Format(time.RFC3339Nano)
	exec(t, db, `INSERT INTO friction_records
		(session_id, source_event_id, friction_kind, event_type, observation_level, tool_name, category,
		 category_rule, category_rule_en, classifier_version, signature, is_error, payload_json, locator_json, occurred_at, created_at)
		VALUES ('opencode:ses_loop_now', 'loop-relapse', 'tool_error', 'transcript_tool_result', 'invoked', 'exec', 'tool_error',
		        '补丁上下文对不上', 'patch context mismatch', 'friction/test', ?, 1, '{}', '{}', ?, ?)`,
		signature, at, at)
	getJSON(t, handler, "/api/v1/signature-watches", &list)
	if list.Watches[0].Evaluation.Status != "no_change" {
		t.Errorf("status = %q after a fresh occurrence, want no_change", list.Watches[0].Evaluation.Status)
	}

	// Cancelling needs confirmation too, then keeps the row.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/signature-watches/cancel",
		strings.NewReader(`{"id":1}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("cancel without confirmed = %d, want 400", rec.Code)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/signature-watches/cancel",
		strings.NewReader(`{"id":1,"confirmed":true}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d, body=%s", rec.Code, rec.Body.String())
	}
	getJSON(t, handler, "/api/v1/signature-watches", &list)
	if list.Watches[0].Status != "cancelled" {
		t.Errorf("status = %q, want cancelled (row kept)", list.Watches[0].Status)
	}
}

func TestFrictionSignatureRowCarriesWatchBadge(t *testing.T) {
	db, signature := ruleLoopFixtureDB(t)
	handler := NewServerWithDB(db).Handler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/signature-watches",
		strings.NewReader(`{"signature":"`+signature+`","confirmed":true,"window_days":14}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("create watch = %d", rec.Code)
	}
	var response struct {
		Groups []struct {
			Signature string `json:"signature"`
			Watch     *struct {
				Status     string `json:"status"`
				WindowDays int    `json:"window_days"`
			} `json:"watch"`
		} `json:"groups"`
	}
	getJSON(t, handler, "/api/v1/friction?group=signature&from=all", &response)
	for _, group := range response.Groups {
		if group.Signature == signature {
			if group.Watch == nil {
				t.Fatal("signature row carries no watch badge")
			}
			if group.Watch.Status != "watching" || group.Watch.WindowDays != 14 {
				t.Errorf("watch badge = %+v", group.Watch)
			}
			return
		}
	}
	t.Fatalf("signature group not found among %d groups", len(response.Groups))
}

// TestWatchVerdictAppearsInNotifications pins the second notification source
// (ADR-24): a settled fix verification is a notification with the signature as
// its drill-down, deduped per (watch, status), and a cancelled watch withdraws
// its verdict.
func TestWatchVerdictAppearsInNotifications(t *testing.T) {
	db, signature := ruleLoopFixtureDB(t)
	handler := NewServerWithDB(db).Handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/signature-watches",
		strings.NewReader(`{"signature":"`+signature+`","confirmed":true,"window_days":1}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("create watch = %d", rec.Code)
	}
	// Backdate the watch and give the watched project a live session, so the
	// verdict is "verified" rather than "unobservable".
	exec(t, db, `UPDATE signature_watches SET created_at = ?`,
		time.Now().UTC().AddDate(0, 0, -3).Format(time.RFC3339Nano))
	now := time.Now().UTC()
	exec(t, db, `INSERT INTO sessions
		(id, source, source_session_id, started_at, ended_at, thread_kind, title, project_key)
		VALUES ('opencode:ses-verdict-live', 'opencode', 'verdict-live', ?, ?, 'main', '判定夹具', '/synthetic/loop')`,
		now.Add(-time.Hour).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))

	var page struct {
		Notifications []notificationResponse `json:"notifications"`
	}
	getJSON(t, handler, "/api/v1/notifications", &page)
	var verdict *notificationResponse
	for index := range page.Notifications {
		if page.Notifications[index].Kind == "watch_verified" {
			verdict = &page.Notifications[index]
			break
		}
	}
	if verdict == nil {
		t.Fatalf("no watch_verified notification among %d", len(page.Notifications))
	}
	if verdict.WatchID == nil || *verdict.WatchID != 1 {
		t.Errorf("verdict watch id = %v, want 1", verdict.WatchID)
	}
	if verdict.Signature != signature {
		t.Errorf("verdict signature = %q, want the watched signature", verdict.Signature)
	}
	if verdict.ID >= 0 {
		t.Errorf("verdict id = %d, want negative (outside the transition id space)", verdict.ID)
	}
	if verdict.SummaryEN == "" || verdict.Summary == "" {
		t.Errorf("verdict carries only one language: %q / %q", verdict.Summary, verdict.SummaryEN)
	}

	// Cancelling the watch withdraws the verdict from the projection.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/signature-watches/cancel",
		strings.NewReader(`{"id":1,"confirmed":true}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d", rec.Code)
	}
	getJSON(t, handler, "/api/v1/notifications", &page)
	for index := range page.Notifications {
		if page.Notifications[index].Kind == "watch_verified" {
			t.Fatal("cancelled watch still projects a verdict")
		}
	}
}
