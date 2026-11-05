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
		Expect(lola("list", "--user", "--plain").out).To(Equal("a\tclaude-code\tok\ton\t" + a + "\tuser\tprofile:default\t\n" +
			"c\tclaude-code\tok\ton\t" + c + "\tuser\tprofile:default\t\n"))
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
		Expect(lola("list", "--user", "--plain").out).To(Equal("a\tclaude-code\tok\ton\t" + a + "\tuser\tprofile:default\t\n"))
	})

	It("Prof-P1-c: uninstall -y under a profile on removes the base's module, the one layer that lists it", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		Expect(lola("profile", "go", "--on", "--user", "-y").code).To(Equal(0))
		r := lola("uninstall", "--user", "c", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "default.yml"))).To(Equal([]byte("version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  - {name: a, source: " + a + "}\n")))
	})

	It("Prof-P1-c: uninstall -y refuses when two layers list the module, hinting --profile with -y, which works", func() {
		listC := "version: 1\nmodules:\n  - {name: c, source: " + c + "}\n"
		writeTree(p.UserProfiles, map[string]string{"one.yml": listC, "two.yml": listC, "work.yml": "version: 1\nbundle: [one, two]\n"})
		Expect(lola("profile", "work", "--switch", "--user", "-y").code).To(Equal(0))
		r := lola("uninstall", "--user", "c", "-y")
		Expect(r.code).To(Equal(1), r.err)
		Expect(r.err).To(Equal("user scope: bundle work (one, two) over default\n"+
			"error: c comes from profile two; lola uninstall needs to know whether to remove it there, and there is no terminal to ask on\n"+
			"  hint: lola uninstall c --user --yes --profile user:two\n"), "-y does not choose between layers")

		r = lola("uninstall", "--user", "c", "--yes", "--profile", "user:two")
		Expect(r.code).To(Equal(0), r.err)
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "two.yml"))).To(Equal([]byte("version: 1\n")))
	})

	It("Prof-P1-c: uninstall without -y refuses off a terminal, and its hint adds -y", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		Expect(lola("profile", "go", "--on", "--user", "-y").code).To(Equal(0))
		r := lola("uninstall", "--user", "c")
		Expect(r.code).To(Equal(1), r.err)
		Expect(r.err).To(Equal("user scope: profile go over default\n" +
			"error: c comes from default (base); lola uninstall needs to know whether to remove it there, and there is no terminal to ask on\n" +
			"  hint: lola uninstall c --user --profile base -y\n"))
		r = lola("uninstall", "--user", "c", "--profile", "base", "-y")
		Expect(r.code).To(Equal(0), r.err)
	})

	It("Prof-P1-c: uninstall -t <one target> -y off a terminal still asks which layer a bundled module changes in", func() {
		beta := filepath.Join(tmp, "mods", "beta")
		writeTree(beta, map[string]string{"lola.yml": "name: beta\n", "commands/beta.md": "B\n"})
		writeTree(p.UserProfiles, map[string]string{"mixed-modules.yml": "version: 1\ntargets: [claude-code, opencode]\nmodules:\n  - {name: beta, source: " + beta + "}\n",
			"docs.yml": "version: 1\n", "work.yml": "version: 1\nbundle: [mixed-modules, docs]\n"})
		Expect(lola("profile", "work", "--switch", "--user", "-y").code).To(Equal(0))
		Expect(lola("sync", "--user").code).To(Equal(0))
		r := lola("uninstall", "--user", "beta", "-t", "opencode", "-y")
		Expect(r.code).To(Equal(1), r.err)
		Expect(r.err).To(ContainSubstring("lola uninstall needs to know whether to change it there, and there is no terminal to ask on"))
	})

	It("Prof-P1-c: uninstall -y removes a module from the one bundle member that lists it", func() {
		beta := filepath.Join(tmp, "mods", "beta")
		writeTree(beta, map[string]string{"lola.yml": "name: beta\n", "commands/beta.md": "B\n"})
		writeTree(p.UserProfiles, map[string]string{"mixed-modules.yml": "version: 1\nmodules:\n  - {name: beta, source: " + beta + "}\n",
			"docs.yml": "version: 1\n", "work.yml": "version: 1\nbundle: [mixed-modules, docs]\n"})
		Expect(lola("profile", "work", "--switch", "--user", "-y").code).To(Equal(0))
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
		Expect(lola("profile", "go", "--on", "--user", "-y").code).To(Equal(0))

		r := lola("uninstall", "--user", "a", "--profile", "work")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out + r.err).To(ContainSubstring("saved to profile work; it is not on at user scope → lola profile --user work --switch"))
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
		Expect(r.code).To(Equal(1), "removing files still asks")
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
		r = lola("profile", "nope", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal(userContext + "error: no profile nope for user scope\n  hint: make it: lola profile nope --switch --new --user\n  hint: profiles user scope can use: user:baseline, default, user:go\n"))
	})

	It("lists where each profile stands, a bundle on and its members in a project and the profile on at user scope, as one glance (spec 2026-10-29 §4 S8)", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n", "docs.yml": "version: 1\n", "work.yml": "version: 1\nbundle: [go, docs]\n"})
		proj := filepath.Join(tmp, "proj")
		writeTree(proj, map[string]string{".lola/profiles/review.yml": "version: 1\n"})
		Expect(lola("profile", "go", "--switch", "--user").code).To(Equal(0))
		Expect(lola("profile", "work", "--switch", "-C", proj).code).To(Equal(0))

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
		r := lola("profile", "try", "--remove", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HaveSuffix("error: lola profile --remove asks before it moves " + try + " to the trash, and there is no terminal to ask on\n" +
			"  hint: move " + try + " to the trash: lola profile --user user:try --remove -y\n"))
		no := &confirming{answers: []bool{false}}
		r = lolaAsking(no, "profile", "user:try", "--remove", "--user")
		Expect(r.code).To(Equal(0), "a No is a cancel")
		Expect(r.err).To(ContainSubstring("aborted; nothing was removed."))
		Expect(no.questions).To(Equal([]string{"Remove profile user:try (" + try + ")? It goes to the trash"}))
		Expect(try).To(BeARegularFile())

		r = lola("profile", "--user", "user:try", "--remove", "-y", "--json")
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
		Expect(lola("profile", "go", "--on", "--user").code).To(Equal(0))
		r := lola("profile", "base", "--switch", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("Profile user:go (" + filepath.Join(p.UserProfiles, "go.yml") + ") → off\n"))
		r = lola("profile", "default", "--switch", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("→ already on the base\n"))
		r = lola("profile", "base", "--off", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HaveSuffix("error: the base is always loaded; lola profile --off turns off the profile over it\n"))
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

		r = lola("profile", "go", "--on")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal(projectContext(proj) + "error: go is ambiguous: your profile user:go (" + filepath.Join(p.UserProfiles, "go.yml") +
			") and the project's project:go (" + filepath.Join(proj, ".lola", "profiles", "go.yml") + ") are both named go\n" +
			"  hint: yours: lola profile --project user:go --on\n  hint: the project's: lola profile --project project:go --on\n"))
		r = lola("profile", "go", "--on", "-n", "--trust=hooks")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HaveSuffix("  hint: yours: lola profile --project user:go --on --trust=hooks -n\n"+
			"  hint: the project's: lola profile --project project:go --on --trust=hooks -n\n"), "a dry run's hints stay dry")
		r = lola("profile", "go")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal(projectContext(proj)+userContext+"error: go is ambiguous: your profile user:go ("+filepath.Join(p.UserProfiles, "go.yml")+
			") and the project's project:go ("+filepath.Join(proj, ".lola", "profiles", "go.yml")+") are both named go\n"+
			"  hint: yours: lola profile --project user:go\n  hint: the project's: lola profile --project project:go\n"),
			"the state form's hints show state")

		r = lola("profile", "docs", "--on")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("  in profile project:docs (.lola/profiles/docs.yml):\n  + b             mods/b\n"))
		Expect(r.err).To(ContainSubstring("error: lola profile needs your trust for .lola/profiles/docs.yml, which has not been trusted on this machine"))
		Expect(r.err).To(ContainSubstring("hint: then accept them: lola profile --project project:docs --on --trust=profiles\n"))
		r = lola("profile", "project:docs", "--on", "--trust")
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
		r := lola("profile", "go", "--on", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		writeTree(proj, map[string]string{"mods/b/lola.yml": "name: b\n", "mods/b/commands/b.md": "B\n",
			".lola/profiles/go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: b, source: ../../mods/b}\n"})

		r = lola("sync", "-C", proj, "--trust", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out + r.err).To(ContainSubstring("project:go also exists → lola profile -C " + proj + " project:go --on\n"))
		r = lola("profile", "-C", proj)
		Expect(r.out).To(ContainSubstring("⚠ project:go also exists → lola profile -C " + proj + " project:go --on\n"))

		r = lola("profile", "project:go", "--on", "-C", proj, "-y")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("  ~ project:go now replaces user:go (b, c)\n"))
		Expect(r.err).To(ContainSubstring("error: lola profile needs your trust for project:go replacing user:go and .lola/profiles/go.yml, which has not been trusted on this machine, "))

		Expect(lola("profile", "project:go", "--on", "-C", proj, "-n", "--plain").out).To(ContainSubstring("replace\tproject:go\tuser:go\n"))
		var dry struct {
			Trust struct {
				Replacements []map[string]any `json:"replacements"`
			} `json:"trust"`
		}
		Expect(json.Unmarshal([]byte(lola("profile", "project:go", "--on", "-C", proj, "-n", "--json").out), &dry)).To(Succeed())
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
		lolaAsking(conf, "profile", "project:go", "--on", "-C", proj)
		Expect(conf.steps[0].Changes).To(Equal(3), "the replacement, b, and project:go's targets (spec 2026-10-29 §3.10)")
		Expect(conf.steps[0].Groups).To(BeEmpty(), "all or none")

		r = lola("profile", "project:go", "--on", "-C", proj, "--trust", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(lola("profile", "-C", proj, "--plain").out).To(ContainSubstring("project\tgo\tproject\ton\t\t\t"))
	})

	It("names a replacement that is the only change it needs trust for", func() {
		proj := filepath.Join(tmp, "proj")
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: " + c + "}\n"})
		GinkgoT().Chdir(tmp)
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		Expect(lola("profile", "go", "--on", "-C", proj).code).To(Equal(0))
		// A project:go with no entries asks nothing of its own: the replacement is the only change.
		writeTree(proj, map[string]string{".lola/profiles/go.yml": "version: 1\n"})
		r := lola("profile", "project:go", "--switch", "-C", proj, "-y")
		Expect(r.code).To(Equal(1))
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
		Expect(lola("__complete", "profile", "").out).To(HavePrefix("baseline\ngo\n:"), "default is always on")
		Expect(lola("__complete", "profile", "go", "").out).To(HavePrefix(":"), "one profile per run: nothing after the first")
	})

	It("two different action flags refuse", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		for _, flags := range [][]string{{"--on", "--off"}, {"--switch", "--on"}, {"--off", "--switch"}} {
			r := lola(append([]string{"profile", "go", "--user"}, flags...)...)
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(HavePrefix("error: --"), flags[0])
			Expect(r.err).To(ContainSubstring(" cannot be combined; pick one\n"))
		}
		Expect(lola("profile", "go", "--user", "--on", "--off").err).To(Equal("error: --on and --off cannot be combined; pick one\n" +
			"  hint: keep --on: lola profile go --on --user\n  hint: keep --off: lola profile go --off --user\n"))
		Expect(lola("profile", "go", "--user", "--on", "--add", "https://example.com/p.git").err).To(HavePrefix(
			"error: --on and --add cannot be combined; pick one\n"))
		Expect(lola("profile", "go", "--user", "--off", "--switch").err).To(HavePrefix("error: --off and --switch cannot be combined; pick one\n"))
		Expect(lola("profile", "go", "--user", "--enable", "--deactivate").err).To(HavePrefix("error: --on and --off cannot be combined; pick one\n"),
			"aliases are refused by their canonical names")
		Expect(lola("profile", "go", "--user", "--use", "--disable", "-n").err).To(HavePrefix("error: --off and --switch cannot be combined; pick one\n"))
		r := lola("profile", "go", "--user", "--on", "--enable", "--activate", "-n")
		Expect(r.code).To(Equal(0), "an alias repeated with its canonical name is one action: "+r.err)
		Expect(r.out).To(HavePrefix("Profile user:go (" + filepath.Join(p.UserProfiles, "go.yml") + ") → on\n"))
		Expect(p.UserLedger).NotTo(BeAnExistingFile(), "nothing applied")
		Expect(lola("profile", "--on", "--user").err).To(Equal("error: --on needs the name of a profile\n  hint: list them: lola profile\n"))
	})

	DescribeTable("refuses an action's flags without an action, or with one that does not take them",
		func(args []string, want string) {
			writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
			r := lola(append([]string{"profile", "go"}, args...)...)
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal(want))
		},
		Entry("-y", []string{"-y"}, "error: --yes applies to --on, --off, --switch, --edit, --move, --split, --update and --remove only\n  hint: drop it: lola profile go\n"),
		Entry("--trust", []string{"--trust"},
			"error: --trust applies to --on, --off, --switch, --edit, --move, --split, --add and --update only\n  hint: drop it: lola profile go\n"),
		Entry("-n", []string{"-n", "--user"},
			"error: --dry-run applies to --on, --off, --switch, --edit, --move, --split, --add, --update and --remove only\n  hint: drop it: lola profile go --user\n"),
		Entry("--ref", []string{"--on", "--ref", "v1"}, "error: --ref applies to --edit and --add only\n  hint: drop it: lola profile go --on\n"),
		Entry("--path", []string{"--path", "x"}, "error: --path applies to --edit and --add only\n  hint: drop it: lola profile go\n"),
		Entry("-y with --add", []string{"--add", "https://example.com/p.git", "-y"},
			"error: --yes applies to --on, --off, --switch, --edit, --move, --split, --update and --remove only\n  hint: drop it: lola profile go --add https://example.com/p.git\n"),
		Entry("--force", []string{"--add", "https://example.com/p.git", "--force"},
			"error: --force applies to --on, --off, --switch, --edit, --move, --split and --remove only\n  hint: drop it: lola profile go --add https://example.com/p.git\n"),
		Entry("--ref with --update", []string{"--update", "--ref", "v1"}, "error: --ref applies to --edit and --add only\n  hint: drop it: lola profile go --update\n"),
	)

	It("turns profiles on and off, asking before it removes files", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: " + a + "}\n  - {name: c, source: " + c + "}\n"})
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: " + c + "}\n"})
		inHome := func(name string) string { return filepath.Join(p.Home, ".claude", "commands", name+".md") }
		Expect(lola("sync", "--user").code).To(Equal(0))

		r := lola("profile", "go", "--on", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("Profile user:go (" + filepath.Join(p.UserProfiles, "go.yml") + ") → on\n"))
		Expect(r.out).To(MatchRegexp(`\n  \+ a\s+` + a + `\s+profile:user:go\n`))
		Expect(r.out).To(MatchRegexp(`\n  ~ c\s+` + c + `\s+profile:user:go overrides profile:default\n`))
		Expect(inHome("a")).To(BeARegularFile())

		r = lola("profile", "go", "--off", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: lola profile needs your confirmation to remove a, and there is no terminal to ask on\n" +
			"  hint: see them: lola profile --user user:go --off -n\n  hint: then confirm them: lola profile --user user:go --off -y\n"))
		Expect(inHome("a")).To(BeARegularFile())

		ask := &confirming{answers: []bool{false}}
		r = lolaAsking(ask, "profile", "go", "--off", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(ask.questions).To(Equal([]string{"Remove these and apply? No applies nothing"}))
		Expect(r.err).To(ContainSubstring("removals declined; user scope was not changed."))
		Expect(inHome("a")).To(BeARegularFile())
		led, err := ledger.Load(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Profile).To(Equal(&ledger.Active{Name: "user:go", By: ledger.ByOn}))

		r = lola("profile", "go", "--off", "--user", "-y", "--json")
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
		r := lola("profile", "try", "--switch", "--new", "--user", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("→ would make profile try (" + filepath.Join(p.UserProfiles, "try.yml") + ", empty)\n"))
		Expect(r.out).NotTo(ContainSubstring("✓ new"), "-n makes nothing")
		r = lola("profile", "work", "--switch", "--new", "--bundle", "try", "--user", "-n", "--plain")
		Expect(r.out).To(HavePrefix("would-create\tuser:try\t" + filepath.Join(p.UserProfiles, "try.yml") + "\nwould-create\tuser:work\t" +
			filepath.Join(p.UserProfiles, "work.yml") + "\n"))
		Expect(filepath.Join(p.UserProfiles, "try.yml")).NotTo(BeAnExistingFile())
		r = lola("profile", "try", "--switch", "--new", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("✓ new profile try (" + filepath.Join(p.UserProfiles, "try.yml") + ", empty)\nProfile user:try (" +
			filepath.Join(p.UserProfiles, "try.yml") + ") → on\n"))
		r = lola("profile", "work", "--create", "--switch", "--bundle", "try,docs", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("✓ new profile docs (" + filepath.Join(p.UserProfiles, "docs.yml") + ", empty)\n✓ new bundle work (try, docs)\n"))
		Expect(r.out).To(HaveSuffix("now: user scope: bundle work (try, docs) over default\n"))

		r = lola("profile", "work2", "--switch", "--bundle", "try", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HaveSuffix("error: --bundle makes a new profile and goes with --new\n  hint: make it: lola profile work2 --bundle try --switch --user --new\n"))
		r = lola("profile", "try", "--switch", "--from", "docs", "--user")
		Expect(r.err).To(HaveSuffix("error: --from makes a new profile and goes with --new\n  hint: switch to the one that exists: lola profile try --switch --user\n"))
		r = lola("profile", "x", "--switch", "--new", "--from", "try", "--bundle", "docs", "--user")
		Expect(r.err).To(HaveSuffix("error: --from copies a profile and --bundle combines several; pick one\n" +
			"  hint: keep --from: lola profile x --from try --new --switch --user\n  hint: keep --bundle: lola profile x --bundle docs --new --switch --user\n"))
		r = lola("profile", "x", "--on", "--new", "--user")
		Expect(r.err).To(HaveSuffix("error: --new applies to --switch and --edit only\n  hint: drop it: lola profile x --on --user\n"))
	})

	It("refuses a --switch --new that removes files off a terminal, hinting the same run with -y, which runs as printed", func() {
		g := filepath.Join(tmp, "mods", "g")
		writeTree(g, map[string]string{"lola.yml": "name: g\n", "commands/g.md": "G\n"})
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: g, source: " + g + "}\n"})
		Expect(lola("profile", "go", "--switch", "--user").code).To(Equal(0))
		gmd := filepath.Join(p.Home, ".claude", "commands", "g.md")
		Expect(gmd).To(BeARegularFile())

		r := lola("profile", "work", "--switch", "--new", "--bundle", "docs,notes", "--user")
		Expect(r.code).To(Equal(1))
		run := "lola profile --user user:work --switch --new --bundle docs,notes"
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
		r := lola("profile", "team", "--switch", "--from", "go")
		Expect(r.err).To(HaveSuffix("error: --from makes a new profile and goes with --new\n  hint: switch to the one that exists: lola profile team --switch\n"))
		r = runHint("lola profile team --switch --trust=profiles")
		Expect(r.code).To(Equal(0), r.err)

		r = lola("profile", "go", "--switch", "--bundle", "team")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HaveSuffix("error: --bundle makes a new profile and goes with --new; go is ambiguous: your profile user:go ("+
			filepath.Join(p.UserProfiles, "go.yml")+") and the project's project:go ("+filepath.Join(proj, ".lola", "profiles", "go.yml")+
			") are both named go\n  hint: yours: lola profile --project user:go\n  hint: the project's: lola profile --project project:go\n"),
			"go is both kinds': neither a switch nor --new takes it bare")
		Expect(runHint("lola profile --project project:go").code).To(Equal(0))
	})

	It("refuses two names for --switch or --on, hinting the bundle of them, which runs as printed", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n", "docs.yml": "version: 1\n"})
		for _, act := range []string{"--switch", "--on"} {
			r := lola("profile", "go", "docs", act, "--user")
			Expect(r.code).To(Equal(1), act)
			Expect(r.err).To(Equal("error: one profile is on at a time; to combine go and docs make a bundle\n"+
				"  hint: lola profile --user go-docs --switch --new --bundle go,docs\n"), act)
		}
		r := lola("profile", "go", "docs", "--switch", "--user", "-n", "--trust=profiles", "-y", "--force")
		Expect(r.err).To(Equal("error: one profile is on at a time; to combine go and docs make a bundle\n"+
			"  hint: lola profile --user go-docs --switch --new --bundle go,docs --trust=profiles -n\n"), "the run's flags kept, never -y or --force")
		r = runHint(strings.TrimPrefix(strings.Split(r.err, "\n")[1], "  hint: "))
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(p.UserProfiles, "go-docs.yml")).NotTo(BeAnExistingFile(), "the hint keeps -n")
		r = lola("profile", "--user", "go-docs", "--switch", "--new", "--bundle", "go,docs")
		Expect(r.code).To(Equal(0), r.err)
		r = lola("profile", "go", "docs", "--off", "--user")
		Expect(r.err).To(Equal("error: one profile is on at a time; --off takes one name\n  hint: lola profile go --off --user\n  hint: lola profile docs --off --user\n"))
	})

	It("splits a mixed profile with --split, and a command that loads one offers the split on a terminal and hints it off one", func() {
		work := filepath.Join(p.UserProfiles, "work.yml")
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n", "work.yml": "version: 1\nbundle: [go]\nmodules:\n  - {name: a, source: ../a}\n"})
		r := lola("profile", "work", "--split", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HaveSuffix("error: lola profile --split rewrites " + work + " and makes " + filepath.Join(p.UserProfiles, "work-modules.yml") +
			", and there is no terminal to ask on\n  hint: split it: lola profile --user work --split -y\n"))

		writeTree(p.UserProfiles, map[string]string{"work.yml": "version: 1\nbundle: [go]\nmodules:\n  - {name: a, source: " + a + "}\n"})
		led := ledger.New()
		led.Profile = &ledger.Active{Name: "user:work", By: ledger.ByOn}
		Expect(led.Save(p.UserLedger)).To(Succeed())
		r = lola("list", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HaveSuffix("error: " + work + ":2: work has both bundle: and modules:; a bundle combines profiles and a profile lists modules\n" +
			"  hint: move its modules into a profile of their own: lola profile --user work --split\n"))

		no := &confirming{answers: []bool{false}}
		r = lolaAsking(no, "sync", "--user", "-y", "--force")
		Expect(r.code).To(Equal(1))
		Expect(no.questions).To(Equal([]string{"Split work now?"}), "-y and --force of the other command never answer it")
		mixedErr := work + ":2: work has both bundle: and modules:; a bundle combines profiles and a profile lists modules"
		Expect(r.err).To(Equal("user scope: profile work (unreadable: "+mixedErr+") over default\n"+
			"  ~ user:work  bundle: go, work-modules  ("+work+")\n"+
			"  + user:work-modules  modules: a  ("+filepath.Join(p.UserProfiles, "work-modules.yml")+", new)\n"+
			"error: "+mixedErr+"\n"+
			"  hint: move its modules into a profile of their own: lola profile --user work --split\n"), "the plan alone before the question, then the refusal once")
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
		r := lolaAsking(&confirming{trusts: []engine.TrustAnswer{{All: true}}}, "profile", "project:team", "--on", "-C", proj)
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
		Expect(lola("profile", "go", "--on", "--user").code).To(Equal(0))
		r := lola("profile", "--off", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("Profile user:go (" + filepath.Join(p.UserProfiles, "go.yml") + ") → off\n"))
		r = lola("profile", "--off", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("→ no profile is on\n✓ no files to change\n"))
	})

	It("--off and --switch take --force to remove edited files without asking", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: " + a + "}\n",
			"none.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n"})
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: " + c + "}\n"})
		Expect(lola("profile", "go", "--on", "--user", "-y").code).To(Equal(0))
		edited := filepath.Join(p.Home, ".claude", "commands", "a.md")
		Expect(os.WriteFile(edited, []byte("mine now\n"), 0o644)).To(Succeed())

		for _, run := range [][]string{{"go", "--off"}, {"none", "--switch"}} {
			r := lola(append(append([]string{"profile"}, run...), "--user", "-y")...)
			Expect(r.code).To(Equal(1), "%v", run)
			Expect(r.err).To(ContainSubstring("error: refusing to change anything: 1 conflict, which --force overrides\n"), "%v", run)
			Expect(r.err).To(ContainSubstring("lola profile "+run[0]+" "+run[1]+" --user --force\n"), "%v: the hint drops -y", run)
		}
		Expect(os.ReadFile(edited)).To(Equal([]byte("mine now\n")))
		r := lola("profile", "go", "--off", "--user", "--force")
		Expect(r.code).To(Equal(0), r.err)
		Expect(edited).NotTo(BeAnExistingFile(), "--force answered the removal and the edit")
	})

	It("asks before --on replaces the profile on, on a terminal, offering the bundle of both, and refuses off one toward --switch", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n", "docs.yml": "version: 1\n"})
		Expect(lola("profile", "go", "--on", "--user").code).To(Equal(0))
		refused := "error: user:go is on, and --on does not replace it without asking; --switch does\n" +
			"  hint: lola profile --user user:docs --switch\n  hint: lola profile --user go-docs --switch --new --bundle go,docs\n"
		for _, extra := range [][]string{nil, {"-y"}, {"--force"}} {
			r := lola(append([]string{"profile", "docs", "--on", "--user"}, extra...)...)
			Expect(r.code).To(Equal(1), "%v", extra)
			Expect(r.err).To(HaveSuffix(refused), "%v answers removals only", extra)
		}
		question := cli.WhereStep{Title: "user:go is on.", Options: []string{"Switch to user:docs", "Make bundle go-docs of go and docs"}}
		for _, extra := range []string{"-y", "--force"} {
			no := &confirming{scripted: scripted{where: []int{-1}}}
			r := lolaAsking(no, "profile", "docs", "--on", "--user", extra)
			Expect(r.code).To(Equal(0), r.err)
			Expect(no.whereSteps).To(Equal([]cli.WhereStep{question}), "%s never answers it", extra)
			Expect(r.err).To(HaveSuffix("aborted; nothing changed.\n"))
			led, err := ledger.Load(p.UserLedger)
			Expect(err).NotTo(HaveOccurred())
			Expect(led.Profile).To(Equal(&ledger.Active{Name: "user:go", By: ledger.ByOn}))
		}

		dry := &confirming{}
		r := lolaAsking(dry, "profile", "docs", "--on", "--user", "-n")
		Expect(r.code).To(Equal(1))
		Expect(dry.whereSteps).To(BeEmpty(), "-n asks nothing")
		Expect(r.err).To(Equal("user scope: profile go over default\n" +
			"error: user:go is on, and --on does not replace it without asking; --switch does\n" +
			"  hint: lola profile --user user:docs --switch -n\n  hint: lola profile --user go-docs --switch --new --bundle go,docs -n\n"))

		bundle := &confirming{scripted: scripted{where: []int{1}}}
		r = lolaAsking(bundle, "profile", "docs", "--on", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("✓ new bundle go-docs (go, docs)\n"))
		led, err := ledger.Load(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Profile).To(Equal(&ledger.Active{Name: "user:go-docs", By: ledger.BySwitch}))

		yes := &confirming{scripted: scripted{where: []int{0}}}
		r = lolaAsking(yes, "profile", "go", "--on", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(yes.whereSteps).To(Equal([]cli.WhereStep{{Title: "user:go-docs is on.", Options: []string{"Switch to user:go", "Make bundle go-docs-go of go-docs and go"}}}))
		led, err = ledger.Load(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Profile).To(Equal(&ledger.Active{Name: "user:go", By: ledger.BySwitch}))

		js := &confirming{}
		r = lolaAsking(js, "profile", "docs", "--on", "--user", "--json")
		Expect(r.code).To(Equal(1))
		Expect(js.whereSteps).To(BeEmpty(), "--json never asks")
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.err), &doc)).To(Succeed(), r.err)
		Expect(doc["hints"]).To(Equal([]any{"lola profile --user user:docs --switch --json", "lola profile --user go-docs-2 --switch --new --bundle go,docs --json"}),
			"go-docs is taken")
	})

	It("keeps --trust in --on's --switch and bundle hints, never -y or --force", func() {
		// --switch removes files --on would not, so the hint must not carry an answer to their
		// removal (-y, --force); the run's trust answers the same entries either way and is kept.
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n", "docs.yml": "version: 1\n"})
		Expect(lola("profile", "go", "--on", "--user").code).To(Equal(0))
		r := lola("profile", "docs", "--on", "--user", "-y", "--force", "--trust=profiles", "-n")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("user scope: profile go over default\n" +
			"error: user:go is on, and --on does not replace it without asking; --switch does\n" +
			"  hint: lola profile --user user:docs --switch --trust=profiles -n\n" +
			"  hint: lola profile --user go-docs --switch --new --bundle go,docs --trust=profiles -n\n"))
	})

	It("an action that moves no module still says what applies it, and writes the list", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n"})
		Expect(lola("sync", "--user").code).To(Equal(0))
		before, err := ledger.Load(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())

		r := lola("profile", "go", "--on", "--user", "-n", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		Expect(doc).To(HaveKeyWithValue("in_sync", false))
		Expect(doc).To(HaveKeyWithValue("changes", BeEmpty()))
		Expect(doc).To(HaveKeyWithValue("equivalent_commands", ConsistOf("lola profile --user user:go --on")))
		r = lola("profile", "go", "--on", "--user", "-n")
		Expect(r.out).To(HaveSuffix("→ to apply: lola profile --user user:go --on\n→ no files to change\n"))
		Expect(p.UserLedger).To(BeARegularFile())
		after, err := ledger.Load(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())
		Expect(after.Profile).To(BeNil(), "a dry run writes nothing")

		r = lola("profile", "go", "--on", "--user")
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
		Expect(lola("profile", "go", "--on", "--user").code).To(Equal(0))
		api, goFile := filepath.Join(p.UserProfiles, "api.yml"), filepath.Join(p.UserProfiles, "go.yml")

		r := lola("profile", "api", "--switch", "--user", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("Profile user:api (" + api + ") → on\n" +
			"→ to apply: lola profile --user user:api --switch\n" +
			"→ no files to change\n"))
		r = lola("profile", "go", "--on", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("→ user:go is already on\n✓ no files to change\n"))

		r = lola("profile", "go", "--off", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("Profile user:go (" + goFile + ") → off\n✓ no files to change\nnow: user scope: default\n"))
		r = lola("profile", "api", "--off", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("→ user:api is not on\n✓ no files to change\n"))
	})

	It("prints the new context last after a change of the profile on, and nothing after a dry run or no change", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		Expect(lola("profile", "go", "--switch", "--user", "-n").out).NotTo(ContainSubstring("now:"))
		r := lola("profile", "go", "--switch", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HaveSuffix("now: user scope: profile go over default\n"))
		Expect(r.err).To(HavePrefix(userContext), "the context the run started in")
		Expect(lola("profile", "go", "--switch", "--user").out).NotTo(ContainSubstring("now:"), "nothing changed")
		Expect(lola("profile", "--off", "--user").out).To(HaveSuffix("now: user scope: default\n"))

		r = lola("profile", "go", "--on", "--user", "--json")
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
		r := lola("profile", "go", "--on", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(proj, ".claude", "commands", "a.md")).To(BeARegularFile())

		ask := &confirming{trusts: []engine.TrustAnswer{{All: true}}, answers: []bool{false}}
		r = lolaAsking(ask, "profile", "docs", "--switch", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(ask.questions).To(Equal([]string{"trust", "Remove these and apply? No applies nothing"}))
		Expect(r.err).To(ContainSubstring("removals declined; the project was not changed."))
		Expect(r.err).To(ContainSubstring("The changes you trusted are recorded; the next lola profile -C " + proj + " user:docs --switch asks only about the removals.\n"))
		Expect(r.err).NotTo(ContainSubstring("lola sync"))
		Expect(filepath.Join(proj, ".claude", "commands", "a.md")).To(BeARegularFile())
	})

	It("pages an action's summary", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: " + a + "}\n"})
		paged := filepath.Join(tmp, "paged")
		Expect(os.WriteFile(filepath.Join(tmp, "bin", "fakepager"), []byte("#!/bin/sh\ncat > \"$PAGED\"\n"), 0o755)).To(Succeed())
		GinkgoT().Setenv("PAGED", paged)
		GinkgoT().Setenv("LOLA_PAGER", "fakepager")
		root := cli.NewRootCmd(os.Getenv, cli.WithTerminals(true, true))
		var out, errb bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errb)
		root.SetArgs([]string{"profile", "go", "--on", "--user"})
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
			"notice\twarn\tprofile user:gone is on but no longer exists → lola profile --user user:gone --off\n"))
	})

	It("list warns about an active profile that no longer exists", func() {
		led := ledger.New()
		led.Profile = &ledger.Active{Name: "user:gone", By: ledger.ByOn}
		Expect(led.Save(p.UserLedger)).To(Succeed())
		r := lola("list", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("⚠ profile user:gone is on but no longer exists → lola profile --user user:gone --off\n"))
		var v struct{ Notices []map[string]string }
		Expect(json.Unmarshal([]byte(lola("list", "--user", "--json").out), &v)).To(Succeed())
		Expect(v.Notices).To(ContainElement(HaveKeyWithValue("text", "profile user:gone is on but no longer exists → lola profile --user user:gone --off")))
	})
})
