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
		cwd = testenv.Isolate()
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

	It("treats an existing x.git directory, or a path that looks like scp form, as a folder", func() {
		for _, dir := range []string{"x.git", "host:dir"} {
			Expect(os.MkdirAll(filepath.Join(cwd, dir), 0o755)).To(Succeed())
			s, ok, err := source.Parse(dir, cwd)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(s.Kind).To(Equal(source.KindFolder), dir)
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
