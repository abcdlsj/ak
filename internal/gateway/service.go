package gateway

// The service: ak's gateway answering other machines. One base URL speaks
// Anthropic Messages and OpenAI Responses and Chat; the model name picks the
// route (an account, an account's model, an alias or a pool), a bearer
// token per machine says who may call, and each request goes to an upstream
// of its own protocol untranslated, or through internal/translate when the
// account has none. Failover, cooldown, affinity and the strategies are the
// pool gateway's own, kept in a state of the service's. See docs/service.md.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/translate"
	"github.com/abcdlsj/ak/internal/vendor"
)

// NewToken makes a service token and the hash the config keeps of it.
func NewToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	token = "ak_" + base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken is the hash a token is kept as.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// CheckExposure refuses to serve addr to other machines while no token
// could be let in: a service on a public address must have tokens.
func CheckExposure(addr string, cfg *config.Config) error {
	if len(cfg.Service.Tokens) > 0 || loopback(addr) {
		return nil
	}
	return fmt.Errorf("the service would listen on %s, reachable from other machines, with no token defined; "+
		"issue one with `ak serve token add <name>` or listen on 127.0.0.1", addr)
}

func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// routing is the service's routes as the pool machinery reads them: each
// pool route a pool over its members, aliases followed, and each member
// that is an account's model a concrete provider on that account, which a
// smart pool reads the quota of.
func routing(cfg *config.Config) *config.Config {
	t := &config.Config{Providers: map[string]config.Provider{}, Accounts: cfg.Accounts}
	for _, name := range cfg.Service.RouteNames() {
		r := cfg.Service.Routes[name]
		if !r.IsPool() {
			continue
		}
		members := make([]string, 0, len(r.Members))
		for _, m := range r.Members {
			f := unalias(cfg, m)
			members = append(members, f)
			if ref, err := cfg.ParseModel(f); err == nil && ref.Route == "" {
				p, _ := cfg.AccountProvider(ref.Account)
				t.Providers[f] = p
			}
		}
		t.Providers[name] = config.Provider{Members: members, Strategy: r.Strategy}
	}
	return t
}

// unalias follows aliases from a model name to a pool or an account's model.
func unalias(cfg *config.Config, name string) string {
	for range config.MaxPoolDepth + 1 {
		r, ok := cfg.Service.Routes[name]
		if !ok || r.Model == "" {
			return name
		}
		name = r.Model
	}
	return name
}

// protocolOf is the protocol an engine endpoint speaks.
func protocolOf(p config.Provider) translate.Protocol {
	switch p.Kind {
	case config.KindCodex:
		if p.WireAPI == "chat" {
			return translate.Chat
		}
		return translate.Responses
	case config.KindPi:
		switch p.PiAPI {
		case "openai-completions":
			return translate.Chat
		case "openai-responses":
			return translate.Responses
		}
	}
	return translate.Anthropic
}

// nearest orders, for a client protocol, the others an upstream may speak
// instead, the one translated most directly first.
var nearest = map[translate.Protocol][]translate.Protocol{
	translate.Anthropic: {translate.Chat, translate.Responses},
	translate.Responses: {translate.Chat, translate.Anthropic},
	translate.Chat:      {translate.Responses, translate.Anthropic},
}

// upstreamOf picks the endpoint of an account that answers a request in
// want: the first engine speaking it, else the nearest protocol another
// engine speaks.
func upstreamOf(cfg *config.Config, account string, want translate.Protocol) (config.Provider, translate.Protocol, bool) {
	v, ok := vendor.Lookup(cfg.Accounts[account].Vendor)
	if !ok {
		return config.Provider{}, "", false
	}
	var eps []config.Provider
	for _, k := range v.Kinds() {
		if p, ok := cfg.OnAccount(account, config.Kind(k)); ok {
			eps = append(eps, p)
		}
	}
	for _, proto := range append([]translate.Protocol{want}, nearest[want]...) {
		for _, p := range eps {
			if protocolOf(p) == proto {
				return p, proto, true
			}
		}
	}
	return config.Provider{}, "", false
}

// endpoint is the upstream URL for a request in proto; count asks for an
// Anthropic token count.
func endpoint(p config.Provider, proto translate.Protocol, count bool) string {
	base := strings.TrimRight(p.BaseURL, "/")
	switch {
	case proto == translate.Anthropic && count:
		return base + "/v1/messages/count_tokens"
	case proto == translate.Anthropic:
		return base + "/v1/messages"
	case proto == translate.Responses:
		return base + "/responses"
	}
	return base + "/chat/completions"
}

// caller is the token a request presents, by name.
func caller(cfg *config.Config, r *http.Request) (string, config.Token, bool) {
	tok := strings.TrimSpace(r.Header.Get("X-Api-Key"))
	if v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		tok = strings.TrimSpace(v)
	}
	if tok == "" {
		return "", config.Token{}, false
	}
	h := []byte(HashToken(tok))
	for _, name := range cfg.Service.TokenNames() {
		t := cfg.Service.Tokens[name]
		if subtle.ConstantTimeCompare([]byte(t.Hash), h) == 1 {
			return name, t, true
		}
	}
	return "", config.Token{}, false
}

// allows reports whether a token may use a model name: every name when it
// is not limited, else a route it lists, or an account it lists with any
// of that account's models.
func allows(t config.Token, name string) bool {
	if len(t.Routes) == 0 {
		return true
	}
	acct, _, _ := strings.Cut(name, "/")
	return slices.Contains(t.Routes, name) || slices.Contains(t.Routes, acct)
}

// accountsOf lists the accounts a token reaches: every account, or those it
// lists and those under the routes it lists.
func accountsOf(cfg *config.Config, t config.Token) []string {
	if len(t.Routes) == 0 {
		return cfg.AccountNames()
	}
	seen := map[string]bool{}
	var walk func(name string, depth int)
	walk = func(name string, depth int) {
		ref, err := cfg.ParseModel(name)
		if err != nil || depth > config.MaxPoolDepth+1 {
			return
		}
		if ref.Account != "" {
			seen[ref.Account] = true
			return
		}
		r := cfg.Service.Routes[ref.Route]
		for _, m := range append(slices.Clone(r.Members), r.Model) {
			if m != "" {
				walk(m, depth+1)
			}
		}
	}
	for _, n := range t.Routes {
		walk(n, 0)
	}
	var out []string
	for _, a := range cfg.AccountNames() {
		if seen[a] {
			out = append(out, a)
		}
	}
	return out
}

// ServiceHandler is the handler of the service's own listener.
func (s *Server) ServiceHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", s.handleModel(translate.Anthropic, false))
	mux.HandleFunc("POST /v1/messages/count_tokens", s.handleModel(translate.Anthropic, true))
	mux.HandleFunc("POST /v1/responses", s.handleModel(translate.Responses, false))
	mux.HandleFunc("POST /v1/chat/completions", s.handleModel(translate.Chat, false))
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("GET /ak/v1/usage", s.handleUsage)
	mux.HandleFunc("GET /ak/v1/quota", s.handleQuota)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}` + "\n"))
	})
	if s.UI != nil {
		mux.Handle("/ui/", s.UI)
		mux.Handle("GET /{$}", http.RedirectHandler("/ui/", http.StatusSeeOther))
	}
	return mux
}

// apiError answers in the client's protocol.
func apiError(w http.ResponseWriter, proto translate.Protocol, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(translate.Error(proto, status, msg))
}

// handleModel serves one model API: authenticate, resolve the model name,
// and relay to the route's members.
func (s *Server) handleModel(proto translate.Protocol, count bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := s.config()
		who, tok, ok := caller(cfg, r)
		if !ok {
			apiError(w, proto, http.StatusUnauthorized, "ak: no valid token; send it as Authorization: Bearer <token>")
			return
		}
		s.live.setLimit(cfg.Settings.MaxInflight)
		if !s.live.enter() {
			w.Header().Set("Retry-After", "1")
			apiError(w, proto, http.StatusServiceUnavailable, "ak: the service is at its in-flight limit; retry shortly")
			return
		}
		defer s.live.leave()
		body, ok := readBody(w, r)
		if !ok {
			return
		}
		var req struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(body, &req) != nil || req.Model == "" {
			apiError(w, proto, http.StatusBadRequest, "ak: the request names no model")
			return
		}
		name := req.Model
		final := unalias(cfg, name)
		ref, err := cfg.ParseModel(final)
		if err != nil {
			apiError(w, proto, http.StatusNotFound, fmt.Sprintf("ak: unknown model %q (%v); GET /v1/models lists what this token may use", name, err))
			return
		}
		if !allows(tok, name) {
			apiError(w, proto, http.StatusForbidden, fmt.Sprintf("ak: token %q may not use %q", who, name))
			return
		}

		conv := conversationKey(r, body)
		svc := s.svc.Load()
		routes := []route{{member: final}}
		if ref.Route != "" {
			if routes = s.sst.candidates(svc, final, svc.Providers[final], conv); len(routes) == 0 {
				apiError(w, proto, http.StatusServiceUnavailable, "ak: route "+final+" has no members")
				return
			}
		}
		models := map[string]string{}
		build := func(rt route) (leg, error) {
			lg, model, err := s.serviceLeg(cfg, proto, count, r, body, rt.member)
			models[rt.member] = model
			return lg, err
		}
		t := newTap(w)
		member := s.relay(t, r, s.sst, final, routes, conv, build, func(status int, msg string) {
			apiError(t, proto, status, "ak: "+name+msg)
		})
		if count {
			return // a token count is not a request to bill
		}
		acct, _, _ := strings.Cut(member, "/")
		row := UsageRow{Day: time.Now().Format(time.DateOnly), Token: who, Route: name,
			Account: acct, Model: models[member], Requests: 1, Tokens: t.done()}
		if member == "" || t.status >= 400 {
			row.Failed = 1
		}
		s.meter.add(row)
	}
}

// serviceLeg is how one member, an account's model, answers a request in
// proto: untranslated when the account has an upstream of that protocol,
// else translated for the nearest one. It returns the model asked for.
func (s *Server) serviceLeg(cfg *config.Config, proto translate.Protocol, count bool, r *http.Request, body []byte, member string) (leg, string, error) {
	ref, err := cfg.ParseModel(member)
	if err != nil || ref.Account == "" {
		return leg{}, "", fmt.Errorf("no account for %q", member)
	}
	up, upProto, ok := upstreamOf(cfg, ref.Account, proto)
	if !ok {
		return leg{}, "", fmt.Errorf("account %s serves no endpoint", ref.Account)
	}
	model := ref.Model
	if model == "" {
		model = up.Model
	}
	if model == "" {
		return leg{}, "", fmt.Errorf("account %s has no default model for %s; name one as %s/<model>", ref.Account, upProto, ref.Account)
	}
	if count && upProto != translate.Anthropic {
		return leg{}, model, fmt.Errorf("account %s has no Anthropic endpoint to count tokens", ref.Account)
	}
	lg := leg{member: member, p: up, target: endpoint(up, upProto, count), header: func(h http.Header) {
		h.Del("Cookie")
		if upProto == translate.Anthropic && h.Get("Anthropic-Version") == "" {
			h.Set("Anthropic-Version", "2023-06-01")
		}
	}}
	if upProto == proto {
		lg.body = rewriteModel(body, model)
		if r.URL.RawQuery != "" {
			lg.target += "?" + r.URL.RawQuery
		}
		return lg, model, nil
	}
	lg.body, err = translate.Request(proto, upProto, body, model)
	if err != nil {
		return leg{}, model, fmt.Errorf("cannot translate %s to %s: %w", proto, upProto, err)
	}
	lg.reply = func(w http.ResponseWriter) replyWriter { return newTranslated(w, upProto, proto) }
	return lg, model, nil
}

// ModelInfo is one model name a token may use, and how each protocol
// reaches it: native, translated, mixed (a pool with both) or absent.
type ModelInfo struct {
	ID        string            `json:"id"`
	Protocols map[string]string `json:"protocols"`
}

// Models lists the model names a token may use: its routes, and for each
// account the account and its engines' default models.
func Models(cfg *config.Config, t config.Token) []ModelInfo {
	var out []ModelInfo
	for _, n := range cfg.Service.RouteNames() {
		if allows(t, n) {
			out = append(out, ModelInfo{ID: n, Protocols: reach(cfg, n)})
		}
	}
	for _, a := range cfg.AccountNames() {
		if !allows(t, a) {
			continue
		}
		out = append(out, ModelInfo{ID: a, Protocols: reach(cfg, a)})
		seen := map[string]bool{}
		v, _ := vendor.Lookup(cfg.Accounts[a].Vendor)
		for _, k := range v.Kinds() {
			p, ok := cfg.OnAccount(a, config.Kind(k))
			if id := a + "/" + p.Model; ok && p.Model != "" && !seen[id] {
				seen[id] = true
				out = append(out, ModelInfo{ID: id, Protocols: reach(cfg, id)})
			}
		}
	}
	return out
}

// reach says how each protocol reaches a model name.
func reach(cfg *config.Config, name string) map[string]string {
	out := map[string]string{}
	for _, proto := range translate.Protocols {
		var native, translated bool
		for _, acct := range leafAccounts(cfg, name, 0) {
			if _, got, ok := upstreamOf(cfg, acct, proto); ok {
				native = native || got == proto
				translated = translated || got != proto
			}
		}
		switch {
		case native && translated:
			out[string(proto)] = "mixed"
		case native:
			out[string(proto)] = "native"
		case translated:
			out[string(proto)] = "translated"
		}
	}
	return out
}

// leafAccounts lists the accounts a model name ends at.
func leafAccounts(cfg *config.Config, name string, depth int) []string {
	ref, err := cfg.ParseModel(name)
	if err != nil || depth > config.MaxPoolDepth+1 {
		return nil
	}
	if ref.Account != "" {
		return []string{ref.Account}
	}
	r := cfg.Service.Routes[ref.Route]
	var out []string
	for _, m := range append(slices.Clone(r.Members), r.Model) {
		if m != "" {
			out = append(out, leafAccounts(cfg, m, depth+1)...)
		}
	}
	return out
}

// handleModels serves GET /v1/models, in Anthropic's shape when the client
// says it speaks Anthropic's API.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	cfg := s.config()
	proto := translate.Chat
	if r.Header.Get("Anthropic-Version") != "" {
		proto = translate.Anthropic
	}
	_, tok, ok := caller(cfg, r)
	if !ok {
		apiError(w, proto, http.StatusUnauthorized, "ak: no valid token")
		return
	}
	models := Models(cfg, tok)
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		e := map[string]any{"id": m.ID, "ak": map[string]any{"protocols": m.Protocols}}
		if proto == translate.Anthropic {
			e["type"], e["display_name"], e["created_at"] = "model", m.ID, "1970-01-01T00:00:00Z"
		} else {
			e["object"], e["created"], e["owned_by"] = "model", 0, "ak"
		}
		data = append(data, e)
	}
	var body map[string]any
	if proto == translate.Anthropic {
		body = map[string]any{"data": data, "has_more": false, "first_id": nil, "last_id": nil}
		if len(models) > 0 {
			body["first_id"], body["last_id"] = models[0].ID, models[len(models)-1].ID
		}
	} else {
		body = map[string]any{"object": "list", "data": data}
	}
	writeJSON(w, body)
}

// handleUsage serves GET /ak/v1/usage: the calling token's own usage.
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	who, _, ok := caller(s.config(), r)
	if !ok {
		apiError(w, translate.Chat, http.StatusUnauthorized, "ak: no valid token")
		return
	}
	days, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil || days <= 0 || days > usageDays {
		days = 7
	}
	writeJSON(w, map[string]any{"days": days, "token": who, "rows": s.meter.since(days, who)})
}

// Usage is every token's usage over the last days days, for the web UI.
func (s *Server) Usage(days int) []UsageRow { return s.meter.since(days, "") }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
