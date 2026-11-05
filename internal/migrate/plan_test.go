package migrate_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/migrate"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("NewPlan", func() {
	var (
		tmp, userHome, projA, projB, src string
		h                                pyHome
		e                                *engine.Engine
	)
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		userHome = filepath.Join(tmp, "home")
		h = pyHome{dir: filepath.Join(userHome, ".lola")}
		projA, projB, src = filepath.Join(tmp, "a-proj"), filepath.Join(tmp, "b-proj"), filepath.Join(tmp, "src", "review")
		for _, d := range []string{projA, projB} {
			Expect(os.MkdirAll(d, 0o755)).To(Succeed())
		}
		writeTree(src, reviewModule)
		h.source("review", map[string]string{"source": src, "type": "folder"})
		installAssistants(tmp)
		e = newEngine()
	})
	ctx := context.Background()
	planWith := func(opts migrate.Options) *migrate.Plan {
		st, err := migrate.Load(h.dir, userHome)
		Expect(err).NotTo(HaveOccurred())
		p, err := migrate.NewPlan(ctx, e, st, opts)
		Expect(err).NotTo(HaveOccurred())
		return p
	}
	plan := func() *migrate.Plan { return planWith(migrate.Options{}) }
	// unpinned drops the cached version each install is pinned to, which differs per run.
	unpinned := func(list []migrate.Install) []migrate.Install {
		for i := range list {
			Expect(list[i].Version).NotTo(BeNil())
			list[i].Version = nil
		}
		return list
	}
	at := func(proj, assistant string) rec {
		return rec{"module": "review", "assistant": assistant, "scope": "project", "project_path": proj}
	}

	It("groups records into destinations: project roots by key, then the user scope", func() {
		h.install(at(projB, "claude-code"), rec{"module": "review", "assistant": "claude-code", "scope": "user"},
			at(projA, "claude-code"), at(projA, "opencode"))
		p := plan()
		Expect(p.Destinations).To(HaveLen(3))
		Expect(p.Destinations[0].Root).To(Equal(projA))
		Expect(p.Destinations[0].ID).To(MatchRegexp(`^[0-9a-f]{16}$`))
		Expect(p.Destinations[1].Root).To(Equal(projB))
		Expect(p.Destinations[2].Scope.Kind).To(Equal(target.User))
		Expect(p.Destinations[2].ID).To(Equal("user"))
		Expect(p.Destinations[2].Root).To(Equal(userHome))
		Expect(p.Destinations[0].Installs[0].Version).To(HaveField("Name", "review"), "pinned to the version the plan resolved")
		Expect(unpinned(p.Destinations[0].Installs)).To(Equal([]migrate.Install{{PythonModules: []string{"review"}, Kind: source.KindFolder, Source: src,
			Targets: []string{"claude-code", "opencode"}}}), "one install per source, with every record's target")
		Expect(p.Destinations[0].Targets).To(Equal([]string{"claude-code", "opencode"}))
	})

	DescribeTable("decides each record's outcome (§4)",
		func(assistant string, setup func(), outcome, want string) {
			if setup != nil {
				setup()
			}
			h.install(at(projA, assistant))
			p := plan()
			Expect(p.Destinations).To(HaveLen(1))
			d := p.Destinations[0]
			switch outcome {
			case "install":
				Expect(d.Installs).To(HaveLen(1))
				Expect(d.Installs[0].Targets).To(Equal([]string{want}))
				Expect(d.Unmappable).To(BeEmpty())
			case "already":
				Expect(d.Installs).To(BeEmpty())
				Expect(d.Already).To(HaveLen(1))
				Expect(d.Already[0].Module).To(Equal(want))
			default:
				Expect(d.Installs).To(BeEmpty())
				Expect(d.Unmappable).To(HaveLen(1))
				Expect(d.Unmappable[0].Reason).To(ContainSubstring(want))
				Expect(d.Unmappable[0].Hints).NotTo(BeEmpty())
				Expect(d.Recorded).To(BeEmpty(), "an unmappable record's files are left exactly as they are")
			}
		},
		Entry("claude-code", "claude-code", nil, "install", "claude-code"),
		Entry("gemini-cli", "gemini-cli", nil, "install", "gemini-cli"),
		Entry("opencode", "opencode", nil, "install", "opencode"),
		Entry("cursor", "cursor", nil, "unmappable", "lola-go has no target for cursor"),
		Entry("copilot-cli", "copilot-cli", nil, "unmappable", "lola-go has no target for copilot-cli"),
		Entry("copilot-vscode", "copilot-vscode", nil, "unmappable", "lola-go has no target for copilot-vscode"),
		Entry("openclaw", "openclaw", nil, "unmappable", "lola-go has no target for openclaw"),
		Entry("an unknown assistant", "zed", nil, "unmappable", "lola-go has no target for zed"),
		Entry("no source.yml", "claude-code", func() {
			Expect(os.RemoveAll(filepath.Join(h.dir, "modules"))).To(Succeed())
		}, "unmappable", "recorded no source for review"),
		Entry("a local source that no longer exists", "claude-code", func() {
			Expect(os.RemoveAll(src)).To(Succeed())
		}, "unmappable", "the local source "),
		Entry("a git:// source", "claude-code", func() {
			h.source("review", map[string]string{"source": "git://example.com/review.git", "type": "git"})
		}, "unmappable", "lola-go cannot fetch git://example.com/review.git"),
		Entry("a type lola-go cannot fetch", "claude-code", func() {
			h.source("review", map[string]string{"source": "svn://example.com/review", "type": "svn"})
		}, "unmappable", `source type "svn" is not one lola-go can fetch`),
		Entry("an archive URL lola-go reads as git", "claude-code", func() {
			h.source("review", map[string]string{"source": "https://example.com/review/archive/main", "type": "zipurl"})
		}, "unmappable", "lola-go reads https://example.com/review/archive/main as a git source, not zipurl"),
		Entry("an archive suffix lola-go does not read", "claude-code", func() {
			writeFile(filepath.Join(tmp, "review.tar.bz2"), "x")
			h.source("review", map[string]string{"source": filepath.Join(tmp, "review.tar.bz2"), "type": "tar"})
		}, "unmappable", "lola-go cannot fetch"),
		Entry("a relative local source", "claude-code", func() {
			h.source("review", map[string]string{"source": "src/review", "type": "folder"})
		}, "unmappable", "is not an absolute path"),
		Entry("already migrated: same source and target in the manifest", "claude-code", func() {
			writeFile(filepath.Join(projA, ".lola", "project.yml"), "version: 1\ntargets: [claude-code]\nmodules:\n  - name: review\n    source: "+src+"\n")
		}, "already", "review"),
		Entry("already migrated under another lola-go name, matched by source", "claude-code", func() {
			writeFile(filepath.Join(projA, ".lola", "project.yml"), "version: 1\ntargets: [claude-code]\nmodules:\n  - name: code-review\n    source: "+src+"\n")
		}, "already", "code-review"),
		Entry("already migrated with a source relative to the project", "claude-code", func() {
			writeFile(filepath.Join(projA, ".lola", "project.yml"), "version: 1\ntargets: [claude-code]\nmodules:\n  - name: review\n    source: ../src/review\n")
		}, "already", "review"),
	)

	It("matches an entry whose absolute source reaches the folder through a symbolic link", func() {
		link := filepath.Join(tmp, "srclink")
		Expect(os.Symlink(filepath.Dir(src), link)).To(Succeed())
		writeFile(filepath.Join(projA, ".lola", "project.yml"),
			"version: 1\ntargets: [claude-code]\nmodules:\n  - name: review\n    source: "+filepath.Join(link, "review")+"\n")
		h.install(at(projA, "claude-code"))

		p := plan()
		Expect(p.Destinations).To(HaveLen(1))
		Expect(p.Destinations[0].Installs).To(BeEmpty())
		Expect(p.Destinations[0].Already).To(HaveLen(1))
	})

	It("installs a target the manifest's entry lacks, naming the lola-go module it matched by source", func() {
		writeFile(filepath.Join(projA, ".lola", "project.yml"),
			"version: 1\ntargets: [claude-code]\nmodules:\n  - name: code-review\n    source: "+src+"\n")
		h.install(at(projA, "claude-code"), at(projA, "gemini-cli"))
		d := plan().Destinations[0]
		Expect(d.Already).To(Equal([]migrate.Install{{Module: "code-review", PythonModules: []string{"review"}, Kind: source.KindFolder,
			Source: src, Targets: []string{"claude-code"}}}))
		Expect(unpinned(d.Installs)).To(Equal([]migrate.Install{{Module: "code-review", PythonModules: []string{"review"}, Kind: source.KindFolder,
			Source: src, Targets: []string{"gemini-cli"}}}))
	})

	It("reports a record whose project_path cannot be used without a destination", func() {
		h.install(at(filepath.Join(tmp, "gone"), "claude-code"))
		p := plan()
		Expect(p.Destinations).To(BeEmpty())
		Expect(p.Unmappable).To(HaveLen(1))
		Expect(p.Unmappable[0].Reason).To(ContainSubstring("no longer exists"))
	})

	It("maps the source: the record's ref, else source.yml's, and content_dirname as --path", func() {
		srv := filepath.Join(tmp, "srv")
		for name, files := range map[string]map[string]string{"review": {"plugins/review/agents/critic.md": reviewModule["agents/critic.md"]},
			"other": {"commands/ship.md": reviewModule["commands/ship.md"], "module/agents/x.md": reviewModule["agents/critic.md"]}} {
			work := filepath.Join(tmp, "work", name)
			writeTree(work, files)
			git(work, "init", "-q", "-b", "main")
			git(work, "add", "-A")
			git(work, "commit", "-q", "-m", "init")
			git(work, "tag", "v2")
			git(tmp, "clone", "-q", "--bare", work, filepath.Join(srv, name+".git"))
		}
		url := testenv.ServeGit(srv)
		h.source("review", map[string]string{"source": url + "/review.git", "type": "git", "ref": "main", "content_dirname": "plugins/review"})
		h.source("other", map[string]string{"source": url + "/other.git", "type": "git", "ref": "main", "content_dirname": "/"})
		h.install(rec{"module": "review", "assistant": "claude-code", "scope": "user", "ref": "v2", "agents": []string{"critic"}},
			rec{"module": "other", "assistant": "claude-code", "scope": "user", "commands": []string{"ship"}})
		d := plan().Destinations[0]
		Expect(unpinned(d.Installs)).To(Equal([]migrate.Install{
			{PythonModules: []string{"review"}, Kind: source.KindGit, Source: url + "/review.git", Ref: "v2",
				Path: "plugins/review", Targets: []string{"claude-code"}},
			{PythonModules: []string{"other"}, Kind: source.KindGit, Source: url + "/other.git", Ref: "main", Path: ".",
				Targets: []string{"claude-code"}},
		}), `"/" is the repository's top, not the module/ lola-go would find there`)
	})

	Context("a source that cannot be fetched", func() {
		var url string
		BeforeEach(func() {
			srv := filepath.Join(tmp, "srv")
			Expect(os.MkdirAll(srv, 0o755)).To(Succeed())
			url = testenv.ServeGit(srv)
			h.source("gone", map[string]string{"source": url + "/gone.git", "type": "git"})
		})
		gone := func(proj, assistant string) rec {
			return rec{"module": "gone", "assistant": assistant, "scope": "project", "project_path": proj}
		}

		It("leaves only that module unmappable, with the cause and both hints", func() {
			h.install(at(projA, "claude-code"), gone(projA, "claude-code"))
			d := plan().Destinations[0]
			Expect(d.Installs).To(HaveLen(1))
			Expect(d.Installs[0].PythonModules).To(Equal([]string{"review"}))
			Expect(d.Unmappable).To(HaveLen(1))
			Expect(d.Unmappable[0].Module).To(Equal("gone"))
			Expect(d.Unmappable[0].Reason).To(HavePrefix("lola-go cannot fetch " + url + "/gone.git: "))
			Expect(d.Unmappable[0].Hints).To(Equal([]string{"git ls-remote -- " + url + "/gone.git", "rerun lola migrate once the source is reachable"}))
			Expect(d.Recorded).To(HaveLen(1), "only the module that migrates records its paths")
		})

		It("keeps the module's other records at the destination (all or none)", func() {
			writeFile(filepath.Join(projA, ".lola", "project.yml"),
				"version: 1\ntargets: [opencode]\nmodules:\n  - name: gone\n    source: "+url+"/gone.git\n")
			h.install(gone(projA, "claude-code"), gone(projA, "opencode"))
			d := plan().Destinations[0]
			Expect(d.Installs).To(BeEmpty())
			Expect(d.Already).To(BeEmpty(), "the already-migrated record is held too")
			Expect(d.Unmappable).To(HaveLen(2))
			Expect(d.Unmappable[1].Reason).To(HavePrefix("kept with gone for claude-code, which lola-go cannot migrate"))
		})

		It("aborts when the context is cancelled instead of reporting the module unmappable", func() {
			h.install(gone(projA, "claude-code"))
			st, err := migrate.Load(h.dir, userHome)
			Expect(err).NotTo(HaveOccurred())
			cctx, cancel := context.WithCancel(ctx)
			cancel()
			_, err = migrate.NewPlan(cctx, e, st, migrate.Options{})
			Expect(err).To(MatchError(context.Canceled))
		})
	})

	DescribeTable("maps content_dirname to --path",
		func(typ, dirname, want, unmappable string) {
			mono := filepath.Join(tmp, "src", "mono")
			writeTree(mono, reviewModule)
			writeTree(filepath.Join(mono, "plugins", "review"), reviewModule)
			writeTree(mono, map[string]string{"module/commands/other.md": "Other\n"})
			loc := mono
			if typ == "zip" {
				loc = filepath.Join(tmp, "it's here", "mono.zip")
				Expect(os.MkdirAll(filepath.Dir(loc), 0o755)).To(Succeed())
				zipTree(loc, mono)
			}
			fields := map[string]string{"source": loc, "type": typ}
			if dirname != "" {
				fields["content_dirname"] = dirname
			}
			h.source("review", fields)
			h.install(rec{"module": "review", "assistant": "claude-code", "project_path": projA})
			d := plan().Destinations[0]
			if unmappable != "" {
				Expect(d.Installs).To(BeEmpty())
				Expect(d.Unmappable).To(ConsistOf(HaveField("Reason", ContainSubstring(unmappable))))
				Expect(d.Unmappable[0].Hints).To(ContainElement(ContainSubstring(errs.ShellJoin([]string{"lola", "install", loc, "--path"}) + " <dir>")))
				Expect(d.Unmappable[0].Hints).NotTo(ContainElement(ContainSubstring("lola install " + loc)))
				return
			}
			Expect(d.Unmappable).To(BeEmpty())
			Expect(d.Installs).To(ConsistOf(HaveField("Path", want)))
		},
		Entry("a folder's /: the folder itself", "folder", "/", ".", ""),
		Entry("a folder's subdirectory", "folder", "plugins/review", "plugins/review", ""),
		Entry("a folder without one: lola-go's rule (module/, else the root)", "folder", "", "", ""),
		Entry("an archive's /: lola-go's rule", "zip", "/", "", ""),
		Entry("an archive without one", "zip", "", "", ""),
		Entry("an archive's subdirectory, which the Python lola looked up below the module it found", "zip", "plugins/review", "",
			`content_dirname "plugins/review"`),
	)

	It("leaves a record unmappable when lola-go's module root holds no component it lists", func() {
		writeTree(src, map[string]string{"module/commands/other.md": "Other\n"})
		h.install(rec{"module": "review", "assistant": "claude-code", "project_path": projA, "skills": []string{"review"}},
			rec{"module": "review", "assistant": "gemini-cli", "project_path": projA, "commands": []string{"other"}})
		d := plan().Destinations[0]
		Expect(d.Unmappable).To(HaveLen(2))
		Expect(d.Unmappable[0].Assistant).To(Equal("claude-code"))
		Expect(d.Unmappable[0].Reason).To(Equal("lola-go's module root for " + src + " holds no skill review, which the Python lola installed"))
		Expect(d.Unmappable[0].Hints).To(Equal([]string{
			"if the module is in another directory of the source, install it with " + errs.ShellJoin([]string{"lola", "install", src, "--path"}) +
				" <dir>, then rerun lola migrate",
			"if the source no longer ships it, keep the Python lola's files, or install the module with " +
				errs.ShellJoin([]string{"lola", "install", src}) + " and delete them by hand; lola migrate leaves this record and its files as they are",
		}))
		Expect(d.Unmappable[1].Assistant).To(Equal("gemini-cli"), "kept with the record that cannot be migrated")
		Expect(d.Installs).To(BeEmpty())
		Expect(d.Recorded).To(BeEmpty(), "an unmappable record's files are left exactly as they are")
	})

	It("quotes the source for the shell in the install commands it coaches for a record it cannot migrate", func() {
		hostile := filepath.Join(tmp, "it's src", "review")
		writeTree(hostile, reviewModule)
		h.source("review", map[string]string{"source": hostile, "type": "folder"})
		h.install(rec{"module": "review", "assistant": "claude-code", "project_path": projA, "skills": []string{"gone"}})
		d := plan().Destinations[0]
		Expect(d.Unmappable).To(HaveLen(1))
		Expect(d.Unmappable[0].Hints).To(Equal([]string{
			"if the module is in another directory of the source, install it with " + errs.ShellJoin([]string{"lola", "install", hostile, "--path"}) +
				" <dir>, then rerun lola migrate",
			"if the source no longer ships it, keep the Python lola's files, or install the module with " +
				errs.ShellJoin([]string{"lola", "install", hostile}) + " and delete them by hand; lola migrate leaves this record and its files as they are",
		}))
		Expect(d.Unmappable[0].Hints).NotTo(ContainElement(ContainSubstring("lola install " + hostile)))
	})

	It("keeps the record's ref, and its path where it names one, in the install commands it coaches for a record it cannot migrate", func() {
		work, srv := filepath.Join(tmp, "work", "review"), filepath.Join(tmp, "srv")
		writeTree(filepath.Join(work, "plugins", "review"), reviewModule)
		git(work, "init", "-q", "-b", "main")
		git(work, "add", "-A")
		git(work, "commit", "-q", "-m", "init")
		Expect(os.MkdirAll(srv, 0o755)).To(Succeed())
		git(tmp, "clone", "-q", "--bare", work, filepath.Join(srv, "review.git"))
		url := testenv.ServeGit(srv) + "/review.git"
		h.source("review", map[string]string{"source": url, "type": "git", "ref": "main", "content_dirname": "plugins/review"})
		h.install(rec{"module": "review", "assistant": "claude-code", "project_path": projA, "skills": []string{"gone"}})
		d := plan().Destinations[0]
		Expect(d.Unmappable).To(HaveLen(1))
		Expect(d.Unmappable[0].Hints).To(ContainElement("if the module is in another directory of the source, install it with " +
			errs.ShellJoin([]string{"lola", "install", url, "--ref", "main", "--path"}) + " <dir>, then rerun lola migrate"))
		Expect(d.Unmappable[0].Hints).To(ContainElement("if the source no longer ships it, keep the Python lola's files, or install the module with " +
			errs.ShellJoin([]string{"lola", "install", url, "--ref", "main", "--path", "plugins/review"}) +
			" and delete them by hand; lola migrate leaves this record and its files as they are"))
	})

	It("keeps every record of a module at a destination when one of them cannot be migrated", func() {
		h.install(at(projA, "claude-code"), at(projA, "cursor"), at(projA, "opencode"), at(projB, "claude-code"))
		p := plan()
		a, b := p.Destinations[0], p.Destinations[1]
		Expect(a.Installs).To(BeEmpty())
		Expect(a.Recorded).To(BeEmpty())
		Expect(a.Targets).To(BeEmpty())
		Expect(a.Unmappable).To(HaveLen(3))
		for _, u := range a.Unmappable {
			if u.Assistant == "cursor" {
				continue
			}
			Expect(u.Reason).To(Equal("kept with review for cursor, which lola-go cannot migrate: they share the Python lola's copy of review"), u.Assistant)
			Expect(u.Hints).NotTo(BeEmpty())
		}
		Expect(b.Installs).To(HaveLen(1), "another destination migrates on its own")
	})

	It("matches a skill the Python lola renamed with the module's name as prefix to its own name", func() {
		h.install(rec{"module": "review", "assistant": "claude-code", "project_path": projA, "skills": []string{"review_review"},
			"commands": []string{"ship"}, "agents": []string{"critic"}, "mcps": []string{"lola-gh"}})
		d := plan().Destinations[0]
		Expect(d.Unmappable).To(BeEmpty())
		Expect(d.Installs).To(HaveLen(1))
	})

	It("lists the paths each assistant recorded at project scope (§6)", func() {
		h.install(rec{"module": "review", "assistant": "claude-code", "project_path": projA, "skills": []string{"review"},
			"commands": []string{"ship"}, "agents": []string{"critic"}, "mcps": []string{"lola-gh"}},
			rec{"module": "review", "assistant": "gemini-cli", "project_path": projA, "skills": []string{"review"},
				"commands": []string{"ship"}, "mcps": []string{"lola-gh"}},
			rec{"module": "review", "assistant": "opencode", "project_path": projA, "skills": []string{"review"},
				"commands": []string{"ship"}, "agents": []string{"critic"}, "mcps": []string{"lola-gh"}})
		d := plan().Destinations[0]
		var got []string
		for _, r := range d.Recorded {
			Expect(r.Root).To(Equal(projA))
			s := r.Target + " " + r.Kind + " " + r.Rel
			if r.Dir {
				s += "/"
			}
			if r.Key != "" {
				s += " " + r.Key
			}
			got = append(got, s)
		}
		Expect(got).To(ConsistOf(
			"claude-code skill .claude/skills/review/",
			"claude-code command .claude/commands/ship.md",
			"claude-code command .claude/commands/ship/",
			"claude-code agent .claude/agents/critic.md",
			"claude-code mcp .mcp.json /mcpServers/lola-gh",
			"gemini-cli command .gemini/commands/ship.toml",
			"gemini-cli mcp .gemini/settings.json /mcpServers/lola-gh",
			"opencode skill .opencode/skills/review/",
			"opencode command .opencode/commands/ship.md",
			"opencode command .opencode/commands/ship/",
			"opencode command .opencode/command/ship.md",
			"opencode command .opencode/command/review.ship.md",
			"opencode agent .opencode/agents/critic.md",
			"opencode agent .opencode/agent/critic.md",
			"opencode agent .opencode/agent/review.critic.md",
			"opencode mcp opencode.json /mcp/lola-gh",
			" module-copy .lola/modules/review/",
		))
	})

	It("uses the user-scope roots: the home directory, and OpenCode's config directory without .opencode/", func() {
		h.install(rec{"module": "review", "assistant": "claude-code", "scope": "user", "skills": []string{"review"}, "mcps": []string{"lola-gh"}},
			rec{"module": "review", "assistant": "opencode", "scope": "user", "agents": []string{"critic"}})
		d := plan().Destinations[0]
		var got []string
		for _, r := range d.Recorded {
			got = append(got, r.Abs()+" "+r.Key)
		}
		Expect(got).To(ConsistOf(
			filepath.Join(userHome, ".claude/skills/review")+" ",
			filepath.Join(userHome, ".mcp.json")+" /mcpServers/lola-gh",
			filepath.Join(tmp, "config/opencode/agents/critic.md")+" ",
			filepath.Join(tmp, "config/opencode/agent/critic.md")+" ",
			filepath.Join(tmp, "config/opencode/agent/review.critic.md")+" ",
		), "no module copy at user scope: the Python lola does not record where it was")
	})

	It("reports what is never migrated, each with its file and marker or path and a hint (§5)", func() {
		writeFile(filepath.Join(projA, ".lola-req"), "review>=1\n")
		Expect(os.MkdirAll(filepath.Join(projA, ".lola", "plugin-data", "review"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(h.dir, "market"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(h.dir, "plugin-data", "review"), 0o755)).To(Succeed())
		h.install(rec{"module": "review", "assistant": "claude-code", "project_path": projA, "has_instructions": true},
			rec{"module": "review", "assistant": "gemini-cli", "project_path": projA, "skills": []string{"review"}, "append_context": "AGENTS.md"},
			rec{"module": "review", "assistant": "claude-code", "scope": "user", "has_instructions": true},
			rec{"module": "review", "assistant": "gemini-cli", "scope": "user", "has_instructions": true},
			rec{"module": "review", "assistant": "opencode", "scope": "user", "has_instructions": true})
		p := plan()
		Expect(p.NotMigrated).To(BeEmpty(), "marketplaces are registered (Plan 6c §6.9), and an empty market/ holds none")
		Expect(p.Markets).To(BeEmpty())
		type shown struct{ What, Path, Marker string }
		items := func(d *migrate.Destination) []shown {
			var out []shown
			for _, it := range d.NotMigrated {
				Expect(it.Hints).NotTo(BeEmpty())
				out = append(out, shown{it.What, it.Path, it.Marker})
			}
			return out
		}
		block := "<!-- lola:module:review:start -->"
		Expect(items(p.Destinations[0])).To(ConsistOf(
			shown{migrate.ItemInstructions, filepath.Join(projA, "CLAUDE.md"), block},
			shown{migrate.ItemInstructions, filepath.Join(projA, "GEMINI.md"), block},
			shown{migrate.ItemGeminiSkills, filepath.Join(projA, "GEMINI.md"), "<!-- lola:skills:start --> ### review"},
			shown{migrate.ItemPluginData, filepath.Join(projA, ".lola", "plugin-data", "review"), ""},
			shown{migrate.ItemLolaReq, filepath.Join(projA, ".lola-req"), ""},
		))
		Expect(items(p.Destinations[1])).To(ConsistOf(
			shown{migrate.ItemInstructions, filepath.Join(userHome, ".claude", "CLAUDE.md"), block},
			shown{migrate.ItemInstructions, filepath.Join(userHome, "GEMINI.md"), block},
			shown{migrate.ItemInstructions, filepath.Join(tmp, "config", "opencode", "AGENTS.md"), block},
			shown{migrate.ItemPluginData, filepath.Join(h.dir, "plugin-data", "review"), ""},
		))
	})

	Context("project paths another machine had (Plan 10 §3.1)", func() {
		It("looks for a missing project path at the same place below a --map-root's new root, and installs there", func() {
			h.install(at(filepath.Join(tmp, "old", "a-proj"), "claude-code"))
			p := planWith(migrate.Options{Roots: []migrate.RootMap{{Old: filepath.Join(tmp, "old"), New: tmp}}})
			Expect(p.Unmappable).To(BeEmpty())
			Expect(p.RootSuggestions).To(BeEmpty(), "a moved path is never suggested again")
			Expect(p.Destinations).To(HaveLen(1))
			Expect(p.Destinations[0].Root).To(Equal(projA))
			Expect(p.Destinations[0].Installs).To(HaveLen(1))
		})

		It("leaves a moved path that is still missing unmappable, naming where it looked", func() {
			h.install(at(filepath.Join(tmp, "old", "a-proj"), "claude-code"))
			p := planWith(migrate.Options{Roots: []migrate.RootMap{{Old: filepath.Join(tmp, "old"), New: filepath.Join(tmp, "nowhere")}}})
			Expect(p.Destinations).To(BeEmpty())
			Expect(p.Unmappable).To(HaveLen(1))
			Expect(p.Unmappable[0].Reason).To(Equal("project_path " + filepath.Join(tmp, "nowhere", "a-proj") + " no longer exists"))
			Expect(p.Unmappable[0].ProjectPath).To(Equal(filepath.Join(tmp, "nowhere", "a-proj")))
			Expect(p.Unmappable[0].Hints).To(ConsistOf(HavePrefix("rerun lola migrate with --map-root <old root>=<where it is on this machine>;")),
				"no root to offer for a path a map moved")
			Expect(p.RootSuggestions).To(BeEmpty(), "a moved path is never suggested again")
		})

		It("applies the longest --map-root that matches, and only at a whole name", func() {
			h.install(at(filepath.Join(tmp, "old", "deep", "a-proj"), "claude-code"), at(filepath.Join(tmp, "older", "b-proj"), "claude-code"))
			p := planWith(migrate.Options{Roots: []migrate.RootMap{{Old: filepath.Join(tmp, "old"), New: filepath.Join(tmp, "nowhere")},
				{Old: filepath.Join(tmp, "old", "deep"), New: tmp}}})
			Expect(p.Destinations).To(HaveLen(1))
			Expect(p.Destinations[0].Root).To(Equal(projA))
			Expect(p.Unmappable).To(HaveLen(1))
			Expect(p.Unmappable[0].ProjectPath).To(Equal(filepath.Join(tmp, "older", "b-proj")), "old is not a name of older")
		})

		It("suggests each missing root once, with your home as the new root, and hints --map-root on each record", func() {
			h.install(at("/Users/alice/src/a", "claude-code"), at("/Users/alice/b", "opencode"),
				at(filepath.Join(tmp, "gone", "deeper", "c"), "claude-code"), at(`C:\Users\bob\d`, "claude-code"))
			p := plan()
			got := map[string]int{}
			for _, s := range p.RootSuggestions {
				Expect(s.New).To(Equal(userHome))
				got[s.Old] = len(s.Records)
			}
			Expect(got).To(Equal(map[string]int{"/Users/alice": 2, filepath.Join(tmp, "gone"): 1, `C:\Users\bob`: 1}))
			Expect(p.RootSuggestions).To(HaveLen(3))
			Expect(p.Unmappable).To(HaveLen(4))
			hints := map[string][]string{}
			for _, u := range p.Unmappable {
				hints[u.ProjectPath] = u.Hints
			}
			hint := func(flag string) []string {
				return []string{"rerun lola migrate with --map-root " + flag + "; until then it leaves this record and its files as they are"}
			}
			Expect(hints).To(Equal(map[string][]string{
				"/Users/alice/src/a":                      hint("/Users/alice=" + userHome),
				"/Users/alice/b":                          hint("/Users/alice=" + userHome),
				filepath.Join(tmp, "gone", "deeper", "c"): hint(filepath.Join(tmp, "gone") + "=" + userHome),
				`C:\Users\bob\d`:                          hint(`'C:\Users\bob=` + userHome + "'"),
			}), "each record's hint is the --map-root its root needs, with your home as the new root")
		})

		It("moves a local source missing here by the same --map-root: one answer per old root covers paths and sources", func() {
			kit := filepath.Join(userHome, "src", "kit")
			writeTree(kit, map[string]string{"skills/lint/SKILL.md": "---\ndescription: Lint\n---\nLint.\n"})
			h.source("kit", map[string]string{"source": "/Users/alice/src/kit", "type": "folder"})
			moved := filepath.Join(userHome, "proj")
			Expect(os.MkdirAll(moved, 0o755)).To(Succeed())
			h.install(rec{"module": "kit", "assistant": "claude-code", "scope": "user", "skills": []string{"lint"}}, at("/Users/alice/proj", "claude-code"))
			p := plan()
			Expect(p.RootSuggestions).To(HaveLen(1))
			Expect(p.RootSuggestions[0].Old).To(Equal("/Users/alice"))
			Expect(p.RootSuggestions[0].Records).To(HaveLen(2), "the project record and the record whose source is there")
			Expect(p.Destinations[0].Unmappable).To(ConsistOf(HaveField("Reason", "the local source /Users/alice/src/kit no longer exists")))
			Expect(p.Destinations[0].Unmappable[0].Hints).To(Equal([]string{"restore it there, rerun lola migrate with --map-root /Users/alice=" + userHome +
				", or install the module from another source with lola install"}))

			p = planWith(migrate.Options{Roots: []migrate.RootMap{{Old: "/Users/alice", New: userHome}}})
			Expect(p.RootSuggestions).To(BeEmpty())
			Expect(p.Unmappable).To(BeEmpty())
			Expect(p.Warnings).To(BeEmpty())
			Expect(p.Destinations).To(HaveLen(2))
			Expect(p.Destinations[0].Root).To(Equal(moved))
			Expect(p.Destinations[1].Installs).To(ConsistOf(HaveField("Source", kit)))
		})

		It("leaves a moved source that is still missing unmappable, naming where it looked; another OS's source path is missing", func() {
			h.source("kit", map[string]string{"source": `C:\Users\alice\src\kit`, "type": "folder"})
			h.install(rec{"module": "kit", "assistant": "claude-code", "scope": "user"})
			p := plan()
			Expect(p.Destinations[0].Unmappable).To(ConsistOf(HaveField("Reason", `the local source C:\Users\alice\src\kit no longer exists`)))
			Expect(p.RootSuggestions).To(ConsistOf(HaveField("Old", `C:\Users\alice`)))

			p = planWith(migrate.Options{Roots: []migrate.RootMap{{Old: `C:\Users\alice`, New: filepath.Join(tmp, "nowhere")}}})
			Expect(p.Destinations[0].Unmappable).To(ConsistOf(HaveField("Reason", "the local source "+filepath.Join(tmp, "nowhere", "src", "kit")+" no longer exists")))
			Expect(p.Destinations[0].Unmappable[0].Hints).To(ConsistOf(ContainSubstring("--map-root <old root>=<where it is on this machine>,")),
				"no root to offer for a source a map moved")
			Expect(p.RootSuggestions).To(BeEmpty())
		})

		It("does not move a path through .., and does not suggest a root for it", func() {
			h.install(at("/Users/alice/../../etc/kit", "claude-code"))
			p := planWith(migrate.Options{Roots: []migrate.RootMap{{Old: "/Users/alice", New: tmp}}})
			Expect(p.Destinations).To(BeEmpty())
			Expect(p.Unmappable).To(ConsistOf(HaveField("Reason", "project_path /Users/alice/../../etc/kit is not in its clean form")))
			Expect(p.RootSuggestions).To(BeEmpty())
		})

		It("suggests no root that --map-root would refuse for moving every path, such as a project at a drive root", func() {
			h.install(at(`E:\`, "claude-code"), at("/", "claude-code"))
			p := plan()
			Expect(p.RootSuggestions).To(BeEmpty(), "a hint must be a command that works")
			Expect(p.Unmappable).To(HaveLen(2), "the records stay unmappable, with their reason")
			Expect(p.Unmappable[0].ProjectPath).To(Equal(`E:\`))
			Expect(p.Unmappable[0].Hints).To(Equal([]string{"rerun lola migrate with --map-root <old root>=<where it is on this machine>; " +
				"until then it leaves this record and its files as they are"}), "no root to offer, so the hint names what to write")
		})

		It("reports a local source with a .. name as not in its clean form, and suggests no root for it", func() {
			h.source("win", map[string]string{"source": `C:\Users\alice\..\..\kit`, "type": "folder"})
			h.source("nat", map[string]string{"source": filepath.Join(tmp, "nowhere") + "/../kit", "type": "folder"})
			h.install(rec{"module": "win", "assistant": "claude-code", "scope": "user"}, rec{"module": "nat", "assistant": "claude-code", "scope": "user"})
			p := planWith(migrate.Options{Roots: []migrate.RootMap{{Old: tmp, New: userHome}}})
			Expect(p.Destinations[0].Unmappable).To(ConsistOf(
				HaveField("Reason", `the local source C:\Users\alice\..\..\kit is not in its clean form`),
				HaveField("Reason", "the local source "+filepath.Join(tmp, "nowhere")+"/../kit is not in its clean form")))
			Expect(p.Destinations[0].Unmappable).To(HaveEach(HaveField("Hints", ConsistOf(ContainSubstring("lola install <source>")))))
			Expect(p.RootSuggestions).To(BeEmpty())
		})

		It("matches a Windows --map-root whatever its drive letter's case, and does not call a shadowed map unused", func() {
			h.install(at(`C:\Users\alice\a-proj`, "claude-code"))
			p := planWith(migrate.Options{Roots: []migrate.RootMap{{Old: `c:\Users`, New: filepath.Join(tmp, "nowhere")}, {Old: "C:/Users/alice", New: tmp}}})
			Expect(p.Warnings).To(BeEmpty())
			Expect(p.Unmappable).To(BeEmpty())
			Expect(p.Destinations).To(HaveLen(1))
			Expect(p.Destinations[0].Root).To(Equal(filepath.Join(tmp, "a-proj")))
		})

		It("refuses a map that moves a path onto your home", func() {
			h.install(at("/Users/alice", "claude-code"))
			p := planWith(migrate.Options{Roots: []migrate.RootMap{{Old: "/Users/alice", New: userHome}}})
			Expect(p.Destinations).To(BeEmpty())
			Expect(p.Unmappable).To(ConsistOf(HaveField("Reason", "project_path "+userHome+" is your home directory or one of its parents")))
		})

		It("warns of a --map-root that matches no missing project path or source, and plans as without it", func() {
			h.install(at(projA, "claude-code"))
			p := planWith(migrate.Options{Roots: []migrate.RootMap{{Old: "/Users/bob", New: tmp}}})
			Expect(p.Warnings).To(ConsistOf("--map-root /Users/bob=" + tmp + " matches no missing project path or source of the Python lola; it changes nothing"))
			Expect(p.Destinations[0].Installs).To(HaveLen(1))
		})
	})

	Context("assistants this machine lacks (Plan 10 §3.2)", func() {
		BeforeEach(func() { Expect(os.Remove(filepath.Join(tmp, "bin", "claude"))).To(Succeed()) })

		It("leaves a record whose assistant is neither on your PATH nor set up where it goes, and suggests those on your PATH", func() {
			h.install(at(projA, "claude-code"), at(projB, "claude-code"))
			p := plan()
			Expect(p.Destinations).To(HaveLen(2))
			for _, d := range p.Destinations {
				Expect(d.Installs).To(BeEmpty())
				Expect(d.Unmappable).To(ConsistOf(HaveField("Reason", "claude-code is not installed here")))
				Expect(d.Unmappable[0].Hints).To(Equal([]string{"install claude-code and rerun lola migrate, or rerun it with --map-target claude-code=gemini-cli,opencode " +
					"to install for one you have (claude-code=claude-code installs for claude-code anyway)"}))
			}
			Expect(p.TargetSuggestions).To(HaveLen(1))
			Expect(p.TargetSuggestions[0].Old).To(Equal("claude-code"))
			Expect(p.TargetSuggestions[0].Detected).To(Equal([]string{"gemini-cli", "opencode"}))
			Expect(p.TargetSuggestions[0].Records).To(HaveLen(2))
		})

		It("names no assistant in its hints when none is on your PATH", func() {
			for _, name := range []string{"gemini", "opencode"} {
				Expect(os.Remove(filepath.Join(tmp, "bin", name))).To(Succeed())
			}
			h.install(at(projA, "claude-code"), at(projB, "cursor"))
			p := plan()
			Expect(p.Destinations).To(HaveLen(2))
			Expect(p.Destinations[0].Unmappable[0].Hints).To(Equal([]string{"install claude-code and rerun lola migrate, or rerun it with --map-target claude-code=<assistant> " +
				"to install for one you have (claude-code=claude-code installs for claude-code anyway)"}))
			Expect(p.Destinations[1].Unmappable[0].Hints).To(ContainElement("to install its modules for an assistant you have instead, rerun lola migrate with --map-target cursor=<assistant>"))
		})

		It("installs for an assistant set up where the record goes, though it is not on your PATH, as lola tidy counts it", func() {
			Expect(os.MkdirAll(filepath.Join(projA, ".claude"), 0o755)).To(Succeed())
			h.install(at(projA, "claude-code"))
			p := plan()
			Expect(p.Destinations[0].Installs).To(HaveLen(1))
			Expect(p.TargetSuggestions).To(BeEmpty())
		})

		It("installs a missing assistant's records for the targets --map-target names, recording none of its paths", func() {
			h.install(at(projA, "claude-code"), rec{"module": "review", "assistant": "claude-code", "scope": "user", "has_instructions": true})
			p := planWith(migrate.Options{Targets: map[string][]string{"claude-code": {"opencode", "gemini-cli"}}})
			Expect(p.TargetSuggestions).To(BeEmpty())
			Expect(p.Destinations).To(HaveLen(2))
			for _, d := range p.Destinations {
				Expect(unpinned(d.Installs)).To(Equal([]migrate.Install{{PythonModules: []string{"review"}, Kind: source.KindFolder, Source: src,
					Targets: []string{"gemini-cli", "opencode"}}}), "one install, for every target the map names, in registry order")
				Expect(d.NotMigrated).NotTo(ContainElement(HaveField("What", migrate.ItemInstructions)), "the Python lola's claude-code files are not here")
			}
			Expect(p.Destinations[0].Recorded).To(ConsistOf(HaveField("Kind", migrate.KindModuleCopy)), "only the module copy, which the project has")
			Expect(p.Destinations[1].Recorded).To(BeEmpty(), "the Python lola wrote nothing for gemini-cli or opencode")
		})

		It("maps an assistant lola has no target for, whose hint names --map-target", func() {
			h.install(at(projA, "cursor"))
			p := plan()
			Expect(p.TargetSuggestions).To(ConsistOf(HaveField("Old", "cursor")))
			Expect(p.Destinations[0].Unmappable[0].Hints).To(ContainElement("to install its modules for an assistant you have instead, rerun lola migrate with --map-target cursor=gemini-cli,opencode"))

			p = planWith(migrate.Options{Targets: map[string][]string{"cursor": {"opencode"}}})
			Expect(p.Destinations[0].Installs).To(HaveLen(1))
			Expect(p.Destinations[0].Installs[0].Targets).To(Equal([]string{"opencode"}))
		})

		It("warns of a --map-target that matches no record whose assistant is missing here", func() {
			Expect(os.MkdirAll(filepath.Join(projA, ".claude"), 0o755)).To(Succeed())
			h.install(at(projA, "claude-code"))
			p := planWith(migrate.Options{Targets: map[string][]string{"claude-code": {"opencode"}, "cursor": {"opencode"}}})
			Expect(p.Warnings).To(ConsistOf(
				"--map-target claude-code=opencode matches no record whose assistant is missing here; it changes nothing",
				"--map-target cursor=opencode matches no record whose assistant is missing here; it changes nothing"))
			Expect(p.Destinations[0].Installs[0].Targets).To(Equal([]string{"claude-code"}), "set up here, so the map does not apply")
		})

		It("reports a record mapped to two targets once when it cannot migrate", func() {
			h.install(rec{"module": "review", "assistant": "claude-code", "scope": "project", "project_path": projA, "agents": []string{"gone"}})
			p := planWith(migrate.Options{Targets: map[string][]string{"claude-code": {"gemini-cli", "opencode"}}})
			Expect(p.Destinations[0].Installs).To(BeEmpty())
			Expect(p.Destinations[0].Unmappable).To(ConsistOf(HaveField("Reason", ContainSubstring("holds no agent gone"))))
		})
	})
})
