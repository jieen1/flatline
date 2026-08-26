// Package claudecode maps synthetic Claude Code session records to canonical facts.
package claudecode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/canonical"
)

const adapterVersion = "claudecode/1"

type Adapter struct{}

func New() Adapter                      { return Adapter{} }
func (Adapter) Source() adapters.Source { return adapters.SourceClaudeCode }
func (Adapter) Version() string         { return adapterVersion }

func (Adapter) FieldMatrix() adapters.FieldMatrix {
	return adapters.FieldMatrix{
		Supported:   []string{"source_session_id", "started_at", "ended_at", "harness_version", "model", "cwd", "asset_invocation", "asset_content_hash", "reference_inputs", "followed_signal", "violated_signal", "observation_level", "locator"},
		Unsupported: []string{"raw_source_bytes"},
		Unrecorded:  []string{"loaded_signal", "offered_signal"},
	}
}

type fixture struct {
	Session  session   `json:"session"`
	Messages []message `json:"messages"`
}
type session struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	TaskText       string `json:"task_text"`
	StartedAt      string `json:"started_at"`
	EndedAt        string `json:"ended_at"`
	HarnessVersion string `json:"harness_version"`
	Model          string `json:"model"`
	CWD            string `json:"cwd"`

	ThreadKind      string `json:"thread_kind"`
	ParentSessionID string `json:"parent_session_id"`
	AgentRole       string `json:"agent_role"`
	AgentNickname   string `json:"agent_nickname"`
	Originator      string `json:"originator"`
}
type message struct {
	ID               string       `json:"id"`
	Timestamp        string       `json:"timestamp"`
	Role             string       `json:"role"`
	Kind             string       `json:"kind"`
	Text             string       `json:"text"`
	Content          string       `json:"content"`
	ToolName         string       `json:"tool_name"`
	ToolUseID        string       `json:"tool_use_id"`
	ToolInput        string       `json:"tool_input"`
	ToolOutput       string       `json:"tool_output"`
	Truncated        bool         `json:"truncated"`
	IsError          *bool        `json:"is_error"`
	ExitCode         *int         `json:"exit_code"`
	AgentID          string       `json:"agent_id"`
	Sidechain        bool         `json:"sidechain"`
	Usage            *usageRecord `json:"usage"`
	AssetInvocations []invocation `json:"asset_invocations"`
}

// usageRecord is the per-message token record the parser copied from the
// source's own usage block. It passes through untouched.
type usageRecord struct {
	Input       int64 `json:"input_tokens"`
	CachedInput int64 `json:"cached_input_tokens"`
	CacheWrite  int64 `json:"cache_write_tokens"`
	Output      int64 `json:"output_tokens"`
	Reasoning   int64 `json:"reasoning_tokens"`
	Total       int64 `json:"total_tokens"`
}
type invocation struct {
	AssetID             string                         `json:"asset_id"`
	ContentHash         string                         `json:"content_hash"`
	References          []string                       `json:"references"`
	ParticipationSignal *canonical.ParticipationSignal `json:"participation_signal"`
	ObservationLevel    *canonical.ObservationLevel    `json:"observation_level"`
	Followed            *bool                          `json:"followed"`
	Violated            *bool                          `json:"violated"`
}

func (Adapter) DetectVersion(raw adapters.RawSession) (adapters.VersionInfo, error) {
	var input fixture
	if err := json.Unmarshal(raw.RawJSON, &input); err != nil {
		return adapters.VersionInfo{}, fmt.Errorf("claudecode: decode session: %w", err)
	}
	return adapters.VersionInfo{HarnessVersion: input.Session.HarnessVersion, Model: input.Session.Model, Raw: input.Session.HarnessVersion}, nil
}

func (a Adapter) Parse(raw adapters.RawSession) (adapters.SessionMeta, []canonical.Event, error) {
	if raw.Source != a.Source() {
		return adapters.SessionMeta{}, nil, fmt.Errorf("claudecode: source is %q", raw.Source)
	}
	var input fixture
	if err := json.Unmarshal(raw.RawJSON, &input); err != nil {
		return adapters.SessionMeta{}, nil, fmt.Errorf("claudecode: decode session: %w", err)
	}
	id := input.Session.ID
	if id == "" {
		id = raw.SessionID
	}
	if id == "" {
		return adapters.SessionMeta{}, nil, fmt.Errorf("claudecode: session id is missing")
	}
	started, err := parseTime(input.Session.StartedAt)
	if err != nil {
		return adapters.SessionMeta{}, nil, fmt.Errorf("claudecode: started_at: %w", err)
	}
	ended, err := parseTime(input.Session.EndedAt)
	if err != nil {
		return adapters.SessionMeta{}, nil, fmt.Errorf("claudecode: ended_at: %w", err)
	}
	qualified := string(a.Source()) + ":" + id
	meta := adapters.SessionMeta{SourceSessionID: id, StartedAt: started, EndedAt: ended, HarnessVersion: input.Session.HarnessVersion, Model: input.Session.Model, CWD: input.Session.CWD, Title: input.Session.Title, TaskText: input.Session.TaskText,
		ThreadKind: input.Session.ThreadKind, ParentSessionID: input.Session.ParentSessionID,
		AgentRole: input.Session.AgentRole, AgentNickname: input.Session.AgentNickname,
		Originator: input.Session.Originator}
	events := []canonical.Event{{
		SourceEventID: stableID(qualified, "session"), SessionID: qualified, EventType: canonical.EventTypeSessionStarted,
		ObservationLevel: canonical.LevelUnknown, Payload: map[string]any{"source_session_id": id},
		Locator: canonical.Locator{Source: string(a.Source()), SessionID: qualified, RawRef: "session"}, OccurredAt: started, AdapterVersion: a.Version(),
	}}
	for messageIndex, message := range input.Messages {
		messageRef := message.ID
		if messageRef == "" {
			messageRef = fmt.Sprintf("message-%d", messageIndex)
		}
		text := message.Text
		if text == "" {
			text = message.Content
		}
		if message.Kind != "" || text != "" || message.ToolName != "" || message.ToolUseID != "" || message.ToolInput != "" || message.ToolOutput != "" || message.IsError != nil || message.ExitCode != nil {
			occurred, err := parseTime(message.Timestamp)
			if err != nil {
				return adapters.SessionMeta{}, nil, fmt.Errorf("claudecode: message %s timestamp: %w", messageRef, err)
			}
			kind := message.Kind
			if kind == "" {
				kind = "message"
			}
			eventType := transcriptEventType(kind)
			payload := map[string]any{"message_id": messageRef, "role": message.Role}
			if text != "" {
				payload["text"] = text
			}
			if message.ToolName != "" {
				payload["tool_name"] = message.ToolName
			}
			if message.ToolUseID != "" {
				payload["tool_use_id"] = message.ToolUseID
			}
			if message.ToolInput != "" {
				payload["tool_input"] = message.ToolInput
			}
			if message.ToolOutput != "" {
				payload["tool_output"] = message.ToolOutput
			}
			if message.IsError != nil {
				payload["is_error"] = *message.IsError
			}
			if message.ExitCode != nil {
				payload["exit_code"] = *message.ExitCode
			}
			if message.Truncated {
				payload["truncated"] = true
			}
			if message.AgentID != "" {
				payload["agent_id"] = message.AgentID
			}
			if message.Sidechain {
				payload["sidechain"] = true
			}
			if message.Usage != nil {
				payload["usage"] = message.Usage
			}
			index := messageIndex
			transcript := canonical.Event{
				SourceEventID: stableID(qualified, messageRef, "transcript", kind), SessionID: qualified,
				EventType: eventType, ObservationLevel: canonical.LevelUnknown, Payload: payload,
				Locator:    canonical.Locator{Source: string(a.Source()), SessionID: qualified, MessageIndex: &index, RawRef: messageRef + ":" + kind},
				OccurredAt: occurred, AdapterVersion: a.Version(),
			}
			if err := transcript.Validate(); err != nil {
				return adapters.SessionMeta{}, nil, err
			}
			events = append(events, transcript)
		}
		for invocationIndex, invocation := range message.AssetInvocations {
			occurred, err := parseTime(message.Timestamp)
			if err != nil {
				return adapters.SessionMeta{}, nil, fmt.Errorf("claudecode: message %s timestamp: %w", messageRef, err)
			}
			payload := map[string]any{"message_id": messageRef}
			if invocation.ContentHash != "" {
				payload["content_hash"] = invocation.ContentHash
			}
			if invocation.References != nil {
				payload["references"] = invocation.References
			}
			index := messageIndex
			participation := canonical.SignalInvoked
			if invocation.ParticipationSignal != nil {
				participation = *invocation.ParticipationSignal
			}
			level := canonical.LevelInvoked
			if invocation.ObservationLevel != nil {
				level = *invocation.ObservationLevel
			}
			event := canonical.Event{
				SourceEventID: stableID(qualified, messageRef, fmt.Sprintf("asset-%d", invocationIndex)), SessionID: qualified,
				EventType: canonical.EventTypeAssetInvoked, AssetID: invocation.AssetID, ParticipationSignal: signal(participation),
				ObservationLevel: level, Payload: payload,
				Locator: canonical.Locator{Source: string(a.Source()), SessionID: qualified, MessageIndex: &index, RawRef: messageRef}, OccurredAt: occurred, AdapterVersion: a.Version(),
			}
			if err := event.Validate(); err != nil {
				return adapters.SessionMeta{}, nil, err
			}
			events = append(events, event)
			if invocation.Followed != nil && *invocation.Followed {
				followed := outcomeEvent(qualified, messageRef, invocationIndex, invocation.AssetID, invocation.ContentHash, index, occurred, canonical.EventTypeAssetObservedUse, canonical.SignalFollowed, canonical.LevelObservedUse, a.Version(), map[string]any{"followed": true})
				if err := followed.Validate(); err != nil {
					return adapters.SessionMeta{}, nil, err
				}
				events = append(events, followed)
			}
			if invocation.Violated != nil && *invocation.Violated {
				violation := outcomeEvent(qualified, messageRef, invocationIndex, invocation.AssetID, invocation.ContentHash, index, occurred, canonical.EventTypeAssetViolation, "", canonical.LevelInvoked, a.Version(), map[string]any{"violated": true})
				if err := violation.Validate(); err != nil {
					return adapters.SessionMeta{}, nil, err
				}
				events = append(events, violation)
			}
		}
	}
	return meta, events, nil
}

func transcriptEventType(kind string) string {
	switch kind {
	case "tool_call":
		return canonical.EventTypeTranscriptToolCall
	case "tool_result":
		return canonical.EventTypeTranscriptResult
	default:
		return canonical.EventTypeTranscriptMessage
	}
}

func parseTime(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, err
	}
	parsed = parsed.UTC()
	return &parsed, nil
}
func signal(value canonical.ParticipationSignal) *canonical.ParticipationSignal { return &value }
func outcomeEvent(sessionID, messageRef string, invocationIndex int, assetID, contentHash string, index int, occurred *time.Time, eventType string, participation canonical.ParticipationSignal, level canonical.ObservationLevel, adapterVersion string, extra map[string]any) canonical.Event {
	payload := map[string]any{"message_id": messageRef}
	for key, value := range extra {
		payload[key] = value
	}
	if contentHash != "" {
		payload["content_hash"] = contentHash
	}
	var signalValue *canonical.ParticipationSignal
	if participation != "" {
		signalValue = signal(participation)
	}
	return canonical.Event{
		SourceEventID: stableID(sessionID, messageRef, fmt.Sprintf("asset-%d-%s", invocationIndex, strings.TrimPrefix(eventType, "asset_"))),
		SessionID:     sessionID, EventType: eventType, AssetID: assetID, ParticipationSignal: signalValue,
		ObservationLevel: level, Payload: payload, Locator: canonical.Locator{Source: string(adapters.SourceClaudeCode), SessionID: sessionID, MessageIndex: &index, RawRef: messageRef + ":" + strings.TrimPrefix(eventType, "asset_")}, OccurredAt: occurred, AdapterVersion: adapterVersion,
	}
}
func stableID(parts ...string) string {
	hash := sha256.Sum256([]byte(join(parts)))
	return "cc:" + hex.EncodeToString(hash[:12])
}
func join(parts []string) string {
	result := ""
	for i, part := range parts {
		if i > 0 {
			result += "\x00"
		}
		result += part
	}
	return result
}
