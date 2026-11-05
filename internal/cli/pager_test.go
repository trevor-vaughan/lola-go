package cli_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// pagedFirst is a terminal Prompter that records, at each trust question, whether the fake pager
// had already run.
type pagedFirst struct {
	*confirming
	paged string
	seen  []bool
}

func (p *pagedFirst) Trust(st cli.TrustStep) (engine.TrustAnswer, error) {
	_, err := os.Stat(p.paged)
	p.seen = append(p.seen, err == nil)
	return p.confirming.Trust(st)
}

var _ = Describe("the consent-diff pager (Plan 6b §9)", func() {
	var tmp, proj, paged string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(tmp, "proj")
		paged = filepath.Join(tmp, "paged")
		writeTree(filepath.Join(proj, "mods", "kit"), map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Kit\n"})
		files := map[string]string{"lola.yml": "name: big\n"}
		for i := range 25 {
			files[fmt.Sprintf("commands/c%02d.md", i)] = "C\n"
		}
		writeTree(filepath.Join(proj, "mods", "big"), files)
		r := lola("install", filepath.Join(proj, "mods", "kit"), "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		b, err := os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(proj, ".lola", "project.yml"), append(b, "  - name: big\n    source: mods/big\n"...), 0o644)).To(Succeed())
		// The fake pager records what it was given, its arguments and LESS, and exits
		// $PAGER_EXIT.
		Expect(os.WriteFile(filepath.Join(tmp, "bin", "fakepager"), []byte("#!/bin/sh\ncat > \"$PAGED\"\necho \"$@\" > \"$PAGED.args\"\n"+
			"echo \"LESS=$LESS\" > \"$PAGED.env\"\nexit ${PAGER_EXIT:-0}\n"), 0o755)).To(Succeed())
		GinkgoT().Setenv("PAGED", paged)
		GinkgoT().Setenv("LOLA_PAGER", "fakepager --flag")
		GinkgoT().Chdir(proj)
	})
	// askingOn runs lola with a trust question that cancels, on a terminal, stdout and stderr
	// terminals as outTTY and errTTY say.
	askingOn := func(outTTY, errTTY bool, args ...string) (*pagedFirst, result) {
		p := &pagedFirst{confirming: &confirming{trusts: []engine.TrustAnswer{{}}}, paged: paged}
		root := cli.NewRootCmd(os.Getenv, cli.WithPrompter(p), cli.WithTerminals(outTTY, errTTY))
		var out bytes.Buffer
		p.stderr = &bytes.Buffer{}
		root.SetOut(&out)
		root.SetErr(p.stderr)
		root.SetIn(strings.NewReader(""))
		root.SetArgs(args)
		code := cli.Execute(root)
		return p, result{out: out.String(), err: p.stderr.String(), code: code}
	}
	asking := func(tty bool, args ...string) (*pagedFirst, result) { return askingOn(tty, tty, args...) }
	const header = ".lola/project.yml changed since lola last wrote it or you last trusted it:\n"

	It("the question comes after the pager exits", func() {
		p, r := asking(true, "sync", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.seen).To(Equal([]bool{true}), "the pager ran and exited before the question")
		Expect(p.before[0]).NotTo(ContainSubstring(header), "the diff went to the pager, not stderr")
		got, err := os.ReadFile(paged)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(HavePrefix(header))
		Expect(string(got)).To(ContainSubstring("+ commands/c24.md\n"), "paged output is uncapped")
		Expect(string(got)).NotTo(ContainSubstring("… and"))
		Expect(os.ReadFile(paged+".args")).To(Equal([]byte("--flag\n")), "split on whitespace, no shell")
		Expect(os.ReadFile(paged+".env")).To(Equal([]byte("LESS=FRX\n")), "LESS=FRX when LESS is unset")
		Expect(filepath.Join(proj, ".claude", "commands", "c00.md")).NotTo(BeAnExistingFile())

		GinkgoT().Setenv("LESS", "R")
		Expect(os.Remove(paged)).To(Succeed())
		p, _ = asking(true, "sync", "--project")
		Expect(p.seen).To(Equal([]bool{true}))
		Expect(os.ReadFile(paged+".env")).To(Equal([]byte("LESS=R\n")), "the user's LESS is kept")
	})

	It("opens the diff in the viewer, in full, before the question, when $LOLA_PAGER is not set (Plan 10 §2.1)", func() {
		GinkgoT().Setenv("LOLA_PAGER", "")
		GinkgoT().Setenv("COLUMNS", "80")
		GinkgoT().Setenv("LINES", "10")
		// A terminal's input: q, then nothing more until the spec ends.
		in, typed, err := os.Pipe()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			Expect(typed.Close()).To(Succeed())
			Expect(in.Close()).To(Succeed())
		})
		_, err = typed.WriteString("q")
		Expect(err).NotTo(HaveOccurred())
		p := &pagedFirst{confirming: &confirming{trusts: []engine.TrustAnswer{{}}}, paged: paged}
		root := cli.NewRootCmd(os.Getenv, cli.WithPrompter(p), cli.WithTerminals(true, true))
		var out bytes.Buffer
		p.stderr = &bytes.Buffer{}
		root.SetOut(&out)
		root.SetErr(p.stderr)
		root.SetIn(in)
		root.SetArgs([]string{"sync", "--project"})
		Expect(cli.Execute(root)).To(Equal(0), p.stderr.String())
		Expect(out.String()).To(ContainSubstring("\x1b[?1049h"), "the viewer opened")
		Expect(out.String()).To(ContainSubstring(header))
		Expect(out.String()).To(ContainSubstring("+ commands/c24.md\n"), "the viewer's text is uncapped")
		Expect(out.String()).NotTo(ContainSubstring("… and"))
		Expect(p.before[0]).NotTo(ContainSubstring(header), "the diff went to the viewer, not stderr")
		Expect(p.questions).To(Equal([]string{"trust"}), "the question comes after the viewer")
		Expect(paged).NotTo(BeAnExistingFile())
	})

	It("pages a dry run's diff and plan, uncapped", func() {
		_, r := asking(true, "sync", "--project", "-n")
		Expect(r.code).To(Equal(0), r.err)
		got, err := os.ReadFile(paged)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(ContainSubstring("plan (dry run; nothing was written):"))
		Expect(string(got)).NotTo(ContainSubstring("… and"))
	})

	DescribeTable("never pages",
		func(tty bool, setup func(), args ...string) {
			if setup != nil {
				setup()
			}
			_, r := asking(tty, append([]string{"sync", "--project"}, args...)...)
			Expect(paged).NotTo(BeAnExistingFile())
			Expect(r.err).NotTo(ContainSubstring("⚠"))
		},
		Entry("with --no-pager", true, nil, "--no-pager"),
		Entry("with NO_PAGER set, though $LOLA_PAGER is (Plan 10 §2.1)", true, func() { GinkgoT().Setenv("NO_PAGER", "1") }),
		Entry("with pager: false in config.yaml", true, func() {
			Expect(os.MkdirAll(filepath.Join(tmp, "config", "lola"), 0o700)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(tmp, "config", "lola", "config.yaml"), []byte("version: 1\npager: false\n"), 0o600)).To(Succeed())
		}),
		Entry("under --json", true, nil, "--json"),
		Entry("under --plain", true, nil, "--plain"),
		Entry("off a terminal", false, nil),
		Entry("when the pager is cat", true, func() { GinkgoT().Setenv("LOLA_PAGER", "cat") }),
	)

	It("never pages when stderr is not a terminal, printing the diff there unpaged", func() {
		p, r := askingOn(true, false, "sync", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(paged).NotTo(BeAnExistingFile())
		Expect(p.before[0]).To(ContainSubstring(header))
		Expect(p.before[0]).To(ContainSubstring("… and 5 more"))
	})

	It("takes a pager that exits 0 without reading a diff larger than a pipe holds", func() {
		files := map[string]string{"lola.yml": "name: big\n"}
		for i := range 1200 {
			files[fmt.Sprintf("commands/c%04d%s.md", i, strings.Repeat("x", 50))] = "C\n"
		}
		writeTree(filepath.Join(proj, "mods", "big"), files) // about 90 KiB of diff; a pipe holds 64 KiB
		Expect(os.WriteFile(filepath.Join(tmp, "bin", "quitter"), []byte("#!/bin/sh\nexit 0\n"), 0o755)).To(Succeed())
		GinkgoT().Setenv("LOLA_PAGER", "quitter")
		p, r := asking(true, "sync", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).NotTo(ContainSubstring("⚠"))
		Expect(p.questions).To(Equal([]string{"trust"}), "the question is still asked")
	})

	// A pager quit with Ctrl+C or q that exits non-zero takes the failing-pager path below
	// (PAGER_EXIT), as does one that dies of a signal.
	It("prints the diff unpaged, capped, before the question when the pager fails", func() {
		GinkgoT().Setenv("PAGER_EXIT", "3")
		p, r := asking(true, "sync", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.before[0]).To(ContainSubstring("⚠ the pager fakepager failed: exit status 3; showing it here\n" + header))
		Expect(p.before[0]).To(ContainSubstring("… and 5 more (lola sync --project -n --json lists them all)\n"))

		GinkgoT().Setenv("LOLA_PAGER", "no-such-pager")
		p, _ = asking(true, "sync", "--project")
		Expect(p.before[0]).To(ContainSubstring("⚠ not paging: the pager no-such-pager was not found; showing it here\n" + header))
	})

	It("refuses a pager found in the current directory", func() {
		Expect(os.WriteFile(filepath.Join(proj, "evilpager"), []byte("#!/bin/sh\ntouch \"$PAGED.evil\"\n"), 0o755)).To(Succeed())
		GinkgoT().Setenv("PATH", "."+string(os.PathListSeparator)+os.Getenv("PATH"))
		GinkgoT().Setenv("LOLA_PAGER", "evilpager")
		p, r := asking(true, "sync", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(paged + ".evil").NotTo(BeAnExistingFile())
		Expect(p.before[0]).To(ContainSubstring("⚠ not paging: the pager evilpager is in the current directory, " +
			"which lola never runs a program from; showing it here\n" + header))
	})
})
