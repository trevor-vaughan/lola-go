package plan_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

const tgt = "claude-code"

func sum(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

var _ = Describe("Compute", func() {
	var root string
	var led *ledger.Ledger

	put := func(rel, content string) {
		Expect(os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644)).To(Succeed())
	}
	cmd := func(path, data string) plan.Intent {
		return plan.Intent{Target: tgt, Kind: "command", Op: plan.OpWriteFile, Path: path, Data: []byte(data), Mode: 0o644}
	}
	mcp := func(name, value string) plan.Intent {
		return plan.Intent{Target: tgt, Kind: "mcp", Op: plan.OpSetJSON, Path: ".mcp.json", Pointer: "/mcpServers/" + name, Value: []byte(value)}
	}
	block := func(lines ...string) plan.Intent {
		return plan.Intent{Target: tgt, Kind: plan.KindPointerBlock, Op: plan.OpPointerBlock, Path: "CLAUDE.md", Lines: lines}
	}
	req := func(force bool, changes ...plan.ModuleChange) plan.Request {
		return plan.Request{Roots: map[string]string{tgt: root}, BaseDirs: map[string][]string{tgt: {".claude/commands"}},
			Ledger: led, Force: force, Changes: changes}
	}
	change := func(name string, intents ...plan.Intent) plan.ModuleChange {
		return plan.ModuleChange{Name: name, Entry: &ledger.Entry{Source: "/s/" + name, Digest: "sha256:new"}, Targets: []string{tgt}, Intents: intents}
	}
	removal := func(name string) plan.ModuleChange { return plan.ModuleChange{Name: name, Targets: []string{tgt}} }
	install := func(name string, force bool, intents ...plan.Intent) (*plan.Plan, error) {
		return plan.Compute(req(force, change(name, intents...)))
	}
	own := func(name string, arts ...ledger.Artifact) {
		led.Modules[name] = &ledger.Entry{Source: "/s/" + name, Digest: "sha256:old", Artifacts: arts}
	}
	fileArt := func(path, content string) ledger.Artifact {
		return ledger.Artifact{Target: tgt, Kind: "command", Path: path, SHA256: sum(content)}
	}
	conflicts := func(err error) []plan.Conflict {
		var ce *plan.ConflictError
		ExpectWithOffset(1, errors.As(err, &ce)).To(BeTrue(), "expected a ConflictError, got %v", err)
		return ce.Conflicts
	}
	// apply writes a plan's file changes directly, standing in for plan.Apply (Task 10).
	apply := func(p *plan.Plan) {
		for _, f := range p.Files {
			if f.Delete {
				Expect(os.Remove(f.Abs)).To(Succeed())
				continue
			}
			Expect(os.MkdirAll(filepath.Dir(f.Abs), 0o755)).To(Succeed())
			Expect(os.WriteFile(f.Abs, f.Data, f.Mode)).To(Succeed())
		}
		led = p.Ledger
	}

	BeforeEach(func() {
		var err error
		root, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		led = ledger.New()
	})

	Describe("writes", func() {
		It("creates a new file and records it without mutating the input ledger", func() {
			p, err := install("a", false, cmd(".claude/commands/x.md", "X"))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps).To(Equal([]plan.Step{{Action: plan.ActionCreate, Module: "a", Target: tgt, Kind: "command", Path: ".claude/commands/x.md"}}))
			Expect(p.Files).To(Equal([]plan.FileChange{{Target: tgt, Logical: ".claude/commands/x.md", Abs: filepath.Join(root, ".claude/commands/x.md"),
				Root: root, Data: []byte("X"), Mode: 0o644, StopDirs: []string{root, filepath.Join(root, ".claude/commands")}}}))
			Expect(p.Ledger.Modules["a"].Artifacts).To(Equal([]ledger.Artifact{fileArt(".claude/commands/x.md", "X")}))
			Expect(p.Ledger.Modules["a"].Digest).To(Equal("sha256:new"))
			Expect(led.Modules).To(BeEmpty())
		})

		It("adopts identical unowned content without rewriting it", func() {
			put(".claude/commands/x.md", "X")
			p, err := install("a", false, cmd(".claude/commands/x.md", "X"))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps[0].Action).To(Equal(plan.ActionAdopt))
			Expect(p.Files).To(BeEmpty())
			Expect(p.Ledger.Modules["a"].Artifacts).To(HaveLen(1))
		})

		It("refuses different unowned content unless forced", func() {
			put(".claude/commands/x.md", "mine")
			_, err := install("a", false, cmd(".claude/commands/x.md", "X"))
			Expect(conflicts(err)).To(Equal([]plan.Conflict{{Target: tgt, Path: ".claude/commands/x.md", Reason: "exists and was not written by lola", Forceable: true}}))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("--force")))
			p, err := install("a", true, cmd(".claude/commands/x.md", "X"))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps[0].Action).To(Equal(plan.ActionUpdate))
			Expect(p.Steps[0].Forced).To(Equal("unowned"))
			Expect(p.Files[0].Data).To(Equal([]byte("X")))
		})

		It("updates an owned, unmodified file and skips an unchanged one", func() {
			put(".claude/commands/x.md", "old")
			put(".claude/commands/y.md", "same")
			own("a", fileArt(".claude/commands/x.md", "old"), fileArt(".claude/commands/y.md", "same"))
			p, err := install("a", false, cmd(".claude/commands/x.md", "new"), cmd(".claude/commands/y.md", "same"))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps).To(HaveLen(1))
			Expect(p.Steps[0].Action).To(Equal(plan.ActionUpdate))
			Expect(p.Files).To(HaveLen(1))
		})

		It("refuses to overwrite a hand-edited owned file unless forced", func() {
			put(".claude/commands/x.md", "edited by hand")
			own("a", fileArt(".claude/commands/x.md", "old"))
			_, err := install("a", false, cmd(".claude/commands/x.md", "new"))
			Expect(conflicts(err)[0].Reason).To(Equal("hand-edited since lola wrote it"))
			p, err := install("a", true, cmd(".claude/commands/x.md", "new"))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps[0].Forced).To(Equal("hand-edited"))
		})

		It("rewrites an owned file that went missing, with a note", func() {
			own("a", fileArt(".claude/commands/x.md", "X"))
			p, err := install("a", false, cmd(".claude/commands/x.md", "X"))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps[0]).To(Equal(plan.Step{Action: plan.ActionCreate, Module: "a", Target: tgt, Kind: "command", Path: ".claude/commands/x.md", Note: "was missing; rewritten"}))
		})

		It("expands a CopyTree with excludes and normalized modes", func() {
			src := filepath.Join(root, "src")
			Expect(os.MkdirAll(filepath.Join(src, "ref"), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("S"), 0o644)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(src, "ref", "run.sh"), []byte("R"), 0o700)).To(Succeed())
			p, err := install("a", false, plan.Intent{Target: tgt, Kind: "skill", Op: plan.OpCopyTree, Src: src, Path: ".claude/skills/s", Exclude: []string{"SKILL.md"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Files).To(HaveLen(1))
			Expect(p.Files[0].Logical).To(Equal(".claude/skills/s/ref/run.sh"))
			Expect(p.Files[0].Mode).To(Equal(os.FileMode(0o755)))
		})

		It("applies a mode-only change", func() {
			put(".claude/commands/x.md", "X")
			own("a", fileArt(".claude/commands/x.md", "X"))
			p, err := install("a", false, plan.Intent{Target: tgt, Kind: "command", Op: plan.OpWriteFile, Path: ".claude/commands/x.md", Data: []byte("X"), Mode: 0o755})
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps).To(HaveLen(1))
			Expect(p.Steps[0].Action).To(Equal(plan.ActionUpdate))
			Expect(p.Steps[0].Note).To(Equal("mode changed"))
			Expect(p.Files).To(HaveLen(1))
			Expect(p.Files[0].Mode).To(Equal(os.FileMode(0o755)))
		})

		It("does not rewrite an identical file whose mode is merely group-writable", func() {
			put(".claude/commands/x.md", "X")
			Expect(os.Chmod(filepath.Join(root, ".claude/commands/x.md"), 0o664)).To(Succeed())
			own("a", fileArt(".claude/commands/x.md", "X"))
			p, err := install("a", false, cmd(".claude/commands/x.md", "X"))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps).To(BeEmpty())
			Expect(p.Files).To(BeEmpty())
		})

		It("normalizes intent modes so plans converge", func() {
			put(".claude/commands/x.md", "X")
			Expect(os.Chmod(filepath.Join(root, ".claude/commands/x.md"), 0o755)).To(Succeed())
			own("a", fileArt(".claude/commands/x.md", "X"))
			p, err := install("a", false, plan.Intent{Target: tgt, Kind: "command", Op: plan.OpWriteFile, Path: ".claude/commands/x.md", Data: []byte("X"), Mode: 0o700})
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps).To(BeEmpty())
			Expect(p.Files).To(BeEmpty())
		})
	})

	Describe("ownership (Review Focus 2)", func() {
		It("refuses a destination another installed module owns, even with --force", func() {
			put(".claude/commands/x.md", "B")
			own("b", fileArt(".claude/commands/x.md", "B"))
			_, err := install("a", true, cmd(".claude/commands/x.md", "A"))
			cs := conflicts(err)
			Expect(cs[0].Reason).To(Equal(`owned by module "b"; uninstall it first`))
			Expect(cs[0].Forceable).To(BeFalse())
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("cannot override")))
		})

		It("refuses two modules claiming one destination in the same operation", func() {
			_, err := plan.Compute(req(true, change("a", cmd(".claude/commands/x.md", "A")), change("b", cmd(".claude/commands/x.md", "B"))))
			Expect(conflicts(err)[0].Reason).To(ContainSubstring(`also provided by module "a"`))
		})

		It("moves a destination between replanned modules without a conflict or a delete", func() {
			put(".claude/commands/x.md", "A")
			own("a", fileArt(".claude/commands/x.md", "A"))
			own("b")
			p, err := plan.Compute(req(false, change("a"), change("b", cmd(".claude/commands/x.md", "B"))))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Files).To(HaveLen(1))
			Expect(p.Files[0].Delete).To(BeFalse())
			Expect(p.Files[0].Data).To(Equal([]byte("B")))
			Expect(p.Ledger.Modules["a"].Artifacts).To(BeEmpty())
			Expect(p.Ledger.Modules["b"].Artifacts).To(HaveLen(1))
		})

		It("refuses symlink-aliased destinations", func() {
			Expect(os.MkdirAll(filepath.Join(root, ".claude/commands"), 0o755)).To(Succeed())
			Expect(os.Symlink(filepath.Join(root, ".claude/commands"), filepath.Join(root, ".claude/alias"))).To(Succeed())
			_, err := install("a", false, cmd(".claude/commands/x.md", "A"), cmd(".claude/alias/x.md", "B"))
			cs := conflicts(err)
			Expect(cs).To(HaveLen(1))
			Expect(cs[0].Reason).To(ContainSubstring("resolves to the same file"))
			Expect(cs[0].Forceable).To(BeFalse())
			_, err = plan.Compute(req(true, change("a", cmd(".claude/commands/x.md", "A"), cmd(".claude/alias/x.md", "B"))))
			cs = conflicts(err)
			Expect(cs).To(HaveLen(1))
			Expect(cs[0].Forceable).To(BeFalse())
		})
	})

	Describe("orphans", func() {
		It("deletes an unmodified orphan and drops the module from the ledger", func() {
			put(".claude/commands/x.md", "X")
			own("a", fileArt(".claude/commands/x.md", "X"))
			p, err := plan.Compute(req(false, removal("a")))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps[0].Action).To(Equal(plan.ActionDelete))
			Expect(p.Files[0].Delete).To(BeTrue())
			Expect(p.Ledger.Modules).NotTo(HaveKey("a"))
		})

		It("keeps a hand-edited orphan unless forced", func() {
			put(".claude/commands/x.md", "edited")
			own("a", fileArt(".claude/commands/x.md", "X"))
			_, err := plan.Compute(req(false, removal("a")))
			Expect(conflicts(err)[0].Reason).To(ContainSubstring("kept"))
			p, err := plan.Compute(req(true, removal("a")))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Files[0].Delete).To(BeTrue())
			Expect(p.Steps[0].Forced).To(Equal("hand-edited"))
		})

		It("forgets an orphan that is already gone", func() {
			own("a", fileArt(".claude/commands/x.md", "X"))
			p, err := plan.Compute(req(false, removal("a")))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps[0].Action).To(Equal(plan.ActionForget))
			Expect(p.Files).To(BeEmpty())
		})

		It("keeps the module's artifacts on targets that are not being replanned", func() {
			own("a", fileArt(".claude/commands/x.md", "X"), ledger.Artifact{Target: "opencode", Kind: "command", Path: ".opencode/commands/x.md", SHA256: sum("X")})
			put(".claude/commands/x.md", "X")
			p, err := plan.Compute(req(false, removal("a")))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Ledger.Modules["a"].Artifacts).To(Equal([]ledger.Artifact{{Target: "opencode", Kind: "command", Path: ".opencode/commands/x.md", SHA256: sum("X")}}))
		})
	})

	Describe("JSON members", func() {
		It("merges into a commented .mcp.json and uninstall never removes a server lola did not write", func() {
			user := "{\n  // mine\n  \"mcpServers\": {\n    \"github\": {\"command\": \"gh-mcp\"}\n  }\n}\n"
			put(".mcp.json", user)
			p, err := install("a", false, mcp("lola-srv", `{"command":"x"}`))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(p.Files[0].Data)).To(ContainSubstring(`"github": {"command": "gh-mcp"}`))
			Expect(string(p.Files[0].Data)).To(ContainSubstring(`"lola-srv"`))
			Expect(p.Ledger.Created).To(BeEmpty())
			apply(p)

			p, err = plan.Compute(req(false, removal("a")))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(p.Files[0].Data)).To(Equal(user))
			Expect(p.Files[0].Delete).To(BeFalse())
		})

		It("refuses to replace a same-named server the user wrote", func() {
			put(".mcp.json", `{"mcpServers":{"github":{"command":"mine"}}}`)
			_, err := install("a", false, mcp("github", `{"command":"theirs"}`))
			Expect(conflicts(err)[0]).To(Equal(plan.Conflict{Target: tgt, Path: ".mcp.json", Key: "/mcpServers/github", Reason: "exists and was not written by lola", Forceable: true}))
		})

		It("corrupt JSON config is never overwritten, even with --force", func() {
			put(".mcp.json", "{\n  \"mcpServers\": {,\n}")
			_, err := install("a", true, mcp("x", `{"command":"x"}`))
			Expect(err).To(MatchError(ContainSubstring(".mcp.json")))
			Expect(err).To(MatchError(ContainSubstring("line 2")))
			var ce *plan.ConflictError
			Expect(errors.As(err, &ce)).To(BeFalse())
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("fix it by hand")))
		})

		It("deletes a file it created once its last member is removed", func() {
			p, err := install("a", false, mcp("x", `{"command":"x"}`))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Ledger.Created).To(Equal([]ledger.FileRef{{Target: tgt, Path: ".mcp.json"}}))
			apply(p)
			p, err = plan.Compute(req(false, removal("a")))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Files[0].Delete).To(BeTrue())
			Expect(p.Ledger.Created).To(BeEmpty())
		})
	})

	Describe("shared pointer blocks", func() {
		shared := func(intents ...plan.Intent) plan.Request {
			r := req(false)
			r.SharedTargets, r.Shared = []string{tgt}, intents
			return r
		}

		It("creates CLAUDE.md, then deletes it when the last line goes", func() {
			p, err := plan.Compute(shared(block("@.claude/lola/a.md")))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(p.Files[0].Data)).To(Equal("<!-- lola:instructions:start -->\n@.claude/lola/a.md\n<!-- lola:instructions:end -->\n"))
			Expect(p.Ledger.Shared[0].Kind).To(Equal(plan.KindPointerBlock))
			apply(p)
			p, err = plan.Compute(shared())
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Files[0].Delete).To(BeTrue())
			Expect(p.Ledger.Shared).To(BeEmpty())
		})

		It("keeps the user's CLAUDE.md text byte-identical around the block", func() {
			put("CLAUDE.md", "# Rules\n")
			p, err := plan.Compute(shared(block("@.claude/lola/a.md")))
			Expect(err).NotTo(HaveOccurred())
			apply(p)
			p, err = plan.Compute(shared())
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Files[0].Delete).To(BeFalse())
			Expect(string(p.Files[0].Data)).To(Equal("# Rules\n"))
		})
	})

	It("writes through a symlinked directory, shows where, and keeps the logical path (Review Focus 3)", func() {
		outside, err := filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		Expect(os.MkdirAll(filepath.Join(root, ".claude"), 0o755)).To(Succeed())
		Expect(os.Symlink(outside, filepath.Join(root, ".claude", "commands"))).To(Succeed())
		p, err := install("a", false, cmd(".claude/commands/x.md", "X"))
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Files[0].Abs).To(Equal(filepath.Join(outside, "x.md")))
		Expect(p.Files[0].Outside).To(BeTrue())
		Expect(p.Steps[0].Via).To(Equal(filepath.Join(outside, "x.md")))
		Expect(p.Ledger.Modules["a"].Artifacts[0].Path).To(Equal(".claude/commands/x.md"))
		Expect(p.Ledger.Modules["a"].Artifacts[0].Resolved).To(Equal(filepath.Join(outside, "x.md")))
	})

	It("Observe reports current hashes and absence", func() {
		put(".claude/commands/x.md", "X")
		Expect(plan.Observe(root, fileArt(".claude/commands/x.md", "X"))).To(Equal(sum("X")))
		Expect(plan.Observe(root, fileArt(".claude/commands/none.md", "X"))).To(Equal(""))
		put(".mcp.json", `{"mcpServers":{"a":{"command":"x"}}}`)
		Expect(plan.Observe(root, ledger.Artifact{Target: tgt, Kind: "mcp", Path: ".mcp.json", Key: "/mcpServers/a"})).To(Equal(sum(`{"command":"x"}`)))
	})
})
