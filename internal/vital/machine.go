// Package vital implements the deterministic Vital State Machine (VSM).
//
// Detectors supply evidence; this package alone chooses the primary state and
// whether a transition is an alert projection. Broken content is represented
// as an overlay whenever a participation state already exists, preserving the
// orthogonality described by system design v0.4 §3.2.
package vital

import (
	"fmt"
	"strings"
	"time"

	"flatline/internal/detectors"
)

type State string

const (
	StateHealthy              State = "healthy"
	StateDegraded             State = "degraded"
	StateSilent               State = "silent"
	StateBroken               State = "broken"
	StateBypassed             State = "bypassed"
	StateDormant              State = "dormant"
	StateNoOpportunity        State = "no_opportunity"
	StateUnobservable         State = "unobservable"
	StateAwaitingResurrection State = "awaiting_resurrection"
	StateArchived             State = "archived"
)

func (s State) Valid() bool {
	switch s {
	case StateHealthy, StateDegraded, StateSilent, StateBroken, StateBypassed,
		StateDormant, StateNoOpportunity, StateUnobservable,
		StateAwaitingResurrection, StateArchived:
		return true
	default:
		return false
	}
}

const (
	MachineVersion          = "vital/1"
	MachineSchemaVersion    = "schema/1"
	DefaultThresholdVersion = "thresholds/1"
)

type Config struct {
	SilentMinimumHistoricalOpportunities int
	SilentMinimumHistoricalRate          float64
	SilentConsecutiveOpportunities       int
	DegradedMinimumRecentOpportunities   int
	DormantMinimumAge                    time.Duration
	DormantMaximumParticipations         int
	ResurrectionFailureOpportunities     int
	ThresholdVersion                     string
}

func DefaultConfig() Config {
	return Config{
		SilentMinimumHistoricalOpportunities: detectors.DefaultMinimumHistoricalOpportunities,
		SilentMinimumHistoricalRate:          detectors.DefaultMinimumHistoricalRate,
		SilentConsecutiveOpportunities:       detectors.DefaultRequiredRecentOpportunities,
		DegradedMinimumRecentOpportunities:   5,
		DormantMinimumAge:                    detectors.DefaultDormantAge,
		DormantMaximumParticipations:         detectors.DefaultDormantMaxParticipations,
		ResurrectionFailureOpportunities:     detectors.DefaultRequiredRecentOpportunities,
		ThresholdVersion:                     DefaultThresholdVersion,
	}
}

func (c Config) Validate() error {
	if c.SilentMinimumHistoricalOpportunities <= 0 || c.SilentConsecutiveOpportunities <= 0 || c.DegradedMinimumRecentOpportunities <= 0 || c.ResurrectionFailureOpportunities <= 0 {
		return fmt.Errorf("vital: count thresholds must be positive")
	}
	if c.SilentMinimumHistoricalRate <= 0 || c.SilentMinimumHistoricalRate > 1 {
		return fmt.Errorf("vital: silent historical rate must be in (0,1]")
	}
	if c.DormantMinimumAge <= 0 || c.DormantMaximumParticipations < 0 || strings.TrimSpace(c.ThresholdVersion) == "" {
		return fmt.Errorf("vital: invalid dormant or threshold configuration")
	}
	return nil
}

// Assessment is the pure input to the state machine. All detector verdicts
// are already computed from persisted facts. The booleans distinguish an
// absent opportunity from an unobservable source.
type Assessment struct {
	AssetID               string
	At                    time.Time
	PreviousState         State
	PreviousBrokenOverlay bool
	Archived              bool
	Restore               bool
	RequestResurrection   bool
	Resurrected           bool
	ResurrectionFailed    bool
	HasOpportunity        bool
	HasBaseline           bool
	ParticipationObserved bool
	NoOpportunity         bool
	Unobservable          bool
	Silent                detectors.Verdict
	Degraded              detectors.Verdict
	Broken                detectors.ReferenceVerdict
	Bypassed              detectors.Verdict
	Dormant               detectors.Verdict
	Alignment             []Alignment
}

// Alignment is a factual ±3-day item around a transition. It deliberately
// has no cause/effect field or causal wording.
type Alignment struct {
	Kind       string    `json:"kind"`
	OccurredAt time.Time `json:"occurred_at"`
	Summary    string    `json:"summary"`
	LocatorRef string    `json:"locator_ref,omitempty"`
}

type Decision struct {
	AssetID          string
	State            State
	BrokenOverlay    bool
	Transition       bool
	Alert            bool
	Resurrection     bool
	From             State
	Reason           string
	Rule             string
	DetectorVersion  string
	SchemaVersion    string
	ThresholdVersion string
	Evidence         map[string]any
	Alignment        []Alignment
}

type Machine struct {
	config Config
}

func NewMachine(config Config) *Machine {
	if config.ThresholdVersion == "" {
		config.ThresholdVersion = DefaultThresholdVersion
	}
	return &Machine{config: config}
}

func (m *Machine) Config() Config { return m.config }

// Decide applies the documented priority order and returns a transition
// projection. It does not persist anything.
func (m *Machine) Decide(in Assessment) (Decision, error) {
	if m == nil {
		return Decision{}, fmt.Errorf("vital: machine is required")
	}
	if err := m.config.Validate(); err != nil {
		return Decision{}, err
	}
	if strings.TrimSpace(in.AssetID) == "" {
		return Decision{}, fmt.Errorf("vital: asset id is required")
	}
	if in.At.IsZero() || in.At.Location() != time.UTC {
		return Decision{}, fmt.Errorf("vital: assessment time must be UTC")
	}
	if in.PreviousState != "" && !in.PreviousState.Valid() {
		return Decision{}, fmt.Errorf("vital: invalid previous state %q", in.PreviousState)
	}
	if in.NoOpportunity && in.Unobservable {
		return Decision{}, fmt.Errorf("vital: no_opportunity and unobservable are distinct states")
	}
	if err := validateAlignment(in.Alignment); err != nil {
		return Decision{}, err
	}

	previous := in.PreviousState
	decision := Decision{
		AssetID:          in.AssetID,
		From:             previous,
		DetectorVersion:  MachineVersion,
		SchemaVersion:    MachineSchemaVersion,
		ThresholdVersion: m.config.ThresholdVersion,
		Evidence: map[string]any{
			"silent":   in.Silent,
			"degraded": in.Degraded,
			"broken":   in.Broken,
			"bypassed": in.Bypassed,
			"dormant":  in.Dormant,
		},
		Alignment: append([]Alignment(nil), in.Alignment...),
	}

	// Archive is a user declaration and therefore wins over derived evidence.
	// Restore is the only path out of archived, and it intentionally re-enters
	// through dormant until fresh observations establish a participation state.
	if in.Archived || (previous == StateArchived && !in.Restore) {
		decision.State = StateArchived
		decision.Reason = "asset is archived by user disposition"
		decision.Rule = "archived is retained until an explicit restore action"
		return finishDecision(decision, in.PreviousBrokenOverlay), nil
	}
	if previous == StateArchived && in.Restore {
		decision.State = StateDormant
		decision.Reason = "asset was explicitly restored; awaiting a fresh observable opportunity"
		decision.Rule = "archived -> dormant only after explicit restore confirmation"
		return finishDecision(decision, false), nil
	}

	if in.RequestResurrection && (resurrectionEligible(previous) || in.PreviousBrokenOverlay) {
		decision.State = StateAwaitingResurrection
		decision.Reason = "asset modification was recorded; awaiting the first observable participation"
		decision.Rule = "modified asset remains awaiting_resurrection until one qualifying participation"
		return finishDecision(decision, false), nil
	}

	if previous == StateAwaitingResurrection {
		switch {
		case in.Resurrected:
			decision.State = StateHealthy
			decision.Resurrection = true
			decision.Reason = "resurrection observed: first qualifying participation after modification"
			decision.Rule = "awaiting_resurrection -> healthy on first qualifying participation"
		case in.ResurrectionFailed:
			decision.State = StateSilent
			decision.Resurrection = true
			decision.Reason = fmt.Sprintf("resurrection not observed after %d opportunities", m.config.ResurrectionFailureOpportunities)
			decision.Rule = fmt.Sprintf("awaiting_resurrection -> silent after %d known opportunities with no qualifying participation", m.config.ResurrectionFailureOpportunities)
		default:
			decision.State = StateAwaitingResurrection
			decision.Reason = "awaiting the first qualifying participation after modification"
			decision.Rule = "awaiting_resurrection requires one qualifying participation or the configured failure boundary"
		}
		return finishDecision(decision, false), nil
	}

	// Broken content is orthogonal to participation. Once a primary state
	// exists, preserve it and set the overlay; a brand-new asset uses broken as
	// its primary state until a participation state is available.
	if in.Broken.Triggered {
		if previous == "" || previous == StateBroken {
			decision.State = StateBroken
		} else {
			decision.State = previous
		}
		decision.BrokenOverlay = true
		decision.Reason = in.Broken.Summary
		decision.Rule = in.Broken.Rule
		return finishDecision(decision, in.PreviousBrokenOverlay), nil
	}

	decision.State = choosePrimary(in)
	if previous == StateBroken {
		// A broken primary state has no remembered participation state in the
		// schema; healthy is the honest recovery default unless another detector
		// supplies a more specific state.
		if decision.State == StateBroken {
			decision.State = StateHealthy
		}
	}
	decision.BrokenOverlay = false
	decision.Reason, decision.Rule = primaryReason(decision.State, in)
	return finishDecision(decision, in.PreviousBrokenOverlay), nil
}

func choosePrimary(in Assessment) State {
	if in.Bypassed.Triggered && in.Bypassed.Observable {
		return StateBypassed
	}
	if in.Silent.Triggered && in.Silent.Observable {
		return StateSilent
	}
	if in.Dormant.Triggered && in.Dormant.Observable {
		return StateDormant
	}
	// An absent denominator is a separate state from dormant. Dormant is only
	// selected after its age/low-use detector has triggered; a newly scanned
	// asset with no related task record must remain explicitly unclassified.
	if in.NoOpportunity {
		return StateNoOpportunity
	}
	if in.Unobservable {
		return StateUnobservable
	}
	// ADR-26: the first evaluation lands on the evidence. A newly registered
	// asset whose assessment already carries participation starts healthy;
	// only one with an opportunity and no recorded participation takes the
	// neutral dormant start until evidence arrives. The unconditional dormant
	// stopover painted 12 skills as "几乎未使用" for one import cycle on real
	// history while their participations were already in the store.
	if in.PreviousState == "" {
		if in.ParticipationObserved {
			return StateHealthy
		}
		return StateDormant
	}
	if in.Degraded.Triggered && in.Degraded.Observable {
		return StateDegraded
	}
	if in.PreviousState == StateDormant && !in.ParticipationObserved && !in.HasBaseline {
		return StateDormant
	}
	return StateHealthy
}

func primaryReason(state State, in Assessment) (string, string) {
	switch state {
	case StateBypassed:
		return in.Bypassed.Summary, in.Bypassed.Rule
	case StateSilent:
		return in.Silent.Summary, in.Silent.Rule
	case StateDormant:
		return in.Dormant.Summary, in.Dormant.Rule
	case StateNoOpportunity:
		return "no same-shape opportunity is recorded in the current window", "opportunity denominator is absent; no opportunity is not zero participation"
	case StateUnobservable:
		return "participation is unobservable for the current source", "source capability does not provide a participation observation"
	case StateDegraded:
		return in.Degraded.Summary, in.Degraded.Rule
	case StateHealthy:
		if in.ParticipationObserved {
			return "participation is observed and no higher-priority detector is triggered", "no silent, broken, bypass, dormant, or degraded detector is triggered"
		}
		return "no higher-priority detector is triggered", "no silent, broken, bypass, dormant, or degraded detector is triggered"
	default:
		return "state is retained", "state machine retained the current state"
	}
}

func finishDecision(decision Decision, previousOverlay bool) Decision {
	decision.Transition = decision.State != decision.From || decision.BrokenOverlay != previousOverlay
	decision.Alert = decision.Transition && alertFor(decision.State, decision.BrokenOverlay, decision.Resurrection)
	// A healthy alert means recovery, and a first-ever state recovered from
	// nothing: without this, ADR-26's evidence-first start would fire one
	// recovery alert per healthy asset on a first import. A first evaluation
	// that lands on silent or broken still alerts — that is the backtest
	// lighting the wall and speaking, which is the monitor's job.
	if decision.From == "" && decision.State == StateHealthy && !decision.BrokenOverlay {
		decision.Alert = false
	}
	if decision.State == StateArchived {
		decision.Alert = false
	}
	return decision
}

func alertFor(state State, brokenOverlay, resurrection bool) bool {
	if resurrection {
		return true
	}
	if brokenOverlay || state == StateSilent || state == StateBroken || state == StateBypassed || state == StateDegraded || state == StateHealthy {
		return true
	}
	return false
}

func resurrectionEligible(state State) bool {
	return state == StateSilent || state == StateBroken || state == StateBypassed
}

// CanTransition is the explicit state-map guard. Same-state is allowed for a
// broken overlay change; persistence decides whether a row is needed.
func CanTransition(from, to State) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	if from == to {
		return true
	}
	switch from {
	case StateDormant:
		return to == StateHealthy || to == StateArchived || to == StateBroken || to == StateNoOpportunity || to == StateUnobservable
	case StateHealthy:
		return to == StateDegraded || to == StateSilent || to == StateBypassed || to == StateBroken || to == StateNoOpportunity || to == StateUnobservable || to == StateArchived
	case StateDegraded:
		return to == StateHealthy || to == StateSilent || to == StateBypassed || to == StateBroken || to == StateNoOpportunity || to == StateUnobservable || to == StateArchived
	case StateSilent:
		return to == StateAwaitingResurrection || to == StateHealthy || to == StateBroken || to == StateArchived || to == StateNoOpportunity || to == StateUnobservable
	case StateBroken:
		return to == StateAwaitingResurrection || to == StateHealthy || to == StateSilent || to == StateBypassed || to == StateNoOpportunity || to == StateUnobservable || to == StateArchived
	case StateBypassed:
		return to == StateHealthy || to == StateBroken || to == StateSilent || to == StateNoOpportunity || to == StateUnobservable || to == StateArchived
	case StateNoOpportunity:
		return to == StateHealthy || to == StateDormant || to == StateUnobservable || to == StateBroken || to == StateArchived
	case StateUnobservable:
		return to == StateHealthy || to == StateNoOpportunity || to == StateDormant || to == StateBroken || to == StateArchived
	case StateAwaitingResurrection:
		return to == StateHealthy || to == StateSilent || to == StateBroken || to == StateArchived
	case StateArchived:
		return to == StateDormant
	default:
		return false
	}
}

func validateAlignment(items []Alignment) error {
	for _, item := range items {
		if item.OccurredAt.IsZero() || item.OccurredAt.Location() != time.UTC {
			return fmt.Errorf("vital: alignment occurred_at must be UTC")
		}
		if strings.TrimSpace(item.Kind) == "" || strings.TrimSpace(item.Summary) == "" {
			return fmt.Errorf("vital: alignment kind and summary are required")
		}
		lower := strings.ToLower(item.Summary)
		if strings.Contains(lower, "cause") || strings.Contains(item.Summary, "导致") || strings.Contains(lower, "because") {
			return fmt.Errorf("vital: alignment must not contain causal language")
		}
	}
	return nil
}
