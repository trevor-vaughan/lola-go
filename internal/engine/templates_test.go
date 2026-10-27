package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/target/claude"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
	"github.com/trevor-vaughan/lola-go/internal/trust"
)

// kitTemplates is a module whose every Markdown component is a template, and kitLiteral(t) the
// literal module it renders as for target t at project scope.
var kitTemplates = map[string]string{
	"lola.yml":                      "name: kit\n",
	"skills/review/SKILL.md.liquid": "---\ndescription: {{ 'Review for ' | append: target | json }}\n---\nReview for {{ target }} in {{ scope }}.\n",
	"skills/review/ref/a.md.liquid": "a for {{ target }}\n",
	"skills/review/ref/plain.txt":   "plain\n",
	"commands/ship.md.liquid":       "---\ndescription: {{ 'Ship' | json }}\n---\nShip for {{ target }}\n",
	"commands/ship/notes.md.liquid": "notes for {{ target }}\n",
	"agents/critic.md.liquid":       "---\ndescription: {{ 'Critic' | json }}\n{% if target == 'claude-code' %}model: opus{% endif %}\n---\nCriticize for {{ target }}.\n",
	"rules/style.md.liquid":         "Style for {{ target }}.\n",
}

func kitLiteral(t string) map[string]string {
	model := ""
	if t == "claude-code" {
		model = "model: opus"
	}
	return map[string]string{
		"lola.yml":                    "name: kit\n",
		"skills/review/SKILL.md":      "---\ndescription: \"Review for " + t + "\"\n---\nReview for " + t + " in project.\n",
		"skills/review/ref/a.md":      "a for " + t + "\n",
		"skills/review/ref/plain.txt": "plain\n",
		"commands/ship.md":            "---\ndescription: \"Ship\"\n---\nShip for " + t + "\n",
		"commands/ship/notes.md":      "notes for " + t + "\n",
		"agents/critic.md":            "---\ndescription: \"Critic\"\n" + model + "\n---\nCriticize for " + t + ".\n",
		"rules/style.md":              "Style for " + t + ".\n",
	}
}

var _ = Describe("Templates (Plan 7.2)", func() {
	var (
		tmp, proj, src string
		p              paths.Paths
		e              *engine.Engine
		sc             engine.Scope
		ctx            = context.Background()
	)
	// files is every file under dir but lola's state, path → content.
	files := func(dir string) map[string]string {
		GinkgoHelper()
		out := map[string]string{}
		Expect(filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(dir, path)
			if strings.HasPrefix(rel, ".lola") {
				return nil
			}
			b, err := os.ReadFile(path)
			out[filepath.ToSlash(rel)] = string(b)
			return err
		})).To(Succeed())
		return out
	}
	read := func(rel string) string {
		GinkgoHelper()
		b, err := os.ReadFile(filepath.Join(proj, rel))
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}
	// activate records names as the scope's active profiles, as lola profile --on does.
	activate := func(s engine.Scope, names ...string) {
		GinkgoHelper()
		led, err := e.Ledger(s)
		Expect(err).NotTo(HaveOccurred())
		led.Profiles = nil
		for _, n := range names {
			led.Profiles = append(led.Profiles, ledger.Active{Name: n, By: ledger.ByOn})
		}
		Expect(led.Save(filepath.Join(s.ProjectDir, ".lola", "lock.json"))).To(Succeed())
	}
	sync := func(opts engine.Options) (*engine.SyncResult, error) {
		return e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: opts, ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil },
			Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }})
	}
	exists := func(rel string) bool {
		_, err := os.Lstat(filepath.Join(proj, rel))
		return err == nil
	}
	install := func(opts engine.Options, args ...string) (*engine.Result, error) {
		var reqs []engine.InstallRequest
		for _, a := range args {
			reqs = append(reqs, engine.InstallRequest{Arg: a})
		}
		return e.Install(ctx, sc, proj, reqs, opts)
	}
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj = filepath.Join(tmp, "proj")
		e.Getwd = func() (string, error) { return proj, nil }
		src = filepath.Join(tmp, "src", "kit")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
	})

	It("a case variant of a reserved name is never copied to a target and warns", func() {
		writeTree(src, map[string]string{
			"skills/review/SKILL.md":           "Review.\n",
			"skills/review/Skill.md.liquid":    "not read\n",
			"skills/review/ref.md":             "kept\n",
			"SKILL.md":                         "Root.\n",
			"_Partials/h.liquid":               "not read\n",
			"notes.md":                         "kept\n",
			"skills/review/SKILL.targets.yaml": "claude-code: {}\n",
			"skills/review/skill.targets.yaml": "nope: {}\n",
			"SKILL.targets.jsonc":              "{}",
			"Skill.targets.JSONC":              "{}",
		})
		if es, err := os.ReadDir(filepath.Join(src, "skills", "review")); err != nil || len(es) != 5 {
			Skip("the file system folds case: SKILL.targets.yaml and skill.targets.yaml are one file")
		}
		res, err := install(engine.Options{Targets: []string{"claude-code", "opencode"}}, "../src/kit")
		Expect(err).NotTo(HaveOccurred())
		Expect(exists(".claude/skills/review/ref.md")).To(BeTrue())
		Expect(exists(".claude/skills/review/Skill.md.liquid")).To(BeFalse())
		Expect(exists(".opencode/skills/review/Skill.md.liquid")).To(BeFalse())
		Expect(res.Findings).To(ContainElement(HaveField("Text", "skills/review/Skill.md.liquid is not read; name it SKILL.md.liquid")))
		Expect(exists(".claude/skills/kit/notes.md")).To(BeTrue())
		Expect(exists(".claude/skills/kit/_Partials")).To(BeFalse())
		Expect(exists(".opencode/skills/kit/_Partials")).To(BeFalse())
		Expect(res.Findings).To(ContainElement(HaveField("Text", "_Partials is not read; name it _partials")))
		for _, f := range []string{".claude/skills/review/SKILL.targets.yaml", ".claude/skills/review/skill.targets.yaml",
			".opencode/skills/review/skill.targets.yaml", ".claude/skills/kit/SKILL.targets.jsonc", ".claude/skills/kit/Skill.targets.JSONC"} {
			Expect(exists(f)).To(BeFalse(), f)
		}
		Expect(res.Findings).To(ContainElement(HaveField("Text", "skills/review/skill.targets.yaml is not read; name it skills/review/SKILL.targets.yaml")))
		Expect(res.Findings).To(ContainElement(HaveField("Text", "Skill.targets.JSONC is not read; name it SKILL.targets.jsonc")))
	})

	It("_partials is never copied to a target, also for a root skill", func() {
		writeTree(src, map[string]string{
			"SKILL.md.liquid":                "Root {% include 'h' %}\n",
			"notes.md":                       "kept\n",
			"skills/review/SKILL.md.liquid":  "Review {% include 'h' %}\n",
			"_partials/h.liquid":             "head",
			"_partials/h.claude-code.liquid": "claude head",
		})
		_, err := install(engine.Options{Targets: []string{"claude-code", "gemini-cli", "opencode"}}, "../src/kit")
		Expect(err).NotTo(HaveOccurred())
		got := files(proj)
		for path := range got {
			Expect(strings.ToLower(path)).NotTo(ContainSubstring("_partials"), path)
		}
		Expect(got).To(HaveKeyWithValue(".claude/skills/kit/SKILL.md", "Root claude head\n"))
		Expect(got).To(HaveKeyWithValue(".claude/skills/kit/notes.md", "kept\n"))
		Expect(got).To(HaveKeyWithValue(".claude/skills/review/SKILL.md", "Review claude head\n"))
	})
	It("a .md.liquid file renders as the component it names, at that target's path", func() {
		deployed := func(mod map[string]string, t string) map[string]string {
			GinkgoHelper()
			dir, err := os.MkdirTemp(tmp, "proj")
			Expect(err).NotTo(HaveOccurred())
			from, err := os.MkdirTemp(tmp, "src")
			Expect(err).NotTo(HaveOccurred())
			writeTree(filepath.Join(from, "kit"), mod)
			_, err = e.Install(ctx, engine.Scope{Kind: target.Project, ProjectDir: dir}, dir, []engine.InstallRequest{{Arg: filepath.Join(from, "kit")}},
				engine.Options{Targets: []string{t}})
			Expect(err).NotTo(HaveOccurred())
			Expect(e.Store.Remove("kit")).To(BeTrue()) // the next module of that name is another source
			return files(dir)
		}
		for _, t := range []string{"claude-code", "gemini-cli", "opencode"} {
			got := deployed(kitTemplates, t)
			Expect(got).To(Equal(deployed(kitLiteral(t), t)), t)
			for path := range got {
				Expect(path).NotTo(HaveSuffix(".liquid"), t)
			}
		}
		Expect(deployed(kitTemplates, "claude-code")).To(HaveKeyWithValue(".claude/skills/review/ref/a.md", "a for claude-code\n"))
	})

	It("templates render per target with the run's target, scope and profiles", func() {
		writeTree(p.UserProfiles, map[string]string{"alpha.yml": "version: 1\nmodules: []\n", "work.yml": "version: 1\nmodules: []\n"})
		writeTree(src, map[string]string{"commands/ship.md.liquid": "{{ target }} {{ scope }} {{ profiles | join: ',' }}\n"})
		activate(sc, "user:alpha", "user:work")
		_, err := install(engine.Options{Targets: []string{"claude-code", "gemini-cli", "opencode"}}, "../src/kit")
		Expect(err).NotTo(HaveOccurred())
		got := files(proj)
		for prefix, t := range map[string]string{".claude/": "claude-code", ".gemini/": "gemini-cli", ".opencode/": "opencode"} {
			Expect(slices.ContainsFunc(slices.Collect(maps.Keys(got)), func(k string) bool {
				return strings.HasPrefix(k, prefix) && strings.Contains(got[k], t+" project user:alpha,user:work")
			})).To(BeTrue(), "%s: %v", t, got)
		}
		_, err = e.Install(ctx, engine.Scope{Kind: target.User}, proj, []engine.InstallRequest{{Arg: src}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		home, err := os.UserHomeDir()
		Expect(err).NotTo(HaveOccurred())
		b, err := os.ReadFile(filepath.Join(home, ".claude", "commands", "ship.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("claude-code user \n"), "the user scope has no active profile")
	})

	It("rendered frontmatter is parsed, checked and consented per target", func() {
		writeTree(src, map[string]string{"agents/guard.md.liquid": "---\ndescription: {{ 'g' | json }}\n{% if target == 'claude-code' %}hooks:\n" +
			"  Stop:\n    - hooks:\n        - type: command\n          command: echo hi\n{% endif %}\n---\nGuard.\n"})
		res, err := install(engine.Options{Targets: []string{"claude-code", "opencode"}, Trust: engine.TrustRuntimeHooks}, "../src/kit")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Modules).To(HaveLen(1))
		Expect(res.Modules[0].RuntimeHooks).To(ConsistOf(And(HaveField("Target", "claude-code"), HaveField("Component", "agent guard"),
			HaveField("Command", HaveSuffix("; echo hi")))))
		Expect(read(".claude/agents/guard.md")).To(ContainSubstring("; echo hi"))
		for path, content := range files(proj) {
			if strings.HasPrefix(path, ".opencode/") {
				Expect(content).NotTo(ContainSubstring("echo hi"), path)
			}
		}
	})

	It("a frontmatter value that renders a line break without json is template-frontmatter-value, and json keeps it one string", func() {
		writeTree(src, map[string]string{"lola.yml": "name: kit\nvars:\n  v: \"x\\nhooks: [Bash]\"\n", "agents/a.md.liquid": "---\ndescription: {{ vars.v }}\n---\nA\n"})
		_, err := install(engine.Options{Targets: []string{"claude-code"}}, "../src/kit")
		te, ok := errors.AsType[*engine.TemplateError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(te.Code).To(Equal("template-frontmatter-value"))
		Expect(te.Error()).To(Equal("kit: claude-code agent a: agents/a.md.liquid:2: in frontmatter, this {{ }} rendered a line break; " +
			"end it with | json so the value stays one YAML value: {{ vars.v | json }}"))
		Expect(exists(".claude/agents/a.md")).To(BeFalse())

		writeTree(src, map[string]string{"lola.yml": "name: kit\n", "agents/a.md.liquid": "---\ndescription: {{ target }} agent\n---\nA\n"})
		_, err = install(engine.Options{Targets: []string{"claude-code"}}, "../src/kit")
		Expect(err).NotTo(HaveOccurred(), "a value without a line break renders as written")
		doc, err := module.ParseDoc([]byte(read(".claude/agents/a.md")))
		Expect(err).NotTo(HaveOccurred())
		Expect(doc.Frontmatter).To(Equal(map[string]any{"name": "a", "description": "claude-code agent"}))

		writeTree(src, map[string]string{"agents/a.md.liquid": "---\n{% assign v = \"x\nhooks: [Bash]\" %}description: {{ v | json }}\n---\nA\n"})
		_, err = install(engine.Options{Targets: []string{"claude-code"}}, "../src/kit")
		Expect(err).NotTo(HaveOccurred())
		doc, err = module.ParseDoc([]byte(read(".claude/agents/a.md")))
		Expect(err).NotTo(HaveOccurred())
		Expect(doc.Frontmatter).To(Equal(map[string]any{"name": "a", "description": "x\nhooks: [Bash]"}))

		writeTree(src, map[string]string{"lola.yml": "name: kit\nvars:\n  v: \"x\\nhooks: [Bash]\"\n", "agents/a.md.liquid": "---\ndescription: {{ vars.v | json }}\n---\nA\n"})
		_, err = install(engine.Options{Targets: []string{"claude-code"}}, "../src/kit")
		Expect(err).NotTo(HaveOccurred())
		doc, err = module.ParseDoc([]byte(read(".claude/agents/a.md")))
		Expect(err).NotTo(HaveOccurred())
		Expect(doc.Frontmatter).To(Equal(map[string]any{"name": "a", "description": "x\nhooks: [Bash]"}), "a var holding a newline stays one string")

		By("a string YAML would not read back as itself refuses unless it ends with json (R-7-U18)")
		writeTree(src, map[string]string{"lola.yml": "name: kit\nvars:\n  v: \"sonnet #fast\"\n", "agents/a.md.liquid": "---\ndescription: {{ vars.v }}\n---\nA\n"})
		_, err = install(engine.Options{Targets: []string{"claude-code"}}, "../src/kit")
		te, ok = errors.AsType[*engine.TemplateError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(te.Code).To(Equal("template-frontmatter-value"))
		Expect(te.Error()).To(Equal(`kit: claude-code agent a: agents/a.md.liquid:2: in frontmatter, YAML would read "sonnet #fast" as a comment after "sonnet"; end that {{ }} with | json`))
		writeTree(src, map[string]string{"agents/a.md.liquid": "---\ndescription: {{ vars.v | json }}\n---\nA\n"})
		_, err = install(engine.Options{Targets: []string{"claude-code"}}, "../src/kit")
		Expect(err).NotTo(HaveOccurred())
		doc, err = module.ParseDoc([]byte(read(".claude/agents/a.md")))
		Expect(err).NotTo(HaveOccurred())
		Expect(doc.Frontmatter).To(Equal(map[string]any{"name": "a", "description": "sonnet #fast"}))
	})

	It("a var holding a newline and --- cannot end the frontmatter early: tools: stays in the frontmatter", func() {
		writeTree(src, map[string]string{"agents/a.md.liquid": "---\n{% assign v = \"a\n---\ntools: [Bash]\" %}description: {{ v | json }}\ntools: [Read]\n---\nA\n"})
		_, err := install(engine.Options{Targets: []string{"claude-code"}}, "../src/kit")
		Expect(err).NotTo(HaveOccurred())
		doc, err := module.ParseDoc([]byte(read(".claude/agents/a.md")))
		Expect(err).NotTo(HaveOccurred())
		Expect(doc.Frontmatter).To(HaveKeyWithValue("tools", []any{"Read"}))
		Expect(doc.Frontmatter).To(HaveKeyWithValue("description", "a\n---\ntools: [Bash]"))
		Expect(doc.Body).To(Equal("A\n"))

		writeTree(src, map[string]string{"lola.yml": "name: kit\nvars:\n  v: \"a\\n---\\ntools: [Bash]\"\n",
			"agents/a.md.liquid": "---\ndescription: {{ vars.v | json }}\ntools: [Read]\n---\nA\n"})
		_, err = install(engine.Options{Targets: []string{"claude-code"}}, "../src/kit")
		Expect(err).NotTo(HaveOccurred())
		doc, err = module.ParseDoc([]byte(read(".claude/agents/a.md")))
		Expect(err).NotTo(HaveOccurred())
		Expect(doc.Frontmatter).To(HaveKeyWithValue("tools", []any{"Read"}), "from a var too")
		Expect(doc.Frontmatter).To(HaveKeyWithValue("description", "a\n---\ntools: [Bash]"))
	})

	It("a failed template writes nothing in any module of the run", func() {
		good, bad := filepath.Join(proj, "mods", "good"), filepath.Join(proj, "mods", "bad")
		writeTree(good, map[string]string{"commands/g.md.liquid": "G {{ target }}\n"})
		writeTree(bad, map[string]string{"commands/b.md.liquid": "B {{ targt }}\n"})
		writeTree(src, map[string]string{"commands/k.md": "K\n"})
		_, err := install(engine.Options{Targets: []string{"claude-code"}}, "../src/kit")
		Expect(err).NotTo(HaveOccurred())
		before, cachedBefore := files(proj), cachedNames(e)
		state := func() map[string]string {
			out := map[string]string{}
			for _, f := range []string{".lola/project.yml", ".lola/lock.json"} {
				b, _ := os.ReadFile(filepath.Join(proj, f))
				out[f] = string(b)
			}
			return out
		}
		lola := state()
		unchanged := func(what string) {
			GinkgoHelper()
			Expect(files(proj)).To(Equal(before), what)
			Expect(state()).To(Equal(lola), what)
		}

		_, err = install(engine.Options{Targets: []string{"claude-code"}}, "mods/good", "mods/bad")
		Expect(err).To(MatchError(ContainSubstring("commands/b.md.liquid:1: undefined variable in {{ targt }}")))
		unchanged("install")
		Expect(cachedNames(e)).To(ConsistOf(append(cachedBefore, "bad", "good")), "the fetch cached both sources")
		for _, name := range []string{"good", "bad"} {
			v, ok, err := e.Store.Head(name, "")
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(files(v.ContentDir)).To(HaveLen(2), "the source and lola.yml, never a render: %v", files(v.ContentDir))
		}

		manifestFile := filepath.Join(proj, ".lola", "project.yml")
		written := read(".lola/project.yml")
		Expect(os.WriteFile(manifestFile, []byte(written+"  - {name: good, source: mods/good}\n  - {name: bad, source: mods/bad}\n"), 0o644)).To(Succeed())
		_, err = manifest.Load(manifestFile)
		Expect(err).NotTo(HaveOccurred())
		lola = state()
		_, err = sync(engine.Options{})
		Expect(err).To(MatchError(ContainSubstring("undefined variable in {{ targt }}")))
		unchanged("sync of two modules")

		writeTree(p.UserProfiles, map[string]string{"alpha.yml": "version: 1\nmodules: []\n"})
		Expect(os.WriteFile(manifestFile, []byte(written), 0o644)).To(Succeed())
		writeTree(src, map[string]string{"commands/p.md.liquid": "{% for p in profiles %}\n{{ nope }}{% endfor %}\n"})
		_, err = sync(engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		before = files(proj)
		activate(sc, "user:alpha")
		lola = state()
		_, err = e.Render(ctx, sc, engine.Options{})
		Expect(err).To(MatchError(ContainSubstring("commands/p.md.liquid:2: undefined variable in {{ nope }}")))
		unchanged("render")

		// git and archive fetches: nothing written, the cache holds the source as fetched, the run's
		// staging directory is gone
		runs := func() []string {
			GinkgoHelper()
			es, err := os.ReadDir(p.Staging)
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			Expect(err).NotTo(HaveOccurred())
			var out []string
			for _, e := range es {
				out = append(out, e.Name())
			}
			return out
		}
		badFiles := map[string]string{"lola.yml": "name: gbad\n", "commands/b.md.liquid": "B {{ targt }}\n"}
		work := filepath.Join(tmp, "work", "gbad")
		writeTree(work, badFiles)
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "c")
		gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "gbad.git"))
		url := testenv.ServeGit(filepath.Join(tmp, "srv")) + "/gbad.git"
		archive := filepath.Join(tmp, "abad.tar.gz")
		Expect(os.WriteFile(archive, tarGz("abad-1", map[string]string{"lola.yml": "name: abad\n", "commands/b.md.liquid": "B {{ targt }}\n"}), 0o644)).To(Succeed())
		for name, arg := range map[string]string{"gbad": url, "abad": archive} {
			before, lola = files(proj), state()
			_, err = install(engine.Options{Targets: []string{"claude-code"}}, arg)
			Expect(err).To(MatchError(ContainSubstring("commands/b.md.liquid:1: undefined variable in {{ targt }}")), name)
			unchanged(name)
			Expect(runs()).To(BeEmpty(), "%s: the run directory is removed after the refusal", name)
			v, ok, err := e.Store.Head(name, "")
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue(), name)
			Expect(files(v.ContentDir)).To(Equal(map[string]string{"lola.yml": "name: " + name + "\n", "commands/b.md.liquid": "B {{ targt }}\n"}),
				"%s: the cache holds the source, never a render", name)
		}

		Expect(os.WriteFile(manifestFile, []byte(written+"  - {name: gbad, source: '"+url+"'}\n"), 0o644)).To(Succeed())
		before, lola = files(proj), state()
		_, err = sync(engine.Options{})
		Expect(err).To(MatchError(ContainSubstring("commands/b.md.liquid:1: undefined variable in {{ targt }}")))
		unchanged("sync of a git entry")
		Expect(runs()).To(BeEmpty(), "the sync's run directory is removed after the refusal")
	})

	It("after a timeout no later template renders and the run refuses", func() {
		var seen []string
		e.Targets = []target.Target{spy{Target: claude.Target{}, seen: &seen}}
		e.TemplateLimits.Timeout = 100 * time.Millisecond
		loop := "{% assign l = 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx' | split: '' %}" +
			"{% for a in l %}{% for b in l %}{% for c in l %}{% for d in l %}{% assign s = a | append: b %}{% endfor %}{% endfor %}{% endfor %}{% endfor %}"
		slow, later := filepath.Join(tmp, "src", "aslow"), filepath.Join(tmp, "src", "later")
		writeTree(slow, map[string]string{"commands/a.md.liquid": loop, "commands/b.md.liquid": "b"})
		writeTree(later, map[string]string{"commands/c.md.liquid": "c"})
		start := time.Now()
		_, err := install(engine.Options{Targets: []string{"claude-code"}}, "../src/aslow", "../src/later")
		Expect(err).To(MatchError("aslow: claude-code command a: commands/a.md.liquid: took longer than 100ms to render; lola stops a template after 100ms"))
		Expect(time.Since(start)).To(BeNumerically("<", e.TemplateLimits.Timeout+time.Second))
		Expect(seen).NotTo(ContainElement(HavePrefix("plan")), "no module was planned after the timeout")
		Expect(files(proj)).To(BeEmpty())
	})

	It("install, sync, render, market install, check and profile switches all render templates", func() {
		writeTree(p.UserProfiles, map[string]string{"alpha.yml": "version: 1\nmodules: []\n"})
		writeTree(src, map[string]string{"commands/ship.md.liquid": "Ship for {{ target }} {{ profiles | join: ',' }}\n"})
		_, err := install(engine.Options{Targets: []string{"claude-code"}}, "../src/kit")
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".claude/commands/ship.md")).To(Equal("Ship for claude-code \n"), "install")

		Expect(os.Remove(filepath.Join(proj, ".claude", "commands", "ship.md"))).To(Succeed())
		_, err = sync(engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".claude/commands/ship.md")).To(Equal("Ship for claude-code \n"), "sync")

		activate(sc, "user:alpha")
		_, err = e.Render(ctx, sc, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".claude/commands/ship.md")).To(Equal("Ship for claude-code user:alpha\n"), "render")

		pk := filepath.Join(tmp, "src", "pk")
		writeTree(pk, map[string]string{"commands/pk.md.liquid": "pk {{ profiles | join: ',' }}\n"})
		writeTree(p.UserProfiles, map[string]string{"work.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: pk, source: " + pk + "}\n"})
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Names: []string{"work"}},
			ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil }})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".claude/commands/pk.md")).To(Equal("pk user:alpha,user:work\n"), "a profile switch")

		rep, err := e.Check(ctx, sc, proj, []string{src}, []string{"claude-code"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rep.Modules[0].Findings).NotTo(ContainElement(HaveField("Code", HavePrefix("template-"))), "check")

		msrv := filepath.Join(tmp, "msrv")
		work := filepath.Join(tmp, "work", "plugins")
		Expect(os.MkdirAll(work, 0o755)).To(Succeed())
		gitIn(work, "init", "-q", "-b", "main")
		writeTree(work, map[string]string{".claude-plugin/marketplace.json": `{"name": "acme", "owner": {"name": "Acme"}, "plugins": [{"name": "mk", "source": "./mk"}]}`,
			"mk/commands/mk.md.liquid": "mk for {{ target }}\n"})
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "c")
		gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(msrv, "plugins.git"))
		_, err = e.AddMarket(ctx, engine.MarketAddRequest{Source: testenv.ServeGitWith(msrv, nil) + "/plugins.git"})
		Expect(err).NotTo(HaveOccurred())
		_, err = install(engine.Options{Targets: []string{"claude-code"}}, "@acme/mk")
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".claude/commands/mk.md")).To(Equal("mk for claude-code\n"), "market install")
	})

	It("findings are computed on the rendered body", func() {
		writeTree(src, map[string]string{"skills/review/SKILL.md.liquid": "---\ndescription: {{ 'r' | json }}\n---\n{% if target == 'claude-code' %}!`git status`{% endif %}Review.\n"})
		res, err := install(engine.Options{Targets: []string{"claude-code", "opencode"}}, "../src/kit")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Findings).NotTo(ContainElement(HaveField("Code", "skill-shell-dropped")))
		Expect(read(".claude/skills/review/SKILL.md")).To(ContainSubstring("!`git status`"))

		writeTree(src, map[string]string{"skills/review/SKILL.md.liquid": "---\ndescription: {{ 'r' | json }}\n---\n!`git status` Review.\n"})
		res, err = install(engine.Options{Targets: []string{"claude-code", "opencode"}}, "../src/kit")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Findings).To(ContainElement(And(HaveField("Code", "skill-shell-dropped"), HaveField("Target", "opencode"))))
	})

	It("a key a value renders into frontmatter installs, under the name check, Recheck and consent", func() {
		module := func(vars, agent string) {
			Expect(os.RemoveAll(src)).To(Succeed())
			writeTree(src, map[string]string{"lola.yml": "name: kit\nvars:\n" + vars, "agents/a.md.liquid": agent})
		}
		module("  key: model\n", "---\ndescription: a\n{{ vars.key }}: sonnet\n---\nA\n")
		_, err := install(engine.Options{Targets: []string{"claude-code"}}, "../src/kit")
		Expect(err).NotTo(HaveOccurred(), "a value may add a key on its own line")
		Expect(read(".claude/agents/a.md")).To(ContainSubstring("\nmodel: sonnet\n"))

		hooks := "---\ndescription: a\n{{ vars.key }}:\n  Stop:\n    - hooks:\n        - type: command\n          command: echo stop\n---\nA\n"
		module("  key: hooks\n", hooks)
		res, err := install(engine.Options{Targets: []string{"claude-code"}}, "../src/kit")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Findings).To(ContainElement(And(HaveField("Code", "claude-code/runtime-hooks-declined"), HaveField("Artifact", "a"))),
			"hooks a value names are asked about as any hooks")
		Expect(exists(".claude/agents/a.md")).To(BeFalse())
		_, err = install(engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustRuntimeHooks}, "../src/kit")
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".claude/agents/a.md")).To(ContainSubstring("; echo stop\n"))

		for vars, want := range map[string]string{
			"  key: name\n":  "kit: claude-code agent a: agents/a.md.liquid (rendered for claude-code): renders name: other, but its path names it a",
			"  key: Hooks\n": "kit: claude-code agent a: agents/a.md.liquid (rendered for claude-code): agent a: frontmatter Hooks",
			"  key: '---'\n": "kit: claude-code agent a: agents/a.md.liquid (rendered for claude-code): frontmatter: rendered line 3 is ---, which would end the frontmatter",
		} {
			agent := "---\ndescription: a\n{{ vars.key }}: other\n---\nA\n"
			if strings.Contains(vars, "---") {
				agent = "---\ndescription: a\n{{ vars.key }}\nmodel: x\n---\nA\n"
			}
			module(vars, agent)
			_, err := install(engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustRuntimeHooks}, "../src/kit")
			Expect(err).To(MatchError(HavePrefix(want)), vars)
		}

		By("Claude Code's capability warnings fire on keys and values a value renders (R-7-56)")
		for vars, want := range map[string]types.GomegaMatcher{
			"  key: allowed-tools\n  value: Bash\n": And(HaveField("Code", "claude-code/allowed-tools"), HaveField("Artifact", "a"),
				HaveField("Text", "pre-approves tools Claude Code then runs without asking: Bash")),
			"  key: permissionMode\n  value: bypassPermissions\n": And(HaveField("Code", "claude-code/permission-mode"), HaveField("Artifact", "a"),
				HaveField("Text", "sets the permission mode it runs in: bypassPermissions (Claude Code v2.1.267+ ignores bypassPermissions in a subagent's frontmatter)")),
		} {
			module(vars, "---\ndescription: a\n{{ vars.key }}: {{ vars.value }}\n---\nA\n")
			res, err := install(engine.Options{Targets: []string{"claude-code"}}, "../src/kit")
			Expect(err).NotTo(HaveOccurred(), vars)
			Expect(res.Findings).To(ContainElement(want), vars)
		}
	})

	It("a partial included in frontmatter cannot write a --- or ... line", func() {
		for name, tree := range map[string]map[string]string{
			"a fence built across tags": {"commands/c.md.liquid": "---\n--{% if true %}{% endif %}-\nk: 1\n---\nbody\n"},
			"a fence built across partials": {"commands/c.md.liquid": "---\n{% include 'a' %}\nk: 1\n---\nbody\n",
				"_partials/a.liquid": "--{% include 'b' %}", "_partials/b.liquid": "-"},
			"a document end built across tags": {"commands/c.md.liquid": "---\nk: 1\n.{% if true %}..{% endif %}\n---\nbody\n"},
			"a CRLF fence built across partials": {"commands/c.md.liquid": "---\r\n{% include 'a' %}\r\nk: 1\r\n---\r\nbody\r\n",
				"_partials/a.liquid": "--{% include 'b' %}", "_partials/b.liquid": "-"},
			"a fence with a trailing space built across partials": {"commands/c.md.liquid": "---\n{% include 'a' %}\nk: 1\n---\nbody\n",
				"_partials/a.liquid": "--{% include 'b' %}", "_partials/b.liquid": "- \t"},
			"a fence a partial writes as text": {"commands/c.md.liquid": "---\nk: 1\n{% include 'a' %}\n---\nbody\n", "_partials/a.liquid": "---"},
			"a fence a partial writes in a block": {"commands/c.md.liquid": "---\n{% include 'a' %}\nk: 1\n---\nbody\n",
				"_partials/a.liquid": "{% if true %}---{% endif %}"},
			"a document end a partial writes": {"commands/c.md.liquid": "---\nk: 1\n{% include 'a' %}\n---\nbody\n", "_partials/a.liquid": "...  "},
		} {
			Expect(os.RemoveAll(src)).To(Succeed())
			writeTree(src, tree)
			_, err := install(engine.Options{Targets: []string{"claude-code"}}, "../src/kit")
			te, ok := errors.AsType[*engine.TemplateError](err)
			Expect(ok).To(BeTrue(), "%s: %v", name, err)
			Expect(te.Code).To(Equal("template-frontmatter-fence"), name)
			Expect(te.JSONFields()["template"]).To(HaveKeyWithValue("rendered", true), name)
			Expect(te.Error()).To(MatchRegexp(`^kit: claude-code command c: commands/c\.md\.liquid \(rendered for claude-code\): `+
				`frontmatter: rendered line [23] is (---|\.\.\.), which would end the frontmatter$`), name)
			Expect(files(proj)).To(BeEmpty(), name)
		}
	})

	It("a refusal in a partial names the partial and its include site, in the error, its JSON and check", func() {
		writeTree(src, map[string]string{
			"commands/a.md.liquid":                "a\n{% include 'header' %}",
			"_partials/header.claude-code.liquid": "Header\n{{ titel }}",
			"_partials/header.liquid":             "generic",
		})
		_, err := install(engine.Options{Targets: []string{"claude-code"}}, "../src/kit")
		te, ok := errors.AsType[*engine.TemplateError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(te.Error()).To(Equal("kit: claude-code command a: _partials/header.claude-code.liquid:2: undefined variable in {{ titel }} " +
			"(included from commands/a.md.liquid:2)"))
		Expect(te.JSONFields()["template"]).To(SatisfyAll(
			HaveKeyWithValue("file", "_partials/header.claude-code.liquid"), HaveKeyWithValue("line", 2),
			HaveKeyWithValue("included_from", "commands/a.md.liquid:2"), HaveKeyWithValue("reason", "undefined variable in {{ titel }}")))

		rep, err := e.Check(ctx, sc, proj, []string{src}, []string{"claude-code", "opencode"}, nil)
		Expect(err).NotTo(HaveOccurred())
		var got []string
		for _, f := range rep.Modules[0].Findings {
			if strings.HasPrefix(f.Code, "template-") {
				got = append(got, f.Target+" "+f.Code+": "+f.Text)
			}
		}
		Expect(got).To(ConsistOf("claude-code template-undefined-variable: _partials/header.claude-code.liquid:2: undefined variable in {{ titel }} "+
			"(included from commands/a.md.liquid:2)"), "opencode renders the generic partial")
	})

	It("lola check reports each template error as a skip finding and keeps checking; a timeout refuses the check", func() {
		writeTree(src, map[string]string{
			"commands/a.md.liquid":  "x\n{{ targt }}",
			"agents/b.md.liquid":    "{% capture x %}{% endcapture %}",
			"commands/ok.md.liquid": "ok {{ target }}",
			"agents/h.md.liquid":    "---\nhooks: {{ target | json }}\n---\nh",
		})
		rep, err := e.Check(ctx, sc, proj, []string{src}, []string{"claude-code", "opencode"}, nil)
		Expect(err).NotTo(HaveOccurred())
		var got []string
		for _, f := range rep.Modules[0].Findings {
			if strings.HasPrefix(f.Code, "template-") && f.Code != "template-rendered-check" {
				got = append(got, string(f.Severity)+" "+f.Target+" "+f.Kind+" "+f.Artifact+" "+f.Code+": "+f.Text)
			}
		}
		Expect(got).To(ConsistOf(
			"skip claude-code command a template-undefined-variable: commands/a.md.liquid:2: undefined variable in {{ targt }}",
			"skip opencode command a template-undefined-variable: commands/a.md.liquid:2: undefined variable in {{ targt }}",
			"skip claude-code agent b template-tag-unavailable: agents/b.md.liquid:1: the capture tag is not available in lola templates",
			"skip opencode agent b template-tag-unavailable: agents/b.md.liquid:1: the capture tag is not available in lola templates",
		))
		var hooks []string
		for _, f := range rep.Modules[0].Findings {
			if f.Code == "template-rendered-check" {
				hooks = append(hooks, string(f.Severity)+" "+f.Target+" "+f.Kind+" "+f.Artifact+": "+f.Text)
			}
		}
		Expect(hooks).To(ConsistOf(HavePrefix("skip claude-code agent h: agents/h.md.liquid (rendered for claude-code): agent h: frontmatter hooks"),
			HavePrefix("skip opencode agent h: agents/h.md.liquid (rendered for opencode): agent h: frontmatter hooks")))
		Expect(rep.Modules[0].Targets).To(Equal([]string{"claude-code", "opencode"}))

		e.TemplateLimits.Timeout = 50 * time.Millisecond
		writeTree(src, map[string]string{"commands/a.md.liquid": "{% assign l = 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx' | split: '' %}" +
			"{% for a in l %}{% for b in l %}{% for c in l %}{% for d in l %}{% assign s = a | append: b %}{% endfor %}{% endfor %}{% endfor %}{% endfor %}"})
		_, err = e.Check(ctx, sc, proj, []string{src}, []string{"claude-code"}, nil)
		Expect(err).To(MatchError("kit: claude-code command a: commands/a.md.liquid: took longer than 50ms to render; lola stops a template after 50ms"))
	})

	It("${PLUGIN_ROOT} in rendered output is expanded after rendering", func() {
		writeTree(src, map[string]string{"commands/run.md.liquid": "Run {{ '${PLUGIN_ROOT}' }}/bin and ${PLUGIN_ROOT}/x for {{ target }}\n"})
		_, err := install(engine.Options{Targets: []string{"claude-code"}}, "../src/kit")
		Expect(err).NotTo(HaveOccurred())
		root := filepath.Join(proj, ".lola", "modules", "kit")
		Expect(read(".claude/commands/run.md")).To(Equal("Run " + root + "/bin and " + root + "/x for claude-code\n"))
	})

	It("the run budget refuses on time and on held output, and a re-plan after a trust answer gets a fresh budget", func() {
		writeTree(src, map[string]string{"commands/a.md.liquid": "aaaa", "commands/b.md.liquid": "bbbb"})
		clock := time.Unix(0, 0)
		e.Now = func() time.Time { clock = clock.Add(time.Second); return clock }
		e.TemplateBudget.RunTime = time.Nanosecond
		_, err := install(engine.Options{Targets: []string{"claude-code"}}, "../src/kit")
		te, ok := errors.AsType[*engine.TemplateError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(*te).To(Equal(engine.TemplateError{Module: "kit", Scope: "project", Code: "template-run-budget",
			Reason: "rendering templates took more than 1ns; lola stops after 1ns per run"}))
		Expect(te.Error()).To(Equal("kit: rendering templates took more than 1ns; lola stops after 1ns per run"))

		e.TemplateBudget.RunTime = 20 * time.Second
		e.TemplateBudget.RunOutput = 10
		writeTree(src, map[string]string{"commands/b.md.liquid": "bbbbbbb"})
		_, err = install(engine.Options{Targets: []string{"claude-code"}}, "../src/kit")
		Expect(err).To(MatchError("kit: rendering templates held more than 10 bytes of rendered templates; at most 10 bytes per run"))
		Expect(files(proj)).To(BeEmpty())

		// claude-code only, so the trust diff, which renders for every target a module supports, holds 8 bytes too
		writeTree(filepath.Join(proj, "mods", "a"), map[string]string{"lola.yml": "name: a\ntargets: [claude-code]\n", "commands/a.md.liquid": "aaaa"})
		writeTree(filepath.Join(proj, "mods", "b"), map[string]string{"lola.yml": "name: b\ntargets: [claude-code]\n", "commands/b.md.liquid": "bbbb"})
		writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  - {name: a, source: mods/a}\n  - {name: b, source: mods/b}\n"})
		e.TemplateBudget.RunOutput = 16 // the trust diff and the first pass render both (8 + 8); the re-plan renders a again
		asked := 0
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil },
			Trust: func(engine.TrustReport) (engine.TrustAnswer, error) {
				asked++
				return engine.TrustAnswer{Modules: []string{"a"}}, nil
			}})
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(Equal(1))
		Expect(read(".claude/commands/a.md")).To(Equal("aaaa"), "the pass after the answer planned a again, within a budget of its own")
	})

	Describe("rendered_with", func() {
		var reads, noread, plain string
		lock := func() map[string]any {
			GinkgoHelper()
			var l map[string]any
			Expect(json.Unmarshal([]byte(read(".lola/lock.json")), &l)).To(Succeed())
			return l["modules"].(map[string]any)
		}
		turn := func(action engine.ProfileAction, opts engine.Options, names ...string) (*engine.SyncResult, error) {
			return e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: opts, Profiles: &engine.ProfileChange{Action: action, Names: names},
				ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil }})
		}
		BeforeEach(func() {
			reads, noread, plain = filepath.Join(proj, "mods", "reads"), filepath.Join(proj, "mods", "noread"), filepath.Join(proj, "mods", "plain")
			// reads reads profiles only through a partial
			writeTree(reads, map[string]string{"commands/r.md.liquid": "r {% include 'p' %}\n", "_partials/p.liquid": "{{ profiles | join: ',' }}"})
			writeTree(noread, map[string]string{"commands/n.md.liquid": "n {{ target }}\n"})
			writeTree(plain, map[string]string{"commands/p.md": "p\n"})
			writeTree(p.UserProfiles, map[string]string{"alpha.yml": "version: 1\nmodules: []\n", "work.yml": "version: 1\nmodules: []\n"})
			_, err := install(engine.Options{Targets: []string{"claude-code"}}, "mods/reads", "mods/noread", "mods/plain")
			Expect(err).NotTo(HaveOccurred())
		})

		It("rendered_with is written for templated modules only, profiles only when read, and an invalid one re-renders", func() {
			Expect(lock()["reads"]).To(HaveKeyWithValue("rendered_with", map[string]any{"profiles": []any{}, "vars": map[string]any{}}))
			Expect(lock()["noread"]).To(HaveKeyWithValue("rendered_with", map[string]any{"vars": map[string]any{}}))
			Expect(lock()["plain"]).NotTo(HaveKey("rendered_with"))

			edited := strings.Replace(read(".lola/lock.json"), `"rendered_with": {
        "vars": {}
      }`, `"rendered_with": {"profiles": ["not a profile!"], "vars": {}}`, 1)
			Expect(edited).NotTo(Equal(read(".lola/lock.json")), "the fixture edits noread's record")
			Expect(os.WriteFile(filepath.Join(proj, ".lola", "lock.json"), []byte(edited), 0o644)).To(Succeed())
			led, err := e.Ledger(sc)
			Expect(err).NotTo(HaveOccurred(), "an invalid rendered_with does not refuse lock.json")
			Expect(led.Modules["noread"].RenderedWith).To(BeNil())

			res, err := turn(engine.ProfileOn, engine.Options{}, "work")
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Changed).To(Equal([]string{"reads"}), "noread renders the same bytes")
			Expect(lock()["noread"]).To(HaveKeyWithValue("rendered_with", map[string]any{"vars": map[string]any{}}), "written again, valid")
		})

		It("lola render and lola sync re-render a module whose rendered_with changed", func() {
			activate(sc, "user:alpha")
			_, err := e.Render(ctx, sc, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(read(".claude/commands/r.md")).To(Equal("r user:alpha\n"))
			Expect(lock()["reads"]).To(HaveKeyWithValue("rendered_with", HaveKeyWithValue("profiles", []any{"user:alpha"})))

			activate(sc)
			_, err = sync(engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(read(".claude/commands/r.md")).To(Equal("r \n"))
			Expect(lock()["reads"]).To(HaveKeyWithValue("rendered_with", HaveKeyWithValue("profiles", []any{})))
		})

		It("a profile switch re-renders exactly the modules whose templates read profiles", func() {
			res, err := turn(engine.ProfileOn, engine.Options{}, "work")
			Expect(err).NotTo(HaveOccurred())
			var touched []string
			for _, st := range res.Steps {
				touched = append(touched, st.Module)
			}
			Expect(slices.Compact(touched)).To(Equal([]string{"reads"}))
			Expect(read(".claude/commands/r.md")).To(Equal("r user:work\n"))
			Expect(lock()["reads"]).To(HaveKeyWithValue("rendered_with", HaveKeyWithValue("profiles", []any{"user:work"})))
		})

		It("a profile switch whose template fails writes nothing", func() {
			writeTree(reads, map[string]string{"commands/r.md.liquid": "{% if profiles contains 'user:work' %}\n{{ nope }}{% endif %}\n"})
			_, err := sync(engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			before, state := files(proj), read(".lola/lock.json")
			_, err = turn(engine.ProfileOn, engine.Options{}, "work")
			Expect(err).To(MatchError(ContainSubstring("commands/r.md.liquid:2: undefined variable in {{ nope }}")))
			Expect(files(proj)).To(Equal(before))
			Expect(read(".lola/lock.json")).To(Equal(state), "the active list is unchanged too")
		})

		It("a profile switch that changes rendered hooks asks consent again, and one that leaves them byte-equal does not", func() {
			hooks := func(command string) string {
				return "---\ndescription: {{ 'g' | json }}\nhooks:\n  Stop:\n    - hooks:\n        - type: command\n          command: " + command + "\n---\n"
			}
			writeTree(filepath.Join(proj, "mods", "hooky"), map[string]string{"agents/h.md.liquid": hooks("{{ profiles | join: '-' | prepend: 'echo ' | json }}") + "H\n"})
			writeTree(filepath.Join(proj, "mods", "steady"), map[string]string{"agents/s.md.liquid": hooks("echo steady") + "S {{ profiles | join: ',' }}\n"})
			_, err := install(engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustRuntimeHooks}, "mods/hooky", "mods/steady")
			Expect(err).NotTo(HaveOccurred())
			var asked []string
			ask := engine.Options{AskRuntimeHooks: func(module string, _ []engine.RuntimeHook) (bool, error) {
				asked = append(asked, module)
				return true, nil
			}}
			_, err = turn(engine.ProfileOn, ask, "work")
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(Equal([]string{"hooky"}))
			Expect(read(".claude/agents/s.md")).To(HaveSuffix("S user:work\n"), "steady rendered again, with the same hooks")
			Expect(read(".claude/agents/h.md")).To(ContainSubstring("echo user:work"))

			// The trust diff renders the version installed with the profiles it was installed under,
			// so a list changed since shows in its hooks.
			activate(sc)
			edited := strings.Replace(read(".lola/project.yml"), "name: hooky\n", "name: hooky\n    rules: false\n", 1)
			Expect(edited).NotTo(Equal(read(".lola/project.yml")), "the fixture changes hooky's entry")
			Expect(os.WriteFile(filepath.Join(proj, ".lola", "project.yml"), []byte(edited), 0o644)).To(Succeed())
			var report engine.TrustReport
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: ask, ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil },
				Trust: func(r engine.TrustReport) (engine.TrustAnswer, error) {
					report = r
					return engine.TrustAnswer{All: true}, nil
				}})
			Expect(err).NotTo(HaveOccurred())
			var diff []string
			for _, c := range report.Changes {
				if c.Module == "hooky" {
					for _, h := range c.Hooks {
						diff = append(diff, h.Change+" "+h.Detail)
					}
				}
			}
			Expect(diff).To(ContainElements(MatchRegexp(`^removed .*echo user:work`), MatchRegexp(`^added .*echo $`)), "%v", diff)
		})
	})

	It("lola install keeps an entry's vars when it rewrites the entry", func() {
		writeTree(filepath.Join(proj, "mods", "kit"), map[string]string{"lola.yml": "name: kit\nvars:\n  language: go\n", "commands/c.md.liquid": "{{ vars.language }}\n"})
		_, err := install(engine.Options{Targets: []string{"claude-code"}}, "mods/kit")
		Expect(err).NotTo(HaveOccurred())
		edited := strings.Replace(read(".lola/project.yml"), "    source: mods/kit\n", "    source: mods/kit\n    vars: # chosen\n      language: rust\n", 1)
		Expect(edited).NotTo(Equal(read(".lola/project.yml")))
		Expect(os.WriteFile(filepath.Join(proj, ".lola", "project.yml"), []byte(edited), 0o644)).To(Succeed())
		_, err = sync(engine.Options{})
		Expect(err).NotTo(HaveOccurred(), "the edit is trusted")

		_, err = install(engine.Options{Targets: []string{"claude-code", "opencode"}}, "mods/kit")
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".lola/project.yml")).To(ContainSubstring("    vars: # chosen\n      language: rust\n"), "kept as written")
		m, err := manifest.Load(filepath.Join(proj, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Find("kit").Vars).To(Equal(map[string]any{"language": "rust"}))
		Expect(m.Find("kit").Targets).To(ConsistOf("claude-code", "opencode"))
		Expect(read(".opencode/commands/c.md")).To(Equal("rust\n"), "and renders with them")

		edited = strings.Replace(read(".lola/project.yml"), "language: rust", "language: zig", 1)
		Expect(os.WriteFile(filepath.Join(proj, ".lola", "project.yml"), []byte(edited), 0o644)).To(Succeed())
		_, err = install(engine.Options{}, "mods/kit")
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".lola/project.yml")).NotTo(ContainSubstring("vars:"), "vars the trust record does not cover are never inherited")
		Expect(read(".claude/commands/c.md")).To(Equal("go\n"))
	})

	It("lola check uses an entry's vars only for the source it checks, and the defaults when the scope's files cannot be read", func() {
		kit := map[string]string{"lola.yml": "name: kit\nvars:\n  language: go\n",
			"commands/c.md.liquid": "{% if vars.language == 'rust' %}{{ rust_only }}{% endif %}\n"}
		writeTree(filepath.Join(proj, "mods", "kit"), kit)
		writeTree(filepath.Join(proj, "other", "kit"), kit)
		writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: mods/kit, vars: {language: rust}}\n"})
		rendersRust := func(arg string) bool {
			GinkgoHelper()
			rep, err := e.Check(ctx, sc, proj, []string{arg}, []string{"claude-code"}, nil)
			Expect(err).NotTo(HaveOccurred())
			return slices.ContainsFunc(rep.Modules[0].Findings, func(f target.Finding) bool { return f.Code == "template-undefined-variable" })
		}
		Expect(rendersRust("mods/kit")).To(BeTrue(), "the entry's source")
		Expect(rendersRust(filepath.Join(proj, "mods", "kit"))).To(BeTrue(), "however it is written")
		Expect(rendersRust("other/kit")).To(BeFalse(), "a module of the same name from another source gets its defaults")
		_, err := e.AddSource(ctx, proj, "mods/kit", "", "", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rendersRust("kit")).To(BeTrue(), "the cache's kit came from the entry's source")

		writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\nmodules: [\n"})
		rep, err := e.Check(ctx, sc, proj, []string{"mods/kit"}, []string{"claude-code"}, nil)
		Expect(err).NotTo(HaveOccurred(), "an unreadable project.yml never stops lola check")
		Expect(rep.Findings).To(ContainElement(And(HaveField("Severity", target.SeverityInfo), HaveField("Code", "vars-defaults"),
			HaveField("Text", HaveSuffix("; lola check renders every module with its default vars")))))
		Expect(rep.Modules[0].Findings).NotTo(ContainElement(HaveField("Code", "template-undefined-variable")))
	})

	It("lola check warns on an attrs path no attribute file defines for any target", func() {
		writeTree(filepath.Join(proj, "mods", "kit"), map[string]string{
			"agents/a.md.liquid": "---\ndescription: {{ attrs.model | default: 'a' }}\n---\n{{ attrs.modle }} {{ attrs.opts.depth }} {{ attrs.opts.dpeth }}\n" +
				"{{ attrs['tools'][0] }} {{ attrs.tools.size }} {{ attrs.opts.size }} {{ attrs.none }} {% for k in attrs %}{{ attrs[k] }}{% endfor %}\n",
			"agents/a.targets.yaml":    "claude-code:\n  model: sonnet\n  opts: {depth: 2}\n  none: null\n",
			"commands/c.md.liquid":     "{% include 'p' %}{{ 'attrs.quoted' }}",
			"commands/c.targets.jsonc": `{"opencode": {"tools": ["read"]}}`,
			"_partials/p.liquid":       "\n{{ attrs.gone.deep }}{% raw %}{{ attrs.raw }}{% endraw %}",
		})
		rep, err := e.Check(ctx, sc, proj, []string{"mods/kit"}, []string{"claude-code", "opencode"}, nil)
		Expect(err).NotTo(HaveOccurred())
		var undefined []target.Finding
		for _, f := range rep.Modules[0].Findings {
			if f.Code == "attrs-undefined" {
				undefined = append(undefined, f)
			}
		}
		warn := func(text string) target.Finding {
			return target.Finding{Severity: target.SeverityWarn, Module: "kit", Kind: "module", Code: "attrs-undefined", Text: text}
		}
		Expect(undefined).To(Equal([]target.Finding{
			warn("_partials/p.liquid:2: attrs.gone.deep is defined by no attribute file of kit, for any target; it always reads as nil"),
			warn("agents/a.md.liquid:4: attrs.modle is defined by no attribute file of kit, for any target; it always reads as nil"),
			warn("agents/a.md.liquid:4: attrs.opts.dpeth is defined by no attribute file of kit, for any target; it always reads as nil"),
		}), "a key one target's file defines is defined; size, list indexes and computed keys are not checked")
		Expect(rep.Modules[0].Findings).NotTo(ContainElement(HaveField("Severity", target.SeveritySkip)), "a warning, never a refusal")

		By("a control character in the path reaches the finding only as untrusted.Text prints it; the loader reads no file so named")
		writeTree(filepath.Join(proj, "mods", "esc"), map[string]string{
			"commands/c.md.liquid":      "{{ attrs['red\x1b[31m'] }}{% include 'p' %}",
			"_partials/p.liquid":        "\n{{ attrs.gone }}",
			"_partials/q\x1b[2J.liquid": "{{ attrs.ignored }}",
		})
		rep, err = e.Check(ctx, sc, proj, []string{"mods/esc"}, []string{"claude-code"}, nil)
		Expect(err).NotTo(HaveOccurred())
		var texts []string
		for _, f := range rep.Modules[0].Findings {
			if f.Code == "attrs-undefined" {
				texts = append(texts, f.Text)
			}
		}
		Expect(texts).To(Equal([]string{
			"_partials/p.liquid:2: attrs.gone is defined by no attribute file of esc, for any target; it always reads as nil",
			"commands/c.md.liquid:1: attrs.red?[31m is defined by no attribute file of esc, for any target; it always reads as nil",
		}))
	})

	It("a bad override refuses in the trust diff as it is, not as a runtime hooks failure", func() {
		writeTree(filepath.Join(proj, "mods", "kit"), map[string]string{"lola.yml": "name: kit\nvars:\n  language: go\n",
			"commands/c.md.liquid": "{{ vars.language }}\n"})
		writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: mods/kit, vars: {lang: rust}}\n"})
		asked := false
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil },
			Trust: func(engine.TrustReport) (engine.TrustAnswer, error) {
				asked = true
				return engine.TrustAnswer{All: true}, nil
			}})
		Expect(err).To(MatchError("project.yml: modules[kit].vars.lang: kit declares no var lang; it declares language"))
		Expect(asked).To(BeFalse(), "the diff of the entry to trust refused, before the question")
	})

	It("an override of an undeclared var or of another type refuses, naming the entry and the declared vars", func() {
		kit := filepath.Join(proj, "mods", "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\nvars:\n  language: go\n  strict: false\n  tools: [rg]\n",
			"commands/c.md.liquid": "{{ vars.language }}\n"})
		writeTree(filepath.Join(proj, "mods", "bare"), map[string]string{"commands/b.md": "b\n"})
		_, err := e.AddSource(ctx, proj, "mods/kit", "", "", nil) // lola info shows the version refused
		Expect(err).NotTo(HaveOccurred())
		for vars, want := range map[string]string{
			"{lang: rust}":           "project.yml: modules[kit].vars.lang: kit declares no var lang; it declares language, strict, tools",
			"{strict: 'yes'}":        "project.yml: modules[kit].vars.strict: kit's default is true or false; this is a string",
			"{tools: rg}":            "project.yml: modules[kit].vars.tools: kit's default is a list of strings; this is a string",
			"{language: [go, rust]}": "project.yml: modules[kit].vars.language: kit's default is a string; this is a list of strings",
		} {
			writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: mods/kit, vars: " + vars + "}\n"})
			_, err := sync(engine.Options{Display: []string{"--json"}})
			Expect(err).To(MatchError(want), vars)
			Expect(errs.Hints(err)).To(Equal([]string{"lola info --project kit --json"}))
			Expect(exists(".claude/commands/c.md")).To(BeFalse(), "nothing renders")

			rep, err := e.Check(ctx, sc, proj, []string{"mods/kit"}, nil, nil)
			Expect(err).NotTo(HaveOccurred(), "lola check reports a bad override and goes on")
			Expect(rep.Modules[0].Findings).To(ContainElement(target.Finding{Severity: target.SeveritySkip, Module: "kit", Kind: "module",
				Code: "vars-override", Text: want}))
			Expect(rep.Modules[0].Targets).NotTo(BeEmpty(), "rendered with its defaults")
		}
		writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: bare, source: mods/bare, vars: {x: y}}\n"})
		_, err = sync(engine.Options{})
		Expect(err).To(MatchError("project.yml: modules[bare].vars.x: bare declares no var x; it declares none"))
		Expect(errs.Hints(err)).To(BeEmpty(), "bare is not in the cache, so no command shows its vars")

		writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: mods/kit, vars: {language: rust, strict: true, tools: []}}\n"})
		_, err = sync(engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".claude/commands/c.md")).To(Equal("rust\n"))
		rep, err := e.Check(ctx, sc, proj, []string{"mods/kit"}, nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rep.Modules[0].Findings).NotTo(ContainElement(HaveField("Severity", target.SeveritySkip)))
	})

	It("a vars-only edit re-renders on lola sync and needs re-trust in a project manifest, showing a vars change", func() {
		writeTree(filepath.Join(proj, "mods", "kit"), map[string]string{"lola.yml": "name: kit\nvars:\n  language: go\n", "commands/c.md.liquid": "{{ vars.language }}\n"})
		_, err := install(engine.Options{Targets: []string{"claude-code"}}, "mods/kit")
		Expect(err).NotTo(HaveOccurred())
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["kit"].RenderedWith).To(Equal(&ledger.RenderedWith{Vars: map[string]any{}}), "no overrides")

		edited := strings.Replace(read(".lola/project.yml"), "    source: mods/kit\n", "    source: mods/kit\n    vars: {language: rust}\n", 1)
		Expect(os.WriteFile(filepath.Join(proj, ".lola", "project.yml"), []byte(edited), 0o644)).To(Succeed())
		var report engine.TrustReport
		answer := engine.TrustAnswer{}
		syncAsking := func() error {
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil },
				Trust: func(r engine.TrustReport) (engine.TrustAnswer, error) {
					report = r
					return answer, nil
				}})
			return err
		}
		Expect(syncAsking()).To(HaveOccurred(), "declined")
		Expect(report.Changes).To(ContainElement(HaveField("Change.Fields", ContainElement(trust.FieldChange{Field: "vars", To: `language = "rust"`}))))
		Expect(read(".claude/commands/c.md")).To(Equal("go\n"), "nothing renders before the answer")

		answer = engine.TrustAnswer{All: true}
		Expect(syncAsking()).To(Succeed())
		Expect(read(".claude/commands/c.md")).To(Equal("rust\n"))
		led, err = e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["kit"].RenderedWith).To(Equal(&ledger.RenderedWith{Vars: map[string]any{"language": "rust"}}))

		report = engine.TrustReport{}
		Expect(syncAsking()).To(Succeed())
		Expect(report.Changes).To(BeEmpty(), "trusted now")

		// lock.json is editable: vars it records that no entry could set are read as unknown.
		raw := strings.Replace(read(".lola/lock.json"), `"language": "rust"`, `"language": 1e999`, 1)
		Expect(raw).NotTo(Equal(read(".lola/lock.json")))
		Expect(os.WriteFile(filepath.Join(proj, ".lola", "lock.json"), []byte(raw), 0o644)).To(Succeed())
		led, err = e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["kit"].RenderedWith).To(BeNil())
	})

	It("finite decimals in attribute files and numbers in vars render, and NaN and infinity refuse", func() {
		writeTree(filepath.Join(proj, "mods", "num"), map[string]string{
			"lola.yml": "name: num\nvars:\n  ratio: 0.7\n  count: 3\n  neg: -0.0\n  one: 1.0\n",
			"agents/rev.md.liquid": "---\ndescription: {{ 'r' | json }}\n---\n{{ vars.ratio }} {{ vars.count }} {{ vars.neg }} {{ vars.neg | json }} {{ vars.one }} " +
				"{{ vars.ratio | times: 2 }} {{ attrs.temperature }} {{ attrs.temperature | json }}\n",
			"agents/rev.targets.yaml":  "opencode:\n  temperature: 0.2\nclaude-code:\n  temperature: 0.25\n",
			"agents/lit.md":            "---\ndescription: l\n---\nL\n",
			"agents/lit.targets.jsonc": `{"opencode": {"temperature": 1e-1, "steps": 2}}`,
		})
		_, err := install(engine.Options{Targets: []string{"claude-code", "opencode"}}, "mods/num")
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".claude/agents/rev.md")).To(HaveSuffix("---\n0.7 3 -0 -0 1 1.4 0.25 0.25\n"))
		Expect(read(".opencode/agents/rev.md")).To(ContainSubstring("\ntemperature: 0.2\n"), "a merged float is a YAML number")
		Expect(read(".opencode/agents/lit.md")).To(ContainSubstring("\ntemperature: 0.1\n"))
		Expect(read(".opencode/agents/lit.md")).To(ContainSubstring("\nsteps: 2\n"))

		for file, body := range map[string]string{
			"agents/rev.targets.yaml":  "claude-code:\n  temperature: .nan\n",
			"agents/lit.targets.jsonc": `{"claude-code": {"temperature": 1e999}}`,
		} {
			mod := filepath.Join(proj, "mods", "bad"+strings.TrimPrefix(filepath.Ext(file), "."))
			writeTree(mod, map[string]string{"agents/rev.md": "---\ndescription: r\n---\nR\n", "agents/lit.md": "---\ndescription: l\n---\nL\n", file: body})
			_, err := install(engine.Options{Targets: []string{"claude-code"}}, mod)
			Expect(err).To(MatchError(ContainSubstring(file+":")), file)
			Expect(err).To(MatchError(ContainSubstring("claude-code")), file)
			Expect(err).To(MatchError(ContainSubstring("temperature: NaN and infinity are not supported")), file)
		}
	})

	It("a number var's override must be a number, and cache info, rendered_with and the trust prompt show it", func() {
		writeTree(filepath.Join(proj, "mods", "kit"), map[string]string{"lola.yml": "name: kit\nvars:\n  temperature: 0.7\n",
			"commands/c.md.liquid": "{{ vars.temperature }}\n"})
		_, err := e.AddSource(ctx, proj, "mods/kit", "", "", nil)
		Expect(err).NotTo(HaveOccurred())
		project := func(vars string) {
			writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: mods/kit, vars: " + vars + "}\n"})
		}
		project("{temperature: hot}")
		_, err = sync(engine.Options{})
		Expect(err).To(MatchError("project.yml: modules[kit].vars.temperature: kit's default is a number; this is a string"))
		Expect(errs.Hints(err)).To(Equal([]string{"lola info --project kit"}))

		project("{temperature: 1}")
		_, err = sync(engine.Options{})
		Expect(err).NotTo(HaveOccurred(), "an integer overrides a float default: one kind")
		Expect(read(".claude/commands/c.md")).To(Equal("1\n"))

		project("{temperature: 2.0}")
		var report engine.TrustReport
		syncAsking := func() error {
			report = engine.TrustReport{}
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil },
				Trust: func(r engine.TrustReport) (engine.TrustAnswer, error) {
					report = r
					return engine.TrustAnswer{All: true}, nil
				}})
			return err
		}
		Expect(syncAsking()).To(Succeed())
		Expect(report.Changes).To(ContainElement(HaveField("Change.Fields", ContainElement(trust.FieldChange{Field: "vars", From: "temperature = 1", To: "temperature = 2.0"}))))
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["kit"].RenderedWith).To(Equal(&ledger.RenderedWith{Vars: map[string]any{"temperature": 2.0}}), "a stored 2.0 reads back as a float")
		Expect(read(".lola/lock.json")).To(ContainSubstring(`"temperature": 2.0`))
		Expect(syncAsking()).To(Succeed())
		Expect(report.Changes).To(BeEmpty(), "the trust record round-trips 2.0")

		project("{temperature: 2}")
		Expect(syncAsking()).To(Succeed())
		Expect(report.Changes).To(ContainElement(HaveField("Change.Fields", ContainElement(trust.FieldChange{Field: "vars", From: "temperature = 2.0", To: "temperature = 2"}))),
			"a 2.0 → 2 edit asks trust again")
		led, err = e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["kit"].RenderedWith).To(Equal(&ledger.RenderedWith{Vars: map[string]any{"temperature": int64(2)}}), "and re-renders")

		project("{temperature: 0.25}")
		Expect(syncAsking()).To(Succeed())
		Expect(report.Changes).To(ContainElement(HaveField("Change.Fields", ContainElement(trust.FieldChange{Field: "vars", From: "temperature = 2", To: "temperature = 0.25"}))))
		Expect(read(".claude/commands/c.md")).To(Equal("0.25\n"))
		led, err = e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules["kit"].RenderedWith).To(Equal(&ledger.RenderedWith{Vars: map[string]any{"temperature": 0.25}}))
		Expect(syncAsking()).To(Succeed())
		Expect(report.Changes).To(BeEmpty(), "the trust record round-trips the float")
		Expect(read(".lola/project.yml")).To(ContainSubstring("vars: {temperature: 0.25}"))
	})

	It("the trust diff compares with the version installed rendered with only the lock.json vars it declares", func() {
		writeTree(filepath.Join(proj, "mods", "hooky"), map[string]string{
			"lola.yml":           "name: hooky\nvars:\n  cmd: echo one\n",
			"agents/h.md.liquid": "---\ndescription: {{ 'h' | json }}\nhooks:\n  Stop:\n    - hooks:\n        - type: command\n          command: {{ vars.cmd | json }}\n---\nH\n"})
		_, err := install(engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustRuntimeHooks}, "mods/hooky")
		Expect(err).NotTo(HaveOccurred())
		raw := strings.Replace(read(".lola/lock.json"), `"vars": {}`, `"vars": {"nope": "x", "cmd": ["not", "a", "string"]}`, 1)
		Expect(raw).NotTo(Equal(read(".lola/lock.json")))
		Expect(os.WriteFile(filepath.Join(proj, ".lola", "lock.json"), []byte(raw), 0o644)).To(Succeed())
		writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - name: hooky\n    source: mods/hooky\n    vars: {cmd: echo two}\n"})
		var report engine.TrustReport
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustRuntimeHooks},
			ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil },
			Trust: func(r engine.TrustReport) (engine.TrustAnswer, error) {
				report = r
				return engine.TrustAnswer{All: true}, nil
			}})
		Expect(err).NotTo(HaveOccurred())
		Expect(report.Changes).To(HaveLen(1))
		Expect(report.Changes[0].FilesNote).To(BeEmpty(), "the version installed is read")
		var diff []string
		for _, h := range report.Changes[0].Hooks {
			diff = append(diff, h.Change+" "+h.Detail)
		}
		Expect(diff).To(ContainElements(MatchRegexp(`^removed .*echo one`), MatchRegexp(`^added .*echo two`)), "%v", diff)
	})

	It("the trust diff renders a version installed without a valid rendered_with with the defaults", func() {
		writeTree(filepath.Join(proj, "mods", "hooky"), map[string]string{
			"lola.yml":           "name: hooky\nvars:\n  cmd: echo one\n  note: a\n",
			"agents/h.md.liquid": "---\ndescription: {{ vars.note | json }}\nhooks:\n  Stop:\n    - hooks:\n        - type: command\n          command: {{ vars.cmd | json }}\n---\nH\n"})
		setVars := func(vars string) {
			GinkgoHelper()
			writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - name: hooky\n    source: mods/hooky\n    vars: " + vars + "\n"})
		}
		setVars("{cmd: echo two}")
		_, err := sync(engine.Options{Trust: engine.TrustRuntimeHooks})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".claude/agents/h.md")).To(ContainSubstring("echo two"))

		var l map[string]any
		Expect(json.Unmarshal([]byte(read(".lola/lock.json")), &l)).To(Succeed())
		hooky := l["modules"].(map[string]any)["hooky"].(map[string]any)
		Expect(hooky).To(HaveKey("rendered_with"))
		delete(hooky, "rendered_with")
		b, err := json.Marshal(l)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(proj, ".lola", "lock.json"), b, 0o644)).To(Succeed())

		setVars("{cmd: echo two, note: b}")
		var report engine.TrustReport
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustRuntimeHooks},
			ConfirmRemovals: func([]engine.Removal) (bool, error) { return true, nil },
			Trust: func(r engine.TrustReport) (engine.TrustAnswer, error) {
				report = r
				return engine.TrustAnswer{All: true}, nil
			}})
		Expect(err).NotTo(HaveOccurred())
		var diff []string
		for _, c := range report.Changes {
			for _, h := range c.Hooks {
				diff = append(diff, h.Change+" "+h.Detail)
			}
		}
		Expect(diff).To(ContainElements(MatchRegexp(`^removed .*echo one`), MatchRegexp(`^added .*echo two`)),
			"what it was installed with is unknown: compared as the defaults render, never as the new entry's vars: %v", diff)
	})

	It("a var edit that changes rendered hooks asks consent again", func() {
		writeTree(filepath.Join(proj, "mods", "hooky"), map[string]string{
			"lola.yml":           "name: hooky\nvars:\n  cmd: echo one\n  note: a\n",
			"agents/h.md.liquid": "---\ndescription: {{ vars.note | json }}\nhooks:\n  Stop:\n    - hooks:\n        - type: command\n          command: {{ vars.cmd | json }}\n---\nH\n"})
		_, err := install(engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustRuntimeHooks}, "mods/hooky")
		Expect(err).NotTo(HaveOccurred())
		var asked []string
		ask := engine.Options{AskRuntimeHooks: func(module string, _ []engine.RuntimeHook) (bool, error) {
			asked = append(asked, module)
			return true, nil
		}}
		setVars := func(vars string) {
			GinkgoHelper()
			writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - name: hooky\n    source: mods/hooky\n    vars: " + vars + "\n"})
		}
		setVars("{note: b}")
		_, err = sync(ask)
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(BeEmpty(), "the hooks render byte-equal")
		Expect(read(".claude/agents/h.md")).To(ContainSubstring("description: \"b\""))

		setVars("{note: b, cmd: echo two}")
		_, err = sync(ask)
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(Equal([]string{"hooky"}))
		Expect(read(".claude/agents/h.md")).To(ContainSubstring("echo two"))
	})

	Describe("attribute files (Plan 7.4)", func() {
		both := engine.Options{Targets: []string{"claude-code", "opencode"}}
		frontmatter := func(rel string) map[string]any {
			GinkgoHelper()
			doc, err := module.ParseDoc([]byte(read(rel)))
			Expect(err).NotTo(HaveOccurred())
			return doc.Frontmatter
		}

		It("attributes replace frontmatter keys one level deep, null removes, comments and order stay", func() {
			writeTree(src, map[string]string{
				"agents/reviewer.md": "---\n# who\ndescription: Reviews\nmodel: opus\ntools: [Read]\nextra: {a: 1, b: 2}\n---\nReview.\n",
				"agents/reviewer.targets.yaml": "claude-code:\n  model: sonnet\n  tools: null\n  extra: {c: 3}\n  color: blue\n" +
					"opencode:\n  model: anthropic/sonnet\n  tools: null\n  absent: null\n",
			})
			_, err := install(both, "../src/kit")
			Expect(err).NotTo(HaveOccurred())
			Expect(read(".claude/agents/reviewer.md")).To(Equal("---\nname: reviewer\n# who\ndescription: Reviews\nmodel: sonnet\nextra:\n  c: 3\ncolor: blue\n---\nReview.\n"),
				"a key is replaced whole, a removed one goes, a new one follows the file's own, and the comment stays")
			Expect(frontmatter(".opencode/agents/reviewer.md")).To(Equal(map[string]any{
				"description": "Reviews", "model": "anthropic/sonnet", "extra": map[string]any{"a": 1, "b": 2}, "mode": "subagent"}))

			writeTree(src, map[string]string{"agents/merge.md": "---\ndescription: M\n---\nM.\n",
				"agents/merge.targets.jsonc": `{"claude-code": {"<<": {"color": "red"}, "tone": "<<"}}`})
			_, err = install(both, "../src/kit")
			Expect(err).NotTo(HaveOccurred())
			Expect(frontmatter(".claude/agents/merge.md")).To(Equal(map[string]any{"name": "merge", "description": "M",
				"<<": map[string]any{"color": "red"}, "tone": "<<"}), "a << key or value is written quoted, never as a merge key")
		})

		It("a template's rendered frontmatter wins over attributes, which fill only missing keys", func() {
			writeTree(src, map[string]string{
				"agents/critic.md.liquid":    "---\ndescription: {{ 'Critic' | json }}\nmodel: opus\n---\nCriticize.\n",
				"agents/critic.targets.yaml": "claude-code:\n  model: sonnet\n  color: blue\n  description: null\n  gone: null\n",
			})
			_, err := install(both, "../src/kit")
			Expect(err).NotTo(HaveOccurred())
			Expect(frontmatter(".claude/agents/critic.md")).To(Equal(map[string]any{"name": "critic", "description": "Critic", "model": "opus", "color": "blue"}))
			Expect(frontmatter(".opencode/agents/critic.md")).To(Equal(map[string]any{"description": "Critic", "mode": "subagent"}), "opencode drops a model not in its provider/model form")
		})

		It("attrs binds the target's attributes, also in partials", func() {
			writeTree(src, map[string]string{
				"commands/ship.md.liquid":    "---\ndescription: {{ attrs.model | json }}\n---\n{% include 'p' %}\n",
				"commands/ship.targets.yaml": "claude-code: {model: sonnet}\nopencode: {model: big, color: red}\n",
				"commands/plain.md.liquid":   "{% if attrs contains 'model' %}has{% else %}none{% endif %} {% for k in attrs %}{{ k }}{% endfor %}\n",
				"_partials/p.liquid":         "{{ attrs.model }}{% if attrs contains 'color' %} {{ attrs.color }}{% endif %} {% for k in attrs %}{{ k }},{% endfor %}",
			})
			_, err := install(both, "../src/kit")
			Expect(err).NotTo(HaveOccurred())
			Expect(read(".claude/commands/ship.md")).To(Equal("---\ndescription: \"sonnet\"\nmodel: sonnet\n---\nsonnet model,\n"))
			Expect(read(".opencode/commands/ship.md")).To(HaveSuffix("---\nbig red color,model,\n"))
			Expect(read(".claude/commands/plain.md")).To(Equal("none \n"), "a component without an attribute file binds {}")
		})

		It("hooks from an attribute file are checked and consented as frontmatter hooks", func() {
			writeTree(src, map[string]string{
				"agents/guard.md":           "---\ndescription: Guard\n---\nGuard.\n",
				"agents/guard.targets.yaml": "claude-code:\n  hooks:\n    Stop:\n      - hooks:\n          - type: command\n            command: echo hi\n",
			})
			res, err := install(engine.Options{Targets: []string{"claude-code", "opencode"}, Trust: engine.TrustRuntimeHooks}, "../src/kit")
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Modules[0].RuntimeHooks).To(ConsistOf(And(HaveField("Target", "claude-code"), HaveField("Component", "agent guard"),
				HaveField("Command", HaveSuffix("; echo hi")))))
			Expect(read(".claude/agents/guard.md")).To(ContainSubstring("; echo hi"))
			Expect(read(".opencode/agents/guard.md")).NotTo(ContainSubstring("echo hi"))

			writeTree(src, map[string]string{"agents/guard.targets.yaml": "opencode:\n  hooks: {Nope: []}\n"})
			_, err = install(engine.Options{Targets: []string{"claude-code", "opencode"}, Trust: engine.TrustRuntimeHooks}, "../src/kit")
			te, ok := errors.AsType[*engine.TemplateError](err)
			Expect(ok).To(BeTrue(), "%v", err)
			Expect(te.Code).To(Equal("template-rendered-check"))
			Expect(te.File).To(Equal("agents/guard.targets.yaml"))
			Expect(te.Error()).To(HavePrefix("kit: opencode agent guard: agents/guard.targets.yaml (merged for opencode): agent guard: frontmatter hooks: "),
				"a literal component's attributes are merged, not rendered")
			Expect(te.JSONFields()["template"]).To(HaveKeyWithValue("rendered", true), "JSON keeps rendered (R-7-21)")

			// A declined consent writes no hook from an attribute file.
			writeTree(src, map[string]string{"agents/guard.targets.yaml": "claude-code:\n  hooks:\n    Stop:\n      - hooks:\n          - type: command\n            command: echo declined\n"})
			var asked []string
			_, err = install(engine.Options{Targets: []string{"claude-code"}, AskRuntimeHooks: func(module string, _ []engine.RuntimeHook) (bool, error) {
				asked = append(asked, module)
				return false, nil
			}}, "../src/kit")
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(Equal([]string{"kit"}))
			Expect(exists(".claude/agents/guard.md")).To(BeFalse(), "declined: the agent and its hook are not written")

			// A template's rendered frontmatter and its attribute file's hooks: the hooks the file adds
			// are consented as the template's own; when they refuse, the refusal names the file.
			Expect(os.Remove(filepath.Join(src, "agents", "guard.md"))).To(Succeed())
			writeTree(src, map[string]string{
				"agents/guard.md.liquid":    "---\ndescription: {{ 'Guard' | json }}\n---\nGuard.\n",
				"agents/guard.targets.yaml": "claude-code:\n  hooks:\n    Stop:\n      - hooks:\n          - type: command\n            command: echo templated\n",
			})
			res, err = install(engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustRuntimeHooks}, "../src/kit")
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Modules[0].RuntimeHooks).To(ConsistOf(And(HaveField("Component", "agent guard"), HaveField("Command", HaveSuffix("; echo templated")))))
			Expect(read(".claude/agents/guard.md")).To(ContainSubstring("; echo templated"))

			writeTree(src, map[string]string{"agents/guard.targets.yaml": "claude-code:\n  hooks: {Nope: []}\n"})
			_, err = install(engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustRuntimeHooks}, "../src/kit")
			te, ok = errors.AsType[*engine.TemplateError](err)
			Expect(ok).To(BeTrue(), "%v", err)
			Expect(te.File).To(Equal("agents/guard.targets.yaml"), "the keys the file added refuse, not the template")
			Expect(te.Error()).To(HavePrefix("kit: claude-code agent guard: agents/guard.targets.yaml (merged for claude-code): agent guard: frontmatter hooks: "))

			writeTree(src, map[string]string{"agents/guard.md.liquid": "---\ndescription: {{ 'Guard' | json }}\nhooks: {Nope: []}\n---\nGuard.\n",
				"agents/guard.targets.yaml": "claude-code:\n  model: sonnet\n"})
			_, err = install(engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustRuntimeHooks}, "../src/kit")
			te, ok = errors.AsType[*engine.TemplateError](err)
			Expect(ok).To(BeTrue(), "%v", err)
			Expect(te.Error()).To(HavePrefix("kit: claude-code agent guard: agents/guard.md.liquid (rendered for claude-code): agent guard: frontmatter hooks: "),
				"the template's own keys refuse as rendered, even after the file added one")
		})
	})
})

// cachedNames lists the modules in e's cache.
func cachedNames(e *engine.Engine) []string {
	GinkgoHelper()
	names, err := e.Store.Names()
	Expect(err).NotTo(HaveOccurred())
	return names
}
