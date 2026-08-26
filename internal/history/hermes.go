package history

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Hermes keeps a ~/.hermes tree with skills, hooks, memories and logs, and a
// sessions directory that is empty on this machine. There is no transcript
// format to read yet, so Flatline probes the root and reports what it found.
// An empty sessions directory is "no_sessions", not an error and not zero
// sessions imported from a format we understood (ADR-13).

// SourceStatus is one source's answer to "is there anything here to read?".
type SourceStatus struct {
	Kind       string     `json:"kind"`
	Root       string     `json:"root"`
	Status     string     `json:"status"`
	Sessions   *int       `json:"sessions"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	Detail     string     `json:"detail,omitempty"`
	Error      string     `json:"error,omitempty"`
}

const (
	StatusOK         = "ok"
	StatusNotFound   = "not_found"
	StatusNoSessions = "no_sessions"
	StatusError      = "error"
)

// probeHermes counts what the Hermes session directory holds. Hermes has no
// documented transcript format here, so nothing is parsed: the probe exists so
// the absence is a reported number rather than a silent gap.
func probeHermes(root string) SourceStatus {
	status := SourceStatus{Kind: "hermes", Root: root}
	if root == "" {
		status.Status = StatusNotFound
		return status
	}
	sessions := filepath.Join(root, "sessions")
	entries, err := os.ReadDir(sessions)
	if err != nil {
		if os.IsNotExist(err) {
			status.Status = StatusNotFound
			return status
		}
		status.Status = StatusError
		status.Error = err.Error()
		return status
	}
	present := 0
	var newest *time.Time
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		present++
		at := info.ModTime().UTC()
		if newest == nil || at.After(*newest) {
			newest = &at
		}
	}
	// sessions counts what Flatline can read, and no adapter reads Hermes yet,
	// so it is zero whatever the directory holds. When entries do appear, the
	// count is reported in detail instead of being absorbed into that zero:
	// "nothing readable" and "nothing there" must not look the same.
	readable := 0
	status.Sessions = &readable
	status.LastSeenAt = newest
	status.Status = StatusNoSessions
	if present > 0 {
		status.Detail = fmt.Sprintf("%d entries present; no Hermes transcript reader exists yet", present)
	}
	return status
}
