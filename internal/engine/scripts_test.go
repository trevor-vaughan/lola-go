package engine_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/consent"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("install scripts", func() {
	var (
		tmp, proj, src string
		p              paths.Paths
		e              *engine.Engine
		sc             engine.Scope
		ctx            = context.Background()
	)
	// configure writes config.yaml naming the user's scripts, event key → path.
	configure := func(lines string) {
		Expect(os.MkdirAll(filepath.Dir(p.Config), 0o700)).To(Succeed())
		Expect(os.WriteFile(p.Config, []byte("version: 1\n"+lines), 0o600)).To(Succeed())
	}
	// projectYML writes the project's project.yml with lines after its version.
	projectYML := func(lines string) {
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(proj, ".lola", "project.yml"), []byte("version: 1\n"+lines), 0o644)).To(Succeed())
	}
	// script writes an executable shell script at path.
	script := func(path, body string) string {
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		Expect(os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755)).To(Succeed())
		return path
	}
	install := func(opts engine.Options) (*engine.Result, error) {
		opts.Targets = []string{"claude-code"}
		return e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: src}}, opts)
	}

	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj = filepath.Join(tmp, "proj")
		e.Getwd = func() (string, error) { return proj, nil }
		src = filepath.Join(tmp, "src", "example")
		writeTree(src, exampleModule)
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
	})

	DescribeTable("a missing, irregular or non-executable configured script is refused naming file, line and key",
		func(source, problem string) {
			var path, msg, chmod string
			var opts engine.Options
			switch source {
			case "config.yaml":
				path = filepath.Join(tmp, "bin", "pre.sh")
				chmod = path
				configure("pre_install: " + path + "\n")
				msg = p.Config + ":2: pre_install: " + path
			case "project.yml":
				path = filepath.Join(proj, "ci", "check.sh")
				chmod = path
				projectYML("targets: [claude-code]\npre_install: ci/check.sh\n")
				msg = filepath.Join(proj, ".lola", "project.yml") + ":3: pre_install: ci/check.sh"
			case "flag":
				path, chmod = filepath.Join(tmp, "hooks", "pre.sh"), "./hooks/pre.sh"
				opts.PreInstall = engine.FlagScript{Given: "./hooks/pre.sh", Path: path}
				msg = "--pre-install ./hooks/pre.sh"
			}
			switch problem {
			case "missing":
				msg += " does not exist"
			case "irregular":
				Expect(os.MkdirAll(path, 0o755)).To(Succeed())
				msg += " is not a regular file"
			case "not executable":
				script(path, "exit 0\n")
				Expect(os.Chmod(path, 0o644)).To(Succeed())
				msg += " is not executable"
			}
			_, err := install(opts)
			Expect(err).To(MatchError(msg))
			if problem == "not executable" {
				Expect(errs.Hints(err)).To(HaveExactElements("chmod +x " + chmod))
			} else {
				Expect(errs.Hints(err)).To(BeEmpty())
			}
			Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory(), "nothing was written")
			Expect(filepath.Join(proj, ".lola", "lock.json")).NotTo(BeAnExistingFile())
		},
		Entry("config.yaml: a missing script", "config.yaml", "missing"),
		Entry("config.yaml: a directory", "config.yaml", "irregular"),
		Entry("config.yaml: a script that is not executable", "config.yaml", "not executable"),
		Entry("project.yml: a missing script", "project.yml", "missing"),
		Entry("project.yml: a directory", "project.yml", "irregular"),
		Entry("project.yml: a script that is not executable", "project.yml", "not executable"),
		Entry("flag: a missing script", "flag", "missing"),
		Entry("flag: a directory", "flag", "irregular"),
		Entry("flag: a script that is not executable", "flag", "not executable"),
	)

	It("a project script that resolves outside the project is refused naming the file, line and key", func() {
		// through a link among its directories
		script(filepath.Join(tmp, "shared", "check.sh"), "exit 0\n")
		Expect(os.Symlink(filepath.Join(tmp, "shared"), filepath.Join(proj, "ci"))).To(Succeed())
		projectYML("targets: [claude-code]\npost_install: ci/check.sh\n")
		_, err := install(engine.Options{})
		Expect(errs.Hints(err)).To(BeEmpty())
		Expect(err).To(MatchError(filepath.Join(proj, ".lola", "project.yml") + ":3: post_install: ci/check.sh resolves outside the project (" +
			filepath.Join(tmp, "shared", "check.sh") + ")"))
		Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory(), "nothing was written")
	})

	It("a project script given as an absolute path is refused naming the file, line and key, even inside the project", func() {
		for _, abs := range []string{script(filepath.Join(proj, "ci", "check.sh"), "exit 0\n"), script(filepath.Join(tmp, "shared", "check.sh"), "exit 0\n")} {
			projectYML("targets: [claude-code]\npre_install: " + abs + "\n")
			_, err := install(engine.Options{Trust: engine.TrustScripts})
			Expect(err).To(MatchError(filepath.Join(proj, ".lola", "project.yml") + ":3: pre_install: " + abs +
				" is an absolute path; name the script by its path inside the project"))
			Expect(errs.Hints(err)).To(BeEmpty())
			Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory(), "nothing was written")
		}
	})

	It("a project script under a directory link that stays inside the project runs, shown as the file that runs", func() {
		script(filepath.Join(proj, "tools", "ci", "check.sh"), "exit 0\n")
		Expect(os.Symlink(filepath.Join("tools", "ci"), filepath.Join(proj, "ci"))).To(Succeed())
		projectYML("targets: [claude-code]\npre_install: ci/check.sh\n")
		res, err := install(engine.Options{Trust: engine.TrustScripts})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Scripts).To(ConsistOf(And(HaveField("Path", "tools/ci/check.sh"), HaveField("Status", engine.HookRan))))
	})

	It("a project script that is a symbolic link is refused naming the file, line and key", func() {
		script(filepath.Join(proj, "ci", "real.sh"), "exit 0\n")
		Expect(os.Symlink("real.sh", filepath.Join(proj, "ci", "check.sh"))).To(Succeed())
		projectYML("targets: [claude-code]\npre_install: ci/check.sh\n")
		_, err := install(engine.Options{})
		Expect(err).To(MatchError(filepath.Join(proj, ".lola", "project.yml") + ":3: pre_install: ci/check.sh is a symbolic link; lola runs only a regular file inside the project"))
		Expect(errs.Hints(err)).To(BeEmpty())
		Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory(), "nothing was written")
	})

	DescribeTable("a project script path with a .. component is refused naming the file, line and key",
		func(setup func() string) {
			path := setup()
			projectYML("targets: [claude-code]\npre_install: " + path + "\n")
			_, err := install(engine.Options{Trust: engine.TrustScripts})
			Expect(err).To(MatchError(filepath.Join(proj, ".lola", "project.yml") + ":3: pre_install: " + path +
				" has a .. component; name the script by its path inside the project"))
			Expect(errs.Hints(err)).To(BeEmpty())
			Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory(), "nothing was written")
		},
		Entry("inside the project", func() string {
			script(filepath.Join(proj, "check.sh"), "exit 0\n")
			Expect(os.MkdirAll(filepath.Join(proj, "ci"), 0o755)).To(Succeed())
			return "ci/../check.sh"
		}),
		// Join cleans link/.. away, so lola would run <root>/evil.sh, where a shell reading the path
		// resolves the link first and finds another file (S1)
		Entry("through a link", func() string {
			script(filepath.Join(proj, "evil.sh"), "exit 0\n")
			script(filepath.Join(tmp, "elsewhere", "evil.sh"), "exit 0\n")
			Expect(os.Symlink(filepath.Join(tmp, "elsewhere", "sub"), filepath.Join(proj, "link"))).To(Succeed())
			Expect(os.MkdirAll(filepath.Join(tmp, "elsewhere", "sub"), 0o755)).To(Succeed())
			return "link/../evil.sh"
		}),
	)

	It("a project script path with a control character or a line break is refused naming the file, line and key", func() {
		for _, name := range []string{"pre\x1b[31m.sh", "pre\nfake.sh"} {
			script(filepath.Join(proj, "ci", name), "exit 0\n")
			projectYML("targets: [claude-code]\npre_install: " + strconv.Quote("ci/"+name) + "\n")
			_, err := install(engine.Options{Trust: engine.TrustScripts})
			Expect(err).To(MatchError(MatchRegexp(`^` + regexp.QuoteMeta(filepath.Join(proj, ".lola", "project.yml")) +
				`:3: pre_install: ci/pre.*\.sh holds a control character or a line break; lola runs only a script whose path is plain text$`)))
			Expect(err.Error()).NotTo(ContainSubstring("\x1b"))
			Expect(strings.Count(err.Error(), "\n")).To(BeZero(), "on one line")
			Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory(), "nothing was written")
		}
	})

	It("a project script refusal names a project.yml in a directory holding control or format characters as text that cannot drive the terminal", func() {
		proj = filepath.Join(tmp, "evil\x1b[31m\u202eproj")
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		projectYML("targets: [claude-code]\npre_install: ci/missing.sh\n")
		_, err := install(engine.Options{Trust: engine.TrustScripts})
		Expect(err).To(MatchError(filepath.Join(tmp, "evil?[31m?proj", ".lola", "project.yml") + ":3: pre_install: ci/missing.sh does not exist"))
		Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory(), "nothing was written")
	})

	It("a non-executable script whose path is not plain text gets no chmod hint", func() {
		path := script(filepath.Join(tmp, "bin", "pre\nx.sh"), "exit 0\n")
		Expect(os.Chmod(path, 0o644)).To(Succeed())
		configure("pre_install: " + strconv.Quote(path) + "\n")
		_, err := install(engine.Options{})
		Expect(err).To(MatchError(p.Config + ":2: pre_install: " + filepath.Join(tmp, "bin", "pre?x.sh") + " is not executable"))
		Expect(errs.Hints(err)).To(BeEmpty(), "a chmod of the path as shown would name another file")
	})

	It("lola's edits of project.yml keep pre_install and post_install", func() {
		_, err := install(engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		yml := filepath.Join(proj, ".lola", "project.yml")
		b, err := os.ReadFile(yml)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(yml, append(b, "pre_install: ci/check.sh\npost_install: ci/after.sh # the team's\n"...), 0o644)).To(Succeed())
		_, err = e.Uninstall(ctx, sc, []string{"example"}, engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		b, err = os.ReadFile(yml)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("version: 1\ntargets: [claude-code]\npre_install: ci/check.sh\npost_install: ci/after.sh # the team's\n"))
	})

	Describe("running", func() {
		var log string
		// logLine is what each script and hook appends to the log: who it is, and what lola told it.
		const logLine = `echo "$WHO ${LOLA_SCRIPT_SOURCE:--} $LOLA_HOOK $LOLA_MODULE_NAME $LOLA_ASSISTANT $LOLA_SCOPE $LOLA_MODULE_PATH $LOLA_PROJECT_PATH $(pwd) written=$written" >> "$SCRIPT_LOG"` + "\n"
		logger := func(who string) string {
			return "WHO=" + who + "\nwritten=no\ntest -e \"$LOLA_PROJECT_PATH/.claude/commands/ship.md\" && written=yes\n" + logLine
		}
		lines := func() []string {
			GinkgoHelper()
			b, err := os.ReadFile(log)
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			Expect(err).NotTo(HaveOccurred())
			return strings.Split(strings.TrimSpace(string(b)), "\n")
		}
		// who is the first word of each log line.
		who := func() []string {
			var out []string
			for _, l := range lines() {
				out = append(out, strings.Fields(l)[0]+" "+strings.Fields(l)[1]+" "+strings.Fields(l)[2])
			}
			return out
		}
		hooked := func() {
			writeTree(src, map[string]string{"lola.yml": "name: example\nhooks: {pre-install: hooks/pre.sh, post-install: hooks/post.sh}\n",
				"hooks/pre.sh": "#!/bin/sh\n" + logger("hook"), "hooks/post.sh": "#!/bin/sh\n" + logger("hook")})
			Expect(os.Chmod(filepath.Join(src, "hooks/pre.sh"), 0o755)).To(Succeed())
			Expect(os.Chmod(filepath.Join(src, "hooks/post.sh"), 0o755)).To(Succeed())
		}
		// all configures a script of every source for both events, each logging its source.
		all := func() engine.Options {
			configure("pre_install: " + script(filepath.Join(tmp, "bin", "pre.sh"), logger("user")) + "\npost_install: " +
				script(filepath.Join(tmp, "bin", "post.sh"), logger("user")) + "\n")
			script(filepath.Join(proj, "ci", "check.sh"), logger("project"))
			script(filepath.Join(proj, "ci", "after.sh"), logger("project"))
			projectYML("targets: [claude-code]\npre_install: ci/check.sh\npost_install: ci/after.sh\n")
			return engine.Options{
				PreInstall:  engine.FlagScript{Given: "flag/pre.sh", Path: script(filepath.Join(tmp, "flag", "pre.sh"), logger("flag"))},
				PostInstall: engine.FlagScript{Given: "flag/post.sh", Path: script(filepath.Join(tmp, "flag", "post.sh"), logger("flag"))},
				Trust:       engine.TrustScripts | engine.TrustHooks,
			}
		}
		BeforeEach(func() {
			log = filepath.Join(tmp, "scripts.log")
			e.Environ = func() []string { return append(os.Environ(), "SCRIPT_LOG="+log) }
		})

		It("scripts run in order: user, project, flag, module hook; then module hook, flag, project, user", func() {
			hooked()
			res, err := install(all())
			Expect(err).NotTo(HaveOccurred())
			Expect(who()).To(Equal([]string{
				"user user pre-install", "project project pre-install", "flag flag pre-install", "hook - pre-install",
				"hook - post-install", "flag flag post-install", "project project post-install", "user user post-install",
			}))
			Expect(res.Scripts).To(HaveLen(6))
			Expect(res.Scripts[0]).To(Equal(engine.ScriptRun{Event: engine.PreInstall, Source: engine.ScriptUser, Path: filepath.Join(tmp, "bin", "pre.sh"),
				Module: "example", Target: "claude-code", Status: engine.HookRan}))
			Expect(res.Scripts[1]).To(HaveField("Path", "ci/check.sh"))
			Expect(res.Scripts[2]).To(HaveField("Path", "flag/pre.sh"))
		})

		It("each script runs once per module per target with the hook environment and LOLA_SCRIPT_SOURCE", func() {
			configure("pre_install: " + script(filepath.Join(tmp, "bin", "pre.sh"), logger("user")) + "\npost_install: " +
				script(filepath.Join(tmp, "bin", "post.sh"), logger("user")) + "\n")
			other := filepath.Join(tmp, "src", "other")
			writeTree(other, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "Other\n"})
			_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: src}, {Arg: other}}, engine.Options{Targets: []string{"claude-code", "gemini-cli"}})
			Expect(err).NotTo(HaveOccurred())
			mods := filepath.Join(proj, ".lola", "modules")
			var want []string
			for _, ev := range []struct{ hook, written string }{{"pre-install", "no"}, {"post-install", "yes"}} {
				for _, m := range []string{"example", "other"} {
					for _, t := range []string{"claude-code", "gemini-cli"} {
						// one plan writes every module's files: after every pre-install script, before any post-install one
						want = append(want, strings.Join([]string{"user", "user", ev.hook, m, t, "project", filepath.Join(mods, m), proj, filepath.Join(mods, m), "written=" + ev.written}, " "))
					}
				}
			}
			Expect(lines()).To(Equal(want))
		})

		It("a module hook never sees LOLA_SCRIPT_SOURCE, even one lola's own environment holds", func() {
			hooked()
			e.Environ = func() []string { return append(os.Environ(), "SCRIPT_LOG="+log, "LOLA_SCRIPT_SOURCE=user") }
			configure("pre_install: " + script(filepath.Join(tmp, "bin", "pre.sh"), logger("user")) + "\n")
			_, err := install(engine.Options{Trust: engine.TrustHooks})
			Expect(err).NotTo(HaveOccurred())
			Expect(who()).To(Equal([]string{"user user pre-install", "hook - pre-install", "hook - post-install"}))
		})

		It("a script runs from its resolved absolute path, never a file of that name in the module copy", func() {
			marker := filepath.Join(tmp, "marker")
			// the module holds files at the scripts' relative paths, which would leave a marker
			writeTree(src, map[string]string{"lola.yml": "name: example\n", "commands/ship.md": "Ship $ARGUMENTS\n",
				"flag/pre.sh": "#!/bin/sh\ntouch " + marker + "\n", "ci/check.sh": "#!/bin/sh\ntouch " + marker + "\n"})
			Expect(os.Chmod(filepath.Join(src, "flag/pre.sh"), 0o755)).To(Succeed())
			Expect(os.Chmod(filepath.Join(src, "ci/check.sh"), 0o755)).To(Succeed())
			script(filepath.Join(proj, "ci", "check.sh"), logger("project"))
			projectYML("targets: [claude-code]\npre_install: ci/check.sh\n")
			_, err := install(engine.Options{Trust: engine.TrustScripts,
				PreInstall: engine.FlagScript{Given: "flag/pre.sh", Path: script(filepath.Join(tmp, "flag", "pre.sh"), logger("flag"))}})
			Expect(err).NotTo(HaveOccurred())
			Expect(who()).To(Equal([]string{"project project pre-install", "flag flag pre-install"}))
			Expect(marker).NotTo(BeAnExistingFile())

			// a relative path, which a caller left unresolved, is taken from lola's working directory,
			// never the module copy that holds a file of that name
			Expect(os.Remove(log)).To(Succeed())
			Expect(os.RemoveAll(filepath.Join(proj, ".lola", "modules"))).To(Succeed())
			GinkgoT().Chdir(tmp)
			_, err = install(engine.Options{Trust: engine.TrustScripts, PreInstall: engine.FlagScript{Given: "flag/pre.sh", Path: "flag/pre.sh"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(who()).To(Equal([]string{"project project pre-install", "flag flag pre-install"}))
			Expect(marker).NotTo(BeAnExistingFile())
		})

		DescribeTable("project scripts run only in runs whose scope is that project",
			func(run func() error, want []string) {
				configure("pre_install: " + script(filepath.Join(tmp, "bin", "pre.sh"), logger("user")) + "\n")
				script(filepath.Join(proj, "ci", "check.sh"), logger("project"))
				projectYML("targets: [claude-code]\npre_install: ci/check.sh\n")
				Expect(run()).To(Succeed())
				var got []string
				for _, l := range lines() {
					f := strings.Fields(l)
					got = append(got, f[0]+" "+f[5])
				}
				Expect(got).To(Equal(want))
			},
			Entry("--user", func() error {
				_, err := e.Install(ctx, engine.Scope{Kind: target.User}, proj, []engine.InstallRequest{{Arg: src}},
					engine.Options{Targets: []string{"claude-code"}})
				return err
			}, []string{"user user"}),
			Entry("--scope both", func() error {
				_, err := e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Modules: []engine.InstallRequest{{Arg: src}},
					Scopes: []engine.ScopeInstall{
						{ScopeChoice: engine.ScopeChoice{Scope: sc}, Targets: []string{"claude-code"}},
						{ScopeChoice: engine.ScopeChoice{Scope: engine.Scope{Kind: target.User}}, Targets: []string{"claude-code"}},
					}, Options: engine.Options{Trust: engine.TrustScripts}})
				return err
			}, []string{"user project", "project project", "user user"}),
		)

		DescribeTable("scripts wrap module hooks wherever those run",
			func(run func() error) {
				hooked()
				configure("pre_install: " + script(filepath.Join(tmp, "bin", "pre.sh"), logger("user")) + "\n")
				Expect(run()).To(Succeed())
				Expect(who()).To(HaveExactElements("user user pre-install", "hook - pre-install", "hook - post-install"))
			},
			Entry("install", func() error {
				_, err := install(engine.Options{Trust: engine.TrustHooks})
				return err
			}),
			Entry("render replacing a copy", func() error {
				if _, err := install(engine.Options{Trust: engine.TrustHooks}); err != nil {
					return err
				}
				Expect(os.Remove(log)).To(Succeed())
				Expect(os.RemoveAll(filepath.Join(proj, ".lola", "modules", "example"))).To(Succeed())
				_, err := e.Render(ctx, sc, engine.Options{})
				return err
			}),
			Entry("sync", func() error {
				Expect(os.CopyFS(filepath.Join(proj, "mods", "example"), os.DirFS(src))).To(Succeed())
				writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: example, source: ./mods/example}\n"})
				_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries | engine.TrustHooks}})
				return err
			}),
			Entry("profile --on", func() error {
				Expect(os.CopyFS(filepath.Join(proj, "mods", "example"), os.DirFS(src))).To(Succeed())
				writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n",
					".lola/profiles/review.yml": "version: 1\nmodules:\n  - name: example\n    source: ../../mods/example\n"})
				_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileOn, Names: []string{"review"}},
					Options: engine.Options{Trust: engine.TrustEntries | engine.TrustProfiles | engine.TrustHooks}})
				return err
			}),
			Entry("migrate (InstallAll as lola migrate calls it: the module's own targets, BeforeWrite)", func() error {
				_, err := e.InstallAll(ctx, engine.InstallSpec{Cwd: proj, Modules: []engine.InstallRequest{{Arg: src, Targets: []string{"claude-code"}}},
					Scopes:      []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: sc}, Targets: []string{"claude-code"}}},
					Options:     engine.Options{Trust: engine.TrustHooks},
					BeforeWrite: func(*engine.InstallResult) error { return nil }})
				return err
			}),
		)

		It("scripts wrap a module without hooks", func() {
			configure("pre_install: " + script(filepath.Join(tmp, "bin", "pre.sh"), logger("user")) + "\npost_install: " +
				script(filepath.Join(tmp, "bin", "post.sh"), logger("user")) + "\n")
			res, err := install(engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(who()).To(Equal([]string{"user user pre-install", "user user post-install"}))
			Expect(res.Modules[0].Hooks).To(BeEmpty())

			// a reinstall that keeps the copy installs nothing, so nothing wraps it
			Expect(os.Remove(log)).To(Succeed())
			_, err = install(engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(lines()).To(BeNil())
		})

		It("a failing pre script writes nothing and names script, source and exit status; a failing post script warns", func() {
			projectYML("targets: [claude-code]\npre_install: ci/check.sh\n")
			script(filepath.Join(proj, "ci", "check.sh"), "exit 3\n")
			res, err := install(engine.Options{Trust: engine.TrustScripts})
			Expect(err).To(MatchError(ContainSubstring("pre-install script ci/check.sh (project) failed for example on claude-code: exit status 3")))
			Expect(res).To(BeNil())
			Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory())
			Expect(filepath.Join(proj, ".lola", "lock.json")).NotTo(BeAnExistingFile())
			Expect(filepath.Join(proj, ".lola", "modules", "example")).NotTo(BeADirectory(), "the copy is rolled back")

			configure("post_install: " + script(filepath.Join(tmp, "bin", "post.sh"), "exit 1\n") + "\n")
			projectYML("targets: [claude-code]\n")
			res, err = install(engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
				Text: "post-install script " + filepath.Join(tmp, "bin", "post.sh") + " (user) failed for example on claude-code: exit status 1; the installation was kept"}))
			Expect(filepath.Join(proj, ".claude", "commands", "ship.md")).To(BeAnExistingFile())
			Expect(res.Scripts).To(ConsistOf(HaveField("Status", engine.HookFailed)))
		})

		It("a failed flag script's retry keeps --pre-install and --post-install", func() {
			pre, post := script(filepath.Join(tmp, "flag", "pre.sh"), "exit 3\n"), script(filepath.Join(tmp, "flag", "post.sh"), "exit 0\n")
			_, err := install(engine.Options{PreInstall: engine.FlagScript{Given: "flag/pre.sh", Path: pre}, PostInstall: engine.FlagScript{Given: "flag/post.sh", Path: post}})
			Expect(err).To(MatchError(ContainSubstring("pre-install script flag/pre.sh (flag) failed for example on claude-code: exit status 3")))
			Expect(errs.Hints(err)).To(ContainElement("fetched modules stay in your cache; retry with: lola install --project example "+
				"--pre-install flag/pre.sh --post-install flag/post.sh"), "the retry keeps the gate the user gave")
		})

		It("a pre script that times out writes nothing and says timed out after the hook timeout", func() {
			e.HookTimeout = 200 * time.Millisecond
			configure("pre_install: " + script(filepath.Join(tmp, "bin", "pre.sh"), "sleep 5\n") + "\n")
			_, err := install(engine.Options{})
			Expect(err).To(MatchError("pre-install script " + filepath.Join(tmp, "bin", "pre.sh") + " (user) failed for example on claude-code: timed out after 200ms"))
			Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory())
		})

		It("an untrusted project pre script refuses off a terminal with --trust=scripts; post is declined and pending", func() {
			script(filepath.Join(proj, "ci", "check.sh"), logger("project"))
			projectYML("targets: [claude-code]\npre_install: ci/check.sh\n")
			opts := engine.Options{Display: []string{"--json"}, Run: "lola install", Rerun: func(k engine.TrustKinds) string { return "lola install " + src + " " + k.Arg() }}
			_, err := install(opts)
			Expect(err).To(MatchError("lola install needs your trust for the pre-install script ci/check.sh of this project (new), and there is no terminal to ask on"))
			Expect(errs.Hints(err)).To(HaveExactElements("review it: lola info --project "+src+" --target claude-code --json",
				"then consent to it: lola install "+src+" --trust=scripts"))
			Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory())
			Expect(lines()).To(BeNil())

			script(filepath.Join(proj, "ci", "after.sh"), logger("project"))
			projectYML("targets: [claude-code]\npost_install: ci/after.sh\n")
			res, err := install(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Join(proj, ".claude", "commands", "ship.md")).To(BeAnExistingFile())
			Expect(lines()).To(BeNil())
			Expect(res.Scripts).To(ConsistOf(And(HaveField("Event", engine.PostInstall), HaveField("Source", engine.ScriptProject),
				HaveField("Path", "ci/after.sh"), HaveField("Module", "example"), HaveField("Target", "claude-code"), HaveField("Status", engine.HookDeclined),
				HaveField("Root()", proj))))
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
				Text: "example: post-install script ci/after.sh (project) skipped without your trust; review and run it: lola sync --project example, " +
					"or trust it without asking: lola sync --project example --trust=scripts"}))
			led, err := e.Ledger(sc)
			Expect(err).NotTo(HaveOccurred())
			Expect(led.Modules["example"].ScriptsPending).To(BeTrue())
		})

		It("a sync refused for its entries also trusts the project's pre script its install would run", func() {
			Expect(os.CopyFS(filepath.Join(proj, "mods", "example"), os.DirFS(src))).To(Succeed())
			script(filepath.Join(proj, "ci", "check.sh"), logger("project"))
			projectYML("targets: [claude-code]\npre_install: ci/check.sh\nmodules:\n  - {name: example, source: ./mods/example}\n")
			req := engine.SyncRequest{Scope: sc, Display: []string{"--json"}}
			_, err := e.Sync(ctx, req)
			Expect(err).To(HaveOccurred())
			Expect(errs.Hints(err)).To(ContainElement("then accept them: lola sync --project --trust=entries,scripts --json"))
			req.Options.Trust = engine.TrustEntries | engine.TrustScripts
			_, err = e.Sync(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(who()).To(Equal([]string{"project project pre-install"}))
		})

		DescribeTable("a refusal off a terminal hints every kind the run is refused for, so its command is not refused again",
			func(setup func(), dry bool, hint func(res *engine.Result, err error) string, want engine.TrustKinds) {
				setup()
				script(filepath.Join(proj, "ci", "check.sh"), logger("project"))
				projectYML("targets: [claude-code]\npre_install: ci/check.sh\n")
				opts := engine.Options{DryRun: dry, Rerun: func(k engine.TrustKinds) string { return "lola install " + src + " " + k.Arg() }}
				res, err := install(opts)
				Expect(hint(res, err)).To(Equal("lola install " + src + " " + want.Arg()))
				opts.DryRun, opts.Trust = false, want
				_, err = install(opts)
				Expect(err).NotTo(HaveOccurred(), "the hinted run is refused for nothing")
				Expect(who()).To(ContainElement("project project pre-install"))
			},
			Entry("a pre-install hook and the project's pre-install script: --trust=scripts,hooks", hooked, false,
				func(_ *engine.Result, err error) string {
					Expect(err).To(MatchError("example has a pre-install hook, and lola runs hooks only with your consent"))
					return strings.TrimPrefix(errs.Hints(err)[len(errs.Hints(err))-1], "then consent to it: ")
				}, engine.TrustScripts|engine.TrustHooks),
			Entry("runtime hooks and the project's pre-install script: --trust=scripts,runtime-hooks", func() {
				writeTree(src, map[string]string{"lola.yml": "name: example\ncomponents:\n  runtime_hooks: [{path: hooks/hooks.json}]\n",
					"hooks/hooks.json": `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"}]}]}}`})
			}, true, func(res *engine.Result, err error) string {
				Expect(err).NotTo(HaveOccurred())
				i := slices.IndexFunc(res.Findings, func(f target.Finding) bool { return f.Code == "claude-code/runtime-hooks-declined" })
				Expect(i).To(BeNumerically(">=", 0))
				return strings.TrimPrefix(res.Findings[i].Hint, "review the hooks listed above, then consent to them: ")
			}, engine.TrustScripts|engine.TrustRuntimeHooks),
		)

		Describe("a pending project post script", func() {
			var after, copyDir string
			pending := func() bool {
				GinkgoHelper()
				led, err := e.Ledger(sc)
				Expect(err).NotTo(HaveOccurred())
				return led.Modules["example"].ScriptsPending
			}
			BeforeEach(func() {
				after, copyDir = script(filepath.Join(proj, "ci", "after.sh"), logger("project")), filepath.Join(proj, ".lola", "modules", "example")
				projectYML("post_install: ci/after.sh\n")
				_, err := install(engine.Options{})
				Expect(err).NotTo(HaveOccurred())
				Expect(pending()).To(BeTrue())
				Expect(lines()).To(BeNil())
				// what earlier scripts built in the copy, which a run alone must keep
				Expect(os.WriteFile(filepath.Join(copyDir, "built"), []byte("x"), 0o644)).To(Succeed())
			})

			DescribeTable("a pending project post script runs only with consent for its current digest",
				func(setup func() engine.SyncRequest, runs bool) {
					req := setup()
					req.Scope = sc
					res, err := e.Sync(ctx, req)
					Expect(err).NotTo(HaveOccurred())
					Expect(filepath.Join(copyDir, "built")).To(BeAnExistingFile(), "the copy is not replaced")
					if !runs {
						Expect(lines()).To(BeNil())
						Expect(pending()).To(BeTrue())
						notice := "pending post-install script for example → lola sync --project example"
						if req.Options.AskScript == nil {
							notice += " (off a terminal: lola sync --project example --trust=scripts)"
						}
						Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn, Text: notice}))
						return
					}
					Expect(who()).To(Equal([]string{"project project post-install"}))
					Expect(lines()[0]).To(HaveSuffix(" " + copyDir + " written=yes"))
					Expect(res.Scripts).To(ConsistOf(And(HaveField("Event", engine.PostInstall), HaveField("Source", engine.ScriptProject),
						HaveField("Path", "ci/after.sh"), HaveField("Module", "example"), HaveField("Target", "claude-code"), HaveField("Status", engine.HookRan))))
					Expect(res.InSync).To(BeFalse())
					Expect(pending()).To(BeFalse())
				},
				Entry("sync naming the module with --trust=scripts runs it alone in the existing copy", func() engine.SyncRequest {
					return engine.SyncRequest{Modules: []string{"example"}, Options: engine.Options{Trust: engine.TrustScripts}}
				}, true),
				Entry("sync naming it off a terminal without --trust=scripts keeps it pending with the notice", func() engine.SyncRequest {
					return engine.SyncRequest{Modules: []string{"example"}}
				}, false),
				Entry("sync naming it on a terminal, answered yes, runs it", func() engine.SyncRequest {
					return engine.SyncRequest{Modules: []string{"example"}, Options: engine.Options{AskScript: func(q engine.ScriptQuestion) (bool, error) {
						Expect(q).To(And(HaveField("Event", engine.PostInstall), HaveField("Path", "ci/after.sh"), HaveField("Later", false)))
						return true, nil
					}}}
				}, true),
				Entry("sync naming it on a terminal, answered no, keeps it pending with the notice", func() engine.SyncRequest {
					return engine.SyncRequest{Modules: []string{"example"}, Options: engine.Options{AskScript: func(engine.ScriptQuestion) (bool, error) { return false, nil }}}
				}, false),
				Entry("a recorded consent for the same digest runs it", func() engine.SyncRequest {
					real, err := filepath.EvalSymlinks(proj)
					Expect(err).NotTo(HaveOccurred())
					sum, err := os.ReadFile(after)
					Expect(err).NotTo(HaveOccurred())
					Expect(e.Consent.Grant(consent.ScriptKey(real, engine.PostInstall), "sha256:"+sha256Hex(sum))).To(Succeed())
					return engine.SyncRequest{Modules: []string{"example"}}
				}, true),
				Entry("a recorded consent for another digest does not", func() engine.SyncRequest {
					real, err := filepath.EvalSymlinks(proj)
					Expect(err).NotTo(HaveOccurred())
					Expect(e.Consent.Grant(consent.ScriptKey(real, engine.PostInstall), "sha256:"+sha256Hex([]byte("an older script")))).To(Succeed())
					return engine.SyncRequest{Modules: []string{"example"}}
				}, false),
			)

			It("a pending script that runs alone and fails stays pending", func() {
				script(after, "exit 1\n")
				res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"example"}, Options: engine.Options{Trust: engine.TrustScripts}})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Scripts).To(ConsistOf(HaveField("Status", engine.HookFailed)))
				Expect(pending()).To(BeTrue(), "the next sync naming it offers it again")
			})

			It("a dry run says what the run would do with a pending script", func() {
				res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"example"}, Options: engine.Options{DryRun: true, Trust: engine.TrustScripts}})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Scripts).To(ConsistOf(And(HaveField("Status", engine.HookWouldRun), HaveField("NeedsTrust()", false))))
				Expect(res.InSync).To(BeFalse())
				Expect(res.Changed).To(Equal([]string{"example"}))
				Expect(res.ToApply).To(Equal("lola sync --project example --trust=scripts"))

				// without the trust it needs, the run off a terminal keeps it pending, and says so
				res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"example"}, Options: engine.Options{DryRun: true}})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Scripts).To(ConsistOf(And(HaveField("Status", engine.HookWouldRun), HaveField("NeedsTrust()", true))))
				Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
					Text: "pending post-install script for example → lola sync --project example (off a terminal: lola sync --project example --trust=scripts)"}))
				Expect(res.InSync).To(BeTrue())
				Expect(res.Changed).To(BeEmpty())
				Expect(lines()).To(BeNil())
				Expect(pending()).To(BeTrue())
			})

			It("a pending script runs alone without reading your own scripts, which wrap nothing", func() {
				configure("pre_install: " + filepath.Join(tmp, "bin", "gone.sh") + "\n")
				_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"example"}, Options: engine.Options{Trust: engine.TrustScripts}})
				Expect(err).NotTo(HaveOccurred(), "no copy is written, so config.yaml's missing script wraps nothing")
				Expect(who()).To(Equal([]string{"project project post-install"}))
				Expect(pending()).To(BeFalse())
			})

			It("a pending script whose key project.yml no longer has is pending no more", func() {
				projectYML("targets: [claude-code]\nmodules:\n  - name: example\n    source: " + src + "\n")
				res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Modules: []string{"example"}, Options: engine.Options{Trust: engine.TrustEntries | engine.TrustScripts}})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("pending post-install script"))))
				Expect(pending()).To(BeFalse(), "a sync clears the mark")
				Expect(lines()).To(BeNil())
			})

			It("a sync that does not name the module keeps the script pending with its notice", func() {
				res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.InSync).To(BeTrue())
				Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
					Text: "pending post-install script for example → lola sync --project example (off a terminal: lola sync --project example --trust=scripts)"}))
				Expect(res.Notices).NotTo(ContainElement(HaveField("Text", ContainSubstring("is not trusted on this machine"))),
					"the pending notice already says how to trust it")
				Expect(lines()).To(BeNil())
				Expect(pending()).To(BeTrue())

				// on a terminal it names only the sync that asks, and asks nothing: the script would
				// not run (M3)
				res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{AskScript: func(engine.ScriptQuestion) (bool, error) {
					Fail("a sync that does not name the module asks nothing about its pending script")
					return false, nil
				}}})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn, Text: "pending post-install script for example → lola sync --project example"}))
				Expect(lines()).To(BeNil())
				Expect(pending()).To(BeTrue())
			})
		})

		It("a sync asks about the project's scripts when they change, even with nothing to install", func() {
			Expect(os.CopyFS(filepath.Join(proj, "mods", "example"), os.DirFS(src))).To(Succeed())
			projectYML("targets: [claude-code]\nmodules:\n  - {name: example, source: ./mods/example}\n")
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries}})
			Expect(err).NotTo(HaveOccurred())

			// a pull adds a pre-install script: the project is in sync, but its script is not trusted
			script(filepath.Join(proj, "ci", "check.sh"), logger("project"))
			projectYML("targets: [claude-code]\npre_install: ci/check.sh\nmodules:\n  - {name: example, source: ./mods/example}\n")
			req := engine.SyncRequest{Scope: sc, Display: []string{"--json"}, Options: engine.Options{Run: "lola sync", Display: []string{"--json"}}}
			_, err = e.Sync(ctx, req)
			Expect(err).To(MatchError("lola sync needs your trust for the pre-install script ci/check.sh of this project (new), and there is no terminal to ask on"))
			Expect(errs.Hints(err)).To(HaveExactElements("review it: lola info --project example --json", "then consent to it: lola sync --project --trust=scripts --json"))
			dry, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(dry.OffTerminal).To(Equal("lola sync --project --trust=scripts"))

			req.Options.Trust = engine.TrustScripts
			res, err := e.Sync(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.InSync).To(BeTrue())
			Expect(res.Trusted).To(Equal([]engine.Consent{{Kind: "scripts", Item: "pre-install ci/check.sh", Change: engine.ConsentNew}}))
			Expect(lines()).To(BeNil(), "nothing installs, so nothing runs")
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc})
			Expect(err).NotTo(HaveOccurred(), "the consent is recorded")

			// an edit asks again, on a terminal too
			script(filepath.Join(proj, "ci", "check.sh"), logger("project")+"true\n")
			var asked []engine.ScriptQuestion
			ask := engine.Options{AskScript: func(q engine.ScriptQuestion) (bool, error) { asked = append(asked, q); return true, nil }}
			_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: ask})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveLen(1))
			Expect(asked[0]).To(And(HaveField("Event", engine.PreInstall), HaveField("Path", "ci/check.sh"), HaveField("Root", proj),
				HaveField("Later", true)), "asked as trust for later runs: nothing runs now")
			Expect(lines()).To(BeNil())

			// a post-install script without trust is only warned of, on a terminal too: nothing is
			// installed for it to skip, so a question would answer nothing (M3)
			script(filepath.Join(proj, "ci", "after.sh"), logger("project"))
			projectYML("targets: [claude-code]\npost_install: ci/after.sh\nmodules:\n  - {name: example, source: ./mods/example}\n")
			res, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: ask})
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveLen(1))
			Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
				Text: "post-install script ci/after.sh (project) is not trusted on this machine; trust it: lola sync --project --trust=scripts"}))
		})

		It("a sync that writes no copy checks only its project's scripts, never your own", func() {
			Expect(os.CopyFS(filepath.Join(proj, "mods", "example"), os.DirFS(src))).To(Succeed())
			projectYML("targets: [claude-code]\nmodules:\n  - {name: example, source: ./mods/example}\n")
			_, err := e.Sync(ctx, engine.SyncRequest{Scope: sc, Options: engine.Options{Trust: engine.TrustEntries}})
			Expect(err).NotTo(HaveOccurred())
			configure("pre_install: " + filepath.Join(tmp, "bin", "gone.sh") + "\n")
			res, err := e.Sync(ctx, engine.SyncRequest{Scope: sc})
			Expect(err).NotTo(HaveOccurred(), "your script wraps nothing here")
			Expect(res.InSync).To(BeTrue())
		})

		It("an edited project script asks again", func() {
			script(filepath.Join(proj, "ci", "check.sh"), logger("project"))
			projectYML("pre_install: ci/check.sh\n") // targets the install records, which the trust record then covers
			var asked []string
			opts := engine.Options{AskScript: func(q engine.ScriptQuestion) (bool, error) {
				asked = append(asked, q.Event+" "+q.Path+" "+q.SHA256)
				return true, nil
			}}
			_, err := install(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveLen(1))
			Expect(asked[0]).To(MatchRegexp(`^pre-install ci/check.sh [0-9a-f]{64}$`))
			Expect(os.RemoveAll(filepath.Join(proj, ".lola", "modules", "example"))).To(Succeed())
			_, err = e.Render(ctx, sc, opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveLen(1), "consent is recorded for the project, event and digest")

			script(filepath.Join(proj, "ci", "check.sh"), logger("project")+"true\n")
			Expect(os.RemoveAll(filepath.Join(proj, ".lola", "modules", "example"))).To(Succeed())
			_, err = e.Render(ctx, sc, opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveLen(2), "an edit asks again")

			// consent in one project never covers another's script of the same bytes
			other := filepath.Join(tmp, "other")
			Expect(os.MkdirAll(filepath.Join(other, ".lola"), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(other, ".lola", "project.yml"), []byte("version: 1\npre_install: ci/check.sh\n"), 0o644)).To(Succeed())
			script(filepath.Join(other, "ci", "check.sh"), logger("project")+"true\n")
			opts.Targets = []string{"claude-code"}
			_, err = e.Install(ctx, engine.Scope{Kind: target.Project, ProjectDir: other}, other, []engine.InstallRequest{{Arg: src}}, opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(asked).To(HaveLen(3))
			ok, err := e.Consent.Recorded(consent.ScriptKey(other, engine.PreInstall))
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
		})

		DescribeTable("a script changed between planning and its run is not run",
			func(setup func() engine.Options, wantErr, wantNotice string) {
				opts := setup()
				res, err := install(opts)
				if wantErr != "" {
					Expect(err).To(MatchError(wantErr))
					Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory(), "nothing was written")
					Expect(who()).To(Equal([]string{"user user pre-install"}))
					return
				}
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn, Text: wantNotice}))
				Expect(filepath.Join(proj, ".claude", "commands", "ship.md")).To(BeAnExistingFile(), "the installation was kept")
				Expect(who()).To(Equal([]string{"hook - post-install"}))
			},
			Entry("a user pre script rewrites the project pre script", func() engine.Options {
				check := script(filepath.Join(proj, "ci", "check.sh"), logger("project"))
				configure("pre_install: " + script(filepath.Join(tmp, "bin", "pre.sh"), logger("user")+"echo 'echo pwned' >> "+check+"\n") + "\n")
				projectYML("targets: [claude-code]\npre_install: ci/check.sh\n")
				return engine.Options{Trust: engine.TrustScripts}
			}, "pre-install script ci/check.sh (project) changed after lola checked it; nothing was installed", ""),
			Entry("a module post hook rewrites the flag post script", func() engine.Options {
				flag := filepath.Join(tmp, "flag", "post.sh")
				writeTree(src, map[string]string{"lola.yml": "name: example\nhooks: {post-install: hooks/post.sh}\n",
					"hooks/post.sh": "#!/bin/sh\n" + logger("hook") + "echo 'echo pwned' >> " + flag + "\n"})
				Expect(os.Chmod(filepath.Join(src, "hooks/post.sh"), 0o755)).To(Succeed())
				return engine.Options{Trust: engine.TrustHooks, PostInstall: engine.FlagScript{Given: "flag/post.sh", Path: script(flag, logger("flag"))}}
			}, "", "post-install script flag/post.sh (flag) changed after lola checked it; the installation was kept"),
		)

		It("script statuses are would-run, ran, failed and declined", func() {
			configure("pre_install: " + script(filepath.Join(tmp, "bin", "pre.sh"), "exit 0\n") + "\npost_install: " +
				script(filepath.Join(tmp, "bin", "post.sh"), "exit 1\n") + "\n")
			script(filepath.Join(proj, "ci", "after.sh"), "exit 0\n")
			projectYML("targets: [claude-code]\npost_install: ci/after.sh\n")
			status := func(res *engine.Result) []string {
				var out []string
				for _, s := range res.Scripts {
					out = append(out, s.Event+" "+s.Source+" "+s.Status)
				}
				return out
			}
			res, err := install(engine.Options{DryRun: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(status(res)).To(Equal([]string{"pre-install user would-run", "post-install project would-run", "post-install user would-run"}))
			res, err = install(engine.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(status(res)).To(Equal([]string{"pre-install user ran", "post-install project declined", "post-install user failed"}), "in run order")
		})

		DescribeTable("script paths reach human and JSON output through untrusted.Text",
			func(name string, setup func(name string) engine.Options, shownPath func(name string) string) {
				res, err := install(setup(name))
				shownName := strings.NewReplacer("\x1b", "?", "\n", "?").Replace(name)
				if shownPath == nil { // a project's is refused at plan time (A27), its path cleaned on one line
					Expect(err).To(MatchError(ContainSubstring("pre_install: ci/" + shownName + " holds a control character")))
					return
				}
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Scripts).To(HaveLen(1))
				Expect(res.Scripts[0].Path).To(Equal(shownPath(shownName)), "on one line, with no escape")
			},
			Entry("user", "pre\x1b[31m.sh", func(name string) engine.Options {
				configure("pre_install: " + strconv.Quote(filepath.Join(tmp, "bin", name)) + "\n")
				script(filepath.Join(tmp, "bin", name), "exit 0\n")
				return engine.Options{}
			}, func(n string) string { return filepath.Join(tmp, "bin", n) }),
			Entry("user, a line break", "pre\nfake.sh", func(name string) engine.Options {
				configure("pre_install: " + strconv.Quote(filepath.Join(tmp, "bin", name)) + "\n")
				script(filepath.Join(tmp, "bin", name), "exit 0\n")
				return engine.Options{}
			}, func(n string) string { return filepath.Join(tmp, "bin", n) }),
			Entry("project", "pre\x1b[31m.sh", func(name string) engine.Options {
				projectYML("targets: [claude-code]\npre_install: " + strconv.Quote("ci/"+name) + "\n")
				script(filepath.Join(proj, "ci", name), "exit 0\n")
				return engine.Options{Trust: engine.TrustScripts}
			}, nil),
			Entry("flag", "pre\x1b[31m.sh", func(name string) engine.Options {
				return engine.Options{PreInstall: engine.FlagScript{Given: "flag/" + name, Path: script(filepath.Join(tmp, "flag", name), "exit 0\n")}}
			}, func(n string) string { return "flag/" + n }),
			Entry("flag, a line break", "pre\nfake.sh", func(name string) engine.Options {
				return engine.Options{PreInstall: engine.FlagScript{Given: "flag/" + name, Path: script(filepath.Join(tmp, "flag", name), "exit 0\n")}}
			}, func(n string) string { return "flag/" + n }),
		)
	})
})

// sha256Hex is the hex sha256 of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
