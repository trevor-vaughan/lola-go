package engine_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// stripFetched rewrites the cache's record of name as a lola from before fetch stamps wrote it.
func stripFetched(storeDir, name string) {
	GinkgoHelper()
	index := filepath.Join(storeDir, name, "index.yml")
	b, err := os.ReadFile(index)
	Expect(err).NotTo(HaveOccurred())
	Expect(os.WriteFile(index, []byte(regexp.MustCompile(`(?m)^\s*fetched: .*\n`).ReplaceAllString(string(b), "")), 0o644)).To(Succeed())
}

// writeConfig writes the user's config.yaml.
func writeConfig(p paths.Paths, body string) {
	GinkgoHelper()
	Expect(os.MkdirAll(filepath.Dir(p.Config), 0o755)).To(Succeed())
	Expect(os.WriteFile(p.Config, []byte(body), 0o600)).To(Succeed())
}

var _ = Describe("Sync of what is installed (lola update)", func() {
	var (
		tmp, proj, src, work, bare, url string
		bare2, otherURL                 string
		p                               paths.Paths
		e                               *engine.Engine
		sc                              engine.Scope
		requests                        atomic.Int32
		clock                           time.Time
		ctx                             = context.Background()
		claude                          = engine.Options{Targets: []string{"claude-code"}}
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		clock = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
		e.Now = func() time.Time { return clock }
		proj = filepath.Join(tmp, "proj")
		e.Getwd = func() (string, error) { return proj, nil }
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
		src = filepath.Join(tmp, "src", "other")
		writeTree(src, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "Other\n"})
		// A git repository holding exampleModule on main, served over loopback; requests counts
		// what the server receives, which is how a spec sees that nothing was fetched.
		work, bare = filepath.Join(tmp, "work"), filepath.Join(tmp, "srv", "example.git")
		writeTree(work, exampleModule)
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "init")
		gitIn(tmp, "clone", "-q", "--bare", work, bare)
		// A second served repository, holding the module other on main.
		work2 := filepath.Join(tmp, "work2")
		bare2 = filepath.Join(tmp, "srv", "other.git")
		writeTree(work2, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "Other\n"})
		gitIn(work2, "init", "-q", "-b", "main")
		gitIn(work2, "add", "-A")
		gitIn(work2, "commit", "-q", "-m", "init")
		gitIn(tmp, "clone", "-q", "--bare", work2, bare2)
		requests.Store(0)
		served := testenv.ServeGitWith(filepath.Join(tmp, "srv"), func(*http.Request) { requests.Add(1) })
		url, otherURL = served+"/example.git", served+"/other.git"
	})
	push := func(files map[string]string) string {
		writeTree(work, files)
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "change")
		gitIn(work, "push", "-q", "-f", "--all", bare)
		return gitIn(work, "rev-parse", "HEAD")
	}
	install := func(sc engine.Scope, args ...engine.InstallRequest) {
		GinkgoHelper()
		_, err := e.Install(ctx, sc, proj, args, claude)
		Expect(err).NotTo(HaveOccurred())
	}
	sync := func(req engine.SyncRequest) (*engine.SyncResult, error) {
		if req.Scope == (engine.Scope{}) {
			req.Scope = sc
		}
		return e.Sync(ctx, req)
	}
	update := func(req engine.SyncRequest) (*engine.SyncResult, error) {
		req.Installed, req.Update = true, true
		req.Options.Trust = engine.TrustAll // trust is covered by its own spec below
		return sync(req)
	}
	read := func(rel string) string {
		GinkgoHelper()
		b, err := os.ReadFile(filepath.Join(proj, rel))
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}
	lockFile := func() *ledger.Ledger {
		GinkgoHelper()
		l, err := ledger.Load(filepath.Join(proj, ".lola", "lock.json"))
		Expect(err).NotTo(HaveOccurred())
		return l
	}
	loadManifest := func() *manifest.Manifest {
		GinkgoHelper()
		m, err := e.Manifest(sc)
		Expect(err).NotTo(HaveOccurred())
		return m
	}
	// state is the bytes of the project's state, the trust record and the cache.
	state := func() map[string]string {
		GinkgoHelper()
		out := map[string]string{}
		for _, dir := range []string{proj, filepath.Dir(p.Store), filepath.Dir(p.Trust)} {
			Expect(filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() || strings.Contains(path, string(filepath.Separator)+"locks"+string(filepath.Separator)) {
					return err
				}
				if d.Type()&os.ModeSymlink != 0 {
					target, err := os.Readlink(path)
					out[path] = "link to " + target
					return err
				}
				b, err := os.ReadFile(path)
				out[path] = string(b)
				return err
			})).To(Succeed())
		}
		return out
	}
	// pull changes project.yml the way a git pull would: behind lola's back.
	pull := func(c manifest.Change) {
		GinkgoHelper()
		dst, data, err := manifest.Edit(filepath.Join(proj, ".lola", "project.yml"), c)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(dst, data, 0o644)).To(Succeed())
	}

	It("serves a fresh head from the cache without contacting the source", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		push(map[string]string{"commands/ship.md": "Ship v2\n"})
		requests.Store(0)
		clock = clock.Add(23 * time.Hour)
		res, err := update(engine.SyncRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(requests.Load()).To(BeZero())
		Expect(res.Pins).To(BeEmpty())
	})

	It("fetches a stale head and moves the module to it", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		commit := push(map[string]string{"commands/ship.md": "Ship v2\n"})
		clock = clock.Add(25 * time.Hour)
		res, err := update(engine.SyncRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Pins).To(ConsistOf(HaveField("Commit", commit)))
		Expect(read(".claude/commands/ship.md")).To(Equal("Ship v2\n"))
		Expect(loadManifest().Find("example")).To(HaveField("Commit", commit))
	})

	It("moves a module to a head another run fetched while it was fresh", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		commit := push(map[string]string{"commands/ship.md": "Ship v2\n"})
		other := filepath.Join(tmp, "teammate")
		Expect(os.MkdirAll(other, 0o755)).To(Succeed())
		_, err := e.Install(ctx, engine.Scope{Kind: target.Project, ProjectDir: other}, other,
			[]engine.InstallRequest{{Arg: url, Ref: "main"}}, claude) // moves the cache head for main
		Expect(err).NotTo(HaveOccurred())
		requests.Store(0)
		res, err := update(engine.SyncRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(requests.Load()).To(BeZero())
		Expect(res.Pins).To(ConsistOf(And(HaveField("Commit", commit), HaveField("Reason", engine.PinUpdate))))
		Expect(read(".claude/commands/ship.md")).To(Equal("Ship v2\n"))
	})

	It("asks nothing, off a terminal and without trust, when every fresh head is the version installed", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		push(map[string]string{"commands/ship.md": "Ship v2\n"})
		requests.Store(0)
		for _, names := range [][]string{nil, {"example"}} {
			res, err := sync(engine.SyncRequest{Installed: true, Update: true, Modules: names})
			Expect(err).NotTo(HaveOccurred(), "nothing moves, so nothing needs trust")
			Expect(res.InSync).To(BeTrue())
			Expect(res.Pins).To(BeEmpty())
		}
		Expect(requests.Load()).To(BeZero())
	})

	It("asks about a fresh head that moved, naming the known move, without fetching", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		push(map[string]string{"commands/ship.md": "Ship v2\n"})
		other := filepath.Join(tmp, "teammate")
		Expect(os.MkdirAll(other, 0o755)).To(Succeed())
		_, err := e.Install(ctx, engine.Scope{Kind: target.Project, ProjectDir: other}, other,
			[]engine.InstallRequest{{Arg: url, Ref: "main"}}, claude) // moves the cache head for main
		Expect(err).NotTo(HaveOccurred())
		requests.Store(0)
		before := state()
		_, err = sync(engine.SyncRequest{Installed: true, Update: true})
		refusal, refused := errors.AsType[*engine.TrustRefusalError](err)
		Expect(refused).To(BeTrue(), "the move needs trust")
		Expect(refusal.Report.Changes).To(ConsistOf(HaveField("Reason", "re-pin from main")), "the head is known: nothing is left to fetch")
		Expect(requests.Load()).To(BeZero())
		Expect(state()).To(Equal(before))
	})

	It("asks before fetching a stale head, as sync --update does, even one that will not move", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		clock = clock.Add(25 * time.Hour)
		requests.Store(0)
		_, err := sync(engine.SyncRequest{Installed: true, Update: true})
		refusal, refused := errors.AsType[*engine.TrustRefusalError](err)
		Expect(refused).To(BeTrue())
		Expect(refusal.Report.Changes).To(ConsistOf(HaveField("Reason", "re-pin from main (not fetched yet)")))
		Expect(requests.Load()).To(BeZero(), "refused before any fetch")
	})

	It("fetches a fresh head with Refetch", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		commit := push(map[string]string{"commands/ship.md": "Ship v2\n"})
		requests.Store(0)
		res, err := update(engine.SyncRequest{Refetch: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(requests.Load()).NotTo(BeZero())
		Expect(res.Pins).To(ConsistOf(HaveField("Commit", commit)))
	})

	It("treats a head with no stamp as stale", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		stripFetched(p.Store, "example")
		requests.Store(0)
		_, err := update(engine.SyncRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(requests.Load()).NotTo(BeZero())
	})

	It("treats a stamp in the future as fresh", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		clock = clock.Add(-72 * time.Hour)
		requests.Store(0)
		_, err := update(engine.SyncRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(requests.Load()).To(BeZero())
	})

	It("fetches an entry whose cached local source is gone, however fresh its head, and replaces it in the cache", func() {
		archive := tarGz("kit", map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Kit\n"})
		oldKit, newKit := filepath.Join(tmp, "old", "kit.tar.gz"), filepath.Join(proj, "mods", "kit.tar.gz")
		for _, path := range []string{oldKit, newKit} {
			Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
			Expect(os.WriteFile(path, archive, 0o644)).To(Succeed())
		}
		install(sc, engine.InstallRequest{Arg: oldKit})
		Expect(os.RemoveAll(filepath.Dir(oldKit))).To(Succeed()) // the archive moves
		ent := *loadManifest().Find("kit")
		ent.Source = newKit
		pull(manifest.Change{Set: []manifest.Entry{ent}})
		res, err := update(engine.SyncRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(HaveField("Text", ContainSubstring("which no longer exists; caching it from "+newKit))))
		o, found, err := e.Store.Origin("kit")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(o.Source).To(Equal(newKit), "the cache now records the source the run read")
	})

	It("fetches on every run with cache_fresh_for: never", func() {
		writeConfig(p, "version: 1\ncache_fresh_for: never\n")
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		requests.Store(0)
		_, err := update(engine.SyncRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(requests.Load()).NotTo(BeZero())
	})

	It("keeps a head fresh for the window cache_fresh_for sets", func() {
		writeConfig(p, "version: 1\ncache_fresh_for: 1w\n")
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		requests.Store(0)
		clock = clock.Add(6 * 24 * time.Hour)
		_, err := update(engine.SyncRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(requests.Load()).To(BeZero())
		clock = clock.Add(24 * time.Hour)
		_, err = update(engine.SyncRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(requests.Load()).NotTo(BeZero())
	})

	It("never installs an entry that is listed but not installed", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		pull(manifest.Change{Set: []manifest.Entry{{Name: "other", Source: src}}})
		res, err := update(engine.SyncRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(lockFile().Modules).NotTo(HaveKey("other"))
		Expect(res.Removals).To(BeEmpty())
	})

	It("names the entries a layer lists that are not installed, with the sync that installs them", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"}, engine.InstallRequest{Arg: otherURL, Ref: "main"})
		pull(manifest.Change{Remove: []string{"other"}}) // installed, no longer listed: a finding, not this notice
		pull(manifest.Change{Set: []manifest.Entry{{Name: "kit", Source: src}, {Name: "zeta", Source: src}}})
		res, err := sync(engine.SyncRequest{Installed: true, Update: true, Display: []string{"--json"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
			Text: "2 modules are listed but not installed: kit, zeta → lola sync --project kit zeta --json"}))
		Expect(res.Findings).To(ContainElement(And(HaveField("Code", engine.CodeNotInManifest), HaveField("Module", "other"))))
		for _, n := range res.Notices {
			Expect(n.Text).NotTo(ContainSubstring("other"), "an installed module no layer lists has its finding")
		}
		res, err = sync(engine.SyncRequest{Installed: true, Update: true, Modules: []string{"example"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(HaveField("Text", "2 modules are listed but not installed: kit, zeta → lola sync --project kit zeta")))
	})

	It("says nothing of entries not installed when every listed entry is installed", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		res, err := sync(engine.SyncRequest{Installed: true, Update: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("not installed"))))
	})

	It("notes the modules a fresh head served, with the run that fetches them now", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"}, engine.InstallRequest{Arg: otherURL, Ref: "main"})
		res, err := sync(engine.SyncRequest{Installed: true, Update: true, Display: []string{"--json"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo,
			Text: "2 modules served from your cache (fetched within cache_fresh_for) → lola update --project --fetch --json"}))
		res, err = sync(engine.SyncRequest{Installed: true, Update: true, Modules: []string{"example"}, Options: engine.Options{DryRun: true}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(HaveField("Text",
			"1 module served from your cache (fetched within cache_fresh_for) → lola update --project example --fetch -n")))
	})

	It("counts a fresh head that moves the module among those the cache served", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		push(map[string]string{"commands/ship.md": "Ship v2\n"})
		other := filepath.Join(tmp, "teammate")
		Expect(os.MkdirAll(other, 0o755)).To(Succeed())
		_, err := e.Install(ctx, engine.Scope{Kind: target.Project, ProjectDir: other}, other,
			[]engine.InstallRequest{{Arg: url, Ref: "main"}}, claude) // moves the cache head for main
		Expect(err).NotTo(HaveOccurred())
		res, err := update(engine.SyncRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(HaveField("Text",
			"1 module served from your cache (fetched within cache_fresh_for) → lola update --project --fetch --trust=entries,profiles,scripts,hooks,runtime-hooks")))
	})

	It("notes no cache-served module under Refetch, or when every head was fetched", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		for _, req := range []engine.SyncRequest{{Refetch: true}, {}} {
			if !req.Refetch {
				clock = clock.Add(25 * time.Hour)
			}
			res, err := update(req)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("served from your cache"))))
		}
	})

	It("refuses a folder module edited since its install off a terminal without trust, and renders it with trust", func() {
		install(sc, engine.InstallRequest{Arg: src})
		writeTree(src, map[string]string{"commands/other.md": "Other v2\n"})
		before := state()
		_, err := sync(engine.SyncRequest{Installed: true, Update: true})
		refusal, refused := errors.AsType[*engine.TrustRefusalError](err)
		Expect(refused).To(BeTrue(), "the folder's new content needs trust")
		Expect(refusal.Report.Changes).To(ConsistOf(And(HaveField("Module", "other"), HaveField("Reason", "content changed since lola installed it"))))
		Expect(state()).To(Equal(before))
		_, err = update(engine.SyncRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(".claude/commands/other.md")).To(Equal("Other v2\n"))
	})

	It("refuses a named module that is not installed, hinting sync", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		pull(manifest.Change{Set: []manifest.Entry{{Name: "other", Source: src}}})
		before := state()
		_, err := update(engine.SyncRequest{Modules: []string{"other"}})
		Expect(err).To(MatchError("other is not installed here"))
		Expect(errs.Hints(err)).To(Equal([]string{"install it as project.yml lists it: lola sync --project other",
			"install it: lola install --project other"}))
		Expect(state()).To(Equal(before))
	})

	It("refuses a named module that is neither installed nor listed, hinting install", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		_, err := update(engine.SyncRequest{Modules: []string{"nope"}})
		Expect(err).To(MatchError("nope is not installed here"))
		Expect(errs.Hints(err)).To(Equal([]string{"install it: lola install --project nope"}))
	})

	It("updates only the installed modules it names", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"}, engine.InstallRequest{Arg: src})
		push(map[string]string{"commands/ship.md": "Ship v2\n"})
		clock = clock.Add(25 * time.Hour)
		requests.Store(0)
		res, err := update(engine.SyncRequest{Modules: []string{"other"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(requests.Load()).To(BeZero(), "example is not in the run")
		Expect(res.Pins).To(BeEmpty())
		Expect(read(".claude/commands/ship.md")).NotTo(Equal("Ship v2\n"))
	})

	It("leaves an installed module no layer lists as installed, with a finding, and removes nothing", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		pull(manifest.Change{Remove: []string{"example"}})
		res, err := update(engine.SyncRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Removals).To(BeEmpty())
		Expect(lockFile().Modules).To(HaveKey("example"))
		Expect(read(".claude/commands/ship.md")).NotTo(BeEmpty())
		Expect(res.Findings).To(ContainElement(And(HaveField("Code", engine.CodeNotInManifest), HaveField("Module", "example"),
			HaveField("Hint", ContainSubstring("lola sync")))))
	})

	It("leaves a module whose entry asks for other targets as installed, with a finding", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		ent := *loadManifest().Find("example")
		ent.Targets = []string{"gemini-cli"}
		pull(manifest.Change{Set: []manifest.Entry{ent}})
		res, err := update(engine.SyncRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Findings).To(ContainElement(And(HaveField("Code", engine.CodeTargetsDiffer), HaveField("Module", "example"),
			HaveField("Hint", ContainSubstring("lola sync --project example")))))
		Expect(lockFile().Modules["example"].Targets).To(Equal([]string{"claude-code"}))
	})

	It("is not refused when the scope has no manifest", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		Expect(os.Remove(filepath.Join(proj, ".lola", "project.yml"))).To(Succeed())
		res, err := update(engine.SyncRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Findings).To(ContainElement(HaveField("Code", engine.CodeNotInManifest)))
	})

	It("is a clean no-op where nothing is installed", func() {
		res, err := update(engine.SyncRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.InSync).To(BeTrue())
		Expect(res.Findings).To(BeEmpty())
	})

	It("asks to trust a moved pin at project scope, and --force does not answer it", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		push(map[string]string{"commands/ship.md": "Ship v2\n"})
		clock = clock.Add(25 * time.Hour)
		before := state()
		_, err := sync(engine.SyncRequest{Installed: true, Update: true, Options: engine.Options{Force: true}})
		_, refused := errors.AsType[*engine.TrustRefusalError](err)
		Expect(refused).To(BeTrue(), "no terminal and no --trust: the moved pin is refused, --force does not answer it")
		Expect(state()).To(Equal(before))
	})

	It("writes nothing in a project dry run", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		push(map[string]string{"commands/ship.md": "Ship v2\n"})
		clock = clock.Add(25 * time.Hour)
		before := state()
		res, err := update(engine.SyncRequest{Options: engine.Options{DryRun: true}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Pins).To(HaveLen(1))
		Expect(state()).To(Equal(before))
	})

	It("names lola update, with --fetch as given, in the hints that repeat the run", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		push(map[string]string{"commands/ship.md": "Ship v2\n"})
		res, err := sync(engine.SyncRequest{Installed: true, Update: true, Refetch: true, Modules: []string{"example"}, Options: engine.Options{DryRun: true}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.ToApply).To(Equal("lola update --project example --fetch"))
		Expect(res.OffTerminal).To(Equal("lola update --project example --fetch --trust=entries"))
		Expect(res.Installed).To(BeTrue())
		Expect(res.Trust.Changes).To(ConsistOf(HaveField("Reason", "re-pin from main (--fetch)")), "lola update has no --update")
		_, err = sync(engine.SyncRequest{Installed: true, Update: true, Refetch: true, Modules: []string{"example"}})
		refusal, refused := errors.AsType[*engine.TrustRefusalError](err)
		Expect(refused).To(BeTrue())
		Expect(refusal.Report.Changes).To(ConsistOf(HaveField("Reason", "re-pin from main (--fetch; not fetched yet)")))
		clock = clock.Add(25 * time.Hour) // stale: a fresh head the version installed asks nothing (ruling R7)
		_, err = sync(engine.SyncRequest{Installed: true, Update: true, Modules: []string{"example"}})
		refusal, refused = errors.AsType[*engine.TrustRefusalError](err)
		Expect(refused).To(BeTrue())
		Expect(refusal.Report.Changes).To(ConsistOf(HaveField("Reason", "re-pin from main (not fetched yet)")))
		Expect(err).To(MatchError(HavePrefix("lola update needs your trust")))
		Expect(errs.Hints(err)).To(ContainElement("see them: lola update --project example -n"))
		Expect(strings.Join(errs.Hints(err), "\n")).NotTo(ContainSubstring("lola sync"))
	})

	It("keeps a module whose fetch fails as installed and updates the others, failing the run", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"}, engine.InstallRequest{Arg: otherURL, Ref: "main"})
		otherDigestBefore := lockFile().Modules["other"].Digest
		push(map[string]string{"commands/ship.md": "Ship v2\n"})
		Expect(os.RemoveAll(bare2)).To(Succeed()) // its source is gone
		clock = clock.Add(25 * time.Hour)
		res, err := update(engine.SyncRequest{})
		failed, ok := errors.AsType[*engine.FetchesFailedError](err)
		Expect(ok).To(BeTrue())
		Expect(failed.Modules).To(Equal([]string{"other"}))
		Expect(res).NotTo(BeNil(), "the result of what was applied comes with the error")
		Expect(res.Failed).To(Equal([]string{"other"}))
		Expect(res.Findings).To(ContainElement(And(HaveField("Code", engine.CodeFetchFailed), HaveField("Module", "other"),
			HaveField("Hint", ContainSubstring("lola update --project other --fetch")))))
		Expect(read(".claude/commands/ship.md")).To(Equal("Ship v2\n"))
		Expect(read(".claude/commands/other.md")).To(Equal("Other\n"))
		Expect(lockFile().Modules["other"].Digest).To(Equal(otherDigestBefore))
	})

	It("keeps the fetch's own hints in its finding, before the retry they make work", func() {
		gitIn(work, "push", "-q", bare, "main:dev")
		install(sc, engine.InstallRequest{Arg: url, Ref: "dev"})
		gitIn(bare, "branch", "-q", "-D", "dev") // the ref is gone
		clock = clock.Add(25 * time.Hour)
		res, err := update(engine.SyncRequest{Display: []string{"--json"}})
		_, ok := errors.AsType[*engine.FetchesFailedError](err)
		Expect(ok).To(BeTrue())
		Expect(res.Findings).To(ContainElement(And(HaveField("Code", engine.CodeFetchFailed), HaveField("Module", "example"),
			HaveField("Hint", "list them: git ls-remote --heads --tags "+url+"; retry: lola update --project example --fetch --json"))))
	})

	It("leaves everything as installed when every fetch fails (offline)", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		inProject := func(all map[string]string) map[string]string {
			out := map[string]string{}
			for path, body := range all {
				if strings.HasPrefix(path, proj+string(filepath.Separator)) {
					out[path] = body
				}
			}
			return out
		}
		before := inProject(state())
		Expect(os.RemoveAll(bare)).To(Succeed())
		clock = clock.Add(25 * time.Hour)
		res, err := update(engine.SyncRequest{})
		failed, ok := errors.AsType[*engine.FetchesFailedError](err)
		Expect(ok).To(BeTrue())
		Expect(failed.Modules).To(Equal([]string{"example"}))
		Expect(res.Failed).To(Equal([]string{"example"}))
		Expect(inProject(state())).To(Equal(before), "lock.json, project.yml and the rendered files are as they were")
	})

	It("leaves the one module it names as installed when its fetch fails", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		Expect(os.RemoveAll(bare)).To(Succeed())
		clock = clock.Add(25 * time.Hour)
		res, err := update(engine.SyncRequest{Modules: []string{"example"}})
		failed, ok := errors.AsType[*engine.FetchesFailedError](err)
		Expect(ok).To(BeTrue())
		Expect(failed.Modules).To(Equal([]string{"example"}))
		Expect(res.Steps).To(BeEmpty())
	})

	It("leaves a module whose fetch fails under Refetch as installed", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		Expect(os.RemoveAll(bare)).To(Succeed())
		res, err := update(engine.SyncRequest{Refetch: true})
		failed, ok := errors.AsType[*engine.FetchesFailedError](err)
		Expect(ok).To(BeTrue(), "a fresh head --fetch could not fetch")
		Expect(failed.Modules).To(Equal([]string{"example"}))
		Expect(res.Findings).To(ContainElement(HaveField("Code", engine.CodeFetchFailed)))
	})

	It("leaves a module whose fetch fails as installed in a user dry run", func() {
		user := engine.Scope{Kind: target.User}
		install(user, engine.InstallRequest{Arg: url, Ref: "main"}, engine.InstallRequest{Arg: otherURL, Ref: "main"})
		push(map[string]string{"commands/ship.md": "Ship v2\n"})
		Expect(os.RemoveAll(bare2)).To(Succeed())
		clock = clock.Add(25 * time.Hour)
		res, err := update(engine.SyncRequest{Scope: user, Options: engine.Options{DryRun: true}})
		failed, ok := errors.AsType[*engine.FetchesFailedError](err)
		Expect(ok).To(BeTrue())
		Expect(failed.Modules).To(Equal([]string{"other"}))
		Expect(res.Pins).To(BeEmpty(), "default.yml is never pinned")
		Expect(res.Changed).To(Equal([]string{"example"}), "the module fetched moves")
		Expect(res.Findings).To(ContainElement(And(HaveField("Code", engine.CodeFetchFailed), HaveField("Module", "other"),
			HaveField("Hint", ContainSubstring("lola update --user other --fetch")))))
	})

	It("still fails a sync whose fetch fails", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		Expect(os.RemoveAll(bare)).To(Succeed())
		res, err := sync(engine.SyncRequest{Update: true, Options: engine.Options{Trust: engine.TrustAll}})
		Expect(err).To(HaveOccurred())
		Expect(res).To(BeNil())
		_, isFetch := errors.AsType[*engine.FetchesFailedError](err)
		Expect(isFetch).To(BeFalse())
	})

	It("still aborts on a failure of lola's own state", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		Expect(os.WriteFile(filepath.Join(p.Store, "example", "index.yml"), []byte("{"), 0o644)).To(Succeed())
		res, err := update(engine.SyncRequest{})
		Expect(err).To(HaveOccurred())
		Expect(res).To(BeNil())
		_, isFetch := errors.AsType[*engine.FetchesFailedError](err)
		Expect(isFetch).To(BeFalse())
	})
})
