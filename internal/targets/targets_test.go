package targets_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/targetspec"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// userSpec is a binary-less target with one agent kind; agentPath says where it places agents.
func userSpec(id, agentPath string) string {
	return `id: ` + id + `
readers: "the ` + id + ` editor"
roots: {project: "{project}", user: "{home}"}
detect: {project: {any: [.quill/]}, user: {any: [.quill/]}}
eras:
  - since: "1.0"
    layout:
      agent: {mech: markdown-file, path: ` + agentPath + `, ext: .md}
`
}

var builtinIDs = []string{"claude-code", "gemini-cli", "opencode", "agents"}

var _ = Describe("registry", func() {
	var p paths.Paths

	BeforeEach(func() {
		testenv.Isolate()
		var err error
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
	})

	write := func(name, text string) {
		Expect(os.MkdirAll(p.Targets, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(p.Targets, name), []byte(text), 0o600)).To(Succeed())
	}

	It("lists every built-in target once, in registration order, and looks them up by id", func() {
		r := targets.Builtin()
		Expect(r.IDs()).To(Equal(builtinIDs))
		for _, id := range r.IDs() {
			t, ok := r.Lookup(id)
			Expect(ok).To(BeTrue(), id)
			Expect(t.ID()).To(Equal(id))
			Expect(r.Replaced(id)).To(BeEmpty(), id)
		}
		_, ok := r.Lookup("vim")
		Expect(ok).To(BeFalse())
	})

	It("loads every built-in spec", func() {
		_, err := targetspec.Builtin()
		Expect(err).NotTo(HaveOccurred())
	})

	It("registers every built-in spec", func() {
		specs, err := targetspec.Builtin()
		Expect(err).NotTo(HaveOccurred())
		var ids []string
		for _, s := range specs {
			ids = append(ids, s.ID)
		}
		Expect(targets.Builtin().IDs()).To(ConsistOf(ids))
	})

	It("is the built-ins alone when the user has no target directory", func() {
		r, err := targets.Load(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.IDs()).To(Equal(builtinIDs))
		for _, id := range r.IDs() {
			Expect(r.Replaced(id)).To(BeEmpty(), id)
		}
	})

	It("is the built-ins alone when the target directory is empty", func() {
		Expect(os.MkdirAll(p.Targets, 0o755)).To(Succeed())
		r, err := targets.Load(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.IDs()).To(Equal(builtinIDs))
	})

	It("is the built-ins alone for the zero Paths", func() {
		r, err := targets.Load(paths.Paths{})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.IDs()).To(Equal(builtinIDs))
	})

	It("adds the user's new targets after the built-ins, sorted by id", func() {
		write("quill.yaml", userSpec("quill", ".quill/agents"))
		write("aardvark.yaml", userSpec("aardvark", ".aardvark/agents"))
		r, err := targets.Load(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.IDs()).To(Equal(append(append([]string{}, builtinIDs...), "aardvark", "quill")))
		t, ok := r.Lookup("quill")
		Expect(ok).To(BeTrue())
		Expect(t.ID()).To(Equal("quill"))
		Expect(r.Replaced("quill")).To(BeEmpty())
	})

	It("replaces a built-in with the user's spec of the same id, in the built-in's place", func() {
		write("mine.yaml", userSpec("opencode", ".mine/agents"))
		r, err := targets.Load(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.IDs()).To(Equal(builtinIDs))
		Expect(r.Replaced("opencode")).To(Equal(filepath.Join(p.Targets, "mine.yaml")))
		Expect(r.Replaced("claude-code")).To(BeEmpty())

		t, ok := r.Lookup("opencode")
		Expect(ok).To(BeTrue())
		dest, err := t.Destinations(target.Project, "/proj")
		Expect(err).NotTo(HaveOccurred())
		Expect(dest.Dirs).To(HaveLen(1))
		Expect(dest.Dirs[0].Path).To(Equal(".mine/agents"))
	})

	It("refuses a broken spec, names its file and offers the fix, and falls back to nothing", func() {
		write("quill.yaml", "id: quill\nroots: {project: \"{project}\", user: \"{home}\"}\n")
		r, err := targets.Load(p)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(filepath.Join(p.Targets, "quill.yaml")))
		te, ok := errors.AsType[*targetspec.Error](err)
		Expect(ok).To(BeTrue())
		Expect(te.Fix).NotTo(BeEmpty())
		Expect(errs.Hints(err)).To(ContainElement(te.Fix))
		Expect(errs.Hints(err)).To(ContainElement(
			"lola stops rather than render with its built-in in place of your spec; fix or move aside " + filepath.Join(p.Targets, "quill.yaml")))
		Expect(r).To(Equal(targets.Registry{}))
	})

	It("refuses two files that define one id, naming both", func() {
		write("a.yaml", userSpec("quill", ".quill/agents"))
		write("b.yaml", userSpec("quill", ".quill/other"))
		r, err := targets.Load(p)
		Expect(err).To(HaveOccurred())
		te, ok := errors.AsType[*targetspec.Error](err)
		Expect(ok).To(BeTrue())
		Expect(te.Field).To(Equal("id"))
		Expect(err.Error()).To(ContainSubstring(filepath.Join(p.Targets, "a.yaml")))
		Expect(err.Error()).To(ContainSubstring(filepath.Join(p.Targets, "b.yaml")))
		Expect(r).To(Equal(targets.Registry{}))
	})

	It("refuses a .yml file rather than skip it silently", func() {
		write("quill.yml", userSpec("quill", ".quill/agents"))
		r, err := targets.Load(p)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(filepath.Join(p.Targets, "quill.yml")))
		Expect(errs.Hints(err)).To(ContainElement("rename it to quill.yaml: lola reads target specs only from .yaml files"))
		Expect(r).To(Equal(targets.Registry{}))
	})

	It("ignores other files and directories beside the specs", func() {
		write("quill.yaml", userSpec("quill", ".quill/agents"))
		write("README.md", "notes")
		write("quill.yaml~", "not yaml: [")
		Expect(os.Mkdir(filepath.Join(p.Targets, "sub.yaml"), 0o755)).To(Succeed())
		r, err := targets.Load(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.IDs()).To(Equal(append(append([]string{}, builtinIDs...), "quill")))
	})

	It("skips a dot file, such as an editor's lock file, and a dangling symlink", func() {
		write("quill.yaml", userSpec("quill", ".quill/agents"))
		write(".quill.yaml", "not yaml: [")
		write(".quill.yml", "not yaml: [")
		Expect(os.Symlink("you@host.1234:1", filepath.Join(p.Targets, ".#quill.yaml"))).To(Succeed())
		Expect(os.Symlink("gone.yaml", filepath.Join(p.Targets, "dangling.yaml"))).To(Succeed())
		r, err := targets.Load(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.IDs()).To(Equal(append(append([]string{}, builtinIDs...), "quill")))
	})

	It("skips a spec name that is not a regular file, and reads one through a symlink", func() {
		write("real.txt", userSpec("quill", ".quill/agents"))
		Expect(os.Symlink("real.txt", filepath.Join(p.Targets, "quill.yaml"))).To(Succeed())
		mkfifo(filepath.Join(p.Targets, "pipe.yaml"))
		r, err := targets.Load(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.IDs()).To(Equal(append(append([]string{}, builtinIDs...), "quill")))
	})

	It("prints no raw control character from a file name or a file's content", func() {
		for _, name := range []string{"x\x1b[31m.yml", "x\x1b[31m.yaml"} {
			Expect(os.RemoveAll(p.Targets)).To(Succeed())
			write(name, "id: [\x1b[31m\n")
			r, err := targets.Load(p)
			Expect(err).To(HaveOccurred(), name)
			Expect(err.Error()).NotTo(ContainSubstring("\x1b"), name)
			Expect(err.Error()).To(ContainSubstring("x?[31m"), name)
			for _, h := range errs.Hints(err) {
				Expect(h).NotTo(ContainSubstring("\x1b"), name)
			}
			Expect(r).To(Equal(targets.Registry{}))
		}
	})

	It("refuses a target path that is a file", func() {
		Expect(os.MkdirAll(filepath.Dir(p.Targets), 0o755)).To(Succeed())
		Expect(os.WriteFile(p.Targets, []byte("x"), 0o600)).To(Succeed())
		r, err := targets.Load(p)
		Expect(err).To(MatchError(p.Targets + " is not a directory"))
		Expect(errs.Hints(err)).To(ContainElement("move it aside: lola reads your target specs from the directory " + p.Targets))
		Expect(r).To(Equal(targets.Registry{}))
	})

	It("refuses an unreadable target directory", func() {
		if os.Geteuid() == 0 {
			Skip("root reads any directory")
		}
		write("quill.yaml", userSpec("quill", ".quill/agents"))
		Expect(os.Chmod(p.Targets, 0o000)).To(Succeed())
		DeferCleanup(func() { Expect(os.Chmod(p.Targets, 0o755)).To(Succeed()) })
		r, err := targets.Load(p)
		Expect(err).To(HaveOccurred())
		Expect(strings.Contains(err.Error(), p.Targets)).To(BeTrue(), err.Error())
		Expect(r).To(Equal(targets.Registry{}))
	})
})
