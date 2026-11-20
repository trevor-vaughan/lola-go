package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("--force and the trash", func() {
	var proj, kit, skill string
	BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, kit = filepath.Join(tmp, "proj"), filepath.Join(tmp, "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "skills/hello/SKILL.md": "---\ndescription: Say hello\n---\nHello.\n"})
		skill = filepath.Join(proj, ".claude/skills/hello/SKILL.md")
		writeTree(proj, map[string]string{".claude/skills/hello/SKILL.md": "MY OWN SKILL\n"})
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
	})
	trashOf := func(out string) []string {
		var v struct {
			Trash            []string `json:"trash"`
			TrashCredentials []string `json:"trash_credentials"`
		}
		Expect(json.Unmarshal([]byte(out), &v)).To(Succeed(), out)
		Expect(v.TrashCredentials).NotTo(BeNil())
		return v.Trash
	}

	It("says where the file --force replaced went", func() {
		r := lola("install", kit, "-C", proj, "-t", "claude-code", "--force")
		Expect(r.code).To(Equal(0), r.err)
		runs, err := os.ReadDir(filepath.Join(os.Getenv("XDG_STATE_HOME"), "lola", "trash"))
		Expect(err).NotTo(HaveOccurred())
		Expect(runs).To(HaveLen(1))
		run := filepath.Join(os.Getenv("XDG_STATE_HOME"), "lola", "trash", runs[0].Name())
		Expect(r.out).To(ContainSubstring("→ 1 file --force replaced or deleted is in the trash: " + run + "\n"))
		b, err := os.ReadFile(skill)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("Hello."))
	})

	It("warns of the credential copy of a JSON member's file, and lists it under --json", func() {
		writeTree(kit, map[string]string{"mcps.json": `{"mcpServers":{"gh":{"command":"gh"}}}`})
		writeTree(proj, map[string]string{".mcp.json": `{"mcpServers":{"gh":{"command":"my-gh"},"other":{"command":"x"}}}`})
		r := lola("install", kit, "-C", proj, "-t", "claude-code", "--force")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("⚠ the trash run holds a copy of .mcp.json, which may contain credentials\n"))

		writeTree(proj, map[string]string{".mcp.json": `{"mcpServers":{"gh":{"command":"mine-again"}}}`})
		r = lola("install", kit, "-C", proj, "-t", "claude-code", "--force", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var v struct {
			TrashCredentials []string `json:"trash_credentials"`
		}
		Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed(), r.out)
		Expect(v.TrashCredentials).To(HaveLen(1))
		b, err := os.ReadFile(v.TrashCredentials[0])
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("mine-again"))
	})

	It("keeps a run per scope, and says so for each, when --force replaces files in both", func() {
		Expect(os.Remove(skill)).To(Succeed())
		r := lola("install", kit, "-C", proj, "-t", "claude-code", "--scope", "both")
		Expect(r.code).To(Equal(0), r.err)
		user := filepath.Join(os.Getenv("HOME"), ".claude/skills/hello/SKILL.md")
		for _, f := range []string{skill, user} {
			Expect(os.WriteFile(f, []byte("MY EDITS\n"), 0o644)).To(Succeed())
		}
		r = lola("sync", "-C", proj, "--scope", "both", "--force")
		Expect(r.code).To(Equal(0), r.err)
		base := filepath.Join(os.Getenv("XDG_STATE_HOME"), "lola", "trash")
		runs, err := os.ReadDir(base)
		Expect(err).NotTo(HaveOccurred())
		Expect(runs).To(HaveLen(2))
		for _, d := range runs {
			Expect(r.out).To(ContainSubstring("→ 1 file --force replaced or deleted is in the trash: " + filepath.Join(base, d.Name()) + "\n"))
		}

		for _, f := range []string{skill, user} {
			Expect(os.WriteFile(f, []byte("MY EDITS AGAIN\n"), 0o644)).To(Succeed())
		}
		r = lola("sync", "-C", proj, "--scope", "both", "--force", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var v struct {
			Scopes []struct {
				Trash []string `json:"trash"`
			} `json:"scopes"`
		}
		Expect(json.Unmarshal([]byte(r.out), &v)).To(Succeed(), r.out)
		Expect(v.Scopes).To(HaveLen(2))
		Expect(v.Scopes[0].Trash).To(HaveLen(1))
		Expect(v.Scopes[1].Trash).To(HaveLen(1))
		Expect(v.Scopes[0].Trash[0]).NotTo(Equal(v.Scopes[1].Trash[0]))
	})

	It("lists the run under --json and --plain, and [] when nothing was replaced", func() {
		r := lola("install", kit, "-C", proj, "-t", "claude-code", "--force", "--json")
		Expect(r.code).To(Equal(0), r.err)
		runs := trashOf(r.out)
		Expect(runs).To(HaveLen(1))
		Expect(runs[0]).To(BeADirectory())

		Expect(os.WriteFile(skill, []byte("MY EDITS\n"), 0o644)).To(Succeed())
		r = lola("uninstall", "kit", "-C", proj, "--force", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`(?m)^trash\t` + filepath.Join(os.Getenv("XDG_STATE_HOME"), "lola", "trash") + `/\S+$`))

		r = lola("install", kit, "-C", proj, "-t", "claude-code", "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(trashOf(r.out)).To(Equal([]string{}))
	})
})
