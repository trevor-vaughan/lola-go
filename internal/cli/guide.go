package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/errs"
)

// argGuide describes a command's positional argument once; it renders both the Example
// block and the coaching error for a missing argument.
type argGuide struct {
	noun     string   // what the argument names
	find     string   // how to find valid values
	usage    string   // the usage line
	examples []string // complete example command lines
}

func (g argGuide) example() string {
	return "  " + strings.Join(g.examples, "\n  ")
}

// args requires at least min positional arguments. A find that is a lola command, whole, keeps
// the run's flags that command takes (runAs); one with a placeholder or prose is as written.
func (g argGuide) args(min int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) >= min {
			return nil
		}
		find := g.find
		if words := strings.Fields(find); len(words) > 1 && words[0] == "lola" {
			if found, rest, err := cmd.Root().Find(words[1:]); err == nil && found != cmd.Root() && len(rest) == 0 {
				find = runAs(words, cmd, nil)
			}
		}
		return errs.WithHints(fmt.Errorf("%s needs a %s", cmd.CommandPath(), g.noun),
			"usage: "+g.usage, "find one: "+find, "example: "+g.examples[0])
	}
}
