package secrets

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/abcdlsj/ak/internal/config"
)

// counting counts how often it was asked, to show the cache resolves once.
type counting struct {
	n atomic.Int64
}

func (c *counting) Resolve(config.Provider) (string, error) {
	c.n.Add(1)
	return "sk-once", nil
}

func TestCacheResolvesOnce(t *testing.T) {
	inner := &counting{}
	c := Cached(inner)
	p := config.Provider{APIKeyRef: "env:X"}
	for i := 0; i < 3; i++ {
		got, err := c.Resolve(p)
		if err != nil || got != "sk-once" {
			t.Fatalf("resolve = %q, %v", got, err)
		}
	}
	if inner.n.Load() != 1 {
		t.Errorf("inner resolver called %d times, want 1", inner.n.Load())
	}
}

// A cmd: reference that hangs fails instead of hanging the caller.
func TestCmdRefTimeout(t *testing.T) {
	old := cmdTimeout
	cmdTimeout = 50 * time.Millisecond
	defer func() { cmdTimeout = old }()
	if _, err := Default().Resolve(config.Provider{APIKeyRef: "cmd:sleep 30"}); err == nil {
		t.Fatal("a hanging cmd: reference resolved without error")
	}
}
