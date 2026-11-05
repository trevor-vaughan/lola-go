package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// runConfirmed runs a hint that names another command as printed. That hint carries no -y (R-8),
// so off a terminal a run that removes files refuses, and its last hint is that run with -y (its
// words as the refusal names them), which runConfirmed runs.
func runConfirmed(hint string) result {
	GinkgoHelper()
	r := runHint(hint)
	if r.code == 0 {
		return r
	}
	hs := hintLines(r.err)
	if len(hs) == 0 { // under --json the refusal is a document
		var doc struct{ Hints []string }
		Expect(json.Unmarshal([]byte(r.err), &doc)).To(Succeed(), r.err)
		hs = doc.Hints
	}
	Expect(hs).NotTo(BeEmpty(), r.err)
	last := hs[len(hs)-1]
	confirm := last[strings.Index(last, "lola "):]
	Expect(confirm).To(HaveSuffix(" -y"), r.err)
	return runHint(confirm)
}

var _ = Describe("the move hint after an install (spec 2026-10-29 §3.7)", func() {
	var (
		tmp, proj, notes string
		p                paths.Paths
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		proj, notes = filepath.Join(tmp, "proj"), filepath.Join(tmp, "src", "notes")
		writeTree(notes, map[string]string{"lola.yml": "name: notes\n", "commands/notes.md": "N\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		GinkgoT().Chdir(proj)
	})
	read := func(path string) string {
		GinkgoHelper()
		b, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}
	// moveHints is the move hint lines of an install's human output, without their arrow.
	moveHints := func(out string) []string {
		hints := []string{}
		for _, line := range strings.Split(out, "\n") {
			text, ok := strings.CutPrefix(line, "→ ")
			if ok && (strings.HasPrefix(text, "meant for ") || strings.HasPrefix(text, "keep it to yourself: ") || strings.HasPrefix(text, "other profiles: ")) {
				hints = append(hints, text)
			}
		}
		return hints
	}
	install := func(args ...string) []string {
		GinkgoHelper()
		r := lola(append([]string{"install"}, args...)...)
		Expect(r.code).To(Equal(0), r.err)
		return moveHints(r.out + r.err)
	}
	// command is a hint's command line, after its question.
	command := func(hint string) string {
		_, cmd, _ := strings.Cut(hint, "? ")
		if strings.HasPrefix(hint, "keep it to yourself: ") {
			cmd = strings.TrimPrefix(hint, "keep it to yourself: ")
		}
		return cmd
	}
	userNotes := func() string { return filepath.Join(p.Home, ".claude", "commands", "notes.md") }
	projectNotes := func() string { return filepath.Join(proj, ".claude", "commands", "notes.md") }

	It("after a project.yml write, the move into your profile switches to it and keeps the module installed", func() {
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n"})
		hints := install(notes, "--project", "-t", "claude-code")
		Expect(hints).To(Equal([]string{
			"meant for another profile? lola profile --project --move notes --to me --switch",
			"meant for every project? lola profile --user --move notes --to base"}))
		r := runHint(command(hints[0]))
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(p.UserProfiles, "me.yml"))).To(ContainSubstring("name: notes"))
		Expect(read(filepath.Join(proj, ".lola", "project.yml"))).NotTo(ContainSubstring("notes"))
		Expect(projectNotes()).To(BeAnExistingFile())
	})

	It("after a project.yml write, the move into every project runs as printed, from here and with -C", func() {
		hints := install(notes, "--project", "-t", "claude-code")
		Expect(hints).To(ContainElement("meant for every project? lola profile --user --move notes --to base"))
		r := runConfirmed(command(hints[len(hints)-1]))
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(p.UserProfiles, "default.yml"))).To(ContainSubstring("name: notes"))
		Expect(read(filepath.Join(proj, ".lola", "project.yml"))).NotTo(ContainSubstring("notes"))
		Expect(userNotes()).To(BeAnExistingFile())
		Expect(projectNotes()).NotTo(BeAnExistingFile())

		other := filepath.Join(tmp, "src", "other")
		writeTree(other, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "O\n"})
		GinkgoT().Chdir(tmp)
		hints = install(other, "-C", proj, "-t", "claude-code")
		Expect(hints).To(ContainElement("meant for every project? lola profile --user --move other --to base -C " + proj))
		r = runConfirmed(command(hints[len(hints)-1]))
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(p.UserProfiles, "default.yml"))).To(ContainSubstring("name: other"))
		Expect(read(filepath.Join(proj, ".lola", "project.yml"))).NotTo(ContainSubstring("other"))
	})

	It("with no profile of yours, keep it to yourself creates mine, switches to it and keeps the module installed", func() {
		hints := install(notes, "--project", "-t", "claude-code")
		Expect(hints[0]).To(Equal("keep it to yourself: lola profile --project --move notes --to mine --switch"))
		r := runHint(command(hints[0]))
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(p.UserProfiles, "mine.yml"))).To(ContainSubstring("name: notes"))
		Expect(read(filepath.Join(proj, ".lola", "project.yml"))).NotTo(ContainSubstring("notes"))
		Expect(projectNotes()).To(BeAnExistingFile())
	})

	It("after a user-scope write in a found project, the move into this project only runs as printed", func() {
		writeTree(p.UserProfiles, map[string]string{"a.yml": "version: 1\n"})
		hints := install(notes, "--user", "-t", "claude-code")
		Expect(hints).To(Equal([]string{
			"meant for another profile? lola profile --user --move notes --to a --switch",
			"meant for this project only? lola profile --project --move notes --to user:a --switch"}))
		r := runConfirmed(command(hints[1]))
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(p.UserProfiles, "a.yml"))).To(ContainSubstring("name: notes"))
		Expect(read(filepath.Join(p.UserProfiles, "default.yml"))).NotTo(ContainSubstring("notes"))
		Expect(userNotes()).NotTo(BeAnExistingFile())
		Expect(projectNotes()).To(BeAnExistingFile())
	})

	// onAtUser switches a, which lists kept, on at user scope, beside b, and installs notes there,
	// into a, from a directory with no project: the hints that follow.
	onAtUser := func() []string {
		GinkgoHelper()
		kept := filepath.Join(tmp, "src", "kept")
		writeTree(kept, map[string]string{"lola.yml": "name: kept\n", "commands/kept.md": "K\n"})
		writeTree(p.UserProfiles, map[string]string{"a.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kept, source: " + kept + "}\n",
			"b.yml": "version: 1\n"})
		r := lola("profile", "a", "--switch", "--user", "-y")
		Expect(r.code).To(Equal(0), r.err)
		GinkgoT().Chdir(tmp)
		return install(notes, "--user", "-t", "claude-code")
	}
	keptFile := func() string { return filepath.Join(p.Home, ".claude", "commands", "kept.md") }

	It("from the profile on at user scope, the move into another profile names the profile its switch turns off, and runs as printed", func() {
		hints := onAtUser()
		Expect(hints).To(Equal([]string{
			"meant for another profile (switches a off, removing its modules)? lola profile --user --move notes --to b --switch",
			"meant for another profile? lola profile --user --move notes --to base"}))
		r := runConfirmed(command(hints[0]))
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(p.UserProfiles, "b.yml"))).To(ContainSubstring("name: notes"))
		Expect(read(filepath.Join(p.UserProfiles, "a.yml"))).NotTo(ContainSubstring("notes"))
		Expect(userNotes()).To(BeAnExistingFile())
		Expect(keptFile()).NotTo(BeAnExistingFile(), "what the hint said: a is off, its modules removed")
	})

	It("from the profile on at user scope, the move into the base turns nothing off, and runs as printed", func() {
		hints := onAtUser()
		r := runConfirmed(command(hints[1]))
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(p.UserProfiles, "default.yml"))).To(ContainSubstring("name: notes"))
		Expect(userNotes()).To(BeAnExistingFile())
		Expect(keptFile()).To(BeAnExistingFile())
	})

	It("groups the modules one write added into one move, which runs as printed", func() {
		other := filepath.Join(tmp, "src", "other")
		writeTree(other, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "O\n"})
		hints := install(notes, other, "--project", "-t", "claude-code")
		Expect(hints).To(Equal([]string{
			"keep it to yourself: lola profile --project --move notes,other --to mine --switch",
			"meant for every project? lola profile --user --move notes,other --to base"}))
		r := runConfirmed(command(hints[1]))
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(p.UserProfiles, "default.yml"))).To(And(ContainSubstring("name: notes"), ContainSubstring("name: other")))
		Expect(read(filepath.Join(proj, ".lola", "project.yml"))).NotTo(Or(ContainSubstring("notes"), ContainSubstring("other")))
		Expect(filepath.Join(p.Home, ".claude", "commands", "other.md")).To(BeAnExistingFile())
		Expect(userNotes()).To(BeAnExistingFile())
	})

	It("without a profile of yours, the two-step into this project runs as printed", func() {
		hints := install(notes, "--user", "-t", "claude-code")
		Expect(hints).To(Equal([]string{"meant for this project only? lola uninstall --user notes, then lola install --project notes -t claude-code"}))
		first, second, _ := strings.Cut(command(hints[0]), ", then ")
		r := runConfirmed(first)
		Expect(r.code).To(Equal(0), r.err)
		r = runConfirmed(second)
		Expect(r.code).To(Equal(0), r.err)
		Expect(userNotes()).NotTo(BeAnExistingFile())
		Expect(projectNotes()).To(BeAnExistingFile())
		Expect(read(filepath.Join(proj, ".lola", "project.yml"))).To(ContainSubstring("name: notes"))
	})

	It("qualifies a profile of yours a project profile shares the name of, and the move runs as printed", func() {
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n"})
		writeTree(proj, map[string]string{".lola/profiles/me.yml": "version: 1\n"})
		hints := install(notes, "--project", "-t", "claude-code")
		Expect(hints[0]).To(Equal("meant for another profile? lola profile --project --move notes --to user:me --switch"))
		r := runHint(command(hints[0]))
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(p.UserProfiles, "me.yml"))).To(ContainSubstring("name: notes"))
	})

	It("after a write to a project profile on, the moves into mine and into every project run as printed", func() {
		writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\n"})
		r := lola("profile", "team", "--switch", "--project", "-y")
		Expect(r.code).To(Equal(0), r.err)
		hints := install(notes, "--project", "-t", "claude-code")
		Expect(hints).To(Equal([]string{
			"keep it to yourself: lola profile --project --move notes --to mine --switch",
			"meant for every project? lola profile --user --move notes --to base"}))
		team := filepath.Join(proj, ".lola", "profiles", "team.yml")
		r = runConfirmed(command(hints[0]))
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(p.UserProfiles, "mine.yml"))).To(ContainSubstring("name: notes"))
		Expect(read(team)).NotTo(ContainSubstring("notes"))
		Expect(projectNotes()).To(BeAnExistingFile())

		other := filepath.Join(tmp, "src", "other")
		writeTree(other, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "O\n"})
		Expect(lola("profile", "team", "--switch", "--project", "-y").code).To(Equal(0))
		hints = install(other, "--project", "-t", "claude-code")
		Expect(hints).To(Equal([]string{
			"meant for another profile? lola profile --project --move other --to mine --switch",
			"meant for every project? lola profile --user --move other --to base"}),
			"a switch to mine installs notes again, which asks nothing")
		r = runConfirmed(command(hints[1]))
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(p.UserProfiles, "default.yml"))).To(ContainSubstring("name: other"))
		Expect(read(team)).NotTo(ContainSubstring("other"))
		Expect(filepath.Join(p.Home, ".claude", "commands", "other.md")).To(BeAnExistingFile())
	})

	It("after a write to your profile on in the project, the move into every project runs as printed", func() {
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n"})
		r := lola("profile", "me", "--switch", "--project", "-y")
		Expect(r.code).To(Equal(0), r.err)
		hints := install(notes, "--project", "-t", "claude-code")
		Expect(hints).To(Equal([]string{"meant for every project? lola profile --user --move notes --to base"}))
		r = runConfirmed(command(hints[0]))
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(p.UserProfiles, "default.yml"))).To(ContainSubstring("name: notes"))
		Expect(read(filepath.Join(p.UserProfiles, "me.yml"))).NotTo(ContainSubstring("notes"))
		Expect(userNotes()).To(BeAnExistingFile())
		Expect(projectNotes()).NotTo(BeAnExistingFile())
	})

	It("after a write to a profile that is off here, each move names it as the source and runs as printed", func() {
		writeTree(p.UserProfiles, map[string]string{"a.yml": "version: 1\n", "b.yml": "version: 1\n"})
		Expect(lola("profile", "b", "--switch", "--user").code).To(Equal(0))
		hints := install(notes, "--user", "--profile", "a", "-t", "claude-code")
		Expect(hints).To(Equal([]string{
			"meant for another profile? lola profile --user a --move notes --to b",
			"meant for another profile? lola profile --user a --move notes --to base"}))
		r := runHint(command(hints[1]) + " -n")
		Expect(r.code).To(Equal(0), r.err)
		r = runHint(command(hints[0]))
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(p.UserProfiles, "b.yml"))).To(ContainSubstring("name: notes"))
		Expect(read(filepath.Join(p.UserProfiles, "a.yml"))).NotTo(ContainSubstring("notes"))
		Expect(userNotes()).To(BeAnExistingFile(), "b is on: the move installs it")
	})

	It("after a write to a profile that is off, a move into another profile that is off keeps it off", func() {
		writeTree(p.UserProfiles, map[string]string{"a.yml": "version: 1\n", "c.yml": "version: 1\n"})
		hints := install(notes, "--user", "--profile", "a", "-t", "claude-code")
		Expect(hints).To(Equal([]string{
			"meant for another profile? lola profile --user a --move notes --to c --keep-off",
			"meant for another profile? lola profile --user a --move notes --to base"}))
		r := runHint(command(hints[1]) + " -n")
		Expect(r.code).To(Equal(0), r.err)
		r = runHint(command(hints[0]))
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(p.UserProfiles, "c.yml"))).To(ContainSubstring("name: notes"))
		Expect(userNotes()).NotTo(BeAnExistingFile(), "nothing is on that lists it")
	})

	It("after a write to a profile that is off, never hints a move that installs a hooked module, which asks for consent", func() {
		hk := filepath.Join(tmp, "src", "hk")
		writeTree(hk, map[string]string{"lola.yml": "name: hk\nhooks: {pre-install: setup.sh}\n", "setup.sh": "#!/bin/sh\nexit 0\n",
			"commands/hk.md": "H\n"})
		writeTree(p.UserProfiles, map[string]string{"a.yml": "version: 1\n", "b.yml": "version: 1\n", "c.yml": "version: 1\n"})
		Expect(lola("profile", "b", "--switch", "--user").code).To(Equal(0))
		hints := install(hk, "--user", "--profile", "a", "-t", "claude-code")
		Expect(hints).To(Equal([]string{"meant for another profile? lola profile --user a --move hk --to c --keep-off"}),
			"a move into b, on, or into the base installs hk, which asks for consent")
		r := runHint(command(hints[0]))
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(p.UserProfiles, "c.yml"))).To(ContainSubstring("name: hk"))
	})

	It("after a write to a profile that is off, a move into one that is off and lists other modules keeps it off and installs nothing", func() {
		other := filepath.Join(tmp, "src", "other")
		writeTree(other, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "O\n"})
		writeTree(p.UserProfiles, map[string]string{"a.yml": "version: 1\n",
			"c.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: other, source: " + other + "}\n"})
		hints := install(notes, "--user", "--profile", "a", "-t", "claude-code")
		Expect(hints).To(Equal([]string{
			"meant for another profile? lola profile --user a --move notes --to c --keep-off",
			"meant for another profile? lola profile --user a --move notes --to base"}))
		r := runHint(command(hints[1]) + " -n")
		Expect(r.code).To(Equal(0), r.err)
		r = runHint(command(hints[0]))
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(p.UserProfiles, "c.yml"))).To(ContainSubstring("name: notes"))
		Expect(filepath.Join(p.Home, ".claude", "commands", "other.md")).NotTo(BeAnExistingFile())
		Expect(userNotes()).NotTo(BeAnExistingFile())
	})

	It("after a write to a project profile that is off, no move is hinted", func() {
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n"})
		writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\n"})
		r := lola("install", notes, "--project", "--profile", "team", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out + r.err).To(ContainSubstring("saved to profile team; it is not on in this project → "))
		Expect(moveHints(r.out + r.err)).To(BeEmpty())
	})

	It("keeps the run's output flags, and goes with --json into the notices", func() {
		r := lola("install", notes, "--project", "-t", "claude-code", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var doc struct {
			Notices []struct{ Level, Text string } `json:"notices"`
		}
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		Expect(doc.Notices).To(ContainElement(HaveField("Text", "meant for every project? lola profile --user --move notes --to base --json")))
	})
	It("names both scopes' context when a move takes the entry from the other scope, one when it does not (spec 2026-10-29 §3.8)", func() {
		Expect(lola("install", notes, "--project", "-t", "claude-code").code).To(Equal(0))
		r := lola("profile", "--user", "--move", "notes", "--to", "base", "-y", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(HavePrefix("user scope: default\nproject " + proj + ": project.yml\n"))
		r = lola("profile", "--user", "--move", "notes", "--to", "base", "-y", "-n", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var doc struct {
			Context []map[string]any `json:"context"`
		}
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed(), r.out)
		Expect(doc.Context).To(HaveLen(2))
		Expect(doc.Context[1]).To(And(HaveKeyWithValue("scope", "project"), HaveKeyWithValue("writes_to", BeNil())))

		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n"})
		r = lola("profile", "--project", "--move", "notes", "--to", "me", "--keep-off", "-y", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(strings.Count(r.err, "scope: ")+strings.Count(r.err, "project "+proj+": ")).To(Equal(1), "a move within the project names it alone")
	})

	Describe("Pick-P1-a: a move out of project.yml carries the module's selections.yaml picks", func() {
		var alpha string
		BeforeEach(func() {
			alpha = filepath.Join(tmp, "src", "alpha")
			writeTree(alpha, map[string]string{"lola.yml": "name: alpha\n", "commands/hello.md": "H\n",
				"skills/a1/SKILL.md": "---\nname: a1\ndescription: A1\n---\nA1\n", "skills/a2/SKILL.md": "---\nname: a2\ndescription: A2\n---\nA2\n",
				"skills/a3/SKILL.md": "---\nname: a3\ndescription: A3\n---\nA3\n"})
		})
		// installedFiles is every file under the project's .claude, relative to it.
		installedFiles := func() []string {
			GinkgoHelper()
			out := []string{}
			Expect(filepath.WalkDir(filepath.Join(proj, ".claude"), func(path string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					rel, _ := filepath.Rel(proj, path)
					out = append(out, filepath.ToSlash(rel))
				}
				return err
			})).To(Succeed())
			return out
		}
		// unchanged asserts a move's output: the carried picks on its write line, and no file
		// installed or removed, no warning about the selections.yaml entry it took them from.
		unchanged := func(r result) {
			GinkgoHelper()
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out + r.err).To(ContainSubstring("picks: carried from selections.yaml"))
			Expect(r.out + r.err).NotTo(Or(ContainSubstring("create "), ContainSubstring("delete "), ContainSubstring("is ignored")))
		}

		It("Pick-P1-a: the keep-it-to-yourself hint, run as printed, keeps the --only pick and changes no installed file", func() {
			hints := install(alpha, "--project", "-t", "claude-code", "--only", "skill/a1")
			Expect(hints[0]).To(Equal("keep it to yourself: lola profile --project --move alpha --to mine --switch"))
			Expect(installedFiles()).To(Equal([]string{".claude/skills/a1/SKILL.md"}))

			r := runHint(command(hints[0]))
			unchanged(r)
			Expect(installedFiles()).To(Equal([]string{".claude/skills/a1/SKILL.md"}))
			Expect(read(filepath.Join(p.UserProfiles, "mine.yml"))).To(ContainSubstring("    skip: [skill/a2, skill/a3, command/hello]\n"))
			Expect(read(p.Selections)).NotTo(ContainSubstring("alpha"))
			Expect(lola("sync", "--project").out).NotTo(ContainSubstring("create "), "the next sync installs nothing either")
		})

		It("Pick-P1-a: --to project:team keeps the pick in the project profile and changes no installed file", func() {
			team := filepath.Join(proj, ".lola", "profiles", "team.yml")
			writeTree(proj, map[string]string{".lola/profiles/team.yml": "version: 1\n"})
			install(alpha, "--project", "-t", "claude-code", "--only", "skill/a1")

			r := lola("profile", "--project", "--move", "alpha", "--to", "project:team", "--switch", "-y")
			unchanged(r)
			Expect(installedFiles()).To(Equal([]string{".claude/skills/a1/SKILL.md"}))
			Expect(read(team)).To(ContainSubstring("    skip: [skill/a2, skill/a3, command/hello]\n"))
			Expect(read(p.Selections)).NotTo(ContainSubstring("alpha"))
		})

		It("Pick-P1-a: the every-project hint, run as printed, carries the pick into default at user scope", func() {
			hints := install(alpha, "--project", "-t", "claude-code", "--only", "skill/a1")
			Expect(hints[len(hints)-1]).To(Equal("meant for every project? lola profile --user --move alpha --to base"))

			r := runConfirmed(command(hints[len(hints)-1]))
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out + r.err).To(ContainSubstring("picks: carried from selections.yaml"))
			Expect(r.out + r.err).NotTo(ContainSubstring("is ignored"))
			Expect(read(filepath.Join(p.UserProfiles, "default.yml"))).To(ContainSubstring("    skip: [skill/a2, skill/a3, command/hello]\n"))
			Expect(read(p.Selections)).NotTo(ContainSubstring("alpha"))
			Expect(filepath.Join(p.Home, ".claude", "skills", "a1", "SKILL.md")).To(BeAnExistingFile())
			Expect(filepath.Join(p.Home, ".claude", "skills", "a2", "SKILL.md")).NotTo(BeAnExistingFile())
			Expect(filepath.Join(p.Home, ".claude", "commands", "hello.md")).NotTo(BeAnExistingFile())
		})

		It("Pick-P1-a: an exact only: list and an include carry as the profile entry's only: list; selections.yaml keeps its other entries as written", func() {
			install(alpha, "--project", "-t", "claude-code", "--only", "skill/a1", "--exact")
			install("alpha", "--project", "--unskip", "skill/a2")
			Expect(read(p.Selections)).To(ContainSubstring("only: [skill/a1]"))
			Expect(read(p.Selections)).To(ContainSubstring("include: [skill/a2]"))
			// Another project's entry, with the user's comment, stays byte for byte.
			other := "  # kept by hand\n  /elsewhere/proj:\n    beta: {skip: [skill/b1]}\n"
			sel := strings.Replace(read(p.Selections), "projects:\n", "projects:\n"+other, 1)
			Expect(os.WriteFile(p.Selections, []byte(sel), 0o600)).To(Succeed())
			Expect(installedFiles()).To(Equal([]string{".claude/skills/a1/SKILL.md", ".claude/skills/a2/SKILL.md"}))

			r := lola("profile", "--project", "--move", "alpha", "--to", "mine", "--switch", "-y", "-n")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out + r.err).To(ContainSubstring("picks: carried from selections.yaml"))
			Expect(read(p.Selections)).To(Equal(sel), "-n writes nothing")
			Expect(filepath.Join(p.UserProfiles, "mine.yml")).NotTo(BeAnExistingFile())

			r = lola("profile", "--project", "--move", "alpha", "--to", "mine", "--switch", "-y")
			unchanged(r)
			Expect(installedFiles()).To(Equal([]string{".claude/skills/a1/SKILL.md", ".claude/skills/a2/SKILL.md"}))
			Expect(read(filepath.Join(p.UserProfiles, "mine.yml"))).To(ContainSubstring("    only: [skill/a1, skill/a2]\n"))
			Expect(read(p.Selections)).To(ContainSubstring("projects:\n" + other))
			Expect(read(p.Selections)).NotTo(ContainSubstring("alpha"))
		})

		It("Pick-P1-a: a glob skip carries as the profile entry's skip:", func() {
			hints := install(alpha, "--project", "-t", "claude-code", "--skip", "skill/*")
			Expect(hints[0]).To(Equal("keep it to yourself: lola profile --project --move alpha --to mine --switch"))
			unchanged(runHint(command(hints[0])))
			Expect(installedFiles()).To(Equal([]string{".claude/commands/hello.md"}))
			Expect(read(filepath.Join(p.UserProfiles, "mine.yml"))).To(ContainSubstring("    skip: [skill/*]\n"))
		})

		// refused asserts that the move of alpha into mine is refused with want and its hint, and that
		// neither it nor its dry run writes anything; then that the hint, run as printed, moves it,
		// leaving files.
		refused := func(want string, files []string) {
			GinkgoHelper()
			sel := read(p.Selections)
			before := installedFiles()
			r := lola("profile", "--project", "--move", "alpha", "--to", "mine", "--switch", "-y")
			Expect(r.code).To(Equal(1), r.out)
			hint := "lola profile --move alpha --project --switch --to mine --force"
			Expect(r.err).To(Equal("project "+proj+": project.yml\nerror: "+want+"\n  hint: move it anyway: "+hint+"\n"), r.err)
			r = lola("profile", "--project", "--move", "alpha", "--to", "mine", "--switch", "-y", "-n")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out + r.err).To(ContainSubstring("the move refuses: " + want + " → move it anyway: lola profile --dry-run --move alpha --project --switch --to mine --force"))
			Expect(read(p.Selections)).To(Equal(sel))
			Expect(installedFiles()).To(Equal(before))
			Expect(filepath.Join(p.UserProfiles, "mine.yml")).NotTo(BeAnExistingFile())

			r = runHint(hint)
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out + r.err).To(ContainSubstring(want + "; --force moves it all the same"))
			Expect(installedFiles()).To(Equal(files))
		}

		It("Pick-P1-a: an include over a glob skip, with no only: list, refuses the move and hints none after the install", func() {
			Expect(install(alpha, "--project", "-t", "claude-code", "--skip", "skill/*", "--unskip", "skill/a1")).To(BeEmpty(),
				"no move hint: the move would be refused")
			Expect(read(p.Selections)).To(ContainSubstring("include: [skill/a1]"))
			refused("alpha's picks in selections.yaml projects["+proj+"] cannot move into profile mine as they are: include: skill/a1 cannot travel: "+
				"a profile entry has no include: to override the skip skill/*; the move would remove skill/a1 here",
				[]string{".claude/commands/hello.md"})
		})

		It("Pick-P1-a: an include over a global skip refuses the move and hints none after the install", func() {
			writeTree(filepath.Dir(p.Selections), map[string]string{"selections.yaml": "version: 1\nglobal:\n  alpha: {skip: [skill/a1]}\n"})
			Expect(install(alpha, "--project", "-t", "claude-code", "--unskip", "skill/a1")).To(BeEmpty(), "no move hint: the move would be refused")
			Expect(read(p.Selections)).To(ContainSubstring("include: [skill/a1]"))
			refused("alpha's picks in selections.yaml projects["+proj+"] cannot move into profile mine as they are: include: skill/a1 cannot travel: "+
				"a profile entry has no include:, and the global skip skill/a1 applies to it; the move would remove skill/a1 here",
				[]string{".claude/commands/hello.md", ".claude/skills/a2/SKILL.md", ".claude/skills/a3/SKILL.md"})
		})

		It("Pick-P1-a: a move of two modules whose picks cannot travel names both", func() {
			beta := filepath.Join(tmp, "src", "beta")
			writeTree(beta, map[string]string{"lola.yml": "name: beta\n", "skills/b1/SKILL.md": "---\nname: b1\ndescription: B1\n---\nB1\n",
				"skills/b2/SKILL.md": "---\nname: b2\ndescription: B2\n---\nB2\n"})
			writeTree(filepath.Dir(p.Selections), map[string]string{"selections.yaml": "version: 1\nglobal:\n  alpha: {skip: [skill/a1]}\n  beta: {skip: [skill/b1]}\n"})
			install(alpha, beta, "--project", "-t", "claude-code", "--unskip", "alpha:skill/a1", "--unskip", "beta:skill/b1")
			r := lola("profile", "--project", "--move", "alpha,beta", "--to", "mine", "--switch", "-y")
			Expect(r.code).To(Equal(1), r.out)
			Expect(r.err).To(ContainSubstring("alpha's picks in selections.yaml projects[" + proj + "] cannot move into profile mine as they are: " +
				"include: skill/a1 cannot travel: a profile entry has no include:, and the global skip skill/a1 applies to it; the move would remove skill/a1 here; " +
				"beta's picks in selections.yaml projects[" + proj + "] cannot move into profile mine as they are: " +
				"include: skill/b1 cannot travel: a profile entry has no include:, and the global skip skill/b1 applies to it; the move would remove skill/b1 here\n"))
		})

		It("Pick-P2-a: a move whose selections.yaml write fails names the path key the entry is still under in a clone", func() {
			install(alpha, "--project", "-t", "claude-code", "--only", "skill/a1")
			for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://example.com/o/r"}} {
				out, err := exec.Command("git", append([]string{"-C", proj}, args...)...).CombinedOutput()
				Expect(err).NotTo(HaveOccurred(), string(out))
			}
			lolaDir := filepath.Dir(p.Selections)
			Expect(os.MkdirAll(p.UserProfiles, 0o700)).To(Succeed())
			Expect(os.Chmod(lolaDir, 0o500)).To(Succeed())
			DeferCleanup(os.Chmod, lolaDir, os.FileMode(0o700))

			r := lola("profile", "--user", "--move", "alpha", "--to", "base", "-y")
			Expect(os.Chmod(lolaDir, 0o700)).To(Succeed())
			Expect(r.code).To(Equal(1), r.out)
			Expect(r.err).To(ContainSubstring("but lola could not remove alpha from selections.yaml's projects[" + proj + "]: "))
			Expect(r.err).To(ContainSubstring("  hint: no lola command removes it: in " + p.Selections + ", delete alpha under projects: " + proj +
				":; until then lola warns that it is ignored\n"))
		})

		It("Pick-P1-a: a move between scopes whose selections.yaml write fails says the move is done, and hints no rerun", func() {
			install(alpha, "--project", "-t", "claude-code", "--only", "skill/a1")
			lolaDir := filepath.Dir(p.Selections)
			Expect(os.MkdirAll(p.UserProfiles, 0o700)).To(Succeed())
			Expect(os.Chmod(lolaDir, 0o500)).To(Succeed())
			DeferCleanup(os.Chmod, lolaDir, os.FileMode(0o700))

			r := lola("profile", "--user", "--move", "alpha", "--to", "base", "-y")
			Expect(os.Chmod(lolaDir, 0o700)).To(Succeed())
			Expect(r.code).To(Equal(1), r.out)
			Expect(r.err).To(ContainSubstring("error: alpha moved: the profile entry holds the picks, and project.yml and lock.json are written, " +
				"but lola could not remove alpha from selections.yaml's projects[" + proj + "]: "))
			Expect(r.err).To(ContainSubstring("  hint: no lola command removes it: in " + p.Selections + ", delete alpha under projects: " + proj +
				":; until then lola warns that it is ignored\n"))
			Expect(r.err).NotTo(Or(ContainSubstring("still in"), ContainSubstring("finish the move"), ContainSubstring("lola profile")))
			Expect(read(filepath.Join(p.UserProfiles, "default.yml"))).To(ContainSubstring("    skip: [skill/a2, skill/a3, command/hello]\n"))
			Expect(read(filepath.Join(proj, ".lola", "project.yml"))).NotTo(ContainSubstring("alpha"))
			Expect(read(p.Selections)).To(ContainSubstring("alpha"))
		})
	})
})
