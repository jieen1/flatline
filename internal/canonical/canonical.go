// Package canonical defines the frozen canonical event model for Flatline.
package canonical

import (
	"fmt"
	"time"
)

// ObservationLevel describes how participation was observed.
type ObservationLevel string

const (
	LevelInvoked     ObservationLevel = "invoked"
	LevelObservedUse ObservationLevel = "observed-use"
	LevelLoaded      ObservationLevel = "loaded"
	LevelOffered     ObservationLevel = "offered"
	LevelInferred    ObservationLevel = "inferred"
	LevelUnknown     ObservationLevel = "unknown"
)

func (l ObservationLevel) Valid() bool {
	switch l {
	case LevelInvoked, LevelObservedUse, LevelLoaded, LevelOffered, LevelInferred, LevelUnknown:
		return true
	default:
		return false
	}
}

func AllObservationLevels() []ObservationLevel {
	return []ObservationLevel{LevelInvoked, LevelObservedUse, LevelLoaded, LevelOffered, LevelInferred, LevelUnknown}
}

// ParticipationSignal describes what happened, independently of observation level.
type ParticipationSignal string

const (
	SignalOffered     ParticipationSignal = "offered"
	SignalLoaded      ParticipationSignal = "loaded"
	SignalInvoked     ParticipationSignal = "invoked"
	SignalObservedUse ParticipationSignal = "observed-use"
	SignalFollowed    ParticipationSignal = "followed"
)

func (s ParticipationSignal) Valid() bool {
	switch s {
	case SignalOffered, SignalLoaded, SignalInvoked, SignalObservedUse, SignalFollowed:
		return true
	default:
		return false
	}
}

func AllParticipationSignals() []ParticipationSignal {
	return []ParticipationSignal{SignalOffered, SignalLoaded, SignalInvoked, SignalObservedUse, SignalFollowed}
}

// Locator identifies the source position for an event. Nil positions mean the
// source did not record that coordinate; they are never replaced with zero.
type Locator struct {
	Source       string `json:"source"`
	SessionID    string `json:"session_id"`
	MessageIndex *int   `json:"message_index,omitempty"`
	Line         *int   `json:"line,omitempty"`
	ByteOffset   *int   `json:"byte_offset,omitempty"`
	RawRef       string `json:"raw_ref,omitempty"`
}

func (l Locator) Valid() bool {
	if l.Source == "" || l.SessionID == "" {
		return false
	}
	return l.RawRef != "" || l.MessageIndex != nil || l.Line != nil || l.ByteOffset != nil
}

// Event is one append-only canonical fact. SourceEventID is required so
// adapter-produced events are idempotent on repeated ingestion.
type Event struct {
	SourceEventID       string
	SessionID           string
	EventType           string
	AssetID             string
	AssetVersionID      *int64
	ParticipationSignal *ParticipationSignal
	ObservationLevel    ObservationLevel
	Payload             map[string]any
	Locator             Locator
	OccurredAt          *time.Time
	AdapterVersion      string
}

const (
	EventTypeSessionStarted     = "session_started"
	EventTypeTranscriptMessage  = "transcript_message"
	EventTypeTranscriptToolCall = "transcript_tool_call"
	EventTypeTranscriptResult   = "transcript_tool_result"
	EventTypeAssetInvoked       = "asset_invoked"
	EventTypeAssetLoaded        = "asset_loaded"
	EventTypeAssetOffered       = "asset_offered"
	EventTypeAssetObservedUse   = "asset_observed_use"
	EventTypeAssetViolation     = "asset_violation"
	EventTypeEnvironmentChanged = "environment_changed"
)

func (e Event) Validate() error {
	if e.SourceEventID == "" {
		return fmt.Errorf("canonical: source_event_id is required for idempotent ingestion")
	}
	if !e.ObservationLevel.Valid() {
		return fmt.Errorf("canonical: invalid observation level %q", e.ObservationLevel)
	}
	if e.ParticipationSignal != nil && !e.ParticipationSignal.Valid() {
		return fmt.Errorf("canonical: invalid participation signal %q", *e.ParticipationSignal)
	}
	if e.SessionID == "" {
		return fmt.Errorf("canonical: event has empty session_id")
	}
	if e.EventType == "" {
		return fmt.Errorf("canonical: event has empty event_type")
	}
	if !e.Locator.Valid() {
		return fmt.Errorf("canonical: locator requires source, session_id, and a concrete source position")
	}
	if e.OccurredAt != nil && e.OccurredAt.Location() != time.UTC {
		return fmt.Errorf("canonical: occurred_at must be UTC")
	}
	return nil
}
