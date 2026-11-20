package engine_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("--force and the trash", func() {
	var (
		proj, kit string
		e         *engine.Engine
		sc        engine.Scope
		ctx       = context.Background()
		claude    = engine.Options{Targets: []string{"claude-code"}}
	)
	BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p, targets.Builtin())
		proj, kit = filepath.Join(tmp, "proj"), filepath.Join(tmp, "src", "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "skills/hello/SKILL.md": "---\ndescription: Say hello\n---\nHello.\n",
			"mcps.json": `{"mcpServers":{"gh":{"command":"gh"}}}`})
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		sc = engine.Scope{Kind: target.Project, ProjectDir: proj}
	})
	install := func(opts engine.Options) (*engine.Result, error) {
		opts.Targets = claude.Targets
		return e.Install(ctx, sc, proj, []engine.InstallRequest{{Arg: kit}}, opts)
	}
	read := func(path string) string {
		GinkgoHelper()
		b, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}
	runs := func() []string {
		ents, err := os.ReadDir(e.Paths.Trash)
		if os.IsNotExist(err) {
			return nil
		}
		Expect(err).NotTo(HaveOccurred())
		var out []string
		for _, d := range ents {
			out = append(out, filepath.Join(e.Paths.Trash, d.Name()))
		}
		return out
	}
	skill := func() string { return filepath.Join(proj, ".claude/skills/hello/SKILL.md") }

	It("copies an unowned file --force replaces into a new trash run, which restores it", func() {
		writeTree(proj, map[string]string{".claude/skills/hello/SKILL.md": "MY OWN SKILL\n"})
		res, err := install(engine.Options{Force: true, ForceRun: "lola install ./kit --force"})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(skill())).To(ContainSubstring("Hello."))
		Expect(res.Trash).To(HaveLen(1))
		Expect(runs()).To(Equal(res.Trash))
		Expect(res.TrashedFiles(res.Trash[0])).To(Equal(1))
		show, err := e.ShowTrash(filepath.Base(res.Trash[0]), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(show.Command).To(Equal("lola install ./kit --force"))
		Expect(show.Scope).To(Equal("project"))
		Expect(show.Root).To(Equal(proj))
		Expect(show.Entries).To(ConsistOf(HaveField("Original", skill())))
		Expect(read(show.Entries[0].Trash)).To(Equal("MY OWN SKILL\n"))
		Expect(res.TrashCredentials).To(BeEmpty())

		plan, err := e.PlanTrashRestore(engine.TrashRestoreRequest{Run: show.ID, Force: true})
		Expect(err).NotTo(HaveOccurred())
		_, err = e.RestoreTrash(plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(read(skill())).To(Equal("MY OWN SKILL\n"))
	})

	It("copies a hand-edited file uninstall --force deletes", func() {
		_, err := install(engine.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(skill(), []byte("MY EDITS\n"), 0o644)).To(Succeed())
		res, err := e.Uninstall(ctx, sc, []string{"kit"}, engine.Options{Force: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(skill()).NotTo(BeAnExistingFile())
		Expect(res.Trash).To(HaveLen(1))
		show, err := e.ShowTrash(filepath.Base(res.Trash[0]), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(show.Entries).To(ConsistOf(HaveField("Original", skill())))
		Expect(read(show.Entries[0].Trash)).To(Equal("MY EDITS\n"))
	})

	It("copies the whole file of an unowned JSON member, once, as a credential copy", func() {
		mcp := filepath.Join(proj, ".mcp.json")
		mine := `{"mcpServers":{"gh":{"command":"my-gh","env":{"TOKEN":"secret"}},"other":{"command":"x"}}}`
		writeTree(proj, map[string]string{".mcp.json": mine})
		res, err := install(engine.Options{Force: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(mcp)).NotTo(ContainSubstring("my-gh"))
		Expect(res.Trash).To(HaveLen(1))
		show, err := e.ShowTrash(filepath.Base(res.Trash[0]), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(show.Entries).To(ConsistOf(And(HaveField("Original", mcp), HaveField("Credentials", true))))
		Expect(read(show.Entries[0].Trash)).To(Equal(mine))
		Expect(res.TrashCredentials).To(ConsistOf(show.Entries[0].Trash))
		Expect(res.TrashedCredentialFiles()).To(Equal([]string{".mcp.json"}))
	})

	It("opens no trash run for an install that forces nothing", func() {
		res, err := install(engine.Options{Force: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Trash).To(Equal([]string{}))
		Expect(res.TrashCredentials).To(Equal([]string{}))
		Expect(runs()).To(BeEmpty())
	})

	It("opens no trash run on a dry run with --force", func() {
		writeTree(proj, map[string]string{".claude/skills/hello/SKILL.md": "MY OWN SKILL\n"})
		res, err := install(engine.Options{Force: true, DryRun: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Steps).To(ContainElement(HaveField("Forced", "unowned")))
		Expect(res.Trash).To(Equal([]string{}))
		Expect(runs()).To(BeEmpty())
		Expect(read(skill())).To(Equal("MY OWN SKILL\n"))
	})
})
