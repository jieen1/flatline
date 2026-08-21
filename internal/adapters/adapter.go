// Package adapters defines the shared source-adapter contract.
package adapters

import (
	"fmt"
	"time"

	"flatline/internal/canonical"
)

type Source string

const (
	SourceClaudeCode Source = "claude_code"
	SourceCodex      Source = "codex"
)

func (s Source) Valid() bool {
	return s == SourceClaudeCode || s == SourceCodex
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
