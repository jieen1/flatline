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
			if scenario == "missing_fields" && len(events) != 2 {
				t.Fatalf("missing fixture lost transcript/invocation events: %d", len(events))
			}
			if scenario != "missing_fields" && len(events) != 3 {
				t.Fatalf("events = %d, want session + transcript + invocation", len(events))
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

func TestParsePreservesExplicitOutcomeEvidence(t *testing.T) {
	raw := []byte(`{"session":{"id":"outcome","started_at":"2026-08-20T09:00:00Z","ended_at":"2026-08-20T09:03:00Z"},"turns":[{"id":"t1","timestamp":"2026-08-20T09:01:00Z","asset_invocations":[{"asset_id":"skill:fixture","followed":true,"violated":true}]}]}`)
	_, events, err := New().Parse(adapters.RawSession{Source: adapters.SourceCodex, RawJSON: raw})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 || events[2].ParticipationSignal == nil || *events[2].ParticipationSignal != canonical.SignalFollowed || events[3].EventType != canonical.EventTypeAssetViolation {
		t.Fatalf("events = %#v, want session/invocation/followed/violation", events)
	}
	if events[3].ObservationLevel != canonical.LevelInvoked || events[3].Payload["violated"] != true {
		t.Fatalf("violation event = %#v", events[3])
	}
}

func TestParsePreservesNonZeroExitCode(t *testing.T) {
	raw := []byte("{\"session\":{\"id\":\"tool-exit\",\"started_at\":\"2026-08-20T09:00:00Z\",\"ended_at\":\"2026-08-20T09:03:00Z\"},\"turns\":[{\"id\":\"result-1\",\"timestamp\":\"2026-08-20T09:01:00Z\",\"role\":\"tool\",\"kind\":\"tool_result\",\"tool_output\":\"Exit code 7\\\\npermission denied\",\"exit_code\":7}]}")
	_, events, err := New().Parse(adapters.RawSession{Source: adapters.SourceCodex, RawJSON: raw})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.EventType == canonical.EventTypeTranscriptResult && event.Payload["exit_code"] == 7 {
			return
		}
	}
	t.Fatalf("exit code evidence missing: %#v", events)
}
