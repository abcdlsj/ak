package gateway

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/vendor"
)

// slowThinker is how long the fake vendor thinks between two events.
const slowThinker = 400 * time.Millisecond

// A stream reaches the agent as the vendor produces it: the first event is
// readable while the vendor is still thinking about the second, and the
// response says a proxy in front of it must not buffer.
func TestServiceStreamsAsTheyArrive(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		io.WriteString(w, sse(`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"early"}}`))
		f.Flush()
		time.Sleep(slowThinker)
		io.WriteString(w, sse(`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"late"}}`))
		f.Flush()
	}))
	t.Cleanup(up.Close)

	dir := t.TempDir()
	write := fmt.Sprintf("id = \"slow\"\n[engines.claude]\nbase_url = %q\nmodel = \"m\"\n", up.URL)
	if err := os.WriteFile(filepath.Join(dir, "slow.toml"), []byte(write), 0o600); err != nil {
		t.Fatal(err)
	}
	vendor.LoadDir(dir)
	t.Cleanup(func() { vendor.LoadDir(t.TempDir()) })

	tok, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Accounts["slow"] = config.Account{Vendor: "slow", APIKey: "key"}
	cfg.Service.Tokens = map[string]config.Token{"laptop": {Hash: hash}}
	cfg.Resolve()
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	srv := New(cfg)
	srv.Logf = func(string, ...any) {}

	live := httptest.NewServer(srv.ServiceHandler())
	t.Cleanup(live.Close)

	req, err := http.NewRequest(http.MethodPost, live.URL+"/v1/messages",
		strings.NewReader(`{"model":"slow","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering = %q, want no", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}

	// The first event must arrive before the vendor has thought its second.
	br := bufio.NewReader(resp.Body)
	start := time.Now()
	var first strings.Builder
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("reading the first event: %v", err)
		}
		first.WriteString(line)
		if line == "\n" {
			break
		}
	}
	if d := time.Since(start); d > slowThinker/2 {
		t.Errorf("the first event took %s, want well under %s", d, slowThinker)
	}
	if !strings.Contains(first.String(), "early") {
		t.Errorf("the first event is %q", first.String())
	}

	rest, err := io.ReadAll(br)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rest), "late") {
		t.Errorf("the rest of the stream is %q", rest)
	}
}
