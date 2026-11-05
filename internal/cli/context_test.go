package cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("the context line", func() {
	var tmp, proj, kit string
	var p paths.Paths
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		proj = filepath.Join(tmp, "proj")
		kit = filepath.Join(proj, "mods", "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "commands/kit.md": "Kit\n"})
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n", ".claude/.keep": ""})
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
	})

	// The scope commands are listed, not found by walking the cobra tree: the scope flags live on
	// the root, so no command's own flags tell a scope command apart.
	DescribeTable("every scope command prints it on stderr, first and once, after resolving the scope",
		func(args []string) {
			r := lola(append(args, "-C", proj)...)
			Expect(r.err).To(HavePrefix(projectContext(proj)), "%v", args)
			Expect(strings.Count(r.err, "project "+proj+": ")).To(Equal(1), "%v", args)
		},
		Entry("list", []string{"list"}),
		Entry("sync", []string{"sync", "-n"}),
		Entry("update", []string{"update", "-n"}),
		Entry("uninstall", []string{"uninstall", "kit", "-y"}),
		Entry("tidy", []string{"tidy", "-n"}),
		Entry("repair", []string{"repair", "-n"}),
		Entry("profile", []string{"profile"}),
		Entry("profile <name>", []string{"profile", "go"}),
		Entry("profile --on", []string{"profile", "go", "--on", "-n"}),
		Entry("profile --switch", []string{"profile", "go", "--switch", "-n"}),
		Entry("profile --off", []string{"profile", "--off"}),
	)

	It("lola info prints it for the scope it plans, lola check once it read the scope's entries", func() {
		r := lola("info", kit, "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(HavePrefix(projectContext(proj)))
		r = lola("check", kit, "-C", proj)
		Expect(r.err).NotTo(ContainSubstring("project "+proj+": "), "no entry of the scope was read")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: mods/kit}\n"})
		r = lola("check", kit, "-C", proj)
		Expect(r.err).To(HavePrefix(projectContext(proj)), "kit renders with its entry here")
	})

	It("prints a line per scope, naming the profile on", func() {
		Expect(lola("profile", "go", "--switch", "--user").code).To(Equal(0))
		r := lola("list", "--scope", "both", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(HavePrefix(projectContext(proj) + "user scope: profile go over default\n"))
	})

	It("names a bundle on and its members, grouping a nested bundle's and cutting the list at six", func() {
		writeTree(p.UserProfiles, map[string]string{"docs.yml": "version: 1\n", "go-lint.yml": "version: 1\n",
			"go-all.yml": "version: 1\nbundle: [go, go-lint]\n", "work.yml": "version: 1\nbundle: [go, docs]\n"})
		Expect(lola("profile", "work", "--switch", "--user").code).To(Equal(0))
		r := lola("list", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(HavePrefix("user scope: bundle work (go, docs) over default\n"))

		writeTree(p.UserProfiles, map[string]string{"work.yml": "version: 1\nbundle: [go-all, docs]\n"})
		Expect(lola("list", "--user").err).To(HavePrefix("user scope: bundle work (go-all › go, go-lint; docs) over default\n"))

		var many []string
		for i := range 8 {
			name := fmt.Sprintf("p%d", i)
			many = append(many, name)
			writeTree(p.UserProfiles, map[string]string{name + ".yml": "version: 1\n"})
		}
		writeTree(p.UserProfiles, map[string]string{"work.yml": "version: 1\nbundle: [" + strings.Join(many, ", ") + "]\n"})
		Expect(lola("list", "--user").err).To(HavePrefix("user scope: bundle work (p0, p1, p2, p3, p4, p5, and 2 more) over default\n"))

		r = lola("list", "--user", "--json")
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed(), r.out)
		profileDoc := doc["context"].([]any)[0].(map[string]any)["profile"].(map[string]any)
		Expect(profileDoc).To(HaveKeyWithValue("bundle", true))
		Expect(profileDoc["members"]).To(HaveLen(8), "JSON lists every member")
		Expect(profileDoc["members"].([]any)[0]).To(Equal(map[string]any{"name": "user:p0", "kind": "user", "path": filepath.Join(p.UserProfiles, "p0.yml")}))
	})

	It("shows a bundle's members as it expands them, and the module each wins with", func() {
		writeTree(p.UserProfiles, map[string]string{"docs.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + "}\n",
			"work.yml": "version: 1\nbundle: [go, docs]\n"})
		r := lola("profile", "work", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("Profile user:work (" + filepath.Join(p.UserProfiles, "work.yml") + ")\nbundle of user:go, user:docs\n"))
		Expect(r.out).To(ContainSubstring("  kit  profile:user:docs\n"))
		r = lola("profile", "work", "--user", "--json")
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed(), r.out)
		Expect(doc["profiles"].([]any)[0]).To(HaveKeyWithValue("members", []any{"user:go", "user:docs"}))
	})

	It("lists a pinned remote member this machine lacks once, where its bundle names it, and says how to restore it", func() {
		writeTree(p.UserProfiles, map[string]string{"go-lint.yml": "version: 1\n",
			"go-all.yml": "version: 1\nbundle: [go-lint, remote:x]\n", "work.yml": "version: 1\nbundle: [go, go-all, remote:x]\n"})
		led, err := ledger.Load(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())
		led.Profile = &ledger.Active{Name: "user:work", By: ledger.ByOn}
		Expect(os.MkdirAll(filepath.Dir(p.UserLedger), 0o700)).To(Succeed())
		Expect(led.Save(p.UserLedger)).To(Succeed())
		pins, err := config.LoadPins(p.Pins)
		Expect(err).NotTo(HaveOccurred())
		out, err := pins.Bytes(nil, map[string]config.Remote{"x": {Source: "https://example.com/profiles.git",
			Commit: strings.Repeat("a", 40), Digest: "sha256:" + strings.Repeat("b", 64)}})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.MkdirAll(filepath.Dir(p.Pins), 0o700)).To(Succeed())
		Expect(os.WriteFile(p.Pins, out, 0o600)).To(Succeed())

		r := lola("list", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(HavePrefix("user scope: bundle work (go; go-all › go-lint, remote:x) (remote:x not on this machine yet → lola sync --user) over default\n"))

		r = lola("list", "--user", "--json")
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed(), r.out)
		c := doc["context"].([]any)[0].(map[string]any)
		Expect(c["restore"]).To(Equal("lola sync --user"))
		Expect(c["writes_to"]).To(BeNil())
		var names []any
		for _, m := range c["profile"].(map[string]any)["members"].([]any) {
			names = append(names, m.(map[string]any)["name"])
		}
		Expect(names).To(Equal([]any{"user:go", "user:go-lint", "remote:x"}), "remote:x once, in the place its bundle names it")

		r = lola("profile", "work", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("Profile user:work (" + filepath.Join(p.UserProfiles, "work.yml") + ")\nbundle of user:go, user:go-lint, remote:x\n"))
		r = lola("profile", "work", "--user", "--json")
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed(), r.out)
		Expect(doc["profiles"].([]any)[0]).To(HaveKeyWithValue("members", []any{"user:go", "user:go-lint", "remote:x"}))

		writeTree(p.UserProfiles, map[string]string{"work.yml": "version: 1\nbundle: [remote:x]\n"})
		Expect(lola("list", "--user").err).To(HavePrefix("user scope: bundle work (remote:x) (remote:x not on this machine yet → lola sync --user) over default\n"))
		r = lola("profile", "work", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HavePrefix("Profile user:work (" + filepath.Join(p.UserProfiles, "work.yml") + ")\nbundle of remote:x\n"))
		r = lola("profile", "work", "--user", "--json")
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed(), r.out)
		Expect(doc["profiles"].([]any)[0]).To(HaveKeyWithValue("members", []any{"remote:x"}))
	})

	It("names a profile on whose file is gone, and the command still runs", func() {
		Expect(lola("profile", "go", "--switch", "--user").code).To(Equal(0))
		Expect(os.Remove(filepath.Join(p.UserProfiles, "go.yml"))).To(Succeed())
		r := lola("list", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(HavePrefix("user scope: profile user:go (unreadable: profile user:go no longer exists) over default\n"))
	})

	It("names a remote profile on that this machine lacks and pins.yaml records, and how to restore it", func() {
		Expect(os.Remove(filepath.Join(p.UserProfiles, "go.yml"))).To(Succeed()) // yours would shadow remote:go
		led, err := ledger.Load(p.UserLedger)
		Expect(err).NotTo(HaveOccurred())
		led.Profile = &ledger.Active{Name: "remote:go", By: ledger.ByOn}
		Expect(os.MkdirAll(filepath.Dir(p.UserLedger), 0o700)).To(Succeed())
		Expect(led.Save(p.UserLedger)).To(Succeed())
		pins, err := config.LoadPins(p.Pins)
		Expect(err).NotTo(HaveOccurred())
		out, err := pins.Bytes(nil, map[string]config.Remote{"go": {Source: "https://example.com/profiles.git",
			Commit: strings.Repeat("a", 40), Digest: "sha256:" + strings.Repeat("b", 64)}})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.MkdirAll(filepath.Dir(p.Pins), 0o700)).To(Succeed())
		Expect(os.WriteFile(p.Pins, out, 0o600)).To(Succeed())
		Expect(filepath.Join(p.RemoteProfiles, "go")).NotTo(BeADirectory())

		r := lola("list", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(HavePrefix("user scope: profile remote:go (not on this machine yet → lola sync --user) over default\n"))
		Expect(r.out).To(ContainSubstring("⚠ profile remote:go is on but not on this machine yet → lola sync --user restores it from pins.yaml\n"))
		Expect(r.out).NotTo(ContainSubstring("no longer exists"))
		r = lola("list", "--user", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var doc struct {
			Context []map[string]any `json:"context"`
		}
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		Expect(doc.Context).To(ConsistOf(HaveKeyWithValue("restore", "lola sync --user")))
		Expect(doc.Context[0]).NotTo(HaveKey("error"))

		// Something in its way: no run restores it until it is moved aside, and the output says so.
		stray := filepath.Join(p.RemoteProfiles, "go")
		Expect(os.MkdirAll(p.RemoteProfiles, 0o700)).To(Succeed())
		Expect(os.WriteFile(stray, []byte("x\n"), 0o600)).To(Succeed())
		r = lola("list", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(HavePrefix("user scope: profile remote:go (unreadable: " + stray + " is in the way of it; move it aside) over default\n"))
		Expect(r.out).To(ContainSubstring("⚠ profile remote:go is on but " + stray + " is in the way of it → move it aside, then lola sync --user restores it from pins.yaml\n"))
		Expect(r.out).NotTo(ContainSubstring("not on this machine yet"))
	})

	It("names a profile on whose file does not load, and tidy and repair still run", func() {
		Expect(lola("profile", "go", "--switch", "--user").code).To(Equal(0))
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules: []\n"})
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: [\n"})
		line := "user scope: profile go (unreadable: " + filepath.Join(p.UserProfiles, "go.yml") +
			": does not parse: yaml: line 1: did not find expected node content) over default\n"
		for _, args := range [][]string{{"tidy", "-n", "--user"}, {"repair", "-n", "--user"}} {
			r := lola(args...)
			Expect(r.code).To(Equal(0), "%v: %s", args, r.err)
			Expect(r.err).To(HavePrefix(line), "%v", args)
		}
	})

	It("adds context first to the JSON document, and to a refusal's", func() {
		r := lola("list", "-C", proj, "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(BeEmpty(), "no line under --json")
		Expect(r.out).To(HavePrefix("{\n  \"context\": ["))
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		checkJSON(doc, "")
		Expect(doc["context"]).To(Equal([]any{projectContextJSON(proj)}))

		r = lola("uninstall", "kit", "-C", proj, "-y", "--json")
		Expect(r.code).To(Equal(1))
		var refusal map[string]any
		Expect(json.Unmarshal([]byte(r.err), &refusal)).To(Succeed(), r.err)
		Expect(refusal).To(HaveKey("error"))
		Expect(refusal["context"]).To(ConsistOf(HaveKeyWithValue("writes_to",
			map[string]any{"name": "project.yml", "kind": "base", "path": filepath.Join(proj, ".lola", "project.yml")})))

		r = lola("list", "--json", "--scope", "nope")
		var early map[string]any
		Expect(json.Unmarshal([]byte(r.err), &early)).To(Succeed(), r.err)
		Expect(early).NotTo(HaveKey("context"), "refused before any scope was resolved")
	})

	It("names the profile on in JSON, members [] for a profile and no error when it loads", func() {
		Expect(lola("profile", "go", "--switch", "--user").code).To(Equal(0))
		r := lola("list", "--user", "--json")
		Expect(r.code).To(Equal(0), r.err)
		var doc map[string]any
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		checkJSON(doc, "")
		Expect(doc["context"]).To(Equal([]any{map[string]any{"scope": "user", "root": "", "writes_to": nil,
			"base": map[string]any{"name": "default", "kind": "base", "path": filepath.Join(p.UserProfiles, "default.yml")},
			"profile": map[string]any{"name": "user:go", "kind": "user", "path": filepath.Join(p.UserProfiles, "go.yml"),
				"bundle": false, "members": []any{}}}}))
	})

	It("prints a context row per scope on stderr under --plain, so stdout keeps the command's rows", func() {
		writeTree(p.UserProfiles, map[string]string{"docs.yml": "version: 1\n", "work.yml": "version: 1\nbundle: [go, docs]\n"})
		Expect(lola("profile", "work", "--switch", "--user").code).To(Equal(0))
		named := filepath.Join(tmp, "context") // a module named context
		writeTree(named, map[string]string{"lola.yml": "name: context\n", "commands/c.md": "C\n"})
		Expect(lola("install", named, "-C", proj, "-t", "claude-code", "-y").code).To(Equal(0))
		r := lola("list", "--scope", "both", "-C", proj, "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(Equal("context\tproject\t" + proj + "\tproject.yml\t-\t\n" +
			"context\tuser\t\tdefault\tuser:work\tuser:go,user:docs\n"))
		Expect(r.out).To(HavePrefix("context\tclaude-code\tok\t"), "the module named context is stdout's row, and the only one there")
		Expect(strings.Count("\n"+r.out, "\ncontext\t")).To(Equal(1))
		r = lola("list", "--user", "--json")
		Expect(r.err).To(BeEmpty())
	})

	It("prints unknown in the --plain row's profile column when lola cannot read the scope's state, as the line says (profile unknown)", func() {
		writeTree(proj, map[string]string{".lola/lock.json": "{not json"})
		r := lola("list", "-C", proj, "--plain")
		Expect(r.err).To(HavePrefix("context\tproject\t" + proj + "\tproject.yml\tunknown\t\n"))
		r = lola("list", "-C", proj)
		Expect(r.err).To(HavePrefix("project " + proj + ": project.yml (profile unknown)\n"))
	})

	It("names each row's layer in lola list's FROM as the context line does; --plain and --json keep lock.json's from", func() {
		notes := filepath.Join(tmp, "notes")
		writeTree(notes, map[string]string{"lola.yml": "name: notes\n", "commands/notes.md": "N\n"})
		writeTree(p.UserProfiles, map[string]string{
			"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: kit, source: " + kit + "}\n",
			"docs.yml":    "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + notes + "}\n",
			"work.yml":    "version: 1\nbundle: [go, docs]\n"})
		Expect(lola("profile", "work", "--switch", "--user", "-y").code).To(Equal(0))
		r := lola("list", "--user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`user\s+kit\s.*\s` + regexpQuote(kit) + `\s+default\n`))
		Expect(r.out).To(MatchRegexp(`user\s+notes\s.*\s` + regexpQuote(notes) + `\s+docs \(bundle work\)\n`))
		r = lola("list", "--user", "--plain")
		Expect(r.out).To(ContainSubstring("\tuser\tprofile:default\t\n"))
		Expect(r.out).To(ContainSubstring("\tuser\tprofile:user:docs\t\n"))

		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: notes, source: " + notes + "}\n"})
		Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
		Expect(lola("list", "--user").out).To(MatchRegexp(`user\s+notes\s.*\s` + regexpQuote(notes) + `\s+profile go\n`))
	})
})
