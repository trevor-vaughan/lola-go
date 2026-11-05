package config_test

import (
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Pick-P2-a: a project's repository key", func() {
	var dir string
	BeforeEach(func() {
		var err error
		dir, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
	})
	git := func(at string, args ...string) {
		GinkgoHelper()
		out, err := exec.Command("git", append([]string{"-C", at}, args...)...).CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
	}
	repo := func(remote string) string {
		GinkgoHelper()
		r := filepath.Join(dir, "repo")
		Expect(os.MkdirAll(r, 0o755)).To(Succeed())
		git(r, "init", "-q")
		if remote != "" {
			git(r, "remote", "add", "origin", remote)
		}
		return r
	}

	DescribeTable("Pick-P2-a: every spelling of one remote is one key, with no credential in it",
		func(remote string) {
			key, ok, err := config.RepoKey(repo(remote), source.GitCommand)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(key).To(Equal("git:github.com/O/r#."))
		},
		Entry("https with .git", "https://github.com/O/r.git"),
		Entry("an upper-case host and a trailing slash", "https://GitHub.com/O/r/"),
		Entry("userinfo", "https://user:tok@github.com/O/r"),
		Entry("a query", "https://github.com/O/r.git?private_token=tok"),
		Entry("scp-style ssh", "git@github.com:O/r.git"),
		Entry("an ssh URL", "ssh://git@github.com/O/r"),
		Entry("an ssh URL with its default port", "ssh://git@github.com:22/O/r"),
		Entry("an https URL with its default port", "https://github.com:443/O/r"),
		Entry("a git URL with its default port", "git://github.com:9418/O/r"),
	)

	DescribeTable("Pick-P2-a: keeps a port other than the scheme's default",
		func(remote, want string) {
			key, ok, err := config.RepoKey(repo(remote), source.GitCommand)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(key).To(Equal(want))
		},
		Entry("ssh on 2222", "ssh://git@GitHub.com:2222/O/r.git", "git:github.com:2222/O/r#."),
		Entry("https on 8443", "https://github.com:8443/O/r", "git:github.com:8443/O/r#."),
		Entry("https on ssh's port", "https://github.com:22/O/r", "git:github.com:22/O/r#."),
		Entry("an IPv6 host, bracketed", "ssh://git@[::1]:2222/x", "git:[::1]:2222/x#."),
		Entry("an IPv6 host on the default port", "https://[::1]/x", "git:[::1]/x#."),
	)

	It("Pick-P2-a: keeps the path's case", func() {
		key, ok, err := config.RepoKey(repo("https://github.com/o/R"), source.GitCommand)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(key).To(Equal("git:github.com/o/R#."))
	})

	It("Pick-P2-a: names a project root below the work-tree top by its directory there", func() {
		sub := filepath.Join(repo("git@example.com:team/mono.git"), "sub", "dir")
		Expect(os.MkdirAll(sub, 0o755)).To(Succeed())
		key, ok, err := config.RepoKey(sub, source.GitCommand)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(key).To(Equal("git:example.com/team/mono#sub/dir"))
	})

	DescribeTable("Pick-P2-a: has no key, so the project keeps its path key",
		func(remote string) {
			key, ok, err := config.RepoKey(repo(remote), source.GitCommand)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeFalse())
			Expect(key).To(BeEmpty())
		},
		Entry("an scp-style origin with a token", "x-access-token:tok@github.com:O/r"),
		Entry("an scp-style origin with a user and token", "user:tok@github.com:O/r.git"),
		Entry("an @ in a URL's path", "https://github.com/O/r@tok"),
		Entry("an scp-style bracketed host", "git@[::1]:x"),
		Entry("a work tree with no origin", ""),
		Entry("a local path origin", "/srv/git/r.git"),
		Entry("a file URL origin", "file:///srv/git/r.git"),
		Entry("a transport helper origin", "ext::ssh example.com %S r"),
		Entry("an origin with no path", "https://github.com/"),
		Entry("an origin holding a format character", "https://github.com/O/r\u202e"),
	)

	It("Pick-P2-a: has no key outside a work tree", func() {
		_, ok, err := config.RepoKey(dir, source.GitCommand)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
	})

	It("Pick-P2-a: has no key for a project directory that is gone, even inside a clone", func() {
		_, ok, err := config.RepoKey(filepath.Join(repo("https://github.com/O/r"), "gone"), source.GitCommand)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
	})

	It("Pick-P2-a: has no key without git", func() {
		r := repo("https://github.com/O/r")
		GinkgoT().Setenv("PATH", "")
		_, ok, err := config.RepoKey(r, source.GitCommand)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
	})

	It("Pick-P2-a: names git's first line for a work tree git cannot read, with no hint", func() {
		bad := filepath.Join(dir, "bad")
		Expect(os.MkdirAll(bad, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(bad, ".git"), []byte("gitdir: nowhere\n"), 0o644)).To(Succeed())
		_, _, err := config.RepoKey(bad, source.GitCommand)
		Expect(err).To(MatchError("git could not read the repository of " + bad + ": fatal: not a git repository: " + filepath.Join(bad, "nowhere")))
		Expect(errs.Hints(err)).To(BeEmpty())
	})

	It("Pick-P2-a: hints the safe.directory command for a repository git refuses as someone else's", func() {
		r := repo("https://github.com/O/r")
		sub := filepath.Join(r, "sub")
		Expect(os.MkdirAll(sub, 0o755)).To(Succeed())
		GinkgoT().Setenv("GIT_TEST_ASSUME_DIFFERENT_OWNER", "1")
		_, _, err := config.RepoKey(sub, source.GitCommand)
		Expect(err).To(MatchError(HavePrefix("git could not read the repository of " + sub + ": fatal: detected dubious ownership in repository at")))
		Expect(errs.Hints(err)).To(Equal([]string{"git config --global --add safe.directory " + r}))
	})
})

var _ = Describe("Pick-P2-a: selections.yaml sections keyed by repository", func() {
	const repoKey = "git:github.com/O/r#."
	var dir, proj, path string
	var w config.Where
	BeforeEach(func() {
		var err error
		dir, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(dir, "src", "api")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		path = filepath.Join(dir, "selections.yaml")
		w = config.RepoProject(repoKey, proj)
	})
	write := func(s string) { Expect(os.WriteFile(path, []byte(s), 0o600)).To(Succeed()) }
	read := func() string {
		GinkgoHelper()
		b, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}
	load := func() *config.Selections {
		GinkgoHelper()
		s, err := config.LoadSelections(path)
		Expect(err).NotTo(HaveOccurred())
		return s
	}
	skip := func(p string) config.Entry {
		GinkgoHelper()
		pat, err := config.ParsePattern(p)
		Expect(err).NotTo(HaveOccurred())
		return config.Entry{Skip: []config.Pattern{pat}}
	}
	const other = "  /elsewhere: # theirs\n    m:\n      skip: [agent/x]\n"

	It("Pick-P2-a: prints the repository key in the section's origin", func() {
		Expect(w.Origin()).To(Equal("selections.yaml projects[" + repoKey + "]"))
	})

	It("Pick-P2-a: reads a section under the repository key", func() {
		write("version: 1\nprojects:\n  " + repoKey + ":\n    m:\n      skip: [skill/a]\n")
		Expect(load().Exclusions(w, "m").Excludes("skill", "a")).To(BeTrue())
		Expect(load().Exclusions(config.Project(proj), "m").Excludes("skill", "a")).To(BeFalse(), "a path key does not name a repository's section")
	})

	It("Pick-P2-a: names the section that holds a module's entry by its key as the file writes it", func() {
		Expect(os.Symlink(filepath.Join(dir, "src"), filepath.Join(dir, "link"))).To(Succeed())
		linked := filepath.Join(dir, "link", "api")
		write("version: 1\nprojects:\n  " + linked + ":\n    m:\n      skip: [skill/a]\n  " + repoKey + ":\n    n:\n      skip: [skill/n]\n")
		s := load()
		Expect(s.Origin(w, "m")).To(Equal("selections.yaml projects[" + linked + "]"))
		Expect(s.Origin(w, "n")).To(Equal("selections.yaml projects[" + repoKey + "]"))
		Expect(s.Origin(w, "none")).To(Equal("selections.yaml projects["+repoKey+"]"), "no entry: the key lola writes")
		Expect(s.Origin(config.Global(), "m")).To(Equal("selections.yaml global"))
	})

	It("Pick-P2-a: still applies a section under the project's path key", func() {
		write("version: 1\nprojects:\n  " + proj + ":\n    m:\n      skip: [skill/a]\n")
		Expect(load().Exclusions(w, "m").Excludes("skill", "a")).To(BeTrue())
	})

	It("Pick-P2-a: prefers the repository key's entry for a module both keys hold, and reads the path key's for the rest", func() {
		write("version: 1\nprojects:\n  " + proj + ":\n    m:\n      skip: [skill/a]\n    n:\n      skip: [skill/n]\n  " +
			repoKey + ":\n    m:\n      skip: [skill/b]\n")
		s := load()
		Expect(s.Entry(w, "m")).To(Equal(skip("skill/b")))
		Expect(s.Entry(w, "n")).To(Equal(skip("skill/n")))
	})

	It("Pick-P2-a: rewrites a path key to the repository key on the next write, keeping the file's other entries byte for byte", func() {
		write("version: 1\nprojects:\n" + other + "  " + proj + ":  # my api\n    m:\n      skip: [skill/a]\n    n:\n      skip: [skill/n]\n")
		Expect(config.Save(path, []config.Change{{Section: w, Module: "m", Entry: skip("skill/b")}})).To(Succeed())
		Expect(read()).To(Equal("version: 1\nprojects:\n" + other + "  " + repoKey + ": # my api\n    m:\n      skip: [skill/b]\n    n:\n      skip: [skill/n]\n"))
		s := load()
		s.Set(w, "m", skip("skill/b"))
		Expect(s.Entry(w, "n")).To(Equal(skip("skill/n")))
	})

	It("Pick-P2-a: moves a module's entry from the path key to an existing repository key, dropping the path key it empties", func() {
		write("version: 1\nprojects:\n" + other + "  " + proj + ":\n    m:\n      skip: [skill/a]\n  " + repoKey + ":\n    n:\n      skip: [skill/n]\n")
		Expect(config.Save(path, []config.Change{{Section: w, Module: "m", Entry: skip("skill/b")}})).To(Succeed())
		Expect(read()).To(Equal("version: 1\nprojects:\n" + other + "  " + repoKey + ":\n    n:\n      skip: [skill/n]\n    m:\n      skip: [skill/b]\n"))
	})

	It("Pick-P2-a: removes a module's entry under either key", func() {
		write("version: 1\nprojects:\n" + other + "  " + proj + ":\n    m:\n      skip: [skill/a]\n    n:\n      skip: [skill/n]\n  " + repoKey + ":\n    m:\n      skip: [skill/b]\n")
		Expect(config.Save(path, []config.Change{{Section: w, Module: "m"}})).To(Succeed())
		Expect(read()).To(Equal("version: 1\nprojects:\n" + other + "  " + proj + ":\n    n:\n      skip: [skill/n]\n"))
		s := load()
		s.Set(w, "m", config.Entry{})
		Expect(s.Entry(w, "m").Empty()).To(BeTrue())
		Expect(s.Entry(w, "n")).To(Equal(skip("skill/n")))
	})

	It("Pick-P2-a: sets in memory as Save writes: a path key's section moves to the repository key", func() {
		write("version: 1\nprojects:\n  " + proj + ":\n    m:\n      skip: [skill/a]\n    n:\n      skip: [skill/n]\n")
		s := load()
		s.Set(w, "m", skip("skill/b"))
		Expect(s.Entry(w, "m")).To(Equal(skip("skill/b")))
		Expect(s.Entry(w, "n")).To(Equal(skip("skill/n")))
		Expect(s.Entry(config.Project(proj), "n").Empty()).To(BeTrue(), "the section is the repository's now")
	})

	DescribeTable("Pick-P2-a: refuses a repository key lola would not write",
		func(key, why string) {
			write("version: 1\nprojects:\n  '" + key + "':\n    m:\n      skip: [skill/a]\n")
			_, err := config.LoadSelections(path)
			Expect(err).To(MatchError(ContainSubstring(why)))
		},
		Entry("no subdirectory", "git:github.com/O/r", "a repository key ends in #<directory>"),
		Entry("no repository", "git:#.", "a repository key ends in #<directory>"),
		Entry("a format character", "git:github.com/O/r\u202e#.", "control or format character"),
	)
})
