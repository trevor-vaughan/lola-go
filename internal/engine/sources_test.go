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
	"sort"
	"strings"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/source"
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
		ups, err := e.Update(ctx, []string{"example"})
		Expect(err).NotTo(HaveOccurred())
		Expect(hits.Load()).To(BeEquivalentTo(2))
		Expect(ups).To(ConsistOf(HaveField("Changed", true)))
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

	It("refuses a hostile ref recorded in the project's state before running git", func() {
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
			Expect(err).To(MatchError(ContainSubstring("starts with '-'")), "cached: %v", cached)
			Expect(pwned).NotTo(BeAnExistingFile())
		}
	})
})
