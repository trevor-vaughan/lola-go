package migrate_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/migrate"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("ParseRootMaps", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("reads OLD=NEW, keeping OLD without a trailing separator and NEW clean", func() {
		got, err := migrate.ParseRootMaps([]string{"/Users/alice/=/home/alice/./x", `C:\Users\bob\=/home/bob`})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]migrate.RootMap{{Old: "/Users/alice", New: "/home/alice/x"}, {Old: `C:\Users\bob`, New: "/home/bob"}}))
	})

	DescribeTable("refuses a map it cannot read, saying exactly what is wrong",
		func(arg, want string) {
			_, err := migrate.ParseRootMaps([]string{arg})
			Expect(err).To(MatchError(want))
		},
		Entry("no =", "/Users/alice", `--map-root "/Users/alice" is not OLD=NEW: write the root the Python lola's project paths start with, =, and where it is on this machine, such as --map-root /Users/alice=/home/alice`),
		Entry("no new root", "/Users/alice=", `--map-root "/Users/alice=" is not OLD=NEW: write the root the Python lola's project paths start with, =, and where it is on this machine, such as --map-root /Users/alice=/home/alice`),
		Entry("a relative old root", "alice=/home/alice", `--map-root "alice=/home/alice": "alice" is not an absolute path`),
		Entry("a relative new root", "/Users/alice=home", `--map-root "/Users/alice=home": "home" is not an absolute path on this machine`),
		Entry("the root", "/=/home/me", `--map-root "/=/home/me" would move every path: name the directory the Python lola's project paths start with, such as /Users/alice`),
		Entry("a Windows drive", `C:\=/home/me`, `--map-root "C:\\=/home/me" would move every path: name the directory the Python lola's project paths start with, such as /Users/alice`),
	)
})

var _ = Describe("ParseTargetMaps", func() {
	var e *engine.Engine
	BeforeEach(func() {
		testenv.Isolate()
		e = newEngine()
	})

	It("reads OLD=NEW[,NEW], each target once", func() {
		got, err := migrate.ParseTargetMaps(e, []string{"claude-code=opencode,gemini-cli,opencode", "cursor=claude-code"})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(map[string][]string{"claude-code": {"opencode", "gemini-cli"}, "cursor": {"claude-code"}}))
	})

	DescribeTable("refuses a map it cannot read, saying exactly what is wrong",
		func(args []string, want string) {
			_, err := migrate.ParseTargetMaps(e, args)
			Expect(err).To(MatchError(want))
		},
		Entry("no =", []string{"claude-code"}, `--map-target "claude-code" is not OLD=NEW[,NEW]: write the Python lola's assistant, =, and the assistants to install its modules for, such as --map-target claude-code=opencode`),
		Entry("no target", []string{"claude-code="}, `--map-target "claude-code=" is not OLD=NEW[,NEW]: write the Python lola's assistant, =, and the assistants to install its modules for, such as --map-target claude-code=opencode`),
		Entry("a target lola does not have", []string{"claude-code=vim"}, `--map-target "claude-code=vim": "vim" is not an assistant lola installs for; write claude-code, gemini-cli, opencode`),
		Entry("an empty name in the list", []string{"claude-code=opencode,"}, `--map-target "claude-code=opencode,": "" is not an assistant lola installs for; write claude-code, gemini-cli, opencode`),
		Entry("a bad target after a good one for the same assistant", []string{"claude-code=opencode", "claude-code=vim"}, `--map-target "claude-code=vim": "vim" is not an assistant lola installs for; write claude-code, gemini-cli, opencode`),
		Entry("a bad target before a good one for the same assistant", []string{"claude-code=vim", "claude-code=opencode"}, `--map-target "claude-code=vim": "vim" is not an assistant lola installs for; write claude-code, gemini-cli, opencode`),
		Entry("an assistant given twice", []string{"claude-code=opencode", "claude-code=gemini-cli"}, `--map-target names claude-code twice: give it once, with every assistant after =, such as --map-target claude-code=opencode,gemini-cli`),
	)
})
