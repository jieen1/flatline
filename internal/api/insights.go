package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// The insights endpoint projects facts that are already in the database onto
// the question the raw counts do not answer: where the recorded cost and the
// recorded pain concentrate. Everything here is a read-time aggregation of
// canonical tables (ADR-20): no new tables, no writes, no scores. Each insight
// carries its own selection rule in one sentence, and every number can be
// drilled into through the links.
//
// Two things are deliberately absent. There is no token estimate for an
// interrupted turn, because per-message usage is not in the store and an
// estimate would be fabricated. And "zero recorded edits" is not "produced
// nothing": the lines columns only count what edit tools recorded, so the
// criterion says exactly that and leaves the judgement to the reader.

// zeroEditTokenThreshold is what makes a session "high-input": five million
// total tokens is far above a single question, and the number is part of the
// selection rule, not a tuned score.
const zeroEditTokenThreshold = 5_000_000

// stuckLoopThreshold is how many times one friction signature must repeat
// inside one session before the pair counts as a loop.
const stuckLoopThreshold = 5

// insightTopRows caps every per-item list. The endpoint names a pattern, not a
// leaderboard; the links lead to the full set.
const insightTopRows = 5

// waitingTools are the tools whose call is an exercise in waiting on a command
// or a background process. The set is part of the interrupt criterion sentence.
var waitingTools = map[string]struct{}{"exec_command": {}, "write_stdin": {}, "wait": {}}

type insightLink struct {
	Href    string `json:"href"`
	Label   string `json:"label"`
	LabelEN string `json:"label_en"`
}

type insight struct {
	Kind        string         `json:"kind"`
	Title       string         `json:"title"`
	TitleEN     string         `json:"title_en"`
	Summary     string         `json:"summary"`
	SummaryEN   string         `json:"summary_en"`
	Criterion   string         `json:"criterion"`
	CriterionEN string         `json:"criterion_en"`
	Facts       map[string]any `json:"facts"`
	Links       []insightLink  `json:"links"`
}

func (s *Server) handleInsights(w http.ResponseWriter, r *http.Request) {
	rangeSpec, _, _, err := overviewWindow(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	scope, err := s.mainSessionScope(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	periodScope := periodScope{}
	if scope != "" {
		periodScope.scope = scope
	}
	sessionWhere, sessionArgs := periodScope.where(rangeSpec, true)

	insights := make([]insight, 0, 6)
	add := func(item insight, err error) bool {
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return false
		}
		if item.Facts != nil {
			insights = append(insights, item)
		}
		return true
	}
	if !add(s.interruptInsight(ctx, rangeSpec)) {
		return
	}
	if !add(s.zeroEditInsight(ctx, sessionWhere, sessionArgs, rangeSpec)) {
		return
	}
	if !add(s.stuckLoopInsight(ctx, rangeSpec)) {
		return
	}
	if !add(s.rereadInsight(ctx, sessionWhere, sessionArgs)) {
		return
	}
	if !add(s.coverageGapInsight(ctx, rangeSpec)) {
		return
	}
	if !add(s.missingCommandInsight(ctx, sessionWhere, sessionArgs)) {
		return
	}
	watchCounts, err := s.watchStatusCounts(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"range":        rangeSpec,
		"scope":        scopeNote(scope),
		"insights":     insights,
		"watches":      watchCounts,
		"data_version": s.dataVersion(),
	})
}

// watchStatusCounts is the fix-verification scoreboard: how many watches are
// open, how many verdicts say the rule helped, and how many say it did not.
func (s *Server) watchStatusCounts(ctx context.Context) (map[string]int, error) {
	watches, err := s.loadWatches(ctx)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{"watching": 0, "verified": 0, "no_change": 0, "unobservable": 0}
	for _, watch := range watches {
		status := watch.Status
		if watch.Evaluation != nil {
			status = watch.Evaluation.Status
		}
		if _, known := counts[status]; known {
			counts[status]++
		}
	}
	return counts, nil
}

func scopeNote(scope string) map[string]any {
	note := "会话类洞察默认只统计主会话（thread_kind='main'）；摩擦与中断类洞察统计全部会话。"
	noteEN := "Session-based insights count main sessions (thread_kind='main') only; friction and interrupt insights count every session."
	if scope == "" {
		note = "主会话过滤规则尚未就绪，会话类洞察统计全部会话。"
		noteEN = "The main-session rule is not available yet, so session-based insights count every session."
	}
	return map[string]any{"main_sessions_only": scope != "", "note": note, "note_en": noteEN}
}

// interruptInsight aggregates user interrupts: how many, where they
// concentrate, which tool was the session running when the user stopped it,
// and — for the sources that record per-message usage (Claude Code) — how many
// tokens the interrupted turn had already spent. The turn is "since the last
// user message"; a source that records no per-message usage stays out of that
// denominator instead of being filled with a zero.
func (s *Server) interruptInsight(ctx context.Context, window overviewRange) (insight, error) {
	out := insight{Kind: "interrupts",
		Title: "中断集中在哪里", TitleEN: "Where interrupts concentrate",
		Criterion:   "统计 friction_kind='user_interrupt' 的记录；“中断前工具”是同一会话中时间不晚于该中断的最后一次 transcript_tool_call 的工具名；等待集 = exec_command / write_stdin / wait。中断轮次 token 按来源取数：claude_code = 中断与前一条用户消息之间 assistant 消息 usage.total_tokens 之和（ADR-22）；codex = 解析器按运行总量差值归属到轮次用户消息的 turn_tokens（ADR-23）；其余来源不计入分母。项目行附带该项目可测中断的轮次 token 合计。按次数排序。",
		CriterionEN: "Counts records with friction_kind='user_interrupt'; the tool before an interrupt is the tool name of the last transcript_tool_call at or before it in the same session; the waiting set is exec_command / write_stdin / wait. Per source: claude_code sums usage.total_tokens over the assistant messages between the interrupt and the last user message (ADR-22); codex reads the turn cost the parser attributed to the turn's own user message by subtracting the harness's running total (ADR-23); other sources stay out of the measurable count. Project rows also carry that project's measurable turn-token total. Sorted by count.",
		Facts:       nil, Links: []insightLink{
			{Href: "#/friction?kind=user_interrupt", Label: "查看中断记录", LabelEN: "Open interrupt records"},
		}}
	where, args := " WHERE f.friction_kind = 'user_interrupt'", make([]any, 0, 2)
	if window.From != nil {
		where += " AND f.occurred_at >= ?"
		args = append(args, *window.From)
	}
	if window.To != nil {
		where += " AND f.occurred_at <= ?"
		args = append(args, *window.To)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.session_id, f.occurred_at, COALESCE(f.source_event_id, ''), s.project_key, s.source,
		  (SELECT e.payload_json FROM events e
		    WHERE e.session_id = f.session_id AND e.event_type = 'transcript_tool_call'
		      AND e.occurred_at <= f.occurred_at
		    ORDER BY e.occurred_at DESC, e.id DESC LIMIT 1)
		FROM friction_records f JOIN sessions s ON s.id = f.session_id`+where, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	projects, tools := map[string]int{}, map[string]int{}
	total, waiting := 0, 0
	interrupts := make([]interruptRef, 0, 64)
	for rows.Next() {
		var sessionID, sourceEventID, project, source, at string
		var payload *string
		if err := rows.Scan(&sessionID, &at, &sourceEventID, &project, &source, &payload); err != nil {
			return out, err
		}
		total++
		interrupts = append(interrupts, interruptRef{sessionID: sessionID, sourceEventID: sourceEventID, source: source, project: project, at: at})
		key := project
		if key == "" {
			key = unrecordedKey
		}
		projects[key]++
		if payload != nil {
			var fields struct {
				ToolName string `json:"tool_name"`
			}
			if json.Unmarshal([]byte(*payload), &fields) == nil && fields.ToolName != "" {
				tools[fields.ToolName]++
				if _, isWaiting := waitingTools[fields.ToolName]; isWaiting {
					waiting++
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if total == 0 {
		return out, nil
	}
	sessionCount, err := s.countInterruptSessions(ctx, where, args)
	if err != nil {
		return out, err
	}
	// The interrupted turn's tokens: measurable only where the source records
	// per-message or per-turn usage. The denominator says how many interrupts
	// that is; the per-project split is what makes the number actionable.
	turnTokens, measured, projectTokens := s.interruptTurnTokens(ctx, interrupts)
	topProjects := topCounts(projects, insightTopRows)
	for _, entry := range topProjects {
		if sums, found := projectTokens[entry["key"].(string)]; found {
			entry["turn_tokens"] = sums.total
			entry["turn_measured"] = sums.measured
		}
	}
	out.Facts = map[string]any{
		"total":             total,
		"sessions":          sessionCount,
		"top_projects":      topProjects,
		"last_tools":        topCounts(tools, insightTopRows),
		"waiting_share":     map[string]int{"numerator": waiting, "denominator": total},
		"turn_tokens_total": turnTokens,
		"turn_measured":     measured,
	}
	out.Summary = interruptSummary(total, sessionCount, projects, tools, waiting)
	out.SummaryEN = interruptSummaryEN(total, sessionCount, projects, tools, waiting)
	if measured > 0 {
		out.Summary += "可测的 " + strconv.Itoa(measured) + " 次中断轮次合计已投入 " + strconv.FormatInt(turnTokens, 10) + " token（消息级记录了 token 的来源）。"
		out.SummaryEN += " The " + strconv.Itoa(measured) + " measurable interrupted turns had already spent " + strconv.FormatInt(turnTokens, 10) + " tokens in total."
	}
	return out, nil
}

// interruptRef locates one interrupt for the turn-token lookup.
type interruptRef struct {
	sessionID     string
	sourceEventID string
	source        string
	project       string
	at            string
}

func (s *Server) countInterruptSessions(ctx context.Context, where string, args []any) (int, error) {
	sessionRows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT f.session_id FROM friction_records f`+where, args...)
	if err != nil {
		return 0, err
	}
	defer sessionRows.Close()
	sessions := map[string]struct{}{}
	for sessionRows.Next() {
		var id string
		if err := sessionRows.Scan(&id); err != nil {
			return 0, err
		}
		sessions[id] = struct{}{}
	}
	return len(sessions), sessionRows.Err()
}

// interruptTurnTokens sums, over every measurable interrupt, what its turn
// had already spent, per the source's own recording: Claude Code records
// per-message usage, so the turn is the assistant messages since the last user
// message; Codex reports a running session total, so the turn is the
// subtraction the parser already attributed to the user message that opened it
// (ADR-22/23). An interrupt whose source records neither stays out of the
// measurable count.
type interruptTokenSum struct {
	total    int64
	measured int
}

func (s *Server) interruptTurnTokens(ctx context.Context, interrupts []interruptRef) (int64, int, map[string]interruptTokenSum) {
	total := int64(0)
	measured := 0
	byProject := map[string]interruptTokenSum{}
	for _, item := range interrupts {
		var cost int64
		var ok bool
		switch item.source {
		case "codex":
			cost, ok = s.codexInterruptTurnTokens(ctx, item)
		case "claude_code":
			cost, ok = s.claudeInterruptTurnTokens(ctx, item)
		}
		if !ok {
			continue
		}
		total += cost
		measured++
		key := item.project
		if key == "" {
			key = unrecordedKey
		}
		sums := byProject[key]
		sums.total += cost
		sums.measured++
		byProject[key] = sums
	}
	return total, measured, byProject
}

// codexInterruptTurnTokens reads the turn cost the parser attributed to the
// user message that opened the interrupted turn. The interrupt's own recording
// is excluded — it is itself written as a user message.
func (s *Server) codexInterruptTurnTokens(ctx context.Context, item interruptRef) (int64, bool) {
	var cost *int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT json_extract(e.payload_json, '$.turn_tokens')
		FROM events e
		WHERE e.session_id = ? AND e.event_type = 'transcript_message'
		  AND json_extract(e.payload_json, '$.role') = 'user'
		  AND json_extract(e.payload_json, '$.turn_tokens') IS NOT NULL
		  AND e.source_event_id IS NOT NULL AND e.source_event_id <> ?
		  AND e.occurred_at <= ?
		ORDER BY e.occurred_at DESC, e.id DESC LIMIT 1`,
		item.sessionID, item.sourceEventID, item.at).Scan(&cost); err != nil || cost == nil {
		return 0, false
	}
	return *cost, true
}

// claudeInterruptTurnTokens sums the assistant messages' total_tokens between
// the interrupt and the last user message before it.
func (s *Server) claudeInterruptTurnTokens(ctx context.Context, item interruptRef) (int64, bool) {
	var turnStart *string
	// See codexInterruptTurnTokens: the interrupt records itself as a user
	// message and must not become its own turn start.
	if err := s.db.QueryRowContext(ctx, `
		SELECT MAX(e.occurred_at) FROM events e
		WHERE e.session_id = ? AND e.event_type = 'transcript_message'
		  AND json_extract(e.payload_json, '$.role') = 'user'
		  AND e.source_event_id IS NOT NULL AND e.source_event_id <> ?
		  AND e.occurred_at <= ?`, item.sessionID, item.sourceEventID, item.at).Scan(&turnStart); err != nil {
		return 0, false
	}
	if turnStart == nil || *turnStart == "" {
		return 0, false
	}
	var sum *int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT SUM(CAST(json_extract(e.payload_json, '$.usage.total_tokens') AS INTEGER))
		FROM events e
		WHERE e.session_id = ? AND e.event_type = 'transcript_message'
		  AND json_extract(e.payload_json, '$.role') = 'assistant'
		  AND json_extract(e.payload_json, '$.usage.total_tokens') IS NOT NULL
		  AND e.occurred_at > ? AND e.occurred_at <= ?`,
		item.sessionID, *turnStart, item.at).Scan(&sum); err != nil {
		return 0, false
	}
	if sum == nil {
		return 0, false
	}
	return *sum, true
}

func interruptSummary(total, sessions int, projects, tools map[string]int, waiting int) string {
	var b strings.Builder
	b.WriteString("窗口内 ")
	b.WriteString(strconv.Itoa(total))
	b.WriteString(" 次用户中断，出现在 ")
	b.WriteString(strconv.Itoa(sessions))
	b.WriteString(" 个会话里")
	if key, count, ok := topOne(projects); ok && key != unrecordedKey {
		b.WriteString("，最多在 ")
		b.WriteString(key)
		b.WriteString("（")
		b.WriteString(strconv.Itoa(count))
		b.WriteString(" 次）")
	}
	if tool, count, ok := topOne(tools); ok {
		b.WriteString("；中断前最后一次调用最常见的是 ")
		b.WriteString(tool)
		b.WriteString("（")
		b.WriteString(strconv.Itoa(count))
		b.WriteString(" 次）")
	}
	if waiting > 0 {
		b.WriteString("；")
		b.WriteString(strconv.Itoa(waiting))
		b.WriteString(" 次发生在等待命令或后台进程的工具上")
	}
	b.WriteString("。")
	return b.String()
}

func interruptSummaryEN(total, sessions int, projects, tools map[string]int, waiting int) string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(total))
	b.WriteString(" user interrupts in ")
	b.WriteString(strconv.Itoa(sessions))
	b.WriteString(" sessions")
	if key, count, ok := topOne(projects); ok && key != unrecordedKey {
		b.WriteString(", most in ")
		b.WriteString(key)
		b.WriteString(" (")
		b.WriteString(strconv.Itoa(count))
		b.WriteString(")")
	}
	if tool, count, ok := topOne(tools); ok {
		b.WriteString("; the tool running right before was most often ")
		b.WriteString(tool)
		b.WriteString(" (")
		b.WriteString(strconv.Itoa(count))
		b.WriteString(")")
	}
	if waiting > 0 {
		b.WriteString("; ")
		b.WriteString(strconv.Itoa(waiting))
		b.WriteString(" hit a tool that waits on a command or background process")
	}
	b.WriteString(".")
	return b.String()
}

// zeroEditInsight names the window's high-input sessions that recorded no edit
// -tool changes. Research sessions legitimately look like this, which is why the
// criterion spells out what "no edits" does and does not mean.
func (s *Server) zeroEditInsight(ctx context.Context, where string, args []any, window overviewRange) (insight, error) {
	out := insight{Kind: "zero_edit_heavy",
		Title: "零改动的高投入会话", TitleEN: "High-input sessions with no recorded edits",
		Criterion:   "主会话、session_usage.total_tokens ≥ 5,000,000、且 lines_added+lines_removed = 0；改动行只统计编辑类工具（Edit/Write/apply_patch）记录的改动，bash 改写不计入；调研/分析会话本来就可能零改动。按 token 降序取前 5。",
		CriterionEN: "Main sessions with session_usage.total_tokens >= 5,000,000 and lines_added+lines_removed = 0; the lines columns only count what edit tools (Edit/Write/apply_patch) recorded, so a bash rewrite is not counted; a research or analysis session can legitimately have zero. Top 5 by tokens.",
		Facts:       nil, Links: []insightLink{sessionListLink(window)}}
	zeroWhere := where + " AND u.total_tokens >= ? AND COALESCE(u.lines_added, 0) + COALESCE(u.lines_removed, 0) = 0"
	zeroArgs := append(append([]any{}, args...), zeroEditTokenThreshold)
	var count int
	var tokens int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), SUM(u.total_tokens)
		FROM sessions s
		JOIN session_usage u ON u.session_id = s.id
		LEFT JOIN session_stats st ON st.session_id = s.id`+zeroWhere, zeroArgs...).
		Scan(&count, &nullableInt64Sum{&tokens}); err != nil {
		return out, err
	}
	if count == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id, COALESCE(s.title, ''), COALESCE(s.project_key, ''), u.total_tokens,
		       COALESCE(st.duration_ms, 0)
		FROM sessions s
		JOIN session_usage u ON u.session_id = s.id
		LEFT JOIN session_stats st ON st.session_id = s.id`+zeroWhere+`
		ORDER BY u.total_tokens DESC LIMIT ?`, append(append([]any{}, zeroArgs...), insightTopRows)...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	type sessionRow struct {
		ID          string `json:"session_id"`
		Title       string `json:"title"`
		ProjectKey  string `json:"project_key"`
		TotalTokens int64  `json:"total_tokens"`
		DurationMS  int64  `json:"duration_ms"`
	}
	top := make([]sessionRow, 0, insightTopRows)
	for rows.Next() {
		var row sessionRow
		if err := rows.Scan(&row.ID, &row.Title, &row.ProjectKey, &row.TotalTokens, &row.DurationMS); err != nil {
			return out, err
		}
		top = append(top, row)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	total := tokens
	out.Facts = map[string]any{
		"count":            count,
		"total_tokens":     total,
		"threshold_tokens": int64(zeroEditTokenThreshold),
		"top":              top,
	}
	out.Summary = "窗口内 " + strconv.Itoa(count) + " 个主会话 token ≥ 500 万且没有记录到编辑工具改动，合计 " + strconv.FormatInt(total, 10) + " token。"
	out.SummaryEN = strconv.Itoa(count) + " main sessions in this window spent 5M or more tokens with no edit-tool change recorded, " + strconv.FormatInt(total, 10) + " tokens in total."
	return out, nil
}

// stuckLoopInsight names signature-and-session pairs where the same recorded
// outcome came back at least five times — a session repeating a failing action.
func (s *Server) stuckLoopInsight(ctx context.Context, window overviewRange) (insight, error) {
	out := insight{Kind: "stuck_loops",
		Title: "同一失败动作的重复循环", TitleEN: "Loops of the same failed action",
		Criterion:   "按（签名, 会话）分组计数，≥5 次的组计入；用户中断不算失败动作；按次数降序取前 5。",
		CriterionEN: "Groups by (signature, session); a pair counts once it repeated at least 5 times; user interrupts are not a failed action; top 5 by count.",
		Facts:       nil, Links: []insightLink{
			{Href: "#/friction", Label: "打开摩擦页", LabelEN: "Open friction page"},
		}}
	where, args := " WHERE f.signature <> '' AND f.friction_kind <> 'user_interrupt'", make([]any, 0, 2)
	if window.From != nil {
		where += " AND f.occurred_at >= ?"
		args = append(args, *window.From)
	}
	if window.To != nil {
		where += " AND f.occurred_at <= ?"
		args = append(args, *window.To)
	}
	having := " GROUP BY f.signature, f.session_id HAVING COUNT(*) >= ?"
	loopArgs := append(append([]any{}, args...), stuckLoopThreshold)
	var groups int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
		  SELECT f.signature, f.session_id FROM friction_records f`+where+having+`)`, loopArgs...).Scan(&groups); err != nil {
		return out, err
	}
	if groups == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.signature, f.session_id, COALESCE(s.project_key, ''), s.source, COUNT(*) AS c
		FROM friction_records f JOIN sessions s ON s.id = f.session_id`+where+having+`
		ORDER BY c DESC, f.signature LIMIT ?`,
		append(append([]any{}, loopArgs...), insightTopRows)...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	type loopRow struct {
		Signature  string `json:"signature"`
		SessionID  string `json:"session_id"`
		ProjectKey string `json:"project_key"`
		Count      int    `json:"count"`
		TurnTokens *int64 `json:"turn_tokens"`
		TurnsSeen  int    `json:"turns_measured"`
	}
	top := make([]loopRow, 0, insightTopRows)
	for rows.Next() {
		var row loopRow
		var source string
		if err := rows.Scan(&row.Signature, &row.SessionID, &row.ProjectKey, &source, &row.Count); err != nil {
			return out, err
		}
		top = append(top, row)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	// What the looped turns cost, per the source's own recording (ADR-22/23):
	// every friction record in the loop names the turn it happened in, and a
	// turn is counted once even when the loop hit it several times.
	for index := range top {
		cost, turns, err := s.loopTurnTokens(ctx, top[index].Signature, top[index].SessionID)
		if err != nil {
			return out, err
		}
		if turns > 0 {
			top[index].TurnTokens = &cost
			top[index].TurnsSeen = turns
		}
	}
	out.Facts = map[string]any{"groups": groups, "threshold": stuckLoopThreshold, "top": top}
	out.Summary = "窗口内 " + strconv.Itoa(groups) + " 组「同一签名在同一会话连撞 ≥" + strconv.Itoa(stuckLoopThreshold) + " 次」，最严重的一组重复了 " + strconv.Itoa(top[0].Count) + " 次。"
	out.SummaryEN = strconv.Itoa(groups) + " signature-and-session pairs repeated the same recorded outcome at least " + strconv.Itoa(stuckLoopThreshold) + " times; the worst repeated " + strconv.Itoa(top[0].Count) + " times."
	return out, nil
}

// loopTurnTokens sums, over the turns that contain this signature's records in
// this session, what each turn cost by the source's own recording. A turn is
// counted once no matter how many of the loop's records landed in it; turns
// whose cost the source does not record stay out of both sums.
func (s *Server) loopTurnTokens(ctx context.Context, signature, sessionID string) (int64, int, error) {
	var source string
	if err := s.db.QueryRowContext(ctx, `SELECT source FROM sessions WHERE id = ?`, sessionID).Scan(&source); err != nil {
		return 0, 0, err
	}
	turns, err := s.db.QueryContext(ctx, `
		SELECT f.occurred_at, f.source_event_id, (
		  SELECT e.occurred_at FROM events e
		  WHERE e.session_id = f.session_id AND e.event_type = 'transcript_message'
		    AND json_extract(e.payload_json, '$.role') = 'user'
		    AND e.source_event_id IS NOT NULL AND e.source_event_id <> f.source_event_id
		    AND e.occurred_at <= f.occurred_at
		  ORDER BY e.occurred_at DESC, e.id DESC LIMIT 1
		)
		FROM friction_records f
		WHERE f.signature = ? AND f.session_id = ? AND f.occurred_at IS NOT NULL
		ORDER BY f.occurred_at`, signature, sessionID)
	if err != nil {
		return 0, 0, err
	}
	defer turns.Close()
	type span struct {
		start string
		at    string
		event string
	}
	spans := make([]span, 0, 32)
	for turns.Next() {
		var at, eventID string
		var start *string
		if err := turns.Scan(&at, &eventID, &start); err != nil {
			return 0, 0, err
		}
		if start == nil || *start == "" {
			continue
		}
		spans = append(spans, span{start: *start, at: at, event: eventID})
	}
	if err := turns.Err(); err != nil {
		return 0, 0, err
	}
	total := int64(0)
	seen := map[string]struct{}{}
	for _, item := range spans {
		key := item.start
		if _, dup := seen[key]; dup {
			continue
		}
		var cost *int64
		switch source {
		case "codex":
			// The turn's cost sits on the user message that opened it.
			if err := s.db.QueryRowContext(ctx, `
				SELECT json_extract(e.payload_json, '$.turn_tokens')
				FROM events e
				WHERE e.session_id = ? AND e.event_type = 'transcript_message'
				  AND e.occurred_at = ? AND json_extract(e.payload_json, '$.role') = 'user'
				  AND json_extract(e.payload_json, '$.turn_tokens') IS NOT NULL
				ORDER BY e.id DESC LIMIT 1`, sessionID, key).Scan(&cost); err != nil {
				return 0, 0, err
			}
		case "claude_code":
			if err := s.db.QueryRowContext(ctx, `
				SELECT SUM(CAST(json_extract(e.payload_json, '$.usage.total_tokens') AS INTEGER))
				FROM events e
				WHERE e.session_id = ? AND e.event_type = 'transcript_message'
				  AND json_extract(e.payload_json, '$.role') = 'assistant'
				  AND json_extract(e.payload_json, '$.usage.total_tokens') IS NOT NULL
				  AND e.occurred_at > ? AND e.occurred_at <= ?`,
				sessionID, key, item.at).Scan(&cost); err != nil {
				return 0, 0, err
			}
		default:
			continue
		}
		if cost != nil {
			seen[key] = struct{}{}
			total += *cost
		}
	}
	return total, len(seen), nil
}

// rereadInsight reuses the overview reread block's own computation, so the two
// pages cannot disagree about what a repeated read is.
func (s *Server) rereadInsight(ctx context.Context, where string, args []any) (insight, error) {
	out := insight{Kind: "reread",
		Title: "重复读取的文件", TitleEN: "Files read over and over",
		Criterion: rereadNote, CriterionEN: rereadNoteEN,
		Facts: nil, Links: []insightLink{}}
	summary, err := s.reread(ctx, where, args)
	if err != nil || summary.Sessions == 0 {
		return out, err
	}
	links := make([]insightLink, 0, len(summary.TopFiles))
	for _, file := range summary.TopFiles {
		if len(links) >= 3 {
			break
		}
		links = append(links, insightLink{
			Href: "#/sessions?file=" + url.QueryEscape(file.Path), Label: file.Path, LabelEN: file.Path})
	}
	out.Links = links
	files := summary.TopFiles
	if len(files) > 3 {
		files = files[:3]
	}
	out.Facts = map[string]any{
		"sessions": summary.Sessions, "reads": summary.Reads,
		"threshold": summary.Threshold, "top_files": files,
	}
	out.Summary = "窗口内 " + strconv.Itoa(summary.Sessions) + " 个会话把同一文件读了 ≥" + strconv.Itoa(summary.Threshold) + " 次，合计 " + strconv.Itoa(summary.Reads) + " 次读取。"
	out.SummaryEN = strconv.Itoa(summary.Sessions) + " sessions read the same file at least " + strconv.Itoa(summary.Threshold) + " times, " + strconv.Itoa(summary.Reads) + " reads in total."
	return out, nil
}

// coverageGapInsight reuses the friction page's own coverage machinery, so an
// insight here and a gap there are the same sentence.
func (s *Server) coverageGapInsight(ctx context.Context, window overviewRange) (insight, error) {
	out := insight{Kind: "coverage_gaps",
		Title: "反复出现但规则没提到的机制", TitleEN: "Recurring mechanisms no rule mentions",
		Criterion: coverageGapNote, CriterionEN: coverageGapNoteEN,
		Facts: nil, Links: []insightLink{
			{Href: "#/friction", Label: "打开摩擦页", LabelEN: "Open friction page"},
		}}
	filters := overviewFrictionFilters(window, 0, 0)
	set, err := s.loadFrictionSet(ctx, filters)
	if err != nil {
		return out, err
	}
	mentions, err := s.keywordCoverage(ctx)
	if err != nil {
		return out, err
	}
	gaps := set.coverageGaps(mentions)
	if len(gaps) == 0 {
		return out, nil
	}
	type gapRow struct {
		Signature    string `json:"signature"`
		ProjectKey   string `json:"project_key"`
		SessionCount int    `json:"session_count"`
		Mechanism    string `json:"mechanism"`
		MechanismEN  string `json:"mechanism_en"`
	}
	rows := make([]gapRow, 0, len(gaps))
	links := make([]insightLink, 0, 3)
	for _, gap := range gaps {
		rows = append(rows, gapRow{Signature: gap.Signature, ProjectKey: gap.ProjectKey,
			SessionCount: gap.SessionCount, Mechanism: gap.Mechanism, MechanismEN: gap.MechanismEN})
		if len(links) < 3 {
			links = append(links, insightLink{
				Href:  "#/friction?group=signature&signature=" + url.QueryEscape(gap.Signature),
				Label: frictionSignatureLine(gap.Signature), LabelEN: frictionSignatureLine(gap.Signature)})
		}
	}
	out.Links = links
	out.Facts = map[string]any{"gaps": rows}
	out.Summary = strconv.Itoa(len(rows)) + " 个（签名 × 项目）组合反复出现，且该项目适用的规则资产没有提到该机制的关键词。"
	out.SummaryEN = strconv.Itoa(len(rows)) + " signature-and-project pairs recur while no rule applicable to that project mentions the mechanism."
	return out, nil
}

// missingCommandInsight reuses the overview environment block's own
// computation for the missing-command half.
func (s *Server) missingCommandInsight(ctx context.Context, where string, args []any) (insight, error) {
	out := insight{Kind: "missing_commands",
		Title: "不在 PATH 里的命令", TitleEN: "Commands missing from PATH",
		Criterion:   "判定口径与总览「环境」区块一致：category='command_not_found' 的样例行，或整条命令行只有 `which X` 且退出非零。按次数排序取前 5。",
		CriterionEN: "Same rule as the overview environment block: the sample line of a category='command_not_found' signature, or a call whose whole line is `which X` and which exited nonzero. Top 5 by count.",
		Facts:       nil, Links: []insightLink{
			{Href: "#/friction?category=command_not_found", Label: "查看 command_not_found 记录", LabelEN: "Open command_not_found records"},
		}}
	summary, err := s.environment(ctx, where, args)
	if err != nil || len(summary.MissingCommands) == 0 {
		return out, err
	}
	commands := summary.MissingCommands
	if len(commands) > insightTopRows {
		commands = commands[:insightTopRows]
	}
	total := 0
	for _, command := range summary.MissingCommands {
		total += command.Count
	}
	out.Facts = map[string]any{"commands": commands, "total": total}
	out.Summary = "窗口内 " + strconv.Itoa(len(summary.MissingCommands)) + " 个命令被调用但没有找到，共 " + strconv.Itoa(total) + " 次。"
	out.SummaryEN = strconv.Itoa(len(summary.MissingCommands)) + " commands were called and not found in this window, " + strconv.Itoa(total) + " calls in total."
	return out, nil
}

func sessionListLink(window overviewRange) insightLink {
	href := "#/sessions?thread=main&empty=all"
	if window.From != nil {
		href += "&from=" + url.QueryEscape(*window.From)
	}
	if window.To != nil {
		href += "&to=" + url.QueryEscape(*window.To)
	}
	return insightLink{Href: href, Label: "查看这一窗口的会话", LabelEN: "Open this window's sessions"}
}

// topCounts sorts a count map into at most limit rows, count first.
func topCounts(counts map[string]int, limit int) []map[string]any {
	out := make([]map[string]any, 0, len(counts))
	for key, count := range counts {
		out = append(out, map[string]any{"key": key, "count": count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i]["count"] != out[j]["count"] {
			return out[i]["count"].(int) > out[j]["count"].(int)
		}
		return out[i]["key"].(string) < out[j]["key"].(string)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func topOne(counts map[string]int) (string, int, bool) {
	rows := topCounts(counts, 1)
	if len(rows) == 0 {
		return "", 0, false
	}
	return rows[0]["key"].(string), rows[0]["count"].(int), true
}
