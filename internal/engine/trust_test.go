package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

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

		_, err := e.Uninstall(ctx, sc, proj, []string{"other"}, engine.Options{})
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
		var trustIt string
		// project.yml records the pulled source relative to the project, so the hint names it
		// relative to the directory lola runs in, here the project.
		JustBeforeEach(func() { trustIt = "if you trust it, install it from its source: lola install ../src/evil" })

		It("refuses install by name, and the pickers never offer it, before anything is fetched or written", func() {
			err := install(engine.Options{}, "example")
			Expect(err).To(MatchError(refusal))
			Expect(errs.Hints(err)).To(Equal([]string{trustIt}))
			Expect(state()).To(Equal(before))

			req := engine.ChoicesRequest{Cwd: proj, Scopes: []engine.ScopeChoice{{Scope: sc, Source: engine.SourceFlag}}}
			ch, err := e.InstallChoices(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(ch.Module("example")).To(BeNil())
			Expect(ch.Module("third")).NotTo(BeNil())
			Expect(ch.Warnings).To(ContainElement("example is not listed: project.yml changed for it since you last trusted it; " + trustIt))
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

		It("installing it from its source, as the hint says, trusts it as written", func() {
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
			_, err := e.Uninstall(ctx, sc, proj, []string{"example"}, engine.Options{Targets: []string{"opencode"}})
			Expect(err).To(MatchError(refusal))
			Expect(errs.Hints(err)).To(Equal([]string{trustIt, "or remove it: lola uninstall example"}))
			Expect(state()).To(Equal(before))

			_, err = e.Uninstall(ctx, sc, proj, []string{"example"}, engine.Options{Targets: []string{"claude-code", "opencode"}})
			Expect(err).NotTo(HaveOccurred(), "every target is a whole uninstall")
			Expect(loadManifest().Find("example")).To(BeNil())
			Expect(record().Find("example")).To(BeNil())
			Expect(covered("third")).To(BeTrue())
		})
	})

	It("never records trust at user scope", func() {
		_, err := e.Install(ctx, engine.Scope{Kind: target.User}, tmp, []engine.InstallRequest{{Arg: src}}, claude)
		Expect(err).NotTo(HaveOccurred())
		_, err = e.Uninstall(ctx, engine.Scope{Kind: target.User}, tmp, []string{"example"}, engine.Options{})
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

	It("leaves out a module whose pulled ref the cache has no version of with the trust warning, not the install-by-name one", func() {
		Expect(install(claude, src)).To(Succeed())
		pulled := *loadManifest().Find("example")
		pulled.Ref = "v9"
		pull(manifest.Change{Set: []manifest.Entry{pulled}})

		ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: []engine.ScopeChoice{{Scope: sc, Source: engine.SourceFlag}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(ch.Module("example")).To(BeNil())
		Expect(ch.Warnings).To(ContainElement(And(
			ContainSubstring("example is not listed: project.yml changed for it since you last trusted it"),
			ContainSubstring("if you trust it, install it from its source: lola install "+src)),
		))
		Expect(ch.Warnings).NotTo(ContainElement(ContainSubstring("install it by name")))
	})

	It("coaches a source recorded through a symlinked root with a location that resolves", func() {
		real := filepath.Join(tmp, "data", "proj")
		Expect(os.MkdirAll(real, 0o755)).To(Succeed())
		link := filepath.Join(tmp, "home", "me", "proj")
		Expect(os.MkdirAll(filepath.Dir(link), 0o755)).To(Succeed())
		Expect(os.Symlink(real, link)).To(Succeed())
		shared := filepath.Join(tmp, "home", "me", "shared", "example")
		writeTree(shared, exampleModule)
		lsc := engine.Scope{Kind: target.Project, ProjectDir: link}
		_, err := e.Install(ctx, lsc, link, []engine.InstallRequest{{Arg: "../shared/example"}}, claude)
		Expect(err).NotTo(HaveOccurred())
		m, err := e.Manifest(lsc)
		Expect(err).NotTo(HaveOccurred())
		off := false
		pulled := *m.Find("example")
		pulled.Rules = &off
		dst, data, err := manifest.Edit(filepath.Join(link, ".lola", "project.yml"), manifest.Change{Set: []manifest.Entry{pulled}})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(dst, data, 0o644)).To(Succeed())

		_, err = e.Install(ctx, lsc, link, []engine.InstallRequest{{Arg: "example"}}, engine.Options{})
		Expect(err).To(MatchError(ContainSubstring("changed for it since you last trusted it")))
		Expect(errs.Hints(err)).To(Equal([]string{"if you trust it, install it from its source: lola install ../shared/example"}))

		_, err = e.Install(ctx, lsc, link, []engine.InstallRequest{{Arg: "../shared/example"}}, engine.Options{})
		Expect(err).NotTo(HaveOccurred(), "the hint, run where it was given")
		m, err = e.Manifest(lsc)
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Find("example").Source).To(Equal(pulled.Source), "recorded as it was")
	})

	Describe("an entry whose source project.yml records relative to the project", func() {
		BeforeEach(func() {
			writeTree(filepath.Join(proj, "mods", "review"), map[string]string{"lola.yml": "name: review\n", "commands/review.md": "Review\n"})
			Expect(os.MkdirAll(filepath.Join(proj, "sub"), 0o755)).To(Succeed())
			Expect(install(claude, "./mods/review")).To(Succeed())
			off := false
			pulled := *loadManifest().Find("review")
			Expect(pulled.Source).To(Equal("mods/review"))
			pulled.Rules = &off
			pull(manifest.Change{Set: []manifest.Entry{pulled, {Name: "gone", Source: "mods/gone"}}})
		})

		DescribeTable("is hinted relative to the directory lola runs in, and following the hint keeps it relative",
			func(dir, arg string) {
				cwd := filepath.Join(tmp, dir)
				hint := "if you trust it, install it from its source: lola install " + arg
				_, err := e.Install(ctx, sc, cwd, []engine.InstallRequest{{Arg: "review"}}, engine.Options{})
				Expect(err).To(MatchError("review: project.yml changed for it since you last trusted it"))
				Expect(errs.Hints(err)).To(Equal([]string{hint}))

				ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: cwd, Scopes: []engine.ScopeChoice{{Scope: sc, Source: engine.SourceFlag}}})
				Expect(err).NotTo(HaveOccurred())
				Expect(ch.Warnings).To(ContainElement("review is not listed: project.yml changed for it since you last trusted it; " + hint))

				_, err = e.Uninstall(ctx, sc, cwd, []string{"review"}, engine.Options{Targets: []string{"opencode"}})
				Expect(err).To(MatchError(ContainSubstring("changed for it since you last trusted it")))
				Expect(errs.Hints(err)).To(Equal([]string{hint, "or remove it: lola uninstall review"}))

				_, err = e.Install(ctx, sc, cwd, []engine.InstallRequest{{Arg: arg}}, engine.Options{})
				Expect(err).NotTo(HaveOccurred())
				Expect(loadManifest().Find("review").Source).To(Equal("mods/review"), "a committed project.yml stays portable")
				Expect(covered("review")).To(BeTrue())
			},
			Entry("at the project root", "proj", "./mods/review"),
			Entry("below it", "proj/sub", "../mods/review"),
			Entry("above it", ".", "./proj/mods/review"),
		)

		It("hints a recorded source that no longer resolves the same way", func() {
			_, err := e.Install(ctx, sc, filepath.Join(proj, "sub"), []engine.InstallRequest{{Arg: "gone"}}, engine.Options{})
			Expect(err).To(MatchError("gone: project.yml changed for it since you last trusted it"))
			Expect(errs.Hints(err)).To(Equal([]string{"if you trust it, install it from its source: lola install ../mods/gone"}))
		})
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
	})
})
