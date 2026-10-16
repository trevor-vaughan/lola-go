package engine_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"

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
	)
	BeforeEach(func() {
		tmp = testenv.Isolate()
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj = filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
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
		return testenv.ServeGit(filepath.Join(tmp, "srv")) + "/example.git", gitIn(work, "rev-parse", "v1")
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
		rep, err := e.Check(ctx, sc, proj, []string{archive}, []string{"claude-code"})
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
			_, err = e.Status(sc)
			Expect(err).To(MatchError(ContainSubstring("starts with '-'")), "list, cached: %v", cached)
			Expect(pwned).NotTo(BeAnExistingFile())
		}
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
			Expect(os.WriteFile(manPath, goodMan, 0o600)).To(Succeed())

			l := lockFile()
			l.Modules["example"].Source, l.Modules["example"].Path = h.source, h.path
			Expect(l.Save(ledPath)).To(Succeed())
			_, err = e.Ledger(sc)
			Expect(err).To(MatchError(ContainSubstring(h.want)), "lock.json load: %s", h.source)
			Expect(e.Store.Remove("example")).To(BeTrue())
			_, err = e.Render(ctx, sc, engine.Options{})
			Expect(err).To(MatchError(ContainSubstring(h.want)), "lock.json render: %s", h.source)
			Expect(os.WriteFile(ledPath, goodLed, 0o600)).To(Succeed())
			_, err = e.Render(ctx, sc, engine.Options{})
			Expect(err).NotTo(HaveOccurred(), "restored state renders again")
			Expect(pwned).NotTo(BeAnExistingFile())
		}
	})

	Describe("render installs the version lock.json records", func() {
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

		It("sync refuses a pinned commit that is not on its ref", func() {
			gitIn(work, "checkout", "-q", "-b", "unreviewed")
			push(map[string]string{"commands/ship.md": "Ship unreviewed\n"})
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "unreviewed"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			l := lockFile()
			commit := l.Modules["example"].Commit
			l.Modules["example"].Ref = "main"
			Expect(l.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())
			Expect(e.Store.Remove("example")).To(BeTrue())

			_, err = e.Render(ctx, sc, engine.Options{})
			Expect(err).To(MatchError("example: commit " + commit + " is not on main at " + url))
			_, found, err := e.Store.Origin("example")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse(), "nothing was cached")
		})

		It("refuses an archive that no longer holds the version installed, caching nothing", func() {
			archive := filepath.Join(tmp, "example.tgz")
			Expect(os.WriteFile(archive, tarGz("example", exampleModule), 0o644)).To(Succeed())
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: archive}}, claude)
			Expect(err).NotTo(HaveOccurred())
			Expect(e.Store.Remove("example")).To(BeTrue())
			Expect(os.WriteFile(archive, tarGz("example", map[string]string{"commands/ship.md": "replaced\n"}), 0o644)).To(Succeed())

			_, err = e.Render(ctx, sc, engine.Options{})
			Expect(err).To(MatchError(ContainSubstring("not the pinned " + lockFile().Modules["example"].Digest)))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("lola install ")))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("lola uninstall example")))
			Expect(ship()).To(Equal("Ship $ARGUMENTS\n"))
			_, found, err := e.Store.Origin("example")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
		})

		It("coaches a git version with no recorded commit to install what its source serves now", func() {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "main"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			l := lockFile()
			l.Modules["example"].Commit = ""
			Expect(l.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())
			Expect(e.Store.Remove("example")).To(BeTrue())

			_, err = e.Render(ctx, sc, engine.Options{})
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

		It("refuses a folder that now declares another module, caching nothing under that name", func() {
			folder := filepath.Join(tmp, "folder", "example")
			writeTree(folder, exampleModule)
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: folder}}, claude)
			Expect(err).NotTo(HaveOccurred())
			writeTree(folder, map[string]string{"lola.yml": "name: beta\n"})

			_, err = e.Render(ctx, sc, engine.Options{})
			Expect(err).To(MatchError(`example: its source now declares module "beta"`))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("lola uninstall example")))
			_, found, err := e.Store.Origin("beta")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse(), "nothing is cached under the other name")
			Expect(lockFile().Modules).To(HaveKey("example"))
			Expect(ship()).To(Equal("Ship $ARGUMENTS\n"))
		})

		It("refuses a pinned fetch that declares another module before caching it or moving a head", func() {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "main"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			l := lockFile()
			l.Modules["other"] = l.Modules["example"]
			delete(l.Modules, "example")
			Expect(l.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())
			Expect(e.Store.Remove("example")).To(BeTrue())

			_, err = e.Render(ctx, sc, engine.Options{})
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

		It("reads a folder source as it is now, and renders the version installed once the folder is gone", func() {
			folder := filepath.Join(tmp, "folder", "example")
			writeTree(folder, exampleModule)
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: folder}}, claude)
			Expect(err).NotTo(HaveOccurred())
			writeTree(folder, map[string]string{"commands/ship.md": "Ship from the folder\n"})
			_, err = e.Render(ctx, sc, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(ship()).To(Equal("Ship from the folder\n"))

			Expect(os.RemoveAll(folder)).To(Succeed())
			res, err := e.Render(ctx, sc, engine.Options{Force: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(ship()).To(Equal("Ship from the folder\n"))
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
			_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "example", Targets: []string{"gemini-cli"}}}, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(entry()).To(And(HaveField("Commit", ""), HaveField("Digest", ""), HaveField("Targets", ContainElement("gemini-cli"))))
			Expect(lockFile().Modules["example"].Commit).To(Equal(v1))
		})

		It("lists the version of the entry's ref and installs exactly it", func() {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "v1"}}, claude)
			Expect(err).NotTo(HaveOccurred())
			push(map[string]string{"commands/deploy.md": "Deploy\n"})
			_, err = e.AddSource(ctx, proj, url, "", "")
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
			_, err = e.AddSource(ctx, proj, url, "", "")
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
			def, err := e.AddSource(ctx, proj, url, "", "")
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
				_, err = e.AddSource(ctx, proj, url, "", "")
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
				_, err := e.AddSource(ctx, proj, other, "", "")
				Expect(err).NotTo(HaveOccurred())
				path := filepath.Join(proj, ".lola", "project.yml")
				Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
				Expect(os.WriteFile(path, []byte("modules:\n  - name: example\n    source: "+url+"\n    ref: v9\n"), 0o644)).To(Succeed())
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
				_, err := e.AddSource(ctx, proj, url, "", "")
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
			def, err := e.AddSource(ctx, proj, url, "", "")
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
			mpath := filepath.Join(proj, ".lola", "project.yml")
			raw, err := os.ReadFile(mpath)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(mpath, bytes.Replace(raw, []byte("ref: v1"), []byte("ref: v2"), 1), 0o644)).To(Succeed())
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
			rows, err := e.Status(sc)
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
})
