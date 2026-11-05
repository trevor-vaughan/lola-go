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

// Spec 2026-10-29 §16.2: a local entry inside a project never becomes yours unreviewed.
var _ = Describe("local entries inside a project", func() {
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
		proj = filepath.Join(tmp, "proj")
		notes = filepath.Join(proj, "mods", "notes")
		writeTree(proj, map[string]string{"mods/notes/lola.yml": "name: notes\n", "mods/notes/commands/notes.md": "N\n",
			".lola/project.yml": "version: 1\ntargets: [claude-code]\n"})
	})
	inside := func(file string) string {
		return "notes (" + file + ") installs from " + notes + ", inside project " + proj + ": changes there install without review wherever that file is on"
	}
	refused := func(file string) string {
		return inside(file) + "; lola keeps a project's local modules out of your files"
	}
	read := func(path string) string {
		GinkgoHelper()
		b, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}

	It("refuses moving a project's folder entry into your files, within the project and to user scope", func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: mods/notes}\n"})
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n"})
		r := lola("sync", "-C", proj, "--trust", "-y")
		Expect(r.code).To(Equal(0), r.err)
		r = lola("profile", "me", "--on", "-C", proj, "-y")
		Expect(r.code).To(Equal(0), r.err)
		before := read(filepath.Join(proj, ".lola", "project.yml"))

		me := filepath.Join(p.UserProfiles, "me.yml")
		r = lola("profile", "--move", "notes", "--to", "me", "-C", proj)
		Expect(r.code).NotTo(Equal(0))
		Expect(r.err).To(HaveSuffix("\nerror: " + refused(me) + "\n"))
		Expect(read(me)).To(Equal("version: 1\n"))

		def := filepath.Join(p.UserProfiles, "default.yml")
		r = lola("profile", "--move", "notes", "--to", "base", "--user", "-C", proj, "--json")
		Expect(r.code).NotTo(Equal(0))
		var doc struct {
			Error string   `json:"error"`
			Hints []string `json:"hints"`
		}
		Expect(json.Unmarshal([]byte(r.err), &doc)).To(Succeed(), r.err)
		Expect(doc.Error).To(Equal(refused(def)))
		Expect(doc.Hints).To(BeEmpty())
		Expect(def).NotTo(BeAnExistingFile())
		Expect(read(filepath.Join(proj, ".lola", "project.yml"))).To(Equal(before))
	})

	It("warns once when an install adds a folder inside a project to your files, with a hint that runs as printed", func() {
		r := lola("install", notes, "--user", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		def := filepath.Join(p.UserProfiles, "default.yml")
		// project.yml's targets are not trusted here: the install names the assistants notes has.
		twoStep := "lola uninstall --user notes, then lola install -C " + proj + " notes -t claude-code"
		Expect(r.out).To(ContainSubstring("⚠ " + inside(def) + "\n→ keep it in the project instead: " + twoStep + "\n"))

		again := lola("sync", "--user")
		Expect(again.code).To(Equal(0), again.err)
		Expect(again.out+again.err).NotTo(ContainSubstring("installs from"), "a sync says nothing of it")

		for _, step := range strings.Split(twoStep, ", then ") {
			r = runConfirmed(step)
			Expect(r.code).To(Equal(0), r.err)
		}
		Expect(read(def)).NotTo(ContainSubstring("notes"))
		Expect(read(filepath.Join(proj, ".lola", "project.yml"))).To(ContainSubstring("name: notes"))
	})

	It("warns once when an install at project scope adds the project's folder to a profile of yours", func() {
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n"})
		r := lola("install", notes, "-C", proj, "--profile", "me", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		me := filepath.Join(p.UserProfiles, "me.yml")
		Expect(strings.Count(r.out, "⚠ "+inside(me)+"\n")).To(Equal(1), r.out)
		again := lola("sync", "-C", proj)
		Expect(again.code).To(Equal(0), again.err)
		Expect(again.out+again.err).NotTo(ContainSubstring("installs from"), "a sync says nothing of it")
	})

	It("names the warning a warn notice in JSON", func() {
		r := lola("install", notes, "--user", "-t", "claude-code", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var doc struct {
			Notices []map[string]string `json:"notices"`
		}
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed(), r.out)
		Expect(doc.Notices).To(ContainElements(map[string]string{"level": "warn", "class": "safety", "text": inside(filepath.Join(p.UserProfiles, "default.yml"))},
			map[string]string{"level": "info", "class": "safety", "text": "keep it in the project instead: lola uninstall --user notes --json, then lola install -C " + proj + " notes -t claude-code --json"}))
	})
})
