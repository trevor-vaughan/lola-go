package cli

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// scopeSourceLabel says where a scope came from, in the summary.
func scopeSourceLabel(c engine.ScopeChoice) string {
	switch c.Source {
	case engine.SourceConfig:
		return "config.yaml"
	case engine.SourceDiscovered:
		return "found .lola/ at " + untrusted.Text(c.Scope.ProjectDir, untrusted.MaxName)
	}
	return c.Source
}

// assistants are the targets the destination key installs for: the answer, else the
// manifest's.
func (fl *installFlow) assistants(key string) []string {
	if t := fl.targets[key]; t != nil {
		return t
	}
	for _, s := range fl.choices.Scopes {
		if s.Scope.Key() == key {
			return s.FixedTargets
		}
	}
	return nil
}

// changes reports whether the run changes anything: a selections.yaml entry, a component, a
// module not installed yet, or one its write target neither lists nor inherits (the install writes
// its entry there), or lists at another commit than the one the install pins (it re-pins it),
// --force, or an installed module whose files lola list does not report ok (missing, hand-edited,
// stale): the engine repairs or reports those. A target that renders nothing of a module (skipped)
// stays as it is. A module the active profile lists
// (pv.SaveOnly) changes only by its manifest entry: the install installs nothing of it (spec
// 2026-10-29 §3.1), so neither its components nor its files count.
func (fl *installFlow) changes(pv *engine.Preview) (bool, error) {
	if len(pv.Changes) > 0 || fl.f.force {
		return true, nil
	}
	for _, c := range fl.scopes {
		key := c.Scope.Key()
		for _, name := range fl.modules {
			m := fl.choices.Module(name)
			if m == nil {
				return true, nil
			}
			// An inherited entry is kept, pin included, by an install asking what it says.
			ms := m.Scope(key)
			if !ms.Installed || !ms.Listed && !ms.Inherited {
				return true, nil
			}
			if repins(ms) {
				return true, nil
			}
			if _, ok := pv.SaveOnly[key][name]; ok {
				continue
			}
			for _, id := range fl.assistants(key) {
				d := m.Diff(key, id, pv.Selected[key][name])
				if len(d.Added)+len(d.Updated)+len(d.Removed) > 0 {
					return true, nil
				}
			}
		}
		rows, _, err := fl.eng.Status(fl.cmd.Context(), c.Scope, nil)
		if err != nil {
			return false, err
		}
		for _, row := range rows {
			_, saveOnly := pv.SaveOnly[key][row.Name]
			if row.Status != "ok" && row.Status != "skipped" && !saveOnly && slices.Contains(fl.modules, row.Name) && slices.Contains(fl.assistants(key), row.Target) {
				return true, nil
			}
		}
	}
	return false, nil
}

// repins reports whether an install re-pins the module ms is at a destination: installed, its
// entry is the write target's own, and the commit the install pins there is another than the one
// lock.json records (spec 2026-10-29 §16).
func repins(ms engine.ModuleScope) bool {
	return ms.Installed && ms.Listed && ms.ListedCommit != "" && ms.ListedCommit != ms.Commit
}

// diffLine renders a component diff: + added, ~ updated, - removed, = N unchanged.
func diffLine(d engine.ComponentDiff) string {
	var parts []string
	for _, x := range []struct {
		glyph string
		items []string
	}{{"+", d.Added}, {"~", d.Updated}, {"-", d.Removed}} {
		for _, c := range x.items {
			parts = append(parts, x.glyph+" "+c)
		}
	}
	if d.Unchanged > 0 || len(parts) == 0 {
		parts = append(parts, "= "+strconv.Itoa(d.Unchanged)+" unchanged")
	}
	return strings.Join(parts, ", ")
}

// printSummary writes the review's summary (§4.5, Plan 10 §5): each destination with its
// directory and where it came from; per assistant, why it was chosen, and each module's component
// diff, or for a module the install saves but installs nothing of (pv.SaveOnly) that it saves the
// entry of its write target (pv.WritesTo), and why: a higher layer's entry wins, or that file is not
// on here; the selections.yaml changes, the notices and the findings a dry run of the install
// reports, as the run prints them (printNotices), dropped answers, and the equivalent commands.
func (fl *installFlow) printSummary(w io.Writer, spec engine.InstallSpec, pv *engine.Preview, findings []target.Finding) {
	fmt.Fprintln(w, "Summary:")
	for _, line := range fl.choices.Resolved {
		fmt.Fprintln(w, "  "+line)
	}
	for _, c := range fl.scopes {
		key := c.Scope.Key()
		dest := destLabel(c.Scope)
		if c.Scope.ProjectDir == "" {
			dest += " (" + untrusted.Text(fl.eng.Paths.Home, untrusted.MaxName) + ")"
		}
		fmt.Fprintf(w, "  %s, %s:\n", dest, scopeSourceLabel(c))
		for _, id := range fl.assistants(key) {
			fmt.Fprintf(w, "    → for %s (%s)\n", id, fl.why[key])
			for _, name := range fl.modules {
				if winner, ok := pv.SaveOnly[key][name]; ok && winner == "" {
					fmt.Fprintf(w, "      %s: saves %s's entry; it is not on here\n", name, pv.WritesTo[key])
				} else if ok {
					fmt.Fprintf(w, "      %s: saves %s's entry; installs nothing while %s's entry wins\n", name, pv.WritesTo[key], winner)
				} else if m := fl.choices.Module(name); m != nil {
					fmt.Fprintf(w, "      %s: %s\n", name, diffLine(m.Diff(key, id, pv.Selected[key][name])))
				}
			}
		}
		for _, name := range fl.modules {
			if m := fl.choices.Module(name); m != nil && repins(m.Scope(key)) {
				fmt.Fprintf(w, "    → %s: re-pinned at %s\n", name, shortCommit(m.Scope(key).ListedCommit))
			}
		}
	}
	// Picks a profile entry holds are listed under that profile, and an override's under the profile
	// it writes (spec 2026-10-29 §3.6); selections.yaml holds the rest.
	var onFile []config.Change
	var files []string
	entries := map[string][]string{}
	for _, ch := range pv.Changes {
		in := ""
		if ch.Section.Kind == config.SectionProfile {
			in = pv.PicksIn[ch.Section.Profile]
		}
		for _, c := range fl.scopes {
			key := c.Scope.Key()
			if to, ok := pv.Inherited[key][ch.Section][ch.Module]; ok && (fl.f.override || fl.answers[whereKey{key, ch.Module}].Override) {
				in = to
				break
			}
		}
		if in == "" {
			onFile = append(onFile, ch)
			continue
		}
		if _, seen := entries[in]; !seen {
			files = append(files, in)
		}
		picks := entryText(ch.Entry)
		if ch.Entry.Empty() {
			picks = "every component (no only: or skip:)" // the profile's entry stays; only its picks go
		}
		entries[in] = append(entries[in], fmt.Sprintf("    %s: %s\n", ch.Module, picks))
	}
	if len(onFile) > 0 {
		fmt.Fprintf(w, "  selections.yaml (%s):\n", untrusted.Text(fl.choices.SelectionsPath(), untrusted.MaxName))
		for _, ch := range onFile {
			fmt.Fprintf(w, "    %s in %s: %s\n", ch.Module, untrusted.Text(ch.Section.String(), untrusted.MaxName), entryText(ch.Entry))
		}
	}
	for _, in := range files {
		fmt.Fprintf(w, "  %s:\n%s", in, strings.Join(entries[in], ""))
	}
	var found strings.Builder
	printNotices(&found, style{verbose: fl.a.style(fl.cmd).verbose}, &engine.Result{Findings: findings, Notices: pv.Notices})
	for _, line := range strings.SplitAfter(found.String(), "\n") {
		if line != "" {
			fmt.Fprint(w, "  "+line)
		}
	}
	for _, d := range []struct {
		n   int
		why string
	}{{fl.droppedModule, "module removed"}, {fl.droppedScope, "scope removed"}} {
		switch {
		case d.n == 1:
			fmt.Fprintf(w, "  1 component pick dropped: %s\n", d.why)
		case d.n > 1:
			fmt.Fprintf(w, "  %d component picks dropped: %s\n", d.n, d.why)
		}
	}
	fmt.Fprintln(w, "Equivalent commands:")
	// A -C root is made safe in the command itself (equivalents), so it is checked here.
	unsafeRoot := slices.ContainsFunc(spec.Scopes, func(si engine.ScopeInstall) bool {
		return si.Scope.ProjectDir != untrusted.Text(si.Scope.ProjectDir, untrusted.MaxName)
	})
	for _, line := range fl.equivalents(spec) {
		safe := untrusted.Text(line, len(line))
		fmt.Fprintln(w, "  "+safe)
		if safe != line || (unsafeRoot && strings.Contains(line, " -C ")) {
			fmt.Fprintln(w, "note: the line above has unsafe characters replaced by '?'; it is not copyable as shown")
		}
	}
}

// entryText shows a selections.yaml entry as it will be written; patterns from the file are
// made safe to print.
func entryText(e config.Entry) string {
	list := func(ps []config.Pattern) string {
		var s []string
		for _, p := range ps {
			s = append(s, untrusted.Text(p.String(), untrusted.MaxName))
		}
		return "[" + strings.Join(s, ", ") + "]"
	}
	var parts []string
	for _, l := range []struct {
		key string
		ps  []config.Pattern
	}{{"skip", e.Skip}, {"include", e.Include}, {"only", e.Only}} {
		if len(l.ps) > 0 {
			parts = append(parts, l.key+" "+list(l.ps))
		}
	}
	if len(parts) == 0 {
		return "(removed)"
	}
	return strings.Join(parts, ", ")
}

// equivalents are the commands that do what this run does off a terminal (§4.5, §14): a lola
// cache add for each source added, then one lola install line per destination (one per group of
// modules whose where-answers there are alike), quoted for the
// user's shell (errs.ShellJoin) and without --dry-run. With more than one destination every
// project line names its root with -C; a single project line does so only when the root is not
// the project found from here, which the engine decides for every hint (FoundHere).
func (fl *installFlow) equivalents(spec engine.InstallSpec) []string {
	var out []string
	for _, a := range fl.added {
		args := []string{"lola", "cache", "add", a.source}
		if a.ref != "" {
			args = append(args, "--ref", a.ref)
		}
		out = append(out, errs.ShellJoin(args))
	}
	// pre and post are the run's flags before and after a line's where-answers and global edits.
	var pre, post []string
	if len(fl.args) > 0 && fl.f.ref != "" {
		pre = append(pre, "--ref", fl.f.ref)
	}
	if len(fl.args) > 0 && fl.f.path != "" {
		pre = append(pre, "--path", fl.f.path)
	}
	if fl.f.profile != "" {
		pre = append(pre, "--profile", fl.f.profile)
	}
	if fl.f.override {
		pre = append(pre, "--override")
	}
	if fl.f.global {
		post = append(post, "--global")
	}
	if fl.f.exact {
		post = append(post, "--exact")
	}
	if fl.f.withMentions {
		post = append(post, "--with-mentions")
	}
	for _, f := range []struct{ flag, given string }{{"--pre-install", fl.f.preInstall}, {"--post-install", fl.f.postInstall}} {
		if f.given != "" {
			post = append(post, f.flag, f.given)
		}
	}
	for _, b := range []struct {
		on   bool
		flag string
	}{{fl.f.rules, "--rules"}, {fl.f.noRules, "--no-rules"}, {fl.f.force, "--force"}, {fl.f.strict, "--strict"},
		{fl.f.trust != 0, fl.f.trust.Arg()}} {
		if b.on {
			post = append(post, b.flag)
		}
	}
	// where is the flag form of the where-question answered on the terminal for module at the
	// destination key, or else for the profile on there (module ""), as --profile and --override
	// say it (spec 2026-10-29 §3.6); nil when none was, or the run's own flag answers it.
	where := func(key, module string) []string {
		c, ok := fl.answers[whereKey{key, module}]
		if !ok {
			c, ok = fl.answers[whereKey{key, ""}]
		}
		switch {
		case !ok || fl.f.override:
			return nil
		case c.Override:
			return []string{"--override"}
		case c.Profile != "" && fl.f.profile == "":
			return []string{"--profile", c.Profile}
		}
		return nil
	}
	// edits are es's flags for the modules names: an edit naming another module goes with its line.
	edits := func(es []engine.SelectionEdit, names []string) []string {
		var f []string
		for _, e := range es {
			if e.Selector.Module == "" || slices.Contains(names, e.Selector.Module) {
				f = append(f, "--"+e.Op, e.Selector.String())
			}
		}
		return f
	}
	// Each destination gets one line per group of modules whose where-answers there are alike.
	type group struct{ args, names, where []string }
	for _, si := range spec.Scopes {
		key := si.Scope.Key()
		var groups []*group
		for _, r := range spec.Modules {
			name := r.Arg
			if r.Version != nil {
				name = r.Version.Name
			}
			w := where(key, name)
			k := slices.IndexFunc(groups, func(g *group) bool { return slices.Equal(g.where, w) })
			if k < 0 {
				groups, k = append(groups, &group{where: w}), len(groups)
			}
			groups[k].args, groups[k].names = append(groups[k].args, r.Arg), append(groups[k].names, name)
		}
		var root []string
		if si.Scope.ProjectDir != "" && (len(spec.Scopes) > 1 || !fl.eng.FoundHere(si.Scope)) {
			root = []string{"-C", untrusted.Text(si.Scope.ProjectDir, untrusted.MaxName)}
		}
		for _, g := range groups {
			args := append(append([]string{"lola", "install"}, g.args...), "--scope", si.Scope.Kind.String())
			args = append(args, root...)
			if len(si.Targets) > 0 {
				args = append(args, "-t", strings.Join(si.Targets, ","))
			}
			args = slices.Concat(args, edits(si.Edits, g.names), pre, g.where, edits(spec.GlobalEdits, g.names), post)
			out = append(out, errs.ShellJoin(args))
		}
	}
	return out
}
