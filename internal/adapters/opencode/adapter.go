// Package opencode maps normalized opencode session records to canonical facts.
package opencode

import (
	"flatline/internal/adapters"
	"flatline/internal/adapters/normalized"
)

const adapterVersion = "opencode/1"

func New() normalized.Adapter {
	return normalized.New(normalized.Config{
		Source:  adapters.SourceOpenCode,
		Version: adapterVersion,
		Matrix: adapters.FieldMatrix{
			Supported: []string{
				"source_session_id", "started_at", "ended_at", "harness_version", "model", "cwd",
				"title", "task_text", "parent_session_id", "thread_kind", "agent_role", "originator",
				"call_id", "is_error", "input_tokens", "output_tokens", "reasoning_tokens",
				"cached_input_tokens", "cache_write_tokens", "total_tokens", "cost",
				"lines_added", "lines_removed", "files_changed", "asset_invocation", "locator",
			},
			// opencode records a tool failure as state.status='error' with a
			// message. Only the bash tool also records a process exit status,
			// so exit_code is per-record evidence, not a session-wide field.
			Unsupported: []string{"raw_source_bytes", "agent_nickname", "context_window"},
			Unrecorded:  []string{"loaded_signal", "offered_signal", "followed_signal", "violated_signal"},
		},
	})
}
