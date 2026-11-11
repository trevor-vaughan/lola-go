package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
	"github.com/trevor-vaughan/lola-go/internal/trash"
)

var _ = Describe("completion candidates", func() {
	var (
		tmp, proj string
		p         paths.Paths
		e         *engine.Engine
		sc        engine.Scope
		ctx       = context.Background()
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p, targets.Builtin())
		proj = filepath.Join(tmp, "proj")
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
		writeTree(filepath.Join(tmp, "kit"), map[string]string{"lola.yml": "name: kit\n", "skills/a/SKILL.md": "---\nname: a\ndescription: a\n---\nhi\n",
			"commands/b.md": "B\n", "rules/r.md": "R\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		_, err = e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: filepath.Join(tmp, "kit")}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
	})

	It("ComponentNames lists each cached module's components in picker order, once, and nothing for a name the cache lacks", func() {
		Expect(e.ComponentNames([]string{"kit", "nope", "kit"})).To(Equal([]string{"skill/a", "command/b", "rule/r"}))
		Expect(e.ComponentNames(nil)).To(BeEmpty())
	})

	It("InstalledVersions is each installed module's version as --at takes it", func() {
		got, err := e.InstalledVersions(sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(HaveKeyWithValue("kit", MatchRegexp(`^sha256:[0-9a-f]{64}$`)))
		Expect(got).To(HaveLen(1))
		got, err = e.InstalledVersions(engine.Scope{Kind: target.User})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(BeEmpty())
	})

	It("MoveNames is a profile's modules when one of yours is named, else the scope's", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\nmodules:\n  - name: x\n    source: /x\n  - name: y\n    source: /y\n"})
		for _, from := range []string{"go", "user:go"} {
			got, err := e.MoveNames(sc, from)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal([]string{"x", "y"}), from)
		}
		for _, from := range []string{"", "base", "remote:go", "project:go", "nope"} {
			got, err := e.MoveNames(sc, from)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal([]string{"kit"}), from)
		}
	})

	It("TrashNames lists runs newest first with their command, and TrashOriginals a run's originals", func() {
		names, err := e.TrashNames()
		Expect(err).NotTo(HaveOccurred())
		Expect(names).To(BeEmpty(), "no trash yet")
		writeTree(proj, map[string]string{"a.md": "a\n", "b.md": "b\n"})
		run := func(at time.Time, command, name string) string {
			t, err := trash.OpenTrash(p.Trash, at, trash.RunRecord{Command: command, Scope: "project", Root: proj})
			Expect(err).NotTo(HaveOccurred())
			root, err := os.OpenRoot(proj)
			Expect(err).NotTo(HaveOccurred())
			_, err = t.Move("abc", proj, root, name)
			Expect(err).NotTo(HaveOccurred())
			Expect(root.Close()).To(Succeed())
			Expect(t.Close()).To(Succeed())
			return filepath.Base(t.Dir)
		}
		at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
		older, newer := run(at, "lola tidy", "a.md"), run(at.Add(time.Hour), "lola migrate", "b.md")
		writeTree(p.Trash, map[string]string{"stray": "not a run\n"})
		names, err = e.TrashNames()
		Expect(err).NotTo(HaveOccurred())
		Expect(names).To(Equal([]engine.TrashName{{ID: newer, Command: "lola migrate"}, {ID: older, Command: "lola tidy"}}))
		originals, err := e.TrashOriginals(older)
		Expect(err).NotTo(HaveOccurred())
		Expect(originals).To(Equal([]string{filepath.Join(proj, "a.md")}))
		_, err = e.TrashOriginals("../" + older)
		Expect(err).To(HaveOccurred())
	})
})
