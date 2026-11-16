package cli

import (
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("the progress line's pace", func() {
	BeforeEach(func() { testenv.Isolate() })
	line := func(p pace) (*progressLine, *gbytes.Buffer) {
		out := gbytes.NewBuffer()
		return &progressLine{out: out, width: func() int { return 0 }, pace: p, since: time.Now()}, out
	}

	It("shows nothing of a run that ends before the delay", func() {
		p, out := line(pace{delay: time.Hour})
		p.report("fetching kit (1/2)")
		p.report("fetching other (2/2)")
		p.hold("", false)
		Consistently(out.Contents, "50ms").Should(BeEmpty())
	})

	It("draws the step waiting when the delay ends, the latest one", func() {
		p, out := line(pace{delay: 20 * time.Millisecond})
		p.report("fetching kit (1/2)")
		p.report("fetching other (2/2)")
		Eventually(out.Contents).Should(Equal([]byte(clearLine + "… fetching other (2/2)")))
		p.hold("", false)
		Expect(string(out.Contents())).To(HaveSuffix(clearLine))
	})

	It("draws no more often than every, and a cleared line drops the step waiting", func() {
		p, out := line(pace{every: time.Hour})
		p.report("fetching kit (1/2)")
		p.report("rendering kit (1/2)")
		p.hold("", false)
		Consistently(out.Contents, "50ms").Should(Equal([]byte(clearLine + "… fetching kit (1/2)" + clearLine)))
	})

	It("drops the step waiting when held, and waits out the delay again once resumed", func() {
		p, out := line(pace{delay: time.Hour, every: time.Hour})
		p.since = time.Now().Add(-2 * time.Hour)
		p.report("fetching kit (1/2)")
		p.report("rendering kit (1/2)")
		Expect(out.Contents()).To(Equal([]byte(clearLine + "… fetching kit (1/2)")))
		p.hold("running pre-install script ci/x.sh (project) for kit on claude-code", false)
		p.since = time.Now().Add(-2 * time.Hour) // a long question
		p.resume()
		p.report("rendering other (2/2)")
		Consistently(out.Contents, "50ms").Should(Equal([]byte(clearLine + "… fetching kit (1/2)" + clearLine)))
	})

	It("prints a kept step at once as a line of its own, and draws nothing until the next step", func() {
		p, out := line(pace{every: 20 * time.Millisecond})
		p.report("fetching kit (1/2)")
		p.report("rendering kit (1/2)")
		p.hold("fetching other (2/2) from git@host:acme/other.git", true)
		Consistently(out.Contents, "50ms").Should(Equal([]byte(clearLine + "… fetching kit (1/2)" + clearLine +
			"… fetching other (2/2) from git@host:acme/other.git\n")))
	})

	It("cuts a step to the terminal's width at each draw, by display width, and to 80 when it is unknown", func() {
		p, out := line(pace{})
		width := 12
		p.width = func() int { return width }
		p.report("fetching 漢字漢字漢字漢字")
		Expect(string(out.Contents())).To(Equal(clearLine + "… fetching…"))
		width = 0
		p.report(strings.Repeat("x", 100))
		Expect(string(out.Contents())).To(HaveSuffix(clearLine + "… " + strings.Repeat("x", 76) + "…"))
	})
})
