package source_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("Recorded.Check", func() {
	BeforeEach(func() { testenv.Isolate() })

	sha1 := strings.Repeat("a", 40)
	digest := "sha256:" + strings.Repeat("0", 64)

	It("accepts the fields lola writes", func() {
		for _, r := range []source.Recorded{
			{Source: "https://example.com/a.git", Ref: "v1", Path: "sub/dir", Commit: sha1, Digest: digest},
			{Source: "git@example.com:a/b.git", Commit: strings.Repeat("b", 64), Digest: digest},
			{Source: "../modules/x"},
			{Source: "https://example.com/x.tar.gz", Digest: digest},
			{Source: "ssh://git@example.com/a/b.git", Commit: sha1, Digest: digest},
			{Source: "https://example.com/a", Commit: sha1, Digest: digest},
		} {
			Expect(r.Check()).To(Succeed(), "%+v", r)
		}
	})

	DescribeTable("refuses a field lola never writes, naming it",
		func(r source.Recorded, msg string) {
			Expect(r.Check()).To(MatchError(ContainSubstring(msg)))
		},
		Entry("an empty source", source.Recorded{}, "source is empty"),
		Entry("a source over 4096 bytes", source.Recorded{Source: "https://x/" + strings.Repeat("a", 4096)}, "source is longer than 4096 bytes"),
		Entry("a source read as an option", source.Recorded{Source: "--upload-pack=touch PWNED"}, "starts with '-'"),
		Entry("a remote helper", source.Recorded{Source: "ext::sh -c touch PWNED"}, "git remote helper"),
		Entry("fd::0", source.Recorded{Source: "fd::0"}, "git remote helper"),
		Entry("git://", source.Recorded{Source: "git://example.com/a.git"}, "git:// source is refused"),
		Entry("GIT:// in upper case", source.Recorded{Source: "GIT://example.com/a.git"}, "git:// source is refused"),
		Entry("a remote helper of another transport", source.Recorded{Source: "persistent-https::example.com/a.git"}, "git remote helper"),
		Entry("a remote helper with an upper-case transport", source.Recorded{Source: "Ext::sh -c touch PWNED"}, "git remote helper"),
		Entry("a ref read as an option", source.Recorded{Source: "../x", Ref: "--upload-pack=touch PWNED"}, `ref "--upload-pack=touch PWNED" starts with '-'`),
		Entry("a ref over 256 bytes", source.Recorded{Source: "../x", Ref: strings.Repeat("r", 257)}, "ref is longer than 256 bytes"),
		Entry("a path leaving the source", source.Recorded{Source: "../x", Path: "../y"}, `path "../y" must be a relative path inside the source`),
		Entry("an absolute path", source.Recorded{Source: "../x", Path: "/etc"}, `path "/etc" must be a relative path inside the source`),
		Entry("an abbreviated commit", source.Recorded{Source: "../x", Commit: "abc123"}, `commit "abc123" is not a full commit SHA`),
		Entry("an upper-case commit", source.Recorded{Source: "../x", Commit: strings.Repeat("A", 40)}, "is not a full commit SHA"),
		Entry("a commit on an archive URL", source.Recorded{Source: "https://example.com/x.tar.gz", Commit: sha1, Digest: digest}, "commit is recorded only for a git source"),
		Entry("a commit on a local archive", source.Recorded{Source: "../x.zip", Commit: sha1, Digest: digest}, "commit is recorded only for a git source"),
		Entry("a commit on a folder", source.Recorded{Source: "../x", Commit: sha1, Digest: digest}, "commit is recorded only for a git source"),
		Entry("a commit on a file URL", source.Recorded{Source: "file:///srv/x", Commit: sha1, Digest: digest}, "commit is recorded only for a git source"),
		Entry("a digest without its algorithm", source.Recorded{Source: "../x", Digest: strings.Repeat("0", 64)}, "is not sha256: and 64 hex digits"),
		Entry("a short digest", source.Recorded{Source: "../x", Digest: "sha256:abc"}, "is not sha256: and 64 hex digits"),
		Entry("an upper-case digest", source.Recorded{Source: "../x", Digest: "sha256:" + strings.Repeat("A", 64)}, "is not sha256: and 64 hex digits"),
		Entry("a control character in the source", source.Recorded{Source: "../x\x1b[2J"}, "source holds control or invisible characters"),
		Entry("a newline in the ref", source.Recorded{Source: "../x", Ref: "v1\nv2"}, "ref holds control or invisible characters"),
		Entry("a bidi override in the path", source.Recorded{Source: "../x", Path: "a\u202eb"}, "path holds control or invisible characters"),
	)
})

var _ = Describe("Host", func() {
	BeforeEach(func() { testenv.Isolate() })

	It("names where a recorded location is fetched from", func() {
		for loc, want := range map[string]string{
			"https://GitHub.com/acme/a.git":      "https://github.com",
			"https://github.com:8443/acme/a.git": "https://github.com:8443",
			"http://127.0.0.1:9/a.git":           "http://127.0.0.1:9",
			"ssh://git@git.example.net/a.git":    "ssh://git.example.net",
			"git@git.example.net:acme/a.git":     "ssh://git.example.net",
			"mods/a":                             "",
			"/abs/a.zip":                         "",
			"file:///abs/a.zip":                  "",
			"https://github.com@evil.com/x":      "https://evil.com",
			"Git@GitHub.COM:a/b":                 "ssh://github.com",
		} {
			Expect(source.Host(loc)).To(Equal(want), loc)
		}
		Expect(source.Host("https://a.example/x.git")).NotTo(Equal(source.Host("https://b.example/x.git")))
		Expect(source.Host("https://a.example/x.git")).NotTo(Equal(source.Host("ssh://a.example/x.git")), "another scheme")
		Expect(source.Host("https://%zz/x.git")).To(Equal("https://%zz/x.git"), "an unparseable URL matches nothing else")
	})
})
