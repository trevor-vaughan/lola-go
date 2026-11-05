package cli_test

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("lola install's staged run (cherry-pick design §7)", func() {
	var tmp, proj, fresh string
	var p paths.Paths
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(tmp, "proj")
		fresh = filepath.Join(tmp, "src", "fresh")
		writeTree(fresh, map[string]string{"lola.yml": "name: fresh\n", "commands/fresh.md": "Fresh.\n", "commands/other.md": "Other.\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		GinkgoT().Chdir(proj)
	})
	cachedFresh := func() bool {
		_, err := os.Stat(filepath.Join(p.Store, "fresh"))
		return err == nil
	}
	runDirs := func() []string {
		GinkgoHelper()
		es, err := os.ReadDir(p.Staging)
		if os.IsNotExist(err) {
			return nil
		}
		Expect(err).NotTo(HaveOccurred())
		var out []string
		for _, d := range es {
			if strings.HasPrefix(d.Name(), "run-") {
				out = append(out, d.Name())
			}
		}
		return out
	}
	installed := filepath.Join("proj", ".claude", "commands", "fresh.md")
	aborted := func(cli.ComponentPage) ([]string, error) { return nil, cli.ErrAborted }

	DescribeTable("a run that ends without Apply leaves the cache as it was and removes its staging",
		func(sp *scripted, extra ...string) {
			r := lolaP(sp, append([]string{"install", fresh, "--scope", "project"}, extra...)...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(cachedFresh()).To(BeFalse())
			Expect(runDirs()).To(BeEmpty())
			Expect(filepath.Join(tmp, installed)).NotTo(BeAnExistingFile())
		},
		Entry("cancelled at the component page", &scripted{components: []func(cli.ComponentPage) ([]string, error){aborted}}),
		Entry("Quit at the review", &scripted{components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewQuit}}),
		Entry("-n, applied at the review", &scripted{components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewApply}}, "-n"),
	)

	It("a refusal at Apply leaves the cache as it was", func() {
		writeTree(proj, map[string]string{".claude/commands/fresh.md": "mine\n"})
		r := lolaP(&scripted{components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewApply}},
			"install", fresh, "--scope", "project")
		Expect(r.code).To(Equal(1), r.err)
		Expect(r.err).To(ContainSubstring("fresh.md"))
		Expect(cachedFresh()).To(BeFalse())
		Expect(runDirs()).To(BeEmpty())
		b, err := os.ReadFile(filepath.Join(tmp, installed))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("mine\n"))
	})

	It("Apply caches the version the pickers fetched and installs its bytes, though the source changed before Apply", func() {
		edit := func(pg cli.ComponentPage) ([]string, error) {
			writeTree(fresh, map[string]string{"commands/fresh.md": "Changed.\n"})
			return keep()(pg)
		}
		r := lolaP(&scripted{components: []func(cli.ComponentPage) ([]string, error){edit}, review: []string{cli.ReviewApply}},
			"install", fresh, "--scope", "project")
		Expect(r.code).To(Equal(0), r.err)
		b, err := os.ReadFile(filepath.Join(tmp, installed))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("Fresh.\n"), "the bytes the pickers fetched, not the source's later ones")
		versions, err := os.ReadDir(filepath.Join(p.Store, "fresh", "versions"))
		Expect(err).NotTo(HaveOccurred())
		Expect(versions).To(HaveLen(1), "one fetch, the one reviewed")
		Expect(runDirs()).To(BeEmpty())
	})

	It("off a terminal, -n and a refusal leave the cache as it was, and the install caches what it applies", func() {
		run := func(extra ...string) result {
			return lola(append([]string{"install", fresh, "-C", proj, "-t", "claude-code"}, extra...)...)
		}
		r := run("-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(cachedFresh()).To(BeFalse())
		Expect(runDirs()).To(BeEmpty())

		writeTree(proj, map[string]string{".claude/commands/fresh.md": "mine\n"})
		Expect(run().code).To(Equal(1))
		Expect(cachedFresh()).To(BeFalse())
		Expect(runDirs()).To(BeEmpty())

		Expect(os.Remove(filepath.Join(tmp, installed))).To(Succeed())
		r = run()
		Expect(r.code).To(Equal(0), r.err)
		Expect(cachedFresh()).To(BeTrue())
		Expect(runDirs()).To(BeEmpty())
	})
})
