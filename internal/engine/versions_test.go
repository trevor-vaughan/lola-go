package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("the assistant version an install writes for", func() {
	var (
		p              paths.Paths
		tmp, home, mod string
		proj, cwd      string
		ctx            = context.Background()
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		home = p.Home
		Expect(os.MkdirAll(p.Targets, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(p.Targets, "quill.yaml"), []byte(quillEras), 0o600)).To(Succeed())
		proj, mod = filepath.Join(tmp, "proj"), filepath.Join(tmp, "m")
		writeTree(mod, map[string]string{"lola.yml": "name: m\n", "commands/c.md": "Go.\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		cwd = proj
	})
	project := func() engine.Scope { return engine.Scope{Kind: target.Project, ProjectDir: proj} }
	user := engine.Scope{Kind: target.User}
	elsewhere := func() { cwd = tmp } // the project is another directory than the one lola runs in
	installed := func(v string) {
		writeTree(filepath.Join(home, ".quill"), map[string]string{"version.json": `{"version": "` + v + `"}`})
	}
	newEngine := func() *engine.Engine {
		GinkgoHelper()
		reg, err := targets.Load(p)
		Expect(err).NotTo(HaveOccurred())
		e := engine.New(p, reg)
		e.Getwd = func() (string, error) { return cwd, nil }
		return e
	}
	install := func(dry bool, scs ...engine.ScopeInstall) (*engine.InstallResult, error) {
		GinkgoHelper()
		for i := range scs {
			scs[i].Source, scs[i].Targets = engine.SourceFlag, []string{"quill"}
		}
		return newEngine().InstallAll(ctx, engine.InstallSpec{Cwd: cwd, Modules: []engine.InstallRequest{{Arg: mod}},
			Options: engine.Options{Targets: []string{"quill"}, DryRun: dry}, Scopes: scs})
	}
	at := func(sc engine.Scope, versions map[string]string) engine.ScopeInstall {
		return engine.ScopeInstall{ScopeChoice: engine.ScopeChoice{Scope: sc}, Versions: versions}
	}
	read := func(path string) string {
		GinkgoHelper()
		b, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}
	recorded := func(res *engine.InstallResult) []string {
		var out []string
		for _, n := range res.Notices {
			if strings.HasPrefix(n.Text, "recorded ") || strings.HasPrefix(n.Text, "would record ") ||
				strings.HasPrefix(n.Text, "changed ") || strings.HasPrefix(n.Text, "would change ") {
				Expect(n.Level).To(Equal(engine.LevelInfo))
				Expect(n.Class).To(Equal(target.ClassDetail))
				out = append(out, n.Text)
			}
		}
		return out
	}
	codes := func(res *engine.InstallResult) []string {
		var out []string
		for _, f := range res.Findings {
			if strings.HasPrefix(f.Code, "version/") {
				out = append(out, f.Code)
			}
		}
		return out
	}

	It("Records leaves out a version the scope declares, compared as versions, and names the file", func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntarget_versions:\n  quill: \"2.0\"\n"})
		got, file, err := newEngine().Records(project(), map[string]string{"quill": "2.0.0"})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(BeEmpty())
		Expect(file).To(Equal(".lola/project.yml"))
		got, _, err = newEngine().Records(project(), map[string]string{"quill": "1.0"})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(map[string]string{"quill": "1.0"}))
		got, file, err = newEngine().Records(user, map[string]string{"quill": "1.0"})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(map[string]string{"quill": "1.0"}))
		Expect(file).To(Equal(p.Config))
	})

	Describe("VersionChoices", func() {
		It("asks about a two-era target the standard project declares nothing for, preselecting the installed era", func() {
			installed("1.2.0")
			qs, err := newEngine().VersionChoices(ctx, project(), []string{"claude-code", "quill"}, nil, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(qs).To(HaveExactElements(SatisfyAll(HaveField("Target", "quill"), HaveField("Eras", []string{"2.0", "1.0"}),
				HaveField("Labels", []string{">=2.0", ">=1.0 <2.0"}), HaveField("Summaries", []string{"", "commands in old/"}),
				HaveField("Drops", [][]string{nil, nil}), HaveField("Installed", "1.2.0"), HaveField("Preselect", "1.0"), HaveField("Declared", ""),
				HaveField("Standard", true))))
		})

		It("preselects nothing at another directory, whose assistant may be another than this machine's", func() {
			installed("1.2.0")
			elsewhere()
			qs, err := newEngine().VersionChoices(ctx, project(), []string{"quill"}, nil, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(qs).To(HaveLen(1))
			Expect(qs[0].Standard).To(BeFalse())
			Expect(qs[0].Installed).To(Equal("1.2.0"))
			Expect(qs[0].Preselect).To(BeEmpty())
		})

		It("preselects nothing when the assistant is not installed", func() {
			qs, err := newEngine().VersionChoices(ctx, user, []string{"quill"}, nil, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(qs).To(HaveLen(1))
			Expect(qs[0].Standard).To(BeTrue())
			Expect(qs[0].Installed).To(BeEmpty())
			Expect(qs[0].Preselect).To(BeEmpty())
		})

		It("names the version the scope declares, its era preselected and recorded as declared, and leaves out a single-era target", func() {
			installed("2.1.0")
			writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntarget_versions: {quill: \"1.2\"}\n"})
			qs, err := newEngine().VersionChoices(ctx, project(), []string{"claude-code", "quill"}, nil, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(qs).To(HaveLen(1))
			Expect(qs[0].Declared).To(Equal("1.2"))
			Expect(qs[0].Preselect).To(Equal("1.0"))
			Expect(qs[0].Recorded("1.0")).To(Equal("1.2"), "the declared version stays")
			Expect(qs[0].Recorded("2.0")).To(Equal("2.1.0"), "the installed era: the installed version")
		})

		It("says, per era, the values of the modules installed that it drops and another era keeps", func() {
			agents := map[string]string{"lola.yml": "name: a\n"}
			for _, n := range []string{"rev", "lint", "doc"} {
				agents["agents/"+n+".md"] = "---\ndescription: D.\ncolor: accent\n---\nX.\n"
			}
			agents["agents/hex.md"] = "---\ndescription: D.\ncolor: \"#ff0000\"\n---\nX.\n"
			writeTree(filepath.Join(tmp, "a"), agents)
			e := newEngine()
			ch, err := e.InstallChoices(ctx, engine.ChoicesRequest{Cwd: cwd, Scopes: []engine.ScopeChoice{{Scope: project(), Source: engine.SourceFlag}},
				Modules: []engine.InstallRequest{{Arg: filepath.Join(tmp, "a")}}})
			Expect(err).NotTo(HaveOccurred())
			qs, err := e.VersionChoices(ctx, project(), []string{"opencode"}, ch, []string{"a"})
			Expect(err).NotTo(HaveOccurred())
			Expect(qs).To(HaveLen(1))
			Expect(qs[0].Eras).To(Equal([]string{"2.0", "1.0"}))
			Expect(qs[0].Drops).To(Equal([][]string{{`doc's color "accent"`, `lint's color "accent"`, `rev's color "accent"`}, nil}))
			qs, err = e.VersionChoices(ctx, project(), []string{"opencode"}, ch, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(qs[0].Drops).To(Equal([][]string{nil, nil}), "no module installed")
		})

		It("records the installed version for the installed era, else the era's since", func() {
			installed("1.2.0")
			qs, err := newEngine().VersionChoices(ctx, project(), []string{"quill"}, nil, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(qs[0].Recorded("1.0")).To(Equal("1.2.0"))
			Expect(qs[0].Recorded("2.0")).To(Equal("2.0"))
		})
	})

	Describe("DefaultVersions", func() {
		It("records the installed version at the standard project", func() {
			installed("1.2.0")
			got, err := newEngine().DefaultVersions(ctx, project(), []string{"quill"}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(map[string]string{"quill": "1.2.0"}))
		})

		It("records nothing at a standard scope where the assistant is not installed", func() {
			got, err := newEngine().DefaultVersions(ctx, user, []string{"quill"}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(BeEmpty())
		})

		It("refuses another directory, coaching a run per era", func() {
			installed("1.2.0")
			elsewhere()
			rerun := func(id, since string) string { return "lola install m -C " + proj + " -t " + id + "@" + since }
			writeTree(proj, map[string]string{".lola/project.yml": "version: 1\n"})
			_, err := newEngine().DefaultVersions(ctx, project(), []string{"claude-code", "quill"}, rerun)
			Expect(err).To(MatchError("which quill version will read " + proj + "? lola assumes the installed quill only for the project you run it in, and " +
				proj + " declares none"))
			Expect(errs.Hints(err)).To(Equal([]string{
				"for quill >=2.0: lola install m -C " + proj + " -t quill@2.0",
				"for quill >=1.0 <2.0: lola install m -C " + proj + " -t quill@1.0"}), "declaring it by hand would need trusting the file")
		})

		It("at both scopes with the project in another directory, records at user scope and refuses the project", func() {
			installed("1.2.0")
			elsewhere()
			e := newEngine()
			got, err := e.DefaultVersions(ctx, user, []string{"quill"}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(map[string]string{"quill": "1.2.0"}))
			_, err = e.DefaultVersions(ctx, project(), []string{"quill"}, nil)
			Expect(errs.Hints(err)).To(ContainElement("for quill >=1.0 <2.0: -t quill@1.0"))
		})
	})

	Describe("an install given versions", func() {
		manifest := func() string { return filepath.Join(proj, ".lola", "project.yml") }

		It("renders the chosen era, records it in project.yml and says so", func() {
			res, err := install(false, at(project(), map[string]string{"quill": "1.0"}))
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Join(proj, "old", "commands", "c.md")).To(BeAnExistingFile())
			Expect(filepath.Join(proj, "new")).NotTo(BeAnExistingFile())
			Expect(read(manifest())).To(ContainSubstring("target_versions:\n  quill: \"1.0\"\n"))
			Expect(recorded(res)).To(Equal([]string{"recorded quill 1.0 in .lola/project.yml (target_versions): files are written for quill >=1.0 <2.0"}))
		})

		It("records at user scope in config.yaml, after the files are written", func() {
			res, err := install(false, at(user, map[string]string{"quill": "1.0"}))
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Join(home, "old", "commands", "c.md")).To(BeAnExistingFile())
			Expect(read(p.Config)).To(ContainSubstring("target_versions:\n  quill: \"1.0\"\n"))
			Expect(recorded(res)).To(Equal([]string{"recorded quill 1.0 in " + p.Config + " (target_versions): files are written for quill >=1.0 <2.0"}))
		})

		It("shows what it would record under -n, and writes nothing", func() {
			res, err := install(true, at(project(), map[string]string{"quill": "1.0"}), at(user, map[string]string{"quill": "1.0"}))
			Expect(err).NotTo(HaveOccurred())
			Expect(manifest()).NotTo(BeAnExistingFile())
			Expect(p.Config).NotTo(BeAnExistingFile())
			Expect(filepath.Join(proj, "old")).NotTo(BeAnExistingFile())
			Expect(recorded(res)).To(ConsistOf("would record quill 1.0 in .lola/project.yml (target_versions): files are written for quill >=1.0 <2.0",
				"would record quill 1.0 in "+p.Config+" (target_versions): files are written for quill >=1.0 <2.0"))
		})

		It("shows the pending recording to the review before the write, and only the done one after", func() {
			var reviewed []string
			e := newEngine()
			res, err := e.InstallAll(ctx, engine.InstallSpec{Cwd: cwd, Modules: []engine.InstallRequest{{Arg: mod}},
				Options: engine.Options{Targets: []string{"quill"}},
				Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: project(), Source: engine.SourceFlag}, Targets: []string{"quill"},
					Versions: map[string]string{"quill": "1.0"}}},
				BeforeWrite: func(r *engine.InstallResult) error {
					reviewed = recorded(r)
					Expect(manifest()).NotTo(BeAnExistingFile())
					return nil
				}})
			Expect(err).NotTo(HaveOccurred())
			Expect(reviewed).To(Equal([]string{"would record quill 1.0 in .lola/project.yml (target_versions): files are written for quill >=1.0 <2.0"}))
			Expect(recorded(res)).To(Equal([]string{"recorded quill 1.0 in .lola/project.yml (target_versions): files are written for quill >=1.0 <2.0"}))
		})

		It("lists what it records, would record under -n, with where each came from", func() {
			writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntarget_versions: {quill: \"2.0\"}\n"})
			for _, dry := range []bool{true, false} {
				si := at(project(), map[string]string{"quill": "1.2"})
				si.Sources = map[string]string{"quill": engine.VersionChosen}
				res, err := install(dry, si)
				Expect(err).NotTo(HaveOccurred())
				Expect(res.TargetVersions).To(Equal([]engine.TargetVersion{{Scope: "project", Target: "quill", Version: "1.2", Previous: "2.0",
					Source: engine.VersionChosen, File: filepath.Join(proj, ".lola", "project.yml"), Era: "1.0"}}), "dry run %v", dry)
			}
			res, err := install(false, at(user, map[string]string{"quill": "2.1"}))
			Expect(err).NotTo(HaveOccurred())
			Expect(res.TargetVersions).To(Equal([]engine.TargetVersion{{Scope: "user", Target: "quill", Version: "2.1", Source: engine.VersionFlag, File: p.Config, Era: "2.0"}}))
			res, err = install(false, at(user, map[string]string{"quill": "2.1"}))
			Expect(err).NotTo(HaveOccurred())
			Expect(res.TargetVersions).To(BeEmpty())
			Expect(res.TargetVersions).NotTo(BeNil())
		})

		It("replaces the version project.yml declares with the one given", func() {
			writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntarget_versions: {quill: \"1.2\"}\n"})
			res, err := install(false, at(project(), map[string]string{"quill": "2.0"}))
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Join(proj, "new", "commands", "c.md")).To(BeAnExistingFile())
			Expect(read(manifest())).To(ContainSubstring(`quill: "2.0"`))
			Expect(read(manifest())).NotTo(ContainSubstring("1.2"))
			Expect(recorded(res)).To(Equal([]string{"changed quill 1.2 → 2.0 in .lola/project.yml (target_versions): files are written for quill >=2.0"}))
			writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntarget_versions: {quill: \"1.2\"}\n"})
			res, err = install(true, at(project(), map[string]string{"quill": "2.0"}))
			Expect(err).NotTo(HaveOccurred())
			Expect(recorded(res)).To(Equal([]string{"would change quill 1.2 → 2.0 in .lola/project.yml (target_versions): files are written for quill >=2.0"}))
		})

		It("says when the version recorded is the installed one or of its era, and warns once when it is of another", func() {
			chosen := func(res *engine.InstallResult) []target.Finding {
				return slices.DeleteFunc(slices.Clone(res.Findings), func(f target.Finding) bool { return f.Code != "version/chosen-era" })
			}
			installed("1.2.0")
			res, err := install(false, at(project(), map[string]string{"quill": "1.2.0"}))
			Expect(err).NotTo(HaveOccurred())
			Expect(recorded(res)).To(Equal([]string{"recorded quill 1.2.0 (the installed version) in .lola/project.yml (target_versions): files are written for quill >=1.0 <2.0"}))
			Expect(chosen(res)).To(BeEmpty())
			res, err = install(false, at(project(), map[string]string{"quill": "1.0"}))
			Expect(err).NotTo(HaveOccurred())
			Expect(recorded(res)).To(Equal([]string{"changed quill 1.2.0 → 1.0 in .lola/project.yml (target_versions): files are written for quill >=1.0 <2.0 (matches the installed 1.2.0)"}))
			Expect(chosen(res)).To(BeEmpty())

			installed("2.1.0")
			for _, dry := range []bool{true, false} {
				res, err = install(dry, at(user, map[string]string{"quill": "1.0"}))
				Expect(err).NotTo(HaveOccurred())
				Expect(recorded(res)).To(ConsistOf(HaveSuffix("in " + p.Config + " (target_versions): files are written for quill >=1.0 <2.0")))
				Expect(chosen(res)).To(Equal([]target.Finding{{Severity: target.SeverityWarn, Target: "quill", Kind: "assistant", Code: "version/chosen-era",
					Text: "this machine's quill 2.1.0 reads files written for >=2.0; these are written for >=1.0 <2.0 (commands in old/)",
					Hint: "to write for what this machine's quill 2.1.0 reads (>=2.0): -t quill@2.1.0"}}), "dry run %v", dry)
			}
			elsewhere()
			res, err = install(false, at(project(), map[string]string{"quill": "1.1"}))
			Expect(err).NotTo(HaveOccurred())
			Expect(chosen(res)).To(BeEmpty(), "this machine's assistant is not assumed for another directory")
		})

		It("names no installed version beside one recorded for another directory", func() {
			installed("1.2.0")
			elsewhere()
			res, err := install(false, at(project(), map[string]string{"quill": "2.0"}))
			Expect(err).NotTo(HaveOccurred())
			Expect(recorded(res)).To(Equal([]string{"recorded quill 2.0 in .lola/project.yml (target_versions): files are written for quill >=2.0"}))
			Expect(os.Remove(filepath.Join(proj, ".lola", "project.yml"))).To(Succeed())
			res, err = install(false, at(project(), map[string]string{"quill": "1.2"}))
			Expect(err).NotTo(HaveOccurred())
			Expect(recorded(res)).To(Equal([]string{"recorded quill 1.2 in .lola/project.yml (target_versions): files are written for quill >=1.0 <2.0"}),
				"this machine's version is not the one assumed for another directory, even when equal")
		})

		It("records in lock.json the era it rendered for the version given, not the one project.yml declared", func() {
			writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntarget_versions: {quill: \"2.1\"}\n"})
			_, err := install(false, at(project(), map[string]string{"quill": "1.5"}))
			Expect(err).NotTo(HaveOccurred())
			Expect(read(filepath.Join(proj, ".lola", "lock.json"))).To(ContainSubstring("\"eras\": {\n        \"quill\": \"1.0\"\n      }"))
		})

		It("records nothing for a version equal to the declared one written another way", func() {
			writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntarget_versions: {quill: \"1.0.0\"}\n"})
			res, err := install(false, at(project(), map[string]string{"quill": "1.0"}))
			Expect(err).NotTo(HaveOccurred())
			Expect(recorded(res)).To(BeEmpty())
			Expect(read(manifest())).To(ContainSubstring(`quill: "1.0.0"`))
		})

		It("says the files are written for the version when config.yaml cannot record it, and how to record it", func() {
			Expect(os.MkdirAll(filepath.Dir(p.Config), 0o755)).To(Succeed())
			Expect(os.Symlink(filepath.Join(tmp, "gone", "config.yaml"), p.Config)).To(Succeed())
			res, err := install(false, at(user, map[string]string{"quill": "1.0"}))
			Expect(res).To(BeNil())
			Expect(err).To(MatchError(ContainSubstring("the files were written for quill 1.0, but the version was not recorded in " + p.Config)))
			Expect(errs.Hints(err)).To(ContainElement(`rerun with -t quill@1.0, or add quill: "1.0" under target_versions: in ` + p.Config))
			Expect(filepath.Join(home, "old", "commands", "c.md")).To(BeAnExistingFile())
		})

		It("records nothing again on a re-run with the version already declared", func() {
			_, err := install(false, at(project(), map[string]string{"quill": "1.0"}))
			Expect(err).NotTo(HaveOccurred())
			was := read(manifest())
			res, err := install(false, at(project(), map[string]string{"quill": "1.0"}))
			Expect(err).NotTo(HaveOccurred())
			Expect(recorded(res)).To(BeEmpty())
			Expect(read(manifest())).To(Equal(was))
		})

		It("records a version when the run has nothing else to write", func() {
			_, err := install(false, at(user, nil))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Config).NotTo(BeAnExistingFile())
			res, err := install(false, at(user, map[string]string{"quill": "2.0"}))
			Expect(err).NotTo(HaveOccurred())
			Expect(read(p.Config)).To(ContainSubstring("quill: \"2.0\""))
			Expect(recorded(res)).To(HaveLen(1))
		})

		It("refuses a version that is not one, naming the flag, and writes nothing", func() {
			_, err := install(false, at(project(), map[string]string{"quill": "new"}))
			Expect(err).To(MatchError(`-t quill@new: "new" is not a version`))
			Expect(errs.Hints(err)).To(Equal([]string{"for quill >=2.0: -t quill@2.0", "for quill >=1.0 <2.0: -t quill@1.0"}))
			Expect(manifest()).NotTo(BeAnExistingFile())
		})

		It("takes any version for a target with one era, records nothing for it, and says so", func() {
			for _, v := range []string{"0.1", "7.0"} {
				res, err := newEngine().InstallAll(ctx, engine.InstallSpec{Cwd: cwd, Modules: []engine.InstallRequest{{Arg: mod}},
					Options: engine.Options{Targets: []string{"claude-code"}},
					Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: project(), Source: engine.SourceFlag}, Targets: []string{"claude-code"},
						Versions: map[string]string{"claude-code": v}}}})
				Expect(err).NotTo(HaveOccurred(), v)
				Expect(filepath.Join(proj, ".claude", "commands", "c.md")).To(BeAnExistingFile())
				Expect(read(manifest())).NotTo(ContainSubstring("target_versions"), v)
				Expect(recorded(res)).To(BeEmpty(), v)
				Expect(res.Notices).To(ContainElement(engine.Notice{Level: engine.LevelInfo, Class: target.ClassDetail,
					Text: "claude-code's files are the same for every version lola knows; nothing recorded"}), v)
			}
		})

		It("refuses a version older than any era, hinting the -t of each, and writes nothing", func() {
			_, err := install(false, at(project(), map[string]string{"quill": "0.5"}))
			Expect(err).To(MatchError("-t quill@0.5: quill 0.5 is older than any quill lola knows"))
			Expect(errs.Hints(err)).To(Equal([]string{"for quill >=2.0: -t quill@2.0", "for quill >=1.0 <2.0: -t quill@1.0"}))
			Expect(manifest()).NotTo(BeAnExistingFile())
		})

		It("refuses a version for a target lola has none for", func() {
			_, err := install(false, at(project(), map[string]string{"nope": "1.0"}))
			Expect(err).To(MatchError(ContainSubstring(`unknown target "nope"`)))
			Expect(manifest()).NotTo(BeAnExistingFile())
		})
	})

	Describe("an assistant that is not installed", func() {
		notInstalled := func(res *engine.InstallResult) []string {
			var out []string
			for _, n := range res.Notices {
				if strings.Contains(n.Text, " is not installed here") {
					Expect(n.Class).To(Equal(target.ClassDetail))
					out = append(out, n.Text)
					out = append(out, n.Hints...)
				}
			}
			return out
		}

		It("is written for its newest era with nothing recorded, and the notice says how to choose", func() {
			res, err := install(false, at(user, nil))
			Expect(err).NotTo(HaveOccurred())
			hints := []string{"to choose, for quill >=2.0: -t quill@2.0", "to choose, for quill >=1.0 <2.0: -t quill@1.0"} // no EraRun: the -t alone
			Expect(notInstalled(res)).To(Equal(append([]string{"quill is not installed here, so lola wrote for quill >=2.0 and recorded nothing"}, hints...)))
			res, err = install(true, at(project(), nil))
			Expect(err).NotTo(HaveOccurred())
			Expect(notInstalled(res)).To(Equal(append([]string{"quill is not installed here, so lola would write for quill >=2.0 and record nothing"}, hints...)))
		})

		It("says nothing when the version is given or declared, or the project is another directory", func() {
			res, err := install(false, at(project(), map[string]string{"quill": "1.0"}))
			Expect(err).NotTo(HaveOccurred())
			Expect(notInstalled(res)).To(BeEmpty())
			res, err = install(false, at(project(), nil))
			Expect(err).NotTo(HaveOccurred())
			Expect(notInstalled(res)).To(BeEmpty(), "declared by the first run")
			Expect(os.RemoveAll(filepath.Join(proj, ".lola"))).To(Succeed())
			elsewhere()
			res, err = install(false, at(project(), nil))
			Expect(err).NotTo(HaveOccurred())
			Expect(notInstalled(res)).To(BeEmpty())
		})
	})

	Describe("version findings", func() {
		It("are not raised for a project in another directory", func() {
			installed("1.0")
			elsewhere()
			res, err := install(false, at(project(), nil))
			Expect(err).NotTo(HaveOccurred())
			Expect(codes(res)).To(BeEmpty())
			writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntarget_versions: {quill: \"2.0\"}\n"})
			res, err = install(false, at(project(), nil))
			Expect(err).NotTo(HaveOccurred())
			Expect(codes(res)).To(BeEmpty())
		})

		DescribeTable("offer the era that keeps a frontmatter value the era written for drops, before the module's own fix, to an install",
			func(color string, eraRun func(sc engine.Scope, id, since string) string, want string) {
				agent := filepath.Join(tmp, "a")
				writeTree(agent, map[string]string{"lola.yml": "name: a\n", "agents/rev.md": "---\ndescription: Reviews.\ncolor: " + color + "\n---\nReview.\n"})
				Expect(os.MkdirAll(filepath.Join(proj, ".opencode"), 0o755)).To(Succeed())
				// Declared: an assistant neither declared nor installed is the not-installed notice's,
				// whose runs for each era leave the finding none of its own.
				writeTree(filepath.Join(proj, ".lola"), map[string]string{"project.yml": "version: 1\ntarget_versions: {opencode: \"2.0\"}\n"})
				res, err := newEngine().InstallAll(ctx, engine.InstallSpec{Cwd: cwd, Modules: []engine.InstallRequest{{Arg: agent}},
					Options: engine.Options{Targets: []string{"opencode"}, EraRun: eraRun},
					Scopes:  []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: project(), Source: engine.SourceFlag}, Targets: []string{"opencode"}}}})
				Expect(err).NotTo(HaveOccurred())
				var dropped []target.Finding
				for _, f := range res.Findings {
					if f.Code == "frontmatter-value-dropped" {
						dropped = append(dropped, f)
					}
				}
				Expect(dropped).To(HaveLen(1))
				Expect(dropped[0].Key).To(Equal("color"))
				own := "in rev.targets.yaml beside the agent, set opencode's color to"
				if want == "" {
					Expect(dropped[0].Hint).To(HavePrefix(own))
					Expect(dropped[0].KeptSince).To(BeEmpty())
					Expect(dropped[0].UserHint).To(BeEmpty())
					return
				}
				Expect(dropped[0].KeptSince).To(Equal("1.0"))
				Expect(dropped[0].Text).To(Equal(`color "accent" dropped — opencode >=2.0: agent colors must be hex (#rrggbb)`))
				Expect(dropped[0].Hint).To(HavePrefix(want + "; or " + own))
				Expect(dropped[0].UserHint).To(Equal(dropped[0].Hint), "install prints the way to keep it and the module's own fix")
			},
			Entry("a theme name the older era keeps", "accent", func(sc engine.Scope, id, since string) string {
				return "lola install a --" + sc.Kind.String() + " -t " + id + "@" + since
			}, "keep it by writing for opencode >=1.0 <2.0: lola install a --project -t opencode@1.0"),
			Entry("from a command that takes no -t version, the edit of target_versions and the sync that applies it", "accent", nil,
				`keep it by writing for opencode >=1.0 <2.0: set target_versions.opencode to "1.0" in .lola/project.yml, then: lola sync --project`),
			Entry("not when no era keeps it", "purple", func(_ engine.Scope, id, since string) string { return "-t " + id + "@" + since }, ""),
		)

		It("raise only the choice's warning in the run that records the version, and after it the mismatch only when files are written", func() {
			installed("1.2.0")
			res, err := install(false, at(project(), map[string]string{"quill": "2.0"}))
			Expect(err).NotTo(HaveOccurred())
			Expect(codes(res)).To(Equal([]string{"version/chosen-era"}))
			res, err = install(false, at(project(), nil))
			Expect(err).NotTo(HaveOccurred())
			Expect(codes(res)).To(BeEmpty(), "nothing written")
			writeTree(mod, map[string]string{"commands/d.md": "Do.\n"})
			res, err = install(false, at(project(), nil))
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Findings).To(ContainElement(SatisfyAll(HaveField("Code", "version/era-mismatch"),
				HaveField("Hint", `set target_versions.quill to "1.2.0" in .lola/project.yml, then: lola sync --project; or install a release of quill >=2.0`))))
		})

		It("are raised for the project lola runs in", func() {
			installed("1.0")
			res, err := install(false, at(project(), nil))
			Expect(err).NotTo(HaveOccurred())
			Expect(codes(res)).To(ConsistOf("version/undeclared"))
		})
	})

	Describe("a scope whose modules are written for one version", func() {
		var n string
		BeforeEach(func() {
			n = filepath.Join(tmp, "n")
			writeTree(n, map[string]string{"lola.yml": "name: n\n", "commands/d.md": "Do.\n"})
			_, err := install(false, at(project(), map[string]string{"quill": "2.0"}))
			Expect(err).NotTo(HaveOccurred())
		})
		installN := func(dry, force bool, version string) (*engine.InstallResult, error) {
			GinkgoHelper()
			return newEngine().InstallAll(ctx, engine.InstallSpec{Cwd: cwd, Modules: []engine.InstallRequest{{Arg: n}},
				Options: engine.Options{Targets: []string{"quill"}, DryRun: dry, Force: force, ForceRun: "lola install n --force",
					EraRun: func(_ engine.Scope, id, since string) string { return "lola install n -t " + id + "@" + since }},
				Scopes: []engine.ScopeInstall{{ScopeChoice: engine.ScopeChoice{Scope: project(), Source: engine.SourceFlag}, Targets: []string{"quill"},
					Versions: map[string]string{"quill": version}}}})
		}
		eraOf := func(module string) string {
			GinkgoHelper()
			led, err := ledger.Load(filepath.Join(proj, ".lola", "lock.json"))
			Expect(err).NotTo(HaveOccurred())
			return led.Modules[module].Eras["quill"]
		}

		It("WrittenFor names the era the scope's modules are written for, leaving out the modules given", func() {
			got, err := newEngine().WrittenFor(project(), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(map[string]string{"quill": "2.0"}))
			got, err = newEngine().WrittenFor(project(), []string{"m"})
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(BeEmpty(), "reinstalling the only module holds no version")
			got, err = newEngine().WrittenFor(user, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(BeEmpty(), "nothing installed")
		})

		It("WrittenFor leaves out a target whose modules are already written for different eras", func() {
			_, err := installN(false, true, "1.0")
			Expect(err).NotTo(HaveOccurred())
			led, err := ledger.Load(filepath.Join(proj, ".lola", "lock.json"))
			Expect(err).NotTo(HaveOccurred())
			ent := led.Modules["m"]
			ent.Eras = map[string]string{"quill": "2.0"}
			led.Modules["m"] = ent
			Expect(led.Save(filepath.Join(proj, ".lola", "lock.json"))).To(Succeed())
			got, err := newEngine().WrittenFor(project(), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(BeEmpty())
		})

		It("refuses, under -n too and writing nothing, an install for another version, hinting the scope's version and --force", func() {
			for _, dry := range []bool{true, false} {
				_, err := installN(dry, false, "1.0")
				Expect(err).To(MatchError("this install writes quill files for >=1.0 <2.0, but m in this project is written for >=2.0; lola keeps one quill version in a scope"), "dry run %v", dry)
				Expect(errs.Hints(err)).To(Equal([]string{"install for >=2.0, the version m is written for: lola install n -t quill@2.0",
					"or rewrite m for >=1.0 <2.0 too: lola install n --force"}))
			}
			Expect(filepath.Join(proj, "old")).NotTo(BeAnExistingFile())
			Expect(eraOf("m")).To(Equal("2.0"))
			Expect(read(filepath.Join(proj, ".lola", "project.yml"))).To(ContainSubstring("quill: \"2.0\""))
		})

		It("installs for the version the scope's modules are written for", func() {
			_, err := installN(false, false, "2.0")
			Expect(err).NotTo(HaveOccurred())
			Expect(eraOf("n")).To(Equal("2.0"))
		})

		It("under --force, rewrites the scope's other modules for the version it writes for, in the same run", func() {
			res, err := installN(false, true, "1.0")
			Expect(err).NotTo(HaveOccurred())
			Expect(eraOf("m")).To(Equal("1.0"))
			Expect(eraOf("n")).To(Equal("1.0"))
			Expect(res.Modules).To(ContainElement(SatisfyAll(HaveField("Name", "m"), HaveField("Rewritten", true))))
		})

		It("reinstalls the scope's only module for another version without refusing", func() {
			_, err := install(false, at(project(), map[string]string{"quill": "1.0"}))
			Expect(err).NotTo(HaveOccurred())
			Expect(eraOf("m")).To(Equal("1.0"))
		})
	})
})
