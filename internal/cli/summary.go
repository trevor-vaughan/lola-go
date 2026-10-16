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
// module not installed yet, --force, or an installed module whose files lola list does not
// report ok (missing, hand-edited, stale): the engine repairs or reports those. A target that
// renders nothing of a module (skipped) stays as it is.
func (fl *installFlow) changes(pv *engine.Preview) (bool, error) {
	if len(pv.Changes) > 0 || fl.f.force {
		return true, nil
	}
	for _, c := range fl.scopes {
		key := c.Scope.Key()
		for _, name := range fl.modules {
			m := fl.choices.Module(name)
			if m == nil || !m.Scope(key).Installed {
				return true, nil
			}
			for _, id := range fl.assistants(key) {
				d := m.Diff(key, id, pv.Selected[key][name])
				if len(d.Added)+len(d.Updated)+len(d.Removed) > 0 {
					return true, nil
				}
			}
		}
		rows, _, err := fl.eng.Status(fl.cmd.Context(), c.Scope)
		if err != nil {
			return false, err
		}
		for _, row := range rows {
			if row.Status != "ok" && row.Status != "skipped" && slices.Contains(fl.modules, row.Name) && slices.Contains(fl.assistants(key), row.Target) {
				return true, nil
			}
		}
	}
	return false, nil
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

// printSummary prints §4.5's summary on stderr: per destination and assistant the component
// diff, each destination's source, the selections.yaml changes, dropped answers, and the
// equivalent commands.
func (fl *installFlow) printSummary(w io.Writer, spec engine.InstallSpec, pv *engine.Preview) {
	fmt.Fprintln(w, "Summary:")
	for _, c := range fl.scopes {
		key := c.Scope.Key()
		fmt.Fprintf(w, "  %s, %s:\n", destLabel(c.Scope), scopeSourceLabel(c))
		for _, id := range fl.assistants(key) {
			fmt.Fprintf(w, "    %s:\n", id)
			for _, name := range fl.modules {
				if m := fl.choices.Module(name); m != nil {
					fmt.Fprintf(w, "      %s: %s\n", name, diffLine(m.Diff(key, id, pv.Selected[key][name])))
				}
			}
		}
	}
	if len(pv.Changes) > 0 {
		fmt.Fprintf(w, "  selections.yaml (%s):\n", untrusted.Text(fl.choices.SelectionsPath(), untrusted.MaxName))
		for _, ch := range pv.Changes {
			fmt.Fprintf(w, "    %s in %s: %s\n", ch.Module, untrusted.Text(ch.Section.String(), untrusted.MaxName), entryText(ch.Entry))
		}
	}
	for _, n := range pv.Notices {
		fmt.Fprintln(w, "  "+noticeLine(style{}, n))
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
	switch {
	case e.Empty():
		return "(removed)"
	case len(e.Include) == 0:
		return "skip " + list(e.Skip)
	case len(e.Skip) == 0:
		return "include " + list(e.Include)
	}
	return "skip " + list(e.Skip) + ", include " + list(e.Include)
}

// equivalents are the commands that do what this run does off a terminal (§4.5, §14): a lola
// cache add for each source added, then one lola install line per destination, quoted for the
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
	base := []string{"lola", "install"}
	for _, m := range spec.Modules {
		base = append(base, m.Arg)
	}
	var tail []string
	if len(fl.args) > 0 && fl.f.ref != "" {
		tail = append(tail, "--ref", fl.f.ref)
	}
	if len(fl.args) > 0 && fl.f.path != "" {
		tail = append(tail, "--path", fl.f.path)
	}
	for _, e := range spec.GlobalEdits { // a scope's edits, flags included, go with its line
		tail = append(tail, "--"+e.Op, e.Selector.String())
	}
	if fl.f.global {
		tail = append(tail, "--global")
	}
	for _, b := range []struct {
		on   bool
		flag string
	}{{fl.f.rules, "--rules"}, {fl.f.noRules, "--no-rules"}, {fl.f.force, "--force"}, {fl.f.strict, "--strict"},
		{fl.f.allowHooks, "--allow-hooks"}, {fl.f.allowRuntime, "--allow-runtime-hooks"}} {
		if b.on {
			tail = append(tail, b.flag)
		}
	}
	per := func(si engine.ScopeInstall) []string {
		var f []string
		if len(si.Targets) > 0 {
			f = append(f, "-t", strings.Join(si.Targets, ","))
		}
		for _, e := range si.Edits {
			f = append(f, "--"+e.Op, e.Selector.String())
		}
		return f
	}
	line := func(scope string, rootFlag, flags []string) string {
		args := append(append(append(append(slices.Clone(base), "--scope", scope), rootFlag...), flags...), tail...)
		return errs.ShellJoin(args)
	}
	for _, si := range spec.Scopes {
		var root []string
		if si.Scope.ProjectDir != "" && (len(spec.Scopes) > 1 || !fl.eng.FoundHere(si.Scope)) {
			root = []string{"-C", untrusted.Text(si.Scope.ProjectDir, untrusted.MaxName)}
		}
		out = append(out, line(si.Scope.Kind.String(), root, per(si)))
	}
	return out
}
