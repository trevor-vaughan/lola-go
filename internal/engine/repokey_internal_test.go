package engine

import (
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Pick-P2-a: a clone git cannot read", func() {
	var e *Engine
	var proj string
	BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = New(p)
		proj = filepath.Join(tmp, "r")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
	})
	// section resolves proj's section, which must be its path key, and the notice that says why,
	// which the run takes once.
	section := func() []Notice {
		GinkgoHelper()
		sc := Scope{Kind: target.Project, ProjectDir: proj}
		w, err := e.scopeSection(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(w).To(Equal(config.Project(proj)))
		notes := e.keyNotices(sc)
		Expect(e.keyNotices(sc)).To(BeEmpty(), "once per run")
		return notes
	}

	It("Pick-P2-a: keys picks by path when git refuses the repository as someone else's, hinting the command that trusts it", func() {
		out, err := exec.Command("git", "-C", proj, "init", "-q").CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		out, err = exec.Command("git", "-C", proj, "remote", "add", "origin", "https://example.com/o/r").CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		GinkgoT().Setenv("GIT_TEST_ASSUME_DIFFERENT_OWNER", "1")
		Expect(section()).To(Equal([]Notice{{Level: LevelWarn, Text: "git could not read the repository of " + proj +
			": fatal: detected dubious ownership in repository at '" + proj + "'; picks for this project are keyed by its path → git config --global --add safe.directory " + proj}}))
	})

	It("Pick-P2-a: keys picks by path when git cannot read the clone, naming git's reason", func() {
		Expect(os.WriteFile(filepath.Join(proj, ".git"), []byte("gitdir: nowhere\n"), 0o644)).To(Succeed())
		Expect(section()).To(Equal([]Notice{{Level: LevelWarn, Text: "git could not read the repository of " + proj +
			": fatal: not a git repository: " + filepath.Join(proj, "nowhere") + "; picks for this project are keyed by its path"}}))
	})
})
