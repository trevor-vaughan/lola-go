package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// infoCmd is lola info (Plan 9 §4): a module, a source or a marketplace entry, fetched into the
// cache as cache add does, shown with what would run code and what installing it here would do.
func (a *app) infoCmd() *cobra.Command {
	var targets []string
	var ref, path string
	cmd := &cobra.Command{
		Use:     "info <source|@market/module|module>",
		Aliases: []string{"show", "view"},
		Short:   "Show a module, the code it runs and what installing it here would do",
		Long: "Show a module before you install it. The argument is what lola install takes: a module in your\n" +
			"cache, @market/module, or a source:\n" + source.AcceptedForms + ".\n" +
			"A source or a marketplace entry is fetched into your cache as lola cache add fetches it, so\n" +
			"installing the cached name later fetches nothing. lola info runs nothing: no install hook,\n" +
			"runtime hook, MCP server or script.\n" +
			"\n" +
			"It shows the module's identity and contents; what would run code, and when: each install\n" +
			"hook with its sha256 and first lines, the runtime hooks and MCP servers as the assistant\n" +
			"would run them; and what installing it here would do: the plan lola install -n prints, the\n" +
			"consent each hook needs, whether you gave it already, and the --trust kinds still needed.\n" +
			"Its last line is the install of exactly the version shown: a git source at the commit\n" +
			"fetched, anything else by its cached name or its marketplace address.\n" +
			"\n" +
			"Scope: --scope, --user, --project or -C; else a project with .lola/ at or above the current\n" +
			"directory; else default_scope in config.yaml (both shows both). lola info never asks for a\n" +
			"scope and never guesses one: without one it shows everything but the plan and hints the\n" +
			"runs that name one. -t names the assistants to plan for, as install's -t does.",
		Example: "  lola info ./my-module\n  lola info https://github.com/acme/review.git --ref v1.2.0 --project\n" +
			"  lola info @acme/formatter --user\n  lola info my-module -t claude-code --json",
		GroupID:           groupCore,
		Args:              cobra.MatchAll(infoArgs, cobra.MaximumNArgs(1)),
		ValidArgsFunction: a.completeInfo,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, isAddr, err := market.ParseAddress(args[0]); err != nil {
				return err
			} else if isAddr && path != "" {
				return errs.WithHints(fmt.Errorf("--path does not apply to %s: its catalog gives the path", untrusted.Text(args[0], untrusted.MaxName)),
					runWithout(cmd, args, "path"))
			}
			eng, err := a.engine()
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			scope, err := a.scopeRequest(cwd, false)
			if err != nil {
				return err
			}
			info, err := eng.Info(cmd.Context(), engine.InfoRequest{Cwd: cwd, Arg: args[0], Ref: ref, Path: path, Scope: scope, Targets: targets,
				Display: outputFlags(cmd),
				Rerun: func(extra ...string) string {
					return runWithout(cmd, args) + " " + errs.ShellJoin(extra)
				},
				UserRun: runWithout(cmd, args, scopeFlags...) + " --user",
				Install: func(arg, ref, path string, k engine.TrustKinds) string {
					words := []string{arg}
					for _, f := range [][2]string{{"--ref", ref}, {"--path", path}} {
						if f[1] != "" {
							words = append(words, f[0], f[1])
						}
					}
					run := runAs([]string{"lola", "install"}, cmd, words, "ref", "path")
					if k != 0 {
						run += " " + k.Arg()
					}
					return run
				}})
			if err != nil {
				return tagRefHint(cmd, args, err)
			}
			return a.emit(cmd, func(w io.Writer, s style) { printInfo(w, s, info) }, info)
		},
	}
	addTargetFlag(cmd, &targets, "assistants to plan for, as install's -t")
	cmd.Flags().StringVar(&ref, "ref", "", "git branch, tag or full commit SHA (git sources)")
	cmd.Flags().StringVar(&path, "path", "", "module root inside the source (monorepos)")
	return cmd
}

// infoArgs refuses a run with no argument, hinting the cache's listing with the run's output
// flags.
func infoArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return nil
	}
	return errs.WithHints(errors.New("lola info needs a module or source"),
		"cached modules: "+errs.ShellJoin(append([]string{"lola", "cache", "list"}, outputFlags(cmd)...)))
}

// completeInfo completes @ to a marketplace address, as install does, and anything else to a
// cached module's name, or as the shell completes a path.
func (a *app) completeInfo(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	if strings.HasPrefix(toComplete, "@") {
		return a.completeAddress(cmd, args, toComplete)
	}
	names, _ := a.completeStored(cmd, args, toComplete)
	return names, cobra.ShellCompDirectiveDefault
}

// recordedText is how human output says whether a consent is recorded; unknown says why, by
// whether a scope resolved.
func recordedText(recorded, scope string) string {
	switch {
	case recorded == engine.RecordedYes:
		return "recorded"
	case recorded == engine.RecordedNo:
		return "not recorded"
	case scope == "":
		return "unknown without a scope"
	}
	return "unknown until lola can plan here"
}

// printInfo prints lola info's five sections: the module, what would run code, what installing
// it would do at each scope, and the install of the version shown, or the hints that resolve a
// missing scope or assistant. A --plain row is the module's, then one per install hook
// ("hook", event, script, sha256), install script ("script", event, source, path, sha256, needs_trust), runtime hook (as lola install's "runtime-hook" row), MCP
// server ("mcp", name, target, command line, URL), refusal of a plan ("refusal", scope, error)
// followed by its hints ("hint", command), consent item ("consent", scope, kind, item,
// recorded), step, next step ("install", command), why no scope or assistant resolved
// ("no_scope" or "no_assistant", sentence) and hint ("hint", command).
func printInfo(w io.Writer, s style, info *engine.Info) {
	printModuleView(w, s, info.ModuleView)
	if s.plain {
		for _, h := range info.Runs.InstallHooks {
			fmt.Fprintf(w, "hook\t%s\t%s\t%s\n", h.Event, h.Script, h.SHA256)
		}
		for _, sr := range info.Runs.Scripts {
			fmt.Fprintf(w, "script\t%s\t%s\t%s\t%s\t%t\n", sr.Event, sr.Source, sr.Path, sr.SHA256, sr.NeedsTrust)
		}
		printRuntimeHooks(w, s, engine.ModuleResult{Name: info.Name, RuntimeHooks: info.Runs.RuntimeHooks})
		for _, m := range info.Runs.MCPServers {
			line := ""
			if m.Command != "" {
				line = errs.ShellJoin(slices.Concat([]string{m.Command}, m.Args))
			}
			fmt.Fprintf(w, "mcp\t%s\t%s\t%s\t%s\n", m.Name, m.Target, line, m.URL)
		}
		for _, in := range info.Install {
			if in.Refusal != nil {
				fmt.Fprintf(w, "refusal\t%s\t%s\n", in.Scope, strings.Join(refusalLines(in.Refusal.Error), "; "))
				for _, h := range in.Refusal.Hints {
					fmt.Fprintf(w, "hint\t%s\n", h)
				}
			}
			for _, c := range in.Consent {
				fmt.Fprintf(w, "consent\t%s\t%s\t%s\t%s\n", in.Scope, c.Kind, c.Item, c.Recorded)
			}
			for _, st := range in.Steps {
				fmt.Fprintln(w, stepLine(s, st))
			}
		}
		if len(info.Install) > 0 && info.Install[0].Command != "" {
			fmt.Fprintf(w, "install\t%s\n", info.Install[0].Command)
		}
		if info.NoScope != "" {
			fmt.Fprintf(w, "no_scope\t%s\n", info.NoScope)
		}
		if info.NoAssistant != "" {
			fmt.Fprintf(w, "no_assistant\t%s\n", info.NoAssistant)
		}
		for _, h := range info.Hints {
			fmt.Fprintf(w, "hint\t%s\n", h)
		}
		return
	}
	planned := info.NoAssistant == "" && slices.ContainsFunc(info.Install, func(in engine.InfoInstall) bool { return in.Scope != "" && in.Refusal == nil })
	printRuns(w, info.Runs, planned)
	for _, in := range info.Install {
		where := "without a scope"
		if in.Scope != "" {
			where = "at " + scopeLabel(engine.ScopeResult{Scope: in.Scope, Root: in.Root()})
		}
		if len(in.Targets) > 0 {
			where += " for " + strings.Join(in.Targets, ", ")
		}
		fmt.Fprintln(w, "install "+where+":")
		if in.Refusal != nil {
			lines := refusalLines(in.Refusal.Error)
			fmt.Fprintln(w, "  "+s.bad("installing here would refuse: "+lines[0]))
			for _, l := range lines[1:] {
				fmt.Fprintln(w, "    "+l)
			}
			for _, h := range in.Refusal.Hints {
				fmt.Fprintln(w, "    hint: "+h)
			}
		}
		for _, n := range in.Notices {
			fmt.Fprintln(w, "  "+noticeLine(s, n))
		}
		for _, st := range in.Steps {
			fmt.Fprintln(w, "  "+stepLine(s, st))
		}
		for _, f := range in.Findings {
			if line := findingLine(s, f); line != "" && (f.Severity != target.SeverityInfo || f.Hint != "") {
				fmt.Fprintln(w, "  "+strings.ReplaceAll(line, "\n", "\n  "))
			}
		}
		for _, c := range in.Consent {
			fmt.Fprintf(w, "  consent: %s %s: %s\n", c.Kind, c.Item, recordedText(c.Recorded, in.Scope))
		}
		if len(in.TrustNeeded) > 0 {
			fmt.Fprintf(w, "  needs --trust=%s\n", strings.Join(in.TrustNeeded, ","))
		}
	}
	switch {
	case info.NoScope != "":
		fmt.Fprintln(w, "no scope: "+info.NoScope)
	case info.NoAssistant != "":
		fmt.Fprintln(w, info.NoAssistant)
	case len(info.Install) > 0 && info.Install[0].Command != "":
		fmt.Fprintln(w, s.info("install it: "+info.Install[0].Command))
	}
	for _, h := range info.Hints {
		fmt.Fprintln(w, "  hint: "+h)
	}
}

// printRuns prints what in a module would run code: its install hooks with their sha256 and first
// lines, between the install scripts that would wrap them, then the runtime hooks and MCP servers per assistant, as rendered when planned, else as
// the module declares them; a heading starts each run of one assistant's rendered or declared
// hooks.
func printRuns(w io.Writer, r engine.InfoRuns, planned bool) {
	if len(r.InstallHooks)+len(r.RuntimeHooks)+len(r.MCPServers)+len(r.Scripts) == 0 {
		fmt.Fprintln(w, "runs code: nothing (no install hooks, install scripts, runtime hooks or MCP servers)")
		return
	}
	fmt.Fprintln(w, "runs code:")
	// In run order: the pre-install scripts, the hooks, then the post-install scripts.
	scripts := func(event string) {
		for _, sr := range r.Scripts {
			if sr.Event != event {
				continue
			}
			when := "around its install"
			if sr.NeedsTrust {
				when += " with your trust"
			}
			source := sr.Source
			if sr.Root() != "" {
				source += " at " + sr.Root()
			}
			fmt.Fprintf(w, "  %s script %s (%s, sha256 %s), %s\n", sr.Event, sr.Path, source, sr.SHA256, when)
		}
	}
	scripts(engine.PreInstall)
	for _, h := range r.InstallHooks {
		fmt.Fprintf(w, "  %s hook %s (sha256 %s), on install with your consent:\n", h.Event, h.Script, h.SHA256)
		for _, l := range h.FirstLines {
			fmt.Fprintln(w, "    | "+l)
		}
	}
	scripts(engine.PostInstall)
	for i, h := range r.RuntimeHooks {
		if i == 0 || h.Target != r.RuntimeHooks[i-1].Target || (h.Status == engine.RecordedUnknown) != (r.RuntimeHooks[i-1].Status == engine.RecordedUnknown) {
			if h.Status == engine.RecordedUnknown {
				fmt.Fprintf(w, "  runtime hooks as declared (rendered once a scope and an assistant are known), run by %s on its events, "+
					"installed with your consent:\n", h.Target)
			} else {
				fmt.Fprintf(w, "  runtime hooks, run by %s on its events, installed with your consent:\n", h.Target)
			}
		}
		for _, line := range runtimeHookLines(h) {
			fmt.Fprintln(w, "    "+line)
		}
	}
	for _, m := range r.MCPServers {
		runs := "runs: " + errs.ShellJoin(slices.Concat([]string{m.Command}, m.Args))
		if m.URL != "" {
			runs = "connects to: " + m.URL
		}
		as := ""
		if !planned {
			as = ", as declared"
		}
		fmt.Fprintf(w, "  MCP server %s%s, run by %s, %s\n", m.Name, as, m.Target, runs)
	}
}

// refusalLines is a refusal's message line by line, each trimmed: the human output indents them
// under the scope, and --plain joins them on its one row.
func refusalLines(msg string) []string {
	lines := strings.Split(msg, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return lines
}
