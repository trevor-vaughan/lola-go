package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// installFlags are lola install's flags.
type installFlags struct {
	targets, skips, onlys, unskips                                            []string
	ref, path, scope                                                          string
	dry, force, strict, rules, noRules, allowHooks, allowRuntime, global, yes bool
}

// addedSource is a source added to the cache during the run (Add a source…).
type addedSource struct{ source, ref string }

// installFlow is one run of lola install (Plan 5c §3, §4). It fills every answer the flags
// leave open, asking the Prompter on a terminal, shows the summary and review menu when a
// picker ran, and turns the answers into one engine.InstallSpec.
type installFlow struct {
	a     *app
	cmd   *cobra.Command
	eng   *engine.Engine
	p     Prompter
	ask   bool
	cwd   string
	f     installFlags
	args  []string
	guide argGuide
	edits []engine.SelectionEdit // from --skip, --only and --unskip

	resolution                  engine.ScopeResolution
	scopes                      []engine.ScopeChoice // nil until answered
	prevScopes                  []string             // the scope picker's last answer, preselected when Change scope asks again
	prevOther                   string               // the last Other directory as typed, offered again
	choices                     *engine.Choices
	pins                        []engine.VersionRef // the versions the module arguments resolved to, kept when Change scope asks again
	cachePins                   []engine.VersionRef // the versions of the modules picked from the cache, kept the same way
	modules                     []string            // module names; nil until answered
	prevMods                    []string
	targets                     map[string][]string            // destination (Scope.Key) → assistants (nil: the manifest's); nil until answered
	prevTarget                  []string                       // the last picker answer
	picks                       map[string]map[string][]string // destination (Scope.Key) → module → selected kind/name
	prevPicks                   map[string]map[string][]string // the picks before Change components, preselected
	added                       []addedSource
	droppedModule, droppedScope int             // component picks dropped because their module or destination was removed; reset after each summary
	asked                       map[string]bool // modules whose picks came from a component page
	shown                       map[string]bool // steps whose picker ran
}

// runInstall runs the install flow.
func (a *app) runInstall(cmd *cobra.Command, g argGuide, args []string, f installFlags) error {
	if (f.ref != "" || f.path != "") && len(args) > 1 {
		return errors.New("--ref and --path apply to a single source")
	}
	if (f.ref != "" || f.path != "") && len(args) == 0 {
		flag := "--ref"
		if f.ref == "" {
			flag = "--path"
		}
		return errs.WithHints(errors.New("--ref and --path apply to a source argument, and none was given"),
			"name the source they apply to: lola install <source> "+flag+" …")
	}
	eng, err := a.engine()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	edits, err := selectionEdits(f.skips, f.onlys, f.unskips)
	if err != nil {
		return err
	}
	if f.global && len(edits) == 0 {
		return errs.WithHints(errors.New("--global changes where --skip, --only and --unskip are saved, and none was given"),
			"example: lola install my-module --skip skill/slow-lint --global")
	}
	p := a.prompter(cmd)
	fl := &installFlow{a: a, cmd: cmd, eng: eng, p: p, ask: !a.json && p.Interactive(), cwd: cwd, f: f, args: args, guide: g,
		edits: edits, shown: map[string]bool{}, asked: map[string]bool{}}
	if !fl.ask && len(args) == 0 {
		return g.args(1)(cmd, args)
	}
	return fl.run()
}

// prompter returns the injected Prompter, the non-interactive one under --json, or the huh
// adapter.
func (a *app) prompter(cmd *cobra.Command) Prompter {
	if a.json {
		return noPrompter{}
	}
	if a.newPrompter != nil {
		return a.newPrompter(cmd)
	}
	return a.huhPrompter(cmd)
}

func (fl *installFlow) run() error {
	var spec engine.InstallSpec
	for {
		if err := fl.ensureScopes(); err != nil {
			return err
		}
		if err := fl.ensureChoices(); err != nil {
			return err
		}
		if err := fl.ensureModules(); err != nil {
			return err
		}
		if err := fl.ensureAssistants(); err != nil {
			return err
		}
		if err := fl.ensureComponents(); err != nil {
			return err
		}
		var err error
		if spec, err = fl.spec(); err != nil {
			return err
		}
		if !fl.ask || len(fl.shown) == 0 {
			break
		}
		pv, err := fl.choices.Preview(spec, fl.modules)
		if err != nil {
			return err
		}
		changed, err := fl.changes(pv)
		if err != nil {
			return err
		}
		if !changed {
			fmt.Fprintln(fl.cmd.OutOrStdout(), fl.a.style(fl.cmd).ok("already up to date"))
			return nil
		}
		fl.printSummary(fl.cmd.ErrOrStderr(), spec, pv)
		fl.droppedModule, fl.droppedScope = 0, 0
		if fl.f.yes {
			break
		}
		choice, err := fl.p.Review(ReviewStep{Options: fl.reviewOptions()})
		if err != nil {
			return err
		}
		if done, err := fl.review(choice); done || err != nil {
			if err != nil {
				return err
			}
			break
		}
	}
	fl.a.hookOptions(fl.cmd, &spec.Options, fl.f.allowHooks, fl.f.allowRuntime)
	res, err := fl.eng.InstallAll(fl.cmd.Context(), spec)
	if err != nil {
		return err
	}
	if saved := savedTo(res.Result); saved != "" {
		fmt.Fprintln(fl.cmd.ErrOrStderr(), "→ saved to "+untrusted.Text(saved, untrusted.MaxName))
	}
	eq := []string{}
	if len(fl.shown) > 0 {
		eq = fl.equivalents(spec)
	}
	return fl.a.emit(fl.cmd, func(w io.Writer, s style) { printInstall(w, s, res) }, installOutput{InstallResult: res, EquivalentCommands: eq})
}

// review acts on the review menu: Apply ends the loop, Quit cancels, a Change entry forgets
// that step's answer (kept as its preselection) so the loop asks it again.
func (fl *installFlow) review(choice string) (bool, error) {
	switch choice {
	case ReviewApply:
		return true, nil
	case ReviewScope:
		fl.pins = fl.choices.Versions
		fl.scopes, fl.choices, fl.targets = nil, nil, nil
	case ReviewModules:
		fl.prevMods, fl.modules = fl.modules, nil
	case ReviewAssistants:
		fl.targets = nil
	case ReviewComponents:
		fl.prevPicks, fl.picks, fl.asked = fl.picks, nil, map[string]bool{}
	default:
		return false, ErrAborted
	}
	return false, nil
}

// reviewOptions offers Apply, a Change entry for every step whose picker was shown, and Quit.
func (fl *installFlow) reviewOptions() []string {
	out := []string{ReviewApply}
	for _, step := range []string{ReviewScope, ReviewModules, ReviewAssistants, ReviewComponents} {
		if fl.shown[step] {
			out = append(out, step)
		}
	}
	return append(out, ReviewQuit)
}

// destLabel names a destination in the flow's text: "user", or "project (<root>)" with the root
// made safe to print.
func destLabel(sc engine.Scope) string {
	if sc.ProjectDir == "" {
		return sc.Kind.String()
	}
	return sc.Kind.String() + " (" + untrusted.Text(sc.ProjectDir, untrusted.MaxName) + ")"
}

// ensureScopes resolves the scopes (§5.1), asking on a terminal when no flag gives them.
func (fl *installFlow) ensureScopes() error {
	if fl.scopes != nil {
		return nil
	}
	dir, err := fl.a.projectDir()
	if err != nil {
		return err
	}
	res, err := fl.eng.ResolveInstallScope(engine.ScopeRequest{Scope: fl.f.scope, User: fl.a.user, ProjectDir: dir, Cwd: fl.cwd, Interactive: fl.ask})
	if err != nil {
		return err
	}
	fl.resolution = res
	delete(fl.shown, ReviewScope)
	if !res.Pick {
		for _, c := range res.Scopes {
			if c.Source == engine.SourceDiscovered {
				fmt.Fprintf(fl.cmd.ErrOrStderr(), "scope: project (found .lola/ at %s)\n", untrusted.Text(c.Scope.ProjectDir, untrusted.MaxName))
			}
		}
		fl.scopes = res.Scopes
		return nil
	}
	step := ScopeStep{ProjectRoot: untrusted.Text(res.ProjectRoot, untrusted.MaxName), Home: fl.eng.Paths.Home, Preselect: fl.prevScopes}
	for {
		entries, err := fl.p.Scope(step)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			step.Problem = "pick at least one scope"
			continue
		}
		home := engine.Scope{Kind: target.Project, ProjectDir: fl.eng.Paths.Home}.Key()
		user := slices.Contains(entries, ScopeUser)
		if slices.Contains(entries, ScopeProject) && (engine.Scope{Kind: target.Project, ProjectDir: res.ProjectRoot}).Key() == home {
			step.Problem = "the project here is your home directory; tick User instead"
			continue
		}
		// Project roots first, then the user scope, as --scope both orders them.
		var scopes []engine.ScopeChoice
		if slices.Contains(entries, ScopeProject) {
			scopes = engine.Scopes([]string{config.ScopeProject}, res.ProjectRoot, engine.SourcePicked)
		}
		if slices.Contains(entries, ScopeOther) {
			other, err := fl.otherDirectory(scopes)
			if err != nil {
				return err
			}
			scopes = append(scopes, engine.Scopes([]string{config.ScopeProject}, other, engine.SourcePicked)...)
		}
		if user {
			scopes = append(scopes, engine.Scopes([]string{config.ScopeUser}, "", engine.SourcePicked)...)
		}
		fl.shown[ReviewScope], fl.prevScopes, fl.scopes = true, entries, scopes
		for key, byMod := range fl.picks {
			if !slices.ContainsFunc(fl.scopes, func(c engine.ScopeChoice) bool { return c.Scope.Key() == key }) {
				for _, picked := range byMod {
					fl.droppedScope += len(picked)
				}
				delete(fl.picks, key)
			}
		}
		return nil
	}
}

// otherDirectory asks for the directory of Other directory… (§14) until the answer is an
// existing directory that is not the project picked above, nor the home directory (the user
// scope's root, which install refuses as a project), and returns it absolute. A relative
// answer is taken from the current directory; ~ is not expanded. An answer is offered again to
// edit only when it is safe to show.
func (fl *installFlow) otherDirectory(picked []engine.ScopeChoice) (string, error) {
	home := engine.Scope{Kind: target.Project, ProjectDir: fl.eng.Paths.Home}.Key()
	offer := func(in string) string {
		if untrusted.Text(in, len(in)) != in {
			return ""
		}
		return in
	}
	step := DirectoryStep{Value: fl.prevOther}
	for {
		in, err := fl.p.OtherDirectory(step)
		if err != nil {
			return "", err
		}
		dir := in
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(fl.cwd, dir)
		}
		dir = filepath.Clean(dir)
		key := engine.Scope{Kind: target.Project, ProjectDir: dir}.Key()
		safe := untrusted.Text(dir, untrusted.MaxName)
		fi, err := os.Stat(dir)
		var problem string
		var pathErr *fs.PathError
		switch {
		case in == "":
			problem = "enter the path of a directory"
		case errors.Is(err, fs.ErrNotExist):
			problem = safe + " does not exist"
			if strings.HasPrefix(in, "~") {
				problem += " (~ is not expanded)"
			}
		case errors.As(err, &pathErr):
			problem = safe + ": " + pathErr.Err.Error()
		case err != nil:
			problem = untrusted.Text(err.Error(), untrusted.MaxName)
		case !fi.IsDir():
			problem = safe + " is not a directory"
		case slices.ContainsFunc(picked, func(c engine.ScopeChoice) bool { return c.Scope.Key() == key }):
			problem = "that is the project above"
		case key == home:
			problem = "that is your user scope (~); tick User instead"
		}
		if problem == "" {
			fl.prevOther = offer(in)
			return dir, nil
		}
		step = DirectoryStep{Value: offer(in), Problem: problem}
	}
}

// ensureChoices computes what the pickers offer; off a terminal nothing is offered.
func (fl *installFlow) ensureChoices() error {
	if !fl.ask || fl.choices != nil {
		return nil
	}
	ch, err := fl.eng.InstallChoices(fl.cmd.Context(), engine.ChoicesRequest{Cwd: fl.cwd, Scopes: fl.scopes, Modules: fl.requests()})
	fl.choices = ch
	return err
}

// requests are the module arguments and the modules picked from the cache, pinned to the
// versions listed before Change scope.
func (fl *installFlow) requests() []engine.InstallRequest {
	var out []engine.InstallRequest
	for i, arg := range fl.args {
		r := engine.InstallRequest{Arg: arg, Ref: fl.f.ref, Path: fl.f.path}
		if i < len(fl.pins) {
			r.Version = &fl.pins[i]
		}
		out = append(out, r)
	}
	for i := range fl.cachePins {
		out = append(out, engine.InstallRequest{Arg: fl.cachePins[i].Name, Version: &fl.cachePins[i]})
	}
	return out
}

// ensureModules takes the module arguments, or asks for modules from the cache.
func (fl *installFlow) ensureModules() error {
	if fl.modules != nil {
		return nil
	}
	if len(fl.args) > 0 {
		fl.modules = fl.args
		if fl.ask {
			fl.modules = slices.Clone(fl.choices.Requested)
		}
		return nil
	}
	delete(fl.shown, ReviewModules)
	step := ModuleStep{Options: fl.moduleOptions(), Preselect: fl.prevMods}
	for {
		ans, err := fl.p.Modules(step)
		if err != nil {
			return err
		}
		if ans.AddSource && ans.Source == "" {
			step = ModuleStep{Options: fl.moduleOptions(), Preselect: ans.Names}
			continue
		}
		if ans.AddSource {
			v, err := fl.eng.AddSource(fl.cmd.Context(), fl.cwd, ans.Source, ans.Ref, "")
			if err != nil {
				writeError(fl.cmd.ErrOrStderr(), err, false)
				step.Problem = "could not add " + untrusted.Text(ans.Source, untrusted.MaxName) + "; try another source"
				continue
			}
			fl.added = append(fl.added, addedSource{source: ans.Source, ref: ans.Ref})
			fl.cachePins = slices.DeleteFunc(fl.cachePins, func(p engine.VersionRef) bool { return p.Name == v.Name }) // list what was just fetched
			fl.choices = nil
			if err := fl.ensureChoices(); err != nil {
				return err
			}
			step = ModuleStep{Options: fl.moduleOptions(), Preselect: union(slices.Clone(ans.Names), []string{v.Name})}
			continue
		}
		if len(ans.Names) == 0 {
			step.Problem = "pick at least one module"
			continue
		}
		fl.shown[ReviewModules] = true
		fl.modules = ans.Names
		fl.cachePins = nil
		for _, name := range fl.modules {
			if m := fl.choices.Module(name); m != nil {
				fl.cachePins = append(fl.cachePins, engine.VersionRef{Name: name, ID: m.ID})
			}
		}
		fl.dropRemovedPicks()
		return nil
	}
}

// dropRemovedPicks forgets the picks of modules no longer chosen and counts them, once per
// component whatever the number of scopes.
func (fl *installFlow) dropRemovedPicks() {
	lost := map[string]map[string]bool{}
	for _, byMod := range fl.picks {
		for m, picked := range byMod {
			if slices.Contains(fl.modules, m) {
				continue
			}
			if lost[m] == nil {
				lost[m] = map[string]bool{}
			}
			for _, c := range picked {
				lost[m][c] = true
			}
			delete(byMod, m)
		}
	}
	for _, comps := range lost {
		fl.droppedModule += len(comps)
	}
}

// moduleOptions labels the cached modules: source · name — description, marked (installed)
// when installed in a chosen scope.
func (fl *installFlow) moduleOptions() []ModuleOption {
	var out []ModuleOption
	for _, m := range fl.choices.Modules {
		label := m.Source + " · " + m.Name
		if m.Description != "" {
			label += " — " + m.Description
		}
		installed := false
		for _, s := range fl.scopes {
			installed = installed || m.Scope(s.Scope.Key()).Installed
		}
		if installed {
			label += " (installed)"
		}
		out = append(out, ModuleOption{Name: m.Name, Label: label})
	}
	return out
}

// ensureAssistants decides each scope's assistants (§4.3): -t, else the targets the scope's
// manifest fixes; off a terminal, those installed and set up here; on one, the only
// assistant installed, else the picker (once, for every scope whose targets are open).
func (fl *installFlow) ensureAssistants() error {
	if fl.targets != nil {
		return nil
	}
	delete(fl.shown, ReviewAssistants)
	answer := map[string][]string{}
	var open, openLabels []string
	var installed, here []string
	for _, c := range fl.scopes {
		name := c.Scope.Key()
		if len(fl.f.targets) > 0 {
			answer[name] = fl.f.targets
			continue
		}
		man, err := fl.eng.Manifest(c.Scope)
		if err != nil {
			return err
		}
		if len(man.Targets) > 0 {
			answer[name] = nil
			continue
		}
		var inst, marked []string
		for _, as := range fl.eng.Detect(c.Scope) {
			if as.Binary == "" {
				continue
			}
			inst = append(inst, as.Target)
			if as.Marked {
				marked = append(marked, as.Target)
			}
		}
		switch {
		case len(inst) == 0:
			return errs.WithHints(errors.New("no assistant found on your PATH, so lola does not know where to install"),
				"pass -t with one or more of: "+strings.Join(targets.IDs(), ", "),
				"example: lola install ./my-module -t claude-code")
		case !fl.ask && len(marked) == 0:
			return errs.WithHints(errors.New("none of your installed assistants is set up here, so lola does not know where to install"),
				"installed: "+strings.Join(inst, ", "),
				"pass -t with one or more of them")
		case !fl.ask:
			answer[name] = marked
		case len(inst) == 1:
			answer[name] = inst
		default:
			open, openLabels = append(open, name), append(openLabels, destLabel(c.Scope))
			installed, here = union(installed, inst), union(here, marked)
		}
	}
	if len(open) > 0 {
		pre := fl.prevTarget
		if pre == nil {
			pre = here
		}
		if len(pre) == 0 {
			pre = installed
		}
		step := AssistantStep{Options: installed, Preselect: pre, Scopes: openLabels}
		for {
			picked, err := fl.p.Assistants(step)
			if err != nil {
				return err
			}
			if len(picked) > 0 {
				fl.shown[ReviewAssistants], fl.prevTarget = true, picked
				for _, s := range open {
					answer[s] = picked
				}
				break
			}
			step.Problem = "pick at least one assistant"
		}
	}
	fl.targets = answer
	return nil
}

func union(a, b []string) []string {
	for _, x := range b {
		if !slices.Contains(a, x) {
			a = append(a, x)
		}
	}
	return a
}

// rulesOff reports whether rules are off for a module at the destination key: --no-rules, or
// rules: false in its manifest entry without --rules.
func (fl *installFlow) rulesOff(m *engine.ModuleChoice, key string) bool {
	return fl.f.noRules || (m.Scope(key).RulesOff && !fl.f.rules)
}

// preselected is a module's preselection at the destination key: every component minus the
// exclusions, rules left out when they are off.
func (fl *installFlow) preselected(m *engine.ModuleChoice, key string) []string {
	ms := m.Scope(key)
	out := []string{}
	for _, c := range m.Components {
		if (c.Kind == config.KindRule && fl.rulesOff(m, key)) || slices.Contains(ms.Excluded, c.String()) {
			continue
		}
		out = append(out, c.String())
	}
	return out
}

// ensureComponents asks for each module's components (§4.4, §14), one page per module, or one
// per destination when the destinations' preselections differ; a page for one of several
// destinations names it. Not asked under --skip, --only or --unskip, or off a terminal. A
// module with one component and no exclusion, or with only rules while rules are off, needs no
// page.
func (fl *installFlow) ensureComponents() error {
	if !fl.ask || len(fl.edits) > 0 {
		return nil
	}
	if fl.picks == nil {
		fl.picks = map[string]map[string][]string{}
	}
	for _, c := range fl.scopes {
		if fl.picks[c.Scope.Key()] == nil {
			fl.picks[c.Scope.Key()] = map[string][]string{}
		}
	}
	for _, name := range slices.Clone(fl.modules) {
		m := fl.choices.Module(name)
		if m == nil {
			continue
		}
		var missing []engine.ScopeChoice
		for _, c := range fl.scopes {
			if _, ok := fl.picks[c.Scope.Key()][name]; !ok {
				missing = append(missing, c)
			}
		}
		if len(missing) == 0 {
			continue
		}
		pickable := slices.ContainsFunc(missing, func(d engine.ScopeChoice) bool {
			return slices.ContainsFunc(m.Components, func(c engine.ComponentChoice) bool {
				return c.Kind != config.KindRule || !fl.rulesOff(m, d.Scope.Key())
			})
		})
		single := len(m.Components) == 1 && !slices.ContainsFunc(missing, func(d engine.ScopeChoice) bool { return len(m.Scope(d.Scope.Key()).Excluded) > 0 })
		if !pickable || single {
			for _, d := range missing {
				fl.picks[d.Scope.Key()][name] = fl.preselected(m, d.Scope.Key())
			}
			continue
		}
		first := missing[0].Scope.Key()
		pages := [][]engine.ScopeChoice{missing}
		if slices.ContainsFunc(missing[1:], func(d engine.ScopeChoice) bool {
			return !slices.Equal(fl.preselected(m, first), fl.preselected(m, d.Scope.Key())) || fl.rulesOff(m, first) != fl.rulesOff(m, d.Scope.Key())
		}) {
			pages = nil
			for _, d := range missing {
				pages = append(pages, []engine.ScopeChoice{d})
			}
		}
		for _, dests := range pages {
			title := name
			if len(dests) == 1 && len(fl.scopes) > 1 {
				title += " — " + destLabel(dests[0].Scope)
			}
			key := dests[0].Scope.Key()
			picked, err := fl.p.Components(fl.page(m, key, title))
			if err != nil {
				return err
			}
			fl.asked[name] = true
			if len(picked) == 0 && len(fl.preselected(m, key)) > 0 {
				ok, err := fl.p.ConfirmEmpty(name)
				if err != nil {
					return err
				}
				if !ok {
					fl.modules = slices.DeleteFunc(fl.modules, func(x string) bool { return x == name })
					for _, byMod := range fl.picks {
						delete(byMod, name)
					}
					break
				}
			}
			for _, d := range dests {
				fl.picks[d.Scope.Key()][name] = picked
			}
		}
	}
	delete(fl.shown, ReviewComponents)
	for _, name := range fl.modules {
		if fl.asked[name] {
			fl.shown[ReviewComponents] = true
		}
	}
	return nil
}

// kindTitles name the kinds on a component page.
var kindTitles = map[string]string{config.KindSkill: "Skills", config.KindCommand: "Commands", config.KindAgent: "Agents",
	config.KindMCP: "MCP servers", config.KindRule: "Rules"}

// page builds a module's component page for the destination key: one group per kind with a
// count, current picks (or the preselection) selected, rules disabled when they are off.
func (fl *installFlow) page(m *engine.ModuleChoice, key, title string) ComponentPage {
	pre := fl.preselected(m, key)
	if prev, ok := fl.prevPicks[key][m.Name]; ok {
		pre = prev
	}
	ms := m.Scope(key)
	pg := ComponentPage{Module: m.Name, Title: title}
	for _, kind := range config.Kinds() {
		var g ComponentGroup
		for _, c := range m.Components {
			if c.Kind != kind {
				continue
			}
			label := c.Name
			if c.Description != "" {
				label += " — " + c.Description
			}
			if c.RunsCommand {
				label += " (runs a command)"
			}
			if slices.Contains(ms.Excluded, c.String()) {
				label += " (skipped in selections.yaml)"
			}
			g.Options = append(g.Options, ComponentOption{ID: c.String(), Label: label, Selected: slices.Contains(pre, c.String())})
		}
		if len(g.Options) == 0 {
			continue
		}
		g.Kind, g.Title = kind, fmt.Sprintf("%s (%d)", kindTitles[kind], len(g.Options))
		if kind == config.KindRule && fl.rulesOff(m, key) {
			g.Disabled, g.Note = true, "(off: --no-rules)"
			for i := range g.Options {
				g.Options[i].Selected = false
			}
		}
		pg.Groups = append(pg.Groups, g)
	}
	if m.RuntimeHooks > 0 {
		pg.Footer = fmt.Sprintf("Runtime hooks: %d (you will be asked before they are installed)", m.RuntimeHooks)
	}
	return pg
}

// pickEdits turns the component picks at the destination key into selection edits: a component
// not picked and not excluded is skipped, one picked but excluded is unskipped. Rules that are
// off are left as they are.
func (fl *installFlow) pickEdits(key string) []engine.SelectionEdit {
	var out []engine.SelectionEdit
	for _, name := range fl.modules {
		picked, ok := fl.picks[key][name]
		m := fl.choices.Module(name)
		if !ok || m == nil {
			continue
		}
		ms := m.Scope(key)
		for _, c := range m.Components {
			if c.Kind == config.KindRule && fl.rulesOff(m, key) {
				continue
			}
			sel := config.Selector{Module: name, Pattern: config.Pattern{Kind: c.Kind, Name: c.Name}}
			on, excluded := slices.Contains(picked, c.String()), slices.Contains(ms.Excluded, c.String())
			switch {
			case !on && !excluded:
				out = append(out, engine.SelectionEdit{Op: engine.EditSkip, Selector: sel})
			case on && excluded:
				out = append(out, engine.SelectionEdit{Op: engine.EditUnskip, Selector: sel})
			}
		}
	}
	return out
}

// spec builds the install from the answers.
func (fl *installFlow) spec() (engine.InstallSpec, error) {
	spec := engine.InstallSpec{Cwd: fl.cwd, Options: engine.Options{DryRun: fl.f.dry, Force: fl.f.force, Strict: fl.f.strict}}
	if fl.f.rules || fl.f.noRules {
		spec.Options.Rules = &fl.f.rules
	}
	for _, c := range fl.scopes {
		key := c.Scope.Key()
		si := engine.ScopeInstall{ScopeChoice: c, Targets: fl.targets[key]}
		if fl.picks != nil {
			si.Edits = fl.pickEdits(key)
		}
		spec.Scopes = append(spec.Scopes, si)
	}
	for i, arg := range fl.args {
		req := engine.InstallRequest{Arg: arg, Ref: fl.f.ref, Path: fl.f.path}
		if fl.ask {
			if !slices.Contains(fl.modules, fl.choices.Requested[i]) {
				continue // dropped from this run at the component step
			}
			req.Version = &fl.choices.Versions[i] // exactly the version the pickers listed
		}
		spec.Modules = append(spec.Modules, req)
	}
	if len(fl.args) == 0 {
		for _, name := range fl.modules {
			req := engine.InstallRequest{Arg: name}
			if m := fl.choices.Module(name); m != nil {
				req.Version = &engine.VersionRef{Name: name, ID: m.ID}
			}
			spec.Modules = append(spec.Modules, req)
		}
	}
	if len(spec.Modules) == 0 {
		return spec, ErrAborted
	}
	return spec, spec.AddFlagEdits(fl.edits, fl.f.global)
}
