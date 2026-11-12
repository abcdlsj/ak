package quota

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/abcdlsj/ak/internal/config"
)

// program is a source that is an executable in quota.d, in any language: a
// shell script, a Go binary. Its file name, extension aside, is its id; it
// matches no host, so a vendor or provider names it with quota = "<id>".
//
// It is run with AK_QUOTA_KEY, AK_QUOTA_BASE_URL and AK_QUOTA_VAR_<NAME>
// (quota_vars and the account's secrets) set, and prints the quota as JSON,
// a Quota: {"windows":[{"name":"daily","used":42,"resets":"…"}],
// "balance":12.5,"currency":"USD"}, or a bare number taken as a balance.
// A non-zero exit fails the reading with what it wrote to stderr.
type program struct {
	id   string
	path string
}

func (pr program) ID() string        { return pr.id }
func (pr program) Match(string) bool { return false }

func (pr program) Fetch(ctx context.Context, p config.Provider, key string) (Quota, error) {
	return run(ctx, exec.CommandContext(ctx, pr.path), p, key)
}

// asProgram makes an executable regular file a program source.
func asProgram(dir string, e os.DirEntry) (program, bool) {
	info, err := e.Info()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return program{}, false
	}
	id := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
	return program{id: id, path: filepath.Join(dir, e.Name())}, true
}
