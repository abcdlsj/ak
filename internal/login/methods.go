package login

import (
	"os/exec"
	"runtime"

	"github.com/abcdlsj/ak/internal/vendor"
)

// Methods lists the vendor's ways to sign in that can run here, best first:
// a file login only when its file exists, pasting a key always, last.
func Methods(v vendor.Vendor) []vendor.Login {
	var out []vendor.Login
	for _, l := range v.Logins() {
		if l.Method == vendor.LoginFile && FilePath(l) == "" {
			continue
		}
		out = append(out, l)
	}
	return out
}

// Describe says what a method does, for a person choosing one.
func Describe(l vendor.Login) string {
	switch l.Method {
	case vendor.LoginFile:
		return "use the sign-in in " + tilde(FilePath(l))
	case vendor.LoginDevice:
		return "approve a code in the browser"
	}
	return "paste a key"
}

// OpenBrowser opens url in the user's browser; failing is fine, the URL is
// shown too.
func OpenBrowser(url string) {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	if cmd := exec.Command(name, url); cmd.Start() == nil {
		go cmd.Wait()
	}
}
