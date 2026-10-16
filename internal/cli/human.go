package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
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

// stepLine renders one plan step.
func stepLine(s style, st plan.Step) string {
	dest := st.Path
	if st.Key != "" {
		dest += " " + st.Key
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
	default:
		return s.ok(line)
	}
}

func noticeLine(s style, n target.Notice) string {
	if s.plain {
		return "notice\t" + n.Level + "\t" + n.Text
	}
	if n.Level == target.LevelWarn {
		return s.warn(n.Text)
	}
	return s.info(n.Text)
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
