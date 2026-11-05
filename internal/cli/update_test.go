package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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
	// state is the bytes of the project, the cache, the trust record and your home (user scope).
	state := func() map[string]string {
		GinkgoHelper()
		out := map[string]string{}
		for _, dir := range []string{proj, filepath.Join(tmp, "cache"), filepath.Join(tmp, "state"), filepath.Join(tmp, "data"), filepath.Join(tmp, "home")} {
			Expect(filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
				if os.IsNotExist(err) && path == dir {
					return nil
				}
				if err != nil || d.IsDir() || strings.Contains(path, string(filepath.Separator)+"locks"+string(filepath.Separator)) {
					return err
				}
				b, err := os.ReadFile(path)
				out[path] = string(b)
				return err
			})).To(Succeed())
		}
		return out
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
		Expect(r.err).To(ContainSubstring("~ example       re-pin from main (--fetch)\n"))
		Expect(r.err).NotTo(ContainSubstring("~ other"), "other's fetch found nothing new: it needs no trust")
		writeTree(filepath.Join(tmp, "config", "lola"), map[string]string{"config.yaml": "version: 1\ncache_fresh_for: never\n"})
		r = lola("update", "-C", proj) // every head stale: fetched into the run's staging, then asked
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("~ example       re-pin from main\n"))
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
		Expect(out).To(ContainSubstring("-y, --yes"), "update confirms the files a new version no longer ships")
	})

	It("takes --refresh for --fetch", func() {
		push(map[string]string{"commands/example.md": "example v2\n"})
		r := lola("update", "--refresh", "--trust", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(".claude/commands/example.md")).To(Equal("example v2\n"))
	})

	It("has no -t", func() {
		Expect(lola("update", "-t", "claude-code", "-C", proj).err).To(ContainSubstring("unknown shorthand flag: 't'"))
	})

	It("Update-removals: files a new version no longer ships are listed, refused off a terminal without -y, and removed with it", func() {
		gitIn(work, "mv", "commands/example.md", "commands/renamed.md")
		gitIn(work, "commit", "-q", "-m", "rename")
		gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "example.git"), "main")
		gone := "removals:\n  ✗ example: claude-code (not in the version this run installs)\n      .claude/commands/example.md\n"

		r := lola("update", "--fetch", "--trust", "-C", proj, "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out + r.err).To(ContainSubstring(gone))

		r = lola("update", "--fetch", "--trust", "-C", proj)
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(ContainSubstring(gone))
		Expect(hintLines(r.err)).To(ContainElement("then confirm them: lola update -C "+proj+" --fetch --trust=entries,profiles --at example="+
			gitIn(work, "rev-parse", "HEAD")+" -y"), "the hint pins the version whose removals it lists")
		Expect(read(".claude/commands/example.md")).To(Equal("example v1\n"), "nothing was written")

		r = lola("update", "--fetch", "--trust", "-C", proj, "--json")
		Expect(r.code).To(Equal(1))
		var refused map[string]any
		Expect(json.Unmarshal([]byte(r.err), &refused)).To(Succeed(), r.err)
		Expect(refused["removals"]).To(ContainElement(HaveKeyWithValue("why", "not in the version this run installs")))

		r = lola("update", "--fetch", "--trust", "-C", proj, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(proj, ".claude/commands/example.md")).NotTo(BeAnExistingFile())
		Expect(read(".claude/commands/renamed.md")).To(Equal("example v1\n"))
	})

	It("Update-removals: --fetch off a terminal is refused once, naming the trust and the files the new version drops, and its hint runs as printed", func() {
		gitIn(work, "mv", "commands/example.md", "commands/renamed.md")
		gitIn(work, "commit", "-q", "-m", "rename")
		gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "example.git"), "main")
		before := state()

		r := lola("update", "--fetch", "-C", proj)
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(ContainSubstring("error: lola update needs your trust for the pins it would write for example; " +
			"and your confirmation to remove the files example's new version drops, and there is no terminal to ask on"))
		Expect(r.err).To(ContainSubstring("re-pin from main (--fetch)\n"), "the new version was fetched, so the refusal shows it")
		Expect(state()).To(Equal(before), "a refusal leaves the cache, the trust record and the project as they were")
		accept := ""
		for _, h := range hintLines(r.err) {
			if c, ok := strings.CutPrefix(h, "then accept them: "); ok {
				accept = c
			}
		}
		// The hint pins each version the refusal showed, so it applies exactly what was listed.
		Expect(accept).To(Equal("lola update -C "+proj+" --fetch --trust=entries --at example="+gitIn(work, "rev-parse", "HEAD")+" -y"), r.err)

		r = runHint(accept)
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(proj, ".claude/commands/example.md")).NotTo(BeAnExistingFile())
		Expect(read(".claude/commands/renamed.md")).To(Equal("example v1\n"))
	})

	It("Update-removals: a refusal offers no -y while a pulled entry's new content is unread, and the run it hints lists those files", func() {
		// example, which the record covers, drops a file; other's ref is changed by a pull to a
		// branch that drops one too, which lola does not fetch before you trust it.
		gitIn(work, "mv", "commands/example.md", "commands/renamed.md")
		gitIn(work, "commit", "-q", "-m", "rename")
		gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "example.git"), "main")
		w := filepath.Join(tmp, "work", "other")
		gitIn(w, "checkout", "-q", "-b", "next")
		gitIn(w, "mv", "commands/other.md", "commands/moved.md")
		gitIn(w, "commit", "-q", "-m", "rename")
		gitIn(w, "push", "-q", otherBare, "next")
		manifestPath := filepath.Join(proj, ".lola", "project.yml")
		b, err := os.ReadFile(manifestPath)
		Expect(err).NotTo(HaveOccurred())
		at := strings.Index(string(b), "name: other")
		Expect(at).To(BeNumerically(">", 0), string(b))
		pulled := string(b[:at]) + strings.Replace(string(b[at:]), "ref: main", "ref: next", 1)
		Expect(pulled).NotTo(Equal(string(b)))
		Expect(os.WriteFile(manifestPath, []byte(pulled), 0o644)).To(Succeed())

		r := lola("update", "--fetch", "-C", proj)
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(ContainSubstring(".claude/commands/example.md"), "example's dropped file is listed")
		Expect(r.err).NotTo(ContainSubstring(".claude/commands/other.md"), "other's content was not fetched")
		accept := ""
		for _, h := range hintLines(r.err) {
			if c, ok := strings.CutPrefix(h, "then accept them: "); ok {
				accept = c
			}
		}
		Expect(accept).To(Equal("lola update -C "+proj+" --fetch --trust=entries --at example="+gitIn(work, "rev-parse", "HEAD")),
			"no -y: it would confirm other's unlisted removals: "+r.err)
		Expect(r.err).To(ContainSubstring("error: lola update needs your trust for changes to .lola/project.yml and the pins it would write for example, other; " +
			"and your confirmation to remove the files example's new version drops, and there is no terminal to ask on; " +
			"until it reads other, the removals listed may change, so -y waits until the next run lists them\n"))

		// Your own -y is not repeated either: it would confirm other's removals unlisted.
		r = lola("update", "--fetch", "-y", "-C", proj)
		Expect(r.code).To(Equal(1), r.out)
		Expect(hintLines(r.err)).To(ContainElement("then accept them: lola update -C "+proj+" --fetch --trust=entries --at example="+gitIn(work, "rev-parse", "HEAD")), r.err)
		Expect(r.err).To(ContainSubstring("-y waits until the next run lists"))

		// That run reads other, whose new version no refusal showed: it is refused, and the dry run
		// it hints lists other's files.
		r = runHint(accept)
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(ContainSubstring("error: other has a new version no refusal showed, and --at does not pin it"))
		see := ""
		for _, h := range hintLines(r.err) {
			if c, ok := strings.CutPrefix(h, "see what changed: "); ok {
				see = c
			}
		}
		r = runHint(see)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out+r.err).To(ContainSubstring(".claude/commands/other.md"), "the dry run it hints lists other's files")
		Expect(read(".claude/commands/other.md")).To(Equal("other v1\n"), "nothing removed unlisted")
		Expect(read(".claude/commands/example.md")).To(Equal("example v1\n"))
	})

	It("Update-removals: a hinted run whose source moved since the refusal is refused, writing nothing, with a fresh -n as its hint", func() {
		gitIn(work, "mv", "commands/example.md", "commands/renamed.md")
		gitIn(work, "commit", "-q", "-m", "rename")
		gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "example.git"), "main")
		shown := gitIn(work, "rev-parse", "HEAD")
		r := lola("update", "--fetch", "-C", proj)
		Expect(r.code).To(Equal(1), r.out)
		accept := ""
		for _, h := range hintLines(r.err) {
			if c, ok := strings.CutPrefix(h, "then accept them: "); ok {
				accept = c
			}
		}
		Expect(accept).To(ContainSubstring("--at example="+shown), r.err)

		push(map[string]string{"commands/gone-too.md": "x\n"}) // upstream moves on
		gitIn(work, "rm", "-q", "commands/renamed.md")
		gitIn(work, "commit", "-q", "-m", "drop it")
		gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "example.git"), "main")
		moved := gitIn(work, "rev-parse", "HEAD")
		before := state()
		r = runHint(accept)
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(ContainSubstring("error: example: its source now serves " + moved + ", not " + shown + " as --at says; nothing was written\n"))
		Expect(hintLines(r.err)).To(ContainElement("see what it serves now: lola update -C " + proj + " --fetch --trust=entries -n"))
		Expect(state()).To(Equal(before), "nothing written: not the cache, the trust record or the project")
		Expect(read(".claude/commands/example.md")).To(Equal("example v1\n"))
	})

	It("Update-removals: a run given --at applies only versions a refusal showed: a module that moved meanwhile, unpinned, is refused, writing nothing, under --scope both too", func() {
		gitIn(work, "mv", "commands/example.md", "commands/renamed.md")
		gitIn(work, "commit", "-q", "-m", "rename")
		gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "example.git"), "main")
		shown := gitIn(work, "rev-parse", "HEAD")
		r := lola("update", "--fetch", "-C", proj)
		Expect(r.code).To(Equal(1), r.out)
		accept := ""
		for _, h := range hintLines(r.err) {
			if c, ok := strings.CutPrefix(h, "then accept them: "); ok {
				accept = c
			}
		}
		Expect(accept).To(Equal("lola update -C "+proj+" --fetch --trust=entries --at example="+shown+" -y"), r.err)

		w := filepath.Join(tmp, "work", "other") // other moves on after the refusal, which showed it unchanged
		writeTree(w, map[string]string{"commands/other.md": "other v2\n"})
		gitIn(w, "commit", "-q", "-am", "two")
		gitIn(w, "push", "-q", otherBare, "main")
		before := state()
		for _, run := range [][]string{shellWords(accept)[1:], {"update", "-C", proj, "--scope", "both", "--fetch", "--trust=entries", "--at", "example=" + shown, "-y"}} {
			r = lola(run...)
			Expect(r.code).To(Equal(1), "%v: %s", run, r.out)
			Expect(r.err).To(ContainSubstring("error: other has a new version no refusal showed, and --at does not pin it: "+
				"a run given --at applies only the versions a refusal showed; nothing was written"), "%v", run)
			Expect(hintLines(r.err)).To(ContainElement(HavePrefix("see what changed: lola update -C "+proj)), "%v", run)
			Expect(hintLines(r.err)).To(ContainElement(HaveSuffix(" --fetch --trust=entries -n")), "%v", run)
			Expect(state()).To(Equal(before), "%v: nothing written: not the cache, the trust record or the project", run)
		}
		Expect(read(".claude/commands/other.md")).To(Equal("other v1\n"))
		Expect(read(".claude/commands/example.md")).To(Equal("example v1\n"))
	})

	It("Update-removals: a pulled pin whose new version is unread is pinned in the hint, and the run it hints lists its removals before deleting anything", func() {
		w := filepath.Join(tmp, "work", "other") // a teammate updates other, which drops its file, and you pull it
		gitIn(w, "mv", "commands/other.md", "commands/moved.md")
		gitIn(w, "commit", "-q", "-m", "rename")
		gitIn(w, "push", "-q", otherBare, "main")
		mate := filepath.Join(tmp, "mate")
		Expect(os.CopyFS(mate, os.DirFS(proj))).To(Succeed())
		r := lola("update", "-C", mate, "--fetch", "--trust=entries", "-y")
		Expect(r.code).To(Equal(0), r.err)
		pulled, err := os.ReadFile(filepath.Join(mate, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(proj, ".lola", "project.yml"), pulled, 0o644)).To(Succeed())
		gitIn(work, "mv", "commands/example.md", "commands/renamed.md")
		gitIn(work, "commit", "-q", "-m", "rename")
		gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "example.git"), "main")

		r = lola("update", "--fetch", "-C", proj)
		Expect(r.code).To(Equal(1), r.out)
		accept := ""
		for _, h := range hintLines(r.err) {
			if c, ok := strings.CutPrefix(h, "then accept them: "); ok {
				accept = c
			}
		}
		Expect(accept).To(ContainSubstring("--at example="), r.err)
		Expect(accept).To(ContainSubstring("--at other="+gitIn(w, "rev-parse", "HEAD")), "the pulled pin the diff showed")
		Expect(accept).NotTo(HaveSuffix(" -y"), "other's removals are not listed yet")
		before := state()
		r = runHint(accept)
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(ContainSubstring("needs your confirmation to remove the files example's and other's new version drops"))
		Expect(r.err).To(ContainSubstring(".claude/commands/other.md"), "other's files are listed")
		Expect(state()).To(Equal(before), "nothing written")
		Expect(read(".claude/commands/other.md")).To(Equal("other v1\n"), "nothing deleted")
	})

	It("Update-removals: under --scope both a refused run leaves both scopes and the cache byte for byte, and each scope's hint pins only its own modules", func() {
		u := filepath.Join(tmp, "work", "u")
		writeTree(u, map[string]string{"lola.yml": "name: u\n", "commands/u.md": "u v1\n"})
		gitIn(u, "init", "-q", "-b", "main")
		gitIn(u, "add", "-A")
		gitIn(u, "commit", "-q", "-m", "init")
		gitIn(tmp, "clone", "-q", "--bare", u, filepath.Join(tmp, "srv", "u.git"))
		r := lola("install", served+"/u.git", "--ref", "main", "-t", "claude-code", "--user")
		Expect(r.code).To(Equal(0), r.err)
		gitIn(work, "mv", "commands/example.md", "commands/renamed.md")
		gitIn(work, "commit", "-q", "-m", "rename")
		gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "example.git"), "main")
		e := gitIn(work, "rev-parse", "HEAD")
		gitIn(u, "mv", "commands/u.md", "commands/gone.md") // u drops its file after the refusal
		gitIn(u, "commit", "-q", "-m", "rename")
		gitIn(u, "push", "-q", filepath.Join(tmp, "srv", "u.git"), "main")
		uc := gitIn(u, "rev-parse", "HEAD")

		before := state()
		r = lola("update", "--scope", "both", "-C", proj, "--fetch", "--trust=entries", "--at", "example="+e, "-y")
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(ContainSubstring("u has a new version no refusal showed, and --at does not pin it"))
		Expect(state()).To(Equal(before), "neither scope, nor the cache, is written")

		r = lola("update", "--scope", "both", "-C", proj, "--fetch", "--trust=entries", "--at", "example="+e, "--at", "u="+uc)
		Expect(r.code).To(Equal(1), r.out)
		Expect(state()).To(Equal(before))
		for _, h := range hintLines(r.err) {
			if strings.Contains(h, "--user") {
				Expect(h).NotTo(ContainSubstring("--at example="), "the user scope's hint pins only its own modules")
			}
		}

		r = lola("update", "--scope", "both", "-C", proj, "--at", "nope="+e)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: --at names nope, which no scope of this run includes\n"))
	})

	It("a conflict's --force hint keeps your own --trust, --at and -y, so it runs as printed, and the refused run writes nothing", func() {
		push(map[string]string{"commands/new.md": "new\n"})
		h := gitIn(work, "rev-parse", "HEAD")
		Expect(os.WriteFile(filepath.Join(proj, ".claude", "commands", "new.md"), []byte("mine\n"), 0o644)).To(Succeed())
		before := state()
		r := lola("update", "-C", proj, "--fetch", "--trust=entries", "--at", "example="+h, "-y")
		Expect(r.code).To(Equal(1), r.out)
		Expect(state()).To(Equal(before), "a conflict leaves the cache as it was")
		force := ""
		for _, l := range hintLines(r.err) {
			if strings.HasSuffix(l, " --force") {
				force = l[strings.Index(l, "lola "):]
			}
		}
		Expect(force).To(Equal("lola update --at example="+h+" --fetch --root "+proj+" --trust=entries --yes --force"), r.err)
		r = runHint(force)
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(".claude/commands/new.md")).To(Equal("new\n"))
	})

	It("Update-removals: a hint pins a pulled pin the refusal showed, so a second pull before the hinted run is refused, writing nothing", func() {
		w := filepath.Join(tmp, "work", "other")
		mateUpdate := func() string { // a teammate updates other, and you pull their project.yml
			GinkgoHelper()
			mate := filepath.Join(tmp, "mate")
			Expect(os.RemoveAll(mate)).To(Succeed())
			Expect(os.CopyFS(mate, os.DirFS(proj))).To(Succeed())
			r := lola("update", "-C", mate, "--fetch", "--trust=entries", "-y")
			Expect(r.code).To(Equal(0), r.err)
			pulled, err := os.ReadFile(filepath.Join(mate, ".lola", "project.yml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(filepath.Join(proj, ".lola", "project.yml"), pulled, 0o644)).To(Succeed())
			return gitIn(w, "rev-parse", "HEAD")
		}
		writeTree(w, map[string]string{"commands/a.md": "a\n"})
		gitIn(w, "add", "-A")
		gitIn(w, "commit", "-q", "-m", "a")
		gitIn(w, "push", "-q", otherBare, "main")
		shown := mateUpdate()
		r := lola("update", "-C", proj)
		Expect(r.code).To(Equal(1), r.out)
		accept := ""
		for _, h := range hintLines(r.err) {
			if c, ok := strings.CutPrefix(h, "then accept them: "); ok {
				accept = c
			}
		}
		Expect(accept).To(Equal("lola update -C "+proj+" --trust=entries --at other="+shown), r.err)

		writeTree(w, map[string]string{"commands/evil.md": "evil\n"}) // a second pull lands before the hinted run
		gitIn(w, "add", "-A")
		gitIn(w, "commit", "-q", "-m", "evil")
		gitIn(w, "push", "-q", otherBare, "main")
		mateUpdate()
		before := state()
		r = runHint(accept)
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(ContainSubstring("error: other: its source now serves "))
		Expect(state()).To(Equal(before), "nothing written")
		Expect(filepath.Join(proj, ".claude", "commands", "evil.md")).NotTo(BeAnExistingFile())
	})

	It("refuses an --at that is not <module>=<commit or digest>, or names a module the run does not include", func() {
		sha := strings.Repeat("a", 40)
		for _, c := range []struct{ arg, msg string }{
			{"example", `--at example: want <module>=<commit>, or <module>=sha256:<digest> for a folder or archive`},
			{"example=main", `--at example=main: want <module>=<commit>, or <module>=sha256:<digest> for a folder or archive`},
			{"=" + sha, `--at =` + sha + `: want <module>=<commit>, or <module>=sha256:<digest> for a folder or archive`},
		} {
			r := lola("update", "--at", c.arg, "-C", proj)
			Expect(r.code).To(Equal(1), c.arg)
			Expect(r.err).To(ContainSubstring("error: "+c.msg+"\n"), c.arg)
		}
		for _, c := range []struct{ arg string }{{"example"}, {"example=main"}} {
			r := lola("update", "--at", c.arg, "-C", proj)
			Expect(hintLines(r.err)).To(ContainElement("run it without --at: lola update --root "+proj), c.arg)
		}
		r := lola("update", "--at", "example="+sha, "--at", "example="+strings.Repeat("b", 40), "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: --at names example twice, with two versions\n"))
		Expect(hintLines(r.err)).To(ContainElement("run it without --at: lola update --root " + proj))
		r = lola("update", "--at", "example=sha256:"+strings.Repeat("a", 64), "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: example is a git module: --at takes its commit, not a sha256: digest\n"))
		r = lola("update", "--at", "nope="+sha, "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: --at names nope, which this run does not include\n"))
		Expect(hintLines(r.err)).To(ContainElement("drop it: lola update -C " + proj))

		// A commit in capitals is the same commit, and one --at given twice alike is one.
		head := strings.ToUpper(gitIn(work, "rev-parse", "HEAD"))
		r = lola("update", "--at", "example="+head, "--at", "example="+head, "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
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

var _ = Describe("Update-removals: lola sync of a folder that dropped a component", func() {
	It("Update-removals: lists the files, refuses off a terminal with the -y hint, and removes them with -y", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, mod := filepath.Join(tmp, "proj"), filepath.Join(tmp, "m")
		writeTree(mod, map[string]string{"lola.yml": "name: m\n", "commands/a.md": "A\n", "commands/b.md": "B\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		Expect(lola("install", mod, "-C", proj, "-t", "claude-code").code).To(Equal(0))
		Expect(os.Remove(filepath.Join(mod, "commands", "b.md"))).To(Succeed())
		gone := "removals:\n  ✗ m: claude-code (not in the version this run installs)\n      .claude/commands/b.md\n"

		r := lola("sync", "-C", proj, "--trust", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out + r.err).To(ContainSubstring(gone))

		r = lola("sync", "-C", proj, "--trust")
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(ContainSubstring(gone))
		Expect(hintLines(r.err)).To(ContainElement(And(HavePrefix("then confirm them: lola sync -C "+proj+" --trust=entries,profiles --at m=sha256:"), HaveSuffix(" -y"))))
		Expect(filepath.Join(proj, ".claude/commands/b.md")).To(BeAnExistingFile())

		r = lola("sync", "-C", proj, "--trust", "--plain")
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(ContainSubstring("remove\tm\tclaude-code\t.claude/commands/b.md\tproject\n"), "--plain: rows with the scope, as install's refusal")
		Expect(r.err).NotTo(ContainSubstring("removals:"))

		r = lola("sync", "-C", proj, "--trust", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(proj, ".claude/commands/b.md")).NotTo(BeAnExistingFile())
	})
})

var _ = Describe("Update-removals: a refusal for trust names the files a new version drops", func() {
	It("Update-removals: one refusal, whose accept hint carries -y and runs as printed", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, mod := filepath.Join(tmp, "proj"), filepath.Join(tmp, "m")
		writeTree(mod, map[string]string{"lola.yml": "name: m\n", "commands/a.md": "A\n", "commands/b.md": "B\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		Expect(lola("install", mod, "-C", proj, "-t", "claude-code").code).To(Equal(0))
		Expect(os.Remove(filepath.Join(mod, "commands", "b.md"))).To(Succeed())

		r := lola("sync", "-C", proj)
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(ContainSubstring("your confirmation to remove the files m's new version drops"))
		hints := hintLines(r.err)
		accept := ""
		for _, h := range hints {
			if c, ok := strings.CutPrefix(h, "then accept them: "); ok {
				accept = c
			}
		}
		Expect(accept).To(HaveSuffix(" -y"), r.err)
		r = runHint(accept)
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(proj, ".claude/commands/b.md")).NotTo(BeAnExistingFile())
	})
})

var _ = Describe("a folder module's new pre-install hook", func() {
	It("is refused off a terminal with a review hint that runs as printed and shows the new version's hook", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj := filepath.Join(tmp, "proj")
		kit := filepath.Join(proj, "mods", "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Kit\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		r := lola("install", kit, "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		writeTree(kit, map[string]string{"lola.yml": "name: kit\nhooks: {pre-install: setup.sh}\n", "setup.sh": "#!/bin/sh\necho set up\n"})
		Expect(os.Chmod(filepath.Join(kit, "setup.sh"), 0o755)).To(Succeed())
		GinkgoT().Chdir(proj)

		r = lola("sync", "--trust=entries")
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(ContainSubstring("kit has a pre-install hook, and lola runs hooks only with your consent"))
		review := ""
		for _, h := range hintLines(r.err) {
			if c, ok := strings.CutPrefix(h, "review it: "); ok {
				review = c
			}
		}
		Expect(review).To(Equal("lola info --project ./mods/kit --target claude-code"), r.err)
		r = runHint(review)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("  pre-install hook setup.sh"), "the new version's hook")

		// The consent it hints covers the version reviewed alone: a hook edited since is refused, not run.
		consent := ""
		for _, h := range hintLines(lola("sync", "--trust=entries").err) {
			if c, ok := strings.CutPrefix(h, "then consent to it: "); ok {
				consent = c
			}
		}
		Expect(consent).To(MatchRegexp(`^lola sync --project --trust=entries,hooks --at kit=sha256:[0-9a-f]{64}$`))
		pwned := filepath.Join(tmp, "pwned")
		writeTree(kit, map[string]string{"setup.sh": "#!/bin/sh\ntouch " + pwned + "\n"})
		r = runHint(consent)
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(ContainSubstring("error: kit: its source now serves sha256:"))
		Expect(pwned).NotTo(BeAnExistingFile(), "the edited hook never ran")
	})
})

var _ = Describe("lola install --at", func() {
	var tmp, proj, kit string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, kit = filepath.Join(tmp, "proj"), filepath.Join(tmp, "kit")
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		writeTree(kit, map[string]string{"lola.yml": "name: kit\nhooks: {pre-install: setup.sh}\n", "setup.sh": "#!/bin/sh\necho set up\n", "commands/kit.md": "Kit\n"})
		Expect(os.Chmod(filepath.Join(kit, "setup.sh"), 0o755)).To(Succeed())
	})

	It("pins in the hook's consent hint the version the refusal showed, so a folder edited after review is refused and its hook not run", func() {
		r := lola("install", kit, "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(ContainSubstring("error: kit has a pre-install hook, and lola runs hooks only with your consent"))
		consent := ""
		for _, h := range hintLines(r.err) {
			if c, ok := strings.CutPrefix(h, "then consent to it: "); ok {
				consent = c
			}
		}
		Expect(consent).To(MatchRegexp(`^lola install ` + regexp.QuoteMeta(kit) + ` .*--trust=hooks --at kit=sha256:[0-9a-f]{64}$`))
		pwned := filepath.Join(tmp, "pwned")
		writeTree(kit, map[string]string{"setup.sh": "#!/bin/sh\ntouch " + pwned + "\n"})
		r = runHint(consent)
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(ContainSubstring("error: kit: its source now serves sha256:"))
		Expect(pwned).NotTo(BeAnExistingFile(), "the edited hook never ran")
		Expect(filepath.Join(proj, ".claude", "commands", "kit.md")).NotTo(BeAnExistingFile())
	})

	It("takes --at as sync does: checked, coached, and refused for a module the install does not include", func() {
		r := lola("install", kit, "-C", proj, "--at", "kit=main", "-t", "claude-code")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: --at kit=main: want <module>=<commit>, or <module>=sha256:<digest> for a folder or archive\n"))
		Expect(hintLines(r.err)).To(ContainElement(HavePrefix("run it without --at: lola install ")))
		r = lola("install", kit, "-C", proj, "--at", "kit="+strings.Repeat("a", 40), "-t", "claude-code")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: kit is a folder module: --at takes its content digest, sha256:<digest>, not a commit\n"))
		r = lola("install", kit, "-C", proj, "--at", "nope=sha256:"+strings.Repeat("a", 64), "-t", "claude-code")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: --at names nope, which this install does not include\n"))
	})
})
