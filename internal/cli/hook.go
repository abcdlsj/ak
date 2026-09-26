package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/usage"
	"github.com/spf13/cobra"
)

// hookMarker marks a hook as managed by ak, so uninstall can find it.
const hookMarker = "ak-session-attrib"

// newHookCmd manages the SessionStart hook.
//
// Claude's session logs do not record which provider a session used, so this
// hook writes session_id -> AK_PROVIDER; usage aggregation then joins on it to
// attribute tokens to a provider.
func newHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Install or remove the SessionStart hook required for usage attribution",
	}
	cmd.AddCommand(newHookInstallCmd(), newHookUninstallCmd())
	return cmd
}

func newHookInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install the SessionStart hook so claude usage is attributed to providers",
		RunE: func(cmd *cobra.Command, args []string) error {
			return installHook()
		},
	}
}

func newHookUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the hook",
		RunE: func(cmd *cobra.Command, args []string) error {
			return uninstallHook()
		},
	}
}

// hookScript is the hook body. It appends a single JSON line and does
// nothing that could delay startup.
//
// ak is looked up on PATH first and the path of this binary is only a
// fallback: an absolute path alone breaks after `go install` to another
// GOBIN or a package-manager upgrade.
func hookScript() string {
	fallback := "ak"
	if exe, err := os.Executable(); err == nil && exe != "" {
		fallback = exe
	}
	return fmt.Sprintf(`"$(command -v ak || echo %s)" __record-session "${AK_PROVIDER:-}" # %s`,
		shellQuote(fallback), hookMarker)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func settingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

func installHook() error {
	path := settingsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	entry := map[string]any{
		"matcher": "startup|resume",
		"hooks": []map[string]string{
			{"type": "command", "command": hookScript()},
		},
	}

	hooks := map[string][]map[string]any{}
	if raw, ok := settings["hooks"]; ok {
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return fmt.Errorf("parse existing hooks: %w", err)
		}
	}
	// Re-installing replaces an older ak entry, which is how a stale
	// command line gets upgraded.
	removeMarked(hooks)
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
	fmt.Println("installed SessionStart hook")
	fmt.Println("claude usage is now attributed to providers; earlier records stay unknown")
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
	if !ok || !strings.Contains(string(raw), hookMarker) {
		fmt.Println("no ak-managed hook found")
		return nil
	}

	var hooks map[string][]map[string]any
	if err := json.Unmarshal(raw, &hooks); err != nil {
		return err
	}
	removeMarked(hooks)
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

// removeMarked drops every hook entry carrying the ak marker.
func removeMarked(hooks map[string][]map[string]any) {
	for event, list := range hooks {
		var kept []map[string]any
		for _, e := range list {
			if b, _ := json.Marshal(e); strings.Contains(string(b), hookMarker) {
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
}

// hookInstalled reports whether settings.json carries the ak hook.
func hookInstalled() bool {
	data, err := os.ReadFile(settingsPath())
	return err == nil && strings.Contains(string(data), hookMarker)
}

// newRecordSessionCmd is a hidden command invoked by the hook, not by hand.
//
// Claude passes a JSON payload on stdin (including session_id) rather than an
// environment variable; the first implementation read an env var and every
// session_id came back empty.
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

			// Without an id the record is useless; RecordSession skips it.
			return usage.RecordSession(sessionIDFromStdin(), provider)
		},
	}
}

// sessionIDFromStdin reads session_id from the hook's stdin JSON.
func sessionIDFromStdin() string {
	var payload struct {
		SessionID string `json:"session_id"`
	}
	// A single decode; empty stdin just yields an empty id.
	dec := json.NewDecoder(os.Stdin)
	if err := dec.Decode(&payload); err != nil {
		return ""
	}
	return payload.SessionID
}
