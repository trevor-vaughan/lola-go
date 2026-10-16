package ledger_test

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// dg and dg2 are digests of the form the cache computes.
const (
	dg  = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	dg2 = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
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
		l.Modules["b"] = &ledger.Entry{Source: "/s/b", Digest: dg2, Artifacts: []ledger.Artifact{
			{Target: "claude-code", Kind: "skill", Path: ".claude/skills/z/SKILL.md", SHA256: "1"},
			{Target: "claude-code", Kind: "mcp", Path: ".mcp.json", Key: "/mcpServers/gh", SHA256: "2"},
			{Target: "claude-code", Kind: "command", Path: ".claude/commands/a.md", SHA256: "3"},
		}}
		l.Modules["a"] = &ledger.Entry{Source: "/s/a", Digest: dg}
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

	It("tells array members and created arrays apart from their file and round-trips them", func() {
		mine := ledger.Artifact{Target: "gemini-cli", Kind: "rule", Path: ".gemini/settings.json", Key: "/context/includeDirectories", Value: ".gemini/lola/a"}
		theirs := mine
		theirs.Value = ".gemini/lola/b"
		Expect(mine.ID()).NotTo(Equal(theirs.ID()))
		l := ledger.New()
		l.Modules["a"] = &ledger.Entry{Source: "/s/a", Digest: dg, Artifacts: []ledger.Artifact{mine}}
		l.Created = []ledger.FileRef{
			{Target: "gemini-cli", Path: ".gemini/settings.json", Key: "/context/includeDirectories"},
			{Target: "gemini-cli", Path: ".gemini/settings.json"},
		}
		Expect(l.Save(path)).To(Succeed())
		back, err := ledger.Load(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(back.Modules["a"].Artifacts).To(Equal([]ledger.Artifact{mine}))
		Expect(back.Created).To(Equal([]ledger.FileRef{
			{Target: "gemini-cli", Path: ".gemini/settings.json"},
			{Target: "gemini-cli", Path: ".gemini/settings.json", Key: "/context/includeDirectories"},
		}))
	})

	It("loads a ledger written before JSON-value members, and writes it back byte for byte", func() {
		written := `{
  "version": 1,
  "modules": {
    "a": {
      "source": "/s/a",
      "digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000",
      "artifacts": [
        {
          "target": "gemini-cli",
          "kind": "rule",
          "path": ".gemini/settings.json",
          "key": "/context/includeDirectories",
          "value": ".gemini/lola/a",
          "sha256": "9f"
        }
      ]
    }
  },
  "created": [
    {
      "target": "gemini-cli",
      "path": ".gemini/settings.json",
      "key": "/context/includeDirectories"
    }
  ]
}
`
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		Expect(os.WriteFile(path, []byte(written), 0o600)).To(Succeed())
		l, err := ledger.Load(path)
		Expect(err).NotTo(HaveOccurred())
		a := l.Modules["a"].Artifacts[0]
		Expect(a.Value).To(Equal(".gemini/lola/a"))
		Expect(a.Member).To(BeNil())
		Expect(string(a.Element())).To(Equal(`".gemini/lola/a"`))
		Expect(l.Save(path)).To(Succeed())
		Expect(os.ReadFile(path)).To(Equal([]byte(written)))
	})

	It("records a JSON-value member as canonical JSON and round-trips it", func() {
		group := ledger.Artifact{Target: "claude-code", Kind: "runtime-hook", Path: ".claude/settings.local.json", Key: "/hooks/Stop",
			Member: []byte(`{"hooks":[{"command":"x","type":"command"}]}`), SHA256: "1"}
		other := group
		other.Member = []byte(`{"hooks":[{"command":"y","type":"command"}]}`)
		Expect(group.ID()).NotTo(Equal(other.ID()))
		Expect(string(group.Element())).To(Equal(`{"hooks":[{"command":"x","type":"command"}]}`))
		Expect(ledger.Artifact{Path: "x"}.Element()).To(BeNil())
		l := ledger.New()
		l.Modules["a"] = &ledger.Entry{Source: "/s/a", Digest: dg, Artifacts: []ledger.Artifact{group, other}}
		Expect(l.Save(path)).To(Succeed())
		Expect(os.ReadFile(path)).To(ContainSubstring(`"member": {`))
		back, err := ledger.Load(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(back.Modules["a"].Artifacts).To(ConsistOf(group, other))
	})

	It("loads a member another tool reformatted, such as a pretty-printer writing && unescaped, and saves it back canonical", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		body := "{\n    \"version\": 1,\n    \"modules\": {\"a\": {\"source\": \"s\", \"digest\": \"" + dg + "\", \"artifacts\": [\n" +
			"        {\"target\": \"claude-code\", \"kind\": \"runtime-hook\", \"path\": \".claude/settings.local.json\", \"key\": \"/hooks/Stop\", \"sha256\": \"1\",\n" +
			"         \"member\": {\"matcher\": \"Bash\",\n            \"hooks\": [ {\"type\": \"command\", \"command\": \"a && b > c\"} ]}}]}},\n    \"created\": []\n}\n"
		Expect(os.WriteFile(path, []byte(body), 0o600)).To(Succeed())
		l, err := ledger.Load(path)
		Expect(err).NotTo(HaveOccurred())
		canonical := `{"hooks":[{"command":"a \u0026\u0026 b \u003e c","type":"command"}],"matcher":"Bash"}`
		Expect(string(l.Modules["a"].Artifacts[0].Member)).To(Equal(canonical))
		Expect(l.Save(path)).To(Succeed())
		back, err := ledger.Load(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(back.Modules["a"].Artifacts[0].Member)).To(Equal(canonical))
	})

	DescribeTable("refuses a member it could not have written",
		func(artifact, want string) {
			Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
			body := `{"version":1,"modules":{"a":{"source":"s","digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000","artifacts":[` + artifact + `]}},"created":[]}`
			Expect(os.WriteFile(path, []byte(body), 0o600)).To(Succeed())
			_, err := ledger.Load(path)
			Expect(err).To(MatchError(path + `: module "a" has an artifact (".claude/settings.local.json") ` + want))
		},
		Entry("a string", `{"target":"t","kind":"k","path":".claude/settings.local.json","key":"/hooks/Stop","member":"x"}`,
			"whose member is not a JSON object, array, number or boolean"),
		Entry("null", `{"target":"t","kind":"k","path":".claude/settings.local.json","key":"/hooks/Stop","member":null}`,
			"whose member is not a JSON object, array, number or boolean"),
		Entry("without a key", `{"target":"t","kind":"k","path":".claude/settings.local.json","member":{"a":1}}`,
			"with a member but no key, or with a value too"),
		Entry("with a value too", `{"target":"t","kind":"k","path":".claude/settings.local.json","key":"/k","value":"v","member":{"a":1}}`,
			"with a member but no key, or with a value too"),
	)

	It("coaches a refused member: restore lock.json, or reinstall", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		body := `{"version":1,"modules":{"a":{"source":"s","digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000","artifacts":[` +
			`{"target":"t","kind":"k","path":"p","key":"/hooks/Stop","member":"x"}]}},"created":[]}`
		Expect(os.WriteFile(path, []byte(body), 0o600)).To(Succeed())
		_, err := ledger.Load(path)
		Expect(errs.Hints(err)).To(ConsistOf(And(ContainSubstring("restore"), ContainSubstring("lock.json"), ContainSubstring("reinstall"))))
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
			`{"version":1,"modules":{"x":null}}`: "x",
			`{"version":1,"modules":{"bad\nname":{"source":"s","digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000"}}}`:                                  "invalid",
			`{"version":1,"modules":{"x":{"source":"s","digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000","artifacts":[{"kind":"file","path":"p"}]}}}`: "target",
		} {
			Expect(os.WriteFile(path, []byte(body), 0o600)).To(Succeed())
			_, err := ledger.Load(path)
			Expect(err).To(MatchError(ContainSubstring(path)), body)
			Expect(err).To(MatchError(ContainSubstring(want)), body)
		}
	})

	It("records the active profiles in order and each module's winning profile, and leaves both out when empty", func() {
		l := ledger.New()
		l.Modules["a"] = &ledger.Entry{Source: "/s/a", Digest: dg}
		Expect(l.Save(path)).To(Succeed())
		saved, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(saved)).To(Equal("{\n  \"version\": 1,\n  \"modules\": {\n    \"a\": {\n      \"source\": \"/s/a\",\n      \"digest\": \"" + dg + "\",\n      \"artifacts\": []\n    }\n  },\n  \"created\": []\n}\n"))

		l.Profiles = []ledger.Active{{Name: "user:base", By: ledger.ByOn}, {Name: "remote:go", By: ledger.BySwitch}}
		l.Modules["a"].From = ledger.FromProfile("user:base")
		l.Modules["b"] = &ledger.Entry{From: ledger.FromProfile("default"), Source: "/s/b", Digest: dg2}
		Expect(l.Save(path)).To(Succeed())
		Expect(os.ReadFile(path)).To(MatchJSON(`{"version":1,
			"profiles":[{"name":"user:base","by":"on"},{"name":"remote:go","by":"switch"}],
			"modules":{"a":{"from":"profile:user:base","source":"/s/a","digest":"` + dg + `","artifacts":[]},
			           "b":{"from":"profile:default","source":"/s/b","digest":"` + dg2 + `","artifacts":[]}},
			"created":[]}`))
		back, err := ledger.Load(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(back.Profiles).To(Equal(l.Profiles))
		Expect(back.Modules["b"].From).To(Equal("profile:default"))
		c := back.Clone()
		c.Profiles[0].Name = "user:other"
		Expect(back.Profiles[0].Name).To(Equal("user:base"))
	})

	DescribeTable("refuses an active list lola never writes",
		func(profiles, want string) {
			Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
			Expect(os.WriteFile(path, []byte(`{"version":1,"profiles":`+profiles+`,"modules":{},"created":[]}`), 0o600)).To(Succeed())
			_, err := ledger.Load(path)
			Expect(err).To(MatchError(And(ContainSubstring(path+": profiles"), ContainSubstring(want))))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("remove its profiles list")))
		},
		Entry("a bare name", `[{"name":"go","by":"on"}]`, "is not user:, project: or remote:"),
		Entry("default", `[{"name":"user:default","by":"on"}]`, "default is always on"),
		Entry("a name listed twice", `[{"name":"user:go","by":"on"},{"name":"user:go","by":"switch"}]`, "user:go is listed twice"),
		Entry("an unknown action", `[{"name":"user:go","by":"pull"}]`, `has by "pull"`),
		Entry("an invalid name", `[{"name":"user:../x","by":"on"}]`, "is invalid"),
		Entry("too many", `[`+strings.TrimSuffix(strings.Repeat(`{"name":"user:x","by":"on"},`, 65), ",")+`]`, "more than 64 profiles"),
	)

	It("refuses a from lola never writes", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		for _, from := range []string{"project.yml", "default", "profile:", "profile:go", "profile:user:../x", "user:go"} {
			body := `{"version":1,"modules":{"kit":{"from":"` + from + `","source":"/s","digest":"` + dg + `","artifacts":[]}},"created":[]}`
			Expect(os.WriteFile(path, []byte(body), 0o600)).To(Succeed())
			_, err := ledger.Load(path)
			Expect(err).To(MatchError(ContainSubstring(path+": module kit: from")), from)
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

	DescribeTable("refuses a recorded source lola never writes, before anything reads it",
		func(fields, want string) {
			Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
			body := `{"version":1,"modules":{"kit":{` + fields + `,"digest":"` + dg + `","artifacts":[]}},"created":[]}`
			Expect(os.WriteFile(path, []byte(body), 0o600)).To(Succeed())
			_, err := ledger.Load(path)
			Expect(err).To(MatchError(And(ContainSubstring(path+": module kit: "), ContainSubstring(want))))
			Expect(errs.Hints(err)).To(ContainElement(And(ContainSubstring("restore lock.json"), ContainSubstring("reinstall"))))
		},
		Entry("a ref read as an option", `"source":"https://example.com/kit.git","ref":"--upload-pack=touch PWNED"`, "starts with '-'"),
		Entry("a remote helper", `"source":"ext::sh -c touch PWNED"`, "git remote helper"),
		Entry("a source read as an option", `"source":"--upload-pack=touch PWNED"`, "starts with '-'"),
		Entry("git://", `"source":"git://example.com/kit.git"`, "git:// source is refused"),
		Entry("a path leaving the source", `"source":"https://example.com/kit.git","path":"../x"`, "must be a relative path inside the source"),
		Entry("a short commit", `"source":"https://example.com/kit.git","commit":"abc"`, "is not a full commit SHA"),
		Entry("an empty source", `"source":""`, "source is empty"),
		Entry("a control character", `"source":"https://example.com/kit.git","ref":"v1\u001b[2J"`, "ref holds control or invisible characters"),
	)

	It("records the targets a module is installed on, sorted, and falls back to its artifacts' without them", func() {
		l := ledger.New()
		l.Modules["a"] = &ledger.Entry{Source: "/s/a", Digest: dg, Targets: []string{"gemini-cli", "claude-code"},
			Artifacts: []ledger.Artifact{{Target: "claude-code", Kind: "command", Path: ".claude/commands/a.md"}}}
		l.Modules["b"] = &ledger.Entry{Source: "/s/b", Digest: dg, Artifacts: []ledger.Artifact{
			{Target: "opencode", Kind: "command", Path: ".opencode/commands/b.md"},
			{Target: "claude-code", Kind: "command", Path: ".claude/commands/b.md"},
			{Target: "claude-code", Kind: "skill", Path: ".claude/skills/b/SKILL.md"},
		}}
		Expect(l.Save(path)).To(Succeed())
		Expect(os.ReadFile(path)).To(ContainSubstring("\"targets\": [\n        \"claude-code\",\n        \"gemini-cli\"\n      ]"))
		back, err := ledger.Load(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(back.Modules["a"].InstalledTargets()).To(Equal([]string{"claude-code", "gemini-cli"}), "a target with no files is installed")
		Expect(back.Modules["b"].Targets).To(BeNil(), "an entry from before the field writes none")
		Expect(back.Modules["b"].InstalledTargets()).To(Equal([]string{"claude-code", "opencode"}))

		c := back.Clone()
		c.Modules["a"].Targets[0] = "opencode"
		Expect(back.Modules["a"].Targets[0]).To(Equal("claude-code"), "Clone copies the targets")
	})

	DescribeTable("refuses recorded targets lola never writes",
		func(targets, want string) {
			Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
			body := `{"version":1,"modules":{"kit":{"source":"s","digest":"` + dg + `","targets":` + targets + `,"artifacts":[]}},"created":[]}`
			Expect(os.WriteFile(path, []byte(body), 0o600)).To(Succeed())
			_, err := ledger.Load(path)
			Expect(err).To(MatchError(And(ContainSubstring(path+": module kit: "), ContainSubstring(want))))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("restore lock.json")))
		},
		Entry("a duplicate", `["claude-code","claude-code"]`, `target "claude-code" is listed twice`),
		Entry("an empty id", `[""]`, "target name"),
		Entry("a path", `["../x"]`, "target name"),
		Entry("a control character", `["claude-code\u001b[2J"]`, "target name"),
	)

	It("refuses an artifact on a target the entry does not list", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		body := `{"version":1,"modules":{"kit":{"source":"s","digest":"` + dg + `","targets":["claude-code"],` +
			`"artifacts":[{"target":"opencode","kind":"command","path":".opencode/commands/x.md"}]}},"created":[]}`
		Expect(os.WriteFile(path, []byte(body), 0o600)).To(Succeed())
		_, err := ledger.Load(path)
		Expect(err).To(MatchError(ContainSubstring(`module kit: artifact ".opencode/commands/x.md" is on target "opencode", which targets does not list`)))
		Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("restore lock.json")))
	})

	It("refuses a digest lola never writes", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		body := `{"version":1,"modules":{"kit":{"source":"s","digest":"sha256:abc","artifacts":[]}},"created":[]}`
		Expect(os.WriteFile(path, []byte(body), 0o600)).To(Succeed())
		_, err := ledger.Load(path)
		Expect(err).To(MatchError(ContainSubstring("module kit: digest")))
	})
})
