package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
)

// parseCodexaProfile 从 codexa 的 config.toml + auth.json 提取供应商信息。
//
// 只读。codexa 的 profile 目录里有 GB 级日志库,且部分日志是指回主 home 的
// symlink,误删或误跟随 symlink 写会不可逆地损坏主 home,所以这里只读不写。
func parseCodexaProfile(raw []byte) (codexaProfile, error) {
	var doc struct {
		ModelProvider       string `toml:"model_provider"`
		Model               string `toml:"model"`
		ReasoningEffort     string `toml:"model_reasoning_effort"`
		DisableRespStorage  bool   `toml:"disable_response_storage"`
		PlanReasoningEffort string `toml:"plan_mode_reasoning_effort"`
		ModelProviders      map[string]struct {
			BaseURL string `toml:"base_url"`
			WireAPI string `toml:"wire_api"`
			EnvKey  string `toml:"env_key"`
			Name    string `toml:"name"`
		} `toml:"model_providers"`
	}
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return codexaProfile{}, err
	}

	if doc.ModelProvider == "" {
		return codexaProfile{}, nil
	}
	table, ok := doc.ModelProviders[doc.ModelProvider]
	if !ok {
		return codexaProfile{}, fmt.Errorf("model_provider %q 没有对应的 [model_providers] 表", doc.ModelProvider)
	}

	out := codexaProfile{
		Model:      doc.Model,
		Reasoning:  doc.ReasoningEffort,
		ProviderID: doc.ModelProvider,
		WireAPI:    "responses",
		EnvKey:     table.EnvKey,
		BaseURL:    table.BaseURL,
	}
	if table.WireAPI != "" {
		out.WireAPI = table.WireAPI
	}

	// 无效的 reasoning 值直接丢弃:写进 ak 会过不了校验。
	if out.Reasoning != "" && !config.ValidReasoning()[out.Reasoning] {
		out.Reasoning = ""
	}
	// plan_mode_reasoning_effort 也是 codexa profile 里的已知字段,当前 ak 不建模。
	if doc.PlanReasoningEffort != "" && !config.ValidReasoning()[doc.PlanReasoningEffort] {
		// codexa 的 bili-luna 里有个 typo "hign",忽略即可。
		_ = doc.PlanReasoningEffort
	}
	return out, nil
}

// codexaKey 从 profile 的 auth.json 按 env_key 取密钥。
// 解析失败时返回空串,由调用方报告"需要补 key",不阻断导入。
func codexaKey(profileDir, envKey string) string {
	if envKey == "" {
		envKey = "OPENAI_API_KEY"
	}
	data, err := os.ReadFile(filepath.Join(profileDir, "auth.json"))
	if err != nil {
		return ""
	}
	var auth map[string]string
	if err := json.Unmarshal(data, &auth); err != nil {
		return ""
	}
	if k, ok := auth[envKey]; ok && k != "" {
		return k
	}
	// 兜底:env_key 指向的键不在 auth.json 里时,取第一个非空值。
	for _, v := range auth {
		if v != "" {
			return v
		}
	}
	return ""
}

// pruneCodexaCmd 清理 codexa 留下的旧命令。默认 dry-run,逐个确认。
func newPruneCodexaCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "prune-codexa",
		Short: "清理 codexa 生成的 codex-* 旧命令(默认 dry-run)",
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			bin := filepath.Join(home, ".local", "bin")
			entries, err := os.ReadDir(bin)
			if err != nil {
				return err
			}
			var candidates []string
			for _, e := range entries {
				n := e.Name()
				if len(n) > 6 && n[:6] == "codex-" && n != "codex-alias" && n != "codexalias" {
					candidates = append(candidates, filepath.Join(bin, n))
				}
			}
			if len(candidates) == 0 {
				fmt.Println("没有 codex-* 旧命令。")
				return nil
			}
			for _, c := range candidates {
				fmt.Println("  ", filepath.Base(c))
			}
			if !yes {
				fmt.Println("\n(dry-run) 加 --yes 确认删除。codexa 的 profile 目录不受影响。")
				return nil
			}
			for _, c := range candidates {
				if err := os.Remove(c); err != nil {
					warnf("删除 %s 失败: %v", c, err)
					continue
				}
				fmt.Printf("已删除 %s\n", filepath.Base(c))
			}
			fmt.Println("~/.codex/profiles/ 未改动,可随时回退。")
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "确认删除")
	return cmd
}
