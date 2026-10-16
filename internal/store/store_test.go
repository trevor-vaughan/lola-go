package store_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// goldenFixture is the module goldenDigest is the digest of, with hooks/build.sh executable.
var goldenFixture = map[string]string{
	"lola.yml":               "name: example\ndescription: A fixture\nversion: 1.0.0\nkeywords: [b, a]\n",
	"skills/review/SKILL.md": "---\ndescription: Review code\n---\nReview it.\n",
	"commands/ship.md":       "Ship $ARGUMENTS\n",
	"hooks/build.sh":         "#!/bin/sh\nexit 0\n",
	"mcps.json":              `{"mcpServers":{"gh":{"command":"gh","args":["mcp"]}}}`,
}

// writeFiles writes files below root.
func writeFiles(root string, files map[string]string) {
	for rel, c := range files {
		Expect(os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(root, rel), []byte(c), 0o644)).To(Succeed())
	}
}

func gitIn(dir string, args ...string) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))
}

// writeTarGz writes files as a gzip tar at path, below a directory named example, with the
// executable ones at mode 0755.
func writeTarGz(path string, files map[string]string, executable ...string) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	names := slices.Sorted(maps.Keys(files))
	for _, n := range names {
		mode := int64(0o644)
		if slices.Contains(executable, n) {
			mode = 0o755
		}
		Expect(tw.WriteHeader(&tar.Header{Name: "example/" + n, Mode: mode, Typeflag: tar.TypeReg, Size: int64(len(files[n]))})).To(Succeed())
		_, err := tw.Write([]byte(files[n]))
		Expect(err).NotTo(HaveOccurred())
	}
	Expect(tw.Close()).To(Succeed())
	Expect(zw.Close()).To(Succeed())
	Expect(os.WriteFile(path, buf.Bytes(), 0o644)).To(Succeed())
}

// goldenDigest is the digest of the fixture module of "the digest of a fixture module is stable".
const goldenDigest = "sha256:df27ef8b1d9e26a735e139c0778668e2b5d9501415f9ca80d16a1bcbb5e249d0"

var _ = Describe("Store", func() {
	var (
		p   paths.Paths
		s   *store.Store
		now = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	)

	// stage builds a module content dir under paths.Staging, as a fetcher would.
	stage := func(files map[string]string) string {
		Expect(os.MkdirAll(p.Staging, 0o700)).To(Succeed())
		dir, err := os.MkdirTemp(p.Staging, "fetch-*")
		Expect(err).NotTo(HaveOccurred())
		for rel, c := range files {
			Expect(os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dir, rel), []byte(c), 0o644)).To(Succeed())
		}
		return dir
	}
	put := func(files map[string]string, ref string) (store.Version, error) {
		return s.Put(store.PutRequest{Staged: stage(files), FallbackName: "example", Source: "/src/example", Type: "folder", Ref: ref, Now: now})
	}

	BeforeEach(func() {
		testenv.Isolate()
		var err error
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		s = store.New(p, targets.IDs())
	})

	It("stores a version, moves the staged dir, and records the index and the version's .lola-origin", func() {
		staged := stage(map[string]string{"commands/a.md": "A"})
		v, err := s.Put(store.PutRequest{Staged: staged, FallbackName: "example", Source: "/src/example", Type: "folder", Now: now})
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Name).To(Equal("example"))
		Expect(v.ID).To(HaveLen(12))
		Expect(v.Meta.Digest).To(HavePrefix("sha256:" + v.ID))
		Expect(v.Meta.FetchedAt).To(Equal("2026-10-08T12:00:00Z"))
		Expect(v.Meta.Format).To(Equal("native"))
		Expect(filepath.Join(p.Store, "example", "versions", v.ID, ".lola-origin")).To(BeARegularFile())
		Expect(v.Module.Commands).To(HaveLen(1))
		Expect(filepath.Join(v.ContentDir, "commands/a.md")).To(BeARegularFile())
		Expect(staged).NotTo(BeADirectory())
		o, found, err := s.Origin("example")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(o).To(Equal(store.Origin{Source: "/src/example", Type: "folder", Heads: []store.Head{{Ref: "", Digest: v.ID}}}))
		fi, err := os.Stat(filepath.Join(p.Store, "example", "index.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
	})

	It("adapts a plugin once: .lola-origin records its format, the version keeps the plugin files, and a refetch is the same version", func() {
		plugin := map[string]string{
			".claude-plugin/plugin.json": `{"name":"deploy","lspServers":{},"mcpServers":"./b.mcpb"}`,
			"commands/a.md":              "A",
			"mcps.json":                  `{"mcpServers":{"native":{"command":"n"}}}`,
		}
		v, err := put(plugin, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Name).To(Equal("deploy"))
		Expect(v.Meta.Format).To(Equal("claude-plugin"))
		Expect(v.Meta.Manifest).To(Equal(".claude-plugin/plugin.json"))
		Expect(v.Module.Format).To(Equal("claude-plugin"))
		Expect(v.Module.MCPServers).To(BeEmpty())
		Expect(filepath.Join(v.ContentDir, ".claude-plugin", "plugin.json")).To(BeARegularFile())
		Expect(v.Module.Warnings).To(HaveLen(2), "Put reports what detection found, which a cached load no longer sees")
		again, err := put(plugin, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(again.ID).To(Equal(v.ID))
		loaded, err := s.Load("deploy", v.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(loaded.Module.Warnings).To(BeEmpty())
		Expect(loaded.Module.Commands).To(Equal(v.Module.Commands))
	})

	It("caches a plugin with a \"<<\" key, which YAML would otherwise read as a merge key", func() {
		v, err := put(map[string]string{".claude-plugin/plugin.json": `{"name":"mk","mcpServers":{"s":{"command":"x","env":{"<<":"v"}}}}`}, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Module.MCPServers).To(HaveLen(1))
		Expect(v.Module.MCPServers[0].Env).To(Equal(map[string]string{"<<": "v"}))
	})

	It("caches a Claude hooks event named \"<<\" as a literal event", func() {
		v, err := put(map[string]string{".claude-plugin/plugin.json": `{"name":"mk","hooks":{"<<":[{"hooks":[{"type":"command","command":"x"}]}]}}`}, "")
		Expect(err).NotTo(HaveOccurred())
		loaded, err := s.Load("mk", v.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(loaded.Module.RuntimeHooks).To(HaveLen(1))
		Expect(string(loaded.Module.RuntimeHooks[0].Hooks)).To(MatchJSON(`{"<<":[{"hooks":[{"type":"command","command":"x"}]}]}`))
	})

	It("writes a \"<<\" value as a quoted string, not a merge tag", func() {
		v, err := put(map[string]string{".claude-plugin/plugin.json": `{"name":"mk","mcpServers":{"s":{"command":"<<","env":{"A":"<<"}}}}`}, "")
		Expect(err).NotTo(HaveOccurred())
		yml, err := os.ReadFile(filepath.Join(v.ContentDir, "lola.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(yml)).NotTo(ContainSubstring("!!merge"))
		Expect(string(yml)).To(ContainSubstring(`A: "<<"`))
		loaded, err := s.Load("mk", v.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(loaded.Module.MCPServers[0].Command).To(Equal("<<"))
		Expect(loaded.Module.MCPServers[0].Env).To(Equal(map[string]string{"A": "<<"}))
	})

	DescribeTable("caches a kept value of -0.0, which YAML would read back as 0", func(files map[string]string) {
		_, err := put(files, "")
		Expect(err).NotTo(HaveOccurred())
	},
		Entry("a Claude plugin's unknown key", map[string]string{".claude-plugin/plugin.json": `{"name":"mk","weird":-0.0}`, "commands/a.md": "A"}),
		Entry("an Agent Plugins extensions value", map[string]string{
			"plugin.json":       `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"mk","extensions":{"com.example":{"n":-0.0}}}`,
			"skills/s/SKILL.md": "---\nname: s\ndescription: S\n---\n",
		}),
	)

	It("refuses a cache index whose head is not a version id, before using it in a path", func() {
		_, err := put(map[string]string{"commands/a.md": "x"}, "")
		Expect(err).NotTo(HaveOccurred())
		index := filepath.Join(p.Store, "example", "index.yml")
		Expect(os.WriteFile(index, []byte("source: /src/example\ntype: folder\nheads:\n  - {ref: \"\", digest: ../../../outside}\n"), 0o600)).To(Succeed())
		_, _, err = s.Origin("example")
		Expect(err).To(MatchError(ContainSubstring(`version id "../../../outside" is invalid`)))
		_, _, err = s.Head("example", "")
		Expect(err).To(HaveOccurred())
	})

	It("refuses a cache index whose head records a commit that is not a full SHA", func() {
		v, err := put(map[string]string{"commands/a.md": "x"}, "")
		Expect(err).NotTo(HaveOccurred())
		index := filepath.Join(p.Store, "example", "index.yml")
		Expect(os.WriteFile(index, []byte("source: /src/example\ntype: git\nheads:\n  - {ref: \"\", digest: "+v.ID+", commit: nope}\n"), 0o600)).To(Succeed())
		_, _, err = s.Origin("example")
		Expect(err).To(MatchError(ContainSubstring(`records commit "nope"`)))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("lola cache rm example")))
		_, _, err = s.Head("example", "")
		Expect(err).To(HaveOccurred())

		full := strings.Repeat("a", 40)
		Expect(os.WriteFile(index, []byte("source: /src/example\ntype: git\nheads:\n  - {ref: \"\", digest: "+v.ID+", commit: "+full+"}\n"), 0o600)).To(Succeed())
		got, ok, err := s.Head("example", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(got.Commit).To(Equal(full))
	})

	It("refuses a cached version whose recorded digest is not its id's", func() {
		v, err := put(map[string]string{"commands/a.md": "x"}, "")
		Expect(err).NotTo(HaveOccurred())
		record := filepath.Join(p.Store, "example", "versions", v.ID, ".lola-origin")
		b, err := os.ReadFile(record)
		Expect(err).NotTo(HaveOccurred())
		for _, bad := range []string{"sha256:" + strings.Repeat("f", 64), "nope"} {
			Expect(os.WriteFile(record, []byte(strings.Replace(string(b), v.Meta.Digest, bad, 1)), 0o600)).To(Succeed())
			_, err = s.Load("example", v.ID)
			Expect(err).To(MatchError(ContainSubstring("cached version example/" + v.ID + " records digest")))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("lola cache rm example")))
		}
	})

	It("refuses names and version ids that are not lola's before using them in a path", func() {
		_, _, err := s.Origin("../outside")
		Expect(err).To(MatchError(ContainSubstring("module name")))
		_, err = s.Load("../outside", "0123456789ab")
		Expect(err).To(MatchError(ContainSubstring("module name")))
		_, err = s.Load("example", "../versions")
		Expect(err).To(MatchError(ContainSubstring("is invalid")))
		_, err = s.Has("../outside")
		Expect(err).To(MatchError(ContainSubstring("module name")))
	})

	It("reports a cache entry present whatever its index holds", func() {
		_, err := put(map[string]string{"commands/a.md": "x"}, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(p.Store, "example", "index.yml"), []byte("heads: [\n"), 0o600)).To(Succeed())
		_, _, err = s.Origin("example")
		Expect(err).To(HaveOccurred())
		Expect(s.Has("example")).To(BeTrue())
		Expect(s.Has("other")).To(BeFalse())
	})

	It("normalizes at add: one generated lola.yml, the others removed and recorded in .lola-origin", func() {
		v, err := put(map[string]string{"lola.yml": "name: example\n", ".lola.yaml": "name: other\n", "commands/a.md": "x"}, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Meta.Format).To(Equal("lola"))
		Expect(v.Meta.Manifest).To(Equal("lola.yml"))
		Expect(v.Meta.OthersPresent).To(Equal([]string{".lola.yaml"}))
		Expect(filepath.Join(v.ContentDir, ".lola.yaml")).NotTo(BeAnExistingFile())
		b, err := os.ReadFile(filepath.Join(v.ContentDir, "lola.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("name: example\n"))
		Expect(store.Digest(v.ContentDir, nil)).To(Equal(v.Meta.Digest), "the digest covers the normalized content")
	})

	It("records the source's executables and digests with them, never with the file system's bits", func() {
		staged := stage(map[string]string{"lola.yml": "name: example\n", "commands/run.sh": "#!/bin/sh\n", "commands/a.md": "A"})
		v, err := s.Put(store.PutRequest{Staged: staged, FallbackName: "example", Source: "/src/example", Type: "folder", Now: now,
			Executables: []string{"commands/run.sh", "lola.yml", "gone.sh"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Meta.Executables).To(Equal([]string{"commands/run.sh"}), "the generated lola.yml and files not kept are never executable")
		Expect(store.Digest(v.ContentDir, v.Meta.Executables)).To(Equal(v.Meta.Digest))
		Expect(store.Digest(v.ContentDir, nil)).NotTo(Equal(v.Meta.Digest))
		back, err := s.Load("example", v.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(back.Meta.Executables).To(Equal([]string{"commands/run.sh"}))
	})

	It("records the execute bits of a version cached before they were recorded when it is put again", func() {
		files := map[string]string{"lola.yml": "name: example\n", "hooks/build.sh": "#!/bin/sh\n", "commands/a.md": "A"}
		put := func() store.Version {
			v, err := s.Put(store.PutRequest{Staged: stage(files), FallbackName: "example", Source: "/src/example", Type: "folder", Now: now,
				Executables: []string{"hooks/build.sh"}})
			Expect(err).NotTo(HaveOccurred())
			return v
		}
		v := put()
		// The same version as a cache from before execute bits were recorded holds it: on a
		// platform with execute bits its id is the same, but .lola-origin lists none.
		origin := filepath.Join(filepath.Dir(v.ContentDir), ".lola-origin")
		b, err := os.ReadFile(origin)
		Expect(err).NotTo(HaveOccurred())
		old := regexp.MustCompile(`(?m)^executables:\n(\s+- .*\n)+`).ReplaceAll(b, nil)
		Expect(old).NotTo(Equal(b))
		Expect(os.WriteFile(origin, old, 0o600)).To(Succeed())
		stale, err := s.Load("example", v.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(stale.Meta.Executables).To(BeEmpty())

		Expect(put().ID).To(Equal(v.ID))
		back, err := s.Load("example", v.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(back.Meta.Executables).To(Equal([]string{"hooks/build.sh"}))
		Expect(store.Digest(back.ContentDir, back.Meta.Executables)).To(Equal(back.Meta.Digest))
	})

	It("the digest of a fixture module is stable", func() {
		// A golden value (Plan 6a §4.4, §14 spec 20): it changes only when the digest's input, a
		// fetch or module.Normalize changes the bytes for unchanged content, which bumps
		// manifest.Version.
		v, err := s.Put(store.PutRequest{Staged: stage(goldenFixture), FallbackName: "example", Source: "/src/example", Type: "folder", Now: now, Executables: []string{"hooks/build.sh"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Meta.Digest).To(Equal(goldenDigest))
	})

	Describe("fetched content", func() {
		var tmp string
		ctx := context.Background()
		BeforeEach(func() { tmp = filepath.Dir(p.Home) })
		putFetched := func(spec source.Spec) store.Version {
			f, err := source.Fetch(ctx, spec, p.Staging)
			Expect(err).NotTo(HaveOccurred())
			v, err := s.Put(store.PutRequest{Staged: f.Dir, FallbackName: f.Name, Source: spec.Location, Type: string(spec.Kind),
				Ref: spec.Ref, Commit: f.Commit, Executables: f.Executables, Now: now})
			Expect(err).NotTo(HaveOccurred())
			return v
		}
		serveGit := func(files map[string]string, executable ...string) string {
			work := filepath.Join(tmp, "work")
			writeFiles(work, files)
			for _, x := range executable {
				Expect(os.Chmod(filepath.Join(work, x), 0o755)).To(Succeed())
			}
			gitIn(work, "init", "-q", "-b", "main")
			gitIn(work, "add", "-A")
			gitIn(work, "commit", "-q", "-m", "init")
			gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "example.git"))
			return testenv.ServeGit(filepath.Join(tmp, "srv")) + "/example.git"
		}

		It("the digest does not depend on the platform's execute bits", func() {
			url := serveGit(goldenFixture, "hooks/build.sh")
			archive := filepath.Join(tmp, "example.tgz")
			writeTarGz(archive, goldenFixture, "hooks/build.sh")
			for _, spec := range []source.Spec{{Kind: source.KindGit, Location: url}, {Kind: source.KindArchive, Location: archive}} {
				v := putFetched(spec)
				Expect(v.Meta.Digest).To(Equal(goldenDigest), string(spec.Kind))
				// A platform without execute bits: the content as Windows would hold it.
				Expect(filepath.WalkDir(v.ContentDir, func(p string, d os.DirEntry, err error) error {
					if err != nil || d.IsDir() {
						return err
					}
					return os.Chmod(p, 0o644)
				})).To(Succeed())
				Expect(store.Digest(v.ContentDir, v.Meta.Executables)).To(Equal(goldenDigest), string(spec.Kind))
				Expect(s.Remove("example")).To(BeTrue())
			}
		})

		It("git content does not depend on line-ending settings", func() {
			files := map[string]string{"lola.yml": "name: example\n", "commands/a.md": "A\nB\n", ".gitattributes": "* text eol=crlf\n"}
			url := serveGit(files)
			plain := putFetched(source.Spec{Kind: source.KindGit, Location: url})
			Expect(s.Remove("example")).To(BeTrue())
			global := filepath.Join(tmp, "gitconfig")
			Expect(os.WriteFile(global, []byte("[core]\n\tautocrlf = true\n"), 0o644)).To(Succeed())
			GinkgoT().Setenv("GIT_CONFIG_GLOBAL", global)
			crlf := putFetched(source.Spec{Kind: source.KindGit, Location: url})
			Expect(os.ReadFile(filepath.Join(crlf.ContentDir, "commands", "a.md"))).To(Equal([]byte("A\nB\n")))
			Expect(crlf.Meta.Digest).To(Equal(plain.Meta.Digest))
		})
	})

	It("keeps one head per ref so projects can pin different refs", func() {
		v1, err := put(map[string]string{"commands/a.md": "v1"}, "v1")
		Expect(err).NotTo(HaveOccurred())
		v2, err := put(map[string]string{"commands/a.md": "v2"}, "v2")
		Expect(err).NotTo(HaveOccurred())
		h1, ok, err := s.Head("example", "v1")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(h1.ID).To(Equal(v1.ID))
		h2, _, _ := s.Head("example", "v2")
		Expect(h2.ID).To(Equal(v2.ID))
		_, ok, err = s.Head("example", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
	})

	It("moves a head on new content and keeps the old version", func() {
		v1, _ := put(map[string]string{"commands/a.md": "old"}, "")
		v2, err := put(map[string]string{"commands/a.md": "new"}, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(v2.ID).NotTo(Equal(v1.ID))
		h, _, _ := s.Head("example", "")
		Expect(h.ID).To(Equal(v2.ID))
		Expect(v1.ContentDir).To(BeADirectory())
	})

	It("records the commit each fetch resolved in its head, not the version's first", func() {
		c1, c2 := strings.Repeat("1", 40), strings.Repeat("2", 40)
		v1, err := s.Put(store.PutRequest{Staged: stage(map[string]string{"commands/a.md": "same"}), FallbackName: "example",
			Source: "/src/example", Type: "git", Ref: "main", Commit: c1, Now: now})
		Expect(err).NotTo(HaveOccurred())
		Expect(v1.Commit).To(Equal(c1))
		v2, err := s.Put(store.PutRequest{Staged: stage(map[string]string{"commands/a.md": "same"}), FallbackName: "example",
			Source: "/src/example", Type: "git", Ref: "v1", Commit: c2, Now: now})
		Expect(err).NotTo(HaveOccurred())
		Expect(v2.ID).To(Equal(v1.ID))
		Expect(v2.Commit).To(Equal(c2))
		Expect(v2.Meta.Commit).To(Equal(c1), ".lola-origin keeps the first fetch's commit")
		h, ok, err := s.Head("example", "v1")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(h.Commit).To(Equal(c2))
		o, _, err := s.Origin("example")
		Expect(err).NotTo(HaveOccurred())
		Expect(o.Heads).To(ConsistOf(store.Head{Ref: "main", Digest: v1.ID, Commit: c1}, store.Head{Ref: "v1", Digest: v1.ID, Commit: c2}))
	})

	It("sets a head only where there is none when a commit asks for that", func() {
		v1, err := put(map[string]string{"commands/a.md": "old"}, "main")
		Expect(err).NotTo(HaveOccurred())
		st, err := s.Prepare(store.PutRequest{Staged: stage(map[string]string{"commands/a.md": "new"}), FallbackName: "example",
			Source: "/src/example", Type: "folder", Ref: "main", Now: now})
		Expect(err).NotTo(HaveOccurred())
		v2, err := s.Commit(st, true)
		Expect(err).NotTo(HaveOccurred())
		h, _, _ := s.Head("example", "main")
		Expect(h.ID).To(Equal(v1.ID), "an existing head never moves")
		Expect(v2.ContentDir).To(BeADirectory(), "the version is cached")
		st, err = s.Prepare(store.PutRequest{Staged: stage(map[string]string{"commands/a.md": "new"}), FallbackName: "example",
			Source: "/src/example", Type: "folder", Ref: "dev", Now: now})
		Expect(err).NotTo(HaveOccurred())
		_, err = s.Commit(st, true)
		Expect(err).NotTo(HaveOccurred())
		h, _, _ = s.Head("example", "dev")
		Expect(h.ID).To(Equal(v2.ID), "a ref without a head gets one")
	})

	Describe("Prepare and Commit", func() {
		It("prepares content in staging without touching the cache, and commits a copy of it", func() {
			staged := stage(map[string]string{"commands/a.md": "A", "bin/x.sh": "x"})
			st, err := s.Prepare(store.PutRequest{Staged: staged, FallbackName: "example", Source: "/src/example", Type: "git",
				Ref: "main", Commit: strings.Repeat("1", 40), Executables: []string{"bin/x.sh"}, Now: now})
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Version.ContentDir).To(Equal(staged))
			want, err := store.Digest(staged, []string{"bin/x.sh"})
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Version.Meta.Digest).To(Equal(want))
			Expect(st.Version.ID).To(Equal(strings.TrimPrefix(st.Version.Meta.Digest, "sha256:")[:12]))
			Expect(st.Version.Commit).To(Equal(strings.Repeat("1", 40)))
			Expect(st.Version.Module.Commands).To(HaveLen(1))
			Expect(filepath.Join(p.Store, "example")).NotTo(BeAnExistingFile(), "nothing reaches the cache")
			Expect(s.Names()).To(BeEmpty())

			v, err := s.Commit(st, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(v.ID).To(Equal(st.Version.ID))
			Expect(v.Meta.Digest).To(Equal(st.Version.Meta.Digest))
			Expect(v.ContentDir).To(Equal(filepath.Join(p.Store, "example", "versions", v.ID, "content")))
			Expect(os.ReadFile(filepath.Join(staged, "commands/a.md"))).To(Equal([]byte("A")), "the run keeps its staged tree")
			h, ok, err := s.Head("example", "main")
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(h.ID).To(Equal(v.ID))
			Expect(h.Commit).To(Equal(strings.Repeat("1", 40)))
			got, ok, err := s.Pinned("example", v.Meta.Digest, filepath.Dir(staged))
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue(), "the cached copy reads back as the version prepared")
			Expect(got.Meta.Executables).To(Equal([]string{"bin/x.sh"}))
		})

		It("checks the same-name rule again when it commits", func() {
			st, err := s.Prepare(store.PutRequest{Staged: stage(map[string]string{"commands/a.md": "A"}), FallbackName: "example",
				Source: "/src/example", Type: "folder", Now: now})
			Expect(err).NotTo(HaveOccurred())
			// Another process caches the name from another source between prepare and commit.
			_, err = s.Put(store.PutRequest{Staged: stage(map[string]string{"commands/a.md": "B"}), FallbackName: "example",
				Source: "/elsewhere/example", Type: "folder", Now: now})
			Expect(err).NotTo(HaveOccurred())
			before, _, err := s.Origin("example")
			Expect(err).NotTo(HaveOccurred())

			_, err = s.Commit(st, false)
			Expect(err).To(MatchError(ContainSubstring(`a different module named "example" is already in your cache (from /elsewhere/example)`)))
			Expect(errs.Hints(err)).To(ContainElement("remove the cached one first: lola cache rm example"))
			after, _, err := s.Origin("example")
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before))
			Expect(filepath.Join(p.Store, "example", "versions", st.Version.ID)).NotTo(BeAnExistingFile())
		})

		It("refuses content that declares another module than the one asked for", func() {
			_, err := s.Prepare(store.PutRequest{Staged: stage(map[string]string{"lola.yml": "name: beta\n", "commands/a.md": "A"}),
				FallbackName: "example", Name: "example", Source: "/src/example", Type: "folder", Now: now})
			Expect(err).To(MatchError(`example: its source now declares module "beta"`))
		})
	})

	Describe("Origin.LocalGone", func() {
		origin := func(source, typ string) store.Origin {
			GinkgoHelper()
			_, err := s.Put(store.PutRequest{Staged: stage(map[string]string{"commands/a.md": "A"}), FallbackName: "example", Source: source, Type: typ, Now: now})
			Expect(err).NotTo(HaveOccurred())
			o, found, err := s.Origin("example")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			return o
		}
		It("is true for a local folder that is missing", func() {
			Expect(origin(filepath.Join(p.Staging, "nowhere"), "folder").LocalGone()).To(BeTrue())
		})
		It("is false for a local folder that exists", func() {
			Expect(os.MkdirAll(p.Staging, 0o700)).To(Succeed())
			Expect(origin(p.Staging, "folder").LocalGone()).To(BeFalse())
		})
		It("is false for a remote source", func() {
			Expect(origin("https://example.invalid/example.git", "git").LocalGone()).To(BeFalse())
		})
		It("is false for a symbolic link that dangles: the link exists", func() {
			link := filepath.Join(p.Staging, "dangling")
			Expect(os.MkdirAll(p.Staging, 0o700)).To(Succeed())
			Expect(os.Symlink(filepath.Join(p.Staging, "nowhere"), link)).To(Succeed())
			Expect(origin(link, "folder").LocalGone()).To(BeFalse())
		})
		It("is false when the location cannot be examined", func() {
			Expect(os.MkdirAll(p.Staging, 0o700)).To(Succeed())
			file := filepath.Join(p.Staging, "file")
			Expect(os.WriteFile(file, nil, 0o600)).To(Succeed())
			Expect(origin(filepath.Join(file, "below"), "folder").LocalGone()).To(BeFalse(), "ENOTDIR")
		})
		It("Commit refuses to replace a remote origin even when asked to", func() {
			origin("https://example.invalid/example.git", "git")
			st, err := s.Prepare(store.PutRequest{Staged: stage(map[string]string{"commands/b.md": "B"}), FallbackName: "example",
				Source: "/src/example", Type: "folder", Now: now, ReplaceGoneOrigin: true})
			Expect(err).NotTo(HaveOccurred())
			_, err = s.Commit(st, false)
			Expect(err).To(MatchError(ContainSubstring(`a different module named "example" is already in your cache`)))
		})
	})

	Describe("Pinned", func() {
		var run string
		BeforeEach(func() {
			Expect(os.MkdirAll(p.Staging, 0o700)).To(Succeed())
			var err error
			run, err = os.MkdirTemp(p.Staging, "run-*")
			Expect(err).NotTo(HaveOccurred())
		})

		It("snapshots a cached version into the run's directory and loads it from there", func() {
			v, err := s.Put(store.PutRequest{Staged: stage(map[string]string{"commands/a.md": "A", "bin/x.sh": "x"}), FallbackName: "example",
				Source: "/src/example", Type: "folder", Executables: []string{"bin/x.sh"}, Now: now})
			Expect(err).NotTo(HaveOccurred())
			got, ok, err := s.Pinned("example", v.Meta.Digest, run)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(got.ID).To(Equal(v.ID))
			Expect(got.Meta).To(Equal(v.Meta))
			Expect(filepath.Dir(got.ContentDir)).To(Equal(run))
			Expect(os.ReadFile(filepath.Join(got.ContentDir, "commands/a.md"))).To(Equal([]byte("A")))
			Expect(got.Module.Commands).To(HaveLen(1))
		})

		It("reports a version the cache does not hold", func() {
			_, ok, err := s.Pinned("example", "sha256:"+strings.Repeat("a", 64), run)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeFalse())
		})

		It("refuses a cached version whose content or record no longer matches the pin", func() {
			v, err := put(map[string]string{"commands/a.md": "A"}, "")
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(filepath.Join(v.ContentDir, "commands/a.md"), []byte("tampered"), 0o644)).To(Succeed())
			_, _, err = s.Pinned("example", v.Meta.Digest, run)
			Expect(err).To(MatchError(ContainSubstring("cached version example/" + v.ID + " does not match its digest")))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("lola cache rm example")))
			entries, err := os.ReadDir(run)
			Expect(err).NotTo(HaveOccurred())
			Expect(entries).To(BeEmpty(), "the snapshot is removed")

			Expect(os.WriteFile(filepath.Join(v.ContentDir, "commands/a.md"), []byte("A"), 0o644)).To(Succeed())
			other := v.Meta.Digest[:len(v.Meta.Digest)-1] + "0"
			if other == v.Meta.Digest {
				other = v.Meta.Digest[:len(v.Meta.Digest)-1] + "1"
			}
			_, _, err = s.Pinned("example", other, run)
			Expect(err).To(MatchError(ContainSubstring("does not match its digest")))
		})

		It("refuses a cached version holding a symbolic link as one that does not match its digest", func() {
			v, err := put(map[string]string{"commands/a.md": "A"}, "")
			Expect(err).NotTo(HaveOccurred())
			Expect(os.Symlink("a.md", filepath.Join(v.ContentDir, "commands", "b.md"))).To(Succeed())
			_, _, err = s.Pinned("example", v.Meta.Digest, run)
			Expect(err).To(MatchError(ContainSubstring("cached version example/" + v.ID + " does not match its digest")))
			Expect(err.Error()).NotTo(ContainSubstring(run))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("lola cache rm example")))
			entries, err := os.ReadDir(run)
			Expect(err).NotTo(HaveOccurred())
			Expect(entries).To(BeEmpty(), "the snapshot is removed")
		})

		It("refuses a digest that is not one", func() {
			_, _, err := s.Pinned("example", "sha256:../../x", run)
			Expect(err).To(MatchError(ContainSubstring("is not a digest")))
		})
	})

	It("reuses the version directory for identical content", func() {
		v1, _ := put(map[string]string{"commands/a.md": "same"}, "")
		v2, err := put(map[string]string{"commands/a.md": "same"}, "main")
		Expect(err).NotTo(HaveOccurred())
		Expect(v2.ID).To(Equal(v1.ID))
		entries, _ := os.ReadDir(filepath.Join(p.Store, "example", "versions"))
		Expect(entries).To(HaveLen(1))
		staging, _ := os.ReadDir(p.Staging)
		Expect(staging).To(BeEmpty())
	})

	It("refuses a same-named module from a different origin", func() {
		_, err := put(map[string]string{"commands/a.md": "x"}, "")
		Expect(err).NotTo(HaveOccurred())
		_, err = s.Put(store.PutRequest{Staged: stage(map[string]string{"commands/a.md": "y"}), FallbackName: "example", Source: "/elsewhere/example", Type: "folder", Now: now})
		Expect(err).To(MatchError(ContainSubstring("different module named \"example\"")))
		Expect(errs.Hints(err)).To(ContainElement("remove the cached one first: lola cache rm example"))
	})

	It("never prints a control byte of either origin raw when refusing a same-named module", func() {
		_, err := s.Put(store.PutRequest{Staged: stage(map[string]string{"commands/a.md": "x"}), FallbackName: "example",
			Source: "/src\x1b]0;pwned\x07/example", Path: "sub\x1b[31m", Type: "folder", Now: now})
		Expect(err).NotTo(HaveOccurred())
		_, err = s.Put(store.PutRequest{Staged: stage(map[string]string{"commands/a.md": "y"}), FallbackName: "example",
			Source: "/elsewhere\x1b[2J/example", Type: "folder", Now: now})
		Expect(err).To(MatchError(`a different module named "example" is already in your cache (from /src?]0;pwned?/example (path sub?[31m)); ` +
			"this one is from /elsewhere?[2J/example"))
	})

	It("stores nothing when the staged dir is not a module", func() {
		_, err := put(map[string]string{"README.md": "x"}, "")
		Expect(err).To(MatchError(ContainSubstring("no module found")))
		names, err := s.Names()
		Expect(err).NotTo(HaveOccurred())
		Expect(names).To(BeEmpty())
	})

	It("serializes concurrent puts of the same content", func() {
		var wg sync.WaitGroup
		errsCh := make(chan error, 4)
		dirs := make([]string, 4)
		for i := range dirs {
			dirs[i] = stage(map[string]string{"commands/a.md": "same"})
		}
		for _, d := range dirs {
			wg.Add(1)
			go func(d string) {
				defer wg.Done()
				defer GinkgoRecover()
				_, err := s.Put(store.PutRequest{Staged: d, FallbackName: "example", Source: "/src/example", Type: "folder", Now: now})
				errsCh <- err
			}(d)
		}
		wg.Wait()
		close(errsCh)
		for err := range errsCh {
			Expect(err).NotTo(HaveOccurred())
		}
		entries, _ := os.ReadDir(filepath.Join(p.Store, "example", "versions"))
		Expect(entries).To(HaveLen(1))
	})

	It("lists names sorted and removes every version", func() {
		_, _ = put(map[string]string{"commands/a.md": "x"}, "")
		_, err := s.Put(store.PutRequest{Staged: stage(map[string]string{"lola.yml": "name: alpha\n", "commands/a.md": "x"}), Source: "/src/alpha", Type: "folder", Now: now})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Names()).To(Equal([]string{"alpha", "example"}))
		Expect(s.Remove("example")).To(BeTrue())
		Expect(s.Remove("example")).To(BeFalse())
		Expect(s.Names()).To(Equal([]string{"alpha"}))
	})
})

var _ = Describe("Digest", func() {
	var dir string
	BeforeEach(func() {
		dir = testenv.Isolate()
		Expect(os.WriteFile(filepath.Join(dir, "a"), []byte("x"), 0o644)).To(Succeed())
	})

	It("is stable and sensitive to content, path and the recorded execute bits, never to the file system's", func() {
		d1, err := store.Digest(dir, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(d1).To(MatchRegexp(`^sha256:[0-9a-f]{64}$`))
		Expect(store.Digest(dir, nil)).To(Equal(d1))
		Expect(os.Chmod(filepath.Join(dir, "a"), 0o755)).To(Succeed())
		Expect(store.Digest(dir, nil)).To(Equal(d1), "a file system's execute bit is not read")
		x, err := store.Digest(dir, []string{"a"})
		Expect(err).NotTo(HaveOccurred())
		Expect(x).NotTo(Equal(d1))
		Expect(os.Chmod(filepath.Join(dir, "a"), 0o644)).To(Succeed())
		Expect(store.Digest(dir, []string{"a"})).To(Equal(x), "nor its absence")
		Expect(os.Rename(filepath.Join(dir, "a"), filepath.Join(dir, "b"))).To(Succeed())
		Expect(store.Digest(dir, nil)).NotTo(Equal(d1))
	})

	It("refuses symlinks", func() {
		Expect(os.Symlink("a", filepath.Join(dir, "l"))).To(Succeed())
		_, err := store.Digest(dir, nil)
		Expect(err).To(MatchError(ContainSubstring("symbolic link")))
	})
})
