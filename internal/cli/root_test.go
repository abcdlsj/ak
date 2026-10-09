package cli

import (
	"testing"

	"github.com/abcdlsj/ak/internal/config"
)

// Every subcommand and alias is reserved, so no provider can shadow one.
func TestCommandsReserved(t *testing.T) {
	for _, c := range newRoot().Commands() {
		for _, n := range append([]string{c.Name()}, c.Aliases...) {
			if err := config.ValidateName(n); err == nil && n[0] != '_' {
				t.Errorf("%q is a command but a valid provider name", n)
			}
		}
	}
}
