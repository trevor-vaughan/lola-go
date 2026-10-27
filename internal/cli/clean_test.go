package cli_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// Plan 9 §6.5: lola cache clean's refusals and its empty run.
var _ = Describe("lola cache clean (Plan 9 §6.5)", func() {
	var p paths.Paths
	BeforeEach(func() {
		testenv.Isolate()
		var err error
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
	})

	It("--all with --unused-for is refused", func() {
		r := lola("cache", "clean", "--all", "--unused-for", "30d", "--json")
		Expect(r.code).To(Equal(1))
		Expect(r.out).To(BeEmpty())
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.err), &doc)).To(Succeed(), r.err)
		Expect(doc).To(Equal(map[string]any{
			"error": "if any flags in the group [all unused-for] are set none of the others can be; [all unused-for] were all set",
			"hints": []any{}}))
	})

	It("with nothing to remove clean prints ✓ nothing to clean and asks nothing", func() {
		for _, args := range [][]string{{"cache", "clean"}, {"cache", "clean", "-n"}} {
			asker := &confirming{answers: []bool{true}}
			r := lolaAsking(asker, args...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(asker.questions).To(BeEmpty())
			Expect(r.out).To(Equal("✓ nothing to clean\n"))
			Expect(r.err).To(BeEmpty())
			off := lola(args...)
			Expect(off.code).To(Equal(0), off.err)
			Expect(off.out).To(Equal("✓ nothing to clean\n"), "off a terminal, with no question to refuse")
		}
		r := lola("cache", "clean", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed(), r.out)
		Expect(doc).To(Equal(map[string]any{"removed": []any{}, "kept": []any{}, "bytes_freed": 0.0, "dry_run": false, "notices": []any{}}))
	})

	DescribeTable("an invalid age is refused naming the value",
		func(config string, args []string, want string) {
			if config != "" {
				writeTree(filepath.Dir(p.Config), map[string]string{"config.yaml": config})
			}
			r := lola(append([]string{"cache", "clean"}, args...)...)
			Expect(r.code).To(Equal(1))
			Expect(r.out).To(BeEmpty())
			Expect(r.err).To(Equal("error: " + strings.Replace(want, "PATH", p.Config, 1) + "; write a whole number of days or weeks, such as 30d or 2w, or never\n"))
		},
		Entry("flag", "", []string{"--unused-for", "3x"}, `--unused-for "3x" is not an age`),
		Entry("flag given empty, which never falls back to config.yaml's age", "version: 1\ncache_unused_for: 1d\n", []string{"--unused-for="},
			`--unused-for "" is not an age`),
		Entry("config.yaml, with its line", "version: 1\npager: true\ncache_unused_for: 3x\n", nil,
			`PATH:3: cache_unused_for: "3x" is not an age`),
	)

	It("a clean that fails part way says in its JSON what it had removed (A28)", func() {
		if os.Geteuid() == 0 {
			Skip("root renames out of a read-only directory")
		}
		tmp := filepath.Dir(p.Config)
		for _, m := range []string{"a", "b"} {
			src := filepath.Join(tmp, "src", m)
			writeTree(src, map[string]string{"lola.yml": "name: " + m + "\n", "commands/hi.md": "v1\n"})
			Expect(lola("cache", "add", src).code).To(Equal(0))
			writeTree(src, map[string]string{"commands/hi.md": "v2\n"})
			Expect(lola("cache", "add", src).code).To(Equal(0))
		}
		locked := filepath.Join(p.Store, "b", "versions")
		Expect(os.Chmod(locked, 0o500)).To(Succeed())
		DeferCleanup(os.Chmod, locked, os.FileMode(0o700))
		r := lola("cache", "clean", "-y", "--json")
		Expect(r.code).To(Equal(1))
		Expect(r.out).To(BeEmpty())
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.err), &doc)).To(Succeed(), r.err)
		Expect(doc["error"]).To(HaveSuffix("; this clean had removed 1 item from your cache first"))
		removed := doc["removed"].([]any)
		Expect(removed).To(HaveLen(1))
		Expect(removed[0].(map[string]any)["module"]).To(Equal("a"))
		Expect(doc["bytes_freed"]).To(BeNumerically(">", 0))
	})

	// cacheTwice caches module a twice from one folder, so its first version is superseded, and
	// returns that version's path in the cache.
	cacheTwice := func() string {
		GinkgoHelper()
		src := filepath.Join(filepath.Dir(p.Config), "src", "a")
		writeTree(src, map[string]string{"lola.yml": "name: a\n", "commands/hi.md": "v1\n"})
		Expect(lola("cache", "add", src).code).To(Equal(0))
		ids, err := os.ReadDir(filepath.Join(p.Store, "a", "versions"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ids).To(HaveLen(1))
		writeTree(src, map[string]string{"commands/hi.md": "v2\n"})
		Expect(lola("cache", "add", src).code).To(Equal(0))
		return filepath.Join(p.Store, "a", "versions", ids[0].Name())
	}

	DescribeTable("clean prints its plan and its result as rows, with a path that is not plain text cleaned (T6-M2)",
		func(plain bool) {
			if os.Geteuid() == 0 {
				Skip("root deletes in a read-only directory")
			}
			old := cacheTwice()
			oldRel := "modules/a/versions/" + filepath.Base(old)
			writeTree(filepath.Join(p.Staging, "run-\x1b[31m"), map[string]string{"x": "0123456789"})
			writeTree(filepath.Join(p.Store, "bad"), map[string]string{"index.yml": "heads: [\n"})
			n := lola("cache", "clean", "-n", "--json")
			Expect(n.code).To(Equal(0), n.err)
			var plan struct {
				Removed []struct {
					Path  string `json:"path"`
					Bytes int64  `json:"bytes"`
				} `json:"removed"`
				BytesFreed int64 `json:"bytes_freed"`
			}
			Expect(json.Unmarshal([]byte(n.out), &plan)).To(Succeed(), n.out)
			Expect(plan.Removed).To(HaveLen(2))
			Expect(plan.Removed[0].Path).To(Equal(oldRel))
			Expect(plan.Removed[1].Path).To(Equal(".staging/run-?[31m"))
			Expect(plan.Removed[1].Bytes).To(Equal(int64(10)))
			vb := plan.Removed[0].Bytes
			Expect(vb).To(BeNumerically("<", 10240), "sizes print in bytes")
			commands := filepath.Join(old, "content", "commands")
			Expect(os.Chmod(commands, 0o500)).To(Succeed(), "a clean cannot delete what it set aside: a notice")
			DeferCleanup(func() error {
				aside, err := filepath.Glob(filepath.Join(p.Staging, "clean-*", "*", "content", "commands"))
				for _, d := range aside {
					err = errors.Join(err, os.Chmod(d, 0o700))
				}
				return err
			})
			q := regexp.QuoteMeta
			var dry, done string
			if plain {
				rows := func(act string) string {
					return fmt.Sprintf("%s\tsuperseded\t%s\t%d\n%s\tscratch\t.staging/run-?[31m\t10\nkept\tunreadable\tmodules/bad\t0\n", act, oldRel, vb, act)
				}
				dry = "^" + q(rows("remove")) + "$"
				done = "^" + q(rows("removed")) + q("notice\twarn\tcould not delete what this clean set aside in .staging/clean-") + `[0-9]+: .*; lola cache clean retries it\n$`
			} else {
				dry = "^" + q("→ kept modules/bad (unreadable)\nwill remove from your cache:\n") +
					q("    "+oldRel) + ` +superseded +` + q(fmt.Sprintf("%d B\n", vb)) +
					q("    .staging/run-?[31m") + ` +scratch +10 B\n` +
					q(fmt.Sprintf("  total: %d B\n", plan.BytesFreed)) + "$"
				done = "^" + q("→ kept modules/bad (unreadable)\n⚠ could not delete what this clean set aside in .staging/clean-") + `[0-9]+: .*; lola cache clean retries it\n` +
					q("✓ removed "+oldRel) + ` +superseded +` + q(fmt.Sprintf("%d B\n", vb)) +
					q("✓ removed .staging/run-?[31m") + ` +scratch +10 B\n` +
					q("✓ freed ") + `[0-9]+ B of your cache\n$`
			}
			args := []string{"cache", "clean"}
			if plain {
				args = append(args, "--plain")
			}
			r := lola(append(args, "-n")...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(MatchRegexp(dry))
			r = lola(append(args, "-y")...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(MatchRegexp(done))
		},
		Entry("human", false),
		Entry("--plain", true),
	)

	It("a confirmed clean whose every item changed says nothing was removed", func() {
		old := cacheTwice()
		asker := &confirming{answers: []bool{true}, meddle: func() { Expect(os.RemoveAll(old)).To(Succeed()) }}
		r := lolaAsking(asker, "cache", "clean")
		Expect(r.code).To(Equal(0), r.err)
		Expect(asker.questions).To(Equal([]string{"Remove these from your cache?"}))
		Expect(r.out).To(Equal("→ kept modules/a/versions/" + filepath.Base(old) + " (changed)\n→ nothing removed\n"))
	})

	It("help says --unused-for never turns cache_unused_for off for one run", func() {
		Expect(lola("cache", "clean", "--help").out).To(ContainSubstring("--unused-for never turns it off for one run"))
	})
})
