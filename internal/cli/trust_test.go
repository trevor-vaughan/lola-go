package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

func gitIn(dir string, args ...string) string {
	GinkgoHelper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))
	return strings.TrimSpace(string(out))
}

// changing is a terminal that answers yes to the receipt, running change first: what the receipt
// showed moves before the command applies it.
type changing struct {
	*scripted
	change func()
}

func (c *changing) Confirm(question string, _ cli.Nav) (bool, error) {
	c.calls = append(c.calls, "confirm:"+question)
	c.change()
	return true, nil
}

// aborting is a terminal whose every question is answered with Ctrl+C.
type aborting struct{ *scripted }

func (a *aborting) Confirm(question string, _ cli.Nav) (bool, error) {
	a.calls = append(a.calls, "confirm:"+question)
	return false, cli.ErrAborted
}

var _ = Describe("typed --trust (Plan 9 §3)", func() {
	var tmp, proj, kit, mark string
	var p paths.Paths
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		proj, mark = filepath.Join(tmp, "proj"), filepath.Join(tmp, "hook-ran")
		kit = filepath.Join(proj, "mods", "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\nhooks: {pre-install: hooks/pre.sh}\n", "commands/kit.md": "Kit\n",
			"hooks/pre.sh": "#!/bin/sh\ntouch '" + mark + "'\n"})
		Expect(os.Chmod(filepath.Join(kit, "hooks", "pre.sh"), 0o755)).To(Succeed())
		writeTree(proj, map[string]string{"mods/plain/lola.yml": "name: plain\n", "mods/plain/commands/plain.md": "Plain\n"})
	})
	// pull writes project.yml behind lola's back, as a git pull would: its entries are untrusted.
	pull := func(modules ...string) {
		content := "version: 1\ntargets: [claude-code]\nmodules:\n"
		for _, m := range modules {
			content += "  - {name: " + m + ", source: mods/" + m + "}\n"
		}
		writeTree(proj, map[string]string{".lola/project.yml": content})
	}
	// fill puts the fixture's paths in place of PROJ and KIT, which a table's entries name.
	fill := func(s string) string { return strings.NewReplacer("PROJ", proj, "KIT", kit).Replace(s) }
	written := func() bool {
		_, err := os.Stat(filepath.Join(proj, ".claude"))
		return err == nil
	}
	consent := func() string {
		b, err := os.ReadFile(p.Consent)
		if os.IsNotExist(err) {
			return ""
		}
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}

	DescribeTable("--trust with no value means entries and profiles, and covers no hook, script or runtime hook",
		func(args []string, msg string, hints []string) {
			pull("kit")
			for i := range args {
				args[i] = fill(args[i])
			}
			r := lola(args...)
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(ContainSubstring("error: " + msg + "\n"))
			for _, h := range hints {
				Expect(r.err).To(ContainSubstring("  hint: " + fill(h) + "\n"))
			}
			Expect(mark).NotTo(BeAnExistingFile())
			Expect(consent()).To(BeEmpty())
		},
		Entry("sync: entries covered, the hook refused", []string{"sync", "-C", "PROJ", "--trust"},
			"kit has a pre-install hook, and lola runs hooks only with your consent",
			[]string{"then consent to it: lola sync -C PROJ --trust=entries,profiles,hooks"}),
		Entry("install: bare --trust grants none of its kinds", []string{"install", "KIT", "-C", "PROJ", "--trust"},
			"lola install never asks for entries or profiles trust; it takes --trust=scripts,hooks,runtime-hooks",
			[]string{"lola install KIT --root PROJ"}),
	)

	It("an unknown trust kind is refused naming every kind, hinting the closest", func() {
		r := lola("sync", "-C", proj, "--trust=hook,entries", "--json")
		Expect(r.code).To(Equal(1))
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.err), &doc)).To(Succeed(), r.err)
		Expect(doc["error"]).To(Equal(`--trust: "hook" is not a kind of trust; kinds are entries, profiles, scripts, hooks, runtime-hooks, all`))
		Expect(doc["hints"]).To(Equal([]any{"lola sync --json --root " + proj + " --trust=entries,hooks"}))
	})

	DescribeTable("--trust= is refused naming every kind",
		func(args func() []string, hints string) {
			r := lola(args()...)
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: --trust= names no kind; kinds are entries, profiles, scripts, hooks, runtime-hooks, all\n" + fill(hints)))
		},
		Entry("sync hints bare --trust", func() []string { return []string{"sync", "-C", proj, "--trust="} },
			"  hint: lola sync --root PROJ --trust=entries,profiles\n"),
		Entry("install has no hint", func() []string { return []string{"install", kit, "-C", proj, "--trust="} }, ""),
	)

	It("an unknown kind far from every kind is refused with no hint", func() {
		r := lola("sync", "-C", proj, "--trust=zzz")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: --trust: \"zzz\" is not a kind of trust; kinds are entries, profiles, scripts, hooks, runtime-hooks, all\n"))
	})

	It("a close-kind hint never suggests all", func() {
		r := lola("sync", "-C", proj, "--trust=al")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: --trust: \"al\" is not a kind of trust; kinds are entries, profiles, scripts, hooks, runtime-hooks, all\n"))
	})

	DescribeTable("a trust kind a command never asks is refused naming the kinds it takes",
		func(args func() []string, msg, hint string) {
			r := lola(args()...)
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal("error: " + msg + "\n  hint: " + fill(hint) + "\n"))
			Expect(written()).To(BeFalse())
		},
		Entry("install", func() []string { return []string{"install", kit, "-C", proj, "--trust=entries,hooks"} },
			"lola install never asks for entries trust; it takes --trust=scripts,hooks,runtime-hooks", "lola install KIT --root PROJ --trust=hooks"),
		Entry("render", func() []string { return []string{"render", "-C", proj, "--trust=profiles"} },
			"lola render never asks for profiles trust; it takes --trust=scripts,hooks,runtime-hooks", "lola render --root PROJ"),
		Entry("migrate", func() []string { return []string{"migrate", "--trust=entries,profiles,hooks", "-n"} },
			"lola migrate never asks for entries or profiles trust; it takes --trust=scripts,hooks,runtime-hooks", "lola migrate --dry-run --trust=hooks"),
		Entry("profile --add", func() []string {
			return []string{"profile", "go", "--add", "https://example.com/profiles.git", "--user", "--trust=entries"}
		}, "lola profile --add never asks for entries trust; it takes --trust=profiles", "lola profile go --add https://example.com/profiles.git --user"),
	)

	DescribeTable("--trust followed by a kind name as its own argument is refused, hinting --trust=<kind>",
		func(args func() []string, want string) {
			pull("kit")
			r := lola(args()...)
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal(fill(want)))
			Expect(mark).NotTo(BeAnExistingFile())
		},
		Entry("sync, the kinds written only", func() []string { return []string{"sync", "-C", proj, "--trust", "hooks"} },
			"error: \"hooks\" is a kind of trust, not a module: write --trust=hooks\n  hint: lola sync --root PROJ --trust=hooks\n"),
		Entry("sync, a list in another case", func() []string { return []string{"sync", "-C", proj, "--trust", "Hooks,scripts", "--trust=entries"} },
			"error: \"Hooks,scripts\" is a kind of trust, not a module: write --trust=scripts,hooks\n  hint: lola sync --root PROJ --trust=entries,scripts,hooks\n"),
		Entry("install, all, which no hint writes", func() []string { return []string{"install", "-C", proj, "--trust", "all"} },
			"error: \"all\" is a kind of trust, not a module: write --trust=scripts,hooks,runtime-hooks\n"+
				"  hint: lola install --root PROJ --trust=scripts,hooks,runtime-hooks\n"),
		Entry("install, a kind it never asks", func() []string { return []string{"install", "-C", proj, "--trust", "entries"} },
			"error: lola install never asks for entries trust; it takes --trust=scripts,hooks,runtime-hooks\n  hint: lola install --root PROJ\n"),
		Entry("profile --add, a kind it never asks", func() []string {
			return []string{"profile", "go", "--add", "https://example.com/profiles.git", "--user", "--trust", "hooks"}
		}, "error: lola profile --add never asks for hooks trust; it takes --trust=profiles\n"+
			"  hint: lola profile go --add https://example.com/profiles.git --user\n"),
		Entry("render, which takes no arguments", func() []string { return []string{"render", "-C", proj, "--trust", "hooks"} },
			"error: \"hooks\" is a kind of trust, not a module: write --trust=hooks\n  hint: lola render --root PROJ --trust=hooks\n"),
		Entry("migrate, which takes no arguments", func() []string { return []string{"migrate", "-n", "--trust", "Hooks"} },
			"error: \"Hooks\" is a kind of trust, not a module: write --trust=hooks\n  hint: lola migrate --dry-run --trust=hooks\n"),
		Entry("render, any other argument as before", func() []string { return []string{"render", "-C", proj, "--trust=hooks", "kit"} },
			"error: unknown command \"kit\" for \"lola render\"\n"),
	)

	It("an argument after -- is never --trust's value", func() {
		pull("kit")
		Expect(lola("sync", "kit", "-C", proj, "--trust", "--", "hooks").err).NotTo(ContainSubstring("is a kind of trust"))
	})

	It("help shows --trust[=entries,profiles] where bare --trust grants them, and --trust=<kinds> where it is refused", func() {
		for _, c := range []string{"sync", "profile"} {
			Expect(lola(c, "--help").out).To(ContainSubstring("--trust[=entries,profiles]"), c)
		}
		for _, c := range []string{"install", "render", "migrate"} {
			out := lola(c, "--help").out
			Expect(out).To(ContainSubstring("--trust=<kinds>"), c)
			Expect(out).NotTo(ContainSubstring("[=entries,profiles]"), c)
		}
		Expect(lola("help").out).NotTo(ContainSubstring("\x00"), "the bare value never shows")
	})

	It("profile --update without a scope flag refuses --trust=entries, naming the scope flags", func() {
		writeTree(p.RemoteProfiles, map[string]string{"go/profile.yml": "version: 1\nmodules: []\n",
			"go/source.json": `{"version":1,"source":"https://example.com/profiles.git","commit":"0000000000000000000000000000000000000000","digest":"sha256:00"}`})
		r := lola("profile", "go", "--update", "--trust=entries,hooks")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: --trust=entries trusts a project's changes, which lola profile --update answers only in a scope a flag names\n" +
			"  hint: name the project: lola profile go --trust=entries,hooks --update --project\n" +
			"  hint: or with user scope too: lola profile go --trust=entries,hooks --update --scope both\n"))
	})

	It("a failure after the grants were written carries trusted in its --json error", func() {
		writeTree(kit, map[string]string{"hooks/pre.sh": "#!/bin/sh\nexit 3\n"})
		pull("kit")
		r := lola("sync", "-C", proj, "--trust=entries,hooks", "--json")
		Expect(r.code).To(Equal(1))
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.err), &doc)).To(Succeed(), r.err)
		Expect(doc["error"]).To(ContainSubstring("pre-install hook hooks/pre.sh of kit failed"))
		Expect(doc["trusted"]).To(Equal([]any{map[string]any{"kind": "hooks", "item": "kit pre-install", "change": "new"}}))
	})

	It("sync --scope both shows one receipt for both scopes; No applies neither", func() {
		pull("plain")
		writeTree(filepath.Dir(p.UserManifest), map[string]string{filepath.Base(p.UserManifest): "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " +
			kit + "}\n"})
		c := &confirming{scripted: scripted{}, answers: []bool{false}}
		r := lolaAsking(c, "sync", "-C", proj, "--scope", "both", "--trust=entries,hooks")
		Expect(r.code).To(Equal(0), r.err)
		Expect(c.questions).To(Equal([]string{"Proceed?"}))
		Expect(r.err).To(ContainSubstring("trusting, as --trust=entries,hooks allows:\n  → entries: targets (new), plain (new)\n  → hooks: kit pre-install (new)\n"))
		Expect(r.err).To(HaveSuffix("aborted; nothing changed.\n"))
		Expect(written()).To(BeFalse())
		Expect(filepath.Join(p.Home, ".claude")).NotTo(BeADirectory())
		Expect(consent()).To(BeEmpty())

		c = &confirming{scripted: scripted{}, answers: []bool{true}}
		r = lolaAsking(c, "sync", "-C", proj, "--scope", "both", "--trust=entries,hooks")
		Expect(r.code).To(Equal(0), r.err)
		Expect(c.questions).To(Equal([]string{"Proceed?"}))
		Expect(written()).To(BeTrue())
		Expect(mark).To(BeAnExistingFile())
	})

	It("--trust=all covers exactly the kinds the command takes, and no hint ever writes all", func() {
		pull("kit")
		r := lola("sync", "-C", proj, "--trust=all", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HaveSuffix("→ to apply: lola sync -C "+proj+" --trust=entries,profiles,scripts,hooks,runtime-hooks\n"),
			"it covers every question: the command needs no other flag off a terminal")
		r = lola("install", kit, "-C", proj, "-t", "claude-code", "--trust=all", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		Expect(doc["trusted"]).To(Equal([]any{map[string]any{"kind": "hooks", "item": "kit pre-install", "change": "new"}}))
		Expect(mark).To(BeAnExistingFile())
	})

	It("covered items print one receipt grouped by kind, then one question; -y answers it; No writes nothing", func() {
		pull("kit", "plain")
		c := &confirming{scripted: scripted{}, answers: []bool{false}}
		r := lolaAsking(c, "sync", "-C", proj, "--trust=entries,hooks")
		Expect(r.code).To(Equal(0), r.err)
		Expect(c.questions).To(Equal([]string{"Proceed?"}))
		Expect(r.err).To(ContainSubstring("trusting, as --trust=entries,hooks allows:\n" +
			"  → entries: targets (new), kit (new), plain (new)\n  → hooks: kit pre-install (new)\n"))
		Expect(r.err).To(HaveSuffix("aborted; the project was not changed.\n"))
		Expect(written()).To(BeFalse())
		Expect(mark).NotTo(BeAnExistingFile())
		Expect(consent()).To(BeEmpty())

		c = &confirming{scripted: scripted{}}
		r = lolaAsking(c, "sync", "-C", proj, "--trust=entries,hooks", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(c.questions).To(BeEmpty(), "-y answers the receipt")
		Expect(r.err).To(ContainSubstring("  → hooks: kit pre-install (new)\n"))
		Expect(mark).To(BeAnExistingFile())
		Expect(consent()).To(ContainSubstring(`"kit"`))

		writeTree(kit, map[string]string{"hooks/pre.sh": "#!/bin/sh\ntouch '" + mark + "2'\n"})
		c = &confirming{scripted: scripted{}, answers: []bool{false}}
		r = lolaAsking(c, "install", kit, "-C", proj, "--trust=hooks")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(HaveSuffix("aborted; nothing changed.\n"), "No at install's receipt is an abort, not an error")
		Expect(mark + "2").NotTo(BeAnExistingFile())
	})

	It("uncovered items are asked one by one before the receipt", func() {
		pull("kit")
		c := &confirming{scripted: scripted{}, trusts: []engine.TrustAnswer{{All: true}}, answers: []bool{true}}
		r := lolaAsking(c, "sync", "-C", proj, "--trust=hooks")
		Expect(r.code).To(Equal(0), r.err)
		Expect(c.questions).To(Equal([]string{"trust", "Proceed?"}))
		Expect(c.before[1]).To(ContainSubstring("trusting, as --trust=hooks allows:\n  → hooks: kit pre-install (new)\n"))
		Expect(c.before[0]).NotTo(ContainSubstring("trusting, as"), "the receipt comes after the questions")
		Expect(mark).To(BeAnExistingFile())
	})

	It("off a terminal a covered run proceeds, the receipt on stderr, trusted in the JSON", func() {
		pull("kit")
		r := lola("sync", "-C", proj, "--trust=entries,hooks")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("trusting, as --trust=entries,hooks allows:\n  → entries: targets (new), kit (new)\n  → hooks: kit pre-install (new)\n"))
		Expect(mark).To(BeAnExistingFile())

		writeTree(kit, map[string]string{"hooks/pre.sh": "#!/bin/sh\ntouch '" + mark + "2'\n"})
		r = lola("install", kit, "-C", proj, "--trust=hooks", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		Expect(doc["trusted"]).To(Equal([]any{map[string]any{"kind": "hooks", "item": "kit pre-install", "change": "changed"}}))
	})

	It("on a terminal under --json a covered run asks nothing and prints no receipt: trusted carries it", func() {
		pull("kit")
		c := &confirming{scripted: scripted{}}
		r := lolaAsking(c, "sync", "-C", proj, "--trust=entries,hooks", "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(c.questions).To(BeEmpty())
		Expect(r.err).NotTo(ContainSubstring("trusting, as"), "stderr under --json holds only an error's JSON line")
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		Expect(doc["trusted"]).To(HaveLen(3))
	})

	It("trusted is [] when nothing was covered, and under -n lists what the flags would cover", func() {
		pull("plain")
		r := lola("sync", "-C", proj, "--trust", "--json", "-n")
		Expect(r.code).To(Equal(0), r.err)
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		Expect(doc["trusted"]).To(Equal([]any{map[string]any{"kind": "entries", "item": "targets", "change": "new"},
			map[string]any{"kind": "entries", "item": "plain", "change": "new"}}))
		Expect(filepath.Join(p.StateHome, "lola", "trust.json")).NotTo(BeAnExistingFile())
		Expect(lola("sync", "-C", proj, "--trust").code).To(Equal(0))
		r = lola("sync", "-C", proj, "--trust", "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		Expect(doc["trusted"]).To(Equal([]any{}))
	})

	DescribeTable("off a terminal an uncovered run refuses, hinting the run with exactly the missing kinds and its own flags",
		func(setup func() []string, hint string) {
			r := lola(setup()...)
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(ContainSubstring("  hint: " + fill(hint) + "\n"))
			Expect(written()).To(BeFalse())
		},
		Entry("entries", func() []string { pull("plain"); return []string{"sync", "-C", proj, "--plain"} },
			"then accept them: lola sync -C PROJ --trust=entries --plain"),
		Entry("profiles", func() []string {
			writeTree(proj, map[string]string{".lola/profiles/go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: plain, source: ../../mods/plain}\n"})
			return []string{"profile", "go", "--on", "-C", proj, "--no-pager"}
		}, "then accept them: lola profile -C PROJ project:go --on --trust=profiles --no-pager"),
		Entry("hooks", func() []string { pull("kit"); return []string{"sync", "-C", proj, "--trust=entries"} },
			"then consent to it: lola sync -C PROJ --trust=entries,hooks"),
		Entry("entries+hooks", func() []string { pull("kit"); return []string{"sync", "-C", proj} },
			"then accept them: lola sync -C PROJ --trust=entries,hooks"),
		Entry("remote --add", func() []string {
			return []string{"profile", "go", "--add", "https://example.com/profiles.git", "--user", "--plain"}
		}, "then add it unasked: lola profile --user go --add https://example.com/profiles.git --trust=profiles --plain"),
	)

	It("-y and --force grant no kind of trust", func() {
		pull("kit")
		r := lola("sync", "-C", proj, "-y", "--force")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("; -y confirms removals only; accept them with --trust=entries,hooks\n"))
		r = lola("install", kit, "--user", "-t", "claude-code", "-y", "--force")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: kit has a pre-install hook, and lola runs hooks only with your consent\n"))
		Expect(mark).NotTo(BeAnExistingFile())
		Expect(consent()).To(BeEmpty())
	})

	DescribeTable("a receipt answered No, a refusal or an abort records no consent given by --trust",
		func(pr func() cli.Prompter, args []string) {
			pull("kit")
			run := append([]string{"sync", "-C", proj}, args...)
			var r result
			if p := pr(); p != nil {
				r = lolaP(p, run...)
			} else {
				r = lola(run...)
			}
			Expect(r.code).To(BeElementOf(0, 1), r.err)
			Expect(consent()).To(BeEmpty(), r.err)
			Expect(filepath.Join(p.StateHome, "lola", "trust.json")).NotTo(BeAnExistingFile())
			Expect(written()).To(BeFalse())
			Expect(mark).NotTo(BeAnExistingFile())
		},
		Entry("No", func() cli.Prompter { return &scripted{confirm: []bool{false}} }, []string{"--trust=entries,hooks"}),
		Entry("a refusal for another kind", func() cli.Prompter { return nil }, []string{"--trust=hooks"}),
		Entry("Ctrl+C", func() cli.Prompter { return &aborting{&scripted{}} }, []string{"--trust=entries,hooks"}),
	)

	It("profile --update without a scope flag applies profiles trust only", func() {
		pull("plain")
		writeTree(p.RemoteProfiles, map[string]string{"go/profile.yml": "version: 1\nmodules: []\n",
			"go/source.json": `{"version":1,"source":"https://example.com/profiles.git","commit":"0000000000000000000000000000000000000000","digest":"sha256:00"}`})
		GinkgoT().Chdir(proj)
		r := lola("profile", "go", "--update", "--trust=all")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("has changes you have not trusted (plain and project.yml's targets), which its sync asks about, and no flag named it"))
		Expect(r.err).To(ContainSubstring("  hint: name it, and trust them: lola profile --project remote:go --update --trust=entries,profiles,scripts,hooks,runtime-hooks\n"))
	})

	Describe("sync --scope both after its one receipt (R-9-16)", func() {
		userKit := func(dir, body string) {
			writeTree(dir, map[string]string{"lola.yml": "name: " + filepath.Base(dir) + "\nhooks: {pre-install: pre.sh}\n", "commands/" + filepath.Base(dir) + ".md": "X\n",
				"pre.sh": "#!/bin/sh\n" + body + "\n"})
			Expect(os.Chmod(filepath.Join(dir, "pre.sh"), 0o755)).To(Succeed())
		}
		userList := func(entries string) {
			writeTree(filepath.Dir(p.UserManifest), map[string]string{filepath.Base(p.UserManifest): "version: 1\ntargets: [claude-code]\nmodules:\n" + entries})
		}

		It("refuses a scope whose hook digest moved between the receipt and its apply, even under the same receipt line", func() {
			dir := filepath.Join(tmp, "src", "ukit")
			userKit(dir, "true # v0")
			userList("  - {name: ukit, source: " + dir + "}\n")
			Expect(lola("sync", "--user", "--trust=hooks").code).To(Equal(0))
			userKit(dir, "true # v1, what the receipt shows as changed")
			pull("plain")
			pr := &changing{scripted: &scripted{}, change: func() { userKit(dir, "true # v2, never shown") }}
			r := lolaP(pr, "sync", "-C", proj, "--scope", "both", "--trust=entries,hooks")
			Expect(r.code).To(Equal(1), r.err)
			Expect(r.err).To(ContainSubstring("error: what --trust covers changed after you answered its receipt; nothing more was written"))
			Expect(consent()).To(ContainSubstring(`"ukit"`))
			Expect(read(filepath.Join(p.Home, ".claude", "commands", "ukit.md"))).To(Equal("X\n"), "the copy v0's sync wrote")
		})

		It("applies one module at two versions in two scopes", func() {
			work := filepath.Join(tmp, "kit-work")
			userKit(work, "true # v1")
			gitIn(work, "init", "-q", "-b", "main")
			gitIn(work, "add", "-A")
			gitIn(work, "commit", "-q", "-m", "v1")
			gitIn(work, "tag", "v1")
			userKit(work, "true # v2")
			gitIn(work, "commit", "-q", "-am", "v2")
			gitIn(work, "tag", "v2")
			gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "kit-work.git"))
			url := testenv.ServeGit(filepath.Join(tmp, "srv")) + "/kit-work.git"
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit-work, source: " + url + ", ref: v1}\n"})
			userList("  - {name: kit-work, source: " + url + ", ref: v2}\n")
			c := &confirming{scripted: scripted{}, answers: []bool{true}}
			r := lolaAsking(c, "sync", "-C", proj, "--scope", "both", "--trust=entries,hooks")
			Expect(r.code).To(Equal(0), r.err)
			Expect(c.questions).To(Equal([]string{"Proceed?"}))
			Expect(filepath.Join(proj, ".claude", "commands", "kit-work.md")).To(BeAnExistingFile())
			Expect(filepath.Join(p.Home, ".claude", "commands", "kit-work.md")).To(BeAnExistingFile())
		})

		It("a scope that fails after its grants carries every grant written so far in the error JSON", func() {
			pull("kit")
			bad := filepath.Join(tmp, "src", "bad")
			userKit(bad, "exit 3")
			userList("  - {name: bad, source: " + bad + "}\n")
			r := lola("sync", "-C", proj, "--scope", "both", "--trust=entries,hooks", "--json")
			Expect(r.code).To(Equal(1))
			var doc map[string]any
			Expect(json.Unmarshal([]byte(r.err), &doc)).To(Succeed(), r.err)
			Expect(doc["trusted"]).To(ConsistOf(map[string]any{"kind": "entries", "item": "targets", "change": "new"},
				map[string]any{"kind": "entries", "item": "kit", "change": "new"}, map[string]any{"kind": "hooks", "item": "kit pre-install", "change": "new"},
				map[string]any{"kind": "hooks", "item": "bad pre-install", "change": "new"}))
		})
	})

	It("completion shows --trust's help without back-quotes or a placeholder", func() {
		r := lola("__complete", "sync", "--tru")
		Expect(r.out).To(ContainSubstring("--trust"))
		Expect(r.out).NotTo(ContainSubstring("`"))
	})

	It("a hint built before --trust is parsed repeats it as given", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		r := lola("profile", "go", "--on", "--off", "--trust=hooks")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("keep --on: lola profile go --on --trust=hooks\n"))
	})

	It("sync, profile, install, render and migrate have --trust; uninstall, market, tidy, info and every cache command do not; no command has --allow-hooks or --allow-runtime-hooks", func() {
		with := []string{"lola sync", "lola profile", "lola install", "lola render", "lola migrate"}
		var has []string
		for _, c := range commands() {
			Expect(c.Flags().Lookup("allow-hooks")).To(BeNil(), c.CommandPath())
			Expect(c.Flags().Lookup("allow-runtime-hooks")).To(BeNil(), c.CommandPath())
			if c.Flags().Lookup("trust") != nil {
				has = append(has, c.CommandPath())
			}
		}
		slices.Sort(has)
		slices.Sort(with)
		Expect(has).To(Equal(with))
		Expect(lola("sync", "--help").out).To(ContainSubstring("--trust[=entries,profiles]"))
	})
})
