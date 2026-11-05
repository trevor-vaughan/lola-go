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

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// editFlags are lola profile --edit's flags beside syncFlags (spec 2026-10-29 §6.2; cherry-pick
// design §4.3): the install flags it passes on (picks, assistants, --override) and --new.
type editFlags struct {
	targets, skips, onlys, unskips []string
	exact, withMentions, override  bool
	create                         bool
	from                           string   // --from, with --new
	members                        []string // --bundle (nil when not given; [] for --bundle "")
}

// profileEdit runs lola profile [<name>] --edit [<module>...] (spec 2026-10-29 §6.2; cherry-pick
// design §4.3): the arguments are split into the profile and the modules at the one scope the run
// takes without asking (EditArgs, which asks which a name is when it is both); with modules, the
// edit is lola install of them into that profile (its --profile), or into the one edits go to when
// it names none, applied in each scope the profile is on (EditScopes), its hints worded as this run;
// without, --new makes the profile, and anything else is refused, saying how to name a module.
func (a *app) profileEdit(cmd *cobra.Command, args []string, ef editFlags, f syncFlags) error {
	eng, err := a.engine()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	read, err := a.readChoice(eng)
	if err != nil {
		return err
	}
	sc := read.Scope
	// Read before the install locks the catalogs: hints qualify a profile name an entry has too,
	// and every one when a catalog cannot be read, which fails nothing else.
	catalog, complete := eng.CatalogNames(outputFlags(cmd))
	name, modules, err := eng.EditArgs(engine.EditArgsRequest{Scope: sc, Cwd: cwd, Args: args, Made: ef.create, Display: outputFlags(cmd), Catalog: catalog,
		CatalogPartial: !complete,
		Rerun:          func(names ...string) string { return runWithout(cmd, names, consentFlags...) }, Ask: a.askWhere(a.prompter(cmd), nil)})
	if err != nil {
		a.printContext(cmd, eng, []engine.Scope{sc}, nil)
		return err
	}
	lead := []string{}
	if name != "" {
		lead = []string{name}
	}
	req := editTarget(cmd, lead, modules, ef.create)
	req.Profile, req.Catalog, req.CatalogPartial = name, catalog, !complete
	if ef.create && name == "" {
		return errs.WithHints(errors.New("--new makes the profile --edit names, and none was named: name it, as in lola profile <name> --edit --new"),
			"see the profiles: "+eng.Command(sc, "profile", outputFlags(cmd)...))
	}
	bundle := cmd.Flags().Changed("bundle")
	if err := editModifiers(cmd, eng, sc, lead, modules, ef, req); err != nil {
		a.printContext(cmd, eng, []engine.Scope{sc}, nil)
		return err
	}
	g := argGuide{noun: "module", find: "lola cache list", usage: "lola profile [<name>] --edit <module>...",
		examples: []string{"lola profile go-review --edit cc-skills-golang --only skill/golang-testing"}}
	// The pages ask for no flag they would drop: picks and a source's --ref/--path name a module.
	picks := len(ef.skips)+len(ef.onlys)+len(ef.unskips) > 0 || ef.exact || cmd.Flags().Changed("ref") || cmd.Flags().Changed("path")
	if len(modules) == 0 && !ef.create && !bundle && !picks && !a.json && a.prompter(cmd).Interactive() {
		return a.editPage(cmd, eng, g, read, name, req, ef, f)
	}
	if name != "" && !ef.create {
		if _, err := eng.EditTarget(sc, req, nil); engine.IsUnknownProfile(err) {
			a.printContext(cmd, eng, []engine.Scope{sc}, nil)
			return err
		}
	}
	if len(modules) == 0 { // no install flow, which prints the context once it has the scope
		if bundle && !ef.create {
			return a.editBundle(cmd, eng, read, name, ef.members, req, f, forceRun(cmd, []string{name}), false)
		}
		a.printContext(cmd, eng, []engine.Scope{sc}, nil)
		if ef.create {
			return a.makeProfile(cmd, eng, engine.MakeRequest{Scope: sc, Name: name, From: ef.from, Bundle: ef.members, Target: req,
				Options: engine.Options{DryRun: f.dry, Display: outputFlags(cmd)}})
		}
		return a.nameAModule(cmd, eng, sc, req)
	}
	scopes, err := eng.EditScopes(read, name)
	if err != nil {
		a.printContext(cmd, eng, []engine.Scope{sc}, nil)
		return err
	}
	ref, _ := cmd.Flags().GetString("ref")
	path, _ := cmd.Flags().GetString("path")
	return a.runInstall(cmd, g, modules, installFlags{targets: ef.targets, skips: ef.skips, onlys: ef.onlys, unskips: ef.unskips, ref: ref, path: path,
		profile: name, dry: f.dry, force: f.force, yes: f.yes, exact: ef.exact, withMentions: ef.withMentions, override: ef.override, trust: f.trust,
		edit: lead, create: ef.create, scopes: scopes, catalog: catalog, catalogPartial: !complete})
}

// editModifiers refuses --from and --bundle where lola profile <name> --edit cannot take them
// (spec 2026-10-29 §6.2): the two together (a new profile copies one, or bundles several), beside
// modules (each makes or edits the profile's own file), and --from without --new. Each hint is a
// run that works as the name reads at sc (req.Profile): one that makes the profile only when no
// profile has the name, one that sets a bundle's members only on a bundle or an empty profile.
func editModifiers(cmd *cobra.Command, eng *engine.Engine, sc engine.Scope, lead, modules []string, ef editFlags, req engine.TargetRequest) error {
	from, bundle := ef.from != "", cmd.Flags().Changed("bundle")
	if !from && !bundle || len(modules) == 0 && (ef.create || !from) && (!from || !bundle) {
		return nil
	}
	name := req.Profile
	without := func(args []string, drop ...string) string {
		return runWithout(cmd, args, append(drop, consentFlags...)...)
	}
	var unknown, bundleable, exists bool
	if name != "" {
		t, err := eng.EditTarget(sc, req, nil)
		unknown, exists = engine.IsUnknownProfile(err), err == nil
		bundleable = exists && t.Name != "base" && (t.Bundle || len(t.Modules) == 0)
	}
	fromRun, fromWorks := without(lead, "bundle"), unknown
	if !ef.create {
		fromRun += " --new"
	}
	bundleRun, bundleWorks := without(lead, "from"), bundleable && !slices.Contains(ef.members, name) // a bundle cannot name itself
	if ef.create {
		bundleWorks = unknown
	}
	modulesWork := exists || name == ""
	if ef.create {
		modulesWork = unknown
	}
	var msg error
	var hints []string
	switch {
	case from && bundle:
		msg = errors.New("--from copies a profile and --bundle combines several; pick one")
		if fromWorks {
			hints = append(hints, "keep --from: "+fromRun)
		}
		if bundleWorks {
			hints = append(hints, "keep --bundle: "+bundleRun)
		}
	case len(modules) > 0:
		flag, run, works := "bundle", bundleRun, bundleWorks
		if from {
			flag, run, works = "from", fromRun, fromWorks
		}
		msg = fmt.Errorf("--%s makes or edits a profile's own file; the modules %s name are a separate edit", flag,
			untrusted.Text(strings.Join(modules, ", "), untrusted.MaxName))
		if works && name != "" {
			hints = append(hints, "drop them: "+run)
		}
		if modulesWork {
			hints = append(hints, "or drop --"+flag+": "+without(append(slices.Clone(lead), modules...), flag))
		}
	case name == "":
		msg = errors.New("--from makes a new profile and goes with --new: name it, as in lola profile <name> --edit --new --from " +
			untrusted.Text(ef.from, untrusted.MaxName))
	case exists:
		msg = fmt.Errorf("--from makes a new profile and goes with --new, and %s exists", untrusted.Text(name, untrusted.MaxName))
		hints = append(hints, "see it: "+eng.Command(sc, "profile", append([]string{name}, outputFlags(cmd)...)...))
	default:
		msg = errors.New("--from makes a new profile and goes with --new")
		if fromWorks {
			hints = append(hints, "make it: "+without(lead)+" --new")
		}
	}
	if len(hints) == 0 {
		hints = append(hints, "see the profiles: "+eng.Command(sc, "profile", outputFlags(cmd)...))
	}
	return errs.WithHints(msg, hints...)
}

// editPage is lola profile [<name>] --edit with no module on a terminal (spec 2026-10-29 §6.2;
// cherry-pick design §4.3): what it edits is asked as an install asks it (the member question under
// a bundle); a profile's page is the module picker with its modules ticked, then each ticked
// module's component page, a module unticked being removed from it once asked, applied in each
// scope it is on (EditScopes); a bundle's is its members page, then editBundle; a name no profile
// has asks whether the new profile lists modules (the module page, then the profile is made with
// what was picked, or empty) or bundles profiles (the members page, then the bundle is made).
func (a *app) editPage(cmd *cobra.Command, eng *engine.Engine, g argGuide, read engine.ScopeChoice, name string, req engine.TargetRequest, ef editFlags,
	f syncFlags) error {
	p, sc := a.prompter(cmd), read.Scope
	t, err := eng.EditTarget(sc, req, a.askWhere(p, nil))
	install := installFlags{targets: ef.targets, dry: f.dry, force: f.force, yes: f.yes, withMentions: ef.withMentions, override: ef.override, trust: f.trust,
		catalog: req.Catalog, catalogPartial: req.CatalogPartial}
	switch {
	case engine.IsUnknownProfile(err):
		a.printContext(cmd, eng, []engine.Scope{sc}, nil)
		shown := untrusted.Text(name, untrusted.MaxName)
		i, err := p.Where(WhereStep{Title: "New profile " + shown + ":", Options: []string{"Lists modules", "Bundles profiles"}})
		if err != nil {
			return err
		}
		req.New = true
		if i == 1 {
			members, err := a.membersPage(cmd, eng, sc, "new bundle "+shown, name)
			if err != nil {
				return err
			}
			return a.makeProfile(cmd, eng, engine.MakeRequest{Scope: sc, Name: name, Bundle: members, Target: req,
				Options: engine.Options{DryRun: f.dry, Display: outputFlags(cmd)}})
		}
		install.profile, install.edit, install.create, install.scopes = name, []string{name}, true, []engine.ScopeChoice{read}
		install.keep, install.keepLabel = []string{}, "new profile "+shown
		return a.runInstall(cmd, g, nil, install)
	case err != nil:
		a.printContext(cmd, eng, []engine.Scope{sc}, nil)
		return err
	case t.Bundle:
		members, err := a.membersPage(cmd, eng, sc, t.Label, t.Name)
		if err != nil {
			return err
		}
		return a.editBundle(cmd, eng, read, t.Name, members, req, f, forceRun(cmd, []string{t.Name}), false)
	}
	scopes, err := eng.EditScopes(read, t.Name)
	if err != nil {
		a.printContext(cmd, eng, []engine.Scope{sc}, nil)
		return err
	}
	install.profile, install.edit, install.scopes, install.keep, install.keepLabel, install.keepAdd = t.Name, []string{t.Name}, scopes, t.Modules, t.Label,
		t.CacheAdd
	return a.runInstall(cmd, g, nil, install)
}

// membersPage asks the members of the bundle name, labeled label, at sc (engine.MemberChoices): the
// profiles it can name, its members now ticked, one that names no profile labeled so; the answer
// keeps the members ticked in their order, the ones newly ticked after them.
func (a *app) membersPage(cmd *cobra.Command, eng *engine.Engine, sc engine.Scope, label, name string) ([]string, error) {
	bm, err := eng.MemberChoices(sc, name)
	if err != nil {
		return nil, err
	}
	current := bm.Members
	options := make([]ModuleOption, 0, len(bm.Options))
	for _, n := range bm.Options {
		o := ModuleOption{Name: n, Label: n}
		if slices.Contains(bm.Unresolved, n) {
			o.Label += " (names no profile here)"
		}
		options = append(options, o)
	}
	picked, err := a.prompter(cmd).Members(MembersStep{Title: label + " › members", Options: options, Preselect: current})
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, m := range current {
		if slices.Contains(picked, m) {
			out = append(out, m)
		}
	}
	for _, m := range picked {
		if !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out, nil
}

// editTarget is the TargetRequest of lola profile <lead> --edit <modules> (spec 2026-10-29 §6.2): its
// hints are the run, the profile it names taken from extra's "--profile <name>" (base included) in
// place of lead, and --override as given; --new kept only when create and the name is the run's own.
func editTarget(cmd *cobra.Command, lead, modules []string, create bool) engine.TargetRequest {
	return engine.TargetRequest{Edit: true, New: create, Again: rerunFlags(cmd, 0), Rerun: func(extra ...string) string {
		named, rest := slices.Clone(lead), extra
		if name, others := engine.ProfileFlag(extra); name != "" {
			named, rest = []string{name}, others
		}
		return strings.Join(append([]string{runWithout(cmd, append(named, modules...), slices.Concat([]string{"override", "new"}, consentFlags)...)},
			rest...), " ")
	}, UserRun: func() string {
		return runWithout(cmd, append(slices.Clone(lead), modules...), slices.Concat(scopeFlags, consentFlags)...) + " --user"
	}}
}

// nameAModule refuses lola profile [<name>] --edit without a module: it names the form, and hints
// what shows the profile's modules and the cache's.
func (a *app) nameAModule(cmd *cobra.Command, eng *engine.Engine, sc engine.Scope, req engine.TargetRequest) error {
	t, err := eng.EditTarget(sc, req, a.askWhere(a.prompter(cmd), nil))
	if err != nil {
		return err
	}
	show := eng.Command(sc, "profile", t.Name)
	if t.Name == "base" {
		show = eng.Command(sc, "list")
	}
	if t.Bundle {
		return errs.WithHints(fmt.Errorf("%s lists profiles, not modules (%s): --bundle sets them, as in lola profile %s --edit --bundle %s",
			t.Label, strings.Join(t.Members, ", "), untrusted.Text(t.Name, untrusted.MaxName), strings.Join(append(slices.Clone(t.Members), "<profile>"), ",")),
			"see it: "+show, "see the profiles: "+eng.Command(sc, "profile", outputFlags(cmd)...))
	}
	return errs.WithHints(fmt.Errorf("lola profile --edit edits %s by its modules: name one, as in lola profile %s --edit <module> --only <kind/name>",
		t.Label, untrusted.Text(t.Name, untrusted.MaxName)), "see what it lists: "+show, "see your cache: lola cache list")
}

// makeProfile runs lola profile <name> --edit --new with no module: the profile is made, empty, a
// copy (--from) or a bundle (--bundle), and nothing is applied (spec 2026-10-29 §6.2).
func (a *app) makeProfile(cmd *cobra.Command, eng *engine.Engine, req engine.MakeRequest) error {
	res, err := eng.MakeProfile(cmd.Context(), req)
	if err != nil {
		return err
	}
	return a.emit(cmd, func(w io.Writer, s style) {
		printCreated(w, s, res.Created, res.DryRun)
		for _, n := range res.Notices {
			fmt.Fprintln(w, noticeLine(s, n))
		}
	}, res)
}

// editBundle runs lola profile <name> --edit --bundle <members> at read's scope: the bundle's members
// are set, and each scope it reaches applies them (EditScopes, ruling P1), each scope's context line
// printed first, with sync's questions: removals asked (-y, --force), trust asked or refused off a
// terminal (--trust) (spec 2026-10-29 §6.2). force is the edit with --force, which a refusal --force
// overrides hints; said says read's context line is printed already (--on's question), so only
// the other scopes' lines are printed.
func (a *app) editBundle(cmd *cobra.Command, eng *engine.Engine, read engine.ScopeChoice, name string, members []string, req engine.TargetRequest, f syncFlags,
	force string, said bool) error {
	sc := read.Scope
	choices, err := eng.EditScopes(read, name)
	if err != nil {
		if !said {
			a.printContext(cmd, eng, []engine.Scope{sc}, nil)
		}
		return err
	}
	scopes := make([]engine.Scope, len(choices))
	for i, c := range choices {
		scopes[i] = c.Scope
	}
	switch {
	case !said:
		a.printContext(cmd, eng, scopes, nil)
	case len(scopes) > 1: // read's line is printed: the others follow it, once each
		printed := a.contexts
		a.printContext(cmd, eng, scopes[1:], nil)
		a.contexts = append(printed, a.contexts...)
	}
	opts := engine.Options{DryRun: f.dry, Force: f.force, Display: outputFlags(cmd), ForceRun: force}
	opts.Trust, opts.Receipt = f.trust, a.receipt(cmd, f.trust, f.yes)
	a.hookOptions(cmd, &opts)
	bundle := manifestName(sc)
	diff := func(rep engine.TrustReport) func(io.Writer, style) {
		return func(w io.Writer, s style) { printTrustDiff(w, s, bundle, "lola profile", rep, "") }
	}
	var asked bool
	breq := engine.BundleEditRequest{Scope: sc, Also: scopes[1:], Name: name, Members: members, Target: req, Options: opts}
	breq.Show, breq.Trust, breq.ConfirmRemovals = a.syncQuestions(cmd, f, true, "Remove these and apply? No applies nothing", diff, &asked)
	res, err := eng.EditBundle(cmd.Context(), breq)
	if errors.Is(err, engine.ErrDeclined) || errors.Is(err, ErrAborted) {
		return syncAborted(sc)
	}
	if err != nil {
		return err
	}
	return a.emit(cmd, func(w io.Writer, s style) { printBundleEdit(w, s, sc, res) }, res)
}

// printBundleEdit prints an --edit --bundle at sc: under -n the trust question it would ask first,
// when the bundle is off (an on bundle's apply shows it), the profiles it made (printCreated), the write line
// of the bundle (under -n, "would change …"), its notices, then the apply of each scope it applied
// in, each headed by its scope when there are several. --plain rows are created (or would-create)
// per profile made, then bundle (or would-bundle), the profile, its members comma-separated, and
// its path.
func printBundleEdit(w io.Writer, s style, sc engine.Scope, r *engine.BundleEdit) {
	if r.Trust != nil { // -n of a bundle that is off: the question the run would ask first
		printTrustDiff(w, s, manifestName(sc), "lola profile", *r.Trust, "")
	}
	printCreated(w, s, r.Created, r.DryRun)
	if s.plain {
		bundle := "bundle"
		if r.DryRun {
			bundle = "would-bundle"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", bundle, r.Profile, strings.Join(r.Members, ","), r.Path)
	} else {
		_, label, _ := strings.Cut(r.Profile, ":")
		list := func(ms []string) string { return cmp.Or(strings.Join(ms, ", "), "none") }
		line := "bundle " + label + " (" + list(r.Was) + " → " + list(r.Members) + ")  (" + r.Path + ")"
		switch {
		case slices.Equal(r.Was, r.Members):
		case r.DryRun:
			fmt.Fprintln(w, s.info("would change "+line))
		default:
			fmt.Fprintln(w, s.ok("changed "+line))
		}
	}
	for _, n := range r.Notices {
		fmt.Fprintln(w, noticeLine(s, n))
	}
	applied := r.Also
	if r.Sync != nil {
		applied = append([]engine.ScopeSync{{Scope: sc, SyncResult: r.Sync}}, applied...)
	}
	for _, a := range applied {
		if len(r.Also) > 0 && !s.plain {
			fmt.Fprintln(w, destLabel(a.Scope)+":")
		}
		printSync(w, s, manifestName(a.Scope), a.SyncResult, nil, "")
	}
}
