// Package runtime is the daemon-owned application composition root. It turns
// persisted P3 facts into detector inputs and submits them to the single VSM
// repository. No source files are written here.
package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"sync"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/assets"
	"flatline/internal/canonical"
	"flatline/internal/detectors"
	"flatline/internal/history"
	"flatline/internal/ingest"
	"flatline/internal/storage"
	"flatline/internal/vital"
)

type App struct {
	db          *storage.DB
	registry    *assets.Registry
	snapshotter *assets.Snapshotter
	pipeline    *ingest.Pipeline
	states      *vital.Repository
	machine     *vital.Machine
	nativeMu    sync.Mutex
	nativeFiles map[string]history.FileStamp
}

func New(db *storage.DB, sourceRegistry *adapters.Registry, config vital.Config) *App {
	if sourceRegistry == nil {
		sourceRegistry = adapters.NewRegistry()
	}
	assetRegistry := assets.New(db)
	machine := vital.NewMachine(config)
	return &App{db: db, registry: assetRegistry, snapshotter: assets.NewSnapshotter(assetRegistry), pipeline: ingest.NewPipeline(db, sourceRegistry), states: vital.NewRepository(db, machine), machine: machine, nativeFiles: make(map[string]history.FileStamp)}
}

func (a *App) Pipeline() *ingest.Pipeline { return a.pipeline }
func (a *App) States() *vital.Repository  { return a.states }

type ScanReport struct {
	Root              string
	Discovered        int
	SnapshotsObserved int
	VersionsCreated   int
}

// NativeHistoryReport is the daemon-facing summary of a read-only native
// history pass. A session that cannot be normalized is reported as a warning;
// one malformed historical file must not prevent other local sessions from
// being imported.
type NativeHistoryReport struct {
	FilesSeen          int
	FilesRead          int
	FilesSkipped       int
	SessionsFound      int
	SessionsNormalized int
	SessionsIngested   int
	AssetEvidenceFound int
	EventsInserted     int
	Warnings           []string
}

// ImportNativeHistory discovers native Claude Code and Codex JSONL files and
// replays them through the same canonical pipeline as explicit adapter input.
// It reads only the configured roots and never writes to those roots.
func (a *App) ImportNativeHistory(ctx context.Context, config history.Config) (NativeHistoryReport, error) {
	if a == nil || a.pipeline == nil || a.registry == nil {
		return NativeHistoryReport{}, fmt.Errorf("runtime: native history import is not wired")
	}
	assetsList, err := a.registry.List(ctx)
	if err != nil {
		return NativeHistoryReport{}, fmt.Errorf("runtime: list assets for native history: %w", err)
	}
	config.Assets = assetsList
	a.nativeMu.Lock()
	knownFiles := make(map[string]history.FileStamp, len(a.nativeFiles))
	for path, stamp := range a.nativeFiles {
		knownFiles[path] = stamp
	}
	a.nativeMu.Unlock()
	config.KnownFiles = knownFiles
	sessions, discovered, err := history.Discover(config)
	if err != nil {
		return NativeHistoryReport{}, err
	}
	report := NativeHistoryReport{
		FilesSeen: discovered.FilesSeen, FilesRead: discovered.FilesRead, FilesSkipped: discovered.FilesSkipped,
		SessionsFound: discovered.SessionsFound, SessionsNormalized: discovered.SessionsNormalized,
		AssetEvidenceFound: discovered.AssetEvidenceFound,
		Warnings:           append([]string(nil), discovered.Warnings...),
	}
	a.nativeMu.Lock()
	for path, stamp := range discovered.FileStamps {
		a.nativeFiles[path] = stamp
	}
	a.nativeMu.Unlock()
	for _, session := range sessions {
		result, err := a.pipeline.Ingest(ctx, session.Input)
		if err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: ingest: %v", session.SourcePath, err))
			continue
		}
		report.SessionsIngested++
		report.EventsInserted += result.EventsInserted
	}
	return report, nil
}

// ScanAssets performs an explicit read-only scan of root. It uses os.DirFS
// only as a reader and never edits, renames, disables, or removes a source
// file. The caller can run EvaluateAll afterwards to update states.
func (a *App) ScanAssets(ctx context.Context, root string, scope assets.Scope, observedAt time.Time) (ScanReport, error) {
	if a == nil || a.snapshotter == nil {
		return ScanReport{}, fmt.Errorf("runtime: snapshotter is not wired")
	}
	if root == "" {
		return ScanReport{}, fmt.Errorf("runtime: asset scan root is required")
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	if observedAt.Location() != time.UTC {
		return ScanReport{}, fmt.Errorf("runtime: asset scan time must be UTC")
	}
	return a.scanFS(ctx, os.DirFS(root), root, scope, observedAt)
}

func (a *App) scanFS(ctx context.Context, files fs.FS, root string, scope assets.Scope, observedAt time.Time) (ScanReport, error) {
	discovered, err := assets.Discover(files, assets.DiscoveryOptions{Scope: scope, RootLabel: root, FirstSeenAt: observedAt, ObservedAt: observedAt, ObservationLevel: canonical.LevelLoaded, ContentRefPrefix: root})
	if err != nil {
		return ScanReport{Root: root}, err
	}
	report := ScanReport{Root: root, Discovered: len(discovered)}
	for _, item := range discovered {
		version, err := a.snapshotter.SnapshotFile(ctx, files, item.Asset, assets.FileSnapshotInput{Path: item.Path, ObservationLevel: canonical.LevelLoaded, ObservedAt: observedAt, ContentRef: item.ContentRef})
		if err != nil {
			return report, fmt.Errorf("runtime: snapshot discovered %s: %w", item.Path, err)
		}
		report.SnapshotsObserved++
		if version.Created {
			report.VersionsCreated++
		}
		content, err := fs.ReadFile(files, item.Path)
		if err != nil {
			return report, fmt.Errorf("runtime: read discovered content for reference check %s: %w", item.Path, err)
		}
		references := detectors.ExtractReferences(content)
		checker := detectors.NewLocalReferenceChecker(root)
		observations, err := detectors.ObserveReferences(references, checker)
		if err != nil {
			return report, fmt.Errorf("runtime: extract references %s: %w", item.Path, err)
		}
		if err := a.recordReferenceCheck(ctx, version.ID, item.Asset.ID(), observedAt, observations); err != nil {
			return report, err
		}
	}
	return report, nil
}

func (a *App) recordReferenceCheck(ctx context.Context, versionID int64, assetID string, checkedAt time.Time, observations []detectors.ReferenceObservation) error {
	checked, failed, unknown := 0, 0, 0
	for _, observation := range observations {
		if !observation.Known || observation.Exists == nil {
			unknown++
			continue
		}
		checked++
		if !*observation.Exists {
			failed++
		}
	}
	status := "ok"
	if failed > 0 {
		status = "failed"
	} else if checked == 0 && unknown > 0 {
		status = "unknown"
	} else if unknown > 0 {
		status = "partial"
	}
	result, err := a.db.ExecContext(ctx, `
		INSERT INTO reference_checks (asset_version_id, checked_at, overall_status, checker_version)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (asset_version_id, checked_at) DO NOTHING`, versionID, formatTime(checkedAt), status, "reference-checker/1")
	if err != nil {
		return fmt.Errorf("runtime: record reference check for %s: %w", assetID, err)
	}
	_ = result
	var checkID int64
	if err := a.db.QueryRowContext(ctx, `SELECT id FROM reference_checks WHERE asset_version_id = ? AND checked_at = ?`, versionID, formatTime(checkedAt)).Scan(&checkID); err != nil {
		return fmt.Errorf("runtime: load reference check for %s: %w", assetID, err)
	}
	for _, observation := range observations {
		var exists any
		if observation.Known && observation.Exists != nil {
			if *observation.Exists {
				exists = 1
			} else {
				exists = 0
			}
		}
		if _, err := a.db.ExecContext(ctx, `
			INSERT INTO reference_check_items (check_id, ref_kind, ref_value, "exists", detail)
			SELECT ?, ?, ?, ?, ?
			WHERE NOT EXISTS (
				SELECT 1 FROM reference_check_items WHERE check_id = ? AND ref_kind = ? AND ref_value = ?
			)`, checkID, observation.Kind, observation.Value, exists, observation.Detail, checkID, observation.Kind, observation.Value); err != nil {
			return fmt.Errorf("runtime: record reference item for %s: %w", assetID, err)
		}
	}
	return nil
}

func (a *App) IngestAndEvaluate(ctx context.Context, input ingest.SessionInput, asOf time.Time) (ingest.Report, []vital.Decision, error) {
	report, err := a.pipeline.Ingest(ctx, input)
	if err != nil {
		return report, nil, err
	}
	decisions, err := a.EvaluateAll(ctx, asOf)
	return report, decisions, err
}

// EvaluateAll recomputes current derived states from the persisted P3 facts.
// The operation is deterministic for a fixed asOf and database content.
func (a *App) EvaluateAll(ctx context.Context, asOf time.Time) ([]vital.Decision, error) {
	if a == nil || a.db == nil || a.registry == nil || a.states == nil || a.machine == nil {
		return nil, fmt.Errorf("runtime: app is not fully wired")
	}
	if asOf.IsZero() {
		asOf = time.Now().UTC()
	}
	if asOf.Location() != time.UTC {
		return nil, fmt.Errorf("runtime: as_of must be UTC")
	}
	assetsList, err := a.registry.List(ctx)
	if err != nil {
		return nil, err
	}
	decisions := make([]vital.Decision, 0, len(assetsList))
	for _, asset := range assetsList {
		assessment, err := a.assessAsset(ctx, asset, asOf)
		if err != nil {
			return nil, fmt.Errorf("runtime: assess %s: %w", asset.ID, err)
		}
		decision, err := a.states.Apply(ctx, assessment)
		if err != nil {
			return nil, fmt.Errorf("runtime: persist %s: %w", asset.ID, err)
		}
		decisions = append(decisions, decision)
	}
	sort.Slice(decisions, func(i, j int) bool { return decisions[i].AssetID < decisions[j].AssetID })
	return decisions, nil
}

type opportunityFact struct {
	ID                 int64
	SessionID          string
	ShapeClass         string
	DetectedAt         time.Time
	Participated       bool
	ParticipationKnown bool
	Followed           bool
	Levels             []canonical.ObservationLevel
	LocatorRefs        []string
}

func (a *App) assessAsset(ctx context.Context, asset assets.Asset, asOf time.Time) (vital.Assessment, error) {
	current, err := a.states.Current(ctx, asset.ID)
	if err != nil {
		return vital.Assessment{}, err
	}
	opportunities, err := a.assetOpportunities(ctx, asset.ID, asOf)
	if err != nil {
		return vital.Assessment{}, err
	}
	// A task-shape rule change creates a new comparison population. Keep the
	// state detectors on the latest recorded shape, otherwise an old shape can
	// manufacture a baseline for a newer shape and emit a false silence or
	// degradation alert.
	opportunities = latestOpportunityShape(opportunities)
	resurrected, resurrectionFailed := resurrectionOutcome(current, opportunities, a.machine.Config())
	historyEnd := len(opportunities) - a.machine.Config().SilentConsecutiveOpportunities
	if historyEnd < 0 {
		historyEnd = 0
	}
	history := opportunities[:historyEnd]
	historicalOpportunities, historicalParticipations := knownCounts(history)
	recent := make([]detectors.OpportunityObservation, 0, len(opportunities))
	for _, opportunity := range opportunities {
		recent = append(recent, detectors.OpportunityObservation{ID: opportunity.ID, SessionID: opportunity.SessionID, DetectedAt: opportunity.DetectedAt, Participated: opportunity.Participated, ParticipationKnown: opportunity.ParticipationKnown, ObservationLevels: opportunity.Levels, LocatorRefs: opportunity.LocatorRefs})
	}
	silent, err := detectors.EvaluateSilent(detectors.SilentInput{AssetID: asset.ID, HistoricalOpportunityCount: historicalOpportunities, HistoricalParticipationCount: historicalParticipations, Recent: recent, RequiredRecentOpportunities: a.machine.Config().SilentConsecutiveOpportunities, MinimumHistoricalOpportunities: a.machine.Config().SilentMinimumHistoricalOpportunities, MinimumHistoricalRate: a.machine.Config().SilentMinimumHistoricalRate})
	if err != nil {
		return vital.Assessment{}, err
	}
	baselineRate := 0.0
	if historicalOpportunities > 0 {
		baselineRate = float64(historicalParticipations) / float64(historicalOpportunities)
	}
	recentForDegraded := recent
	if required := a.machine.Config().DegradedMinimumRecentOpportunities; len(recentForDegraded) > required {
		recentForDegraded = recentForDegraded[len(recentForDegraded)-required:]
	}
	degraded, err := detectors.EvaluateDegraded(detectors.DegradedInput{AssetID: asset.ID, BaselineRate: baselineRate, MinimumRecentOpportunities: a.machine.Config().DegradedMinimumRecentOpportunities, Recent: recentForDegraded})
	if err != nil {
		return vital.Assessment{}, err
	}
	cumulative, err := a.cumulativeParticipations(ctx, asset.ID)
	if err != nil {
		return vital.Assessment{}, err
	}
	dormant, err := detectors.EvaluateDormant(detectors.DormantInput{AssetID: asset.ID, FirstSeenAt: asset.FirstSeenAt, AsOf: asOf, CumulativeParticipations: cumulative, MinimumAge: a.machine.Config().DormantMinimumAge, MaximumParticipations: a.machine.Config().DormantMaximumParticipations})
	if err != nil {
		return vital.Assessment{}, err
	}
	broken, err := a.referenceVerdict(ctx, asset.ID, asOf)
	if err != nil {
		return vital.Assessment{}, err
	}
	bypassed, err := a.bypassVerdict(ctx, asset.ID, asOf)
	if err != nil {
		return vital.Assessment{}, err
	}
	alignment, err := a.alignment(ctx, asset.ID, asOf)
	if err != nil {
		return vital.Assessment{}, err
	}
	unknown := false
	participationObserved := false
	for _, opportunity := range opportunities {
		if !opportunity.ParticipationKnown {
			unknown = true
		}
		if opportunity.Participated {
			participationObserved = true
		}
	}
	return vital.Assessment{
		AssetID: asset.ID, At: asOf, HasOpportunity: len(opportunities) > 0,
		HasBaseline:           historicalOpportunities >= a.machine.Config().SilentMinimumHistoricalOpportunities && historicalOpportunities > 0 && float64(historicalParticipations)/float64(historicalOpportunities) >= a.machine.Config().SilentMinimumHistoricalRate,
		Archived:              asset.ArchivedAt != nil,
		Resurrected:           resurrected,
		ResurrectionFailed:    resurrectionFailed,
		ParticipationObserved: participationObserved, NoOpportunity: len(opportunities) == 0, Unobservable: len(opportunities) > 0 && unknown,
		Silent: silent, Degraded: degraded, Broken: broken,
		Bypassed: bypassed, Dormant: dormant, Alignment: alignment,
	}, nil
}

// alignment returns only records inside the documented ±3 day window around
// an assessment. These are temporal anchors for a transition, never causal
// explanations. Asset versions are solid markers; environment changes are
// inferred anchors with their original event locator retained.
func (a *App) alignment(ctx context.Context, assetID string, asOf time.Time) ([]vital.Alignment, error) {
	const window = 3 * 24 * time.Hour
	start, end := asOf.Add(-window), asOf.Add(window)
	out := make([]vital.Alignment, 0)
	versionRows, err := a.db.QueryContext(ctx, `
		SELECT id, observed_at
		FROM asset_versions
		WHERE asset_id = ? AND julianday(observed_at) BETWEEN julianday(?) AND julianday(?)
		ORDER BY observed_at, id`, assetID, formatTime(start), formatTime(end))
	if err != nil {
		return nil, fmt.Errorf("runtime: query asset alignment: %w", err)
	}
	for versionRows.Next() {
		var id int64
		var observed string
		if err := versionRows.Scan(&id, &observed); err != nil {
			versionRows.Close()
			return nil, fmt.Errorf("runtime: scan asset alignment: %w", err)
		}
		at, err := time.Parse(time.RFC3339Nano, observed)
		if err != nil {
			versionRows.Close()
			return nil, fmt.Errorf("runtime: parse asset alignment: %w", err)
		}
		out = append(out, vital.Alignment{Kind: "asset_version", OccurredAt: at, Summary: "资产版本变化已记录。", LocatorRef: fmt.Sprintf("asset_version:%d", id)})
	}
	if err := versionRows.Err(); err != nil {
		versionRows.Close()
		return nil, fmt.Errorf("runtime: iterate asset alignment: %w", err)
	}
	if err := versionRows.Close(); err != nil {
		return nil, fmt.Errorf("runtime: close asset alignment: %w", err)
	}

	environmentRows, err := a.db.QueryContext(ctx, `
		SELECT id, occurred_at, COALESCE(payload_json, '')
		FROM events
		WHERE event_type = 'environment_changed'
		  AND julianday(occurred_at) BETWEEN julianday(?) AND julianday(?)
		ORDER BY occurred_at, id`, formatTime(start), formatTime(end))
	if err != nil {
		return nil, fmt.Errorf("runtime: query environment alignment: %w", err)
	}
	for environmentRows.Next() {
		var id int64
		var occurred, payload string
		if err := environmentRows.Scan(&id, &occurred, &payload); err != nil {
			environmentRows.Close()
			return nil, fmt.Errorf("runtime: scan environment alignment: %w", err)
		}
		at, err := time.Parse(time.RFC3339Nano, occurred)
		if err != nil {
			environmentRows.Close()
			return nil, fmt.Errorf("runtime: parse environment alignment: %w", err)
		}
		summary := "环境变化已记录；具体字段见时间线。"
		var fields struct {
			Field string `json:"field"`
			From  string `json:"from"`
			To    string `json:"to"`
		}
		if json.Unmarshal([]byte(payload), &fields) == nil && fields.Field != "" {
			summary = "环境变化已记录：" + fields.Field
			if fields.From != "" && fields.To != "" {
				summary += "（" + fields.From + " → " + fields.To + "）"
			}
		}
		out = append(out, vital.Alignment{Kind: "environment_changed", OccurredAt: at, Summary: summary, LocatorRef: fmt.Sprintf("event:%d", id)})
	}
	if err := environmentRows.Err(); err != nil {
		environmentRows.Close()
		return nil, fmt.Errorf("runtime: iterate environment alignment: %w", err)
	}
	if err := environmentRows.Close(); err != nil {
		return nil, fmt.Errorf("runtime: close environment alignment: %w", err)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].OccurredAt.Equal(out[j].OccurredAt) {
			return out[i].Kind < out[j].Kind
		}
		return out[i].OccurredAt.Before(out[j].OccurredAt)
	})
	return out, nil
}

func (a *App) assetOpportunities(ctx context.Context, assetID string, asOf time.Time) ([]opportunityFact, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT o.id, o.session_id, o.shape_class, o.detected_at, s.source,
		       CASE WHEN EXISTS (
				SELECT 1 FROM participations p JOIN asset_versions av ON av.id = p.asset_version_id
				WHERE p.session_id = o.session_id AND av.asset_id = o.asset_id
		       ) THEN 1 ELSE 0 END
		FROM opportunities o JOIN sessions s ON s.id = o.session_id
		WHERE o.asset_id = ? AND julianday(o.detected_at) <= julianday(?)
		ORDER BY o.detected_at, o.id`, assetID, formatTime(asOf))
	if err != nil {
		return nil, fmt.Errorf("runtime: query opportunities: %w", err)
	}
	out := make([]opportunityFact, 0)
	for rows.Next() {
		var item opportunityFact
		var detected, source string
		var participated int
		if err := rows.Scan(&item.ID, &item.SessionID, &item.ShapeClass, &detected, &source, &participated); err != nil {
			return nil, fmt.Errorf("runtime: scan opportunity: %w", err)
		}
		item.DetectedAt, err = time.Parse(time.RFC3339Nano, detected)
		if err != nil {
			return nil, err
		}
		item.Participated = participated != 0
		item.ParticipationKnown = sourceHasExactInvocation(source)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("runtime: close opportunities: %w", err)
	}
	// storage.DB deliberately uses one SQLite connection. Finish iterating and
	// close the opportunity rows before querying participation evidence, or a
	// nested read would wait on the same connection indefinitely.
	for i := range out {
		out[i].Levels, out[i].LocatorRefs, out[i].Followed, err = a.participationEvidence(ctx, out[i].SessionID, assetID)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func latestOpportunityShape(opportunities []opportunityFact) []opportunityFact {
	if len(opportunities) == 0 {
		return opportunities
	}
	latestShape := opportunities[len(opportunities)-1].ShapeClass
	filtered := opportunities[:0]
	for _, opportunity := range opportunities {
		if opportunity.ShapeClass == latestShape {
			filtered = append(filtered, opportunity)
		}
	}
	return filtered
}

func (a *App) participationEvidence(ctx context.Context, sessionID, assetID string) ([]canonical.ObservationLevel, []string, bool, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT p.participation_signal, p.observation_level, COALESCE(p.locator_json, '')
		FROM participations p JOIN asset_versions av ON av.id = p.asset_version_id
		WHERE p.session_id = ? AND av.asset_id = ? ORDER BY p.id`, sessionID, assetID)
	if err != nil {
		return nil, nil, false, err
	}
	defer rows.Close()
	var levels []canonical.ObservationLevel
	var locators []string
	followed := false
	for rows.Next() {
		var signal, level, locator string
		if err := rows.Scan(&signal, &level, &locator); err != nil {
			return nil, nil, false, err
		}
		levels = append(levels, canonical.ObservationLevel(level))
		if locator != "" {
			locators = append(locators, locator)
		}
		if canonical.ParticipationSignal(signal) == canonical.SignalFollowed && (canonical.ObservationLevel(level) == canonical.LevelInvoked || canonical.ObservationLevel(level) == canonical.LevelObservedUse) {
			followed = true
		}
	}
	return levels, locators, followed, rows.Err()
}

func (a *App) cumulativeParticipations(ctx context.Context, assetID string) (int, error) {
	var count int
	err := a.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT p.session_id) FROM participations p JOIN asset_versions av ON av.id = p.asset_version_id WHERE av.asset_id = ?`, assetID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("runtime: cumulative participations: %w", err)
	}
	return count, nil
}

func (a *App) referenceVerdict(ctx context.Context, assetID string, asOf time.Time) (detectors.ReferenceVerdict, error) {
	var checkID, versionID int64
	var checkedAt string
	err := a.db.QueryRowContext(ctx, `
		SELECT rc.id, av.id, rc.checked_at
		FROM reference_checks rc JOIN asset_versions av ON av.id = rc.asset_version_id
		WHERE av.asset_id = ? AND julianday(rc.checked_at) <= julianday(?)
		ORDER BY rc.checked_at DESC, rc.id DESC LIMIT 1`, assetID, formatTime(asOf)).Scan(&checkID, &versionID, &checkedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return detectors.ReferenceVerdict{Verdict: detectors.Verdict{Detector: detectors.ReferenceDetector, Observable: false, ReasonCode: "reference_check_not_recorded", Rule: "known failed references / known checked references", Summary: "reference health is unknown; no check is recorded"}}, nil
		}
		return detectors.ReferenceVerdict{}, err
	}
	checked, err := time.Parse(time.RFC3339Nano, checkedAt)
	if err != nil {
		return detectors.ReferenceVerdict{}, err
	}
	rows, err := a.db.QueryContext(ctx, `SELECT ref_kind, ref_value, "exists", detail FROM reference_check_items WHERE check_id = ? ORDER BY id`, checkID)
	if err != nil {
		return detectors.ReferenceVerdict{}, err
	}
	defer rows.Close()
	items := make([]detectors.ReferenceObservation, 0)
	for rows.Next() {
		var kind, value string
		var detail sql.NullString
		var exists sql.NullInt64
		if err := rows.Scan(&kind, &value, &exists, &detail); err != nil {
			return detectors.ReferenceVerdict{}, err
		}
		var present *bool
		if exists.Valid {
			value := exists.Int64 != 0
			present = &value
		}
		items = append(items, detectors.ReferenceObservation{Kind: detectors.ReferenceKind(kind), Value: value, Exists: present, Known: exists.Valid, Detail: detail.String, LocatorRef: fmt.Sprintf("reference_check:%d", checkID)})
	}
	if err := rows.Err(); err != nil {
		return detectors.ReferenceVerdict{}, err
	}
	return detectors.EvaluateReferenceHealth(detectors.ReferenceInput{AssetID: assetID, AssetVersionID: versionID, CheckedAt: checked, Items: items})
}

type bypassEvent struct {
	ID         int64
	SessionID  string
	EventType  string
	Level      canonical.ObservationLevel
	Payload    string
	OccurredAt time.Time
}

// bypassVerdict reconstructs an invoked-then-violated chain from canonical
// events. A missing violation event remains unknown; absence is not treated as
// proof that the asset was followed.
func (a *App) bypassVerdict(ctx context.Context, assetID string, asOf time.Time) (detectors.Verdict, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT id, session_id, event_type, observation_level, COALESCE(payload_json, ''), occurred_at
		FROM events
		WHERE asset_id = ? AND occurred_at IS NOT NULL AND occurred_at <> ''
		  AND julianday(occurred_at) <= julianday(?)
		ORDER BY occurred_at, id`, assetID, formatTime(asOf))
	if err != nil {
		return detectors.Verdict{}, fmt.Errorf("runtime: query bypass evidence: %w", err)
	}
	defer rows.Close()
	var invocations []bypassEvent
	var violations []bypassEvent
	for rows.Next() {
		var item bypassEvent
		var level, occurred string
		if err := rows.Scan(&item.ID, &item.SessionID, &item.EventType, &level, &item.Payload, &occurred); err != nil {
			return detectors.Verdict{}, fmt.Errorf("runtime: scan bypass evidence: %w", err)
		}
		item.Level = canonical.ObservationLevel(level)
		item.OccurredAt, err = time.Parse(time.RFC3339Nano, occurred)
		if err != nil {
			return detectors.Verdict{}, fmt.Errorf("runtime: parse bypass evidence time: %w", err)
		}
		switch item.EventType {
		case canonical.EventTypeAssetInvoked:
			invocations = append(invocations, item)
		case canonical.EventTypeAssetViolation:
			var payload struct {
				Violated *bool `json:"violated"`
			}
			if err := json.Unmarshal([]byte(item.Payload), &payload); err != nil || payload.Violated == nil || !*payload.Violated {
				continue
			}
			violations = append(violations, item)
		}
	}
	if err := rows.Err(); err != nil {
		return detectors.Verdict{}, fmt.Errorf("runtime: iterate bypass evidence: %w", err)
	}
	var latest *bypassEvent
	for i := range invocations {
		latest = &invocations[i]
	}
	if len(violations) > 0 {
		for i := range violations {
			violation := &violations[i]
			var matching *bypassEvent
			for j := range invocations {
				candidate := &invocations[j]
				if candidate.SessionID != violation.SessionID || candidate.OccurredAt.After(violation.OccurredAt) {
					continue
				}
				if matching == nil || candidate.OccurredAt.After(matching.OccurredAt) || (candidate.OccurredAt.Equal(matching.OccurredAt) && candidate.ID > matching.ID) {
					matching = candidate
				}
			}
			if matching == nil {
				continue
			}
			verdict, err := detectors.EvaluateBypass(detectors.BypassInput{
				AssetID: assetID, OccurredAt: violation.OccurredAt, Violated: true,
				Invocation: detectors.EvidencePoint{Present: true, Level: matching.Level, LocatorRef: fmt.Sprintf("event:%d", matching.ID)},
				Violation:  detectors.EvidencePoint{Present: true, Level: violation.Level, LocatorRef: fmt.Sprintf("event:%d", violation.ID)},
			})
			if err != nil {
				return detectors.Verdict{}, err
			}
			if verdict.Triggered {
				return verdict, nil
			}
		}
	}
	if latest == nil {
		verdict, err := detectors.EvaluateBypass(detectors.BypassInput{AssetID: assetID, OccurredAt: asOf})
		if err != nil {
			return detectors.Verdict{}, err
		}
		return verdict, nil
	}
	verdict, err := detectors.EvaluateBypass(detectors.BypassInput{
		AssetID: assetID, OccurredAt: latest.OccurredAt,
		Invocation: detectors.EvidencePoint{Present: true, Level: latest.Level, LocatorRef: fmt.Sprintf("event:%d", latest.ID)},
	})
	if err != nil {
		return detectors.Verdict{}, err
	}
	return verdict, nil
}

func knownCounts(opportunities []opportunityFact) (int, int) {
	denominator, numerator := 0, 0
	for _, opportunity := range opportunities {
		if !opportunity.ParticipationKnown {
			continue
		}
		denominator++
		if opportunity.Participated {
			numerator++
		}
	}
	return denominator, numerator
}

func resurrectionOutcome(current *vital.CurrentState, opportunities []opportunityFact, config vital.Config) (bool, bool) {
	if current == nil || current.State != vital.StateAwaitingResurrection {
		return false, false
	}
	known := 0
	for _, opportunity := range opportunities {
		if opportunity.DetectedAt.Before(current.StartedAt) {
			continue
		}
		if opportunity.Participated && opportunity.ParticipationKnown && opportunity.Followed {
			return true, false
		}
		if opportunity.ParticipationKnown {
			known++
		}
	}
	return false, known >= config.ResurrectionFailureOpportunities
}

func sourceHasExactInvocation(source string) bool {
	return adapters.Source(source) == adapters.SourceClaudeCode || adapters.Source(source) == adapters.SourceCodex
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
