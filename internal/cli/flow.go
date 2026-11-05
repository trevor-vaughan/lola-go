package cli

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// installFlags are lola install's flags.
type installFlags struct {
	targets, skips, onlys, unskips                                       []string
	ref, path                                                            string
	preInstall, postInstall                                              string // --pre-install and --post-install, as given
	profile                                                              string // --profile: the write target (engine.TargetRequest)
	dry, force, strict, rules, noRules, global, yes, exact, withMentions bool
	override                                                             bool // --override (engine.TargetRequest)
	trust                                                                engine.TrustKinds
	at                                                                   []string          // lola install's --at, as given
	pinned                                                               map[string]string // --at parsed (parseAt)
	// edit, when not nil, says the run is lola profile <edit> --edit <module>... (spec 2026-10-29
	// §6.2): edit holds the profile's name as typed ([] for the one edits go to), which every hint
	// repeats before the modules; create is its --new; scopes, where it applies, which the run
	// resolved without asking (engine.EditScopes); catalog, the names your catalogs list
	// (engine.CatalogNames).
	edit    []string
	create  bool
	scopes  []engine.ScopeChoice
	catalog []string
	// catalogPartial says lola could not read every catalog (engine.TargetRequest.CatalogPartial).
	catalogPartial bool
	// keep, when not nil, says the run is lola profile <name> --edit's module page (spec 2026-10-29
	// §6.2; cherry-pick design §4.3): the profile's modules, preselected, a module unticked there
	// being removed from it once asked; keepLabel names the profile ("profile go-review").
	keep      []string
	keepLabel string
	// keepAdd is, for each of keep, the lola cache add run of its entry's source
	// (engine.EditTarget.CacheAdd), which coaches a module the page cannot pick for want of it.
	keepAdd map[string]string
}

// addedSource is a source added to the cache during the run (Add a source…).
type addedSource struct{ source, ref string }

// installFlow is one run of lola install (Plan 5c §3, §4). It fills every answer the flags
// leave open, asking the Prompter on a terminal, ends with the review whenever lola chose
// something the user did not type (Plan 10 §5), and turns the answers into one
// engine.InstallSpec.
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
	staged                      *engine.StagedRun   // what the run fetched, out of the cache until Apply commits it (cherry-pick design §7)
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
	why                         map[string]string              // destination (Scope.Key) → why its assistants were chosen, which the review says
	done                        int                            // fields the pickers asked in this pass, for the next step's number
	auto                        bool                           // lola chose an assistant the user did not name: the only one installed
	picks                       map[string]map[string][]string // destination (Scope.Key) → module → selected kind/name
	prevPicks                   map[string]map[string][]string // the picks before Change components, preselected
	added                       []addedSource
	droppedModule, droppedScope int                                                    // component picks dropped because their module or destination was removed; reset after each summary
	asked                       map[string]bool                                        // modules whose picks came from a component page
	pageLog                     []answeredPage                                         // the component pages answered, in the order asked, for Esc
	asking                      string                                                 // the module whose component page is asked
	shown                       map[string]bool                                        // steps whose picker ran
	warned                      map[string]bool                                        // choice warnings printed, each once per run
	where                       func(engine.WhereQuestion) (engine.WhereChoice, error) // the run's AskWhere (askWhere), nil off a terminal
	answers                     map[whereKey]engine.WhereChoice                        // the where-questions answered
	overrides                   map[string]string                                      // module → the file an override of its inherited entry writes, as its page names it (targetOf)
	dropShown                   bool                                                   // the review showed the removal of what lola profile --edit's page unticked
}

// whereKey is the destination (Scope.Key) and module ("" for the profile on's) a where-question
// was about.
type whereKey struct{ scope, module string }

// answeredPage is a component page the user answered: its module, and the destinations (Scope.Key)
// it asked.
type answeredPage struct {
	module string
	keys   []string
}

// runInstall runs the install flow.
func (a *app) runInstall(cmd *cobra.Command, g argGuide, args []string, f installFlags) (err error) {
	if (f.ref != "" || f.path != "") && len(args) > 1 {
		return errors.New("--ref and --path apply to a single source")
	}
	if f.path != "" && len(args) == 1 {
		if _, ok, _ := market.ParseAddress(args[0]); ok {
			return errs.WithHints(fmt.Errorf("--path does not apply to %s: its catalog gives the path", untrusted.Text(args[0], untrusted.MaxName)),
				runWithout(cmd, append(slices.Clone(f.edit), args...), "path"))
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
	edits, err := selectionEdits(f.skips, f.onlys, f.unskips, f.exact)
	if err != nil {
		return err
	}
	if f.exact && len(f.onlys) == 0 {
		run := runWithout(cmd, append(slices.Clone(f.edit), args...), "exact")
		if len(edits) == 0 {
			// --exact asks for an only: list; with no pick, the run without it takes the whole module,
			// a no-op where a layer gives it already, so no hint would do what was meant.
			if f.edit == nil && len(args) == 0 { // the example names the module an install needs
				run = cmd.CommandPath() + " <module>" + strings.TrimPrefix(run, cmd.CommandPath())
			}
			return fmt.Errorf("--exact applies to --only, and none was given: name the components to keep, as in %s --only <kind>/<name> --exact", run)
		}
		return errs.WithHints(errors.New("--exact applies to --only"), run)
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
	fl.answers, fl.overrides = map[whereKey]engine.WhereChoice{}, map[string]string{}
	fl.where = a.askWhere(p, fl.answers)
	if err := fl.expandMarkets(); err != nil {
		return err
	}
	// What the run fetches stays out of the cache until Apply (cherry-pick design §7). The run is
	// closed on every path, which drops whatever Apply did not commit.
	if fl.staged, err = eng.StageInstall(); err != nil {
		return err
	}
	defer func() {
		if cerr := fl.staged.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
	}()
	return coachSource(strings.Fields(cmd.CommandPath()), cmd, fl.hintArgs(args), fl.run())
}

// hintArgs is args, module arguments of the run, as a hint repeating the run gives them: after the
// profile's name for lola profile <name> --edit.
func (fl *installFlow) hintArgs(args []string) []string {
	return append(slices.Clone(fl.f.edit), args...)
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
		return errs.WithHints(errors.New("--path does not apply to a marketplace entry: its catalog gives the path"), runWithout(fl.cmd, fl.hintArgs(out), "path"))
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
		hints = append(hints, runWithout(fl.cmd, fl.hintArgs(run), "path"))
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
	ans, err := fl.p.Modules(ModuleStep{Markets: []ModuleGroup{offerGroup(groups[k], func(engine.MarketOffer) bool { return true })}, Browse: true})
	if err != nil {
		return nil, err
	}
	return ans.Names, nil
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
		fl.done = 0 // each pass numbers the steps it asks (Plan 10 §5)
		if stage, err := fl.answer(); err != nil {
			if errors.Is(err, ErrBack) && fl.back(stage) {
				continue
			}
			return err
		}
		var err error
		if spec, err = fl.spec(); err != nil {
			switch {
			case !errors.Is(err, ErrAborted) || fl.f.keep == nil:
			case fl.f.create: // a new profile, nothing picked for it: made empty
				return fl.a.makeProfile(fl.cmd, fl.eng, engine.MakeRequest{Scope: fl.scopes[0].Scope, Name: fl.f.profile, Target: fl.target(),
					Options: engine.Options{DryRun: fl.f.dry, Display: outputFlags(fl.cmd)}})
			case len(fl.dropped()) > 0: // the page only unticked: nothing to install
				gone, err := fl.askDrop(fl.dropped())
				if err != nil {
					return err
				}
				return fl.removeDropped(nil, gone)
			default: // only modules your cache lacks were kept, or none was picked: nothing changes
				return fl.unchanged()
			}
			return err
		}
		if !fl.reviews() {
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
		if !changed && fl.f.keep != nil && len(fl.dropped()) > 0 { // what was kept is as it was: only the removal is left
			gone, err := fl.askDrop(fl.dropped())
			if err != nil {
				return err
			}
			return fl.removeDropped(nil, gone)
		}
		if !changed {
			s := fl.a.style(fl.cmd)
			said := false
			for _, name := range fl.modules { // an override answered, which writes nothing without a change, says so
				if to, ok := fl.overrides[name]; ok {
					fmt.Fprintln(fl.cmd.OutOrStdout(), s.info("nothing changed: "+untrusted.Text(name, untrusted.MaxName)+"'s picks are as they were, so "+to+
						" gets no entry of its own (an override is written only with a change)"))
					said = true
				}
			}
			if !said {
				fmt.Fprintln(fl.cmd.OutOrStdout(), s.ok("already up to date"))
			}
			return nil
		}
		// The review shows the findings a dry run of exactly this install reports (Plan 10 §5):
		// it asks nothing and writes nothing.
		dry := spec
		dry.Options.DryRun = true
		dry.Options.Rerun, dry.Options.Display = consentRun(fl.cmd, fl.hintArgs(fl.args)), outputFlags(fl.cmd)
		planned, err := fl.eng.InstallAll(fl.cmd.Context(), dry)
		if err != nil {
			return err
		}
		var summary strings.Builder
		fl.printSummary(&summary, spec, pv, planned.Findings)
		printRemovals(&summary, style{}, engine.PickDeletes(spec, planned)) // what Apply would delete is shown
		fl.droppedModule, fl.droppedScope = 0, 0
		if gone := fl.dropped(); fl.f.keep != nil && len(gone) > 0 { // the review lists the removal it leads to
			plan, err := fl.dropPlan(gone)
			if err != nil {
				return err
			}
			summary.WriteString(plan)
			fl.dropShown = true
		}
		// --force confirms the plan, as -y does (user decision, 2026-10-22).
		if fl.f.yes || fl.f.force {
			fmt.Fprint(fl.cmd.ErrOrStderr(), summary.String())
			break
		}
		choice, err := fl.p.Review(ReviewStep{Summary: summary.String(), Options: fl.reviewOptions(), Step: Steps{At: fl.done + 1, Of: fl.done + 1},
			Nav: fl.nav(ReviewApply)})
		if errors.Is(err, ErrBack) && fl.back(ReviewApply) {
			continue
		}
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
	gone, err := fl.askDrop(fl.dropped())
	if err != nil {
		return err
	}
	spec.Options.Receipt, spec.Options.Rerun = fl.a.receipt(fl.cmd, fl.f.trust, fl.f.yes), consentRun(fl.cmd, fl.hintArgs(fl.args))
	if fl.cmd.Name() == "install" { // lola install takes --at; lola profile --edit does not
		spec.Options.PinConsent, spec.At = true, fl.f.pinned
		spec.AtFresh = runWithout(fl.cmd, fl.hintArgs(fl.args), "at", "dry-run") + " -n"
	}
	fl.a.hookOptions(fl.cmd, &spec.Options)
	// The review, when there was one, already showed where each address resolved, before Apply.
	if !fl.a.json && !fl.reviews() {
		s := fl.a.style(fl.cmd)
		spec.Options.ShowResolved = func(lines []string) {
			for _, l := range lines {
				fmt.Fprintln(fl.cmd.ErrOrStderr(), s.info(l))
			}
		}
	}
	res, err := fl.eng.InstallAll(fl.cmd.Context(), spec)
	if err != nil {
		err = fl.a.deletesRefusal(fl.cmd, err)
	}
	if errors.Is(err, engine.ErrDeclined) {
		return ErrAborted
	}
	if renamed, ok := errors.AsType[*engine.RenamedEntryError](err); ok && renamed.Source != nil {
		if run, ok := fl.bySource(renamed); ok {
			err = errs.WithHints(renamed, run)
		}
	}
	if err != nil {
		return err
	}
	// A profile the install wrote has its write line; selections.yaml has none.
	if saved := savedTo(res.Result); saved != "" && !slices.ContainsFunc(res.Edits, func(ed engine.Edit) bool { return ed.Path == untrusted.Text(saved, len(saved)) }) {
		fmt.Fprintln(fl.cmd.ErrOrStderr(), "→ saved to "+untrusted.Text(saved, untrusted.MaxName))
	}
	if fl.f.keep != nil {
		return fl.removeDropped(res, gone)
	}
	eq := []string{}
	if fl.reviews() {
		eq = fl.equivalents(spec)
	}
	return fl.a.emit(fl.cmd, func(w io.Writer, s style) { printInstall(w, s, res) }, installOutput{InstallResult: res, EquivalentCommands: eq})
}

// dropped is what lola profile --edit's page unticked: the profile's modules no longer picked.
func (fl *installFlow) dropped() []string {
	var out []string
	for _, m := range fl.f.keep {
		if !slices.Contains(fl.modules, m) {
			out = append(out, m)
		}
	}
	return out
}

// askDrop asks once whether to remove gone, the modules unticked, from the profile (cherry-pick
// design §4.3), after showing what the removal does (dropPlan) unless the review did; -y and
// --force answer yes, and No keeps them (none is removed).
func (fl *installFlow) askDrop(gone []string) ([]string, error) {
	if len(gone) == 0 {
		return gone, nil
	}
	if !fl.dropShown {
		plan, err := fl.dropPlan(gone)
		if err != nil {
			return nil, err
		}
		fmt.Fprint(fl.cmd.ErrOrStderr(), plan)
	}
	if fl.f.yes || fl.f.force {
		return gone, nil
	}
	ok, err := fl.p.Confirm("Remove "+untrusted.Text(strings.Join(gone, ", "), untrusted.MaxName)+" from "+fl.f.keepLabel+"?", Nav{})
	if err != nil || !ok {
		return nil, err
	}
	return gone, nil
}

// dropPlan is what removing gone, the modules unticked, from the profile does, for the review and
// the question: lola uninstall --profile's plan at the run's first scope (the entry, and the files
// it deletes where the profile is on), then each later scope the profile is on, which applies it
// after, asking what it removes there.
func (fl *installFlow) dropPlan(gone []string) (string, error) {
	sc := fl.scopes[0].Scope
	preview, err := fl.eng.Uninstall(fl.cmd.Context(), sc, gone, engine.Options{DryRun: true, Force: fl.f.force, Display: outputFlags(fl.cmd),
		ForceRun: forceRun(fl.cmd, fl.hintArgs(nil)), Target: fl.target(), AskWhere: fl.where, Then: choiceScopes(fl.scopes[1:])})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintln(&b, "Remove "+untrusted.Text(strings.Join(gone, ", "), untrusted.MaxName)+" from "+fl.f.keepLabel+", as you unticked "+destWhere(sc)+":")
	printResult(&b, style{}, "", preview)
	for _, c := range fl.scopes[1:] {
		fmt.Fprintln(&b, "then apply the removal "+destWhere(c.Scope)+", where "+fl.f.keepLabel+" is on too, asking what it removes there")
	}
	return b.String(), nil
}

// unchanged ends lola profile --edit's page when it changes nothing: what was kept is as it was
// and nothing was unticked. A module kept that your cache lacks is named, with the lola cache add
// that puts it there, so its components can be picked on the page.
func (fl *installFlow) unchanged() error {
	var uncached, runs []string
	for _, m := range fl.f.keep {
		if slices.Contains(fl.modules, m) && fl.choices.Module(m) == nil {
			uncached = append(uncached, m)
			if run := fl.f.keepAdd[m]; run != "" {
				runs = append(runs, run)
			}
		}
	}
	notes := []string{fl.f.keepLabel + " is unchanged"}
	if len(uncached) > 0 {
		notes[0] += ": " + untrusted.Text(strings.Join(uncached, ", "), untrusted.MaxName) + " is not in your cache, so its entry stays as it is"
		for _, run := range runs {
			notes = append(notes, "to pick its components, add it to your cache, then edit again: "+run)
		}
	}
	return fl.a.emit(fl.cmd, func(w io.Writer, s style) {
		for _, n := range notes {
			fmt.Fprintln(w, s.info(n))
		}
	}, map[string]any{"notices": notes})
}

// destWhere names a scope as a sentence does: "at user scope", or "in project (<root>)".
func destWhere(sc engine.Scope) string {
	if sc.ProjectDir == "" {
		return "at user scope"
	}
	return "in " + destLabel(sc)
}

// removeDropped ends lola profile --edit's page: gone, the modules unticked and confirmed, are
// removed from the profile at the run's first scope as lola uninstall --profile removes them (the
// entries, and their files where it is on), after res, the install of what was picked (nil when
// none was); each later scope the profile is on then applies the file as lola sync does, its
// removals asked (-y and --force answer yes), and under -n is named as one the run would apply. Everything is
// printed, a module unticked and kept said kept. A removal that fails after the install says what
// the install did and hints the removal; a later scope that fails says what was written and hints
// its sync.
func (fl *installFlow) removeDropped(res *engine.InstallResult, gone []string) error {
	var removed *engine.Result
	also, notes := []engine.ScopeSync{}, []string{}
	sc, label := fl.scopes[0].Scope, untrusted.Text(strings.Join(gone, ", "), untrusted.MaxName)
	if kept := fl.dropped(); len(gone) == 0 && len(kept) > 0 {
		notes = append(notes, "kept "+untrusted.Text(strings.Join(kept, ", "), untrusted.MaxName)+" in "+fl.f.keepLabel)
	}
	if len(gone) > 0 {
		var err error
		removed, err = fl.eng.Uninstall(fl.cmd.Context(), sc, gone, engine.Options{DryRun: fl.f.dry, Force: fl.f.force, Display: outputFlags(fl.cmd),
			ForceRun: forceRun(fl.cmd, fl.hintArgs(nil)), Target: fl.target(), AskWhere: fl.where, Then: choiceScopes(fl.scopes[1:])})
		if err != nil && res != nil {
			return errs.WithHints(fmt.Errorf("the edit added what you picked, but did not remove %s: %w", label, err),
				"remove them: "+fl.eng.Command(sc, "uninstall", append(slices.Clone(gone), "--profile", fl.f.profile)...))
		}
		if err != nil {
			return err
		}
		var asked bool
		_, _, confirm := fl.a.syncQuestions(fl.cmd, syncFlags{yes: fl.f.yes}, false, "Remove these there too? No leaves that scope as it was", nil, &asked)
		for _, c := range fl.scopes[1:] {
			if fl.f.dry { // nothing was written for that scope's sync to read
				notes = append(notes, "would then apply the removal "+destWhere(c.Scope)+", where "+fl.f.keepLabel+" is on too")
				continue
			}
			r, err := fl.eng.Sync(fl.cmd.Context(), engine.SyncRequest{Scope: c.Scope, ConfirmRemovals: confirm, Display: outputFlags(fl.cmd),
				Options: engine.Options{Force: fl.f.force, ForceRun: forceRun(fl.cmd, fl.hintArgs(nil)), Trust: fl.f.trust, Display: outputFlags(fl.cmd)}})
			if refusal, ok := errors.AsType[*engine.TrustRefusalError](err); ok && !fl.a.json {
				printSyncRefusal(fl.cmd.ErrOrStderr(), style{color: fl.a.color(fl.cmd.ErrOrStderr()), plain: fl.a.plain}, manifestName(c.Scope), "lola sync", refusal, "")
			}
			if errors.Is(err, engine.ErrDeclined) || errors.Is(err, ErrAborted) {
				err = errors.New("you declined its removals there")
			}
			if err != nil {
				return errs.WithHints(fmt.Errorf("%w; %s was removed from %s and applied %s, not %s", err, label, fl.f.keepLabel, destWhere(sc), destWhere(c.Scope)),
					"apply it there: "+fl.eng.SyncCommand(c.Scope))
			}
			also = append(also, engine.ScopeSync{Scope: c.Scope, SyncResult: r})
		}
	}
	return fl.a.emit(fl.cmd, func(w io.Writer, s style) {
		if res != nil {
			printInstall(w, s, res)
		}
		if removed != nil {
			printResult(w, s, "uninstalled", removed)
		}
		for _, a := range also {
			fmt.Fprintln(w, destLabel(a.Scope)+":")
			printSync(w, s, manifestName(a.Scope), a.SyncResult, nil, "")
		}
		for _, n := range notes {
			fmt.Fprintln(w, s.info(n))
		}
	}, map[string]any{"install": res, "uninstall": removed, "also": also, "notices": notes})
}

// answer fills every answer the pass lacks, in the flow's order, and returns the stage whose
// question failed with the error.
func (fl *installFlow) answer() (string, error) {
	if err := fl.ensureScopes(); err != nil {
		return ReviewScope, err
	}
	if err := fl.coach(); err != nil {
		return ReviewScope, err
	}
	if err := fl.ensureChoices(); err != nil {
		return ReviewScope, err
	}
	if err := fl.ensureModules(); err != nil {
		return ReviewModules, err
	}
	if err := fl.ensureAssistants(); err != nil {
		return ReviewAssistants, err
	}
	return ReviewComponents, fl.ensureComponents()
}

// nav places the question asked at stage (ReviewScope, …, ReviewApply for the review) in the run
// for lola's keys: whether there is a screen before it to go back to, and how many answers the
// run holds from the screens the user answered.
func (fl *installFlow) nav(stage string) Nav {
	before, _ := fl.before(stage)
	return Nav{Back: before != "", Answered: fl.held()}
}

// held counts the answers the run holds from screens the user answered: the scope, module and
// assistant pickers' and each component page's.
func (fl *installFlow) held() int {
	n := 0
	for _, s := range []string{ReviewScope, ReviewModules, ReviewAssistants} {
		if fl.shown[s] {
			n++
		}
	}
	for _, pa := range fl.pageLog {
		if fl.holds(pa) {
			n++
		}
	}
	return n
}

// holds reports whether the run still holds the answer of the page pa: its module is still
// picked and its destinations' picks are kept.
func (fl *installFlow) holds(pa answeredPage) bool {
	if !slices.Contains(fl.modules, pa.module) {
		return false
	}
	for _, key := range pa.keys {
		if _, ok := fl.picks[key][pa.module]; !ok {
			return false
		}
	}
	return true
}

// before is the screen Esc at stage goes back to: on a component page (the one for fl.asking) or
// the review, the last page held that is asked before it, by its module's place and then the order
// asked, as its index in fl.pageLog; else the last picker before stage whose answer the run holds;
// "" when there is none.
func (fl *installFlow) before(stage string) (string, int) {
	if stage == ReviewComponents || stage == ReviewApply {
		limit := len(fl.modules)
		if stage == ReviewComponents {
			limit = slices.Index(fl.modules, fl.asking)
		}
		last, lastAt := -1, -1
		for i, pa := range fl.pageLog {
			if at := slices.Index(fl.modules, pa.module); at <= limit && at >= lastAt && fl.holds(pa) {
				last, lastAt = i, at
			}
		}
		if last >= 0 {
			return ReviewComponents, last
		}
	}
	order := []string{ReviewScope, ReviewModules, ReviewAssistants}
	end := len(order)
	if i := slices.Index(order, stage); i >= 0 {
		end = i
	}
	for i := end - 1; i >= 0; i-- {
		if fl.shown[order[i]] {
			return order[i], -1
		}
	}
	return "", -1
}

// back goes back from stage a screen (Esc): it forgets the answer of the screen before, kept as
// that screen's preselection, so the next pass asks it again and then carries on. It reports
// false when there is no screen before.
func (fl *installFlow) back(stage string) bool {
	step, k := fl.before(stage)
	switch {
	case step == "":
		return false
	case k >= 0:
		pa := fl.pageLog[k]
		fl.pageLog = slices.Delete(fl.pageLog, k, k+1)
		if fl.prevPicks == nil {
			fl.prevPicks = map[string]map[string][]string{}
		}
		for _, key := range pa.keys {
			if fl.prevPicks[key] == nil {
				fl.prevPicks[key] = map[string][]string{}
			}
			fl.prevPicks[key][pa.module] = fl.picks[key][pa.module]
			delete(fl.picks[key], pa.module)
		}
	default:
		_, err := fl.review(step) // a picker's step, which review always takes
		return err == nil
	}
	return true
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
	return strings.TrimSpace(runWithout(fl.cmd, fl.hintArgs(args), "ref", "path") + " " + errs.ShellJoin(tail)), true
}

// reviews reports whether the run ends with the review (Plan 10 §5): on a terminal, whenever lola
// chose something the user did not type, a picker's answer or an assistant taken as the only one
// installed.
func (fl *installFlow) reviews() bool { return fl.ask && (len(fl.shown) > 0 || fl.auto) }

// steps numbers the step the flow asks at stage (ReviewScope, …): after the fields this pass
// asked, of those and the fields the flow expects to ask from stage on (remaining).
func (fl *installFlow) steps(stage string) Steps {
	return Steps{At: fl.done + 1, Of: fl.done + fl.remaining(stage)}
}

// remaining forecasts how many fields the pass asks from stage on, the review included: the scope
// picker's one; the module picker's lists when no argument names the modules and they are not
// answered; the assistant picker's one when neither -t nor an answer gives them; the component
// pages still open, unless --skip, --only or --unskip answers them. A step is counted exactly
// once what decides it is known, and as one field (a component page per module argument) before:
// an assistant taken as the only one installed, or a module with one component, drops out of the
// count at the next step.
func (fl *installFlow) remaining(stage string) int {
	n := 1 // the review
	stages := []string{ReviewScope, ReviewModules, ReviewAssistants, ReviewComponents}
	for _, s := range stages[slices.Index(stages, stage):] {
		switch {
		case s == ReviewScope:
			n++
		case s == ReviewModules && fl.modules == nil && len(fl.args) == 0 && fl.choices != nil:
			n += moduleFields(ModuleStep{Markets: fl.marketGroups()})
		case s == ReviewModules && fl.modules == nil && len(fl.args) == 0:
			n++
		case s == ReviewAssistants && fl.targets == nil && len(fl.f.targets) == 0:
			n++
		case s == ReviewComponents && len(fl.edits) == 0:
			n += fl.componentFields()
		}
	}
	return n
}

// componentFields forecasts the lists the component pages still ask: exactly once the modules
// are picked, and one page per module argument (at least one) before.
func (fl *installFlow) componentFields() int {
	if fl.choices == nil || fl.modules == nil {
		return max(len(fl.args), 1)
	}
	n := 0
	for _, name := range fl.modules {
		if m := fl.choices.Module(name); m != nil {
			_, pages := fl.componentPages(m)
			for _, dests := range pages {
				n += fl.page(m, dests[0].Scope.Key(), name, nil).lists()
			}
		}
	}
	return n
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
		fl.prevPicks, fl.picks, fl.asked, fl.pageLog = fl.picks, nil, map[string]bool{}, nil
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

// ensureScopes resolves the scopes (§5.1), asking on a terminal when no flag gives them, then
// prints their context lines (spec 2026-10-29 §3.8).
func (fl *installFlow) ensureScopes() error {
	if fl.scopes != nil {
		return nil
	}
	if fl.f.scopes != nil { // lola profile --edit asks no scope
		fl.scopes = fl.f.scopes
		t := fl.target()
		fl.a.printContext(fl.cmd, fl.eng, choiceScopes(fl.scopes), &t)
		return nil
	}
	req, err := fl.a.scopeRequest(fl.cwd, fl.ask)
	if err != nil {
		return err
	}
	req.Command, req.Both, req.Run = "install", true, completeRun(fl.cmd, fl.args, scopeFlags...)
	res, err := fl.eng.ResolveScope(req)
	if err != nil {
		return err
	}
	fl.resolution = res
	delete(fl.shown, ReviewScope)
	if !res.Pick {
		fl.scopes = res.Scopes
		t := fl.target()
		fl.a.printContext(fl.cmd, fl.eng, choiceScopes(fl.scopes), &t)
		return nil
	}
	entries, scopes, err := fl.picker.pick(res, ScopeStep{Question: "Install where?", Preselect: fl.prevScopes, Step: fl.steps(ReviewScope),
		Nav: fl.nav(ReviewScope)})
	if err != nil {
		return err
	}
	fl.shown[ReviewScope], fl.prevScopes, fl.scopes = true, entries, scopes
	t := fl.target()
	fl.a.printContext(fl.cmd, fl.eng, choiceScopes(fl.scopes), &t)
	fl.done++
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
	req := engine.ChoicesRequest{Cwd: fl.cwd, Scopes: fl.scopes, Modules: fl.requests(), DryRun: fl.f.dry,
		Display: outputFlags(fl.cmd), Run: fl.staged, Target: fl.target()}
	if fl.f.edit != nil { // lola profile --edit asks which file it writes before any page (spec 2026-10-29 S2)
		req.AskWhere = fl.where
	}
	ch, err := fl.eng.InstallChoices(fl.cmd.Context(), req)
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
	if fl.prevMods == nil && fl.f.keep != nil {
		keys = fl.f.keep // the profile's own, ticked
	}
	step := ModuleStep{Options: fl.moduleOptions(), Markets: fl.marketGroups(), Preselect: keys}
	for {
		step.Step, step.Nav = fl.steps(ReviewModules), fl.nav(ReviewModules)
		if fl.f.keep != nil {
			step.Title, step.None = fl.f.keepLabel+" › modules", true
		}
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
		fl.done += moduleFields(step)
		fl.modules = append([]string{}, names...) // none is an answer on lola profile --edit's page
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

// moduleFields is how many lists the module picker shows for step: one per marketplace group, and
// the cached modules' unless it browses a marketplace alone.
func moduleFields(step ModuleStep) int {
	if step.Browse {
		return len(step.Markets)
	}
	return 1 + len(step.Markets)
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
// when installed in a chosen scope; on lola profile --edit's page, first the profile's modules
// your cache does not hold, so unticking one is a choice made in view.
func (fl *installFlow) moduleOptions() []ModuleOption {
	var out []ModuleOption
	for _, name := range fl.f.keep {
		if fl.choices.Module(name) == nil {
			out = append(out, ModuleOption{Name: name, Label: untrusted.Text(name, untrusted.MaxName) + " (in " + fl.f.keepLabel + "; not in your cache)"})
		}
	}
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
// manifest fixes; off a terminal, those set up here (TargetChoice.Here); on one, the targets with
// no binary (agents) set up here when no assistant is installed, the only assistant installed
// unless it is not set up here and agents is, else the picker (once, for every scope whose targets
// are open), which offers agents after the assistants, only where it is set up. On a
// terminal, top-level targets the trust record does not cover fix nothing: the scope is open,
// and the picker never preselects them (Plan 6a §10.1). It keeps why each scope's assistants were
// chosen, which the review says (Plan 10 §5).
func (fl *installFlow) ensureAssistants() error {
	if fl.targets != nil {
		return nil
	}
	delete(fl.shown, ReviewAssistants)
	answer, reasons, auto := map[string][]string{}, map[string]string{}, false
	var open, openLabels []string
	var installed, here, setUpOnly, untrustedTargets []string
	// The run with -t naming one assistant, for each of ids.
	withTarget := func(ids []string) []string {
		var out []string
		for _, id := range ids {
			out = append(out, completeRun(fl.cmd, fl.hintArgs(fl.args))+" -t "+id)
		}
		return out
	}
	for _, c := range fl.scopes {
		name := c.Scope.Key()
		if len(fl.f.targets) > 0 {
			answer[name], reasons[name] = fl.f.targets, "from -t"
			continue
		}
		choice, err := fl.eng.DefaultTargets(c.Scope, fl.requests(), fl.target())
		if err != nil {
			return fl.eng.CoachUnresolved(err, []engine.Scope{c.Scope}, fl.f.dry, outputFlags(fl.cmd))
		}
		fixed, from := choice.Manifest, choice.From
		if fl.choices != nil {
			for _, s := range fl.choices.Scopes {
				if s.Scope.Key() == name {
					fixed, from, untrustedTargets = s.FixedTargets, s.TargetsFrom, union(untrustedTargets, s.UntrustedTargets)
				}
			}
		}
		if len(fixed) > 0 {
			answer[name], reasons[name] = nil, "from "+cmp.Or(from, manifestName(c.Scope))
			continue
		}
		inst, marked, only := choice.Installed, choice.SetUp, choice.SetUpOnly
		choice.Manifest = nil // the pickers' fixed targets decided above
		switch why := choice.NoneHere(); {
		case len(inst) == 0 && len(only) == 0:
			return errs.WithHints(errors.New(why), withTarget(targets.IDs())...)
		case !fl.ask && why != "":
			return errs.WithHints(errors.New(why), withTarget(fl.eng.RerunTargets(choice))...)
		case !fl.ask:
			answer[name], reasons[name] = choice.Here(), "set up here"
		case len(inst) == 0:
			answer[name], reasons[name], auto = only, "set up here", true
		case len(inst) == 1 && (len(marked) == 1 || len(only) == 0):
			answer[name], reasons[name], auto = inst, "the only assistant found", true
		default:
			open, openLabels = append(open, name), append(openLabels, destLabel(c.Scope))
			installed, here, setUpOnly = union(installed, inst), union(here, marked), union(setUpOnly, only)
		}
	}
	if len(open) > 0 {
		pre := fl.prevTarget
		if pre == nil {
			pre = here
		}
		if len(pre) == 0 {
			pre = setUpOnly
		}
		if len(pre) == 0 {
			pre = installed
		}
		pre = slices.DeleteFunc(slices.Clone(pre), func(id string) bool { return slices.Contains(untrustedTargets, id) })
		picked, err := fl.p.Assistants(AssistantStep{Options: union(slices.Clone(installed), setUpOnly), Preselect: pre, Scopes: openLabels, Step: fl.steps(ReviewAssistants),
			Nav: fl.nav(ReviewAssistants)})
		if err != nil {
			return err
		}
		fl.shown[ReviewAssistants], fl.prevTarget = true, picked
		fl.done++
		for _, s := range open {
			answer[s], reasons[s] = picked, "picked"
		}
	}
	fl.targets, fl.why, fl.auto = answer, reasons, auto
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
		missing, pages := fl.componentPages(m)
		if len(missing) == 0 {
			continue
		}
		if pages == nil {
			for _, d := range missing {
				fl.picks[d.Scope.Key()][name] = fl.preselected(m, d.Scope.Key())
			}
			continue
		}
		for _, dests := range pages {
			prefix, err := fl.targetOf(m, dests)
			if err != nil {
				return err
			}
			title := prefix + name
			if len(dests) == 1 && len(fl.scopes) > 1 {
				title += " — " + destLabel(dests[0].Scope)
			}
			picked, ok, err := fl.askPage(m, dests, title)
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
			pa := answeredPage{module: name}
			for _, d := range dests {
				fl.picks[d.Scope.Key()][name] = picked
				pa.keys = append(pa.keys, d.Scope.Key())
			}
			fl.pageLog = append(slices.DeleteFunc(fl.pageLog, func(old answeredPage) bool {
				return old.module == name && slices.ContainsFunc(old.keys, func(k string) bool { return slices.Contains(pa.keys, k) })
			}), pa)
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

// askPage asks m's component page for dests, titled title, and, when nothing is picked of a
// preselection, whether to install m with no components, going back to the page from that
// question (Esc), as answered. ok is false when the user said no: m is dropped from the run.
func (fl *installFlow) askPage(m *engine.ModuleChoice, dests []engine.ScopeChoice, title string) (picked []string, ok bool, err error) {
	key := dests[0].Scope.Key()
	fl.asking = m.Name
	// A module whose mentions cannot be read gets no labels; the engine's own scan warns about it.
	mentions, _ := fl.choices.Mentions(m.Name)
	pg := fl.page(m, key, title, mentions)
	pg.Step, pg.Nav = fl.steps(ReviewComponents), fl.nav(ReviewComponents)
	fl.done += pg.lists()
	for {
		if picked, err = fl.p.Components(pg); err != nil {
			return nil, false, err
		}
		fl.asked[m.Name] = true
		if len(picked) > 0 || len(fl.preselected(m, key)) == 0 {
			return picked, true, nil
		}
		ok, err := fl.p.ConfirmEmpty(m.Name, Nav{Back: true, Answered: pg.Nav.Answered + 1})
		if !errors.Is(err, ErrBack) {
			return picked, ok, err
		}
		for _, g := range pg.Groups {
			for i := range g.Options {
				g.Options[i].Selected = false
			}
		}
	}
}

// componentPages is what the component step asks about m: the destinations whose picks are still
// open (missing), and the pages that ask them, nil when no page does: no component can be picked
// there, or m has one component and no exclusion. One page asks every destination, or one page
// each when their preselections, rules or only: lists (the page's footer) differ.
func (fl *installFlow) componentPages(m *engine.ModuleChoice) (missing []engine.ScopeChoice, pages [][]engine.ScopeChoice) {
	for _, c := range fl.scopes {
		if _, ok := fl.picks[c.Scope.Key()][m.Name]; !ok {
			missing = append(missing, c)
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	pickable := slices.ContainsFunc(missing, func(d engine.ScopeChoice) bool {
		return slices.ContainsFunc(m.Components, func(c engine.ComponentChoice) bool {
			return c.Kind != config.KindRule || !fl.rulesOff(m, d.Scope.Key())
		})
	})
	single := len(m.Components) == 1 && !slices.ContainsFunc(missing, func(d engine.ScopeChoice) bool { return len(m.Scope(d.Scope.Key()).Excluded) > 0 })
	if !pickable || single {
		return missing, nil
	}
	first := missing[0].Scope.Key()
	if slices.ContainsFunc(missing[1:], func(d engine.ScopeChoice) bool {
		return !slices.Equal(fl.preselected(m, first), fl.preselected(m, d.Scope.Key())) || fl.rulesOff(m, first) != fl.rulesOff(m, d.Scope.Key()) ||
			m.Scope(first).OnlyFrom != m.Scope(d.Scope.Key()).OnlyFrom
	}) {
		for _, d := range missing {
			pages = append(pages, []engine.ScopeChoice{d})
		}
		return missing, pages
	}
	return missing, [][]engine.ScopeChoice{missing}
}

// kindTitles name the kinds on a component page.
var kindTitles = map[string]string{config.KindSkill: "skills", config.KindCommand: "commands", config.KindAgent: "agents",
	config.KindMCP: "MCP servers", config.KindRule: "rules"}

// page builds a module's component page for the destination key: one group per kind with a
// count, current picks (or the preselection) selected, rules disabled when they are off,
// "(mentions N)" on a component that mentions N others (mentions; nil labels none), what leaves a
// component out ("(not in only:)", "(skipped in …)"), "(new)" on one the version adds over the one
// installed, and in the footer the profile that provides the module and the only: list in force.
func (fl *installFlow) page(m *engine.ModuleChoice, key, title string, mentions engine.Mentions) ComponentPage {
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
			if n := len(mentions[c.Component]); n > 0 {
				label += fmt.Sprintf(" (mentions %d)", n)
			}
			switch {
			case slices.Contains(ms.NotInOnly, c.String()):
				label += " (not in only:)" // the footer names the list
			case slices.Contains(ms.Excluded, c.String()):
				label += " (skipped in " + ms.ExcludedIn + ")"
			}
			if slices.Contains(ms.New, c.String()) {
				label += " (new)"
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
	var footer []string
	if m.RuntimeHooks > 0 {
		footer = append(footer, fmt.Sprintf("Runtime hooks: %d (you will be asked before they are installed)", m.RuntimeHooks))
	}
	if ms.Provider != "" {
		footer = append(footer, m.Name+" comes from "+ms.Provider+" here")
	}
	if ms.OnlyFrom != "" {
		footer = append(footer, "only: in "+ms.OnlyFrom+" — components a new version adds are not installed")
	}
	pg.Footer = strings.Join(footer, "\n")
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

// flagScript is the script --pre-install or --post-install names, given, made absolute against the
// directory lola runs in (Plan 9 §5.1, A1); none for "".
func (fl *installFlow) flagScript(given string) engine.FlagScript {
	if given == "" || filepath.IsAbs(given) {
		return engine.FlagScript{Given: given, Path: given}
	}
	return engine.FlagScript{Given: given, Path: filepath.Join(fl.cwd, given)}
}

// targetOf is the file the install of m writes at dests, as a page title starts with it: "profile
// go › ", "default (base) › ", "project.yml › " (spec 2026-10-29 S2, §8.9); "" when the
// destinations' differ. lola profile --edit and lola install --profile decide the file before the
// page: --edit asked which file it writes with the choices (ensureChoices), --profile names it,
// and an inherited m asks the inherited-edit question here (§3.6); each override answered is
// recorded in fl.overrides. A plain lola install names only a file no later question can change:
// "" while a remote profile or a bundle is on, or for m inherited without --override.
func (fl *installFlow) targetOf(m *engine.ModuleChoice, dests []engine.ScopeChoice) (string, error) {
	label := ""
	for i, d := range dests {
		key := d.Scope.Key()
		var w engine.ContextLayer
		if fl.f.edit != nil || fl.f.profile != "" {
			var err error
			if w, err = fl.choices.Place(d.Scope, m.Name, engine.Options{Target: fl.target(), AskWhere: fl.where}); err != nil {
				return "", err
			}
		} else {
			k := slices.IndexFunc(fl.scopes, func(c engine.ScopeChoice) bool { return c.Scope.Key() == key })
			if k < 0 || k >= len(fl.a.contexts) || fl.a.contexts[k].WritesTo == nil || m.Scope(key).Inherited && !fl.f.override {
				return "", nil
			}
			w = *fl.a.contexts[k].WritesTo
		}
		l := "profile " + w.Label
		if w.Kind == "base" {
			l = w.Label
			if d.Scope.Kind == target.User {
				l += " (base)"
			}
		}
		if m.Scope(key).Inherited && (fl.f.override || fl.answers[whereKey{key, m.Name}].Override) {
			fl.overrides[m.Name] = l
		}
		if i > 0 && l != label {
			label = "" // asked on: each destination's question comes before the page
			continue
		}
		label = l
	}
	if label == "" {
		return "", nil
	}
	return label + " › ", nil
}

// askWhere is the run's AskWhere (spec 2026-10-29 §3.6): when p, the run's prompter, is on a
// terminal, and not under --json, the where-picker, each question asked once per run (the review's
// dry run and the apply ask alike), each answer recorded by destination and module in byModule
// when it is not nil; nil otherwise, so the engine refuses with the flag forms.
func (a *app) askWhere(p Prompter, byModule map[whereKey]engine.WhereChoice) func(engine.WhereQuestion) (engine.WhereChoice, error) {
	if a.json || !p.Interactive() {
		return nil
	}
	answered := map[string]engine.WhereChoice{}
	return func(q engine.WhereQuestion) (engine.WhereChoice, error) {
		key := q.Scope.Key() + "\x00" + q.Module + "\x00" + q.Title
		if c, ok := answered[key]; ok {
			return c, nil
		}
		labels := make([]string, 0, len(q.Choices))
		for _, c := range q.Choices {
			labels = append(labels, c.Label)
		}
		i, err := p.Where(WhereStep{Title: q.Title, Options: labels})
		if err != nil {
			return engine.WhereChoice{}, err
		}
		answered[key] = q.Choices[i]
		if byModule != nil {
			byModule[whereKey{q.Scope.Key(), q.Module}] = q.Choices[i]
		}
		return q.Choices[i], nil
	}
}

// target is the run's TargetRequest (spec 2026-10-29 §3.5): --profile and --override, the run
// without them as each hint's base, and the run at user scope for --profile default in a project;
// for lola profile --edit, editTarget's.
func (fl *installFlow) target() engine.TargetRequest {
	if fl.f.edit != nil {
		t := editTarget(fl.cmd, fl.f.edit, fl.args, fl.f.create)
		t.Profile, t.Override, t.Catalog, t.CatalogPartial = fl.f.profile, fl.f.override, fl.f.catalog, fl.f.catalogPartial
		return t
	}
	without := append([]string{"profile", "override"}, consentFlags...)
	return engine.TargetRequest{Profile: fl.f.profile, Override: fl.f.override,
		Rerun: func(extra ...string) string {
			return strings.Join(append([]string{runWithout(fl.cmd, fl.args, without...)}, extra...), " ")
		},
		UserRun: func() string {
			return runWithout(fl.cmd, fl.args, slices.Concat(scopeFlags, consentFlags)...) + " --user"
		}, Again: rerunFlags(fl.cmd, 0)}
}

// spec builds the install from the answers.
func (fl *installFlow) spec() (engine.InstallSpec, error) {
	t := fl.target()
	spec := engine.InstallSpec{Cwd: fl.cwd, Staged: fl.staged, Options: engine.Options{Targets: fl.f.targets, DryRun: fl.f.dry, Force: fl.f.force,
		Strict: fl.f.strict, ForceRun: forceRun(fl.cmd, fl.hintArgs(fl.args)), RenderRun: renderRun(fl.cmd, fl.hintArgs(fl.args)), Trust: fl.f.trust, PreInstall: fl.flagScript(fl.f.preInstall),
		PostInstall: fl.flagScript(fl.f.postInstall), Display: outputFlags(fl.cmd), Target: t, AskWhere: fl.where,
		TargetRun: func(id string) string {
			return runWithout(fl.cmd, fl.hintArgs(fl.args), slices.Concat([]string{"target"}, consentFlags)...) + " -t " + id
		}},
		WithMentions: fl.f.withMentions, NameMentions: true, UserRun: t.UserRun()}
	if fl.f.rules || fl.f.noRules {
		spec.Options.Rules = &fl.f.rules
	}
	// The files the plan deletes, and a later scope's sync of lola profile --edit, are asked as
	// lola uninstall and lola sync ask (engine.InstallSpec.ConfirmRemovals); the review's Apply
	// already answered the plan's own.
	var asked bool
	_, _, spec.ConfirmRemovals = fl.a.syncQuestions(fl.cmd, syncFlags{yes: fl.f.yes}, false, "Remove these and apply? No writes nothing", nil, &asked)
	spec.DeletesConfirmed = fl.reviews()
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
			if slices.Contains(fl.f.keep, name) && fl.choices.Module(name) == nil && fl.marketNames[name] == "" {
				continue // the profile's, not in your cache: its entry stays as it is
			}
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
