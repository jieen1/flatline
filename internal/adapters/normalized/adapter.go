// Package normalized turns the reader-produced normalized session JSON into
// canonical events. Every source whose reader emits that shape shares this
// one mapping, so a new harness needs a reader and a registration, not another
// copy of the event-building rules.
package normalized

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

// Config is what distinguishes one source's adapter from another.
type Config struct {
	Source  adapters.Source
	Version string
	Matrix  adapters.FieldMatrix
}

type Adapter struct{ config Config }

func New(config Config) Adapter           { return Adapter{config: config} }
func (a Adapter) Source() adapters.Source { return a.config.Source }
func (a Adapter) Version() string         { return a.config.Version }
func (a Adapter) FieldMatrix() adapters.FieldMatrix {
	return a.config.Matrix
}

type document struct {
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

	Usage map[string]any `json:"usage"`
}

type message struct {
	ID               string       `json:"id"`
	Timestamp        string       `json:"timestamp"`
	Role             string       `json:"role"`
	Kind             string       `json:"kind"`
	Text             string       `json:"text"`
	ToolName         string       `json:"tool_name"`
	CallID           string       `json:"call_id"`
	ToolInput        string       `json:"tool_input"`
	ToolOutput       string       `json:"tool_output"`
	Truncated        bool         `json:"truncated"`
	IsError          *bool        `json:"is_error"`
	ExitCode         *int         `json:"exit_code"`
	AgentID          string       `json:"agent_id"`
	Sidechain        bool         `json:"sidechain"`
	AbortReason      string       `json:"abort_reason"`
	TurnTokens       *int64       `json:"turn_tokens"`
	Usage            *usageRecord `json:"usage"`
	AssetInvocations []invocation `json:"asset_invocations"`
}

type invocation struct {
	AssetID             string                         `json:"asset_id"`
	ContentHash         string                         `json:"content_hash"`
	References          []string                       `json:"references"`
	ParticipationSignal *canonical.ParticipationSignal `json:"participation_signal"`
	ObservationLevel    *canonical.ObservationLevel    `json:"observation_level"`
}

func (a Adapter) DetectVersion(raw adapters.RawSession) (adapters.VersionInfo, error) {
	var input document
	if err := json.Unmarshal(raw.RawJSON, &input); err != nil {
		return adapters.VersionInfo{}, fmt.Errorf("%s: decode session: %w", a.config.Source, err)
	}
	return adapters.VersionInfo{
		HarnessVersion: input.Session.HarnessVersion,
		Model:          input.Session.Model,
		Raw:            input.Session.HarnessVersion,
	}, nil
}

func (a Adapter) Parse(raw adapters.RawSession) (adapters.SessionMeta, []canonical.Event, error) {
	if raw.Source != a.config.Source {
		return adapters.SessionMeta{}, nil, fmt.Errorf("%s: source is %q", a.config.Source, raw.Source)
	}
	var input document
	if err := json.Unmarshal(raw.RawJSON, &input); err != nil {
		return adapters.SessionMeta{}, nil, fmt.Errorf("%s: decode session: %w", a.config.Source, err)
	}
	id := input.Session.ID
	if id == "" {
		id = raw.SessionID
	}
	if id == "" {
		return adapters.SessionMeta{}, nil, fmt.Errorf("%s: session id is missing", a.config.Source)
	}
	started, err := parseTime(input.Session.StartedAt)
	if err != nil {
		return adapters.SessionMeta{}, nil, fmt.Errorf("%s: started_at: %w", a.config.Source, err)
	}
	ended, err := parseTime(input.Session.EndedAt)
	if err != nil {
		return adapters.SessionMeta{}, nil, fmt.Errorf("%s: ended_at: %w", a.config.Source, err)
	}
	qualified := string(a.config.Source) + ":" + id
	meta := adapters.SessionMeta{
		SourceSessionID: id, StartedAt: started, EndedAt: ended,
		HarnessVersion: input.Session.HarnessVersion, Model: input.Session.Model,
		CWD: input.Session.CWD, Title: input.Session.Title, TaskText: input.Session.TaskText,
		ThreadKind: input.Session.ThreadKind, ParentSessionID: input.Session.ParentSessionID,
		AgentRole: input.Session.AgentRole, AgentNickname: input.Session.AgentNickname,
		Originator: input.Session.Originator,
	}

	// The session_started payload carries usage because the session store has
	// no usage column yet (§15 is A4's migration 011). Keeping it on the event
	// means the source's own numbers are recorded now and stay drillable,
	// instead of being recomputed later from bounded payloads.
	startPayload := map[string]any{"source_session_id": id}
	if len(input.Session.Usage) > 0 {
		startPayload["usage"] = input.Session.Usage
	}
	events := []canonical.Event{{
		SourceEventID: a.stableID(qualified, "session"), SessionID: qualified,
		EventType: canonical.EventTypeSessionStarted, ObservationLevel: canonical.LevelUnknown,
		Payload:    startPayload,
		Locator:    canonical.Locator{Source: string(a.config.Source), SessionID: qualified, RawRef: "session"},
		OccurredAt: started, AdapterVersion: a.Version(),
	}}

	for index, item := range input.Messages {
		ref := item.ID
		if ref == "" {
			ref = fmt.Sprintf("message-%d", index)
		}
		occurred, err := parseTime(item.Timestamp)
		if err != nil {
			return adapters.SessionMeta{}, nil, fmt.Errorf("%s: message %s timestamp: %w", a.config.Source, ref, err)
		}
		if hasContent(item) {
			kind := item.Kind
			if kind == "" {
				kind = "message"
			}
			position := index
			event := canonical.Event{
				SourceEventID: a.stableID(qualified, ref, "transcript", kind), SessionID: qualified,
				EventType: transcriptEventType(kind), ObservationLevel: canonical.LevelUnknown,
				Payload: transcriptPayload(ref, item),
				Locator: canonical.Locator{Source: string(a.config.Source), SessionID: qualified,
					MessageIndex: &position, RawRef: ref + ":" + kind},
				OccurredAt: occurred, AdapterVersion: a.Version(),
			}
			if err := event.Validate(); err != nil {
				return adapters.SessionMeta{}, nil, err
			}
			events = append(events, event)
		}
		for order, item := range item.AssetInvocations {
			position := index
			participation := canonical.SignalInvoked
			if item.ParticipationSignal != nil {
				participation = *item.ParticipationSignal
			}
			level := canonical.LevelInvoked
			if item.ObservationLevel != nil {
				level = *item.ObservationLevel
			}
			payload := map[string]any{"turn_id": ref}
			if item.ContentHash != "" {
				payload["content_hash"] = item.ContentHash
			}
			if item.References != nil {
				payload["references"] = item.References
			}
			event := canonical.Event{
				SourceEventID: a.stableID(qualified, ref, fmt.Sprintf("asset-%d", order)), SessionID: qualified,
				EventType: canonical.EventTypeAssetInvoked, AssetID: item.AssetID,
				ParticipationSignal: &participation, ObservationLevel: level, Payload: payload,
				Locator: canonical.Locator{Source: string(a.config.Source), SessionID: qualified,
					MessageIndex: &position, RawRef: ref},
				OccurredAt: occurred, AdapterVersion: a.Version(),
			}
			if err := event.Validate(); err != nil {
				return adapters.SessionMeta{}, nil, err
			}
			events = append(events, event)
		}
	}
	return meta, events, nil
}

// hasContent keeps a record out of the transcript when the source recorded
// nothing about it. An empty record is not an empty message; it is no message.
func hasContent(item message) bool {
	return item.Kind != "" || item.Text != "" || item.ToolName != "" || item.CallID != "" ||
		item.ToolInput != "" || item.ToolOutput != "" || item.IsError != nil ||
		item.ExitCode != nil || item.AbortReason != ""
}

func transcriptPayload(ref string, item message) map[string]any {
	payload := map[string]any{"turn_id": ref, "role": item.Role}
	for key, value := range map[string]string{
		"text": item.Text, "tool_name": item.ToolName, "call_id": item.CallID,
		"tool_input": item.ToolInput, "tool_output": item.ToolOutput,
		"agent_id": item.AgentID, "abort_reason": item.AbortReason,
	} {
		if value != "" {
			payload[key] = value
		}
	}
	if item.IsError != nil {
		payload["is_error"] = *item.IsError
	}
	if item.ExitCode != nil {
		payload["exit_code"] = *item.ExitCode
	}
	if item.Truncated {
		payload["truncated"] = true
	}
	if item.Sidechain {
		payload["sidechain"] = true
	}
	if item.Usage != nil {
		payload["usage"] = item.Usage
	}
	if item.TurnTokens != nil {
		payload["turn_tokens"] = *item.TurnTokens
	}
	return payload
}

// usageRecord is the per-message token record a source attached to one
// assistant message. It passes through untouched: the numbers are the source's
// own, and the session total is recomputed from them elsewhere.
type usageRecord struct {
	Input       int64 `json:"input_tokens"`
	CachedInput int64 `json:"cached_input_tokens"`
	CacheWrite  int64 `json:"cache_write_tokens"`
	Output      int64 `json:"output_tokens"`
	Reasoning   int64 `json:"reasoning_tokens"`
	Total       int64 `json:"total_tokens"`
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

func (a Adapter) stableID(parts ...string) string {
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return string(a.config.Source) + ":" + hex.EncodeToString(hash[:12])
}
