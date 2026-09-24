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
		return fmt.Errorf("read %s: %w", path, err)
	}

	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	// Already installed: do not add a second copy.
	if raw, ok := settings["hooks"]; ok && contains(string(raw), hookMarker) {
		fmt.Println("hook already installed")
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
			return fmt.Errorf("parse existing hooks: %w", err)
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
	if !ok || !contains(string(raw), hookMarker) {
		fmt.Println("no ak-managed hook found")
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

			sessionID := sessionIDFromStdin()
			if sessionID == "" {
				// Without an id the record is useless, so skip it rather
				// than writing an empty entry.
			}
			return usage.RecordSession(sessionID, provider)
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
