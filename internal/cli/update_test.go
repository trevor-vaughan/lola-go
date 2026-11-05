package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("lola update", func() {
	var tmp, proj, work, otherBare, served string
	// push commits files to the served example repository's main.
	push := func(files map[string]string) {
		GinkgoHelper()
		writeTree(work, files)
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "change")
		gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "example.git"), "main")
	}
	read := func(rel string) string {
		GinkgoHelper()
		b, err := os.ReadFile(filepath.Join(proj, rel))
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		// Two served git repositories, example and other, each a module on main.
		for _, name := range []string{"example", "other"} {
			w := filepath.Join(tmp, "work", name)
			writeTree(w, map[string]string{"lola.yml": "name: " + name + "\n", "commands/" + name + ".md": name + " v1\n"})
			gitIn(w, "init", "-q", "-b", "main")
			gitIn(w, "add", "-A")
			gitIn(w, "commit", "-q", "-m", "init")
			gitIn(tmp, "clone", "-q", "--bare", w, filepath.Join(tmp, "srv", name+".git"))
		}
		work, otherBare = filepath.Join(tmp, "work", "example"), filepath.Join(tmp, "srv", "other.git")
		served = testenv.ServeGit(filepath.Join(tmp, "srv"))
		for _, name := range []string{"example", "other"} {
			r := lola("install", served+"/"+name+".git", "--ref", "main", "-t", "claude-code", "-C", proj)
			Expect(r.code).To(Equal(0), r.err)
		}
	})

	It("moves an installed module to its source's new version", func() {
		push(map[string]string{"commands/example.md": "example v2\n"})
		r := lola("update", "--fetch", "--trust", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`(?m)^  → example: sha256:[0-9a-f]{64} → sha256:[0-9a-f]{64}$`), "the module moved, from → to")
		Expect(r.out).To(ContainSubstring("✓ updated example (claude-code)"))
		Expect(r.out).To(ContainSubstring("✓ 1 unchanged"))
		Expect(r.out).NotTo(ContainSubstring("synced"))
		Expect(r.out + r.err).NotTo(ContainSubstring("--update"))
		Expect(read(".claude/commands/example.md")).To(Equal("example v2\n"))
	})

	It("moves an installed module to a force-pushed head that drops its commit", func() {
		writeTree(work, map[string]string{"commands/example.md": "example rewritten\n"})
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "--amend", "-m", "rewritten")
		gitIn(work, "push", "-q", "--force", filepath.Join(tmp, "srv", "example.git"), "main")
		r := lola("update", "--fetch", "--trust", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✓ updated example (claude-code)"))
		Expect(read(".claude/commands/example.md")).To(Equal("example rewritten\n"))
	})

	It("coaches a re-pin when a force-push removed the commit a teammate's manifest pins", func() {
		pinned, err := os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		mate := filepath.Join(tmp, "mate")
		writeTree(mate, map[string]string{".lola/project.yml": string(pinned)})
		Expect(os.MkdirAll(filepath.Join(mate, ".claude"), 0o755)).To(Succeed())
		Expect(os.RemoveAll(filepath.Join(tmp, "cache"))).To(Succeed(), "the teammate's cache never held the pinned commit")
		writeTree(work, map[string]string{"commands/example.md": "example rewritten\n"})
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "--amend", "-m", "rewritten")
		bare := filepath.Join(tmp, "srv", "example.git")
		gitIn(work, "push", "-q", "--force", bare, "main")
		gitIn(bare, "reflog", "expire", "--expire=now", "--all")
		gitIn(bare, "gc", "-q", "--prune=now")

		r := lola("sync", "--trust", "-C", mate)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(MatchRegexp(`error: example: commit [0-9a-f]{40} is not on main at ` + served + `/example\.git\n`))
		Expect(r.err).To(ContainSubstring("hint: re-pin to what main serves: lola sync -C " + mate + " --update example\n"))
		Expect(filepath.Join(mate, ".claude/commands/example.md")).NotTo(BeAnExistingFile())

		r = lola("sync", "-C", mate, "--update", "example", "--trust")
		Expect(r.code).To(Equal(0), r.err)
		b, err := os.ReadFile(filepath.Join(mate, ".claude/commands/example.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("example rewritten\n"))
	})

	It("serves a ref fetched within cache_fresh_for from the cache without --fetch", func() {
		push(map[string]string{"commands/example.md": "example v2\n"})
		// Off a terminal and without --trust: a fresh head that is the version installed asks nothing.
		r := lola("update", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(".claude/commands/example.md")).To(Equal("example v1\n"), "the install just fetched main: its head is fresh")
		Expect(r.out).To(Equal("→ 2 modules served from your cache (fetched within cache_fresh_for) → lola update -C " + proj + " --fetch\n" +
			"✓ already up to date\n"))
		r = lola("update", "example", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("→ 1 module served from your cache (fetched within cache_fresh_for) → lola update -C " + proj + " example --fetch\n" +
			"✓ example already up to date\n"))
		r = lola("update", "--json", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed(), r.out)
		Expect(doc["notices"]).To(ContainElement(map[string]any{"level": "info", "class": "safety",
			"text": "2 modules served from your cache (fetched within cache_fresh_for) → lola update -C " + proj + " --fetch --json"}))
	})

	It("still asks about a fresh head another run moved, without --trust refusing it", func() {
		push(map[string]string{"commands/example.md": "example v2\n"})
		teammate := filepath.Join(tmp, "teammate")
		Expect(os.MkdirAll(filepath.Join(teammate, ".claude"), 0o755)).To(Succeed())
		r := lola("install", served+"/example.git", "--ref", "main", "-t", "claude-code", "-C", teammate)
		Expect(r.code).To(Equal(0), r.err) // moves the cache head for main
		r = lola("update", "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("~ example       re-pin from main\n"))
		Expect(r.err).To(ContainSubstring("error: lola update needs your trust"))
		Expect(read(".claude/commands/example.md")).To(Equal("example v1\n"))
		r = lola("update", "--trust", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(".claude/commands/example.md")).To(Equal("example v2\n"))
	})

	It("never takes --force as consent to a new version", func() {
		push(map[string]string{"commands/example.md": "example v2\n"})
		r := lola("update", "--fetch", "--force", "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: lola update needs your trust"))
		Expect(r.err).To(ContainSubstring("hint: then accept them: lola update -C " + proj + " --fetch --force --trust=entries"))
		Expect(r.err).NotTo(ContainSubstring("hint: see them: lola sync"))
		Expect(r.err).To(ContainSubstring("hint: see them: lola update -C " + proj + " --fetch --force -n"))
		Expect(read(".claude/commands/example.md")).To(Equal("example v1\n"))
	})

	It("names lola update, and no flag of sync, in its trust diff, refused or dry", func() {
		push(map[string]string{"commands/example.md": "example v2\n"})
		r := lola("update", "--fetch", "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring(".lola/project.yml is unchanged; lola update needs your trust for:\n"))
		Expect(r.err).To(ContainSubstring("re-pin from main (--fetch; not fetched yet)"))
		writeTree(filepath.Join(tmp, "config", "lola"), map[string]string{"config.yaml": "version: 1\ncache_fresh_for: never\n"})
		r = lola("update", "-C", proj) // every head stale: asked before it is fetched
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("re-pin from main (not fetched yet)"))
		r = lola("update", "--fetch", "-n", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(".lola/project.yml is unchanged; lola update needs your trust for:\n"))
		Expect(r.out).To(ContainSubstring("re-pin from main (--fetch)"))
		Expect(r.out + r.err).NotTo(ContainSubstring("lola sync"))
		Expect(r.out + r.err).NotTo(ContainSubstring("--update"))
	})

	It("is reachable as lola upgrade", func() {
		Expect(lola("upgrade", "--help").out).To(ContainSubstring("lola update"))
	})

	It("says in its help how to render again without fetching, how to fetch now and how often lola fetches", func() {
		out := lola("update", "--help").out
		Expect(out).To(ContainSubstring("lola sync"))
		Expect(out).To(ContainSubstring("--fetch"))
		Expect(out).To(ContainSubstring("cache_fresh_for:"))
		Expect(out).To(ContainSubstring("config.yaml"))
		Expect(out).NotTo(ContainSubstring("store"))
		Expect(out).NotTo(ContainSubstring("origin"))
		Expect(out).NotTo(ContainSubstring("(-y"), "update has no -y to answer its questions")
	})

	It("takes --refresh for --fetch", func() {
		push(map[string]string{"commands/example.md": "example v2\n"})
		r := lola("update", "--refresh", "--trust", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(".claude/commands/example.md")).To(Equal("example v2\n"))
	})

	It("has no -y and no -t", func() {
		Expect(lola("update", "-y", "-C", proj).err).To(ContainSubstring("unknown shorthand flag: 'y'"))
		Expect(lola("update", "-t", "claude-code", "-C", proj).err).To(ContainSubstring("unknown shorthand flag: 't'"))
	})

	It("exits 1 and lists the module whose fetch failed, applying the rest", func() {
		push(map[string]string{"commands/example.md": "example v2\n"})
		Expect(os.RemoveAll(otherBare)).To(Succeed())
		r := lola("update", "--fetch", "--trust", "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("could not fetch other; left as installed"))
		Expect(r.out).To(ContainSubstring("✓ updated example (claude-code)"), "the result of what was applied is printed")
		Expect(r.out).To(ContainSubstring("other: could not fetch it"))
		Expect(read(".claude/commands/example.md")).To(Equal("example v2\n"))
		Expect(read(".claude/commands/other.md")).To(Equal("other v1\n"))
	})

	It("exits 1 without saying up to date when a module's source folder is gone", func() {
		folder := filepath.Join(tmp, "folder", "local")
		writeTree(folder, map[string]string{"lola.yml": "name: local\n", "commands/local.md": "local v1\n"})
		Expect(lola("install", folder, "-t", "claude-code", "-C", proj).code).To(Equal(0))
		Expect(os.RemoveAll(folder)).To(Succeed())
		r := lola("update", "local", "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.out).To(ContainSubstring("local: source gone; not checked"))
		Expect(r.out).To(ContainSubstring("lola uninstall -C " + proj + " local"))
		Expect(r.out).NotTo(ContainSubstring("up to date"))
		Expect(r.err).To(ContainSubstring("local: source gone; not checked; left as installed"))
		Expect(read(".claude/commands/local.md")).To(Equal("local v1\n"))

		r = lola("update", "local", "--json", "-C", proj)
		Expect(r.code).To(Equal(1))
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.err), &doc)).To(Succeed(), r.err)
		Expect(doc["findings"]).To(ContainElement(And(HaveKeyWithValue("code", "source-gone"), HaveKeyWithValue("module", "local"))))
		Expect(doc).NotTo(HaveKey("gone"))
	})

	It("reports a failed fetch in --json as a fetch-failed finding beside the result, with no new key", func() {
		push(map[string]string{"commands/example.md": "example v2\n"})
		Expect(os.RemoveAll(otherBare)).To(Succeed())
		r := lola("update", "--fetch", "--trust", "--json", "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.out).To(BeEmpty())
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.err), &doc)).To(Succeed(), r.err)
		Expect(doc["error"]).To(Equal("could not fetch other; left as installed"))
		Expect(doc).NotTo(HaveKey("failed"))
		Expect(doc["findings"]).To(ContainElement(And(HaveKeyWithValue("code", "fetch-failed"), HaveKeyWithValue("module", "other"))))
		Expect(doc["pins"]).To(ContainElement(HaveKeyWithValue("module", "example")))
		Expect(doc["hints"]).To(Equal([]any{}))
	})

	It("with --scope both stops at a scope whose fetch failed, after reporting what it applied there", func() {
		push(map[string]string{"commands/example.md": "example v2\n"})
		Expect(os.RemoveAll(otherBare)).To(Succeed())
		r := lola("update", "--scope", "both", "--fetch", "--trust", "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.out).To(ContainSubstring("✓ updated example (claude-code)"))
		Expect(r.err).To(ContainSubstring("could not fetch other; left as installed; user scope was not updated"))
		Expect(read(".claude/commands/example.md")).To(Equal("example v2\n"))

		push(map[string]string{"commands/example.md": "example v3\n"})
		r = lola("update", "--scope", "both", "--fetch", "--trust", "--json", "-C", proj)
		Expect(r.code).To(Equal(1))
		var doc struct {
			Scopes []struct {
				Scope    string           `json:"scope"`
				Findings []map[string]any `json:"findings"`
			} `json:"scopes"`
		}
		Expect(json.Unmarshal([]byte(r.err), &doc)).To(Succeed(), r.err)
		Expect(doc.Scopes).To(HaveLen(1), "the project's result, applied before the failure; user scope was not reached")
		Expect(doc.Scopes[0].Scope).To(Equal("project"))
		Expect(doc.Scopes[0].Findings).To(ContainElement(And(HaveKeyWithValue("code", "fetch-failed"), HaveKeyWithValue("module", "other"))))
	})

	It("no longer knows lola render", func() {
		Expect(lola("render").err).To(ContainSubstring(`unknown command "render"`))
	})
})
