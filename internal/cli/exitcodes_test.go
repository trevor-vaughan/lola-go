package cli_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// Exit 3 is a refusal that needs your answer, 4 a decline or cancel, 1 any other error.
var _ = Describe("exit codes", func() {
	var tmp, proj, mod string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, mod = filepath.Join(tmp, "proj"), filepath.Join(tmp, "example")
		writeTree(mod, map[string]string{"lola.yml": "name: example\n", "commands/ship.md": "Ship\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
	})

	Describe("3, needs your answer", func() {
		It("a sync that needs entries trust off a terminal", func() {
			writeTree(proj, map[string]string{"mods/example/lola.yml": "name: example\n", "mods/example/commands/ship.md": "Ship\n",
				".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: example, source: mods/example}\n"})
			r := lola("sync", "-C", proj)
			Expect(r.code).To(Equal(3), r.err)
			Expect(r.err).To(ContainSubstring("error: lola sync needs your trust"))
			Expect(r.err).To(ContainSubstring("hint: "))
			Expect(filepath.Join(proj, ".claude", "commands", "ship.md")).NotTo(BeAnExistingFile())
		})

		It("a pre-install hook without consent, even under --force", func() {
			writeTree(mod, map[string]string{"lola.yml": "name: example\nhooks: {pre-install: hooks/pre.sh}\n", "hooks/pre.sh": "#!/bin/sh\nexit 0\n"})
			Expect(os.Chmod(filepath.Join(mod, "hooks", "pre.sh"), 0o755)).To(Succeed())
			r := lola("install", mod, "-C", proj, "--force")
			Expect(r.code).To(Equal(3))
			Expect(r.err).To(ContainSubstring("has a pre-install hook, and lola runs hooks only with your consent"))
		})

		It("a conflict without --force", func() {
			writeTree(proj, map[string]string{".claude/commands/ship.md": "mine"})
			r := lola("install", mod, "-C", proj)
			Expect(r.code).To(Equal(3))
			Expect(r.err).To(ContainSubstring("error: refusing to change anything: 1 conflict, which --force overrides"))
			Expect(lola("install", mod, "-C", proj, "--force").code).To(Equal(0), "the hint's flag proceeds")
		})

		It("tidy --all without -y off a terminal", func() {
			writeTree(proj, map[string]string{".claude/agents/z.md": "x\n"})
			r := lola("tidy", "-C", proj, "-t", "claude-code", "--all")
			Expect(r.code).To(Equal(3))
			Expect(r.err).To(ContainSubstring("it cannot ask (off a terminal or with --json), so it needs -y to move them"))
			Expect(filepath.Join(proj, ".claude", "agents", "z.md")).To(BeARegularFile())
		})

		It("uninstall without -y off a terminal", func() {
			Expect(lola("install", mod, "-C", proj).code).To(Equal(0))
			r := lola("uninstall", "example", "-C", proj)
			Expect(r.code).To(Equal(3))
			Expect(r.err).To(ContainSubstring("lola uninstall needs confirmation, and there is no terminal to ask on"))
		})

		It("the same refusal under --json", func() {
			Expect(lola("install", mod, "-C", proj).code).To(Equal(0))
			r := lola("uninstall", "example", "-C", proj, "--json")
			Expect(r.code).To(Equal(3))
			Expect(r.err).To(ContainSubstring(`"error":"lola uninstall needs confirmation, and there is no terminal to ask on"`))
		})
	})

	Describe("4, declined or cancelled", func() {
		It("a No to a question", func() {
			Expect(lola("install", mod, "-C", proj).code).To(Equal(0))
			r := lolaP(&scripted{confirm: []bool{false}}, "uninstall", "example", "-C", proj)
			Expect(r.code).To(Equal(4), r.err)
			Expect(r.err).To(HaveSuffix("✗ cancelled — nothing was changed\n"))
			Expect(filepath.Join(proj, ".claude", "commands", "ship.md")).To(BeARegularFile())
		})

		It("Ctrl+C at a question", func() {
			Expect(lola("install", mod, "-C", proj).code).To(Equal(0))
			r := lolaP(&aborting{&scripted{}}, "uninstall", "example", "-C", proj)
			Expect(r.code).To(Equal(4), r.err)
			Expect(r.err).To(HaveSuffix("✗ cancelled — nothing was changed\n"))
			Expect(filepath.Join(proj, ".claude", "commands", "ship.md")).To(BeARegularFile())
		})
	})

	Describe("1, any other error", func() {
		// stub runs a command that fails with err, through the real root and Execute.
		stub := func(err error) result {
			root, rerr := cli.NewRootCmd(os.Getenv)
			Expect(rerr).NotTo(HaveOccurred())
			root.AddCommand(&cobra.Command{Use: "stub", RunE: func(*cobra.Command, []string) error { return err }})
			var out, errb bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&errb)
			root.SetArgs([]string{"stub"})
			code := cli.Execute(root)
			return result{out: out.String(), err: errb.String(), code: code}
		}

		It("a cache version whose digest is not the recorded one", func() {
			r := stub(&store.DigestError{Name: "kit", ID: "v1", Digest: "sha256:aa", Recorded: true})
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(HavePrefix("error: cached version kit/v1 records digest"))
		})

		It("an error that is not a refusal", func() {
			r := stub(errors.New("fetching kit: connection refused"))
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: fetching kit: connection refused\n"))
		})

		It("an error that wraps a refusal keeps its hints and exits 3", func() {
			r := stub(errs.WithHints(errs.NeedsAnswer(errors.New("needs -y")), "run it with -y"))
			Expect(r.code).To(Equal(3))
			Expect(r.err).To(Equal("error: needs -y\n  hint: run it with -y\n"))
		})

		It("an unknown flag", func() {
			r := lola("sync", "--no-such-flag")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(ContainSubstring("unknown flag: --no-such-flag"))
		})
	})
})
