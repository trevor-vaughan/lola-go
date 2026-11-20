package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("uninstall --all", func() {
	var p paths.Paths
	var tmp, proj string
	// command is where a module's command file is at user scope: a and b come from the base, c from the profile on.
	command := func(name string) string { return filepath.Join(p.Home, ".claude", "commands", name+".md") }
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		src := map[string]string{}
		for _, n := range []string{"a", "b", "c"} {
			dir := filepath.Join(tmp, "mods", n)
			writeTree(dir, map[string]string{"lola.yml": "name: " + n + "\n", "commands/" + n + ".md": n + "\n"})
			Expect(lola("cache", "add", dir).code).To(Equal(0))
			src[n] = dir
		}
		writeTree(p.UserProfiles, map[string]string{
			"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: a, source: " + src["a"] + "}\n  - {name: b, source: " + src["b"] + "}\n",
			"go.yml":      "version: 1\nmodules:\n  - {name: c, source: " + src["c"] + "}\n"})
		Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
		r := lola("sync", "--user", "-y")
		Expect(r.code).To(Equal(0), r.err)
		for _, n := range []string{"a", "b", "c"} {
			Expect(command(n)).To(BeARegularFile())
		}
	})
	profile := func(name string) string {
		GinkgoHelper()
		b, err := os.ReadFile(filepath.Join(p.UserProfiles, name))
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}

	It("removes every module the scope lists, the base's and the profile on's, and the plan lists all three", func() {
		r := lola("uninstall", "--all", "--user", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("would remove a from default (base)"))
		Expect(r.out).To(ContainSubstring("would remove b from default (base)"))
		Expect(r.out).To(ContainSubstring("would remove c from profile go"))
		for _, n := range []string{"a", "b", "c"} {
			Expect(command(n)).To(BeARegularFile(), "-n writes nothing")
		}
		r = lola("uninstall", "--all", "--user", "-y")
		Expect(r.code).To(Equal(0), r.err)
		for _, n := range []string{"a", "b", "c"} {
			Expect(command(n)).NotTo(BeAnExistingFile())
		}
		Expect(profile("default.yml")).To(Equal("version: 1\ntargets: [claude-code]\n"))
		Expect(profile("go.yml")).To(Equal("version: 1\n"))
	})

	It("removes a module the base and the profile on both list from both, leaving lola list empty", func() {
		a := filepath.Join(tmp, "mods", "a")
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\nmodules:\n  - {name: c, source: " + filepath.Join(tmp, "mods", "c") + "}\n  - {name: a, source: " + a + ", rules: false}\n"})
		Expect(lola("sync", "--user", "-y").code).To(Equal(0))
		r := lola("uninstall", "--all", "--user", "-y")
		Expect(r.code).To(Equal(0), r.err)
		for _, n := range []string{"a", "b", "c"} {
			Expect(command(n)).NotTo(BeAnExistingFile())
		}
		Expect(profile("default.yml")).To(Equal("version: 1\ntargets: [claude-code]\n"))
		Expect(profile("go.yml")).To(Equal("version: 1\n"))
		r = lola("list", "--user", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(BeEmpty())
	})

	It("--profile off a terminal without -y refuses, and the hint keeps --profile", func() {
		r := lola("uninstall", "--all", "--profile", "go", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("lola uninstall needs confirmation"))
		Expect(r.err).To(HaveSuffix("  hint: lola uninstall --all --profile go --user -y\n"))
		Expect(command("c")).To(BeARegularFile())
		Expect(lola("uninstall", "--all", "--profile", "go", "--user", "-y").code).To(Equal(0))
		Expect(command("c")).NotTo(BeAnExistingFile())
	})

	It("--profile removes only that profile's entries", func() {
		r := lola("uninstall", "--all", "--profile", "go", "--user", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(command("c")).NotTo(BeAnExistingFile())
		Expect(command("a")).To(BeARegularFile())
		Expect(command("b")).To(BeARegularFile())
		Expect(profile("go.yml")).To(Equal("version: 1\n"))
		Expect(profile("default.yml")).To(ContainSubstring("name: a"))
	})

	It("--scope both removes at each scope, and --json reports one document with a result per scope", func() {
		Expect(lola("install", filepath.Join(tmp, "mods", "a"), "-C", proj).code).To(Equal(0))
		projCmd := filepath.Join(proj, ".claude", "commands", "a.md")
		Expect(projCmd).To(BeARegularFile())
		r := lola("uninstall", "--all", "--scope", "both", "-C", proj, "-n", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var doc struct {
			Scopes []json.RawMessage `json:"scopes"`
		}
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		Expect(doc.Scopes).To(HaveLen(2))
		Expect(projCmd).To(BeARegularFile())

		r = lola("uninstall", "--all", "--scope", "both", "-C", proj, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(projCmd).NotTo(BeAnExistingFile())
		for _, n := range []string{"a", "b", "c"} {
			Expect(command(n)).NotTo(BeAnExistingFile())
		}
	})

	It("refuses names beside --all, removing nothing, and hints both ways out", func() {
		r := lola("uninstall", "--all", "a", "--user", "-y")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: --all removes every module, and a names some\n"))
		Expect(r.err).To(ContainSubstring("  hint: remove only those: lola uninstall a --user --yes\n"))
		Expect(r.err).To(ContainSubstring("  hint: remove them all: lola uninstall --all --user --yes\n"))
		Expect(command("a")).To(BeARegularFile())
		Expect(lola(shellWords("lola uninstall a --user --yes")[1:]...).code).To(Equal(0))
		Expect(lola(shellWords("lola uninstall --all --user --yes")[1:]...).code).To(Equal(0))
	})

	It("off a terminal without -y refuses, removes nothing, and hints the same run with -y", func() {
		r := lola("uninstall", "--all", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("lola uninstall needs confirmation"))
		Expect(r.err).To(HaveSuffix("  hint: lola uninstall --all --user -y\n"))
		for _, n := range []string{"a", "b", "c"} {
			Expect(command(n)).To(BeARegularFile())
		}
		Expect(lola("uninstall", "--all", "--user", "-y").code).To(Equal(0))
		Expect(command("a")).NotTo(BeAnExistingFile())
	})

	It("says there is nothing to uninstall, and succeeds, when nothing is installed", func() {
		Expect(lola("uninstall", "--all", "--user", "-y").code).To(Equal(0))
		for _, args := range [][]string{{"--user"}, {"--user", "-y"}, {"--user", "-n"}} {
			r := lola(append([]string{"uninstall", "--all"}, args...)...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("nothing to uninstall"), args)
		}
		r := lola("uninstall", "--all", "--user", "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(`"modules": []`))
	})

	It("takes --keep-edits: an edited file stays, the rest goes", func() {
		writeTree(p.Home, map[string]string{".claude/commands/a.md": "mine now"})
		r := lola("uninstall", "--all", "--keep-edits", "--user", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ kept .claude/commands/a.md"))
		Expect(command("a")).To(BeARegularFile())
		Expect(command("b")).NotTo(BeAnExistingFile())
		Expect(command("c")).NotTo(BeAnExistingFile())
	})
})
