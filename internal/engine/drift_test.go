package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

var _ = Describe("drift", func() {
	var (
		tmp  string
		p    paths.Paths
		e    *engine.Engine
		user = engine.Scope{Kind: target.User}
		ctx  = context.Background()
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		e = engine.New(p, targets.Builtin())
		e.Getwd = func() (string, error) { return tmp, nil }
		dir := filepath.Join(tmp, "mods", "kit")
		writeTree(dir, map[string]string{"lola.yml": "name: kit\n",
			"skills/a/SKILL.md": "---\nname: a\ndescription: A\n---\nA\n", "skills/b/SKILL.md": "---\nname: b\ndescription: B\n---\nB\n"})
		_, err = e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: dir}}, engine.Options{Targets: []string{"claude-code"}})
		Expect(err).NotTo(HaveOccurred())
	})
	sync := func() *engine.SyncResult {
		GinkgoHelper()
		res, err := e.Sync(ctx, engine.SyncRequest{Scope: user})
		Expect(err).NotTo(HaveOccurred())
		return res
	}
	names := func() []string {
		GinkgoHelper()
		led, err := ledger.Load(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())
		var out []string
		for _, a := range led.Modules["kit"].Artifacts {
			if a.Name != "" {
				out = append(out, a.Name)
			}
		}
		return out
	}

	It("lock.json records the skill each written file belongs to, and the next sync plans nothing", func() {
		Expect(names()).To(ContainElements("a", "b"))
		res := sync()
		Expect(res.InSync).To(BeTrue())
		Expect(res.Steps).To(BeEmpty())
	})

	It("a lock.json from before names were recorded plans no step and is rewritten with them", func() {
		raw, err := os.ReadFile(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())
		var doc map[string]any
		Expect(json.Unmarshal(raw, &doc)).To(Succeed())
		for _, m := range doc["modules"].(map[string]any) {
			for _, a := range m.(map[string]any)["artifacts"].([]any) {
				delete(a.(map[string]any), "name")
			}
		}
		raw, err = json.Marshal(doc)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(p.UserLedger, raw, 0o600)).To(Succeed())
		Expect(names()).To(BeEmpty())

		Expect(sync().Steps).To(BeEmpty())
		Expect(names()).To(ContainElements("a", "b"))
	})

	Describe("Status", func() {
		install := func(name string, files map[string]string) {
			GinkgoHelper()
			dir := filepath.Join(tmp, "mods", name)
			writeTree(dir, files)
			_, err := e.Install(ctx, user, tmp, []engine.InstallRequest{{Arg: dir}}, engine.Options{Targets: []string{"claude-code"}})
			Expect(err).NotTo(HaveOccurred())
		}
		row := func(name string) engine.StatusRow {
			GinkgoHelper()
			rows, _, err := e.Status(ctx, user, nil)
			Expect(err).NotTo(HaveOccurred())
			for _, r := range rows {
				if r.Name == name {
					return r
				}
			}
			Fail("no status row for " + name)
			return engine.StatusRow{}
		}
		recorded := func(name string) []ledger.Artifact {
			GinkgoHelper()
			led, err := ledger.Load(p.UserLedger)
			Expect(err).NotTo(HaveOccurred())
			return led.Modules[name].Artifacts
		}
		claude := func(rel string) string { return filepath.Join(p.Home, ".claude", rel) }
		stripNames := func() {
			GinkgoHelper()
			raw, err := os.ReadFile(p.UserLedger)
			Expect(err).NotTo(HaveOccurred())
			var doc map[string]any
			Expect(json.Unmarshal(raw, &doc)).To(Succeed())
			for _, m := range doc["modules"].(map[string]any) {
				for _, a := range m.(map[string]any)["artifacts"].([]any) {
					delete(a.(map[string]any), "name")
				}
			}
			raw, err = json.Marshal(doc)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(p.UserLedger, raw, 0o600)).To(Succeed())
		}

		It("an intact module has an empty, non-nil drift that marshals as []", func() {
			r := row("kit")
			Expect(r.Status).To(Equal("ok"))
			Expect(r.Drift).NotTo(BeNil())
			Expect(r.Drift).To(BeEmpty())
			Expect(r.Recorded).To(Equal(len(recorded("kit"))))
			raw, err := json.Marshal(r)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(raw)).To(ContainSubstring(`"drift":[]`))
			Expect(string(raw)).To(ContainSubstring(`"recorded":`))
		})

		It("a deleted multi-file skill is one missing entry counting its files", func() {
			install("big", map[string]string{"lola.yml": "name: big\n",
				"skills/big/SKILL.md": "---\nname: big\ndescription: B\n---\nB\n", "skills/big/ref/one.md": "1", "skills/big/ref/two.md": "2"})
			Expect(os.RemoveAll(claude("skills/big"))).To(Succeed())
			r := row("big")
			Expect(r.Status).To(Equal("missing"))
			Expect(r.Drift).To(HaveLen(1))
			Expect(r.Drift[0]).To(MatchFields(IgnoreExtras, Fields{"Kind": Equal("skill"), "Name": Equal("big"), "Status": Equal("missing"), "Files": Equal(3)}))
			Expect(r.Recorded).To(Equal(len(recorded("big"))))
		})

		It("a modified skill and a deleted one are two entries, missing first", func() {
			Expect(os.WriteFile(claude("skills/a/SKILL.md"), []byte("changed\n"), 0o644)).To(Succeed())
			Expect(os.RemoveAll(claude("skills/b"))).To(Succeed())
			r := row("kit")
			Expect(r.Status).To(Equal("missing"))
			Expect(r.Drift).To(HaveLen(2))
			Expect(r.Drift[0]).To(MatchFields(IgnoreExtras, Fields{"Name": Equal("b"), "Status": Equal("missing")}))
			Expect(r.Drift[1]).To(MatchFields(IgnoreExtras, Fields{"Name": Equal("a"), "Status": Equal("modified")}))
		})

		It("a component with one deleted and one modified file yields one entry per status", func() {
			install("pair", map[string]string{"lola.yml": "name: pair\n",
				"skills/pair/SKILL.md": "---\nname: pair\ndescription: P\n---\nP\n", "skills/pair/ref.md": "r"})
			Expect(os.Remove(claude("skills/pair/ref.md"))).To(Succeed())
			Expect(os.WriteFile(claude("skills/pair/SKILL.md"), []byte("changed\n"), 0o644)).To(Succeed())
			r := row("pair")
			Expect(r.Status).To(Equal("missing"))
			Expect(r.Drift).To(HaveLen(2))
			Expect(r.Drift[0]).To(MatchFields(IgnoreExtras, Fields{"Name": Equal("pair"), "Status": Equal("missing"), "Files": Equal(1)}))
			Expect(r.Drift[1]).To(MatchFields(IgnoreExtras, Fields{"Name": Equal("pair"), "Status": Equal("modified"), "Files": Equal(1)}))
		})

		It("a deleted module copy is a copy entry at the copy's directory, with the artifacts untouched", func() {
			cp := filepath.Join(p.UserModules, "kit")
			Expect(os.RemoveAll(cp)).To(Succeed())
			r := row("kit")
			Expect(r.Status).To(Equal("missing"))
			Expect(r.Drift).To(HaveLen(1))
			Expect(r.Drift[0]).To(MatchFields(IgnoreExtras, Fields{"Kind": Equal("copy"), "Path": Equal(cp), "Status": Equal("missing"), "Files": Equal(1)}))
		})

		It("a ledger without names yields path-based entries", func() {
			stripNames()
			Expect(os.RemoveAll(claude("skills/a"))).To(Succeed())
			r := row("kit")
			Expect(r.Drift).To(HaveLen(1))
			Expect(r.Drift[0].Name).To(BeEmpty())
			var a ledger.Artifact
			for _, art := range recorded("kit") {
				if strings.Contains(art.Path, "skills/a/") {
					a = art
				}
			}
			Expect(a.Path).NotTo(BeEmpty())
			Expect(r.Drift[0].Path).To(Equal(a.Path))
		})

		It("a registration removed from its JSON file carries its key", func() {
			install("tools", map[string]string{"lola.yml": "name: tools\n", "mcps.json": `{"mcpServers":{"gh":{"command":"gh"}}}`})
			var keyed ledger.Artifact
			for _, a := range recorded("tools") {
				if a.Key != "" {
					keyed = a
				}
			}
			Expect(keyed.Key).NotTo(BeEmpty())
			Expect(os.Remove(filepath.Join(p.Home, keyed.Path))).To(Succeed())
			r := row("tools")
			Expect(r.Status).To(Equal("missing"))
			Expect(r.Drift).To(ContainElement(MatchFields(IgnoreExtras, Fields{"Kind": Equal(keyed.Kind), "Key": Equal(keyed.Key), "Status": Equal("missing")})))
		})

		It("a control character in a component name does not reach the entry", func() {
			stripNames()
			raw, err := os.ReadFile(p.UserLedger)
			Expect(err).NotTo(HaveOccurred())
			Expect(json.Unmarshal(raw, new(any))).To(Succeed())
			bad := strings.Replace(string(raw), `"kind":"skill"`, `"kind":"sk\u001b[31mill","name":"ev\u001b[31mil"`, 1)
			Expect(os.WriteFile(p.UserLedger, []byte(bad), 0o600)).To(Succeed())
			Expect(os.RemoveAll(claude("skills/a"))).To(Succeed())
			Expect(os.RemoveAll(claude("skills/b"))).To(Succeed())
			r := row("kit")
			Expect(r.Drift).NotTo(BeEmpty())
			for _, d := range r.Drift {
				Expect(untrusted.IsPlain(d.Name)).To(BeTrue(), "%q", d.Name)
				Expect(untrusted.IsPlain(d.Kind)).To(BeTrue(), "%q", d.Kind)
			}
		})
	})
})
