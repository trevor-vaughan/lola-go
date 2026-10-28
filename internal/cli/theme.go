package cli

import (
	"fmt"
	"image/color"
	"math"
	"os"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// The multi-select markers of both themes (Plan 10 §4): a ticked option, and one that is not.
const (
	checkedMark   = "[x] "
	uncheckedMark = "[ ] "
)

// lolaTheme is the pickers' theme in color (Plan 10 §4): huh's Charm theme with lola's markers
// and colors chosen for a dark (isDark) or light background. Every foreground reaches WCAG's
// 4.5:1 against #000000 when isDark and #FFFFFF otherwise, or against the style's own background
// where it sets one; the key help included, which huh leaves at bubbles' dark defaults. huh
// passes a Theme its own guess at the background; huhForms.theme wraps lolaTheme so lola's
// answer wins.
func lolaTheme(isDark bool) *huh.Styles {
	t := huh.ThemeCharm(isDark)
	ld := lipgloss.LightDark(isDark)
	var (
		text     = ld(lipgloss.Color("#1C1C1C"), lipgloss.Color("#E4E4E4"))
		muted    = ld(lipgloss.Color("#595959"), lipgloss.Color("#B2B2B2"))
		faint    = ld(lipgloss.Color("#6B6B6B"), lipgloss.Color("#8A8A8A"))
		accent   = ld(lipgloss.Color("#4B3FCF"), lipgloss.Color("#B4A7FF"))
		cursor   = ld(lipgloss.Color("#A3006B"), lipgloss.Color("#FF87D7"))
		ticked   = ld(lipgloss.Color("#00703C"), lipgloss.Color("#5FD787"))
		failure  = ld(lipgloss.Color("#C4002B"), lipgloss.Color("#FF6B6B"))
		buttonFg = ld(lipgloss.Color("#FFFFFF"), lipgloss.Color("#000000"))
		buttonBg = ld(lipgloss.Color("#4B3FCF"), lipgloss.Color("#FF87D7"))
		idleBg   = ld(lipgloss.Color("#E4E4E4"), lipgloss.Color("#3A3A3A"))
	)
	f := &t.Focused
	f.Base = f.Base.BorderForeground(accent)
	f.Card = f.Base
	f.Title = f.Title.Foreground(accent)
	f.NoteTitle = f.NoteTitle.Foreground(accent)
	f.Directory = f.Directory.Foreground(accent)
	f.File = f.File.Foreground(text)
	f.Description = f.Description.Foreground(muted)
	f.ErrorIndicator = f.ErrorIndicator.Foreground(failure)
	f.ErrorMessage = f.ErrorMessage.Foreground(failure)
	f.SelectSelector = f.SelectSelector.Foreground(cursor).Bold(true)
	f.MultiSelectSelector = f.MultiSelectSelector.Foreground(cursor).Bold(true)
	f.NextIndicator = f.NextIndicator.Foreground(cursor)
	f.PrevIndicator = f.PrevIndicator.Foreground(cursor)
	f.Option = f.Option.Foreground(text)
	f.SelectedOption = f.SelectedOption.Foreground(ticked)
	f.SelectedPrefix = lipgloss.NewStyle().Foreground(ticked).Bold(true).SetString(checkedMark)
	f.UnselectedOption = f.UnselectedOption.Foreground(text)
	f.UnselectedPrefix = lipgloss.NewStyle().Foreground(text).SetString(uncheckedMark)
	f.FocusedButton = f.FocusedButton.Foreground(buttonFg).Background(buttonBg)
	f.Next = f.FocusedButton
	f.BlurredButton = f.BlurredButton.Foreground(text).Background(idleBg)
	f.TextInput.Cursor = f.TextInput.Cursor.Foreground(cursor)
	f.TextInput.Placeholder = f.TextInput.Placeholder.Foreground(faint)
	f.TextInput.Prompt = f.TextInput.Prompt.Foreground(cursor)
	f.TextInput.Text = f.TextInput.Text.Foreground(text)
	t.Help.Ellipsis = t.Help.Ellipsis.Foreground(muted)
	t.Help.ShortKey = t.Help.ShortKey.Foreground(text)
	t.Help.ShortDesc = t.Help.ShortDesc.Foreground(muted)
	t.Help.ShortSeparator = t.Help.ShortSeparator.Foreground(faint)
	t.Help.FullKey = t.Help.FullKey.Foreground(text)
	t.Help.FullDesc = t.Help.FullDesc.Foreground(muted)
	t.Help.FullSeparator = t.Help.FullSeparator.Foreground(faint)
	blur(t)
	return t
}

// plainTheme is the pickers' theme without color (LOLA_COLOR=0, NO_COLOR, or stderr not a
// terminal): huh's base theme with lolaTheme's markers, its cursors and ticks in bold; titles
// stay plain, as huh's base theme draws them.
func plainTheme() *huh.Styles {
	t := huh.ThemeBase(false)
	f := &t.Focused
	f.SelectSelector = f.SelectSelector.Bold(true)
	f.MultiSelectSelector = f.MultiSelectSelector.Bold(true)
	f.SelectedPrefix = lipgloss.NewStyle().Bold(true).SetString(checkedMark)
	f.UnselectedPrefix = lipgloss.NewStyle().SetString(uncheckedMark)
	blur(t)
	return t
}

// blur makes t's blurred styles, and its group's title and description, its focused ones: a
// field without the focus keeps its colors and loses its border, cursor and paging arrows, as in
// huh's own themes.
func blur(t *huh.Styles) {
	t.Blurred = t.Focused
	t.Blurred.Base = t.Focused.Base.BorderStyle(lipgloss.HiddenBorder())
	t.Blurred.Card = t.Blurred.Base
	t.Blurred.MultiSelectSelector = lipgloss.NewStyle().SetString("  ")
	t.Blurred.NextIndicator = lipgloss.NewStyle()
	t.Blurred.PrevIndicator = lipgloss.NewStyle()
	t.Group.Title = t.Focused.Title
	t.Group.Description = t.Focused.Description
}

// contrastRatio is WCAG 2's contrast ratio of a and b: 1 for none, 21 for black on white.
func contrastRatio(a, b color.Color) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// luminance is WCAG 2's relative luminance of c, from 0 (black) to 1 (white).
func luminance(c color.Color) float64 {
	r, g, b, _ := c.RGBA()
	linear := func(v uint32) float64 {
		s := float64(v) / 0xFFFF
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(r) + 0.7152*linear(g) + 0.0722*linear(b)
}

// darkBackground reports whether the terminal the pickers draw on is dark (Plan 10 §4):
// theme: in config.yaml; else, in accessible mode, light; else the terminal's answer to lola's
// query; else, on a terminal, the user's answer, saved as theme: so lola asks once (a save that
// fails is a warning, and the answer still holds for this run); else light.
func (a *app) darkBackground(cmd *cobra.Command) (bool, error) {
	eng, err := a.engine()
	if err != nil {
		return false, err
	}
	cfg, err := config.LoadConfig(eng.Paths.Config, eng.Paths.Home)
	if err != nil {
		return false, err
	}
	if cfg.Theme != "" {
		return cfg.Theme == config.ThemeDark, nil
	}
	// Accessible mode neither queries nor asks: the escapes would reach a screen reader, and the
	// question is noise there. Without a configured theme it draws light, and saves nothing.
	if a.accessible() {
		return false, nil
	}
	query := a.queryBackground
	if query == nil {
		query = terminalBackground
	}
	// A terminal that does not answer is not an error: lola asks instead. A query can also
	// succeed without a color (a terminal that answers DA1 but not OSC 11).
	if bg, err := query(cmd); err == nil && bg != nil {
		return contrastRatio(bg, color.White) > contrastRatio(bg, color.Black), nil
	}
	var p Prompter
	if a.newPrompter != nil {
		p = a.newPrompter(cmd)
	} else {
		p = a.huhPrompter(cmd)
	}
	if !p.Interactive() {
		return false, nil
	}
	dark, err := p.Background()
	if err != nil {
		return false, err
	}
	theme := config.ThemeLight
	if dark {
		theme = config.ThemeDark
	}
	where := untrusted.Text(eng.Paths.Config, untrusted.MaxName)
	if err := config.SaveTheme(eng.Paths.Config, eng.Paths.Home, theme); err != nil {
		// The answer still draws this run's pickers; the warning says how to keep it.
		fmt.Fprintln(cmd.ErrOrStderr(), style{color: a.color(cmd.ErrOrStderr())}.warn(
			"could not save theme: "+untrusted.CleanErr(err).Error()+"; set theme: "+theme+" in "+where))
		return dark, nil
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "→ saved theme: "+theme+" in "+where)
	return dark, nil
}

// terminalBackground asks the terminal on stdin or stderr, where the forms draw, for its
// background color (R-W3-4); an error when neither is a terminal file or the terminal does not
// answer in time. The color may be nil when the terminal answers without one.
func terminalBackground(cmd *cobra.Command) (color.Color, error) {
	in, inFile := cmd.InOrStdin().(*os.File)
	out, outFile := cmd.ErrOrStderr().(*os.File)
	if !inFile || !outFile {
		return nil, errNoTerminal
	}
	return lipgloss.BackgroundColor(in, out)
}
