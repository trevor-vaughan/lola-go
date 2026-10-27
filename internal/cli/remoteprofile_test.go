package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("remote profiles", func() {
	var (
		tmp            string
		p              paths.Paths
		commit, digest string
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		commit, digest = strings.Repeat("a", 40), "sha256:"+strings.Repeat("b", 64)
		GinkgoT().Chdir(tmp)
	})
	// keep writes a remote profile as lola profile --add does: profile.yml and its source.json.
	keep := func(name, content string) string {
		GinkgoHelper()
		dir := filepath.Join(p.RemoteProfiles, name)
		Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
		b, err := json.Marshal(profile.Origin{Version: 1, Source: "https://example.com/profiles.git", Ref: "v1", Commit: commit,
			Digest: profile.Digest([]byte(content))})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(dir, "source.json"), b, 0o600)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "profile.yml"), []byte(content), 0o600)).To(Succeed())
		return filepath.Join(dir, "profile.yml")
	}
	gopls := func() string {
		return "version: 1\nmodules:\n  - {name: gopls, source: https://example.com/gopls.git, commit: " + commit + ", digest: " + digest + "}\n"
	}

	It("lists remote profiles, says which a local one shadows, and shows one's source and pin", func() {
		path := keep("go", gopls())
		keep("docs", "version: 1\n")
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})

		r := lola("profile", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`user\s+docs\s+remote\s+off\s+` + regexpQuote(filepath.Join(p.RemoteProfiles, "docs", "profile.yml")) + `\n`))
		Expect(r.out).To(MatchRegexp(`user\s+go\s+remote\s+off\s+` + regexpQuote(path) + ` \(shadowed by user:go\)\n`))
		var v struct {
			Profiles []map[string]any `json:"profiles"`
		}
		Expect(json.Unmarshal([]byte(lola("profile", "--user", "--json").out), &v)).To(Succeed())
		Expect(v.Profiles).To(ContainElement(And(HaveKeyWithValue("name", "go"), HaveKeyWithValue("kind", "remote"),
			HaveKeyWithValue("source", "https://example.com/profiles.git"), HaveKeyWithValue("ref", "v1"), HaveKeyWithValue("commit", commit),
			HaveKeyWithValue("shadowed_by", "user:go"))))
		Expect(v.Profiles).To(ContainElement(And(HaveKeyWithValue("name", "go"), HaveKeyWithValue("kind", "user"),
			HaveKeyWithValue("shadowed_by", BeNil()), HaveKeyWithValue("commit", ""))))

		Expect(lola("__complete", "profile", "--update", "").out).To(HavePrefix("docs\ngo\n:"), "--update completes remote profiles")
		Expect(lola("__complete", "profile", "--update", "remote:go", "").out).To(HavePrefix("docs\n:"), "remote:go names go")
		Expect(lola("__complete", "profile", "--remove", "").out).To(HavePrefix("docs\ngo\n:"))
		Expect(lola("__complete", "profile", "--remove", "go", "").out).To(HavePrefix(":"), "--remove takes one name")
		Expect(lola("__complete", "profile", "--update", "--all", "").out).To(HavePrefix(":"), "--all takes no names")

		r = lola("profile", "--user", "remote:go")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("Profile remote:go (" + path + ")\nfrom https://example.com/profiles.git (ref v1)\ncommit " + commit + "\ndigest " +
			profile.Digest([]byte(gopls())) + "\nuser: off, shadowed by user:go; its modules:\n  gopls  not wanted here\n"))
		r = lola("profile", "--user", "go")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("Profile user:go ("), "a bare name is the local one")

		r = lola("profile", "--user", "remote:go", "--on")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: remote:go is shadowed by your profile user:go"))
		Expect(r.err).To(ContainSubstring("hint: use yours: lola profile --user user:go --on"))

		// Turned on before user:go existed, it is read as user:go, and still shows its own modules.
		led := ledger.New()
		led.Profiles = []ledger.Active{{Name: "remote:go", By: ledger.ByOn}}
		Expect(os.MkdirAll(filepath.Dir(p.UserLedger), 0o755)).To(Succeed())
		Expect(led.Save(p.UserLedger)).To(Succeed())
		r = lola("profile", "--user", "remote:go")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("\nuser: 1st of 1, read as user:go; its modules:\n  gopls  not wanted here\n"))
		Expect(json.Unmarshal([]byte(lola("profile", "--user", "remote:go", "--json").out), &v)).To(Succeed())
		Expect(v.Profiles[0]).To(HaveKeyWithValue("read_as", "user:go"))
	})

	It("says a project profile of its name would take an active remote profile's place, never that it is read as it", func() {
		keep("go", gopls())
		proj := filepath.Join(tmp, "proj")
		writeTree(proj, map[string]string{".lola/profiles/go.yml": "version: 1\n"})
		led := ledger.New()
		led.Profiles = []ledger.Active{{Name: "remote:go", By: ledger.ByOn}}
		Expect(led.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())
		GinkgoT().Chdir(proj)
		r := lola("profile", "--project", "remote:go")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).NotTo(ContainSubstring("read as"))
		Expect(r.out).To(ContainSubstring("\nproject (" + proj + "): 1st of 1, in effect; project:go would take its place if turned on; its modules:\n" +
			"  gopls  profile:remote:go\n"))
		Expect(r.out).To(ContainSubstring("→ to use project:go instead: lola profile --project project:go --on\n"))
		var v struct {
			Profiles []map[string]any `json:"profiles"`
		}
		Expect(json.Unmarshal([]byte(lola("profile", "--project", "remote:go", "--json").out), &v)).To(Succeed())
		Expect(v.Profiles[0]).To(And(HaveKeyWithValue("shadowed_by", "project:go"), HaveKeyWithValue("read_as", BeNil())))

		// Both on, the state says so, and offers no --on of what is on.
		led.Profiles = []ledger.Active{{Name: "remote:go", By: ledger.ByOn}, {Name: "project:go", By: ledger.ByOn}}
		Expect(led.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())
		r = lola("profile", "--project", "remote:go")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("\nproject (" + proj + "): 1st of 2, in effect; project:go is on too, 2nd of 2, " +
			"and its entry wins where both list a module; its modules:\n"))
		Expect(r.out).NotTo(ContainSubstring("would take its place"))
		Expect(r.out).NotTo(ContainSubstring("to use project:go instead"))
		led.Profiles = []ledger.Active{{Name: "project:go", By: ledger.ByOn}, {Name: "remote:go", By: ledger.ByOn}}
		Expect(led.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())
		r = lola("profile", "--project", "remote:go")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("): 2nd of 2, in effect; project:go is on too, 1st of 2, and this one's entry wins where both list a module;"))
	})

	It("refuses --update --scope both when the project is your home directory, before any network contact", func() {
		// Its source is never reachable from a spec: a fetch would fail with another error.
		path := keep("go", gopls())
		Expect(os.MkdirAll(filepath.Join(p.Home, ".lola"), 0o755)).To(Succeed())
		refused := "error: --scope both names a project and user scope, and the project there is your home directory, where user scope installs\n" +
			"  hint: update at user scope only: lola profile go --update --user\n"
		GinkgoT().Chdir(p.Home)
		r := lola("profile", "go", "--update", "--scope", "both")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal(refused))
		r = lola("profile", "go", "--update", "--scope", "both", "--trust", "-y", "--trust=hooks,runtime-hooks")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal(refused), "answers given for both scopes answer nothing for user scope alone")
		GinkgoT().Chdir(tmp)
		r = lola("profile", "go", "--update", "--scope", "both", "-C", p.Home)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal(refused), "user scope takes no -C")
		Expect(os.ReadFile(path)).To(Equal([]byte(gopls())))
	})

	It("refuses --update naming your home directory as its project, before any network contact", func() {
		// Its source is never reachable from a spec: a fetch would fail with another error.
		path := keep("go", gopls())
		Expect(os.MkdirAll(filepath.Join(p.Home, ".lola"), 0o755)).To(Succeed())
		refused := "error: project scope at " + p.Home + " is your home directory, where user scope installs\n" +
			"  hint: update at user scope only: lola profile --user remote:go --update\n"
		GinkgoT().Chdir(p.Home)
		r := lola("profile", "go", "--update", "--project", "--trust", "-y", "--trust=hooks,runtime-hooks")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal(refused), "answers given for the project answer nothing for user scope")
		GinkgoT().Chdir(tmp)
		r = lola("profile", "go", "--update", "-C", p.Home, "--trust")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal(refused), "user scope takes no -C")
		Expect(os.ReadFile(path)).To(Equal([]byte(gopls())))
	})

	Describe("--add", func() {
		var src, url, mcommit, mdigest string
		gitIn := func(dir string, args ...string) string {
			GinkgoHelper()
			out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), string(out))
			return strings.TrimSpace(string(out))
		}
		repo := func(name string, files map[string]string) string {
			GinkgoHelper()
			work := filepath.Join(tmp, name+"-work")
			writeTree(work, files)
			gitIn(work, "init", "-q", "-b", "main")
			gitIn(work, "add", "-A")
			gitIn(work, "commit", "-q", "-m", "init")
			gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", name+".git"))
			return gitIn(work, "rev-parse", "HEAD")
		}
		BeforeEach(func() {
			mcommit = repo("example", map[string]string{"commands/ship.md": "Ship\n"})
			base := testenv.ServeGitTLS(filepath.Join(tmp, "srv"), nil)
			url, src = base+"/example.git", base+"/profiles.git"
			other := p
			other.Store, other.StoreLock, other.Staging = filepath.Join(tmp, "pin"), filepath.Join(tmp, "pin.lock"), filepath.Join(tmp, "pin-staging")
			v, err := engine.New(other).Fetch(context.Background(), source.Spec{Kind: source.KindGit, Location: url})
			Expect(err).NotTo(HaveOccurred())
			mdigest = v.Meta.Digest
			entry := "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: example, source: " + url + ", commit: " + mcommit + ", digest: " + mdigest + "}\n"
			repo("profiles", map[string]string{"profiles/go.yml": entry, "profiles/docs.yml": "version: 1\ndescription: Docs\n"})
		})

		It("refuses off a terminal without --trust before any network contact, and adds with it, showing what it adds", func() {
			r := lola("profile", "go", "--add", src, "--user")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: lola profile --add shows the profile and asks before adding it, and there is no terminal to ask on\n" +
				"  hint: see it: lola profile --user go --add " + src + " -n\n" +
				"  hint: then add it unasked: lola profile --user go --add " + src + " --trust=profiles\n"))
			Expect(p.RemoteProfiles).NotTo(BeADirectory())

			r = lola("profile", "go", "--get", src, "--user", "-n")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("remote:go → " + filepath.Join(p.RemoteProfiles, "go", "profile.yml") +
				" (shared by every scope); content compared with user scope\n  from " + src + "\n"))
			Expect(r.out).To(MatchRegexp(`\+ example\s+` + regexpQuote(url)))
			Expect(r.out).To(ContainSubstring("→ would add remote:go"))
			Expect(r.out).To(ContainSubstring("→ to add remote:go: lola profile --user go --add " + src + " (off a terminal: --trust=profiles)\n"))
			Expect(p.RemoteProfiles).NotTo(BeADirectory())

			r = lola("profile", "go", "--add", src, "--user", "--trust")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.err).To(ContainSubstring("  from "+src+"\n"), "what --trust accepted is shown")
			Expect(r.out).To(Equal("✓ added remote:go (" + filepath.Join(p.RemoteProfiles, "go", "profile.yml") + ")\n" +
				"→ turn it on: lola profile --user remote:go --on\n"))
			r = lola("profile", "remote:go", "--on", "--user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(filepath.Join(p.Home, ".claude", "commands", "ship.md")).To(BeAnExistingFile())
		})

		It("offers the source's profiles on a terminal, asks after showing them, and adds nothing on No", func() {
			sp := &scripted{profiles: []func(cli.ProfileStep) ([]string, error){
				func(st cli.ProfileStep) ([]string, error) { return []string{"docs"}, nil }}, confirm: []bool{false}}
			r := lolaP(sp, "profile", "--add", src, "--user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.err).To(ContainSubstring("aborted; nothing was added."))
			Expect(sp.calls).To(Equal([]string{"profiles", "confirm:Add remote:docs? Nothing is turned on"}))
			Expect(sp.profileSteps).To(Equal([]cli.ProfileStep{{Source: src, Names: []string{"docs", "go"}}}))
			Expect(r.err).To(ContainSubstring("remote:docs → "+filepath.Join(p.RemoteProfiles, "docs", "profile.yml")+
				" (shared by every scope); content compared with user scope\n"), "shown before the question")
			Expect(filepath.Join(p.RemoteProfiles, "docs")).NotTo(BeADirectory())

			sp = &scripted{profiles: []func(cli.ProfileStep) ([]string, error){
				func(st cli.ProfileStep) ([]string, error) { return []string{"docs", "go"}, nil }}, confirm: []bool{true}}
			r = lolaP(sp, "profile", "--add", src, "--user", "--json")
			Expect(r.code).To(Equal(1), "--json never asks: no picker")
			Expect(r.err).To(ContainSubstring("lola profile --add shows the profile and asks before adding it"))
			r = lolaP(sp, "profile", "--add", src, "--user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(sp.calls).To(Equal([]string{"profiles", "confirm:Add remote:docs, remote:go? Nothing is turned on"}))
			var v struct {
				Profiles []map[string]any `json:"profiles"`
			}
			Expect(json.Unmarshal([]byte(lola("profile", "--user", "--json").out), &v)).To(Succeed())
			Expect(v.Profiles).To(ContainElement(And(HaveKeyWithValue("name", "go"), HaveKeyWithValue("kind", "remote"), HaveKeyWithValue("source", src))))
		})

		// hookedUpdate publishes a go that adds hooked, whose pre-install hook runs body.
		hookedUpdate := func(body string) {
			GinkgoHelper()
			// The update adds hooked, whose pre-install hook the scope's sync runs.
			hw := filepath.Join(tmp, "hooked-work")
			writeTree(hw, map[string]string{"lola.yml": "name: hooked\nhooks: {pre-install: pre.sh}\n", "commands/h.md": "H\n", "pre.sh": "#!/bin/sh\n" + body + "\n"})
			Expect(os.Chmod(filepath.Join(hw, "pre.sh"), 0o755)).To(Succeed())
			gitIn(hw, "init", "-q", "-b", "main")
			gitIn(hw, "add", "-A")
			gitIn(hw, "commit", "-q", "-m", "init")
			gitIn(tmp, "clone", "-q", "--bare", hw, filepath.Join(tmp, "srv", "hooked.git"))
			hurl := url[:len(url)-len("example.git")] + "hooked.git"
			other := p
			other.Store, other.StoreLock, other.Staging = filepath.Join(tmp, "pin2"), filepath.Join(tmp, "pin2.lock"), filepath.Join(tmp, "pin2-staging")
			hv, err := engine.New(other).Fetch(context.Background(), source.Spec{Kind: source.KindGit, Location: hurl})
			Expect(err).NotTo(HaveOccurred())
			work := filepath.Join(tmp, "profiles-work")
			writeTree(work, map[string]string{"profiles/go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: example, source: " + url +
				", commit: " + mcommit + ", digest: " + mdigest + "}\n  - {name: hooked, source: " + hurl + ", commit: " + gitIn(hw, "rev-parse", "HEAD") +
				", digest: " + hv.Meta.Digest + "}\n"})
			gitIn(work, "add", "-A")
			gitIn(work, "commit", "-qm", "add hooked")
			gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "profiles.git"), "main")
		}
		It("an update whose sync fails after its grants carries every grant written so far in the error JSON", func() {
			Expect(lola("profile", "go", "--add", src, "--user", "--trust").code).To(Equal(0))
			Expect(lola("profile", "go", "--on", "--user").code).To(Equal(0))
			hookedUpdate("exit 3")
			r := lola("profile", "go", "--update", "--user", "--trust=profiles,hooks", "--json")
			Expect(r.code).To(Equal(1))
			var doc map[string]any
			Expect(json.Unmarshal([]byte(r.err), &doc)).To(Succeed(), r.err)
			Expect(doc["trusted"]).To(ConsistOf(map[string]any{"kind": "profiles", "item": "remote:go", "change": "changed"},
				map[string]any{"kind": "hooks", "item": "hooked pre-install", "change": "new"}))
		})

		It("an update with --trust shows one receipt for the profiles and each scope's sync; No updates and applies nothing", func() {

			Expect(lola("profile", "go", "--add", src, "--user", "--trust").code).To(Equal(0))
			Expect(lola("profile", "go", "--on", "--user").code).To(Equal(0))
			hookedUpdate("true")
			kept := filepath.Join(p.RemoteProfiles, "go", "profile.yml")
			before, err := os.ReadFile(kept)
			Expect(err).NotTo(HaveOccurred())

			c := &confirming{scripted: scripted{}, answers: []bool{false}}
			r := lolaAsking(c, "profile", "go", "--update", "--user", "--trust=profiles,hooks")
			Expect(r.code).To(Equal(0), r.err)
			Expect(c.questions).To(Equal([]string{"Proceed?"}), "one question for the update and its sync")
			Expect(r.err).To(ContainSubstring("trusting, as --trust=profiles,hooks allows:\n  → profiles: remote:go (changed)\n  → hooks: hooked pre-install (new)\n"))
			Expect(r.err).To(HaveSuffix("aborted; nothing was updated.\n"))
			Expect(os.ReadFile(kept)).To(Equal(before), "nothing is written before the receipt's yes")

			Expect(p.Consent).NotTo(BeAnExistingFile())

			c = &confirming{scripted: scripted{}, answers: []bool{true}}
			r = lolaAsking(c, "profile", "go", "--update", "--user", "--trust=profiles,hooks")
			Expect(r.code).To(Equal(0), r.err)
			Expect(c.questions).To(Equal([]string{"Proceed?"}))
			Expect(os.ReadFile(kept)).NotTo(Equal(before))
			Expect(r.out).To(ContainSubstring("✓ updated remote:go ("))
			Expect(r.out).To(ContainSubstring("✓ synced hooked (claude-code)"))
		})

		It("an update with --trust across two scopes asks one receipt, then runs and records each scope's hook", func() {
			Expect(lola("profile", "go", "--add", src, "--user", "--trust").code).To(Equal(0))
			Expect(lola("profile", "go", "--on", "--user").code).To(Equal(0))
			proj := filepath.Join(tmp, "proj")
			Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
			GinkgoT().Chdir(proj)
			Expect(lola("profile", "go", "--on", "--project").code).To(Equal(0))
			mark := filepath.Join(tmp, "hook-ran")
			hookedUpdate("echo ran >> '" + mark + "'")

			c := &confirming{answers: []bool{true}}
			r := lolaAsking(c, "profile", "go", "--update", "--scope", "both", "--trust=profiles,hooks")
			Expect(r.code).To(Equal(0), r.err)
			Expect(c.questions).To(Equal([]string{"Proceed?"}), "one question for the update and both scopes' syncs")
			Expect(r.err).To(ContainSubstring("trusting, as --trust=profiles,hooks allows:\n  → profiles: remote:go (changed)\n  → hooks: hooked pre-install (new)\n"))
			Expect(os.ReadFile(mark)).To(Equal([]byte("ran\nran\n")), "the hook ran in each scope")
			Expect(filepath.Join(proj, ".claude", "commands", "h.md")).To(BeAnExistingFile())
			Expect(filepath.Join(p.Home, ".claude", "commands", "h.md")).To(BeAnExistingFile())
			Expect(lola("sync", "--scope", "both").code).To(Equal(0), "the consent is recorded: nothing asks again")
		})

		It("an update with --trust names what it wrote when a later profile changed before the receipt's yes", func() {
			Expect(lola("profile", "go", "--add", src, "--user", "--trust").code).To(Equal(0))
			Expect(lola("profile", "go", "--on", "--user").code).To(Equal(0))
			work := filepath.Join(tmp, "profiles-work")
			writeTree(work, map[string]string{"profiles/docs.yml": "version: 1\ndescription: docs\n"})
			gitIn(work, "add", "-A")
			gitIn(work, "commit", "-qm", "add docs")
			gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "profiles.git"), "main")
			Expect(lola("profile", "docs", "--add", src, "--user", "--trust").code).To(Equal(0))
			writeTree(work, map[string]string{"profiles/docs.yml": "version: 1\ndescription: docs 2\n"})
			hookedUpdate("true")

			// Another lola run replaces docs while the receipt is asked.
			c := &confirming{answers: []bool{true}, meddle: func() {
				o, err := profile.ReadOrigin(filepath.Join(p.RemoteProfiles, "docs"))
				Expect(err).NotTo(HaveOccurred())
				o.Commit = strings.Repeat("0", 40)
				b, err := json.Marshal(o)
				Expect(err).NotTo(HaveOccurred())
				Expect(os.WriteFile(filepath.Join(p.RemoteProfiles, "docs", "source.json"), b, 0o600)).To(Succeed())
			}}
			r := lolaAsking(c, "profile", "go", "docs", "--update", "--user", "--trust=profiles,hooks")
			Expect(r.code).To(Equal(1), r.err)
			Expect(c.questions).To(Equal([]string{"Proceed?"}))
			Expect(r.out).To(ContainSubstring("✓ updated remote:go ("))
			Expect(r.err).To(HaveSuffix("error: remote:go is updated, but not applied; the run stopped: remote:docs changed while you were asked; it was not updated\n" +
				"  hint: apply what was updated at user scope: lola sync --user\n" +
				"  hint: run it again: lola profile --user remote:docs --update --trust=profiles,hooks\n"))
			Expect(filepath.Join(p.Home, ".claude", "commands", "h.md")).NotTo(BeAnExistingFile(), "nothing is applied")
		})

		It("an update with --trust whose sync is cancelled before the receipt updates nothing", func() {
			Expect(lola("profile", "go", "--add", src, "--user", "--trust").code).To(Equal(0))
			Expect(lola("profile", "go", "--on", "--user").code).To(Equal(0))
			work := filepath.Join(tmp, "profiles-work")
			writeTree(work, map[string]string{"profiles/go.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n"})
			gitIn(work, "commit", "-qam", "drop example")
			gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "profiles.git"), "main")
			kept := filepath.Join(p.RemoteProfiles, "go", "profile.yml")
			before, err := os.ReadFile(kept)
			Expect(err).NotTo(HaveOccurred())
			c := &confirming{scripted: scripted{}, answers: []bool{false}}
			r := lolaAsking(c, "profile", "go", "--update", "--user", "--trust=profiles")
			Expect(r.code).To(Equal(0), r.err)
			Expect(c.questions).To(Equal([]string{"Remove these and sync? No applies nothing"}), "a No to the removals is no refusal: no receipt follows")
			Expect(r.err).To(HaveSuffix("aborted; nothing was updated.\n"))
			Expect(os.ReadFile(kept)).To(Equal(before))
		})

		It("updates a remote profile, applying it where it is on", func() {
			Expect(lola("profile", "go", "--add", src, "--user", "--trust").code).To(Equal(0))
			Expect(lola("profile", "go", "--on", "--user").code).To(Equal(0))
			r := lola("profile", "go", "--upgrade", "--user")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(HavePrefix("error: lola profile --update shows how each profile changes and asks before keeping it, and there is no terminal to ask on\n"))
			r = lola("profile", "go", "--update", "--user", "--trust")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(HavePrefix("→ remote:go is up to date with " + src + " (commit "))

			work := filepath.Join(tmp, "profiles-work")
			writeTree(work, map[string]string{"profiles/go.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n"})
			gitIn(work, "commit", "-qam", "drop example")
			gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "profiles.git"), "main")
			// A module of user.yml not yet installed is a pending change the stopped sync still reports.
			pending := func(name string) {
				GinkgoHelper()
				dir := filepath.Join(tmp, "mods", name)
				writeTree(dir, map[string]string{"lola.yml": "name: " + name + "\n", "commands/" + name + ".md": "X\n"})
				writeTree(filepath.Dir(p.UserManifest), map[string]string{"user.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: " + name +
					", source: " + dir + "}\n"})
			}
			pending("extra")
			r = lola("profile", "go", "--update", "--user", "--trust", "--trust=hooks")
			Expect(r.code).To(Equal(1), "dropping example removes files, which -y confirms")
			Expect(r.err).To(ContainSubstring("  - example"), "the change it accepted is shown")
			Expect(r.out).To(ContainSubstring("✓ updated remote:go ("), "the update is reported before it is applied")
			Expect(r.out).To(ContainSubstring("→ 1 other change is pending → lola sync --user\n"), "with its notices")
			Expect(r.err).To(ContainSubstring("error: remote:go is updated, but not applied to user scope: lola sync needs your confirmation to remove example, " +
				"and there is no terminal to ask on\n  hint: apply it later: lola sync --user example --trust=entries,profiles,hooks\n"))
			Expect(filepath.Join(p.Home, ".claude", "commands", "ship.md")).To(BeAnExistingFile())
			r = lola("sync", "--user", "-y")
			Expect(r.code).To(Equal(0), r.err)
			Expect(filepath.Join(p.Home, ".claude", "commands", "ship.md")).NotTo(BeAnExistingFile(), "the update was kept; the sync applied it")

			// Under --json, the notices join the error's hints.
			writeTree(work, map[string]string{"profiles/go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: example, source: " + url +
				", commit: " + mcommit + ", digest: " + mdigest + "}\n"})
			gitIn(work, "commit", "-qam", "example again")
			gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "profiles.git"), "main")
			r = lola("profile", "go", "--update", "--user", "--trust")
			Expect(r.code).To(Equal(0), r.err)
			writeTree(work, map[string]string{"profiles/go.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n"})
			gitIn(work, "commit", "-qam", "drop example again")
			gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "profiles.git"), "main")
			pending("more")
			r = lola("profile", "go", "--update", "--user", "--trust", "--trust=runtime-hooks", "--json")
			Expect(r.code).To(Equal(1))
			var out map[string]any
			Expect(json.Unmarshal([]byte(r.err), &out)).To(Succeed(), r.err)
			Expect(out["hints"]).To(ContainElement("apply it later: lola sync --user example --trust=entries,profiles,runtime-hooks --json"), "it keeps the run's output flags")
			Expect(out["notices"]).To(ContainElement(HaveKeyWithValue("text", "2 other changes are pending → lola sync --user")),
				"extra, now installed and no longer wanted, and more")
		})

		It("keeps an update whose sync hits a hand-edited file, hinting the sync with --force that applies it", func() {
			Expect(lola("profile", "go", "--add", src, "--user", "--trust").code).To(Equal(0))
			Expect(lola("profile", "go", "--on", "--user").code).To(Equal(0))
			writeTree(filepath.Join(p.Home, ".claude", "commands"), map[string]string{"ship.md": "mine now\n"})
			work := filepath.Join(tmp, "profiles-work")
			writeTree(work, map[string]string{"profiles/go.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n"})
			gitIn(work, "commit", "-qam", "drop example")
			gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "profiles.git"), "main")

			r := lola("profile", "go", "--update", "--user", "--trust", "-y")
			Expect(r.code).To(Equal(1))
			Expect(r.out).To(ContainSubstring("✓ updated remote:go ("))
			Expect(r.err).To(ContainSubstring("error: remote:go is updated, but not applied to user scope: refusing to change anything: 1 conflict\n"),
				"lola profile takes no --force")
			Expect(r.err).NotTo(ContainSubstring("which --force overrides"))
			// The same sync would meet the same file: the hint is the one --force lets through,
			// which says what it overrides and carries none of the run's answers (ruling R-6cux-4).
			Expect(r.err).To(HaveSuffix("  hint: apply it later, overwriting what the conflicts list: lola sync --user example --force\n"))
			Expect(r.err).NotTo(ContainSubstring("apply it later: "))
			Expect(os.ReadFile(filepath.Join(p.Home, ".claude", "commands", "ship.md"))).To(Equal([]byte("mine now\n")))
			r = lolaP(&scripted{confirm: []bool{true}}, "sync", "--user", "example", "--force")
			Expect(r.code).To(Equal(0), r.err)
			Expect(filepath.Join(p.Home, ".claude", "commands", "ship.md")).NotTo(BeAnExistingFile(), "the hint applies the update")
		})

		It("keeps an update whose sync meets a file another module owns, with no sync hint, saying what to remove", func() {
			mine := filepath.Join(tmp, "mods", "mine")
			writeTree(mine, map[string]string{"lola.yml": "name: mine\n", "commands/ship.md": "Mine\n"})
			r := lola("install", "--user", mine, "-t", "claude-code")
			Expect(r.code).To(Equal(0), r.err)
			work := filepath.Join(tmp, "profiles-work")
			writeTree(work, map[string]string{"profiles/go.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n"})
			gitIn(work, "commit", "-qam", "drop example")
			gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "profiles.git"), "main")
			Expect(lola("profile", "go", "--add", src, "--user", "--trust").code).To(Equal(0))
			Expect(lola("profile", "go", "--on", "--user").code).To(Equal(0))
			gitIn(work, "checkout", "-q", "HEAD~1", "--", "profiles/go.yml")
			gitIn(work, "commit", "-qam", "example again")
			gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "profiles.git"), "main")

			r = lola("profile", "go", "--update", "--user", "--trust", "-y")
			Expect(r.code).To(Equal(1))
			Expect(r.out).To(ContainSubstring("✓ updated remote:go ("))
			Expect(r.err).To(ContainSubstring("error: remote:go is updated, but not applied to user scope: refusing to change anything: 1 conflict\n"))
			Expect(r.err).To(ContainSubstring("--force cannot override it: remove example from profile:remote:go\n"))
			Expect(r.err).NotTo(ContainSubstring("hint:"), "no sync applies it while mine owns the file")
			Expect(os.ReadFile(filepath.Join(p.Home, ".claude", "commands", "ship.md"))).To(Equal([]byte("Mine\n")))
		})

		It("updates in the project found here, then user scope, without a scope flag, stopping at the first that fails", func() {
			Expect(lola("profile", "go", "--add", src, "--user", "--trust").code).To(Equal(0))
			Expect(lola("profile", "go", "--on", "--user").code).To(Equal(0))
			proj := filepath.Join(tmp, "proj")
			Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
			GinkgoT().Chdir(proj)
			Expect(lola("profile", "go", "--on", "--project").code).To(Equal(0))
			work := filepath.Join(tmp, "profiles-work")
			push := func(content, msg string) {
				GinkgoHelper()
				writeTree(work, map[string]string{"profiles/go.yml": content})
				gitIn(work, "commit", "-qam", msg)
				gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "profiles.git"), "main")
			}
			push("version: 1\ntargets: [claude-code]\nmodules: []\n", "drop example")
			r := lola("profile", "go", "--update", "--trust", "--trust=hooks")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(ContainSubstring("content compared with project " + proj + "\n"))
			Expect(r.out).To(ContainSubstring("→ every other checkout that uses remote:go picks the update up at its next lola sync\n"))
			Expect(r.err).To(ContainSubstring("error: remote:go is updated, but not applied to the project: lola sync needs your confirmation to remove example, " +
				"and there is no terminal to ask on; user scope was not synced\n" +
				"  hint: apply it later: lola sync --project example --trust=hooks\n  hint: apply it later: lola sync --user example --trust=hooks\n"))
			Expect(filepath.Join(proj, ".claude", "commands", "ship.md")).To(BeAnExistingFile())
			Expect(filepath.Join(p.Home, ".claude", "commands", "ship.md")).To(BeAnExistingFile())
			Expect(lola("sync", "--scope", "both", "-y").code).To(Equal(0))

			push("version: 1\ntargets: [claude-code]\nmodules:\n  - {name: example, source: "+url+", commit: "+mcommit+", digest: "+mdigest+"}\n", "example again")
			r = lola("profile", "go", "--update", "--trust")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(MatchRegexp(`(?s)✓ updated remote:go .*\nproject \(` + regexpQuote(proj) + `\):\n.*\nuser:\n`))
			Expect(filepath.Join(proj, ".claude", "commands", "ship.md")).To(BeAnExistingFile())
			Expect(filepath.Join(p.Home, ".claude", "commands", "ship.md")).To(BeAnExistingFile())

			push("version: 1\ntargets: [claude-code]\ndescription: narrowed\nmodules:\n  - {name: example, source: "+url+", commit: "+mcommit+
				", digest: "+mdigest+"}\n", "describe")
			r = lola("profile", "go", "--update", "--trust", "--user", "--json")
			Expect(r.code).To(Equal(0), r.err)
			var v map[string]any
			Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed())
			Expect(v["scopes"]).To(ConsistOf(HaveKeyWithValue("scope", "user")), "a scope flag narrows it")
			Expect(v["notices"]).To(ContainElement(HaveKeyWithValue("text", "remote:go is also on in project "+proj+"; the update changes nothing installed there")))

			// A No at a scope's own question stops there: the update is kept, and the message says how to apply it.
			push("version: 1\ntargets: [claude-code]\nmodules: []\n", "drop example again")
			sp := &scripted{confirm: []bool{true, false}}
			r = lolaP(sp, "profile", "go", "--update")
			Expect(r.code).To(Equal(0), r.err)
			Expect(sp.calls).To(Equal([]string{"confirm:Update remote:go?", "confirm:Remove these and sync? No applies nothing"}))
			Expect(r.err).To(HaveSuffix("remote:go is updated, but not applied to the project: removals declined; the project was not changed; user scope was not synced. " +
				"Apply it later: lola sync --project example, then lola sync --user example.\n"))
			Expect(filepath.Join(proj, ".claude", "commands", "ship.md")).To(BeAnExistingFile())

			// --scope both is every known scope, as no flag is (ruling R-6b3c-2), and its hints keep it.
			push("version: 1\ntargets: [claude-code]\ndescription: both\nmodules: []\n", "describe")
			r = lola("profile", "go", "--update", "--scope", "both", "-n")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("→ to update remote:go: lola profile --scope both remote:go --update (off a terminal: --trust=profiles)\n"))
			r = lola("profile", "go", "--update", "--trust", "--scope", "both", "-y", "--json")
			Expect(r.code).To(Equal(0), r.err)
			Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed())
			Expect(v["scopes"]).To(HaveExactElements(HaveKeyWithValue("scope", "project"), HaveKeyWithValue("scope", "user")))

			// Without a project, it is refused, offering user scope alone with the run's other flags.
			GinkgoT().Chdir(tmp)
			r = lola("profile", "go", "--update", "--trust", "--scope", "both", "--trust=hooks", "-y", "--json")
			Expect(r.code).To(Equal(1))
			var refused map[string]any
			Expect(json.Unmarshal([]byte(r.err), &refused)).To(Succeed(), r.err)
			Expect(refused["error"]).To(Equal("--scope both names a project and user scope, and no project with .lola/ was found in " + tmp + " or above it"))
			Expect(refused["hints"]).To(Equal([]any{"update at user scope only: lola profile go --json --update --user"}),
				"the answers given for both scopes are dropped; --json is kept")
			r = lola("profile", "go", "--update", "--trust", "--scope", "both", "-C", tmp)
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: --scope both names a project and user scope, and " + tmp + " has no .lola/\n" +
				"  hint: update at user scope only: lola profile go --update --user\n"))
		})

		It("updates the profiles named, or --all, asking about each on its own, and refuses neither, both or a name twice", func() {
			Expect(lola("profile", "go", "--add", src, "--user", "--trust").code).To(Equal(0))
			Expect(lola("profile", "docs", "--add", src, "--user", "--trust").code).To(Equal(0))
			r := lola("profile", "--update", "--user", "--trust=hooks", "-y", "--trust", "--json")
			Expect(r.code).To(Equal(1))
			var doc map[string]any
			Expect(json.Unmarshal([]byte(r.err), &doc)).To(Succeed(), r.err)
			Expect(doc["error"]).To(Equal("--update needs the remote profiles to update, or --all; you have remote:docs, remote:go"))
			Expect(doc["hints"]).To(Equal([]any{"name them: lola profile remote:docs remote:go --json --update --user",
				"or every one: lola profile --json --update --user --all"}), "no answer is given for profiles never named")
			r = lola("profile", "go", "--update", "--all", "--user", "--trust=hooks", "-y", "--trust", "--json")
			Expect(r.code).To(Equal(1))
			Expect(json.Unmarshal([]byte(r.err), &doc)).To(Succeed(), r.err)
			Expect(doc["error"]).To(Equal("--all updates every remote profile you have, and takes no names"))
			Expect(doc["hints"]).To(Equal([]any{"update every one: lola profile --all --json --update --user",
				"or only those named: lola profile go --json --trust=entries,profiles,hooks --update --user --yes"}), "those named keep every answer")
			// The scope flags are checked first, so the hints above name scopes that work.
			GinkgoT().Chdir(tmp)
			r = lola("profile", "--update", "--scope", "both", "--trust")
			Expect(r.err).To(HavePrefix("error: --scope both names a project and user scope, and no project with .lola/ was found in "))
			r = lola("profile", "go", "docs", "remote:go", "--update", "--user")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: remote:go is named twice\n  hint: name it once: lola profile go docs --update --user\n"))
			r = lola("profile", "go", "--on", "--all", "--user")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: --all applies to --update only\n  hint: drop it: lola profile go --on --user\n"))

			work := filepath.Join(tmp, "profiles-work")
			push := func(docs string) {
				GinkgoHelper()
				writeTree(work, map[string]string{"profiles/docs.yml": "version: 1\ndescription: " + docs + "\n"})
				gitIn(work, "commit", "-qam", docs)
				gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "profiles.git"), "main")
			}
			push("Docs 2")
			sp := &scripted{confirm: []bool{false, false}}
			r = lolaP(sp, "profile", "--update", "--all", "--user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(sp.calls).To(Equal([]string{"confirm:Update remote:docs?", "confirm:Update remote:go?"}), "go is at another commit too")
			Expect(r.err).To(HaveSuffix("aborted; nothing was updated.\n"))

			push("Docs 3")
			sp = &scripted{confirm: []bool{false, true}}
			r = lolaP(sp, "profile", "docs", "go", "--update", "--user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(HavePrefix("→ declined remote:docs; it is kept as it was\n✓ updated remote:go (" +
				filepath.Join(p.RemoteProfiles, "go", "profile.yml") + ")\n"))
			r = lola("profile", "docs", "go", "--update", "--user", "--trust")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(HavePrefix("✓ updated remote:docs (" + filepath.Join(p.RemoteProfiles, "docs", "profile.yml") + ")\n→ remote:go is up to date with " + src))

			// A failure is that profile's: the others are updated, and the run fails after.
			o, err := profile.ReadOrigin(filepath.Join(p.RemoteProfiles, "go"))
			Expect(err).NotTo(HaveOccurred())
			o.Ref = "--upload-pack=touch PWNED"
			b, err := json.Marshal(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(filepath.Join(p.RemoteProfiles, "go", "source.json"), b, 0o600)).To(Succeed())
			push("Docs 4")
			r = lola("profile", "--update", "--all", "--user", "--trust", "--plain")
			Expect(r.code).To(Equal(1))
			Expect(r.out).To(HavePrefix("updated\tremote:docs\t" + filepath.Join(p.RemoteProfiles, "docs", "profile.yml") + "\nfailed\tremote:go\t"))
			Expect(r.err).To(ContainSubstring("error: 1 of 2 remote profiles was not updated: remote:go: "+filepath.Join(p.RemoteProfiles, "go", "source.json")+
				": ref \"--upload-pack=touch PWNED\" starts with '-'\n"), "the reason, in every output mode")
			Expect(r.err).To(ContainSubstring("  hint: remove it: lola profile --user remote:go --remove --force\n"))
			push("Docs 5")
			r = lola("profile", "--update", "--all", "--user", "--trust")
			Expect(r.out).To(ContainSubstring("✗ remote:go was not updated (the error says why)\n"))
			Expect(r.err).To(ContainSubstring("\n  hint: remove it: lola profile --user remote:go --remove --force\n"))
			push("Docs 6")
			r = lola("profile", "--update", "--all", "--user", "--trust", "--json")
			Expect(r.code).To(Equal(1))
			var v map[string]any
			Expect(json.Unmarshal([]byte(r.err), &v)).To(Succeed(), r.err)
			Expect(v["updates"]).To(HaveExactElements(And(HaveKeyWithValue("profile", "remote:docs"), HaveKeyWithValue("outcome", "updated")),
				And(HaveKeyWithValue("profile", "remote:go"), HaveKeyWithValue("outcome", "failed"), HaveKeyWithValue("error", ContainSubstring("starts with '-'")))))
			Expect(filepath.Join(tmp, "PWNED")).NotTo(BeAnExistingFile())
			Expect(strings.ReplaceAll(r.err, `"sync":null`, "")).NotTo(ContainSubstring("null"), "no array is null")
		})

		It("stops at Ctrl+C, keeping what it updated and naming how to apply it", func() {
			Expect(lola("profile", "go", "--add", src, "--user", "--trust").code).To(Equal(0))
			Expect(lola("profile", "docs", "--add", src, "--user", "--trust").code).To(Equal(0))
			Expect(lola("profile", "go", "--on", "--user").code).To(Equal(0))
			work := filepath.Join(tmp, "profiles-work")
			writeTree(work, map[string]string{"profiles/docs.yml": "version: 1\ndescription: Docs 2\n",
				"profiles/go.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n"})
			gitIn(work, "commit", "-qam", "both")
			gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "profiles.git"), "main")
			sp := &ctrlC{scripted: &scripted{confirm: []bool{true, true}}, n: 1}
			r := lolaP(sp, "profile", "go", "docs", "--update", "--user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.err).To(HaveSuffix("aborted; nothing was updated.\n"))
			sp = &ctrlC{scripted: &scripted{confirm: []bool{true, true}}, n: 2}
			r = lolaP(sp, "profile", "go", "docs", "--update", "--user", "--trust=hooks")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.err).To(HaveSuffix("aborted; nothing was updated.\n"), "with --trust nothing is written before the command's one receipt")
			sp = &ctrlC{scripted: &scripted{confirm: []bool{true, true}}, n: 2}
			r = lolaP(sp, "profile", "go", "docs", "--update", "--user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.err).To(HaveSuffix("aborted; remote:go is updated, but not applied (lola sync --user example); nothing else was updated.\n"))
			Expect(filepath.Join(p.Home, ".claude", "commands", "ship.md")).To(BeAnExistingFile(), "nothing is applied")

			// A profile that failed before Ctrl+C is reported, with each one the stop left, and fails the run.
			writeTree(work, map[string]string{"profiles/zed.yml": "version: 1\ndescription: Zed\n"})
			gitIn(work, "add", "-A")
			gitIn(work, "commit", "-qm", "add zed")
			gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "profiles.git"), "main")
			Expect(lola("profile", "zed", "--add", src, "--user", "--trust").code).To(Equal(0))
			o, err := profile.ReadOrigin(filepath.Join(p.RemoteProfiles, "docs"))
			Expect(err).NotTo(HaveOccurred())
			o.Ref = "--upload-pack=touch PWNED"
			b, err := json.Marshal(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(filepath.Join(p.RemoteProfiles, "docs", "source.json"), b, 0o600)).To(Succeed())
			writeTree(work, map[string]string{"profiles/go.yml": "version: 1\ntargets: [claude-code]\ndescription: Go\nmodules: []\n",
				"profiles/zed.yml": "version: 1\ndescription: Zed 2\n"})
			gitIn(work, "commit", "-qam", "describe go and zed")
			gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "profiles.git"), "main")
			sp = &ctrlC{scripted: &scripted{confirm: []bool{true, true}}, n: 2}
			r = lolaP(sp, "profile", "--update", "--all", "--user")
			Expect(r.code).To(Equal(1))
			Expect(r.out).To(HavePrefix("✗ remote:docs was not updated (the error says why)\n✓ updated remote:go"))
			Expect(r.err).To(ContainSubstring("error: 2 of 3 remote profiles were not updated: remote:docs: "))
			Expect(r.err).To(ContainSubstring("; remote:zed: stopped before it was updated; remote:go is updated, but not applied; the run stopped at Ctrl+C\n"),
				"the profile the stop left is counted")
		})

		It("never lets --trust answer the found project's trust when no flag names it", func() {
			Expect(lola("profile", "go", "--add", src, "--user", "--trust").code).To(Equal(0))
			proj := filepath.Join(tmp, "proj")
			Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
			GinkgoT().Chdir(proj)
			Expect(lola("profile", "go", "--on", "--project").code).To(Equal(0))
			// The checkout gains a project.yml you never trusted, as a hostile, untracked repository would.
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: planted, source: " + url + "}\n"})
			work := filepath.Join(tmp, "profiles-work")
			writeTree(work, map[string]string{"profiles/go.yml": "version: 1\ntargets: [claude-code]\ndescription: new\nmodules:\n" +
				"  - {name: example, source: " + url + ", commit: " + mcommit + ", digest: " + mdigest + "}\n"})
			gitIn(work, "commit", "-qam", "describe")
			gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "profiles.git"), "main")
			r := lola("profile", "go", "--update", "--trust", "--trust=hooks")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: project " + proj + " has changes you have not trusted (planted and project.yml's targets), which its sync asks " +
				"about, and no flag named it: --trust then answers only for the remote profiles\n" +
				"  hint: name it, and trust them: lola profile --project remote:go --update --trust=entries,profiles,hooks\n" +
				"  hint: or with user scope too: lola profile --scope both remote:go --update --trust=entries,profiles,hooks\n"))
			Expect(r.out).To(BeEmpty(), "nothing was fetched or kept")
			r = lola("profile", "go", "--update", "--trust", "--trust=hooks", "--scope", "both")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("✓ updated remote:go ("), "named, the project's trust is --trust's to give")
		})

		It("says why the one profile of --all failed, and keeps the run's document under --json", func() {
			Expect(lola("profile", "go", "--add", src, "--user", "--trust").code).To(Equal(0))
			o, err := profile.ReadOrigin(filepath.Join(p.RemoteProfiles, "go"))
			Expect(err).NotTo(HaveOccurred())
			o.Ref = "--upload-pack=touch PWNED"
			b, err := json.Marshal(o)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(filepath.Join(p.RemoteProfiles, "go", "source.json"), b, 0o600)).To(Succeed())
			r := lola("profile", "--update", "--all", "--user", "--trust")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(ContainSubstring("error: " + filepath.Join(p.RemoteProfiles, "go", "source.json") + ": ref \"--upload-pack=touch PWNED\" starts with '-'\n"))
			Expect(r.err).To(ContainSubstring("  hint: remove it: lola profile --user remote:go --remove --force\n"))
			r = lola("profile", "go", "--update", "--user", "--trust", "--json")
			Expect(r.code).To(Equal(1))
			var v map[string]any
			Expect(json.Unmarshal([]byte(r.err), &v)).To(Succeed(), r.err)
			Expect(v["updates"]).To(ConsistOf(HaveKeyWithValue("outcome", "failed")), "one profile's failure keeps the run's document")
			Expect(v).To(HaveKey("notices"))
		})

		It("reports what else stopped a run with Ctrl+C: the update is not applied, and that error's hints are kept", func() {
			Expect(lola("profile", "go", "--add", src, "--user", "--trust").code).To(Equal(0))
			Expect(lola("profile", "docs", "--add", src, "--user", "--trust").code).To(Equal(0))
			Expect(lola("profile", "docs", "--on", "--user").code).To(Equal(0))
			work := filepath.Join(tmp, "profiles-work")
			writeTree(work, map[string]string{"profiles/docs.yml": "version: 1\ndescription: Docs 2\n",
				"profiles/go.yml": "version: 1\ntargets: [claude-code]\ndescription: Go\nmodules: []\n"})
			gitIn(work, "commit", "-qam", "both")
			gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "profiles.git"), "main")
			// Once docs is kept, user scope stops reading: an active profile of yours breaks.
			sp := &ctrlC{scripted: &scripted{confirm: []bool{true, true}}, n: 2, before: func(n int) {
				if n != 2 {
					return
				}
				writeTree(p.UserProfiles, map[string]string{"broken.yml": "version: 1\nshell: x\n"})
				led, err := ledger.Load(p.UserLedger)
				Expect(err).NotTo(HaveOccurred())
				led.Profiles = append(led.Profiles, ledger.Active{Name: "user:broken", By: ledger.ByOn})
				Expect(led.Save(p.UserLedger)).To(Succeed())
			}}
			r := lolaP(sp, "profile", "docs", "go", "--update", "--user")
			Expect(r.code).To(Equal(1), "not a plain abort")
			Expect(r.err).To(ContainSubstring("error: remote:docs is updated, but not applied; the run stopped at Ctrl+C, and: "+
				"lola could not tell whether remote:docs is in use at user scope: "), "said once, by the stop")
			Expect(r.err).To(ContainSubstring("  hint: apply it at user scope once that is fixed: lola sync --user\n"))
		})

		It("shows an update that changes no entry as such", func() {
			Expect(lola("profile", "go", "--add", src, "--user", "--trust").code).To(Equal(0))
			r := lola("profile", "docs", "--add", src, "--user", "--trust")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.err).To(ContainSubstring("  Docs\n  (no modules)\n"))
			work := filepath.Join(tmp, "profiles-work")
			writeTree(work, map[string]string{"profiles/go.yml": "version: 1\ntargets: [claude-code]\ndescription: Go tools\nmodules:\n" +
				"  - {name: example, source: " + url + ", commit: " + mcommit + ", digest: " + mdigest + "}\n"})
			gitIn(work, "commit", "-qam", "describe go")
			gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "profiles.git"), "main")
			r = lola("profile", "go", "--update", "--user", "--trust")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.err).To(ContainSubstring("  Go tools\n  (no entry changes)\n"))
		})

		It("removes a remote profile, asking on a terminal unless -y or --force answers", func() {
			Expect(lola("profile", "go", "--add", src, "--user", "--trust").code).To(Equal(0))
			r := lola("profile", "go", "--rm", "--user")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: lola profile --remove asks before it deletes a profile, and there is no terminal to ask on\n" +
				"  hint: confirm it: lola profile --user remote:go --remove -y\n"))
			Expect(lola("profile", "go", "--on", "--user").code).To(Equal(0))
			r = lola("profile", "go", "--delete", "--user", "-y")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: remote:go is on at user scope\n  hint: turn the profile off first: lola profile --user remote:go --off\n"+
				"  hint: or remove it anyway: lola profile --user remote:go --remove --force\n"), "the run's -y answers this removal only")

			// --force answers the question on a terminal too (user decision, 2026-10-22).
			sp := &scripted{}
			r = lolaP(sp, "profile", "go", "--uninstall", "--user", "--force")
			Expect(sp.calls).To(BeEmpty(), "nothing was asked")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(Equal("⚠ remote:go is on at user scope; its modules stay installed there until the next lola sync --user, which removes them\n" +
				"✓ removed remote:go (" + filepath.Join(p.RemoteProfiles, "go", "profile.yml") + ")\n"))
			Expect(filepath.Join(p.RemoteProfiles, "go")).NotTo(BeADirectory())
		})

		It("caps a long content unpaged, naming the command that lists it, and shows its servers and hooks first", func() {
			files := map[string]string{"mcps.json": `{"mcpServers":{"gh":{"command":"gh"}}}`}
			for i := range 25 {
				files[fmt.Sprintf("commands/c%02d.md", i)] = "C\n"
			}
			bcommit := repo("big", files)
			other := p
			other.Store, other.StoreLock, other.Staging = filepath.Join(tmp, "pin2"), filepath.Join(tmp, "pin2.lock"), filepath.Join(tmp, "pin2-staging")
			v, err := engine.New(other).Fetch(context.Background(), source.Spec{Kind: source.KindGit, Location: strings.TrimSuffix(url, "example.git") + "big.git"})
			Expect(err).NotTo(HaveOccurred())
			work := filepath.Join(tmp, "profiles-work")
			writeTree(work, map[string]string{"profiles/big.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: big, source: " +
				strings.TrimSuffix(url, "example.git") + "big.git, commit: " + bcommit + ", digest: " + v.Meta.Digest + "}\n"})
			gitIn(work, "add", "-A")
			gitIn(work, "commit", "-qm", "big")
			gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "profiles.git"), "main")
			r := lola("profile", "big", "--add", src, "--user", "--trust")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.err).To(ContainSubstring("        … and 6 more (lola profile --user remote:big --add " + src + " -n --json lists them all)\n"))
			Expect(strings.Index(r.err, "MCP server gh")).To(BeNumerically("<", strings.Index(r.err, "files: 26 added")), r.err)

			// Picked rather than named, the command still names the profile.
			Expect(os.RemoveAll(filepath.Join(p.RemoteProfiles, "big"))).To(Succeed())
			sp := &scripted{profiles: []func(cli.ProfileStep) ([]string, error){
				func(cli.ProfileStep) ([]string, error) { return []string{"big"}, nil }}, confirm: []bool{false}}
			r = lolaP(sp, "profile", "--add", src, "--user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.err).To(ContainSubstring("        … and 6 more (lola profile --user remote:big --add " + src + " -n --json lists them all)\n"))
		})

		It("takes one name per action but --update, hinting each run with its flags", func() {
			r := lola("profile", "go", "docs", "--add", src, "--user", "--trust")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: --add takes one profile name; add them one at a time\n" +
				"  hint: add go: lola profile go --add " + src + " --trust=profiles --user\n  hint: add docs: lola profile docs --add " + src + " --trust=profiles --user\n"))
			r = lola("profile", "go", "docs", "--remove", "--user", "-y", "--force", "-n")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: --remove takes one remote profile's name\n  hint: see yours: lola profile --user\n"))
		})

		It("asks no scope, comparing with the project found here, else user scope, or the one a flag names", func() {
			sp := &scripted{confirm: []bool{false}}
			r := lolaP(sp, "profile", "docs", "--add", src)
			Expect(r.code).To(Equal(0), r.err)
			Expect(sp.calls).To(Equal([]string{"confirm:Add remote:docs? Nothing is turned on"}), "no scope picker")
			Expect(r.err).To(ContainSubstring("(shared by every scope); content compared with user scope\n"))

			proj := filepath.Join(tmp, "proj")
			Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
			GinkgoT().Chdir(proj)
			r = lola("profile", "docs", "--add", src, "-n")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("(shared by every scope); content compared with project " + proj + "\n"))
			Expect(r.out).To(ContainSubstring("→ to add remote:docs: lola profile docs --add " + src + " (off a terminal: --trust=profiles)\n"))
			r = lola("profile", "docs", "--add", src, "-n", "--user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("content compared with user scope\n"))

			r = lola("profile", "docs", "--add", src, "--trust", "--scope", "both")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: --scope both names two scopes; --add compares the content with one\n"+
				"  hint: drop it: lola profile docs --add "+src+"\n"+
				"  hint: or name the project: lola profile docs --add "+src+" --project\n"+
				"  hint: or user scope: lola profile docs --add "+src+" --user\n"), "--trust answered nothing about the scope each hint names")
			r = lola("profile", "docs", "--add", src, "--trust", "--scope", "both", "-C", proj)
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(HaveSuffix("  hint: or user scope: lola profile docs --add "+src+" --user\n"), "user scope takes no -C")
		})

		It("writes a JSON document of what it updated, with what it applied", func() {
			Expect(lola("profile", "go", "--add", src, "--user", "--trust").code).To(Equal(0))
			Expect(lola("profile", "go", "--on", "--user").code).To(Equal(0))
			work := filepath.Join(tmp, "profiles-work")
			b, err := os.ReadFile(filepath.Join(work, "profiles", "go.yml"))
			Expect(err).NotTo(HaveOccurred())
			writeTree(work, map[string]string{"profiles/go.yml": string(b) + "description: rules off\n"})
			gitIn(work, "commit", "-qam", "describe")
			gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "profiles.git"), "main")
			r := lola("profile", "go", "--update", "--user", "--trust", "--json")
			Expect(r.code).To(Equal(0), r.err)
			var v map[string]any
			Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed())
			Expect(v).To(And(HaveKey("remote_profiles"), HaveKey("notices"), HaveKeyWithValue("dry_run", false)))
			Expect(v["scopes"]).To(ConsistOf(And(HaveKeyWithValue("scope", "user"), HaveKeyWithValue("root", ""),
				HaveKeyWithValue("apply", BeEmpty()), HaveKeyWithValue("sync", BeNil()))))
		})

		It("writes a JSON document of what it added", func() {
			r := lola("profile", "docs", "--add", src, "--user", "--trust", "--json")
			Expect(r.code).To(Equal(0), r.err)
			var v map[string]any
			Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed())
			Expect(v["dry_run"]).To(BeFalse())
			Expect(v["notices"]).To(HaveLen(1))
			Expect(v["remote_profiles"]).To(ConsistOf(And(HaveKeyWithValue("profile", "remote:docs"), HaveKeyWithValue("written", true),
				HaveKeyWithValue("source", src), HaveKeyWithValue("description", "Docs"), HaveKeyWithValue("changes", BeEmpty()))))
			Expect(r.out).NotTo(ContainSubstring("null"))
		})
	})
})

// ctrlC is a scripted prompter whose confirmation number n is Ctrl+C; before, when set, runs
// first at each confirmation, given its number.
type ctrlC struct {
	*scripted
	n      int
	before func(int)
}

func (c *ctrlC) Confirm(question string) (bool, error) {
	ok, err := c.scripted.Confirm(question)
	confirms := 0
	for _, call := range c.calls {
		if strings.HasPrefix(call, "confirm:") {
			confirms++
		}
	}
	if c.before != nil {
		c.before(confirms)
	}
	if confirms == c.n {
		return false, cli.ErrAborted
	}
	return ok, err
}
