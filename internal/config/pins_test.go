package config_test

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("pins.yaml", func() {
	var path string
	commit, digest := strings.Repeat("a", 40), "sha256:"+strings.Repeat("b", 64)
	// q is how lola writes a value holding ':' in a flow mapping: single-quoted.
	q := func(s string) string { return "'" + s + "'" }
	BeforeEach(func() { path = filepath.Join(testenv.Isolate(), "pins.yaml") })
	const header = "# Written by lola: the versions installed from your profiles at user scope.\n" +
		"# Safe to sync between machines. Hand edits apply until lola next writes this file.\n"

	It("reads pins and remote records, finds a pin by its entry's key, and renders them back", func() {
		body := header + "version: 1\nmodules:\n  kit:\n" +
			"    - {source: " + q("https://example.com/kit.git") + ", ref: main, commit: " + commit + ", digest: " + q(digest) + "}\n" +
			"    - {source: " + q("https://example.com/kit.git") + ", ref: v2, commit: " + commit + ", digest: " + q(digest) + "}\n" +
			"  tarball:\n    - {source: " + q("https://example.com/t-1.0.tar.gz") + ", digest: " + q(digest) + "}\n" +
			"remotes:\n  go: {source: " + q("https://example.com/profiles.git") + ", ref: main, path: profiles/go, commit: " + commit + ", digest: " + q(digest) + "}\n"
		Expect(os.WriteFile(path, []byte(body), 0o600)).To(Succeed())
		p, err := config.LoadPins(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Find("kit", "https://example.com/kit.git", "v2", "")).To(Equal(&config.Pin{Source: "https://example.com/kit.git", Ref: "v2", Commit: commit, Digest: digest}))
		Expect(p.Find("kit", "https://example.com/kit.git", "v3", "")).To(BeNil(), "an edited entry resolves afresh")
		Expect(p.Remotes).To(Equal(map[string]config.Remote{"go": {Source: "https://example.com/profiles.git", Ref: "main", Path: "profiles/go",
			Commit: commit, Digest: digest}}))
		out, err := p.Bytes(p.Modules, p.Remotes)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(Equal(body), "lola renders what it reads, byte for byte")
		Expect(p.Same(out)).To(BeTrue())
	})

	It("reads an unquoted flow mapping a person wrote", func() {
		Expect(os.WriteFile(path, []byte("version: 1\nmodules:\n  t:\n    - {source: https://example.com/t.tar.gz, digest: "+digest+"}\n"), 0o600)).To(Succeed())
		p, err := config.LoadPins(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Find("t", "https://example.com/t.tar.gz", "", "")).To(Equal(&config.Pin{Source: "https://example.com/t.tar.gz", Digest: digest}))
	})

	It("starts a new file with lola's header, keeps a hand-written one, and holds nothing as the version alone", func() {
		p, err := config.LoadPins(path)
		Expect(err).NotTo(HaveOccurred())
		out, err := p.Bytes(nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(Equal(header + "version: 1\n"))
		Expect(os.WriteFile(path, []byte("# synced by chezmoi\nversion: 1\n"), 0o600)).To(Succeed())
		p, err = config.LoadPins(path)
		Expect(err).NotTo(HaveOccurred())
		out, err = p.Bytes(map[string][]config.Pin{"t": {{Source: "https://example.com/t.tar.gz", Digest: digest}}}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(Equal("# synced by chezmoi\nversion: 1\nmodules:\n  t:\n    - {source: " + q("https://example.com/t.tar.gz") + ", digest: " + q(digest) + "}\n"))
	})

	It("keeps a header whose comment lines blank lines separate, byte for byte", func() {
		const head = "# synced by chezmoi\n\n# do not edit on this machine\n\n"
		Expect(os.WriteFile(path, []byte(head+"version: 1\n"), 0o600)).To(Succeed())
		p, err := config.LoadPins(path)
		Expect(err).NotTo(HaveOccurred())
		out, err := p.Bytes(nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(Equal(head + "version: 1\n"))
		Expect(p.Same(out)).To(BeTrue(), "an unchanged file is no write")
	})

	It("leaves out a module with no pin left, so the file reads back as it was written", func() {
		p, err := config.LoadPins(path)
		Expect(err).NotTo(HaveOccurred())
		out, err := p.Bytes(map[string][]config.Pin{"gone": {}, "t": {{Source: "https://example.com/t.tar.gz", Digest: digest}}}, map[string]config.Remote{})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(Equal(header + "version: 1\nmodules:\n  t:\n    - {source: " + q("https://example.com/t.tar.gz") + ", digest: " + q(digest) + "}\n"))
		out, err = p.Bytes(map[string][]config.Pin{"gone": nil}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(Equal(header + "version: 1\n"))
	})

	It("checks a pin as a read does: a source on this machine, or one without its digest, is no pin", func() {
		Expect(config.Pin{Source: "https://example.com/kit.git", Ref: "main", Commit: commit, Digest: digest}.Check()).To(Succeed())
		Expect(config.Pin{Source: "/home/me/kit.tar.gz", Digest: digest}.Check()).To(MatchError("a source on this machine is never pinned; pins.yaml holds no machine paths"))
		Expect(config.Pin{Source: "https://example.com/kit.git", Commit: commit}.Check()).To(MatchError("digest is missing"))
	})

	DescribeTable("refuses a hostile pins.yaml, naming the line",
		func(body, want string) {
			Expect(os.WriteFile(path, []byte("version: 1\n"+body), 0o600)).To(Succeed())
			_, err := config.LoadPins(path)
			Expect(err).To(MatchError(HavePrefix(path + ":")))
			Expect(err).To(MatchError(ContainSubstring(want)))
			Expect(errs.Hints(err)).NotTo(BeEmpty())
		},
		Entry("a module name that is not one", "modules:\n  ../x:\n    - {source: https://e.com/x.git, commit: "+commit+", digest: "+digest+"}\n", ":3:"),
		Entry("a folder pinned", "modules:\n  x:\n    - {source: /home/me/x, digest: "+digest+"}\n", "a folder is never pinned"),
		Entry("a local archive pinned", "modules:\n  x:\n    - {source: ./x.tar.gz, digest: "+digest+"}\n", ":4:"),
		Entry("a file URL pinned", "modules:\n  x:\n    - {source: 'file:///tmp/x.tar.gz', digest: "+digest+"}\n", "a source on this machine is never pinned"),
		Entry("a pin without its digest", "modules:\n  x:\n    - {source: https://e.com/x.tar.gz}\n", "digest is missing"),
		Entry("a git pin without its commit", "modules:\n  x:\n    - {source: https://e.com/x.git, digest: "+digest+"}\n", "commit is missing"),
		Entry("a ref on an archive", "modules:\n  x:\n    - {source: https://e.com/x.tar.gz, ref: v1, digest: "+digest+"}\n", "only a git source has a ref"),
		Entry("a remote on this machine", "remotes:\n  go: {source: file:///etc, digest: "+digest+"}\n", ":3:"),
		Entry("a remote name that is not one", "remotes:\n  ../go: {source: https://e.com/p.git, commit: "+commit+", digest: "+digest+"}\n", ":3:"),
		Entry("an unknown key", "trust: all\n", ":2:"),
		Entry("a control character", "modules:\n  x:\n    - {source: \"https://e.com/x\\e.git\", commit: "+commit+", digest: "+digest+"}\n", "control"),
	)

	It("coaches a pin with no source without telling the reader to add a version number", func() {
		Expect(os.WriteFile(path, []byte("version: 1\nmodules:\n  x:\n    - {digest: "+digest+"}\n"), 0o600)).To(Succeed())
		_, err := config.LoadPins(path)
		Expect(err).To(MatchError(path + ":4: modules.x[0]: source is empty"))
		Expect(errs.Hints(err)).To(ConsistOf(ContainSubstring("fix it by hand, or remove it")))
	})

	It("refuses a write over a file changed since it was read", func() {
		Expect(os.WriteFile(path, []byte("version: 1\n"), 0o600)).To(Succeed())
		p, err := config.LoadPins(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Current()).To(Succeed())
		Expect(os.WriteFile(path, []byte("version: 1\nmodules: {}\n"), 0o600)).To(Succeed())
		Expect(p.Current()).To(MatchError(path + " changed while lola was reading it; nothing was written"))
	})
})
