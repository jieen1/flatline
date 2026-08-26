package history

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/eventstore"
)

// This file holds what the non-JSONL source readers (opencode, dsh) share:
// the normalized-document shape they emit and the usage object defined in
// §14/§15. The Claude Code and Codex readers in native.go keep their own
// marshal helpers; nothing here changes them.

// UsageSourceOpenCode and UsageSourceDSH name where a session's numbers came
// from. opencode accumulates its own per-session totals, so those are read
// straight off the session row; dsh records usage per assistant message, so
// they are summed. eventstore owns the two original constants.
const (
	UsageSourceOpenCode = eventstore.UsageSourceOpenCode
	UsageSourceDSH      = eventstore.UsageSourceDSH
)

// marshalNormalized emits the document shape the normalized adapter reads.
// The pairing key is always call_id: §13 requires tool_call and tool_result to
// carry one, and a source that names it differently is translated by its
// reader, not by the adapter.
func marshalNormalized(sessionID, cwd, version, model, title, taskText string, started, ended *time.Time, thread threadInfo, usage *eventstore.SessionUsage, cost *float64, messages []normalizedMessage) ([]byte, error) {
	items := make([]any, 0, len(messages))
	for _, message := range messages {
		items = append(items, normalizedMessageMap(message, "call_id"))
	}
	document := sessionMap(sessionID, cwd, version, model, title, taskText, started, ended, thread)
	if usage != nil {
		encoded, err := json.Marshal(usage)
		if err != nil {
			return nil, err
		}
		var merged map[string]any
		if err := json.Unmarshal(encoded, &merged); err != nil {
			return nil, err
		}
		if cost != nil {
			merged["cost"] = *cost
		}
		document["usage"] = merged
	}
	return json.Marshal(map[string]any{"session": document, "messages": items})
}

func rowStamp(updatedUnixMilli int64) FileStamp {
	return FileStamp{ModTimeUnixNano: updatedUnixMilli * int64(time.Millisecond)}
}

// unchangedRow is the fingerprint check for a source whose unit of change is a
// database row rather than a file. currentFileStamp cannot stat a pseudo-path,
// so the caller supplies the stamp the row itself reports.
func unchangedRow(path string, current FileStamp, known map[string]FileStamp) bool {
	if len(known) == 0 {
		return false
	}
	previous, exists := known[path]
	return exists && previous == current
}

func rowPath(root, id string) string { return root + "#" + id }

func millisToTime(value int64) *time.Time {
	if value <= 0 {
		return nil
	}
	at := time.UnixMilli(value).UTC()
	return &at
}

func int64Ptr(value int64) *int64 { return &value }

// positiveInt64 records a count only when the source actually reported one.
// opencode defaults its token columns to 0, so a zero there is indistinguishable
// from "this session never ran a model turn"; that stays unrecorded.
func positiveInt64(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func formatMillis(value int64) string {
	at := millisToTime(value)
	if at == nil {
		return ""
	}
	return at.Format(time.RFC3339Nano)
}

func warn(file string, err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%s: %v", file, err)
}

// discoverOpenCode and discoverDSH keep Discover's body to one registration
// line per source: the reader, the fingerprint rule and the health status for
// a source all live with that source, not in the shared scan loop.
func discoverOpenCode(config Config, index assetIndex, report *Report, notify func()) ([]Session, SourceStatus) {
	status := SourceStatus{Kind: string(adapters.SourceOpenCode), Root: config.OpenCodeDB}
	if strings.TrimSpace(config.OpenCodeDB) == "" {
		status.Status = StatusNotFound
		return nil, status
	}
	if _, err := os.Stat(config.OpenCodeDB); err != nil {
		status.Status = StatusNotFound
		return nil, status
	}
	count, lastSeen, err := openCodeProbe(config.OpenCodeDB)
	if err != nil {
		status.Status = StatusError
		status.Error = err.Error()
		report.Warnings = append(report.Warnings, warn(config.OpenCodeDB, err))
		return nil, status
	}
	status.Sessions = &count
	status.LastSeenAt = lastSeen
	status.Status = StatusOK
	if count == 0 {
		status.Status = StatusNoSessions
		return nil, status
	}
	sessions, err := readOpenCode(config.OpenCodeDB, index, config.ProjectRoot, config.KnownFiles, report, notify)
	if err != nil {
		status.Status = StatusError
		status.Error = err.Error()
		report.Warnings = append(report.Warnings, warn(config.OpenCodeDB, err))
		return nil, status
	}
	return sessions, status
}

func discoverDSH(config Config, index assetIndex, report *Report, notify func()) ([]Session, SourceStatus) {
	root := strings.TrimSpace(config.DSHRoot)
	status := SourceStatus{Kind: string(adapters.SourceDSH), Root: root}
	files, err := dshFiles(config.DSHRoot, config.DSHRoots)
	if err != nil {
		status.Status = StatusError
		status.Error = err.Error()
		return nil, status
	}
	if root == "" {
		status.Status = StatusNotFound
		return nil, status
	}
	if _, statErr := os.Stat(root); statErr != nil {
		status.Status = StatusNotFound
		return nil, status
	}
	count := len(files)
	status.Sessions = &count
	status.Status = StatusOK
	if count == 0 {
		status.Status = StatusNoSessions
		return nil, status
	}
	var out []Session
	var newest *time.Time
	for _, file := range files {
		report.FilesSeen++
		notify()
		if stamp, ok := currentFileStamp(file); ok {
			at := time.Unix(0, stamp.ModTimeUnixNano).UTC()
			if newest == nil || at.After(*newest) {
				newest = &at
			}
		}
		if unchanged, ok := unchangedFile(file, config.KnownFiles); ok {
			report.FilesSkipped++
			report.FileStamps[file] = unchanged
			continue
		}
		session, evidence, ok, warning := readDSH(file, index, config.ProjectRoot)
		if warning != "" {
			report.Warnings = append(report.Warnings, warning)
		}
		if !ok {
			continue
		}
		if stamp, ok := currentFileStamp(file); ok {
			report.FileStamps[file] = stamp
		}
		report.FilesRead++
		report.SessionsFound++
		report.AssetEvidenceFound += evidence
		out = append(out, session)
	}
	status.LastSeenAt = newest
	return out, status
}

// claudeStatus and codexStatus report the two original sources in the same
// shape, so /ingest/health lists every source the daemon looked at rather than
// only the ones added later.
func claudeStatus(config Config, files int) SourceStatus {
	return rootStatus(string(adapters.SourceClaudeCode), config.ClaudeRoot, files)
}

func codexStatus(config Config, files int) SourceStatus {
	return rootStatus(string(adapters.SourceCodex), config.CodexRoot, files)
}

func rootStatus(kind, root string, files int) SourceStatus {
	status := SourceStatus{Kind: kind, Root: root}
	if strings.TrimSpace(root) == "" {
		status.Status = StatusNotFound
		return status
	}
	if _, err := os.Stat(root); err != nil {
		status.Status = StatusNotFound
		return status
	}
	status.Sessions = &files
	status.Status = StatusOK
	if files == 0 {
		status.Status = StatusNoSessions
	}
	return status
}

// lastSources is the snapshot of what the most recent Discover pass found for
// each source. It lives here rather than in the API server because it is a
// property of the read pass, not of the HTTP layer: /ingest/health reports the
// last pass, and a pass that has not run yet reports an empty list rather than
// a fabricated set of roots.
var lastSources struct {
	mu    sync.RWMutex
	items []SourceStatus
}

func recordSourceStatuses(items []SourceStatus) {
	lastSources.mu.Lock()
	defer lastSources.mu.Unlock()
	lastSources.items = append([]SourceStatus(nil), items...)
}

// SourceStatuses returns what the last discovery pass found, per source.
func SourceStatuses() []SourceStatus {
	lastSources.mu.RLock()
	defer lastSources.mu.RUnlock()
	return append([]SourceStatus(nil), lastSources.items...)
}
