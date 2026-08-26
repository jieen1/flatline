package api

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// sessionExportLimit bounds one download. The export is a copy of what the
// session list is showing, not a database dump.
const sessionExportLimit = 5000

var sessionExportColumns = []string{
	"id", "source", "source_session_id", "title", "task_text", "started_at", "ended_at",
	"duration_ms", "harness_version", "model", "cwd", "project_key", "project_label",
	"thread_kind", "parent_session_id", "agent_role", "agent_nickname", "originator",
	"event_count", "transcript_count", "message_count", "user_message_count",
	"tool_call_count", "tool_result_count", "friction_count", "tool_error_count",
	"nonzero_exit_count", "asset_count", "subagent_count", "command_count",
	"failed_command_count", "file_count", "is_empty", "pinned", "tags",
}

// handleSessionsExport downloads the current session filter. It takes exactly
// the parameters of the session list, so what is downloaded is what was on
// screen; only the page size differs.
func (s *Server) handleSessionsExport(w http.ResponseWriter, r *http.Request) {
	format := strings.TrimSpace(r.URL.Query().Get("format"))
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "csv" {
		http.Error(w, fmt.Sprintf("unsupported export format %q", format), http.StatusBadRequest)
		return
	}
	query, err := parseSessionQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	query.limit, query.offset = sessionExportLimit, 0
	items, total, err := s.querySessions(r.Context(), query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	filename := "flatline-sessions-" + time.Now().UTC().Format("2006-01-02") + "." + format
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	if format == "csv" {
		writeSessionCSV(w, items)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"sessions": items, "exported": len(items), "matched": total,
		"truncated": len(items) < total, "data_version": s.dataVersion(),
	})
}

func writeSessionCSV(w http.ResponseWriter, items []sessionResponse) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	writer := csv.NewWriter(w)
	defer writer.Flush()
	_ = writer.Write(sessionExportColumns)
	for _, item := range items {
		tags := make([]string, 0, len(item.Tags))
		for _, tag := range item.Tags {
			tags = append(tags, tag.Tag)
		}
		_ = writer.Write([]string{
			item.ID, item.Source, item.SourceSessionID,
			stringOrEmpty(item.Title), stringOrEmpty(item.TaskText),
			timeOrEmpty(item.StartedAt), timeOrEmpty(item.EndedAt), int64OrEmpty(item.DurationMS),
			stringOrEmpty(item.HarnessVersion), stringOrEmpty(item.Model), stringOrEmpty(item.CWD),
			item.ProjectKey, item.ProjectLabel,
			stringOrEmpty(item.ThreadKind), stringOrEmpty(item.ParentSessionID),
			stringOrEmpty(item.AgentRole), stringOrEmpty(item.AgentNickname), stringOrEmpty(item.Originator),
			strconv.Itoa(item.EventCount), strconv.Itoa(item.TranscriptCount), strconv.Itoa(item.MessageCount),
			strconv.Itoa(item.UserMessageCount), strconv.Itoa(item.ToolCallCount), strconv.Itoa(item.ToolResultCount),
			strconv.Itoa(item.FrictionCount), strconv.Itoa(item.ToolErrorCount), strconv.Itoa(item.NonzeroExitCount),
			strconv.Itoa(item.AssetCount), strconv.Itoa(item.SubagentCount), strconv.Itoa(item.CommandCount),
			strconv.Itoa(item.FailedCommands), strconv.Itoa(item.FileCount),
			strconv.FormatBool(item.IsEmpty), strconv.FormatBool(item.Pinned),
			strings.Join(tags, ";"),
		})
	}
}

// stringOrEmpty writes an unrecorded field as an empty cell. A CSV has no way
// to say "not recorded", so the reader is told in the column header docs
// rather than by inventing a zero.
func stringOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func timeOrEmpty(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func int64OrEmpty(value *int64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(*value, 10)
}
