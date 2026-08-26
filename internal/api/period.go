package api

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"flatline/internal/friction"
)

// A period summary answers "what happened in this stretch of time", in the
// same shape for the current stretch and for the one before it, so the two can
// be read side by side and subtracted.
//
// Every number carries the denominator it was counted out of. A measurement no
// source recorded is null, never zero, and a delta over a null is null too: a
// period nobody measured did not use zero tokens.

// periodScope is what a period is counted over: the dimensions of the page
// (nothing on the overview, one project on the project page) and the default
// counting rule. Both are applied to the current and previous windows
// identically, which is what makes the two comparable at all.
type periodScope struct {
	conditions []string
	args       []any
	// scope is the main-sessions-only rule, already in " AND ..." form, or ""
	// when the columns it needs do not exist yet.
	scope string
}

// where builds the clause for one window. withScope decides whether the
// main-sessions rule is applied; the subagent block needs the same window
// without it, because subagent threads are exactly what that rule removes.
func (p periodScope) where(window overviewRange, withScope bool) (string, []any) {
	clause := " WHERE 1 = 1"
	args := make([]any, 0, len(p.args)+2)
	if window.From != nil {
		clause += " AND s.started_at >= ?"
		args = append(args, *window.From)
	}
	if window.To != nil {
		clause += " AND s.started_at <= ?"
		args = append(args, *window.To)
	}
	for _, condition := range p.conditions {
		clause += " AND " + condition
	}
	args = append(args, p.args...)
	if withScope {
		clause += p.scope
	}
	return clause, args
}

type parallelismSummary struct {
	// Peak is the largest number of sessions that were open at the same
	// moment, and PeakAt is the first moment that many were open.
	Peak   int     `json:"peak"`
	PeakAt *string `json:"peak_at"`
	// SessionsConsidered is the denominator: sessions with both a start and an
	// end recorded. Unbounded counts what could not be placed on the timeline
	// at all — a running session, or one whose end the source never wrote.
	SessionsConsidered int    `json:"sessions_considered"`
	Unbounded          int    `json:"unbounded_sessions"`
	Note               string `json:"note"`
	NoteEN             string `json:"note_en"`
}

const parallelismNote = "峰值 = 起止区间重叠的会话数；只统计同时记录了 started_at 与 ended_at 的会话，未记录结束时间的会话计入 unbounded_sessions 而不计入分母。"

const parallelismNoteEN = "The peak is how many sessions overlap in time. Only sessions that recorded both a started_at and an ended_at are counted; a session whose end was never recorded goes to unbounded_sessions and stays out of the denominator."

type shareValue struct {
	Numerator   int      `json:"numerator"`
	Denominator int      `json:"denominator"`
	Rate        *float64 `json:"rate"`
}

type subagentSummary struct {
	// SessionsWithSubagents counts the parents; SubagentSessions counts the
	// threads they launched. AvgPerSession is the second over the first and is
	// null when no session launched one.
	SessionsWithSubagents int          `json:"sessions_with_subagents"`
	SubagentSessions      int          `json:"subagent_sessions"`
	AvgPerSession         *float64     `json:"avg_per_session"`
	ByRole                []facetCount `json:"by_role"`
	// FrictionShare is the friction recorded in subagent threads over the
	// friction recorded in every thread of the window.
	FrictionShare shareValue `json:"friction_share"`
	Note          string     `json:"note"`
	NoteEN        string     `json:"note_en"`
}

const subagentNote = "子代理会话 = thread_kind='subagent'；角色分布只统计子代理自己的 agent_role，主会话没有角色、不进入分布。"

const subagentNoteEN = "A subagent session is thread_kind='subagent'. The role split counts only a subagent's own agent_role; a main session has no role and does not enter it."

// rereadFile is one file that was read again and again inside single sessions.
type rereadFile struct {
	Path string `json:"path"`
	// Sessions is how many sessions read this file at least Threshold times;
	// Reads is what those repetitions add up to.
	Sessions int `json:"sessions"`
	Reads    int `json:"reads"`
}

type rereadSummary struct {
	Sessions  int          `json:"sessions"`
	Reads     int          `json:"reads"`
	Threshold int          `json:"threshold"`
	TopFiles  []rereadFile `json:"top_files"`
	Note      string       `json:"note"`
	NoteEN    string       `json:"note_en"`
}

const rereadThreshold = 3

const rereadNote = "同一会话内同一路径被 action='read' 记录 ≥3 次即计入；reads 是这些 (会话,路径) 组的读取次数之和。"

const rereadNoteEN = "A path counts once it was recorded with action='read' at least 3 times inside one session; reads is what those (session, path) groups add up to."

type missingCommand struct {
	Command  string  `json:"command"`
	Sessions int     `json:"sessions"`
	Count    int     `json:"count"`
	LastAt   *string `json:"last_at"`
}

type failingProgram struct {
	Program string `json:"program"`
	Calls   int    `json:"calls"`
	// KnownOutcomes is the denominator Rate is computed over: calls whose
	// outcome the source actually recorded. Expected nonzero exits — `rg`
	// exiting 1 because nothing matched — are known outcomes and are not
	// failures.
	KnownOutcomes int     `json:"known_outcomes"`
	Failures      int     `json:"failures"`
	Rate          float64 `json:"rate"`
}

type environmentSummary struct {
	MissingCommands  []missingCommand `json:"missing_commands"`
	FailingPrograms  []failingProgram `json:"failing_programs"`
	MinKnownOutcomes int              `json:"min_known_outcomes"`
	Note             string           `json:"note"`
	NoteEN           string           `json:"note_en"`
}

const environmentNote = "缺失命令来自两处证据：category='command_not_found' 的样例行（命令名从行内抽取，已按签名规则小写并把数字串归一为 #），以及整条命令行只有 `which X` 一个语句且退出码非 0 的调用（= X 不在 PATH 里）。行内抽不出命令名的记进 __unparsed__，排在最后。失败率只列已记录结果 ≥30 次、且命令行确实运行了某个程序的记录，分母是 known_outcomes，预期非零退出不算失败；只跑 shell 内建/语法的命令行没有程序名，不进这张表。"

const environmentNoteEN = "A missing command comes from two records: the sample line of a category='command_not_found' signature (the name is read out of the line, lowercased and with digit runs normalized to # by the signature rule), and a call whose whole command line is the single statement `which X` and which exited nonzero (= X is not on PATH). A line no name could be read from goes to __unparsed__, which always sorts last. The failure rate lists only programs with at least 30 recorded outcomes on a line that really ran a program; the denominator is known_outcomes and an expected nonzero exit is not a failure. A line that runs only shell builtins or syntax names no program and does not enter this table."

// unparsedCommandKey holds the command_not_found records whose recorded line
// names no command. It is not the same as unrecordedKey: the source did record
// a line, and the line is what could not be read.
const unparsedCommandKey = "__unparsed__"

// periodSummary is one window's whole answer. previous carries the same struct
// for the window before it, so a page compares like with like.
type periodSummary struct {
	Range    overviewRange `json:"range"`
	Sessions int           `json:"sessions"`
	// Projects is how many distinct working directories the window's sessions
	// ran in. A session that recorded none is counted under one bucket, the
	// same way the project list reports it.
	Projects             int                `json:"projects"`
	Events               int                `json:"events"`
	Messages             int                `json:"messages"`
	ToolCalls            int                `json:"tool_calls"`
	Friction             int                `json:"friction"`
	SessionsWithFriction int                `json:"sessions_with_friction"`
	DurationMS           int64              `json:"duration_ms"`
	DurationKnown        int                `json:"duration_known_sessions"`
	UsageKnown           int                `json:"usage_known_sessions"`
	TokenSessions        int                `json:"token_sessions"`
	TotalTokens          *int64             `json:"total_tokens"`
	OutputTokens         *int64             `json:"output_tokens"`
	LinesAdded           *int64             `json:"lines_added"`
	LinesRemoved         *int64             `json:"lines_removed"`
	ActiveMS             *int64             `json:"active_ms"`
	Parallelism          parallelismSummary `json:"parallelism"`
	Subagents            subagentSummary    `json:"subagents"`
	Reread               rereadSummary      `json:"reread"`
	Environment          environmentSummary `json:"environment"`
}

// deltaValue is one KPI's movement. Direction is a word rather than a sign so
// a page never has to decide what a negative number means for this KPI.
type deltaValue struct {
	Value     int64  `json:"value"`
	Direction string `json:"direction"`
}

func directionOf(value int64) string {
	switch {
	case value > 0:
		return "up"
	case value < 0:
		return "down"
	default:
		return "flat"
	}
}

func delta(current, previous int64) *deltaValue {
	value := current - previous
	return &deltaValue{Value: value, Direction: directionOf(value)}
}

// deltaOfOptional is null when either side was never measured. A window with
// no token record did not use zero tokens, so it cannot be subtracted from.
func deltaOfOptional(current, previous *int64) *deltaValue {
	if current == nil || previous == nil {
		return nil
	}
	return delta(*current, *previous)
}

// periodDelta is the movement of every KPI between two windows, keyed by the
// field name of the summary it came from.
func periodDelta(current, previous periodSummary) map[string]*deltaValue {
	return map[string]*deltaValue{
		"sessions":                delta(int64(current.Sessions), int64(previous.Sessions)),
		"projects":                delta(int64(current.Projects), int64(previous.Projects)),
		"events":                  delta(int64(current.Events), int64(previous.Events)),
		"messages":                delta(int64(current.Messages), int64(previous.Messages)),
		"tool_calls":              delta(int64(current.ToolCalls), int64(previous.ToolCalls)),
		"friction":                delta(int64(current.Friction), int64(previous.Friction)),
		"sessions_with_friction":  delta(int64(current.SessionsWithFriction), int64(previous.SessionsWithFriction)),
		"duration_ms":             delta(current.DurationMS, previous.DurationMS),
		"total_tokens":            deltaOfOptional(current.TotalTokens, previous.TotalTokens),
		"output_tokens":           deltaOfOptional(current.OutputTokens, previous.OutputTokens),
		"lines_added":             deltaOfOptional(current.LinesAdded, previous.LinesAdded),
		"lines_removed":           deltaOfOptional(current.LinesRemoved, previous.LinesRemoved),
		"active_ms":               deltaOfOptional(current.ActiveMS, previous.ActiveMS),
		"parallel_peak":           delta(int64(current.Parallelism.Peak), int64(previous.Parallelism.Peak)),
		"sessions_with_subagents": delta(int64(current.Subagents.SessionsWithSubagents), int64(previous.Subagents.SessionsWithSubagents)),
		"subagent_sessions":       delta(int64(current.Subagents.SubagentSessions), int64(previous.Subagents.SubagentSessions)),
		"reread_sessions":         delta(int64(current.Reread.Sessions), int64(previous.Reread.Sessions)),
		"reread_reads":            delta(int64(current.Reread.Reads), int64(previous.Reread.Reads)),
		"missing_commands":        delta(int64(len(current.Environment.MissingCommands)), int64(len(previous.Environment.MissingCommands))),
		"failing_programs":        delta(int64(len(current.Environment.FailingPrograms)), int64(len(previous.Environment.FailingPrograms))),
	}
}

// previousWindow is the same length of time immediately before this one. A
// window with no lower bound has no length, so it has no previous.
func previousWindow(window overviewRange) (overviewRange, bool) {
	if window.From == nil {
		return overviewRange{}, false
	}
	from, err := time.Parse(time.RFC3339Nano, *window.From)
	if err != nil {
		return overviewRange{}, false
	}
	to := time.Now().UTC()
	if window.To != nil {
		parsed, err := time.Parse(time.RFC3339Nano, *window.To)
		if err != nil {
			return overviewRange{}, false
		}
		to = parsed
	}
	length := to.Sub(from)
	if length <= 0 {
		return overviewRange{}, false
	}
	// The previous window ends one millisecond before this one starts, so a
	// session cannot be counted in both.
	previousTo := from.Add(-time.Millisecond).UTC().Format(time.RFC3339Nano)
	previousFrom := from.Add(-length).UTC().Format(time.RFC3339Nano)
	return overviewRange{From: &previousFrom, To: &previousTo}, true
}

// buildPeriodSummary answers every question of one window in one place.
func (s *Server) buildPeriodSummary(ctx context.Context, window overviewRange, scope periodScope) (periodSummary, error) {
	out := periodSummary{Range: window}
	where, args := scope.where(window, true)
	unscoped, unscopedArgs := scope.where(window, false)

	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       SUM(COALESCE(st.event_count, 0)), SUM(COALESCE(st.message_count, 0)),
		       SUM(COALESCE(st.tool_call_count, 0)), SUM(COALESCE(st.friction_count, 0)),
		       SUM(CASE WHEN COALESCE(st.friction_count, 0) > 0 THEN 1 ELSE 0 END),
		       SUM(CASE WHEN st.duration_ms IS NULL THEN 0 ELSE 1 END),
		       SUM(COALESCE(st.duration_ms, 0))
		FROM sessions s LEFT JOIN session_stats st ON st.session_id = s.id`+where, args...).
		Scan(&out.Sessions, &nullableInt{&out.Events}, &nullableInt{&out.Messages},
			&nullableInt{&out.ToolCalls}, &nullableInt{&out.Friction},
			&nullableInt{&out.SessionsWithFriction}, &nullableInt{&out.DurationKnown},
			&nullableInt64Sum{&out.DurationMS}); err != nil {
		return periodSummary{}, fmt.Errorf("api: period totals: %w", err)
	}

	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT COALESCE(s.project_key, '`+unrecordedKey+`'))
		FROM sessions s LEFT JOIN session_stats st ON st.session_id = s.id`+where, args...).
		Scan(&out.Projects); err != nil {
		return periodSummary{}, fmt.Errorf("api: period projects: %w", err)
	}

	usage, err := s.aggregateUsage(ctx, where, args)
	if err != nil {
		return periodSummary{}, err
	}
	out.UsageKnown, out.TokenSessions = usage.KnownSessions, usage.TokenSessions
	if usage.TokenSessions > 0 {
		total, output := usage.TotalTokens, usage.OutputTokens
		out.TotalTokens, out.OutputTokens = &total, &output
	}
	if usage.KnownSessions > 0 {
		added, removed, active := usage.LinesAdded, usage.LinesRemoved, usage.ActiveMS
		out.LinesAdded, out.LinesRemoved, out.ActiveMS = &added, &removed, &active
	}

	if out.Parallelism, err = s.parallelism(ctx, where, args); err != nil {
		return periodSummary{}, err
	}
	if out.Subagents, err = s.subagents(ctx, unscoped, unscopedArgs); err != nil {
		return periodSummary{}, err
	}
	if out.Reread, err = s.reread(ctx, where, args); err != nil {
		return periodSummary{}, err
	}
	if out.Environment, err = s.environment(ctx, where, args); err != nil {
		return periodSummary{}, err
	}
	return out, nil
}

type sessionInterval struct {
	start, end time.Time
}

// parallelism is the largest number of sessions that were open at once. It is
// a sweep over the recorded start and end of each session: +1 at every start,
// -1 at every end, and the peak is the running maximum.
func (s *Server) parallelism(ctx context.Context, where string, args []any) (parallelismSummary, error) {
	out := parallelismSummary{Note: parallelismNote, NoteEN: parallelismNoteEN}
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.started_at, s.ended_at
		FROM sessions s LEFT JOIN session_stats st ON st.session_id = s.id`+where, args...)
	if err != nil {
		return out, fmt.Errorf("api: parallelism: %w", err)
	}
	defer rows.Close()
	intervals := make([]sessionInterval, 0, 256)
	for rows.Next() {
		var started, ended sql.NullString
		if err := rows.Scan(&started, &ended); err != nil {
			return out, fmt.Errorf("api: scan parallelism: %w", err)
		}
		start, startOK := parseStamp(started)
		end, endOK := parseStamp(ended)
		if !startOK || !endOK || end.Before(start) {
			out.Unbounded++
			continue
		}
		intervals = append(intervals, sessionInterval{start: start, end: end})
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("api: iterate parallelism: %w", err)
	}
	out.SessionsConsidered = len(intervals)
	if len(intervals) == 0 {
		return out, nil
	}
	type edge struct {
		at    time.Time
		delta int
	}
	edges := make([]edge, 0, len(intervals)*2)
	for _, interval := range intervals {
		edges = append(edges, edge{at: interval.start, delta: 1}, edge{at: interval.end, delta: -1})
	}
	// An end at the same instant as a start closes first, so two sessions that
	// merely touch are not reported as having overlapped.
	sort.Slice(edges, func(i, j int) bool {
		if !edges[i].at.Equal(edges[j].at) {
			return edges[i].at.Before(edges[j].at)
		}
		return edges[i].delta < edges[j].delta
	})
	open := 0
	for _, item := range edges {
		open += item.delta
		if open > out.Peak {
			out.Peak = open
			at := item.at.UTC().Format(time.RFC3339Nano)
			out.PeakAt = &at
		}
	}
	return out, nil
}

func parseStamp(value sql.NullString) (time.Time, bool) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

// subagents reads the window without the main-sessions rule, because subagent
// threads are precisely what that rule removes.
func (s *Server) subagents(ctx context.Context, where string, args []any) (subagentSummary, error) {
	out := subagentSummary{ByRole: []facetCount{}, Note: subagentNote, NoteEN: subagentNoteEN}
	hasThread, err := s.hasColumn(ctx, "sessions", "thread_kind")
	if err != nil || !hasThread {
		return out, err
	}
	subagentWhere := where + " AND s.thread_kind = 'subagent'"
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(DISTINCT s.parent_session_id)
		FROM sessions s LEFT JOIN session_stats st ON st.session_id = s.id`+subagentWhere, args...).
		Scan(&out.SubagentSessions, &out.SessionsWithSubagents); err != nil {
		return out, fmt.Errorf("api: subagent counts: %w", err)
	}
	if out.SessionsWithSubagents > 0 {
		average := float64(out.SubagentSessions) / float64(out.SessionsWithSubagents)
		out.AvgPerSession = &average
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(NULLIF(TRIM(COALESCE(s.agent_role, '')), ''), '`+unrecordedKey+`') AS role, COUNT(*)
		FROM sessions s LEFT JOIN session_stats st ON st.session_id = s.id`+subagentWhere+`
		GROUP BY role ORDER BY 2 DESC, 1 LIMIT 20`, args...)
	if err != nil {
		return out, fmt.Errorf("api: subagent roles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item facetCount
		if err := rows.Scan(&item.Key, &item.Count); err != nil {
			return out, fmt.Errorf("api: scan subagent role: %w", err)
		}
		out.ByRole = append(out.ByRole, item)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT SUM(CASE WHEN s.thread_kind = 'subagent' THEN COALESCE(st.friction_count, 0) ELSE 0 END),
		       SUM(COALESCE(st.friction_count, 0))
		FROM sessions s LEFT JOIN session_stats st ON st.session_id = s.id`+where, args...).
		Scan(&nullableInt{&out.FrictionShare.Numerator}, &nullableInt{&out.FrictionShare.Denominator}); err != nil {
		return out, fmt.Errorf("api: subagent friction share: %w", err)
	}
	if out.FrictionShare.Denominator > 0 {
		rate := float64(out.FrictionShare.Numerator) / float64(out.FrictionShare.Denominator)
		out.FrictionShare.Rate = &rate
	}
	return out, nil
}

// reread counts the sessions that read the same file three or more times, and
// how many reads those repetitions account for.
func (s *Server) reread(ctx context.Context, where string, args []any) (rereadSummary, error) {
	out := rereadSummary{Threshold: rereadThreshold, TopFiles: []rereadFile{}, Note: rereadNote, NoteEN: rereadNoteEN}
	has, err := s.hasTable(ctx, "session_files")
	if err != nil || !has {
		return out, err
	}
	// The inner query is the set of (session, path) pairs that were read at
	// least Threshold times; the two readings below are the same set counted
	// two ways, so the totals and the list cannot disagree.
	inner := `
		SELECT f.session_id AS session_id, f.path AS path, COUNT(*) AS reads
		FROM session_files f
		JOIN sessions s ON s.id = f.session_id
		LEFT JOIN session_stats st ON st.session_id = s.id` + where + `
		  AND f.action = 'read'
		GROUP BY f.session_id, f.path
		HAVING COUNT(*) >= ?`
	innerArgs := append(append([]any{}, args...), rereadThreshold)
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT session_id), COALESCE(SUM(reads), 0) FROM (`+inner+`)`,
		innerArgs...).Scan(&out.Sessions, &out.Reads); err != nil {
		return out, fmt.Errorf("api: reread counts: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT path, COUNT(DISTINCT session_id), SUM(reads) FROM (`+inner+`)
		GROUP BY path ORDER BY SUM(reads) DESC, path LIMIT 8`, innerArgs...)
	if err != nil {
		return out, fmt.Errorf("api: reread files: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item rereadFile
		if err := rows.Scan(&item.Path, &item.Sessions, &item.Reads); err != nil {
			return out, fmt.Errorf("api: scan reread file: %w", err)
		}
		out.TopFiles = append(out.TopFiles, item)
	}
	return out, rows.Err()
}

// minKnownOutcomes is the smallest number of recorded outcomes a program needs
// before its failure rate is reported. Below it the rate is one or two runs and
// says nothing about the program.
const minKnownOutcomes = 30

func (s *Server) environment(ctx context.Context, where string, args []any) (environmentSummary, error) {
	out := environmentSummary{MissingCommands: []missingCommand{}, FailingPrograms: []failingProgram{},
		MinKnownOutcomes: minKnownOutcomes, Note: environmentNote, NoteEN: environmentNoteEN}
	commands, err := s.missingCommands(ctx, where, args)
	if err != nil {
		return out, err
	}
	out.MissingCommands = commands
	programs, err := s.failingPrograms(ctx, where, args)
	if err != nil {
		return out, err
	}
	out.FailingPrograms = programs
	return out, nil
}

// missingCommandBucket accumulates one command name's evidence.
type missingCommandBucket struct {
	sessions map[string]struct{}
	count    int
	lastAt   string
}

func (b *missingCommandBucket) add(sessionID, occurred string) {
	b.count++
	b.sessions[sessionID] = struct{}{}
	if occurred > b.lastAt {
		b.lastAt = occurred
	}
}

// missingCommands reads the two records that say a command was not found and
// groups them by the command they name: the command_not_found friction records
// of the window, and the `which X` probes that came back nonzero. The first
// reads the signature's own sample line, so the page and the friction list are
// reading the same evidence.
func (s *Server) missingCommands(ctx context.Context, where string, args []any) ([]missingCommand, error) {
	out := make([]missingCommand, 0)
	has, err := s.hasColumn(ctx, "friction_records", "signature")
	if err != nil || !has {
		return out, err
	}
	byCommand := make(map[string]*missingCommandBucket)
	bucket := func(name string) *missingCommandBucket {
		item, ok := byCommand[name]
		if !ok {
			item = &missingCommandBucket{sessions: make(map[string]struct{})}
			byCommand[name] = item
		}
		return item
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(fr.signature, ''), fr.session_id, COALESCE(fr.occurred_at, '')
		FROM friction_records fr
		JOIN sessions s ON s.id = fr.session_id
		LEFT JOIN session_stats st ON st.session_id = s.id`+where+`
		  AND fr.category = 'command_not_found'`, args...)
	if err != nil {
		return nil, fmt.Errorf("api: missing commands: %w", err)
	}
	for rows.Next() {
		var signature, sessionID, occurred string
		if err := rows.Scan(&signature, &sessionID, &occurred); err != nil {
			rows.Close()
			return nil, fmt.Errorf("api: scan missing command: %w", err)
		}
		bucket(missingCommandName(frictionSignatureLine(signature))).add(sessionID, occurred)
	}
	if err := finishRows(rows); err != nil {
		return nil, err
	}
	if err := s.addProbedCommands(ctx, where, args, bucket); err != nil {
		return nil, err
	}
	for name, item := range byCommand {
		entry := missingCommand{Command: name, Sessions: len(item.sessions), Count: item.count}
		if item.lastAt != "" {
			last := item.lastAt
			entry.LastAt = &last
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		// The bucket that names no command sinks below every bucket that does:
		// it is a count of unread lines, not a missing command.
		if (out[i].Command == unparsedCommandKey) != (out[j].Command == unparsedCommandKey) {
			return out[j].Command == unparsedCommandKey
		}
		if out[i].Sessions != out[j].Sessions {
			return out[i].Sessions > out[j].Sessions
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Command < out[j].Command
	})
	if len(out) > 10 {
		out = out[:10]
	}
	return out, nil
}

// addProbedCommands folds the command-existence probes of the window into the
// same buckets. The rule is one sentence: a command line whose only statement
// is `which X` asks whether X is on PATH, so a nonzero exit recorded for it
// says X was not found. The name is normalized the same way a signature's is,
// so one missing command is one row rather than two.
func (s *Server) addProbedCommands(ctx context.Context, where string, args []any, bucket func(string) *missingCommandBucket) error {
	has, err := s.hasTable(ctx, "session_commands")
	if err != nil || !has {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.command, c.session_id, COALESCE(c.occurred_at, '')
		FROM session_commands c
		JOIN sessions s ON s.id = c.session_id
		LEFT JOIN session_stats st ON st.session_id = s.id`+where+`
		  AND c.expected_exit = 0
		  AND (c.is_error = 1 OR (c.exit_code IS NOT NULL AND c.exit_code != 0))
		  AND instr(c.command, 'which ') > 0`, args...)
	if err != nil {
		return fmt.Errorf("api: probed commands: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var command, sessionID, occurred string
		if err := rows.Scan(&command, &sessionID, &occurred); err != nil {
			return fmt.Errorf("api: scan probed command: %w", err)
		}
		name, ok := friction.ProbedCommand(command)
		if !ok {
			continue
		}
		bucket(friction.NormalizeLine(name)).add(sessionID, occurred)
	}
	return rows.Err()
}

// missingCommandName pulls the command out of a recorded "not found" line.
// Every shell writes the name immediately before or after its own marker, so
// this reads the line rather than guessing from the tool input. A line with no
// marker is reported under the explicit unparsed key instead of being dropped.
func missingCommandName(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return unparsedCommandKey
	}
	lower := strings.ToLower(line)
	for _, marker := range []string{"command not found", "not recognized as an internal", "is not recognized"} {
		index := strings.Index(lower, marker)
		if index < 0 {
			continue
		}
		// zsh writes "command not found: foo"; bash writes "foo: command not found".
		if tail := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line[index+len(marker):]), ":")); tail != "" {
			if name := firstToken(tail); name != "" {
				return name
			}
		}
		head := strings.TrimRight(strings.TrimSpace(line[:index]), ": ")
		if cut := strings.LastIndexByte(head, ':'); cut >= 0 {
			head = head[cut+1:]
		}
		if name := lastToken(head); name != "" {
			return name
		}
	}
	// "ls exit 127" is the fallback signature the classifier writes when the
	// output carried no literal; the program name is its first token.
	if strings.Contains(lower, "exit 127") {
		if name := firstToken(line); name != "" && name != "exit" {
			return name
		}
	}
	return unparsedCommandKey
}

func firstToken(value string) string {
	fields := strings.Fields(strings.Trim(value, `"'`))
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[0], `"',:`)
}

func lastToken(value string) string {
	fields := strings.Fields(strings.Trim(value, `"'`))
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[len(fields)-1], `"',:`)
}

// failingPrograms is the failure rate of every program the window ran often
// enough to have a rate at all. The denominator is the calls whose outcome the
// source recorded, and the nonzero exits a program documents as an answer are
// not failures. A command line that named no program — one that only ran shell
// builtins or shell syntax — is left out entirely: a failure rate over "no
// program" ranks nothing.
func (s *Server) failingPrograms(ctx context.Context, where string, args []any) ([]failingProgram, error) {
	out := make([]failingProgram, 0)
	has, err := s.hasTable(ctx, "session_commands")
	if err != nil || !has {
		return out, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT TRIM(c.program) AS program,
		       COUNT(*),
		       SUM(CASE WHEN c.exit_code IS NOT NULL OR c.is_error IS NOT NULL THEN 1 ELSE 0 END) AS known_outcomes,
		       SUM(CASE WHEN c.expected_exit = 0
		                 AND (c.is_error = 1 OR (c.exit_code IS NOT NULL AND c.exit_code != 0)) THEN 1 ELSE 0 END) AS failures
		FROM session_commands c
		JOIN sessions s ON s.id = c.session_id
		LEFT JOIN session_stats st ON st.session_id = s.id`+where+`
		  AND c.program IS NOT NULL AND TRIM(c.program) <> ''
		GROUP BY program
		HAVING known_outcomes >= ?
		ORDER BY CAST(failures AS REAL) / known_outcomes DESC, failures DESC, program
		LIMIT 10`, append(append([]any{}, args...), minKnownOutcomes)...)
	if err != nil {
		return nil, fmt.Errorf("api: failing programs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item failingProgram
		if err := rows.Scan(&item.Program, &item.Calls, &nullableInt{&item.KnownOutcomes},
			&nullableInt{&item.Failures}); err != nil {
			return nil, fmt.Errorf("api: scan failing program: %w", err)
		}
		if item.KnownOutcomes > 0 {
			item.Rate = float64(item.Failures) / float64(item.KnownOutcomes)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// attachPeriod puts the current window's summary on a response, and — when the
// caller asked to compare and the window has a length — the previous window
// beside it with the movement between them.
func (s *Server) attachPeriod(ctx context.Context, response map[string]any, window overviewRange, scope periodScope, compare bool) error {
	current, err := s.buildPeriodSummary(ctx, window, scope)
	if err != nil {
		return err
	}
	response["current"] = current
	response["parallelism"] = current.Parallelism
	response["subagents"] = current.Subagents
	response["reread"] = current.Reread
	response["environment"] = current.Environment
	response["previous"], response["delta"] = nil, nil
	if !compare {
		return nil
	}
	previousRange, ok := previousWindow(window)
	if !ok {
		return nil
	}
	previous, err := s.buildPeriodSummary(ctx, previousRange, scope)
	if err != nil {
		return err
	}
	response["previous"], response["delta"] = previous, periodDelta(current, previous)
	return nil
}

func wantsCompare(value string) bool { return value == "1" || value == "true" }
