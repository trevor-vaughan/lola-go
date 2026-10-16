package source_test

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Parse", func() {
	var cwd string
	BeforeEach(func() {
		var err error
		cwd, err = filepath.EvalSymlinks(testenv.Isolate()) // locations come back canonical
		Expect(err).NotTo(HaveOccurred())
		Expect(os.MkdirAll(filepath.Join(cwd, "mods", "x"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(cwd, "file.txt"), nil, 0o644)).To(Succeed())
	})

	It("resolves relative and absolute directories", func() {
		s, ok, err := source.Parse("mods/x", cwd)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(s).To(Equal(source.Spec{Kind: source.KindFolder, Location: filepath.Join(cwd, "mods/x")}))
		s, ok, _ = source.Parse(filepath.Join(cwd, "mods", "x")+"/", "/")
		Expect(ok).To(BeTrue())
		Expect(s.Location).To(Equal(filepath.Join(cwd, "mods/x")))
	})

	It("accepts file:// URLs to directories", func() {
		s, ok, err := source.Parse("file://"+filepath.Join(cwd, "mods/x"), "/")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(s.Location).To(Equal(filepath.Join(cwd, "mods/x")))
	})

	It("canonicalizes a local location through symbolic links, once", func() {
		Expect(os.Symlink(filepath.Join(cwd, "mods"), filepath.Join(cwd, "link"))).To(Succeed())
		Expect(os.WriteFile(filepath.Join(cwd, "mods", "a.zip"), nil, 0o644)).To(Succeed())
		for arg, want := range map[string]string{
			"link/x":                                        filepath.Join(cwd, "mods", "x"),
			"link/../mods/x/":                               filepath.Join(cwd, "mods", "x"),
			filepath.Join(cwd, "link", "a.zip"):             filepath.Join(cwd, "mods", "a.zip"),
			"file://" + filepath.Join(cwd, "link/x"):        filepath.Join(cwd, "mods", "x"),
			"file://" + filepath.Join(cwd, "link", "a.zip"): filepath.Join(cwd, "mods", "a.zip"),
		} {
			s, ok, err := source.Parse(arg, cwd)
			Expect(err).NotTo(HaveOccurred(), arg)
			Expect(ok).To(BeTrue(), arg)
			Expect(s.Location).To(Equal(want), arg)
		}
		Expect(source.SameLocation(source.KindFolder, filepath.Join(cwd, "link", "x"), filepath.Join(cwd, "mods", "x"))).To(BeTrue(),
			"a location recorded through a link names the same folder")
		Expect(source.SameLocation(source.KindFolder, filepath.Join(cwd, "mods"), filepath.Join(cwd, "mods", "x"))).To(BeFalse())
		Expect(source.SameLocation(source.KindGit, "https://example.com/a.git", "https://example.com/b.git")).To(BeFalse())
		Expect(source.SameLocation(source.KindFolder, "link/x", filepath.Join(cwd, "mods", "x"))).To(BeFalse(),
			"a relative recorded location is never resolved against an unknown directory")
		Expect(source.SameLocation(source.KindFolder, filepath.Join(cwd, "gone", "x"), filepath.Join(cwd, "mods", "x"))).To(BeFalse(),
			"a recorded location that no longer exists names nothing")
	})

	It("tells whether a location is inside a directory, after symbolic links", func() {
		Expect(os.Symlink(filepath.Join(cwd, "mods"), filepath.Join(cwd, "link"))).To(Succeed())
		Expect(os.Symlink(cwd, filepath.Join(cwd, "mods", "up"))).To(Succeed())
		for child, want := range map[string]bool{
			filepath.Join(cwd, "mods", "x"):               true,
			filepath.Join(cwd, "link", "x"):               true,
			filepath.Join(cwd, "mods", "x", "new"):        true, // not there yet: resolved through its parent
			filepath.Join(cwd, "mods"):                    true,
			filepath.Join(cwd, "mods", "up"):              false, // a link leading out
			filepath.Join(cwd, "mods", "up", "mods", "x"): true,  // out and back in
			filepath.Join(cwd, "file.txt"):                false,
			filepath.Dir(cwd):                             false,
		} {
			in, err := source.IsWithin(filepath.Join(cwd, "mods"), child)
			Expect(err).NotTo(HaveOccurred(), child)
			Expect(in).To(Equal(want), child)
		}
		in, err := source.IsWithin(filepath.Join(cwd, "link"), filepath.Join(cwd, "mods", "x"))
		Expect(err).NotTo(HaveOccurred())
		Expect(in).To(BeTrue(), "the parent is resolved too")
		_, err = source.IsWithin(filepath.Join(cwd, "missing"), filepath.Join(cwd, "mods", "x"))
		Expect(err).To(HaveOccurred(), "a parent that cannot be resolved is an error, never an answer")
	})

	It("returns an error, never an answer, for a path it cannot place", func() {
		outside := filepath.Join(filepath.Dir(cwd), "outside-"+filepath.Base(cwd))
		Expect(os.MkdirAll(outside, 0o755)).To(Succeed())
		DeferCleanup(func() { Expect(os.RemoveAll(outside)).To(Succeed()) })
		Expect(os.Symlink(outside, filepath.Join(cwd, "mods", "out"))).To(Succeed())

		// out/.. is cwd's parent, shared with other runs: the missing name is this run's own.
		_, err := source.IsWithin(filepath.Join(cwd, "mods"), filepath.Join(cwd, "mods", "out")+"/../absent-"+filepath.Base(cwd))
		Expect(err).To(HaveOccurred(), "a missing path through a link and '..' is not cleaned lexically")

		_, err = source.IsWithin(filepath.Join(cwd, "mods"), "mods/x")
		Expect(err).To(HaveOccurred(), "a relative child cannot be compared with an absolute parent")

		if os.Geteuid() != 0 {
			sealed := filepath.Join(cwd, "mods", "sealed")
			Expect(os.Mkdir(sealed, 0o755)).To(Succeed())
			Expect(os.Symlink(outside, filepath.Join(sealed, "link"))).To(Succeed())
			Expect(os.Chmod(sealed, 0o000)).To(Succeed())
			DeferCleanup(func() { Expect(os.Chmod(sealed, 0o755)).To(Succeed()) })
			_, err = source.IsWithin(filepath.Join(cwd, "mods"), filepath.Join(sealed, "link", "x"))
			Expect(err).To(HaveOccurred(), "a path that cannot be examined is not inside")
		}
	})

	It("accepts an archive link whose target has the same suffix and refuses one that changes it", func() {
		blobs := filepath.Join(cwd, "blobs")
		Expect(os.MkdirAll(blobs, 0o755)).To(Succeed())
		for name, link := range map[string]string{"real.zip": "same.zip", "sha256-ab12": "blob.zip", "other.zip": "mod.tgz"} {
			Expect(os.WriteFile(filepath.Join(blobs, name), nil, 0o644)).To(Succeed())
			Expect(os.Symlink(filepath.Join(blobs, name), filepath.Join(cwd, link))).To(Succeed())
		}

		s, ok, err := source.Parse("same.zip", cwd)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(s.Location).To(Equal(filepath.Join(blobs, "real.zip")))

		_, _, err = source.Parse("blob.zip", cwd)
		Expect(err).To(MatchError(And(ContainSubstring("blob.zip"), ContainSubstring(filepath.Join(blobs, "sha256-ab12")), ContainSubstring("link"))))
		Expect(errs.Hints(err)).To(ConsistOf("lola install " + filepath.Join(blobs, "sha256-ab12")))

		_, _, err = source.Parse(filepath.Join(cwd, "mod.tgz"), cwd)
		Expect(err).To(MatchError(And(ContainSubstring("mod.tgz"), ContainSubstring(filepath.Join(blobs, "other.zip")))))
		Expect(errs.Hints(err)).To(ConsistOf("lola install " + filepath.Join(blobs, "other.zip")))
	})

	It("says 'not a source' for names and missing paths", func() {
		for _, arg := range []string{"example", "missing/dir", "missing.zip"} {
			_, ok, err := source.Parse(arg, cwd)
			Expect(err).NotTo(HaveOccurred(), arg)
			Expect(ok).To(BeFalse(), arg)
		}
	})

	It("rejects file:// URLs with a remote host, to a missing path or to a plain file", func() {
		_, _, err := source.Parse("file://example.com/x", cwd)
		Expect(err).To(MatchError(ContainSubstring("remote host")))
		_, _, err = source.Parse("file://"+filepath.Join(cwd, "missing.zip"), cwd)
		Expect(err).To(MatchError(ContainSubstring("no such directory or archive")))
		_, _, err = source.Parse("file://"+filepath.Join(cwd, "file.txt"), cwd)
		Expect(err).To(MatchError(ContainSubstring("not a directory or an archive")))
	})

	It("accepts archive files by suffix, case-insensitively, as paths or file:// URLs", func() {
		for _, name := range []string{"a.zip", "b.tar", "c.tar.gz", "d.TGZ"} {
			Expect(os.WriteFile(filepath.Join(cwd, name), nil, 0o644)).To(Succeed())
			s, ok, err := source.Parse(name, cwd)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(s).To(Equal(source.Spec{Kind: source.KindArchive, Location: filepath.Join(cwd, name)}))
			s, _, err = source.Parse("file://"+filepath.Join(cwd, name), "/")
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Kind).To(Equal(source.KindArchive))
		}
	})

	It("refuses an existing file that is not an archive, with a hint", func() {
		_, ok, err := source.Parse("file.txt", cwd)
		Expect(ok).To(BeFalse())
		Expect(err).To(MatchError("file.txt is a file, not a directory or an archive"))
		Expect(errs.Hints(err)).To(ContainElement("archive names end in .zip, .tar, .tar.gz or .tgz"))
	})
})

var _ = Describe("Parse refusals", func() {
	It("refuses an argument that starts with '-', with a hint", func() {
		cwd := testenv.Isolate()
		Expect(os.MkdirAll(filepath.Join(cwd, "-rf"), 0o755)).To(Succeed())
		_, ok, err := source.Parse("-rf", cwd)
		Expect(ok).To(BeFalse())
		Expect(err).To(MatchError(ContainSubstring("starts with '-'")))
		Expect(errs.Hints(err)).To(ContainElement("for a path, write ./-rf"))
	})

	It("quotes the path it suggests for an argument that starts with '-' for the shell", func() {
		cwd := testenv.Isolate()
		_, _, err := source.Parse("-my skills", cwd)
		Expect(err).To(MatchError(ContainSubstring("starts with '-'")))
		Expect(errs.Hints(err)).To(ContainElement("for a path, write " + errs.ShellJoin([]string{"./-my skills"})))
	})

	It("refuses a git remote helper, but not an IPv6 URL", func() {
		cwd := testenv.Isolate()
		_, _, err := source.Parse("fd::0", cwd)
		Expect(err).To(MatchError(ContainSubstring("remote helper")))
		Expect(errs.Hints(err)).To(ContainElement("sources are " + source.AcceptedForms))
		s, ok, err := source.Parse("http://[::1]/x.git", cwd)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(s.Kind).To(Equal(source.KindGit))
	})
})

var _ = Describe("Parse of URLs", func() {
	var cwd string
	BeforeEach(func() { cwd = testenv.Isolate() })

	It("accepts https archive URLs, and http ones only on a loopback host", func() {
		for _, arg := range []string{"https://example.com/m.zip", "https://example.com/dl/m-1.0.TAR.GZ", "http://localhost:8080/m.tgz",
			"http://127.0.0.1/m.tar", "http://127.9.9.9/m.zip", "http://[::1]:99/m.zip"} {
			s, ok, err := source.Parse(arg, cwd)
			Expect(err).NotTo(HaveOccurred(), arg)
			Expect(ok).To(BeTrue(), arg)
			Expect(s).To(Equal(source.Spec{Kind: source.KindArchive, Location: arg}))
		}
	})

	It("refuses plain http to any other host, without resolving names", func() {
		for _, arg := range []string{"http://example.com/m.zip", "http://localhost.example.com/m.zip", "http://127.1/m.zip",
			"http://[::ffff:127.0.0.1]/m.zip", "http://10.0.0.1/repo.git"} {
			_, _, err := source.Parse(arg, cwd)
			Expect(err).To(MatchError(ContainSubstring("plain http:// is allowed only for a loopback host")), arg)
			Expect(errs.Hints(err)).To(ContainElement("use https://"))
		}
	})

	It("refuses credentials, a query string or a fragment, without echoing them", func() {
		for _, arg := range []string{"https://token@example.com/m.zip", "https://u:s3cr3t@example.com/m.zip",
			"https://example.com/m.zip?X-Amz-Signature=s3cr3t", "https://example.com/m.zip#s3cr3t", "https://example.com/m.zip?",
			"file:///tmp/m.zip?private_token=s3cr3t", "file://tok3n@evil/x.zip", "https://tok3n@example.com/m.zip",
			"http://tok3n@example.com/m.zip"} {
			_, _, err := source.Parse(arg, cwd)
			Expect(err).To(MatchError(MatchRegexp("must not (carry credentials|have a query string or fragment)")), arg)
			Expect(err.Error()).NotTo(ContainSubstring("s3cr3t"), arg)
			Expect(err.Error()).NotTo(ContainSubstring("tok3n"), arg)
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("without credentials")), arg)
		}
	})
})

var _ = Describe("Parse of malformed URLs", func() {
	It("refuses a URL that does not parse without echoing its credentials or query, with a hint", func() {
		cwd := testenv.Isolate()
		for _, arg := range []string{"https://user:s3cr3t@github.com:44x/o/r.git", "https://h/%zz?token=s3cr3t", "x%zz:s3cr3t@h://r"} {
			_, _, err := source.Parse(arg, cwd)
			Expect(err).To(MatchError(ContainSubstring("not a valid URL")), arg)
			Expect(err.Error()).NotTo(ContainSubstring("s3cr3t"), arg)
			Expect(errs.Hints(err)).NotTo(BeEmpty(), arg)
		}
	})

	It("shows no part of an unparseable URL whose text before :// is not a scheme", func() {
		_, _, err := source.Parse("x%zz:s3cr3t@h://r", testenv.Isolate())
		Expect(err).To(MatchError("the URL is not a valid URL"))
	})

	It("shows only the scheme of an unparseable URL that holds an '@', wherever a '?' or '#' falls", func() {
		cwd := testenv.Isolate()
		for _, arg := range []string{"https://u:s3c?r3t@h/r", "https://u:s3c#r3t@h/r", "https://u:s3cr3t@h:44x/r"} {
			_, _, err := source.Parse(arg, cwd)
			Expect(err).To(MatchError(ContainSubstring("not a valid URL")), arg)
			Expect(err.Error()).To(HavePrefix("https://…"), arg)
			shown := strings.Join(append([]string{err.Error()}, errs.Hints(err)...), "\n")
			for _, secret := range []string{"s3c", "r3t", "s3cr3t"} {
				Expect(shown).NotTo(ContainSubstring(secret), arg)
			}
			Expect(errs.Hints(err)).NotTo(BeEmpty(), arg)
		}
	})
})

var _ = Describe("Parse of git sources", func() {
	var cwd string
	BeforeEach(func() { cwd = testenv.Isolate() })

	It("accepts https, ssh, scp-style and loopback http git URLs", func() {
		for _, arg := range []string{"https://github.com/o/r", "https://github.com/o/r.git", "ssh://git@github.com/o/r.git",
			"ssh://github.com:2222/o/r", "git@github.com:o/r.git", "github.com:o/r", "http://127.0.0.1:8080/r.git"} {
			s, ok, err := source.Parse(arg, cwd)
			Expect(err).NotTo(HaveOccurred(), arg)
			Expect(ok).To(BeTrue(), arg)
			Expect(s).To(Equal(source.Spec{Kind: source.KindGit, Location: arg}), arg)
		}
	})

	It("treats an existing x.git directory as a folder", func() {
		Expect(os.MkdirAll(filepath.Join(cwd, "x.git"), 0o755)).To(Succeed())
		s, ok, err := source.Parse("x.git", cwd)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(s.Kind).To(Equal(source.KindFolder))
	})

	It("never looks a URL or scp form up on disk, where a directory named like it could stand in for it", func() {
		for arg, dir := range map[string]string{"host:dir": "host:dir", "https://example.com/x.git": "https:/example.com/x.git",
			"http://127.0.0.1/x.git": "http:/127.0.0.1/x.git"} {
			Expect(os.MkdirAll(filepath.Join(cwd, dir), 0o755)).To(Succeed())
			s, ok, err := source.Parse(arg, cwd)
			Expect(err).NotTo(HaveOccurred(), arg)
			Expect(ok).To(BeTrue(), arg)
			Expect(s).To(Equal(source.Spec{Kind: source.KindGit, Location: arg}), arg)
		}
	})

	It("does not read a drive path or an unknown scheme as git", func() {
		for _, arg := range []string{"C:/x", "ftp://example.com/r.git", "host:/abs"} {
			_, ok, err := source.Parse(arg, cwd)
			Expect(err).NotTo(HaveOccurred(), arg)
			Expect(ok).To(BeFalse(), arg)
		}
	})

	It("refuses git://, with a hint, without echoing credentials or a query", func() {
		for _, arg := range []string{"git://github.com/o/r.git", "git://tok3n@github.com/o/r.git?x=s3cr3t#s3cr3t"} {
			_, _, err := source.Parse(arg, cwd)
			Expect(err).To(MatchError(ContainSubstring("unauthenticated and unencrypted")), arg)
			Expect(err.Error()).NotTo(Or(ContainSubstring("tok3n"), ContainSubstring("s3cr3t")), arg)
			Expect(errs.Hints(err)).To(ContainElement("use the repository's https:// or ssh:// URL"), arg)
		}
	})

	It("refuses credentials in a git URL, hinting at credential helpers, but allows ssh user names", func() {
		for _, arg := range []string{"https://ghp_s3cr3t@github.com/o/r.git", "ssh://git:s3cr3t@github.com/o/r.git",
			"https://github.com/o/r.git?private_token=s3cr3t", "git:s3cr3t@github.com:o/r.git", "u:a/s3cr3t@github.com:o/r",
			"github.com:o/r.git?private_token=s3cr3t", "git@github.com:o/r.git#s3cr3t"} {
			_, _, err := source.Parse(arg, cwd)
			Expect(err).To(HaveOccurred(), arg)
			Expect(err.Error()).NotTo(ContainSubstring("s3cr3t"), arg)
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("git credential helper")), arg)
		}
		s, ok, err := source.Parse("github.com:o/r@v2", cwd)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(s.Kind).To(Equal(source.KindGit))
	})

	It("refuses a query or fragment in a git URL with a hint that names them", func() {
		for _, arg := range []string{"https://github.com/o/r.git?x=1", "github.com:o/r.git#frag"} {
			_, _, err := source.Parse(arg, cwd)
			Expect(err).To(MatchError(ContainSubstring("query string or fragment")), arg)
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("query or fragment")), arg)
		}
	})

	It("refuses an ssh user name that ssh would read as an option", func() {
		_, _, err := source.Parse("ssh://-oProxyCommand=x@host/x", cwd)
		Expect(err).To(MatchError(ContainSubstring("user name starts with '-'")))
		Expect(errs.Hints(err)).NotTo(BeEmpty())
		_, _, err = source.Parse("-oProxyCommand=x@host:x", cwd) // scp form: the whole argument starts with '-'
		Expect(err).To(MatchError(ContainSubstring("starts with '-'")))
	})

	It("refuses a host that ssh would read as an option", func() {
		for _, arg := range []string{"git@-oProxyCommand:x", "ssh://-oProxyCommand=x/r"} {
			_, _, err := source.Parse(arg, cwd)
			Expect(err).To(MatchError(ContainSubstring("has no usable host")), arg)
		}
	})
})
