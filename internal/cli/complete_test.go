package cli_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// completions is what lola offers for args, the word being completed last: each candidate's
// line (candidate, or candidate<TAB>description) and the directive line (:<n>).
func completions(args ...string) ([]string, string) {
	GinkgoHelper()
	r := lola(append([]string{"__complete"}, args...)...)
	Expect(r.code).To(Equal(0), r.err)
	lines := strings.Split(strings.TrimSuffix(r.out, "\n"), "\n")
	return lines[:len(lines)-1], lines[len(lines)-1]
}

// Directives as cobra's __complete prints them.
const (
	dirDefault    = ":0"
	dirNoFile     = ":4"
	dirNoSpace    = ":6"  // and no file names
	dirFilterDirs = ":16" // directories only
	dirOrdered    = ":36" // in the order given, no file names
)

// agentsReaders is how -t describes agents where .agents/skills is not set up.
const agentsReaders = "skills for Codex, Gemini, OpenCode, Copilot, Cursor, Amp"

var _ = Describe("shell completion", func() {
	var tmp, proj, kitDir string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, kitDir = filepath.Join(tmp, "proj"), filepath.Join(tmp, "src", "kit")
		writeTree(kitDir, map[string]string{"lola.yml": "name: kit\ndescription: Kit\n",
			"skills/a/SKILL.md": "---\nname: a\ndescription: a skill\n---\nhi\n", "commands/b.md": "B\n"})
		writeTree(filepath.Join(tmp, "src", "other"), map[string]string{"lola.yml": "name: other\ndescription: Other\n", "commands/o.md": "O\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		r := lola("install", kitDir, "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		r = lola("cache", "add", filepath.Join(tmp, "src", "other"))
		Expect(r.code).To(Equal(0), r.err)
		writeTree(filepath.Join(tmp, "config", "lola", "profiles"), map[string]string{
			"go.yml": "version: 1\nmodules:\n  - name: other\n    source: " + filepath.Join(tmp, "src", "other") + "\n"})
	})

	It("install and check offer cached and installed modules, and file names, since a source can be a path", func() {
		for _, cmd := range []string{"install", "check"} {
			got, d := completions(cmd, "-C", proj, "")
			Expect(got).To(Equal([]string{"kit", "other"}), cmd)
			Expect(d).To(Equal(dirDefault), cmd)
			got, _ = completions(cmd, "-C", proj, "o")
			Expect(got).To(Equal([]string{"other"}), cmd)
		}
	})

	It("--skip, --only and --unskip offer the components of the modules named, completing the last of a list", func() {
		for _, flag := range []string{"--skip", "--only", "--unskip"} {
			got, d := completions("install", "kit", flag, "")
			Expect(got).To(Equal([]string{"skill/a", "command/b"}), flag)
			Expect(d).To(Equal(dirNoFile), flag)
		}
		got, _ := completions("install", "kit", "--skip", "skill/a,")
		Expect(got).To(Equal([]string{"skill/a,command/b"}), "the prefix kept, what it names left out")
		got, _ = completions("install", "kit", "other", "--only", "c")
		Expect(got).To(Equal([]string{"command/b", "command/o"}))
		got, _ = completions("install", "--skip", "")
		Expect(got).To(BeEmpty(), "no module named, nothing to offer")
		got, _ = completions("profile", "go", "--edit", "kit", "--only", "")
		Expect(got).To(Equal([]string{"skill/a", "command/b"}))
		got, _ = completions("install", kitDir, "--skip", "")
		Expect(got).To(Equal([]string{"skill/a", "command/b"}), "a local folder is read in place")
		got, _ = completions("install", filepath.Join(tmp, "nowhere"), "--skip", "")
		Expect(got).To(BeEmpty())
	})

	It("profile --edit offers the profiles first, then the modules to edit", func() {
		got, _ := completions("profile", "--edit", "")
		Expect(got).To(ContainElements("go", "kit", "other"))
		got, _ = completions("profile", "go", "--edit", "")
		Expect(got).To(ConsistOf("kit", "other"))
		got, _ = completions("profile", "go", "--edit", "k")
		Expect(got).To(Equal([]string{"kit"}))
	})

	It("tidy offers the kinds a selector starts with, and --unkeep the keep-list", func() {
		for _, args := range [][]string{{"tidy", "-C", proj, "s"}, {"tidy", "-C", proj, "--keep", "s"}} {
			got, d := completions(args...)
			Expect(got).To(Equal([]string{"skill/"}), "%v", args)
			Expect(d).To(Equal(dirNoSpace), "%v", args)
		}
		Expect(lola("tidy", "-C", proj, "--keep", "skill/notes", "-y").code).To(Equal(0))
		got, d := completions("tidy", "-C", proj, "--unkeep", "")
		Expect(got).To(Equal([]string{"skill/notes"}))
		Expect(d).To(Equal(dirNoFile))
	})

	It("every flag that takes a profile offers the profiles, and base where the scope's base is one", func() {
		for _, args := range [][]string{{"install", "kit", "--profile"}, {"uninstall", "kit", "--profile"}, {"profile", "--move", "kit", "--to"}} {
			got, d := completions(append(args, "")...)
			Expect(got).To(Equal([]string{"base", "go"}), strings.Join(args, " "))
			Expect(d).To(Equal(dirNoFile))
		}
		got, _ := completions("profile", "x", "--switch", "--new", "--from", "")
		Expect(got).To(Equal([]string{"go"}))
		got, _ = completions("profile", "x", "--switch", "--new", "--bundle", "go,")
		Expect(got).To(BeEmpty(), "go is named already")
		got, _ = completions("profile", "x", "--switch", "--new", "--bundle", "")
		Expect(got).To(Equal([]string{"go"}))
	})

	It("--move offers the modules of the profile named, else of the scope", func() {
		got, d := completions("profile", "go", "--move", "")
		Expect(got).To(Equal([]string{"other"}))
		Expect(d).To(Equal(dirNoFile))
		got, _ = completions("profile", "-C", proj, "--move", "")
		Expect(got).To(Equal([]string{"kit"}))
	})

	It("lola trash offers its runs, newest first with their command, then a run's originals", func() {
		writeTree(proj, map[string]string{"notes.md": "n\n", "old.md": "o\n"})
		at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
		older := trashRunOf(proj, at, "lola tidy", nil, "old.md")
		newer := trashRunOf(proj, at.Add(time.Hour), "lola profile go --remove", nil, "notes.md")
		got, d := completions("trash", "")
		Expect(got).To(Equal([]string{newer + "\tlola profile go --remove", older + "\tlola tidy"}))
		Expect(d).To(Equal(dirOrdered))
		got, d = completions("trash", newer, "")
		Expect(got).To(Equal([]string{filepath.Join(proj, "notes.md")}))
		Expect(d).To(Equal(dirNoFile))
		got, _ = completions("trash", "--empty", newer, "")
		Expect(got).To(Equal([]string{older + "\tlola tidy"}), "--empty takes more runs")
		got, _ = completions("trash", "nope", "")
		Expect(got).To(BeEmpty())
	})

	It("--at offers <module>= for the modules installed, then the version installed", func() {
		for _, cmd := range []string{"sync", "update", "install"} {
			got, d := completions(cmd, "-C", proj, "--at", "")
			Expect(got).To(Equal([]string{"kit="}), cmd)
			Expect(d).To(Equal(dirNoSpace), cmd)
		}
		got, d := completions("sync", "-C", proj, "--at", "kit=")
		Expect(got).To(HaveLen(1))
		Expect(got[0]).To(MatchRegexp(`^kit=sha256:[0-9a-f]{64}$`))
		Expect(d).To(Equal(dirNoFile))
		got, _ = completions("sync", "-C", proj, "--at", "nope=")
		Expect(got).To(BeEmpty())
	})

	It("--trust offers the kinds the command takes, completing the last of a list, and no file names", func() {
		got, d := completions("sync", "--trust=")
		Expect(got).To(Equal([]string{"entries", "profiles", "scripts", "hooks", "runtime-hooks", "all"}))
		Expect(d).To(Equal(dirNoFile))
		got, _ = completions("sync", "--trust=entries,")
		Expect(got).To(Equal([]string{"entries,profiles", "entries,scripts", "entries,hooks", "entries,runtime-hooks"}))
		got, _ = completions("install", "--trust=")
		Expect(got).To(Equal([]string{"scripts", "hooks", "runtime-hooks", "all"}))
	})

	It("value flags offer no file names, but for a path", func() {
		for _, args := range [][]string{{"install", "--ref"}, {"info", "kit", "--ref"}, {"cache", "add", "--ref"}, {"market", "--ssh-host"},
			{"migrate", "--map-root"}, {"list", "x"}, {"cache", "search"}} {
			_, d := completions(append(args, "")...)
			Expect(d).To(Equal(dirNoFile), strings.Join(args, " "))
		}
		for _, args := range [][]string{{"install", "--path"}, {"cache", "add", "--path"}, {"-C"}, {"init"}} {
			_, d := completions(append(args, "")...)
			Expect(d).To(Equal(dirFilterDirs), strings.Join(args, " "))
		}
		got, d := completions("trash", "--empty", "--older-than", "")
		Expect(got).To(ContainElements("30d\t30 days", "2w\t2 weeks"))
		Expect(d).To(Equal(dirOrdered))
		got, _ = completions("cache", "clean", "--unused-for", "")
		Expect(got).To(ContainElement("never\tnot this run"))
	})

	It("-t offers the assistants found here first, saying where, and the rest as not found", func() {
		got, d := completions("install", "x", "-t", "")
		Expect(got).To(Equal([]string{"claude-code\tnot found on this machine", "gemini-cli\tnot found on this machine",
			"opencode\tnot found on this machine", "agents\t" + agentsReaders}))
		Expect(d).To(Equal(dirOrdered))
		fakeAssistant(tmp, "opencode", "1.0")
		got, _ = completions("install", "x", "-t", "")
		Expect(got).To(Equal([]string{"opencode\tfound at " + filepath.Join(tmp, "bin", "opencode"),
			"claude-code\tnot found on this machine", "gemini-cli\tnot found on this machine", "agents\t" + agentsReaders}))
		got, _ = completions("list", "-t", "opencode,")
		Expect(got).To(Equal([]string{"opencode,claude-code\tnot found on this machine", "opencode,gemini-cli\tnot found on this machine",
			"opencode,agents\t" + agentsReaders}))
	})

	It("-t offers agents last, as set up where the command's scope has .agents/skills", func() {
		Expect(os.MkdirAll(filepath.Join(proj, ".agents", "skills"), 0o755)).To(Succeed())
		got, _ := completions("install", "x", "-C", proj, "-t", "")
		Expect(got).To(HaveLen(4))
		Expect(got[3]).To(Equal("agents\tset up here"))
		got, _ = completions("install", "x", "--user", "-t", "")
		Expect(got[3]).To(Equal("agents\t"+agentsReaders), "the user scope has no .agents/skills")
		got, _ = completions("install", "x", "-C", proj, "--scope", "both", "-t", "")
		Expect(got[3]).To(Equal("agents\tset up here"), "set up in one of the run's scopes")
		Expect(os.RemoveAll(filepath.Join(proj, ".agents"))).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".agents", "skills"), 0o755)).To(Succeed())
		got, _ = completions("install", "x", "--user", "-t", "")
		Expect(got[3]).To(Equal("agents\tset up here"))
		got, _ = completions("install", "x", "-C", proj, "--scope", "both", "-t", "")
		Expect(got[3]).To(Equal("agents\tset up here"), "set up in the other of the run's scopes")
		got, _ = completions("install", "x", "-C", proj, "--scope", "project", "-t", "")
		Expect(got[3]).To(Equal("agents\t" + agentsReaders))
	})

	It("writes nothing", func() {
		writeTree(proj, map[string]string{"notes.md": "n\n"})
		id := trashRunOf(proj, time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC), "lola tidy", nil, "notes.md")
		before := tree(tmp)
		for _, args := range [][]string{{"install", "-C", proj, ""}, {"install", "kit", "--skip", ""}, {"install", "--profile", ""},
			{"sync", "-C", proj, "--at", "kit="}, {"trash", ""}, {"trash", id, ""}, {"install", "-t", ""}, {"profile", "go", "--move", ""},
			{"profile", "-C", proj, "--move", ""}, {"uninstall", "-C", proj, ""}, {"sync", "-C", proj, ""}} {
			completions(args...)
		}
		Expect(tree(tmp)).To(Equal(before))
	})
})

// tree is every path below root with its size and modification time.
func tree(root string) map[string]string {
	GinkgoHelper()
	out := map[string]string{}
	Expect(filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		out[p] = fmt.Sprint(fi.Mode(), fi.ModTime(), fi.Size())
		return nil
	})).To(Succeed())
	return out
}
