package provider

import (
	"fmt"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/pelletier/go-toml/v2"
)

// EnvKey 派生该供应商专用的密钥环境变量名。
// 每个供应商一个独立变量,shim 只 export 自己那个,彼此不干扰,
// 也不会与用户已有的 OPENAI_API_KEY / AICODING_API_KEY 冲突。
func EnvKey(name string) string {
	var b strings.Builder
	b.WriteString("AK_KEY_")
	for _, r := range strings.ToUpper(name) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// ProfileName 是 codex 的 profile 名,对应 ~/.codex/<ProfileName>.config.toml。
func ProfileName(name string) string { return "ak-" + name }

// CodexEnv 计算 codex 供应商的环境变量方案。
// codex 的模型与端点走 profile 文件,环境变量只负责密钥。
func CodexEnv(name string, p config.Provider, key string) EnvPlan {
	env := map[string]string{}
	if key != "" {
		env[EnvKey(name)] = key
	}
	env["AK_PROVIDER"] = name
	if p.CodexHome != "" {
		env["CODEX_HOME"] = config.ExpandHome(p.CodexHome)
	}
	return EnvPlan{Set: sortedKV(env)}
}

// codexProviderID 返回 [model_providers.<id>] 的表名。
func codexProviderID(name string, p config.Provider) string {
	if p.ProviderID != "" {
		return p.ProviderID
	}
	return name
}

// codexProfile 是生成的 profile 文件结构。
//
// 只写标量键 + model_providers 一张表。绝不写 [[skills.config]]、[projects."..."]、
// [features]、[tui] —— 万一层叠语义是"同名顶层键整体替换",被替换的也只有我们
// 本就想覆盖的键,base config 里的 skills 与 projects 顶层键名不同,不受影响。
type codexProfile struct {
	ModelProvider  string                        `toml:"model_provider"`
	Model          string                        `toml:"model,omitempty"`
	ReasoningLevel string                        `toml:"model_reasoning_effort,omitempty"`
	ModelProviders map[string]codexProviderTable `toml:"model_providers"`
}

type codexProviderTable struct {
	Name    string `toml:"name"`
	BaseURL string `toml:"base_url"`
	WireAPI string `toml:"wire_api"`
	EnvKey  string `toml:"env_key"`
}

// CodexProfileTOML 渲染 profile 文件内容(不含标记行,标记由 shim 包统一加)。
func CodexProfileTOML(name string, p config.Provider) ([]byte, error) {
	id := codexProviderID(name, p)
	wire := p.WireAPI
	if wire == "" {
		wire = "responses"
	}
	prof := codexProfile{
		ModelProvider:  id,
		Model:          p.Model,
		ReasoningLevel: p.Reasoning,
		ModelProviders: map[string]codexProviderTable{
			id: {
				Name:    id,
				BaseURL: p.BaseURL,
				WireAPI: wire,
				EnvKey:  EnvKey(name),
			},
		},
	}
	data, err := toml.Marshal(prof)
	if err != nil {
		return nil, fmt.Errorf("渲染 codex profile %q: %w", name, err)
	}
	return data, nil
}

// CodexVariants 返回 codex shim 识别的变体名:reasoning 档位 + 自定义变体。
func CodexVariants(p config.Provider) []string {
	seen := map[string]bool{}
	var out []string
	for v := range config.ValidReasoning() {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for v := range p.Variants {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
