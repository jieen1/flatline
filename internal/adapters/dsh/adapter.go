// Package dsh maps normalized DeepSeek Harness session records to canonical facts.
package dsh

import (
	"flatline/internal/adapters"
	"flatline/internal/adapters/normalized"
)

const adapterVersion = "dsh/1"

func New() normalized.Adapter {
	return normalized.New(normalized.Config{
		Source:  adapters.SourceDSH,
		Version: adapterVersion,
		Matrix: adapters.FieldMatrix{
			Supported: []string{
				"source_session_id", "started_at", "ended_at", "harness_version", "model", "cwd",
				"title", "task_text", "thread_kind", "call_id", "is_error", "abort_reason",
				"input_tokens", "output_tokens", "cached_input_tokens", "total_tokens",
				"context_window", "asset_invocation", "locator",
			},
			// A dsh session header records delegationDepth but never names the
			// delegating session, so a subagent's parent stays unrecorded
			// rather than being guessed from the directory or the id.
			Unsupported: []string{"raw_source_bytes", "agent_nickname", "cost", "lines_added", "lines_removed", "files_changed"},
			Unrecorded:  []string{"parent_session_id", "exit_code", "reasoning_tokens", "cache_write_tokens", "loaded_signal", "offered_signal", "followed_signal", "violated_signal"},
		},
	})
}
