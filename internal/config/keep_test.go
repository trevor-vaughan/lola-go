package config_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("keep.yaml", func() {
	var dir, proj, path string
	BeforeEach(func() {
		var err error
		dir, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(dir, "work", "app")
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		path = filepath.Join(dir, "config", "lola", "keep.yaml")
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
	})
	write := func(s string) { Expect(os.WriteFile(path, []byte(s), 0o600)).To(Succeed()) }
	read := func() string {
		b, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}
	pats := func(ss ...string) []config.Pattern {
		out := []config.Pattern{}
		for _, s := range ss {
			p, err := config.ParsePattern(s)
			Expect(err).NotTo(HaveOccurred())
			out = append(out, p)
		}
		return out
	}

	It("reads each scope's selectors, and none when the file is missing", func() {
		k, err := config.LoadKeep(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(k.Entries(config.User())).To(BeEmpty())
		write("# mine\nversion: 1\nuser:\n  - skill/my-notes\n  - mcp/github\nprojects:\n  " + proj + ":\n    - command/deploy\n    - \"skill/scratch-*\"\n    - 'skill/:(glob)\\*'\n")
		k, err = config.LoadKeep(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(k.Entries(config.User())).To(Equal(pats("skill/my-notes", "mcp/github")))
		Expect(k.Entries(config.Project(proj))).To(Equal(pats("command/deploy", "skill/scratch-*", `skill/:(glob)\*`)))
		Expect(k.Entries(config.Project(filepath.Join(dir, "other")))).To(BeEmpty())
	})

	DescribeTable("a malformed keep.yaml refuses, naming the line",
		func(content func(proj string) string, msg, hint string) {
			write(content(proj))
			_, err := config.LoadKeep(path)
			Expect(err).To(MatchError(ContainSubstring(path + ":")))
			Expect(err).To(MatchError(ContainSubstring(msg)))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring(hint)))
		},
		Entry("YAML that does not parse", func(string) string { return "version: 1\nuser: [skill/a\n" }, "does not parse: yaml: line", "fix it by hand"),
		Entry("no version", func(string) string { return "user: [skill/a]\n" }, ":1: missing key version", "add version: 1"),
		Entry("an unknown section", func(string) string { return "version: 1\nglobal: [skill/a]\n" }, `:2: unknown key "global"`, "known keys: projects, user, version"),
		Entry("a mapping where a list belongs", func(string) string { return "version: 1\nuser:\n  m: [skill/a]\n" }, ":3: user: must be a list, not a mapping", "skill/my-notes"),
		Entry("a bad selector", func(string) string { return "version: 1\nuser:\n  - hook/x\n" }, `:3: user[0]: selector "hook/x" has an unknown kind`, "kind is one of skill"),
		Entry("a relative project key", func(string) string { return "version: 1\nprojects:\n  work/app: [skill/a]\n" }, "a project key must be an absolute path", "absolute path"),
		Entry("two keys for one project", func(p string) string {
			return "version: 1\nprojects:\n  " + p + ": [skill/a]\n  " + p + "/: [skill/b]\n"
		}, "names the same project as", "merge the two entries"),
	)

	It("--keep and --unkeep edit only their section, add no duplicate, keep comments, and a project reached through a link finds its canonical section", func() {
		Expect(os.Symlink(filepath.Join(dir, "work"), filepath.Join(dir, "link"))).To(Succeed())
		linked := filepath.Join(dir, "link", "app")
		write("# lola keep-list\nversion: 1\nuser:\n  - skill/my-notes # mine\nprojects:\n  " + linked + ": # the app\n    - command/deploy\n    - skill/old\n")
		Expect(config.SaveKeep(path, config.Project(proj), pats("skill/new", "command/deploy"), pats("skill/old"))).To(Succeed())
		Expect(read()).To(Equal("# lola keep-list\nversion: 1\nuser:\n  - skill/my-notes # mine\nprojects:\n  " + linked + ": # the app\n    - command/deploy\n    - skill/new\n"))

		Expect(config.SaveKeep(path, config.User(), pats(`skill/\*`), nil)).To(Succeed())
		k, err := config.LoadKeep(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(k.Entries(config.User())).To(Equal(pats("skill/my-notes", `skill/\*`)))
		Expect(k.Entries(config.Project(proj))).To(Equal(pats("command/deploy", "skill/new")))

		By("a section that loses its last entry goes, unless it carries a comment")
		Expect(config.SaveKeep(path, config.Project(proj), nil, pats("command/deploy", "skill/new"))).To(Succeed())
		Expect(read()).To(ContainSubstring(linked + ": # the app\n"))
		_, err = config.LoadKeep(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(config.SaveKeep(path, config.User(), nil, pats("skill/my-notes", `skill/\*`))).To(Succeed())
		Expect(read()).NotTo(ContainSubstring("user:"))
	})

	It("creates the file with its header at mode 0600, and writes a linked file at its target", func() {
		Expect(config.SaveKeep(path, config.Project(proj), pats("skill/notes"), nil)).To(Succeed())
		Expect(read()).To(Equal("# lola keep-list: what `lola tidy` never offers. Safe to edit by hand.\nversion: 1\nprojects:\n  " + proj + ":\n    - skill/notes\n"))
		fi, err := os.Stat(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))

		real := filepath.Join(dir, "dotfiles", "keep.yaml")
		Expect(os.MkdirAll(filepath.Dir(real), 0o700)).To(Succeed())
		Expect(os.Rename(path, real)).To(Succeed())
		Expect(os.Symlink(real, path)).To(Succeed())
		Expect(config.SaveKeep(path, config.User(), pats("mcp/github"), nil)).To(Succeed())
		fi, err = os.Lstat(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode() & os.ModeSymlink).NotTo(BeZero())
		Expect(read()).To(ContainSubstring("user:\n  - mcp/github\n"))
	})

	It("never overwrites a file that does not load", func() {
		write("version: 1\nuser: {m: x}\n")
		err := config.SaveKeep(path, config.User(), pats("skill/a"), nil)
		Expect(err).To(MatchError(ContainSubstring("not saving your keep-list: " + path)))
		Expect(read()).To(Equal("version: 1\nuser: {m: x}\n"))
	})
})
