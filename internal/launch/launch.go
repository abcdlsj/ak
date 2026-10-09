// Package launch turns a provider into the exact process ak execs: the engine
// binary, its arguments and its environment. It is the only implementation of
// a launch; the generated ak-<name> commands just call back into ak.
package launch

import (
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/provider"
)

// EnvKeys names the variable listing what a launch exported, so a nested
// launch can drop its parent's provider environment before setting its own.
const EnvKeys = "AK_ENV_KEYS"

// envVariant is informational (e.g. for a status line) and never read back,
// so a parent's variant cannot leak into a nested launch.
const envVariant = "AK_VARIANT"

// ParseTarget splits name[:variant].
func ParseTarget(s string) (name, variant string, err error) {
	name, variant, explicit := strings.Cut(s, ":")
	if name == "" || explicit && variant == "" {
		return "", "", fmt.Errorf("invalid target %q; use <provider> or <provider>:<variant>", s)
	}
	return name, variant, nil
}

// Options carries what a launch takes from its surroundings.
type Options struct {
	// Environ is the environment the launch starts from, as os.Environ.
	Environ []string
	// Key is the provider's resolved key.
	Key string
	// SessionID is passed to claude as --session-id when the arguments start a
	// new session, so ak knows which provider the session used without a hook.
	SessionID string
}

// Plan is the process to exec.
type Plan struct {
	Bin  string
	Argv []string
	Env  []string
	// Pool means the command talks to ak's gateway, which must be up.
	Pool bool
	// SessionID is the claude session id that was passed, or "" when none was.
	SessionID string
}

// Build computes the launch of provider name with variant; an empty variant
// means the provider's default variant, if any.
func Build(cfg *config.Config, name, variant string, args []string, o Options) (Plan, error) {
	p, ok := cfg.Providers[name]
	if !ok {
		return Plan{}, fmt.Errorf("provider %q does not exist; see `ak list`", name)
	}
	eng := provider.EngineFor(p.Kind)
	if eng == nil {
		return Plan{}, fmt.Errorf("provider %q has unsupported kind %q", name, p.Kind)
	}
	l, err := eng.Launch(name, p, provider.Literal(o.Key), provider.Context{Gateway: cfg.Settings.GatewayURL()})
	if err != nil {
		return Plan{}, err
	}
	if variant == "" {
		variant = l.DefaultVariant
	}
	var v *provider.Variant
	if variant != "" {
		if v = findVariant(l.Variants, variant); v == nil {
			return Plan{}, fmt.Errorf("provider %q has no variant %q; available: %s", name, variant, variantList(l.Variants))
		}
	}

	env := envMap(o.Environ)
	bin, err := findBin(eng, cfg.Settings, env)
	if err != nil {
		return Plan{}, err
	}

	// A nested launch inherits what the parent ak command exported; drop it
	// first, so a parent's config dir, proxy or key cannot leak into this one.
	for _, k := range strings.Fields(env[EnvKeys]) {
		delete(env, k)
	}
	// Provider keys that are not set are dropped too: inherited from a parent
	// process they would silently route to the wrong provider.
	for _, k := range l.Env.Unset {
		delete(env, k)
	}
	set := l.Env.Set
	argv := append([]string{bin}, l.Args...)
	if v != nil {
		set = append(append([]provider.KV(nil), set...), v.Env...)
		argv = append(argv, v.Args...)
		env[envVariant] = v.Name
	} else {
		delete(env, envVariant)
	}
	keys := make([]string, 0, len(set))
	for _, kv := range set {
		env[kv.Key] = kv.Value
		keys = append(keys, kv.Key)
	}
	env[EnvKeys] = strings.Join(keys, " ")

	plan := Plan{Bin: bin, Pool: p.IsPool()}
	if p.Kind == config.KindClaude && o.SessionID != "" && startsClaudeSession(args) {
		argv = append(argv, "--session-id", o.SessionID)
		plan.SessionID = o.SessionID
	}
	plan.Argv = append(argv, args...)
	plan.Env = envList(env)
	return plan, nil
}

func findVariant(vs []provider.Variant, name string) *provider.Variant {
	for i := range vs {
		if vs[i].Name == name {
			return &vs[i]
		}
	}
	return nil
}

// variantList names every variant the provider accepts, for an error message.
func variantList(vs []provider.Variant) string {
	var names []string
	for _, v := range vs {
		names = append(names, v.Name)
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// findBin returns the engine binary: the override variable, then the binary
// pinned in settings, then PATH.
func findBin(eng provider.Engine, s config.Settings, env map[string]string) (string, error) {
	cand := env[eng.BinEnvVar()]
	if cand == "" {
		cand = config.ExpandHome(eng.ConfiguredBin(s))
	}
	if cand != "" && executable(cand) {
		return cand, nil
	}
	if p, err := exec.LookPath(eng.BinName()); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%s not found; install it, add it to PATH, or set %s", eng.BinName(), eng.BinEnvVar())
}

func executable(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir() && fi.Mode().Perm()&0o111 != 0
}

func envMap(environ []string) map[string]string {
	m := make(map[string]string, len(environ))
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

func envList(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// claudeSubcommands are claude's own commands. They start no session, so a
// --session-id in front of them would at best be ignored.
var claudeSubcommands = map[string]bool{
	"agents": true, "attach": true, "auth": true, "auto-mode": true, "config": true,
	"doctor": true, "gateway": true, "import": true, "install": true, "logs": true,
	"mcp": true, "migrate-installer": true, "plugin": true, "plugins": true,
	"purge": true, "respawn": true, "rm": true, "setup-token": true, "stop": true,
	"kill": true, "ultrareview": true, "update": true, "upgrade": true,
}

// sessionFlags pick or name a session themselves.
var sessionFlags = []string{"-c", "--continue", "-r", "--resume", "--session-id",
	"--fork-session", "--from-pr", "--teleport", "-h", "--help", "-v", "--version"}

// startsClaudeSession reports whether claude, given args, starts a new session
// whose id ak may choose.
func startsClaudeSession(args []string) bool {
	if len(args) > 0 && claudeSubcommands[args[0]] {
		return false
	}
	for _, a := range args {
		if a == "--" {
			break
		}
		for _, f := range sessionFlags {
			if a == f || strings.HasPrefix(a, f+"=") {
				return false
			}
		}
	}
	return true
}

// NewSessionID returns a random version 4 UUID, the form claude requires for
// --session-id.
func NewSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
