package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("lola profile --move", func() {
	It("moves a module from the base into the profile on, naming both writes", func() {
		tmp := testenv.Isolate()
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		notes := filepath.Join(tmp, "notes")
		writeTree(notes, map[string]string{"lola.yml": "name: notes\n", "commands/notes.md": "N\n"})
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + notes + "}\n",
			"me.yml": "version: 1\n"})
		Expect(lola("sync", "--user", "-y").code).To(Equal(0))
		Expect(lola("profile", "me", "--switch", "--user", "-y").code).To(Equal(0))
		r := lola("profile", "--move", "notes", "--to", "me", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("✓ removed notes from default (base)  (" + filepath.Join(p.UserProfiles, "default.yml") + ")\n" +
			"✓ added notes to profile me  (" + filepath.Join(p.UserProfiles, "me.yml") + ")\n"))
		r = lola("profile", "--mv", "notes", "--to", "base", "--user", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var v struct {
			Edits []map[string]any `json:"edits"`
		}
		Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed())
		Expect(v.Edits).To(HaveExactElements(HaveKeyWithValue("action", "removed"), HaveKeyWithValue("action", "added")),
			"a move is two edits, never one moved")
	})

	It("refuses --to, --keep-off and --switch beside another action, and --move without --to", func() {
		testenv.Isolate()
		Expect(lola("profile", "--to", "me").err).To(ContainSubstring("--to applies to --move and --split only"))
		Expect(lola("profile", "me", "--on", "--keep-off").err).To(ContainSubstring("--keep-off applies to --move only"))
		Expect(lola("profile", "me", "--on", "--switch").err).To(ContainSubstring("--on and --switch cannot be combined"))
		r := lola("profile", "--move", "notes", "--user")
		Expect(r.code).NotTo(Equal(0))
		Expect(r.err).To(ContainSubstring("--move needs --to"))
		Expect(r.err).NotTo(ContainSubstring("hint:"), "no layer lists notes: no --to takes it")
		proj := GinkgoT().TempDir()
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\n"})
		r = lola("profile", "--move", "notes", "--to", "default", "-C", proj)
		Expect(r.err).To(HaveSuffix("error: default is user scope's base; this project's base is project.yml\n"), "nothing to move: no hint")
		r = lola("profile", "--move", "notes", "--to", "me", "--switch", "--keep-off", "--user")
		Expect(r.code).NotTo(Equal(0))
		Expect(r.err).To(ContainSubstring("--switch and --keep-off cannot be combined; pick one\n" +
			"  hint: keep --switch: lola profile --move notes --switch --to me --user\n" +
			"  hint: keep --keep-off: lola profile --keep-off --move notes --to me --user\n"))
	})

	// run runs a hint line's command as printed.
	run := func(hint string) result {
		GinkgoHelper()
		f := strings.Fields(hint)
		Expect(f[0]).To(Equal("lola"), hint)
		return lola(f[1:]...)
	}

	It("hints only the --to a refused move takes, and each hint runs as printed", func() {
		tmp := testenv.Isolate()
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		notes, proj := filepath.Join(tmp, "notes"), filepath.Join(tmp, "proj")
		writeTree(notes, map[string]string{"lola.yml": "name: notes\n", "commands/notes.md": "N\n"})
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n"})
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + notes + "}\n",
			"me.yml": "version: 1\ntargets: [claude-code]\n"})
		Expect(lola("sync", "--user", "-y").code).To(Equal(0))
		Expect(lola("profile", "me", "--switch", "--user", "-y").code).To(Equal(0))

		r := lola("profile", "--move", "notes", "--user")
		Expect(r.err).To(HaveSuffix("error: --move needs --to, the profile the entries move to\n  hint: lola profile --move notes --user --to me\n"))
		r = run("lola profile --move notes --user --to me")
		Expect(r.code).To(Equal(0), r.err)

		// notes is in me now, which is on in the project too.
		Expect(lola("sync", "--root", proj, "--trust=entries", "-y").code).To(Equal(0), "the project's own targets, trusted")
		Expect(lola("profile", "me", "--switch", "--root", proj, "-y").code).To(Equal(0))
		r = lola("profile", "--move", "notes", "--to", "default", "--root", proj)
		Expect(r.err).To(HaveSuffix("error: default is user scope's base; this project's base is project.yml\n" +
			"  hint: lola profile --move notes --root " + proj + " --to base\n  hint: lola profile --move notes --to default --user\n"))
		r = lola("profile", "--move", "notes", "--to", "default", "--root", proj, "-y")
		Expect(r.err).To(HaveSuffix("  hint: lola profile --move notes --to default --user\n"),
			"R-8: the -y given at the project answers nothing at user scope")
		Expect(r.err).NotTo(ContainSubstring("--yes"))
		me, err := os.ReadFile(filepath.Join(p.UserProfiles, "me.yml"))
		Expect(err).NotTo(HaveOccurred())
		r = run("lola profile --move notes --root " + proj + " --to base")
		Expect(r.code).To(Equal(0), r.err)
		// The hints are alternatives (me.yml is one file at both scopes): the other runs from where the refusal was.
		Expect(os.WriteFile(filepath.Join(p.UserProfiles, "me.yml"), me, 0o600)).To(Succeed())
		r = run("lola profile --move notes --to default --user")
		Expect(r.code).To(Equal(0), r.err)
	})

	// switchedTo is the profile a move's JSON says its sync turns on ("" for none).
	switchedTo := func(out string) string {
		GinkgoHelper()
		var v struct {
			Syncs []struct {
				Profile *string `json:"profile"`
			} `json:"syncs"`
		}
		Expect(json.Unmarshal([]byte(out), &v)).To(Succeed())
		Expect(v.Syncs).NotTo(BeEmpty())
		if v.Syncs[0].Profile == nil {
			return ""
		}
		return *v.Syncs[0].Profile
	}

	It("refuses a move into a profile that is off, off a terminal, hinting --switch and --keep-off -y with the run's flags", func() {
		tmp := testenv.Isolate()
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		notes := filepath.Join(tmp, "notes")
		writeTree(notes, map[string]string{"lola.yml": "name: notes\n", "commands/notes.md": "N\n"})
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + notes + "}\n",
			"me.yml": "version: 1\n"})
		Expect(lola("sync", "--user", "-y").code).To(Equal(0))
		r := lola("profile", "--move", "notes", "--to", "me", "--user")
		Expect(r.code).NotTo(Equal(0))
		Expect(r.err).To(HaveSuffix("error: me is not on at user scope; lola needs to know whether to switch to it, and there is no terminal to ask on\n" +
			"  hint: lola profile --move notes --user --to me --switch\n" +
			"  hint: lola profile --move notes --user --to me --keep-off -y\n"))
		// The hints are alternatives: each runs as printed from where the refusal was.
		files := map[string]string{}
		for _, n := range []string{"default.yml", "me.yml"} {
			b, err := os.ReadFile(filepath.Join(p.UserProfiles, n))
			Expect(err).NotTo(HaveOccurred())
			files[n] = string(b)
		}
		led, err := os.ReadFile(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())

		turned := "Profile user:me (" + filepath.Join(p.UserProfiles, "me.yml") + ") → on\n"
		r = lola("profile", "--move", "notes", "--user", "--to", "me", "--switch", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(turned), "a dry run shows the switch")
		r = lola("profile", "--move", "notes", "--user", "--to", "me", "--switch", "-n", "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(switchedTo(r.out)).To(Equal("user:me"), "its JSON too")
		r = run("lola profile --move notes --user --to me --switch")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(turned), "as the run does")
		Expect(r.out).To(HaveSuffix("now: user scope: profile me over default\n"))
		Expect(filepath.Join(p.Home, ".claude", "commands", "notes.md")).To(BeAnExistingFile(), "the switch keeps notes installed")

		writeTree(p.UserProfiles, files)
		Expect(os.WriteFile(p.UserLedger, led, 0o600)).To(Succeed())
		r = run("lola profile --move notes --user --to me --keep-off -y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).NotTo(ContainSubstring("now:"), "nothing was switched")
		Expect(filepath.Join(p.Home, ".claude", "commands", "notes.md")).NotTo(BeAnExistingFile(), "kept off, notes is removed here")
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "me.yml"))).To(ContainSubstring("name: notes"))
	})

	It("creates the profile --to names when none has the name, and says so", func() {
		tmp := testenv.Isolate()
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		notes := filepath.Join(tmp, "notes")
		writeTree(notes, map[string]string{"lola.yml": "name: notes\n", "commands/notes.md": "N\n"})
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + notes + "}\n"})
		Expect(lola("sync", "--user", "-y").code).To(Equal(0))
		mine := filepath.Join(p.UserProfiles, "mine.yml")
		turned := "Profile user:mine (" + mine + ") → on\n"
		r := lola("profile", "--move", "notes", "--to", "mine", "--switch", "--user", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(turned), "a dry run shows the switch to the profile it would create")
		Expect(r.out+r.err).NotTo(ContainSubstring("no longer exists"), "the profile it would create is not gone")
		r = lola("profile", "--move", "notes", "--to", "mine", "--switch", "--user", "-n", "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(switchedTo(r.out)).To(Equal("user:mine"), "its JSON too")
		Expect(mine).NotTo(BeAnExistingFile())
		r = lola("profile", "--move", "notes", "--to", "mine", "--switch", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(turned))
		Expect(r.out).To(HavePrefix("✓ new profile mine  (" + mine + ")\n"))
		Expect(r.out).To(HaveSuffix("now: user scope: profile mine over default\n"))
		Expect(os.ReadFile(mine)).To(ContainSubstring("name: notes"))
	})

	It("says a --switch to the profile on is already on, and refuses --keep-off with it, hinting the run without", func() {
		tmp := testenv.Isolate()
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		notes, todo := filepath.Join(tmp, "notes"), filepath.Join(tmp, "todo")
		writeTree(notes, map[string]string{"lola.yml": "name: notes\n", "commands/notes.md": "N\n"})
		writeTree(todo, map[string]string{"lola.yml": "name: todo\n", "commands/todo.md": "T\n"})
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + notes +
			"}\n  - {name: todo, source: " + todo + "}\n", "me.yml": "version: 1\n"})
		Expect(lola("sync", "--user", "-y").code).To(Equal(0))
		Expect(lola("profile", "me", "--switch", "--user", "-y").code).To(Equal(0))

		r := lola("profile", "--move", "todo", "--to", "me", "--keep-off", "--user", "-y")
		Expect(r.code).NotTo(Equal(0))
		Expect(r.err).To(HaveSuffix("error: me is on at user scope already; --keep-off applies to a profile that is off\n" +
			"  hint: drop it: lola profile --move todo --to me --user --yes\n"))
		r = run("lola profile --move todo --to me --user --yes")
		Expect(r.code).To(Equal(0), r.err)

		r = lola("profile", "--move", "notes", "--to", "me", "--switch", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("user:me is already on"))
		Expect(r.out).NotTo(ContainSubstring("→ on"))
	})

	It("refuses --keep-off without --move, and its hint runs", func() {
		testenv.Isolate()
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n"})
		r := lola("profile", "me", "--switch", "--keep-off", "--user", "-y")
		Expect(r.code).NotTo(Equal(0))
		Expect(r.err).To(HaveSuffix("error: --keep-off applies to --move only\n  hint: drop it: lola profile me --switch --user --yes\n"))
		r = run("lola profile me --switch --user --yes")
		Expect(r.code).To(Equal(0), r.err)
	})

	It("reads a name after --move's value as the source, and hints it as a module", func() {
		tmp := testenv.Isolate()
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		notes, todo := filepath.Join(tmp, "notes"), filepath.Join(tmp, "todo")
		writeTree(notes, map[string]string{"lola.yml": "name: notes\n", "commands/notes.md": "N\n"})
		writeTree(todo, map[string]string{"lola.yml": "name: todo\n", "commands/todo.md": "T\n"})
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + notes +
			"}\n  - {name: todo, source: " + todo + "}\n", "me.yml": "version: 1\n"})
		Expect(lola("sync", "--user", "-y").code).To(Equal(0))
		Expect(lola("profile", "me", "--switch", "--user", "-y").code).To(Equal(0))
		r := lola("profile", "--move", "notes", "todo", "--to", "me", "--user")
		Expect(r.code).NotTo(Equal(0))
		Expect(r.err).To(ContainSubstring("no profile todo for user scope\n  hint: move them all: lola profile --to me --user --move notes,todo\n"))
		hint := strings.Fields("profile --to me --user --move notes,todo")
		r = lola(hint...)
		Expect(r.code).To(Equal(0), r.err)
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "me.yml"))).To(And(ContainSubstring("name: notes"), ContainSubstring("name: todo")))
	})

	It("refuses a move out of the remote profile on, and its hint turns it off", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		work := filepath.Join(tmp, "work")
		writeTree(work, map[string]string{"lola.yml": "name: notes\n", "commands/notes.md": "N\n"})
		gitIn(work, "init", "-q", "-b", "main")
		gitIn(work, "add", "-A")
		gitIn(work, "commit", "-q", "-m", "one")
		gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "notes.git"))
		url := testenv.ServeGitTLS(filepath.Join(tmp, "srv"), nil) + "/notes.git"
		other := p
		other.Store, other.StoreLock, other.Staging = filepath.Join(tmp, "pin"), filepath.Join(tmp, "pin.lock"), filepath.Join(tmp, "pin-staging")
		v, err := engine.New(other).Fetch(context.Background(), source.Spec{Kind: source.KindGit, Location: url})
		Expect(err).NotTo(HaveOccurred())
		content := "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + url + ", commit: " + gitIn(work, "rev-parse", "HEAD") +
			", digest: '" + v.Meta.Digest + "'}\n"
		dir := filepath.Join(p.RemoteProfiles, "team")
		Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
		b, err := json.Marshal(profile.Origin{Version: 1, Source: "https://example.com/profiles.git", Commit: strings.Repeat("c", 40),
			Digest: profile.Digest([]byte(content))})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(dir, "source.json"), b, 0o600)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "profile.yml"), []byte(content), 0o600)).To(Succeed())
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n"})
		r := lola("profile", "team", "--switch", "--user", "-y")
		Expect(r.code).To(Equal(0), r.err)

		r = lola("profile", "--move", "notes", "--to", "base", "--user")
		Expect(r.code).NotTo(Equal(0))
		Expect(r.err).To(ContainSubstring("notes comes from remote:team, which is read-only; lola does not edit remote profiles\n" +
			"  hint: turn it off: lola profile --user --off -y\n" +
			"  hint: or switch to a copy of yours: lola profile --user team-mine --switch --new --from remote:team -y\n"))
		r = lola(strings.Fields("profile --user --off -y")...) // the hint as printed
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("Profile remote:team"))

		Expect(lola("profile", "team", "--switch", "--user", "-y").code).To(Equal(0))
		r = lola(strings.Fields("profile --user team-mine --switch --new --from remote:team -y")...) // the other hint as printed
		Expect(r.code).To(Equal(0), r.err)
		r = lola("profile", "--move", "notes", "--to", "base", "--user", "-y")
		Expect(r.code).To(Equal(0), "the copy is yours to edit: %s", r.err)

		// A bundle's remote member goes off with its bundle, or out of it.
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\nbundle: [remote:team]\n", "default.yml": "version: 1\n"})
		Expect(lola("profile", "me", "--switch", "--user", "-y").code).To(Equal(0))
		r = lola("profile", "--move", "notes", "--to", "base", "--user")
		Expect(r.code).NotTo(Equal(0))
		Expect(r.err).To(HaveSuffix("error: notes comes from remote:team, which is read-only; lola does not edit remote profiles\n" +
			"  hint: turn bundle me off: lola profile --user --off -y\n" +
			"  hint: or take remote:team out of bundle me: lola profile --user me --edit --bundle '' -y\n"))
		r = lola("profile", "--user", "me", "--edit", "--bundle", "", "-y", "-n") // the hint as printed, under -n
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("would change bundle me (remote:team → none)"))
		r = lola(strings.Fields("profile --user --off -y")...) // the hint as printed
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("Profile user:me"))
	})

	Context("between user scope and a project", func() {
		var p paths.Paths
		var notes, proj string
		BeforeEach(func() {
			tmp, err := filepath.EvalSymlinks(testenv.Isolate())
			Expect(err).NotTo(HaveOccurred())
			p, err = paths.Resolve(os.Getenv)
			Expect(err).NotTo(HaveOccurred())
			proj = filepath.Join(tmp, "proj")
			// A git source: a project's folder entry never moves into your files (spec 2026-10-29 §16.2).
			work := filepath.Join(tmp, "notes")
			writeTree(work, map[string]string{"lola.yml": "name: notes\n", "commands/notes.md": "N\n"})
			gitIn(work, "init", "-q", "-b", "main")
			gitIn(work, "add", "-A")
			gitIn(work, "commit", "-q", "-m", "one")
			gitIn(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "notes.git"))
			notes = testenv.ServeGit(filepath.Join(tmp, "srv")) + "/notes.git"
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + notes + "}\n"})
			r := lola("sync", "--root", proj, "--trust=entries", "-y")
			Expect(r.code).To(Equal(0), r.err)
		})
		read := func(path string) string {
			GinkgoHelper()
			b, err := os.ReadFile(path)
			Expect(err).NotTo(HaveOccurred())
			return string(b)
		}

		It("moves a module of a project into default with --user and -C, asking about its removal there, and the hint runs", func() {
			r := lola("profile", "--move", "notes", "--to", "base", "--user", "-C", proj)
			Expect(r.code).NotTo(Equal(0))
			Expect(r.err).To(HaveSuffix("error: lola profile needs your confirmation to remove notes, and there is no terminal to ask on\n" +
				"  hint: see them: lola profile --user --move notes --to base -C " + proj + " -n\n" +
				"  hint: then confirm them: lola profile --user --move notes --to base -C " + proj + " -y\n"))
			Expect(filepath.Join(p.UserProfiles, "default.yml")).NotTo(BeAnExistingFile(), "refused before anything is written")
			r = run("lola profile --user --move notes --to base -C " + proj + " -n")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("to apply: lola profile --user --move notes --to base -C " + proj + "\n"))
			r = run("lola profile --user --move notes --to base -C " + proj + " -y")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(HavePrefix("✓ removed notes from project.yml (base)\n" +
				"✓ added notes to default (base)  (" + filepath.Join(p.UserProfiles, "default.yml") + ", new)\nuser scope:\n"))
			Expect(r.out).To(MatchRegexp(`(?s)\nuser scope:\n.*create claude-code .claude/commands/notes.md.*\nproject `+regexp.QuoteMeta(proj)+
				`:\n.*delete claude-code .claude/commands/notes.md.*✓ removed notes\n$`), "each scope's sync, named")
			Expect(read(filepath.Join(p.UserProfiles, "default.yml"))).To(ContainSubstring("name: notes"))
			Expect(read(filepath.Join(proj, ".lola", "project.yml"))).NotTo(ContainSubstring("notes"))
			Expect(filepath.Join(p.Home, ".claude", "commands", "notes.md")).To(BeAnExistingFile())
			Expect(filepath.Join(proj, ".claude", "commands", "notes.md")).NotTo(BeAnExistingFile())
		})

		It("takes from -C with --user only a project: none at your home directory, none without .lola/", func() {
			r := lola("profile", "--move", "notes", "--to", "base", "--user", "-C", p.Home)
			Expect(r.err).To(HaveSuffix("error: -C " + p.Home + " is your home directory, where user scope installs, not a project to move from\n"))
			bare := GinkgoT().TempDir()
			r = lola("profile", "--move", "notes", "--to", "base", "--user", "-C", bare)
			Expect(r.err).To(HaveSuffix("error: -C " + bare + " has no .lola/, so it is no project to move from\n"))
			Expect(filepath.Join(bare, ".lola")).NotTo(BeAnExistingFile())
		})

		It("moves within the project when its layers list the module, though default lists it too", func() {
			writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + notes + "}\n",
				"me.yml": "version: 1\n"})
			Expect(lola("sync", "--user", "-y").code).To(Equal(0))
			Expect(lola("profile", "me", "--switch", "-C", proj, "-y").code).To(Equal(0))
			before := read(filepath.Join(p.UserProfiles, "default.yml"))
			r := lola("profile", "--move", "notes", "--to", "me", "-C", proj)
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(HavePrefix("✓ removed notes from project.yml (base)\n" +
				"✓ added notes to profile me  (" + filepath.Join(p.UserProfiles, "me.yml") + ")\n"))
			Expect(read(filepath.Join(p.UserProfiles, "default.yml"))).To(Equal(before))
		})

		It("refuses a module both scopes list when the run names both, and each hint runs as printed", func() {
			writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + notes + "}\n",
				"me.yml": "version: 1\n"})
			Expect(lola("sync", "--user", "-y").code).To(Equal(0))
			Expect(lola("profile", "me", "--switch", "--user", "-y").code).To(Equal(0))
			r := lola("profile", "--move", "notes", "--to", "me", "--user", "-C", proj)
			Expect(r.code).NotTo(Equal(0))
			Expect(r.err).To(HaveSuffix("error: notes is in default (base) at user scope and in project.yml (base) in this project; name the layer it moves from\n" +
				"  hint: lola profile --move notes --root " + proj + " --user --to me default\n" +
				"  hint: lola profile --move notes --root " + proj + " --user --to me project.yml -y\n"))
			// The hints are alternatives: each runs as printed from where the refusal was.
			saved := map[string]string{}
			for _, f := range []string{filepath.Join(p.UserProfiles, "default.yml"), filepath.Join(p.UserProfiles, "me.yml"), filepath.Join(proj, ".lola", "project.yml"), p.UserLedger} {
				saved[f] = read(f)
			}
			r = run("lola profile --move notes --root " + proj + " --user --to me default")
			Expect(r.code).To(Equal(0), r.err)
			Expect(read(filepath.Join(p.UserProfiles, "default.yml"))).NotTo(ContainSubstring("notes"))
			Expect(read(filepath.Join(proj, ".lola", "project.yml"))).To(ContainSubstring("notes"), "within user scope")

			writeTree("/", saved)
			r = run("lola profile --move notes --root " + proj + " --user --to me project.yml -y")
			Expect(r.code).To(Equal(0), r.err)
			Expect(read(filepath.Join(proj, ".lola", "project.yml"))).NotTo(ContainSubstring("notes"))
			Expect(read(filepath.Join(p.UserProfiles, "me.yml"))).To(ContainSubstring("name: notes"))
		})

		It("a failure between the two syncs reports the duplicate, and its hint finishes the move as printed", func() {
			if os.Geteuid() == 0 {
				Skip("root writes into a read-only directory")
			}
			commands := filepath.Join(proj, ".claude", "commands")
			Expect(os.Chmod(commands, 0o500)).To(Succeed())
			DeferCleanup(func() { _ = os.Chmod(commands, 0o700) })
			r := lola("profile", "--move", "notes", "--to", "base", "--user", "-C", proj, "-y")
			Expect(r.code).NotTo(Equal(0))
			Expect(r.err).To(ContainSubstring("error: notes was added to default (base) at user scope but is still in project.yml (base) in this project: "))
			hint := "lola profile --user project.yml --move notes --to base -y -C " + proj
			Expect(r.err).To(ContainSubstring("  hint: finish the move: " + hint + "\n"))
			Expect(read(filepath.Join(p.UserProfiles, "default.yml"))).To(ContainSubstring("name: notes"))
			Expect(read(filepath.Join(proj, ".lola", "project.yml"))).To(ContainSubstring("name: notes"))

			Expect(os.Chmod(commands, 0o700)).To(Succeed())
			r = run(hint)
			Expect(r.code).To(Equal(0), r.err)
			Expect(read(filepath.Join(proj, ".lola", "project.yml"))).NotTo(ContainSubstring("notes"))
			Expect(filepath.Join(commands, "notes.md")).NotTo(BeAnExistingFile())
			Expect(filepath.Join(p.Home, ".claude", "commands", "notes.md")).To(BeAnExistingFile())
		})
	})
})
