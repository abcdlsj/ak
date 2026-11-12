package cli

import (
	"testing"
	"time"

	"github.com/abcdlsj/ak/internal/quota"
)

func TestQuotaNumbersAreRounded(t *testing.T) {
	b, u, l := 15.986172332999999, 4.013827667, 20.0
	q := quota.Quota{Balance: &b, Used: &u, Limit: &l, Currency: "USD",
		Windows: []quota.Window{{Name: "5h", Used: 33.33333}, {Name: "weekly", Used: 50}}}
	if got := balanceText(q); got != "15.99 USD" {
		t.Errorf("balance = %q", got)
	}
	if got := usageText(q); got != usageTextWant(q) {
		t.Errorf("usage = %q", got)
	}
	for in, want := range map[float64]string{15: "15", 0.004: "0", -0.001: "0", 1.5: "1.5", 1234.567: "1234.57"} {
		if got := amount(in); got != want {
			t.Errorf("amount(%v) = %q, want %q", in, got, want)
		}
	}
}

// usageTextWant builds the expected string with the real separator, so the
// test does not depend on it.
func usageTextWant(q quota.Quota) string {
	return joinParts([]string{"4.01/20", "5h 33.3%", "weekly 50%"})
}

// A reset time today is a time; another day carries its date.
func TestResetText(t *testing.T) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 15, 4, 0, 0, time.Local)
	if got, want := resetText(today), "↻ 15:04"; got != want {
		t.Errorf("today = %q, want %q", got, want)
	}
	other := today.AddDate(0, 0, -3)
	if got, want := resetText(other), "↻ "+other.Format("01-02 15:04"); got != want {
		t.Errorf("another day = %q, want %q", got, want)
	}
}

// A plan is the balance and its windows, each with when it resets.
func TestPlanText(t *testing.T) {
	b, reset := 12.5, time.Now().Add(3*time.Hour)
	q := quota.Quota{Balance: &b, Currency: "USD",
		Windows: []quota.Window{{Name: "5h", Used: 12}, {Name: "weekly", Used: 40, Resets: &reset}}}
	want := joinParts([]string{"12.5 USD", "5h 12%", "weekly 40% " + resetText(reset)})
	if got := planText(q); got != want {
		t.Errorf("plan = %q, want %q", got, want)
	}
	if got := planText(quota.Quota{}); got != "" {
		t.Errorf("empty plan = %q", got)
	}
}

// A balance the source does not report is empty, so the table can show a dim
// dash rather than a zero.
func TestBalanceTextEmpty(t *testing.T) {
	if got := balanceText(quota.Quota{}); got != "" {
		t.Errorf("balance = %q, want empty", got)
	}
}
