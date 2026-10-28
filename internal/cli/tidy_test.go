package cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// tidyIDs are the IDs of the options whose labels contain any of parts.
func tidyIDs(opts []cli.TidyOption, parts ...string) []string {
	var ids []string
	for _, o := range opts {
		for _, part := range parts {
			if strings.Contains(o.Label, part) {
				ids = append(ids, o.ID)
				break
			}
		}
	}
	return ids
}

// tidyPicks answers the move picker with the options whose labels contain any of parts.
func tidyPicks(parts ...string) []func(cli.TidyPickStep) ([]string, error) {
	return []func(cli.TidyPickStep) ([]string, error){func(st cli.TidyPickStep) ([]string, error) { return tidyIDs(st.Options, parts...), nil }}
}

// tidyKeeps answers the keep picker with the options whose labels contain any of parts.
func tidyKeeps(parts ...string) []func(cli.TidyKeepStep) ([]string, error) {
	return []func(cli.TidyKeepStep) ([]string, error){func(st cli.TidyKeepStep) ([]string, error) { return tidyIDs(st.Options, parts...), nil }}
}

// tidyLabels are the labels a tidy picker offered.
func tidyLabels(opts []cli.TidyOption) []string {
	out := make([]string, len(opts))
	for i, o := range opts {
		out[i] = o.Label
	}
	return out
}

var _ = Describe("lola tidy", func() {
	var tmp, proj string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(tmp, "proj")
		mod := filepath.Join(tmp, "example")
		writeTree(mod, map[string]string{"commands/ship.md": "Ship $ARGUMENTS\n"})
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		r := lola("install", mod, "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		GinkgoT().Chdir(tmp) // hints name -C for a user outside the project
	})

	It("the run reports its trash directory and size, and names every MCP copy as possibly holding credentials", func() {
		mcp := `{"mcpServers": {"legacy": {"command": "npx", "args": ["-y", "legacy"]}}}`
		writeTree(proj, map[string]string{".claude/skills/old/SKILL.md": "0123456789\n", ".mcp.json": mcp})
		r := lola("tidy", "--all", "-y", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		entries, err := os.ReadDir(filepath.Join(tmp, "state", "lola", "trash"))
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(HaveLen(1))
		run := filepath.Join(tmp, "state", "lola", "trash", entries[0].Name())
		Expect(r.out).To(HavePrefix("Tidy project " + proj + " — claude-code\n"))
		Expect(r.out).To(ContainSubstring(fmt.Sprintf("✓ moved 2 items (%d B) to the trash: %s\n", 11+len(mcp), run)))
		Expect(r.out).To(ContainSubstring("  → claude-code  skill/old  .claude/skills/old/\n"), "a directory ends in /")
		Expect(r.out).To(ContainSubstring("  → claude-code  mcp/legacy (npx)  .mcp.json /mcpServers/legacy\n"))
		Expect(r.out).To(ContainSubstring("⚠ the trash run holds a copy of .mcp.json, which may contain credentials\n"))

		r = lola("tidy", "--all", "-y", "-C", proj, "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(`"trash": ""`), "nothing left to move")
	})

	It("MCP arguments, environment, headers and URL userinfo, path and query never reach human or JSON output", func() {
		userinfo := "SENTINEL_USER:SENTINEL_PW"
		servers := `{"mcpServers": {
  "stdio": {"command": "TOKEN=SENTINEL_ENVWORD /opt/bin/srv-cmd", "args": ["--token", "SENTINEL_ARG"], "env": {"K": "SENTINEL_ENV"}},
  "remote": {"type": "http", "url": "https://` + userinfo + `@mcp.example.com/SENTINEL_PATH?q=SENTINEL_QUERY#SENTINEL_FRAG",
    "headers": {"Authorization": "SENTINEL_HEADER"}},
  "kept": {"command": "kept-cmd", "args": ["SENTINEL_KEPT"]},
  "argsonly": {"args": ["--token=SENTINEL_ARGONLY"]},
  "envonly": {"command": "A=1", "args": ["SENTINEL_ARGENV"]},
  "arrenv": {"command": ["A=1", "SENTINEL_ARRENV"]},
  "strflag": {"command": "A=1 --token=SENTINEL_STRFLAG"}
}}`
		file := servers
		check := func(args ...string) result {
			GinkgoHelper()
			var human result
			for _, format := range [][]string{{"--json"}, {"--plain"}, nil} {
				writeTree(proj, map[string]string{".mcp.json": file})
				human = lola(append(append([]string{"tidy", "-C", proj}, args...), format...)...)
				Expect(human.out+human.err).NotTo(ContainSubstring("SENTINEL"), "%v %v", args, format)
			}
			return human
		}
		r := check("-n", "--keep", "mcp/kept")
		Expect(r.out).To(ContainSubstring("mcp/stdio (srv-cmd)"))
		Expect(r.out).To(ContainSubstring("mcp/remote (mcp.example.com)"))
		Expect(r.out).To(ContainSubstring("mcp/kept (kept-cmd)"), "protected")
		for _, name := range []string{"argsonly", "envonly", "arrenv", "strflag"} {
			Expect(r.out).To(ContainSubstring("mcp/"+name+"  .mcp.json /mcpServers/"+name+"\n"), "a label never comes from args")
		}
		r = check()
		Expect(r.code).To(Equal(1), "the refusal's listing")
		Expect(r.err).To(ContainSubstring("mcp/stdio (srv-cmd)"))
		r = check("--all", "-y", "--keep", "mcp/kept")
		Expect(r.out).To(ContainSubstring("mcp/remote (mcp.example.com)"))
		Expect(r.out).To(ContainSubstring("mcp/argsonly  .mcp.json /mcpServers/argsonly\n"), "moved")

		By("the review question and its listing on a terminal")
		writeTree(proj, map[string]string{".mcp.json": file})
		p := &scripted{confirm: []bool{false}}
		r = lolaP(p, "tidy", "--all", "-C", proj, "--keep", "mcp/kept")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(HaveLen(1))
		Expect(strings.Join(p.calls, "\n") + r.out + r.err).NotTo(ContainSubstring("SENTINEL"))
		Expect(r.err).To(ContainSubstring("mcp/envonly  .mcp.json /mcpServers/envonly\n"))

		file = strings.Replace(servers, `"kept"`, `"remote"`, 1)
		r = check("-n")
		Expect(r.out).To(ContainSubstring("names the server remote twice"), "skipped")
	})

	It("tidy --json: snake_case keys, no null arrays, raw path and trash on every item, key through untrusted.Text, trash \"\" when nothing moved", func() {
		writeTree(proj, map[string]string{".claude/skills/old/SKILL.md": "x\n", ".claude/skills/bad\x1b[2J/SKILL.md": "x\n",
			".mcp.json": "{\"mcpServers\": {\"a\\u001bb\": {\"command\": \"x\"}}}"})
		var doc map[string]any
		r := lola("tidy", "-n", "-C", proj, "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		checkJSON(doc, "")
		Expect(doc["trash"]).To(Equal(""))
		Expect(doc["dry_run"]).To(BeTrue())
		Expect(doc["candidates"]).To(ContainElement(SatisfyAll(HaveKeyWithValue("path", filepath.Join(proj, ".claude", "skills", "old")),
			HaveKeyWithValue("name", "old"), HaveKeyWithValue("kind", "skill"))))
		Expect(doc["protected"]).To(ContainElement(SatisfyAll(HaveKeyWithValue("key", "/mcpServers/a?b"), HaveKeyWithValue("name", "a?b"),
			HaveKeyWithValue("path", filepath.Join(proj, ".mcp.json")), HaveKeyWithValue("code", "unselectable_name"))))
		Expect(doc["protected"]).To(ContainElement(SatisfyAll(HaveKeyWithValue("path", filepath.Join(proj, ".claude", "skills", "bad\x1b[2J")),
			HaveKeyWithValue("name", "bad?[2J"), HaveKeyWithValue("code", "unselectable_name"))))
		Expect(r.out).To(ContainSubstring(`bad\u001b[2J`), "raw but for JSON's escaping")

		r = lola("tidy", "skill/old", "-y", "-C", proj, "--json")
		Expect(r.code).To(Equal(0), r.err)
		doc = nil
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		checkJSON(doc, "")
		Expect(doc["candidates"]).To(BeEmpty())
		moved := doc["moved"].([]any)
		Expect(moved).To(HaveLen(1))
		Expect(moved[0]).To(HaveKeyWithValue("trash", HavePrefix(doc["trash"].(string)+string(filepath.Separator))))
		Expect(moved[0]).To(HaveKeyWithValue("path", filepath.Join(proj, ".claude", "skills", "old")))
	})

	It("the keep picker offers only what was not chosen and writes each answer as an escaped literal selector", func() {
		writeTree(proj, map[string]string{".claude/skills/old/SKILL.md": "x\n", ".claude/skills/a*b/SKILL.md": "x\n",
			".claude/skills/axb/SKILL.md": "x\n", ".claude/commands/tmp.md": "x\n"})
		p := &scripted{tidyPick: tidyPicks("skill/old"), tidyKeep: tidyKeeps(`skill/a\*b`), confirm: []bool{true}}
		r := lolaP(p, "tidy", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"tidy-pick", "tidy-keep", "confirm:Move 1 item to the trash and keep 1?"}))
		Expect(tidyLabels(p.tidyPickSteps[0].Options)).To(Equal([]string{"claude-code  skill/a*b  .claude/skills/a*b/",
			"claude-code  skill/axb  .claude/skills/axb/", "claude-code  skill/old  .claude/skills/old/", "claude-code  command/tmp  .claude/commands/tmp.md"}))
		Expect(tidyLabels(p.tidyKeepSteps[0].Options)).To(Equal([]string{`skill/a\*b  claude-code`, "skill/axb  claude-code",
			"command/tmp  claude-code"}), "one option per selector, naming what it keeps; skill/old was chosen to move")
		Expect(p.tidyKeepSteps[0].Required).To(BeFalse(), "something moves: keeping nothing is an answer")
		Expect(r.err).To(ContainSubstring("  → keep skill/a\\*b\n"))
		Expect(r.out).To(ContainSubstring("keep-list: added skill/a\\*b (project " + proj + ")"))
		Expect(os.ReadFile(filepath.Join(tmp, "config", "lola", "keep.yaml"))).To(ContainSubstring("- skill/a\\*b\n"))
		Expect(filepath.Join(proj, ".claude", "skills", "old")).NotTo(BeAnExistingFile())

		r = lola("tidy", "-n", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("locked  claude-code  skill/a*b  .claude/skills/a*b/  on your keep-list (skill/a\\*b)"))
		Expect(r.out).To(ContainSubstring("  → claude-code  skill/axb  .claude/skills/axb/\n"), "the escaped selector keeps only its own name")

		By("a selector keeps every assistant's item of its name, and is not offered when one of them was chosen")
		writeTree(proj, map[string]string{".claude/commands/dup.md": "x\n", ".gemini/commands/dup.toml": "x\n", ".claude/commands/solo.md": "x\n"})
		p = &scripted{tidyPick: tidyPicks("skill/axb"), tidyKeep: tidyKeeps("command/dup"), confirm: []bool{false}}
		r = lolaP(p, "tidy", "-C", proj, "-t", "claude-code", "-t", "gemini-cli")
		Expect(r.code).To(Equal(0), r.err)
		Expect(tidyLabels(p.tidyKeepSteps[0].Options)).To(Equal([]string{"command/dup  claude-code, gemini-cli", "command/solo  claude-code",
			"command/tmp  claude-code"}))
		p = &scripted{tidyPick: tidyPicks(".claude/commands/dup.md"), tidyKeep: tidyKeeps(), confirm: []bool{false}}
		r = lolaP(p, "tidy", "-C", proj, "-t", "claude-code", "-t", "gemini-cli")
		Expect(r.code).To(Equal(0), r.err)
		Expect(tidyLabels(p.tidyKeepSteps[0].Options)).To(Equal([]string{"skill/axb  claude-code", "command/solo  claude-code",
			"command/tmp  claude-code"}), "keeping command/dup would keep the item chosen to move (R-6d2-8)")
		Expect(os.RemoveAll(filepath.Join(proj, ".gemini"))).To(Succeed())
		Expect(os.Remove(filepath.Join(proj, ".claude", "commands", "dup.md"))).To(Succeed())
		Expect(os.Remove(filepath.Join(proj, ".claude", "commands", "solo.md"))).To(Succeed())

		By("choosing nothing to move makes the keep picker need a pick; Ctrl+C there cancels")
		p = &scripted{tidyPick: tidyPicks(), tidyKeep: []func(cli.TidyKeepStep) ([]string, error){
			func(cli.TidyKeepStep) ([]string, error) { return nil, cli.ErrAborted }}}
		r = lolaP(p, "tidy", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("aborted; nothing changed."))
		Expect(p.calls).To(Equal([]string{"tidy-pick", "tidy-keep"}))
		Expect(p.tidyKeepSteps[0].Required).To(BeTrue())

		By("choosing everything skips the keep picker")
		p = &scripted{tidyPick: tidyPicks("claude-code"), confirm: []bool{false}}
		r = lolaP(p, "tidy", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"tidy-pick", "confirm:Move 2 items to the trash?"}))
	})

	It("picker answers the CLI never offered are dropped: only the offered item moves, and the keep-list is unchanged", func() {
		writeTree(proj, map[string]string{".claude/skills/old/SKILL.md": "x\n", ".claude/commands/tmp.md": "x\n"})
		keep := "version: 1\nprojects:\n  " + proj + ":\n    - command/other\n"
		writeTree(filepath.Join(tmp, "config", "lola"), map[string]string{"keep.yaml": keep})
		p := &scripted{
			tidyPick: []func(cli.TidyPickStep) ([]string, error){func(st cli.TidyPickStep) ([]string, error) {
				return append([]string{"99"}, tidyIDs(st.Options, "skill/old")...), nil
			}},
			tidyKeep: []func(cli.TidyKeepStep) ([]string, error){func(cli.TidyKeepStep) ([]string, error) {
				return []string{"skill/zzz"}, nil
			}},
			confirm: []bool{true},
		}
		r := lolaP(p, "tidy", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"tidy-pick", "tidy-keep", "confirm:Move 1 item to the trash?"}))
		Expect(filepath.Join(proj, ".claude", "skills", "old")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(proj, ".claude", "commands", "tmp.md")).To(BeARegularFile())
		Expect(os.ReadFile(filepath.Join(tmp, "config", "lola", "keep.yaml"))).To(Equal([]byte(keep)))
		Expect(r.out + r.err).NotTo(ContainSubstring("skill/zzz"))
	})

	It("the review prints the equivalent command only when it selects exactly the chosen set", func() {
		writeTree(proj, map[string]string{".claude/skills/old/SKILL.md": "x\n", ".claude/commands/dup.md": "x\n", ".claude/commands/dup/ref.md": "x\n"})
		p := &scripted{tidyPick: tidyPicks("skill/old"), tidyKeep: tidyKeeps(), confirm: []bool{false}}
		r := lolaP(p, "tidy", "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("Equivalent command:\n  lola tidy -C " + proj + " skill/old -t claude-code\n"))
		Expect(r.err).NotTo(ContainSubstring("no single command"))

		p = &scripted{tidyPick: tidyPicks(".claude/commands/dup.md"), tidyKeep: tidyKeeps(), confirm: []bool{false}}
		r = lolaP(p, "tidy", "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("no single command selects this set\n"), "command/dup also names its sidecar directory")
		Expect(r.err).NotTo(ContainSubstring("Equivalent command"))

		By("the keep answers join the equivalent command as --keep")
		writeTree(proj, map[string]string{".claude/agents/z.md": "x\n"})
		p = &scripted{tidyPick: tidyPicks("skill/old"), tidyKeep: tidyKeeps("agent/z"), confirm: []bool{false}}
		r = lolaP(p, "tidy", "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("Equivalent command:\n  lola tidy -C " + proj + " skill/old -t claude-code --keep agent/z\n"))

		By("a keep-only answer's equivalent is the keep-only run")
		p = &scripted{tidyPick: tidyPicks(), tidyKeep: tidyKeeps("agent/z"), confirm: []bool{false}}
		r = lolaP(p, "tidy", "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"tidy-pick", "tidy-keep", "confirm:Keep 1?"}))
		Expect(r.err).To(ContainSubstring("Equivalent command:\n  lola tidy -C " + proj + " -t claude-code --keep agent/z\n"))

		By("an equivalent command lola cannot print is said to exist, not denied")
		odd := filepath.Join(tmp, "pro\u202ej")
		writeTree(odd, map[string]string{".claude/skills/old/SKILL.md": "x\n"})
		p = &scripted{tidyPick: tidyPicks("skill/old"), tidyKeep: tidyKeeps(), confirm: []bool{false}}
		r = lolaP(p, "tidy", "-C", odd, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("lola cannot show the equivalent command: it holds characters lola does not print or a word too long to print\n"))
		Expect(r.err).NotTo(ContainSubstring("no single command"))
		Expect(r.err).NotTo(ContainSubstring("Equivalent command"))

		By("so is one whose project root is a word too long to print")
		long := filepath.Join(tmp, strings.Repeat("d", 100), strings.Repeat("e", 100), strings.Repeat("f", 100))
		Expect(len(long)).To(BeNumerically(">", untrusted.MaxName))
		writeTree(long, map[string]string{".claude/skills/old/SKILL.md": "x\n"})
		p = &scripted{tidyPick: tidyPicks("skill/old"), tidyKeep: tidyKeeps(), confirm: []bool{false}}
		r = lolaP(p, "tidy", "-C", long, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("lola cannot show the equivalent command: it holds characters lola does not print or a word too long to print\n"))
		Expect(r.err).NotTo(ContainSubstring("no single command"))
		Expect(r.err).NotTo(ContainSubstring("Equivalent command"))

		By("the review of a named run prints neither")
		p = &scripted{confirm: []bool{false}}
		r = lolaP(p, "tidy", "skill/old", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).NotTo(ContainSubstring("Equivalent command"))
		Expect(r.err).NotTo(ContainSubstring("no single command"))
	})

	It("--json on a terminal never opens the picker: it refuses as off one, its hint keeping --json", func() {
		writeTree(proj, map[string]string{".claude/skills/old/SKILL.md": "x\n"})
		p := &scripted{}
		r := lolaP(p, "tidy", "-C", proj, "--json")
		Expect(r.code).To(Equal(1))
		Expect(p.calls).To(BeEmpty())
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.err), &doc)).To(Succeed(), r.err)
		Expect(doc["error"]).To(Equal("lola tidy needs names or --all when it cannot ask (off a terminal or with --json): 1 item can move"))
		Expect(doc["hints"]).To(Equal([]any{"lola tidy -C " + proj + " --all -y --json"}))
		Expect(filepath.Join(proj, ".claude", "skills", "old")).To(BeADirectory())
	})

	It("-y on a terminal without names still runs the picker", func() {
		writeTree(proj, map[string]string{".claude/skills/old/SKILL.md": "x\n", ".claude/commands/tmp.md": "x\n"})
		p := &scripted{tidyPick: tidyPicks("skill/old"), tidyKeep: tidyKeeps()}
		r := lolaP(p, "tidy", "-y", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"tidy-pick", "tidy-keep"}), "-y answers only the review")
		Expect(r.out).To(ContainSubstring("✓ moved 1 item"))
		Expect(filepath.Join(proj, ".claude", "skills", "old")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(proj, ".claude", "commands", "tmp.md")).To(BeAnExistingFile())

		By("--force never runs it, on a terminal too")
		p = &scripted{}
		r = lolaP(p, "tidy", "--force", "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: --force needs names or --all: 1 item can move\n"))
		Expect(p.calls).To(BeEmpty())

		By("off a terminal it still refuses with the --all -y hint")
		r = lola("tidy", "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: lola tidy needs names or --all when it cannot ask (off a terminal or with --json): 1 item can move\n"))
		Expect(r.err).To(ContainSubstring("  hint: lola tidy -C " + proj + " --all -y\n"))
	})

	It("-n on a terminal hints the run without -n, which asks: no -y", func() {
		writeTree(proj, map[string]string{".claude/skills/old/SKILL.md": "x\n"})
		p := &scripted{}
		r := lolaP(p, "tidy", "-n", "skill/old", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ lola tidy -C " + proj + " skill/old\n"))
		Expect(p.calls).To(BeEmpty())
		r = lolaP(p, "tidy", "-n", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ lola tidy -C "+proj+"\n"), "without names the run it names runs the picker")
		Expect(p.calls).To(BeEmpty())
		r = lola("tidy", "-n", "skill/old", "-C", proj)
		Expect(r.out).To(ContainSubstring("→ lola tidy -C "+proj+" skill/old -y\n"), "off a terminal the run needs -y")
	})

	It("a hint keeps --json, --plain and --no-pager as given, and never -n", func() {
		writeTree(proj, map[string]string{".claude/skills/old/SKILL.md": "x\n"})
		r := lola("tidy", "-C", proj, "--plain", "--no-pager")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("  hint: lola tidy -C " + proj + " --all -y --plain --no-pager\n"))
		r = lola("tidy", "-n", "-C", proj, "--json")
		Expect(r.code).To(Equal(0), r.err)
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		Expect(doc["hints"]).To(Equal([]any{"lola tidy -C " + proj + " --all -y --json"}))
		words := shellWords(doc["hints"].([]any)[0].(string))
		r = lola(words[1:]...)
		Expect(r.code).To(Equal(0), r.err)
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		Expect(doc["moved"]).To(HaveLen(1), "the hint, pasted, is the run it names")
	})

	It("asks the review on a terminal when names are given, and declining moves nothing", func() {
		writeTree(proj, map[string]string{".claude/skills/old/SKILL.md": "x\n", ".claude/commands/tmp.md": "x\n"})
		p := &scripted{confirm: []bool{false}}
		r := lolaP(p, "tidy", "skill/old", "--keep", "command/tmp", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Move 1 item to the trash and keep 1?"}))
		Expect(r.err).To(ContainSubstring("→ claude-code  skill/old  .claude/skills/old"))
		Expect(r.err).To(ContainSubstring("aborted; nothing changed."))
		Expect(filepath.Join(proj, ".claude", "skills", "old")).To(BeADirectory())
		Expect(filepath.Join(tmp, "config", "lola", "keep.yaml")).NotTo(BeAnExistingFile())

		p = &scripted{confirm: []bool{true}}
		r = lolaP(p, "tidy", "skill/old", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✓ moved 1 item (2 B) to the trash: "))
	})

	It("the review question counts and lists exactly the keep-list edits the run makes", func() {
		writeTree(proj, map[string]string{".claude/skills/old/SKILL.md": "x\n"})
		writeTree(filepath.Join(tmp, "config", "lola"), map[string]string{"keep.yaml": "version: 1\nprojects:\n  " + proj +
			":\n    - command/tmp-2\n    - command/gone\n"})
		p := &scripted{confirm: []bool{false}}
		r := lolaP(p, "tidy", "skill/old", "--keep", "skill/n*", "--keep", "skill/n*", "--keep", "command/tmp-2", "--unkeep", "command/gone", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Move 1 item to the trash and keep 1 and stop keeping 1?"}))
		Expect(r.err).To(ContainSubstring("  → keep skill/n*\n"))
		Expect(r.err).To(ContainSubstring("  → unkeep command/gone\n"))
		Expect(r.err).NotTo(ContainSubstring("keep command/tmp-2"), "already on the keep-list")
		Expect(strings.Count(r.err, "keep skill/n*")).To(Equal(1))
	})

	It("a failed run still prints its keep-list edits and where what moved is", func() {
		writeTree(proj, map[string]string{".claude/skills/old/SKILL.md": "x\n", ".claude/commands/tmp.md": "x\n"})
		commands := filepath.Join(proj, ".claude", "commands")
		Expect(os.Chmod(commands, 0o555)).To(Succeed())
		DeferCleanup(os.Chmod, commands, os.FileMode(0o755))
		r := lola("tidy", "--all", "-y", "--keep", "command/zz", "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("permission denied"))
		Expect(r.err).To(ContainSubstring("keep-list: added command/zz (project " + proj + ")\n"))
		Expect(r.err).To(ContainSubstring("what moved is in " + filepath.Join(tmp, "state", "lola", "trash")))
	})

	It("a printed hint works as the user pastes it", func() {
		writeTree(proj, map[string]string{".claude/skills/a b'c/SKILL.md": "x\n", ".claude/commands/tmp-1.md": "x\n", ".claude/agents/z.md": "x\n"})
		r := lola("tidy", "command/tmp-*", "skill/a b'c", "-C", proj, "-t", "claude-code", "--keep", "agent/z")
		Expect(r.code).To(Equal(1))
		var hint string
		for _, l := range strings.Split(r.err, "\n") {
			if h, ok := strings.CutPrefix(l, "  hint: "); ok {
				hint = h
			}
		}
		Expect(hint).To(Equal("lola tidy -C " + proj + ` 'command/tmp-*' 'skill/a b'\''c' -t claude-code --keep agent/z -y`))
		words := shellWords(hint)
		Expect(words[0]).To(Equal("lola"))
		r = lola(words[1:]...)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✓ moved 2 items"))
		Expect(filepath.Join(proj, ".claude", "skills", "a b'c")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(proj, ".claude", "agents", "z.md")).To(BeAnExistingFile(), "kept, as the hint's --keep says")
		Expect(r.out).To(ContainSubstring("keep-list: added agent/z"))
	})

	It("--scope both is refused with the run at each scope", func() {
		r := lola("tidy", "--scope", "both", "-C", proj, "skill/old")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: lola tidy acts on one scope per run\n"))
		Expect(r.err).To(ContainSubstring("  hint: lola tidy -C " + proj + " skill/old\n"))
		Expect(r.err).To(ContainSubstring("  hint: lola tidy --user skill/old\n"))
	})
})
