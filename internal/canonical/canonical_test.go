package canonical

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestObservationLevelClosedSet(t *testing.T) {
	for _, level := range AllObservationLevels() {
		if !level.Valid() {
			t.Errorf("%q is not valid", level)
		}
	}
	for _, level := range []ObservationLevel{"", "exact", "followed", "INVoked"} {
		if level.Valid() {
			t.Errorf("%q unexpectedly valid", level)
		}
	}
}

func TestParticipationSignalIsOrthogonal(t *testing.T) {
	if !SignalFollowed.Valid() {
		t.Fatal("followed should be a participation signal")
	}
	if ObservationLevel(SignalFollowed).Valid() {
		t.Fatal("followed must not be an observation level")
	}
}

func TestEventValidation(t *testing.T) {
	line := 0
	occurred := time.Date(2026, 8, 20, 1, 2, 3, 0, time.UTC)
	valid := Event{
		SourceEventID:    "evt-1",
		SessionID:        "claude_code:s1",
		EventType:        EventTypeAssetInvoked,
		ObservationLevel: LevelInvoked,
		Locator:          Locator{Source: "claude_code", SessionID: "claude_code:s1", Line: &line},
		OccurredAt:       &occurred,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid event: %v", err)
	}
	cases := []struct {
		name string
		edit func(*Event)
	}{
		{"missing source id", func(e *Event) { e.SourceEventID = "" }},
		{"bad level", func(e *Event) { e.ObservationLevel = "exact" }},
		{"bad signal", func(e *Event) { s := ParticipationSignal("exact"); e.ParticipationSignal = &s }},
		{"missing locator position", func(e *Event) { e.Locator = Locator{Source: "claude_code", SessionID: e.SessionID} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := valid
			tc.edit(&e)
			if err := e.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestMissingCoordinatesAreNotZero(t *testing.T) {
	data, err := json.Marshal(Locator{Source: "codex", SessionID: "codex:s1"})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, field := range []string{"message_index", "line", "byte_offset"} {
		if strings.Contains(text, field) {
			t.Errorf("missing field %q was serialized: %s", field, text)
		}
	}
	if strings.Contains(text, ":0") {
		t.Errorf("missing coordinate was serialized as zero: %s", text)
	}
}

func TestUTCValidation(t *testing.T) {
	local := time.Date(2026, 8, 20, 1, 2, 3, 0, time.FixedZone("test", 3600))
	e := Event{
		SourceEventID:    "evt-1",
		SessionID:        "codex:s1",
		EventType:        EventTypeSessionStarted,
		ObservationLevel: LevelInferred,
		Locator:          Locator{Source: "codex", SessionID: "codex:s1", RawRef: "session"},
		OccurredAt:       &local,
	}
	if err := e.Validate(); err == nil {
		t.Fatal("non-UTC event should be rejected")
	}
}
