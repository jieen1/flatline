// Package ingest connects source adapters to the canonical event store and
// the P3 asset/opportunity/participation projections. It is deliberately
// explicit about the asset mapping and task-shape inputs: neither can be
// safely inferred from an adapter that did not record them.
package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/assets"
	"flatline/internal/baseline"
	"flatline/internal/canonical"
	"flatline/internal/eventstore"
	"flatline/internal/storage"
	"flatline/internal/tracking"
)

type AssetObservation struct {
	// SourceAssetID is the id used by the source adapter. It may differ from
	// the stable Flatline asset id and is required when the source namespace
	// cannot be used directly as a Flatline kind/scope/name.
	SourceAssetID    string
	Asset            assets.AssetInput
	Content          []byte
	ObservationLevel canonical.ObservationLevel
	ObservedAt       time.Time
	ContentRef       string
}

type SessionInput struct {
	Raw                 adapters.RawSession
	TaskTags            []string
	Assets              []AssetObservation
	OpportunityAssetIDs []string
}

type Report struct {
	SessionID                 string
	EventsInserted            int
	FrictionRecordsInserted   int
	EnvironmentEventsInserted int
	AssetSnapshotsObserved    int
	AssetVersionsCreated      int
	OpportunitiesInserted     int
	ParticipationsInserted    int
	UnresolvedAssetVersions   int
	BundleResolved            bool
	BundleReason              string
	ShapeRecorded             bool
	ShapeClass                string
	ShapeReason               string
}

type Pipeline struct {
	adapters    *adapters.Registry
	events      *eventstore.Store
	assets      *assets.Registry
	snapshotter *assets.Snapshotter
	tracker     *tracking.Tracker
	resolver    *baseline.Resolver
}

func NewPipeline(db *storage.DB, registry *adapters.Registry) *Pipeline {
	assetRegistry := assets.New(db)
	return &Pipeline{
		adapters:    registry,
		events:      eventstore.New(db),
		assets:      assetRegistry,
		snapshotter: assets.NewSnapshotter(assetRegistry),
		tracker:     tracking.New(db),
		resolver:    baseline.NewResolver(db),
	}
}

// Ingest performs one idempotent adapter replay. Validation of all mappings
// and snapshot inputs happens before any database write; a missing mapping is
// an error rather than a silently dropped event or a fabricated asset.
func (p *Pipeline) Ingest(ctx context.Context, input SessionInput) (Report, error) {
	if p == nil || p.adapters == nil || p.events == nil || p.assets == nil || p.snapshotter == nil || p.tracker == nil || p.resolver == nil {
		return Report{}, fmt.Errorf("ingest: pipeline is not fully wired")
	}
	if !input.Raw.Source.Valid() {
		return Report{}, fmt.Errorf("ingest: invalid source %q", input.Raw.Source)
	}
	adapter, ok := p.adapters.Get(input.Raw.Source)
	if !ok {
		return Report{}, fmt.Errorf("ingest: no adapter registered for %q", input.Raw.Source)
	}
	meta, events, err := adapter.Parse(input.Raw)
	if err != nil {
		return Report{}, fmt.Errorf("ingest: parse %s session: %w", input.Raw.Source, err)
	}
	sessionID := string(input.Raw.Source) + ":" + meta.SourceSessionID
	if meta.SourceSessionID == "" {
		return Report{}, fmt.Errorf("ingest: adapter returned empty source session id")
	}

	sourceToTarget, targetObservations, err := validateInputs(ctx, input, events, p.assets)
	if err != nil {
		return Report{}, err
	}

	// Snapshot before event ingestion so foreign-keyed event versions are
	// available. All input validation above has already completed.
	versions := make(map[string]*assets.AssetVersion, len(targetObservations))
	report := Report{SessionID: sessionID}
	for targetID, observation := range targetObservations {
		version, err := p.snapshotter.Snapshot(ctx, assets.SnapshotInput{
			Asset: observation.Asset, Content: observation.Content,
			ObservationLevel: observation.ObservationLevel, ObservedAt: observation.ObservedAt,
			ContentRef: observation.ContentRef,
		})
		if err != nil {
			return report, fmt.Errorf("ingest: snapshot %s: %w", targetID, err)
		}
		versions[targetID] = version
		report.AssetSnapshotsObserved++
		if version.Created {
			report.AssetVersionsCreated++
		}
	}

	for i := range events {
		if events[i].SessionID != sessionID {
			return report, fmt.Errorf("ingest: event %q session %q does not match %q", events[i].SourceEventID, events[i].SessionID, sessionID)
		}
		if events[i].AssetID == "" {
			continue
		}
		targetID, ok := sourceToTarget[events[i].AssetID]
		if !ok {
			// A stable target id may be emitted directly by an adapter.
			if _, err := p.assets.Get(ctx, events[i].AssetID); err != nil {
				return report, fmt.Errorf("ingest: event %q references unmapped asset %q", events[i].SourceEventID, events[i].AssetID)
			}
			targetID = events[i].AssetID
		}
		events[i].AssetID = targetID
		if version, ok := versions[targetID]; ok && contentHashMatches(events[i], version.ContentHash) {
			id := version.ID
			events[i].AssetVersionID = &id
			continue
		}
		if events[i].AssetVersionID == nil {
			var version *assets.AssetVersion
			var versionErr error
			if contentHash := eventContentHash(events[i]); contentHash != "" {
				version, versionErr = p.assets.VersionByHash(ctx, targetID, contentHash)
			} else {
				version, versionErr = p.assets.LatestVersion(ctx, targetID)
			}
			if versionErr == nil {
				id := version.ID
				events[i].AssetVersionID = &id
			} else if versionErr != sql.ErrNoRows {
				return report, fmt.Errorf("ingest: resolve asset version %s: %w", targetID, versionErr)
			}
		}
	}

	storedSessionID, err := p.events.IngestSession(ctx, input.Raw.Source, meta)
	if err != nil {
		return report, err
	}
	if storedSessionID != sessionID {
		return report, fmt.Errorf("ingest: event store session id %q differs from adapter id %q", storedSessionID, sessionID)
	}
	inserted, err := p.events.IngestEvents(ctx, sessionID, events)
	if err != nil {
		return report, err
	}
	report.EventsInserted = inserted
	frictionInserted, err := p.events.IngestFriction(ctx, sessionID, events)
	if err != nil {
		return report, err
	}
	report.FrictionRecordsInserted = frictionInserted
	anchors, err := p.events.DetectEnvironmentChanges(ctx, sessionID)
	if err != nil {
		return report, err
	}
	report.EnvironmentEventsInserted = anchors

	if meta.StartedAt != nil {
		if _, err := p.resolver.Resolve(ctx, sessionID); err != nil {
			report.BundleReason = err.Error()
		} else {
			report.BundleResolved = true
		}
	} else {
		report.BundleReason = "session start time is not recorded; effective bundle is not time-anchored"
	}

	opportunityIDs := input.OpportunityAssetIDs
	if len(opportunityIDs) == 0 {
		opportunityIDs = make([]string, 0, len(targetObservations))
		for targetID := range targetObservations {
			opportunityIDs = append(opportunityIDs, targetID)
		}
	}
	opportunityIDs, err = mapAssetIDs(ctx, p.assets, opportunityIDs, sourceToTarget, targetObservations)
	if err != nil {
		return report, fmt.Errorf("ingest: opportunity asset set: %w", err)
	}
	sort.Strings(opportunityIDs)
	if len(input.TaskTags) == 0 {
		report.ShapeReason = "task shape is not recorded by this input; no opportunity is inferred"
	} else if len(opportunityIDs) == 0 {
		report.ShapeReason = "no asset set was recorded for the task shape; no opportunity is inferred"
	} else {
		class, inserted, err := p.tracker.RecordSessionShape(ctx, tracking.SessionShape{SessionID: sessionID, Tags: input.TaskTags, AssetIDs: opportunityIDs, DetectedAt: sessionTime(meta, events)})
		if err != nil {
			return report, fmt.Errorf("ingest: record session shape: %w", err)
		}
		report.ShapeRecorded = true
		report.ShapeClass = class
		report.OpportunitiesInserted = inserted
	}

	for _, event := range events {
		if event.ParticipationSignal == nil || event.AssetID == "" || event.AssetVersionID == nil {
			if event.ParticipationSignal != nil && event.AssetID != "" && event.AssetVersionID == nil {
				report.UnresolvedAssetVersions++
			}
			continue
		}
		var opportunityID *int64
		if report.ShapeRecorded {
			opportunity, lookupErr := p.tracker.OpportunityFor(ctx, sessionID, event.AssetID, report.ShapeClass)
			if lookupErr == nil {
				opportunityID = &opportunity.ID
			} else if lookupErr != sql.ErrNoRows {
				return report, fmt.Errorf("ingest: lookup opportunity for %s: %w", event.AssetID, lookupErr)
			}
		}
		locator := event.Locator
		inserted, err := p.tracker.RecordParticipation(ctx, tracking.ParticipationInput{
			AssetVersionID: *event.AssetVersionID, SessionID: sessionID, OpportunityID: opportunityID,
			Signal: *event.ParticipationSignal, Level: event.ObservationLevel,
			OccurredAt: event.OccurredAt, Locator: &locator,
		})
		if err != nil {
			return report, fmt.Errorf("ingest: record participation %s: %w", event.SourceEventID, err)
		}
		if inserted {
			report.ParticipationsInserted++
		}
	}
	return report, nil
}

func validateInputs(ctx context.Context, input SessionInput, events []canonical.Event, registry *assets.Registry) (map[string]string, map[string]AssetObservation, error) {
	sourceToTarget := make(map[string]string, len(input.Assets))
	targetObservations := make(map[string]AssetObservation, len(input.Assets))
	for _, observation := range input.Assets {
		if err := observation.Asset.Validate(); err != nil {
			return nil, nil, fmt.Errorf("ingest: asset observation %q: %w", observation.SourceAssetID, err)
		}
		if observation.Content == nil {
			return nil, nil, fmt.Errorf("ingest: asset observation %q has nil content", observation.SourceAssetID)
		}
		if !observation.ObservationLevel.Valid() {
			return nil, nil, fmt.Errorf("ingest: asset observation %q has invalid observation level %q", observation.SourceAssetID, observation.ObservationLevel)
		}
		if observation.ObservedAt.IsZero() || observation.ObservedAt.Location() != time.UTC {
			return nil, nil, fmt.Errorf("ingest: asset observation %q observed_at must be UTC", observation.SourceAssetID)
		}
		targetID := observation.Asset.ID()
		sourceID := observation.SourceAssetID
		if sourceID == "" {
			sourceID = targetID
		}
		if existing, ok := sourceToTarget[sourceID]; ok && existing != targetID {
			return nil, nil, fmt.Errorf("ingest: source asset %q maps to both %q and %q", sourceID, existing, targetID)
		}
		if existing, ok := targetObservations[targetID]; ok && existing.SourceAssetID != sourceID {
			return nil, nil, fmt.Errorf("ingest: target asset %q has conflicting source ids", targetID)
		}
		sourceToTarget[sourceID] = targetID
		targetObservations[targetID] = observation
	}
	for _, event := range events {
		if event.AssetID == "" {
			continue
		}
		if _, ok := sourceToTarget[event.AssetID]; ok {
			continue
		}
		// A stable target id may be replayed without resending the asset
		// snapshot. Source-specific ids still require an explicit mapping.
		if _, ok := targetObservations[event.AssetID]; !ok {
			if registry == nil {
				return nil, nil, fmt.Errorf("ingest: no explicit mapping for source asset %q", event.AssetID)
			}
			if _, err := registry.Get(ctx, event.AssetID); err != nil {
				if err == sql.ErrNoRows {
					return nil, nil, fmt.Errorf("ingest: no explicit mapping for source asset %q", event.AssetID)
				}
				return nil, nil, fmt.Errorf("ingest: resolve existing asset %q: %w", event.AssetID, err)
			}
		}
	}
	return sourceToTarget, targetObservations, nil
}

func mapAssetIDs(ctx context.Context, registry *assets.Registry, input []string, sourceToTarget map[string]string, observations map[string]AssetObservation) ([]string, error) {
	seen := make(map[string]struct{}, len(input))
	out := make([]string, 0, len(input))
	for _, sourceID := range input {
		targetID, ok := sourceToTarget[sourceID]
		if !ok {
			if _, exists := observations[sourceID]; exists {
				targetID = sourceID
				ok = true
			}
		}
		if !ok {
			if registry == nil {
				return nil, fmt.Errorf("no explicit mapping for opportunity asset %q", sourceID)
			}
			if _, err := registry.Get(ctx, sourceID); err != nil {
				if err == sql.ErrNoRows {
					return nil, fmt.Errorf("no explicit mapping for opportunity asset %q", sourceID)
				}
				return nil, fmt.Errorf("resolve opportunity asset %q: %w", sourceID, err)
			}
			targetID = sourceID
		}
		if _, exists := seen[targetID]; exists {
			continue
		}
		seen[targetID] = struct{}{}
		out = append(out, targetID)
	}
	return out, nil
}

func sessionTime(meta adapters.SessionMeta, events []canonical.Event) time.Time {
	if meta.StartedAt != nil {
		return meta.StartedAt.UTC()
	}
	for _, event := range events {
		if event.OccurredAt != nil {
			return event.OccurredAt.UTC()
		}
	}
	// Adapter input validation deliberately allows a session with no
	// timestamps. A caller that supplies no timestamp cannot produce a valid
	// opportunity; this value is only a defensive fallback and is rejected by
	// the tracker if reached.
	return time.Time{}
}

func contentHashMatches(event canonical.Event, contentHash string) bool {
	if contentHash == "" {
		return true
	}
	value, ok := event.Payload["content_hash"].(string)
	return !ok || strings.TrimSpace(value) == "" || value == contentHash
}

func eventContentHash(event canonical.Event) string {
	value, ok := event.Payload["content_hash"].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}
