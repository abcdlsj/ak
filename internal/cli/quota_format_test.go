package cli

import (
	"testing"

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
