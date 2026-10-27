package engine_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
)

// trashRuns lists the run directories under the trash.
func trashRuns(t *tidyEnv) []string {
	entries, err := os.ReadDir(t.p.Trash)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	Expect(err).NotTo(HaveOccurred())
	var out []string
	for _, e := range entries {
		out = append(out, filepath.Join(t.p.Trash, e.Name()))
	}
	return out
}

// projectSub is the trash subdirectory of a project scope: the hash of its key.
func projectSub(sc engine.Scope) string {
	sum := sha256.Sum256([]byte(sc.Key()))
	return hex.EncodeToString(sum[:8])
}

// confirmWith answers the review yes after running change.
func confirmWith(change func()) func(engine.TidyChoice) (bool, error) {
	return func(engine.TidyChoice) (bool, error) {
		change()
		return true, nil
	}
}

var _ = Describe("lola tidy: choosing and moving", func() {
	var t *tidyEnv
	BeforeEach(func() {
		t = newTidyEnv()
		t.install(t.sc)
	})

	It("a unit that is a symbolic link moves as a link and its target is untouched", func() {
		outside := filepath.Join(t.tmp, "outside.md")
		Expect(os.WriteFile(outside, []byte("mine\n"), 0o644)).To(Succeed())
		t.link(outside, filepath.Join(t.proj, ".claude", "commands", "ln.md"))
		res, err := t.tidy(engine.TidyRequest{Select: pats("command/ln"), Yes: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed(res.Moved)).To(Equal([]string{"claude-code command/ln"}))
		Expect(os.Readlink(res.Moved[0].Trash)).To(Equal(outside))
		Expect(os.ReadFile(outside)).To(Equal([]byte("mine\n")))
		_, err = os.Lstat(filepath.Join(t.proj, ".claude", "commands", "ln.md"))
		Expect(err).To(MatchError(os.ErrNotExist))
	})

	It("a choice moves exactly the chosen units", func() {
		t.files(t.proj, map[string]string{".claude/skills/*/SKILL.md": "star\n", ".claude/skills/x/SKILL.md": "x\n",
			".claude/commands/dup.md": "c\n", ".gemini/commands/dup.toml": "g\n"})
		var asked engine.TidyChoice
		choose := func(id func(engine.TidyScan) engine.UnitID) func(engine.TidyScan) (engine.TidyChoice, error) {
			return func(s engine.TidyScan) (engine.TidyChoice, error) {
				return engine.TidyChoice{Move: []engine.UnitID{id(s)}}, nil
			}
		}
		find := func(s engine.TidyScan, target, kind, name string) engine.UnitID {
			for _, c := range s.Candidates {
				if c.Target == target && c.Kind == kind && c.Name == name {
					return c.ID
				}
			}
			Fail("no candidate " + target + " " + kind + "/" + name)
			return engine.UnitID{}
		}
		confirm := func(c engine.TidyChoice) (bool, error) { asked = c; return true, nil }
		res, err := t.tidy(engine.TidyRequest{Targets: []string{"claude-code", "gemini-cli"}, Confirm: confirm,
			Choose: choose(func(s engine.TidyScan) engine.UnitID { return find(s, "claude-code", "skill", "*") })})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed(res.Moved)).To(Equal([]string{"claude-code skill/*"}))
		Expect(filepath.Join(t.proj, ".claude", "skills", "x")).To(BeADirectory())
		Expect(asked.Command).To(Equal(`lola tidy --project 'skill/\*' -t claude-code -t gemini-cli`))

		res, err = t.tidy(engine.TidyRequest{Targets: []string{"claude-code", "gemini-cli"}, Confirm: confirm,
			Choose: choose(func(s engine.TidyScan) engine.UnitID { return find(s, "claude-code", "command", "dup") })})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed(res.Moved)).To(Equal([]string{"claude-code command/dup"}))
		Expect(filepath.Join(t.proj, ".gemini", "commands", "dup.toml")).To(BeAnExistingFile())
		Expect(asked.Command).To(Equal("lola tidy --project command/dup -t claude-code"), "-t narrows a name two targets share")

		By("the keep answers join the equivalent command as --keep, unless a selector would be named to move and to keep")
		t.files(t.proj, map[string]string{".claude/commands/dup.md": "c\n", ".claude/skills/x/SKILL.md": "x\n"})
		keepToo := func(kind, name string, keep string) func(engine.TidyScan) (engine.TidyChoice, error) {
			return func(s engine.TidyScan) (engine.TidyChoice, error) {
				return engine.TidyChoice{Move: []engine.UnitID{find(s, "claude-code", kind, name)}, Keep: pats(keep)}, nil
			}
		}
		_, err = t.tidy(engine.TidyRequest{Targets: []string{"claude-code", "gemini-cli"}, Confirm: func(c engine.TidyChoice) (bool, error) {
			asked = c
			return false, nil
		}, Choose: keepToo("skill", "x", "command/dup")})
		Expect(err).To(MatchError(engine.ErrDeclined))
		Expect(asked.Command).To(Equal("lola tidy --project skill/x -t claude-code -t gemini-cli --keep command/dup"))
		_, err = t.tidy(engine.TidyRequest{Targets: []string{"claude-code", "gemini-cli"}, Confirm: func(c engine.TidyChoice) (bool, error) {
			asked = c
			return false, nil
		}, Choose: keepToo("command", "dup", "command/dup")})
		Expect(err).To(MatchError(engine.ErrDeclined))
		Expect(asked.Command).To(BeEmpty(), "lola tidy command/dup --keep command/dup is refused")

		By("a set no single command selects")
		t.files(t.proj, map[string]string{".claude/commands/dup.md": "c\n", ".claude/commands/dup/ref.md": "c\n"})
		res, err = t.tidy(engine.TidyRequest{Targets: []string{"claude-code"}, Confirm: confirm,
			Choose: func(s engine.TidyScan) (engine.TidyChoice, error) {
				for _, c := range s.Candidates {
					if c.Rel == ".claude/commands/dup.md" {
						return engine.TidyChoice{Move: []engine.UnitID{c.ID}}, nil
					}
				}
				return engine.TidyChoice{}, nil
			}})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed(res.Moved)).To(Equal([]string{"claude-code command/dup"}))
		Expect(asked.Command).To(BeEmpty())
		Expect(filepath.Join(t.proj, ".claude", "commands", "dup")).To(BeADirectory())
	})

	It("a choice that only keeps asks the review, then writes the keep-list and moves nothing; declined, it writes nothing", func() {
		t.files(t.proj, map[string]string{".claude/skills/old/SKILL.md": "x\n"})
		choose := func(engine.TidyScan) (engine.TidyChoice, error) {
			return engine.TidyChoice{Keep: pats("skill/old")}, nil
		}
		asked := 0
		_, err := t.tidy(engine.TidyRequest{Choose: choose, Confirm: func(c engine.TidyChoice) (bool, error) { asked++; return false, nil }})
		Expect(err).To(MatchError(engine.ErrDeclined))
		Expect(asked).To(Equal(1))
		Expect(t.p.Keep).NotTo(BeAnExistingFile())

		res, err := t.tidy(engine.TidyRequest{Choose: choose, Confirm: func(c engine.TidyChoice) (bool, error) {
			Expect(c.Items).To(BeEmpty())
			Expect(c.Command).To(Equal("lola tidy --project --keep skill/old"), "the keep-only run")
			return true, nil
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Moved).To(BeEmpty())
		Expect(res.KeepAdded).To(Equal([]string{"skill/old"}))
		Expect(os.ReadFile(t.p.Keep)).To(ContainSubstring("- skill/old"))
		Expect(filepath.Join(t.proj, ".claude", "skills", "old")).To(BeADirectory())
		Expect(trashRuns(t)).To(BeEmpty())
	})

	It("a choice naming a unit lola did not offer refuses before anything moves", func() {
		t.files(t.proj, map[string]string{".claude/skills/old/SKILL.md": "x\n", ".claude/commands/CLAUDE.md": "x\n"})
		_, confirm := never()
		for _, choose := range []func(engine.TidyScan) (engine.TidyChoice, error){
			func(s engine.TidyScan) (engine.TidyChoice, error) {
				return engine.TidyChoice{Move: []engine.UnitID{s.Candidates[0].ID, s.Protected[0].ID}}, nil
			},
			func(s engine.TidyScan) (engine.TidyChoice, error) {
				return engine.TidyChoice{Move: []engine.UnitID{s.Candidates[0].ID}, Keep: pats("skill/o*")}, nil
			},
			func(s engine.TidyScan) (engine.TidyChoice, error) {
				id := s.Candidates[0].ID
				id.Path += "/../../commands/CLAUDE.md"
				return engine.TidyChoice{Move: []engine.UnitID{id}}, nil
			},
		} {
			_, err := t.tidy(engine.TidyRequest{Choose: choose, Confirm: confirm})
			Expect(err).To(MatchError("the picker answered with an item lola did not offer"))
			Expect(errs.Hints(err)).To(BeEmpty())
			Expect(filepath.Join(t.proj, ".claude", "skills", "old")).To(BeADirectory())
			Expect(trashRuns(t)).To(BeEmpty())
			Expect(t.p.Keep).NotTo(BeAnExistingFile())
		}
	})

	It("a keep-list selector locks what it matches in its own scope only", func() {
		t.files(filepath.Dir(t.p.Keep), map[string]string{"keep.yaml": "version: 1\nuser:\n  - skill/old\nprojects:\n  " +
			t.proj + ":\n    - command/t*\n"})
		for _, root := range []string{t.proj, t.home} {
			t.files(root, map[string]string{".claude/skills/old/SKILL.md": "x\n", ".claude/commands/tmp.md": "x\n"})
		}
		res := t.dry(t.sc)
		Expect(listed(res.Candidates)).To(Equal([]string{"claude-code skill/old"}))
		Expect(codes(res.Protected)).To(Equal([]string{"claude-code command/tmp kept"}))
		Expect(res.Protected[0].Reason).To(Equal("on your keep-list (command/t*)"))
		res = t.dry(t.user, "claude-code")
		Expect(listed(res.Candidates)).To(Equal([]string{"claude-code command/tmp"}))
		Expect(codes(res.Protected)).To(Equal([]string{"claude-code skill/old kept"}))
	})

	It("--keep and --unkeep apply to this run's scan", func() {
		keep := "version: 1\nprojects:\n  " + t.proj + ":\n    - skill/old\n"
		t.files(filepath.Dir(t.p.Keep), map[string]string{"keep.yaml": keep})
		t.files(t.proj, map[string]string{".claude/skills/old/SKILL.md": "x\n", ".claude/commands/tmp.md": "x\n"})
		res, err := t.tidy(engine.TidyRequest{Dry: true, Keep: pats("command/tmp"), Unkeep: pats("skill/old")})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed(res.Candidates)).To(Equal([]string{"claude-code skill/old"}))
		Expect(codes(res.Protected)).To(Equal([]string{"claude-code command/tmp kept"}))
		Expect(os.ReadFile(t.p.Keep)).To(Equal([]byte(keep)), "a dry run writes nothing")

		res, err = t.tidy(engine.TidyRequest{Select: pats("skill/old"), Yes: true, Keep: pats("command/tmp"), Unkeep: pats("skill/old")})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed(res.Moved)).To(Equal([]string{"claude-code skill/old"}))
		Expect(res.KeepAdded).To(Equal([]string{"command/tmp"}))
		Expect(res.KeepRemoved).To(Equal([]string{"skill/old"}))
		Expect(os.ReadFile(t.p.Keep)).To(Equal([]byte("version: 1\nprojects:\n  " + t.proj + ":\n    - command/tmp\n")))
	})

	It("moved units land in one new trash run under their destination's subdirectory, mirrored below the resolved root, one manifest line per unit naming its resolved original, synced before the move", func() {
		t.files(t.proj, map[string]string{".claude/skills/old/SKILL.md": "x\n", ".claude/agents/scout.md": "x\n"})
		linked := filepath.Join(t.tmp, "linked-proj")
		t.link(t.proj, linked)
		sc := engine.Scope{Kind: target.Project, ProjectDir: linked}
		res, err := t.e.Tidy(tidyCtx, engine.TidyRequest{Scope: sc, All: true, Yes: true})
		Expect(err).NotTo(HaveOccurred())
		runs := trashRuns(t)
		Expect(runs).To(HaveLen(1))
		Expect(res.Trash).To(Equal(runs[0]))
		sub := filepath.Join(runs[0], projectSub(t.sc))
		Expect(listed(res.Moved)).To(Equal([]string{"claude-code skill/old", "claude-code agent/scout"}))
		Expect(res.Moved[0].Trash).To(Equal(filepath.Join(sub, ".claude", "skills", "old")))
		Expect(res.Moved[1].Trash).To(Equal(filepath.Join(sub, ".claude", "agents", "scout.md")))
		Expect(filepath.Join(sub, ".claude", "skills", "old", "SKILL.md")).To(BeAnExistingFile())
		b, err := os.ReadFile(filepath.Join(runs[0], "manifest.txt"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal(filepath.Join(t.proj, ".claude", "skills", "old") + "\t" + res.Moved[0].Trash + "\n" +
			filepath.Join(t.proj, ".claude", "agents", "scout.md") + "\t" + res.Moved[1].Trash + "\n"))
		Expect(res.SizeBytes).To(Equal(int64(4)))
	})

	It("an MCP file's verified bytes are copied into the trash at 0600 before the edit, and a linked MCP file stays a link", func() {
		Expect(os.Remove(filepath.Join(t.proj, ".mcp.json"))).To(Succeed())
		orig := "{\n  // servers\n  \"mcpServers\": {\n    \"a\": {\"command\": \"a-mcp\"},\n    \"b\": {\"command\": \"b-mcp\"}\n  }\n}\n"
		t.files(t.proj, map[string]string{"config/mcp.json": orig})
		t.link("config/mcp.json", filepath.Join(t.proj, ".mcp.json"))
		res, err := t.tidy(engine.TidyRequest{Select: pats("mcp/a"), Yes: true})
		Expect(err).NotTo(HaveOccurred())
		copyPath := filepath.Join(trashRuns(t)[0], projectSub(t.sc), "config", "mcp.json")
		Expect(listed(res.Moved)).To(Equal([]string{"claude-code mcp/a"}))
		Expect(res.Moved[0].Trash).To(Equal(copyPath))
		Expect(res.TrashCredentials).To(Equal([]string{copyPath}))
		Expect(os.ReadFile(copyPath)).To(Equal([]byte(orig)))
		fi, err := os.Stat(copyPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		Expect(os.Readlink(filepath.Join(t.proj, ".mcp.json"))).To(Equal("config/mcp.json"))
		Expect(os.ReadFile(filepath.Join(t.proj, "config", "mcp.json"))).To(Equal([]byte(
			"{\n  // servers\n  \"mcpServers\": {\n    \"b\": {\"command\": \"b-mcp\"}\n  }\n}\n")))
		fi, err = os.Stat(filepath.Join(t.proj, "config", "mcp.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o644)))
	})

	It("a unit that changed since the scan is skipped with a notice", func() {
		t.git(t.proj, "init", "-q")
		outside := filepath.Join(t.tmp, "outside")
		t.files(outside, map[string]string{"e.md": "outside\n"})
		t.files(t.proj, map[string]string{".claude/commands/a.md": "a\n", ".claude/skills/b/SKILL.md": "b\n", ".claude/skills/c/SKILL.md": "c\n",
			".claude/skills/d/SKILL.md": "d\n", ".claude/agents/e.md": "e\n"})
		res, err := t.tidy(engine.TidyRequest{Select: pats("command/a", "skill/b", "skill/c", "skill/d", "agent/e"), Confirm: confirmWith(func() {
			Expect(os.Remove(filepath.Join(t.proj, ".claude", "commands", "a.md"))).To(Succeed())
			Expect(os.WriteFile(filepath.Join(t.proj, ".claude", "commands", "a.md"), []byte("a again\n"), 0o644)).To(Succeed())
			Expect(os.Rename(filepath.Join(t.proj, ".claude", "skills", "b"), filepath.Join(t.proj, "b-moved"))).To(Succeed())
			t.link("../../b-moved", filepath.Join(t.proj, ".claude", "skills", "b"))
			Expect(os.WriteFile(filepath.Join(t.proj, ".claude", "skills", "c", "CLAUDE.md"), []byte("x\n"), 0o644)).To(Succeed())
			t.commit(t.proj, ".claude/skills/d/SKILL.md")
			Expect(os.Rename(filepath.Join(t.proj, ".claude", "agents"), filepath.Join(t.proj, "agents-moved"))).To(Succeed())
			t.link(outside, filepath.Join(t.proj, ".claude", "agents"))
		})})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Moved).To(BeEmpty())
		Expect(codes(res.Skipped)).To(Equal([]string{"claude-code skill/b changed", "claude-code skill/c instruction_file",
			"claude-code skill/d tracked", "claude-code command/a changed", "claude-code agent/e link_out"}))
		Expect(res.Skipped[0].Reason).To(Equal("changed since lola looked; run lola tidy again"))
		Expect(noticeTexts(res.Notices)).To(ContainElement("skill/b (.claude/skills/b) changed since lola looked; run lola tidy again"))
		Expect(os.ReadFile(filepath.Join(outside, "e.md"))).To(Equal([]byte("outside\n")))
		Expect(os.ReadFile(filepath.Join(t.proj, ".claude", "commands", "a.md"))).To(Equal([]byte("a again\n")))
	})

	It("an MCP member whose value changed since the scan is kept, and an MCP file that became a link out is skipped", func() {
		write := func(s string) {
			Expect(os.WriteFile(filepath.Join(t.proj, ".mcp.json"), []byte(s), 0o644)).To(Succeed())
		}
		write(`{"mcpServers": {"a": {"command": "a"}, "b": {"command": "b"}}}`)
		res, err := t.tidy(engine.TidyRequest{Select: pats("mcp/a", "mcp/b"), Confirm: confirmWith(func() {
			write(`{"mcpServers": {"a": {"command": "a2"}, "b": {"command": "b"}}}`)
		})})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed(res.Moved)).To(Equal([]string{"claude-code mcp/b"}))
		Expect(codes(res.Skipped)).To(Equal([]string{"claude-code mcp/a changed"}))
		Expect(os.ReadFile(filepath.Join(t.proj, ".mcp.json"))).To(Equal([]byte(`{"mcpServers": {"a": {"command": "a2"}}}`)))

		outside := filepath.Join(t.tmp, "outside.json")
		Expect(os.WriteFile(outside, []byte(`{"mcpServers": {"a": {"command": "a2"}}}`), 0o644)).To(Succeed())
		res, err = t.tidy(engine.TidyRequest{Select: pats("mcp/a"), Confirm: confirmWith(func() {
			Expect(os.Remove(filepath.Join(t.proj, ".mcp.json"))).To(Succeed())
			t.link(outside, filepath.Join(t.proj, ".mcp.json"))
		})})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Moved).To(BeEmpty())
		Expect(codes(res.Skipped)).To(Equal([]string{"claude-code mcp/a link_out"}))
		Expect(os.ReadFile(outside)).To(Equal([]byte(`{"mcpServers": {"a": {"command": "a2"}}}`)))
	})

	It("an MCP server map whose file changed since the scan (opencode.json to opencode.jsonc) is skipped as changed", func() {
		t.files(t.proj, map[string]string{"opencode.json": `{"mcp": {"stray": {"type": "local", "command": ["x"]}}}`})
		res, err := t.tidy(engine.TidyRequest{Select: pats("mcp/stray"), Targets: []string{"opencode"}, Confirm: confirmWith(func() {
			t.files(t.proj, map[string]string{"opencode.jsonc": `{"mcp": {}}`})
		})})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Moved).To(BeEmpty())
		Expect(codes(res.Skipped)).To(Equal([]string{"opencode mcp/stray changed"}))
		Expect(os.ReadFile(filepath.Join(t.proj, "opencode.json"))).To(ContainSubstring("stray"))
	})

	It("keep edits are written before the scope lock is released", func() {
		t.files(t.proj, map[string]string{".claude/skills/old/SKILL.md": "x\n"})
		key, err := paths.ProjectKey(filepath.Join(t.proj, ".lola"))
		Expect(err).NotTo(HaveOccurred())
		sum := sha256.Sum256([]byte(paths.KeyID(key)))
		lockFile := filepath.Join(t.p.Locks, hex.EncodeToString(sum[:8])+".lock")
		seen := make(chan string, 1)
		_, err = t.tidy(engine.TidyRequest{Select: pats("skill/old"), Keep: pats("command/k"), Confirm: confirmWith(func() {
			go func() {
				defer GinkgoRecover()
				fl := flock.New(lockFile)
				Expect(fl.Lock()).To(Succeed())
				b, _ := os.ReadFile(t.p.Keep)
				Expect(fl.Unlock()).To(Succeed())
				seen <- string(b)
			}()
		})})
		Expect(err).NotTo(HaveOccurred())
		Eventually(seen, "5s").Should(Receive(ContainSubstring("- command/k")), "a run waiting on the scope lock reads the new keep-list")
	})

	It("tidy never writes lock.json, project.yml or the trust record", func() {
		t.files(t.proj, map[string]string{".claude/skills/old/SKILL.md": "x\n"})
		b, err := os.ReadFile(filepath.Join(t.proj, ".mcp.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(t.proj, ".mcp.json"), []byte(strings.Replace(string(b), `"mcpServers": {`, `"mcpServers": {"x": {},`, 1)), 0o644)).To(Succeed())
		files := []string{filepath.Join(t.proj, ".lola", "lock.json"), filepath.Join(t.proj, ".lola", "project.yml"), t.p.Trust}
		type stamp struct {
			b []byte
			m time.Time
		}
		before := map[string]stamp{}
		for _, f := range files {
			fi, err := os.Stat(f)
			Expect(err).NotTo(HaveOccurred(), f)
			b, err := os.ReadFile(f)
			Expect(err).NotTo(HaveOccurred())
			before[f] = stamp{b, fi.ModTime()}
		}
		res, err := t.tidy(engine.TidyRequest{All: true, Force: true, Keep: pats("command/k")})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed(res.Moved)).To(Equal([]string{"claude-code skill/old", "claude-code mcp/x"}))
		for _, f := range files {
			fi, err := os.Stat(f)
			Expect(err).NotTo(HaveOccurred())
			b, err := os.ReadFile(f)
			Expect(err).NotTo(HaveOccurred())
			Expect(stamp{b, fi.ModTime()}).To(Equal(before[f]), f)
		}
	})

	It("the first failed move stops the run, reports what moved, and still writes the keep edits", func() {
		t.files(t.proj, map[string]string{".claude/skills/old/SKILL.md": "x\n", ".claude/commands/tmp.md": "x\n", ".claude/agents/scout.md": "x\n"})
		commands := filepath.Join(t.proj, ".claude", "commands")
		Expect(os.Chmod(commands, 0o555)).To(Succeed())
		DeferCleanup(os.Chmod, commands, os.FileMode(0o755))
		_, err := t.tidy(engine.TidyRequest{All: true, Yes: true, Keep: pats("command/k")})
		Expect(err).To(MatchError(ContainSubstring("cannot move " + filepath.Join(commands, "tmp.md") + " into the trash: permission denied")))
		res := refusal(err)
		Expect(listed(res.Moved)).To(Equal([]string{"claude-code skill/old"}))
		Expect(res.Trash).NotTo(BeEmpty())
		Expect(errs.Hints(err)).To(BeEmpty(), "where the moved items are is a notice, not a command")
		Expect(noticeTexts(res.Notices)).To(ContainElement("what moved is in " + res.Trash + "; its manifest.txt lists each item's original path"))
		Expect(filepath.Join(t.proj, ".claude", "agents", "scout.md")).To(BeAnExistingFile())
		Expect(os.ReadFile(t.p.Keep)).To(ContainSubstring("- command/k"))
		Expect(res.KeepAdded).To(Equal([]string{"command/k"}))

		By("a keep-list that cannot be written still reports what moved and where")
		t.files(t.proj, map[string]string{".claude/skills/gone/SKILL.md": "x\n"})
		config := filepath.Dir(t.p.Keep)
		Expect(os.Chmod(config, 0o555)).To(Succeed())
		DeferCleanup(os.Chmod, config, os.FileMode(0o755))
		_, err = t.tidy(engine.TidyRequest{Select: pats("skill/gone"), Yes: true, Keep: pats("command/k2")})
		Expect(err).To(MatchError(ContainSubstring("could not write " + t.p.Keep)))
		res = refusal(err)
		Expect(listed(res.Moved)).To(Equal([]string{"claude-code skill/gone"}))
		Expect(res.Trash).NotTo(BeEmpty())
		Expect(noticeTexts(res.Notices)).To(ContainElement("what moved is in " + res.Trash + "; its manifest.txt lists each item's original path"))
		Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelWarn,
			Text: "nothing was written to the keep-list: close any program that holds it open and check its directory's permissions"}))
		Expect(errs.Hints(err)).To(Equal([]string{"lola tidy --project --keep command/k2"}), "what moved is gone: only the keep edits are left to do")
		for _, h := range errs.Hints(err) {
			Expect(h).NotTo(ContainSubstring("rerun"))
		}
		Expect(res.KeepAdded).To(BeEmpty())
		Expect(filepath.Join(t.proj, ".claude", "skills", "gone")).NotTo(BeAnExistingFile())
	})

	It("declining the review writes nothing, the keep-list included", func() {
		t.files(t.proj, map[string]string{".claude/skills/old/SKILL.md": "x\n"})
		var asked engine.TidyChoice
		_, err := t.tidy(engine.TidyRequest{Select: pats("skill/old"), Keep: pats("command/k"),
			Confirm: func(c engine.TidyChoice) (bool, error) { asked = c; return false, nil }})
		Expect(err).To(MatchError(engine.ErrDeclined))
		Expect(listed(asked.Items)).To(Equal([]string{"claude-code skill/old"}))
		Expect(filepath.Join(t.proj, ".claude", "skills", "old")).To(BeADirectory())
		Expect(trashRuns(t)).To(BeEmpty())
		Expect(t.p.Keep).NotTo(BeAnExistingFile())
	})
})
