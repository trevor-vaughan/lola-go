package cli

import (
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("noun commands' shared helpers", func() {
	// noun is a lola <noun> with the verbs add, rm and list, and items the words of have.
	noun := func(have ...string) (*cobra.Command, func(*cobra.Command, string) bool) {
		root := &cobra.Command{Use: "lola"}
		n := &cobra.Command{Use: "thing", SuggestionsMinimumDistance: 2}
		add := &cobra.Command{Use: "add", Aliases: []string{"get"}, RunE: func(*cobra.Command, []string) error { return nil }}
		rm := &cobra.Command{Use: "rm", Aliases: []string{"del"}, RunE: func(*cobra.Command, []string) error { return nil }}
		list := &cobra.Command{Use: "list", RunE: func(*cobra.Command, []string) error { return nil }}
		n.AddCommand(add, rm, list)
		root.AddCommand(n)
		return n, func(_ *cobra.Command, name string) bool { return slices.Contains(have, name) }
	}

	Describe("reservedWords", func() {
		It("is every verb name and alias and the extras, sorted, each once", func() {
			n, _ := noun()
			Expect(reservedWords(n, "show", "add")).To(Equal([]string{"add", "del", "get", "list", "rm", "show"}))
		})

		It("is only the extras for a noun with no verbs", func() {
			Expect(reservedWords(&cobra.Command{Use: "empty"}, "b", "a")).To(Equal([]string{"a", "b"}))
		})
	})

	Describe("verbWinsNote", func() {
		It("names the show that reaches an item named like the verb that ran", func() {
			n, exists := noun("add")
			add, _, err := n.Find([]string{"add"})
			Expect(err).NotTo(HaveOccurred())
			n.Root().SetArgs([]string{"thing", "add"})
			Expect(n.Root().Execute()).To(Succeed()) // sets CalledAs
			Expect(verbWinsNote(add, "thing", exists)).To(Equal([]string{"ran add; to show the thing named add: lola thing show add"}))
		})

		It("uses the word the verb was called by, an alias included", func() {
			n, exists := noun("get")
			n.Root().SetArgs([]string{"thing", "get"})
			Expect(n.Root().Execute()).To(Succeed())
			add := n.Commands()[0]
			Expect(verbWinsNote(add, "thing", exists)).To(Equal([]string{"ran get; to show the thing named get: lola thing show get"}))
		})

		It("is nil when the word names no item", func() {
			n, exists := noun("other")
			add, _, _ := n.Find([]string{"add"})
			n.Root().SetArgs([]string{"thing", "add"})
			Expect(n.Root().Execute()).To(Succeed())
			Expect(verbWinsNote(add, "thing", exists)).To(BeNil())
		})
	})

	Describe("nounRunE", func() {
		var shown []string
		var bared bool
		run := func(have []string, args ...string) error {
			testenv.Isolate()
			n, exists := noun(have...)
			shown, bared = nil, false
			a := &app{}
			n.RunE = a.nounRunE(nounSpec{noun: "thing", exists: exists,
				bare: func(*cobra.Command) error { bared = true; return nil },
				show: func(_ *cobra.Command, args []string) error { shown = args; return nil }})
			n.Args = cobra.ArbitraryArgs
			n.Root().SetArgs(append([]string{"thing"}, args...))
			return n.Root().Execute()
		}

		It("runs bare with no words", func() {
			Expect(run(nil)).To(Succeed())
			Expect(bared).To(BeTrue())
		})

		It("shows the words that name no verb, whether or not an item has them", func() {
			Expect(run(nil, "widget", "x")).To(Succeed())
			Expect(shown).To(Equal([]string{"widget", "x"}))
		})

		It("refuses a word near a verb that names no item, hinting the verb with the operands", func() {
			err := run(nil, "ad", "x")
			Expect(err).To(MatchError(`unknown thing or command "ad"`))
			Expect(errs.Hints(err)).To(Equal([]string{"did you mean: lola thing add x", "see them: lola thing list"}))
			Expect(shown).To(BeNil())
		})

		It("shows a word near a verb when it names an item", func() {
			Expect(run([]string{"ad"}, "ad")).To(Succeed())
			Expect(shown).To(Equal([]string{"ad"}))
		})
	})

	Describe("coachFlags", func() {
		// coached runs lola thing args against a noun whose add takes <src> [<name>] and --ref, rm takes one
		// <name>, and reports the refusal's message and hints.
		coached := func(args ...string) (string, []string) {
			testenv.Isolate()
			root := &cobra.Command{Use: "lola", SilenceErrors: true, SilenceUsage: true}
			n := &cobra.Command{Use: "thing", Args: cobra.ArbitraryArgs, RunE: func(*cobra.Command, []string) error { return nil }}
			add := &cobra.Command{Use: "add <src> [<name>]", Args: cobra.RangeArgs(1, 2), RunE: func(*cobra.Command, []string) error { return nil }}
			add.Flags().StringP("ref", "r", "", "")
			add.Flags().BoolP("dry-run", "n", false, "")
			rm := &cobra.Command{Use: "rm <name>", Args: cobra.ExactArgs(1), RunE: func(*cobra.Command, []string) error { return nil }}
			list := &cobra.Command{Use: "list", RunE: func(*cobra.Command, []string) error { return nil }}
			n.AddCommand(add, rm, list)
			root.AddCommand(n)
			a := &app{argv: append([]string{"thing"}, args...)}
			a.coachFlags(n, nounSpec{exists: func(*cobra.Command, string) bool { return false }}, map[string]oldFlag{"add": {verb: "add", takesValue: true}, "remove": {verb: "rm"}})
			root.SetArgs(a.argv)
			err := root.Execute()
			Expect(err).To(HaveOccurred())
			return err.Error(), errs.Hints(err)
		}

		It("hints an old flag's verb with its value first, then every word, parsed or not, and the flags it defines", func() {
			msg, hints := coached("acme", "--add", "U", "--ref", "main", "-n")
			Expect(msg).To(Equal("--add is not a flag of lola thing"))
			Expect(hints).To(Equal([]string{"lola thing add U acme --ref main -n"}))
			_, hints = coached("--remove", "a")
			Expect(hints).To(Equal([]string{"lola thing rm a"}))
		})

		It("gives a second old flag its own hint, and drops flags its verb lacks", func() {
			_, hints := coached("a", "--add", "U", "--remove", "--ref", "main")
			Expect(hints).To(Equal([]string{"lola thing add U a --ref main", "lola thing rm a"}))
		})

		It("offers usage and the list, not a run, when the verb would refuse the operands", func() {
			_, hints := coached("--remove")
			Expect(hints).To(Equal([]string{"usage: lola thing rm <name>", "find one: lola thing list"}))
			_, hints = coached("a", "b", "--remove")
			Expect(hints).To(Equal([]string{"usage: lola thing rm <name>", "find one: lola thing list"}))
		})

		It("gives a sibling verb's flag as that verb's usage with the flag and its value, never the operands", func() {
			msg, hints := coached("rm", "a", "--ref", "main")
			Expect(msg).To(Equal("--ref applies to lola thing add"))
			Expect(hints).To(Equal([]string{"usage: lola thing add <src> [<name>] --ref main"}))
			_, hints = coached("rm", "a", "--ref", "a b")
			Expect(hints).To(Equal([]string{"usage: lola thing add <src> [<name>] --ref " + errs.ShellJoin([]string{"a b"})}),
				"the value quoted for the shell")
		})

		It("expands a shorthand cluster, a value-taking letter taking the rest of the token or the next", func() {
			_, hints := coached("--add", "U", "-nr", "main")
			Expect(hints).To(Equal([]string{"lola thing add U -n -r main"}))
			_, hints = coached("--add", "U", "-rmain")
			Expect(hints).To(Equal([]string{"lola thing add U -r main"}))
			_, hints = coached("--add", "U", "-xn")
			Expect(hints).To(Equal([]string{"lola thing add U -n"}), "a letter no verb takes is dropped")
		})
	})

	Describe("andJoinWords", func() {
		It("joins as prose", func() {
			Expect(andJoinWords(nil)).To(Equal(""))
			Expect(andJoinWords([]string{"a"})).To(Equal("a"))
			Expect(andJoinWords([]string{"a", "b"})).To(Equal("a and b"))
			Expect(andJoinWords([]string{"a", "b", "c"})).To(Equal("a, b and c"))
		})
	})
})
