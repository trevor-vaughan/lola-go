package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("lola repair", func() {
	var proj, lp string
	BeforeEach(func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(tmp, "proj")
		lp = filepath.Join(proj, ".lola", "lock.json")
		writeTree(filepath.Join(proj, "mods", "kit"), map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Kit\n"})
		r := lola("install", filepath.Join(proj, "mods", "kit"), "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		GinkgoT().Chdir(tmp) // hints name -C for a user outside the project
	})
	listed := "    module kit: target \"bogus\"\n    module kit: artifact \"x.md\" on target \"bogus\"\n"

	It("follows a refusal's hint: lists what it forgets, needs -y off a terminal, forgets, and the command works again", func() {
		plantTarget(proj)
		r := lola("list", "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: " + lp + `: module kit: target "bogus" is not a target lola knows` + "\n  hint: lola repair -C " + proj + "\n"))
		r = lola(shellWords("lola repair -C " + proj)[1:]...)
		Expect(r.code).To(Equal(1))
		Expect(r.out).To(BeEmpty())
		Expect(r.err).To(Equal("will forget 2 entries in " + lp + "; files are not touched:\n" + listed +
			"error: lola repair needs confirmation, and there is no terminal to ask on\n" +
			"  hint: lola repair --root " + proj + " -y\n"))
		r = lola("repair", "--root", proj, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("✓ forgot 2 entries in "+lp+"; no file was touched:\n"+listed),
			"nothing on a target lola does not know can be put back, so no sync is named")
		Expect(lola("list", "-C", proj).code).To(Equal(0))
		Expect(filepath.Join(proj, ".claude", "commands", "kit.md")).To(BeARegularFile())
	})

	It("on a terminal, asks about the list and forgets it", func() {
		plantTarget(proj)
		p := &confirming{answers: []bool{true}}
		r := lolaAsking(p, "repair", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.questions).To(Equal([]string{"Forget 2 entries lola could not have written? Files are not touched."}))
		Expect(p.before[0]).To(Equal("will forget 2 entries in " + lp + "; files are not touched:\n" + listed))
		Expect(lola("list", "-C", proj).code).To(Equal(0))
	})

	It("-n lists what it would forget and writes nothing", func() {
		plantTarget(proj)
		before, err := os.ReadFile(lp)
		Expect(err).NotTo(HaveOccurred())
		r := lola("repair", "-C", proj, "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("will forget 2 entries in " + lp + "; files are not touched:\n" + listed))
		Expect(os.ReadFile(lp)).To(Equal(before))
		r = lola("repair", "-C", proj, "-n", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("forget\tmodule kit: target \"bogus\"\nforget\tmodule kit: artifact \"x.md\" on target \"bogus\"\n"))
	})

	It("says there is nothing to repair, and exits 0 without asking or writing", func() {
		before, err := os.ReadFile(lp)
		Expect(err).NotTo(HaveOccurred())
		r := lola("repair", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(BeEmpty())
		Expect(r.out).To(Equal("✓ nothing to repair in " + lp + "\n"))
		Expect(os.ReadFile(lp)).To(Equal(before))
		r = lola("repair", "-C", proj, "--json")
		Expect(r.code).To(Equal(0), r.err)
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed(), r.out)
		checkJSON(doc, "")
		Expect(doc).To(Equal(map[string]any{"dry_run": false, "file": lp, "forgotten": []any{}, "forgotten_modules": []any{}, "notices": []any{}}))
	})

	It("keeps the refusal of a lock.json it cannot repair, writing nothing", func() {
		plantTarget(proj)
		editLock(proj, func(doc map[string]any) { doc["modules"].(map[string]any)["kit"].(map[string]any)["from"] = "nope" })
		before, err := os.ReadFile(lp)
		Expect(err).NotTo(HaveOccurred())
		r := lola("repair", "-C", proj, "-y")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: " + lp + `: module kit: from "nope" is not a profile lola records` +
			"\n  hint: restore lock.json from a backup, or remove the module's entry from it and reinstall the module\n"))
		Expect(os.ReadFile(lp)).To(Equal(before))
	})
})
