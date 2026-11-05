package engine_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/target/claude"
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

// spy is claude-code recording the scope and root the engine gives Detect and Plan.
type spy struct {
	claude.Target
	seen *[]string
}

func (s spy) Detect(scope target.Scope, root string) bool {
	*s.seen = append(*s.seen, "detect "+scope.String()+" "+root)
	return s.Target.Detect(scope, root)
}

func (s spy) Plan(m *module.Module, ctx target.PlanContext) ([]plan.Intent, []target.Finding, error) {
	*s.seen = append(*s.seen, "plan "+ctx.Scope.String()+" "+ctx.Root)
	return s.Target.Plan(m, ctx)
}

// loose is claude-code with a version pattern that matches anywhere in the output.
type loose struct{ claude.Target }

func (loose) Probe() target.Probe {
	return target.Probe{Binaries: []string{"claude"}, VersionArgs: []string{"--version"}, VersionRE: regexp.MustCompile(`(\d+\.\d+\.\d+)`)}
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
		e.Getwd = func() (string, error) { return proj, nil } // hints are written for a user in the project
		src = filepath.Join(tmp, "src", "example")
		writeTree(src, exampleModule)
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
	})

	It("records a local source relative to the canonical project root and caches one canonical location", func() {
		real := filepath.Join(tmp, "real")
		writeTree(filepath.Join(real, "mods", "example"), exampleModule)
		link := filepath.Join(tmp, "link")
		Expect(os.Symlink(real, link)).To(Succeed())
		// A cache entry an earlier lola recorded through the link still names this folder.
		_, err := e.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: filepath.Join(link, "mods", "example")})
		Expect(err).NotTo(HaveOccurred())

		for _, root := range []string{link, real} {
			_, err := e.Install(ctx, engine.Scope{Kind: target.Project, ProjectDir: root}, root, []engine.InstallRequest{{Arg: "mods/example"}}, claude)
			Expect(err).NotTo(HaveOccurred(), root)
			man, err := manifest.Load(filepath.Join(real, ".lola", "project.yml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(man.Find("example")).To(HaveField("Source", "mods/example"), root)
		}
		_, err = e.Install(ctx, engine.Scope{Kind: target.Project, ProjectDir: link}, link,
			[]engine.InstallRequest{{Arg: filepath.Join(link, "mods", "example")}}, claude)
		Expect(err).NotTo(HaveOccurred(), "an absolute path through the link is the cached folder too")
	})

	It("resolves a relative source recorded through a symlinked root again once the cache lost the module", func() {
		// The link is deeper than its target, so the recorded source climbs above the lexical root.
		real := filepath.Join(tmp, "data", "proj")
		Expect(os.MkdirAll(real, 0o755)).To(Succeed())
		link := filepath.Join(tmp, "home", "me", "proj")
		Expect(os.MkdirAll(filepath.Dir(link), 0o755)).To(Succeed())
		Expect(os.Symlink(real, link)).To(Succeed())
		writeTree(filepath.Join(tmp, "home", "me", "shared", "example"), exampleModule)
		lsc := engine.Scope{Kind: target.Project, ProjectDir: link}

		_, err := e.Install(ctx, lsc, link, []engine.InstallRequest{{Arg: "../shared/example"}}, claude)
		Expect(err).NotTo(HaveOccurred())
		removed, err := e.Store.Remove("example")
		Expect(err).NotTo(HaveOccurred())
		Expect(removed).To(BeTrue())

		_, err = e.Render(ctx, lsc, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		_, err = e.Install(ctx, lsc, link, []engine.InstallRequest{{Arg: "example"}}, claude)
		Expect(err).NotTo(HaveOccurred())
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

	It("installs a Claude Code plugin from a folder, reporting its declined runtime hooks and unsupported keys", func() {
		plugin := filepath.Join(tmp, "src", "deploy")
		writeTree(plugin, map[string]string{
			".claude-plugin/plugin.json": `{"name":"deploy","commands":{"about":{"content":"About ${CLAUDE_PLUGIN_ROOT}","description":"About"}},"lspServers":{}}`,
			"skills/review/SKILL.md":     "---\ndescription: Review\n---\nReview.\n",
			".mcp.json":                  `{"mcpServers":{"gh":{"command":"${CLAUDE_PLUGIN_ROOT}/bin/gh","env":{"D":"${CLAUDE_PLUGIN_DATA}"}}}}`,
			"hooks/hooks.json":           `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"}]}]}}`,
		})
		res, err := install(claude, plugin)
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".claude/skills/review/SKILL.md")).To(Equal("---\ndescription: Review\n---\nReview.\n"))
		Expect(read(".claude/commands/about.md")).To(Equal("---\ndescription: About\n---\nAbout " + filepath.Join(proj, ".lola/modules/deploy")))
		Expect(read(".mcp.json")).To(MatchJSON(`{"mcpServers":{"gh":{"command":"` + filepath.Join(proj, ".lola/modules/deploy/bin/gh") +
			`","env":{"D":"` + filepath.Join(proj, ".lola/plugin-data/deploy") + `"}}}}`))
		Expect(res.Findings).To(ContainElements(
			SatisfyAll(HaveField("Code", "claude-code/runtime-hooks-declined"), HaveField("Kind", "runtime-hook"), HaveField("Severity", target.SeverityWarn)),
			SatisfyAll(HaveField("Code", "module-warning"), HaveField("Text", ContainSubstring("lspServers is not supported")))))
	})

	It("agent-plugins client namespaces reach only their target", func() {
		pkg := filepath.Join(tmp, "src", "tools")
		writeTree(pkg, map[string]string{
			"plugin.json":                            `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"tools"}`,
			"skills/deploy/SKILL.md":                 "---\nname: deploy\ndescription: Deploy\n---\nDeploy.\n",
			"com.anthropic.claude/commands/cc.md":    "Claude.\n",
			"com.anthropic.claude-code/agents/ca.md": "---\ndescription: Claude agent\n---\nAgent.\n",
			"ai.opencode/commands/oc.md":             "OpenCode.\n",
			"com.google.gemini-cli/commands/gm.md":   "---\ndescription: Gemini\n---\nGemini.\n",
			"dev.getlola/commands/all.md":            "---\ndescription: All\n---\nAll.\n",
		})
		_, err := install(engine.Options{Targets: []string{"claude-code", "gemini-cli", "opencode"}}, pkg)
		Expect(err).NotTo(HaveOccurred())
		var commands []string
		for _, f := range tree(proj) {
			if !strings.HasPrefix(f, ".lola/") && (strings.Contains(f, "/commands/") || strings.Contains(f, "/agents/") || strings.Contains(f, "/skills/")) {
				commands = append(commands, f)
			}
		}
		Expect(commands).To(ConsistOf(
			".claude/skills/deploy/SKILL.md", ".gemini/skills/deploy/SKILL.md", ".opencode/skills/deploy/SKILL.md",
			".claude/commands/cc.md", ".claude/agents/ca.md",
			".opencode/commands/oc.md",
			".gemini/commands/gm.toml",
			".claude/commands/all.md", ".gemini/commands/all.toml", ".opencode/commands/all.md",
		))
	})

	It("installs once when a target is named twice", func() {
		_, err := install(engine.Options{Targets: []string{"claude-code", "claude-code"}}, src)
		Expect(err).NotTo(HaveOccurred())
		Expect(status()).To(Equal([]string{"example/claude-code=ok"}))
	})

	It("installs into Gemini CLI next to the user's own settings, and leaves settings.json byte for byte on uninstall", func() {
		settings := "{\n  // my theme\n  \"theme\": \"dark\",\n  \"mcpServers\": {\n    \"mine\": {\"command\": \"x\"} // keep\n  }\n}\n"
		writeTree(proj, map[string]string{".gemini/settings.json": settings})
		res, err := install(engine.Options{Targets: []string{"gemini-cli"}}, src)
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".gemini/skills/review/SKILL.md")).To(Equal("---\nname: review\ndescription: Review code\n---\nReview it.\n"))
		Expect(read(".gemini/skills/review/ref/checks.md")).To(Equal("checks"))
		Expect(read(".gemini/commands/ship.toml")).To(Equal("prompt = \"Ship {{args}}\\n\"\n"))
		Expect(read(".gemini/agents/critic.md")).To(Equal("---\nname: critic\ndescription: critic\n---\nCriticize.\n"))
		Expect(read(".gemini/settings.json")).To(SatisfyAll(ContainSubstring("// my theme"), ContainSubstring(`"mine": {"command": "x"}, // keep`),
			ContainSubstring(`"lola-gh": {`), ContainSubstring(filepath.Join(proj, ".lola/plugin-data/example"))))
		Expect(res.Findings).To(ContainElements(
			SatisfyAll(HaveField("Code", "frontmatter-value-dropped"), HaveField("Artifact", "critic"), HaveField("Severity", target.SeverityWarn)),
			SatisfyAll(HaveField("Code", "gemini-cli/arguments"), HaveField("Artifact", "ship"), HaveField("Severity", target.SeverityInfo))))
		Expect(status()).To(Equal([]string{"example/gemini-cli=ok"}))

		_, err = e.Uninstall(ctx, sc, []string{"example"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".gemini/settings.json")).To(Equal(settings))
		Expect(filepath.Join(proj, ".gemini/skills/review")).NotTo(BeAnExistingFile())
	})

	It("installs into OpenCode's opencode.jsonc beside an opencode.json, and leaves both byte for byte on uninstall", func() {
		jsonc := "{\n  // mine\n  \"theme\": \"tokyonight\",\n  \"mcp\": {\n    \"own\": {\"type\": \"remote\", \"url\": \"https://own\"}, // keep\n  },\n}\n"
		plain := "{\"model\": \"anthropic/claude-sonnet-5-5\"}\n"
		writeTree(proj, map[string]string{"opencode.jsonc": jsonc, "opencode.json": plain})
		writeTree(src, map[string]string{"mcps.json": `{"mcpServers":{"lola-gh":{"command":"gh","env":{"LEVEL":"${LOG_LEVEL:-info}"}}}}`})
		res, err := install(engine.Options{Targets: []string{"opencode"}}, src)
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".opencode/skills/review/SKILL.md")).To(Equal("---\nname: review\ndescription: Review code\n---\nReview it.\n"))
		Expect(read(".opencode/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"))
		Expect(read(".opencode/agents/critic.md")).To(Equal("---\nmode: subagent\ndescription: critic\n---\nCriticize.\n"))
		Expect(read("opencode.jsonc")).To(SatisfyAll(ContainSubstring("// mine"), ContainSubstring("// keep"),
			ContainSubstring(`"lola-gh": {`), ContainSubstring(`"LEVEL": "{env:LOG_LEVEL}"`)))
		Expect(read("opencode.json")).To(Equal(plain))
		Expect(res.Findings).To(ContainElement(SatisfyAll(HaveField("Code", "opencode/env-default-dropped"), HaveField("Artifact", "lola-gh"),
			HaveField("Severity", target.SeverityWarn), HaveField("Module", "example"))))
		Expect(status()).To(Equal([]string{"example/opencode=ok"}))

		_, err = e.Uninstall(ctx, sc, []string{"example"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(read("opencode.jsonc")).To(Equal(jsonc))
		Expect(read("opencode.json")).To(Equal(plain))
	})

	It("moves lola's opencode entries into an opencode.jsonc created later, keeping its comments", func() {
		_, err := install(engine.Options{Targets: []string{"opencode"}}, src)
		Expect(err).NotTo(HaveOccurred())
		Expect(read("opencode.json")).To(ContainSubstring(`"lola-gh"`))
		writeTree(proj, map[string]string{"opencode.jsonc": "{\n  // mine\n}\n"})
		_, err = e.Render(ctx, sc, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(proj, "opencode.json")).NotTo(BeAnExistingFile(), "lola created it and nothing else is left in it")
		Expect(read("opencode.jsonc")).To(SatisfyAll(ContainSubstring("// mine"), ContainSubstring(`"lola-gh"`)))
		Expect(status()).To(Equal([]string{"example/opencode=ok"}))
		_, err = e.Uninstall(ctx, sc, []string{"example"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(read("opencode.jsonc")).To(Equal("{\n  // mine\n}\n"))
	})

	It("installs OpenCode's user-scope artifacts directly under $XDG_CONFIG_HOME/opencode", func() {
		user := engine.Scope{Kind: target.User}
		_, err := e.Install(ctx, user, proj, []engine.InstallRequest{{Arg: src}}, engine.Options{Targets: []string{"opencode"}})
		Expect(err).NotTo(HaveOccurred())
		oc := filepath.Join(p.ConfigHome, "opencode")
		Expect(filepath.Join(oc, "commands/ship.md")).To(BeARegularFile())
		Expect(filepath.Join(oc, "skills/review/SKILL.md")).To(BeARegularFile())
		cfg, err := os.ReadFile(filepath.Join(oc, "opencode.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(cfg)).To(ContainSubstring(`"lola-gh"`))
		Expect(e.Detect(user)).To(ContainElement(engine.Assistant{Target: "opencode", Marked: true}), "its config directory marks it")
	})

	It("delivers rules to gemini-cli and opencode through owned list members, and removes them without orphans", func() {
		all := engine.Options{Targets: []string{"claude-code", "gemini-cli", "opencode"}}
		_, err := install(all, src)
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".gemini/lola/example/GEMINI.md")).To(Equal("Prefer small diffs.\n"))
		Expect(read(".opencode/lola/example/style.md")).To(Equal("Prefer small diffs.\n"))
		Expect(read(".gemini/settings.json")).To(ContainSubstring("\"context\": {\n    \"includeDirectories\": [\n      \".gemini/lola/example\"\n    ]\n  }"))
		Expect(read("opencode.json")).To(ContainSubstring("\"instructions\": [\n    \".opencode/lola/example/*.md\"\n  ]"))
		for _, f := range []string{"GEMINI.md", "AGENTS.md", "CLAUDE.md"} {
			Expect(filepath.Join(proj, f)).NotTo(BeAnExistingFile(), "lola never creates "+f)
		}
		Expect(status()).To(ConsistOf("example/claude-code=ok", "example/gemini-cli=ok", "example/opencode=ok"))

		off := false
		_, err = install(engine.Options{Rules: &off}, "example")
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(proj, ".gemini/lola")).NotTo(BeAnExistingFile(), "emptied rule dirs are removed")
		Expect(filepath.Join(proj, ".opencode/lola")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(proj, ".opencode")).To(BeADirectory(), "the assistant's own directory stays")
		Expect(read(".gemini/settings.json")).NotTo(ContainSubstring("context"), "lola created the array, so it goes with its last member")
		Expect(read("opencode.json")).NotTo(ContainSubstring("instructions"))

		_, err = e.Uninstall(ctx, sc, []string{"example"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(tree(proj)).To(ConsistOf(".lola/.gitignore", ".lola/lock.json", ".lola/project.yml"))
	})

	It("adds rule members beside the user's own list entries, and leaves the files byte for byte on uninstall", func() {
		settings := "{\n  // mine\n  \"context\": {\n    \"includeDirectories\": [\"../shared\"] // team docs\n  }\n}\n"
		jsonc := "{\n  \"instructions\": [\n    \"CONTRIBUTING.md\", // ours\n  ],\n}\n"
		writeTree(proj, map[string]string{".gemini/settings.json": settings, "opencode.jsonc": jsonc})
		_, err := install(engine.Options{Targets: []string{"gemini-cli", "opencode"}}, src)
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".gemini/settings.json")).To(SatisfyAll(ContainSubstring(`"../shared"`), ContainSubstring(`".gemini/lola/example"`), ContainSubstring("// team docs")))
		Expect(read("opencode.jsonc")).To(SatisfyAll(ContainSubstring(`"CONTRIBUTING.md"`), ContainSubstring(`".opencode/lola/example/*.md"`), ContainSubstring("// ours")))
		_, err = e.Uninstall(ctx, sc, []string{"example"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".gemini/settings.json")).To(Equal(settings))
		Expect(read("opencode.jsonc")).To(Equal(jsonc))
	})

	It("lists user-scope rule directories by absolute path", func() {
		user := engine.Scope{Kind: target.User}
		_, err := e.Install(ctx, user, proj, []engine.InstallRequest{{Arg: src}}, engine.Options{Targets: []string{"gemini-cli", "opencode"}})
		Expect(err).NotTo(HaveOccurred())
		settings, err := os.ReadFile(filepath.Join(p.Home, ".gemini/settings.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(settings)).To(ContainSubstring(`"` + filepath.Join(p.Home, ".gemini/lola/example") + `"`))
		Expect(filepath.Join(p.Home, ".gemini/lola/example/GEMINI.md")).To(BeARegularFile())
		oc := filepath.Join(p.ConfigHome, "opencode")
		Expect(filepath.Join(oc, "lola/example/style.md")).To(BeARegularFile())
		cfg, err := os.ReadFile(filepath.Join(oc, "opencode.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(cfg)).To(ContainSubstring(`"` + filepath.Join(oc, "lola/example/*.md") + `"`))
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
		Expect(locks).To(ConsistOf(MatchRegexp(`[0-9a-f]{16}\.lock$`), HaveSuffix("trust.lock")), "one scope lock, beside the trust record's")
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
		_, err = install(engine.Options{}, src)
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
		lolaDir := filepath.Join(proj, ".lola")
		Expect(os.Chmod(lolaDir, 0o555)).To(Succeed())
		DeferCleanup(os.Chmod, lolaDir, os.FileMode(0o755))
		_, err = install(engine.Options{}, src)
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
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: "info", Text: "still in your cache: lola cache rm example"}))
		Expect(res.Notices).To(ContainElement(HaveField("Text", ContainSubstring("plugin data kept at"))))
		m, _ := manifest.Load(filepath.Join(proj, ".lola/project.yml"))
		Expect(m.Modules).To(BeEmpty())
		l, _ := ledger.Load(filepath.Join(proj, ".lola/lock.json"))
		Expect(l.Modules).To(BeEmpty())
		Expect(filepath.Join(proj, ".claude/rules/lola")).NotTo(BeAnExistingFile(), "emptied rule dirs are removed")
	})

	It("render leaves a changed folder source as installed; list never calls a folder stale, and shows modified and missing", func() {
		_, err := install(claude, src)
		Expect(err).NotTo(HaveOccurred())
		writeTree(src, map[string]string{"commands/ship.md": "Ship faster\n"})
		_, err = e.Fetch(ctx, sourceSpec(src))
		Expect(err).NotTo(HaveOccurred())
		rows, err := e.Status(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(ConsistOf(And(HaveField("Status", "ok"), HaveField("Pin", ""), HaveField("UpdateAvailable", false))))
		res, err := e.Render(ctx, sc, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Findings).To(ContainElement(HaveField("Code", engine.CodeContentChanged)))
		Expect(read(".claude/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"), "render never installs content nobody reviewed")
		Expect(status()).To(Equal([]string{"example/claude-code=ok"}))
		_, err = install(engine.Options{}, src) // the finding's remedy: the content is the user's own
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
		writeTree(proj, map[string]string{".claude/commands/ship.md": "my edit"})
		_, err = e.Render(ctx, sc, engine.Options{})
		Expect(err).To(MatchError(ContainSubstring("hand-edited")))
		Expect(read(".claude/commands/ship.md")).To(Equal("my edit"))
		_, err = e.Render(ctx, sc, engine.Options{Force: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".claude/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"))
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
		writeTree(proj, map[string]string{".claude/commands/ship.md": "mine"})
		_, err := install(claude, src)
		Expect(err).To(MatchError(ContainSubstring("exists and was not written by lola")))
		Expect(errs.Hints(err)).To(ContainElement("fetched modules stay in your cache; retry with: lola install --project example"))
		names, _ := e.Store.Names()
		Expect(names).To(Equal([]string{"example"}))

		// From outside the project, the retry names it.
		e.Getwd = func() (string, error) { return tmp, nil }
		_, err = install(claude, src)
		Expect(errs.Hints(err)).To(ContainElement("fetched modules stay in your cache; retry with: lola install -C " + proj + " example"))
	})

	Describe("findings", func() {
		BeforeEach(func() { writeTree(src, map[string]string{"agents/bare.md": "Criticize without a description.\n"}) })

		It("skips what a target cannot render and installs the rest", func() {
			res, err := install(claude, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Findings).To(ContainElement(SatisfyAll(
				HaveField("Severity", target.SeveritySkip), HaveField("Module", "example"), HaveField("Artifact", "bare"))))
			Expect(filepath.Join(proj, ".claude/agents/bare.md")).NotTo(BeAnExistingFile())
			Expect(read(".claude/agents/critic.md")).To(ContainSubstring("Criticize."))
			Expect(status()).To(Equal([]string{"example/claude-code=ok"}))
		})

		It("renders forceable skips with --force, marked as forced", func() {
			res, err := install(engine.Options{Targets: claude.Targets, Force: true}, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(read(".claude/agents/bare.md")).To(ContainSubstring("Criticize without a description."))
			Expect(res.Findings).To(ContainElement(SatisfyAll(HaveField("Severity", target.SeverityWarn), HaveField("Forced", true))))
		})

		It("--strict lists every finding and writes nothing; forced skips do not count", func() {
			_, err := install(engine.Options{Targets: claude.Targets, Strict: true}, src)
			Expect(err).To(MatchError(ContainSubstring("--strict: 1 finding(s); nothing was changed")))
			Expect(err).To(MatchError(ContainSubstring("example: claude-code agent bare: claude-code needs a description")))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("rerun without --strict")))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("--force renders")))
			Expect(filepath.Join(proj, ".claude")).NotTo(BeAnExistingFile())

			_, err = install(engine.Options{Targets: claude.Targets, Strict: true, Force: true}, src)
			Expect(err).NotTo(HaveOccurred())
		})

		It("--strict advises --force only when a refused finding is forceable, and the caller's own rerun advice", func() {
			warn := target.Finding{Severity: target.SeverityWarn, Module: "example", Code: "module-warning", Text: "legacy file"}
			err := engine.StrictError([]target.Finding{warn}, "use the other command", true)
			Expect(errs.Hints(err)).To(Equal([]string{"use the other command"}))

			skip := target.Finding{Severity: target.SeveritySkip, Module: "example", Code: "required-key-missing", Forceable: true}
			err = engine.StrictError([]target.Finding{warn, skip}, "use the other command", true)
			Expect(errs.Hints(err)).To(ConsistOf("use the other command", ContainSubstring("--force renders")))

			err = engine.StrictError([]target.Finding{skip}, "use the other command", false)
			Expect(errs.Hints(err)).To(Equal([]string{"use the other command"}))

			Expect(engine.StrictError([]target.Finding{{Severity: target.SeverityInfo}}, "x", true)).To(Succeed())
		})

		It("lists a target where nothing of a module could be rendered as skipped", func() {
			Expect(os.RemoveAll(src)).To(Succeed())
			writeTree(src, map[string]string{"agents/bare.md": "No description.\n"})
			_, err := install(claude, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(status()).To(Equal([]string{"example/claude-code=skipped"}))
		})
	})

	Describe("Check", func() {
		It("reports every target's findings for a folder without touching any state", func() {
			writeTree(src, map[string]string{"agents/bare.md": "No description.\n", "AGENTS.md": "legacy\n"})
			rep, err := e.Check(ctx, sc, proj, []string{"../src/example"}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(rep.Modules).To(HaveLen(1))
			m := rep.Modules[0]
			Expect(m.Name).To(Equal("example"))
			Expect(m.Targets).To(Equal([]string{"claude-code", "gemini-cli", "opencode"}))
			Expect(m.Findings).To(ContainElement(SatisfyAll(HaveField("Kind", "module"), HaveField("Code", "module-warning"))))
			Expect(m.Findings).To(ContainElement(SatisfyAll(HaveField("Severity", target.SeveritySkip), HaveField("Artifact", "bare"),
				HaveField("Module", "example"))))
			Expect(rep.Findings).To(HaveLen(3))
			Expect(rep.Findings).To(HaveEach(HaveField("Code", "probe/not-found")))
			names, err := e.Store.Names()
			Expect(err).NotTo(HaveOccurred())
			Expect(names).To(BeEmpty(), "nothing was added to the cache")
			Expect(filepath.Join(proj, ".lola")).NotTo(BeAnExistingFile())
			Expect(tree(p.Tmp)).To(BeEmpty(), "the staged copy is removed")
		})

		It("detects a plugin format in a source it checks", func() {
			pkg := filepath.Join(tmp, "src", "tools")
			writeTree(pkg, map[string]string{
				"plugin.json":                 `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"tools","extensions":{"com.x":{}}}`,
				"ai.opencode/commands/oc.md":  "OpenCode.\n",
				"dev.getlola/commands/all.md": "All.\n",
			})
			rep, err := e.Check(ctx, sc, proj, []string{pkg}, []string{"claude-code", "opencode"})
			Expect(err).NotTo(HaveOccurred())
			Expect(rep.Modules[0].Name).To(Equal("tools"))
			Expect(rep.Modules[0].Findings).To(ContainElement(SatisfyAll(HaveField("Code", "module-warning"),
				HaveField("Text", "plugin.json: extensions: lola renders nothing for the namespace com.x; it was ignored"))))
		})

		It("checks a cached module by name, and explains an argument that is neither", func() {
			_, err := e.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src})
			Expect(err).NotTo(HaveOccurred())
			rep, err := e.Check(ctx, sc, proj, []string{"example"}, []string{"claude-code"})
			Expect(err).NotTo(HaveOccurred())
			Expect(rep.Modules[0].Name).To(Equal("example"))

			_, err = e.Check(ctx, sc, proj, []string{"nothing-here"}, nil)
			Expect(err).To(MatchError(`"nothing-here" is neither a source nor a module in your cache`))
			Expect(errs.Hints(err)).To(ContainElement("cached modules: lola cache list"))
			_, err = e.Check(ctx, sc, proj, []string{"../src/example"}, []string{"vim"})
			Expect(err).To(MatchError(`unknown target "vim"`))
		})
	})

	It("requires targets and validates them", func() {
		_, err := install(engine.Options{}, src)
		Expect(err).To(MatchError("no targets selected"))
		_, err = install(engine.Options{Targets: []string{"vim"}}, src)
		Expect(err).To(MatchError(`unknown target "vim"`))
		Expect(errs.Hints(err)).To(ContainElement("valid targets: claude-code, gemini-cli, opencode"))
	})

	It("refuses --ref for a folder source", func() {
		_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: src, Ref: "v1"}}, claude)
		Expect(err).To(MatchError(ContainSubstring("--ref applies to git sources only")))
	})

	It("refuses a folder whose name project.yml could not load again, before anything is fetched or written", func() {
		odd := filepath.Join(tmp, "src", "ab\u200ccd")
		writeTree(odd, exampleModule)
		writeTree(odd, map[string]string{"lola.yml": "name: example\n"})
		_, err := install(claude, odd)
		Expect(err).To(MatchError(ContainSubstring("source holds control or invisible characters")))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("rename a folder")))
		Expect(tree(proj)).To(BeEmpty())
		Expect(tree(p.Store)).To(BeEmpty())
		Expect(tree(p.Staging)).To(BeEmpty())
	})

	It("names user.yml when the user scope could not record a source", func() {
		odd := filepath.Join(tmp, "src", "ab\u200ccd")
		writeTree(odd, exampleModule)
		writeTree(odd, map[string]string{"lola.yml": "name: example\n"})
		_, err := e.Install(ctx, engine.Scope{Kind: target.User}, proj, []engine.InstallRequest{{Arg: odd}}, claude)
		Expect(err).To(MatchError(ContainSubstring("in user.yml or lock.json")))
	})

	It("coaches fixing --path when it is the field project.yml could not load again", func() {
		_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: src, Path: "a\u202eb"}}, claude)
		Expect(err).To(MatchError(ContainSubstring("path holds control or invisible characters")))
		Expect(errs.Hints(err)).To(ConsistOf(ContainSubstring("--path")))
		Expect(tree(proj)).To(BeEmpty())
	})

	It("coaches removing a cached module whose recorded source project.yml could not load again", func() {
		_, err := install(claude, src)
		Expect(err).NotTo(HaveOccurred())
		index := filepath.Join(p.Store, "example", "index.yml")
		b, err := os.ReadFile(index)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(index, []byte(strings.Replace(string(b), "example", "ex\u200cample", 1)), 0o600)).To(Succeed())
		_, err = e.Install(ctx, engine.Scope{Kind: target.User}, proj, []engine.InstallRequest{{Arg: "example"}}, claude)
		Expect(err).To(MatchError(ContainSubstring("source holds control or invisible characters")))
		Expect(errs.Hints(err)).To(ConsistOf("remove it from your cache, then install it again from its source: " +
			errs.ShellJoin([]string{"lola", "cache", "rm", "example"})))
	})

	It("still installs from a folder whose name is plain", func() {
		_, err := install(claude, src)
		Expect(err).NotTo(HaveOccurred())
		man, err := e.Manifest(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(man.Find("example")).NotTo(BeNil())
	})

	It("skips targets a module does not declare, with a notice", func() {
		writeTree(src, map[string]string{"lola.yml": "name: example\ntargets: [opencode]\n"})
		res, err := install(claude, src)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: "info", Text: "example does not support claude-code; skipped there"}))
		Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory())
		Expect(status()).To(BeEmpty())
	})

	It("uninstall of a module that is not installed points at the store", func() {
		_, err := e.Fetch(ctx, sourceSpec(src))
		Expect(err).NotTo(HaveOccurred())
		_, err = e.Uninstall(ctx, sc, []string{"example"}, engine.Options{})
		Expect(err).To(MatchError(`lola uninstall: "example" is not installed in this project`))
		Expect(errs.Hints(err)).To(Equal([]string{"still in your cache: lola cache rm example", "installed here: lola list --project"}))
	})

	It("uninstall coaches lola cache rm for a cached module whose index lola refuses", func() {
		_, err := install(claude, src)
		Expect(err).NotTo(HaveOccurred())
		index := filepath.Join(p.Store, "example", "index.yml")
		b, err := os.ReadFile(index)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(index, append(b, []byte("  - ref: broken\n    digest: nothex\n")...), 0o600)).To(Succeed())
		res, err := e.Uninstall(ctx, sc, []string{"example"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: "info", Text: "still in your cache: lola cache rm example"}))
		_, err = e.Uninstall(ctx, sc, []string{"example"}, engine.Options{})
		Expect(errs.Hints(err)).To(ContainElement("still in your cache: lola cache rm example"))
	})

	It("rejects uninstall with a malicious module name and leaves the project intact", func() {
		testFile := filepath.Join(proj, "important.txt")
		Expect(os.WriteFile(testFile, []byte("keep this"), 0o644)).To(Succeed())
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\nmodules:\n  - name: ../..\n    source: " + src + "\n"})
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

	It("gives each target its scope and that scope's root, to detect, install and check", func() {
		var seen []string
		e.Targets = []target.Target{spy{seen: &seen}}
		user := engine.Scope{Kind: target.User}
		e.Detect(sc)
		e.Detect(user)
		_, err := install(claude, src)
		Expect(err).NotTo(HaveOccurred())
		_, err = e.Check(ctx, user, proj, []string{src}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(seen).To(Equal([]string{"detect project " + proj, "detect user " + p.Home, "plan project " + proj, "plan user " + p.Home}))
	})

	It("FindProject walks up to the nearest .lola/ and falls back to the start dir", func() {
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(proj, "a/b"), 0o755)).To(Succeed())
		Expect(engine.FindProject(filepath.Join(proj, "a/b"))).To(Equal(proj))
		Expect(engine.FindProject(tmp)).To(Equal(tmp))
	})

	Describe("assistants", func() {
		// assistant puts an executable on the spec's PATH that runs script.
		assistant := func(name, script string) string {
			path := filepath.Join(tmp, "bin", name)
			Expect(os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755)).To(Succeed())
			return path
		}
		probeFindings := func(res *engine.Result) []target.Finding {
			var out []target.Finding
			for _, f := range res.Findings {
				if f.Kind == "assistant" {
					out = append(out, f)
				}
			}
			return out
		}

		It("Detect reports installed binaries and project markers without running anything", func() {
			Expect(e.Detect(sc)).To(Equal([]engine.Assistant{{Target: "claude-code"}, {Target: "gemini-cli"}, {Target: "opencode"}}))
			bin := assistant("claude", "touch \""+filepath.Join(tmp, "ran")+"\"\n")
			Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
			Expect(e.Detect(sc)).To(Equal([]engine.Assistant{{Target: "claude-code", Binary: bin, Marked: true}, {Target: "gemini-cli"}, {Target: "opencode"}}))
			Expect(filepath.Join(tmp, "ran")).NotTo(BeAnExistingFile())
		})

		It("ignores a binary found only through a relative PATH entry", func() {
			Expect(os.MkdirAll(filepath.Join(proj, "bin"), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(proj, "bin", "claude"), []byte("#!/bin/sh\n"), 0o755)).To(Succeed())
			GinkgoT().Chdir(proj)
			GinkgoT().Setenv("PATH", "bin:"+os.Getenv("PATH"))
			p, err := paths.Resolve(os.Getenv)
			Expect(err).NotTo(HaveOccurred())
			Expect(engine.New(p).Detect(sc)[0].Binary).To(BeEmpty())
		})

		It("still renders an explicitly chosen target whose assistant is missing, and says so", func() {
			res, err := install(claude, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(read(".claude/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"))
			Expect(probeFindings(res)).To(Equal([]target.Finding{{Severity: target.SeverityInfo, Target: "claude-code", Kind: "assistant",
				Code: "probe/not-found", Text: "claude is not on PATH; checked against the newest version lola knows"}}))
		})

		It("reads the installed version once per binary build and caches it", func() {
			count := filepath.Join(tmp, "count")
			assistant("claude", "echo x >> \""+count+"\"\necho '2.1.287 (Claude Code)'\n")
			for range 2 {
				res, err := install(engine.Options{Targets: claude.Targets, DryRun: true}, src)
				Expect(err).NotTo(HaveOccurred())
				Expect(probeFindings(res)).To(BeEmpty())
			}
			Expect(os.ReadFile(count)).To(Equal([]byte("x\n")), "the second run used the cache")
			b, err := os.ReadFile(p.Probes)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(ContainSubstring(`"version": "2.1.287"`))
		})

		It("keeps control characters out of the version it reads", func() {
			assistant("claude", "printf '2.1.287\\033[31m (Claude Code)\\n'\n")
			_, err := install(engine.Options{Targets: claude.Targets, DryRun: true}, src)
			Expect(err).NotTo(HaveOccurred())
			b, err := os.ReadFile(p.Probes)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(ContainSubstring(`"version": "2.1.287"`))
		})

		It("reads at most a few KiB of an assistant's version output", func() {
			e.Targets = []target.Target{loose{}}
			noise := filepath.Join(tmp, "noise")
			Expect(os.WriteFile(noise, []byte(strings.Repeat("x", 64<<10)), 0o644)).To(Succeed())
			assistant("claude", "cat \""+noise+"\"\necho 9.9.9\n")
			res, err := install(engine.Options{Targets: claude.Targets, DryRun: true}, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(probeFindings(res)).To(ConsistOf(HaveField("Code", "probe/unknown-version")), "the version came after the limit")
		})

		It("reports a version it cannot read, and stops a probe that hangs", func() {
			assistant("claude", "echo 'no version here'\n")
			res, err := install(engine.Options{Targets: claude.Targets, DryRun: true}, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(probeFindings(res)).To(ConsistOf(HaveField("Code", "probe/unknown-version")))

			assistant("claude", "exec sleep 5\n")
			e.ProbeTimeout = 100 * time.Millisecond
			start := time.Now()
			res, err = install(engine.Options{Targets: claude.Targets, DryRun: true}, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(time.Since(start)).To(BeNumerically("<", 3*time.Second))
			Expect(probeFindings(res)).To(ConsistOf(HaveField("Code", "probe/unknown-version")))
		})

		It("stops a probe whose children keep its output open", func() {
			assistant("claude", "sleep 8 &\nexec sleep 8\n")
			e.ProbeTimeout = 200 * time.Millisecond
			start := time.Now()
			res, err := install(engine.Options{Targets: claude.Targets, DryRun: true}, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(time.Since(start)).To(BeNumerically("<", 3*time.Second))
			Expect(probeFindings(res)).To(ConsistOf(HaveField("Code", "probe/unknown-version")))
		})
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

	It("coaches fetching a module gone from the cache and its source with the recorded source quoted for the shell", func() {
		hostile := filepath.Join(tmp, "it's src", "example")
		writeTree(hostile, exampleModule)
		_, err := install(claude, "../it's src/example")
		Expect(err).NotTo(HaveOccurred())
		ok, err := e.Store.Remove("example")
		Expect(ok).To(BeTrue())
		Expect(err).NotTo(HaveOccurred())
		Expect(os.RemoveAll(hostile)).To(Succeed())
		_, err = e.Render(ctx, sc, engine.Options{})
		Expect(err).To(MatchError("example is no longer in your cache"))
		Expect(errs.Hints(err)).To(ConsistOf("fetch it again: "+errs.ShellJoin([]string{"lola", "install", "../it's src/example"}),
			ContainSubstring("lola uninstall example")))
		Expect(errs.Hints(err)).NotTo(ContainElement(ContainSubstring("install ../it's src")))
	})

	Describe("coaching a module gone from the cache whose recorded source is not a source", func() {
		record := func(source, ref, path string) {
			_, err := install(claude, src)
			Expect(err).NotTo(HaveOccurred())
			lock := filepath.Join(proj, ".lola", "lock.json")
			l, err := ledger.Load(lock)
			Expect(err).NotTo(HaveOccurred())
			l.Modules["example"].Source, l.Modules["example"].Ref, l.Modules["example"].Path = source, ref, path
			Expect(l.Save(lock)).To(Succeed())
			ok, err := e.Store.Remove("example")
			Expect(ok).To(BeTrue())
			Expect(err).NotTo(HaveOccurred())
		}

		It("refuses a recorded source holding control bytes without printing them", func() {
			record("gone\x1b[31mRED\x1b]0;pwned\x07", "", "")
			_, err := e.Render(ctx, sc, engine.Options{})
			Expect(err).To(MatchError(ContainSubstring("module example: source holds control or invisible characters")))
			Expect(err.Error()).NotTo(ContainSubstring("\x1b"))
			Expect(errs.Hints(err)).NotTo(ContainElement(ContainSubstring("\x1b")))
		})

		It("names the recorded ref and path, so the command fetches the same module", func() {
			// At user scope, whose user.yml is the user's own and says the same as lock.json, render
			// acts on lock.json's record.
			user := engine.Scope{Kind: target.User}
			_, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: src}}, claude)
			Expect(err).NotTo(HaveOccurred())
			l, err := ledger.Load(p.UserLedger)
			Expect(err).NotTo(HaveOccurred())
			l.Modules["example"].Source, l.Modules["example"].Ref, l.Modules["example"].Path = "gone", "v1", "plugins/it's"
			Expect(l.Save(p.UserLedger)).To(Succeed())
			m, err := e.Manifest(user)
			Expect(err).NotTo(HaveOccurred())
			ent := *m.Find("example")
			ent.Source, ent.Ref, ent.Path = "gone", "v1", "plugins/it's"
			dst, data, err := manifest.Edit(p.UserManifest, manifest.Change{Set: []manifest.Entry{ent}})
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(dst, data, 0o600)).To(Succeed())
			ok, err := e.Store.Remove("example")
			Expect(ok).To(BeTrue())
			Expect(err).NotTo(HaveOccurred())
			_, err = e.Render(ctx, user, engine.Options{})
			Expect(err).To(MatchError("example is no longer in your cache"))
			Expect(errs.Hints(err)).To(ConsistOf("fetch it again: "+
				errs.ShellJoin([]string{"lola", "install", "gone", "--ref", "v1", "--path", "plugins/it's"}),
				ContainSubstring("lola uninstall example")))
		})
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

	Describe("a tracked lock.json", func() {
		BeforeEach(func() {
			_, err := install(claude, src)
			Expect(err).NotTo(HaveOccurred())
			gitIn(proj, "init", "-q")
		})
		ledgerBytes := func() string { return read(".lola/lock.json") }

		It("a tracked lock.json is refused", func() {
			gitIn(proj, "add", "-f", ".lola/lock.json")
			before := ledgerBytes()
			Expect(os.Remove(filepath.Join(proj, ".claude/commands/ship.md"))).To(Succeed())
			_, err := e.Render(ctx, sc, engine.Options{})
			Expect(err).To(MatchError(".lola/lock.json is tracked by git; lola keeps it private to your checkout, since a committed lock.json lets a pull decide what lola deletes"))
			Expect(errs.Hints(err)).To(Equal([]string{"git rm --cached .lola/lock.json",
				"if you never installed with lola in this checkout, delete it too, then run lola sync --project"}))
			Expect(filepath.Join(proj, ".claude/commands/ship.md")).NotTo(BeAnExistingFile(), "nothing rendered")
			Expect(ledgerBytes()).To(Equal(before))
			for _, dry := range []bool{false, true} {
				_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{DryRun: dry}})
				Expect(err).To(MatchError(".lola/lock.json is tracked by git; lola keeps it private to your checkout, since a committed lock.json lets a pull decide what lola deletes"), "sync, dry run: %v", dry)
				Expect(filepath.Join(proj, ".claude/commands/ship.md")).NotTo(BeAnExistingFile(), "nothing synced")
				Expect(ledgerBytes()).To(Equal(before))
			}

			gitIn(proj, "rm", "-q", "--cached", ".lola/lock.json")
			_, err = e.Render(ctx, sc, engine.Options{})
			Expect(err).NotTo(HaveOccurred(), "an untracked ledger in a work tree renders")
			Expect(filepath.Join(proj, ".claude/commands/ship.md")).To(BeARegularFile())
		})

		It("refuses install, the install pickers and uninstall too, before anything is resolved, fetched or written", func() {
			gitIn(proj, "add", "-f", ".lola/lock.json")
			// A pull changed the entry, so an install by source would take its targets from the ledger.
			off := false
			m, err := e.Manifest(sc)
			Expect(err).NotTo(HaveOccurred())
			pulled := *m.Find("example")
			pulled.Rules = &off
			dst, data, err := manifest.Edit(filepath.Join(proj, ".lola", "project.yml"), manifest.Change{Set: []manifest.Entry{pulled}})
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(dst, data, 0o644)).To(Succeed())
			other := filepath.Join(tmp, "src", "other")
			writeTree(other, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "Other\n"})
			ledger, man := ledgerBytes(), read(".lola/project.yml")
			refused := func(err error) {
				GinkgoHelper()
				Expect(err).To(MatchError(".lola/lock.json is tracked by git; lola keeps it private to your checkout, since a committed lock.json lets a pull decide what lola deletes"))
				Expect(errs.Hints(err)).To(Equal([]string{"git rm --cached .lola/lock.json",
					"if you never installed with lola in this checkout, delete it too, then run lola sync --project"}))
				Expect(ledgerBytes()).To(Equal(ledger))
				Expect(read(".lola/project.yml")).To(Equal(man))
				Expect(filepath.Join(proj, ".claude/commands/ship.md")).To(BeARegularFile())
				cached, err := e.Store.Has("other")
				Expect(err).NotTo(HaveOccurred())
				Expect(cached).To(BeFalse(), "nothing fetched")
			}

			_, err = install(engine.Options{}, src, other)
			refused(err)
			_, err = e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: []engine.ScopeChoice{{Scope: sc, Source: engine.SourceFlag}}})
			refused(err)
			_, err = e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: []engine.ScopeChoice{{Scope: sc, Source: engine.SourceFlag}},
				Modules: []engine.InstallRequest{{Arg: other}}})
			refused(err)
			_, err = e.Uninstall(ctx, sc, []string{"example"}, engine.Options{})
			refused(err)

			gitIn(proj, "rm", "-q", "--cached", ".lola/lock.json")
			_, err = install(engine.Options{}, src, other)
			Expect(err).NotTo(HaveOccurred(), "an untracked ledger in a work tree installs")
		})

		It("renders a lock.json git ignores", func() {
			Expect(os.WriteFile(filepath.Join(proj, ".gitignore"), []byte(".lola/lock.json\n"), 0o644)).To(Succeed())
			gitIn(proj, "add", ".gitignore")
			Expect(os.Remove(filepath.Join(proj, ".claude/commands/ship.md"))).To(Succeed())
			_, err := e.Render(ctx, sc, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Join(proj, ".claude/commands/ship.md")).To(BeARegularFile())
		})

		It("fails closed when git cannot tell, and only warns when git is not installed", func() {
			Expect(os.WriteFile(filepath.Join(proj, ".git", "HEAD"), []byte("garbage\n"), 0o644)).To(Succeed())
			_, err := e.Render(ctx, sc, engine.Options{})
			Expect(err).To(MatchError(ContainSubstring("lola could not check whether git tracks .lola/lock.json")))
			Expect(errs.Hints(err)).To(Equal([]string{"lola asks git whether it tracks .lola/lock.json: make git status work in this repository, then run lola again"}))

			GinkgoT().Setenv("PATH", filepath.Join(tmp, "bin"))
			res, err := e.Render(ctx, sc, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
				Text: "git is not on PATH, so lola could not check that git does not track .lola/lock.json"}))
		})
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

		It("refuses a .lola/plugin-data that links outside the project", func() {
			link := plant(".lola/plugin-data")
			_, err := install(claude, src)
			expectRefused(err, link)
			Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory())
		})

		It("replaces a module copy that is not a real directory, and list reports it missing until then", func() {
			_, err := install(claude, src)
			Expect(err).NotTo(HaveOccurred())
			link := plant(".lola/modules/example")
			Expect(status()).To(Equal([]string{"example/claude-code=missing"}))
			_, err = e.Render(ctx, sc, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			fi, err := os.Lstat(link)
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.IsDir()).To(BeTrue(), "the copy is a real directory again")
			Expect(filepath.Join(link, "commands/ship.md")).To(BeARegularFile())
			Expect(tree(outside)).To(ConsistOf("example/keep.txt"), "nothing was written or removed through the link")
			Expect(status()).To(Equal([]string{"example/claude-code=ok"}))
		})

		It("replaces a module copy that is a regular file", func() {
			_, err := install(claude, src)
			Expect(err).NotTo(HaveOccurred())
			copyDir := filepath.Join(proj, ".lola/modules/example")
			Expect(os.RemoveAll(copyDir)).To(Succeed())
			Expect(os.WriteFile(copyDir, []byte("not a copy"), 0o644)).To(Succeed())
			Expect(status()).To(Equal([]string{"example/claude-code=missing"}))
			_, err = e.Render(ctx, sc, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Join(copyDir, "commands/ship.md")).To(BeARegularFile())
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

		It("refuses a project.yml that is a symbolic link at project scope, writing nothing", func() {
			real := filepath.Join(outside, "project.yml")
			Expect(os.WriteFile(real, []byte("version: 1 # theirs\n"), 0o600)).To(Succeed())
			link := filepath.Join(proj, ".lola", "project.yml")
			Expect(os.MkdirAll(filepath.Dir(link), 0o755)).To(Succeed())
			Expect(os.Symlink(real, link)).To(Succeed())
			_, err := install(claude, src)
			Expect(err).To(MatchError(link + " is a symlink, and lola keeps the project manifest in a regular file"))
			Expect(errs.Hints(err)).To(ContainElement("replace it with a regular file holding the same content, then rerun"))
			Expect(os.ReadFile(real)).To(Equal([]byte("version: 1 # theirs\n")))
			Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory())
			Expect(filepath.Join(proj, ".lola", "lock.json")).NotTo(BeAnExistingFile())
			_, err = e.Uninstall(ctx, sc, []string{"example"}, engine.Options{})
			Expect(err).To(MatchError(ContainSubstring("is a symlink")))
			Expect(os.ReadFile(real)).To(Equal([]byte("version: 1 # theirs\n")))
		})

		It("still edits a symlinked user-scope manifest at its target", func() {
			real := filepath.Join(outside, "user.yml")
			Expect(os.WriteFile(real, []byte("version: 1 # dotfiles\n"), 0o600)).To(Succeed())
			Expect(os.MkdirAll(filepath.Dir(p.UserManifest), 0o700)).To(Succeed())
			Expect(os.Symlink(real, p.UserManifest)).To(Succeed())
			_, err := e.Install(ctx, engine.Scope{Kind: target.User}, proj, []engine.InstallRequest{{Arg: src}}, claude)
			Expect(err).NotTo(HaveOccurred())
			b, err := os.ReadFile(real)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(And(ContainSubstring("# dotfiles"), ContainSubstring("name: example")))
			fi, err := os.Lstat(p.UserManifest)
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode() & os.ModeSymlink).NotTo(BeZero())
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
			Expect(res.Notices).To(ContainElement(HaveField("Text", "example: post-install hook skipped without your consent; "+
				"review and run it: lola sync --project example, or consent without asking: lola sync --project example --allow-hooks")))
			Expect(hookLog()).To(BeNil())
		})

		pending := func() bool {
			GinkgoHelper()
			l, err := e.Ledger(sc)
			Expect(err).NotTo(HaveOccurred())
			return l.Modules["example"].HooksPending
		}

		It("a declined post-install hook runs on lola sync --allow-hooks", func() {
			hooks("{post-install: hooks/post.sh}", map[string]string{})
			_, err := install(engine.Options{Targets: claude.Targets}, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(pending()).To(BeTrue(), "the declined hook is pending in lock.json")
			Expect(read(".lola/lock.json")).To(ContainSubstring(`"hooks_pending": true`))

			// Off a terminal without consent it stays pending, the copy as it is, and says how to run it.
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"example"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.InSync).To(BeTrue())
			Expect(res.Notices).To(ContainElement(HaveField("Text", "example: its post-install hook has not run (you declined it); "+
				"review and run it: lola sync --project example, or consent without asking: lola sync --project example --allow-hooks")))
			Expect(pending()).To(BeTrue())
			Expect(hookLog()).To(BeNil())

			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"example"}, Options: engine.Options{AllowHooks: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(hookLog()).To(HaveLen(1), "it ran once")
			Expect(pending()).To(BeFalse(), "and the mark is cleared")

			res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{AllowHooks: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.InSync).To(BeTrue())
			Expect(hookLog()).To(HaveLen(1), "not again")
		})

		It("a pending hook runs only the consented bytes", func() {
			hooks("{post-install: hooks/post.sh}", map[string]string{})
			_, err := install(engine.Options{Targets: claude.Targets}, src)
			Expect(err).NotTo(HaveOccurred())
			// The module copy is edited: its hook would run something no consent covered.
			evil := filepath.Join(tmp, "evil")
			writeTree(copyDir, map[string]string{"hooks/post.sh": "#!/bin/sh\ntouch " + evil + "\n"})

			var shown []engine.HookScript
			opts := engine.Options{Ask: func(m string, scripts []engine.HookScript) (bool, error) {
				asked, shown = append(asked, m), scripts
				return true, nil
			}}
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"example"}, Options: opts})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(Equal([]string{"example"}), "on a terminal, lola sync <m> asks the hook consent")
			Expect(shown).To(ConsistOf(And(HaveField("Event", engine.PostInstall), HaveField("Script", "hooks/post.sh"))))
			Expect(evil).NotTo(BeAnExistingFile(), "the copy was replaced from the hashed version before the hook ran")
			Expect(hookLog()).To(HaveLen(1))
			Expect(read(".lola/modules/example/hooks/post.sh")).To(Equal(script))

			// A planted hooks_pending, with no consent recorded for those bytes, runs nothing.
			Expect(os.Remove(p.Consent)).To(Succeed())
			lp := filepath.Join(proj, ".lola", "lock.json")
			l, err := ledger.Load(lp)
			Expect(err).NotTo(HaveOccurred())
			l.Modules["example"].HooksPending = true
			Expect(l.Save(lp)).To(Succeed())
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"example"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(hookLog()).To(HaveLen(1), "nothing ran")
			Expect(res.Notices).To(ContainElement(HaveField("Text", HavePrefix("example: its post-install hook has not run (you declined it)"))))
		})

		It("offers a pending hook only to a sync that names its module or gives --allow-hooks", func() {
			hooks("{post-install: hooks/post.sh}", map[string]string{})
			_, err := install(engine.Options{Targets: claude.Targets}, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(pending()).To(BeTrue())

			// A sync that does not name it, on a terminal or off one, asks nothing, and says how.
			for _, opts := range []engine.Options{{Ask: ask(true)}, {}} {
				res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: opts})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.InSync).To(BeTrue())
				want := "pending post-install hook for example → lola sync --project example"
				if opts.Ask == nil {
					want += " (off a terminal: lola sync --project example --allow-hooks)"
				}
				Expect(res.Notices).To(ContainElement(HaveField("Text", want)))
			}
			Expect(asked).To(BeEmpty())
			Expect(pending()).To(BeTrue())

			// A dry run shows it: the notice, and named, the hook it would run.
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(ContainElement(HaveField("Text", HavePrefix("pending post-install hook for example"))))
			res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"example"}, Options: engine.Options{DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.InSync).To(BeFalse())
			Expect(res.Modules[0].Hooks).To(ConsistOf(HaveField("Status", engine.HookWouldRun)))

			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"example"}, Options: engine.Options{Ask: ask(true)}})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(Equal([]string{"example"}), "named, it is asked")
			Expect(hookLog()).To(HaveLen(1))
			Expect(pending()).To(BeFalse())
		})

		It("install --allow-hooks over a pending hook replaces an edited copy before the hook runs", func() {
			hooks("{post-install: hooks/post.sh}", map[string]string{})
			_, err := install(engine.Options{Targets: claude.Targets}, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(pending()).To(BeTrue())
			evil := filepath.Join(tmp, "evil")
			writeTree(copyDir, map[string]string{"hooks/post.sh": "#!/bin/sh\ntouch " + evil + "\n"})

			_, err = install(engine.Options{AllowHooks: true}, "example")
			Expect(err).NotTo(HaveOccurred())
			Expect(evil).NotTo(BeAnExistingFile(), "the kept copy never runs")
			Expect(hookLog()).To(HaveLen(1), "the cache's script ran")
			Expect(read(".lola/modules/example/hooks/post.sh")).To(Equal(script))
			Expect(pending()).To(BeFalse())
		})

		It("an install off a terminal without --allow-hooks keeps a copy whose hooks ran", func() {
			for _, yaml := range []string{"{post-install: hooks/post.sh}", "{pre-install: hooks/pre.sh, post-install: hooks/post.sh}"} {
				hooks(yaml, map[string]string{})
				_, err := install(engine.Options{Targets: claude.Targets, AllowHooks: true}, src)
				Expect(err).NotTo(HaveOccurred(), yaml)
				ran := len(hookLog())
				writeTree(copyDir, map[string]string{"built.txt": "built by the hook\n"})

				res, err := install(engine.Options{}, "example")
				Expect(err).NotTo(HaveOccurred(), "%s: no hook runs, so none needs consent", yaml)
				Expect(read(".lola/modules/example/built.txt")).To(Equal("built by the hook\n"), "%s: the copy is kept", yaml)
				Expect(hookLog()).To(HaveLen(ran), yaml)
				Expect(pending()).To(BeFalse(), "%s: the hook ran in this copy", yaml)
				Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("post-install hook"))), yaml)
			}
		})

		It("a same-version install keeps the copy of a module on a target that rendered none of its files", func() {
			Expect(os.RemoveAll(src)).To(Succeed())
			hooks("{post-install: hooks/post.sh}", map[string]string{"commands/ship.md": "Ship !`ls`\n", "hooks/post.sh": script})
			_, err := install(engine.Options{Targets: []string{"claude-code", "gemini-cli"}, AllowHooks: true}, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(hookLog()).To(HaveLen(2))
			l, err := e.Ledger(sc)
			Expect(err).NotTo(HaveOccurred())
			Expect(l.Modules["example"].Targets).To(Equal([]string{"claude-code", "gemini-cli"}), "lock.json records both, files or not")
			Expect(l.Modules["example"].Artifacts).To(HaveEach(HaveField("Target", "claude-code")), "gemini-cli rendered nothing")
			writeTree(copyDir, map[string]string{"built.txt": "built by the hook\n"})

			for range 2 {
				res, err := install(engine.Options{}, "example")
				Expect(err).NotTo(HaveOccurred())
				Expect(read(".lola/modules/example/built.txt")).To(Equal("built by the hook\n"), "the copy is kept")
				Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("post-install hook"))))
				Expect(hookLog()).To(HaveLen(2))
				Expect(pending()).To(BeFalse())
			}
		})

		// hookStatuses is each hook run a result records, as target=status.
		hookStatuses := func(mods []engine.ModuleResult) []string {
			var out []string
			for _, m := range mods {
				for _, h := range m.Hooks {
					out = append(out, h.Target+"="+h.Status)
				}
			}
			return out
		}
		// addTarget makes the manifest at path ask for example on claude-code and opencode.
		addTarget := func(path string) {
			GinkgoHelper()
			m, err := manifest.Load(path)
			Expect(err).NotTo(HaveOccurred())
			ent := *m.Find("example")
			ent.Targets = []string{"claude-code", "opencode"}
			dst, data, err := manifest.Edit(path, manifest.Change{Set: []manifest.Entry{ent}})
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(dst, data, 0o644)).To(Succeed())
		}

		It("a same-version sync that adds a target declines its post-install hook off a terminal, and leaves it pending", func() {
			src = filepath.Join(proj, "mods", "example") // a sync trusts a folder inside the project
			writeTree(src, exampleModule)
			hooks("{post-install: hooks/post.sh}", map[string]string{"hooks/post.sh": script})
			_, err := install(engine.Options{Targets: claude.Targets, AllowHooks: true}, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(hookLog()).To(HaveLen(1))
			addTarget(filepath.Join(proj, ".lola", "project.yml"))
			trustAll := func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }
			evil := filepath.Join(tmp, "evil")
			writeTree(copyDir, map[string]string{"hooks/post.sh": "#!/bin/sh\ntouch " + evil + "\n"})

			res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll, Options: engine.Options{DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(hookStatuses(res.Modules)).To(ConsistOf("claude-code="+engine.HookWouldRun, "opencode="+engine.HookWouldRun))

			res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Trust: trustAll})
			Expect(err).NotTo(HaveOccurred())
			Expect(hookStatuses(res.Modules)).To(ConsistOf("claude-code="+engine.HookDeclined, "opencode="+engine.HookDeclined))
			Expect(res.Notices).To(ContainElement(HaveField("Text", HavePrefix("example: post-install hook skipped without your consent"))))
			Expect(hookLog()).To(HaveLen(1), "nothing ran")
			Expect(pending()).To(BeTrue())
			Expect(read(".lola/modules/example/hooks/post.sh")).To(Equal(script), "the copy was replaced from the cache")

			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"example"}, Options: engine.Options{AllowHooks: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(hookLog()).To(HaveLen(3), "the pending hook ran on both targets")
			Expect(evil).NotTo(BeAnExistingFile())
			Expect(pending()).To(BeFalse())
		})

		It("a same-version render that adds a target declines its post-install hook off a terminal, and leaves it pending", func() {
			hooks("{post-install: hooks/post.sh}", map[string]string{})
			user := engine.Scope{Kind: target.User}
			_, err := e.Install(ctx, user, proj, []engine.InstallRequest{{Arg: src}}, engine.Options{Targets: claude.Targets, AllowHooks: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(hookLog()).To(HaveLen(1))
			addTarget(p.UserManifest)
			userCopy := filepath.Join(p.DataHome, "lola", "user-modules", "example")
			writeTree(userCopy, map[string]string{"built.txt": "built by the hook\n"})

			res, err := e.Render(ctx, user, engine.Options{DryRun: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(hookStatuses(res.Modules)).To(ConsistOf("claude-code="+engine.HookWouldRun, "opencode="+engine.HookWouldRun))

			res, err = e.Render(ctx, user, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(hookStatuses(res.Modules)).To(ConsistOf("claude-code="+engine.HookDeclined, "opencode="+engine.HookDeclined))
			Expect(res.Notices).To(ContainElement(HaveField("Text", HavePrefix("example: post-install hook skipped without your consent"))))
			Expect(hookLog()).To(HaveLen(1), "nothing ran")
			Expect(filepath.Join(userCopy, "built.txt")).NotTo(BeAnExistingFile(), "the copy was replaced from the cache")
			l, err := e.Ledger(user)
			Expect(err).NotTo(HaveOccurred())
			Expect(l.Modules["example"].HooksPending).To(BeTrue())
			Expect(l.Modules["example"].Targets).To(Equal([]string{"claude-code", "opencode"}))

			res, err = e.Render(ctx, user, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Modules[0].Hooks).To(BeEmpty(), "a render that adds no target runs no hook")
			Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("post-install hook"))))
		})

		It("a same-version install that adds a target declines its post-install hook off a terminal, and leaves it pending", func() {
			hooks("{post-install: hooks/post.sh}", map[string]string{})
			_, err := install(engine.Options{Targets: claude.Targets, AllowHooks: true}, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(hookLog()).To(HaveLen(1))
			both := engine.Options{Targets: []string{"claude-code", "opencode"}}
			ranOn := func(res *engine.Result) []string {
				var out []string
				for _, h := range res.Modules[0].Hooks {
					out = append(out, h.Target+"="+h.Status)
				}
				return out
			}

			dry := both
			dry.DryRun = true
			res, err := install(dry, "example")
			Expect(err).NotTo(HaveOccurred())
			Expect(ranOn(res)).To(ConsistOf("claude-code="+engine.HookWouldRun, "opencode="+engine.HookWouldRun))

			res, err = install(both, "example")
			Expect(err).NotTo(HaveOccurred())
			Expect(ranOn(res)).To(ConsistOf("claude-code="+engine.HookDeclined, "opencode="+engine.HookDeclined), "the run the dry run planned")
			Expect(res.Notices).To(ContainElement(HaveField("Text", "example: post-install hook skipped without your consent; "+
				"review and run it: lola sync --project example, or consent without asking: lola sync --project example --allow-hooks")))
			Expect(hookLog()).To(HaveLen(1), "nothing ran")
			Expect(pending()).To(BeTrue(), "lola sync offers it")
		})

		It("a planted pending mark replaces an edited copy and runs the cache's bytes once consent passes", func() {
			_, err := install(engine.Options{Targets: claude.Targets, Ask: ask(true)}, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(hookLog()).To(HaveLen(2))
			evil := filepath.Join(tmp, "evil")
			writeTree(copyDir, map[string]string{"hooks/post.sh": "#!/bin/sh\ntouch " + evil + "\n"})
			lp := filepath.Join(proj, ".lola", "lock.json")
			l, err := ledger.Load(lp)
			Expect(err).NotTo(HaveOccurred())
			l.Modules["example"].HooksPending = true
			Expect(l.Save(lp)).To(Succeed())

			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"example"}, Options: engine.Options{AllowHooks: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(evil).NotTo(BeAnExistingFile())
			Expect(hookLog()).To(HaveLen(4), "pre- and post-install ran again, from the cache's bytes")
			Expect(read(".lola/modules/example/hooks/post.sh")).To(Equal(script))
			Expect(pending()).To(BeFalse())
		})

		It("a pending hook declined again leaves the edited copy as it is and stays pending", func() {
			hooks("{post-install: hooks/post.sh}", map[string]string{})
			_, err := install(engine.Options{Targets: claude.Targets}, src)
			Expect(err).NotTo(HaveOccurred())
			edited := "#!/bin/sh\n# edited\n"
			writeTree(copyDir, map[string]string{"hooks/post.sh": edited})

			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"example"}, Options: engine.Options{Ask: ask(false)}})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(Equal([]string{"example"}))
			Expect(read(".lola/modules/example/hooks/post.sh")).To(Equal(edited))
			Expect(hookLog()).To(HaveLen(0))
			Expect(pending()).To(BeTrue())
		})

		It("a pending hook of a version never seen is not called one the user declined", func() {
			hooks("{post-install: hooks/post.sh}", map[string]string{})
			_, err := install(engine.Options{Targets: claude.Targets}, src)
			Expect(err).NotTo(HaveOccurred())
			writeTree(src, map[string]string{"hooks/post.sh": script + "# v2\n"})
			Expect(os.Chmod(filepath.Join(src, "hooks/post.sh"), 0o755)).To(Succeed())

			res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"example"}, Update: true,
				Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(ContainElement(HaveField("Text", HavePrefix("example: post-install hook skipped without your consent"))))
			Expect(pending()).To(BeTrue())
		})

		It("a pending mark on a module with no post-install hook is cleared and runs nothing", func() {
			for _, yaml := range []string{"{pre-install: hooks/pre.sh}", "{}"} {
				Expect(os.RemoveAll(filepath.Join(proj, ".lola"))).To(Succeed())
				Expect(os.Remove(log)).To(Or(Succeed(), MatchError(fs.ErrNotExist)))
				hooks(yaml, map[string]string{"hooks/pre.sh": script})
				_, err := install(engine.Options{Targets: claude.Targets, Ask: ask(true)}, src)
				Expect(err).NotTo(HaveOccurred())
				before := len(hookLog())
				edited := "#!/bin/sh\n# edited\n"
				writeTree(copyDir, map[string]string{"hooks/pre.sh": edited})
				lp := filepath.Join(proj, ".lola", "lock.json")
				l, err := ledger.Load(lp)
				Expect(err).NotTo(HaveOccurred())
				l.Modules["example"].HooksPending = true
				Expect(l.Save(lp)).To(Succeed())

				_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"example"}, Options: engine.Options{AllowHooks: true}})
				Expect(err).NotTo(HaveOccurred())
				Expect(hookLog()).To(HaveLen(before), yaml+": nothing ran")
				Expect(read(".lola/modules/example/hooks/pre.sh")).To(Equal(edited), yaml+": the copy was not forced")
				Expect(pending()).To(BeFalse(), yaml+": the stale mark is cleared")
			}
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

		It("a failed sync leaves the project as it was", func() {
			// The install form of §14 spec 24: a failing pre-install hook, and (at user scope, where
			// the manifest may be a link into another directory than the ledger) a manifest write
			// that fails inside plan.Apply after the files and the ledger were written.
			if os.Geteuid() == 0 {
				Skip("root ignores directory permissions")
			}
			snapshot := func(roots ...string) map[string]string {
				out := map[string]string{}
				for _, root := range roots {
					Expect(filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
						if err != nil || d.IsDir() {
							return err
						}
						if d.Type()&os.ModeSymlink != 0 {
							l, err := os.Readlink(p)
							out[p] = "-> " + l
							return err
						}
						b, err := os.ReadFile(p)
						out[p] = string(b)
						return err
					})).To(Succeed())
				}
				return out
			}
			allow := engine.Options{Targets: claude.Targets, AllowHooks: true}
			_, err := install(allow, src)
			Expect(err).NotTo(HaveOccurred())
			before := snapshot(proj)

			writeTree(src, map[string]string{"commands/ship.md": "v2\n", "hooks/pre.sh": "#!/bin/sh\nexit 3\n"})
			_, err = install(allow, src)
			Expect(err).To(MatchError(ContainSubstring("pre-install hook hooks/pre.sh of example failed")))
			Expect(snapshot(proj)).To(Equal(before))

			writeTree(src, map[string]string{"hooks/pre.sh": "#!/bin/sh\n"})
			user := engine.Scope{Kind: target.User}
			userInstall := func() error {
				_, err := e.Install(ctx, user, proj, []engine.InstallRequest{{Arg: src}}, allow)
				return err
			}
			Expect(userInstall()).To(Succeed())
			shared := filepath.Join(tmp, "shared")
			Expect(os.MkdirAll(shared, 0o755)).To(Succeed())
			Expect(os.Rename(p.UserManifest, filepath.Join(shared, "user.yml"))).To(Succeed())
			Expect(os.Symlink(filepath.Join(shared, "user.yml"), p.UserManifest)).To(Succeed())
			userRoots := []string{p.Home, p.ConfigHome, p.StateHome, shared, filepath.Join(p.DataHome, "lola", "user-modules")}
			before = snapshot(userRoots...)

			writeTree(src, map[string]string{"commands/ship.md": "v3\n"})
			Expect(os.Chmod(shared, 0o555)).To(Succeed())
			DeferCleanup(os.Chmod, shared, os.FileMode(0o755))
			Expect(userInstall()).To(MatchError(ContainSubstring("permission denied")))
			Expect(snapshot(userRoots...)).To(Equal(before))
		})

		It("dry-run lists the hooks and runs none; render runs none for the version installed", func() {
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
			_, err = e.Render(ctx, sc, allow)
			Expect(err).NotTo(HaveOccurred())
			Expect(hookLog()).To(HaveLen(2), "render leaves a folder's new content out, hooks and all")
			_, err = install(allow, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(hookLog()).To(HaveLen(4), "a new version runs them")
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
