package paths_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

var _ = Describe("Resolve", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("uses $HOME-relative defaults when XDG vars are unset", func() {
		p, err := paths.Resolve(env(map[string]string{"HOME": "/h"}))
		Expect(err).NotTo(HaveOccurred())
		Expect(p.ConfigHome).To(Equal("/h/.config"))
		Expect(p.CacheHome).To(Equal("/h/.cache"))
		Expect(p.StateHome).To(Equal("/h/.local/state"))
		Expect(p.DataHome).To(Equal("/h/.local/share"))
		Expect(p.Config).To(Equal("/h/.config/lola/config.yaml"))
		Expect(p.UserManifest).To(Equal("/h/.config/lola/user.yml"))
		Expect(p.UserLedger).To(Equal("/h/.local/state/lola/lock.json"))
		Expect(p.UserLock).To(Equal("/h/.local/state/lola/.lock"))
		Expect(p.Locks).To(Equal("/h/.local/state/lola/locks"))
		Expect(p.Consent).To(Equal("/h/.local/state/lola/consent.json"))
		Expect(p.Store).To(Equal("/h/.cache/lola/modules"))
		Expect(p.StoreLock).To(Equal("/h/.cache/lola/.lock"))
		Expect(p.Staging).To(Equal("/h/.cache/lola/.staging"))
		Expect(p.Tmp).To(Equal("/h/.cache/lola/tmp"))
		Expect(p.UserModules).To(Equal("/h/.local/share/lola/user-modules"))
		Expect(p.PluginData).To(Equal("/h/.local/share/lola/plugin-data"))
		Expect(p.Probes).To(Equal("/h/.cache/lola/probes.json"))
	})

	It("searches only the absolute PATH entries for assistants", func() {
		p, err := paths.Resolve(env(map[string]string{"HOME": "/h", "PATH": "/usr/bin::bin:./tools:/opt/x/../y"}))
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Search).To(Equal([]string{"/usr/bin", "/opt/y"}))
	})

	It("uses absolute XDG values", func() {
		p, err := paths.Resolve(env(map[string]string{"HOME": "/h", "XDG_CONFIG_HOME": "/c/", "XDG_CACHE_HOME": "/k"}))
		Expect(err).NotTo(HaveOccurred())
		Expect(p.ConfigHome).To(Equal("/c"))
		Expect(p.Store).To(Equal("/k/lola/modules"))
	})

	It("ignores relative XDG values, as the XDG spec requires", func() {
		p, err := paths.Resolve(env(map[string]string{"HOME": "/h", "XDG_CONFIG_HOME": "rel/cfg", "XDG_STATE_HOME": "./s"}))
		Expect(err).NotTo(HaveOccurred())
		Expect(p.ConfigHome).To(Equal("/h/.config"))
		Expect(p.StateHome).To(Equal("/h/.local/state"))
	})

	It("rejects an unset HOME", func() {
		_, err := paths.Resolve(env(map[string]string{}))
		Expect(err).To(MatchError(ContainSubstring("HOME")))
	})

	It("rejects a relative HOME", func() {
		_, err := paths.Resolve(env(map[string]string{"HOME": "home"}))
		Expect(err).To(MatchError(ContainSubstring("absolute")))
	})
})
