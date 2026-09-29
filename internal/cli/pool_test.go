package cli

import "testing"

func TestParseMemberModels(t *testing.T) {
	m, err := parseMemberModels([]string{"a=m1", "b = m2"})
	if err != nil {
		t.Fatal(err)
	}
	if m["a"] != "m1" || m["b"] != "m2" {
		t.Errorf("parsed = %v", m)
	}
	if got, err := parseMemberModels(nil); err != nil || got != nil {
		t.Errorf("empty = %v, %v; want nil, nil", got, err)
	}
	for _, bad := range []string{"a", "=m", "a=", ""} {
		if _, err := parseMemberModels([]string{bad}); err == nil {
			t.Errorf("parseMemberModels(%q) accepted a malformed mapping", bad)
		}
	}
}

func TestParseMemberList(t *testing.T) {
	got := parseMemberList([]string{" a ", "", "b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("parseMemberList = %v", got)
	}
}
