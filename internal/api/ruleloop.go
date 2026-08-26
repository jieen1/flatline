package api

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"flatline/internal/friction"
)

// The rule loop (ADR-21): a recurring friction signature becomes a rule brief
// the user hands to their own agent, and after the user writes the rule the
// signature can be watched until the facts say whether the rule helped. This
// file holds both halves: briefs are a read-time projection over facts that
// already exist, watches are the one new user-intent write path, and neither
// ever calls a model.

// briefSampleLimit is how many raw sample lines a brief carries. Three is
// enough to see the shape of the failure without turning the brief into a log.
const briefSampleLimit = 3

// briefSampleBound caps one sample line. The line is evidence, not a dump.
const briefSampleBound = 200

// signatureBrief is the evidence pack for one recurring signature, plus a
// prompt the user can paste into their own agent. Every sentence in it comes
// from the fact layer; the prompt asks the agent to draft, never claims a fix.
type signatureBrief struct {
	// Mechanism is the hint dictionary's answer, or null when no rule covers
	// the signature — the brief says so instead of guessing.
	Mechanism     *friction.Hint `json:"mechanism"`
	Target        briefTarget    `json:"target"`
	Evidence      briefEvidence  `json:"evidence"`
	PastePrompt   string         `json:"paste_prompt"`
	PastePromptEN string         `json:"paste_prompt_en"`
	Criterion     string         `json:"criterion"`
	CriterionEN   string         `json:"criterion_en"`
}

type briefTarget struct {
	// Kind is one of rule / hook / skill / environment / workflow / unrecorded.
	Kind      string `json:"kind"`
	Reason    string `json:"reason"`
	ReasonEN  string `json:"reason_en"`
	KindLabel string `json:"kind_label"`
}

type briefEvidence struct {
	Count        int               `json:"count"`
	SessionCount int               `json:"session_count"`
	ProjectCount int               `json:"project_count"`
	FirstSeenAt  string            `json:"first_seen_at,omitempty"`
	LastSeenAt   string            `json:"last_seen_at,omitempty"`
	SampleLines  []string          `json:"sample_lines"`
	TopProjects  []briefProjectRef `json:"top_projects"`
}

type briefProjectRef struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

// briefTargetFor maps a mechanism kind to where a fix should live. The mapping
// is the one written down in ADR-21: enforcement belongs in a hook, behaviour
// guidance in a rule, a missing binary in the environment, and the user's own
// interruptions are a workflow question, not an agent instruction.
func briefTargetFor(hint *friction.Hint) briefTarget {
	if hint == nil {
		return briefTarget{Kind: "unrecorded", KindLabel: "机制未记录",
			Reason:   "提示字典没有覆盖这个签名：先带着样例问你的 agent 这是什么机制，再决定落点。",
			ReasonEN: "The mechanism dictionary does not cover this signature: show your agent the samples first, then decide where the fix belongs."}
	}
	switch hint.Kind {
	case friction.HintUserHook, friction.HintPermission:
		return briefTarget{Kind: "hook", KindLabel: "hook",
			Reason:   "这是拦截/批准类机制，规则只是上下文、不保证被遵守；用 hook 在工具层强制。",
			ReasonEN: "This is an enforcement or approval mechanism; a rule is context and is not guaranteed to be followed, so enforce it with a hook."}
	case friction.HintEnvironment:
		return briefTarget{Kind: "environment", KindLabel: "环境修复",
			Reason:   "命令或包缺失是环境问题：修 PATH 或安装，写规则不解决。",
			ReasonEN: "A missing command or package is an environment problem: fix PATH or install it; a rule will not."}
	case friction.HintUserStopped:
		return briefTarget{Kind: "workflow", KindLabel: "工作流",
			Reason:   "中断是你自己的动作：简报改为工作流反思——看看中断前 agent 在做什么模式。",
			ReasonEN: "The interrupt is your own action; treat the brief as a workflow review of what the agent was doing when you stopped it."}
	case friction.HintTest, friction.HintBuild:
		return briefTarget{Kind: "hook", KindLabel: "hook",
			Reason:   "编译/测试失败适合在收尾前强制跑一次检查（hook），比提醒可靠。",
			ReasonEN: "Build and test failures are best caught by running the check before wrap-up (a hook), which is more reliable than a reminder."}
	default:
		return briefTarget{Kind: "rule", KindLabel: "rule",
			Reason:   "行为指引类机制：一条具体、可验证的规则（AGENTS.md / rules）适用。",
			ReasonEN: "A behaviour-guidance mechanism: a concrete, verifiable rule in AGENTS.md or rules fits."}
	}
}

// briefPastePrompt is what the user copies into their own agent. It carries the
// evidence and asks for a draft; it never claims the rule will fix anything.
func briefPastePrompt(signature string, target briefTarget, hint *friction.Hint, evidence briefEvidence) (string, string) {
	mechanism := "提示字典未覆盖，请先根据样例判断机制"
	mechanismEN := "Not in the mechanism dictionary; judge the mechanism from the samples first"
	if hint != nil {
		mechanism, mechanismEN = hint.Mechanism, hint.MechanismEN
	}
	samples := make([]string, 0, len(evidence.SampleLines))
	for _, line := range evidence.SampleLines {
		samples = append(samples, "  - "+line)
	}
	samplesEN := make([]string, 0, len(evidence.SampleLines))
	for _, line := range evidence.SampleLines {
		samplesEN = append(samplesEN, "  - "+line)
	}
	projects := make([]string, 0, len(evidence.TopProjects))
	for _, project := range evidence.TopProjects {
		projects = append(projects, project.Key)
	}
	zh := "请为我的 agent 起草一条" + target.KindLabel + "，用于消除一个反复出现的摩擦。以下证据来自本机会话历史（Flatline 提取，未做任何归因）：\n" +
		"- 签名：" + signature + "\n" +
		"- 机制：" + mechanism + "\n" +
		"- 规模：出现在 " + strconv.Itoa(evidence.SessionCount) + " 个会话、共 " + strconv.Itoa(evidence.Count) + " 次；最近一次 " + evidence.LastSeenAt + "\n" +
		"- 集中项目：" + strings.Join(projects, "、") + "\n" +
		"- 样例（原文截断）：\n" + strings.Join(samples, "\n") + "\n" +
		"- 落点建议：" + target.KindLabel + "（" + target.Reason + "）\n" +
		"请给出：1) 具体到可执行的规则文本（适合放进 AGENTS.md 或 rules）；若你认为更适合 hook/skill，请给出对应配置草稿。要求：具体、简短、可验证，不要空话。"
	en := "Draft a " + target.Kind + " for my agent to stop one recurring friction. The evidence below comes from this machine's session history (extracted by Flatline, no attribution claimed):\n" +
		"- Signature: " + signature + "\n" +
		"- Mechanism: " + mechanismEN + "\n" +
		"- Scale: " + strconv.Itoa(evidence.SessionCount) + " sessions, " + strconv.Itoa(evidence.Count) + " occurrences; last seen " + evidence.LastSeenAt + "\n" +
		"- Projects: " + strings.Join(projects, ", ") + "\n" +
		"- Samples (truncated verbatim):\n" + strings.Join(samplesEN, "\n") + "\n" +
		"- Suggested target: " + target.Kind + " (" + target.ReasonEN + ")\n" +
		"Deliver: 1) rule text concrete enough to verify (for AGENTS.md or rules); if you think a hook or skill fits better, provide that config draft instead. Be specific, short, and verifiable."
	return zh, en
}

const briefCriterion = "对反复出现（≥2 个会话）的签名生成：机制来自封闭提示字典（未覆盖则如实标注）；落点按机制类别确定性映射（ADR-21）；样例是该签名最近记录的原文截断。简报只陈述证据，不声称规则会修复任何问题。"

const briefCriterionEN = "Built for recurring signatures (2 or more sessions): the mechanism comes from the closed hint dictionary (stated as unrecorded when it is not covered); the target maps deterministically from the mechanism kind (ADR-21); samples are truncated verbatim lines of the signature's latest records. The brief states evidence only and never claims the rule will fix anything."

// attachBriefs fills the Brief field of signature groups. Sample lines need the
// raw payloads, which the grouped set does not carry, so the brief queries the
// newest records per signature directly — a handful of indexed lookups.
func (s *Server) attachBriefs(ctx context.Context, groups []frictionGroupResponse) error {
	for index := range groups {
		group := &groups[index]
		if group.Signature == "" {
			continue
		}
		evidence := briefEvidence{
			Count: group.Count, SessionCount: group.SessionCount, ProjectCount: group.ProjectCount,
			FirstSeenAt: group.FirstOccurredAt, LastSeenAt: group.LastOccurredAt,
			SampleLines: []string{}, TopProjects: []briefProjectRef{},
		}
		samples, err := s.signatureSampleLines(ctx, group.Signature)
		if err != nil {
			return err
		}
		evidence.SampleLines = samples
		projects, err := s.signatureTopProjects(ctx, group.Signature)
		if err != nil {
			return err
		}
		evidence.TopProjects = projects
		target := briefTargetFor(group.Hint)
		zh, en := briefPastePrompt(group.Signature, target, group.Hint, evidence)
		group.Brief = &signatureBrief{
			Mechanism: group.Hint, Target: target, Evidence: evidence,
			PastePrompt: zh, PastePromptEN: en,
			Criterion: briefCriterion, CriterionEN: briefCriterionEN,
		}
	}
	return nil
}

// signatureSampleLines reads the newest records of one signature and pulls a
// human-readable line out of each payload: the tool output's first line, then
// the tool input's first line, then the recorded sample. A record whose payload
// names no line (an interrupt has no tool output) falls back to the session it
// happened in, labelled as such. Missing stays missing.
func (s *Server) signatureSampleLines(ctx context.Context, signature string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.payload_json, COALESCE(s.title, ''), COALESCE(s.task_text, '')
		FROM friction_records f JOIN sessions s ON s.id = f.session_id
		WHERE f.signature = ? ORDER BY f.occurred_at DESC, f.id DESC LIMIT 12`, signature)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0, briefSampleLimit)
	seen := make(map[string]struct{}, briefSampleLimit)
	for rows.Next() && len(out) < briefSampleLimit {
		var payload, title, task string
		if err := rows.Scan(&payload, &title, &task); err != nil {
			return nil, err
		}
		line := briefSampleLine(payload)
		if line == "" {
			context := strings.TrimSpace(title)
			if context == "" {
				context = strings.TrimSpace(task)
			}
			if index := strings.IndexByte(context, '\n'); index >= 0 {
				context = context[:index]
			}
			if context != "" {
				line = "（所在会话）" + context
			}
		}
		if line == "" {
			continue
		}
		if _, dup := seen[line]; dup {
			continue
		}
		seen[line] = struct{}{}
		out = append(out, line)
	}
	return out, rows.Err()
}

// briefSampleLine picks the most readable line of a recorded payload: the
// tool output's first line — except a traceback, whose first line ("Traceback
// (most recent call last):") names nothing; there the last line carries the
// exception type and message. Falls back to the tool input, then the recorded
// line field.
func briefSampleLine(payload string) string {
	var fields struct {
		ToolOutput string `json:"tool_output"`
		ToolInput  string `json:"tool_input"`
		Line       string `json:"line"`
	}
	if err := json.Unmarshal([]byte(payload), &fields); err != nil {
		return ""
	}
	line := briefLineOf(fields.ToolOutput)
	if line == "" {
		line = briefLineOf(fields.ToolInput)
	}
	if line == "" {
		line = strings.TrimSpace(fields.Line)
	}
	if len(line) > briefSampleBound {
		line = line[:briefSampleBound] + "…"
	}
	return line
}

// briefErrorHint marks the lines that name a failure. A sample that names the
// failure beats one that merely precedes it.
var briefErrorHint = regexp.MustCompile(`(?i)error|fail|fatal|cannot|denied|not found|exception|panic|traceback`)

// briefLineOf picks one line from a tool output: the first non-empty one that
// names a failure, falling back to the first line — except a traceback, whose
// answer is the last line (the exception type and message).
func briefLineOf(text string) string {
	lines := make([]string, 0, 8)
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	if strings.Contains(strings.ToLower(lines[0]), "traceback") {
		return lines[len(lines)-1]
	}
	for _, line := range lines {
		if briefErrorHint.MatchString(line) {
			return line
		}
	}
	return lines[0]
}

// signatureTopProjects is where the signature concentrates, count first.
func (s *Server) signatureTopProjects(ctx context.Context, signature string) ([]briefProjectRef, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(s.project_key, ''), COUNT(*) AS c
		FROM friction_records f JOIN sessions s ON s.id = f.session_id
		WHERE f.signature = ?
		GROUP BY s.project_key ORDER BY c DESC, s.project_key LIMIT 3`, signature)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]briefProjectRef, 0, 3)
	for rows.Next() {
		var ref briefProjectRef
		if err := rows.Scan(&ref.Key, &ref.Count); err != nil {
			return nil, err
		}
		if ref.Key == "" {
			ref.Label = "项目未记录"
		} else {
			ref.Label = ref.Key
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// ---- watches ----

type signatureWatchRecord struct {
	ID              int64           `json:"id"`
	Signature       string          `json:"signature"`
	CreatedAt       string          `json:"created_at"`
	WindowDays      int             `json:"window_days"`
	BaselineCount   int             `json:"baseline_count"`
	BaselineSession int             `json:"baseline_session_count"`
	ProjectKeys     []string        `json:"project_keys"`
	Status          string          `json:"status"`
	Note            *string         `json:"note"`
	LastEvaluatedAt *string         `json:"last_evaluated_at,omitempty"`
	ResolvedAt      *string         `json:"resolved_at,omitempty"`
	Evaluation      *watchEvalution `json:"evaluation"`
	Criterion       string          `json:"criterion"`
	CriterionEN     string          `json:"criterion_en"`
}

type watchEvalution struct {
	// PostCount is every occurrence since the watch started — the number the
	// verdict reads. WindowCount is occurrences in the last window_days whatever
	// their date, reported for context only. ProjectSessions is how many
	// sessions ran in the watched projects inside the window — the honesty
	// denominator that separates "it stopped happening" from "nothing ran to
	// hit it".
	PostCount       int     `json:"post_count"`
	WindowCount     int     `json:"window_count"`
	ProjectSessions int     `json:"project_sessions_in_window"`
	Status          string  `json:"status"`
	ResolvedAt      *string `json:"resolved_at"`
}

const watchCriterion = "verified = 创建已满 window_days 天、创建后该签名零发生、且同项目在最近一个窗口内确有会话在跑；no_change = 创建后仍发生；unobservable = 窗口内同项目没有会话，无法判断；watching = 创建未满一个窗口。取消的 watch 保留原行（status='cancelled'）。"

const watchCriterionEN = "verified = a full window has passed since creation, zero occurrences after creation, and the watched projects did run sessions in the last window; no_change = it still occurs after creation; unobservable = no session ran in the watched projects, so there is nothing to judge; watching = less than one window has passed. A cancelled watch keeps its row (status='cancelled')."

type signatureWatchesResponse struct {
	Watches     []signatureWatchRecord `json:"watches"`
	DataVersion int64                  `json:"data_version"`
}

func (s *Server) handleSignatureWatches(w http.ResponseWriter, r *http.Request) {
	watches, err := s.loadWatches(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, signatureWatchesResponse{Watches: watches, DataVersion: s.dataVersion()})
}

func (s *Server) loadWatches(ctx context.Context) ([]signatureWatchRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, signature, created_at, window_days, baseline_count, baseline_session_count,
		       project_keys_json, status, note, last_evaluated_at, resolved_at
		FROM signature_watches ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]signatureWatchRecord, 0)
	for rows.Next() {
		var record signatureWatchRecord
		var projects string
		var lastEvaluated, resolved *string
		if err := rows.Scan(&record.ID, &record.Signature, &record.CreatedAt, &record.WindowDays,
			&record.BaselineCount, &record.BaselineSession, &projects, &record.Status, &record.Note,
			&lastEvaluated, &resolved); err != nil {
			return nil, err
		}
		record.LastEvaluatedAt, record.ResolvedAt = lastEvaluated, resolved
		record.ProjectKeys = []string{}
		_ = json.Unmarshal([]byte(projects), &record.ProjectKeys)
		record.Criterion, record.CriterionEN = watchCriterion, watchCriterionEN
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range out {
		if out[index].Status == "cancelled" {
			continue
		}
		evaluation, err := s.evaluateWatch(ctx, &out[index])
		if err != nil {
			return nil, err
		}
		out[index].Evaluation = evaluation
		if evaluation.Status != out[index].Status {
			if err := s.storeWatchStatus(ctx, out[index].ID, evaluation.Status); err != nil {
				return nil, err
			}
			out[index].Status = evaluation.Status
			// The persisted verdict timestamps are what the notification
			// projection reads; the in-memory record carries them too.
			now := time.Now().UTC().Format(time.RFC3339Nano)
			out[index].LastEvaluatedAt = &now
			if evaluation.ResolvedAt != nil {
				out[index].ResolvedAt = evaluation.ResolvedAt
			}
		}
	}
	return out, nil
}

// evaluateWatch applies the one-line rule in watchCriterion. The stored status
// only moves forward between watching/verified/no_change/unobservable; a
// verified watch that fires again flips back to no_change — the loop never
// closes permanently, because the facts decide.
func (s *Server) evaluateWatch(ctx context.Context, watch *signatureWatchRecord) (*watchEvalution, error) {
	created, err := time.Parse(time.RFC3339Nano, watch.CreatedAt)
	if err != nil {
		return nil, err
	}
	windowStart := time.Now().UTC().AddDate(0, 0, -watch.WindowDays)
	windowStartStamp := windowStart.Format(time.RFC3339Nano)
	evaluation := &watchEvalution{}
	var lastSeen any
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(MAX(occurred_at), '')
		FROM friction_records WHERE signature = ? AND occurred_at > ?`,
		watch.Signature, watch.CreatedAt).Scan(&evaluation.PostCount, &lastSeen); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM friction_records
		WHERE signature = ? AND occurred_at >= ?`,
		watch.Signature, windowStartStamp).Scan(&evaluation.WindowCount); err != nil {
		return nil, err
	}
	sessions := 0
	if len(watch.ProjectKeys) > 0 {
		conditions := make([]string, 0, len(watch.ProjectKeys))
		args := make([]any, 0, len(watch.ProjectKeys)+1)
		args = append(args, windowStartStamp)
		for _, key := range watch.ProjectKeys {
			conditions = append(conditions, "project_key = ?")
			args = append(args, key)
		}
		if err := s.db.QueryRowContext(ctx, `
			SELECT COUNT(DISTINCT id) FROM sessions
			WHERE started_at >= ? AND (`+strings.Join(conditions, " OR ")+`)`, args...).Scan(&sessions); err != nil {
			return nil, err
		}
	}
	evaluation.ProjectSessions = sessions
	elapsed := time.Since(created)
	switch {
	case evaluation.PostCount > 0:
		evaluation.Status = "no_change"
	case elapsed < time.Duration(watch.WindowDays)*24*time.Hour:
		// Quiet so far, but the window has not closed: still watching.
		evaluation.Status = "watching"
	case evaluation.ProjectSessions == 0:
		evaluation.Status = "unobservable"
	default:
		evaluation.Status = "verified"
		now := time.Now().UTC().Format(time.RFC3339Nano)
		evaluation.ResolvedAt = &now
	}
	return evaluation, nil
}

func (s *Server) storeWatchStatus(ctx context.Context, id int64, status string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE signature_watches SET status = ?, last_evaluated_at = ?,
		       resolved_at = CASE WHEN ? IN ('verified') THEN ? ELSE resolved_at END
		WHERE id = ?`, status, time.Now().UTC().Format(time.RFC3339Nano),
		status, time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

type createWatchRequest struct {
	Signature  *string `json:"signature"`
	Confirmed  bool    `json:"confirmed"`
	WindowDays *int    `json:"window_days"`
	Note       *string `json:"note"`
}

func (s *Server) handleCreateSignatureWatch(w http.ResponseWriter, r *http.Request) {
	var request createWatchRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid watch request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !request.Confirmed {
		http.Error(w, "watch creation requires confirmed=true (AGENTS.md §3)", http.StatusBadRequest)
		return
	}
	if request.Signature == nil || strings.TrimSpace(*request.Signature) == "" {
		http.Error(w, "signature is required", http.StatusBadRequest)
		return
	}
	signature := strings.TrimSpace(*request.Signature)
	windowDays := 14
	if request.WindowDays != nil {
		if *request.WindowDays < 1 || *request.WindowDays > 90 {
			http.Error(w, "window_days must be between 1 and 90", http.StatusBadRequest)
			return
		}
		windowDays = *request.WindowDays
	}
	ctx := r.Context()
	// Baseline and project keys freeze the facts the before/after comparison
	// needs, so a later re-ingest cannot drift the comparison.
	baselineCount, baselineSessions, projects := 0, 0, []string{}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(DISTINCT session_id) FROM friction_records WHERE signature = ?`,
		signature).Scan(&baselineCount, &baselineSessions); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	projectRows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT COALESCE(s.project_key, '') FROM friction_records f
		JOIN sessions s ON s.id = f.session_id WHERE f.signature = ? LIMIT 20`, signature)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for projectRows.Next() {
		var key string
		if err := projectRows.Scan(&key); err != nil {
			projectRows.Close()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if key != "" {
			projects = append(projects, key)
		}
	}
	projectRows.Close()
	if err := projectRows.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	projectsJSON, _ := json.Marshal(projects)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO signature_watches
		  (signature, created_at, window_days, baseline_count, baseline_session_count, project_keys_json, status, note)
		VALUES (?, ?, ?, ?, ?, ?, 'watching', ?)`,
		signature, now, windowDays, baselineCount, baselineSessions, string(projectsJSON), request.Note)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, _ := result.LastInsertId()
	// A watch changes what /insights and /notifications report, so the
	// response cache must not serve the pre-watch view.
	s.bumpDataVersion()
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "signature": signature, "created_at": now, "window_days": windowDays,
		"baseline_count": baselineCount, "baseline_session_count": baselineSessions,
		"project_keys": projects, "status": "watching",
		"note": "只写本地库；原始转写文件未被改动。源文件未改变。",
	})
}

type cancelWatchRequest struct {
	ID        *int64 `json:"id"`
	Confirmed bool   `json:"confirmed"`
}

func (s *Server) handleCancelSignatureWatch(w http.ResponseWriter, r *http.Request) {
	var request cancelWatchRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid watch cancel request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !request.Confirmed {
		http.Error(w, "cancelling a watch requires confirmed=true (AGENTS.md §3)", http.StatusBadRequest)
		return
	}
	if request.ID == nil {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	result, err := s.db.ExecContext(r.Context(), `
		UPDATE signature_watches SET status = 'cancelled' WHERE id = ? AND status <> 'cancelled'`, *request.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		http.Error(w, "watch not found or already cancelled", http.StatusNotFound)
		return
	}
	s.bumpDataVersion()
	writeJSON(w, http.StatusOK, map[string]any{"id": *request.ID, "status": "cancelled"})
}

// attachWatchStatuses puts the live watch status on the signature group rows so
// the friction page can badge a watched signature without a second request.
func (s *Server) attachWatchStatuses(ctx context.Context, groups []frictionGroupResponse) error {
	watches, err := s.loadWatches(ctx)
	if err != nil {
		return err
	}
	bySignature := make(map[string]*signatureWatchRecord, len(watches))
	for index := range watches {
		if watches[index].Status == "cancelled" {
			continue
		}
		if _, exists := bySignature[watches[index].Signature]; !exists {
			bySignature[watches[index].Signature] = &watches[index]
		}
	}
	for index := range groups {
		if watch, found := bySignature[groups[index].Signature]; found && groups[index].Signature != "" {
			status := watch.Status
			if watch.Evaluation != nil {
				status = watch.Evaluation.Status
			}
			groups[index].Watch = &frictionWatchBadge{
				ID: watch.ID, Status: status, CreatedAt: watch.CreatedAt,
				WindowDays: watch.WindowDays, WindowCount: watch.Evaluation.WindowCount,
				ProjectSessions: watch.Evaluation.ProjectSessions,
			}
		}
	}
	return nil
}
