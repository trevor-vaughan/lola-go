package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("profiles", func() {
	var tmp, a, c string
	var p paths.Paths
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		a, c = filepath.Join(tmp, "mods", "a"), filepath.Join(tmp, "mods", "c")
		writeTree(a, map[string]string{"lola.yml": "name: a\n", "commands/a.md": "A\n"})
		writeTree(c, map[string]string{"lola.yml": "name: c\n", "commands/c.md": "C\n"})
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: c, source: " + c + "}\n"})
		writeTree(filepath.Dir(p.UserManifest), map[string]string{"user.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: " + a + "}\n"})
	})

	It("list names the layer each module comes from", func() {
		r := lola("list", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`SCOPE\s+MODULE\s+TARGET\s+STATUS\s+RULES\s+SOURCE\s+FROM\n`))
		Expect(r.out).To(MatchRegexp(`user\s+c\s+claude-code\s+⚠ not installed\s+on\s+` + c + `\s+profile:default\n`))

		r = lola("sync", "--user")
		Expect(r.code).To(Equal(0), r.err)
		r = lola("list", "--user")
		Expect(r.out).To(MatchRegexp(`user\s+a\s+claude-code\s+✓ ok\s+on\s+` + a + `\s+user.yml\n`))
		Expect(r.out).To(MatchRegexp(`user\s+c\s+claude-code\s+✓ ok\s+on\s+` + c + `\s+profile:default\n`))
		Expect(lola("list", "--user", "--plain").out).To(Equal("a\tclaude-code\tok\ton\t" + a + "\tuser\tuser.yml\n" +
			"c\tclaude-code\tok\ton\t" + c + "\tuser\tprofile:default\n"))
		var v struct {
			Modules []map[string]any `json:"modules"`
			Notices []any            `json:"notices"`
		}
		Expect(json.Unmarshal([]byte(lola("list", "--user", "--json").out), &v)).To(Succeed())
		Expect(v.Modules).To(ContainElement(And(HaveKeyWithValue("name", "c"), HaveKeyWithValue("from", "profile:default"))))
		Expect(v.Notices).NotTo(BeNil())
		Expect(v.Notices).To(BeEmpty())
	})

	It("uninstall refuses a module default wants, saying how to drop it", func() {
		Expect(lola("sync", "--user").code).To(Equal(0))
		r := lola("uninstall", "--user", "c", "-y")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: c is wanted by profile default\n"))
		Expect(r.err).To(ContainSubstring("hint: remove it from " + filepath.Join(p.UserProfiles, "default.yml") + ", which is always on at user scope"))
		Expect(lola("uninstall", "--user", "a", "-y").code).To(Equal(0))
	})

	It("list warns about an active profile that no longer exists", func() {
		led := ledger.New()
		led.Profiles = []ledger.Active{{Name: "user:gone", By: ledger.ByOn}}
		Expect(led.Save(p.UserLedger)).To(Succeed())
		r := lola("list", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("⚠ profile user:gone is on but no longer exists → lola profile --user user:gone --off\n"))
		var v struct{ Notices []map[string]string }
		Expect(json.Unmarshal([]byte(lola("list", "--user", "--json").out), &v)).To(Succeed())
		Expect(v.Notices).To(ConsistOf(HaveKeyWithValue("text", "profile user:gone is on but no longer exists → lola profile --user user:gone --off")))
	})
})
