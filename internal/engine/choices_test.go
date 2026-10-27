package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("InstallChoices (Plan 5c §7)", func() {
	var (
		tmp, proj, src, other string
		p                     paths.Paths
		e                     *engine.Engine
		ctx                   = context.Background()
		scopes                []engine.ScopeChoice
	)
	choices := func(mods ...string) *engine.Choices {
		req := engine.ChoicesRequest{Cwd: proj, Scopes: scopes}
		for _, m := range mods {
			req.Modules = append(req.Modules, engine.InstallRequest{Arg: m})
		}
		ch, err := e.InstallChoices(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		return ch
	}
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj = filepath.Join(tmp, "proj")
		src, other = filepath.Join(tmp, "src", "example"), filepath.Join(tmp, "a-src", "zeta")
		writeTree(src, exampleModule)
		writeTree(src, map[string]string{
			"lola.yml":         "name: example\ndescription: \"Review kit\\nsecond line\"\ncomponents:\n  runtime_hooks: [{path: hooks/hooks.json}]\n",
			"hooks/hooks.json": `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"}]},{"matcher":"m","hooks":[{"type":"command","command":"y"}]}]}}`,
			"commands/evil.md": "---\ndescription: \"bad\\e[31m red\\nmore\"\n---\nx\n",
		})
		writeTree(other, map[string]string{"lola.yml": "name: zeta\n", "commands/z.md": "Z\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(tmp, "bin", "claude"), []byte("#!/bin/sh\necho 2.1.287\n"), 0o755)).To(Succeed())
		scopes = []engine.ScopeChoice{{Scope: engine.Scope{Kind: target.Project, ProjectDir: proj}, Source: engine.SourcePicked},
			{Scope: engine.Scope{Kind: target.User}, Source: engine.SourcePicked}}
	})

	It("labels a template component (template), since its frontmatter exists only once rendered", func() {
		writeTree(other, map[string]string{"agents/t.md.liquid": "---\ndescription: {{ target | json }}\n---\nT\n"})
		m := choices(other).Module("zeta")
		Expect(m.Components).To(ContainElement(HaveField("Description", "(template)")))
	})

	It("lists cached modules by source then name, with components, labels made safe, and a source argument fetched", func() {
		_, err := e.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: other})
		Expect(err).NotTo(HaveOccurred())
		ch := choices(src)
		Expect(ch.Requested).To(Equal([]string{"example"}))
		Expect(ch.Modules).To(HaveLen(2))
		Expect(ch.Modules[0].Name).To(Equal("zeta"), "its source sorts first")
		m := ch.Module("example")
		Expect(m.Description).To(Equal("Review kit"))
		Expect(m.Source).To(Equal(src))
		Expect(m.Type).To(Equal("folder"))
		Expect(m.RuntimeHooks).To(Equal(2))
		var names []string
		for _, c := range m.Components {
			names = append(names, c.String())
		}
		Expect(names).To(Equal([]string{"skill/review", "command/evil", "command/ship", "agent/critic", "mcp/lola-gh", "rule/style"}))
		Expect(m.Components[0].Description).To(Equal("Review code"))
		Expect(m.Components[1].Description).To(Equal("bad?[31m red"), "control characters replaced, only the first line")
		Expect(m.Components[4].RunsCommand).To(BeTrue())
		Expect(m.Components[5].Description).To(Equal("Prefer small diffs."))
	})

	It("reports per scope the assistants, fixed targets, installed state, exclusions and current components", func() {
		_, err := e.Install(ctx, scopes[0].Scope, proj, []engine.InstallRequest{{Arg: src}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.MkdirAll(filepath.Dir(p.Selections), 0o700)).To(Succeed())
		Expect(os.WriteFile(p.Selections, []byte("version: 1\nuser:\n  example:\n    skip: [command/ship]\n"), 0o600)).To(Succeed())

		ch := choices()
		Expect(ch.Scopes[0].Name).To(Equal("project"))
		Expect(ch.Scopes[0].FixedTargets).To(Equal([]string{"claude-code"}))
		Expect(ch.Scopes[1].FixedTargets).To(Equal([]string{}))
		Expect(ch.Scopes[0].Assistants[0]).To(SatisfyAll(HaveField("Target", "claude-code"), HaveField("Marked", true), HaveField("Binary", Not(BeEmpty()))))

		m := ch.Module("example")
		pr, us := m.Scope(scopes[0].Scope.Key()), m.Scope("user")
		Expect(pr.Installed).To(BeTrue())
		Expect(pr.Targets).To(Equal([]string{"claude-code"}))
		Expect(pr.Current).To(ContainElements("command/ship", "skill/review", "rule/style"))
		Expect(pr.Excluded).To(BeEmpty())
		Expect(us.Installed).To(BeFalse())
		Expect(us.Excluded).To(Equal([]string{"command/ship"}))

		d := m.Diff(scopes[0].Scope.Key(), "claude-code", []string{"skill/review", "command/new"})
		Expect(d.Added).To(Equal([]string{"command/new"}))
		Expect(d.Unchanged).To(Equal(1))
		Expect(d.Removed).To(ContainElements("command/ship", "rule/style"))
		Expect(m.Diff(scopes[0].Scope.Key(), "gemini-cli", []string{"skill/review"}).Added).To(Equal([]string{"skill/review"}), "a new assistant gets everything")

		writeTree(src, map[string]string{"skills/review/SKILL.md": "---\ndescription: Review code better\n---\nReview it.\n"})
		m = choices(src).Module("example")
		Expect(m.Diff(scopes[0].Scope.Key(), "claude-code", []string{"skill/review"}).Updated).To(Equal([]string{"skill/review"}), "a new cached version updates")
	})

	It("pins the install to the version the choices listed, though the source changed in between", func() {
		ch := choices(src)
		Expect(ch.Versions).To(HaveLen(1))
		listed := ch.Module("example").Digest
		writeTree(src, map[string]string{"commands/ship.md": "Ship faster\n", "commands/new.md": "New\n"})
		res, err := e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Scopes: []engine.ScopeInstall{{ScopeChoice: scopes[0], Targets: []string{"claude-code"}}},
			Modules: []engine.InstallRequest{{Arg: src, Version: &ch.Versions[0]}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Modules[0].Digest).To(Equal(listed))
		b, err := os.ReadFile(filepath.Join(proj, ".claude/commands/ship.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("Ship $ARGUMENTS\n"))
		Expect(filepath.Join(proj, ".claude/commands/new.md")).NotTo(BeAnExistingFile())
		m, err := os.ReadFile(filepath.Join(proj, ".lola/project.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(m)).To(ContainSubstring("source: "+src), "the source is recorded as typed, not as the cache")
	})

	It("lists a pinned argument at its version again, without fetching the source", func() {
		ch := choices(src)
		listed := ch.Module("example").Digest
		writeTree(src, map[string]string{"commands/new.md": "New\n"})
		again, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: scopes,
			Modules: []engine.InstallRequest{{Arg: src, Version: &ch.Versions[0]}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(again.Versions).To(Equal(ch.Versions))
		Expect(again.Module("example").Digest).To(Equal(listed))
		Expect(again.Module("example").Components).NotTo(ContainElement(HaveField("Component.Name", "new")))
	})

	It("reports a module's state per destination, two project roots apart (Plan 5c §14)", func() {
		_, err := e.Install(ctx, scopes[0].Scope, proj, []engine.InstallRequest{{Arg: src}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		proj2 := filepath.Join(tmp, "proj2")
		Expect(os.MkdirAll(proj2, 0o755)).To(Succeed())
		second := engine.Scope{Kind: target.Project, ProjectDir: proj2}
		scopes = append(scopes, engine.ScopeChoice{Scope: second, Source: engine.SourcePicked})
		m := choices().Module("example")
		Expect(m.Scope(scopes[0].Scope.Key()).Installed).To(BeTrue())
		Expect(m.Scope(second.Key()).Installed).To(BeFalse())
		Expect(m.Scope(second.Key()).Scope).To(Equal(second.Key()))
		Expect(second.Key()).NotTo(Equal(scopes[0].Scope.Key()))
		Expect(engine.Scope{Kind: target.User}.Key()).To(Equal("user"))
	})

	It("records rules off at a scope", func() {
		off := false
		_, err := e.Install(ctx, scopes[0].Scope, proj, []engine.InstallRequest{{Arg: src}}, engine.Options{Targets: []string{"claude-code"}, Rules: &off})
		Expect(err).NotTo(HaveOccurred())
		m := choices().Module("example")
		Expect(m.Scope(scopes[0].Scope.Key()).RulesOff).To(BeTrue())
		Expect(m.Scope(scopes[0].Scope.Key()).Current).NotTo(ContainElement("rule/style"))
		Expect(m.Scope("user").RulesOff).To(BeFalse())
	})

	Describe("refusing a pin that does not fit", func() {
		var zeta engine.VersionRef
		BeforeEach(func() {
			ch := choices(other)
			zeta = ch.Versions[0]
		})
		It("refuses a source argument pinned to another module", func() {
			_, err := e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Scopes: []engine.ScopeInstall{{ScopeChoice: scopes[0], Targets: []string{"claude-code"}}},
				Modules: []engine.InstallRequest{{Arg: src, Version: &zeta}}})
			Expect(err).To(HaveOccurred())
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("lola install again")))
			Expect(filepath.Join(proj, ".lola/project.yml")).NotTo(BeAnExistingFile())
		})
		It("refuses a cache-name argument pinned to another module", func() {
			choices(src)
			_, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: scopes,
				Modules: []engine.InstallRequest{{Arg: "example", Version: &zeta}}})
			Expect(err).To(HaveOccurred())
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("lola install again")))
		})
		It("refuses a pin whose version was removed from the cache, without the file name", func() {
			gone := engine.VersionRef{Name: "zeta", ID: "0123456789ab"}
			_, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: scopes,
				Modules: []engine.InstallRequest{{Arg: other, Version: &gone}}})
			Expect(err).To(MatchError(ContainSubstring("was removed from the cache")))
			Expect(err.Error()).NotTo(ContainSubstring(p.Store))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("lola install again")))
		})
		It("refuses a pin with an invalid id", func() {
			bad := engine.VersionRef{Name: "zeta", ID: "../../x"}
			_, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: scopes,
				Modules: []engine.InstallRequest{{Arg: other, Version: &bad}}})
			Expect(err).To(HaveOccurred())
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("lola install again")))
		})
	})

	It("installs the pinned version of a cache-name argument after the source was fetched again", func() {
		ch := choices(src)
		listed := ch.Module("example").Digest
		writeTree(src, map[string]string{"commands/new.md": "New\n"})
		_, err := e.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src})
		Expect(err).NotTo(HaveOccurred())
		res, err := e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Scopes: []engine.ScopeInstall{{ScopeChoice: scopes[0], Targets: []string{"claude-code"}}},
			Modules: []engine.InstallRequest{{Arg: "example", Version: &ch.Versions[0]}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Modules[0].Digest).To(Equal(listed))
		Expect(filepath.Join(proj, ".claude/commands/new.md")).NotTo(BeAnExistingFile())
	})

	It("refuses two arguments that resolve to different versions of one module", func() {
		ch := choices(src)
		first := ch.Versions[0]
		writeTree(src, map[string]string{"commands/new.md": "New\n"})
		_, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: scopes,
			Modules: []engine.InstallRequest{{Arg: src, Version: &first}, {Arg: src}}})
		Expect(err).To(HaveOccurred())
		Expect(errs.Hints(err)).NotTo(BeEmpty())
	})

	It("still offers a requested module whose default head no longer loads", func() {
		ch := choices(src)
		pin := ch.Versions[0]
		writeTree(src, map[string]string{"commands/new.md": "New\n"})
		head, err := e.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src})
		Expect(err).NotTo(HaveOccurred())
		lola := filepath.Join(head.ContentDir, "lola.yml")
		Expect(os.Chmod(lola, 0o644)).To(Succeed())
		Expect(os.WriteFile(lola, []byte("name: example\nbogus_key: 1\n"), 0o644)).To(Succeed())
		Expect(choices().Module("example")).To(BeNil())
		again, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: scopes,
			Modules: []engine.InstallRequest{{Arg: src, Version: &pin}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(again.Module("example")).NotTo(BeNil())
		Expect(again.Module("example").Digest).To(Equal(ch.Module("example").Digest))
	})

	It("passes no raw escape sequence from the cache or a warning", func() {
		_, err := e.Install(ctx, scopes[0].Scope, proj, []engine.InstallRequest{{Arg: src}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		index := filepath.Join(p.Store, "example", "index.yml")
		b, err := os.ReadFile(index)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(index, []byte(strings.Replace(string(b), "type: folder", "type: \"folder\\e[31m\"", 1)), 0o644)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(p.Store, "broken"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(p.Store, "broken", "index.yml"), []byte("source: \"x\\e[31m\"\nbogus: 1\n"), 0o644)).To(Succeed())

		ch := choices()
		Expect(ch.Warnings).NotTo(BeEmpty())
		out, err := json.Marshal(ch)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).NotTo(ContainSubstring("\\u001b"))
		Expect(ch.Module("example").Type).To(ContainSubstring("folder"))
		Expect(ch.Module("example").Scope(scopes[0].Scope.Key()).Digest).To(ContainSubstring("sha256:"))
	})
})
