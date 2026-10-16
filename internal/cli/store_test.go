package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
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
			"setup.sh":         "#!/bin/sh\n",
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
			"  hint: still in your cache: lola cache rm example\n  hint: installed here: lola list -C " + proj + "\n"))
	})

	It("cache add shows a plugin's format and each detection warning once", func() {
		plugin := filepath.Join(tmp, "plugin")
		writeTree(plugin, map[string]string{
			".claude-plugin/plugin.json": `{"name":"deploy","lspServers":{}}`,
			".lola.yaml":                 "name: deploy\n",
			".lola.yml":                  "name: shadowed\n",
			"commands/a.md":              "A",
		})
		r := lola("cache", "add", plugin)
		Expect(r.code).To(Equal(0), r.err)
		Expect(strings.Count(r.out, ".lola.yml was ignored: .lola.yaml takes precedence")).To(Equal(1))
		Expect(strings.Count(r.out, ".claude-plugin/plugin.json was ignored: .lola.yaml takes precedence")).To(Equal(1))

		Expect(os.Remove(filepath.Join(plugin, ".lola.yml"))).To(Succeed())
		Expect(os.Remove(filepath.Join(plugin, ".lola.yaml"))).To(Succeed())
		Expect(lola("cache", "rm", "deploy", "-y").code).To(Equal(0))
		r = lola("cache", "add", plugin)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("format:   claude-plugin"))
		Expect(strings.Count(r.out, "lspServers is not supported")).To(Equal(1))
	})

	It("adds, lists, shows, updates and removes stored modules", func() {
		r := lola("cache", "add", mod)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("example 1.2.0"))
		Expect(r.out).To(ContainSubstring("⚠ example has install hooks; lola runs them only with your consent:\n    pre-install: setup.sh"))
		Expect(r.out).To(ContainSubstring("AGENTS.md in a module is not installed"))
		Expect(r.out).To(ContainSubstring("✓ cached example; install it with: lola install example"))

		r = lola("cache", "ls")
		Expect(r.out).To(MatchRegexp(`(?m)^example\s+1\.2\.0\s+1\s+` + regexpQuote(mod) + `$`))

		r = lola("cache", "show", "example", "--json")
		var info map[string]any
		Expect(json.Unmarshal([]byte(r.out), &info)).To(Succeed())
		Expect(info["format"]).To(Equal("lola"))
		Expect(info["commands"]).To(Equal([]any{"ship"}))
		Expect(info["skills"]).To(Equal([]any{}))
		Expect(info["rules"]).To(Equal([]any{}))
		Expect(info["hooks"]).To(Equal([]any{map[string]any{"event": "pre-install", "script": "setup.sh"}}))

		Expect(lola("cache", "update").out).To(ContainSubstring("→ example (default): unchanged"))
		writeTree(mod, map[string]string{"commands/ship.md": "Ship v2\n"})
		r = lola("cache", "update", "example")
		Expect(r.out).To(MatchRegexp(`✓ example \(default\): [0-9a-f]{12} → [0-9a-f]{12}\n`))
		Expect(r.out).To(ContainSubstring("installed modules keep the versions they were installed at; upgrade one with lola install <module>"))
		Expect(r.out).NotTo(ContainSubstring("lola render"))

		r = lola("cache", "rm", "example")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("will remove from your cache: example"))
		Expect(r.err).NotTo(ContainSubstring("stop working"))
		Expect(r.err).To(ContainSubstring("hint: pass -y to confirm"))
		r = lola("cache", "delete", "example", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(lola("cache", "list").out).To(ContainSubstring("your cache is empty"))
	})

	It("cache info warns about a manifest that normalization ignored", func() {
		writeTree(mod, map[string]string{".lola.yml": "name: other\n"})
		Expect(lola("cache", "add", mod).code).To(Equal(0))
		r := lola("cache", "info", "example", "--json")
		var info map[string]any
		Expect(json.Unmarshal([]byte(r.out), &info)).To(Succeed())
		Expect(info["warnings"]).To(ContainElement(".lola.yml was ignored: lola.yml takes precedence"))
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

	It("shows a manifest's description, version and keywords without their control characters", func() {
		plugin := filepath.Join(tmp, "meta")
		writeTree(plugin, map[string]string{
			".claude-plugin/plugin.json": `{"name":"meta","description":"tidy\u001b[2J\u0007desc","version":"1.0\u001b]0;x\u0007",` +
				`"keywords":["k\u009bw","tab\tbed"]}`,
			"commands/a.md": "A",
		})
		r := lola("cache", "add", plugin)
		Expect(r.code).To(Equal(0), r.err)
		for _, out := range []string{r.out, lola("cache", "show", "meta").out, lola("cache", "show", "meta", "--plain").out,
			lola("search", "tidy").out, lola("search", "tidy", "--plain").out, lola("search", "tidy", "--json").out} {
			Expect(out).NotTo(ContainSubstring("\x1b"))
			Expect(out).NotTo(ContainSubstring("\a"))
		}
		Expect(lola("cache", "show", "meta").out).To(ContainSubstring("meta 1.0?]0;x?\n  tidy?[2J?desc\n"))
		var info map[string]any
		Expect(json.Unmarshal([]byte(lola("cache", "show", "meta", "--json").out), &info)).To(Succeed())
		Expect(info["description"]).To(Equal("tidy?[2J?desc"))
		Expect(info["version"]).To(Equal("1.0?]0;x?"))
		var found map[string][]map[string]any
		Expect(json.Unmarshal([]byte(lola("search", "tidy", "--json").out), &found)).To(Succeed())
		Expect(found["results"]).To(HaveLen(1))
		Expect(found["results"][0]["keywords"]).To(Equal([]any{"k?w", "tab?bed"}))
		Expect(found["results"][0]["description"]).To(Equal("tidy?[2J?desc"))
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
		writeTree(filepath.Join(tmp, "hidden"), map[string]string{".lola.yml": "name: hidden\n"})
		Expect(lola("init", "hidden").err).To(ContainSubstring("already a module (it has .lola.yml)"))
	})

	It("init suggests an install command with the directory quoted for the shell", func() {
		dir := filepath.Join(tmp, "it's here", "fresh")
		r := lola("init", dir)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("try it: " + errs.ShellJoin([]string{"lola", "install", dir, "-t", "claude-code"})))
		Expect(r.out).NotTo(ContainSubstring("lola install " + dir))
	})

	It("init writes nothing when any scaffold path already exists", func() {
		dir := filepath.Join(tmp, "partial")
		writeTree(dir, map[string]string{"skills/hello/SKILL.md": "mine"})
		r := lola("init", dir)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("skills/hello/SKILL.md already exists; lola init never overwrites files"))
		Expect(filepath.Join(dir, "plugin.json")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(dir, "dev.getlola")).NotTo(BeADirectory())
	})

	It("init scaffolds an Agent Plugins package by default and the native layout with --format lola", func() {
		GinkgoT().Chdir(tmp)
		r := lola("init", "plugged", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var v map[string]any
		Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed())
		Expect(v["format"]).To(Equal("agent-plugins"))
		Expect(v["files"]).To(Equal([]any{"plugin.json", "skills/hello/SKILL.md", "dev.getlola/commands/hello.md"}))
		r = lola("init", "native_one", "--format", "lola")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✓ created module native_one in native_one (lola)"))
		Expect(filepath.Join(tmp, "native_one", "lola.yml")).To(BeARegularFile())
		r = lola("init", "other", "--format", "zip")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring(`lola init cannot scaffold format "zip"`))
		Expect(r.err).To(ContainSubstring("hint: use --format agent-plugins or --format lola"))
		r = lola("init", "under_score")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("hint: or pass --format lola, whose names may also hold '_'"))
	})

	It("a module scaffolded by lola init installs as is", func() {
		GinkgoT().Chdir(tmp)
		r := lola("init", "fresh")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✓ created module fresh in fresh"))
		Expect(lola("install", "./fresh", "-C", proj, "-t", "claude-code").code).To(Equal(0))
		Expect(filepath.Join(proj, ".claude/skills/hello/SKILL.md")).To(BeARegularFile())
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
			Expect(lola("install", "example", "-t", "claude-code", "-C", proj, "--allow-hooks").code).To(Equal(0))
			vers, err := filepath.Glob(filepath.Join(tmp, "cache", "lola", "modules", "example", "versions", "*", "content", "lola.yml"))
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

	Describe("with a cache index lola refuses", func() {
		var entry, index, id string
		BeforeEach(func() {
			Expect(lola("cache", "add", mod).code).To(Equal(0))
			entry = filepath.Join(tmp, "cache", "lola", "modules", "example")
			index = filepath.Join(entry, "index.yml")
			vers, err := filepath.Glob(filepath.Join(entry, "versions", "*"))
			Expect(err).NotTo(HaveOccurred())
			Expect(vers).To(HaveLen(1))
			id = filepath.Base(vers[0])
		})

		DescribeTable("cache rm removes the entry",
			func(index func(id string) string) {
				Expect(os.WriteFile(filepath.Join(entry, "index.yml"), []byte(index(id)), 0o600)).To(Succeed())
				r := lola("cache", "rm", "example", "-y")
				Expect(r.code).To(Equal(0), r.err)
				Expect(r.out).To(ContainSubstring("removed example from your cache"))
				Expect(entry).NotTo(BeAnExistingFile())
			},
			Entry("whose head records a commit that is not a full SHA", func(id string) string {
				return "source: /src/example\ntype: folder\nheads:\n  - {ref: \"\", digest: " + id + ", commit: nope}\n"
			}),
			Entry("whose head digest is not a version id", func(string) string {
				return "source: /src/example\ntype: folder\nheads:\n  - {ref: \"\", digest: ../../../outside}\n"
			}),
			Entry("that is not valid YAML", func(string) string { return "heads: [\n" }),
		)

		It("cache rm of an absent module still fails", func() {
			Expect(os.WriteFile(index, []byte("heads: [\n"), 0o600)).To(Succeed())
			r := lola("cache", "rm", "nope", "-y")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(ContainSubstring("nope is not in your cache"))
			Expect(entry).To(BeADirectory())
		})

		It("follows the refusal's hint: cache rm, then install again", func() {
			Expect(os.WriteFile(index, []byte("source: "+mod+"\ntype: folder\nheads:\n  - {ref: \"\", digest: "+id+", commit: nope}\n"), 0o600)).To(Succeed())
			r := lola("install", "example", "-t", "claude-code", "-C", proj, "--allow-hooks")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(ContainSubstring(`records commit "nope"`))
			Expect(r.err).To(ContainSubstring("hint: remove it from your cache, then install it again from its source: lola cache rm example"))
			r = lola("cache", "rm", "example", "-y")
			Expect(r.code).To(Equal(0), r.err)
			r = lola("install", mod, "-t", "claude-code", "-C", proj, "--allow-hooks")
			Expect(r.code).To(Equal(0), r.err)
			Expect(filepath.Join(proj, ".claude", "commands", "ship.md")).To(BeAnExistingFile())
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
