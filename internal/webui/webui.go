// Package webui is the service's web UI: accounts and their quota, routes,
// tokens and usage, behind an admin password. It is Go templates and a
// little vanilla JS, embedded, served by `ak serve` under /ui/.
//
// Only an admin session opens it: an API token never does, and the session
// cookie is never accepted by the model API. Every change goes through core
// and providers.toml, as the CLI's do, and the running service is reloaded
// with the result.
package webui

import (
	"cmp"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/gateway"
	"github.com/abcdlsj/ak/internal/login"
	"github.com/abcdlsj/ak/internal/quota"
	"github.com/abcdlsj/ak/internal/vendor"
	"github.com/abcdlsj/ak/internal/version"
	"golang.org/x/crypto/bcrypt"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Backend is what the UI needs of the running service.
type Backend interface {
	Config() *config.Config
	Reload(*config.Config)
	Usage(days int) []gateway.UsageRow
	QuotasNow(accounts []string) []quota.Quota
	RefreshQuotas(accounts []string)
	Latencies() []gateway.LatencyRow
	Forget(account string)
}

// maxFormBytes is the largest form the UI reads: a key, a few routes.
const maxFormBytes = 1 << 20

// UI serves the pages.
type UI struct {
	b        Backend
	sessions *sessions
	pages    map[string]*template.Template
	// mu serializes changes, so two forms never save over each other.
	mu sync.Mutex
	// devices are the device sign-ins under way, by id.
	devices *devices
	// now is the clock, so tests can move it.
	now func() time.Time
}

// New builds the UI over a running service.
func New(b Backend) *UI {
	u := &UI{b: b, sessions: newSessions(), pages: map[string]*template.Template{},
		devices: newDevices(), now: time.Now}
	funcs := template.FuncMap{
		"join": strings.Join, "num": number, "human": humanize, "pix": pix,
		"contains": func(list []string, s string) bool { return slices.Contains(list, s) },
		"title":    func(s string) string { return strings.ToUpper(s[:1]) + s[1:] },
	}
	for _, p := range []string{"login", "setup", "accounts", "device", "routes", "tokens", "usage"} {
		u.pages[p] = template.Must(template.New("layout.html").Funcs(funcs).ParseFS(templateFS,
			"templates/layout.html", "templates/components.html", "templates/"+p+".html"))
	}
	return u
}

// ServeHTTP serves /ui/.
func (u *UI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	// Inline styles carry the bars' shares and heatmap nothing else; the
	// values are numbers this package computes, never text from a request.
	h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Cache-Control", "no-store")
	if r.Method == http.MethodPost && !sameOrigin(r) {
		http.Error(w, "ak: cross-origin request refused", http.StatusForbidden)
		return
	}
	// A form is small; refuse a body that is not, before it is read.
	if r.Method == http.MethodPost {
		if r.ContentLength > maxFormBytes {
			http.Error(w, "ak: form too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	}
	path := strings.TrimPrefix(r.URL.Path, "/ui")
	if strings.HasPrefix(path, "/static/") {
		sub, _ := fs.Sub(staticFS, "static")
		h.Set("Cache-Control", "max-age=3600")
		http.StripPrefix("/ui/static/", http.FileServerFS(sub)).ServeHTTP(w, r)
		return
	}
	if u.b.Config().Service.AdminPassword == "" {
		u.render(w, "setup", nil, nil)
		return
	}
	if path == "/login" {
		u.login(w, r)
		return
	}
	id, ss, ok := u.sessions.get(r)
	if !ok {
		http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodPost && !equal(r.PostFormValue("csrf"), ss.csrf) {
		http.Error(w, "ak: the form is stale; reload the page", http.StatusForbidden)
		return
	}
	c := &call{u: u, w: w, r: r, id: id, ss: ss}
	switch {
	case path == "/" || path == "":
		http.Redirect(w, r, "/ui/accounts", http.StatusSeeOther)
	case path == "/logout" && r.Method == http.MethodPost:
		u.sessions.end(id)
		setCookie(w, r, sessionCookie, "", -1)
		http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
	case path == "/accounts" && r.Method == http.MethodGet:
		c.accounts()
	case path == "/accounts/key" && r.Method == http.MethodPost:
		c.addKey()
	case path == "/accounts/device" && r.Method == http.MethodPost:
		c.startDevice()
	case path == "/accounts/device" && r.Method == http.MethodGet:
		c.device()
	case path == "/accounts/device/status" && r.Method == http.MethodGet:
		c.deviceStatus()
	case path == "/accounts/remove" && r.Method == http.MethodPost:
		c.removeAccount()
	case path == "/routes" && r.Method == http.MethodGet:
		c.routes()
	case path == "/routes/save" && r.Method == http.MethodPost:
		c.saveRoute()
	case path == "/routes/remove" && r.Method == http.MethodPost:
		c.removeRoute()
	case path == "/tokens" && r.Method == http.MethodGet:
		c.tokens()
	case path == "/tokens/add" && r.Method == http.MethodPost:
		c.addToken()
	case path == "/tokens/save" && r.Method == http.MethodPost:
		c.saveToken()
	case path == "/tokens/revoke" && r.Method == http.MethodPost:
		c.revokeToken()
	case path == "/usage" && r.Method == http.MethodGet:
		c.usage()
	default:
		http.NotFound(w, r)
	}
}

// login serves the login form and checks the password.
func (u *UI) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		u.loginForm(w, r, 0, "")
		return
	}
	now := u.now()
	if locked, until := u.sessions.locked(now); locked {
		w.Header().Set("Retry-After", fmt.Sprint(int(until.Sub(now).Seconds())+1))
		u.loginForm(w, r, http.StatusTooManyRequests, "Too many failed logins; try again in a few minutes.")
		return
	}
	c, err := r.Cookie(loginCookie)
	if err != nil || !equal(r.PostFormValue("csrf"), c.Value) {
		u.loginForm(w, r, http.StatusForbidden, "That form had expired; log in again.")
		return
	}
	hash := u.b.Config().Service.AdminPassword
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(r.PostFormValue("password"))) != nil {
		u.sessions.failed(now)
		u.loginForm(w, r, http.StatusUnauthorized, "Wrong password.")
		return
	}
	setCookie(w, r, loginCookie, "", -1)
	setCookie(w, r, sessionCookie, u.sessions.start(), int(sessionTTL.Seconds()))
	http.Redirect(w, r, "/ui/accounts", http.StatusSeeOther)
}

// loginToken is the form's CSRF token: the browser's existing ak_login cookie
// when it has one. Reusing it keeps a form already on screen valid, so a
// second tab or a reload does not make the first one fail to submit.
func (u *UI) loginToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(loginCookie); err == nil && c.Value != "" {
		return c.Value
	}
	tok := randomID()
	setCookie(w, r, loginCookie, tok, 600)
	return tok
}

// loginForm renders the login page with a token, why it is back, and a status
// (0 for 200). It always answers with the form, so an expired one is answered
// by a fresh one rather than a dead end.
func (u *UI) loginForm(w http.ResponseWriter, r *http.Request, status int, msg string) {
	data := map[string]any{"CSRF": u.loginToken(w, r)}
	if msg != "" {
		data["Error"] = msg
	}
	if status != 0 {
		w.WriteHeader(status)
	}
	u.render(w, "login", data, nil)
}

// render writes a page in the layout.
func (u *UI) render(w http.ResponseWriter, page string, data map[string]any, c *call) {
	if data == nil {
		data = map[string]any{}
	}
	data["Page"] = page
	data["Version"] = version.Display()
	if c != nil {
		data["CSRF"] = c.ss.csrf
		data["Flash"], data["Token"] = u.sessions.take(c.ss)
		data["Signed"] = true
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := u.pages[page].Execute(w, data); err != nil {
		http.Error(w, "ak: "+err.Error(), http.StatusInternalServerError)
	}
}

// call is one signed-in request.
type call struct {
	u  *UI
	w  http.ResponseWriter
	r  *http.Request
	id string
	ss *session
}

// back redirects to a page with a message for it.
func (c *call) back(page, flash string) {
	c.u.sessions.tell(c.ss, flash, "")
	http.Redirect(c.w, c.r, "/ui/"+page, http.StatusSeeOther)
}

// change loads the config from disk, applies a change through core, and
// reloads the service with what was saved.
func (c *call) change(do func(cfg *config.Config) error) error {
	c.u.mu.Lock()
	defer c.u.mu.Unlock()
	cfg, err := core.Load()
	if err != nil {
		return err
	}
	if err := do(cfg); err != nil {
		return err
	}
	c.u.b.Reload(cfg)
	return nil
}

// accountView is an account as the Accounts page shows it: the board's card,
// as markup.
type accountView struct {
	Name, Vendor, User string
	Users              []string
	Hue                int
	Act                activity
	Meters             []meterView
	Balance            *amountView
	Err                string
	NoSource           bool
	NoQuota            bool
	Pending            bool // the first reading of this account is under way
}

func (c *call) accounts() {
	cfg := c.u.b.Config()
	names := cfg.AccountNames()
	// What was read last answers the page at once; a vendor is asked in the
	// background, so a slow one cannot hold the page for its timeout.
	qs := c.u.b.QuotasNow(names)
	c.u.b.RefreshQuotas(names)
	act := activities(c.u.b.Usage(activityDays), func(r gateway.UsageRow) string { return r.Account })
	views := make([]accountView, len(names))
	for i, n := range names {
		a := cfg.Accounts[n]
		v, _ := vendor.Lookup(a.Vendor)
		q := qs[i]
		views[i] = accountView{Name: n, Vendor: v.Name, User: a.User, Users: cfg.UsersOf(n),
			Hue: hueOf(v.ID), Meters: meters(q), Err: q.Error, NoSource: q.NoSource, Act: act[n]}
		if q.Balance != nil {
			amount := money(*q.Balance)
			views[i].Balance = &amountView{Name: "Balance", Note: q.Currency, Value: amount,
				Label: amount + " " + q.Currency}
		}
		views[i].NoQuota = views[i].Err == "" && !q.NoSource && len(views[i].Meters) == 0 && views[i].Balance == nil
		views[i].Pending = q.Provider == "" && q.Error == "" && q.Source == ""
	}
	type vendorOpt struct {
		ID, Name string
		Device   bool
	}
	var vendors []vendorOpt
	for _, v := range vendor.All() {
		vendors = append(vendors, vendorOpt{ID: v.ID, Name: v.Name, Device: deviceLogin(v) != nil})
	}
	// ?edit=<name> opens the sign-in form set to that account, so a new key
	// replaces the one stored.
	edit, editVendor := "", ""
	if e := c.r.URL.Query().Get("edit"); e != "" {
		if a, ok := cfg.Accounts[e]; ok {
			edit, editVendor = e, a.Vendor
		}
	}
	c.u.render(c.w, "accounts", map[string]any{"Accounts": views, "Vendors": vendors,
		"Edit": edit, "EditVendor": editVendor}, c)
}

// deviceLogin is a vendor's device sign-in, if it has one.
func deviceLogin(v vendor.Vendor) *vendor.Device {
	for _, l := range v.Login {
		if l.Method == vendor.LoginDevice && l.Device != nil {
			return l.Device
		}
	}
	return nil
}

func (c *call) addKey() {
	name, vid, key := field(c.r, "name"), field(c.r, "vendor"), field(c.r, "key")
	if name == "" {
		name = vid
	}
	if key == "" {
		c.back("accounts", "Paste the key.")
		return
	}
	err := c.change(func(cfg *config.Config) error {
		if _, ok := vendor.Lookup(vid); !ok {
			return fmt.Errorf("no vendor %q", vid)
		}
		return core.SignIn(cfg, name, vid, login.Credentials{Key: key})
	})
	c.u.b.Forget(name)
	c.back("accounts", done(err, "Signed in "+name+"."))
}

func (c *call) removeAccount() {
	name := field(c.r, "name")
	err := c.change(func(cfg *config.Config) error { return core.RemoveAccount(cfg, name) })
	c.back("accounts", done(err, "Removed "+name+"."))
}

// routeView is a route as the Routes page shows it.
type routeView struct {
	Name, Model, Strategy, Kind string
	Members                     []string
	Hue                         int
	Act                         activity
}

// routeForm is the add form's values, filled from a route when ?edit names
// one.
type routeForm struct {
	Name, Model, Members, Kind, Strategy string
	Editing                              bool
}

func (c *call) routes() {
	cfg := c.u.b.Config()
	act := activities(c.u.b.Usage(activityDays), func(r gateway.UsageRow) string { return r.Route })
	var views []routeView
	for _, n := range cfg.Service.RouteNames() {
		r := cfg.Service.Routes[n]
		v := routeView{Name: n, Model: r.Model, Members: r.Members, Strategy: r.Strategy, Kind: "alias", Hue: hueOf(n)}
		if r.IsPool() {
			v.Kind = "pool"
			if v.Strategy == "" {
				v.Strategy = config.StrategyOrder
			}
		} else {
			v.Strategy = ""
		}
		v.Act = act[n]
		views = append(views, v)
	}
	form := routeForm{Kind: "alias", Strategy: config.StrategyOrder}
	if e := c.r.URL.Query().Get("edit"); e != "" {
		if r, ok := cfg.Service.Routes[e]; ok {
			form = routeForm{Name: e, Model: r.Model, Members: strings.Join(r.Members, "\n"),
				Kind: "alias", Strategy: config.StrategyOrder, Editing: true}
			if r.IsPool() {
				form.Kind = "pool"
				form.Strategy = cmp.Or(r.Strategy, config.StrategyOrder)
			}
		}
	}
	c.u.render(c.w, "routes", map[string]any{"Routes": views, "Accounts": cfg.AccountNames(), "Form": form,
		"Strategies": []string{config.StrategyOrder, config.StrategyRotate, config.StrategyLeastUsed, config.StrategySmart}}, c)
}

func (c *call) saveRoute() {
	name := field(c.r, "name")
	var r config.Route
	if c.r.PostFormValue("type") == "pool" {
		r.Members = strings.FieldsFunc(c.r.PostFormValue("members"), func(c rune) bool {
			return c == ',' || c == '\n' || c == '\r' || c == ' '
		})
		if s := field(c.r, "strategy"); s != config.StrategyOrder {
			r.Strategy = s
		}
	} else {
		r.Model = field(c.r, "model")
	}
	err := c.change(func(cfg *config.Config) error {
		return core.ChangeService(cfg, func(s *config.Service) { s.Routes[name] = r })
	})
	c.back("routes", done(err, "Saved "+name+"."))
}

func (c *call) removeRoute() {
	name := field(c.r, "name")
	err := c.change(func(cfg *config.Config) error {
		if _, ok := cfg.Service.Routes[name]; !ok {
			return fmt.Errorf("no route %q", name)
		}
		return core.ChangeService(cfg, func(s *config.Service) { delete(s.Routes, name) })
	})
	c.back("routes", done(err, "Removed "+name+"."))
}

func (c *call) tokens() {
	cfg := c.u.b.Config()
	act := activities(c.u.b.Usage(activityDays), func(r gateway.UsageRow) string { return r.Token })
	type tokenView struct {
		Name, Created string
		Routes        []string
		Hue           int
		Act           activity
	}
	var views []tokenView
	for _, n := range cfg.Service.TokenNames() {
		t := cfg.Service.Tokens[n]
		created := ""
		if !t.Created.IsZero() {
			created = t.Created.Local().Format("2006-01-02 15:04")
		}
		views = append(views, tokenView{Name: n, Routes: t.Routes, Created: created,
			Hue: hueOf("token:" + n), Act: act[n]})
	}
	targets := append(cfg.Service.RouteNames(), cfg.AccountNames()...)
	// ?edit=<name> turns the issue form into one that changes a token's
	// routes; its hash, and so the token a machine holds, stays as it is.
	type tokenForm struct {
		Name    string
		Routes  []string
		Editing bool
	}
	var form tokenForm
	if e := c.r.URL.Query().Get("edit"); e != "" {
		if t, ok := cfg.Service.Tokens[e]; ok {
			form = tokenForm{Name: e, Routes: t.Routes, Editing: true}
		}
	}
	c.u.render(c.w, "tokens", map[string]any{"Tokens": views, "Targets": targets, "Form": form}, c)
}

func (c *call) addToken() {
	name := field(c.r, "name")
	c.r.ParseForm()
	routes := c.r.PostForm["routes"]
	var tok string
	err := c.change(func(cfg *config.Config) error {
		if _, ok := cfg.Service.Tokens[name]; ok {
			return fmt.Errorf("token %q exists; revoke it first", name)
		}
		t, hash, err := gateway.NewToken()
		if err != nil {
			return err
		}
		tok = t
		return core.ChangeService(cfg, func(s *config.Service) {
			s.Tokens[name] = config.Token{Hash: hash, Routes: routes, Created: c.u.now().UTC().Truncate(time.Second)}
		})
	})
	if err != nil {
		c.back("tokens", done(err, ""))
		return
	}
	c.u.sessions.tell(c.ss, "Issued "+name+". Copy the token now: it is not shown again.", tok)
	http.Redirect(c.w, c.r, "/ui/tokens", http.StatusSeeOther)
}

func (c *call) revokeToken() {
	name := field(c.r, "name")
	err := c.change(func(cfg *config.Config) error {
		if _, ok := cfg.Service.Tokens[name]; !ok {
			return fmt.Errorf("no token %q", name)
		}
		return core.ChangeService(cfg, func(s *config.Service) { delete(s.Tokens, name) })
	})
	c.back("tokens", done(err, "Revoked "+name+"."))
}

// saveToken changes what a token may ask, keeping the token itself: a
// machine's key does not have to be issued again.
func (c *call) saveToken() {
	c.r.ParseForm()
	name, routes := field(c.r, "name"), c.r.PostForm["routes"]
	err := c.change(func(cfg *config.Config) error {
		t, ok := cfg.Service.Tokens[name]
		if !ok {
			return fmt.Errorf("no token %q", name)
		}
		return core.ChangeService(cfg, func(s *config.Service) {
			t.Routes = routes
			s.Tokens[name] = t
		})
	})
	c.back("tokens", done(err, "Saved "+name+"."))
}

// usageGroup is one row of a usage table.
type usageGroup struct {
	Bar      int // how full the row's bar is, against the largest row
	Name     string
	Requests int64
	Failed   int64
	gateway.Tokens
}

func (c *call) usage() {
	days := 7
	if c.r.URL.Query().Get("days") == "30" {
		days = 30
	}
	rows := c.u.b.Usage(days)
	by := func(key func(gateway.UsageRow) string) []usageGroup {
		m := map[string]*usageGroup{}
		for _, r := range rows {
			k := key(r)
			if k == "" {
				k = "-"
			}
			g := m[k]
			if g == nil {
				g = &usageGroup{Name: k}
				m[k] = g
			}
			g.Requests += r.Requests
			g.Failed += r.Failed
			g.Input += r.Input
			g.Output += r.Output
			g.CacheRead += r.CacheRead
			g.CacheWrite += r.CacheWrite
		}
		out := make([]usageGroup, 0, len(m))
		for _, g := range m {
			out = append(out, *g)
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Requests != out[j].Requests {
				return out[i].Requests > out[j].Requests
			}
			return out[i].Name < out[j].Name
		})
		most := maxRequests(out)
		for i := range out {
			out[i].Bar = barWidth(out[i].Requests, most)
		}
		return out
	}
	totals := totalsOf(rows)
	c.u.render(c.w, "usage", map[string]any{
		"Days":    days,
		"Heat":    buildHeatYear(c.u.b.Usage(usageYear), c.u.now()),
		"Latency": latencyRows(c.u.b.Latencies()),
		"Tiles": []tileView{
			tile("Requests", totals.Requests, ""),
			tile("Failed", totals.Failed, ""),
			tile("Input", totals.Input, ""),
			tile("Output", totals.Output, ""),
			tile("Cache", totals.Cache, ""),
		},
		"Tables": []struct {
			Title  string
			Groups []usageGroup
		}{
			{"By token", by(func(r gateway.UsageRow) string { return r.Token })},
			{"By account", by(func(r gateway.UsageRow) string { return r.Account })},
			{"By route", by(func(r gateway.UsageRow) string { return r.Route })},
		},
	}, c)
}

// field is a trimmed form value.
func field(r *http.Request, name string) string { return strings.TrimSpace(r.PostFormValue(name)) }

// done is the message after a change: the error, or ok.
func done(err error, ok string) string {
	if err != nil {
		return "Not saved: " + err.Error()
	}
	return ok
}

var errNoDevice = errors.New("this vendor has no device sign-in")
