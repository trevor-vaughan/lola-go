package cli

import (
	"bytes"
	"image/color"
	"os"
	"path/filepath"
	"reflect"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// minContrast is WCAG 2's AA contrast ratio for text, which Plan 10 §4 asks of every color.
const minContrast = 4.5

// eachStyle calls visit with every lipgloss.Style in v, a huh.Styles or a struct inside one,
// and the style's field path.
func eachStyle(v reflect.Value, at string, visit func(string, lipgloss.Style)) {
	if s, ok := v.Interface().(lipgloss.Style); ok {
		visit(at, s)
		return
	}
	if v.Kind() != reflect.Struct {
		return
	}
	for i := range v.NumField() {
		if f := v.Type().Field(i); f.IsExported() {
			eachStyle(v.Field(i), at+"."+f.Name, visit)
		}
	}
}

// pickerView draws a focused two-option multi-select in t: a ticked, under the cursor, and b.
func pickerView(t *huh.Styles) string {
	var v []string
	opts := []huh.Option[string]{huh.NewOption("a", "a").Selected(true), huh.NewOption("b", "b")}
	ms := (&huhForms{height: 40}).multi("Pick", "", opts, &v)
	ms.WithTheme(huh.ThemeFunc(func(bool) *huh.Styles { return t }))
	ms.Focus()
	return ms.View()
}

var _ = Describe("the pickers' theme (Plan 10 §4)", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("measures contrast as WCAG 2 does", func() {
		Expect(contrastRatio(color.Black, color.White)).To(BeNumerically("~", 21, 0.001))
		Expect(contrastRatio(color.White, color.White)).To(BeNumerically("~", 1, 0.001))
		Expect(contrastRatio(lipgloss.Color("#767676"), color.White)).To(BeNumerically("~", 4.54, 0.005))
		Expect(contrastRatio(color.White, lipgloss.Color("#767676"))).To(Equal(contrastRatio(lipgloss.Color("#767676"), color.White)), "order does not matter")
	})

	DescribeTable("every color the theme sets reads at 4.5:1 or more against what it is drawn on",
		func(isDark bool, terminal color.Color) {
			seen := 0
			eachStyle(reflect.ValueOf(*lolaTheme(isDark)), "Styles", func(at string, s lipgloss.Style) {
				under := terminal
				if _, none := s.GetBackground().(lipgloss.NoColor); !none {
					under = s.GetBackground()
				}
				for what, fg := range map[string]color.Color{"foreground": s.GetForeground(), "left border": s.GetBorderLeftForeground()} {
					if _, none := fg.(lipgloss.NoColor); none {
						continue
					}
					seen++
					Expect(contrastRatio(fg, under)).To(BeNumerically(">=", minContrast), "%s's %s %v on %v", at, what, fg, under)
				}
			})
			Expect(seen).To(BeNumerically(">=", 30), "the walk reached the theme's colors")
		},
		Entry("dark, on #000000", true, color.Black),
		Entry("light, on #FFFFFF", false, color.White),
	)

	It("marks a pick [x] in bold and the rest [ ], under a bold cursor, in color and without", func() {
		for _, t := range []*huh.Styles{lolaTheme(true), lolaTheme(false), plainTheme()} {
			view := pickerView(t)
			Expect(ansi.Strip(view)).To(ContainSubstring("> [x] a"))
			Expect(ansi.Strip(view)).To(ContainSubstring("[ ] b"))
			Expect(view).To(MatchRegexp(`\x1b\[1(;[0-9;]*)?m> `), "the cursor is bold")
			Expect(ansi.Strip(view)).To(ContainSubstring("┃"), "the focused field's thick left border")
			Expect(view).To(MatchRegexp(`\x1b\[1(;[0-9;]*)?m\[x\] `), "the tick is bold")
		}
		Expect(pickerView(plainTheme())).NotTo(MatchRegexp(`\x1b\[[0-9;]*[34]8;`), "without color, no color at all")
	})

	It("shows huh's key help under a picker, in the theme's colors (nv-2)", func() {
		var v []string
		g := huh.NewGroup((&huhForms{height: 40}).multi("Pick", "", []huh.Option[string]{huh.NewOption("a", "a").Selected(true)}, &v))
		huh.NewForm(g).WithTheme(huh.ThemeFunc(func(bool) *huh.Styles { return lolaTheme(true) })).Init()
		Expect(ansi.Strip(g.Footer())).To(ContainSubstring("x toggle • ↑ up • ↓ down"))
		Expect(g.Footer()).To(ContainSubstring("\x1b[38;2;228;228;228mx\x1b[m"), "keys in the dark theme's text color, #E4E4E4")
	})

	It("draws forms in lola's theme for the background it resolves once per prompter, and plainly without color", func() {
		var out bytes.Buffer
		asked := 0
		h := accessible(&out, "1", "0", "1", "0")
		h.color, h.dark = true, func() (bool, error) { asked++; return true, nil }
		for range 2 {
			_, err := h.Assistants(AssistantStep{Options: []string{"claude-code"}})
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(asked).To(Equal(1))
		Expect(out.String()).To(ContainSubstring("\x1b[1;38;2;180;167;255mInstall for which assistants?"), "the title in the dark accent, #B4A7FF")

		out.Reset()
		_, err := accessible(&out, "1", "0").Assistants(AssistantStep{Options: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(out.String()).To(ContainSubstring("Install for which assistants?"))
		Expect(out.String()).NotTo(MatchRegexp(`\x1b\[[0-9;]*[34]8;`))

		By("a background that cannot be resolved stops the form before it asks")
		out.Reset()
		h = accessible(&out, "1", "0")
		h.color, h.dark = true, func() (bool, error) { return false, ErrAborted }
		_, err = h.Assistants(AssistantStep{Options: []string{"claude-code"}})
		Expect(err).To(MatchError(ErrAborted))
		Expect(out.String()).To(BeEmpty())
	})
})

var _ = Describe("the pickers' background (Plan 10 §4)", func() {
	var (
		errb bytes.Buffer
		cfg  string
	)
	noAnswer := func(*cobra.Command) (color.Color, error) { return nil, errNoTerminal }
	says := func(c color.Color) func(*cobra.Command) (color.Color, error) {
		return func(*cobra.Command) (color.Color, error) { return c, nil }
	}
	BeforeEach(func() {
		cfg = filepath.Join(testenv.Isolate(), "config", "lola", "config.yaml")
		errb.Reset()
	})
	// background resolves the background with the terminal answering query and p asking.
	background := func(query func(*cobra.Command) (color.Color, error), p Prompter) (bool, error) {
		a := &app{getenv: os.Getenv, queryBackground: query, newPrompter: func(*cobra.Command) Prompter { return p }}
		cmd := &cobra.Command{}
		cmd.SetErr(&errb)
		return a.darkBackground(cmd)
	}
	writeConfig := func(s string) {
		Expect(os.MkdirAll(filepath.Dir(cfg), 0o700)).To(Succeed())
		Expect(os.WriteFile(cfg, []byte(s), 0o600)).To(Succeed())
	}

	It("takes theme: from config.yaml first, over what the terminal says", func() {
		writeConfig("version: 1\ntheme: light\n")
		dark, err := background(says(color.Black), accessible(&errb))
		Expect(err).NotTo(HaveOccurred())
		Expect(dark).To(BeFalse())
		writeConfig("version: 1\ntheme: dark\n")
		dark, err = background(says(color.White), accessible(&errb))
		Expect(err).NotTo(HaveOccurred())
		Expect(dark).To(BeTrue())
		Expect(errb.String()).To(BeEmpty(), "nothing asked")
	})

	It("refuses a theme: it does not know, saying what to write", func() {
		writeConfig("version: 1\ntheme: blue\n")
		_, err := background(says(color.Black), accessible(&errb))
		Expect(err).To(MatchError(cfg + `:2: theme: "blue" is not a theme`))
		Expect(errs.Hints(err)).To(Equal([]string{"write theme: dark or theme: light, for your terminal's background"}))
	})

	DescribeTable("takes the terminal's answer when config.yaml has none, and writes nothing",
		func(bg color.Color, dark bool) {
			got, err := background(says(bg), accessible(&errb))
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(dark))
			Expect(errb.String()).To(BeEmpty())
			Expect(cfg).NotTo(BeAnExistingFile())
		},
		Entry("a dark terminal", lipgloss.Color("#1E1E1E"), true),
		Entry("a light terminal", lipgloss.Color("#FDF6E3"), false),
	)

	It("asks when the terminal answers without a color", func() {
		dark, err := background(func(*cobra.Command) (color.Color, error) { return nil, nil }, accessible(&errb, "1"))
		Expect(err).NotTo(HaveOccurred())
		Expect(dark).To(BeTrue())
		Expect(errb.String()).To(ContainSubstring("Is your terminal background dark or light?"))
	})

	It("in accessible mode never queries the terminal nor asks: the configured theme, else light, unsaved", func() {
		queried := false
		a := &app{getenv: func(k string) string {
			if k == "LOLA_ACCESSIBLE" {
				return "1"
			}
			return os.Getenv(k)
		}, queryBackground: func(*cobra.Command) (color.Color, error) { queried = true; return color.Black, nil },
			newPrompter: func(*cobra.Command) Prompter { return accessible(&errb, "1") }}
		cmd := &cobra.Command{}
		cmd.SetErr(&errb)
		dark, err := a.darkBackground(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(dark).To(BeFalse())
		Expect(queried).To(BeFalse())
		Expect(errb.String()).To(BeEmpty(), "no question, and nothing saved")
		Expect(cfg).NotTo(BeAnExistingFile())

		Expect(config.SaveTheme(cfg, "", config.ThemeDark)).To(Succeed())
		dark, err = a.darkBackground(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(dark).To(BeTrue(), "the theme in config.yaml")
		Expect(errb.String()).To(BeEmpty())
	})

	It("asks once on a terminal that does not say, and saves the answer in config.yaml", func() {
		dark, err := background(noAnswer, accessible(&errb, "1"))
		Expect(err).NotTo(HaveOccurred())
		Expect(dark).To(BeTrue())
		Expect(errb.String()).To(ContainSubstring("Is your terminal background dark or light?"))
		Expect(errb.String()).To(HaveSuffix("→ saved theme: dark in " + cfg + "\n"))
		c, err := config.LoadConfig(cfg, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Theme).To(Equal(config.ThemeDark))

		By("the next run reads it and asks nothing")
		errb.Reset()
		dark, err = background(noAnswer, accessible(&errb))
		Expect(err).NotTo(HaveOccurred())
		Expect(dark).To(BeTrue())
		Expect(errb.String()).To(BeEmpty())
	})

	It("warns when it cannot save the answer, and goes on with it", func() {
		if os.Geteuid() == 0 {
			Skip("root writes into a read-only directory")
		}
		Expect(os.MkdirAll(filepath.Dir(cfg), 0o700)).To(Succeed())
		Expect(os.Chmod(filepath.Dir(cfg), 0o500)).To(Succeed())
		DeferCleanup(os.Chmod, filepath.Dir(cfg), os.FileMode(0o700))
		dark, err := background(noAnswer, accessible(&errb, "1"))
		Expect(err).NotTo(HaveOccurred())
		Expect(dark).To(BeTrue())
		Expect(errb.String()).To(ContainSubstring("⚠ could not save theme: could not write " + cfg))
		Expect(errb.String()).To(HaveSuffix("; set theme: dark in " + cfg + "\n"))
		Expect(errb.String()).NotTo(ContainSubstring("→ saved theme"))
		Expect(cfg).NotTo(BeAnExistingFile())
	})

	It("cancels when the question is cancelled, and saves nothing", func() {
		_, err := background(noAnswer, accessible(&errb))
		Expect(err).To(MatchError(ErrAborted))
		Expect(cfg).NotTo(BeAnExistingFile())
	})

	It("assumes light off a terminal, asking nothing and saving nothing", func() {
		dark, err := background(noAnswer, noPrompter{})
		Expect(err).NotTo(HaveOccurred())
		Expect(dark).To(BeFalse())
		Expect(errb.String()).To(BeEmpty())
		Expect(cfg).NotTo(BeAnExistingFile())
	})
})
