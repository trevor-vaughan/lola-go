package engine

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("an entry's web page", func() {
	BeforeEach(func() { testenv.Isolate() })

	remote := func(location, ref, path string) source.Spec {
		return source.Spec{Kind: source.KindGit, Location: location, Ref: ref, Path: path}
	}

	DescribeTable("derives an http(s) address from the entry's remote source",
		func(spec source.Spec, want string) { Expect(webURL(spec)).To(Equal(want)) },
		Entry("https remote, .git stripped", remote("https://github.com/acme/tools.git", "", ""), "https://github.com/acme/tools"),
		Entry("https remote on no known host", remote("https://example.org/acme/tools.git", "main", "module"), "https://example.org/acme/tools"),
		Entry("GitHub tree for a path at a ref", remote("https://github.com/acme/tools.git", "main", "module/sub"), "https://github.com/acme/tools/tree/main/module/sub"),
		Entry("GitHub tree for a path with no ref", remote("https://github.com/acme/tools", "", "module"), "https://github.com/acme/tools/tree/HEAD/module"),
		Entry("GitHub repo alone for a ref and no path", remote("https://github.com/acme/tools.git", "v1", ""), "https://github.com/acme/tools"),
		Entry("GitLab tree", remote("https://gitlab.com/acme/tools.git", "main", "module"), "https://gitlab.com/acme/tools/-/tree/main/module"),
		Entry("a ref with a slash", remote("https://github.com/acme/tools.git", "feat/x", "m"), "https://github.com/acme/tools/tree/feat/x/m"),
		Entry("a path with a space is escaped", remote("https://github.com/acme/tools.git", "main", "my dir"), "https://github.com/acme/tools/tree/main/my%20dir"),
		Entry("a control character in a ref is escaped", remote("https://github.com/acme/tools.git", "main\nx", "m"), "https://github.com/acme/tools/tree/main%0Ax/m"),
		Entry("scp-style remote of a known host", remote("git@github.com:acme/tools.git", "main", "module"), "https://github.com/acme/tools/tree/main/module"),
		Entry("ssh URL of a known host, port dropped", remote("ssh://git@gitlab.com:22/acme/tools.git", "", ""), "https://gitlab.com/acme/tools"),
		Entry("Codeberg repo alone", remote("git@codeberg.org:acme/tools.git", "main", "module"), "https://codeberg.org/acme/tools"),
		Entry("Bitbucket repo alone", remote("ssh://git@bitbucket.org/acme/tools.git", "", ""), "https://bitbucket.org/acme/tools"),
		Entry("http stays http", remote("http://example.org/acme/tools.git", "", ""), "http://example.org/acme/tools"),
		Entry("credentials, query and fragment are dropped", remote("https://user:secret@example.org/acme/tools.git?x=1#f", "", ""), "https://example.org/acme/tools"),
		Entry("ssh remote of an unknown host has no known web page", remote("git@example.org:acme/tools.git", "", ""), ""),
		Entry("a folder", source.Spec{Kind: source.KindFolder, Location: "/srv/tools"}, ""),
		Entry("a file URL", remote("file:///srv/tools", "", ""), ""),
		Entry("an archive", source.Spec{Kind: source.KindArchive, Location: "https://example.org/tools.tar.gz"}, ""),
		Entry("a location with a newline", remote("https://example.org/a\nb", "", ""), ""),
		Entry("a location that is an option", remote("-oProxyCommand=x", "", ""), ""),
		Entry("a host with a command substitution", remote("https://a$(id)b.com/x/y", "", ""), ""),
		Entry("a host with a semicolon and quotes", remote("https://a;b'c\"d<e>.com/x", "", ""), ""),
		Entry("a host with a glob", remote("https://x*.com/x", "", ""), ""),
		Entry("a host with a backtick", remote("https://a`id`.com/x", "", ""), ""),
		Entry("a bracketed host", remote("https://[::1]/x", "", ""), ""),
		Entry("a scheme that is not http(s)", remote("javascript://github.com/acme/tools", "", ""), ""),
	)
})

var _ = Describe("an entry's source URL", func() {
	BeforeEach(func() { testenv.Isolate() })

	remote := func(location, ref, path string) source.Spec {
		return source.Spec{Kind: source.KindGit, Location: location, Ref: ref, Path: path}
	}

	DescribeTable("is the source location as it may be shown",
		func(spec source.Spec, want string) { Expect(entryURL(spec)).To(Equal(want)) },
		Entry("an https remote", remote("https://example.org/acme/tools.git", "", ""), "https://example.org/acme/tools.git"),
		Entry("an ssh remote in scp form, its user dropped", remote("git@example.org:acme/tools.git", "", ""), "example.org:acme/tools.git"),
		Entry("credentials, query and fragment dropped", remote("https://user:secret@example.org/acme/tools.git?x=1#f", "", ""), "https://example.org/acme/tools.git"),
		Entry("an archive URL", source.Spec{Kind: source.KindArchive, Location: "https://example.org/tools.tar.gz"}, "https://example.org/tools.tar.gz"),
		Entry("a folder", source.Spec{Kind: source.KindFolder, Location: "/srv/tools"}, ""),
		Entry("a file URL", remote("file:///srv/tools", "", ""), ""),
	)
})
