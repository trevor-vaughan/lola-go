package engine_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
	"github.com/trevor-vaughan/lola-go/internal/trust"
)

// tarGz returns a gzip tar holding files below dir/.
func tarGz(dir string, files map[string]string) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		Expect(tw.WriteHeader(&tar.Header{Name: dir + "/" + n, Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(files[n]))})).To(Succeed())
		_, err := tw.Write([]byte(files[n]))
		Expect(err).NotTo(HaveOccurred())
	}
	Expect(tw.Close()).To(Succeed())
	Expect(zw.Close()).To(Succeed())
	return buf.Bytes()
}

func gitIn(dir string, args ...string) string {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))
	return strings.TrimSpace(string(out))
}

var _ = Describe("Engine with git and archive sources", func() {
	var (
		tmp, proj string
		e         *engine.Engine
		sc        engine.Scope
		ctx       = context.Background()
		claude    = engine.Options{Targets: []string{"claude-code"}}
		// served, when set, sees every request the test git server receives, on the server's
		// goroutines; servedMu guards it and whatever it records.
		served   func(*http.Request)
		servedMu sync.Mutex
	)
	serve := func(f func(*http.Request)) {
		servedMu.Lock()
		defer servedMu.Unlock()
		served = f
	}
	BeforeEach(func() {
		tmp = testenv.Isolate()
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		e.Getwd = func() (string, error) { return proj, nil } // hints are written for a user in the project
		proj = filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
		serve(nil)
	})
	// serveRepo serves a repository holding exampleModule, tagged v1, and returns its URL and
	// the tagged commit.
	serveRepo := func() (string, string) {
		work := filepath.Join(tmp, "work")
		writeTree(work, exampleModule)
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "init")
		gitIn(work, "tag", "v1")
		gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "example.git"))
		url := testenv.ServeGitWith(filepath.Join(tmp, "srv"), func(r *http.Request) {
			servedMu.Lock()
			defer servedMu.Unlock()
			if served != nil {
				served(r)
			}
		})
		return url + "/example.git", gitIn(work, "rev-parse", "v1")
	}
	lockFile := func() *ledger.Ledger {
		l, err := ledger.Load(filepath.Join(proj, ".lola", "lock.json"))
		Expect(err).NotTo(HaveOccurred())
		return l
	}

	It("installs from git, recording type, ref and commit", func() {
		url, commit := serveRepo()
		_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "v1"}}, claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(proj, ".claude/commands/ship.md")).To(BeARegularFile())

		o, found, err := e.Store.Origin("example")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(o.Type).To(Equal("git"))
		Expect(o.Source).To(Equal(url))
		Expect(o.Heads).To(ConsistOf(HaveField("Ref", "v1")))
		v, ok, err := e.Store.Head("example", "v1")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(v.Meta).To(And(HaveField("Ref", "v1"), HaveField("Commit", commit)))

		Expect(lockFile().Modules["example"]).To(And(HaveField("Source", url), HaveField("Ref", "v1"), HaveField("Commit", commit)))
		man, err := e.Manifest(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(man.Find("example")).To(And(HaveField("Source", url), HaveField("Ref", "v1")))
	})

	It("refuses a ref longer than lock.json could load again, before anything is fetched or written", func() {
		url, _ := serveRepo()
		long := strings.Repeat("r", 300)
		_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: long}}, claude)
		Expect(err).To(MatchError(ContainSubstring("ref is longer than 256 bytes")))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("shorter")))
		Expect(os.ReadDir(proj)).To(BeEmpty())
		_, found, err := e.Store.Origin("example")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse(), "nothing reaches the cache")
	})

	It("refuses a too-long ref for a cached module named by name, before fetching", func() {
		url, _ := serveRepo()
		_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "v1"}}, claude)
		Expect(err).NotTo(HaveOccurred())
		before, err := os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "example", Ref: strings.Repeat("r", 300)}}, claude)
		Expect(err).To(MatchError(ContainSubstring("ref is longer than 256 bytes")))
		Expect(os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))).To(Equal(before))
		o, _, err := e.Store.Origin("example")
		Expect(err).NotTo(HaveOccurred())
		Expect(o.Heads).To(ConsistOf(HaveField("Ref", "v1")), "no head fetched for the refused ref")
	})

	It("never pins a commit from a cache index that is not a full SHA", func() {
		url, commit := serveRepo()
		spec, ok, err := source.Parse(url, proj)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		_, err = e.Fetch(ctx, spec)
		Expect(err).NotTo(HaveOccurred())
		index := filepath.Join(e.Paths.Store, "example", "index.yml")
		b, err := os.ReadFile(index)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(index, bytes.ReplaceAll(b, []byte(commit), []byte("nope")), 0o600)).To(Succeed())
		_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "example"}}, claude)
		Expect(err).To(MatchError(ContainSubstring(`records commit "nope"`)))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("lola cache rm example")))
		Expect(filepath.Join(proj, ".lola", "project.yml")).NotTo(BeAnExistingFile())
	})

	It("an install of a git source at a full commit SHA the cache holds for that source fetches nothing", func() {
		url, commit := serveRepo()
		var requests atomic.Int32
		serve(func(*http.Request) { requests.Add(1) })
		_, err := e.Fetch(ctx, source.Spec{Kind: source.KindGit, Location: url})
		Expect(err).NotTo(HaveOccurred())
		fetched := requests.Load()
		Expect(fetched).NotTo(BeZero())
		_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: commit}}, claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(requests.Load()).To(Equal(fetched), "the commit is cached for this source")
		Expect(lockFile().Modules["example"]).To(And(HaveField("Commit", commit), HaveField("Ref", commit)))

		for _, r := range []engine.InstallRequest{{Arg: url, Ref: "v1"}, {Arg: url, Ref: commit, Path: "."}} {
			before := requests.Load()
			_, _ = e.Install(ctx, sc, proj, []engine.InstallRequest{r}, engine.Options{Targets: claude.Targets, DryRun: true})
			Expect(requests.Load()).To(BeNumerically(">", before), "a tag, or another path, fetches: %+v", r)
		}

		o, _, err := e.Store.Origin("example")
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(e.Paths.Store, "example", "versions", o.Heads[0].Digest, "content", "commands", "ship.md"),
			[]byte("tampered\n"), 0o644)).To(Succeed())
		before := requests.Load()
		_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: commit}}, engine.Options{Targets: claude.Targets, DryRun: true})
		Expect(err).To(MatchError(ContainSubstring("does not match its digest")), "the cached version is hashed again before it is used")
		Expect(requests.Load()).To(Equal(before))
	})

	DescribeTable("an install at a full commit SHA the cache holds only for another location, path or component selection fetches",
		func(cached func(url, commit string) source.Spec) {
			url, commit := serveRepo()
			var requests atomic.Int32
			serve(func(*http.Request) { requests.Add(1) })
			_, err := e.Fetch(ctx, cached(url, commit))
			Expect(err).NotTo(HaveOccurred())
			before := requests.Load()
			_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: commit}}, engine.Options{Targets: claude.Targets, DryRun: true})
			Expect(err).To(MatchError(HavePrefix(`a different module named "example" is already in your cache`)), "fetched, then refused: the cache keeps one source per name")
			Expect(requests.Load()).To(BeNumerically(">", before))
		},
		Entry("another location of the same repository", func(_, commit string) source.Spec {
			other := testenv.ServeGit(filepath.Join(tmp, "srv")) + "/example.git"
			return source.Spec{Kind: source.KindGit, Location: other, Ref: commit}
		}),
		Entry("another --path", func(url, commit string) source.Spec {
			return source.Spec{Kind: source.KindGit, Location: url, Ref: commit, Path: "."}
		}),
		Entry("a component selection", func(url, commit string) source.Spec {
			return source.Spec{Kind: source.KindGit, Location: url, Ref: commit, Components: &source.Components{Commands: []string{"commands/ship.md"}}}
		}),
	)

	It("an install at a cached commit looks past an unreadable version and another name of the source, and takes the SHA in any case", func() {
		url, commit := serveRepo()
		var requests atomic.Int32
		serve(func(*http.Request) { requests.Add(1) })
		_, err := e.Fetch(ctx, source.Spec{Kind: source.KindGit, Location: url})
		Expect(err).NotTo(HaveOccurred())
		// "aaa" is the same source cached under another name, at no version of that commit; it sorts first.
		Expect(os.CopyFS(filepath.Join(e.Paths.Store, "aaa"), os.DirFS(filepath.Join(e.Paths.Store, "example")))).To(Succeed())
		index := filepath.Join(e.Paths.Store, "aaa", "index.yml")
		b, err := os.ReadFile(index)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(index, []byte(strings.ReplaceAll(string(b), commit, strings.Repeat("b", 40))), 0o600)).To(Succeed())
		versions, err := os.ReadDir(filepath.Join(e.Paths.Store, "aaa", "versions"))
		Expect(err).NotTo(HaveOccurred())
		for _, v := range versions {
			origin := filepath.Join(e.Paths.Store, "aaa", "versions", v.Name(), ".lola-origin")
			b, err := os.ReadFile(origin)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(origin, []byte(strings.ReplaceAll(string(b), commit, strings.Repeat("b", 40))), 0o600)).To(Succeed())
		}
		// An unreadable version record of the right name is skipped, not an error: the head records
		// another commit, so the versions' records are read, the unreadable one first.
		writeTree(filepath.Join(e.Paths.Store, "example", "versions", "000000000000"), map[string]string{".lola-origin": "{not yaml"})
		index = filepath.Join(e.Paths.Store, "example", "index.yml")
		b, err = os.ReadFile(index)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(index, []byte(strings.ReplaceAll(string(b), commit, strings.Repeat("c", 40))), 0o600)).To(Succeed())
		fetched := requests.Load()
		_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: strings.ToUpper(commit)}}, claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(requests.Load()).To(Equal(fetched))
	})

	It("records a full commit SHA typed in capitals lower-cased: in project.yml, lock.json and the cache head", func() {
		url, commit := serveRepo()
		_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: strings.ToUpper(commit)}}, claude)
		Expect(err).NotTo(HaveOccurred())
		man, err := e.Manifest(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(man.Find("example")).To(And(HaveField("Ref", commit), HaveField("Commit", commit)))
		Expect(lockFile().Modules["example"]).To(HaveField("Ref", commit))
		o, _, err := e.Store.Origin("example")
		Expect(err).NotTo(HaveOccurred())
		Expect(o.Heads).To(ConsistOf(HaveField("Ref", commit)))
	})

	It("a refused pre-install hook's hints review and consent to the version the install resolved, not the cache's head", func() {
		work := filepath.Join(tmp, "hooked")
		hook := func(text string) string { return "#!/bin/sh\necho " + text + "\n" }
		writeTree(work, map[string]string{"lola.yml": "name: kit\nhooks: {pre-install: setup.sh}\n", "setup.sh": hook("v1"), "commands/ship.md": "Ship\n"})
		Expect(os.Chmod(filepath.Join(work, "setup.sh"), 0o755)).To(Succeed())
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "v1")
		v1 := gitIn(work, "rev-parse", "HEAD")
		writeTree(work, map[string]string{"setup.sh": hook("v2")})
		gitIn(work, "commit", "-q", "-am", "v2")
		gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "kit.git"))
		url := testenv.ServeGit(filepath.Join(tmp, "srv")) + "/kit.git"
		_, err := e.Fetch(ctx, source.Spec{Kind: source.KindGit, Location: url}) // the cache's default head: v2
		Expect(err).NotTo(HaveOccurred())

		rerun := func(k engine.TrustKinds) string {
			return "lola install " + url + " --ref " + v1 + " " + k.Arg() + " --json"
		}
		_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: v1}},
			engine.Options{Targets: claude.Targets, Display: []string{"--json"}, Rerun: rerun})
		Expect(err).To(MatchError("kit has a pre-install hook, and lola runs hooks only with your consent"))
		Expect(errs.Hints(err)).To(Equal([]string{"review it: lola info --project " + url + " --ref " + v1 + " --target claude-code --json",
			"then consent to it: " + rerun(engine.TrustHooks)}))

		info, err := e.Info(ctx, engine.InfoRequest{Cwd: proj, Arg: url, Ref: v1, Scope: engine.ScopeRequest{Project: true, Cwd: proj}, Targets: claude.Targets,
			Rerun: func(...string) string { return "" }, Install: func(string, string, string, engine.TrustKinds) string { return "" }})
		Expect(err).NotTo(HaveOccurred())
		sum := sha256.Sum256([]byte(hook("v1")))
		Expect(info.Runs.InstallHooks).To(ConsistOf(HaveField("SHA256", hex.EncodeToString(sum[:]))), "the review shows the hook the install would run")
	})

	Describe("a pre-install refusal's review hint", func() {
		var url, v1, v2, d1, d2 string
		var requests atomic.Int64
		hook := func(text string) string { return "#!/bin/sh\necho " + text + "\n" }
		BeforeEach(func() {
			work := filepath.Join(tmp, "hooked")
			writeTree(work, map[string]string{"lola.yml": "name: kit\nhooks: {pre-install: setup.sh}\n", "setup.sh": hook("v1"), "commands/ship.md": "Ship\n"})
			Expect(os.Chmod(filepath.Join(work, "setup.sh"), 0o755)).To(Succeed())
			gitIn(work, "init", "-q", "-b", "main")
			gitIn(work, "add", "-A")
			gitIn(work, "commit", "-q", "-m", "v1")
			gitIn(work, "tag", "v1")
			v1 = gitIn(work, "rev-parse", "HEAD")
			writeTree(work, map[string]string{"setup.sh": hook("v2")})
			gitIn(work, "commit", "-q", "-am", "v2")
			v2 = gitIn(work, "rev-parse", "HEAD")
			gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "kit.git"))
			url = testenv.ServeGitWith(filepath.Join(tmp, "srv"), func(*http.Request) { requests.Add(1) }) + "/kit.git"
			old, err := e.Fetch(ctx, source.Spec{Kind: source.KindGit, Location: url, Ref: "v1"}) // a head for the tag v1
			Expect(err).NotTo(HaveOccurred())
			head, err := e.Fetch(ctx, source.Spec{Kind: source.KindGit, Location: url}) // the cache's default head: v2
			Expect(err).NotTo(HaveOccurred())
			d1, d2 = old.Meta.Digest, head.Meta.Digest
		})
		// pinned is project.yml listing kit at main, pinned at v1, behind the cache's head.
		pinned := func() string {
			return "version: 1\ntargets: [claude-code]\nmodules:\n  - name: kit\n    source: " + url + "\n    ref: main\n    commit: " + v1 + "\n    digest: " + d1 + "\n"
		}

		DescribeTable("names exactly the version the refused run would install, which lola info shows from the cache",
			func(run func() error, want func() (ref, digest string), tail string) {
				err := run()
				Expect(err).To(MatchError("kit has a pre-install hook, and lola runs hooks only with your consent"))
				ref, digest := want()
				Expect(errs.Hints(err)[0]).To(Equal("review it: lola info --project "+url+" --ref "+ref+tail), "an install's keeps its targets")
				before := requests.Load()
				info, err := e.Info(ctx, engine.InfoRequest{Cwd: proj, Arg: url, Ref: ref, Scope: engine.ScopeRequest{Project: true, Cwd: proj},
					Targets: claude.Targets, Rerun: func(...string) string { return "" }, Install: func(string, string, string, engine.TrustKinds) string { return "" }})
				Expect(err).NotTo(HaveOccurred())
				Expect(info.Digest).To(Equal(digest))
				Expect(requests.Load()).To(Equal(before), "the review fetches nothing the run did not")
			},
			Entry("sync of an entry pinned behind the cache's head", func() error {
				writeTree(proj, map[string]string{".lola/project.yml": pinned()})
				_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries}})
				return err
			}, func() (string, string) { return v1, d1 }, ""),
			Entry("render replacing its copy", func() error {
				writeTree(proj, map[string]string{".lola/project.yml": pinned()})
				_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries | engine.TrustHooks}})
				Expect(err).NotTo(HaveOccurred())
				Expect(os.Remove(e.Paths.Consent)).To(Succeed())
				Expect(os.RemoveAll(filepath.Join(proj, ".lola", "modules", "kit"))).To(Succeed())
				_, err = e.Render(ctx, sc, engine.Options{})
				return err
			}, func() (string, string) { return v1, d1 }, ""),
			Entry("profile --on of a project profile listing it", func() error {
				writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n",
					".lola/profiles/review.yml": strings.Replace(pinned(), "targets: [claude-code]\n", "", 1)})
				_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Names: []string{"review"}},
					Options: engine.Options{Trust: engine.TrustEntries | engine.TrustProfiles}})
				return err
			}, func() (string, string) { return v1, d1 }, ""),
			Entry("migrate's install of the cached name", func() error {
				_, err := e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Modules: []engine.InstallRequest{{Arg: "kit", Targets: claude.Targets}},
					Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc}, Targets: claude.Targets}}})
				return err
			}, func() (string, string) { return v2, d2 }, " --target claude-code"),
			Entry("install of the cached name at a tag", func() error {
				_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "kit", Ref: "v1"}}, claude)
				return err
			}, func() (string, string) { return v1, d1 }, " --target claude-code"),
		)

		It("is left out when no lola info command shows that version, and the consent hint stays", func() {
			folder := filepath.Join(tmp, "kitdir")
			writeTree(folder, map[string]string{"lola.yml": "name: fkit\nhooks: {pre-install: setup.sh}\n", "setup.sh": hook("a"), "commands/ship.md": "Ship\n"})
			Expect(os.Chmod(filepath.Join(folder, "setup.sh"), 0o755)).To(Succeed())
			a, err := e.Fetch(ctx, sourceSpec(folder))
			Expect(err).NotTo(HaveOccurred())
			writeTree(folder, map[string]string{"setup.sh": hook("b")})
			_, err = e.Fetch(ctx, sourceSpec(folder)) // the folder's head moves to b
			Expect(err).NotTo(HaveOccurred())
			opts := claude
			opts.Rerun = func(k engine.TrustKinds) string { return "lola install fkit " + k.Arg() }
			_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "fkit", Version: &engine.VersionRef{Name: "fkit", ID: a.ID}}}, opts)
			Expect(err).To(MatchError("fkit has a pre-install hook, and lola runs hooks only with your consent"))
			Expect(errs.Hints(err)).To(Equal([]string{"then consent to it: lola install fkit --trust=hooks"}))
		})

		It("without a run to repeat, a listed version no ref names gets no consent hint: installing the name again installs another", func() {
			folder := filepath.Join(tmp, "kitdir")
			writeTree(folder, map[string]string{"lola.yml": "name: fkit\nhooks: {pre-install: setup.sh}\n", "setup.sh": hook("a"), "commands/ship.md": "Ship\n"})
			Expect(os.Chmod(filepath.Join(folder, "setup.sh"), 0o755)).To(Succeed())
			a, err := e.Fetch(ctx, sourceSpec(folder))
			Expect(err).NotTo(HaveOccurred())
			writeTree(folder, map[string]string{"setup.sh": hook("b")})
			_, err = e.Fetch(ctx, sourceSpec(folder))
			Expect(err).NotTo(HaveOccurred())
			_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "fkit", Version: &engine.VersionRef{Name: "fkit", ID: a.ID}}}, claude)
			Expect(err).To(MatchError("fkit has a pre-install hook, and lola runs hooks only with your consent"))
			Expect(errs.Hints(err)).To(BeEmpty())
		})

		It("a refusal with no run to repeat still hints the run that consents", func() {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "v1"}}, claude)
			Expect(err).To(HaveOccurred())
			Expect(errs.Hints(err)).To(Equal([]string{"review it: lola info --project " + url + " --ref " + v1 + " --target claude-code",
				"then consent to it: lola install --project " + url + " --ref v1 --target claude-code --trust=hooks"}))

			writeTree(proj, map[string]string{".lola/project.yml": pinned()})
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries | engine.TrustHooks}})
			Expect(err).NotTo(HaveOccurred())
			Expect(os.Remove(e.Paths.Consent)).To(Succeed())
			Expect(os.RemoveAll(filepath.Join(proj, ".lola", "modules", "kit"))).To(Succeed())
			_, err = e.Render(ctx, sc, engine.Options{})
			Expect(errs.Hints(err)).To(Equal([]string{"review it: lola info --project " + url + " --ref " + v1, "then consent to it: lola render --project --trust=hooks"}))
		})
	})

	It("adds a git source's default branch to the cache", func() {
		url, commit := serveRepo()
		spec, ok, err := source.Parse(url, proj)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		v, err := e.Fetch(ctx, spec)
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Meta).To(And(HaveField("Ref", ""), HaveField("Commit", commit)))
	})

	It("installs from a local archive and adds one to the cache, recording type archive", func() {
		archive := filepath.Join(tmp, "example-1.0.tar.gz")
		Expect(os.WriteFile(archive, tarGz("example-1.0", exampleModule), 0o644)).To(Succeed())
		_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: archive}}, claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(proj, ".claude/commands/ship.md")).To(BeARegularFile())
		o, _, err := e.Store.Origin("example-1.0")
		Expect(err).NotTo(HaveOccurred())
		Expect(o).To(And(HaveField("Type", "archive"), HaveField("Source", archive)))
		Expect(lockFile().Modules["example-1.0"]).To(And(HaveField("Source", archive), HaveField("Commit", "")))
	})

	It("re-downloads an archive URL on cache update", func() {
		var hits atomic.Int32
		body := tarGz("example", exampleModule)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			_, _ = w.Write(body)
		}))
		DeferCleanup(srv.Close)
		spec, ok, err := source.Parse(srv.URL+"/example.tar.gz", proj)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		_, err = e.Fetch(ctx, spec)
		Expect(err).NotTo(HaveOccurred())

		body = tarGz("example", map[string]string{"commands/ship.md": "Ship faster\n"})
		up, err := e.Update(ctx, []string{"example"})
		Expect(err).NotTo(HaveOccurred())
		Expect(hits.Load()).To(BeEquivalentTo(2))
		Expect(up.Updates).To(ConsistOf(HaveField("Changed", true)))
		o, _, err := e.Store.Origin("example")
		Expect(err).NotTo(HaveOccurred())
		Expect(o.Type).To(Equal("archive"))
	})

	It("checks an archive without adding it to the cache", func() {
		archive := filepath.Join(tmp, "example.tgz")
		Expect(os.WriteFile(archive, tarGz("example", exampleModule), 0o644)).To(Succeed())
		rep, err := e.Check(ctx, sc, proj, []string{archive}, []string{"claude-code"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rep.Modules).To(ConsistOf(HaveField("Name", "example")))
		names, err := e.Store.Names()
		Expect(err).NotTo(HaveOccurred())
		Expect(names).To(BeEmpty())
	})

	It("sync refuses a hostile ref in lock.json before git runs", func() {
		url, _ := serveRepo()
		_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url}}, claude)
		Expect(err).NotTo(HaveOccurred())
		pwned := filepath.Join(tmp, "PWNED")
		l := lockFile()
		l.Modules["example"].Ref = "--upload-pack=touch " + pwned
		Expect(l.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())
		for _, cached := range []bool{true, false} {
			if !cached {
				Expect(e.Store.Remove("example")).To(BeTrue())
			}
			_, err = e.Render(ctx, sc, engine.Options{})
			Expect(err).To(MatchError(ContainSubstring("starts with '-'")), "render, cached: %v", cached)
			_, _, err = e.Status(ctx, sc, nil)
			Expect(err).To(MatchError(ContainSubstring("starts with '-'")), "list, cached: %v", cached)
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc})
			Expect(err).To(MatchError(ContainSubstring("starts with '-'")), "sync, cached: %v", cached)
			Expect(pwned).NotTo(BeAnExistingFile())
		}
	})

	It("sync refuses a hostile ref in project.yml before git runs", func() {
		url, _ := serveRepo()
		_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url}}, claude)
		Expect(err).NotTo(HaveOccurred())
		pwned := filepath.Join(tmp, "PWNED")
		manPath := filepath.Join(proj, ".lola", "project.yml")
		b, err := os.ReadFile(manPath)
		Expect(err).NotTo(HaveOccurred())
		hostile := strings.Replace(string(b), "source: "+url, "source: "+url+"\n    ref: --upload-pack=touch "+pwned, 1)
		Expect(hostile).NotTo(Equal(string(b)))
		Expect(os.WriteFile(manPath, []byte(hostile), 0o644)).To(Succeed())
		var requests atomic.Int32
		serve(func(*http.Request) { requests.Add(1) })
		for _, cached := range []bool{true, false} {
			if !cached {
				Expect(e.Store.Remove("example")).To(BeTrue())
			}
			for _, dry := range []bool{false, true} {
				_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{DryRun: dry}})
				Expect(err).To(MatchError(ContainSubstring("starts with '-'")), "cached: %v, dry run: %v", cached, dry)
			}
			Expect(pwned).NotTo(BeAnExistingFile())
		}
		Expect(requests.Load()).To(BeZero(), "git never ran against the source")
	})

	It("sync refuses a hostile source or path before git runs", func() {
		url, _ := serveRepo()
		_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url}}, claude)
		Expect(err).NotTo(HaveOccurred())
		pwned := filepath.Join(tmp, "PWNED")
		manPath, ledPath := filepath.Join(proj, ".lola", "project.yml"), filepath.Join(proj, ".lola", "lock.json")
		goodMan, err := os.ReadFile(manPath)
		Expect(err).NotTo(HaveOccurred())
		goodLed, err := os.ReadFile(ledPath)
		Expect(err).NotTo(HaveOccurred())
		hostile := []struct{ source, path, want string }{
			{"ext::sh -c touch " + pwned, "", "git remote helper"},
			{"--upload-pack=touch " + pwned, "", "starts with '-'"},
			{"fd::0", "", "git remote helper"},
			{"git://example.com/example.git", "", "git:// source is refused"},
			{url, "../x", "must be a relative path inside the source"},
		}
		for _, h := range hostile {
			m := "version: 1\ntargets: [claude-code]\nmodules:\n  - name: example\n    source: '" + h.source + "'\n"
			if h.path != "" {
				m += "    path: " + h.path + "\n"
			}
			Expect(os.WriteFile(manPath, []byte(m), 0o600)).To(Succeed())
			_, err = e.Manifest(sc)
			Expect(err).To(MatchError(ContainSubstring(h.want)), "project.yml load: %s", h.source)
			_, err = e.Render(ctx, sc, engine.Options{})
			Expect(err).To(MatchError(ContainSubstring(h.want)), "project.yml render: %s", h.source)
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc})
			Expect(err).To(MatchError(ContainSubstring(h.want)), "project.yml sync: %s", h.source)
			Expect(os.WriteFile(manPath, goodMan, 0o600)).To(Succeed())

			l := lockFile()
			l.Modules["example"].Source, l.Modules["example"].Path = h.source, h.path
			Expect(l.Save(ledPath)).To(Succeed())
			_, err = e.Ledger(sc)
			Expect(err).To(MatchError(ContainSubstring(h.want)), "lock.json load: %s", h.source)
			Expect(e.Store.Remove("example")).To(BeTrue())
			_, err = e.Render(ctx, sc, engine.Options{})
			Expect(err).To(MatchError(ContainSubstring(h.want)), "lock.json render: %s", h.source)
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc})
			Expect(err).To(MatchError(ContainSubstring(h.want)), "lock.json sync: %s", h.source)
			Expect(os.WriteFile(ledPath, goodLed, 0o600)).To(Succeed())
			_, err = e.Render(ctx, sc, engine.Options{})
			Expect(err).NotTo(HaveOccurred(), "restored state renders again")
			Expect(pwned).NotTo(BeAnExistingFile())
		}
	})

	Describe("render installs the version the manifest pins, else the one lock.json records", func() {
		var url, work, bare string
		BeforeEach(func() {
			url, _ = serveRepo()
			work, bare = filepath.Join(tmp, "work"), filepath.Join(tmp, "srv", "example.git")
		})
		push := func(files map[string]string) string {
			writeTree(work, files)
			gitIn(work, "add", "-A")
			gitIn(work, "commit", "-q", "-m", "change")
			gitIn(work, "push", "-q", "--all", bare)
			return gitIn(work, "rev-parse", "HEAD")
		}
		ship := func() string {
			b, err := os.ReadFile(filepath.Join(proj, ".claude/commands/ship.md"))
			Expect(err).NotTo(HaveOccurred())
			return string(b)
		}
		user := engine.Scope{Kind: target.User}
		// atUser installs req at user scope, whose user.yml and lock.json are the user's own, then
		// edits both alike by hand: render acts on what they say, as it never acts on a project's
		// lock.json against its project.yml.
		atUser := func(req engine.InstallRequest, change func(*manifest.Entry, *ledger.Entry)) {
			GinkgoHelper()
			_, err := e.Install(ctx, user, tmp, []engine.InstallRequest{req}, claude)
			Expect(err).NotTo(HaveOccurred())
			l, err := ledger.Load(e.Paths.UserLedger)
			Expect(err).NotTo(HaveOccurred())
			m, err := e.Manifest(user)
			Expect(err).NotTo(HaveOccurred())
			ent := *m.Find("example")
			in := l.Modules["example"]
			change(&ent, in)
			delete(l.Modules, "example")
			l.Modules[ent.Name] = in
			Expect(l.Save(e.Paths.UserLedger)).To(Succeed())
			Expect(os.WriteFile(e.Paths.UserManifest, []byte(""), 0o600)).To(Succeed())
			dst, data, err := manifest.Edit(e.Paths.UserManifest, manifest.Change{Set: []manifest.Entry{ent}, Targets: m.Targets})
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(dst, data, 0o600)).To(Succeed())
		}

		It("cache update never changes what is installed", func() {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "main"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			installed := lockFile().Modules["example"].Digest
			push(map[string]string{"commands/ship.md": "Ship v2\n"})
			up, err := e.Update(ctx, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(up.Updates).To(ConsistOf(HaveField("Changed", true)))
			_, err = e.Render(ctx, sc, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(ship()).To(Equal("Ship $ARGUMENTS\n"))
			Expect(lockFile().Modules["example"].Digest).To(Equal(installed))
		})

		It("render renders the pin, never the head", func() {
			_, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: url, Ref: "main"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			push(map[string]string{"commands/ship.md": "Ship v2\n"})
			_, err = e.Update(ctx, nil)
			Expect(err).NotTo(HaveOccurred())
			v2, _, err := e.Store.Head("example", "main")
			Expect(err).NotTo(HaveOccurred())
			push(map[string]string{"commands/ship.md": "Ship v3\n"})
			_, err = e.Update(ctx, nil)
			Expect(err).NotTo(HaveOccurred())
			// The user pins v2 in user.yml by hand: lock.json has v1, the cache head v3.
			m, err := e.Manifest(user)
			Expect(err).NotTo(HaveOccurred())
			ent := *m.Find("example")
			ent.Commit, ent.Digest = v2.Meta.Commit, v2.Meta.Digest
			dst, data, err := manifest.Edit(e.Paths.UserManifest, manifest.Change{Set: []manifest.Entry{ent}})
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(dst, data, 0o600)).To(Succeed())

			_, err = e.Render(ctx, user, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			b, err := os.ReadFile(filepath.Join(e.Paths.Home, ".claude/commands/ship.md"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(Equal("Ship v2\n"))
			l, err := ledger.Load(e.Paths.UserLedger)
			Expect(err).NotTo(HaveOccurred())
			Expect(l.Modules["example"]).To(And(HaveField("Digest", v2.Meta.Digest), HaveField("Commit", v2.Meta.Commit)))
		})

		It("fetches the installed version by its commit when the cache lost it, never moving the ref's head", func() {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "main"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			installed := lockFile().Modules["example"]
			push(map[string]string{"commands/ship.md": "Ship v2\n"})
			_, err = e.Update(ctx, nil)
			Expect(err).NotTo(HaveOccurred())
			head, _, err := e.Store.Head("example", "main")
			Expect(err).NotTo(HaveOccurred())
			id := strings.TrimPrefix(installed.Digest, "sha256:")[:12]
			Expect(os.RemoveAll(filepath.Join(e.Paths.Store, "example", "versions", id))).To(Succeed())
			Expect(os.Remove(filepath.Join(proj, ".claude/commands/ship.md"))).To(Succeed())

			_, err = e.Render(ctx, sc, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(ship()).To(Equal("Ship $ARGUMENTS\n"))
			Expect(filepath.Join(e.Paths.Store, "example", "versions", id)).To(BeADirectory())
			after, _, err := e.Store.Head("example", "main")
			Expect(err).NotTo(HaveOccurred())
			Expect(after.ID).To(Equal(head.ID))
			Expect(lockFile().Modules["example"]).To(And(HaveField("Digest", installed.Digest), HaveField("Commit", installed.Commit)))
			entries, err := os.ReadDir(e.Paths.Staging)
			Expect(err).NotTo(HaveOccurred())
			Expect(entries).To(BeEmpty(), "render leaves nothing staged")
		})

		It("reports the same findings for a version fetched again as for one the cache holds", func() {
			push(map[string]string{".claude-plugin/plugin.json": `{"name":"example","x-extra":"kept"}`})
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "main"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			hit, err := e.Render(ctx, sc, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(e.Store.Remove("example")).To(BeTrue())

			miss, err := e.Render(ctx, sc, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(miss.Findings).To(Equal(hit.Findings), "the cache state never changes what render reports")
			Expect(miss.Findings).NotTo(ContainElement(HaveField("Text", ContainSubstring("x-extra"))), "a format adapter's warning is install's, not render's")
		})

		It("keeps the scratch of a pinned fetch inside the run's staging directory", func() {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "main"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			Expect(e.Store.Remove("example")).To(BeTrue())
			var seen [][]string
			serve(func(*http.Request) { // on the server's goroutine: record, assert later
				entries, err := os.ReadDir(e.Paths.Staging)
				names := []string{}
				if err != nil {
					names = append(names, err.Error())
				}
				for _, d := range entries {
					names = append(names, d.Name())
				}
				seen = append(seen, names)
			})

			_, err = e.Render(ctx, sc, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			servedMu.Lock()
			defer servedMu.Unlock()
			Expect(seen).NotTo(BeEmpty(), "the version was fetched again")
			for _, names := range seen {
				Expect(names).To(ConsistOf(MatchRegexp(`^run-`)), "every temporary tree of the fetch is inside the run's directory")
			}
		})

		It("sync refuses a pinned commit that is not on its ref", func() {
			gitIn(work, "checkout", "-q", "-b", "unreviewed")
			push(map[string]string{"commands/ship.md": "Ship unreviewed\n"})
			var commit string
			atUser(engine.InstallRequest{Arg: url, Ref: "unreviewed"}, func(m *manifest.Entry, l *ledger.Entry) {
				commit, m.Ref, l.Ref = m.Commit, "main", "main"
			})
			Expect(e.Store.Remove("example")).To(BeTrue())

			_, err := e.Render(ctx, user, engine.Options{})
			Expect(err).To(MatchError("example: commit " + commit + " is not on main at " + url))
			Expect(errs.Hints(err)).To(ContainElement("re-pin to what main serves: lola sync --user --update example"))
			_, found, err := e.Store.Origin("example")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse(), "nothing was cached")
		})

		It("syncs a manifest that pins a full commit SHA in capitals from an empty cache, recording the ref lower-cased", func() {
			commit := gitIn(work, "rev-parse", "HEAD")
			atUser(engine.InstallRequest{Arg: url, Ref: commit}, func(m *manifest.Entry, l *ledger.Entry) {
				m.Ref, l.Ref = strings.ToUpper(commit), strings.ToUpper(commit)
			})
			Expect(e.Store.Remove("example")).To(BeTrue())

			_, err := e.Render(ctx, user, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			b, err := os.ReadFile(filepath.Join(e.Paths.Home, ".claude/commands/ship.md"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(Equal("Ship $ARGUMENTS\n"))
			o, found, err := e.Store.Origin("example")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(o.Heads).To(ConsistOf(HaveField("Ref", commit)), "the cache records the ref lower-cased")
		})

		It("refuses an archive that no longer holds the version installed, caching nothing", func() {
			archive := filepath.Join(tmp, "example.tgz")
			Expect(os.WriteFile(archive, tarGz("example", exampleModule), 0o644)).To(Succeed())
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: archive}}, claude)
			Expect(err).NotTo(HaveOccurred())
			Expect(e.Store.Remove("example")).To(BeTrue())
			Expect(os.WriteFile(archive, tarGz("example", map[string]string{"commands/ship.md": "replaced\n"}), 0o644)).To(Succeed())

			_, err = e.Render(ctx, sc, engine.Options{})
			Expect(err).To(MatchError(ContainSubstring("the source now serves different content than project.yml pins (pinned " +
				lockFile().Modules["example"].Digest)))
			Expect(errs.Hints(err)).To(ContainElement("if you expect the change, re-pin it: lola sync --project --update example"),
				"under the pin, never installing what the source serves unseen")
			Expect(ship()).To(Equal("Ship $ARGUMENTS\n"))
			_, found, err := e.Store.Origin("example")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
		})

		It("coaches a git version with no recorded commit to install what its source serves now", func() {
			// An unpinned entry renders lock.json's version, which records no commit.
			atUser(engine.InstallRequest{Arg: url, Ref: "main"}, func(m *manifest.Entry, l *ledger.Entry) {
				m.Commit, m.Digest, l.Commit = "", "", ""
			})
			Expect(e.Store.Remove("example")).To(BeTrue())

			_, err := e.Render(ctx, user, engine.Options{})
			Expect(err).To(MatchError(ContainSubstring("no commit is recorded")))
			Expect(errs.Hints(err)).To(ContainElement(And(ContainSubstring("lola install "), ContainSubstring("--ref main"))))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("lola uninstall example")))
		})

		It("keeps the uninstall hint when the cache index of a module cannot be read", func() {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "main"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(filepath.Join(e.Paths.Store, "example", "index.yml"), []byte(": ["), 0o600)).To(Succeed())

			_, err = e.Render(ctx, sc, engine.Options{})
			Expect(err).To(HaveOccurred())
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("lola uninstall example")))
		})

		It("leaves out a folder that now declares another module, caching nothing under that name", func() {
			folder := filepath.Join(tmp, "folder", "example")
			writeTree(folder, exampleModule)
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: folder}}, claude)
			Expect(err).NotTo(HaveOccurred())
			writeTree(folder, map[string]string{"lola.yml": "name: beta\n"})

			res, err := e.Render(ctx, sc, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Findings).To(ContainElement(And(HaveField("Module", "example"), HaveField("Code", engine.CodeContentChanged),
				HaveField("Text", `its content no longer loads: its source now declares module "beta"`),
				HaveField("Hint", "review it: lola sync --project example; or remove it: lola uninstall --project example"))))
			_, found, err := e.Store.Origin("beta")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse(), "nothing is cached under the other name")
			Expect(lockFile().Modules).To(HaveKey("example"))
			Expect(ship()).To(Equal("Ship $ARGUMENTS\n"))
		})

		It("refuses a pinned fetch that declares another module before caching it or moving a head", func() {
			// A module that names itself: one that does not takes the name its entry gives it.
			push(map[string]string{"lola.yml": "name: example\n"})
			atUser(engine.InstallRequest{Arg: url, Ref: "main"}, func(m *manifest.Entry, _ *ledger.Entry) { m.Name = "other" })
			Expect(e.Store.Remove("example")).To(BeTrue())

			_, err := e.Render(ctx, user, engine.Options{})
			Expect(err).To(MatchError(`other: its source now declares module "example"`))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("lola uninstall other")))
			for _, name := range []string{"example", "other"} {
				_, found, err := e.Store.Origin(name)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeFalse(), "nothing is cached as %s", name)
			}
		})

		It("renders a version cached before execute bits were recorded once cache update or install fetches it again", func() {
			writeTree(work, map[string]string{"hooks/build.sh": "#!/bin/sh\nexit 0\n"})
			Expect(os.Chmod(filepath.Join(work, "hooks", "build.sh"), 0o755)).To(Succeed())
			push(nil)
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "main"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			id := strings.TrimPrefix(lockFile().Modules["example"].Digest, "sha256:")[:12]
			origin := filepath.Join(e.Paths.Store, "example", "versions", id, ".lola-origin")
			b, err := os.ReadFile(origin)
			Expect(err).NotTo(HaveOccurred())
			oldFormat := regexp.MustCompile(`(?m)^executables:\n(\s+- .*\n)+`).ReplaceAll(b, nil)
			Expect(oldFormat).NotTo(Equal(b))

			for _, refetch := range []func() error{
				func() error { _, err := e.Update(ctx, nil); return err },
				func() error {
					_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "main"}}, claude)
					return err
				},
			} {
				Expect(os.WriteFile(origin, oldFormat, 0o600)).To(Succeed())
				_, err = e.Render(ctx, sc, engine.Options{})
				Expect(err).To(MatchError(ContainSubstring("does not match its digest")))
				Expect(refetch()).To(Succeed())
				_, err = e.Render(ctx, sc, engine.Options{})
				Expect(err).NotTo(HaveOccurred())
			}
		})

		It("reads a folder source as it is now at user scope, and renders the version installed once the folder is gone", func() {
			folder := filepath.Join(tmp, "folder", "example")
			writeTree(folder, exampleModule)
			_, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: folder}}, claude)
			Expect(err).NotTo(HaveOccurred())
			userShip := func() string {
				b, err := os.ReadFile(filepath.Join(e.Paths.Home, ".claude/commands/ship.md"))
				Expect(err).NotTo(HaveOccurred())
				return string(b)
			}
			writeTree(folder, map[string]string{"commands/ship.md": "Ship from the folder\n"})
			_, err = e.Render(ctx, user, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(userShip()).To(Equal("Ship from the folder\n"), "user.yml is the user's own: no trust question")

			Expect(os.RemoveAll(folder)).To(Succeed())
			res, err := e.Render(ctx, user, engine.Options{Force: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(userShip()).To(Equal("Ship from the folder\n"))
			Expect(res.Notices).To(ContainElement(HaveField("Text", "example: its source folder "+folder+" is gone; rendered the version installed")))
		})
	})

	Describe("install writes pins", func() {
		var url, v1, work, bare string
		BeforeEach(func() {
			url, v1 = serveRepo()
			work, bare = filepath.Join(tmp, "work"), filepath.Join(tmp, "srv", "example.git")
		})
		push := func(files map[string]string) string {
			writeTree(work, files)
			gitIn(work, "add", "-A")
			gitIn(work, "commit", "-q", "-m", "change")
			gitIn(work, "push", "-q", "--all", bare)
			return gitIn(work, "rev-parse", "HEAD")
		}
		entry := func() *manifest.Entry {
			man, err := e.Manifest(sc)
			Expect(err).NotTo(HaveOccurred())
			return man.Find("example")
		}

		It("pins a git source to the commit its fetch resolved and the digest installed", func() {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "v1"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			installed := lockFile().Modules["example"]
			Expect(entry()).To(And(HaveField("Ref", "v1"), HaveField("Commit", v1), HaveField("Digest", installed.Digest)))
			Expect(installed.Commit).To(Equal(v1))
		})

		It("records the fetch's commit, not the first one cached with the same content", func() {
			gitIn(work, "commit", "-q", "--allow-empty", "-m", "same content")
			gitIn(work, "push", "-q", "--all", bare)
			second := gitIn(work, "rev-parse", "HEAD")
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "v1"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "main"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			Expect(entry()).To(And(HaveField("Ref", "main"), HaveField("Commit", second)))
			Expect(lockFile().Modules["example"].Commit).To(Equal(second))
		})

		It("pins an archive to its digest only", func() {
			archive := filepath.Join(tmp, "example.tgz")
			Expect(os.WriteFile(archive, tarGz("example", exampleModule), 0o644)).To(Succeed())
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: archive}}, claude)
			Expect(err).NotTo(HaveOccurred())
			Expect(entry()).To(And(HaveField("Commit", ""), HaveField("Digest", lockFile().Modules["example"].Digest)))
		})

		It("never pins a folder", func() {
			folder := filepath.Join(tmp, "folder", "example")
			writeTree(folder, exampleModule)
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: folder}}, claude)
			Expect(err).NotTo(HaveOccurred())
			Expect(entry()).To(And(HaveField("Commit", ""), HaveField("Digest", "")))
		})

		It("refuses to pin a git version whose cached head records no commit, coaching cache update", func() {
			staged := filepath.Join(e.Paths.Staging, "fetch-legacy")
			writeTree(staged, exampleModule)
			_, err := e.Store.Put(store.PutRequest{Staged: staged, FallbackName: "example", Source: url, Type: "git", Now: e.Now()})
			Expect(err).NotTo(HaveOccurred())
			_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "example"}}, claude)
			Expect(err).To(MatchError(ContainSubstring("example: your cache does not record which commit its version")))
			Expect(errs.Hints(err)).To(ContainElement("fetch it again, then install it: lola cache update example, then lola install example"))
		})

		It("re-pins install <name> to the cache head for the entry's ref", func() {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "v1"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			later := push(map[string]string{"commands/ship.md": "Ship v2\n"})
			spec, ok, err := source.Parse(url, proj)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			_, err = e.Fetch(ctx, spec)
			Expect(err).NotTo(HaveOccurred(), "the default branch's head is now newer than v1's")

			_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "example"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			Expect(entry()).To(And(HaveField("Ref", "v1"), HaveField("Commit", v1)))
			Expect(os.ReadFile(filepath.Join(proj, ".claude/commands/ship.md"))).To(Equal([]byte("Ship $ARGUMENTS\n")))

			gitIn(work, "tag", "-f", "v1", later)
			gitIn(work, "push", "-q", "-f", "--tags", bare)
			_, err = e.Update(ctx, []string{"example"})
			Expect(err).NotTo(HaveOccurred())
			_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "example"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			Expect(entry()).To(And(HaveField("Ref", "v1"), HaveField("Commit", later), HaveField("Digest", lockFile().Modules["example"].Digest)))
			Expect(os.ReadFile(filepath.Join(proj, ".claude/commands/ship.md"))).To(Equal([]byte("Ship v2\n")))
		})

		It("keeps the installed version and its pin when an install adds targets", func() {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "main"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			pinned := *entry()
			push(map[string]string{"commands/ship.md": "Ship v2\n"})
			_, err = e.Update(ctx, nil)
			Expect(err).NotTo(HaveOccurred())
			_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "example", Targets: []string{"gemini-cli"}}}, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(entry()).To(And(HaveField("Commit", pinned.Commit), HaveField("Digest", pinned.Digest)))
		})

		It("adds targets to a module installed before pins without asking for a fetch", func() {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "v1"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			// The cache's head and the manifest's entry as a lola without pins wrote them.
			for f, field := range map[string]string{filepath.Join(e.Paths.Store, "example", "index.yml"): "commit",
				filepath.Join(proj, ".lola", "project.yml"): "commit|digest"} {
				b, err := os.ReadFile(f)
				Expect(err).NotTo(HaveOccurred())
				Expect(os.WriteFile(f, regexp.MustCompile(`(?m)^\s*(`+field+`): .*\n`).ReplaceAll(b, nil), 0o600)).To(Succeed())
			}
			man, err := e.Manifest(sc)
			Expect(err).NotTo(HaveOccurred())
			Expect(e.Trust.Save(proj, trust.ViewOf(man))).To(Succeed(), "that lola recorded the entry it wrote")
			_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "example", Targets: []string{"gemini-cli"}}}, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(entry()).To(And(HaveField("Commit", ""), HaveField("Digest", ""), HaveField("Targets", ContainElement("gemini-cli"))))
			Expect(lockFile().Modules["example"].Commit).To(Equal(v1))
		})

		It("lists the version of the entry's ref and installs exactly it", func() {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "v1"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			push(map[string]string{"commands/deploy.md": "Deploy\n"})
			_, err = e.AddSource(ctx, proj, url, "", "", nil)
			Expect(err).NotTo(HaveOccurred())
			ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: []engine.ScopeChoice{{Scope: sc, Source: engine.SourceFlag}}})
			Expect(err).NotTo(HaveOccurred())
			m := ch.Module("example")
			Expect(m).NotTo(BeNil())
			names := []string{}
			for _, c := range m.Components {
				names = append(names, c.String())
			}
			Expect(names).NotTo(ContainElement("command/deploy"))
			Expect(names).To(ContainElement("command/ship"))
			_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "example", Version: &engine.VersionRef{Name: "example", ID: m.ID}}}, claude)
			Expect(err).NotTo(HaveOccurred())
			Expect(entry()).To(And(HaveField("Ref", "v1"), HaveField("Commit", v1)))
			Expect(filepath.Join(proj, ".claude/commands/ship.md")).To(BeARegularFile())
			Expect(filepath.Join(proj, ".claude/commands/deploy.md")).NotTo(BeAnExistingFile())
		})

		It("lets the picker deselect a component the entry's ref version has", func() {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "v1"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			push(map[string]string{"commands/deploy.md": "Deploy\n"})
			_, err = e.AddSource(ctx, proj, url, "", "", nil)
			Expect(err).NotTo(HaveOccurred())
			ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: []engine.ScopeChoice{{Scope: sc, Source: engine.SourceFlag}}})
			Expect(err).NotTo(HaveOccurred())
			selector, err := config.ParseSelector("example:command/ship")
			Expect(err).NotTo(HaveOccurred())
			_, err = e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Options: claude,
				Modules: []engine.InstallRequest{{Arg: "example", Version: &engine.VersionRef{Name: "example", ID: ch.Module("example").ID}}},
				Scopes:  []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}, Edits: []engine.SelectionEdit{{Op: "skip", Selector: selector}}}}})
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Join(proj, ".claude/commands/ship.md")).NotTo(BeAnExistingFile())
		})

		It("refuses a pinned version that is not the head of the entry's ref, writing nothing", func() {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "v1"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			push(map[string]string{"commands/deploy.md": "Deploy\n"})
			def, err := e.AddSource(ctx, proj, url, "", "", nil)
			Expect(err).NotTo(HaveOccurred())
			before, err := os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))
			Expect(err).NotTo(HaveOccurred())
			_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "example", Version: &engine.VersionRef{Name: "example", ID: def.ID}}}, claude)
			Expect(err).To(MatchError(ContainSubstring("records ref v1")))
			Expect(errs.Hints(err)).To(ContainElement("run lola install again to list the current versions"))
			after, err := os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before))
			Expect(filepath.Join(proj, ".claude/commands/deploy.md")).NotTo(BeAnExistingFile())
		})

		It("refuses a pinned version the default branch its entry tracks has moved from, writing nothing", func() {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url}}, claude)
			Expect(err).NotTo(HaveOccurred())
			listed, ok, err := e.Store.Head("example", "")
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			push(map[string]string{"commands/deploy.md": "Deploy\n"})
			_, err = e.Update(ctx, nil)
			Expect(err).NotTo(HaveOccurred())
			before, err := os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))
			Expect(err).NotTo(HaveOccurred())
			_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "example", Version: &engine.VersionRef{Name: "example", ID: listed.ID}}}, claude)
			Expect(err).To(MatchError(ContainSubstring("tracks the default branch, whose cached version is")))
			Expect(errs.Hints(err)).To(ContainElement("run lola install again to list the current versions"))
			Expect(os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))).To(Equal(before))
		})

		usr := engine.Scope{Kind: target.User}
		scopes := func(order ...string) []engine.ScopeChoice {
			out := []engine.ScopeChoice{}
			for _, o := range order {
				s := sc
				if o == "user" {
					s = usr
				}
				out = append(out, engine.ScopeChoice{Scope: s, Source: engine.SourceFlag})
			}
			return out
		}

		Context("when a scope has no entry or an uncached ref", func() {
			BeforeEach(func() {
				_, err := e.Install(ctx, usr, proj, []engine.InstallRequest{{Arg: url, Ref: "v1"}}, claude)
				Expect(err).NotTo(HaveOccurred())
				push(map[string]string{"commands/deploy.md": "Deploy\n"})
				_, err = e.AddSource(ctx, proj, url, "", "", nil)
				Expect(err).NotTo(HaveOccurred())
			})
			DescribeTable("refuses a name argument at listing when a scope without an entry would take another version",
				func(order ...string) {
					_, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Modules: []engine.InstallRequest{{Arg: "example"}}, Scopes: scopes(order...)})
					Expect(err).To(MatchError(ContainSubstring("resolve to two versions")))
					Expect(errs.Hints(err)).NotTo(BeEmpty())
				},
				Entry("user first", "user", "project"),
				Entry("project first", "project", "user"),
			)

			DescribeTable("leaves out of the cache picker, with a warning, a module a scope without an entry would take at another version",
				func(order ...string) {
					ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: scopes(order...)})
					Expect(err).NotTo(HaveOccurred())
					Expect(ch.Module("example")).To(BeNil())
					Expect(ch.Warnings).To(ContainElement(SatisfyAll(ContainSubstring("example"), ContainSubstring("resolve to two versions"),
						ContainSubstring("lola install example --scope "+order[0]))))
				},
				Entry("user first", "user", "project"),
				Entry("project first", "project", "user"),
			)

			It("leaves out of the cache picker, with a warning, a module whose entry's ref has no cached head, still listing the others", func() {
				other := filepath.Join(tmp, "other")
				writeTree(other, exampleModule)
				_, err := e.AddSource(ctx, proj, other, "", "", nil)
				Expect(err).NotTo(HaveOccurred())
				path := filepath.Join(proj, ".lola", "project.yml")
				Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
				Expect(os.WriteFile(path, []byte("modules:\n  - name: example\n    source: "+url+"\n    ref: v9\n"), 0o644)).To(Succeed())
				man, err := e.Manifest(sc)
				Expect(err).NotTo(HaveOccurred())
				Expect(e.Trust.Save(proj, trust.ViewOf(man))).To(Succeed(), "that lola recorded the entry it wrote")
				ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: scopes("project")})
				Expect(err).NotTo(HaveOccurred())
				Expect(ch.Module("example")).To(BeNil())
				Expect(ch.Module("other")).NotTo(BeNil())
				Expect(ch.Warnings).To(ContainElement(SatisfyAll(ContainSubstring("example"), ContainSubstring("no version of ref v9"),
					ContainSubstring("lola install example"))))
			})

			It("refuses at listing a picked version that a scope's entry ref resolves away from, writing nothing", func() {
				ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: scopes("project")})
				Expect(err).NotTo(HaveOccurred())
				pick := []engine.InstallRequest{{Arg: "example", Version: &engine.VersionRef{Name: "example", ID: ch.Module("example").ID}}}
				_, err = e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Modules: pick, Scopes: scopes("project")})
				Expect(err).NotTo(HaveOccurred(), "the pick is the version a scope without an entry takes")
				for _, order := range [][]string{{"project", "user"}, {"user", "project"}} {
					_, err = e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Modules: pick, Scopes: scopes(order...)})
					Expect(err).To(MatchError(ContainSubstring("example: the scopes record refs of it that resolve to two versions")), "%v", order)
					Expect(errs.Hints(err)).To(ContainElements("run lola install again to pick again",
						"install it into one scope at a time: lola install example --scope "+order[0]+", then lola install example --scope "+order[1]))
				}
				_, err = e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Modules: pick, Scopes: scopes("user")})
				Expect(err).To(MatchError(ContainSubstring("example: a scope's entry records a ref whose cached version is")))
				Expect(errs.Hints(err)).To(ContainElement("run lola install again to pick again"))
				Expect(filepath.Join(proj, ".lola", "project.yml")).NotTo(BeAnExistingFile())
			})

			It("installs the cached default head into a scope without an entry ref, recording the default ref", func() {
				ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: scopes("project")})
				Expect(err).NotTo(HaveOccurred())
				m := ch.Module("example")
				Expect(m).NotTo(BeNil())
				_, err = e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Options: claude,
					Modules: []engine.InstallRequest{{Arg: "example", Version: &engine.VersionRef{Name: "example", ID: m.ID}}},
					Scopes:  []engine.ScopeInstall{{ScopeChoice: scopes("project")[0], Targets: claude.Targets}}})
				Expect(err).NotTo(HaveOccurred())
				Expect(entry()).To(And(HaveField("Ref", ""), HaveField("Commit", gitIn(work, "rev-parse", "HEAD"))))
				Expect(filepath.Join(proj, ".claude/commands/deploy.md")).To(BeARegularFile())
			})
		})

		Context("when the cache holds only another ref's head", func() {
			BeforeEach(func() {
				_, err := e.Install(ctx, usr, proj, []engine.InstallRequest{{Arg: url, Ref: "v1"}}, claude)
				Expect(err).NotTo(HaveOccurred())
			})
			DescribeTable("installs the listed head into a scope without an entry ref, recording that head's ref",
				func(order ...string) {
					ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: scopes(order...)})
					Expect(err).NotTo(HaveOccurred())
					Expect(ch.Warnings).To(BeEmpty())
					m := ch.Module("example")
					Expect(m).NotTo(BeNil())
					spec := engine.InstallSpec{Cwd: proj, Options: claude,
						Modules: []engine.InstallRequest{{Arg: "example", Version: &engine.VersionRef{Name: "example", ID: m.ID}}}}
					for _, s := range scopes(order...) {
						spec.Scopes = append(spec.Scopes, engine.ScopeInstall{ScopeChoice: s, Targets: claude.Targets})
					}
					_, err = e.InstallAll(ctx, spec)
					Expect(err).NotTo(HaveOccurred())
					for _, s := range scopes(order...) {
						man, err := e.Manifest(s.Scope)
						Expect(err).NotTo(HaveOccurred())
						Expect(man.Find("example")).To(And(HaveField("Ref", "v1"), HaveField("Commit", v1)), s.Scope.Key())
					}
					Expect(e.Store.Remove("example")).To(BeTrue())
					for _, s := range scopes(order...) {
						_, err = e.Render(ctx, s.Scope, engine.Options{})
						Expect(err).NotTo(HaveOccurred(), "render from an emptied cache fetches the pin through its ref")
					}
				},
				Entry("user, project", "user", "project"),
				Entry("project, user", "project", "user"),
				Entry("project only", "project"),
			)

			It("leaves out of the cache picker, with a warning, a module whose entry tracks a default branch that is not cached", func() {
				path := filepath.Join(proj, ".lola", "project.yml")
				Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
				before := []byte("modules:\n  - name: example\n    source: " + url + "\n")
				Expect(os.WriteFile(path, before, 0o644)).To(Succeed())
				man, err := e.Manifest(sc)
				Expect(err).NotTo(HaveOccurred())
				Expect(e.Trust.Save(proj, trust.ViewOf(man))).To(Succeed(), "that lola recorded the entry it wrote")
				ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: scopes("project")})
				Expect(err).NotTo(HaveOccurred())
				Expect(ch.Module("example")).To(BeNil())
				Expect(ch.Warnings).To(ContainElement(SatisfyAll(ContainSubstring("example is not listed"), ContainSubstring("default branch"),
					ContainSubstring("lola install example"))))
				head, ok, err := e.Store.Head("example", "v1")
				Expect(err).NotTo(HaveOccurred())
				Expect(ok).To(BeTrue())
				_, err = e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Options: claude,
					Modules: []engine.InstallRequest{{Arg: "example", Version: &engine.VersionRef{Name: "example", ID: head.ID}}},
					Scopes:  []engine.ScopeInstall{{ScopeChoice: scopes("project")[0], Targets: claude.Targets}}})
				Expect(err).To(HaveOccurred(), "an entry tracking the default branch is never moved to another ref")
				Expect(os.ReadFile(path)).To(Equal(before))
			})
		})

		It("leaves out of the cache picker, with a warning, a module whose scopes record refs resolving to two versions", func() {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "v1"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			push(map[string]string{"commands/deploy.md": "Deploy\n"})
			_, err = e.Install(ctx, usr, proj, []engine.InstallRequest{{Arg: url, Ref: "main"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: scopes("project", "user")})
			Expect(err).NotTo(HaveOccurred())
			Expect(ch.Module("example")).To(BeNil())
			Expect(ch.Warnings).To(ContainElement(ContainSubstring("example is not listed: the scopes record refs of it that resolve to two versions")))
		})

		Context("when the cache's git head predates pins", func() {
			BeforeEach(func() {
				_, err := e.AddSource(ctx, proj, url, "", "", nil)
				Expect(err).NotTo(HaveOccurred())
				index := filepath.Join(e.Paths.Store, "example", "index.yml")
				b, err := os.ReadFile(index)
				Expect(err).NotTo(HaveOccurred())
				Expect(os.WriteFile(index, regexp.MustCompile(`(?m)^\s*commit: .*\n`).ReplaceAll(b, nil), 0o600)).To(Succeed())
			})

			It("leaves it out of the cache picker, with a warning coaching a fetch", func() {
				ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Scopes: scopes("project")})
				Expect(err).NotTo(HaveOccurred())
				Expect(ch.Module("example")).To(BeNil())
				Expect(ch.Warnings).To(ContainElement(SatisfyAll(ContainSubstring("example is not listed"),
					ContainSubstring("does not record which commit"), ContainSubstring("lola cache update example"))))
			})

			It("refuses it as a module argument at listing, writing nothing", func() {
				_, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Modules: []engine.InstallRequest{{Arg: "example"}}, Scopes: scopes("project")})
				Expect(err).To(MatchError(ContainSubstring("does not record which commit")))
				Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("lola cache update example")))
				Expect(filepath.Join(proj, ".lola")).NotTo(BeAnExistingFile())
			})
		})

		It("refuses to pin a version only another ref's head holds, coaching cache update", func() {
			def, err := e.AddSource(ctx, proj, url, "", "", nil)
			Expect(err).NotTo(HaveOccurred())
			_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "example", Ref: "v1", Version: &engine.VersionRef{Name: "example", ID: def.ID}}}, claude)
			Expect(err).To(MatchError(ContainSubstring("example: your cache does not record which commit its version")))
			Expect(errs.Hints(err)).To(ContainElement("fetch it again, then install it: lola cache update example, then lola install example"))
		})

		It("gives each scope the commit of its own entry's ref", func() {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "v1"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			_, err = e.Install(ctx, usr, proj, []engine.InstallRequest{{Arg: "example", Ref: "v1"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			later := push(map[string]string{"commands/ship.md": "Ship v2\n"})
			gitIn(work, "tag", "v2", later)
			gitIn(work, "push", "-q", "--tags", bare)
			// The project's entry moves to v2 the way lola records it: installed from its source.
			_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "v2"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			_, err = e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Modules: []engine.InstallRequest{{Arg: "example"}}, Options: claude, Scopes: []engine.ScopeInstall{
				{ScopeChoice: engine.ScopeChoice{Scope: sc, Source: engine.SourceFlag}}, {ScopeChoice: engine.ScopeChoice{Scope: usr, Source: engine.SourceFlag}}}})
			Expect(err).NotTo(HaveOccurred())
			Expect(entry()).To(And(HaveField("Ref", "v2"), HaveField("Commit", later)))
			uman, err := e.Manifest(usr)
			Expect(err).NotTo(HaveOccurred())
			Expect(uman.Find("example")).To(And(HaveField("Ref", "v1"), HaveField("Commit", v1)))
		})

		It("pins the version the pickers listed to the commit of the head that holds it", func() {
			ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj, Modules: []engine.InstallRequest{{Arg: url, Ref: "v1"}},
				Scopes: []engine.ScopeChoice{{Scope: sc, Source: engine.SourceFlag}}})
			Expect(err).NotTo(HaveOccurred())
			Expect(ch.Versions).To(HaveLen(1))
			_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "v1", Version: &ch.Versions[0]}}, claude)
			Expect(err).NotTo(HaveOccurred())
			Expect(entry()).To(HaveField("Commit", v1))
		})
	})

	Describe("list compares the installed version with the pin and the cache head", func() {
		var url string
		BeforeEach(func() { url, _ = serveRepo() })
		row := func() engine.StatusRow {
			rows, _, err := e.Status(ctx, sc, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(rows).To(HaveLen(1))
			return rows[0]
		}
		// editPin rewrites project.yml with digest as the pin ("" for an unpinned entry).
		editPin := func(digest string) {
			m := "version: 1\ntargets: [claude-code]\nmodules:\n  - name: example\n    source: " + url + "\n    ref: main\n"
			if digest != "" {
				m += "    commit: " + lockFile().Modules["example"].Commit + "\n    digest: " + digest + "\n"
			}
			Expect(os.WriteFile(filepath.Join(proj, ".lola", "project.yml"), []byte(m), 0o600)).To(Succeed())
		}

		It("shows the pin, ok and no update for a fresh install", func() {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "main"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			Expect(row()).To(And(HaveField("Status", "ok"), HaveField("Pin", lockFile().Modules["example"].Digest), HaveField("UpdateAvailable", false)))
		})

		It("calls a module stale when project.yml pins another version than the one installed", func() {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "main"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			other := "sha256:" + strings.Repeat("e", 64)
			editPin(other)
			Expect(row()).To(And(HaveField("Status", "stale"), HaveField("Pin", other)))
			editPin("")
			Expect(row()).To(And(HaveField("Status", "ok"), HaveField("Pin", "")), "an unpinned entry is never stale")
		})

		It("says an update is available when the cache head for the entry's ref differs, and is not stale", func() {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "main"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			work := filepath.Join(tmp, "work")
			writeTree(work, map[string]string{"commands/ship.md": "Ship v2\n"})
			gitIn(work, "commit", "-qam", "v2")
			gitIn(work, "push", "-q", "--all", filepath.Join(tmp, "srv", "example.git"))
			_, err = e.Update(ctx, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(row()).To(And(HaveField("Status", "ok"), HaveField("UpdateAvailable", true)))
			editPin("")
			Expect(row()).To(HaveField("UpdateAvailable", true), "unpinned: the head against the version installed")
		})
	})

	Describe("run staging", func() {
		// runs lists the run directories under the cache's staging directory.
		runs := func() []string {
			es, err := os.ReadDir(e.Paths.Staging)
			if os.IsNotExist(err) {
				return nil
			}
			Expect(err).NotTo(HaveOccurred())
			var out []string
			for _, d := range es {
				if strings.HasPrefix(d.Name(), "run-") {
					out = append(out, d.Name())
				}
			}
			return out
		}

		It("keeps a staging command's fetch temporaries in one private run directory, removed when it ends", func() {
			url, _ := serveRepo()
			real, err := exec.LookPath("git")
			Expect(err).NotTo(HaveOccurred())
			log := filepath.Join(tmp, "git.log")
			// git logs each directory lola runs it in (-C), then runs as itself.
			wrapper := "#!/bin/sh\nprev=\nfor a in \"$@\"; do\n  [ \"$prev\" = -C ] && printf '%s\\n' \"$a\" >> " + log + "\n  prev=$a\ndone\nexec " + real + " \"$@\"\n"
			Expect(os.WriteFile(filepath.Join(tmp, "bin", "git"), []byte(wrapper), 0o755)).To(Succeed())

			_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "v1"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			b, err := os.ReadFile(log)
			Expect(err).NotTo(HaveOccurred())
			seen := map[string]bool{}
			for _, dir := range strings.Fields(string(b)) {
				rel, err := filepath.Rel(e.Paths.Staging, dir)
				Expect(err).NotTo(HaveOccurred())
				if !filepath.IsLocal(rel) {
					continue // not a temporary of the fetch
				}
				parts := strings.Split(filepath.ToSlash(rel), "/")
				Expect(parts[0]).To(HavePrefix("run-"), "every temporary is inside the run directory: %s", rel)
				seen[parts[0]] = true
			}
			Expect(seen).To(HaveLen(1), "one run directory per command")
			Expect(runs()).To(BeEmpty(), "the run directory is removed when the command ends")
		})

		It("removes the run directory when a fetch fails or is cancelled", func() {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: testenv.ServeGit(filepath.Join(tmp, "srv")) + "/missing.git"}}, claude)
			Expect(err).To(HaveOccurred())
			Expect(runs()).To(BeEmpty())

			url, _ := serveRepo()
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			_, err = e.Install(cancelled, sc, proj, []engine.InstallRequest{{Arg: url}}, claude)
			Expect(err).To(HaveOccurred())
			Expect(runs()).To(BeEmpty())
		})

		DescribeTable("each staging command first removes run directories older than a day, never a younger one",
			func(command func() error) {
				old, live := filepath.Join(e.Paths.Staging, "run-old"), filepath.Join(e.Paths.Staging, "run-live")
				writeTree(old, map[string]string{"fetch-1/commands/a.md": "a"})
				writeTree(live, map[string]string{"fetch-2/commands/a.md": "a"})
				Expect(os.Chtimes(old, time.Now().Add(-25*time.Hour), time.Now().Add(-25*time.Hour))).To(Succeed())
				Expect(os.Chtimes(live, time.Now().Add(-23*time.Hour), time.Now().Add(-23*time.Hour))).To(Succeed())

				_ = command() // the sweep comes first, whether or not the command then succeeds

				Expect(old).NotTo(BeADirectory())
				Expect(filepath.Join(live, "fetch-2/commands/a.md")).To(BeARegularFile())
				Expect(runs()).To(Equal([]string{"run-live"}))
			},
			Entry("install", func() error {
				_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: filepath.Join(tmp, "nowhere")}}, claude)
				return err
			}),
			Entry("cache update", func() error { _, err := e.Update(ctx, nil); return err }),
			Entry("render", func() error { _, err := e.Render(ctx, sc, claude); return err }),
			Entry("the install pickers", func() error {
				_, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj})
				return err
			}),
		)

		noticeTexts := func(ns []engine.Notice) []string {
			var out []string
			for _, n := range ns {
				out = append(out, n.Text)
			}
			return out
		}

		DescribeTable("a stale run directory the sweep cannot remove is a warning, never a refusal",
			func(command func() ([]string, error)) {
				if os.Geteuid() == 0 {
					Skip("root ignores directory permissions")
				}
				locked := filepath.Join(e.Paths.Staging, "run-old", "fetch-1")
				writeTree(locked, map[string]string{"commands/a.md": "a"})
				Expect(os.Chmod(locked, 0o500)).To(Succeed())
				DeferCleanup(os.Chmod, locked, os.FileMode(0o700))
				old := filepath.Dir(locked)
				Expect(os.Chtimes(old, time.Now().Add(-25*time.Hour), time.Now().Add(-25*time.Hour))).To(Succeed())

				warnings, err := command()
				Expect(err).NotTo(HaveOccurred())
				Expect(warnings).To(ContainElement(ContainSubstring("could not remove")))
				Expect(old).To(BeADirectory())
			},
			Entry("install", func() ([]string, error) {
				writeTree(filepath.Join(tmp, "src", "example"), exampleModule)
				res, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: filepath.Join(tmp, "src", "example")}}, claude)
				if err != nil {
					return nil, err
				}
				return noticeTexts(res.Notices), nil
			}),
			Entry("cache update", func() ([]string, error) {
				res, err := e.Update(ctx, nil)
				if err != nil {
					return nil, err
				}
				return noticeTexts(res.Notices), nil
			}),
			Entry("render", func() ([]string, error) {
				res, err := e.Render(ctx, sc, claude)
				if err != nil {
					return nil, err
				}
				return noticeTexts(res.Notices), nil
			}),
			Entry("the install pickers", func() ([]string, error) {
				ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: proj})
				if err != nil {
					return nil, err
				}
				return ch.Warnings, nil
			}),
		)
	})
})
