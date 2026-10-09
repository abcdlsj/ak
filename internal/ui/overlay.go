package ui

import (
	"fmt"
	"strings"

	"github.com/abcdlsj/ak/internal/core"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
)

// formOverlay edits a provider with the same form as `ak add` / `ak edit`.
type formOverlay struct {
	draft   *Draft
	editing bool
	form    *huh.Form
	// picking is set while a new provider's engine and preset are asked,
	// before the main form.
	picking bool
}

func newFormOverlay(d *Draft, editing bool) *formOverlay {
	return &formOverlay{draft: d, editing: editing, picking: !editing && d.IsNew()}
}

func (o *formOverlay) init(a *app) tea.Cmd { return o.build(a).Init() }

func (o *formOverlay) build(a *app) *huh.Form {
	if o.form == nil {
		f := o.draft.PresetForm()
		if !o.picking {
			f = o.draft.Form(func(n string) bool { _, ok := a.cfg.Providers[n]; return ok })
		}
		o.form = f.WithWidth(min(a.contentWidth(), 80)).WithShowHelp(true)
	}
	return o.form
}

func (o *formOverlay) update(a *app, msg tea.Msg) (bool, tea.Cmd) {
	f := o.build(a)
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc", "ctrl+c":
			return true, a.notify("cancelled", false)
		case "ctrl+s":
			// Save from any field, without stepping through the rest. While
			// picking a preset there is nothing to save yet.
			if !o.picking {
				return o.save(a)
			}
		}
	}
	m, cmd := f.Update(msg)
	if nf, ok := m.(*huh.Form); ok {
		o.form = nf
	}
	switch o.form.State {
	case huh.StateAborted:
		return true, a.notify("cancelled", false)
	case huh.StateCompleted:
		if o.picking {
			o.picking = false
			o.draft.ApplyPreset()
			o.form = nil
			return false, o.init(a)
		}
		return o.save(a)
	}
	return false, cmd
}

// save stores the draft; on a rejected change the form stays open so nothing
// typed is lost.
func (o *formOverlay) save(a *app) (bool, tea.Cmd) {
	p := o.draft.Provider()
	var err error
	if o.editing {
		err = core.Edit(a.cfg, o.draft.Orig(), o.draft.Name, p)
	} else {
		err = core.Add(a.cfg, o.draft.Name, p)
	}
	if err != nil {
		if o.form.State != huh.StateNormal {
			// A finished huh form ignores input; rebuild it from the draft.
			o.form = nil
			return false, tea.Batch(o.init(a), a.notify(err.Error(), true))
		}
		return false, a.notify(err.Error(), true)
	}
	// Leave the cursor on what was just saved.
	for _, i := range []int{pageHome, pageManage} {
		a.pages[i].(*providersPage).focus(a.cfg, o.draft.Name)
	}
	return true, a.sync()
}

func (o *formOverlay) view(a *app, width int) string {
	head := "Add provider"
	if o.editing {
		head = "Edit " + o.draft.Name
	}
	return section(head, width) + "\n\n" + o.build(a).View() + "\n" + dimStyle.Render("ctrl+s save  ·  esc cancel")
}

// confirmOverlay asks a yes/no question before a destructive action.
type confirmOverlay struct {
	question string
	onYes    func(a *app) tea.Cmd
}

func newConfirmOverlay(q string, onYes func(a *app) tea.Cmd) *confirmOverlay {
	return &confirmOverlay{question: q, onYes: onYes}
}

func (o *confirmOverlay) update(a *app, msg tea.Msg) (bool, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return false, nil
	}
	switch strings.ToLower(k.String()) {
	case "y":
		return true, o.onYes(a)
	case "n", "esc", "q", "ctrl+c", "enter":
		return true, nil
	}
	return false, nil
}

func (o *confirmOverlay) view(a *app, width int) string {
	return panelStyle.Render(warnStyle.Render(o.question) + "\n\n" + dimStyle.Render("y confirm  ·  n / esc cancel"))
}

// pickOverlay chooses a variant before launching.
type pickOverlay struct {
	provider string
	options  []string // the first option is "no variant"
	cursor   int
}

func newPickOverlay(provider string, variants []string) *pickOverlay {
	return &pickOverlay{provider: provider, options: append([]string{""}, variants...)}
}

func (o *pickOverlay) update(a *app, msg tea.Msg) (bool, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return false, nil
	}
	switch k.String() {
	case "up", "k":
		o.cursor = max(o.cursor-1, 0)
	case "down", "j":
		o.cursor = min(o.cursor+1, len(o.options)-1)
	case "enter":
		return true, a.launch(Selection{Provider: o.provider, Variant: o.options[o.cursor]})
	case "esc", "q", "ctrl+c":
		return true, nil
	default:
		// Typing a variant's first letter jumps to it.
		for i, v := range o.options {
			if v != "" && strings.HasPrefix(v, k.String()) {
				o.cursor = i
				break
			}
		}
	}
	return false, nil
}

func (o *pickOverlay) view(a *app, width int) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Launch %s%s with\n\n", a.cfg.Settings.Prefix, o.provider))
	for i, v := range o.options {
		label := v
		if v == "" {
			label = "default"
		}
		if i == o.cursor {
			b.WriteString(pickStyle.Render("▌ "+label) + "\n")
		} else {
			b.WriteString("  " + label + "\n")
		}
	}
	b.WriteString("\n" + dimStyle.Render("enter launch  ·  esc back"))
	return panelStyle.Render(b.String())
}
