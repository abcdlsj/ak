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
// want: the one speaking it, else the nearest protocol another speaks.
func upstreamOf(cfg *config.Config, account string, want translate.Protocol) (config.Provider, translate.Protocol, bool) {
	if _, ok := vendor.Lookup(cfg.Accounts[account].Vendor); !ok {
		return config.Provider{}, "", false
	}
	for _, proto := range append([]translate.Protocol{want}, nearest[want]...) {
		if p, ok := cfg.OnAccountProtocol(account, string(proto)); ok {
			return p, proto, true
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
	mux.HandleFunc("GET /ak/v1/stats", s.handleLatency)
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
		started := time.Now()
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
		member := s.relay(t, r, s.sst, final, routes, conv, build, func(status int, retryAfter time.Duration, msg string) {
			if retryAfter > 0 {
				t.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
			}
			apiError(t, proto, status, "ak: "+name+msg)
		})
		if count {
			return // a token count is not a request to bill
		}
		// What the agent waited for, and what the whole request took: the
		// numbers a slow vendor shows up in first.
		first := t.first
		if first.IsZero() {
			first = time.Now()
		}
		s.lat.add(name, first.Sub(started), time.Since(started))
		acct, _, _ := strings.Cut(member, "/")
		u := t.done()
		row := UsageRow{Day: time.Now().Format(time.DateOnly), Token: who, Route: name,
			Account: acct, Model: models[member], Requests: 1, Tokens: u}
		if member == "" || t.status >= 400 {
			row.Failed = 1
		}
		s.meter.add(row)
		// What the vendor cached of the conversation decides whether the next
		// request of it stays here; a pool's own gateway never learns it. A
		// vendor that keeps a prompt cached without reporting the write says
		// nothing of what it now holds, and what it holds is the prompt this
		// turn sent: counting it apart would let one turn's cache be thrown
		// away by the next.
		if member != "" && conv != "" {
			cache := u.CacheRead + u.CacheWrite
			if u.CacheWrite == 0 {
				cache += u.Input
			}
			s.sst.noteCache(final, member, conv, cache)
		}
	}
}

// serviceLeg is how one member, an account's model, answers a request in
// proto: untranslated when the account has an endpoint of that protocol, else
// translated for the nearest one it does have. An endpoint the account is
// known to refuse for this model is skipped, so a protocol that does serve it
// is found instead of asking a vendor what it has already answered.
func (s *Server) serviceLeg(cfg *config.Config, proto translate.Protocol, count bool, r *http.Request, body []byte, member string) (leg, string, error) {
	ref, err := cfg.ParseModel(member)
	if err != nil || ref.Account == "" {
		return leg{}, "", fmt.Errorf("no account for %q", member)
	}
	var refused []string
	sawEndpoint, sawModel := false, false
	for _, upProto := range append([]translate.Protocol{proto}, nearest[proto]...) {
		if count && upProto != translate.Anthropic {
			continue
		}
		up, ok := cfg.OnAccountProtocol(ref.Account, string(upProto))
		if !ok {
			continue
		}
		sawEndpoint = true
		model := ref.Model
		if model == "" {
			model = up.Model
		}
		if model == "" {
			continue
		}
		sawModel = true
		if x, blocked := s.ref.get(ref.Account, model, string(upProto)); blocked {
			refused = append(refused, fmt.Sprintf("%s on %s (%s)", model, upProto, x.Reason))
			continue
		}
		lg := leg{member: member, p: up, target: endpoint(up, upProto, count), model: model, protocol: string(upProto),
			header: func(h http.Header) {
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
		lg.reply = func(w http.ResponseWriter) replyWriter { return newTranslated(w, upProto, proto, model) }
		return lg, model, nil
	}
	if len(refused) > 0 {
		return leg{}, "", &refusedError{account: ref.Account, what: strings.Join(refused, ", ")}
	}
	switch {
	case count && !sawEndpoint:
		return leg{}, "", fmt.Errorf("account %s has no Anthropic endpoint to count tokens", ref.Account)
	case !sawEndpoint:
		return leg{}, "", fmt.Errorf("account %s serves no endpoint", ref.Account)
	case !sawModel:
		return leg{}, "", fmt.Errorf("account %s has no default model for %s; name one as %s/<model>", ref.Account, proto, ref.Account)
	}
	return leg{}, "", fmt.Errorf("account %s serves no endpoint in %s", ref.Account, proto)
}

// refusedError is an account that will not serve a model, as it already said:
// the request is answered from that, not by asking the vendor again.
type refusedError struct {
	account string
	what    string // "model on protocol (reason)", one per endpoint refused
}

func (e *refusedError) Error() string {
	return fmt.Sprintf("account %s refuses %s", e.account, e.what)
}

// ModelInfo is one model name a token may use, and how each protocol
// reaches it: native, translated, mixed (a pool with both), absent (no member
// serves that protocol) or refused (they said they will not, this account).
type ModelInfo struct {
	ID        string            `json:"id"`
	Protocols map[string]string `json:"protocols"`
	// Models are the models a pool's members answer with, when they are not
	// all the same: a name that may answer as more than one model says which.
	Models []string `json:"models,omitempty"`
	// Refused is what an account has already answered that it does not serve.
	Refused []RefusalRow `json:"refused,omitempty"`
}

// Models lists the model names a token may use: its routes, and for each
// account the account and its endpoints' default models.
func (s *Server) Models(cfg *config.Config, t config.Token) []ModelInfo {
	var out []ModelInfo
	for _, n := range cfg.Service.RouteNames() {
		if allows(t, n) {
			out = append(out, s.info(cfg, n))
		}
	}
	for _, a := range cfg.AccountNames() {
		if !allows(t, a) {
			continue
		}
		out = append(out, s.info(cfg, a))
		seen := map[string]bool{}
		v, _ := vendor.Lookup(cfg.Accounts[a].Vendor)
		for _, proto := range v.EndpointProtocols() {
			p, ok := cfg.OnAccountProtocol(a, proto)
			if id := a + "/" + p.Model; ok && p.Model != "" && !seen[id] {
				seen[id] = true
				out = append(out, s.info(cfg, id))
			}
		}
	}
	return out
}

// info is one model name as the service reports it.
func (s *Server) info(cfg *config.Config, name string) ModelInfo {
	return ModelInfo{
		ID:        name,
		Protocols: s.reach(cfg, name),
		Models:    s.modelsOf(cfg, name),
		Refused:   s.refusedFor(cfg, name),
	}
}

// reach says how each protocol reaches a model name.
func (s *Server) reach(cfg *config.Config, name string) map[string]string {
	out := map[string]string{}
	for _, proto := range translate.Protocols {
		var native, translated, refused, served bool
		for _, leaf := range leafRefs(cfg, name, 0) {
			p, got, ok := upstreamOf(cfg, leaf.Account, proto)
			if !ok {
				continue
			}
			served = true
			model := leaf.Model
			if model == "" {
				model = p.Model
			}
			if _, blocked := s.ref.get(leaf.Account, model, string(got)); blocked {
				refused = true
				continue
			}
			native = native || got == proto
			translated = translated || got != proto
		}
		switch {
		case !served:
			out[string(proto)] = "absent"
		case native && translated:
			out[string(proto)] = "mixed"
		case native:
			out[string(proto)] = "native"
		case translated:
			out[string(proto)] = "translated"
		case refused:
			out[string(proto)] = "refused"
		}
	}
	return out
}

// modelsOf lists the models a pool's members answer with, when they differ:
// a name may be a fallback chain over models, and a client deserves to know
// which one it may get.
func (s *Server) modelsOf(cfg *config.Config, name string) []string {
	seen := map[string]bool{}
	var out []string
	for _, leaf := range leafRefs(cfg, name, 0) {
		model := leaf.Model
		if model == "" {
			if p, _, ok := upstreamOf(cfg, leaf.Account, translate.Anthropic); ok {
				model = p.Model
			}
		}
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		out = append(out, model)
	}
	slices.Sort(out)
	if len(out) < 2 {
		return nil
	}
	return out
}

// refusedFor lists what the accounts behind a name have refused: a name that
// asks for one model says only what was said of that one, a bare account says
// all of it.
func (s *Server) refusedFor(cfg *config.Config, name string) []RefusalRow {
	var out []RefusalRow
	for _, leaf := range leafRefs(cfg, name, 0) {
		for _, row := range s.ref.of(leaf.Account) {
			if leaf.Model == "" || leaf.Model == row.Model {
				out = append(out, row)
			}
		}
	}
	slices.SortFunc(out, func(a, b RefusalRow) int {
		return strings.Compare(a.Model+a.Protocol, b.Model+b.Protocol)
	})
	return out
}

// leafRefs lists the accounts and models a model name ends at.
func leafRefs(cfg *config.Config, name string, depth int) []config.ModelRef {
	ref, err := cfg.ParseModel(name)
	if err != nil || depth > config.MaxPoolDepth+1 {
		return nil
	}
	if ref.Account != "" {
		return []config.ModelRef{ref}
	}
	r := cfg.Service.Routes[ref.Route]
	var out []config.ModelRef
	for _, m := range append(slices.Clone(r.Members), r.Model) {
		if m != "" {
			out = append(out, leafRefs(cfg, m, depth+1)...)
		}
	}
	return out
}

// leafAccounts lists the accounts a model name ends at.
func leafAccounts(cfg *config.Config, name string, depth int) []string {
	var out []string
	for _, ref := range leafRefs(cfg, name, depth) {
		out = append(out, ref.Account)
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
	models := s.Models(cfg, tok)
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		ak := map[string]any{"protocols": m.Protocols}
		if len(m.Models) > 0 {
			ak["models"] = m.Models
		}
		if len(m.Refused) > 0 {
			ak["refused"] = m.Refused
		}
		e := map[string]any{"id": m.ID, "ak": ak}
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

// handleLatency serves GET /ak/v1/stats: each route's recent first-byte and
// whole-request timings, which is where a slow vendor shows up first.
func (s *Server) handleLatency(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := caller(s.config(), r); !ok {
		apiError(w, translate.Chat, http.StatusUnauthorized, "ak: no valid token")
		return
	}
	type row struct {
		Route    string           `json:"route"`
		Requests int              `json:"requests"`
		First    map[string]int64 `json:"first_ms"`
		Total    map[string]int64 `json:"total_ms"`
	}
	rows := s.Latencies()
	out := make([]row, 0, len(rows))
	for _, l := range rows {
		out = append(out, row{Route: l.Route, Requests: l.Requests,
			First: map[string]int64{"p50": l.First50.Milliseconds(), "p95": l.First95.Milliseconds()},
			Total: map[string]int64{"p50": l.Total50.Milliseconds(), "p95": l.Total95.Milliseconds()}})
	}
	writeJSON(w, map[string]any{"routes": out})
}

// Usage is every token's usage over the last days days, for the web UI.
func (s *Server) Usage(days int) []UsageRow { return s.meter.since(days, "") }

// RouteReach returns how each protocol reaches route name (native, translated,
// mixed, refused, or absent).
func (s *Server) RouteReach(name string) map[string]string {
	return s.reach(s.config(), name)
}

// AccountRefusals returns the remembered refusals for account.
func (s *Server) AccountRefusals(account string) []RefusalRow {
	rows := s.ref.of(account)
	slices.SortFunc(rows, func(a, b RefusalRow) int {
		return strings.Compare(a.Model+a.Protocol, b.Model+b.Protocol)
	})
	return rows
}

// ForgetRefusals drops remembered refusals for account (or all if empty).
func (s *Server) ForgetRefusals(account string) {
	s.ref.forgetAccount(account)
	s.ref.save(s.refusalPath)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
