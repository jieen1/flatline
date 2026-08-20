// Package codex maps synthetic Codex session records to canonical facts.
package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
		Supported:   []string{"source_session_id", "started_at", "ended_at", "harness_version", "model", "cwd", "asset_invocation", "asset_content_hash", "reference_inputs", "observation_level", "locator"},
		Unsupported: []string{"followed_signal", "raw_source_bytes"},
		Unrecorded:  []string{"loaded_signal", "offered_signal"},
	}
}

type fixture struct {
	Session session `json:"session"`
	Turns   []turn  `json:"turns"`
}
type session struct {
	ID             string `json:"id"`
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
	Text             string       `json:"text"`
	AssetInvocations []invocation `json:"asset_invocations"`
}
type invocation struct {
	AssetID     string   `json:"asset_id"`
	ContentHash string   `json:"content_hash"`
	References  []string `json:"references"`
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
	meta := adapters.SessionMeta{SourceSessionID: id, StartedAt: started, EndedAt: ended, HarnessVersion: input.Session.HarnessVersion, Model: input.Session.Model, CWD: input.Session.CWD}
	events := []canonical.Event{{SourceEventID: stableID(qualified, "session"), SessionID: qualified, EventType: canonical.EventTypeSessionStarted, ObservationLevel: canonical.LevelUnknown, Payload: map[string]any{"source_session_id": id}, Locator: canonical.Locator{Source: string(a.Source()), SessionID: qualified, RawRef: "session"}, OccurredAt: started, AdapterVersion: a.Version()}}
	for turnIndex, turn := range input.Turns {
		for invocationIndex, invocation := range turn.AssetInvocations {
			turnRef := turn.ID
			if turnRef == "" {
				turnRef = fmt.Sprintf("turn-%d", turnIndex)
			}
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
			event := canonical.Event{SourceEventID: stableID(qualified, turnRef, fmt.Sprintf("asset-%d", invocationIndex)), SessionID: qualified, EventType: canonical.EventTypeAssetInvoked, AssetID: invocation.AssetID, ParticipationSignal: signal(canonical.SignalInvoked), ObservationLevel: canonical.LevelInvoked, Payload: payload, Locator: canonical.Locator{Source: string(a.Source()), SessionID: qualified, MessageIndex: &index, RawRef: turnRef}, OccurredAt: occurred, AdapterVersion: a.Version()}
			if err := event.Validate(); err != nil {
				return adapters.SessionMeta{}, nil, err
			}
			events = append(events, event)
		}
	}
	return meta, events, nil
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
