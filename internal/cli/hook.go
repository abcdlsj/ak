package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/usage"
	"github.com/spf13/cobra"
)

// hookMarker 标识这条 hook 由 ak 管理,卸载时据此识别。
const hookMarker = "ak-session-attrib"

// newHookCmd 管理 SessionStart hook。
//
// claude 的 session 日志不记录连的是哪个供应商,要靠这条 hook 把
// session_id → AK_PROVIDER 记下来,用量统计才能归属到供应商。
func newHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "安装或卸载用量归属所需的 SessionStart hook",
	}
	cmd.AddCommand(newHookInstallCmd(), newHookUninstallCmd())
	return cmd
}

func newHookInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "安装 SessionStart hook(让 claude 用量能归到供应商)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return installHook()
		},
	}
}

func newHookUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "卸载 hook",
		RunE: func(cmd *cobra.Command, args []string) error {
			return uninstallHook()
		},
	}
}

// hookScript 是 hook 本体。只追加一行 jsonl,不做任何可能阻塞启动的事。
func hookScript() string {
	bin, err := os.Executable()
	if err != nil || bin == "" {
		bin = "ak"
	}
	return fmt.Sprintf(`%s __record-session "$AK_PROVIDER"`, bin)
}

func settingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

func installHook() error {
	path := settingsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取 %s: %w", path, err)
	}

	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("解析 %s: %w", path, err)
	}

	// 已有 hooks 则不重复装。
	if raw, ok := settings["hooks"]; ok && contains(string(raw), hookMarker) {
		fmt.Println("hook 已安装。")
		return nil
	}

	entry := map[string]any{
		"matcher": "startup|resume",
		"hooks": []map[string]string{
			{"type": "command", "command": hookScript() + " # " + hookMarker},
		},
	}

	var hooks map[string][]map[string]any
	if raw, ok := settings["hooks"]; ok {
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return fmt.Errorf("解析现有 hooks: %w", err)
		}
	} else {
		hooks = map[string][]map[string]any{}
	}
	hooks["SessionStart"] = append(hooks["SessionStart"], entry)

	hooksRaw, err := json.Marshal(hooks)
	if err != nil {
		return err
	}
	settings["hooks"] = hooksRaw

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := config.AtomicWrite(path, out, 0o600); err != nil {
		return err
	}
	fmt.Println("已安装 SessionStart hook。")
	fmt.Println("之后 claude 的用量就能按供应商归因了(之前的记录仍记为 unknown)。")
	return nil
}

func uninstallHook() error {
	path := settingsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		return err
	}
	raw, ok := settings["hooks"]
	if !ok || !contains(string(raw), hookMarker) {
		fmt.Println("没有 ak 装的 hook。")
		return nil
	}

	var hooks map[string][]map[string]any
	if err := json.Unmarshal(raw, &hooks); err != nil {
		return err
	}
	for event, list := range hooks {
		var kept []map[string]any
		for _, e := range list {
			if contains(mustJSON(e), hookMarker) {
				continue
			}
			kept = append(kept, e)
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	} else {
		hooksRaw, _ := json.Marshal(hooks)
		settings["hooks"] = hooksRaw
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return config.AtomicWrite(path, out, 0o600)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// newRecordSessionCmd 是隐藏命令,由 hook 调用,不给人手敲。
//
// claude 通过 stdin 传 JSON 给 hook(含 session_id),不是环境变量 ——
// 第一次实现时按环境变量取,结果 session_id 全是空的。
func newRecordSessionCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "__record-session",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			provider := ""
			if len(args) > 0 {
				provider = args[0]
			}
			if provider == "" {
				return nil
			}

			sessionID := sessionIDFromStdin()
			if sessionID == "" {
				return nil // 拿不到 id 就放弃,不写无用的空记录
			}
			return usage.RecordSession(sessionID, provider)
		},
	}
}

// sessionIDFromStdin 从 hook 的 stdin JSON 里取 session_id。
func sessionIDFromStdin() string {
	var payload struct {
		SessionID string `json:"session_id"`
	}
	// 只读一次;空 stdin 时直接返回空。
	dec := json.NewDecoder(os.Stdin)
	if err := dec.Decode(&payload); err != nil {
		return ""
	}
	return payload.SessionID
}
