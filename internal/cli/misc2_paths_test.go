package cli_test

// One spec per row of the init, market add, migrate and hook and script consent transition tables
// in docs/architecture/tui-flows.md, named "init: <from> --<input>--> <to>", "market add: …",
// "migrate: …" and "consent: …", the input followed by the row's guard in parentheses where rows
// share a from and an input. Rows an existing spec pins already:
//
// also pinned (init: start --run (no terminal)--> refused): confirm_test.go "off a terminal, refuses with the plan and a hint that keeps the run's flags, writing nothing"
// also pinned (init: start --run (an invalid name)--> refused): confirm_test.go "refuses an invalid name before asking anything"
// also pinned (init: confirm --yes--> done): confirm_test.go "on a terminal, shows the plan, asks, and scaffolds when you accept"
// also pinned (init: confirm --no--> cancelled): confirm_test.go "on a terminal, declining writes nothing and exits 0"
// also pinned (market add: start --run (the catalog's own name cannot name a marketplace)--> name): market_test.go "asks a name on a terminal for a catalog whose own name no marketplace can have, pre-filled with its slug"
// also pinned (market add: name --esc--> name): keys_internal_test.go "Esc on the marketplace name question stays: it is a first screen"
// also pinned (migrate: start --run (no terminal)--> refused): migrate_test.go "needs -y off a terminal, printing the plan and writing nothing"
// also pinned (migrate: root --answer--> next step; migrate: targets --answer--> next step): migrate_test.go "asks where each missing root is and which assistants stand in for each missing one, then migrates with the answers"
// also pinned (migrate: root --esc (first screen)--> root; migrate: targets --esc (the first, after a root question)--> root): migrate_test.go "goes back from an assistant question to the root question on Esc, the root answered pre-filled"
// also pinned (migrate: confirm --yes--> done): confirm_test.go "c4: on a terminal without --json, <cmd> asks after the plan and prints the human result"
// also pinned (migrate: confirm --no--> cancelled): confirm_test.go "c5: declining <cmd> on a terminal writes nothing and exits 0"
// also pinned (migrate: consent --yes--> done): misc2_paths_test.go "migrate: confirm --yes (a hook or script without your consent)--> consent"
// also pinned (consent: start --run (--json or no terminal, post-install only)--> done): scripts_test.go "an untrusted project pre script refuses off a terminal with --trust=scripts; post is declined and pending"
// also pinned (consent: hooks --esc--> hooks; consent: runtime --esc--> runtime; consent: script --esc--> script; consent: later --esc--> later): keys_paths_internal_test.go "viewer: inline --esc (no screen before)--> inline"
// also pinned (consent: hooks --q--> cancelled; consent: runtime --q--> cancelled; consent: script --q--> cancelled; consent: later --q--> cancelled): keys_paths_internal_test.go "viewer: inline --q (fewer than two answers)--> cancelled"
// also pinned (consent: later --ctrl+c--> cancelled): misc2_paths_test.go "consent: start --run (lola sync, nothing to install)--> later"

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

var _ = Describe("market add paths", func() {
	var tmp, acme, priv string
	var p paths.Paths
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		p, err = paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		GinkgoT().Chdir(tmp)
		acme, priv = filepath.Join(tmp, "acme"), filepath.Join(tmp, "priv")
		writeTree(acme, map[string]string{".claude-plugin/marketplace.json": marketCatalog, "tools/formatter/x.md": "x"})
		writeTree(priv, map[string]string{".claude-plugin/marketplace.json": sshMarketCatalog})
	})

	// markets is markets.yaml as written.
	markets := func() string {
		GinkgoHelper()
		b, err := os.ReadFile(p.Markets)
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}

	It("market add: start --run--> done", func() {
		sp := &scripted{}
		r := lolaP(sp, "market", "--add", acme)
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(BeEmpty(), "a free name and no ssh host: nothing asked")
		Expect(r.out).To(HavePrefix("✓ marketplace acme: 2 entries (Claude marketplace.json) from " + acme + "\n"))
		Expect(markets()).To(ContainSubstring("- name: acme\n"))
		Expect(filepath.Join(p.MarketCache, "acme")).To(BeADirectory())
	})

	It("market add: start --run (the name is another source's)--> name", func() {
		Expect(lola("market", "priv", "--add", acme).code).To(Equal(0))
		before := markets()
		sp := &scripted{names: []string{"mine"}, sshHosts: []func(cli.SSHHostStep) ([]string, error){aborted[cli.SSHHostStep, []string]}}
		r := lolaP(sp, "market", "--add", priv)
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"market-name", "ssh-hosts"}))
		Expect(sp.nameSteps[0]).To(Equal(cli.MarketNameStep{Why: "priv is already a marketplace, from " + acme, Value: "priv-2"}))
		Expect(markets()).To(Equal(before))
	})

	It("market add: start --run (the name is this marketplace already)--> refused", func() {
		Expect(lola("market", "--add", acme).code).To(Equal(0))
		before := markets()
		sp := &scripted{}
		r := lolaP(sp, "market", "acme", "--add", acme)
		Expect(r.code).To(Equal(1))
		Expect(sp.calls).To(BeEmpty())
		Expect(r.err).To(Equal("error: acme is already this marketplace\n  hint: lola market acme --update\n"))
		Expect(markets()).To(Equal(before))
	})

	It("market add: start --run (ssh hosts not allowed)--> ssh", func() {
		sp := &scripted{sshHosts: []func(cli.SSHHostStep) ([]string, error){aborted[cli.SSHHostStep, []string]}}
		r := lolaP(sp, "market", "--add", priv)
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"ssh-hosts"}))
		Expect(sp.sshSteps[0].Market).To(Equal("priv"))
		Expect(sp.sshSteps[0].Hosts).To(Equal([]cli.SSHHostChoice{{Host: "git.corp.test", Entries: 2}, {Host: "other.test", Entries: 1}}))
		Expect(p.Markets).NotTo(BeAnExistingFile())
	})

	It("market add: start --run (--ssh-host)--> ssh", func() {
		sp := &scripted{sshHosts: []func(cli.SSHHostStep) ([]string, error){func(cli.SSHHostStep) ([]string, error) { return nil, nil }}}
		r := lolaP(sp, "market", "--add", priv, "--ssh-host", "git.corp.test")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.sshSteps[0].Hosts).To(Equal([]cli.SSHHostChoice{{Host: "other.test", Entries: 1}}), "only the hosts --ssh-host left")
		Expect(markets()).To(ContainSubstring("ssh_hosts:\n      - git.corp.test\n"))
		sp = &scripted{}
		r = lolaP(sp, "market", "both", "--add", priv, "--ssh-host", "git.corp.test", "--ssh-host", "other.test")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(BeEmpty(), "nothing left to ask")
	})

	It("market add: start --run (no terminal or --json, a taken name)--> refused", func() {
		Expect(lola("market", "priv", "--add", acme).code).To(Equal(0))
		before := markets()
		sp := &scripted{}
		r := lolaP(sp, "market", "--add", priv, "--json")
		Expect(r.code).To(Equal(1))
		Expect(sp.calls).To(BeEmpty())
		Expect(r.err).To(MatchJSON(`{"error": "priv is already a marketplace, from ` + acme + `",
			"hints": ["lola market priv-2 --add ` + priv + ` --path .claude-plugin/marketplace.json --json"]}`))
		Expect(markets()).To(Equal(before))
	})

	It("market add: start --run (no terminal or --json, ssh hosts)--> done", func() {
		sp := &scripted{}
		r := lolaP(sp, "market", "--add", priv, "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(BeEmpty())
		Expect(r.out).To(ContainSubstring("lola market priv --update --ssh-host git.corp.test --ssh-host other.test --json"))
		Expect(markets()).NotTo(ContainSubstring("ssh_hosts"))
	})

	It("market add: start --run (-n)--> done", func() {
		Expect(lola("market", "priv", "--add", acme).code).To(Equal(0))
		before := markets()
		sp := &scripted{}
		r := lolaP(sp, "market", "other", "--add", priv, "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(BeEmpty(), "a dry run asks nothing")
		Expect(r.out).To(HavePrefix("→ would add marketplace other: "))
		r = lolaP(sp, "market", "--add", priv, "-n")
		Expect(r.code).To(Equal(1), "a taken name is refused as off a terminal")
		Expect(sp.calls).To(BeEmpty())
		Expect(markets()).To(Equal(before))
		Expect(filepath.Join(p.MarketCache, "other")).NotTo(BeAnExistingFile())
	})

	It("market add: name --answer--> ssh", func() {
		Expect(lola("market", "priv", "--add", acme).code).To(Equal(0))
		sp := &scripted{names: []string{"mine"}, sshHosts: []func(cli.SSHHostStep) ([]string, error){aborted[cli.SSHHostStep, []string]}}
		Expect(lolaP(sp, "market", "--add", priv).code).To(Equal(0))
		Expect(sp.calls).To(Equal([]string{"market-name", "ssh-hosts"}))
		Expect(sp.sshSteps[0].Market).To(Equal("mine"))
	})

	It("market add: name --answer (no ssh host to ask)--> done", func() {
		Expect(lola("market", "--add", acme).code).To(Equal(0))
		other := filepath.Join(tmp, "other")
		writeTree(other, map[string]string{".claude-plugin/marketplace.json": marketCatalog, "tools/formatter/x.md": "x"})
		sp := &scripted{names: []string{"mine"}}
		r := lolaP(sp, "market", "--add", other)
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"market-name"}))
		Expect(r.out).To(ContainSubstring("→ the catalog calls itself acme; it is your marketplace mine\n"))
		Expect(markets()).To(ContainSubstring("- name: mine\n"))
	})

	It("market add: name --ctrl+c--> cancelled", func() {
		Expect(lola("market", "priv", "--add", acme).code).To(Equal(0))
		before := markets()
		sp := &scripted{names: []string{cancelRun}}
		r := lolaP(sp, "market", "--add", priv)
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"market-name"}))
		Expect(markets()).To(Equal(before))
		Expect(filepath.Join(p.MarketCache, "priv-2")).NotTo(BeAnExistingFile())
	})

	It("market add: ssh --answer--> done", func() {
		sp := &scripted{sshHosts: []func(cli.SSHHostStep) ([]string, error){func(cli.SSHHostStep) ([]string, error) { return []string{"other.test"}, nil }}}
		r := lolaP(sp, "market", "--add", priv)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ allowed ssh hosts: other.test\n"))
		Expect(markets()).To(ContainSubstring("ssh_hosts:\n      - other.test\n"))
		Expect(markets()).NotTo(ContainSubstring("git.corp.test"))
	})

	It("market add: ssh --empty submit--> done", func() {
		sp := &scripted{sshHosts: []func(cli.SSHHostStep) ([]string, error){func(cli.SSHHostStep) ([]string, error) { return nil, nil }}}
		r := lolaP(sp, "market", "--add", priv)
		Expect(r.code).To(Equal(0), r.err)
		Expect(markets()).To(ContainSubstring("- name: priv\n"))
		Expect(markets()).NotTo(ContainSubstring("ssh_hosts"), "no host allowed")
		Expect(r.out + r.err).To(ContainSubstring("lola market priv --update --ssh-host git.corp.test --ssh-host other.test"))
	})

	It("market add: ssh --ctrl+c--> cancelled", func() {
		Expect(lola("market", "priv", "--add", acme).code).To(Equal(0))
		before := markets()
		sp := &scripted{names: []string{"mine"}, sshHosts: []func(cli.SSHHostStep) ([]string, error){aborted[cli.SSHHostStep, []string]}}
		r := lolaP(sp, "market", "--add", priv)
		Expect(r.code).To(Equal(0), r.err)
		Expect(markets()).To(Equal(before), "the name answered is not registered")
		Expect(filepath.Join(p.MarketCache, "mine")).NotTo(BeAnExistingFile())
	})

	It("market add: name --answer (refused)--> name", func() {
		Expect(lola("market", "priv", "--add", acme).code).To(Equal(0))
		before := markets()
		sp := &scripted{names: []string{"Bad Name", "priv", "mine"}, sshHosts: []func(cli.SSHHostStep) ([]string, error){aborted[cli.SSHHostStep, []string]}}
		r := lolaP(sp, "market", "--add", priv)
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"market-name", "market-name", "market-name", "ssh-hosts"}))
		why := "priv is already a marketplace, from " + acme
		Expect(sp.nameSteps[0]).To(Equal(cli.MarketNameStep{Why: why, Value: "priv-2"}))
		Expect(sp.nameSteps[1].Value).To(Equal("Bad Name"), "what was typed is kept")
		Expect(sp.nameSteps[1].Problem).To(ContainSubstring("Bad Name"), "the note says why it was refused")
		Expect(sp.nameSteps[2]).To(Equal(cli.MarketNameStep{Why: why, Value: "priv", Problem: why}))
		Expect(markets()).To(Equal(before), "nothing is written before the last question is answered")
	})

	It("market add: ssh --esc (after the name question)--> name", func() {
		Expect(lola("market", "priv", "--add", acme).code).To(Equal(0))
		before := markets()
		sp := &scripted{names: []string{"mine", "theirs"}, sshHosts: []func(cli.SSHHostStep) ([]string, error){goneBack[cli.SSHHostStep, []string],
			func(cli.SSHHostStep) ([]string, error) { return []string{"git.corp.test"}, nil }}}
		r := lolaP(sp, "market", "--add", priv)
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"market-name", "ssh-hosts", "market-name", "ssh-hosts"}))
		Expect(sp.sshSteps[0].Nav).To(Equal(cli.Nav{Back: true, Answered: 1}), "Esc has the name question to go back to")
		Expect(sp.nameSteps[1]).To(Equal(cli.MarketNameStep{Why: "priv is already a marketplace, from " + acme, Value: "mine"}), "the typed name kept")
		Expect(sp.sshSteps[1].Market).To(Equal("theirs"))
		Expect(markets()).NotTo(ContainSubstring("mine"))
		Expect(markets()).To(ContainSubstring("- name: theirs\n"))
		Expect(markets()).To(ContainSubstring("ssh_hosts:\n      - git.corp.test\n"))
		Expect(markets()).To(HavePrefix(before[:len(before)-1]), "priv kept as it was")
	})

	It("market add: ssh --esc (first screen)--> ssh", func() {
		sp := &scripted{sshHosts: []func(cli.SSHHostStep) ([]string, error){aborted[cli.SSHHostStep, []string]}}
		r := lolaP(sp, "market", "--add", priv)
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(Equal([]string{"ssh-hosts"}))
		Expect(sp.sshSteps[0].Nav).To(Equal(cli.Nav{}), "a first screen: Esc stays")
		Expect(p.Markets).NotTo(BeAnExistingFile())
	})
})

var _ = Describe("migrate paths", func() {
	var tmp, home, proj, src, moved string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		home, proj, src = filepath.Join(tmp, "home", ".lola"), filepath.Join(tmp, "proj"), filepath.Join(tmp, "src", "review")
		writeTree(src, map[string]string{"commands/ship.md": "Ship $ARGUMENTS\n"})
		writeTree(home, map[string]string{
			"installed.yml": "version: '1.0'\ninstallations:\n- {module: review, assistant: claude-code, scope: project, project_path: " + proj +
				", commands: [ship]}\n",
			"modules/review/.lola/source.yml": "source: " + src + "\ntype: folder\n",
		})
		writeTree(proj, map[string]string{".claude/commands/ship.md": "python ship\n"})
		moved = filepath.Join(tmp, "home", "proj")
	})

	// fromAnotherMachine records the project at /Users/alice/proj, which is here at moved, and a
	// user-scope install for claude-code, which is not installed here; opencode is.
	fromAnotherMachine := func() {
		writeTree(moved, map[string]string{".claude/commands/ship.md": "python ship\n"})
		fakeAssistant(tmp, "opencode", "1.0.0")
		writeTree(home, map[string]string{"installed.yml": "version: '1.0'\ninstallations:\n" +
			"- {module: review, assistant: claude-code, scope: project, project_path: /Users/alice/proj, commands: [ship]}\n" +
			"- {module: review, assistant: claude-code, scope: user, commands: [ship]}\n"})
	}

	// fetchedOnce asserts that the run, however many times it planned, left review cached once
	// and no run directory behind in the cache's staging.
	fetchedOnce := func() {
		GinkgoHelper()
		pp, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		versions, err := os.ReadDir(filepath.Join(pp.Store, "review", "versions"))
		Expect(err).NotTo(HaveOccurred())
		Expect(versions).To(HaveLen(1), "one cached version")
		runs, err := os.ReadDir(pp.Staging)
		if !os.IsNotExist(err) {
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(runs).To(BeEmpty(), "no run directory left in staging")
	}

	// untouched asserts the run wrote nothing: the Python lola's files and home as they were.
	untouched := func() {
		GinkgoHelper()
		Expect(home).To(BeADirectory())
		Expect(read(filepath.Join(proj, ".claude/commands/ship.md"))).To(Equal("python ship\n"))
		Expect(filepath.Join(proj, ".lola")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(tmp, "state", "lola", "trash")).NotTo(BeAnExistingFile())
	}

	// userRecords records review at user scope for each of the assistants, none installed here.
	userRecords := func(assistants ...string) {
		y := "version: '1.0'\ninstallations:\n"
		for _, a := range assistants {
			y += "- {module: review, assistant: " + a + ", scope: user, commands: [ship]}\n"
		}
		writeTree(home, map[string]string{"installed.yml": y})
	}

	It("migrate: start --run--> confirm", func() {
		p := &scripted{confirm: []bool{false}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Migrate?"}))
		Expect(r.err).To(ContainSubstring("plan (nothing written yet; the question below applies it):\n\nproject "+proj+":\n  install review"), "the plan first")
		untouched()
		pp, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(pp.Store, "review")).To(BeADirectory(), "the source was fetched into the cache before Migrate?, and a No leaves it there")
	})

	It("migrate: start --run (a missing root)--> root", func() {
		fromAnotherMachine()
		p := &scripted{roots: []string{cancelRun}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"migrate-root:/Users/alice"}))
		Expect(p.rootSteps[0]).To(Equal(cli.MigrateRootStep{Old: "/Users/alice", Suggest: filepath.Join(tmp, "home"), Value: filepath.Join(tmp, "home"), Records: 1}))
		Expect(read(filepath.Join(moved, ".claude/commands/ship.md"))).To(Equal("python ship\n"))
	})

	It("migrate: start --run (a missing assistant, one on your PATH)--> targets", func() {
		fakeAssistant(tmp, "opencode", "1.0.0")
		userRecords("claude-code")
		p := &scripted{mapTargets: [][]string{{cancelRun}}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"migrate-targets:claude-code"}))
		Expect(p.mapTargetSteps[0]).To(Equal(cli.MigrateTargetStep{Old: "claude-code", Supported: true, Modules: 1, Options: []string{"opencode"}}),
			"every option ticked; a first screen: Esc stays")
		Expect(home).To(BeADirectory())
	})

	It("migrate: start --run (a missing assistant, none on your PATH)--> confirm", func() {
		writeTree(home, map[string]string{"installed.yml": "version: '1.0'\ninstallations:\n" +
			"- {module: review, assistant: claude-code, scope: project, project_path: " + proj + ", commands: [ship]}\n" +
			"- {module: review, assistant: claude-code, scope: user, commands: [ship]}\n"})
		p := &scripted{confirm: []bool{false}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Migrate?"}), "no assistant to offer: not asked")
		Expect(r.err).To(ContainSubstring("unmappable review (claude-code): claude-code is not installed here"))
		untouched()
	})

	It("migrate: start --run (--map-root or --map-target)--> confirm", func() {
		fromAnotherMachine()
		p := &scripted{confirm: []bool{false}}
		r := lolaP(p, "migrate", "--map-root", "/Users/alice="+filepath.Join(tmp, "home"), "--map-target", "claude-code=opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Migrate?"}), "what the flags map is not asked")
		Expect(p.confirmNavs[0]).To(Equal(cli.Nav{}))
		Expect(r.err).To(ContainSubstring("project " + moved + ":\n  install review (claude-code)"))
	})

	It("migrate: start --run (-n)--> dry run", func() {
		fromAnotherMachine()
		p := &scripted{roots: []string{filepath.Join(tmp, "home")}, mapTargets: [][]string{{"opencode"}}}
		r := lolaP(p, "migrate", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"migrate-root:/Users/alice", "migrate-targets:claude-code"}), "the mapping questions are asked, Migrate? is not")
		Expect(r.out).To(ContainSubstring("project " + moved + ":\n  install review (claude-code)"))
		Expect(read(filepath.Join(moved, ".claude/commands/ship.md"))).To(Equal("python ship\n"))
		Expect(home).To(BeADirectory())
		Expect(filepath.Join(tmp, "state", "lola", "trash")).NotTo(BeAnExistingFile())
	})

	It("migrate: start --run (-y)--> done", func() {
		p := &scripted{}
		r := lolaP(p, "migrate", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty())
		Expect(r.out).To(ContainSubstring("✓ migrated project " + proj))
		Expect(read(filepath.Join(proj, ".claude/commands/ship.md"))).To(Equal("Ship $ARGUMENTS\n"))
	})

	It("migrate: start --run (--force)--> root", func() {
		fromAnotherMachine()
		p := &scripted{roots: []string{filepath.Join(tmp, "home")}, mapTargets: [][]string{{"opencode"}}}
		r := lolaP(p, "migrate", "--force")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"migrate-root:/Users/alice", "migrate-targets:claude-code"}), "--force answers Migrate?, not the mapping questions")
		Expect(r.out).To(ContainSubstring("✓ migrated project " + moved))
	})

	It("migrate: start --run (--json, on a terminal)--> confirm", func() {
		fakeAssistant(tmp, "opencode", "1.0.0")
		writeTree(home, map[string]string{"installed.yml": "version: '1.0'\ninstallations:\n" +
			"- {module: review, assistant: claude-code, scope: project, project_path: " + proj + ", commands: [ship]}\n" +
			"- {module: other, assistant: claude-code, scope: user, commands: [ship]}\n"})
		p := &scripted{confirm: []bool{false}}
		r := lolaP(p, "migrate", "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Migrate?"}), "under --json the mapping questions are not asked")
		Expect(r.out).To(BeEmpty())
		untouched()
	})

	It("migrate: start --run (nothing to migrate)--> done", func() {
		writeTree(home, map[string]string{"installed.yml": "version: '1.0'\ninstallations:\n- {module: review, assistant: cursor, scope: user}\n"})
		p := &scripted{}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty())
		Expect(r.out).To(HavePrefix("nothing more to migrate"))
		Expect(home).To(BeADirectory())
	})

	It("migrate: root --empty submit--> next step", func() {
		fromAnotherMachine()
		p := &scripted{roots: []string{""}, mapTargets: [][]string{{"opencode"}}, confirm: []bool{false}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"migrate-root:/Users/alice", "migrate-targets:claude-code", "confirm:Migrate?"}))
		Expect(r.err).To(ContainSubstring("unmappable review (claude-code at /Users/alice/proj): project_path /Users/alice/proj no longer exists"), "its records are left")
	})

	It("migrate: root --esc (after another root)--> root", func() {
		bob := filepath.Join(tmp, "bob")
		writeTree(filepath.Join(bob, "proj"), map[string]string{".claude/commands/ship.md": "python ship\n"})
		writeTree(moved, map[string]string{".claude/commands/ship.md": "python ship\n"})
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		writeTree(home, map[string]string{"installed.yml": "version: '1.0'\ninstallations:\n" +
			"- {module: review, assistant: claude-code, scope: project, project_path: /Users/alice/proj, commands: [ship]}\n" +
			"- {module: review, assistant: claude-code, scope: project, project_path: /Users/bob/proj, commands: [ship]}\n"})
		p := &scripted{roots: []string{"/srv/a", goBack, filepath.Join(tmp, "home"), bob}, confirm: []bool{false}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"migrate-root:/Users/alice", "migrate-root:/Users/bob", "migrate-root:/Users/alice", "migrate-root:/Users/bob", "confirm:Migrate?"}))
		Expect(p.rootSteps[1].Nav).To(Equal(cli.Nav{Back: true, Answered: 1}))
		Expect(p.rootSteps[2].Value).To(Equal("/srv/a"), "asked again as answered")
		Expect(r.err).To(ContainSubstring("project " + moved + ":"))
		Expect(r.err).To(ContainSubstring("project " + filepath.Join(bob, "proj") + ":"))
	})

	It("migrate: root --ctrl+c--> cancelled", func() {
		fromAnotherMachine()
		p := &scripted{roots: []string{cancelRun}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(filepath.Join(moved, ".claude/commands/ship.md"))).To(Equal("python ship\n"))
		Expect(home).To(BeADirectory())
		Expect(filepath.Join(tmp, "state", "lola", "trash")).NotTo(BeAnExistingFile())
	})

	It("migrate: targets --empty submit--> next step", func() {
		fakeAssistant(tmp, "opencode", "1.0.0")
		writeTree(home, map[string]string{"installed.yml": "version: '1.0'\ninstallations:\n" +
			"- {module: review, assistant: claude-code, scope: project, project_path: " + proj + ", commands: [ship]}\n" +
			"- {module: review, assistant: claude-code, scope: user, commands: [ship]}\n"})
		p := &scripted{mapTargets: [][]string{{}}, confirm: []bool{false}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"migrate-targets:claude-code", "confirm:Migrate?"}))
		Expect(r.err).To(ContainSubstring("unmappable review (claude-code): claude-code is not installed here"), "the records are left")
	})

	It("migrate: targets --esc (after another assistant)--> targets", func() {
		fakeAssistant(tmp, "opencode", "1.0.0")
		userRecords("claude-code", "gemini-cli")
		p := &scripted{mapTargets: [][]string{{"opencode"}, {goBack}, {"opencode"}, {"opencode"}}, confirm: []bool{false}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"migrate-targets:claude-code", "migrate-targets:gemini-cli", "migrate-targets:claude-code",
			"migrate-targets:gemini-cli", "confirm:Migrate?"}))
		Expect(p.mapTargetSteps[1].Nav).To(Equal(cli.Nav{Back: true, Answered: 1}))
		Expect(p.mapTargetSteps[2]).To(And(HaveField("Again", true), HaveField("Preselect", []string{"opencode"})), "asked again as answered")
	})

	It("migrate: targets --esc (first screen)--> targets", func() {
		fakeAssistant(tmp, "opencode", "1.0.0")
		userRecords("claude-code")
		p := &scripted{mapTargets: [][]string{{"opencode"}}, confirm: []bool{false}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.mapTargetSteps[0].Nav).To(Equal(cli.Nav{}), "no question before it: Esc stays")
	})

	It("migrate: targets --ctrl+c--> cancelled", func() {
		fakeAssistant(tmp, "opencode", "1.0.0")
		userRecords("claude-code")
		p := &scripted{mapTargets: [][]string{{cancelRun}}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"migrate-targets:claude-code"}))
		Expect(home).To(BeADirectory())
		Expect(lola("list", "--user").out).NotTo(ContainSubstring("review"))
	})

	It("migrate: confirm --yes (a hook or script without your consent)--> consent", func() {
		mark := filepath.Join(tmp, "hook-ran")
		writeTree(src, map[string]string{"lola.yml": "name: review\nhooks: {pre-install: pre.sh}\n", "pre.sh": "#!/bin/sh\ntouch '" + mark + "'\n"})
		Expect(os.Chmod(filepath.Join(src, "pre.sh"), 0o755)).To(Succeed())
		p := &scripted{confirm: []bool{true}, consent: []bool{true}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Migrate?", "consent:Run review's install hooks?"}), "the hook question after Migrate?")
		Expect(mark).To(BeAnExistingFile())
		Expect(r.out).To(ContainSubstring("✓ migrated project " + proj))
	})

	It("migrate: consent --no (a pre-install hook)--> failed", func() {
		mark := filepath.Join(tmp, "hook-ran")
		writeTree(src, map[string]string{"lola.yml": "name: review\nhooks: {pre-install: pre.sh}\n", "pre.sh": "#!/bin/sh\ntouch '" + mark + "'\n"})
		Expect(os.Chmod(filepath.Join(src, "pre.sh"), 0o755)).To(Succeed())
		p := &scripted{confirm: []bool{true}, consent: []bool{false}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(1))
		Expect(r.out + r.err).To(ContainSubstring("review has a pre-install hook, and lola runs hooks only with your consent"))
		Expect(mark).NotTo(BeAnExistingFile())
		untouched()
	})

	It("migrate: confirm --yes (--trust)--> receipt", func() {
		writeTree(src, map[string]string{"lola.yml": "name: review\nhooks: {pre-install: pre.sh}\n", "pre.sh": "#!/bin/sh\nexit 0\n"})
		Expect(os.Chmod(filepath.Join(src, "pre.sh"), 0o755)).To(Succeed())
		p := &scripted{confirm: []bool{true, false}}
		r := lolaP(p, "migrate", "--trust=hooks")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Migrate?", "confirm:Proceed?"}))
		Expect(r.err).To(ContainSubstring("trusting, as --trust=hooks allows:\n"))
		untouched()
	})

	It("migrate: confirm --yes (a destination fails)--> failed", func() {
		writeTree(home, map[string]string{"installed.yml": "version: '1.0'\ninstallations:\n" +
			"- {module: review, assistant: claude-code, scope: project, project_path: " + proj + ", commands: [ship]}\n" +
			"- {module: review, assistant: claude-code, scope: user, commands: [ship]}\n"})
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		claude := filepath.Join(proj, ".claude", "commands")
		p := &confirming{answers: []bool{true}, meddle: func() { Expect(os.Chmod(claude, 0o500)).To(Succeed()) }}
		DeferCleanup(func() { _ = os.Chmod(claude, 0o755) })
		r := lolaAsking(p, "migrate")
		Expect(r.code).To(Equal(1), r.out)
		Expect(p.questions).To(Equal([]string{"Migrate?"}))
		Expect(r.out).To(ContainSubstring("✓ migrated user scope"), "the other destination is migrated")
		Expect(read(filepath.Join(proj, ".claude/commands/ship.md"))).To(Equal("python ship\n"))
	})

	It("migrate: confirm --ctrl+c--> cancelled", func() {
		p := &scripted{confirmCancels: 1}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Migrate?"}))
		untouched()
	})

	It("migrate: confirm --esc (after an assistant question)--> targets", func() {
		fromAnotherMachine()
		p := &scripted{roots: []string{filepath.Join(tmp, "home")}, mapTargets: [][]string{{"opencode"}, {}}, confirmBacks: 1, confirm: []bool{true}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"migrate-root:/Users/alice", "migrate-targets:claude-code", "confirm:Migrate?",
			"migrate-targets:claude-code", "confirm:Migrate?"}))
		Expect(p.confirmNavs[0]).To(Equal(cli.Nav{Back: true, Answered: 2}), "Esc has the mapping questions to go back to")
		Expect(p.mapTargetSteps[1]).To(Equal(cli.MigrateTargetStep{Old: "claude-code", Supported: true, Modules: 1, Options: []string{"opencode"},
			Again: true, Preselect: []string{"opencode"}, Nav: cli.Nav{Back: true, Answered: 1}}), "asked again as answered")
		Expect(r.out).To(ContainSubstring("✓ migrated project " + moved))
		Expect(lola("list", "--user").out).NotTo(ContainSubstring("opencode"), "the second answer, none, stands")
		fetchedOnce()
	})

	It("migrate: confirm --esc (after root questions only)--> root", func() {
		writeTree(moved, map[string]string{".claude/commands/ship.md": "python ship\n"})
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		writeTree(home, map[string]string{"installed.yml": "version: '1.0'\ninstallations:\n" +
			"- {module: review, assistant: claude-code, scope: project, project_path: /Users/alice/proj, commands: [ship]}\n"})
		p := &scripted{roots: []string{filepath.Join(tmp, "home"), filepath.Join(tmp, "home")}, confirmBacks: 1, confirm: []bool{false}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"migrate-root:/Users/alice", "confirm:Migrate?", "migrate-root:/Users/alice", "confirm:Migrate?"}))
		Expect(p.confirmNavs[0]).To(Equal(cli.Nav{Back: true, Answered: 1}))
		Expect(p.rootSteps[1].Value).To(Equal(filepath.Join(tmp, "home")), "the root is offered as answered")
		Expect(p.rootSteps[1].Nav).To(Equal(cli.Nav{}), "the first question has nothing before it")
		Expect(strings.Count(r.err, "project "+moved+":\n  install review (claude-code)")).To(Equal(2), "the plan is shown again for the answers")
		Expect(read(filepath.Join(moved, ".claude/commands/ship.md"))).To(Equal("python ship\n"), "nothing written")
		Expect(home).To(BeADirectory())
		fetchedOnce()
	})

	It("migrate: confirm --esc (no mapping question)--> confirm", func() {
		p := &scripted{confirm: []bool{false}}
		r := lolaP(p, "migrate")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Migrate?"}))
		Expect(p.confirmNavs[0]).To(Equal(cli.Nav{}), "a first screen: Esc stays")
		untouched()
	})
})

var _ = Describe("consent paths", func() {
	var tmp, proj, kit, rt, plain, consentJSON string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, kit, rt, plain = filepath.Join(tmp, "proj"), filepath.Join(tmp, "kit"), filepath.Join(tmp, "rt"), filepath.Join(tmp, "plain")
		consentJSON = filepath.Join(tmp, "state", "lola", "consent.json")
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		writeTree(kit, map[string]string{
			"lola.yml":             "name: kit\nhooks: {pre-install: pre.sh, post-install: post.sh}\n",
			"skills/lint/SKILL.md": "---\ndescription: Lint\n---\nLint.\n",
			"pre.sh":               "#!/bin/sh\ntouch '" + filepath.Join(tmp, "pre-ran") + "'\n",
			"post.sh":              "#!/bin/sh\ntouch '" + filepath.Join(tmp, "post-ran") + "'\n",
		})
		Expect(os.Chmod(filepath.Join(kit, "pre.sh"), 0o755)).To(Succeed())
		Expect(os.Chmod(filepath.Join(kit, "post.sh"), 0o755)).To(Succeed())
		writeTree(rt, map[string]string{
			".claude-plugin/plugin.json": `{"name":"rt"}`,
			"skills/tidy/SKILL.md":       "---\ndescription: Tidy\n---\nTidy.\n",
			"hooks/hooks.json":           `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/stop.sh"}]}]}}`,
			"stop.sh":                    "#!/bin/sh\nexit 0\n",
		})
		writeTree(plain, map[string]string{"commands/ship.md": "Ship $ARGUMENTS\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
	})

	// scripts gives the project a pre-install and a post-install script, each touching a file
	// named after its event when it runs.
	scripts := func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\npre_install: ci/check.sh\npost_install: ci/after.sh\n",
			"ci/check.sh": "#!/bin/sh\ntouch '" + filepath.Join(tmp, "check-ran") + "'\n",
			"ci/after.sh": "#!/bin/sh\ntouch '" + filepath.Join(tmp, "after-ran") + "'\n"})
		Expect(os.Chmod(filepath.Join(proj, "ci", "check.sh"), 0o755)).To(Succeed())
		Expect(os.Chmod(filepath.Join(proj, "ci", "after.sh"), 0o755)).To(Succeed())
	}
	ran := func(name string) bool {
		_, err := os.Stat(filepath.Join(tmp, name))
		return err == nil
	}
	lock := func() string {
		GinkgoHelper()
		return read(filepath.Join(proj, ".lola", "lock.json"))
	}
	// untouched asserts the run wrote nothing: no files, no lock, no consent, no hook run.
	untouched := func() {
		GinkgoHelper()
		Expect(filepath.Join(proj, ".claude", "skills")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(proj, ".claude", "commands")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(proj, ".lola", "lock.json")).NotTo(BeAnExistingFile())
		Expect(consentJSON).NotTo(BeAnExistingFile())
		Expect(ran("pre-ran") || ran("post-ran") || ran("check-ran") || ran("after-ran")).To(BeFalse(), "nothing ran")
	}
	install := func(p *scripted, mod string, more ...string) result {
		return lolaP(p, append([]string{"install", mod, "-C", proj, "-t", "claude-code"}, more...)...)
	}

	It("consent: start --run (hooks without your consent, on a terminal)--> hooks", func() {
		p := &scripted{consentCancels: 1}
		r := install(p, kit, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"consent:Run kit's install hooks?"}))
		Expect(r.err).To(MatchRegexp(`kit has install hooks\. They run as you, unconfined:\n    pre-install: pre\.sh \(sha256 [0-9a-f]{64}\)\n` +
			`    post-install: post\.sh \(sha256 [0-9a-f]{64}\)\n  Consent covers only the scripts listed, not other files they run; review those too\.\n`))
		untouched()
	})

	It("consent: start --run (Claude Code hooks without your consent, on a terminal)--> runtime", func() {
		p := &scripted{consentCancels: 1}
		r := install(p, rt, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"consent:Install rt's Claude Code hooks?"}))
		Expect(r.err).To(ContainSubstring("rt has Claude Code hooks:\n    Stop: command\n      runs: "))
		Expect(r.err).To(ContainSubstring("\n  These run inside Claude Code with your permissions.\n"))
		untouched()
	})

	It("consent: start --run (a project script without your consent, on a terminal)--> script", func() {
		scripts()
		p := &scripted{consentCancels: 1}
		r := install(p, plain, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"consent:Run this project's pre-install script?"}))
		Expect(r.err).To(MatchRegexp(`The project at ` + regexp.QuoteMeta(proj) + ` has a pre-install script\. It runs as you, unconfined, for every module lola installs there:\n` +
			`    ci/check\.sh \(sha256 [0-9a-f]{64}\)\n`))
		Expect(r.err).NotTo(ContainSubstring("check-ran"), "the path and digest, not the body")
		untouched()
	})

	It("consent: start --run (lola sync, nothing to install)--> later", func() {
		scripts()
		Expect(lola("install", plain, "-C", proj, "-t", "claude-code", "-y", "--trust=scripts").code).To(Equal(0))
		Expect(os.Remove(filepath.Join(tmp, "check-ran"))).To(Succeed())
		writeTree(proj, map[string]string{"ci/check.sh": "#!/bin/sh\ntouch '" + filepath.Join(tmp, "check-ran") + "'\nexit 0\n"})
		before := read(consentJSON)
		p := &scripted{consentCancels: 1}
		r := lolaP(p, "sync", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"consent:Trust this project's pre-install script for later runs?"}))
		Expect(read(consentJSON)).To(Equal(before))
		Expect(ran("check-ran")).To(BeFalse())
	})

	It("consent: start --run (consent recorded)--> done", func() {
		Expect(install(&scripted{consent: []bool{true}}, kit, "-y").code).To(Equal(0))
		Expect(os.RemoveAll(filepath.Join(proj, ".claude", "skills"))).To(Succeed())
		p := &scripted{}
		r := lolaP(p, "sync", "-C", proj, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty(), "the consent recorded for the hooks' digest answers")
		Expect(filepath.Join(proj, ".claude", "skills", "lint", "SKILL.md")).To(BeARegularFile())
	})

	It("consent: start --run (-y or --force)--> hooks", func() {
		for _, flag := range []string{"-y", "--force"} {
			p := &scripted{consentCancels: 1}
			r := install(p, kit, flag)
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.calls).To(Equal([]string{"consent:Run kit's install hooks?"}), flag+" never answers consent")
			untouched()
		}
	})

	It("consent: start --run (--trust)--> receipt", func() {
		p := &scripted{confirm: []bool{true}}
		r := install(p, kit, "--trust=hooks")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Proceed?"}), "the kind --trust covers is not asked")
		Expect(r.err).To(ContainSubstring("trusting, as --trust=hooks allows:\n  → hooks: kit pre-install (new), kit post-install (new)\n"))
		Expect(ran("pre-ran") && ran("post-ran")).To(BeTrue())
		Expect(read(consentJSON)).To(ContainSubstring(`"kit"`))
	})

	It("consent: start --run (-n)--> done", func() {
		scripts()
		p := &scripted{}
		r := install(p, kit, "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty(), "a dry run asks nothing")
		Expect(r.out).To(ContainSubstring("pre-install hook pre.sh"))
		untouched()
	})

	It("consent: start --run (--json or no terminal, a pre-install hook or script)--> refused", func() {
		for _, args := range [][]string{{"-y", "--json"}, {"-y"}} {
			p := &scripted{}
			var r result
			if len(args) == 2 {
				r = install(p, kit, args...)
			} else {
				r = lola(append([]string{"install", kit, "-C", proj, "-t", "claude-code"}, args...)...)
			}
			Expect(r.code).To(Equal(1), r.err)
			Expect(p.calls).To(BeEmpty())
			Expect(r.err).To(ContainSubstring("kit has a pre-install hook, and lola runs hooks only with your consent"))
			Expect(r.err).To(ContainSubstring("--trust=hooks"))
			untouched()
		}
	})

	It("consent: hooks --yes--> next step", func() {
		scripts()
		p := &scripted{consent: []bool{true, true, true}}
		r := install(p, kit, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"consent:Run kit's install hooks?", "consent:Run this project's pre-install script?",
			"consent:Run this project's post-install script?"}))
		Expect(ran("pre-ran") && ran("post-ran") && ran("check-ran") && ran("after-ran")).To(BeTrue())
		Expect(read(consentJSON)).To(ContainSubstring(`"kit"`))
		Expect(lock()).NotTo(ContainSubstring("pending"))
	})

	It("consent: hooks --no (a pre-install hook)--> refused", func() {
		p := &scripted{consent: []bool{false}}
		r := install(p, kit, "-y")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: kit has a pre-install hook, and lola runs hooks only with your consent\n"))
		Expect(r.err).To(ContainSubstring("  hint: then consent to it: lola install " + kit))
		untouched()
	})

	It("consent: hooks --no (post-install hooks only)--> done", func() {
		writeTree(kit, map[string]string{"lola.yml": "name: kit\nhooks: {post-install: post.sh}\n"})
		p := &scripted{consent: []bool{false}}
		r := install(p, kit, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"consent:Run kit's install hooks?"}))
		Expect(filepath.Join(proj, ".claude", "skills", "lint", "SKILL.md")).To(BeARegularFile())
		Expect(ran("post-ran")).To(BeFalse())
		Expect(lock()).To(ContainSubstring(`"hooks_pending": true`))
		Expect(consentJSON).NotTo(BeAnExistingFile())
		Expect(r.out + r.err).To(ContainSubstring("kit: post-install hook skipped without your consent; review and run it: lola sync -C " + proj + " kit"))
	})

	It("consent: hooks --ctrl+c--> cancelled", func() {
		scripts()
		p := &scripted{consentCancels: 1}
		r := install(p, kit, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"consent:Run kit's install hooks?"}))
		untouched()
	})

	It("consent: runtime --yes--> next step", func() {
		p := &scripted{consent: []bool{true}}
		r := install(p, rt, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"consent:Install rt's Claude Code hooks?"}))
		Expect(r.out).To(ContainSubstring("runtime hooks for claude-code (consented)"))
		Expect(consentJSON).To(BeARegularFile())
		p = &scripted{}
		Expect(lolaP(p, "sync", "-C", proj, "-y").code).To(Equal(0))
		Expect(p.calls).To(BeEmpty(), "not asked again")
	})

	It("consent: runtime --no--> next step", func() {
		p := &scripted{consent: []bool{false}}
		r := install(p, rt, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(proj, ".claude", "skills", "tidy", "SKILL.md")).To(BeARegularFile(), "the rest is installed")
		Expect(r.out).NotTo(ContainSubstring("(consented)"))
		Expect(consentJSON).NotTo(BeAnExistingFile())
		p = &scripted{consentCancels: 1}
		Expect(lolaP(p, "sync", "-C", proj, "-y").code).To(Equal(0))
		Expect(p.calls).To(Equal([]string{"consent:Install rt's Claude Code hooks?"}), "the next sync asks again")
	})

	It("consent: runtime --ctrl+c--> cancelled", func() {
		p := &scripted{consentCancels: 1}
		r := install(p, rt, "-y")
		Expect(r.code).To(Equal(0), r.err)
		untouched()
	})

	It("consent: script --yes--> next step", func() {
		scripts()
		p := &scripted{consent: []bool{true, true}}
		r := install(p, plain, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"consent:Run this project's pre-install script?", "consent:Run this project's post-install script?"}))
		Expect(ran("check-ran") && ran("after-ran")).To(BeTrue())
		Expect(consentJSON).To(BeARegularFile())
	})

	It("consent: script --no (pre-install)--> refused", func() {
		scripts()
		p := &scripted{consent: []bool{false}}
		r := install(p, plain, "-y")
		Expect(r.code).To(Equal(1))
		Expect(p.calls).To(Equal([]string{"consent:Run this project's pre-install script?"}))
		Expect(r.err).To(ContainSubstring("pre-install script ci/check.sh"))
		untouched()
	})

	It("consent: script --no (post-install)--> done", func() {
		scripts()
		p := &scripted{consent: []bool{true, false}}
		r := install(p, plain, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(ran("check-ran")).To(BeTrue())
		Expect(ran("after-ran")).To(BeFalse())
		Expect(lock()).To(ContainSubstring(`"scripts_pending": true`))
		Expect(r.out + r.err).To(ContainSubstring("post-install script ci/after.sh (project) skipped without your trust; review and run it: lola sync -C " + proj + " plain"))
	})

	It("consent: script --ctrl+c--> cancelled", func() {
		scripts()
		p := &scripted{consentCancels: 1}
		r := install(p, plain, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"consent:Run this project's pre-install script?"}))
		untouched()
	})

	It("consent: later --yes--> done", func() {
		scripts()
		Expect(lola("install", plain, "-C", proj, "-t", "claude-code", "-y", "--trust=scripts").code).To(Equal(0))
		Expect(os.Remove(filepath.Join(tmp, "check-ran"))).To(Succeed())
		writeTree(proj, map[string]string{"ci/check.sh": "#!/bin/sh\ntouch '" + filepath.Join(tmp, "check-ran") + "'\nexit 0\n"})
		before := read(consentJSON)
		p := &scripted{consent: []bool{true}}
		r := lolaP(p, "sync", "-C", proj)
		Expect(r.code).To(Equal(0), r.err)
		Expect(read(consentJSON)).NotTo(Equal(before), "the new digest is recorded")
		Expect(ran("check-ran")).To(BeFalse(), "nothing runs: there is nothing to wrap")
	})

	It("consent: later --no--> refused", func() {
		scripts()
		Expect(lola("install", plain, "-C", proj, "-t", "claude-code", "-y", "--trust=scripts").code).To(Equal(0))
		writeTree(proj, map[string]string{"ci/check.sh": "#!/bin/sh\nexit 0\n"})
		before := read(consentJSON)
		p := &scripted{consent: []bool{false}}
		r := lolaP(p, "sync", "-C", proj)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("pre-install script ci/check.sh"))
		Expect(read(consentJSON)).To(Equal(before))
	})
})

var _ = Describe("init paths", func() {
	var tmp, dir string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		dir = filepath.Join(tmp, "fresh")
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		GinkgoT().Chdir(dir)
	})
	question := func() string { return "confirm:Create module fresh in " + dir + "?" }
	// empty asserts nothing was scaffolded in d.
	empty := func(d string) {
		GinkgoHelper()
		entries, err := os.ReadDir(d)
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(BeEmpty(), "nothing written")
	}

	It("init: start --run--> confirm", func() {
		p := &scripted{confirm: []bool{false}}
		r := lolaP(p, "init")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{question()}))
		Expect(r.err).To(HavePrefix("will create module fresh in "+dir+" (agent-plugins):\n    plugin.json\n"), "the plan first")
		empty(dir)
	})

	It("init: start --dir--> done", func() {
		p := &scripted{}
		r := lolaP(p, "init", filepath.Join(tmp, "other"))
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty(), "a directory given asks nothing, on a terminal too")
		Expect(filepath.Join(tmp, "other", "plugin.json")).To(BeARegularFile())
	})

	It("init: start --run (-y)--> done", func() {
		p := &scripted{}
		r := lolaP(p, "init", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty())
		Expect(filepath.Join(dir, "plugin.json")).To(BeARegularFile())
	})

	It("init: start --run (--json, on a terminal)--> confirm", func() {
		p := &scripted{confirm: []bool{true}}
		r := lolaP(p, "init", "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{question()}))
		Expect(r.out).To(ContainSubstring(`"files": [`), "the result as one JSON document on stdout")
		Expect(filepath.Join(dir, "plugin.json")).To(BeARegularFile())
	})

	It("init: confirm --yes (a write fails)--> failed", func() {
		if os.Geteuid() == 0 {
			Skip("root writes into a read-only directory")
		}
		locked := filepath.Join(dir, "dev.getlola")
		p := &confirming{answers: []bool{true}, meddle: func() { Expect(os.Mkdir(locked, 0o500)).To(Succeed()) }}
		DeferCleanup(os.Chmod, locked, os.FileMode(0o755))
		r := lolaAsking(p, "init")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("permission denied"))
		Expect(filepath.Join(dir, "plugin.json")).To(BeARegularFile(), "what was written before it stays: a partial scaffold")
		Expect(filepath.Join(dir, "skills", "hello", "SKILL.md")).To(BeARegularFile())
		Expect(filepath.Join(locked, "commands")).NotTo(BeAnExistingFile())
	})

	It("init: confirm --esc--> confirm", func() {
		p := &scripted{confirm: []bool{false}}
		Expect(lolaP(p, "init").code).To(Equal(0))
		Expect(p.confirmNavs).To(Equal([]cli.Nav{{}}), "a first screen: Esc stays")
		empty(dir)
	})

	It("init: confirm --ctrl+c--> cancelled", func() {
		p := &scripted{confirmCancels: 1}
		r := lolaP(p, "init")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{question()}))
		empty(dir)
	})
})
