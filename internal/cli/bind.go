package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/abcdlsj/ak/internal/bind"
	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/launch"
	"github.com/abcdlsj/ak/internal/out"
	"github.com/spf13/cobra"
)

// overrideEnv names the variable that picks the provider for one invocation of
// an engine command: AK=deepseek claude, or AK=native claude.
const overrideEnv = "AK"

// resolution is what an engine command typed in a directory runs on, and why.
type resolution struct {
	// Target is the provider[:variant], or "" for the engine's own command.
	Target string
	// Source says which rule chose it.
	Source string
}

// resolveEngine decides what engine k's own command runs on in dir: the AK
// override, else the binding there, else the engine default, else the
// engine's own command. A target that names no provider of k is an error
// rather than a silent fall back to the engine's own command, so a request
// never quietly goes somewhere the user did not mean.
func resolveEngine(cfg *config.Config, store *bind.Store, k config.Kind, dir string) (resolution, error) {
	r := resolution{}
	switch {
	case os.Getenv(overrideEnv) != "":
		r = resolution{Target: os.Getenv(overrideEnv), Source: "$" + overrideEnv}
	default:
		b, ok, err := store.Lookup(dir, string(k))
		if err != nil {
			return resolution{}, err
		}
		if ok {
			r = resolution{Target: b.Target, Source: "bound at " + displayPlace(b.Place)}
		} else if t := cfg.Settings.EngineDefault(k); t != "" {
			r = resolution{Target: t, Source: "default_" + string(k)}
		} else {
			return resolution{Source: "no binding or default"}, nil
		}
	}
	if r.Target == bind.Native {
		r.Target = ""
		return r, nil
	}
	if err := config.ValidateTarget(cfg, k, r.Target); err != nil {
		return r, fmt.Errorf("%s (%s): %w", k, r.Source, err)
	}
	return r, nil
}

// displayPlace shows a repository by its working tree rather than its git dir.
func displayPlace(place string) string {
	if strings.HasSuffix(place, "/.git") {
		place = strings.TrimSuffix(place, "/.git")
	}
	if home, err := os.UserHomeDir(); err == nil && (place == home || strings.HasPrefix(place, home+"/")) {
		place = "~" + strings.TrimPrefix(place, home)
	}
	return place
}

// parseKind accepts an engine name.
func parseKind(s string) (config.Kind, bool) {
	for _, k := range config.Kinds {
		if string(k) == s {
			return k, true
		}
	}
	return "", false
}

func kindList() string {
	names := make([]string, len(config.Kinds))
	for i, k := range config.Kinds {
		names[i] = string(k)
	}
	return strings.Join(names, ", ")
}

func newBindCmd() *cobra.Command {
	var engine string
	cmd := &cobra.Command{
		Use:   "bind <provider[:variant]|native>",
		Short: "Make claude/codex/pi typed in this repository run on a provider",
		Long: `bind makes the engine's own command, typed in this repository (every
worktree of it) or, outside one, this directory and those below it, run on a
provider. It needs the shell functions from ak init. The engine is the
provider's kind; native keeps the engine's own command here, for every engine
or the one --engine names. Nothing is written into the directory.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeTarget,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			store, err := bind.Default()
			if err != nil {
				return err
			}
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			target := args[0]
			var kinds []config.Kind
			if engine != "" {
				k, ok := parseKind(engine)
				if !ok {
					return fmt.Errorf("unknown engine %q; one of %s", engine, kindList())
				}
				kinds = []config.Kind{k}
			}
			if target != bind.Native {
				name, _, _ := strings.Cut(target, ":")
				p, ok := cfg.Providers[name]
				if !ok {
					return fmt.Errorf("provider %q does not exist; see `ak list`", name)
				}
				if len(kinds) == 0 {
					kinds = []config.Kind{p.Kind}
				}
				if err := config.ValidateTarget(cfg, kinds[0], target); err != nil {
					return err
				}
			} else if len(kinds) == 0 {
				kinds = config.Kinds
			}
			for _, k := range kinds {
				if _, err = store.Set(wd, string(k), target); err != nil {
					return err
				}
				out.Done(os.Stdout, "%s here runs %s", k, describeTarget(target))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&engine, "engine", "", "the engine to bind ("+kindList()+"); defaults to the provider's kind")
	return cmd
}

// describeTarget is how a target reads after "runs": "on kimi", "natively".
func describeTarget(t string) string {
	if t == "" || t == bind.Native {
		return "natively"
	}
	return "on " + t
}

func newUnbindCmd() *cobra.Command {
	return &cobra.Command{
		Use:       "unbind [engine]",
		Short:     "Remove this repository's binding, for one engine or all",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"claude", "codex", "pi"},
		RunE: func(cmd *cobra.Command, args []string) error {
			engine := ""
			if len(args) == 1 {
				k, ok := parseKind(args[0])
				if !ok {
					return fmt.Errorf("unknown engine %q; one of %s", args[0], kindList())
				}
				engine = string(k)
			}
			store, err := bind.Default()
			if err != nil {
				return err
			}
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			place, removed, err := store.Unset(wd, engine)
			if err != nil {
				return err
			}
			if !removed {
				fmt.Printf("Nothing bound at %s.\n", displayPlace(place))
				// A binding further up still applies; say where it is.
				for _, k := range config.Kinds {
					if engine != "" && string(k) != engine {
						continue
					}
					if b, ok, _ := store.Lookup(wd, string(k)); ok {
						out.Hint(os.Stdout, "%s here follows %s, bound at %s", k, b.Target, displayPlace(b.Place))
					}
				}
				return nil
			}
			if engine == "" {
				out.Done(os.Stdout, "unbound every engine here")
				return nil
			}
			out.Done(os.Stdout, "unbound %s here", engine)
			return nil
		},
	}
}

func newBindingsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "bindings",
		Short: "List every binding and the engine defaults",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			store, err := bind.Default()
			if err != nil {
				return err
			}
			list, err := store.List()
			if err != nil {
				return err
			}
			if len(list) == 0 && !hasEngineDefaults(cfg) {
				out.Empty(os.Stdout, "Nothing bound.", "Bind one with `ak bind <provider>`.")
				return nil
			}
			out.Table(os.Stdout, []string{"engine", "runs on", "place"}, bindingRows(cfg, list))
			return nil
		},
	}
}

// bindingRows is the engine defaults and the bindings: what each engine runs
// on, and where that was set.
func bindingRows(cfg *config.Config, list []bind.Binding) [][]string {
	var rows [][]string
	for _, k := range config.Kinds {
		if t := cfg.Settings.EngineDefault(k); t != "" {
			rows = append(rows, []string{string(k), t, "default"})
		}
	}
	for _, b := range list {
		rows = append(rows, []string{b.Engine, b.Target, displayPlace(b.Place)})
	}
	return rows
}

// hasEngineDefaults reports whether any engine has a default target.
func hasEngineDefaults(cfg *config.Config) bool {
	for _, k := range config.Kinds {
		if cfg.Settings.EngineDefault(k) != "" {
			return true
		}
	}
	return false
}

func newWhichCmd() *cobra.Command {
	return &cobra.Command{
		Use:       "which [engine...]",
		Short:     "Show what claude/codex/pi typed here runs on, and why",
		ValidArgs: []string{"claude", "codex", "pi"},
		RunE: func(cmd *cobra.Command, args []string) error {
			kinds := config.Kinds
			if len(args) > 0 {
				kinds = nil
				for _, a := range args {
					k, ok := parseKind(a)
					if !ok {
						return fmt.Errorf("unknown engine %q; one of %s", a, kindList())
					}
					kinds = append(kinds, k)
				}
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			store, err := bind.Default()
			if err != nil {
				return err
			}
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			rows, broken := whichRows(cfg, store, kinds, wd)
			out.Table(os.Stdout, []string{"engine", "runs on", "why"}, rows)
			if broken {
				return errors.New("a binding or default points at a provider that cannot run it")
			}
			return nil
		},
	}
}

// whichRows is what each engine runs on here, and which rule chose it; one
// that cannot run is marked broken and says why.
func whichRows(cfg *config.Config, store *bind.Store, kinds []config.Kind, dir string) ([][]string, bool) {
	var rows [][]string
	broken := false
	for _, k := range kinds {
		r, err := resolveEngine(cfg, store, k, dir)
		switch {
		case err != nil:
			broken = true
			rows = append(rows, []string{out.Mark(out.Broken) + " " + string(k), out.Or(r.Target), out.Err(whyLine(r, err))})
		case r.Target == "":
			rows = append(rows, []string{string(k), out.Dim(bind.Native), r.Source})
		default:
			rows = append(rows, []string{string(k), r.Target, r.Source})
		}
	}
	return rows, broken
}

// whyLine is why a resolution failed, without the engine and target the row
// already names.
func whyLine(r resolution, err error) string {
	cause := errors.Unwrap(err)
	if cause == nil {
		return err.Error()
	}
	if r.Source == "" {
		return cause.Error()
	}
	return r.Source + ": " + cause.Error()
}

// newResolveCmd is what the shell functions ask before each engine command:
// it prints the target to launch, or nothing for the engine's own command.
// It reads only the config and the bindings, so it stays fast.
func newResolveCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "__resolve <engine>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, ok := parseKind(args[0])
			if !ok {
				return fmt.Errorf("unknown engine %q", args[0])
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			store, err := bind.Default()
			if err != nil {
				return err
			}
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			r, err := resolveEngine(cfg, store, k, wd)
			if err != nil {
				return fmt.Errorf("%w; fix it with `ak bind`, `ak unbind` or `ak default %s`, or run it natively with AK=native %s", err, k, k)
			}
			if r.Target != "" {
				fmt.Println(r.Target)
			}
			return nil
		},
	}
}

// newEngineCmd is what the shell functions run for an engine command: it
// resolves what the command runs on here and replaces ak with it, the
// provider or the engine's own command, either with the engine arguments in
// settings.
func newEngineCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "__engine <engine> [args...]",
		Hidden:             true,
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			k, ok := parseKind(args[0])
			if !ok {
				return fmt.Errorf("unknown engine %q", args[0])
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			store, err := bind.Default()
			if err != nil {
				return err
			}
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			r, err := resolveEngine(cfg, store, k, wd)
			if err != nil {
				return fmt.Errorf("%w; fix it with `ak bind`, `ak unbind` or `ak default %s`, or run it natively with AK=native %s", err, k, k)
			}
			if r.Target != "" {
				return runTarget(r.Target, args[1:])
			}
			plan, err := launch.Native(cfg, k, args[1:], withoutOverride(os.Environ()))
			if err != nil {
				return err
			}
			return syscall.Exec(plan.Bin, plan.Argv, plan.Env)
		},
	}
}

func newInitCmd() *cobra.Command {
	var engines []string
	cmd := &cobra.Command{
		Use:   "init <zsh|bash|fish>",
		Short: "Print shell functions that route claude/codex/pi through ak",
		Long: `init prints shell functions named after the engines. Each asks ak what
the command runs on in the current directory (AK=..., a binding, the engine
default) and launches that provider, or the engine's own command when nothing
applies, so repositories nothing is bound in keep the engine as it is.

  eval "$(ak init zsh)"        # in ~/.zshrc
  eval "$(ak init bash)"       # in ~/.bashrc
  ak init fish | source        # in ~/.config/fish/config.fish

Functions apply only to interactive shells: scripts, editors and other tools
still run the engine's own command.`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"zsh", "bash", "fish"},
		RunE: func(cmd *cobra.Command, args []string) error {
			var kinds []config.Kind
			for _, e := range engines {
				k, ok := parseKind(e)
				if !ok {
					return fmt.Errorf("unknown engine %q; one of %s", e, kindList())
				}
				kinds = append(kinds, k)
			}
			out, err := shellInit(args[0], kinds)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&engines, "engines", []string{"claude", "codex", "pi"}, "the engines to wrap")
	return cmd
}

// shellInit renders the functions for shell.
func shellInit(shell string, kinds []config.Kind) (string, error) {
	var b strings.Builder
	switch shell {
	case "zsh", "bash":
		b.WriteString("# ak: route engine commands by directory; see `ak init --help`.\n")
		for _, k := range kinds {
			// The function keyword form, so an alias of the same name does
			// not turn the definition into a syntax error.
			fmt.Fprintf(&b, "unalias %[1]s 2>/dev/null\nfunction %[1]s { command ak __engine %[1]s \"$@\"; }\n", k)
		}
	case "fish":
		b.WriteString("# ak: route engine commands by directory; see `ak init --help`.\n")
		for _, k := range kinds {
			fmt.Fprintf(&b, "function %[1]s --wraps %[1]s\n    command ak __engine %[1]s $argv\nend\n", k)
		}
	default:
		return "", fmt.Errorf("unsupported shell %q; one of zsh, bash, fish", shell)
	}
	return b.String(), nil
}
