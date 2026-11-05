package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Pick-P2-f: lola uninstall clears the module's picks and says how to restore them", func() {
	var tmp, proj, mod, review string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, mod = filepath.Join(tmp, "proj"), filepath.Join(tmp, "example")
		review = filepath.Join(proj, ".claude", "commands", "test.md")
		writeTree(mod, map[string]string{"commands/ship.md": "Ship\n", "commands/test.md": "Test\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
	})

	It("prints the cleared picks with a restore command that applies them again after a reinstall", func() {
		Expect(lola("install", mod, "-C", proj, "--skip", "command/test").code).To(Equal(0))
		r := lola("uninstall", "example", "-C", proj, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ example: picks cleared: command/test (restore: lola install -C " + proj + " example --skip command/test)"))

		Expect(lola("install", mod, "-C", proj).code).To(Equal(0))
		Expect(review).To(BeARegularFile(), "a plain install installs everything: the pick is gone")
		Expect(lola("uninstall", "example", "-C", proj, "-y").code).To(Equal(0))

		Expect(lola("install", mod, "-C", proj, "--skip", "command/test").code).To(Equal(0))
		r = lola("uninstall", "example", "-C", proj, "-y", "--json")
		var out struct {
			Cleared []struct {
				Module, Restore string
				Skip            []string
			} `json:"picks_cleared"`
		}
		Expect(json.Unmarshal([]byte(r.out), &out)).To(Succeed(), r.out)
		Expect(out.Cleared).To(HaveLen(1))
		Expect(lola("install", mod, "-C", proj).code).To(Equal(0))
		r = runHint(out.Cleared[0].Restore + " -y") // the user adds -y: the narrowing install asks before deleting
		Expect(r.code).To(Equal(0), r.err)
		Expect(review).NotTo(BeARegularFile(), "the restore command re-applied the skip")
	})

	It("lists an empty picks_cleared when the module had none, and writes nothing under -n", func() {
		Expect(lola("install", mod, "-C", proj).code).To(Equal(0))
		r := lola("uninstall", "example", "-C", proj, "-y", "--json")
		Expect(r.out).To(ContainSubstring(`"picks_cleared": []`))

		Expect(lola("install", mod, "-C", proj, "--skip", "command/test").code).To(Equal(0))
		selections := filepath.Join(tmp, "config", "lola", "selections.yaml")
		before, err := os.ReadFile(selections)
		Expect(err).NotTo(HaveOccurred())
		r = lola("uninstall", "example", "-C", proj, "-n")
		Expect(r.out).To(ContainSubstring("example: picks would be cleared: command/test"))
		Expect(os.ReadFile(selections)).To(Equal(before))
	})
})
