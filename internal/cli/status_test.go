package cli

import (
	"testing"

	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/out"
	"github.com/abcdlsj/ak/internal/quota"
)

// A healthy provider says ok; a broken one names what is wrong.
func TestHealthText(t *testing.T) {
	if got, want := healthText(core.Status{}), out.Mark(out.Fine)+" ok"; got != want {
		t.Errorf("ok = %q, want %q", got, want)
	}
	got := healthText(core.Status{NoKey: true})
	if want := out.Mark(out.Broken) + " no API key"; got != want {
		t.Errorf("no key = %q, want %q", got, want)
	}
}

// A plan is a dim dash when no source serves it, and says why when the read
// failed.
func TestPlanCell(t *testing.T) {
	if got, want := planCell(quota.Quota{NoSource: true}), out.Or(""); got != want {
		t.Errorf("no source = %q, want %q", got, want)
	}
	if got, want := planCell(quota.Quota{Provider: "a"}), out.Or(""); got != want {
		t.Errorf("no reading = %q, want %q", got, want)
	}
	const body = `HTTP 401: {"error":{"message":"Bad key"}}`
	if got, want := planCell(quota.Quota{Error: body}), out.Err("✗ ")+out.Reason(body); got != want {
		t.Errorf("failed read = %q, want %q", got, want)
	}
}
