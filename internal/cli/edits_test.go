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

var _ = Describe("write lines", func() {
	var p paths.Paths
	var tmp, kit string
	BeforeEach(func() {
		tmp = testenv.Isolate()
		var err error
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		kit = filepath.Join(tmp, "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "skills/a/SKILL.md": "---\nname: a\ndescription: A\n---\nA\n",
			"skills/b/SKILL.md": "---\nname: b\ndescription: B\n---\nB\n"})
	})

	It("names the file each write went to, before the plan, in human, JSON and --plain output", func() {
		def := filepath.Join(p.UserProfiles, "default.yml")
		r := lola("install", kit, "--user", "-t", "claude-code", "--only", "skill/a", "--exact")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("✓ added kit to default (base)  (" + def + ", new)  only: 1 skill\n"))
		Expect(r.err).NotTo(ContainSubstring("saved to"), "the write line names the file; no second line says it")

		r = lola("install", "kit", "--user", "--only", "skill/b", "--exact", "--json", "-n")
		Expect(r.code).To(Equal(0), r.err)
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		checkJSON(doc, "")
		Expect(doc["edits"]).To(Equal([]any{map[string]any{"module": "kit", "action": "changed", "profile": "default", "kind": "base",
			"path": def, "scope": "user"}}))

		r = lola("uninstall", "kit", "--user", "-y", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("edit\tremoved\tkit\tdefault\t" + def + "\n"))
	})

	It("takes comma-separated --only, --skip and --unskip values as one selector each", func() {
		def := filepath.Join(p.UserProfiles, "default.yml")
		r := lola("install", kit, "--user", "-t", "claude-code", "--only", "skill/a,skill/b", "--exact", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(os.ReadFile(def)).To(ContainSubstring("    only: [skill/a, skill/b]\n"))

		r = lola("install", "kit", "--user", "--skip", "skill/a,skill/b", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(os.ReadFile(def)).To(ContainSubstring("    skip: [skill/a, skill/b]\n"))

		r = lola("install", "kit", "--user", "--unskip", "skill/a,skill/b", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(os.ReadFile(def)).NotTo(ContainSubstring("skip:"))
	})

	It("lists the only: list in force and its layer in lola list --json, on a not installed row too; none is [] and \"\"", func() {
		other := filepath.Join(tmp, "other")
		writeTree(other, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "O\n"})
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + ", only: [skill/a]}\n" +
			"  - {name: other, source: " + other + "}\n"})
		rows := func() map[string]map[string]any {
			GinkgoHelper()
			r := lola("list", "--user", "--json")
			Expect(r.code).To(Equal(0), r.err)
			var doc struct{ Modules []map[string]any }
			Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
			byName := map[string]map[string]any{}
			for _, m := range doc.Modules {
				byName[m["name"].(string)] = m
			}
			return byName
		}

		got := rows()
		Expect(got["kit"]).To(And(HaveKeyWithValue("status", "not_installed"), HaveKeyWithValue("only", []any{"skill/a"}), HaveKeyWithValue("only_from", "default (base)")))
		Expect(got["other"]).To(And(HaveKeyWithValue("only", []any{}), HaveKeyWithValue("only_from", "")))

		r := lola("sync", "--user", "-y")
		Expect(r.code).To(Equal(0), r.err)
		got = rows()
		Expect(got["kit"]).To(And(HaveKeyWithValue("only", []any{"skill/a"}), HaveKeyWithValue("only_from", "default (base)")))
		Expect(got["other"]).To(And(HaveKeyWithValue("only", []any{}), HaveKeyWithValue("only_from", "")))
	})

	It("says shared: git tracks it for project.yml and for a project profile git tracks", func() {
		proj := filepath.Join(tmp, "proj")
		other := filepath.Join(tmp, "other")
		writeTree(other, map[string]string{"lola.yml": "name: other\n", "commands/other.md": "O\n"})
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n",
			".lola/profiles/team.yml": "version: 1\ntargets: [claude-code]\n"})
		gitIn(proj, "init", "-q", "-b", "main")
		gitIn(proj, "add", ".lola")
		gitIn(proj, "commit", "-q", "-m", "lola")
		r := lola("install", kit, "-C", proj, "-t", "claude-code", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("✓ added kit to project.yml (base, shared: git tracks it)\n"))

		r = lola("profile", "team", "--switch", "-C", proj, "-y")
		Expect(r.code).To(Equal(0), r.err)
		r = lola("install", other, "-C", proj, "-t", "claude-code", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("✓ added other to profile team (shared: git tracks it)\n"))
	})
})
