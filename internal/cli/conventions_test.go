package cli_test

import (
	"fmt"
	"os"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// commands walks the live command tree (available commands plus help).
func commands() []*cobra.Command {
	var out []*cobra.Command
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		out = append(out, c)
		for _, s := range c.Commands() {
			if s.IsAvailableCommand() || s.Name() == "help" {
				walk(s)
			}
		}
	}
	root, err := cli.NewRootCmd(os.Getenv)
	Expect(err).NotTo(HaveOccurred())
	walk(root)
	return out
}

var _ = Describe("command-tree conventions", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("every command has a Short and an Example", func() {
		for _, c := range commands() {
			Expect(c.Short).NotTo(BeEmpty(), c.CommandPath())
			Expect(c.Example).NotTo(BeEmpty(), c.CommandPath())
		}
	})

	It("keeps help short: one-line Short, Long of 15 lines at most, Use and flag usages one short line", func() {
		var over []string
		for _, c := range commands() {
			p := c.CommandPath()
			if strings.Contains(strings.TrimSpace(c.Short), "\n") {
				over = append(over, p+": Short is more than one line")
			}
			if n := strings.Count(strings.TrimSpace(c.Long), "\n") + 1; c.Long != "" && n > 15 {
				over = append(over, fmt.Sprintf("%s: Long is %d lines (max 15)", p, n))
			}
			if n := len(c.Use); n > 72 {
				over = append(over, fmt.Sprintf("%s: Use is %d characters (max 72)", p, n))
			}
			c.LocalFlags().VisitAll(func(f *pflag.Flag) {
				if strings.Contains(f.Usage, "\n") || len(f.Usage) > 80 {
					over = append(over, fmt.Sprintf("%s --%s: usage is %d characters (max 80, one line)", p, f.Name, len(f.Usage)))
				}
			})
		}
		Expect(over).To(BeEmpty(), strings.Join(over, "\n"))
	})

	It("aliases never collide with a sibling's name or alias", func() {
		for _, c := range commands() {
			seen := map[string]string{}
			for _, s := range c.Commands() {
				for _, n := range append([]string{s.Name()}, s.Aliases...) {
					Expect(seen).NotTo(HaveKey(n), "%s: %q used by %s and %s", c.CommandPath(), n, seen[n], s.Name())
					seen[n] = s.Name()
				}
			}
		}
	})

	It("every --yes has -y, and --force is never shorthanded", func() {
		for _, c := range commands() {
			if f := c.Flags().Lookup("yes"); f != nil {
				Expect(f.Shorthand).To(Equal("y"), c.CommandPath())
			}
			if f := c.Flags().Lookup("force"); f != nil {
				Expect(f.Shorthand).To(BeEmpty(), c.CommandPath())
			}
		}
	})

	It("no command's help but lola migrate's mentions Python", func() {
		for _, c := range commands() {
			path := strings.Fields(c.CommandPath())[1:]
			if len(path) == 0 || path[0] == "help" || path[0] == "migrate" {
				continue // the root's and help's full reference include lola migrate's
			}
			for _, args := range [][]string{append(slices.Clone(path), "--help"), append(append([]string{"help"}, path...), "--short")} {
				r := lola(args...)
				Expect(r.code).To(Equal(0), strings.Join(args, " "))
				Expect(r.out).NotTo(MatchRegexp(`(?i)python`), strings.Join(args, " "))
			}
		}
	})

	It("every command group completes its subcommands' aliases", func() {
		for _, c := range commands() {
			if c.HasAvailableSubCommands() {
				Expect(c.ValidArgsFunction).NotTo(BeNil(), c.CommandPath())
			}
		}
	})

	It("every flag that takes a value completes, and every command completes its arguments", func() {
		// A free-text flag registers cobra.NoFileCompletions: without it the shell offers file names.
		var missing []string
		for _, c := range commands() {
			c.LocalFlags().VisitAll(func(f *pflag.Flag) {
				if f.NoOptDefVal != "" || f.Hidden {
					return // a bool, or a flag whose value is optional
				}
				if _, ok := c.GetFlagCompletionFunc(f.Name); !ok {
					missing = append(missing, c.CommandPath()+" --"+f.Name)
				}
			})
			// The shell completes file names for a command that registers nothing, even one that
			// takes no arguments.
			if c.ValidArgsFunction == nil && len(c.ValidArgs) == 0 {
				missing = append(missing, c.CommandPath()+" <args>")
			}
		}
		Expect(missing).To(BeEmpty())
	})
})
