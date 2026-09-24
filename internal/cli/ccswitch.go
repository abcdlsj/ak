package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/abcdlsj/ak/internal/ccswitch"
	"github.com/abcdlsj/ak/internal/config"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/term"
)

// ccCandidate is one cc-switch provider after parsing, before the user decides
// whether to import it.
type ccCandidate struct {
	sourceID   string
	appType    string
	sourceName string
	name       string // proposed ak provider name
	provider   config.Provider
	current    bool
	ready      bool   // complete enough to pre-select: has a base URL and a key
	warning    string // non-empty: importable but incomplete, so not pre-selected
	skip       string // non-empty: not importable at all
}

// importCCSwitch scans the cc-switch database and imports only the providers
// the user selects. Nothing is written until the selection is confirmed, so a
// scan or a cancellation leaves the config untouched.
//
// all skips the prompt and imports every complete provider, for scripted use.
func importCCSwitch(cfg *config.Config, all bool) error {
	raws, err := ccswitch.Scan()
	if err != nil {
		return err
	}
	if len(raws) == 0 {
		return fmt.Errorf("the cc-switch database holds no providers")
	}

	cands, unsupported := ccSwitchCandidates(cfg, raws)
	printCCSwitchScan(cands, unsupported)

	importable := 0
	for _, c := range cands {
		if c.skip == "" {
			importable++
		}
	}
	if importable == 0 {
		return fmt.Errorf("no claude or codex provider in the cc-switch database could be imported")
	}

	var chosen []int
	if all {
		for i, c := range cands {
			if c.skip == "" {
				chosen = append(chosen, i)
			}
		}
	} else {
		if !term.IsTerminal(os.Stdin.Fd()) {
			return fmt.Errorf("stdin is not a terminal; run this in an interactive shell or pass --all to import every provider")
		}
		chosen, err = selectCCSwitch(cands)
		if err != nil {
			return err
		}
	}
	if len(chosen) == 0 {
		fmt.Println("Nothing selected, nothing imported.")
		return nil
	}

	if cfg.Providers == nil {
		cfg.Providers = map[string]config.Provider{}
	}
	names := make([]string, 0, len(chosen))
	for _, i := range chosen {
		c := cands[i]
		cfg.Providers[c.name] = c.provider
		names = append(names, c.name)
	}
	// Only adopt a default if none is set, so an existing choice is never
	// silently overwritten.
	if cfg.Settings.Default == "" {
		for _, i := range chosen {
			if cands[i].current {
				cfg.Settings.Default = cands[i].name
				break
			}
		}
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

	fmt.Printf("\nimported %d provider(s): %s\n", len(names), strings.Join(names, ", "))
	var missing []string
	for _, i := range chosen {
		if cands[i].provider.APIKey == "" && cands[i].provider.APIKeyRef == "" {
			missing = append(missing, cands[i].name)
		}
	}
	if len(missing) > 0 {
		fmt.Printf("no API key was stored for: %s — fill it in with `ak edit <name>`\n",
			strings.Join(missing, ", "))
	}
	fmt.Println("The cc-switch database is unchanged; cc-switch keeps working as before.")
	return nil
}

// ccSwitchCandidates parses every raw row into a candidate. Unsupported app
// types are counted and dropped; the caller decides what to do with the rest.
func ccSwitchCandidates(cfg *config.Config, raws []ccswitch.Raw) ([]ccCandidate, map[string]int) {
	unsupported := map[string]int{}
	used := map[string]bool{}
	out := make([]ccCandidate, 0, len(raws))

	for _, r := range raws {
		var p config.Provider
		var notes []string
		switch r.AppType {
		case ccswitch.AppClaude:
			p, notes = claudeProviderFromCCSwitch(r.Settings)
		case ccswitch.AppCodex:
			p, notes = codexProviderFromCCSwitch(r.Settings)
		default:
			unsupported[r.AppType]++
			continue
		}

		c := ccCandidate{
			sourceID:   r.ID,
			appType:    r.AppType,
			sourceName: r.Name,
			provider:   p,
			current:    r.IsCurrent,
		}
		c.name = uniqueProviderName(cfg, used, slugify(r.Name), r.AppType+"-"+r.ID)
		used[c.name] = true

		// Fall back to the endpoint table: some providers keep only the base URL
		// there and leave it out of the env block.
		if c.provider.BaseURL == "" && len(r.Endpoints) > 0 {
			c.provider.BaseURL = r.Endpoints[0]
			if len(r.Endpoints) > 1 {
				notes = append(notes, fmt.Sprintf("using first of %d endpoints", len(r.Endpoints)))
			}
		}
		if c.provider.BaseURL == "" {
			c.skip = "no base_url"
		}
		if p.APIKey == "" && p.APIKeyRef == "" {
			notes = append(notes, "no API key")
		} else if c.skip == "" {
			c.ready = true
		}
		c.warning = strings.Join(notes, "; ")
		out = append(out, c)
	}
	return out, unsupported
}

// claudeProviderFromCCSwitch maps a cc-switch claude settings_config onto a
// provider. cc-switch stores the whole env block per provider, so every key ak
// does not map directly travels in Provider.Env and is exported verbatim. That
// preserves fable, *_MODEL_NAME, CLAUDE_CODE_* and CA-certificate keys.
func claudeProviderFromCCSwitch(settings string) (config.Provider, []string) {
	var doc struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal([]byte(settings), &doc); err != nil {
		return config.Provider{}, []string{"unreadable settings"}
	}
	p, consumed := claudeProviderFromEnv(doc.Env)
	for k, v := range doc.Env {
		if consumed[k] || v == "" {
			continue
		}
		if p.Env == nil {
			p.Env = map[string]string{}
		}
		p.Env[k] = v
	}
	if len(p.Env) == 0 {
		p.Env = nil
	}
	return p, nil
}

// codexProviderFromCCSwitch maps a cc-switch codex settings_config onto a
// provider. The profile is stored inline as a TOML string, so it reuses the
// same parser as the codexa import; only the secret lookup differs.
func codexProviderFromCCSwitch(settings string) (config.Provider, []string) {
	var doc struct {
		Auth   map[string]string `json:"auth"`
		Config string            `json:"config"`
	}
	if err := json.Unmarshal([]byte(settings), &doc); err != nil {
		return config.Provider{}, []string{"unreadable settings"}
	}
	if strings.TrimSpace(doc.Config) == "" {
		return config.Provider{}, []string{"no config"}
	}
	prof, err := parseCodexaProfile([]byte(doc.Config))
	if err != nil {
		return config.Provider{}, []string{"unparseable config"}
	}
	p := config.Provider{
		Kind:       config.KindCodex,
		BaseURL:    prof.BaseURL,
		Model:      prof.Model,
		Reasoning:  prof.Reasoning,
		WireAPI:    prof.WireAPI,
		ProviderID: prof.ProviderID,
		APIKey:     codexKeyFromAuth(doc.Auth, prof.EnvKey),
	}
	return p, nil
}

// codexKeyFromAuth picks the secret cc-switch stored for a codex provider. The
// env_key declared by the profile wins; OPENAI_API_KEY is the usual fallback,
// and any remaining value is used rather than dropping the key.
func codexKeyFromAuth(auth map[string]string, envKey string) string {
	if envKey != "" {
		if v := auth[envKey]; v != "" {
			return v
		}
	}
	if v := auth["OPENAI_API_KEY"]; v != "" {
		return v
	}
	keys := make([]string, 0, len(auth))
	for k := range auth {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if auth[k] != "" {
			return auth[k]
		}
	}
	return ""
}

// selectCCSwitch shows the multi-select and returns the chosen candidate
// indices. An empty result is valid and means nothing was selected; the caller
// distinguishes it from an abort by the nil error.
func selectCCSwitch(cands []ccCandidate) ([]int, error) {
	opts := make([]huh.Option[int], 0, len(cands))
	for i, c := range cands {
		o := huh.NewOption(ccSwitchLabel(c), i)
		// Pre-select only what is ready to use. A provider without a key would
		// be imported half configured and fail on its first launch; the user can
		// still tick it deliberately.
		if c.ready {
			o = o.Selected(true)
		}
		opts = append(opts, o)
	}

	height := len(cands) + 2
	if height > 18 {
		height = 18
	}
	var selected []int
	field := huh.NewMultiSelect[int]().
		Title("Import providers from cc-switch").
		Description("space toggles, ctrl+a selects all, / filters, enter imports, ctrl+c cancels").
		Options(opts...).
		Filterable(true).
		Value(&selected)
	field.Height(height)

	form := huh.NewForm(huh.NewGroup(field))
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, nil
		}
		return nil, err
	}
	return selected, nil
}

// ccSwitchLabel renders one row of the picker.
func ccSwitchLabel(c ccCandidate) string {
	status := ""
	switch {
	case c.skip != "":
		status = "  disabled: " + c.skip
	case c.warning != "":
		status = "  " + c.warning
	}
	current := ""
	if c.current {
		current = "  (current)"
	}
	return fmt.Sprintf("%-20s %-7s %-24s%s%s",
		c.name, c.appType, ellipsize(c.sourceName, 24), current, status)
}

// printCCSwitchScan reports what the scan found before the picker opens, so the
// user understands the list that follows.
func printCCSwitchScan(cands []ccCandidate, unsupported map[string]int) {
	ready, incomplete, broken := 0, 0, 0
	for _, c := range cands {
		switch {
		case c.skip != "":
			broken++
		case !c.ready:
			incomplete++
		default:
			ready++
		}
	}

	fmt.Printf("Scanned the cc-switch database: %d claude/codex provider(s).\n", len(cands))
	if ready > 0 {
		fmt.Printf("  %d ready to import\n", ready)
	}
	if incomplete > 0 {
		fmt.Printf("  %d missing an API key, not pre-selected\n", incomplete)
	}
	if broken > 0 {
		fmt.Printf("  %d unusable, no base_url\n", broken)
	}
	for _, app := range sortedKeys(unsupported) {
		fmt.Printf("  %d %s provider(s) skipped: ak only runs claude and codex\n",
			unsupported[app], app)
	}
	fmt.Println()
}

// slugify turns a display name into a valid provider name: lower case, with
// runs of anything that is not a letter, digit, dot, underscore or hyphen
// collapsed to a single hyphen.
func slugify(s string) string {
	var b strings.Builder
	pendingDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_':
			b.WriteRune(r)
			pendingDash = false
		case r == '-':
			b.WriteRune(r)
			pendingDash = false
		default:
			if !pendingDash && b.Len() > 0 {
				b.WriteByte('-')
				pendingDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-._")
}

// uniqueProviderName sanitises base and appends a numeric suffix until the name
// is free. Already-chosen names, existing providers and ak's reserved
// subcommand names are all avoided, so a batch of imports cannot collide with
// each other or shadow a command.
func uniqueProviderName(cfg *config.Config, used map[string]bool, base, fallback string) string {
	if base == "" {
		base = slugify(fallback)
	}
	if base == "" {
		base = "cc-provider"
	}
	candidate := base
	for i := 2; ; i++ {
		if !nameTaken(cfg, used, candidate) {
			return candidate
		}
		candidate = fmt.Sprintf("%s%d", base, i)
	}
}

func nameTaken(cfg *config.Config, used map[string]bool, name string) bool {
	if used[name] {
		return true
	}
	if _, ok := cfg.Providers[name]; ok {
		return true
	}
	return config.ValidateName(name) != nil
}

// ellipsize shortens s to at most n runes, appending an ellipsis.
func ellipsize(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n <= 1 {
		return string(runes[:n])
	}
	return string(runes[:n-1]) + "…"
}

// sortedKeys returns the map keys in sorted order, for deterministic output.
func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
