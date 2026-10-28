package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("lola migrate", func() {
	var tmp, home, proj, src string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		home, proj, src = filepath.Join(tmp, "home", ".lola"), filepath.Join(tmp, "proj"), filepath.Join(tmp, "src", "review")
		writeTree(src, map[string]string{"commands/ship.md": "Ship $ARGUMENTS\n", "skills/review/SKILL.md": "---\ndescription: Review\n---\nReview.\n"})
		writeTree(home, map[string]string{
			"installed.yml": "version: '1.0'\ninstallations:\n- module: review\n  assistant: claude-code\n  scope: project\n  project_path: " + proj +
				"\n  skills: [review, review_review]\n  commands: [ship]\n  agents: []\n  mcps: []\n  has_instructions: false\n",
			"modules/review/.lola/source.yml": "source: " + src + "\ntype: folder\n",
		})
		writeTree(proj, map[string]string{
			".claude/commands/ship.md":              "python ship\n",
			".claude/skills/review/SKILL.md":        "python skill\n",
			".claude/skills/review_review/SKILL.md": "python renamed skill\n",
		})
	})
	trash := func() string { return filepath.Join(tmp, "state", "lola", "trash") }

	It("prints the plan on --dry-run and writes nothing", func() {
		r := lola("migrate", "--dry-run")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("plan (dry run; nothing was written):"))
		Expect(r.out).To(ContainSubstring("project " + proj + ":"))
		Expect(r.out).To(ContainSubstring("install review (claude-code) from " + src))
		Expect(r.out).To(ContainSubstring("replace " + filepath.Join(proj, ".claude/commands/ship.md") + " (backed up to the trash)"))
		Expect(r.out).To(ContainSubstring("move    " + filepath.Join(proj, ".claude/skills/review_review") + " to the trash"))
		Expect(r.out).To(ContainSubstring("the Python lola's home " + home + " moves to the trash"))
		Expect(read(filepath.Join(proj, ".claude/commands/ship.md"))).To(Equal("python ship\n"))
		Expect(trash()).NotTo(BeADirectory())
		Expect(home).To(BeADirectory())
	})

	It("needs -y off a terminal, printing the plan and writing nothing", func() {
		r := lola("migrate")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("lola migrate needs confirmation, and there is no terminal to ask on"))
		Expect(r.err).To(ContainSubstring("hint: lola migrate -y"))
		Expect(r.err).To(ContainSubstring("install review (claude-code)"))
		Expect(read(filepath.Join(proj, ".claude/commands/ship.md"))).To(Equal("python ship\n"))
		Expect(trash()).NotTo(BeADirectory())
	})

	It("migrates with -y and prints what it did; a second run has nothing to migrate", func() {
		r := lola("migrate", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`replace .*\.claude/commands/ship\.md \(backed up to .*/state/lola/trash/\d{8}T\d{6}Z/`))
		Expect(r.out).To(ContainSubstring("✓ migrated project " + proj))
		Expect(r.out).To(ContainSubstring("✓ the Python lola's home " + home + " moved to "))
		Expect(r.out).To(MatchRegexp(`trash: .*/state/lola/trash/\d{8}T\d{6}Z`))
		Expect(read(filepath.Join(proj, ".claude/commands/ship.md"))).To(Equal("Ship $ARGUMENTS\n"))
		Expect(filepath.Join(proj, ".claude/skills/review_review")).NotTo(BeAnExistingFile())
		Expect(lola("list", "-C", proj).out).To(MatchRegexp(`review\s+claude-code\s+✓ ok`))

		r = lola("migrate", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("nothing to migrate"))
	})

	It("--force answers the Migrate? question off a terminal, and never the hooks' consent", func() {
		mark := filepath.Join(tmp, "hook-ran")
		writeTree(src, map[string]string{"lola.yml": "name: review\nhooks: {pre-install: pre.sh}\n", "pre.sh": "#!/bin/sh\ntouch '" + mark + "'\n"})
		Expect(os.Chmod(filepath.Join(src, "pre.sh"), 0o755)).To(Succeed())
		r := lola("migrate", "--force")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("review has a pre-install hook, and lola runs hooks only with your consent"))
		Expect(r.err).To(ContainSubstring("  hint: then consent to it: lola migrate --force --trust=hooks\n"), "the hint is the command refused")
		Expect(r.err).NotTo(ContainSubstring("needs confirmation"), "--force answered Migrate? (ruling R-6c3-4)")
		Expect(read(filepath.Join(proj, ".claude/commands/ship.md"))).To(Equal("python ship\n"), "nothing was written")
		Expect(mark).NotTo(BeAnExistingFile())

		r = lola("migrate", "--force", "--trust=hooks")
		Expect(r.code).To(Equal(0), r.err)
		Expect(mark).To(BeAnExistingFile())
		Expect(r.out).To(ContainSubstring("✓ migrated project " + proj))
	})

	It("says where nothing migrates and counts the records, in human, plain and JSON output (Plan 10 §3.3)", func() {
		writeTree(home, map[string]string{"installed.yml": "version: '1.0'\ninstallations:\n- module: review\n  assistant: claude-code\n  scope: project\n  project_path: " + proj +
			"\n  skills: [review, review_review]\n  commands: [ship]\n- module: review\n  assistant: cursor\n  scope: user\n"})
		r := lola("migrate", "--dry-run")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("⚠ nothing to migrate at user scope: 1 record(s) left as they are\n"))
		Expect(r.out).To(ContainSubstring("records: 1 to migrate, 1 left as they are\n"))

		r = lola("migrate", "--dry-run", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("status\tuser\tnothing\t\n"))
		Expect(r.out).To(ContainSubstring("summary\t1\t0\t1\t0\n"))

		r = lola("migrate", "-y", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var v struct {
			Destinations []struct {
				Status string `json:"status"`
			} `json:"destinations"`
			Summary map[string]int `json:"summary"`
		}
		Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed())
		Expect(v.Destinations).To(HaveLen(2))
		Expect(v.Destinations[1].Status).To(Equal("nothing"))
		Expect(v.Summary).To(Equal(map[string]int{"migrated": 1, "already": 0, "left": 1, "failed": 0}))

		r = lola("migrate", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("records: 0 to migrate, 1 already migrated, 1 left as they are\n"))
	})

	Context("from another machine (Plan 10 §3.1, §3.2)", func() {
		var moved string
		BeforeEach(func() {
			moved = filepath.Join(tmp, "home", "proj")
			writeTree(moved, map[string]string{".claude/commands/ship.md": "python ship\n"})
			writeTree(filepath.Join(tmp, "home", "src", "kit"), map[string]string{"skills/lint/SKILL.md": "---\ndescription: Lint\n---\nLint.\n"})
			fakeAssistant(tmp, "opencode", "1.0.0")
			writeTree(home, map[string]string{"installed.yml": "version: '1.0'\ninstallations:\n" +
				"- {module: review, assistant: claude-code, scope: project, project_path: /Users/alice/proj, commands: [ship]}\n" +
				"- {module: review, assistant: claude-code, scope: user, commands: [ship]}\n" +
				"- {module: kit, assistant: claude-code, scope: user, skills: [lint]}\n",
				"modules/kit/.lola/source.yml": "source: /Users/alice/src/kit\ntype: folder\n"})
		})

		It("asks where each missing root is and which assistants stand in for each missing one, then migrates with the answers", func() {
			p := &scripted{roots: []string{filepath.Join(tmp, "home")}, mapTargets: [][]string{{"opencode"}}, confirm: []bool{true}}
			r := lolaP(p, "migrate")
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.calls).To(Equal([]string{"migrate-root:/Users/alice", "migrate-targets:claude-code", "confirm:Migrate?"}))
			Expect(p.confirmNavs).To(Equal([]cli.Nav{{Answered: 2}}), "two mapping answers: q at Migrate? asks first")
			Expect(p.rootSteps).To(Equal([]cli.MigrateRootStep{{Old: "/Users/alice", Suggest: filepath.Join(tmp, "home"), Value: filepath.Join(tmp, "home"),
				Records: 2}}), "one question covers the project path and kit's folder source")
			Expect(p.mapTargetSteps).To(Equal([]cli.MigrateTargetStep{{Old: "claude-code", Supported: true, Modules: 2, Options: []string{"opencode"},
				Nav: cli.Nav{Back: true, Answered: 1}}}))
			Expect(r.out).To(ContainSubstring("✓ migrated project " + moved))
			Expect(r.out).To(ContainSubstring("✓ migrated user scope"))
			Expect(r.out).NotTo(ContainSubstring("map what this machine lacks"), "nothing is left to map")
			Expect(lola("list", "-C", moved).out).To(MatchRegexp(`review\s+claude-code\s+✓ ok`))
			Expect(lola("list", "--user").out).To(MatchRegexp(`review\s+opencode\s+✓ ok`))
			Expect(lola("list", "--user").out).To(MatchRegexp(`kit\s+opencode\s+✓ ok`))
		})

		It("goes back from an assistant question to the root question on Esc, the root answered pre-filled", func() {
			p := &scripted{roots: []string{"", filepath.Join(tmp, "home")}, mapTargets: [][]string{{goBack}, {"opencode"}}, confirm: []bool{false}}
			r := lolaP(p, "migrate")
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.calls).To(Equal([]string{"migrate-root:/Users/alice", "migrate-targets:claude-code", "migrate-root:/Users/alice",
				"migrate-targets:claude-code", "confirm:Migrate?"}))
			Expect(p.rootSteps[0].Nav).To(Equal(cli.Nav{}), "the first question has no screen before it")
			Expect(p.rootSteps[1].Value).To(BeEmpty(), "the root was skipped, and is offered as answered")
			Expect(p.mapTargetSteps[0].Options).To(Equal([]string{"opencode"}))
			Expect(r.err).To(ContainSubstring("project "+moved+":\n  install review (claude-code)"), "the second answer maps the root")
		})

		It("asks nothing off a terminal, and ends with the run plus the flags that map what it left", func() {
			r := lola("migrate", "-n")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("unmappable review (claude-code at /Users/alice/proj): project_path /Users/alice/proj no longer exists\n"))
			Expect(r.out).To(ContainSubstring("  unmappable review (claude-code): claude-code is not installed here\n"))
			Expect(r.out).To(HaveSuffix("→ map what this machine lacks: lola migrate --dry-run --map-root /Users/alice=" + filepath.Join(tmp, "home") +
				" --map-target claude-code=opencode\n"))

			r = lola("migrate", "-y", "--json", "--map-target", "claude-code=opencode")
			Expect(r.code).To(Equal(0), r.err)
			var v struct {
				Remap string `json:"remap"`
			}
			Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed())
			Expect(v.Remap).To(Equal("lola migrate --json --map-target claude-code=opencode --map-root /Users/alice="+filepath.Join(tmp, "home")),
				"the run's own map is kept, and -y is not")
		})

		It("sync works right after migrate", func() {
			r := lola("migrate", "-y", "--map-root", "/Users/alice="+filepath.Join(tmp, "home"), "--map-target", "claude-code=opencode")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("records: 3 migrated, 0 left as they are\n"))
			r = lola("sync", "--user")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.err).NotTo(ContainSubstring("no user.yml"))
			r = lola("sync", "-C", moved)
			Expect(r.code).To(Equal(0), r.err)
		})

		It("reruns with the same maps, warning that they match nothing", func() {
			maps := []string{"--map-root", "/Users/alice=" + filepath.Join(tmp, "home"), "--map-target", "claude-code=opencode"}
			r := lola(append([]string{"migrate", "-y"}, maps...)...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).NotTo(ContainSubstring("matches no"))

			r = lola(append([]string{"migrate", "-y"}, maps...)...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("⚠ --map-root /Users/alice=" + filepath.Join(tmp, "home") +
				" matches no missing project path or source of the Python lola; it changes nothing\n"))
			Expect(r.out).To(ContainSubstring("⚠ --map-target claude-code=opencode matches no record whose assistant is missing here; it changes nothing\n"))

			r = lola(append([]string{"migrate", "-y", "--json"}, maps...)...)
			Expect(r.code).To(Equal(0), r.err)
			var v struct {
				Warnings []string `json:"warnings"`
			}
			Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed())
			Expect(v.Warnings).To(ConsistOf(HavePrefix("--map-root /Users/alice="), HavePrefix("--map-target claude-code=opencode ")))
		})

		It("never offers a root --map-root would refuse, such as a project at a drive root", func() {
			writeTree(home, map[string]string{"installed.yml": "version: '1.0'\ninstallations:\n" +
				"- {module: review, assistant: opencode, scope: project, project_path: 'E:\\', commands: [ship]}\n"})
			r := lola("migrate", "-n")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring(`unmappable review (opencode at E:\)`))
			Expect(r.out).NotTo(ContainSubstring("map what this machine lacks"))
			Expect(lola("migrate", "-n", "--json").out).To(ContainSubstring(`"remap": ""`))
		})
	})

	It("refuses a --map-root or --map-target it cannot read, saying what is wrong", func() {
		r := lola("migrate", "--map-root", "Users/alice=/home/alice")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: --map-root \"Users/alice=/home/alice\": \"Users/alice\" is not an absolute path\n"))
		r = lola("migrate", "--map-target", "claude-code=vim")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: --map-target \"claude-code=vim\": \"vim\" is not an assistant lola installs for; write claude-code, gemini-cli, opencode\n"))
	})

	It("lists what it keeps and why when nothing is left to migrate", func() {
		writeTree(home, map[string]string{"installed.yml": "version: '1.0'\ninstallations:\n- module: review\n  assistant: cursor\n  project_path: " + proj + "\n"})
		r := lola("migrate")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("nothing to migrate"))
		Expect(r.out).To(ContainSubstring("unmappable review (cursor): lola-go has no target for cursor"))
		Expect(r.out).To(ContainSubstring("the Python lola's home " + home + " stays: 1 record(s) lola-go cannot migrate are still in installed.yml"))
	})

	It("prints each hint for a record it cannot migrate on a line of its own, and as a hints array in JSON", func() {
		writeTree(home, map[string]string{"installed.yml": "version: '1.0'\ninstallations:\n- module: review\n  assistant: claude-code\n  project_path: " + proj +
			"\n  agents: [gone]\n"})
		r := lola("migrate", "--dry-run")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("holds no agent gone, which the Python lola installed\n" +
			"      hint: if the module is in another directory of the source, install it with " + errs.ShellJoin([]string{"lola", "install", src, "--path"}) +
			" <dir>, then rerun lola migrate\n" +
			"      hint: if the source no longer ships it,"))

		r = lola("migrate", "--dry-run", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var v struct {
			Destinations []struct {
				Unmappable []struct {
					Hints []string `json:"hints"`
				} `json:"unmappable"`
			} `json:"destinations"`
		}
		Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed())
		Expect(v.Destinations[0].Unmappable[0].Hints).To(HaveLen(2))
	})

	It("keeps what lola-go did not replace with --keep-python", func() {
		r := lola("migrate", "-y", "--keep-python")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("keep    " + filepath.Join(proj, ".claude/skills/review_review") + ": --keep-python keeps what lola-go did not replace"))
		Expect(filepath.Join(proj, ".claude/skills/review_review")).To(BeAnExistingFile())
		Expect(home).To(BeADirectory())
	})

	It("registers the Python lola's marketplaces, and every hint it prints for one it cannot runs", func() {
		work := filepath.Join(tmp, "plugins-work")
		writeTree(work, map[string]string{"catalog.yml": "name: acme\nmodules:\n  - name: tool\n    repository: https://example.com/tool.git\n"})
		for _, args := range [][]string{{"-C", work, "init", "-q", "-b", "main"}, {"-C", work, "add", "-A"}, {"-C", work, "commit", "-qm", "catalog"},
			{"clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "plugins.git")}} {
			out, err := exec.Command("git", args...).CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), string(out))
		}
		plugins := testenv.ServeGit(filepath.Join(tmp, "srv")) + "/plugins.git"
		writeTree(filepath.Join(home, "market"), map[string]string{
			"acme.yml":    "enabled: true\nname: acme\nurl: git+" + plugins + "\n",
			"off.yml":     "enabled: false\nname: off\nurl: git+" + plugins + "#catalog.yml\n",
			"general.yml": "enabled: true\nname: Lola General\nurl: git+" + plugins + "\n",
			"plain.yml":   "enabled: true\nname: plain\nurl: http://example.com/m.yml\n",
		})

		r := lola("migrate", "--dry-run", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var doc struct {
			Markets []struct {
				Name, Status string
				Hints        []string
			} `json:"markets"`
		}
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		Expect(doc.Markets).To(HaveLen(4))
		for _, m := range doc.Markets {
			for _, h := range m.Hints {
				Expect(shellWords(h)).To(ContainElement("--json"), "a hint keeps the run's output flags: "+h)
			}
		}

		r = lola("migrate", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("marketplaces:\n  ✓ registered acme from "+plugins+" path catalog.yml\n"),
			"its catalog is the only .yml at the root, which the Python lola read and lola's own lookup would not find")
		Expect(r.out).To(ContainSubstring("  not registered: off (git+" + plugins + "): it is disabled in the Python lola\n" +
			"      hint: lola market off --add " + plugins + " --path catalog.yml\n"))
		Expect(r.out).To(ContainSubstring("  not registered: plain (http://example.com/m.yml): it uses plain http://"))
		Expect(r.out).To(ContainSubstring("the Python lola's home " + home + " stays: 2 marketplaces lola did not register are still in " +
			filepath.Join(home, "market")))
		var hints []string
		for _, line := range strings.Split(r.out, "\n") {
			if h, ok := strings.CutPrefix(strings.TrimSpace(line), "hint: lola market"); ok {
				hints = append(hints, "lola market"+h)
			}
		}
		Expect(hints).To(Equal([]string{"lola market lola-general --add " + plugins + " --path catalog.yml", "lola market off --add " + plugins + " --path catalog.yml"}))
		for _, h := range hints {
			again := lola(shellWords(h)[1:]...)
			Expect(again.code).To(Equal(0), h+": "+again.err)
		}
		r = lola("market", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(And(ContainSubstring("acme\t"), ContainSubstring("lola-general\t"), ContainSubstring("off\t")))
	})

	It("never prints a Python marketplace url's credentials or query, in any output", func() {
		writeTree(filepath.Join(home, "market"), map[string]string{
			"user.yml":  "enabled: true\nname: user\nurl: 'git+https://user:" + "SECRET1@example.com/r.git'\n",
			"query.yml": "enabled: true\nname: query\nurl: 'https://example.com/m.yml?token=SECRET2'\n",
			"off.yml":   "enabled: false\nname: off\nurl: 'https://x:" + "SECRET3@example.com/r.git#m.yml'\n",
			// scp-looking text with an @ after the first colon: git would read user or u as the host.
			"scp.yml":  "enabled: true\nname: scp\nurl: 'user:" + "SECRET4@example.com/r.git'\n",
			"scp2.yml": "enabled: true\nname: scp2\nurl: 'u:p@" + "SECRET5@example.com:r.git'\n",
			"ok.yml":   "enabled: true\nname: ok\nurl: 'file://" + filepath.Join(tmp, "folder", "market.yml") + "'\n",
		})
		writeTree(filepath.Join(tmp, "folder"), map[string]string{"market.yml": "name: ok\nmodules: []\n"})
		for _, args := range [][]string{{"migrate", "--dry-run"}, {"migrate", "--dry-run", "--json"}, {"migrate", "--dry-run", "--plain"},
			{"migrate", "-y", "--json"}, {"migrate", "-y"}, {"migrate", "-y", "--plain"}} {
			r := lola(args...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out+r.err).NotTo(ContainSubstring("SECRET"), "%v", args)
			Expect(r.out+r.err).To(ContainSubstring("example.com"), "%v: the redacted url is shown", args)
		}
		Expect(lola("market").out).NotTo(ContainSubstring("example.com"), "none was registered")
		registry, err := os.ReadFile(filepath.Join(tmp, "config", "lola", "markets.yaml"))
		Expect(err).NotTo(HaveOccurred(), "ok was registered")
		Expect(string(registry)).To(ContainSubstring("name: ok"))
		Expect(string(registry)).NotTo(ContainSubstring("SECRET"))
		Expect(string(registry)).NotTo(ContainSubstring("example.com"))
	})

	It("emits snake_case JSON whose arrays are never null", func() {
		for _, args := range [][]string{{"migrate", "--dry-run", "--json"}, {"migrate", "-y", "--json"}, {"migrate", "-y", "--json"}} {
			r := lola(args...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).NotTo(ContainSubstring("null"))
			var v map[string]any
			Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed())
			Expect(v).To(HaveKey("destinations"))
			Expect(v).To(HaveKey("nothing_to_migrate"))
			Expect(v).To(HaveKey("python_home"))
		}
	})

	It("prints tab-separated lines with --plain", func() {
		r := lola("migrate", "--dry-run", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("install\t" + proj + "\treview\tclaude-code\t" + src + "\n"))
		Expect(r.out).To(ContainSubstring("move\t" + proj + "\t" + filepath.Join(proj, ".claude/skills/review_review") + "\t\t\n"))
		Expect(r.out).To(ContainSubstring("home\tmove\t" + home + "\t\t\n"))
		Expect(r.out).NotTo(ContainSubstring("✓"))
	})

	It("refuses a malformed installed.yml with a hint, writing nothing", func() {
		writeTree(home, map[string]string{"installed.yml": "installations:\n- module: ../x\n  assistant: claude-code\n"})
		r := lola("migrate", "-y")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring(`"../x" is not a single path component`))
		Expect(r.err).To(ContainSubstring("hint: fix the file by hand"))
		Expect(trash()).NotTo(BeADirectory())
	})

	It("refuses a conflict outside the recorded paths with the --force hint", func() {
		writeTree(proj, map[string]string{".claude/skills/review/notes.md": "x"})
		writeTree(src, map[string]string{"agents/critic.md": "---\ndescription: critic\n---\nC.\n"})
		writeTree(proj, map[string]string{".claude/agents/critic.md": "the user's own\n"})
		r := lola("migrate", "-y")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring(".claude/agents/critic.md: exists and was not written by lola"))
		Expect(r.err).To(HaveSuffix("  hint: lola migrate --force\n"), "no -y: --force answers Migrate? too (ruling R-6c3-4)")
		Expect(trash()).NotTo(BeADirectory())
		r = lola("migrate", "--force")
		Expect(r.code).To(Equal(0), r.err)
	})

	It("refuses a relative LOLA_HOME", func() {
		GinkgoT().Setenv("LOLA_HOME", "lola")
		r := lola("migrate", "--dry-run")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("LOLA_HOME must be an absolute path"))
	})

	It("is listed with the additional commands, with its flags in help", func() {
		Expect(lola("help").out).To(MatchRegexp(`(?s)Additional:.*migrate`))
		out := lola("migrate", "--help").out
		for _, f := range []string{"--dry-run", "--yes", "--force", "--trust", "--keep-python"} {
			Expect(out).To(ContainSubstring(f))
		}
	})
})

func read(p string) string {
	b, err := os.ReadFile(p)
	Expect(err).NotTo(HaveOccurred())
	return string(b)
}
