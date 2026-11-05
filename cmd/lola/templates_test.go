package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"

	"github.com/trevor-vaughan/lola-go/internal/templating"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("the lola binary", func() {
	It("the built binary exits non-zero within Timeout plus 5 s on a template that loops without writing, and writes nothing", func() {
		tmp := testenv.Isolate()
		proj, src := filepath.Join(tmp, "proj"), filepath.Join(tmp, "src", "kit")
		// four loops of 64 over nothing: no write and no filter, so nothing inside the render
		// can stop it; only the process ending does
		loop := "{% assign l = 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx' | split: '' %}" +
			"{% for a in l %}{% for b in l %}{% for c in l %}{% for d in l %}{% for f in l %}{% endfor %}{% endfor %}{% endfor %}{% endfor %}{% endfor %}"
		for dir, files := range map[string]map[string]string{src: {"commands/spin.md.liquid": loop}, proj: {}} {
			for rel, body := range files {
				Expect(os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755)).To(Succeed())
				Expect(os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644)).To(Succeed())
			}
			Expect(os.MkdirAll(filepath.Join(dir, ".lola"), 0o755)).To(Succeed())
		}
		cmd := exec.Command(lolaBin, "install", src, "-t", "claude-code")
		cmd.Dir = proj
		limit := templating.DefaultLimits().Timeout + 5*time.Second
		start := time.Now()
		session, err := gexec.Start(cmd, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(session.Kill) // a binary that hangs must not outlive the spec
		Eventually(session).WithTimeout(limit).Should(gexec.Exit(1))
		Expect(time.Since(start)).To(BeNumerically("<", limit))
		Expect(string(session.Err.Contents())).To(ContainSubstring("commands/spin.md.liquid: took longer than 2s to render; lola stops a template after 2s"))
		Expect(filepath.Join(proj, ".claude")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(proj, ".lola", "lock.json")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(proj, ".lola", "project.yml")).NotTo(BeAnExistingFile())
	})
})
