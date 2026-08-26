package friction

import "regexp"

// A hint names the mechanism behind a recurring friction signature: what kind
// of thing it is, and one sentence saying what that thing does. It is a closed,
// hand-written table, not a model: a signature that matches no rule gets no
// hint rather than a guess.
//
// The mechanism sentence describes only how the recorded outcome comes about.
// It never says what caused a session to fail and never suggests a fix; that is
// the reader's call, and Flatline does not make causal claims (ADR-8).

// The closed set of hint kinds.
const (
	HintEnvironment = "environment"
	HintHarnessRule = "harness_rule"
	HintUserHook    = "user_hook"
	HintToolMisuse  = "tool_misuse"
	HintPermission  = "permission"
	HintTimeout     = "timeout"
	HintTest        = "test"
	HintBuild       = "build"
	HintUserStopped = "user_interrupt"
)

// HintKinds is the closed set, in the order the rules below first use them.
var HintKinds = []string{
	HintUserStopped, HintEnvironment, HintHarnessRule, HintUserHook, HintToolMisuse,
	HintPermission, HintTimeout, HintTest, HintBuild,
}

// Hint is what a matched rule says about a signature. The mechanism sentence
// is written twice, once in each of the languages the pages are read in; both
// come from the same rule, neither is translated after the fact.
type Hint struct {
	Kind        string `json:"kind"`
	Mechanism   string `json:"mechanism"`
	MechanismEN string `json:"mechanism_en"`
}

type hintRule struct {
	match       *regexp.Regexp
	kind        string
	mechanism   string
	mechanismEN string
	// keywords are the words a user-written rule would use to state the same
	// mechanism in its own text. They exist only for harness_rule signatures:
	// those are rules the harness enforces on every session, and a user rule
	// can restate them. Looking for these words in the rule assets answers one
	// factual question — does any rule the user wrote mention this mechanism —
	// and nothing more. It is not a claim that a rule would have prevented
	// anything.
	keywords []string
}

// hintRules are tried in order; the first match wins. Each pattern is matched
// against the whole signature (category|tool|evidence line), so a rule can key
// on the evidence line or on the category the classifier already decided.
var hintRules = []hintRule{
	// The category the classifier already decided is the most reliable key
	// there is, so it is tried before any rule that reads the evidence line.
	{match: regexp.MustCompile(`^user_interrupt\|`),
		kind: HintUserStopped, mechanism: "用户主动中断了这一轮，harness 把中断本身记了下来。",
		mechanismEN: "The user stopped this turn; the harness recorded the interruption itself."},
	{match: regexp.MustCompile(`(?i)command not found|not recognized as an internal|is not recognized`),
		kind: HintEnvironment, mechanism: "被调用的命令不在这次会话的 PATH 里。",
		mechanismEN: "The command that was called is not on this session's PATH."},
	{match: regexp.MustCompile(`(?i)no module named|modulenotfounderror`),
		kind: HintEnvironment, mechanism: "运行用的 Python 解释器里没有安装这个包。",
		mechanismEN: "The Python interpreter that ran does not have this package installed."},
	{match: regexp.MustCompile(`(?i)file has not been read yet`),
		kind: HintHarnessRule, mechanism: "Claude Code 要求 Edit/Write 之前先用 Read 读过同一个文件。",
		mechanismEN: "Claude Code requires a Read of the same file before an Edit or Write.",
		keywords:    []string{"Read before Edit", "read before you edit", "read the file first", "先读后写", "先读再改"}},
	{match: regexp.MustCompile(`(?i)string to replace not found`),
		kind: HintToolMisuse, mechanism: "Edit 的 old_string 与文件里的内容对不上，工具拒绝改写。",
		mechanismEN: "Edit's old_string does not match what is in the file, so the tool refused the rewrite."},
	{match: regexp.MustCompile(`(?i)blocked by .*hook|pretooluse hook`),
		kind: HintUserHook, mechanism: "被用户自己在设置里配置的 PreToolUse hook 拦下。",
		mechanismEN: "Stopped by a PreToolUse hook the user configured in their own settings."},
	{match: regexp.MustCompile(`(?i)auto mode classifier`),
		kind: HintPermission, mechanism: "Claude Code 的自动批准分类器没有放行这个动作，调用被转成需要用户手动批准。",
		mechanismEN: "Claude Code's auto-approval classifier did not allow this action, so the call fell back to asking the user."},
	{match: regexp.MustCompile(`(?i)exec cell .*not found|exec cell # not found`),
		kind: HintToolMisuse, mechanism: "等待的后台执行单元不存在：它已经结束、被清理，或者从未启动。",
		mechanismEN: "The background exec cell being waited for does not exist: it already finished, was cleaned up, or never started."},
	{match: regexp.MustCompile(`(?i)unknown process id`),
		kind: HintToolMisuse, mechanism: "引用的后台进程 id 已经不存在，写入立即被拒绝。",
		mechanismEN: "The referenced background process id no longer exists, so the write was refused immediately."},
	{match: regexp.MustCompile(`(?i)failed to find expected lines|verification failed`),
		kind: HintToolMisuse, mechanism: "apply_patch 的上下文行与文件里的内容对不上，补丁被拒绝应用。",
		mechanismEN: "apply_patch's context lines do not match what is in the file, so the patch was refused."},
	{match: regexp.MustCompile(`(?i)thread limit`),
		kind: HintHarnessRule, mechanism: "harness 对同时运行的 agent 线程数有上限，超过上限的 spawn 被拒绝。",
		mechanismEN: "The harness caps how many agent threads may run at once; spawns past the cap are refused.",
		keywords:    []string{"thread limit", "agent thread", "线程上限", "并发上限", "spawn 上限"}},
	{match: regexp.MustCompile(`(?i)requires approval|permission to (use|run)`),
		kind: HintPermission, mechanism: "harness 在执行前要用户批准，这次没有拿到批准。",
		mechanismEN: "The harness asks the user to approve before running, and this run did not get that approval."},
	{match: regexp.MustCompile(`(?i)operation not permitted|permission denied|eacces|eperm`),
		kind: HintPermission, mechanism: "操作系统拒绝了这次文件或进程操作。",
		mechanismEN: "The operating system refused this file or process operation."},
	{match: regexp.MustCompile(`(?i)timed out after|timeout after|etimedout|deadline exceeded`),
		kind: HintTimeout, mechanism: "命令在 harness 给的时限内没有结束，被中止。",
		mechanismEN: "The command did not finish inside the time limit the harness gave it and was stopped."},
	{match: regexp.MustCompile(`exit 124\b`),
		kind: HintTimeout, mechanism: "命令在时限内没有结束，被按 timeout(1) 的约定以退出码 124 中止。",
		mechanismEN: "The command did not finish in time and was stopped with exit code 124, timeout(1)'s own convention."},
	{match: regexp.MustCompile(`exit (12[89]|1[3-8][0-9]|19[01])\b`),
		kind: HintEnvironment, mechanism: "退出码在 128–191 之间：命令进程被信号终止，信号编号 = 退出码 − 128（shell 约定）。",
		mechanismEN: "An exit code between 128 and 191 means the process was killed by a signal; signal number = exit code - 128 (shell convention)."},
	{match: regexp.MustCompile(`(?i)\brg exit 2\b`),
		kind: HintToolMisuse, mechanism: "退出码 2 是 rg 的“发生了错误”约定（如正则不合法）；shell 解析失败也以 2 报告。",
		mechanismEN: "Exit code 2 is rg's \"an error occurred\" convention (such as an invalid regex); a shell parse failure also reports 2."},
	{match: regexp.MustCompile(`(?i)\bgit exit 2\b`),
		kind: HintToolMisuse, mechanism: "git 以退出码 2 报告了错误；git diff --check 用它表示发现了空白问题。",
		mechanismEN: "git reported an error with exit code 2; git diff --check uses it to report whitespace problems."},
	{match: regexp.MustCompile(`(?i)\bls exit 2\b`),
		kind: HintToolMisuse, mechanism: "ls 以退出码 2 报告了较严重的问题，如目标不存在或无权访问。",
		mechanismEN: "ls reported serious trouble with exit code 2, such as a missing or inaccessible target."},
	{match: regexp.MustCompile(`(?i)found (#|\d+) errors?\.`),
		kind: HintBuild, mechanism: "代码检查工具（linter）自己报告了错误，命令以非零码退出。",
		mechanismEN: "The code checker (linter) reported errors itself, and the command exited non-zero."},
	{match: regexp.MustCompile(`(?i)syntax ?error`),
		kind: HintBuild, mechanism: "解释器在语法解析阶段拒绝了这份文件，命令以非零码退出。",
		mechanismEN: "The interpreter rejected the file while parsing it, and the command exited non-zero."},
	{match: regexp.MustCompile(`(?i)can't read|no such file or directory|does not exist|enoent`),
		kind: HintToolMisuse, mechanism: "命令给出的路径在这次会话的工作目录下不存在。",
		mechanismEN: "The path the command gave does not exist under this session's working directory."},
	{match: regexp.MustCompile(`(?i)inputvalidationerror|must be absolute`),
		kind: HintToolMisuse, mechanism: "工具参数不符合它自己的输入约束，调用在执行前就被拒绝。",
		mechanismEN: "The arguments do not satisfy the tool's own input constraints, so the call was refused before it ran."},
	{match: regexp.MustCompile(`^test_failure\|`),
		kind: HintTest, mechanism: "测试命令自己报告了失败用例。",
		mechanismEN: "The test command reported failing cases itself."},
	{match: regexp.MustCompile(`^build_error\|`),
		kind: HintBuild, mechanism: "编译或构建命令自己报告了错误。",
		mechanismEN: "The compile or build command reported errors itself."},
}

// LookupHint returns the mechanism behind a signature, or nil when no rule in
// the table matches. nil is "no rule covers this", never "no mechanism".
func LookupHint(signature string) *Hint {
	if signature == "" {
		return nil
	}
	for _, rule := range hintRules {
		if rule.match.MatchString(signature) {
			hint := Hint{Kind: rule.kind, Mechanism: rule.mechanism, MechanismEN: rule.mechanismEN}
			return &hint
		}
	}
	return nil
}

// CoverageKeywords returns the words a user-written rule would use to state
// this signature's mechanism, and nothing for a signature whose mechanism is
// not a harness rule. An empty result means the question "does a user rule
// mention this" cannot be asked, not that the answer is no.
func CoverageKeywords(signature string) []string {
	if signature == "" {
		return nil
	}
	for _, rule := range hintRules {
		if rule.match.MatchString(signature) {
			return rule.keywords
		}
	}
	return nil
}

// AllCoverageKeywords is every keyword in the table, so a caller can scan a
// rule's text once for all of them instead of once per signature.
func AllCoverageKeywords() []string {
	out := make([]string, 0)
	for _, rule := range hintRules {
		out = append(out, rule.keywords...)
	}
	return out
}
