package source_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

func writeTree(root string, files map[string]string) {
	for p, c := range files {
		full := filepath.Join(root, p)
		Expect(os.MkdirAll(filepath.Dir(full), 0o755)).To(Succeed())
		Expect(os.WriteFile(full, []byte(c), 0o644)).To(Succeed())
	}
}

func listTree(root string) []string {
	var out []string
	Expect(filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		Expect(err).NotTo(HaveOccurred())
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})).To(Succeed())
	return out
}

// expectNoLinks fails when the tree at root holds a symbolic link: the cache never holds one.
func expectNoLinks(root string) {
	GinkgoHelper()
	Expect(filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		Expect(err).NotTo(HaveOccurred())
		Expect(d.Type()&os.ModeSymlink).To(BeZero(), p)
		return nil
	})).To(Succeed())
}

func git(dir string, args ...string) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))
}

var _ = Describe("Fetch of a folder", func() {
	var tmp, src, staging string
	ctx := context.Background()
	BeforeEach(func() {
		tmp = testenv.Isolate()
		src = filepath.Join(tmp, "example")
		staging = filepath.Join(tmp, "staging")
	})

	It("copies the module, skipping ignored directories, named after the folder", func() {
		writeTree(src, map[string]string{
			"commands/a.md": "A", "skills/s/SKILL.md": "S",
			"node_modules/x.js": "", ".venv/bin/py": "", "__pycache__/c.pyc": "", ".lola/lock.json": "", "sub/.DS_Store": "",
		})
		Expect(os.Chmod(filepath.Join(src, "commands/a.md"), 0o700)).To(Succeed())
		f, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src}, staging)
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Name).To(Equal("example"))
		Expect(listTree(f.Dir)).To(ConsistOf("commands/a.md", "skills/s/SKILL.md"))
		fi, _ := os.Stat(filepath.Join(f.Dir, "commands/a.md"))
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o755)))
		Expect(filepath.Dir(f.Dir)).To(Equal(staging))
	})

	It("records the files the file system marks executable, relative to the module root", func() {
		writeTree(src, map[string]string{"module/bin/run.sh": "#!/bin/sh\n", "module/commands/a.md": "A", "README.md": ""})
		Expect(os.Chmod(filepath.Join(src, "module/bin/run.sh"), 0o750)).To(Succeed())
		f, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src}, staging)
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Executables).To(Equal([]string{"bin/run.sh"}))
	})

	It("uses <root>/module/ when present and --path is not given", func() {
		writeTree(src, map[string]string{"README.md": "x", "module/commands/a.md": "A"})
		f, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src}, staging)
		Expect(err).NotTo(HaveOccurred())
		Expect(listTree(f.Dir)).To(ConsistOf("commands/a.md"))
		Expect(f.Name).To(Equal("example"))
		Expect(f.Root).To(Equal("module"), "where the module root is in the source")
	})

	It("takes --path . as the folder itself, not its module/, named after the folder", func() {
		writeTree(src, map[string]string{"commands/a.md": "A", "module/commands/b.md": "B"})
		for _, p := range []string{".", "./"} {
			f, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src, Path: p}, staging)
			Expect(err).NotTo(HaveOccurred(), p)
			Expect(f.Name).To(Equal("example"), p)
			Expect(listTree(f.Dir)).To(ConsistOf("commands/a.md", "module/commands/b.md"), p)
			Expect(f.Root).To(BeEmpty(), p)
		}
	})

	It("uses --path for monorepos and names the module after that directory", func() {
		writeTree(src, map[string]string{"plugins/review/commands/a.md": "A", "plugins/other/commands/b.md": "B"})
		f, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src, Path: "plugins/review"}, staging)
		Expect(err).NotTo(HaveOccurred())
		Expect(listTree(f.Dir)).To(ConsistOf("commands/a.md"))
		Expect(f.Name).To(Equal("review"))
	})

	It("rejects a --path that leaves the source", func() {
		writeTree(src, map[string]string{"commands/a.md": "A"})
		_, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src, Path: "../x"}, staging)
		Expect(err).To(MatchError(ContainSubstring("--path")))
	})

	It("coaches a --path that is not a directory, printing only the --path, made printable", func() {
		writeTree(src, map[string]string{"commands/a.md": "A"})
		for _, p := range []string{"nope\x1b[31mRED", "commands/a.md", "commands/a.md/x", "../x"} {
			_, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src, Path: p}, staging)
			Expect(err).To(MatchError(ContainSubstring("--path")), p)
			Expect(err.Error()).NotTo(ContainSubstring("\x1b"), p)
			Expect(err.Error()).NotTo(ContainSubstring(tmp), p)
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("--path is relative to")), p)
		}
		_, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src, Path: "nope\x1b[31mRED"}, staging)
		Expect(err).To(MatchError(ContainSubstring(`--path "nope?[31mRED"`)))
	})

	It("refuses an absolute symlink and leaves nothing staged", func() {
		writeTree(src, map[string]string{"commands/a.md": "A"})
		Expect(os.Symlink("/etc/passwd", filepath.Join(src, "commands", "b.md"))).To(Succeed())
		_, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src}, staging)
		Expect(err).To(MatchError(source.ErrLinkInSource))
		Expect(err).To(MatchError(ContainSubstring("commands/b.md: symbolic link refused: it points to /etc/passwd, an absolute path")))
		entries, _ := os.ReadDir(staging)
		Expect(entries).To(BeEmpty())
	})

	Context("with a symbolic link inside the source", func() {
		BeforeEach(func() {
			if runtime.GOOS == "windows" {
				Skip("symbolic links need privileges on Windows")
			}
		})
		link := func(at, to string) {
			GinkgoHelper()
			Expect(os.MkdirAll(filepath.Dir(filepath.Join(src, at)), 0o755)).To(Succeed())
			Expect(os.Symlink(to, filepath.Join(src, at))).To(Succeed())
		}

		It("copies a link to a file elsewhere in the folder as a regular file with the target's content and mode", func() {
			writeTree(src, map[string]string{"module/skills/s/SKILL.md": "S", "reference/x.md": "X", "tools/run.sh": "#!/bin/sh\n"})
			Expect(os.Chmod(filepath.Join(src, "tools/run.sh"), 0o755)).To(Succeed())
			link("module/skills/s/reference/x.md", "../../../../reference/x.md")
			link("module/bin/run", "../../tools/run.sh")
			f, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src}, staging)
			Expect(err).NotTo(HaveOccurred())
			Expect(listTree(f.Dir)).To(ConsistOf("skills/s/SKILL.md", "skills/s/reference/x.md", "bin/run"))
			Expect(os.ReadFile(filepath.Join(f.Dir, "skills/s/reference/x.md"))).To(Equal([]byte("X")))
			Expect(f.Executables).To(Equal([]string{"bin/run"}))
			expectNoLinks(f.Dir)
		})

		It("accepts a link that leaves the --path but stays in the folder", func() {
			writeTree(src, map[string]string{"module/commands/a.md": "A", "shared/b.md": "B"})
			link("module/commands/b.md", "../../shared/b.md")
			f, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src, Path: "module"}, staging)
			Expect(err).NotTo(HaveOccurred())
			Expect(listTree(f.Dir)).To(ConsistOf("commands/a.md", "commands/b.md"))
			Expect(os.ReadFile(filepath.Join(f.Dir, "commands/b.md"))).To(Equal([]byte("B")))
			expectNoLinks(f.Dir)
		})

		It("refuses a link that leaves the folder, a link into .git, and a link to a directory, leaving nothing staged", func() {
			outside := filepath.Join(tmp, "outside.md")
			Expect(os.WriteFile(outside, []byte("secret"), 0o644)).To(Succeed())
			for to, reason := range map[string]string{
				"../../outside.md":   "commands/b.md: symbolic link refused: its target ../../outside.md leaves the source",
				"../.git/config":     "commands/b.md: symbolic link refused: its target is in .git/config, which lola never copies",
				"../.lola/lock.json": "commands/b.md: symbolic link refused: its target is in .lola/lock.json, which lola never copies",
				"../skills":          "commands/b.md: symbolic link refused: its target is a directory",
			} {
				Expect(os.RemoveAll(src)).To(Succeed())
				writeTree(src, map[string]string{"commands/a.md": "A", "skills/s/SKILL.md": "S", ".git/config": "secret", ".lola/lock.json": "{}"})
				link("commands/b.md", to)
				_, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src}, staging)
				Expect(err).To(MatchError(source.ErrLinkInSource), to)
				Expect(err).To(MatchError(ContainSubstring(reason)), to)
				Expect(errs.Hints(err)).NotTo(BeEmpty(), to)
				entries, _ := os.ReadDir(staging)
				Expect(entries).To(BeEmpty(), to)
			}
		})

		It("copies a link below a --path inside a name lola never copies, and refuses one leaving it for another", func() {
			writeTree(src, map[string]string{"node_modules/pkg/commands/a.md": "A", "node_modules/other/x.md": "X"})
			link("node_modules/pkg/commands/b.md", "a.md")
			f, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src, Path: "node_modules/pkg"}, staging)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.ReadFile(filepath.Join(f.Dir, "commands/b.md"))).To(Equal([]byte("A")))
			expectNoLinks(f.Dir)
			link("node_modules/pkg/commands/x.md", "../../other/x.md")
			_, err = source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src, Path: "node_modules/pkg"}, staging)
			Expect(err).To(MatchError(ContainSubstring("its target is in node_modules/other/x.md, which lola never copies")))
		})

		It("copies a link from a module's git work tree to a file in the folder outside that work tree", func() {
			writeTree(src, map[string]string{"shared/b.md": "B", "mod/commands/a.md": "A", "mod/.gitignore": "secret.md\n", "mod/secret.md": "S"})
			git(filepath.Join(src, "mod"), "init", "-q")
			git(filepath.Join(src, "mod"), "add", ".")
			git(filepath.Join(src, "mod"), "commit", "-q", "-m", "init")
			link("mod/commands/b.md", "../../shared/b.md")
			f, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src, Path: "mod"}, staging)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.ReadFile(filepath.Join(f.Dir, "commands/b.md"))).To(Equal([]byte("B")))
			expectNoLinks(f.Dir)
			link("mod/commands/s.md", "../secret.md")
			_, err = source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src, Path: "mod"}, staging)
			Expect(err).To(MatchError(ContainSubstring("mod/commands/s.md: symbolic link refused: git ignores its target mod/secret.md")))
		})

		It("in a git work tree refuses a link to a file git ignores, and copies one to a tracked or new file", func() {
			writeTree(src, map[string]string{"commands/a.md": "A", ".gitignore": "secret.md\n", "ref/x.md": "X"})
			git(src, "init", "-q")
			git(src, "add", ".")
			git(src, "commit", "-q", "-m", "init")
			writeTree(src, map[string]string{"secret.md": "S", "ref/new.md": "N"})
			link("commands/x.md", "../ref/x.md")
			link("commands/new.md", "../ref/new.md")
			f, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src}, staging)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.ReadFile(filepath.Join(f.Dir, "commands/x.md"))).To(Equal([]byte("X")))
			Expect(os.ReadFile(filepath.Join(f.Dir, "commands/new.md"))).To(Equal([]byte("N")))
			expectNoLinks(f.Dir)

			link("commands/s.md", "../secret.md")
			_, err = source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src}, staging)
			Expect(err).To(MatchError(source.ErrLinkInSource))
			Expect(err).To(MatchError(ContainSubstring("commands/s.md: symbolic link refused: git ignores its target secret.md")))
			Expect(errs.Hints(err)).NotTo(BeEmpty())
		})
	})

	It("in a git work tree copies tracked and new unignored files, not ignored ones", func() {
		writeTree(src, map[string]string{"commands/a.md": "A", ".gitignore": "secret.md\n"})
		git(src, "init", "-q")
		git(src, "add", ".")
		git(src, "commit", "-q", "-m", "init")
		writeTree(src, map[string]string{"commands/new.md": "N", "secret.md": "S"})
		f, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src}, staging)
		Expect(err).NotTo(HaveOccurred())
		Expect(listTree(f.Dir)).To(ConsistOf("commands/a.md", "commands/new.md", ".gitignore"))
	})

	It("never runs a source repository's fsmonitor", func() {
		writeTree(src, map[string]string{"commands/a.md": "A"})
		git(src, "init", "-q")
		git(src, "add", ".")
		git(src, "commit", "-q", "-m", "init")
		pwned := filepath.Join(tmp, "PWNED")
		git(src, "config", "core.fsmonitor", "touch "+pwned+"; true")
		f, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src}, staging)
		Expect(err).NotTo(HaveOccurred())
		Expect(listTree(f.Dir)).To(ConsistOf("commands/a.md"))
		Expect(pwned).NotTo(BeAnExistingFile())
	})

	It("refuses a source that contains the staging directory", func() {
		writeTree(src, map[string]string{"commands/a.md": "A"})
		inner := filepath.Join(src, "inner")
		_, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src}, inner)
		Expect(err).To(MatchError(ContainSubstring("inside it")))
		_, err = os.Lstat(inner)
		Expect(err).To(HaveOccurred()) // inner must not exist
	})

	It("refuses a symlinked module/ directory", func() {
		outside := filepath.Join(tmp, "outside")
		writeTree(outside, map[string]string{"commands/a.md": "A"})
		Expect(os.MkdirAll(src, 0o755)).To(Succeed())
		Expect(os.Symlink(outside, filepath.Join(src, "module"))).To(Succeed())
		_, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src}, staging)
		Expect(err).To(MatchError(ContainSubstring("module: symbolic link refused: the module path goes through it")))
	})

	It("refuses a --path that goes through a symlink", func() {
		outside := filepath.Join(tmp, "outside")
		writeTree(outside, map[string]string{"review/commands/a.md": "A"})
		Expect(os.MkdirAll(src, 0o755)).To(Succeed())
		Expect(os.Symlink(outside, filepath.Join(src, "plugins"))).To(Succeed())
		_, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src, Path: "plugins/review"}, staging)
		Expect(err).To(MatchError(ContainSubstring("plugins: symbolic link refused: the module path goes through it")))
	})
})

var _ = Describe("Fetch of a selection of components", func() {
	It("stages exactly the skills, commands and agents Spec.Components selects, with their execute bits, refusing what it cannot select", func() {
		tmp := testenv.Isolate()
		src, staging := filepath.Join(tmp, "src"), filepath.Join(tmp, "staging")
		writeTree(src, map[string]string{"skills/xlsx/SKILL.md": "x", "skills/xlsx/run.sh": "#!/bin/sh\n", "skills/pptx/SKILL.md": "p",
			"tools/cmds/sum.md": "s", "agents/a.md": "a", "README.md": "r", "module/skills/other/SKILL.md": "o"})
		Expect(os.Chmod(filepath.Join(src, "skills", "xlsx", "run.sh"), 0o755)).To(Succeed())
		sel := &source.Components{Skills: []string{"skills/xlsx"}, Commands: []string{"tools/cmds/sum.md"}, Agents: []string{"agents/a.md"}}
		f, err := source.Fetch(context.Background(), source.Spec{Kind: source.KindFolder, Location: src, Components: sel}, staging)
		Expect(err).NotTo(HaveOccurred())
		Expect(listTree(f.Dir)).To(ConsistOf("skills/xlsx/SKILL.md", "skills/xlsx/run.sh", "commands/sum.md", "agents/a.md"),
			"from the directory itself, never its module/")
		if runtime.GOOS != "windows" {
			Expect(f.Executables).To(Equal([]string{"skills/xlsx/run.sh"}))
		}
		entries, err := os.ReadDir(staging)
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(HaveLen(1), "nothing but the selection is left in staging")

		for c, want := range map[*source.Components]string{
			{Skills: []string{"skills/docx"}}:                                       "does not hold skills/docx, which it selects among its skills",
			{Skills: []string{"README.md"}}:                                         "selects README.md among its skills, which is not a directory",
			{Skills: []string{"skills/../x"}}:                                       `skills path "skills/../x" is not a clean relative path`,
			{Skills: []string{"skills/xlsx", "skills/xlsx/a"}}:                      "skills/xlsx and skills/xlsx/a overlap",
			{Skills: []string{"skills/xlsx/a", "skills/xlsx"}}:                      "skills/xlsx and skills/xlsx/a overlap",
			{Skills: []string{"skills/xlsx"}, Agents: []string{"skills/xlsx/a.md"}}: "skills/xlsx and skills/xlsx/a.md overlap",
			{Skills: []string{"x/a"}, Commands: []string{"x/a"}}:                    "x/a and x/a overlap",
			{Commands: []string{"a/x.md", "b/x.md"}}:                                "commands lists two paths named x.md",
			{}:                                                                      "selects no skill, command or agent",
		} {
			_, err := source.Fetch(context.Background(), source.Spec{Kind: source.KindFolder, Location: src, Components: c}, staging)
			Expect(err).To(MatchError(ContainSubstring(want)), want)
		}
	})
})

var _ = Describe("Components as lock.json and trust.json record them", func() {
	It("leaves out a list it has none of, never writing null", func() {
		testenv.Isolate()
		b, err := json.Marshal(source.Components{Skills: []string{"skills/a"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal(`{"skills":["skills/a"]}`))
	})
})
