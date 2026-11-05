package cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("P2-6: a run's notices: safety ones in full, the rest one line per module", func() {
	var tmp, proj, mod, summary string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, mod = filepath.Join(tmp, "proj"), filepath.Join(tmp, "m")
		// 15 MCP servers with a cwd Claude Code drops, 15 agents with a key Gemini CLI drops, and
		// runtime hooks: declined off a terminal for claude-code, and with no place in gemini-cli.
		servers := map[string]any{}
		files := map[string]string{
			".claude-plugin/plugin.json": `{"name":"m"}`,
			"hooks/hooks.json":           `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/stop.sh"}]}]}}`,
		}
		for i := 1; i <= 15; i++ {
			servers[fmt.Sprintf("s%02d", i)] = map[string]any{"command": "srv", "cwd": "/srv"}
			files[fmt.Sprintf("agents/a%02d.md", i)] = "---\ndescription: a\nfoo: 1\n---\nBody\n"
		}
		doc, err := json.Marshal(map[string]any{"mcpServers": servers})
		Expect(err).NotTo(HaveOccurred())
		files[".mcp.json"] = string(doc)
		writeTree(mod, files)
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		summary = "→ m: 30 notices (claude-code 15, gemini-cli 15) — lola check -C " + proj + " m -t claude-code,gemini-cli\n"
	})

	// safety is what every human run prints in full: both runtime-hook findings.
	safety := func(out string) {
		GinkgoHelper()
		Expect(out).To(ContainSubstring("⚠ m: claude-code: runtime hooks skipped: they run inside claude-code with your permissions"))
		Expect(out).To(ContainSubstring("✗ skipped m: gemini-cli runtime-hook hooks/hooks.json: gemini-cli has no place for runtime-hooks"))
	}
	// detail is the capability findings as -v prints them: grouped, without the author's hints.
	detail := func(out string) {
		GinkgoHelper()
		Expect(out).To(ContainSubstring("⚠ m: gemini-cli agent: foo dropped: gemini-cli does not support it (15 agents: a01, a02, a03, …; --json lists each)\n"))
		Expect(out).To(ContainSubstring("⚠ m: claude-code mcp: claude-code has no working-directory setting; cwd \"/srv\" dropped (15 MCP servers: s01, s02, s03, …; --json lists each)\n"))
		Expect(out).NotTo(ContainSubstring("targets.yaml"), "a hint for the module's author is lola check's")
		Expect(out).NotTo(ContainSubstring("notices ("))
	}

	It("install prints the runtime hooks in full and one line for the 30 capability notices, whose lola check lists them with the author's hints", func() {
		r := lola("install", mod, "-C", proj, "-t", "claude-code,gemini-cli")
		Expect(r.code).To(Equal(0), r.err)
		safety(r.out)
		Expect(r.out).To(ContainSubstring(summary))
		Expect(r.out).NotTo(ContainSubstring("dropped"))
		Expect(r.out).NotTo(ContainSubstring("targets.yaml"))

		hint := strings.Fields(strings.TrimSuffix(strings.SplitN(summary, " — ", 2)[1], "\n"))
		Expect(hint[0]).To(Equal("lola"))
		c := lola(hint[1:]...)
		Expect(c.code).To(Equal(0), c.err)
		Expect(c.out).To(ContainSubstring("⚠ m: gemini-cli agent a01: foo dropped: gemini-cli does not support it\n" +
			"    hint: leave foo out for gemini-cli: in a01.targets.yaml beside the agent, add gemini-cli: {foo: null}\n"))
		Expect(c.out).To(ContainSubstring("⚠ m: claude-code mcp s15: claude-code has no working-directory setting"))
	})

	// runs runs the summary line's lola check, which must list the module's capability findings.
	runs := func(out string) {
		GinkgoHelper()
		_, after, ok := strings.Cut(out, "→ m: 30 notices (claude-code 15, gemini-cli 15) — ")
		Expect(ok).To(BeTrue(), out)
		line, _, _ := strings.Cut(after, "\n")
		c := runHint(line)
		Expect(c.code).To(Equal(0), c.err)
		Expect(c.out).To(ContainSubstring("⚠ m: gemini-cli agent a01: foo dropped: gemini-cli does not support it\n"))
	}

	It("I-1: install -n of a source your cache lacks names the source in its lola check, which runs", func() {
		r := lola("install", mod, "-C", proj, "-t", "claude-code,gemini-cli", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ m: 30 notices (claude-code 15, gemini-cli 15) — lola check -C " + proj + " " + mod + " -t claude-code,gemini-cli\n"))
		runs(r.out)
		Expect(lola("cache", "list", "--plain").out).NotTo(ContainSubstring("m\t"), "the check fetched it without caching it")
	})

	It("I-1: sync -n of a fresh project whose modules your cache lacks names their sources in its lola check, which runs", func() {
		inside := filepath.Join(proj, "mods", "m")
		Expect(os.CopyFS(inside, os.DirFS(mod))).To(Succeed())
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code, gemini-cli]\nmodules:\n  - name: m\n    source: ./mods/m\n"})
		r := lola("sync", "-C", proj, "-n", "--trust")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("— lola check -C " + proj + " " + inside + " -t claude-code,gemini-cli\n"))
		runs(r.out)
	})

	It("-v prints every notice, grouped as without it, and never the hints for the module's author", func() {
		r := lola("install", mod, "-C", proj, "-t", "claude-code,gemini-cli", "-n", "-v")
		Expect(r.code).To(Equal(0), r.err)
		safety(r.out)
		detail(r.out)
		r = lola("install", mod, "-C", proj, "-t", "claude-code,gemini-cli", "-n", "--verbose")
		Expect(r.code).To(Equal(0), r.err)
		detail(r.out)
	})

	It("--json keeps every notice, each with its class", func() {
		r := lola("install", mod, "-C", proj, "-t", "claude-code,gemini-cli", "-n", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var doc struct {
			Findings []map[string]any `json:"findings"`
			Notices  []map[string]any `json:"notices"`
		}
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		classes := map[string]int{}
		for _, f := range doc.Findings {
			classes[fmt.Sprint(f["target"], " ", f["kind"], " ", f["class"])]++
		}
		Expect(classes).To(Equal(map[string]int{"claude-code mcp detail": 15, "gemini-cli agent detail": 15,
			"claude-code runtime-hook safety": 1, "gemini-cli runtime-hook safety": 1,
			"claude-code assistant safety": 1, "gemini-cli assistant safety": 1}), r.out)
		Expect(doc.Findings).To(ContainElement(HaveKeyWithValue("hint", "leave foo out for gemini-cli: in a01.targets.yaml beside the agent, add gemini-cli: {foo: null}")),
			"JSON keeps the author's hints")
		for _, n := range doc.Notices {
			Expect(n).To(HaveKey("class"))
		}
	})

	It("sync, update and a profile switch summarise the same way, and -v lists them", func() {
		Expect(lola("install", mod, "-C", proj, "-t", "claude-code,gemini-cli").code).To(Equal(0))
		for _, run := range [][]string{{"sync", "-C", proj}, {"sync", "-C", proj, "-n"}, {"update", "-C", proj}} {
			r := lola(run...)
			Expect(r.code).To(Equal(0), r.err)
			safety(r.out)
			Expect(r.out).To(ContainSubstring(summary), strings.Join(run, " "))
			Expect(r.out).NotTo(ContainSubstring("dropped"))

			r = lola(append(run, "-v")...)
			Expect(r.code).To(Equal(0), r.err)
			safety(r.out)
			detail(r.out)
		}

		Expect(lola("uninstall", "m", "-C", proj, "-y").code).To(Equal(0))
		profiles := filepath.Join(tmp, "config", "lola", "profiles")
		writeTree(profiles, map[string]string{"p.yml": "modules:\n  - name: m\n    source: " + mod + "\n    targets: [claude-code, gemini-cli]\n"})
		r := lola("profile", "p", "--switch", "-C", proj, "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(summary))
		Expect(r.out).NotTo(ContainSubstring("dropped"))
		r = lola("profile", "p", "--switch", "-C", proj, "-n", "-v")
		Expect(r.code).To(Equal(0), r.err)
		detail(r.out)
	})

	It("R-1: the notice that picks do not narrow runtime hooks is a safety notice, printed once per module", func() {
		r := lola("install", mod, "-C", proj, "-t", "claude-code,gemini-cli", "--skip", "agent/a01")
		Expect(r.code).To(Equal(0), r.err)
		notice := "→ m: runtime hooks are installed for the whole module; picks do not narrow them\n"
		Expect(strings.Count(r.out, notice)).To(Equal(1), r.out)
		r = lola("sync", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(strings.Count(r.out, notice)).To(Equal(1), r.out)
		r = lola("sync", "-C", proj, "--json")
		Expect(r.code).To(Equal(0), r.err)
		var doc struct {
			Notices []map[string]any `json:"notices"`
		}
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		Expect(doc.Notices).To(ConsistOf(map[string]any{"level": "info", "class": "safety",
			"text": "m: runtime hooks are installed for the whole module; picks do not narrow them"}))
	})
})

var _ = Describe("I-1: the summary's lola check checks the version installed, else the notices print in full", func() {
	var tmp, proj string
	// m drops an agent key for gemini-cli and mentions a skill the run skips: a finding and a
	// mention notice, both detail.
	m := map[string]string{"lola.yml": "name: m\n", "agents/a.md": "---\ndescription: a\nfoo: 1\n---\nBody\n",
		"skills/skill-one/SKILL.md": "---\ndescription: One\n---\nPairs with skill-two.\n", "skills/skill-two/SKILL.md": "---\ndescription: Two\n---\nTwo.\n"}
	full := func(out string) {
		GinkgoHelper()
		Expect(out).To(ContainSubstring("⚠ m: gemini-cli agent a: foo dropped: gemini-cli does not support it\n"))
		Expect(out).To(ContainSubstring("→ m: skill/skill-one mentions 1 component you did not pick: skill/skill-two"))
		Expect(out).NotTo(ContainSubstring("notices ("))
	}
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
	})

	It("I-1: a folder in the cwd named like the module, which lola check would read instead, prints the notices in full", func() {
		mod := filepath.Join(tmp, "src", "m")
		writeTree(mod, m)
		writeTree(filepath.Join(proj, "m"), map[string]string{"lola.yml": "name: m\n"})
		GinkgoT().Chdir(proj)
		r := lola("install", mod, "--project", "-t", "gemini-cli", "--only", "skill/skill-one", "--only", "agent/a")
		Expect(r.code).To(Equal(0), r.err)
		full(r.out)
		r = lola("sync", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("⚠ m: gemini-cli agent a: foo dropped: gemini-cli does not support it\n"))
		Expect(r.out).NotTo(ContainSubstring("notice ("))

		Expect(os.RemoveAll(filepath.Join(proj, "m"))).To(Succeed())
		r = lola("sync", "--project")
		Expect(r.code).To(Equal(0), r.err)
		_, hint, ok := strings.Cut(r.out, "→ m: 1 notice (gemini-cli 1) — ")
		Expect(ok).To(BeTrue(), "with no folder in the way the summary names the cache: "+r.out)
		hint, _, _ = strings.Cut(hint, "\n")
		c := runHint(hint)
		Expect(c.code).To(Equal(0), c.err)
		Expect(c.out).To(ContainSubstring("⚠ m: gemini-cli agent a: foo dropped: gemini-cli does not support it\n"))
	})

	It("I-1: a version pinned behind the cache's head, which lola check would read instead, prints the notices in full", func() {
		work := filepath.Join(tmp, "work")
		writeTree(work, m)
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "v1")
		bare := filepath.Join(tmp, "srv", "m.git")
		Expect(exec.Command("git", "clone", "-q", "--bare", work, bare).Run()).To(Succeed())
		url := testenv.ServeGit(filepath.Join(tmp, "srv")) + "/m.git"
		r := lola("install", url, "-C", proj, "-t", "gemini-cli", "--only", "skill/skill-one", "--only", "agent/a")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ m: 2 notices (gemini-cli 1, module 1) — lola check -C " + proj + " m -t gemini-cli\n"))

		writeTree(work, map[string]string{"agents/a.md": "---\ndescription: a\n---\nBody\n", "skills/skill-one/SKILL.md": "---\ndescription: One\n---\nAlone.\n"})
		gitIn(work, "commit", "-q", "-am", "v2")
		gitIn(work, "push", "-q", bare, "main")
		Expect(lola("cache", "add", url).code).To(Equal(0))
		r = lola("sync", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("⚠ m: gemini-cli agent a: foo dropped: gemini-cli does not support it\n"), "the pin installs v1")
		Expect(r.out).NotTo(ContainSubstring("notice ("), "lola check m reads v2, the cache's head")
	})
})

var _ = Describe("M-1: --scope both summarises each scope's notices on its own line", func() {
	It("names each scope's lola check", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, mod := filepath.Join(tmp, "proj"), filepath.Join(tmp, "m")
		writeTree(mod, map[string]string{"lola.yml": "name: m\n", "agents/a.md": "---\ndescription: a\nfoo: 1\n---\nBody\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		GinkgoT().Chdir(proj)
		r := lola("install", mod, "--scope", "both", "-t", "gemini-cli", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ m: 1 notice (gemini-cli 1) — lola check --project " + mod + " -t gemini-cli\n"))
		Expect(r.out).To(ContainSubstring("→ m: 1 notice (gemini-cli 1) — lola check --user " + mod + " -t gemini-cli\n"))
	})
})

var _ = Describe("M-2: a safety finding with an author's fix points to the module's lola check", func() {
	It("M-2: install, sync, update and a profile switch name the check before --force", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, mod := filepath.Join(tmp, "proj"), filepath.Join(tmp, "m")
		writeTree(mod, map[string]string{"lola.yml": "name: m\n", "agents/a.md": "---\ndescription: a\ntools: [Read]\n---\nBody\n"})
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		check := "lola check -C " + proj + " m -t gemini-cli"
		skipped := "✗ skipped m: gemini-cli agent a: gemini-cli cannot enforce this agent's tool list; without it the agent can use every tool\n"
		r := lola("install", mod, "-C", proj, "-t", "gemini-cli")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(skipped + "    hint: the module's own fix: " + check + "; or render it anyway: lola install " + mod + " --root " + proj + " --target gemini-cli --force\n"))
		for _, run := range [][]string{{"sync", "-C", proj}, {"update", "-C", proj}} {
			r = lola(run...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring(skipped+"    hint: the module's own fix: "+check), strings.Join(run, " "))
		}
		c := runHint(check)
		Expect(c.code).To(Equal(0), c.err)
		Expect(c.out).To(ContainSubstring("under gemini-cli: in the agent's .targets.yaml, list its tools by gemini-cli's names"), "the check names the author's fix")

		Expect(lola("uninstall", "m", "-C", proj, "-y").code).To(Equal(0))
		writeTree(filepath.Join(tmp, "config", "lola", "profiles"), map[string]string{"p.yml": "modules:\n  - name: m\n    source: " + mod + "\n    targets: [gemini-cli]\n"})
		r = lola("profile", "p", "--switch", "-C", proj, "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(skipped + "    hint: the module's own fix: " + check))
	})
})

var _ = Describe("install -t gemini-cli,agents says why gemini-cli has no copy of a skill", func() {
	It("counts skill-via-agents in the one-line summary and prints it in full with -v", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, mod := filepath.Join(tmp, "proj"), filepath.Join(tmp, "m")
		writeTree(mod, map[string]string{"lola.yml": "name: m\n", "skills/review/SKILL.md": "---\ndescription: Review code\n---\nReview it.\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		r := lola("install", mod, "-C", proj, "-t", "gemini-cli,agents")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out + r.err).To(ContainSubstring("→ m: 1 notice (gemini-cli 1) — lola check -C " + proj + " m -t gemini-cli\n"))
		Expect(r.out+r.err).NotTo(ContainSubstring("delivered through"), "detail: counted, not printed")

		Expect(os.RemoveAll(filepath.Join(proj, ".agents"))).To(Succeed())
		Expect(os.RemoveAll(filepath.Join(proj, ".lola"))).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		r = lola("install", mod, "-C", proj, "-t", "gemini-cli,agents", "-v")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out + r.err).To(ContainSubstring("→ m: gemini-cli skill review: delivered through .agents/skills, which gemini-cli reads\n"))
	})
})
