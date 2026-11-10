package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("the lola binary with a user target spec that cannot be loaded", func() {
	var specFile string
	BeforeEach(func() {
		testenv.Isolate()
		dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "lola", "targets")
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		specFile = filepath.Join(dir, "quill.yaml")
		Expect(os.WriteFile(specFile, []byte("id: quill\nroots: {project: \"{project}\", user: \"{home}\"}\n"), 0o600)).To(Succeed())
	})

	run := func(args ...string) *gexec.Session {
		GinkgoHelper()
		session, err := gexec.Start(exec.Command(lolaBin, args...), GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		Eventually(session, "10s").Should(gexec.Exit(1))
		return session
	}

	It("exits 1 with one JSON error naming the file and offering hints under --json", func() {
		session := run("version", "--json")
		var doc struct {
			Error string   `json:"error"`
			Hints []string `json:"hints"`
		}
		Expect(json.Unmarshal(session.Err.Contents(), &doc)).To(Succeed(), string(session.Err.Contents()))
		Expect(doc.Error).To(ContainSubstring("quill.yaml"))
		Expect(doc.Hints).NotTo(BeEmpty())
		Expect(session.Out.Contents()).To(BeEmpty())
	})

	It("exits 1 with an error and hint lines otherwise", func() {
		session := run("version")
		Expect(string(session.Err.Contents())).To(And(HavePrefix("error: "), ContainSubstring("quill.yaml"), ContainSubstring("  hint: ")))
	})
})
