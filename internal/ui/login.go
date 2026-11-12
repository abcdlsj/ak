package ui

import (
	"context"
	"strings"

	"github.com/abcdlsj/ak/internal/core"
	"github.com/abcdlsj/ak/internal/login"
	"github.com/abcdlsj/ak/internal/vendor"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
)

// loginOverlay signs in to a vendor as an account: it asks the vendor and
// the account's name unless given, then how to sign in, and runs it. A
// device sign-in waits here, showing the code, until it is approved.
type loginOverlay struct {
	fixed    bool // the vendor was given; only the method is asked
	vendorID string
	name     string
	method   string
	key      string
	form     *huh.Form

	// While a device sign-in waits: what to show, and how to stop it.
	waiting bool
	prompt  login.Prompt
	events  chan tea.Msg
	cancel  context.CancelFunc

	// then runs once the account is saved; nil just says so.
	then func(a *app, account string) tea.Cmd
}

type (
	devicePromptMsg login.Prompt
	signedInMsg     struct {
		creds login.Credentials
		err   error
	}
)

// newLoginOverlay signs in to v as account name; a zero v asks for both.
func newLoginOverlay(v vendor.Vendor, name string, then func(*app, string) tea.Cmd) *loginOverlay {
	return &loginOverlay{fixed: v.ID != "", vendorID: v.ID, name: name, then: then}
}

func (o *loginOverlay) vendor() vendor.Vendor {
	v, _ := vendor.Lookup(o.vendorID)
	return v
}

func (o *loginOverlay) init(a *app) tea.Cmd { return o.build(a).Init() }

func (o *loginOverlay) build(a *app) *huh.Form {
	if o.form != nil {
		return o.form
	}
	var groups []*huh.Group
	if !o.fixed {
		var opts []huh.Option[string]
		for _, v := range vendor.All() {
			opts = append(opts, huh.NewOption(v.Name+" ("+v.ID+")", v.ID))
		}
		groups = append(groups, huh.NewGroup(
			huh.NewSelect[string]().Title("Vendor").Value(&o.vendorID).Options(opts...).Height(12).
				Description("/ filters"),
			huh.NewInput().Title("Account name").Value(&o.name).
				PlaceholderFunc(func() string { return o.vendorID }, &o.vendorID).
				Description("Blank names it after the vendor"),
		))
	}
	groups = append(groups,
		huh.NewGroup(
			huh.NewSelect[string]().Title("Sign in by").Value(&o.method).
				OptionsFunc(func() []huh.Option[string] {
					var opts []huh.Option[string]
					for _, l := range login.Methods(o.vendor()) {
						opts = append(opts, huh.NewOption(login.Describe(l), l.Method))
					}
					return opts
				}, &o.vendorID),
		).WithHideFunc(func() bool { return len(login.Methods(o.vendor())) < 2 }),
		huh.NewGroup(
			huh.NewInput().Title("API key").Value(&o.key).EchoMode(huh.EchoModePassword).
				DescriptionFunc(func() string {
					if u := o.vendor().APIKeyURL; u != "" {
						return "Get one at " + u
					}
					return "Or a reference: env:NAME, cmd:..., keychain:..."
				}, &o.vendorID),
		).WithHideFunc(func() bool { return o.chosen().Method != vendor.LoginPaste }),
	)
	o.form = huh.NewForm(groups...).WithWidth(min(a.contentWidth(), 80)).WithShowHelp(true)
	return o.form
}

// chosen is the login method picked, or the only one there is.
func (o *loginOverlay) chosen() vendor.Login {
	ms := login.Methods(o.vendor())
	for _, l := range ms {
		if l.Method == o.method {
			return l
		}
	}
	if len(ms) > 0 {
		return ms[0]
	}
	return vendor.Login{Method: vendor.LoginPaste}
}

func (o *loginOverlay) update(a *app, msg tea.Msg) (bool, tea.Cmd) {
	switch msg := msg.(type) {
	case devicePromptMsg:
		o.prompt = login.Prompt(msg)
		login.OpenBrowser(o.prompt.URL)
		return false, o.next()
	case signedInMsg:
		o.waiting = false
		if msg.err != nil {
			return true, a.notify("sign-in failed: "+msg.err.Error(), true)
		}
		return o.save(a, msg.creds)
	case tea.KeyMsg:
		if s := msg.String(); s == "esc" || s == "ctrl+c" {
			if o.cancel != nil {
				o.cancel()
			}
			return true, a.notify("sign-in cancelled", false)
		}
	}
	if o.waiting {
		return false, nil
	}
	m, cmd := o.build(a).Update(msg)
	if f, ok := m.(*huh.Form); ok {
		o.form = f
	}
	switch o.form.State {
	case huh.StateAborted:
		return true, a.notify("sign-in cancelled", false)
	case huh.StateCompleted:
		return o.run(a)
	}
	return false, cmd
}

// run signs in by the method chosen: a pasted key or a file at once, a
// device sign-in in the background, reporting back through events.
func (o *loginOverlay) run(a *app) (bool, tea.Cmd) {
	l := o.chosen()
	switch l.Method {
	case vendor.LoginPaste:
		return o.save(a, login.Credentials{Key: strings.TrimSpace(o.key)})
	case vendor.LoginFile:
		c, err := login.File(l)
		if err != nil {
			return true, a.notify("sign-in failed: "+err.Error(), true)
		}
		return o.save(a, c)
	}
	ctx, cancel := context.WithCancel(context.Background())
	o.waiting, o.cancel, o.events = true, cancel, make(chan tea.Msg, 2)
	go func() {
		c, err := login.Device(ctx, *l.Device, func(p login.Prompt) { o.events <- devicePromptMsg(p) })
		o.events <- signedInMsg{creds: c, err: err}
	}()
	return false, o.next()
}

// next waits for the device sign-in's next event.
func (o *loginOverlay) next() tea.Cmd {
	return func() tea.Msg { return <-o.events }
}

func (o *loginOverlay) save(a *app, c login.Credentials) (bool, tea.Cmd) {
	if c.Key == "" {
		return true, a.notify("sign-in cancelled: no key", false)
	}
	name := strings.TrimSpace(o.name)
	if name == "" {
		name = o.vendorID
	}
	if err := core.SignIn(a.cfg, name, o.vendorID, c); err != nil {
		return true, a.notify(err.Error(), true)
	}
	if o.then != nil {
		return true, o.then(a, name)
	}
	return true, tea.Batch(a.notify("signed in to "+o.vendor().Name+" as "+name, false), a.sync())
}

func (o *loginOverlay) view(a *app, width int) string {
	head := "Sign in"
	if o.fixed {
		head += " to " + o.vendor().Name
	}
	if !o.waiting {
		return section(head, width) + "\n\n" + o.build(a).View() + "\n" + dimStyle.Render("enter next  ·  esc cancel")
	}
	var b strings.Builder
	b.WriteString(section(head, width) + "\n\n")
	if o.prompt.URL == "" {
		b.WriteString(a.spinner.View() + " starting the sign-in…\n")
	} else {
		b.WriteString("Approve the sign-in in the browser:\n\n  " + o.prompt.URL + "\n")
		if o.prompt.Code != "" {
			b.WriteString("\n  code  " + titleStyle.Render(o.prompt.Code) + "\n")
		}
		b.WriteString("\n" + a.spinner.View() + " waiting for the approval…\n")
	}
	return b.String() + "\n" + dimStyle.Render("esc cancel")
}
