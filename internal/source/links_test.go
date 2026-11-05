package source_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("resolveLink", func() {
	var b string
	BeforeEach(func() {
		testenv.Isolate()
		if runtime.GOOS == "windows" {
			Skip("symbolic links need privileges on Windows")
		}
		b = GinkgoT().TempDir()
		writeTree(b, map[string]string{"ref/a.md": "A", "module/skills/s/x.md": "X", ".git/config": "secret", "d/keep": ""})
	})
	link := func(at, to string) {
		GinkgoHelper()
		Expect(os.MkdirAll(filepath.Dir(filepath.Join(b, at)), 0o755)).To(Succeed())
		Expect(os.Symlink(to, filepath.Join(b, at))).To(Succeed())
	}
	refused := func(at, reason string) {
		GinkgoHelper()
		_, err := source.ResolveLinkForTest(b, at)
		Expect(err).To(MatchError(source.ErrLinkInSource))
		Expect(err).To(MatchError(ContainSubstring(reason)))
		Expect(errs.Hints(err)).NotTo(BeEmpty(), "a refusal says what to change in the source")
	}

	It("resolves a relative link to a file elsewhere in the source", func() {
		link("module/skills/s/a.md", "../../../ref/a.md")
		Expect(source.ResolveLinkForTest(b, "module/skills/s/a.md")).To(Equal("ref/a.md"))
	})

	It("follows a chain of links", func() {
		link("m/1", "2")
		link("m/2", "../ref/a.md")
		Expect(source.ResolveLinkForTest(b, "m/1")).To(Equal("ref/a.md"))
	})

	It("follows a chain of 8 links and refuses one of 9", func() {
		for i := 1; i < 9; i++ {
			link(fmt.Sprintf("m/%d", i), fmt.Sprint(i+1))
		}
		link("m/9", "../ref/a.md")
		Expect(source.ResolveLinkForTest(b, "m/2")).To(Equal("ref/a.md"), "m/2 to m/9: 8 links")
		refused("m/1", "more than 8 links")
	})

	DescribeTable("refuses",
		func(at, to, reason string) {
			link(at, to)
			refused(at, reason)
		},
		Entry("an absolute link, naming its target", "m/abs", "/etc/passwd", "m/abs: symbolic link refused: it points to /etc/passwd, an absolute path"),
		Entry("a link that leaves the source, naming its target", "m/up", "../../outside", "m/up: symbolic link refused: its target ../../outside leaves the source"),
		Entry("a dangling link", "m/gone", "nope.md", "its target does not exist"),
		Entry("a link to a directory", "m/dir", "../d", "its target is a directory"),
		Entry("a link into .git", "m/git", "../.git/config", "its target is in .git/config, which lola never copies"),
		Entry("a link into .git in another letter case, as a case-insensitive file system resolves it", "m/git", "../.GIT/config",
			"its target is in .GIT/config, which lola never copies"),
		Entry("a link into node_modules in another letter case", "m/nm", "../Node_Modules/x.js", "its target is in Node_Modules/x.js, which lola never copies"),
		Entry("a link through a Windows short name", "m/short", "../GIT~1/config", "its target is in GIT~1/config, which lola never copies"),
	)

	It("refuses a link through a linked directory even when the directory link stays inside", func() {
		link("ld", "d")
		link("m/via", "../ld/keep")
		refused("m/via", "linked directory ld")
		link("gl", ".git")
		link("m/conf", "../gl/config")
		refused("m/conf", "linked directory gl")
	})

	It("refuses a chain that passes through an ignored name", func() {
		link(".git/hop", "../ref/a.md")
		link("m/x", "../.git/hop")
		refused("m/x", ".git/hop, which lola never copies")
	})

	It("refuses a loop", func() {
		link("m/a", "b")
		link("m/b", "a")
		refused("m/a", "loop")
	})

	It("refuses a link to something other than a regular file", func() {
		mkfifo(filepath.Join(b, "ref", "pipe"))
		link("m/p", "../ref/pipe")
		refused("m/p", "not a regular file")
	})

	It("refuses a link whose parent directory is a link that leaves the source", func() {
		outside := GinkgoT().TempDir()
		writeTree(outside, map[string]string{"f.md": "F"})
		Expect(os.Symlink(outside, filepath.Join(b, "esc"))).To(Succeed())
		link("m/via", "../esc/f.md")
		_, err := source.ResolveLinkForTest(b, "m/via")
		Expect(err).To(MatchError(source.ErrLinkInSource))
	})

	It("reports a link it cannot read as a read error, not a refusal of the source", func() {
		if os.Geteuid() == 0 {
			Skip("root reads a directory without search permission")
		}
		writeTree(b, map[string]string{"locked/a.md": "A"})
		link("m/l", "../locked/a.md")
		Expect(os.Chmod(filepath.Join(b, "locked"), 0)).To(Succeed())
		DeferCleanup(os.Chmod, filepath.Join(b, "locked"), os.FileMode(0o755))
		_, err := source.ResolveLinkForTest(b, "m/l")
		Expect(err).To(MatchError(HavePrefix("m/l: lola cannot read it: ")))
		Expect(err).NotTo(MatchError(source.ErrLinkInSource))
		Expect(errs.Hints(err)).NotTo(BeEmpty())
	})

	It("prints an author's link name and target made printable", func() {
		link("m/l\x1b]0;x\x07nk", "/e\x1b[31mtc")
		_, err := source.ResolveLinkForTest(b, "m/l\x1b]0;x\x07nk")
		Expect(err).To(MatchError(HavePrefix("m/l?]0;x?nk: symbolic link refused: it points to /e?[31mtc")))
	})
})
