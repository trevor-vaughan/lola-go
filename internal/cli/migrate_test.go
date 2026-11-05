package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

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
		Expect(r.err).To(ContainSubstring("hint: pass -y to confirm"))
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
		Expect(r.err).To(ContainSubstring("--force"))
		Expect(trash()).NotTo(BeADirectory())
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
		for _, f := range []string{"--dry-run", "--yes", "--force", "--allow-hooks", "--allow-runtime-hooks", "--keep-python"} {
			Expect(out).To(ContainSubstring(f))
		}
	})
})

func read(p string) string {
	b, err := os.ReadFile(p)
	Expect(err).NotTo(HaveOccurred())
	return string(b)
}
