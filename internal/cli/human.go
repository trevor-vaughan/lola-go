package cli

import (
	"bytes"
	"cmp"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

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

var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// ansiTable aligns tab-separated lines like a tabwriter (two spaces between columns, the last cell
// unpadded) but measures a cell by what shows: a tabwriter counts the bytes of a color sequence,
// so rows whose cells are colored differently would not line up. Use it for any table whose cells
// may carry color.
type ansiTable struct {
	w   io.Writer
	buf bytes.Buffer
}

func (t *ansiTable) Write(p []byte) (int, error) { return t.buf.Write(p) }

// Flush writes the buffered lines aligned; a column's width spans the run of consecutive lines
// that have a cell in it, as a tabwriter's does.
func (t *ansiTable) Flush() error {
	text := strings.TrimSuffix(t.buf.String(), "\n")
	t.buf.Reset()
	if text == "" {
		return nil
	}
	var lines [][]string
	for l := range strings.SplitSeq(text, "\n") {
		lines = append(lines, strings.Split(l, "\t"))
	}
	visible := func(c string) int { return utf8.RuneCountInString(ansiSeq.ReplaceAllString(c, "")) }
	widths := make([][]int, len(lines))
	for i, cells := range lines {
		widths[i] = make([]int, len(cells))
	}
	for col, found := 0, true; found; col++ {
		found = false
		for i := 0; i < len(lines); {
			if len(lines[i])-1 <= col {
				i++
				continue
			}
			found = true
			end, width := i, 0
			for ; end < len(lines) && len(lines[end])-1 > col; end++ {
				width = max(width, visible(lines[end][col]))
			}
			for ; i < end; i++ {
				widths[i][col] = width + 2
			}
		}
	}
	var out strings.Builder
	for i, cells := range lines {
		for col, c := range cells {
			out.WriteString(c)
			if col < len(cells)-1 {
				out.WriteString(strings.Repeat(" ", widths[i][col]-visible(c)))
			}
		}
		out.WriteByte('\n')
	}
	_, err := io.WriteString(t.w, out.String())
	return err
}

// strong is bold when the style is (app.bold): on a terminal, as emphasis, not color.
func (s style) strong(t string) string {
	if !s.bold {
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
	case plan.ActionDelete, plan.ActionForget, plan.ActionKeep:
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
	// The marker is two columns, so the text wraps under a two-space lead and s.info or s.warn adds it.
	text := strings.TrimPrefix(wrapAt("  ", n.Text, s.width, true), "  ")
	line := s.info(text)
	if n.Level == engine.LevelWarn {
		line = s.warn(text)
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
		row := []string{"finding", string(f.Severity), f.Module, f.Target, f.Kind, f.Artifact, f.Code, f.Text}
		if f.New != nil { // a run's finding: lola check's and lola info's carry none
			row = append(row, map[bool]string{true: "new", false: "seen"}[*f.New])
		}
		return strings.Join(row, "\t")
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

// scopeLabel names a scope in human output.
func scopeLabel(sc engine.ScopeResult) string {
	if sc.Root != "" {
		return sc.Scope + " (" + untrusted.Text(sc.Root, untrusted.MaxName) + ")"
	}
	return sc.Scope
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

// planHeader heads a plan that writes nothing yet: a dry run, the plan the next question applies, or
// the plan -y or --force applies without a question.
func planHeader(s style) string {
	if s.applied {
		return "plan (applied now, unasked):"
	}
	if s.asking {
		return "plan (nothing written yet; the question below applies it):"
	}
	return "plan (dry run; nothing was written):"
}

// printScripts lists the install scripts of a run in run order: each with its module,
// event, source and target, and its status; a dry run's project script the run has no trust for
// says it needs --trust=scripts. A --plain row is "script", module, event, source, path, target,
// status, sha256, trusted (true or false) and the command that trusts a later script, "-" for none.
func printScripts(w io.Writer, s style, scripts []engine.ScriptRun) {
	bodies := map[string]bool{}
	for _, sr := range scripts {
		if s.plain {
			fmt.Fprintf(w, "script\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%t\t%s\n", sr.Module, sr.Event, sr.Source, sr.Path, sr.Target, sr.Status, sr.SHA256, sr.Trusted, cmp.Or(sr.TrustRun, "-"))
			printBody(w, s, bodies, "script-line\t"+sr.Module+"\t"+sr.Event+"\t"+sr.Path, runBody(sr))
			continue
		}
		status := sr.Status
		switch status {
		case engine.HookWouldRun:
			status = "would run" // JSON and --plain keep the would-run value
		case engine.HookLater:
			status = "trusted; nothing to wrap now"
			if sr.NeedsTrust() {
				status = "not trusted yet; to trust it: " + sr.TrustRun
			}
		}
		if sr.NeedsTrust() && sr.Status != engine.HookLater {
			status += ", needs " + engine.TrustScripts.Arg()
		}
		source := sr.Source
		if sr.Root() != "" {
			source += " at " + sr.Root() // the path is from that root
		}
		line := fmt.Sprintf("%s %s script %s (%s, sha256 %s, %s): %s", sr.Module, sr.Event, sr.Path, source, sr.SHA256, sr.Target, status)
		if sr.Status == engine.HookLater { // checked with nothing to wrap: no module, no assistant
			line = fmt.Sprintf("%s script %s (%s, sha256 %s): %s", sr.Event, sr.Path, source, sr.SHA256, status)
		}
		switch sr.Status {
		case engine.HookRan:
			fmt.Fprintln(w, s.ok(line))
		case engine.HookWouldRun, engine.HookLater:
			fmt.Fprintln(w, s.info(line))
		default:
			fmt.Fprintln(w, s.warn(line))
		}
		printBody(w, s, bodies, sr.Path, runBody(sr))
	}
}

// scriptBody is a hook's or project script's body as a run lists it, for printBody: its text, raw,
// or why it is withheld and the file to inspect, its shown record key ("" for a script no consent
// covers) and whether the record lacks it (nil outside a run).
type scriptBody struct {
	text, withheld, inspect, key string
	fresh                        *bool
}

func hookBody(h engine.HookScript) scriptBody {
	return scriptBody{h.Body, h.Withheld, h.Inspect(), h.ShownKey(), h.New}
}

func runBody(sr engine.ScriptRun) scriptBody {
	return scriptBody{sr.Body, sr.Withheld, sr.Inspect(), sr.ShownKey(), sr.New}
}

// bodyLines is a body's text as output prints it: a line each, without the empty one after a
// final newline, each tab expanded to 8-column stops, then through untrusted.Text, uncut.
func bodyLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i, l := range lines {
		l = expandTabs(l)
		lines[i] = untrusted.Text(l, len(l))
	}
	return lines
}

// expandTabs is l with each tab replaced by the spaces to the next multiple of 8 columns, a rune
// a column.
func expandTabs(l string) string {
	if !strings.Contains(l, "\t") {
		return l
	}
	var b strings.Builder
	col := 0
	for _, r := range l {
		if r == '\t' {
			n := 8 - col%8
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		b.WriteRune(r)
		col++
	}
	return b.String()
}

// withheldLine is what output prints in place of a body it cannot show (scriptBody.withheld).
func withheldLine(b scriptBody) string {
	return "(not shown: " + strings.ReplaceAll(b.withheld, "_", " ") + "; inspect " + untrusted.Text(b.inspect, len(b.inspect)) +
		"; only --unseen consents to it)"
}

// printBody prints, once per key (done), the body of the hook or script named name under its line,
// which is how output shows it, so the shown record may count it (key): in full, each line
// "    | …" (bodyLines), when lola has not shown it before (fresh) or under -v, else as a fold that
// only a folder can open, which records nothing. An empty body is shown by its line alone. One
// withheld prints why and the file to inspect, and is never shown. A --plain body is a row per
// line, name its first columns, and a withheld one a withheld row (reason, file). A script no
// consent covers (no key) prints nothing.
func printBody(w io.Writer, s style, done map[string]bool, name string, b scriptBody) {
	if b.key == "" || done[b.key] {
		return
	}
	done[b.key] = true
	lines := bodyLines(b.text)
	switch {
	case b.withheld != "" && s.plain:
		fmt.Fprintf(w, "withheld\t%s\t%s\n", b.withheld, untrusted.Text(b.inspect, len(b.inspect)))
		return
	case b.withheld != "":
		fmt.Fprintln(w, "    "+withheldLine(b))
		return
	case s.plain:
		for _, l := range lines {
			fmt.Fprintf(w, "%s\t%s\n", name, l)
		}
	case b.fresh == nil || *b.fresh || s.verbose || len(lines) == 0:
		for _, l := range lines {
			fmt.Fprintln(w, "    | "+l)
		}
	default:
		detail := make([]string, len(lines))
		for i, l := range lines {
			detail[i] = "| " + l
		}
		printFold(w, s, fmt.Sprintf("%s: %s", name, counted(len(lines), "line")), detail)
		return
	}
	s.saw(b.key)
}

// worthShowing reports whether a run prints f (printFindings): a warn or skip finding, an info
// finding with a hint, or one saying a target wrote no copy of a skill because the agents target
// delivers it, without which the run would name a target that got no file and say nothing why.
func worthShowing(f target.Finding) bool {
	return f.Severity != target.SeverityInfo || f.Hint != "" || f.Code == target.CodeSkillViaAgents
}

// printFindings prints the findings worth showing (worthShowing). Under --plain every finding is
// a row; otherwise findings group (groupFindings), a group of two or more as a summary with a line
// per member.
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
		for _, line := range groupLines(s, g) {
			if line != "" {
				fmt.Fprintln(w, line)
			}
		}
	}
}

// userFindings is fs as lola shows them to whoever runs it: a finding whose hint is for the
// module's author (target.Finding.AuthorHint) carries the hint for the user (UserHint) instead.
func userFindings(fs []target.Finding) []target.Finding {
	out := make([]target.Finding, 0, len(fs))
	for _, f := range fs {
		if f.AuthorHint {
			f.Hint = f.UserHint
		}
		out = append(out, f)
	}
	return out
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
	findings := userFindings(r.Findings)
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
// the hook ("-" for settings hooks); an agent's inline MCP server has "mcp_server:<name>" for
// its event and its transport for its type. In a run's report (run) a --plain row then has new
// or seen (engine.RuntimeHook.New). Every --plain row ends with the hook in full, its lines
// (runtimeHookLines) joined by "; ", which, in a run's report, the shown record then holds.
func printRuntimeHooks(w io.Writer, s style, m engine.ModuleResult, run bool) {
	if s.plain {
		for _, h := range m.RuntimeHooks {
			event := h.Event
			if h.MCPServer != "" {
				event = "mcp_server:" + h.MCPServer
			}
			row := fmt.Sprintf("runtime-hook\t%s\t%s\t%s\t%s\t%s\t%s", m.Name, h.Target, event, h.Type, h.Status, cmp.Or(h.Component, "-"))
			if run {
				row += "\t" + map[bool]string{true: "new", false: "seen"}[hookIsNew(h)]
				s.saw(h.ShownKey(m.Name))
			}
			var lines []string
			for _, l := range runtimeHookLines(h) {
				lines = append(lines, strings.TrimSpace(l))
			}
			row += "\t" + strings.Join(lines, "; ")
			fmt.Fprintln(w, row)
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

// hookIsNew reports whether a runtime hook of a run's report is new to the user
// (engine.RuntimeHook.New); one no run compared is.
func hookIsNew(h engine.RuntimeHook) bool { return h.New == nil || *h.New }

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
