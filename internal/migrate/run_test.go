package migrate_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/migrate"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// snapshot hashes every file and link below each root, so a spec can show nothing changed.
func snapshot(roots ...string) map[string]string {
	out := map[string]string{}
	for _, root := range roots {
		r, err := os.OpenRoot(root)
		if os.IsNotExist(err) {
			continue
		}
		Expect(err).NotTo(HaveOccurred())
		Expect(fs.WalkDir(r.FS(), ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if d.Type()&fs.ModeSymlink != 0 {
				t, err := r.Readlink(p)
				out[filepath.Join(root, p)] = "link:" + t
				return err
			}
			b, err := r.ReadFile(p)
			sum := sha256.Sum256(b)
			out[filepath.Join(root, p)] = hex.EncodeToString(sum[:])
			return err
		})).To(Succeed())
		Expect(r.Close()).To(Succeed())
	}
	return out
}

func read(p string) string {
	b, err := os.ReadFile(p)
	Expect(err).NotTo(HaveOccurred())
	return string(b)
}

// git runs git in dir with the spec's isolated config.
func git(dir string, args ...string) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))
}

var _ = Describe("Migrate", func() {
	var (
		tmp, userHome, proj, src string
		h                        pyHome
		e                        *engine.Engine
		ctx                      = context.Background()
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		userHome = filepath.Join(tmp, "home")
		h = pyHome{dir: filepath.Join(userHome, ".lola")}
		proj, src = filepath.Join(tmp, "proj"), filepath.Join(tmp, "src", "review")
		writeTree(src, reviewModule)
		h.source("review", map[string]string{"source": src, "type": "folder"})
		e = newEngine()
	})
	// python writes what the Python lola's claude-code target leaves in root for review.
	python := func(root string) {
		writeTree(root, map[string]string{
			".claude/skills/review/SKILL.md":              "python skill\n",
			".claude/skills/review/ref/checks.md":         "checks\n",
			".claude/skills/review/old.md":                "a file the module no longer has\n",
			".claude/commands/ship.md":                    "python ship\n",
			".claude/commands/ship/notes.txt":             "python sidecar\n",
			".claude/agents/critic.md":                    "python critic\n",
			".claude/skills/review_review/SKILL.md":       "a skill the Python lola renamed\n",
			".mcp.json":                                   `{"mcpServers":{"lola-gh":{"command":"gh-mcp","cwd":"` + root + `/.lola/modules/review"}}}` + "\n",
			".lola/modules/review/skills/review/SKILL.md": "python copy\n",
			"CLAUDE.md": "<!-- lola:instructions:start -->\n<!-- lola:module:review:start -->\nBe kind.\n<!-- lola:module:review:end -->\n<!-- lola:instructions:end -->\n",
		})
	}
	record := func(root string) rec {
		return rec{"module": "review", "assistant": "claude-code", "project_path": root, "skills": []string{"review", "review_review"},
			"commands": []string{"ship"}, "agents": []string{"critic"}, "mcps": []string{"lola-gh"}, "has_instructions": true}
	}
	plan := func() *migrate.Plan {
		st, err := migrate.Load(h.dir, userHome)
		Expect(err).NotTo(HaveOccurred())
		p, err := migrate.NewPlan(ctx, e, st)
		Expect(err).NotTo(HaveOccurred())
		return p
	}
	run := func(opts migrate.Options) (*migrate.Result, error) {
		return migrate.Migrate(ctx, e, plan(), opts)
	}
	trashOf := func(res *migrate.Result) string {
		return filepath.Join(res.Trash, res.Destinations[0].ID)
	}

	It("installs, replaces with backup, moves the rest to the trash, and moves the Python home last", func() {
		python(proj)
		h.install(record(proj))
		res, err := run(migrate.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.DryRun).To(BeFalse())
		d := res.Destinations[0]
		Expect(d.Status).To(Equal(migrate.StatusMigrated))
		Expect(d.Installs).To(Equal([]migrate.InstallItem{{Module: "review", PythonModules: []string{"review"}, Source: src,
			Targets: []string{"claude-code"}, Status: migrate.InstallNew}}))

		Expect(read(filepath.Join(proj, ".claude/commands/ship.md"))).To(ContainSubstring("Ship $ARGUMENTS"))
		Expect(read(filepath.Join(proj, ".claude/skills/review/SKILL.md"))).To(ContainSubstring("Review it."))
		Expect(read(filepath.Join(proj, ".mcp.json"))).To(ContainSubstring(`"--stdio"`))
		led, err := e.Ledger(engine.Scope{Kind: target.Project, ProjectDir: proj})
		Expect(err).NotTo(HaveOccurred())
		var owned []string
		for _, a := range led.Modules["review"].Artifacts {
			owned = append(owned, a.Path+a.Key)
		}
		Expect(owned).To(ContainElements(".claude/commands/ship.md", ".claude/commands/ship/notes.txt", ".claude/agents/critic.md",
			".claude/skills/review/SKILL.md", ".mcp.json/mcpServers/lola-gh"))

		t := trashOf(res)
		Expect(read(filepath.Join(t, ".claude/commands/ship.md"))).To(Equal("python ship\n"), "replaced files are backed up first")
		Expect(read(filepath.Join(t, ".claude/skills/review/old.md"))).To(Equal("a file the module no longer has\n"))
		Expect(read(filepath.Join(t, ".mcp.json"))).To(ContainSubstring(".lola/modules/review"))
		Expect(read(filepath.Join(t, ".claude/skills/review_review/SKILL.md"))).To(Equal("a skill the Python lola renamed\n"))
		Expect(read(filepath.Join(t, ".lola/modules/review/skills/review/SKILL.md"))).To(Equal("python copy\n"))
		Expect(filepath.Join(proj, ".claude/skills/review_review")).NotTo(BeAnExistingFile(), "moved, not copied")
		Expect(filepath.Join(proj, ".claude/skills/review/old.md")).NotTo(BeAnExistingFile(), "a backed-up leftover is unlinked")
		Expect(filepath.Join(proj, ".lola/modules/review/lola.yml")).To(BeAnExistingFile(), "lola-go's copy took the place")

		Expect(d.Replaced).To(ContainElements(
			migrate.Moved{Path: filepath.Join(proj, ".claude/commands/ship.md"), Kind: migrate.KindCommand, Trash: filepath.Join(t, ".claude/commands/ship.md")},
			migrate.Moved{Path: filepath.Join(proj, ".mcp.json"), Key: "/mcpServers/lola-gh", Kind: migrate.KindMCP, Trash: filepath.Join(t, ".mcp.json")},
		))
		Expect(d.Moved).To(ConsistOf(migrate.Moved{Path: filepath.Join(proj, ".claude/skills/review_review"), Kind: migrate.KindSkill,
			Trash: filepath.Join(t, ".claude/skills/review_review")}))
		Expect(d.NotMigrated).To(ContainElement(HaveField("Path", filepath.Join(proj, "CLAUDE.md"))))
		Expect(read(filepath.Join(proj, "CLAUDE.md"))).To(ContainSubstring("Be kind."), "instructions are never edited")

		for _, line := range strings.Split(strings.TrimSpace(read(filepath.Join(res.Trash, "manifest.txt"))), "\n") {
			orig, to, ok := strings.Cut(line, "\t")
			Expect(ok).To(BeTrue())
			Expect(filepath.IsAbs(orig)).To(BeTrue())
			_, err := os.Lstat(to)
			Expect(err).NotTo(HaveOccurred(), "every manifest line names an existing trash path")
		}

		Expect(res.Home.Action).To(Equal(migrate.HomeMove))
		Expect(h.dir).NotTo(BeADirectory())
		Expect(filepath.Join(res.Home.Trash, "installed.yml")).To(BeAnExistingFile())

		res, err = run(migrate.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Nothing).To(BeTrue(), "a second run does nothing")
	})

	It("writes nothing on a dry run, and shows what it would replace and move", func() {
		python(proj)
		h.install(record(proj))
		before := snapshot(proj, h.dir, filepath.Join(tmp, "state"))
		res, err := run(migrate.Options{Options: engine.Options{DryRun: true}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.DryRun).To(BeTrue())
		Expect(res.Trash).To(BeEmpty())
		d := res.Destinations[0]
		Expect(d.Status).To(Equal(migrate.StatusPlanned))
		Expect(d.Installs[0].Module).To(Equal("review"))
		Expect(d.Replaced).To(ContainElement(migrate.Moved{Path: filepath.Join(proj, ".claude/commands/ship.md"), Kind: migrate.KindCommand}))
		Expect(d.Moved).To(ConsistOf(migrate.Moved{Path: filepath.Join(proj, ".claude/skills/review_review"), Kind: migrate.KindSkill}))
		Expect(res.Home.Action).To(Equal(migrate.HomeMove))
		Expect(snapshot(proj, h.dir, filepath.Join(tmp, "state"))).To(Equal(before))
	})

	It("installs the version it planned, though the source changed after the preview", func() {
		python(proj)
		h.install(record(proj))
		p := plan()
		_, err := migrate.Migrate(ctx, e, p, migrate.Options{Options: engine.Options{DryRun: true}})
		Expect(err).NotTo(HaveOccurred())
		writeFile(filepath.Join(src, "commands/ship.md"), "---\ndescription: Ship it\n---\nShip something newer\n")
		_, err = migrate.Migrate(ctx, e, p, migrate.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(filepath.Join(proj, ".claude/commands/ship.md"))).To(ContainSubstring("Ship $ARGUMENTS"))
	})

	It("adds an assistant to a module lola-go has here at the version installed there, never upgrading it", func() {
		sc := engine.Scope{Kind: target.Project, ProjectDir: proj}
		_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: src}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		writeFile(filepath.Join(src, "commands/ship.md"), "---\ndescription: Ship it\n---\nShip something newer\n")
		writeTree(proj, map[string]string{".gemini/commands/ship.toml": "python toml\n"})
		h.install(rec{"module": "review", "assistant": "gemini-cli", "project_path": proj, "commands": []string{"ship"}})
		led, err := e.Ledger(sc)
		Expect(err).NotTo(HaveOccurred())
		p := plan()
		Expect(p.Destinations[0].Installs).To(HaveLen(1))
		Expect(led.Modules["review"].Digest).To(HavePrefix("sha256:"+p.Destinations[0].Installs[0].Version.ID), "the plan shows the installed version")
		res, err := migrate.Migrate(ctx, e, p, migrate.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Destinations[0].Installs).To(ContainElement(HaveField("Status", migrate.InstallNew)))
		Expect(read(filepath.Join(proj, ".claude/commands/ship.md"))).NotTo(ContainSubstring("newer"))
		Expect(read(filepath.Join(proj, ".gemini/commands/ship.toml"))).NotTo(ContainSubstring("newer"))
		Expect(read(filepath.Join(proj, ".gemini/commands/ship.toml"))).To(ContainSubstring("Ship"))
	})

	It("install by name never trusts a pulled source: migrate refuses to rewrite a pulled entry", func() {
		sc := engine.Scope{Kind: target.Project, ProjectDir: proj}
		_, err := e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: src}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		mpath := filepath.Join(proj, ".lola", "project.yml")
		writeFile(mpath, strings.Replace(read(mpath), "  - name: review\n", "  - name: review\n    rules: false\n", 1)) // a pull
		writeTree(proj, map[string]string{".gemini/commands/ship.toml": "python toml\n"})
		h.install(rec{"module": "review", "assistant": "gemini-cli", "project_path": proj, "commands": []string{"ship"}})
		before := snapshot(proj)
		res, err := run(migrate.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(snapshot(proj)).To(Equal(before), "nothing written")
		Expect(res.Destinations[0].Unmappable).To(ConsistOf(And(HaveField("Module", "review"),
			HaveField("Reason", "review: project.yml changed for it since you last trusted it"),
			HaveField("Hints", []string{"review and apply it: lola sync -C " + proj + " review"}))),
			"hints name the scope the user ran: migrate ran outside the project")

		GinkgoT().Chdir(proj)
		res, err = run(migrate.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Destinations[0].Unmappable).To(ConsistOf(HaveField("Hints", []string{"review and apply it: lola sync --project review"})),
			"from inside the project, no -C")
	})

	It("refuses a planned version removed from the cache since, writing nothing", func() {
		python(proj)
		h.install(record(proj))
		p := plan()
		_, err := e.Store.Remove("review")
		Expect(err).NotTo(HaveOccurred())
		before := snapshot(proj, h.dir)
		_, err = migrate.Migrate(ctx, e, p, migrate.Options{})
		Expect(err).To(MatchError(ContainSubstring("was removed from the cache since it was listed")))
		Expect(errs.Hints(err)).NotTo(BeEmpty())
		Expect(snapshot(proj, h.dir)).To(Equal(before))
	})

	It("refuses a conflict outside the replaceable paths at any destination, writing nothing anywhere", func() {
		other := filepath.Join(tmp, "other")
		python(proj)
		python(other)
		writeFile(filepath.Join(other, ".claude/commands/ship.md"), "the user's own command\n")
		r := record(other)
		r["commands"] = []string{}
		h.install(record(proj), r)
		before := snapshot(proj, other, h.dir, filepath.Join(tmp, "state"))
		_, err := run(migrate.Options{})
		Expect(err).To(MatchError(ContainSubstring("project " + other)))
		Expect(err).To(MatchError(ContainSubstring(".claude/commands/ship.md: exists and was not written by lola")))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("--force")))
		Expect(snapshot(proj, other, h.dir, filepath.Join(tmp, "state"))).To(Equal(before))
	})

	It("coaches removing the module the Python lola skipped a same-named component in, writing nothing; the rerun migrates", func() {
		proj = filepath.Join(tmp, "it's proj") // the coached command quotes it for the shell
		kit := filepath.Join(tmp, "src", "kit")
		writeTree(kit, map[string]string{"skills/review/SKILL.md": "---\ndescription: Kit review\n---\nKit.\n",
			"agents/helper.md": "---\ndescription: helper\n---\nHelp.\n"})
		h.source("kit", map[string]string{"source": kit, "type": "folder"})
		python(proj)
		writeFile(filepath.Join(proj, ".claude/agents/helper.md"), "python helper\n")
		h.install(record(proj), rec{"module": "kit", "assistant": "claude-code", "project_path": proj, "agents": []string{"helper"}})
		before := snapshot(proj, h.dir)
		_, err := run(migrate.Options{})
		Expect(err).To(MatchError(ContainSubstring(`.claude/skills/review/SKILL.md: also provided by module "review" in this operation`)))
		Expect(err).To(MatchError(HavePrefix("project " + proj + ": review and kit both have skill/review for claude-code; " +
			"the Python lola installed review's and skipped kit's")))
		Expect(errs.Hints(err)).To(ContainElement("remove kit here with the Python lola first (its command: " +
			errs.ShellJoin([]string{"lola", "uninstall", "kit", "-a", "claude-code", proj}) + "), then rerun lola migrate"))
		Expect(errs.Hints(err)).NotTo(ContainElement(ContainSubstring("claude-code " + proj)))
		Expect(snapshot(proj, h.dir)).To(Equal(before))

		By("doing what the Python lola's uninstall does: dropping kit's record and the files it lists")
		h.install(record(proj))
		Expect(os.Remove(filepath.Join(proj, ".claude/agents/helper.md"))).To(Succeed())
		res, err := run(migrate.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Destinations[0].Status).To(Equal(migrate.StatusMigrated))
	})

	It("keeps the general coaching when both records list the component two modules have", func() {
		proj = filepath.Join(tmp, "it's proj") // the coached command quotes it for the shell
		kit := filepath.Join(tmp, "src", "kit")
		writeTree(kit, map[string]string{"skills/review/SKILL.md": "---\ndescription: Kit review\n---\nKit.\n"})
		h.source("kit", map[string]string{"source": kit, "type": "folder"})
		python(proj)
		h.install(record(proj), rec{"module": "kit", "assistant": "claude-code", "project_path": proj, "skills": []string{"review"}})
		_, err := run(migrate.Options{})
		Expect(err).To(MatchError(ContainSubstring("also provided by module")))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("lola uninstall <module> -a <assistant> " + errs.ShellJoin([]string{proj}) + ")")))
		Expect(errs.Hints(err)).NotTo(ContainElement(ContainSubstring("<assistant> " + proj)))
	})

	It("keeps what lola-go did not replace with --keep-python, and the Python home", func() {
		python(proj)
		h.install(record(proj))
		res, err := run(migrate.Options{KeepPython: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(filepath.Join(proj, ".claude/commands/ship.md"))).To(ContainSubstring("Ship $ARGUMENTS"))
		Expect(filepath.Join(res.Trash, res.Destinations[0].ID, ".claude/commands/ship.md")).To(BeAnExistingFile())
		Expect(filepath.Join(proj, ".claude/skills/review_review")).To(BeADirectory())
		Expect(res.Destinations[0].Kept).To(ContainElement(migrate.Kept{Path: filepath.Join(proj, ".claude/skills/review_review"),
			Reason: "--keep-python keeps what lola-go did not replace"}))
		Expect(res.Home).To(HaveField("Action", migrate.HomeKeep))
		Expect(res.Home.Reason).To(HavePrefix("--keep-python"))
		Expect(res.Home.Reason).To(ContainSubstring("lola-go sees a project at ~"))
		Expect(h.dir).To(BeADirectory())
	})

	It("keeps, and lists, a replaced directory's files lola-go did not write with --keep-python", func() {
		python(proj)
		h.install(record(proj))
		res, err := run(migrate.Options{KeepPython: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(filepath.Join(proj, ".claude/skills/review/old.md"))).To(Equal("a file the module no longer has\n"))
		Expect(res.Destinations[0].Kept).To(ContainElement(migrate.Kept{Path: filepath.Join(proj, ".claude/skills/review/old.md"),
			Reason: "--keep-python keeps what lola-go did not replace"}))
		Expect(read(filepath.Join(trashOf(res), ".claude/skills/review/old.md"))).To(Equal("a file the module no longer has\n"),
			"the directory was still backed up before the install")
	})

	It("maps the other assistants' paths: Gemini TOML commands replaced, OpenCode sidecars and legacy paths moved", func() {
		writeTree(proj, map[string]string{
			".gemini/commands/ship.toml":       "python toml\n",
			".opencode/commands/ship.md":       "python ship\n",
			".opencode/commands/ship/notes.md": "python sidecar\n",
			".opencode/command/ship.md":        "legacy\n",
			".opencode/agent/review.critic.md": "legacy prefixed\n",
		})
		h.install(rec{"module": "review", "assistant": "gemini-cli", "project_path": proj, "commands": []string{"ship"}},
			rec{"module": "review", "assistant": "opencode", "project_path": proj, "commands": []string{"ship"}, "agents": []string{"critic"}})
		res, err := run(migrate.Options{})
		Expect(err).NotTo(HaveOccurred())
		d, t := res.Destinations[0], trashOf(res)
		Expect(d.Installs[0].Targets).To(Equal([]string{"gemini-cli", "opencode"}))
		Expect(read(filepath.Join(proj, ".gemini/commands/ship.toml"))).NotTo(Equal("python toml\n"))
		Expect(read(filepath.Join(t, ".gemini/commands/ship.toml"))).To(Equal("python toml\n"))
		var moved []string
		for _, m := range d.Moved {
			moved = append(moved, m.Path)
		}
		Expect(moved).To(ConsistOf(filepath.Join(proj, ".opencode/commands/ship"), filepath.Join(proj, ".opencode/command/ship.md"),
			filepath.Join(proj, ".opencode/agent/review.critic.md")))
		Expect(read(filepath.Join(t, ".opencode/commands/ship/notes.md"))).To(Equal("python sidecar\n"))
	})

	It("moves a renamed component's path, which lola-go does not write", func() {
		writeTree(proj, map[string]string{".claude/skills/review_review/SKILL.md": "renamed by the Python lola\n"})
		h.install(rec{"module": "review", "assistant": "claude-code", "project_path": proj, "skills": []string{"review_review"}})
		res, err := run(migrate.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Destinations[0].Moved).To(ConsistOf(HaveField("Path", filepath.Join(proj, ".claude/skills/review_review"))))
		Expect(filepath.Join(proj, ".claude/skills/review/SKILL.md")).To(BeAnExistingFile())
	})

	Describe("guards", func() {
		It("keeps a recorded path that is a symbolic link, or below one", func() {
			elsewhere := filepath.Join(tmp, "dotfiles")
			writeTree(elsewhere, map[string]string{"critic.md": "theirs\n", "old.md": "theirs\n"})
			Expect(os.MkdirAll(filepath.Join(proj, ".claude", "skills"), 0o755)).To(Succeed())
			Expect(os.MkdirAll(filepath.Join(proj, ".opencode"), 0o755)).To(Succeed())
			Expect(os.Symlink(elsewhere, filepath.Join(proj, ".opencode", "agent"))).To(Succeed())
			Expect(os.Symlink(filepath.Join(elsewhere, "old.md"), filepath.Join(proj, ".claude", "skills", "review_review"))).To(Succeed())
			h.install(rec{"module": "review", "assistant": "claude-code", "project_path": proj, "skills": []string{"review_review"}},
				rec{"module": "review", "assistant": "opencode", "project_path": proj, "agents": []string{"critic"}})
			res, err := run(migrate.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Destinations[0].Kept).To(ConsistOf(
				migrate.Kept{Path: filepath.Join(proj, ".opencode/agent/critic.md"), Reason: "a symbolic link, or below one"},
				migrate.Kept{Path: filepath.Join(proj, ".claude/skills/review_review"), Reason: "a symbolic link, or below one"},
			))
			Expect(filepath.Join(elsewhere, "critic.md")).To(BeAnExistingFile())
		})

		It("with --force, backs up what it writes through a symbolically linked recorded directory and reports it replaced", func() {
			elsewhere := filepath.Join(tmp, "dotfiles", "review")
			writeTree(elsewhere, map[string]string{"SKILL.md": "theirs\n"})
			Expect(os.MkdirAll(filepath.Join(proj, ".claude", "skills"), 0o755)).To(Succeed())
			Expect(os.Symlink(elsewhere, filepath.Join(proj, ".claude", "skills", "review"))).To(Succeed())
			h.install(rec{"module": "review", "assistant": "claude-code", "project_path": proj, "skills": []string{"review"}})
			_, err := run(migrate.Options{})
			Expect(err).To(MatchError(ContainSubstring(".claude/skills/review/SKILL.md: exists and was not written by lola")))

			res, err := run(migrate.Options{Options: engine.Options{Force: true}})
			Expect(err).NotTo(HaveOccurred())
			d := res.Destinations[0]
			Expect(d.Kept).To(BeEmpty(), "nothing it wrote through is reported as kept")
			Expect(d.Replaced).To(ConsistOf(MatchFields(IgnoreExtras, Fields{
				"Path": Equal(filepath.Join(proj, ".claude/skills/review/SKILL.md")), "Kind": Equal(migrate.KindSkill)})))
			Expect(read(d.Replaced[0].Trash)).To(Equal("theirs\n"))
			Expect(read(filepath.Join(elsewhere, "SKILL.md"))).To(ContainSubstring("Review it."))
			Expect(filepath.Join(proj, ".claude", "skills", "review")).To(BeADirectory(), "the link stays")
		})

		DescribeTable("with --force, backs up what it writes through a symbolic link inside a replaced directory, and keeps the link",
			func(link, real, backup string) {
				writeTree(proj, map[string]string{".claude/skills/review/SKILL.md": "python skill\n", real + "/checks.md": "python checks\n"})
				Expect(os.Symlink(link, filepath.Join(proj, ".claude/skills/review/ref"))).To(Succeed())
				h.install(rec{"module": "review", "assistant": "claude-code", "project_path": proj, "skills": []string{"review"}})
				res, err := run(migrate.Options{Options: engine.Options{Force: true}})
				Expect(err).NotTo(HaveOccurred())
				d, t := res.Destinations[0], trashOf(res)
				Expect(d.Status).To(Equal(migrate.StatusMigrated))
				Expect(d.Replaced).To(ConsistOf(
					migrate.Moved{Path: filepath.Join(proj, ".claude/skills/review"), Kind: migrate.KindSkill, Trash: filepath.Join(t, ".claude/skills/review")},
					migrate.Moved{Path: filepath.Join(proj, ".claude/skills/review/ref/checks.md"), Kind: migrate.KindSkill, Trash: filepath.Join(t, backup)},
				))
				Expect(read(filepath.Join(t, backup))).To(Equal("python checks\n"), "the file behind the link is backed up, not just the link")
				Expect(read(filepath.Join(proj, real, "checks.md"))).To(Equal("checks\n"))
				Expect(read(filepath.Join(proj, ".claude/skills/review/ref/checks.md"))).To(Equal("checks\n"), "the link lola-go writes through stays")
			},
			Entry("a link out of the directory", filepath.Join("..", "..", "..", "shared"), "shared", "shared/checks.md"),
			Entry("a link within the directory, whose copy holds the file", "real", ".claude/skills/review/real", ".claude/skills/review/real/checks.md"),
		)

		DescribeTable("owns the file a link within a replaced directory leads to: it neither unlinks it nor keeps it as edited",
			func(checks string, links map[string]string, opts migrate.Options) {
				writeTree(proj, map[string]string{".claude/skills/review/real/checks.md": checks})
				for l, to := range links {
					p := filepath.Join(proj, ".claude/skills/review", filepath.FromSlash(l))
					Expect(os.MkdirAll(filepath.Dir(p), 0o755)).To(Succeed())
					if strings.HasPrefix(to, "/") {
						to = filepath.Join(tmp, to)
					}
					Expect(os.Symlink(filepath.FromSlash(to), p)).To(Succeed())
				}
				h.install(rec{"module": "review", "assistant": "claude-code", "project_path": proj, "skills": []string{"review"}})
				res, err := run(opts)
				Expect(err).NotTo(HaveOccurred())
				d := res.Destinations[0]
				Expect(d.Status).To(Equal(migrate.StatusMigrated))
				Expect(d.Kept).To(BeEmpty())
				Expect(read(filepath.Join(proj, ".claude/skills/review/ref/checks.md"))).To(Equal("checks\n"))
			},
			Entry("the Python copy matches what lola-go writes", "checks\n", map[string]string{"ref": "real"}, migrate.Options{}),
			Entry("with --force, the Python copy differs", "python checks\n", map[string]string{"ref": "real"},
				migrate.Options{Options: engine.Options{Force: true}}),
			Entry("with --keep-python", "checks\n", map[string]string{"ref": "real"}, migrate.Options{KeepPython: true}),
			Entry("through a chain of two links", "checks\n", map[string]string{"ref": "mid", "mid": "real"}, migrate.Options{}),
			Entry("through a file link part-way along", "checks\n", map[string]string{"ref": "x", "x/checks.md": "../real/checks.md"},
				migrate.Options{}),
			Entry("through a link out of the directory and back", "checks\n", map[string]string{"ref": "../review/real"}, migrate.Options{}),
			Entry("through an absolute link to a link outside that leads back", "checks\n",
				map[string]string{"ref": "/out", "../../../../out": "proj/.claude/skills/review/real"}, migrate.Options{}),
		)

		It("does not own a file whose path only reads as inside, when a link before .. leads out", func() {
			review := filepath.Join(proj, ".claude/skills/review")
			writeTree(tmp, map[string]string{"proj/.claude/skills/review/real/checks.md": "checks\n", "real/checks.md": "checks\n",
				"outdir/keep.md": "x\n"})
			Expect(os.Symlink(filepath.Join("..", "..", "..", "..", "outdir"), filepath.Join(review, "sub"))).To(Succeed())
			Expect(os.Symlink(strings.Join([]string{review, "sub", "..", "real"}, string(filepath.Separator)), filepath.Join(review, "ref"))).To(Succeed())
			h.install(rec{"module": "review", "assistant": "claude-code", "project_path": proj, "skills": []string{"review"}})
			res, err := run(migrate.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Destinations[0].Kept).To(BeEmpty())
			Expect(read(filepath.Join(review, "ref/checks.md"))).To(Equal("checks\n"))
			Expect(filepath.Join(review, "real")).NotTo(BeAnExistingFile(), "the Python copy inside is a leftover: it matches its backup")
		})

		It("keeps what git tracks, a partly tracked directory whole, unless --force", func() {
			writeTree(proj, map[string]string{".opencode/agent/critic.md": "tracked\n", ".claude/skills/review_review/a.md": "tracked\n",
				".claude/skills/review_review/b.md": "untracked\n", ".opencode/commands/ship/notes.md": "untracked\n",
				".mcp.json": `{"mcpServers":{"lola-gh":{"command":"python"}}}`})
			git(proj, "init", "-q")
			git(proj, "add", ".opencode/agent/critic.md", ".claude/skills/review_review/a.md", ".mcp.json")
			git(proj, "commit", "-q", "-m", "x")
			h.install(rec{"module": "review", "assistant": "claude-code", "project_path": proj, "skills": []string{"review_review"},
				"mcps": []string{"lola-gh"}},
				rec{"module": "review", "assistant": "opencode", "project_path": proj, "commands": []string{"ship"}, "agents": []string{"critic"}})
			res, err := run(migrate.Options{Options: engine.Options{DryRun: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Destinations[0].Kept).To(ConsistOf(
				migrate.Kept{Path: filepath.Join(proj, ".opencode/agent/critic.md"), Reason: "tracked by git; --force moves it anyway"},
				migrate.Kept{Path: filepath.Join(proj, ".claude/skills/review_review"), Reason: "tracked by git; --force moves it anyway"},
			))
			Expect(res.Destinations[0].Moved).To(ConsistOf(HaveField("Path", filepath.Join(proj, ".opencode/commands/ship"))))
			Expect(res.Destinations[0].Replaced).To(ConsistOf(HaveField("Path", filepath.Join(proj, ".mcp.json"))),
				"an MCP entry is edited in place like every install edits a tracked settings file")

			res, err = run(migrate.Options{Options: engine.Options{Force: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Destinations[0].Moved).To(HaveLen(3))
		})

		It("refuses, naming why it keeps it, a tracked recorded path the install writes; --force replaces it with a backup", func() {
			writeTree(proj, map[string]string{".claude/agents/critic.md": "python critic\n"})
			git(proj, "init", "-q")
			git(proj, "add", ".claude/agents/critic.md")
			git(proj, "commit", "-q", "-m", "x")
			h.install(rec{"module": "review", "assistant": "claude-code", "project_path": proj, "agents": []string{"critic"}})
			before := snapshot(proj, h.dir, filepath.Join(tmp, "state"))
			for _, dry := range []bool{true, false} {
				_, err := run(migrate.Options{Options: engine.Options{DryRun: dry}})
				Expect(err).To(MatchError(HavePrefix("project " + proj + ": the install writes " + filepath.Join(proj, ".claude/agents/critic.md") +
					", which lola migrate keeps (tracked by git; --force moves it anyway): refusing to change anything")))
				Expect(errs.Hints(err)).To(ConsistOf("rerun lola migrate with --force, which replaces it after backing it up into the trash"))
				Expect(snapshot(proj, h.dir, filepath.Join(tmp, "state"))).To(Equal(before))
			}

			res, err := run(migrate.Options{Options: engine.Options{Force: true}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Destinations[0].Replaced).To(ConsistOf(migrate.Moved{Path: filepath.Join(proj, ".claude/agents/critic.md"),
				Kind: migrate.KindAgent, Trash: filepath.Join(trashOf(res), ".claude/agents/critic.md")}))
			Expect(read(filepath.Join(trashOf(res), ".claude/agents/critic.md"))).To(Equal("python critic\n"))
			Expect(read(filepath.Join(proj, ".claude/agents/critic.md"))).To(ContainSubstring("Criticize."))
		})

		It("refuses, naming why it keeps it, a recorded path below a symbolic link that the install writes", func() {
			elsewhere := filepath.Join(tmp, "dotfiles")
			writeTree(elsewhere, map[string]string{"critic.md": "theirs\n"})
			Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
			Expect(os.Symlink(elsewhere, filepath.Join(proj, ".claude", "agents"))).To(Succeed())
			h.install(rec{"module": "review", "assistant": "claude-code", "project_path": proj, "agents": []string{"critic"}})
			before := snapshot(proj, elsewhere, h.dir, filepath.Join(tmp, "state"))
			_, err := run(migrate.Options{})
			Expect(err).To(MatchError(HavePrefix("project " + proj + ": the install writes " + filepath.Join(proj, ".claude/agents/critic.md") +
				", which lola migrate keeps (a symbolic link, or below one): refusing to change anything")))
			Expect(errs.Hints(err)).To(ConsistOf("rerun lola migrate with --force, which replaces it after backing it up into the trash",
				"or replace the symbolic link with a directory, then rerun lola migrate"))
			Expect(snapshot(proj, elsewhere, h.dir, filepath.Join(tmp, "state"))).To(Equal(before))
		})

		It("keeps a path in a git work tree when git is missing, failing closed", func() {
			writeTree(proj, map[string]string{".opencode/agent/critic.md": "x\n"})
			git(proj, "init", "-q")
			h.install(rec{"module": "review", "assistant": "opencode", "project_path": proj, "agents": []string{"critic"}})
			GinkgoT().Setenv("PATH", filepath.Join(tmp, "bin"))
			res, err := run(migrate.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Destinations[0].Kept).To(ConsistOf(migrate.Kept{Path: filepath.Join(proj, ".opencode/agent/critic.md"),
				Reason: "in a git work tree, and git is not on PATH to tell whether it is tracked; --force moves it anyway"}))
		})

		It("never replaces or moves a path a lola-go module owns", func() {
			other := filepath.Join(tmp, "src", "other")
			writeTree(other, map[string]string{"skills/review_review/SKILL.md": "---\ndescription: mine\n---\nMine.\n"})
			_, err := e.Install(ctx, engine.Scope{Kind: target.Project, ProjectDir: proj}, proj, []engine.InstallRequest{{Arg: other}},
				engine.Options{Targets: []string{"claude-code"}})
			Expect(err).NotTo(HaveOccurred())
			h.install(rec{"module": "review", "assistant": "claude-code", "project_path": proj, "skills": []string{"review_review"}})
			res, err := run(migrate.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Destinations[0].Kept).To(ConsistOf(migrate.Kept{Path: filepath.Join(proj, ".claude/skills/review_review"),
				Reason: "owned by lola-go module other"}))
			Expect(read(filepath.Join(proj, ".claude/skills/review_review/SKILL.md"))).To(ContainSubstring("Mine."))
		})
	})

	Describe("the Python home and module copies", func() {
		It("leaves every record of a module at a destination, and its copy, when one record cannot be migrated", func() {
			tools := filepath.Join(tmp, "src", "toolkit")
			writeTree(tools, map[string]string{"commands/t.md": "T\n"})
			h.source("tools", map[string]string{"source": tools, "type": "folder"})
			writeTree(proj, map[string]string{".lola/modules/tools/commands/t.md": "T\n", ".claude/commands/t.md": "python t\n"})
			h.install(rec{"module": "tools", "assistant": "claude-code", "project_path": proj, "commands": []string{"t"}},
				rec{"module": "tools", "assistant": "cursor", "project_path": proj})
			before := snapshot(proj)
			res, err := run(migrate.Options{})
			Expect(err).NotTo(HaveOccurred())
			d := res.Destinations[0]
			Expect(d.Installs).To(BeEmpty())
			Expect(d.Unmappable).To(ConsistOf(HaveField("Assistant", "cursor"),
				HaveField("Reason", "kept with tools for cursor, which lola-go cannot migrate: they share the Python lola's copy of tools")))
			Expect(snapshot(proj)).To(Equal(before))
			Expect(res.Home.Action).To(Equal(migrate.HomeKeep))
			Expect(res.Home.Reason).To(HavePrefix("2 record(s) lola-go cannot migrate are still in installed.yml"))

			res, err = run(migrate.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Nothing).To(BeTrue(), "a second run only reports")
		})

		It("keeps the home and module copies an MCP entry left in place references", func() {
			writeTree(userHome, map[string]string{
				".mcp.json": `{"mcpServers":{"lola-gh":{"command":"gh-mcp","env":{"PLUGIN_DATA":"` + h.dir + `/plugin-data/review"}}}}`,
			})
			h.install(rec{"module": "review", "assistant": "claude-code", "scope": "user", "mcps": []string{"lola-gh"}})
			res, err := run(migrate.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Destinations[0].NotMigrated).To(ContainElement(migrate.Item{What: migrate.ItemMCP, Module: "review",
				Path: filepath.Join(userHome, ".mcp.json"), Marker: "/mcpServers/lola-gh", Hints: []string{"it keeps working; remove the entry by hand when you no longer need it"}}))
			Expect(read(filepath.Join(userHome, ".claude.json"))).To(ContainSubstring("lola-gh"), "lola-go writes its own entry")
			Expect(res.Home.Action).To(Equal(migrate.HomeKeep))
			Expect(res.Home.Reason).To(HavePrefix("referenced by MCP server lola-gh in " + filepath.Join(userHome, ".mcp.json")))
		})

		It("refuses an install that would replace a module copy no record it migrates accounts for, writing nothing", func() {
			tools := filepath.Join(tmp, "src", "tools")
			writeTree(tools, map[string]string{"commands/t.md": "T\n"})
			h.source("pytools", map[string]string{"source": tools, "type": "folder"})
			writeTree(proj, map[string]string{".lola/modules/tools/notes.md": "not the Python lola's\n", ".claude/commands/t.md": "python t\n"})
			h.install(rec{"module": "pytools", "assistant": "claude-code", "project_path": proj, "commands": []string{"t"}})
			before := snapshot(proj, h.dir)
			_, err := run(migrate.Options{})
			Expect(err).To(MatchError("project " + proj + ": installing tools would replace " + filepath.Join(proj, ".lola/modules/tools") +
				", which neither lola-go nor a Python lola install lola migrate migrates here owns"))
			Expect(errs.Hints(err)).To(ConsistOf(ContainSubstring("move it out of " + filepath.Join(proj, ".lola/modules"))))
			Expect(snapshot(proj, h.dir)).To(Equal(before))
		})

		Describe("a Python module copy the install swaps out", func() {
			var tools string
			BeforeEach(func() {
				tools = filepath.Join(tmp, "src", "tools")
				writeTree(tools, map[string]string{"commands/t.md": "T\n"})
				h.source("tools", map[string]string{"source": tools, "type": "folder"})
				writeTree(proj, map[string]string{".lola/modules/tools/commands/t.md": "T\n", ".claude/commands/t.md": "python t\n"})
				git(proj, "init", "-q")
				git(proj, "add", ".lola/modules/tools/commands/t.md")
				git(proj, "commit", "-q", "-m", "x")
				h.install(rec{"module": "tools", "assistant": "claude-code", "project_path": proj, "commands": []string{"t"}})
			})

			It("refuses a git-tracked copy without --force, writing nothing", func() {
				before := snapshot(proj, h.dir, filepath.Join(tmp, "state"))
				_, err := run(migrate.Options{})
				Expect(err).To(MatchError(ContainSubstring(filepath.Join(proj, ".lola/modules/tools"))))
				Expect(err).To(MatchError(ContainSubstring("tracked by git")))
				Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("--force")))
				Expect(snapshot(proj, h.dir, filepath.Join(tmp, "state"))).To(Equal(before))
			})

			It("replaces it with a backup under --force", func() {
				res, err := run(migrate.Options{Options: engine.Options{Force: true}})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Destinations[0].Status).To(Equal(migrate.StatusMigrated))
				Expect(filepath.Join(trashOf(res), ".lola/modules/tools/commands/t.md")).To(BeAnExistingFile())
			})
		})

		It("keeps only the module copy an MCP entry left in place references, not its siblings", func() {
			for _, m := range []string{"tools", "kit"} {
				src := filepath.Join(tmp, "src", m+"src")
				writeTree(src, map[string]string{"commands/" + m + ".md": m + "\n"})
				h.source(m, map[string]string{"source": src, "type": "folder"})
				writeTree(proj, map[string]string{".lola/modules/" + m + "/commands/" + m + ".md": m + "\n"})
			}
			writeTree(userHome, map[string]string{
				".mcp.json": `{"mcpServers":{"lola-gh":{"command":"` + proj + `/.lola/modules/kit/bin/gh"}}}`,
			})
			h.install(rec{"module": "review", "assistant": "claude-code", "scope": "user", "mcps": []string{"lola-gh"}},
				rec{"module": "tools", "assistant": "claude-code", "project_path": proj},
				rec{"module": "kit", "assistant": "claude-code", "project_path": proj})
			res, err := run(migrate.Options{})
			Expect(err).NotTo(HaveOccurred())
			d := res.Destinations[0]
			Expect(d.Root).To(Equal(proj))
			Expect(d.Moved).To(ConsistOf(HaveField("Path", filepath.Join(proj, ".lola/modules/tools"))))
			Expect(d.Kept).To(ConsistOf(migrate.Kept{Path: filepath.Join(proj, ".lola/modules/kit"),
				Reason: "referenced by MCP server lola-gh in " + filepath.Join(userHome, ".mcp.json")}))
		})

		DescribeTable("keeps the home unless it holds only what the Python lola writes",
			func(setup func(), want string) {
				h.install(rec{"module": "review", "assistant": "claude-code", "scope": "user"})
				setup()
				res, err := run(migrate.Options{})
				Expect(err).NotTo(HaveOccurred())
				if want == "" {
					Expect(res.Home.Action).To(Equal(migrate.HomeMove))
					return
				}
				Expect(res.Home.Action).To(Equal(migrate.HomeKeep))
				Expect(res.Home.Reason).To(HavePrefix(want))
			},
			Entry("stale temporary files are ignored", func() {
				writeFile(filepath.Join(h.dir, ".installed.yml.abc.tmp"), "")
				Expect(os.MkdirAll(filepath.Join(h.dir, "market", "x"), 0o755)).To(Succeed())
			}, ""),
			Entry("a file the Python lola does not write", func() { writeFile(filepath.Join(h.dir, "notes.txt"), "mine") },
				"it holds notes.txt, which the Python lola does not write there"),
			Entry("a home that is a symbolic link", func() {
				real := filepath.Join(tmp, "real-lola")
				Expect(os.Rename(h.dir, real)).To(Succeed())
				Expect(os.Symlink(real, h.dir)).To(Succeed())
			}, "it is a symbolic link"),
		)

		It("refuses a home that holds a destination or is lola-go's own directory", func() {
			GinkgoT().Setenv("LOLA_HOME", tmp)
			h = pyHome{dir: tmp}
			h.source("review", map[string]string{"source": src, "type": "folder"})
			Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
			h.install(rec{"module": "review", "assistant": "claude-code", "project_path": proj})
			res, err := run(migrate.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Home.Action).To(Equal(migrate.HomeKeep))
			Expect(res.Home.Reason).To(HavePrefix("it holds the project " + proj))
		})
	})

	It("migrates the other destinations when one fails, keeps the home, and names the failure", func() {
		hooked := filepath.Join(tmp, "src", "hooked")
		writeTree(hooked, map[string]string{"lola.yml": "name: hooked\nhooks: {pre-install: pre.sh}\n", "commands/h.md": "H\n",
			"pre.sh": "#!/bin/sh\nexit 3\n"})
		Expect(os.Chmod(filepath.Join(hooked, "pre.sh"), 0o755)).To(Succeed())
		h.source("hooked", map[string]string{"source": hooked, "type": "folder"})
		other := filepath.Join(tmp, "other")
		python(proj)
		writeTree(other, map[string]string{".claude/commands/h.md": "python h\n"})
		h.install(record(proj), rec{"module": "hooked", "assistant": "claude-code", "project_path": other, "commands": []string{"h"}})
		res, err := run(migrate.Options{Options: engine.Options{AllowHooks: true}})
		Expect(err).To(MatchError(ContainSubstring("lola migrate did not finish: project " + other + " failed")))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("rerun lola migrate")))
		Expect(res.Destinations[0].Root).To(Equal(other))
		Expect(res.Destinations[0].Status).To(Equal(migrate.StatusFailed))
		Expect(res.Destinations[0].Error).To(ContainSubstring("pre-install"))
		Expect(read(filepath.Join(other, ".claude/commands/h.md"))).To(Equal("python h\n"))
		Expect(res.Destinations[1].Status).To(Equal(migrate.StatusMigrated))
		Expect(filepath.Join(proj, ".claude/skills/review_review")).NotTo(BeAnExistingFile())
		Expect(res.Home.Action).To(Equal(migrate.HomeKeep))
		Expect(res.Home.Reason).To(HavePrefix("a destination failed"))
	})

	It("reports what it did and coaches when the trash cannot be closed", func() {
		base := filepath.Join(tmp, "state", "lola", "trash")
		By("a hook that copies a read-only directory into the unused trash, so removing it fails")
		locked := filepath.Join(tmp, "locked", "x")
		writeFile(filepath.Join(locked, "f"), "f")
		Expect(os.Chmod(locked, 0o500)).To(Succeed())
		DeferCleanup(func() {
			copies, err := filepath.Glob(filepath.Join(base, "*", "x"))
			Expect(err).NotTo(HaveOccurred())
			for _, p := range append(copies, locked) {
				Expect(os.Chmod(p, 0o700)).To(Succeed())
			}
		})
		hooked := filepath.Join(tmp, "src", "hooked")
		writeTree(hooked, map[string]string{"lola.yml": "name: hooked\nhooks: {pre-install: pre.sh}\n", "commands/h.md": "H\n",
			"pre.sh": "#!/bin/sh\ncp -Rp '" + locked + "' '" + base + "'/*/\n"})
		Expect(os.Chmod(filepath.Join(hooked, "pre.sh"), 0o755)).To(Succeed())
		h.source("hooked", map[string]string{"source": hooked, "type": "folder"})
		writeFile(filepath.Join(h.dir, "notes.txt"), "keeps the home in place")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		h.install(rec{"module": "hooked", "assistant": "claude-code", "project_path": proj})
		res, err := run(migrate.Options{Options: engine.Options{AllowHooks: true}})
		Expect(err).To(MatchError(HavePrefix("lola migrate did not finish: closing the trash failed: ")))
		Expect(res).NotTo(BeNil())
		Expect(res.Destinations[0].Status).To(Equal(migrate.StatusMigrated))
		Expect(res.Trash).To(BeEmpty(), "nothing was put in it")
		hints := errs.Hints(err)
		dirs, err := filepath.Glob(filepath.Join(base, "*"))
		Expect(err).NotTo(HaveOccurred())
		Expect(dirs).To(HaveLen(1))
		Expect(hints).To(ContainElement("the trash is " + dirs[0] + "; its manifest.txt lists everything lola backed up or moved there"))
	})

	It("asks every hook consent before writing anything, so a cancelled prompt writes nothing anywhere", func() {
		hooked := filepath.Join(tmp, "src", "hooked")
		writeTree(hooked, map[string]string{"lola.yml": "name: hooked\nhooks: {pre-install: pre.sh}\n", "commands/h.md": "H\n",
			"pre.sh": "#!/bin/sh\nexit 0\n"})
		Expect(os.Chmod(filepath.Join(hooked, "pre.sh"), 0o755)).To(Succeed())
		h.source("hooked", map[string]string{"source": hooked, "type": "folder"})
		other := filepath.Join(tmp, "zz-other")
		python(proj)
		Expect(os.MkdirAll(other, 0o755)).To(Succeed())
		h.install(record(proj), rec{"module": "hooked", "assistant": "claude-code", "project_path": other})
		before := snapshot(proj, other, h.dir, filepath.Join(tmp, "state", "lola", "trash"))
		var asked []string
		_, err := run(migrate.Options{Options: engine.Options{Ask: func(module string, _ []engine.HookScript) (bool, error) {
			asked = append(asked, module)
			return false, errors.New("cancelled")
		}}})
		Expect(err).To(MatchError(ContainSubstring("cancelled")))
		Expect(asked).To(Equal([]string{"hooked"}))
		Expect(snapshot(proj, other, h.dir, filepath.Join(tmp, "state", "lola", "trash"))).To(Equal(before))
	})

	It("leaves a destination with nothing to migrate unchanged", func() {
		python(proj)
		h.install(record(proj), rec{"module": "review", "assistant": "cursor", "scope": "user"})
		res, err := run(migrate.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Destinations[0].Status).To(Equal(migrate.StatusMigrated))
		Expect(res.Destinations[1].Status).To(Equal(migrate.StatusUnchanged))
	})

	It("reports nothing to migrate without a Python home, and moves a home with no installs", func() {
		res, err := run(migrate.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Nothing).To(BeFalse())
		Expect(res.Home.Action).To(Equal(migrate.HomeMove), "registered modules alone are nothing lola-go needs")
		Expect(h.dir).NotTo(BeADirectory())

		res, err = run(migrate.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Nothing).To(BeTrue())
		Expect(res.Home.Action).To(Equal(migrate.HomeNone))
	})
})
