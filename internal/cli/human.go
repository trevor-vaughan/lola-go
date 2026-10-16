package cli

import (
	"fmt"
	"io"
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

// memberShown caps a JSON array member, such as a hook group, in a step line; the full member
// is in the JSON output.
const memberShown = 120

// stepLine renders one plan step.
func stepLine(s style, st plan.Step) string {
	// Paths and keys can carry a module's file and server names: printed safely here, and kept
	// raw in the result, where lola migrate reads them as paths.
	dest := untrusted.Text(st.Path, len(st.Path))
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
	case st.Replaced:
		line += " [replaces a file lola did not write]"
	case st.Forced != "":
		line += " [forced: " + st.Forced + "]"
	}
	if st.Note != "" {
		line += " (" + st.Note + ")"
	}
	switch st.Action {
	case plan.ActionDelete:
		return s.bad(line)
	case plan.ActionForget:
		return s.info(line)
	case plan.ActionAbsent:
		return s.warn(line)
	default:
		return s.ok(line)
	}
}

func noticeLine(s style, n engine.Notice) string {
	if s.plain {
		return "notice\t" + n.Level + "\t" + n.Text
	}
	if n.Level == engine.LevelWarn {
		return s.warn(n.Text)
	}
	return s.info(n.Text)
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
		line += "\n    hint: " + f.Hint
	}
	return line
}

// printCheck renders a lola check report: every finding, including info ones, and a line for
// each target that renders a module without loss.
func printCheck(w io.Writer, s style, rep *engine.CheckReport) {
	for _, f := range rep.Findings {
		fmt.Fprintln(w, checkLine(s, f))
	}
	for _, m := range rep.Modules {
		printExcluded(w, s, m.Name, m.Skipped, false)
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
			line += "\n    hint: " + f.Hint
		}
		return line
	}
	return findingLine(s, f)
}

// printInstall renders an install result; with more than one scope, steps are grouped and
// modules named per scope.
func printInstall(w io.Writer, s style, r *engine.InstallResult) {
	printScoped(w, s, "installed", r.Result, r.Scopes)
}

// printResult renders an install/uninstall/render result: steps, MCP transparency, install
// hooks, notices, and a summary line.
func printResult(w io.Writer, s style, verb string, r *engine.Result) {
	printScoped(w, s, verb, r, nil)
}

// scopeLabel names a scope in human output.
func scopeLabel(sc engine.ScopeResult) string {
	if sc.Root != "" {
		return sc.Scope + " (" + untrusted.Text(sc.Root, untrusted.MaxName) + ")"
	}
	return sc.Scope
}

func printScoped(w io.Writer, s style, verb string, r *engine.Result, scopes []engine.ScopeResult) {
	printPlan(w, s, r, scopes)
	printFindings(w, s, r.Findings)
	for _, n := range r.Notices {
		fmt.Fprintln(w, noticeLine(s, n))
	}
	if r.DryRun || s.plain {
		return
	}
	printDone(w, s, verb, r, scopes)
}

// printPlan prints the steps and each module's details: what a run did, or in a dry run would do.
func printPlan(w io.Writer, s style, r *engine.Result, scopes []engine.ScopeResult) {
	if r.DryRun && !s.plain {
		fmt.Fprintln(w, "plan (dry run; nothing was written):")
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
		for _, st := range r.Steps {
			fmt.Fprintln(w, "  "+stepLine(s, st))
		}
	}
	for _, m := range r.Modules {
		printExcluded(w, s, m.Name, m.Skipped, r.DryRun)
		if len(m.MCPServers) > 0 && !s.plain {
			fmt.Fprintf(w, "%s MCP servers:\n", m.Name)
			for _, srv := range m.MCPServers {
				fmt.Fprintf(w, "    %s (%s) runs: %s\n", srv.Name, srv.Transport, srv.Runs)
			}
		}
		printRuntimeHooks(w, s, m)
		for _, h := range m.Hooks {
			script := untrusted.Text(h.Script, len(h.Script))
			if s.plain {
				fmt.Fprintf(w, "hook\t%s\t%s\t%s\t%s\t%s\n", m.Name, h.Event, script, h.Target, h.Status)
				continue
			}
			line := fmt.Sprintf("%s %s hook %s (%s): %s", m.Name, h.Event, script, h.Target, h.Status)
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
}

// printDone prints the closing "✓ <verb> <module>" line of each module a run wrote.
func printDone(w io.Writer, s style, verb string, r *engine.Result, scopes []engine.ScopeResult) {
	for _, m := range r.Modules {
		where := ""
		if len(scopes) > 1 {
			where = " at " + m.Scope + " scope"
		}
		fmt.Fprintln(w, s.ok(fmt.Sprintf("%s %s%s (%s)", verb, m.Name, where, strings.Join(m.Targets, ", "))))
	}
}

// printFindings prints the findings the user can act on: warn and skip findings, and info
// findings with a hint.
func printFindings(w io.Writer, s style, findings []target.Finding) {
	for _, f := range findings {
		if f.Severity == target.SeverityInfo && f.Hint == "" {
			continue
		}
		if line := findingLine(s, f); line != "" {
			fmt.Fprintln(w, line)
		}
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

// printExcluded names a module's components that selections.yaml excludes; a dry run saved
// no edit, so it says they would be skipped. A --plain row is "skipped", the module and the
// component (kind/name).
func printExcluded(w io.Writer, s style, module string, skipped []string, dry bool) {
	if len(skipped) == 0 {
		return
	}
	names := make([]string, len(skipped))
	for i, c := range skipped {
		names[i] = untrusted.Text(c, untrusted.MaxName)
	}
	if s.plain {
		for _, c := range names {
			fmt.Fprintf(w, "skipped\t%s\t%s\n", module, c)
		}
		return
	}
	verb := "skipped"
	if dry {
		verb = "would be skipped"
	}
	fmt.Fprintln(w, s.info(module+": "+verb+" in selections.yaml: "+strings.Join(names, ", ")))
}
