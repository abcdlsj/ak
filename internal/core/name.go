package core

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/vendor"
)

// SuggestName names a provider after what it runs on and its engine,
// <source>-<engine>: kimi-claude, commandcode-codex, cpa-codex. The source
// is its account's vendor by the vendor's short name, a provider pi already
// knows, or its endpoint's host. A name already taken gets -2, -3, …; a pool,
// or an endpoint that names nothing (an IP address), gets "".
func SuggestName(cfg *config.Config, p config.Provider) string {
	src := source(cfg, p)
	if src == "" || p.Kind == "" {
		return ""
	}
	base := src + "-" + string(p.Kind)
	if config.ValidateName(base) != nil {
		return ""
	}
	for name, i := base, 2; ; name, i = fmt.Sprintf("%s-%d", base, i), i+1 {
		if _, taken := cfg.Providers[name]; !taken {
			return name
		}
	}
}

// NameFor is name, or when it is blank the suggested one; it fails when
// nothing suggests one.
func NameFor(cfg *config.Config, name string, p config.Provider) (string, error) {
	if name = strings.TrimSpace(name); name != "" {
		return name, nil
	}
	if name = SuggestName(cfg, p); name == "" {
		return "", fmt.Errorf("name the provider: nothing it runs on suggests a name")
	}
	return name, nil
}

// source is what a provider runs on, as a name may say it.
func source(cfg *config.Config, p config.Provider) string {
	switch {
	case p.IsPool():
		return ""
	case p.Account != "":
		if v, ok := vendor.Lookup(cfg.Accounts[p.Account].Vendor); ok {
			return v.ShortName()
		}
		return p.Account
	case p.PiProvider != "":
		return strings.ToLower(p.PiProvider)
	}
	return hostSource(p.BaseURL)
}

// genericLabel marks a host's first label that names a role, not a vendor:
// api.commandcode.ai is commandcode's, api-ai-coding.bilibili.co bilibili's.
func genericLabel(l string) bool {
	switch {
	case l == "www", l == "gateway", l == "llm", l == "ai", strings.HasPrefix(l, "api"):
		return true
	}
	return false
}

// hostSource names an endpoint by its host: the first label, unless that
// names a role, then the domain's own label. An IP address or localhost
// names nothing.
func hostSource(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || host == "localhost" || net.ParseIP(host) != nil {
		return ""
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return labels[0]
	}
	if genericLabel(labels[0]) {
		return labels[len(labels)-2]
	}
	return labels[0]
}
