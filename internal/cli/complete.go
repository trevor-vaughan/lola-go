package cli

import (
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Shell completion reads local state only, through the engine's candidate readers: it writes
// nothing and touches no network. On any error it offers nothing, and no file names but where a
// path is a valid value. Descriptions are short and plain.

// offerList is the candidates among cands that complete the last item of toComplete, a
// comma-separated list: each is the items before the last, as typed, then the candidate. A
// candidate the list names already is not offered again; a candidate's description (after a tab)
// is kept.
func offerList(toComplete string, cands []cobra.Completion) []cobra.Completion {
	prefix, last := "", toComplete
	if i := strings.LastIndex(toComplete, ","); i >= 0 {
		prefix, last = toComplete[:i+1], toComplete[i+1:]
	}
	given := strings.Split(prefix, ",")
	var out []cobra.Completion
	for _, c := range cands {
		name, desc, _ := strings.Cut(c, "\t")
		if strings.HasPrefix(name, last) && !slices.Contains(given, name) && untrusted.IsPlain(name) && untrusted.IsPlain(desc) {
			out = append(out, prefix+c)
		}
	}
	return out
}

// completeTargets offers the assistants for -t, a comma-separated list: those found on this
// machine first, saying where, then the others, as not found, then the targets with no binary
// (agents), as set up where one of the command's scopes has their markers, else with what reads
// them.
func (a *app) completeTargets(cmd *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	eng, err := a.engine()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	marked := map[string]bool{} // set up in any of the run's scopes
	for _, sc := range a.markerScopes(cmd, eng) {
		for _, as := range eng.Detect(sc) {
			marked[as.Target] = marked[as.Target] || as.Marked
		}
	}
	var found, missing, binaryless []cobra.Completion
	for i, as := range eng.Detect(engine.Scope{Kind: target.User}) {
		switch t := eng.Targets[i]; {
		case target.NoBinary(t) && marked[as.Target]:
			binaryless = append(binaryless, cobra.CompletionWithDesc(as.Target, "set up here"))
		case target.NoBinary(t):
			binaryless = append(binaryless, cobra.CompletionWithDesc(as.Target, t.Probe().Readers))
		case as.Binary == "":
			missing = append(missing, cobra.CompletionWithDesc(as.Target, "not found on this machine"))
		case untrusted.IsPlain(as.Binary):
			found = append(found, cobra.CompletionWithDesc(as.Target, "found at "+as.Binary))
		default:
			found = append(found, cobra.CompletionWithDesc(as.Target, "found on this machine"))
		}
	}
	return offerList(toComplete, append(append(found, missing...), binaryless...)), cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveKeepOrder
}

// markerScopes is the scopes a run of cmd acts on, as completion reads them without asking: the one
// its flags or its directory name, or both under --scope both; none where its scope flags disagree.
func (a *app) markerScopes(cmd *cobra.Command, eng *engine.Engine) []engine.Scope {
	if sc, err := a.scope(cmd, eng); err == nil {
		return []engine.Scope{sc}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	req, err := a.scopeRequest(cwd, false)
	if err != nil {
		return nil
	}
	req.Command, req.Both = cmd.Name(), true // a.scope refuses --scope both
	res, err := eng.ResolveScope(req)
	if err != nil {
		return nil
	}
	var out []engine.Scope
	for _, c := range res.Scopes {
		out = append(out, c.Scope)
	}
	return out
}

// completeSource offers what install and check take: @ completes to a marketplace address
// (completeAddress); anything else to a cached or installed module's name, or a path.
func (a *app) completeSource(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if strings.HasPrefix(toComplete, "@") {
		return a.completeAddress(cmd, args, toComplete)
	}
	eng, err := a.engine()
	if err != nil {
		return nil, cobra.ShellCompDirectiveDefault
	}
	names, err := eng.Store.Names()
	if err != nil {
		return nil, cobra.ShellCompDirectiveDefault
	}
	if _, sc, err := a.session(cmd); err == nil {
		if installed, err := eng.InstalledVersions(sc); err == nil {
			for n := range installed {
				names = append(names, n)
			}
		}
	}
	slices.Sort(names)
	var out []cobra.Completion
	for _, n := range slices.Compact(names) {
		if strings.HasPrefix(n, toComplete) && !slices.Contains(args, n) {
			out = append(out, n)
		}
	}
	return out, cobra.ShellCompDirectiveDefault
}

// completeComponents offers, for --skip, --only and --unskip, the kind/name of each component
// of the modules named on the line (args[from:]), as the cache holds them, completing the last
// of a comma-separated list.
func (a *app) completeComponents(from int) cobra.CompletionFunc {
	return func(_ *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		eng, err := a.engine()
		if err != nil || len(args) <= from {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return offerList(toComplete, eng.ComponentNames(args[from:])), cobra.ShellCompDirectiveNoFileComp
	}
}

// completeProfiles offers the profiles a flag takes, as a comma-separated list, with base, the
// scope's base, first when the flag takes it.
func (a *app) completeProfiles(base bool) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		eng, err := a.engine()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		names, err := eng.ProfileNames()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		if base {
			names = append([]string{"base"}, names...)
		}
		return offerList(toComplete, names), cobra.ShellCompDirectiveNoFileComp
	}
}

// completeMove offers the modules lola profile [<from>] --move takes: those of the profile named,
// else of the scope, as a comma-separated list.
func (a *app) completeMove(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	eng, sc, err := a.session(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	from := ""
	if len(args) > 0 {
		from = args[0]
	}
	names, err := eng.MoveNames(sc, from)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return offerList(toComplete, names), cobra.ShellCompDirectiveNoFileComp
}

// completeAt offers --at's <module>=<version>: <module>= for each module installed in the scope,
// leaving the cursor after the =, then the version installed.
func (a *app) completeAt(cmd *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	eng, sc, err := a.session(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	installed, err := eng.InstalledVersions(sc)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	if name, _, typed := strings.Cut(toComplete, "="); typed {
		if v := installed[name]; v != "" && strings.HasPrefix(name+"="+v, toComplete) && untrusted.IsPlain(v) {
			return []cobra.Completion{name + "=" + v}, cobra.ShellCompDirectiveNoFileComp
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []cobra.Completion
	for name := range installed {
		if strings.HasPrefix(name, toComplete) && untrusted.IsPlain(name) {
			out = append(out, name+"=")
		}
	}
	slices.Sort(out)
	return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}

// completeTrash offers lola trash's arguments: the runs, newest first, with the command that made
// each; after a run, the originals of its entries, for a restore of some of them; with --empty,
// more runs.
func (a *app) completeTrash(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	eng, err := a.engine()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	if len(args) > 0 && !cmd.Flags().Changed("empty") {
		originals, err := eng.TrashOriginals(args[0])
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var out []cobra.Completion
		for _, o := range originals {
			if strings.HasPrefix(o, toComplete) && !slices.Contains(args[1:], o) && untrusted.IsPlain(o) {
				out = append(out, o)
			}
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}
	runs, err := eng.TrashNames()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []cobra.Completion
	for _, r := range runs {
		if strings.HasPrefix(r.ID, toComplete) && !slices.Contains(args, r.ID) && untrusted.IsPlain(r.ID) {
			c := r.ID
			if r.Command != "" && untrusted.IsPlain(r.Command) {
				c = cobra.CompletionWithDesc(r.ID, r.Command)
			}
			out = append(out, c)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveKeepOrder
}

// completeKinds offers the kind/ a tidy selector starts with, leaving the cursor after the slash:
// what can move is found by a scan that runs git, which completion never does, so the names are
// typed.
func completeKinds(_ *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	var out []cobra.Completion
	for _, k := range config.Kinds() {
		if strings.HasPrefix(k+"/", toComplete) {
			out = append(out, k+"/")
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}

// completeKept offers the selectors on the scope's keep-list, for lola tidy --unkeep.
func (a *app) completeKept(cmd *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	eng, sc, err := a.session(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names, err := eng.KeepNames(sc)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []cobra.Completion
	for _, n := range names {
		if strings.HasPrefix(n, toComplete) && untrusted.IsPlain(n) {
			out = append(out, n)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// completeDirs completes a flag or argument that takes a directory.
func completeDirs(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveFilterDirs
}

// completeFiles completes a flag that takes a path or a source, which may be a path.
func completeFiles(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveDefault
}
