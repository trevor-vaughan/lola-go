package engine_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/target"
)

var _ = Describe("lola tidy: git, ledgers and targets", func() {
	var t *tidyEnv
	BeforeEach(func() { t = newTidyEnv() })

	It("a git-tracked unit or a unit in a tracked MCP file is locked unless --include-tracked or --force, asking the work tree that holds the destination", func() {
		t.install(t.sc)
		t.files(t.proj, map[string]string{
			".claude/skills/notes/SKILL.md":    "notes\n",
			".claude/skills/new/SKILL.md":      "new\n",
			".claude/skills/:(glob)*/SKILL.md": "magic\n",
		})
		b, err := os.ReadFile(filepath.Join(t.proj, ".mcp.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(t.proj, ".mcp.json"), []byte(`{"mcpServers": {"team": {"command": "team-mcp"}, "lola-gh": `+
			string(mustMember(b, "/mcpServers/lola-gh"))+`}}`), 0o644)).To(Succeed())
		t.commit(t.proj, ".claude/skills/notes/SKILL.md", ".mcp.json")
		res := t.dry(t.sc)
		Expect(listed(res.Candidates)).To(Equal([]string{"claude-code skill/:(glob)*", "claude-code skill/new"}))
		Expect(codes(res.Protected)).To(Equal([]string{"claude-code skill/notes tracked", "claude-code mcp/team tracked"}))
		Expect(res.Protected[0].Reason).To(Equal("tracked by git"))

		for _, req := range []engine.TidyRequest{{Dry: true, IncludeTracked: true}, {Dry: true, Force: true}} {
			res, err := t.tidy(req)
			Expect(err).NotTo(HaveOccurred())
			Expect(listed(res.Candidates)).To(Equal([]string{"claude-code skill/:(glob)*", "claude-code skill/new", "claude-code skill/notes",
				"claude-code mcp/team"}))
		}

		By("at user scope, asking the dotfiles repository a linked ~/.claude lies in, $HOME in no work tree")
		dots := filepath.Join(t.home, "dotfiles")
		t.files(dots, map[string]string{"claude/skills/committed/SKILL.md": "x\n", "claude/skills/fresh/SKILL.md": "x\n"})
		t.commit(dots, "claude/skills/committed/SKILL.md")
		t.link(filepath.Join(dots, "claude"), filepath.Join(t.home, ".claude"))
		res = t.dry(t.user, "claude-code")
		Expect(listed(res.Candidates)).To(Equal([]string{"claude-code skill/fresh"}))
		Expect(codes(res.Protected)).To(Equal([]string{"claude-code skill/committed tracked"}))
	})

	Describe("in a work tree, git that is missing, fails or prints what tidy cannot parse locks every unit as tracked", func() {
		It("at a project whose destination is in a work tree of its own", func() {
			t.install(t.sc)
			claude := filepath.Join(t.proj, ".claude")
			t.files(claude, map[string]string{"skills/old/SKILL.md": "x\n"})
			t.commit(claude, "skills/review/SKILL.md")
			cases := []struct {
				setup  func()
				notice string
			}{
				{func() { t.fakeGit("exit 3") }, "git could not say what it tracks in " + claude + ": git ls-files exited with status 3"},
				{func() { t.fakeGit("printf 'skills/old'") }, "git could not say what it tracks in " + claude + ": git printed what lola cannot read"},
				{t.noGit, "git could not say what it tracks in " + claude + ": git is not on PATH"},
			}
			for _, c := range cases {
				c.setup()
				res := t.dry(t.sc)
				Expect(res.Candidates).To(BeEmpty(), c.notice)
				Expect(codes(res.Protected)).To(Equal([]string{"claude-code skill/old tracked"}), c.notice)
				Expect(res.Protected[0].Reason).To(Equal("git could not say whether it is tracked"))
				Expect(noticeTexts(res.Notices)).To(ContainElement(c.notice))
			}
		})

		It("where lola cannot tell whether a unit is in a work tree at all", func() {
			if runtime.GOOS != "linux" {
				Skip("the fixture relies on Linux's 4096-byte PATH_MAX")
			}
			By("a destination whose path fits but whose .git does not: looking for it fails with ENAMETOOLONG")
			const destLen = 4092 // <dest>/x fits in PATH_MAX; <dest>/.git does not
			proj := t.tmp
			for left := destLen - len("/.claude/commands") - len(proj); left > 0; {
				n := min(left-1, 200)
				if left-1-n > 0 && left-1-n < 2 {
					n -= 2 // leave room for a final component of at least one byte
				}
				proj = filepath.Join(proj, strings.Repeat("d", n))
				left -= n + 1
			}
			dest := filepath.Join(proj, ".claude", "commands")
			Expect(dest).To(HaveLen(destLen))
			Expect(os.MkdirAll(dest, 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dest, "x"), []byte("x\n"), 0o644)).To(Succeed())
			sc := engine.Scope{Kind: target.Project, ProjectDir: proj}
			res := t.dry(sc, "claude-code")
			Expect(res.Candidates).To(BeEmpty())
			Expect(codes(res.Protected)).To(Equal([]string{"claude-code command/x tracked"}))
			Expect(res.Protected[0].Reason).To(Equal("git could not say whether it is tracked"))
			_, err := t.e.Tidy(tidyCtx, engine.TidyRequest{Scope: sc, Targets: []string{"claude-code"}, Select: pats("command/x"), Yes: true})
			Expect(err).To(MatchError(HavePrefix("git could not say whether command/x is tracked: lola cannot tell whether it is in a git work tree: ")))
			Expect(filepath.Join(dest, "x")).To(BeARegularFile())
		})

		It("in a user-scope $HOME work tree, and in the dotfiles repository a linked ~/.claude lies in, whose links on the way skip their destinations", func() {
			By("$HOME a work tree")
			t.files(t.home, map[string]string{".claude/skills/a/SKILL.md": "x\n", ".claude.json": `{"mcpServers": {"m": {}}}`})
			t.git(t.home, "init", "-q")
			t.fakeGit("exit 1")
			res := t.dry(t.user, "claude-code")
			Expect(res.Candidates).To(BeEmpty())
			Expect(codes(res.Protected)).To(Equal([]string{"claude-code skill/a tracked", "claude-code mcp/m tracked"}))
			Expect(os.RemoveAll(filepath.Join(t.home, ".git"))).To(Succeed())
			Expect(os.RemoveAll(filepath.Join(t.home, ".claude"))).To(Succeed())
			Expect(os.Remove(filepath.Join(t.tmp, "bin", "git"))).To(Succeed())

			By("a dotfiles repository, $HOME in no work tree")
			dots := filepath.Join(t.home, "dotfiles")
			t.files(dots, map[string]string{"claude/skills/b/SKILL.md": "x\n", "agents-real/scout.md": "x\n"})
			t.link("../agents-real", filepath.Join(dots, "claude", "agents"))
			t.commit(dots, "claude/skills/b/SKILL.md")
			t.link(filepath.Join(dots, "claude"), filepath.Join(t.home, ".claude"))
			t.fakeGit("exit 1")
			res = t.dry(t.user, "claude-code")
			Expect(listed(res.Candidates)).To(Equal([]string{"claude-code mcp/m"}), "~/.claude.json is in no work tree")
			Expect(codes(res.Protected)).To(Equal([]string{"claude-code skill/b tracked"}))
			Expect(codes(res.Skipped)).To(Equal([]string{"claude-code agent/ tracked_link"}))
			Expect(res.Skipped[0].Reason).To(Equal("git could not say whether its path passes through a symbolic link git tracks"))
		})
	})

	It("an unreadable lock.json, a tracked one, or one git cannot check refuses tidy, also with --force", func() {
		t.install(t.sc)
		t.files(t.proj, map[string]string{".claude/skills/old/SKILL.md": "x\n"})
		ledgerPath := filepath.Join(t.proj, ".lola", "lock.json")
		good, err := os.ReadFile(ledgerPath)
		Expect(err).NotTo(HaveOccurred())
		reqs := []engine.TidyRequest{{All: true, Yes: true}, {All: true, Force: true}, {Dry: true}}

		By("unreadable")
		Expect(os.WriteFile(ledgerPath, []byte("{"), 0o644)).To(Succeed())
		for _, req := range reqs {
			_, err := t.tidy(req)
			Expect(err).To(MatchError(ContainSubstring("lock.json")))
		}
		Expect(os.WriteFile(ledgerPath, good, 0o644)).To(Succeed())

		By("git cannot check")
		t.git(t.proj, "init", "-q")
		path := os.Getenv("PATH")
		t.noGit()
		for _, req := range reqs {
			_, err := t.tidy(req)
			Expect(err).To(MatchError("lola could not check whether git tracks .lola/lock.json: git is not on PATH; lola tidy needs to know"))
			Expect(errs.Hints(err)).To(BeEmpty())
		}
		GinkgoT().Setenv("PATH", path)

		By("tracked")
		t.commit(t.proj, ".lola/lock.json")
		for _, req := range reqs {
			_, err := t.tidy(req)
			Expect(err).To(MatchError(ContainSubstring(".lola/lock.json is tracked by git")))
		}
		Expect(filepath.Join(t.proj, ".claude", "skills", "old")).To(BeADirectory())
	})

	It("project targets come from project.yml only as far as trust covers them", func() {
		t.install(t.sc)
		t.files(t.proj, map[string]string{".gemini/commands/old.toml": "x\n", ".claude/commands/old.md": "x\n"})
		res := t.dry(t.sc)
		Expect(res.Targets).To(Equal([]string{"claude-code"}))

		By("a pulled project.yml that adds gemini-cli")
		man, err := manifest.Load(filepath.Join(t.proj, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(man.Targets).To(Equal([]string{"claude-code"}))
		b, err := os.ReadFile(filepath.Join(t.proj, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(t.proj, ".lola", "project.yml"),
			[]byte(strings.Replace(string(b), "claude-code", "claude-code, gemini-cli", 1)), 0o644)).To(Succeed())
		res = t.dry(t.sc)
		Expect(res.Targets).To(Equal([]string{"claude-code"}))
		Expect(listed(res.Candidates)).To(Equal([]string{"claude-code command/old"}))
		Expect(noticeTexts(res.Notices)).To(ContainElement("project.yml's targets changed since you trusted them; lola sync --project reviews the change"))

		By("-t names it")
		res = t.dry(t.sc, "gemini-cli")
		Expect(listed(res.Candidates)).To(Equal([]string{"gemini-cli command/old"}))
	})

	It("a project whose destinations are also the user scope's honors both ledgers", func() {
		t.install(t.user)
		home := engine.Scope{Kind: t.sc.Kind, ProjectDir: t.home}
		t.e.Getwd = func() (string, error) { return t.home, nil }
		t.files(t.home, map[string]string{".claude/skills/old/SKILL.md": "x\n"})
		res, err := t.e.Tidy(tidyCtx, engine.TidyRequest{Scope: home, Dry: true, Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed(res.Candidates)).To(Equal([]string{"claude-code skill/old"}), "the user scope's review, ship, critic and lola-gh are owned")

		res, err = t.e.Tidy(tidyCtx, engine.TidyRequest{Scope: home, All: true, Yes: true, Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed(res.Moved)).To(Equal([]string{"claude-code skill/old"}))
		Expect(filepath.Join(t.home, ".claude", "skills", "review", "SKILL.md")).To(BeAnExistingFile())
	})

	It("a project destination holding a user-scope destination honors the user ledger too", func() {
		shared := filepath.Join(t.proj, ".claude", "skills", "shared")
		Expect(os.MkdirAll(shared, 0o755)).To(Succeed())
		t.link(shared, filepath.Join(t.home, ".claude", "skills"))
		t.install(t.user)
		Expect(filepath.Join(shared, "review", "SKILL.md")).To(BeAnExistingFile())
		t.files(t.proj, map[string]string{".claude/skills/old/SKILL.md": "x\n"})
		res := t.dry(t.sc, "claude-code")
		Expect(listed(res.Candidates)).To(Equal([]string{"claude-code skill/old"}), "shared holds what the user scope installed")
	})
})

// mustMember is the canonical JSON of the member at ptr in b.
func mustMember(b []byte, ptr string) []byte {
	v, found, err := fsx.JSONGet(b, ptr)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue(), ptr)
	return v
}
