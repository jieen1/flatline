package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/eventstore"
	"flatline/internal/storage"
)

const displayProject = "/synthetic/project-display"

// displayFixtureDB is four sessions covering every source a row's name can
// come from, plus the subagent whose name has to be built.
func displayFixtureDB(t *testing.T) (*storage.DB, map[string]string) {
	t.Helper()
	db := testAPIDB(t)
	ctx := context.Background()
	store := eventstore.New(db)
	at := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)

	ids := make(map[string]string, 4)
	add := func(key, sourceID, title, taskText string) {
		id, err := store.IngestSession(ctx, adapters.SourceCodex, adapters.SessionMeta{
			SourceSessionID: sourceID, StartedAt: &at, CWD: displayProject,
			Title: title, TaskText: taskText,
		})
		if err != nil {
			t.Fatalf("ingest session %s: %v", sourceID, err)
		}
		if err := store.RecomputeSessionStats(ctx, id); err != nil {
			t.Fatalf("recompute stats %s: %v", sourceID, err)
		}
		ids[key] = id
	}
	add("titled", "disp-titled", "父会话标题：把命令投影接上摩擦签名", "")
	add("tasked", "disp-tasked", "", "只有任务文本没有标题的会话")
	add("subagent", "disp-subagent", "", "")
	add("bare", "disp-bare", "", "")

	exec(t, db, `UPDATE session_stats SET is_empty = 0`)
	exec(t, db, `UPDATE sessions SET thread_kind = 'main' WHERE id IN (?, ?, ?)`, ids["titled"], ids["tasked"], ids["bare"])
	exec(t, db, `UPDATE sessions SET thread_kind = 'subagent', parent_session_id = ?,
		agent_role = 'explore', agent_nickname = 'Ptolemy' WHERE id = ?`, ids["titled"], ids["subagent"])
	return db, ids
}

type displayRow struct {
	ID           string  `json:"id"`
	DisplayTitle *string `json:"display_title"`
	ParentTitle  *string `json:"parent_title"`
	TitleSource  string  `json:"title_source"`
}

func TestSessionDisplayTitleNamesItsSource(t *testing.T) {
	db, ids := displayFixtureDB(t)
	handler := NewServerWithDB(db).Handler()
	var list struct {
		Sessions []displayRow `json:"sessions"`
	}
	getJSON(t, handler, "/api/v1/sessions?thread=all&empty=all&limit=50", &list)
	byID := make(map[string]displayRow, len(list.Sessions))
	for _, row := range list.Sessions {
		byID[row.ID] = row
	}

	titled := byID[ids["titled"]]
	if titled.TitleSource != titleSourceAI || titled.DisplayTitle == nil {
		t.Errorf("titled session = %+v", titled)
	}
	tasked := byID[ids["tasked"]]
	if tasked.TitleSource != titleSourceTask || tasked.DisplayTitle == nil ||
		*tasked.DisplayTitle != "只有任务文本没有标题的会话" {
		t.Errorf("task-text session = %+v", tasked)
	}
	sub := byID[ids["subagent"]]
	if sub.TitleSource != titleSourceSynthesized || sub.DisplayTitle == nil {
		t.Fatalf("subagent session = %+v", sub)
	}
	// The name is only the name; what the parent thread is called is its own
	// field, so nothing decorative is spliced onto the end of it.
	if want := "explore · Ptolemy"; *sub.DisplayTitle != want {
		t.Errorf("synthesized display title = %q, want %q", *sub.DisplayTitle, want)
	}
	if sub.ParentTitle == nil || *sub.ParentTitle != "父会话标题：把命令投影接上摩擦签名" {
		t.Errorf("parent_title = %v", sub.ParentTitle)
	}
	bare := byID[ids["bare"]]
	if bare.TitleSource != titleSourceNone || bare.DisplayTitle != nil {
		t.Errorf("session with nothing recorded = %+v, want a null display title", bare)
	}

	// The detail response and the search results carry the same two fields.
	var detail struct {
		Session displayRow `json:"session"`
	}
	getJSON(t, handler, "/api/v1/sessions/"+ids["subagent"], &detail)
	if detail.Session.TitleSource != titleSourceSynthesized || detail.Session.DisplayTitle == nil {
		t.Errorf("session detail = %+v", detail.Session)
	}
	var search struct {
		Sessions []displayRow `json:"sessions"`
	}
	getJSON(t, handler, "/api/v1/search?q=只有任务文本", &search)
	if len(search.Sessions) == 0 || search.Sessions[0].TitleSource != titleSourceTask {
		t.Errorf("search results = %+v", search.Sessions)
	}
}

func TestSynthesizedTitleIsTheAgentIdentityAlone(t *testing.T) {
	value, source := sessionDisplayTitle(
		&sessionResponse{AgentRole: strPtr("architect"), AgentNickname: strPtr("Kepler")})
	if source != titleSourceSynthesized || value == nil || *value != "architect · Kepler" {
		t.Fatalf("synthesized = %v / %q", value, source)
	}
	value, source = sessionDisplayTitle(&sessionResponse{AgentRole: strPtr("architect")})
	if source != titleSourceSynthesized || value == nil || *value != "architect" {
		t.Errorf("subagent with no nickname = %v / %q", value, source)
	}
}

func TestIsHomeDirMarksTheHomeDirectoryAndItsBareChildren(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory in this environment")
	}
	if !isHomeDir(home) {
		t.Errorf("the home directory itself is not marked")
	}
	if isHomeDir(unrecordedKey) || isHomeDir("") {
		t.Errorf("an unrecorded working directory must not be marked")
	}
	if isHomeDir(filepath.Join(home, "a", "b")) {
		t.Errorf("a directory two levels below home must not be marked")
	}
	// A checkout directly under home is a project, not a home directory.
	checkout := filepath.Join(t.TempDir(), "checkout")
	if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if isHomeDir(checkout) {
		t.Errorf("a directory outside home must not be marked")
	}
}

// A harness-written wrapper is not a name. 65 local sessions carry a whole
// <teammate-message …> block as their recorded title; the row then reads as
// raw XML. The display name is derived from the block — the summary attribute
// when the tag has one, the first non-empty inner line otherwise — and the
// source honestly says the system built it (ADR-25).
func TestDisplayTitleUnwrapsHarnessWrappers(t *testing.T) {
	summaryWrapped := "<teammate-message teammate_id=\"team-lead\" summary=\"审计测试冗余\">\n你是 cognode 仓库的测试审计员。开始前先确认接单。\n</teammate-message>"
	value, source := sessionDisplayTitle(&sessionResponse{Title: strPtr(summaryWrapped)})
	if source != titleSourceSynthesized || value == nil || *value != "审计测试冗余" {
		t.Errorf("summary-attribute wrapper = %v / %q, want the summary attribute", value, source)
	}

	lineWrapped := "<teammate-message teammate_id=\"team-lead\">\n\nYou are **reviewer-1721** on the cognode sprint-61 executor team.\nMore lines follow.\n</teammate-message>"
	value, source = sessionDisplayTitle(&sessionResponse{Title: strPtr(lineWrapped)})
	if source != titleSourceSynthesized || value == nil || *value != "You are **reviewer-1721** on the cognode sprint-61 executor team." {
		t.Errorf("no-summary wrapper = %v / %q, want the first inner line", value, source)
	}

	// The same wrapper arriving as task text is unwrapped the same way.
	value, source = sessionDisplayTitle(&sessionResponse{TaskText: strPtr(summaryWrapped)})
	if source != titleSourceSynthesized || value == nil || *value != "审计测试冗余" {
		t.Errorf("wrapped task text = %v / %q", value, source)
	}

	// A title that merely mentions the tag is a real title and keeps its source.
	mention := "why does <teammate-message> appear in my counts?"
	value, source = sessionDisplayTitle(&sessionResponse{Title: strPtr(mention)})
	if source != titleSourceAI || value == nil || *value != mention {
		t.Errorf("mention = %v / %q, want the ai title untouched", value, source)
	}

	// <task> is genuine user content, not a harness wrapper; it stays as-is.
	task := "<task>Perform an adversarial architecture review.</task>"
	value, source = sessionDisplayTitle(&sessionResponse{Title: strPtr(task)})
	if source != titleSourceAI || value == nil || *value != task {
		t.Errorf("task-tag title = %v / %q, want untouched", value, source)
	}
}
