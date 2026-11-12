// Package version is what this build says it is: the tag a release was built
// from, the module version `go install ...@vX` records, or "dev".
package version

import (
	"regexp"
	"runtime/debug"
	"strings"
)

// Version is set at build time, so a binary says what it is:
//
//	go build -ldflags "-X github.com/abcdlsj/ak/internal/version.Version=v1.2.3"
var Version = "dev"

// release matches a tagged version, not the pseudo-version a build from a
// checkout carries, which says nothing a reader wants.
var release = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)

func init() {
	if Version != "dev" {
		return
	}
	if bi, ok := debug.ReadBuildInfo(); ok && release.MatchString(bi.Main.Version) {
		Version = bi.Main.Version
	}
}

// Display is the version as a page shows it: one leading v, never two.
func Display() string {
	if Version == "" || Version == "dev" {
		return "dev"
	}
	return "v" + strings.TrimPrefix(Version, "v")
}
