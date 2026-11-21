package cli

import (
	"errors"
	"os"
	"strings"

	"github.com/spf13/cobra"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/charmbracelet/x/ansi"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/migrate"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// A hint keeps the run's -t and -n, and -y only
// when it completes the run (completeRun) and is no dry run; a hint repeating it for another
// reason (runWithout with consentFlags) or naming another command (runAs) drops -y. None keeps
// --trust.
var _ = Describe("the hint builder", func() {
	BeforeEach(func() { testenv.Isolate() })

	// given is each of -y, -t claude-code and, when dry, -n that cmd defines, as a user types them.
	given := func(cmd *cobra.Command, dry bool) []string {
		var out []string
		for _, f := range [][]string{{"dry-run", "-n"}, {"yes", "-y"}, {"target", "-t", "claude-code"}} {
			if cmd.Flags().Lookup(f[0]) != nil && (dry || f[0] != "dry-run") {
				out = append(out, f[1:]...)
			}
		}
		return out
	}

	It("keeps -t and -n on every command that defines them, -y only to complete a run that is no dry run, and never --trust", func() {
		var checked int
		var dry bool
		var walk func(*cobra.Command)
		walk = func(c *cobra.Command) {
			for _, s := range c.Commands() {
				walk(s)
			}
			run := given(c, dry)
			if len(run) == 0 {
				return
			}
			if c.Flags().Lookup("trust") != nil {
				run = append(run, "--trust=all")
			}
			Expect(c.ParseFlags(run)).To(Succeed(), c.CommandPath())
			complete, again := completeRun(c, nil), runWithout(c, nil, consentFlags...)
			for _, f := range [][]string{{"dry-run", " --dry-run"}, {"target", " --target claude-code"}} {
				if c.Flags().Lookup(f[0]) != nil && (dry || f[0] != "dry-run") {
					Expect(complete).To(ContainSubstring(f[1]), c.CommandPath())
					Expect(again).To(ContainSubstring(f[1]), c.CommandPath())
				}
			}
			Expect(strings.Contains(complete, " --yes")).To(Equal(c.Flags().Lookup("yes") != nil && (!dry || c.Flags().Lookup("dry-run") == nil)), c.CommandPath())
			Expect(again).NotTo(ContainSubstring("--yes"), c.CommandPath())
			Expect(complete).NotTo(ContainSubstring("--trust"), c.CommandPath())
			Expect(again).NotTo(ContainSubstring("--trust"), c.CommandPath())
			checked++
		}
		root, err := NewRootCmd(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		walk(root)
		dry = true
		root, err = NewRootCmd(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		walk(root)
		Expect(checked).To(BeNumerically(">", 5))
	})

	It("a hint naming another command keeps the flags it takes, never -y and none it does not take", func() {
		root, err := NewRootCmd(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		install, _, err := root.Find([]string{"install"})
		Expect(err).NotTo(HaveOccurred())
		Expect(install.ParseFlags([]string{"-n", "-y", "-t", "claude-code", "--json"})).To(Succeed())
		Expect(runAs([]string{"lola", "info"}, install, []string{"kit"})).To(Equal("lola info kit --json --target claude-code"))
		Expect(runAs(strings.Fields(install.CommandPath()), install, []string{"kit"})).To(Equal("lola install kit --dry-run --json --target claude-code"), "a dry run asks nothing")
	})
})

var _ = Describe("a hint line", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("prints a hint as one line with no width or when it fits", func() {
		h := "see them: lola sync --project -n"
		Expect(hintLine("  ", h, 0)).To(Equal("  hint: " + h))
		Expect(hintLine("  ", h, len("  hint: "+h))).To(Equal("  hint: " + h))
	})

	It("wraps the prose at spaces under its first word and keeps a command on one line", func() {
		h := "set target_versions.opencode to \"2.0\" in .lola/project.yml, then: lola sync --project --trust=entries"
		Expect(hintLine("    ", h, 60)).To(Equal("" +
			"    hint: set target_versions.opencode to \"2.0\" in\n" +
			"          .lola/project.yml, then:\n" +
			"          lola sync --project --trust=entries"))
	})

	It("gives a command wider than the line a line of its own, never broken", func() {
		cmd := "lola install m -C /a/very/long/project/directory -t opencode@1.0,claude-code -y"
		Expect(hintLine("  ", "for opencode 1.0: "+cmd, 40)).To(Equal("  hint: for opencode 1.0:\n        " + cmd))
	})

	It("ends a command at a semicolon outside quotes only, so a quoted argument stays whole", func() {
		h := "remove it: lola uninstall 'a; b' 'it'\\''s; x' --project; or keep it with lola sync"
		Expect(hintLine("", h, 50)).To(Equal("" +
			"hint: remove it:\n" +
			"      lola uninstall 'a; b' 'it'\\''s; x' --project;\n" +
			"      or keep it with lola sync"))
	})

	It("keeps every space inside a command it wraps", func() {
		cmd := "lola install 'my  module' --project"
		Expect(hintLine("", "install it from the directory you named: "+cmd, 40)).To(Equal("hint: install it from the directory you\n      named:\n      " + cmd))
	})

	It("wraps an error's hints to the width writeError is given, and not at all with none", func() {
		err := errs.WithHints(errors.New("lola sync needs your trust"), "see the changes it would make first: lola sync --project -n")
		var b strings.Builder
		writeError(&b, err, false, 40)
		Expect(b.String()).To(Equal("error: lola sync needs your trust\n  hint: see the changes it would make\n        first: lola sync --project -n\n"))
		b.Reset()
		writeError(&b, err, false, 0)
		Expect(b.String()).To(Equal("error: lola sync needs your trust\n  hint: see the changes it would make first: lola sync --project -n\n"))
	})

	It("does not wrap on a line too narrow to hold the hint's lead and some text", func() {
		h := "see them: lola sync --project -n --json --trust=entries"
		Expect(hintLine("        ", h, 24)).To(Equal("        hint: " + h))
	})
})

var _ = Describe("a notice line", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("wraps a warning wider than the terminal at spaces, under its first word, and keeps its hints", func() {
		n := engine.Notice{Level: engine.LevelWarn, Text: "my-module comes from /tmp/work/my-module, outside the project: a teammate's sync will refuse it", Hints: []string{"lola sync -n"}}
		Expect(noticeLine(style{width: 50}, n)).To(Equal("" +
			"⚠ my-module comes from /tmp/work/my-module,\n" +
			"  outside the project: a teammate's sync will\n" +
			"  refuse it\n" +
			"    hint: lola sync -n"))
	})

	It("wraps an informational notice the same way and prints one that fits, or with no width, on one line", func() {
		n := engine.Notice{Level: engine.LevelInfo, Text: "one two three four five six seven eight"}
		Expect(noticeLine(style{width: 30}, n)).To(Equal("→ one two three four five six\n  seven eight"))
		Expect(noticeLine(style{width: 41}, n)).To(Equal("→ " + n.Text))
		Expect(noticeLine(style{}, n)).To(Equal("→ " + n.Text))
	})

	It("wraps a notice that names lola in passing, which is prose and no command", func() {
		n := engine.Notice{Level: engine.LevelWarn, Text: "git is not on PATH, so lola could not check that git does not track the files it wrote"}
		Expect(noticeLine(style{width: 40}, n)).To(Equal("" +
			"⚠ git is not on PATH, so lola could not\n" +
			"  check that git does not track the\n" +
			"  files it wrote"))
	})

	It("indents every line of a notice under lola info and lola migrate, and fits each in the width", func() {
		text := "one two three four five six seven eight nine ten eleven twelve"
		notice := []engine.Notice{{Level: engine.LevelWarn, Text: text}}
		var b strings.Builder
		printInfo(&b, style{width: 30}, &engine.Info{Install: []engine.InfoInstall{{Scope: "user", Notices: notice}}})
		printMigration(&b, style{width: 30}, &migrate.Result{Destinations: []*migrate.DestResult{{Notices: notice}}})
		var wrapped int
		for line := range strings.SplitSeq(b.String(), "\n") {
			if strings.Contains(line, "one two") || strings.Contains(line, "seven") || strings.Contains(line, "twelve") || strings.Contains(line, "nine") {
				Expect(line).To(HavePrefix("  "))
				Expect(ansi.StringWidth(line)).To(BeNumerically("<=", 30), line)
				wrapped++
			}
		}
		Expect(wrapped).To(BeNumerically(">=", 4), b.String())
	})

	It("never wraps a --plain row", func() {
		n := engine.Notice{Level: engine.LevelWarn, Text: "one two three four five six seven eight"}
		Expect(noticeLine(style{plain: true, width: 30}, n)).To(Equal("notice\twarn\t" + n.Text))
	})
})
