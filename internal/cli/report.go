package cli

import (
	"cmp"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// report is one run's human report, which printReport renders: the engine's result and what the
// command adds.
type report struct {
	Result *engine.Result
	// Verb is each outcome line's past tense: installed, uninstalled, synced or updated.
	Verb string
	// Scopes is an install's; with more than one, its files are listed under each.
	Scopes []engine.ScopeResult
	// UpToDate says which modules an install left as they were; nil for none.
	UpToDate func(engine.ModuleResult) bool
	// Sync marks a sync, update or profile switch: the modules in Changed changed and the rest are
	// counted as unchanged. Update marks lola update, whose pin moves read "<was> → <now>".
	Sync, Update bool
	Changed      []string
	Pins         []engine.Pin
	Removals     []engine.Removal
	// InSync, when set, is the line closing a run that changes nothing ("already in sync").
	InSync string
	// Listed is the modules the trust diff above the report listed with their MCP servers.
	Listed []string
	// ToApply and OffTerminal close a dry run: the command that applies it, and the one that does
	// off a terminal.
	ToApply, OffTerminal string
	// More is the command that lists every step of a module whose steps are capped (stepsShown).
	More string
	// NoticesOnly renders the sections about safety and notices alone: the install review's block.
	NoticesOnly bool
	// Reviewed is the planned result the install review showed before Apply in this run; nil when
	// none was shown. The new warnings, skips and runtime hooks it showed fold into one line
	// (printReviewed), so the report ends on its outcome.
	Reviewed *engine.Result
	// Alone marks the report as the whole run's (lola install, sync or update), not a part of a
	// run that writes more, such as a profile split: as a dry run that would change nothing
	// (nothingToDo), it prints only its outcome.
	Alone bool
	// CacheServed is lola update's modules served from your cache without checking their source.
	// A run in sync says so as its InSync line (the line closing it, with the run that checks);
	// any other run adds it after its outcome.
	CacheServed *engine.CacheServed
}

// printReport renders a run's report. Under --plain it prints every row (printPlainReport).
// Otherwise its sections in order, each under a header when it has content, after planHeader in a
// dry run that writes: the runtime hooks, warnings and skips new to the user, in full, a target's
// declined settings hooks ending with their warning's hint in its place (declinedGroup); install
// hooks and scripts; MCP servers the trust diff did not list; notices; one fold of everything seen
// before; the files, a fold per module; the removals, and what declined runtime hooks remove
// (engine.DeclinedRemovals), Removed (To remove in a plan); the outcome;
// the fold footer while a fold is closed; and a dry run's to apply: lines. A run that changes nothing (InSync) and the review's
// block (NoticesOnly) leave out what a run writes, and a dry run that would change nothing
// (nothingToDo), with no warning or skip new to the user and no warning among its notices
// (warnsInNotices), and under -v no finding, notice or runtime hook, prints only its outcome.
func printReport(w io.Writer, s style, rep report) {
	if s.plain {
		printPlainReport(w, s, rep)
		return
	}
	r := rep.Result
	findings, counts := reportFindings(s, rep)
	hints := map[string]string{}
	findings = slices.DeleteFunc(findings, func(f target.Finding) bool {
		if !declinedGroup(f) || !slices.ContainsFunc(r.Modules, func(m engine.ModuleResult) bool {
			return m.Name == f.Module && (f.Scope == "" || f.Scope == m.Scope) && slices.ContainsFunc(m.RuntimeHooks, func(h engine.RuntimeHook) bool {
				return h.Target == f.Target && h.Status == engine.RuntimeHooksDeclined
			})
		}) {
			return false // no hook block of the run prints its hint: the warning stays
		}
		hints[f.Scope+"\x00"+f.Module+"\x00"+f.Target] = f.Hint
		return true
	})
	var reviewedFinds map[string]bool
	var reviewedHooks map[string]bool
	if rep.Reviewed != nil {
		reviewedFinds, reviewedHooks = map[string]bool{}, shownHooks(rep.Reviewed)
		for _, f := range rep.Reviewed.Findings {
			reviewedFinds[findingKey(f)] = true
		}
	}
	var newWarn, newSkip, seen, rest, reviewed []target.Finding
	for _, f := range findings {
		switch {
		case cmp.Or(f.Class, target.ClassSafety) != target.ClassSafety || f.Severity == target.SeverityInfo:
			rest = append(rest, f)
		case f.New != nil && !*f.New:
			seen = append(seen, f)
		case reviewedFinds[findingKey(f)] && (f.Severity == target.SeverityWarn || f.Severity == target.SeveritySkip):
			reviewed = append(reviewed, f)
		case f.Severity == target.SeverityWarn:
			newWarn = append(newWarn, f)
		case f.Severity == target.SeveritySkip:
			newSkip = append(newSkip, f)
		}
	}
	// -v prints every finding, notice and runtime hook in full, so under it they are something to show.
	verboseShows := s.verbose && (len(findings)+len(r.Notices) > 0 ||
		slices.ContainsFunc(r.Modules, func(m engine.ModuleResult) bool { return len(m.RuntimeHooks) > 0 }))
	if len(newWarn)+len(newSkip) == 0 && !warnsInNotices(rest, r.Notices) && !verboseShows && nothingToDo(rep) {
		if rep.InSync != "" {
			printInSync(w, s, rep, r.DryRun)
			return
		}
		for _, m := range r.Modules {
			fmt.Fprintln(w, s.info(upToDateLine(rep, m)))
		}
		return
	}
	// New runtime hooks print in full, under their own header or in the review above it (the
	// review's own summary, NoticesOnly, prints that header too); under -v the seen ones too.
	for _, m := range r.Modules {
		for _, h := range m.RuntimeHooks {
			if hookIsNew(h) || s.verbose {
				s.saw(h.ShownKey(m.Name))
			}
		}
	}
	writes := !rep.NoticesOnly && rep.InSync == ""
	if r.DryRun && writes {
		fmt.Fprintln(w, planHeader(s))
	}
	folded := 0
	section := func(title string, body func(io.Writer)) {
		var b reportBuf
		body(&b)
		if len(b.blocks) == 0 {
			return
		}
		fmt.Fprintln(w, s.strong(title+":"))
		folded += b.folds()
		b.ReplayTo(w)
	}
	inReview := func(m engine.ModuleResult) func(engine.RuntimeHook) bool {
		return func(h engine.RuntimeHook) bool { return hookIsNew(h) && reviewedHooks[hookKey(m.Name, h)] }
	}
	if !rep.NoticesOnly {
		printReviewed(w, s, r.Modules, reviewed, inReview, hints, &folded)
	}
	section("Runtime hooks — new", func(b io.Writer) {
		for _, m := range r.Modules {
			unreviewed := func(h engine.RuntimeHook) bool { return hookIsNew(h) && !inReview(m)(h) }
			for _, l := range runtimeHookBlock(s, m, unreviewed, hints) {
				fmt.Fprintln(b, l)
			}
		}
	})
	section("Warnings — new", func(b io.Writer) { printFindings(b, s, newWarn) })
	section("Skipped — new", func(b io.Writer) { printFindings(b, s, newSkip) })
	if !rep.NoticesOnly { // a sync in sync lists the project scripts it checks, which it shows
		section("Hooks and scripts", func(b io.Writer) { printInstallHooks(b, s, r) })
	}
	if writes {
		section("MCP servers", func(b io.Writer) { printMCPServers(b, r.Modules, rep.Listed) })
	}
	section("Notices", func(b io.Writer) {
		printNotices(b, s, &engine.Result{Findings: rest, Notices: r.Notices})
		for _, c := range counts {
			fmt.Fprintln(b, s.info(c))
		}
	})
	section("Unchanged since last run", func(b io.Writer) { printSeen(b, s, r.Modules, seen, hints) })
	if writes {
		section("Files", func(b io.Writer) { printFileFolds(b, s, rep) })
		removed := "Removed"
		if r.DryRun || s.asking {
			removed = "To remove"
		}
		section(removed, func(b io.Writer) {
			printRemovalLines(b, s, append(slices.Clone(rep.Removals), engine.DeclinedRemovals(r)...))
		})
	}
	if !rep.NoticesOnly {
		printOutcome(w, s, rep)
		if rep.InSync == "" && rep.CacheServed != nil {
			c := rep.CacheServed
			fmt.Fprintln(w, s.ok(fmt.Sprintf("%d %s %s", c.Modules, map[bool]string{true: "module", false: "modules"}[c.Modules == 1], cacheServedText(c))))
			fmt.Fprintln(w, s.info(c.Command))
		}
	}
	if folded > 0 {
		printFoldFooter(w, s)
	}
	if r.DryRun && rep.ToApply != "" {
		fmt.Fprintln(w, s.info("to apply: "+rep.ToApply))
		if rep.OffTerminal != "" {
			fmt.Fprintln(w, s.info("off a terminal: "+rep.OffTerminal))
		}
	}
}

// nothingToDo reports whether rep is a whole run's (Alone) dry run that would change nothing: no command to apply it,
// no step, edit, pin or removal, the sync in sync (InSync) or every module of the install up to date, and no runtime hook
// new to the user: granted, which the run would record, or declined and not seen so before, and
// no install hook or project script lola has not shown.
func nothingToDo(rep report) bool {
	r := rep.Result
	if !r.DryRun || !rep.Alone || rep.ToApply != "" || len(r.Steps)+len(r.Edits)+len(rep.Pins)+len(rep.Removals) > 0 ||
		rep.InSync == "" && (rep.UpToDate == nil || len(r.Modules) == 0) {
		return false
	}
	for _, m := range r.Modules {
		if slices.ContainsFunc(m.RuntimeHooks, hookIsNew) || slices.ContainsFunc(m.Hooks, func(h engine.HookRun) bool { return unshown(h.New) }) ||
			rep.InSync == "" && !rep.UpToDate(m) {
			return false
		}
	}
	return !slices.ContainsFunc(r.Scripts, func(sr engine.ScriptRun) bool { return unshown(sr.New) })
}

// unshown reports whether a hook or script of a run is one lola has not shown (New true), which a
// dry run prints in full however little else it has to say.
func unshown(fresh *bool) bool { return fresh != nil && *fresh }

// warnsInNotices reports whether the Notices section would print a ⚠ or ✗ line: a warning or skip
// among findings, or a warning among notices, that it prints rather than counts per module
// (printNotices counts a detail one with a lola check to name).
func warnsInNotices(findings []target.Finding, notices []engine.Notice) bool {
	counted := func(class target.Class, module, check string) bool {
		return class == target.ClassDetail && module != "" && check != ""
	}
	return slices.ContainsFunc(findings, func(f target.Finding) bool {
		return f.Severity != target.SeverityInfo && (!counted(f.Class, f.Module, f.Check) || f.KeptSince != "")
	}) || slices.ContainsFunc(notices, func(n engine.Notice) bool {
		return n.Level == engine.LevelWarn && !counted(n.Class, n.Module, n.Check)
	})
}

// upToDateLine is the outcome of a module an install leaves as it was, without its glyph.
func upToDateLine(rep report, m engine.ModuleResult) string {
	where := ""
	if len(rep.Scopes) > 1 {
		where = " at " + m.Scope + " scope"
	}
	return m.Name + " already up to date" + where + " (" + strings.Join(m.Targets, ", ") + ")"
}

// reportFindings is the run's findings as a report shows them, each with the hint for whoever runs
// lola in place of one for the module's author (target.Finding.AuthorHint), and, unless -v, without
// the values another era keeps of the modules the run leaves as they were, which the returned
// lines count (foldDropped).
func reportFindings(s style, rep report) ([]target.Finding, []string) {
	r := rep.Result
	findings := make([]target.Finding, 0, len(r.Findings))
	for _, f := range r.Findings {
		if f.AuthorHint {
			f.Hint = f.UserHint
		}
		findings = append(findings, f)
	}
	if s.verbose || rep.InSync == "" && rep.UpToDate == nil {
		return findings, nil
	}
	return foldDropped(findings, func(name string) bool {
		return rep.InSync != "" || !slices.ContainsFunc(r.Modules, func(m engine.ModuleResult) bool { return m.Name == name && !rep.UpToDate(m) })
	})
}

// declinedGroup reports whether f is the warning that a target's settings hooks were declined
// (engine's runtime-hooks-declined with no component): a report whose run lists those hooks shows
// them in their block, which ends with the warning's hint, in its place.
func declinedGroup(f target.Finding) bool {
	return strings.HasSuffix(f.Code, "/runtime-hooks-declined") && f.Artifact == ""
}

// runtimeHookBlock is the lines of m's runtime hooks that keep takes: per target, settings file
// and status a header, "caveman: 4 runtime hooks for claude-code in .claude/settings.json
// (granted)", then each hook (runtimeHookLines), indented, and after declined ones the hint of
// their declinedGroup warning, hints by the finding's scope ("" in a run of one), module and target.
func runtimeHookBlock(s style, m engine.ModuleResult, keep func(engine.RuntimeHook) bool, hints map[string]string) []string {
	hooks := slices.DeleteFunc(slices.Clone(m.RuntimeHooks), func(h engine.RuntimeHook) bool { return !keep(h) })
	var out []string
	for i := 0; i < len(hooks); {
		j := i
		for j < len(hooks) && hooks[j].Target == hooks[i].Target && hooks[j].Settings == hooks[i].Settings && hooks[j].Status == hooks[i].Status &&
			hooks[j].DeclinedEarlier == hooks[i].DeclinedEarlier {
			j++
		}
		status := hooks[i].Status
		if hooks[i].DeclinedEarlier {
			status = "declined earlier"
		}
		out = append(out, fmt.Sprintf("%s: %s for %s in %s (%s)", untrusted.Text(m.Name, untrusted.MaxName), counted(j-i, "runtime hook"),
			hooks[i].Target, untrusted.Text(hooks[i].Settings, untrusted.MaxName), status))
		for _, h := range hooks[i:j] {
			for _, l := range runtimeHookLines(h) {
				out = append(out, foldDetail+l)
			}
		}
		hint := hints[m.Scope+"\x00"+m.Name+"\x00"+hooks[i].Target] // a run of more than one scope names each finding's
		hint = cmp.Or(hint, hints["\x00"+m.Name+"\x00"+hooks[i].Target])
		if hint != "" && hooks[i].Status == engine.RuntimeHooksDeclined {
			out = append(out, hintLine(foldDetail, hint, s.width))
		}
		i = j
	}
	return out
}

// counted is n of noun: "1 change", "3 changes".
func counted(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// printInstallHooks lists each module's install hooks, each script with its sha256, with what
// became of them, then the install scripts (printScripts).
func printInstallHooks(w io.Writer, s style, r *engine.Result) {
	bodies := map[string]bool{}
	for _, m := range r.Modules {
		for _, h := range m.Hooks {
			status := h.Status
			if status == engine.HookWouldRun {
				status = "would run" // JSON and --plain keep the would-run value
			}
			line := fmt.Sprintf("%s %s hook %s (sha256 %s, %s): %s", m.Name, h.Event, untrusted.Text(h.Script, len(h.Script)), h.SHA256, h.Target, status)
			switch h.Status {
			case engine.HookRan:
				fmt.Fprintln(w, s.ok(line))
			case engine.HookWouldRun:
				fmt.Fprintln(w, s.info(line))
			default:
				fmt.Fprintln(w, s.warn(line))
			}
			printBody(w, s, bodies, untrusted.Text(h.Script, len(h.Script)), hookBody(h.HookScript))
		}
	}
	printScripts(w, s, r.Scripts)
}

// printMCPServers lists the MCP servers of each module the trust diff did not list (listed).
func printMCPServers(w io.Writer, mods []engine.ModuleResult, listed []string) {
	for _, m := range mods {
		if len(m.MCPServers) == 0 || slices.Contains(listed, m.Name) {
			continue
		}
		fmt.Fprintf(w, "%s MCP servers:\n", m.Name)
		for _, srv := range m.MCPServers {
			verb := "runs"
			if srv.Transport != "stdio" {
				verb = "connects to"
			}
			fmt.Fprintf(w, "    %s (%s) %s: %s\n", srv.Name, srv.Transport, verb, srv.Runs)
		}
	}
}

// printReviewed is one fold of what the install review showed before Apply in this run: summary
// "2 warnings, 1 skip, 2 runtime hooks shown in the review above"; detail, the runtime hooks of
// each module that shown takes (runtimeHookBlock, with hints), then the findings, grouped. It adds
// the fold to folded; it prints nothing when the review showed none of them.
func printReviewed(w io.Writer, s style, mods []engine.ModuleResult, finds []target.Finding, shown func(engine.ModuleResult) func(engine.RuntimeHook) bool,
	hints map[string]string, folded *int) {
	var detail []string
	nhook := 0
	for _, m := range mods {
		detail = append(detail, runtimeHookBlock(s, m, shown(m), hints)...)
		nhook += len(slices.DeleteFunc(slices.Clone(m.RuntimeHooks), func(h engine.RuntimeHook) bool { return !shown(m)(h) }))
	}
	warn, skip := 0, 0
	for _, f := range finds {
		switch f.Severity {
		case target.SeverityWarn:
			warn++
		case target.SeveritySkip:
			skip++
		}
	}
	if warn+skip+nhook == 0 {
		return
	}
	for _, g := range groupFindings(finds) {
		detail = append(detail, groupLines(s, g)...)
	}
	printFold(w, s, tally(warn, skip, nhook)+" shown in the review above", detail)
	*folded++
}

// tally counts warnings, skips and runtime hooks for a fold's summary: "2 warnings, 1 skip, 4
// runtime hooks", leaving out each that is 0.
func tally(warn, skip, nhook int) string {
	var parts []string
	for _, p := range []struct {
		n    int
		noun string
	}{{warn, "warning"}, {skip, "skip"}, {nhook, "runtime hook"}} {
		if p.n > 0 {
			parts = append(parts, counted(p.n, p.noun))
		}
	}
	return strings.Join(parts, ", ")
}

// hookKey identifies module's runtime hook h as shown: its target and its lines (runtimeHookLines),
// whatever its consent status.
func hookKey(module string, h engine.RuntimeHook) string {
	return module + "\x00" + h.Target + "\x00" + strings.Join(runtimeHookLines(h), "\n")
}

// shownHooks is the hookKey of each runtime hook r's report shows in full: those new to the user
// (hookIsNew), not those its fold of what was seen before counts.
func shownHooks(r *engine.Result) map[string]bool {
	out := map[string]bool{}
	for _, m := range r.Modules {
		for _, h := range m.RuntimeHooks {
			if hookIsNew(h) {
				out[hookKey(m.Name, h)] = true
			}
		}
	}
	return out
}

// findingKey identifies a finding as a report shows it: what Describe prints, its code and severity,
// and its hint for whoever runs lola (reportFindings), so one whose hint changed is another.
func findingKey(f target.Finding) string {
	hint := f.Hint
	if f.AuthorHint {
		hint = f.UserHint
	}
	return f.Describe() + "\x00" + f.Code + "\x00" + string(f.Severity) + "\x00" + hint
}

// printSeen is one fold of what the run shows that lola recorded for its module before: seen
// findings (target.Finding.New false) and runtime hooks (hookIsNew), those declined with the hints
// runtimeHookBlock takes. Its summary counts them, the runtime hooks declined earlier, which no
// question asked about again, apart (seenTally); its detail, per module, counts that module's and
// says where to read them again — lola info when it has seen runtime hooks or its findings name no
// lola check, else that check — then each in full.
func printSeen(w io.Writer, s style, mods []engine.ModuleResult, seen []target.Finding, hints map[string]string) {
	type group struct {
		name, check, info         string
		finds                     []target.Finding
		hooks                     []string
		warn, skip, nhook, nearly int // nearly: runtime hooks declined earlier, not asked again
	}
	var groups []*group
	find := func(name string) *group {
		for _, g := range groups {
			if g.name == name {
				return g
			}
		}
		g := &group{name: name}
		if i := slices.IndexFunc(mods, func(m engine.ModuleResult) bool { return m.Name == name }); i >= 0 {
			g.info = mods[i].Info
		}
		groups = append(groups, g)
		return g
	}
	for _, f := range seen {
		g := find(f.Module)
		g.finds = append(g.finds, f)
		g.check = cmp.Or(g.check, f.Check)
		if f.Severity == target.SeveritySkip {
			g.skip++
		} else {
			g.warn++
		}
	}
	for _, m := range mods {
		old := func(h engine.RuntimeHook) bool { return !hookIsNew(h) }
		if lines := runtimeHookBlock(s, m, old, hints); len(lines) > 0 {
			g := find(m.Name)
			g.hooks = append(g.hooks, lines...)
			for _, h := range m.RuntimeHooks {
				switch {
				case !old(h):
				case h.DeclinedEarlier:
					g.nearly++
				default:
					g.nhook++
				}
			}
		}
	}
	if len(groups) == 0 {
		return
	}
	var warn, skip, nhook, nearly int
	var detail []string
	for _, g := range groups {
		warn, skip, nhook, nearly = warn+g.warn, skip+g.skip, nhook+g.nhook, nearly+g.nearly
		line := untrusted.Text(g.name, untrusted.MaxName) + ": " + seenTally(g.warn, g.skip, g.nhook, g.nearly, "")
		pointer := g.check
		if g.nhook+g.nearly > 0 || pointer == "" {
			pointer = g.info
		}
		if pointer != "" {
			line += " — " + pointer
		}
		detail = append(detail, line)
		for _, fg := range groupFindings(g.finds) {
			for _, l := range groupLines(s, fg) {
				detail = append(detail, "  "+l)
			}
		}
		for _, l := range g.hooks {
			detail = append(detail, "  "+l)
		}
	}
	printFold(w, s, seenTally(warn, skip, nhook, nearly, " seen before"), detail)
}

// seenTally is printSeen's count: "2 warnings, 1 runtime hook" and seen after it ("seen before"
// in the summary), then the runtime hooks declined earlier, which no question asked about again:
// "; 1 runtime hook declined earlier".
func seenTally(warn, skip, nhook, nearly int, seen string) string {
	var parts []string
	if t := tally(warn, skip, nhook); t != "" {
		parts = append(parts, t+seen)
	}
	if nearly > 0 {
		parts = append(parts, counted(nearly, "runtime hook")+" declined earlier")
	}
	return strings.Join(parts, "; ")
}

// printFileFolds lists what the run writes: its edits (printEdits) in full; then per module a fold
// of its steps, "kit: 12 changes (claude-code 6, opencode 6)" over a step line each, under a line
// naming each scope of an install of more than one; then what each module's picks leave out.
func printFileFolds(w io.Writer, s style, rep report) {
	r := rep.Result
	printEdits(w, s, r.Edits, r.DryRun)
	if len(rep.Scopes) > 1 {
		for _, sc := range rep.Scopes {
			fmt.Fprintln(w, scopeLabel(sc)+":")
			printStepFolds(w, s, slices.DeleteFunc(slices.Clone(r.Steps), func(st plan.Step) bool { return st.Scope != sc.Scope || st.Root != sc.Root }), "")
		}
	} else {
		printStepFolds(w, s, r.Steps, rep.More)
	}
	once := excludedOnce(r)
	for _, m := range r.Modules {
		if label, ok := once(m); ok {
			printExcluded(w, s, label, m.Skipped, m.SkippedIn, m.NotInOnly, m.OnlyIn, m.Summary, r.DryRun)
		}
	}
}

// printStepFolds folds steps per module, in the order modules first appear. Given more, the command
// that lists every step, a module's detail stops at stepsShown and its last line says how many more.
func printStepFolds(w io.Writer, s style, steps []plan.Step, more string) {
	var order []string
	by := map[string][]plan.Step{}
	for _, st := range steps {
		if _, ok := by[st.Module]; !ok {
			order = append(order, st.Module)
		}
		by[st.Module] = append(by[st.Module], st)
	}
	for _, mod := range order {
		sts := by[mod]
		var targets []string
		n := map[string]int{}
		for _, st := range sts {
			if n[st.Target] == 0 {
				targets = append(targets, st.Target)
			}
			n[st.Target]++
		}
		counts := make([]string, 0, len(targets))
		for _, t := range targets {
			counts = append(counts, fmt.Sprintf("%s %d", cmp.Or(t, "module"), n[t]))
		}
		name := untrusted.Text(mod, untrusted.MaxName)
		shown := sts
		if more != "" && len(sts) > stepsShown {
			shown = sts[:stepsShown]
		}
		detail := make([]string, 0, len(shown)+1)
		for _, st := range shown {
			detail = append(detail, stepLine(s, st))
		}
		if len(shown) < len(sts) {
			detail = append(detail, fmt.Sprintf("… and %d more for %s (%s lists them all)", len(sts)-len(shown), name, more))
		}
		printFold(w, s, fmt.Sprintf("%s: %s (%s)", name, counted(len(sts), "change"), strings.Join(counts, ", ")), detail)
	}
}

// excludedOnce returns, for each module of r in turn, the label printExcluded names it by and
// whether its line is due: a line two scopes would print alike (a profile both apply) is said once,
// and a module whose lines differ between scopes names the scope of each.
func excludedOnce(r *engine.Result) func(engine.ModuleResult) (string, bool) {
	skipLine := func(m engine.ModuleResult) string {
		return m.SkippedIn + "\x00" + m.OnlyIn + "\x00" + strings.Join(m.NotInOnly, "\x00") + "\x00" + strings.Join(m.Skipped, "\x00")
	}
	lines := map[string]map[string]bool{}
	for _, m := range r.Modules {
		if len(m.Skipped) > 0 {
			if lines[m.Name] == nil {
				lines[m.Name] = map[string]bool{}
			}
			lines[m.Name][skipLine(m)] = true
		}
	}
	said := map[string]bool{}
	return func(m engine.ModuleResult) (string, bool) {
		key := m.Name + "\x00" + skipLine(m)
		if said[key] {
			return "", false
		}
		said[key] = true
		label := m.Name
		if len(lines[m.Name]) > 1 {
			label += ", " + scopeLabel(engine.ScopeResult{Scope: m.Scope, Root: m.Root})
		}
		return label, true
	}
}

// printOutcome is the report's closing lines. A run that changed nothing says so (InSync). A dry
// run lists the pins it would write (printPins) and "→ <module> already up to date" for each an
// install would leave as it is. Otherwise "✓ <verb> <module>"
// for each module the run wrote, with its scope when an install had more than one and its new pin
// (pinText); a pin of no such module on its own line; "✓ removed <module>" for each removed whole;
// "✓ <module> already up to date" for each an install left as it was; and how many a sync left
// unchanged. A module an install rewrote for another era is skipped: its notice reports it.
func printOutcome(w io.Writer, s style, rep report) {
	r := rep.Result
	switch {
	case rep.InSync != "":
		printInSync(w, s, rep, r.DryRun)
		return
	case r.DryRun:
		printPins(w, s, rep.Pins, rep.Update)
		for _, m := range r.Modules {
			if rep.UpToDate != nil && rep.UpToDate(m) {
				fmt.Fprintln(w, s.info(upToDateLine(rep, m)))
			}
		}
		return
	}
	pinned := map[string]bool{}
	var current []string
	unchanged := 0
	for _, m := range r.Modules {
		where := ""
		if len(rep.Scopes) > 1 {
			where = " at " + m.Scope + " scope"
		}
		targets := " (" + strings.Join(m.Targets, ", ") + ")"
		switch {
		case m.Rewritten:
		case rep.UpToDate != nil && rep.UpToDate(m):
			current = append(current, upToDateLine(rep, m))
		case rep.Sync && !slices.Contains(rep.Changed, m.Name):
			unchanged++
		default:
			line := rep.Verb + " " + m.Name + where
			if i := slices.IndexFunc(rep.Pins, func(p engine.Pin) bool { return p.Module == m.Name }); i >= 0 {
				line += pinText(rep.Pins[i], rep.Update)
				pinned[m.Name] = true
			}
			fmt.Fprintln(w, s.ok(line+targets))
		}
	}
	for _, p := range rep.Pins {
		if !pinned[p.Module] {
			fmt.Fprintln(w, s.ok(p.Module+pinText(p, rep.Update)))
		}
	}
	for _, rm := range rep.Removals {
		if rm.WholeModule {
			fmt.Fprintln(w, s.ok("removed "+rm.Module))
		}
	}
	for _, c := range current {
		fmt.Fprintln(w, s.ok(c))
	}
	if unchanged > 0 {
		fmt.Fprintln(w, s.ok(fmt.Sprintf("%d unchanged", unchanged)))
	}
}

// cacheServedText is what a run served from your cache says of those modules, after their count.
func cacheServedText(c *engine.CacheServed) string {
	return "up to date with your cache (fetched " + c.Fetched + ")"
}

// printInSync is the line closing a run that changes nothing, "→ <line>" in a dry run, "✓ <line>"
// otherwise; a run served from your cache says so with a ✓ either way, and the run that checks the
// source.
func printInSync(w io.Writer, s style, rep report, dry bool) {
	if c := rep.CacheServed; c != nil {
		fmt.Fprintln(w, s.ok(rep.InSync))
		fmt.Fprintln(w, s.info(c.Command))
		return
	}
	if dry {
		fmt.Fprintln(w, s.info(rep.InSync))
		return
	}
	fmt.Fprintln(w, s.ok(rep.InSync))
}

// pinText is a pin in an outcome line, after the module: the project profile it is written into,
// then lola update's move "a1b2c3 → d4e5f6", else "pinned <digest>" ("re-pinned" for an update).
func pinText(p engine.Pin, update bool) string {
	out := ""
	if p.Profile != "" {
		out = " (" + p.Profile + ")"
	}
	switch {
	case update && p.Reason == engine.PinUpdate && p.PreviousDigest != "":
		return out + " " + p.PreviousDigest + " → " + p.Digest
	case p.Reason == engine.PinUpdate:
		return out + " re-pinned " + p.Digest
	}
	return out + " pinned " + p.Digest
}

// printPlainReport is a report's --plain rows, every one, in this order: edit, step, per module its
// skipped components, runtime hooks (each ending new or seen) and install hooks, then script, pin,
// remove (what declined runtime hooks remove too), finding (a run's ending new or seen) and notice rows. The review's block prints its
// finding and notice rows alone.
func printPlainReport(w io.Writer, s style, rep report) {
	r := rep.Result
	if rep.NoticesOnly {
		printNotices(w, s, r)
		return
	}
	printEdits(w, s, r.Edits, r.DryRun)
	for _, st := range r.Steps {
		fmt.Fprintln(w, stepLine(s, st))
	}
	once, bodies := excludedOnce(r), map[string]bool{}
	for _, m := range r.Modules {
		if label, ok := once(m); ok {
			printExcluded(w, s, label, m.Skipped, m.SkippedIn, m.NotInOnly, m.OnlyIn, m.Summary, r.DryRun)
		}
		printRuntimeHooks(w, s, m, true)
		printPlainHooks(w, s, bodies, m)
	}
	printScripts(w, s, r.Scripts)
	printPins(w, s, rep.Pins, rep.Update)
	printRemovals(w, s, append(slices.Clone(rep.Removals), engine.DeclinedRemovals(r)...))
	printNotices(w, s, r)
	if c := rep.CacheServed; c != nil {
		fmt.Fprintf(w, "cache\t%d\t%s\t%s\n", c.Modules, c.Fetched, c.Command)
	}
}

// printPlainHooks is m's install hook --plain rows (module, event, script, assistant, status,
// sha256), each followed by its body's rows (printBody), once per script across bodies.
func printPlainHooks(w io.Writer, s style, bodies map[string]bool, m engine.ModuleResult) {
	for _, h := range m.Hooks {
		script := untrusted.Text(h.Script, len(h.Script))
		fmt.Fprintf(w, "hook\t%s\t%s\t%s\t%s\t%s\t%s\n", m.Name, h.Event, script, h.Target, h.Status, h.SHA256)
		printBody(w, s, bodies, "hook-line\t"+m.Name+"\t"+h.Event+"\t"+script, hookBody(h.HookScript))
	}
}

// installReport is an install's report. A module whose lock.json entry it kept (Kept), writing
// none of its files and no entry for it, and running none of its install hooks (as --force runs
// them in a new copy), is already up to date: its outcome line says so, and the values its version
// drops are counted, as a sync with nothing to write does, unless -v.
func installReport(r *engine.InstallResult) report {
	upToDate := func(m engine.ModuleResult) bool {
		return m.Kept && !slices.ContainsFunc(r.Steps, func(st plan.Step) bool { return st.Module == m.Name && st.Scope == m.Scope }) &&
			!slices.ContainsFunc(r.Edits, func(ed engine.Edit) bool { return ed.Module == m.Name }) &&
			!slices.ContainsFunc(m.Hooks, func(h engine.HookRun) bool { return h.Status != engine.HookDeclined })
	}
	return report{Result: r.Result, Verb: "installed", Scopes: r.Scopes, UpToDate: upToDate}
}
