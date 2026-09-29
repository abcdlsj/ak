package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/gateway"
)

func TestSparkline(t *testing.T) {
	got := []rune(sparkline([]int64{0, 4, 8}, 8, 10))
	if len(got) != 3 {
		t.Fatalf("length = %d, want 3", len(got))
	}
	if got[0] != ' ' {
		t.Errorf("zero should be blank, got %q", got[0])
	}
	if got[2] != sparkGlyphs[len(sparkGlyphs)-1] {
		t.Errorf("peak should be the top glyph, got %q", got[2])
	}
	// Only the newest points survive a narrow width.
	got = []rune(sparkline([]int64{1, 2, 3, 4}, 4, 2))
	if len(got) != 2 {
		t.Fatalf("narrowed length = %d, want 2", len(got))
	}
}

func poolApp(stats map[string]gateway.PoolStats, err error) *app {
	cfg := config.Default()
	cfg.Providers["a"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://a", APIKey: "k"}
	cfg.Providers["b"] = config.Provider{Kind: config.KindClaude, BaseURL: "https://b", APIKey: "k"}
	cfg.Providers["pool"] = config.Provider{Kind: config.KindClaude, Members: []string{"a", "b"}}
	return &app{cfg: cfg, poolStats: stats, poolStatsErr: err}
}

func TestPoolFlowRendersMembers(t *testing.T) {
	a := poolApp(map[string]gateway.PoolStats{
		"pool": {Name: "pool", Members: []gateway.MemberStats{
			{Name: "a", OK: 5, Series: []int64{1, 2, 3}},
			{Name: "b", OK: 1, Fail: 2, Cooling: true, Series: []int64{0, 1, 0}},
		}},
	}, nil)

	out := poolFlow(a, "pool", 60)
	for _, want := range []string{"flow", "pool", "a", "b", "ok 5", "fail 2", "cooling", string(sparkGlyphs[len(sparkGlyphs)-1])} {
		if !strings.Contains(out, strings.TrimSpace(want)) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestPoolFlowGatewayDown(t *testing.T) {
	a := poolApp(nil, errors.New("connection refused"))
	out := poolFlow(a, "pool", 60)
	if !strings.Contains(out, "gateway not running") {
		t.Errorf("output = %q", out)
	}
}

func TestPoolFlowNoTraffic(t *testing.T) {
	a := poolApp(map[string]gateway.PoolStats{"pool": {Name: "pool"}}, nil)
	if out := poolFlow(a, "pool", 60); !strings.Contains(out, "no traffic yet") {
		t.Errorf("output = %q", out)
	}
}

func TestPoolFlowSkipsNarrowPanel(t *testing.T) {
	a := poolApp(nil, nil)
	if out := poolFlow(a, "pool", 10); out != "" {
		t.Errorf("narrow panel should render nothing, got %q", out)
	}
}
