package cli_test

import (
	"context"
	"encoding/json"
	"os"
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

var _ = Describe("lola profile --edit", func() {
	var (
		p        paths.Paths
		tmp, kit string
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		kit = filepath.Join(tmp, "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "skills/a/SKILL.md": "---\ndescription: A\n---\nA\n",
			"skills/b/SKILL.md": "---\ndescription: B\n---\nB\n"})
		Expect(lola("install", kit, "--user", "-t", "claude-code", "-y").code).To(Equal(0))
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
	})

	It("edits a named profile's module and its picks, as a write line says, and applies it where the profile is on", func() {
		Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
		goYml := filepath.Join(p.UserProfiles, "go.yml")
		r := lola("profile", "go", "--edit", "kit", "--only", "skill/a", "--override", "--user", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(HavePrefix("user scope: profile go over default\n"))
		Expect(r.out + r.err).To(ContainSubstring("✓ added kit to profile go  (" + goYml + ")\n"))
		Expect(os.ReadFile(goYml)).To(ContainSubstring("  - name: kit\n    source: "+kit+"\n    skip: [skill/b]\n"), "--only tracks, as lola install's: the rest is skipped")
		Expect(os.ReadFile(goYml)).NotTo(ContainSubstring("only:"))

		r = lola("profile", "go", "--edit", "kit", "--only", "skill/a", "--exact", "--user", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out + r.err).To(ContainSubstring("✓ changed kit in profile go  (" + goYml + ")  only: 1 skill\n"))
		Expect(os.ReadFile(goYml)).To(ContainSubstring("    only: [skill/a]\n"), "--exact writes the entry's only: list")

		r = lola("profile", "--pick", "kit", "--unskip", "skill/b", "--user", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var v struct {
			Edits []map[string]any `json:"edits"`
		}
		Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed())
		Expect(v.Edits).To(ConsistOf(And(HaveKeyWithValue("module", "kit"), HaveKeyWithValue("action", "changed"), HaveKeyWithValue("profile", "user:go"))),
			"no name: the profile edits go to; --pick is --edit")
		Expect(os.ReadFile(goYml)).To(ContainSubstring("    only: [skill/a, skill/b]\n"))
	})

	It("names, after a sync, the edit of the profile that provides a module whose new component its only: list leaves out, which runs as printed", func() {
		Expect(lola("install", "kit", "--user", "--only", "skill/a", "--exact", "-y").code).To(Equal(0))
		writeTree(kit, map[string]string{"skills/c/SKILL.md": "---\ndescription: C\n---\nC\n"})
		r := lola("sync", "--user")
		Expect(r.code).To(Equal(0), r.err)
		hint := "lola profile --user default --edit kit --unskip skill/c"
		Expect(r.out).To(ContainSubstring("→ kit adds skill/c, not installed (only: in default (base)) → " + hint + "\n"))
		r = runHint(hint)
		Expect(r.code).To(Equal(0), hint+": "+r.err)
		Expect(filepath.Join(p.Home, ".claude", "skills", "c", "SKILL.md")).To(BeAnExistingFile())
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "default.yml"))).To(ContainSubstring("only: [skill/a, skill/c]"))
	})

	It("names, after a sync, a profile named as its module qualified in the edit, which runs as printed", func() {
		writeTree(p.UserProfiles, map[string]string{"kit.yml": "version: 1\nmodules:\n  - {name: kit, source: " + kit + ", only: [skill/a]}\n"})
		Expect(lola("profile", "user:kit", "--switch", "--user", "-y").code).To(Equal(0))
		writeTree(kit, map[string]string{"skills/c/SKILL.md": "---\ndescription: C\n---\nC\n"})
		r := lola("sync", "--user")
		Expect(r.code).To(Equal(0), r.err)
		hint := "lola profile --user user:kit --edit kit --unskip skill/c"
		Expect(r.out).To(ContainSubstring("→ kit adds skill/c, not installed (only: in profile kit) → " + hint + "\n"))
		r = runHint(hint)
		Expect(r.code).To(Equal(0), hint+": "+r.err)
		Expect(filepath.Join(p.Home, ".claude", "skills", "c", "SKILL.md")).To(BeAnExistingFile())
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "kit.yml"))).To(ContainSubstring("only: [skill/a, skill/c]"))
	})

	It("names, after a sync, project.yml's change through base while a profile is on there, which runs as printed", func() {
		proj := filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		Expect(lola("install", kit, "-C", proj, "-t", "claude-code", "--only", "skill/a", "--exact", "-y").code).To(Equal(0))
		Expect(lola("profile", "go", "--switch", "-C", proj, "-y").code).To(Equal(0))
		writeTree(kit, map[string]string{"skills/c/SKILL.md": "---\ndescription: C\n---\nC\n"})
		r := lola("sync", "-C", proj, "--trust=entries")
		Expect(r.code).To(Equal(0), r.err)
		hint := "lola profile -C " + proj + " base --edit kit --unskip skill/c"
		Expect(r.out).To(ContainSubstring(" → " + hint + "\n"))
		r = runHint(hint)
		Expect(r.code).To(Equal(0), hint+": "+r.err)
		Expect(filepath.Join(proj, ".claude", "skills", "c", "SKILL.md")).To(BeAnExistingFile())
	})

	It("warns that selections.yaml's project section is ignored for a module a profile provides there, hinting the profile's edit, which runs as printed", func() {
		proj := filepath.Join(tmp, "proj")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n"})
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\nmodules:\n  - {name: kit, source: " + kit + "}\n"})
		writeTree(filepath.Dir(p.Selections), map[string]string{"selections.yaml": "version: 1\nprojects:\n  " + proj + ":\n    kit:\n      skip: [skill/a]\n"})
		Expect(lola("sync", "-C", proj, "--trust=entries", "-y").code).To(Equal(0))
		GinkgoT().Chdir(proj)
		r := lola("profile", "user:go", "--switch", "-y")
		Expect(r.code).To(Equal(0), r.err)
		hint := "lola profile --project go --edit kit"
		Expect(r.out + r.err).To(ContainSubstring("⚠ selections.yaml projects[" + proj + "] kit is ignored: profile go provides it; delete it from " + p.Selections + ", and pick kit's components in profile go → " + hint + "\n"))
		Expect(filepath.Join(proj, ".claude", "skills", "a", "SKILL.md")).To(BeAnExistingFile(), "the section's skip is not go's")
		words := shellWords(hint)
		Expect(words[0]).To(Equal("lola"), hint)
		sp := &scripted{components: []func(cli.ComponentPage) ([]string, error){func(cli.ComponentPage) ([]string, error) { return []string{"skill/b"}, nil }},
			review: []string{cli.ReviewApply}, confirm: []bool{true}}
		r = lolaP(sp, words[1:]...)
		Expect(r.code).To(Equal(0), hint+": "+r.err)
		Expect(sp.calls).To(ContainElement("components:profile go › kit"), "the hint opens kit's component page")
		r = lola("sync", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out+r.err).To(ContainSubstring(" kit is ignored: "), "editing go's picks leaves the section's entry")

		writeTree(filepath.Dir(p.Selections), map[string]string{"selections.yaml": "version: 1\n"})
		r = lola("sync", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out+r.err).NotTo(ContainSubstring("is ignored"), "deleting the entry the warning names stops it")
	})

	It("lola info lists, per scope, each component as the providing layer's picks leave it, what is new, and the re-pick, which runs as printed", func() {
		Expect(lola("install", "kit", "--user", "--only", "skill/a", "--exact", "-y").code).To(Equal(0))
		writeTree(kit, map[string]string{"skills/c/SKILL.md": "---\ndescription: C\n---\nC\n"})
		Expect(lola("cache", "add", kit).code).To(Equal(0), "the cache holds kit with skill/c; the version installed has a and b")
		hint := "lola profile --user default --edit kit"
		r := lola("info", "kit", "--user", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("  components of kit here, provided by default (base):\n" +
			"    skill/a  installed\n" +
			"    skill/b  not in the only: list of default (base)\n" +
			"    skill/c  not in the only: list of default (base) (new)\n" +
			"  → change it: " + hint + "\n"))
		r = lola("info", "kit", "--user", "-t", "claude-code", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("from\tuser\tprofile:default\n" +
			"component\tuser\tskill/a\tinstalled\t\tfalse\n" +
			"component\tuser\tskill/b\tnot_in_only\tdefault (base)\tfalse\n" +
			"component\tuser\tskill/c\tnot_in_only\tdefault (base)\ttrue\n" +
			"change\tuser\t" + hint + "\n"))
		r = lola("info", "kit", "--user", "-t", "claude-code", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var v struct {
			Install []map[string]any `json:"install"`
		}
		Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed())
		Expect(v.Install).To(HaveExactElements(And(HaveKeyWithValue("from", "profile:default"), HaveKeyWithValue("change", hint),
			HaveKeyWithValue("components", ContainElement(map[string]any{"kind": "skill", "name": "c", "state": "not_in_only", "origin": "default (base)",
				"new": true, "mentions": []any{}})))))
		words := shellWords(hint)
		Expect(words[0]).To(Equal("lola"), hint)
		sp := &scripted{components: []func(cli.ComponentPage) ([]string, error){func(cli.ComponentPage) ([]string, error) { return []string{"skill/a", "skill/c"}, nil }},
			review: []string{cli.ReviewApply}, confirm: []bool{true}}
		r = lolaP(sp, words[1:]...)
		Expect(r.code).To(Equal(0), hint+": "+r.err)
		Expect(sp.calls).To(ContainElement("components:default (base) › kit"), "the hint opens kit's component page")
		var offered []string
		for _, g := range sp.pages[0].Groups {
			for _, o := range g.Options {
				offered = append(offered, o.ID)
			}
		}
		Expect(offered).To(ContainElement("skill/c"), "the page offers the component info called new")
		Expect(r.out + r.err).NotTo(ContainSubstring("has no component"))
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "default.yml"))).To(ContainSubstring("only: [skill/a, skill/c]"))
		Expect(filepath.Join(p.Home, ".claude", "skills", "c", "SKILL.md")).To(BeAnExistingFile())
		Expect(filepath.Join(p.Home, ".claude", "skills", "b")).NotTo(BeADirectory())
	})

	It("lola info prints no components, change it: or change row at a scope no layer provides the module", func() {
		proj := filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		r := lola("info", "kit", "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("install at project (" + proj + ") for claude-code:\n"))
		Expect(r.out).NotTo(ContainSubstring("components ("))
		Expect(r.out).NotTo(ContainSubstring("change it:"))
		r = lola("info", "kit", "-C", proj, "-t", "claude-code", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).NotTo(MatchRegexp(`(?m)^(from|component|change)\t`))
	})

	It("lola info names the provider and its components but no change it: or change row where it cannot list the profiles, saying why", func() {
		proj := filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		Expect(lola("install", kit, "-C", proj, "-t", "claude-code", "-y").code).To(Equal(0))
		writeTree(proj, map[string]string{".lola/profiles": "not a directory\n"})
		r := lola("info", "kit", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("  ⚠ kit: cannot list the profiles, so lola info names no run that re-picks its components: "))
		Expect(r.out).To(ContainSubstring("  components of kit here, provided by project.yml (base):\n" +
			"    skill/a  installed\n" +
			"    skill/b  installed\n"))
		Expect(r.out).NotTo(ContainSubstring("change it:"))
		r = lola("info", "kit", "-C", proj, "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("from\tproject\tproject.yml\n"))
		Expect(r.out).NotTo(MatchRegexp(`(?m)^change\t`))
	})

	It("lola info names project.yml as the provider and its re-pick, lola install with no profile on and base's edit with one, each running as printed", func() {
		proj := filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		Expect(lola("install", kit, "-C", proj, "-t", "claude-code", "--only", "skill/a", "--exact", "-y").code).To(Equal(0))
		hint := "lola install -C " + proj + " kit"
		r := lola("info", "kit", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("  components of kit here, provided by project.yml (base):\n" +
			"    skill/a  installed\n" +
			"    skill/b  not in the only: list of selections.yaml projects[" + proj + "]\n" +
			"  → change it: " + hint + "\n"))
		Expect(lola("info", "kit", "-C", proj, "--plain").out).To(ContainSubstring("from\tproject\tproject.yml\n"))
		words := shellWords(hint)
		Expect(words[0]).To(Equal("lola"), hint)
		sp := &scripted{components: []func(cli.ComponentPage) ([]string, error){func(cli.ComponentPage) ([]string, error) { return []string{"skill/a", "skill/b"}, nil }},
			review: []string{cli.ReviewApply}, confirm: []bool{true}}
		r = lolaP(sp, words[1:]...)
		Expect(r.code).To(Equal(0), hint+": "+r.err)
		Expect(sp.calls).To(ContainElement(HavePrefix("components:")), "the hint opens kit's component page")
		Expect(filepath.Join(proj, ".claude", "skills", "b", "SKILL.md")).To(BeAnExistingFile())

		Expect(lola("profile", "go", "--switch", "-C", proj, "-y").code).To(Equal(0))
		hint = "lola profile -C " + proj + " base --edit kit"
		r = lola("info", "kit", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("  components of kit here, provided by project.yml (base):\n"))
		Expect(r.out).To(ContainSubstring("  → change it: " + hint + "\n"))
		words = shellWords(hint)
		Expect(words[0]).To(Equal("lola"), hint)
		sp = &scripted{components: []func(cli.ComponentPage) ([]string, error){func(cli.ComponentPage) ([]string, error) { return []string{"skill/a"}, nil }},
			review: []string{cli.ReviewApply}, confirm: []bool{true}}
		r = lolaP(sp, words[1:]...)
		Expect(r.code).To(Equal(0), hint+": "+r.err)
		Expect(sp.calls).To(ContainElement(HavePrefix("components:")), "the hint opens kit's component page")
		Expect(filepath.Join(proj, ".claude", "skills", "b", "SKILL.md")).NotTo(BeAnExistingFile())
	})

	It("lola info names a bundle member as the provider, its re-pick naming the member, never the bundle, which runs as printed", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\nmodules:\n  - {name: kit, source: " + kit + ", only: [skill/a]}\n",
			"docs.yml": "version: 1\n", "work.yml": "version: 1\nbundle: [go, docs]\n"})
		Expect(lola("profile", "work", "--switch", "--user", "-y").code).To(Equal(0))
		hint := "lola profile --user go --edit kit"
		r := lola("info", "kit", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("  components of kit here, provided by profile go:\n" +
			"    skill/a  installed\n" +
			"    skill/b  not in the only: list of profile go\n" +
			"  → change it: " + hint + "\n"))
		words := shellWords(hint)
		Expect(words[0]).To(Equal("lola"), hint)
		sp := &scripted{components: []func(cli.ComponentPage) ([]string, error){func(cli.ComponentPage) ([]string, error) { return []string{"skill/a", "skill/b"}, nil }},
			review: []string{cli.ReviewApply}, confirm: []bool{true}}
		r = lolaP(sp, words[1:]...)
		Expect(r.code).To(Equal(0), hint+": "+r.err)
		Expect(sp.calls).To(ContainElement("components:profile go › kit"), "the hint opens kit's component page in the member")
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "work.yml"))).To(Equal([]byte("version: 1\nbundle: [go, docs]\n")))
	})

	It("lola info's install refusal under a bundle hints a run that names the module and keeps the scope, and it runs as printed", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n", "docs.yml": "version: 1\n", "work.yml": "version: 1\nbundle: [go, docs]\n"})
		Expect(lola("profile", "work", "--switch", "--user", "-y").code).To(Equal(0))
		r := lola("info", kit, "--user", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		hint := "lola install kit --target claude-code --user --profile go"
		Expect(r.out).To(ContainSubstring(hint+"\n"), "the refusal's hint keeps the module and --user")
		words := shellWords(hint)
		Expect(words[0]).To(Equal("lola"), hint)
		r = lola(words[1:]...)
		Expect(r.err).NotTo(ContainSubstring("needs a module or source"), hint)
		Expect(r.code).To(Equal(0), hint+": "+r.err)
	})

	It("lola info names a remote profile as the provider and the copy that re-picks, which runs as printed and switches to the copy", func() {
		work := filepath.Join(tmp, "work")
		writeTree(work, map[string]string{"lola.yml": "name: notes\n", "commands/notes.md": "N\n"})
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "one")
		gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "notes.git"))
		url := testenv.ServeGitTLS(filepath.Join(tmp, "srv"), nil) + "/notes.git"
		other := p
		other.Store, other.StoreLock, other.Staging = filepath.Join(tmp, "pin"), filepath.Join(tmp, "pin.lock"), filepath.Join(tmp, "pin-staging")
		v, err := engine.New(other).Fetch(context.Background(), source.Spec{Kind: source.KindGit, Location: url})
		Expect(err).NotTo(HaveOccurred())
		body := "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + ", commit: " + gitIn(work, "rev-parse", "HEAD") +
			", digest: '" + v.Meta.Digest + "'}\n"
		o, err := json.Marshal(profile.Origin{Version: profile.OriginVersion, Source: "https://example.com/profiles.git",
			Commit: strings.Repeat("c", 40), Digest: profile.Digest([]byte(body))})
		Expect(err).NotTo(HaveOccurred())
		writeTree(filepath.Join(p.RemoteProfiles, "team"), map[string]string{profile.RemoteFile: body, profile.OriginFile: string(o)})
		r := lola("profile", "team", "--switch", "--user", "-y")
		Expect(r.code).To(Equal(0), r.err)
		hint := "lola profile --user team-mine --switch --new --from remote:team"
		r = lola("info", "notes", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("  components of notes here, provided by remote:team:\n" +
			"    command/notes  installed\n" +
			"  → change it: " + hint + "\n"))
		r = runHint(hint)
		Expect(r.code).To(Equal(0), hint+": "+r.err)
		led, err := ledger.Load(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Profile).To(HaveField("Name", "user:team-mine"))
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "team-mine.yml"))).To(ContainSubstring("name: notes"))
	})

	It("labels on a profile's component page what its only: list leaves out and what the version adds, and names the profile that provides the module", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\nmodules:\n  - {name: kit, source: " + kit + ", only: [skill/a]}\n"})
		Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
		writeTree(kit, map[string]string{"skills/c/SKILL.md": "---\ndescription: C\n---\nC\n"})
		Expect(lola("cache", "add", kit).code).To(Equal(0))
		sp := &scripted{components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewApply}}
		r := lolaP(sp, "profile", "go", "--edit", "kit", "--user", "-n")
		Expect(r.code).To(Equal(0), r.err)
		labels := []string{}
		for _, o := range sp.pages[0].Groups[0].Options {
			labels = append(labels, o.Label)
		}
		Expect(labels).To(Equal([]string{"a — A", "b — B (not in only:)", "c — C (not in only:) (new)"}))
		Expect(sp.pages[0].Footer).To(Equal("kit comes from profile go here\nonly: in profile go — components a new version adds are not installed"))

		led, err := ledger.Load(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())
		origin := filepath.Join(p.Store, "kit", "versions", strings.TrimPrefix(led.Modules["kit"].Digest, "sha256:")[:12], ".lola-origin")
		Expect(os.WriteFile(origin, []byte("{\n"), 0o644)).To(Succeed())
		sp = &scripted{components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewApply}}
		r = lolaP(sp, "profile", "go", "--edit", "kit", "--user", "-n")
		Expect(r.code).To(Equal(0), r.err)
		labels = []string{}
		for _, o := range sp.pages[0].Groups[0].Options {
			labels = append(labels, o.Label)
		}
		Expect(labels).To(Equal([]string{"a — A", "b — B (not in only:)", "c — C (not in only:)"}),
			"an installed version the cache holds but cannot read leaves the page open, calling nothing new")
	})

	It("refuses an inherited edit off a terminal with lola profile base --edit and lola profile <target> --edit --override, which run as printed", func() {
		Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
		r := lola("profile", "--edit", "kit", "--only", "skill/a", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HaveSuffix("error: kit comes from default (base); lola profile --edit needs to know whether to change it there or override it in profile go, " +
			"and there is no terminal to ask on\n  hint: lola profile base kit --edit --only skill/a --user\n  hint: lola profile go kit --edit --only skill/a --user --override\n"))
		for _, hint := range []string{"lola profile go kit --edit --only skill/a --user --override", "lola profile base kit --edit --only skill/a --user"} {
			r = lola(append(strings.Fields(hint)[1:], "-y")...)
			Expect(r.code).To(Equal(0), hint+": "+r.err)
		}
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "default.yml"))).To(ContainSubstring("  - name: kit\n    source: " + kit + "\n    skip: [skill/b]\n"))
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "default.yml"))).NotTo(ContainSubstring("only:"))
	})

	It("refuses a name no profile has, hinting --new, which makes it, and refuses --edit without a module off a terminal with hints that run", func() {
		r := lola("profile", "try", "--edit", "kit", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal(userContext + "error: no profile try for user scope; --new makes it\n  hint: make it: lola profile try kit --edit --user --new\n"))
		r = lola("profile", "try", "kit", "--edit", "--user", "--new")
		Expect(r.code).To(Equal(0), "the hint runs as printed: "+r.err)
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "try.yml"))).To(ContainSubstring("name: kit"))

		r = lola("profile", "go", "--edit", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal(userContext + "error: lola profile --edit edits profile go by its modules: name one, as in lola profile go --edit <module> --only <kind/name>\n" +
			"  hint: see what it lists: lola profile --user go\n  hint: see your cache: lola cache list\n"))
		for _, run := range [][]string{{"profile", "--user", "go"}, {"cache", "list"}} {
			Expect(lola(run...).code).To(Equal(0), strings.Join(run, " "))
		}

		r = lola("profile", "docs", "--edit", "--new", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✓ new profile docs (" + filepath.Join(p.UserProfiles, "docs.yml") + ", empty)\n"))
		Expect(r.out).To(ContainSubstring("docs is not on at user scope → lola profile --user docs --switch\n"))
		r = lola("profile", "--edit", "--new", "--user", "-n")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: --new makes the profile --edit names, and none was named: name it, as in lola profile <name> --edit --new\n"+
			"  hint: see the profiles: lola profile --user\n"), "the listing keeps no -n, which it refuses")
		Expect(lola("profile", "--user").code).To(Equal(0), "the hint runs as printed")
	})

	It("refuses --exact with no pick by saying what it needs, with no hint: the run without it changes nothing the base gives", func() {
		for _, run := range [][]string{{"profile", "go", "kit", "--edit", "--exact", "--user"}, {"install", "kit", "--exact", "--user"}} {
			r := lola(run...)
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: --exact applies to --only, and none was given: name the components to keep, as in lola " +
				strings.Join(run[:len(run)-2], " ") + " --user --only <kind>/<name> --exact\n"))
		}
		r := lola("install", "--exact", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: --exact applies to --only, and none was given: name the components to keep, as in lola "+
			"install <module> --user --only <kind>/<name> --exact\n"), "an install names its module")
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "go.yml"))).To(Equal([]byte("version: 1\n")))
	})

	It("acts on the one scope found from a project with no scope flag, and applies a named profile of yours in each scope it is on", func() {
		proj := filepath.Join(tmp, "proj")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n", ".lola/profiles/team.yml": "version: 1\n"})
		Expect(lola("sync", "-C", proj, "--trust=entries", "-y").code).To(Equal(0))
		GinkgoT().Chdir(proj)
		defaultYml := filepath.Join(p.UserProfiles, "default.yml")
		before, err := os.ReadFile(defaultYml)
		Expect(err).NotTo(HaveOccurred())
		r := lola("profile", "--edit", "kit", "--only", "skill/a", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(HavePrefix(projectContext(proj)))
		Expect(r.err).NotTo(ContainSubstring("user scope"))
		Expect(os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))).To(ContainSubstring("name: kit"))
		Expect(os.ReadFile(defaultYml)).To(Equal(before), "one scope: the project's write target alone")

		r = lola("profile", "project:team", "--edit", "kit", "--only", "skill/a")
		Expect(r.code).To(Equal(0), "a project profile, from its project: "+r.err)
		Expect(os.ReadFile(filepath.Join(proj, ".lola", "profiles", "team.yml"))).To(ContainSubstring("  - name: kit\n    source: " + kit + "\n    skip: [skill/b]\n"))
		Expect(os.ReadFile(filepath.Join(proj, ".lola", "profiles", "team.yml"))).NotTo(ContainSubstring("only:"))

		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\nmodules:\n  - {name: kit, source: " + kit + "}\n"})
		Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
		Expect(lola("profile", "user:go", "--switch", "-y").code).To(Equal(0))
		skillB := map[string]string{"user": filepath.Join(p.Home, ".claude", "skills", "b", "SKILL.md"), "project": filepath.Join(proj, ".claude", "skills", "b", "SKILL.md")}
		r = lola("profile", "go", "--edit", "kit", "--only", "skill/a")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(HavePrefix("project " + proj + ": profile go over project.yml\nuser scope: profile go over default\n"))
		Expect(strings.Count(r.out, "changed kit in profile go")).To(Equal(1), "written once: "+r.out)
		Expect(r.out).To(And(ContainSubstring("✓ installed kit at project scope"), ContainSubstring("✓ installed kit at user scope")))
		Expect(skillB["user"]).NotTo(BeAnExistingFile())
		Expect(skillB["project"]).NotTo(BeAnExistingFile())

		r = lola("profile", "go", "--edit", "kit", "--unskip", "skill/b", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).NotTo(ContainSubstring("user scope"), "a scope flag names the one scope")
		Expect(skillB["project"]).To(BeAnExistingFile())
		Expect(skillB["user"]).NotTo(BeAnExistingFile())
	})

	It("writes a profile of yours on in two scopes once and syncs the other from it, whatever assistants each scope defaults to", func() {
		proj := filepath.Join(tmp, "proj")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [opencode]\n"})
		Expect(lola("sync", "-C", proj, "--trust=entries", "-y").code).To(Equal(0))
		GinkgoT().Chdir(proj)
		Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
		Expect(lola("profile", "user:go", "--switch", "-y").code).To(Equal(0))
		goYml := filepath.Join(p.UserProfiles, "go.yml")

		r := lola("profile", "go", "--edit", "kit", "--only", "skill/a", "--override", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(And(ContainSubstring("✓ create opencode    .opencode/skills/a/SKILL.md\n"), ContainSubstring("user:\n  → delete claude-code .claude/skills/b/SKILL.md\n")),
			"-n plans the other scope from the file as it would be written")
		Expect(os.ReadFile(goYml)).To(Equal([]byte("version: 1\n")))

		r = lola("profile", "go", "--edit", "kit", "--only", "skill/a", "--override")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(HavePrefix("project " + proj + ": profile go over project.yml\nuser scope: profile go over default\n"))
		Expect(r.out + r.err).NotTo(ContainSubstring("written differently"))
		Expect(strings.Count(r.out, "added kit to profile go")).To(Equal(1), r.out)
		Expect(strings.Count(r.out, "kit: skipped in profile go: skill/b\n")).To(Equal(1), "said once for both scopes: "+r.out)
		Expect(r.out).To(And(ContainSubstring("✓ installed kit at project scope (opencode)"), ContainSubstring("✓ installed kit at user scope (claude-code)")))
		Expect(os.ReadFile(goYml)).To(ContainSubstring("  - name: kit\n    source: " + kit + "\n    skip: [skill/b]\n"))
		Expect(os.ReadFile(goYml)).NotTo(ContainSubstring("only:"))
		Expect(filepath.Join(proj, ".opencode", "skills", "a", "SKILL.md")).To(BeAnExistingFile())
		Expect(filepath.Join(p.Home, ".claude", "skills", "b", "SKILL.md")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(p.Home, ".claude", "skills", "a", "SKILL.md")).To(BeAnExistingFile())
	})

	It("asks before an edit writes anything about what syncing the other scope removes: off a terminal refused, -y removing", func() {
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + ", targets: [claude-code, opencode]}\n"})
		Expect(lola("sync", "--user", "-y").code).To(Equal(0))
		proj := filepath.Join(tmp, "proj")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n"})
		Expect(lola("sync", "-C", proj, "--trust=all", "-y").code).To(Equal(0))
		GinkgoT().Chdir(proj)
		Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
		Expect(lola("profile", "user:go", "--switch", "-y").code).To(Equal(0))
		goYml := filepath.Join(p.UserProfiles, "go.yml")

		r := lola("profile", "go", "--edit", "kit", "--only", "skill/a")
		Expect(r.code).To(Equal(1))
		Expect(lola("profile", "go", "kit", "--edit", "--only", "skill/a", "-n").code).To(Equal(0), "the -n hint runs as printed")
		Expect(r.err).To(HaveSuffix("error: lola profile --edit needs your confirmation to remove kit (opencode) at user scope, and there is no terminal to ask on; nothing was written\n" +
			"  hint: see what goes: lola profile go kit --edit --only skill/a -n\n  hint: confirm it: lola profile go kit --edit --only skill/a -y\n"))
		Expect(os.ReadFile(goYml)).To(Equal([]byte("version: 1\n")), "nothing was written")
		Expect(filepath.Join(proj, ".claude", "skills", "a", "SKILL.md")).NotTo(BeAnExistingFile())

		r = lola("profile", "go", "kit", "--edit", "--only", "skill/a", "-y")
		Expect(r.code).To(Equal(0), "the hint runs as printed: "+r.err)
		Expect(os.ReadFile(goYml)).To(ContainSubstring("  - name: kit\n    source: " + kit + "\n    skip: [skill/b]\n"))
		Expect(os.ReadFile(goYml)).NotTo(ContainSubstring("only:"))
		Expect(filepath.Join(p.Home, ".claude", "skills", "b", "SKILL.md")).NotTo(BeAnExistingFile())
	})

	It("runs an edit beside a catalog lola cannot read, qualifying each profile its hints name", func() {
		Expect(os.WriteFile(p.Markets, []byte("version: [\n"), 0o600)).To(Succeed())
		Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
		r := lola("profile", "--edit", "kit", "--only", "skill/a", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HaveSuffix("  hint: lola profile base kit --edit --only skill/a --user\n  hint: lola profile user:go kit --edit --only skill/a --user --override\n"),
			"go might be a catalog's entry too")
		r = lola("profile", "user:go", "kit", "--edit", "--only", "skill/a", "--user", "--override")
		Expect(r.code).To(Equal(0), "the hint runs as printed: "+r.err)
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "go.yml"))).To(ContainSubstring("  - name: kit\n    source: " + kit + "\n    skip: [skill/b]\n"))
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "go.yml"))).NotTo(ContainSubstring("only:"))
	})

	It("refuses off a terminal a name that is a profile and a module, hinting each reading, which run as printed", func() {
		writeTree(p.UserProfiles, map[string]string{"kit.yml": "version: 1\n"})
		r := lola("profile", "kit", "--edit", "--only", "skill/a", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal(userContext + "error: kit names profile user:kit (" + filepath.Join(p.UserProfiles, "kit.yml") + ") and a module; " +
			"lola profile --edit needs to know which to edit, and there is no terminal to ask on\n" +
			"  hint: see profile user:kit, then name a module after it: lola profile --user user:kit\n" +
			"  hint: edit module kit in default (base): lola profile base kit --edit --only skill/a --user\n"))
		for _, hint := range []string{"lola profile --user user:kit", "lola profile base kit --edit --only skill/a --user"} {
			r = lola(strings.Fields(hint)[1:]...)
			Expect(r.code).To(Equal(0), hint+": "+r.err)
		}
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "default.yml"))).To(ContainSubstring("  - name: kit\n    source: " + kit + "\n    skip: [skill/b]\n"))
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "default.yml"))).NotTo(ContainSubstring("only:"))
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "kit.yml"))).To(Equal([]byte("version: 1\n")))
	})

	It("refuses --new of a name a profile has, with hints that run", func() {
		r := lola("profile", "go", "--edit", "--new", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal(userContext + "error: go names user:go (" + filepath.Join(p.UserProfiles, "go.yml") + "); --new makes a profile that does not exist yet\n" +
			"  hint: see it: lola profile --user user:go\n  hint: or make another: lola profile go-2 --edit --user --new\n"))
		for _, run := range [][]string{{"profile", "--user", "user:go"}, {"profile", "go-2", "--edit", "--user", "--new"}} {
			Expect(lola(run...).code).To(Equal(0), strings.Join(run, " "))
		}
	})

	It("narrows picks off a terminal as lola install --skip does: removing files unasked, refusing a hand-edited one with a --force hint that runs", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\nmodules:\n  - {name: kit, source: " + kit + "}\n"})
		Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
		skill := func(n string) string { return filepath.Join(p.Home, ".claude", "skills", n, "SKILL.md") }
		Expect(os.WriteFile(skill("b"), []byte("mine\n"), 0o600)).To(Succeed())
		r := lola("profile", "go", "--edit", "kit", "--skip", "skill/b", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HaveSuffix("  hint: lola profile go kit --edit --skip skill/b --user --force\n"))
		Expect(skill("b")).To(BeAnExistingFile())
		r = lola("profile", "go", "kit", "--edit", "--skip", "skill/b", "--user", "--force")
		Expect(r.code).To(Equal(0), "the hint runs as printed: "+r.err)
		Expect(skill("b")).NotTo(BeAnExistingFile())
		r = lola("profile", "go", "--edit", "kit", "--skip", "skill/a", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(skill("a")).NotTo(BeAnExistingFile())
	})

	It("refuses an edit of a bundle's modules, hinting each member, and takes its flags only with --edit", func() {
		writeTree(p.UserProfiles, map[string]string{"docs.yml": "version: 1\n", "work.yml": "version: 1\nbundle: [go, docs]\n"})
		r := lola("profile", "work", "--edit", "kit", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: work is a bundle; it lists profiles, not modules\n  hint: lola profile go kit --edit --user\n  hint: lola profile docs kit --edit --user\n"))
		Expect(lola("profile", "go", "kit", "--edit", "--user").code).To(Equal(0), "the hint runs as printed")
		Expect(lola("profile", "go", "--on", "--only", "skill/a").err).To(ContainSubstring("--only applies to --edit only"))
	})

	It("sets a bundle's members with --bundle, saying what it made and changed, and makes one, or a copy, with --new", func() {
		work := filepath.Join(p.UserProfiles, "work.yml")
		writeTree(p.UserProfiles, map[string]string{"work.yml": "version: 1\nbundle: [go]\n"})
		r := lola("profile", "work", "--edit", "--bundle", "go,notes", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("✓ new profile notes (" + filepath.Join(p.UserProfiles, "notes.yml") + ", empty)\n" +
			"✓ changed bundle work (go → go, notes)  (" + work + ")\n"))
		Expect(r.out).To(ContainSubstring("saved to bundle work; it is not on at user scope → lola profile --user work --switch\n"))
		r = lola("profile", "work", "--modify", "--bundle", "notes", "--user", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var v map[string]any
		Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed())
		Expect(v).To(And(HaveKeyWithValue("profile", "user:work"), HaveKeyWithValue("members", []any{"notes"}), HaveKeyWithValue("was", []any{"go", "notes"}),
			HaveKeyWithValue("created", BeEmpty()), HaveKeyWithValue("sync", BeNil())))
		Expect(lola("profile", "work", "--edit", "--bundle", "go,notes", "--user", "-n", "--plain").out).To(HavePrefix("would-bundle\tuser:work\tgo,notes\t" + work + "\n"))
		Expect(os.ReadFile(work)).To(Equal([]byte("version: 1\nbundle: [notes]\n")), "-n writes nothing")

		r = lola("profile", "work", "--edit", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HaveSuffix("error: bundle work lists profiles, not modules (notes): --bundle sets them, as in lola profile work --edit --bundle notes,<profile>\n" +
			"  hint: see it: lola profile --user work\n  hint: see the profiles: lola profile --user\n"))

		r = lola("profile", "both", "--edit", "--new", "--bundle", "go,notes", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("✓ new bundle both (go, notes)\n"))
		r = lola("profile", "mine", "--edit", "--new", "--from", "go", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("✓ new profile mine, a copy of go (0 modules)\n"))
	})

	It("names the profile --new --from copies bare, qualified only beside a namesake (spec 2026-10-29 §3.8, S3)", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		r := lola("profile", "try", "--edit", "--new", "--from", "go", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("✓ new profile try, a copy of go (0 modules)\n"))

		proj := filepath.Join(tmp, "named")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\n", ".lola/profiles/go.yml": "version: 1\n"})
		r = lola("profile", "again", "--edit", "--new", "--from", "user:go", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("✓ new profile again, a copy of user:go (0 modules)\n"), "the project's go is a namesake")
		r = lola("profile", "again2", "--edit", "--new", "--from", "user:go", "-C", proj, "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(`"from": "user:go"`), "JSON keeps the qualified name")

		r = lola("profile", "fromdef", "--edit", "--new", "--from", "default", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("✓ new profile fromdef, a copy of default (1 module)\n"), "the default profile is not a layer 'default (base)'")
	})

	It("refuses --from without --new, --from beside --bundle, and modules beside either, hinting the runs that work", func() {
		r := lola("profile", "mine", "--edit", "--from", "go", "--user")
		Expect(r.err).To(HaveSuffix("error: --from makes a new profile and goes with --new\n  hint: make it: lola profile mine --edit --from go --user --new\n"))
		r = lola("profile", "mine", "--edit", "--new", "--from", "go", "--bundle", "go", "--user")
		Expect(r.err).To(HaveSuffix("error: --from copies a profile and --bundle combines several; pick one\n" +
			"  hint: keep --from: lola profile mine --edit --from go --new --user\n  hint: keep --bundle: lola profile mine --bundle go --edit --new --user\n"))
		r = lola("profile", "go", "--edit", "kit", "--bundle", "notes", "--user", "-y")
		Expect(r.err).To(HaveSuffix("error: --bundle makes or edits a profile's own file; the modules kit name are a separate edit\n" +
			"  hint: drop them: lola profile go --bundle notes --edit --user\n  hint: or drop --bundle: lola profile go kit --edit --user\n"))
		for _, run := range []string{"lola profile mine --edit --from go --user --new", "lola profile mine --bundle go --edit --new --user",
			"lola profile go --bundle notes --edit --user", "lola profile go kit --edit --user"} {
			Expect(lola(strings.Fields(run)[1:]...).code).To(Equal(0), run)
			Expect(os.RemoveAll(filepath.Join(p.UserProfiles, "mine.yml"))).To(Succeed())
			Expect(os.WriteFile(filepath.Join(p.UserProfiles, "go.yml"), []byte("version: 1\n"), 0o600)).To(Succeed())
		}

		r = lola("profile", "go", "--edit", "kit", "--bundle", "go", "--user")
		Expect(r.err).To(HaveSuffix("error: --bundle makes or edits a profile's own file; the modules kit name are a separate edit\n"+
			"  hint: or drop --bundle: lola profile go kit --edit --user\n"), "a bundle cannot name itself: no hint that would")
		writeTree(p.UserProfiles, map[string]string{"mods.yml": "version: 1\nmodules:\n  - {name: kit, source: " + kit + "}\n"})
		r = lola("profile", "mods", "--edit", "--bundle", "go", "--from", "go", "--new", "--user")
		Expect(r.err).To(HaveSuffix("error: --from copies a profile and --bundle combines several; pick one\n  hint: see the profiles: lola profile --user\n"),
			"mods exists: --new makes neither")
		r = lola("profile", "go", "--edit", "--from", "mods", "--user")
		Expect(r.err).To(HaveSuffix("error: --from makes a new profile and goes with --new, and go exists\n  hint: see it: lola profile --user go\n"))
		Expect(lola("profile", "--user", "go").code).To(Equal(0), "the hint runs as printed")
	})

	It("refuses --edit --bundle with no name, saying to name the bundle, its hints running as printed (ruling P4)", func() {
		writeTree(p.UserProfiles, map[string]string{"work.yml": "version: 1\nbundle: [go]\n"})
		Expect(lola("profile", "work", "--switch", "--user", "-y").code).To(Equal(0))
		r := lola("profile", "--edit", "--bundle", "notes", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HaveSuffix("error: --bundle sets the members of the bundle --edit names, and none was named: name the bundle to edit, as in " +
			"lola profile <bundle> --edit --bundle <profile>,<profile>\n" +
			"  hint: add them to work, the bundle on: lola profile --user work --edit --bundle go,notes\n" +
			"  hint: or make a bundle of them: lola profile --user notes-2 --edit --new --bundle notes\n"))
		Expect(r.err).NotTo(ContainSubstring("no profile  for"))
		for _, run := range []string{"lola profile --user notes-2 --edit --new --bundle notes", "lola profile --user work --edit --bundle go,notes"} {
			Expect(lola(strings.Fields(run)[1:]...).code).To(Equal(0), run)
		}
	})

	It("refuses, off a terminal, an edit of a project bundle whose members a pull changed, naming and showing every change its hint accepts", func() {
		proj := filepath.Join(tmp, "proj")
		team, lint := filepath.Join(proj, ".lola", "profiles", "team.yml"), filepath.Join(proj, ".lola", "profiles", "lint.yml")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n", ".lola/profiles/lint.yml": "version: 1\ntargets: [claude-code]\n",
			".lola/profiles/deploy.yml": "version: 1\n", ".lola/profiles/team.yml": "version: 1\nbundle: [lint]\n"})
		GinkgoT().Chdir(proj)
		Expect(lola("profile", "team", "--switch", "--trust=all", "-y").code).To(Equal(0))
		Expect(os.WriteFile(team, []byte("version: 1\nbundle: [lint, deploy]\n"), 0o600)).To(Succeed()) // as a pull would
		Expect(os.WriteFile(lint, []byte("version: 1\ntargets: [claude-code, cursor]\n"), 0o600)).To(Succeed())

		r := lola("profile", "team", "--edit", "--bundle", "lint,extra")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HaveSuffix("error: lola profile needs your trust for changes to the members of project:team and changes to the targets of project:lint, " +
			"and there is no terminal to ask on; nothing was written\n  hint: see them: lola profile --project team --edit --bundle lint,extra -n\n" +
			"  hint: then accept them: lola profile --project team --edit --bundle lint,extra --trust=profiles\n"))
		Expect(os.ReadFile(team)).To(Equal([]byte("version: 1\nbundle: [lint, deploy]\n")))
		r = lola("profile", "--edit", "--bundle", "x")
		Expect(r.err).NotTo(ContainSubstring("add them"), "no hint repeats the pulled list")

		r = lola("profile", "--project", "team", "--edit", "--bundle", "lint,extra", "-n")
		Expect(r.code).To(Equal(0), "the see hint runs as printed: "+r.err)
		Expect(r.out).To(And(ContainSubstring("changed since lola last wrote it or you last trusted it:\n"), MatchRegexp(`in profile project:team .*\n  ~ bundle +\[lint\] → \[lint, deploy\]\n`),
			MatchRegexp(`in profile project:lint .*\n  [+~] targets +.*\[claude-code, cursor\]\n`)), "it shows every change the accept hint takes in")
		Expect(r.out).NotTo(ContainSubstring("is unchanged"))
		Expect(lola("profile", "--project", "team", "--edit", "--bundle", "lint,extra", "--trust=profiles").code).To(Equal(0), "the accept hint runs as printed")
		Expect(os.ReadFile(team)).To(Equal([]byte("version: 1\nbundle: [lint, extra]\n")))
	})

	It("edits a bundle of yours on at user scope from a project, applying it in both scopes, each scope's context line first (ruling P1)", func() {
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\n"}) // kit comes through go alone
		Expect(lola("sync", "--user", "-y").code).To(Equal(0))
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + "}\n",
			"work.yml": "version: 1\nbundle: []\n"})
		Expect(lola("profile", "work", "--switch", "--user", "-y").code).To(Equal(0))
		proj := filepath.Join(tmp, "proj")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n"})
		Expect(lola("sync", "-C", proj, "--trust=all", "-y").code).To(Equal(0))
		GinkgoT().Chdir(proj)

		r := lola("profile", "work", "--edit", "--bundle", "go")
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(p.Home, ".claude", "skills", "a", "SKILL.md")).To(BeAnExistingFile(), "applied at user scope, where work is on")
		Expect(r.err).To(HavePrefix("project " + proj + ": project.yml\nuser scope: bundle work () over default\n"))
		Expect(r.out).To(ContainSubstring("saved to bundle work; it is not on in this project → lola profile --project work --switch\n"))
		Expect(r.out).To(ContainSubstring("user:\n"))
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "work.yml"))).To(Equal([]byte("version: 1\nbundle: [go]\n")))

		r = lola("profile", "work", "--edit", "--bundle", "")
		Expect(r.code).To(Equal(1), r.out+r.err)
		Expect(r.err).To(HaveSuffix("error: lola profile needs your confirmation to remove kit (claude-code) at user scope, and there is no terminal to ask on; nothing was written\n" +
			"  hint: see what goes: lola profile --project work --edit --bundle '' -n\n  hint: then confirm them: lola profile --project work --edit --bundle '' -y\n"))
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "work.yml"))).To(Equal([]byte("version: 1\nbundle: [go]\n")))
		Expect(lola("profile", "work", "--edit", "--bundle", "", "-n").code).To(Equal(0), "the -n hint runs as printed")
		Expect(lola("profile", "work", "--edit", "--bundle", "", "-y").code).To(Equal(0), "the hint runs as printed")
		Expect(filepath.Join(p.Home, ".claude", "skills", "a", "SKILL.md")).NotTo(BeAnExistingFile())
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "work.yml"))).To(Equal([]byte("version: 1\nbundle: []\n")))
	})

	It("keeps --json in the take-out a bundle's broken member hints, which runs as printed", func() {
		writeTree(p.UserProfiles, map[string]string{"docs.yml": "version: 1\n", "work.yml": "version: 1\nbundle: [docs]\n"})
		Expect(lola("profile", "work", "--switch", "--user").code).To(Equal(0))
		writeTree(p.UserProfiles, map[string]string{"work.yml": "version: 1\nbundle: [docs, gone]\n"})
		r := lola("sync", "--user", "--json")
		Expect(r.code).NotTo(Equal(0))
		var refusal struct {
			Hints []string `json:"hints"`
		}
		Expect(json.Unmarshal([]byte(r.err), &refusal)).To(Succeed(), r.err)
		Expect(refusal.Hints).To(Equal([]string{"see the profiles: lola profile --user --json", "or take it out: lola profile --user work --edit --bundle docs --json"}))
		r = lola(strings.Fields(strings.TrimPrefix(refusal.Hints[1], "or take it out: "))[1:]...)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("{"), "the run as printed prints JSON")
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "work.yml"))).To(Equal([]byte("version: 1\nbundle: [docs]\n")))
	})

	Describe("on a terminal", func() {
		var lint string
		BeforeEach(func() {
			lint = filepath.Join(filepath.Dir(kit), "lint")
			writeTree(lint, map[string]string{"lola.yml": "name: lint\n", "commands/lint.md": "L\n"})
			Expect(lola("cache", "add", lint).code).To(Equal(0))
		})

		It("shows the profile's module page, its modules ticked, a component page per module, and removes what you untick once asked", func() {
			writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + "}\n" +
				"  - {name: notes, source: " + filepath.Join(filepath.Dir(kit), "gone") + "}\n"})
			sp := &scripted{
				modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){func(cli.ModuleStep) (cli.ModuleAnswer, error) {
					return cli.ModuleAnswer{Names: []string{"kit", "lint"}}, nil
				}},
				components: []func(cli.ComponentPage) ([]string, error){func(cli.ComponentPage) ([]string, error) { return []string{"skill/a"}, nil }},
				review:     []string{cli.ReviewApply}, confirm: []bool{true}}
			r := lolaP(sp, "profile", "go", "--edit", "--user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(sp.moduleSteps[0]).To(And(HaveField("Title", "profile go › modules"), HaveField("None", true), HaveField("Preselect", []string{"kit", "notes"})))
			Expect(sp.moduleSteps[0].Options).To(ContainElement(cli.ModuleOption{Name: "notes", Label: "notes (in profile go; not in your cache)"}))
			Expect(sp.calls).To(ContainElements("components:profile go › kit", "confirm:Remove notes from profile go?"))
			b, err := os.ReadFile(filepath.Join(p.UserProfiles, "go.yml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(And(ContainSubstring("name: lint"), Not(ContainSubstring("notes")), ContainSubstring("skip: [skill/b]")))
		})

		It("keeps what you untick when you say No, and shows a bundle's members page, the members kept in order and new ones after", func() {
			writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + "}\n"})
			sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){func(cli.ModuleStep) (cli.ModuleAnswer, error) { return cli.ModuleAnswer{}, nil }},
				confirm: []bool{false}}
			r := lolaP(sp, "profile", "go", "--edit", "--user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(os.ReadFile(filepath.Join(p.UserProfiles, "go.yml"))).To(ContainSubstring("name: kit"), "No keeps it")

			writeTree(p.UserProfiles, map[string]string{"docs.yml": "version: 1\n", "work.yml": "version: 1\nbundle: [go]\n"})
			sp = &scripted{members: []func(cli.MembersStep) ([]string, error){func(cli.MembersStep) ([]string, error) { return []string{"docs", "go"}, nil }}}
			r = lolaP(sp, "profile", "work", "--edit", "--user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(sp.memberSteps).To(Equal([]cli.MembersStep{{Title: "bundle work › members", Options: []cli.ModuleOption{{Name: "docs", Label: "docs"}, {Name: "go", Label: "go"}}, Preselect: []string{"go"}}}))
			Expect(os.ReadFile(filepath.Join(p.UserProfiles, "work.yml"))).To(Equal([]byte("version: 1\nbundle: [go, docs]\n")))
		})

		It("asks what a new profile is for a name no profile has: a bundle of the members picked, or an empty profile when nothing is picked", func() {
			sp := &scripted{where: []int{1}, members: []func(cli.MembersStep) ([]string, error){func(cli.MembersStep) ([]string, error) { return []string{"go"}, nil }}}
			r := lolaP(sp, "profile", "both", "--edit", "--user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(sp.whereSteps).To(Equal([]cli.WhereStep{{Title: "New profile both:", Options: []string{"Lists modules", "Bundles profiles"}}}))
			Expect(sp.memberSteps[0].Title).To(Equal("new bundle both › members"))
			Expect(os.ReadFile(filepath.Join(p.UserProfiles, "both.yml"))).To(Equal([]byte("version: 1\nbundle: [go]\n")))

			sp = &scripted{where: []int{0}, modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){func(cli.ModuleStep) (cli.ModuleAnswer, error) { return cli.ModuleAnswer{}, nil }}}
			r = lolaP(sp, "profile", "try", "--edit", "--user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(sp.moduleSteps[0].Title).To(Equal("new profile try › modules"))
			Expect(os.ReadFile(filepath.Join(p.UserProfiles, "try.yml"))).To(Equal([]byte("version: 1\n")))
		})

		It("offers --on of a profile while a bundle is on its addition to that bundle, which keeps the bundle on", func() {
			writeTree(p.UserProfiles, map[string]string{"docs.yml": "version: 1\n", "lint.yml": "version: 1\n", "work.yml": "version: 1\nbundle: [go, docs]\n"})
			Expect(lola("profile", "work", "--switch", "--user").code).To(Equal(0))
			sp := &scripted{where: []int{2}}
			r := lolaP(sp, "profile", "lint", "--on", "--user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(sp.whereSteps).To(Equal([]cli.WhereStep{{Title: "user:work is on.",
				Options: []string{"Switch to user:lint", "Make bundle work-lint of work and lint", "Add user:lint to bundle work"}}}))
			Expect(r.out).To(ContainSubstring("✓ changed bundle work (go, docs → go, docs, lint)"))
			Expect(r.err).To(Equal("user scope: bundle work (go, docs) over default\n"), "the context line once, before the question")
			Expect(os.ReadFile(filepath.Join(p.UserProfiles, "work.yml"))).To(Equal([]byte("version: 1\nbundle: [go, docs, lint]\n")))
		})

		It("Src-P2-6-bundle: a forceable skip in a profile added to the bundle on hints the bundle edit, not a plain profile run", func() {
			kit := filepath.Join(tmp, "kit")
			writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "agents/critic.md": "Criticize.\n"})
			writeTree(p.UserProfiles, map[string]string{"docs.yml": "version: 1\n", "work.yml": "version: 1\nbundle: [go, docs]\n",
				"lint.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + "}\n"})
			Expect(lola("profile", "work", "--switch", "--user").code).To(Equal(0))
			r := lolaP(&scripted{where: []int{2}}, "profile", "lint", "--on", "--user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("render it anyway: lola profile --user work --edit --bundle go,docs,lint --force\n"))
		})

		It("prints each scope's context line once when --on's addition to a bundle applies in two scopes", func() {
			writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n", "docs.yml": "version: 1\n", "lint.yml": "version: 1\n",
				"work.yml": "version: 1\nbundle: [go, docs]\n"})
			proj := filepath.Join(tmp, "proj")
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\n"})
			GinkgoT().Chdir(proj)
			Expect(lola("profile", "work", "--switch", "--user").code).To(Equal(0))
			Expect(lola("profile", "user:work", "--switch", "--project").code).To(Equal(0))
			sp := &scripted{where: []int{2}, scope: []func(cli.ScopeStep) ([]string, error){scopes("project")}}
			r := lolaP(sp, "profile", "lint", "--on")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.err).To(Equal("project "+proj+": bundle work (go, docs) over project.yml\n"+
				"user scope: bundle work (go, docs) over default\n"), "read's line before the question, the other scope's after it")
			Expect(os.ReadFile(filepath.Join(p.UserProfiles, "work.yml"))).To(Equal([]byte("version: 1\nbundle: [go, docs, lint]\n")))
		})

		It("offers each profile once on a bundle's members page, as its file spells it, and a member that resolves to none on its own (ruling P11)", func() {
			work := filepath.Join(p.UserProfiles, "work.yml")
			writeTree(p.UserProfiles, map[string]string{"docs.yml": "version: 1\n", "work.yml": "version: 1\nbundle: [user:go, gone]\n"})
			sp := &scripted{members: []func(cli.MembersStep) ([]string, error){func(cli.MembersStep) ([]string, error) { return []string{"user:go", "docs"}, nil }}}
			r := lolaP(sp, "profile", "work", "--edit", "--user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(sp.memberSteps).To(Equal([]cli.MembersStep{{Title: "bundle work › members", Options: []cli.ModuleOption{
				{Name: "gone", Label: "gone (names no profile here)"}, {Name: "docs", Label: "docs"}, {Name: "user:go", Label: "user:go"}}, Preselect: []string{"user:go", "gone"}}}))
			Expect(os.ReadFile(work)).To(Equal([]byte("version: 1\nbundle: ['user:go', docs]\n")))
		})

		It("never records a pulled member list as trusted from the members page: Enter changes nothing, and a change asks the trust question first, -y or not (ruling P6)", func() {
			proj := filepath.Join(tmp, "proj")
			team := filepath.Join(proj, ".lola", "profiles", "team.yml")
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n", ".lola/profiles/lint.yml": "version: 1\n",
				".lola/profiles/deploy.yml": "version: 1\n", ".lola/profiles/team.yml": "version: 1\nbundle: [lint]\n"})
			GinkgoT().Chdir(proj)
			Expect(lola("profile", "team", "--switch", "--trust=all", "-y").code).To(Equal(0))
			Expect(os.WriteFile(team, []byte("version: 1\nbundle: [lint, deploy]\n"), 0o600)).To(Succeed()) // as a pull would

			sp := &scripted{members: []func(cli.MembersStep) ([]string, error){func(st cli.MembersStep) ([]string, error) { return st.Preselect, nil }}}
			r := lolaP(sp, "profile", "team", "--edit")
			Expect(r.code).To(Equal(0), r.err)
			Expect(sp.memberSteps[0]).To(And(HaveField("Title", "bundle team › members"), HaveField("Preselect", []string{"lint", "deploy"})))
			Expect(sp.calls).NotTo(ContainElement("trust"))
			Expect(r.out).To(ContainSubstring("bundle team already lists lint, deploy"))
			r = lola("sync")
			Expect(r.code).To(Equal(1), "the pulled list is still untrusted: "+r.out)
			Expect(r.err).To(ContainSubstring("trust"))

			for _, extra := range [][]string{nil, {"-y"}, {"--force"}} {
				sp = &scripted{members: []func(cli.MembersStep) ([]string, error){func(cli.MembersStep) ([]string, error) { return []string{"lint"}, nil }},
					trust: []func(cli.TrustStep) (engine.TrustAnswer, error){func(cli.TrustStep) (engine.TrustAnswer, error) { return engine.TrustAnswer{}, nil }}}
				r = lolaP(sp, append([]string{"profile", "team", "--edit"}, extra...)...)
				Expect(sp.calls).To(Equal([]string{"members", "trust"}), "%v", extra)
				Expect(r.err).To(MatchRegexp(`~ bundle +\[lint\] → \[lint, deploy\]`), "the question shows the pulled change")
				Expect(os.ReadFile(team)).To(Equal([]byte("version: 1\nbundle: [lint, deploy]\n")), "declined: nothing written")
			}
		})

		It("removes what you untick unasked under -y or --force, and keeps --edit's refusal for picks without a module", func() {
			goYml := filepath.Join(p.UserProfiles, "go.yml")
			for _, flag := range []string{"-y", "--force"} {
				writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\nmodules:\n  - {name: kit, source: " + kit + "}\n"})
				sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){func(cli.ModuleStep) (cli.ModuleAnswer, error) { return cli.ModuleAnswer{}, nil }}}
				r := lolaP(sp, "profile", "go", "--edit", "--user", flag)
				Expect(r.code).To(Equal(0), r.err)
				Expect(sp.calls).To(Equal([]string{"modules"}), flag)
				Expect(os.ReadFile(goYml)).To(Equal([]byte("version: 1\n")), flag)
			}
			sp := &scripted{}
			r := lolaP(sp, "profile", "go", "--edit", "--only", "skill/a", "--user")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(ContainSubstring("lola profile --edit edits profile go by its modules: name one"))
			Expect(sp.calls).To(BeEmpty())
		})

		It("removes what you untick in each scope the profile is on, and under -n says it would", func() {
			proj := filepath.Join(tmp, "proj")
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n"})
			Expect(lola("sync", "-C", proj, "--trust=entries", "-y").code).To(Equal(0))
			GinkgoT().Chdir(proj)
			goYml := filepath.Join(p.UserProfiles, "go.yml")
			writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\n", // kit comes from go alone
				"go.yml": "version: 1\nmodules:\n  - {name: kit, source: " + kit + "}\n"})
			Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
			Expect(lola("profile", "user:go", "--switch", "-y").code).To(Equal(0))
			skillA := []string{filepath.Join(p.Home, ".claude", "skills", "a", "SKILL.md"), filepath.Join(proj, ".claude", "skills", "a", "SKILL.md")}
			untick := func() *scripted {
				return &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){func(cli.ModuleStep) (cli.ModuleAnswer, error) { return cli.ModuleAnswer{}, nil }},
					confirm: []bool{true, true}}
			}

			sp := untick()
			r := lolaP(sp, "profile", "go", "--edit", "-n")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("would then apply the removal at user scope, where profile go is on too"))
			Expect(r.out).NotTo(ContainSubstring("lola sync --user"), "no notice sends you to apply what the run applies")
			Expect(os.ReadFile(goYml)).To(ContainSubstring("name: kit"))

			sp = untick()
			r = lolaP(sp, "profile", "go", "--edit")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.err).To(HavePrefix("project " + proj + ": profile go over project.yml\nuser scope: profile go over default\n"))
			Expect(sp.calls).To(Equal([]string{"modules", "confirm:Remove kit from profile go?", "confirm:Remove these there too? No leaves that scope as it was"}))
			Expect(r.out).To(And(ContainSubstring("user:\n"), Not(ContainSubstring("lola sync --user"))))
			Expect(os.ReadFile(goYml)).NotTo(ContainSubstring("kit"))
			for _, f := range skillA {
				Expect(f).NotTo(BeAnExistingFile())
			}

			writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\nmodules:\n  - {name: kit, source: " + kit + "}\n"})
			Expect(lola("sync", "--user", "-y").code).To(Equal(0))
			Expect(lola("sync", "-y").code).To(Equal(0))
			Expect(skillA[0]).To(BeAnExistingFile())
			sp = untick()
			r = lolaP(sp, "profile", "go", "--edit", "--force")
			Expect(r.code).To(Equal(0), r.err)
			Expect(sp.calls).To(Equal([]string{"modules"}), "--force answers both removal questions")
			for _, f := range skillA {
				Expect(f).NotTo(BeAnExistingFile())
			}
		})

		It("removes what you untick when what you keep is unchanged, showing the removal first, and lists it in the review -y applies", func() {
			goYml := filepath.Join(p.UserProfiles, "go.yml")
			lintMd := filepath.Join(p.Home, ".claude", "commands", "lint.md")
			writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\n",
				"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + "}\n  - {name: lint, source: " + lint + "}\n"})
			Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
			Expect(lintMd).To(BeAnExistingFile())
			sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){func(cli.ModuleStep) (cli.ModuleAnswer, error) {
				return cli.ModuleAnswer{Names: []string{"kit"}}, nil
			}}, components: []func(cli.ComponentPage) ([]string, error){func(cli.ComponentPage) ([]string, error) { return []string{"skill/a", "skill/b"}, nil }},
				confirm: []bool{true}}
			r := lolaP(sp, "profile", "go", "--edit", "--user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(sp.calls).To(ContainElement("confirm:Remove lint from profile go?"))
			Expect(r.err).To(And(ContainSubstring("Remove lint from profile go, as you unticked at user scope:\n"), ContainSubstring("→ delete claude-code .claude/commands/lint.md")))
			Expect(r.out).NotTo(ContainSubstring("already up to date"))
			Expect(os.ReadFile(goYml)).NotTo(ContainSubstring("lint"))
			Expect(lintMd).NotTo(BeAnExistingFile())

			writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: lint, source: " + lint + "}\n"})
			Expect(lola("sync", "--user", "-y").code).To(Equal(0))
			Expect(lintMd).To(BeAnExistingFile())
			sp = &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){func(cli.ModuleStep) (cli.ModuleAnswer, error) {
				return cli.ModuleAnswer{Names: []string{"kit"}}, nil
			}}, components: []func(cli.ComponentPage) ([]string, error){func(cli.ComponentPage) ([]string, error) { return []string{"skill/a"}, nil }}}
			r = lolaP(sp, "profile", "go", "--edit", "--user", "-y")
			Expect(r.code).To(Equal(0), r.err)
			Expect(sp.calls).NotTo(ContainElement(HavePrefix("confirm:")))
			Expect(r.err).To(MatchRegexp(`(?s)install.*Remove lint from profile go, as you unticked at user scope:\n.*→ delete claude-code \.claude/commands/lint\.md`),
				"the review -y applies lists the removal")
			Expect(strings.Count(r.err, "Remove lint from profile go, as you unticked")).To(Equal(1), "shown once")
			Expect(lintMd).NotTo(BeAnExistingFile())
			Expect(os.ReadFile(goYml)).To(And(ContainSubstring("name: kit"), Not(ContainSubstring("lint"))))
		})

		It("says the profile is unchanged when only modules your cache lacks are kept, hinting the lola cache add that lets you pick them", func() {
			notes := filepath.Join(tmp, "notes")
			writeTree(notes, map[string]string{"lola.yml": "name: notes\n", "commands/n.md": "N\n"})
			writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\nmodules:\n  - {name: notes, source: " + notes + "}\n"})
			sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){func(cli.ModuleStep) (cli.ModuleAnswer, error) {
				return cli.ModuleAnswer{Names: []string{"notes"}}, nil
			}}}
			r := lolaP(sp, "profile", "go", "--edit", "--user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(Equal("→ profile go is unchanged: notes is not in your cache, so its entry stays as it is\n" +
				"→ to pick its components, add it to your cache, then edit again: lola cache add " + notes + "\n"))
			Expect(r.err).NotTo(ContainSubstring("aborted"))
			Expect(lola("cache", "add", notes).code).To(Equal(0), "the hint runs as printed")
		})

		Describe("an inherited module (spec 2026-10-29 S2, §3.6)", func() {
			var goYml, defaultYml string
			var pickA func(cli.ComponentPage) ([]string, error)
			BeforeEach(func() {
				goYml, defaultYml = filepath.Join(p.UserProfiles, "go.yml"), filepath.Join(p.UserProfiles, "default.yml")
				pickA = func(cli.ComponentPage) ([]string, error) { return []string{"skill/a"}, nil }
				Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
			})

			It("asks where the edit goes before the page, which is titled with the profile Override writes", func() {
				before, err := os.ReadFile(defaultYml)
				Expect(err).NotTo(HaveOccurred())
				sp := &scripted{where: []int{1}, components: []func(cli.ComponentPage) ([]string, error){pickA}, review: []string{cli.ReviewApply}}
				r := lolaP(sp, "profile", "--edit", "kit", "--user")
				Expect(r.code).To(Equal(0), r.err)
				Expect(sp.calls).To(Equal([]string{"where", "components:profile go › kit", "review"}), "the question first, then the page for the file it chose")
				Expect(sp.whereSteps[0].Title).To(Equal("kit comes from default (base), loaded under every profile at user scope."))
				Expect(sp.pages[0].Groups[0].Title).To(Equal("skills (2)"))
				Expect(os.ReadFile(goYml)).To(ContainSubstring("name: kit"))
				Expect(os.ReadFile(defaultYml)).To(Equal(before))
			})

			It("titles the page with the base when the answer changes it there", func() {
				sp := &scripted{where: []int{0}, components: []func(cli.ComponentPage) ([]string, error){pickA}, review: []string{cli.ReviewApply}}
				r := lolaP(sp, "profile", "--edit", "kit", "--user")
				Expect(r.code).To(Equal(0), r.err)
				Expect(sp.calls).To(Equal([]string{"where", "components:default (base) › kit", "review"}))
				Expect(os.ReadFile(defaultYml)).To(ContainSubstring("  - name: kit\n    source: " + kit + "\n    skip: [skill/b]\n"))
				Expect(os.ReadFile(defaultYml)).NotTo(ContainSubstring("only:"))
				Expect(os.ReadFile(goYml)).To(Equal([]byte("version: 1\n")))
			})

			It("opens no page and writes nothing when the question is cancelled, and writes nothing under -n", func() {
				before, err := os.ReadFile(defaultYml)
				Expect(err).NotTo(HaveOccurred())
				sp := &scripted{where: []int{-1}}
				r := lolaP(sp, "profile", "--edit", "kit", "--user")
				Expect(r.code).To(Equal(0), r.err)
				Expect(sp.calls).To(Equal([]string{"where"}), r.err)
				Expect(r.err).To(HaveSuffix("aborted; nothing changed.\n"))

				sp = &scripted{where: []int{1}, components: []func(cli.ComponentPage) ([]string, error){pickA}, review: []string{cli.ReviewApply}}
				r = lolaP(sp, "profile", "--edit", "kit", "--user", "-n")
				Expect(r.code).To(Equal(0), r.err)
				Expect(sp.calls).To(Equal([]string{"where", "components:profile go › kit", "review"}), "-n opens the page and asks")
				Expect(os.ReadFile(goYml)).To(Equal([]byte("version: 1\n")))
				Expect(os.ReadFile(defaultYml)).To(Equal(before))
			})

			It("says nothing changed when Override keeps the picks the module has, and writes no entry", func() {
				pickBoth := func(cli.ComponentPage) ([]string, error) { return []string{"skill/a", "skill/b"}, nil }
				sp := &scripted{where: []int{1}, components: []func(cli.ComponentPage) ([]string, error){pickBoth}}
				r := lolaP(sp, "profile", "--edit", "kit", "--user")
				Expect(r.code).To(Equal(0), r.err)
				Expect(sp.calls).To(Equal([]string{"where", "components:profile go › kit"}))
				Expect(r.out).To(Equal("→ nothing changed: kit's picks are as they were, so profile go gets no entry of its own (an override is written only with a change)\n"))
				Expect(os.ReadFile(goYml)).To(Equal([]byte("version: 1\n")))
			})

			It("asks a bundle's member question, then the inherited-edit question, before the page", func() {
				writeTree(p.UserProfiles, map[string]string{"docs.yml": "version: 1\n", "work.yml": "version: 1\nbundle: [docs, go]\n"})
				Expect(lola("profile", "work", "--switch", "--user", "-y").code).To(Equal(0))
				sp := &scripted{where: []int{0, 1}, components: []func(cli.ComponentPage) ([]string, error){pickA}, review: []string{cli.ReviewApply}}
				r := lolaP(sp, "profile", "--edit", "kit", "--user")
				Expect(r.code).To(Equal(0), r.err)
				Expect(sp.calls).To(Equal([]string{"where", "where", "components:profile go › kit", "review"}), "each question once, both before the page")
				Expect(sp.whereSteps[0]).To(Equal(cli.WhereStep{Title: "work is a bundle. Which profile gets kit?", Options: []string{"go  (last member: wins overlaps)", "docs"}}))
				Expect(sp.whereSteps[1].Title).To(Equal("kit comes from default (base), loaded under every profile at user scope."))
				Expect(os.ReadFile(goYml)).To(ContainSubstring("name: kit"))
				Expect(os.ReadFile(filepath.Join(p.UserProfiles, "docs.yml"))).To(Equal([]byte("version: 1\n")))
			})

			It("asks where to write while a remote profile is on before the page, titled with the base", func() {
				body := "version: 1\n"
				o, err := json.Marshal(profile.Origin{Version: profile.OriginVersion, Source: "https://example.com/profiles.git",
					Commit: strings.Repeat("c", 40), Digest: profile.Digest([]byte(body))})
				Expect(err).NotTo(HaveOccurred())
				writeTree(filepath.Join(p.RemoteProfiles, "team"), map[string]string{profile.RemoteFile: body, profile.OriginFile: string(o)})
				led, err := ledger.Load(p.UserLedger)
				Expect(err).NotTo(HaveOccurred())
				led.Profile = &ledger.Active{Name: "remote:team", By: ledger.ByOn}
				Expect(led.Save(p.UserLedger)).To(Succeed())
				sp := &scripted{where: []int{0}, components: []func(cli.ComponentPage) ([]string, error){pickA}, review: []string{cli.ReviewApply}}
				r := lolaP(sp, "profile", "--edit", "kit", "--user")
				Expect(r.code).To(Equal(0), r.err)
				Expect(sp.calls).To(Equal([]string{"where", "components:default (base) › kit", "review"}))
				Expect(sp.whereSteps[0]).To(Equal(cli.WhereStep{Title: "remote:team is read-only.", Options: []string{"Write to the base (default)"}}))
				Expect(os.ReadFile(defaultYml)).To(ContainSubstring("  - name: kit\n    source: " + kit + "\n    skip: [skill/b]\n"))
				Expect(os.ReadFile(defaultYml)).NotTo(ContainSubstring("only:"))
			})

			It("asks lola install --profile's inherited-edit question before the page; a plain install names no file a later question decides", func() {
				sp := &scripted{where: []int{0}, components: []func(cli.ComponentPage) ([]string, error){pickA}, review: []string{cli.ReviewApply}}
				r := lolaP(sp, "install", "kit", "--profile", "go", "--user")
				Expect(r.code).To(Equal(0), r.err)
				Expect(sp.calls).To(Equal([]string{"where", "components:default (base) › kit", "review"}))
				Expect(os.ReadFile(defaultYml)).To(ContainSubstring("  - name: kit\n    source: " + kit + "\n    skip: [skill/b]\n"))
				Expect(os.ReadFile(defaultYml)).NotTo(ContainSubstring("only:"))
				Expect(os.ReadFile(goYml)).To(Equal([]byte("version: 1\n")))

				pickBoth := func(cli.ComponentPage) ([]string, error) { return []string{"skill/a", "skill/b"}, nil }
				sp = &scripted{where: []int{1}, components: []func(cli.ComponentPage) ([]string, error){pickBoth}, review: []string{cli.ReviewApply}}
				r = lolaP(sp, "install", "kit", "--user")
				Expect(r.code).To(Equal(0), r.err)
				Expect(sp.calls).To(Equal([]string{"components:kit", "where", "review"}), "the question the picks lead to follows the page, which names no file")
				Expect(os.ReadFile(goYml)).To(ContainSubstring("name: kit"))
			})

			It("says where each part of a Change asked before the page goes, as each edit then writes: picks to selections.yaml, assistants to project.yml", func() {
				proj := filepath.Join(tmp, "proj")
				projectYml := filepath.Join(proj, ".lola", "project.yml")
				writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n"})
				Expect(lola("sync", "-C", proj, "--trust=entries", "-y").code).To(Equal(0))
				Expect(lola("install", "kit", "-C", proj, "-y").code).To(Equal(0))
				Expect(lola("profile", "user:go", "--switch", "-C", proj, "-y").code).To(Equal(0))
				before, err := os.ReadFile(projectYml)
				Expect(err).NotTo(HaveOccurred())
				options := []string{"Change it for project.yml's module  (picks: selections.yaml; other edits: project.yml, shared)",
					"Override it in profile go  (go gets its own entry, starting from project.yml's)"}

				sp := &scripted{where: []int{0}, components: []func(cli.ComponentPage) ([]string, error){pickA}, review: []string{cli.ReviewApply}}
				r := lolaP(sp, "profile", "--edit", "kit", "-C", proj)
				Expect(r.code).To(Equal(0), r.err)
				Expect(sp.calls).To(Equal([]string{"where", "components:project.yml › kit", "review"}))
				Expect(sp.whereSteps[0].Options).To(Equal(options))
				Expect(os.ReadFile(p.Selections)).To(ContainSubstring("skill/b"), "picks alone: selections.yaml")
				Expect(os.ReadFile(projectYml)).To(Equal(before), "the shared file is untouched")

				sp = &scripted{where: []int{0}, components: []func(cli.ComponentPage) ([]string, error){pickA}, review: []string{cli.ReviewApply}}
				r = lolaP(sp, "profile", "--edit", "kit", "-C", proj, "-t", "claude-code,opencode")
				Expect(r.code).To(Equal(0), r.err)
				Expect(sp.calls).To(Equal([]string{"where", "components:project.yml › kit", "review"}))
				Expect(sp.whereSteps[0].Options).To(Equal(options))
				Expect(os.ReadFile(projectYml)).To(ContainSubstring("opencode"), "assistants: project.yml")
				Expect(os.ReadFile(goYml)).To(Equal([]byte("version: 1\n")))
			})

			It("opens no page and writes nothing when the bundle's member question or the remote profile's question is cancelled", func() {
				before, err := os.ReadFile(defaultYml)
				Expect(err).NotTo(HaveOccurred())
				writeTree(p.UserProfiles, map[string]string{"docs.yml": "version: 1\n", "work.yml": "version: 1\nbundle: [docs, go]\n"})
				Expect(lola("profile", "work", "--switch", "--user", "-y").code).To(Equal(0))
				sp := &scripted{where: []int{-1}}
				r := lolaP(sp, "profile", "--edit", "kit", "--user")
				Expect(r.code).To(Equal(0), r.err)
				Expect(sp.calls).To(Equal([]string{"where"}))
				Expect(sp.whereSteps[0].Title).To(Equal("work is a bundle. Which profile gets kit?"))
				Expect(r.err).To(HaveSuffix("aborted; nothing changed.\n"))
				Expect(os.ReadFile(goYml)).To(Equal([]byte("version: 1\n")))
				Expect(os.ReadFile(filepath.Join(p.UserProfiles, "docs.yml"))).To(Equal([]byte("version: 1\n")))

				body := "version: 1\n"
				o, err := json.Marshal(profile.Origin{Version: profile.OriginVersion, Source: "https://example.com/profiles.git",
					Commit: strings.Repeat("c", 40), Digest: profile.Digest([]byte(body))})
				Expect(err).NotTo(HaveOccurred())
				writeTree(filepath.Join(p.RemoteProfiles, "team"), map[string]string{profile.RemoteFile: body, profile.OriginFile: string(o)})
				led, err := ledger.Load(p.UserLedger)
				Expect(err).NotTo(HaveOccurred())
				led.Profile = &ledger.Active{Name: "remote:team", By: ledger.ByOn}
				Expect(led.Save(p.UserLedger)).To(Succeed())
				sp = &scripted{where: []int{-1}}
				r = lolaP(sp, "profile", "--edit", "kit", "--user")
				Expect(r.code).To(Equal(0), r.err)
				Expect(sp.calls).To(Equal([]string{"where"}))
				Expect(sp.whereSteps[0].Title).To(Equal("remote:team is read-only."))
				Expect(r.err).To(HaveSuffix("aborted; nothing changed.\n"))
				Expect(os.ReadFile(defaultYml)).To(Equal(before))
			})

			It("qualifies the profile in the page title when a profile of another kind has its name", func() {
				proj := filepath.Join(tmp, "proj")
				writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n", ".lola/profiles/go.yml": "version: 1\n"})
				Expect(lola("sync", "-C", proj, "--trust=entries", "-y").code).To(Equal(0))
				Expect(lola("install", "kit", "-C", proj, "-y").code).To(Equal(0))
				Expect(lola("profile", "user:go", "--switch", "-C", proj, "-y").code).To(Equal(0))
				sp := &scripted{where: []int{0}, components: []func(cli.ComponentPage) ([]string, error){pickA}, review: []string{cli.ReviewApply}}
				r := lolaP(sp, "profile", "--edit", "kit", "-C", proj)
				Expect(r.code).To(Equal(0), r.err)
				Expect(sp.calls).To(Equal([]string{"where", "components:project.yml › kit", "review"}), "the project's base, as the context line names it")
				Expect(sp.whereSteps[0].Options).To(Equal([]string{"Change it for project.yml's module  (picks: selections.yaml; other edits: project.yml, shared)",
					"Override it in profile user:go  (user:go gets its own entry, starting from project.yml's)"}))

				pickBoth := func(cli.ComponentPage) ([]string, error) { return []string{"skill/a", "skill/b"}, nil }
				sp = &scripted{where: []int{1}, components: []func(cli.ComponentPage) ([]string, error){pickBoth}, review: []string{cli.ReviewApply}}
				r = lolaP(sp, "profile", "--edit", "kit", "-C", proj)
				Expect(r.code).To(Equal(0), r.err)
				Expect(sp.calls).To(Equal([]string{"where", "components:profile user:go › kit", "review"}))
				Expect(os.ReadFile(goYml)).To(ContainSubstring("name: kit"))
			})
		})
	})
})
