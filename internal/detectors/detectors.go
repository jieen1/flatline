// Package detectors contains deterministic, side-effect-free evidence
// evaluators for the P4 vital-state layer.
//
// A detector never writes a state or emits an alert. It only returns a
// verdict carrying the evidence needed by the state machine and the UI.
// This keeps derived results replayable (ADR-10) and keeps causality claims
// out of the product (AGENTS.md §2).
package detectors

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"flatline/internal/canonical"
)

const (
	DetectorVersion   = "detectors/1"
	SilentDetector    = "silent/1"
	DormantDetector   = "dormant/1"
	ReferenceDetector = "reference-health/1"
	BypassDetector    = "bypass/1"
	DegradedDetector  = "degraded/1"
)

// Evidence is the structured, drillable basis of a verdict. Pointer fields
// distinguish “not recorded” from a measured zero.
type Evidence struct {
	Numerator         *int                         `json:"numerator,omitempty"`
	Denominator       *int                         `json:"denominator,omitempty"`
	Rate              *float64                     `json:"rate,omitempty"`
	Threshold         string                       `json:"threshold,omitempty"`
	WindowStart       *time.Time                   `json:"window_start,omitempty"`
	WindowEnd         *time.Time                   `json:"window_end,omitempty"`
	ObservationLevels []canonical.ObservationLevel `json:"observation_levels,omitempty"`
	LocatorRefs       []string                     `json:"locator_refs,omitempty"`
	Details           map[string]any               `json:"details,omitempty"`
}

// Verdict is the common detector output. Observable=false means the source
// did not provide enough evidence to decide; it never means “zero”.
type Verdict struct {
	Detector   string     `json:"detector"`
	Triggered  bool       `json:"triggered"`
	Observable bool       `json:"observable"`
	ReasonCode string     `json:"reason_code"`
	Rule       string     `json:"rule"`
	Summary    string     `json:"summary"`
	DetectedAt *time.Time `json:"detected_at,omitempty"`
	Evidence   Evidence   `json:"evidence"`
}

// OpportunityObservation is the tracker projection consumed by silence and
// degradation detectors. ParticipationKnown is deliberately separate from
// Participated: an adapter that cannot observe a signal must not be counted as
// an observed zero.
type OpportunityObservation struct {
	ID                 int64
	SessionID          string
	DetectedAt         time.Time
	Participated       bool
	ParticipationKnown bool
	ObservationLevels  []canonical.ObservationLevel
	LocatorRefs        []string
}

type SilentInput struct {
	AssetID                        string
	HistoricalOpportunityCount     int
	HistoricalParticipationCount   int
	Recent                         []OpportunityObservation
	RequiredRecentOpportunities    int
	MinimumHistoricalOpportunities int
	MinimumHistoricalRate          float64
}

const (
	DefaultRequiredRecentOpportunities    = 8
	DefaultMinimumHistoricalOpportunities = 5
	DefaultMinimumHistoricalRate          = 0.30
)

// EvaluateSilent applies the one-line rule:
// “historical participation >= 5 and rate >= 30%, then the latest 8 known
// opportunities contain 0 participations.”
func EvaluateSilent(in SilentInput) (Verdict, error) {
	if strings.TrimSpace(in.AssetID) == "" {
		return Verdict{}, fmt.Errorf("detectors: silent asset id is required")
	}
	if in.HistoricalOpportunityCount < 0 || in.HistoricalParticipationCount < 0 || in.HistoricalParticipationCount > in.HistoricalOpportunityCount {
		return Verdict{}, fmt.Errorf("detectors: invalid silent historical counts")
	}
	if in.RequiredRecentOpportunities <= 0 {
		in.RequiredRecentOpportunities = DefaultRequiredRecentOpportunities
	}
	if in.MinimumHistoricalOpportunities <= 0 {
		in.MinimumHistoricalOpportunities = DefaultMinimumHistoricalOpportunities
	}
	if in.MinimumHistoricalRate <= 0 {
		in.MinimumHistoricalRate = DefaultMinimumHistoricalRate
	}
	recent := sortedOpportunities(in.Recent)
	if err := validateOpportunities(recent); err != nil {
		return Verdict{}, fmt.Errorf("detectors: silent: %w", err)
	}
	denominator := in.HistoricalOpportunityCount
	numerator := in.HistoricalParticipationCount
	rate := ratio(numerator, denominator)
	evidence := Evidence{
		Numerator:   intPointer(numerator),
		Denominator: intPointer(denominator),
		Rate:        rate,
		Threshold: fmt.Sprintf("historical opportunities >= %d; historical rate >= %.0f%%; latest opportunities = %d",
			in.MinimumHistoricalOpportunities, in.MinimumHistoricalRate*100, in.RequiredRecentOpportunities),
		Details: map[string]any{
			"historical_opportunities":  denominator,
			"historical_participations": numerator,
			"recent_required":           in.RequiredRecentOpportunities,
			"recent_available":          len(recent),
		},
	}
	if len(recent) < in.RequiredRecentOpportunities {
		return Verdict{Detector: SilentDetector, Observable: true, ReasonCode: "insufficient_recent_opportunities", Rule: silentRule(in), Summary: fmt.Sprintf("%s: not silent; recent opportunities %d of required %d", in.AssetID, len(recent), in.RequiredRecentOpportunities), Evidence: evidence}, nil
	}
	recent = recent[len(recent)-in.RequiredRecentOpportunities:]
	for _, opportunity := range recent {
		if !opportunity.ParticipationKnown {
			return Verdict{Detector: SilentDetector, Observable: false, ReasonCode: "unknown_participation", Rule: silentRule(in), Summary: fmt.Sprintf("%s: silence is unknown; a recent opportunity has unrecorded participation (unknown is not zero)", in.AssetID), Evidence: evidence}, nil
		}
	}
	if denominator < in.MinimumHistoricalOpportunities || rate == nil || *rate < in.MinimumHistoricalRate {
		return Verdict{Detector: SilentDetector, Observable: true, ReasonCode: "no_historical_baseline", Rule: silentRule(in), Summary: fmt.Sprintf("%s: not silent; historical participation %d of %d does not meet the baseline threshold", in.AssetID, numerator, denominator), Evidence: evidence}, nil
	}
	recentParticipations := 0
	levels := make([]canonical.ObservationLevel, 0)
	locators := make([]string, 0)
	for _, opportunity := range recent {
		if opportunity.Participated {
			recentParticipations++
		}
		levels = append(levels, opportunity.ObservationLevels...)
		locators = append(locators, opportunity.LocatorRefs...)
	}
	recentDenominator := len(recent)
	evidence.Numerator = intPointer(recentParticipations)
	evidence.Denominator = intPointer(recentDenominator)
	evidence.ObservationLevels = uniqueLevels(levels)
	evidence.LocatorRefs = uniqueStrings(locators)
	evidence.Details["recent_participations"] = recentParticipations
	if recentParticipations != 0 {
		return Verdict{Detector: SilentDetector, Observable: true, ReasonCode: "recent_participation_present", Rule: silentRule(in), Summary: fmt.Sprintf("%s: not silent; recent participation %d of %d, historical participation %d of %d", in.AssetID, recentParticipations, recentDenominator, numerator, denominator), Evidence: evidence}, nil
	}
	return Verdict{Detector: SilentDetector, Triggered: true, Observable: true, ReasonCode: "consecutive_zero_participation", Rule: silentRule(in), Summary: fmt.Sprintf("%s entered silent: historical participation %d of %d (%.0f%%); latest %d opportunities 0 of %d participated", in.AssetID, numerator, denominator, *rate*100, in.RequiredRecentOpportunities, recentDenominator), Evidence: evidence}, nil
}

func silentRule(in SilentInput) string {
	return fmt.Sprintf("historical participation >= %d of %d opportunities and rate >= %.0f%%; latest %d known opportunities have 0 participation", DefaultMinimumHistoricalOpportunities, in.HistoricalOpportunityCount, DefaultMinimumHistoricalRate*100, in.RequiredRecentOpportunities)
}

type DegradedInput struct {
	AssetID                    string
	BaselineRate               float64
	MinimumRecentOpportunities int
	Recent                     []OpportunityObservation
}

// EvaluateDegraded applies the one-line rule “latest >=5 known opportunity
// sessions have a participation rate below half of the recorded baseline”.
func EvaluateDegraded(in DegradedInput) (Verdict, error) {
	if strings.TrimSpace(in.AssetID) == "" {
		return Verdict{}, fmt.Errorf("detectors: degraded asset id is required")
	}
	if in.BaselineRate < 0 || in.BaselineRate > 1 {
		return Verdict{}, fmt.Errorf("detectors: degraded baseline rate must be in [0,1]")
	}
	if in.MinimumRecentOpportunities <= 0 {
		in.MinimumRecentOpportunities = 5
	}
	recent := sortedOpportunities(in.Recent)
	if err := validateOpportunities(recent); err != nil {
		return Verdict{}, fmt.Errorf("detectors: degraded: %w", err)
	}
	if len(recent) < in.MinimumRecentOpportunities {
		return Verdict{Detector: DegradedDetector, Observable: true, ReasonCode: "insufficient_recent_opportunities", Rule: degradedRule(in), Summary: fmt.Sprintf("%s: not degraded; recent opportunities %d of required %d", in.AssetID, len(recent), in.MinimumRecentOpportunities), Evidence: Evidence{Threshold: fmt.Sprintf("recent opportunities >= %d; recent rate < baseline x 0.5", in.MinimumRecentOpportunities), Details: map[string]any{"baseline_rate": in.BaselineRate, "recent_available": len(recent)}}}, nil
	}
	for _, opportunity := range recent {
		if !opportunity.ParticipationKnown {
			return Verdict{Detector: DegradedDetector, Observable: false, ReasonCode: "unknown_participation", Rule: degradedRule(in), Summary: fmt.Sprintf("%s: degradation is unknown; a recent opportunity has unrecorded participation", in.AssetID)}, nil
		}
	}
	participations := 0
	for _, opportunity := range recent {
		if opportunity.Participated {
			participations++
		}
	}
	recentRate := ratio(participations, len(recent))
	threshold := in.BaselineRate * 0.5
	evidence := Evidence{Numerator: intPointer(participations), Denominator: intPointer(len(recent)), Rate: recentRate, Threshold: fmt.Sprintf("recent rate < baseline %.4f x 0.5 = %.4f", in.BaselineRate, threshold), Details: map[string]any{"baseline_rate": in.BaselineRate, "recent_rate": derefRate(recentRate)}}
	if recentRate != nil && *recentRate < threshold {
		return Verdict{Detector: DegradedDetector, Triggered: true, Observable: true, ReasonCode: "recent_rate_below_half_baseline", Rule: degradedRule(in), Summary: fmt.Sprintf("%s is degraded: recent participation %d of %d (%.0f%%); baseline %.0f%%", in.AssetID, participations, len(recent), *recentRate*100, in.BaselineRate*100), Evidence: evidence}, nil
	}
	return Verdict{Detector: DegradedDetector, Observable: true, ReasonCode: "recent_rate_not_below_threshold", Rule: degradedRule(in), Summary: fmt.Sprintf("%s: not degraded; recent participation %d of %d; baseline %.0f%%", in.AssetID, participations, len(recent), in.BaselineRate*100), Evidence: evidence}, nil
}

func degradedRule(in DegradedInput) string {
	return fmt.Sprintf("latest %d known opportunities have participation rate < recorded baseline x 0.5", in.MinimumRecentOpportunities)
}

type DormantInput struct {
	AssetID                  string
	FirstSeenAt              time.Time
	AsOf                     time.Time
	CumulativeParticipations int
	MinimumAge               time.Duration
	MaximumParticipations    int
}

const (
	DefaultDormantAge               = 30 * 24 * time.Hour
	DefaultDormantMaxParticipations = 2
)

// EvaluateDormant applies the one-line rule “asset age >=30 days and
// cumulative participation <=2”.
func EvaluateDormant(in DormantInput) (Verdict, error) {
	if strings.TrimSpace(in.AssetID) == "" {
		return Verdict{}, fmt.Errorf("detectors: dormant asset id is required")
	}
	if in.FirstSeenAt.IsZero() || in.AsOf.IsZero() {
		return Verdict{}, fmt.Errorf("detectors: dormant first_seen_at and as_of are required")
	}
	if in.FirstSeenAt.Location() != time.UTC || in.AsOf.Location() != time.UTC {
		return Verdict{}, fmt.Errorf("detectors: dormant timestamps must be UTC")
	}
	if in.AsOf.Before(in.FirstSeenAt) {
		return Verdict{}, fmt.Errorf("detectors: dormant as_of precedes first_seen_at")
	}
	if in.CumulativeParticipations < 0 {
		return Verdict{}, fmt.Errorf("detectors: dormant participation count cannot be negative")
	}
	if in.MinimumAge <= 0 {
		in.MinimumAge = DefaultDormantAge
	}
	if in.MaximumParticipations < 0 {
		return Verdict{}, fmt.Errorf("detectors: dormant maximum participation cannot be negative")
	}
	if in.MaximumParticipations == 0 {
		in.MaximumParticipations = DefaultDormantMaxParticipations
	}
	age := in.AsOf.Sub(in.FirstSeenAt)
	evidence := Evidence{Threshold: fmt.Sprintf("age >= %s and cumulative participation <= %d", in.MinimumAge, in.MaximumParticipations), Details: map[string]any{"age": age.String(), "cumulative_participations": in.CumulativeParticipations}}
	if age >= in.MinimumAge && in.CumulativeParticipations <= in.MaximumParticipations {
		return Verdict{Detector: DormantDetector, Triggered: true, Observable: true, ReasonCode: "age_and_low_cumulative_participation", Rule: fmt.Sprintf("asset age >= %s and cumulative participation <= %d", in.MinimumAge, in.MaximumParticipations), Summary: fmt.Sprintf("%s is dormant: age %s; cumulative participation %d (threshold <= %d)", in.AssetID, age.Round(time.Second), in.CumulativeParticipations, in.MaximumParticipations), DetectedAt: timePointer(in.AsOf), Evidence: evidence}, nil
	}
	return Verdict{Detector: DormantDetector, Observable: true, ReasonCode: "dormant_threshold_not_met", Rule: fmt.Sprintf("asset age >= %s and cumulative participation <= %d", in.MinimumAge, in.MaximumParticipations), Summary: fmt.Sprintf("%s: not dormant; age %s; cumulative participation %d", in.AssetID, age.Round(time.Second), in.CumulativeParticipations), DetectedAt: timePointer(in.AsOf), Evidence: evidence}, nil
}

type ReferenceKind string

const (
	ReferenceCommand ReferenceKind = "command"
	ReferencePath    ReferenceKind = "path"
	ReferenceTool    ReferenceKind = "tool"
)

func (k ReferenceKind) Valid() bool {
	return k == ReferenceCommand || k == ReferencePath || k == ReferenceTool
}

type ReferenceObservation struct {
	Kind       ReferenceKind
	Value      string
	Exists     *bool
	Known      bool
	Detail     string
	LocatorRef string
}

type ReferenceInput struct {
	AssetID        string
	AssetVersionID int64
	CheckedAt      time.Time
	Items          []ReferenceObservation
}

// ReferenceVerdict carries counts used by the reference-health API and
// persistence layer. Unknown items are counted separately from failed items.
type ReferenceVerdict struct {
	Verdict
	Checked int `json:"checked"`
	Failed  int `json:"failed"`
	Unknown int `json:"unknown"`
}

// EvaluateReferenceHealth evaluates only explicit reference observations. It
// never invokes a shell or resolves a path itself; those side effects belong
// to an injected checker at the ingestion boundary.
func EvaluateReferenceHealth(in ReferenceInput) (ReferenceVerdict, error) {
	if strings.TrimSpace(in.AssetID) == "" {
		return ReferenceVerdict{}, fmt.Errorf("detectors: reference asset id is required")
	}
	if in.AssetVersionID <= 0 {
		return ReferenceVerdict{}, fmt.Errorf("detectors: reference asset version id must be positive")
	}
	if in.CheckedAt.IsZero() || in.CheckedAt.Location() != time.UTC {
		return ReferenceVerdict{}, fmt.Errorf("detectors: reference checked_at must be UTC")
	}
	checked, failed, unknown := 0, 0, 0
	locators := make([]string, 0)
	details := make([]map[string]any, 0, len(in.Items))
	for _, item := range in.Items {
		if !item.Kind.Valid() || strings.TrimSpace(item.Value) == "" {
			return ReferenceVerdict{}, fmt.Errorf("detectors: invalid reference observation %q/%q", item.Kind, item.Value)
		}
		if item.LocatorRef != "" {
			locators = append(locators, item.LocatorRef)
		}
		entry := map[string]any{"kind": item.Kind, "value": item.Value, "known": item.Known, "detail": item.Detail}
		if !item.Known || item.Exists == nil {
			unknown++
			details = append(details, entry)
			continue
		}
		checked++
		entry["exists"] = *item.Exists
		if !*item.Exists {
			failed++
		}
		details = append(details, entry)
	}
	evidence := Evidence{Numerator: intPointer(failed), Denominator: intPointer(checked), Threshold: "any known referenced command/path/tool with exists=false", LocatorRefs: uniqueStrings(locators), Details: map[string]any{"items": details, "checked": checked, "failed": failed, "unknown": unknown}}
	if len(in.Items) == 0 {
		return ReferenceVerdict{Verdict: Verdict{Detector: ReferenceDetector, Observable: true, ReasonCode: "no_references_recorded", Rule: "0 recorded references have existence failures", Summary: fmt.Sprintf("%s version %d: no references recorded for health check", in.AssetID, in.AssetVersionID), DetectedAt: timePointer(in.CheckedAt), Evidence: evidence}}, nil
	}
	if checked == 0 {
		return ReferenceVerdict{Verdict: Verdict{Detector: ReferenceDetector, Observable: false, ReasonCode: "reference_existence_unknown", Rule: "known failed references / known checked references", Summary: fmt.Sprintf("%s version %d: reference health unknown; %d references have no recorded existence result", in.AssetID, in.AssetVersionID, unknown), DetectedAt: timePointer(in.CheckedAt), Evidence: evidence}, Unknown: unknown}, nil
	}
	if failed > 0 {
		return ReferenceVerdict{Verdict: Verdict{Detector: ReferenceDetector, Triggered: true, Observable: true, ReasonCode: "missing_reference", Rule: "known failed references / known checked references > 0", Summary: fmt.Sprintf("%s version %d is broken: %d of %d checked references are missing", in.AssetID, in.AssetVersionID, failed, checked), DetectedAt: timePointer(in.CheckedAt), Evidence: evidence}, Checked: checked, Failed: failed, Unknown: unknown}, nil
	}
	return ReferenceVerdict{Verdict: Verdict{Detector: ReferenceDetector, Observable: true, ReasonCode: "references_present", Rule: "known failed references / known checked references = 0", Summary: fmt.Sprintf("%s version %d: %d of %d checked references exist (%d unknown)", in.AssetID, in.AssetVersionID, checked, checked, unknown), DetectedAt: timePointer(in.CheckedAt), Evidence: evidence}, Checked: checked, Unknown: unknown}, nil
}

type EvidencePoint struct {
	Present    bool
	Level      canonical.ObservationLevel
	LocatorRef string
}

type BypassInput struct {
	AssetID    string
	OccurredAt time.Time
	Violated   bool
	Invocation EvidencePoint
	Violation  EvidencePoint
}

// EvaluateBypass requires exact observation at both ends of the
// invoked-then-violated chain. “followed” remains a participation signal and
// is not silently promoted to an observation level.
func EvaluateBypass(in BypassInput) (Verdict, error) {
	if strings.TrimSpace(in.AssetID) == "" {
		return Verdict{}, fmt.Errorf("detectors: bypass asset id is required")
	}
	if in.OccurredAt.IsZero() || in.OccurredAt.Location() != time.UTC {
		return Verdict{}, fmt.Errorf("detectors: bypass occurred_at must be UTC")
	}
	rule := "invocation and violation both have observation_level=invoked, then violation=true"
	if !in.Invocation.Present {
		return Verdict{Detector: BypassDetector, Observable: true, ReasonCode: "not_invoked", Rule: rule, Summary: fmt.Sprintf("%s: no exact invocation evidence; bypass not detected", in.AssetID), DetectedAt: timePointer(in.OccurredAt)}, nil
	}
	if in.Invocation.Level != canonical.LevelInvoked || !in.Violation.Present || in.Violation.Level != canonical.LevelInvoked {
		return Verdict{Detector: BypassDetector, Observable: false, ReasonCode: "incomplete_exact_chain", Rule: rule, Summary: fmt.Sprintf("%s: bypass unknown; invocation and violation must both be exact invoked evidence", in.AssetID), DetectedAt: timePointer(in.OccurredAt), Evidence: Evidence{ObservationLevels: uniqueLevels([]canonical.ObservationLevel{normalizeObservationLevel(in.Invocation.Level), normalizeObservationLevel(in.Violation.Level)}), LocatorRefs: uniqueStrings([]string{in.Invocation.LocatorRef, in.Violation.LocatorRef})}}, nil
	}
	evidence := Evidence{Numerator: intPointer(1), Denominator: intPointer(1), Threshold: "exact invocation + exact violation", ObservationLevels: []canonical.ObservationLevel{canonical.LevelInvoked}, LocatorRefs: uniqueStrings([]string{in.Invocation.LocatorRef, in.Violation.LocatorRef})}
	if in.Violated {
		return Verdict{Detector: BypassDetector, Triggered: true, Observable: true, ReasonCode: "invoked_then_violated", Rule: rule, Summary: fmt.Sprintf("%s was bypassed: exact invocation and exact violation evidence are both recorded", in.AssetID), DetectedAt: timePointer(in.OccurredAt), Evidence: evidence}, nil
	}
	return Verdict{Detector: BypassDetector, Observable: true, ReasonCode: "no_violation", Rule: rule, Summary: fmt.Sprintf("%s: exact invocation has no recorded violation", in.AssetID), DetectedAt: timePointer(in.OccurredAt), Evidence: evidence}, nil
}

func sortedOpportunities(input []OpportunityObservation) []OpportunityObservation {
	out := append([]OpportunityObservation(nil), input...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].DetectedAt.Equal(out[j].DetectedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].DetectedAt.Before(out[j].DetectedAt)
	})
	return out
}

func validateOpportunities(input []OpportunityObservation) error {
	for _, opportunity := range input {
		if opportunity.ID <= 0 || strings.TrimSpace(opportunity.SessionID) == "" || opportunity.DetectedAt.IsZero() {
			return fmt.Errorf("opportunity requires positive id, session id, and detected_at")
		}
		if opportunity.DetectedAt.Location() != time.UTC {
			return fmt.Errorf("opportunity %d detected_at must be UTC", opportunity.ID)
		}
		for _, level := range opportunity.ObservationLevels {
			if !level.Valid() {
				return fmt.Errorf("opportunity %d has invalid observation level %q", opportunity.ID, level)
			}
		}
	}
	return nil
}

func ratio(numerator, denominator int) *float64 {
	if denominator <= 0 {
		return nil
	}
	rate := float64(numerator) / float64(denominator)
	return &rate
}

func derefRate(rate *float64) float64 {
	if rate == nil {
		return 0
	}
	return *rate
}

func intPointer(value int) *int              { return &value }
func timePointer(value time.Time) *time.Time { return &value }

func uniqueStrings(input []string) []string {
	seen := make(map[string]struct{}, len(input))
	out := make([]string, 0, len(input))
	for _, value := range input {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func uniqueLevels(input []canonical.ObservationLevel) []canonical.ObservationLevel {
	seen := make(map[canonical.ObservationLevel]struct{}, len(input))
	out := make([]canonical.ObservationLevel, 0, len(input))
	for _, level := range input {
		if !level.Valid() {
			continue
		}
		if _, ok := seen[level]; ok {
			continue
		}
		seen[level] = struct{}{}
		out = append(out, level)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func normalizeObservationLevel(level canonical.ObservationLevel) canonical.ObservationLevel {
	if !level.Valid() {
		return canonical.LevelUnknown
	}
	return level
}
