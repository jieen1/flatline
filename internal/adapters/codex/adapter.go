// Package codex maps synthetic Codex session records to canonical facts.
package codex

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

const adapterVersion = "codex/1"

type Adapter struct{}

func New() Adapter                      { return Adapter{} }
func (Adapter) Source() adapters.Source { return adapters.SourceCodex }
func (Adapter) Version() string         { return adapterVersion }
func (Adapter) FieldMatrix() adapters.FieldMatrix {
	return adapters.FieldMatrix{
		Supported:   []string{"source_session_id", "started_at", "ended_at", "harness_version", "model", "cwd", "asset_invocation", "asset_content_hash", "reference_inputs", "followed_signal", "violated_signal", "observation_level", "locator"},
		Unsupported: []string{"raw_source_bytes"},
		Unrecorded:  []string{"loaded_signal", "offered_signal"},
	}
}

type fixture struct {
	Session session `json:"session"`
	Turns   []turn  `json:"turns"`
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
}
type turn struct {
	ID               string       `json:"id"`
	Timestamp        string       `json:"timestamp"`
	Role             string       `json:"role"`
	Kind             string       `json:"kind"`
	Text             string       `json:"text"`
	ToolName         string       `json:"tool_name"`
	ToolInput        string       `json:"tool_input"`
	ToolOutput       string       `json:"tool_output"`
	Truncated        bool         `json:"truncated"`
	IsError          *bool        `json:"is_error"`
	ExitCode         *int         `json:"exit_code"`
	AssetInvocations []invocation `json:"asset_invocations"`
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
		return adapters.VersionInfo{}, fmt.Errorf("codex: decode session: %w", err)
	}
	return adapters.VersionInfo{HarnessVersion: input.Session.HarnessVersion, Model: input.Session.Model, Raw: input.Session.HarnessVersion}, nil
}

func (a Adapter) Parse(raw adapters.RawSession) (adapters.SessionMeta, []canonical.Event, error) {
	if raw.Source != a.Source() {
		return adapters.SessionMeta{}, nil, fmt.Errorf("codex: source is %q", raw.Source)
	}
	var input fixture
	if err := json.Unmarshal(raw.RawJSON, &input); err != nil {
		return adapters.SessionMeta{}, nil, fmt.Errorf("codex: decode session: %w", err)
	}
	id := input.Session.ID
	if id == "" {
		id = raw.SessionID
	}
	if id == "" {
		return adapters.SessionMeta{}, nil, fmt.Errorf("codex: session id is missing")
	}
	started, err := parseTime(input.Session.StartedAt)
	if err != nil {
		return adapters.SessionMeta{}, nil, fmt.Errorf("codex: started_at: %w", err)
	}
	ended, err := parseTime(input.Session.EndedAt)
	if err != nil {
		return adapters.SessionMeta{}, nil, fmt.Errorf("codex: ended_at: %w", err)
	}
	qualified := string(a.Source()) + ":" + id
	meta := adapters.SessionMeta{SourceSessionID: id, StartedAt: started, EndedAt: ended, HarnessVersion: input.Session.HarnessVersion, Model: input.Session.Model, CWD: input.Session.CWD, Title: input.Session.Title, TaskText: input.Session.TaskText}
	events := []canonical.Event{{SourceEventID: stableID(qualified, "session"), SessionID: qualified, EventType: canonical.EventTypeSessionStarted, ObservationLevel: canonical.LevelUnknown, Payload: map[string]any{"source_session_id": id}, Locator: canonical.Locator{Source: string(a.Source()), SessionID: qualified, RawRef: "session"}, OccurredAt: started, AdapterVersion: a.Version()}}
	for turnIndex, turn := range input.Turns {
		turnRef := turn.ID
		if turnRef == "" {
			turnRef = fmt.Sprintf("turn-%d", turnIndex)
		}
		if turn.Kind != "" || turn.Text != "" || turn.ToolName != "" || turn.ToolInput != "" || turn.ToolOutput != "" || turn.IsError != nil || turn.ExitCode != nil {
			occurred, err := parseTime(turn.Timestamp)
			if err != nil {
				return adapters.SessionMeta{}, nil, fmt.Errorf("codex: turn %s timestamp: %w", turnRef, err)
			}
			kind := turn.Kind
			if kind == "" {
				kind = "message"
			}
			eventType := transcriptEventType(kind)
			payload := map[string]any{"turn_id": turnRef, "role": turn.Role}
			if turn.Text != "" {
				payload["text"] = turn.Text
			}
			if turn.ToolName != "" {
				payload["tool_name"] = turn.ToolName
			}
			if turn.ToolInput != "" {
				payload["tool_input"] = turn.ToolInput
			}
			if turn.ToolOutput != "" {
				payload["tool_output"] = turn.ToolOutput
			}
			if turn.IsError != nil {
				payload["is_error"] = *turn.IsError
			}
			if turn.ExitCode != nil {
				payload["exit_code"] = *turn.ExitCode
			}
			if turn.Truncated {
				payload["truncated"] = true
			}
			index := turnIndex
			transcript := canonical.Event{
				SourceEventID: stableID(qualified, turnRef, "transcript", kind), SessionID: qualified,
				EventType: eventType, ObservationLevel: canonical.LevelUnknown, Payload: payload,
				Locator:    canonical.Locator{Source: string(a.Source()), SessionID: qualified, MessageIndex: &index, RawRef: turnRef + ":" + kind},
				OccurredAt: occurred, AdapterVersion: a.Version(),
			}
			if err := transcript.Validate(); err != nil {
				return adapters.SessionMeta{}, nil, err
			}
			events = append(events, transcript)
		}
		for invocationIndex, invocation := range turn.AssetInvocations {
			occurred, err := parseTime(turn.Timestamp)
			if err != nil {
				return adapters.SessionMeta{}, nil, fmt.Errorf("codex: turn %s timestamp: %w", turnRef, err)
			}
			payload := map[string]any{"turn_id": turnRef}
			if invocation.ContentHash != "" {
				payload["content_hash"] = invocation.ContentHash
			}
			if invocation.References != nil {
				payload["references"] = invocation.References
			}
			index := turnIndex
			participation := canonical.SignalInvoked
			if invocation.ParticipationSignal != nil {
				participation = *invocation.ParticipationSignal
			}
			level := canonical.LevelInvoked
			if invocation.ObservationLevel != nil {
				level = *invocation.ObservationLevel
			}
			event := canonical.Event{SourceEventID: stableID(qualified, turnRef, fmt.Sprintf("asset-%d", invocationIndex)), SessionID: qualified, EventType: canonical.EventTypeAssetInvoked, AssetID: invocation.AssetID, ParticipationSignal: signal(participation), ObservationLevel: level, Payload: payload, Locator: canonical.Locator{Source: string(a.Source()), SessionID: qualified, MessageIndex: &index, RawRef: turnRef}, OccurredAt: occurred, AdapterVersion: a.Version()}
			if err := event.Validate(); err != nil {
				return adapters.SessionMeta{}, nil, err
			}
			events = append(events, event)
			if invocation.Followed != nil && *invocation.Followed {
				followed := outcomeEvent(qualified, turnRef, invocationIndex, invocation.AssetID, invocation.ContentHash, index, occurred, canonical.EventTypeAssetObservedUse, canonical.SignalFollowed, canonical.LevelObservedUse, a.Version(), map[string]any{"followed": true})
				if err := followed.Validate(); err != nil {
					return adapters.SessionMeta{}, nil, err
				}
				events = append(events, followed)
			}
			if invocation.Violated != nil && *invocation.Violated {
				violation := outcomeEvent(qualified, turnRef, invocationIndex, invocation.AssetID, invocation.ContentHash, index, occurred, canonical.EventTypeAssetViolation, "", canonical.LevelInvoked, a.Version(), map[string]any{"violated": true})
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
func outcomeEvent(sessionID, turnRef string, invocationIndex int, assetID, contentHash string, index int, occurred *time.Time, eventType string, participation canonical.ParticipationSignal, level canonical.ObservationLevel, adapterVersion string, extra map[string]any) canonical.Event {
	payload := map[string]any{"turn_id": turnRef}
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
		SourceEventID: stableID(sessionID, turnRef, fmt.Sprintf("asset-%d-%s", invocationIndex, strings.TrimPrefix(eventType, "asset_"))),
		SessionID:     sessionID, EventType: eventType, AssetID: assetID, ParticipationSignal: signalValue,
		ObservationLevel: level, Payload: payload, Locator: canonical.Locator{Source: string(adapters.SourceCodex), SessionID: sessionID, MessageIndex: &index, RawRef: turnRef + ":" + strings.TrimPrefix(eventType, "asset_")}, OccurredAt: occurred, AdapterVersion: adapterVersion,
	}
}
func stableID(parts ...string) string {
	hash := sha256.Sum256([]byte(join(parts)))
	return "codex:" + hex.EncodeToString(hash[:12])
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
