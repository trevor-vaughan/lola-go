package cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("template refusals (Plan 7 §11)", func() {
	var tmp, proj, mod string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, mod = filepath.Join(tmp, "proj"), filepath.Join(tmp, "src", "kit")
		writeTree(mod, map[string]string{"commands/ship.md.liquid": "Ship\n{{ targt }}\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		GinkgoT().Chdir(proj)
	})
	// refusal is the --json error document of r, a refused run.
	refusal := func(r result) (map[string]any, []string) {
		GinkgoHelper()
		Expect(r.code).To(Equal(1), r.out+r.err)
		var doc struct {
			Error    string         `json:"error"`
			Hints    []string       `json:"hints"`
			Template map[string]any `json:"template"`
		}
		lines := strings.Split(strings.TrimSpace(r.err), "\n") // the scope line comes first
		Expect(json.Unmarshal([]byte(lines[len(lines)-1]), &doc)).To(Succeed(), r.err)
		return doc.Template, doc.Hints
	}
	// works runs hint through the CLI: it exits 0, carries the run's flags, and its JSON report
	// shows the refusal as a finding.
	works := func(hint string, flags ...string) {
		GinkgoHelper()
		words := shellWords(hint)
		Expect(words[:2]).To(Equal([]string{"lola", "check"}))
		for _, f := range flags {
			Expect(words).To(ContainElement(f), hint)
		}
		r := lola(words[1:]...)
		Expect(r.code).To(Equal(0), r.out+r.err)
		Expect(r.out).To(ContainSubstring(`"code": "template-undefined-variable"`))
		Expect(r.out).To(ContainSubstring(`commands/ship.md.liquid:2: undefined variable in {{ targt }}`))
	}

	It("a template refusal under --json carries the template object, and the check hint works with the run's address and flags", func() {
		By("a path")
		tpl, hints := refusal(lola("install", "../src/kit", "-t", "claude-code", "--json", "--no-pager"))
		Expect(tpl).To(Equal(map[string]any{"module": "kit", "target": "claude-code", "scope": "project", "kind": "command", "artifact": "ship",
			"file": "commands/ship.md.liquid", "line": float64(2), "code": "template-undefined-variable", "reason": "undefined variable in {{ targt }}",
			"included_from": "", "rendered": false}))
		Expect(hints).To(Equal([]string{"lola check --project " + mod + " --json --no-pager"}))
		works(hints[0], "--json", "--no-pager")

		By("a git URL, pinned at install")
		work := filepath.Join(tmp, "work")
		writeTree(work, map[string]string{"lola.yml": "name: gkit\n", "commands/ship.md.liquid": "Ship\n{{ targt }}\n"})
		git := func(dir string, args ...string) {
			GinkgoHelper()
			out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), string(out))
		}
		git(work, "init", "-q", "-b", "main")
		git(work, "add", "-A")
		git(work, "commit", "-q", "-m", "c")
		Expect(os.MkdirAll(filepath.Join(tmp, "srv"), 0o755)).To(Succeed())
		git(tmp, "clone", "-q", "--bare", work, filepath.Join(tmp, "srv", "kit.git"))
		srv := testenv.ServeGit(filepath.Join(tmp, "srv"))
		_, hints = refusal(lola("install", srv+"/kit.git", "-t", "claude-code", "--json"))
		Expect(hints).To(Equal([]string{"lola check --project " + srv + "/kit.git --json"}))
		works(hints[0], "--json")
		_, hints = refusal(lola("install", srv+"/kit.git", "--ref", "main", "-t", "claude-code", "--json"))
		Expect(hints).To(BeEmpty(), "lola check takes no --ref, so no check reaches that version")

		By("a marketplace entry")
		market := filepath.Join(tmp, "market")
		writeTree(market, map[string]string{".claude-plugin/marketplace.json": `{"name": "acme", "owner": {"name": "Acme"}, "plugins": [{"name": "mk", "source": "./mk"}]}`,
			"mk/commands/ship.md.liquid": "Ship\n{{ targt }}\n"})
		git(market, "init", "-q", "-b", "main")
		git(market, "add", "-A")
		git(market, "commit", "-q", "-m", "c")
		git(tmp, "clone", "-q", "--bare", market, filepath.Join(tmp, "srv", "plugins.git"))
		r := lola("market", "acme", "--add", srv+"/plugins.git")
		Expect(r.code).To(Equal(0), r.out+r.err)
		_, hints = refusal(lola("install", "@acme/mk", "-t", "claude-code", "--json", "--no-pager"))
		Expect(hints).To(Equal([]string{"lola check --project @acme/mk --json --no-pager"}))
		works(hints[0], "--json", "--no-pager")

		By("a module a project sync has not trusted yet, refused while the trust diff renders it")
		kit2 := filepath.Join(proj, "mods", "kit2")
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - name: kit2\n    source: mods/kit2\n",
			"mods/kit2/commands/ship.md.liquid": "Ship\n{{ targt }}\n"})
		r = lola("sync", "--json", "--no-pager")
		_, hints = refusal(r)
		Expect(r.err).To(ContainSubstring(`"error":"kit2: claude-code command ship: commands/ship.md.liquid:2: undefined variable in {{ targt }}"`),
			"named once, and not as a runtime hook")
		Expect(hints).To(Equal([]string{"lola check --project " + kit2 + " --json --no-pager"}))
		works(hints[0], "--json", "--no-pager")
	})

	It("a template-run-budget refusal under --json has empty target, file and included_from and line 0", func() {
		megabyte := "{% assign l = '0123456789abcdef' | split: '' %}{% for a in l %}{% for b in l %}" + strings.Repeat("x", 4096) + "{% endfor %}{% endfor %}"
		files := map[string]string{}
		for i := range 33 { // 33 files of 1 MiB each, past the 32 MiB a pass holds
			files[fmt.Sprintf("commands/c%02d.md.liquid", i)] = megabyte
		}
		big := filepath.Join(tmp, "src", "big")
		writeTree(big, files)
		tpl, hints := refusal(lola("install", "../src/big", "-t", "claude-code", "--json"))
		Expect(tpl).To(Equal(map[string]any{"module": "big", "target": "", "scope": "project", "kind": "", "artifact": "", "file": "",
			"line": float64(0), "code": "template-run-budget", "reason": "rendering templates held more than 32 MiB of rendered templates; at most 32 MiB per run",
			"included_from": "", "rendered": false}))
		Expect(hints).To(BeEmpty())
	})
})
