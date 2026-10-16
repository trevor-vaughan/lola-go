package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"

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
	"INSTRUCTIONS.md":             "Prefer small diffs.\n",
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
		Expect(read(".claude/lola/example.md")).To(Equal("Prefer small diffs.\n"))
		Expect(read("CLAUDE.md")).To(Equal("<!-- lola:instructions:start -->\n@.claude/lola/example.md\n<!-- lola:instructions:end -->\n"))
		Expect(read(".mcp.json")).To(ContainSubstring(filepath.Join(proj, ".lola/plugin-data/example")))
		Expect(filepath.Join(proj, ".lola/plugin-data/example")).To(BeADirectory())
		Expect(read(".lola/.gitignore")).To(Equal("lock.json\nplugin-data/\n.lock\n"))

		Expect(res.Modules).To(HaveLen(1))
		Expect(res.Modules[0].Targets).To(Equal([]string{"claude-code"}))
		Expect(res.Modules[0].MCPServers[0].Runs).To(HavePrefix(p.Store))

		m, err := manifest.Load(filepath.Join(proj, ".lola/project.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Targets).To(Equal([]string{"claude-code"}))
		Expect(m.Modules).To(Equal([]manifest.Entry{{Name: "example", Source: "../src/example"}}))
		Expect(status()).To(Equal([]string{"example/claude-code=ok"}))
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
		Expect(tree(proj)).To(ConsistOf("CLAUDE.md", ".lola/.gitignore", ".lola/.lock", ".lola/lock.json", ".lola/project.yml"))
		Expect(filepath.Join(proj, ".claude/skills")).To(BeADirectory(), "base dirs stay")
		Expect(res.Notices).To(ContainElement(target.Notice{Level: "info", Text: "still in your cache: lola cache rm example"}))
		Expect(res.Notices).To(ContainElement(HaveField("Text", ContainSubstring("plugin data kept at"))))
		m, _ := manifest.Load(filepath.Join(proj, ".lola/project.yml"))
		Expect(m.Modules).To(BeEmpty())
		l, _ := ledger.Load(filepath.Join(proj, ".lola/lock.json"))
		Expect(l.Modules).To(BeEmpty())
		Expect(l.Shared).To(BeEmpty())
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

	It("user-scope state does not depend on the working directory (Review Focus 5)", func() {
		user := engine.Scope{Kind: target.User}
		for _, cwd := range []string{proj, tmp} {
			_, err := e.Install(ctx, user, cwd, []engine.InstallRequest{{Arg: src}}, claude)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(filepath.Join(p.Home, ".claude/commands/ship.md")).To(BeARegularFile())
		Expect(filepath.Join(p.Home, ".claude.json")).To(BeARegularFile())
		Expect(filepath.Join(p.Home, "CLAUDE.md")).NotTo(BeAnExistingFile(), "no user-scope instructions")
		Expect(p.UserLedger).To(BeARegularFile())
		Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())
		m, err := manifest.Load(p.UserManifest)
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Modules[0].Source).To(Equal(src), "user scope records absolute sources")
		rows, err := e.Status(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(1))
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
})

func sourceSpec(dir string) source.Spec { return source.Spec{Kind: source.KindFolder, Location: dir} }
