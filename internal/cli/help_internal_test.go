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
