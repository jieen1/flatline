package normalized_test

import (
	"os"
	"path/filepath"
	"testing"

	"flatline/internal/adapters"
	"flatline/internal/adapters/dsh"
	"flatline/internal/adapters/opencode"
	"flatline/internal/canonical"
)

func loadFixture(t *testing.T, dir, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", dir, name+".json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

type replayAdapter interface {
	Source() adapters.Source
	Version() string
	DetectVersion(adapters.RawSession) (adapters.VersionInfo, error)
	Parse(adapters.RawSession) (adapters.SessionMeta, []canonical.Event, error)
	FieldMatrix() adapters.FieldMatrix
}

func TestAdaptersReplayNormalizedFixtures(t *testing.T) {
	cases := []struct {
		name    string
		dir     string
		adapter replayAdapter
		source  adapters.Source
		version string
		parent  string
		thread  string
	}{
		{"opencode", "opencode", opencode.New(), adapters.SourceOpenCode, "opencode/1", "opencode:ses_parent", "subagent"},
		{"dsh", "dsh", dsh.New(), adapters.SourceDSH, "dsh/1", "", "main"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			raw := adapters.RawSession{Source: testCase.source, RawJSON: loadFixture(t, testCase.dir, "normal")}
			meta, events, err := testCase.adapter.Parse(raw)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if testCase.adapter.Version() != testCase.version {
				t.Fatalf("version = %q", testCase.adapter.Version())
			}
			if meta.ThreadKind != testCase.thread || meta.ParentSessionID != testCase.parent {
				t.Fatalf("hierarchy = %q / %q", meta.ThreadKind, meta.ParentSessionID)
			}
			if meta.CWD != "/home/dev/demo" || meta.Model != "demo-model" {
				t.Fatalf("meta = %#v", meta)
			}
			if err := testCase.adapter.FieldMatrix().Validate(); err != nil {
				t.Fatalf("field matrix: %v", err)
			}

			byType := map[string]int{}
			ids := map[string]bool{}
			pairing := map[string][]string{}
			for _, event := range events {
				if err := event.Validate(); err != nil {
					t.Fatalf("event %s: %v", event.SourceEventID, err)
				}
				if ids[event.SourceEventID] {
					t.Fatalf("duplicate source_event_id %s makes ingestion non-idempotent", event.SourceEventID)
				}
				ids[event.SourceEventID] = true
				byType[event.EventType]++
				if event.Locator.Source != string(testCase.source) {
					t.Fatalf("locator source = %q", event.Locator.Source)
				}
				if event.AdapterVersion != testCase.version {
					t.Fatalf("adapter version = %q", event.AdapterVersion)
				}
				switch event.EventType {
				case canonical.EventTypeTranscriptToolCall, canonical.EventTypeTranscriptResult:
					callID, ok := event.Payload["call_id"].(string)
					if !ok || callID == "" {
						t.Fatalf("%s has no call_id: §13 pairing needs one", event.EventType)
					}
					pairing[callID] = append(pairing[callID], event.EventType)
				}
			}
			if byType[canonical.EventTypeSessionStarted] != 1 {
				t.Fatalf("session_started = %d", byType[canonical.EventTypeSessionStarted])
			}
			if byType[canonical.EventTypeTranscriptToolCall] != 2 || byType[canonical.EventTypeTranscriptResult] != 2 {
				t.Fatalf("tool events = %d / %d", byType[canonical.EventTypeTranscriptToolCall], byType[canonical.EventTypeTranscriptResult])
			}
			for callID, kinds := range pairing {
				if len(kinds) != 2 {
					t.Fatalf("call %s has %v, want one call and one result", callID, kinds)
				}
			}
		})
	}
}

// The session store has no usage column of its own on the event path, so the
// session_started payload is where a source's own numbers stay drillable.
func TestSessionStartedCarriesUsage(t *testing.T) {
	raw := adapters.RawSession{Source: adapters.SourceOpenCode, RawJSON: loadFixture(t, "opencode", "normal")}
	_, events, err := opencode.New().Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	usage, ok := events[0].Payload["usage"].(map[string]any)
	if !ok {
		t.Fatalf("session_started payload = %#v", events[0].Payload)
	}
	if usage["source"] != "opencode_session" {
		t.Fatalf("usage source = %v", usage["source"])
	}
	if usage["total_tokens"] != float64(1750) {
		t.Fatalf("total tokens = %v", usage["total_tokens"])
	}
	if usage["cost"] != 0.25 {
		t.Fatalf("cost = %v", usage["cost"])
	}
	// A field the source did not record must stay null, not become zero.
	if value, present := usage["active_ms"]; !present || value != nil {
		t.Fatalf("active_ms = %v (present=%v), want null", value, present)
	}
}

func TestOutcomeFieldsSurviveTheAdapter(t *testing.T) {
	raw := adapters.RawSession{Source: adapters.SourceDSH, RawJSON: loadFixture(t, "dsh", "normal")}
	_, events, err := dsh.New().Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	results := map[string]map[string]any{}
	aborts := 0
	for _, event := range events {
		if event.EventType == canonical.EventTypeTranscriptResult {
			results[event.Payload["call_id"].(string)] = event.Payload
		}
		if _, ok := event.Payload["abort_reason"]; ok {
			aborts++
		}
	}
	if results["call_ok"]["is_error"] != false {
		t.Fatalf("call_ok is_error = %v", results["call_ok"]["is_error"])
	}
	if results["call_bad"]["is_error"] != true {
		t.Fatalf("call_bad is_error = %v", results["call_bad"]["is_error"])
	}
	// dsh records no exit status, so the key must be absent rather than 0.
	if _, present := results["call_bad"]["exit_code"]; present {
		t.Fatal("exit_code invented for a source that does not record one")
	}
	if aborts != 1 {
		t.Fatalf("abort records = %d, want 1", aborts)
	}
}

func TestParseRejectsMismatchedSource(t *testing.T) {
	raw := adapters.RawSession{Source: adapters.SourceCodex, RawJSON: loadFixture(t, "dsh", "normal")}
	if _, _, err := dsh.New().Parse(raw); err == nil {
		t.Fatal("parsing a fixture under the wrong source should fail")
	}
}

func TestDetectVersionReadsHarnessVersion(t *testing.T) {
	raw := adapters.RawSession{Source: adapters.SourceOpenCode, RawJSON: loadFixture(t, "opencode", "normal")}
	info, err := opencode.New().DetectVersion(raw)
	if err != nil {
		t.Fatalf("DetectVersion: %v", err)
	}
	if info.HarnessVersion != "1.18.18" || info.Model != "demo-model" {
		t.Fatalf("version info = %#v", info)
	}
	// dsh records no harness version; reporting one would be an invention.
	dshInfo, err := dsh.New().DetectVersion(adapters.RawSession{Source: adapters.SourceDSH, RawJSON: loadFixture(t, "dsh", "normal")})
	if err != nil {
		t.Fatalf("DetectVersion dsh: %v", err)
	}
	if dshInfo.HarnessVersion != "" {
		t.Fatalf("dsh harness version = %q, want unrecorded", dshInfo.HarnessVersion)
	}
}
