package cli_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("list drift", func() {
	var tmp, home, proj string
	var pth paths.Paths
	skill := func(name string) string { return "---\nname: " + name + "\ndescription: " + name + "\n---\nbody\n" }
	installKit := func(extra map[string]string) {
		GinkgoHelper()
		files := map[string]string{"lola.yml": "name: kit\n", "skills/a/SKILL.md": skill("a"), "skills/b/SKILL.md": skill("b")}
		maps.Copy(files, extra)
		writeTree(filepath.Join(tmp, "mods", "kit"), files)
		r := lola("install", filepath.Join(tmp, "mods", "kit"), "--user", "-t", "claude-code", "-y")
		Expect(r.code).To(Equal(0), r.err)
	}
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		home = os.Getenv("HOME")
		pth, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
	})
	deleteSkill := func(name string) {
		GinkgoHelper()
		Expect(os.RemoveAll(filepath.Join(home, ".claude", "skills", name))).To(Succeed())
	}

	It("a missing skill shows counts in the cell and a block with restore and keep-out hints", func() {
		installKit(nil)
		deleteSkill("a")
		r := lola("list", "--user", "kit")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`✗ missing 1/2\b`))
		Expect(r.out).To(ContainSubstring("user kit claude-code:\n"))
		Expect(r.out).To(MatchRegexp(`\n\s+✗ skill/a\n`))
		Expect(r.out).To(ContainSubstring("→ restore: lola sync --user\n"))
		Expect(r.out).To(ContainSubstring("→ keep out: lola install --user kit --skip skill/a\n"))
	})

	It("a modified skill shows a warning line and no keep-out hint", func() {
		installKit(nil)
		Expect(os.WriteFile(filepath.Join(home, ".claude", "skills", "b", "SKILL.md"), []byte("edited\n"), 0o644)).To(Succeed())
		r := lola("list", "--user", "kit")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`⚠ modified 1/2\b`))
		Expect(r.out).To(MatchRegexp(`\n\s+⚠ skill/b \(modified\)\n`))
		Expect(r.out).To(ContainSubstring("→ restore: lola sync --user\n"))
		Expect(r.out).NotTo(ContainSubstring("keep out"))
	})

	It("a missing module copy shows its own cell and line and no keep-out hint", func() {
		installKit(nil)
		Expect(os.RemoveAll(filepath.Join(pth.UserModules, "kit"))).To(Succeed())
		r := lola("list", "--user", "kit")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("missing (module copy)"))
		Expect(r.out).To(MatchRegexp(`\n\s+✗ module copy\n`))
		Expect(r.out).NotTo(ContainSubstring("keep out"))
	})

	It("a skill with several files prints its file count", func() {
		installKit(map[string]string{"skills/a/extra.md": "more\n"})
		deleteSkill("a")
		r := lola("list", "--user", "kit")
		Expect(r.out).To(MatchRegexp(`✗ missing 2/3\b`))
		Expect(r.out).To(MatchRegexp(`\n\s+✗ skill/a \(2 files\)\n`))
	})

	It("bare list points at the drifted modules once per scope, and at none when nothing drifted", func() {
		installKit(nil)
		Expect(lola("list", "--user").out).NotTo(ContainSubstring("see what changed"))
		deleteSkill("a")
		r := lola("list", "--user")
		Expect(strings.Count(r.out, "see what changed")).To(Equal(1))
		Expect(r.out).To(ContainSubstring("→ see what changed: lola list --user kit\n"))
		Expect(r.out).NotTo(ContainSubstring("→ restore"))
	})

	It("an unknown module is an error that names it and hints at the list", func() {
		installKit(nil)
		r := lola("list", "--user", "nope")
		Expect(r.code).NotTo(Equal(0))
		Expect(r.err).To(ContainSubstring("nope is not installed"))
		Expect(r.err).To(ContainSubstring("hint: list the installed modules: lola list --user"))
	})

	It("a module copy gone and a file only modified reads missing (module copy), not a file count", func() {
		installKit(nil)
		Expect(os.RemoveAll(filepath.Join(pth.UserModules, "kit"))).To(Succeed())
		Expect(os.WriteFile(filepath.Join(home, ".claude", "skills", "b", "SKILL.md"), []byte("edited\n"), 0o644)).To(Succeed())
		r := lola("list", "--user", "kit")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("missing (module copy)"))
		Expect(r.out).NotTo(MatchRegexp(`missing \d+/\d+`))
		Expect(r.out).To(MatchRegexp(`\n\s+⚠ skill/b \(modified\)\n`))
		Expect(r.out).To(MatchRegexp(`\n\s+✗ module copy\n`))
	})

	It("a module copy gone and a file missing too counts the drifted files", func() {
		installKit(nil)
		Expect(os.RemoveAll(filepath.Join(pth.UserModules, "kit"))).To(Succeed())
		deleteSkill("a")
		Expect(os.WriteFile(filepath.Join(home, ".claude", "skills", "b", "SKILL.md"), []byte("edited\n"), 0o644)).To(Succeed())
		r := lola("list", "--user", "kit")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`✗ missing 2/2\b`))
		Expect(r.out).NotTo(ContainSubstring("(module copy)\n"))
		Expect(r.out).To(MatchRegexp(`\n\s+✗ module copy\n`))
	})

	It("keep out repeats --skip once per component, so no argument is cut short", func() {
		files := map[string]string{}
		var names []string
		for i := range 20 {
			n := fmt.Sprintf("skill-number-%02d-with-a-long-name", i)
			names = append(names, n)
			files["skills/"+n+"/SKILL.md"] = skill(n)
		}
		installKit(files)
		for _, n := range names {
			deleteSkill(n)
		}
		r := lola("list", "--user", "kit")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).NotTo(ContainSubstring("…"))
		for _, n := range names {
			Expect(r.out).To(ContainSubstring("--skip skill/" + n))
		}
	})

	It("a named module does not say a scope is empty when it holds other modules", func() {
		installKit(nil)
		writeTree(filepath.Join(tmp, "mods", "other"), map[string]string{"lola.yml": "name: other\n", "skills/o/SKILL.md": skill("o")})
		i := lola("install", filepath.Join(tmp, "mods", "other"), "--project", "-C", proj, "-t", "claude-code", "-y")
		Expect(i.code).To(Equal(0), i.err)
		r := lola("list", "--scope", "both", "-C", proj, "kit")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).NotTo(ContainSubstring("nothing installed"))
	})

	It("-t with no module for that target says what is missing in each scope instead of printing nothing", func() {
		installKit(nil)
		writeTree(filepath.Join(tmp, "mods", "other"), map[string]string{"lola.yml": "name: other\n", "skills/o/SKILL.md": skill("o")})
		i := lola("install", filepath.Join(tmp, "mods", "other"), "--project", "-C", proj, "-t", "claude-code", "-y")
		Expect(i.code).To(Equal(0), i.err)
		r := lola("list", "--scope", "both", "-C", proj, "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("nothing installed for opencode in " + proj + " (try: lola install -C " + proj + " <source>)\n"))
		Expect(r.out).To(ContainSubstring("nothing installed for opencode at user scope (try: lola install --user <source>)\n"))
		r = lola("list", "--scope", "both", "-C", proj, "-t", "opencode", "-t", "gemini-cli", "kit")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("nothing installed for opencode, gemini-cli in " + proj))
	})

	It("a scope with no module keeps its plain wording when no target is given", func() {
		installKit(nil)
		r := lola("list", "--scope", "both", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("nothing installed in " + proj + " (try: lola install -C " + proj + " <source>)\n"))
	})

	It("an unknown module's hint lists each scope read, with that scope's flags", func() {
		installKit(nil)
		r := lola("list", "--scope", "both", "-C", proj, "nope")
		Expect(r.code).NotTo(Equal(0))
		Expect(r.err).To(ContainSubstring("hint: list the installed modules: lola list --user"))
		Expect(r.err).To(ContainSubstring("hint: list the installed modules: lola list -C " + proj + "\n"))
	})

	It("a module installed in one of two read scopes is not an error", func() {
		installKit(nil)
		r := lola("list", "--scope", "both", "-C", proj, "kit")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("kit"))
	})

	It("-t limits the blocks to that target", func() {
		installKit(nil)
		deleteSkill("a")
		r := lola("list", "--user", "-t", "opencode", "kit")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).NotTo(ContainSubstring("claude-code"))
		Expect(r.out).NotTo(ContainSubstring("skill/a"))
	})

	It("--json rows carry drift and recorded, drift being [] when ok", func() {
		installKit(nil)
		var doc struct {
			Modules []struct {
				Drift    []map[string]any `json:"drift"`
				Recorded int              `json:"recorded"`
			} `json:"modules"`
		}
		Expect(json.Unmarshal([]byte(lola("list", "--user", "--json").out), &doc)).To(Succeed())
		Expect(doc.Modules).To(HaveLen(1))
		Expect(doc.Modules[0].Drift).NotTo(BeNil())
		Expect(doc.Modules[0].Drift).To(BeEmpty())
		Expect(doc.Modules[0].Recorded).To(Equal(2))
		deleteSkill("a")
		Expect(json.Unmarshal([]byte(lola("list", "--user", "--json", "kit").out), &doc)).To(Succeed())
		Expect(doc.Modules[0].Drift).To(HaveLen(1))
		Expect(doc.Modules[0].Drift[0]).To(HaveKeyWithValue("name", "a"))
	})

	It("--plain keeps its columns and prints no drift lines", func() {
		installKit(nil)
		deleteSkill("a")
		r := lola("list", "--user", "--plain", "kit")
		Expect(r.out).To(MatchRegexp(`^kit\tclaude-code\tmissing\ton\t.*\tuser\t.*\n$`))
		Expect(r.out).NotTo(ContainSubstring("restore"))
		Expect(r.out).NotTo(ContainSubstring("see what changed"))
	})

	It("prints a component name carrying an escape without the raw ESC", func() {
		installKit(nil)
		raw, err := os.ReadFile(pth.UserLedger)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(raw)).To(ContainSubstring(`"name": "a"`))
		Expect(os.WriteFile(pth.UserLedger, []byte(strings.ReplaceAll(string(raw), `"name": "a"`, `"name": "a\u001b[31mred"`)), 0o644)).To(Succeed())
		deleteSkill("a")
		r := lola("list", "--user", "kit")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("\n    ✗ skill/a?[31mred\n"))
		Expect(r.out).NotTo(ContainSubstring("\x1b"))
	})

	It("completes installed module names and no file names", func() {
		installKit(nil)
		got, d := completions("list", "--user", "k")
		Expect(got).To(Equal([]string{"kit"}))
		Expect(d).To(Equal(dirNoFile))
	})
})
