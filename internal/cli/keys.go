package cli

import (
	"slices"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
)

// formExit is how lola's keys ended a form: not at all, q quitting it, or Esc going back a screen.
type formExit int

const (
	exitNone formExit = iota
	exitQuit
	exitBack
)

// quitQuestion is what q asks once two or more steps are answered; y quits, and n, Enter or Esc
// go back to the screen as it was.
const quitQuestion = "Quit? your answers so far are dropped — nothing has been changed"

// keyed is a field of a full-screen form under lola's keys: q quits, unless the field takes it as
// text (a text input, a filter being typed); Esc moves to the form's field before, as Shift+Tab
// does, and on the form's first field goes back a screen, unless the field takes it (a filter
// being typed or set); on the first screen of a flow (nav.Back unset) Esc says q quits instead.
// With two or more steps answered (nav.Answered and the form's fields before this one), q asks
// quitQuestion first. The question and the first-screen note take the key help's place, below
// the form, and the key help names esc back and q quit where they apply.
//
// A quit or a step back ends the form as Ctrl+C does, through the form's Quit binding, which
// huh's default keymap binds to ctrl+c alone, and exit says which it was.
type keyed struct {
	huh.Field
	nav    Nav
	index  int // the form's fields that take keys before this one
	exit   *formExit
	asking bool // q asked quitQuestion
	first  bool // Esc on the first screen: the key help says q quits
}

// quitKey is the key the form's Quit binding matches.
var quitKey = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}

// Update applies lola's keys before huh's, and returns the wrapper, which the group keeps in the
// field's place.
func (k *keyed) Update(msg tea.Msg) (huh.Model, tea.Cmd) {
	press, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return k.pass(msg)
	}
	if *k.exit != exitNone {
		// The form is ending: keys read with the one that ended it must not answer it.
		return k, nil
	}
	k.first = false
	if k.asking {
		switch press.String() {
		case "y", "Y":
			return k.end(exitQuit)
		case "n", "N", "enter", "esc":
			k.asking = false
		}
		return k, nil
	}
	switch press.String() {
	case "q":
		if k.typing() {
			break
		}
		if k.nav.Answered+k.index >= 2 {
			k.asking = true
			return k, nil
		}
		return k.end(exitQuit)
	case "esc":
		switch {
		case k.takesEsc():
		case k.index > 0:
			return k.pass(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
		case k.nav.Back:
			return k.end(exitBack)
		default:
			k.first = true
			return k, nil
		}
	}
	return k.pass(msg)
}

// pass hands msg to the field.
func (k *keyed) pass(msg tea.Msg) (huh.Model, tea.Cmd) {
	m, cmd := k.Field.Update(msg)
	k.Field = m.(huh.Field)
	return k, cmd
}

// end records how the keys ended the form and ends it as Ctrl+C does.
func (k *keyed) end(e formExit) (huh.Model, tea.Cmd) {
	*k.exit = e
	return k, func() tea.Msg { return quitKey }
}

// typing reports whether the field takes q as text: a text input, or a list whose filter is
// being typed.
func (k *keyed) typing() bool {
	if _, ok := k.Field.(*huh.Input); ok {
		return true
	}
	f, ok := k.Field.(interface{ GetFiltering() bool })
	return ok && f.GetFiltering()
}

// takesEsc reports whether the field has a use for Esc now: huh binds it to set or clear a
// list's filter while one is typed or set.
func (k *keyed) takesEsc() bool {
	return slices.ContainsFunc(k.Field.KeyBinds(), func(b key.Binding) bool {
		return b.Enabled() && slices.Contains(b.Keys(), "esc")
	})
}

// KeyBinds is the key help: esc back and q quit where they apply, then the field's; quitQuestion
// while q asks it, and after Esc with no screen to go back to, a note that q quits: "first step"
// when nothing is answered yet, else "no step to go back to" (a flow's closing question). lola's keys come first: the help is
// cut to the terminal's width, and huh's alone fill 80 columns.
func (k *keyed) KeyBinds() []key.Binding {
	quits := "q quits"
	if k.typing() {
		quits = "ctrl+c quits"
	}
	switch {
	case k.asking:
		return []key.Binding{key.NewBinding(key.WithKeys("y"), key.WithHelp(quitQuestion, "[y/N]"))}
	case k.first && k.nav.Answered+k.index > 0:
		return []key.Binding{key.NewBinding(key.WithKeys("esc"), key.WithHelp("no step to go back to —", quits))}
	case k.first:
		return []key.Binding{key.NewBinding(key.WithKeys("esc"), key.WithHelp("first step —", quits))}
	}
	var binds []key.Binding
	if !k.takesEsc() && (k.index > 0 || k.nav.Back) {
		binds = append(binds, key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")))
	}
	if !k.typing() {
		binds = append(binds, key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")))
	}
	return append(binds, k.Field.KeyBinds()...)
}

// Error is the field's error, but none while q asks: a group shows its fields' errors in the key
// help's place, where the question is.
func (k *keyed) Error() error {
	if k.asking {
		return nil
	}
	return k.Field.Error()
}
