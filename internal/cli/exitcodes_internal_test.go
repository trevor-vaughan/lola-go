package cli

import (
	"bytes"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// Exit 4 is a decline or cancel, 3 a refusal for want of an answer (end of input included), 0 a
// viewer closed with q.
var _ = Describe("exit codes at questions and the viewer", func() {
	var proj string
	// run runs lola with prompter p's streams, returning its stderr and exit code.
	run := func(p Prompter, args ...string) (string, int) {
		root, err := NewRootCmd(os.Getenv, WithPrompter(p))
		Expect(err).NotTo(HaveOccurred())
		var errb bytes.Buffer
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&errb)
		root.SetArgs(args)
		code := Execute(root)
		return errb.String(), code
	}
	BeforeEach(func() {
		tmp := testenv.Isolate()
		proj = filepath.Join(tmp, "proj")
		mod := filepath.Join(tmp, "example")
		Expect(os.MkdirAll(filepath.Join(mod, "commands"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(mod, "lola.yml"), []byte("name: example\n"), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(mod, "commands", "ship.md"), []byte("Ship\n"), 0o644)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		var errb bytes.Buffer
		root, err := NewRootCmd(os.Getenv, WithPrompter(accessible(&errb)))
		Expect(err).NotTo(HaveOccurred())
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&errb)
		root.SetArgs([]string{"install", mod, "-C", proj, "-t", "claude-code"})
		Expect(Execute(root)).To(Equal(0), errb.String())
	})

	It("a n typed at an accessible question exits 4", func() {
		errs, code := run(accessible(&bytes.Buffer{}, "n"), "uninstall", "example", "-C", proj)
		Expect(code).To(Equal(4), errs)
		Expect(errs).To(HaveSuffix("✗ cancelled — nothing was changed\n"))
		Expect(filepath.Join(proj, ".claude", "commands", "ship.md")).To(BeARegularFile())
	})

	It("a y typed at an accessible question proceeds", func() {
		errs, code := run(accessible(&bytes.Buffer{}, "y"), "uninstall", "example", "-C", proj)
		Expect(code).To(Equal(0), errs)
		Expect(filepath.Join(proj, ".claude", "commands", "ship.md")).NotTo(BeAnExistingFile())
	})

	It("closed stdin at an accessible question exits 3, as a full terminal's does, and changes nothing", func() {
		errs, code := run(accessible(&bytes.Buffer{}), "uninstall", "example", "-C", proj)
		Expect(code).To(Equal(3), errs)
		Expect(errs).To(ContainSubstring("error: input ended before the question was answered\n"))
		Expect(errs).To(ContainSubstring("hint: or pass -y/--yes"))
		Expect(filepath.Join(proj, ".claude", "commands", "ship.md")).To(BeARegularFile())
	})

	It("q in a picker or form exits 4", func() {
		root, err := NewRootCmd(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		root.AddCommand(&cobra.Command{Use: "quit", RunE: func(*cobra.Command, []string) error { return (&huhForms{}).quit() }})
		var errb bytes.Buffer
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&errb)
		root.SetArgs([]string{"quit"})
		Expect(Execute(root)).To(Equal(4))
		Expect(errb.String()).To(Equal("✗ cancelled — nothing was changed\n"))
	})

	It("q in the lola help viewer closes it and exits 0", func() {
		GinkgoT().Setenv("COLUMNS", "80")
		GinkgoT().Setenv("LINES", "10")
		root, err := NewRootCmd(os.Getenv, WithTerminals(true, true))
		Expect(err).NotTo(HaveOccurred())
		var out, errb bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errb)
		root.SetIn(keys("q"))
		root.SetArgs([]string{"help", "install"})
		Expect(Execute(root)).To(Equal(0), errb.String())
		Expect(out.String()).To(ContainSubstring(altScreen), "the viewer opened")
	})
})
