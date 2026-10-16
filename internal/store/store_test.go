package store_test

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

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

	It("refuses names and version ids that are not lola's before using them in a path", func() {
		_, _, err := s.Origin("../outside")
		Expect(err).To(MatchError(ContainSubstring("module name")))
		_, err = s.Load("../outside", "0123456789ab")
		Expect(err).To(MatchError(ContainSubstring("module name")))
		_, err = s.Load("example", "../versions")
		Expect(err).To(MatchError(ContainSubstring("is invalid")))
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
		Expect(store.Digest(v.ContentDir)).To(Equal(v.Meta.Digest), "the digest covers the normalized content")
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

	It("is stable and sensitive to content, path and executability", func() {
		d1, err := store.Digest(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(d1).To(MatchRegexp(`^sha256:[0-9a-f]{64}$`))
		Expect(store.Digest(dir)).To(Equal(d1))
		Expect(os.Chmod(filepath.Join(dir, "a"), 0o600)).To(Succeed())
		Expect(store.Digest(dir)).To(Equal(d1), "owner-only mode changes are normalized away")
		Expect(os.Chmod(filepath.Join(dir, "a"), 0o755)).To(Succeed())
		Expect(store.Digest(dir)).NotTo(Equal(d1))
		Expect(os.Chmod(filepath.Join(dir, "a"), 0o644)).To(Succeed())
		Expect(os.Rename(filepath.Join(dir, "a"), filepath.Join(dir, "b"))).To(Succeed())
		Expect(store.Digest(dir)).NotTo(Equal(d1))
	})

	It("refuses symlinks", func() {
		Expect(os.Symlink("a", filepath.Join(dir, "l"))).To(Succeed())
		_, err := store.Digest(dir)
		Expect(err).To(MatchError(ContainSubstring("symbolic link")))
	})
})
