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
	dest := st.Path
	if st.Key != "" {
		dest += " " + st.Key
	}
	if st.Value != "" {
		dest += " " + strconv.Quote(st.Value)
	}
	if st.Member != nil {
		dest += " " + untrusted.Text(string(st.Member), memberShown)
	}
	if st.Via != "" {
		dest += " (via symlink → " + st.Via + ")"
	}
	if s.plain {
		return strings.Join([]string{string(st.Action), st.Module, st.Target, st.Kind, dest, st.Forced, st.Note}, "\t")
	}
	line := fmt.Sprintf("%-6s %-11s %s", st.Action, st.Target, dest)
	if st.Forced != "" {
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

// printResult renders an install/uninstall/render result: steps, MCP transparency, install
// hooks, notices, and a summary line.
func printResult(w io.Writer, s style, verb string, r *engine.Result) {
	if r.DryRun && !s.plain {
		fmt.Fprintln(w, "plan (dry run; nothing was written):")
	}
	for _, st := range r.Steps {
		fmt.Fprintln(w, "  "+stepLine(s, st))
	}
	for _, m := range r.Modules {
		if len(m.MCPServers) > 0 && !s.plain {
			fmt.Fprintf(w, "%s MCP servers:\n", m.Name)
			for _, srv := range m.MCPServers {
				fmt.Fprintf(w, "    %s (%s) runs: %s\n", srv.Name, srv.Transport, srv.Runs)
			}
		}
		printRuntimeHooks(w, s, m)
		for _, h := range m.Hooks {
			if s.plain {
				fmt.Fprintf(w, "hook\t%s\t%s\t%s\t%s\t%s\n", m.Name, h.Event, h.Script, h.Target, h.Status)
				continue
			}
			line := fmt.Sprintf("%s %s hook %s (%s): %s", m.Name, h.Event, h.Script, h.Target, h.Status)
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
	for _, f := range r.Findings {
		if f.Severity == target.SeverityInfo && f.Hint == "" {
			continue // an info finding with a hint is something the user can act on
		}
		if line := findingLine(s, f); line != "" {
			fmt.Fprintln(w, line)
		}
	}
	for _, n := range r.Notices {
		fmt.Fprintln(w, noticeLine(s, n))
	}
	if r.DryRun || s.plain {
		return
	}
	for _, m := range r.Modules {
		fmt.Fprintln(w, s.ok(fmt.Sprintf("%s %s (%s)", verb, m.Name, strings.Join(m.Targets, ", "))))
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
