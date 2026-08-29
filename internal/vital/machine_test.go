package vital

import (
	"strings"
	"testing"
	"time"

	"flatline/internal/detectors"
)

func vitalTime(day int) time.Time {
	return time.Date(2026, 8, day, 12, 0, 0, 0, time.UTC)
}

func noVerdict(detector string) detectors.Verdict {
	return detectors.Verdict{Detector: detector, Observable: true, Summary: "synthetic fixture: no trigger"}
}

func silentVerdict(triggered bool) detectors.Verdict {
	return detectors.Verdict{Detector: detectors.SilentDetector, Triggered: triggered, Observable: true, Rule: "synthetic silent rule", Summary: "synthetic fixture silent verdict"}
}

func referenceVerdict(triggered bool) detectors.ReferenceVerdict {
	return detectors.ReferenceVerdict{Verdict: detectors.Verdict{Detector: detectors.ReferenceDetector, Triggered: triggered, Observable: true, Rule: "synthetic reference rule", Summary: "synthetic fixture reference verdict"}}
}

func assess(previous State) Assessment {
	return Assessment{
		AssetID:               "skill:project:fixture",
		At:                    vitalTime(20),
		PreviousState:         previous,
		HasOpportunity:        true,
		HasBaseline:           true,
		ParticipationObserved: true,
		Silent:                noVerdict(detectors.SilentDetector),
		Degraded:              noVerdict(detectors.DegradedDetector),
		Broken:                referenceVerdict(false),
		Bypassed:              noVerdict(detectors.BypassDetector),
		Dormant:               noVerdict(detectors.DormantDetector),
	}
}

func TestMachineFollowsCoreStateTransitions(t *testing.T) {
	machine := NewMachine(DefaultConfig())

	decision, err := machine.Decide(Assessment{AssetID: "skill:project:new", At: vitalTime(1)})
	if err != nil {
		t.Fatalf("initial Decide: %v", err)
	}
	if decision.State != StateDormant || !decision.Transition || decision.Alert {
		t.Fatalf("initial decision = %+v, want dormant transition without alert", decision)
	}

	in := assess(StateDormant)
	decision, err = machine.Decide(in)
	if err != nil {
		t.Fatalf("dormant recovery: %v", err)
	}
	if decision.State != StateHealthy {
		t.Fatalf("dormant recovery state = %q, want healthy", decision.State)
	}

	in = assess(StateHealthy)
	in.Degraded = detectors.Verdict{Detector: detectors.DegradedDetector, Triggered: true, Observable: true, Rule: "recent rate < half baseline", Summary: "synthetic fixture degraded"}
	decision, err = machine.Decide(in)
	if err != nil {
		t.Fatalf("degraded: %v", err)
	}
	if decision.State != StateDegraded || !decision.Alert {
		t.Fatalf("degraded decision = %+v, want degraded alert", decision)
	}

	in = assess(StateDegraded)
	in.Silent = silentVerdict(true)
	decision, err = machine.Decide(in)
	if err != nil {
		t.Fatalf("silent: %v", err)
	}
	if decision.State != StateSilent || !decision.Alert {
		t.Fatalf("silent decision = %+v, want silent alert", decision)
	}
}

func TestMachineHandlesResurrectionAndFailure(t *testing.T) {
	machine := NewMachine(DefaultConfig())

	in := assess(StateSilent)
	in.RequestResurrection = true
	decision, err := machine.Decide(in)
	if err != nil {
		t.Fatalf("awaiting resurrection: %v", err)
	}
	if decision.State != StateAwaitingResurrection {
		t.Fatalf("awaiting decision = %+v, want awaiting_resurrection", decision)
	}

	in = assess(StateAwaitingResurrection)
	in.Resurrected = true
	decision, err = machine.Decide(in)
	if err != nil {
		t.Fatalf("resurrection: %v", err)
	}
	if decision.State != StateHealthy || !decision.Resurrection {
		t.Fatalf("resurrection decision = %+v, want healthy resurrection", decision)
	}
	if !strings.Contains(decision.Reason, "resur") {
		t.Fatalf("resurrection reason = %q, want explicit resurrection wording", decision.Reason)
	}

	in = assess(StateAwaitingResurrection)
	in.ResurrectionFailed = true
	decision, err = machine.Decide(in)
	if err != nil {
		t.Fatalf("resurrection failure: %v", err)
	}
	if decision.State != StateSilent || !decision.Resurrection {
		t.Fatalf("failure decision = %+v, want silent resurrection failure", decision)
	}
}

func TestBrokenIsAnOverlayAndDoesNotRewriteTheParticipationState(t *testing.T) {
	machine := NewMachine(DefaultConfig())
	in := assess(StateSilent)
	in.Broken = referenceVerdict(true)
	decision, err := machine.Decide(in)
	if err != nil {
		t.Fatalf("broken overlay: %v", err)
	}
	if decision.State != StateSilent || !decision.BrokenOverlay || !decision.Alert {
		t.Fatalf("broken overlay decision = %+v, want silent + broken overlay alert", decision)
	}

	in.PreviousBrokenOverlay = true
	decision, err = machine.Decide(in)
	if err != nil {
		t.Fatalf("repeated broken overlay: %v", err)
	}
	if decision.Alert {
		t.Fatalf("repeated broken overlay = %+v, want no duplicate alert", decision)
	}

	in.Broken = referenceVerdict(false)
	decision, err = machine.Decide(in)
	if err != nil {
		t.Fatalf("broken recovery: %v", err)
	}
	if decision.BrokenOverlay || !decision.Alert {
		t.Fatalf("broken recovery = %+v, want one recovery transition", decision)
	}
}

func TestMachineSeparatesNoOpportunityAndUnobservable(t *testing.T) {
	machine := NewMachine(DefaultConfig())
	in := Assessment{AssetID: "skill:project:no-opportunity", At: vitalTime(1), PreviousState: StateHealthy, NoOpportunity: true}
	decision, err := machine.Decide(in)
	if err != nil {
		t.Fatalf("no opportunity: %v", err)
	}
	if decision.State != StateNoOpportunity {
		t.Fatalf("no opportunity = %+v", decision)
	}

	in = Assessment{AssetID: "skill:project:unobservable", At: vitalTime(1), PreviousState: StateHealthy, Unobservable: true}
	decision, err = machine.Decide(in)
	if err != nil {
		t.Fatalf("unobservable: %v", err)
	}
	if decision.State != StateUnobservable || strings.Contains(strings.ToLower(decision.Reason), "0") {
		t.Fatalf("unobservable = %+v, want explicit non-zero-free unknown state", decision)
	}
}

func TestTransitionMapIncludesDesignEdgesAndRejectsArchivedExit(t *testing.T) {
	for _, edge := range [][2]State{
		{StateDormant, StateHealthy},
		{StateHealthy, StateDegraded},
		{StateHealthy, StateSilent},
		{StateHealthy, StateBypassed},
		{StateSilent, StateAwaitingResurrection},
		{StateAwaitingResurrection, StateHealthy},
		{StateAwaitingResurrection, StateSilent},
		{StateDormant, StateArchived},
	} {
		if !CanTransition(edge[0], edge[1]) {
			t.Errorf("CanTransition(%q, %q) = false, want true", edge[0], edge[1])
		}
	}
	if CanTransition(StateArchived, StateHealthy) {
		t.Fatal("archived -> healthy must be rejected without an explicit restore action")
	}
}

// ADR-26: the first evaluation lands on the evidence. Before it, every asset's
// first-ever Decide returned dormant even when the same Assessment already
// carried participation — on real history that painted 12 skills as "几乎未
// 使用" for one import cycle, and the day's own dogfood log was misled by it.
func TestFirstEvaluationWithEvidenceLandsOnHealthy(t *testing.T) {
	machine := NewMachine(DefaultConfig())
	in := assess("")
	in.HasBaseline = false
	decision, err := machine.Decide(in)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision.State != StateHealthy {
		t.Fatalf("first evaluation with participation = %q, want healthy without a dormant stopover", decision.State)
	}
	if decision.Alert {
		t.Fatalf("decision = %+v, want no alert for a healthy start", decision)
	}

	// Without participation evidence the neutral dormant start stays: an
	// asset with an opportunity and no recorded participation reads as
	// dormant until evidence arrives, which is the honest reading.
	bare := assess("")
	bare.ParticipationObserved = false
	bare.HasBaseline = false
	decision, err = machine.Decide(bare)
	if err != nil {
		t.Fatalf("Decide bare: %v", err)
	}
	if decision.State != StateDormant {
		t.Fatalf("first evaluation without participation = %q, want the neutral dormant start", decision.State)
	}
}
