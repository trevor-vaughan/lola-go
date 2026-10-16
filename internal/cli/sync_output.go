package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/trust"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// filesShown caps the file paths the human trust diff lists per module, and stepsShown the steps
// a dry run's plan lists per module; JSON lists all.
const (
	filesShown = 20
	stepsShown = 40
)

// pad is the trust diff's value column, under "  ~ <module>     ".
const pad = "                  "

// printSync renders a sync result (Plan 6a §11.1): in a dry run the trust diff first; then the
// plan as render shows it, with the MCP servers of the modules the diff did not show, the pins
// (unless the diff on stdout showed them), the removals, findings and notices, and a line per
// module the run changed, then how many it left unchanged. A run in sync prints its findings
// and notices, then says so, naming the modules it was limited to (names), each once. more is the command
// that lists every file and step of a module whose diff or dry-run plan is capped.
func printSync(w io.Writer, s style, name string, r *engine.SyncResult, names []string, more string) {
	inSync := "already in sync"
	if len(names) > 0 {
		var shownNames []string
		for _, n := range names {
			if shown := untrusted.Text(n, untrusted.MaxName); !slices.Contains(shownNames, shown) {
				shownNames = append(shownNames, shown)
			}
		}
		inSync = strings.Join(shownNames, ", ") + " " + inSync
	}
	asked := r.Trust.Status != trust.StatusTrusted && r.Trust.Status != engine.TrustStatusNotApplicable
	diffShown := r.DryRun && asked // on stdout, with the pins
	if diffShown {
		printTrustDiff(w, s, name, r.Trust, more)
	}
	if r.InSync && !diffShown && !s.plain {
		printFindings(w, s, r.Findings)
		for _, n := range r.Notices {
			fmt.Fprintln(w, noticeLine(s, n))
		}
		if r.DryRun {
			fmt.Fprintln(w, s.info(inSync))
		} else {
			fmt.Fprintln(w, s.ok(inSync))
		}
		return
	}
	// The diff listed the MCP servers of the modules it showed: the footer names the others.
	plan := *r.Result
	plan.Modules = slices.Clone(r.Modules)
	for i, m := range plan.Modules {
		if asked && slices.ContainsFunc(r.Trust.Changes, func(c engine.TrustChange) bool { return c.Module == m.Name }) {
			plan.Modules[i].MCPServers = nil
		}
	}
	steps := ""
	if r.DryRun {
		steps = more
	}
	printPlan(w, s, &plan, nil, steps)
	if !diffShown || s.plain {
		printPins(w, s, r.Pins)
	}
	printRemovals(w, s, r.Removals)
	printFindings(w, s, r.Findings)
	for _, n := range r.Notices {
		fmt.Fprintln(w, noticeLine(s, n))
	}
	if r.DryRun && r.ToApply != "" && !s.plain {
		fmt.Fprintln(w, s.info("to apply: "+r.ToApply))
		if r.OffTerminal != "" {
			fmt.Fprintln(w, s.info("off a terminal: "+r.OffTerminal))
		}
	}
	if r.DryRun || s.plain {
		return
	}
	unchanged := 0
	for _, m := range r.Modules {
		if slices.Contains(r.Changed, m.Name) {
			fmt.Fprintln(w, s.ok(fmt.Sprintf("synced %s (%s)", m.Name, strings.Join(m.Targets, ", "))))
		} else {
			unchanged++
		}
	}
	for _, rm := range r.Removals {
		if rm.WholeModule {
			fmt.Fprintln(w, s.ok("removed "+rm.Module))
		}
	}
	if unchanged > 0 {
		fmt.Fprintln(w, s.ok(fmt.Sprintf("%d unchanged", unchanged)))
	}
}

// printPins lists the pins a sync writes, each once with its new digest in full (the diff and
// JSON have the commit and the previous digest); --plain rows are pin, module, digest, reason.
func printPins(w io.Writer, s style, pins []engine.Pin) {
	if len(pins) == 0 {
		return
	}
	if s.plain {
		for _, p := range pins {
			fmt.Fprintf(w, "pin\t%s\t%s\t%s\n", p.Module, p.Digest, p.Reason)
		}
		return
	}
	fmt.Fprintln(w, "pins:")
	for _, p := range pins {
		line := p.Module + " pinned " + p.Digest
		if p.Reason == engine.PinUpdate {
			line = p.Module + " re-pinned " + p.Digest + " (--update)"
		}
		fmt.Fprintln(w, "  "+s.info(line))
	}
}

// printRemovals lists what a sync removes, per module and target, with each path lola wrote
// there on a line of its own; --plain rows are remove, module, target, path, one per path (a
// target lola wrote no path for has one row with none).
func printRemovals(w io.Writer, s style, rs []engine.Removal) {
	if len(rs) == 0 {
		return
	}
	if s.plain {
		for _, r := range rs {
			for _, t := range r.Targets {
				rows := 0
				for _, p := range r.Paths {
					if p.Target == t {
						fmt.Fprintf(w, "remove\t%s\t%s\t%s\n", r.Module, t, p.Path)
						rows++
					}
				}
				if rows == 0 {
					fmt.Fprintf(w, "remove\t%s\t%s\t\n", r.Module, t)
				}
			}
		}
		return
	}
	fmt.Fprintln(w, "removals:")
	for _, r := range rs {
		why := "(no longer in its targets)"
		if r.WholeModule {
			why = "(whole module)"
		}
		fmt.Fprintln(w, "  "+s.bad(r.Module+": "+strings.Join(r.Targets, ", ")+" "+why))
		for _, p := range r.Paths {
			fmt.Fprintln(w, "      "+p.Path)
		}
	}
}

// printTrustDiff shows how the manifest differs from what the user last trusted (Plan 6a §7.5):
// a header naming the cause; a source that changed host first, marked !, then the other modules
// in module-name order, each marked ~ changed, + added or - removed, with its fields and the
// content it brings; then the top-level targets. Every value has been through untrusted.Text. --plain rows are trust, module, change.
// A report with no change prints nothing.
func printTrustDiff(w io.Writer, s style, name string, rep engine.TrustReport, more string) {
	if len(rep.Changes) == 0 && rep.Targets == nil {
		return
	}
	if s.plain {
		for _, c := range rep.Changes {
			fmt.Fprintf(w, "trust\t%s\t%s\n", c.Module, c.Kind)
		}
		if rep.Targets != nil {
			fmt.Fprintf(w, "trust\t\ttargets\n")
		}
		return
	}
	// The header names the cause, so a re-pin the user asked for is not framed as someone else's
	// change.
	switch {
	case rep.Status == trust.StatusNew:
		fmt.Fprintf(w, "%s has not been trusted on this machine (a fresh clone, or a moved project):\n", name)
	case rep.ManifestChanged:
		fmt.Fprintf(w, "%s changed since lola last wrote it or you last trusted it:\n", name)
	default:
		fmt.Fprintf(w, "%s is unchanged; lola sync needs your trust for:\n", name)
	}
	var first, rest []engine.TrustChange
	for _, c := range rep.Changes {
		if c.HostChanged {
			first = append(first, c)
		} else {
			rest = append(rest, c)
		}
	}
	for _, c := range append(first, rest...) {
		printTrustChange(w, c, more)
	}
	switch t := rep.Targets; {
	case t == nil:
	case t.From == nil: // a side that is absent is the + or -, never "(none) →"
		fmt.Fprintf(w, "  + %-13s %s\n", "targets", shownValue(t.To))
	case t.To == nil:
		fmt.Fprintf(w, "  - %-13s %s\n", "targets", shownValue(t.From))
	default:
		fmt.Fprintf(w, "  ~ %-13s %s → %s\n", "targets", shownValue(t.From), shownValue(t.To))
	}
}

// changeGlyphs mark a change in the diff and the picker (the picker adds ! for a host change).
var changeGlyphs = map[string]string{trust.ChangeAdded: "+", trust.ChangeChanged: "~", trust.ChangeRemoved: "-"}

// printTrustChange is one module of the trust diff: its fields under the value column, a commit
// or digest from→to on two lines so no hash line runs long; its files, one per line, changed and
// removed before added, at most filesShown; each MCP server and hook with its change mark, a
// changed server's previous form on was: lines; and, for a folder whose content changed, how to
// review the edits and how to record them as the user's own.
func printTrustChange(w io.Writer, c engine.TrustChange, more string) {
	glyph := changeGlyphs[c.Kind]
	if c.HostChanged {
		glyph = "!"
	}
	var lines, hashes []string
	switch c.Kind {
	case trust.ChangeAdded:
		var src []string
		for _, f := range c.Fields {
			switch f.Field {
			case "source":
				src = append(src, shownValue(f.To))
			case "ref", "path":
				src = append(src, f.Field+" "+shownValue(f.To))
			case "commit", "digest":
				hashes = append(hashes, f.Field+" "+shownValue(f.To))
			default:
				lines = append(lines, f.Field+" "+shownValue(f.To))
			}
		}
		if len(src) > 0 { // a folder lola never installed adds only its digest
			lines = append([]string{strings.Join(src, "  ")}, lines...)
		}
	case trust.ChangeChanged:
		for _, f := range c.Fields {
			switch {
			case c.HostChanged && f.Field == "source":
				lines = append([]string{"source host changed: " + shownValue(f.From) + " → " + shownValue(f.To)}, lines...)
			case f.Field == "commit" || f.Field == "digest":
				hashes = append(hashes, hashLines(f)...)
			default:
				lines = append(lines, f.Field+" "+shownValue(f.From)+" → "+shownValue(f.To))
			}
		}
	}
	if c.Note != "" && len(lines) > 0 {
		lines[0] += " (" + c.Note + ")"
	}
	if c.Reason != "" {
		lines = append(lines, c.Reason)
	}
	// A pin equal to the one the record holds changes no field: its hashes print here, once.
	if p := c.Pin; p != nil && len(hashes) == 0 {
		if p.Commit != "" {
			hashes = append(hashes, "commit "+untrusted.Text(p.Commit, untrusted.MaxName))
		}
		hashes = append(hashes, "digest "+untrusted.Text(p.Digest, untrusted.MaxName))
	}
	head := fmt.Sprintf("  %s %-13s", glyph, c.Module)
	if len(lines) == 0 { // a hash never shares the module's line: a long name would overflow it
		fmt.Fprintln(w, strings.TrimRight(head, " "))
	}
	for i, l := range lines {
		if i == 0 {
			fmt.Fprintln(w, head+" "+l)
		} else {
			fmt.Fprintln(w, pad+l)
		}
	}
	for _, h := range hashes {
		fmt.Fprintln(w, pad+h)
	}
	printFiles(w, c.Files, more)
	if c.FilesNote != "" {
		fmt.Fprintln(w, "      ("+c.FilesNote+")")
	}
	for _, m := range c.MCPServers {
		printServer(w, m)
	}
	for _, h := range c.Hooks {
		switch h.Kind {
		case engine.HookKindInstall:
			fmt.Fprintf(w, "      install hook %s, %s: %s\n", h.Event, h.Change, h.Detail)
		case engine.HookKindMCPServer:
			fmt.Fprintf(w, "      agent MCP server for %s, %s: %s\n", h.Target, h.Change, h.Detail)
		default:
			fmt.Fprintf(w, "      runtime hook for %s on %s, %s: %s\n", h.Target, h.Event, h.Change, h.Detail)
		}
	}
	for _, l := range []string{c.ReviewEdits, c.InstallOwn} {
		if l != "" {
			fmt.Fprintln(w, "      "+l)
		}
	}
}

// hashLines prints a commit or digest change: from on the field's line and → to under it, the
// values aligned; a side that is absent leaves one line.
func hashLines(f trust.FieldChange) []string {
	switch {
	case f.From == nil:
		return []string{f.Field + " " + shownValue(f.To)}
	case f.To == nil:
		return []string{f.Field + " " + shownValue(f.From) + " (dropped)"}
	}
	return []string{f.Field + " " + shownValue(f.From), strings.Repeat(" ", len(f.Field)-1) + "→ " + shownValue(f.To)}
}

// printFiles prints a module's file changes: their counts, then one path per line, changed and
// removed before added, each group in path order, at most filesShown, then how many more.
func printFiles(w io.Writer, files []engine.FileChange, more string) {
	if len(files) == 0 {
		return
	}
	var counts, shown []string
	for _, kind := range []string{trust.ChangeChanged, trust.ChangeRemoved, trust.ChangeAdded} {
		n := 0
		for _, f := range files {
			if f.Change == kind {
				n++
				shown = append(shown, changeGlyphs[kind]+" "+f.Path)
			}
		}
		if n > 0 {
			counts = append(counts, fmt.Sprintf("%d %s", n, kind))
		}
	}
	fmt.Fprintln(w, "      files: "+strings.Join(counts, ", "))
	for i, f := range shown {
		if i == filesShown {
			fmt.Fprintf(w, "        … and %d more (%s lists them all)\n", len(shown)-filesShown, more)
			break
		}
		fmt.Fprintln(w, "        "+f)
	}
}

// printServer prints one module MCP server of the diff with its change mark: an unchanged one on
// one line; otherwise what it runs or where it connects, its env, headers and cwd, and for a
// changed one a was: line under each part that differs, the values aligned.
func printServer(w io.Writer, m engine.ContentMCPServer) {
	head := fmt.Sprintf("MCP server %s (%s), %s", m.Name, m.Transport, m.Change)
	if m.Change == engine.ChangeUnchanged {
		fmt.Fprintln(w, "      "+head)
		return
	}
	head += ": "
	was := strings.Repeat(" ", 6+len([]rune(head))-len("was: ")) + "was: "
	fmt.Fprintln(w, "      "+head+serverReach(m))
	prev := m.Previous
	if prev != nil && serverReach(*prev) != serverReach(m) {
		fmt.Fprintln(w, was+serverReach(*prev))
	}
	for _, part := range []struct {
		label    string
		now, old []string
	}{{"env", m.Env, prevList(prev, func(p *engine.ContentMCPServer) []string { return p.Env })},
		{"header", m.Headers, prevList(prev, func(p *engine.ContentMCPServer) []string { return p.Headers })},
		{"cwd", nonEmpty(m.Cwd), prevList(prev, func(p *engine.ContentMCPServer) []string { return nonEmpty(p.Cwd) })}} {
		for _, v := range part.now {
			fmt.Fprintln(w, "        "+part.label+": "+v)
		}
		if prev != nil && !slices.Equal(part.now, part.old) {
			old := strings.Join(part.old, ", ")
			if old == "" {
				old = "(none)"
			}
			fmt.Fprintln(w, "        was "+part.label+": "+old)
		}
	}
}

// serverReach is what an MCP server runs (stdio) or connects to (http, sse).
func serverReach(m engine.ContentMCPServer) string {
	if m.Transport == "stdio" {
		return "runs " + m.Runs
	}
	return "connects to " + m.Runs
}

// prevList is a part of a changed server's previous form, nil for a server with none.
func prevList(p *engine.ContentMCPServer, part func(*engine.ContentMCPServer) []string) []string {
	if p == nil {
		return nil
	}
	return part(p)
}

// nonEmpty is s as a list of one, or none when s is "".
func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

// shownValue prints a trust field value: none for an absent one, a list in brackets.
func shownValue(v any) string {
	switch v := v.(type) {
	case nil:
		return "(none)"
	case string:
		return v
	case []string:
		return "[" + strings.Join(v, ", ") + "]"
	}
	return fmt.Sprint(v)
}
