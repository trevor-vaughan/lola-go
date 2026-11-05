package source_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("CheckReachable", func() {
	var tmp, staging string
	ctx := context.Background()
	BeforeEach(func() {
		tmp = testenv.Isolate()
		staging = filepath.Join(tmp, "staging")
	})
	check := func(url, ref, commit string) error {
		return source.CheckReachable(ctx, source.Spec{Kind: source.KindGit, Location: url, Ref: ref}, commit, staging)
	}
	expectNothingStaged := func() {
		entries, _ := os.ReadDir(staging)
		Expect(entries).To(BeEmpty())
	}

	It("accepts a commit reachable from its branch, its tag or the default branch", func() {
		r := newRemote(tmp, map[string]string{"commands/a.md": "A"})
		first := r.rev("HEAD")
		git(r.work, "tag", "v1")
		r.commit(map[string]string{"commands/a.md": "B"})
		git(r.work, "push", "-q", "--tags", r.bare)
		for _, ref := range []string{"main", "v1", ""} {
			Expect(check(r.url, ref, first)).To(Succeed(), "ref %q", ref)
		}
		expectNothingStaged()
	})

	It("refuses a commit reachable only from another branch", func() {
		r := newRemote(tmp, map[string]string{"commands/a.md": "A"})
		git(r.work, "checkout", "-q", "-b", "unreviewed")
		other := r.commit(map[string]string{"commands/a.md": "evil"})
		err := check(r.url, "main", other)
		Expect(err).To(MatchError("commit " + other + " is not on main at " + r.url))
		expectNothingStaged()
		err = check(r.url, "", other)
		Expect(err).To(MatchError("commit " + other + " is not on the default branch at " + r.url))
	})

	It("follows merges: a merged commit is on the ref, a later one on its branch is not", func() {
		r := newRemote(tmp, map[string]string{"commands/a.md": "A"})
		git(r.work, "checkout", "-q", "-b", "side")
		side := r.commit(map[string]string{"commands/b.md": "B"})
		git(r.work, "checkout", "-q", "main")
		r.commit(map[string]string{"commands/c.md": "C"})
		git(r.work, "merge", "-q", "--no-edit", "side")
		git(r.work, "push", "-q", "--all", r.bare)
		Expect(check(r.url, "main", side)).To(Succeed(), "merged into main")
		git(r.work, "checkout", "-q", "-b", "later", "side~0")
		later := r.commit(map[string]string{"commands/d.md": "D"})
		Expect(check(r.url, "main", later)).To(MatchError(ContainSubstring("is not on main")))
	})

	It("skips the check when the ref is the commit itself", func() {
		commit := "0123456789abcdef0123456789abcdef01234567"
		Expect(check("https://127.0.0.1:1/nowhere.git", commit, commit)).To(Succeed())
	})

	It("refuses a hostile location or ref before git runs", func() {
		pwned := filepath.Join(tmp, "PWNED")
		commit := "0123456789abcdef0123456789abcdef01234567"
		Expect(check("--upload-pack=touch "+pwned, "main", commit)).To(MatchError(ContainSubstring("starts with '-'")))
		Expect(check("https://example.com/x.git", "--upload-pack=touch "+pwned, commit)).To(MatchError(ContainSubstring("starts with '-'")))
		Expect(pwned).NotTo(BeAnExistingFile())
	})

	It("refuses a ref that names both a branch and a tag", func() {
		r := newRemote(tmp, map[string]string{"commands/a.md": "A"})
		git(r.work, "tag", "main")
		git(r.work, "push", "-q", "--tags", r.bare)
		Expect(check(r.url, "main", r.rev("HEAD"))).To(MatchError(ContainSubstring("has both a branch and a tag named main")))
	})
})
