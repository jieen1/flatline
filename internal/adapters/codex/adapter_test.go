package codex

import (
	"os"
	"path/filepath"
	"testing"

	"flatline/internal/adapters"
	"flatline/internal/canonical"
)

func fixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "codex", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseFixtures(t *testing.T) {
	for _, scenario := range []string{"normal", "version_change", "missing_fields"} {
		t.Run(scenario, func(t *testing.T) {
			adapter := New()
			meta, events, err := adapter.Parse(adapters.RawSession{Source: adapters.SourceCodex, RawJSON: fixtureBytes(t, scenario)})
			if err != nil {
				t.Fatal(err)
			}
			if meta.SourceSessionID == "" || len(events) == 0 {
				t.Fatalf("meta/events = %#v/%d", meta, len(events))
			}
			if events[0].EventType != canonical.EventTypeSessionStarted || events[0].ObservationLevel != canonical.LevelUnknown {
				t.Fatalf("session event = %#v", events[0])
			}
			for _, event := range events {
				if err := event.Validate(); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "missing_fields" && len(events) != 1 {
				t.Fatalf("missing fixture fabricated invocation: %d events", len(events))
			}
			if scenario != "missing_fields" && len(events) != 2 {
				t.Fatalf("events = %d, want session + invocation", len(events))
			}
		})
	}
}

func TestParseIsDeterministic(t *testing.T) {
	adapter := New()
	raw := adapters.RawSession{Source: adapters.SourceCodex, RawJSON: fixtureBytes(t, "normal")}
	_, first, err := adapter.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := adapter.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if first[1].SourceEventID != second[1].SourceEventID || first[1].Locator.RawRef != second[1].Locator.RawRef {
		t.Fatal("parse output is not deterministic")
	}
}

func TestDetectVersion(t *testing.T) {
	adapter := New()
	version, err := adapter.DetectVersion(adapters.RawSession{Source: adapters.SourceCodex, RawJSON: fixtureBytes(t, "normal")})
	if err != nil {
		t.Fatal(err)
	}
	if version.HarnessVersion != "0.42.0" || version.Model == "" {
		t.Fatalf("version = %#v", version)
	}
	if _, err := adapter.DetectVersion(adapters.RawSession{Source: adapters.SourceCodex, RawJSON: []byte("{")}); err == nil {
		t.Fatal("malformed JSON should fail")
	}
}

func TestFieldMatrix(t *testing.T) {
	if err := New().FieldMatrix().Validate(); err != nil {
		t.Fatal(err)
	}
}
