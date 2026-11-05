package cli_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// infoKit declares an install hook that leaves a marker where it runs, a Claude Code runtime hook
// and an MCP server.
var infoKit = map[string]string{
	"lola.yml":         "name: kit\nversion: 1.0.0\nhooks: {pre-install: setup.sh}\ncomponents:\n  runtime_hooks:\n    - {path: hooks/hooks.json}\n",
	"setup.sh":         "#!/bin/sh\ntouch \"$LOLA_PROJECT_PATH/hook-ran\"\n",
	"hooks/hooks.json": `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/stop.sh"}]}]}}`,
	"commands/ship.md": "Ship\n",
	"mcps.json":        `{"mcpServers":{"tools":{"command":"tools-mcp","args":["--stdio"]}}}`,
}

// infoDoc is the part of lola info's JSON these specs read.
type infoDoc struct {
	Name        string `json:"name"`
	Digest      string `json:"digest"`
	NoScope     string `json:"no_scope"`
	NoAssistant string `json:"no_assistant"`
	Hints       []string
	Install     []struct {
		Scope      string            `json:"scope"`
		Targets    []string          `json:"targets"`
		Steps      []json.RawMessage `json:"steps"`
		Command    string            `json:"command"`
		NewProject bool              `json:"new_project"`
	} `json:"install"`
}

func readInfo(out string) infoDoc {
	GinkgoHelper()
	var doc infoDoc
	Expect(json.Unmarshal([]byte(out), &doc)).To(Succeed(), out)
	return doc
}

// runHint runs a hint's command line in-process, off a terminal.
func runHint(hint string) result {
	GinkgoHelper()
	words := shellWords(hint)
	Expect(words[0]).To(Equal("lola"), hint)
	return lola(words[1:]...)
}

var _ = Describe("lola info", func() {
	var tmp, proj, kit string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, kit = filepath.Join(tmp, "proj"), filepath.Join(tmp, "src", "kit")
		writeTree(kit, infoKit)
		Expect(os.Chmod(filepath.Join(kit, "setup.sh"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
	})

	It("info shows each hook's sha256 and first lines cleaned, and runtime hooks and MCP servers as the assistant would run them", func() {
		writeTree(kit, map[string]string{"setup.sh": "#!/bin/sh\necho \x1b[2Jcleared\n"})
		r := lola("info", kit, "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`\n  pre-install hook setup\.sh \(sha256 [0-9a-f]{64}\), on install with your consent:\n` +
			`    \| #!/bin/sh\n    \| echo \?\[2Jcleared\n`))
		Expect(r.out).To(ContainSubstring("  runtime hooks, run by claude-code on its events, installed with your consent:\n    Stop: command\n" +
			"      runs: export CLAUDE_PLUGIN_ROOT='" + proj + "/.lola/modules/kit'"))
		Expect(r.out).To(ContainSubstring("  MCP server tools, run by claude-code, runs: tools-mcp --stdio\n"))
		Expect(r.out).NotTo(ContainSubstring("\x1b"))
	})

	It("info's hint installs with exactly the kinds needed and keeps the run's flags", func() {
		r := lola("info", kit, "-C", proj, "-t", "claude-code", "--json")
		Expect(r.code).To(Equal(0), r.err)
		doc := readInfo(r.out)
		Expect(doc.Install[0].Command).To(Equal("lola install kit --json --root " + proj + " --target claude-code --trust=hooks,runtime-hooks"))
		Expect(lola("info", kit, "-C", proj, "-t", "claude-code").out).To(HaveSuffix(
			"→ install it: lola install kit --root " + proj + " --target claude-code --trust=hooks,runtime-hooks\n"))
		r = runHint(doc.Install[0].Command)
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(proj, "hook-ran")).To(BeARegularFile())
		Expect(filepath.Join(proj, ".claude", "settings.local.json")).To(BeARegularFile())

		r = lola("info", kit, "-C", proj, "-t", "claude-code", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("consent\tproject\thooks\tkit pre-install\tyes\n"))
		Expect(r.out).To(HaveSuffix("install\tlola install kit --plain --root "+proj+" --target claude-code\n"), "nothing more to trust")

		for _, row := range []struct {
			flags []string
			want  string
		}{
			{[]string{"-C", proj, "--no-pager"}, "lola install kit --json --no-pager --root " + proj + " --target claude-code"},
			{[]string{"--user"}, "lola install kit --json --target claude-code --user --trust=runtime-hooks"},
			{[]string{"--scope", "user"}, "lola install kit --json --scope user --target claude-code"},
		} {
			r = lola(append([]string{"info", kit, "-t", "claude-code", "--json"}, row.flags...)...)
			Expect(r.code).To(Equal(0), r.err)
			hint := readInfo(r.out).Install[0].Command
			Expect(hint).To(Equal(row.want), "%v", row.flags)
			r = runHint(hint)
			Expect(r.code).To(Equal(0), r.err)
		}
	})

	DescribeTable("info's hint installs the digest info showed",
		func(arg func() string, after func()) {
			r := lola("info", arg(), "-C", proj, "-t", "claude-code", "--json")
			Expect(r.code).To(Equal(0), r.err)
			doc := readInfo(r.out)
			after()
			r = runHint(doc.Install[0].Command)
			Expect(r.code).To(Equal(0), r.err)
			l, err := ledger.Load(filepath.Join(proj, ".lola", "lock.json"))
			Expect(err).NotTo(HaveOccurred())
			Expect(l.Modules[doc.Name].Digest).To(Equal(doc.Digest))
		},
		Entry("a git branch moved after info: the hint's --ref <sha> installs the old commit", func() string {
			work := filepath.Join(tmp, "work")
			writeTree(work, infoKit)
			Expect(os.Chmod(filepath.Join(work, "setup.sh"), 0o755)).To(Succeed())
			gitIn(work, "init", "-q", "-b", "main")
			gitIn(work, "add", "-A")
			gitIn(work, "commit", "-q", "-m", "v1")
			gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "kit.git"))
			return testenv.ServeGit(filepath.Join(tmp, "srv")) + "/kit.git"
		}, func() {
			work := filepath.Join(tmp, "work")
			writeTree(work, map[string]string{"commands/ship.md": "Ship v2\n"})
			gitIn(work, "commit", "-q", "-am", "v2")
			gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "kit.git"), "main")
		}),
		Entry("an archive: the cached name", func() string {
			archive := filepath.Join(tmp, "kit.zip")
			writeZip(archive, map[string]string{"kit/lola.yml": "name: kit\n", "kit/commands/ship.md": "Ship\n"})
			return archive
		}, func() {
			writeZip(filepath.Join(tmp, "kit.zip"), map[string]string{"kit/lola.yml": "name: kit\n", "kit/commands/ship.md": "Ship v2\n"})
		}),
		Entry("a folder edited after info: the cached name installs what info showed", func() string { return kit }, func() {
			writeTree(kit, map[string]string{"commands/ship.md": "Ship v2\n"})
		}),
		Entry("an @market entry: its git source at the commit the catalog pins", func() string {
			work := filepath.Join(tmp, "work")
			files := map[string]string{".claude-plugin/marketplace.json": `{"name": "acme", "owner": {"name": "Acme"}, "plugins": [{"name": "kit", "source": "./kit"}]}`}
			for k, v := range infoKit {
				files["kit/"+k] = v
			}
			writeTree(work, files)
			Expect(os.Chmod(filepath.Join(work, "kit", "setup.sh"), 0o755)).To(Succeed())
			gitIn(work, "init", "-q", "-b", "main")
			gitIn(work, "add", "-A")
			gitIn(work, "commit", "-q", "-m", "v1")
			gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "acme.git"))
			r := lola("market", "--add", testenv.ServeGit(filepath.Join(tmp, "srv"))+"/acme.git")
			Expect(r.code).To(Equal(0), r.err)
			return "@acme/kit"
		}, func() {
			work := filepath.Join(tmp, "work")
			writeTree(work, map[string]string{"kit/commands/ship.md": "Ship v2\n"})
			gitIn(work, "commit", "-q", "-am", "v2")
			gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "acme.git"), "main")
		}),
	)

	It("info shows a refusal and the plan's findings under their scope, and a refusal on one --plain row", func() {
		r := lola("info", kit, "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("\n  ⚠ kit: claude-code: runtime hooks skipped"), "a finding is indented under its scope")
		writeTree(proj, map[string]string{".claude/commands/ship.md": "mine\n"})
		r = lola("info", kit, "-C", proj, "-t", "claude-code", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("refusal\tproject\trefusing to change anything: 1 conflict, which --force overrides; " +
			"claude-code: .claude/commands/ship.md: exists and was not written by lola\n"))
		r = lola("info", kit, "-C", proj, "-t", "claude-code")
		Expect(r.out).To(ContainSubstring("  ✗ installing here would refuse: refusing to change anything: 1 conflict, which --force overrides\n" +
			"    claude-code: .claude/commands/ship.md: exists and was not written by lola\n" +
			"    hint: lola install kit --root " + proj + " --target claude-code --trust=hooks,runtime-hooks --force\n"))
		Expect(r.out).NotTo(ContainSubstring("install it:"))
	})

	It("info --plain has a row for each refusal hint, runtime hook and MCP server, and for no scope or no assistant", func() {
		writeTree(proj, map[string]string{".claude/commands/ship.md": "mine\n"})
		r := lola("info", kit, "-C", proj, "-t", "claude-code", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("\nrefusal\tproject\trefusing to change anything: 1 conflict, which --force overrides; " +
			"claude-code: .claude/commands/ship.md: exists and was not written by lola\n" +
			"hint\tlola install kit --plain --root " + proj + " --target claude-code --trust=hooks,runtime-hooks --force\n"))
		Expect(r.out).To(ContainSubstring("\nruntime-hook\tkit\tclaude-code\tStop\tcommand\tunknown\t\n"))
		Expect(r.out).To(ContainSubstring("\nmcp\ttools\tclaude-code\ttools-mcp --stdio\t\n"))

		dir := filepath.Join(tmp, "work", "x")
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		GinkgoT().Chdir(dir)
		r = lola("info", kit, "-t", "claude-code", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HaveSuffix("no_scope\tno .lola/ was found in " + dir + " or above it, and config.yaml sets no default_scope\n" +
			"hint\tlola info " + kit + " --plain --target claude-code --project\nhint\tlola info " + kit + " --plain --target claude-code --user\n"))
		r = lola("info", kit, "--project", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HaveSuffix("no_assistant\tno assistant found on your PATH, so lola does not know where to install\n"))
	})

	It("info --json is one document whose arrays are never null", func() {
		for _, args := range [][]string{{"info", kit, "-C", proj, "-t", "claude-code", "--json"}, {"info", "kit", "--json"}} {
			r := lola(args...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).NotTo(ContainSubstring("null"))
			dec := json.NewDecoder(strings.NewReader(r.out))
			var doc map[string]any
			Expect(dec.Decode(&doc)).To(Succeed())
			Expect(dec.Decode(&doc)).To(MatchError(io.EOF), "one document")
			Expect(doc).To(HaveKey("runs"))
			Expect(doc).To(HaveKey("install"))
		}
	})

	It("info refuses no argument and a non-source with their exact texts", func() {
		r := lola("info")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: lola info needs a module or source\n  hint: cached modules: lola cache list\n"))
		r = lola("info", "--json")
		Expect(r.err).To(Equal(`{"error":"lola info needs a module or source","hints":["cached modules: lola cache list --json"]}` + "\n"))
		r = lola("info", "./nope", "--plain")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal(`error: "./nope" is neither a module in your cache nor a source; sources are ` + source.AcceptedForms + "\n" +
			"  hint: cached modules: lola cache list --plain\n"))
	})

	It("cache add and cache list JSON keep the module identity and contents", func() {
		keys := []string{"name", "description", "version", "digest", "format", "source", "type", "heads", "skills", "commands", "agents",
			"mcp_servers", "rules", "hooks", "warnings", "vars", "templates", "partials", "attributes"}
		r := lola("cache", "add", kit, "--json")
		Expect(r.code).To(Equal(0), r.err)
		var added map[string]any
		Expect(json.Unmarshal([]byte(r.out), &added)).To(Succeed())
		var listed struct{ Modules []map[string]any }
		Expect(json.Unmarshal([]byte(lola("cache", "list", "--json").out), &listed)).To(Succeed())
		var shown map[string]any
		Expect(json.Unmarshal([]byte(lola("info", "kit", "--json").out), &shown)).To(Succeed())
		for _, doc := range []map[string]any{added, listed.Modules[0]} {
			Expect(slices.Sorted(maps.Keys(doc))).To(ConsistOf(keys))
			for _, k := range keys {
				Expect(shown[k]).To(Equal(doc[k]), k)
			}
		}
	})

	Describe("without a scope", func() {
		var dir string
		BeforeEach(func() {
			dir = filepath.Join(tmp, "work", "x")
			Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
			GinkgoT().Chdir(dir)
		})

		It("info with no scope shows everything but the plan, says no scope was found and hints --project and --user, keeping the run's flags", func() {
			r := lola("info", kit, "-t", "claude-code")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("  pre-install hook setup.sh"))
			Expect(r.out).To(ContainSubstring("  runtime hooks as declared (rendered once a scope and an assistant are known), run by claude-code"))
			Expect(r.out).To(ContainSubstring("  MCP server tools, as declared, run by claude-code, runs: tools-mcp --stdio\n"))
			Expect(r.out).To(HaveSuffix("  consent: hooks kit pre-install: not recorded\n  consent: runtime-hooks kit: unknown without a scope\n" +
				"  needs --trust=hooks,runtime-hooks\n" +
				"no scope: no .lola/ was found in " + dir + " or above it, and config.yaml sets no default_scope\n" +
				"  hint: lola info " + kit + " --target claude-code --project\n" +
				"  hint: lola info " + kit + " --target claude-code --user\n"))

			r = lola("info", kit, "-t", "claude-code", "--json")
			Expect(r.code).To(Equal(0), r.err)
			doc := readInfo(r.out)
			Expect(doc.NoScope).NotTo(BeEmpty())
			Expect(doc.Install[0].Command).To(BeEmpty())
			Expect(doc.Hints).To(Equal([]string{"lola info " + kit + " --json --target claude-code --project",
				"lola info " + kit + " --json --target claude-code --user"}))
			for _, hint := range doc.Hints {
				r := runHint(hint)
				Expect(r.code).To(Equal(0), r.err)
				hinted := readInfo(r.out)
				Expect(hinted.Install[0].Scope).NotTo(BeEmpty())
				Expect(hinted.NoScope).To(BeEmpty())
				Expect(hinted.Install[0].Steps).NotTo(BeEmpty())
				Expect(hinted.Install[0].NewProject).To(Equal(strings.HasSuffix(hint, "--project")))
			}
			r = runHint(strings.TrimSuffix(doc.Hints[0], " --json --target claude-code --project") + " --target claude-code --project")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("  → new project at " + dir + "\n"))
			entries, err := os.ReadDir(dir)
			Expect(err).NotTo(HaveOccurred())
			Expect(entries).To(BeEmpty(), "nothing was written")
		})

		It("info in the home directory with no scope hints --user only", func() {
			home := os.Getenv("HOME")
			GinkgoT().Chdir(home)
			r := lola("info", kit, "--json")
			Expect(r.code).To(Equal(0), r.err)
			Expect(readInfo(r.out).Hints).To(Equal([]string{"lola info " + kit + " --json --user"}))
		})

		It("info never asks for a scope on a terminal", func() {
			root := cli.NewRootCmd(os.Getenv, cli.WithPrompter(&scripted{}), cli.WithTerminals(true, true))
			var out, errb bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&errb)
			root.SetIn(strings.NewReader(""))
			root.SetArgs([]string{"info", kit})
			Expect(cli.Execute(root)).To(Equal(0), errb.String())
			Expect(out.String()).To(ContainSubstring("no scope: no .lola/ was found in " + dir))
		})

		It("info with a scope but no assistant set up hints one -t run per installed assistant, and none when no assistant is installed", func() {
			r := lola("info", kit, "--project")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(HaveSuffix("no assistant found on your PATH, so lola does not know where to install\n"))
			Expect(r.out).To(ContainSubstring("  consent: runtime-hooks kit: unknown until lola can plan here\n"), "a scope resolved")

			fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
			fakeAssistant(tmp, "opencode", "1.0.0")
			r = lola("info", kit, "--project", "--json")
			Expect(r.code).To(Equal(0), r.err)
			doc := readInfo(r.out)
			Expect(doc.NoAssistant).To(Equal("none of your installed assistants (claude-code, opencode) is set up here, so lola does not know where to install"))
			Expect(doc.Hints).To(Equal([]string{"lola info " + kit + " --json --project -t claude-code", "lola info " + kit + " --json --project -t opencode"}))
			for i, id := range []string{"claude-code", "opencode"} {
				r := runHint(doc.Hints[i])
				Expect(r.code).To(Equal(0), r.err)
				Expect(readInfo(r.out).Install[0].Targets).To(Equal([]string{id}))
			}
		})

		It("info with default_scope both returns one install element per scope, and runtime-hooks consent is unknown without a scope", func() {
			config := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "lola", "config.yaml")
			writeTree(filepath.Dir(config), map[string]string{"config.yaml": "version: 1\ndefault_scope: both\n"})
			r := lola("info", kit, "-t", "claude-code", "--json")
			Expect(r.code).To(Equal(0), r.err)
			doc := readInfo(r.out)
			Expect(doc.Install).To(HaveLen(2))
			Expect([]string{doc.Install[0].Scope, doc.Install[1].Scope}).To(Equal([]string{"project", "user"}))
			Expect(r.out).NotTo(ContainSubstring(`"recorded": "unknown"`))
			Expect(os.Remove(config)).To(Succeed())
			r = lola("info", kit, "-t", "claude-code", "--json")
			Expect(r.out).To(ContainSubstring(`"recorded": "unknown"`))
		})

		It("info whose project plan is refused beside a user plan shows the declared runtime hooks under their own heading", func() {
			config := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "lola", "config.yaml")
			writeTree(filepath.Dir(config), map[string]string{"config.yaml": "version: 1\ndefault_scope: both\n"})
			writeTree(dir, map[string]string{".claude/commands/ship.md": "mine\n"})
			r := lola("info", kit, "-t", "claude-code")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("  runtime hooks, run by claude-code on its events, installed with your consent:\n    Stop: command\n"))
			Expect(r.out).To(ContainSubstring("  runtime hooks as declared (rendered once a scope and an assistant are known), run by claude-code " +
				"on its events, installed with your consent:\n    Stop: command\n      runs: ${CLAUDE_PLUGIN_ROOT}/stop.sh\n"))
		})
	})
})

// writeZip writes a zip archive at path holding files.
func writeZip(path string, files map[string]string) {
	GinkgoHelper()
	f, err := os.Create(path)
	Expect(err).NotTo(HaveOccurred())
	zw := zip.NewWriter(f)
	for _, name := range slices.Sorted(maps.Keys(files)) {
		w, err := zw.Create(name)
		Expect(err).NotTo(HaveOccurred())
		_, err = w.Write([]byte(files[name]))
		Expect(err).NotTo(HaveOccurred())
	}
	Expect(zw.Close()).To(Succeed())
	Expect(f.Close()).To(Succeed())
}
