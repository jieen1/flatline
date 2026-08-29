package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The briefing (P18-4) is the corpus feeding the next session: one document
// an agent fetches at task start, assembled entirely from the refined layers
// — working commands, recurring friction with mechanism and mined endings,
// hot files — every line factual and corroboration-counted.
func TestProjectBriefingComposesTheRefinedLayers(t *testing.T) {
	db, ids := knowledgeFixtureDB(t)
	// Add recurring friction with a dictionary mechanism and an ending.
	base := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	hit := func(id, eventID string, at time.Time) {
		stamp := at.Format(time.RFC3339Nano)
		exec(t, db, `INSERT INTO friction_records
			(session_id, source_event_id, friction_kind, event_type, observation_level, tool_name, category,
			 classifier_version, signature, payload_json, locator_json, occurred_at, created_at)
			VALUES (?, ?, 'tool_error', 'transcript_tool_result', 'invoked', 'Edit', 'tool_error',
			        'friction/test', 'tool_error|Edit|<tool_use_error>file has not been read yet. read it first…', '{}', '{}', ?, ?)`,
			id, eventID, stamp, stamp)
	}
	hit(ids["one"], "brf-1", base.Add(30*time.Minute))
	hit(ids["two"], "brf-2", base.Add(150*time.Minute))
	exec(t, db, `INSERT INTO session_files (session_id, event_id, path, action, tool_name, occurred_at) VALUES
		(?, 9501, '/synthetic/project-knowledge/STATE.md', 'read', 'Read', ?),
		(?, 9502, '/synthetic/project-knowledge/STATE.md', 'read', 'Read', ?),
		(?, 9503, '/synthetic/project-knowledge/STATE.md', 'read', 'Read', ?)`,
		ids["one"], base.Add(31*time.Minute).Format(time.RFC3339Nano),
		ids["one"], base.Add(32*time.Minute).Format(time.RFC3339Nano),
		ids["two"], base.Add(151*time.Minute).Format(time.RFC3339Nano))

	handler := NewServerWithDB(db).Handler()
	record := getJSON(t, handler, "/api/v1/projects/"+url.QueryEscape(knowledgeProject)+"/briefing?format=json", nil)
	body := record.Body.String()
	for _, want := range []string{
		`"working_commands"`, `"recurring"`, `"hot_files"`, `"note"`,
		"just ci-fast",
		"file has not been read yet",
		"Claude Code 要求", // the dictionary mechanism rides along
		"STATE.md",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("briefing json misses %q", want)
		}
	}

	markdown := getRawBody(t, handler, "/api/v1/projects/"+url.QueryEscape(knowledgeProject)+"/briefing")
	for _, want := range []string{
		"# 项目开工简报",
		"## 怎么干活",
		"just ci-fast",
		"2 个会话",
		"## 这里常撞的",
		"file has not been read yet",
		"## 状态在哪些文件里",
		"STATE.md",
		"只陈述", // the non-causal criterion is part of the document
	} {
		if !strings.Contains(markdown, want) {
			t.Errorf("briefing markdown misses %q\n----\n%s", want, markdown)
		}
	}
}

// The dominant fleet workflow spawns fresh worktrees (/tmp/cog-843-dev held
// 62 sessions with no stored link to its repo), so the thin briefing lists
// the projects that do have history: the agent knows its own git remote and
// can fetch the right one — facts from the system, judgement with the reader.
func TestProjectBriefingOfAnUnknownProjectSaysSoAndOffersTheIndex(t *testing.T) {
	db, _ := knowledgeFixtureDB(t)
	handler := NewServerWithDB(db).Handler()
	markdown := getRawBody(t, handler, "/api/v1/projects/"+url.QueryEscape("/synthetic/never-seen")+"/briefing")
	if !strings.Contains(markdown, "没有") && !strings.Contains(markdown, "尚无") {
		t.Fatalf("empty briefing does not say the history is empty:\n%s", markdown)
	}
	if !strings.Contains(markdown, knowledgeProject) {
		t.Fatalf("empty briefing does not index the projects that have history:\n%s", markdown)
	}
	if !strings.Contains(markdown, "新工作树") {
		t.Fatalf("the index does not say what it is for:\n%s", markdown)
	}
}

func getRawBody(t *testing.T, handler http.Handler, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, body=%s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}
