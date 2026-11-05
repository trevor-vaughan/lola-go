package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// tidyFlags are lola tidy's flags.
type tidyFlags struct {
	targets          []string
	keep, unkeep     []string
	all, yes, force  bool
	dry, withTracked bool
}

func (a *app) tidyCmd() *cobra.Command {
	var f tidyFlags
	cmd := &cobra.Command{
		Use:   "tidy [<kind/name>...]",
		Short: "Move what lola does not manage to the trash",
		Long: "Move what lola does not manage out of the directories and MCP server lists lola writes into\n" +
			"(.claude/skills, .claude/commands, .mcp.json, …) into the trash under\n" +
			"$XDG_STATE_HOME/lola/trash: files, directories and links lola never wrote or no longer records,\n" +
			"and MCP servers it did not install. Nothing is deleted; manifest.txt in each trash run says\n" +
			"where every item came from.\n\n" +
			"Name what moves as kind/name selectors (kinds: skill, command, agent, mcp, rule; the name may\n" +
			"be a glob, and \\ escapes * ? [ \\), or pass --all; on a terminal without either, lola offers what\n" +
			"can move in a picker, then the rest for your keep-list. lola asks before moving on a terminal; -y\n" +
			"answers, and --force also offers what git tracks and never asks. lola never moves CLAUDE.md,\n" +
			"AGENTS.md or GEMINI.md, anything holding one, what git tracks (without --include-tracked or\n" +
			"--force), or what your keep-list ($XDG_CONFIG_HOME/lola/keep.yaml) names; --keep and --unkeep\n" +
			"edit this scope's keep-list. -n lists what would move and what is locked, and why.\n\n" + scopeHelp,
		Example: "  lola tidy\n  lola tidy -n\n  lola tidy skill/old-review 'command/tmp-*' -y\n  lola tidy --all -y -t claude-code\n" +
			"  lola tidy --keep skill/notes",
		GroupID: groupCore,
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runTidy(cmd, args, f)
		},
	}
	cmd.Flags().BoolVar(&f.all, "all", false, "move every item that is not locked")
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&f.force, "force", false, "also move what git tracks, without asking; names or --all are still needed (never answers a consent question)")
	cmd.Flags().BoolVarP(&f.dry, "dry-run", "n", false, "list what would move and what is locked, and change nothing")
	cmd.Flags().BoolVar(&f.withTracked, "include-tracked", false, "also offer what git tracks")
	cmd.Flags().StringArrayVar(&f.keep, "keep", nil, "add a kind/name selector to this scope's keep-list (repeatable)")
	cmd.Flags().StringArrayVar(&f.unkeep, "unkeep", nil, "remove a selector from this scope's keep-list, as written there (repeatable)")
	addTargetFlag(cmd, &f.targets, "only tidy these assistants' directories")
	// Aliases for muscle memory (Plan 6d §3.2); lola tidy itself has none. Additive and stable;
	// the published table is pinned in store_test.go.
	aliasFlags(cmd, map[string][]string{"keep": {"protect"}, "unkeep": {"unprotect"}})
	return cmd
}

// tidyPatterns parses the selectors given as names or to flag ("" for names).
func tidyPatterns(flag string, vals []string) ([]config.Pattern, error) {
	var out []config.Pattern
	for _, v := range vals {
		p, err := config.ParsePattern(v)
		if err != nil {
			if flag != "" {
				return nil, fmt.Errorf("--%s: %w", flag, err)
			}
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (a *app) runTidy(cmd *cobra.Command, args []string, f tidyFlags) error {
	eng, err := a.engine()
	if err != nil {
		return err
	}
	req := engine.TidyRequest{Targets: f.targets, All: f.all, IncludeTracked: f.withTracked, Force: f.force, Yes: f.yes, Dry: f.dry,
		Display: outputFlags(cmd)}
	if req.Select, err = tidyPatterns("", args); err != nil {
		return err
	}
	if req.Keep, err = tidyPatterns("keep", f.keep); err != nil {
		return err
	}
	if req.Unkeep, err = tidyPatterns("unkeep", f.unkeep); err != nil {
		return err
	}
	if both, err := a.bothScopes(eng); err != nil || both != nil {
		if err != nil {
			return err
		}
		req.Scope = *both
		return eng.TidyOneScope(req)
	}
	scs, err := a.scopes(cmd, eng, args, "Tidy where?", false, false)
	if err != nil {
		return err
	}
	req.Scope = scs[0].Scope
	stderr := a.style(cmd)
	p := a.prompter(cmd)
	req.Interactive = p.Interactive()
	picked := false
	if req.Interactive {
		req.Choose = func(scan engine.TidyScan) (engine.TidyChoice, error) {
			picked = true
			return tidyChoose(p, scan)
		}
	}
	if req.Interactive && !f.yes && !f.force {
		req.Confirm = func(c engine.TidyChoice) (bool, error) {
			w := cmd.ErrOrStderr()
			for _, u := range c.Items {
				fmt.Fprintln(w, "  "+stderr.info(tidyLabel(u)))
			}
			for _, k := range c.KeepAdd {
				fmt.Fprintln(w, "  "+stderr.info("keep "+k))
			}
			for _, k := range c.KeepRemove {
				fmt.Fprintln(w, "  "+stderr.info("unkeep "+k))
			}
			switch {
			case !picked:
			case c.Command != "":
				fmt.Fprintln(w, "Equivalent command:\n  "+c.Command)
			case c.Hidden:
				fmt.Fprintln(w, "lola cannot show the equivalent command: it holds characters lola does not print or a word too long to print")
			default:
				fmt.Fprintln(w, "no single command selects this set")
			}
			var asks []string
			if len(c.Items) > 0 {
				asks = append(asks, "move "+engine.CountItems(len(c.Items))+" to the trash")
			}
			if n := len(c.KeepAdd); n > 0 {
				asks = append(asks, fmt.Sprintf("keep %d", n))
			}
			if n := len(c.KeepRemove); n > 0 {
				asks = append(asks, fmt.Sprintf("stop keeping %d", n))
			}
			q := strings.Join(asks, " and ")
			return p.Confirm(strings.ToUpper(q[:1]) + q[1:] + "?")
		}
	}
	res, err := eng.Tidy(cmd.Context(), req)
	if te, ok := errors.AsType[*engine.TidyError](err); ok && !a.json {
		printTidyLists(cmd.ErrOrStderr(), stderr, te.Result)
		printTidyEdits(cmd.ErrOrStderr(), stderr, te.Result)
	}
	if errors.Is(err, engine.ErrDeclined) {
		return ErrAborted
	}
	if err != nil {
		return err
	}
	return a.emit(cmd, func(w io.Writer, s style) { printTidy(w, s, res) }, res)
}

// tidyChoose asks lola tidy's picker steps (Plan 6d §3.4): which candidates move, then which
// of the rest join the keep-list. The keep step offers one option per escaped literal selector,
// since a keep entry covers every assistant's item of that name, and none for a selector that
// would also keep an item chosen to move (R-6d2-6, R-6d2-8). Choosing nothing in both cancels.
func tidyChoose(p Prompter, scan engine.TidyScan) (engine.TidyChoice, error) {
	opts := make([]TidyOption, len(scan.Candidates))
	for i, c := range scan.Candidates {
		opts[i] = TidyOption{ID: strconv.Itoa(i), Label: tidyLabel(c)}
	}
	locked := make([]string, len(scan.Protected))
	for i, u := range scan.Protected {
		locked[i] = tidyLabel(u) + "  " + u.Reason
	}
	move, err := p.TidyPick(TidyPickStep{Options: opts, Locked: locked})
	if err != nil {
		return engine.TidyChoice{}, err
	}
	var choice engine.TidyChoice
	var moved, sels []config.Pattern
	covers := map[config.Pattern][]string{}
	for i, c := range scan.Candidates {
		if slices.Contains(move, opts[i].ID) {
			choice.Move = append(choice.Move, c.ID)
			moved = append(moved, c.Literal())
		}
	}
	for _, c := range scan.Candidates {
		l := c.Literal()
		if slices.Contains(moved, l) {
			continue
		}
		if _, ok := covers[l]; !ok {
			sels = append(sels, l)
		}
		if !slices.Contains(covers[l], c.Target) {
			covers[l] = append(covers[l], c.Target)
		}
	}
	if len(sels) > 0 {
		rest := make([]TidyOption, len(sels))
		for i, l := range sels {
			rest[i] = TidyOption{ID: l.String(), Label: untrusted.Text(l.String(), untrusted.MaxName) + "  " + strings.Join(covers[l], ", ")}
		}
		keep, err := p.TidyKeep(TidyKeepStep{Options: rest})
		if err != nil {
			return engine.TidyChoice{}, err
		}
		for _, l := range sels {
			if slices.Contains(keep, l.String()) {
				choice.Keep = append(choice.Keep, l)
			}
		}
	}
	if len(choice.Move) == 0 && len(choice.Keep) == 0 {
		return engine.TidyChoice{}, ErrAborted
	}
	return choice, nil
}

// bothScopes is the project of a run whose --scope names two scopes, which lola tidy refuses
// (Plan 6d §3.3); nil when it names one or none.
func (a *app) bothScopes(eng *engine.Engine) (*engine.Scope, error) {
	if a.scopeFlag == "" {
		return nil, nil
	}
	names, err := engine.ParseScope(a.scopeFlag)
	if err != nil || len(names) < 2 {
		return nil, nil // ResolveScope reports a bad --scope
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	req, err := a.scopeRequest(cwd, false)
	if err != nil {
		return nil, err
	}
	req.Command, req.Both = "tidy", true
	res, err := eng.ResolveScope(req)
	if err != nil {
		return nil, err
	}
	return &res.Scopes[0].Scope, nil
}

// tidyLabel is how human output shows an item: target, kind/name with an MCP server's label,
// and where it is (a directory ending in /), with a link's target.
func tidyLabel(u engine.TidyUnit) string {
	name := u.Kind + "/" + u.Name
	if u.Name == "" {
		name = u.Kind
	}
	if u.Label != "" {
		name += " (" + u.Label + ")"
	}
	where := u.Rel
	if u.Dir {
		where += "/"
	}
	if u.Key != "" {
		where += " " + u.Key
	}
	if u.Link != "" {
		where += " → " + u.Link
	}
	return u.Target + "  " + name + "  " + where
}

// sizeText is n bytes in the largest unit that still counts at least 10 of them.
func sizeText(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for n >= 1024*10 && i < len(units)-1 {
		n /= 1024
		i++
	}
	return fmt.Sprintf("%d %s", n, units[i])
}

// printTidyLists prints what a tidy run found: what is locked, what was skipped, and what would
// move or moved.
func printTidyLists(w io.Writer, s style, r *engine.TidyResult) {
	if s.plain {
		for _, l := range []struct {
			name  string
			items []engine.TidyUnit
		}{{"locked", r.Protected}, {"skipped", r.Skipped}, {"candidate", r.Candidates}, {"moved", r.Moved}} {
			for _, u := range l.items {
				fmt.Fprintln(w, strings.Join([]string{l.name, u.Target, u.Kind, u.Name, u.Rel, u.Key, u.Code, u.Reason,
					untrusted.Text(u.Trash, untrusted.MaxName)}, "\t"))
			}
		}
		if r.Trash != "" {
			fmt.Fprintf(w, "trash\t%s\t%d\n", untrusted.Text(r.Trash, untrusted.MaxName), r.SizeBytes)
		}
		for _, c := range r.TrashCredentials {
			fmt.Fprintln(w, "credentials\t"+untrusted.Text(c, untrusted.MaxName))
		}
		return
	}
	for _, u := range r.Protected {
		fmt.Fprintln(w, "  "+s.warn("locked  "+tidyLabel(u)+"  "+u.Reason))
	}
	for _, u := range r.Skipped {
		fmt.Fprintln(w, "  "+s.warn("skipped  "+tidyLabel(u)+"  "+u.Reason))
	}
	if len(r.Candidates) > 0 {
		fmt.Fprintln(w, "would move "+engine.CountItems(len(r.Candidates))+":")
		for _, u := range r.Candidates {
			fmt.Fprintln(w, "  "+s.info(tidyLabel(u)))
		}
	}
	if len(r.Moved) > 0 {
		fmt.Fprintln(w, s.ok(fmt.Sprintf("moved %s (%s) to the trash: %s", engine.CountItems(len(r.Moved)), sizeText(r.SizeBytes),
			untrusted.Text(r.Trash, untrusted.MaxName))))
		for _, u := range r.Moved {
			fmt.Fprintln(w, "  "+s.info(tidyLabel(u)))
		}
	}
	var copies []string
	for _, u := range r.Moved {
		if u.Key != "" && !slices.Contains(copies, u.Rel) {
			copies = append(copies, u.Rel)
			fmt.Fprintln(w, s.warn("the trash run holds a copy of "+u.Rel+", which may contain credentials"))
		}
	}
}

// printTidy renders a tidy run (Plan 6d §11.1).
func printTidy(w io.Writer, s style, r *engine.TidyResult) {
	if !s.plain {
		where := r.Scope
		if r.Root != "" {
			where += " " + untrusted.Text(r.Root, untrusted.MaxName)
		}
		fmt.Fprintln(w, "Tidy "+where+" — "+strings.Join(r.Targets, ", "))
	}
	printTidyLists(w, s, r)
	printTidyEdits(w, s, r)
	for _, h := range r.Hints {
		if s.plain {
			fmt.Fprintln(w, "hint	"+h)
			continue
		}
		fmt.Fprintln(w, s.info(h))
	}
}

// printTidyEdits prints what a tidy run wrote to the keep-list and its notices, which a run that
// stopped reports too.
func printTidyEdits(w io.Writer, s style, r *engine.TidyResult) {
	scope := "user scope"
	if r.Root != "" {
		scope = "project " + untrusted.Text(r.Root, untrusted.MaxName)
	}
	for _, k := range []struct {
		verb    string
		entries []string
	}{{"added", r.KeepAdded}, {"removed", r.KeepRemoved}} {
		for _, e := range k.entries {
			if s.plain {
				fmt.Fprintln(w, "keep	"+k.verb+"	"+e)
				continue
			}
			fmt.Fprintln(w, s.ok("keep-list: "+k.verb+" "+e+" ("+scope+")"))
		}
	}
	for _, n := range r.Notices {
		fmt.Fprintln(w, noticeLine(s, n))
	}
}
