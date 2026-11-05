package source

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("git environment and listing", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("removes repository-selecting variables, keeps the rest and turns prompts off", func() {
		env := gitEnv([]string{"GIT_DIR=/x/.git", "GIT_WORK_TREE=/x", "GIT_INDEX_FILE=i", "GIT_OBJECT_DIRECTORY=o",
			"GIT_ALTERNATE_OBJECT_DIRECTORIES=a", "GIT_COMMON_DIR=c", "GIT_NAMESPACE=n", "GIT_CEILING_DIRECTORIES=/",
			"GIT_CONFIG_PARAMETERS='core.x=y'", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.x", "GIT_CONFIG_VALUE_0=y",
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
		files, err := listFiles(context.Background(), dir, listGit)
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
		Expect(allocated(func() { _, err = listFiles(context.Background(), GinkgoT().TempDir(), listGit) })).To(BeNumerically("<", 2<<20))
		Expect(err).To(MatchError(ContainSubstring("git ls-files")))
		Expect(len(err.Error())).To(BeNumerically("<", 2*maxStderr))
	})

	It("does not print the directory git ls-files ran in", func() {
		root := GinkgoT().TempDir()
		_, err := listFiles(context.Background(), root, listGit)
		Expect(err).To(MatchError(ContainSubstring("git ls-files")))
		Expect(err.Error()).NotTo(ContainSubstring(root))
	})
})
