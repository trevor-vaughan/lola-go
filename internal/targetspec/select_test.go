package targetspec_test

import (
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/targetspec"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Select", func() {
	BeforeEach(func() { testenv.Isolate() })

	spec := targetspec.Spec{Eras: []targetspec.Era{{Since: "1.0"}, {Since: "2.0"}, {Since: "2.5"}}}

	DescribeTable("picks the era a version selects",
		func(version string, era int, before bool) {
			got, gotBefore := spec.Select(version)
			Expect(got).To(Equal(era))
			Expect(gotBefore).To(Equal(before))
		},
		Entry("undeclared is the newest", "", 2, false),
		Entry("below the first era is the first, flagged", "0.9", 0, true),
		Entry("the first era's own version", "1.0", 0, false),
		Entry("a patch release of the first era", "1.9.9", 0, false),
		Entry("a bare major", "2", 1, false),
		Entry("a v-prefixed version", "v2.0.0", 1, false),
		Entry("just below the last era", "2.4", 1, false),
		Entry("the last era's own version", "2.5", 2, false),
		Entry("far above the last era", "9", 2, false),
	)
})

var _ = Describe("Canonical", func() {
	BeforeEach(func() { testenv.Isolate() })

	DescribeTable("turns a version into the form semver compares",
		func(in, want string) { Expect(targetspec.Canonical(in)).To(Equal(want)) },
		Entry("a bare major", "2", "v2"),
		Entry("a v-prefixed minor", "v2.1", "v2.1"),
		Entry("a full version", "1.2.3", "v1.2.3"),
		Entry("a non-version", "x", ""),
		Entry("empty", "", ""),
	)
})

var _ = Describe("FileProbe.Expand", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("joins the variable's value with the relative path", func() {
		f := targetspec.FileProbe{Path: "{home}/.t/v.json"}
		Expect(f.Expand(targetspec.RootVars{Home: "/h"})).To(Equal(filepath.Join("/h", ".t", "v.json")))
	})

	It("uses the variable the path names", func() {
		f := targetspec.FileProbe{Path: "{data_home}/t/v"}
		Expect(f.Expand(targetspec.RootVars{Home: "/h", DataHome: "/d"})).To(Equal(filepath.Join("/d", "t", "v")))
	})
})
