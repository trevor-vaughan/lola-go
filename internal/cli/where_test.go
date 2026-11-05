package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("where an inherited edit goes", func() {
	var p paths.Paths
	var kit string
	BeforeEach(func() {
		tmp := testenv.Isolate()
		var err error
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		kit = filepath.Join(tmp, "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "skills/a/SKILL.md": "---\nname: a\ndescription: A\n---\nA\n",
			"skills/b/SKILL.md": "---\nname: b\ndescription: B\n---\nB\n"})
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + "}\n",
			"go.yml": "version: 1\n"})
		Expect(lola("sync", "--user", "-y").code).To(Equal(0))
		Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
	})

	It("reviews an override that takes every component as the entry it writes, never as a removal", func() {
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + ", skip: [skill/b]}\n"})
		Expect(lola("sync", "--user", "-y").code).To(Equal(0))
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("kit")},
			assistants: []func(cli.AssistantStep) ([]string, error){func(cli.AssistantStep) ([]string, error) { return []string{"claude-code"}, nil }},
			components: []func(cli.ComponentPage) ([]string, error){pick("skill/a", "skill/b")}, where: []int{1}, review: []string{cli.ReviewApply}}
		r := lolaP(sp, "install", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("  profile go:\n    kit: every component (no only: or skip:)\n"))
		Expect(r.err).NotTo(ContainSubstring("(removed)"))
		b, err := os.ReadFile(filepath.Join(p.UserProfiles, "go.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("name: kit"))
		Expect(string(b)).NotTo(ContainSubstring("skip:"))
	})

	It("asks on a terminal once per run, and the answer's file is written", func() {
		sp := &scripted{where: []int{1}}
		r := lolaP(sp, "install", "kit", "--user", "--skip", "skill/b", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ kit: skipped in profile go: skill/b\n"), "the picks are the entry's, not selections.yaml's")
		Expect(sp.whereSteps).To(Equal([]cli.WhereStep{{Title: "kit comes from default (base), loaded under every profile at user scope.",
			Options: []string{"Change it in default  (every profile sees it)", "Override it in profile go  (go gets its own entry, starting from default's)"}}}))
		b, err := os.ReadFile(filepath.Join(p.UserProfiles, "go.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("skip: [skill/b]"))
	})

	It("reviews the picks under the profile the answer writes, never selections.yaml", func() {
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("kit")},
			assistants: []func(cli.AssistantStep) ([]string, error){func(cli.AssistantStep) ([]string, error) { return []string{"claude-code"}, nil }},
			components: []func(cli.ComponentPage) ([]string, error){pick("skill/a")}, where: []int{1}, review: []string{cli.ReviewApply}}
		r := lolaP(sp, "install", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"modules", "components:kit", "where", "review"}))
		Expect(r.err).To(ContainSubstring("      kit: - skill/b, = 1 unchanged\n  profile go:\n    kit: skip [skill/b]\n"))
		Expect(r.err).NotTo(ContainSubstring("selections.yaml"))
	})

	It("never asks under --json, refusing with both flag forms that keep the run's flags", func() {
		sp := &scripted{}
		r := lolaP(sp, "install", "kit", "--user", "--skip", "skill/b", "--json")
		Expect(r.code).NotTo(Equal(0))
		Expect(sp.whereSteps).To(BeEmpty())
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.err), &doc)).To(Succeed(), r.err) // --json: one JSON line on stderr
		Expect(doc["hints"]).To(Equal([]any{"lola install kit --json --skip skill/b --user --profile base",
			"lola install kit --json --skip skill/b --user --override"}))
	})
})
