// Package adapters defines the shared source-adapter contract.
package adapters

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"flatline/internal/canonical"
)

type Source string

const (
	SourceClaudeCode Source = "claude_code"
	SourceCodex      Source = "codex"
	SourceOpenCode   Source = "opencode"
	SourceDSH        Source = "dsh"
	SourceHermes     Source = "hermes"
)

// sources is the open enumeration behind Source.Valid. Adding a harness means
// registering it here or registering an adapter for it, not editing a CHECK
// constraint (migration 012) or a switch.
var sources = struct {
	mu    sync.RWMutex
	known map[Source]string
}{known: map[Source]string{
	SourceClaudeCode: "Claude Code",
	SourceCodex:      "Codex",
	SourceOpenCode:   "opencode",
	SourceDSH:        "DeepSeek Harness",
	SourceHermes:     "Hermes",
}}

// RegisterSource declares a source kind and its display name. Registering an
// adapter declares its source too, so this is for sources that are probed
// before any adapter exists for them.
func RegisterSource(source Source, displayName string) error {
	if strings.TrimSpace(string(source)) == "" {
		return fmt.Errorf("adapters: empty source")
	}
	sources.mu.Lock()
	defer sources.mu.Unlock()
	if existing, ok := sources.known[source]; ok && existing != displayName && displayName != "" {
		return fmt.Errorf("adapters: source %q already registered as %q", source, existing)
	}
	if _, ok := sources.known[source]; !ok {
		sources.known[source] = displayName
	}
	return nil
}

// Valid reports whether the source has been registered. It is what the event
// store checks before writing a session, so an unregistered source fails at
// ingest rather than becoming an unattributable row.
func (s Source) Valid() bool {
	sources.mu.RLock()
	defer sources.mu.RUnlock()
	_, ok := sources.known[s]
	return ok
}

// DisplayName is the human-facing name for the source, for the UI and for
// health output. It falls back to the identifier when none was registered.
func (s Source) DisplayName() string {
	sources.mu.RLock()
	defer sources.mu.RUnlock()
	if name, ok := sources.known[s]; ok && name != "" {
		return name
	}
	return string(s)
}

// KnownSources lists every registered source in deterministic order.
func KnownSources() []Source {
	sources.mu.RLock()
	defer sources.mu.RUnlock()
	out := make([]Source, 0, len(sources.known))
	for source := range sources.known {
		out = append(out, source)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// VersionInfo carries environment facts detected by an adapter.
type VersionInfo struct {
	HarnessVersion string
	Model          string
	Raw            string
}

// RawSession is the adapter input. The raw bytes are supplied by the caller,
// so tests and future ingestion never need to read real user directories.
type RawSession struct {
	Source     Source
	SessionID  string
	RawJSON    []byte
	SourcePath string
}

// SessionMeta is the source-independent session projection.
type SessionMeta struct {
	SourceSessionID string
	StartedAt       *time.Time
	EndedAt         *time.Time
	HarnessVersion  string
	Model           string
	CWD             string
	// Title is a source-backed display name. It is nullable in storage:
	// callers must not derive a title from an opaque id when the source has no
	// task/title evidence.
	Title string
	// TaskText is a bounded source-backed excerpt used to explain what the
	// session was about. It is not a task-shape classification and must never
	// create an opportunity by itself.
	TaskText string
	// ThreadKind is "main" or "subagent" and stays empty when the source did
	// not record where the thread came from. ParentSessionID is already
	// qualified with the source prefix.
	ThreadKind      string
	ParentSessionID string
	AgentRole       string
	AgentNickname   string
	Originator      string
}

// FieldMatrix documents source coverage without turning missing data into zero.
type FieldMatrix struct {
	Supported   []string
	Unsupported []string
	Unrecorded  []string
}

func (m FieldMatrix) Validate() error {
	seen := make(map[string]string)
	for group, fields := range map[string][]string{
		"supported": m.Supported, "unsupported": m.Unsupported, "unrecorded": m.Unrecorded,
	} {
		for _, field := range fields {
			if field == "" {
				return fmt.Errorf("adapters: empty field in %s matrix", group)
			}
			if previous, ok := seen[field]; ok {
				return fmt.Errorf("adapters: field %q appears in %s and %s", field, previous, group)
			}
			seen[field] = group
		}
	}
	return nil
}

// Adapter maps a source session to deterministic canonical facts.
type Adapter interface {
	Source() Source
	Version() string
	DetectVersion(raw RawSession) (VersionInfo, error)
	Parse(raw RawSession) (SessionMeta, []canonical.Event, error)
	FieldMatrix() FieldMatrix
}
