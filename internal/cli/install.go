package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/spf13/cobra"
)

// The block ak install adds to a zsh or bash rc file, between these markers,
// so it is found again to update or remove.
const (
	rcBegin = "# >>> ak shell integration >>>"
	rcEnd   = "# <<< ak shell integration <<<"
)

func newInstallCmd() *cobra.Command {
	var shell string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Set up the shell so claude/codex/pi follow bindings (ak init, once)",
		Long: `install sets up the shell functions of ak init for your shell, detected
from $SHELL unless --shell names one: for fish a file in
~/.config/fish/conf.d, for zsh and bash a marked block in ~/.zshrc or
~/.bashrc. Running it again changes nothing; ak uninstall takes it out.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			sh, err := pickShell(shell)
			if err != nil {
				return err
			}
			path, changed, err := installShell(sh)
			if err != nil {
				return err
			}
			if !changed {
				fmt.Printf("%s already set up in %s\n", sh, displayPlace(path))
			} else {
				fmt.Printf("set up %s in %s\n", sh, displayPlace(path))
			}
			fmt.Println("Open a new shell (or `exec " + sh + "`), then `ak bind <provider>` in a repository")
			fmt.Println("or `ak default claude <provider>`; `ak which` shows the result.")
			return nil
		},
	}
	cmd.Flags().StringVar(&shell, "shell", "", "zsh, bash or fish; defaults to $SHELL")
	return cmd
}

func newUninstallCmd() *cobra.Command {
	var shell string
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove what ak install set up",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			shells := []string{"zsh", "bash", "fish"}
			if shell != "" {
				sh, err := pickShell(shell)
				if err != nil {
					return err
				}
				shells = []string{sh}
			}
			removed := false
			for _, sh := range shells {
				path, ok, err := uninstallShell(sh)
				if err != nil {
					return err
				}
				if ok {
					removed = true
					fmt.Printf("removed the %s setup from %s\n", sh, displayPlace(path))
				}
			}
			if !removed {
				fmt.Println("nothing to remove")
			} else {
				fmt.Println("Open a new shell for claude/codex/pi to be the engines' own again.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&shell, "shell", "", "only this shell: zsh, bash or fish")
	return cmd
}

// pickShell is the named shell, else $SHELL's.
func pickShell(name string) (string, error) {
	if name == "" {
		name = filepath.Base(os.Getenv("SHELL"))
		if name == "." || name == "" {
			return "", fmt.Errorf("cannot tell your shell from $SHELL; name it with --shell zsh|bash|fish")
		}
	}
	switch name {
	case "zsh", "bash", "fish":
		return name, nil
	}
	return "", fmt.Errorf("unsupported shell %q; ak supports zsh, bash and fish (or add `eval \"$(ak init bash)\"` yourself)", name)
}

// shellTarget is the file ak install writes for shell.
func shellTarget(shell string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch shell {
	case "zsh":
		dir := os.Getenv("ZDOTDIR")
		if dir == "" {
			dir = home
		}
		return filepath.Join(dir, ".zshrc"), nil
	case "bash":
		return filepath.Join(home, ".bashrc"), nil
	case "fish":
		dir := os.Getenv("XDG_CONFIG_HOME")
		if dir == "" {
			dir = filepath.Join(home, ".config")
		}
		return filepath.Join(dir, "fish", "conf.d", "ak.fish"), nil
	}
	return "", fmt.Errorf("unsupported shell %q", shell)
}

// rcBlock is what the rc file holds: it asks the installed ak for the current
// functions on every shell start, so an upgrade of ak needs no reinstall, and
// it does nothing where ak is not on PATH.
func rcBlock(shell string) string {
	if shell == "fish" {
		return "# Added by `ak install`; `ak uninstall` removes this file.\n" +
			"if status is-interactive; and command -q ak\n" +
			"    ak init fish | source\n" +
			"end\n"
	}
	return rcBegin + "\n" +
		fmt.Sprintf("if command -v ak >/dev/null 2>&1; then eval \"$(ak init %s)\"; fi\n", shell) +
		rcEnd + "\n"
}

// installShell writes the setup for shell and reports whether anything changed.
func installShell(shell string) (string, bool, error) {
	path, err := shellTarget(shell)
	if err != nil {
		return "", false, err
	}
	block := rcBlock(shell)
	if shell == "fish" {
		if old, err := os.ReadFile(path); err == nil && string(old) == block {
			return path, false, nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", false, err
		}
		return path, true, writeKeepingLink(path, []byte(block), 0o644)
	}
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", false, err
	}
	next, found := replaceBlock(string(old), block)
	if !found {
		next = string(old)
		if next != "" && !strings.HasSuffix(next, "\n") {
			next += "\n"
		}
		if next != "" {
			next += "\n"
		}
		next += block
	}
	if next == string(old) {
		return path, false, nil
	}
	return path, true, writeKeepingLink(path, []byte(next), 0o644)
}

// uninstallShell removes the setup for shell and reports whether there was one.
func uninstallShell(shell string) (string, bool, error) {
	path, err := shellTarget(shell)
	if err != nil {
		return "", false, err
	}
	old, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return path, false, nil
	}
	if err != nil {
		return "", false, err
	}
	if shell == "fish" {
		// Only a file ak wrote is removed.
		if !strings.Contains(string(old), "ak install") {
			return path, false, nil
		}
		return path, true, os.Remove(path)
	}
	next, found := replaceBlock(string(old), "")
	if !found {
		return path, false, nil
	}
	return path, true, writeKeepingLink(path, []byte(next), 0o644)
}

// replaceBlock swaps the marked block in s, with the blank line before it
// when block is "", and reports whether there was one.
func replaceBlock(s, block string) (string, bool) {
	i := strings.Index(s, rcBegin)
	if i < 0 {
		return s, false
	}
	j := strings.Index(s[i:], rcEnd)
	if j < 0 {
		return s, false
	}
	end := i + j + len(rcEnd)
	if end < len(s) && s[end] == '\n' {
		end++
	}
	start := i
	if block == "" && strings.HasSuffix(s[:start], "\n\n") {
		start--
	}
	return s[:start] + block + s[end:], true
}

// writeKeepingLink writes path atomically, through a symlink to its target, so
// a dotfiles manager's link stays a link, and keeps the file's mode.
func writeKeepingLink(path string, data []byte, mode os.FileMode) error {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	return config.AtomicWrite(path, data, mode)
}
