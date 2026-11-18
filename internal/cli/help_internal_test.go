package cli

import (
	"github.com/spf13/cobra"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("flag aliases", func() {
	BeforeEach(func() { testenv.Isolate() })

	// tree is a root with a persistent --user and a child whose --on has aliases.
	tree := func(aliases ...string) *cobra.Command {
		root := &cobra.Command{Use: "root"}
		root.PersistentFlags().Bool("user", false, "")
		child := &cobra.Command{Use: "child", Run: func(*cobra.Command, []string) {}}
		child.Flags().Bool("on", false, "")
		aliasFlags(child, map[string][]string{"on": aliases})
		root.AddCommand(child)
		return root
	}

	It("refuses an alias that names a flag the command inherits", func() {
		Expect(func() { checkFlagAliases(tree("enable")) }).NotTo(Panic())
		Expect(func() { checkFlagAliases(tree("enable", "user")) }).To(PanicWith("flag alias --user of root child is a flag root gives every command"))
		Expect(func() { checkFlagAliases(tree("help")) }).To(PanicWith("flag alias --help of root child is cobra's help flag"))
	})
})

var _ = Describe("help forms", func() {
	BeforeEach(func() { testenv.Isolate() })

	// cmd is a command with a value flag -t/--target, a bool -y, and cobra's help flag.
	cmd := func() *cobra.Command {
		root := &cobra.Command{Use: "root"}
		c := &cobra.Command{Use: "install", Run: func(*cobra.Command, []string) {}}
		c.Flags().StringP("target", "t", "", "")
		c.Flags().String("ref", "", "")
		c.Flags().BoolP("yes", "y", false, "")
		root.AddCommand(c)
		c.InitDefaultHelpFlag()
		return c
	}

	DescribeTable("longHelpAsked",
		func(args []string, want bool) { Expect(longHelpAsked(cmd(), args)).To(Equal(want)) },
		Entry("-h", []string{"install", "-h"}, false),
		Entry("--help", []string{"install", "--help"}, true),
		Entry("both", []string{"install", "-h", "--help"}, true),
		Entry("--help=true", []string{"install", "--help=true"}, true),
		Entry("cluster", []string{"install", "-yh"}, false),
		Entry("-t takes --help as its value", []string{"install", "-t", "--help", "-h"}, false),
		Entry("a cluster ending in -t takes the next", []string{"install", "-yt", "--help", "-h"}, false),
		Entry("-t with its value attached", []string{"install", "-tx", "--help"}, true),
		Entry("--ref takes the next", []string{"install", "--ref", "--help", "-h"}, false),
		Entry("--ref=v", []string{"install", "--ref=--help", "-h"}, false),
		Entry("after --", []string{"install", "-h", "--", "--help"}, false),
		Entry("non-ASCII shorthand letters stop the cluster", []string{"install", "-é", "--help"}, true),
	)

	DescribeTable("referenceURL",
		func(path []string, mod, ver, want string) {
			root := &cobra.Command{Use: "lola"}
			c := root
			for _, p := range path {
				sub := &cobra.Command{Use: p}
				c.AddCommand(sub)
				c = sub
			}
			Expect(referenceURL(c, mod, ver)).To(Equal(want))
		},
		Entry("root", nil, "github.com/o/r", "dev", "https://github.com/o/r/blob/main/docs/usage.md"),
		Entry("command", []string{"install"}, "github.com/o/r", "dev", "https://github.com/o/r/blob/main/docs/reference/install.md"),
		Entry("subcommand uses its top-level page", []string{"cache", "add"}, "github.com/o/r", "dev", "https://github.com/o/r/blob/main/docs/reference/cache.md"),
		Entry("release tag", []string{"install"}, "github.com/o/r", "v1.2.3", "https://github.com/o/r/blob/v1.2.3/docs/reference/install.md"),
		Entry("describe past a tag", []string{"install"}, "github.com/o/r", "v1.2.3-4-gabcdef0", "https://github.com/o/r/blob/main/docs/reference/install.md"),
		Entry("dirty", []string{"install"}, "github.com/o/r", "v1.2.3-dirty", "https://github.com/o/r/blob/main/docs/reference/install.md"),
		Entry("major-version suffix", []string{"install"}, "github.com/o/r/v2", "v2.0.0", "https://github.com/o/r/blob/v2.0.0/docs/reference/install.md"),
		Entry("no module path", []string{"install"}, "", "v1.2.3", ""),
	)
})
