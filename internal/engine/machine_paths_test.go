package engine_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("machine paths in a git-tracked file", func() {
	var (
		proj string
		e    *engine.Engine
	)
	git := func(args ...string) {
		out, err := exec.Command("git", append([]string{"-C", proj}, args...)...).CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
	}
	// install installs the example module, whose stdio server renders absolute paths into
	// .mcp.json, at sc, and returns the notices that name .mcp.json.
	install := func(sc engine.Scope) []engine.Notice {
		res, err := e.Install(context.Background(), sc, proj, []engine.InstallRequest{{Arg: filepath.Join(filepath.Dir(proj), "src", "example")}},
			engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
		var out []engine.Notice
		for _, n := range res.Notices {
			if n.Level == engine.LevelWarn && strings.Contains(n.Text, ".mcp.json is tracked by git") {
				out = append(out, n)
			}
		}
		return out
	}
	BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p, targets.Builtin())
		proj = filepath.Join(tmp, "proj")
		e.Getwd = func() (string, error) { return proj, nil }
		writeTree(filepath.Join(tmp, "src", "example"), map[string]string{
			"mcps.json": `{"mcpServers":{"lola-gh":{"command":"${PLUGIN_ROOT}/bin/gh","args":["--data","${PLUGIN_DATA}"]}}}`})
		writeTree(proj, map[string]string{".mcp.json": "{}\n"})
	})

	It("warns, naming the file and the member and hinting how to untrack it, when git tracks it", func() {
		git("init", "-q")
		git("add", ".mcp.json")
		git("-c", "user.name=lola-test", "-c", "user.email=lola-test@invalid", "commit", "-q", "-m", "x")
		notes := install(engine.Scope{Kind: target.Project, ProjectDir: proj})
		Expect(notes).To(HaveLen(1))
		Expect(notes[0].Text).To(ContainSubstring("lola-gh"))
		Expect(notes[0].Text).To(ContainSubstring("holds paths of this machine"))
		Expect(notes[0].Text).To(ContainSubstring("untrack it, then add it to .gitignore"))
		Expect(notes[0].Hints).To(Equal([]string{"git rm --cached -- .mcp.json"}))
		Expect(notes[0].Module).To(Equal("example"))
	})

	It("says nothing when git does not track the file", func() {
		git("init", "-q")
		Expect(install(engine.Scope{Kind: target.Project, ProjectDir: proj})).To(BeEmpty())
	})

	It("says nothing outside a git work tree", func() {
		Expect(install(engine.Scope{Kind: target.Project, ProjectDir: proj})).To(BeEmpty())
	})

	It("says nothing at user scope", func() {
		Expect(install(engine.Scope{Kind: target.User})).To(BeEmpty())
	})
})
