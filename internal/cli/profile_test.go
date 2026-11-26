package cli_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("profiles", func() {
	var tmp, a, c string
	var p paths.Paths
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		a, c = filepath.Join(tmp, "mods", "a"), filepath.Join(tmp, "mods", "c")
		writeTree(a, map[string]string{"lola.yml": "name: a\n", "commands/a.md": "A\n"})
		writeTree(c, map[string]string{"lola.yml": "name: c\n", "commands/c.md": "C\n"})
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: " + c + "}\n" +
			"  - {name: a, source: " + a + "}\n"})
	})

	It("list names the layer each module comes from", func() {
		r := lola("list", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`SCOPE\s+MODULE\s+TARGET\s+STATUS\s+RULES\s+SOURCE\s+FROM\n`))
		Expect(r.out).To(MatchRegexp(`user\s+c\s+claude-code\s+⚠ not installed\s+on\s+` + c + `\s+default\n`))

		r = lola("sync", "--user")
		Expect(r.code).To(Equal(0), r.err)
		r = lola("list", "--user")
		Expect(r.out).To(MatchRegexp(`user\s+a\s+claude-code\s+✓ ok\s+on\s+` + a + `\s+default\n`))
		Expect(r.out).To(MatchRegexp(`user\s+c\s+claude-code\s+✓ ok\s+on\s+` + c + `\s+default\n`))
		Expect(lola("list", "--user", "--plain").out).To(Equal("a\tclaude-code\tok\ton\t" + a + "\tuser\tprofile:default\t\t-\n" +
			"c\tclaude-code\tok\ton\t" + c + "\tuser\tprofile:default\t\t-\n"))
		var v struct {
			Modules []map[string]any `json:"modules"`
			Notices []any            `json:"notices"`
		}
		Expect(json.Unmarshal([]byte(lola("list", "--user", "--json").out), &v)).To(Succeed())
		Expect(v.Modules).To(ContainElement(And(HaveKeyWithValue("name", "c"), HaveKeyWithValue("from", "profile:default"))))
		Expect(v.Notices).NotTo(BeNil())
		Expect(v.Notices).To(BeEmpty())
	})

	It("uninstall at user scope removes the module from default.yml", func() {
		Expect(lola("sync", "--user").code).To(Equal(0))
		r := lola("uninstall", "--user", "c", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "default.yml"))).To(Equal([]byte("version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  - {name: a, source: " + a + "}\n")))
		Expect(lola("list", "--user", "--plain").out).To(Equal("a\tclaude-code\tok\ton\t" + a + "\tuser\tprofile:default\t\t-\n"))
	})

	It("uninstall -y under a profile on removes the base's module, the one layer that lists it", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		Expect(lola("profile", "on", "go", "--user", "-y").code).To(Equal(0))
		r := lola("uninstall", "--user", "c", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "default.yml"))).To(Equal([]byte("version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  - {name: a, source: " + a + "}\n")))
	})

	It("uninstall -y refuses when two layers list the module, hinting --profile with -y, which works", func() {
		listC := "version: 1\nmodules:\n  - {name: c, source: " + c + "}\n"
		writeTree(p.UserProfiles, map[string]string{"one.yml": listC, "two.yml": listC, "work.yml": "version: 1\nbundle: [one, two]\n"})
		Expect(lola("profile", "switch", "work", "--user", "-y").code).To(Equal(0))
		r := lola("uninstall", "--user", "c", "-y")
		Expect(r.code).To(Equal(3), r.err)
		Expect(r.err).To(Equal("user scope: bundle work (one, two) over default\n"+
			"error: c comes from profile two; lola uninstall needs to know whether to remove it there, and there is no terminal to ask on\n"+
			"  hint: lola uninstall c --user --yes --profile user:two\n"), "-y does not choose between layers")

		r = lola("uninstall", "--user", "c", "--yes", "--profile", "user:two")
		Expect(r.code).To(Equal(0), r.err)
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "two.yml"))).To(Equal([]byte("version: 1\n")))
	})

	It("uninstall without -y refuses off a terminal, and its hint adds -y", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		Expect(lola("profile", "on", "go", "--user", "-y").code).To(Equal(0))
		r := lola("uninstall", "--user", "c")
		Expect(r.code).To(Equal(3), r.err)
		Expect(r.err).To(Equal("user scope: profile go over default\n" +
			"error: c comes from default (base); lola uninstall needs to know whether to remove it there, and there is no terminal to ask on\n" +
			"  hint: lola uninstall c --user --profile base -y\n"))
		r = lola("uninstall", "--user", "c", "--profile", "base", "-y")
		Expect(r.code).To(Equal(0), r.err)
	})

	It("uninstall -t <one target> -y off a terminal still asks which layer a bundled module changes in", func() {
		beta := filepath.Join(tmp, "mods", "beta")
		writeTree(beta, map[string]string{"lola.yml": "name: beta\n", "commands/beta.md": "B\n"})
		writeTree(p.UserProfiles, map[string]string{"mixed-modules.yml": "version: 1\ntargets: [claude-code, opencode]\nmodules:\n  - {name: beta, source: " + beta + "}\n",
			"docs.yml": "version: 1\n", "work.yml": "version: 1\nbundle: [mixed-modules, docs]\n"})
		Expect(lola("profile", "switch", "work", "--user", "-y").code).To(Equal(0))
		Expect(lola("sync", "--user").code).To(Equal(0))
		r := lola("uninstall", "--user", "beta", "-t", "opencode", "-y")
		Expect(r.code).To(Equal(3), r.err)
		Expect(r.err).To(ContainSubstring("lola uninstall needs to know whether to change it there, and there is no terminal to ask on"))
	})

	It("uninstall -y removes a module from the one bundle member that lists it", func() {
		beta := filepath.Join(tmp, "mods", "beta")
		writeTree(beta, map[string]string{"lola.yml": "name: beta\n", "commands/beta.md": "B\n"})
		writeTree(p.UserProfiles, map[string]string{"mixed-modules.yml": "version: 1\nmodules:\n  - {name: beta, source: " + beta + "}\n",
			"docs.yml": "version: 1\n", "work.yml": "version: 1\nbundle: [mixed-modules, docs]\n"})
		Expect(lola("profile", "switch", "work", "--user", "-y").code).To(Equal(0))
		Expect(lola("sync", "--user").code).To(Equal(0))
		r := lola("uninstall", "--user", "beta", "-y", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out + r.err).To(ContainSubstring("would remove beta from profile mixed-modules"))
		r = lola("uninstall", "--user", "beta", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "mixed-modules.yml"))).To(Equal([]byte("version: 1\n")))
		Expect(lola("list", "--user", "--plain").out).NotTo(ContainSubstring("beta"))
	})

	It("uninstall that removes no files edits the file without asking for confirmation, off a terminal without -y", func() {
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code, opencode]\nmodules:\n  - {name: c, source: " + c + "}\n" +
			"  - {name: a, source: " + a + "}\n"})
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: " + c + "}\n",
			"work.yml": "version: 1\nmodules:\n  - {name: a, source: " + a + "}\n"})
		Expect(lola("profile", "on", "go", "--user", "-y").code).To(Equal(0))

		r := lola("uninstall", "--user", "a", "--profile", "work")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out + r.err).To(ContainSubstring("saved to profile work; it is not on at user scope → lola profile --user switch work"))
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "work.yml"))).To(Equal([]byte("version: 1\n")))

		r = lola("uninstall", "--user", "c", "--profile", "base", "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out + r.err).To(ContainSubstring("c: profile go's entry wins; this change to default (base) applies when it is off"))
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "default.yml"))).To(Equal([]byte("version: 1\ntargets: [claude-code, opencode]\nmodules:\n"+
			"  - {name: c, source: "+c+", targets: [claude-code]}\n  - {name: a, source: "+a+"}\n")), "narrowed, nothing removed")

		r = lola("uninstall", "--user", "c", "--profile", "base", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out + r.err).To(ContainSubstring("c stays: profile go lists it too → also remove it there: lola uninstall --user c --profile user:go"))
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "default.yml"))).To(Equal([]byte("version: 1\ntargets: [claude-code, opencode]\nmodules:\n"+
			"  - {name: a, source: "+a+"}\n")), "the entry's last target is gone: it is removed, and go keeps c installed")
		Expect(lola("list", "--user", "--plain").out).To(ContainSubstring("c\tclaude-code\tok"), "go still installs it")

		r = lola("uninstall", "--user", "c")
		Expect(r.code).To(Equal(3), "removing files still asks")
		Expect(r.err).To(ContainSubstring("needs confirmation, and there is no terminal to ask on"))
	})

	It("lola profile lists the profiles each scope can use, and shows one", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: " + c + "}\n",
			"baseline.yml": "version: 1\n", "Bad Name.yml": ""})
		proj := filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		led := ledger.New()
		led.Profile = &ledger.Active{Name: "user:go", By: ledger.ByOn}
		Expect(led.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())
		GinkgoT().Chdir(proj)

		r := lola("profile")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`SCOPE\s+NAME\s+KIND\s+STATE\s+FILE\n`))
		Expect(r.out).To(MatchRegexp(`\nproject\s+baseline\s+user\s+off\s+` + filepath.Join(p.UserProfiles, "baseline.yml") + `\n`))
		Expect(r.out).To(MatchRegexp(`\nproject\s+go\s+user\s+on\s+`))
		Expect(r.out).To(MatchRegexp(`\nuser\s+default\s+default\s+base\s+`))
		Expect(r.out).To(MatchRegexp(`\nuser\s+go\s+user\s+off\s+`))
		Expect(strings.Count(r.out, "⚠ 1 file with an invalid name in "+p.UserProfiles+" is not a profile\n")).To(Equal(1), "said once for both scopes")
		Expect(r.out).NotTo(ContainSubstring("Bad Name"))
		Expect(lola("profile", "--user", "--plain").out).To(Equal("user\tbaseline\tuser\toff\t\t\t" + filepath.Join(p.UserProfiles, "baseline.yml") + "\n" +
			"user\tdefault\tdefault\tbase\t\t\t" + filepath.Join(p.UserProfiles, "default.yml") + "\n" +
			"user\tgo\tuser\toff\t\t\t" + filepath.Join(p.UserProfiles, "go.yml") + "\n" +
			"notice\twarn\t1 file with an invalid name in " + p.UserProfiles + " is not a profile\n"))
		var v struct {
			Profiles []map[string]any `json:"profiles"`
			Notices  []any            `json:"notices"`
		}
		Expect(json.Unmarshal([]byte(lola("profile", "--project", "--json").out), &v)).To(Succeed())
		Expect(v.Profiles).To(ConsistOf(
			And(HaveKeyWithValue("name", "baseline"), HaveKeyWithValue("state", "off"), HaveKeyWithValue("bundle", false), HaveKeyWithValue("scope", "project"),
				HaveKeyWithValue("shadowed_by", BeNil()), HaveKeyWithValue("members", BeEmpty()), HaveKeyWithValue("source", ""), Not(HaveKey("position"))),
			And(HaveKeyWithValue("name", "go"), HaveKeyWithValue("state", "on"), HaveKeyWithValue("active", true))))

		r = lola("profile", "go")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("Profile user:go (" + filepath.Join(p.UserProfiles, "go.yml") + ")\n" +
			"project (" + proj + "): on\n  c  profile:user:go\n" +
			"user: off; switched to, its modules would come from:\n  c  profile:user:go  (over profile:default)\n"))
		var st struct {
			Profiles []struct {
				Scope   string           `json:"scope"`
				Modules []map[string]any `json:"modules"`
			} `json:"profiles"`
		}
		Expect(json.Unmarshal([]byte(lola("profile", "go", "--json").out), &st)).To(Succeed())
		Expect(st.Profiles).To(HaveLen(2))
		Expect(st.Profiles[1].Modules).To(ConsistOf(And(HaveKeyWithValue("name", "c"), HaveKeyWithValue("from", "profile:user:go"),
			HaveKeyWithValue("overrides", ConsistOf("profile:default")))))

		r = lola("profile", "go", "baseline")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: name one profile to show\n  hint: list them all: lola profile\n" +
			"  hint: or show one: lola profile go\n  hint: or show one: lola profile baseline\n"))
		r = lola("profile", "absent", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal(userContext + "error: no profile absent for user scope\n  hint: make it: lola profile switch absent --new --user\n  hint: profiles user scope can use: user:baseline, default, user:go\n"))
		expectRunnable("lola profile switch absent --new --user")
	})

	It("lists where each profile stands, a bundle on and its members in a project and the profile on at user scope, as one glance", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n", "docs.yml": "version: 1\n", "work.yml": "version: 1\nbundle: [go, docs]\n"})
		proj := filepath.Join(tmp, "proj")
		writeTree(proj, map[string]string{".lola/profiles/review.yml": "version: 1\n"})
		Expect(lola("profile", "switch", "go", "--user").code).To(Equal(0))
		Expect(lola("profile", "switch", "work", "-C", proj).code).To(Equal(0))

		r := lola("profile", "-C", proj, "--scope", "both")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(Equal("project " + proj + ": bundle work (go, docs) over project.yml\nuser scope: profile go over default\n"))
		Expect(r.out).To(MatchRegexp(`\nproject\s+docs\s+user\s+in bundle work\s+` + regexp.QuoteMeta(filepath.Join(p.UserProfiles, "docs.yml")) + `\n`))
		Expect(r.out).To(MatchRegexp(`\nproject\s+review\s+project\s+off\s+`))
		Expect(r.out).To(MatchRegexp(`\nproject\s+work\s+user\s+on \(bundle\)\s+`))
		Expect(r.out).To(MatchRegexp(`\nuser\s+default\s+default\s+base\s+`))
		Expect(r.out).To(MatchRegexp(`\nuser\s+go\s+user\s+on\s+`))
		Expect(r.out).To(MatchRegexp(`\nuser\s+work\s+user\s+off \(bundle\)\s+`))
		Expect(lola("profile", "-C", proj, "--plain").out).To(ContainSubstring("project\twork\tuser\ton\tbundle\tuser:go,user:docs\t" + filepath.Join(p.UserProfiles, "work.yml") + "\n"))
		var v struct {
			Profiles []map[string]any `json:"profiles"`
		}
		Expect(json.Unmarshal([]byte(lola("profile", "-C", proj, "--json").out), &v)).To(Succeed())
		Expect(v.Profiles).To(ContainElements(
			And(HaveKeyWithValue("name", "work"), HaveKeyWithValue("state", "on"), HaveKeyWithValue("bundle", true), HaveKeyWithValue("members", []any{"user:go", "user:docs"})),
			And(HaveKeyWithValue("name", "go"), HaveKeyWithValue("state", "member"), HaveKeyWithValue("active", true), HaveKeyWithValue("members", BeEmpty()))))

		r = lola("profile", "docs", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("project (" + proj + "): in bundle work\n  (no modules)\n"))
	})

	It("removes a profile of yours to the trash, asking on a terminal and taking -y off one", func() {
		writeTree(p.UserProfiles, map[string]string{"try.yml": "version: 1\n"})
		try := filepath.Join(p.UserProfiles, "try.yml")
		r := lola("profile", "rm", "try", "--user")
		Expect(r.code).To(Equal(3))
		Expect(r.err).To(HaveSuffix("error: lola profile rm asks before it moves " + try + " to the trash, and there is no terminal to ask on\n" +
			"  hint: move " + try + " to the trash: lola profile --user rm user:try -y\n"))
		no := &confirming{answers: []bool{false}}
		r = lolaAsking(no, "profile", "rm", "user:try", "--user")
		Expect(r.code).To(Equal(4), "a No is a cancel")
		Expect(r.err).To(ContainSubstring("✗ cancelled — nothing was changed"))
		Expect(no.questions).To(Equal([]string{"Remove profile user:try (" + try + ")? It goes to the trash"}))
		Expect(try).To(BeARegularFile())

		r = lola("profile", "--user", "rm", "user:try", "-y", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var v map[string]any
		Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed())
		Expect(v).To(And(HaveKeyWithValue("profile", "user:try"), HaveKeyWithValue("file", try), HaveKeyWithValue("removed", true),
			HaveKeyWithValue("trash", HavePrefix(p.Trash))))
		Expect(try).NotTo(BeAnExistingFile())
		Expect(v["trash"]).To(BeAnExistingFile())
	})

	It("switches to the base by name, at user scope by default's name too", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		Expect(lola("profile", "on", "go", "--user").code).To(Equal(0))
		r := lola("profile", "switch", "base", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("Profile user:go (" + filepath.Join(p.UserProfiles, "go.yml") + ") → off\n"))
		r = lola("profile", "switch", "default", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("Notices:\n→ already on the base\n"))
		r = lola("profile", "off", "base", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HaveSuffix("error: the base is always loaded; lola profile off turns off the profile over it\n"))
	})

	It("lists a project's profiles and turns one on with the user's trust", func() {
		proj := filepath.Join(tmp, "proj")
		writeTree(proj, map[string]string{"mods/b/lola.yml": "name: b\n", "mods/b/commands/b.md": "B\n",
			".lola/profiles/docs.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: b, source: ../../mods/b}\n",
			".lola/profiles/go.yml":   "version: 1\n"})
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		GinkgoT().Chdir(proj)

		r := lola("profile", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`\nproject\s+docs\s+project\s+off\s+` + filepath.Join(proj, ".lola", "profiles", "docs.yml") + `\n`))
		Expect(r.out).To(MatchRegexp(`\nproject\s+go\s+project\s+off\s+`))
		Expect(r.out).To(MatchRegexp(`\nproject\s+go\s+user\s+off\s+`))

		r = lola("profile", "on", "go")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal(projectContext(proj) + "error: go is ambiguous: your profile user:go (" + filepath.Join(p.UserProfiles, "go.yml") +
			") and the project's project:go (" + filepath.Join(proj, ".lola", "profiles", "go.yml") + ") are both named go\n" +
			"  hint: yours: lola profile --project on user:go\n  hint: the project's: lola profile --project on project:go\n"))
		r = lola("profile", "on", "go", "-n", "--trust=hooks")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HaveSuffix("  hint: yours: lola profile --project on user:go --trust=hooks -n\n"+
			"  hint: the project's: lola profile --project on project:go --trust=hooks -n\n"), "a dry run's hints stay dry")
		r = lola("profile", "go")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal(projectContext(proj)+userContext+"error: go is ambiguous: your profile user:go ("+filepath.Join(p.UserProfiles, "go.yml")+
			") and the project's project:go ("+filepath.Join(proj, ".lola", "profiles", "go.yml")+") are both named go\n"+
			"  hint: yours: lola profile --project user:go\n  hint: the project's: lola profile --project project:go\n"),
			"the state form's hints show state")

		r = lola("profile", "on", "docs")
		Expect(r.code).To(Equal(3))
		Expect(r.err).To(ContainSubstring("  in profile project:docs (.lola/profiles/docs.yml):\n  + b             mods/b\n"))
		Expect(r.err).To(ContainSubstring("error: lola profile needs your trust for .lola/profiles/docs.yml, which has not been trusted on this machine"))
		Expect(r.err).To(ContainSubstring("hint: then accept them: lola profile --project on project:docs --trust=profiles\n"))
		r = lola("profile", "on", "project:docs", "--trust")
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(proj, ".claude", "commands", "b.md")).To(BeARegularFile())
		r = lola("profile", "docs", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("Profile project:docs (" + filepath.Join(proj, ".lola", "profiles", "docs.yml") + ")\n"))
	})

	It("puts a project profile in place of your active one only when you turn it on, as its own change", func() {
		proj := filepath.Join(tmp, "proj")
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: " + c + "}\n"})
		GinkgoT().Chdir(tmp)
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		r := lola("profile", "on", "go", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		writeTree(proj, map[string]string{"mods/b/lola.yml": "name: b\n", "mods/b/commands/b.md": "B\n",
			".lola/profiles/go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: b, source: ../../mods/b}\n"})

		r = lola("sync", "-C", proj, "--trust", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out + r.err).To(ContainSubstring("project:go also exists → lola profile -C " + proj + " on project:go\n"))
		r = lola("profile", "-C", proj)
		Expect(r.out).To(ContainSubstring("⚠ project:go also exists → lola profile -C " + proj + " on project:go\n"))

		r = lola("profile", "on", "project:go", "-C", proj, "-y")
		Expect(r.code).To(Equal(3))
		Expect(r.err).To(ContainSubstring("  ~ project:go now replaces user:go (b, c)\n"))
		Expect(r.err).To(ContainSubstring("error: lola profile needs your trust for project:go replacing user:go and .lola/profiles/go.yml, which has not been trusted on this machine, "))

		Expect(lola("profile", "on", "project:go", "-C", proj, "-n", "--plain").out).To(ContainSubstring("replace\tproject:go\tuser:go\n"))
		var dry struct {
			Trust struct {
				Replacements []map[string]any `json:"replacements"`
			} `json:"trust"`
		}
		Expect(json.Unmarshal([]byte(lola("profile", "on", "project:go", "-C", proj, "-n", "--json").out), &dry)).To(Succeed())
		Expect(dry.Trust.Replacements).To(ConsistOf(And(HaveKeyWithValue("profile", "project:go"), HaveKeyWithValue("replaces", "user:go"))))
		var plain struct {
			Trust struct {
				Replacements []any `json:"replacements"`
			} `json:"trust"`
		}
		Expect(json.Unmarshal([]byte(lola("sync", "-C", proj, "-n", "--json").out), &plain)).To(Succeed())
		Expect(plain.Trust.Replacements).To(BeEmpty())
		Expect(plain.Trust.Replacements).NotTo(BeNil(), "an empty array, never absent or null")

		conf := &confirming{trusts: []engine.TrustAnswer{{}}}
		lolaAsking(conf, "profile", "on", "project:go", "-C", proj)
		Expect(conf.steps[0].Changes).To(Equal(3), "the replacement, b, and project:go's targets")
		Expect(conf.steps[0].Groups).To(BeEmpty(), "all or none")

		r = lola("profile", "on", "project:go", "-C", proj, "--trust", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(lola("profile", "-C", proj, "--plain").out).To(ContainSubstring("project\tgo\tproject\ton\t\t\t"))
	})

	It("names a replacement that is the only change it needs trust for", func() {
		proj := filepath.Join(tmp, "proj")
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: " + c + "}\n"})
		GinkgoT().Chdir(tmp)
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		Expect(lola("profile", "on", "go", "-C", proj).code).To(Equal(0))
		// A project:go with no entries asks nothing of its own: the replacement is the only change.
		writeTree(proj, map[string]string{".lola/profiles/go.yml": "version: 1\n"})
		r := lola("profile", "switch", "project:go", "-C", proj, "-y")
		Expect(r.code).To(Equal(3))
		Expect(r.err).To(ContainSubstring("an activation would put a project profile in place of one of yours; it needs your trust for:\n  ~ project:go now replaces user:go (c)\n"))
		Expect(r.err).NotTo(MatchRegexp(`(?m)^ is unchanged`))
	})

	It("keeps the listing's columns aligned when STATE is colored", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		GinkgoT().Setenv("LOLA_COLOR", "1")
		r := lola("profile", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("\x1b[32mbase"))
		lines := strings.Split(strings.TrimSuffix(regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(r.out, ""), "\n"), "\n")
		Expect(lines).To(HaveLen(3))
		at := strings.Index(lines[0], "FILE")
		Expect(lines[1][at:]).To(HavePrefix(filepath.Join(p.UserProfiles, "default.yml")), lines[1])
		Expect(lines[2][at:]).To(HavePrefix(filepath.Join(p.UserProfiles, "go.yml")), lines[2])
	})

	It("completes profile names, minus those given", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "", "baseline.yml": ""})
		Expect(lola("__complete", "profile", "on", "").out).To(HavePrefix("baseline\ngo\n:"), "default is always on")
		Expect(lola("__complete", "profile", "on", "go", "").out).To(HavePrefix(":"), "one profile per run: nothing after the first")
		Expect(lola("__complete", "profile", "").out).To(And(ContainSubstring("\non\t"), ContainSubstring("\ngo\n")), "the verbs and the profiles to show")
	})

	It("takes each verb's aliases as the verb", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		for _, w := range []string{"on", "enable", "activate"} {
			r := lola("profile", w, "go", "--user", "-n")
			Expect(r.code).To(Equal(0), w+": "+r.err)
			Expect(r.out).To(HavePrefix("Profile user:go ("+filepath.Join(p.UserProfiles, "go.yml")+") → on\n"), w)
		}
		for _, w := range []string{"switch", "use"} {
			Expect(lola("profile", w, "go", "--user", "-n").code).To(Equal(0), w)
		}
		Expect(p.UserLedger).NotTo(BeAnExistingFile(), "nothing applied")
		Expect(lola("profile", "on", "go", "--user", "-y").code).To(Equal(0))
		for _, w := range []string{"off", "disable", "deactivate"} {
			Expect(lola("profile", w, "--user", "-n").code).To(Equal(0), w)
		}
		for _, w := range []string{"list", "ls"} {
			Expect(lola("profile", w, "--user", "--json")).To(Equal(lola("profile", "--user", "--json")), w)
		}
	})

	It("no longer takes uninstall for rm, coaching rm with the run's names and flags", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		r := lola("profile", "uninstall", "go", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: uninstall is not a lola profile command; it is lola profile rm\n" +
			"  hint: lola profile rm go --user\n"))
		Expect(filepath.Join(p.UserProfiles, "go.yml")).To(BeARegularFile())
		r = lola(append(shellWords(strings.TrimPrefix(lastHint(r.err), "lola ")), "-n")...)
		Expect(r.code).To(Equal(0), "the hint runs: "+r.err)
		Expect(r.out).To(ContainSubstring("would remove user:go"))
		writeTree(p.UserProfiles, map[string]string{"uninstall.yml": "version: 1\n"})
		Expect(lola("profile", "uninstall", "--user").code).To(Equal(0), "a profile of that name shows")
	})

	It("coaches uninstall to rm when a flag rm takes follows, keeping the flag", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		for _, flag := range []string{"-y", "--yes", "-n", "--force"} {
			r := lola("profile", "uninstall", "go", "--user", flag)
			Expect(r.code).To(Equal(1), flag)
			Expect(r.err).To(Equal("error: uninstall is not a lola profile command; it is lola profile rm\n"+
				"  hint: lola profile rm go --user "+flag+"\n"), flag)
			expectRunnable(lastHint(r.err))
		}
		Expect(filepath.Join(p.UserProfiles, "go.yml")).To(BeARegularFile())
	})

	It("coaches a bare uninstall with rm's usage and the list, not a rm that refuses", func() {
		r := lola("profile", "uninstall")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: uninstall is not a lola profile command; it is lola profile rm\n" +
			"  hint: usage: lola profile rm <name>\n" +
			"  hint: find one: lola profile list\n"))
		expectRunnable(lastHint(r.err))
	})

	It("keeps the run's scope and output flags in the find one hint", func() {
		r := lola("profile", "uninstall", "--user")
		Expect(r.err).To(HaveSuffix("  hint: find one: lola profile list --user\n"))
		expectRunnable(lastHint(r.err))
		r = lola("profile", "--on", "--user")
		Expect(r.err).To(HaveSuffix("  hint: find one: lola profile list --user\n"))
		expectRunnable(lastHint(r.err))
		r = lola("profile", "on", "--user", "--json")
		Expect(r.code).To(Equal(1))
		var d map[string]any
		Expect(json.Unmarshal([]byte(r.out+r.err), &d)).To(Succeed(), r.out+r.err)
		Expect(d["hints"]).To(ContainElement("find one: lola profile list --json --user"))
		expectRunnable("find one: lola profile list --json --user")
		r = lola("profile", "rm", "--user", "-y")
		Expect(r.err).To(ContainSubstring("  hint: find one: lola profile list --user\n"), "never -y, which list does not take")
		r = lola("profile", "move", "--user")
		Expect(r.err).To(ContainSubstring("  hint: find one: lola profile show <profile>\n"), "a find with a placeholder stays as it is")
	})

	It("runs the verb when a profile has the verb's name, saying how to show the profile", func() {
		writeTree(p.UserProfiles, map[string]string{"off.yml": "version: 1\n"}) // made by hand: lola refuses the name
		r := lola("profile", "off", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out + r.err).To(ContainSubstring("ran off; to show the profile named off: lola profile show off"))
		r = lola("profile", "show", "off", "--user", "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(`"name": "off"`), "the file still loads and shows")
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		r = lola("profile", "on", "go", "--user", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out+r.err).NotTo(ContainSubstring("ran on"), "no note when no profile has the word")
	})

	It("notes how to show a profile named like the verb from every verb, alias and refusal too", func() {
		writeTree(p.UserProfiles, map[string]string{"ls.yml": "version: 1\n", "edit.yml": "version: 1\n", "rm.yml": "version: 1\n"})
		note := "ran ls; to show the profile named ls: lola profile show ls"
		r := lola("profile", "ls", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(HavePrefix(userContext), "the context line comes first")
		Expect(r.err).To(ContainSubstring(note))
		expectRunnable(note)
		r = lola("profile", "ls", "--user", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var d map[string]any
		Expect(json.Unmarshal([]byte(r.out), &d)).To(Succeed(), r.out)
		Expect(d["notices"]).To(ContainElement(HaveKeyWithValue("text", note)), "in the notices of --json")
		Expect(r.err).To(BeEmpty())
		r = lola("profile", "edit", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("ran edit; to show the profile named edit: lola profile show edit"), "a refusal in the run")
		r = lola("profile", "rm", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("ran rm; to show the profile named rm: lola profile show rm"), "a refusal of the operands")
		Expect(lola("profile", "list", "--user").err).NotTo(ContainSubstring("ran list"), "no note when no profile has the word")
	})

	It("refuses to make a profile named after a profile verb or alias", func() {
		for _, w := range []string{"use", "on", "ls", "rm", "remove", "show"} {
			r := lola("profile", "switch", w, "--new", "--user")
			Expect(r.code).NotTo(Equal(0), w)
			Expect(r.err).To(ContainSubstring(w+" is a lola profile command, so no profile can be named "+w), w)
			Expect(lastHint(r.err)).To(Equal("name it otherwise: lola profile switch "+w+"-1 --new --user"), w)
			expectRunnable(strings.TrimPrefix(lastHint(r.err), "name it otherwise: "))
			Expect(filepath.Join(p.UserProfiles, w+".yml")).NotTo(BeAnExistingFile(), w)
		}
		r := lola("profile", "switch", "user:on", "--new", "--user")
		Expect(r.err).To(ContainSubstring("on is a lola profile command, so no profile can be named on"))
		Expect(lastHint(r.err)).To(Equal("name it otherwise: lola profile switch user:on-1 --new --user"), "the kind is kept")
		kit := filepath.Join(tmp, "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "K\n"})
		writeTree(p.UserProfiles, map[string]string{"work.yml": "version: 1\nbundle: [a]\nmodules:\n  - {name: kit, source: " + kit + "}\n",
			"a.yml": "version: 1\n"})
		r = lola("profile", "split", "work", "--to", "list", "--user")
		Expect(r.err).To(ContainSubstring("list is a lola profile command, so no profile can be named list"))
		Expect(lastHint(r.err)).To(Equal("name it otherwise: lola profile split work --user --to list-1"), "another name, never the refused run")
		r = lola(append(shellWords(strings.TrimPrefix(lastHint(r.err), "name it otherwise: lola ")), "-y")...)
		Expect(r.code).To(Equal(0), "the hint runs: "+r.err)
		Expect(filepath.Join(p.UserProfiles, "list-1.yml")).To(BeARegularFile())
		r = lola("profile", "edit", "off", "--new", "--user")
		Expect(r.err).To(ContainSubstring("off is a lola profile command, so no profile can be named off"))
		r = lola("profile", "add", "https://example.com/p.git", "update", "--user")
		Expect(r.err).To(ContainSubstring("update is a lola profile command, so no profile can be named update"))
		Expect(lastHint(r.err)).To(Equal("name it otherwise: lola profile add https://example.com/p.git update-1 --user"))
		Expect(lola("profile", "switch", "go-1", "--new", "--user", "-n").code).To(Equal(0), "another name is made")
	})

	It("parses scope flags before the verb", func() {
		Expect(lola("profile", "--user", "list", "--json").code).To(Equal(0))
	})

	It("lists with profile list as bare profile does", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		Expect(lola("profile", "list", "--json")).To(Equal(lola("profile", "--json")))
		Expect(lola("profile", "list", "--user", "--plain")).To(Equal(lola("profile", "--user", "--plain")))
	})

	It("shows with profile <name> as profile show <name> does, and refuses two names", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		Expect(lola("profile", "go", "--user", "--json")).To(Equal(lola("profile", "show", "go", "--user", "--json")))
		r := lola("profile", "show", "go", "docs", "--user")
		Expect(r.err).To(Equal("error: name one profile to show\n  hint: list them all: lola profile --user\n" +
			"  hint: or show one: lola profile go --user\n  hint: or show one: lola profile docs --user\n"))
		r = lola("profile", "show", "--user")
		Expect(r.err).To(HavePrefix("error: lola profile show needs a profile\n  hint: usage: lola profile show <name>\n  hint: find one: lola profile list --user\n"))
	})

	It("refuses a near-verb word that names no profile, keeping the operands", func() {
		r := lola("profile", "swtich", "go", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring(`unknown profile or command "swtich"`))
		Expect(r.err).To(ContainSubstring("did you mean: lola profile switch go --user"))
		expectRunnable("lola profile switch go --user")
	})

	It("coaches the old action flag, keeping the scope and other flags", func() {
		r := lola("profile", "go", "--on", "--user", "-n")
		Expect(r.err).To(ContainSubstring("--on is not a flag of lola profile"))
		Expect(lastHint(r.err)).To(Equal("lola profile on go --user -n"))
		expectRunnable(lastHint(r.err))
		r = lola("profile", "go", "--add", "https://example.com/p.git", "--ref", "v1")
		Expect(lastHint(r.err)).To(Equal("lola profile add https://example.com/p.git go --ref v1"))
		expectRunnable(lastHint(r.err))
		r = lola("profile", "--on")
		Expect(r.err).To(Equal("error: --on is not a flag of lola profile\n  hint: usage: lola profile on <name>\n  hint: find one: lola profile list\n"),
			"a run the verb refuses is no hint")
		r = lola("profile", "--update")
		Expect(r.err).To(ContainSubstring("hint: usage: lola profile update [<name>...]"))
	})

	It("coaches a sibling verb's flag on a verb", func() {
		r := lola("profile", "on", "go", "--to", "me")
		Expect(r.err).To(ContainSubstring("--to applies to lola profile move and split"))
		Expect(r.err).To(ContainSubstring("hint: usage: lola profile move <module>... --to me\n"))
		Expect(r.err).To(ContainSubstring("hint: usage: lola profile split <name> --to me\n"))
	})

	It("names the verbs that take -y on the bare noun", func() {
		r := lola("profile", "-y")
		Expect(r.err).To(ContainSubstring("--yes applies to"))
		Expect(r.err).To(ContainSubstring("lola profile off"))
	})

	It("hints add's swapped order when the name looks like a source", func() {
		for _, src := range []string{"https://example.com/p.git", "git@example.com:acme/p", "./p.git"} {
			r := lola("profile", "add", "go", src, "-n")
			Expect(r.code).To(Equal(1), src)
			Expect(r.err).To(HaveSuffix("  hint: lola profile add "+src+" go --dry-run\n"), src)
			expectRunnable("lola profile add " + src + " go --dry-run")
		}
	})

	It("refuses update with names and --all, and with neither, in its Args", func() {
		r := lola("profile", "update", "go", "--all")
		Expect(r.err).To(HavePrefix("error: --all updates every remote profile you have, and takes no names\n"))
		Expect(cli.ResolveHint([]string{"lola", "profile", "update", "go", "--all"})).NotTo(Succeed())
		Expect(cli.ResolveHint([]string{"lola", "profile", "update", "--all"})).To(Succeed())
	})

	It("coaches each old action flag of a run to its verb", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		r := lola("profile", "go", "--user", "--on", "--off")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: --on is not a flag of lola profile\n" +
			"  hint: lola profile on go --user\n  hint: lola profile off go --user\n"))
		expectRunnable("lola profile on go --user")
		expectRunnable("lola profile off go --user")
		r = lola("profile", "go", "--user", "--enable", "-n")
		Expect(r.err).To(Equal("error: --enable is not a flag of lola profile\n  hint: lola profile on go --user -n\n"),
			"an old alias flag coaches its verb")
		Expect(lola("profile", "--user", "on").err).To(HavePrefix("error: lola profile on needs a profile\n"))
	})

	It("coaches an old action flag typed after a flag of a verb, keeping that flag", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		r := lola("profile", "go", "-n", "--on")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: --on is not a flag of lola profile\n  hint: lola profile on go -n\n"))
		expectRunnable(lastHint(r.err))
		r = lola("profile", "-y", "go", "--off", "--user")
		Expect(r.err).To(Equal("error: --off is not a flag of lola profile\n  hint: lola profile off go -y --user\n"))
		expectRunnable(lastHint(r.err))
		r = lola("profile", "go", "--to", "me", "--move", "a")
		Expect(lastHint(r.err)).To(Equal("lola profile move a --from go --to me"), "the old flag's lead and value, the sibling's value")
		expectRunnable(lastHint(r.err))
	})

	It("keeps each flag of a shorthand cluster the verb takes, so -ny stays a dry run", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		r := lola("profile", "go", "--on", "-ny")
		Expect(r.err).To(Equal("error: --on is not a flag of lola profile\n  hint: lola profile on go -n -y\n"))
		expectRunnable(lastHint(r.err))
		r = lola("profile", "uninstall", "go", "--user", "-yn")
		Expect(r.err).To(Equal("error: uninstall is not a lola profile command; it is lola profile rm\n" +
			"  hint: lola profile rm go --user -y -n\n"))
		expectRunnable(lastHint(r.err))
		Expect(filepath.Join(p.UserProfiles, "go.yml")).To(BeARegularFile())
	})

	DescribeTable("coaches a flag the verb does not take, naming the verbs that do",
		func(args []string, want string) {
			writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
			r := lola(append([]string{"profile"}, args...)...)
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(HavePrefix(want))
		},
		Entry("-y on the noun", []string{"go", "-y"}, "error: --yes applies to lola profile on, off, switch, edit, move, split, update and rm\n"),
		Entry("--trust on the noun", []string{"go", "--trust"}, "error: --trust applies to lola profile on, off, switch, edit, move, split, add and update\n"),
		Entry("-n on the noun", []string{"go", "-n", "--user"}, "error: --dry-run applies to lola profile on, off, switch, edit, move, split, add, update and rm\n"),
		Entry("--ref on on", []string{"on", "go", "--ref", "v1"}, "error: --ref applies to lola profile edit and add\n"),
		Entry("--path on the noun", []string{"go", "--path", "x"}, "error: --path applies to lola profile edit and add\n"),
		Entry("-y on add", []string{"add", "https://example.com/p.git", "go", "-y"}, "error: --yes applies to lola profile on, off, switch, edit, move, split, update and rm\n"),
		Entry("--force on add", []string{"add", "https://example.com/p.git", "go", "--force"}, "error: --force applies to lola profile on, off, switch, edit, move, split and rm\n"),
		Entry("--ref on update", []string{"update", "go", "--ref", "v1"}, "error: --ref applies to lola profile edit and add\n"),
	)

	It("turns profiles on and off, asking before it removes files", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: " + a + "}\n  - {name: c, source: " + c + "}\n"})
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: " + c + "}\n"})
		inHome := func(name string) string { return filepath.Join(p.Home, ".claude", "commands", name+".md") }
		Expect(lola("sync", "--user").code).To(Equal(0))

		r := lola("profile", "on", "go", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("Profile user:go (" + filepath.Join(p.UserProfiles, "go.yml") + ") → on\n"))
		Expect(r.out).To(MatchRegexp(`\n  \+ a\s+` + a + `\s+profile:user:go\n`))
		Expect(r.out).To(MatchRegexp(`\n  ~ c\s+` + c + `\s+profile:user:go overrides profile:default\n`))
		Expect(inHome("a")).To(BeARegularFile())

		r = lola("profile", "off", "go", "--user")
		Expect(r.code).To(Equal(3))
		Expect(r.err).To(ContainSubstring("error: lola profile needs your confirmation to remove a, and there is no terminal to ask on\n" +
			"  hint: see them: lola profile --user off user:go -n\n  hint: then confirm them: lola profile --user off user:go -y\n"))
		Expect(inHome("a")).To(BeARegularFile())

		ask := &confirming{answers: []bool{false}}
		r = lolaAsking(ask, "profile", "off", "go", "--user")
		Expect(r.code).To(Equal(4), r.err)
		Expect(ask.questions).To(Equal([]string{"Remove these and apply? No applies nothing"}))
		Expect(r.err).To(ContainSubstring("removals declined; user scope was not changed."))
		Expect(inHome("a")).To(BeARegularFile())
		led, err := ledger.Load(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Profile).To(Equal(&ledger.Active{Name: "user:go", By: ledger.ByOn}))

		r = lola("profile", "off", "go", "--user", "-y", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		checkJSON(doc, "")
		Expect(doc).To(HaveKeyWithValue("profile", BeNil()))
		Expect(doc).To(HaveKeyWithValue("changes", ConsistOf(
			map[string]any{"module": "a", "action": "unload", "from": "profile:user:go", "over": []any{}},
			map[string]any{"module": "c", "action": "replace", "from": "profile:default", "over": []any{}})))
		Expect(inHome("a")).NotTo(BeAnExistingFile())
		Expect(inHome("c")).To(BeARegularFile(), "default still wants c")
	})

	It("makes a profile with --switch --new, saying so, and refuses its modifiers alone", func() {
		r := lola("profile", "switch", "try", "--new", "--user", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("→ would make profile try (" + filepath.Join(p.UserProfiles, "try.yml") + ", empty)\n"))
		Expect(r.out).NotTo(ContainSubstring("✓ new"), "-n makes nothing")
		r = lola("profile", "switch", "work", "--new", "--bundle", "try", "--user", "-n", "--plain")
		Expect(r.out).To(HavePrefix("would-create\tuser:try\t" + filepath.Join(p.UserProfiles, "try.yml") + "\nwould-create\tuser:work\t" +
			filepath.Join(p.UserProfiles, "work.yml") + "\n"))
		Expect(filepath.Join(p.UserProfiles, "try.yml")).NotTo(BeAnExistingFile())
		r = lola("profile", "switch", "try", "--new", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("✓ new profile try (" + filepath.Join(p.UserProfiles, "try.yml") + ", empty)\nProfile user:try (" +
			filepath.Join(p.UserProfiles, "try.yml") + ") → on\n"))
		r = lola("profile", "switch", "work", "--create", "--bundle", "try,docs", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("✓ new profile docs (" + filepath.Join(p.UserProfiles, "docs.yml") + ", empty)\n✓ new bundle work (try, docs)\n"))
		Expect(r.out).To(HaveSuffix("now: user scope: bundle work (try, docs) over default\n"))

		r = lola("profile", "switch", "work2", "--bundle", "try", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HaveSuffix("error: --bundle makes a new profile and goes with --new\n  hint: make it: lola profile switch work2 --bundle try --user --new\n"))
		r = lola("profile", "switch", "try", "--from", "docs", "--user")
		Expect(r.err).To(HaveSuffix("error: --from makes a new profile and goes with --new\n  hint: switch to the one that exists: lola profile switch try --user\n"))
		r = lola("profile", "switch", "x", "--new", "--from", "try", "--bundle", "docs", "--user")
		Expect(r.err).To(HaveSuffix("error: --from copies a profile and --bundle combines several; pick one\n" +
			"  hint: keep --from: lola profile switch x --from try --new --user\n  hint: keep --bundle: lola profile switch x --bundle docs --new --user\n"))
		r = lola("profile", "on", "x", "--new", "--user")
		Expect(r.err).To(HaveSuffix("error: --new applies to lola profile switch and edit\n" +
			"  hint: usage: lola profile switch <name> --new\n  hint: usage: lola profile edit [<name>] <module>... --new\n"))
	})

	It("refuses a --switch --new that removes files off a terminal, hinting the same run with -y, which runs as printed", func() {
		g := filepath.Join(tmp, "mods", "g")
		writeTree(g, map[string]string{"lola.yml": "name: g\n", "commands/g.md": "G\n"})
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: g, source: " + g + "}\n"})
		Expect(lola("profile", "switch", "go", "--user").code).To(Equal(0))
		gmd := filepath.Join(p.Home, ".claude", "commands", "g.md")
		Expect(gmd).To(BeARegularFile())

		r := lola("profile", "switch", "work", "--new", "--bundle", "docs,notes", "--user")
		Expect(r.code).To(Equal(3))
		run := "lola profile --user switch user:work --new --bundle docs,notes"
		Expect(r.err).To(HaveSuffix("error: lola profile needs your confirmation to remove g, and there is no terminal to ask on\n" +
			"  hint: see them: " + run + " -n\n  hint: then confirm them: " + run + " -y\n"))
		Expect(runHint(run+" -n").code).To(Equal(0), "-n shows the removal and writes nothing")
		for _, n := range []string{"work", "docs", "notes"} {
			Expect(filepath.Join(p.UserProfiles, n+".yml")).NotTo(BeAnExistingFile(), n)
		}
		r = runHint(run + " -y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("✓ new profile docs ("))
		Expect(filepath.Join(p.UserProfiles, "work.yml")).To(BeARegularFile())
		Expect(gmd).NotTo(BeAnExistingFile(), "-y answered the removal")
	})

	It("hints --from or --bundle without --new as the run's scope finds the name, a project's profile included", func() {
		proj := filepath.Join(tmp, "proj")
		writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\n", ".lola/profiles/go.yml": "version: 1\n"})
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		GinkgoT().Chdir(proj)
		r := lola("profile", "switch", "team", "--from", "go")
		Expect(r.err).To(HaveSuffix("error: --from makes a new profile and goes with --new\n  hint: switch to the one that exists: lola profile switch team\n"))
		r = runHint("lola profile switch team --trust=profiles")
		Expect(r.code).To(Equal(0), r.err)

		r = lola("profile", "switch", "go", "--bundle", "team")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HaveSuffix("error: --bundle makes a new profile and goes with --new; go is ambiguous: your profile user:go ("+
			filepath.Join(p.UserProfiles, "go.yml")+") and the project's project:go ("+filepath.Join(proj, ".lola", "profiles", "go.yml")+
			") are both named go\n  hint: yours: lola profile --project user:go\n  hint: the project's: lola profile --project project:go\n"),
			"go is both kinds': neither a switch nor --new takes it bare")
		Expect(runHint("lola profile --project project:go").code).To(Equal(0))
	})

	It("refuses two names for switch or on, hinting the bundle of them, which runs as printed", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n", "docs.yml": "version: 1\n"})
		for _, act := range []string{"switch", "on"} {
			r := lola("profile", act, "go", "docs", "--user")
			Expect(r.code).To(Equal(1), act)
			Expect(r.err).To(Equal("error: one profile is on at a time; to combine go and docs make a bundle\n"+
				"  hint: lola profile --user switch go-docs --new --bundle go,docs\n"), act)
		}
		r := lola("profile", "switch", "go", "docs", "--user", "-n", "--trust=profiles", "-y", "--force")
		Expect(r.err).To(Equal("error: one profile is on at a time; to combine go and docs make a bundle\n"+
			"  hint: lola profile --user switch go-docs --new --bundle go,docs --trust=profiles -n\n"), "the run's flags kept, never -y or --force")
		r = runHint(strings.TrimPrefix(strings.Split(r.err, "\n")[1], "  hint: "))
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(p.UserProfiles, "go-docs.yml")).NotTo(BeAnExistingFile(), "the hint keeps -n")
		r = lola("profile", "--user", "switch", "go-docs", "--new", "--bundle", "go,docs")
		Expect(r.code).To(Equal(0), r.err)
		r = lola("profile", "off", "go", "docs", "--user")
		Expect(r.err).To(Equal("error: one profile is on at a time; off takes one name\n  hint: lola profile off go --user\n  hint: lola profile off docs --user\n"))
	})

	It("splits a mixed profile with --split, and a command that loads one offers the split on a terminal and hints it off one", func() {
		work := filepath.Join(p.UserProfiles, "work.yml")
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n", "work.yml": "version: 1\nbundle: [go]\nmodules:\n  - {name: a, source: ../a}\n"})
		r := lola("profile", "split", "work", "--user")
		Expect(r.code).To(Equal(3))
		Expect(r.err).To(HaveSuffix("error: lola profile split rewrites " + work + " and makes " + filepath.Join(p.UserProfiles, "work-modules.yml") +
			", and there is no terminal to ask on\n  hint: split it: lola profile --user split work -y\n"))

		writeTree(p.UserProfiles, map[string]string{"work.yml": "version: 1\nbundle: [go]\nmodules:\n  - {name: a, source: " + a + "}\n"})
		led := ledger.New()
		led.Profile = &ledger.Active{Name: "user:work", By: ledger.ByOn}
		Expect(led.Save(p.UserLedger)).To(Succeed())
		r = lola("list", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HaveSuffix("error: " + work + ":2: work has both bundle: and modules:; a bundle combines profiles and a profile lists modules\n" +
			"  hint: move its modules into a profile of their own: lola profile --user split work\n"))

		no := &confirming{answers: []bool{false}}
		r = lolaAsking(no, "sync", "--user", "-y", "--force")
		Expect(r.code).To(Equal(1))
		Expect(no.questions).To(Equal([]string{"Split work now?"}), "-y and --force of the other command never answer it")
		mixedErr := work + ":2: work has both bundle: and modules:; a bundle combines profiles and a profile lists modules"
		Expect(r.err).To(Equal("user scope: profile work (unreadable: "+mixedErr+") over default\n"+
			"  ~ user:work  bundle: go, work-modules  ("+work+")\n"+
			"  + user:work-modules  modules: a  ("+filepath.Join(p.UserProfiles, "work-modules.yml")+", new)\n"+
			"error: "+mixedErr+"\n"+
			"  hint: move its modules into a profile of their own: lola profile --user split work\n"), "the plan alone before the question, then the refusal once")
		yes := &confirming{answers: []bool{true}}
		r = lolaAsking(yes, "list", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("  ~ user:work  bundle: go, work-modules  (" + work + ")\n"))
		Expect(r.err).To(ContainSubstring("✓ split work; running it again\n"))
		Expect(os.ReadFile(work)).To(Equal([]byte("version: 1\nbundle: [go, work-modules]\n")))
	})

	It("offers the split of a project file on a terminal, asks its trust there and runs the command again; a failed question is the run's error", func() {
		proj := filepath.Join(tmp, "proj")
		team := filepath.Join(proj, ".lola", "profiles", "team.yml")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n",
			"mods/r/lola.yml": "name: r\n", "mods/r/commands/r.md": "R\n", ".lola/profiles/lint.yml": "version: 1\n",
			".lola/profiles/team.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: r, source: ../../mods/r}\n"})
		r := lolaAsking(&confirming{trusts: []engine.TrustAnswer{{All: true}}}, "profile", "on", "project:team", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		mixed := "version: 1\ntargets: [claude-code]\nbundle: [lint]\nmodules:\n  - {name: r, source: ../../mods/r}\n"
		Expect(os.WriteFile(team, []byte(mixed), 0o644)).To(Succeed())

		broken := &confirming{fail: errors.New("interrupted")}
		r = lolaAsking(broken, "sync", "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HaveSuffix("error: interrupted\n"), "the question's error, never the refusal it was asked about")
		Expect(os.ReadFile(team)).To(Equal([]byte(mixed)))

		ask := &confirming{answers: []bool{true}, trusts: []engine.TrustAnswer{{All: true}}}
		r = lolaAsking(ask, "sync", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(ask.questions).To(Equal([]string{"Split project:team now?", "trust"}), "the member list is asked about on the terminal, never refused")
		Expect(r.err).To(ContainSubstring("✓ split project:team; running it again\n"))
		Expect(os.ReadFile(team)).To(Equal([]byte("version: 1\nbundle: [lint, team-modules]\n")))
		Expect(filepath.Join(proj, ".claude", "commands", "r.md")).To(BeARegularFile())
		r = lola("sync", "-C", proj)
		Expect(r.code).To(Equal(0), "the trust given is recorded: "+r.err)
	})

	It("turns off whatever profile is on with --off and no name", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		Expect(lola("profile", "on", "go", "--user").code).To(Equal(0))
		r := lola("profile", "--user", "off")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("Profile user:go (" + filepath.Join(p.UserProfiles, "go.yml") + ") → off\n"))
		r = lola("profile", "--user", "off")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("Notices:\n→ no profile is on\n✓ no files to change\n"))
	})

	It("off and switch take --force to remove edited files without asking", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: " + a + "}\n",
			"none.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n"})
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: " + c + "}\n"})
		Expect(lola("profile", "on", "go", "--user", "-y").code).To(Equal(0))
		edited := filepath.Join(p.Home, ".claude", "commands", "a.md")
		Expect(os.WriteFile(edited, []byte("mine now\n"), 0o644)).To(Succeed())

		for _, run := range [][]string{{"off", "go"}, {"switch", "none"}} {
			r := lola(append(append([]string{"profile"}, run...), "--user", "-y")...)
			Expect(r.code).To(Equal(3), "%v", run)
			Expect(r.err).To(ContainSubstring("error: refusing to change anything: 1 conflict, which --force overrides\n"), "%v", run)
			Expect(r.err).To(ContainSubstring("lola profile "+run[0]+" "+run[1]+" --user --force\n"), "%v: the hint drops -y", run)
		}
		Expect(os.ReadFile(edited)).To(Equal([]byte("mine now\n")))
		r := lola("profile", "off", "go", "--user", "--force")
		Expect(r.code).To(Equal(0), r.err)
		Expect(edited).NotTo(BeAnExistingFile(), "--force answered the removal and the edit")
	})

	It("asks before --on replaces the profile on, on a terminal, offering the bundle of both, and refuses off one toward --switch", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n", "docs.yml": "version: 1\n"})
		Expect(lola("profile", "on", "go", "--user").code).To(Equal(0))
		refused := "error: user:go is on, and lola profile on does not replace it without asking; switch does\n" +
			"  hint: lola profile --user switch user:docs\n  hint: lola profile --user switch go-docs --new --bundle go,docs\n"
		for _, extra := range [][]string{nil, {"-y"}, {"--force"}} {
			r := lola(append([]string{"profile", "on", "docs", "--user"}, extra...)...)
			Expect(r.code).To(Equal(1), "%v", extra)
			Expect(r.err).To(HaveSuffix(refused), "%v answers removals only", extra)
		}
		question := cli.WhereStep{Title: "user:go is on.", Options: []string{"Switch to user:docs", "Make bundle go-docs of go and docs"}}
		for _, extra := range []string{"-y", "--force"} {
			no := &confirming{scripted: scripted{where: []int{-1}}}
			r := lolaAsking(no, "profile", "on", "docs", "--user", extra)
			Expect(r.code).To(Equal(4), r.err)
			Expect(no.whereSteps).To(Equal([]cli.WhereStep{question}), "%s never answers it", extra)
			Expect(r.err).To(HaveSuffix("✗ cancelled — nothing was changed\n"))
			led, err := ledger.Load(p.UserLedger)
			Expect(err).NotTo(HaveOccurred())
			Expect(led.Profile).To(Equal(&ledger.Active{Name: "user:go", By: ledger.ByOn}))
		}

		dry := &confirming{}
		r := lolaAsking(dry, "profile", "on", "docs", "--user", "-n")
		Expect(r.code).To(Equal(1))
		Expect(dry.whereSteps).To(BeEmpty(), "-n asks nothing")
		Expect(r.err).To(Equal("user scope: profile go over default\n" +
			"error: user:go is on, and lola profile on does not replace it without asking; switch does\n" +
			"  hint: lola profile --user switch user:docs -n\n  hint: lola profile --user switch go-docs --new --bundle go,docs -n\n"))

		bundle := &confirming{scripted: scripted{where: []int{1}}}
		r = lolaAsking(bundle, "profile", "on", "docs", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("✓ new bundle go-docs (go, docs)\n"))
		led, err := ledger.Load(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Profile).To(Equal(&ledger.Active{Name: "user:go-docs", By: ledger.BySwitch}))

		yes := &confirming{scripted: scripted{where: []int{0}}}
		r = lolaAsking(yes, "profile", "on", "go", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(yes.whereSteps).To(Equal([]cli.WhereStep{{Title: "user:go-docs is on.", Options: []string{"Switch to user:go", "Make bundle go-docs-go of go-docs and go"}}}))
		led, err = ledger.Load(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Profile).To(Equal(&ledger.Active{Name: "user:go", By: ledger.BySwitch}))

		js := &confirming{}
		r = lolaAsking(js, "profile", "on", "docs", "--user", "--json")
		Expect(r.code).To(Equal(1))
		Expect(js.whereSteps).To(BeEmpty(), "--json never asks")
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.err), &doc)).To(Succeed(), r.err)
		Expect(doc["hints"]).To(Equal([]any{"lola profile --user switch user:docs --json", "lola profile --user switch go-docs-2 --new --bundle go,docs --json"}),
			"go-docs is taken")
	})

	It("keeps --trust in --on's --switch and bundle hints, never -y or --force", func() {
		// --switch removes files --on would not, so the hint must not carry an answer to their
		// removal (-y, --force); the run's trust answers the same entries either way and is kept.
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n", "docs.yml": "version: 1\n"})
		Expect(lola("profile", "on", "go", "--user").code).To(Equal(0))
		r := lola("profile", "on", "docs", "--user", "-y", "--force", "--trust=profiles", "-n")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("user scope: profile go over default\n" +
			"error: user:go is on, and lola profile on does not replace it without asking; switch does\n" +
			"  hint: lola profile --user switch user:docs --trust=profiles -n\n" +
			"  hint: lola profile --user switch go-docs --new --bundle go,docs --trust=profiles -n\n"))
	})

	It("an action that moves no module still says what applies it, and writes the list", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n"})
		Expect(lola("sync", "--user").code).To(Equal(0))
		before, err := ledger.Load(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())

		r := lola("profile", "on", "go", "--user", "-n", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		Expect(doc).To(HaveKeyWithValue("in_sync", false))
		Expect(doc).To(HaveKeyWithValue("changes", BeEmpty()))
		Expect(doc).To(HaveKeyWithValue("equivalent_commands", ConsistOf("lola profile --user on user:go")))
		r = lola("profile", "on", "go", "--user", "-n")
		Expect(r.out).To(HaveSuffix("→ no files to change\n→ to apply: lola profile --user on user:go\n"))
		Expect(p.UserLedger).To(BeARegularFile())
		after, err := ledger.Load(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())
		Expect(after.Profile).To(BeNil(), "a dry run writes nothing")

		r = lola("profile", "on", "go", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HaveSuffix("✓ no files to change\nnow: user scope: profile go over default\n"))
		after, err = ledger.Load(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())
		Expect(after.Profile).To(Equal(&ledger.Active{Name: "user:go", By: ledger.ByOn}))
		Expect(after.Modules).To(Equal(before.Modules), "only the list changed")
		Expect(after.Created).To(Equal(before.Created))
	})

	It("heads only the profiles a run turns on or off; one it leaves is its notice alone", func() {
		writeTree(p.UserProfiles, map[string]string{"api.yml": "version: 1\n", "go.yml": "version: 1\n"})
		Expect(lola("sync", "--user").code).To(Equal(0))
		Expect(lola("profile", "on", "go", "--user").code).To(Equal(0))
		api, goFile := filepath.Join(p.UserProfiles, "api.yml"), filepath.Join(p.UserProfiles, "go.yml")

		r := lola("profile", "switch", "api", "--user", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("Profile user:api (" + api + ") → on\n" +
			"→ no files to change\n" +
			"→ to apply: lola profile --user switch user:api\n"))
		r = lola("profile", "on", "go", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("Notices:\n→ user:go is already on\n✓ no files to change\n"))

		r = lola("profile", "off", "go", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("Profile user:go (" + goFile + ") → off\n✓ no files to change\nnow: user scope: default\n"))
		r = lola("profile", "off", "api", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("Notices:\n→ user:api is not on\n✓ no files to change\n"))
	})

	It("prints the new context last after a change of the profile on, and nothing after a dry run or no change", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		Expect(lola("profile", "switch", "go", "--user", "-n").out).NotTo(ContainSubstring("now:"))
		r := lola("profile", "switch", "go", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HaveSuffix("now: user scope: profile go over default\n"))
		Expect(r.err).To(HavePrefix(userContext), "the context the run started in")
		Expect(lola("profile", "switch", "go", "--user").out).NotTo(ContainSubstring("now:"), "nothing changed")
		Expect(lola("profile", "--user", "off").out).To(HaveSuffix("now: user scope: default\n"))

		r = lola("profile", "on", "go", "--user", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		checkJSON(doc, "")
		Expect(doc).To(HaveKeyWithValue("profile", "user:go"))
		Expect(doc["context"]).To(ConsistOf(HaveKeyWithValue("profile", BeNil())), "the context the run started in")
		Expect(r.out).NotTo(ContainSubstring("now:"))
	})

	It("a No to an activation's removals names the activation to run again", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: " + a + "}\n",
			"docs.yml": "version: 1\nmodules:\n  - {name: c, source: " + c + "}\n"}) // c installs to project.yml's targets, which trust covers
		proj := filepath.Join(tmp, "proj")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n"})
		r := lola("profile", "on", "go", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(proj, ".claude", "commands", "a.md")).To(BeARegularFile())

		ask := &confirming{trusts: []engine.TrustAnswer{{All: true}}, answers: []bool{false}}
		r = lolaAsking(ask, "profile", "switch", "docs", "-C", proj)
		Expect(r.code).To(Equal(4), r.err)
		Expect(ask.questions).To(Equal([]string{"trust", "Remove these and apply? No applies nothing"}))
		Expect(r.err).To(ContainSubstring("removals declined; the project was not changed."))
		Expect(r.err).To(ContainSubstring("The changes you trusted are recorded; the next lola profile -C " + proj + " switch user:docs asks only about the removals.\n"))
		Expect(r.err).NotTo(ContainSubstring("lola sync"))
		Expect(filepath.Join(proj, ".claude", "commands", "a.md")).To(BeARegularFile())
	})

	It("pages an action's summary", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: " + a + "}\n"})
		paged := filepath.Join(tmp, "paged")
		Expect(os.WriteFile(filepath.Join(tmp, "bin", "fakepager"), []byte("#!/bin/sh\ncat > \"$PAGED\"\n"), 0o755)).To(Succeed())
		GinkgoT().Setenv("PAGED", paged)
		GinkgoT().Setenv("LOLA_PAGER", "fakepager")
		root, err := cli.NewRootCmd(os.Getenv, cli.WithTerminals(true, true))
		Expect(err).NotTo(HaveOccurred())
		var out, errb bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errb)
		root.SetArgs([]string{"profile", "on", "go", "--user"})
		Expect(cli.Execute(root)).To(Equal(0), errb.String())
		got, err := os.ReadFile(paged)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(HavePrefix("Profile user:go (" + filepath.Join(p.UserProfiles, "go.yml") + ") → on\n"))
		Expect(out.String()).To(BeEmpty(), "the summary went to the pager")
	})

	It("lola sync completes the modules of every layer, installed or not", func() {
		Expect(lola("__complete", "sync", "--user", "").out).To(HavePrefix("a\nc\n:"))
		Expect(lola("__complete", "sync", "--user", "c", "").out).To(HavePrefix("a\n:"))
	})

	It("lola profile <name> lists each entry's only: and skip: picks, under its layer, in --plain's two last columns and as JSON arrays", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  - {name: a, source: " + a + ", only: [command/a, 'skill/*'], skip: [command/x]}\n  - {name: c, source: " + c + "}\n"})
		r := lola("profile", "go", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("user: off; switched to, its modules would come from:\n" +
			"  a  profile:user:go  (over profile:default)\n     only: command/a, skill/*\n     skip: command/x\n  c  profile:user:go  (over profile:default)\n"))
		r = lola("profile", "go", "--user", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("user\ta\tprofile:user:go\tprofile:default\tcommand/a,skill/*\tcommand/x\nuser\tc\tprofile:user:go\tprofile:default\t\t\n"))
		r = lola("profile", "go", "--user", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var v struct {
			Profiles []struct {
				Modules []map[string]any `json:"modules"`
			} `json:"profiles"`
		}
		Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed())
		Expect(v.Profiles[0].Modules).To(HaveExactElements(
			And(HaveKeyWithValue("name", "a"), HaveKeyWithValue("only", []any{"command/a", "skill/*"}), HaveKeyWithValue("skip", []any{"command/x"})),
			And(HaveKeyWithValue("name", "c"), HaveKeyWithValue("only", []any{}), HaveKeyWithValue("skip", []any{}))))
	})

	It("lola profile --plain keeps a shown profile's notices", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: " + c + "}\n"})
		led := ledger.New()
		led.Profile = &ledger.Active{Name: "user:gone", By: ledger.ByOn}
		Expect(led.Save(p.UserLedger)).To(Succeed())
		r := lola("profile", "go", "--user", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("user\tc\tprofile:user:go\tprofile:default\t\t\n" +
			"notice\twarn\tprofile user:gone is on but no longer exists → lola profile --user off user:gone\n"))
	})

	It("list warns about an active profile that no longer exists", func() {
		led := ledger.New()
		led.Profile = &ledger.Active{Name: "user:gone", By: ledger.ByOn}
		Expect(led.Save(p.UserLedger)).To(Succeed())
		r := lola("list", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("⚠ profile user:gone is on but no longer exists → lola profile --user off user:gone\n"))
		var v struct{ Notices []map[string]string }
		Expect(json.Unmarshal([]byte(lola("list", "--user", "--json").out), &v)).To(Succeed())
		Expect(v.Notices).To(ContainElement(HaveKeyWithValue("text", "profile user:gone is on but no longer exists → lola profile --user off user:gone")))
	})
})
