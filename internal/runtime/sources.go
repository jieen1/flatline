package runtime

import (
	"context"
	"fmt"
	"os"
	"strings"

	"flatline/internal/adapters"
	"flatline/internal/history"
)

// The source registry is the daemon's record of where it reads from.
//
// | 谁做 | 做什么 | 结果 |
// | --- | --- | --- |
// | daemon 启动 | 把这次运行要读的每个根登记一行（已有的行不动） | 用户能在数据页看到并命名它们 |
// | daemon 每轮 refresh | 从注册表算出这一轮实际要读的根 | 被关掉的根不读；用户新增的根这一轮开始读 |
// | daemon 每轮 refresh 之后 | 按转写文件路径把会话挂到它所属的根上 | 会话能说出自己来自哪台机器的哪个目录 |
//
// Easy misreading: turning a source off does not remove the sessions already
// read from it. It stops the daemon reading that directory again; the record
// of what was read stays exactly where it is.

// RegisterSourceRoots records the roots this run was configured with. A root
// already in the registry keeps the label the user gave it.
func (a *App) RegisterSourceRoots(ctx context.Context, roots map[adapters.Source][]string) error {
	if a == nil || a.events == nil {
		return fmt.Errorf("runtime: source registry is not wired")
	}
	for kind, list := range roots {
		for _, root := range list {
			if strings.TrimSpace(root) == "" {
				continue
			}
			if err := a.events.RegisterSource(ctx, string(kind), root, kind.DisplayName()); err != nil {
				return err
			}
		}
	}
	return nil
}

// ConfiguredHistory is the history configuration this pass should actually
// read: the roots the daemon was started with, plus every enabled root the
// user added, minus every root the user turned off. A root that has since been
// removed from disk is dropped here rather than failing the pass.
func (a *App) ConfiguredHistory(ctx context.Context, base history.Config) (history.Config, error) {
	if a == nil || a.events == nil {
		return base, nil
	}
	sources, err := a.events.ListSources(ctx)
	if err != nil {
		return base, err
	}
	disabled := make(map[string]struct{})
	extra := make(map[adapters.Source][]string)
	for _, source := range sources {
		key := source.Kind + "\x00" + source.Root
		if !source.Enabled {
			disabled[key] = struct{}{}
			continue
		}
		if _, err := os.Stat(source.Root); err != nil {
			continue
		}
		extra[adapters.Source(source.Kind)] = append(extra[adapters.Source(source.Kind)], source.Root)
	}
	out := base
	out.ClaudeRoot, out.ClaudeRoots = mergeRoots(adapters.SourceClaudeCode, base.ClaudeRoot, base.ClaudeRoots, extra[adapters.SourceClaudeCode], disabled)
	out.CodexRoot, out.CodexRoots = mergeRoots(adapters.SourceCodex, base.CodexRoot, base.CodexRoots, extra[adapters.SourceCodex], disabled)
	out.DSHRoot, out.DSHRoots = mergeRoots(adapters.SourceDSH, base.DSHRoot, base.DSHRoots, extra[adapters.SourceDSH], disabled)
	if _, off := disabled[string(adapters.SourceOpenCode)+"\x00"+base.OpenCodeDB]; off {
		out.OpenCodeDB = ""
	}
	if _, off := disabled[string(adapters.SourceHermes)+"\x00"+base.HermesRoot]; off {
		out.HermesRoot = ""
	}
	return out, nil
}

// mergeRoots is the deduplicated list of roots for one kind with the ones the
// user turned off removed, split into the primary root and the rest. The split
// is not cosmetic: /ingest/health probes the primary root of each kind, so
// emptying it would report a source that is being read as not found.
func mergeRoots(kind adapters.Source, single string, configured, registered []string, disabled map[string]struct{}) (string, []string) {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(configured)+len(registered)+1)
	for _, root := range append(append([]string{single}, configured...), registered...) {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if _, off := disabled[string(kind)+"\x00"+root]; off {
			continue
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		out = append(out, root)
	}
	if len(out) == 0 {
		return "", nil
	}
	return out[0], out[1:]
}

// AttachSessionSources files each session under the configured root its
// transcript was read from.
func (a *App) AttachSessionSources(ctx context.Context) (int, error) {
	if a == nil || a.events == nil {
		return 0, fmt.Errorf("runtime: source registry is not wired")
	}
	return a.events.AttachSessionSources(ctx)
}
