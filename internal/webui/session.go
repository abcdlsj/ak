package webui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Session and login bounds.
const (
	sessionCookie = "ak_session"
	loginCookie   = "ak_login" // the login form's CSRF token, before a session
	sessionTTL    = 12 * time.Hour
	// maxFailures failed logins within failWindow lock the login for the
	// rest of that window. The count is global: behind a reverse proxy every
	// client has the proxy's address.
	maxFailures = 5
	failWindow  = 5 * time.Minute
)

// session is one signed-in browser.
type session struct {
	csrf    string
	expires time.Time
	flash   string // a message for the next page, shown once
	token   string // a token just issued, shown once
	remote  string // the command that puts that token on a machine, shown once
}

// sessions holds the signed-in browsers in memory: a restart signs out.
type sessions struct {
	mu    sync.Mutex
	m     map[string]*session
	fails []time.Time
}

func newSessions() *sessions { return &sessions{m: map[string]*session{}} }

func randomID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on supported systems
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// start makes a session and returns its id.
func (s *sessions) start() string {
	id := randomID()
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, v := range s.m {
		if now.After(v.expires) {
			delete(s.m, k)
		}
	}
	s.m[id] = &session{csrf: randomID(), expires: now.Add(sessionTTL)}
	return id
}

// get is the request's session, if it has a live one.
func (s *sessions) get(r *http.Request) (string, *session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return "", nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.m[c.Value]
	if !ok || time.Now().After(ss.expires) {
		delete(s.m, c.Value)
		return "", nil, false
	}
	return c.Value, ss, true
}

func (s *sessions) end(id string) {
	s.mu.Lock()
	delete(s.m, id)
	s.mu.Unlock()
}

// locked reports whether too many logins failed lately, and until when.
func (s *sessions) locked(now time.Time) (bool, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.fails[:0]
	for _, t := range s.fails {
		if now.Sub(t) < failWindow {
			kept = append(kept, t)
		}
	}
	s.fails = kept
	if len(s.fails) >= maxFailures {
		return true, s.fails[0].Add(failWindow)
	}
	return false, time.Time{}
}

func (s *sessions) failed(now time.Time) {
	s.mu.Lock()
	s.fails = append(s.fails, now)
	s.mu.Unlock()
}

// take reads and clears a session's one-time fields.
func (s *sessions) take(ss *session) (flash, token, remote string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	flash, token, remote = ss.flash, ss.token, ss.remote
	ss.flash, ss.token, ss.remote = "", "", ""
	return flash, token, remote
}

func (s *sessions) tell(ss *session, flash, token, remote string) {
	s.mu.Lock()
	ss.flash, ss.token, ss.remote = flash, token, remote
	s.mu.Unlock()
}

// secure reports whether the browser reached us over TLS, directly or
// through the reverse proxy.
func secure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func setCookie(w http.ResponseWriter, r *http.Request, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/ui/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secure(r),
	})
}

// sameOrigin refuses a request whose Origin or Referer names another host,
// and one that names neither: a browser sends one of them with every form,
// so a post without them is not a page of ours.
func sameOrigin(r *http.Request) bool {
	src := r.Header.Get("Origin")
	if src == "" {
		src = r.Header.Get("Referer")
	}
	if src == "" || src == "null" {
		return false
	}
	u, err := url.Parse(src)
	if err != nil {
		return false
	}
	host := r.Host
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
		host = fh
	}
	return strings.EqualFold(u.Host, host)
}

func equal(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
