package cli_test

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("help, errors and output contract", func() {
	BeforeEach(func() { testenv.Isolate() })

	// find is the live command at path.
	find := func(path ...string) *cobra.Command {
		root, err := cli.NewRootCmd(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		c, _, err := root.Find(path)
		Expect(err).NotTo(HaveOccurred())
		return c
	}
	const docs = "https://github.com/trevor-vaughan/lola-go/blob/main/docs/"

	It("-h prints the short description, usage, examples and the command's own flags", func() {
		c := find("install")
		r := lola("install", "-h")
		Expect(r.code).To(Equal(0))
		Expect(r.out).To(HavePrefix(c.Short + "\n\nUsage:\n"))
		Expect(r.out).To(ContainSubstring("Examples:\n"))
		Expect(r.out).To(MatchRegexp(`Flags:\n(.*\n)*      --strict `))
		Expect(r.out).NotTo(ContainSubstring("Global Flags:"))
		Expect(r.out).NotTo(ContainSubstring("Reference:"))
		Expect(r.out).To(HaveSuffix("\nMore: lola install --help\n"))
	})

	It("--help prints the long description, the full usage and the reference link", func() {
		c := find("install")
		r := lola("install", "--help")
		Expect(r.code).To(Equal(0))
		Expect(r.out).To(HavePrefix(strings.TrimSpace(c.Long) + "\n\nUsage:\n"))
		Expect(strings.Count(r.out, "Global Flags:")).To(Equal(1))
		Expect(r.out).To(HaveSuffix("\nReference: " + docs + "reference/install.md\n"))
		Expect(lola("cache", "add", "--help").out).To(HaveSuffix("\nReference: " + docs + "reference/cache.md\n"))
	})

	It("lola help <cmd> is --help and help --short <cmd> is -h, aliases resolved", func() {
		Expect(lola("help", "install").out).To(Equal(lola("install", "--help").out))
		Expect(lola("help", "--short", "add").out).To(Equal(lola("install", "-h").out))
		Expect(lola("help", "cache", "add").out).To(Equal(lola("cache", "add", "--help").out))
	})

	It("a flag's value is never read as --help, and nothing after -- is a flag", func() {
		short := lola("install", "-h").out
		Expect(lola("install", "-yh").out).To(Equal(short))
		Expect(lola("install", "-t", "--help", "-h").out).To(Equal(short))
		Expect(lola("install", "-h", "--", "--help").out).To(Equal(short))
		Expect(lola("install", "-h", "--help").out).To(Equal(lola("install", "--help").out))
	})

	It("the root's long help adds the environment; bare lola prints -h", func() {
		long := lola("--help").out
		Expect(long).To(ContainSubstring("\nEnvironment:\n"))
		Expect(long).To(ContainSubstring("XDG_CACHE_HOME"))
		Expect(long).To(HaveSuffix("\nReference: " + docs + "usage.md\n"))
		Expect(lola("help").out).To(Equal(long))
		short := lola("-h").out
		Expect(short).NotTo(ContainSubstring("Environment:"))
		Expect(lola([]string{}...).out).To(Equal(short))
	})

	It("piped help is not the tree; help --all is, with the global flags once", func() {
		Expect(lola("help").out).NotTo(ContainSubstring("== lola install =="))
		r := lola("help", "--all")
		Expect(r.code).To(Equal(0))
		Expect(r.out).To(ContainSubstring("== lola install ==\n"))
		Expect(r.out).To(ContainSubstring("== lola cache add ==\n"))
		Expect(r.out).To(ContainSubstring("== lola completion ==\n"))
		Expect(len(regexp.MustCompile(`(?m)^\s+--no-pager\s`).FindAllString(r.out, -1))).To(Equal(1))
		Expect(r.out).NotTo(ContainSubstring("OUTPUT CONTRACT"))
		r = lola("help", "--all", "cache")
		Expect(r.out).To(HavePrefix("Global Flags:\n"))
		Expect(r.out).To(ContainSubstring("== lola cache add ==\n"))
		Expect(r.out).NotTo(ContainSubstring("== lola install =="))
		Expect(len(regexp.MustCompile(`(?m)^\s+--no-pager\s`).FindAllString(r.out, -1))).To(Equal(1))
	})

	It("help --short prints grouped commands with aliases inline", func() {
		r := lola("help", "--short")
		Expect(r.code).To(Equal(0))
		Expect(r.out).To(ContainSubstring("Additional:"))
		Expect(r.out).To(MatchRegexp(`(?m)^  version \(ver\)\s+Print lola's version$`))
	})

	It("rejects an unknown help topic, --all with --short, and the removed --long", func() {
		Expect(lola("help", "nope").err).To(ContainSubstring("no help topic \"nope\"\n  hint: see: lola help\n"))
		Expect(lola("help", "--all", "--short").err).To(ContainSubstring(
			"--all and --short cannot be combined\n  hint: pick one: lola help --all, or lola help --short <command>"))
		Expect(lola("help", "--long").code).To(Equal(1))
	})

	It("help for one command resolves aliases", func() {
		r := lola("help", "ver", "--short")
		Expect(r.code).To(Equal(0))
		Expect(r.out).To(HavePrefix("Print lola's version"))
		Expect(r.out).To(ContainSubstring("Aliases:\n  version, ver"))
	})

	It("lists the scope flags among every command's global flags", func() {
		r := lola("install", "--help")
		Expect(r.code).To(Equal(0))
		Expect(r.out).To(MatchRegexp(`--scope string\s+act on this scope: project, user or both \(also project,user\)\n`))
		Expect(r.out).To(MatchRegexp(`--project\s+act on project scope: --scope project\n`))
		Expect(r.out).To(MatchRegexp(`-C, --root string\s+the project at this directory; implies project scope\n`))
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

	It("rejects --json with --plain, and --user with -C where a command has a scope", func() {
		Expect(lola("version", "--json", "--plain").err).To(ContainSubstring("--json and --plain cannot be combined"))
		Expect(lola("sync", "--user", "-C", ".").err).To(ContainSubstring("--user cannot be combined with -C"))
		Expect(lola("version", "--user", "-C", ".").code).To(Equal(0), "a command without a scope ignores the scope flags")
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
