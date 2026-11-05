package cli_test

import (
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"

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
	walk(cli.NewRootCmd(os.Getenv))
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

	It("every command group completes its subcommands' aliases", func() {
		for _, c := range commands() {
			if c.HasAvailableSubCommands() {
				Expect(c.ValidArgsFunction).NotTo(BeNil(), c.CommandPath())
			}
		}
	})
})
