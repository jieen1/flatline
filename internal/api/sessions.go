package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"flatline/internal/canonical"
)

// transcriptIdleBound is how long a transcript has to go unwritten before the
// session that owns it stops being called in progress. It is the same ten
// minutes the active-time clock treats as a gap.
const transcriptIdleBound = 10 * time.Minute

type sessionTagResponse struct {
	Tag  string `json:"tag"`
	Kind string `json:"kind"`
}

type annotationResponse struct {
	Pinned    bool    `json:"pinned"`
	Note      *string `json:"note"`
	UpdatedAt *string `json:"updated_at"`
}

type sessionResponse struct {
	ID              string  `json:"id"`
	Source          string  `json:"source"`
	SourceSessionID string  `json:"source_session_id"`
	Title           *string `json:"title,omitempty"`
	TaskText        *string `json:"task_text,omitempty"`
	DisplayTitle    *string `json:"display_title"`
	TitleSource     string  `json:"title_source"`
	// ParentTitle is what the parent thread is called, for a row whose own
	// name had to be built from the agent's identity. It is a separate field
	// so the name itself stays a name.
	ParentTitle    *string    `json:"parent_title"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
	HarnessVersion *string    `json:"harness_version,omitempty"`
	Model          *string    `json:"model,omitempty"`
	CWD            *string    `json:"cwd,omitempty"`
	ProjectKey     string     `json:"project_key"`
	ProjectLabel   string     `json:"project_label"`
	// Worktree is the throwaway checkout this session ran in, when the harness
	// made one. The session is counted under the repository that owns it; this
	// says where inside it the work happened.
	Worktree *string `json:"worktree"`
	// SourceLabel and MachineLabel name the configured root this session was
	// read from. Both are null when the session predates the registry or its
	// transcript is under no configured root — which is "not recorded", not
	// "this machine".
	SourceLabel  *string `json:"source_label"`
	MachineLabel *string `json:"machine_label"`
	// InProgress marks a session whose transcript was written to in the last
	// few minutes. Its numbers are a reading of a file that is still growing:
	// the turn counts, the token totals and the active time will all be larger
	// the next time it is read.
	InProgress       bool                 `json:"in_progress"`
	EventCount       int                  `json:"event_count"`
	TranscriptCount  int                  `json:"transcript_count"`
	MessageCount     int                  `json:"message_count"`
	UserMessageCount int                  `json:"user_message_count"`
	ToolCallCount    int                  `json:"tool_call_count"`
	ToolResultCount  int                  `json:"tool_result_count"`
	FrictionCount    int                  `json:"friction_count"`
	ToolErrorCount   int                  `json:"tool_error_count"`
	NonzeroExitCount int                  `json:"nonzero_exit_count"`
	AssetCount       int                  `json:"asset_count"`
	DurationMS       *int64               `json:"duration_ms"`
	ThreadKind       *string              `json:"thread_kind"`
	ParentSessionID  *string              `json:"parent_session_id"`
	AgentRole        *string              `json:"agent_role"`
	AgentNickname    *string              `json:"agent_nickname"`
	Originator       *string              `json:"originator"`
	SubagentCount    int                  `json:"subagent_count"`
	CommandCount     int                  `json:"command_count"`
	FailedCommands   int                  `json:"failed_command_count"`
	FileCount        int                  `json:"file_count"`
	IsEmpty          bool                 `json:"is_empty"`
	Tags             []sessionTagResponse `json:"tags"`
	Pinned           bool                 `json:"pinned"`
	NotePreview      *string              `json:"note_preview"`
	MatchCount       *int                 `json:"match_count"`
	MatchSnippet     *string              `json:"match_snippet"`
	Annotation       *annotationResponse  `json:"annotation,omitempty"`
	// ExpectedExitCount is how many of this session's recorded nonzero exits
	// the classifier reads as an answer rather than a failure; they are left
	// out of FrictionCount and counted here instead.
	ExpectedExitCount int `json:"expected_exit_count"`
	// Usage is what the session cost and changed. Its fields are null when the
	// source did not record them; source = "unrecorded" means no measurement
	// row exists for this session at all.
	Usage usageResponse `json:"usage"`
}

const sessionColumns = `s.id, s.source, s.source_session_id, s.title, s.task_text, s.started_at, s.ended_at,
	s.harness_version, s.model, s.cwd, s.project_key, s.worktree,
	COALESCE(st.event_count, 0), COALESCE(st.transcript_count, 0), COALESCE(st.message_count, 0),
	COALESCE(st.user_message_count, 0), COALESCE(st.tool_call_count, 0), COALESCE(st.tool_result_count, 0),
	COALESCE(st.friction_count, 0), COALESCE(st.tool_error_count, 0), COALESCE(st.nonzero_exit_count, 0),
	COALESCE(st.asset_count, 0), st.duration_ms,
	s.thread_kind, s.parent_session_id, s.agent_role, s.agent_nickname, s.originator,
	COALESCE(st.subagent_count, 0), COALESCE(st.command_count, 0), COALESCE(st.failed_command_count, 0),
	COALESCE(st.file_count, 0), COALESCE(st.is_empty, 0), COALESCE(st.expected_exit_count, 0),
	COALESCE(an.pinned, 0), an.note, p.title, p.task_text,
	src.label, src.machine_label, nf.mtime_ns,
	` + usageColumns

const sessionFrom = ` FROM sessions s
	LEFT JOIN session_stats st ON st.session_id = s.id
	LEFT JOIN session_annotations an ON an.session_id = s.id
	LEFT JOIN session_usage u ON u.session_id = s.id
	LEFT JOIN sessions p ON p.id = s.parent_session_id
	LEFT JOIN sources src ON src.id = s.source_id
	LEFT JOIN (SELECT session_id, MAX(mtime_ns) AS mtime_ns FROM native_files GROUP BY session_id) nf
		ON nf.session_id = s.id`

func scanSession(row interface{ Scan(...any) error }) (*sessionResponse, error) {
	var item sessionResponse
	var title, taskText, started, ended, harness, model, cwd, note sql.NullString
	var projectKey, worktree sql.NullString
	var threadKind, parentID, agentRole, agentNickname, originator sql.NullString
	var parentTitle, parentTask, sourceLabel, machineLabel sql.NullString
	var transcriptMTime sql.NullInt64
	var duration sql.NullInt64
	var pinned, isEmpty int
	usageValues := make([]sql.NullInt64, usageValueCount)
	var usageCost sql.NullFloat64
	var usageSource sql.NullString
	scan := []any{&item.ID, &item.Source, &item.SourceSessionID, &title, &taskText, &started, &ended,
		&harness, &model, &cwd, &projectKey, &worktree,
		&item.EventCount, &item.TranscriptCount, &item.MessageCount, &item.UserMessageCount,
		&item.ToolCallCount, &item.ToolResultCount, &item.FrictionCount, &item.ToolErrorCount,
		&item.NonzeroExitCount, &item.AssetCount, &duration,
		&threadKind, &parentID, &agentRole, &agentNickname, &originator,
		&item.SubagentCount, &item.CommandCount, &item.FailedCommands, &item.FileCount, &isEmpty,
		&item.ExpectedExitCount, &pinned, &note, &parentTitle, &parentTask, &sourceLabel, &machineLabel, &transcriptMTime}
	scan = append(scan, usageScanTargets(usageValues, &usageCost, &usageSource)...)
	if err := row.Scan(scan...); err != nil {
		return nil, err
	}
	item.Usage = scanUsage(usageValues, usageCost, usageSource)
	item.ThreadKind = optionalString(threadKind)
	item.ParentSessionID = optionalString(parentID)
	item.AgentRole = optionalString(agentRole)
	item.AgentNickname = optionalString(agentNickname)
	item.Originator = optionalString(originator)
	item.IsEmpty = isEmpty != 0
	if title.Valid {
		item.Title = &title.String
	}
	if taskText.Valid {
		item.TaskText = &taskText.String
	}
	var err error
	if item.StartedAt, err = optionalTime(started); err != nil {
		return nil, err
	}
	if item.EndedAt, err = optionalTime(ended); err != nil {
		return nil, err
	}
	if harness.Valid {
		item.HarnessVersion = &harness.String
	}
	if model.Valid {
		item.Model = &model.String
	}
	if cwd.Valid {
		item.CWD = &cwd.String
	}
	item.ProjectKey = projectKeyOf(projectKey)
	item.ProjectLabel = projectLabelOf(item.ProjectKey)
	item.Worktree = optionalString(worktree)
	item.SourceLabel, item.MachineLabel = optionalString(sourceLabel), optionalString(machineLabel)
	item.InProgress = transcriptMTime.Valid &&
		time.Since(time.Unix(0, transcriptMTime.Int64)) < transcriptIdleBound
	if duration.Valid {
		value := duration.Int64
		item.DurationMS = &value
	}
	item.Pinned = pinned != 0
	if note.Valid && strings.TrimSpace(note.String) != "" {
		preview := boundRunes(note.String, 80)
		item.NotePreview = &preview
	}
	item.Tags = make([]sessionTagResponse, 0)
	item.DisplayTitle, item.TitleSource = sessionDisplayTitle(&item)
	if parent := firstRecorded(parentTitle, parentTask); parent != "" {
		bounded := boundRunes(parent, parentTitleBound)
		item.ParentTitle = &bounded
	}
	return &item, nil
}

// Where the name on a session row comes from.
const (
	titleSourceAI          = "ai"          // the harness wrote a title
	titleSourceTask        = "task"        // no title; the first user message stands in
	titleSourceSynthesized = "synthesized" // no text at all; built from the agent's own role
	titleSourceNone        = "none"        // nothing recorded — the UI shows 未记录
)

// parentTitleBound keeps the parent name short enough to sit beside a row.
const parentTitleBound = 60

// sessionDisplayTitle picks the one name a row shows, and says where it came
// from so the UI can mark a name it built itself. Nothing is invented: a
// session with no title, no task text and no agent identity returns nil, which
// reads as "not recorded" rather than as an empty string.
//
// The name is only a name. What the parent thread is called goes into
// parent_title, so nothing has to be spliced onto the end of it.
func sessionDisplayTitle(item *sessionResponse) (*string, string) {
	if value := trimmedValue(item.Title); value != "" {
		if inner, ok := unwrapInjectedTitle(value); ok {
			bounded := boundRunes(inner, maxSessionTitleRunes)
			return &bounded, titleSourceSynthesized
		}
		return &value, titleSourceAI
	}
	if value := trimmedValue(item.TaskText); value != "" {
		if inner, ok := unwrapInjectedTitle(value); ok {
			bounded := boundRunes(inner, maxSessionTitleRunes)
			return &bounded, titleSourceSynthesized
		}
		bounded := boundRunes(value, maxSessionTitleRunes)
		return &bounded, titleSourceTask
	}
	parts := make([]string, 0, 2)
	for _, field := range []*string{item.AgentRole, item.AgentNickname} {
		if value := trimmedValue(field); value != "" {
			parts = append(parts, value)
		}
	}
	if len(parts) == 0 {
		return nil, titleSourceNone
	}
	value := strings.Join(parts, " · ")
	return &value, titleSourceSynthesized
}

const maxSessionTitleRunes = 120

// unwrapInjectedTitle derives a display name from a harness-written block that
// was recorded as a session's title or task text — on this machine, whole
// <teammate-message …> blocks stand as the title of 65 sessions. The name is
// the tag's summary attribute when it has one, else the first non-empty line
// inside the block. The second return is false for anything that is not one of
// the known injected blocks, including genuine user text such as <task>…: the
// closed list in canonical.InjectedMessagePrefixes decides, nothing is guessed.
func unwrapInjectedTitle(value string) (string, bool) {
	if canonical.InjectedMessagePrefix(value) == "" {
		return "", false
	}
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "<") {
		return "", false
	}
	end := strings.Index(value, ">")
	if end < 0 {
		return "", false
	}
	openTag, body := value[:end+1], value[end+1:]
	if summary := tagAttribute(openTag, "summary"); summary != "" {
		return summary, true
	}
	// Drop the closing tag so the last line is content, not markup.
	if name, _, ok := strings.Cut(strings.TrimPrefix(openTag, "<"), " "); ok || name != "" {
		name = strings.TrimSuffix(name, ">")
		if close := strings.LastIndex(body, "</"+name+">"); close >= 0 {
			body = body[:close]
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line, true
		}
	}
	return "", false
}

// tagAttribute reads one double-quoted attribute from an opening tag. The
// blocks it reads are machine-written, so the quoting is regular.
func tagAttribute(openTag, name string) string {
	_, after, found := strings.Cut(openTag, name+`="`)
	if !found {
		return ""
	}
	attr, _, found := strings.Cut(after, `"`)
	if !found {
		return ""
	}
	return strings.TrimSpace(attr)
}

func trimmedValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func firstRecorded(values ...sql.NullString) string {
	for _, value := range values {
		if value.Valid && strings.TrimSpace(value.String) != "" {
			return strings.TrimSpace(value.String)
		}
	}
	return ""
}

func optionalString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func optionalTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func boundRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

// sessionCondition carries the dimension it filters on, so a facet count can
// leave out its own filter and still honour every other one.
type sessionCondition struct {
	dimension string
	expr      string
	args      []any
}

type sessionQuery struct {
	conditions []sessionCondition
	sortKey    string
	limit      int
	offset     int
	text       string
	deep       bool
	ftsMatch   string
}

func (q sessionQuery) where(exclude ...string) (string, []any) {
	skip := make(map[string]struct{}, len(exclude))
	for _, dimension := range exclude {
		skip[dimension] = struct{}{}
	}
	parts := make([]string, 0, len(q.conditions))
	args := make([]any, 0)
	for _, condition := range q.conditions {
		if _, ok := skip[condition.dimension]; ok {
			continue
		}
		parts = append(parts, condition.expr)
		args = append(args, condition.args...)
	}
	if len(parts) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(parts, " AND "), args
}

var sessionSortOrder = map[string]string{
	"recent":     " ORDER BY s.started_at IS NULL, s.started_at DESC, s.id DESC",
	"oldest":     " ORDER BY s.started_at IS NULL, s.started_at ASC, s.id ASC",
	"duration":   " ORDER BY st.duration_ms IS NULL, st.duration_ms DESC, s.id DESC",
	"events":     " ORDER BY COALESCE(st.event_count, 0) DESC, s.id DESC",
	"friction":   " ORDER BY COALESCE(st.friction_count, 0) DESC, s.id DESC",
	"tool_calls": " ORDER BY COALESCE(st.tool_call_count, 0) DESC, s.id DESC",
	// A session with no measurement row sorts last rather than as zero: it was
	// never measured, which is not the same as costing nothing.
	"tokens":        " ORDER BY u.total_tokens IS NULL, u.total_tokens DESC, s.id DESC",
	"lines_changed": " ORDER BY (u.lines_added IS NULL AND u.lines_removed IS NULL), COALESCE(u.lines_added, 0) + COALESCE(u.lines_removed, 0) DESC, s.id DESC",
	"active":        " ORDER BY u.active_ms IS NULL, u.active_ms DESC, s.id DESC",
}

func parseSessionQuery(r *http.Request) (sessionQuery, error) {
	values := r.URL.Query()
	query := sessionQuery{sortKey: "recent", limit: 50, offset: queryOffset(r)}
	if sortKey := values.Get("sort"); sortKey != "" {
		if _, ok := sessionSortOrder[sortKey]; !ok {
			return query, fmt.Errorf("unknown sort %q", sortKey)
		}
		query.sortKey = sortKey
	}
	if limit := queryLimit(r, 50); limit > 0 {
		query.limit = limit
	}
	if query.limit > 200 {
		query.limit = 200
	}

	add := func(dimension, expr string, args ...any) {
		query.conditions = append(query.conditions, sessionCondition{dimension: dimension, expr: expr, args: args})
	}

	if projects := values["project"]; len(projects) > 0 {
		parts := make([]string, 0, len(projects))
		args := make([]any, 0, len(projects))
		for _, project := range projects {
			if project == unrecordedKey {
				parts = append(parts, "s.project_key IS NULL")
				continue
			}
			parts = append(parts, "s.project_key = ?")
			args = append(args, project)
		}
		add("project", "("+strings.Join(parts, " OR ")+")", args...)
	}
	if harnesses := values["harness"]; len(harnesses) > 0 {
		placeholders := make([]string, len(harnesses))
		args := make([]any, len(harnesses))
		for i, harness := range harnesses {
			placeholders[i], args[i] = "?", harness
		}
		add("harness", "s.source IN ("+strings.Join(placeholders, ", ")+")", args...)
	}
	if models := values["model"]; len(models) > 0 {
		placeholders := make([]string, len(models))
		args := make([]any, len(models))
		for i, model := range models {
			placeholders[i], args[i] = "?", model
		}
		add("model", "s.model IN ("+strings.Join(placeholders, ", ")+")", args...)
	}
	if tags := values["tag"]; len(tags) > 0 {
		placeholders := make([]string, len(tags))
		args := make([]any, len(tags))
		for i, tag := range tags {
			placeholders[i], args[i] = "?", tag
		}
		add("tag", "EXISTS (SELECT 1 FROM session_tags t WHERE t.session_id = s.id AND t.tag IN ("+strings.Join(placeholders, ", ")+"))", args...)
	}
	window, err := rangeWindow(values, "")
	if err != nil {
		return query, err
	}
	if window.From != nil {
		add("date", "s.started_at >= ?", *window.From)
	}
	if window.To != nil {
		add("date", "s.started_at <= ?", *window.To)
	}
	// A parent's child list is an explicit request for exactly those threads,
	// so it overrides the two list defaults instead of intersecting with them.
	parent := strings.TrimSpace(values.Get("parent"))
	if parent != "" {
		add("parent", "s.parent_session_id = ?", parent)
	} else {
		switch thread := values.Get("thread"); thread {
		case "", "main":
			// A session whose thread kind was never recorded is not known to
			// be a subagent, so the main view keeps it rather than hiding it.
			add("thread", "(s.thread_kind IS NULL OR s.thread_kind = 'main')")
		case "subagent":
			add("thread", "s.thread_kind = 'subagent'")
		case "all":
		default:
			return query, fmt.Errorf("unknown thread %q", thread)
		}
		switch empty := values.Get("empty"); empty {
		case "", "0":
			add("empty", "COALESCE(st.is_empty, 0) = 0")
		case "1":
			add("empty", "COALESCE(st.is_empty, 0) = 1")
		case "all":
		default:
			return query, fmt.Errorf("unknown empty %q", empty)
		}
	}
	if role := strings.TrimSpace(values.Get("role")); role != "" {
		add("role", "s.agent_role = ?", role)
	}
	if program := strings.TrimSpace(values.Get("program")); program != "" {
		add("program", "EXISTS (SELECT 1 FROM session_commands c WHERE c.session_id = s.id AND c.program = ?)", program)
	}
	if file := strings.TrimSpace(values.Get("file")); file != "" {
		add("file", "EXISTS (SELECT 1 FROM session_files f WHERE f.session_id = s.id AND f.path LIKE ?)", "%"+file+"%")
	}
	if values.Get("has_friction") == "1" {
		add("friction", "COALESCE(st.friction_count, 0) > 0")
	}
	if values.Get("pinned") == "1" {
		add("pinned", "COALESCE(an.pinned, 0) = 1")
	}
	if text := strings.TrimSpace(values.Get("q")); text != "" {
		query.text = text
		query.deep = values.Get("deep") == "1"
		if len([]rune(text)) >= 3 {
			query.ftsMatch = ftsPhrase(text)
			expr := "s.id IN (SELECT session_id FROM sessions_fts WHERE sessions_fts MATCH ?)"
			args := []any{query.ftsMatch}
			if query.deep {
				expr = "(" + expr + " OR s.id IN (SELECT e.session_id FROM events_fts f JOIN events e ON e.id = f.rowid WHERE events_fts MATCH ?))"
				args = append(args, query.ftsMatch)
			}
			add("q", expr, args...)
		} else {
			// The trigram tokenizer cannot match fewer than three characters;
			// falling back to LIKE keeps a two-character query honest instead
			// of silently returning nothing.
			like := "%" + text + "%"
			add("q", "(COALESCE(s.title, '') LIKE ? OR COALESCE(s.task_text, '') LIKE ? OR COALESCE(s.cwd, '') LIKE ? OR COALESCE(s.model, '') LIKE ?)", like, like, like, like)
		}
	}
	return query, nil
}

// ftsPhrase wraps the user's text as one FTS5 phrase so punctuation cannot be
// read as query syntax.
func ftsPhrase(text string) string {
	return `"` + strings.ReplaceAll(text, `"`, `""`) + `"`
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	query, err := parseSessionQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	items, total, err := s.querySessions(r.Context(), query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessions":     items,
		"data_version": s.dataVersion(),
		"pagination": map[string]any{
			"offset": query.offset, "limit": query.limit, "total": total,
			"has_more": query.offset+len(items) < total,
		},
	})
}

func (s *Server) querySessions(ctx context.Context, query sessionQuery) ([]sessionResponse, int, error) {
	where, args := query.where("")
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*)`+sessionFrom+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("api: count sessions: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+sessionColumns+sessionFrom+where+sessionSortOrder[query.sortKey]+` LIMIT ? OFFSET ?`,
		append(append([]any{}, args...), query.limit, query.offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("api: list sessions: %w", err)
	}
	items := make([]sessionResponse, 0, query.limit)
	ids := make([]string, 0, query.limit)
	for rows.Next() {
		item, err := scanSession(rows)
		if err != nil {
			rows.Close()
			return nil, 0, fmt.Errorf("api: scan session: %w", err)
		}
		items = append(items, *item)
		ids = append(ids, item.ID)
	}
	if err := finishRows(rows); err != nil {
		return nil, 0, err
	}
	tags, err := s.sessionTags(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	for i := range items {
		if list, ok := tags[items[i].ID]; ok {
			items[i].Tags = list
		}
	}
	if query.deep && query.ftsMatch != "" {
		if err := s.attachBodyMatches(ctx, items, ids, query.ftsMatch, query.text); err != nil {
			return nil, 0, err
		}
	}
	return items, total, nil
}

func (s *Server) sessionTags(ctx context.Context, ids []string) (map[string][]sessionTagResponse, error) {
	out := make(map[string][]sessionTagResponse, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	placeholders, args := inPlaceholders(ids)
	rows, err := s.db.QueryContext(ctx, `
		SELECT session_id, tag, kind FROM session_tags
		WHERE session_id IN (`+placeholders+`)
		ORDER BY kind, tag`, args...)
	if err != nil {
		return nil, fmt.Errorf("api: session tags: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sessionID string
		var tag sessionTagResponse
		if err := rows.Scan(&sessionID, &tag.Tag, &tag.Kind); err != nil {
			return nil, fmt.Errorf("api: scan session tag: %w", err)
		}
		out[sessionID] = append(out[sessionID], tag)
	}
	return out, rows.Err()
}

// attachBodyMatches fills match_count / match_snippet for a deep search. The
// snippet is cut from the recorded message text; the search index itself is
// contentless and holds no text to quote.
func (s *Server) attachBodyMatches(ctx context.Context, items []sessionResponse, ids []string, match, text string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders, args := inPlaceholders(ids)
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.session_id, COUNT(*), json_extract(e.payload_json, '$.text')
		FROM events_fts f JOIN events e ON e.id = f.rowid
		WHERE events_fts MATCH ? AND e.session_id IN (`+placeholders+`)
		GROUP BY e.session_id`, append([]any{match}, args...)...)
	if err != nil {
		return fmt.Errorf("api: session body matches: %w", err)
	}
	defer rows.Close()
	counts := make(map[string]int, len(ids))
	snippets := make(map[string]string, len(ids))
	for rows.Next() {
		var sessionID string
		var count int
		var body sql.NullString
		if err := rows.Scan(&sessionID, &count, &body); err != nil {
			return fmt.Errorf("api: scan session body match: %w", err)
		}
		counts[sessionID] = count
		if body.Valid {
			snippets[sessionID] = snippetAround(body.String, text)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range items {
		count, ok := counts[items[i].ID]
		if !ok {
			continue
		}
		value := count
		items[i].MatchCount = &value
		if snippet := snippets[items[i].ID]; snippet != "" {
			items[i].MatchSnippet = &snippet
		}
	}
	return nil
}

func snippetAround(body, text string) string {
	index := strings.Index(strings.ToLower(body), strings.ToLower(text))
	if index < 0 {
		return boundRunes(body, 120)
	}
	runes := []rune(body)
	start := len([]rune(body[:index])) - 40
	if start < 0 {
		start = 0
	}
	end := start + 120
	if end > len(runes) {
		end = len(runes)
	}
	snippet := string(runes[start:end])
	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(runes) {
		snippet += "…"
	}
	return snippet
}

func inPlaceholders(ids []string) (string, []any) {
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i], args[i] = "?", id
	}
	return strings.Join(placeholders, ", "), args
}

type facetCount struct {
	Key   string `json:"key"`
	Label string `json:"label,omitempty"`
	CWD   string `json:"cwd,omitempty"`
	Count int    `json:"count"`
}

func (s *Server) handleSessionFacets(w http.ResponseWriter, r *http.Request) {
	query, err := parseSessionQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	response := map[string]any{"data_version": s.dataVersion()}

	where, args := query.where("")
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*)`+sessionFrom+where, args...).Scan(&total); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["total"] = total

	projects, err := s.facetCounts(ctx, query, `COALESCE(s.project_key, '`+unrecordedKey+`')`, "project")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for i := range projects {
		projects[i].Label = projectLabelOf(projects[i].Key)
		if projects[i].Key != unrecordedKey {
			projects[i].CWD = projects[i].Key
		}
	}
	response["projects"] = projects

	for name, dimension := range map[string]struct {
		expr string
		dims []string
	}{
		"harnesses": {"s.source", []string{"harness"}},
		"models":    {"s.model", []string{"model"}},
		// An agent_role only exists on a subagent thread, so the role facet has
		// to look past the thread filter or the default view would always show
		// it as empty.
		"roles": {"s.agent_role", []string{"role", "thread"}},
	} {
		counts, err := s.facetCounts(ctx, query, dimension.expr, dimension.dims...)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		response[name] = counts
	}

	tags, err := s.tagFacets(ctx, query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["tags"] = tags

	frictionWhere, frictionArgs := query.where("friction")
	var withFriction, withoutFriction int
	if err := s.db.QueryRowContext(ctx, `
		SELECT SUM(CASE WHEN COALESCE(st.friction_count, 0) > 0 THEN 1 ELSE 0 END),
		       SUM(CASE WHEN COALESCE(st.friction_count, 0) = 0 THEN 1 ELSE 0 END)`+sessionFrom+frictionWhere, frictionArgs...).
		Scan(&nullableInt{&withFriction}, &nullableInt{&withoutFriction}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["friction"] = map[string]int{"with": withFriction, "without": withoutFriction}

	pinnedWhere, pinnedArgs := query.where("pinned")
	var pinned int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*)`+sessionFrom+andWhere(pinnedWhere, "COALESCE(an.pinned, 0) = 1"), pinnedArgs...).Scan(&pinned); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["pinned"] = pinned

	threads, err := s.facetCounts(ctx, query, `COALESCE(s.thread_kind, '`+unrecordedKey+`')`, "thread")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["threads"] = threads

	emptyWhere, emptyArgs := query.where("empty")
	var emptyYes, emptyNo int
	if err := s.db.QueryRowContext(ctx, `
		SELECT SUM(CASE WHEN COALESCE(st.is_empty, 0) = 1 THEN 1 ELSE 0 END),
		       SUM(CASE WHEN COALESCE(st.is_empty, 0) = 0 THEN 1 ELSE 0 END)`+sessionFrom+emptyWhere, emptyArgs...).
		Scan(&nullableInt{&emptyYes}, &nullableInt{&emptyNo}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["empty"] = map[string]int{"yes": emptyYes, "no": emptyNo}

	programs, err := s.programFacets(ctx, query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["programs"] = programs

	histogram, err := s.dateHistogram(ctx, query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["date_histogram"] = histogram

	writeJSON(w, http.StatusOK, response)
}

// programFacets counts the sessions each program was run in, not the number of
// calls: the facet drives a session filter.
func (s *Server) programFacets(ctx context.Context, query sessionQuery) ([]facetCount, error) {
	where, args := query.where("program")
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.program, COUNT(DISTINCT s.id)`+sessionFrom+`
		JOIN session_commands c ON c.session_id = s.id`+
		andWhere(where, "c.program IS NOT NULL")+
		` GROUP BY c.program ORDER BY COUNT(DISTINCT s.id) DESC, c.program LIMIT 30`, args...)
	if err != nil {
		return nil, fmt.Errorf("api: program facets: %w", err)
	}
	return scanFacetCounts(rows, "program facet")
}

// nullableInt lets a SUM over an empty set scan as zero without turning a
// missing observation into a fabricated one: the set is empty, so zero is the
// count, not a guess.
type nullableInt struct{ target *int }

func (n *nullableInt) Scan(value any) error {
	switch typed := value.(type) {
	case nil:
		*n.target = 0
	case int64:
		*n.target = int(typed)
	case float64:
		*n.target = int(typed)
	default:
		return fmt.Errorf("api: unexpected count type %T", value)
	}
	return nil
}

// andWhere appends one more predicate to an already-built WHERE clause,
// supplying the WHERE keyword when the clause is still empty.
func andWhere(where, condition string) string {
	if where == "" {
		return " WHERE " + condition
	}
	return where + " AND " + condition
}

func (s *Server) facetCounts(ctx context.Context, query sessionQuery, expr string, dimensions ...string) ([]facetCount, error) {
	where, args := query.where(dimensions...)
	rows, err := s.db.QueryContext(ctx, `SELECT `+expr+` AS facet_key, COUNT(*)`+sessionFrom+where+
		` GROUP BY facet_key HAVING facet_key IS NOT NULL ORDER BY COUNT(*) DESC, facet_key`, args...)
	if err != nil {
		return nil, fmt.Errorf("api: facet %s: %w", expr, err)
	}
	return scanFacetCounts(rows, "facet "+expr)
}

type tagFacet struct {
	Tag   string `json:"tag"`
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

func (s *Server) tagFacets(ctx context.Context, query sessionQuery) ([]tagFacet, error) {
	where, args := query.where("tag")
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.tag, t.kind, COUNT(*)`+sessionFrom+`
		JOIN session_tags t ON t.session_id = s.id`+where+`
		GROUP BY t.tag, t.kind ORDER BY COUNT(*) DESC, t.tag`, args...)
	if err != nil {
		return nil, fmt.Errorf("api: tag facets: %w", err)
	}
	return scanTagFacets(rows, "tag facet")
}

// scanFacetCounts drains a (key, count) result set. label names the query in a
// scan failure, which is all the callers differ in.
func scanFacetCounts(rows *sql.Rows, label string) ([]facetCount, error) {
	defer rows.Close()
	out := make([]facetCount, 0)
	for rows.Next() {
		var item facetCount
		if err := rows.Scan(&item.Key, &item.Count); err != nil {
			return nil, fmt.Errorf("api: scan %s: %w", label, err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// scanTagFacets drains a (tag, kind, count) result set. label names the query
// in a scan failure.
func scanTagFacets(rows *sql.Rows, label string) ([]tagFacet, error) {
	defer rows.Close()
	out := make([]tagFacet, 0)
	for rows.Next() {
		var item tagFacet
		if err := rows.Scan(&item.Tag, &item.Kind, &item.Count); err != nil {
			return nil, fmt.Errorf("api: scan %s: %w", label, err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

type dayCount struct {
	Day   string `json:"day"`
	Count int    `json:"count"`
}

func (s *Server) dateHistogram(ctx context.Context, query sessionQuery) ([]dayCount, error) {
	where, args := query.where("date")
	rows, err := s.db.QueryContext(ctx, `
		SELECT substr(s.started_at, 1, 10) AS day, COUNT(*)`+sessionFrom+
		andWhere(where, "s.started_at IS NOT NULL AND s.started_at <> ''")+
		` GROUP BY day ORDER BY day`, args...)
	if err != nil {
		return nil, fmt.Errorf("api: date histogram: %w", err)
	}
	defer rows.Close()
	out := make([]dayCount, 0)
	for rows.Next() {
		var item dayCount
		if err := rows.Scan(&item.Day, &item.Count); err != nil {
			return nil, fmt.Errorf("api: scan date histogram: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Server) loadSession(ctx context.Context, id string) (*sessionResponse, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+sessionColumns+sessionFrom+` WHERE s.id = ?`, id)
	item, err := scanSession(row)
	if err != nil {
		return nil, err
	}
	tags, err := s.sessionTags(ctx, []string{id})
	if err != nil {
		return nil, err
	}
	if list, ok := tags[id]; ok {
		item.Tags = list
	}
	annotation, err := s.sessionAnnotation(ctx, id)
	if err != nil {
		return nil, err
	}
	item.Annotation = annotation
	models, err := s.sessionModelUsage(ctx, id)
	if err != nil {
		return nil, err
	}
	item.Usage.ByModel = models
	return item, nil
}

func (s *Server) sessionAnnotation(ctx context.Context, id string) (*annotationResponse, error) {
	var pinned int
	var note, updated sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT pinned, note, updated_at FROM session_annotations WHERE session_id = ?`, id).
		Scan(&pinned, &note, &updated)
	if err == sql.ErrNoRows {
		return &annotationResponse{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("api: session annotation: %w", err)
	}
	out := &annotationResponse{Pinned: pinned != 0}
	if note.Valid {
		out.Note = &note.String
	}
	if updated.Valid {
		out.UpdatedAt = &updated.String
	}
	return out, nil
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	item, err := s.loadSession(r.Context(), r.PathValue("id"))
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	paged := r.URL.Query().Get("events") == "page"
	eventOffset := queryOffset(r)
	eventLimit := queryLimit(r, 1000)
	eventQuery := `SELECT id, session_id, event_type, asset_id, asset_version_id, COALESCE(source_event_id, ''), participation_signal, observation_level, COALESCE(payload_json, ''), COALESCE(locator_json, ''), COALESCE(occurred_at, ''), COALESCE(adapter_version, '') FROM events WHERE session_id = ? ORDER BY occurred_at, id`
	args := []any{item.ID}
	if paged {
		eventQuery += ` LIMIT ? OFFSET ?`
		args = append(args, eventLimit, eventOffset)
	}
	rows, err := s.db.QueryContext(r.Context(), eventQuery, args...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	events := make([]map[string]any, 0, eventLimit)
	for rows.Next() {
		var id int64
		var sessionID, eventType, sourceID, observation, payload, locator, occurred, adapterVersion string
		var assetID, signal sql.NullString
		var versionID sql.NullInt64
		if err := rows.Scan(&id, &sessionID, &eventType, &assetID, &versionID, &sourceID, &signal, &observation, &payload, &locator, &occurred, &adapterVersion); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		events = append(events, eventResponseFromFields(id, sessionID, eventType, assetID, versionID, sourceID, signal, observation, payload, locator, occurred, adapterVersion, paged))
	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	friction, err := s.sessionFriction(r.Context(), item.ID, 500)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response := map[string]any{"session": item, "events": events, "friction": friction, "data_version": s.dataVersion()}
	if err := s.attachSessionThread(r.Context(), item, response); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if paged {
		response["event_offset"] = eventOffset
		response["event_limit"] = eventLimit
		response["event_total"] = item.EventCount
		response["events_has_more"] = eventOffset+len(events) < item.EventCount
	}
	writeJSON(w, http.StatusOK, response)
}

type parentSessionResponse struct {
	ID           string  `json:"id"`
	Title        *string `json:"title"`
	ProjectLabel string  `json:"project_label"`
}

type sessionCommandResponse struct {
	EventID    int64   `json:"event_id"`
	ToolName   string  `json:"tool_name"`
	Program    *string `json:"program"`
	Command    string  `json:"command"`
	ExitCode   *int    `json:"exit_code"`
	IsError    *bool   `json:"is_error"`
	OccurredAt *string `json:"occurred_at"`
}

type sessionFileResponse struct {
	Path         string  `json:"path"`
	Reads        int     `json:"reads"`
	Edits        int     `json:"edits"`
	Writes       int     `json:"writes"`
	Deletes      int     `json:"deletes"`
	FirstEventID int64   `json:"first_event_id"`
	LastAt       *string `json:"last_at"`
}

// attachSessionThread adds the session's place in the thread tree and the two
// projections a reader needs to see what it actually ran and touched.
func (s *Server) attachSessionThread(ctx context.Context, item *sessionResponse, response map[string]any) error {
	parent, err := s.parentSession(ctx, item.ParentSessionID)
	if err != nil {
		return err
	}
	response["parent"] = parent
	children, err := s.childSessions(ctx, item.ID, 100)
	if err != nil {
		return err
	}
	response["children"] = children
	commands, total, err := s.sessionCommands(ctx, item.ID, 500)
	if err != nil {
		return err
	}
	response["commands"], response["commands_total"] = commands, total
	files, filesTotal, err := s.sessionFiles(ctx, item.ID, 500)
	if err != nil {
		return err
	}
	response["files"], response["files_total"] = files, filesTotal
	return nil
}

func (s *Server) parentSession(ctx context.Context, parentID *string) (*parentSessionResponse, error) {
	if parentID == nil || strings.TrimSpace(*parentID) == "" {
		return nil, nil
	}
	var out parentSessionResponse
	var title, cwd sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, title, project_key FROM sessions WHERE id = ?`, *parentID).Scan(&out.ID, &title, &cwd)
	if err == sql.ErrNoRows {
		// The parent thread is named by the child but was never ingested; that
		// is a missing record, not a missing link.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("api: parent session: %w", err)
	}
	out.Title = optionalString(title)
	out.ProjectLabel = projectLabelOf(projectKeyOf(cwd))
	return &out, nil
}

func (s *Server) childSessions(ctx context.Context, id string, limit int) ([]sessionResponse, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sessionColumns+sessionFrom+`
		WHERE s.parent_session_id = ?
		ORDER BY s.started_at IS NULL, s.started_at, s.id LIMIT ?`, id, limit)
	if err != nil {
		return nil, fmt.Errorf("api: child sessions: %w", err)
	}
	out := make([]sessionResponse, 0)
	ids := make([]string, 0)
	for rows.Next() {
		item, err := scanSession(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("api: scan child session: %w", err)
		}
		out = append(out, *item)
		ids = append(ids, item.ID)
	}
	if err := finishRows(rows); err != nil {
		return nil, err
	}
	tags, err := s.sessionTags(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if list, ok := tags[out[i].ID]; ok {
			out[i].Tags = list
		}
	}
	return out, nil
}

func (s *Server) sessionCommands(ctx context.Context, id string, limit int) ([]sessionCommandResponse, int, error) {
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_commands WHERE session_id = ?`, id).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("api: count session commands: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT event_id, tool_name, program, command, exit_code, is_error, occurred_at
		FROM session_commands WHERE session_id = ? ORDER BY id LIMIT ?`, id, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("api: session commands: %w", err)
	}
	defer rows.Close()
	out := make([]sessionCommandResponse, 0)
	for rows.Next() {
		var item sessionCommandResponse
		var program, occurred sql.NullString
		var exitCode, isError sql.NullInt64
		if err := rows.Scan(&item.EventID, &item.ToolName, &program, &item.Command, &exitCode, &isError, &occurred); err != nil {
			return nil, 0, fmt.Errorf("api: scan session command: %w", err)
		}
		item.Program = optionalString(program)
		item.OccurredAt = optionalString(occurred)
		if exitCode.Valid {
			value := int(exitCode.Int64)
			item.ExitCode = &value
		}
		if isError.Valid {
			value := isError.Int64 != 0
			item.IsError = &value
		}
		out = append(out, item)
	}
	return out, total, rows.Err()
}

func (s *Server) sessionFiles(ctx context.Context, id string, limit int) ([]sessionFileResponse, int, error) {
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT path) FROM session_files WHERE session_id = ?`, id).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("api: count session files: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT path,
		       SUM(CASE WHEN action = 'read' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN action = 'edit' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN action = 'write' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN action = 'delete' THEN 1 ELSE 0 END),
		       MIN(event_id), MAX(occurred_at)
		FROM session_files WHERE session_id = ?
		GROUP BY path
		ORDER BY MAX(occurred_at) IS NULL, MAX(occurred_at) DESC, path LIMIT ?`, id, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("api: session files: %w", err)
	}
	defer rows.Close()
	out := make([]sessionFileResponse, 0)
	for rows.Next() {
		var item sessionFileResponse
		var lastAt sql.NullString
		if err := rows.Scan(&item.Path, &item.Reads, &item.Edits, &item.Writes, &item.Deletes, &item.FirstEventID, &lastAt); err != nil {
			return nil, 0, fmt.Errorf("api: scan session file: %w", err)
		}
		item.LastAt = optionalString(lastAt)
		out = append(out, item)
	}
	return out, total, rows.Err()
}

type annotationRequest struct {
	Pinned *bool     `json:"pinned"`
	Note   *string   `json:"note"`
	Tags   *[]string `json:"tags"`
}

// handleSessionAnnotation records the user's own marks. It writes to the local
// database only; the native transcript the session came from is never touched.
func (s *Server) handleSessionAnnotation(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	var exists string
	if err := s.db.QueryRowContext(r.Context(), `SELECT id FROM sessions WHERE id = ?`, sessionID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var request annotationRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&request); err != nil {
		http.Error(w, "invalid annotation body", http.StatusBadRequest)
		return
	}
	if request.Note != nil && len([]rune(*request.Note)) > 4000 {
		http.Error(w, "note exceeds 4000 characters", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	if request.Pinned != nil || request.Note != nil {
		pinned, note := 0, any(nil)
		if request.Pinned != nil && *request.Pinned {
			pinned = 1
		}
		if request.Note != nil {
			note = *request.Note
		}
		setPinned, setNote := "pinned", "note"
		if request.Pinned != nil {
			setPinned = "excluded.pinned"
		} else {
			setPinned = "session_annotations.pinned"
		}
		if request.Note != nil {
			setNote = "excluded.note"
		} else {
			setNote = "session_annotations.note"
		}
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO session_annotations (session_id, pinned, note, updated_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT (session_id) DO UPDATE SET
				pinned = `+setPinned+`,
				note = `+setNote+`,
				updated_at = excluded.updated_at`,
			sessionID, pinned, note, formatTime(time.Now().UTC())); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if request.Tags != nil {
		if err := s.replaceUserTags(ctx, sessionID, *request.Tags); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	s.bumpDataVersion()
	annotation, err := s.sessionAnnotation(ctx, sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tags, err := s.sessionTags(ctx, []string{sessionID})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	list := tags[sessionID]
	if list == nil {
		list = make([]sessionTagResponse, 0)
	}
	writeJSON(w, http.StatusOK, map[string]any{"annotation": annotation, "tags": list, "data_version": s.dataVersion()})
}

func (s *Server) replaceUserTags(ctx context.Context, sessionID string, tags []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("api: begin user tag write: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_tags WHERE session_id = ? AND kind = 'user'`, sessionID); err != nil {
		tx.Rollback()
		return fmt.Errorf("api: clear user tags: %w", err)
	}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO session_tags (session_id, tag, kind) VALUES (?, ?, 'user')
			ON CONFLICT (session_id, tag, kind) DO NOTHING`, sessionID, boundRunes(tag, 64)); err != nil {
			tx.Rollback()
			return fmt.Errorf("api: insert user tag: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("api: commit user tag write: %w", err)
	}
	return nil
}

// assetRelatedSessions returns every session that created an opportunity for
// the asset, including sessions where participation was not recorded. An
// opportunity is the related-task denominator; filtering this projection to
// participations would make valid non-participating task records disappear.
func (s *Server) assetRelatedSessions(ctx context.Context, assetID string, limit int) ([]sessionResponse, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sessionColumns+sessionFrom+`
		WHERE EXISTS (
			SELECT 1 FROM opportunities o
			WHERE o.session_id = s.id AND o.asset_id = ? AND o.superseded_at IS NULL
		) OR EXISTS (
			SELECT 1
			FROM participations p JOIN asset_versions av ON av.id = p.asset_version_id AND p.superseded_at IS NULL
			WHERE p.session_id = s.id AND av.asset_id = ?
		)
		ORDER BY COALESCE(s.started_at, '') DESC, s.id DESC LIMIT ?`, assetID, assetID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]sessionResponse, 0)
	for rows.Next() {
		item, err := scanSession(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, *item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	return out, rows.Close()
}
