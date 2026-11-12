package out

import (
	"bytes"
	"strings"
	"testing"
)

func TestReason(t *testing.T) {
	cases := map[string]string{
		`401 Unauthorized: {"success":false,"error":{"code":"UNAUTHORIZED","message":"Invalid token."}}`: "401 Invalid token.",
		`key rejected (HTTP 401): {"error":{"message":"Bad key","type":"authentication_error"}}`:          "401 Bad key",
		`HTTP 502: Proxy Error`: "HTTP 502: Proxy Error",
		"dial tcp:\n  refused":  "dial tcp: refused",
	}
	for in, want := range cases {
		if got := Reason(in); got != want {
			t.Errorf("Reason(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Reason(strings.Repeat("x", 200)); len([]rune(got)) != 80 {
		t.Errorf("long reason not cut: %d", len([]rune(got)))
	}
}

// Columns line up on screen width, and the last one is not padded.
func TestTable(t *testing.T) {
	var b bytes.Buffer
	Table(&b, []string{"name", "model"}, [][]string{{"a", "m1"}, {"longer", "模型"}})
	want := "NAME    MODEL\na       m1\nlonger  模型\n"
	if b.String() != want {
		t.Errorf("got\n%s\nwant\n%s", b.String(), want)
	}
}
