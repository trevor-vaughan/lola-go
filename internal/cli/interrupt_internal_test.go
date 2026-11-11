package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"syscall"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Execute after a signal", func() {
	// run runs a command returning err under root's flags, as sig cancelled its context.
	run := func(sig syscall.Signal, err error, args ...string) (int, string) {
		testenv.Isolate()
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(Interrupted{Signal: sig})
		root, rerr := NewRootCmd(os.Getenv)
		Expect(rerr).NotTo(HaveOccurred())
		root.AddCommand(&cobra.Command{Use: "stopped", RunE: func(*cobra.Command, []string) error { return err }})
		var errb bytes.Buffer
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&errb)
		root.SetArgs(append([]string{"stopped"}, args...))
		root.SetContext(ctx)
		return Execute(root), errb.String()
	}
	updated := func(outcome string) error {
		res := &engine.ProfileUpdateResult{Updates: []engine.ProfileOutcome{{Profile: "remote:go", Outcome: outcome, Hints: []string{}}},
			Scopes: []engine.UpdateScope{}}
		return &updateError{err: errs.WithHints(errors.New("remote:go is updated, but not applied; the run was stopped by a signal (terminated)"),
			"apply it later: lola sync --user example --json"), doc: profileUpdateDoc{ProfileUpdateResult: res, Scopes: []updateScopeDoc{}}}
	}

	It("prints, under --json, the error of an update that wrote a profile it did not apply, and exits as the signal would", func() {
		code, out := run(syscall.SIGTERM, updated(engine.UpdateUpdated), "--json")
		Expect(code).To(Equal(143))
		var v map[string]any
		Expect(json.Unmarshal([]byte(out), &v)).To(Succeed(), out)
		Expect(v["error"]).To(Equal("remote:go is updated, but not applied; the run was stopped by a signal (terminated)"))
		Expect(v["hints"]).To(Equal([]any{"apply it later: lola sync --user example --json"}))
		Expect(v["updates"]).To(ConsistOf(And(HaveKeyWithValue("profile", "remote:go"), HaveKeyWithValue("outcome", "updated"))))
	})

	It("prints nothing for an update that wrote nothing, or any other error", func() {
		code, out := run(syscall.SIGTERM, updated(engine.UpdateSkipped), "--json")
		Expect(code).To(Equal(143))
		Expect(out).To(BeEmpty())
		code, out = run(syscall.SIGTERM, errors.New("context canceled"))
		Expect(code).To(Equal(143))
		Expect(out).To(BeEmpty())
	})

	It("still exits 0 for a cancelled prompt: Ctrl+C at an accessible prompt is a SIGINT too", func() {
		code, out := run(syscall.SIGINT, ErrAborted)
		Expect(code).To(Equal(0))
		Expect(out).To(Equal("✗ cancelled — nothing was changed\n"))
	})
})

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
		var err error
		root, err = NewRootCmd(os.Getenv, WithPrompter(h))
		if err != nil {
			t.Fatal(err)
		}
		root.SetArgs([]string{"sync", "-C", os.Getenv("LOLA_SPEC_PROJECT")})
	case "modules":
		root = ask(func() error {
			_, err := h.Modules(ModuleStep{Options: []ModuleOption{{Name: "m", Label: "m"}}})
			return err
		})
	case "hooks":
		root = ask(func() error {
			_, err := h.Consent("Run m's install hooks?")
			return err
		})
	default:
		t.Fatalf("no child %q", child)
	}
	// As main does: the interrupt cancels the run's context too, so the cancelled prompt must win.
	ctx, stop := NotifyContext(context.Background())
	root.SetContext(ctx)
	code := Execute(root)
	stop()
	os.Exit(code)
}
