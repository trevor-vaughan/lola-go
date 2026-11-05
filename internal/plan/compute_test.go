package plan_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
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
	req := func(force bool, changes ...plan.ModuleChange) plan.Request {
		return plan.Request{Roots: map[string]string{tgt: root}, BaseDirs: map[string][]string{tgt: {".claude/commands"}},
			Ledger: led, Force: force, ForceRun: "lola render --force", Changes: changes}
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

		It("writes a CopyTree's overlay as files of the tree, mode 0644, beside its walked files", func() {
			src := filepath.Join(root, "src")
			Expect(os.MkdirAll(filepath.Join(src, "ref"), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(src, "ref", "a.md.liquid"), []byte("{{ target }}"), 0o644)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(src, "ref", "run.sh"), []byte("R"), 0o700)).To(Succeed())
			p, err := install("a", false, plan.Intent{Target: tgt, Kind: "skill", Op: plan.OpCopyTree, Src: src, Path: ".claude/skills/s",
				Exclude: []string{"ref/a.md.liquid"}, Overlay: []plan.File{{Path: "ref/a.md", Data: []byte("claude-code")}}})
			Expect(err).NotTo(HaveOccurred())
			var got []string
			for _, f := range p.Files {
				got = append(got, fmt.Sprintf("%s %o", f.Logical, f.Mode))
			}
			Expect(got).To(ConsistOf(".claude/skills/s/ref/run.sh 755", ".claude/skills/s/ref/a.md 644"))

			for _, bad := range []string{"../x.md", "/etc/x.md", "ref/../../x.md", ""} {
				_, err := install("a", false, plan.Intent{Target: tgt, Kind: "skill", Op: plan.OpCopyTree, Src: src, Path: ".claude/skills/s",
					Overlay: []plan.File{{Path: bad, Data: []byte("x")}}})
				Expect(err).To(MatchError(fmt.Sprintf(".claude/skills/s: overlay file %q is not a path inside the tree", bad)), bad)
			}
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
			Expect(cs[0].Reason).To(Equal(`wanted by module "a", owned by module "b"; --force cannot override it`))
			Expect(cs[0]).To(And(HaveField("Forceable", false), HaveField("Owner", "b")))
			Expect(errs.Hints(err)).To(BeEmpty(), "the engine hints the owner's uninstall; the plan knows no scope")
		})

		It("refuses two modules claiming one destination in the same operation", func() {
			_, err := plan.Compute(req(true, change("a", cmd(".claude/commands/x.md", "A")), change("b", cmd(".claude/commands/x.md", "B"))))
			Expect(conflicts(err)[0].Reason).To(ContainSubstring(`also provided by module "a"`))
		})

		It("refuses one module claiming one destination twice, even with --force", func() {
			_, err := plan.Compute(req(true, change("a", cmd(".claude/commands/x.md", "A"), cmd(".claude/commands/x.md", "B"))))
			cs := conflicts(err)
			Expect(cs).To(HaveLen(1))
			Expect(cs[0].Reason).To(Equal(`provided twice by module "a"; its author must rename one of the two`))
			Expect(cs[0].Forceable).To(BeFalse())
			Expect(errs.Hints(err)).To(BeEmpty(), "only the module's author can rename one: no command, so no hint")
			_, err = plan.Compute(req(true, change("a", mcp("gh", `{"command":"a"}`), mcp("gh", `{"command":"b"}`))))
			Expect(conflicts(err)[0].Reason).To(Equal(`provided twice by module "a"; its author must rename one of the two`))
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
			Expect(conflicts(err)[0].Reason).To(Equal("hand-edited since lola wrote it; deleting it would lose your edits"))
			Expect(err).To(MatchError(HavePrefix("refusing to change anything: 1 conflict, which --force overrides\n")))
			Expect(errs.Hints(err)).To(Equal([]string{"lola render --force"}), "the hint is the run with --force")
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

	Describe("installed targets", func() {
		It("records the change's targets with those it does not replan, files or not", func() {
			oc := ledger.Artifact{Target: "opencode", Kind: "command", Path: ".opencode/commands/x.md", SHA256: sum("X")}
			led.Modules["a"] = &ledger.Entry{Source: "/s/a", Digest: "sha256:old", Targets: []string{"gemini-cli", "opencode"}, Artifacts: []ledger.Artifact{oc}}
			c := change("a", cmd(".claude/commands/x.md", "X"))
			c.Entry.Targets = []string{tgt}
			p, err := plan.Compute(req(false, c))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Ledger.Modules["a"].Targets).To(Equal([]string{tgt, "gemini-cli", "opencode"}))
		})

		It("records every target the change writes an artifact on", func() {
			led.Modules["a"] = &ledger.Entry{Source: "/s/a", Digest: "sha256:old", Targets: []string{"opencode"}}
			p, err := install("a", false, cmd(".claude/commands/x.md", "X"))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Ledger.Modules["a"].Targets).To(Equal([]string{tgt, "opencode"}))
		})

		It("keeps a module installed on a target that holds none of its files when another target leaves", func() {
			put(".claude/commands/x.md", "X")
			led.Modules["a"] = &ledger.Entry{Source: "/s/a", Digest: "sha256:old", Targets: []string{tgt, "gemini-cli"},
				Artifacts: []ledger.Artifact{fileArt(".claude/commands/x.md", "X")}}
			p, err := plan.Compute(req(false, removal("a")))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Ledger.Modules["a"].Targets).To(Equal([]string{"gemini-cli"}))
			Expect(p.Ledger.Modules["a"].Artifacts).To(BeEmpty())

			p, err = plan.Compute(req(false, plan.ModuleChange{Name: "a", Targets: []string{tgt, "gemini-cli"}}))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Ledger.Modules).NotTo(HaveKey("a"), "removed from every target, the module leaves lock.json")
		})

		It("infers the targets of an entry that records none from its artifacts", func() {
			put(".claude/commands/x.md", "X")
			own("a", fileArt(".claude/commands/x.md", "X"), ledger.Artifact{Target: "opencode", Kind: "command", Path: ".opencode/commands/x.md", SHA256: sum("X")})
			p, err := plan.Compute(req(false, removal("a")))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Ledger.Modules["a"].Targets).To(Equal([]string{"opencode"}))
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

	Describe("owned array members", func() {
		const dirs = "/context/includeDirectories"
		member := func(value string) plan.Intent {
			return plan.Intent{Target: tgt, Kind: "rule", Op: plan.OpAddJSONMember, Path: "settings.json", Pointer: dirs, Member: plan.StringMember(value)}
		}
		read := func() string {
			b, err := os.ReadFile(filepath.Join(root, "settings.json"))
			Expect(err).NotTo(HaveOccurred())
			return string(b)
		}

		It("creates the array and its file, shares it between modules, and removes both after the last member", func() {
			p, err := install("a", false, member("lola/a"))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps).To(Equal([]plan.Step{{Action: plan.ActionCreate, Module: "a", Target: tgt, Kind: "rule", Path: "settings.json", Key: dirs, Value: "lola/a"}}))
			Expect(p.Ledger.Modules["a"].Artifacts).To(Equal([]ledger.Artifact{{Target: tgt, Kind: "rule", Path: "settings.json", Key: dirs, Value: "lola/a", SHA256: sum(`"lola/a"`)}}))
			Expect(p.Ledger.Created).To(Equal([]ledger.FileRef{{Target: tgt, Path: "settings.json"}, {Target: tgt, Path: "settings.json", Key: dirs}}))
			apply(p)
			Expect(read()).To(Equal("{\n  \"context\": {\n    \"includeDirectories\": [\n      \"lola/a\"\n    ]\n  }\n}\n"))

			p, err = install("b", false, member("lola/b"))
			Expect(err).NotTo(HaveOccurred())
			apply(p)
			Expect(read()).To(ContainSubstring("\"lola/a\",\n      \"lola/b\"\n"))

			p, err = plan.Compute(req(false, removal("a")))
			Expect(err).NotTo(HaveOccurred())
			apply(p)
			Expect(read()).To(Equal("{\n  \"context\": {\n    \"includeDirectories\": [\n      \"lola/b\"\n    ]\n  }\n}\n"))
			Expect(p.Ledger.Created).To(HaveLen(2))

			p, err = plan.Compute(req(false, removal("b")))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Files).To(HaveLen(1))
			Expect(p.Files[0].Delete).To(BeTrue())
			Expect(p.Ledger.Created).To(BeEmpty())
		})

		It("adds to the user's commented array and restores it byte for byte on removal", func() {
			user := "{\n  // mine\n  \"context\": {\n    \"includeDirectories\": [\n      \"docs\" // shared\n    ]\n  }\n}\n"
			put("settings.json", user)
			p, err := install("a", false, member("lola/a"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(p.Files[0].Data)).To(ContainSubstring("\"docs\", // shared\n      \"lola/a\"\n"))
			Expect(p.Ledger.Created).To(BeEmpty())
			apply(p)

			p, err = plan.Compute(req(false, removal("a")))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(p.Files[0].Data)).To(Equal(user))
		})

		It("keeps an array it did not create when its last member leaves", func() {
			put("settings.json", "{\n  \"context\": {\n    \"includeDirectories\": []\n  }\n}\n")
			p, err := install("a", false, member("lola/a"))
			Expect(err).NotTo(HaveOccurred())
			apply(p)
			p, err = plan.Compute(req(false, removal("a")))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Files[0].Delete).To(BeFalse())
			v, found, err := fsx.JSONGet(p.Files[0].Data, dirs)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(string(v)).To(Equal("[]"))
		})

		It("adopts a member the user already listed, and rewrites one the user removed, with a note", func() {
			put("settings.json", `{"context": {"includeDirectories": ["lola/a"]}}`)
			p, err := install("a", false, member("lola/a"))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps[0].Action).To(Equal(plan.ActionAdopt))
			Expect(p.Files).To(BeEmpty())
			apply(p)

			put("settings.json", `{"context": {"includeDirectories": []}}`)
			p, err = install("a", false, member("lola/a"))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps[0].Action).To(Equal(plan.ActionCreate))
			Expect(p.Steps[0].Note).To(Equal("was missing; rewritten"))
		})

		It("forgets a member the user already removed", func() {
			own("a", ledger.Artifact{Target: tgt, Kind: "rule", Path: "settings.json", Key: dirs, Value: "lola/a", SHA256: sum(`"lola/a"`)})
			put("settings.json", `{"context": {"includeDirectories": ["docs"]}}`)
			p, err := plan.Compute(req(false, removal("a")))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps[0].Action).To(Equal(plan.ActionForget))
			Expect(p.Files).To(BeEmpty())
		})

		It("refuses a destination that is not an array, even with --force", func() {
			put("settings.json", `{"context": {"includeDirectories": "docs"}}`)
			_, err := install("a", true, member("lola/a"))
			Expect(err).To(MatchError(ContainSubstring(`"/context/includeDirectories" is not an array`)))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("fix it by hand")))
		})

		It("Observe reports a member's hash only while the member is listed", func() {
			a := ledger.Artifact{Target: tgt, Kind: "rule", Path: "settings.json", Key: dirs, Value: "lola/a"}
			put("settings.json", `{"context": {"includeDirectories": ["lola/a"]}}`)
			Expect(plan.Observe(root, a)).To(Equal(sum(`"lola/a"`)))
			put("settings.json", `{"context": {"includeDirectories": ["docs"]}}`)
			Expect(plan.Observe(root, a)).To(Equal(""))
		})
	})

	Describe("JSON-value members", func() {
		const settings, stop = ".claude/settings.local.json", "/hooks/Stop"
		groupX := `{"matcher":"","hooks":[{"type":"command","command":"x"}]}`
		canonX := `{"hooks":[{"command":"x","type":"command"}],"matcher":""}`
		group := func(value string) plan.Intent {
			return plan.Intent{Target: tgt, Kind: "runtime-hook", Op: plan.OpAddJSONMember, Path: settings, Pointer: stop, Member: []byte(value)}
		}
		read := func() string {
			b, err := os.ReadFile(filepath.Join(root, settings))
			Expect(err).NotTo(HaveOccurred())
			return string(b)
		}

		It("records the member as canonical JSON, and removes it with the arrays and file it created", func() {
			user := "{\n  \"model\": \"opus\"\n}\n"
			put(settings, user)
			p, err := install("a", false, group(groupX))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps).To(Equal([]plan.Step{{Action: plan.ActionCreate, Module: "a", Target: tgt, Kind: "runtime-hook", Path: settings,
				Key: stop, Member: []byte(canonX)}}))
			Expect(p.Ledger.Modules["a"].Artifacts).To(Equal([]ledger.Artifact{{Target: tgt, Kind: "runtime-hook", Path: settings, Key: stop,
				Member: []byte(canonX), SHA256: sum(canonX)}}))
			Expect(p.Ledger.Created).To(Equal([]ledger.FileRef{{Target: tgt, Path: settings, Key: stop}}))
			apply(p)
			Expect(read()).To(ContainSubstring("\"hooks\": {\n    \"Stop\": [\n      {\n        \"hooks\": [\n"))
			Expect(plan.Observe(root, p.Ledger.Modules["a"].Artifacts[0])).To(Equal(sum(canonX)))

			p, err = install("a", false, group(canonX))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps).To(BeEmpty(), "the same group in another key order is unchanged")

			p, err = plan.Compute(req(false, removal("a")))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps[0].Action).To(Equal(plan.ActionDelete))
			apply(p)
			Expect(read()).To(Equal(user))
			Expect(p.Ledger.Created).To(BeEmpty())
		})

		It("deletes the settings file it created once the last group leaves", func() {
			p, err := install("a", false, group(groupX))
			Expect(err).NotTo(HaveOccurred())
			apply(p)
			p, err = plan.Compute(req(false, removal("a")))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Files).To(HaveLen(1))
			Expect(p.Files[0].Delete).To(BeTrue())
		})

		It("keeps the user's groups in an event array it did not create", func() {
			user := "{\n  \"hooks\": {\n    \"Stop\": [\n      {\"hooks\": [{\"type\": \"command\", \"command\": \"mine\"}]} // mine\n    ]\n  }\n}\n"
			put(settings, user)
			p, err := install("a", false, group(groupX))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Ledger.Created).To(BeEmpty())
			apply(p)
			p, err = plan.Compute(req(false, removal("a")))
			Expect(err).NotTo(HaveOccurred())
			apply(p)
			Expect(read()).To(Equal(user))
		})

		It("refuses to adopt an identical group lola does not own, unless forced", func() {
			put(settings, `{"hooks": {"Stop": [`+groupX+`]}}`)
			_, err := install("a", false, group(groupX))
			Expect(conflicts(err)).To(Equal([]plan.Conflict{{Target: tgt, Path: settings, Key: stop, Value: canonX,
				Reason: "exists and was not written by lola; owning it would remove it on uninstall", Forceable: true}}))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("--force")))

			p, err := install("a", true, group(groupX))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps[0].Action).To(Equal(plan.ActionAdopt))
			Expect(p.Steps[0].Forced).To(Equal("unowned"))
			Expect(p.Ledger.Modules["a"].Artifacts).To(HaveLen(1))
		})

		It("refuses the same group from two modules, even with --force", func() {
			_, err := plan.Compute(req(true, change("a", group(groupX)), change("b", group(canonX))))
			Expect(conflicts(err)).To(ConsistOf(HaveField("Reason",
				`also provided by module "a" in this operation; only one of the two can install it, and --force cannot override that`)))
			own("a", ledger.Artifact{Target: tgt, Kind: "runtime-hook", Path: settings, Key: stop, Member: []byte(canonX), SHA256: sum(canonX)})
			put(settings, `{"hooks": {"Stop": [`+groupX+`]}}`)
			_, err = install("b", true, group(groupX))
			Expect(conflicts(err)).To(ConsistOf(HaveField("Reason", `wanted by module "b", owned by module "a"; --force cannot override it`)))
		})

		It("keeps an owned group the user removed absent, recorded as owned, unless forced", func() {
			own("a", ledger.Artifact{Target: tgt, Kind: "runtime-hook", Path: settings, Key: stop, Member: []byte(canonX), SHA256: sum(canonX)})
			edited := `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "x --safe"}]}]}}`
			put(settings, edited)
			p, err := install("a", false, group(groupX))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps).To(Equal([]plan.Step{{Action: plan.ActionAbsent, Module: "a", Target: tgt, Kind: "runtime-hook", Path: settings,
				Key: stop, Member: []byte(canonX), Note: "missing; --force restores it"}}))
			Expect(p.Files).To(BeEmpty())
			Expect(p.Ledger.Modules["a"].Artifacts).To(Equal([]ledger.Artifact{{Target: tgt, Kind: "runtime-hook", Path: settings, Key: stop,
				Member: []byte(canonX), SHA256: sum(canonX)}}))

			p, err = install("a", true, group(groupX))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps).To(ConsistOf(MatchFields(IgnoreExtras, Fields{"Action": Equal(plan.ActionCreate), "Forced": Equal("missing"),
				"Note": Equal("was missing; rewritten")})))
			Expect(p.Files).To(HaveLen(1))
		})

		It("forgets a group the user edited, leaving the edited one alone", func() {
			own("a", ledger.Artifact{Target: tgt, Kind: "runtime-hook", Path: settings, Key: stop, Member: []byte(canonX), SHA256: sum(canonX)})
			edited := `{"hooks": {"Stop": [{"matcher": "", "hooks": [{"type": "command", "command": "x --safe"}]}]}}`
			put(settings, edited)
			p, err := plan.Compute(req(false, removal("a")))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps).To(Equal([]plan.Step{{Action: plan.ActionForget, Module: "a", Target: tgt, Kind: "runtime-hook", Path: settings,
				Key: stop, Member: []byte(canonX), Note: "already gone"}}))
			Expect(p.Files).To(BeEmpty())
		})

		DescribeTable("refuses a settings file it cannot edit, with coaching",
			func(content, msg string) {
				put(settings, content)
				_, err := install("a", true, group(groupX))
				Expect(err).To(MatchError(ContainSubstring(msg)))
				Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("fix it by hand")))
			},
			Entry("malformed JSON", `{"hooks": `, settings+": "),
			Entry("hooks that is not an object", `{"hooks": "off"}`, `"/hooks" is not an object`),
			Entry("an event that is not an array", `{"hooks": {"Stop": {}}}`, `"/hooks/Stop" is not an array`),
		)

		It("refuses a member that is not JSON", func() {
			_, err := install("a", false, group(`{"hooks":`))
			Expect(err).To(MatchError(ContainSubstring(settings + " " + stop + ": member:")))
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

	Describe("replaceable paths", func() {
		replace := func(force bool, rs []plan.Replaceable, intents ...plan.Intent) (*plan.Plan, error) {
			r := req(force, change("a", intents...))
			r.Replace = rs
			return plan.Compute(r)
		}
		file := func(path string) plan.Replaceable { return plan.Replaceable{Target: tgt, Path: path} }
		dir := func(path string) plan.Replaceable { return plan.Replaceable{Target: tgt, Path: path, Dir: true} }

		It("overwrites an unowned file listed as replaceable instead of refusing it, and marks the step", func() {
			put(".claude/commands/x.md", "theirs")
			p, err := replace(false, []plan.Replaceable{file(".claude/commands/x.md")}, cmd(".claude/commands/x.md", "ours"))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps).To(ConsistOf(MatchFields(IgnoreExtras, Fields{"Action": Equal(plan.ActionUpdate), "Path": Equal(".claude/commands/x.md"),
				"Replaced": BeTrue(), "Forced": BeEmpty()})))
			Expect(p.Files).To(HaveLen(1))
			Expect(string(p.Files[0].Data)).To(Equal("ours"))
			Expect(p.Ledger.Modules["a"].Artifacts).To(HaveLen(1))
		})

		It("marks the step replaced when it adopts an identical unowned file listed as replaceable", func() {
			put(".claude/commands/x.md", "ours")
			put(".claude/commands/y.md", "ours")
			p, err := replace(false, []plan.Replaceable{file(".claude/commands/x.md")},
				cmd(".claude/commands/x.md", "ours"), cmd(".claude/commands/y.md", "ours"))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps).To(ConsistOf(
				MatchFields(IgnoreExtras, Fields{"Action": Equal(plan.ActionAdopt), "Path": Equal(".claude/commands/x.md"), "Replaced": BeTrue()}),
				MatchFields(IgnoreExtras, Fields{"Action": Equal(plan.ActionAdopt), "Path": Equal(".claude/commands/y.md"), "Replaced": BeFalse()}),
			))
		})

		It("overwrites every unowned file under a replaceable directory", func() {
			put(".claude/skills/r/SKILL.md", "theirs")
			put(".claude/skills/r/ref/a.md", "theirs")
			p, err := replace(false, []plan.Replaceable{dir(".claude/skills/r")},
				cmd(".claude/skills/r/SKILL.md", "ours"), cmd(".claude/skills/r/ref/a.md", "ours"))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Steps).To(HaveEach(HaveField("Replaced", BeTrue())))
		})

		It("replaces an MCP entry only at the listed pointer", func() {
			put(".mcp.json", `{"mcpServers":{"gh":{"command":"old"},"db":{"command":"old"}}}`)
			_, err := replace(false, []plan.Replaceable{{Target: tgt, Path: ".mcp.json", Key: "/mcpServers/gh"}},
				mcp("gh", `{"command":"new"}`))
			Expect(err).NotTo(HaveOccurred())
			_, err = replace(false, []plan.Replaceable{{Target: tgt, Path: ".mcp.json", Key: "/mcpServers/gh"}},
				mcp("db", `{"command":"new"}`))
			Expect(conflicts(err)).To(ConsistOf(HaveField("Key", "/mcpServers/db")))
		})

		It("still refuses unowned paths that are not listed, naming them with the --force hint", func() {
			put(".claude/commands/x.md", "theirs")
			put(".claude/commands/y.md", "theirs")
			_, err := replace(false, []plan.Replaceable{file(".claude/commands/x.md"), dir(".claude/commands/x")},
				cmd(".claude/commands/x.md", "ours"), cmd(".claude/commands/y.md", "ours"))
			Expect(conflicts(err)).To(ConsistOf(HaveField("Path", ".claude/commands/y.md")))
			Expect(errs.Hints(err)).To(ContainElement(ContainSubstring("--force")))
		})

		It("never replaces a path a lola module owns, even when it is listed", func() {
			put(".claude/commands/x.md", "edited")
			own("other", fileArt(".claude/commands/x.md", "X"))
			_, err := replace(true, []plan.Replaceable{file(".claude/commands/x.md")}, cmd(".claude/commands/x.md", "ours"))
			Expect(conflicts(err)).To(ConsistOf(HaveField("Reason", ContainSubstring(`owned by module "other"`))))

			own("a", fileArt(".claude/commands/x.md", "X"))
			delete(led.Modules, "other")
			_, err = replace(false, []plan.Replaceable{file(".claude/commands/x.md")}, cmd(".claude/commands/x.md", "ours"))
			Expect(conflicts(err)).To(ConsistOf(HaveField("Reason", "hand-edited since lola wrote it")), "its own hand-edited file stays a conflict")
		})

		It("never replaces through a symbolic link or a symlinked parent", func() {
			outside, err := filepath.EvalSymlinks(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(filepath.Join(outside, "x.md"), []byte("theirs"), 0o644)).To(Succeed())
			Expect(os.MkdirAll(filepath.Join(root, ".claude", "commands"), 0o755)).To(Succeed())
			Expect(os.Symlink(filepath.Join(outside, "x.md"), filepath.Join(root, ".claude", "commands", "x.md"))).To(Succeed())
			_, err = replace(false, []plan.Replaceable{file(".claude/commands/x.md")}, cmd(".claude/commands/x.md", "ours"))
			Expect(conflicts(err)).To(ConsistOf(HaveField("Path", ".claude/commands/x.md")))

			Expect(os.Symlink(outside, filepath.Join(root, ".claude", "skills"))).To(Succeed())
			Expect(os.WriteFile(filepath.Join(outside, "SKILL.md"), []byte("theirs"), 0o644)).To(Succeed())
			_, err = replace(false, []plan.Replaceable{dir(".claude/skills")}, cmd(".claude/skills/SKILL.md", "ours"))
			Expect(conflicts(err)).To(ConsistOf(HaveField("Path", ".claude/skills/SKILL.md")))
			Expect(os.ReadFile(filepath.Join(outside, "SKILL.md"))).To(Equal([]byte("theirs")))
		})

		It("does not match a sibling that shares the directory's name as a prefix", func() {
			put(".claude/skills/rx/SKILL.md", "theirs")
			_, err := replace(false, []plan.Replaceable{dir(".claude/skills/r")}, cmd(".claude/skills/rx/SKILL.md", "ours"))
			Expect(conflicts(err)).To(HaveLen(1))
		})
	})

	It("Observe reports current hashes and absence", func() {
		put(".claude/commands/x.md", "X")
		Expect(plan.Observe(root, fileArt(".claude/commands/x.md", "X"))).To(Equal(sum("X")))
		Expect(plan.Observe(root, fileArt(".claude/commands/none.md", "X"))).To(Equal(""))
		put(".mcp.json", `{"mcpServers":{"a":{"command":"x"}}}`)
		Expect(plan.Observe(root, ledger.Artifact{Target: tgt, Kind: "mcp", Path: ".mcp.json", Key: "/mcpServers/a"})).To(Equal(sum(`{"command":"x"}`)))
	})
})
