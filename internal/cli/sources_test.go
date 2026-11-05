package cli_test

import (
	"archive/zip"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("git and archive sources", func() {
	var tmp, proj string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
	})

	It("adds a git ref to the cache and installs it", func() {
		work := filepath.Join(tmp, "work")
		writeTree(work, map[string]string{"commands/ship.md": "Ship\n"})
		for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "init"}, {"tag", "v1"}} {
			Expect(exec.Command("git", append([]string{"-C", work}, args...)...).Run()).To(Succeed())
		}
		Expect(exec.Command("git", "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "example.git")).Run()).To(Succeed())
		url := testenv.ServeGit(filepath.Join(tmp, "srv")) + "/example.git"

		r := lola("cache", "add", url, "--ref", "v1", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var mv map[string]any
		Expect(json.Unmarshal([]byte(r.out), &mv)).To(Succeed())
		Expect(mv["type"]).To(Equal("git"))
		Expect(mv["source"]).To(Equal(url))
		Expect(mv["heads"]).To(ConsistOf(HaveKeyWithValue("ref", "v1")))

		r = lola("install", "example", "--ref", "v1", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(proj, ".claude/commands/ship.md")).To(BeARegularFile())
	})

	It("checks and installs a local archive", func() {
		archive := filepath.Join(tmp, "example.zip")
		f, err := os.Create(archive)
		Expect(err).NotTo(HaveOccurred())
		zw := zip.NewWriter(f)
		w, err := zw.Create("example/commands/ship.md")
		Expect(err).NotTo(HaveOccurred())
		_, err = w.Write([]byte("Ship\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(zw.Close()).To(Succeed())
		Expect(f.Close()).To(Succeed())

		r := lola("check", archive, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(lola("cache", "ls", "--plain").out).NotTo(ContainSubstring("example"))
		r = lola("install", archive, "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✓ installed example (claude-code)"))
	})

	It("refuses a source with credentials, hinting at a credential helper", func() {
		r := lola("cache", "add", "https://ghp_s3cr3t@github.com/o/r.git")
		Expect(r.code).To(Equal(1))
		Expect(r.err).NotTo(ContainSubstring("s3cr3t"))
		Expect(r.err).To(ContainSubstring("hint: remove the credentials, query or fragment; let a git credential helper"))
	})
})
