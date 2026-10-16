package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("store, search and init commands", func() {
	var tmp, proj, mod string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, mod = filepath.Join(tmp, "proj"), filepath.Join(tmp, "example")
		writeTree(mod, map[string]string{
			"lola.yml":         "name: example\ndescription: Code review helpers\nversion: 1.2.0\nkeywords: [git, review]\nhooks: {pre-install: setup.sh}\n",
			"commands/ship.md": "Ship\n",
			"AGENTS.md":        "legacy",
		})
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
	})

	It("says where a module is when it is not installed here", func() {
		Expect(lola("cache", "add", mod).code).To(Equal(0))
		r := lola("uninstall", "example", "-y", "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: lola uninstall: \"example\" is not installed in this project\n" +
			"  hint: still in your cache: lola cache rm example\n  hint: installed here: lola list\n"))
	})

	It("adds, lists, shows, updates and removes stored modules", func() {
		r := lola("cache", "add", mod)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("example 1.2.0"))
		Expect(r.out).To(ContainSubstring("⚠ example declares install hooks that lola does not run:"))
		Expect(r.out).To(ContainSubstring("AGENTS.md in a module is not installed"))
		Expect(r.out).To(ContainSubstring("✓ cached example; install it with: lola install example"))

		r = lola("mod", "ls")
		Expect(r.out).To(MatchRegexp(`(?m)^example\s+1\.2\.0\s+1\s+` + regexpQuote(mod) + `$`))

		r = lola("cache", "show", "example", "--json")
		var info map[string]any
		Expect(json.Unmarshal([]byte(r.out), &info)).To(Succeed())
		Expect(info["format"]).To(Equal("lola"))
		Expect(info["commands"]).To(Equal([]any{"ship"}))
		Expect(info["skills"]).To(Equal([]any{}))
		Expect(info["hooks"]).To(Equal([]any{map[string]any{"event": "pre-install", "command": "setup.sh"}}))

		Expect(lola("cache", "update").out).To(ContainSubstring("→ example (default): unchanged"))
		writeTree(mod, map[string]string{"commands/ship.md": "Ship v2\n"})
		Expect(lola("cache", "update", "example").out).To(MatchRegexp(`✓ example \(default\): [0-9a-f]{12} → [0-9a-f]{12} \(run lola render to apply\)`))

		r = lola("cache", "rm", "example")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("MCP servers that run from the cache will stop working"))
		Expect(r.err).To(ContainSubstring("hint: pass -y to confirm"))
		r = lola("cache", "delete", "example", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(lola("cache", "list").out).To(ContainSubstring("your cache is empty"))
	})

	It("refuses to remove a module that is not stored", func() {
		r := lola("cache", "rm", "nope", "-y")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("nope is not in your cache"))
	})

	It("searches the store by name, description and keyword, case-insensitively", func() {
		Expect(lola("cache", "add", mod).code).To(Equal(0))
		for _, q := range []string{"EXAM", "review helpers", "GIT"} {
			Expect(lola("search", q, "--plain").out).To(HavePrefix("example\t1.2.0\tcache\t"), q)
		}
		Expect(lola("cache", "find", "zzz").out).To(Equal("no matches\n"))
		var v map[string][]any
		Expect(json.Unmarshal([]byte(lola("find", "zzz", "--json").out), &v)).To(Succeed())
		Expect(v["results"]).To(Equal([]any{}))
	})

	It("scaffolds a module that installs, and never overwrites", func() {
		GinkgoT().Chdir(tmp)
		r := lola("init", "fresh")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✓ created module fresh in fresh"))
		Expect(lola("install", "./fresh", "-C", proj, "-t", "claude-code").code).To(Equal(0))
		Expect(filepath.Join(proj, ".claude/skills/hello/SKILL.md")).To(BeARegularFile())
		Expect(lola("init", "fresh").err).To(ContainSubstring("already a module"))
		Expect(lola("init", "Bad_Name").err).To(ContainSubstring("module name"))
	})

	It("init writes nothing when any scaffold path already exists", func() {
		dir := filepath.Join(tmp, "partial")
		writeTree(dir, map[string]string{"skills/hello/SKILL.md": "mine"})
		r := lola("init", dir)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("skills/hello/SKILL.md already exists; lola init never overwrites files"))
		Expect(filepath.Join(dir, "lola.yml")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(dir, "commands")).NotTo(BeADirectory())
	})

	It("lola mod init <dir> scaffolds a module", func() {
		GinkgoT().Chdir(tmp)
		r := lola("mod", "init", "fresh")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✓ created module fresh in fresh"))
		Expect(lola("install", "./fresh", "-C", proj, "-t", "claude-code").code).To(Equal(0))
		Expect(filepath.Join(proj, ".claude/skills/hello/SKILL.md")).To(BeARegularFile())
	})

	It("lola mod ls lists the cache", func() {
		Expect(lola("cache", "add", mod).code).To(Equal(0))
		r := lola("mod", "ls")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`(?m)^example\s+1\.2\.0\s+1\s+`))
	})

	It("bare lola cache lists the cache", func() {
		Expect(lola("cache", "add", mod).code).To(Equal(0))
		bare := lola("cache")
		Expect(bare.code).To(Equal(0), bare.err)
		Expect(bare.out).To(Equal(lola("cache", "list").out))
		Expect(bare.out).To(ContainSubstring("example"))
	})

	It("fails on unknown store subcommands with a suggestion", func() {
		r := lola("cache", "lst")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring(`hint: did you mean: lola cache list`))
	})

	It("completes subcommand aliases, including group aliases", func() {
		Expect(lola("__complete", "mo").out).To(HavePrefix("mod\tCompatibility commands: forward to lola cache and lola init\n"))
		Expect(lola("__complete", "cache", "ls").out).To(HavePrefix("ls\tList modules in your cache\n"))
		Expect(lola("__complete", "cache", "l").out).To(HavePrefix("list\t"))
	})

	It("completes stored module names and target ids", func() {
		Expect(lola("cache", "add", mod).code).To(Equal(0))
		Expect(lola("__complete", "cache", "info", "ex").out).To(HavePrefix("example\n"))
		Expect(lola("__complete", "install", "x", "-t", "").out).To(HavePrefix("claude-code\n"))
	})

	Describe("with one unloadable cache entry", func() {
		BeforeEach(func() {
			other := filepath.Join(tmp, "other")
			writeTree(other, map[string]string{"lola.yml": "name: other\ndescription: Other thing\n", "commands/o.md": "O\n"})
			Expect(lola("cache", "add", mod).code).To(Equal(0))
			Expect(lola("cache", "add", other).code).To(Equal(0))
			Expect(lola("install", "example", "-t", "claude-code", "-C", proj).code).To(Equal(0))
			vers, err := filepath.Glob(filepath.Join(tmp, "data", "lola", "modules", "example", "versions", "*", "content", "lola.yml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(vers).To(HaveLen(1))
			f, err := os.OpenFile(vers[0], os.O_APPEND|os.O_WRONLY, 0)
			Expect(err).NotTo(HaveOccurred())
			_, err = f.WriteString("bogus: 1\n")
			Expect(err).NotTo(HaveOccurred())
			Expect(f.Close()).To(Succeed())
		})

		It("cache list still lists the rest and warns", func() {
			r := lola("cache", "list")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("other"))
			Expect(r.err).To(ContainSubstring("⚠ example: "))
			r = lola("cache", "list", "--json")
			Expect(r.code).To(Equal(0), r.err)
			var got struct {
				Modules  []map[string]any `json:"modules"`
				Warnings []string         `json:"warnings"`
			}
			Expect(json.Unmarshal([]byte(r.out), &got)).To(Succeed())
			Expect(got.Modules).To(HaveLen(1))
			Expect(got.Warnings).To(ConsistOf(HavePrefix("example: ")))
		})

		It("search still finds the rest and warns", func() {
			r := lola("search", "thing")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("other"))
			Expect(r.err).To(ContainSubstring("⚠ example: "))
		})

		It("list shows the module as unreadable", func() {
			r := lola("list", "-C", proj)
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("unreadable"))
		})

		It("cache rm removes it", func() {
			r := lola("cache", "rm", "example", "-y")
			Expect(r.code).To(Equal(0), r.err)
			Expect(lola("cache", "info", "example").code).To(Equal(1))
		})

		It("cache rm of an absent module still fails", func() {
			r := lola("cache", "rm", "nope", "-y")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(ContainSubstring("nope is not in your cache"))
		})
	})
})

var _ = Describe("published alias table", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("matches exactly (aliases are additive and stable)", func() {
		table := map[string][]string{}
		for _, c := range commands() {
			if len(c.Aliases) > 0 {
				table[c.CommandPath()] = c.Aliases
			}
		}
		Expect(table).To(Equal(map[string][]string{
			"lola install":      {"add"},
			"lola uninstall":    {"remove", "rm", "erase", "delete", "del"},
			"lola list":         {"ls"},
			"lola render":       {"update"},
			"lola search":       {"find"},
			"lola cache add":    {"get"},
			"lola cache rm":     {"remove", "uninstall", "erase", "delete", "del"},
			"lola cache list":   {"ls"},
			"lola cache info":   {"show", "view"},
			"lola cache search": {"find"},
			"lola mod add":      {"get"},
			"lola mod rm":       {"remove", "uninstall", "erase", "delete", "del"},
			"lola mod list":     {"ls"},
			"lola mod info":     {"show", "view"},
			"lola mod search":   {"find"},
			"lola version":      {"ver"},
		}))
	})

	It("JSON array keys are [] in an empty environment", func() {
		proj := GinkgoT().TempDir()
		for _, args := range [][]string{
			{"list", "-C", proj}, {"render", "-C", proj}, {"cache", "list"}, {"cache", "update"},
			{"search", "x"}, {"cache", "search", "x"}, {"list", "--user"},
		} {
			r := lola(append(args, "--json")...)
			Expect(r.code).To(Equal(0), strings.Join(args, " ")+": "+r.err)
			Expect(r.out).NotTo(ContainSubstring("null"), strings.Join(args, " "))
		}
	})

	It("aliases appear in help", func() {
		Expect(lola("help", "--short").out).To(ContainSubstring("uninstall (remove, rm, erase, delete, del)"))
		Expect(lola("help", "cache", "--short").out).To(ContainSubstring("rm (remove, uninstall, erase, delete, del)"))
	})
})

func regexpQuote(s string) string {
	return strings.NewReplacer(".", `\.`, "/", `/`).Replace(s)
}
