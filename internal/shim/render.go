package shim

import (
	"fmt"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/provider"
)

// Spec 描述一个要生成的 shim。
type Spec struct {
	Name     string // 供应商名
	Kind     config.Kind
	Bin      string // 钉住的引擎可执行文件绝对路径,空则完全依赖 PATH
	BinName  string // 引擎命令名,PATH 兜底时用
	EnvVar   string // 覆盖 Bin 的环境变量名,如 AK_CLAUDE_BIN
	Plan     provider.EnvPlan
	Variants []string // shim 需识别的变体名
	// VariantPlans 是每个变体相对基础环境的增量,claude 用。
	VariantPlans map[string][]provider.KV
	// Profile 非空时作为 codex 的 --profile 参数。
	Profile string
	// ReasoningVariants 为 true 时变体转成 -c model_reasoning_effort=,codex 用。
	ReasoningVariants bool
}

// shellQuote 用单引号包裹并转义内部单引号,保证任意内容都安全。
// 单引号内 bash 不做任何展开,所以 $ ` " \ 换行全部字面化。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Render 渲染 shim 脚本内容,返回完整文件(含标记行)。
func Render(s Spec) string {
	var b strings.Builder

	body := renderBody(s)
	m := Marker{Kind: string(s.Kind), Provider: s.Name, Hash: hashBody(body)}

	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString(m.Line())
	b.WriteString("\n# DO NOT EDIT — 由 ak 生成,改动会在下次 `ak sync` 时被覆盖。\n")
	b.WriteString(body)
	return b.String()
}

func renderBody(s Spec) string {
	var b strings.Builder
	b.WriteString("set -euo pipefail\n\n")

	if len(s.Variants) > 0 {
		b.WriteString(renderVariantDispatch(s))
	}

	b.WriteString("# 供应商环境(由 ak 管理)\n")
	for _, kv := range s.Plan.Set {
		fmt.Fprintf(&b, "export %s=%s\n", kv.Key, shellQuote(kv.Value))
	}
	// 显式 unset 未设置的供应商键:嵌套启动时会从父进程继承,不清会静默走错供应商。
	for _, k := range s.Plan.Unset {
		fmt.Fprintf(&b, "unset %s || true\n", k)
	}
	b.WriteString("\n")

	if len(s.Variants) > 0 {
		b.WriteString(renderVariantApply(s))
	}

	b.WriteString(renderExec(s))
	return b.String()
}

// renderVariantDispatch 生成首参变体识别。
// 只在首参精确匹配变体名时吞掉它,否则原样透传 —— 不能伤到 `ak-kimi -p "..."`。
func renderVariantDispatch(s Spec) string {
	var b strings.Builder
	b.WriteString("# 变体:仅当首参精确匹配时吞掉,其余原样透传\n")
	b.WriteString("ak_variant=\"${AK_VARIANT:-}\"\n")
	b.WriteString("if [ $# -gt 0 ]; then\n")
	b.WriteString("  case \"$1\" in\n")
	fmt.Fprintf(&b, "    %s)\n", strings.Join(s.Variants, "|"))
	b.WriteString("      ak_variant=\"$1\"; shift ;;\n")
	b.WriteString("  esac\n")
	b.WriteString("fi\n\n")
	return b.String()
}

// renderVariantApply 生成变体生效逻辑。
func renderVariantApply(s Spec) string {
	var b strings.Builder

	if s.ReasoningVariants {
		// codex:变体转成 -c 覆盖。bash 3.2 下空数组展开必须用 ${arr[@]+"${arr[@]}"}。
		b.WriteString("ak_extra=()\n")
		b.WriteString("if [ -n \"$ak_variant\" ]; then\n")
		b.WriteString("  ak_extra+=(-c \"model_reasoning_effort=\\\"$ak_variant\\\"\")\n")
		b.WriteString("fi\n\n")
		return b.String()
	}

	// claude:变体覆盖环境变量。
	b.WriteString("case \"$ak_variant\" in\n")
	for _, v := range s.Variants {
		kvs := s.VariantPlans[v]
		if len(kvs) == 0 {
			continue
		}
		fmt.Fprintf(&b, "  %s)\n", v)
		for _, kv := range kvs {
			fmt.Fprintf(&b, "    export %s=%s\n", kv.Key, shellQuote(kv.Value))
		}
		b.WriteString("    ;;\n")
	}
	b.WriteString("  *) ;;\n")
	b.WriteString("esac\n")
	b.WriteString("if [ -n \"$ak_variant\" ]; then export AK_VARIANT=\"$ak_variant\"; fi\n\n")
	return b.String()
}

// renderExec 生成 exec 段。
// 钉绝对路径 + PATH 兜底:warren/tmux/VSCode 起的非交互 shell 可能不含 ~/.local/bin。
// 但只钉到 symlink 那层,不解析穿透到版本目录,否则引擎自升级后 shim 全指向旧版。
func renderExec(s Spec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "ak_bin=\"${%s:-%s}\"\n", s.EnvVar, shellEnvDefault(s.Bin))
	b.WriteString("if [ ! -x \"$ak_bin\" ]; then\n")
	fmt.Fprintf(&b, "  ak_bin=\"$(command -v %s || true)\"\n", s.BinName)
	b.WriteString("fi\n")
	b.WriteString("if [ -z \"$ak_bin\" ]; then\n")
	fmt.Fprintf(&b, "  echo 'ak: 找不到 %s,请检查 PATH 或设置 %s' >&2\n", s.BinName, s.EnvVar)
	b.WriteString("  exit 127\n")
	b.WriteString("fi\n")

	switch {
	case s.Profile != "" && s.ReasoningVariants:
		fmt.Fprintf(&b, "exec \"$ak_bin\" --profile %s ${ak_extra[@]+\"${ak_extra[@]}\"} \"$@\"\n",
			shellQuote(s.Profile))
	case s.Profile != "":
		fmt.Fprintf(&b, "exec \"$ak_bin\" --profile %s \"$@\"\n", shellQuote(s.Profile))
	default:
		b.WriteString("exec \"$ak_bin\" \"$@\"\n")
	}
	return b.String()
}

// shellEnvDefault 渲染 ${VAR:-default} 里的 default 部分。
func shellEnvDefault(v string) string {
	if v == "" {
		return ""
	}
	return shellQuote(v)
}
