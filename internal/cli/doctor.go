package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/bind"
	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/out"
	"github.com/abcdlsj/ak/internal/provider"
	"github.com/abcdlsj/ak/internal/quota"
	"github.com/abcdlsj/ak/internal/secrets"
	"github.com/abcdlsj/ak/internal/shim"
	"github.com/spf13/cobra"
)

// providerEnvKeys are the provider-specific keys that stop working when they
// remain in settings.json. Claude Code runs Object.assign(process.env,
// settingsEnv) at startup, so settings.json wins over the environment. Any of
// these keys left behind silently override the matching ak-* command.
var providerEnvKeys = config.ClaudeRoutingKeys

func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check for conditions that would make ak commands misroute",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return runDoctor(cfg)
		},
	}
	return cmd
}

func runDoctor(cfg *config.Config) error {
	d := &doctor{}

	// 1. Provider keys left in settings.json. This is the worst case: no error
	// is raised, requests just silently go to the wrong provider.
	// An isolated provider reads its own settings.json, which can hold the
	// same leftovers.
	clean := true
	for _, path := range claudeSettingsPaths(cfg) {
		residual := residualProviderKeys(path)
		if len(residual) == 0 {
			continue
		}
		clean = false
		d.bad("%s still has provider-specific keys in env", path)
		d.details(residual)
		d.note("These override the environment variables ak-* commands inject, so")
		d.note("those commands silently reach the wrong provider.")
		d.hint("Keep them with `ak import --from claude-settings`, then remove them")
		d.hint("Keep global preferences, e.g. NODE_EXTRA_CA_CERTS and CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC")
	}
	if clean {
		d.fine("no provider keys left in settings.json")
	}

	// 2. bin_dir must be on PATH.
	binDir := config.ExpandHome(cfg.Settings.BinDir)
	if onPath(binDir) {
		d.fine("%s is on PATH", binDir)
	} else {
		d.bad("%s is not on PATH, ak-* commands cannot be invoked directly", binDir)
		d.hint("Add it with `export PATH=\"%s:$PATH\"` in your shell profile", binDir)
	}

	// 3. Engine binaries must resolve.
	for _, k := range config.Kinds {
		if hasKind(cfg, k) {
			d.engineBin(cfg, k)
		}
	}

	// 4. Generated commands must match the config on disk.
	switch drifted, err := driftReport(cfg); {
	case err != nil:
		d.bad("cannot check the commands against the config: %v", err)
	case len(drifted) > 0:
		d.bad("these commands do not match the config")
		d.details(baseNames(drifted))
		d.hint("Run `ak sync` to regenerate them")
	case len(cfg.Providers) > 0:
		d.fine("all commands match the config")
	}

	// 5. ak passes each new claude session its own id and records it, so the
	// SessionStart hook only adds sessions ak did not start (a fork).
	if hasKind(cfg, config.KindClaude) {
		if missing := hookMissing(cfg); len(missing) == 0 {
			d.fine("usage attribution hook installed")
		} else {
			d.check(out.Unknown, "usage attribution hook not installed; sessions ak starts are attributed anyway")
			d.details(missing)
			d.hint("Run `ak hook install` so forked sessions are attributed too")
		}
	}

	// 6. Config file permissions; auth.json holds the plaintext keys.
	for _, f := range []func() (string, error){config.Path, config.AuthPath} {
		path, err := f()
		if err != nil {
			continue
		}
		if fi, err := os.Stat(path); err == nil {
			if fi.Mode().Perm() != 0o600 {
				d.bad("%s has mode %o, expected 600", path, fi.Mode().Perm())
				d.hint("Run `chmod 600 %s`", path)
			} else {
				d.fine("%s mode is 600", filepath.Base(path))
			}
		}
	}

	// 6b. An auth_key with no entry in auth.json launches with no key.
	for _, name := range cfg.Names() {
		if p := cfg.Providers[name]; p.AuthKey != "" && p.APIKey == "" {
			d.bad("%s names auth_key %q, which auth.json does not hold", name, p.AuthKey)
			d.hint("Set it with `ak edit %s --key -`", name)
		}
	}

	// 6c. A default left pointing at a provider that is gone or of another
	// kind; the command that would use it stops with the same message.
	for _, err := range config.ValidateDefaults(cfg) {
		d.bad("%v", err)
		d.hint("Fix it with `ak default`")
	}

	// 7. A codex provider_id that the base config also uses cannot be told
	// apart in the session logs, so its usage is reported under the raw id.
	if names := codexIDCollisions(cfg); len(names) > 0 {
		d.notice("these codex providers reuse a model_provider id declared in ~/.codex/config.toml")
		d.details(names)
		d.note("Session logs record only the id, so ak cannot tell the two apart and")
		d.note("`ak usage` reports the id itself instead of the provider.")
		d.hint("Give each provider its own id in providers.toml, then run `ak sync`")
	}

	// 8. Keys ak does not know are ignored, usually a misspelt field.
	if keys, err := config.UnknownKeys(); err == nil && len(keys) > 0 {
		d.notice("providers.toml has keys ak ignores (misspelt?)")
		d.details(keys)
		d.note("The next save drops them.")
	}

	// 9. A referenced key must resolve now, or the command fails at launch.
	if failed := keyRefProblems(cfg); len(failed) > 0 {
		d.bad("these providers' api_key_ref does not resolve")
		d.details(failed)
		d.hint("Fix each reference, or set the key with `ak edit <provider> --key -`")
	} else if hasKeyRef(cfg) {
		d.fine("all referenced keys resolve")
	}

	// 10. Pools route through the local gateway, which must be running.
	if pools := poolNames(cfg); len(pools) > 0 {
		addr := cfg.Settings.GatewayAddr
		if addr == "" {
			addr = config.DefaultGatewayAddr
		}
		if gatewayUp(addr) {
			d.fine("pool gateway reachable at %s", addr)
		} else {
			d.notice("%s configured but nothing is listening on %s", count(len(pools), "pool"), addr)
			d.hint("Run `ak serve`, or any pool command, which starts it")
		}
	}

	// 11. A quota plugin that does not load is skipped by `ak quota`.
	if errs := quota.PluginErrors(); len(errs) > 0 {
		d.notice("these quota plugins do not load and are skipped")
		d.details(errorLines(errs))
	}

	// 12. A smart pool ranks members by their quota; one without a source
	// is never ranked ahead of those with one.
	for _, name := range poolNames(cfg) {
		if cfg.Providers[name].StrategyOrDefault() != config.StrategySmart {
			continue
		}
		var blind []string
		for _, m := range cfg.Leaves(name) {
			if !quota.HasSource(cfg.Providers[m]) {
				blind = append(blind, m)
			}
		}
		if len(blind) > 0 {
			d.notice("smart pool %s cannot read the quota of %s", name, strings.Join(blind, ", "))
			d.note("They rank after members with allowance that resets.")
		}
	}

	// 13. A binding to a provider since removed, or of the wrong kind, makes
	// the engine's command fail where it applies.
	if store, err := bind.Default(); err == nil {
		list, err := store.List()
		if err != nil {
			d.bad("bindings: %v", err)
		}
		for _, b := range list {
			if b.Target == bind.Native {
				continue
			}
			if err := config.ValidateTarget(cfg, config.Kind(b.Engine), b.Target); err != nil {
				d.bad("%s bound at %s: %v", b.Engine, displayPlace(b.Place), err)
				d.hint("Run `ak unbind %s` there", b.Engine)
			}
		}
	}

	if len(cfg.Providers) == 0 {
		out.Empty(os.Stdout, "No providers configured yet.", "Add one with `ak add`, or `ak import --from claude-settings`.")
		return nil
	}
	d.finish()
	return d.err()
}

// doctor is one run's result: the checks it printed, how many found something
// wrong, and what to do about it, printed as hints under them.
type doctor struct {
	problems int
	warnings int
	hints    []string
}

// check writes a check line: the state's mark, then what was checked.
func (d *doctor) check(l out.Level, format string, a ...any) {
	fmt.Printf("%s %s\n", out.Mark(l), fmt.Sprintf(format, a...))
}

// fine writes a check that passed.
func (d *doctor) fine(format string, a ...any) {
	d.check(out.Fine, format, a...)
}

// notice writes a check worth a look, and counts the warning.
func (d *doctor) notice(format string, a ...any) {
	d.warnings++
	d.check(out.Notice, format, a...)
}

// bad writes a check that failed, and counts the problem.
func (d *doctor) bad(format string, a ...any) {
	d.problems++
	d.check(out.Broken, format, a...)
}

// details writes the dim lines under a check: the names it found.
func (d *doctor) details(lines []string) {
	for _, s := range lines {
		fmt.Println("    " + out.Dim(s))
	}
}

// note writes a dim line under a check, explaining what it means.
func (d *doctor) note(format string, a ...any) {
	fmt.Println("  " + out.Dim(fmt.Sprintf(format, a...)))
}

// hint records what to do about a failed check, printed once under them all.
func (d *doctor) hint(format string, a ...any) {
	h := fmt.Sprintf(format, a...)
	for _, have := range d.hints {
		if have == h {
			return
		}
	}
	d.hints = append(d.hints, h)
}

// finish prints what to do about what failed, then the result line.
func (d *doctor) finish() {
	if len(d.hints) > 0 {
		fmt.Println()
		for _, h := range d.hints {
			out.Hint(os.Stdout, "%s", h)
		}
	}
	fmt.Println()
	fmt.Println(d.summary())
}

// summary is the result line: how many problems, and the warnings when there
// are any.
func (d *doctor) summary() string {
	line := "No problems"
	if d.problems > 0 {
		line = count(d.problems, "problem")
	}
	if d.warnings > 0 {
		line += " · " + count(d.warnings, "warning")
	}
	return out.Bold(line)
}

// err is the command's failure when a check found a problem.
func (d *doctor) err() error {
	switch {
	case d.problems == 1:
		return errors.New("1 problem needs attention")
	case d.problems > 1:
		return fmt.Errorf("%d problems need attention", d.problems)
	}
	return nil
}

// errorLines is errors as the text a detail line shows.
func errorLines(errs []error) []string {
	lines := make([]string, len(errs))
	for i, err := range errs {
		lines[i] = err.Error()
	}
	return lines
}

// baseNames is paths by their file name, which is what the generated commands
// differ in.
func baseNames(paths []string) []string {
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = filepath.Base(p)
	}
	return names
}

// count names n of what: "1 pool", "2 pools".
func count(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

// engineBin checks that engine k's executable resolves, naming what was found.
func (d *doctor) engineBin(cfg *config.Config, k config.Kind) {
	configured, name := cfg.Settings.ClaudeBin, "claude"
	switch k {
	case config.KindCodex:
		configured, name = cfg.Settings.CodexBin, "codex"
	case config.KindPi:
		configured, name = cfg.Settings.PiBin, "pi"
	}
	if p := lookPathOr(configured, name); p != "" {
		d.fine("%s: %s", name, p)
		return
	}
	d.bad("%s executable not found", name)
	d.hint("Install %s, or set %s_bin in providers.toml", name, name)
}

// keyRefProblems resolves every api_key_ref and names the ones that fail, so
// a broken env:/cmd:/keychain: reference is caught before a launch.
func keyRefProblems(cfg *config.Config) []string {
	resolver := secrets.Default()
	var out []string
	for _, name := range cfg.Names() {
		p := cfg.Providers[name]
		if p.APIKeyRef == "" {
			continue
		}
		if _, err := resolver.Resolve(p); err != nil {
			out = append(out, fmt.Sprintf("%s (%v)", name, err))
		}
	}
	return out
}

func hasKeyRef(cfg *config.Config) bool {
	for _, p := range cfg.Providers {
		if p.APIKeyRef != "" {
			return true
		}
	}
	return false
}

// codexIDCollisions returns "name (provider_id \"id\")" for every ak codex
// provider whose id the base codex config also declares.
func codexIDCollisions(cfg *config.Config) []string {
	base := provider.BaseCodexProviderIDs()
	if len(base) == 0 {
		return nil
	}
	var out []string
	for _, name := range cfg.Names() {
		p := cfg.Providers[name]
		if p.Kind != config.KindCodex {
			continue
		}
		if id := provider.CodexProviderID(name, p); base[id] {
			out = append(out, fmt.Sprintf("%s (provider_id %q)", name, id))
		}
	}
	return out
}

// poolNames lists the configured pool providers.
func poolNames(cfg *config.Config) []string {
	var out []string
	for _, name := range cfg.Names() {
		if cfg.Providers[name].IsPool() {
			out = append(out, name)
		}
	}
	return out
}

// gatewayUp reports whether something is listening on the gateway address.
func gatewayUp(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 400*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func hasKind(cfg *config.Config, k config.Kind) bool {
	for _, p := range cfg.Providers {
		if p.Kind == k {
			return true
		}
	}
	return false
}

func lookPathOr(configured, name string) string {
	if configured != "" {
		p := config.ExpandHome(configured)
		if isExecutable(p) {
			return p
		}
	}
	if p, err := lookPath(name); err == nil {
		return p
	}
	return ""
}

// lookPath is a thin wrapper around exec.LookPath.
func lookPath(name string) (string, error) { return exec.LookPath(name) }

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Mode().Perm()&0o111 != 0
}

// residualProviderKeys returns provider keys still present in settings.json's env.
func residualProviderKeys(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var settings struct {
		Env map[string]json.RawMessage `json:"env"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil
	}
	var out []string
	for _, k := range providerEnvKeys {
		if _, ok := settings.Env[k]; ok {
			out = append(out, k)
		}
	}
	return out
}

// claudeSettingsPaths lists the shared settings.json and that of every claude
// provider with its own config_dir.
func claudeSettingsPaths(cfg *config.Config) []string {
	paths := []string{claudeSettingsPath()}
	seen := map[string]bool{paths[0]: true}
	for _, name := range cfg.Names() {
		p := cfg.Providers[name]
		if p.Kind != config.KindClaude || p.ConfigDir == "" {
			continue
		}
		path := filepath.Join(config.ExpandHome(p.ConfigDir), "settings.json")
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	return paths
}

func claudeSettingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

// onPath reports whether dir appears in PATH.
func onPath(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == dir {
			return true
		}
	}
	return false
}

// driftReport returns the paths of generated artifacts that no longer match
// the config. DryRun is set so doctor only inspects and never writes.
func driftReport(cfg *config.Config) ([]string, error) {
	rep, err := core.Sync(cfg, true)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rep.Results {
		switch r.Action {
		case shim.ActionUpdated, shim.ActionCreated, shim.ActionRemoved:
			out = append(out, r.Path)
		case shim.ActionSkipped:
			if r.Foreign {
				continue
			}
			reason := r.Reason
			if reason == "" {
				reason = "skipped"
			}
			out = append(out, r.Path+" ("+reason+")")
		}
	}
	return out, nil
}
