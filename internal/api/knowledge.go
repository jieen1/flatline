package api

import (
	"database/sql"
	"net/http"
	"sort"
	"strings"

	"flatline/internal/friction"
)

// Project knowledge (P18-3) is the second refined product of the corpus: how
// work is actually done in a project, at the command level. top_programs says
// git ran 9,141 times, which tells a newcomer nothing; the playbook fact is
// the invocation itself — this exact command, run this many times, across
// this many sessions, with this many recorded failures. Failures are stated,
// never used to hide a command: a test command failing sometimes is normal
// use of a test command.

const knowledgeMinRuns = 3
const knowledgeMinSessions = 2
const knowledgeLimit = 30

// navigationPrograms are exploration, not method: how someone looked around,
// never how the work is done. The closed list is part of the selection rule.
var navigationPrograms = map[string]struct{}{
	"cd": {}, "ls": {}, "cat": {}, "grep": {}, "rg": {}, "find": {}, "echo": {},
	"head": {}, "tail": {}, "sed": {}, "awk": {}, "which": {}, "pwd": {}, "wc": {},
	// sleep is waiting, not method: on real data it ranked 8th in cognode's
	// playbook (21 sessions) while telling a newcomer nothing to reuse.
	"sleep": {},
}

const knowledgeNote = "作业命令 = 本项目里同一条规范化命令 ≥3 次运行且 ≥2 个会话使用过；查看/导航类程序（cd ls cat grep rg find echo head tail sed awk which pwd wc sleep）不入清单——查看是探索、sleep 是等待，都不是方法。failures_recorded 是这些运行里记录到失败（错误标记或非预期非零退出）的次数，如实陈述、不用于隐藏命令：测试命令偶尔失败正是测试命令的正常用法。按会话佐证数排序。"

const knowledgeNoteEN = "A working command is one normalized command run 3+ times across 2+ sessions in this project; navigation programs (cd ls cat grep rg find echo head tail sed awk which pwd wc sleep) stay out — looking around is exploring and sleep is waiting, neither is method. failures_recorded counts the runs that recorded a failure (error flag or unexpected nonzero exit); it is stated, never used to hide a command — a test command failing sometimes is normal use of a test command. Ordered by session corroboration."

type workingCommand struct {
	Label    string `json:"label"`
	Program  string `json:"program"`
	Runs     int    `json:"runs"`
	Sessions int    `json:"sessions"`
	Failures int    `json:"failures_recorded"`
	LastAt   string `json:"last_at"`
}

type knowledgeResponse struct {
	ProjectKey      string           `json:"project_key"`
	WorkingCommands []workingCommand `json:"working_commands"`
	// CommandsSeen is how many command rows the project holds at all, so an
	// empty list can honestly say "not enough corroboration yet" instead of
	// looking like a project where nothing ever ran.
	CommandsSeen int    `json:"commands_seen"`
	Note         string `json:"note"`
	NoteEN       string `json:"note_en"`
	Complete     bool   `json:"complete"`
}

func (s *Server) handleProjectKnowledge(w http.ResponseWriter, r *http.Request) {
	projectKey := r.PathValue("key")
	ctx := r.Context()
	out := knowledgeResponse{ProjectKey: projectKey, WorkingCommands: []workingCommand{},
		Note: knowledgeNote, NoteEN: knowledgeNoteEN, Complete: true}

	rows, err := s.db.QueryContext(ctx, `
		SELECT sc.session_id, COALESCE(sc.program, ''), sc.command,
		       sc.exit_code, sc.is_error, sc.expected_exit, COALESCE(sc.occurred_at, '')
		FROM session_commands sc JOIN sessions s ON s.id = sc.session_id
		WHERE s.project_key = ?`, projectKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type record struct {
		item     workingCommand
		sessions map[string]struct{}
	}
	totals := make(map[string]*record)
	for rows.Next() {
		var sessionID, program, command, occurredAt string
		var exitCode sql.NullInt64
		var isError sql.NullInt64
		var expectedExit int
		if err := rows.Scan(&sessionID, &program, &command, &exitCode, &isError, &expectedExit, &occurredAt); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out.CommandsSeen++
		if _, navigation := navigationPrograms[strings.ToLower(program)]; navigation || program == "" {
			continue
		}
		label := friction.NormalizeLine(command)
		entry, ok := totals[label]
		if !ok {
			entry = &record{item: workingCommand{Label: label, Program: program},
				sessions: make(map[string]struct{})}
			totals[label] = entry
		}
		entry.item.Runs++
		entry.sessions[sessionID] = struct{}{}
		if (isError.Valid && isError.Int64 != 0) ||
			(exitCode.Valid && exitCode.Int64 != 0 && expectedExit == 0) {
			entry.item.Failures++
		}
		if occurredAt > entry.item.LastAt {
			entry.item.LastAt = occurredAt
		}
	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, entry := range totals {
		if entry.item.Runs < knowledgeMinRuns || len(entry.sessions) < knowledgeMinSessions {
			continue
		}
		entry.item.Sessions = len(entry.sessions)
		out.WorkingCommands = append(out.WorkingCommands, entry.item)
	}
	sort.Slice(out.WorkingCommands, func(i, j int) bool {
		left, right := out.WorkingCommands[i], out.WorkingCommands[j]
		if left.Sessions != right.Sessions {
			return left.Sessions > right.Sessions
		}
		if left.Runs != right.Runs {
			return left.Runs > right.Runs
		}
		return left.Label < right.Label
	})
	if len(out.WorkingCommands) > knowledgeLimit {
		out.WorkingCommands = out.WorkingCommands[:knowledgeLimit]
	}
	writeJSON(w, http.StatusOK, out)
}
