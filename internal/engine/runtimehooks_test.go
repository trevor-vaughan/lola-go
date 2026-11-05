package engine_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	"github.com/onsi/gomega/types"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// rogue is a target that plans like claude-code but drops the consent marks from what it plans
// and, when kind is set, renames its runtime hook intents: the engine must refuse it either way.
type rogue struct {
	target.Target
	kind string
}

func (r rogue) Plan(m *module.Module, ctx target.PlanContext) ([]plan.Intent, []target.Finding, error) {
	ins, fs, err := r.Target.Plan(m, ctx)
	for i := range ins {
		ins[i].Consent = nil
		if r.kind != "" && ins[i].Kind == target.KindRuntimeHook {
			ins[i].Kind = r.kind
		}
	}
	return ins, fs, err
}

var _ = Describe("runtime hooks", func() {
	var (
		tmp, proj, plugin string
		e                 *engine.Engine
		sc                engine.Scope
		ctx               = context.Background()
		claudeOnly        = engine.Options{Targets: []string{"claude-code"}}
	)
	const settings = ".claude/settings.local.json"
	git := func(args ...string) {
		out, err := exec.Command("git", append([]string{"-C", proj}, args...)...).CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
	}
	install := func(opts engine.Options) (*engine.Result, error) {
		return e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: plugin}}, opts)
	}
	check := func() []target.Finding {
		rep, err := e.Check(ctx, sc, proj, []string{plugin}, []string{"claude-code"}, nil)
		Expect(err).NotTo(HaveOccurred())
		return rep.Modules[0].Findings
	}

	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj, plugin = filepath.Join(tmp, "proj"), filepath.Join(tmp, "src", "deploy")
		writeTree(plugin, map[string]string{
			".claude-plugin/plugin.json": `{"name":"deploy"}`,
			"commands/ship.md":           "Ship\n",
			"hooks/hooks.json":           `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/stop.sh"}]}]}}`,
		})
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
	})

	It("never reach settings.local.json without consent, while the rest of the module installs", func() {
		for _, opts := range []engine.Options{claudeOnly, {Targets: []string{"claude-code"}, Force: true, Trust: engine.TrustHooks}} {
			res, err := install(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Join(proj, ".claude/commands/ship.md")).To(BeARegularFile())
			Expect(filepath.Join(proj, settings)).NotTo(BeAnExistingFile())
			Expect(res.Findings).To(ContainElement(MatchFields(IgnoreExtras, Fields{"Severity": Equal(target.SeverityWarn), "Module": Equal("deploy"),
				"Target": Equal("claude-code"), "Kind": Equal("runtime-hook"), "Code": Equal("claude-code/runtime-hooks-declined")})))
			res, err = e.Render(ctx, sc, opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Join(proj, settings)).NotTo(BeAnExistingFile())
			Expect(res.Findings).To(ContainElement(HaveField("Code", "claude-code/runtime-hooks-declined")))
		}
	})

	Describe("consent", func() {
		var asked [][]engine.RuntimeHook
		answer := func(yes bool) func(string, []engine.RuntimeHook) (bool, error) {
			return func(module string, hooks []engine.RuntimeHook) (bool, error) {
				Expect(module).To(Equal("deploy"))
				asked = append(asked, hooks)
				return yes, nil
			}
		}
		never := func(string, []engine.RuntimeHook) (bool, error) {
			Fail("asked again for consent that was recorded")
			return false, nil
		}
		opts := func(ask func(string, []engine.RuntimeHook) (bool, error)) engine.Options {
			return engine.Options{Targets: []string{"claude-code"}, AskRuntimeHooks: ask}
		}
		consentFile := func() string {
			b, err := os.ReadFile(filepath.Join(tmp, "state", "lola", "consent.json"))
			if errors.Is(err, fs.ErrNotExist) {
				return ""
			}
			Expect(err).NotTo(HaveOccurred())
			return string(b)
		}
		hooksIn := func(rel string) map[string][]map[string]any {
			b, err := os.ReadFile(filepath.Join(proj, rel))
			Expect(err).NotTo(HaveOccurred())
			v, found, err := fsx.JSONGet(b, "/hooks")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			var hooks map[string][]map[string]any
			Expect(json.Unmarshal(v, &hooks)).To(Succeed())
			return hooks
		}
		declined := MatchFields(IgnoreExtras, Fields{"Severity": Equal(target.SeverityWarn), "Module": Equal("deploy"), "Target": Equal("claude-code"),
			"Kind": Equal("runtime-hook"), "Code": Equal("claude-code/runtime-hooks-declined"), "Hint": ContainSubstring("--trust=runtime-hooks")})
		BeforeEach(func() { asked = nil })

		It("asks, records a yes, and installs the groups; the same hooks never ask again", func() {
			res, err := install(opts(answer(true)))
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveLen(1))
			Expect(asked[0]).To(ConsistOf(MatchFields(IgnoreExtras, Fields{"Target": Equal("claude-code"), "Event": Equal("Stop"), "Type": Equal("command"),
				"Command": Equal("export CLAUDE_PLUGIN_ROOT='" + filepath.Join(proj, ".lola/modules/deploy") + "' CLAUDE_PLUGIN_DATA='" +
					filepath.Join(proj, ".lola/plugin-data/deploy") + "'; " + filepath.Join(proj, ".lola/modules/deploy") + "/stop.sh")})))
			Expect(hooksIn(settings)["Stop"]).To(HaveLen(1))
			Expect(filepath.Join(proj, ".lola/plugin-data/deploy")).To(BeADirectory(), "every command hook exports CLAUDE_PLUGIN_DATA")
			Expect(res.Modules[0].RuntimeHooks).To(ConsistOf(HaveField("Status", engine.RuntimeHooksConsented)))
			Expect(res.Findings).NotTo(ContainElement(HaveField("Code", "claude-code/runtime-hooks-declined")))
			Expect(consentFile()).To(ContainSubstring(`"deploy/runtime-hooks/`))

			_, err = install(opts(never))
			Expect(err).NotTo(HaveOccurred())
			res, err = e.Render(ctx, sc, opts(never))
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Steps).To(BeEmpty())
			Expect(res.Modules[0].RuntimeHooks).To(ConsistOf(HaveField("Status", engine.RuntimeHooksConsented)))
		})

		It("does not record a no, and skips the hooks with a coached warning while the rest installs", func() {
			res, err := install(opts(answer(false)))
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Join(proj, settings)).NotTo(BeAnExistingFile())
			Expect(filepath.Join(proj, ".claude/commands/ship.md")).To(BeARegularFile())
			Expect(res.Findings).To(ContainElement(declined))
			Expect(res.Modules[0].RuntimeHooks).To(ConsistOf(HaveField("Status", engine.RuntimeHooksDeclined)))
			Expect(consentFile()).NotTo(ContainSubstring("runtime-hooks"))
			_, err = install(opts(answer(false)))
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveLen(2), "a no asks again next time")
		})

		It("records consent with --trust=runtime-hooks, which --trust=hooks never stands in for", func() {
			res, err := install(engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustHooks})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Findings).To(ContainElement(declined))
			Expect(filepath.Join(proj, settings)).NotTo(BeAnExistingFile())

			_, err = install(engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustRuntimeHooks})
			Expect(err).NotTo(HaveOccurred())
			Expect(hooksIn(settings)["Stop"]).To(HaveLen(1))
			Expect(consentFile()).To(ContainSubstring(`"deploy/runtime-hooks/`))
			res, err = e.Render(ctx, sc, claudeOnly)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Findings).NotTo(ContainElement(HaveField("Code", "claude-code/runtime-hooks-declined")), "the recorded consent holds off a terminal")
		})

		It("makes the real run's decision on --dry-run, listing the hooks, asking nothing and recording nothing", func() {
			dry := engine.Options{Targets: []string{"claude-code"}, DryRun: true, AskRuntimeHooks: never}
			res, err := install(dry)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Modules[0].RuntimeHooks).To(ConsistOf(MatchFields(IgnoreExtras, Fields{"Event": Equal("Stop"), "Status": Equal(engine.RuntimeHooksDeclined)})))
			Expect(res.Findings).To(ContainElement(declined))
			Expect(res.Steps).NotTo(ContainElement(HaveField("Kind", "runtime-hook")))

			dry.Trust = engine.TrustRuntimeHooks
			res, err = install(dry)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Modules[0].RuntimeHooks).To(ConsistOf(HaveField("Status", engine.RuntimeHooksConsented)))
			Expect(res.Steps).To(ContainElement(MatchFields(IgnoreExtras, Fields{"Kind": Equal("runtime-hook"), "Action": Equal(plan.ActionCreate)})))
			Expect(res.Findings).NotTo(ContainElement(HaveField("Code", "claude-code/runtime-hooks-declined")))
			Expect(filepath.Join(proj, settings)).NotTo(BeAnExistingFile())
			Expect(consentFile()).To(BeEmpty())

			_, err = install(engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustRuntimeHooks})
			Expect(err).NotTo(HaveOccurred())
			dry.Trust = 0
			res, err = install(dry)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Modules[0].RuntimeHooks).To(ConsistOf(HaveField("Status", engine.RuntimeHooksConsented)), "a recorded consent holds")
		})

		It("fails closed when asking for consent fails: the run errors, writing and recording nothing", func() {
			boom := errors.New("input closed")
			_, err := install(opts(func(string, []engine.RuntimeHook) (bool, error) { return true, boom }))
			Expect(err).To(MatchError(boom))
			Expect(filepath.Join(proj, settings)).NotTo(BeAnExistingFile())
			Expect(filepath.Join(proj, ".claude/commands/ship.md")).NotTo(BeAnExistingFile())
			Expect(consentFile()).To(BeEmpty())
		})

		It("refuses with --strict when the hooks are declined, writing nothing", func() {
			_, err := install(engine.Options{Targets: []string{"claude-code"}, Strict: true})
			Expect(err).To(MatchError(HavePrefix("--strict: 1 finding(s); nothing was changed\n  deploy: claude-code: runtime hooks skipped")))
			Expect(filepath.Join(proj, ".claude")).NotTo(BeAnExistingFile())
		})

		It("asks again for a new version, and a declined update removes the groups installed before", func() {
			_, err := install(opts(answer(true)))
			Expect(err).NotTo(HaveOccurred())
			writeTree(plugin, map[string]string{"hooks/hooks.json": `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/stop.sh --v2"}]}]}}`})
			res, err := install(opts(answer(false)))
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveLen(2))
			Expect(res.Findings).To(ContainElement(declined))
			Expect(res.Steps).To(ContainElement(MatchFields(IgnoreExtras, Fields{"Kind": Equal("runtime-hook"), "Action": Equal(plan.ActionDelete)})))
			Expect(filepath.Join(proj, settings)).NotTo(BeAnExistingFile(), "lola created the file, and nothing of the user's is in it")
		})

		It("on a declined update, forgets a group the user edited with a hint that it still runs files from the module copy", func() {
			_, err := install(opts(answer(true)))
			Expect(err).NotTo(HaveOccurred())
			b, err := os.ReadFile(filepath.Join(proj, settings))
			Expect(err).NotTo(HaveOccurred())
			edited := strings.Replace(string(b), "/stop.sh", "/stop.sh --mine", 1)
			writeTree(proj, map[string]string{settings: edited})
			writeTree(plugin, map[string]string{"hooks/hooks.json": `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/stop.sh --v2"}]}]}}`})
			res, err := install(opts(answer(false)))
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Findings).To(ContainElement(MatchFields(IgnoreExtras, Fields{"Code": Equal("claude-code/runtime-hook-drift"),
				"Hint": ContainSubstring("module copy at " + filepath.Join(proj, ".lola/modules/deploy"))})))
			b, err = os.ReadFile(filepath.Join(proj, settings))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(Equal(edited), "the user's group is left alone")
		})

		It("asks again in another project, and removes the groups of a moved project off a terminal", func() {
			_, err := install(engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustRuntimeHooks})
			Expect(err).NotTo(HaveOccurred())
			other := filepath.Join(tmp, "other")
			Expect(os.MkdirAll(other, 0o755)).To(Succeed())
			_, err = e.Install(ctx, engine.Scope{Kind: target.Project, ProjectDir: other}, other, []engine.InstallRequest{{Arg: plugin}}, opts(answer(false)))
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveLen(1), "the rendered paths differ, so the digest does")

			moved := filepath.Join(tmp, "moved")
			Expect(os.Rename(proj, moved)).To(Succeed())
			// A moved project has no trust record, so render leaves its modules out; installing the
			// module again, as its finding coaches, renders it there.
			res, err := e.Render(ctx, engine.Scope{Kind: target.Project, ProjectDir: moved}, claudeOnly)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Findings).To(ContainElement(HaveField("Code", engine.CodeNotTrusted)))
			res, err = e.Install(ctx, engine.Scope{Kind: target.Project, ProjectDir: moved}, moved, []engine.InstallRequest{{Arg: plugin}}, claudeOnly)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Findings).To(ContainElement(declined))
			Expect(filepath.Join(moved, settings)).NotTo(BeAnExistingFile())
		})

		It("keys consent by module and settings file, so hooks without paths still ask again in another project", func() {
			writeTree(plugin, map[string]string{"hooks/hooks.json": `{"hooks":{"Stop":[{"hooks":[{"type":"prompt","prompt":"Done?"}]}]}}`})
			_, err := install(opts(answer(true)))
			Expect(err).NotTo(HaveOccurred())
			sum := sha256.Sum256([]byte(filepath.Join(proj, settings)))
			Expect(consentFile()).To(ContainSubstring(`"deploy/runtime-hooks/` + hex.EncodeToString(sum[:]) + `": "sha256:`))

			linked := filepath.Join(tmp, "linked")
			Expect(os.Symlink(proj, linked)).To(Succeed())
			_, err = e.Render(ctx, engine.Scope{Kind: target.Project, ProjectDir: linked}, opts(never))
			Expect(err).NotTo(HaveOccurred(), "the same settings file through a symlink is the same destination")

			other := filepath.Join(tmp, "other")
			Expect(os.MkdirAll(other, 0o755)).To(Succeed())
			_, err = e.Install(ctx, engine.Scope{Kind: target.Project, ProjectDir: other}, other, []engine.InstallRequest{{Arg: plugin}}, opts(answer(false)))
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveLen(2), "another project's settings file needs its own consent")

			_, err = e.Install(ctx, engine.Scope{Kind: target.User}, proj, []engine.InstallRequest{{Arg: plugin}}, opts(answer(false)))
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveLen(3), "user scope is another settings file")
			_, err = e.Render(ctx, sc, opts(never))
			Expect(err).NotTo(HaveOccurred(), "declining elsewhere leaves this project's consent alone")
		})

		Describe("in component frontmatter", func() {
			BeforeEach(func() {
				writeTree(plugin, map[string]string{
					"hooks/hooks.json":       `{"hooks":{"Stop":[{"hooks":[{"type":"prompt","prompt":"Done?"}]}]}}`,
					"skills/review/SKILL.md": "---\ndescription: Review\nhooks: {PreToolUse: [{matcher: Bash, hooks: [{type: command, command: ./guard.sh}]}]}\n---\nReview.\n",
					"commands/ops/deploy.md": "---\nHooks: {Stop: [{hooks: [{type: agent, prompt: verify}]}]}\n---\nDeploy.\n",
					"skills/plain/SKILL.md":  "---\ndescription: Plain\n---\nPlain.\n",
				})
			})
			skillFile := func() string { return filepath.Join(proj, ".claude/skills/review/SKILL.md") }

			It("asks for them with the settings hooks, under each component's name", func() {
				_, err := install(opts(answer(true)))
				Expect(err).NotTo(HaveOccurred())
				Expect(asked).To(HaveLen(1))
				Expect(asked[0]).To(ContainElements(
					MatchFields(IgnoreExtras, Fields{"Component": Equal(""), "Event": Equal("Stop"), "Type": Equal("prompt")}),
					MatchFields(IgnoreExtras, Fields{"Component": Equal("skill review"), "Event": Equal("PreToolUse"), "Matcher": Equal("Bash"),
						"Command": HaveSuffix("; ./guard.sh")}),
					MatchFields(IgnoreExtras, Fields{"Component": Equal("command deploy"), "Type": Equal("agent"), "Prompt": Equal("verify")})))
				Expect(skillFile()).To(BeARegularFile())
				Expect(filepath.Join(proj, ".claude/commands/deploy.md")).To(BeARegularFile())
			})

			It("skips each component with hooks when declined, and installs the rest", func() {
				res, err := install(opts(answer(false)))
				Expect(err).NotTo(HaveOccurred())
				Expect(skillFile()).NotTo(BeAnExistingFile())
				Expect(filepath.Join(proj, ".claude/commands/deploy.md")).NotTo(BeAnExistingFile())
				Expect(filepath.Join(proj, ".claude/skills/plain/SKILL.md")).To(BeARegularFile())
				Expect(filepath.Join(proj, ".claude/commands/ship.md")).To(BeARegularFile())
				Expect(res.Findings).To(ContainElements(
					MatchFields(IgnoreExtras, Fields{"Kind": Equal("skill"), "Artifact": Equal("review"), "Code": Equal("claude-code/runtime-hooks-declined"),
						"Hint": ContainSubstring("--trust=runtime-hooks")}),
					MatchFields(IgnoreExtras, Fields{"Kind": Equal("command"), "Artifact": Equal("deploy"), "Code": Equal("claude-code/runtime-hooks-declined")}),
					MatchFields(IgnoreExtras, Fields{"Kind": Equal("runtime-hook"), "Artifact": Equal(""), "Code": Equal("claude-code/runtime-hooks-declined")})))
			})

			It("asks again when only a component's hooks change, and a declined update removes the component", func() {
				_, err := install(engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustRuntimeHooks})
				Expect(err).NotTo(HaveOccurred())
				Expect(skillFile()).To(BeARegularFile())
				writeTree(plugin, map[string]string{
					"skills/review/SKILL.md": "---\ndescription: Review\nhooks: {PreToolUse: [{matcher: Bash, hooks: [{type: command, command: ./other.sh}]}]}\n---\nReview.\n"})
				res, err := install(opts(answer(false)))
				Expect(err).NotTo(HaveOccurred())
				Expect(asked).To(HaveLen(1), "a component identical but for its hooks changes the digest")
				Expect(skillFile()).NotTo(BeAnExistingFile(), "fail closed: the old component is removed")
				Expect(res.Steps).To(ContainElement(MatchFields(IgnoreExtras, Fields{"Kind": Equal("skill"), "Action": Equal(plan.ActionDelete)})))
			})
		})

		Describe("inline MCP servers in agent frontmatter", func() {
			const agentFile = ".claude/agents/browser.md"
			browser := func(servers string) map[string]string {
				return map[string]string{"agents/browser.md": "---\ndescription: Browser\nmcpServers:\n" + servers + "---\nBrowse.\n"}
			}
			playwright := "  - github\n  - pw: {command: npx, args: [-y, '@playwright/mcp@1']}\n"
			server := func(fields Fields) types.GomegaMatcher {
				all := Fields{"Target": Equal("claude-code"), "Component": Equal("agent browser"), "Event": Equal("")}
				maps.Copy(all, fields)
				return MatchFields(IgnoreExtras, all)
			}

			It("asks for an agent's inline server under the agent, skips the agent when declined, and installs it with --trust=runtime-hooks", func() {
				writeTree(plugin, browser(playwright))
				res, err := install(opts(answer(false)))
				Expect(err).NotTo(HaveOccurred())
				Expect(asked).To(HaveLen(1))
				Expect(asked[0]).To(ContainElement(server(Fields{"MCPServer": Equal("pw"), "Type": Equal("stdio"), "Command": Equal("npx -y @playwright/mcp@1")})))
				Expect(asked[0]).NotTo(ContainElement(HaveField("MCPServer", "github")), "a name of a configured server needs no consent")
				Expect(filepath.Join(proj, agentFile)).NotTo(BeAnExistingFile())
				Expect(filepath.Join(proj, ".claude/commands/ship.md")).To(BeARegularFile())
				Expect(res.Findings).To(ContainElement(MatchFields(IgnoreExtras, Fields{"Kind": Equal("agent"), "Artifact": Equal("browser"),
					"Code": Equal("claude-code/runtime-hooks-declined"), "Text": ContainSubstring("inline MCP servers"), "Hint": ContainSubstring("--trust=runtime-hooks")})))

				res, err = install(engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustRuntimeHooks})
				Expect(err).NotTo(HaveOccurred())
				Expect(filepath.Join(proj, agentFile)).To(BeARegularFile())
				Expect(res.Modules[0].RuntimeHooks).To(ContainElement(server(Fields{"MCPServer": Equal("pw"), "Status": Equal(engine.RuntimeHooksConsented)})))
				_, err = e.Render(ctx, sc, opts(never))
				Expect(err).NotTo(HaveOccurred())
			})

			It("installs an agent that only names configured servers without asking", func() {
				Expect(os.Remove(filepath.Join(plugin, "hooks/hooks.json"))).To(Succeed())
				writeTree(plugin, browser("  - github\n  - slack\n"))
				res, err := install(opts(never))
				Expect(err).NotTo(HaveOccurred())
				Expect(filepath.Join(proj, agentFile)).To(BeARegularFile())
				Expect(res.Modules[0].RuntimeHooks).To(BeEmpty())
				Expect(consentFile()).To(BeEmpty())
			})

			It("asks again when only a server's args change, and a declined update removes the agent", func() {
				writeTree(plugin, browser(playwright))
				_, err := install(engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustRuntimeHooks})
				Expect(err).NotTo(HaveOccurred())
				Expect(filepath.Join(proj, agentFile)).To(BeARegularFile())
				writeTree(plugin, browser("  - github\n  - pw: {command: npx, args: [-y, '@playwright/mcp@2']}\n"))
				res, err := install(opts(answer(false)))
				Expect(err).NotTo(HaveOccurred())
				Expect(asked).To(HaveLen(1), "an agent identical but for a server's args changes the digest")
				Expect(filepath.Join(proj, agentFile)).NotTo(BeAnExistingFile(), "fail closed: the old agent is removed")
				Expect(res.Steps).To(ContainElement(MatchFields(IgnoreExtras, Fields{"Kind": Equal("agent"), "Action": Equal(plan.ActionDelete)})))
			})

			It("shows each server's transport, command line or URL, env and headers with their values, and every other field as JSON, with control and bidi characters replaced", func() {
				writeTree(plugin, browser("  - pw: {command: \"npx\\e[2J\", args: [-y, \"a\\u202eb\"], env: {LD_PRELOAD: /x/evil.so, B: \"1\\e\"}, cwd: /tmp}\n"+
					"  - db: {type: sse, url: \"https://db.example/\\e\", headers: {Authorization: Bearer x}, oauth: {clientId: \"c\\u202e\"}}\n"))
				writeTree(plugin, map[string]string{"agents/second.md": "---\ndescription: S\nMcpServers:\n  - lint: {url: 'https://lint.example'}\n---\nS\n"})
				_, err := install(opts(answer(false)))
				Expect(err).NotTo(HaveOccurred())
				Expect(asked[0]).To(ContainElements(
					engine.RuntimeHook{Target: "claude-code", Component: "agent browser", MCPServer: "pw", Type: "stdio", Command: `"npx\x1b[2J" -y "a\u202eb"`,
						Env: []string{"B=1?", "LD_PRELOAD=/x/evil.so"}, Fields: `{"cwd":"/tmp"}`},
					engine.RuntimeHook{Target: "claude-code", Component: "agent browser", MCPServer: "db", Type: "sse", URL: "https://db.example/?",
						Headers: []string{"Authorization: Bearer x"}, Fields: `{"oauth":{"clientId":"c?"}}`},
					engine.RuntimeHook{Target: "claude-code", Component: "agent second", MCPServer: "lint", Type: "http", URL: "https://lint.example"}))
				Expect(filepath.Join(proj, ".claude/agents/second.md")).NotTo(BeAnExistingFile(), "a case variant of the key is gated too")
			})
		})

		It("shows every handler type and every field it does not describe for consent, with control characters replaced and prompts cut at 200 characters", func() {
			long := strings.Repeat("é", 199) + "xyz"
			writeTree(plugin, map[string]string{"hooks/hooks.json": `{"hooks":{"PreToolUse":[{"matcher":"Bash\u001b[2J|Edit","hooks":[` +
				`{"type":"http","url":"https://hooks.example/\u001b]0;x","headers":{"X-B":"2","Authorization":"Bearer $TOKEN"},"allowedEnvVars":["TOKEN"],"if":"Bash(git *)","timeout":30},` +
				`{"type":"mcp_tool","server":"db","tool":"scan","input":{"p":"${tool_input.file_path}"}},` +
				`{"type":"agent","prompt":"` + long + `"},{"type":"prompt","prompt":"short\nline","model":"m\u001b"},` +
				`{"type":"command","command":"node","args":["${CLAUDE_PLUGIN_ROOT}/a b.js","--x"],"async":true,"shell":"bash","asyncRewake":true,"statusMessage":"s"},` +
				`{"type":"future","weird":"\u0007"}]}]}}`})
			_, err := install(opts(answer(false)))
			Expect(err).NotTo(HaveOccurred())
			root := filepath.Join(proj, ".lola/modules/deploy")
			Expect(asked[0]).To(Equal([]engine.RuntimeHook{
				{Target: "claude-code", Event: "PreToolUse", Matcher: "Bash?[2J|Edit", Type: "http", If: "Bash(git *)", URL: "https://hooks.example/?]0;x",
					Headers: []string{"Authorization: Bearer $TOKEN", "X-B: 2"}, AllowedEnvVars: []string{"TOKEN"}, Fields: `{"timeout":30}`},
				{Target: "claude-code", Event: "PreToolUse", Matcher: "Bash?[2J|Edit", Type: "mcp_tool", Server: "db", Tool: "scan",
					Fields: `{"input":{"p":"${tool_input.file_path}"}}`},
				{Target: "claude-code", Event: "PreToolUse", Matcher: "Bash?[2J|Edit", Type: "agent", Prompt: strings.Repeat("é", 199) + "x (cut)"},
				{Target: "claude-code", Event: "PreToolUse", Matcher: "Bash?[2J|Edit", Type: "prompt", Prompt: "short\n  line", Fields: `{"model":"m\u001b"}`},
				{Target: "claude-code", Event: "PreToolUse", Matcher: "Bash?[2J|Edit", Type: "command", Async: true, Shell: "bash",
					Command: `env -- CLAUDE_PLUGIN_ROOT=` + root + ` CLAUDE_PLUGIN_DATA=` + filepath.Join(proj, ".lola/plugin-data/deploy") +
						` node "` + root + `/a b.js" --x`, Fields: `{"asyncRewake":true,"statusMessage":"s"}`},
				{Target: "claude-code", Event: "PreToolUse", Matcher: "Bash?[2J|Edit", Type: "future", Fields: `{"weird":"\u0007"}`},
			}))
		})

		It("shows a matcher group's fields it does not describe for every handler of the group, without refusing them", func() {
			writeTree(plugin, map[string]string{"hooks/hooks.json": `{"hooks":{"Stop":[{"matcher":"m","future":{"x":"\u001b1"},"hooks":[` +
				`{"type":"prompt","prompt":"p"},{"type":"mcp_tool","server":"s","tool":"t"}]}]}}`})
			_, err := install(opts(answer(false)))
			Expect(err).NotTo(HaveOccurred())
			Expect(asked[0]).To(Equal([]engine.RuntimeHook{
				{Target: "claude-code", Event: "Stop", Matcher: "m", Type: "prompt", Prompt: "p", GroupFields: `{"future":{"x":"\u001b1"}}`},
				{Target: "claude-code", Event: "Stop", Matcher: "m", Type: "mcp_tool", Server: "s", Tool: "t", GroupFields: `{"future":{"x":"\u001b1"}}`},
			}))
		})

		It("keeps the user's settings byte for byte, and uninstall removes only lola's groups and the arrays it created", func() {
			user := "{\n  // mine\n  \"model\": \"opus\",\n  \"hooks\": {\n    \"Stop\": [\n      {\"hooks\": [{\"type\": \"command\", \"command\": \"mine\"}]}\n    ]\n  }\n}\n"
			writeTree(plugin, map[string]string{"hooks/hooks.json": `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"}]}],` +
				`"SessionStart":[{"hooks":[{"type":"command","command":"y"}]}]}}`})
			writeTree(proj, map[string]string{settings: user})
			_, err := install(engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustRuntimeHooks})
			Expect(err).NotTo(HaveOccurred())
			got := hooksIn(settings)
			Expect(got["Stop"]).To(HaveLen(2))
			Expect(got["SessionStart"]).To(HaveLen(1))
			_, err = e.Uninstall(ctx, sc, []string{"deploy"}, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			b, err := os.ReadFile(filepath.Join(proj, settings))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(Equal(user))
		})

		It("writes and shows & < > as themselves, so a shell operator reads as one, and uninstall still removes the group", func() {
			writeTree(plugin, map[string]string{"hooks/hooks.json": `{"hooks":{"Stop":[{"note":"<b>","hooks":[` +
				`{"type":"command","command":"a && b > c","statusMessage":"x && y"}]}]}}`})
			_, err := install(opts(answer(true)))
			Expect(err).NotTo(HaveOccurred())
			Expect(asked[0]).To(ConsistOf(MatchFields(IgnoreExtras, Fields{"Fields": Equal(`{"statusMessage":"x && y"}`), "GroupFields": Equal(`{"note":"<b>"}`)})))
			b, err := os.ReadFile(filepath.Join(proj, settings))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(And(ContainSubstring(`a && b > c"`), ContainSubstring(`"x && y"`), ContainSubstring(`"<b>"`)))
			Expect(string(b)).NotTo(ContainSubstring(`\u00`))
			res, err := e.Render(ctx, sc, claudeOnly)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Steps).To(BeEmpty(), "the unescaped group is the owned group")
			_, err = e.Uninstall(ctx, sc, []string{"deploy"}, engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Join(proj, settings)).NotTo(BeAnExistingFile())
		})

		It("leaves a group the user removed or edited absent, reporting drift without failing --strict, until --force restores it", func() {
			_, err := install(engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustRuntimeHooks})
			Expect(err).NotTo(HaveOccurred())
			edited := "{\n  \"hooks\": {\n    \"Stop\": [{\"hooks\": [{\"type\": \"command\", \"command\": \"true\"}]}]\n  }\n}\n"
			writeTree(proj, map[string]string{settings: edited})
			res, err := e.Render(ctx, sc, claudeOnly)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Steps).To(ConsistOf(MatchFields(IgnoreExtras, Fields{"Action": Equal(plan.ActionAbsent), "Kind": Equal("runtime-hook")})))
			Expect(res.Findings).To(ContainElement(MatchFields(IgnoreExtras, Fields{"Severity": Equal(target.SeverityInfo), "Module": Equal("deploy"),
				"Code": Equal("claude-code/runtime-hook-drift"), "Hint": Equal("restore it: " + e.Command(sc, "render", "--force"))})))
			res, err = e.Render(ctx, sc, engine.Options{Targets: []string{"claude-code"}, ForceRun: "lola render --project --force", Display: []string{"--json"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Findings).To(ContainElement(HaveField("Hint", "restore it: lola render --project --force")), "the run's own --force run, when it has one")
			_, err = e.Render(ctx, sc, engine.Options{Targets: []string{"claude-code"}, Strict: true})
			Expect(err).NotTo(HaveOccurred(), "drift reports the user's choice; --strict does not escalate it")
			b, err := os.ReadFile(filepath.Join(proj, settings))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(Equal(edited))
			led, err := ledger.Load(filepath.Join(proj, ".lola/lock.json"))
			Expect(err).NotTo(HaveOccurred())
			Expect(led.Modules["deploy"].Artifacts).To(ContainElement(HaveField("Kind", "runtime-hook")), "still owned while absent")

			_, err = e.Render(ctx, sc, engine.Options{Targets: []string{"claude-code"}, Force: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(hooksIn(settings)["Stop"]).To(HaveLen(2))
		})

		It("refuses to take over an identical group the user already has, unless forced", func() {
			mine := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"` + "export CLAUDE_PLUGIN_ROOT='" + filepath.Join(proj, ".lola/modules/deploy") +
				"' CLAUDE_PLUGIN_DATA='" + filepath.Join(proj, ".lola/plugin-data/deploy") + "'; " + filepath.Join(proj, ".lola/modules/deploy") + `/stop.sh"}]}]}}`
			writeTree(proj, map[string]string{settings: mine})
			_, err := install(engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustRuntimeHooks})
			Expect(err).To(MatchError(ContainSubstring("exists and was not written by lola; owning it would remove it on uninstall")))
			_, err = install(engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustRuntimeHooks, Force: true})
			Expect(err).NotTo(HaveOccurred())
		})

		It("refuses the same group from two modules, even with --force", func() {
			same := `{"hooks":{"Stop":[{"hooks":[{"type":"prompt","prompt":"Is the task done?"}]}]}}`
			twin := filepath.Join(tmp, "src", "twin")
			writeTree(twin, map[string]string{".claude-plugin/plugin.json": `{"name":"twin"}`, "hooks/hooks.json": same})
			writeTree(plugin, map[string]string{"hooks/hooks.json": same})
			_, err := install(engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustRuntimeHooks})
			Expect(err).NotTo(HaveOccurred())
			_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: twin}}, engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustRuntimeHooks, Force: true})
			Expect(err).To(MatchError(ContainSubstring(`wanted by module "twin", owned by module "deploy"; --force cannot override it: uninstall deploy first`)))
			Expect(err).To(MatchError(ContainSubstring("--force cannot override it")))
			Expect(errs.Hints(err)).To(Equal([]string{"lola uninstall -C " + proj + " deploy"}))
		})

		It("writes ~/.claude/settings.json at user scope", func() {
			sc = engine.Scope{Kind: target.User}
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: plugin}}, engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustRuntimeHooks})
			Expect(err).NotTo(HaveOccurred())
			b, err := os.ReadFile(filepath.Join(tmp, "home", ".claude", "settings.json"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(ContainSubstring(`"Stop"`))
			Expect(filepath.Join(proj, settings)).NotTo(BeAnExistingFile())
		})
	})

	Describe("the git-ignore check at project scope", func() {
		notIgnored := func(text string) OmegaMatcher {
			return ContainElement(MatchFields(IgnoreExtras, Fields{"Severity": Equal(target.SeveritySkip), "Kind": Equal("runtime-hook"),
				"Code": Equal("claude-code/settings-local-not-ignored"), "Forceable": BeTrue(), "Text": ContainSubstring(text),
				"Hint": Equal("add .claude/settings.local.json to .gitignore; Claude Code ignores it only when Claude Code creates it")}))
		}

		It("is skipped outside a git work tree, and at user scope", func() {
			Expect(check()).NotTo(ContainElement(HaveField("Code", "claude-code/settings-local-not-ignored")))
			git("init", "-q")
			sc = engine.Scope{Kind: target.User}
			Expect(check()).NotTo(ContainElement(HaveField("Code", "claude-code/settings-local-not-ignored")))
		})

		It("skips the hooks when the file is not ignored, and passes them once it is", func() {
			git("init", "-q")
			Expect(check()).To(notIgnored(".claude/settings.local.json is not ignored by git"))
			writeTree(proj, map[string]string{".gitignore": ".claude/settings.local.json\n"})
			Expect(check()).NotTo(ContainElement(HaveField("Code", "claude-code/settings-local-not-ignored")))
		})

		It("skips the hooks when the file is tracked, even if ignored", func() {
			git("init", "-q")
			writeTree(proj, map[string]string{".gitignore": ".claude/settings.local.json\n", settings: "{}\n"})
			git("add", "-f", settings)
			Expect(check()).To(notIgnored("is tracked by git"))
		})

		It("checks a subdirectory's repository, and fails closed without git", func() {
			git("init", "-q")
			sub := filepath.Join(proj, "sub")
			Expect(os.MkdirAll(sub, 0o755)).To(Succeed())
			sc = engine.Scope{Kind: target.Project, ProjectDir: sub}
			Expect(check()).To(notIgnored("is not ignored by git"))
			GinkgoT().Setenv("PATH", filepath.Join(tmp, "bin"))
			Expect(check()).To(notIgnored("lola could not check that git ignores .claude/settings.local.json (git is not on PATH)"))
		})

		It("fails closed when git fails, and never runs the repository's configured programs", func() {
			git("init", "-q")
			writeTree(proj, map[string]string{"a": "a"})
			git("add", "a")
			pwned := filepath.Join(tmp, "pwned")
			git("config", "core.fsmonitor", "touch "+pwned) // plain git ls-files would run it
			git("config", "core.hooksPath", filepath.Join(tmp, "hooks"))
			Expect(check()).To(notIgnored("is not ignored by git"))
			Expect(pwned).NotTo(BeAnExistingFile())
			writeTree(proj, map[string]string{".git/index": "not an index"})
			Expect(check()).To(notIgnored("lola could not check that git ignores .claude/settings.local.json (git ls-files exited with status 128)"))
		})

		It("reports the skip on install too", func() {
			git("init", "-q")
			res, err := install(claudeOnly)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Findings).To(notIgnored("is not ignored by git"))
		})
	})

	DescribeTable("refuses a target that plans hooks into Claude Code's settings without a consent destination, whatever their kind",
		func(kind string) {
			for i, t := range e.Targets {
				if t.ID() == "claude-code" {
					e.Targets[i] = rogue{Target: t, kind: kind}
				}
			}
			_, err := install(engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustRuntimeHooks})
			Expect(err).To(MatchError(ContainSubstring("planned runtime hooks in .claude/settings.local.json without a consent destination")))
			Expect(filepath.Join(proj, settings)).NotTo(BeAnExistingFile())
		},
		Entry("the runtime hook kind", ""),
		Entry("another kind", "mcp"),
	)

	It("leaves out an installed module that no longer loads, such as one with a hooked sidecar from before the refusal, with its coaching", func() {
		native := filepath.Join(tmp, "src", "ship")
		writeTree(native, map[string]string{"commands/ship.md": "Ship\n"})
		_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: native}}, claudeOnly)
		Expect(err).NotTo(HaveOccurred())
		// The folder now holds what an older lola installed and this one refuses.
		writeTree(native, map[string]string{"commands/ship/notes.md": "---\nhooks: {Stop: [{hooks: [{type: prompt, prompt: p}]}]}\n---\nN\n"})
		res, err := e.Render(ctx, sc, claudeOnly)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Findings).To(ContainElement(And(HaveField("Module", "ship"), HaveField("Code", engine.CodeContentChanged),
			HaveField("Text", ContainSubstring("frontmatter hooks")), HaveField("Hint", ContainSubstring("put hooks in the command itself")))))
		_, err = e.Uninstall(ctx, sc, []string{"ship"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred(), "the hint's way out works")
		_, err = e.Render(ctx, sc, claudeOnly)
		Expect(err).NotTo(HaveOccurred())
	})

	It("forgets a hook group the user edited and leaves it alone on uninstall, reporting drift", func() {
		_, err := install(claudeOnly)
		Expect(err).NotTo(HaveOccurred())
		group := `{"hooks":[{"command":"x","type":"command"}]}`
		edited := "{\n  \"hooks\": {\n    \"Stop\": [{\"hooks\": [{\"type\": \"command\", \"command\": \"x --safe\"}]}]\n  }\n}\n"
		writeTree(proj, map[string]string{settings: edited})
		lockPath := filepath.Join(proj, ".lola/lock.json")
		led, err := ledger.Load(lockPath)
		Expect(err).NotTo(HaveOccurred())
		led.Modules["deploy"].Artifacts = append(led.Modules["deploy"].Artifacts, ledger.Artifact{Target: "claude-code", Kind: "runtime-hook",
			Path: settings, Key: "/hooks/Stop", Member: []byte(group), SHA256: "x"})
		Expect(led.Save(lockPath)).To(Succeed())

		res, err := e.Uninstall(ctx, sc, []string{"deploy"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Steps).To(ContainElement(MatchFields(IgnoreExtras, Fields{"Action": Equal(plan.ActionForget), "Kind": Equal("runtime-hook")})))
		Expect(res.Findings).To(ContainElement(MatchFields(IgnoreExtras, Fields{"Severity": Equal(target.SeverityInfo), "Module": Equal("deploy"),
			"Code": Equal("claude-code/runtime-hook-drift"), "Text": ContainSubstring("Stop hook group"),
			"Hint": And(ContainSubstring(filepath.Join(proj, ".lola/modules/deploy")), ContainSubstring("remove it from "+settings))})))
		b, err := os.ReadFile(filepath.Join(proj, settings))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal(edited))
	})

	It("reports drift without the control characters of an event name read back from the ledger", func() {
		_, err := install(claudeOnly)
		Expect(err).NotTo(HaveOccurred())
		lockPath := filepath.Join(proj, ".lola/lock.json")
		led, err := ledger.Load(lockPath)
		Expect(err).NotTo(HaveOccurred())
		led.Modules["deploy"].Artifacts = append(led.Modules["deploy"].Artifacts, ledger.Artifact{Target: "claude-code", Kind: "runtime-hook",
			Path: settings, Key: "/hooks/\x1b[2JStop", Member: []byte(`{"hooks":[{"command":"x","type":"command"}]}`), SHA256: "x"})
		Expect(led.Save(lockPath)).To(Succeed())

		res, err := e.Uninstall(ctx, sc, []string{"deploy"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Findings).To(ContainElement(MatchFields(IgnoreExtras, Fields{"Code": Equal("claude-code/runtime-hook-drift"),
			"Text": And(ContainSubstring("Stop hook group"), Not(ContainSubstring("\x1b")))})))
	})
})
