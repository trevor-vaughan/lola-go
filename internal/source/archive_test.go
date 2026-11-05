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
// in "/" and mode carries the file type. link is a tar link's target, /etc/passwd when empty.
type entry struct {
	name, body, link string
	mode             os.FileMode
	typ              byte
}

// symlink is a link entry at name to target, for a tar (typ) or a zip (mode and body).
func symlink(name, target string) entry {
	return entry{name: name, link: target, body: target, mode: os.ModeSymlink | 0o777, typ: tar.TypeSymlink}
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
			if e.link != "" {
				hdr.Linkname = e.link
			}
		}
		Expect(tw.WriteHeader(hdr)).To(Succeed())
		if e.typ == tar.TypeReg {
			_, err := tw.Write([]byte(e.body))
			Expect(err).NotTo(HaveOccurred())
		}
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
			path, name, root string
			entries          []entry
			want             []string
		}{
			{"plugins/review", "review", "plugins/review", []entry{file("plugins/review/commands/a.md", "A"), file("plugins/other/lola.yml", "")},
				[]string{"commands/a.md"}},
			{"", "mod", "x/mod", []entry{file("x/mod/lola.yml", ""), file("x/mod/commands/a.md", "A"), file("x/README.md", ""),
				file("x/y/z/skills/s/SKILL.md", "S")}, []string{"lola.yml", "commands/a.md"}},
			{"", "plug", "plug", []entry{file("plug/.claude-plugin/plugin.json", "{}"), file("plug/agents/a.md", "A"), file("top.md", "")},
				[]string{".claude-plugin/plugin.json", "agents/a.md"}},
			{"", "repo-1.0", "repo-1.0", []entry{file("repo-1.0/rules/r.md", "R"), file("repo-1.0/docs/d.md", "D"), file("NOTICE", "")},
				[]string{"rules/r.md", "docs/d.md"}},
			{"", "only", "only", []entry{file("only/README.md", "")}, []string{"README.md"}},
			{"", "bundle", "", []entry{file("a.md", ""), file("b/c.md", "")}, []string{"a.md", "b/c.md"}},
		}
		for _, c := range cases {
			writeTar(archive, true, c.entries...)
			f, err := fetch(archive, c.path)
			Expect(err).NotTo(HaveOccurred(), c.name)
			Expect(f.Name).To(Equal(c.name))
			Expect(f.Root).To(Equal(c.root), "where the module root is in the archive, %s", c.name)
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

	It("refuses unsafe paths, absolute links and every entry that is not a file, directory or link, keeping nothing", func() {
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

	It("copies a tar or zip link to another file in the archive as that file, with its mode", func() {
		for _, name := range []string{"links.tar", "links.zip"} {
			archive := filepath.Join(tmp, name)
			entries := []entry{file("pkg/skills/s/SKILL.md", "S"), file("pkg/reference/x.md", "X"),
				{name: "pkg/tools/run.sh", body: "#!/bin/sh\n", mode: 0o755, typ: tar.TypeReg},
				symlink("pkg/skills/s/x.md", "../../reference/x.md"), symlink("pkg/skills/s/y.md", "x.md"),
				symlink("pkg/skills/s/run", "../../tools/run.sh")}
			if name == "links.zip" {
				writeZip(archive, entries...)
			} else {
				writeTar(archive, false, entries...)
			}
			f, err := fetch(archive, "")
			Expect(err).NotTo(HaveOccurred(), name)
			Expect(listTree(f.Dir)).To(ConsistOf("skills/s/SKILL.md", "skills/s/x.md", "skills/s/y.md", "skills/s/run", "reference/x.md", "tools/run.sh"), name)
			Expect(os.ReadFile(filepath.Join(f.Dir, "skills/s/x.md"))).To(Equal([]byte("X")), name)
			Expect(os.ReadFile(filepath.Join(f.Dir, "skills/s/y.md"))).To(Equal([]byte("X")), "a chain of links, %s", name)
			Expect(f.Executables).To(Equal([]string{"skills/s/run", "tools/run.sh"}), name)
			expectNoLinks(f.Dir)
			Expect(os.RemoveAll(f.Dir)).To(Succeed())
		}
	})

	It("refuses a link that is absolute, leaves the archive, dangles, loops, names a directory or .git, keeping nothing", func() {
		cases := []struct {
			reason string
			links  []entry
		}{
			{"commands/l.md: symbolic link refused: it points to /etc/passwd, an absolute path", []entry{symlink("commands/l.md", "/etc/passwd")}},
			{"commands/l.md: symbolic link refused: its target ../../x.md leaves the source", []entry{symlink("commands/l.md", "../../x.md")}},
			{"commands/l.md: symbolic link refused: its target does not exist", []entry{symlink("commands/l.md", "nope.md")}},
			{"commands/l.md: symbolic link refused: it is a loop or a chain of more than 8 links", []entry{symlink("commands/l.md", "m.md"), symlink("commands/m.md", "l.md")}},
			{"commands/l.md: symbolic link refused: its target is a directory", []entry{symlink("commands/l.md", "../skills")}},
			{"commands/l.md: symbolic link refused: its target is in .git/config, which lola never copies", []entry{symlink("commands/l.md", "../.git/config")}},
			{"commands/l.md: symbolic link refused: its target is in .Git/config, which lola never copies", []entry{symlink("commands/l.md", "../.Git/config")}},
			{"commands/l.md: symbolic link refused: its target is in GIT~1/config, which lola never copies", []entry{symlink("commands/l.md", "../GIT~1/config")}},
			{"commands/l.md: symbolic link refused: it goes through the linked directory zd", []entry{symlink("zd", "skills"), symlink("commands/l.md", "../zd/s.md")}},
		}
		for _, c := range cases {
			for _, name := range []string{"bad.tar", "bad.zip"} {
				archive := filepath.Join(tmp, name)
				entries := append([]entry{file("commands/a.md", "A"), file("skills/s.md", "S"), file(".git/config", "secret")}, c.links...)
				if name == "bad.zip" {
					writeZip(archive, entries...)
				} else {
					writeTar(archive, false, entries...)
				}
				_, err := fetch(archive, "")
				Expect(err).To(MatchError(source.ErrLinkInSource), "%s: %s", name, c.reason)
				Expect(err).To(MatchError(ContainSubstring(c.reason)), name)
				Expect(errs.Hints(err)).NotTo(BeEmpty(), "%s: %s", name, c.reason)
			}
		}
		expectNothingStaged()
	})

	Context("with a link lola refuses outside the module root", func() {
		write := func(name string, entries ...entry) string {
			archive := filepath.Join(tmp, name)
			if strings.HasSuffix(name, ".zip") {
				writeZip(archive, entries...)
			} else {
				writeTar(archive, true, entries...)
			}
			return archive
		}
		module := file("repo/mods/x/skills/demo/SKILL.md", "S")
		outside := symlink("repo/tests/abs", "/etc/hostname")

		It("installs the module, by --path or found, as a git or folder source would", func() {
			for _, name := range []string{"repo.tar.gz", "repo.zip"} {
				archive := write(name, module, outside)
				for _, p := range []string{"repo/mods/x", ""} {
					f, err := fetch(archive, p)
					Expect(err).NotTo(HaveOccurred(), "%s --path %q", name, p)
					Expect(f.Root).To(Equal("repo/mods/x"), name)
					Expect(listTree(f.Dir)).To(ConsistOf("skills/demo/SKILL.md"), name)
					Expect(os.RemoveAll(f.Dir)).To(Succeed())
				}
			}
		})

		It("still copies a link in the module to a file outside it", func() {
			for _, name := range []string{"repo.tar.gz", "repo.zip"} {
				archive := write(name, module, outside, file("repo/shared/ref.md", "R"),
					symlink("repo/mods/x/skills/demo/ref.md", "../../../../shared/ref.md"))
				f, err := fetch(archive, "repo/mods/x")
				Expect(err).NotTo(HaveOccurred(), name)
				Expect(os.ReadFile(filepath.Join(f.Dir, "skills/demo/ref.md"))).To(Equal([]byte("R")), name)
				expectNoLinks(f.Dir)
				Expect(os.RemoveAll(f.Dir)).To(Succeed())
			}
		})

		It("refuses the same link inside the module root, or one there whose chain goes through it, with its reason", func() {
			cases := map[string]entry{
				"repo/mods/x/tests/abs: symbolic link refused: it points to /etc/hostname, an absolute path":                   symlink("repo/mods/x/tests/abs", "/etc/hostname"),
				"repo/mods/x/skills/demo/h: symbolic link refused: it points to /etc/hostname, an absolute path":               symlink("repo/mods/x/skills/demo/h", "../../../../tests/abs"),
				"repo/mods/x/skills/demo/d: symbolic link refused: its target does not exist":                                  symlink("repo/mods/x/skills/demo/d", "nope.md"),
				"repo/mods/x/skills/demo/e: symbolic link refused: it is a loop or a chain of more than 8 links":               symlink("repo/mods/x/skills/demo/e", "e"),
				"repo/mods/x/skills/demo/g: symbolic link refused: its target is in repo/.git/config, which lola never copies": symlink("repo/mods/x/skills/demo/g", "../../../../.git/config"),
			}
			for reason, bad := range cases {
				for _, name := range []string{"repo.tar.gz", "repo.zip"} {
					archive := write(name, module, outside, file("repo/.git/config", "secret"), bad)
					for _, p := range []string{"repo/mods/x", ""} {
						_, err := fetch(archive, p)
						Expect(err).To(MatchError(source.ErrLinkInSource), "%s --path %q: %s", name, p, reason)
						Expect(err).To(MatchError(reason), "%s --path %q", name, p)
						Expect(errs.Hints(err)).NotTo(BeEmpty(), reason)
					}
				}
			}
			expectNothingStaged()
		})

		It("refuses a link it would refuse on the module path, or at a place that would choose the root", func() {
			cases := []struct {
				path, reason string
				entries      []entry
			}{
				{"repo/mods/x", "repo/mods: symbolic link refused: it points to /srv/mods, an absolute path",
					[]entry{file("repo/README.md", ""), symlink("repo/mods", "/srv/mods")}},
				{"", "repo/skills: symbolic link refused: it points to /srv/skills, an absolute path",
					[]entry{file("repo/tests/fixture/skills/s/SKILL.md", "S"), symlink("repo/skills", "/srv/skills")}},
				{"", "repo/lola.yml: symbolic link refused: it points to /srv/lola.yml, an absolute path",
					[]entry{file("repo/a/skills/s/SKILL.md", "S"), symlink("repo/lola.yml", "/srv/lola.yml")}},
				{"", "top: symbolic link refused: it points to /srv/top, an absolute path",
					[]entry{file("repo/README.md", ""), symlink("top", "/srv/top")}},
			}
			for _, c := range cases {
				for _, name := range []string{"repo.tar.gz", "repo.zip"} {
					_, err := fetch(write(name, c.entries...), c.path)
					Expect(err).To(MatchError(c.reason), "%s --path %q", name, c.path)
				}
			}
			expectNothingStaged()
		})
	})

	It("refuses a link that shares its path with another entry", func() {
		archive := filepath.Join(tmp, "dup.tar")
		writeTar(archive, false, file("commands/a.md", "A"), file("commands/l.md", "L"), symlink("commands/l.md", "a.md"))
		_, err := fetch(archive, "")
		Expect(err).To(MatchError("commands/l.md: the archive holds this path more than once"))
		writeTar(archive, false, file("commands/a.md", "A"), symlink("commands/l.md", "a.md"), file("commands/l.md", "L"))
		_, err = fetch(archive, "")
		Expect(err).To(MatchError("commands/l.md: the archive holds this path more than once"))
		expectNothingStaged()
	})

	It("refuses a zip link whose target is longer than 4096 bytes", func() {
		archive := filepath.Join(tmp, "long.zip")
		writeZip(archive, file("commands/a.md", "A"), symlink("commands/l.md", strings.Repeat("a/", 2049)))
		_, err := fetch(archive, "")
		Expect(err).To(MatchError(ContainSubstring("commands/l.md: symbolic link refused: its target is longer than 4096 bytes")))
		expectNothingStaged()
	})

	It("refuses a tar link with an empty target", func() {
		archive := filepath.Join(tmp, "empty.tar")
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		Expect(tw.WriteHeader(&tar.Header{Name: "commands/l.md", Typeflag: tar.TypeSymlink})).To(Succeed())
		Expect(tw.Close()).To(Succeed())
		Expect(os.WriteFile(archive, buf.Bytes(), 0o644)).To(Succeed())
		_, err := fetch(archive, "")
		Expect(err).To(MatchError("commands/l.md: symbolic link refused: its target is empty"))
		expectNothingStaged()
	})

	It("copies a link below a --path inside a name lola never copies", func() {
		archive := filepath.Join(tmp, "nm.tar")
		writeTar(archive, false, file("node_modules/pkg/commands/a.md", "A"), symlink("node_modules/pkg/commands/b.md", "a.md"))
		f, err := fetch(archive, "node_modules/pkg")
		Expect(err).NotTo(HaveOccurred())
		Expect(os.ReadFile(filepath.Join(f.Dir, "commands/b.md"))).To(Equal([]byte("A")))
	})

	It("ignores a link inside a name lola never copies", func() {
		archive := filepath.Join(tmp, "nm.tar")
		writeTar(archive, false, file("commands/a.md", "A"), symlink("node_modules/.bin/x", "/etc/passwd"))
		f, err := fetch(archive, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(listTree(f.Dir)).To(ConsistOf("commands/a.md"))
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

	It("records the execute bits the entries declare, relative to the module root", func() {
		tgz, zipped := filepath.Join(tmp, "pkg-1.tgz"), filepath.Join(tmp, "pkg-1.zip")
		entries := []entry{{name: "pkg-1/bin/run.sh", body: "#!/bin/sh\n", mode: 0o755, typ: tar.TypeReg},
			{name: "pkg-1/commands/a.md", body: "A", mode: 0o644, typ: tar.TypeReg}}
		writeTar(tgz, true, entries...)
		writeZip(zipped, entries...)
		for _, archive := range []string{tgz, zipped} {
			f, err := fetch(archive, "")
			Expect(err).NotTo(HaveOccurred())
			Expect(f.Executables).To(Equal([]string{"bin/run.sh"}), archive)
		}
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
