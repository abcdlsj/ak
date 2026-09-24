package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/spf13/cobra"
)

func newImportCmd() *cobra.Command {
	var from string
	cmd := &cobra.Command{
		Use:   "import",
		Short: "从现有配置导入供应商",
		Long: `从现有配置读取供应商,写进 ak。

  ak import --from claude-settings   # 读 ~/.claude/settings.json 的 env
  ak import --from codexa            # 读 ~/.codex/profiles/*/`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			switch from {
			case "claude-settings":
				return importClaudeSettings(cfg)
			case "codexa":
				return importCodexa(cfg)
			default:
				return fmt.Errorf("--from 只能是 claude-settings 或 codexa,当前是 %q", from)
			}
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "数据源:claude-settings 或 codexa")
	cmd.MarkFlagRequired("from")
	return cmd
}

// importClaudeSettings 把 settings.json 里的供应商键收进 ak。
//
// 只搬供应商专属键。全局偏好(NODE_EXTRA_CA_CERTS、
// CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC 等)原地保留,因为它们对所有供应商都该生效。
// 搬完后打印待删清单 —— 删除动作交给用户手动执行,ak 不写 settings.json 的供应商键。
func importClaudeSettings(cfg *config.Config) error {
	path := claudeSettingsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取 %s: %w", path, err)
	}
	var settings struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("解析 %s: %w", path, err)
	}
	env := settings.Env

	base := env["ANTHROPIC_BASE_URL"]
	if base == "" {
		return fmt.Errorf("%s 的 env 里没有 ANTHROPIC_BASE_URL,无从导入", path)
	}

	// "default" 是 ak 的子命令名,不能用作供应商名。
	name := "imported"
	if _, exists := cfg.Providers[name]; exists {
		for i := 2; ; i++ {
			cand := fmt.Sprintf("imported%d", i)
			if _, exists := cfg.Providers[cand]; !exists {
				name = cand
				break
			}
		}
	}

	p := config.Provider{
		Kind:    config.KindClaude,
		BaseURL: base,
		Model:   env["ANTHROPIC_MODEL"],
		Haiku:   env["ANTHROPIC_DEFAULT_HAIKU_MODEL"],
		Sonnet:  env["ANTHROPIC_DEFAULT_SONNET_MODEL"],
		Opus:    env["ANTHROPIC_DEFAULT_OPUS_MODEL"],
		Env:     map[string]string{},
	}
	if k := env["ANTHROPIC_API_KEY"]; k != "" {
		p.APIKey, p.KeyField = k, "api_key"
	} else if k := env["ANTHROPIC_AUTH_TOKEN"]; k != "" {
		p.APIKey, p.KeyField = k, "auth_token"
	}
	// 其余长尾供应商键一并带走,不留在 settings.json 里。
	// 注意只收供应商专属键;全局偏好(NODE_EXTRA_CA_CERTS、
	// CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC、CLAUDE_AUTOCOMPACT_PCT_OVERRIDE 等)
	// 对所有供应商都该生效,必须留在 settings.json 里。
	for _, k := range []string{
		"CLAUDE_CODE_SUBAGENT_MODEL",
		"CLAUDE_CODE_MAX_CONTEXT_TOKENS",
		"CLAUDE_CODE_AUTO_COMPACT_WINDOW",
		"API_TIMEOUT_MS",
		"CLAUDE_CODE_MAX_OUTPUT_TOKENS",
	} {
		if v := env[k]; v != "" {
			p.Env[k] = v
		}
	}
	if len(p.Env) == 0 {
		p.Env = nil
	}

	cfg.Providers[name] = p
	if cfg.Settings.Default == "" {
		cfg.Settings.Default = name
	}
	if err := config.Validate(cfg); err != nil {
		return err
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	if err := runSync(cfg, false); err != nil {
		return err
	}

	fmt.Printf("\n已导入为供应商 %q。\n\n", name)
	fmt.Println("接下来请手动从 settings.json 删除这些键(否则它们会覆盖 ak-* 的环境变量):")
	for _, k := range providerEnvKeys {
		if _, ok := env[k]; ok {
			fmt.Printf("    %s\n", k)
		}
	}
	fmt.Printf("\n  文件: %s\n", path)
	fmt.Println("  请保留其中的全局偏好键(NODE_EXTRA_CA_CERTS、CLAUDE_AUTOCOMPACT_PCT_OVERRIDE 等)。")
	fmt.Printf("\n删完后用 `ak-%s -p 'reply OK'` 验证。\n", name)
	return nil
}

// importCodexa 读 codexa 的 profile 并合并重复项。
// 全程只读源数据:profile 目录里有 GB 级日志库和指回主 home 的 symlink,误删代价不可逆。
func importCodexa(cfg *config.Config) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	root := filepath.Join(home, ".codex", "profiles")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return fmt.Errorf("没有 %s,可能没用过 codexa", root)
	}
	if err != nil {
		return err
	}

	type imported struct {
		name string
		p    config.Provider
	}
	var got []imported
	seen := map[string]string{} // (base_url,model,wire) -> 已用的供应商名

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		profile := e.Name()
		cfgPath := filepath.Join(root, profile, "config.toml")
		raw, err := os.ReadFile(cfgPath)
		if err != nil || len(raw) == 0 {
			fmt.Printf("  跳过 %s:无 config.toml 或为空\n", profile)
			continue
		}

		prof, err := parseCodexaProfile(raw)
		if err != nil {
			fmt.Printf("  跳过 %s:%v\n", profile, err)
			continue
		}
		if prof.BaseURL == "" {
			fmt.Printf("  跳过 %s:解析不到 base_url\n", profile)
			continue
		}

		// 去重:base_url+model+wire 相同的视为同一供应商,保留较短的名字。
		key := prof.BaseURL + "\x00" + prof.Model + "\x00" + prof.WireAPI
		if prev, ok := seen[key]; ok {
			fmt.Printf("  %s 与 %s 配置相同,已合并\n", profile, prev)
			continue
		}

		name := uniqueName(cfg, profile)
		p := config.Provider{
			Kind:       config.KindCodex,
			BaseURL:    prof.BaseURL,
			Model:      prof.Model,
			Reasoning:  prof.Reasoning,
			WireAPI:    prof.WireAPI,
			ProviderID: prof.ProviderID,
			APIKey:     codexaKey(filepath.Join(root, profile), prof.EnvKey),
			Display:    profile,
		}
		if p.APIKey == "" {
			fmt.Printf("  注意:%s 没读到 key,稍后可用 `ak edit %s` 补\n", profile, name)
		}
		got = append(got, imported{name: name, p: p})
		seen[key] = name
	}

	if len(got) == 0 {
		return fmt.Errorf("没有可导入的 profile")
	}

	if cfg.Providers == nil {
		cfg.Providers = map[string]config.Provider{}
	}
	for _, g := range got {
		cfg.Providers[g.name] = g.p
	}
	if err := config.Validate(cfg); err != nil {
		return err
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	if err := runSync(cfg, false); err != nil {
		return err
	}

	fmt.Printf("\n已导入 %d 个供应商。旧的 ~/.codex/profiles/ 与 codex-* 命令未做任何改动。\n", len(got))
	fmt.Println("确认 `ak-<name>` 行为符合预期后,可用 `ak prune-codexa` 清理旧命令(默认 dry-run)。")
	return nil
}

// codexaProfile 是从 codexa profile 解析出的信息。
type codexaProfile struct {
	ProviderID string
	BaseURL    string
	WireAPI    string
	Model      string
	Reasoning  string
	EnvKey     string
}

// uniqueName 返回不与现有供应商冲突的名字。
func uniqueName(cfg *config.Config, base string) string {
	if _, ok := cfg.Providers[base]; !ok {
		return base
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s%d", base, i)
		if _, ok := cfg.Providers[cand]; !ok {
			return cand
		}
	}
}
