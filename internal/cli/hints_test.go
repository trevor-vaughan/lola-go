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

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// Plan 8 §9.2: a hint that repeats or runs another command keeps the run's output and scope
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
			Expect(got).To(Equal(hints(proj)))
		},
		Entry("sync's trust refusal under --json",
			func(proj string) []string { return []string{"sync", "-C", proj, "--json"} },
			func(proj string) []string {
				return []string{"see them: lola sync -C " + proj + " --json -n", "then accept them: lola sync -C " + proj + " --trust=entries --json"}
			}),
		Entry("sync's trust refusal under --plain and --no-pager, with -y, which answers only the refusal that accepts",
			func(proj string) []string {
				return []string{"sync", "other", "-C", proj, "--plain", "--no-pager", "-y"}
			},
			func(proj string) []string {
				return []string{"see them: lola sync -C " + proj + " other --plain --no-pager -n",
					"then accept them: lola sync -C " + proj + " other --trust=entries --plain --no-pager -y"}
			}),
		Entry("sync's trust refusal of named modules under --update and --json",
			func(proj string) []string { return []string{"sync", "other", "-C", proj, "--update", "--json"} },
			func(proj string) []string {
				return []string{"see them: lola sync -C " + proj + " other --update --json -n",
					"then accept them: lola sync -C " + proj + " other --update --trust=entries --json"}
			}),
		Entry("sync's trust refusal after --force and --trust=hooks, which it repeats, adding only the kinds missing",
			func(proj string) []string {
				return []string{"sync", "-C", proj, "--force", "--trust=hooks", "--json"}
			},
			func(proj string) []string {
				return []string{"see them: lola sync -C " + proj + " --force --trust=hooks --json -n",
					"then accept them: lola sync -C " + proj + " --force --trust=entries,hooks --json"}
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
				return []string{"see them: lola sync -C " + proj + " kit --plain -n", "then accept them: lola sync -C " + proj + " kit --trust=entries --plain",
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
		Entry("uninstall of a module an active profile lists, under --json with -y, which it does not carry to another question",
			func(proj string) []string {
				writeTree(filepath.Join(proj, "mods", "rev"), map[string]string{"lola.yml": "name: rev\n", "commands/rev.md": "Rev\n"})
				writeTree(filepath.Join(proj, ".lola", "profiles"), map[string]string{"review.yml": "version: 1\nmodules:\n  - name: rev\n    source: ../../mods/rev\n"})
				r := lola("profile", "review", "--on", "-C", proj, "--trust", "-y")
				Expect(r.code).To(Equal(0), r.err)
				return []string{"uninstall", "rev", "-C", proj, "-y", "--json"}
			},
			func(proj string) []string {
				return []string{"turn the profile off: lola profile -C " + proj + " project:review --off --json"}
			}),
		Entry("check's argument that is neither a source nor a module in your cache, under --json",
			func(proj string) []string { return []string{"check", "nope", "-C", proj, "--json"} },
			func(string) []string {
				return []string{"cached modules: lola cache list --json"}
			}),
		Entry("cache info of a module not in your cache, under --json",
			func(string) []string { return []string{"cache", "info", "nope", "--json"} },
			func(string) []string { return []string{"cached modules: lola cache list --json"} }),
		Entry("cache rm of a module not in your cache, under --plain",
			func(string) []string { return []string{"cache", "rm", "nope", "-y", "--plain"} },
			func(string) []string { return []string{"cached modules: lola cache list --plain"} }),
		Entry("cache update of a module not in your cache, under --json",
			func(string) []string { return []string{"cache", "update", "nope", "--json"} },
			func(string) []string { return []string{"cached modules: lola cache list --json"} }),
		Entry("cache add of a module whose name your cache holds from another source, under --json",
			func(proj string) []string {
				writeTree(filepath.Join(proj, "mods", "kit2"), map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Another kit\n"})
				return []string{"cache", "add", filepath.Join(proj, "mods", "kit2"), "--json"}
			},
			func(string) []string { return []string{"remove the cached one first: lola cache rm kit --json"} }),
		Entry("install's template refusal under --json, whose check repeats the assistants given",
			func(proj string) []string {
				writeTree(filepath.Join(proj, "mods", "bad"), map[string]string{"lola.yml": "name: bad\nvars:\n  x: \"a\\nb\"\n",
					"commands/c.md.liquid": "---\ndescription: {{ vars.x }}\n---\nBody\n"})
				return []string{"install", filepath.Join(proj, "mods", "bad"), "-C", proj, "-t", "claude-code", "--json"}
			},
			func(proj string) []string {
				return []string{"lola check -C " + proj + " " + filepath.Join(proj, "mods", "bad") + " --target claude-code --json"}
			}),
		Entry("render's refusal of a tracked lock.json, for a user outside the project",
			func(proj string) []string {
				for _, args := range [][]string{{"init", "-q"}, {"add", "-f", ".lola/lock.json"}} {
					out, err := exec.Command("git", append([]string{"-C", proj}, args...)...).CombinedOutput()
					Expect(err).NotTo(HaveOccurred(), string(out))
				}
				return []string{"render", "-C", proj, "--json"}
			},
			func(proj string) []string { return []string{"git -C " + proj + " rm --cached .lola/lock.json"} }),
	)
})
