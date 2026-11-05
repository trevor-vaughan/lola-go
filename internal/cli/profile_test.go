package cli_test

import (
	"bytes"
	"encoding/json"
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
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: " + c + "}\n"})
		writeTree(filepath.Dir(p.UserManifest), map[string]string{"user.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: " + a + "}\n"})
	})

	It("list names the layer each module comes from", func() {
		r := lola("list", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`SCOPE\s+MODULE\s+TARGET\s+STATUS\s+RULES\s+SOURCE\s+FROM\n`))
		Expect(r.out).To(MatchRegexp(`user\s+c\s+claude-code\s+⚠ not installed\s+on\s+` + c + `\s+profile:default\n`))

		r = lola("sync", "--user")
		Expect(r.code).To(Equal(0), r.err)
		r = lola("list", "--user")
		Expect(r.out).To(MatchRegexp(`user\s+a\s+claude-code\s+✓ ok\s+on\s+` + a + `\s+user.yml\n`))
		Expect(r.out).To(MatchRegexp(`user\s+c\s+claude-code\s+✓ ok\s+on\s+` + c + `\s+profile:default\n`))
		Expect(lola("list", "--user", "--plain").out).To(Equal("a\tclaude-code\tok\ton\t" + a + "\tuser\tuser.yml\n" +
			"c\tclaude-code\tok\ton\t" + c + "\tuser\tprofile:default\n"))
		var v struct {
			Modules []map[string]any `json:"modules"`
			Notices []any            `json:"notices"`
		}
		Expect(json.Unmarshal([]byte(lola("list", "--user", "--json").out), &v)).To(Succeed())
		Expect(v.Modules).To(ContainElement(And(HaveKeyWithValue("name", "c"), HaveKeyWithValue("from", "profile:default"))))
		Expect(v.Notices).NotTo(BeNil())
		Expect(v.Notices).To(BeEmpty())
	})

	It("uninstall refuses a module default wants, saying how to drop it", func() {
		Expect(lola("sync", "--user").code).To(Equal(0))
		r := lola("uninstall", "--user", "c", "-y")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: c is wanted by profile default; remove it from " +
			filepath.Join(p.UserProfiles, "default.yml") + ", which is always on at user scope\n"))
		Expect(r.err).NotTo(ContainSubstring("hint:"))
		Expect(lola("uninstall", "--user", "a", "-y").code).To(Equal(0))
	})

	It("lola profile lists the profiles each scope can use, and shows one", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: " + c + "}\n",
			"base.yml": "version: 1\n", "Bad Name.yml": ""})
		proj := filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		led := ledger.New()
		led.Profiles = []ledger.Active{{Name: "user:base", By: ledger.ByOn}, {Name: "user:go", By: ledger.ByOn}}
		Expect(led.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())
		GinkgoT().Chdir(proj)

		r := lola("profile")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`SCOPE\s+PROFILE\s+KIND\s+ACTIVE\s+PATH\n`))
		Expect(r.out).To(MatchRegexp(`\nproject\s+base\s+user\s+1st of 2\s+` + filepath.Join(p.UserProfiles, "base.yml") + `\n`))
		Expect(r.out).To(MatchRegexp(`\nproject\s+go\s+user\s+2nd of 2\s+`))
		Expect(r.out).To(MatchRegexp(`\nuser\s+default\s+default\s+always on\s+`))
		Expect(r.out).To(MatchRegexp(`\nuser\s+go\s+user\s+off\s+`))
		Expect(strings.Count(r.out, "⚠ 1 file with an invalid name in "+p.UserProfiles+" is not a profile\n")).To(Equal(1), "said once for both scopes")
		Expect(r.out).NotTo(ContainSubstring("Bad Name"))
		Expect(lola("profile", "--user", "--plain").out).To(Equal("user\tbase\tuser\toff\t\t" + filepath.Join(p.UserProfiles, "base.yml") + "\n" +
			"user\tdefault\tdefault\ton\t\t" + filepath.Join(p.UserProfiles, "default.yml") + "\n" +
			"user\tgo\tuser\toff\t\t" + filepath.Join(p.UserProfiles, "go.yml") + "\n" +
			"notice\twarn\t1 file with an invalid name in " + p.UserProfiles + " is not a profile\n"))
		var v struct {
			Profiles []map[string]any `json:"profiles"`
			Notices  []any            `json:"notices"`
		}
		Expect(json.Unmarshal([]byte(lola("profile", "--project", "--json").out), &v)).To(Succeed())
		Expect(v.Profiles).To(ConsistOf(
			And(HaveKeyWithValue("name", "base"), HaveKeyWithValue("position", 1.0), HaveKeyWithValue("scope", "project"),
				HaveKeyWithValue("shadowed_by", BeNil()), HaveKeyWithValue("extends", BeEmpty()), HaveKeyWithValue("source", "")),
			And(HaveKeyWithValue("name", "go"), HaveKeyWithValue("position", 2.0), HaveKeyWithValue("active", true))))

		r = lola("profile", "go")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("Profile user:go (" + filepath.Join(p.UserProfiles, "go.yml") + ")\n" +
			"project (" + proj + "): 2nd of 2\n  c  profile:user:go\n" +
			"user: off; turned on, its modules would come from:\n  c  profile:user:go  (over profile:default)\n"))
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

		r = lola("profile", "go", "base")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: name one profile to show\n  hint: list them all: lola profile\n" +
			"  hint: or turn them on: lola profile go base --on\n"))
		r = lola("profile", "nope", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: no profile nope for user scope\n  hint: profiles user scope can use: user:base, default, user:go\n"))
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
		Expect(r.err).To(Equal("error: go is ambiguous: your profile user:go (" + filepath.Join(p.UserProfiles, "go.yml") +
			") and the project's project:go (" + filepath.Join(proj, ".lola", "profiles", "go.yml") + ") are both named go\n" +
			"  hint: yours: lola profile --project user:go --on\n  hint: the project's: lola profile --project project:go --on\n"))
		r = lola("profile", "go", "--on", "-n", "--allow-hooks")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HaveSuffix("  hint: yours: lola profile --project user:go --on --allow-hooks -n\n"+
			"  hint: the project's: lola profile --project project:go --on --allow-hooks -n\n"), "a dry run's hints stay dry")
		r = lola("profile", "go")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: go is ambiguous: your profile user:go ("+filepath.Join(p.UserProfiles, "go.yml")+
			") and the project's project:go ("+filepath.Join(proj, ".lola", "profiles", "go.yml")+") are both named go\n"+
			"  hint: yours: lola profile --project user:go\n  hint: the project's: lola profile --project project:go\n"),
			"the state form's hints show state")

		r = lola("profile", "docs", "--on")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("  in profile project:docs (.lola/profiles/docs.yml):\n  + b             mods/b\n"))
		Expect(r.err).To(ContainSubstring("error: lola profile needs your trust for .lola/profiles/docs.yml, which has not been trusted on this machine"))
		Expect(r.err).To(ContainSubstring("hint: then accept them: lola profile --project project:docs --on --trust\n"))
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
		Expect(conf.steps[0].Changes).To(Equal(2))
		Expect(conf.steps[0].Groups).To(BeEmpty(), "all or none")

		r = lola("profile", "project:go", "--on", "-C", proj, "--trust", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(lola("profile", "-C", proj, "--plain").out).To(ContainSubstring("project\tgo\tproject\ton\t1\t"))
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

	It("keeps the listing's columns aligned when ACTIVE is colored", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		GinkgoT().Setenv("LOLA_COLOR", "1")
		r := lola("profile", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("\x1b[32malways on"))
		lines := strings.Split(strings.TrimSuffix(regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(r.out, ""), "\n"), "\n")
		Expect(lines).To(HaveLen(3))
		at := strings.Index(lines[0], "PATH")
		Expect(lines[1][at:]).To(HavePrefix(filepath.Join(p.UserProfiles, "default.yml")), lines[1])
		Expect(lines[2][at:]).To(HavePrefix(filepath.Join(p.UserProfiles, "go.yml")), lines[2])
	})

	It("completes profile names, minus those given", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "", "base.yml": ""})
		Expect(lola("__complete", "profile", "").out).To(HavePrefix("base\ngo\n:"), "default is always on")
		Expect(lola("__complete", "profile", "go", "").out).To(HavePrefix("base\n:"))
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
		Expect(r.out).To(HavePrefix("Profile user:go (" + filepath.Join(p.UserProfiles, "go.yml") + ") → on, 1st of 1\n"))
		Expect(p.UserLedger).NotTo(BeAnExistingFile(), "nothing applied")
		Expect(lola("profile", "--on", "--user").err).To(Equal("error: --on needs the names of the profiles\n  hint: list them: lola profile\n"))
	})

	DescribeTable("refuses an action's flags without an action, or with one that does not take them",
		func(args []string, want string) {
			writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
			r := lola(append([]string{"profile", "go"}, args...)...)
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal(want))
		},
		Entry("-y", []string{"-y"}, "error: --yes applies to --on, --off, --switch, --update and --remove only\n  hint: drop it: lola profile go\n"),
		Entry("--trust", []string{"--trust"},
			"error: --trust applies to --on, --off, --switch, --add and --update only\n  hint: drop it: lola profile go\n"),
		Entry("-n", []string{"-n", "--user"},
			"error: --dry-run applies to --on, --off, --switch, --add, --update and --remove only\n  hint: drop it: lola profile go --user\n"),
		Entry("--allow-hooks", []string{"--allow-hooks"},
			"error: --allow-hooks applies to --on, --off, --switch and --update only\n  hint: drop it: lola profile go\n"),
		Entry("--allow-runtime-hooks", []string{"--allow-runtime-hooks"},
			"error: --allow-runtime-hooks applies to --on, --off, --switch and --update only\n  hint: drop it: lola profile go\n"),
		Entry("--ref", []string{"--on", "--ref", "v1"}, "error: --ref applies to --add only\n  hint: drop it: lola profile go --on\n"),
		Entry("--path", []string{"--path", "x"}, "error: --path applies to --add only\n  hint: drop it: lola profile go\n"),
		Entry("-y with --add", []string{"--add", "https://example.com/p.git", "-y"},
			"error: --yes applies to --on, --off, --switch, --update and --remove only\n  hint: drop it: lola profile go --add https://example.com/p.git\n"),
		Entry("--force", []string{"--on", "--force"}, "error: --force applies to --remove only\n  hint: drop it: lola profile go --on\n"),
		Entry("--ref with --update", []string{"--update", "--ref", "v1"}, "error: --ref applies to --add only\n  hint: drop it: lola profile go --update\n"),
	)

	It("turns profiles on and off, asking before it removes files", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: " + a + "}\n  - {name: c, source: " + c + "}\n"})
		writeTree(filepath.Dir(p.UserManifest), map[string]string{"user.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n"})
		inHome := func(name string) string { return filepath.Join(p.Home, ".claude", "commands", name+".md") }
		Expect(lola("sync", "--user").code).To(Equal(0))

		r := lola("profile", "go", "--on", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("Profile user:go (" + filepath.Join(p.UserProfiles, "go.yml") + ") → on, 1st of 1\n"))
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
		Expect(led.Profiles).To(Equal([]ledger.Active{{Name: "user:go", By: ledger.ByOn}}))

		r = lola("profile", "go", "--off", "--user", "-y", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		checkJSON(doc, "")
		Expect(doc).To(HaveKeyWithValue("profiles", BeEmpty()))
		Expect(doc).To(HaveKeyWithValue("changes", ConsistOf(
			map[string]any{"module": "a", "action": "unload", "from": "profile:user:go", "over": []any{}},
			map[string]any{"module": "c", "action": "replace", "from": "profile:default", "over": []any{}})))
		Expect(inHome("a")).NotTo(BeAnExistingFile())
		Expect(inHome("c")).To(BeARegularFile(), "default still wants c")
	})

	It("refuses a conflict of --on, --off or --switch without offering the --force lola profile does not take", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: " + a + "}\n",
			"none.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n"})
		x := filepath.Join(tmp, "mods", "x")
		writeTree(x, map[string]string{"lola.yml": "name: x\n", "commands/x.md": "X\n"})
		writeTree(p.UserProfiles, map[string]string{"x.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: x, source: " + x + "}\n"})
		writeTree(filepath.Dir(p.UserManifest), map[string]string{"user.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n"})
		Expect(lola("profile", "go", "--on", "--user", "-y").code).To(Equal(0))
		writeTree(filepath.Join(p.Home, ".claude", "commands"), map[string]string{"a.md": "mine now\n", "x.md": "mine too\n"})

		for _, run := range [][]string{{"go", "--off"}, {"none", "--switch"}, {"x", "--on"}} {
			r := lola(append(append([]string{"profile"}, run...), "--user", "-y")...)
			Expect(r.code).To(Equal(1), "%v", run)
			Expect(r.err).To(HavePrefix("error: refusing to change anything: 1 conflict\n"), "%v: no --force overrides it here", run)
			Expect(r.err).NotTo(ContainSubstring("--force"), "%v", run)
			Expect(r.err).NotTo(ContainSubstring("hint:"), "%v: no hint works", run)
		}
		Expect(os.ReadFile(filepath.Join(p.Home, ".claude", "commands", "a.md"))).To(Equal([]byte("mine now\n")))
	})

	It("an action that moves no module still says what applies it, and writes the list", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: " + a + "}\n"})
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
		Expect(after.Profiles).To(BeEmpty(), "a dry run writes nothing")

		r = lola("profile", "go", "--on", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HaveSuffix("✓ no files to change\n"))
		after, err = ledger.Load(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())
		Expect(after.Profiles).To(Equal([]ledger.Active{{Name: "user:go", By: ledger.ByOn}}))
		Expect(after.Modules).To(Equal(before.Modules), "only the list changed")
		Expect(after.Created).To(Equal(before.Created))
	})

	It("heads only the profiles a run turns on or off; one it leaves is its notice alone", func() {
		writeTree(p.UserProfiles, map[string]string{"api.yml": "version: 1\n", "go.yml": "version: 1\n"})
		Expect(lola("sync", "--user").code).To(Equal(0))
		Expect(lola("profile", "go", "--on", "--user").code).To(Equal(0))
		api, goFile := filepath.Join(p.UserProfiles, "api.yml"), filepath.Join(p.UserProfiles, "go.yml")

		r := lola("profile", "api", "go", "--on", "--user", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("Profile user:api (" + api + ") → on, 2nd of 2\n" +
			"→ user:go is already on (1st of 2)\n" +
			"→ to apply: lola profile --user user:api user:go --on\n" +
			"→ no files to change\n"))
		r = lola("profile", "go", "--on", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("→ user:go is already on (1st of 1)\n✓ no files to change\n"))

		r = lola("profile", "api", "go", "--off", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("Profile user:go (" + goFile + ") → off\n" +
			"→ user:api is not on\n" +
			"✓ no files to change\n"))
		r = lola("profile", "api", "--off", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("→ user:api is not on\n✓ no files to change\n"))
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
		Expect(string(got)).To(HavePrefix("Profile user:go (" + filepath.Join(p.UserProfiles, "go.yml") + ") → on, 1st of 1\n"))
		Expect(out.String()).To(BeEmpty(), "the summary went to the pager")
	})

	It("lola sync completes the modules of every layer, installed or not", func() {
		Expect(lola("__complete", "sync", "--user", "").out).To(HavePrefix("a\nc\n:"))
		Expect(lola("__complete", "sync", "--user", "c", "").out).To(HavePrefix("a\n:"))
	})

	It("lola profile --plain keeps a shown profile's notices", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: " + c + "}\n"})
		led := ledger.New()
		led.Profiles = []ledger.Active{{Name: "user:gone", By: ledger.ByOn}}
		Expect(led.Save(p.UserLedger)).To(Succeed())
		r := lola("profile", "go", "--user", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("user\tc\tprofile:user:go\tprofile:default\n" +
			"notice\twarn\tprofile user:gone is on but no longer exists → lola profile --user user:gone --off\n"))
	})

	It("list warns about an active profile that no longer exists", func() {
		led := ledger.New()
		led.Profiles = []ledger.Active{{Name: "user:gone", By: ledger.ByOn}}
		Expect(led.Save(p.UserLedger)).To(Succeed())
		r := lola("list", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("⚠ profile user:gone is on but no longer exists → lola profile --user user:gone --off\n"))
		var v struct{ Notices []map[string]string }
		Expect(json.Unmarshal([]byte(lola("list", "--user", "--json").out), &v)).To(Succeed())
		Expect(v.Notices).To(ContainElement(HaveKeyWithValue("text", "profile user:gone is on but no longer exists → lola profile --user user:gone --off")))
	})
})
