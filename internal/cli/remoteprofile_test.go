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
				"  hint: then add it unasked: lola profile --user go --add " + src + " --trust\n"))
			Expect(p.RemoteProfiles).NotTo(BeADirectory())

			r = lola("profile", "go", "--get", src, "--user", "-n")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("remote:go → " + filepath.Join(p.RemoteProfiles, "go", "profile.yml") +
				" (shared by every scope); content compared with user scope\n  from " + src + "\n"))
			Expect(r.out).To(MatchRegexp(`\+ example\s+` + regexpQuote(url)))
			Expect(r.out).To(ContainSubstring("→ would add remote:go"))
			Expect(r.out).To(ContainSubstring("→ to add remote:go: lola profile --user go --add " + src + " (off a terminal: --trust)\n"))
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

		It("updates a remote profile, applying it where it is on", func() {
			Expect(lola("profile", "go", "--add", src, "--user", "--trust").code).To(Equal(0))
			Expect(lola("profile", "go", "--on", "--user").code).To(Equal(0))
			r := lola("profile", "go", "--upgrade", "--user")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(HavePrefix("error: lola profile --update shows how the profile changes and asks before keeping it, and there is no terminal to ask on\n"))
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
			r = lola("profile", "go", "--update", "--user", "--trust", "--allow-hooks")
			Expect(r.code).To(Equal(1), "dropping example removes files, which -y confirms")
			Expect(r.err).To(ContainSubstring("  - example"), "the change it accepted is shown")
			Expect(r.err).To(ContainSubstring("→ 1 other change is pending → lola sync --user\n"), "the update's notices are kept")
			Expect(r.err).To(ContainSubstring("error: remote:go is updated, but not applied to user scope: lola sync needs your confirmation to remove example, " +
				"and there is no terminal to ask on\n  hint: apply it later: lola sync --user example --allow-hooks\n"))
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
			r = lola("profile", "go", "--update", "--user", "--trust", "--allow-runtime-hooks", "--json")
			Expect(r.code).To(Equal(1))
			var out map[string]any
			Expect(json.Unmarshal([]byte(r.err), &out)).To(Succeed(), r.err)
			Expect(out["hints"]).To(ContainElements("apply it later: lola sync --user example --allow-runtime-hooks",
				"2 other changes are pending → lola sync --user"), "extra, now installed and no longer wanted, and more")
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

		It("removes a remote profile, asking on a terminal or with -y, and never on --force alone", func() {
			Expect(lola("profile", "go", "--add", src, "--user", "--trust").code).To(Equal(0))
			r := lola("profile", "go", "--rm", "--user")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: lola profile --remove asks before it deletes a profile, and there is no terminal to ask on\n" +
				"  hint: confirm it: lola profile --user remote:go --remove -y\n"))
			Expect(lola("profile", "go", "--on", "--user").code).To(Equal(0))
			r = lola("profile", "go", "--delete", "--user", "-y")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: remote:go is on at user scope\n  hint: turn remote:go off first: lola profile --user remote:go --off -y\n"+
				"  hint: or remove it anyway: lola profile --user remote:go --remove --force -y\n"), "off a terminal, a hint without -y would refuse")

			sp := &scripted{confirm: []bool{false}}
			r = lolaP(sp, "profile", "go", "--remove", "--user", "--force")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.err).To(ContainSubstring("aborted; nothing was removed."))
			Expect(sp.calls).To(Equal([]string{"confirm:Remove remote:go (" + filepath.Join(p.RemoteProfiles, "go", "profile.yml") +
				")? Its modules stay installed until each scope's next sync"}))
			r = lola("profile", "go", "--uninstall", "--user", "--force", "-y")
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

		It("takes one name per action, hinting each run with its flags", func() {
			r := lola("profile", "go", "docs", "--add", src, "--user", "--trust")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: --add takes one profile name; add them one at a time\n" +
				"  hint: add go: lola profile go --add " + src + " --trust --user\n  hint: add docs: lola profile docs --add " + src + " --trust --user\n"))
			r = lola("profile", "go", "docs", "--update", "--user", "-y", "--allow-hooks")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: --update takes one remote profile's name\n  hint: see yours: lola profile --user\n"))
			r = lola("profile", "go", "docs", "--remove", "--user", "-y", "--force", "-n")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: --remove takes one remote profile's name\n  hint: see yours: lola profile --user\n"))
		})

		It("asks which scope to compare with, saying adding turns nothing on", func() {
			sp := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(cli.ScopeUser)}, confirm: []bool{false}}
			r := lolaP(sp, "profile", "docs", "--add", src)
			Expect(r.code).To(Equal(0), r.err)
			Expect(sp.scopeSteps[0].Question).To(Equal("Compare its content with which scope? (adding turns nothing on)"))
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
			Expect(v).To(And(HaveKeyWithValue("apply", BeEmpty()), HaveKeyWithValue("sync", BeNil()), HaveKey("remote_profiles"), HaveKey("notices")))
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
