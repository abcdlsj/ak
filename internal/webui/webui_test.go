package webui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/gateway"
	"github.com/abcdlsj/ak/internal/quota"
	"golang.org/x/crypto/bcrypt"
)

const password = "a-long-password"

// backend is the real service, with quota readings that ask no vendor.
type backend struct{ *gateway.Server }

func (backend) QuotasNow(accounts []string) []quota.Quota {
	out := make([]quota.Quota, len(accounts))
	for i, a := range accounts {
		out[i] = quota.Quota{Provider: a, Source: "test", Windows: []quota.Window{{Name: "5h", Used: 85}}}
	}
	return out
}

// RefreshQuotas is the background read a page starts; the stub is already
// read, so there is nothing to fetch.
func (backend) RefreshQuotas([]string) {}

type site struct {
	t   *testing.T
	srv *gateway.Server
	h   http.Handler
	jar map[string]*http.Cookie
}

func newSite(t *testing.T, withPassword bool) *site {
	t.Helper()
	t.Setenv(config.DirEnv, t.TempDir())
	t.Setenv("HOME", t.TempDir())
	cfg := config.Default()
	if withPassword {
		h, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
		cfg.Service.AdminPassword = string(h)
	}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	cfg, _ = config.Load()
	srv := gateway.New(cfg)
	srv.Logf = func(string, ...any) {}
	srv.UI = New(backend{srv})
	return &site{t: t, srv: srv, h: srv.ServiceHandler(), jar: map[string]*http.Cookie{}}
}

// do sends a request with the site's cookies and keeps those it sets.
func (s *site) do(method, path string, form url.Values, hdr ...string) *httptest.ResponseRecorder {
	s.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	// A browser names the origin on a form post; without it the UI refuses.
	if method == http.MethodPost && req.Header.Get("Origin") == "" {
		req.Header.Set("Origin", "http://"+req.Host)
	}
	for _, c := range s.jar {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(s.jar, c.Name)
		} else {
			s.jar[c.Name] = c
		}
	}
	return rec
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// csrf is the token on a page's forms.
func (s *site) csrf(path string) string {
	s.t.Helper()
	m := csrfRe.FindStringSubmatch(s.do("GET", path, nil).Body.String())
	if m == nil {
		s.t.Fatalf("%s has no form token", path)
	}
	return m[1]
}

func (s *site) login(pw string) *httptest.ResponseRecorder {
	s.t.Helper()
	return s.do("POST", "/ui/login", url.Values{"csrf": {s.csrf("/ui/login")}, "password": {pw}})
}

// post sends a form from page with its token.
func (s *site) post(page, action string, form url.Values) *httptest.ResponseRecorder {
	s.t.Helper()
	form.Set("csrf", s.csrf(page))
	return s.do("POST", action, form)
}

// The footer says what serves the page, and where it lives.
func TestFooter(t *testing.T) {
	s := newSite(t, true)
	s.login(password)
	page := s.do("GET", "/ui/accounts", nil).Body.String()
	for _, want := range []string{"serve by AK", "github.com/abcdlsj/ak", ">dev<"} {
		if !strings.Contains(page, want) {
			t.Errorf("the footer lacks %q", want)
		}
	}
}

func TestSetupWithoutPassword(t *testing.T) {
	s := newSite(t, false)
	if rec := s.do("GET", "/ui/accounts", nil); !strings.Contains(rec.Body.String(), "ak serve admin set-password") {
		t.Errorf("no setup page: %s", rec.Body)
	}
}

func TestLoginAndSession(t *testing.T) {
	s := newSite(t, true)
	if rec := s.do("GET", "/ui/accounts", nil); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/ui/login" {
		t.Fatalf("signed out: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := s.login("wrong"); rec.Code != http.StatusUnauthorized {
		t.Errorf("a wrong password: %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "/ui/login", nil)
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	if h := rec.Header(); !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'self'") ||
		!strings.Contains(h.Get("Content-Security-Policy"), "style-src 'self' 'unsafe-inline'") || h.Get("X-Frame-Options") != "DENY" {
		t.Errorf("security headers: %v", h)
	}

	rec = s.do("POST", "/ui/login", url.Values{"csrf": {s.csrf("/ui/login")}, "password": {password}}, "X-Forwarded-Proto", "https")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	c := s.jar[sessionCookie]
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || !c.Secure || c.Path != "/ui/" {
		t.Fatalf("session cookie = %+v", c)
	}
	if rec := s.do("GET", "/ui/accounts", nil); rec.Code != 200 {
		t.Fatalf("signed in: %d", rec.Code)
	}

	// The session never calls the model API.
	if rec := s.do("POST", "/v1/messages", url.Values{"model": {"x"}}); rec.Code != http.StatusUnauthorized {
		t.Errorf("the session cookie opened the API: %d", rec.Code)
	}

	// A form without the session's token, or from another site, is refused.
	if rec := s.do("POST", "/ui/tokens/add", url.Values{"name": {"x"}}); rec.Code != http.StatusForbidden {
		t.Errorf("no csrf: %d", rec.Code)
	}
	if rec := s.do("POST", "/ui/tokens/add", url.Values{"name": {"x"}, "csrf": {s.csrf("/ui/tokens")}}, "Origin", "https://evil.example"); rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin: %d", rec.Code)
	}

	s.post("/ui/accounts", "/ui/logout", url.Values{})
	if rec := s.do("GET", "/ui/accounts", nil); rec.Code != http.StatusSeeOther {
		t.Errorf("after logout: %d", rec.Code)
	}
}

func TestTokenNeverOpensUI(t *testing.T) {
	s := newSite(t, true)
	tok, hash, _ := gateway.NewToken()
	cfg := *s.srv.Config()
	cfg.Service.Tokens = map[string]config.Token{"laptop": {Hash: hash}}
	s.srv.Reload(&cfg)
	if rec := s.do("GET", "/ui/accounts", nil, "Authorization", "Bearer "+tok); rec.Code != http.StatusSeeOther {
		t.Errorf("a token opened the UI: %d", rec.Code)
	}
}

func TestLoginFormSurvivesASecondVisit(t *testing.T) {
	s := newSite(t, true)
	first := s.csrf("/ui/login")
	if again := s.csrf("/ui/login"); again != first {
		t.Fatalf("a second visit changed the form token")
	}
	if rec := s.do("POST", "/ui/login", url.Values{"csrf": {first}, "password": {password}}); rec.Code != http.StatusSeeOther {
		t.Errorf("the first form after a second visit: %d %s", rec.Code, rec.Body)
	}
}

func TestStaleLoginFormIsAnsweredWithAFreshOne(t *testing.T) {
	s := newSite(t, true)
	rec := s.do("POST", "/ui/login", url.Values{"csrf": {"stale"}, "password": {password}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a stale form: %d", rec.Code)
	}
	if !csrfRe.MatchString(rec.Body.String()) {
		t.Errorf("a stale form is a dead end, no form to try again: %s", rec.Body)
	}
}

// The icon is embedded and served, and the pages point the tab at it.
func TestIcon(t *testing.T) {
	s := newSite(t, true)
	rec := s.do("GET", "/ui/static/icon.svg", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "image/svg") {
		t.Fatalf("icon: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if page := s.do("GET", "/ui/login", nil).Body.String(); !strings.Contains(page, `rel="icon"`) {
		t.Error("the login page names no icon")
	}
}

// A form larger than a key could be is refused before it is read.
func TestFormBodyLimit(t *testing.T) {
	s := newSite(t, true)
	s.login(password)
	big := strings.Repeat("x", maxFormBytes+10)
	rec := s.do("POST", "/ui/accounts/key", url.Values{"csrf": {s.csrf("/ui/accounts")}, "vendor": {"command-code"}, "key": {big}})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a huge form: %d", rec.Code)
	}
}

// A form post names where it came from; one that does not is not from a page
// of ours, and is refused even before the password is looked at.
func TestPostWithoutAnOriginIsRefused(t *testing.T) {
	s := newSite(t, true)
	req := httptest.NewRequest("POST", "/ui/login", strings.NewReader("csrf=x&password="+password))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("a post with no Origin or Referer: %d", rec.Code)
	}
	if rec := s.do("GET", "/ui/login", nil); rec.Code != http.StatusOK {
		t.Errorf("a plain GET: %d", rec.Code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	s := newSite(t, true)
	for range maxFailures {
		s.login("wrong")
	}
	if rec := s.login(password); rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("after %d failures the right password: %d", maxFailures, rec.Code)
	}
}

// A token's routes are edited in place: the machine keeps the token it was
// given, and only what it may ask changes.
func TestTokenEditKeepsTheToken(t *testing.T) {
	s := newSite(t, true)
	s.login(password)
	s.post("/ui/accounts", "/ui/accounts/key", url.Values{"vendor": {"command-code"}, "key": {"sk-secret"}})
	s.post("/ui/accounts", "/ui/accounts/key", url.Values{"vendor": {"opendesign"}, "key": {"sk-secret-2"}})
	s.post("/ui/tokens", "/ui/tokens/add", url.Values{"name": {"laptop"}, "routes": {"command-code"}})
	shown := regexp.MustCompile(`ak_[A-Za-z0-9_-]{43}`).FindString(s.do("GET", "/ui/tokens", nil).Body.String())
	if shown == "" {
		t.Fatal("no token was issued")
	}
	before := s.srv.Config().Service.Tokens["laptop"]

	if page := s.do("GET", "/ui/tokens?edit=laptop", nil).Body.String(); !strings.Contains(page, `value="command-code" checked`) {
		t.Errorf("the edit form does not carry the token's route:\n%s", page)
	}
	rec := s.do("POST", "/ui/tokens/save", url.Values{
		"csrf": {s.csrf("/ui/tokens")}, "name": {"laptop"}, "routes": {"opendesign"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	after := s.srv.Config().Service.Tokens["laptop"]
	if after.Hash != before.Hash {
		t.Errorf("the token was reissued: %q then %q", before.Hash, after.Hash)
	}
	if len(after.Routes) != 1 || after.Routes[0] != "opendesign" {
		t.Errorf("routes = %v, want [opendesign]", after.Routes)
	}
}

// Opening a card brings up its form filled in: a route's name is fixed, and
// signing in again as an account replaces that account's key.
func TestEditFormsAreFilledIn(t *testing.T) {
	s := newSite(t, true)
	s.login(password)
	s.post("/ui/accounts", "/ui/accounts/key", url.Values{"vendor": {"command-code"}, "key": {"sk-secret"}})
	s.post("/ui/routes", "/ui/routes/save", url.Values{
		"name": {"main"}, "type": {"pool"}, "members": {"command-code\ncommand-code/deepseek-v4.4"}, "strategy": {"smart"},
	})
	page := s.do("GET", "/ui/routes?edit=main", nil).Body.String()
	for _, want := range []string{`<details class="add" open>`, `value="main"`, `readonly`, `selected>smart<`} {
		if !strings.Contains(page, want) {
			t.Errorf("the route form lacks %q:\n%s", want, page)
		}
	}

	page = s.do("GET", "/ui/accounts?edit=command-code", nil).Body.String()
	for _, want := range []string{`<details class="add" open>`, `value="command-code" selected`, `Sign in again as command-code`, `>Replace the key<`} {
		if !strings.Contains(page, want) {
			t.Errorf("the account form lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "sk-secret") {
		t.Error("the account's key is on the page")
	}
}

func TestPages(t *testing.T) {
	s := newSite(t, true)
	s.login(password)

	// Accounts: paste a key; it is saved, reloaded and shown with its quota.
	rec := s.post("/ui/accounts", "/ui/accounts/key", url.Values{"vendor": {"command-code"}, "key": {"sk-secret"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("add key: %d", rec.Code)
	}
	if a := s.srv.Config().Accounts["command-code"]; a.APIKey != "sk-secret" {
		t.Fatalf("the service was not reloaded with the account: %+v", a)
	}
	page := s.do("GET", "/ui/accounts", nil).Body.String()
	if !strings.Contains(page, "command-code") || !strings.Contains(page, `data-pct="85"`) ||
		!strings.Contains(page, `class="pix"`) || !strings.Contains(page, "--fill:85%") || strings.Contains(page, "sk-secret") {
		t.Errorf("accounts page:\n%s", page)
	}

	// Routes: a pool, then a bad one refused with the reason.
	s.post("/ui/routes", "/ui/routes/save", url.Values{"name": {"main"}, "type": {"pool"}, "members": {"command-code\ncommand-code/deepseek-v4"}, "strategy": {"smart"}})
	r := s.srv.Config().Service.Routes["main"]
	if len(r.Members) != 2 || r.Strategy != "smart" {
		t.Fatalf("route = %+v", r)
	}
	s.post("/ui/routes", "/ui/routes/save", url.Values{"name": {"bad"}, "type": {"alias"}, "model": {"nobody/x"}})
	if page := s.do("GET", "/ui/routes", nil).Body.String(); !strings.Contains(page, "Not saved") {
		t.Errorf("a bad route did not say why:\n%s", page)
	}

	// Tokens: issued once, kept as its hash, limited, revoked.
	s.post("/ui/tokens", "/ui/tokens/add", url.Values{"name": {"laptop"}, "routes": {"main"}})
	page = s.do("GET", "/ui/tokens", nil).Body.String()
	m := regexp.MustCompile(`ak_[A-Za-z0-9_-]{43}`).FindString(page)
	tok := s.srv.Config().Service.Tokens["laptop"]
	if m == "" || tok.Hash != gateway.HashToken(m) || len(tok.Routes) != 1 {
		t.Fatalf("token shown %q, kept %+v", m, tok)
	}
	if again := s.do("GET", "/ui/tokens", nil).Body.String(); strings.Contains(again, m) {
		t.Error("the token was shown twice")
	}
	s.post("/ui/tokens", "/ui/tokens/revoke", url.Values{"name": {"laptop"}})
	if len(s.srv.Config().Service.Tokens) != 0 {
		t.Error("revoke left the token")
	}

	// Removing an account a route uses is refused; the route first.
	s.post("/ui/accounts", "/ui/accounts/remove", url.Values{"name": {"command-code"}})
	if _, ok := s.srv.Config().Accounts["command-code"]; !ok {
		t.Error("an account a route uses was removed")
	}
	s.post("/ui/routes", "/ui/routes/remove", url.Values{"name": {"main"}})
	s.post("/ui/accounts", "/ui/accounts/remove", url.Values{"name": {"command-code"}})
	if _, ok := s.srv.Config().Accounts["command-code"]; ok {
		t.Error("the account was not removed")
	}

	for _, p := range []string{"/ui/usage", "/ui/usage?days=30"} {
		if rec := s.do("GET", p, nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "By account") {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	if rec := s.do("GET", "/ui/static/style.css", nil); rec.Code != 200 {
		t.Errorf("static: %d", rec.Code)
	}
}

func TestNumber(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1 000", 1234567: "1 234 567"} {
		if got := number(n); got != want {
			t.Errorf("number(%d) = %q", n, got)
		}
	}
}
