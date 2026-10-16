package trust_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
	"github.com/trevor-vaughan/lola-go/internal/trust"
)

var _ = Describe("trust record", func() {
	var (
		tmp, root string
		p         paths.Paths
		s         *trust.Store
	)
	no := false
	man := &manifest.Manifest{Targets: []string{"claude-code"}, Modules: []manifest.Entry{
		{Name: "zeta", Source: "https://example.com/zeta.git", Ref: "v1", Commit: strings.Repeat("a", 40), Digest: "sha256:" + strings.Repeat("b", 64)},
		{Name: "alpha", Source: "mods/alpha", Targets: []string{"opencode"}, Rules: &no},
	}}
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		s = trust.New(p)
		root = filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(root, 0o755)).To(Succeed())
	})
	hints := errs.Hints

	It("has no record for a project it never saw, so nothing is covered", func() {
		r, err := s.Load(root)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.View).To(Equal(trust.View{}))
		Expect(r.View.Covers(man.Modules[0])).To(BeFalse())
	})

	It("saves a project's view sorted by name with its hash, private to the user, and loads it back", func() {
		Expect(s.Save(root, trust.ViewOf(man))).To(Succeed())
		r, err := s.Load(root)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.View.Modules).To(HaveExactElements(HaveField("Name", "alpha"), HaveField("Name", "zeta")))
		for _, e := range man.Modules {
			Expect(r.View.Covers(e)).To(BeTrue(), e.Name)
		}
		Expect(r.View.Targets).To(Equal([]string{"claude-code"}))

		var f struct {
			Version  int `json:"version"`
			Projects map[string]struct {
				SHA256 string     `json:"sha256"`
				View   trust.View `json:"view"`
			} `json:"projects"`
		}
		b, err := os.ReadFile(p.Trust)
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal(b, &f)).To(Succeed())
		Expect(f.Version).To(Equal(1))
		key, err := paths.ProjectKey(root)
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Projects).To(HaveKey(key))
		hash, err := trust.ViewOf(man).Hash()
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Projects[key].SHA256).To(Equal(hash))
		if runtime.GOOS != "windows" {
			fi, err := os.Stat(p.Trust)
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		}
	})

	It("hashes the canonical view: stable across entry order, and empty values omitted", func() {
		h1, err := trust.ViewOf(man).Hash()
		Expect(err).NotTo(HaveOccurred())
		swapped := &manifest.Manifest{Targets: man.Targets, Modules: []manifest.Entry{man.Modules[1], man.Modules[0]}}
		h2, err := trust.ViewOf(swapped).Hash()
		Expect(err).NotTo(HaveOccurred())
		Expect(h2).To(Equal(h1))
		empty, err := trust.ViewOf(&manifest.Manifest{Targets: []string{}}).Hash()
		Expect(err).NotTo(HaveOccurred())
		Expect(empty).To(Equal("44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"), "sha256 of {}")
	})

	DescribeTable("covers an entry only when every view field equals the record's",
		func(change func(e *manifest.Entry)) {
			Expect(s.Save(root, trust.ViewOf(man))).To(Succeed())
			r, err := s.Load(root)
			Expect(err).NotTo(HaveOccurred())
			e := man.Modules[1]
			e.Targets = append([]string(nil), e.Targets...)
			change(&e)
			Expect(r.View.Covers(e)).To(BeFalse())
		},
		Entry("source", func(e *manifest.Entry) { e.Source = "mods/other" }),
		Entry("ref", func(e *manifest.Entry) { e.Ref = "main" }),
		Entry("path", func(e *manifest.Entry) { e.Path = "sub" }),
		Entry("commit", func(e *manifest.Entry) { e.Commit = strings.Repeat("c", 40) }),
		Entry("digest", func(e *manifest.Entry) { e.Digest = "sha256:" + strings.Repeat("d", 64) }),
		Entry("targets", func(e *manifest.Entry) { e.Targets = []string{"claude-code"} }),
		Entry("targets removed", func(e *manifest.Entry) { e.Targets = nil }),
		Entry("rules cleared", func(e *manifest.Entry) { e.Rules = nil }),
		Entry("rules turned on", func(e *manifest.Entry) { yes := true; e.Rules = &yes }),
		Entry("name", func(e *manifest.Entry) { e.Name = "beta" }),
	)

	Describe("Refresh", func() {
		entry := func(name, digest string) trust.Entry {
			return trust.Entry{Name: name, Source: "https://example.com/" + name + ".git", Digest: digest}
		}
		view := func(es ...trust.Entry) trust.View { return trust.View{Targets: []string{"claude-code"}, Modules: es} }

		It("trusts exactly the fields lola's write changed", func() {
			rec := view(entry("x", "d1"))
			pulledX := entry("x", "d1")
			pulledX.Ref = "pulled"                    // a pulled field change on x
			loaded := view(pulledX, entry("y", "dy")) // and a pulled entry y
			saved := view(pulledX, entry("y", "dy"), entry("z", "dz"))
			saved.Modules[0].Digest = "d2" // lola re-pinned x and added z
			got := trust.Refresh(rec, loaded, saved)
			Expect(got.Find("z")).To(Equal(new(entry("z", "dz"))), "an added entry is recorded whole")
			Expect(got.Find("y")).To(BeNil(), "a pulled entry lola left alone is not recorded")
			Expect(got.Find("x")).To(Equal(new(entry("x", "d2"))), "the re-pin is recorded, the pulled ref is not")
			x := manifest.Entry{Name: "x", Source: pulledX.Source, Ref: "pulled", Digest: "d2"}
			Expect(got.Covers(x)).To(BeFalse(), "a write that leaves a pulled field unchanged does not cover it")
		})

		It("drops a removed entry, takes changed top-level targets, and keeps unchanged ones' trust state", func() {
			rec := trust.View{Targets: []string{"claude-code"}, Modules: []trust.Entry{entry("x", "d1"), entry("w", "dw")}}
			loaded := trust.View{Targets: []string{"opencode"}, Modules: []trust.Entry{entry("x", "d1")}}
			got := trust.Refresh(rec, loaded, trust.View{Targets: []string{"opencode"}})
			Expect(got.Modules).To(ConsistOf(entry("w", "dw")), "x was removed; w was not part of the write")
			Expect(got.Targets).To(Equal([]string{"claude-code"}), "pulled targets the write left alone stay untrusted")
			got = trust.Refresh(rec, loaded, trust.View{Targets: []string{"cursor"}})
			Expect(got.Targets).To(Equal([]string{"cursor"}))
		})

		It("returns exactly the saved view for a record equal to the manifest it loaded", func() {
			loaded := view(entry("x", "d1"), entry("y", "dy"))
			for _, saved := range []trust.View{
				view(entry("x", "d2"), entry("y", "dy")),
				view(entry("y", "dy"), entry("z", "dz")),
				{Targets: []string{"opencode"}, Modules: []trust.Entry{entry("x", "d1")}},
				loaded,
			} {
				Expect(trust.Refresh(loaded, loaded, saved)).To(Equal(saved))
			}
		})

		It("records nothing for a changed entry the record does not hold", func() {
			loaded := view(entry("x", "d1"))
			saved := view(entry("x", "d2"))
			Expect(trust.Refresh(trust.View{}, loaded, saved).Modules).To(BeEmpty())
		})
	})

	Describe("Check", func() {
		no := false
		pinned := manifest.Entry{Name: "kit", Source: "https://github.com/acme/kit.git", Ref: "v1", Commit: strings.Repeat("a", 40),
			Digest: "sha256:" + strings.Repeat("b", 64)}
		folder := manifest.Entry{Name: "local", Source: "mods/local", Targets: []string{"opencode"}, Rules: &no}
		m := func(targets []string, es ...manifest.Entry) trust.View {
			return trust.ViewOf(&manifest.Manifest{Targets: targets, Modules: es})
		}
		claude := []string{"claude-code"}

		It("calls a project without a record new, listing every entry as added with each field it has", func() {
			rep := trust.Check(&trust.Record{}, m(claude, pinned, folder))
			Expect(rep.Status).To(Equal(trust.StatusNew))
			Expect(rep.Targets).To(Equal(&trust.FieldChange{Field: "targets", From: nil, To: []string{"claude-code"}}))
			Expect(rep.Changes).To(HaveExactElements(
				trust.Change{Module: "kit", Kind: trust.ChangeAdded, ContentFixed: true, Fields: []trust.FieldChange{
					{Field: "source", To: pinned.Source}, {Field: "ref", To: "v1"}, {Field: "commit", To: pinned.Commit}, {Field: "digest", To: pinned.Digest}}},
				trust.Change{Module: "local", Kind: trust.ChangeAdded, Fields: []trust.FieldChange{
					{Field: "source", To: "mods/local"}, {Field: "targets", To: []string{"opencode"}}, {Field: "rules", To: false}}},
			))
		})

		It("has nothing to ask about an empty manifest in a project without a record", func() {
			Expect(trust.Check(&trust.Record{}, m(nil))).To(Equal(trust.Report{Status: trust.StatusTrusted, Changes: []trust.Change{}}))
			Expect(trust.Check(&trust.Record{}, m(claude)).Status).To(Equal(trust.StatusNew), "top-level targets are a change")
		})

		It("calls a manifest equal to its record trusted, with no changes", func() {
			rep := trust.Check(&trust.Record{View: m(claude, folder, pinned), Exists: true}, m(claude, pinned, folder))
			Expect(rep).To(Equal(trust.Report{Status: trust.StatusTrusted, Changes: []trust.Change{}}))
			Expect(trust.Check(&trust.Record{Exists: true}, m(nil)).Status).To(Equal(trust.StatusTrusted), "a record of an empty manifest")
		})

		It("lists each changed field with typed values, removed entries and changed targets", func() {
			moved := pinned
			moved.Source, moved.Commit, moved.Digest = "https://git.example.net/acme/kit.git", "", ""
			local := folder
			local.Rules, local.Targets = nil, nil
			rec := &trust.Record{View: m(claude, pinned, folder, manifest.Entry{Name: "old", Source: "mods/old"}), Exists: true}
			rep := trust.Check(rec, m([]string{"claude-code", "opencode"}, moved, local))
			Expect(rep.Status).To(Equal(trust.StatusChanged))
			Expect(rep.Targets).To(Equal(&trust.FieldChange{Field: "targets", From: []string{"claude-code"}, To: []string{"claude-code", "opencode"}}))
			Expect(rep.Changes).To(HaveExactElements(
				trust.Change{Module: "kit", Kind: trust.ChangeChanged, HostChanged: true, Fields: []trust.FieldChange{
					{Field: "source", From: pinned.Source, To: moved.Source}, {Field: "commit", From: pinned.Commit}, {Field: "digest", From: pinned.Digest}}},
				trust.Change{Module: "local", Kind: trust.ChangeChanged, Fields: []trust.FieldChange{
					{Field: "targets", From: []string{"opencode"}}, {Field: "rules", From: false}}},
				trust.Change{Module: "old", Kind: trust.ChangeRemoved, Fields: []trust.FieldChange{{Field: "source", From: "mods/old"}}},
			))
			b, err := json.Marshal(rep.Changes[1].Fields)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(Equal(`[{"field":"targets","from":["opencode"],"to":null},{"field":"rules","from":false,"to":null}]`))
		})

		It("marks a source moved between local and remote, or to another scheme, as a host change, and a path change on one host as none", func() {
			for from, to := range map[string]string{
				"mods/kit":                        "https://github.com/acme/kit.git",
				"https://github.com/acme/kit.git": "ssh://git@github.com/acme/kit.git",
				"git@github.com:acme/kit.git":     "git@gitlab.com:acme/kit.git",
			} {
				rec := &trust.Record{View: m(nil, manifest.Entry{Name: "kit", Source: from}), Exists: true}
				Expect(trust.Check(rec, m(nil, manifest.Entry{Name: "kit", Source: to})).Changes[0].HostChanged).To(BeTrue(), from+" → "+to)
			}
			rec := &trust.Record{View: m(nil, manifest.Entry{Name: "kit", Source: "https://github.com/acme/kit.git"}), Exists: true}
			Expect(trust.Check(rec, m(nil, manifest.Entry{Name: "kit", Source: "https://github.com/evil/kit.git"})).Changes[0].HostChanged).To(BeFalse())
		})

		It("marks a swap between git's scp form and a path with a colon as a host change, since a folder may bear that name", func() {
			rec := &trust.Record{View: m(nil, manifest.Entry{Name: "kit", Source: "git@github.com:acme/kit.git"}), Exists: true}
			rep := trust.Check(rec, m(nil, manifest.Entry{Name: "kit", Source: "github.com:acme/kit.git"}))
			Expect(rep.Changes[0].HostChanged).To(BeTrue())
		})

		It("leaves the caller's module order alone", func() {
			b, a := manifest.Entry{Name: "b", Source: "mods/b"}, manifest.Entry{Name: "a", Source: "mods/a"}
			v := trust.View{Modules: []trust.Entry{trust.EntryOf(b), trust.EntryOf(a)}}
			rv := trust.View{Modules: []trust.Entry{trust.EntryOf(b), trust.EntryOf(a)}}
			trust.Check(&trust.Record{View: rv, Exists: true}, v)
			Expect(v.Modules[0].Name).To(Equal("b"))
			Expect(rv.Modules[0].Name).To(Equal("b"))
		})

		It("finds a record through Load as existing, and a project without one as not", func() {
			r, err := s.Load(root)
			Expect(err).NotTo(HaveOccurred())
			Expect(r.Exists).To(BeFalse())
			Expect(s.Save(root, trust.View{})).To(Succeed())
			r, err = s.Load(root)
			Expect(err).NotTo(HaveOccurred())
			Expect(r.Exists).To(BeTrue())
			Expect(trust.Check(r, trust.View{}).Status).To(Equal(trust.StatusTrusted))
		})
	})

	It("finds a project's record through another spelling of its root", func() {
		link := filepath.Join(tmp, "link")
		Expect(os.Symlink(root, link)).To(Succeed())
		Expect(s.Save(link, trust.ViewOf(man))).To(Succeed())
		r, err := s.Load(root)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.View.Covers(man.Modules[0])).To(BeTrue())
		Expect(s.Save(root, trust.View{})).To(Succeed())
		b, err := os.ReadFile(p.Trust)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Count(string(b), `"sha256"`)).To(Equal(1), "one record per project")
	})

	It("changes only the project it saves: concurrent saves for other projects all survive", func() {
		other := filepath.Join(tmp, "other")
		Expect(s.Save(other, trust.ViewOf(man))).To(Succeed())
		var wg sync.WaitGroup
		for i := range 16 {
			wg.Add(1)
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				Expect(trust.New(p).Save(filepath.Join(tmp, fmt.Sprintf("p%d", i)), trust.ViewOf(man))).To(Succeed())
			}()
		}
		wg.Wait()
		for i := range 16 {
			r, err := s.Load(filepath.Join(tmp, fmt.Sprintf("p%d", i)))
			Expect(err).NotTo(HaveOccurred())
			Expect(r.View.Covers(man.Modules[0])).To(BeTrue(), "p%d", i)
		}
		r, err := s.Load(other)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.View.Covers(man.Modules[0])).To(BeTrue())
	})

	It("keeps records for project roots only", func() {
		_, err := s.Load("user")
		Expect(err).To(MatchError(ContainSubstring("project roots only")))
		Expect(s.Save("proj", trust.View{})).To(MatchError(ContainSubstring("project roots only")))
		Expect(p.Trust).NotTo(BeAnExistingFile())
	})

	DescribeTable("never reads or overwrites a trust file it cannot parse",
		func(content string, want string) {
			Expect(os.MkdirAll(filepath.Dir(p.Trust), 0o700)).To(Succeed())
			Expect(os.WriteFile(p.Trust, []byte(content), 0o600)).To(Succeed())
			_, err := s.Load(root)
			Expect(err).To(MatchError(ContainSubstring(want)))
			if strings.Contains(want, "newer lola") {
				Expect(hints(err)).To(ConsistOf("upgrade lola"))
			} else {
				Expect(hints(err)).To(ConsistOf(ContainSubstring("move it aside")))
			}
			Expect(s.Save(root, trust.ViewOf(man))).To(MatchError(ContainSubstring(want)))
			b, err := os.ReadFile(p.Trust)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(Equal(content), "never overwritten")
		},
		Entry("not JSON", "{not json", "invalid character"),
		Entry("an unknown key", `{"version":1,"projects":{},"extra":1}`, "unknown field"),
		Entry("data after the object", `{"version":1,"projects":{}} {}`, "data after the JSON object"),
		Entry("a record whose hash is not its view's", `{"version":1,"projects":{"/p":{"sha256":"00","view":{}}}}`, "does not match its sha256"),
		Entry("a newer version", `{"version":2,"projects":{}}`, "was written by a newer lola (trust version 2)"),
		Entry("a newer version with a key this lola does not know", `{"version":2,"projects":{},"profiles":{}}`,
			"was written by a newer lola (trust version 2)"),
		Entry("a negative version", `{"version":-1,"projects":{}}`, "version -1 is not a trust file version"),
		Entry("a version that is not a number", `{"version":"1","projects":{}}`, "cannot unmarshal string"),
		Entry("more than 4 MiB", `{"version":1,"projects":{}}`+strings.Repeat(" ", 4<<20), "larger than"),
	)

	It("reads version 0 as consent.json does, as the version this lola writes", func() {
		Expect(os.MkdirAll(filepath.Dir(p.Trust), 0o700)).To(Succeed())
		Expect(os.WriteFile(p.Trust, []byte(`{"version":0,"projects":{}}`), 0o600)).To(Succeed())
		_, err := s.Load(root)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Save(root, trust.ViewOf(man))).To(Succeed())
		r, err := s.Load(root)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.View.Covers(man.Modules[0])).To(BeTrue())
	})

	It("refuses a save that would grow the trust file past what lola reads, and leaves it as it was", func() {
		Expect(s.Save(filepath.Join(tmp, "other"), trust.ViewOf(man))).To(Succeed())
		before, err := os.ReadFile(p.Trust)
		Expect(err).NotTo(HaveOccurred())
		big := trust.View{}
		for i := range 20000 {
			big.Modules = append(big.Modules, trust.Entry{Name: fmt.Sprintf("m%05d", i), Source: "https://example.com/" + strings.Repeat("x", 200) + ".git"})
		}
		err = s.Save(root, big)
		Expect(err).To(MatchError(ContainSubstring("larger than")))
		Expect(hints(err)).To(ConsistOf(ContainSubstring("move it aside")))
		after, err := os.ReadFile(p.Trust)
		Expect(err).NotTo(HaveOccurred())
		Expect(after).To(Equal(before))
		_, err = s.Load(root)
		Expect(err).NotTo(HaveOccurred(), "the file stays readable")
	})

	It("checks the state directory and its locks directory before it takes the trust lock", func() {
		real := filepath.Join(tmp, "locks-elsewhere")
		Expect(os.MkdirAll(real, 0o700)).To(Succeed())
		Expect(os.MkdirAll(filepath.Dir(p.Trust), 0o700)).To(Succeed())
		Expect(os.Symlink(real, filepath.Dir(p.TrustLock))).To(Succeed())
		err := s.Save(root, trust.ViewOf(man))
		Expect(err).To(MatchError(ContainSubstring(filepath.Dir(p.TrustLock) + " is a symbolic link")))
		Expect(os.ReadDir(real)).To(BeEmpty(), "no lock file was created through the link")
		Expect(os.Remove(filepath.Dir(p.TrustLock))).To(Succeed())

		if runtime.GOOS == "windows" {
			return
		}
		Expect(os.Chmod(filepath.Dir(p.Trust), 0o707)).To(Succeed())
		DeferCleanup(os.Chmod, filepath.Dir(p.Trust), os.FileMode(0o700))
		Expect(s.Save(root, trust.ViewOf(man))).To(MatchError(ContainSubstring(filepath.Dir(p.Trust) + " is writable by others")))
		Expect(filepath.Dir(p.TrustLock)).NotTo(BeADirectory(), "nothing was created in it")
	})

	It("refuses a symbolic link as the trust file or its directory", func() {
		elsewhere := filepath.Join(tmp, "elsewhere.json")
		Expect(os.WriteFile(elsewhere, []byte(`{"version":1,"projects":{}}`), 0o600)).To(Succeed())
		Expect(os.MkdirAll(filepath.Dir(p.Trust), 0o700)).To(Succeed())
		Expect(os.Symlink(elsewhere, p.Trust)).To(Succeed())
		_, err := s.Load(root)
		Expect(err).To(MatchError(ContainSubstring(p.Trust + " is a symbolic link")))
		Expect(hints(err)).To(ConsistOf("replace it with a regular file"))
		Expect(s.Save(root, trust.View{})).To(MatchError(ContainSubstring("is a symbolic link")))

		Expect(os.RemoveAll(filepath.Dir(p.Trust))).To(Succeed())
		real := filepath.Join(tmp, "state-elsewhere")
		Expect(os.MkdirAll(real, 0o700)).To(Succeed())
		Expect(os.Symlink(real, filepath.Dir(p.Trust))).To(Succeed())
		_, err = s.Load(root)
		Expect(err).To(MatchError(ContainSubstring(filepath.Dir(p.Trust) + " is a symbolic link")))
		Expect(hints(err)).To(ConsistOf("replace it with a directory"))
		Expect(s.Save(root, trust.View{})).To(HaveOccurred())
		Expect(filepath.Join(real, "trust.json")).NotTo(BeAnExistingFile())
	})

	It("refuses a trust file or directory others can write", func() {
		if runtime.GOOS == "windows" {
			Skip("Windows permission bits do not describe access")
		}
		Expect(s.Save(root, trust.View{})).To(Succeed())
		Expect(os.Chmod(p.Trust, 0o620)).To(Succeed())
		_, err := s.Load(root)
		Expect(err).To(MatchError(ContainSubstring(p.Trust + " is writable by others")))
		Expect(hints(err)).To(ConsistOf("chmod go-w " + p.Trust))
		Expect(os.Chmod(p.Trust, 0o600)).To(Succeed())

		Expect(os.Chmod(filepath.Dir(p.Trust), 0o707)).To(Succeed())
		Expect(s.Save(root, trust.ViewOf(man))).To(MatchError(ContainSubstring(filepath.Dir(p.Trust) + " is writable by others")))
		Expect(os.Chmod(filepath.Dir(p.Trust), 0o700)).To(Succeed())
		r, err := s.Load(root)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.View.Modules).To(BeEmpty(), "the refused save wrote nothing")
	})
})
