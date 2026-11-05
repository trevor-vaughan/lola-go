package cli

import (
	"os"
	"testing"

	"github.com/spf13/cobra"
)

// TestInterruptChild is no spec of its own: "accessible mode: Ctrl+C at a sync question cancels
// with exit 0" runs this test binary again with LOLA_SPEC_CHILD naming the question to wait at,
// so the interrupt it sends reaches a lola of its own rather than the spec runner, which takes
// Ctrl+C as the user stopping the specs.
func TestInterruptChild(t *testing.T) {
	child := os.Getenv("LOLA_SPEC_CHILD")
	if child == "" {
		t.Skip("run by the accessible-mode Ctrl+C spec")
	}
	h := &huhForms{in: &eofReader{r: os.Stdin}, out: os.Stderr, tty: true, accessible: true}
	// ask runs one question as a command does, so Execute prints its outcome; it takes no
	// arguments (cobra would otherwise parse the test binary's).
	ask := func(question func() error) *cobra.Command {
		cmd := &cobra.Command{SilenceErrors: true, SilenceUsage: true, RunE: func(*cobra.Command, []string) error { return question() }}
		cmd.SetArgs([]string{})
		return cmd
	}
	var root *cobra.Command
	switch child {
	case "sync":
		root = NewRootCmd(os.Getenv, WithPrompter(h))
		root.SetArgs([]string{"sync", "-C", os.Getenv("LOLA_SPEC_PROJECT")})
	case "modules":
		root = ask(func() error {
			_, err := h.Modules(ModuleStep{Options: []ModuleOption{{Name: "m", Label: "m"}}})
			return err
		})
	case "hooks":
		root = ask(func() error {
			cmd := NewRootCmd(os.Getenv)
			cmd.SetIn(os.Stdin)
			cmd.SetErr(os.Stderr)
			_, err := (&app{getenv: os.Getenv}).ask(cmd, "Run m's install hooks?")
			return err
		})
	default:
		t.Fatalf("no child %q", child)
	}
	os.Exit(Execute(root))
}
