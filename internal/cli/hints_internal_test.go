package cli

import (
	"errors"
	"os"
	"strings"

	"github.com/spf13/cobra"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
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
