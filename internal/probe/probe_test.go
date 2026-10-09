package probe

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

const testKey = "sk-secret-123"

// seen is what a test server received.
type seen struct {
	path, auth, apiKey, version string
	body                        map[string]any
}

// serve answers every request with code and reply after delay, recording the
// last request.
func serve(t *testing.T, code int, reply string, delay time.Duration) (*httptest.Server, *seen) {
	t.Helper()
	got := &seen{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.auth = r.Header.Get("Authorization")
		got.apiKey = r.Header.Get("X-Api-Key")
		got.version = r.Header.Get("anthropic-version")
		data, _ := io.ReadAll(r.Body)
		got.body = nil
		json.Unmarshal(data, &got.body)
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		w.WriteHeader(code)
		w.Write([]byte(reply))
	}))
	t.Cleanup(s.Close)
	return s, got
}

func TestCheckRequestShapes(t *testing.T) {
	tests := []struct {
		name                 string
		p                    config.Provider
		path, auth, apiKey   string
		anthropic            bool
		wantField, wantValue string
	}{
		{"claude bearer", config.Provider{Kind: config.KindClaude, Model: "m"},
			"/v1/messages", "Bearer " + testKey, "", true, "max_tokens", "1"},
		{"claude api_key", config.Provider{Kind: config.KindClaude, Sonnet: "m", KeyField: "api_key"},
			"/v1/messages", "", testKey, true, "max_tokens", "1"},
		{"codex responses", config.Provider{Kind: config.KindCodex, Model: "m"},
			"/v1/responses", "Bearer " + testKey, "", false, "input", "hi"},
		{"codex chat", config.Provider{Kind: config.KindCodex, Model: "m", WireAPI: "chat"},
			"/v1/chat/completions", "Bearer " + testKey, "", false, "max_tokens", "1"},
		{"pi anthropic", config.Provider{Kind: config.KindPi, Model: "m"},
			"/v1/messages", "", testKey, true, "max_tokens", "1"},
		{"pi anthropic bearer", config.Provider{Kind: config.KindPi, Model: "m", PiAuthHeader: true},
			"/v1/messages", "Bearer " + testKey, "", true, "max_tokens", "1"},
		{"pi completions", config.Provider{Kind: config.KindPi, Model: "m", PiAPI: "openai-completions"},
			"/v1/chat/completions", "Bearer " + testKey, "", false, "max_tokens", "1"},
		{"pi responses", config.Provider{Kind: config.KindPi, Model: "m", PiAPI: "openai-responses"},
			"/v1/responses", "Bearer " + testKey, "", false, "max_output_tokens", "16"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, got := serve(t, 200, `{}`, 0)
			tt.p.BaseURL = s.URL
			if tt.p.Kind != config.KindClaude && tt.p.Kind != config.KindPi || tt.p.PiAPI != "" {
				tt.p.BaseURL = s.URL + "/v1" // openai-style bases carry /v1
			}
			r := Check(context.Background(), tt.p, testKey)
			if r.Status != StatusOK {
				t.Fatalf("status = %s (%s)", r.Status, r.Detail)
			}
			if got.path != tt.path || got.auth != tt.auth || got.apiKey != tt.apiKey {
				t.Fatalf("path %q auth %q x-api-key %q", got.path, got.auth, got.apiKey)
			}
			if (got.version != "") != tt.anthropic {
				t.Fatalf("anthropic-version = %q", got.version)
			}
			if got.body["model"] != "m" {
				t.Fatalf("model = %v", got.body["model"])
			}
			v, _ := json.Marshal(got.body[tt.wantField])
			if strings.Trim(string(v), `"`) != tt.wantValue {
				t.Fatalf("%s = %s", tt.wantField, v)
			}
		})
	}
}

func TestCheckClassify(t *testing.T) {
	tests := []struct {
		name  string
		code  int
		reply string
		want  Status
	}{
		{"ok", 200, `{"id":"x"}`, StatusOK},
		{"unauthorized", 401, `{"error":"bad key ` + testKey + `"}`, StatusAuth},
		{"forbidden", 403, `forbidden`, StatusAuth},
		{"model 404", 404, `{"error":{"message":"The model m does not exist"}}`, StatusModel},
		{"model 400", 400, `{"error":{"code":"model_not_found"}}`, StatusModel},
		{"plain 404", 404, `404 page not found`, StatusFail},
		{"server", 500, `upstream exploded`, StatusFail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := serve(t, tt.code, tt.reply, 0)
			r := Check(context.Background(), config.Provider{Kind: config.KindClaude, BaseURL: s.URL, Model: "m"}, testKey)
			if r.Status != tt.want {
				t.Fatalf("status = %s, want %s (%s)", r.Status, tt.want, r.Detail)
			}
			if r.Code != tt.code {
				t.Fatalf("code = %d", r.Code)
			}
			if strings.Contains(r.Detail, testKey) {
				t.Fatalf("detail leaks the key: %s", r.Detail)
			}
		})
	}
}

func TestCheckTimeoutAndSlow(t *testing.T) {
	oldT, oldS := Timeout, SlowAfter
	t.Cleanup(func() { Timeout, SlowAfter = oldT, oldS })

	Timeout, SlowAfter = 50*time.Millisecond, time.Second
	s, _ := serve(t, 200, `{}`, 500*time.Millisecond)
	p := config.Provider{Kind: config.KindClaude, BaseURL: s.URL, Model: "m"}
	if r := Check(context.Background(), p, testKey); r.Status != StatusFail || !strings.Contains(r.Detail, "timed out") {
		t.Fatalf("timeout: %+v", r)
	}

	Timeout, SlowAfter = time.Second, 10*time.Millisecond
	s, _ = serve(t, 200, `{}`, 30*time.Millisecond)
	p.BaseURL = s.URL
	if r := Check(context.Background(), p, testKey); r.Status != StatusSlow || r.LatencyMS < 30 {
		t.Fatalf("slow: %+v", r)
	}
}

func TestCheckSkips(t *testing.T) {
	if r := Check(context.Background(), config.Provider{Kind: config.KindPi, PiProvider: "kimi"}, ""); r.Status != StatusSkip || r.Detail != "skipped (pi's own provider)" {
		t.Fatalf("pi own: %+v", r)
	}
	if r := Check(context.Background(), config.Provider{Kind: config.KindClaude, BaseURL: "http://x"}, ""); r.Status != StatusSkip || r.Detail != "no model to test" {
		t.Fatalf("no model: %+v", r)
	}
}

func TestAggregate(t *testing.T) {
	members := []Result{
		{Provider: "a", Status: StatusFail},
		{Provider: "b", Status: StatusSlow, LatencyMS: 7000},
		{Provider: "c", Status: StatusOK, LatencyMS: 900},
		{Provider: "d", Status: StatusOK, LatencyMS: 300},
	}
	r := Aggregate("pool", members)
	if r.Status != StatusOK || r.LatencyMS != 300 || r.Detail != "3/4 members ok" {
		t.Fatalf("aggregate = %+v", r)
	}
	r = Aggregate("pool", []Result{{Provider: "a", Status: StatusAuth}, {Provider: "b", Status: StatusFail}})
	if r.Status != StatusFail || r.Detail != "0/2 members ok" {
		t.Fatalf("all failing = %+v", r)
	}
}

func TestModelsURLs(t *testing.T) {
	tests := []struct {
		base string
		want []string
	}{
		{"https://x.example/v1/", []string{"https://x.example/v1/models"}},
		{"https://x.example/api/coding/paas/v4", []string{
			"https://x.example/api/coding/paas/v4/models", "https://x.example/api/coding/paas/v4/v1/models"}},
		{"https://x.example", []string{"https://x.example/v1/models", "https://x.example/models"}},
		{"https://x.example/api/anthropic", []string{
			"https://x.example/api/anthropic/v1/models", "https://x.example/api/anthropic/models",
			"https://x.example/v1/models", "https://x.example/models"}},
		{"https://x.example/step_plan", []string{
			"https://x.example/step_plan/v1/models", "https://x.example/step_plan/models",
			"https://x.example/v1/models", "https://x.example/models"}},
		{"", nil},
	}
	for _, tt := range tests {
		if got := ModelsURLs(tt.base); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ModelsURLs(%q) = %q, want %q", tt.base, got, tt.want)
		}
	}
}

// serveModels answers the model list at path only, recording every path asked.
func serveModels(t *testing.T, path, reply string) (*httptest.Server, *[]string, *http.Header) {
	t.Helper()
	var paths []string
	var hdr http.Header
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != path {
			http.Error(w, "nope "+testKey, http.StatusNotFound)
			return
		}
		hdr = r.Header.Clone()
		w.Write([]byte(reply))
	}))
	t.Cleanup(s.Close)
	return s, &paths, &hdr
}

func TestModelsFallsBackToRoot(t *testing.T) {
	s, paths, hdr := serveModels(t, "/v1/models",
		`{"data":[{"id":"claude-b","display_name":"B"},{"id":"claude-a"},{"id":"claude-b"}]}`)
	p := config.Provider{Kind: config.KindClaude, BaseURL: s.URL + "/anthropic", KeyField: "api_key"}
	ids, err := Models(context.Background(), p, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{"claude-a", "claude-b"}) {
		t.Fatalf("ids = %q", ids)
	}
	if want := []string{"/anthropic/v1/models", "/anthropic/models", "/v1/models"}; !reflect.DeepEqual(*paths, want) {
		t.Fatalf("paths = %q, want %q", *paths, want)
	}
	if hdr.Get("X-Api-Key") != testKey || hdr.Get("anthropic-version") == "" {
		t.Fatalf("headers = %v", *hdr)
	}
}

func TestModelsListShape(t *testing.T) {
	s, _, hdr := serveModels(t, "/v1/models",
		`{"models":["m2",{"id":"m1"},{"name":"models/gemini-x"}]}`)
	ids, err := Models(context.Background(), config.Provider{Kind: config.KindCodex, BaseURL: s.URL + "/v1"}, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{"gemini-x", "m1", "m2"}) {
		t.Fatalf("ids = %q", ids)
	}
	if hdr.Get("Authorization") != "Bearer "+testKey || hdr.Get("anthropic-version") != "" {
		t.Fatalf("headers = %v", *hdr)
	}
}

func TestModelsErrorNamesEveryURL(t *testing.T) {
	s, _, _ := serveModels(t, "/never", `{}`)
	_, err := Models(context.Background(), config.Provider{Kind: config.KindClaude, BaseURL: s.URL + "/claude"}, testKey)
	if err == nil {
		t.Fatal("want an error")
	}
	for _, u := range ModelsURLs(s.URL + "/claude") {
		if !strings.Contains(err.Error(), u) {
			t.Errorf("error does not name %s:\n%s", u, err)
		}
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatalf("error leaks the key:\n%s", err)
	}
}

// An Anthropic base that already ends in a version still gets /v1/messages,
// as claude itself would send.
func TestAnthropicCheckPathIgnoresVersionedBase(t *testing.T) {
	url, _ := checkRequest("https://x.example/api/v4", wireAnthropic, "m")
	if url != "https://x.example/api/v4/v1/messages" {
		t.Errorf("url = %s", url)
	}
}
