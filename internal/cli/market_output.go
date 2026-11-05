package cli

import (
	"cmp"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/charmbracelet/x/ansi"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/market"
)

// shortCommit is a commit as people read it: its first 7 hex digits.
func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

// marketLine is the one line a fetched marketplace prints (Plan 6c §11.1): its name, entries and
// format, and where it came from.
func marketLine(r engine.MarketRow) string {
	entries := fmt.Sprintf("%d entries", r.Entries)
	if r.Entries == 1 {
		entries = "1 entry"
	}
	line := "marketplace " + r.Name + ": " + entries + " (" + market.FormatName(r.Format) + ") from " + r.Source
	if r.Ref != "" {
		line += " (ref " + r.Ref + ")"
	}
	if r.Commit != "" {
		line += " at " + shortCommit(r.Commit)
	}
	return line
}

// printMarkets prints lola market's listing (Plan 6c §11.1): a table of your marketplaces, then
// the notices. --plain rows are name, format, entries, skipped, fetched_at ("" when not fetched),
// source, ref and path.
func printMarkets(w io.Writer, s style, rows []engine.MarketRow, notices []engine.Notice) {
	switch {
	case s.plain:
		for _, r := range rows {
			fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\n", r.Name, r.Format, r.Entries, r.Skipped, r.FetchedAt, r.Source, r.Ref, r.Path)
		}
	case len(rows) == 0:
		fmt.Fprintln(w, "no marketplaces yet; lola market --add <source> adds one")
	default:
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tFORMAT\tENTRIES\tSKIPPED\tFETCHED\tSOURCE")
		for _, r := range rows {
			entries, skipped := "-", "-"
			if r.FetchedAt != "" {
				entries, skipped = fmt.Sprint(r.Entries), fmt.Sprint(r.Skipped)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, cmp.Or(r.Format, "-"), entries, skipped, r.Fetched, r.Source)
		}
		_ = tw.Flush()
	}
	for _, n := range notices {
		fmt.Fprintln(w, noticeLine(s, n))
	}
}

// printMarket prints one marketplace (Plan 6c §11.1): its source, ref, commit, file, owner and
// description, the entries (name, version, kind, source with its path, ref and commit, and
// description, cut with an ellipsis to fit width columns; 0 cuts nothing) and the skipped entries
// grouped by reason, the most common first. --plain rows are "market" with the name, source, ref,
// path, commit and file; an "entry" row per entry (name, version, kind, source, ref, path, commit,
// description), a "component" row per path an entry selects (entry, kind, path) and a "skipped"
// row per skip (name, reason), every text in full; then a "hint" row per hint, as people's output
// ends with each.
func printMarket(w io.Writer, s style, v *engine.MarketView, hints []string, width int) {
	if s.plain {
		fmt.Fprintf(w, "market\t%s\t%s\t%s\t%s\t%s\t%s\n", v.Name, v.Source, v.Ref, v.Path, v.Commit, v.File)
		for _, e := range v.Entries {
			fmt.Fprintf(w, "entry\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", e.Name, e.Version, e.Kind, e.Source, e.Ref, e.Path, e.Commit, e.Description)
		}
		for _, e := range v.Entries {
			for _, c := range components(e) {
				fmt.Fprintf(w, "component\t%s\t%s\t%s\n", e.Name, c[0], c[1])
			}
		}
		for _, sk := range v.Skipped {
			fmt.Fprintf(w, "skipped\t%s\t%s\n", sk.Name, sk.Reason)
		}
		for _, n := range v.Notices {
			fmt.Fprintln(w, noticeLine(s, n))
		}
		for _, h := range hints {
			fmt.Fprintf(w, "hint\t%s\n", h)
		}
		return
	}
	// Every line is wrapped to the width at spaces: only a word wider than the terminal, such as
	// an entry's name, is left wider than it, whole.
	var buf strings.Builder
	printMarketHuman(&buf, s, v, hints, width)
	fitLines(w, buf.String(), width)
}

// printMarketHuman is printMarket's human output, the entry table fitted to width, ending with
// each hint.
func printMarketHuman(w io.Writer, s style, v *engine.MarketView, hints []string, width int) {
	title := "Marketplace " + v.Name
	if v.Format != "" {
		title += " (" + market.FormatName(v.Format) + ")"
	}
	fmt.Fprintln(w, title)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, f := range [][2]string{{"source", v.Source}, {"ref", v.Ref}, {"commit", v.Commit}, {"file", v.File}, {"fetched", v.Fetched},
		{"owner", v.Owner}, {"version", v.Version}, {"description", v.Description}} {
		if f[1] != "" {
			fmt.Fprintf(tw, "  %s\t%s\n", f[0], f[1])
		}
	}
	_ = tw.Flush()
	if v.FetchedAt != "" {
		fmt.Fprintf(w, "Entries (%d):\n", len(v.Entries))
		printEntries(w, v.Entries, width)
		for _, e := range v.Entries {
			var picks []string
			for _, c := range components(e) {
				picks = append(picks, c[0]+" "+c[1])
			}
			if len(picks) > 0 {
				fmt.Fprintln(w, s.info(e.Name+": the catalog selects "+strings.Join(picks, ", ")))
			}
			for _, n := range e.Notices {
				fmt.Fprintln(w, s.info(e.Name+": "+n))
			}
		}
		if len(v.Skipped) > 0 {
			fmt.Fprintf(w, "Skipped (%d):\n", len(v.Skipped))
			printSkips(w, v.Skipped)
		}
	}
	for _, n := range v.Notices {
		fmt.Fprintln(w, noticeLine(s, n))
	}
	for _, h := range hints {
		fmt.Fprintln(w, s.info(h))
	}
}

// Widths of lola market <name>'s entry table on a terminal: the name column is at most
// maxNameCell columns, a longer name printing whole on a line of its own with the rest of its row
// under it; a version cell is cut to at most maxVersionCell columns and a source cell to at most
// maxSourceCell; and a description with fewer than minDescCell columns left beside its row goes on
// its own line under it.
const (
	maxNameCell    = 32
	maxVersionCell = 16
	maxSourceCell  = 40
	minDescCell    = 20
)

// printEntries prints a marketplace's entries as a table. Given a width (0 for no limit), every
// line fits it but a name wider than the width, which is never cut (it is what you type):
//   - a name wider than maxNameCell is printed alone, the rest of its row on an indented line;
//   - the version cell is cut with an ellipsis to at most maxVersionCell columns;
//   - the source cell is cut with an ellipsis to at most maxSourceCell columns, and less when the
//     row needs it;
//   - each description fills what the row leaves, cut with an ellipsis, or, with fewer than
//     minDescCell columns left, goes on its own indented line, cut to the width.
func printEntries(w io.Writer, entries []engine.MarketEntry, width int) {
	const indent = "    "
	rows := make([][4]string, 0, len(entries))
	var cols [4]int
	for _, e := range entries {
		from := e.Source
		if e.Path != "" {
			from += " path " + e.Path
		}
		if e.Ref != "" {
			from += " ref " + e.Ref
		}
		if e.Commit != "" {
			from += " @ " + shortCommit(e.Commit)
		}
		version := cmp.Or(e.Version, "-")
		if width > 0 {
			version = ansi.Truncate(version, maxVersionCell, "…")
		}
		row := [4]string{e.Name, version, e.Kind, from}
		for i, c := range row {
			if i > 0 || width == 0 || ansi.StringWidth(c) <= maxNameCell {
				cols[i] = max(cols[i], ansi.StringWidth(c))
			}
		}
		rows = append(rows, row)
	}
	// srcCol is the source column's width for a row whose version starts after lead columns.
	srcCol := func(lead int) int {
		if width == 0 {
			return cols[3]
		}
		return max(min(cols[3], maxSourceCell, width-(lead+cols[1]+2+cols[2]+2)), 1)
	}
	for i, row := range rows {
		line := "  " + row[0] + strings.Repeat(" ", max(cols[0]-ansi.StringWidth(row[0]), 0)+2)
		src := srcCol(2 + cols[0] + 2)
		if width > 0 && ansi.StringWidth(row[0]) > maxNameCell {
			fmt.Fprintln(w, "  "+row[0])
			line, src = indent, srcCol(len(indent))
		}
		for k, c := range row[1:] {
			if width > 0 && k == 2 {
				c = ansi.Truncate(c, src, "…")
			}
			pad := cols[k+1]
			if k == 2 {
				pad = src
			}
			line += c + strings.Repeat(" ", max(pad-ansi.StringWidth(c), 0)+2)
		}
		desc := entries[i].Description
		// A version too long for the width is cut too, so the row fits; its name never is.
		fit := func(l string) string {
			l = strings.TrimRight(l, " ")
			if keep := max(width, 2+ansi.StringWidth(row[0])); width > 0 && ansi.StringWidth(l) > keep {
				return ansi.Truncate(l, keep, "…")
			}
			return l
		}
		switch room := width - ansi.StringWidth(line); {
		case width == 0 || desc == "":
			fmt.Fprintln(w, fit(line+desc))
		case room >= minDescCell:
			fmt.Fprintln(w, fit(line+ansi.Truncate(desc, room, "…")))
		default:
			fmt.Fprintln(w, fit(line))
			fmt.Fprintln(w, indent+ansi.Truncate(desc, max(width-len(indent), 1), "…"))
		}
	}
}

// fitLines writes text to w with each line wider than width columns (0 for no limit) wrapped at
// spaces (wrapWords): a field's value under its own start, any other line's continuations
// indented two columns more than the line. Nothing is lost.
func fitLines(w io.Writer, text string, width int) {
	for _, line := range strings.SplitAfter(text, "\n") {
		body := strings.TrimSuffix(line, "\n")
		if width == 0 || ansi.StringWidth(body) <= width {
			fmt.Fprint(w, line)
			continue
		}
		// A field ("  description  <value>") keeps its label and gap, its value wrapped in its own
		// column; any other line's continuations are indented two more than the line.
		lead := len(body) - len(strings.TrimLeft(body, " "))
		head, pad := body[:lead], strings.Repeat(" ", min(lead+2, width-1))
		if gap := strings.Index(body[lead:], "  "); gap > 0 {
			value := lead + gap + len(body[lead+gap:]) - len(strings.TrimLeft(body[lead+gap:], " "))
			if value < width-minDescCell {
				head, pad = body[:value], strings.Repeat(" ", value)
			}
		}
		for k, part := range wrapWords(body[len(head):], max(width-len(pad), 1)) {
			prefix := pad
			if k == 0 {
				prefix = head
			}
			fmt.Fprintln(w, prefix+part)
		}
	}
}

// wrapWords is text in lines of at most limit columns, broken at spaces only (a name such as
// clangd-lsp stays whole). A word wider than limit is never cut: it gets a line of its own,
// wider than limit (ruling R-6cux-6), since a hard break would corrupt what a user copies.
func wrapWords(text string, limit int) []string {
	var lines []string
	cur := ""
	for _, word := range strings.Fields(text) {
		switch {
		case cur != "" && ansi.StringWidth(cur)+1+ansi.StringWidth(word) <= limit:
			cur += " " + word
			continue
		case cur != "":
			lines = append(lines, cur)
		}
		cur = word
	}
	return append(lines, cur)
}

// printSkips prints a marketplace's skipped entries grouped by reason, the most common reason
// first (then in the order the entries come), each with its count when it has more than one and
// the names it skips: a catalog often skips many entries for one reason.
func printSkips(w io.Writer, skipped []engine.MarketSkip) {
	var reasons []string
	names := map[string][]string{}
	for _, sk := range skipped {
		if names[sk.Reason] == nil {
			reasons = append(reasons, sk.Reason)
		}
		names[sk.Reason] = append(names[sk.Reason], cmp.Or(sk.Name, "(no usable name)"))
	}
	slices.SortStableFunc(reasons, func(a, b string) int { return len(names[b]) - len(names[a]) })
	for _, r := range reasons {
		count := ""
		if n := len(names[r]); n > 1 {
			count = fmt.Sprintf(" (%d)", n)
		}
		fmt.Fprintln(w, "  "+r+count+": "+strings.Join(names[r], ", "))
	}
}

// components are the kind ("skill", "command", "agent") and path of each component e's catalog
// lists select, in list order.
func components(e engine.MarketEntry) [][2]string {
	if e.Components == nil {
		return nil
	}
	var out [][2]string
	for _, l := range []struct {
		kind  string
		paths []string
	}{{"skill", e.Components.Skills}, {"command", e.Components.Commands}, {"agent", e.Components.Agents}} {
		for _, p := range l.paths {
			out = append(out, [2]string{l.kind, p})
		}
	}
	return out
}

// marketGlyphs mark an --update's entry changes.
var marketGlyphs = map[string]string{"added": "+", "removed": "-", "changed": "~"}

// printMarketResult prints what lola market --add or --update did (Plan 6c §11.1): a line per
// marketplace by its outcome, with an --update's entry changes under it, then the notices.
// --plain rows are the outcome and the marketplace, a "change" row per entry change (change,
// market, entry, fields comma-separated), then the notices as notice rows.
func printMarketResult(w io.Writer, s style, res *engine.MarketResult) {
	rows := map[string]engine.MarketRow{}
	for _, r := range res.Markets {
		rows[r.Name] = r
	}
	for _, u := range res.Updates {
		if s.plain {
			fmt.Fprintf(w, "%s\t%s\n", u.Outcome, u.Market)
			continue
		}
		r := rows[u.Market]
		switch u.Outcome {
		case engine.MarketAdded, engine.MarketUpdated:
			fmt.Fprintln(w, s.ok(marketLine(r)))
		case engine.MarketUpToDate:
			fmt.Fprintln(w, s.ok(marketLine(r)+"; up to date"))
		case engine.MarketWouldAdd:
			fmt.Fprintln(w, s.info("would add "+marketLine(r)))
		case engine.MarketWouldUpdate:
			fmt.Fprintln(w, s.info("would update "+marketLine(r)))
		case engine.MarketFailed, engine.MarketStopped:
			// The run's error says why, once (as lola profile --update's).
			fmt.Fprintln(w, s.bad("marketplace "+u.Market+" was not updated (the error says why)"))
		}
		for _, c := range res.Changes {
			if c.Market != u.Market {
				continue
			}
			line := "  " + marketGlyphs[c.Change] + " " + c.Entry
			if len(c.Fields) > 0 {
				line += " (" + strings.Join(c.Fields, ", ") + ")"
			}
			fmt.Fprintln(w, line)
		}
	}
	if s.plain {
		for _, c := range res.Changes {
			fmt.Fprintf(w, "change\t%s\t%s\t%s\t%s\n", c.Change, c.Market, c.Entry, strings.Join(c.Fields, ","))
		}
	}
	for _, n := range res.Notices {
		fmt.Fprintln(w, noticeLine(s, n))
	}
}
