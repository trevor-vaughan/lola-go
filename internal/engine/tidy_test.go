package engine_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// tidyEnv is one isolated environment for lola tidy specs: a project with the example module
// installed for claude-code, whose residue each spec adds.
type tidyEnv struct {
	tmp, proj, home string
	p               paths.Paths
	e               *engine.Engine
	sc, user        engine.Scope
}

var tidyCtx = context.Background()

func newTidyEnv() *tidyEnv {
	tmp, err := filepath.EvalSymlinks(testenv.Isolate())
	Expect(err).NotTo(HaveOccurred())
	p, err := paths.Resolve(os.Getenv)
	Expect(err).NotTo(HaveOccurred())
	t := &tidyEnv{tmp: tmp, proj: filepath.Join(tmp, "proj"), home: p.Home, p: p, e: engine.New(p)}
	Expect(os.MkdirAll(t.proj, 0o755)).To(Succeed())
	t.e.Getwd = func() (string, error) { return t.proj, nil }
	t.sc = engine.Scope{Kind: target.Project, ProjectDir: t.proj}
	t.user = engine.Scope{Kind: target.User}
	writeTree(filepath.Join(tmp, "src", "example"), exampleModule)
	return t
}

// install installs the example module at sc for claude-code.
func (t *tidyEnv) install(sc engine.Scope) {
	_, err := t.e.Install(tidyCtx, sc, t.proj, []engine.InstallRequest{{Arg: filepath.Join(t.tmp, "src", "example")}},
		engine.Options{Targets: []string{"claude-code"}})
	Expect(err).NotTo(HaveOccurred())
}

// files writes files under root.
func (t *tidyEnv) files(root string, files map[string]string) { writeTree(root, files) }

// link makes a symbolic link at root/rel pointing at to.
func (t *tidyEnv) link(to, at string) {
	Expect(os.MkdirAll(filepath.Dir(at), 0o755)).To(Succeed())
	Expect(os.Symlink(to, at)).To(Succeed())
}

// tidy runs lola tidy at the project with req, off a terminal unless req says otherwise.
func (t *tidyEnv) tidy(req engine.TidyRequest) (*engine.TidyResult, error) {
	if req.Scope == (engine.Scope{}) {
		req.Scope = t.sc
	}
	return t.e.Tidy(tidyCtx, req)
}

// dry is a dry run at sc listing every candidate.
func (t *tidyEnv) dry(sc engine.Scope, targets ...string) *engine.TidyResult {
	res, err := t.e.Tidy(tidyCtx, engine.TidyRequest{Scope: sc, Dry: true, Targets: targets})
	Expect(err).NotTo(HaveOccurred())
	return res
}

// reroot points the XDG base directory key at dir and gives the spec a new engine over it.
func (t *tidyEnv) reroot(key, dir string) {
	GinkgoT().Setenv(key, dir)
	p, err := paths.Resolve(os.Getenv)
	Expect(err).NotTo(HaveOccurred())
	t.p, t.e = p, engine.New(p)
	t.e.Getwd = func() (string, error) { return t.proj, nil }
}

// git runs git in dir.
func (t *tidyEnv) git(dir string, args ...string) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))
}

// commit makes dir a git work tree and commits paths.
func (t *tidyEnv) commit(dir string, paths ...string) {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.git(dir, "init", "-q")
	}
	t.git(dir, append([]string{"--literal-pathspecs", "add", "-f", "--"}, paths...)...)
	t.git(dir, "commit", "-q", "-m", "x")
}

// fakeGit puts a git that runs script first on PATH.
func (t *tidyEnv) fakeGit(script string) {
	Expect(os.WriteFile(filepath.Join(t.tmp, "bin", "git"), []byte("#!/bin/sh\n"+script+"\n"), 0o755)).To(Succeed())
}

// noGit leaves only the spec's own bin directory on PATH.
func (t *tidyEnv) noGit() {
	Expect(os.RemoveAll(filepath.Join(t.tmp, "bin", "git"))).To(Succeed())
	GinkgoT().Setenv("PATH", filepath.Join(t.tmp, "bin"))
}

// pats parses selectors.
func pats(ss ...string) []config.Pattern {
	var out []config.Pattern
	for _, s := range ss {
		p, err := config.ParsePattern(s)
		Expect(err).NotTo(HaveOccurred(), s)
		out = append(out, p)
	}
	return out
}

// listed is each item as "target kind/name".
func listed(us []engine.TidyUnit) []string {
	out := []string{}
	for _, u := range us {
		out = append(out, u.Target+" "+u.Kind+"/"+u.Name)
	}
	return out
}

// codes is each item as "target kind/name code".
func codes(us []engine.TidyUnit) []string {
	out := []string{}
	for _, u := range us {
		out = append(out, u.Target+" "+u.Kind+"/"+u.Name+" "+u.Code)
	}
	return out
}

// noticeTexts are the notices' texts.
func noticeTexts(ns []engine.Notice) []string {
	out := []string{}
	for _, n := range ns {
		out = append(out, n.Text)
	}
	return out
}

// refusal is the tidy result a refusal carries.
func refusal(err error) *engine.TidyResult {
	GinkgoHelper()
	te, ok := errors.AsType[*engine.TidyError](err)
	Expect(ok).To(BeTrue(), "%v", err)
	return te.Result
}

// never fails the spec when lola asks anything.
func never() (func(engine.TidyScan) (engine.TidyChoice, error), func(engine.TidyChoice) (bool, error)) {
	return func(engine.TidyScan) (engine.TidyChoice, error) {
			Fail("lola tidy ran the picker")
			return engine.TidyChoice{}, nil
		}, func(engine.TidyChoice) (bool, error) {
			Fail("lola tidy asked for confirmation")
			return false, nil
		}
}

// tidyWords splits a command line errs.ShellJoin wrote: words separated by spaces, '…' quoted.
func tidyWords(line string) []string {
	var out []string
	var cur strings.Builder
	quoted, inWord := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\'':
			quoted, inWord = !quoted, true
		case c == ' ' && !quoted:
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
			}
			inWord = false
		case c == '\\' && !quoted && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
			inWord = true
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out
}

// runHint runs a hint lola printed in the spec's environment, off a terminal (§12 "a hint that
// works"): a lola tidy run, or a lola uninstall. It fails the spec on --json or -n, which a hint
// that acts never carries, and returns what the run returned.
func (t *tidyEnv) runHint(hint string) (*engine.TidyResult, error) {
	GinkgoHelper()
	w := tidyWords(hint)
	Expect(len(w)).To(BeNumerically(">=", 2), hint)
	Expect(w[0]).To(Equal("lola"), hint)
	req := engine.TidyRequest{Scope: t.sc}
	var names []string
	scope := func(i int) int {
		switch w[i] {
		case "--project":
			req.Scope = engine.Scope{Kind: target.Project, ProjectDir: engine.FindProject(t.proj)}
		case "--user":
			req.Scope = t.user
		case "-C":
			req.Scope = engine.Scope{Kind: target.Project, ProjectDir: w[i+1]}
			return i + 1
		default:
			return -1
		}
		return i
	}
	if w[1] == "uninstall" {
		for i := 2; i < len(w); i++ {
			if j := scope(i); j >= 0 {
				i = j
				continue
			}
			names = append(names, w[i])
		}
		_, err := t.e.Uninstall(tidyCtx, req.Scope, names, engine.Options{})
		return nil, err
	}
	Expect(w[1]).To(Equal("tidy"), hint)
	for i := 2; i < len(w); i++ {
		if j := scope(i); j >= 0 {
			i = j
			continue
		}
		switch w[i] {
		case "--json":
			Fail("a hint carries --json: " + hint)
		case "-n", "--dry-run":
			req.Dry = true
		case "-t":
			i++
			req.Targets = append(req.Targets, w[i])
		case "--include-tracked":
			req.IncludeTracked = true
		case "--keep":
			i++
			req.Keep = append(req.Keep, pats(w[i])...)
		case "--unkeep":
			i++
			req.Unkeep = append(req.Unkeep, pats(w[i])...)
		case "--all":
			req.All = true
		case "-y":
			req.Yes = true
		case "--force":
			req.Force = true
		default:
			Expect(w[i]).NotTo(HavePrefix("-"), hint)
			names = append(names, w[i])
		}
	}
	req.Select = pats(names...)
	return t.e.Tidy(tidyCtx, req)
}

var _ = Describe("lola tidy: what it sees", func() {
	var t *tidyEnv
	BeforeEach(func() {
		t = newTidyEnv()
		t.install(t.sc)
	})

	It("tidy offers only unowned direct entries of the scope's declared destinations", func() {
		t.files(t.proj, map[string]string{
			".claude/skills/old/SKILL.md":        "old\n",
			".claude/skills/old/ref/a.md":        "a\n",
			".claude/commands/tmp.md":            "tmp\n",
			".claude/commands/notes.txt":         "notes\n",
			".claude/agents/scout.md":            "scout\n",
			".claude/rules/lola/gone/style.md":   "rule\n",
			".claude/settings.json":              "{}\n",
			".claude/notes.md":                   "notes\n",
			"stray.md":                           "stray\n",
			".gemini/commands/residue.toml":      "x\n",
			".mcp.json.bak":                      "{}\n",
			".claude/skills/review/extra/new.md": "new\n",
		})
		res := t.dry(t.sc)
		Expect(listed(res.Candidates)).To(Equal([]string{
			"claude-code skill/old", "claude-code command/notes.txt", "claude-code command/tmp", "claude-code agent/scout",
			"claude-code rule/gone"}))
		Expect(res.Targets).To(Equal([]string{"claude-code"}))
		Expect(res.Candidates[0].Path).To(Equal(filepath.Join(t.proj, ".claude", "skills", "old")))
		Expect(res.Candidates[0].Rel).To(Equal(".claude/skills/old"))
		Expect(res.Protected).To(BeEmpty())
		Expect(res.Moved).To(BeEmpty())
	})

	It("an entry holding anything lola owns is never offered, and never entered", func() {
		t.files(t.proj, map[string]string{".claude/skills/review/stray.md": "stray\n", ".claude/skills/review/CLAUDE.md": "x\n"})
		locked := filepath.Join(t.proj, ".claude", "skills", "review", "locked")
		Expect(os.MkdirAll(locked, 0o755)).To(Succeed())
		Expect(os.Chmod(locked, 0)).To(Succeed())
		DeferCleanup(os.Chmod, locked, os.FileMode(0o755))
		res := t.dry(t.sc)
		Expect(res.Candidates).To(BeEmpty())
		Expect(res.Protected).To(BeEmpty(), "nothing below an owned skill is walked, so nothing there locks it")
		Expect(res.Skipped).To(BeEmpty())
	})

	It("ownership fails closed: an installed unit whose artifacts no longer resolve is not offered, and user destinations that do not resolve refuse the run", func() {
		review := filepath.Join(t.proj, ".claude", "skills", "review")
		Expect(os.RemoveAll(review)).To(Succeed())
		Expect(os.WriteFile(review, []byte("x\n"), 0o644)).To(Succeed())
		res := t.dry(t.sc)
		Expect(listed(res.Candidates)).NotTo(ContainElement("claude-code skill/review"))
		_, err := t.tidy(engine.TidyRequest{Select: pats("skill/review"), Yes: true})
		Expect(err).To(MatchError("skill/review is installed by module example; lola tidy only moves what lola does not manage"))
		Expect(review).To(BeARegularFile())

		By("a user-scope destination that does not resolve")
		Expect(os.RemoveAll(filepath.Join(t.home, ".claude"))).To(Succeed())
		Expect(os.WriteFile(filepath.Join(t.home, ".claude"), []byte("x\n"), 0o644)).To(Succeed())
		t.files(t.proj, map[string]string{".claude/skills/old/SKILL.md": "x\n"})
		_, err = t.tidy(engine.TidyRequest{All: true, Yes: true})
		Expect(err).To(MatchError(ContainSubstring("cannot resolve " + filepath.Join(t.home, ".claude"))))
		Expect(filepath.Join(t.proj, ".claude", "skills", "old")).To(BeADirectory())
	})

	It("a link unit's ownership is decided through its parent, never through the link", func() {
		t.link("review", filepath.Join(t.proj, ".claude", "skills", "alias"))
		t.link("ship.md", filepath.Join(t.proj, ".claude", "commands", "again.md"))
		res := t.dry(t.sc)
		Expect(listed(res.Candidates)).To(Equal([]string{"claude-code skill/alias", "claude-code command/again"}))
		Expect(res.Candidates[0].Path).To(Equal(filepath.Join(t.proj, ".claude", "skills", "alias")))
		Expect(res.Candidates[0].Link).To(Equal("review"))
	})

	It("rules are tidied only inside lola's own namespace directory", func() {
		t.files(t.proj, map[string]string{".claude/rules/mine.md": "mine\n", ".claude/rules/team/x.md": "x\n",
			".claude/rules/lola/old/x.md": "x\n"})
		Expect(listed(t.dry(t.sc).Candidates)).To(Equal([]string{"claude-code rule/old"}))
	})

	It("a destination whose real path leaves the root is skipped with a notice; one linked inside the root is scanned", func() {
		elsewhere := filepath.Join(t.tmp, "elsewhere")
		t.files(elsewhere, map[string]string{"scout.md": "scout\n"})
		Expect(os.RemoveAll(filepath.Join(t.proj, ".claude", "agents"))).To(Succeed())
		t.link(elsewhere, filepath.Join(t.proj, ".claude", "agents"))
		t.files(t.proj, map[string]string{"shared/rules/old/x.md": "x\n"})
		Expect(os.RemoveAll(filepath.Join(t.proj, ".claude", "rules", "lola"))).To(Succeed())
		t.link("../../shared/rules", filepath.Join(t.proj, ".claude", "rules", "lola"))
		res := t.dry(t.sc)
		Expect(listed(res.Candidates)).To(Equal([]string{"claude-code rule/old"}))
		Expect(codes(res.Skipped)).To(Equal([]string{"claude-code agent/ link_out"}))
		Expect(res.Skipped[0].Path).To(Equal(filepath.Join(t.proj, ".claude", "agents")))
		Expect(noticeTexts(res.Notices)).To(ContainElement(".claude/agents leads outside " + t.proj + "; lola tidy skips it"))
	})

	DescribeTable("a destination that resolves onto the root, an ancestor, another destination, .git, .lola or lola's own directories is skipped whole",
		func(setup func(t *tidyEnv) (dest string), want []string) {
			t.files(t.proj, map[string]string{".claude/skills/old/SKILL.md": "old\n"})
			dest := setup(t)
			res := t.dry(t.sc)
			Expect(codes(res.Skipped)).To(Equal(want))
			for _, c := range res.Candidates {
				Expect(c.Rel).To(HavePrefix(".claude/skills/"), "nothing from %s is offered", dest)
			}
			Expect(noticeTexts(res.Notices)).To(ContainElement(ContainSubstring("lola tidy skips it")))
		},
		Entry(".claude/commands -> .. and .claude/agents -> ..", func(t *tidyEnv) string {
			t.files(t.proj, map[string]string{".env": "SECRET=1\n"})
			for _, d := range []string{"commands", "agents"} {
				Expect(os.RemoveAll(filepath.Join(t.proj, ".claude", d))).To(Succeed())
				t.link("..", filepath.Join(t.proj, ".claude", d))
			}
			return ".claude/commands"
		}, []string{"claude-code command/ link_overlap", "claude-code agent/ link_overlap"}),
		Entry("an ancestor of the root", func(t *tidyEnv) string {
			Expect(os.RemoveAll(filepath.Join(t.proj, ".claude", "agents"))).To(Succeed())
			t.link("../..", filepath.Join(t.proj, ".claude", "agents"))
			return ".claude/agents"
		}, []string{"claude-code agent/ link_overlap"}),
		Entry("another destination", func(t *tidyEnv) string {
			Expect(os.RemoveAll(filepath.Join(t.proj, ".claude", "agents"))).To(Succeed())
			t.link("rules/lola", filepath.Join(t.proj, ".claude", "agents"))
			return ".claude/agents"
		}, []string{"claude-code agent/ link_overlap", "claude-code rule/ link_overlap"}),
		Entry("inside .git", func(t *tidyEnv) string {
			t.git(t.proj, "init", "-q")
			Expect(os.RemoveAll(filepath.Join(t.proj, ".claude", "agents"))).To(Succeed())
			t.link("../.git/hooks", filepath.Join(t.proj, ".claude", "agents"))
			return ".claude/agents"
		}, []string{"claude-code agent/ link_overlap"}),
		Entry("onto .lola", func(t *tidyEnv) string {
			Expect(os.RemoveAll(filepath.Join(t.proj, ".claude", "agents"))).To(Succeed())
			t.link("../.lola", filepath.Join(t.proj, ".claude", "agents"))
			return ".claude/agents"
		}, []string{"claude-code agent/ link_overlap"}),
		Entry("into lola's trash", func(t *tidyEnv) string {
			t.reroot("XDG_STATE_HOME", filepath.Join(t.proj, "state"))
			t.files(t.p.Trash, map[string]string{"20260101T000000Z/manifest.txt": ""})
			Expect(os.RemoveAll(filepath.Join(t.proj, ".claude", "agents"))).To(Succeed())
			t.link("../state/lola/trash", filepath.Join(t.proj, ".claude", "agents"))
			return ".claude/agents"
		}, []string{"claude-code agent/ link_overlap"}),
		Entry("an ancestor of lola's real config directory, $XDG_CONFIG_HOME itself a link", func(t *tidyEnv) string {
			real := filepath.Join(t.proj, "dotfiles", "config")
			t.files(real, map[string]string{"lola/keep.yaml": "version: 1\n"})
			t.link(real, filepath.Join(t.tmp, "linked-config"))
			t.reroot("XDG_CONFIG_HOME", filepath.Join(t.tmp, "linked-config"))
			Expect(os.RemoveAll(filepath.Join(t.proj, ".claude", "agents"))).To(Succeed())
			t.link("../dotfiles", filepath.Join(t.proj, ".claude", "agents"))
			return ".claude/agents"
		}, []string{"claude-code agent/ link_overlap"}),
		Entry("into the gitdir: of a .git file", func(t *tidyEnv) string {
			t.git(t.tmp, "init", "-q", "--separate-git-dir", filepath.Join(t.proj, ".gitdata"), t.proj)
			Expect(os.RemoveAll(filepath.Join(t.proj, ".claude", "agents"))).To(Succeed())
			t.link("../.gitdata/hooks", filepath.Join(t.proj, ".claude", "agents"))
			return ".claude/agents"
		}, []string{"claude-code agent/ link_overlap"}),
	)

	It("a committed link on the way skips its destination", func() {
		t.files(t.proj, map[string]string{"team-claude/commands/old.md": "old\n"})
		Expect(os.RemoveAll(filepath.Join(t.proj, ".claude", "commands"))).To(Succeed())
		t.link("../team-claude/commands", filepath.Join(t.proj, ".claude", "commands"))
		t.commit(t.proj, ".claude/commands")
		res := t.dry(t.sc)
		Expect(codes(res.Skipped)).To(Equal([]string{"claude-code command/ tracked_link"}))
		Expect(res.Skipped[0].Reason).To(Equal("its path passes through a symbolic link git tracks"))
		Expect(listed(res.Candidates)).NotTo(ContainElement("claude-code command/old"))
	})

	It("units named .git or .lola are locked", func() {
		t.files(t.proj, map[string]string{".claude/skills/.git/HEAD": "ref\n", ".claude/commands/.LOLA": "x\n", ".claude/skills/.Lola/x": "x\n"})
		res := t.dry(t.sc)
		Expect(res.Candidates).To(BeEmpty())
		Expect(codes(res.Protected)).To(ConsistOf("claude-code skill/.git reserved_name", "claude-code skill/.Lola reserved_name",
			"claude-code command/.LOLA reserved_name"))
	})

	It("a destination inside another lola project is skipped, from either scope", func() {
		By("project to nested project")
		nested := filepath.Join(t.proj, "sub")
		t.files(nested, map[string]string{".lola/project.yml": "version: 1\n", ".claude/agents/scout.md": "x\n"})
		Expect(os.RemoveAll(filepath.Join(t.proj, ".claude", "agents"))).To(Succeed())
		t.link("../sub/.claude/agents", filepath.Join(t.proj, ".claude", "agents"))
		res := t.dry(t.sc)
		Expect(codes(res.Skipped)).To(Equal([]string{"claude-code agent/ other_project"}))
		Expect(noticeTexts(res.Notices)).To(ContainElement(".claude/agents is inside the lola project " + nested + "; lola tidy skips it"))
		Expect(res.Hints).To(ContainElement("lola tidy -C " + nested))

		By("user scope to a project through a dotfiles link")
		dots := filepath.Join(t.home, "dotfiles")
		t.files(dots, map[string]string{".lola/project.yml": "version: 1\n", "claude/skills/mine/SKILL.md": "x\n"})
		t.link(filepath.Join(dots, "claude"), filepath.Join(t.home, ".claude"))
		res = t.dry(t.user, "claude-code")
		Expect(codes(res.Skipped)).To(ContainElement("claude-code skill/ other_project"))
		Expect(res.Candidates).To(BeEmpty())
		Expect(res.Hints).To(ContainElement("lola tidy -C " + dots + " -t claude-code"))
	})

	It("an instruction file is locked by its on-disk name at any depth in any case", func() {
		docs := filepath.Join(t.tmp, "docs")
		t.files(docs, map[string]string{"CLAUDE.md": "instructions\n"})
		t.files(t.proj, map[string]string{
			".claude/commands/CLAUDE.md":           "x\n",
			".claude/agents/agents.md":             "x\n",
			".opencode/commands/Gemini.md":         "x\n",
			".gemini/lola/m/GEMINI.md":             "x\n",
			".claude/skills/s1/SKILL.md":           "x\n",
			".claude/skills/s2/SKILL.md":           "x\n",
			".claude/skills/s3/deep/dir/AGENTS.MD": "x\n",
		})
		t.link("SKILL.md", filepath.Join(t.proj, ".claude", "skills", "s1", "claude.md"))
		t.link(docs, filepath.Join(t.proj, ".claude", "skills", "s2", "ref"))
		res := t.dry(t.sc, "claude-code", "gemini-cli", "opencode")
		Expect(codes(res.Protected)).To(ConsistOf("claude-code skill/s1 instruction_file", "claude-code skill/s3 instruction_file",
			"claude-code command/CLAUDE instruction_file", "claude-code agent/agents instruction_file",
			"gemini-cli rule/m instruction_file", "opencode command/Gemini instruction_file"))
		Expect(listed(res.Candidates)).To(Equal([]string{"claude-code skill/s2"}))

		moved, err := t.tidy(engine.TidyRequest{Select: pats("skill/s2"), Yes: true, Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed(moved.Moved)).To(Equal([]string{"claude-code skill/s2"}))
		Expect(os.Readlink(filepath.Join(moved.Moved[0].Trash, "ref"))).To(Equal(docs))
		Expect(os.ReadFile(filepath.Join(docs, "CLAUDE.md"))).To(Equal([]byte("instructions\n")))
	})

	It("a fifo, socket, unreadable subdirectory or a path with a line break is locked", func() {
		t.files(t.proj, map[string]string{".claude/skills/pipe/SKILL.md": "x\n", ".claude/skills/sock/SKILL.md": "x\n",
			".claude/skills/closed/sub/x.md": "x\n", ".claude/commands/two\nlines.md": "x\n", ".claude/commands/tab\tbed.md": "x\n"})
		mkfifo(filepath.Join(t.proj, ".claude", "skills", "pipe", "p"))
		mkfifo(filepath.Join(t.proj, ".claude", "commands", "bare-fifo"))
		mksocket(filepath.Join(t.proj, ".claude", "skills", "sock", "s"))
		closed := filepath.Join(t.proj, ".claude", "skills", "closed", "sub")
		Expect(os.Chmod(closed, 0)).To(Succeed())
		DeferCleanup(os.Chmod, closed, os.FileMode(0o755))
		res := t.dry(t.sc)
		Expect(res.Candidates).To(BeEmpty())
		Expect(codes(res.Protected)).To(ConsistOf("claude-code skill/pipe not_a_file", "claude-code skill/sock not_a_file",
			"claude-code skill/closed unreadable", "claude-code command/bare-fifo not_a_file",
			"claude-code command/two\n  lines unrecordable_name", "claude-code command/tab?bed unrecordable_name"))
	})

	It("a name ParsePattern refuses or untrusted.Text alters is locked, and --all skips it", func() {
		long := strings.Repeat("l", 130)
		t.files(t.proj, map[string]string{".claude/skills/esc\x1b[31m/SKILL.md": "x\n", ".claude/skills/zero\u200bwidth/SKILL.md": "x\n",
			".claude/skills/" + long + "/SKILL.md": "x\n", ".claude/commands/fine.md": "x\n"})
		res := t.dry(t.sc)
		Expect(listed(res.Candidates)).To(Equal([]string{"claude-code command/fine"}))
		Expect(codes(res.Protected)).To(ConsistOf("claude-code skill/esc?[31m unselectable_name", "claude-code skill/zero?width unselectable_name",
			"claude-code skill/"+long+" unselectable_name"))
		moved, err := t.tidy(engine.TidyRequest{All: true, Yes: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed(moved.Moved)).To(Equal([]string{"claude-code command/fine"}))
		Expect(filepath.Join(t.proj, ".claude", "skills", "esc\x1b[31m")).To(BeADirectory())
	})

	It("one selector matches a command and its sidecar directory", func() {
		t.files(t.proj, map[string]string{".claude/commands/tmp.md": "x\n", ".claude/commands/tmp/ref.md": "x\n", ".claude/commands/tmpx.md": "x\n"})
		res, err := t.tidy(engine.TidyRequest{Select: pats("command/tmp"), Yes: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed(res.Moved)).To(Equal([]string{"claude-code command/tmp", "claude-code command/tmp"}))
		Expect(filepath.Join(t.proj, ".claude", "commands", "tmp.md")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(t.proj, ".claude", "commands", "tmp")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(t.proj, ".claude", "commands", "tmpx.md")).To(BeAnExistingFile())
	})
})

var _ = Describe("lola tidy: MCP entries", func() {
	var t *tidyEnv
	BeforeEach(func() { t = newTidyEnv() })

	It("only direct members of the declared server map are offered; other keys and other files are untouched", func() {
		claudeJSON := `{
  "mcpServers": {
    "gone": {"command": "npx", "args": ["-y", "gone"]}
  },
  "projects": {"/work/app": {"mcpServers": {"nested": {"command": "x"}}}},
  "theme": "dark"
}
`
		t.files(t.home, map[string]string{".claude.json": claudeJSON, ".claude/settings.json": `{"mcpServers": {"other": {}}}`})
		t.files(t.home, map[string]string{".claude/commands/x.md": "x\n"})
		res := t.dry(t.user, "claude-code")
		Expect(listed(res.Candidates)).To(Equal([]string{"claude-code command/x", "claude-code mcp/gone"}))
		mcp := res.Candidates[1]
		Expect(mcp.Key).To(Equal("/mcpServers/gone"))
		Expect(mcp.Path).To(Equal(filepath.Join(t.home, ".claude.json")))
		Expect(mcp.Label).To(Equal("npx"))

		moved, err := t.e.Tidy(tidyCtx, engine.TidyRequest{Scope: t.user, Select: pats("mcp/gone"), Yes: true, Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed(moved.Moved)).To(Equal([]string{"claude-code mcp/gone"}))
		b, err := os.ReadFile(filepath.Join(t.home, ".claude.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("{\n  \"mcpServers\": {\n  },\n  \"projects\": {\"/work/app\": {\"mcpServers\": {\"nested\": {\"command\": \"x\"}}}},\n  \"theme\": \"dark\"\n}\n"))
		Expect(os.ReadFile(filepath.Join(t.home, ".claude", "settings.json"))).To(Equal([]byte(`{"mcpServers": {"other": {}}}`)))
	})

	It("an MCP member lola owns is never offered, also when hand-edited; one lola never wrote is offered", func() {
		t.install(t.sc)
		b, err := os.ReadFile(filepath.Join(t.proj, ".mcp.json"))
		Expect(err).NotTo(HaveOccurred())
		edited := strings.Replace(string(b), "--data", "--edited", 1)
		edited = strings.Replace(edited, `"mcpServers": {`, `"mcpServers": {"added-by-hand": {"type": "http", "url": "https://user:pw@mcp.example.com/x?token=t"},`, 1)
		Expect(os.WriteFile(filepath.Join(t.proj, ".mcp.json"), []byte(edited), 0o644)).To(Succeed())
		res := t.dry(t.sc)
		Expect(listed(res.Candidates)).To(Equal([]string{"claude-code mcp/added-by-hand"}))
		Expect(res.Candidates[0].Label).To(Equal("mcp.example.com"))
	})

	DescribeTable("an MCP file that does not parse, leaves the root, is not a regular file, or names a server twice is skipped whole with a notice",
		func(setup func(t *tidyEnv), code, notice string) {
			setup(t)
			res := t.dry(t.sc, "claude-code")
			Expect(res.Candidates).To(BeEmpty())
			Expect(codes(res.Skipped)).To(Equal([]string{"claude-code mcp/ " + code}))
			Expect(noticeTexts(res.Notices)).To(ContainElement(ContainSubstring(notice)))
		},
		Entry("does not parse", func(t *tidyEnv) { t.files(t.proj, map[string]string{".mcp.json": `{"mcpServers": {"a": `}) },
			"unparseable", ".mcp.json does not parse as JSON; lola tidy skips it"),
		Entry("leaves the root", func(t *tidyEnv) {
			t.files(t.tmp, map[string]string{"out.json": `{"mcpServers": {"a": {}}}`})
			t.link(filepath.Join(t.tmp, "out.json"), filepath.Join(t.proj, ".mcp.json"))
		}, "link_out", ".mcp.json leads outside "),
		Entry("is not a regular file", func(t *tidyEnv) { Expect(os.MkdirAll(filepath.Join(t.proj, ".mcp.json", "a"), 0o755)).To(Succeed()) },
			"not_regular", ".mcp.json is not a regular file; lola tidy skips it"),
		Entry("names a server twice", func(t *tidyEnv) {
			t.files(t.proj, map[string]string{".mcp.json": `{"mcpServers": {"a": {}, "b": {}, "a": {}}}`})
		},
			"duplicate_key", ".mcp.json names the server a twice; lola tidy skips it"),
		Entry("holds a server map that is not an object", func(t *tidyEnv) { t.files(t.proj, map[string]string{".mcp.json": `{"mcpServers": [1, 2]}`}) },
			"unparseable", ".mcp.json holds /mcpServers, which is not an object; lola tidy skips it"),
	)
})

var _ = Describe("lola tidy: links lola writes through and scope roots", func() {
	var t *tidyEnv
	BeforeEach(func() { t = newTidyEnv() })

	It("a link lola writes through is owned: .claude/skills/review -> ~/elsewhere is not offered", func() {
		elsewhere := filepath.Join(t.home, "elsewhere")
		Expect(os.MkdirAll(elsewhere, 0o755)).To(Succeed())
		t.link(elsewhere, filepath.Join(t.proj, ".claude", "skills", "review"))
		t.install(t.sc)
		Expect(filepath.Join(elsewhere, "SKILL.md")).To(BeAnExistingFile(), "lola wrote through the link")
		t.files(t.proj, map[string]string{".claude/skills/old/SKILL.md": "x\n"})
		res := t.dry(t.sc)
		Expect(listed(res.Candidates)).To(Equal([]string{"claude-code skill/old"}))
		_, err := t.tidy(engine.TidyRequest{Select: pats("skill/review"), Yes: true})
		Expect(err).To(MatchError("skill/review is installed by module example; lola tidy only moves what lola does not manage"))
	})

	It("the scope root is never another project: a user-scope tidy with $HOME/.lola present still scans ~/.claude", func() {
		t.files(t.home, map[string]string{".lola/project.yml": "version: 1\n", ".claude/skills/old/SKILL.md": "x\n"})
		res := t.dry(t.user, "claude-code")
		Expect(res.Skipped).To(BeEmpty())
		Expect(listed(res.Candidates)).To(Equal([]string{"claude-code skill/old"}))
	})
})
