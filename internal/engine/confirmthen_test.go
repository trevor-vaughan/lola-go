package engine_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// An uninstall from a profile of yours on in a later scope too (lola profile --edit's module page)
// asks that scope's removals before it writes anything (Options.ConfirmThen).
var _ = Describe("Uninstall with ConfirmThen", func() {
	var (
		p                     paths.Paths
		e                     *engine.Engine
		user, sc              engine.Scope
		goYml, goBefore, lint string
		userLint, projLint    string
		ctx                   = context.Background()
		yes                   = func([]engine.Removal) (bool, error) { return true, nil }
	)
	BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p, targets.Builtin())
		proj := filepath.Join(tmp, "proj")
		e.Getwd = func() (string, error) { return proj, nil }
		user, sc = engine.Scope{Kind: target.User}, engine.Scope{Kind: target.Project, ProjectDir: proj}
		lint = filepath.Join(tmp, "lint")
		writeTree(lint, map[string]string{"lola.yml": "name: lint\n", "commands/lint.md": "L\n"})
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n"})
		goYml, goBefore = filepath.Join(p.UserProfiles, "go.yml"), "version: 1\nmodules:\n  - {name: lint, source: "+lint+"}\n"
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\n", "go.yml": goBefore})
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: user, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: "go"}, ConfirmRemovals: yes})
		Expect(err).NotTo(HaveOccurred())
		_, err = e.Sync(ctx, engine.SyncRequest{Scope: sc, Profiles: &engine.ProfileChange{Action: engine.ProfileSwitch, Name: "user:go"}, ConfirmRemovals: yes,
			Options: engine.Options{Trust: engine.TrustEntries | engine.TrustProfiles}})
		Expect(err).NotTo(HaveOccurred())
		userLint, projLint = filepath.Join(p.Home, ".claude", "commands", "lint.md"), filepath.Join(proj, ".claude", "commands", "lint.md")
		Expect(userLint).To(BeARegularFile())
		Expect(projLint).To(BeARegularFile())
	})
	// unticked is lola profile go --edit's removal of lint at the project, then applied at user scope.
	unticked := func(opts engine.Options) (*engine.Result, error) {
		opts.Target, opts.Then = engine.TargetRequest{Edit: true, Profile: "go"}, []engine.Scope{user}
		return e.Uninstall(ctx, sc, []string{"lint"}, opts)
	}
	untouched := func() {
		GinkgoHelper()
		Expect(os.ReadFile(goYml)).To(Equal([]byte(goBefore)))
		Expect(projLint).To(BeARegularFile())
		Expect(userLint).To(BeARegularFile())
	}

	It("asks a later scope's removals before anything is written, in a dry run too", func() {
		var asked []engine.Scope
		ask := func(o engine.Scope, rs []engine.Removal) (bool, error) {
			untouched()
			asked = append(asked, o)
			Expect(rs).To(ConsistOf(And(HaveField("Module", "lint"), HaveField("WholeModule", true))))
			return true, nil
		}
		_, err := unticked(engine.Options{DryRun: true, ConfirmThen: ask})
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(Equal([]engine.Scope{user}))
		untouched()

		_, err = unticked(engine.Options{ConfirmThen: ask})
		Expect(err).NotTo(HaveOccurred())
		Expect(asked).To(HaveLen(2))
		Expect(os.ReadFile(goYml)).To(Equal([]byte("version: 1\n")))
		Expect(projLint).NotTo(BeAnExistingFile())
		Expect(userLint).To(BeARegularFile(), "the later scope is the caller's to sync")
	})

	It("writes nothing in any scope on a No, which is ErrDeclined", func() {
		_, err := unticked(engine.Options{ConfirmThen: func(engine.Scope, []engine.Removal) (bool, error) { return false, nil }})
		Expect(err).To(MatchError(engine.ErrDeclined))
		untouched()
	})

	It("asks nothing under --force", func() {
		_, err := unticked(engine.Options{Force: true, ConfirmThen: func(engine.Scope, []engine.Removal) (bool, error) {
			Fail("--force asks nothing")
			return false, nil
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(projLint).NotTo(BeAnExistingFile())
	})

	It("does not ask about a later scope its edit removes nothing from", func() {
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: lint, source: " + lint + "}\n"})
		_, err := unticked(engine.Options{DryRun: true, ConfirmThen: func(engine.Scope, []engine.Removal) (bool, error) {
			Fail("the base still gives lint at user scope: nothing is removed there")
			return false, nil
		}})
		Expect(err).NotTo(HaveOccurred())
	})
})
