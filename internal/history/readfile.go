package history

import (
	"os"

	"flatline/internal/adapters"
)

// ReadFile reads one already-discovered transcript again, with the current
// parser, and returns it in the same normalized shape Discover produces. It is
// what the versioned re-read pass calls: the file's fingerprint has not
// changed, so the refresh pass will never look at it again, and a parser that
// now reads a record the old one skipped needs one more read to pick it up.
//
// ok=false means this source is not a plain transcript file this package can
// re-read on its own; the caller leaves that file alone rather than stamping
// it as read.
func ReadFile(path string, source adapters.Source, config Config) (Session, bool, string) {
	index := newAssetIndex(config.Assets, config.ProjectRoot)
	// An opencode session is a database row, not a file, so it is addressed by
	// the pseudo path the discovery pass recorded for it and there is nothing
	// to stat.
	if source == adapters.SourceOpenCode {
		return readOpenCodeRow(path, index, config.ProjectRoot)
	}
	if _, err := os.Stat(path); err != nil {
		return Session{}, false, ""
	}
	switch source {
	case adapters.SourceClaudeCode:
		session, _, ok, warning := readClaude(path, index, config.ProjectRoot)
		return session, ok, warning
	case adapters.SourceCodex:
		session, _, ok, warning := readCodex(path, index, config.ProjectRoot)
		return session, ok, warning
	case adapters.SourceDSH:
		session, _, ok, warning := readDSH(path, index, config.ProjectRoot)
		return session, ok, warning
	default:
		return Session{}, false, ""
	}
}

// ReparsableSource reports whether ReadFile can re-read a session of this
// source from what the discovery pass recorded for it. Hermes has a root to
// probe but no reader, so it is the one source that cannot be re-read.
func ReparsableSource(source adapters.Source) bool {
	switch source {
	case adapters.SourceClaudeCode, adapters.SourceCodex, adapters.SourceOpenCode, adapters.SourceDSH:
		return true
	default:
		return false
	}
}
