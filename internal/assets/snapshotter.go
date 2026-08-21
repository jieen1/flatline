package assets

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"

	"flatline/internal/canonical"
)

// Snapshotter is the read-only asset snapshot boundary. Callers choose the
// filesystem and path explicitly, so discovery policy stays outside the
// registry and tests never need to read a user's asset directory.
type Snapshotter struct {
	registry *Registry
}

func NewSnapshotter(registry *Registry) *Snapshotter {
	return &Snapshotter{registry: registry}
}

// FileSnapshotInput describes one observation of a file in an fs.FS.
type FileSnapshotInput struct {
	Path             string
	ObservationLevel canonical.ObservationLevel
	ObservedAt       time.Time
	ContentRef       string
}

// DiscoveryOptions controls the explicit, read-only directory discovery
// policy. Timestamps are supplied by the caller so tests and historical
// replay never hide a wall-clock value in the scanner.
type DiscoveryOptions struct {
	Scope            Scope
	RootLabel        string
	FirstSeenAt      time.Time
	ObservedAt       time.Time
	ObservationLevel canonical.ObservationLevel
	ContentRefPrefix string
}

// DiscoveredAsset identifies a file that Flatline recognizes as an asset.
// Content is intentionally not included here; SnapshotFile reads it only
// after the caller has inspected the discovery result.
type DiscoveredAsset struct {
	Asset      AssetInput
	Path       string
	ContentRef string
}

// Discover walks an explicitly supplied fs.FS and returns recognized asset
// files. It never writes to the source filesystem. Recognized files are:
// SKILL.md, AGENTS.md, markdown files below a rules directory, and files below
// a hooks directory. Unknown files remain unclassified rather than becoming
// assets by inference.
func Discover(files fs.FS, options DiscoveryOptions) ([]DiscoveredAsset, error) {
	if files == nil {
		return nil, fmt.Errorf("assets: discovery filesystem is required")
	}
	if !options.Scope.Valid() {
		return nil, fmt.Errorf("assets: discovery scope is invalid")
	}
	if options.FirstSeenAt.IsZero() || options.ObservedAt.IsZero() {
		return nil, fmt.Errorf("assets: discovery first_seen_at and observed_at are required")
	}
	if options.FirstSeenAt.Location() != time.UTC || options.ObservedAt.Location() != time.UTC {
		return nil, fmt.Errorf("assets: discovery timestamps must be UTC")
	}
	if !options.ObservationLevel.Valid() {
		return nil, fmt.Errorf("assets: discovery observation level is invalid")
	}
	var out []DiscoveredAsset
	err := fs.WalkDir(files, ".", func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if filePath != "." && (entry.Name() == ".git" || entry.Name() == ".flatline") {
				return fs.SkipDir
			}
			return nil
		}
		kind, name, ok := classifyDiscoveredPath(filePath)
		if !ok {
			return nil
		}
		sourcePath := joinLocator(options.RootLabel, filePath)
		contentRef := joinLocator(options.ContentRefPrefix, filePath)
		out = append(out, DiscoveredAsset{Asset: AssetInput{Kind: kind, Scope: options.Scope, Name: name, SourcePath: sourcePath, FirstSeenAt: options.FirstSeenAt, LastSeenAt: timePointer(options.ObservedAt)}, Path: filePath, ContentRef: contentRef})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("assets: discover files: %w", err)
	}
	return out, nil
}

// SnapshotInput is the source-independent snapshot operation. Content is
// supplied by the caller; this package does not scan or modify source files.
type SnapshotInput struct {
	Asset            AssetInput
	Content          []byte
	ObservationLevel canonical.ObservationLevel
	ObservedAt       time.Time
	ContentRef       string
}

// Snapshot registers the asset and records its content snapshot. Repeated
// content reuses the same immutable version; a later stronger observation can
// upgrade only the observation metadata, never the content or version number.
func (s *Snapshotter) Snapshot(ctx context.Context, in SnapshotInput) (*AssetVersion, error) {
	if s == nil || s.registry == nil {
		return nil, fmt.Errorf("assets: snapshotter registry is required")
	}
	assetID, err := s.registry.Register(ctx, in.Asset)
	if err != nil {
		return nil, err
	}
	version, err := s.registry.RecordVersion(ctx, VersionInput{
		AssetID:          assetID,
		Content:          in.Content,
		ObservationLevel: in.ObservationLevel,
		ObservedAt:       in.ObservedAt,
		ContentRef:       in.ContentRef,
	})
	if err != nil {
		return nil, err
	}
	if version.Created {
		return version, nil
	}
	if err := s.registry.UpgradeObservation(ctx, version.ID, in.ObservationLevel, in.ContentRef); err != nil {
		return nil, err
	}
	return s.registry.VersionByHash(ctx, assetID, version.ContentHash)
}

// SnapshotFile reads one explicitly selected file from files and snapshots
// its bytes. fs.ValidPath rejects absolute and parent-traversal paths.
func (s *Snapshotter) SnapshotFile(ctx context.Context, files fs.FS, asset AssetInput, in FileSnapshotInput) (*AssetVersion, error) {
	if files == nil {
		return nil, fmt.Errorf("assets: snapshot filesystem is required")
	}
	if !fs.ValidPath(in.Path) {
		return nil, fmt.Errorf("assets: invalid snapshot path %q", in.Path)
	}
	content, err := fs.ReadFile(files, in.Path)
	if err != nil {
		return nil, fmt.Errorf("assets: read snapshot %q: %w", in.Path, err)
	}
	if asset.SourcePath == "" {
		asset.SourcePath = in.Path
	}
	return s.Snapshot(ctx, SnapshotInput{
		Asset:            asset,
		Content:          content,
		ObservationLevel: in.ObservationLevel,
		ObservedAt:       in.ObservedAt,
		ContentRef:       in.ContentRef,
	})
}

func classifyDiscoveredPath(filePath string) (Kind, string, bool) {
	clean := path.Clean(filePath)
	base := path.Base(clean)
	if base == "SKILL.md" {
		return KindSkill, discoveryName(path.Dir(clean), "skill"), true
	}
	if base == "AGENTS.md" {
		return KindAgentsMD, discoveryName(path.Dir(clean), "agents"), true
	}
	parts := strings.Split(clean, "/")
	for _, part := range parts[:len(parts)-1] {
		switch part {
		case "rules":
			if strings.EqualFold(path.Ext(base), ".md") {
				return KindRule, discoveryName(strings.TrimSuffix(clean, path.Ext(clean)), "rule"), true
			}
		case "hooks":
			return KindHook, discoveryName(strings.TrimSuffix(clean, path.Ext(clean)), "hook"), true
		}
	}
	return "", "", false
}

func discoveryName(value, fallback string) string {
	value = strings.Trim(value, "/.")
	if value == "" {
		return fallback
	}
	return strings.ReplaceAll(value, "/", ":")
}

func joinLocator(prefix, value string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return value
	}
	return strings.TrimRight(prefix, "/") + "/" + value
}

func timePointer(value time.Time) *time.Time { return &value }
