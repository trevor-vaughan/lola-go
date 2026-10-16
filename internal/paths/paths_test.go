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
		Expect(p.Store).To(Equal("/h/.local/share/lola/modules"))
		Expect(p.StoreLock).To(Equal("/h/.local/share/lola/.lock"))
		Expect(p.PluginData).To(Equal("/h/.local/share/lola/plugin-data"))
		Expect(p.Staging).To(Equal("/h/.local/share/lola/.staging"))
		Expect(p.Tmp).To(Equal("/h/.cache/lola/tmp"))
	})

	It("uses absolute XDG values", func() {
		p, err := paths.Resolve(env(map[string]string{"HOME": "/h", "XDG_CONFIG_HOME": "/c/", "XDG_DATA_HOME": "/d"}))
		Expect(err).NotTo(HaveOccurred())
		Expect(p.ConfigHome).To(Equal("/c"))
		Expect(p.Store).To(Equal("/d/lola/modules"))
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
