package source_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// entry is one archive member. typ is a tar type flag; for zip, a directory is a name ending
// in "/" and mode carries the file type.
type entry struct {
	name, body string
	mode       os.FileMode
	typ        byte
}

func file(name, body string) entry {
	return entry{name: name, body: body, mode: 0o644, typ: tar.TypeReg}
}

func writeTar(path string, gz bool, entries ...entry) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: int64(e.mode.Perm()), Typeflag: e.typ, Size: int64(len(e.body))}
		if e.typ != tar.TypeReg {
			hdr.Size, hdr.Linkname = 0, "/etc/passwd"
		}
		Expect(tw.WriteHeader(hdr)).To(Succeed())
		_, err := tw.Write([]byte(e.body))
		Expect(err).NotTo(HaveOccurred())
	}
	Expect(tw.Close()).To(Succeed())
	data := buf.Bytes()
	if gz {
		var z bytes.Buffer
		zw := gzip.NewWriter(&z)
		_, err := zw.Write(data)
		Expect(err).NotTo(HaveOccurred())
		Expect(zw.Close()).To(Succeed())
		data = z.Bytes()
	}
	Expect(os.WriteFile(path, data, 0o644)).To(Succeed())
}

func writeZip(path string, entries ...entry) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		hdr.SetMode(e.mode)
		w, err := zw.CreateHeader(hdr)
		Expect(err).NotTo(HaveOccurred())
		_, err = w.Write([]byte(e.body))
		Expect(err).NotTo(HaveOccurred())
	}
	Expect(zw.Close()).To(Succeed())
	Expect(os.WriteFile(path, buf.Bytes(), 0o644)).To(Succeed())
}

var _ = Describe("Fetch of an archive", func() {
	var tmp, staging string
	ctx := context.Background()
	BeforeEach(func() {
		tmp = testenv.Isolate()
		staging = filepath.Join(tmp, "staging")
	})
	fetch := func(archive, path string) (source.Fetched, error) {
		return source.Fetch(ctx, source.Spec{Kind: source.KindArchive, Location: archive, Path: path}, staging)
	}
	expectNothingStaged := func() {
		entries, _ := os.ReadDir(staging)
		Expect(entries).To(BeEmpty())
	}

	It("reads every format, named after the archive", func() {
		for _, name := range []string{"example.zip", "example.tar", "example.tar.gz", "example.TGZ"} {
			archive := filepath.Join(tmp, name)
			dir := func(name string) entry { return entry{name: name, mode: os.ModeDir | 0o755, typ: tar.TypeDir} }
			files := []entry{dir("./"), dir("commands/"), file("commands/a.md", "A"), file("./skills/s/SKILL.md", "S")}
			switch {
			case strings.HasSuffix(name, ".zip"):
				writeZip(archive, files...)
			default:
				writeTar(archive, !strings.HasSuffix(name, ".tar"), files...)
			}
			f, err := fetch(archive, "")
			Expect(err).NotTo(HaveOccurred(), name)
			Expect(f.Name).To(Equal("example"), name)
			Expect(listTree(f.Dir)).To(ConsistOf("commands/a.md", "skills/s/SKILL.md"), name)
			Expect(os.RemoveAll(f.Dir)).To(Succeed())
		}
		expectNothingStaged()
	})

	It("finds the module root by --path, markers, content directories, a single directory or the root", func() {
		archive := filepath.Join(tmp, "bundle.tar.gz")
		cases := []struct {
			path, name string
			entries    []entry
			want       []string
		}{
			{"plugins/review", "review", []entry{file("plugins/review/commands/a.md", "A"), file("plugins/other/lola.yml", "")}, []string{"commands/a.md"}},
			{"", "mod", []entry{file("x/mod/lola.yml", ""), file("x/mod/commands/a.md", "A"), file("x/README.md", ""), file("x/y/z/skills/s/SKILL.md", "S")},
				[]string{"lola.yml", "commands/a.md"}},
			{"", "plug", []entry{file("plug/.claude-plugin/plugin.json", "{}"), file("plug/agents/a.md", "A"), file("top.md", "")},
				[]string{".claude-plugin/plugin.json", "agents/a.md"}},
			{"", "repo-1.0", []entry{file("repo-1.0/rules/r.md", "R"), file("repo-1.0/docs/d.md", "D"), file("NOTICE", "")},
				[]string{"rules/r.md", "docs/d.md"}},
			{"", "only", []entry{file("only/README.md", "")}, []string{"README.md"}},
			{"", "bundle", []entry{file("a.md", ""), file("b/c.md", "")}, []string{"a.md", "b/c.md"}},
		}
		for _, c := range cases {
			writeTar(archive, true, c.entries...)
			f, err := fetch(archive, c.path)
			Expect(err).NotTo(HaveOccurred(), c.name)
			Expect(f.Name).To(Equal(c.name))
			Expect(listTree(f.Dir)).To(ConsistOf(c.want), c.name)
			Expect(os.RemoveAll(f.Dir)).To(Succeed())
		}
	})

	It("refuses two module roots at the same depth, hinting at --path", func() {
		archive := filepath.Join(tmp, "mono.zip")
		for _, entries := range [][]entry{
			{file("a/lola.yml", ""), file("b/plugin.json", "{}")},
			{file("a/skills/s/SKILL.md", ""), file("b/commands/c.md", "")},
		} {
			writeZip(archive, entries...)
			_, err := fetch(archive, "")
			Expect(err).To(MatchError("the archive holds several module roots at the same depth: a, b"))
			Expect(errs.Hints(err)).To(ContainElement("choose one with --path, e.g. --path a"))
		}
		expectNothingStaged()
	})

	It("quotes the module root in the --path hint for the user's shell", func() {
		archive := filepath.Join(tmp, "mono.zip")
		writeZip(archive, file("it's a/lola.yml", ""), file("z/plugin.json", "{}"))
		_, err := fetch(archive, "")
		Expect(err).To(MatchError("the archive holds several module roots at the same depth: it's a, z"))
		Expect(errs.Hints(err)).To(ConsistOf("choose one with --path, e.g. " + errs.ShellJoin([]string{"--path", "it's a"})))
		Expect(errs.Hints(err)).NotTo(ContainElement(ContainSubstring("--path it's a")))
	})

	It("refuses unsafe paths and every entry that is not a file or directory, keeping nothing", func() {
		cases := map[string]entry{
			"../evil.md":   file("../evil.md", "x"),
			"/abs.md":      file("/abs.md", "x"),
			`a\b.md`:       file(`a\b.md`, "x"),
			"link.md":      {name: "link.md", typ: tar.TypeSymlink},
			"hard.md":      {name: "hard.md", typ: tar.TypeLink},
			"dev":          {name: "dev", typ: tar.TypeChar},
			"fifo":         {name: "fifo", typ: tar.TypeFifo},
			"a/../../x.md": file("a/../../x.md", "x"),
		}
		archive := filepath.Join(tmp, "evil.tar")
		for name, bad := range cases {
			writeTar(archive, false, file("commands/a.md", "A"), bad)
			_, err := fetch(archive, "")
			Expect(err).To(MatchError(ContainSubstring(name+": ")), name)
		}
		zipCases := map[string]entry{
			"zlink.md": {name: "zlink.md", mode: os.ModeSymlink | 0o777, body: "/etc/passwd"},
			"zdev":     {name: "zdev", mode: os.ModeDevice | 0o644},
			"../z.md":  {name: "../z.md", mode: 0o644, body: "x"},
		}
		for name, bad := range zipCases {
			writeZip(filepath.Join(tmp, "evil.zip"), entry{name: "commands/a.md", body: "A", mode: 0o644}, bad)
			_, err := fetch(filepath.Join(tmp, "evil.zip"), "")
			Expect(err).To(MatchError(ContainSubstring(name+": ")), name)
		}
		expectNothingStaged()
		Expect(filepath.Join(tmp, "evil.md")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(tmp, "z.md")).NotTo(BeAnExistingFile())
	})

	It("refuses a path the archive holds twice", func() {
		archive := filepath.Join(tmp, "dup.tar")
		writeTar(archive, false, file("commands/a.md", "A"), file("commands/a.md", "B"))
		_, err := fetch(archive, "")
		Expect(err).To(MatchError("commands/a.md: the archive holds this path more than once"))
		expectNothingStaged()
	})

	It("refuses an entry longer than its header declares", func() {
		archive := filepath.Join(tmp, "long.zip")
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		body := []byte("much longer than one byte")
		w, err := zw.CreateRaw(&zip.FileHeader{Name: "commands/a.md", Method: zip.Store, CRC32: crc32.ChecksumIEEE(body),
			CompressedSize64: uint64(len(body)), UncompressedSize64: 1})
		Expect(err).NotTo(HaveOccurred())
		_, err = w.Write(body)
		Expect(err).NotTo(HaveOccurred())
		Expect(zw.Close()).To(Succeed())
		Expect(os.WriteFile(archive, buf.Bytes(), 0o644)).To(Succeed())
		_, err = fetch(archive, "")
		Expect(err).To(MatchError(HavePrefix("commands/a.md: ")))
		expectNothingStaged()
	})

	It("keeps the execute bit and drops every other mode bit", func() {
		archive := filepath.Join(tmp, "modes.tgz")
		writeTar(archive, true, entry{name: "commands/run.sh", body: "#!/bin/sh\n", mode: 0o4750, typ: tar.TypeReg},
			entry{name: "commands/a.md", body: "A", mode: 0o600, typ: tar.TypeReg})
		f, err := fetch(archive, "")
		Expect(err).NotTo(HaveOccurred())
		run, _ := os.Stat(filepath.Join(f.Dir, "commands/run.sh"))
		md, _ := os.Stat(filepath.Join(f.Dir, "commands/a.md"))
		Expect(run.Mode()).To(Equal(os.FileMode(0o755)))
		Expect(md.Mode()).To(Equal(os.FileMode(0o644)))
	})

	It("never copies or consults a .git directory inside an archive", func() {
		pwned := filepath.Join(tmp, "PWNED")
		Expect(os.WriteFile(filepath.Join(tmp, "bin", "git"), []byte("#!/bin/sh\ntouch "+pwned+"\n"), 0o755)).To(Succeed())
		archive := filepath.Join(tmp, "repo.tar")
		writeTar(archive, false, file("repo/.git/config", "[core]\n\tfsmonitor = touch "+pwned+"\n"),
			file("repo/.git/HEAD", "ref: refs/heads/main\n"), file("repo/commands/a.md", "A"))
		f, err := fetch(archive, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(listTree(f.Dir)).To(ConsistOf("commands/a.md"))
		Expect(pwned).NotTo(BeAnExistingFile())
	})

	It("sanitizes an escape sequence in an entry name", func() {
		archive := filepath.Join(tmp, "esc.tar")
		writeTar(archive, false, entry{name: "evil\x1b]0;owned\x07.md", typ: tar.TypeSymlink})
		_, err := fetch(archive, "")
		Expect(err).To(MatchError(HavePrefix("evil?]0;owned?.md: ")))
	})

	It("refuses --ref, with a hint", func() {
		archive := filepath.Join(tmp, "example.zip")
		writeZip(archive, entry{name: "commands/a.md", body: "A", mode: 0o644})
		_, err := source.Fetch(ctx, source.Spec{Kind: source.KindArchive, Location: archive, Ref: "v1"}, staging)
		Expect(err).To(MatchError(ContainSubstring("--ref applies to git sources only")))
		Expect(errs.Hints(err)).NotTo(BeEmpty())
	})

	It("refuses a --path that is not a directory in the archive", func() {
		archive := filepath.Join(tmp, "example.zip")
		writeZip(archive, entry{name: "commands/a.md", body: "A", mode: 0o644})
		for _, p := range []string{"../x", "missing", "commands/a.md"} {
			_, err := fetch(archive, p)
			Expect(err).To(MatchError(ContainSubstring("--path")), p)
		}
		expectNothingStaged()
	})
})

var _ = Describe("Fetch of an archive, error text", func() {
	var tmp, staging string
	ctx := context.Background()
	BeforeEach(func() {
		tmp = testenv.Isolate()
		staging = filepath.Join(tmp, "staging")
	})
	fetch := func(archive string) (source.Fetched, error) {
		return source.Fetch(ctx, source.Spec{Kind: source.KindArchive, Location: archive}, staging)
	}
	expectClean := func(err error) {
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).NotTo(MatchRegexp(`[\x00-\x09\x0b-\x1f\x7f]`))
		Expect(len(err.Error())).To(BeNumerically("<", 400))
	}

	It("keeps entry names out of raw OS errors", func() {
		for _, name := range []string{"e\x1b]0;x\x07", strings.Repeat("n", 300)} {
			archive := filepath.Join(tmp, "clash.tar")
			writeTar(archive, false, file(name, "x"), file(name+"/a.md", "x"))
			_, err := fetch(archive)
			expectClean(err)
		}
	})

	It("keeps the archive path out of raw OS errors", func() {
		_, err := fetch(filepath.Join(tmp, "no\x1b]0;x\x07such", strings.Repeat("d", 300), "a.zip"))
		expectClean(err)
		bad := filepath.Join(tmp, "bad\x1b]0;x\x07.zip")
		Expect(os.WriteFile(bad, []byte("not a zip"), 0o644)).To(Succeed())
		_, err = fetch(bad)
		expectClean(err)
	})

	It("keeps names out of folder copy errors", func() {
		dir := filepath.Join(tmp, "mod")
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		Expect(os.Symlink("/etc/passwd", filepath.Join(dir, "l\x1b]0;x\x07nk"))).To(Succeed())
		_, err := source.Fetch(ctx, source.Spec{Kind: source.KindFolder, Location: dir}, staging)
		expectClean(err)
	})

	It("refuses a location that is not a regular file without opening it", func() {
		fifo := filepath.Join(tmp, "pipe.tar")
		mkfifo(fifo)
		_, err := fetch(fifo)
		Expect(err).To(MatchError(ContainSubstring("not a regular file")))
		Expect(errs.Hints(err)).NotTo(BeEmpty())
	})

	It("does not let a huge declared compressed size trip the ratio check", func() {
		archive := filepath.Join(tmp, "huge.zip")
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, err := zw.CreateRaw(&zip.FileHeader{Name: "commands/a.md", Method: zip.Store, CRC32: crc32.ChecksumIEEE([]byte("x")),
			CompressedSize64: 1 << 62, UncompressedSize64: 1})
		Expect(err).NotTo(HaveOccurred())
		_, err = w.Write([]byte("x"))
		Expect(err).NotTo(HaveOccurred())
		Expect(zw.Close()).To(Succeed())
		Expect(os.WriteFile(archive, buf.Bytes(), 0o644)).To(Succeed())
		_, err = fetch(archive)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).NotTo(ContainSubstring("zip bomb"))
	})

	It("skips a tar global header but counts it as an entry", func() {
		archive := filepath.Join(tmp, "global.tar")
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		Expect(tw.WriteHeader(&tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader,
			PAXRecords: map[string]string{"comment": "abc"}})).To(Succeed())
		Expect(tw.WriteHeader(&tar.Header{Name: "commands/a.md", Mode: 0o644, Typeflag: tar.TypeReg, Size: 1})).To(Succeed())
		_, err := tw.Write([]byte("A"))
		Expect(err).NotTo(HaveOccurred())
		Expect(tw.Close()).To(Succeed())
		Expect(os.WriteFile(archive, buf.Bytes(), 0o644)).To(Succeed())
		f, err := fetch(archive)
		Expect(err).NotTo(HaveOccurred())
		Expect(listTree(f.Dir)).To(ConsistOf("commands/a.md"))
	})

	It("ignores ignored names when looking for the module root", func() {
		archive := filepath.Join(tmp, "nm.tar")
		writeTar(archive, false, file("node_modules/x/lola.yml", ""), file("mod/lola.yml", ""), file("mod/commands/a.md", "A"))
		f, err := fetch(archive)
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Name).To(Equal("mod"))
	})
})
