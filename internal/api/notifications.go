package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"flatline/internal/vital"
)

type notificationResponse struct {
	ID              int64           `json:"id"`
	AssetID         string          `json:"asset_id"`
	AssetName       string          `json:"asset_name"`
	StateInstanceID int64           `json:"state_instance_id"`
	State           vital.State     `json:"state"`
	Kind            string          `json:"kind"`
	Severity        string          `json:"severity"`
	Title           string          `json:"title"`
	Summary         string          `json:"summary"`
	Rule            string          `json:"rule"`
	Evidence        json.RawMessage `json:"evidence"`
	OccurredAt      time.Time       `json:"occurred_at"`
	SessionID       string          `json:"session_id,omitempty"`
	Locator         json.RawMessage `json:"locator,omitempty"`
	// Watch verdict entries (ADR-24) carry the signature instead of an asset:
	// the fields above stay at their zero values and these name the drill-down.
	WatchID   *int64 `json:"watch_id,omitempty"`
	Signature string `json:"signature,omitempty"`
	SummaryEN string `json:"summary_en,omitempty"`
}

type rawTransitionNotification struct {
	Transition vital.Transition
	AssetName  string
}

// handleNotifications is a read projection over state transitions. There is
// no second mutable notification store: this keeps the rule "state migration
// is the only notification source" and makes replay deterministic. An ignore
// disposition suppresses only the matching state instance.
func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	ignored, err := s.ignoredNotificationInstances(r.Context())
	if err != nil {
		http.Error(w, "notification suppression query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT t.id, t.asset_id, a.name, t.from_state, t.to_state, t.broken_overlay,
		       t.occurred_at, t.evidence_json, COALESCE(t.alignment_json, ''),
		       t.detector_version, t.schema_version, t.threshold_version
		FROM state_transitions t JOIN assets a ON a.id = t.asset_id
		ORDER BY t.occurred_at DESC, t.id DESC LIMIT ?`, queryLimit(r, 1000))
	if err != nil {
		http.Error(w, "notification query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	transitions := make([]rawTransitionNotification, 0)
	for rows.Next() {
		var (
			transition              vital.Transition
			assetName               string
			to, occurred, alignment string
			overlay                 int
			nullFrom                sql.NullString
		)
		if err := rows.Scan(&transition.ID, &transition.AssetID, &assetName, &nullFrom, &to, &overlay, &occurred, &transition.EvidenceJSON, &alignment, &transition.DetectorVersion, &transition.SchemaVersion, &transition.ThresholdVersion); err != nil {
			rows.Close()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if nullFrom.Valid {
			value := vital.State(nullFrom.String)
			transition.FromState = &value
		}
		transition.ToState = vital.State(to)
		transition.BrokenOverlay = overlay != 0
		transition.AlignmentJSON = alignment
		transition.OccurredAt, err = time.Parse(time.RFC3339Nano, occurred)
		if err != nil {
			rows.Close()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		transitions = append(transitions, rawTransitionNotification{Transition: transition, AssetName: assetName})
	}
	if !closeRows(w, rows) {
		return
	}

	items := make([]notificationResponse, 0)
	for _, raw := range transitions {
		if _, ok := ignored[notificationInstanceKey(raw.Transition.AssetID, raw.Transition.ID)]; ok {
			continue
		}
		item, ok := s.notificationFromTransition(r.Context(), raw)
		if !ok {
			continue
		}
		items = append(items, item)
		if len(items) >= queryLimit(r, 100) {
			break
		}
	}
	// ADR-24: fix-verification verdicts are the second notification source —
	// still a pure projection, still no second mutable store. A verdict that
	// is newer than the oldest listed transition still deserves its slot, so
	// the merge happens before the limit cuts.
	if len(items) < queryLimit(r, 100) {
		watches, err := s.loadWatches(r.Context())
		if err != nil {
			http.Error(w, "watch verdict query failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		items = append(items, watchVerdictNotifications(watches)...)
		sort.SliceStable(items, func(i, j int) bool { return items[i].OccurredAt.After(items[j].OccurredAt) })
		if len(items) > queryLimit(r, 100) {
			items = items[:queryLimit(r, 100)]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": items})
}

// watchVerdictNotifications projects every settled fix verification into a
// notification, deduped per (watch, status): the verdict is the alarm the
// improvement loop owes the user, and cancelling the watch withdraws it.
func watchVerdictNotifications(watches []signatureWatchRecord) []notificationResponse {
	out := make([]notificationResponse, 0, len(watches))
	for _, watch := range watches {
		if watch.Evaluation == nil {
			continue
		}
		status := watch.Evaluation.Status
		var kind, occurred string
		switch status {
		case "verified":
			kind = "watch_verified"
			if watch.ResolvedAt != nil {
				occurred = *watch.ResolvedAt
			}
		case "no_change":
			kind = "watch_no_change"
		case "unobservable":
			kind = "watch_unobservable"
		default:
			continue
		}
		if occurred == "" && watch.LastEvaluatedAt != nil {
			occurred = *watch.LastEvaluatedAt
		}
		if occurred == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, occurred)
		if err != nil {
			at = time.Time{}
		}
		title, summary, summaryEN := watchVerdictCopy(status, watch)
		out = append(out, notificationResponse{
			ID: -watch.ID, Kind: kind, Severity: severityFor(status),
			Title: title, Summary: summary, SummaryEN: summaryEN,
			Rule:       watch.Criterion,
			Signature:  watch.Signature,
			WatchID:    &watch.ID,
			OccurredAt: at,
		})
	}
	return out
}

func severityFor(status string) string {
	switch status {
	case "verified":
		return "good"
	case "no_change":
		return "warn"
	default:
		return "muted"
	}
}

// watchVerdictCopy is the daemon's own sentence for a verdict, in both
// languages, split on NUL so the response carries both without new fields.
func watchVerdictCopy(status string, watch signatureWatchRecord) (string, string, string) {
	line := frictionSignatureLine(watch.Signature)
	switch status {
	case "verified":
		return "修复验证通过 · " + line,
			"写入规则后，签名在 " + strconv.Itoa(watch.WindowDays) + " 天窗口内零发生，且同项目会话仍在跑。",
			"After the rule was written, the signature did not occur again inside the " + strconv.Itoa(watch.WindowDays) + "-day window while the same projects kept running."
	case "no_change":
		return "修复未见改善 · " + line,
			"写入规则后，签名仍发生了 " + strconv.Itoa(watch.Evaluation.PostCount) + " 次。",
			"After the rule was written, the signature still occurred " + strconv.Itoa(watch.Evaluation.PostCount) + " times."
	default:
		return "修复验证无法判断 · " + line,
			"窗口内同项目没有会话在跑，无法把安静归给规则。",
			"No session ran in the watched projects during the window, so the quiet cannot be credited to the rule."
	}
}

func (s *Server) ignoredNotificationInstances(ctx context.Context) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT asset_id, state_instance_id FROM dispositions WHERE action = 'ignore'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ignored := make(map[string]struct{})
	for rows.Next() {
		var assetID string
		var stateID int64
		if err := rows.Scan(&assetID, &stateID); err != nil {
			return nil, err
		}
		ignored[notificationInstanceKey(assetID, stateID)] = struct{}{}
	}
	return ignored, rows.Err()
}

func notificationInstanceKey(assetID string, stateID int64) string {
	return assetID + "\x00" + strconv.FormatInt(stateID, 10)
}

func (s *Server) notificationFromTransition(ctx context.Context, raw rawTransitionNotification) (notificationResponse, bool) {
	transition := raw.Transition
	var envelope struct {
		Reason       string `json:"reason"`
		Rule         string `json:"rule"`
		Resurrection bool   `json:"resurrection"`
	}
	var evidence map[string]json.RawMessage
	if err := json.Unmarshal([]byte(transition.EvidenceJSON), &evidence); err == nil {
		if value, ok := evidence["decision"]; ok {
			_ = json.Unmarshal(value, &envelope)
		}
	}

	kind, severity, title := "", "", ""
	switch {
	case transition.ToState == vital.StateHealthy && envelope.Resurrection:
		kind, severity, title = "verification_passed", "positive", "修改后验证通过"
	case transition.FromState != nil && *transition.FromState == vital.StateAwaitingResurrection && transition.ToState == vital.StateSilent:
		kind, severity, title = "verification_failed", "attention", "修改后验证未通过"
	case transition.BrokenOverlay || transition.ToState == vital.StateBroken:
		kind, severity, title = "broken", "attention", "引用检查发现失效"
	case transition.ToState == vital.StateSilent:
		kind, severity, title = "silent", "attention", "资产进入沉默"
	case transition.ToState == vital.StateBypassed:
		kind, severity, title = "bypassed", "attention", "记录到资产被绕行"
	default:
		return notificationResponse{}, false
	}
	if envelope.Reason == "" {
		envelope.Reason = notificationFallbackSummary(transition, kind)
	}
	if envelope.Rule == "" {
		envelope.Rule = notificationFallbackRule(transition, kind)
	}
	item := notificationResponse{
		ID: transition.ID, AssetID: transition.AssetID, AssetName: raw.AssetName,
		StateInstanceID: transition.ID, State: transition.ToState, Kind: kind,
		Severity: severity, Title: title, Summary: envelope.Reason, Rule: envelope.Rule,
		Evidence: json.RawMessage(transition.EvidenceJSON), OccurredAt: transition.OccurredAt,
	}
	if len(item.Evidence) == 0 {
		item.Evidence = json.RawMessage(`{}`)
	}
	item.SessionID, item.Locator = s.notificationSession(ctx, transition.AssetID, transition.OccurredAt)
	return item, true
}

func (s *Server) notificationSession(ctx context.Context, assetID string, at time.Time) (string, json.RawMessage) {
	var sessionID, locator string
	err := s.db.QueryRowContext(ctx, `
		SELECT p.session_id, COALESCE(p.locator_json, '')
		FROM participations p JOIN asset_versions av ON av.id = p.asset_version_id AND p.superseded_at IS NULL
		WHERE av.asset_id = ? AND p.occurred_at IS NOT NULL AND p.occurred_at <> ''
		  AND julianday(p.occurred_at) >= julianday(?)
		ORDER BY p.occurred_at, p.id LIMIT 1`, assetID, formatTime(at)).Scan(&sessionID, &locator)
	if err != nil || sessionID == "" {
		return "", nil
	}
	if locator == "" {
		return sessionID, nil
	}
	return sessionID, json.RawMessage(locator)
}

func notificationFallbackSummary(transition vital.Transition, kind string) string {
	switch kind {
	case "verification_passed":
		return "修改后首次记录到符合条件的参与。"
	case "verification_failed":
		return "修改后验证窗口内没有记录到符合条件的参与。"
	case "broken":
		return "引用检查记录到缺失项；具体分子、分母和条目见证据。"
	case "silent":
		return "最近相关任务的参与记录触发沉默判定。"
	case "bypassed":
		return "同一会话中记录到资产调用与明确绕行。"
	default:
		return "状态迁移已记录。"
	}
}

func notificationFallbackRule(transition vital.Transition, kind string) string {
	switch kind {
	case "verification_passed":
		return "修改后首次记录到参与且没有明确违背记录。"
	case "verification_failed":
		return "修改后连续 8 个可判定机会没有符合条件的参与。"
	case "broken":
		return "已检查引用中至少有 1 个明确缺失。"
	case "silent":
		return "历史参与至少达到 5 个相关任务且参与率至少 30%，最近 8 个相关任务记录到 0 次参与。"
	case "bypassed":
		return "同一会话中同时记录资产调用和明确绕行。"
	default:
		return "状态迁移规则已记录。"
	}
}
