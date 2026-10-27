package profile_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("remote profiles", func() {
	var (
		p              paths.Paths
		commit, digest string
		pinned         string
	)
	BeforeEach(func() {
		testenv.Isolate()
		var err error
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		commit, digest = strings.Repeat("a", 40), "sha256:"+strings.Repeat("b", 64)
		pinned = "version: 1\ntargets: [claude-code]\nmodules:\n  - name: gopls\n    source: https://example.com/gopls.git\n    ref: v1\n" +
			"    commit: " + commit + "\n    digest: " + digest + "\n"
	})
	symlinks := func() {
		if runtime.GOOS == "windows" {
			Skip("symbolic links need privileges on Windows")
		}
	}
	// keep writes a remote profile as lola profile --add does: profile.yml and its source.json.
	keep := func(name, content string, edit func(*profile.Origin)) string {
		GinkgoHelper()
		dir := filepath.Join(p.RemoteProfiles, name)
		Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
		o := profile.Origin{Version: 1, Source: "https://example.com/profiles.git", Commit: commit, Digest: profile.Digest([]byte(content))}
		if edit != nil {
			edit(&o)
		}
		b, err := json.Marshal(o)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(dir, profile.OriginFile), b, 0o600)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, profile.RemoteFile), []byte(content), 0o600)).To(Succeed())
		return filepath.Join(dir, profile.RemoteFile)
	}
	// fetched writes a source's tree and returns its profiles/, which the engine fetches.
	fetched := func(files map[string]string) string {
		GinkgoHelper()
		tree := GinkgoT().TempDir()
		for rel, content := range files {
			full := filepath.Join(tree, rel)
			Expect(os.MkdirAll(filepath.Dir(full), 0o700)).To(Succeed())
			Expect(os.WriteFile(full, []byte(content), 0o600)).To(Succeed())
		}
		return filepath.Join(tree, "profiles")
	}

	It("lists the remote profiles by directory, skipping lola's work directories, and counts invalid names", func() {
		keep("go", pinned, nil)
		keep("docs", "version: 1\n", nil)
		Expect(os.MkdirAll(filepath.Join(p.RemoteProfiles, ".new-go-123"), 0o700)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(p.RemoteProfiles, "Bad Name"), 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(p.RemoteProfiles, "stray.yml"), nil, 0o600)).To(Succeed())
		infos, invalid, err := profile.Remote(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(infos).To(Equal([]profile.Info{
			{Name: "docs", Kind: profile.KindRemote, Path: filepath.Join(p.RemoteProfiles, "docs", "profile.yml")},
			{Name: "go", Kind: profile.KindRemote, Path: filepath.Join(p.RemoteProfiles, "go", "profile.yml")},
		}))
		Expect(infos[1].Qualified()).To(Equal("remote:go"))
		Expect(invalid).To(Equal(1))

		Expect(os.RemoveAll(p.RemoteProfiles)).To(Succeed())
		infos, _, err = profile.Remote(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(infos).To(BeEmpty())
		for i := range 257 {
			Expect(os.MkdirAll(filepath.Join(p.RemoteProfiles, "p"+strings.Repeat("x", i%40)+string(rune('a'+i/40))), 0o700)).To(Succeed())
		}
		_, _, err = profile.Remote(p)
		Expect(err).To(MatchError(p.RemoteProfiles + " holds more than 256 files"))
	})

	It("loads a remote profile whose bytes are the ones its source.json pins", func() {
		path := keep("go", pinned, nil)
		pr, o, err := profile.LoadRemote(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(o).To(Equal(profile.Origin{Version: 1, Source: "https://example.com/profiles.git", Commit: commit, Digest: profile.Digest([]byte(pinned))}))
		Expect(pr.Entries()).To(Equal([]manifest.Entry{{Name: "gopls", Source: "https://example.com/gopls.git", Ref: "v1", Commit: commit,
			Digest: digest, Targets: []string{"claude-code"}}}))

		_, _, err = profile.LoadRemote(filepath.Join(p.RemoteProfiles, "gone", "profile.yml"))
		Expect(err).To(MatchError(fs.ErrNotExist))
	})

	It("a crafted profile file refuses at load", func() {
		hint := "lola wrote it when you added the profile; remove the profile and add it again"
		path := keep("go", pinned, nil)
		Expect(os.WriteFile(path, []byte(pinned+"description: changed\n"), 0o600)).To(Succeed())
		_, _, err := profile.LoadRemote(path)
		Expect(err).To(MatchError(path + " changed since you added it: its digest is not the one source.json records"))
		Expect(errs.Hints(err)).To(Equal([]string{hint}))

		for _, edit := range []struct {
			change func(*profile.Origin)
			want   string
		}{
			{func(o *profile.Origin) { o.Version = 2 }, "written by a newer lola (version 2)"},
			{func(o *profile.Origin) { o.Source, o.Commit = "/home/me/profiles.tar.gz", "" }, "/home/me/profiles.tar.gz is on this machine"},
			{func(o *profile.Origin) { o.Source = "https://10.0.0.1/profiles.git" }, "names the private address 10.0.0.1"},
			{func(o *profile.Origin) { o.Source, o.Commit = "file:///home/me/profiles.tar.gz", "" }, "file:///home/me/profiles.tar.gz is on this machine"},
			{func(o *profile.Origin) { o.Source = "ext::sh -c touch% PWNED" }, "names a git remote helper"},
			{func(o *profile.Origin) { o.Ref = "--upload-pack=touch PWNED" }, "starts with '-'"},
			{func(o *profile.Origin) { o.Commit = "" }, "commit is missing"},
			{func(o *profile.Origin) { o.Digest = "" }, "digest is missing"},
		} {
			path := keep("go", pinned, edit.change)
			_, _, err := profile.LoadRemote(path)
			Expect(err).To(MatchError(And(HavePrefix(filepath.Join(filepath.Dir(path), "source.json")), ContainSubstring(edit.want))), edit.want)
		}
		dir := filepath.Dir(keep("go", pinned, nil))
		Expect(os.WriteFile(filepath.Join(dir, "source.json"), []byte(`{"version":1,"shell":"x"}`), 0o600)).To(Succeed())
		_, _, err = profile.LoadRemote(path)
		Expect(err).To(MatchError(ContainSubstring(`unknown field "shell"`)))

		for content, want := range map[string]string{
			"version: 2\n":                "was written by a newer lola (profile version 2)",
			"version: 1\nshell: rm -rf\n": "field shell not found",
			"version: 1\nextends: [go]\n": ":2: a remote profile cannot extend another",
			"version: 1\ndescription: " + strings.Repeat("x", 257) + "\n": ":2: description is longer than 256 bytes",
		} {
			path := keep("bad", content, nil)
			_, _, err := profile.LoadRemote(path)
			Expect(err).To(MatchError(ContainSubstring(want)), content)
		}

		symlinks()
		path = keep("go", pinned, nil)
		real := keep("real", pinned, nil)
		Expect(os.Remove(path)).To(Succeed())
		Expect(os.Symlink(real, path)).To(Succeed())
		_, _, err = profile.LoadRemote(path)
		Expect(err).To(MatchError(path + " is a symbolic link; lola wrote a file there"))

		link := filepath.Join(p.RemoteProfiles, "linked")
		Expect(os.Symlink(filepath.Dir(real), link)).To(Succeed())
		_, _, err = profile.LoadRemote(filepath.Join(link, "profile.yml"))
		Expect(err).To(MatchError(ContainSubstring(link + " is not a directory lola wrote")))
		infos, _, err := profile.Remote(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(infos).NotTo(ContainElement(HaveField("Name", "linked")), "a link is no profile")
	})

	It("a remote profile with a local source or an unpinned entry refuses", func() {
		for content, want := range map[string]string{
			"version: 1\nmodules:\n  - {name: a, source: ../a}\n":                     ":3: module a: ../a is on this machine",
			"version: 1\nmodules:\n  - {name: a, source: /srv/a.tar.gz}\n":            ":3: module a: /srv/a.tar.gz is on this machine",
			"version: 1\nmodules:\n  - {name: a, source: \"file:///srv/a.tar.gz\"}\n": ":3: module a: file:///srv/a.tar.gz is on this machine",
			"version: 1\nmodules:\n  - {name: a, source: https://x/a.git}\n":          ":3: module a: it is not pinned; every entry of a remote profile carries its commit and digest",
			"version: 1\nmodules:\n  - {name: a, source: https://x/a.tar.gz}\n":       ":3: module a: it is not pinned; every entry of a remote profile carries its digest",
		} {
			tree := fetched(map[string]string{"profiles/go.yml": content})
			_, _, err := profile.Fetched(tree, "go")
			Expect(err).To(MatchError(And(HavePrefix(filepath.Join("profiles", "go.yml")), ContainSubstring(want))), content)
			Expect(errs.Hints(err)).To(ContainElement("ask the profile's author to fix it; lola did not add it"))
			if pin, ok := strings.CutPrefix(want, ":3: module a: it is not pinned; every entry of a remote profile carries its "); ok {
				Expect(errs.Hints(err)).To(ContainElement("pin it as project.yml pins it: "+pin), content)
			}

			_, _, err = profile.LoadRemote(keep("go", content, nil))
			Expect(err).To(MatchError(ContainSubstring(want)), content)
		}
		_, _, err := profile.Fetched(fetched(map[string]string{"profiles/go.yml": "version: 1\nmodules:\n  - {name: a, source: https://x/a.tar.gz, digest: " +
			digest + "}\n"}), "go")
		Expect(err).NotTo(HaveOccurred(), "an archive is pinned by its digest alone")
	})

	It("refuses a source a stranger's profile may not make lola fetch", func() {
		for src, want := range map[string]string{
			"http://example.com/a.git":        "http://example.com/a.git uses http://; a remote profile's sources must use https:// or ssh://",
			"git+ssh://example.com/a.git":     "uses git+ssh://",
			"https://127.0.0.1/a.git":         "names the loopback address 127.0.0.1",
			"https://127.0.0.1:8443/a.git":    "names the loopback address 127.0.0.1",
			"https://[::1]/a.git":             "names the loopback address ::1",
			"https://[::ffff:10.0.0.1]/a.git": "names the private address 10.0.0.1",
			"ssh://git@169.254.169.254/a.git": "names the link-local address 169.254.169.254",
			"git@10.1.2.3:a.git":              "names the private address 10.1.2.3",
			"https://192.168.1.1/a.tar.gz":    "names the private address 192.168.1.1",
			"https://0.0.0.0/a.git":           "names the unspecified address 0.0.0.0",
			"https://100.100.100.100/a.git":   "names the shared (carrier-grade NAT) address 100.100.100.100",
			"https://localhost:8443/a.git":    "names localhost, this machine",
			"https://git.localhost/a.git":     "names localhost, this machine",
			"https://2130706433/a.git":        "names the host 2130706433, which git and ssh read as an IP address",
			"https://0177.0.0.1/a.git":        "names the host 0177.0.0.1, which git and ssh read as an IP address",
			"https://0x7f.1/a.git":            "names the host 0x7f.1, which git and ssh read as an IP address",
			"https://0x7f000001/a.git":        "names the host 0x7f000001, which git and ssh read as an IP address",
			"https://127.1/a.git":             "names the host 127.1, which git and ssh read as an IP address",
			"https://0/a.git":                 "names the host 0, which git and ssh read as an IP address",
			"https://167772161/a.git":         "names the host 167772161, which git and ssh read as an IP address",
			"https://example.0x10/a.git":      "names the host example.0x10, which git and ssh read as an IP address",
			"git@2130706433:x":                "names the host 2130706433, which git and ssh read as an IP address",
			"https://localhost./a.git":        "names localhost, this machine",
			"https://localhost../a.git":       "names localhost, this machine",
			"git@localhost.:a.git":            "names localhost, this machine",
			"https://x.localhost./a.git":      "names localhost, this machine",
			"https://example.com/%zz":         "https://example.com/%zz is not a URL lola can read",
			"https://[::127.0.0.1]/a.git":     "names the loopback address 127.0.0.1",
			"https://[::10.0.0.1]/a.git":      "names the private address 10.0.0.1",
			"https://[64:ff9b::7f00:1]/a.git": "names the loopback address 127.0.0.1",
			"https://[64:ff9b:1::1]/a.git":    "names the private address 64:ff9b:1::1",
			"https://[fec0::1]/a.git":         "names the site-local address fec0::1",
			"/srv/profiles":                   "/srv/profiles is on this machine",
			"file:///srv/profiles.tar.gz":     "is on this machine",
		} {
			err := source.CheckRemote(src, profile.RemoteWho)
			Expect(err).To(MatchError(ContainSubstring(want)), src)
			Expect(errs.Hints(err)).To(Equal([]string{"use the repository's https:// or ssh:// URL (or user@host:path), with its host name"}), src)
		}
		for _, src := range []string{"https://example.com/a.git", "https://lola.test:8443/a.git", "ssh://git@example.com/a.git",
			"git@example.com:a.git", "https://8.8.8.8/a.git", "https://example.com/a.tar.gz", "https://1x.example.com/a.git",
			"https://example.com./a.git", "https://host-127.0.0.1.example/a.git", "https://8.8.8.8./a.git", "https://[64:ff9b::808:808]/a.git"} {
			Expect(source.CheckRemote(src, profile.RemoteWho)).To(Succeed(), src)
		}
		_, _, err := profile.Fetched(fetched(map[string]string{"profiles/go.yml": "version: 1\nmodules:\n  - {name: a, source: \"http://example.com/a.git\", commit: " +
			commit + ", digest: " + digest + "}\n"}), "go")
		Expect(err).To(MatchError(ContainSubstring(":3: module a: http://example.com/a.git uses http://")))
	})

	It("reads a fetched source's profiles, refusing a link before anything is read", func() {
		dir := fetched(map[string]string{"profiles/go.yml": pinned, "profiles/docs.yml": "version: 1\n", "profiles/Bad Name.yml": "",
			"profiles/README.md": "", "README.md": ""})
		names, invalid, err := profile.FetchedNames(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(names).To(Equal([]string{"docs", "go"}))
		Expect(invalid).To(Equal(1))
		pr, b, err := profile.Fetched(dir, "go")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal(pinned))
		Expect(pr.Modules).To(HaveLen(1))
		_, _, err = profile.Fetched(dir, "nope")
		Expect(err).To(MatchError("the source has no profile nope (profiles/nope.yml)"))
		_, _, err = profile.Fetched(fetched(map[string]string{"README.md": ""}), "go")
		Expect(err).To(MatchError("the source has no profiles/ directory"))
		_, _, err = profile.FetchedNames(fetched(map[string]string{"profiles": "a file"}))
		Expect(err).To(MatchError("profiles is not a directory"))

		symlinks()
		elsewhere := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(elsewhere, "go.yml"), []byte(pinned), 0o600)).To(Succeed())
		linkedFile := fetched(map[string]string{"profiles/docs.yml": ""})
		Expect(os.Symlink(filepath.Join(elsewhere, "go.yml"), filepath.Join(linkedFile, "go.yml"))).To(Succeed())
		_, _, err = profile.Fetched(linkedFile, "go")
		Expect(err).To(MatchError("profiles/go.yml: symbolic links are not allowed in a profile's source; lola never follows links in a source"))

		linkedDir := filepath.Join(GinkgoT().TempDir(), "profiles")
		Expect(os.Symlink(elsewhere, linkedDir)).To(Succeed())
		_, _, err = profile.Fetched(linkedDir, "go")
		Expect(err).To(MatchError("profiles: symbolic links are not allowed in a profile's source; lola never follows links in a source"))
		_, _, err = profile.FetchedNames(linkedDir)
		Expect(err).To(MatchError(ContainSubstring("profiles: symbolic links are not allowed")))
	})
})
