// Package history reads the native, local-only JSONL histories produced by
// Claude Code and Codex and turns only explicit source evidence into the
// adapter fixture shape consumed by the ingest pipeline.
//
// This package is deliberately a reader. It never edits, renames, disables,
// or deletes a history file or an asset source file. Native transcripts do not
// expose a task-shape enum, so the reader derives a deterministic shape only
// from bounded user task text. Opportunities still require an exact,
// source-backed asset path in that task text or in a recorded tool input; a
// session by itself never creates an opportunity.
package history

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/assets"
	"flatline/internal/canonical"
	"flatline/internal/ingest"
)

// Config selects native history roots and the already-discovered assets that
// may be matched. When ProjectRoot is empty, every session under the
// configured roots is accepted. This is the daemon's full local-history mode;
// it still records unknown task shape as unknown instead of manufacturing an
// opportunity.
type Config struct {
	ClaudeRoot       string
	ClaudeRoots      []string
	CodexRoot        string
	CodexRoots       []string
	ProjectRoot      string
	Assets           []assets.Asset
	IncludeSubagents bool
	KnownFiles       map[string]FileStamp
}

// FileStamp is the read-only fingerprint used to avoid replaying unchanged
// native transcript files on every daemon refresh. It is intentionally not a
// content hash: a size/mtime change causes a fresh parse, while the canonical
// event store remains idempotent.
type FileStamp struct {
	Size            int64
	ModTimeUnixNano int64
}

// Session is one normalized native session ready for the ingest pipeline.
type Session struct {
	Input      ingest.SessionInput
	SourcePath string
}

// Report describes a read-only discovery pass. Warnings are intentionally
// retained instead of failing the entire pass when one historical file is
// truncated or uses a newer record shape.
type Report struct {
	FilesSeen          int                  `json:"files_seen"`
	FilesRead          int                  `json:"files_read"`
	FilesSkipped       int                  `json:"files_skipped"`
	SessionsFound      int                  `json:"sessions_found"`
	SessionsNormalized int                  `json:"sessions_normalized"`
	AssetEvidenceFound int                  `json:"asset_evidence_found"`
	Warnings           []string             `json:"warnings,omitempty"`
	FileStamps         map[string]FileStamp `json:"-"`
}

// Discover scans the configured roots in deterministic path order. Missing
// optional roots are treated as empty, not as an error.
func Discover(config Config) ([]Session, Report, error) {
	index := newAssetIndex(config.Assets, config.ProjectRoot)
	report := Report{Warnings: make([]string, 0), FileStamps: make(map[string]FileStamp)}
	var out []Session

	claudeFiles, err := jsonlFilesForRoots(config.ClaudeRoot, config.ClaudeRoots, config.IncludeSubagents)
	if err != nil {
		return nil, report, fmt.Errorf("history: discover Claude Code files: %w", err)
	}
	codexFiles, err := jsonlFilesForRoots(config.CodexRoot, config.CodexRoots, false)
	if err != nil {
		return nil, report, fmt.Errorf("history: discover Codex files: %w", err)
	}
	for _, file := range claudeFiles {
		report.FilesSeen++
		if unchanged, ok := unchangedFile(file, config.KnownFiles); ok {
			report.FilesSkipped++
			report.FileStamps[file] = unchanged
			continue
		}
		session, evidence, ok, warning := readClaude(file, index, config.ProjectRoot)
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
	for _, file := range codexFiles {
		report.FilesSeen++
		if unchanged, ok := unchangedFile(file, config.KnownFiles); ok {
			report.FilesSkipped++
			report.FileStamps[file] = unchanged
			continue
		}
		if config.ProjectRoot != "" && !codexFileMayBelongToProject(file, config.ProjectRoot) {
			continue
		}
		session, evidence, ok, warning := readCodex(file, index, config.ProjectRoot)
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
	sort.Slice(out, func(i, j int) bool {
		if out[i].Input.Raw.Source != out[j].Input.Raw.Source {
			return out[i].Input.Raw.Source < out[j].Input.Raw.Source
		}
		return out[i].SourcePath < out[j].SourcePath
	})
	report.SessionsNormalized = len(out)
	return out, report, nil
}

type assetRef struct {
	asset assets.Asset
	path  string
}

type assetIndex struct {
	refs        []assetRef
	projectRoot string
}

func newAssetIndex(items []assets.Asset, projectRoot string) assetIndex {
	refs := make([]assetRef, 0, len(items))
	for _, item := range items {
		if item.SourcePath == nil || strings.TrimSpace(*item.SourcePath) == "" {
			continue
		}
		refs = append(refs, assetRef{asset: item, path: filepath.Clean(*item.SourcePath)})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].asset.ID < refs[j].asset.ID })
	return assetIndex{refs: refs, projectRoot: filepath.Clean(projectRoot)}
}

func (index assetIndex) byPath(value string) []assetRef {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	candidates := []string{filepath.Clean(value)}
	if !filepath.IsAbs(value) && index.projectRoot != "" && index.projectRoot != "." {
		candidates = append(candidates, filepath.Join(index.projectRoot, value))
	}
	seen := make(map[string]struct{}, len(candidates))
	var out []assetRef
	for _, candidate := range candidates {
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		for _, ref := range index.refs {
			if ref.path == candidate {
				out = append(out, ref)
			}
		}
	}
	return dedupeRefs(out)
}

func (index assetIndex) bySkill(value string) []assetRef {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "skill:")
	if value == "" {
		return nil
	}
	var out []assetRef
	for _, ref := range index.refs {
		if ref.asset.Kind != assets.KindSkill {
			continue
		}
		name := ref.asset.Name
		tail := name
		if idx := strings.LastIndex(tail, ":"); idx >= 0 {
			tail = tail[idx+1:]
		}
		if name == value || tail == value || strings.HasSuffix(name, ":"+value) {
			out = append(out, ref)
		}
	}
	return dedupeRefs(out)
}

func dedupeRefs(items []assetRef) []assetRef {
	seen := make(map[string]struct{}, len(items))
	out := make([]assetRef, 0, len(items))
	for _, item := range items {
		if _, ok := seen[item.asset.ID]; ok {
			continue
		}
		seen[item.asset.ID] = struct{}{}
		out = append(out, item)
	}
	return out
}

type normalizedInvocation struct {
	AssetID             string
	ParticipationSignal canonical.ParticipationSignal
	ObservationLevel    canonical.ObservationLevel
}

type normalizedMessage struct {
	ID          string
	Timestamp   string
	Role        string
	Kind        string
	Text        string
	ToolName    string
	ToolInput   string
	ToolOutput  string
	Truncated   bool
	IsError     *bool
	ExitCode    *int
	Invocations []normalizedInvocation
}

var nativeExitCodePattern = regexp.MustCompile(`(?im)^\s*exit\s+(?:code|status)\s*[:=]?\s*([1-9][0-9]*)\b`)

const (
	maxTranscriptText = 4096
	maxToolPayload    = 8192
	maxSessionTitle   = 160
	maxTaskText       = 240
)

func (index assetIndex) invocationForTool(name string, input map[string]any) []normalizedInvocation {
	var refs []assetRef
	switch name {
	case "Skill":
		if value, ok := input["skill"].(string); ok {
			refs = index.bySkill(value)
		}
	case "Read":
		if value, ok := input["file_path"].(string); ok {
			refs = index.byPath(value)
		}
	default:
		// Edit/Write are source mutations, not proof that the asset was
		// loaded or followed. The later asset scan records their new version.
	}
	out := make([]normalizedInvocation, 0, len(refs))
	for _, ref := range refs {
		level, signal := canonical.LevelInvoked, canonical.SignalInvoked
		if name == "Read" {
			level, signal = canonical.LevelLoaded, canonical.SignalLoaded
		}
		out = append(out, normalizedInvocation{AssetID: ref.asset.ID, ParticipationSignal: signal, ObservationLevel: level})
	}
	return out
}

type claudeRecord struct {
	Type       string `json:"type"`
	SessionID  string `json:"sessionId"`
	SessionID2 string `json:"session_id"`
	UUID       string `json:"uuid"`
	Timestamp  string `json:"timestamp"`
	CWD        string `json:"cwd"`
	Version    string `json:"version"`
	AITitle    string `json:"aiTitle"`
	Message    struct {
		ID      string          `json:"id"`
		Role    string          `json:"role"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func readClaude(file string, index assetIndex, projectRoot string) (Session, int, bool, string) {
	var sessionID, cwd, version, model, title, taskText string
	var started, ended *time.Time
	var messages []normalizedMessage
	evidence := 0
	warning := ""

	err := eachJSONLine(file, func(line int, raw []byte) error {
		var record claudeRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
		if sessionID == "" {
			sessionID = firstNonEmpty(record.SessionID, record.SessionID2)
		}
		if cwd == "" {
			cwd = record.CWD
		}
		if version == "" {
			version = record.Version
		}
		if model == "" {
			model = record.Message.Model
		}
		if strings.TrimSpace(record.AITitle) != "" {
			title, _ = boundText(record.AITitle, maxSessionTitle)
		}
		at := parseTime(record.Timestamp)
		if at != nil {
			started, ended = extendBounds(started, ended, at)
		}
		if len(record.Message.Content) == 0 {
			return nil
		}
		role := firstNonEmpty(record.Message.Role, record.Type)
		messageRef := firstNonEmpty(record.UUID, record.Message.ID, fmt.Sprintf("line-%d", line))
		blocks, err := claudeContentBlocks(record.Message.Content)
		if err != nil {
			return nil
		}
		for blockIndex, rawBlock := range blocks {
			var block struct {
				Type      string          `json:"type"`
				Text      string          `json:"text"`
				Name      string          `json:"name"`
				Input     map[string]any  `json:"input"`
				Content   json.RawMessage `json:"content"`
				ToolUseID string          `json:"tool_use_id"`
				IsError   *bool           `json:"is_error"`
			}
			if err := json.Unmarshal(rawBlock, &block); err != nil {
				return nil
			}
			ref := fmt.Sprintf("%s-%d", messageRef, blockIndex)
			occurred := formatOptionalTime(at)
			switch block.Type {
			case "text":
				text, truncated := boundText(block.Text, maxTranscriptText)
				if text == "" || isGeneratedNoise(text) || !isTranscriptRole(role) {
					continue
				}
				if taskText == "" && role == "user" && meaningfulTaskText(text) {
					taskText, _ = boundText(text, maxTaskText)
				}
				messages = append(messages, normalizedMessage{ID: ref, Timestamp: occurred, Role: role, Kind: "message", Text: text, Truncated: truncated})
			case "tool_use":
				input := marshalBoundedJSON(block.Input, maxToolPayload)
				invocations := index.invocationForTool(block.Name, block.Input)
				messages = append(messages, normalizedMessage{ID: ref, Timestamp: occurred, Role: "assistant", Kind: "tool_call", ToolName: block.Name, ToolInput: input, Truncated: jsonWasTruncated(input, block.Input, maxToolPayload), Invocations: invocations})
				evidence += len(invocations)
			case "tool_result":
				output, truncated := boundText(rawContentText(block.Content), maxToolPayload)
				isError, exitCode := normalizeToolFailure(output, block.IsError, nil)
				if output == "" && isError == nil && exitCode == nil {
					continue
				}
				messages = append(messages, normalizedMessage{ID: ref, Timestamp: occurred, Role: "tool", Kind: "tool_result", ToolName: block.ToolUseID, ToolOutput: output, Truncated: truncated, IsError: isError, ExitCode: exitCode})
			}
		}
		return nil
	})
	if err != nil {
		warning = fmt.Sprintf("%s: %v", file, err)
	}
	if projectRoot != "" && cwd != "" && !within(projectRoot, cwd) {
		return Session{}, 0, false, warning
	}
	if sessionID == "" {
		sessionID = strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	}
	if sessionID == "" {
		return Session{}, 0, false, warning
	}
	if title == "" && taskText != "" {
		title, _ = boundText(taskText, maxSessionTitle)
	}
	raw, marshalErr := marshalClaude(sessionID, cwd, version, model, title, taskText, started, ended, messages)
	if marshalErr != nil {
		return Session{}, 0, false, fmt.Sprintf("%s: normalize: %v", file, marshalErr)
	}
	taskTags := nativeTaskTags(taskText, cwd)
	opportunityAssetIDs := nativeOpportunityAssetIDs(taskText, messages, index)
	return Session{Input: ingest.SessionInput{
		Raw:                 adapters.RawSession{Source: adapters.SourceClaudeCode, SessionID: sessionID, RawJSON: raw, SourcePath: file},
		TaskTags:            taskTags,
		OpportunityAssetIDs: opportunityAssetIDs,
	}, SourcePath: file}, evidence, true, warning
}

type codexRecord struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexPayload struct {
	Type       string          `json:"type"`
	ID         string          `json:"id"`
	CWD        string          `json:"cwd"`
	CLIVersion string          `json:"cli_version"`
	Model      string          `json:"model"`
	Name       string          `json:"name"`
	CallID     string          `json:"call_id"`
	Input      json.RawMessage `json:"input"`
	Arguments  json.RawMessage `json:"arguments"`
	Output     json.RawMessage `json:"output"`
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	IsError    *bool           `json:"is_error"`
	ExitCode   *int            `json:"exit_code"`
}

func readCodex(file string, index assetIndex, projectRoot string) (Session, int, bool, string) {
	var sessionID, cwd, version, model, title, taskText string
	var started, ended *time.Time
	var turns []normalizedMessage
	evidence := 0
	warning := ""

	err := eachJSONLine(file, func(line int, raw []byte) error {
		var record codexRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
		at := parseTime(record.Timestamp)
		if at != nil {
			started, ended = extendBounds(started, ended, at)
		}
		var payload codexPayload
		if len(record.Payload) == 0 || json.Unmarshal(record.Payload, &payload) != nil {
			return nil
		}
		switch record.Type {
		case "session_meta":
			if sessionID == "" {
				sessionID = payload.ID
			}
			if cwd == "" {
				cwd = payload.CWD
			}
			if version == "" {
				version = payload.CLIVersion
			}
		case "turn_context":
			if cwd == "" {
				cwd = payload.CWD
			}
			if model == "" {
				model = payload.Model
			}
		case "response_item", "response_item_event":
			ref := firstNonEmpty(payload.ID, payload.CallID, fmt.Sprintf("line-%d", line))
			switch payload.Type {
			case "message":
				role := payload.Role
				if !isTranscriptRole(role) {
					return nil
				}
				text, truncated := boundText(codexContentText(payload.Content), maxTranscriptText)
				if text == "" || isGeneratedNoise(text) {
					return nil
				}
				if taskText == "" && role == "user" && meaningfulTaskText(text) {
					taskText, _ = boundText(text, maxTaskText)
				}
				turns = append(turns, normalizedMessage{ID: ref, Timestamp: formatOptionalTime(at), Role: role, Kind: "message", Text: text, Truncated: truncated})
			case "custom_tool_call", "function_call":
				input := rawText(payload.Input)
				if input == "" {
					input = rawText(payload.Arguments)
				}
				input, truncated := boundText(input, maxToolPayload)
				invocations := index.invocationsInText(input)
				turns = append(turns, normalizedMessage{ID: ref, Timestamp: formatOptionalTime(at), Role: "assistant", Kind: "tool_call", ToolName: payload.Name, ToolInput: input, Truncated: truncated, Invocations: invocations})
				evidence += len(invocations)
			case "custom_tool_call_output", "function_call_output":
				output, truncated := boundText(codexToolOutputText(payload.Output), maxToolPayload)
				isError, exitCode := normalizeToolFailure(output, payload.IsError, payload.ExitCode)
				if output == "" && isError == nil && exitCode == nil {
					return nil
				}
				turns = append(turns, normalizedMessage{ID: ref, Timestamp: formatOptionalTime(at), Role: "tool", Kind: "tool_result", ToolOutput: output, Truncated: truncated, IsError: isError, ExitCode: exitCode})
			}
		}
		return nil
	})
	if err != nil {
		warning = fmt.Sprintf("%s: %v", file, err)
	}
	if projectRoot != "" && (cwd == "" || !within(projectRoot, cwd)) {
		return Session{}, 0, false, warning
	}
	if sessionID == "" {
		sessionID = strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	}
	if title == "" && taskText != "" {
		title, _ = boundText(taskText, maxSessionTitle)
	}
	raw, marshalErr := marshalCodex(sessionID, cwd, version, model, title, taskText, started, ended, turns)
	if marshalErr != nil {
		return Session{}, 0, false, fmt.Sprintf("%s: normalize: %v", file, marshalErr)
	}
	taskTags := nativeTaskTags(taskText, cwd)
	opportunityAssetIDs := nativeOpportunityAssetIDs(taskText, turns, index)
	return Session{Input: ingest.SessionInput{
		Raw:                 adapters.RawSession{Source: adapters.SourceCodex, SessionID: sessionID, RawJSON: raw, SourcePath: file},
		TaskTags:            taskTags,
		OpportunityAssetIDs: opportunityAssetIDs,
	}, SourcePath: file}, evidence, true, warning
}

func (index assetIndex) invocationsInText(text string) []normalizedInvocation {
	if text == "" {
		return nil
	}
	var out []normalizedInvocation
	basenameCounts := make(map[string]int)
	for _, ref := range index.refs {
		basenameCounts[filepath.Base(ref.path)]++
	}
	for _, ref := range index.refs {
		if containsPathReference(text, ref.path) {
			out = append(out, normalizedInvocation{AssetID: ref.asset.ID, ParticipationSignal: canonical.SignalLoaded, ObservationLevel: canonical.LevelLoaded})
			continue
		}
		if index.projectRoot != "" && within(index.projectRoot, ref.path) {
			rel, err := filepath.Rel(index.projectRoot, ref.path)
			if err == nil && rel != "." && containsPathReference(text, rel) {
				out = append(out, normalizedInvocation{AssetID: ref.asset.ID, ParticipationSignal: canonical.SignalLoaded, ObservationLevel: canonical.LevelLoaded})
			}
			continue
		}
		// A basename is accepted only when it identifies exactly one indexed
		// asset. This supports native tool input such as `Read AGENTS.md`
		// without turning a common basename into evidence for an arbitrary
		// project asset.
		base := filepath.Base(ref.path)
		if basenameCounts[base] == 1 && containsPathReference(text, base) {
			out = append(out, normalizedInvocation{AssetID: ref.asset.ID, ParticipationSignal: canonical.SignalLoaded, ObservationLevel: canonical.LevelLoaded})
		}
	}
	return dedupeInvocations(out)
}

func nativeOpportunityAssetIDs(taskText string, messages []normalizedMessage, index assetIndex) []string {
	seen := make(map[string]struct{})
	for _, message := range messages {
		for _, invocation := range message.Invocations {
			if invocation.AssetID != "" {
				seen[invocation.AssetID] = struct{}{}
			}
		}
	}
	for _, invocation := range index.invocationsInText(taskText) {
		if invocation.AssetID != "" {
			seen[invocation.AssetID] = struct{}{}
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// nativeTaskTags is intentionally a small, reviewable ruleset. It classifies
// only recorded task text; it does not inspect the session id, event count, or
// asset presence. The fallback keeps a meaningful task in the denominator
// without pretending that an unrecognized task belongs to a known category.
func nativeTaskTags(taskText, cwd string) []string {
	if !meaningfulTaskText(taskText) {
		return nil
	}
	text := strings.ToLower(taskText)
	tags := make(map[string]struct{})
	add := func(tag string) { tags[tag] = struct{}{} }
	containsAny := func(values ...string) bool {
		for _, value := range values {
			if strings.Contains(text, value) {
				return true
			}
		}
		return false
	}
	if containsAny("了解", "分析", "梳理", "排查", "诊断", "审查", "review", "analy", "investigat", "why") {
		add("analysis")
	}
	if containsAny("实现", "开发", "修复", "构建", "重做", "实施", "implement", "develop", "fix", "build", "create", "add") {
		add("implementation")
	}
	if containsAny("测试", "验证", "校验", "截图", "对比", "test", "verify", "check", "qa", "compare", "screenshot") {
		add("verification")
	}
	if containsAny("页面", "前端", "界面", "图标", "样式", "布局", "交互", "动画", "动效", "ui", "frontend", "visual", "design", "prototype") {
		add("ui")
	}
	if containsAny("后端", "接口", "服务", "daemon", "api", "backend", "server") {
		add("backend")
	}
	if containsAny("数据库", "sqlite", "sql", "迁移", "schema", "database", "migration") {
		add("data")
	}
	if containsAny("文档", "规划", "方案", "规范", "说明", "docs", "document", "plan", "spec") {
		add("documentation")
	}
	if containsAny("重构", "清理", "refactor", "cleanup") {
		add("refactor")
	}
	if containsAny("研究", "调研", "查找", "research") {
		add("research")
	}
	if len(tags) == 0 {
		add("recorded-task")
	}
	if workspace := asciiSlug(filepath.Base(filepath.Clean(cwd))); workspace != "" && workspace != "." {
		add("workspace-" + workspace)
	}
	out := make([]string, 0, len(tags))
	for tag := range tags {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

func asciiSlug(value string) string {
	var b strings.Builder
	previousDash := false
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			previousDash = false
			continue
		}
		if b.Len() > 0 && !previousDash {
			b.WriteByte('-')
			previousDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func containsPathReference(text, reference string) bool {
	text = strings.ReplaceAll(text, `\`, "/")
	reference = filepath.ToSlash(filepath.Clean(reference))
	if strings.TrimSpace(reference) == "" || reference == "." {
		return false
	}
	for start := 0; ; {
		index := strings.Index(text[start:], reference)
		if index < 0 {
			return false
		}
		index += start
		beforeOK := index == 0 || !pathReferenceRune(text[index-1])
		after := index + len(reference)
		afterOK := after == len(text) || !pathReferenceRune(text[after])
		if beforeOK && afterOK {
			return true
		}
		start = index + 1
		if start >= len(text) {
			return false
		}
	}
}

func pathReferenceRune(value byte) bool {
	return value == '_' || value == '-' || value == '.' || (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || (value >= '0' && value <= '9')
}

func dedupeInvocations(items []normalizedInvocation) []normalizedInvocation {
	seen := make(map[string]struct{}, len(items))
	out := make([]normalizedInvocation, 0, len(items))
	for _, item := range items {
		if _, ok := seen[item.AssetID]; ok {
			continue
		}
		seen[item.AssetID] = struct{}{}
		out = append(out, item)
	}
	return out
}

func marshalClaude(sessionID, cwd, version, model, title, taskText string, started, ended *time.Time, messages []normalizedMessage) ([]byte, error) {
	input := map[string]any{"session": map[string]any{"id": sessionID, "title": title, "task_text": taskText, "started_at": formatOptionalTime(started), "ended_at": formatOptionalTime(ended), "harness_version": version, "model": model, "cwd": cwd}, "messages": make([]any, 0, len(messages))}
	items := input["messages"].([]any)
	for _, message := range messages {
		items = append(items, normalizedMessageMap(message))
	}
	input["messages"] = items
	return json.Marshal(input)
}

func marshalCodex(sessionID, cwd, version, model, title, taskText string, started, ended *time.Time, turns []normalizedMessage) ([]byte, error) {
	input := map[string]any{"session": map[string]any{"id": sessionID, "title": title, "task_text": taskText, "started_at": formatOptionalTime(started), "ended_at": formatOptionalTime(ended), "harness_version": version, "model": model, "cwd": cwd}, "turns": make([]any, 0, len(turns))}
	items := input["turns"].([]any)
	for _, turn := range turns {
		items = append(items, normalizedMessageMap(turn))
	}
	input["turns"] = items
	return json.Marshal(input)
}

func normalizedMessageMap(message normalizedMessage) map[string]any {
	invocations := make([]any, 0, len(message.Invocations))
	for _, invocation := range message.Invocations {
		invocations = append(invocations, map[string]any{"asset_id": invocation.AssetID, "participation_signal": invocation.ParticipationSignal, "observation_level": invocation.ObservationLevel})
	}
	item := map[string]any{"id": message.ID, "timestamp": message.Timestamp, "role": message.Role, "kind": message.Kind, "asset_invocations": invocations}
	if message.Text != "" {
		item["text"] = message.Text
	}
	if message.ToolName != "" {
		item["tool_name"] = message.ToolName
	}
	if message.ToolInput != "" {
		item["tool_input"] = message.ToolInput
	}
	if message.ToolOutput != "" {
		item["tool_output"] = message.ToolOutput
	}
	if message.IsError != nil {
		item["is_error"] = *message.IsError
	}
	if message.ExitCode != nil {
		item["exit_code"] = *message.ExitCode
	}
	if message.Truncated {
		item["truncated"] = true
	}
	return item
}

func normalizeToolFailure(output string, explicitIsError *bool, explicitExitCode *int) (*bool, *int) {
	var isError *bool
	if explicitIsError != nil {
		value := *explicitIsError
		isError = &value
	} else if strings.Contains(output, "<tool_use_error>") {
		value := true
		isError = &value
	}
	var exitCode *int
	if explicitExitCode != nil {
		value := *explicitExitCode
		exitCode = &value
	} else if matches := nativeExitCodePattern.FindStringSubmatch(output); len(matches) == 2 {
		if value, err := strconv.Atoi(matches[1]); err == nil {
			exitCode = &value
		}
	}
	return isError, exitCode
}

func codexToolOutputText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if value := codexContentText(raw); value != "" && strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
		return value
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil {
		for _, key := range []string{"tool_output", "output", "text"} {
			if value, ok := object[key]; ok {
				if text := codexToolOutputText(value); text != "" {
					return text
				}
			}
		}
	}
	return rawText(raw)
}

func claudeContentBlocks(raw json.RawMessage) ([]json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) == nil {
		return blocks, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		encoded, _ := json.Marshal(map[string]any{"type": "text", "text": text})
		return []json.RawMessage{encoded}, nil
	}
	return nil, fmt.Errorf("unsupported Claude content shape")
}

func rawContentText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if value := rawText(raw); value != "" && value != string(raw) {
		return value
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, block := range blocks {
			var item struct {
				Type    string          `json:"type"`
				Text    string          `json:"text"`
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal(block, &item) != nil {
				continue
			}
			if item.Text != "" {
				parts = append(parts, item.Text)
			} else if item.Content != nil {
				if value := rawContentText(item.Content); value != "" {
					parts = append(parts, value)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return rawText(raw)
}

func codexContentText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		parts := make([]string, 0, len(blocks))
		for _, block := range blocks {
			if block.Type == "input_text" || block.Type == "output_text" || block.Type == "text" || block.Text != "" {
				if strings.TrimSpace(block.Text) != "" {
					parts = append(parts, block.Text)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return rawText(raw)
}

func isTranscriptRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user", "assistant", "tool":
		return true
	default:
		return false
	}
}

func isGeneratedNoise(value string) bool {
	text := strings.TrimSpace(value)
	if text == "" {
		return true
	}
	for _, prefix := range []string{
		"<local-command-caveat>", "<command-name>", "<command-message>", "<command-args>",
		"<local-command-stdout>", "<local-command-stderr>", "<local-command-result>",
		"<system-reminder>", "<task-notification>", "Async agent launched successfully",
		"(Bash completed", "File does not exist",
	} {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return strings.Contains(text, "<INSTRUCTIONS>") || strings.Contains(text, "<permissions instructions>")
}

func meaningfulTaskText(value string) bool {
	text := strings.TrimSpace(value)
	if isGeneratedNoise(text) || len([]rune(text)) < 2 {
		return false
	}
	if strings.HasPrefix(text, "# AGENTS.md instructions") || strings.Contains(text, "AUTONOMY DIRECTIVE") {
		return false
	}
	return true
}

func boundText(value string, limit int) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value, false
	}
	return string(runes[:limit]) + "…", true
}

func marshalBoundedJSON(value any, limit int) string {
	if value == nil {
		return ""
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	text, _ := boundText(string(encoded), limit)
	return text
}

func jsonWasTruncated(value string, original any, limit int) bool {
	if value == "" || original == nil {
		return false
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		return false
	}
	return len([]rune(string(encoded))) > limit
}

func eachJSONLine(file string, fn func(line int, raw []byte) error) error {
	input, err := os.Open(file)
	if err != nil {
		return err
	}
	defer input.Close()
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		if err := fn(line, scanner.Bytes()); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func jsonlFiles(root string, excludeSubagents bool) ([]string, error) {
	if strings.TrimSpace(root) == "" {
		return nil, nil
	}
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if excludeSubagents && entry.Name() == "subagents" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(entry.Name(), ".jsonl") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func jsonlFilesForRoots(primary string, extra []string, includeSubagents bool) ([]string, error) {
	roots := make([]string, 0, 1+len(extra))
	seenRoots := make(map[string]struct{}, 1+len(extra))
	for _, root := range append([]string{primary}, extra...) {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		if _, seen := seenRoots[root]; seen {
			continue
		}
		seenRoots[root] = struct{}{}
		roots = append(roots, root)
	}
	var files []string
	seenFiles := make(map[string]struct{})
	for _, root := range roots {
		items, err := jsonlFiles(root, !includeSubagents)
		if err != nil {
			return nil, err
		}
		for _, file := range items {
			if _, seen := seenFiles[file]; seen {
				continue
			}
			seenFiles[file] = struct{}{}
			files = append(files, file)
		}
	}
	sort.Strings(files)
	return files, nil
}

func currentFileStamp(path string) (FileStamp, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return FileStamp{}, false
	}
	return FileStamp{Size: info.Size(), ModTimeUnixNano: info.ModTime().UnixNano()}, true
}

func unchangedFile(path string, known map[string]FileStamp) (FileStamp, bool) {
	if len(known) == 0 {
		return FileStamp{}, false
	}
	current, ok := currentFileStamp(path)
	if !ok {
		return FileStamp{}, false
	}
	previous, exists := known[path]
	return current, exists && current == previous
}

func codexFileMayBelongToProject(file, projectRoot string) bool {
	if strings.TrimSpace(projectRoot) == "" {
		return true
	}
	input, err := os.Open(file)
	if err != nil {
		return false
	}
	defer input.Close()
	line, err := bufio.NewReaderSize(input, 256*1024).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return false
	}
	var record codexRecord
	if json.Unmarshal(line, &record) != nil {
		return false
	}
	var payload codexPayload
	if json.Unmarshal(record.Payload, &payload) != nil {
		return false
	}
	return payload.CWD != "" && within(projectRoot, payload.CWD)
}

func within(root, value string) bool {
	root, value = filepath.Clean(root), filepath.Clean(value)
	if root == "." || value == "." {
		return root == value
	}
	rel, err := filepath.Rel(root, value)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func parseTime(value string) *time.Time {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	parsed = parsed.UTC()
	return &parsed
}

func extendBounds(started, ended, at *time.Time) (*time.Time, *time.Time) {
	if at == nil {
		return started, ended
	}
	if started == nil || at.Before(*started) {
		value := *at
		started = &value
	}
	if ended == nil || at.After(*ended) {
		value := *at
		ended = &value
	}
	return started, ended
}

func formatOptionalTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func rawText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
