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

	It("refuses symlinks and leaves nothing staged", func() {
		writeTree(src, map[string]string{"commands/a.md": "A"})
		Expect(os.Symlink("/etc/passwd", filepath.Join(src, "commands", "b.md"))).To(Succeed())
		_, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src}, staging)
		Expect(err).To(MatchError(ContainSubstring("commands/b.md: symbolic links are not allowed")))
		entries, _ := os.ReadDir(staging)
		Expect(entries).To(BeEmpty())
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
		Expect(err).To(MatchError(ContainSubstring("module: symbolic links are not allowed")))
	})

	It("refuses a --path that goes through a symlink", func() {
		outside := filepath.Join(tmp, "outside")
		writeTree(outside, map[string]string{"review/commands/a.md": "A"})
		Expect(os.MkdirAll(src, 0o755)).To(Succeed())
		Expect(os.Symlink(outside, filepath.Join(src, "plugins"))).To(Succeed())
		_, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: src, Path: "plugins/review"}, staging)
		Expect(err).To(MatchError(ContainSubstring("plugins: symbolic links are not allowed")))
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
