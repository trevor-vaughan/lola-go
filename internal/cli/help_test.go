package cli_test

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

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

	It("the root's long help lists each environment variable by name, aligned", func() {
		long := lola("--help").out
		Expect(long).To(ContainSubstring("\nEnvironment:\n" +
			"  LOLA_COLOR       1 or 0 forces color on or off\n" +
			"  NO_COLOR         disable color when set (https://no-color.org)\n"))
		Expect(long).To(ContainSubstring("\n  XDG_CACHE_HOME   the cache, marketplace catalogs, downloads and assistant versions (default ~/.cache)\n"))
	})

	It("piped help is not the tree; help --all is, with the global flags once", func() {
		Expect(lola("help").out).NotTo(ContainSubstring("== lola install =="))
		r := lola("help", "--all")
		Expect(r.code).To(Equal(0))
		Expect(r.out).To(ContainSubstring("== lola install ==\n"))
		Expect(r.out).To(ContainSubstring("== lola cache add ==\n"))
		Expect(r.out).To(ContainSubstring("== lola completion ==\n"))
		Expect(regexp.MustCompile(`(?m)^\s+--no-pager\s`).FindAllString(r.out, -1)).To(HaveLen(1))
		Expect(r.out).NotTo(ContainSubstring("OUTPUT CONTRACT"))
		r = lola("help", "--all", "cache")
		Expect(r.out).To(HavePrefix("Global Flags:\n"))
		Expect(r.out).To(ContainSubstring("== lola cache add ==\n"))
		Expect(r.out).NotTo(ContainSubstring("== lola install =="))
		Expect(regexp.MustCompile(`(?m)^\s+--no-pager\s`).FindAllString(r.out, -1)).To(HaveLen(1))
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

	It("coaches -V with the long flag, and other unknown flags with --help", func() {
		r := lola("-V")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("hint: lola has no -V; the flag is --version: lola --version\n"))
		Expect(lola("--version").code).To(Equal(0))
		r = lola("-Z")
		Expect(r.err).To(ContainSubstring("hint: see: lola --help\n"))
		Expect(r.err).NotTo(ContainSubstring("--version"))
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

	Context("under --json", func() {
		// helpJSON is lola's stdout for args, which must exit 0, parsed as one JSON object.
		helpJSON := func(args ...string) map[string]any {
			r := lola(args...)
			Expect(r.code).To(Equal(0), r.err)
			var doc map[string]any
			Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed(), r.out)
			return doc
		}
		named := func(name string) types.GomegaMatcher { return HaveKeyWithValue("name", name) }

		It("help --all --json lists every command prose --all lists, once, in order, each with its own flags", func() {
			var want []string
			for _, m := range regexp.MustCompile(`(?m)^== (.+) ==$`).FindAllStringSubmatch(lola("help", "--all").out, -1) {
				want = append(want, m[1])
			}
			root, err := cli.NewRootCmd(os.Getenv)
			Expect(err).NotTo(HaveOccurred())
			root.InitDefaultHelpCmd()
			root.InitDefaultCompletionCmd()
			var got []string
			var visit func(doc map[string]any)
			visit = func(doc map[string]any) {
				path := doc["path"].(string)
				got = append(got, path)
				c, _, err := root.Find(strings.Fields(path)[1:])
				Expect(err).NotTo(HaveOccurred(), path)
				c.InitDefaultHelpFlag()
				var local, listed []string
				c.LocalFlags().VisitAll(func(f *pflag.Flag) {
					if !f.Hidden {
						local = append(local, f.Name)
					}
				})
				for _, f := range doc["flags"].([]any) {
					listed = append(listed, f.(map[string]any)["name"].(string))
				}
				Expect(listed).To(Equal(local), path)
				for _, sub := range doc["commands"].([]any) {
					visit(sub.(map[string]any))
				}
			}
			visit(helpJSON("help", "--all", "--json"))
			Expect(got).To(Equal(want))
		})

		It("-h and --help print what help --short and help print", func() {
			Expect(lola("install", "-h", "--json").out).To(Equal(lola("help", "--short", "install", "--json").out))
			Expect(lola("install", "--help", "--json").out).To(Equal(lola("help", "install", "--json").out))
		})

		It("short help has no long, examples, global flags or reference; long help has them", func() {
			short := helpJSON("install", "-h", "--json")
			Expect(short).To(HaveKeyWithValue("path", "lola install"))
			Expect(short).To(HaveKeyWithValue("name", "install"))
			Expect(short).To(HaveKeyWithValue("aliases", []any{"add"}))
			Expect(short).To(HaveKeyWithValue("group", "Core"))
			Expect(short).To(HaveKeyWithValue("short", find("install").Short))
			Expect(short).To(HaveKeyWithValue("usage", find("install").UseLine()))
			for _, k := range []string{"long", "examples", "global_flags", "reference_url", "environment"} {
				Expect(short).NotTo(HaveKey(k))
			}
			long := helpJSON("install", "--help", "--json")
			Expect(long).To(HaveKeyWithValue("long", strings.TrimSpace(find("install").Long)))
			Expect(long).To(HaveKeyWithValue("examples", ContainElement(HavePrefix("lola install "))))
			Expect(long).To(HaveKeyWithValue("reference_url", docs+"reference/install.md"))
			Expect(long).To(HaveKeyWithValue("global_flags", ContainElement(named("json"))))
			Expect(long).NotTo(HaveKey("environment"))
		})

		It("lists command aliases and flag aliases", func() {
			Expect(helpJSON("help", "profile", "--json")).To(HaveKeyWithValue("commands",
				ContainElement(And(named("on"), HaveKeyWithValue("aliases", []any{"enable", "activate"})))))
			Expect(helpJSON("help", "update", "--json")).To(HaveKeyWithValue("flags",
				ContainElement(And(named("fetch"), HaveKeyWithValue("aliases", []any{"refresh"})))))
		})

		It("prints empty arrays, never null", func() {
			r := lola("help", "version", "--json")
			Expect(r.out).NotTo(ContainSubstring(": null"))
			doc := helpJSON("help", "version", "--json")
			Expect(doc).To(HaveKeyWithValue("commands", BeEmpty()))
			Expect(doc).To(HaveKeyWithValue("flags", ContainElement(And(named("help"), HaveKeyWithValue("aliases", []any{})))))
			all := lola("help", "--all", "--json")
			Expect(all.out).NotTo(ContainSubstring(": null"))
			Expect(helpJSON("help", "--all", "--json")).To(HaveKeyWithValue("global_flags", []any{}))
		})

		It("says which flags take an optional value, and shows --trust's placeholder without lola's sentinel", func() {
			install := helpJSON("install", "-h", "--json")
			Expect(install).To(HaveKeyWithValue("flags", ContainElement(And(named("strict"),
				HaveKeyWithValue("type", "bool"), HaveKeyWithValue("value", ""), HaveKeyWithValue("value_optional", true)))))
			Expect(install).To(HaveKeyWithValue("flags", ContainElement(And(named("target"),
				HaveKeyWithValue("shorthand", "t"), HaveKeyWithValue("type", "stringSlice"), HaveKeyWithValue("value_optional", false)))))
			Expect(install).To(HaveKeyWithValue("flags", ContainElement(And(named("trust"),
				HaveKeyWithValue("value", "=<kinds>"), HaveKeyWithValue("value_optional", true)))))
			Expect(helpJSON("sync", "-h", "--json")).To(HaveKeyWithValue("flags", ContainElement(And(named("trust"),
				HaveKeyWithValue("value", "[=entries,profiles]")))))
			for _, args := range [][]string{{"install", "-h", "--json"}, {"help", "--all", "--json"}} {
				Expect(lola(args...).out).NotTo(ContainSubstring(`\u0000`))
			}
		})

		It("the root's long help has the environment; bare lola prints short help", func() {
			long := helpJSON("--help", "--json")
			Expect(long).To(HaveKeyWithValue("group", ""))
			Expect(long).To(HaveKeyWithValue("reference_url", docs+"usage.md"))
			Expect(long).To(HaveKeyWithValue("environment", ContainElement(Equal(map[string]any{
				"name": "LOLA_COLOR", "description": "1 or 0 forces color on or off"}))))
			Expect(long).To(HaveKeyWithValue("commands", ContainElement(And(named("help"),
				HaveKeyWithValue("path", "lola help")))))
			Expect(lola("help", "--json").out).To(Equal(lola("--help", "--json").out))
			Expect(lola("--json").out).To(Equal(lola("-h", "--json").out))
		})

		It("help --all <cmd> carries the global flags once, at the top, and only that subtree", func() {
			doc := helpJSON("help", "--all", "cache", "--json")
			Expect(doc).To(HaveKeyWithValue("path", "lola cache"))
			Expect(doc).To(HaveKeyWithValue("global_flags", ContainElement(named("no-pager"))))
			Expect(doc).NotTo(HaveKey("environment"))
			subs := doc["commands"].([]any)
			Expect(subs).NotTo(BeEmpty())
			for _, s := range subs {
				sub := s.(map[string]any)
				Expect(sub["path"]).To(HavePrefix("lola cache "))
				Expect(sub).To(HaveKey("long"))
				Expect(sub).To(HaveKey("flags"))
				Expect(sub).NotTo(HaveKey("global_flags"))
			}
		})

		It("prints JSON on the help path even with --plain, which only the help command refuses", func() {
			Expect(lola("install", "-h", "--json", "--plain").out).To(Equal(lola("install", "-h", "--json").out))
			r := lola("help", "install", "--json", "--plain")
			Expect(r.code).To(Equal(1))
			Expect(r.out).To(BeEmpty())
		})

		It("refuses an unknown topic and --all with --short as a JSON error, stdout empty", func() {
			for _, args := range [][]string{{"help", "nope", "--json"}, {"help", "--all", "--short", "--json"}} {
				r := lola(args...)
				Expect(r.code).To(Equal(1), strings.Join(args, " "))
				Expect(r.out).To(BeEmpty())
				var e map[string]any
				Expect(json.Unmarshal([]byte(r.err), &e)).To(Succeed(), r.err)
				Expect(e).To(HaveKey("error"))
			}
		})
	})
})
