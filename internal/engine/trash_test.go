package engine_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
	"github.com/trevor-vaughan/lola-go/internal/trash"
)

var _ = Describe("lola trash: listing, restoring and emptying the trash", func() {
	var (
		tmp, proj string
		p         paths.Paths
		e         *engine.Engine
	)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p)
		e.Now = func() time.Time { return now }
		proj = filepath.Join(tmp, "proj")
		writeTree(proj, map[string]string{
			".mcp.json":                      "{\"mcpServers\":{}}\n",
			".claude/commands/ship.md":       "ship\n",
			".claude/skills/review/SKILL.md": "skill\n",
			".claude/skills/review/ref/a.md": "a\n",
		})
	})
	// makeRun moves names (paths relative to proj) into a run made at, copies copied as credential
	// copies, and returns the run's id.
	makeRun := func(at time.Time, rec trash.RunRecord, copied []string, names ...string) string {
		GinkgoHelper()
		t, err := trash.OpenTrash(p.Trash, at, rec)
		Expect(err).NotTo(HaveOccurred())
		for _, c := range copied {
			b, err := os.ReadFile(filepath.Join(proj, c))
			Expect(err).NotTo(HaveOccurred())
			to, err := t.CopyBytes("abc", proj, filepath.Join(proj, c), b)
			Expect(err).NotTo(HaveOccurred())
			t.MarkCredential(to)
		}
		for _, n := range names {
			root, err := os.OpenRoot(filepath.Join(proj, filepath.Dir(n)))
			Expect(err).NotTo(HaveOccurred())
			_, err = t.Move("abc", proj, root, filepath.Base(n))
			Expect(err).NotTo(HaveOccurred())
			Expect(root.Close()).To(Succeed())
		}
		Expect(t.Close()).To(Succeed())
		return filepath.Base(t.Dir)
	}
	tidyRec := func() trash.RunRecord {
		return trash.RunRecord{Command: "lola tidy -C " + proj, Scope: "project", Root: proj}
	}
	at := func(rel string) string { return filepath.Join(proj, filepath.FromSlash(rel)) }

	It("lists runs newest first, with each one's command, scope, root, files and size", func() {
		old := makeRun(now.Add(-48*time.Hour), trash.RunRecord{Command: "lola migrate", Scope: "user"}, nil, ".claude/commands/ship.md")
		recent := makeRun(now.Add(-time.Hour), tidyRec(), []string{".mcp.json"}, ".claude/skills/review")
		list, err := e.ListTrash()
		Expect(err).NotTo(HaveOccurred())
		Expect(list.Notices).To(BeEmpty())
		doc, err := json.Marshal(list.Runs)
		Expect(err).NotTo(HaveOccurred())
		Expect(doc).To(MatchJSON(fmt.Sprintf(`[
			{"id": %q, "time": "2026-11-03T11:00:00Z", "command": %q, "scope": "project", "root": %q, "files": 3, "size_bytes": %d},
			{"id": %q, "time": "2026-11-01T12:00:00Z", "command": "lola migrate", "scope": "user", "root": "", "files": 1, "size_bytes": 5}]`,
			recent, "lola tidy -C "+proj, proj, len("{\"mcpServers\":{}}\nskill\na\n"), old)))
	})

	It("lists an empty trash as no runs, and a directory that is no run as a notice", func() {
		list, err := e.ListTrash()
		Expect(err).NotTo(HaveOccurred())
		Expect(list.Runs).To(BeEmpty())
		Expect(list.Runs).NotTo(BeNil())
		Expect(os.MkdirAll(filepath.Join(p.Trash, "stray"), 0o700)).To(Succeed())
		list, err = e.ListTrash()
		Expect(err).NotTo(HaveOccurred())
		Expect(list.Runs).To(BeEmpty())
		Expect(list.Notices).To(ConsistOf(HaveField("Text", ContainSubstring("stray"))))
	})

	It("shows a run's entries, the credential copies marked, and escapes what the run says", func() {
		id := makeRun(now, trash.RunRecord{Command: "lola tidy \x1b[2J", Scope: "project", Root: proj}, []string{".mcp.json"}, ".claude/commands/ship.md")
		show, err := e.ShowTrash(id, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(show.ID).To(Equal(id))
		Expect(show.Command).To(Equal("lola tidy ?[2J"))
		run := filepath.Join(p.Trash, id, "abc")
		Expect(show.Entries).To(Equal([]engine.TrashEntry{
			{Original: at(".mcp.json"), Trash: filepath.Join(run, ".mcp.json"), Credentials: true},
			{Original: at(".claude/commands/ship.md"), Trash: filepath.Join(run, ".claude/commands/ship.md")},
		}))
	})

	It("marks a directory entry that holds a credential copy", func() {
		t, err := trash.OpenTrash(p.Trash, now, tidyRec())
		Expect(err).NotTo(HaveOccurred())
		root, err := os.OpenRoot(at(".claude/skills"))
		Expect(err).NotTo(HaveOccurred())
		to, err := t.Move("abc", proj, root, "review")
		Expect(err).NotTo(HaveOccurred())
		Expect(root.Close()).To(Succeed())
		t.MarkCredential(filepath.Join(to, "ref", "a.md"))
		Expect(t.Close()).To(Succeed())
		show, err := e.ShowTrash(filepath.Base(t.Dir), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(show.Entries).To(ConsistOf(HaveField("Credentials", true)))
	})

	It("refuses a run the trash does not hold, hinting the listing", func() {
		for _, id := range []string{"20990101T000000Z", "..", "a/b", ""} {
			_, err := e.ShowTrash(id, []string{"--json"})
			Expect(err).To(MatchError(ContainSubstring("no trash run")), id)
			Expect(errs.Hints(err)).To(Equal([]string{"see the runs: lola trash --json"}), id)
		}
	})

	It("restores every entry of a run, then removes the emptied run", func() {
		id := makeRun(now, tidyRec(), nil, ".claude/commands/ship.md", ".claude/skills/review")
		plan, err := e.PlanTrashRestore(engine.TrashRestoreRequest{Run: id})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.DryRun).To(BeTrue())
		Expect(plan.Restored).To(HaveLen(2))
		res, err := e.RestoreTrash(plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.DryRun).To(BeFalse())
		Expect(res.Restored).To(HaveLen(2))
		Expect(res.RunRemoved).To(BeTrue())
		Expect(os.ReadFile(at(".claude/commands/ship.md"))).To(Equal([]byte("ship\n")))
		Expect(os.ReadFile(at(".claude/skills/review/ref/a.md"))).To(Equal([]byte("a\n")), "a directory entry comes back whole")
		Expect(filepath.Join(p.Trash, id)).NotTo(BeADirectory())
	})

	It("restores only the named originals, and forgets only their lines", func() {
		id := makeRun(now, tidyRec(), nil, ".claude/commands/ship.md", ".claude/skills/review")
		plan, err := e.PlanTrashRestore(engine.TrashRestoreRequest{Run: id, Originals: []string{at(".claude/commands/ship.md")}})
		Expect(err).NotTo(HaveOccurred())
		res, err := e.RestoreTrash(plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Restored).To(Equal([]engine.TrashEntry{{Original: at(".claude/commands/ship.md"),
			Trash: filepath.Join(p.Trash, id, "abc", ".claude/commands/ship.md")}}))
		Expect(res.RunRemoved).To(BeFalse())
		Expect(at(".claude/commands/ship.md")).To(BeARegularFile())
		Expect(at(".claude/skills/review")).NotTo(BeADirectory())
		show, err := e.ShowTrash(id, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(show.Entries).To(ConsistOf(HaveField("Original", at(".claude/skills/review"))))
	})

	It("restores only the entries a show names, by identity, never another whose original shows the same", func() {
		writeTree(proj, map[string]string{".claude/commands/a\x01.md": "one\n", ".claude/commands/a\x02.md": "two\n"})
		id := makeRun(now, tidyRec(), nil, ".claude/commands/a\x01.md", ".claude/commands/a\x02.md")
		show, err := e.ShowTrash(id, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(show.Entries).To(HaveLen(2))
		Expect(show.Entries[0].Original).To(Equal(show.Entries[1].Original), "both show as the same text")
		first := 0
		plan, err := e.PlanTrashRestore(engine.TrashRestoreRequest{Run: id, Entries: []engine.TrashEntryRef{show.Ref(first)}})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Restored).To(HaveLen(1))
		_, err = e.RestoreTrash(plan)
		Expect(err).NotTo(HaveOccurred())
		restored := 0
		for _, name := range []string{"a\x01.md", "a\x02.md"} {
			if _, err := os.Lstat(at(".claude/commands/" + name)); err == nil {
				restored++
			}
		}
		Expect(restored).To(Equal(1), "only the entry named")

		By("an entry the run no longer holds is refused, hinting the run's entries")
		_, err = e.PlanTrashRestore(engine.TrashRestoreRequest{Run: id, Entries: []engine.TrashEntryRef{show.Ref(first)}, Display: []string{"--plain"}})
		Expect(err).To(MatchError(ContainSubstring("trash run " + id + " no longer holds " + show.Entries[first].Original)))
		Expect(errs.Hints(err)).To(Equal([]string{"see the run's entries: lola trash " + id + " --plain"}))
	})

	It("finds plans of two runs that would restore the same original, or one inside another, naming both runs", func() {
		a := makeRun(now.Add(-time.Hour), tidyRec(), nil, ".claude/commands/ship.md", ".claude/skills/review")
		writeTree(proj, map[string]string{".claude/commands/ship.md": "again\n", ".claude/skills/review/SKILL.md": "again\n"})
		b := makeRun(now, tidyRec(), nil, ".claude/commands/ship.md")
		c := makeRun(now.Add(time.Hour), tidyRec(), nil, ".claude/skills/review/SKILL.md")
		plan := func(id string) *engine.TrashRestoreResult {
			GinkgoHelper()
			pl, err := e.PlanTrashRestore(engine.TrashRestoreRequest{Run: id})
			Expect(err).NotTo(HaveOccurred())
			return pl
		}
		Expect(engine.TrashRestoresOverlap(plan(a), plan(b))).To(MatchError(
			"trash runs " + a + " and " + b + " both hold " + at(".claude/commands/ship.md") + ", which one restore cannot put back twice"))
		Expect(engine.TrashRestoresOverlap(plan(c), plan(a))).To(MatchError(
			"trash run " + c + " holds " + at(".claude/skills/review/SKILL.md") + " and trash run " + a + " holds " + at(".claude/skills/review") +
				", one inside the other, which one restore cannot both put back"))
		Expect(engine.TrashRestoresOverlap(plan(b), plan(c))).To(Succeed())
	})

	It("refuses a named path the run does not hold, hinting the run's entries", func() {
		id := makeRun(now, tidyRec(), nil, ".claude/commands/ship.md")
		_, err := e.PlanTrashRestore(engine.TrashRestoreRequest{Run: id, Originals: []string{at("nope.md")}, Display: []string{"--plain"}})
		Expect(err).To(MatchError(ContainSubstring("does not hold " + at("nope.md"))))
		Expect(errs.Hints(err)).To(Equal([]string{"see the run's entries: lola trash " + id + " --plain"}))
		Expect(filepath.Join(p.Trash, id, "abc", ".claude/commands/ship.md")).To(BeARegularFile())
	})

	It("refuses the run when an original exists, listing it, and restores nothing", func() {
		id := makeRun(now, tidyRec(), nil, ".claude/commands/ship.md", ".claude/skills/review")
		writeTree(proj, map[string]string{".claude/commands/ship.md": "new\n"})
		plan, err := e.PlanTrashRestore(engine.TrashRestoreRequest{Run: id})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Existing).To(Equal([]string{at(".claude/commands/ship.md")}))
		_, err = e.RestoreTrash(plan)
		Expect(err).To(MatchError(ContainSubstring(at(".claude/commands/ship.md"))))
		Expect(errs.Hints(err)).To(ContainElement("move what is there to a new trash run, then restore: lola trash " + id + " --restore --force"))
		Expect(os.ReadFile(at(".claude/commands/ship.md"))).To(Equal([]byte("new\n")))
		Expect(at(".claude/skills/review")).NotTo(BeADirectory(), "the whole run is refused")
	})

	It("with --force moves an existing original into a new trash run, then restores", func() {
		id := makeRun(now.Add(-time.Hour), tidyRec(), []string{".mcp.json"})
		writeTree(proj, map[string]string{".mcp.json": "{\"edited\":true}\n"})
		plan, err := e.PlanTrashRestore(engine.TrashRestoreRequest{Run: id, Force: true})
		Expect(err).NotTo(HaveOccurred())
		res, err := e.RestoreTrash(plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Displaced).NotTo(BeEmpty())
		Expect(os.ReadFile(at(".mcp.json"))).To(Equal([]byte("{\"mcpServers\":{}}\n")))
		fi, err := os.Stat(at(".mcp.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)), "a credential copy comes back private")
		displaced, err := trash.ReadRun(filepath.Join(p.Trash, res.Displaced))
		Expect(err).NotTo(HaveOccurred())
		Expect(displaced.Command).To(Equal("lola trash " + id + " --restore --force"))
		Expect(displaced.Entries).To(HaveLen(1))
		Expect(displaced.Entries[0].Original).To(Equal(at(".mcp.json")))
		Expect(displaced.Entries[0].Credential).To(BeTrue())
		Expect(os.ReadFile(displaced.Entries[0].Trash)).To(Equal([]byte("{\"edited\":true}\n")), "never deleted")
	})

	It("recreates the directory an original was in", func() {
		id := makeRun(now, tidyRec(), nil, ".claude/commands/ship.md")
		Expect(os.RemoveAll(filepath.Join(proj, ".claude"))).To(Succeed())
		plan, err := e.PlanTrashRestore(engine.TrashRestoreRequest{Run: id})
		Expect(err).NotTo(HaveOccurred())
		_, err = e.RestoreTrash(plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.ReadFile(at(".claude/commands/ship.md"))).To(Equal([]byte("ship\n")))
	})

	It("passes over an entry gone from the trash, with a notice, and forgets its line", func() {
		id := makeRun(now, tidyRec(), nil, ".claude/commands/ship.md", ".claude/skills/review")
		Expect(os.Remove(filepath.Join(p.Trash, id, "abc", ".claude/commands/ship.md"))).To(Succeed())
		plan, err := e.PlanTrashRestore(engine.TrashRestoreRequest{Run: id})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Restored).To(ConsistOf(HaveField("Original", at(".claude/skills/review"))))
		Expect(plan.Notices).To(ConsistOf(HaveField("Text", ContainSubstring("is gone from the trash"))))
		res, err := e.RestoreTrash(plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RunRemoved).To(BeTrue())
	})

	It("plans a restore with -n and writes nothing", func() {
		id := makeRun(now, tidyRec(), nil, ".claude/commands/ship.md")
		plan, err := e.PlanTrashRestore(engine.TrashRestoreRequest{Run: id, DryRun: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Restored).To(HaveLen(1))
		Expect(at(".claude/commands/ship.md")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(p.Trash, id, "abc", ".claude/commands/ship.md")).To(BeARegularFile())
	})

	It("empties the named runs, or those older than an age", func() {
		old := makeRun(now.Add(-10*24*time.Hour), tidyRec(), nil, ".claude/commands/ship.md")
		mid := makeRun(now.Add(-3*24*time.Hour), tidyRec(), nil, ".claude/skills/review")
		recent := makeRun(now, tidyRec(), []string{".mcp.json"})

		plan, err := e.PlanTrashEmpty(engine.TrashEmptyRequest{OlderThan: "1w"})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.DryRun).To(BeTrue())
		Expect(plan.Removed).To(ConsistOf(HaveField("ID", old)))
		Expect(filepath.Join(p.Trash, old)).To(BeADirectory(), "a plan writes nothing")
		res, err := e.EmptyTrash(plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.DryRun).To(BeFalse())
		Expect(res.BytesFreed).To(Equal(int64(5)))
		Expect(filepath.Join(p.Trash, old)).NotTo(BeADirectory())

		plan, err = e.PlanTrashEmpty(engine.TrashEmptyRequest{Runs: []string{recent}})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Removed).To(ConsistOf(HaveField("ID", recent)))
		_, err = e.EmptyTrash(plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(p.Trash, recent)).NotTo(BeADirectory())
		Expect(filepath.Join(p.Trash, mid)).To(BeADirectory())

		plan, err = e.PlanTrashEmpty(engine.TrashEmptyRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Removed).To(ConsistOf(HaveField("ID", mid)))
	})

	It("keeps a named run newer than the age, with a notice", func() {
		id := makeRun(now, tidyRec(), nil, ".claude/commands/ship.md")
		plan, err := e.PlanTrashEmpty(engine.TrashEmptyRequest{Runs: []string{id}, OlderThan: "1w"})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Removed).To(BeEmpty())
		Expect(plan.Notices).To(ConsistOf(HaveField("Text", "trash run "+id+" is kept: it is newer than 1w")))
	})

	It("keeps a run that changed since the plan, with the run that plans again", func() {
		id := makeRun(now, tidyRec(), nil, ".claude/skills/review")
		other := makeRun(now.Add(-time.Hour), tidyRec(), nil, ".claude/commands/ship.md")
		plan, err := e.PlanTrashEmpty(engine.TrashEmptyRequest{OlderThan: "", Display: []string{"--json"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Removed).To(HaveLen(2))
		writeTree(filepath.Join(p.Trash, id, "abc", ".claude/skills/review"), map[string]string{"late.md": "late\n"})
		res, err := e.EmptyTrash(plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Removed).To(ConsistOf(HaveField("ID", other)))
		Expect(res.Notices).To(ConsistOf(HaveField("Text", "trash run "+id+" changed since the plan, so it is kept; rerun: lola trash --empty --json")))
		Expect(filepath.Join(p.Trash, id, "abc", ".claude/skills/review", "late.md")).To(BeARegularFile())
	})

	It("restores a credential copy whose mode it cannot make private, forgetting its line and warning", func() {
		if os.Geteuid() == 0 {
			Skip("root opens a file of mode 000")
		}
		id := makeRun(now, tidyRec(), []string{".mcp.json"})
		Expect(os.Remove(at(".mcp.json"))).To(Succeed())
		Expect(os.Chmod(filepath.Join(p.Trash, id, "abc", ".mcp.json"), 0)).To(Succeed())
		plan, err := e.PlanTrashRestore(engine.TrashRestoreRequest{Run: id})
		Expect(err).NotTo(HaveOccurred())
		res, err := e.RestoreTrash(plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Restored).To(HaveLen(1))
		Expect(res.RunRemoved).To(BeTrue(), "its line was forgotten")
		Expect(res.Notices).To(ConsistOf(And(HaveField("Level", engine.LevelWarn),
			HaveField("Text", And(ContainSubstring("restored "+at(".mcp.json")), ContainSubstring("0600"))))))
		Expect(os.Chmod(at(".mcp.json"), 0o600)).To(Succeed())
	})

	It("refuses an age that is none, and a run the trash does not hold", func() {
		Expect(engine.ParseOlderThan("")).Error().To(MatchError(ContainSubstring(`--older-than ""`)))
		for _, age := range []string{"never", "3", "-1d", "1y"} {
			_, err := e.PlanTrashEmpty(engine.TrashEmptyRequest{OlderThan: age})
			Expect(err).To(MatchError(ContainSubstring("--older-than")), age)
		}
		_, err := e.PlanTrashEmpty(engine.TrashEmptyRequest{Runs: []string{"20990101T000000Z"}})
		Expect(err).To(MatchError(ContainSubstring("no trash run")))
		Expect(errs.Hints(err)).To(Equal([]string{"see the runs: lola trash"}))
	})

	It("does not follow a run that is a link out of the trash", func() {
		outside := filepath.Join(tmp, "outside")
		writeTree(outside, map[string]string{"manifest.txt": "", "keep.md": "keep\n"})
		Expect(os.MkdirAll(p.Trash, 0o700)).To(Succeed())
		Expect(os.Symlink(outside, filepath.Join(p.Trash, "20260101T000000Z"))).To(Succeed())
		_, err := e.PlanTrashEmpty(engine.TrashEmptyRequest{Runs: []string{"20260101T000000Z"}})
		Expect(err).To(MatchError(ContainSubstring("no trash run")))
		plan, err := e.PlanTrashEmpty(engine.TrashEmptyRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Removed).To(BeEmpty())
		Expect(filepath.Join(outside, "keep.md")).To(BeARegularFile())
	})
})
