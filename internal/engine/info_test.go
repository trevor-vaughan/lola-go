package engine_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// infoModule declares an install hook that leaves a marker where it runs, a Claude Code runtime
// hook and an MCP server: everything lola info must show and never run.
var infoModule = map[string]string{
	"lola.yml":         "name: kit\nversion: 1.0.0\nhooks: {pre-install: setup.sh}\ncomponents:\n  runtime_hooks:\n    - {path: hooks/hooks.json}\n",
	"setup.sh":         "#!/bin/sh\ntouch \"$LOLA_PROJECT_PATH/hook-ran\"\n",
	"hooks/hooks.json": `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/stop.sh"}]}]}}`,
	"commands/ship.md": "Ship\n",
	"mcps.json":        `{"mcpServers":{"tools":{"command":"tools-mcp","args":["--stdio"]}}}`,
}

var _ = Describe("lola info", func() {
	var (
		tmp, proj, kit string
		e              *engine.Engine
		ctx            = context.Background()
	)
	// rerun is the info run of the spec, with extra flags appended: what a hint repeats.
	rerun := func(extra ...string) string { return strings.Join(append([]string{"lola info " + kit}, extra...), " ") }
	// installBy is what the last run's next step installs, as the engine gave it to Install.
	var installBy struct {
		arg, ref, path string
		kinds          engine.TrustKinds
	}
	request := func(scope engine.ScopeRequest, targets ...string) engine.InfoRequest {
		scope.Cwd = proj
		return engine.InfoRequest{Cwd: proj, Arg: kit, Scope: scope, Targets: targets, Rerun: rerun, UserRun: rerun("--user"),
			Install: func(arg, ref, path string, k engine.TrustKinds) string {
				installBy.arg, installBy.ref, installBy.path, installBy.kinds = arg, ref, path, k
				return strings.TrimSpace("lola install " + arg + " " + k.Arg())
			}}
	}
	claudeHere := func() engine.InfoRequest { return request(engine.ScopeRequest{Project: true}, "claude-code") }
	// serveKit serves infoModule as a git repository with one commit, sets kit to its URL and
	// returns the commit.
	serveKit := func() string {
		work := filepath.Join(tmp, "work")
		writeTree(work, infoModule)
		Expect(os.Chmod(filepath.Join(work, "setup.sh"), 0o755)).To(Succeed())
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "v1")
		gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "kit.git"))
		kit = testenv.ServeGit(filepath.Join(tmp, "srv")) + "/kit.git"
		return gitIn(work, "rev-parse", "HEAD")
	}
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj, kit = filepath.Join(tmp, "proj"), filepath.Join(tmp, "src", "kit")
		writeTree(kit, infoModule)
		Expect(os.Chmod(filepath.Join(kit, "setup.sh"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
	})

	It("info of a source caches it; a later install of the cached name fetches nothing, and a later install of the source fetches again", func() {
		work := filepath.Join(tmp, "work")
		writeTree(work, exampleModule)
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "init")
		gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "example.git"))
		var requests atomic.Int64
		url := testenv.ServeGitWith(filepath.Join(tmp, "srv"), func(*http.Request) { requests.Add(1) }) + "/example.git"
		req := claudeHere()
		req.Arg = url
		info, err := e.Info(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Name).To(Equal("example"))
		Expect(requests.Load()).To(BeNumerically(">", 0), "info fetches the source")
		names, err := e.Store.Names()
		Expect(err).NotTo(HaveOccurred())
		Expect(names).To(Equal([]string{"example"}))

		sc := engine.Scope{Kind: target.Project, ProjectDir: proj}
		before := requests.Load()
		_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "example"}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(requests.Load()).To(Equal(before), "the cached name installs from the cache")
		_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: url}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(requests.Load()).To(BeNumerically(">", before), "the source is fetched again")
	})

	It("info runs no install hook, runtime hook, MCP server or script", func() {
		info, err := e.Info(ctx, claudeHere())
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Runs.InstallHooks).To(HaveLen(1))
		Expect(info.Runs.RuntimeHooks).NotTo(BeEmpty())
		Expect(info.Runs.MCPServers).NotTo(BeEmpty())
		Expect(info.Install[0].Steps).NotTo(BeEmpty())
		entries, err := os.ReadDir(proj)
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(BeEmpty(), "no marker, no settings file, no .lola/: nothing was written to the project")
		Expect(e.Paths.Consent).NotTo(BeAnExistingFile(), "no consent was recorded")
	})

	It("info shows each hook's sha256 and first lines cleaned, and runtime hooks and MCP servers as the assistant would run them", func() {
		script := "#!/bin/sh\necho \x1b[2Jcleared\n"
		writeTree(kit, map[string]string{"setup.sh": script})
		info, err := e.Info(ctx, claudeHere())
		Expect(err).NotTo(HaveOccurred())
		sum := sha256.Sum256([]byte(script))
		Expect(info.Runs.InstallHooks).To(Equal([]engine.InfoHook{{Event: "pre-install", Script: "setup.sh", SHA256: hex.EncodeToString(sum[:]),
			FirstLines: []string{"#!/bin/sh", "echo ?[2Jcleared"}}}))
		Expect(info.Runs.RuntimeHooks).To(ConsistOf(HaveField("Command", HaveSuffix("; "+filepath.Join(proj, ".lola", "modules", "kit")+"/stop.sh"))))
		Expect(info.Runs.RuntimeHooks[0].Target).To(Equal("claude-code"))
		Expect(info.Runs.MCPServers).To(Equal([]engine.InfoMCPServer{{Name: "tools", Target: "claude-code", Command: "tools-mcp",
			Args: []string{"--stdio"}, URL: ""}}))

		writeTree(kit, map[string]string{"mcps.json": `{"mcpServers":{"tools":{"command":"${PLUGIN_ROOT}/bin/t\nx","args":["--data","${PLUGIN_DATA}"]}}}`})
		info, err = e.Info(ctx, claudeHere())
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Runs.MCPServers).To(HaveLen(1))
		srv := info.Runs.MCPServers[0]
		Expect(srv.Command).To(Equal(filepath.Join(proj, ".lola", "modules", "kit")+"/bin/t?x"), "expanded as the assistant runs it, on one line")
		Expect(srv.Args).To(Equal([]string{"--data", filepath.Join(proj, ".lola", "plugin-data", "kit")}))
	})

	It("info lists the runtime hooks a module declares, as declared, when it has no plan, and needs their consent", func() {
		writeTree(kit, map[string]string{"agents/critic.md": "---\ndescription: critic\nhooks:\n  Stop:\n    - hooks:\n        - {type: command, command: ./judge.sh}\n---\nJudge.\n"})
		writeTree(kit, map[string]string{"lola.yml": "name: kit\nversion: 1.0.0\nhooks: {pre-install: setup.sh}\n"}) // no components.runtime_hooks
		proj = filepath.Join(tmp, "work", "x")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		info, err := e.Info(ctx, request(engine.ScopeRequest{}, "claude-code"))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.NoScope).NotTo(BeEmpty())
		Expect(info.Runs.RuntimeHooks).To(ContainElement(And(HaveField("Component", "agent critic"), HaveField("Command", "./judge.sh"),
			HaveField("Status", engine.RecordedUnknown))))
		Expect(info.Install[0].Consent).To(ContainElement(engine.InfoConsent{Kind: "runtime-hooks", Item: "kit", Recorded: engine.RecordedUnknown}))
		Expect(info.Install[0].TrustNeeded).To(ContainElement("runtime-hooks"))
	})

	It("info's error about a component's frontmatter shows the author's key cleaned", func() {
		writeTree(kit, map[string]string{"agents/critic.md": "---\ndescription: critic\n\"bad\\e[2Jkey\": .nan\n---\nJudge.\n"})
		_, err := e.Info(ctx, claudeHere())
		Expect(err).To(MatchError(HavePrefix("agent critic: frontmatter bad?[2Jkey: ")))
	})

	It("info shows at most 5 lines of a hook, each cut at 200 characters", func() {
		long := strings.Repeat("x", 250)
		writeTree(kit, map[string]string{"setup.sh": "#!/bin/sh\n" + long + "\n3\n4\n5\n6\n7\n"})
		info, err := e.Info(ctx, claudeHere())
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Runs.InstallHooks[0].FirstLines).To(Equal([]string{"#!/bin/sh", strings.Repeat("x", 200) + "…", "3", "4", "5"}))

		writeTree(kit, map[string]string{"setup.sh": "#!/bin/sh\n# " + strings.Repeat("é", 250) + "\n"})
		info, err = e.Info(ctx, claudeHere())
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Runs.InstallHooks[0].FirstLines[1]).To(Equal("# "+strings.Repeat("é", 198)+"…"), "characters, not bytes")
	})

	It("info reads at most 1 MiB of a hook for its first lines", func() {
		writeTree(kit, map[string]string{"setup.sh": "#!/bin/sh\n# " + strings.Repeat("x", 1<<20) + "\necho after\n"})
		info, err := e.Info(ctx, claudeHere())
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Runs.InstallHooks[0].FirstLines).To(Equal([]string{"#!/bin/sh", "# " + strings.Repeat("x", 198) + "…"}))
	})

	It("info's install section equals install -n for the same scope and targets", func() {
		info, err := e.Info(ctx, claudeHere())
		Expect(err).NotTo(HaveOccurred())
		sc := engine.Scope{Kind: target.Project, ProjectDir: proj}
		dry, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "kit"}}, engine.Options{Targets: []string{"claude-code"}, DryRun: true,
			Rerun: func(k engine.TrustKinds) string { return "lola install kit " + (k | engine.TrustHooks).Arg() }}) // the install info names
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Install).To(HaveLen(1))
		Expect(info.Install[0].Scope).To(Equal("project"))
		Expect(info.Install[0].Targets).To(Equal([]string{"claude-code"}))
		Expect(info.Install[0].Steps).To(Equal(dry.Steps))
		Expect(info.Install[0].Findings).To(Equal(dry.Findings))
		Expect(info.Install[0].Digest).To(Equal(info.Digest))
	})

	It("info names exactly the trust kinds an install needs and whether each consent is recorded", func() {
		info, err := e.Info(ctx, claudeHere())
		Expect(err).NotTo(HaveOccurred())
		settings := ".claude/settings.local.json"
		Expect(info.Install[0].Consent).To(Equal([]engine.InfoConsent{{Kind: "hooks", Item: "kit pre-install", Recorded: engine.RecordedNo},
			{Kind: "runtime-hooks", Item: "kit in " + settings, Recorded: engine.RecordedNo}}))
		Expect(info.Install[0].TrustNeeded).To(Equal([]string{"hooks", "runtime-hooks"}))
		Expect(info.Install[0].Findings).To(ContainElement(HaveField("Hint",
			"review the hooks listed above, then consent to them: lola install kit --trust=hooks,runtime-hooks")), "the install info names, not --trust alone")

		sc := engine.Scope{Kind: target.Project, ProjectDir: proj}
		_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "kit"}}, engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustHooks})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.Remove(filepath.Join(proj, "hook-ran"))).To(Succeed())
		info, err = e.Info(ctx, claudeHere())
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Install[0].Consent).To(Equal([]engine.InfoConsent{{Kind: "hooks", Item: "kit pre-install", Recorded: engine.RecordedYes},
			{Kind: "runtime-hooks", Item: "kit in " + settings, Recorded: engine.RecordedNo}}))
		Expect(info.Install[0].TrustNeeded).To(Equal([]string{"runtime-hooks"}))
		Expect(filepath.Join(proj, "hook-ran")).NotTo(BeAnExistingFile())

		// Without the consent, installing the version installed keeps its copy and runs no hook: it needs no hooks trust.
		Expect(os.Remove(e.Paths.Consent)).To(Succeed())
		info, err = e.Info(ctx, claudeHere())
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Install[0].Consent).To(Equal([]engine.InfoConsent{{Kind: "runtime-hooks", Item: "kit in " + settings, Recorded: engine.RecordedNo}}))
		Expect(info.Install[0].TrustNeeded).To(Equal([]string{"runtime-hooks"}))
	})

	It("the off-terminal choice of assistants is the manifest's, else those set up in the scope, else none, naming those installed", func() {
		sc := engine.Scope{Kind: target.Project, ProjectDir: proj}
		choice, err := e.DefaultTargets(sc, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(choice).To(Equal(engine.TargetChoice{}))
		Expect(choice.NoneHere()).To(Equal("no assistant found on your PATH, so lola does not know where to install"))
		Expect(os.WriteFile(filepath.Join(tmp, "bin", "claude"), []byte("#!/bin/sh\necho 1.0.0\n"), 0o755)).To(Succeed())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		choice, err = e.DefaultTargets(sc, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(choice).To(Equal(engine.TargetChoice{Installed: []string{"claude-code"}}))
		Expect(choice.NoneHere()).To(Equal("none of your installed assistants (claude-code) is set up here, so lola does not know where to install"))
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		choice, err = e.DefaultTargets(sc, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(choice).To(Equal(engine.TargetChoice{Installed: []string{"claude-code"}, SetUp: []string{"claude-code"}}))
		Expect(choice.NoneHere()).To(BeEmpty())
		writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntargets: [opencode]\nmodules: []\n"})
		choice, err = e.DefaultTargets(sc, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(choice).To(Equal(engine.TargetChoice{Manifest: []string{"opencode"}, Installed: []string{"claude-code"}, SetUp: []string{"claude-code"}}))
		Expect(choice.NoneHere()).To(BeEmpty())
	})

	It("info echoes a full-SHA --ref typed in capitals lower-cased", func() {
		commit := serveKit()
		_, err := e.Fetch(ctx, source.Spec{Kind: source.KindGit, Location: kit, Ref: commit})
		Expect(err).NotTo(HaveOccurred())
		req := claudeHere()
		req.Arg, req.Ref = "kit", strings.ToUpper(commit)
		info, err := e.Info(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect([]string{installBy.arg, installBy.ref}).To(Equal([]string{"kit", commit}))
		Expect(info.Heads).To(ConsistOf(HaveField("Ref", commit)))
	})

	It("info reports a refusal of the install as the plan's, and returns any other error", func() {
		writeTree(proj, map[string]string{".claude/commands/ship.md": "mine\n"})
		info, err := e.Info(ctx, claudeHere())
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Install[0].Refusal).NotTo(BeNil())
		Expect(info.Install[0].Refusal.Error).To(ContainSubstring(".claude/commands/ship.md"))
		Expect(info.Install[0].Refusal.Hints).To(ContainElement(Equal("lola install kit --trust=hooks,runtime-hooks --force")))
		Expect(info.Install[0].Command).To(BeEmpty())

		Expect(os.RemoveAll(filepath.Join(proj, ".claude"))).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(proj, ".lola", "lock.json"), 0o755)).To(Succeed())
		_, err = e.Info(ctx, claudeHere())
		Expect(err).To(HaveOccurred(), "an unreadable lock.json is no refusal of the install")
	})

	It("info whose every plan is refused shows the module's declared runtime hooks and MCP servers, and the trust its --force run needs", func() {
		writeTree(kit, map[string]string{"lola.yml": "name: kit\nversion: 1.0.0\ncomponents:\n  runtime_hooks:\n    - {path: hooks/hooks.json}\n"})
		writeTree(proj, map[string]string{".claude/commands/ship.md": "mine\n"})
		info, err := e.Info(ctx, claudeHere())
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Install[0].Refusal).NotTo(BeNil())
		Expect(info.Install[0].Consent).To(Equal([]engine.InfoConsent{{Kind: "runtime-hooks", Item: "kit", Recorded: engine.RecordedUnknown}}))
		Expect(info.Runs.RuntimeHooks).To(ConsistOf(HaveField("Status", engine.RecordedUnknown)), "the hooks consent is asked for")
		Expect(info.Runs.MCPServers).To(ContainElement(HaveField("Name", "tools")))
		Expect(info.Install[0].Refusal.Hints).To(ContainElement("lola install kit --trust=runtime-hooks --force"))
	})

	var marketCommit string
	DescribeTable("info's hint installs the digest info showed",
		func(arg func() string, after func(), wantArg func() string, wantRef func() string, kinds engine.TrustKinds) {
			req := claudeHere()
			req.Arg = arg()
			info, err := e.Info(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(installBy.arg).To(Equal(wantArg()))
			Expect(installBy.ref).To(Equal(wantRef()))
			Expect(installBy.kinds).To(Equal(kinds))
			Expect(info.Install[0].Command).To(Equal("lola install " + wantArg() + " " + kinds.Arg()))
			after()
			sc := engine.Scope{Kind: target.Project, ProjectDir: proj}
			res, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: installBy.arg, Ref: installBy.ref, Path: installBy.path}},
				engine.Options{Targets: []string{"claude-code"}, Trust: installBy.kinds})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Modules[0].Digest).To(Equal(info.Digest))
		},
		Entry("a git branch moved after info: the hint's --ref <sha> installs the old commit", func() string {
			work := filepath.Join(tmp, "work")
			writeTree(work, infoModule)
			Expect(os.Chmod(filepath.Join(work, "setup.sh"), 0o755)).To(Succeed())
			gitIn(work, "init", "-q", "-b", "main")
			gitIn(work, "add", "-A")
			gitIn(work, "commit", "-q", "-m", "v1")
			gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "kit.git"))
			kit = testenv.ServeGit(filepath.Join(tmp, "srv")) + "/kit.git"
			return kit
		}, func() {
			work := filepath.Join(tmp, "work")
			writeTree(work, map[string]string{"commands/ship.md": "Ship v2\n"})
			gitIn(work, "commit", "-q", "-am", "v2")
			gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "kit.git"), "main")
		}, func() string { return kit }, func() string { return gitIn(filepath.Join(tmp, "work"), "rev-parse", "HEAD") }, engine.TrustHooks|engine.TrustRuntimeHooks),
		Entry("an archive: the cached name", func() string {
			archive := filepath.Join(tmp, "kit-1.0.tar.gz")
			// An archive written here holds no executable, so its module declares no install hook.
			Expect(os.WriteFile(archive, tarGz("kit", map[string]string{"lola.yml": "name: kit\ncomponents:\n  runtime_hooks:\n    - {path: hooks/hooks.json}\n",
				"hooks/hooks.json": infoModule["hooks/hooks.json"], "commands/ship.md": "Ship\n"}), 0o644)).To(Succeed())
			return archive
		}, func() {
			Expect(os.WriteFile(filepath.Join(tmp, "kit-1.0.tar.gz"), tarGz("kit", map[string]string{"lola.yml": "name: kit\n", "commands/x.md": "X\n"}),
				0o644)).To(Succeed())
		}, func() string { return "kit" }, func() string { return "" }, engine.TrustRuntimeHooks),
		Entry("a folder edited after info: the cached name installs what info showed", func() string { return kit }, func() {
			writeTree(kit, map[string]string{"commands/ship.md": "Ship v2\n"})
		}, func() string { return "kit" }, func() string { return "" }, engine.TrustHooks|engine.TrustRuntimeHooks),
		Entry("a cached git module whose scope's entry records another ref: its source at the commit shown", func() string {
			work := filepath.Join(tmp, "work")
			writeTree(work, infoModule)
			Expect(os.Chmod(filepath.Join(work, "setup.sh"), 0o755)).To(Succeed())
			gitIn(work, "init", "-q", "-b", "main")
			gitIn(work, "add", "-A")
			gitIn(work, "commit", "-q", "-m", "v1")
			gitIn(work, "tag", "v1")
			writeTree(work, map[string]string{"commands/ship.md": "Ship v2\n"})
			gitIn(work, "commit", "-q", "-am", "v2")
			gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "kit.git"))
			kit = testenv.ServeGit(filepath.Join(tmp, "srv")) + "/kit.git"
			sc := engine.Scope{Kind: target.Project, ProjectDir: proj}
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: kit, Ref: "v1"}}, engine.Options{Targets: []string{"claude-code"},
				Trust: engine.TrustHooks | engine.TrustRuntimeHooks})
			Expect(err).NotTo(HaveOccurred())
			_, err = e.Fetch(ctx, source.Spec{Kind: source.KindGit, Location: kit}) // the default branch, which info shows
			Expect(err).NotTo(HaveOccurred())
			return "kit"
		}, func() {}, func() string { return kit }, func() string { return gitIn(filepath.Join(tmp, "work"), "rev-parse", "HEAD") },
			engine.TrustRuntimeHooks),
		Entry("an @market entry: its git source at the commit the catalog pins", func() string {
			work := filepath.Join(tmp, "work")
			files := map[string]string{".claude-plugin/marketplace.json": `{"name": "acme", "owner": {"name": "Acme"}, "plugins": [{"name": "kit", "source": "./kit"}]}`}
			for k, v := range infoModule {
				files["kit/"+k] = v
			}
			writeTree(work, files)
			Expect(os.Chmod(filepath.Join(work, "kit", "setup.sh"), 0o755)).To(Succeed())
			gitIn(work, "init", "-q", "-b", "main")
			gitIn(work, "add", "-A")
			gitIn(work, "commit", "-q", "-m", "v1")
			gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "acme.git"))
			marketCommit = gitIn(work, "rev-parse", "HEAD")
			kit = testenv.ServeGit(filepath.Join(tmp, "srv")) + "/acme.git"
			_, err := e.AddMarket(ctx, engine.MarketAddRequest{Source: kit})
			Expect(err).NotTo(HaveOccurred())
			return "@acme/kit"
		}, func() {
			work := filepath.Join(tmp, "work")
			writeTree(work, map[string]string{"kit/commands/ship.md": "Ship v2\n"})
			gitIn(work, "commit", "-q", "-am", "v2")
			gitIn(work, "push", "-q", filepath.Join(tmp, "srv", "acme.git"), "main")
		}, func() string { return kit }, func() string { return marketCommit }, engine.TrustHooks|engine.TrustRuntimeHooks),
	)

	Describe("without a scope", func() {
		BeforeEach(func() { proj = filepath.Join(tmp, "work", "x") })

		It("info with no scope shows everything but the plan, says no scope was found and hints --project and --user, keeping the run's flags", func() {
			Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
			info, err := e.Info(ctx, request(engine.ScopeRequest{}, "claude-code"))
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Name).To(Equal("kit"))
			Expect(info.Runs.InstallHooks).To(HaveLen(1))
			Expect(info.NoScope).To(Equal("no .lola/ was found in " + proj + " or above it, and config.yaml sets no default_scope"))
			Expect(info.Hints).To(Equal([]string{rerun("--project"), rerun("--user")}))
			Expect(info.Install).To(Equal([]engine.InfoInstall{{Scope: "", Targets: []string{}, Steps: []plan.Step{}, Findings: []target.Finding{},
				Consent: []engine.InfoConsent{{Kind: "hooks", Item: "kit pre-install", Recorded: engine.RecordedNo},
					{Kind: "runtime-hooks", Item: "kit", Recorded: engine.RecordedUnknown}},
				TrustNeeded: []string{"hooks", "runtime-hooks"}, Digest: info.Digest, Notices: []engine.Notice{}}}))

			for _, scope := range []engine.ScopeRequest{{Project: true}, {User: true}} {
				hinted, err := e.Info(ctx, request(scope, "claude-code"))
				Expect(err).NotTo(HaveOccurred())
				Expect(hinted.NoScope).To(BeEmpty())
				Expect(hinted.Install[0].Scope).NotTo(BeEmpty())
				Expect(hinted.Install[0].Steps).NotTo(BeEmpty())
				Expect(hinted.Install[0].NewProject).To(Equal(scope.Project))
				if scope.Project {
					Expect(hinted.Install[0].Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Text: "new project at " + proj}))
				}
			}
			entries, err := os.ReadDir(proj)
			Expect(err).NotTo(HaveOccurred())
			Expect(entries).To(BeEmpty())
		})

		It("info with a scope but no assistant set up hints one -t run per installed assistant, and none when no assistant is installed", func() {
			Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
			info, err := e.Info(ctx, request(engine.ScopeRequest{Project: true}))
			Expect(err).NotTo(HaveOccurred())
			Expect(info.NoAssistant).To(Equal("no assistant found on your PATH, so lola does not know where to install"))
			doc, err := json.Marshal(info)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(doc)).To(ContainSubstring(`"no_assistant":"no assistant found on your PATH`))
			Expect(info.Hints).To(Equal([]string{}))
			Expect(info.Install).To(HaveLen(1))
			Expect(info.Install[0].Scope).To(Equal("project"))
			Expect(info.Install[0].Steps).To(Equal([]plan.Step{}))

			for _, bin := range []string{"claude", "opencode"} {
				Expect(os.WriteFile(filepath.Join(tmp, "bin", bin), []byte("#!/bin/sh\necho 1.0.0\n"), 0o755)).To(Succeed())
			}
			p, err := paths.Resolve(os.Getenv)
			Expect(err).NotTo(HaveOccurred())
			e = engine.New(p)
			info, err = e.Info(ctx, request(engine.ScopeRequest{Project: true}))
			Expect(err).NotTo(HaveOccurred())
			Expect(info.NoAssistant).To(Equal("none of your installed assistants (claude-code, opencode) is set up here, so lola does not know where to install"))
			Expect(info.Hints).To(Equal([]string{rerun("-t", "claude-code"), rerun("-t", "opencode")}))
			for _, id := range []string{"claude-code", "opencode"} {
				hinted, err := e.Info(ctx, request(engine.ScopeRequest{Project: true}, id))
				Expect(err).NotTo(HaveOccurred())
				Expect(hinted.NoAssistant).To(BeEmpty())
				Expect(hinted.Install[0].Targets).To(Equal([]string{id}))
			}
		})

		It("info with default_scope both returns one install element per scope, and runtime-hooks consent is unknown without a scope", func() {
			Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
			Expect(os.MkdirAll(filepath.Dir(e.Paths.Config), 0o755)).To(Succeed())
			Expect(os.WriteFile(e.Paths.Config, []byte("version: 1\ndefault_scope: both\n"), 0o644)).To(Succeed())
			info, err := e.Info(ctx, request(engine.ScopeRequest{}, "claude-code"))
			Expect(err).NotTo(HaveOccurred())
			Expect(info.NoScope).To(BeEmpty())
			Expect(info.Install).To(HaveLen(2))
			Expect([]string{info.Install[0].Scope, info.Install[1].Scope}).To(Equal([]string{"project", "user"}))
			for _, in := range info.Install {
				Expect(in.Steps).NotTo(BeEmpty())
				Expect(in.Consent).To(ContainElement(HaveField("Kind", "runtime-hooks")))
				Expect(in.Consent).NotTo(ContainElement(HaveField("Recorded", engine.RecordedUnknown)), "a scope knows its settings file")
			}

			Expect(os.Remove(e.Paths.Config)).To(Succeed())
			info, err = e.Info(ctx, request(engine.ScopeRequest{}, "claude-code"))
			Expect(err).NotTo(HaveOccurred())
			Expect(info.NoScope).NotTo(BeEmpty())
			Expect(info.Install[0].Consent).To(ContainElement(engine.InfoConsent{Kind: "runtime-hooks", Item: "kit", Recorded: engine.RecordedUnknown}))
		})

		It("info with default_scope both shows a runtime hook both scopes would run once, though each writes its own settings file", func() {
			writeTree(kit, map[string]string{"hooks/hooks.json": `{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"https://hooks.example.com/stop"}]}]}}`})
			Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
			Expect(os.MkdirAll(filepath.Dir(e.Paths.Config), 0o755)).To(Succeed())
			Expect(os.WriteFile(e.Paths.Config, []byte("version: 1\ndefault_scope: both\n"), 0o644)).To(Succeed())
			info, err := e.Info(ctx, request(engine.ScopeRequest{}, "claude-code"))
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Install).To(HaveLen(2))
			Expect(info.Runs.RuntimeHooks).To(ConsistOf(HaveField("URL", "https://hooks.example.com/stop")))
		})

		It("info with default_scope both whose project plan is refused shows the module's declared runtime hooks and MCP servers beside the user plan's", func() {
			Expect(os.MkdirAll(filepath.Dir(e.Paths.Config), 0o755)).To(Succeed())
			Expect(os.WriteFile(e.Paths.Config, []byte("version: 1\ndefault_scope: both\n"), 0o644)).To(Succeed())
			writeTree(proj, map[string]string{".claude/commands/ship.md": "mine\n"})
			info, err := e.Info(ctx, request(engine.ScopeRequest{}, "claude-code"))
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Install).To(HaveLen(2))
			Expect(info.Install[0].Refusal).NotTo(BeNil(), "the project plan is refused")
			Expect(info.Install[1].Refusal).To(BeNil(), "the user plan is not")
			Expect(info.Runs.RuntimeHooks).To(ContainElement(And(HaveField("Command", "${CLAUDE_PLUGIN_ROOT}/stop.sh"),
				HaveField("Status", engine.RecordedUnknown))), "the refused project's, as declared")
			Expect(info.Runs.RuntimeHooks).To(ContainElement(HaveField("Command", HaveSuffix("/user-modules/kit/stop.sh"))), "the user plan's")
			Expect(info.Runs.MCPServers).To(ContainElement(HaveField("Name", "tools")))
		})
	})
})
