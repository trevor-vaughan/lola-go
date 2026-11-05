package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

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
			"meant for another profile? lola profile --project --move notes --to me --switch -y",
			"meant for every project? lola profile --user --move notes --to base -y"}))
		r := runHint(command(hints[0]))
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(p.UserProfiles, "me.yml"))).To(ContainSubstring("name: notes"))
		Expect(read(filepath.Join(proj, ".lola", "project.yml"))).NotTo(ContainSubstring("notes"))
		Expect(projectNotes()).To(BeAnExistingFile())
	})

	It("after a project.yml write, the move into every project runs as printed, from here and with -C", func() {
		hints := install(notes, "--project", "-t", "claude-code")
		Expect(hints).To(ContainElement("meant for every project? lola profile --user --move notes --to base -y"))
		r := runHint(command(hints[len(hints)-1]))
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(p.UserProfiles, "default.yml"))).To(ContainSubstring("name: notes"))
		Expect(read(filepath.Join(proj, ".lola", "project.yml"))).NotTo(ContainSubstring("notes"))
		Expect(userNotes()).To(BeAnExistingFile())
		Expect(projectNotes()).NotTo(BeAnExistingFile())

		other := filepath.Join(tmp, "src", "other")
		writeTree(other, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "O\n"})
		GinkgoT().Chdir(tmp)
		hints = install(other, "-C", proj, "-t", "claude-code")
		Expect(hints).To(ContainElement("meant for every project? lola profile --user --move other --to base -y -C " + proj))
		r = runHint(command(hints[len(hints)-1]))
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(p.UserProfiles, "default.yml"))).To(ContainSubstring("name: other"))
		Expect(read(filepath.Join(proj, ".lola", "project.yml"))).NotTo(ContainSubstring("other"))
	})

	It("with no profile of yours, keep it to yourself creates mine, switches to it and keeps the module installed", func() {
		hints := install(notes, "--project", "-t", "claude-code")
		Expect(hints[0]).To(Equal("keep it to yourself: lola profile --project --move notes --to mine --switch -y"))
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
			"meant for another profile? lola profile --user --move notes --to a --switch -y",
			"meant for this project only? lola profile --project --move notes --to user:a --switch -y"}))
		r := runHint(command(hints[1]))
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
			"meant for another profile (switches a off, removing its modules)? lola profile --user --move notes --to b --switch -y",
			"meant for another profile? lola profile --user --move notes --to base"}))
		r := runHint(command(hints[0]))
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(p.UserProfiles, "b.yml"))).To(ContainSubstring("name: notes"))
		Expect(read(filepath.Join(p.UserProfiles, "a.yml"))).NotTo(ContainSubstring("notes"))
		Expect(userNotes()).To(BeAnExistingFile())
		Expect(keptFile()).NotTo(BeAnExistingFile(), "what the hint said: a is off, its modules removed")
	})

	It("from the profile on at user scope, the move into the base turns nothing off, and runs as printed", func() {
		hints := onAtUser()
		r := runHint(command(hints[1]))
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
			"keep it to yourself: lola profile --project --move notes,other --to mine --switch -y",
			"meant for every project? lola profile --user --move notes,other --to base -y"}))
		r := runHint(command(hints[1]))
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(p.UserProfiles, "default.yml"))).To(And(ContainSubstring("name: notes"), ContainSubstring("name: other")))
		Expect(read(filepath.Join(proj, ".lola", "project.yml"))).NotTo(Or(ContainSubstring("notes"), ContainSubstring("other")))
		Expect(filepath.Join(p.Home, ".claude", "commands", "other.md")).To(BeAnExistingFile())
		Expect(userNotes()).To(BeAnExistingFile())
	})

	It("without a profile of yours, the two-step into this project runs as printed", func() {
		hints := install(notes, "--user", "-t", "claude-code")
		Expect(hints).To(Equal([]string{"meant for this project only? lola uninstall --user notes -y, then lola install --project notes -t claude-code"}))
		first, second, _ := strings.Cut(command(hints[0]), ", then ")
		r := runHint(first)
		Expect(r.code).To(Equal(0), r.err)
		r = runHint(second)
		Expect(r.code).To(Equal(0), r.err)
		Expect(userNotes()).NotTo(BeAnExistingFile())
		Expect(projectNotes()).To(BeAnExistingFile())
		Expect(read(filepath.Join(proj, ".lola", "project.yml"))).To(ContainSubstring("name: notes"))
	})

	It("qualifies a profile of yours a project profile shares the name of, and the move runs as printed", func() {
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n"})
		writeTree(proj, map[string]string{".lola/profiles/me.yml": "version: 1\n"})
		hints := install(notes, "--project", "-t", "claude-code")
		Expect(hints[0]).To(Equal("meant for another profile? lola profile --project --move notes --to user:me --switch -y"))
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
			"keep it to yourself: lola profile --project --move notes --to mine --switch -y",
			"meant for every project? lola profile --user --move notes --to base -y"}))
		team := filepath.Join(proj, ".lola", "profiles", "team.yml")
		r = runHint(command(hints[0]))
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(p.UserProfiles, "mine.yml"))).To(ContainSubstring("name: notes"))
		Expect(read(team)).NotTo(ContainSubstring("notes"))
		Expect(projectNotes()).To(BeAnExistingFile())

		other := filepath.Join(tmp, "src", "other")
		writeTree(other, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "O\n"})
		Expect(lola("profile", "team", "--switch", "--project", "-y").code).To(Equal(0))
		hints = install(other, "--project", "-t", "claude-code")
		Expect(hints).To(Equal([]string{
			"meant for another profile? lola profile --project --move other --to mine --switch -y",
			"meant for every project? lola profile --user --move other --to base -y"}),
			"a switch to mine installs notes again, which asks nothing")
		r = runHint(command(hints[1]))
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
		Expect(hints).To(Equal([]string{"meant for every project? lola profile --user --move notes --to base -y"}))
		r = runHint(command(hints[0]))
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
		Expect(doc.Notices).To(ContainElement(HaveField("Text", "meant for every project? lola profile --user --move notes --to base -y --json")))
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
})
