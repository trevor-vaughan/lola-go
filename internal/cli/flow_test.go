package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/cli"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
	"github.com/trevor-vaughan/lola-go/internal/trust"
)

// scripted is a Prompter that answers from queues and records each step it was asked, so
// specs check step order, steps not shown and what each step offered.
type scripted struct {
	calls      []string
	scope      []func(cli.ScopeStep) ([]string, error)
	modules    []func(cli.ModuleStep) (cli.ModuleAnswer, error)
	assistants []func(cli.AssistantStep) ([]string, error)
	components []func(cli.ComponentPage) ([]string, error)
	dirs       []func(cli.DirectoryStep) (string, error)
	empty      []bool
	emptyBacks int // the first emptyBacks no-components questions go back a screen, as Esc does
	review     []string
	confirm    []bool
	trust      []func(cli.TrustStep) (engine.TrustAnswer, error)
	profiles   []func(cli.ProfileStep) ([]string, error)
	names      []string
	entries    []string
	tidyPick   []func(cli.TidyPickStep) ([]string, error)
	tidyKeep   []func(cli.TidyKeepStep) ([]string, error)
	background []bool
	roots      []string
	mapTargets [][]string
	sshHosts   []func(cli.SSHHostStep) ([]string, error)
	where      []int
	members    []func(cli.MembersStep) ([]string, error)
	trashPick  []func(cli.TrashPickStep) (cli.TrashAnswer, error)
	versions   []func(cli.VersionStep) (int, error)
	consent    []bool

	// the first emptyCancels no-components questions (after emptyBacks) and confirmCancels other
	// yes/no questions are cancelled, as Ctrl+C does; the first confirmBacks of those, before
	// them, go back a screen, as Esc does; the first consentCancels consent questions are
	// cancelled, as Ctrl+C does
	emptyCancels, confirmCancels, confirmBacks, consentCancels int

	scopeSteps     []cli.ScopeStep
	dirSteps       []cli.DirectoryStep
	moduleSteps    []cli.ModuleStep
	assistantSteps []cli.AssistantStep
	pages          []cli.ComponentPage
	reviewSteps    []cli.ReviewStep
	trustSteps     []cli.TrustStep
	profileSteps   []cli.ProfileStep
	nameSteps      []cli.MarketNameStep
	entrySteps     []cli.MarketEntryStep
	tidyPickSteps  []cli.TidyPickStep
	tidyKeepSteps  []cli.TidyKeepStep
	rootSteps      []cli.MigrateRootStep
	mapTargetSteps []cli.MigrateTargetStep
	sshSteps       []cli.SSHHostStep
	emptyNavs      []cli.Nav
	confirmNavs    []cli.Nav
	whereSteps     []cli.WhereStep
	memberSteps    []cli.MembersStep
	trashPickSteps []cli.TrashPickStep
	versionSteps   []cli.VersionStep
	asks           []cli.Question // every question Ask was asked

	summaryTo io.Writer // where Review writes the summary, as the huh adapter leaves it on stderr
}

// goBack is a scripted answer to the review or a migrate question that goes back a screen, as
// Esc does.
const goBack = "\x00back"

// cancelRun is a scripted answer to the market name or a migrate question that cancels the run, as
// Ctrl+C does.
const cancelRun = "\x00ctrl+c"

// goneBack answers a step by going back a screen, as Esc does.
func goneBack[S, T any](S) (T, error) {
	var none T
	return none, cli.ErrBack
}

// aborted answers a step by cancelling the run, as Ctrl+C does.
func aborted[S, T any](S) (T, error) {
	var none T
	return none, cli.ErrAborted
}

func pop[T any](q *[]T, step string) T {
	if len(*q) == 0 {
		Fail("the flow asked an unscripted " + step + " step")
	}
	v := (*q)[0]
	*q = (*q)[1:]
	return v
}

func (s *scripted) Interactive() bool { return true }

// Scripted Where answers that go back a screen, as Esc does, and that cancel the run, as Ctrl+C
// does.
const (
	whereBack  = -2
	whereCtrlC = -3
)

// Where answers the option scripted; whereBack goes back, whereCtrlC cancels the run, and any
// other negative one picks Cancel.
func (s *scripted) Where(st cli.WhereStep) (int, error) {
	s.calls, s.whereSteps = append(s.calls, "where"), append(s.whereSteps, st)
	switch i := pop(&s.where, "where"); {
	case i >= 0:
		return i, nil
	case i == whereBack:
		return 0, cli.ErrBack
	case i == whereCtrlC:
		return 0, cli.ErrAborted
	default:
		return 0, cli.ErrCancelled
	}
}
func (s *scripted) Scope(st cli.ScopeStep) ([]string, error) {
	s.calls, s.scopeSteps = append(s.calls, "scope"), append(s.scopeSteps, st)
	return pop(&s.scope, "scope")(st)
}
func (s *scripted) OtherDirectory(st cli.DirectoryStep) (string, error) {
	s.calls, s.dirSteps = append(s.calls, "directory"), append(s.dirSteps, st)
	return pop(&s.dirs, "directory")(st)
}
func (s *scripted) Modules(st cli.ModuleStep) (cli.ModuleAnswer, error) {
	s.calls, s.moduleSteps = append(s.calls, "modules"), append(s.moduleSteps, st)
	return pop(&s.modules, "modules")(st)
}
func (s *scripted) Assistants(st cli.AssistantStep) ([]string, error) {
	s.calls, s.assistantSteps = append(s.calls, "assistants"), append(s.assistantSteps, st)
	return pop(&s.assistants, "assistants")(st)
}
func (s *scripted) Version(st cli.VersionStep) (int, error) {
	s.calls, s.versionSteps = append(s.calls, "version:"+st.Target), append(s.versionSteps, st)
	return pop(&s.versions, "version")(st)
}
func (s *scripted) Components(pg cli.ComponentPage) ([]string, error) {
	s.calls, s.pages = append(s.calls, "components:"+pg.Title), append(s.pages, pg)
	return pop(&s.components, "components")(pg)
}
func (s *scripted) askEmpty(module string, nav cli.Nav) (bool, error) {
	s.calls, s.emptyNavs = append(s.calls, "empty:"+module), append(s.emptyNavs, nav)
	if s.emptyBacks > 0 {
		s.emptyBacks--
		return false, cli.ErrBack
	}
	if s.emptyCancels > 0 {
		s.emptyCancels--
		return false, cli.ErrAborted
	}
	return pop(&s.empty, "confirm-empty"), nil
}
func (s *scripted) askConfirm(question string, nav cli.Nav) (bool, error) {
	s.calls, s.confirmNavs = append(s.calls, "confirm:"+question), append(s.confirmNavs, nav)
	if s.confirmBacks > 0 {
		s.confirmBacks--
		return false, cli.ErrBack
	}
	if s.confirmCancels > 0 {
		s.confirmCancels--
		return false, cli.ErrAborted
	}
	return pop(&s.confirm, "confirm"), nil
}
func (s *scripted) askConsent(question string) (bool, error) {
	s.calls = append(s.calls, "consent:"+question)
	if s.consentCancels > 0 {
		s.consentCancels--
		return false, cli.ErrAborted
	}
	return pop(&s.consent, "consent"), nil
}

// consentPrompt matches hookOptions' and askScript's consent questions; emptyPrompt the question
// an install asks of a module whose components are all unticked.
var (
	consentPrompt = regexp.MustCompile(`^(Run .+'s install hooks|Install .+'s Claude Code hooks( and agent MCP servers)?|Run this project's \S+ script|Trust this project's \S+ script for later runs)\?$`)
	emptyPrompt   = regexp.MustCompile(`^Install (.+) with no components\?$`)
)

// Ask answers a yes/no question from the queue of the kind its wording says: consent questions
// from consent, the no-components question from empty, any other from confirm, each recorded in
// calls as "consent:", "empty:" or "confirm:" with its scripted backs and cancels. The question is
// kept in asks, and its text written to summaryTo, as the huh adapter leaves it on stderr.
func (s *scripted) Ask(q cli.Question) (rune, error) {
	s.asks = append(s.asks, q)
	if s.summaryTo != nil {
		fmt.Fprint(s.summaryTo, cli.ShowText(q))
	}
	var ok bool
	var err error
	switch m := emptyPrompt.FindStringSubmatch(q.Prompt); {
	case m != nil:
		ok, err = s.askEmpty(m[1], q.Nav)
	case consentPrompt.MatchString(q.Prompt):
		ok, err = s.askConsent(q.Prompt)
	default:
		ok, err = s.askConfirm(q.Prompt, q.Nav)
	}
	switch {
	case err != nil:
		return 0, err
	case ok:
		return 'y', nil
	}
	return 'n', nil
}
func (s *scripted) Trust(st cli.TrustStep) (engine.TrustAnswer, error) {
	s.calls, s.trustSteps = append(s.calls, "trust"), append(s.trustSteps, st)
	return pop(&s.trust, "trust")(st)
}
func (s *scripted) Members(st cli.MembersStep) ([]string, error) {
	s.calls, s.memberSteps = append(s.calls, "members"), append(s.memberSteps, st)
	return pop(&s.members, "members")(st)
}
func (s *scripted) Profiles(st cli.ProfileStep) ([]string, error) {
	s.calls, s.profileSteps = append(s.calls, "profiles"), append(s.profileSteps, st)
	return pop(&s.profiles, "profiles")(st)
}
func (s *scripted) SSHHosts(st cli.SSHHostStep) ([]string, error) {
	s.calls, s.sshSteps = append(s.calls, "ssh-hosts"), append(s.sshSteps, st)
	return pop(&s.sshHosts, "ssh-hosts")(st)
}
func (s *scripted) MarketName(st cli.MarketNameStep) (string, error) {
	s.calls, s.nameSteps = append(s.calls, "market-name"), append(s.nameSteps, st)
	if name := pop(&s.names, "market-name"); name != cancelRun {
		return name, nil
	}
	return "", cli.ErrAborted
}
func (s *scripted) MarketEntry(st cli.MarketEntryStep) (string, error) {
	s.calls, s.entrySteps = append(s.calls, "market-entry"), append(s.entrySteps, st)
	return pop(&s.entries, "market-entry"), nil
}
func (s *scripted) TidyPick(st cli.TidyPickStep) ([]string, error) {
	s.calls, s.tidyPickSteps = append(s.calls, "tidy-pick"), append(s.tidyPickSteps, st)
	return pop(&s.tidyPick, "tidy-pick")(st)
}
func (s *scripted) TidyKeep(st cli.TidyKeepStep) ([]string, error) {
	s.calls, s.tidyKeepSteps = append(s.calls, "tidy-keep"), append(s.tidyKeepSteps, st)
	return pop(&s.tidyKeep, "tidy-keep")(st)
}
func (s *scripted) TrashPick(st cli.TrashPickStep) (cli.TrashAnswer, error) {
	s.calls, s.trashPickSteps = append(s.calls, "trash-pick"), append(s.trashPickSteps, st)
	return pop(&s.trashPick, "trash-pick")(st)
}
func (s *scripted) Background() (bool, error) {
	s.calls = append(s.calls, "background")
	return pop(&s.background, "background"), nil
}
func (s *scripted) MigrateRoot(st cli.MigrateRootStep) (string, error) {
	s.calls, s.rootSteps = append(s.calls, "migrate-root:"+st.Old), append(s.rootSteps, st)
	switch to := pop(&s.roots, "migrate-root"); to {
	case goBack:
		return "", cli.ErrBack
	case cancelRun:
		return "", cli.ErrAborted
	default:
		return to, nil
	}
}
func (s *scripted) MigrateTargets(st cli.MigrateTargetStep) ([]string, error) {
	s.calls, s.mapTargetSteps = append(s.calls, "migrate-targets:"+st.Old), append(s.mapTargetSteps, st)
	switch ids := pop(&s.mapTargets, "migrate-targets"); {
	case slices.Equal(ids, []string{goBack}):
		return nil, cli.ErrBack
	case slices.Equal(ids, []string{cancelRun}):
		return nil, cli.ErrAborted
	default:
		return ids, nil
	}
}
func (s *scripted) Review(st cli.ReviewStep) (string, error) {
	s.calls, s.reviewSteps = append(s.calls, "review"), append(s.reviewSteps, st)
	if s.summaryTo != nil {
		fmt.Fprint(s.summaryTo, st.Summary)
	}
	switch r := pop(&s.review, "review"); r {
	case cli.ReviewQuit:
		return "", cli.ErrAborted
	case goBack:
		return "", cli.ErrBack
	default:
		return r, nil
	}
}

// answers return fixed values.
func scopes(names ...string) func(cli.ScopeStep) ([]string, error) {
	return func(cli.ScopeStep) ([]string, error) { return names, nil }
}
func dir(path string) func(cli.DirectoryStep) (string, error) {
	return func(cli.DirectoryStep) (string, error) { return path, nil }
}
func mods(names ...string) func(cli.ModuleStep) (cli.ModuleAnswer, error) {
	return func(cli.ModuleStep) (cli.ModuleAnswer, error) { return cli.ModuleAnswer{Names: names}, nil }
}
func era(i int) func(cli.VersionStep) (int, error) {
	return func(cli.VersionStep) (int, error) { return i, nil }
}
func pick(ids ...string) func(cli.ComponentPage) ([]string, error) {
	return func(cli.ComponentPage) ([]string, error) { return ids, nil }
}

// keep answers a page with its preselection minus drop.
func keep(drop ...string) func(cli.ComponentPage) ([]string, error) {
	return func(pg cli.ComponentPage) ([]string, error) {
		var out []string
		for _, g := range pg.Groups {
			for _, o := range g.Options {
				if o.Selected && !slices.Contains(drop, o.ID) {
					out = append(out, o.ID)
				}
			}
		}
		return out, nil
	}
}

// ticked is what a component page had selected when it was asked.
func ticked(pg cli.ComponentPage) []string {
	var out []string
	for _, g := range pg.Groups {
		for _, o := range g.Options {
			if o.Selected {
				out = append(out, o.ID)
			}
		}
	}
	return out
}

// cacheTwice caches module a twice from one folder, so its first version is superseded, and
// returns that version's path in the cache.
func cacheTwice(p paths.Paths) string {
	GinkgoHelper()
	src := filepath.Join(filepath.Dir(p.Config), "src", "a")
	writeTree(src, map[string]string{"lola.yml": "name: a\n", "commands/hi.md": "v1\n"})
	Expect(lola("cache", "add", src).code).To(Equal(0))
	ids, err := os.ReadDir(filepath.Join(p.Store, "a", "versions"))
	Expect(err).NotTo(HaveOccurred())
	Expect(ids).To(HaveLen(1))
	writeTree(src, map[string]string{"commands/hi.md": "v2\n"})
	Expect(lola("cache", "add", src).code).To(Equal(0))
	return filepath.Join(p.Store, "a", "versions", ids[0].Name())
}

// lolaP runs lola with p as its Prompter.
func lolaP(p cli.Prompter, args ...string) result {
	root, err := cli.NewRootCmd(os.Getenv, cli.WithPrompter(p))
	Expect(err).NotTo(HaveOccurred())
	var out, errb bytes.Buffer
	if s, ok := p.(*scripted); ok {
		s.summaryTo = &errb
	}
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(args)
	code := cli.Execute(root)
	return result{out: out.String(), err: errb.String(), code: code}
}

// equivalents returns the equivalent commands the last summary printed.
func equivalents(stderr string) []string {
	i := strings.LastIndex(stderr, "Equivalent commands:\n")
	Expect(i).NotTo(BeNumerically("<", 0), stderr)
	after := stderr[i+len("Equivalent commands:\n"):]
	var out []string
	for _, l := range strings.Split(after, "\n") {
		if !strings.HasPrefix(l, "  ") {
			break
		}
		out = append(out, strings.TrimSpace(l))
	}
	return out
}

// shellWords splits a command line shellQuote wrote: words separated by spaces, '…' quoted.
func shellWords(line string) []string {
	var out []string
	var cur strings.Builder
	quoted, inWord := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\'':
			quoted, inWord = !quoted, true
		case c == ' ' && !quoted:
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
			}
			inWord = false
		case c == '\\' && !quoted && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
			inWord = true
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out
}

var _ = Describe("the install flow", func() {
	var tmp, proj, review, deploy, selections, home string
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		home = os.Getenv("HOME")
		proj = filepath.Join(tmp, "proj")
		review, deploy = filepath.Join(tmp, "src", "review-kit"), filepath.Join(tmp, "src", "deploy")
		writeTree(review, map[string]string{
			"lola.yml":                  "name: review-kit\ndescription: Code review helpers\n",
			"skills/review/SKILL.md":    "---\ndescription: Review code\n---\nReview.\n",
			"skills/slow-lint/SKILL.md": "---\ndescription: Lint slowly\n---\nLint.\n",
			"commands/ship.md":          "---\ndescription: Ship it\n---\nShip.\n",
			"rules/style.md":            "Be brief.\n",
		})
		writeTree(deploy, map[string]string{"lola.yml": "name: deploy\n", "commands/deploy.md": "Deploy.\n"})
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		selections = filepath.Join(tmp, "config", "lola", "selections.yaml")
		Expect(lola("cache", "add", review).code).To(Equal(0))
		Expect(lola("cache", "add", deploy).code).To(Equal(0))
		GinkgoT().Chdir(proj)
	})

	It("refuses an entry project.yml changed since lola wrote it before any picker or summary", func() {
		Expect(lola("install", "review-kit", "deploy", "--scope", "project", "-t", "claude-code", "-y").code).To(Equal(0))
		mpath := filepath.Join(proj, ".lola", "project.yml")
		b, err := os.ReadFile(mpath)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(mpath, []byte(strings.Replace(string(b), "  - name: review-kit\n", "  - name: review-kit\n    rules: false\n", 1)), 0o644)).To(Succeed())

		p := &scripted{}
		r := lolaP(p, "install", "review-kit", "--scope", "project")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("review-kit: project.yml changed for it since you last trusted it"))
		Expect(r.err).To(ContainSubstring("hint: review and apply it: lola sync --project review-kit\n"))
		Expect(p.calls).To(BeEmpty(), "refused before any prompt")

		p = &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")}, review: []string{cli.ReviewQuit}}
		r = lolaP(p, "install", "--scope", "project", "-t", "claude-code")
		Expect(r.err).To(ContainSubstring("⚠ 1 module in project.yml changed since you last trusted it: review-kit → lola sync --project\n"))
		Expect(p.moduleSteps[0].Options).To(ConsistOf(HaveField("Name", "deploy")), "the picker never offers it")
	})

	It("install without -t coaches an @ source in project.yml as every reader does", func() {
		acme := filepath.Join(tmp, "acme")
		writeTree(acme, map[string]string{".claude-plugin/marketplace.json": `{"name": "acme", "owner": {"name": "Acme"},
  "plugins": [{"name": "formatter", "source": "./formatter", "description": "Formats code"}]}`, "formatter/commands/fmt.md": "Fmt\n"})
		Expect(lola("market", "--add", acme).code).To(Equal(0))
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\nmodules:\n  - name: formatter\n    source: '@acme/formatter'\n" +
			"  - {name: fmt2, source: '@acme/formatter'}\n"})
		for _, dry := range [][]string{nil, {"-n"}} {
			r := lola(slices.Concat([]string{"install", review, "--project"}, dry, []string{"--json"})...)
			Expect(r.code).To(Equal(1))
			var refusal struct {
				Error string   `json:"error"`
				Hints []string `json:"hints"`
			}
			Expect(json.Unmarshal([]byte(r.err), &refusal)).To(Succeed(), r.err)
			Expect(refusal.Error).To(ContainSubstring("formatter's source is @acme/formatter\n"))
			Expect(refusal.Hints).To(Equal([]string{strings.Join(slices.Concat([]string{"lola install --project @acme/formatter"}, dry, []string{"--json"}), " "),
				"lola market acme --json"}), "%v", dry)
		}
	})

	It("install refuses untrusted top-level targets it would use: the picker asks for the assistants, never preselecting them", func() {
		Expect(lola("install", "review-kit", "--scope", "project", "-t", "claude-code", "-y").code).To(Equal(0))
		mpath := filepath.Join(proj, ".lola", "project.yml")
		b, err := os.ReadFile(mpath)
		Expect(err).NotTo(HaveOccurred())
		pulled := strings.Replace(string(b), "targets: [claude-code]", "targets: [claude-code, gemini-cli]", 1)
		Expect(pulled).NotTo(Equal(string(b)))
		Expect(os.WriteFile(mpath, []byte(pulled), 0o644)).To(Succeed())
		fakeAssistant(tmp, "gemini", "0.63.0")
		Expect(os.MkdirAll(filepath.Join(proj, ".gemini"), 0o755)).To(Succeed())

		r := lola("install", "deploy", "--scope", "project")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal(projectContext(proj) + "error: project.yml's top-level targets [claude-code, gemini-cli] changed since you last trusted them\n" +
			"  hint: review and apply them: lola sync --project\n  hint: or name the assistants: lola install --project deploy -t claude-code,gemini-cli\n"))

		p := &scripted{
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")},
			assistants: []func(cli.AssistantStep) ([]string, error){func(cli.AssistantStep) ([]string, error) { return []string{"claude-code"}, nil }},
			review:     []string{cli.ReviewApply},
		}
		r = lolaP(p, "install", "--scope", "project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.assistantSteps[0].Options).To(ContainElements("claude-code", "gemini-cli"))
		Expect(p.assistantSteps[0].Preselect).NotTo(ContainElement("gemini-cli"), "gemini-cli is set up here, but only a pull asked for it")
		Expect(filepath.Join(proj, ".claude/commands/deploy.md")).To(BeARegularFile())
		Expect(filepath.Join(proj, ".gemini/commands/deploy.toml")).NotTo(BeAnExistingFile())
	})

	It("coaches lola sync --user for the modules default lists that are not installed", func() {
		Expect(lola("install", "review-kit", "--user", "-t", "claude-code", "-y").code).To(Equal(0))
		upath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "lola", "profiles", "default.yml")
		b, err := os.ReadFile(upath)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(upath, append(b, []byte("  - name: deploy\n    source: "+deploy+"\n")...), 0o644)).To(Succeed())

		r := lola("install", "--user")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("hint: install what profile:default lists: lola sync --user\n"))

		aborted := func(cli.ModuleStep) (cli.ModuleAnswer, error) { return cli.ModuleAnswer{}, cli.ErrAborted }
		p := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){aborted}}
		r = lolaP(p, "install", "--user")
		Expect(r.err).To(ContainSubstring("⚠ 1 module in profile:default is not installed → lola sync --user\n"))
	})

	It("coaches lola sync for the modules project.yml lists that are not installed", func() {
		Expect(lola("install", "review-kit", "--scope", "project", "-t", "claude-code", "-y").code).To(Equal(0))
		aborted := func(cli.ModuleStep) (cli.ModuleAnswer, error) { return cli.ModuleAnswer{}, cli.ErrAborted }
		p := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){aborted}}
		r := lolaP(p, "install", "--scope", "project")
		Expect(r.err).NotTo(ContainSubstring("not installed"), "nothing to coach when every entry is installed")

		mpath := filepath.Join(proj, ".lola", "project.yml")
		b, err := os.ReadFile(mpath)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(mpath, append(b, []byte("  - name: deploy\n    source: "+deploy+"\n")...), 0o644)).To(Succeed())
		r = lola("install", "--scope", "project")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: lola install needs a module or source\n  hint: install what project.yml lists: lola sync --project\n"),
			"off a terminal, the missing-argument error")

		p = &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){aborted}}
		r = lolaP(p, "install", "--scope", "project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("⚠ 1 module in project.yml is not installed → lola sync --project\n"))
		Expect(p.calls).To(Equal([]string{"modules"}), "said before the module picker")
	})

	It("shows in the review the findings a dry run of the install reports, with their hints", func() {
		agents := filepath.Join(tmp, "src", "agents")
		writeTree(agents, map[string]string{"lola.yml": "name: agents\n", "agents/bare.md": "A bare agent.\n"})
		p := &scripted{review: []string{cli.ReviewQuit}}
		r := lolaP(p, "install", agents, "--scope", "user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"review"}))
		Expect(p.reviewSteps[0].Summary).To(ContainSubstring("\n  → agents: 1 notice (claude-code 1) — lola check --user "+agents+" -t claude-code\n"),
			"detail is one line per module, whose lola check names the source the review may decline")
		Expect(filepath.Join(home, ".claude")).NotTo(BeADirectory(), "the dry run wrote nothing")

		p = &scripted{review: []string{cli.ReviewQuit}}
		r = lolaP(p, "install", agents, "--scope", "user", "-v")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.reviewSteps[0].Summary).To(MatchRegexp(`\n  ✗ skipped agents: claude-code agent bare: .+\n      hint: render it anyway: \S`))
		Expect(p.reviewSteps[0].Summary).NotTo(ContainSubstring("add description:"), "I-2: the hint for the module's author is lola check's")
	})

	It("keeps the run's output flags in the hints of a review's dry-run refusal", func() {
		kit := filepath.Join(tmp, "src", "kit")
		writeTree(kit, map[string]string{"lola.yml": "name: kit\n", "commands/ship.md.liquid": "Ship\n{{ targt }}\n"})
		p := &scripted{}
		r := lolaP(p, "install", kit, "--scope", "user", "--plain")
		Expect(r.code).To(Equal(1), r.err)
		Expect(p.calls).To(BeEmpty(), "the refusal comes before the review")
		Expect(r.err).To(ContainSubstring("hint: lola check --user " + kit + " --plain\n"))
	})

	It("walks from nothing to an installed set: scope, modules, assistants, components, summary, apply", func() {
		fakeAssistant(tmp, "gemini", "0.63.0")
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit", "deploy")},
			assistants: []func(cli.AssistantStep) ([]string, error){func(cli.AssistantStep) ([]string, error) { return []string{"claude-code"}, nil }},
			components: []func(cli.ComponentPage) ([]string, error){keep("skill/slow-lint")},
			review:     []string{cli.ReviewApply},
		}
		r := lolaP(p, "install")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "modules", "assistants", "components:project.yml › review-kit", "review"}),
			"deploy has one component and no exclusion, so it gets no page")

		Expect(p.scopeSteps[0]).To(Equal(cli.ScopeStep{Question: "Install where?", ProjectRoot: proj, Home: home,
			ProjectContext: "project.yml", UserContext: "default", Step: cli.Steps{At: 1, Of: 5}}), "nothing preselected")
		Expect(p.moduleSteps[0].Options).To(Equal([]cli.ModuleOption{
			{Name: "deploy", Label: deploy + " · deploy"},
			{Name: "review-kit", Label: review + " · review-kit — Code review helpers"}}))
		Expect(p.assistantSteps[0]).To(Equal(cli.AssistantStep{Options: []string{"claude-code", "gemini-cli"}, Preselect: []string{"claude-code"},
			Scopes: []string{"project (" + proj + ")"}, Step: cli.Steps{At: 3, Of: 7}, Nav: cli.Nav{Back: true, Answered: 2}}))
		pg := p.pages[0]
		Expect(pg.Groups).To(HaveLen(3))
		Expect(pg.Groups[0].Title).To(Equal("skills (2)"))
		Expect(pg.Groups[0].Options[0]).To(Equal(cli.ComponentOption{ID: "skill/review", Label: "review — Review code", Selected: true}))
		Expect(pg.Groups[2].Title).To(Equal("rules (1)"))
		Expect(p.reviewSteps[0].Options).To(Equal([]string{"apply", "scope", "modules", "assistants", "components", "quit"}))

		// Numbered across the pass: the count is a forecast until the modules are picked,
		// then exact; review-kit's page has three lists, deploy's one component no page.
		Expect([]cli.Steps{p.scopeSteps[0].Step, p.moduleSteps[0].Step, p.assistantSteps[0].Step, pg.Step, p.reviewSteps[0].Step}).To(Equal(
			[]cli.Steps{{At: 1, Of: 5}, {At: 2, Of: 5}, {At: 3, Of: 7}, {At: 4, Of: 7}, {At: 7, Of: 7}}))
		Expect(r.err).To(ContainSubstring("Summary:\n  project (" + proj + "), picked:\n    → for claude-code (picked)\n" +
			"      review-kit: + skill/review, + command/ship, + rule/style\n      deploy: + command/deploy\n"))
		Expect(r.err).To(ContainSubstring("  selections.yaml (" + selections + "):\n    review-kit in projects[" + proj + "]: skip [skill/slow-lint]\n"))
		Expect(equivalents(r.err)).To(Equal([]string{"lola install review-kit deploy --scope project -t claude-code --skip review-kit:skill/slow-lint"}))
		Expect(r.err).To(ContainSubstring("→ saved to " + selections))
		Expect(r.out).To(ContainSubstring("✓ installed review-kit (claude-code)"))
		Expect(filepath.Join(proj, ".claude/skills/slow-lint")).NotTo(BeADirectory())
		Expect(filepath.Join(proj, ".claude/commands/deploy.md")).To(BeARegularFile())
	})

	It("off a terminal, chooses agents only where .agents/skills is set up and no installed assistant is", func() {
		Expect(os.MkdirAll(filepath.Join(proj, ".agents", "skills"), 0o755)).To(Succeed())
		r := lola("install", "review-kit", "--scope", "project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✓ installed review-kit (claude-code)"))
		Expect(filepath.Join(proj, ".claude/skills/review/SKILL.md")).To(BeARegularFile())
		Expect(filepath.Join(proj, ".agents/skills/review")).NotTo(BeAnExistingFile(), "never added beside a set-up assistant")

		other := filepath.Join(tmp, "other")
		Expect(os.MkdirAll(filepath.Join(other, ".agents", "skills"), 0o755)).To(Succeed())
		r = lola("install", "review-kit", "-C", other)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("✓ installed review-kit (agents)"))
		Expect(filepath.Join(other, ".agents/skills/review/SKILL.md")).To(BeARegularFile())
		Expect(filepath.Join(other, ".claude")).NotTo(BeAnExistingFile(), "claude-code is installed but not set up there")

		bare := filepath.Join(tmp, "bare")
		Expect(os.MkdirAll(filepath.Join(bare, ".agents"), 0o755)).To(Succeed())
		r = lola("install", "review-kit", "-C", bare)
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(HaveSuffix("error: none of your installed assistants (claude-code) is set up here, so lola does not know where to install\n" +
			"  hint: lola install review-kit -C " + bare + " -t claude-code\n" +
			"  hint: lola install review-kit -C " + bare + " -t agents\n"))
	})

	It("on a terminal, offers agents after the assistants only where it is set up, and never counts it as one", func() {
		Expect(os.MkdirAll(filepath.Join(proj, ".agents", "skills"), 0o755)).To(Succeed())
		p := &scripted{components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewQuit}}
		r := lolaP(p, "install", "review-kit", "--scope", "project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"components:project.yml › review-kit", "review"}), "claude-code, the only assistant installed, needs no picker")
		Expect(p.reviewSteps[0].Summary).To(ContainSubstring("→ for claude-code (the only assistant found)\n"))

		Expect(os.RemoveAll(filepath.Join(proj, ".claude"))).To(Succeed())
		p = &scripted{assistants: []func(cli.AssistantStep) ([]string, error){func(cli.AssistantStep) ([]string, error) { return []string{"agents"}, nil }},
			components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewQuit}}
		r = lolaP(p, "install", "review-kit", "--scope", "project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.assistantSteps).To(HaveLen(1), "the one assistant installed is not set up here, and agents is: the picker asks")
		Expect(p.assistantSteps[0].Options).To(Equal([]string{"claude-code", "agents"}))
		Expect(p.assistantSteps[0].Preselect).To(Equal([]string{"agents"}))

		Expect(os.Remove(filepath.Join(tmp, "bin", "claude"))).To(Succeed())
		p = &scripted{components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewQuit}}
		r = lolaP(p, "install", "review-kit", "--scope", "project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"components:project.yml › review-kit", "review"}), "agents, the only choice, needs no picker")
		Expect(p.reviewSteps[0].Summary).To(ContainSubstring("→ for agents (set up here)\n"))

		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		fakeAssistant(tmp, "gemini", "0.63.0")
		answer := func(cli.AssistantStep) ([]string, error) { return []string{"claude-code"}, nil }
		p = &scripted{assistants: []func(cli.AssistantStep) ([]string, error){answer}, components: []func(cli.ComponentPage) ([]string, error){keep()},
			review: []string{cli.ReviewQuit}}
		r = lolaP(p, "install", "review-kit", "--scope", "project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.assistantSteps[0].Options).To(Equal([]string{"claude-code", "gemini-cli", "agents"}))
		Expect(p.assistantSteps[0].Preselect).To(Equal([]string{"claude-code"}), "agents is preselected only when nothing else is set up")

		Expect(os.RemoveAll(filepath.Join(proj, ".claude"))).To(Succeed())
		p = &scripted{assistants: []func(cli.AssistantStep) ([]string, error){answer}, components: []func(cli.ComponentPage) ([]string, error){keep()},
			review: []string{cli.ReviewQuit}}
		r = lolaP(p, "install", "review-kit", "--scope", "project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.assistantSteps[0].Options).To(Equal([]string{"claude-code", "gemini-cli", "agents"}))
		Expect(p.assistantSteps[0].Preselect).To(Equal([]string{"agents"}))

		Expect(os.RemoveAll(filepath.Join(proj, ".agents", "skills"))).To(Succeed())
		p = &scripted{assistants: []func(cli.AssistantStep) ([]string, error){answer}, components: []func(cli.ComponentPage) ([]string, error){keep()},
			review: []string{cli.ReviewQuit}}
		r = lolaP(p, "install", "review-kit", "--scope", "project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.assistantSteps[0].Options).To(Equal([]string{"claude-code", "gemini-cli"}), "agents is offered only where it is set up")
	})

	It("asks nothing of a run the flags fully specify, and reviews an assistant lola chose", func() {
		p := &scripted{}
		r := lolaP(p, "install", "deploy", "--scope", "project", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty(), "scope and assistant by flag, module by argument, one component")
		Expect(r.err).NotTo(ContainSubstring("Summary:"))

		p = &scripted{review: []string{cli.ReviewQuit}}
		r = lolaP(p, "install", "deploy", "--scope", "user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"review"}), "the only assistant installed was taken without asking")
		Expect(p.reviewSteps[0].Options).To(Equal([]string{cli.ReviewApply, cli.ReviewQuit}), "no picker ran, so there is no step to change")
		Expect(p.reviewSteps[0].Step).To(Equal(cli.Steps{At: 1, Of: 1}), "the review alone is not numbered")
		Expect(p.reviewSteps[0].Summary).To(HavePrefix("Summary:\n  user (" + home + "), flag:\n    → for claude-code (the only assistant found)\n" +
			"      deploy: + command/deploy\n"))
		Expect(r.err).To(HaveSuffix("✗ cancelled — nothing was changed\n"), "Quit cancels")
		Expect(filepath.Join(home, ".claude/commands/deploy.md")).NotTo(BeAnExistingFile())

		p = &scripted{}
		r = lolaP(p, "install", "deploy", "--scope", "user", "--force")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty(), "--force confirms what lola chose, as -y does")
		Expect(r.err).To(ContainSubstring("    → for claude-code (the only assistant found)\n"))
		Expect(filepath.Join(home, ".claude/commands/deploy.md")).To(BeARegularFile())

		p = &scripted{components: []func(cli.ComponentPage) ([]string, error){keep()}}
		r = lolaP(p, "install", "review-kit", "--scope", "project", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"components:project.yml › review-kit"}), "-y applies without the review menu")
		Expect(r.err).To(ContainSubstring("Summary:"))

		p = &scripted{components: []func(cli.ComponentPage) ([]string, error){keep()}}
		r = lolaP(p, "install", "review-kit", "--scope", "project", "--force")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"components:project.yml › review-kit"}), "--force confirms the plan as -y does (user decision, 2026-10-22)")

		By("component flags replace the component step, but a --skip that deletes installed files asks first; No keeps them")
		rule := filepath.Join(proj, ".claude/skills/review/SKILL.md")
		Expect(rule).To(BeAnExistingFile())
		p = &scripted{confirm: []bool{false}}
		r = lolaP(p, "install", "review-kit", "--scope", "project", "--skip", "skill/review", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Remove these and apply? No writes nothing"}))
		Expect(rule).To(BeAnExistingFile())
		p = &scripted{confirm: []bool{true}}
		r = lolaP(p, "install", "review-kit", "--scope", "project", "--skip", "skill/review", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"confirm:Remove these and apply? No writes nothing"}))
		Expect(rule).NotTo(BeAnExistingFile())
	})

	It("goes back a screen on Esc: the step before asks again with its answer preselected, then the flow carries on", func() {
		fakeAssistant(tmp, "gemini", "0.63.0")
		claude := func(cli.AssistantStep) ([]string, error) { return []string{"claude-code"}, nil }
		p := &scripted{
			scope: []func(cli.ScopeStep) ([]string, error){scopes("project"), scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){goneBack[cli.ModuleStep, cli.ModuleAnswer],
				mods("review-kit", "deploy"), mods("review-kit")},
			assistants: []func(cli.AssistantStep) ([]string, error){goneBack[cli.AssistantStep, []string], claude},
			components: []func(cli.ComponentPage) ([]string, error){keep("skill/slow-lint"), keep()},
			review:     []string{goBack, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "modules", "scope", "modules", "assistants", "modules", "assistants",
			"components:project.yml › review-kit", "review", "components:project.yml › review-kit", "review"}))
		Expect(p.scopeSteps[1].Preselect).To(Equal([]string{"project"}), "Esc on the modules went back to the scope, answered")
		Expect(p.moduleSteps[2].Preselect).To(Equal([]string{"review-kit", "deploy"}), "Esc on the assistants went back to the modules")
		var again []string
		for _, g := range p.pages[1].Groups {
			for _, o := range g.Options {
				if o.Selected {
					again = append(again, o.ID)
				}
			}
		}
		Expect(again).To(Equal([]string{"skill/review", "command/ship", "rule/style"}), "Esc on the review went back to the page, as answered")
		Expect(equivalents(r.err)).To(Equal([]string{"lola install review-kit --scope project -t claude-code --skip review-kit:skill/slow-lint"}))

		By("each step says whether there is a screen to go back to, and how many steps are answered")
		Expect([]cli.Nav{p.scopeSteps[0].Nav, p.moduleSteps[0].Nav, p.scopeSteps[1].Nav, p.moduleSteps[1].Nav, p.assistantSteps[0].Nav,
			p.pages[0].Nav, p.reviewSteps[0].Nav, p.pages[1].Nav, p.reviewSteps[1].Nav}).To(Equal([]cli.Nav{
			{}, {Back: true, Answered: 1}, {}, {Back: true, Answered: 1}, {Back: true, Answered: 2},
			{Back: true, Answered: 3}, {Back: true, Answered: 4}, {Back: true, Answered: 3}, {Back: true, Answered: 4}}))
	})

	It("goes back from the first component page to the assistants, and from a no to no components to its page", func() {
		fakeAssistant(tmp, "gemini", "0.63.0")
		gemini := func(cli.AssistantStep) ([]string, error) { return []string{"gemini-cli"}, nil }
		p := &scripted{
			assistants: []func(cli.AssistantStep) ([]string, error){gemini, gemini},
			components: []func(cli.ComponentPage) ([]string, error){goneBack[cli.ComponentPage, []string], pick(), pick("skill/review")},
			emptyBacks: 1,
			review:     []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", "review-kit", "--scope", "project", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"assistants", "components:project.yml › review-kit", "assistants", "components:project.yml › review-kit",
			"empty:review-kit", "components:project.yml › review-kit", "review"}))
		Expect(p.assistantSteps[0].Nav).To(Equal(cli.Nav{}), "the assistants are the run's first screen")
		Expect(p.assistantSteps[1].Preselect).To(Equal([]string{"gemini-cli"}))
		Expect(p.emptyNavs).To(Equal([]cli.Nav{{Back: true, Answered: 2}}))
		for _, g := range p.pages[2].Groups {
			for _, o := range g.Options {
				Expect(o.Selected).To(BeFalse(), "the page asks again as answered: nothing picked")
			}
		}
		Expect(equivalents(r.err)).To(Equal([]string{"lola install review-kit --scope project -t gemini-cli " +
			"--skip review-kit:skill/slow-lint --skip review-kit:command/ship --skip review-kit:rule/style"}))
	})

	It("goes back from Other directory… to the scope picker, Other ticked", func() {
		p := &scripted{
			scope:   []func(cli.ScopeStep) ([]string, error){scopes("other"), scopes("user")},
			dirs:    []func(cli.DirectoryStep) (string, error){goneBack[cli.DirectoryStep, string]},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")},
			review:  []string{cli.ReviewQuit},
		}
		r := lolaP(p, "install", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "directory", "scope", "modules", "review"}))
		Expect(p.dirSteps[0].Nav).To(Equal(cli.Nav{Back: true}))
		Expect(p.scopeSteps[1].Preselect).To(Equal([]string{"other"}))
	})

	It("re-runs a step from the review menu with its answers preselected, keeping later answers that still apply", func() {
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit", "deploy"), mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship"), keep("rule/style")},
			review:     []string{cli.ReviewComponents, cli.ReviewModules, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "modules", "components:project.yml › review-kit", "review", "components:project.yml › review-kit", "review", "modules", "review"}))
		var second []string
		for _, g := range p.pages[1].Groups {
			for _, o := range g.Options {
				if o.Selected {
					second = append(second, o.ID)
				}
			}
		}
		Expect(second).NotTo(ContainElement("command/ship"), "the re-run page starts from the last answer")
		Expect(p.moduleSteps[1].Preselect).To(Equal([]string{"review-kit", "deploy"}))
		Expect([]cli.Steps{p.pages[1].Step, p.reviewSteps[1].Step, p.moduleSteps[1].Step, p.reviewSteps[2].Step}).To(Equal(
			[]cli.Steps{{At: 1, Of: 4}, {At: 4, Of: 4}, {At: 1, Of: 3}, {At: 2, Of: 2}}), "a pass after a Change entry numbers only the steps it asks")
		Expect(equivalents(r.err)).To(Equal([]string{"lola install review-kit --scope project -t claude-code --skip review-kit:command/ship --skip review-kit:rule/style"}))
		Expect(selections).NotTo(BeAnExistingFile(), "a dry run writes nothing")
		Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())
	})

	It("installs at every scope after Change scope adds one, with a page for the added scope only", func() {
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project"), scopes("project", "user")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit", "deploy")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship"), keep()},
			review:     []string{cli.ReviewScope, cli.ReviewApply},
		}
		r := lolaP(p, "install")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "modules", "components:project.yml › review-kit", "review", "scope", "components:default (base) › review-kit — user", "review"}))
		Expect(p.scopeSteps[1].Preselect).To(Equal([]string{"project"}))
		Expect(filepath.Join(proj, ".claude/commands/deploy.md")).To(BeARegularFile())
		Expect(filepath.Join(home, ".claude/commands/deploy.md")).To(BeARegularFile())
		Expect(filepath.Join(proj, ".claude/commands/ship.md")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(home, ".claude/commands/ship.md")).To(BeARegularFile())
	})

	It("keeps the versions the pickers listed when Change scope asks again", func() {
		p := &scripted{
			scope: []func(cli.ScopeStep) ([]string, error){scopes("project"), scopes("project")},
			components: []func(cli.ComponentPage) ([]string, error){func(pg cli.ComponentPage) ([]string, error) {
				writeTree(review, map[string]string{"commands/ship.md": "Changed.\n"})
				return keep()(pg)
			}},
			review: []string{cli.ReviewScope, cli.ReviewApply},
		}
		r := lolaP(p, "install", review)
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "components:project.yml › review-kit", "review", "scope", "review"}))
		b, err := os.ReadFile(filepath.Join(proj, ".claude/commands/ship.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("Ship."), "the source was not fetched again")
	})

	It("does not preselect an installed module at first, and keeps it when the user picked it", func() {
		Expect(lola("install", "deploy", "--scope", "project").code).To(Equal(0))
		p := &scripted{
			scope: []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){
				func(st cli.ModuleStep) (cli.ModuleAnswer, error) {
					Expect(st.Preselect).To(BeEmpty())
					return cli.ModuleAnswer{Names: []string{"deploy", "review-kit"}}, nil
				},
				func(st cli.ModuleStep) (cli.ModuleAnswer, error) {
					Expect(st.Preselect).To(Equal([]string{"deploy", "review-kit"}))
					return cli.ModuleAnswer{Names: st.Preselect}, nil
				},
			},
			components: []func(cli.ComponentPage) ([]string, error){keep()},
			review:     []string{cli.ReviewModules, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(equivalents(r.err)).To(Equal([]string{"lola install deploy review-kit --scope project"}), "the manifest fixes the targets now")
		Expect(r.err).To(ContainSubstring("    → for claude-code (from .lola/project.yml)\n"))
	})

	It("drops the component picks of a module removed in the review, and says so", func() {
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit", "deploy"), mods("deploy")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship")},
			review:     []string{cli.ReviewModules, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("  3 component picks dropped: module removed\n"), "one per picked component of review-kit")
		Expect(equivalents(r.err)[0]).To(Equal("lola install deploy --scope project -t claude-code"))
	})

	DescribeTable("cancel writes nothing and exits 0",
		func(p *scripted) {
			r := lolaP(p, "install")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.err).To(HaveSuffix("✗ cancelled — nothing was changed\n"))
			Expect(selections).NotTo(BeAnExistingFile())
			Expect(filepath.Join(proj, ".lola")).NotTo(BeADirectory())
			Expect(filepath.Join(proj, ".claude/commands")).NotTo(BeADirectory())
		},
		Entry("Ctrl+C at a picker", &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){func(cli.ModuleStep) (cli.ModuleAnswer, error) { return cli.ModuleAnswer{}, cli.ErrAborted }}}),
		Entry("Quit in the review menu", &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")}, review: []string{cli.ReviewQuit}}),
		Entry("Ctrl+C at the Other directory path", &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes("project", "other")},
			dirs: []func(cli.DirectoryStep) (string, error){func(cli.DirectoryStep) (string, error) { return "", cli.ErrAborted }}}),
	)

	It("preselects nothing on a terminal, though default_scope is set and the project uses lola", func() {
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Dir(selections), 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(filepath.Dir(selections), "config.yaml"), []byte("version: 1\ndefault_scope: user\n"), 0o600)).To(Succeed())
		p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){func(st cli.ScopeStep) ([]string, error) { return nil, cli.ErrAborted }}}
		r := lolaP(p, "install")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.scopeSteps[0]).To(Equal(cli.ScopeStep{Question: "Install where?", ProjectRoot: proj, Home: home,
			ProjectContext: "project.yml", UserContext: "default", Step: cli.Steps{At: 1, Of: 5}}))
	})

	It("asks for the Other directory again, with the reason, until it is a directory that is not the project above", func() {
		writeTree(proj, map[string]string{"notes.txt": "x"})
		other := filepath.Join(tmp, "other")
		Expect(os.MkdirAll(other, 0o755)).To(Succeed())
		p := &scripted{
			scope:   []func(cli.ScopeStep) ([]string, error){scopes("project", "other")},
			dirs:    []func(cli.DirectoryStep) (string, error){dir(""), dir("~/elsewhere"), dir("~nobody/x"), dir("missing"), dir("notes.txt"), dir("."), dir(proj + "/"), dir("../other")},
			confirm: []bool{false, false},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")},
			review:  []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		var confirms []string
		for _, c := range p.calls {
			if strings.HasPrefix(c, "confirm:") {
				confirms = append(confirms, c)
			}
		}
		Expect(confirms).To(Equal([]string{"confirm:Create " + filepath.Join(home, "elsewhere") + "? It does not exist yet",
			"confirm:Create " + filepath.Join(proj, "missing") + "? It does not exist yet"}), "never offered for a literal ~nobody/x")
		Expect(filepath.Join(home, "elsewhere")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(proj, "missing")).NotTo(BeAnExistingFile())
		var problems []string
		for _, st := range p.dirSteps {
			problems = append(problems, st.Problem)
		}
		Expect(problems).To(Equal([]string{"", "enter the path of a directory",
			filepath.Join(home, "elsewhere") + " does not exist",
			filepath.Join(proj, "~nobody/x") + " does not exist (only ~ and ~/ are expanded)",
			filepath.Join(proj, "missing") + " does not exist",
			filepath.Join(proj, "notes.txt") + " is not a directory",
			"that is the project above", "that is the project above"}))
		Expect(p.dirSteps[4].Value).To(Equal("missing"), "the answer is offered again to edit")
		Expect(filepath.Join(proj, ".claude/commands/deploy.md")).To(BeARegularFile())
		Expect(filepath.Join(other, ".claude/commands/deploy.md")).To(BeARegularFile())
		Expect(r.err).To(ContainSubstring("  project (" + other + "), picked:\n"))
	})

	It("creates an Other directory that does not exist yet on your yes, and installs there", func() {
		fresh := filepath.Join(proj, "new", "app")
		p := &scripted{
			scope:   []func(cli.ScopeStep) ([]string, error){scopes("other")},
			dirs:    []func(cli.DirectoryStep) (string, error){dir("new/app")},
			confirm: []bool{true},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")},
			review:  []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(ContainElement("confirm:Create " + fresh + "? It does not exist yet"))
		Expect(p.dirSteps).To(HaveLen(1), "accepted without asking again")
		Expect(filepath.Join(fresh, ".claude/commands/deploy.md")).To(BeARegularFile())
	})

	It("removes an Other directory it created when you quit at the review", func() {
		p := &scripted{
			scope:   []func(cli.ScopeStep) ([]string, error){scopes("other")},
			dirs:    []func(cli.DirectoryStep) (string, error){dir("new/app")},
			confirm: []bool{true},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")},
			review:  []string{cli.ReviewQuit},
		}
		r := lolaP(p, "install", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(ContainElement("confirm:Create " + filepath.Join(proj, "new", "app") + "? It does not exist yet"))
		Expect(filepath.Join(proj, "new")).NotTo(BeAnExistingFile(), "the parents it made go too")
		Expect(r.err).To(ContainSubstring("removed " + filepath.Join(proj, "new", "app") + " (lola created it; nothing was installed there)\n"))
	})

	It("removes an Other directory it created when you go back and install elsewhere", func() {
		p := &scripted{
			scope:   []func(cli.ScopeStep) ([]string, error){scopes("other"), scopes("project")},
			dirs:    []func(cli.DirectoryStep) (string, error){dir("new/app")},
			confirm: []bool{true},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){goneBack[cli.ModuleStep, cli.ModuleAnswer], mods("deploy")},
			review:  []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(proj, ".claude/commands/deploy.md")).To(BeARegularFile())
		Expect(filepath.Join(proj, "new")).NotTo(BeAnExistingFile())
		Expect(r.err).To(ContainSubstring("removed " + filepath.Join(proj, "new", "app") + " (lola created it; nothing was installed there)\n"))
	})

	Describe("-C names a directory that does not exist yet", func() {
		var fresh string
		BeforeEach(func() { fresh = filepath.Join(tmp, "fresh", "app") })

		It("plans a dry run as for the empty directory it would create, and creates nothing", func() {
			for _, yes := range [][]string{nil, {"-y"}} {
				r := lola(slices.Concat([]string{"install", "deploy", "-C", fresh, "-t", "claude-code", "-n"}, yes)...)
				Expect(r.code).To(Equal(0), r.err)
				Expect(r.err).To(HavePrefix("would create " + fresh + "\n"))
				Expect(r.out + r.err).To(ContainSubstring("create claude-code .claude/commands/deploy.md"))
				Expect(filepath.Join(tmp, "fresh")).NotTo(BeAnExistingFile(), "%v", yes)
			}
			r := lola("install", "deploy", "-C", fresh, "-t", "claude-code", "-n", "--json")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.err).To(BeEmpty())
			Expect(filepath.Join(tmp, "fresh")).NotTo(BeAnExistingFile())
		})

		It("creates it under -y or --force, says so, and installs there", func() {
			for _, flag := range []string{"-y", "--force"} {
				Expect(os.RemoveAll(filepath.Join(tmp, "fresh"))).To(Succeed())
				r := lola("install", "deploy", "-C", fresh, "-t", "claude-code", flag)
				Expect(r.code).To(Equal(0), r.err)
				Expect(r.err).To(HavePrefix("created " + fresh + "\n"))
				Expect(filepath.Join(fresh, ".claude/commands/deploy.md")).To(BeARegularFile(), flag)
			}
		})

		It("on a terminal, asks before creating it and installs on your yes", func() {
			p := &scripted{confirm: []bool{true}, review: []string{cli.ReviewApply}}
			r := lolaP(p, "install", "deploy", "-C", fresh, "-t", "claude-code")
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.calls[0]).To(Equal("confirm:Create " + fresh + "? It does not exist yet"))
			Expect(p.confirmNavs[0]).To(Equal(cli.Nav{}))
			Expect(r.err).To(HavePrefix("created " + fresh + "\n"))
			Expect(filepath.Join(fresh, ".claude/commands/deploy.md")).To(BeARegularFile())
		})

		It("on a terminal, your no creates nothing and exits 0", func() {
			p := &scripted{confirm: []bool{false}}
			r := lolaP(p, "install", "deploy", "-C", fresh, "-t", "claude-code")
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.err).To(HaveSuffix("✗ cancelled — nothing was changed\n"))
			Expect(p.calls).To(Equal([]string{"confirm:Create " + fresh + "? It does not exist yet"}))
			Expect(filepath.Join(tmp, "fresh")).NotTo(BeAnExistingFile())
		})

		It("off a terminal, refuses with the -y run and the mkdir, quoted for the shell, as text and JSON", func() {
			spaced := filepath.Join(tmp, "fresh", "my app")
			r := lola("install", "deploy", "-C", spaced, "-t", "claude-code")
			Expect(r.code).To(Equal(1), r.err)
			Expect(r.err).To(HaveSuffix("error: -C " + spaced + " does not exist\n" +
				"  hint: rerun with -y to create it: lola install deploy -C '" + spaced + "' -t claude-code -y\n" +
				"  hint: or create it first: mkdir -p '" + spaced + "'\n"))

			r = lola("install", "deploy", "-C", fresh, "-t", "claude-code", "--json")
			Expect(r.code).To(Equal(1), r.err)
			var refusal struct {
				Error string   `json:"error"`
				Hints []string `json:"hints"`
			}
			Expect(json.Unmarshal([]byte(r.err), &refusal)).To(Succeed(), r.err)
			Expect(refusal.Error).To(Equal("-C " + fresh + " does not exist"))
			Expect(refusal.Hints).To(Equal([]string{"rerun with -y to create it: lola install deploy --json -C " + fresh + " -t claude-code -y",
				"or create it first: mkdir -p " + fresh}))
			Expect(filepath.Join(tmp, "fresh")).NotTo(BeAnExistingFile())
		})

		It("refuses a scope flag that conflicts with -C before asking or creating anything", func() {
			for _, extra := range [][]string{{"--user", "-y"}, {"--user", "-n"}, {"--user"}} {
				p := &scripted{}
				r := lolaP(p, slices.Concat([]string{"install", "deploy", "-C", fresh, "-t", "claude-code"}, extra)...)
				Expect(r.code).To(Equal(1), r.err)
				Expect(r.err).To(HaveSuffix("error: --user cannot be combined with -C/--root\n  hint: user scope has no project directory\n"), "%v", extra)
				Expect(p.calls).To(BeEmpty(), "%v", extra)
				Expect(filepath.Join(tmp, "fresh")).NotTo(BeAnExistingFile(), "%v", extra)
			}
			r := lola("install", "deploy", "-C", fresh, "--scope", "user", "-y")
			Expect(r.code).To(Equal(1), r.err)
			Expect(r.err).To(ContainSubstring("error: --scope user cannot take -C"))
			Expect(filepath.Join(tmp, "fresh")).NotTo(BeAnExistingFile())
		})

		It("refuses as before a path that cannot be shown as it is, creating nothing", func() {
			evil := filepath.Join(tmp, "evil\x1b[2J")
			p := &scripted{}
			r := lolaP(p, "install", "deploy", "-C", evil, "-t", "claude-code", "-y")
			Expect(r.code).To(Equal(1), r.err)
			Expect(r.err).To(HaveSuffix("error: -C " + filepath.Join(tmp, "evil?[2J") + ": not a directory\n"))
			Expect(p.calls).To(BeEmpty())
			Expect(evil).NotTo(BeAnExistingFile())
		})

		It("coaches a directory it cannot create, and creates nothing", func() {
			if os.Geteuid() == 0 {
				Skip("root writes into a read-only directory")
			}
			locked := filepath.Join(tmp, "locked")
			Expect(os.Mkdir(locked, 0o555)).To(Succeed())
			DeferCleanup(os.Chmod, locked, os.FileMode(0o755))
			want := filepath.Join(locked, "app")
			r := lola("install", "deploy", "-C", want, "-t", "claude-code", "-y")
			Expect(r.code).To(Equal(1), r.err)
			Expect(r.err).To(HaveSuffix("error: mkdir " + want + ": permission denied\n  hint: create it first: mkdir -p " + want + "\n"))
			Expect(want).NotTo(BeAnExistingFile())
		})

		It("keeps refusing a path under a regular file, creating nothing", func() {
			writeTree(proj, map[string]string{"notes.txt": "x"})
			r := lola("install", "deploy", "-C", "notes.txt/sub", "-t", "claude-code", "-y")
			Expect(r.code).To(Equal(1), r.err)
			Expect(r.err).To(HaveSuffix("error: -C notes.txt/sub: not a directory\n"))
			Expect(filepath.Join(proj, "notes.txt")).To(BeARegularFile())
		})

		It("is install's alone: another command refuses it as before", func() {
			r := lola("tidy", "-C", fresh, "-y")
			Expect(r.code).To(Equal(1), r.err)
			Expect(r.err).To(ContainSubstring("-C " + fresh + ": not a directory"))
			Expect(filepath.Join(tmp, "fresh")).NotTo(BeAnExistingFile())
		})
	})

	It("asks again when Other directory or Project is the home directory, User ticked or not", func() {
		for _, answer := range [][]string{{"other"}, {"user", "other"}} {
			p := &scripted{
				scope: []func(cli.ScopeStep) ([]string, error){scopes(answer...)},
				dirs:  []func(cli.DirectoryStep) (string, error){dir(home), func(cli.DirectoryStep) (string, error) { return "", cli.ErrAborted }},
			}
			r := lolaP(p, "install")
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.dirSteps[1].Problem).To(Equal("that is your user scope (~); tick User instead"), fmt.Sprint(answer))
		}

		Expect(os.MkdirAll(home, 0o755)).To(Succeed())
		GinkgoT().Chdir(home)
		for _, answer := range [][]string{{"project"}, {"project", "user"}} {
			p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes(answer...), func(cli.ScopeStep) ([]string, error) { return nil, cli.ErrAborted }}}
			r := lolaP(p, "install")
			Expect(r.code).To(Equal(0), r.err)
			Expect(p.scopeSteps[1].Problem).To(Equal("the project here is your home directory; tick User instead"), fmt.Sprint(answer))
		}
	})

	It("says why a path that cannot be read is refused, past the cut of a long path", func() {
		p := &scripted{
			scope: []func(cli.ScopeStep) ([]string, error){scopes("other")},
			dirs:  []func(cli.DirectoryStep) (string, error){dir(strings.Repeat("a", 300)), func(cli.DirectoryStep) (string, error) { return "", cli.ErrAborted }},
		}
		lolaP(p, "install")
		Expect(p.dirSteps[1].Problem).To(HaveSuffix(": file name too long"))
		Expect(p.dirSteps[1].Problem).To(ContainSubstring("…"), "the path is cut")
	})

	It("takes the project directory as Other when Project is not picked", func() {
		p := &scripted{
			scope:   []func(cli.ScopeStep) ([]string, error){scopes("user", "other")},
			dirs:    []func(cli.DirectoryStep) (string, error){dir(".")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")},
			review:  []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(equivalents(r.err)).To(Equal([]string{
			"lola install deploy --scope project -C " + proj + " -t claude-code",
			"lola install deploy --scope user -t claude-code"}), "project roots first, then user")
	})

	It("shows an Other directory holding control characters made safe", func() {
		evil := filepath.Join(tmp, "evil\x1b[2J")
		p := &scripted{
			scope: []func(cli.ScopeStep) ([]string, error){scopes("other")},
			dirs:  []func(cli.DirectoryStep) (string, error){dir(evil), func(cli.DirectoryStep) (string, error) { return "", cli.ErrAborted }},
		}
		lolaP(p, "install")
		Expect(p.confirmNavs).To(BeEmpty(), "never offered to create a path that is not shown as it is")
		Expect(evil).NotTo(BeAnExistingFile())
		Expect(p.dirSteps[1].Problem).To(Equal(filepath.Join(tmp, "evil?[2J") + " does not exist"))
		Expect(p.dirSteps[1].Value).To(BeEmpty(), "an unsafe answer is not offered again")
	})

	It("installs into Project, Other and User with a page per destination when their picks differ, and one shared page when equal", func() {
		other := filepath.Join(tmp, "other")
		Expect(os.MkdirAll(other, 0o755)).To(Succeed())
		def := filepath.Join(tmp, "config", "lola", "profiles", "default.yml")
		writeTree(filepath.Dir(def), map[string]string{"default.yml": "version: 1\nmodules:\n  - {name: review-kit, source: " + review + ", skip: [rule/style]}\n"})
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project", "user", "other")},
			dirs:       []func(cli.DirectoryStep) (string, error){dir(other)},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship"), keep("skill/slow-lint"), keep()},
			review:     []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "directory", "modules", "components:project.yml › review-kit — project (" + proj + ")",
			"components:project.yml › review-kit — project (" + other + ")", "components:default (base) › review-kit — user", "review"}))
		Expect(r.err).To(ContainSubstring("Summary:\n  project (" + proj + "), picked:\n"))
		Expect(r.err).To(ContainSubstring("\n  project (" + other + "), picked:\n"))
		Expect(r.err).To(ContainSubstring("\n  user (" + home + "), picked:\n    → for claude-code (from -t)\n"))
		Expect(equivalents(r.err)).To(Equal([]string{
			"lola install review-kit --scope project -C " + proj + " -t claude-code --skip review-kit:command/ship",
			"lola install review-kit --scope project -C " + other + " -t claude-code --skip review-kit:skill/slow-lint",
			"lola install review-kit --scope user -t claude-code"}))
		b, err := os.ReadFile(selections)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("  " + proj + ":\n    review-kit:\n      skip: [command/ship]\n"))
		Expect(string(b)).To(ContainSubstring("  " + other + ":\n    review-kit:\n      skip: [skill/slow-lint]\n"))
		Expect(filepath.Join(proj, ".claude/commands/ship.md")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(other, ".claude/skills/slow-lint")).NotTo(BeADirectory())
		Expect(filepath.Join(other, ".claude/commands/ship.md")).To(BeARegularFile())
		Expect(filepath.Join(home, ".claude/commands/ship.md")).To(BeARegularFile())
		projSteps, otherSteps, ok := strings.Cut(r.out, "project ("+other+"):\n")
		Expect(ok).To(BeTrue(), r.out)
		Expect(projSteps).NotTo(ContainSubstring("commands/ship.md"), "each root lists its own steps")
		Expect(otherSteps).To(ContainSubstring("commands/ship.md"))

		Expect(os.Remove(selections)).To(Succeed())
		Expect(os.Remove(def)).To(Succeed())
		p = &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project", "user", "other")},
			dirs:       []func(cli.DirectoryStep) (string, error){dir(other)},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("skill/review")},
			review:     []string{cli.ReviewApply},
		}
		r = lolaP(p, "install", "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "directory", "modules", "components:review-kit", "review"}))
		Expect(equivalents(r.err)).To(HaveLen(3))
	})

	It("keeps the picks of the destinations Change scope keeps, and drops and counts the rest", func() {
		other := filepath.Join(tmp, "other")
		Expect(os.MkdirAll(other, 0o755)).To(Succeed())
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project", "other", "user"), scopes("project", "user")},
			dirs:       []func(cli.DirectoryStep) (string, error){dir("../other")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship")},
			review:     []string{cli.ReviewScope, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "directory", "modules", "components:review-kit", "review", "scope", "review"}))
		Expect(p.scopeSteps[1].Preselect).To(Equal([]string{"project", "other", "user"}))
		Expect(r.err).To(ContainSubstring("  3 component picks dropped: scope removed\n"))
		Expect(equivalents(r.err)).To(Equal([]string{
			"lola install review-kit --scope project -C " + proj + " -t claude-code --skip review-kit:command/ship",
			"lola install review-kit --scope user -t claude-code --skip review-kit:command/ship"}))
	})

	It("offers the last Other directory again when Change scope asks", func() {
		other := filepath.Join(tmp, "other")
		Expect(os.MkdirAll(other, 0o755)).To(Succeed())
		p := &scripted{
			scope:   []func(cli.ScopeStep) ([]string, error){scopes("other"), scopes("other")},
			dirs:    []func(cli.DirectoryStep) (string, error){dir("../other"), func(st cli.DirectoryStep) (string, error) { return st.Value, nil }},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")},
			review:  []string{cli.ReviewScope, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.dirSteps[1]).To(Equal(cli.DirectoryStep{Value: "../other", Nav: cli.Nav{Back: true, Answered: 1}}),
			"Esc goes back to the scope picker; the module picker's answer is held")
		Expect(equivalents(r.err)).To(Equal([]string{"lola install deploy --scope project -C " + other + " -t claude-code"}))
	})

	It("gives each destination its own page when their preselections differ, and one shared page otherwise", func() {
		def := filepath.Join(tmp, "config", "lola", "profiles", "default.yml")
		writeTree(filepath.Dir(def), map[string]string{"default.yml": "version: 1\nmodules:\n  - {name: review-kit, source: " + review + ", skip: [rule/style]}\n"})
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project", "user")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship"), keep()},
			review:     []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "modules", "components:project.yml › review-kit — project (" + proj + ")", "components:default (base) › review-kit — user", "review"}))
		Expect(p.pages[1].Groups[2].Options[0].Label).To(Equal("style — Be brief. (skipped in default (base))"))
		Expect(equivalents(r.err)).To(Equal([]string{
			"lola install review-kit --scope project -C " + proj + " -t claude-code --skip review-kit:command/ship",
			"lola install review-kit --scope user -t claude-code"}))
		Expect(filepath.Join(proj, ".claude/commands/ship.md")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(home, ".claude/commands/ship.md")).To(BeARegularFile())

		Expect(os.Remove(selections)).To(Succeed())
		Expect(os.Remove(def)).To(Succeed())
		p = &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project", "user")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy", "review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("skill/slow-lint")},
			review:     []string{cli.ReviewApply},
		}
		r = lolaP(p, "install", "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "modules", "components:review-kit", "review"}))
		Expect(equivalents(r.err)).To(Equal([]string{
			"lola install deploy review-kit --scope project -C " + proj + " -t claude-code --skip review-kit:skill/slow-lint",
			"lola install deploy review-kit --scope user -t claude-code --skip review-kit:skill/slow-lint"}))
	})

	It("shows rules disabled under --no-rules and the runtime hooks line", func() {
		writeTree(review, map[string]string{"lola.yml": "name: review-kit\ncomponents:\n  runtime_hooks: [{path: hooks.json}]\n",
			"hooks.json": `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"}]}]}}`})
		Expect(lola("cache", "add", review).code).To(Equal(0))
		p := &scripted{components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", "review-kit", "--scope", "project", "--no-rules", "-n")
		Expect(r.code).To(Equal(0), r.err)
		rules := p.pages[0].Groups[2]
		Expect(rules.Disabled).To(BeTrue())
		Expect(rules.Note).To(Equal("(off: --no-rules)"))
		Expect(rules.Options[0].Selected).To(BeFalse())
		Expect(p.pages[0].Footer).To(Equal("Runtime hooks: 1 (you will be asked before they are installed)"))
		Expect(equivalents(r.err)).To(Equal([]string{"lola install review-kit --scope project -t claude-code --no-rules"}))
	})

	It("says on the component page which only: list decides, and shows --exact's list in the summary", func() {
		Expect(os.MkdirAll(filepath.Dir(selections), 0o700)).To(Succeed())
		Expect(os.WriteFile(selections, []byte("version: 1\nprojects:\n  "+proj+":\n    review-kit:\n      only: [skill/review]\n"), 0o600)).To(Succeed())
		p := &scripted{components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", "review-kit", "--scope", "project", "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.pages[0].Footer).To(Equal("only: in selections.yaml projects[" + proj + "] — components a new version adds are not installed"))

		Expect(os.Remove(selections)).To(Succeed())
		p = &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")}, review: []string{cli.ReviewApply}}
		r = lolaP(p, "install", "-t", "claude-code", "-n", "--only", "review-kit:skill/review", "--exact")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("    review-kit in projects[" + proj + "]: only [skill/review]\n"))
		Expect(equivalents(r.err)).To(Equal([]string{"lola install review-kit --scope project -t claude-code --only review-kit:skill/review --exact"}))
	})

	It("asks scopes on separate pages when only one of them has an only: list, though the same components are picked", func() {
		Expect(os.MkdirAll(filepath.Dir(selections), 0o700)).To(Succeed())
		Expect(os.WriteFile(selections, []byte("version: 1\nprojects:\n  "+proj+":\n    review-kit:\n      only: [skill/*, command/*, agent/*, mcp/*, rule/*]\n"), 0o600)).To(Succeed())
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project", "user")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep(), keep()},
			review:     []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.pages).To(HaveLen(2))
		Expect(p.pages[0].Footer).To(ContainSubstring("only: in selections.yaml projects[" + proj + "]"))
		Expect(p.pages[1].Footer).NotTo(ContainSubstring("only:"))
	})

	It("gives no page to a module whose only components are rules that are off", func() {
		house := filepath.Join(tmp, "src", "house-rules")
		writeTree(house, map[string]string{"lola.yml": "name: house-rules\n", "rules/brief.md": "Be brief.\n", "rules/kind.md": "Be kind.\n"})
		Expect(lola("cache", "add", house).code).To(Equal(0))
		p := &scripted{}
		r := lolaP(p, "install", "house-rules", "--scope", "project", "--no-rules", "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty(), "there is nothing to pick")
	})

	It("refuses --ref or --path without a source argument, before asking anything", func() {
		for _, flag := range []string{"--ref", "--path"} {
			p := &scripted{}
			r := lolaP(p, "install", flag, "v1")
			Expect(r.code).NotTo(Equal(0))
			Expect(r.err).To(Equal("error: "+flag+" applies to a source argument, and none was given: name it, as in lola install <source> "+
				flag+" <value>\n"), "no command would do it, so no hint")
			Expect(p.calls).To(BeEmpty())
		}
	})

	It("shows a project path holding control characters made safe", func() {
		evil := filepath.Join(tmp, "evil\x1b[2J")
		Expect(os.MkdirAll(filepath.Join(evil, ".lola"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(evil, ".claude"), 0o755)).To(Succeed())
		GinkgoT().Chdir(evil)
		safe := filepath.Join(tmp, "evil?[2J")
		r := lola("install", "deploy", "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("project " + safe + ": project.yml\n"))
		Expect(r.err).NotTo(ContainSubstring("\x1b"))

		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship")},
			review:     []string{cli.ReviewApply},
		}
		r = lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.scopeSteps[0].ProjectRoot).To(Equal(safe))
		Expect(r.err).To(ContainSubstring("  project (" + safe + "), picked:\n"))
		Expect(r.err).To(ContainSubstring("    review-kit in projects[" + safe + "]: skip [command/ship]\n"))
		Expect(r.err).NotTo(ContainSubstring("\x1b"))
	})

	It("asks before installing a module with no components; No drops it from the run", func() {
		p := &scripted{
			components: []func(cli.ComponentPage) ([]string, error){pick()},
			empty:      []bool{false},
		}
		r := lolaP(p, "install", "review-kit", "--scope", "project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"components:project.yml › review-kit", "empty:review-kit"}))
		Expect(r.err).To(HaveSuffix("✗ cancelled — nothing was changed\n"), "nothing is left to install")
		Expect(selections).NotTo(BeAnExistingFile())

		p = &scripted{components: []func(cli.ComponentPage) ([]string, error){pick()}, empty: []bool{true}, review: []string{cli.ReviewApply}}
		r = lolaP(p, "install", "review-kit", "--scope", "project")
		Expect(r.code).To(Equal(0), r.err)
		b, err := os.ReadFile(selections)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("skip: [skill/review, skill/slow-lint, command/ship, rule/style]"))
	})

	It("installs the version the pickers listed, though the source changed meanwhile", func() {
		p := &scripted{
			components: []func(cli.ComponentPage) ([]string, error){func(pg cli.ComponentPage) ([]string, error) {
				writeTree(review, map[string]string{"commands/late.md": "Late.\n", "commands/ship.md": "Changed.\n"})
				return keep()(pg)
			}},
			review: []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", review, "--scope", "project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(proj, ".claude/commands/late.md")).NotTo(BeAnExistingFile())
		b, err := os.ReadFile(filepath.Join(proj, ".claude/commands/ship.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("Ship."))
		Expect(equivalents(r.err)).To(Equal([]string{"lola install " + review + " --scope project -t claude-code"}))
	})

	It("installs the cached version a picker listed, though the cache moved on meanwhile", func() {
		p := &scripted{
			scope:   []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")},
			review:  []string{cli.ReviewApply},
		}
		p.modules[0] = func(cli.ModuleStep) (cli.ModuleAnswer, error) {
			writeTree(deploy, map[string]string{"commands/deploy.md": "Deploy v2.\n"})
			Expect(lola("cache", "update", "deploy").code).To(Equal(0))
			return cli.ModuleAnswer{Names: []string{"deploy"}}, nil
		}
		r := lolaP(p, "install")
		Expect(r.code).To(Equal(0), r.err)
		b, err := os.ReadFile(filepath.Join(proj, ".claude/commands/deploy.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("Deploy.\n"))
	})

	It("says already up to date when nothing would change", func() {
		Expect(lola("install", "deploy", "--scope", "project").code).To(Equal(0))
		p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes("project")}, modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")}}
		r := lolaP(p, "install")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("✓ already up to date\n"))
		Expect(p.moduleSteps[0].Options[0]).To(Equal(cli.ModuleOption{Name: "deploy", Label: deploy + " · deploy (installed)"}))
	})

	It("repairs a missing file and reports a hand-edited one instead of saying already up to date", func() {
		Expect(lola("install", "deploy", "--scope", "project").code).To(Equal(0))
		rendered := filepath.Join(proj, ".claude/commands/deploy.md")
		Expect(os.Remove(rendered)).To(Succeed())
		p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes("project")}, modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")},
			review: []string{cli.ReviewApply}}
		r := lolaP(p, "install")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).NotTo(ContainSubstring("already up to date"))
		Expect(r.out).To(ContainSubstring("(was missing; rewritten)"))
		Expect(rendered).To(BeARegularFile())

		Expect(os.WriteFile(rendered, []byte("Hand-edited.\n"), 0o644)).To(Succeed())
		p = &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes("project")}, modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")},
			review: []string{cli.ReviewApply}}
		r = lolaP(p, "install")
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(ContainSubstring("hand-edited since lola wrote it"))
	})

	It("goes through the engine under --force though nothing changed", func() {
		Expect(lola("install", "deploy", "--scope", "project").code).To(Equal(0))
		p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){scopes("project")}, modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")},
			review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", "--force")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).NotTo(ContainSubstring("already up to date"))
		Expect(r.out).To(ContainSubstring("✓ installed deploy (claude-code)"))
	})

	It("an install with --profile base of a module the active profile lists saves default's entry, says so, and installs nothing of it", func() {
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  - {name: review-kit, source: " + review + ", rules: false}\n  - {name: deploy, source: " + deploy + ", targets: [claude-code]}\n"})
		Expect(lola("profile", "go", "--on", "--user", "-y").code).To(Equal(0))
		style := filepath.Join(home, ".claude", "rules", "lola", "review-kit", "style.md")
		Expect(style).NotTo(BeAnExistingFile(), "go's entry turns rules off")
		listed := func() string {
			GinkgoHelper()
			r := lola("list", "--user", "--plain")
			Expect(r.code).To(Equal(0), r.err)
			return r.out
		}
		before := listed()
		Expect(before).To(ContainSubstring("\tprofile:user:go\t\n"))

		r := lola("install", "review-kit", "--scope", "user", "-t", "claude-code", "--profile", "base", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("⚠ review-kit: profile go's entry wins; this change to default (base) applies when it is off\n"))
		man, err := os.ReadFile(filepath.Join(p.UserProfiles, "default.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(man)).To(ContainSubstring("name: review-kit"), "the entry is saved")
		Expect(style).NotTo(BeAnExistingFile(), "nothing of default's entry is installed")
		Expect(listed()).To(Equal(before), "go's entry stays installed")

		// On a terminal: deploy's components are go's already, but the entry is a change, so the
		// review is shown and applying it saves the entry, never "already up to date". The review
		// says, before Apply, that go's entry wins and nothing of deploy is installed: no
		// component diff of go's copy is offered as a change.
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")}, review: []string{cli.ReviewApply}}
		r = lolaP(sp, "install", "--scope", "user", "-t", "claude-code", "--profile", "base")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.reviewSteps).To(HaveLen(1))
		Expect(sp.reviewSteps[0].Summary).To(HavePrefix("Summary:\n  user (" + home + "), flag:\n    → for claude-code (from -t)\n" +
			"      deploy: saves default (base)'s entry; installs nothing while profile go's entry wins\n" +
			"  ⚠ deploy: profile go's entry wins; this change to default (base) applies when it is off\n" +
			"Equivalent commands:\n  lola install deploy --scope user -t claude-code --profile base\n"))
		Expect(r.out).NotTo(ContainSubstring("already up to date"))
		Expect(r.out).To(ContainSubstring("⚠ deploy: profile go's entry wins; this change to default (base) applies when it is off\n"))
		man, err = os.ReadFile(filepath.Join(p.UserProfiles, "default.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(man)).To(ContainSubstring("name: deploy"), "the entry is saved")
		Expect(listed()).To(Equal(before))
	})

	It("reviews an install with --profile base while a remote profile that lists the module is on, naming the base and the winner", func() {
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		// A remote profile as lola profile --add keeps it, on at user scope.
		body := "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: deploy, source: https://example.com/deploy.git, commit: " +
			strings.Repeat("a", 40) + ", digest: sha256:" + strings.Repeat("b", 64) + "}\n"
		o, err := json.Marshal(profile.Origin{Version: profile.OriginVersion, Source: "https://example.com/profiles.git",
			Commit: strings.Repeat("c", 40), Digest: profile.Digest([]byte(body))})
		Expect(err).NotTo(HaveOccurred())
		writeTree(filepath.Join(p.RemoteProfiles, "team"), map[string]string{profile.RemoteFile: body, profile.OriginFile: string(o)})
		led := ledger.New()
		led.Profile = &ledger.Active{Name: "remote:team", By: ledger.ByOn}
		Expect(led.Save(p.UserLedger)).To(Succeed())

		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")}, review: []string{cli.ReviewQuit}}
		r := lolaP(sp, "install", "--scope", "user", "-t", "claude-code", "--profile", "base")
		Expect(sp.reviewSteps).To(HaveLen(1), r.err)
		Expect(sp.reviewSteps[0].Summary).To(HavePrefix("Summary:\n  user (" + home + "), flag:\n    → for claude-code (from -t)\n" +
			"      deploy: saves default (base)'s entry; installs nothing while remote:team's entry wins\n" +
			"  ⚠ deploy: remote:team's entry wins; this change to default (base) applies when it is off\n"))

		// Without --profile, the run needs the base question, which only a terminal answers.
		r = lola("install", "deploy", "--scope", "user", "-t", "claude-code", "-y")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: remote:team is read-only; lola install needs to know whether to write to the base (default), " +
			"and there is no terminal to ask on\n  hint: lola install deploy --scope user --target claude-code --profile base\n"))
		r = lola("install", "deploy", "--scope", "user", "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(1))
		bundle := "lola profile --user team-work --switch --new --bundle remote:team,team-extras -n"
		Expect(r.err).To(HaveSuffix("  hint: lola install deploy --dry-run --scope user --target claude-code --profile base\n"+
			"  hint: or bundle it with a profile of yours: "+bundle+"\n"), "both hints keep -n")
		r = runHint(bundle)
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(p.UserProfiles, "team-work.yml")).NotTo(BeAnExistingFile(), "run as printed, it writes nothing")
		sp = &scripted{where: []int{0}}
		r = lolaP(sp, "install", "deploy", "--scope", "user", "-t", "claude-code", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.whereSteps).To(Equal([]cli.WhereStep{{Title: "remote:team is read-only.", Options: []string{"Write to the base (default)"}}}),
			"asked once, though the review's dry run and the apply both plan")
		def, err := os.ReadFile(filepath.Join(p.UserProfiles, "default.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(def)).To(ContainSubstring("name: deploy"))
	})

	It("titles the component page with the file the install writes", func() {
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\n"})
		Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewApply}}
		r := lolaP(sp, "install", "--scope", "user")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.calls).To(ContainElement("components:profile go › review-kit"))
	})

	DescribeTable("reviews an install that would re-pin a module, never saying it is up to date, and keeps an inherited pin",
		func(profileOn, repins bool) {
			// A git module whose branch moved to another commit of the same content: the cache's head
			// is the same version at a new commit.
			work, srv := filepath.Join(tmp, "repin-work"), filepath.Join(tmp, "repin-srv")
			writeTree(work, map[string]string{"lola.yml": "name: repin-kit\n", "commands/go.md": "Go.\n"})
			git := func(dir string, args ...string) string {
				GinkgoHelper()
				out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
				Expect(err).NotTo(HaveOccurred(), string(out))
				return strings.TrimSpace(string(out))
			}
			git(work, "init", "-q", "-b", "main")
			git(work, "add", "-A")
			git(work, "commit", "-q", "-m", "one")
			first := git(work, "rev-parse", "HEAD")
			git(tmp, "clone", "-q", "--bare", work, filepath.Join(srv, "kit.git"))
			url := testenv.ServeGitTLS(srv, nil) + "/kit.git"
			p, err := paths.Resolve(os.Getenv)
			Expect(err).NotTo(HaveOccurred())
			writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: repin-kit, source: " + url + "}\n",
				"go.yml": "version: 1\n"})
			Expect(lola("sync", "--user", "-y").code).To(Equal(0))
			if profileOn {
				Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
			}
			git(work, "commit", "-q", "--allow-empty", "-m", "two")
			moved := git(work, "rev-parse", "HEAD")
			git(work, "push", "-q", filepath.Join(srv, "kit.git"), "main")
			Expect(lola("cache", "update", "repin-kit").code).To(Equal(0))

			sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("repin-kit")}, review: []string{cli.ReviewApply}}
			r := lolaP(sp, "install", "--scope", "user", "-t", "claude-code")
			Expect(r.code).To(Equal(0), r.err)
			led, err := ledger.Load(p.UserLedger)
			Expect(err).NotTo(HaveOccurred())
			if repins {
				Expect(r.out + r.err).NotTo(ContainSubstring("already up to date"))
				Expect(sp.reviewSteps).To(HaveLen(1))
				Expect(r.err).To(ContainSubstring("    → repin-kit: re-pinned at " + moved[:7] + "\n"))
				Expect(led.Modules["repin-kit"].Commit).To(Equal(moved))
				return
			}
			Expect(r.out).To(Equal("✓ already up to date\n"))
			Expect(sp.reviewSteps).To(BeEmpty())
			Expect(led.Modules["repin-kit"].Commit).To(Equal(first))
		},
		Entry("the entry is the base's own, which the install writes: re-pinned", false, true),
		Entry("the profile on inherits the base's entry, which the install keeps", true, false),
	)

	It("judges a re-pin per scope, each at the commit of its own entry's ref", func() {
		// project.yml pins tag v1 and default.yml tracks main, a later commit of the same content:
		// one version, two commits, each installed where its ref points.
		work, srv := filepath.Join(tmp, "repin-work"), filepath.Join(tmp, "repin-srv")
		writeTree(work, map[string]string{"lola.yml": "name: repin-kit\n", "commands/go.md": "Go.\n"})
		git := func(dir string, args ...string) string {
			GinkgoHelper()
			out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), string(out))
			return strings.TrimSpace(string(out))
		}
		git(work, "init", "-q", "-b", "main")
		git(work, "add", "-A")
		git(work, "commit", "-q", "-m", "one")
		git(work, "tag", "v1")
		git(work, "commit", "-q", "--allow-empty", "-m", "two")
		git(tmp, "clone", "-q", "--bare", work, filepath.Join(srv, "kit.git"))
		url := testenv.ServeGitTLS(srv, nil) + "/kit.git"
		Expect(lola("install", url, "--ref", "v1", "--scope", "project", "-t", "claude-code", "-y").code).To(Equal(0))
		Expect(lola("install", url, "--scope", "user", "-t", "claude-code", "-y").code).To(Equal(0))

		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("repin-kit")}}
		r := lolaP(sp, "install", "--scope", "both", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("✓ already up to date\n"))
		Expect(sp.reviewSteps).To(BeEmpty())
	})

	It("says an install is up to date by the entry of the file it writes, and installs one that file lacks through the engine", func() {
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		install := func(args ...string) result {
			GinkgoHelper()
			sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
				components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewApply}}
			r := lolaP(sp, append([]string{"install", "--scope", "user", "-t", "claude-code"}, args...)...)
			Expect(r.code).To(Equal(0), r.err)
			return r
		}
		// The profile on lists it and the base does not: nothing to do.
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\n",
			"go.yml": "version: 1\nmodules:\n  - {name: review-kit, source: " + review + "}\n"})
		Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
		Expect(install().out).To(Equal("✓ already up to date\n"))

		// The base, which the install writes, lacks it (Listed false): the engine writes its entry.
		r := install("--profile", "base")
		Expect(r.out + r.err).NotTo(ContainSubstring("already up to date"))
		b, err := os.ReadFile(filepath.Join(p.UserProfiles, "default.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("name: review-kit"))

		// The profile on inherits it from the base unchanged: the install writes nothing.
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\n"})
		Expect(lola("sync", "--user", "-y").code).To(Equal(0))
		Expect(install().out).To(Equal("✓ already up to date\n"))
		b, err = os.ReadFile(filepath.Join(p.UserProfiles, "go.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("version: 1\n"))
	})

	It("installs a module another layer provides through the engine, which asks where it goes and writes the entry", func() {
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: review-kit, source: " + review + "}\n",
			"go.yml": "version: 1\n"})
		Expect(lola("sync", "--user", "-y").code).To(Equal(0))
		Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("skill/review")}, where: []int{1}, review: []string{cli.ReviewApply}}
		r := lolaP(sp, "install", "--scope", "user", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(sp.whereSteps).To(HaveLen(1), "the review's dry run asks; the apply reuses the answer")
		Expect(equivalents(r.err)).To(ContainElement(ContainSubstring("--override")))
		b, err := os.ReadFile(filepath.Join(p.UserProfiles, "go.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("name: review-kit"))
	})

	DescribeTable("the review's equivalent commands carry the where-question's answer, and do what the run does",
		func(answer int, flag []string, file string) {
			p, err := paths.Resolve(os.Getenv)
			Expect(err).NotTo(HaveOccurred())
			writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n  - {name: review-kit, source: " + review + "}\n",
				"go.yml": "version: 1\n"})
			Expect(lola("sync", "--user", "-y").code).To(Equal(0))
			Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
			sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
				components: []func(cli.ComponentPage) ([]string, error){keep("skill/review")}, where: []int{answer}, review: []string{cli.ReviewQuit}}
			r := lolaP(sp, "install", "--scope", "user", "-t", "claude-code")
			Expect(sp.reviewSteps).To(HaveLen(1), r.err)
			Expect(equivalents(r.err)).To(Equal([]string{"lola install review-kit --scope user -t claude-code --skip review-kit:skill/review " + strings.Join(flag, " ")}))

			words := shellWords(equivalents(r.err)[0])
			r = lola(append(words[1:], "-y")...)
			Expect(r.code).To(Equal(0), r.err)
			b, err := os.ReadFile(filepath.Join(p.UserProfiles, file))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(ContainSubstring("skip: [skill/review]"))
		},
		Entry("change it in the base", 0, []string{"--profile", "base"}, "default.yml"),
		Entry("override it in the profile on", 1, []string{"--override"}, "go.yml"),
	)

	It("gives modules answered alike one equivalent line each, which does what the run does for them", func() {
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  - {name: review-kit, source: " + review + "}\n  - {name: deploy, source: " + deploy + "}\n", "go.yml": "version: 1\n"})
		writeTree(deploy, map[string]string{"commands/rollback.md": "Roll back.\n"})
		Expect(lola("cache", "update", "deploy").code).To(Equal(0))
		Expect(lola("sync", "--user", "-y").code).To(Equal(0))
		Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy", "review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/rollback"), keep("skill/review")},
			where:      []int{0, 1}, review: []string{cli.ReviewQuit}}
		r := lolaP(sp, "install", "--scope", "user", "-t", "claude-code")
		Expect(sp.whereSteps).To(HaveLen(2), r.err)
		Expect(r.err).NotTo(ContainSubstring("note:"))
		lines := equivalents(r.err)
		Expect(lines).To(Equal([]string{
			"lola install deploy --scope user -t claude-code --skip deploy:command/rollback --profile base",
			"lola install review-kit --scope user -t claude-code --skip review-kit:skill/review --override"}))

		for _, l := range lines {
			r = lola(append(shellWords(l)[1:], "-y")...)
			Expect(r.code).To(Equal(0), r.err)
		}
		def, err := os.ReadFile(filepath.Join(p.UserProfiles, "default.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(def)).To(ContainSubstring("skip: [command/rollback]"))
		Expect(string(def)).NotTo(ContainSubstring("skip: [skill/review]"))
		goYml, err := os.ReadFile(filepath.Join(p.UserProfiles, "go.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(goYml)).To(ContainSubstring("name: review-kit"))
		Expect(string(goYml)).To(ContainSubstring("skip: [skill/review]"))
		Expect(string(goYml)).NotTo(ContainSubstring("name: deploy"))
	})

	It("keeps each scope's where-answers to its own equivalent line and review block", func() {
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		writeTree(p.UserProfiles, map[string]string{"default.yml": "version: 1\ntargets: [claude-code]\nmodules:\n" +
			"  - {name: review-kit, source: " + review + "}\n", "go.yml": "version: 1\n"})
		Expect(lola("sync", "--user", "-y").code).To(Equal(0))
		Expect(lola("install", "review-kit", "--scope", "project", "-t", "claude-code", "-y").code).To(Equal(0))
		Expect(lola("profile", "go", "--switch", "--user", "-y").code).To(Equal(0))
		Expect(lola("profile", "go", "--switch", "--project", "-y").code).To(Equal(0))
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("skill/review")}, where: []int{0, 1}, review: []string{cli.ReviewQuit}}
		r := lolaP(sp, "install", "--scope", "both", "-t", "claude-code")
		Expect(sp.whereSteps).To(HaveLen(2), r.err)
		Expect(r.err).To(ContainSubstring("  selections.yaml ("+selections+"):\n    review-kit in projects["+proj+"]: skip [skill/review]\n"+
			"  profile go:\n    review-kit: skip [skill/review]\nEquivalent commands:\n"), "each change under the file its scope's answer writes")
		lines := equivalents(r.err)
		Expect(lines).To(Equal([]string{
			"lola install review-kit --scope project -C " + proj + " -t claude-code --skip review-kit:skill/review --profile base",
			"lola install review-kit --scope user -t claude-code --skip review-kit:skill/review --override"}))

		for _, l := range lines {
			r = lola(append(shellWords(l)[1:], "-y")...)
			Expect(r.code).To(Equal(0), r.err)
		}
		sel, err := os.ReadFile(selections)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(sel)).To(ContainSubstring("skill/review"))
		goYml, err := os.ReadFile(filepath.Join(p.UserProfiles, "go.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(goYml)).To(ContainSubstring("name: review-kit"))
		Expect(string(goYml)).To(ContainSubstring("skip: [skill/review]"))
		pj, err := os.ReadFile(filepath.Join(proj, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(pj)).NotTo(ContainSubstring("skip"))
	})

	It("takes the assistants of the profile it writes, naming it in the review, and off a terminal where nothing is set up", func() {
		p, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		writeTree(p.UserProfiles, map[string]string{"go.yml": "version: 1\ntargets: [claude-code]\n"})
		on := func(root string) {
			GinkgoHelper()
			led := ledger.New()
			led.Profile = &ledger.Active{Name: "user:go", By: ledger.ByOn}
			Expect(led.Save(filepath.Join(root, ".lola", "lock.json"))).To(Succeed())
		}
		on(proj)
		sp := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")}, review: []string{cli.ReviewQuit}}
		r := lolaP(sp, "install", "--scope", "project")
		Expect(sp.reviewSteps).To(HaveLen(1), r.err)
		Expect(sp.reviewSteps[0].Summary).To(HavePrefix("Summary:\n  project ("+proj+"), flag:\n    → for claude-code (from profile go)\n"),
			"project.yml has no targets: go's apply")

		// A project where no assistant is set up: the install takes go's, as the engine does.
		bare := filepath.Join(tmp, "bare")
		Expect(os.MkdirAll(filepath.Join(bare, ".lola"), 0o755)).To(Succeed())
		on(bare)
		r = lola("install", "deploy", "-C", bare, "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(bare, ".claude", "commands", "deploy.md")).To(BeAnExistingFile())
		Expect(os.ReadFile(filepath.Join(p.UserProfiles, "go.yml"))).To(ContainSubstring("name: deploy"))
	})

	It("adds a source from the empty cache and selects it, and the summary repeats the cache add", func() {
		Expect(lola("cache", "rm", "review-kit", "-y").code).To(Equal(0))
		Expect(lola("cache", "rm", "deploy", "-y").code).To(Equal(0))
		p := &scripted{
			scope: []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){
				func(st cli.ModuleStep) (cli.ModuleAnswer, error) {
					Expect(st.Options).To(BeEmpty())
					return cli.ModuleAnswer{AddSource: true, Source: filepath.Join(tmp, "nowhere")}, nil
				},
				func(st cli.ModuleStep) (cli.ModuleAnswer, error) {
					Expect(st.Problem).To(HavePrefix("could not add "))
					return cli.ModuleAnswer{AddSource: true, Source: "../src/deploy"}, nil
				},
				func(st cli.ModuleStep) (cli.ModuleAnswer, error) {
					Expect(st.Preselect).To(Equal([]string{"deploy"}))
					return cli.ModuleAnswer{Names: st.Preselect}, nil
				},
			},
			review: []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.out+r.err)
		Expect(equivalents(r.err)).To(Equal([]string{"lola cache add ../src/deploy", "lola install deploy --scope project -t claude-code"}))
	})

	It("prints equivalent commands that resolve to the same plan through the real command tree", func() {
		writeTree(proj, map[string]string{"sub dir/.keep": ""})
		Expect(os.MkdirAll(filepath.Join(tmp, "other"), 0o755)).To(Succeed())
		GinkgoT().Chdir(filepath.Join(proj, "sub dir"))
		Expect(os.MkdirAll(filepath.Dir(selections), 0o700)).To(Succeed())
		Expect(os.WriteFile(selections, []byte("version: 1\nglobal:\n  review-kit:\n    skip: [rule/style]\n"), 0o600)).To(Succeed())
		before, err := os.ReadFile(selections)
		Expect(err).NotTo(HaveOccurred())
		// Drops a component and takes back the one the global section skips.
		page := func(pg cli.ComponentPage) ([]string, error) {
			out, _ := keep("command/ship")(pg)
			return append(out, "rule/style"), nil
		}
		for _, tc := range []struct {
			answer, flags []string
		}{{answer: []string{"project"}}, {answer: []string{"user"}}, {answer: []string{"project", "user"}},
			{answer: []string{"project", "other", "user"}},
			{answer: []string{"project"}, flags: []string{"--skip", "review-kit:skill/slow-lint"}},
			{answer: []string{"project", "user"}, flags: []string{"--only", "review-kit:skill/review", "--global"}},
			{answer: []string{"project"}, flags: []string{"--only", "review-kit:skill/review", "--exact"}}} {
			answer := tc.answer
			p := &scripted{
				scope:   []func(cli.ScopeStep) ([]string, error){scopes(answer...)},
				modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit", "deploy")},
				review:  []string{cli.ReviewApply},
				dirs:    []func(cli.DirectoryStep) (string, error){dir(filepath.Join(tmp, "other"))},
			}
			if tc.flags == nil {
				p.components = []func(cli.ComponentPage) ([]string, error){page}
			}
			r := lolaP(p, append([]string{"install", "-n"}, tc.flags...)...)
			Expect(r.code).To(Equal(0), r.err)
			want := r.out
			var got strings.Builder
			for _, line := range equivalents(r.err) {
				words := shellWords(line)
				Expect(words[:2]).To(Equal([]string{"lola", "install"}))
				for _, f := range tc.flags {
					n := 0
					for _, w := range words {
						if w == f {
							n++
						}
					}
					Expect(n).To(Equal(1), line+": each flag once")
				}
				eq := lola(append(words[1:], "-n")...)
				Expect(eq.code).To(Equal(0), line+"\n"+eq.err)
				got.WriteString(eq.out)
			}
			for _, l := range strings.Split(strings.TrimSpace(got.String()), "\n") {
				if strings.HasPrefix(l, "  ") {
					Expect(want).To(ContainSubstring(l+"\n"), fmt.Sprint(answer))
				}
			}
			Expect(strings.Count(want, "\n  ")).To(Equal(strings.Count(got.String(), "\n  ")), fmt.Sprint(answer)+"\n"+want+"\n---\n"+got.String())
			after, err := os.ReadFile(selections)
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before), "dry runs write nothing")
		}
	})

	It("keeps the version a cache pick was listed at when Change scope lists again", func() {
		p := &scripted{
			scope: []func(cli.ScopeStep) ([]string, error){scopes("project"), scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){func(cli.ModuleStep) (cli.ModuleAnswer, error) {
				writeTree(deploy, map[string]string{"commands/deploy.md": "Deploy v2.\n", "commands/extra.md": "Extra.\n"})
				Expect(lola("cache", "update", "deploy").code).To(Equal(0))
				return cli.ModuleAnswer{Names: []string{"deploy"}}, nil
			}},
			review: []string{cli.ReviewScope, cli.ReviewApply},
		}
		r := lolaP(p, "install")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"scope", "modules", "review", "scope", "review"}))
		b, err := os.ReadFile(filepath.Join(proj, ".claude/commands/deploy.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("Deploy.\n"), "the version the module picker listed")
		Expect(filepath.Join(proj, ".claude/commands/extra.md")).NotTo(BeAnExistingFile())
		if b, err := os.ReadFile(selections); err == nil {
			Expect(string(b)).NotTo(ContainSubstring("extra"), "no page ever showed it, so nothing skips it")
		}
	})

	It("says once on stderr why a cached module is left out of the picker, though Change scope lists again", func() {
		Expect(os.MkdirAll(filepath.Join(proj, ".lola"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(proj, ".lola", "project.yml"),
			[]byte("modules:\n  - name: deploy\n    source: "+deploy+"\n    ref: v9\n"), 0o644)).To(Succeed())
		man, err := manifest.Load(filepath.Join(proj, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		pp, err := paths.Resolve(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		Expect(trust.New(pp).Save(proj, trust.ViewOf(man))).To(Succeed(), "that lola recorded the entry it wrote")
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project"), scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep(), keep()},
			review:     []string{cli.ReviewScope, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-t", "claude-code")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.moduleSteps[0].Options).To(HaveLen(1), "deploy is left out")
		Expect(strings.Count(r.err, "⚠ deploy is not listed: your cache has no version of ref v9")).To(Equal(1), r.err)
	})

	It("offers Apply, Change components and Quit when only the component picker ran", func() {
		p := &scripted{components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", "review-kit", "--scope", "project", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.reviewSteps[0].Options).To(Equal([]string{"apply", "components", "quit"}))
	})

	It("never asks under --json, and its equivalent commands are a dry run's run that applies it", func() {
		p := &scripted{}
		r := lolaP(p, "install", "review-kit", "--scope", "project", "--json", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(BeEmpty())
		Expect(parseInstall(r.out).EquivalentCommands).To(Equal([]string{"lola install review-kit --scope project"}))
		r = lolaP(p, "install", "review-kit", "--scope", "project", "--json")
		Expect(r.code).To(Equal(0), r.err)
		v := parseInstall(r.out)
		Expect(v.EquivalentCommands).NotTo(BeNil())
		Expect(v.EquivalentCommands).To(BeEmpty())
	})

	It("counts a dropped module's picks once, not once per scope, and not again in the next summary", func() {
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project", "user")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit", "deploy"), mods("deploy"), mods("deploy", "review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship"), keep()},
			review:     []string{cli.ReviewModules, cli.ReviewModules, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("  3 component picks dropped: module removed\n"))
		Expect(strings.Count(r.err, "component picks dropped")).To(Equal(1), "the next summary starts from zero")
	})

	It("counts one dropped pick in the singular", func() {
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit", "deploy"), mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep()},
			review:     []string{cli.ReviewModules, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("  1 component pick dropped: module removed\n"))

		p = &scripted{
			scope:   []func(cli.ScopeStep) ([]string, error){scopes("project", "user"), scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("deploy")},
			review:  []string{cli.ReviewScope, cli.ReviewApply},
		}
		r = lolaP(p, "install", "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("  1 component pick dropped: scope removed\n"))
	})

	It("reports the picks lost when Change scope removes a scope", func() {
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project", "user"), scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship")},
			review:     []string{cli.ReviewScope, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-t", "claude-code", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("  3 component picks dropped: scope removed\n"))
	})

	It("drops a Change entry once its step is no longer shown", func() {
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit", "deploy"), mods("deploy")},
			components: []func(cli.ComponentPage) ([]string, error){keep()},
			review:     []string{cli.ReviewModules, cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.reviewSteps[0].Options).To(Equal([]string{"apply", "scope", "modules", "components", "quit"}))
		Expect(p.reviewSteps[1].Options).To(Equal([]string{"apply", "scope", "modules", "quit"}), "deploy has no page, so there is nothing to change")
	})

	It("prints an equivalent command with an unsafe -C path made safe, and says it is not copyable", func() {
		evil := filepath.Join(tmp, "evil\x1b[2J")
		Expect(os.MkdirAll(filepath.Join(evil, ".claude"), 0o755)).To(Succeed())
		p := &scripted{
			scope:      []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules:    []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("review-kit")},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship")},
			review:     []string{cli.ReviewApply},
		}
		r := lolaP(p, "-C", evil, "install", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.err).To(ContainSubstring("-C '" + filepath.Join(tmp, "evil?[2J") + "'"))
		Expect(r.err).To(ContainSubstring("not copyable as shown"))
		Expect(r.err).NotTo(ContainSubstring("\x1b"))
	})

	It("does not preselect a module twice when its source is added again", func() {
		p := &scripted{
			scope: []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){
				func(cli.ModuleStep) (cli.ModuleAnswer, error) {
					return cli.ModuleAnswer{AddSource: true, Source: deploy}, nil
				},
				func(st cli.ModuleStep) (cli.ModuleAnswer, error) {
					Expect(st.Preselect).To(Equal([]string{"deploy"}))
					return cli.ModuleAnswer{AddSource: true, Source: deploy}, nil
				},
				func(st cli.ModuleStep) (cli.ModuleAnswer, error) {
					Expect(st.Preselect).To(Equal([]string{"deploy"}))
					return cli.ModuleAnswer{Names: st.Preselect}, nil
				},
			},
			review: []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.out+r.err)
	})

	It("installs the version a source added again fetched, not the one picked before", func() {
		p := &scripted{
			scope: []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){
				mods("deploy"),
				func(cli.ModuleStep) (cli.ModuleAnswer, error) {
					writeTree(deploy, map[string]string{"commands/deploy.md": "Deploy v2.\n"})
					return cli.ModuleAnswer{Names: []string{"deploy"}, AddSource: true, Source: deploy}, nil
				},
				func(st cli.ModuleStep) (cli.ModuleAnswer, error) { return cli.ModuleAnswer{Names: st.Preselect}, nil },
			},
			review: []string{cli.ReviewModules, cli.ReviewApply},
		}
		r := lolaP(p, "install")
		Expect(r.code).To(Equal(0), r.err)
		b, err := os.ReadFile(filepath.Join(proj, ".claude/commands/deploy.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("Deploy v2.\n"))
	})

	It("returns to the module picker with the picks when the source to add is left empty", func() {
		p := &scripted{
			scope: []func(cli.ScopeStep) ([]string, error){scopes("project")},
			modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){
				func(cli.ModuleStep) (cli.ModuleAnswer, error) {
					return cli.ModuleAnswer{AddSource: true, Source: deploy}, nil
				},
				func(cli.ModuleStep) (cli.ModuleAnswer, error) {
					return cli.ModuleAnswer{Names: []string{"review-kit"}, AddSource: true}, nil
				},
				func(st cli.ModuleStep) (cli.ModuleAnswer, error) {
					Expect(st.Preselect).To(Equal([]string{"review-kit"}), "the picks, not the module the first step added")
					Expect(st.Problem).To(BeEmpty(), "an empty source is not a failed add")
					Expect(st.Options).To(HaveLen(2))
					return cli.ModuleAnswer{Names: st.Preselect}, nil
				},
			},
			components: []func(cli.ComponentPage) ([]string, error){keep("command/ship")},
			review:     []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", "-n")
		Expect(r.code).To(Equal(0), r.out+r.err)
		Expect(r.err).NotTo(ContainSubstring("could not add"))
	})
})

var _ = Describe("the version question", func() {
	var tmp, proj, other, config string
	labels := []string{">=2.0 — agent colors must be hex (#rrggbb)", ">=1.0 <2.0 — agent colors may also be theme names such as accent"}
	BeforeEach(func() {
		var err error
		tmp, err = filepath.EvalSymlinks(testenv.Isolate())
		Expect(err).NotTo(HaveOccurred())
		proj, other = filepath.Join(tmp, "proj"), filepath.Join(tmp, "other")
		config = filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "lola", "config.yaml")
		mod := filepath.Join(tmp, "m")
		writeTree(mod, map[string]string{"lola.yml": "name: m\n", "commands/c.md": "Go.\n"})
		for _, d := range []string{proj, other} {
			Expect(os.MkdirAll(filepath.Join(d, ".opencode"), 0o755)).To(Succeed())
		}
		fakeAssistant(tmp, "opencode", "opencode v2.2.5")
		Expect(lola("cache", "add", mod).code).To(Equal(0))
		GinkgoT().Chdir(proj)
	})
	declared := func(dir string) map[string]string {
		GinkgoHelper()
		m, err := manifest.Load(filepath.Join(dir, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		return m.TargetVersions
	}
	versionCalls := func(p *scripted) []string {
		return slices.DeleteFunc(slices.Clone(p.calls), func(c string) bool { return !strings.HasPrefix(c, "version:") })
	}

	It("asks in the project lola runs in, preselecting the installed era, and records the installed version", func() {
		p := &scripted{versions: []func(cli.VersionStep) (int, error){era(0)}, review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", "m", "--project", "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.versionSteps).To(HaveLen(1))
		st := p.versionSteps[0]
		Expect(st.Target).To(Equal("opencode"))
		Expect(st.Options).To(Equal(labels))
		Expect(st.Preselect).To(Equal(0))
		Expect(st.Note).To(Equal("installed here: 2.2.5"))
		Expect(st.Scope).To(BeEmpty(), "one scope: the context line names it")
		Expect(declared(proj)).To(Equal(map[string]string{"opencode": "2.2.5"}))
		Expect(r.err).To(ContainSubstring("    → opencode >=2.0 (installed 2.2.5; will be recorded in .lola/project.yml)\n"))
		Expect(p.reviewSteps[0].Options).To(Equal([]string{cli.ReviewApply, cli.ReviewVersions, cli.ReviewQuit}))
		Expect(filepath.Join(proj, ".opencode", "commands", "c.md")).To(BeARegularFile())
	})

	It("records the era picked when it is not the installed one, warning once that this machine's reads another", func() {
		p := &scripted{versions: []func(cli.VersionStep) (int, error){era(1)}, review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", "m", "--project", "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(declared(proj)).To(Equal(map[string]string{"opencode": "1.0"}))
		warning := "⚠ opencode: this machine's opencode 2.2.5 reads files written for >=2.0; these are written for >=1.0 <2.0 " +
			"(agent colors may also be theme names such as accent)\n"
		Expect(p.reviewSteps[0].Summary).To(ContainSubstring(warning + `      hint: to write for what this machine's opencode 2.2.5 reads (>=2.0): choose "Change versions"` + "\n"))
		hint := "    hint: to write for what this machine's opencode 2.2.5 reads (>=2.0): lola install m --project -t opencode@2.2.5\n"
		Expect(r.out).To(ContainSubstring(warning + hint))
		Expect(strings.Count(r.out, "⚠ opencode: this machine's")).To(Equal(1))
		Expect(r.out).To(ContainSubstring("→ recorded opencode 1.0 in .lola/project.yml (target_versions): files are written for opencode >=1.0 <2.0\n"))
		r = lola(strings.Fields(strings.TrimPrefix(strings.TrimSpace(hint), "hint: to write for what this machine's opencode 2.2.5 reads (>=2.0): "))[1:]...)
		Expect(r.code).To(Equal(0), r.err)
		Expect(declared(proj)).To(Equal(map[string]string{"opencode": "2.2.5"}), "the hint records the installed version")
	})

	It("asks for another directory with nothing preselected, this machine's version only a note", func() {
		p := &scripted{versions: []func(cli.VersionStep) (int, error){era(0)}, review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", "m", "-C", other, "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.versionSteps).To(HaveLen(1))
		Expect(p.versionSteps[0].Preselect).To(Equal(-1))
		Expect(p.versionSteps[0].Note).To(Equal("installed on this machine: 2.2.5 — not assumed for " + other + "; pick the version used there"))
		Expect(declared(other)).To(Equal(map[string]string{"opencode": "2.0"}), "the era's since: this machine's version says nothing of that project's")
	})

	It("asks once per scope, naming each, when the install has more than one", func() {
		Expect(os.MkdirAll(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "opencode"), 0o755)).To(Succeed())
		p := &scripted{versions: []func(cli.VersionStep) (int, error){era(0), era(1)}, review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", "m", "--scope", "both", "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.versionSteps).To(HaveLen(2))
		Expect(p.versionSteps[0].Scope).To(Equal("project (" + proj + ")"))
		Expect(p.versionSteps[1].Scope).To(Equal("user"))
		Expect(p.versionSteps[0].Note).To(Equal("installed here: 2.2.5"))
		Expect(p.versionSteps[1].Note).To(Equal("installed: 2.2.5"))
		Expect(declared(proj)).To(Equal(map[string]string{"opencode": "2.2.5"}))
		b, err := os.ReadFile(config)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("opencode: \"1.0\""))
		Expect(r.err).To(ContainSubstring("    → opencode >=1.0 <2.0 (installed 2.2.5; will be recorded in " + config + ")\n"))
	})

	It("never asks about a version the scope declares", func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntarget_versions:\n  opencode: \"1.0\"\n"})
		p := &scripted{}
		r := lolaP(p, "install", "m", "--project", "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(versionCalls(p)).To(BeEmpty())
		Expect(declared(proj)).To(Equal(map[string]string{"opencode": "1.0"}))
	})

	It("refuses a -C directory that does not exist before creating it when the version rule would refuse the install", func() {
		fresh := filepath.Join(tmp, "fresh", "app")
		for _, yes := range [][]string{{"-y"}, nil} {
			r := lola(slices.Concat([]string{"install", "m", "-C", fresh, "-t", "opencode"}, yes)...)
			Expect(r.code).To(Equal(1), r.err)
			Expect(r.err).To(Equal("error: -C "+fresh+" does not exist; which opencode version will read it? lola assumes the installed opencode only for the project you run it in\n"+
				"  hint: create it, for opencode >=2.0: lola install m -C "+fresh+" -t opencode@2.0 -y\n"+
				"  hint: create it, for opencode >=1.0 <2.0: lola install m -C "+fresh+" -t opencode@1.0 -y\n"), "%v", yes)
			Expect(filepath.Join(tmp, "fresh")).NotTo(BeAnExistingFile(), "%v", yes)
		}
		r := lola("install", "m", "-C", fresh, "-t", "opencode@1.0", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(declared(fresh)).To(Equal(map[string]string{"opencode": "1.0"}))
	})

	It("refuses a -C directory that does not exist, with no -t, before creating it, each hint the run that creates it for an assistant", func() {
		fresh := filepath.Join(tmp, "fresh", "app")
		want := "error: -C " + fresh + " does not exist, so no assistant is set up there; name the ones it is for with -t\n" +
			"  hint: create it, for opencode >=2.0: lola install m -C " + fresh + " -t opencode@2.0 -y\n" +
			"  hint: create it, for opencode >=1.0 <2.0: lola install m -C " + fresh + " -t opencode@1.0 -y\n" +
			"  hint: create it, for agents: lola install m -C " + fresh + " -t agents -y\n"
		for _, yes := range [][]string{nil, {"-y"}} {
			r := lola(slices.Concat([]string{"install", "m", "-C", fresh}, yes)...)
			Expect(r.code).To(Equal(1), r.err)
			Expect(r.err).To(Equal(want), "%v", yes)
			Expect(filepath.Join(tmp, "fresh")).NotTo(BeAnExistingFile(), "%v", yes)
		}
		for _, hint := range strings.Split(strings.TrimSpace(want), "\n")[1:] {
			_, run, _ := strings.Cut(hint, ": lola ")
			r := lola(strings.Fields(run)...)
			Expect(r.code).To(Equal(0), "%s: %s", run, r.err)
			Expect(os.RemoveAll(filepath.Join(tmp, "fresh"))).To(Succeed())
		}
	})

	It("names -y in the equivalent command of a run that created its directory", func() {
		fresh := filepath.Join(tmp, "fresh")
		p := &scripted{confirm: []bool{true}, versions: []func(cli.VersionStep) (int, error){era(0)}, review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", "m", "-C", fresh, "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(equivalents(r.err)).To(Equal([]string{"lola install m --scope project -C " + fresh + " -t opencode@2.0 -y"}))
	})

	DescribeTable("-y and --force ask nothing and take the installed version here",
		func(flag string) {
			p := &scripted{}
			r := lolaP(p, "install", "m", "--project", "-t", "opencode", flag)
			Expect(r.code).To(Equal(0), r.err)
			Expect(versionCalls(p)).To(BeEmpty())
			Expect(declared(proj)).To(Equal(map[string]string{"opencode": "2.2.5"}))
		},
		Entry("-y", "-y"), Entry("--force", "--force"))

	It("off a terminal takes the installed version here, and refuses another directory, coaching the flag and the declaration", func() {
		Expect(lola("install", "m", "--project", "-t", "opencode").code).To(Equal(0))
		Expect(declared(proj)).To(Equal(map[string]string{"opencode": "2.2.5"}))

		r := lola("install", "m", "-C", other, "-t", "opencode")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: which opencode version will read " + other + "? lola assumes the installed opencode only for the project you run it in, and " +
			other + " declares none\n" +
			"  hint: for opencode >=2.0: lola install m -C " + other + " -t opencode@2.0\n" +
			"  hint: for opencode >=1.0 <2.0: lola install m -C " + other + " -t opencode@1.0\n"))
		Expect(r.err).NotTo(ContainSubstring("declare it"), "no project.yml there to declare it in")
		Expect(filepath.Join(other, ".opencode", "commands", "c.md")).NotTo(BeAnExistingFile())

		r = lola("install", "m", "-C", other, "-t", "opencode", "-t", "opencode@2.0")
		Expect(r.code).To(Equal(0), r.err)
		Expect(declared(other)).To(Equal(map[string]string{"opencode": "2.0"}))
	})

	It("refuses another directory, its hints keeping every assistant the manifest fixes and the flags as typed", func() {
		Expect(os.MkdirAll(filepath.Join(other, ".claude"), 0o755)).To(Succeed())
		writeTree(other, map[string]string{".lola/project.yml": "version: 1\ntargets: [opencode, claude-code]\n"})
		r := lola("install", "m", "-C", other, "-y")
		Expect(r.code).To(Equal(1))
		hint := "lola install m -C " + other + " -t opencode@2.0,claude-code -y"
		Expect(r.err).To(HaveSuffix("  hint: for opencode >=2.0: "+hint+"\n"+
			"  hint: for opencode >=1.0 <2.0: lola install m -C "+other+" -t opencode@1.0,claude-code -y\n"), "declaring it by hand would need trusting the file")

		r = lola(strings.Fields(hint)[1:]...)
		Expect(r.code).To(Equal(0), r.err)
		Expect(filepath.Join(other, ".claude", "commands", "c.md")).To(BeARegularFile(), "claude-code kept")
		Expect(filepath.Join(other, ".opencode", "commands", "c.md")).To(BeARegularFile())
	})

	DescribeTable("-t <id>@<version> asks nothing and records the version, in every form",
		func(ts ...string) {
			Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
			p := &scripted{}
			r := lolaP(p, slices.Concat([]string{"install", "m", "--project"}, ts)...)
			Expect(r.code).To(Equal(0), r.err)
			Expect(versionCalls(p)).To(BeEmpty())
			Expect(declared(proj)).To(Equal(map[string]string{"opencode": "1.0"}))
			Expect(filepath.Join(proj, ".opencode", "commands", "c.md")).To(BeARegularFile())
		},
		Entry("alone", "-t", "opencode@1.0"),
		Entry("comma-separated with a plain id", "-t", "claude-code,opencode@1.0"),
		Entry("repeated with a plain id", "-t", "claude-code", "-t", "opencode@1.0"),
		Entry("beside the same id plain", "-t", "opencode", "-t", "opencode@1.0"))

	DescribeTable("refuses a -t value with an empty id or version, two versions for one id, or a version below every era, before asking anything",
		func(value, want string) {
			p := &scripted{}
			r := lolaP(p, "install", "m", "--project", "-t", value)
			Expect(r.code).To(Equal(1))
			Expect(r.err).To(ContainSubstring(want))
			Expect(p.calls).To(BeEmpty())
			Expect(filepath.Join(proj, ".lola", "project.yml")).NotTo(BeAnExistingFile())
		},
		Entry("no version", "opencode@", "-t opencode@: write <id>@<version>, e.g. opencode@2.0\n"+
			"  hint: for opencode >=2.0: lola install m --project -t opencode@2.0\n"+
			"  hint: for opencode >=1.0 <2.0: lola install m --project -t opencode@1.0\n"+
			"  hint: or with no version: lola install m --project -t opencode\n"),
		Entry("no version of a target with one era", "claude-code@", "-t claude-code@: write <id>@<version>, e.g. opencode@2.0\n"+
			"  hint: lola install m --project -t claude-code\n"),
		Entry("no id", "@1.0", "-t @1.0: write <id>@<version>, e.g. opencode@2.0\n  hint: for opencode: lola install m --project -t opencode@1.0\n"),
		Entry("no id, beside another version for the target", "@1.0,opencode@2.0", "-t @1.0: write <id>@<version>, e.g. opencode@2.0\n"+
			"  hint: for opencode >=2.0: lola install m --project -t opencode@2.0\n"+
			"  hint: for opencode >=1.0 <2.0: lola install m --project -t opencode@1.0\n"),
		Entry("no id, after another version for the target", "opencode@2.0,@1.0", "-t @1.0: write <id>@<version>, e.g. opencode@2.0\n"+
			"  hint: for opencode >=2.0: lola install m --project -t opencode@2.0\n"+
			"  hint: for opencode >=1.0 <2.0: lola install m --project -t opencode@1.0\n"),
		Entry("no id and no version", "claude-code,@", "-t @: write <id>@<version>, e.g. opencode@2.0\n"+
			"  hint: for claude-code: lola install m --project -t claude-code\n"+
			"  hint: for gemini-cli: lola install m --project -t claude-code,gemini-cli\n"+
			"  hint: for opencode: lola install m --project -t claude-code,opencode\n"+
			"  hint: for agents: lola install m --project -t claude-code,agents\n"),
		Entry("two versions", "opencode@1.0,opencode@2.0", "-t opencode@2.0: -t gives opencode version 1.0 too; give one version\n"+
			"  hint: for opencode >=2.0: lola install m --project -t opencode@2.0\n"+
			"  hint: for opencode >=1.0 <2.0: lola install m --project -t opencode@1.0\n"),
		Entry("not a version", "opencode@banana", `-t opencode@banana: "banana" is not a version`+"\n"+
			"  hint: for opencode >=2.0: lola install m --project -t opencode@2.0\n"+
			"  hint: for opencode >=1.0 <2.0: lola install m --project -t opencode@1.0\n"),
		Entry("a version older than any lola knows", "claude-code,opencode@0.5", "error: -t opencode@0.5: opencode 0.5 is older than any opencode lola knows\n"+
			"  hint: for opencode >=2.0: lola install m --project -t claude-code,opencode@2.0\n"+
			"  hint: for opencode >=1.0 <2.0: lola install m --project -t claude-code,opencode@1.0\n"))

	It("hints, for an installed version of another era than the one declared, the edit and the sync that shows it before applying it", func() {
		Expect(lola("install", "m", "--project", "-t", "opencode@1.0").code).To(Equal(0))
		Expect(os.Remove(filepath.Join(proj, ".opencode", "commands", "c.md"))).To(Succeed())
		r := lola("sync", "--project")
		Expect(r.code).To(Equal(0), r.err)
		lead := `set target_versions.opencode to "2.2.5" in .lola/project.yml, then: `
		Expect(r.out).To(ContainSubstring(lead + "lola sync --project; or install"))
		Expect(r.out).NotTo(ContainSubstring("--trust"), "the consent is given where the change is shown")
		file := filepath.Join(proj, ".lola", "project.yml")
		b, err := os.ReadFile(file)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(file, []byte(strings.Replace(string(b), `opencode: "1.0"`, `opencode: "2.2.5"`, 1)), 0o600)).To(Succeed())
		r = lola("sync", "--project")
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.out + r.err).To(ContainSubstring(`  ~ target_versions opencode "1.0" → "2.2.5"` + "\n"))
		Expect(r.err).To(ContainSubstring("lola sync --project --trust=entries"))
		r = lola("sync", "--project", "--trust=entries")
		Expect(r.code).To(Equal(0), r.err)
		Expect(declared(proj)).To(Equal(map[string]string{"opencode": "2.2.5"}))
	})

	It("wraps a hint at spaces to the terminal, each command on one line, and leaves it one line off a terminal", func() {
		Expect(lola("install", "m", "--project", "-t", "opencode@1.0").code).To(Equal(0))
		Expect(os.Remove(filepath.Join(proj, ".opencode", "commands", "c.md"))).To(Succeed())
		GinkgoT().Setenv("COLUMNS", "60")
		root, err := cli.NewRootCmd(os.Getenv, cli.WithTerminals(true, true))
		Expect(err).NotTo(HaveOccurred())
		var out, errb bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errb)
		root.SetIn(strings.NewReader(""))
		root.SetArgs([]string{"sync", "--project"})
		Expect(cli.Execute(root)).To(Equal(0), errb.String())
		Expect(out.String()).To(ContainSubstring("" +
			"    hint: set target_versions.opencode to \"2.2.5\" in\n" +
			"          .lola/project.yml, then: lola sync --project; or\n" +
			"          install a release of opencode >=1.0 <2.0\n"))
		Expect(os.Remove(filepath.Join(proj, ".opencode", "commands", "c.md"))).To(Succeed())
		Expect(lola("sync", "--project").out).To(ContainSubstring(`    hint: set target_versions.opencode to "2.2.5" in .lola/project.yml, then: lola sync --project; or install`))
	})

	It("lists under a target_versions change, before asking or refusing, the values it drops, past two counted with the dry run", func() {
		agents := map[string]string{"lola.yml": "name: a\n"}
		for _, n := range []string{"rev", "lint", "doc"} {
			agents["agents/"+n+".md"] = "---\ndescription: D.\ncolor: accent\n---\nX.\n"
		}
		a, b := filepath.Join(tmp, "a"), filepath.Join(tmp, "b")
		writeTree(a, agents)
		writeTree(b, map[string]string{"lola.yml": "name: b\n", "agents/ask.md": "---\ndescription: D.\ncolor: accent\n---\nX.\n"})
		Expect(lola("cache", "add", a).code).To(Equal(0))
		Expect(lola("cache", "add", b).code).To(Equal(0))
		Expect(lola("install", "b", "--project", "-t", "opencode@1.0").code).To(Equal(0))
		file := filepath.Join(proj, ".lola", "project.yml")
		edit := func(from, to string) {
			GinkgoHelper()
			b, err := os.ReadFile(file)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(file, []byte(strings.Replace(string(b), `opencode: "`+from+`"`, `opencode: "`+to+`"`, 1)), 0o600)).To(Succeed())
		}
		edit("1.0", "2.0")
		row := `  ~ target_versions opencode "1.0" → "2.0"` + "\n"
		drop := `⚠ b: opencode agent ask: color "accent" dropped — opencode >=2.0: agent colors must be hex (#rrggbb)` + "\n"
		r := lola("sync", "--project")
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(ContainSubstring(row + drop))
		p := &scripted{trust: []func(cli.TrustStep) (engine.TrustAnswer, error){func(cli.TrustStep) (engine.TrustAnswer, error) { return engine.TrustAnswer{}, cli.ErrAborted }}}
		r = lolaP(p, "sync", "--project")
		Expect(p.calls).To(Equal([]string{"trust"}))
		Expect(r.err).To(ContainSubstring(row + drop))

		edit("2.0", "1.0")
		Expect(lola("sync", "--project", "--trust=entries").code).To(Equal(0))
		r = lola("install", "a", "--project")
		Expect(r.code).To(Equal(0), r.err)
		edit("1.0", "2.0")
		r = lola("sync", "--project")
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.err).To(MatchRegexp(regexp.QuoteMeta(row) + `⚠ a: opencode agent \w+: color "accent" dropped .*\n(    hint: .*\n)*⚠ a: opencode agent \w+: color "accent" dropped .*\n(    hint: .*\n)*` +
			regexp.QuoteMeta("→ and 2 more — lola sync --project -n\n")))
	})

	It("ends each notice row of a sync of more than one scope with its scope", func() {
		Expect(lola("install", "m", "--scope", "both", "-t", "opencode@1.0").code).To(Equal(0))
		for _, file := range []string{filepath.Join(proj, ".lola", "project.yml"), config} {
			b, err := os.ReadFile(file)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(file, []byte(strings.Replace(string(b), `opencode: "1.0"`, `opencode: "2.0"`, 1)), 0o600)).To(Succeed())
		}
		r := lola("sync", "--scope", "both", "-n", "--plain", "--trust=entries")
		Expect(r.code).To(Equal(0), r.err)
		row := "notice\tinfo\tm: would rewrite opencode files from >=1.0 <2.0 to >=2.0 — no file changes\t"
		Expect(r.out).To(ContainSubstring(row + "project\n"))
		Expect(r.out).To(ContainSubstring(row + "user\n"))
		Expect(lola("sync", "--project", "-n", "--plain", "--trust=entries").out).To(ContainSubstring(strings.TrimSuffix(row, "\t")+"\n"), "one scope: no scope column")
	})

	It("ends each notice row of an install into more than one scope with its scope", func() {
		r := lola("install", "m", "--scope", "both", "-t", "opencode@2.0", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("notice\tinfo\trecorded opencode 2.0 in .lola/project.yml (target_versions): files are written for opencode >=2.0 (matches the installed 2.2.5)\tproject\n"))
		Expect(r.out).To(ContainSubstring("notice\tinfo\trecorded opencode 2.0 in " + config + " (target_versions): files are written for opencode >=2.0 (matches the installed 2.2.5)\tuser\n"))
		Expect(r.out).NotTo(MatchRegexp(`(?m)^notice\t[^\t]*\t[^\t]*$`), "every notice row names its scope")
		r = lola("install", "m", "-C", other, "-t", "opencode@2.0", "--plain")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("notice\tinfo\trecorded opencode 2.0 in .lola/project.yml (target_versions): files are written for opencode >=2.0\n"), "one scope: no scope column")
	})

	It("trusts the versions it records, and a sync shows a hand edit of them as target_versions, the targets unchanged", func() {
		Expect(lola("install", "m", "-C", other, "-t", "opencode@1.0,claude-code").code).To(Equal(0))
		r := lola("sync", "-C", other)
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out + r.err).NotTo(ContainSubstring("changed since"))
		b, err := os.ReadFile(filepath.Join(other, ".lola", "project.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(other, ".lola", "project.yml"), []byte(strings.Replace(string(b), `opencode: "1.0"`, `opencode: "2.0"`, 1)), 0o600)).To(Succeed())
		r = lola("sync", "-C", other)
		Expect(r.code).To(Equal(1), r.out)
		Expect(r.out + r.err).To(ContainSubstring(".lola/project.yml changed since lola last wrote it or you last trusted it:\n" +
			"  ~ target_versions opencode \"1.0\" → \"2.0\"\n"))
		Expect(r.out + r.err).NotTo(ContainSubstring("~ targets"))
		r = lola("sync", "-C", other, "--trust=entries")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out + r.err).To(ContainSubstring("  → entries: target_versions (changed)\n"))
		Expect(os.WriteFile(filepath.Join(other, ".lola", "project.yml"), []byte(strings.Replace(string(b), `opencode: "1.0"`, `opencode: "1.0"`+"\n  gemini-cli: \"0.6\"", 1)), 0o600)).To(Succeed())
		r = lola("sync", "-C", other, "-n", "--json")
		var doc struct {
			Trust struct {
				Targets        any `json:"targets"`
				TargetVersions struct {
					Field    string
					From, To map[string]string
				} `json:"target_versions"`
			}
		}
		Expect(json.Unmarshal([]byte(r.out), &doc)).To(Succeed(), r.out)
		Expect(doc.Trust.Targets).To(BeNil())
		Expect(doc.Trust.TargetVersions.Field).To(Equal("target_versions"))
		Expect(doc.Trust.TargetVersions.From).To(Equal(map[string]string{"opencode": "2.0"}))
		Expect(doc.Trust.TargetVersions.To).To(Equal(map[string]string{"opencode": "1.0", "gemini-cli": "0.6"}))
		Expect(lola("sync", "-C", other, "-n").out).To(ContainSubstring("  + target_versions gemini-cli \"0.6\"\n  ~ target_versions opencode \"2.0\" → \"1.0\"\n"))
		Expect(lola("sync", "-C", other, "-n", "--plain").out).To(ContainSubstring("trust\t\ttarget_versions\t\tgemini-cli\t\t0.6\n" +
			"trust\t\ttarget_versions\t\topencode\t2.0\t1.0\n"))
		var rewrites struct {
			Rewrites []map[string]string `json:"rewrites"`
		}
		Expect(json.Unmarshal([]byte(r.out), &rewrites)).To(Succeed(), r.out)
		Expect(rewrites.Rewrites).To(Equal([]map[string]string{{"module": "m", "target": "opencode", "from": "2.0", "to": "1.0"}}), "kept, though no file changes")
		Expect(lola("sync", "-C", other, "-n").out).To(ContainSubstring(
			"→ m: would rewrite opencode files from >=2.0 to >=1.0 <2.0 — no file changes\n"), "the command's files are the same for both")
		Expect(os.WriteFile(filepath.Join(other, ".lola", "project.yml"), []byte(strings.Replace(string(b), `opencode: "1.0"`, `gemini-cli: "0.6"`, 1)), 0o600)).To(Succeed())
		Expect(lola("sync", "-C", other, "-n").out).To(ContainSubstring("  + target_versions gemini-cli \"0.6\"\n  - target_versions opencode \"2.0\"\n"), "an id removed")
	})

	It("lists the versions it records in JSON, with where each came from", func() {
		type tv struct{ Scope, Target, Version, Previous, Source, File, Era string }
		versions := func(out string) []tv {
			GinkgoHelper()
			var doc struct {
				TargetVersions []tv `json:"target_versions"`
			}
			Expect(json.Unmarshal([]byte(out), &doc)).To(Succeed(), out)
			return doc.TargetVersions
		}
		file := filepath.Join(proj, ".lola", "project.yml")
		r := lola("install", "m", "--project", "-t", "opencode", "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(versions(r.out)).To(Equal([]tv{{"project", "opencode", "2.2.5", "", "installed", file, "2.0"}}))
		r = lola("install", "m", "--project", "-t", "opencode@1.0", "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(versions(r.out)).To(Equal([]tv{{"project", "opencode", "1.0", "2.2.5", "flag", file, "1.0"}}))
	})

	It("hints a -t value it cannot read for a -C directory that does not exist with runs that create it, each with a version", func() {
		fresh := filepath.Join(tmp, "fresh")
		r := lola("install", "m", "-C", fresh, "-t", "opencode@")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(Equal("error: -t opencode@: write <id>@<version>, e.g. opencode@2.0\n" +
			"  hint: for opencode >=2.0: lola install m -C " + fresh + " -t opencode@2.0 -y\n" +
			"  hint: for opencode >=1.0 <2.0: lola install m -C " + fresh + " -t opencode@1.0 -y\n"))
	})

	It("takes a version newer than any lola knows, saying so", func() {
		r := lola("install", "m", "--project", "-t", "opencode@99")
		Expect(r.code).To(Equal(0), r.err)
		Expect(declared(proj)).To(Equal(map[string]string{"opencode": "99"}))
		Expect(r.out).To(ContainSubstring("→ opencode 99 is newer than any opencode lola knows; files are written for opencode >=2.0\n"+
			"→ recorded opencode 99 in .lola/project.yml"), "said before the version recorded")
		Expect(r.out).NotTo(ContainSubstring("matches the installed"), "99 is no version of the installed one's era")
		Expect(lola("install", "m", "-C", other, "-t", "opencode@2.9").out).NotTo(ContainSubstring("newer than"))
	})

	It("off a terminal, with the assistant not installed here, says so with a command for each version", func() {
		Expect(os.Remove(filepath.Join(tmp, "bin", "opencode"))).To(Succeed())
		r := lola("install", "m", "--project", "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ opencode is not installed here, so lola wrote for opencode >=2.0 and recorded nothing\n" +
			"    hint: to choose, for opencode >=2.0: lola install m --project -t opencode@2.0\n" +
			"    hint: to choose, for opencode >=1.0 <2.0: lola install m --project -t opencode@1.0\n"))
		r = lola("install", "m", "--project", "-t", "opencode@1.0")
		Expect(r.code).To(Equal(0), r.err)
		Expect(declared(proj)).To(Equal(map[string]string{"opencode": "1.0"}))
	})

	It("leaves out a dropped value's keep-it hint when the not-installed notice already gives the run for each version", func() {
		Expect(os.Remove(filepath.Join(tmp, "bin", "opencode"))).To(Succeed())
		a := filepath.Join(tmp, "a")
		writeTree(a, map[string]string{"lola.yml": "name: a\n", "agents/rev.md": "---\ndescription: Reviews.\ncolor: accent\n---\nReview.\n"})
		Expect(lola("cache", "add", a).code).To(Equal(0))
		r := lola("install", "a", "--project", "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(`⚠ a: opencode agent rev: color "accent" dropped — opencode >=2.0: agent colors must be hex (#rrggbb)` + "\n" +
			"    hint: in rev.targets.yaml beside the agent"))
		Expect(r.out).To(ContainSubstring("    hint: to choose, for opencode >=1.0 <2.0: lola install a --project -t opencode@1.0\n"))
		Expect(r.out).NotTo(ContainSubstring("keep it by writing"))
	})

	It("takes a version for a target with one era, recording nothing, and says why", func() {
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		for _, v := range []string{"1.0", "0.1"} {
			r := lola("install", "m", "--project", "-t", "claude-code@"+v)
			Expect(r.code).To(Equal(0), r.err)
			Expect(r.out).To(ContainSubstring("→ claude-code's files are the same for every version lola knows; nothing recorded\n"), v)
			Expect(declared(proj)).To(BeEmpty(), v)
		}
	})

	It("takes one version given twice, written two ways, as one", func() {
		p := &scripted{}
		r := lolaP(p, "install", "m", "--project", "-t", "opencode@1.0", "-t", "opencode@1.0.0")
		Expect(r.code).To(Equal(0), r.err)
		Expect(declared(proj)).To(Equal(map[string]string{"opencode": "1.0"}))
	})

	It("goes back from the version question to the assistants, and Change versions asks only it again", func() {
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		fakeAssistant(tmp, "claude", "2.1.287 (Claude Code)")
		picked := func(st cli.AssistantStep) ([]string, error) { return []string{"opencode"}, nil }
		p := &scripted{
			assistants: []func(cli.AssistantStep) ([]string, error){picked, picked},
			versions: []func(cli.VersionStep) (int, error){goneBack[cli.VersionStep, int], era(1),
				func(st cli.VersionStep) (int, error) {
					Expect(st.Preselect).To(Equal(1), "the last answer")
					return 0, nil
				}},
			review: []string{cli.ReviewVersions, cli.ReviewApply},
		}
		r := lolaP(p, "install", "m", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"assistants", "version:opencode", "assistants", "version:opencode", "review", "version:opencode", "review"}))
		Expect(p.versionSteps[0].Nav.Back).To(BeTrue())
		Expect(p.versionSteps[1].Step).To(Equal(cli.Steps{At: 2, Of: 3}))
		Expect(p.versionSteps[1].Again).To(BeFalse())
		Expect(p.versionSteps[2].Again).To(BeTrue(), "asked again from the review")
		Expect(p.versionSteps[2].Step).To(Equal(cli.Steps{}), "one question, not a numbered flow")
		Expect(p.versionSteps[2].Last).To(BeTrue(), "the preselection is the last answer")
		Expect(p.reviewSteps[1].Step).To(Equal(p.reviewSteps[0].Step), "the review keeps its number, so its title, as it was")
		Expect(p.assistantSteps[1].Preselect).To(Equal([]string{"opencode"}))
		Expect(declared(proj)).To(Equal(map[string]string{"opencode": "2.2.5"}))
	})

	It("goes back from Change versions to the review, the answers kept", func() {
		p := &scripted{versions: []func(cli.VersionStep) (int, error){era(1), goneBack[cli.VersionStep, int]},
			review: []string{cli.ReviewVersions, cli.ReviewApply}}
		r := lolaP(p, "install", "m", "--project", "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"version:opencode", "review", "version:opencode", "review"}))
		Expect(p.versionSteps[1].Nav.Back).To(BeTrue())
		Expect(p.reviewSteps[1].Summary).To(ContainSubstring("    → opencode >=1.0 <2.0 (installed 2.2.5; will be recorded in .lola/project.yml)\n"))
		Expect(p.reviewSteps[1].Options).To(ContainElement(cli.ReviewVersions))
		Expect(declared(proj)).To(Equal(map[string]string{"opencode": "1.0"}))
	})

	It("goes back from a later scope's version question to the one before, its answer preselected", func() {
		Expect(os.MkdirAll(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "opencode"), 0o755)).To(Succeed())
		p := &scripted{
			versions: []func(cli.VersionStep) (int, error){era(1), goneBack[cli.VersionStep, int],
				func(st cli.VersionStep) (int, error) {
					Expect(st.Scope).To(Equal("project (" + proj + ")"))
					Expect(st.Preselect).To(Equal(1), "the last answer")
					return 1, nil
				}, era(0)},
			review: []string{cli.ReviewApply},
		}
		r := lolaP(p, "install", "m", "--scope", "both", "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.calls).To(Equal([]string{"version:opencode", "version:opencode", "version:opencode", "version:opencode", "review"}))
		Expect(p.versionSteps[0].Nav.Back).To(BeFalse(), "nothing before the first")
		Expect(p.versionSteps[1].Nav.Back).To(BeTrue())
		Expect(p.versionSteps[2].Step).To(Equal(p.versionSteps[0].Step))
		Expect(declared(proj)).To(Equal(map[string]string{"opencode": "1.0"}))
	})

	It("repeats an answer for assistants the manifest fixes with -t naming them all", func() {
		Expect(os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)).To(Succeed())
		Expect(os.Remove(filepath.Join(tmp, "bin", "opencode"))).To(Succeed())
		Expect(lola("install", "m", "--project", "-t", "claude-code,opencode").code).To(Equal(0))
		Expect(declared(proj)).To(BeEmpty(), "not installed here: nothing recorded")
		n := filepath.Join(tmp, "n")
		writeTree(n, map[string]string{"lola.yml": "name: n\n", "commands/d.md": "Do.\n"})
		Expect(lola("cache", "add", n).code).To(Equal(0))

		// m is installed again in the run, so no module left installed holds the version.
		p := &scripted{versions: []func(cli.VersionStep) (int, error){era(1)}, review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", "m", "n", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.versionSteps[0].Preselect).To(Equal(-1))
		Expect(p.versionSteps[0].Note).To(Equal("opencode is not installed here; pick the version you will use"), "nothing preselected: the note says why")
		Expect(equivalents(r.err)).To(Equal([]string{"lola install m n --scope project -t claude-code,opencode@1.0"}))
		Expect(declared(proj)).To(Equal(map[string]string{"opencode": "1.0"}))
	})

	It("says only what the install records: a -t version the scope declares is no recording", func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntarget_versions:\n  opencode: \"2.0\"\n"})
		for _, tc := range []struct{ value, line string }{{"opencode@2.0.0", "    → opencode >=2.0 (from .lola/project.yml)\n"},
			{"opencode@1.0", "    → opencode >=1.0 <2.0 (was 2.0 in .lola/project.yml; installed 2.2.5)\n"}} {
			p := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("m")}, review: []string{cli.ReviewQuit}}
			r := lolaP(p, "install", "--project", "-t", tc.value)
			Expect(p.reviewSteps).To(HaveLen(1), r.err)
			Expect(p.reviewSteps[0].Summary).To(ContainSubstring(tc.line), tc.value)
			Expect(p.reviewSteps[0].Options).To(ContainElement(cli.ReviewVersions), "a version given can be changed too")
		}
	})

	It("records an answer for a module already installed instead of finding nothing to change", func() {
		Expect(os.Remove(filepath.Join(tmp, "bin", "opencode"))).To(Succeed())
		Expect(lola("install", "m", "--project", "-t", "opencode").code).To(Equal(0))
		p := &scripted{versions: []func(cli.VersionStep) (int, error){era(1)}, review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", "m", "--project", "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out + r.err).NotTo(ContainSubstring("already up to date"))
		Expect(declared(proj)).To(Equal(map[string]string{"opencode": "1.0"}))
	})

	It("offers the era that keeps a dropped value: Change versions in the review, -t after it", func() {
		a := filepath.Join(tmp, "a")
		writeTree(a, map[string]string{"lola.yml": "name: a\n", "agents/rev.md": "---\ndescription: Reviews.\ncolor: accent\n---\nReview.\n"})
		Expect(lola("cache", "add", a).code).To(Equal(0))
		p := &scripted{versions: []func(cli.VersionStep) (int, error){era(0)}, review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", "a", "--project", "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		line := `⚠ a: opencode agent rev: color "accent" dropped — opencode >=2.0: agent colors must be hex (#rrggbb)` + "\n"
		own := "; or in rev.targets.yaml beside the agent, set opencode's color to "
		Expect(p.reviewSteps[0].Summary).To(ContainSubstring(line + `      hint: keep it by writing for opencode >=1.0 <2.0: choose "Change versions"` + own))
		Expect(r.out).To(ContainSubstring(line + "    hint: keep it by writing for opencode >=1.0 <2.0: lola install a --project -t opencode@1.0" + own))
		Expect(r.out).NotTo(ContainSubstring("notice"), "printed in full, not counted")
	})

	It("gives a dropped value's and a chosen version's hints the scope they are for, picked or typed", func() {
		Expect(os.MkdirAll(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "opencode"), 0o755)).To(Succeed())
		a := filepath.Join(tmp, "a")
		writeTree(a, map[string]string{"lola.yml": "name: a\n", "agents/rev.md": "---\ndescription: Reviews.\ncolor: accent\n---\nReview.\n"})
		Expect(lola("cache", "add", a).code).To(Equal(0))
		user := func(cli.ScopeStep) ([]string, error) { return []string{"user"}, nil }
		p := &scripted{scope: []func(cli.ScopeStep) ([]string, error){user}, versions: []func(cli.VersionStep) (int, error){era(0)},
			review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", "a", "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("hint: keep it by writing for opencode >=1.0 <2.0: lola install a --user -t opencode@1.0; or "))

		Expect(os.Remove(config)).To(Succeed()) // nothing declared at user scope: asked again
		p = &scripted{scope: []func(cli.ScopeStep) ([]string, error){user}, versions: []func(cli.VersionStep) (int, error){era(1)},
			review: []string{cli.ReviewApply}}
		r = lolaP(p, "install", "a", "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("hint: to write for what this machine's opencode 2.2.5 reads (>=2.0): lola install a --user -t opencode@2.2.5\n"))

		Expect(os.Remove(config)).To(Succeed())
		r = lola("install", "a", "--scope", "both", "-t", "opencode@1.0", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("lola install a --project -t opencode@2.2.5 -y\n"))
		Expect(r.out).To(ContainSubstring("lola install a --user -t opencode@2.2.5 -y\n"))
	})

	It("says in full, on a sync that rewrites for a new version and in lola check, what that version drops, with the edit that keeps it", func() {
		a := filepath.Join(tmp, "a")
		writeTree(a, map[string]string{"lola.yml": "name: a\n", "agents/rev.md": "---\ndescription: Reviews.\ncolor: accent\n---\nReview.\n"})
		Expect(lola("cache", "add", a).code).To(Equal(0))
		Expect(lola("install", "a", "--project", "-t", "opencode@1.0").code).To(Equal(0))
		file := filepath.Join(proj, ".lola", "project.yml")
		b, err := os.ReadFile(file)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(file, []byte(strings.Replace(string(b), `opencode: "1.0"`, `opencode: "2.0"`, 1)), 0o600)).To(Succeed())
		line := `⚠ a: opencode agent rev: color "accent" dropped — opencode >=2.0: agent colors must be hex (#rrggbb)` + "\n" +
			`    hint: keep it by writing for opencode >=1.0 <2.0: set target_versions.opencode to "1.0" in .lola/project.yml, then: lola sync --project; or in rev.targets.yaml beside the agent`
		r := lola("sync", "--project", "--trust=entries")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(line))
		Expect(r.out).NotTo(ContainSubstring("notice"))
		r = lola("sync", "--project") // in sync: what the write said in full is counted once
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(Equal("→ a: 1 value dropped — lola check --project a\n✓ already in sync\n"))
		r = lola("check", "--project", "a", "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(line))
	})

	It("hints, in lola check of a module the scope does not install, the install that keeps a dropped value, outside a project too", func() {
		a := filepath.Join(tmp, "a")
		writeTree(a, map[string]string{"lola.yml": "name: a\n", "agents/rev.md": "---\ndescription: Reviews.\ncolor: accent\n---\nReview.\n"})
		line := `⚠ a: opencode agent rev: color "accent" dropped — opencode >=2.0: agent colors must be hex (#rrggbb)` + "\n" +
			"    hint: keep it by writing for opencode >=1.0 <2.0: lola install " + a + " --project -t opencode@1.0; or in rev.targets.yaml beside the agent"
		nowhere := filepath.Join(tmp, "nowhere")
		Expect(os.MkdirAll(nowhere, 0o755)).To(Succeed())
		GinkgoT().Chdir(nowhere)
		r := lola("check", a, "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(line))
		Expect(r.out).NotTo(ContainSubstring("set target_versions"))

		GinkgoT().Chdir(proj) // a project that installs another module
		Expect(lola("install", "m", "--project", "-t", "opencode@2.0").code).To(Equal(0))
		r = lola("check", a, "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(line))
		Expect(r.out).NotTo(ContainSubstring("set target_versions"))
	})

	It("names the scope of each module finding in a run of more than one scope, and only then", func() {
		Expect(os.MkdirAll(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "opencode"), 0o755)).To(Succeed())
		a := filepath.Join(tmp, "a")
		writeTree(a, map[string]string{"lola.yml": "name: a\n", "agents/rev.md": "---\ndescription: Reviews.\ncolor: accent\n---\nReview.\n"})
		Expect(lola("cache", "add", a).code).To(Equal(0))
		r := lola("install", "a", "--scope", "both", "-t", "opencode@2.0", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(`⚠ a (project): opencode agent rev: color "accent" dropped`))
		Expect(r.out).To(ContainSubstring(`⚠ a (user): opencode agent rev: color "accent" dropped`))
		r = lola("install", "a", "--project", "-t", "opencode@2.0", "--force")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(`⚠ a: opencode agent rev: color "accent" dropped`))
	})

	It("says top-level targets lola never trusted in a project.yml written by hand are not trusted yet", func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [opencode]\n"})
		Expect(lola("install", "m", "--project", "-t", "opencode@2.0").code).To(Equal(0))
		r := lola("install", "m", "--project")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: project.yml's top-level targets [opencode] are not trusted yet\n"))
	})

	It("shows, beside a refusal whose hint trusts top-level targets no entry installs to, what that hint trusts", func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntargets: [opencode]\ntarget_versions:\n  opencode: \"1.0\"\n"})
		r := lola("install", "m", "--project")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring(".lola/project.yml has not been trusted on this machine (a fresh clone, or a moved project):\n" +
			"  + targets       [opencode]\n" +
			"  + target_versions opencode \"1.0\"\n" +
			"error: project.yml's top-level targets [opencode] have not been trusted on this machine\n" +
			"  hint: trust them: lola sync --project --trust=entries\n"))
		r = lola("install", "m", "--project", "--plain")
		Expect(r.err).To(ContainSubstring("  + targets       [opencode]\n"), "the diff is never in --plain's rows")
		r = lola("install", "m", "--project", "--json")
		Expect(r.code).To(Equal(1))
		var doc struct {
			Trust struct {
				Targets        struct{ To []string }          `json:"targets"`
				TargetVersions struct{ To map[string]string } `json:"target_versions"`
			} `json:"trust"`
		}
		Expect(json.Unmarshal([]byte(r.err), &doc)).To(Succeed(), r.err)
		Expect(doc.Trust.Targets.To).To(Equal([]string{"opencode"}))
		Expect(doc.Trust.TargetVersions.To).To(Equal(map[string]string{"opencode": "1.0"}))
	})

	It("never hints --trust=entries for top-level targets when it would also trust entries the refusal does not show", func() {
		writeTree(proj, map[string]string{"mods/evil/lola.yml": "name: evil\n", "mods/evil/commands/e.md": "E.\n",
			".lola/project.yml": "version: 1\ntargets: [opencode]\ntarget_versions:\n  opencode: \"1.0\"\nmodules:\n  - name: evil\n    source: mods/evil\n    targets: [opencode]\n"})
		r := lola("install", "m", "--project")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("  hint: review and apply them: lola sync --project\n"))
		Expect(r.err).NotTo(ContainSubstring("--trust"))
		Expect(r.err).NotTo(ContainSubstring("+ targets"), "the sync it hints shows the diff")
	})

	It("ends a dry run with the run that applies it, as lola sync -n does", func() {
		r := lola("install", "m", "--project", "-t", "opencode@2.0", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HaveSuffix("→ to apply: lola install m --project -t opencode@2.0\n"))
		r = lola("install", "m", "--project", "-t", "opencode@2.0", "-n", "--json")
		Expect(r.code).To(Equal(0), r.err)
		Expect(parseInstall(r.out).EquivalentCommands).To(Equal([]string{"lola install m --project -t opencode@2.0"}), "JSON carries it, as sync -n's does")
		r = lola("install", "m", "-C", filepath.Join(tmp, "new"), "-t", "opencode@1.0", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(HaveSuffix("→ to apply: lola install m -C "+filepath.Join(tmp, "new")+" -t opencode@1.0 -y\n"), "-y creates the directory")
		Expect(lola("install", "m", "--project", "-t", "opencode@2.0").code).To(Equal(0))
		Expect(lola("install", "m", "--project", "-t", "opencode@2.0", "-n").out).NotTo(ContainSubstring("to apply"), "nothing to apply")
	})

	It("refuses a missing -C under -n as the run it previews would, with the same hints", func() {
		dir := filepath.Join(tmp, "new")
		real, dry := lola("install", "m", "-C", dir), lola("install", "m", "-C", dir, "-n")
		Expect(dry.code).To(Equal(1))
		Expect(dry.err).To(ContainSubstring("error: -C " + dir + " does not exist, so no assistant is set up there; name the ones it is for with -t\n"))
		Expect(dry.err).To(Equal(strings.ReplaceAll(real.err, "lola install m -C", "lola install m --dry-run -C")))
		Expect(dir).NotTo(BeAnExistingFile())
	})

	It("drops from its hints the -y that only created the -C directory", func() {
		Expect(os.Remove(filepath.Join(tmp, "bin", "opencode"))).To(Succeed())
		a := filepath.Join(tmp, "a")
		writeTree(a, map[string]string{"lola.yml": "name: a\n", "agents/rev.md": "---\ndescription: Reviews.\ncolor: accent\n---\nReview.\n"})
		Expect(lola("cache", "add", a).code).To(Equal(0))
		dir := filepath.Join(tmp, "new")
		r := lola("install", "a", "-C", dir, "-t", "opencode@2.0", "-y")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("hint: keep it by writing for opencode >=1.0 <2.0: lola install a -C " + dir + " -t opencode@1.0; or "))
	})

	It("refuses a second version after a comma, -t opencode@2.0,1.0, with the run giving one version per assistant", func() {
		r := lola("install", "m", "--project", "-t", "opencode@2.0,1.0")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("-t 1.0: 1.0 is a version, not an assistant; -t gives opencode one version, as in opencode@2.0\n" +
			"  hint: one version per assistant: lola install m --project -t opencode@2.0\n"))
		Expect(r.err).NotTo(ContainSubstring("unknown target"))
		r = lola("install", "m", "--project", "-t", "opencode@2.0,1.0,2.1")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("  hint: one version per assistant: lola install m --project -t opencode@2.0\n"), "every bare version is dropped")
		Expect(lola("install", "m", "--project", "-t", "opencode@2.0", "-n").code).To(Equal(0), "the hint runs")
		r = lola("install", "m", "--project", "-t", "1.0")
		Expect(r.err).To(ContainSubstring(`unknown target "1.0"`), "a version alone names no assistant before it")
	})

	It("checks for the version -t gives, as install takes it, and refuses one that is not a version with the runs for each era", func() {
		a := filepath.Join(tmp, "a")
		writeTree(a, map[string]string{"lola.yml": "name: a\n", "agents/rev.md": "---\ndescription: Reviews.\ncolor: accent\n---\nReview.\n"})
		r := lola("check", a, "-t", "opencode@1.0")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).NotTo(ContainSubstring("dropped"), "1.0 reads theme names")
		r = lola("check", a, "-t", "opencode@2.0")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(`color "accent" dropped — opencode >=2.0`))
		r = lola("check", a, "-t", "opencode@x")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring(`-t opencode@x: "x" is not a version`))
		Expect(r.err).To(ContainSubstring("hint: for opencode >=1.0 <2.0: lola check " + a + " -t opencode@1.0\n"))
	})

	It("says a module already up to date is, counting the values its version drops with the lola check that lists them", func() {
		a := filepath.Join(tmp, "a")
		writeTree(a, map[string]string{"lola.yml": "name: a\n", "agents/rev.md": "---\ndescription: Reviews.\ncolor: accent\n---\nReview.\n"})
		Expect(lola("cache", "add", a).code).To(Equal(0))
		Expect(lola("install", "a", "--project", "-t", "opencode@2.0").code).To(Equal(0))
		r := lola("install", "a", "m", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ a: 1 value dropped — lola check --project a\n"))
		Expect(r.out).To(ContainSubstring("✓ a already up to date (opencode)\n✓ installed m (opencode)\n"))
		Expect(r.out).NotTo(ContainSubstring(`color "accent" dropped`))
		r = lola("install", "a", "--project", "-v")
		Expect(r.out).To(ContainSubstring(`color "accent" dropped`), "--verbose prints it in full")
	})

	It("leaves out a dropped value's keep-it hint when the warning of the version chosen already gives that run", func() {
		fakeAssistant(tmp, "opencode", "opencode v1.19.5")
		a := filepath.Join(tmp, "a")
		writeTree(a, map[string]string{"lola.yml": "name: a\n", "agents/rev.md": "---\ndescription: Reviews.\ncolor: accent\n---\nReview.\n"})
		Expect(lola("cache", "add", a).code).To(Equal(0))
		r := lola("install", "a", "--project", "-t", "opencode@2.0")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring(`⚠ a: opencode agent rev: color "accent" dropped — opencode >=2.0: agent colors must be hex (#rrggbb)` + "\n" +
			"    hint: in rev.targets.yaml beside the agent"))
		Expect(r.out).To(ContainSubstring("hint: to write for what this machine's opencode 1.19.5 reads (>=1.0 <2.0): lola install a --project -t opencode@1.19.5\n"))
		Expect(r.out).NotTo(ContainSubstring("keep it by writing"))
	})

	It("offers Change versions for a version the scope declares, its era preselected, and records the new answer", func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntarget_versions:\n  opencode: \"2.0\"\n"})
		p := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("m")}, versions: []func(cli.VersionStep) (int, error){era(1)},
			review: []string{cli.ReviewVersions, cli.ReviewApply}}
		r := lolaP(p, "install", "--project", "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.reviewSteps[0].Summary).To(ContainSubstring("    → opencode >=2.0 (from .lola/project.yml)\n"))
		Expect(p.reviewSteps[0].Options).To(Equal([]string{cli.ReviewApply, cli.ReviewModules, cli.ReviewVersions, cli.ReviewQuit}))
		Expect(p.versionSteps).To(HaveLen(1))
		Expect(p.versionSteps[0].Preselect).To(Equal(0), "the declared era")
		Expect(p.reviewSteps[1].Summary).To(ContainSubstring("    → opencode >=1.0 <2.0 (was 2.0 in .lola/project.yml; installed 2.2.5)\n"))
		Expect(declared(proj)).To(Equal(map[string]string{"opencode": "1.0"}))
	})

	It("counts, after Change versions, the files an installed module's are rewritten for, those that come out the same as unchanged", func() {
		a := filepath.Join(tmp, "a")
		writeTree(a, map[string]string{"lola.yml": "name: a\n", "agents/rev.md": "---\ndescription: Reviews.\ncolor: accent\n---\nReview.\n",
			"agents/lint.md": "---\ndescription: Lints.\ncolor: \"#ff6b6b\"\n---\nLint.\n"})
		Expect(lola("cache", "add", a).code).To(Equal(0))
		Expect(lola("install", "a", "--project", "-t", "opencode@2.0").code).To(Equal(0))
		n := filepath.Join(tmp, "n")
		writeTree(n, map[string]string{"lola.yml": "name: n\n", "commands/d.md": "Do.\n"})
		Expect(lola("cache", "add", n).code).To(Equal(0))
		p := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("a", "n")}, versions: []func(cli.VersionStep) (int, error){era(1)},
			components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewVersions, cli.ReviewApply}}
		r := lolaP(p, "install", "--project", "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.reviewSteps[0].Summary).To(ContainSubstring("      a: = 2 unchanged\n"))
		Expect(p.reviewSteps[1].Summary).To(ContainSubstring("      a: 1 rewritten for opencode >=1.0 <2.0, 1 unchanged\n"), "rev's color is kept; lint comes out the same")
		Expect(p.reviewSteps[1].Summary).To(ContainSubstring("    → opencode >=1.0 <2.0 (was 2.0 in .lola/project.yml; installed 2.2.5)\n"))
		Expect(declared(proj)).To(Equal(map[string]string{"opencode": "1.0"}))
	})

	It("counts as rewritten only the components whose own files a rewrite changes, whatever their names", func() {
		a := filepath.Join(tmp, "a")
		writeTree(a, map[string]string{"lola.yml": "name: a\n", "agents/rev.md": "---\ndescription: Reviews.\ncolor: accent\n---\nReview.\n",
			"agents/agents.md": "---\ndescription: Lists agents.\ncolor: \"#ff6b6b\"\n---\nList.\n"})
		Expect(lola("cache", "add", a).code).To(Equal(0))
		Expect(lola("install", "a", "--project", "-t", "opencode@2.0").code).To(Equal(0))
		n := filepath.Join(tmp, "n")
		writeTree(n, map[string]string{"lola.yml": "name: n\n", "commands/d.md": "Do.\n"})
		Expect(lola("cache", "add", n).code).To(Equal(0))
		p := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("a", "n")}, versions: []func(cli.VersionStep) (int, error){era(1)},
			components: []func(cli.ComponentPage) ([]string, error){keep()}, review: []string{cli.ReviewVersions, cli.ReviewApply}}
		r := lolaP(p, "install", "--project", "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.reviewSteps[1].Summary).To(ContainSubstring("      a: 1 rewritten for opencode >=1.0 <2.0, 1 unchanged\n"), "rev's step lies under agents/ but writes rev only")
	})

	eraOf := func(dir, module string) string {
		GinkgoHelper()
		led, err := ledger.Load(filepath.Join(dir, ".lola", "lock.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(led.Modules).To(HaveKey(module))
		return led.Modules[module].Eras["opencode"]
	}

	It("asks no version, and offers no Change versions, for an assistant the scope's modules are written for", func() {
		n := filepath.Join(tmp, "n")
		writeTree(n, map[string]string{"lola.yml": "name: n\n", "commands/d.md": "Do.\n"})
		Expect(lola("cache", "add", n).code).To(Equal(0))
		Expect(lola("install", "m", "--project", "-t", "opencode@2.0").code).To(Equal(0))
		p := &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("n")}, review: []string{cli.ReviewApply}}
		r := lolaP(p, "install", "--project", "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.versionSteps).To(BeEmpty())
		Expect(p.reviewSteps[0].Options).NotTo(ContainElement(cli.ReviewVersions))
		Expect(p.reviewSteps[0].Summary).To(ContainSubstring("    → opencode >=2.0 (from .lola/project.yml; this scope's modules are written for it)\n"))
		Expect(eraOf(proj, "n")).To(Equal("2.0"))

		Expect(lola("uninstall", "m", "n", "--project", "-y").code).To(Equal(0))
		Expect(os.Remove(filepath.Join(tmp, "bin", "opencode"))).To(Succeed())
		Expect(os.WriteFile(filepath.Join(proj, ".lola", "project.yml"), []byte("version: 1\n"), 0o600)).To(Succeed())
		Expect(lola("install", "m", "--project", "-t", "opencode").code).To(Equal(0))
		Expect(declared(proj)).To(BeEmpty(), "not installed here: nothing recorded, written for the newest")
		p = &scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("n")}, review: []string{cli.ReviewApply}}
		r = lolaP(p, "install", "--project", "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.versionSteps).To(BeEmpty(), "nothing declared: m's version still stands")
		Expect(p.reviewSteps[0].Options).NotTo(ContainElement(cli.ReviewVersions))
		Expect(eraOf(proj, "n")).To(Equal("2.0"))
	})

	It("refuses an install for another version than the scope's modules are written for, and rewrites them all under --force", func() {
		Expect(lola("install", "m", "--project", "-t", "opencode@2.0").code).To(Equal(0))
		for _, name := range []string{"n", "x"} {
			dir := filepath.Join(tmp, name)
			writeTree(dir, map[string]string{"lola.yml": "name: " + name + "\n", "commands/" + name + ".md": "Do.\n"})
			Expect(lola("cache", "add", dir).code).To(Equal(0))
		}
		Expect(lola("install", "x", "--project").code).To(Equal(0))
		for _, dry := range [][]string{{"-n"}, nil} {
			r := lola(slices.Concat([]string{"install", "n", "--project", "-t", "opencode@1.0"}, dry)...)
			Expect(r.code).To(Equal(1), "%v", dry)
			if dry != nil {
				Expect(r.err).To(ContainSubstring("error: this install writes opencode files for >=1.0 <2.0, but m and x in this project are written for >=2.0"))
				continue
			}
			Expect(r.err).To(ContainSubstring("error: this install writes opencode files for >=1.0 <2.0, but m and x in this project are written for >=2.0; lola keeps one opencode version in a scope\n" +
				"  hint: install for >=2.0, the version m and x are written for: lola install n --project -t opencode@2.0\n" +
				"  hint: or rewrite m and x for >=1.0 <2.0 too: lola install n --project --target opencode@1.0 --force\n"))
		}
		Expect(filepath.Join(proj, ".opencode", "commands", "n.md")).NotTo(BeAnExistingFile())
		Expect(declared(proj)).To(Equal(map[string]string{"opencode": "2.0"}))
		Expect(eraOf(proj, "m")).To(Equal("2.0"))

		r := lola("install", "n", "--project", "-t", "opencode@1.0", "--force", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("m: would rewrite opencode files from >=2.0 to >=1.0 <2.0"))
		Expect(eraOf(proj, "m")).To(Equal("2.0"), "a dry run writes nothing")
		r = lola("install", "n", "--project", "-t", "opencode@1.0", "--force")
		Expect(r.code).To(Equal(0), r.err)
		for _, name := range []string{"m", "n", "x"} {
			Expect(eraOf(proj, name)).To(Equal("1.0"), name)
		}
		Expect(declared(proj)).To(Equal(map[string]string{"opencode": "1.0"}))
		Expect(lola("sync", "--project", "-n").out).NotTo(ContainSubstring("rewrite"), "nothing is left on the old version")
	})

	It("reports a module --force only rewrote for the new version by its rewrite line, never as installed", func() {
		Expect(lola("install", "m", "--project", "-t", "opencode@2.0").code).To(Equal(0))
		n := filepath.Join(tmp, "n")
		writeTree(n, map[string]string{"lola.yml": "name: n\n", "commands/d.md": "Do.\n"})
		Expect(lola("cache", "add", n).code).To(Equal(0))
		r := lola("install", "n", "--project", "-t", "opencode@1.0", "--force")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("m: rewrote opencode files from >=2.0 to >=1.0 <2.0"))
		Expect(r.out).To(ContainSubstring("✓ installed n (opencode)"))
		Expect(r.out).NotTo(ContainSubstring("installed m"))
		Expect(eraOf(proj, "m")).To(Equal("1.0"))
	})

	It("marks, under --json, a module --force only rewrote for the new version as rewritten, with or without -n", func() {
		Expect(lola("install", "m", "--project", "-t", "opencode@2.0").code).To(Equal(0))
		n := filepath.Join(tmp, "n")
		writeTree(n, map[string]string{"lola.yml": "name: n\n", "commands/d.md": "Do.\n"})
		Expect(lola("cache", "add", n).code).To(Equal(0))
		for _, dry := range [][]string{{"-n"}, nil} {
			r := lola(slices.Concat([]string{"install", "n", "--project", "-t", "opencode@1.0", "--force", "--json"}, dry)...)
			Expect(r.code).To(Equal(0), r.err)
			var got struct {
				Modules []map[string]any `json:"modules"`
			}
			Expect(json.Unmarshal([]byte(r.out), &got)).To(Succeed(), r.out)
			rewritten := map[string]any{}
			for _, m := range got.Modules {
				rewritten[m["name"].(string)] = m["rewritten"]
			}
			Expect(rewritten).To(Equal(map[string]any{"m": true, "n": nil}), "%v", dry)
		}
		Expect(eraOf(proj, "m")).To(Equal("1.0"))
	})

	It("refuses, nothing written, an install whose rewrite of the other modules follows an edit of target_versions not yet trusted, even under --force", func() {
		Expect(lola("install", "m", "--project", "-t", "opencode@2.0").code).To(Equal(0))
		file := filepath.Join(proj, ".lola", "project.yml")
		b, err := os.ReadFile(file)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(file, []byte(strings.Replace(string(b), `opencode: "2.0"`, `opencode: "1.0"`, 1)), 0o600)).To(Succeed())
		n := filepath.Join(tmp, "n")
		writeTree(n, map[string]string{"lola.yml": "name: n\n", "commands/d.md": "Do.\n"})
		Expect(lola("cache", "add", n).code).To(Equal(0))
		r := lola("install", "n", "--project")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: this install writes opencode files for >=1.0 <2.0, but m in this project is written for >=2.0"))
		r = lola("install", "n", "--project", "--force")
		Expect(r.code).To(Equal(1))
		Expect(r.err).To(ContainSubstring("error: rewriting m for the version this install writes for needs your trust first; nothing was written\n" +
			"  hint: see what for, and trust it: lola sync --project\n"))
		Expect(filepath.Join(proj, ".opencode", "commands", "d.md")).NotTo(BeAnExistingFile())
		Expect(eraOf(proj, "m")).To(Equal("2.0"))

		r = lola("install", "n", "--project", "-t", "opencode@2.0", "-n")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).NotTo(ContainSubstring("since you last trusted it"), "the install records the version itself")
		Expect(lola("sync", "--project", "--trust=entries").code).To(Equal(0))
		Expect(lola("install", "n", "--project").code).To(Equal(0))
		Expect(eraOf(proj, "m")).To(Equal("1.0"))
	})

	It("says when the version an install writes for comes from an edit of target_versions not yet trusted", func() {
		Expect(lola("install", "m", "--project", "-t", "opencode@2.0").code).To(Equal(0))
		file := filepath.Join(proj, ".lola", "project.yml")
		b, err := os.ReadFile(file)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(file, []byte(strings.Replace(string(b), `opencode: "2.0"`, `opencode: "2.1"`, 1)), 0o600)).To(Succeed())
		n := filepath.Join(tmp, "n")
		writeTree(n, map[string]string{"lola.yml": "name: n\n", "commands/d.md": "Do.\n"})
		Expect(lola("cache", "add", n).code).To(Equal(0))
		r := lola("install", "n", "--project")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ .lola/project.yml's target_versions changed since you last trusted it; lola sync --project shows the change\n"))
		Expect(r.out).NotTo(ContainSubstring("rewrite"), "2.1 is m's version too")
	})

	It("says a fresh clone's target_versions are not trusted yet, not that they changed", func() {
		writeTree(proj, map[string]string{".lola/project.yml": "version: 1\ntarget_versions:\n  opencode: \"1.0\"\n"})
		r := lola("install", "m", "--project", "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ .lola/project.yml's target_versions are not trusted yet; lola sync --project shows them\n"))
		Expect(r.out).NotTo(ContainSubstring("since you last trusted it"))
	})

	It("says in each version's answer what of the modules installed it drops, two by name", func() {
		files := map[string]string{"lola.yml": "name: a\n"}
		for _, n := range []string{"rev", "lint", "doc"} {
			files["agents/"+n+".md"] = "---\ndescription: D.\ncolor: accent\n---\nX.\n"
		}
		a := filepath.Join(tmp, "a")
		writeTree(a, files)
		Expect(lola("cache", "add", a).code).To(Equal(0))
		p := &scripted{versions: []func(cli.VersionStep) (int, error){era(0)}, components: []func(cli.ComponentPage) ([]string, error){keep()},
			review: []string{cli.ReviewQuit}}
		r := lolaP(p, "install", "a", "--project", "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(p.versionSteps[0].Options).To(Equal([]string{
			`>=2.0 — agent colors must be hex (#rrggbb); drops doc's color "accent", lint's color "accent" and 1 more`,
			">=1.0 <2.0 — agent colors may also be theme names such as accent"}))
	})

	It("names the declared version, and how to change it, when a re-run has nothing to do", func() {
		Expect(lola("install", "m", "--project", "-t", "opencode").code).To(Equal(0))
		r := lolaP(&scripted{modules: []func(cli.ModuleStep) (cli.ModuleAnswer, error){mods("m")}}, "install", "--project", "-t", "opencode")
		Expect(r.code).To(Equal(0), r.err)
		Expect(r.out).To(ContainSubstring("→ opencode >=2.0 (from .lola/project.yml)\n" +
			"    hint: to write for opencode >=1.0 <2.0: lola install m --project -t opencode@1.0\n✓ already up to date\n"))
		Expect(strings.Count(r.out, "→ opencode")).To(Equal(1))
	})

	It("documents -t <id>@<version> in install's help and lola profile --edit's", func() {
		Expect(lola("profile", "--help").out).To(ContainSubstring("<id>@<version> also records which version to write for"))
		r := lola("install", "--help")
		Expect(r.out).To(ContainSubstring("<id>@<version> also records which version to write for"))
		Expect(r.out).To(ContainSubstring("  lola install ./m -C /srv/app -t opencode@1.19\n"), "an example")
		Expect(r.out).To(ContainSubstring("opencode 2 takes only hex agent colors"), "what differs")
	})
})
