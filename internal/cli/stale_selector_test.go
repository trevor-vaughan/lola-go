package cli_test

import (
	"os"
	"path/filepath"
	"regexp"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("the hint of a stale selector removes it, wherever it is saved", func() {
	var (
		tmp, proj, kit string
		p              paths.Paths
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		proj, kit = filepath.Join(tmp, "proj"), filepath.Join(tmp, "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "skills/a/SKILL.md": "---\ndescription: A\n---\nA\n"})
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\n"})
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		Expect(lola("sync", "-C", proj, "--trust=entries", "-y").code).To(Equal(0))
		Expect(lola("install", kit, "-C", proj, "-t", "claude-code", "-y").code).To(Equal(0))
	})
	selections := func(body string) {
		GinkgoHelper()
		writeTree(filepath.Dir(p.Selections), map[string]string{"selections.yaml": "version: 1\n" + body})
	}
	staleLine := regexp.MustCompile(`(?m)^.*has no component skill/gone → (lola .*)$`)
	// hintOf is the hint of the one stale-selector warning in r's output.
	hintOf := func(r result) string {
		GinkgoHelper()
		m := staleLine.FindAllStringSubmatch(r.out+r.err, -1)
		Expect(m).To(HaveLen(1), r.out+r.err)
		return m[0][1]
	}
	// removes runs the warning's hint and expects the selector gone and the next sync silent.
	removes := func(r result) {
		GinkgoHelper()
		hint := hintOf(r)
		got := runHint(hint)
		Expect(got.code).To(Equal(0), hint+": "+got.err)
		Expect(os.ReadFile(p.Selections)).NotTo(ContainSubstring("skill/gone"), hint)
		Expect(lola("sync", "-C", proj, "--trust=entries", "-y").out).NotTo(ContainSubstring("has no component"), hint)
	}
	syncs := func() result {
		GinkgoHelper()
		r := lola("sync", "-C", proj, "--trust=entries", "-y")
		Expect(r.code).To(Equal(0), r.err)
		return r
	}

	It("in the project section", func() {
		selections("projects:\n  " + proj + ":\n    kit:\n      skip: [skill/gone]\n")
		removes(syncs())
	})

	It("in the global section", func() {
		selections("global:\n  kit:\n    skip: [skill/gone]\n")
		removes(syncs())
	})

	It("in the global section, from an install", func() {
		selections("global:\n  kit:\n    skip: [skill/gone]\n")
		r := lola("install", kit, "-C", proj, "-t", "claude-code", "-y")
		Expect(r.code).To(Equal(0), r.err)
		removes(r)
	})

	It("in the project section, from an install", func() {
		selections("projects:\n  " + proj + ":\n    kit:\n      skip: [skill/gone]\n")
		r := lola("install", kit, "-C", proj, "-t", "claude-code", "-y")
		Expect(r.code).To(Equal(0), r.err)
		removes(r)
	})

	It("in the entry of the profile that provides the module", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\nmodules:\n  - {name: kit, source: " + kit + ", skip: [skill/gone]}\n"})
		Expect(lola("profile", "user:go", "--switch", "-C", proj, "-y").code).To(Equal(0))
		r := syncs()
		hint := hintOf(r)
		got := runHint(hint)
		Expect(got.code).To(Equal(0), hint+": "+got.err)
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "go.yml"))).NotTo(ContainSubstring("skill/gone"), hint)
		Expect(syncs().out).NotTo(ContainSubstring("has no component"))
	})

	It("in the project section while a profile is on and project.yml provides the module", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		Expect(lola("profile", "user:go", "--switch", "-C", proj, "-y").code).To(Equal(0))
		selections("projects:\n  " + proj + ":\n    kit:\n      skip: [skill/gone]\n")
		removes(syncs())
	})

	It("from an install, in the entry of the profile that provides the module", func() {
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\nmodules:\n  - {name: kit, source: " + kit + ", skip: [skill/gone]}\n"})
		Expect(lola("profile", "user:go", "--switch", "-C", proj, "-y").code).To(Equal(0))
		r := lola("install", kit, "-C", proj, "-t", "claude-code", "-y")
		Expect(r.code).To(Equal(0), r.err)
		hint := hintOf(r)
		got := runHint(hint)
		Expect(got.code).To(Equal(0), hint+": "+got.err)
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "go.yml"))).NotTo(ContainSubstring("skill/gone"), hint)
	})

	It("whatever list holds it: include and only:, global and project", func() {
		for _, body := range []string{
			"global:\n  kit:\n    only: [skill/a, skill/gone]\n",
			"projects:\n  " + proj + ":\n    kit:\n      only: [skill/a, skill/gone]\n",
			"projects:\n  " + proj + ":\n    kit:\n      include: [skill/gone]\n",
		} {
			selections(body)
			removes(syncs())
		}
	})
})
