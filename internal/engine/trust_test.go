package engine_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
	"github.com/trevor-vaughan/lola-go/internal/trust"
)

var _ = Describe("Engine and the trust record", func() {
	var (
		tmp, proj, src, other, third string
		p                            paths.Paths
		e                            *engine.Engine
		sc                           engine.Scope
		ctx                          = context.Background()
		claude                       = engine.Options{Targets: []string{"claude-code"}}
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj = filepath.Join(tmp, "proj")
		e.Getwd = func() (string, error) { return proj, nil } // hints are written for a user in the project
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
		src, other, third = filepath.Join(tmp, "src", "example"), filepath.Join(tmp, "src", "other"), filepath.Join(tmp, "src", "third")
		writeTree(src, exampleModule)
		writeTree(other, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "Other\n"})
		writeTree(third, map[string]string{"lola.yml": "name: third\n", "commands/third.md": "Third\n"})
	})
	install := func(opts engine.Options, args ...string) error {
		var reqs []engine.InstallRequest
		for _, a := range args {
			reqs = append(reqs, engine.InstallRequest{Arg: a})
		}
		_, err := e.Install(ctx, sc, proj, reqs, opts)
		return err
	}
	loadManifest := func() *manifest.Manifest {
		m, err := e.Manifest(sc)
		Expect(err).NotTo(HaveOccurred())
		return m
	}
	// pull changes project.yml the way a git pull would: behind lola's back.
	pull := func(c manifest.Change) {
		dst, data, err := manifest.Edit(filepath.Join(proj, ".lola", "project.yml"), c)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(dst, data, 0o644)).To(Succeed())
	}
	record := func() trust.View {
		r, err := e.Trust.Load(proj)
		Expect(err).NotTo(HaveOccurred())
		return r.View
	}
	covered := func(name string) bool {
		ent := loadManifest().Find(name)
		Expect(ent).NotTo(BeNil(), name)
		return record().Covers(*ent)
	}

	It("records what install writes, and drops what uninstall removes", func() {
		Expect(install(claude, src, other)).To(Succeed())
		Expect(covered("example")).To(BeTrue())
		Expect(covered("other")).To(BeTrue())
		Expect(record().Targets).To(Equal(loadManifest().Targets))

		_, err := e.Uninstall(ctx, sc, []string{"other"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(record().Find("other")).To(BeNil())
		Expect(covered("example")).To(BeTrue())
	})

	It("lola's own manifest writes never trust someone else's entries", func() {
		Expect(install(claude, src)).To(Succeed())
		off := false
		pulled := *loadManifest().Find("example")
		pulled.Rules, pulled.Targets = &off, []string{"claude-code"}
		pull(manifest.Change{Set: []manifest.Entry{pulled, {Name: "other", Source: "../src/other"}}})
		Expect(covered("example")).To(BeFalse())

		Expect(install(engine.Options{}, third)).To(Succeed())
		Expect(covered("third")).To(BeTrue())
		Expect(covered("other")).To(BeFalse(), "a pulled entry stays untrusted")
		Expect(covered("example")).To(BeFalse(), "a pulled field change stays untrusted")
		Expect(loadManifest().Find("example").Rules).To(Equal(&off), "lola's write keeps the pulled field as it is")

		// Installing it again from its source writes the entry as new: it inherits nothing.
		Expect(install(engine.Options{}, src)).To(Succeed())
		ent := loadManifest().Find("example")
		Expect(ent.Rules).To(BeNil(), "the pulled rules: false is not inherited")
		Expect(ent.Targets).To(BeEmpty(), "the pulled targets are not inherited")
		Expect(covered("example")).To(BeTrue())
		Expect(covered("other")).To(BeFalse())
	})

	Describe("install by name never trusts a pulled source", func() {
		var evil string
		var before map[string]string
		// state is the bytes of everything a refused command must leave alone, and the files of
		// the cache, which a fetch would add to.
		state := func() map[string]string {
			out := map[string]string{}
			for _, f := range []string{filepath.Join(proj, ".lola", "project.yml"), filepath.Join(proj, ".lola", "lock.json"), p.Trust} {
				b, err := os.ReadFile(f)
				Expect(err).NotTo(HaveOccurred())
				out[f] = string(b)
			}
			Expect(filepath.WalkDir(filepath.Dir(p.Store), func(path string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					out[path] = "in the cache"
				}
				return err
			})).To(Succeed())
			return out
		}
		BeforeEach(func() {
			Expect(install(engine.Options{Targets: []string{"claude-code", "opencode"}}, src, third)).To(Succeed())
			evil = filepath.Join(tmp, "src", "evil")
			writeTree(evil, map[string]string{"lola.yml": "name: example\n", "commands/ship.md": "Evil\n"})
			pulled := *loadManifest().Find("example")
			pulled.Source, pulled.Commit, pulled.Digest = "../src/evil", "", ""
			pull(manifest.Change{Set: []manifest.Entry{pulled}})
			before = state()
		})
		refusal := "example: project.yml changed for it since you last trusted it"
		reviewIt := "review and apply it: lola sync example"

		It("refuses install by name, and the pickers never offer it, before anything is fetched or written", func() {
			err := install(engine.Options{}, "example")
			Expect(err).To(MatchError(refusal))
			Expect(errs.Hints(err)).To(Equal([]string{reviewIt}))
			Expect(state()).To(Equal(before))

			req := engine.ChoicesRequest{Cwd: proj, Scopes: []engine.ScopeChoice{{Scope: sc, Source: engine.SourceFlag}}}
			ch, err := e.InstallChoices(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(ch.Module("example")).To(BeNil())
			Expect(ch.Module("third")).NotTo(BeNil())
			Expect(ch.Warnings).To(ContainElement("1 module in project.yml changed since you last trusted it: example → lola sync"))
			req.Modules = []engine.InstallRequest{{Arg: "example"}}
			_, err = e.InstallChoices(ctx, req)
			Expect(err).To(MatchError(refusal))
			Expect(state()).To(Equal(before))
		})

		It("refuses the name among other arguments, at any scope, before anything is fetched or written", func() {
			Expect(install(engine.Options{}, other, "example")).To(MatchError(refusal))
			Expect(state()).To(Equal(before))

			_, err := e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Modules: []engine.InstallRequest{{Arg: other}, {Arg: "example"}},
				Scopes: []engine.ScopeInstall{
					{ScopeChoice: engine.ScopeChoice{Scope: engine.Scope{Kind: target.User}, Source: engine.SourceFlag}, Targets: claude.Targets},
					{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}, Targets: claude.Targets},
				}})
			Expect(err).To(MatchError(refusal))
			Expect(state()).To(Equal(before))

			_, err = e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: []engine.ScopeChoice{{Scope: sc, Source: engine.SourceFlag}},
				Modules: []engine.InstallRequest{{Arg: other}, {Arg: "example"}}})
			Expect(err).To(MatchError(refusal))
			Expect(state()).To(Equal(before))
		})

		It("installing it from its source trusts it as written", func() {
			off := false
			pulled := *loadManifest().Find("example")
			pulled.Rules = &off
			pull(manifest.Change{Set: []manifest.Entry{pulled}})
			err := install(engine.Options{}, evil)
			Expect(err).To(MatchError(ContainSubstring("a different module named \"example\" is already in your cache")))
			_, err = e.Store.Remove("example") // as that refusal coaches
			Expect(err).NotTo(HaveOccurred())
			ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: []engine.ScopeChoice{{Scope: sc, Source: engine.SourceFlag}},
				Modules: []engine.InstallRequest{{Arg: evil}}})
			Expect(err).NotTo(HaveOccurred())
			Expect(ch.Module("example").Scope(sc.Key()).RulesOff).To(BeFalse(), "the pickers show what the install writes: no pulled rules: false")
			Expect(install(engine.Options{}, evil)).To(Succeed())
			Expect(covered("example")).To(BeTrue())
			Expect(loadManifest().Find("example").Rules).To(BeNil())
			Expect(install(engine.Options{}, "example")).To(Succeed(), "now lola wrote it")
		})

		It("refuses a partial uninstall, but removes it whole and forgets it", func() {
			_, err := e.Uninstall(ctx, sc, []string{"example"}, engine.Options{Targets: []string{"opencode"}})
			Expect(err).To(MatchError(refusal))
			Expect(errs.Hints(err)).To(Equal([]string{reviewIt, "or remove it: lola uninstall example"}))
			Expect(state()).To(Equal(before))

			_, err = e.Uninstall(ctx, sc, []string{"example"}, engine.Options{Targets: []string{"claude-code", "opencode"}})
			Expect(err).NotTo(HaveOccurred(), "every target is a whole uninstall")
			Expect(loadManifest().Find("example")).To(BeNil())
			Expect(record().Find("example")).To(BeNil())
			Expect(covered("third")).To(BeTrue())
		})
	})

	Describe("render", func() {
		// rendered says whether each module's command file is rendered in the project.
		rendered := func(names ...string) map[string]bool {
			out := map[string]bool{}
			for _, n := range names {
				file := map[string]string{"example": "ship", "other": "other", "third": "third", "kit": "kit"}[n]
				_, err := os.Stat(filepath.Join(proj, ".claude", "commands", file+".md"))
				out[n] = err == nil
			}
			return out
		}
		removeRendered := func() {
			for _, f := range []string{"ship", "other", "third", "kit"} {
				Expect(os.RemoveAll(filepath.Join(proj, ".claude", "commands", f+".md"))).To(Succeed())
			}
		}
		leftOut := func(name, code, text, hint string) types.GomegaMatcher {
			return And(HaveField("Severity", target.SeverityWarn), HaveField("Module", name), HaveField("Kind", "module"),
				HaveField("Code", code), HaveField("Text", text), HaveField("Hint", hint))
		}

		It("render refuses untrusted state", func() {
			Expect(install(claude, src, other, third)).To(Succeed())
			off := false
			pulled := *loadManifest().Find("example")
			pulled.Rules = &off
			pull(manifest.Change{Set: []manifest.Entry{pulled}, Remove: []string{"other"}})
			removeRendered()

			res, err := e.Render(ctx, sc, engine.Options{})
			Expect(err).NotTo(HaveOccurred(), "left out, not refused: the other modules still render")
			Expect(res.Findings).To(ContainElements(
				leftOut("example", engine.CodeNotTrusted, "project.yml changed for it since you last trusted it", "review and apply it: lola sync example"),
				leftOut("other", engine.CodeNotInManifest, "project.yml no longer lists it", "review: lola sync other")))
			Expect(rendered("example", "other", "third")).To(Equal(map[string]bool{"example": false, "other": false, "third": true}))
			Expect(res.Modules).To(ConsistOf(HaveField("Name", "third")))
			led, err := e.Ledger(sc)
			Expect(err).NotTo(HaveOccurred())
			Expect(led.Modules).To(HaveKey("other"), "left as installed")

			_, err = e.Render(ctx, sc, engine.Options{Strict: true})
			Expect(err).To(MatchError(HavePrefix("--strict: 2 finding(s); nothing was changed")))

			// Top-level targets the record does not cover leave out the entries that install to them.
			Expect(install(engine.Options{}, src)).To(Succeed())
			pull(manifest.Change{Targets: []string{"claude-code", "opencode"}})
			removeRendered()
			res, err = e.Render(ctx, sc, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Findings).To(ContainElements(
				leftOut("example", engine.CodeNotTrusted, "project.yml changed for it since you last trusted it", "review and apply it: lola sync example"),
				leftOut("third", engine.CodeNotTrusted, "project.yml changed for it since you last trusted it", "review and apply it: lola sync third")))
			Expect(rendered("example", "third")).To(Equal(map[string]bool{"example": false, "third": false}))

			// A project this machine has no record of leaves out every module.
			Expect(os.Remove(p.Trust)).To(Succeed())
			res, err = e.Render(ctx, sc, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Findings).To(ContainElement(leftOut("example", engine.CodeNotTrusted, "project.yml has not been trusted on this machine",
				"review and apply it: lola sync example")))

			// A tracked lock.json is still refused whole.
			gitIn(proj, "init", "-q")
			gitIn(proj, "add", "-f", ".lola/lock.json")
			_, err = e.Render(ctx, sc, engine.Options{})
			Expect(err).To(MatchError(HavePrefix(".lola/lock.json is tracked by git")))
		})

		It("render leaves out a folder module whose content changed since lola installed it", func() {
			kit := filepath.Join(proj, "mods", "kit")
			writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Kit\n"})
			Expect(install(claude, src, kit)).To(Succeed())
			writeTree(kit, map[string]string{"mcps.json": `{"mcpServers":{"gh":{"command":"npx","args":["-y","@acme/gh-mcp"]}}}`})
			removeRendered()
			cached := func() []string {
				entries, err := os.ReadDir(filepath.Join(p.Store, "kit", "versions"))
				Expect(err).NotTo(HaveOccurred())
				var ids []string
				for _, d := range entries {
					ids = append(ids, d.Name())
				}
				return ids
			}
			before := cached()

			sub := filepath.Join(proj, "sub")
			Expect(os.MkdirAll(sub, 0o755)).To(Succeed())
			for _, c := range []struct{ wd, install string }{{proj, "lola install ./mods/kit"}, {sub, "lola install ../mods/kit"}} {
				e.Getwd = func() (string, error) { return c.wd, nil }
				res, err := e.Render(ctx, sc, engine.Options{})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Findings).To(ContainElement(leftOut("kit", engine.CodeContentChanged, "its content changed since lola installed it",
					"review it: lola sync kit; edited it yourself? "+c.install)))
				Expect(rendered("example", "kit")).To(Equal(map[string]bool{"example": true, "kit": false}))
				mcp, err := os.ReadFile(filepath.Join(proj, ".mcp.json"))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(mcp)).NotTo(ContainSubstring("@acme/gh-mcp"), "the new MCP server is not installed")
				Expect(cached()).To(Equal(before), "the changed content is not cached")
			}
		})

		It("render leaves out a folder module that no longer loads and renders the others", func() {
			kit := filepath.Join(proj, "mods", "kit")
			writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Kit\n"})
			Expect(install(claude, src, kit)).To(Succeed())
			for _, yml := range []string{"name: kit\nbogus: 1\n", "name: kit2\n"} {
				writeTree(kit, map[string]string{"lola.yml": yml})
				removeRendered()
				res, err := e.Render(ctx, sc, engine.Options{})
				Expect(err).NotTo(HaveOccurred(), yml)
				Expect(res.Findings).To(ContainElement(And(HaveField("Code", engine.CodeContentChanged), HaveField("Module", "kit"),
					HaveField("Text", HavePrefix("its content no longer loads: ")),
					HaveField("Hint", HavePrefix("review it: lola sync kit")))), yml)
				Expect(rendered("example", "kit")).To(Equal(map[string]bool{"example": true, "kit": false}), yml)
			}
		})

		It("render leaves out a module whose manifest entry asks for another source than is installed", func() {
			Expect(install(claude, src, other)).To(Succeed())
			pulled := *loadManifest().Find("other")
			pulled.Source = third
			pull(manifest.Change{Set: []manifest.Entry{pulled}})
			removeRendered()
			res, err := e.Render(ctx, sc, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Findings).To(ContainElement(leftOut("other", engine.CodeSourceDiffers, "project.yml asks for another source than is installed",
				"review and apply it: lola sync other")))
			Expect(rendered("example", "other")).To(Equal(map[string]bool{"example": true, "other": false}))

			// At user scope, whose user.yml the user edits by hand.
			user := engine.Scope{Kind: target.User}
			_, err = e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: other}}, claude)
			Expect(err).NotTo(HaveOccurred())
			m, err := e.Manifest(user)
			Expect(err).NotTo(HaveOccurred())
			edited := *m.Find("other")
			edited.Ref = "v2"
			dst, data, err := manifest.Edit(p.UserManifest, manifest.Change{Set: []manifest.Entry{edited}})
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(dst, data, 0o600)).To(Succeed())
			Expect(os.Remove(filepath.Join(p.Home, ".claude", "commands", "other.md"))).To(Succeed())
			res, err = e.Render(ctx, user, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Findings).To(ContainElement(leftOut("other", engine.CodeSourceDiffers, "user.yml asks for another source than is installed",
				"review and apply it: lola sync --user other")))
			Expect(filepath.Join(p.Home, ".claude", "commands", "other.md")).NotTo(BeAnExistingFile())
		})

		It("render --strict names the remedy for each module it leaves out", func() {
			kit := filepath.Join(proj, "mods", "kit")
			writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Kit\n"})
			Expect(install(claude, src, kit, other)).To(Succeed())
			off := false
			pulled := *loadManifest().Find("example")
			pulled.Rules = &off
			pull(manifest.Change{Set: []manifest.Entry{pulled}})
			writeTree(kit, map[string]string{"commands/kit.md": "Kit, edited\n"})
			removeRendered()

			res, err := e.Render(ctx, sc, engine.Options{})
			Expect(err).NotTo(HaveOccurred(), "without --strict a left-out module exits 0 with its warning")
			Expect(res.Findings).To(ContainElements(HaveField("Code", engine.CodeNotTrusted), HaveField("Code", engine.CodeContentChanged)))
			removeRendered()
			_, err = e.Render(ctx, sc, engine.Options{Strict: true})
			Expect(err).To(MatchError(HavePrefix("--strict: 2 finding(s); nothing was changed")))
			Expect(errs.Hints(err)).To(Equal([]string{"review and apply it: lola sync example", "review it: lola sync kit",
				"edited it yourself? lola install ./mods/kit"}), "each module's own remedy, and no generic advice")
			Expect(rendered("other")).To(Equal(map[string]bool{"other": false}), "nothing was changed")

			// Another finding brings the generic advice back, after the remedies.
			writeTree(other, map[string]string{"agents/bare.md": "Criticize without a description.\n"})
			Expect(install(engine.Options{}, other)).To(Succeed())
			_, err = e.Render(ctx, sc, engine.Options{Strict: true})
			Expect(err).To(MatchError(HavePrefix("--strict: 3 finding(s); nothing was changed")))
			Expect(errs.Hints(err)).To(HaveExactElements("review and apply it: lola sync example", "review it: lola sync kit",
				"edited it yourself? lola install ./mods/kit", ContainSubstring("rerun without --strict"), ContainSubstring("--force")))
		})
	})

	It("install refuses untrusted top-level targets it would use", func() {
		Expect(install(claude, src)).To(Succeed())
		pull(manifest.Change{Targets: []string{"claude-code", "opencode"}})
		before := loadManifest()
		refused := func(err error, args, why string) {
			GinkgoHelper()
			Expect(err).To(MatchError("project.yml's top-level targets " + why))
			Expect(errs.Hints(err)).To(Equal([]string{"review and apply them: lola sync", "or name the assistants: lola install " + args + " -t <id>"}),
				"never a retry that refuses again")
			Expect(loadManifest()).To(Equal(before))
			Expect(filepath.Join(proj, ".opencode")).NotTo(BeAnExistingFile())
		}
		refused(install(engine.Options{}, "example"), "example", "changed since you last trusted them")
		refused(install(engine.Options{}, other), other, "changed since you last trusted them")
		// From outside the project, both hints name it.
		e.Getwd = func() (string, error) { return tmp, nil }
		err := install(engine.Options{}, other)
		Expect(errs.Hints(err)).To(Equal([]string{"review and apply them: lola sync -C " + proj,
			"or name the assistants: lola install -C " + proj + " " + other + " -t <id>"}))
		e.Getwd = func() (string, error) { return proj, nil }
		Expect(install(engine.Options{Targets: []string{"claude-code"}}, other)).To(Succeed(), "-t names the assistants")
		Expect(filepath.Join(proj, ".claude", "commands", "other.md")).To(BeARegularFile())

		// A project this machine has no record of.
		Expect(os.Remove(p.Trust)).To(Succeed())
		before = loadManifest()
		refused(install(engine.Options{}, third), third, "have not been trusted on this machine")
		ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: []engine.ScopeChoice{{Scope: sc, Source: engine.SourceFlag}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(ch.Scopes[0].FixedTargets).To(BeEmpty(), "the pickers never take them unasked")
		Expect(ch.Scopes[0].UntrustedTargets).To(Equal([]string{"claude-code", "opencode"}))
	})

	It("install refuses untrusted top-level targets before it fetches, and carries --ref into the hint", func() {
		work, bare := filepath.Join(tmp, "work"), filepath.Join(tmp, "srv", "gitmod.git")
		writeTree(work, map[string]string{"lola.yml": "name: gitmod\n", "commands/gitmod.md": "Git\n"})
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "init")
		gitIn(work, "tag", "v2")
		gitIn(tmp, "clone", "-q", "--bare", work, bare)
		var requests atomic.Int32
		url := testenv.ServeGitWith(filepath.Join(tmp, "srv"), func(*http.Request) { requests.Add(1) }) + "/gitmod.git"
		cached := func() []string {
			entries, err := os.ReadDir(p.Store)
			if os.IsNotExist(err) {
				return nil
			}
			Expect(err).NotTo(HaveOccurred())
			var names []string
			for _, ent := range entries {
				names = append(names, ent.Name())
			}
			return names
		}

		Expect(install(claude, src)).To(Succeed())
		pull(manifest.Change{Targets: []string{"claude-code", "opencode"}})
		before, inCache := loadManifest(), cached()

		_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "v2"}}, engine.Options{})
		Expect(err).To(MatchError("project.yml's top-level targets changed since you last trusted them"))
		Expect(errs.Hints(err)[1]).To(Equal("or name the assistants: lola install " + url + " --ref v2 -t <id>"))
		_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Path: "sub"}}, engine.Options{})
		Expect(errs.Hints(err)[1]).To(ContainSubstring(url + " --path sub -t <id>"))
		Expect(requests.Load()).To(BeZero(), "the git server saw no request")
		Expect(cached()).To(Equal(inCache), "the cache is unchanged")
		Expect(loadManifest()).To(Equal(before))

		_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "example", Ref: "v1"}}, engine.Options{})
		Expect(err).To(MatchError(ContainSubstring("top-level targets")))
		Expect(requests.Load()).To(BeZero())
	})

	It("install of an entry with targets of its own needs no -t while the top-level targets are untrusted", func() {
		Expect(install(claude, src)).To(Succeed())
		Expect(install(engine.Options{Targets: []string{"opencode"}}, other)).To(Succeed())
		Expect(loadManifest().Find("other").Targets).To(Equal([]string{"opencode"}))
		pull(manifest.Change{Targets: []string{"claude-code", "gemini-cli"}})
		Expect(install(engine.Options{}, other)).To(Succeed(), "its own targets are covered")
		Expect(filepath.Join(proj, ".gemini")).NotTo(BeAnExistingFile())
	})

	It("never records trust at user scope", func() {
		_, err := e.Install(ctx, engine.Scope{Kind: target.User}, tmp, []engine.InstallRequest{{Arg: src}}, claude)
		Expect(err).NotTo(HaveOccurred())
		_, err = e.Uninstall(ctx, engine.Scope{Kind: target.User}, []string{"example"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Trust).NotTo(BeAnExistingFile())
	})

	It("a dry run records nothing", func() {
		Expect(install(engine.Options{Targets: claude.Targets, DryRun: true}, src)).To(Succeed())
		Expect(p.Trust).NotTo(BeAnExistingFile())
	})

	It("says what was applied when the trust record cannot be written, and trusts none of it", func() {
		Expect(os.MkdirAll(p.TrustLock, 0o700)).To(Succeed()) // a lock that cannot be taken
		err := install(claude, src)
		Expect(err).To(MatchError(ContainSubstring("the project's files, lock.json and project.yml were updated, but lola could not record")))
		Expect(filepath.Join(proj, ".claude/commands/ship.md")).To(BeARegularFile())
		Expect(loadManifest().Find("example")).NotTo(BeNil())
		Expect(covered("example")).To(BeFalse(), "fail closed")
	})

	It("says an uninstall removed what it was asked to when the trust record cannot be written", func() {
		Expect(install(claude, src)).To(Succeed())
		Expect(os.Remove(p.TrustLock)).To(Succeed())
		Expect(os.MkdirAll(p.TrustLock, 0o700)).To(Succeed()) // a lock that cannot be taken
		_, err := e.Uninstall(ctx, sc, []string{"example"}, engine.Options{})
		Expect(err).To(MatchError(HavePrefix("lola removed what you uninstalled from the project's files, lock.json and project.yml, " +
			"but lola could not record that you trust what it wrote to project.yml: ")))
		Expect(errs.Hints(err)).To(Equal([]string{"until it can, lola treats those entries as changed behind its back; " +
			"fix the trust file, then run lola sync again to review and trust them", "replace it with a regular file"}))
		Expect(loadManifest().Find("example")).To(BeNil())
	})

	// both installs at the project and the user scope together, as the install pickers do.
	both := func(args ...string) error {
		var reqs []engine.InstallRequest
		for _, a := range args {
			reqs = append(reqs, engine.InstallRequest{Arg: a})
		}
		_, err := e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Modules: reqs, Scopes: []engine.ScopeInstall{
			{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}, Targets: claude.Targets},
			{ScopeChoice: engine.ScopeChoice{Scope: engine.Scope{Kind: target.User}, Source: engine.SourceFlag}, Targets: claude.Targets},
		}})
		return err
	}

	It("names the scopes it did not reach when the trust record cannot be written", func() {
		Expect(os.MkdirAll(p.TrustLock, 0o700)).To(Succeed())
		err := both(src)
		Expect(err).To(MatchError(ContainSubstring("lola could not record")))
		Expect(err).To(MatchError(ContainSubstring("lola stopped there and did not install into the user scope")))
		Expect(errs.Hints(err)).To(ContainElement("fetched modules stay in your cache; retry with: lola install example; lola install --user example"),
			"a retry at each scope")
		rows, err := e.Status(engine.Scope{Kind: target.User})
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(BeEmpty())
	})

	It("records only the project when one install spans the project and the user scope", func() {
		Expect(both(src)).To(Succeed())
		Expect(covered("example")).To(BeTrue())
		b, err := os.ReadFile(p.Trust)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Count(string(b), `"sha256"`)).To(Equal(1), "one record: the project's")
	})

	It("leaves out a module whose pulled ref the cache has no version of with the trust notice, not the install-by-name one", func() {
		Expect(install(claude, src)).To(Succeed())
		pulled := *loadManifest().Find("example")
		pulled.Ref = "v9"
		pull(manifest.Change{Set: []manifest.Entry{pulled}})

		ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: []engine.ScopeChoice{{Scope: sc, Source: engine.SourceFlag}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(ch.Module("example")).To(BeNil())
		Expect(ch.Warnings).To(ConsistOf("1 module in project.yml changed since you last trusted it: example → lola sync"))
	})

	Describe("an entry whose source project.yml records relative to the project", func() {
		BeforeEach(func() {
			writeTree(filepath.Join(proj, "mods", "review"), map[string]string{"lola.yml": "name: review\n", "commands/review.md": "Review\n"})
			writeTree(filepath.Join(proj, "mods", "gone"), map[string]string{"lola.yml": "name: gone\n", "commands/gone.md": "Gone\n"})
			Expect(os.MkdirAll(filepath.Join(proj, "sub"), 0o755)).To(Succeed())
			Expect(install(claude, "./mods/review")).To(Succeed())
			off := false
			pulled := *loadManifest().Find("review")
			Expect(pulled.Source).To(Equal("mods/review"))
			pulled.Rules = &off
			pull(manifest.Change{Set: []manifest.Entry{pulled, {Name: "gone", Source: "mods/gone"}}})
		})

		DescribeTable("refusals coach lola sync, never installing from the source",
			func(dir, flags string, fresh bool) {
				cwd := filepath.Join(tmp, dir)
				flags = strings.ReplaceAll(flags, "PROJ", proj)
				e.Getwd = func() (string, error) { return cwd, nil }
				why, whyNew := "changed for it since you last trusted it", "added it since you last trusted the file"
				notice := "2 modules in project.yml are new or changed since you last trusted the file: gone, review → lola sync" + flags
				if fresh {
					// A fresh clone, or a moved project: this machine has no record of the project.
					Expect(os.Remove(p.Trust)).To(Succeed())
					why, whyNew = "has not been trusted on this machine", "has not been trusted on this machine"
					notice = "2 modules in project.yml are not trusted on this machine yet: gone, review → lola sync" + flags
				}
				review := "review and apply it: lola sync" + flags + " review"
				_, err := e.Install(ctx, sc, cwd, []engine.InstallRequest{{Arg: "review"}}, engine.Options{})
				Expect(err).To(MatchError("review: project.yml " + why))
				Expect(errs.Hints(err)).To(Equal([]string{review}))
				_, err = e.Install(ctx, sc, cwd, []engine.InstallRequest{{Arg: "gone"}}, engine.Options{})
				Expect(err).To(MatchError("gone: project.yml "+whyNew), "an entry the record never held was never trusted")

				ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: cwd, Scopes: []engine.ScopeChoice{{Scope: sc, Source: engine.SourceFlag}}})
				Expect(err).NotTo(HaveOccurred())
				Expect(ch.Warnings).To(ConsistOf(notice), "one notice for every entry it leaves out")
				Expect(ch.Module("review")).To(BeNil())

				_, err = e.Uninstall(ctx, sc, []string{"review"}, engine.Options{Targets: []string{"opencode"}})
				Expect(err).To(MatchError("review: project.yml " + why))
				Expect(errs.Hints(err)).To(Equal([]string{review, "or remove it: lola uninstall" + flags + " review"}))

				// Following the hint off a terminal: lola sync review --trust.
				_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"review"}, Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }})
				Expect(err).NotTo(HaveOccurred())
				Expect(loadManifest().Find("review").Source).To(Equal("mods/review"), "a committed project.yml stays portable")
				Expect(covered("review")).To(BeTrue())
				Expect(covered("gone")).To(BeFalse(), "the entry not named stays untrusted")
				ch, err = e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: cwd, Scopes: []engine.ScopeChoice{{Scope: sc, Source: engine.SourceFlag}}})
				Expect(err).NotTo(HaveOccurred())
				Expect(ch.Warnings).To(ConsistOf("1 module in project.yml is new since you last trusted the file: gone → lola sync" + flags))
			},
			Entry("at the project root", "proj", "", false),
			Entry("below it", "proj/sub", "", false),
			Entry("above it", ".", " -C PROJ", false),
			Entry("in a fresh clone", "proj", "", true),
		)
	})

	Describe("installing from its source over an entry a pull changed", func() {
		// installedTargets is what lock.json installed example on, sorted.
		installedTargets := func() []string {
			rows, err := e.Status(sc)
			Expect(err).NotTo(HaveOccurred())
			var out []string
			for _, r := range rows {
				if r.Name == "example" && r.Status == "ok" {
					out = append(out, r.Target)
				}
			}
			return out
		}

		It("keeps installed what this checkout installed, and records exactly that", func() {
			Expect(install(claude, src)).To(Succeed())
			Expect(install(engine.Options{Targets: []string{"claude-code", "opencode"}}, "example")).To(Succeed())
			off := false
			pulled := *loadManifest().Find("example")
			pulled.Rules = &off
			pull(manifest.Change{Set: []manifest.Entry{pulled}})

			Expect(install(engine.Options{}, src)).To(Succeed())
			man := loadManifest()
			ent := man.Find("example")
			effective := man.Targets
			if len(ent.Targets) > 0 {
				effective = ent.Targets
			}
			Expect(effective).To(Equal([]string{"claude-code", "opencode"}), "installed equals recorded")
			Expect(installedTargets()).To(Equal(effective))
			Expect(ent.Rules).To(BeNil())
			Expect(covered("example")).To(BeTrue())
		})

		It("inherits no pulled ref or path", func() {
			Expect(install(claude, src)).To(Succeed())
			pulled := *loadManifest().Find("example")
			pulled.Ref, pulled.Path = "evil", "sub"
			pull(manifest.Change{Set: []manifest.Entry{pulled}})

			Expect(install(engine.Options{}, src)).To(Succeed())
			ent := loadManifest().Find("example")
			Expect(ent.Ref).To(BeEmpty())
			Expect(ent.Path).To(BeEmpty())
			Expect(covered("example")).To(BeTrue())
		})

		It("refuses a request that adds targets to it with the sync hint alone, never a retry that refuses again", func() {
			Expect(install(claude, src)).To(Succeed())
			pulled := *loadManifest().Find("example")
			pulled.Ref = "evil"
			pull(manifest.Change{Set: []manifest.Entry{pulled}})

			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: other}, {Arg: src, Targets: []string{"opencode"}}}, engine.Options{})
			Expect(err).To(MatchError("example: project.yml changed for it since you last trusted it"))
			Expect(errs.Hints(err)).To(Equal([]string{"review and apply it: lola sync example"}))
		})
	})
})
