package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// R-8-1 (Plan 8 §9.1): the commands that confirm a plan (uninstall, cache rm, migrate, cache
// clean, Plan 9 spec 45, and repair), cell by cell of the matrix: on or off a terminal, with or without
// --json, and the answer.
var _ = Describe("a confirmation's JSON refusal (Plan 8 §9.1)", func() {
	// confirmCase is one command's fixture: the run without its answer, the plan it prints, the
	// question it asks, the hint off a terminal (under --json) and what its change looks like.
	type confirmCase struct {
		args      []string
		plan      string // the plan's first line
		planLines int    // the plan's lines on stderr
		question  string
		hint      string // hints[0] of the run with --json
		done      string // in the human result
		keys      []string
		// steps reads what the plan would do from a refusal's document, and done what was done
		// from the result's.
		steps, did func(map[string]any) any
		written    func() bool
	}
	var tmp string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
	})
	key := func(k string) func(map[string]any) any { return func(doc map[string]any) any { return doc[k] } }
	installKit := func(proj string) {
		kit := filepath.Join(tmp, "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "commands/hi.md": "Hi\n"})
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		r := lola("install", kit, "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
	}
	// fixture builds the named command's fixture in tmp.
	fixture := func(command string) confirmCase {
		proj := filepath.Join(tmp, "proj")
		switch command {
		case "uninstall":
			installKit(proj)
			return confirmCase{
				args: []string{"uninstall", "kit", "-C", proj}, plan: "plan (dry run; nothing was written):", planLines: 2,
				question: "Uninstall kit?", hint: "lola uninstall kit --json --root " + proj + " -y", done: "uninstalled",
				keys: []string{"modules", "steps", "findings", "notices", "dry_run"}, steps: key("steps"), did: key("steps"),
				written: func() bool {
					_, err := os.Stat(filepath.Join(proj, ".claude/commands/hi.md"))
					return os.IsNotExist(err)
				},
			}
		case "cache rm":
			installKit(proj)
			return confirmCase{
				args: []string{"cache", "rm", "kit"}, plan: "will remove from your cache: kit", planLines: 1,
				question: "Remove kit from your cache?", hint: "lola cache rm kit --json -y", done: "removed kit from your cache",
				keys: []string{"would_remove"}, steps: key("would_remove"), did: key("removed"),
				written: func() bool { return !strings.Contains(lola("cache", "list", "--plain").out, "kit") },
			}
		case "cache clean":
			kit := filepath.Join(tmp, "kit")
			writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "commands/hi.md": "v1\n"})
			Expect(lola("cache", "add", kit).code).To(Equal(0))
			writeTree(kit, map[string]string{"commands/hi.md": "v2\n"})
			Expect(lola("cache", "add", kit).code).To(Equal(0))
			p, err := paths.Resolve(os.Getenv)
			Expect(err).NotTo(HaveOccurred())
			versions := func() int {
				es, err := os.ReadDir(filepath.Join(p.Store, "kit", "versions"))
				Expect(err).NotTo(HaveOccurred())
				return len(es)
			}
			Expect(versions()).To(Equal(2), "one superseded")
			return confirmCase{
				args: []string{"cache", "clean"}, plan: "will remove from your cache:", planLines: 3,
				question: "Remove these from your cache?", hint: "lola cache clean --json -y", done: "freed",
				keys: []string{"removed", "kept", "bytes_freed", "dry_run", "notices"}, steps: key("removed"), did: key("removed"),
				written: func() bool { return versions() == 1 },
			}
		case "repair":
			installKit(proj)
			lp := filepath.Join(proj, ".lola", "lock.json")
			editLock(proj, func(doc map[string]any) {
				doc["created"] = append(doc["created"].([]any), map[string]any{"target": "claude-code", "path": "../settings.json"})
			})
			return confirmCase{
				args: []string{"repair", "-C", proj}, plan: "will forget 1 entry in " + lp + "; files are not touched:", planLines: 2,
				question: "Forget 1 entry lola could not have written? Files are not touched.", hint: "lola repair --json --root " + proj + " -y",
				done: "forgot 1 entry in " + lp,
				keys: []string{"dry_run", "file", "forgotten", "forgotten_modules", "notices"}, steps: key("forgotten"), did: key("forgotten"),
				written: func() bool { return !strings.Contains(read(lp), "settings.json") },
			}
		case "migrate":
			home, src := filepath.Join(tmp, "home", ".lola"), filepath.Join(tmp, "src", "review")
			writeTree(src, map[string]string{"commands/ship.md": "Ship\n"})
			writeTree(home, map[string]string{
				"installed.yml": "version: '1.0'\ninstallations:\n- module: review\n  assistant: claude-code\n  scope: project\n  project_path: " + proj +
					"\n  skills: []\n  commands: [ship]\n  agents: []\n  mcps: []\n  has_instructions: false\n",
				"modules/review/.lola/source.yml": "source: " + src + "\ntype: folder\n",
			})
			writeTree(proj, map[string]string{".claude/commands/ship.md": "python ship\n"})
			steps := func(doc map[string]any) any {
				var out []any
				for _, d := range doc["destinations"].([]any) {
					out = append(out, d.(map[string]any)["steps"].([]any)...)
				}
				return out
			}
			return confirmCase{
				args: []string{"migrate"}, plan: "plan (dry run; nothing was written):", planLines: 7,
				question: "Migrate?", hint: "lola migrate --json -y", done: "✓ migrated project " + proj,
				keys:  []string{"dry_run", "nothing_to_migrate", "python_home", "trash", "destinations", "unmappable", "not_migrated", "markets", "home", "warnings", "summary", "remap"},
				steps: steps, did: steps,
				written: func() bool { return read(filepath.Join(proj, ".claude/commands/ship.md")) != "python ship\n" },
			}
		}
		Fail("no fixture for " + command)
		return confirmCase{}
	}
	// refusal reads a refusal's one JSON line.
	refusal := func(stderr string) map[string]any {
		GinkgoHelper()
		var doc map[string]any
		Expect(json.Unmarshal([]byte(stderr), &doc)).To(Succeed(), stderr)
		return doc
	}
	with := func(args []string, more ...string) []string { return append(append([]string{}, args...), more...) }

	DescribeTable("c1: off a terminal under --json, <cmd> prints one JSON line on stderr carrying the plan, and nothing on stdout",
		func(command string) {
			c := fixture(command)
			r := lola(with(c.args, "--json")...)
			Expect(r.code).To(Equal(1))
			Expect(r.out).To(BeEmpty())
			Expect(strings.Count(r.err, "\n")).To(Equal(1), r.err)
			Expect(r.err).NotTo(ContainSubstring(c.plan), "no human plan")
			doc := refusal(r.err)
			Expect(doc["error"]).To(Equal("lola " + command + " needs confirmation, and there is no terminal to ask on"))
			Expect(doc["hints"]).To(Equal([]any{c.hint}), "the run, its flags kept, with -y")
			for _, k := range c.keys {
				Expect(doc).To(HaveKey(k))
			}
			checkJSON(doc, "")
			Expect(c.written()).To(BeFalse())
		},
		Entry("uninstall", "uninstall"), Entry("cache rm", "cache rm"), Entry("migrate", "migrate"), Entry("cache clean", "cache clean"),
		Entry("repair", "repair"))

	DescribeTable("c2: off a terminal without --json, <cmd> prints the plan, then the error and its hint",
		func(command string) {
			c := fixture(command)
			r := lola(c.args...)
			Expect(r.code).To(Equal(1))
			Expect(r.out).To(BeEmpty())
			Expect(r.err).To(HavePrefix(c.plan + "\n"))
			Expect(r.err).To(HaveSuffix("error: lola " + command + " needs confirmation, and there is no terminal to ask on\n" +
				"  hint: " + strings.Replace(c.hint, " --json", "", 1) + "\n"))
			Expect(strings.Count(r.err, "\n")).To(Equal(c.planLines+2), r.err)
			Expect(c.written()).To(BeFalse())
		},
		Entry("uninstall", "uninstall"), Entry("cache rm", "cache rm"), Entry("migrate", "migrate"), Entry("cache clean", "cache clean"),
		Entry("repair", "repair"))

	DescribeTable("c3: on a terminal under --json, <cmd> prints the plan and the question on stderr and one JSON document on stdout",
		func(command string) {
			c := fixture(command)
			p := &confirming{answers: []bool{true}}
			r := lolaAsking(p, with(c.args, "--json")...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.questions).To(Equal([]string{c.question}))
			Expect(p.before[0]).To(HavePrefix(c.plan+"\n"), "the plan, then the question")
			Expect(strings.Count(p.before[0], "\n")).To(Equal(c.planLines), p.before[0])
			Expect(r.err).To(Equal(p.before[0]), "nothing on stderr after the question")
			var doc map[string]any
			Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed(), r.out)
			checkJSON(doc, "")
			Expect(c.written()).To(BeTrue())
		},
		Entry("uninstall", "uninstall"), Entry("cache rm", "cache rm"), Entry("migrate", "migrate"), Entry("cache clean", "cache clean"),
		Entry("repair", "repair"))

	DescribeTable("c4: on a terminal without --json, <cmd> asks after the plan and prints the human result",
		func(command string) {
			c := fixture(command)
			p := &confirming{answers: []bool{true}}
			r := lolaAsking(p, c.args...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.questions).To(Equal([]string{c.question}))
			Expect(p.before[0]).To(HavePrefix(c.plan + "\n"))
			Expect(strings.Count(p.before[0], "\n")).To(Equal(c.planLines), p.before[0])
			Expect(r.out).To(ContainSubstring(c.done))
			Expect(c.written()).To(BeTrue())
		},
		Entry("uninstall", "uninstall"), Entry("cache rm", "cache rm"), Entry("migrate", "migrate"), Entry("cache clean", "cache clean"),
		Entry("repair", "repair"))

	DescribeTable("c5: declining <cmd> on a terminal writes nothing and exits 0",
		func(command string) {
			c := fixture(command)
			for _, args := range [][]string{c.args, with(c.args, "--json")} {
				p := &confirming{answers: []bool{false}}
				r := lolaAsking(p, args...)
				Expect(r.code).To(Equal(0), r.err)
				Expect(r.out).To(BeEmpty())
				Expect(p.before[0]).To(HavePrefix(c.plan + "\n"))
				Expect(r.err).To(Equal(p.before[0]+"aborted; nothing changed.\n"), "the plan, the question, then the abort")
				Expect(c.written()).To(BeFalse())
			}
		},
		Entry("uninstall", "uninstall"), Entry("cache rm", "cache rm"), Entry("migrate", "migrate"), Entry("cache clean", "cache clean"),
		Entry("repair", "repair"))

	DescribeTable("c6: -y off a terminal under --json prints no plan line and one JSON document",
		func(command string) {
			c := fixture(command)
			r := lola(with(c.args, "-y", "--json")...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.err).To(BeEmpty())
			var doc map[string]any
			Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed(), r.out)
			checkJSON(doc, "")
			Expect(c.written()).To(BeTrue())
		},
		Entry("uninstall", "uninstall"), Entry("cache rm", "cache rm"), Entry("migrate", "migrate"), Entry("cache clean", "cache clean"),
		Entry("repair", "repair"))

	DescribeTable("c7: --force off a terminal under --json prints no plan line and one JSON document",
		func(command string) {
			c := fixture(command)
			r := lola(with(c.args, "--force", "--json")...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.err).To(BeEmpty())
			var doc map[string]any
			Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed(), r.out)
			checkJSON(doc, "")
			Expect(c.written()).To(BeTrue())
		},
		Entry("uninstall", "uninstall"), Entry("migrate", "migrate"))

	// onTerminal runs c on a terminal lines rows tall whose keys quit each viewer, answering yes.
	onTerminal := func(c confirmCase, lines string) (*confirming, result) {
		GinkgoHelper()
		GinkgoT().Setenv("LOLA_PAGER", "")
		GinkgoT().Setenv("COLUMNS", "80")
		GinkgoT().Setenv("LINES", lines)
		in, typed, err := os.Pipe()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			Expect(typed.Close()).To(Succeed())
			Expect(in.Close()).To(Succeed())
		})
		_, err = typed.WriteString("q")
		Expect(err).NotTo(HaveOccurred())
		// The result is taller than the terminal too: its viewer gets its own q, typed once the
		// plan's viewer has exited.
		p := &confirming{answers: []bool{true}, meddle: func() {
			_, err := typed.WriteString("q")
			Expect(err).NotTo(HaveOccurred())
		}}
		root := cli.NewRootCmd(os.Getenv, cli.WithPrompter(p), cli.WithTerminals(true, true))
		var out bytes.Buffer
		p.stderr = &bytes.Buffer{}
		root.SetOut(&out)
		root.SetErr(p.stderr)
		root.SetIn(in)
		root.SetArgs(c.args)
		code := cli.Execute(root)
		return p, result{out: out.String(), err: p.stderr.String(), code: code}
	}

	DescribeTable("c8: on a terminal, <cmd>'s plan taller than the terminal opens in the viewer, in full, before the question (Plan 10 §2.1)",
		func(command string) {
			c := fixture(command)
			p, r := onTerminal(c, "2")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("\x1b[?1049h"), "the viewer opened")
			Expect(r.out).To(ContainSubstring(c.plan + "\n"))
			Expect(p.questions).To(Equal([]string{c.question}), "the question comes after the viewer")
			Expect(p.before[0]).NotTo(ContainSubstring(c.plan), "the plan went to the viewer, not stderr")
			Expect(c.written()).To(BeTrue())
		},
		Entry("uninstall", "uninstall"), Entry("migrate", "migrate"), Entry("cache clean", "cache clean"))

	DescribeTable("c9: on a terminal, <cmd>'s plan prints plainly before the question when the viewer is bypassed",
		func(command string, bypass func(), more ...string) {
			c := fixture(command)
			bypass()
			c.args = with(c.args, more...)
			p, r := onTerminal(c, "2")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).NotTo(ContainSubstring("\x1b[?1049h"))
			Expect(p.before[0]).To(HavePrefix(c.plan + "\n"))
			Expect(p.questions).To(Equal([]string{c.question}))
		},
		Entry("migrate, NO_PAGER set", "migrate", func() { GinkgoT().Setenv("NO_PAGER", "1") }),
		Entry("migrate, --no-pager", "migrate", func() {}, "--no-pager"),
		Entry("uninstall, NO_PAGER set", "uninstall", func() { GinkgoT().Setenv("NO_PAGER", "1") }))

	DescribeTable("the refusal's plan keys equal those -n --json prints for the same run",
		func(command string) {
			c := fixture(command)
			r := lola(with(c.args, "--json")...)
			Expect(r.code).To(Equal(1))
			doc := refusal(r.err)
			delete(doc, "error")
			delete(doc, "hints")
			dry := lola(with(c.args, "-n", "--json")...)
			Expect(dry.code).To(Equal(0), dry.err)
			var want map[string]any
			Expect(json.Unmarshal([]byte(dry.out), &want)).To(Succeed(), dry.out)
			Expect(doc).To(Equal(want))
		},
		Entry("uninstall", "uninstall"), Entry("migrate", "migrate"), Entry("cache clean", "cache clean"), Entry("repair", "repair"))

	DescribeTable("the refusal's hint runs the plan it carried",
		func(command string) {
			c := fixture(command)
			r := lola(with(c.args, "--json")...)
			Expect(r.code).To(Equal(1))
			doc := refusal(r.err)
			hint := doc["hints"].([]any)[0].(string)
			again := lola(shellWords(hint)[1:]...)
			Expect(again.code).To(Equal(0), again.err)
			var res map[string]any
			Expect(json.Unmarshal([]byte(again.out), &res)).To(Succeed(), again.out)
			Expect(c.did(res)).To(Equal(c.steps(doc)), "it did what the refusal said it would")
			Expect(c.written()).To(BeTrue())
		},
		Entry("uninstall", "uninstall"), Entry("cache rm", "cache rm"), Entry("migrate", "migrate"), Entry("cache clean", "cache clean"),
		Entry("repair", "repair"))

	It("a refusal plan carries a file name with control characters only JSON-escaped in steps[].path, and cleaned in error and hints", func() {
		proj := filepath.Join(tmp, "pr\aoj")
		kit := filepath.Join(tmp, "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "skills/lint/SKILL.md": "---\ndescription: Lint\n---\nLint.\n",
			"skills/lint/r\x1b[31mx.md": "x\n"})
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		Expect(lola("install", kit, "-C", proj, "-t", "claude-code").code).To(Equal(0))
		r := lola("uninstall", "kit", "-C", proj, "--json")
		Expect(r.code).To(Equal(1))
		Expect(r.err).NotTo(ContainSubstring("\x1b"), "JSON escapes it")
		Expect(r.err).NotTo(ContainSubstring("\a"))
		Expect(r.err).To(ContainSubstring(`r\u001b[31mx.md`))
		doc := refusal(r.err)
		var paths []any
		for _, st := range doc["steps"].([]any) {
			paths = append(paths, st.(map[string]any)["path"])
		}
		Expect(paths).To(ContainElement(".claude/skills/lint/r\x1b[31mx.md"), "what lola would delete, as it is")
		Expect(doc["error"]).To(Equal("lola uninstall needs confirmation, and there is no terminal to ask on"))
		Expect(doc["hints"]).To(Equal([]any{"lola uninstall kit --json --root '" + filepath.Join(tmp, "pr?oj") + "' -y"}), "cleaned")
	})
})

// R-8-U8: a bare lola init, which scaffolds the current directory, asks first; lola init <dir>
// (. too) and -y do not.
var _ = Describe("lola init asks before scaffolding the current directory (R-8-U8)", func() {
	var tmp, dir string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		dir = filepath.Join(tmp, "fresh")
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		GinkgoT().Chdir(dir)
	})
	plan := "will create module fresh in %s (agent-plugins):\n    plugin.json\n    skills/hello/SKILL.md\n    dev.getlola/commands/hello.md\n"
	empty := func(d string) {
		GinkgoHelper()
		entries, err := os.ReadDir(d)
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(BeEmpty(), "nothing written")
	}

	It("on a terminal, shows the plan, asks, and scaffolds when you accept", func() {
		p := &confirming{answers: []bool{true}}
		r := lolaAsking(p, "init")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.questions).To(Equal([]string{"Create module fresh in " + dir + "?"}))
		Expect(p.before[0]).To(Equal(fmt.Sprintf(plan, dir)))
		Expect(r.out).To(ContainSubstring("✓ created module fresh in . (agent-plugins)"))
		Expect(filepath.Join(dir, "plugin.json")).To(BeARegularFile())
	})

	It("on a terminal, declining writes nothing and exits 0", func() {
		for _, args := range [][]string{{"init"}, {"init", "--json"}} {
			p := &confirming{answers: []bool{false}}
			r := lolaAsking(p, args...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(BeEmpty())
			Expect(r.err).To(Equal(fmt.Sprintf(plan, dir) + "aborted; nothing changed.\n"))
			empty(dir)
		}
	})

	DescribeTable("off a terminal, refuses with the plan and a hint that keeps the run's flags, writing nothing",
		func(args []string, hint string) {
			r := lola(args...)
			Expect(r.code).To(Equal(1))
			Expect(r.out).To(BeEmpty())
			Expect(r.err).To(HaveSuffix("error: lola init needs confirmation, and there is no terminal to ask on\n  hint: " + hint + "\n"))
			Expect(r.err).To(HavePrefix("will create module fresh in " + dir))
			empty(dir)
			again := lola(shellWords(hint)[1:]...)
			Expect(again.code).To(Equal(0), again.err)
		},
		Entry("bare", []string{"init"}, "lola init -y"),
		Entry("with --format lola", []string{"init", "--format", "lola"}, "lola init --format lola -y"),
		Entry("with --plain", []string{"init", "--plain"}, "lola init --plain -y"),
	)

	It("off a terminal under --json, prints one JSON line carrying the plan on stderr, and nothing on stdout", func() {
		r := lola("init", "--json")
		Expect(r.code).To(Equal(1))
		Expect(r.out).To(BeEmpty())
		Expect(strings.Count(r.err, "\n")).To(Equal(1), r.err)
		Expect(r.err).NotTo(ContainSubstring("will create"), "no human plan")
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.err), &doc)).To(Succeed(), r.err)
		checkJSON(doc, "")
		Expect(doc["error"]).To(Equal("lola init needs confirmation, and there is no terminal to ask on"))
		Expect(doc["hints"]).To(Equal([]any{"lola init --json -y"}))
		delete(doc, "error")
		delete(doc, "hints")
		empty(dir)
		done := lola("init", "--json", "-y")
		Expect(done.code).To(Equal(0), done.err)
		var want map[string]any
		Expect(json.Unmarshal([]byte(done.out), &want)).To(Succeed(), done.out)
		Expect(doc).To(Equal(want), "the plan is what -y then does")
		Expect(doc["files"]).To(Equal([]any{"plugin.json", "skills/hello/SKILL.md", "dev.getlola/commands/hello.md"}))
	})

	DescribeTable("scaffolds off a terminal without asking",
		func(args ...string) {
			r := lola(args...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.err).To(BeEmpty())
			Expect(r.out).To(ContainSubstring("✓ created module fresh in "))
		},
		Entry("-y", "init", "-y"),
		Entry("--yes", "init", "--yes"),
		Entry("a directory", "init", "../fresh"),
		Entry(".", "init", "."),
	)

	It("shows a directory whose path holds control characters cleaned, in the plan and the question", func() {
		esc := filepath.Join(tmp, "x\x1b[31m", "fresh")
		Expect(os.MkdirAll(esc, 0o755)).To(Succeed())
		GinkgoT().Chdir(esc)
		shown := filepath.Join(tmp, "x?[31m", "fresh")
		p := &confirming{answers: []bool{false}}
		r := lolaAsking(p, "init")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).NotTo(ContainSubstring("\x1b"))
		Expect(p.questions).To(Equal([]string{"Create module fresh in " + shown + "?"}))
		Expect(p.before[0]).To(Equal(fmt.Sprintf(plan, shown)))
		empty(esc)
	})

	It("refuses an invalid name before asking anything", func() {
		bad := filepath.Join(tmp, "Bad_Name")
		Expect(os.MkdirAll(bad, 0o755)).To(Succeed())
		GinkgoT().Chdir(bad)
		p := &confirming{answers: []bool{true}}
		r := lolaAsking(p, "init")
		Expect(r.code).To(Equal(1))
		Expect(p.questions).To(BeEmpty())
		Expect(r.err).To(HavePrefix(`error: "Bad_Name" is not an Agent Plugins name`))
		Expect(r.err).NotTo(ContainSubstring("will create"))
		empty(bad)
	})
})
