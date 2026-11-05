package source_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// remote is a repository served over loopback HTTP: work is where specs commit, bare is the
// served copy and url its address.
type remote struct{ work, bare, url string }

func newRemote(tmp string, files map[string]string) remote {
	work := filepath.Join(tmp, "work")
	writeTree(work, files)
	git(work, "init", "-q", "-b", "main")
	git(work, "add", "-A")
	git(work, "commit", "-q", "-m", "init")
	bare := filepath.Join(tmp, "srv", "example.git")
	git(tmp, "clone", "-q", "--bare", work, bare)
	return remote{work: work, bare: bare, url: testenv.ServeGit(filepath.Join(tmp, "srv")) + "/example.git"}
}

// commit commits files on the work tree's current branch, pushes it and returns the commit.
func (r remote) commit(files map[string]string) string {
	writeTree(r.work, files)
	git(r.work, "add", "-A")
	git(r.work, "commit", "-q", "-m", "change")
	git(r.work, "push", "-q", "--all", r.bare)
	return r.rev("HEAD")
}

func (r remote) rev(name string) string {
	out, err := exec.Command("git", "-C", r.work, "rev-parse", name).Output()
	Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(string(out))
}

var _ = Describe("Fetch of a git source", func() {
	var tmp, staging string
	ctx := context.Background()
	BeforeEach(func() {
		tmp = testenv.Isolate()
		staging = filepath.Join(tmp, "staging")
	})
	fetch := func(url, ref, path string) (source.Fetched, error) {
		return source.Fetch(ctx, source.Spec{Kind: source.KindGit, Location: url, Ref: ref, Path: path}, staging)
	}
	read := func(f source.Fetched, name string) string {
		b, err := os.ReadFile(filepath.Join(f.Dir, name))
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}
	fakeGit := func(stderr string) {
		script := "#!/bin/sh\nprintf '%b\\n' '" + stderr + "' >&2\nexit 128\n"
		Expect(os.WriteFile(filepath.Join(tmp, "bin", "git"), []byte(script), 0o755)).To(Succeed())
	}

	It("clones the default branch, records its commit, and copies only what is committed", func() {
		r := newRemote(tmp, map[string]string{"commands/a.md": "A"})
		f, err := fetch(r.url, "", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Name).To(Equal("example"))
		Expect(f.Commit).To(Equal(r.rev("main")))
		Expect(listTree(f.Dir)).To(ConsistOf("commands/a.md"))
		entries, _ := os.ReadDir(staging)
		Expect(entries).To(HaveLen(1)) // the clone is gone
	})

	It("fetches a branch, a tag and an annotated tag", func() {
		r := newRemote(tmp, map[string]string{"commands/a.md": "v1"})
		git(r.work, "tag", "v1")
		git(r.work, "checkout", "-q", "-b", "feature")
		r.commit(map[string]string{"commands/a.md": "feature"})
		git(r.work, "tag", "-a", "-m", "two", "v2")
		git(r.work, "checkout", "-q", "main")
		r.commit(map[string]string{"commands/a.md": "main"})
		git(r.work, "push", "-q", "--tags", r.bare)
		for ref, want := range map[string]string{"": "main", "feature": "feature", "v1": "v1", "v2": "feature"} {
			f, err := fetch(r.url, ref, "")
			Expect(err).NotTo(HaveOccurred(), ref)
			Expect(read(f, "commands/a.md")).To(Equal(want), ref)
		}
	})

	It("fetches a full commit SHA that no branch or tag points to", func() {
		r := newRemote(tmp, map[string]string{"commands/a.md": "old"})
		old := r.rev("HEAD")
		r.commit(map[string]string{"commands/a.md": "new"})
		f, err := fetch(r.url, old, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Commit).To(Equal(old))
		Expect(read(f, "commands/a.md")).To(Equal("old"))
	})

	It("explains that a protocol v0 server refuses an unadvertised commit", func() {
		r := newRemote(tmp, map[string]string{"commands/a.md": "old"})
		old := r.rev("HEAD")
		r.commit(map[string]string{"commands/a.md": "new"})
		cfg := filepath.Join(tmp, "gitconfig")
		Expect(os.WriteFile(cfg, []byte("[protocol]\n\tversion = 0\n"), 0o644)).To(Succeed())
		GinkgoT().Setenv("GIT_CONFIG_GLOBAL", cfg)
		_, err := fetch(r.url, old, "")
		Expect(err).To(MatchError(ContainSubstring("did not serve commit " + old)))
		Expect(errs.Hints(err)).To(ContainElement("use --ref with a branch or tag that contains the commit"))
	})

	It("refuses a ref that names both a branch and a tag", func() {
		r := newRemote(tmp, map[string]string{"commands/a.md": "A"})
		git(r.work, "branch", "dup")
		git(r.work, "tag", "dup")
		git(r.work, "push", "-q", "--all", r.bare)
		git(r.work, "push", "-q", "--tags", r.bare)
		_, err := fetch(r.url, "dup", "")
		Expect(err).To(MatchError(ContainSubstring("has both a branch and a tag named dup")))
	})

	It("refuses a short commit SHA, asking for the full one", func() {
		r := newRemote(tmp, map[string]string{"commands/a.md": "A"})
		_, err := fetch(r.url, r.rev("HEAD")[:7], "")
		Expect(err).To(MatchError(ContainSubstring("has no branch or tag named")))
		Expect(errs.Hints(err)).To(ContainElement("use the full 40-character commit SHA"))
	})

	It("quotes the repository in the ls-remote hint for the user's shell", func() {
		r := newRemote(tmp, map[string]string{"commands/a.md": "A"})
		git(tmp, "clone", "-q", "--bare", r.work, filepath.Join(tmp, "srv", "it's.git"))
		url := strings.TrimSuffix(r.url, "example.git") + "it's.git"
		_, err := fetch(url, "nope", "")
		Expect(err).To(MatchError(url + " has no branch or tag named nope"))
		Expect(errs.Hints(err)).To(ConsistOf("list them: " + errs.ShellJoin([]string{"git", "ls-remote", "--heads", "--tags", url})))
		Expect(errs.Hints(err)).NotTo(ContainElement(ContainSubstring("--tags " + url)))
	})

	It("a hex-looking branch name is not treated as a commit", func() {
		r := newRemote(tmp, map[string]string{"commands/a.md": "first"})
		first := r.rev("HEAD")
		second := r.commit(map[string]string{"commands/a.md": "second"})
		git(r.work, "branch", first, second) // a branch named like the first commit, at the second
		git(r.work, "branch", "cafe1234", second)
		git(r.work, "push", "-q", "--all", r.bare)
		for _, ref := range []string{first, "cafe1234"} {
			f, err := fetch(r.url, ref, "")
			Expect(err).NotTo(HaveOccurred(), ref)
			Expect(f.Commit).To(Equal(second), ref)
			Expect(read(f, "commands/a.md")).To(Equal("second"), ref)
		}
	})

	It("refuses a committed symlink, naming it", func() {
		r := newRemote(tmp, map[string]string{"commands/a.md": "A"})
		Expect(os.Symlink("/etc/passwd", filepath.Join(r.work, "commands", "b.md"))).To(Succeed())
		r.commit(nil)
		_, err := fetch(r.url, "", "")
		Expect(err).To(MatchError(ContainSubstring("commands/b.md: symbolic links are not allowed")))
		entries, _ := os.ReadDir(staging)
		Expect(entries).To(BeEmpty())
	})

	It("uses module/ and --path like a folder", func() {
		r := newRemote(tmp, map[string]string{"README.md": "", "module/commands/a.md": "A", "plugins/review/agents/x.md": "X"})
		f, err := fetch(r.url, "", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Name).To(Equal("example"))
		Expect(listTree(f.Dir)).To(ConsistOf("commands/a.md"))
		f, err = fetch(r.url, "", "plugins/review")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Name).To(Equal("review"))
		Expect(listTree(f.Dir)).To(ConsistOf("agents/x.md"))
	})

	It("takes --path . as the repository's top, not its module/, named after the repository", func() {
		r := newRemote(tmp, map[string]string{"commands/a.md": "A", "module/commands/b.md": "B"})
		f, err := fetch(r.url, "", ".")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Name).To(Equal("example"))
		Expect(listTree(f.Dir)).To(ConsistOf("commands/a.md", "module/commands/b.md"))
	})

	It("coaches a --path that is not a directory, without lola's scratch directory or raw escapes", func() {
		r := newRemote(tmp, map[string]string{"commands/a.md": "A"})
		_, err := fetch(r.url, "", "nope\x1b[31mRED")
		Expect(err).To(MatchError(ContainSubstring(`--path "nope?[31mRED"`)))
		Expect(err.Error()).NotTo(ContainSubstring("\x1b"))
		Expect(err.Error()).NotTo(ContainSubstring(staging))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("--path is relative to")))
	})

	It("is not redirected by GIT_DIR or other repository-selecting variables", func() {
		r := newRemote(tmp, map[string]string{"commands/a.md": "remote"})
		decoy := filepath.Join(tmp, "decoy")
		writeTree(decoy, map[string]string{"commands/a.md": "decoy"})
		git(decoy, "init", "-q")
		git(decoy, "add", "-A")
		git(decoy, "commit", "-q", "-m", "decoy")
		GinkgoT().Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
		GinkgoT().Setenv("GIT_WORK_TREE", decoy)
		GinkgoT().Setenv("GIT_CONFIG_COUNT", "1")
		GinkgoT().Setenv("GIT_CONFIG_KEY_0", "url."+decoy+".insteadOf") // would clone the decoy instead
		GinkgoT().Setenv("GIT_CONFIG_VALUE_0", r.url)
		f, err := fetch(r.url, "", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(read(f, "commands/a.md")).To(Equal("remote"))
	})

	It("refuses an invalid ref before reaching the network", func() {
		_, err := fetch("https://example.invalid/r.git", "a..b", "")
		Expect(err).To(MatchError(ContainSubstring(`ref "a..b" is not a valid branch or tag name`)))
	})

	It("refuses a hostile git location handed straight to Fetch before git runs", func() {
		ran := filepath.Join(tmp, "GIT_RAN")
		Expect(os.WriteFile(filepath.Join(tmp, "bin", "git"), []byte("#!/bin/sh\ntouch '"+ran+"'\n"), 0o755)).To(Succeed())
		for _, loc := range []string{"ssh://-oProxyCommand=x/r", "git@-oProxyCommand:x", "git://github.com/o/r.git",
			"/tmp/x", "u:s3cr3t@github.com:o/r", "ssh://-oProxyCommand=x@host/x"} {
			_, err := fetch(loc, "", "")
			Expect(err).To(HaveOccurred(), loc)
			Expect(ran).NotTo(BeAnExistingFile(), loc)
		}
	})

	It("sanitizes terminal escapes in git's stderr", func() {
		fakeGit(`fatal: \033[2Jboom\nremote: ok`)
		_, err := fetch("https://example.invalid/r.git", "", "")
		Expect(err).To(MatchError(ContainSubstring("fatal: ?[2Jboom\n  remote: ok")))
		Expect(err.Error()).NotTo(ContainSubstring("\x1b"))
	})

	It("coaches an authentication failure", func() {
		fakeGit("fatal: Authentication failed for https://example.invalid/r.git/")
		_, err := fetch("https://example.invalid/r.git", "", "")
		Expect(err).To(MatchError(ContainSubstring("Authentication failed")))
		Expect(errs.Hints(err)).To(ContainElement("for https, configure a git credential helper (git help credentials)"))
	})

	It("keeps git's progress lines out of a failed clone's error", func() {
		_, err := fetch(testenv.ServeGit(tmp)+"/missing.git", "", "")
		Expect(err).To(MatchError(ContainSubstring("git clone failed")))
		Expect(err.Error()).NotTo(ContainSubstring("Cloning into"))
	})

	It("coaches a timeout by the location's transport", func() {
		Expect(os.WriteFile(filepath.Join(tmp, "bin", "git"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755)).To(Succeed())
		for loc, hint := range map[string]string{
			"https://example.invalid/r.git":   "check your network",
			"ssh://git@example.invalid/r.git": "ssh-agent",
			"SSH://git@example.invalid/r.git": "ssh-agent",
			"git@example.invalid:o/r.git":     "ssh-agent",
		} {
			short, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
			_, err := source.Fetch(short, source.Spec{Kind: source.KindGit, Location: loc}, staging)
			cancel()
			Expect(err).To(MatchError(ContainSubstring("did not finish in time")), loc)
			Expect(errs.Hints(err)).To(ConsistOf(ContainSubstring(hint)), loc)
		}
	})

	It("asks for git when it is not on PATH", func() {
		GinkgoT().Setenv("PATH", filepath.Join(tmp, "bin"))
		_, err := fetch("https://example.invalid/r.git", "", "")
		Expect(errs.Hints(err)).To(ContainElement("install git"))
	})
})
