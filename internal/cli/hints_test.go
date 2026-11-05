package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// A hint that repeats or runs another command keeps the run's output and scope
// flags, and carries a consent flag or --force only when it answers its own refusal's question.
var _ = Describe("hints", func() {
	var proj string
	BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(tmp, "proj")
		writeTree(filepath.Join(proj, "mods", "kit"), map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Kit\n"})
		writeTree(filepath.Join(proj, "mods", "other"), map[string]string{"lola.yml": "name: other\n", "commands/other.md": "Other\n"})
		r := lola("install", filepath.Join(proj, "mods", "kit"), "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		// A teammate's entry, pulled behind lola's back: sync needs trust for it.
		f, err := os.OpenFile(filepath.Join(proj, ".lola", "project.yml"), os.O_APPEND|os.O_WRONLY, 0)
		Expect(err).NotTo(HaveOccurred())
		_, err = f.WriteString("  - name: other\n    source: mods/other\n")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Close()).To(Succeed())
		GinkgoT().Chdir(filepath.Dir(proj)) // hints name -C for a user outside the project
	})

	DescribeTable("a hint that runs another command keeps --json, --plain, --no-pager and the scope flags, repeats the flags the user gave, and adds a consent flag or --force only when it answers its own refusal",
		func(args func(proj string) []string, hints func(proj string) []string) {
			run := args(proj)
			r := lola(run...)
			Expect(r.code).To(Equal(1), r.err)
			Expect(r.out).To(BeEmpty())
			var got []string
			if slices.Contains(run, "--json") {
				var doc struct{ Hints []string }
				Expect(json.Unmarshal([]byte(r.err), &doc)).To(Succeed(), r.err)
				got = doc.Hints
			} else {
				for _, line := range strings.Split(r.err, "\n") {
					if h, ok := strings.CutPrefix(line, "  hint: "); ok {
						got = append(got, h)
					}
				}
			}
			for i := range got { // a folder's --at pin is its content digest, written here as <digest>
				got[i] = regexp.MustCompile(`=sha256:[0-9a-f]{64}`).ReplaceAllString(got[i], "=sha256:<digest>")
			}
			Expect(got).To(Equal(hints(proj)))
		},
		Entry("sync's trust refusal under --json",
			func(proj string) []string { return []string{"sync", "-C", proj, "--json"} },
			func(proj string) []string {
				return []string{"see them: lola sync -C " + proj + " --json -n", "then accept them: lola sync -C " + proj + " --trust=entries --at other=sha256:<digest> --json"}
			}),
		Entry("sync's trust refusal under --plain and --no-pager, with -y, which answers only the refusal that accepts",
			func(proj string) []string {
				return []string{"sync", "other", "-C", proj, "--plain", "--no-pager", "-y"}
			},
			func(proj string) []string {
				return []string{"see them: lola sync -C " + proj + " other --plain --no-pager -n",
					"then accept them: lola sync -C " + proj + " other --trust=entries --at other=sha256:<digest> --plain --no-pager -y"}
			}),
		Entry("sync's trust refusal of named modules under --update and --json",
			func(proj string) []string { return []string{"sync", "other", "-C", proj, "--update", "--json"} },
			func(proj string) []string {
				return []string{"see them: lola sync -C " + proj + " other --update --json -n",
					"then accept them: lola sync -C " + proj + " other --update --trust=entries --at other=sha256:<digest> --json"}
			}),
		Entry("sync's trust refusal after --force and --trust=hooks, which it repeats, adding only the kinds missing",
			func(proj string) []string {
				return []string{"sync", "-C", proj, "--force", "--trust=hooks", "--json"}
			},
			func(proj string) []string {
				return []string{"see them: lola sync -C " + proj + " --force --trust=hooks --json -n",
					"then accept them: lola sync -C " + proj + " --force --trust=entries,hooks --at other=sha256:<digest> --json"}
			}),
		Entry("install's argument that is neither in the cache nor a source, under --json",
			func(proj string) []string {
				return []string{"install", "nope", "-C", proj, "-t", "claude-code", "--json"}
			},
			func(string) []string {
				return []string{"cached modules: lola cache list --json"}
			}),
		Entry("install's argument that is neither in the cache nor a source, under --plain",
			func(proj string) []string {
				return []string{"install", "nope", "-C", proj, "-t", "claude-code", "--plain"}
			},
			func(string) []string {
				return []string{"cached modules: lola cache list --plain"}
			}),
		Entry("sync's refusal of a local source outside the project, under --json",
			func(proj string) []string {
				writeTree(filepath.Join(filepath.Dir(proj), "outside", "ext"), map[string]string{"lola.yml": "name: ext\n", "commands/ext.md": "Ext\n"})
				f, err := os.OpenFile(filepath.Join(proj, ".lola", "project.yml"), os.O_APPEND|os.O_WRONLY, 0)
				Expect(err).NotTo(HaveOccurred())
				_, err = f.WriteString("  - name: ext\n    source: ../outside/ext\n")
				Expect(err).NotTo(HaveOccurred())
				Expect(f.Close()).To(Succeed())
				return []string{"sync", "-C", proj, "--json"}
			},
			func(proj string) []string {
				return []string{"install it yourself if you trust it: lola install -C " + proj + " " + filepath.Join(filepath.Dir(proj), "outside", "ext") + " --json"}
			}),
		Entry("sync's trust refusal of a folder edited since lola installed it, under --plain",
			func(proj string) []string {
				writeTree(filepath.Join(proj, "mods", "kit"), map[string]string{"commands/kit.md": "Kit, edited\n"})
				return []string{"sync", "kit", "-C", proj, "--plain"}
			},
			func(proj string) []string {
				return []string{"see them: lola sync -C " + proj + " kit --plain -n", "then accept them: lola sync -C " + proj + " kit --trust=entries --at kit=sha256:<digest> --plain",
					"edited it yourself? lola install -C " + proj + " ./proj/mods/kit --plain"}
			}),
		Entry("a profile's trust refusal as it turns on, under --json",
			func(proj string) []string {
				writeTree(filepath.Join(proj, "mods", "rev"), map[string]string{"lola.yml": "name: rev\n", "commands/rev.md": "Rev\n"})
				writeTree(filepath.Join(proj, ".lola", "profiles"), map[string]string{"review.yml": "version: 1\nmodules:\n  - name: rev\n    source: ../../mods/rev\n"})
				return []string{"profile", "review", "--on", "-C", proj, "--json"}
			},
			func(proj string) []string {
				return []string{"see them: lola profile -C " + proj + " project:review --on --json -n",
					"then accept them: lola profile -C " + proj + " project:review --on --trust=profiles --json"}
			}),
		Entry("uninstall of a module two bundle members provide, under --json with -y, which answers no where-question between two layers",
			func(proj string) []string {
				writeTree(filepath.Join(proj, "mods", "rev"), map[string]string{"lola.yml": "name: rev\n", "commands/rev.md": "Rev\n"})
				list := "version: 1\nmodules:\n  - name: rev\n    source: ../../mods/rev\n"
				writeTree(filepath.Join(proj, ".lola", "profiles"), map[string]string{"one.yml": list, "two.yml": list, "review.yml": "version: 1\nbundle: [one, two]\n"})
				r := lola("profile", "review", "--on", "-C", proj, "--trust", "-y")
				Expect(r.code).To(Equal(0), r.err)
				return []string{"uninstall", "rev", "-C", proj, "-y", "--json"}
			},
			func(proj string) []string {
				return []string{"lola uninstall rev --json --root " + proj + " --yes --profile project:two"}
			}),
		Entry("check's argument that is neither a source nor a module in your cache, under --json",
			func(proj string) []string { return []string{"check", "nope", "-C", proj, "--json"} },
			func(string) []string {
				return []string{"cached modules: lola cache list --json"}
			}),
		Entry("info of an argument that is neither a module in your cache nor a source, under --json",
			func(string) []string { return []string{"info", "nope", "--json"} },
			func(string) []string { return []string{"cached modules: lola cache list --json"} }),
		Entry("cache rm of a module not in your cache, under --plain",
			func(string) []string { return []string{"cache", "rm", "nope", "-y", "--plain"} },
			func(string) []string { return []string{"cached modules: lola cache list --plain"} }),
		Entry("cache update of a module not in your cache, under --json",
			func(string) []string { return []string{"cache", "update", "nope", "--json"} },
			func(string) []string { return []string{"cached modules: lola cache list --json"} }),
		Entry("install's template refusal under --json, whose check repeats the assistants given",
			func(proj string) []string {
				writeTree(filepath.Join(proj, "mods", "bad"), map[string]string{"lola.yml": "name: bad\nvars:\n  x: \"a\\nb\"\n",
					"commands/c.md.liquid": "---\ndescription: {{ vars.x }}\n---\nBody\n"})
				return []string{"install", filepath.Join(proj, "mods", "bad"), "-C", proj, "-t", "claude-code", "--json"}
			},
			func(proj string) []string {
				return []string{"lola check -C " + proj + " " + filepath.Join(proj, "mods", "bad") + " --target claude-code --json"}
			}),
		Entry("sync's refusal of a tracked lock.json, for a user outside the project",
			func(proj string) []string {
				for _, args := range [][]string{{"init", "-q"}, {"add", "-f", ".lola/lock.json"}} {
					out, err := exec.Command("git", append([]string{"-C", proj}, args...)...).CombinedOutput()
					Expect(err).NotTo(HaveOccurred(), string(out))
				}
				return []string{"sync", "-C", proj, "--json"}
			},
			func(proj string) []string { return []string{"git -C " + proj + " rm --cached .lola/lock.json"} }),
		Entry("list's refusal of an unknown lock.json target, whose lola repair keeps -C",
			func(proj string) []string { plantTarget(proj); return []string{"list", "-C", proj} },
			func(proj string) []string { return []string{"lola repair -C " + proj} }),
		Entry("the same refusal under --json and --no-pager, which its lola repair keeps",
			func(proj string) []string {
				plantTarget(proj)
				return []string{"list", "-C", proj, "--json", "--no-pager"}
			},
			func(proj string) []string { return []string{"lola repair -C " + proj + " --json --no-pager"} }),
	)
})

// editLock edits proj's lock.json as a hand edit would: f changes the decoded document.
func editLock(proj string, f func(doc map[string]any)) {
	GinkgoHelper()
	lp := filepath.Join(proj, ".lola", "lock.json")
	b, err := os.ReadFile(lp)
	Expect(err).NotTo(HaveOccurred())
	var doc map[string]any
	Expect(json.Unmarshal(b, &doc)).To(Succeed())
	f(doc)
	b, err = json.Marshal(doc)
	Expect(err).NotTo(HaveOccurred())
	Expect(os.WriteFile(lp, b, 0o600)).To(Succeed())
}

// plantTarget points kit's lock.json entry at a target lola does not know, with an artifact on it.
func plantTarget(proj string) {
	GinkgoHelper()
	editLock(proj, func(doc map[string]any) {
		kit := doc["modules"].(map[string]any)["kit"].(map[string]any)
		kit["targets"] = append(kit["targets"].([]any), "bogus")
		kit["artifacts"] = append(kit["artifacts"].([]any), map[string]any{"target": "bogus", "kind": "file", "path": "x.md"})
	})
}

// hintLines are the hints of a refusal's human stderr.
func hintLines(stderr string) []string {
	var got []string
	for _, line := range strings.Split(stderr, "\n") {
		if h, ok := strings.CutPrefix(line, "  hint: "); ok {
			got = append(got, h)
		}
	}
	return got
}

// A hint keeps the run's -t and -n, and -y where the hinted command asks
// what the user's -y answered.
var _ = Describe("hints keep the user's flags", func() {
	var tmp, proj string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		GinkgoT().Chdir(proj)
	})

	It("tidy -y refused for want of a scope hints each scope with -y", func() {
		r := lola("tidy", "-y")
		Expect(r.code).To(Equal(1), r.err)
		Expect(hintLines(r.err)).To(Equal([]string{"lola tidy --yes --project", "lola tidy --yes --user"}))
		r = lola("tidy", "-n", "-y")
		Expect(hintLines(r.err)).To(Equal([]string{"lola tidy --dry-run --project", "lola tidy --dry-run --user"}), "a dry run asks nothing")
	})

	It("tidy --dry-run --project refused for want of targets hints a dry run that works", func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\n", ".claude/skills/old/SKILL.md": "x\n"})
		r := lola("tidy", "--dry-run", "--project")
		Expect(r.code).To(Equal(1), r.err)
		Expect(hintLines(r.err)).To(ContainElement("lola tidy --project -t claude-code -n"))
		again := lola(shellWords("lola tidy --project -t claude-code -n")[1:]...)
		Expect(again.code).To(Equal(0), again.err)
		Expect(again.out).To(ContainSubstring("would move 1 item:"))
		Expect(filepath.Join(proj, ".claude", "skills", "old", "SKILL.md")).To(BeARegularFile(), "the hinted run stays dry")
	})

	It("tidy -n -y hints a dry run without -y, which a dry run never asks", func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\n", ".claude/skills/old/SKILL.md": "x\n"})
		r := lola("tidy", "--project", "-n", "-y")
		Expect(r.code).To(Equal(1), r.err)
		Expect(hintLines(r.err)).To(ContainElement("lola tidy --project -t claude-code -n"))
		Expect(r.err).NotTo(ContainSubstring(" -y"))
	})

	It("sync --scope both -n -y names each scope's run without -y; a run at one scope keeps it", func() {
		writeTree(filepath.Join(proj, "mods", "kit"), map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Kit\n"})
		writeTree(filepath.Join(proj, "mods", "other"), map[string]string{"lola.yml": "name: other\n", "commands/other.md": "Other\n"})
		Expect(lola("install", "./mods/kit", "--project", "-t", "claude-code").code).To(Equal(0))
		f, err := os.OpenFile(filepath.Join(proj, ".lola", "project.yml"), os.O_APPEND|os.O_WRONLY, 0)
		Expect(err).NotTo(HaveOccurred())
		_, err = f.WriteString("  - name: other\n    source: mods/other\n")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Close()).To(Succeed())

		r := lola("sync", "--scope", "both", "-n", "-y")
		Expect(r.out).To(ContainSubstring("→ to apply: lola sync --project\n"))
		Expect(r.out).To(ContainSubstring("→ off a terminal: lola sync --project --trust=entries\n"))
		Expect(r.out+r.err).NotTo(ContainSubstring(" -y"), "the -y given for both scopes answers neither alone")

		r = lola("sync", "--project", "-n", "-y")
		Expect(r.out).To(ContainSubstring("→ to apply: lola sync --project -y\n"))
		Expect(r.out).To(ContainSubstring("→ off a terminal: lola sync --project --trust=entries -y\n"))
	})

	It("a dry run's apply hint and a move hint carry -y only when you gave it", func() {
		writeTree(proj, map[string]string{".claude/skills/old/SKILL.md": "x\n"})
		writeTree(filepath.Join(proj, "mods", "kit"), map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Kit\n"})
		r := lola("tidy", "-n", "skill/old", "--project", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ lola tidy --project skill/old -t claude-code\n"))

		r = lola("install", "./mods/kit", "--project", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ keep it to yourself: lola profile --project --move kit --to mine --switch\n"))
		Expect(r.out).To(ContainSubstring("→ meant for every project? lola profile --user --move kit --to base\n"))
		Expect(r.out + r.err).NotTo(ContainSubstring(" -y"))

		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n"})
		r = lola("sync", "--project", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ to apply: lola sync --project\n"))
		Expect(r.out+r.err).NotTo(ContainSubstring(" -y"), "the removal is asked, or refused off a terminal with its own hint")
	})

	It("profile <ambiguous> --switch -y hints each profile with -y, which switches when run verbatim", func() {
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		writeTree(proj, map[string]string{".lola/profiles/mine.yml": "version: 1\n"})
		writeTree(p.UserProfiles, map[string]string{"mine.yml": "version: 1\n"})
		r := lola("profile", "mine", "--switch", "--project", "-y")
		Expect(r.code).To(Equal(1), r.err)
		yours := "lola profile --project user:mine --switch -y"
		Expect(hintLines(r.err)).To(ContainElement("yours: " + yours))
		again := lola(shellWords(yours)[1:]...)
		Expect(again.code).To(Equal(0), again.err)
		Expect(again.out + again.err).To(ContainSubstring("now: project " + proj + ": profile user:mine over project.yml"))
	})

	It("profile <no such> --project hints the switch --new that makes it, which runs as printed", func() {
		writeTree(proj, map[string]string{".lola/profiles/other.yml": "version: 1\n"})
		r := lola("profile", "foo1", "--project")
		Expect(r.code).To(Equal(1), r.err)
		Expect(hintLines(r.err)).To(HaveExactElements("make it: lola profile foo1 --switch --new --project",
			HavePrefix("profiles the project at ")))
		again := lola(shellWords("lola profile foo1 --switch --new --project")[1:]...)
		Expect(again.code).To(Equal(0), again.err)
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(p.UserProfiles, "foo1.yml")).To(BeARegularFile(), "--new makes yours")
	})

	It("profile <reserved> shows no make-it hint, which would be refused", func() {
		r := lola("profile", "project:base", "--project")
		Expect(r.code).To(Equal(1), r.err)
		Expect(r.err).NotTo(ContainSubstring("make it"))
	})

	It("--new project:docs beside user:docs says a bare docs would name two profiles, and hints both qualified runs", func() {
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		writeTree(p.UserProfiles, map[string]string{"docs.yml": "version: 1\n"})
		r := lola("profile", "project:docs", "--switch", "--new", "--project")
		Expect(r.code).To(Equal(1), r.err)
		Expect(r.err).To(ContainSubstring("--new makes a profile that does not exist yet (project:docs beside user:docs would make a bare docs name two profiles)"))
		hints := hintLines(r.err)
		Expect(hints).To(HaveExactElements("switch to it: lola profile --project user:docs --switch",
			"or make another: lola profile --project project:docs-2 --switch --new"))
		for _, h := range hints {
			again := lola(shellWords(h[strings.Index(h, "lola "):])[1:]...)
			Expect(again.code).To(Equal(0), h+": "+again.err)
		}
	})

	It("init's try-it hint names the project scope, and installs from a git project off a terminal", func() {
		out, err := exec.Command("git", "-C", proj, "init", "-q").CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		r := lola("init", "my-module")
		Expect(r.code).To(Equal(0), r.err)
		hint := "lola install my-module --project -t claude-code"
		Expect(r.out).To(ContainSubstring("→ try it: " + hint + "\n"))
		again := lola(shellWords(hint)[1:]...)
		Expect(again.code).To(Equal(0), again.err)
		Expect(filepath.Join(proj, ".claude", "skills", "hello", "SKILL.md")).To(BeARegularFile())
	})
})
