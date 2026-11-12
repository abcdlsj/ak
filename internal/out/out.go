// Package out is how ak's commands print to a terminal, so every command
// reads the same way and the same way as the TUI.
//
// The rules:
//
//   - A listing is a Table: UPPERCASE dim headers, columns two spaces apart,
//     a missing value a dim "-". Columns are measured on screen, so colour
//     and wide characters line up. On a terminal the last column is cut to
//     the width left; piped output is never cut.
//   - A row's state is a Mark at its start: ✓ fine, ! worth a look, ✗ broken,
//     · not known or not applicable. The word, if any, follows in the cell.
//   - A vendor's error is shown by Reason: its own message, short, never a
//     JSON body. The full text stays in --json.
//   - What to do next is a Hint under the output: dim, one line each, a
//     command in backticks. A count or legend is a Hint too.
//   - A change is one Done line, "✓ added og (ak-og)"; details that only
//     matter when something went wrong are left to the command's -v or its
//     failure.
//   - Nothing is printed for an empty listing but what to do: Empty.
//
// Colour follows the terminal: none when stdout is not one or NO_COLOR is
// set (lipgloss decides). --json output never goes through here.
package out

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// The palette, the TUI's.
var (
	headStyle = lipgloss.NewStyle().Faint(true).Bold(true)
	dimStyle  = lipgloss.NewStyle().Faint(true)
	okStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	errStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	boldStyle = lipgloss.NewStyle().Bold(true)
)

// Dim, Bold, OK, Warn and Err style a piece of text.
func Dim(s string) string  { return dimStyle.Render(s) }
func Bold(s string) string { return boldStyle.Render(s) }
func OK(s string) string   { return okStyle.Render(s) }
func Warn(s string) string { return warnStyle.Render(s) }
func Err(s string) string  { return errStyle.Render(s) }

// Or is s, or a dim "-" when s is empty.
func Or(s string) string {
	if s == "" {
		return Dim("-")
	}
	return s
}

// Level is a row's state.
type Level int

const (
	Unknown Level = iota
	Fine
	Notice
	Broken
)

// Mark is a level's one-cell mark, coloured.
func Mark(l Level) string {
	switch l {
	case Fine:
		return OK("✓")
	case Notice:
		return Warn("!")
	case Broken:
		return Err("✗")
	}
	return Dim("·")
}

// Table writes rows under head, aligned. A cell may be styled already.
func Table(w io.Writer, head []string, rows [][]string) {
	widths := make([]int, len(head))
	for i, h := range head {
		widths[i] = len(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if i < len(widths) {
				widths[i] = max(widths[i], ansi.StringWidth(c))
			}
		}
	}
	limit := width(w)
	line := func(cells []string, style func(string) string) {
		var b strings.Builder
		for i, c := range cells {
			if i == len(cells)-1 {
				if limit > 0 {
					c = ansi.Truncate(c, max(limit-ansi.StringWidth(b.String()), 8), "…")
				}
				b.WriteString(style(c))
				break
			}
			b.WriteString(style(c))
			b.WriteString(strings.Repeat(" ", widths[i]-ansi.StringWidth(c)+2))
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
	upper := make([]string, len(head))
	for i, h := range head {
		upper[i] = strings.ToUpper(h)
	}
	line(upper, func(s string) string { return headStyle.Render(s) })
	for _, r := range rows {
		line(r, func(s string) string { return s })
	}
}

// Hint writes a dim line under the output.
func Hint(w io.Writer, format string, a ...any) {
	fmt.Fprintln(w, Dim(fmt.Sprintf(format, a...)))
}

// Done writes a change that went through.
func Done(w io.Writer, format string, a ...any) {
	fmt.Fprintln(w, OK("✓")+" "+fmt.Sprintf(format, a...))
}

// Empty says there is nothing to list, and what to do about it.
func Empty(w io.Writer, what, todo string) {
	fmt.Fprintln(w, what)
	if todo != "" {
		Hint(w, "%s", todo)
	}
}

// width is the terminal's width when w is stdout on a terminal, else 0.
func width(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok || f != os.Stdout || !term.IsTerminal(f.Fd()) {
		return 0
	}
	cols, _, err := term.GetSize(f.Fd())
	if err != nil {
		return 0
	}
	return cols
}

var statusRe = regexp.MustCompile(`\b[1-5]\d\d\b`)

// Reason makes an error a short line: for a JSON body, the HTTP status
// before it and the vendor's own message out of it; at most 80 characters.
func Reason(s string) string {
	if i := strings.Index(s, "{"); i >= 0 {
		var body any
		if json.Unmarshal([]byte(s[i:]), &body) == nil {
			if msg := message(body); msg != "" {
				s = strings.TrimSpace(statusRe.FindString(s[:i]) + " " + msg)
			}
		}
	}
	return ansi.Truncate(strings.Join(strings.Fields(s), " "), 80, "…")
}

// message finds a vendor's error message in a decoded JSON body.
func message(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	for _, k := range []string{"error", "message", "msg", "detail", "title"} {
		switch t := m[k].(type) {
		case string:
			if t != "" {
				return t
			}
		case map[string]any:
			if s := message(t); s != "" {
				return s
			}
		}
	}
	return ""
}
