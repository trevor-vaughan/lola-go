package cli

import (
	"os"
	"strings"

	"github.com/spf13/cobra"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

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
		walk(NewRootCmd(os.Getenv))
		dry = true
		walk(NewRootCmd(os.Getenv))
		Expect(checked).To(BeNumerically(">", 5))
	})

	It("a hint naming another command keeps the flags it takes, never -y and none it does not take", func() {
		root := NewRootCmd(os.Getenv)
		install, _, err := root.Find([]string{"install"})
		Expect(err).NotTo(HaveOccurred())
		Expect(install.ParseFlags([]string{"-n", "-y", "-t", "claude-code", "--json"})).To(Succeed())
		Expect(runAs([]string{"lola", "info"}, install, []string{"kit"})).To(Equal("lola info kit --json --target claude-code"))
		Expect(runAs(strings.Fields(install.CommandPath()), install, []string{"kit"})).To(Equal("lola install kit --dry-run --json --target claude-code"), "a dry run asks nothing")
	})
})
