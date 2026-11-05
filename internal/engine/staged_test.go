package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("an install's staged run", func() {
	var (
		tmp, proj, src, other string
		p                     paths.Paths
		e                     *engine.Engine
		sc                    engine.Scope
		scopes                []engine.ScopeChoice
		ctx                   = context.Background()
		claude                = engine.Options{Targets: []string{"claude-code"}}
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj = filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		e.Getwd = func() (string, error) { return proj, nil }
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
		scopes = []engine.ScopeChoice{{Scope: sc, Source: engine.SourceFlag}}
		src, other = filepath.Join(tmp, "src", "example"), filepath.Join(tmp, "src", "zeta")
		writeTree(src, exampleModule)
		writeTree(src, map[string]string{"lola.yml": "name: example\n"})
		writeTree(other, map[string]string{"lola.yml": "name: zeta\n", "commands/z.md": "Z\n"})
	})
	stage := func() *engine.StagedRun {
		GinkgoHelper()
		run, err := e.StageInstall()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(run.Close)
		return run
	}
	listed := func(run *engine.StagedRun, args ...string) *engine.Choices {
		GinkgoHelper()
		req := engine.ChoicesRequest{Cwd: proj, Scopes: scopes, Run: run}
		for _, a := range args {
			req.Modules = append(req.Modules, engine.InstallRequest{Arg: a})
		}
		ch, err := e.InstallChoices(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		return ch
	}
	cached := func() []string {
		GinkgoHelper()
		names, err := e.Store.Names()
		Expect(err).NotTo(HaveOccurred())
		return names
	}
	// runDirs lists the run directories under the cache's staging directory.
	runDirs := func() []string {
		GinkgoHelper()
		es, err := os.ReadDir(p.Staging)
		if os.IsNotExist(err) {
			return nil
		}
		Expect(err).NotTo(HaveOccurred())
		var out []string
		for _, d := range es {
			if d.IsDir() && strings.HasPrefix(d.Name(), "run-") {
				out = append(out, d.Name())
			}
		}
		return out
	}
	install := func(run *engine.StagedRun, opts engine.Options, reqs ...engine.InstallRequest) (*engine.InstallResult, error) {
		opts.Targets = claude.Targets
		return e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Modules: reqs, Options: opts, Staged: run,
			Scopes: []engine.ScopeInstall{{ScopeChoice: scopes[0], Targets: claude.Targets}}})
	}
	read := func(rel string) string {
		GinkgoHelper()
		b, err := os.ReadFile(filepath.Join(proj, rel))
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}
	hooked := func() {
		GinkgoHelper()
		writeTree(src, map[string]string{"lola.yml": "name: example\nhooks: {pre-install: hooks/pre.sh}\n", "hooks/pre.sh": "#!/bin/sh\n"})
		Expect(os.Chmod(filepath.Join(src, "hooks", "pre.sh"), 0o755)).To(Succeed())
	}

	It("lists a source a module argument fetched into the run, as the cache will record it, and the cache stays as it was", func() {
		ch := listed(stage(), src)
		Expect(ch.Versions).To(HaveLen(1))
		m := ch.Module("example")
		Expect(m).NotTo(BeNil())
		Expect(m.Source).To(Equal(src), "what the cache will record, read from the run")
		Expect(m.Type).To(Equal("folder"))
		Expect(m.ID).To(Equal(ch.Versions[0].ID))
		Expect(cached()).To(BeEmpty())
	})

	It("lists the run's version again, pinned, without fetching the source again", func() {
		run := stage()
		ch := listed(run, src)
		writeTree(src, map[string]string{"commands/new.md": "New\n"})
		again, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: scopes, Run: run,
			Modules: []engine.InstallRequest{{Arg: src, Version: &ch.Versions[0]}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(again.Versions).To(Equal(ch.Versions))
		Expect(again.Module("example").Components).NotTo(ContainElement(HaveField("Component.Name", "new")))
		Expect(cached()).To(BeEmpty())
	})

	It("refuses a pin to a version neither the run nor the cache holds", func() {
		gone := engine.VersionRef{Name: "example", ID: "0123456789ab"}
		_, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: scopes, Run: stage(),
			Modules: []engine.InstallRequest{{Arg: src, Version: &gone}}})
		Expect(err).To(MatchError(ContainSubstring("was removed from the cache since it was listed")))
	})

	It("Close drops what the run fetched, with its directory, and closing again does nothing", func() {
		run, err := e.StageInstall()
		Expect(err).NotTo(HaveOccurred())
		listed(run, src)
		Expect(runDirs()).To(HaveLen(1), "the pickers fetch into the install's one run")
		Expect(run.Close()).To(Succeed())
		Expect(runDirs()).To(BeEmpty())
		Expect(cached()).To(BeEmpty())
		Expect(run.Close()).To(Succeed())
	})

	It("lists a module the cache holds as it is: nothing is fetched", func() {
		_, err := e.Fetch(ctx, sourceSpec(src))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.RemoveAll(src)).To(Succeed())
		Expect(listed(stage(), "example").Module("example")).NotTo(BeNil())
		Expect(cached()).To(Equal([]string{"example"}))
	})

	It("without a run, the pickers cache what they fetch at once, as lola migrate's planning needs", func() {
		_, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: scopes, Modules: []engine.InstallRequest{{Arg: src}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(cached()).To(Equal([]string{"example"}))
		Expect(runDirs()).To(BeEmpty(), "the call's own run is removed when it returns")
	})

	It("lola cache add and lola info of a source still cache what they fetch", func() {
		_, err := e.Fetch(ctx, sourceSpec(other))
		Expect(err).NotTo(HaveOccurred())
		_, err = e.Info(ctx, engine.InfoRequest{Cwd: proj, Arg: src, Scope: engine.ScopeRequest{Project: true, Cwd: proj}, Targets: claude.Targets,
			Rerun: func(...string) string { return "" }, Install: func(string, string, string, engine.TrustKinds) string { return "" }})
		Expect(err).NotTo(HaveOccurred())
		Expect(cached()).To(ConsistOf("example", "zeta"))
	})

	It("caches the version the pickers listed only when the install applies, and installs its bytes though the source changed", func() {
		run := stage()
		ch := listed(run, src)
		pin := engine.InstallRequest{Arg: src, Version: &ch.Versions[0]}
		writeTree(src, map[string]string{"commands/ship.md": "Ship later\n"})
		_, err := install(run, engine.Options{DryRun: true}, pin)
		Expect(err).NotTo(HaveOccurred())
		Expect(cached()).To(BeEmpty(), "the review's dry run caches nothing")

		res, err := install(run, engine.Options{}, pin)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Modules[0].Digest).To(Equal(ch.Module("example").Digest))
		Expect(read(".claude/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"), "the bytes reviewed")
		Expect(cached()).To(Equal([]string{"example"}))
		head, ok, err := e.Store.Head("example", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(head.ID).To(Equal(ch.Versions[0].ID))
		man, err := e.Manifest(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(man.Find("example").Digest).To(BeEmpty(), "a folder is never pinned, though the cache did not hold it when the install was planned")
	})

	It("an install given no pickers' versions fetches into the run and caches what it applies", func() {
		_, err := install(stage(), engine.Options{}, engine.InstallRequest{Arg: src})
		Expect(err).NotTo(HaveOccurred())
		Expect(cached()).To(Equal([]string{"example"}))
		Expect(read(".claude/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"))
	})

	DescribeTable("an install that does not apply leaves the cache as it was, and names no cached retry",
		func(setup func() engine.Options, check func(error)) {
			run := stage()
			_, err := install(run, setup(), engine.InstallRequest{Arg: src})
			check(err)
			if err != nil {
				Expect(errs.Hints(err)).NotTo(ContainElement(HavePrefix("fetched modules stay in your cache")))
			}
			Expect(cached()).To(BeEmpty())
			Expect(filepath.Join(proj, ".lola", "project.yml")).NotTo(BeAnExistingFile())
			Expect(run.Close()).To(Succeed())
			Expect(runDirs()).To(BeEmpty(), "closing removes the staging")
		},
		Entry("a dry run", func() engine.Options { return engine.Options{DryRun: true} },
			func(err error) { Expect(err).NotTo(HaveOccurred()) }),
		Entry("a conflict", func() engine.Options {
			writeTree(proj, map[string]string{".claude/commands/ship.md": "mine"})
			return engine.Options{}
		}, func(err error) { Expect(err).To(MatchError(ContainSubstring("ship.md"))) }),
		Entry("a cancelled hook question", func() engine.Options {
			hooked()
			return engine.Options{Ask: func(string, []engine.HookScript) (bool, error) { return false, errors.New("cancelled") }}
		}, func(err error) { Expect(err).To(MatchError("cancelled")) }),
		Entry("a declined receipt", func() engine.Options {
			hooked()
			return engine.Options{Trust: engine.TrustHooks, Receipt: func([]engine.Consent) (bool, error) { return false, nil }}
		}, func(err error) { Expect(err).To(MatchError(engine.ErrDeclined)) }),
	)

	It("caches only what the install writes: a module the pickers fetched and the run dropped never reaches the cache", func() {
		run := stage()
		ch := listed(run, src, other)
		Expect(ch.Versions).To(HaveLen(2))
		_, err := install(run, engine.Options{}, engine.InstallRequest{Arg: src, Version: &ch.Versions[0]})
		Expect(err).NotTo(HaveOccurred())
		Expect(cached()).To(Equal([]string{"example"}))
	})

	It("pins a git source the run fetched at the commit it fetched, as when the cache held it", func() {
		work, srv := filepath.Join(tmp, "work"), filepath.Join(tmp, "srv")
		writeTree(work, exampleModule)
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "init")
		gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(srv, "example.git"))
		url, commit := testenv.ServeGit(srv)+"/example.git", gitIn(work, "rev-parse", "HEAD")
		run := stage()
		ch := listed(run, url)
		_, err := install(run, engine.Options{}, engine.InstallRequest{Arg: url, Version: &ch.Versions[0]})
		Expect(err).NotTo(HaveOccurred())
		man, err := e.Manifest(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(man.Find("example")).To(And(HaveField("Commit", commit), HaveField("Digest", HavePrefix("sha256:"))))
		head, ok, err := e.Store.Head("example", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(head.Commit).To(Equal(commit))
	})

	It("lists the default branch a name staged again, pinned, when the cache holds a head only for another ref", func() {
		work, srv := filepath.Join(tmp, "work"), filepath.Join(tmp, "srv")
		writeTree(work, exampleModule)
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "v1")
		gitIn(work, "tag", "v1")
		writeTree(work, map[string]string{"commands/later.md": "Later\n"})
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "later")
		gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(srv, "example.git"))
		spec := sourceSpecGit(testenv.ServeGit(srv) + "/example.git")
		spec.Ref = "v1"
		v1, err := e.Fetch(ctx, spec)
		Expect(err).NotTo(HaveOccurred())
		run := stage()
		ch := listed(run, "example")
		Expect(ch.Versions).To(HaveLen(1))
		Expect(ch.Versions[0].ID).NotTo(Equal(v1.ID), "the default branch, staged in the run")
		again, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: scopes, Run: run,
			Modules: []engine.InstallRequest{{Arg: "example", Version: &ch.Versions[0]}}})
		Expect(err).NotTo(HaveOccurred(), "Change scope asks again with the version listed")
		Expect(again.Versions).To(Equal(ch.Versions))
	})

	It("installs a module the cache holds by name from the cache, fetching nothing", func() {
		_, err := e.Fetch(ctx, sourceSpec(src))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.RemoveAll(src)).To(Succeed())
		_, err = install(stage(), engine.Options{}, engine.InstallRequest{Arg: "example"})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".claude/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"))
	})

	It("a failure after the apply cached the version says it stays in your cache, with the retry", func() {
		Expect(os.MkdirAll(p.TrustLock, 0o700)).To(Succeed())
		_, err := install(stage(), engine.Options{}, engine.InstallRequest{Arg: src})
		Expect(err).To(MatchError(ContainSubstring("lola could not record")))
		Expect(errs.Hints(err)).To(ContainElement("fetched modules stay in your cache; retry with: lola install --project example"))
		Expect(cached()).To(Equal([]string{"example"}))
	})

	It("applies, and keeps another source of the name another run cached meanwhile as an origin of its own", func() {
		hooked()
		imposter := filepath.Join(tmp, "elsewhere", "example")
		writeTree(imposter, map[string]string{"lola.yml": "name: example\n", "commands/x.md": "X\n"})
		ask := func(string, []engine.HookScript) (bool, error) {
			_, err := e.Fetch(ctx, sourceSpec(imposter)) // another lola, while this one waits at its question
			return true, err
		}
		_, err := install(stage(), engine.Options{Ask: ask}, engine.InstallRequest{Arg: src})
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(proj, ".claude", "commands", "ship.md")).To(BeARegularFile())
		origins, err := e.Store.Origins("example")
		Expect(err).NotTo(HaveOccurred())
		Expect(origins).To(HaveLen(2))
	})
})
