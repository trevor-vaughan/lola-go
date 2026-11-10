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

var _ = Describe("a run whose context is cancelled after its fetch (Ctrl+C or SIGTERM)", func() {
	It("stops before its first write, though its receipt was answered Yes, and returns the cancel", func() {
		tmp := testenv.Isolate()
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e := engine.New(p, targets.Builtin())
		src, proj := filepath.Join(tmp, "kit"), filepath.Join(tmp, "proj")
		writeTree(src, map[string]string{".claude-plugin/plugin.json": `{"name":"kit"}`, "commands/hi.md": "Hi\n",
			"hooks/hooks.json": `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/stop.sh"}]}]}}`})
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		asked := false
		_, err = e.Install(ctx, engine.Scope{Kind: target.Project, ProjectDir: proj}, proj,
			[]engine.InstallRequest{{Arg: src}}, engine.Options{Targets: []string{"claude-code"}, Trust: engine.TrustRuntimeHooks,
				Receipt: func([]engine.Consent) (bool, error) {
					asked = true
					cancel() // the signal arrives at the receipt, the last question before the first write
					return true, nil
				}})
		Expect(asked).To(BeTrue())
		Expect(err).To(MatchError(context.Canceled))
		var written []string
		Expect(filepath.WalkDir(proj, func(path string, _ os.DirEntry, err error) error {
			if path != proj {
				written = append(written, path)
			}
			return err
		})).To(Succeed())
		Expect(written).To(BeEmpty(), "nothing in the project")
	})
})
