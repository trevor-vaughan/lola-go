package cli

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// installFlags are lola install's flags.
type installFlags struct {
	targets, skips, onlys, unskips                  []string
	ref, path                                       string
	dry, force, strict, rules, noRules, global, yes bool
	trust                                           engine.TrustKinds
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
	picker                      *scopePicker
	choices                     *engine.Choices
	pins                        []engine.VersionRef // the versions the module arguments resolved to, kept when Change scope asks again
	cachePins                   []engine.VersionRef // the versions of the modules picked from the cache, kept the same way
	marketPicks                 []string            // the marketplace entries picked, by address, in pick order (Plan 6c §6.5)
	marketPins                  []engine.VersionRef // the versions they resolved to, kept when Change scope asks again
	marketNames                 map[string]string   // the module name each picked address resolved to → that address
	shownAddrs                  []string            // addresses added with Add a source…, shown in their group though cached
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
	warned                      map[string]bool // choice warnings printed, each once per run
}

// runInstall runs the install flow.
func (a *app) runInstall(cmd *cobra.Command, g argGuide, args []string, f installFlags) error {
	if (f.ref != "" || f.path != "") && len(args) > 1 {
		return errors.New("--ref and --path apply to a single source")
	}
	if f.path != "" && len(args) == 1 {
		if _, ok, _ := market.ParseAddress(args[0]); ok {
			return errs.WithHints(fmt.Errorf("--path does not apply to %s: its catalog gives the path", untrusted.Text(args[0], untrusted.MaxName)),
				runWithout(cmd, args, "path"))
		}
	}
	if (f.ref != "" || f.path != "") && len(args) == 0 {
		// No command would do what was meant without knowing the source: the message says it.
		var given, example []string
		for _, kv := range [][2]string{{f.ref, "--ref"}, {f.path, "--path"}} {
			if kv[0] != "" {
				given, example = append(given, kv[1]), append(example, kv[1]+" <value>")
			}
		}
		verb := " applies"
		if len(given) > 1 {
			verb = " apply"
		}
		return fmt.Errorf("%s%s to a source argument, and none was given: name it, as in lola install <source> %s",
			strings.Join(given, " and "), verb, strings.Join(example, " "))
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
		// The run without --global does all the run asked; without a module, no command does.
		err := errors.New("--global saves --skip, --only and --unskip for every scope, and none was given: add one, " +
			"as in lola install <module> --skip <kind>/<name> --global")
		if len(args) == 0 {
			return err
		}
		return errs.WithHints(err, runWithout(cmd, args, append([]string{"global"}, consentFlags...)...))
	}
	p := a.prompter(cmd)
	fl := &installFlow{a: a, cmd: cmd, eng: eng, p: p, ask: !a.json && p.Interactive(), cwd: cwd, f: f, args: args, guide: g,
		edits: edits, picker: &scopePicker{p: p, eng: eng, cwd: cwd, refuseHome: true}, shown: map[string]bool{}, asked: map[string]bool{}, warned: map[string]bool{}}
	if !fl.ask && len(args) == 0 {
		return fl.missingArgument()
	}
	if err := fl.expandMarkets(); err != nil {
		return err
	}
	return fl.run()
}

// expandMarkets replaces each argument that means an entry of your marketplaces with that
// entry's address (Plan 6c §3.3, §6.5): @<market> alone, on a terminal, by the entries picked
// from its group (off one the engine refuses it, naming its first entries); and a bare name your
// catalogs list that your cache lacks and that is no source here (engine.MarketMeaning), by the
// entry picked on a terminal, never silently; off one the run is refused, every such name at
// once (bareNames). --path is then refused with any address, as the catalog gives the path.
func (fl *installFlow) expandMarkets() error {
	var out []string
	listed := map[string][]engine.MarketOffer{}
	for _, arg := range fl.args {
		addr, isAddr, err := market.ParseAddress(arg)
		switch {
		case err != nil:
			return err
		case isAddr && addr.Entry == "" && fl.ask:
			picks, err := fl.browse(addr.Market)
			if err != nil {
				return err
			}
			out = append(out, picks...)
			continue
		case !isAddr:
			offers, err := fl.eng.MarketMeaning(arg, fl.cwd, outputFlags(fl.cmd))
			if err != nil {
				return err
			}
			if len(offers) > 0 && fl.ask {
				var opts []ModuleOption
				for _, o := range offers {
					opts = append(opts, ModuleOption{Name: o.Install, Label: offerLabel(o.Install, o)})
				}
				if arg, err = fl.p.MarketEntry(MarketEntryStep{Name: arg, Options: opts}); err != nil {
					return err
				}
			} else if len(offers) > 0 {
				listed[arg] = offers
			}
		}
		out = append(out, arg)
	}
	if len(listed) > 0 {
		return fl.bareNames(listed)
	}
	if (fl.f.ref != "" || fl.f.path != "") && len(out) > 1 {
		return errors.New("--ref and --path apply to a single source, and you picked more than one entry")
	}
	if fl.f.path != "" && slices.ContainsFunc(out, func(a string) bool { return strings.HasPrefix(a, "@") }) {
		return errs.WithHints(errors.New("--path does not apply to a marketplace entry: its catalog gives the path"), runWithout(fl.cmd, out, "path"))
	}
	fl.args = out
	return nil
}

// maxBareHints is how many runs a refusal of bare names lists: one per combination of the
// marketplaces listing each name.
const maxBareHints = 10

// bareNames refuses, off a terminal, a run whose bare names listed maps to the marketplace
// entries each may mean (Plan 6c §3.3): one hint per combination of them, each the run with every
// such name replaced by an address and every flag kept but --path, so each hint runs; with more
// combinations than maxBareHints, none, and the message says what to type.
func (fl *installFlow) bareNames(listed map[string][]engine.MarketOffer) error {
	var names, lists []string
	combos := 1 // counted, stopping past maxBareHints, before any is built
	for _, name := range fl.args {
		offers, ok := listed[name]
		if !ok {
			continue
		}
		var addrs []string
		for _, o := range offers {
			addrs = append(addrs, o.Install)
		}
		if combos <= maxBareHints {
			combos *= len(offers)
		}
		names, lists = append(names, untrusted.Text(name, untrusted.MaxName)), append(lists, strings.Join(addrs, ", "))
	}
	msg := names[0] + " is not in your cache, and your marketplaces list it: " + lists[0] + "; name the one to install"
	if len(names) > 1 {
		var each []string
		for i := range names {
			each = append(each, names[i]+" ("+lists[i]+")")
		}
		msg = strings.Join(each, ", ") + " are not in your cache, and your marketplaces list them; name the ones to install"
	}
	if combos > maxBareHints {
		return errors.New(msg + " as @<marketplace>/<name>")
	}
	runs := [][]string{slices.Clone(fl.args)}
	for i, name := range fl.args {
		offers, ok := listed[name]
		if !ok {
			continue
		}
		var next [][]string
		for _, o := range offers {
			for _, run := range runs {
				r := slices.Clone(run)
				r[i] = o.Install
				next = append(next, r)
			}
		}
		runs = next
	}
	var hints []string
	for _, run := range runs {
		hints = append(hints, runWithout(fl.cmd, run, "path"))
	}
	return errs.WithHints(errors.New(msg), hints...)
}

// browse asks for the entries of the marketplace name to install, its group alone (lola
// install @acme, Plan 6c §6.5), every entry it offers; @name itself when there is no such group,
// which the engine refuses with what there is.
func (fl *installFlow) browse(name string) ([]string, error) {
	groups, _, err := fl.eng.MarketGroups(outputFlags(fl.cmd))
	if err != nil {
		return nil, err
	}
	k := slices.IndexFunc(groups, func(g engine.MarketGroup) bool { return g.Name == name })
	if k < 0 || len(groups[k].Entries) == 0 {
		return []string{"@" + name}, nil
	}
	step := ModuleStep{Markets: []ModuleGroup{offerGroup(groups[k], func(engine.MarketOffer) bool { return true })}, Browse: true}
	for {
		ans, err := fl.p.Modules(step)
		if err != nil {
			return nil, err
		}
		if len(ans.Names) > 0 {
			return ans.Names, nil
		}
		step.Problem = "pick at least one module"
	}
}

// offerGroup is g as a group of the module picker (Plan 6c §6.5), titled "@acme — <description>",
// with the entries show keeps.
func offerGroup(g engine.MarketGroup, show func(engine.MarketOffer) bool) ModuleGroup {
	mg := ModuleGroup{Title: "@" + g.Name}
	if g.Description != "" {
		mg.Title += " — " + g.Description
	}
	for _, o := range g.Entries {
		if show(o) {
			mg.Options = append(mg.Options, ModuleOption{Name: o.Install, Label: offerLabel(o.Name, o)})
		}
	}
	return mg
}

// offerLabel is how a picker shows the entry o under name: "name version — description [tags]
// (host)", each part as safe to print as o's are.
func offerLabel(name string, o engine.MarketOffer) string {
	label := name
	if o.Version != "" {
		label += " " + o.Version
	}
	if o.Description != "" {
		label += " — " + o.Description
	}
	if len(o.Tags) > 0 {
		label += " [" + strings.Join(o.Tags, ", ") + "]"
	}
	if o.Host != "" {
		label += " (" + o.Host + ")"
	}
	return label
}

// missingArgument is install's refusal off a terminal without an argument; when the scope the
// install would use lists modules lock.json does not record, it hints the sync that installs
// them (Plan 6a §10.2).
func (fl *installFlow) missingArgument() error {
	err := fl.guide.args(1)(fl.cmd, fl.args)
	req, rerr := fl.a.scopeRequest(fl.cwd, false)
	if rerr != nil {
		return err
	}
	req.Command, req.Both = "install", true
	res, rerr := fl.eng.ResolveScope(req)
	if rerr != nil {
		return err
	}
	var hints []string
	for _, c := range res.Scopes {
		_, hint, cerr := fl.eng.InstallCoaching(fl.cmd.Context(), c.Scope)
		if cerr != nil {
			return cerr
		}
		if hint != "" {
			hints = append(hints, hint)
		}
	}
	return errs.WithHints(err, hints...)
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
		if err := fl.coach(); err != nil {
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
		// --force confirms the plan, as -y does (user decision, 2026-10-22).
		if fl.f.yes || fl.f.force {
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
	spec.Options.Receipt, spec.Options.Rerun = fl.a.receipt(fl.cmd, fl.f.trust, fl.f.yes), trustRun(fl.cmd, fl.args)
	fl.a.hookOptions(fl.cmd, &spec.Options)
	spec.Options.Display = outputFlags(fl.cmd)
	// The summary, when a picker ran, already showed where each address resolved, before Apply.
	if !fl.a.json && (!fl.ask || len(fl.shown) == 0) {
		s := fl.a.style(fl.cmd)
		spec.Options.ShowResolved = func(lines []string) {
			for _, l := range lines {
				fmt.Fprintln(fl.cmd.ErrOrStderr(), s.info(l))
			}
		}
	}
	res, err := fl.eng.InstallAll(fl.cmd.Context(), spec)
	if renamed, ok := errors.AsType[*engine.RenamedEntryError](err); ok && renamed.Source != nil {
		if run, ok := fl.bySource(renamed); ok {
			err = errs.WithHints(renamed, run)
		}
	}
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

// bySource is the run with the address of renamed replaced by its source, every flag kept but
// --ref and --path, which the catalog's take the place of: what installs the module under its own
// name (Plan 6c §6.3). When the source takes --ref or --path, which apply to a single source,
// and the run has other arguments, it installs the renamed module alone. ok is false when the
// address is no argument of the run (a picker's pick), and the engine's hint stands.
func (fl *installFlow) bySource(renamed *engine.RenamedEntryError) (string, bool) {
	k := slices.Index(fl.args, "@"+renamed.Market+"/"+renamed.Entry)
	if k < 0 {
		return "", false
	}
	args := slices.Clone(fl.args)
	args[k] = renamed.Source[0]
	if len(renamed.Source) > 1 && len(args) > 1 {
		args = []string{renamed.Source[0]}
	}
	tail := []string{}
	for _, a := range renamed.Source[1:] {
		tail = append(tail, untrusted.Text(a, untrusted.MaxName))
	}
	return strings.TrimSpace(runWithout(fl.cmd, args, "ref", "path") + " " + errs.ShellJoin(tail)), true
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
	req, err := fl.a.scopeRequest(fl.cwd, fl.ask)
	if err != nil {
		return err
	}
	req.Command, req.Both, req.Run = "install", true, runWithout(fl.cmd, fl.args, slices.Concat(scopeFlags, consentFlags)...)
	res, err := fl.eng.ResolveScope(req)
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
	entries, scopes, err := fl.picker.pick(res, ScopeStep{Question: "Install where?", Preselect: fl.prevScopes})
	if err != nil {
		return err
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

// coach prints, once per scope and before the module picker, how many modules the scope's
// manifest lists that lock.json does not record, and the sync that installs them (Plan 6a
// §10.2).
func (fl *installFlow) coach() error {
	if !fl.ask || len(fl.args) > 0 {
		return nil
	}
	s := fl.a.style(fl.cmd)
	for _, c := range fl.scopes {
		notice, _, err := fl.eng.InstallCoaching(fl.cmd.Context(), c.Scope)
		if err != nil {
			return err
		}
		if notice != "" && !fl.warned[notice] {
			fl.warned[notice] = true
			fmt.Fprintln(fl.cmd.ErrOrStderr(), s.warn(notice))
		}
	}
	return nil
}

// ensureChoices computes what the pickers offer; off a terminal nothing is offered.
func (fl *installFlow) ensureChoices() error {
	if !fl.ask || fl.choices != nil {
		return nil
	}
	ch, err := fl.eng.InstallChoices(fl.cmd.Context(), engine.ChoicesRequest{Cwd: fl.cwd, Scopes: fl.scopes, Modules: fl.requests(), DryRun: fl.f.dry,
		Display: outputFlags(fl.cmd)})
	fl.choices = ch
	if err != nil {
		return err
	}
	s := fl.a.style(fl.cmd)
	for _, w := range ch.Warnings {
		if !fl.warned[w] {
			fl.warned[w] = true
			fmt.Fprintln(fl.cmd.ErrOrStderr(), s.warn(w))
		}
	}
	return nil
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
	for i, addr := range fl.marketPicks {
		r := engine.InstallRequest{Arg: addr}
		if i < len(fl.marketPins) {
			r.Version = &fl.marketPins[i]
		}
		out = append(out, r)
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
	var keys []string // what the picker selects: a module picked from a marketplace by its address
	for _, name := range fl.prevMods {
		keys = append(keys, cmp.Or(fl.marketNames[name], name))
	}
	step := ModuleStep{Options: fl.moduleOptions(), Markets: fl.marketGroups(), Preselect: keys}
	for {
		ans, err := fl.p.Modules(step)
		if err != nil {
			return err
		}
		if ans.AddSource && ans.Source == "" {
			step = ModuleStep{Options: fl.moduleOptions(), Markets: fl.marketGroups(), Preselect: ans.Names}
			continue
		}
		if _, isAddr, _ := market.ParseAddress(ans.Source); ans.AddSource && isAddr {
			// An entry of a marketplace is picked in its group, and fetched once the picks are made.
			fl.shownAddrs = append(fl.shownAddrs, ans.Source)
			step = ModuleStep{Options: fl.moduleOptions(), Markets: fl.marketGroups(), Preselect: union(slices.Clone(ans.Names), []string{ans.Source})}
			if !fl.offers(ans.Source) {
				step.Problem = untrusted.Text(ans.Source, untrusted.MaxName) + " is no entry your marketplaces offer; lola market lists them"
			}
			continue
		}
		if ans.AddSource {
			v, err := fl.eng.AddSource(fl.cmd.Context(), fl.cwd, ans.Source, ans.Ref, "", outputFlags(fl.cmd))
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
			step = ModuleStep{Options: fl.moduleOptions(), Markets: fl.marketGroups(), Preselect: union(slices.Clone(ans.Names), []string{v.Name})}
			continue
		}
		if len(ans.Names) == 0 {
			step.Problem = "pick at least one module"
			continue
		}
		// A catalog entry is cached under its entry name (a renamed module is refused), so two
		// picks of one name are refused before anything is fetched.
		var names, addrs []string
		picked, problem := map[string]string{}, ""
		for _, n := range ans.Names {
			name := n
			if strings.HasPrefix(n, "@") {
				addrs = append(addrs, n)
				_, name, _ = strings.Cut(n, "/")
			} else {
				names = append(names, n)
			}
			switch first, ok := picked[name]; {
			case !ok:
				picked[name] = n
			case problem == "":
				problem = untrusted.Text(first, untrusted.MaxName) + " and " + untrusted.Text(n, untrusted.MaxName) + " are both the module " +
					untrusted.Text(name, untrusted.MaxName) + "; lola installs one module of a name: pick one"
			}
		}
		if problem != "" {
			step = ModuleStep{Options: fl.moduleOptions(), Markets: fl.marketGroups(), Preselect: ans.Names, Problem: problem}
			continue
		}
		var pins []engine.VersionRef
		for _, name := range names {
			if m := fl.choices.Module(name); m != nil {
				pins = append(pins, engine.VersionRef{Name: name, ID: m.ID})
			}
		}
		if err := fl.pickMarkets(pins, addrs); err != nil {
			if fl.choices == nil {
				return err // what was listed before cannot be listed again either
			}
			writeError(fl.cmd.ErrOrStderr(), err, false)
			step = ModuleStep{Options: fl.moduleOptions(), Markets: fl.marketGroups(), Preselect: ans.Names,
				Problem: "could not fetch what you picked from your marketplaces; pick again"}
			continue
		}
		fl.shown[ReviewModules] = true
		fl.modules = names
		for _, addr := range fl.marketPicks {
			for name, a := range fl.marketNames {
				if a == addr {
					fl.modules = append(fl.modules, name)
				}
			}
		}
		fl.dropRemovedPicks()
		return nil
	}
}

// pickMarkets makes pins the versions of the modules picked from the cache and addrs the
// marketplace entries picked (Plan 6c §6.5): when addrs changed, the pickers' choices are computed
// again with them, which fetches each as the flow's Add a source… does, so the component picker
// lists what they install. A pick that cannot be resolved or fetched leaves every pick as it was,
// listed again; when even that fails, the choices stay nil, and the caller stops.
func (fl *installFlow) pickMarkets(pins []engine.VersionRef, addrs []string) error {
	if slices.Equal(addrs, fl.marketPicks) {
		fl.cachePins = pins
		return nil
	}
	wasCache, was, wasPins, wasNames := fl.cachePins, fl.marketPicks, fl.marketPins, fl.marketNames
	fl.cachePins, fl.marketPicks, fl.marketPins, fl.marketNames, fl.choices = pins, addrs, nil, map[string]string{}, nil
	if err := fl.ensureChoices(); err != nil {
		fl.cachePins, fl.marketPicks, fl.marketPins, fl.marketNames, fl.choices = wasCache, was, wasPins, wasNames, nil
		return errors.Join(err, fl.ensureChoices())
	}
	// Choices.Versions follow the requests, whatever their order.
	reqs := fl.requests()
	for _, addr := range addrs {
		v := fl.choices.Versions[slices.IndexFunc(reqs, func(r engine.InstallRequest) bool { return r.Arg == addr })]
		fl.marketPins = append(fl.marketPins, v)
		fl.marketNames[v.Name] = addr
	}
	return nil
}

// offers reports whether addr is an entry a marketplace group of the choices offers.
func (fl *installFlow) offers(addr string) bool {
	for _, g := range fl.choices.Markets {
		for _, o := range g.Entries {
			if o.Install == addr {
				return true
			}
		}
	}
	return false
}

// marketGroups labels each marketplace with a fetched catalog as a group of the module picker
// (Plan 6c §6.5): "@acme — <description>", each entry "name version — description [tags]
// (host)". An entry the cache holds from the same source is offered in the cache's group
// instead, unless it was picked here or added with Add a source….
func (fl *installFlow) marketGroups() []ModuleGroup {
	var out []ModuleGroup
	for _, g := range fl.choices.Markets {
		mg := offerGroup(g, func(o engine.MarketOffer) bool {
			return !o.Cached || slices.Contains(fl.marketPicks, o.Install) || slices.Contains(fl.shownAddrs, o.Install)
		})
		if len(mg.Options) > 0 {
			out = append(out, mg)
		}
	}
	return out
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
		if _, picked := fl.marketNames[m.Name]; picked {
			continue // offered in its marketplace's group, as picked
		}
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
// assistant installed, else the picker (once, for every scope whose targets are open). On a
// terminal, top-level targets the trust record does not cover fix nothing: the scope is open,
// and the picker never preselects them (Plan 6a §10.1).
func (fl *installFlow) ensureAssistants() error {
	if fl.targets != nil {
		return nil
	}
	delete(fl.shown, ReviewAssistants)
	answer := map[string][]string{}
	var open, openLabels []string
	var installed, here, untrustedTargets []string
	// The run with -t naming one assistant, for each of ids.
	withTarget := func(ids []string) []string {
		var out []string
		for _, id := range ids {
			out = append(out, runWithout(fl.cmd, fl.args, consentFlags...)+" -t "+id)
		}
		return out
	}
	for _, c := range fl.scopes {
		name := c.Scope.Key()
		if len(fl.f.targets) > 0 {
			answer[name] = fl.f.targets
			continue
		}
		man, err := fl.eng.InstallManifest(c.Scope, fl.requests())
		if err != nil {
			return fl.eng.CoachUnresolved(err, []engine.Scope{c.Scope}, fl.f.dry, outputFlags(fl.cmd))
		}
		fixed := man.Targets
		if fl.choices != nil {
			for _, s := range fl.choices.Scopes {
				if s.Scope.Key() == name {
					fixed, untrustedTargets = s.FixedTargets, union(untrustedTargets, s.UntrustedTargets)
				}
			}
		}
		if len(fixed) > 0 {
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
				withTarget(targets.IDs())...)
		case !fl.ask && len(marked) == 0:
			return errs.WithHints(fmt.Errorf("none of your installed assistants (%s) is set up here, so lola does not know where to install",
				strings.Join(inst, ", ")), withTarget(inst)...)
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
		pre = slices.DeleteFunc(slices.Clone(pre), func(id string) bool { return slices.Contains(untrustedTargets, id) })
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
	spec := engine.InstallSpec{Cwd: fl.cwd, Options: engine.Options{Targets: fl.f.targets, DryRun: fl.f.dry, Force: fl.f.force,
		Strict: fl.f.strict, ForceRun: forceRun(fl.cmd, fl.args), Trust: fl.f.trust}, UserRun: runWithout(fl.cmd, fl.args, slices.Concat(scopeFlags, consentFlags)...) + " --user"}
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
			if addr, ok := fl.marketNames[name]; ok {
				req = engine.InstallRequest{Arg: addr, Version: &fl.marketPins[slices.Index(fl.marketPicks, addr)]}
			} else if m := fl.choices.Module(name); m != nil {
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
