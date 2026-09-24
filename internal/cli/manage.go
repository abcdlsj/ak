package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/secrets"
	"github.com/abcdlsj/ak/internal/ui"
	"github.com/spf13/cobra"
)

func newAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "新增一个供应商",
		Long: `新增供应商并立即生成命令。

两种用法:
  ak add kimi --kind claude --base-url https://api.moonshot.cn/anthropic --key sk-... --model kimi-k2.7-code
  ak add kimi        # 不带参数时进入交互表单`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name := args[0]

			p, err := buildProviderFromFlags(cmd)
			if err != nil {
				return err
			}
			// 没给任何实质内容则视为要交互录入。
			if p.BaseURL == "" {
				p, err = interactiveAdd(name)
				if err != nil {
					return err
				}
			}
			return doAdd(cfg, name, p)
		},
	}

	f := cmd.Flags()
	f.String("kind", "claude", "引擎:claude 或 codex")
	f.String("base-url", "", "API 端点")
	f.String("key", "", "API key")
	f.String("model", "", "主模型")
	f.String("haiku", "", "haiku 档模型(claude)")
	f.String("sonnet", "", "sonnet 档模型(claude)")
	f.String("opus", "", "opus 档模型(claude)")
	f.String("key-field", "auth_token", "claude 鉴权变量:auth_token 或 api_key")
	f.String("wire-api", "responses", "codex 协议:responses 或 chat")
	f.String("reasoning", "", "codex 默认 reasoning effort")
	f.String("display", "", "列表里显示的名字")
	return cmd
}

func buildProviderFromFlags(cmd *cobra.Command) (config.Provider, error) {
	f := cmd.Flags()
	get := func(n string) string {
		v, _ := f.GetString(n)
		return strings.TrimSpace(v)
	}
	p := config.Provider{
		Kind:      config.Kind(get("kind")),
		BaseURL:   get("base-url"),
		Model:     get("model"),
		Display:   get("display"),
		APIKey:    get("key"),
		KeyField:  get("key-field"),
		WireAPI:   get("wire-api"),
		Reasoning: get("reasoning"),
		Haiku:     get("haiku"),
		Sonnet:    get("sonnet"),
		Opus:      get("opus"),
	}
	return p, nil
}

// doAdd 写入配置、校验、立即 sync。
func doAdd(cfg *config.Config, name string, p config.Provider) error {
	if err := config.ValidateName(name); err != nil {
		return err
	}
	if _, exists := cfg.Providers[name]; exists {
		return fmt.Errorf("供应商 %q 已存在,先 `ak rm %s`", name, name)
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]config.Provider{}
	}
	cfg.Providers[name] = p

	if err := config.Validate(cfg); err != nil {
		return err
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	return runSync(cfg, false)
}

func newRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove"},
		Short:   "删除供应商及其生成的命令",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name := args[0]
			if _, ok := cfg.Providers[name]; !ok {
				return fmt.Errorf("供应商 %q 不存在", name)
			}
			delete(cfg.Providers, name)
			if cfg.Settings.Default == name {
				cfg.Settings.Default = ""
			}
			if err := config.Save(cfg); err != nil {
				return err
			}
			return runSync(cfg, false)
		},
	}
}

func newDefaultCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "default [name]",
		Short: "查看或设置默认供应商",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				if cfg.Settings.Default == "" {
					fmt.Println("未设置默认供应商。")
					return nil
				}
				fmt.Printf("默认供应商: %s\n", cfg.Settings.Default)
				return nil
			}
			name := args[0]
			if _, ok := cfg.Providers[name]; !ok {
				return fmt.Errorf("供应商 %q 不存在", name)
			}
			cfg.Settings.Default = name
			if err := config.Save(cfg); err != nil {
				return err
			}
			fmt.Printf("默认供应商已设为 %s\n", name)
			return nil
		},
	}
}

// newEnvCmd 打印某个供应商将要注入的环境,用于调试。
// 这是验证 shim 行为最便宜的方式:不用真发起请求就能看到确切环境。
func newEnvCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "env <name> [variant]",
		Short: "打印供应商将要注入的环境变量",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name := args[0]
			p, ok := cfg.Providers[name]
			if !ok {
				return fmt.Errorf("供应商 %q 不存在", name)
			}
			variant := ""
			if len(args) > 1 {
				variant = args[1]
			}

			key, err := secrets.Default().Resolve(p)
			if err != nil {
				return err
			}

			if p.Kind == config.KindClaude {
				printEnvPlan(envPlanViewFrom(claudePlanFor(name, p, variant, key)), cfg.Settings.Prefix+name)
				return nil
			}
			printEnvPlan(envPlanViewFrom(codexPlanFor(name, p, key)), cfg.Settings.Prefix+name)
			fmt.Printf("  (codex 供应商还会生成 ~/.codex/ak-%s.config.toml,经 --profile 生效)\n", name)
			return nil
		},
	}
}

// interactiveAdd 用标准输入做最小交互。有 gum 时优先用它,交互更顺手。
func interactiveAdd(name string) (config.Provider, error) {
	r := bufio.NewReader(os.Stdin)
	ask := func(label, def string) string {
		if def != "" {
			fmt.Printf("%s [%s]: ", label, def)
		} else {
			fmt.Printf("%s: ", label)
		}
		line, _ := r.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			return def
		}
		return line
	}

	kind := ask("引擎 (claude/codex)", "claude")
	p := config.Provider{
		Kind:     config.Kind(kind),
		BaseURL:  ask("API 端点", ""),
		Model:    ask("主模型", ""),
		APIKey:   ask("API key", ""),
		Display:  ask("显示名", name),
		KeyField: ask("鉴权变量 (auth_token/api_key)", "auth_token"),
		WireAPI:  ask("codex 协议 (responses/chat)", "responses"),
	}
	return p, nil
}

func printEnvPlan(plan envPlanView, cmd string) {
	fmt.Printf("# %s 将注入:\n", cmd)
	for _, line := range plan {
		fmt.Printf("  %s\n", line)
	}
}

// envPlanView 是脱敏后的环境展示。
type envPlanView []string

func newUICmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ui",
		Short: "打开 TUI(无参数运行 ak 同此)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return ui.RunUI()
		},
	}
}
