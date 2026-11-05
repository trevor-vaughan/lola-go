package engine_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("the profiles a template sees", func() {
	It("profiles lists default first at user scope whenever default.yml exists", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e := engine.New(p)
		src := filepath.Join(tmp, "src", "kit")
		writeTree(src, map[string]string{"commands/ship.md.liquid": "{{ profiles | join: ',' }}\n"})
		writeTree(p.UserProfiles, map[string]string{"work.yml": "version: 1\nmodules: []\n"})
		user := engine.Scope{Kind: target.User}
		led, err := e.Ledger(user)
		Expect(err).NotTo(HaveOccurred())
		led.Profiles = []ledger.Active{{Name: "user:work", By: ledger.ByOn}}
		Expect(led.Save(p.UserLedger)).To(Succeed())
		home, err := os.UserHomeDir()
		Expect(err).NotTo(HaveOccurred())
		shipped := func() string {
			GinkgoHelper()
			_, err := e.Install(context.Background(), user, tmp, []engine.InstallRequest{{Arg: src}}, engine.Options{Targets: []string{"claude-code"}})
			Expect(err).NotTo(HaveOccurred())
			b, err := os.ReadFile(filepath.Join(home, ".claude", "commands", "ship.md"))
			Expect(err).NotTo(HaveOccurred())
			return string(b)
		}
		Expect(shipped()).To(Equal("user:work\n"), "no default.yml")
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\nmodules: []\n"})
		Expect(shipped()).To(Equal("default,user:work\n"))
	})
})
