package cli

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

func (s style) paint(code, text string) string {
	if !s.color {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (s style) ok(t string) string   { return s.paint("32", "✓ "+t) }
func (s style) warn(t string) string { return s.paint("33", "⚠ "+t) }
func (s style) bad(t string) string  { return s.paint("31", "✗ "+t) }
func (s style) info(t string) string { return s.paint("36", "→ "+t) }

// strong is bold, which is emphasis, not color: NO_COLOR and LOLA_COLOR=0 keep it, --plain does
// not.
func (s style) strong(t string) string {
	if s.plain {
		return t
	}
	return "\x1b[1m" + t + "\x1b[22m"
}

// memberShown caps a JSON array member, such as a hook group, in a step line; the full member
// is in the JSON output.
const memberShown = 120

// stepLine renders one plan step.
func stepLine(s style, st plan.Step) string {
	// Paths and keys can carry a module's file and server names: printed safely here, and kept
	// raw in the result, where lola migrate reads them as paths.
	dest := untrusted.Text(st.Path, len(st.Path))
	if st.Kind == plan.KindDirectory {
		dest += "/"
	}
	if st.Key != "" {
		dest += " " + untrusted.Text(st.Key, len(st.Key))
	}
	if st.Value != "" {
		dest += " " + strconv.Quote(st.Value)
	}
	if st.Member != nil {
		dest += " " + untrusted.Text(string(st.Member), memberShown)
	}
	if st.Via != "" {
		dest += " (via symlink → " + untrusted.Text(st.Via, len(st.Via)) + ")"
	}
	forced := st.Forced
	if st.Replaced {
		forced = "replaced"
	}
	if s.plain {
		return strings.Join([]string{string(st.Action), st.Module, st.Target, st.Kind, dest, forced, st.Note}, "\t")
	}
	line := fmt.Sprintf("%-6s %-11s %s", st.Action, st.Target, dest)
	switch {
	case st.Replaced && st.Action == plan.ActionAdopt:
		line += " [identical to a file lola did not write; kept, not rewritten]"
	case st.Replaced:
		line += " [replaces a file lola did not write]"
	case st.Forced != "":
		line += " [forced: " + st.Forced + "]"
	}
	if st.Note != "" {
		line += " (" + st.Note + ")"
	}
	switch st.Action {
	case plan.ActionDelete, plan.ActionForget:
		return s.info(line)
	case plan.ActionAbsent:
		return s.warn(line)
	default:
		return s.ok(line)
	}
}

func noticeLine(s style, n engine.Notice) string {
	if s.plain && n.Scope != "" {
		return "notice\t" + n.Level + "\t" + n.Text + "\t" + n.Scope
	}
	if s.plain {
		return "notice\t" + n.Level + "\t" + n.Text
	}
	line := s.info(n.Text)
	if n.Level == engine.LevelWarn {
		line = s.warn(n.Text)
	}
	for _, h := range n.Hints {
		line += "\n" + hintLine("    ", h, s.width)
	}
	return line
}

// findingLine renders a warn or skip finding, with its hint; info findings print only as JSON
// or in lola check.
func findingLine(s style, f target.Finding) string {
	if s.plain {
		return strings.Join([]string{"finding", string(f.Severity), f.Module, f.Target, f.Kind, f.Artifact, f.Code, f.Text}, "\t")
	}
	var line string
	switch f.Severity {
	case target.SeverityWarn:
		line = s.warn(f.Describe())
	case target.SeveritySkip:
		line = s.bad("skipped " + f.Describe())
	case target.SeverityInfo:
		line = s.info(f.Describe())
	default:
		return ""
	}
	if f.Hint != "" {
		line += "\n" + hintLine("    ", f.Hint, s.width)
	}
	return line
}

// printCheck renders a lola check report: every finding, including info ones, each module's
// notices (what its picks leave out that it mentions), and a line for each target that renders a
// module without loss.
func printCheck(w io.Writer, s style, rep *engine.CheckReport) {
	for _, f := range rep.Findings {
		fmt.Fprintln(w, checkLine(s, f))
	}
	for _, m := range rep.Modules {
		printExcluded(w, s, m.Name, m.Skipped, m.SkippedIn, m.NotInOnly, m.OnlyIn, m.Summary, false)
		for _, n := range m.Notices {
			fmt.Fprintln(w, noticeLine(s, n))
		}
		for _, f := range m.Findings {
			if f.Target == "" {
				fmt.Fprintln(w, checkLine(s, f))
			}
		}
		for _, id := range m.Targets {
			clean := true
			for _, f := range m.Findings {
				if f.Target == id {
					clean = false
					fmt.Fprintln(w, checkLine(s, f))
				}
			}
			if clean && !s.plain {
				fmt.Fprintln(w, s.ok(m.Name+": "+id+" renders everything"))
			}
		}
	}
}

func checkLine(s style, f target.Finding) string {
	if f.Severity == target.SeverityInfo && !s.plain {
		line := s.info(f.Describe())
		if f.Hint != "" {
			line += "\n" + hintLine("    ", f.Hint, s.width)
		}
		return line
	}
	return findingLine(s, f)
}

// printInstall renders an install result; with more than one scope, steps are grouped and
// modules named per scope. A module whose lock.json entry it kept (Kept), writing none of its files
// and no entry for it, is already up to date: it says so, and counts the values its version drops,
// as a sync with nothing to write does (foldDropped), unless --verbose.
func printInstall(w io.Writer, s style, r *engine.InstallResult) {
	upToDate := func(m engine.ModuleResult) bool {
		return !r.DryRun && m.Kept && !slices.ContainsFunc(r.Steps, func(st plan.Step) bool { return st.Module == m.Name && st.Scope == m.Scope }) &&
			!slices.ContainsFunc(r.Edits, func(ed engine.Edit) bool { return ed.Module == m.Name })
	}
	printScoped(w, s, "installed", r.Result, r.Scopes, upToDate)
}

// printResult renders an install or uninstall result: steps, MCP transparency, install
// hooks, notices, and a summary line.
func printResult(w io.Writer, s style, verb string, r *engine.Result) {
	printScoped(w, s, verb, r, nil, nil)
}

// scopeLabel names a scope in human output.
func scopeLabel(sc engine.ScopeResult) string {
	if sc.Root != "" {
		return sc.Scope + " (" + untrusted.Text(sc.Root, untrusted.MaxName) + ")"
	}
	return sc.Scope
}

// printScoped renders a run's edits, plan, notices and closing lines; upToDate, nil for none, says
// which modules are already up to date (printInstall).
func printScoped(w io.Writer, s style, verb string, r *engine.Result, scopes []engine.ScopeResult, upToDate func(engine.ModuleResult) bool) {
	printEdits(w, s, r.Edits, r.DryRun)
	printPlan(w, s, r, scopes, "")
	res := *r
	var counts []string
	if upToDate != nil && !s.verbose && !s.plain {
		res.Findings, counts = foldDropped(r.Findings, func(name string) bool {
			return !slices.ContainsFunc(r.Modules, func(m engine.ModuleResult) bool { return m.Name == name && !upToDate(m) })
		})
	}
	printNotices(w, s, &res)
	for _, c := range counts {
		fmt.Fprintln(w, s.info(c))
	}
	if r.DryRun || s.plain {
		return
	}
	printDone(w, s, verb, r, scopes, upToDate)
}

// printEdits prints a run's write lines (editLine), or under --plain an edit row each: action,
// module, profile and path.
func printEdits(w io.Writer, s style, edits []engine.Edit, dry bool) {
	for _, ed := range edits {
		if s.plain {
			fmt.Fprintf(w, "edit\t%s\t%s\t%s\t%s\n", ed.Action, ed.Module, ed.Profile, ed.Path)
			continue
		}
		fmt.Fprintln(w, editLine(s, ed, dry))
	}
}

// editLine is a write line: "✓ added kit to profile go  (<path>)", the path
// for a file of yours (", new" when the run created it), the base or a project profile said shared
// when git tracks it, then the entry's only: count, the layer an override copied and where a move
// carried its picks from; under -n
// "→ would add …".
func editLine(s style, ed engine.Edit, dry bool) string {
	verb, prep := ed.Action, map[string]string{engine.EditAdded: "to", engine.EditChanged: "in", engine.EditRemoved: "from"}[ed.Action]
	label := ed.Label
	if ed.Shared {
		if l, ok := strings.CutSuffix(label, " (base)"); ok {
			label = l + " (base, shared: git tracks it)"
		} else {
			label += " (shared: git tracks it)"
		}
	}
	if dry {
		verb = "would " + map[string]string{engine.EditAdded: "add", engine.EditChanged: "change", engine.EditRemoved: "remove"}[ed.Action]
	}
	line := verb + " " + ed.Module + " " + prep + " " + label
	if ed.Kind != "project" && ed.Profile != "project.yml" {
		where := ed.Path
		if ed.Created {
			where += ", new"
		}
		line += "  (" + where + ")"
	}
	if ed.Only != "" {
		line += "  only: " + ed.Only
	}
	if ed.Overrides != "" {
		line += " (overrides " + ed.Overrides + "'s)"
	}
	if ed.Picks != "" {
		line += "  picks: " + ed.Picks
	}
	if dry {
		return s.info(line)
	}
	return s.ok(line)
}

// planHeader heads a plan that writes nothing: a dry run, or the plan the next question applies.
func planHeader(s style) string {
	if s.asking {
		return "plan (nothing written yet; the question below applies it):"
	}
	return "plan (dry run; nothing was written):"
}

// printPlan prints the steps and each module's details: what a run did, or in a dry run would do.
// Given more, the command that lists every step, the human steps of one scope stop at stepsShown
// per module, then say how many more.
func printPlan(w io.Writer, s style, r *engine.Result, scopes []engine.ScopeResult, more string) {
	if r.DryRun && !s.plain {
		fmt.Fprintln(w, planHeader(s))
	}
	if len(scopes) > 1 && !s.plain {
		for _, sc := range scopes {
			fmt.Fprintln(w, scopeLabel(sc)+":")
			for _, st := range r.Steps {
				if st.Scope == sc.Scope && st.Root == sc.Root {
					fmt.Fprintln(w, "  "+stepLine(s, st))
				}
			}
		}
	} else {
		indent := "  "
		if s.plain { // rows start at column 1, so cut -f1 reads the action
			indent = ""
		}
		capped := more != "" && !s.plain
		seen := map[string]int{} // steps so far per module; steps of two modules may interleave
		var over []string        // the modules past the cap, in the order they passed it
		for _, st := range r.Steps {
			seen[st.Module]++
			if !capped || seen[st.Module] <= stepsShown {
				fmt.Fprintln(w, indent+stepLine(s, st))
			} else if seen[st.Module] == stepsShown+1 {
				over = append(over, st.Module)
			}
		}
		for _, m := range over {
			fmt.Fprintf(w, "%s… and %d more for %s (%s lists them all)\n", indent, seen[m]-stepsShown, untrusted.Text(m, untrusted.MaxName), more)
		}
	}
	// A line two scopes would print alike (a profile both apply) is said once; a module whose lines
	// differ between scopes names the scope of each.
	excluded, lines := map[string]bool{}, map[string]map[string]bool{}
	skipLine := func(m engine.ModuleResult) string {
		return m.SkippedIn + "\x00" + m.OnlyIn + "\x00" + strings.Join(m.NotInOnly, "\x00") + "\x00" + strings.Join(m.Skipped, "\x00")
	}
	for _, m := range r.Modules {
		if len(m.Skipped) > 0 {
			if lines[m.Name] == nil {
				lines[m.Name] = map[string]bool{}
			}
			lines[m.Name][skipLine(m)] = true
		}
	}
	for _, m := range r.Modules {
		if key := m.Name + "\x00" + skipLine(m); !excluded[key] {
			excluded[key] = true
			label := m.Name
			if len(lines[m.Name]) > 1 {
				label += ", " + scopeLabel(engine.ScopeResult{Scope: m.Scope, Root: m.Root})
			}
			printExcluded(w, s, label, m.Skipped, m.SkippedIn, m.NotInOnly, m.OnlyIn, m.Summary, r.DryRun)
		}
		if len(m.MCPServers) > 0 && !s.plain {
			fmt.Fprintf(w, "%s MCP servers:\n", m.Name)
			for _, srv := range m.MCPServers {
				verb := "runs"
				if srv.Transport != "stdio" {
					verb = "connects to"
				}
				fmt.Fprintf(w, "    %s (%s) %s: %s\n", srv.Name, srv.Transport, verb, srv.Runs)
			}
		}
		printRuntimeHooks(w, s, m)
		for _, h := range m.Hooks {
			script := untrusted.Text(h.Script, len(h.Script))
			if s.plain {
				fmt.Fprintf(w, "hook\t%s\t%s\t%s\t%s\t%s\n", m.Name, h.Event, script, h.Target, h.Status)
				continue
			}
			status := h.Status
			if status == engine.HookWouldRun {
				status = "would run" // JSON and --plain keep the would-run value
			}
			line := fmt.Sprintf("%s %s hook %s (%s): %s", m.Name, h.Event, script, h.Target, status)
			switch h.Status {
			case engine.HookRan:
				fmt.Fprintln(w, s.ok(line))
			case engine.HookWouldRun:
				fmt.Fprintln(w, s.info(line))
			default:
				fmt.Fprintln(w, s.warn(line))
			}
		}
	}
	printScripts(w, s, r.Scripts)
}

// printScripts lists the install scripts of a run in run order: each with its module,
// event, source and target, and its status; a dry run's project script the run has no trust for
// says it needs --trust=scripts. A --plain row is "script", module, event, source, path, target
// and status.
func printScripts(w io.Writer, s style, scripts []engine.ScriptRun) {
	for _, sr := range scripts {
		if s.plain {
			fmt.Fprintf(w, "script\t%s\t%s\t%s\t%s\t%s\t%s\n", sr.Module, sr.Event, sr.Source, sr.Path, sr.Target, sr.Status)
			continue
		}
		status := sr.Status
		if status == engine.HookWouldRun {
			status = "would run" // JSON and --plain keep the would-run value
		}
		if sr.NeedsTrust() {
			status += ", needs " + engine.TrustScripts.Arg()
		}
		source := sr.Source
		if sr.Root() != "" {
			source += " at " + sr.Root() // the path is from that root
		}
		line := fmt.Sprintf("%s %s script %s (%s, %s): %s", sr.Module, sr.Event, sr.Path, source, sr.Target, status)
		switch sr.Status {
		case engine.HookRan:
			fmt.Fprintln(w, s.ok(line))
		case engine.HookWouldRun:
			fmt.Fprintln(w, s.info(line))
		default:
			fmt.Fprintln(w, s.warn(line))
		}
	}
}

// printDone prints the closing "✓ <verb> <module>" line of each module a run wrote, and
// "✓ <module> already up to date" for each upToDate reports (nil for none).
func printDone(w io.Writer, s style, verb string, r *engine.Result, scopes []engine.ScopeResult, upToDate func(engine.ModuleResult) bool) {
	for _, m := range r.Modules {
		if m.Rewritten { // its rewrite notice reports it
			continue
		}
		where := ""
		if len(scopes) > 1 {
			where = " at " + m.Scope + " scope"
		}
		if upToDate != nil && upToDate(m) {
			fmt.Fprintln(w, s.ok(fmt.Sprintf("%s already up to date%s (%s)", m.Name, where, strings.Join(m.Targets, ", "))))
			continue
		}
		fmt.Fprintln(w, s.ok(fmt.Sprintf("%s %s%s (%s)", verb, m.Name, where, strings.Join(m.Targets, ", "))))
	}
}

// worthShowing reports whether a run prints f (printFindings): a warn or skip finding, an info
// finding with a hint, or one saying a target wrote no copy of a skill because the agents target
// delivers it, without which the run would name a target that got no file and say nothing why.
func worthShowing(f target.Finding) bool {
	return f.Severity != target.SeverityInfo || f.Hint != "" || f.Code == target.CodeSkillViaAgents
}

// printFindings prints the findings worth showing (worthShowing). Under --plain every finding is
// a row; otherwise groupMin or more findings that differ only in their artifact print as one line.
func printFindings(w io.Writer, s style, findings []target.Finding) {
	var shown []target.Finding
	for _, f := range findings {
		if !worthShowing(f) {
			continue
		}
		shown = append(shown, f)
	}
	groups := make([]findingGroup, 0, len(shown))
	if s.plain {
		for _, f := range shown {
			groups = append(groups, findingGroup{Findings: []target.Finding{f}})
		}
	} else {
		groups = groupFindings(shown)
	}
	for _, g := range groups {
		if len(g.Findings) >= groupMin {
			fmt.Fprintln(w, groupLine(s, g))
			continue
		}
		for _, f := range g.Findings {
			if line := findingLine(s, f); line != "" {
				fmt.Fprintln(w, line)
			}
		}
	}
}

// printNotices prints the findings and notices of an install, sync, update or profile switch:
// safety ones in full, as printFindings and noticeLine do; detail ones (target.ClassDetail) in full
// under -v or --plain, else as one line per module and lola check (a scope's), in the order they
// first appear: "<module>: 30 notices (claude-code 15, gemini-cli 15) — <the lola check> -t
// claude-code,gemini-cli", -t naming the targets counted. A detail notice with no target counts as
// "module". Detail that no lola check names (its Check is ""), and a dropped value another era of
// its target keeps (KeptSince), is printed in full. A hint for the
// module's author (target.Finding.AuthorHint) is never printed here, only what the engine added for
// the user: lola check prints the rest.
func printNotices(w io.Writer, s style, r *engine.Result) {
	findings := make([]target.Finding, 0, len(r.Findings))
	for _, f := range r.Findings {
		if f.AuthorHint {
			f.Hint = f.UserHint
		}
		findings = append(findings, f)
	}
	if s.plain || s.verbose {
		printFindings(w, s, findings)
		for _, n := range r.Notices {
			fmt.Fprintln(w, noticeLine(s, n))
		}
		return
	}
	type key struct{ module, check string }
	type tally struct {
		total   int
		targets []string // in the order they first appear
		count   map[string]int
	}
	var order []key
	tallies := map[key]*tally{}
	add := func(k key, target string) {
		t := tallies[k]
		if t == nil {
			t = &tally{count: map[string]int{}}
			tallies[k] = t
			order = append(order, k)
		}
		if t.count[target] == 0 && target != "" {
			t.targets = append(t.targets, target)
		}
		t.total++
		t.count[target]++
	}
	var shown []target.Finding
	for _, f := range findings {
		switch {
		case f.Class != target.ClassDetail || f.Module == "" || f.Check == "" || f.KeptSince != "":
			shown = append(shown, f)
		case worthShowing(f): // the ones printFindings would print
			add(key{f.Module, f.Check}, f.Target)
		}
	}
	printFindings(w, s, shown)
	for _, n := range r.Notices {
		if n.Class == target.ClassDetail && n.Module != "" && n.Check != "" {
			add(key{n.Module, n.Check}, "")
			continue
		}
		fmt.Fprintln(w, noticeLine(s, n))
	}
	for _, k := range order {
		t := tallies[k]
		counts := make([]string, 0, len(t.targets)+1)
		for _, id := range t.targets {
			counts = append(counts, fmt.Sprintf("%s %d", id, t.count[id]))
		}
		if n := t.count[""]; n > 0 {
			counts = append(counts, fmt.Sprintf("module %d", n))
		}
		noun := "notices"
		if t.total == 1 {
			noun = "notice"
		}
		check := k.check
		if len(t.targets) > 0 {
			check += " -t " + strings.Join(t.targets, ",")
		}
		fmt.Fprintln(w, s.info(fmt.Sprintf("%s: %d %s (%s) — %s", untrusted.Text(k.module, untrusted.MaxName), t.total, noun, strings.Join(counts, ", "), check)))
	}
}

// printRuntimeHooks lists a module's runtime hooks per target with their consent status. A
// --plain row is module, target, event, type, status and the component whose frontmatter has
// the hook ("" for settings hooks); an agent's inline MCP server has "mcp_server:<name>" for
// its event and its transport for its type.
func printRuntimeHooks(w io.Writer, s style, m engine.ModuleResult) {
	if s.plain {
		for _, h := range m.RuntimeHooks {
			event := h.Event
			if h.MCPServer != "" {
				event = "mcp_server:" + h.MCPServer
			}
			fmt.Fprintf(w, "runtime-hook\t%s\t%s\t%s\t%s\t%s\t%s\n", m.Name, h.Target, event, h.Type, h.Status, h.Component)
		}
		return
	}
	for i, h := range m.RuntimeHooks {
		if i == 0 || h.Target != m.RuntimeHooks[i-1].Target {
			fmt.Fprintf(w, "%s runtime hooks for %s (%s):\n", m.Name, h.Target, h.Status)
		}
		for _, line := range runtimeHookLines(h) {
			fmt.Fprintln(w, "    "+line)
		}
	}
}

// picksText is a skip: list shown as the picks a non-exact --only keeps, then how many others are
// skipped.
func picksText(sum *engine.PicksSummary) string {
	return "only (not exact): " + strings.Join(sum.Picks, ", ") + "; " + strconv.Itoa(sum.Others) + " others skipped"
}

// printExcluded names a module's components that the picks exclude: those a skip: leaves out, in
// in (selections.yaml, or a profile as the engine labels it, safe to print), and those only the
// only: list at onlyIn leaves out (notInOnly, a subset of skipped); a dry run saved no edit, so it
// says they would be. sum, when set and no only: list is in force, replaces the skips with the picks
// they leave. A --plain row is "skipped", the module and the component (kind/name).
func printExcluded(w io.Writer, s style, module string, skipped []string, in string, notInOnly []string, onlyIn string, sum *engine.PicksSummary, dry bool) {
	if len(skipped) == 0 {
		return
	}
	names := func(cs []string) []string {
		out := make([]string, len(cs))
		for i, c := range cs {
			out[i] = untrusted.Text(c, untrusted.MaxName)
		}
		return out
	}
	if s.plain {
		for _, c := range names(skipped) {
			fmt.Fprintf(w, "skipped\t%s\t%s\n", module, c)
		}
		return
	}
	verb := "skipped"
	if dry {
		verb = "would be skipped"
	}
	if sum != nil && len(notInOnly) == 0 {
		others := strconv.Itoa(sum.Others) + " others skipped"
		if dry {
			others = strconv.Itoa(sum.Others) + " others would be skipped"
		}
		fmt.Fprintln(w, s.info(module+": only (not exact) in "+in+": "+strings.Join(sum.Picks, ", ")+"; "+others))
		return
	}
	if rest := slices.DeleteFunc(slices.Clone(skipped), func(c string) bool { return slices.Contains(notInOnly, c) }); len(rest) > 0 {
		fmt.Fprintln(w, s.info(module+": "+verb+" in "+in+": "+strings.Join(names(rest), ", ")))
	}
	if len(notInOnly) > 0 {
		fmt.Fprintln(w, s.info(module+": "+strings.Replace(verb, "skipped", "not in only:", 1)+" ("+onlyIn+"): "+strings.Join(names(notInOnly), ", ")))
	}
}
