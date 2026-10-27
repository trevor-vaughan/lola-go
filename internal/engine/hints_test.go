package engine_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// Plan 8 §9.2: a hint that repeats or runs another command keeps the run's output flags
// (Options.Display, SyncRequest.Display) and its scope's flags.
var _ = Describe("hints", func() {
	var (
		proj string
		e    *engine.Engine
		sc   engine.Scope
		ctx  = context.Background()
	)
	BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj = filepath.Join(tmp, "proj")
		e.Getwd = func() (string, error) { return proj, nil } // hints are written for a user in the project
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
		writeTree(filepath.Join(proj, "mods", "kit"), map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Kit\n"})
		writeTree(filepath.Join(proj, "mods", "other"), map[string]string{"lola.yml": "name: other\n", "commands/other.md": "Other\n"})
		_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: filepath.Join(proj, "mods", "kit")}},
			engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		// A teammate's entry, pulled behind lola's back: sync needs trust for it.
		dst, data, err := manifest.Edit(filepath.Join(proj, ".lola", "project.yml"),
			manifest.Change{Set: []manifest.Entry{{Name: "other", Source: "mods/other"}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(dst, data, 0o644)).To(Succeed())
	})

	DescribeTable("a hint that runs another command keeps --json, --plain, --no-pager and the scope flags, repeats the flags the user gave, and adds a consent flag or --force only when it answers its own refusal",
		func(run func() error, want func() []string) {
			err := run()
			Expect(err).To(HaveOccurred())
			Expect(errs.Hints(err)).To(Equal(want()))
		},
		Entry("sync's trust refusal",
			func() error {
				_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Display: []string{"--json"}})
				return err
			},
			func() []string {
				return []string{"see them: lola sync --project --json -n", "then accept them: lola sync --project --json --trust"}
			}),
		Entry("sync's trust refusal of named modules, with -y, which answers only the refusal that accepts",
			func() error {
				yes := func([]engine.Removal) (bool, error) { return true, nil }
				_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"other"}, ConfirmRemovals: yes,
					Display: []string{"--plain", "--no-pager"}})
				return err
			},
			func() []string {
				return []string{"see them: lola sync --project other --plain --no-pager -n",
					"then accept them: lola sync --project other --plain --no-pager --trust -y"}
			}),
		Entry("sync's trust refusal after --force and --allow-hooks, which it repeats and never adds",
			func() error {
				_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Force: true, AllowHooks: true},
					Display: []string{"--json"}})
				return err
			},
			func() []string {
				return []string{"see them: lola sync --project --force --allow-hooks --json -n",
					"then accept them: lola sync --project --force --allow-hooks --json --trust"}
			}),
		Entry("install's argument that is neither in the cache nor a source",
			func() error {
				_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "nope"}},
					engine.Options{Targets: []string{"claude-code"}, Display: []string{"--json"}})
				return err
			},
			func() []string {
				return []string{"cached modules: lola cache list --json"}
			}),
		Entry("sync's refusal of a local source outside the project",
			func() error {
				writeTree(filepath.Join(filepath.Dir(proj), "outside", "ext"), map[string]string{"lola.yml": "name: ext\n", "commands/ext.md": "Ext\n"})
				f, err := os.OpenFile(filepath.Join(proj, ".lola", "project.yml"), os.O_APPEND|os.O_WRONLY, 0)
				Expect(err).NotTo(HaveOccurred())
				_, err = f.WriteString("  - name: ext\n    source: ../outside/ext\n")
				Expect(err).NotTo(HaveOccurred())
				Expect(f.Close()).To(Succeed())
				_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Display: []string{"--json"}})
				return err
			},
			func() []string {
				return []string{"install it yourself if you trust it: lola install --project " + filepath.Join(filepath.Dir(proj), "outside", "ext") + " --json"}
			}),
		Entry("sync's trust refusal of a folder edited since lola installed it",
			func() error {
				writeTree(filepath.Join(proj, "mods", "kit"), map[string]string{"commands/kit.md": "Kit, edited\n"})
				_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"kit"}, Display: []string{"--json"}})
				return err
			},
			func() []string {
				return []string{"see them: lola sync --project kit --json -n", "then accept them: lola sync --project kit --json --trust",
					"edited it yourself? lola install --project ./mods/kit --json"}
			}),
		Entry("sync's trust diff of a folder edited since lola installed it, under --plain and --no-pager",
			func() error {
				writeTree(filepath.Join(proj, "mods", "kit"), map[string]string{"commands/kit.md": "Kit, edited\n"})
				var line string
				decline := func(r engine.TrustReport) (engine.TrustAnswer, error) {
					for _, c := range r.Changes {
						if c.InstallOwn != "" {
							line = c.InstallOwn
						}
					}
					return engine.TrustAnswer{}, nil
				}
				_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"kit"}, Trust: decline, Display: []string{"--plain", "--no-pager"}})
				Expect(err).To(MatchError(engine.ErrDeclined))
				// The diff's line, read here as the hint it is.
				return errs.WithHints(err, line)
			},
			func() []string {
				return []string{"edited it yourself? lola install --project ./mods/kit --plain --no-pager"}
			}),
		Entry("cache add of a marketplace entry whose name your cache holds from another source",
			func() error {
				writeTree(filepath.Join(proj, "market"), map[string]string{
					".claude-plugin/marketplace.json": `{"name": "acme", "owner": {"name": "Acme"}, "plugins": [{"name": "kit", "source": "./kit"}]}`,
					"kit/commands/kit.md":             "Another kit\n"})
				_, err := e.AddMarket(ctx, engine.MarketAddRequest{Source: filepath.Join(proj, "market")})
				Expect(err).NotTo(HaveOccurred())
				_, err = e.AddEntry(ctx, "@acme/kit", "", "", []string{"--json"})
				return err
			},
			func() []string { return []string{"remove the cached one first: lola cache rm kit --json"} }),
		Entry("a profile's trust refusal as it turns on",
			func() error {
				writeTree(filepath.Join(proj, "mods", "rev"), map[string]string{"lola.yml": "name: rev\n", "commands/rev.md": "Rev\n"})
				writeTree(filepath.Join(proj, ".lola", "profiles"), map[string]string{"review.yml": "version: 1\nmodules:\n  - name: rev\n    source: ../../mods/rev\n"})
				_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Names: []string{"review"}},
					Display: []string{"--json"}})
				return err
			},
			func() []string {
				return []string{"see them: lola profile --project project:review --on --json -n",
					"then accept them: lola profile --project project:review --on --json --trust"}
			}),
		Entry("check's argument that is neither a source nor a module in your cache",
			func() error {
				_, err := e.Check(ctx, sc, proj, []string{"nope"}, nil, []string{"--plain"})
				return err
			},
			func() []string {
				return []string{"cached modules: lola cache list --plain"}
			}),
		Entry("cache add of a module whose name your cache holds from another source",
			func() error {
				writeTree(filepath.Join(proj, "mods", "kit2"), map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Another kit\n"})
				_, err := e.AddSource(ctx, proj, filepath.Join(proj, "mods", "kit2"), "", "", []string{"--json"})
				return err
			},
			func() []string { return []string{"remove the cached one first: lola cache rm kit --json"} }),
		Entry("install of a module whose name your cache holds from another source",
			func() error {
				writeTree(filepath.Join(proj, "mods", "kit2"), map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Another kit\n"})
				_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: filepath.Join(proj, "mods", "kit2")}},
					engine.Options{Targets: []string{"claude-code"}, Display: []string{"--plain", "--no-pager"}})
				return err
			},
			func() []string { return []string{"remove the cached one first: lola cache rm kit --plain --no-pager"} }),
		Entry("uninstall of a module an active profile lists",
			func() error {
				writeTree(filepath.Join(proj, "mods", "rev"), map[string]string{"lola.yml": "name: rev\n", "commands/rev.md": "Rev\n"})
				writeTree(filepath.Join(proj, ".lola", "profiles"), map[string]string{"review.yml": "version: 1\nmodules:\n  - name: rev\n    source: ../../mods/rev\n"})
				trustAll := func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }
				_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Names: []string{"review"}},
					Trust: trustAll})
				Expect(err).NotTo(HaveOccurred())
				_, err = e.Uninstall(ctx, sc, []string{"rev"}, engine.Options{Display: []string{"--json"}})
				return err
			},
			func() []string {
				return []string{"turn the profile off: lola profile --project project:review --off --json"}
			}),
		Entry("install's template refusal, whose check repeats the assistants given",
			func() error {
				writeTree(filepath.Join(proj, "mods", "bad"), map[string]string{"lola.yml": "name: bad\nvars:\n  x: \"a\\nb\"\n",
					"commands/c.md.liquid": "---\ndescription: {{ vars.x }}\n---\nBody\n"})
				_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: filepath.Join(proj, "mods", "bad")}},
					engine.Options{Targets: []string{"claude-code"}, Display: []string{"--json"}})
				return err
			},
			func() []string {
				return []string{"lola check --project " + filepath.Join(proj, "mods", "bad") + " --target claude-code --json"}
			}),
		Entry("render's refusal of a tracked lock.json, for a user outside the project",
			func() error {
				gitIn(proj, "init", "-q")
				gitIn(proj, "add", "-f", ".lola/lock.json")
				e.Getwd = func() (string, error) { return filepath.Dir(proj), nil }
				_, err := e.Render(ctx, sc, engine.Options{Display: []string{"--json"}})
				return err
			},
			func() []string { return []string{"git -C " + proj + " rm --cached .lola/lock.json"} }),
		Entry("render's refusal of a tracked lock.json, whose root untrusted.Text would change",
			func() error {
				odd := filepath.Join(filepath.Dir(proj), "odd\x1bproj")
				oddSc := engine.Scope{Kind: target.Project, ProjectDir: odd}
				_, err := e.Install(ctx, oddSc, odd, []engine.InstallRequest{{Arg: filepath.Join(proj, "mods", "kit")}},
					engine.Options{Targets: []string{"claude-code"}})
				Expect(err).NotTo(HaveOccurred())
				gitIn(odd, "init", "-q")
				gitIn(odd, "add", "-f", ".lola/lock.json")
				e.Getwd = func() (string, error) { return filepath.Dir(proj), nil }
				_, err = e.Render(ctx, oddSc, engine.Options{Display: []string{"--json"}})
				Expect(err).To(MatchError(ContainSubstring(".lola/lock.json is tracked by git")))
				return err
			},
			func() []string { return nil }),
	)

	// Ruling R-8-U5: what no command fixes is said in the error, so every hint is a command.
	DescribeTable("a refusal says in its error what no command fixes, and its hints are commands",
		func(run func() error, msg func() string, hints []string) {
			err := run()
			Expect(err).To(MatchError(msg()))
			Expect(errs.Hints(err)).To(Equal(hints))
		},
		Entry("install's argument that is neither in the cache nor a source",
			func() error {
				_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: "nope"}}, engine.Options{Targets: []string{"claude-code"}})
				return err
			},
			func() string {
				return `"nope" is neither a module in your cache nor a source; sources are ` + source.AcceptedForms
			},
			[]string{"cached modules: lola cache list"}),
		Entry("cache add's argument that is not a source",
			func() error {
				_, err := e.AddSource(ctx, proj, "nope", "", "", nil)
				return err
			},
			func() string { return `"nope" is not a source; sources are ` + source.AcceptedForms }, nil),
		Entry("check's argument that is neither a source nor a module in your cache",
			func() error {
				_, err := e.Check(ctx, sc, proj, []string{"nope"}, nil, nil)
				return err
			},
			func() string {
				return `"nope" is neither a source nor a module in your cache; pass a module folder or another source, ` +
					"e.g. lola check ./my-module; sources are " + source.AcceptedForms
			},
			[]string{"cached modules: lola cache list"}),
		Entry("uninstall of a module default.yml lists",
			func() error {
				p, err := paths.Resolve(os.Getenv)
				Expect(err).NotTo(HaveOccurred())
				writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" +
					"  - {name: kit, source: " + filepath.Join(proj, "mods", "kit") + "}\n"})
				user := engine.Scope{Kind: target.User}
				trustAll := func(engine.TrustReport) (engine.TrustAnswer, error) { return engine.TrustAnswer{All: true}, nil }
				_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, Trust: trustAll})
				Expect(err).NotTo(HaveOccurred())
				_, err = e.Uninstall(ctx, user, []string{"kit"}, engine.Options{Display: []string{"--json"}})
				return err
			},
			func() string {
				p, err := paths.Resolve(os.Getenv)
				Expect(err).NotTo(HaveOccurred())
				return "kit is wanted by profile default; remove it from " + filepath.Join(p.UserProfiles, "default.yml") +
					", which is always on at user scope"
			},
			nil),
	)
})
