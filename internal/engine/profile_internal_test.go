package engine

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("desired", func() {
	It("records the layers each winning entry replaced, lowest first", func() {
		testenv.Isolate()
		a := manifest.Entry{Name: "a", Source: "/m/a"}
		a2 := manifest.Entry{Name: "a", Source: "/m/a2"}
		want, notices := desired([]Layer{
			{Name: "profile:default", Profile: true, Base: true, Entries: []manifest.Entry{a, {Name: "b", Source: "/m/b"}}},
			{Name: "profile:user:go", Profile: true, Entries: []manifest.Entry{a2}},
			{Name: "profile:user:review", Profile: true, Entries: []manifest.Entry{a2}},
		})
		Expect(want).To(HaveLen(2))
		Expect(want[0]).To(And(HaveField("Name", "a"), HaveField("From", "profile:user:review"), HaveField("Over", []string{"profile:default", "profile:user:go"})))
		Expect(want[1]).To(And(HaveField("Name", "b"), HaveField("From", "profile:default"), HaveField("Over", BeEmpty())))
		Expect(notices).To(ConsistOf(Notice{Level: LevelWarn, Text: "a: profile go overrides default (base) (/m/a2 over /m/a)"}),
			"the identical entry is silent")
	})
})
