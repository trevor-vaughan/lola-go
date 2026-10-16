package cli_test

import (
	"encoding/json"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("help, errors and output contract", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("piped help shows the full tree with the preamble", func() {
		for _, args := range [][]string{{"-h"}, {"help"}, {"help", "--long"}} {
			r := lola(args...)
			Expect(r.code).To(Equal(0), strings.Join(args, " "))
			Expect(r.out).To(HavePrefix("GLOBAL FLAGS\n"))
			Expect(r.out).To(ContainSubstring("--json"))
			Expect(r.out).To(ContainSubstring("OUTPUT CONTRACT"))
			Expect(r.out).To(ContainSubstring("ENVIRONMENT"))
			Expect(r.out).To(ContainSubstring("== lola version =="))
			Expect(r.out).To(ContainSubstring("== lola completion =="))
		}
	})

	It("help --short prints concise grouped help with aliases inline", func() {
		r := lola("help", "--short")
		Expect(r.code).To(Equal(0))
		Expect(r.out).NotTo(ContainSubstring("GLOBAL FLAGS"))
		Expect(r.out).To(ContainSubstring("Additional:"))
		Expect(r.out).To(MatchRegexp(`(?m)^  version \(ver\)\s+Print lola's version$`))
	})

	It("help for one command resolves aliases", func() {
		r := lola("help", "ver", "--short")
		Expect(r.code).To(Equal(0))
		Expect(r.out).To(HavePrefix("Print lola's version"))
		Expect(r.out).To(ContainSubstring("Aliases:\n  version, ver"))
	})

	It("rejects an unknown help topic and both modes at once", func() {
		Expect(lola("help", "nope").err).To(ContainSubstring(`no help topic "nope"`))
		Expect(lola("help", "--long", "--short").code).To(Equal(1))
	})

	It("coaches unknown commands with suggestions", func() {
		r := lola("verzion")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: unknown command \"verzion\" for \"lola\"\n  hint: did you mean: lola version\n  hint: see: lola --help\n"))
	})

	It("prints errors as one JSON line under --json", func() {
		r := lola("verzion", "--json")
		Expect(r.code).To(Equal(1))
		Expect(strings.Count(r.err, "\n")).To(Equal(1))
		var v map[string]any
		Expect(json.Unmarshal([]byte(r.err), &v)).To(Succeed())
		Expect(v["error"]).To(Equal(`unknown command "verzion" for "lola"`))
		Expect(v["hints"]).To(ContainElement("see: lola --help"))
	})

	It("rejects --json with --plain and --user with -C", func() {
		Expect(lola("version", "--json", "--plain").err).To(ContainSubstring("--json and --plain cannot be combined"))
		Expect(lola("version", "--user", "-C", ".").err).To(ContainSubstring("--user cannot be combined with -C"))
	})

	It("prints the version in every output mode", func() {
		Expect(lola("version").out).To(MatchRegexp(`^lola dev \(go1\.\d+`))
		Expect(lola("version", "--plain").out).To(MatchRegexp("^dev\tgo1"))
		var v map[string]string
		Expect(json.Unmarshal([]byte(lola("ver", "--json").out), &v)).To(Succeed())
		Expect(v).To(HaveKeyWithValue("version", "dev"))
	})

	It("prints completion scripts and refuses unknown shells", func() {
		for _, sh := range []string{"bash", "zsh", "fish", "powershell"} {
			r := lola("completion", sh)
			Expect(r.code).To(Equal(0), sh)
			Expect(r.out).To(ContainSubstring("lola"), sh)
		}
		Expect(lola("completion", "tcsh").code).To(Equal(1))
	})
})
