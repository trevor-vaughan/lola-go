package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("install scripts", func() {
	var tmp, proj, mod, cfg string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, mod, cfg = filepath.Join(tmp, "proj"), filepath.Join(tmp, "example"), filepath.Join(tmp, "config", "lola", "config.yaml")
		writeTree(mod, map[string]string{"commands/ship.md": "Ship $ARGUMENTS\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
	})

	It("lola list works with a missing configured script", func() {
		r := lola("install", mod, "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(os.MkdirAll(filepath.Dir(cfg), 0o700)).To(Succeed())
		Expect(os.WriteFile(cfg, []byte("version: 1\npre_install: "+filepath.Join(tmp, "gone.sh")+"\n"), 0o600)).To(Succeed())
		yml := filepath.Join(proj, ".lola", "project.yml")
		b, err := os.ReadFile(yml)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(yml, append(b, "post_install: ci/gone.sh\n"...), 0o644)).To(Succeed())

		r = lola("list", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`example\s+claude-code\s+✓ ok`))
		configured, err := os.ReadFile(cfg)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(cfg, append(configured, "default_scope: user\n"...), 0o600)).To(Succeed())
		r = lola("list") // config.yaml is read for its default_scope without a scope flag
		Expect(r.code).To(Equal(0), r.err)
	})
})

var _ = Describe("install scripts off a terminal", func() {
	It("an untrusted project pre script refuses off a terminal with --trust=scripts; post is declined and pending", func() {
		tmp, err := filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, mod := filepath.Join(tmp, "proj"), filepath.Join(tmp, "example")
		writeTree(mod, map[string]string{"commands/ship.md": "Ship $ARGUMENTS\n"})
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\npre_install: ci/check.sh\n", "ci/check.sh": "#!/bin/sh\nexit 0\n"})
		r := lola("install", mod, "-C", proj, "-t", "claude-code", "--json")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal(`{"error":"lola install needs your trust for the pre-install script ci/check.sh of this project (new), and there is no terminal to ask on",` +
			`"hints":["review it: lola info -C ` + proj + ` ` + mod + ` --target claude-code --json",` +
			`"then consent to it: lola install ` + mod + ` --json --root ` + proj + ` --target claude-code --trust=scripts"]}` + "\n"))

		r = lola("install", mod, "-C", proj, "-t", "claude-code", "--json", "--trust=scripts")
		Expect(r.code).To(Equal(0), r.err)
		var doc struct {
			Trusted []map[string]string `json:"trusted"`
			Scripts []map[string]string `json:"scripts"`
			Notices []map[string]string `json:"notices"`
		}
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		Expect(doc.Trusted).To(Equal([]map[string]string{{"kind": "scripts", "item": "pre-install ci/check.sh", "change": "new"}}))
		Expect(doc.Scripts).To(Equal([]map[string]string{{"event": "pre-install", "source": "project", "path": "ci/check.sh", "module": "example",
			"target": "claude-code", "status": "ran"}}))

		yml := filepath.Join(proj, ".lola", "project.yml")
		b, err := os.ReadFile(yml)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(yml, []byte(strings.Replace(string(b), "pre_install: ci/check.sh", "post_install: ci/after.sh", 1)), 0o644)).To(Succeed())
		writeTree(proj, map[string]string{"ci/after.sh": "#!/bin/sh\nexit 0\n"})
		Expect(os.RemoveAll(filepath.Join(proj, ".lola", "modules", "example"))).To(Succeed())
		r = lola("render", "-C", proj, "--json")
		Expect(r.code).To(Equal(0), r.err)
		doc.Scripts = nil
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed())
		Expect(doc.Scripts).To(Equal([]map[string]string{{"event": "post-install", "source": "project", "path": "ci/after.sh", "module": "example",
			"target": "claude-code", "status": "declined"}}))
		Expect(doc.Notices).To(ContainElement(map[string]string{"level": "warn", "text": "example: post-install script ci/after.sh (project) skipped without " +
			"your trust; review and run it: lola sync -C " + proj + " example, or trust it without asking: lola sync -C " + proj + " example --trust=scripts"}))
		b, err = os.ReadFile(filepath.Join(proj, ".lola", "lock.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring(`"scripts_pending": true`))
	})
})

var _ = Describe("install scripts in a dry run", func() {
	var tmp, proj, mod string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, mod = filepath.Join(tmp, "proj"), filepath.Join(tmp, "example")
		writeTree(mod, map[string]string{"commands/ship.md": "Ship $ARGUMENTS\n"})
		Expect(os.MkdirAll(proj, 0o755)).To(Succeed())
	})
	// scriptsOf is the scripts array of a JSON document.
	scriptsOf := func(out string) []map[string]string {
		var doc struct {
			Scripts []map[string]string `json:"scripts"`
		}
		Expect(json.Unmarshal([]byte(out), &doc)).To(Succeed())
		return doc.Scripts
	}

	It("-n lists every script with source and trust; JSON scripts", func() {
		writeTree(tmp, map[string]string{"bin/pre.sh": "#!/bin/sh\n", "flag/post.sh": "#!/bin/sh\n", "proj/ci/check.sh": "#!/bin/sh\n",
			"proj/.lola/project.yml": "version: 1\npre_install: ci/check.sh\n", "config/lola/config.yaml": "version: 1\npre_install: " + filepath.Join(tmp, "bin", "pre.sh") + "\n"})
		args := []string{"install", mod, "-C", proj, "-t", "claude-code", "-n", "--post-install", filepath.Join(tmp, "flag", "post.sh")}
		r := lola(args...)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ example pre-install script " + filepath.Join(tmp, "bin", "pre.sh") + " (user, claude-code): would run\n" +
			"→ example pre-install script ci/check.sh (project at " + proj + ", claude-code): would run, needs --trust=scripts\n" +
			"→ example post-install script " + filepath.Join(tmp, "flag", "post.sh") + " (flag, claude-code): would run\n"))
		r = lola(append(args, "--plain")...)
		Expect(r.out).To(ContainSubstring("script\texample\tpre-install\tproject\tci/check.sh\tclaude-code\twould-run\n"))
		r = lola(append(args, "--json")...)
		Expect(r.code).To(Equal(0), r.err)
		Expect(scriptsOf(r.out)).To(Equal([]map[string]string{
			{"event": "pre-install", "source": "user", "path": filepath.Join(tmp, "bin", "pre.sh"), "module": "example", "target": "claude-code", "status": "would-run"},
			{"event": "pre-install", "source": "project", "path": "ci/check.sh", "module": "example", "target": "claude-code", "status": "would-run"},
			{"event": "post-install", "source": "flag", "path": filepath.Join(tmp, "flag", "post.sh"), "module": "example", "target": "claude-code", "status": "would-run"},
		}))
		r = lola(append(args, "--trust=scripts")...)
		Expect(r.out).To(ContainSubstring("→ example pre-install script ci/check.sh (project at "+proj+", claude-code): would run\n"), "covered, it needs nothing more")
		Expect(filepath.Join(proj, ".claude")).NotTo(BeADirectory())
	})

	It("a relative --pre-install names a script in the directory lola runs in, never the project's, shown as given", func() {
		writeTree(proj, map[string]string{"hooks/pre.sh": "#!/bin/sh\n"}) // -C names the project, not where lola runs
		r := lola("install", mod, "-C", proj, "-t", "claude-code", "--pre-install", "./hooks/pre.sh", "--json")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal(`{"error":"--pre-install ./hooks/pre.sh does not exist","hints":[]}` + "\n"))
	})

	It("info's --plain script row says whether the script needs trust", func() {
		writeTree(proj, map[string]string{"ci/check.sh": "#!/bin/sh\n", ".lola/project.yml": "version: 1\npre_install: ci/check.sh\n"})
		r := lola("info", mod, "-C", proj, "-t", "claude-code", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp("(?m)^script\tpre-install\tproject\tci/check.sh\t[0-9a-f]{64}\ttrue$"))
		Expect(lola("install", mod, "-C", proj, "-t", "claude-code", "--trust=scripts").code).To(Equal(0))
		r = lola("info", mod, "-C", proj, "-t", "claude-code", "--plain")
		Expect(r.out).To(MatchRegexp("(?m)^script\tpre-install\tproject\tci/check.sh\t[0-9a-f]{64}\tfalse$"), "its consent is recorded")
	})

	It("info's human script line names the project's root, and says when the script needs your trust", func() {
		writeTree(proj, map[string]string{"ci/check.sh": "#!/bin/sh\n", ".lola/project.yml": "version: 1\npre_install: ci/check.sh\n"})
		r := lola("info", mod, "-C", proj, "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(MatchRegexp(`(?m)^  pre-install script ci/check\.sh \(project at ` + regexp.QuoteMeta(proj) + `, sha256 [0-9a-f]{64}\), around its install with your trust$`))
		Expect(lola("install", mod, "-C", proj, "-t", "claude-code", "--trust=scripts").code).To(Equal(0))
		r = lola("info", mod, "-C", proj, "-t", "claude-code")
		Expect(r.out).To(MatchRegexp(`(?m)^  pre-install script ci/check\.sh \(project at `+regexp.QuoteMeta(proj)+`, sha256 [0-9a-f]{64}\), around its install$`),
			"its consent is recorded")
	})

	It("a --pre-install or --post-install value that is not plain text is refused before anything else", func() {
		writeTree(tmp, map[string]string{"flag/pre\nx.sh": "#!/bin/sh\n"})
		for _, flag := range []string{"--pre-install", "--post-install"} {
			r := lola("install", mod, "-C", proj, "-t", "claude-code", flag, filepath.Join(tmp, "flag", "pre\nx.sh"), "--json")
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(Equal(`{"error":"` + flag + ` ` + filepath.Join(tmp, "flag", "pre?x.sh") +
				` holds a control character or a line break; lola runs only a script whose path is plain text","hints":[]}` + "\n"))
		}
		Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory(), "nothing was written")
	})

	It("the equivalent command of a picked install keeps --pre-install and --post-install as given", func() {
		Expect(lola("cache", "add", mod).code).To(Equal(0))
		writeTree(tmp, map[string]string{"flag/pre.sh": "#!/bin/sh\n"})
		pre := filepath.Join(tmp, "flag", "pre.sh")
		p := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("example")}, review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", "-C", proj, "--scope", "project", "-t", "claude-code", "--pre-install", pre, "--post-install", pre)
		Expect(r.code).To(Equal(0), r.err)
		Expect(equivalents(r.err)).To(Equal([]string{"lola install example --scope project -C " + proj + " -t claude-code --pre-install " + pre +
			" --post-install " + pre}))
	})

	DescribeTable("script paths reach human and JSON output through untrusted.Text",
		func(setup func() []string, shown string) {
			shown = strings.NewReplacer("BIN", filepath.Join(tmp, "bin"), "FLAG", filepath.Join(tmp, "flag")).Replace(shown)
			args := append([]string{"install", mod, "-C", proj, "-t", "claude-code", "-n"}, setup()...)
			r := lola(args...)
			if strings.HasPrefix(shown, "refused:") { // a project's at plan time (A27), a flag's at parse (RR4), on one line
				Expect(r.code).To(Equal(1))
				Expect(r.err).To(ContainSubstring(strings.TrimPrefix(shown, "refused:") + " holds a control character"))
				Expect(r.err).NotTo(ContainSubstring("\x1b"))
				return
			}
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("example pre-install script "+shown+" ("), "the whole line, unbroken")
			Expect(r.out).NotTo(ContainSubstring("\x1b"))
			r = lola(append(args, "--plain")...)
			var rows []string
			for _, l := range strings.Split(r.out, "\n") {
				if strings.HasPrefix(l, "script\t") {
					rows = append(rows, l)
				}
			}
			Expect(rows).To(HaveLen(1), "one row, never split")
			Expect(strings.Split(rows[0], "\t")).To(HaveLen(7))
			r = lola(append(args, "--json")...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(scriptsOf(r.out)).To(ConsistOf(HaveKeyWithValue("path", shown)))
		},
		Entry("user", func() []string {
			writeTree(tmp, map[string]string{"bin/pre\x1b[31m.sh": "#!/bin/sh\n", "config/lola/config.yaml": "version: 1\npre_install: \"" + tmp + "/bin/pre\\e[31m.sh\"\n"})
			return nil
		}, "BIN/pre?[31m.sh"),
		Entry("user, a line break", func() []string {
			writeTree(tmp, map[string]string{"bin/pre\nscript\texample\tforged.sh": "#!/bin/sh\n",
				"config/lola/config.yaml": "version: 1\npre_install: \"" + tmp + "/bin/pre\\nscript\\texample\\tforged.sh\"\n"})
			return nil
		}, "BIN/pre?script?example?forged.sh"),
		Entry("project", func() []string {
			writeTree(proj, map[string]string{"ci/pre\x1b[31m.sh": "#!/bin/sh\n", ".lola/project.yml": "version: 1\npre_install: \"ci/pre\\e[31m.sh\"\n"})
			return []string{"--trust=scripts"}
		}, "refused:pre_install: ci/pre?[31m.sh"),
		Entry("flag", func() []string {
			writeTree(tmp, map[string]string{"flag/pre\x1b[31m.sh": "#!/bin/sh\n"})
			return []string{"--pre-install", filepath.Join(tmp, "flag", "pre\x1b[31m.sh")}
		}, "refused:--pre-install FLAG/pre?[31m.sh"),
		Entry("flag, a line break", func() []string {
			writeTree(tmp, map[string]string{"flag/pre\nscript\tforged.sh": "#!/bin/sh\n"})
			return []string{"--pre-install", filepath.Join(tmp, "flag", "pre\nscript\tforged.sh")}
		}, "refused:--pre-install FLAG/pre?script?forged.sh"),
	)
})
