package shim

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/provider"
	"github.com/abcdlsj/ak/internal/secrets"
)

// shimMode 是 shim 的权限位。700 而非 755:文件内嵌明文密钥,
// 不给同机其他用户读取的机会。
const shimMode = 0o700

// Action 是对单个文件的处理结果。
type Action string

const (
	ActionCreated   Action = "created"
	ActionUpdated   Action = "updated"
	ActionUnchanged Action = "unchanged"
	ActionRemoved   Action = "removed"
	ActionSkipped   Action = "skipped"
)

// Result 是一条同步结果。
type Result struct {
	Path   string
	Action Action
	Reason string // Skipped 时说明原因
}

// Report 是一次 sync 的全部结果。
type Report struct {
	Results []Result
	DryRun  bool
}

// Counts 按 action 汇总。
func (r Report) Counts() map[Action]int {
	m := map[Action]int{}
	for _, res := range r.Results {
		m[res.Action]++
	}
	return m
}

// Syncer 生成并回收 ak 的产物。
type Syncer struct {
	Cfg      *config.Config
	Resolver secrets.Resolver
	DryRun   bool
}

// Sync 生成全部 shim 与 codex profile,并回收孤儿产物。
func (s *Syncer) Sync() (Report, error) {
	rep := Report{DryRun: s.DryRun}

	binDir := config.ExpandHome(s.Cfg.Settings.BinDir)
	if !s.DryRun {
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			return rep, err
		}
	}

	claudeBin := s.resolveBin(s.Cfg.Settings.ClaudeBin, "claude")
	codexBin := s.resolveBin(s.Cfg.Settings.CodexBin, "codex")

	// 记录本次应当存在的产物,其余带标记的同类文件视为孤儿。
	want := map[string]bool{}

	for _, name := range s.Cfg.Names() {
		p := s.Cfg.Providers[name]
		key, err := s.Resolver.Resolve(p)
		if err != nil {
			return rep, fmt.Errorf("供应商 %s 的密钥: %w", name, err)
		}

		switch p.Kind {
		case config.KindClaude:
			spec := s.claudeSpec(name, p, key, claudeBin)
			path := filepath.Join(binDir, s.Cfg.Settings.Prefix+name)
			want[path] = true
			rep.Results = append(rep.Results, s.writeFile(path, Render(spec), shimMode))

		case config.KindCodex:
			spec := s.codexSpec(name, p, key, codexBin)
			path := filepath.Join(binDir, s.Cfg.Settings.Prefix+name)
			want[path] = true
			rep.Results = append(rep.Results, s.writeFile(path, Render(spec), shimMode))

			profPath, content, err := s.codexProfile(name, p)
			if err != nil {
				return rep, err
			}
			want[profPath] = true
			rep.Results = append(rep.Results, s.writeFile(profPath, content, 0o600))
		}
	}

	orphans, err := s.collectOrphans(binDir, want)
	if err != nil {
		return rep, err
	}
	rep.Results = append(rep.Results, orphans...)

	sort.Slice(rep.Results, func(i, j int) bool { return rep.Results[i].Path < rep.Results[j].Path })
	return rep, nil
}

func (s *Syncer) claudeSpec(name string, p config.Provider, key, bin string) Spec {
	base := provider.ClaudeEnv(name, p, "", key)
	variants := provider.ClaudeVariants(p)

	// 为每个变体算出相对基础环境的增量,只写真正变化的键。
	plans := map[string][]provider.KV{}
	baseMap := map[string]string{}
	for _, kv := range base.Set {
		baseMap[kv.Key] = kv.Value
	}
	for _, v := range variants {
		vp := provider.ClaudeEnv(name, p, v, key)
		var delta []provider.KV
		for _, kv := range vp.Set {
			if baseMap[kv.Key] != kv.Value {
				delta = append(delta, kv)
			}
		}
		if len(delta) > 0 {
			plans[v] = delta
		}
	}
	// 没有实际增量的变体不必进 dispatch。
	var effective []string
	for _, v := range variants {
		if len(plans[v]) > 0 {
			effective = append(effective, v)
		}
	}

	return Spec{
		Name: name, Kind: config.KindClaude,
		Bin: bin, BinName: "claude", EnvVar: "AK_CLAUDE_BIN",
		Plan: base, Variants: effective, VariantPlans: plans,
	}
}

func (s *Syncer) codexSpec(name string, p config.Provider, key, bin string) Spec {
	return Spec{
		Name: name, Kind: config.KindCodex,
		Bin: bin, BinName: "codex", EnvVar: "AK_CODEX_BIN",
		Plan:              provider.CodexEnv(name, p, key),
		Variants:          provider.CodexVariants(p),
		Profile:           provider.ProfileName(name),
		ReasoningVariants: true,
	}
}

// codexProfile 渲染 ~/.codex/ak-<name>.config.toml。
func (s *Syncer) codexProfile(name string, p config.Provider) (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	body, err := provider.CodexProfileTOML(name, p)
	if err != nil {
		return "", "", err
	}
	m := Marker{Kind: string(config.KindCodex), Provider: name, Hash: hashBody(string(body))}
	content := m.Line() + "\n# DO NOT EDIT — 由 ak 生成,改动会在下次 `ak sync` 时被覆盖。\n" + string(body)
	path := filepath.Join(home, ".codex", provider.ProfileName(name)+".config.toml")
	return path, content, nil
}

// writeFile 幂等写入:内容未变则不落盘、不动 mtime。
func (s *Syncer) writeFile(path, content string, mode os.FileMode) Result {
	existing, err := os.ReadFile(path)
	switch {
	case err == nil && string(existing) == content:
		return Result{Path: path, Action: ActionUnchanged}
	case err == nil:
		// 已存在但内容不同:必须确认是 ak 生成的才覆盖,否则可能踩用户自己的脚本。
		if _, ok := readMarker(path); !ok {
			return Result{Path: path, Action: ActionSkipped,
				Reason: "已存在且不是 ak 生成的文件"}
		}
		if s.DryRun {
			return Result{Path: path, Action: ActionUpdated}
		}
		if err := config.AtomicWrite(path, []byte(content), mode); err != nil {
			return Result{Path: path, Action: ActionSkipped, Reason: err.Error()}
		}
		return Result{Path: path, Action: ActionUpdated}
	case os.IsNotExist(err):
		if s.DryRun {
			return Result{Path: path, Action: ActionCreated}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return Result{Path: path, Action: ActionSkipped, Reason: err.Error()}
		}
		if err := config.AtomicWrite(path, []byte(content), mode); err != nil {
			return Result{Path: path, Action: ActionSkipped, Reason: err.Error()}
		}
		return Result{Path: path, Action: ActionCreated}
	default:
		return Result{Path: path, Action: ActionSkipped, Reason: err.Error()}
	}
}

// collectOrphans 找出并删除不再需要的产物。
//
// 删除前必须同时满足五个条件,任一不满足即跳过:
//  1. 位于 bin_dir(或 ~/.codex)
//  2. 名字匹配 prefix
//  3. 是普通文件,不跟随 symlink
//  4. owner 是当前用户
//  5. 头部带 ak:generated 标记且版本已知
//
// ~/.local/bin 里有 warren、minions、agy、aicoding 等用户自有工具,误删代价极高。
func (s *Syncer) collectOrphans(binDir string, want map[string]bool) ([]Result, error) {
	var out []Result

	dirs := []struct {
		dir    string
		prefix string
	}{
		{binDir, s.Cfg.Settings.Prefix},
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, struct {
			dir    string
			prefix string
		}{filepath.Join(home, ".codex"), "ak-"})
	}

	for _, d := range dirs {
		entries, err := os.ReadDir(d.dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasPrefix(name, d.prefix) {
				continue // 前缀不匹配:第一道保险
			}
			path := filepath.Join(d.dir, name)
			if want[path] {
				continue
			}
			if res, ok := s.checkRemovable(path, e); !ok {
				out = append(out, res)
				continue
			}
			if s.DryRun {
				out = append(out, Result{Path: path, Action: ActionRemoved})
				continue
			}
			if err := os.Remove(path); err != nil {
				out = append(out, Result{Path: path, Action: ActionSkipped, Reason: err.Error()})
				continue
			}
			out = append(out, Result{Path: path, Action: ActionRemoved})
		}
	}
	return out, nil
}

// checkRemovable 执行删除前的全部安全检查。
func (s *Syncer) checkRemovable(path string, e os.DirEntry) (Result, bool) {
	// 用 Lstat 而非 Stat:不跟随 symlink,避免顺着链接删到要害文件。
	fi, err := os.Lstat(path)
	if err != nil {
		return Result{Path: path, Action: ActionSkipped, Reason: err.Error()}, false
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return Result{Path: path, Action: ActionSkipped, Reason: "是 symlink,拒绝跟随"}, false
	}
	if !fi.Mode().IsRegular() {
		return Result{Path: path, Action: ActionSkipped, Reason: "不是普通文件"}, false
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		if int(st.Uid) != os.Getuid() {
			return Result{Path: path, Action: ActionSkipped, Reason: "owner 不是当前用户"}, false
		}
	}
	m, ok := readMarker(path)
	if !ok {
		return Result{Path: path, Action: ActionSkipped, Reason: "没有 ak:generated 标记"}, false
	}
	if m.Version > MarkerVersion {
		return Result{Path: path, Action: ActionSkipped,
			Reason: fmt.Sprintf("标记版本 v%d 高于本程序支持的 v%d", m.Version, MarkerVersion)}, false
	}
	return Result{}, true
}

// resolveBin 解析引擎可执行文件路径。
// 只取 PATH 里查到的那一层,不做 EvalSymlinks —— claude 的 ~/.local/bin/claude 是
// 指向 versions/<ver> 的 symlink,穿透后自升级会让 shim 全部指向旧版本。
func (s *Syncer) resolveBin(configured, name string) string {
	if configured != "" {
		return config.ExpandHome(configured)
	}
	if p, err := exec.LookPath(name); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	return ""
}
