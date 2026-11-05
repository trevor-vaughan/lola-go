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

// args requires at least min positional arguments.
func (g argGuide) args(min int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) >= min {
			return nil
		}
		return errs.WithHints(fmt.Errorf("%s needs a %s", cmd.CommandPath(), g.noun),
			"usage: "+g.usage, "find one: "+g.find, "example: "+g.examples[0])
	}
}
