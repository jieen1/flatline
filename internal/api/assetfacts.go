package api

import (
	"context"
	"database/sql"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"flatline/internal/vital"
)

type sparkPoint struct {
	At    time.Time `json:"at"`
	Value *int      `json:"value"`
}

type changeMarker struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
}

// assetFacts contains only persisted counts and timestamps. An empty
// collection means that no corresponding fact was recorded; it is not
// converted into a synthetic opportunity or participation.

type assetFacts struct {
	VersionCount        int            `json:"version_count"`
	SessionCount        int            `json:"session_count"`
	OpportunityCount    int            `json:"opportunity_count"`
	ParticipationCount  int            `json:"participation_count"`
	SourceBytes         *int64         `json:"source_bytes,omitempty"`
	BaselineNumerator   *int           `json:"baseline_participation_numerator,omitempty"`
	BaselineDenominator *int           `json:"baseline_participation_denominator,omitempty"`
	CurrentNumerator    *int           `json:"current_participation_numerator,omitempty"`
	CurrentDenominator  *int           `json:"current_participation_denominator,omitempty"`
	LastParticipationAt *time.Time     `json:"last_participation_at,omitempty"`
	ObservationLevels   []string       `json:"observation_levels"`
	Sparkline           []sparkPoint   `json:"sparkline"`
	ChangeMarkers       []changeMarker `json:"change_markers"`
}

type funnelStepResponse struct {
	Signal            string   `json:"signal"`
	Numerator         *int     `json:"numerator,omitempty"`
	Denominator       *int     `json:"denominator,omitempty"`
	ObservationLevels []string `json:"observation_levels"`
}

type funnelWindowResponse struct {
	Name             string               `json:"name"`
	Basis            string               `json:"basis"`
	OpportunityCount *int                 `json:"opportunity_count,omitempty"`
	Steps            []funnelStepResponse `json:"steps"`
}

type funnelResponse struct {
	Current  funnelWindowResponse  `json:"current"`
	Baseline *funnelWindowResponse `json:"baseline,omitempty"`
	Note     string                `json:"note"`
}

func (s *Server) assetFacts(ctx context.Context, assetID string) (assetFacts, error) {
	return s.assetFactsWithMarkers(ctx, assetID, nil)
}

func (s *Server) assetFactsWithMarkers(ctx context.Context, assetID string, environmentMarkers []changeMarker) (assetFacts, error) {
	facts := assetFacts{ObservationLevels: make([]string, 0), Sparkline: make([]sparkPoint, 0), ChangeMarkers: make([]changeMarker, 0)}
	var sourcePath sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT source_path FROM assets WHERE id = ?`, assetID).Scan(&sourcePath); err != nil {
		return assetFacts{}, err
	}
	if sourcePath.Valid && strings.TrimSpace(sourcePath.String) != "" {
		if info, err := os.Stat(sourcePath.String); err == nil && !info.IsDir() {
			size := info.Size()
			facts.SourceBytes = &size
		}
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM asset_versions WHERE asset_id = ?`, assetID).Scan(&facts.VersionCount); err != nil {
		return assetFacts{}, err
	}
	var last sql.NullString
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT p.session_id), COUNT(*), MAX(p.occurred_at)
		FROM participations p JOIN asset_versions av ON av.id = p.asset_version_id AND p.superseded_at IS NULL
		WHERE av.asset_id = ?`, assetID).Scan(&facts.SessionCount, &facts.ParticipationCount, &last); err != nil {
		return assetFacts{}, err
	}
	if last.Valid && last.String != "" {
		parsed, err := time.Parse(time.RFC3339Nano, last.String)
		if err != nil {
			return assetFacts{}, err
		}
		facts.LastParticipationAt = &parsed
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM opportunities WHERE asset_id = ? AND superseded_at IS NULL`, assetID).Scan(&facts.OpportunityCount); err != nil {
		return assetFacts{}, err
	}
	comparisonItems, err := s.assetComparisonItems(ctx, assetID)
	if err != nil {
		return assetFacts{}, err
	}
	applyAssetComparison(comparisonItems, &facts)
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT p.observation_level
		FROM participations p JOIN asset_versions av ON av.id = p.asset_version_id AND p.superseded_at IS NULL
		WHERE av.asset_id = ? ORDER BY p.observation_level`, assetID)
	if err != nil {
		return assetFacts{}, err
	}
	for rows.Next() {
		var level string
		if err := rows.Scan(&level); err != nil {
			rows.Close()
			return assetFacts{}, err
		}
		facts.ObservationLevels = append(facts.ObservationLevels, level)
	}
	if err := finishRows(rows); err != nil {
		return assetFacts{}, err
	}
	rateObservations := make([]rateObservation, 0, len(comparisonItems))
	for _, item := range comparisonItems {
		rateObservations = append(rateObservations, rateObservation{At: item.At, Participated: item.Participated})
	}
	facts.Sparkline = makeParticipationRateSparkline(rateObservations)
	versionRows, err := s.db.QueryContext(ctx, `SELECT observed_at FROM asset_versions WHERE asset_id = ? ORDER BY observed_at, id`, assetID)
	if err != nil {
		return assetFacts{}, err
	}
	for versionRows.Next() {
		var observed string
		if err := versionRows.Scan(&observed); err != nil {
			versionRows.Close()
			return assetFacts{}, err
		}
		parsed, err := time.Parse(time.RFC3339Nano, observed)
		if err != nil {
			versionRows.Close()
			return assetFacts{}, err
		}
		facts.ChangeMarkers = append(facts.ChangeMarkers, changeMarker{At: parsed, Kind: "asset"})
	}
	if err := versionRows.Err(); err != nil {
		versionRows.Close()
		return assetFacts{}, err
	}
	if err := versionRows.Close(); err != nil {
		return assetFacts{}, err
	}
	// Environment anchors are session-level facts, not asset facts. They are
	// intentionally shown on every asset sparkline so the time alignment is
	// visible without assigning the environment change to a specific asset.
	if environmentMarkers == nil {
		environmentMarkers, err = s.environmentChangeMarkers(ctx)
		if err != nil {
			return assetFacts{}, err
		}
	}
	facts.ChangeMarkers = append(facts.ChangeMarkers, environmentMarkers...)
	return facts, nil
}

func (s *Server) environmentChangeMarkers(ctx context.Context) ([]changeMarker, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT occurred_at FROM events WHERE event_type = 'environment_changed' AND occurred_at IS NOT NULL AND occurred_at <> '' ORDER BY occurred_at, id`)
	if err != nil {
		return nil, err
	}
	markers := make([]changeMarker, 0)
	for rows.Next() {
		var occurred string
		if err := rows.Scan(&occurred); err != nil {
			rows.Close()
			return nil, err
		}
		parsed, err := time.Parse(time.RFC3339Nano, occurred)
		if err != nil {
			rows.Close()
			return nil, err
		}
		markers = append(markers, changeMarker{At: parsed, Kind: "environment"})
	}
	if err := finishRows(rows); err != nil {
		return nil, err
	}
	return markers, nil
}

// compactEnvironmentMarkers keeps the temporal span of a high-volume shared
// marker series while bounding the payload and DOM work performed by every
// wall row. The first and last observations are always retained, and the
// remaining observations are selected deterministically across the interval.

func compactEnvironmentMarkers(markers []changeMarker, max int) []changeMarker {
	if max <= 0 || len(markers) == 0 {
		return nil
	}
	if len(markers) <= max {
		return append([]changeMarker(nil), markers...)
	}
	if max == 1 {
		return []changeMarker{markers[len(markers)-1]}
	}
	out := make([]changeMarker, 0, max)
	last := len(markers) - 1
	denominator := max - 1
	for index := 0; index < max; index++ {
		markerIndex := (index*last + denominator/2) / denominator
		out = append(out, markers[markerIndex])
	}
	return out
}

type assetComparisonOpportunity struct {
	ShapeClass   string
	At           time.Time
	Source       string
	Participated bool
}

// assetComparison provides the compact wall metric without collapsing
// unknown task shapes into zero. It uses the latest recorded shape class for
// the asset, then compares its older known opportunities with the latest
// configured window.

func (s *Server) assetComparison(ctx context.Context, assetID string, facts *assetFacts) error {
	items, err := s.assetComparisonItems(ctx, assetID)
	if err != nil {
		return err
	}
	applyAssetComparison(items, facts)
	return nil
}

func (s *Server) assetComparisonItems(ctx context.Context, assetID string) ([]assetComparisonOpportunity, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT o.shape_class, o.detected_at, s.source,
		       CASE WHEN EXISTS (
				SELECT 1 FROM participations p JOIN asset_versions av ON av.id = p.asset_version_id AND p.superseded_at IS NULL
				WHERE p.session_id = o.session_id AND av.asset_id = o.asset_id
		       ) THEN 1 ELSE 0 END
		FROM opportunities o JOIN sessions s ON s.id = o.session_id AND o.superseded_at IS NULL
		WHERE o.asset_id = ? ORDER BY o.detected_at, o.id`, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]assetComparisonOpportunity, 0)
	for rows.Next() {
		var item assetComparisonOpportunity
		var detected string
		var participated int
		if err := rows.Scan(&item.ShapeClass, &detected, &item.Source, &participated); err != nil {
			return nil, err
		}
		item.At, err = time.Parse(time.RFC3339Nano, detected)
		if err != nil {
			return nil, err
		}
		item.Participated = participated != 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) > 0 {
		shape := items[len(items)-1].ShapeClass
		filtered := items[:0]
		for _, item := range items {
			if item.ShapeClass == shape {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	return items, nil
}

func applyAssetComparison(items []assetComparisonOpportunity, facts *assetFacts) {
	facts.BaselineNumerator = nil
	facts.BaselineDenominator = nil
	facts.CurrentNumerator = nil
	facts.CurrentDenominator = nil
	if len(items) == 0 {
		return
	}
	currentStart := len(items) - vital.DefaultConfig().SilentConsecutiveOpportunities
	if currentStart < 0 {
		currentStart = 0
	}
	setWindow := func(window []assetComparisonOpportunity) (*int, *int) {
		denominator, numerator := 0, 0
		for _, item := range window {
			if item.Source != "claude_code" && item.Source != "codex" {
				continue
			}
			denominator++
			if item.Participated {
				numerator++
			}
		}
		if denominator == 0 {
			return nil, nil
		}
		return intPointer(numerator), intPointer(denominator)
	}
	facts.BaselineNumerator, facts.BaselineDenominator = setWindow(items[:currentStart])
	facts.CurrentNumerator, facts.CurrentDenominator = setWindow(items[currentStart:])
}

type funnelOpportunityFact struct {
	SessionID  string
	Source     string
	ShapeClass string
	At         time.Time
}

type funnelParticipationFact struct {
	SessionID string
	Signal    string
	Level     string
}

// assetFunnel builds a factual current-vs-baseline view from the same
// opportunity boundary used by the VSM: the latest configured number of
// opportunities are current, and older opportunities are the comparison
// window. A missing numerator remains null when the source did not record that
// participation form; it is never silently rendered as zero.

func (s *Server) assetFunnel(ctx context.Context, assetID string) (funnelResponse, error) {
	opportunityRows, err := s.db.QueryContext(ctx, `
		SELECT o.session_id, s.source, o.shape_class, o.detected_at
		FROM opportunities o JOIN sessions s ON s.id = o.session_id AND o.superseded_at IS NULL
		WHERE o.asset_id = ? ORDER BY o.detected_at, o.id`, assetID)
	if err != nil {
		return funnelResponse{}, err
	}
	opportunities := make([]funnelOpportunityFact, 0)
	for opportunityRows.Next() {
		var item funnelOpportunityFact
		var detected string
		if err := opportunityRows.Scan(&item.SessionID, &item.Source, &item.ShapeClass, &detected); err != nil {
			opportunityRows.Close()
			return funnelResponse{}, err
		}
		item.At, err = time.Parse(time.RFC3339Nano, detected)
		if err != nil {
			opportunityRows.Close()
			return funnelResponse{}, err
		}
		opportunities = append(opportunities, item)
	}
	if err := opportunityRows.Err(); err != nil {
		opportunityRows.Close()
		return funnelResponse{}, err
	}
	if err := opportunityRows.Close(); err != nil {
		return funnelResponse{}, err
	}
	if len(opportunities) > 0 {
		latestShape := opportunities[len(opportunities)-1].ShapeClass
		filtered := opportunities[:0]
		for _, opportunity := range opportunities {
			if opportunity.ShapeClass == latestShape {
				filtered = append(filtered, opportunity)
			}
		}
		opportunities = filtered
	}

	participationRows, err := s.db.QueryContext(ctx, `
		SELECT p.session_id, p.participation_signal, p.observation_level
		FROM participations p JOIN asset_versions av ON av.id = p.asset_version_id AND p.superseded_at IS NULL
		WHERE av.asset_id = ? ORDER BY p.occurred_at, p.id`, assetID)
	if err != nil {
		return funnelResponse{}, err
	}
	participations := make([]funnelParticipationFact, 0)
	for participationRows.Next() {
		var item funnelParticipationFact
		if err := participationRows.Scan(&item.SessionID, &item.Signal, &item.Level); err != nil {
			participationRows.Close()
			return funnelResponse{}, err
		}
		participations = append(participations, item)
	}
	if err := participationRows.Err(); err != nil {
		participationRows.Close()
		return funnelResponse{}, err
	}
	if err := participationRows.Close(); err != nil {
		return funnelResponse{}, err
	}

	currentStart := len(opportunities) - vital.DefaultConfig().SilentConsecutiveOpportunities
	if currentStart < 0 {
		currentStart = 0
	}
	current := buildFunnelWindow("当前窗口", opportunities[currentStart:], participations)
	var baseline *funnelWindowResponse
	if currentStart > 0 {
		value := buildFunnelWindow("已记录基线", opportunities[:currentStart], participations)
		baseline = &value
	}
	return funnelResponse{
		Current:  current,
		Baseline: baseline,
		Note:     "当前窗口取最近 8 个相关任务；更早的相关任务才会进入基线。没有相关任务记录时不计算参与率。",
	}, nil
}

func buildFunnelWindow(name string, opportunities []funnelOpportunityFact, participations []funnelParticipationFact) funnelWindowResponse {
	knownSessions := make(map[string]struct{})
	windowSessions := make(map[string]struct{})
	for _, opportunity := range opportunities {
		windowSessions[opportunity.SessionID] = struct{}{}
		if opportunity.Source == "claude_code" || opportunity.Source == "codex" {
			knownSessions[opportunity.SessionID] = struct{}{}
		}
	}
	if len(opportunities) == 0 {
		// With no task-shape denominator, participation rows are still shown as
		// observations, but the denominator stays unknown.
		for _, participation := range participations {
			windowSessions[participation.SessionID] = struct{}{}
		}
	}
	var denominator *int
	if len(knownSessions) > 0 {
		value := len(knownSessions)
		denominator = &value
	}
	steps := make([]funnelStepResponse, 0, 4)
	for _, signal := range []string{"offered", "loaded", "invoked", "observed-use", "followed"} {
		sessions := make(map[string]struct{})
		levels := make(map[string]struct{})
		for _, participation := range participations {
			if _, ok := windowSessions[participation.SessionID]; !ok || participation.Signal != signal {
				continue
			}
			sessions[participation.SessionID] = struct{}{}
			if participation.Level != "" {
				levels[participation.Level] = struct{}{}
			}
		}
		var numerator *int
		if len(sessions) > 0 {
			value := len(sessions)
			numerator = &value
		}
		observedLevels := make([]string, 0, len(levels))
		for level := range levels {
			observedLevels = append(observedLevels, level)
		}
		sort.Strings(observedLevels)
		steps = append(steps, funnelStepResponse{Signal: signal, Numerator: numerator, Denominator: denominator, ObservationLevels: observedLevels})
	}
	basis := "没有相关任务记录，无法建立任务分母。"
	if len(opportunities) > 0 {
		basis = "基于 " + strconv.Itoa(len(opportunities)) + " 个已记录相关任务；只对来源可提供参与判定的会话计算分母。"
	}
	return funnelWindowResponse{Name: name, Basis: basis, OpportunityCount: denominator, Steps: steps}
}

type rateObservation struct {
	At           time.Time
	Participated bool
}

func makeParticipationRateSparkline(observations []rateObservation) []sparkPoint {
	if len(observations) == 0 {
		return []sparkPoint{}
	}
	ordered := append([]rateObservation(nil), observations...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].At.Before(ordered[j].At) })
	if len(ordered) == 1 || ordered[0].At.Equal(ordered[len(ordered)-1].At) {
		return []sparkPoint{{At: ordered[0].At, Value: rateValue(ordered)}}
	}
	const buckets = 12
	start, end := ordered[0].At, ordered[len(ordered)-1].At
	span := end.Sub(start)
	numerators := make([]int, buckets)
	denominators := make([]int, buckets)
	for _, observation := range ordered {
		bucket := int(float64(observation.At.Sub(start)) / float64(span) * float64(buckets-1))
		if bucket < 0 {
			bucket = 0
		}
		if bucket >= buckets {
			bucket = buckets - 1
		}
		denominators[bucket]++
		if observation.Participated {
			numerators[bucket]++
		}
	}
	out := make([]sparkPoint, buckets)
	for i := range denominators {
		var value *int
		if denominators[i] > 0 {
			rounded := (numerators[i]*100 + denominators[i]/2) / denominators[i]
			value = &rounded
		}
		out[i] = sparkPoint{At: start.Add(time.Duration(float64(span) * float64(i) / float64(buckets-1))), Value: value}
	}
	return out
}

func rateValue(observations []rateObservation) *int {
	if len(observations) == 0 {
		return nil
	}
	numerator := 0
	for _, observation := range observations {
		if observation.Participated {
			numerator++
		}
	}
	rounded := (numerator*100 + len(observations)/2) / len(observations)
	return &rounded
}
