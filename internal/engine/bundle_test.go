package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
	"github.com/trevor-vaughan/lola-go/internal/trust"
)

var _ = Describe("bundles", func() {
	var (
		tmp, proj, mods string
		p               paths.Paths
		e               *engine.Engine
		user, sc        engine.Scope
		ctx             = context.Background()
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj, mods = filepath.Join(tmp, "proj"), filepath.Join(tmp, "mods")
		e.Getwd = func() (string, error) { return proj, nil }
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		user, sc = engine.Scope{Kind: target.User}, engine.Scope{Kind: target.Project, ProjectDir: proj}
	})
	// mod writes a folder module named name under mods/dir whose command says text.
	mod := func(dir, name, text string) string {
		GinkgoHelper()
		path := filepath.Join(mods, dir)
		writeTree(path, map[string]string{"lola.yml": "name: " + name + "\n", "commands/" + name + ".md": text + "\n"})
		return path
	}
	writeProfile := func(name, content string) string {
		GinkgoHelper()
		writeTree(p.UserProfiles, map[string]string{name + ".yml": content})
		return filepath.Join(p.UserProfiles, name+".yml")
	}
	writeProject := func(name, content string) string {
		GinkgoHelper()
		writeTree(proj, map[string]string{".lola/profiles/" + name + ".yml": content})
		return filepath.Join(proj, ".lola", "profiles", name+".yml")
	}
	yes := func([]engine.Removal) (bool, error) { return true, nil }
	trustAll := func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }
	// switchTo runs lola profile <name> --switch in s, answering removals and trust yes.
	switchTo := func(s engine.Scope, name string) (*engine.SyncResult, error) {
		return e.Sync(ctx, engine.SyncRequest{Scope: s, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: name},
			ConfirmRemovals: yes, Trust: trustAll})
	}
	installed := func(s engine.Scope) map[string]string {
		GinkgoHelper()
		led, err := e.Ledger(s)
		Expect(err).NotTo(HaveOccurred())
		out := map[string]string{}
		for name, ent := range led.Modules {
			out[name] = ent.From
		}
		return out
	}
	entry := func(name, src string) string { return "  - {name: " + name + ", source: " + src + "}\n" }

	It("loads a bundle's members in order over the base, a later member's entry winning whole with a notice naming the bundle", func() {
		a, b1, b2, c := mod("a", "a", "A"), mod("b1", "b", "B from go-review"), mod("b2", "b", "B from docs"), mod("c", "c", "C")
		writeProfile("default", "version: 1\ntargets: [claude-code]\nmodules:\n"+entry("c", c))
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).NotTo(HaveOccurred())
		writeProfile("go-review", "version: 1\ntargets: [claude-code]\nmodules:\n"+entry("a", a)+entry("b", b1))
		writeProfile("docs", "version: 1\ntargets: [claude-code]\nmodules:\n"+entry("b", b2))
		writeProfile("work", "version: 1\ndescription: Go and docs work\nbundle: [go-review, docs]\n")

		res, err := switchTo(user, "work")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Activation.Profile).To(HaveValue(Equal("user:work")))
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
			Text: "b: profile docs overrides profile go-review in bundle work (" + b2 + " over " + b1 + ")"}))
		Expect(installed(user)).To(Equal(map[string]string{"a": "profile:user:go-review", "b": "profile:user:docs", "c": "profile:default"}),
			"lock.json's from is the member that won")
		b, err := os.ReadFile(filepath.Join(p.Home, ".claude", "commands", "b.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("B from docs\n"))
	})

	Describe("Prof-P1-a: the bundle view shows the winning member's picks", func() {
		var alpha string
		BeforeEach(func() {
			alpha = filepath.Join(mods, "alpha")
			files := map[string]string{"lola.yml": "name: alpha\n", "commands/hello.md": "hello\n"}
			for _, s := range []string{"a1", "a2", "a3"} {
				files["skills/"+s+"/SKILL.md"] = "---\ndescription: " + s + "\n---\n" + s + "\n"
			}
			writeTree(alpha, files)
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" + entry("alpha", alpha)})
		})
		// bundle writes members one and two, picking as the given entries' tails, and bundle both over them.
		bundle := func(onePicks, twoPicks string) {
			GinkgoHelper()
			writeProfile("one", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: alpha, source: "+alpha+", "+onePicks+"}\n")
			writeProfile("two", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: alpha, source: "+alpha+", "+twoPicks+"}\n")
			writeProfile("both", "version: 1\nbundle: [one, two]\n")
		}
		skills := func() []string {
			GinkgoHelper()
			ents, err := os.ReadDir(filepath.Join(proj, ".claude", "skills"))
			if os.IsNotExist(err) {
				return []string{}
			}
			Expect(err).NotTo(HaveOccurred())
			out := []string{}
			for _, ent := range ents {
				out = append(out, ent.Name())
			}
			return out
		}

		It("reports the later member's picks, and sync installs those", func() {
			bundle("only: [skill/a3]", "only: [skill/a1]")
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: "both"},
				ConfirmRemovals: yes, Trust: trustAll})
			Expect(err).NotTo(HaveOccurred())

			states, _, err := e.ProfileState(ctx, []engine.Scope{sc}, "both")
			Expect(err).NotTo(HaveOccurred())
			Expect(states).To(HaveLen(1))
			Expect(states[0].Modules).To(Equal([]engine.ProfileModule{{Name: "alpha", From: "profile:user:two",
				Overrides: []string{"project.yml", "profile:user:one"}, Only: []string{"skill/a1"}, Skip: []string{}}}))
			Expect(skills()).To(Equal([]string{"a1"}))
			Expect(filepath.Join(proj, ".claude", "commands", "hello.md")).NotTo(BeAnExistingFile())
		})

		It("reports the winning member's skips, not the first member's only", func() {
			bundle("only: [skill/a3]", "skip: [skill/a2, skill/a3, command/hello]")
			states, _, err := e.ProfileState(ctx, []engine.Scope{sc}, "both")
			Expect(err).NotTo(HaveOccurred())
			Expect(states[0].Modules).To(ConsistOf(And(HaveField("From", "profile:user:two"),
				HaveField("Only", BeEmpty()), HaveField("Skip", Equal([]string{"skill/a2", "skill/a3", "command/hello"})))))
		})

		It("a winning member that skips every component shows that, and sync writes none of the module", func() {
			bundle("only: [skill/a3]", "skip: [skill/a1, skill/a2, skill/a3, command/hello]")
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: "both"},
				ConfirmRemovals: yes, Trust: trustAll})
			Expect(err).NotTo(HaveOccurred())
			states, _, err := e.ProfileState(ctx, []engine.Scope{sc}, "both")
			Expect(err).NotTo(HaveOccurred())
			Expect(states[0].Modules).To(ConsistOf(And(HaveField("From", "profile:user:two"), HaveField("Only", BeEmpty()),
				HaveField("Skip", Equal([]string{"skill/a1", "skill/a2", "skill/a3", "command/hello"})))))
			Expect(skills()).To(BeEmpty())
			Expect(filepath.Join(proj, ".claude", "commands", "hello.md")).NotTo(BeAnExistingFile())
		})
	})

	It("expands nested bundles depth-first, a profile reached twice keeping its first place with a notice", func() {
		g, l, d := mod("g", "g", "G"), mod("l", "l", "L"), mod("d", "d", "D")
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n"+entry("g", g))
		writeProfile("go-lint", "version: 1\ntargets: [claude-code]\nmodules:\n"+entry("l", l))
		writeProfile("docs", "version: 1\ntargets: [claude-code]\nmodules:\n"+entry("d", d))
		writeProfile("go-all", "version: 1\nbundle: [go, go-lint]\n")
		writeProfile("work", "version: 1\nbundle: [go-all, go, docs]\n")

		res, err := switchTo(user, "work")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "go appears twice in bundle work; its first place counts"}))
		Expect(installed(user)).To(Equal(map[string]string{"g": "profile:user:go", "l": "profile:user:go-lint", "d": "profile:user:docs"}))

		writeProfile("work", "version: 1\nbundle: [go, go-all, docs]\n")
		res, err = e.Sync(ctx, engine.SyncRequest{Scope: user, ConfirmRemovals: yes})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "go appears twice in bundle work (via go-all); its first place counts"}))
	})

	It("refuses a cycle with the line that closes it and the chain, and a bundle nested deeper than 8", func() {
		writeProfile("work", "version: 1\nbundle: [a]\n")
		writeProfile("a", "version: 1\nbundle: [b]\n")
		b := writeProfile("b", "version: 1\nbundle:\n  - docs\n  - a\n")
		writeProfile("docs", "version: 1\n")
		_, err := switchTo(user, "work")
		Expect(err).To(MatchError(b + ":4: bundle: a closes a cycle: work → a → b → a"))
		Expect(errs.Hints(err)).To(BeEmpty(), "an edit of the file fixes a cycle; no command does")
		self := writeProfile("work", "version: 1\nbundle: [work]\n")
		_, err = switchTo(user, "work")
		Expect(err).To(MatchError(self + ":2: bundle: work closes a cycle: work → work"))

		writeProfile("x", "version: 1\n")
		writeProject("x", "version: 1\nbundle: [y]\n")
		y := writeProject("y", "version: 1\nbundle: [x]\n")
		_, err = switchTo(sc, "project:x")
		Expect(err).To(MatchError(y+":2: bundle: x closes a cycle: project:x → y → project:x"), "a name two kinds share is qualified")

		for i := 1; i <= 8; i++ {
			writeProfile(fmt.Sprintf("n%d", i), fmt.Sprintf("version: 1\nbundle: [n%d]\n", i+1))
		}
		writeProfile("n9", "version: 1\nbundle: [docs]\n")
		_, err = switchTo(user, "n1")
		Expect(err).To(MatchError(filepath.Join(p.UserProfiles, "n8.yml") + ":2: bundle: n9 nests bundles more than 8 deep"))
		writeProfile("n8", "version: 1\nbundle: [docs]\n")
		Expect(switchTo(user, "n1")).NotTo(BeNil(), "8 deep is the limit, not past it")
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Profile).To(Equal(&ledger.Active{Name: "user:n1", By: ledger.BySwitch}))
	})

	It("refuses a bundle that expands to more than 64 profiles, naming the member that crosses the cap", func() {
		var first, second []string
		for i := range 40 {
			first, second = append(first, fmt.Sprintf("a%d", i)), append(second, fmt.Sprintf("b%d", i))
			writeProfile(fmt.Sprintf("a%d", i), "version: 1\n")
			writeProfile(fmt.Sprintf("b%d", i), "version: 1\n")
		}
		writeProfile("big-a", "version: 1\nbundle:\n  - "+strings.Join(first, "\n  - ")+"\n")
		big := writeProfile("big-b", "version: 1\nbundle:\n  - "+strings.Join(second, "\n  - ")+"\n")
		writeProfile("work", "version: 1\nbundle: [big-a, big-b]\n")
		_, err := switchTo(user, "work")
		Expect(err).To(MatchError(big + ":27: bundle: b24 takes bundle work past 64 profiles"))
	})

	It("refuses a bundle that expands more than 64 bundles, naming the member that crosses the cap", func() {
		var members []string
		for i := range 64 {
			members = append(members, fmt.Sprintf("c%d", i))
			writeProfile(fmt.Sprintf("c%d", i), "version: 1\nbundle: []\n")
		}
		work := writeProfile("work", "version: 1\nbundle:\n  - "+strings.Join(members, "\n  - ")+"\n")
		_, err := switchTo(user, "work")
		Expect(err).To(MatchError(work + ":66: bundle: c63 takes bundle work past 64 bundles"))
		writeProfile("c63", "version: 1\n")
		Expect(switchTo(user, "work")).NotTo(BeNil(), "64 bundles is the limit, not past it")
	})

	It("expands a bundle reached twice once, noting each repeated name once, so shared members never multiply the work", func() {
		// work lists the 8 bundles of level 1; each bundle of levels 1 to 6 lists the 8 of the next
		// level, and each of level 7 the 8 leaves: 8 bundles deep, 8^8 paths, 57 bundles (within the
		// 64-bundle cap) and 65 files.
		const width, levels = 8, 7
		names := func(level int) []string {
			var out []string
			for j := range width {
				if level > levels {
					out = append(out, fmt.Sprintf("leaf%d", j))
				} else {
					out = append(out, fmt.Sprintf("d%d-%d", level, j))
				}
			}
			return out
		}
		for level := 1; level <= levels; level++ {
			for _, n := range names(level) {
				writeProfile(n, "version: 1\nbundle: ["+strings.Join(names(level+1), ", ")+"]\n")
			}
		}
		for _, n := range names(levels + 1) {
			writeProfile(n, "version: 1\n")
		}
		writeProfile("work", "version: 1\nbundle: ["+strings.Join(names(1), ", ")+"]\n")

		start := time.Now()
		res, err := switchTo(user, "work")
		took := time.Since(start)
		Expect(err).NotTo(HaveOccurred())
		Expect(took).To(BeNumerically("<", time.Second))
		var twice []string
		for _, n := range res.Notices {
			if strings.Contains(n.Text, " appears twice in bundle work") {
				twice = append(twice, strings.Fields(n.Text)[0])
			}
		}
		var repeated []string // every name past level 1 is reached by 8 bundles
		for level := 2; level <= levels+1; level++ {
			repeated = append(repeated, names(level)...)
		}
		Expect(twice).To(ConsistOf(repeated), "one notice for each repeated name: 56 of 65 profiles")
	})

	It("resolves a bare member of your bundle to your profile, never the project's, and project: members in their project only", func() {
		writeProject("review", "version: 1\n")
		work := writeProfile("work", "version: 1\nbundle:\n  - docs\n  - review\n")
		writeProfile("docs", "version: 1\n")
		_, err := switchTo(sc, "work")
		Expect(err).To(MatchError(work + ":4: bundle: review is not a profile of yours or a remote profile you have"))
		Expect(errs.Hints(err)).To(Equal([]string{"see the profiles: lola profile --project", "or take it out: lola profile --project work --edit --bundle docs"}))
		_, err = e.EditBundle(ctx, engine.BundleEditRequest{Scope: sc, Name: "work", Members: []string{"docs"}, ConfirmRemovals: yes})
		Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
		Expect(os.ReadFile(work)).To(Equal([]byte("version: 1\nbundle:\n  - docs\n")))

		writeProfile("work", "version: 1\nbundle:\n  - docs\n  - project:review\n")
		_, err = switchTo(sc, "work")
		Expect(err).NotTo(HaveOccurred())
		_, err = switchTo(user, "work")
		Expect(err).To(MatchError(work + ":4: bundle: project:review is a project's profile, which a bundle can use in its project only"))
		Expect(os.RemoveAll(filepath.Join(proj, ".lola", "profiles"))).To(Succeed())
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, ConfirmRemovals: yes})
		Expect(err).To(MatchError(work + ":4: bundle: project:review is not a profile of this project"))
	})

	It("reads a project bundle's member it cannot resolve alike whether it is missing or a profile of yours", func() {
		writeProject("review", "version: 1\n")
		team := writeProject("team", "version: 1\nbundle:\n  - review\n  - mine\n")
		for _, setup := range []func(){func() {}, func() { writeProfile("mine", "version: 1\n") }} {
			setup()
			_, err := switchTo(sc, "project:team")
			Expect(err).To(MatchError(team + ":4: bundle: mine is not a profile of this project or a remote profile you have"))
		}
	})

	It("refuses a default.yml that is a bundle: the base lists modules", func() {
		def := writeProfile("default", "version: 1\nbundle: [go]\n")
		writeProfile("go", "version: 1\n")
		_, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).To(MatchError(def + ":2: default is the base and lists modules; to combine profiles make a bundle and switch to it"))
		Expect(errs.Hints(err)).To(Equal([]string{"remove bundle: from " + def + ", then switch to a bundle of them: lola profile --user default-bundle --switch --new --bundle go"}))
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, Options: engine.Options{DryRun: true}, Display: []string{"--json"}})
		Expect(errs.Hints(err)).To(Equal([]string{"remove bundle: from " + def + ", then switch to a bundle of them: lola profile --user default-bundle --switch --new --bundle go -n --json"}),
			"the hint keeps the run's -n and output flags")
		writeProfile("default", "version: 1\nbundle:\n  - go\n")
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).To(MatchError(def+":2: default is the base and lists modules; to combine profiles make a bundle and switch to it"),
			"the line of bundle:, not of its first member")
		writeProfile("default", "version: 1\n")
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: "default-bundle", New: true,
			Bundle: []string{"go"}}, Options: engine.Options{DryRun: true}, Display: []string{"--json"}})
		Expect(err).NotTo(HaveOccurred(), "the -n hint's run works")
		Expect(filepath.Join(p.UserProfiles, "default-bundle.yml")).NotTo(BeAnExistingFile(), "and writes nothing")
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: "default-bundle", New: true,
			Bundle: []string{"go"}}})
		Expect(err).NotTo(HaveOccurred(), "the hint's run works once default.yml lists modules again")
	})
	It("asks about a pulled change of a project bundle's members as a profiles change, and leaves what it brings until it is trusted", func() {
		writeTree(proj, map[string]string{"mods/r/lola.yml": "name: r\n", "mods/r/commands/r.md": "R\n",
			"mods/l/lola.yml": "name: l\n", "mods/l/commands/l.md": "L\n"})
		writeProject("review", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: r, source: ../../mods/r}\n")
		writeProject("lint", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: l, source: ../../mods/l}\n")
		team := writeProject("team", "version: 1\nbundle: [review]\n")
		_, err := switchTo(sc, "project:team")
		Expect(err).NotTo(HaveOccurred())
		rec, err := e.Trust.Load(proj)
		Expect(err).NotTo(HaveOccurred())
		Expect(rec.View.Part("team").Bundle).To(Equal([]string{"review"}), "the member list is the bundle's trust content")

		// A pull adds a member.
		Expect(os.WriteFile(team, []byte("version: 1\nbundle: [review, lint]\n"), 0o644)).To(Succeed())
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, ConfirmRemovals: yes})
		refusal, ok := errors.AsType[*engine.TrustRefusalError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(refusal.Report.ProfileTargets).To(ConsistOf(
			engine.ProfileTargets{Profile: "project:team", FieldChange: trust.FieldChange{Field: "bundle", From: []string{"review"}, To: []string{"review", "lint"}}},
			engine.ProfileTargets{Profile: "project:lint", FieldChange: trust.FieldChange{Field: "targets", To: []string{"claude-code"}}}))

		// Trusting l's entry alone leaves it out: the bundle that brings it is not trusted.
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, ConfirmRemovals: yes,
			Trust: func(engine.TrustReport) (engine.TrustAnswer, error) {
				return engine.TrustAnswer{Modules: []string{"l"}}, nil
			}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
			Text: "left as installed until you trust them: l, r → lola sync --project"}), "r stays as installed: what the bundle brings waits on it")
		Expect(installed(sc)).NotTo(HaveKey("l"))

		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, ConfirmRemovals: yes,
			Trust: trustAll})
		Expect(err).NotTo(HaveOccurred())
		Expect(installed(sc)).To(HaveKeyWithValue("l", "profile:project:lint"))
		rec, err = e.Trust.Load(proj)
		Expect(err).NotTo(HaveOccurred())
		Expect(rec.View.Part("team").Bundle).To(Equal([]string{"review", "lint"}))
	})
	It("names the bundle on and its members, as it expands them, in the context, which writes to no file until the member question", func() {
		writeProfile("go", "version: 1\n")
		writeProfile("go-lint", "version: 1\n")
		docs := writeProfile("docs", "version: 1\n")
		writeProfile("go-all", "version: 1\nbundle: [go, go-lint]\n")
		work := writeProfile("work", "version: 1\nbundle: [go-all, docs]\n")
		_, err := switchTo(user, "work")
		Expect(err).NotTo(HaveOccurred())

		c := e.Context(user, &engine.TargetRequest{})
		Expect(c.Profile).To(Equal(&engine.ContextProfile{ContextLayer: engine.ContextLayer{Name: "user:work", Kind: "user", Path: work, Label: "work"},
			Bundle: true, Members: []engine.ContextLayer{
				{Name: "user:go", Kind: "user", Path: filepath.Join(p.UserProfiles, "go.yml"), Label: "go", Via: "go-all"},
				{Name: "user:go-lint", Kind: "user", Path: filepath.Join(p.UserProfiles, "go-lint.yml"), Label: "go-lint", Via: "go-all"},
				{Name: "user:docs", Kind: "user", Path: docs, Label: "docs"},
			}}))
		Expect(c.WritesTo).To(BeNil(), "an edit under a bundle asks which member gets it")
		Expect(e.Context(user, &engine.TargetRequest{Profile: "docs"}).WritesTo).To(Equal(&engine.ContextLayer{Name: "user:docs", Kind: "user", Path: docs, Label: "docs"}))
		Expect(e.Context(user, nil).WritesTo).To(BeNil())
	})

	It("renders a template's profiles as the precedence list: default, the bundle on, then its members", func() {
		t := filepath.Join(mods, "t")
		writeTree(t, map[string]string{"lola.yml": "name: t\n", "commands/t.md.liquid": "{{ profiles | join: ',' }}\n"})
		writeProfile("default", "version: 1\n")
		writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n"+entry("t", t))
		writeProfile("docs", "version: 1\n")
		writeProfile("work", "version: 1\nbundle: [go, docs]\n")
		_, err := switchTo(user, "work")
		Expect(err).NotTo(HaveOccurred())
		b, err := os.ReadFile(filepath.Join(p.Home, ".claude", "commands", "t.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("default,user:work,user:go,user:docs\n"))
	})

	It("qualifies a member's label and the bundle it comes through when a profile of another kind has the name", func() {
		writeProfile("x", "version: 1\n")
		writeProfile("inner", "version: 1\n")
		x := writeProject("x", "version: 1\n")
		writeProject("inner", "version: 1\nbundle: [x]\n")
		writeProject("team", "version: 1\nbundle: [inner]\n")
		_, err := switchTo(sc, "project:team")
		Expect(err).NotTo(HaveOccurred())

		c := e.Context(sc, &engine.TargetRequest{})
		Expect(c.Profile.Members).To(Equal([]engine.ContextLayer{
			{Name: "project:x", Kind: "project", Path: x, Label: "project:x", Via: "project:inner"}}))
	})

	Describe("an edit while a bundle is on", func() {
		var notes string
		// install installs arg at s, hints repeating the run as the CLI's do.
		install := func(s engine.Scope, arg string, opts engine.Options, targets ...string) (*engine.InstallResult, error) {
			if targets == nil {
				targets = []string{"claude-code"}
			}
			spec := engine.InstallSpec{Cwd: tmp, Modules: []engine.InstallRequest{{Arg: arg}},
				Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: s, Source: engine.SourceFlag}, Targets: targets}}, Options: opts}
			spec.Options.Target.Rerun = func(extra ...string) string { return e.Command(s, "install", append([]string{arg}, extra...)...) }
			return e.InstallAll(ctx, spec)
		}
		read := func(path string) string {
			GinkgoHelper()
			b, err := os.ReadFile(path)
			Expect(err).NotTo(HaveOccurred())
			return string(b)
		}
		// uninstallBase uninstalls notes, which the base gives, under the bundle on: off a terminal
		// it is refused, -y and --force answering nothing, hinting --profile base; then it is
		// removed from the base by that hint (byHint) or by answering the question.
		uninstallBase := func(why string, byHint bool) {
			GinkgoHelper()
			_, err := e.Uninstall(ctx, user, []string{"notes"}, engine.Options{Force: true})
			Expect(err).To(MatchError("notes comes from default (base); lola uninstall needs to know whether to remove it there, and there is no terminal to ask on"), why)
			Expect(errs.Hints(err)).To(Equal([]string{"lola uninstall --user notes --profile base"}), why)
			Expect(read(filepath.Join(p.UserProfiles, "default.yml"))).To(ContainSubstring("name: notes"), why)
			opts := engine.Options{Target: engine.TargetRequest{Profile: "base"}}
			var asked []engine.WhereQuestion
			if !byHint {
				opts = engine.Options{AskWhere: func(q engine.WhereQuestion) (engine.WhereChoice, error) {
					asked = append(asked, q)
					return q.Choices[0], nil
				}}
			}
			_, err = e.Uninstall(ctx, user, []string{"notes"}, opts)
			Expect(err).NotTo(HaveOccurred(), why)
			if !byHint {
				Expect(asked).To(Equal([]engine.WhereQuestion{{Scope: user, Module: "notes", Title: "notes comes from default (base), loaded under every profile at user scope.",
					Choices: []engine.WhereChoice{{Label: "Remove it from default (base)", Profile: "base"}}}}), why)
			}
			Expect(read(filepath.Join(p.UserProfiles, "default.yml"))).NotTo(ContainSubstring("name: notes"), why)
			Expect(installed(user)).NotTo(HaveKey("notes"), why)
		}
		BeforeEach(func() {
			notes = mod("notes", "notes", "N")
			writeProfile("go-review", "version: 1\n")
			writeProfile("docs", "version: 1\n")
			writeProfile("work", "version: 1\nbundle: [go-review, docs]\n")
			_, err := switchTo(user, "work")
			Expect(err).NotTo(HaveOccurred())
		})

		It("asks which member gets it, the last member first, and writes the one answered", func() {
			var asked []engine.WhereQuestion
			_, err := install(user, notes, engine.Options{AskWhere: func(q engine.WhereQuestion) (engine.WhereChoice, error) {
				asked = append(asked, q)
				return q.Choices[1], nil
			}})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(Equal([]engine.WhereQuestion{{Scope: user, Title: "work is a bundle. Which profile gets " + notes + "?",
				Choices: []engine.WhereChoice{{Label: "docs  (last member: wins overlaps)", Profile: "docs"}, {Label: "go-review", Profile: "go-review"}}}}))
			Expect(read(filepath.Join(p.UserProfiles, "go-review.yml"))).To(ContainSubstring("name: notes"))
			Expect(read(filepath.Join(p.UserProfiles, "docs.yml"))).To(Equal("version: 1\n"))
			Expect(installed(user)).To(HaveKeyWithValue("notes", "profile:user:go-review"))
		})

		It("refuses off a terminal with a --profile per member, also with one writable member, and neither -y nor --force answers it", func() {
			for _, opts := range []engine.Options{{}, {Force: true}} {
				_, err := install(user, notes, opts)
				Expect(err).To(MatchError("lola install needs to know which profile of bundle work gets " + notes + ", and there is no terminal to ask on"))
				Expect(errs.Hints(err)).To(Equal([]string{"lola install --user " + notes + " --profile docs", "lola install --user " + notes + " --profile go-review"}))
			}
			writeProfile("work", "version: 1\nbundle: [docs]\n")
			_, err := install(user, notes, engine.Options{})
			Expect(errs.Hints(err)).To(Equal([]string{"lola install --user " + notes + " --profile docs"}), "a single member is still asked")
			Expect(read(filepath.Join(p.UserProfiles, "docs.yml"))).To(Equal("version: 1\n"))
			_, err = install(user, notes, engine.Options{Target: engine.TargetRequest{Profile: "docs"}})
			Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
			Expect(installed(user)).To(HaveKeyWithValue("notes", "profile:user:docs"))
		})

		It("refuses --profile of a bundle, hinting each member it can write", func() {
			_, err := install(user, notes, engine.Options{Target: engine.TargetRequest{Profile: "work"}})
			Expect(err).To(MatchError("work is a bundle; it lists profiles, not modules"))
			Expect(errs.Hints(err)).To(Equal([]string{"lola install --user " + notes + " --profile go-review", "lola install --user " + notes + " --profile docs"}))
		})

		It("refuses --profile or --to of a bundle with no member lola can write, hinting the base, and one it cannot expand with why", func() {
			writeProfile("none", "version: 1\nbundle: []\n")
			_, err := install(user, notes, engine.Options{Target: engine.TargetRequest{Profile: "none"}})
			Expect(err).To(MatchError("none is a bundle with no profile lola can write; it lists profiles, not modules"))
			Expect(errs.Hints(err)).To(Equal([]string{"lola install --user " + notes + " --profile base"}))
			_, err = install(user, notes, engine.Options{Target: engine.TargetRequest{Profile: "base"}})
			Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
			_, err = e.Move(ctx, engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: "none", Yes: true})
			Expect(err).To(MatchError("none is a bundle with no profile lola can write; move into another profile"))
			Expect(errs.Hints(err)).To(Equal([]string{"lola profile --user --move notes --to base"}))
			_, err = e.Move(ctx, engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: "docs", Yes: true})
			Expect(err).NotTo(HaveOccurred())
			_, err = e.Move(ctx, engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: "base", Yes: true})
			Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
			Expect(installed(user)).To(HaveKeyWithValue("notes", "profile:default"))

			broken := writeProfile("broken", "version: 1\nbundle: [docs, nope]\n")
			_, err = install(user, notes, engine.Options{Target: engine.TargetRequest{Profile: "broken"}})
			Expect(err).To(MatchError(HavePrefix(broken + ":2: ")))
		})

		It("qualifies a bundle named as the target when a profile of another kind has its name", func() {
			writeProfile("team", "version: 1\n")
			writeProject("x", "version: 1\n")
			writeProject("team", "version: 1\nbundle: [x]\n")
			_, err := install(sc, notes, engine.Options{Target: engine.TargetRequest{Profile: "project:team"}})
			Expect(err).To(MatchError("project:team is a bundle; it lists profiles, not modules"))
			Expect(errs.Hints(err)).To(Equal([]string{"lola install --project " + notes + " --profile x"}))
			_, err = install(sc, notes, engine.Options{Target: engine.TargetRequest{Profile: "x"}})
			Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
		})

		It("asks where an edit of a module another member gives goes: that member, or an override in the one answered", func() {
			writeProfile("go-review", "version: 1\ntargets: [claude-code]\nmodules:\n"+entry("notes", notes))
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: user, ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred())
			var asked []engine.WhereQuestion
			_, err = install(user, notes, engine.Options{AskWhere: func(q engine.WhereQuestion) (engine.WhereChoice, error) {
				asked = append(asked, q)
				return q.Choices[0], nil
			}}, "gemini-cli")
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveLen(2))
			Expect(asked[1]).To(Equal(engine.WhereQuestion{Scope: user, Module: "notes", Title: "notes comes from profile go-review, another member of bundle work.",
				Choices: []engine.WhereChoice{{Label: "Change it in go-review  (every bundle naming go-review sees it)", Profile: "go-review"},
					{Label: "Override it in profile docs  (docs gets its own entry, starting from go-review's)", Override: true,
						Flags: []string{"--profile", "docs", "--override"}}}}))
			Expect(read(filepath.Join(p.UserProfiles, "docs.yml"))).To(Equal("version: 1\n"), "the change went to go-review")
		})

		It("uninstalls from the member that gives the module, once asked", func() {
			writeProfile("docs", "version: 1\ntargets: [claude-code]\nmodules:\n"+entry("notes", notes))
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: user, ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred())
			_, err = e.Uninstall(ctx, user, []string{"notes"}, engine.Options{})
			Expect(err).To(MatchError("notes comes from profile docs; lola uninstall needs to know whether to remove it there, and there is no terminal to ask on"))
			Expect(errs.Hints(err)).To(Equal([]string{"lola uninstall --user notes --profile user:docs"}))
			var asked []engine.WhereQuestion
			_, err = e.Uninstall(ctx, user, []string{"notes"}, engine.Options{AskWhere: func(q engine.WhereQuestion) (engine.WhereChoice, error) {
				asked = append(asked, q)
				return q.Choices[0], nil
			}})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(ConsistOf(HaveField("Title", "notes comes from profile docs, a member of bundle work.")))
			Expect(read(filepath.Join(p.UserProfiles, "docs.yml"))).NotTo(ContainSubstring("notes"))
		})

		It("refuses a move into a bundle, hinting each member", func() {
			writeProfile("docs", "version: 1\ntargets: [claude-code]\nmodules:\n"+entry("notes", notes))
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: user, ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred())
			_, err = e.Move(ctx, engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: "work", Yes: true})
			Expect(err).To(MatchError("work is a bundle; move into one of its members"))
			Expect(errs.Hints(err)).To(Equal([]string{"lola profile --user --move notes --to go-review", "lola profile --user --move notes --to docs"}))
			_, err = e.Move(ctx, engine.MoveRequest{Scope: user, Modules: []string{"notes"}, To: "go-review", Yes: true})
			Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
			Expect(installed(user)).To(HaveKeyWithValue("notes", "profile:user:go-review"))
		})

		It("asks before writing the base while a bundle on has no member lola can write: none, or remote ones only", func() {
			content := "version: 1\n"
			o, err := json.Marshal(profile.Origin{Version: profile.OriginVersion, Source: "https://example.com/profiles.git", Commit: strings.Repeat("a", 40),
				Digest: profile.Digest([]byte(content))})
			Expect(err).NotTo(HaveOccurred())
			writeTree(filepath.Join(p.RemoteProfiles, "x"), map[string]string{profile.RemoteFile: content, profile.OriginFile: string(o)})
			for _, bundle := range []string{"version: 1\nbundle: []\n", "version: 1\nbundle: [remote:x]\n"} {
				writeProfile("work", bundle)
				_, err := install(user, notes, engine.Options{Force: true})
				Expect(err).To(MatchError("bundle work has no profile lola can write; lola install needs to know whether to write to the base (default), and there is no terminal to ask on"), bundle)
				members := strings.TrimSuffix(strings.TrimPrefix(bundle, "version: 1\nbundle: ["), "]\n")
				Expect(errs.Hints(err)).To(Equal([]string{"lola install --user " + notes + " --profile base",
					"or add a profile of yours to it, made empty: lola profile --user work --edit --bundle " + strings.TrimPrefix(members+",work-extras", ",")}), bundle)
				Expect(installed(user)).NotTo(HaveKey("notes"), bundle)
				var asked []engine.WhereQuestion
				_, err = install(user, notes, engine.Options{AskWhere: func(q engine.WhereQuestion) (engine.WhereChoice, error) {
					asked = append(asked, q)
					return q.Choices[0], nil
				}})
				Expect(err).NotTo(HaveOccurred(), bundle)
				Expect(asked).To(Equal([]engine.WhereQuestion{{Scope: user, Title: "bundle work has no profile lola can write.",
					Choices: []engine.WhereChoice{{Label: "Write to the base (default)", Profile: "base"}}}}), bundle)
				Expect(read(filepath.Join(p.UserProfiles, "default.yml"))).To(ContainSubstring("name: notes"), bundle)
				uninstallBase(bundle, false)
				_, err = install(user, notes, engine.Options{Target: engine.TargetRequest{Profile: "base"}})
				Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
				Expect(read(filepath.Join(p.UserProfiles, "default.yml"))).To(ContainSubstring("name: notes"), bundle)
				uninstallBase(bundle, true)
			}
		})

		It("asks before writing the base while every member of the bundle on is left out, in a dry run that restores nothing", func() {
			pins, err := config.LoadPins(p.Pins)
			Expect(err).NotTo(HaveOccurred())
			out, err := pins.Bytes(nil, map[string]config.Remote{"x": {Source: "https://example.com/profiles.git",
				Commit: strings.Repeat("a", 40), Digest: "sha256:" + strings.Repeat("b", 64)}})
			Expect(err).NotTo(HaveOccurred())
			Expect(os.MkdirAll(filepath.Dir(p.Pins), 0o700)).To(Succeed())
			Expect(os.WriteFile(p.Pins, out, 0o600)).To(Succeed())
			writeProfile("work", "version: 1\nbundle: [remote:x]\n")
			res, err := install(user, notes, engine.Options{DryRun: true, Force: true})
			Expect(err).To(MatchError("bundle work has no profile lola can write; lola install needs to know whether to write to the base (default), and there is no terminal to ask on"))
			Expect(errs.Hints(err)).To(Equal([]string{"lola install --user " + notes + " --profile base",
				"or add a profile of yours to it, made empty: lola profile --user work --edit --bundle remote:x,work-extras"}))
			Expect(res).To(BeNil())
			var asked []engine.WhereQuestion
			_, err = install(user, notes, engine.Options{DryRun: true, AskWhere: func(q engine.WhereQuestion) (engine.WhereChoice, error) {
				asked = append(asked, q)
				return q.Choices[0], nil
			}})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(Equal([]engine.WhereQuestion{{Scope: user, Title: "bundle work has no profile lola can write.",
				Choices: []engine.WhereChoice{{Label: "Write to the base (default)", Profile: "base"}}}}))
			Expect(filepath.Join(p.RemoteProfiles, "x")).NotTo(BeADirectory(), "-n restores nothing")
		})

		It("asks before an uninstall removes a module the base gives while a bundle with members is on", func() {
			_, err := install(user, notes, engine.Options{Target: engine.TargetRequest{Profile: "base"}})
			Expect(err).NotTo(HaveOccurred())
			uninstallBase("answered", false)
			_, err = install(user, notes, engine.Options{Target: engine.TargetRequest{Profile: "base"}})
			Expect(err).NotTo(HaveOccurred())
			uninstallBase("by the hint", true)
		})

		It("names a member qualified when a profile of another kind has its name, and the hint runs as printed", func() {
			writeProfile("x", "version: 1\n")
			writeProject("x", "version: 1\n")
			writeProject("team", "version: 1\nbundle: [x]\n")
			_, err := switchTo(sc, "project:team")
			Expect(err).NotTo(HaveOccurred())
			_, err = install(sc, notes, engine.Options{})
			Expect(err).To(MatchError("lola install needs to know which profile of bundle team gets " + notes + ", and there is no terminal to ask on"))
			Expect(errs.Hints(err)).To(Equal([]string{"lola install --project " + notes + " --profile project:x"}))
			_, err = install(sc, notes, engine.Options{Target: engine.TargetRequest{Profile: "project:x"}})
			Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
			Expect(installed(sc)).To(HaveKeyWithValue("notes", "profile:project:x"))
		})
	})
	Describe("--switch --new", func() {
		newSwitch := func(s engine.Scope, ch engine.ProfileChange, opts engine.Options) (*engine.SyncResult, error) {
			ch.Action, ch.New = engine.ProfileSwitch, true
			return e.Sync(ctx, engine.SyncRequest{Scope: s, Profiles: &ch, ConfirmRemovals: yes, Trust: trustAll, Options: opts})
		}

		It("makes an empty profile of yours and switches to it, in the apply's transaction", func() {
			res, err := newSwitch(user, engine.ProfileChange{Name: "try"}, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			path := filepath.Join(p.UserProfiles, "try.yml")
			Expect(res.Created).To(Equal([]engine.Created{{Profile: "user:try", Path: path}}))
			Expect(os.ReadFile(path)).To(Equal([]byte("version: 1\n")))
			led, err := e.Ledger(user)
			Expect(err).NotTo(HaveOccurred())
			Expect(led.Profile).To(Equal(&ledger.Active{Name: "user:try", By: ledger.BySwitch}))
		})

		It("writes nothing under -n, nor when the switch is declined", func() {
			res, err := newSwitch(user, engine.ProfileChange{Name: "try"}, engine.Options{DryRun: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Created).To(HaveLen(1), "what it would make")
			Expect(filepath.Join(p.UserProfiles, "try.yml")).NotTo(BeAnExistingFile())

			a := mod("a", "a", "A")
			writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n"+entry("a", a))
			_, err = switchTo(user, "go")
			Expect(err).NotTo(HaveOccurred())
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: "work", New: true,
				Bundle: []string{"docs", "notes"}}, ConfirmRemovals: func([]engine.Removal) (bool, error) { return false, nil }})
			Expect(err).To(HaveOccurred(), "the removal of a's files was declined")
			for _, n := range []string{"work", "docs", "notes"} {
				Expect(filepath.Join(p.UserProfiles, n+".yml")).NotTo(BeAnExistingFile(), n)
			}
		})

		It("copies --from's entries and targets, never a pin, keeping the installed version", func() {
			a := mod("a", "a", "A")
			writeProfile("go", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: "+a+"}\n")
			_, err := switchTo(user, "go")
			Expect(err).NotTo(HaveOccurred())
			res, err := newSwitch(user, engine.ProfileChange{Name: "try", From: "go"}, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Created).To(Equal([]engine.Created{{Profile: "user:try", Path: filepath.Join(p.UserProfiles, "try.yml"), From: "user:go", FromLabel: "go", Modules: 1}}))
			Expect(os.ReadFile(filepath.Join(p.UserProfiles, "try.yml"))).To(Equal([]byte("version: 1\ntargets: [claude-code]\nmodules:\n  - name: a\n    source: " + a + "\n")))
			Expect(installed(user)).To(Equal(map[string]string{"a": "profile:user:try"}), "a moves to the copy and stays installed")
		})

		It("copies a project profile into yours only as a move would carry it: installed as trusted, and the hint reviews it", func() {
			for _, n := range []string{"notes", "other"} {
				work := filepath.Join(tmp, "work", n)
				writeTree(work, map[string]string{"lola.yml": "name: " + n + "\n", "commands/" + n + ".md": "one\n"})
				gitIn(work, "init", "-q", "-b", "main")
				gitIn(work, "add", "-A")
				gitIn(work, "commit", "-q", "-m", "one")
				gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", n+".git"))
			}
			srv := testenv.ServeGit(filepath.Join(tmp, "srv"))
			url := srv + "/notes.git"
			writeProject("team", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: "+url+"}\n")
			_, err := switchTo(sc, "project:team")
			Expect(err).NotTo(HaveOccurred())
			res, err := newSwitch(sc, engine.ProfileChange{Name: "try", From: "project:team"}, engine.Options{})
			Expect(err).NotTo(HaveOccurred(), "an entry the project installed as trusted, unchanged, copies")
			Expect(res.Created).To(Equal([]engine.Created{{Profile: "user:try", Path: filepath.Join(p.UserProfiles, "try.yml"), From: "project:team", FromLabel: "team", Modules: 1}}))
			Expect(os.ReadFile(filepath.Join(p.UserProfiles, "try.yml"))).To(Equal([]byte("version: 1\ntargets: [claude-code]\nmodules:\n  - name: notes\n    source: " + url + "\n")))

			_, err = switchTo(sc, "project:team")
			Expect(err).NotTo(HaveOccurred())
			writeProject("team", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: "+url+"}\n  - {name: other, source: "+srv+"/other.git}\n") // as a pull would
			_, err = newSwitch(sc, engine.ProfileChange{Name: "try2", From: "project:team"}, engine.Options{})
			Expect(err).To(MatchError("notes, other are not installed in this project as its files list them now; lola copies or moves out of a project only what it installed as you trusted it"))
			Expect(errs.Hints(err)).To(Equal([]string{"review the changes first: lola sync --project notes other"}))
			Expect(filepath.Join(p.UserProfiles, "try2.yml")).NotTo(BeAnExistingFile())
			Expect(filepath.Join(proj, ".claude", "commands", "other.md")).NotTo(BeAnExistingFile(), "nothing installs unasked")

			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"notes", "other"}, Trust: trustAll})
			Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
			_, err = newSwitch(sc, engine.ProfileChange{Name: "try2", From: "project:team"}, engine.Options{})
			Expect(err).NotTo(HaveOccurred(), "then the copy goes")
		})

		It("copies a project bundle into yours naming the same profiles: a bare member the project's, qualified when yours has the name", func() {
			writeProject("team", "version: 1\n")
			writeProject("pb", "version: 1\nbundle: [team]\n")
			writeProfile("team", "version: 1\n")
			res, err := newSwitch(sc, engine.ProfileChange{Name: "mine", From: "project:pb"}, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Created).To(Equal([]engine.Created{{Profile: "user:mine", Path: filepath.Join(p.UserProfiles, "mine.yml"), From: "project:pb", FromLabel: "pb",
				Bundle: []string{"project:team"}}}))
			Expect(os.ReadFile(filepath.Join(p.UserProfiles, "mine.yml"))).To(Equal([]byte("version: 1\nbundle: ['project:team']\n")),
				"not the empty user:team a bare team names in yours")
			Expect(e.Context(sc, nil).Profile).To(HaveField("Members", ContainElement(HaveField("Name", "project:team"))))
		})

		It("copies a bundle member bare where the bare name reads as the same profile in yours", func() {
			content := "version: 1\n"
			o, err := json.Marshal(profile.Origin{Version: profile.OriginVersion, Source: "https://example.com/profiles.git", Commit: strings.Repeat("a", 40),
				Digest: profile.Digest([]byte(content))})
			Expect(err).NotTo(HaveOccurred())
			writeTree(filepath.Join(p.RemoteProfiles, "go"), map[string]string{profile.RemoteFile: content, profile.OriginFile: string(o)})
			writeProject("pb", "version: 1\nbundle: [go]\n")
			res, err := newSwitch(sc, engine.ProfileChange{Name: "mine", From: "project:pb"}, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Created[0].Bundle).To(Equal([]string{"go"}), "remote:go, which no profile of yours shadows")
			Expect(os.ReadFile(filepath.Join(p.UserProfiles, "mine.yml"))).To(Equal([]byte("version: 1\nbundle: [go]\n")))
		})

		It("never copies a project's local folder into yours", func() {
			writeTree(proj, map[string]string{"mods/evil/lola.yml": "name: evil\n", "mods/evil/commands/evil.md": "E\n"})
			writeProject("team", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: evil, source: ../../mods/evil}\n")
			_, err := switchTo(sc, "project:team")
			Expect(err).NotTo(HaveOccurred())
			try := filepath.Join(p.UserProfiles, "try.yml")
			_, err = newSwitch(sc, engine.ProfileChange{Name: "try", From: "project:team"}, engine.Options{})
			Expect(err).To(MatchError("evil (" + try + ") installs from " + filepath.Join(proj, "mods", "evil") + ", inside project " + proj +
				": changes there install without review wherever that file is on; lola keeps a project's local modules out of your files"))
			Expect(try).NotTo(BeAnExistingFile())
			led, err := e.Ledger(sc)
			Expect(err).NotTo(HaveOccurred())
			Expect(led.Profile).To(Equal(&ledger.Active{Name: "project:team", By: ledger.BySwitch}))
		})

		It("makes a bundle, each bare member that names no profile made empty as yours", func() {
			writeProfile("go-review", "version: 1\n")
			res, err := newSwitch(user, engine.ProfileChange{Name: "work", Bundle: []string{"go-review", "docs"}}, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Created).To(Equal([]engine.Created{{Profile: "user:docs", Path: filepath.Join(p.UserProfiles, "docs.yml")},
				{Profile: "user:work", Path: filepath.Join(p.UserProfiles, "work.yml"), Bundle: []string{"go-review", "docs"}}}))
			Expect(os.ReadFile(filepath.Join(p.UserProfiles, "work.yml"))).To(Equal([]byte("version: 1\nbundle: [go-review, docs]\n")))
			Expect(os.ReadFile(filepath.Join(p.UserProfiles, "docs.yml"))).To(Equal([]byte("version: 1\n")))
			Expect(e.Context(user, nil).Profile).To(HaveField("Bundle", true))
		})

		It("refuses a name a profile has, hinting the switch to it and a free name, and a bundle naming itself", func() {
			go1 := writeProfile("go", "version: 1\n")
			_, err := newSwitch(user, engine.ProfileChange{Name: "go", Bundle: []string{"docs"}}, engine.Options{})
			Expect(err).To(MatchError("go names user:go (" + go1 + "); --new makes a profile that does not exist yet"))
			Expect(errs.Hints(err)).To(Equal([]string{"switch to it: lola profile --user user:go --switch",
				"or make another: lola profile --user go-2 --switch --new --bundle docs"}))
			_, err = newSwitch(user, engine.ProfileChange{Name: "work", Bundle: []string{"docs", "work"}}, engine.Options{})
			Expect(err).To(MatchError("--bundle: work is the bundle itself; a bundle cannot name itself"))
			for _, n := range []string{"base", "default"} {
				_, err = newSwitch(user, engine.ProfileChange{Name: n}, engine.Options{})
				Expect(err).To(HaveOccurred(), n)
			}
			Expect(filepath.Join(p.UserProfiles, "docs.yml")).NotTo(BeAnExistingFile(), "a refused --new makes nothing")
		})

		It("refuses a name a directory by its file name holds, hinting no switch to it", func() {
			dir := filepath.Join(p.UserProfiles, "dir.yml")
			Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
			_, err := newSwitch(user, engine.ProfileChange{Name: "dir"}, engine.Options{})
			Expect(err).To(MatchError("dir names user:dir (" + dir + ", a directory); --new makes a profile that does not exist yet"))
			Expect(errs.Hints(err)).To(Equal([]string{"or make another: lola profile --user dir-2 --switch --new"}))
			_, err = newSwitch(user, engine.ProfileChange{Name: "dir-2"}, engine.Options{})
			Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
		})

		It("makes a project bundle in its project, trusting the members you named, and refuses a member of yours", func() {
			writeProject("review", "version: 1\n")
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: "project:team", New: true,
				Bundle: []string{"review", "lint"}}, ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred(), "no trust question: lola wrote the bundle you named")
			Expect(res.Created).To(HaveLen(2))
			rec, err := e.Trust.Load(proj)
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.View.Part("team").Bundle).To(Equal([]string{"review", "lint"}))
			_, err = newSwitch(sc, engine.ProfileChange{Name: "project:mine", Bundle: []string{"user:me"}}, engine.Options{})
			Expect(err).To(MatchError(ContainSubstring("mine.yml:2: bundle: user:me is a profile of yours; a project's bundle names project and remote profiles only")))
			_, err = newSwitch(user, engine.ProfileChange{Name: "project:x"}, engine.Options{})
			Expect(err).To(MatchError("project:x is a project's profile; --new makes one in a project only"))
			Expect(errs.Hints(err)).To(Equal([]string{"make it yours: lola profile --user x --switch --new"}))
		})

		It("refuses a project: member that names no profile, hinting the run that makes it first, which runs as printed", func() {
			writeProject("review", "version: 1\n")
			bundle := engine.ProfileChange{Action: engine.ProfileSwitch, Name: "project:team", New: true, Bundle: []string{"review", "project:lint"}}
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &bundle, ConfirmRemovals: yes})
			Expect(err).To(MatchError("--bundle: project:lint is not a profile of this project"))
			Expect(errs.Hints(err)).To(Equal([]string{"make it first: lola profile --project project:lint --switch --new"}))
			Expect(filepath.Join(proj, ".lola", "profiles", "lint.yml")).NotTo(BeAnExistingFile(), "a shared file is never made on the side")
			Expect(filepath.Join(proj, ".lola", "profiles", "team.yml")).NotTo(BeAnExistingFile())

			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: "project:lint", New: true},
				ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &bundle, ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred(), "then the bundle is made")
			Expect(res.Created).To(Equal([]engine.Created{{Profile: "project:team", Path: filepath.Join(proj, ".lola", "profiles", "team.yml"),
				Bundle: []string{"review", "project:lint"}}}))
		})

		It("refuses a remote member you do not have, hinting yours, and names the line of a member that is a base", func() {
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: user, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: "work", New: true,
				Bundle: []string{"remote:nope"}}, Options: engine.Options{DryRun: true}, Display: []string{"--json"}})
			Expect(err).To(MatchError("--bundle: remote:nope is not a remote profile you have; lola profile --add fetches one"))
			Expect(errs.Hints(err)).To(Equal([]string{"see yours: lola profile --user --json"}), "a listing keeps --json and takes no -n")
			for _, n := range []string{"default", "base"} {
				_, err = newSwitch(user, engine.ProfileChange{Name: "work", Bundle: []string{"docs", n}}, engine.Options{})
				Expect(err).To(MatchError(HavePrefix(filepath.Join(p.UserProfiles, "work.yml")+":2: bundle: ")), n)
			}
			Expect(filepath.Join(p.UserProfiles, "docs.yml")).NotTo(BeAnExistingFile(), "a refused --new makes nothing")
		})
	})
	Describe("--split", func() {
		mixed := func() string {
			GinkgoHelper()
			n := mod("notes", "notes", "N")
			writeProfile("go", "version: 1\n")
			return writeProfile("work", "version: 1\ntargets: [claude-code]\nbundle: [go]\nmodules:\n"+entry("notes", n))
		}

		It("refuses a run that loads a mixed file, hinting --split, which runs as printed", func() {
			work := mixed()
			led, err := e.Ledger(user)
			Expect(err).NotTo(HaveOccurred())
			led.Profile = &ledger.Active{Name: "user:work", By: ledger.ByOn}
			Expect(led.Save(p.UserLedger)).To(Succeed())
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: user})
			offer, ok := errors.AsType[*engine.SplitOfferError](err)
			Expect(ok).To(BeTrue(), "%v", err)
			Expect(offer.Name).To(Equal("work"))
			Expect(err).To(MatchError(work + ":3: work has both bundle: and modules:; a bundle combines profiles and a profile lists modules"))
			Expect(errs.Hints(err)).To(Equal([]string{"move its modules into a profile of their own: lola profile --user work --split"}))

			_, err = e.Split(ctx, engine.SplitRequest{Scope: user, Name: "work"})
			Expect(err).To(MatchError("lola profile --split rewrites " + work + " and makes " + filepath.Join(p.UserProfiles, "work-modules.yml") +
				", and there is no terminal to ask on"))
			Expect(errs.Hints(err)).To(Equal([]string{"split it: lola profile --user work --split -y"}))

			res, err := e.Split(ctx, engine.SplitRequest{Scope: user, Name: "work", Yes: true, ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Plan).To(Equal(engine.SplitPlan{
				Bundle: engine.SplitFile{Name: "user:work", Path: work, Members: []string{"go", "work-modules"}, Targets: []string{}, Modules: []string{}},
				Profile: engine.SplitFile{Name: "user:work-modules", Path: filepath.Join(p.UserProfiles, "work-modules.yml"), Members: []string{},
					Targets: []string{"claude-code"}, Modules: []string{"notes"}}}))
			Expect(os.ReadFile(work)).To(Equal([]byte("version: 1\nbundle: [go, work-modules]\n")))
			Expect(res.Sync).NotTo(BeNil(), "work is on: the split applies the scope")
			Expect(installed(user)).To(Equal(map[string]string{"notes": "profile:user:work-modules"}))
		})

		It("hints --split from every run that reads the mixed file while it is off: an edit of it, a copy of it and a move out of it", func() {
			work := mixed()
			notes := mod("notes2", "notes2", "N")
			hint := []string{"move its modules into a profile of their own: lola profile --user work --split"}
			why := work + ":3: work has both bundle: and modules:; a bundle combines profiles and a profile lists modules"
			ch := engine.ProfileChange{Name: "try", Action: engine.ProfileSwitch, New: true, From: "work"}
			_, edit := e.InstallAll(ctx, engine.InstallSpec{Cwd: tmp, Modules: []engine.InstallRequest{{Arg: notes}},
				Scopes:  []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: user, Source: engine.SourceFlag}, Targets: []string{"claude-code"}}},
				Options: engine.Options{Target: engine.TargetRequest{Profile: "work"}}})
			_, copied := e.Sync(ctx, engine.SyncRequest{Scope: user, Profiles: &ch})
			_, moved := e.Move(ctx, engine.MoveRequest{Scope: user, Modules: []string{"notes"}, From: "work", To: "base", Yes: true})
			for what, err := range map[string]error{"edit": edit, "copy": copied, "move": moved} {
				_, ok := errors.AsType[*engine.SplitOfferError](err)
				Expect(ok).To(BeTrue(), "%s: %v", what, err)
				Expect(err).To(MatchError(why), what)
				Expect(errs.Hints(err)).To(Equal(hint), what)
			}
		})

		It("finishes a project profile's extends: edit with --split, named as the project resolves it, which runs as printed", func() {
			team := writeProject("team", "version: 1\nextends: [go]\nmodules:\n  - {name: r, source: ../../mods/r}\n")
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Name: "project:team", Action: engine.ProfileSwitch}})
			split := "lola profile --project project:team --split"
			Expect(err).To(MatchError(team + ":2: extends: is no longer a key; a bundle combines profiles now"))
			Expect(errs.Hints(err)).To(Equal([]string{"make it a bundle: in " + team + " replace extends: with bundle: [remote:go]; then move its modules: into a profile of their own: " + split}))
			Expect(os.WriteFile(team, []byte("version: 1\nbundle: [remote:go]\nmodules:\n  - {name: r, source: ../../mods/r}\n"), 0o644)).To(Succeed())
			_, err = e.Split(ctx, engine.SplitRequest{Scope: sc, Name: "project:team", Yes: true})
			Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
			Expect(os.ReadFile(team)).To(Equal([]byte("version: 1\nbundle: ['remote:go', team-modules]\n")))
		})

		It("names the profile qualified where another kind has its name, gives the new one a name nothing else has, and the hints run as printed", func() {
			n := mod("notes", "notes", "N")
			writeProfile("go", "version: 1\n")
			work := writeProfile("work", "version: 1\ntargets: [claude-code]\nmodules:\n"+entry("notes", n))
			writeProject("work", "version: 1\n")
			writeProject("work-modules", "version: 1\n")
			pins, err := config.LoadPins(p.Pins)
			Expect(err).NotTo(HaveOccurred())
			out, err := pins.Bytes(nil, map[string]config.Remote{"work-modules-2": {Source: "https://example.com/profiles.git",
				Commit: strings.Repeat("a", 40), Digest: "sha256:" + strings.Repeat("b", 64)}})
			Expect(err).NotTo(HaveOccurred())
			Expect(os.MkdirAll(filepath.Dir(p.Pins), 0o700)).To(Succeed())
			Expect(os.WriteFile(p.Pins, out, 0o600)).To(Succeed())
			_, err = switchTo(sc, "user:work")
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(work, []byte("version: 1\ntargets: [claude-code]\nbundle: [go]\nmodules:\n"+entry("notes", n)), 0o600)).To(Succeed())

			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc})
			offer, ok := errors.AsType[*engine.SplitOfferError](err)
			Expect(ok).To(BeTrue(), "%v", err)
			Expect(offer.Name).To(Equal("user:work"), "a bare work is ambiguous beside project:work")
			Expect(errs.Hints(err)).To(Equal([]string{"move its modules into a profile of their own: lola profile --project user:work --split"}))
			res, err := e.Split(ctx, engine.SplitRequest{Scope: sc, Name: "user:work", Yes: true, ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
			Expect(res.Plan.Profile.Name).To(Equal("user:work-modules-3"), "project:work-modules has -modules, and pins.yaml records remote:work-modules-2")
			Expect(os.ReadFile(work)).To(Equal([]byte("version: 1\nbundle: [go, work-modules-3]\n")))
			Expect(installed(sc)).To(Equal(map[string]string{"notes": "profile:user:work-modules-3"}))
		})

		It("refuses --to naming a profile, hinting a free name, which runs as printed", func() {
			work := mixed()
			_, err := e.Split(ctx, engine.SplitRequest{Scope: user, Name: "work", To: "go", Yes: true})
			Expect(err).To(MatchError("go names a profile already; --to names the new one"))
			Expect(errs.Hints(err)).To(Equal([]string{"name another: lola profile --user work --split --to go-2"}))
			_, err = e.Split(ctx, engine.SplitRequest{Scope: user, Name: "work", To: "go-2", Yes: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(os.ReadFile(work)).To(Equal([]byte("version: 1\nbundle: [go, go-2]\n")))

			long := strings.Repeat("l", 64)
			writeProfile(long, "version: 1\n")
			writeProfile("work", "version: 1\nbundle: [go]\nmodules:\n"+entry("notes", mod("notes", "notes", "N")))
			_, err = e.Split(ctx, engine.SplitRequest{Scope: user, Name: "work", To: long, Yes: true})
			Expect(errs.Hints(err)).To(Equal([]string{"name another: lola profile --user work --split --to " + long[:60]}), "a name of 64 characters at most")
			_, err = e.Split(ctx, engine.SplitRequest{Scope: user, Name: "work", To: long[:60], Yes: true})
			Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
		})

		It("names the new profile modules when <name>-modules would pass a profile name's 64 characters", func() {
			long := strings.Repeat("w", 58)
			writeProfile("go", "version: 1\n")
			path := writeProfile(long, "version: 1\nbundle: [go]\nmodules:\n"+entry("notes", mod("notes", "notes", "N")))
			res, err := e.Split(ctx, engine.SplitRequest{Scope: user, Name: long, Yes: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Plan.Profile.Name).To(Equal("user:modules"))
			Expect(os.ReadFile(path)).To(Equal([]byte("version: 1\nbundle: [go, modules]\n")))
		})

		It("leaves both files as they were when either changes before the write", func() {
			work := mixed()
			made := filepath.Join(p.UserProfiles, "work-modules.yml")
			before, err := os.ReadFile(work)
			Expect(err).NotTo(HaveOccurred())
			_, err = e.Split(ctx, engine.SplitRequest{Scope: user, Name: "work", Confirm: func(engine.SplitPlan) (bool, error) {
				return true, os.WriteFile(made, []byte("version: 1\n# mine\n"), 0o600)
			}})
			Expect(err).To(MatchError(made + " changed while lola was reading it; nothing was written"))
			Expect(os.ReadFile(work)).To(Equal(before))
			Expect(os.ReadFile(made)).To(Equal([]byte("version: 1\n# mine\n")), "a file made meanwhile is never overwritten")
			Expect(os.Remove(made)).To(Succeed())

			_, err = e.Split(ctx, engine.SplitRequest{Scope: user, Name: "work", Confirm: func(engine.SplitPlan) (bool, error) {
				return true, os.WriteFile(work, append(before, "# edited\n"...), 0o600)
			}})
			Expect(err).To(MatchError(work + " changed while lola was reading it; nothing was written"))
			Expect(os.ReadFile(work)).To(Equal(append(before, "# edited\n"...)))
			Expect(made).NotTo(BeAnExistingFile())
		})

		It("writes nothing under -n, nor when declined, and splits a profile that is off without applying", func() {
			work := mixed()
			before, err := os.ReadFile(work)
			Expect(err).NotTo(HaveOccurred())
			res, err := e.Split(ctx, engine.SplitRequest{Scope: user, Name: "work", Options: engine.Options{DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.DryRun).To(BeTrue())
			_, err = e.Split(ctx, engine.SplitRequest{Scope: user, Name: "work", Confirm: func(engine.SplitPlan) (bool, error) { return false, nil }})
			Expect(err).To(MatchError(engine.ErrDeclined))
			Expect(os.ReadFile(work)).To(Equal(before))
			Expect(filepath.Join(p.UserProfiles, "work-modules.yml")).NotTo(BeAnExistingFile())

			res, err = e.Split(ctx, engine.SplitRequest{Scope: user, Name: "work", To: "notes-kit", Options: engine.Options{Force: true}})
			Expect(err).NotTo(HaveOccurred(), "--force answers it: it rewrites a file")
			Expect(res.Sync).To(BeNil(), "work is off: nothing to apply")
			Expect(os.ReadFile(work)).To(Equal([]byte("version: 1\nbundle: [go, notes-kit]\n")))
			Expect(installed(user)).To(BeEmpty())
		})

		It("refuses the base, a remote profile and a file that is not mixed", func() {
			writeProfile("go", "version: 1\n")
			for _, n := range []string{"base", "default"} {
				_, err := e.Split(ctx, engine.SplitRequest{Scope: user, Name: n, Yes: true})
				Expect(err).To(MatchError("the base lists modules and bundles nothing; --split splits a profile that does both"), n)
			}
			_, err := e.Split(ctx, engine.SplitRequest{Scope: user, Name: "go", Yes: true})
			Expect(err).To(MatchError(filepath.Join(p.UserProfiles, "go.yml") + " has not both bundle: and modules:; there is nothing to split"))
			content := "version: 1\n"
			o, err := json.Marshal(profile.Origin{Version: profile.OriginVersion, Source: "https://example.com/profiles.git", Commit: strings.Repeat("a", 40),
				Digest: profile.Digest([]byte(content))})
			Expect(err).NotTo(HaveOccurred())
			writeTree(filepath.Join(p.RemoteProfiles, "team"), map[string]string{profile.RemoteFile: content, profile.OriginFile: string(o)})
			_, err = e.Split(ctx, engine.SplitRequest{Scope: user, Name: "team", Yes: true})
			Expect(err).To(MatchError("remote:team is read-only; lola does not edit remote profiles, and none has a bundle: key"))
			Expect(errs.Hints(err)).To(Equal([]string{"switch to a copy of yours, which you can edit: lola profile --user team-2 --switch --new --from remote:team"}))
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, ConfirmRemovals: yes,
				Profiles: &engine.ProfileChange{Name: "team-2", Action: engine.ProfileSwitch, New: true, From: "remote:team"}})
			Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
			Expect(filepath.Join(p.UserProfiles, "team-2.yml")).To(BeARegularFile())
		})

		It("refuses a default.yml that bundles beside its modules, hinting the switch that keeps its members and the profile on", func() {
			writeProfile("go", "version: 1\n")
			writeProfile("mine", "version: 1\n")
			_, err := switchTo(user, "user:mine")
			Expect(err).NotTo(HaveOccurred())
			notes := entry("notes", mod("notes", "notes", "N"))
			def := writeProfile("default", "version: 1\ntargets: [claude-code]\nbundle: [go]\nmodules:\n"+notes)
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: user})
			_, offered := errors.AsType[*engine.SplitOfferError](err)
			Expect(offered).To(BeFalse(), "no split makes the base a bundle")
			Expect(err).To(MatchError(def + ":3: default has both bundle: and modules:; a bundle combines profiles and a profile lists modules"))
			Expect(errs.Hints(err)).To(Equal([]string{"remove bundle: from " + def + ", then switch to a bundle of them and mine, the profile on now, " +
				"which the switch keeps: lola profile --user default-bundle --switch --new --bundle go,mine"}))
			writeProfile("default", "version: 1\ntargets: [claude-code]\nmodules:\n"+notes)
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, ConfirmRemovals: yes,
				Profiles: &engine.ProfileChange{Name: "default-bundle", Action: engine.ProfileSwitch, New: true, Bundle: []string{"go", "mine"}}})
			Expect(err).NotTo(HaveOccurred(), "the hint runs as printed")
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: user})
			Expect(err).NotTo(HaveOccurred())
			Expect(installed(user)).To(Equal(map[string]string{"notes": "profile:default"}), "the base keeps its modules")
			led, err := e.Ledger(user)
			Expect(err).NotTo(HaveOccurred())
			Expect(led.Profile.Name).To(Equal("user:default-bundle"))
		})

		It("applies the split of a member of the bundle on, as the expansion reaches it", func() {
			work := mixed()
			writeProfile("all", "version: 1\nbundle: [work]\n")
			led, err := e.Ledger(user)
			Expect(err).NotTo(HaveOccurred())
			led.Profile = &ledger.Active{Name: "user:all", By: ledger.ByOn}
			Expect(led.Save(p.UserLedger)).To(Succeed())
			res, err := e.Split(ctx, engine.SplitRequest{Scope: user, Name: "work", Yes: true, ConfirmRemovals: yes})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Sync).NotTo(BeNil(), "work is on through all")
			Expect(os.ReadFile(work)).To(Equal([]byte("version: 1\nbundle: [go, work-modules]\n")))
			Expect(installed(user)).To(Equal(map[string]string{"notes": "profile:user:work-modules"}))
		})

		It("keeps the mode of a profile of yours it rewrites, and writes through its link", func() {
			writeProfile("go", "version: 1\n")
			dots := filepath.Join(tmp, "dots")
			writeTree(dots, map[string]string{"work.yml": "version: 1\nbundle: [go]\nmodules:\n" + entry("notes", mod("notes", "notes", "N"))})
			Expect(os.Chmod(filepath.Join(dots, "work.yml"), 0o640)).To(Succeed())
			work := filepath.Join(p.UserProfiles, "work.yml")
			Expect(os.Symlink(filepath.Join(dots, "work.yml"), work)).To(Succeed())
			_, err := e.Split(ctx, engine.SplitRequest{Scope: user, Name: "work", Yes: true})
			Expect(err).NotTo(HaveOccurred())
			fi, err := os.Lstat(work)
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode()&fs.ModeSymlink).NotTo(BeZero(), "a stow-style link stays a link")
			Expect(os.ReadFile(filepath.Join(dots, "work.yml"))).To(Equal([]byte("version: 1\nbundle: [go, work-modules]\n")))
			fi, err = os.Stat(filepath.Join(dots, "work.yml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o640)), "files of yours keep their mode (12.2a)")
			fi, err = os.Stat(filepath.Join(p.UserProfiles, "work-modules.yml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)), "a file lola makes is private to you")
		})

		It("splits a project profile that is off while the project bundle on has a pulled member change, judging only what it writes", func() {
			writeTree(proj, map[string]string{"mods/r/lola.yml": "name: r\n", "mods/r/commands/r.md": "R\n"})
			writeProject("lint", "version: 1\n")
			writeProject("extra", "version: 1\n")
			team := writeProject("team", "version: 1\nbundle: [lint]\n")
			_, err := switchTo(sc, "project:team")
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(team, []byte("version: 1\nbundle: [lint, extra]\n"), 0o644)).To(Succeed()) // a pull adds a member
			other := writeProject("other", "version: 1\nbundle: [lint]\nmodules:\n  - {name: r, source: ../../mods/r}\n")
			res, err := e.Split(ctx, engine.SplitRequest{Scope: sc, Name: "project:other", Yes: true})
			Expect(err).NotTo(HaveOccurred(), "a split that applies nothing is not asked about team's members")
			Expect(res.Sync).To(BeNil(), "other is off: nothing to apply")
			Expect(os.ReadFile(other)).To(Equal([]byte("version: 1\nbundle: [lint, other-modules]\n")))
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc})
			refusal, ok := errors.AsType[*engine.TrustRefusalError](err)
			Expect(ok).To(BeTrue(), "the next run that applies still asks about team's pulled member: %v", err)
			Expect(refusal.Report.ProfileTargets).To(ContainElement(engine.ProfileTargets{Profile: "project:team",
				FieldChange: trust.FieldChange{Field: "bundle", From: []string{"lint"}, To: []string{"lint", "extra"}}}))
		})

		It("records a project profile's split as trusted only as far as the record covered it", func() {
			writeTree(proj, map[string]string{"mods/r/lola.yml": "name: r\n", "mods/r/commands/r.md": "R\n"})
			team := writeProject("team", "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: r, source: ../../mods/r}\n")
			writeProject("lint", "version: 1\n")
			_, err := switchTo(sc, "project:team")
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(team, []byte("version: 1\ntargets: [claude-code]\nbundle: [lint]\nmodules:\n  - {name: r, source: ../../mods/r}\n"), 0o644)).To(Succeed())
			_, err = e.Split(ctx, engine.SplitRequest{Scope: sc, Name: "project:team", Yes: true, ConfirmRemovals: yes})
			refusal, ok := errors.AsType[*engine.TrustRefusalError](err)
			Expect(ok).To(BeTrue(), "the pulled bundle: is asked about; -y is no trust: %v", err)
			Expect(err.Error()).To(HavePrefix("lola profile needs your trust"), "the run is lola profile --split")
			Expect(refusal.Report.ProfileTargets).To(ContainElement(engine.ProfileTargets{Profile: "project:team",
				FieldChange: trust.FieldChange{Field: "bundle", To: []string{"lint", "team-modules"}}}))
			Expect(refusal.Report.Changes).NotTo(ContainElement(HaveField("Module", "r")), "r moves with the trust it had")
			Expect(os.ReadFile(team)).To(ContainSubstring("modules:"), "a refused run writes nothing")
			Expect(errs.Hints(err)).To(Equal([]string{"see them: lola profile --project project:team --split -n",
				"then accept them: lola profile --project project:team --split --trust=profiles -y"}), "lola sync cannot load the file: the hints split")

			res, err := e.Split(ctx, engine.SplitRequest{Scope: sc, Name: "project:team", Options: engine.Options{DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Sync).NotTo(BeNil(), "-n shows what the apply would ask about")
			Expect(res.Sync.Trust.ProfileTargets).To(ContainElement(engine.ProfileTargets{Profile: "project:team",
				FieldChange: trust.FieldChange{Field: "bundle", To: []string{"lint", "team-modules"}}}))
			Expect(os.ReadFile(team)).To(ContainSubstring("modules:"), "-n writes nothing")

			_, err = e.Split(ctx, engine.SplitRequest{Scope: sc, Name: "project:team", Yes: true, ConfirmRemovals: yes,
				Options: engine.Options{Trust: engine.TrustProfiles}})
			Expect(err).NotTo(HaveOccurred())
			Expect(os.ReadFile(team)).To(Equal([]byte("version: 1\nbundle: [lint, team-modules]\n")))
			Expect(installed(sc)).To(Equal(map[string]string{"r": "profile:project:team-modules"}))
		})
	})
})
