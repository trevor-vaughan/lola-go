package engine_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

func writeTree(root string, files map[string]string) {
	for p, c := range files {
		full := filepath.Join(root, p)
		Expect(os.MkdirAll(filepath.Dir(full), 0o755)).To(Succeed())
		Expect(os.WriteFile(full, []byte(c), 0o644)).To(Succeed())
	}
}

var exampleModule = map[string]string{
	"skills/review/SKILL.md":      "---\ndescription: Review code\n---\nReview it.\n",
	"skills/review/ref/checks.md": "checks",
	"commands/ship.md":            "Ship $ARGUMENTS\n",
	"agents/critic.md":            "---\ndescription: critic\nmodel: opus\n---\nCriticize.\n",
	"mcps.json":                   `{"mcpServers":{"lola-gh":{"command":"${PLUGIN_ROOT}/bin/gh","args":["--data","${PLUGIN_DATA}"]}}}`,
	"rules/style.md":              "Prefer small diffs.\n",
}

var _ = Describe("Engine", func() {
	var (
		tmp, proj, src string
		p              paths.Paths
		e              *engine.Engine
		sc             engine.Scope
		ctx            = context.Background()
		claude         = engine.Options{Targets: []string{"claude-code"}}
	)
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(proj, rel))
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}
	install := func(opts engine.Options, args ...string) (*engine.Result, error) {
		var reqs []engine.InstallRequest
		for _, a := range args {
			reqs = append(reqs, engine.InstallRequest{Arg: a})
		}
		return e.Install(ctx, sc, proj, reqs, opts)
	}
	status := func() []string {
		rows, err := e.Status(sc)
		Expect(err).NotTo(HaveOccurred())
		var out []string
		for _, r := range rows {
			out = append(out, r.Name+"/"+r.Target+"="+r.Status)
		}
		return out
	}
	// tree lists every file under dir, relative and slash-separated.
	tree := func(dir string) []string {
		var out []string
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel, _ := filepath.Rel(dir, path)
				out = append(out, filepath.ToSlash(rel))
			}
			return nil
		})
		return out
	}

	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj = filepath.Join(tmp, "proj")
		src = filepath.Join(tmp, "src", "example")
		writeTree(src, exampleModule)
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
	})

	It("installs every artifact kind into Claude Code and records a relative source relative to the project", func() {
		res, err := install(claude, "../src/example")
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".claude/skills/review/SKILL.md")).To(Equal("---\ndescription: Review code\n---\nReview it.\n"))
		Expect(read(".claude/skills/review/ref/checks.md")).To(Equal("checks"))
		Expect(read(".claude/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"))
		Expect(read(".claude/agents/critic.md")).To(ContainSubstring("model: opus"))
		Expect(read(".claude/rules/lola/example/style.md")).To(Equal("Prefer small diffs.\n"))
		Expect(filepath.Join(proj, "CLAUDE.md")).NotTo(BeAnExistingFile(), "lola never creates or edits CLAUDE.md")
		Expect(read(".mcp.json")).To(ContainSubstring(filepath.Join(proj, ".lola/plugin-data/example")))
		Expect(filepath.Join(proj, ".lola/plugin-data/example")).To(BeADirectory())
		Expect(read(".lola/.gitignore")).To(Equal("lock.json\nmodules/\nplugin-data/\n"))

		Expect(res.Modules).To(HaveLen(1))
		Expect(res.Modules[0].Targets).To(Equal([]string{"claude-code"}))
		Expect(res.Modules[0].MCPServers[0].Runs).To(HavePrefix(filepath.Join(proj, ".lola/modules/example")))

		m, err := manifest.Load(filepath.Join(proj, ".lola/project.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Targets).To(Equal([]string{"claude-code"}))
		Expect(m.Modules).To(Equal([]manifest.Entry{{Name: "example", Source: "../src/example"}}))
		Expect(status()).To(Equal([]string{"example/claude-code=ok"}))
	})

	It("keeps the scope lock outside .lola/, one per project however it is spelled, and owns .lola/.gitignore", func() {
		writeTree(proj, map[string]string{".lola/.gitignore": "stale\n"})
		_, err := install(claude, src)
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".lola/.gitignore")).To(Equal("lock.json\nmodules/\nplugin-data/\n"))
		Expect(filepath.Join(proj, ".lola/.lock")).NotTo(BeAnExistingFile())

		link := filepath.Join(tmp, "link")
		Expect(os.Symlink(proj, link)).To(Succeed())
		_, err = e.Render(ctx, engine.Scope{Kind: target.Project, ProjectDir: link}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		locks, err := filepath.Glob(filepath.Join(p.Locks, "*.lock"))
		Expect(err).NotTo(HaveOccurred())
		Expect(locks).To(HaveLen(1))
	})

	It("records rules: false per module, honors it on render and reinstall, and clears it with Rules=true", func() {
		off, on := false, true
		rule := filepath.Join(proj, ".claude/rules/lola/example/style.md")
		_, err := install(engine.Options{Targets: claude.Targets, Rules: &off}, src)
		Expect(err).NotTo(HaveOccurred())
		Expect(rule).NotTo(BeAnExistingFile())
		Expect(read(".claude/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"), "everything else installs")
		m, err := manifest.Load(filepath.Join(proj, ".lola/project.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Modules[0].Rules).To(Equal(&off))
		rows, err := e.Status(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows[0].Rules).To(BeFalse())

		_, err = e.Render(ctx, sc, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(rule).NotTo(BeAnExistingFile())
		_, err = install(engine.Options{}, "example")
		Expect(err).NotTo(HaveOccurred())
		Expect(rule).NotTo(BeAnExistingFile(), "a plain reinstall keeps the entry's setting")

		_, err = install(engine.Options{Rules: &on}, src)
		Expect(err).NotTo(HaveOccurred())
		Expect(rule).To(BeARegularFile())
		m, _ = manifest.Load(filepath.Join(proj, ".lola/project.yml"))
		Expect(m.Modules[0].Rules).To(BeNil())

		_, err = install(engine.Options{Rules: &off}, "example")
		Expect(err).NotTo(HaveOccurred())
		Expect(rule).NotTo(BeAnExistingFile(), "turning rules off removes the installed ones")
		Expect(status()).To(Equal([]string{"example/claude-code=ok"}))
	})

	It("renders ${PLUGIN_ROOT} into the project's own module copy, which survives removal from the cache", func() {
		_, err := install(claude, src)
		Expect(err).NotTo(HaveOccurred())
		copyDir := filepath.Join(proj, ".lola/modules/example")
		Expect(read(".mcp.json")).To(ContainSubstring(filepath.Join(copyDir, "bin/gh")))
		Expect(read(".lola/modules/example/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"))
		Expect(filepath.Join(copyDir, "lola.yml")).To(BeARegularFile(), "the copy is the normalized cache content")
		Expect(e.Store.Remove("example")).To(BeTrue())
		Expect(read(".lola/modules/example/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"))
		Expect(status()).To(Equal([]string{"example/claude-code=ok"}))
	})

	It("keeps the module copy's build output, restores a missing copy, replaces it on a new version, and removes it on uninstall", func() {
		_, err := install(claude, src)
		Expect(err).NotTo(HaveOccurred())
		copyDir := filepath.Join(proj, ".lola/modules/example")
		writeTree(copyDir, map[string]string{"build/out.js": "built"})
		Expect(os.Symlink("out.js", filepath.Join(copyDir, "build/link.js"))).To(Succeed())
		Expect(status()).To(Equal([]string{"example/claude-code=ok"}), "content added to the copy is not reported")
		_, err = e.Render(ctx, sc, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".lola/modules/example/build/out.js")).To(Equal("built"), "render keeps the copy of an unchanged version")

		Expect(os.RemoveAll(copyDir)).To(Succeed())
		Expect(status()).To(Equal([]string{"example/claude-code=missing"}))
		_, err = e.Render(ctx, sc, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(status()).To(Equal([]string{"example/claude-code=ok"}))
		Expect(read(".lola/modules/example/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"))

		writeTree(copyDir, map[string]string{"build/out.js": "built"})
		writeTree(src, map[string]string{"commands/ship.md": "Ship faster\n"})
		_, err = e.Fetch(ctx, sourceSpec(src))
		Expect(err).NotTo(HaveOccurred())
		_, err = e.Render(ctx, sc, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".lola/modules/example/commands/ship.md")).To(Equal("Ship faster\n"))
		Expect(filepath.Join(copyDir, "build")).NotTo(BeAnExistingFile(), "a new version replaces the copy whole")
		Expect(tree(filepath.Join(proj, ".lola/modules"))).NotTo(ContainElement(HavePrefix(".example")), "no staged or old copy remains")

		_, err = e.Uninstall(ctx, sc, []string{"example"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(copyDir).NotTo(BeADirectory())
	})

	It("keeps the previous module copy when the plan cannot be committed", func() {
		if os.Geteuid() == 0 {
			Skip("root ignores directory permissions")
		}
		_, err := install(claude, src)
		Expect(err).NotTo(HaveOccurred())
		writeTree(src, map[string]string{"commands/ship.md": "v2\n"})
		_, err = e.Fetch(ctx, sourceSpec(src))
		Expect(err).NotTo(HaveOccurred())
		lolaDir := filepath.Join(proj, ".lola")
		Expect(os.Chmod(lolaDir, 0o555)).To(Succeed())
		DeferCleanup(os.Chmod, lolaDir, os.FileMode(0o755))
		_, err = e.Render(ctx, sc, engine.Options{})
		Expect(err).To(HaveOccurred())
		Expect(os.Chmod(lolaDir, 0o755)).To(Succeed())
		Expect(read(".lola/modules/example/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"))
		Expect(read(".claude/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"))
		Expect(tree(filepath.Join(proj, ".lola/modules"))).NotTo(ContainElement(HavePrefix(".example")))
	})

	It("dry-run computes the plan and writes nothing, not even .lola/", func() {
		res, err := install(engine.Options{Targets: []string{"claude-code"}, DryRun: true}, src)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.DryRun).To(BeTrue())
		Expect(res.Steps).NotTo(BeEmpty())
		Expect(tree(proj)).To(BeEmpty())
	})

	It("uninstall and render leave no orphans", func() {
		Expect(os.WriteFile(filepath.Join(proj, "CLAUDE.md"), []byte("# Mine\n"), 0o644)).To(Succeed())
		_, err := install(claude, src)
		Expect(err).NotTo(HaveOccurred())

		Expect(os.RemoveAll(filepath.Join(src, "agents"))).To(Succeed())
		_, err = install(engine.Options{}, src)
		Expect(err).NotTo(HaveOccurred())
		_, err = e.Render(ctx, sc, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(proj, ".claude/agents/critic.md")).NotTo(BeAnExistingFile())

		res, err := e.Uninstall(ctx, sc, []string{"example"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(read("CLAUDE.md")).To(Equal("# Mine\n"))
		Expect(tree(proj)).To(ConsistOf("CLAUDE.md", ".lola/.gitignore", ".lola/lock.json", ".lola/project.yml"))
		Expect(filepath.Join(proj, ".claude/skills")).To(BeADirectory(), "base dirs stay")
		Expect(res.Notices).To(ContainElement(target.Notice{Level: "info", Text: "still in your cache: lola cache rm example"}))
		Expect(res.Notices).To(ContainElement(HaveField("Text", ContainSubstring("plugin data kept at"))))
		m, _ := manifest.Load(filepath.Join(proj, ".lola/project.yml"))
		Expect(m.Modules).To(BeEmpty())
		l, _ := ledger.Load(filepath.Join(proj, ".lola/lock.json"))
		Expect(l.Modules).To(BeEmpty())
		Expect(filepath.Join(proj, ".claude/rules/lola")).NotTo(BeAnExistingFile(), "emptied rule dirs are removed")
	})

	It("render picks up a new store head; list shows stale, modified and missing", func() {
		_, err := install(claude, src)
		Expect(err).NotTo(HaveOccurred())
		writeTree(src, map[string]string{"commands/ship.md": "Ship faster\n"})
		_, err = e.Fetch(ctx, sourceSpec(src))
		Expect(err).NotTo(HaveOccurred())
		Expect(status()).To(Equal([]string{"example/claude-code=stale"}))
		_, err = e.Render(ctx, sc, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".claude/commands/ship.md")).To(Equal("Ship faster\n"))
		Expect(status()).To(Equal([]string{"example/claude-code=ok"}))

		writeTree(proj, map[string]string{".claude/commands/ship.md": "edited"})
		Expect(status()).To(Equal([]string{"example/claude-code=modified"}))
		Expect(os.Remove(filepath.Join(proj, ".claude/commands/ship.md"))).To(Succeed())
		Expect(status()).To(Equal([]string{"example/claude-code=missing"}))
	})

	It("render refuses hand edits unless forced", func() {
		_, err := install(claude, src)
		Expect(err).NotTo(HaveOccurred())
		writeTree(src, map[string]string{"commands/ship.md": "v2\n"})
		_, err = e.Fetch(ctx, sourceSpec(src))
		Expect(err).NotTo(HaveOccurred())
		writeTree(proj, map[string]string{".claude/commands/ship.md": "my edit"})
		_, err = e.Render(ctx, sc, engine.Options{})
		Expect(err).To(MatchError(ContainSubstring("hand-edited")))
		Expect(read(".claude/commands/ship.md")).To(Equal("my edit"))
		_, err = e.Render(ctx, sc, engine.Options{Force: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".claude/commands/ship.md")).To(Equal("v2\n"))
	})

	It("installs a stored module by name without touching its source", func() {
		_, err := e.Fetch(ctx, sourceSpec(src))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.RemoveAll(src)).To(Succeed())
		_, err = install(claude, "example")
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".claude/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"))
	})

	It("explains what was searched when an argument is neither stored nor a source", func() {
		_, err := install(claude, "nope")
		Expect(err).To(MatchError(`"nope" is neither a module in your cache nor a source`))
		Expect(errs.Hints(err)).To(ContainElement("cached modules: lola cache list"))
	})

	It("keeps a fetched module stored when the install fails, and says how to retry", func() {
		writeTree(src, map[string]string{"agents/bad.md": "no frontmatter\n"})
		_, err := install(claude, src)
		Expect(err).To(MatchError(ContainSubstring("needs a description")))
		Expect(errs.Hints(err)).To(ContainElement("fetched modules stay in your cache; retry with: lola install example"))
		names, _ := e.Store.Names()
		Expect(names).To(Equal([]string{"example"}))
	})

	It("requires targets and validates them", func() {
		_, err := install(engine.Options{}, src)
		Expect(err).To(MatchError("no targets selected"))
		_, err = install(engine.Options{Targets: []string{"vim"}}, src)
		Expect(err).To(MatchError(`unknown target "vim"`))
		Expect(errs.Hints(err)).To(ContainElement("valid targets: claude-code"))
	})

	It("refuses --ref for a folder source", func() {
		_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: src, Ref: "v1"}}, claude)
		Expect(err).To(MatchError(ContainSubstring("--ref applies to git sources only")))
	})

	It("skips targets a module does not declare, with a notice", func() {
		writeTree(src, map[string]string{"lola.yml": "name: example\ntargets: [opencode]\n"})
		res, err := install(claude, src)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(target.Notice{Level: "info", Text: "example does not support claude-code; skipped there"}))
		Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory())
		Expect(status()).To(BeEmpty())
	})

	It("uninstall of a module that is not installed points at the store", func() {
		_, err := e.Fetch(ctx, sourceSpec(src))
		Expect(err).NotTo(HaveOccurred())
		_, err = e.Uninstall(ctx, sc, []string{"example"}, engine.Options{})
		Expect(err).To(MatchError(`lola uninstall: "example" is not installed in this project`))
		Expect(errs.Hints(err)).To(Equal([]string{"still in your cache: lola cache rm example", "installed here: lola list"}))
	})

	It("rejects uninstall with a malicious module name and leaves the project intact", func() {
		testFile := filepath.Join(proj, "important.txt")
		Expect(os.WriteFile(testFile, []byte("keep this"), 0o644)).To(Succeed())
		man := &manifest.Manifest{Version: 1, Modules: []manifest.Entry{{Name: "../..", Source: src}}}
		Expect(man.Save(filepath.Join(proj, ".lola", "project.yml"))).To(Succeed())
		_, err := e.Uninstall(ctx, sc, []string{"../.."}, engine.Options{})
		Expect(err).To(HaveOccurred(), "should reject invalid module name")
		Expect(testFile).To(BeAnExistingFile(), "important file should not be deleted")
		content, err := os.ReadFile(testFile)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(Equal("keep this"))
	})

	It("user-scope state does not depend on the working directory (Review Focus 5)", func() {
		user := engine.Scope{Kind: target.User}
		for _, cwd := range []string{proj, tmp} {
			_, err := e.Install(ctx, user, cwd, []engine.InstallRequest{{Arg: src}}, claude)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(filepath.Join(p.Home, ".claude/commands/ship.md")).To(BeARegularFile())
		Expect(filepath.Join(p.Home, ".claude.json")).To(BeARegularFile())
		Expect(filepath.Join(p.Home, ".claude/rules/lola/example/style.md")).To(BeARegularFile(), "rules install at user scope")
		Expect(filepath.Join(p.DataHome, "lola/user-modules/example/commands/ship.md")).To(BeARegularFile(), "user-scope module copy")
		Expect(filepath.Join(p.Home, ".claude/CLAUDE.md")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(p.Home, "CLAUDE.md")).NotTo(BeAnExistingFile())
		Expect(p.UserLedger).To(BeARegularFile())
		Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())
		m, err := manifest.Load(p.UserManifest)
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Modules[0].Source).To(Equal(src), "user scope records absolute sources")
		rows, err := e.Status(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		lolaDataHome, err := os.Stat(filepath.Join(p.DataHome, "lola"))
		Expect(err).NotTo(HaveOccurred())
		Expect(lolaDataHome.Mode()&0o777).To(Equal(os.FileMode(0o700)),
			"lola XDG directories must be 0700")
		userModulesDir, err := os.Stat(filepath.Join(p.DataHome, "lola", "user-modules"))
		Expect(err).NotTo(HaveOccurred())
		Expect(userModulesDir.Mode()&0o777).To(Equal(os.FileMode(0o700)),
			"user-modules directory must be 0700")
	})

	It("FindProject walks up to the nearest .lola/ and falls back to the start dir", func() {
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(proj, "a/b"), 0o755)).To(Succeed())
		Expect(engine.FindProject(filepath.Join(proj, "a/b"))).To(Equal(proj))
		Expect(engine.FindProject(tmp)).To(Equal(tmp))
	})

	It("Detect finds Claude Code by .claude/ or CLAUDE.md", func() {
		Expect(e.Detect(sc)).To(BeEmpty())
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		Expect(e.Detect(sc)).To(Equal([]string{"claude-code"}))
	})

	It("reports conflicts with hints and changes nothing", func() {
		writeTree(proj, map[string]string{".claude/commands/ship.md": "mine"})
		_, err := install(claude, src)
		var ce *plan.ConflictError
		Expect(errors.As(err, &ce)).To(BeTrue())
		Expect(read(".claude/commands/ship.md")).To(Equal("mine"))
		Expect(filepath.Join(proj, ".claude/skills")).NotTo(BeADirectory())
	})

	It("keeps the manifest's relative source when reinstalling by name", func() {
		_, err := install(claude, "../src/example")
		Expect(err).NotTo(HaveOccurred())
		_, err = install(engine.Options{}, "example")
		Expect(err).NotTo(HaveOccurred())
		m, err := manifest.Load(filepath.Join(proj, ".lola/project.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Modules[0].Source).To(Equal("../src/example"))
	})

	It("leaves no .lola/ behind when a command fails or records nothing", func() {
		// (a) install with no targets selected fails and leaves no .lola/
		_, err := install(engine.Options{}, src)
		Expect(err).To(MatchError("no targets selected"))
		Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())

		// (b) uninstall of nonexistent module fails and leaves no .lola/
		_, err = e.Uninstall(ctx, sc, []string{"nope"}, engine.Options{})
		Expect(err).To(MatchError(ContainSubstring("not installed")))
		Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())

		// (c) render on empty manifest succeeds but leaves no .lola/
		_, err = e.Render(ctx, sc, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())
	})

	It("render fetches a module removed from the store", func() {
		_, err := install(claude, src)
		Expect(err).NotTo(HaveOccurred())
		ok, err := e.Store.Remove("example")
		Expect(ok).To(BeTrue())
		Expect(err).NotTo(HaveOccurred())
		_, err = e.Render(ctx, sc, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		names, _ := e.Store.Names()
		Expect(names).To(Equal([]string{"example"}))
	})

	It("never removes a .lola/ that existed before a failed command", func() {
		// Create .lola/ before any operation
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		_, err := install(engine.Options{}, src)
		Expect(err).To(MatchError("no targets selected"))
		Expect(filepath.Join(proj, ".lola")).To(BeADirectory())

		// Dry-run that fails with pre-existing .lola containing a file
		Expect(os.WriteFile(filepath.Join(proj, ".lola/keep"), []byte("data"), 0o644)).To(Succeed())
		_, err = install(engine.Options{DryRun: true, Targets: []string{"vim"}}, src)
		Expect(err).To(MatchError(ContainSubstring("unknown target")))
		Expect(filepath.Join(proj, ".lola/keep")).To(BeARegularFile())
	})

	It("render with nothing installed leaves no .lola/", func() {
		_, err := e.Render(ctx, sc, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())
	})

	Describe("project state behind a symlink", func() {
		var outside string
		// plant replaces rel under the project by a symlink to outside, which holds example/keep.txt.
		plant := func(rel string) string {
			link := filepath.Join(proj, rel)
			Expect(os.RemoveAll(link)).To(Succeed())
			Expect(os.MkdirAll(filepath.Dir(link), 0o755)).To(Succeed())
			Expect(os.Symlink(outside, link)).To(Succeed())
			return link
		}
		expectRefused := func(err error, link string) {
			Expect(err).To(MatchError(link + " is a symlink, and lola keeps its state in real directories"))
			Expect(errs.Hints(err)).To(ContainElement("replace it with a directory: rm " + link + ", then rerun"))
			Expect(os.ReadFile(filepath.Join(outside, "example/keep.txt"))).To(Equal([]byte("mine")))
			Expect(tree(outside)).To(ConsistOf("example/keep.txt"))
		}
		BeforeEach(func() {
			outside = filepath.Join(tmp, "victim")
			writeTree(outside, map[string]string{"example/keep.txt": "mine"})
		})

		It("refuses to install into a .lola/modules that links outside the project", func() {
			link := plant(".lola/modules")
			_, err := install(claude, src)
			expectRefused(err, link)
			Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory())
		})

		It("refuses to uninstall through a .lola/modules planted after install", func() {
			_, err := install(claude, src)
			Expect(err).NotTo(HaveOccurred())
			link := plant(".lola/modules")
			_, err = e.Uninstall(ctx, sc, []string{"example"}, engine.Options{})
			expectRefused(err, link)
			Expect(read(".claude/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"))
			_, err = e.Render(ctx, sc, engine.Options{})
			expectRefused(err, link)
		})

		It("refuses a .lola that is itself a symlink, also for dry runs and list", func() {
			link := plant(".lola")
			_, err := install(claude, src)
			expectRefused(err, link)
			_, err = install(engine.Options{Targets: claude.Targets, DryRun: true}, src)
			expectRefused(err, link)
			_, err = e.Status(sc)
			expectRefused(err, link)
		})

		It("still allows the user-scope module copies to be a symlink", func() {
			Expect(os.MkdirAll(filepath.Dir(p.UserModules), 0o700)).To(Succeed())
			Expect(os.Symlink(outside, p.UserModules)).To(Succeed())
			_, err := e.Install(ctx, engine.Scope{Kind: target.User}, proj, []engine.InstallRequest{{Arg: src}}, claude)
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Join(outside, "example/commands/ship.md")).To(BeARegularFile())
		})
	})

	Describe("install hooks", func() {
		var log, copyDir string
		var asked []string
		ask := func(answer bool) func(string, []engine.HookScript) (bool, error) {
			return func(m string, _ []engine.HookScript) (bool, error) {
				asked = append(asked, m)
				return answer, nil
			}
		}
		hookLog := func() []string {
			b, err := os.ReadFile(log)
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			Expect(err).NotTo(HaveOccurred())
			return strings.Split(strings.TrimSpace(string(b)), "\n")
		}
		// script logs its environment, and whether lola had written the project's files yet.
		script := "#!/bin/sh\nwritten=no\ntest -e \"$LOLA_PROJECT_PATH/.claude/commands/ship.md\" && written=yes\n" +
			"echo \"$LOLA_HOOK $LOLA_MODULE_NAME $LOLA_ASSISTANT $LOLA_SCOPE $LOLA_MODULE_PATH $LOLA_PROJECT_PATH $(pwd) written=$written\" >> \"$HOOK_LOG\"\n"
		hooks := func(yaml string, files map[string]string) {
			files["lola.yml"] = "name: example\nhooks: " + yaml + "\n"
			writeTree(src, files)
			for p := range files {
				if strings.HasSuffix(p, ".sh") {
					Expect(os.Chmod(filepath.Join(src, p), 0o755)).To(Succeed())
				}
			}
		}
		BeforeEach(func() {
			log, copyDir, asked = filepath.Join(tmp, "hooks.log"), filepath.Join(proj, ".lola/modules/example"), nil
			e.Environ = func() []string { return append(os.Environ(), "HOOK_LOG="+log) }
			hooks("{pre-install: hooks/pre.sh, post-install: hooks/post.sh}", map[string]string{"hooks/pre.sh": script, "hooks/post.sh": script})
		})

		It("runs pre-install before anything is written and post-install after, per target, in the module copy, with the §9.1 environment", func() {
			res, err := install(engine.Options{Targets: claude.Targets, Ask: ask(true)}, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(hookLog()).To(Equal([]string{
				"pre-install example claude-code project " + copyDir + " " + proj + " " + copyDir + " written=no",
				"post-install example claude-code project " + copyDir + " " + proj + " " + copyDir + " written=yes",
			}))
			Expect(asked).To(Equal([]string{"example"}))
			Expect(res.Modules[0].Hooks).To(HaveLen(2))
			Expect(res.Modules[0].Hooks[0]).To(HaveField("Status", engine.HookRan))
			Expect(res.Modules[0].Hooks[0].SHA256).To(MatchRegexp(`^[0-9a-f]{64}$`))

			_, err = install(engine.Options{Ask: ask(true)}, "example")
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveLen(1), "consent is remembered for unchanged hooks")
			Expect(hookLog()).To(HaveLen(4))

			hooks("{pre-install: hooks/pre.sh, post-install: hooks/post.sh}", map[string]string{"hooks/post.sh": script + "true\n"})
			_, err = install(engine.Options{Ask: ask(true)}, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveLen(2), "a changed hook asks again")
		})

		It("without consent, a pre-install hook aborts before anything is written and a post-install hook is skipped with a warning", func() {
			_, err := install(engine.Options{Targets: claude.Targets, Ask: ask(false)}, src)
			Expect(err).To(MatchError("example has a pre-install hook, and lola runs hooks only with your consent"))
			Expect(errs.Hints(err)).To(ContainElement("then rerun with --allow-hooks, or on a terminal answer the prompt"))
			Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory())
			Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())
			Expect(hookLog()).To(BeNil())

			hooks("{post-install: hooks/post.sh}", map[string]string{})
			res, err := install(engine.Options{Targets: claude.Targets}, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(read(".claude/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"))
			Expect(res.Modules[0].Hooks).To(ConsistOf(HaveField("Status", engine.HookDeclined)))
			Expect(res.Notices).To(ContainElement(HaveField("Text", "example: post-install hook skipped without your consent (rerun with --allow-hooks to run it)")))
			Expect(hookLog()).To(BeNil())
		})

		It("installs none of several modules when one of them lacks consent for its pre-install hook", func() {
			other := filepath.Join(tmp, "src", "other")
			writeTree(other, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "O\n"})
			_, err := install(engine.Options{Targets: claude.Targets, Ask: ask(false)}, other, src)
			Expect(err).To(MatchError(ContainSubstring("example has a pre-install hook")))
			Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory())
			Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())
		})

		It("a failing pre-install hook installs nothing and keeps the previous copy; a failing post-install hook keeps the installation", func() {
			allow := engine.Options{Targets: claude.Targets, AllowHooks: true}
			_, err := install(allow, src)
			Expect(err).NotTo(HaveOccurred())
			writeTree(src, map[string]string{"commands/ship.md": "v2\n", "hooks/pre.sh": "#!/bin/sh\nexit 3\n"})
			_, err = install(allow, src)
			Expect(err).To(MatchError("pre-install hook hooks/pre.sh of example failed for claude-code: exit status 3"))
			Expect(read(".claude/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"))
			Expect(read(".lola/modules/example/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"))

			writeTree(src, map[string]string{"hooks/pre.sh": "#!/bin/sh\n", "hooks/post.sh": "#!/bin/sh\nexit 4\n"})
			res, err := install(allow, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(read(".claude/commands/ship.md")).To(Equal("v2\n"))
			Expect(res.Notices).To(ContainElement(HaveField("Text",
				"post-install hook hooks/post.sh of example failed for claude-code: exit status 4; the installation was kept")))
			Expect(res.Modules[0].Hooks[1]).To(HaveField("Status", engine.HookFailed))
		})

		It("dry-run lists the hooks and runs none; render runs them only for a new version", func() {
			res, err := install(engine.Options{Targets: claude.Targets, DryRun: true, Ask: ask(true)}, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Modules[0].Hooks).To(HaveLen(2))
			Expect(res.Modules[0].Hooks).To(HaveEach(HaveField("Status", engine.HookWouldRun)))
			Expect(asked).To(BeEmpty())
			Expect(hookLog()).To(BeNil())

			allow := engine.Options{AllowHooks: true}
			_, err = install(engine.Options{Targets: claude.Targets, AllowHooks: true}, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(hookLog()).To(HaveLen(2))
			_, err = e.Render(ctx, sc, allow)
			Expect(err).NotTo(HaveOccurred())
			Expect(hookLog()).To(HaveLen(2), "the same version runs no hooks")
			writeTree(src, map[string]string{"commands/ship.md": "v2\n"})
			_, err = e.Fetch(ctx, sourceSpec(src))
			Expect(err).NotTo(HaveOccurred())
			_, err = e.Render(ctx, sc, allow)
			Expect(err).NotTo(HaveOccurred())
			Expect(hookLog()).To(HaveLen(4))
		})

		It("render reruns the hooks when it restores a missing copy, and keeps what a hook built in the copy", func() {
			hooks("{post-install: hooks/post.sh}", map[string]string{
				"hooks/post.sh": script + "echo built > \"$LOLA_MODULE_PATH/built.txt\"\n"})
			allow := engine.Options{AllowHooks: true}
			_, err := install(engine.Options{Targets: claude.Targets, AllowHooks: true}, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(hookLog()).To(HaveLen(1))
			Expect(status()).To(Equal([]string{"example/claude-code=ok"}))
			_, err = e.Render(ctx, sc, allow)
			Expect(err).NotTo(HaveOccurred())
			Expect(hookLog()).To(HaveLen(1))
			Expect(filepath.Join(copyDir, "built.txt")).To(BeARegularFile(), "render keeps the hook's build")

			Expect(os.RemoveAll(copyDir)).To(Succeed())
			_, err = e.Render(ctx, sc, allow)
			Expect(err).NotTo(HaveOccurred())
			Expect(hookLog()).To(HaveLen(2), "a restored copy reruns the hooks")
			Expect(filepath.Join(copyDir, "built.txt")).To(BeARegularFile())
			Expect(status()).To(Equal([]string{"example/claude-code=ok"}))
		})

		It("stops a hook that runs past the timeout", func() {
			e.HookTimeout = 200 * time.Millisecond
			writeTree(src, map[string]string{"hooks/pre.sh": "#!/bin/sh\nexec sleep 5\n"})
			start := time.Now()
			_, err := install(engine.Options{Targets: claude.Targets, AllowHooks: true}, src)
			Expect(err).To(MatchError(ContainSubstring("pre-install hook hooks/pre.sh of example failed for claude-code: timed out after 200ms")))
			Expect(time.Since(start)).To(BeNumerically("<", 3*time.Second))
		})
	})
})

func sourceSpec(dir string) source.Spec { return source.Spec{Kind: source.KindFolder, Location: dir} }
