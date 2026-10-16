package ledger_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("ledger", func() {
	var path string
	BeforeEach(func() { path = filepath.Join(testenv.Isolate(), ".lola", "lock.json") })

	It("loads a missing file as an empty ledger with [] arrays", func() {
		l, err := ledger.Load(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(l.Version).To(Equal(1))
		Expect(l.Save(path)).To(Succeed())
		Expect(os.ReadFile(path)).To(MatchJSON(`{"version":1,"modules":{},"created":[]}`))
	})

	It("writes deterministic, sorted output with mode 0600 and round-trips", func() {
		l := ledger.New()
		l.Modules["b"] = &ledger.Entry{Source: "/s/b", Digest: "sha256:2", Artifacts: []ledger.Artifact{
			{Target: "claude-code", Kind: "skill", Path: ".claude/skills/z/SKILL.md", SHA256: "1"},
			{Target: "claude-code", Kind: "mcp", Path: ".mcp.json", Key: "/mcpServers/gh", SHA256: "2"},
			{Target: "claude-code", Kind: "command", Path: ".claude/commands/a.md", SHA256: "3"},
		}}
		l.Modules["a"] = &ledger.Entry{Source: "/s/a", Digest: "sha256:1"}
		l.Created = []ledger.FileRef{{Target: "claude-code", Path: "CLAUDE.md"}, {Target: "claude-code", Path: ".mcp.json"}}
		Expect(l.Save(path)).To(Succeed())
		first, _ := os.ReadFile(path)
		Expect(l.Save(path)).To(Succeed())
		Expect(os.ReadFile(path)).To(Equal(first))
		fi, _ := os.Stat(path)
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))

		back, err := ledger.Load(path)
		Expect(err).NotTo(HaveOccurred())
		paths := []string{}
		for _, a := range back.Modules["b"].Artifacts {
			paths = append(paths, a.Path)
		}
		Expect(paths).To(Equal([]string{".claude/commands/a.md", ".claude/skills/z/SKILL.md", ".mcp.json"}))
		Expect(back.Modules["a"].Artifacts).To(Equal([]ledger.Artifact{}))
		Expect(back.Created[0].Path).To(Equal(".mcp.json"))
	})

	It("refuses a newer version and unknown fields", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		Expect(os.WriteFile(path, []byte(`{"version":2,"modules":{}}`), 0o600)).To(Succeed())
		_, err := ledger.Load(path)
		Expect(err).To(MatchError(ContainSubstring("newer lola")))
		Expect(os.WriteFile(path, []byte(`{"version":1,"modules":{},"extra":1}`), 0o600)).To(Succeed())
		_, err = ledger.Load(path)
		Expect(err).To(MatchError(ContainSubstring("extra")))
	})

	It("rejects malformed entries at load instead of panicking", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		for body, want := range map[string]string{
			`{"version":1,"modules":{"x":null}}`:                                                    "x",
			`{"version":1,"modules":{"bad\nname":{"digest":"d"}}}`:                                  "invalid",
			`{"version":1,"modules":{"x":{"digest":"d","artifacts":[{"kind":"file","path":"p"}]}}}`: "target",
		} {
			Expect(os.WriteFile(path, []byte(body), 0o600)).To(Succeed())
			_, err := ledger.Load(path)
			Expect(err).To(MatchError(ContainSubstring(path)), body)
			Expect(err).To(MatchError(ContainSubstring(want)), body)
		}
	})

	It("Clone is deep", func() {
		l := ledger.New()
		l.Modules["a"] = &ledger.Entry{Artifacts: []ledger.Artifact{{Path: "x"}}}
		c := l.Clone()
		c.Modules["a"].Artifacts[0].Path = "y"
		c.Modules["b"] = &ledger.Entry{}
		Expect(l.Modules["a"].Artifacts[0].Path).To(Equal("x"))
		Expect(l.Modules).NotTo(HaveKey("b"))
	})
})
