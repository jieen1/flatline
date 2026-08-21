package detectors

import (
	"strings"
	"testing"
	"time"

	"flatline/internal/canonical"
)

// These are synthetic, deliberately labelled inputs. They exercise detector
// boundaries without importing or exposing real user session data.

func detectorTime(day int) time.Time {
	return time.Date(2026, 8, day, 12, 0, 0, 0, time.UTC)
}

func opportunities(n int, participatedAt ...int) []OpportunityObservation {
	participated := make(map[int]bool, len(participatedAt))
	for _, index := range participatedAt {
		participated[index] = true
	}
	out := make([]OpportunityObservation, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, OpportunityObservation{
			ID:                 int64(i + 1),
			SessionID:          "fixture-session-" + string(rune('a'+i)),
			DetectedAt:         detectorTime(i + 1),
			Participated:       participated[i],
			ParticipationKnown: true,
			ObservationLevels:  []canonical.ObservationLevel{canonical.LevelInvoked},
		})
	}
	return out
}

func TestSilentUsesHistoricalAndRecentBoundaries(t *testing.T) {
	base := SilentInput{
		AssetID:                        "skill:project:fixture",
		HistoricalOpportunityCount:     10,
		HistoricalParticipationCount:   3,
		Recent:                         opportunities(8),
		RequiredRecentOpportunities:    8,
		MinimumHistoricalOpportunities: 5,
		MinimumHistoricalRate:          0.30,
	}

	for name, input := range map[string]SilentInput{
		"historical count below minimum": func() SilentInput {
			in := base
			in.HistoricalOpportunityCount = 4
			return in
		}(),
		"historical rate below minimum": func() SilentInput {
			in := base
			in.HistoricalParticipationCount = 2
			return in
		}(),
		"recent N minus one": func() SilentInput {
			in := base
			in.Recent = opportunities(7)
			return in
		}(),
		"recent N with participation": func() SilentInput {
			in := base
			in.Recent = opportunities(8, 7)
			return in
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			verdict, err := EvaluateSilent(input)
			if err != nil {
				t.Fatalf("EvaluateSilent: %v", err)
			}
			if verdict.Triggered {
				t.Fatalf("Triggered = true, want false: %+v", verdict)
			}
			if !verdict.Observable {
				t.Fatalf("Observable = false, want true: %+v", verdict)
			}
		})
	}

	verdict, err := EvaluateSilent(base)
	if err != nil {
		t.Fatalf("EvaluateSilent trigger: %v", err)
	}
	if !verdict.Triggered || !verdict.Observable {
		t.Fatalf("trigger verdict = %+v, want observable trigger", verdict)
	}
	if !strings.Contains(verdict.Summary, "0 of 8") || !strings.Contains(verdict.Summary, "3 of 10") {
		t.Fatalf("summary = %q, want numerator/denominator evidence", verdict.Summary)
	}
	if strings.Contains(strings.ToLower(verdict.Summary), "cause") || strings.Contains(verdict.Summary, "导致") {
		t.Fatalf("summary contains causal language: %q", verdict.Summary)
	}
}

func TestSilentDoesNotTreatUnknownAsZero(t *testing.T) {
	in := SilentInput{
		AssetID:                        "skill:project:fixture",
		HistoricalOpportunityCount:     10,
		HistoricalParticipationCount:   7,
		RequiredRecentOpportunities:    2,
		MinimumHistoricalOpportunities: 5,
		MinimumHistoricalRate:          0.30,
		Recent: []OpportunityObservation{
			{ID: 1, SessionID: "fixture-1", DetectedAt: detectorTime(1), ParticipationKnown: true},
			{ID: 2, SessionID: "fixture-2", DetectedAt: detectorTime(2), ParticipationKnown: false},
		},
	}
	verdict, err := EvaluateSilent(in)
	if err != nil {
		t.Fatalf("EvaluateSilent: %v", err)
	}
	if verdict.Triggered || verdict.Observable {
		t.Fatalf("verdict = %+v, want non-triggering unobservable result", verdict)
	}
	if !strings.Contains(verdict.Summary, "unknown") {
		t.Fatalf("summary = %q, want explicit unknown", verdict.Summary)
	}
}

func TestDormantBoundary(t *testing.T) {
	base := DormantInput{
		AssetID:                  "skill:project:fixture",
		FirstSeenAt:              detectorTime(1),
		AsOf:                     detectorTime(31),
		CumulativeParticipations: 2,
		MinimumAge:               30 * 24 * time.Hour,
		MaximumParticipations:    2,
	}
	verdict, err := EvaluateDormant(base)
	if err != nil {
		t.Fatalf("EvaluateDormant: %v", err)
	}
	if !verdict.Triggered || !verdict.Observable {
		t.Fatalf("verdict = %+v, want dormant", verdict)
	}

	base.AsOf = detectorTime(31)
	verdict, err = EvaluateDormant(base)
	if err != nil {
		t.Fatalf("EvaluateDormant at boundary: %v", err)
	}
	if !verdict.Triggered {
		t.Fatalf("30-day verdict = %+v, want dormant", verdict)
	}

	base.AsOf = detectorTime(30)
	verdict, err = EvaluateDormant(base)
	if err != nil {
		t.Fatalf("EvaluateDormant below boundary: %v", err)
	}
	if verdict.Triggered {
		t.Fatalf("29-day verdict = %+v, want not dormant", verdict)
	}
}

func TestReferenceHealthRequiresKnownEvidence(t *testing.T) {
	missing := false
	verdict, err := EvaluateReferenceHealth(ReferenceInput{
		AssetID:        "skill:project:fixture",
		AssetVersionID: 1,
		CheckedAt:      detectorTime(2),
		Items: []ReferenceObservation{{
			Kind: ReferenceCommand, Value: "missing-command", Exists: &missing, Known: true,
			LocatorRef: "fixture:asset:line-2",
		}},
	})
	if err != nil {
		t.Fatalf("EvaluateReferenceHealth: %v", err)
	}
	if !verdict.Triggered || !verdict.Observable || verdict.Failed != 1 {
		t.Fatalf("verdict = %+v, want one known failure", verdict)
	}

	verdict, err = EvaluateReferenceHealth(ReferenceInput{
		AssetID: "skill:project:fixture", AssetVersionID: 1, CheckedAt: detectorTime(2),
		Items: []ReferenceObservation{{Kind: ReferenceTool, Value: "unrecorded-tool", Known: false}},
	})
	if err != nil {
		t.Fatalf("unknown reference: %v", err)
	}
	if verdict.Triggered || verdict.Observable {
		t.Fatalf("unknown verdict = %+v, want no trigger and unobservable", verdict)
	}
}

func TestBypassRequiresExactEvidenceAtBothEnds(t *testing.T) {
	trigger, err := EvaluateBypass(BypassInput{
		AssetID: "skill:project:fixture", OccurredAt: detectorTime(3), Violated: true,
		Invocation: EvidencePoint{Present: true, Level: canonical.LevelInvoked, LocatorRef: "fixture:session:message-1"},
		Violation:  EvidencePoint{Present: true, Level: canonical.LevelInvoked, LocatorRef: "fixture:session:message-2"},
	})
	if err != nil {
		t.Fatalf("EvaluateBypass: %v", err)
	}
	if !trigger.Triggered || !trigger.Observable {
		t.Fatalf("trigger = %+v, want exact invoked-then-violated", trigger)
	}

	unknown, err := EvaluateBypass(BypassInput{
		AssetID: "skill:project:fixture", OccurredAt: detectorTime(3), Violated: true,
		Invocation: EvidencePoint{Present: true, Level: canonical.LevelInvoked},
		Violation:  EvidencePoint{Present: true, Level: canonical.LevelUnknown},
	})
	if err != nil {
		t.Fatalf("unknown bypass: %v", err)
	}
	if unknown.Triggered || unknown.Observable || !strings.Contains(unknown.Summary, "unknown") {
		t.Fatalf("unknown = %+v, want explicit unobservable result", unknown)
	}

	missingLevel, err := EvaluateBypass(BypassInput{
		AssetID: "skill:project:fixture", OccurredAt: detectorTime(3), Violated: true,
		Invocation: EvidencePoint{Present: true, Level: canonical.LevelInvoked},
		Violation:  EvidencePoint{Present: false},
	})
	if err != nil {
		t.Fatalf("missing-level bypass: %v", err)
	}
	for _, level := range missingLevel.Evidence.ObservationLevels {
		if !level.Valid() {
			t.Fatalf("missing-level evidence contains invalid observation level %q", level)
		}
	}
	if len(missingLevel.Evidence.ObservationLevels) != 2 || missingLevel.Evidence.ObservationLevels[1] != canonical.LevelUnknown {
		t.Fatalf("missing-level evidence = %v, want invoked and unknown", missingLevel.Evidence.ObservationLevels)
	}
}

func TestDegradedRequiresFiveRecentOpportunities(t *testing.T) {
	verdict, err := EvaluateDegraded(DegradedInput{
		AssetID: "skill:project:fixture", BaselineRate: 0.8, MinimumRecentOpportunities: 5,
		Recent: opportunities(5, 0),
	})
	if err != nil {
		t.Fatalf("EvaluateDegraded: %v", err)
	}
	if !verdict.Triggered {
		t.Fatalf("verdict = %+v, want degraded", verdict)
	}
}
