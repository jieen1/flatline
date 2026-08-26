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
	"sort"
	"strings"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/assets"
	"flatline/internal/canonical"
	"flatline/internal/eventstore"
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
	OpenCodeDB       string
	DSHRoot          string
	DSHRoots         []string
	HermesRoot       string
	ProjectRoot      string
	Assets           []assets.Asset
	IncludeSubagents bool
	KnownFiles       map[string]FileStamp
	// OnFile, when set, is called after each discovered file with the running
	// seen/read/skipped counts so a long first pass can report real progress.
	OnFile func(seen, read, skipped int)
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
	Sources            []SourceStatus       `json:"sources,omitempty"`
	FileStamps         map[string]FileStamp `json:"-"`
}

// Discover scans the configured roots in deterministic path order. Missing
// optional roots are treated as empty, not as an error.
func Discover(config Config) ([]Session, Report, error) {
	index := newAssetIndex(config.Assets, config.ProjectRoot)
	report := Report{Warnings: make([]string, 0), FileStamps: make(map[string]FileStamp)}
	var out []Session

	notify := func() {
		if config.OnFile != nil {
			config.OnFile(report.FilesSeen, report.FilesRead, report.FilesSkipped)
		}
	}

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
		notify()
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
		notify()
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
	// Sources added after the two JSONL harnesses register here. Each one owns
	// its own reader and its own fingerprint rule; nothing above changes.
	openCodeSessions, openCodeStatus := discoverOpenCode(config, index, &report, notify)
	out = append(out, openCodeSessions...)
	dshSessions, dshStatus := discoverDSH(config, index, &report, notify)
	out = append(out, dshSessions...)
	report.Sources = []SourceStatus{
		claudeStatus(config, len(claudeFiles)),
		codexStatus(config, len(codexFiles)),
		openCodeStatus, dshStatus, probeHermes(config.HermesRoot),
	}
	recordSourceStatuses(report.Sources)
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
	CallID      string
	ToolInput   string
	ToolOutput  string
	Truncated   bool
	IsError     *bool
	ExitCode    *int
	AgentID     string
	Sidechain   bool
	AbortReason string
	Invocations []normalizedInvocation
	// UsageKey is the assistant message id the source attached usage to. It is
	// resolved to the message's final usage after the whole file is read (a
	// streaming message's usage grows across its follow-up records) and never
	// marshaled itself; see usageFor.
	UsageKey string
	Usage    *MessageUsage
	HasUsage bool
	// TurnTokens is what the harness's own running token total says this whole
	// user turn cost, attributed by subtraction (ADR-23). It sits on the user
	// message that opened the turn and is nil when the source recorded no
	// running total at all.
	TurnTokens *int64
}

// usageFor resolves every message's UsageKey against the folded accumulator.
// A key the source never attached usage to stays unrecorded — no zero filling.
func usageFor(messages []normalizedMessage, usage *usageAccumulator) {
	for index := range messages {
		message := &messages[index]
		if message.UsageKey == "" {
			continue
		}
		if measured, found := usage.UsageForMessage(message.UsageKey); found {
			message.Usage = &measured
			message.HasUsage = true
		}
	}
}

// threadInfo is what a source records about where a session came from. Every
// field stays empty when the source did not record it; nothing here is derived
// from an id or a filename.
type threadInfo struct {
	Kind            string
	ParentSessionID string
	AgentRole       string
	AgentNickname   string
	Originator      string
}

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
	Type        string `json:"type"`
	SessionID   string `json:"sessionId"`
	SessionID2  string `json:"session_id"`
	IsSidechain bool   `json:"isSidechain"`
	AgentID     string `json:"agentId"`
	UUID        string `json:"uuid"`
	Timestamp   string `json:"timestamp"`
	CWD         string `json:"cwd"`
	Version     string `json:"version"`
	AITitle     string `json:"aiTitle"`
	Message     struct {
		ID         string          `json:"id"`
		Role       string          `json:"role"`
		Model      string          `json:"model"`
		StopReason string          `json:"stop_reason"`
		Usage      json.RawMessage `json:"usage"`
		Content    json.RawMessage `json:"content"`
	} `json:"message"`
}

// claudeSubagentID reads the agent id out of a subagent transcript path
// (…/<parent session>/subagents/agent-<agentId>.jsonl). An empty result means
// this file is a top-level thread.
func claudeSubagentID(file string) string {
	if filepath.Base(filepath.Dir(file)) != "subagents" {
		return ""
	}
	name := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	if !strings.HasPrefix(name, "agent-") {
		return ""
	}
	return strings.TrimPrefix(name, "agent-")
}

// claudeSubagentMeta is the record Claude Code writes beside a subagent
// transcript: what kind of agent was launched, what it was asked to do, and
// the parent's tool call that launched it.
type claudeSubagentMeta struct {
	AgentType   string `json:"agentType"`
	IsFork      bool   `json:"isFork"`
	Description string `json:"description"`
	ToolUseID   string `json:"toolUseId"`
	SpawnDepth  int    `json:"spawnDepth"`
}

// maxSubagentDescription bounds the launch description used as the thread's
// name.
const maxSubagentDescription = 120

// readClaudeSubagentMeta reads agent-<id>.meta.json next to the transcript.
// A missing file is not an error: the thread simply keeps no recorded role.
func readClaudeSubagentMeta(file string) (claudeSubagentMeta, bool) {
	raw, err := os.ReadFile(strings.TrimSuffix(file, filepath.Ext(file)) + ".meta.json")
	if err != nil {
		return claudeSubagentMeta{}, false
	}
	var meta claudeSubagentMeta
	if json.Unmarshal(raw, &meta) != nil {
		return claudeSubagentMeta{}, false
	}
	meta.AgentType = strings.TrimSpace(meta.AgentType)
	meta.Description = strings.TrimSpace(meta.Description)
	return meta, meta.AgentType != "" || meta.Description != ""
}

func readClaude(file string, index assetIndex, projectRoot string) (Session, int, bool, string) {
	// A Claude Code subagent writes its own transcript, but every record in it
	// carries the parent's sessionId. Read as-is, the subagent's work is merged
	// into the parent and a Claude session's tool-call count means something
	// different from a Codex one, where a subagent is its own session. The file
	// is what identifies the thread, so it becomes its own session here.
	agentID := claudeSubagentID(file)
	var sessionID, cwd, version, model, title, taskText string
	var started, ended *time.Time
	var messages []normalizedMessage
	usage := newUsageAccumulator()
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
		usage.observe(at)
		// Every assistant record carries the usage of the message it belongs
		// to, and the same message is written out again on each follow-up
		// record, so the accumulator dedupes on message.id. A subagent's tokens
		// were still spent in this session, so they count; its turns are the
		// subagent's own and are counted below only for the main thread.
		usage.addClaudeUsage(record.Message.ID, record.Message.Model, decodeClaudeUsage(record.Message.Usage))
		ownThread := agentID != "" || !record.IsSidechain
		if ownThread && strings.TrimSpace(record.Message.StopReason) == claudeEndTurn {
			usage.addClaudeAssistantTurn(record.Message.ID, record.Message.Model)
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
		firstMessage := len(messages)
		for blockIndex, rawBlock := range blocks {
			var block struct {
				Type      string          `json:"type"`
				ID        string          `json:"id"`
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
				if text == "" || !isTranscriptRole(role) {
					continue
				}
				// A command the user ran with `!` is written under the user
				// role but is not a turn. It stays in the transcript because it
				// is the only record that the command ran; the counts leave it
				// out with the other harness-written blocks.
				if _, userShell := canonical.UserShellCommand(text); userShell {
					messages = append(messages, normalizedMessage{ID: ref, Timestamp: occurred, Role: role, Kind: "message", Text: text, Truncated: truncated})
					continue
				}
				if isGeneratedNoise(text) {
					continue
				}
				if taskText == "" && role == "user" && meaningfulTaskText(text) {
					taskText, _ = boundText(text, maxTaskText)
				}
				if role == "user" && ownThread {
					usage.addUserTurn()
				}
				appended := normalizedMessage{ID: ref, Timestamp: occurred, Role: role, Kind: "message", Text: text, Truncated: truncated}
				if role == "assistant" {
					appended.UsageKey = record.Message.ID
				}
				messages = append(messages, appended)
			case "tool_use":
				// The edit is measured from the full recorded input; the copy
				// that goes into the event payload is bounded and cannot be
				// measured from.
				usage.recordChange(changeCounts(block.Name, block.Input))
				input := marshalBoundedJSON(block.Input, maxToolPayload)
				invocations := index.invocationForTool(block.Name, block.Input)
				messages = append(messages, normalizedMessage{ID: ref, Timestamp: occurred, Role: "assistant", Kind: "tool_call", ToolName: block.Name, CallID: block.ID, ToolInput: input, Truncated: jsonWasTruncated(input, block.Input, maxToolPayload), Invocations: invocations})
				evidence += len(invocations)
			case "tool_result":
				// A result with nothing in it is still a result: the call came
				// back. Dropping it loses one side of a pair and makes the
				// result count disagree with the transcript.
				output, truncated := boundText(rawContentText(block.Content), maxToolPayload)
				isError, exitCode := normalizeToolFailure(output, block.IsError, nil)
				messages = append(messages, normalizedMessage{ID: ref, Timestamp: occurred, Role: "tool", Kind: "tool_result", CallID: block.ToolUseID, ToolOutput: output, Truncated: truncated, IsError: isError, ExitCode: exitCode})
			}
		}
		// Sidechain records live in the same file set as their parent session
		// and are merged into it. The agent that produced them is recorded on
		// the event so the merge stays visible instead of silent.
		if record.IsSidechain || strings.TrimSpace(record.AgentID) != "" {
			for i := firstMessage; i < len(messages); i++ {
				messages[i].Sidechain = record.IsSidechain
				messages[i].AgentID = strings.TrimSpace(record.AgentID)
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
	thread := threadInfo{Kind: threadKindMain, Originator: originatorClaudeCode}
	if agentID != "" {
		// The records name the parent session; the file names the thread.
		if sessionID != "" && sessionID != agentID {
			thread.ParentSessionID = string(adapters.SourceClaudeCode) + ":" + sessionID
		}
		thread.Kind = threadKindSubagent
		sessionID = agentID
		// Claude Code writes what it launched this thread for in a small file
		// beside the transcript: the agent's kind, the task it was given, and
		// the parent tool call that started it. That is the harness's own name
		// for the thread, so it wins over the first user message — which for a
		// subagent is the boilerplate the harness prepends to the prompt.
		if meta, ok := readClaudeSubagentMeta(file); ok {
			thread.AgentRole = meta.AgentType
			thread.AgentNickname, _ = boundText(meta.Description, maxSubagentDescription)
			if title == "" && meta.Description != "" {
				title, _ = boundText(meta.Description, maxSessionTitle)
			}
		}
	}
	if title == "" && taskText != "" {
		title, _ = boundText(taskText, maxSessionTitle)
	}
	measured := usage.result()
	// The fold above settles every message's final usage; only now can the
	// transcript carry what each assistant message cost.
	usageFor(messages, usage)
	raw, marshalErr := marshalClaude(sessionID, cwd, version, model, title, taskText, started, ended, thread, messages, measured)
	if marshalErr != nil {
		return Session{}, 0, false, fmt.Sprintf("%s: normalize: %v", file, marshalErr)
	}
	taskTags := nativeTaskTags(taskText, cwd)
	opportunityAssetIDs := nativeOpportunityAssetIDs(taskText, messages, index)
	return Session{Input: ingest.SessionInput{
		Raw:                 adapters.RawSession{Source: adapters.SourceClaudeCode, SessionID: sessionID, RawJSON: raw, SourcePath: file},
		TaskTags:            taskTags,
		OpportunityAssetIDs: opportunityAssetIDs,
		Usage:               measured,
		ParserVersion:       ParserVersion,
	}, SourcePath: file}, evidence, true, warning
}

// SessionThread is what a native transcript records about where a session came
// from. Every field is empty when the source did not record it.
type SessionThread struct {
	Kind            string
	ParentSessionID string
	AgentRole       string
	AgentNickname   string
	Originator      string
}

// ReadCodexThread reads only the session_meta record of a Codex transcript, so
// a database migrated after the session was already ingested can be filled in
// without replaying the whole file. It reports false when the file records no
// session_meta.
func ReadCodexThread(file string) (SessionThread, bool) {
	var out SessionThread
	found := false
	stop := fmt.Errorf("history: session meta found")
	err := eachJSONLine(file, func(line int, raw []byte) error {
		var record codexRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil
		}
		if record.Type != "session_meta" || len(record.Payload) == 0 {
			return nil
		}
		var payload codexPayload
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			return nil
		}
		thread := codexThread(payload)
		out = SessionThread{Kind: thread.Kind, ParentSessionID: thread.ParentSessionID,
			AgentRole: thread.AgentRole, AgentNickname: thread.AgentNickname, Originator: thread.Originator}
		found = true
		return stop
	})
	if err != nil && err != stop {
		return SessionThread{}, false
	}
	return out, found
}

// ClaudeThread is the fixed thread identity of a Claude Code transcript file:
// the file is the top-level thread and the harness that wrote it is named.
func ClaudeThread() SessionThread {
	return SessionThread{Kind: threadKindMain, Originator: originatorClaudeCode}
}

type codexRecord struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexPayload struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	// A subagent thread names its parent and its role in session_meta. A
	// forked thread names only the thread it inherited context from.
	ParentThreadID string          `json:"parent_thread_id"`
	ForkedFromID   string          `json:"forked_from_id"`
	ThreadSource   string          `json:"thread_source"`
	AgentRole      string          `json:"agent_role"`
	AgentNickname  string          `json:"agent_nickname"`
	Originator     string          `json:"originator"`
	CWD            string          `json:"cwd"`
	CLIVersion     string          `json:"cli_version"`
	Model          string          `json:"model"`
	Name           string          `json:"name"`
	CallID         string          `json:"call_id"`
	Input          json.RawMessage `json:"input"`
	Arguments      json.RawMessage `json:"arguments"`
	Output         json.RawMessage `json:"output"`
	Role           string          `json:"role"`
	Content        json.RawMessage `json:"content"`
	IsError        *bool           `json:"is_error"`
	ExitCode       *int            `json:"exit_code"`
	// Codex closes an interrupted turn with an event_msg/turn_aborted record
	// naming the turn it ended and why.
	TurnID string `json:"turn_id"`
	Reason string `json:"reason"`
	// Info carries the running token totals of an event_msg/token_count record.
	Info json.RawMessage `json:"info"`
}

func readCodex(file string, index assetIndex, projectRoot string) (Session, int, bool, string) {
	var sessionID, cwd, version, model, title, taskText string
	var started, ended *time.Time
	var turns []normalizedMessage
	var thread threadInfo
	usage := newUsageAccumulator()
	turnModel := ""
	evidence := 0
	warning := ""

	// Turn-token attribution state (ADR-23): lastTotal is the harness's own
	// running total from the latest token_count; a real user message opens a
	// turn at that total, and the next user message, a turn_aborted, or the
	// end of the file closes it at whatever the total says then. A session
	// that never wrote a token_count attributes nothing.
	lastTotal := int64(0)
	tokensSeen := false
	turnOpen := false
	turnBaseline := int64(0)
	turnUserIdx := -1
	closeTurn := func() {
		if !turnOpen {
			return
		}
		turnOpen = false
		if !tokensSeen {
			return
		}
		if cost := lastTotal - turnBaseline; cost >= 0 && turnUserIdx >= 0 && turnUserIdx < len(turns) {
			turns[turnUserIdx].TurnTokens = &cost
		}
	}

	err := eachJSONLine(file, func(line int, raw []byte) error {
		var record codexRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
		at := parseTime(record.Timestamp)
		if at != nil {
			started, ended = extendBounds(started, ended, at)
		}
		usage.observe(at)
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
			if thread.Kind == "" {
				thread = codexThread(payload)
			}
		case "turn_context":
			if cwd == "" {
				cwd = payload.CWD
			}
			if model == "" {
				model = payload.Model
			}
			// One turn_context record is one assistant turn, and it names the
			// model that turn ran on.
			turnModel = strings.TrimSpace(payload.Model)
			usage.addAssistantTurn(turnModel)
		case "event_msg":
			// Two event_msg records Flatline keeps. token_count carries the
			// session's running token totals; turn_aborted says a turn ended
			// early and why. Nothing is inferred from either one's absence.
			if payload.Type == "token_count" {
				var info codexTokenInfo
				if len(payload.Info) > 0 && json.Unmarshal(payload.Info, &info) == nil {
					usage.setCodexTotals(info, turnModel)
					if info.TotalTokenUsage != nil && info.TotalTokenUsage.TotalTokens != nil {
						lastTotal = *info.TotalTokenUsage.TotalTokens
						tokensSeen = true
					}
				}
				return nil
			}
			if payload.Type != "turn_aborted" || strings.TrimSpace(payload.Reason) == "" {
				return nil
			}
			closeTurn()
			ref := firstNonEmpty(payload.TurnID, fmt.Sprintf("line-%d", line))
			turns = append(turns, normalizedMessage{ID: ref, Timestamp: formatOptionalTime(at),
				Role: "system", Kind: "message", AbortReason: strings.TrimSpace(payload.Reason)})
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
				if role == "user" {
					usage.addUserTurn()
					closeTurn()
					turnOpen = true
					turnBaseline = lastTotal
					turnUserIdx = len(turns)
				}
				turns = append(turns, normalizedMessage{ID: ref, Timestamp: formatOptionalTime(at), Role: role, Kind: "message", Text: text, Truncated: truncated})
			case "custom_tool_call", "function_call":
				full := rawText(payload.Input)
				if full == "" {
					full = rawText(payload.Arguments)
				}
				// Codex writes an apply_patch body inside the exec script, so
				// the edit is measured from the full input before it is bounded.
				usage.recordChange(patchChanges(full))
				input, truncated := boundText(full, maxToolPayload)
				invocations := index.invocationsInText(input)
				turns = append(turns, normalizedMessage{ID: ref, Timestamp: formatOptionalTime(at), Role: "assistant", Kind: "tool_call", ToolName: payload.Name, CallID: payload.CallID, ToolInput: input, Truncated: truncated, Invocations: invocations})
				evidence += len(invocations)
			case "custom_tool_call_output", "function_call_output":
				// An empty result is still a result; see readClaude.
				output, truncated := boundText(codexToolOutputText(payload.Output), maxToolPayload)
				isError, exitCode := normalizeToolFailure(output, payload.IsError, payload.ExitCode)
				turns = append(turns, normalizedMessage{ID: ref, Timestamp: formatOptionalTime(at), Role: "tool", Kind: "tool_result", CallID: payload.CallID, ToolOutput: output, Truncated: truncated, IsError: isError, ExitCode: exitCode})
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
	// A turn still open at end of file closed when the session ended.
	closeTurn()
	measured := usage.result()
	raw, marshalErr := marshalCodex(sessionID, cwd, version, model, title, taskText, started, ended, thread, turns, measured)
	if marshalErr != nil {
		return Session{}, 0, false, fmt.Sprintf("%s: normalize: %v", file, marshalErr)
	}
	taskTags := nativeTaskTags(taskText, cwd)
	opportunityAssetIDs := nativeOpportunityAssetIDs(taskText, turns, index)
	return Session{Input: ingest.SessionInput{
		Raw:                 adapters.RawSession{Source: adapters.SourceCodex, SessionID: sessionID, RawJSON: raw, SourcePath: file},
		TaskTags:            taskTags,
		OpportunityAssetIDs: opportunityAssetIDs,
		Usage:               measured,
		ParserVersion:       ParserVersion,
	}, SourcePath: file}, evidence, true, warning
}

// pathSuffixSegments is how much of an asset's path a mention has to reproduce
// before it counts as naming that asset. A bare basename does not: one project
// writing /tmp/x/taskboard/__init__.py was recorded 61 times as a load of
// another project's .../hookify/hooks/__init__.py, because both files are
// called __init__.py. Two segments — the file and the directory holding it —
// is the shortest reference that says which file is meant.
const pathSuffixSegments = 2

// pathSuffix returns the last n segments of a path, or "" when the path is
// shorter than that. A single-segment path therefore names no asset by suffix:
// it would be the basename rule again.
func pathSuffix(value string, n int) string {
	segments := strings.Split(filepath.ToSlash(filepath.Clean(value)), "/")
	if len(segments) < n {
		return ""
	}
	tail := segments[len(segments)-n:]
	for _, segment := range tail {
		if segment == "" {
			return ""
		}
	}
	return strings.Join(tail, "/")
}

func (index assetIndex) invocationsInText(text string) []normalizedInvocation {
	if text == "" {
		return nil
	}
	var out []normalizedInvocation
	suffixCounts := make(map[string]int)
	for _, ref := range index.refs {
		if suffix := pathSuffix(ref.path, pathSuffixSegments); suffix != "" {
			suffixCounts[suffix]++
		}
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
		// A two-segment suffix is accepted only when it identifies exactly one
		// indexed asset, so a mention that could mean either of two assets
		// means neither.
		suffix := pathSuffix(ref.path, pathSuffixSegments)
		if suffix != "" && suffixCounts[suffix] == 1 && containsPathReference(text, suffix) {
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

const (
	threadKindMain       = "main"
	threadKindSubagent   = "subagent"
	originatorClaudeCode = "claude_code"
)

// codexThread applies the one hierarchy rule Codex records: a thread whose
// session_meta says thread_source=subagent is a subagent of parent_thread_id;
// anything else is a main thread. A forked thread is still a main thread, but
// it names the thread it inherited its context from.
func codexThread(payload codexPayload) threadInfo {
	thread := threadInfo{Kind: threadKindMain, Originator: strings.TrimSpace(payload.Originator)}
	if strings.TrimSpace(payload.ThreadSource) == threadKindSubagent {
		thread.Kind = threadKindSubagent
		thread.AgentRole = strings.TrimSpace(payload.AgentRole)
		thread.AgentNickname = strings.TrimSpace(payload.AgentNickname)
	}
	parent := firstNonEmpty(payload.ParentThreadID, payload.ForkedFromID)
	if parent != "" {
		thread.ParentSessionID = string(adapters.SourceCodex) + ":" + strings.TrimSpace(parent)
	}
	return thread
}

func sessionMap(sessionID, cwd, version, model, title, taskText string, started, ended *time.Time, thread threadInfo) map[string]any {
	return map[string]any{
		"id": sessionID, "title": title, "task_text": taskText,
		"started_at": formatOptionalTime(started), "ended_at": formatOptionalTime(ended),
		"harness_version": version, "model": model, "cwd": cwd,
		"thread_kind": thread.Kind, "parent_session_id": thread.ParentSessionID,
		"agent_role": thread.AgentRole, "agent_nickname": thread.AgentNickname,
		"originator": thread.Originator,
	}
}

func marshalClaude(sessionID, cwd, version, model, title, taskText string, started, ended *time.Time, thread threadInfo, messages []normalizedMessage, usage *eventstore.SessionUsage) ([]byte, error) {
	items := make([]any, 0, len(messages))
	for _, message := range messages {
		items = append(items, normalizedMessageMap(message, "tool_use_id"))
	}
	return json.Marshal(map[string]any{
		"session":  sessionMap(sessionID, cwd, version, model, title, taskText, started, ended, thread),
		"messages": items,
		"usage":    usageMap(usage),
	})
}

func marshalCodex(sessionID, cwd, version, model, title, taskText string, started, ended *time.Time, thread threadInfo, turns []normalizedMessage, usage *eventstore.SessionUsage) ([]byte, error) {
	items := make([]any, 0, len(turns))
	for _, turn := range turns {
		items = append(items, normalizedMessageMap(turn, "call_id"))
	}
	return json.Marshal(map[string]any{
		"session": sessionMap(sessionID, cwd, version, model, title, taskText, started, ended, thread),
		"turns":   items,
		"usage":   usageMap(usage),
	})
}

func normalizedMessageMap(message normalizedMessage, callIDKey string) map[string]any {
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
	if message.CallID != "" {
		item[callIDKey] = message.CallID
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
	if message.AgentID != "" {
		item["agent_id"] = message.AgentID
	}
	if message.Sidechain {
		item["sidechain"] = true
	}
	if message.AbortReason != "" {
		item["abort_reason"] = message.AbortReason
	}
	if message.TurnTokens != nil {
		item["turn_tokens"] = *message.TurnTokens
	}
	if message.HasUsage && message.Usage != nil {
		item["usage"] = map[string]any{
			"input_tokens":        message.Usage.Input,
			"cached_input_tokens": message.Usage.CachedInput,
			"cache_write_tokens":  message.Usage.CacheWrite,
			"output_tokens":       message.Usage.Output,
			"reasoning_tokens":    message.Usage.Reasoning,
			"total_tokens":        message.Usage.Total,
		}
	}
	return item
}

func normalizeToolFailure(output string, explicitIsError *bool, explicitExitCode *int) (*bool, *int) {
	return canonical.NormalizeToolFailure(output, explicitIsError, explicitExitCode)
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

// isGeneratedNoise reports whether a recorded text is a harness-injected block
// rather than something a person or a model wrote. The closed list lives in
// canonical, because the session projection needs the same set for the records
// an older parser stored before this rule existed.
func isGeneratedNoise(value string) bool {
	text := strings.TrimSpace(value)
	if text == "" {
		return true
	}
	if prefix := canonical.InjectedMessagePrefix(text); prefix != "" {
		noteInjectedSkip(prefix)
		return true
	}
	if strings.Contains(text, "<INSTRUCTIONS>") || strings.Contains(text, "<permissions instructions>") {
		noteInjectedSkip("<INSTRUCTIONS>")
		return true
	}
	return false
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
			file = resolvedFile(file)
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

// resolvedFile is the path a transcript actually lives at. Claude Code links a
// subagent transcript into a second parent's directory, so the same file is
// reachable under two paths — and read under both, which measured one session's
// tokens and active time twice. The real path is the file's identity; a path
// that cannot be resolved is kept exactly as it was walked.
func resolvedFile(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
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
