package engine_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("scope context", func() {
	var (
		tmp, proj string
		p         paths.Paths
		e         *engine.Engine
		user, sc  engine.Scope
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		proj = filepath.Join(tmp, "proj")
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		e.Getwd = func() (string, error) { return proj, nil }
		user, sc = engine.Scope{Kind: target.User}, engine.Scope{Kind: target.Project, ProjectDir: proj}
	})
	// activate records name as the profile on in the lock.json at path.
	activate := func(path, name string) {
		GinkgoHelper()
		led := ledger.New()
		led.Profile = &ledger.Active{Name: name, By: ledger.ByOn}
		Expect(led.Save(path)).To(Succeed())
	}

	It("names the base, the profile on and the file an edit writes, at both scopes", func() {
		userBase := engine.ContextLayer{Name: "default", Kind: "base", Path: filepath.Join(p.UserProfiles, "default.yml"), Label: "default"}
		Expect(e.Context(user, nil)).To(Equal(engine.ScopeContext{Scope: "user", Root: "", Base: userBase}))
		Expect(e.Context(user, &engine.TargetRequest{}).WritesTo).To(Equal(&userBase), "with nothing on, an edit writes default.yml, the base")

		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		activate(p.UserLedger, "user:go")
		goLayer := engine.ContextLayer{Name: "user:go", Kind: "user", Path: filepath.Join(p.UserProfiles, "go.yml"), Label: "go"}
		Expect(e.Context(user, nil).Profile).To(Equal(&engine.ContextProfile{ContextLayer: goLayer, Members: []engine.ContextLayer{}}))
		Expect(e.Context(user, &engine.TargetRequest{}).WritesTo).To(Equal(&goLayer), "an edit writes the profile on")

		pm := filepath.Join(proj, ".lola", "project.yml")
		Expect(e.Context(sc, &engine.TargetRequest{})).To(Equal(engine.ScopeContext{Scope: "project", Root: proj,
			Base:     engine.ContextLayer{Name: "project.yml", Kind: "base", Path: pm, Label: "project.yml"},
			WritesTo: &engine.ContextLayer{Name: "project.yml", Kind: "base", Path: pm, Label: "project.yml"}}))
		activate(filepath.Join(proj, ".lola", "lock.json"), "user:go")
		Expect(e.Context(sc, &engine.TargetRequest{}).WritesTo).To(Equal(&goLayer))
	})

	It("names no file an edit writes while a remote profile is on, and the --profile named", func() {
		writeTree(p.UserProfiles, map[string]string{"me.yml": "version: 1\n"})
		// a remote profile as lola profile --add keeps it, so the context reads it
		body := "version: 1\n"
		o, err := json.Marshal(profile.Origin{Version: profile.OriginVersion, Source: "https://example.com/profiles.git",
			Commit: strings.Repeat("a", 40), Digest: profile.Digest([]byte(body))})
		Expect(err).NotTo(HaveOccurred())
		writeTree(filepath.Join(p.RemoteProfiles, "team"), map[string]string{profile.RemoteFile: body, profile.OriginFile: string(o)})
		activate(p.UserLedger, "remote:team")
		Expect(e.Context(user, &engine.TargetRequest{}).WritesTo).To(BeNil(), "the target needs the base question")
		Expect(e.Context(user, &engine.TargetRequest{Profile: "me"}).WritesTo).To(Equal(&engine.ContextLayer{Name: "user:me", Kind: "user",
			Path: filepath.Join(p.UserProfiles, "me.yml"), Label: "me"}))
		base := &engine.ContextLayer{Name: "default", Kind: "base", Path: filepath.Join(p.UserProfiles, "default.yml"), Label: "default"}
		Expect(e.Context(user, &engine.TargetRequest{Profile: "base"}).WritesTo).To(Equal(base))
		Expect(e.Context(user, &engine.TargetRequest{Profile: "default"}).WritesTo).To(Equal(base), "default is user scope's base")
		Expect(e.Context(user, &engine.TargetRequest{Profile: "user:default"}).WritesTo).To(Equal(base))
		Expect(e.Context(user, &engine.TargetRequest{Profile: "remote:team"}).WritesTo).To(BeNil(), "a remote profile is refused")
	})

	It("names a profile by its qualified name when another kind has its name", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		writeTree(proj, map[string]string{".lola/profiles/go.yml": "version: 1\n"})
		activate(filepath.Join(proj, ".lola", "lock.json"), "project:go")
		got := e.Context(sc, nil)
		Expect(got.Profile.ContextLayer).To(Equal(engine.ContextLayer{Name: "project:go", Kind: "project",
			Path: filepath.Join(proj, ".lola", "profiles", "go.yml"), Label: "project:go"}))
	})

	It("names the profile of yours read in place of a remote one that is on", func() {
		Expect(os.MkdirAll(filepath.Join(p.RemoteProfiles, "go"), 0o755)).To(Succeed())
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		activate(p.UserLedger, "remote:go")
		got := e.Context(user, nil)
		Expect(got.Profile.ContextLayer).To(Equal(engine.ContextLayer{Name: "user:go", Kind: "user",
			Path: filepath.Join(p.UserProfiles, "go.yml"), Label: "user:go"}), "remote:go is listed too, so the name is qualified")
		Expect(got.Error).To(BeEmpty())
	})

	It("says the profile on is gone, or that lock.json cannot be read, instead of refusing", func() {
		activate(p.UserLedger, "user:gone")
		got := e.Context(user, nil)
		Expect(got.Profile.ContextLayer).To(Equal(engine.ContextLayer{Name: "user:gone", Kind: "user", Path: "", Label: "user:gone"}))
		Expect(got.Error).To(Equal("profile user:gone no longer exists"))

		Expect(os.WriteFile(p.UserLedger, []byte("{"), 0o600)).To(Succeed())
		got = e.Context(user, nil)
		Expect(got.Profile).To(BeNil())
		Expect(got.Error).To(HavePrefix(p.UserLedger + ": "))
	})

	It("says why the profile on cannot be read instead of refusing", func() {
		path := filepath.Join(p.UserProfiles, "go.yml")
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\nnope: 1\n"})
		activate(p.UserLedger, "user:go")
		got := e.Context(user, nil)
		Expect(got.Profile.ContextLayer).To(Equal(engine.ContextLayer{Name: "user:go", Kind: "user", Path: path, Label: "go"}),
			"the profile is still named, so the line can say it is unreadable")
		Expect(got.Error).To(HavePrefix(path + ":"))
		Expect(got.Error).To(ContainSubstring("nope"))
	})

	It("passes the root and paths through untrusted.Text", func() {
		evil := filepath.Join(tmp, "evil\x1b[2J")
		Expect(os.MkdirAll(filepath.Join(evil, ".lola"), 0o755)).To(Succeed())
		got := e.Context(engine.Scope{Kind: target.Project, ProjectDir: evil}, nil)
		Expect(got.Root).To(Equal(filepath.Join(tmp, "evil?[2J")))
		Expect(got.Base.Path).To(Equal(filepath.Join(tmp, "evil?[2J", ".lola", "project.yml")))
	})
})
