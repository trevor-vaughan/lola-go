package source

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("git environment and listing", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("removes repository-selecting variables, keeps the rest and turns prompts off", func() {
		env := gitEnv([]string{"GIT_DIR=/x/.git", "GIT_WORK_TREE=/x", "GIT_INDEX_FILE=i", "GIT_OBJECT_DIRECTORY=o",
			"GIT_ALTERNATE_OBJECT_DIRECTORIES=a", "GIT_COMMON_DIR=c", "GIT_NAMESPACE=n", "GIT_CEILING_DIRECTORIES=/",
			"GIT_CONFIG_PARAMETERS='core.x=y'", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.x", "GIT_CONFIG_VALUE_0=y",
			"GIT_CONFIG=/x/config", "GIT_IMPLICIT_WORK_TREE=0", "GIT_GRAFT_FILE=g", "GIT_NO_REPLACE_OBJECTS=1",
			"GIT_REPLACE_REF_BASE=refs/r/", "GIT_PREFIX=sub/", "GIT_SHALLOW_FILE=s",
			"GIT_CONFIG_GLOBAL=/home/u/.gitconfig", "SSH_AUTH_SOCK=/tmp/agent", "GIT_TERMINAL_PROMPT=1", "HOME=/home/u"})
		Expect(env).To(Equal([]string{"GIT_CONFIG_GLOBAL=/home/u/.gitconfig", "SSH_AUTH_SOCK=/tmp/agent", "GIT_TERMINAL_PROMPT=1",
			"HOME=/home/u", "GIT_TERMINAL_PROMPT=0", "GIT_PROTOCOL_FROM_USER=0", "GIT_OPTIONAL_LOCKS=0"}))
	})

	It("lists only committed files in a clone", func() {
		dir := filepath.Join(testenv.Isolate(), "clone")
		Expect(os.MkdirAll(filepath.Join(dir, "commands"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "commands", "a.md"), []byte("A"), 0o644)).To(Succeed())
		for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-q", "-m", "init"}} {
			Expect(exec.Command("git", append([]string{"-C", dir}, args...)...).Run()).To(Succeed())
		}
		Expect(os.WriteFile(filepath.Join(dir, "commands", "untracked.md"), []byte("U"), 0o644)).To(Succeed())
		files, _, err := listFiles(context.Background(), dir, listGit)
		Expect(err).NotTo(HaveOccurred())
		Expect(files).To(Equal([]string{filepath.Join("commands", "a.md")}))
	})

	It("returns soon after its deadline even when a child of git keeps stderr open", func() {
		bin := filepath.Join(testenv.Isolate(), "bin")
		// The orphan outlives WaitDelay (5 s), so the spec still proves the bound, and exits soon after.
		Expect(os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nsleep 7 >&2 &\nexec sleep 30\n"), 0o755)).To(Succeed())
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := runGit(ctx, "", "ls-remote")
		Expect(err).To(MatchError(ContainSubstring("did not finish in time")))
		Expect(time.Since(start)).To(BeNumerically("<", 15*time.Second))
	})
})

var _ = Describe("git's stderr", func() {
	var bin string
	BeforeEach(func() {
		tmp := testenv.Isolate()
		bin = filepath.Join(tmp, "bin")
		noise := filepath.Join(tmp, "noise")
		Expect(os.WriteFile(noise, []byte(strings.Repeat("remote: x\n", 800<<10)), 0o644)).To(Succeed()) // 8 MiB
		Expect(os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\ncat '"+noise+"' >&2\nexit 1\n"), 0o755)).To(Succeed())
	})
	// allocated runs f and returns the bytes it allocated.
	allocated := func(f func()) uint64 {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		f()
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc
	}

	It("keeps only the head of what is written, and accepts all of it", func() {
		w := &headWriter{limit: 5}
		for _, p := range []string{"abc", "defg", "hij"} {
			n, err := w.Write([]byte(p))
			Expect(err).NotTo(HaveOccurred())
			Expect(n).To(Equal(len(p)))
		}
		Expect(w.String()).To(Equal("abcde"))
	})

	It("is bounded in memory and in the error when git writes megabytes of it", func() {
		var err error
		Expect(allocated(func() { _, err = runGit(context.Background(), "", "clone") })).To(BeNumerically("<", 2<<20))
		Expect(err).To(MatchError(ContainSubstring("git clone failed")))
		Expect(len(err.Error())).To(BeNumerically("<", 2*maxStderr))
	})

	It("is bounded in memory and in the error when git ls-files writes megabytes of it", func() {
		var err error
		Expect(allocated(func() { _, _, err = listFiles(context.Background(), GinkgoT().TempDir(), listGit) })).To(BeNumerically("<", 2<<20))
		Expect(err).To(MatchError(ContainSubstring("git ls-files")))
		Expect(len(err.Error())).To(BeNumerically("<", 2*maxStderr))
	})

	It("does not print the directory git ls-files ran in", func() {
		root := GinkgoT().TempDir()
		_, _, err := listFiles(context.Background(), root, listGit)
		Expect(err).To(MatchError(ContainSubstring("git ls-files")))
		Expect(err.Error()).NotTo(ContainSubstring(root))
	})
})

var _ = Describe("githubWebHint", func() {
	BeforeEach(func() { testenv.Isolate() })

	DescribeTable("coaches a github.com web URL",
		func(raw string, want *WebPage, wantNote string) {
			web, note := githubWebHint(raw)
			Expect(web).To(Equal(want))
			Expect(note).To(ContainSubstring(wantNote))
		},
		Entry("tree", "https://github.com/o/r/tree/main/docs/x",
			&WebPage{URL: "https://github.com/o/r/tree/main/docs/x", Repo: "https://github.com/o/r", Ref: "main", Path: "docs/x"}, "web page"),
		Entry("tree of skills names the repository root and says how to take one skill", "https://github.com/affaan-m/ECC/tree/main/skills/",
			&WebPage{URL: "https://github.com/affaan-m/ECC/tree/main/skills/", Repo: "https://github.com/affaan-m/ECC", Ref: "main"},
			"to take one skill, use --path skills/<name>"),
		Entry("tree of a deeper skills directory names its parent", "https://github.com/o/r/tree/main/mods/x/skills",
			&WebPage{URL: "https://github.com/o/r/tree/main/mods/x/skills", Repo: "https://github.com/o/r", Ref: "main", Path: "mods/x"},
			"--path mods/x/skills/<name>"),
		Entry("tree with no path", "https://github.com/o/r/tree/main",
			&WebPage{URL: "https://github.com/o/r/tree/main", Repo: "https://github.com/o/r", Ref: "main"}, "web page"),
		Entry("blob names its directory", "https://github.com/o/r/blob/main/skills/x/SKILL.md",
			&WebPage{URL: "https://github.com/o/r/blob/main/skills/x/SKILL.md", Repo: "https://github.com/o/r", Ref: "main", Path: "skills/x"}, "web page"),
		Entry("blob naming no file", "https://github.com/o/r/blob/main",
			&WebPage{URL: "https://github.com/o/r/blob/main", Repo: "https://github.com/o/r", Ref: "main"}, "web page"),
		Entry("query and fragment are not the path", "https://github.com/o/r/tree/main/x?tab=a#frag",
			&WebPage{URL: "https://github.com/o/r/tree/main/x?tab=a#frag", Repo: "https://github.com/o/r", Ref: "main", Path: "x"}, "web page"),
		Entry("organisation page", "https://github.com/lolables/", nil, "organisation"),
		Entry("empty ref names no ref", "https://github.com/o/r/tree//x",
			&WebPage{URL: "https://github.com/o/r/tree//x", Repo: "https://github.com/o/r"}, "web page"),
		Entry("option-like ref names no ref", "https://github.com/o/r/tree/--upload-pack=x/y",
			&WebPage{URL: "https://github.com/o/r/tree/--upload-pack=x/y", Repo: "https://github.com/o/r"}, "web page"),
		Entry("encoded control characters in an organisation", "https://github.com/%0A%1b[31mx/", nil, ""),
		Entry("encoded control characters in a tree", "https://github.com/o/r/tree/main/%0A%1b[31mx", nil, ""),
		Entry("encoded control characters in a repository", "https://github.com/o/%0Ar/tree/main/x", nil, ""),
		Entry("plain repo gets nothing", "https://github.com/o/r", nil, ""),
		Entry("other hosts get nothing", "https://gitlab.com/o/r/tree/main/x", nil, ""),
		Entry("ssh gets nothing", "git@github.com:o/r.git", nil, ""),
	)

	It("says nothing of a URL whose decoded segments hold control characters", func() {
		for _, raw := range []string{"https://github.com/%0A%1b[31mx/", "https://github.com/o/r/tree/main/%0A%1b[31mx"} {
			web, note := githubWebHint(raw)
			Expect(web).To(BeNil(), raw)
			Expect(note).To(BeEmpty(), raw)
		}
	})

	It("leads with what the address is, and leaves the lola command to the caller", func() {
		err := accessDenied([]string{"--", "https://github.com/o/r/tree/main/x"},
			"fatal: repository 'https://github.com/o/r/tree/main/x/' not found")
		Expect(err).To(MatchError(HavePrefix("https://github.com/o/r/tree/main/x is a github.com web page, not a repository URL; ")))
		Expect(err).NotTo(MatchError(ContainSubstring("was not found")))
		Expect(errs.Hints(err)).To(BeEmpty())
		var denied *AccessError
		Expect(errors.As(err, &denied)).To(BeTrue())
		Expect(denied.Web).To(Equal(&WebPage{URL: "https://github.com/o/r/tree/main/x", Repo: "https://github.com/o/r", Ref: "main", Path: "x"}))
	})

	It("offers no git check of an address it called a web page or an organisation page", func() {
		for _, raw := range []string{"https://github.com/o/r/tree//x", "https://github.com/lolables/"} {
			err := accessDenied([]string{"--", raw}, "fatal: repository '"+raw+"' not found")
			Expect(err).To(HaveOccurred(), raw)
			Expect(errs.Hints(err)).To(BeEmpty(), raw)
		}
	})

	It("keeps the git check for an address that is no web page", func() {
		err := accessDenied([]string{"--", "https://github.com/o/r"}, "fatal: repository 'https://github.com/o/r/' not found")
		Expect(errs.Hints(err)).To(Equal([]string{"git ls-remote -- https://github.com/o/r"}))
	})
})
