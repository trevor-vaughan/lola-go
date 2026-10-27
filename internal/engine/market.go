package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
	"github.com/trevor-vaughan/lola-go/internal/yamldoc"
)

// MarketAction is one of lola market's actions (Plan 6c §3.1); each is also the flag lola market
// takes for it.
type MarketAction string

// lola market's actions.
const (
	MarketAdd    MarketAction = "add"
	MarketUpdate MarketAction = "update"
)

// What became of one marketplace of an --add or --update run (MarketOutcome.Outcome).
const (
	MarketAdded       = "added"        // fetched and registered
	MarketWouldAdd    = "would_add"    // a dry run: it would be registered
	MarketUpdated     = "updated"      // fetched again; the cached catalog is replaced
	MarketWouldUpdate = "would_update" // a dry run: the cached catalog would be replaced
	MarketUpToDate    = "up_to_date"   // fetched again; the catalog has not changed
	MarketFailed      = "failed"       // it could not be fetched, read or written; the cached catalog is kept
	MarketStopped     = "skipped"      // the run stopped (Ctrl+C) before it was fetched; the cached catalog is kept
)

// The files of a cached marketplace, <paths.MarketCache>/<name>/ (Plan 6c §5).
const (
	marketPinFile = "source.json"
	maxPinFile    = 1 << 20
)

// notFetched is how a marketplace without a cached catalog lists.
const notFetched = "not fetched"

// MarketRow is a marketplace as lola market lists it (Plan 6c §11.2): its registration, and from
// its cached catalog the format, the commit, when it was fetched (RFC 3339, "" when not), the
// counts of entries and skipped entries, and the catalog's description. Fetched is the age for
// people ("2 days ago", "not fetched"). Every string has been through untrusted.Text.
type MarketRow struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Ref         string `json:"ref"`
	Path        string `json:"path"`
	Format      string `json:"format"`
	Commit      string `json:"commit"`
	FetchedAt   string `json:"fetched_at"`
	Entries     int    `json:"entries"`
	Skipped     int    `json:"skipped"`
	Description string `json:"description"`
	Fetched     string `json:"-"`
}

// MarketView is one marketplace as lola market <name> shows it (Plan 6c §11.1, §11.2): its row,
// the catalog file, owner and version, every entry it offers and every entry it skips, and the
// notices. Every string has been through untrusted.Text.
type MarketView struct {
	MarketRow
	File    string        `json:"file"`
	Owner   string        `json:"owner"`
	Version string        `json:"version"`
	Entries []MarketEntry `json:"entries"`
	Skipped []MarketSkip  `json:"skipped"`
	Notices []Notice      `json:"notices"`
}

// MarketEntry is one entry a catalog offers, mapped to the source lola would install it from.
type MarketEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	Tags        []string `json:"tags"`
	Source      string   `json:"source"`
	Ref         string   `json:"ref"`
	Path        string   `json:"path"`
	Commit      string   `json:"commit"`
	Kind        string   `json:"kind"`
	// Install is the address lola install takes for the entry, @<market>/<entry>.
	Install string   `json:"install"`
	Notices []string `json:"notices"`
	// Components is what the entry's own lists select from its directory, nil when it lists none.
	Components *market.Components `json:"components,omitempty"`
}

// MarketSkip is an entry lola does not offer, and why.
type MarketSkip struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// MarketChange is how an --update changed one entry of a marketplace: "added", "removed", or
// "changed" with the fields that differ (source, path, ref, commit, version, components).
type MarketChange struct {
	Market string   `json:"market"`
	Entry  string   `json:"entry"`
	Change string   `json:"change"`
	Fields []string `json:"fields"`
}

// MarketOutcome is what became of one marketplace of the run, in the order they were named (by
// registration for --all): the outcome, and for a failure its error and hints (Err, the error
// itself, for the caller to return when it is the run's only marketplace).
type MarketOutcome struct {
	Market  string   `json:"market"`
	Outcome string   `json:"outcome"`
	Error   string   `json:"error"`
	Hints   []string `json:"hints"`
	Err     error    `json:"-"`
}

// MarketResult is what lola market --add and --update report (Plan 6c §11.2): the marketplaces
// fetched, the changes to their entries, every marketplace's outcome, and the notices.
type MarketResult struct {
	Markets []MarketRow     `json:"markets"`
	Changes []MarketChange  `json:"changes"`
	Updates []MarketOutcome `json:"updates"`
	Notices []Notice        `json:"notices"`
	DryRun  bool            `json:"dry_run"`
}

// MarketRename asks for the name of a marketplace being added (Plan 6c §3.1, ruling R-6c-11): Why
// the name it has will not do (another marketplace has it, from its source; or the catalog names
// itself what no marketplace can be), Suggest the first free name ("" for none), and Problem why
// the last answer was refused ("" on the first ask).
type MarketRename struct {
	Why, Suggest, Problem string
}

// MarketAddRequest is one lola market [<name>] --add <source> (Plan 6c §3.1, §6.2): fetch Source
// at Ref, find its catalog (under Path, a file or a directory), and register it as Name, else the
// catalog's own name. Rename asks for another name when it is taken by another source, or when
// the catalog's own name cannot name a marketplace; nil means it cannot ask, and the run is
// refused with a hint naming a free one.
type MarketAddRequest struct {
	Name, Source, Ref, Path string
	DryRun                  bool
	// PythonName, set by lola migrate, is the name the Python lola registered the marketplace
	// under: without Path, a tree's catalog is then the file the Python lola read (pythonCatalog),
	// never lola's own lookup (Plan 6c §6.9).
	PythonName string
	// Display is the run's output flags (--json, --plain, --no-pager as given), which every
	// command a hint or notice names keeps.
	Display []string
	Rename  func(MarketRename) (string, error)
}

// MarketUpdateRequest is one lola market <name>... --update, or --update --all (Plan 6c §3.1).
type MarketUpdateRequest struct {
	Names  []string
	All    bool
	DryRun bool
	// Display is the run's output flags, which every command a hint or notice names keeps.
	Display []string
}

// MarketRun is the lola market command with args, each through untrusted.Text, quoted for the
// user's shell: what a hint offers to run.
func MarketRun(args ...string) string {
	cmd := []string{"lola", "market"}
	for _, a := range args {
		cmd = append(cmd, untrusted.Text(a, untrusted.MaxName))
	}
	return errs.ShellJoin(cmd)
}

// addRun is the arguments of an --add, as a hint repeats it with name (none when "") and path
// (none when ""): the source, the ref, -n of a dry run (Plan 6c §11.3: a slug, collision and
// two-catalog hint keeps --ref, --path and -n, never -y), and display, the run's output flags.
func addRun(name, src, ref, p string, dry bool, display []string) []string {
	var out []string
	if name != "" {
		out = append(out, name)
	}
	out = append(out, "--add", src)
	if ref != "" {
		out = append(out, "--ref", ref)
	}
	if p != "" {
		out = append(out, "--path", p)
	}
	if dry {
		out = append(out, "-n")
	}
	return append(out, display...)
}

// marketSource is a marketplace's own source as typed, classified: a catalog file at a URL
// (file), or a tree, a git repository, an archive or a local folder, to fetch whole (spec).
type marketSource struct {
	loc  string
	file bool
	spec source.Spec
}

// marketSource classifies src, the source you typed (Plan 6c §6.2 step 1), with the rules of a
// module's source, since it is your own argument, so a local folder is looked up: an https:// (or
// loopback http://) URL whose path ends in .json, .yml or .yaml is a catalog file; anything else
// is a git repository, an archive at an https URL, or a folder, made absolute with its links
// resolved (ruling R-6c-2). A local catalog file or archive is refused, coached to the folder that
// holds the file. Every recorded-source rule is checked before any network contact.
func (e *Engine) marketSource(name, src, ref, p string, dry bool, display []string) (marketSource, error) {
	shownSrc := untrusted.Text(src, untrusted.MaxName)
	if market.IsCatalogURL(src) {
		// One refusal names every flag a catalog file cannot take, so its hint runs.
		var flags []string
		if ref != "" {
			flags = append(flags, "--ref")
		}
		if p != "" {
			flags = append(flags, "--path")
		}
		switch len(flags) {
		case 1:
			return marketSource{}, errs.WithHints(fmt.Errorf("%s applies to a git repository, archive or folder, and %s is a catalog file", flags[0], shownSrc),
				MarketRun(addRun(name, src, "", "", dry, display)...))
		case 2:
			return marketSource{}, errs.WithHints(fmt.Errorf("--ref and --path apply to a git repository, archive or folder, and %s is a catalog file", shownSrc),
				MarketRun(addRun(name, src, "", "", dry, display)...))
		}
		if err := (source.Recorded{Source: src}).Check(); err != nil {
			return marketSource{}, unhinted(err)
		}
		return marketSource{loc: src, file: true}, nil
	}
	cwd, err := e.Getwd()
	if err != nil {
		return marketSource{}, err
	}
	if !strings.Contains(src, "://") && market.FormatOf(src) != "" {
		abs := src
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(cwd, src)
		}
		if fi, err := os.Stat(abs); err == nil && fi.Mode().IsRegular() {
			dir, file := filepath.Dir(abs), filepath.Base(abs)
			if filepath.Base(dir) == ".claude-plugin" {
				dir, file = filepath.Dir(dir), ".claude-plugin/"+file
			}
			return marketSource{}, errs.WithHints(fmt.Errorf("%s is a catalog file on this machine; lola reads a local marketplace from the folder that holds it",
				shownSrc), MarketRun(addRun(name, dir, "", file, dry, display)...))
		}
	}
	spec, ok, err := source.Parse(src, cwd)
	const forms = "give a git repository (https://, ssh:// or user@host:path), an https:// archive or catalog file (.json, .yml, .yaml), or a folder"
	switch {
	case err != nil:
		return marketSource{}, unhinted(err)
	case !ok && strings.Contains(src, "://"):
		return marketSource{}, fmt.Errorf("%s is not a marketplace source: %s", shownSrc, forms)
	case !ok:
		return marketSource{}, fmt.Errorf("%s is no folder here, and no URL: %s", shownSrc, forms)
	case spec.Kind == source.KindArchive && source.IsLocal(spec.Kind, spec.Location):
		return marketSource{}, fmt.Errorf("%s is an archive on this machine; lola reads a local marketplace from a folder, so extract it and add that folder", shownSrc)
	case ref != "" && spec.Kind != source.KindGit:
		return marketSource{}, errs.WithHints(fmt.Errorf("--ref applies to git sources only, not to %s %s", spec.Kind, shownSrc),
			MarketRun(addRun(name, src, "", p, dry, display)...))
	}
	spec.Ref = ref
	if err := (source.Recorded{Source: spec.Location, Ref: ref, Path: p}).Check(); err != nil {
		return marketSource{}, unhinted(err)
	}
	return marketSource{loc: spec.Location, spec: spec}, unhinted(source.Validate(spec))
}

// unhinted is err without the advice the source, file and lock layers attach as hints: a
// marketplace's hints are commands only (Plan 6c §11.3), and the message still says what failed.
// A source.AccessError keeps its hints, which are commands.
func unhinted(err error) error {
	if err == nil || len(errs.Hints(err)) == 0 || errors.As(err, new(*source.AccessError)) {
		return err
	}
	return errors.New(err.Error())
}

// registrySource is a registration's source, classified from its text as a recorded one: it was
// checked when it was added, and markets.yaml's load checked it again (market.ParseRegistry).
func registrySource(r market.Registration) marketSource {
	if market.IsCatalogURL(r.Source) {
		return marketSource{loc: r.Source, file: true}
	}
	kind := source.KindArchive
	switch {
	case source.IsFolderForm(r.Source):
		kind = source.KindFolder
	case source.IsGitForm(r.Source):
		kind = source.KindGit
	}
	return marketSource{loc: r.Source, spec: source.Spec{Kind: kind, Location: r.Source, Ref: r.Ref}}
}

// readMarkets takes the markets lock shared, for a run that reads your marketplaces (Plan 6c §5),
// sweeping what a killed writer left in the cache first (readSwept); dry says the run writes
// nothing.
func (e *Engine) readMarkets(dry bool) (func(), error) {
	unlock, err := readSwept(e.Paths.MarketsLock, e.Paths.MarketCache, market.ValidateName, dry)
	return unlock, unhinted(err)
}

// registry reads markets.yaml (Plan 6c §5) with yamldoc's guards, as a file you edit by hand, and
// returns it with its bytes; a missing file is no marketplaces.
func (e *Engine) registry() (market.Registry, []byte, error) {
	_, b, err := yamldoc.Read(e.Paths.Markets, market.RegistryVersion)
	if err != nil {
		return market.Registry{}, nil, unhinted(err)
	}
	reg, err := market.ParseRegistry(e.Paths.Markets, b)
	return reg, b, err
}

// cachedMarket is a marketplace's cached catalog, bound to its pin.
type cachedMarket struct {
	pin market.Pin
	cat market.Catalog
}

// catalogName is the name of a cached catalog file of format.
func catalogName(format string) string {
	if format == market.FormatClaude {
		return "catalog.json"
	}
	return "catalog.yml"
}

// loadMarket reads the cached catalog of r (Plan 6c §5): nil when there is none. The directory and
// its files must be what lola wrote, never a symbolic link; source.json must check, name r's
// source, ref and path, and hold the digest of the catalog's bytes; and the catalog must parse.
func (e *Engine) loadMarket(r market.Registration) (*cachedMarket, error) {
	dir := filepath.Join(e.Paths.MarketCache, r.Name)
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, err
	case !fi.IsDir():
		return nil, fmt.Errorf("%s is not a directory lola wrote", shown(dir))
	}
	b, err := fsx.ReadOwn(filepath.Join(dir, marketPinFile), maxPinFile)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", shown(filepath.Join(dir, marketPinFile)), untrusted.CleanErr(err))
	}
	pin, err := market.ParsePin(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", shown(filepath.Join(dir, marketPinFile)), err)
	}
	if pin.Source != r.Source || pin.Ref != r.Ref || pin.Path != r.Path {
		return nil, fmt.Errorf("its cached catalog was fetched from %s, and markets.yaml now names %s", marketWhere(pin.Source, pin.Ref, pin.Path),
			marketWhere(r.Source, r.Ref, r.Path))
	}
	format := market.FormatOf(pin.File)
	file := filepath.Join(dir, catalogName(format))
	data, err := fsx.ReadOwn(file, market.MaxCatalog)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", shown(file), untrusted.CleanErr(err))
	}
	if profile.Digest(data) != pin.Digest {
		return nil, fmt.Errorf("%s changed since lola fetched it: its digest is not the one %s records", shown(file), marketPinFile)
	}
	cat, err := market.Parse(format, data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", shown(file), err)
	}
	return &cachedMarket{pin: pin, cat: market.Bind(cat, pin)}, nil
}

// marketWhere is a marketplace's source with its ref and path, through untrusted.Text, for a
// message.
func marketWhere(src, ref, p string) string {
	out := untrusted.Text(src, untrusted.MaxName)
	if ref != "" {
		out += " (ref " + untrusted.Text(ref, untrusted.MaxName) + ")"
	}
	if p != "" {
		out += " at " + untrusted.Text(p, untrusted.MaxName)
	}
	return out
}

// age is how long ago t was, for people.
func age(now, t time.Time) string {
	d := now.Sub(t)
	plural := func(n int, unit string) string {
		if n == 1 {
			return "1 " + unit + " ago"
		}
		return fmt.Sprintf("%d %ss ago", n, unit)
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute")
	case d < 24*time.Hour:
		return plural(int(d/time.Hour), "hour")
	}
	return plural(int(d/(24*time.Hour)), "day")
}

// marketRow is r's row, with its cached catalog c (nil for none).
func (e *Engine) marketRow(r market.Registration, c *cachedMarket) MarketRow {
	row := MarketRow{Name: shown(r.Name), Source: shown(r.Source), Ref: shown(r.Ref), Path: shown(r.Path), Fetched: notFetched}
	if c != nil {
		row.Format, row.Commit = c.cat.Format, shown(c.pin.Commit)
		row.FetchedAt = c.pin.FetchedAt.UTC().Format(time.RFC3339)
		row.Fetched = age(e.Now(), c.pin.FetchedAt)
		row.Entries, row.Skipped = len(c.cat.Entries), len(c.cat.Skipped)
		row.Description = shown(c.cat.Description)
	}
	return row
}

// Markets lists your marketplaces (Plan 6c §3.1, §11): each registration of markets.yaml, in its
// order, with what its cached catalog holds. It never fetches and never asks. A marketplace whose
// cache cannot be read lists as not fetched, with a notice saying why and the --update that
// fetches it again; display is the run's output flags, which the commands it names keep.
func (e *Engine) Markets(display []string) ([]MarketRow, []Notice, error) {
	unlock, err := e.readMarkets(true)
	if err != nil {
		return nil, nil, err
	}
	defer unlock()
	reg, _, err := e.registry()
	if err != nil {
		return nil, nil, err
	}
	rows, notices := []MarketRow{}, []Notice{}
	for _, r := range reg.Markets {
		c, err := e.loadMarket(r)
		switch {
		case err != nil:
			notices = append(notices, Notice{Level: LevelWarn, Text: shown(r.Name) + ": " + shown(untrusted.CleanErr(err).Error()) +
				" → " + MarketRun(append([]string{r.Name, "--update"}, display...)...)})
		case c == nil:
			notices = append(notices, Notice{Level: LevelInfo, Text: shown(r.Name) + " is " + notFetched + " → " + MarketRun(append([]string{r.Name, "--update"}, display...)...)})
		}
		rows = append(rows, e.marketRow(r, c))
	}
	return rows, notices, nil
}

// MarketNames is your marketplaces' names, for completion.
func (e *Engine) MarketNames() ([]string, error) {
	reg, _, err := e.registry()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range reg.Markets {
		out = append(out, r.Name)
	}
	return out, nil
}

// noMarket refuses name, which reg has no marketplace of (Plan 6c §6.3), naming those close to it:
// the hints are run of each close name (the lola market arguments that repeat the run with it in
// name's place; nil for none), then the listing, each with display, the run's output flags.
func noMarket(reg market.Registry, name string, run func(close string) []string, display []string) error {
	msg := "you have no marketplace " + untrusted.Text(name, untrusted.MaxName)
	var hints []string
	near := market.CloseMarkets(reg, name)
	if len(near) > 0 {
		msg += "; close names: " + strings.Join(near, ", ")
	}
	for _, n := range near {
		if run != nil {
			hints = append(hints, MarketRun(append(run(n), display...)...))
		}
	}
	return errs.WithHints(errors.New(msg), append(hints, MarketRun(display...))...)
}

// ShowMarket shows the marketplace name (Plan 6c §3.1, §11.1): its registration, its catalog's
// file, owner and description, every entry with the source lola maps it to, and every skipped
// entry with its reason, each list by name. It never fetches and never asks. display is the run's output flags, which
// the commands it names keep.
func (e *Engine) ShowMarket(name string, display []string) (*MarketView, error) {
	unlock, err := e.readMarkets(true)
	if err != nil {
		return nil, err
	}
	defer unlock()
	reg, _, err := e.registry()
	if err != nil {
		return nil, err
	}
	r := reg.Find(name)
	if r == nil {
		return nil, noMarket(reg, name, func(n string) []string { return []string{n} }, display)
	}
	update := MarketRun(append([]string{name, "--update"}, display...)...)
	c, err := e.loadMarket(*r)
	if err != nil {
		return nil, errs.WithHints(fmt.Errorf("marketplace %s: %w", shown(name), untrusted.CleanErr(err)), update)
	}
	v := &MarketView{MarketRow: e.marketRow(*r, c), Entries: []MarketEntry{}, Skipped: []MarketSkip{}, Notices: []Notice{}}
	if c == nil {
		v.Notices = append(v.Notices, Notice{Level: LevelInfo, Text: notFetched + " → " + update})
		return v, nil
	}
	v.File, v.Owner, v.Version = shown(c.pin.File), shown(c.cat.Owner), shown(c.cat.Version)
	for _, en := range c.cat.Entries {
		me := MarketEntry{Name: shown(en.Name), Description: shown(en.Description), Version: shown(en.Version), Tags: []string{},
			Source: shown(en.Source.Source), Ref: shown(en.Source.Ref), Path: shown(en.Source.Path), Commit: shown(en.Source.Commit),
			Kind: en.Kind, Notices: []string{}}
		for _, t := range en.Tags {
			me.Tags = append(me.Tags, shown(t))
		}
		for _, n := range en.Notices {
			me.Notices = append(me.Notices, shown(n))
		}
		if c := en.Components; c != nil {
			me.Components = &market.Components{Skills: shownAll(c.Skills), Commands: shownAll(c.Commands), Agents: shownAll(c.Agents)}
		}
		me.Install = "@" + r.Name + "/" + me.Name
		v.Entries = append(v.Entries, me)
	}
	for _, s := range c.cat.Skipped {
		v.Skipped = append(v.Skipped, MarketSkip{Name: shown(s.Name), Reason: shown(s.Reason)})
	}
	// By name, not in catalog order, so a long catalog reads like a listing (an entry's name is
	// unique in its catalog, so the order is total; a skip's may repeat, and keeps its order).
	slices.SortFunc(v.Entries, func(a, b MarketEntry) int { return strings.Compare(a.Name, b.Name) })
	slices.SortStableFunc(v.Skipped, func(a, b MarketSkip) int { return strings.Compare(a.Name, b.Name) })
	return v, nil
}

// shownAll is ss, each through untrusted.Text; never nil.
func shownAll(ss []string) []string {
	out := []string{}
	for _, x := range ss {
		out = append(out, shown(x))
	}
	return out
}

// fetchedMarket is a marketplace's catalog as a fetch found it, before anything is written: its
// pin and bytes, and the catalog bound to the pin.
type fetchedMarket struct {
	pin  market.Pin
	data []byte
	cat  market.Catalog
}

// catalogIn finds the catalog of the tree fetched at tree (Plan 6c §6.2 step 3): the file p names,
// or in the directory it names, else at the root, the first of market.CatalogFiles that is there;
// two of them refuse, with the --add of each as hints (add builds it from the file; nil for
// none). Every directory on the way and the file must be real, never a symbolic link. It
// returns the file as a slash path in the tree.
func catalogIn(tree, name, p string, add func(string) string) (string, error) {
	dir := ""
	if p != "" {
		clean, err := market.CheckRelative(p)
		if err != nil {
			return "", fmt.Errorf("--path %w", err)
		}
		if clean == "." {
			clean = ""
		}
		fi, err := realPath(tree, clean)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return "", fmt.Errorf("--path %s is not in the source", untrusted.Text(p, untrusted.MaxName))
		case err != nil:
			return "", err
		case fi.Mode().IsRegular() && market.FormatOf(clean) == "":
			return "", fmt.Errorf("--path %s is not a catalog: its name does not end in .json, .yml or .yaml", untrusted.Text(p, untrusted.MaxName))
		case fi.Mode().IsRegular():
			return clean, nil
		case !fi.IsDir():
			return "", fmt.Errorf("--path %s is neither a file nor a directory", untrusted.Text(p, untrusted.MaxName))
		}
		dir = clean
	}
	var found []string
	for _, f := range market.CatalogFiles(name) {
		rel := path.Join(dir, f)
		fi, err := realPath(tree, rel)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return "", err
		case fi.Mode().IsRegular():
			found = append(found, rel)
		default:
			return "", fmt.Errorf("%s is not a regular file", untrusted.Text(rel, untrusted.MaxName))
		}
	}
	where := "at the source's root"
	if dir != "" {
		where = "under --path " + untrusted.Text(dir, untrusted.MaxName)
		if add == nil {
			where = "under path " + untrusted.Text(dir, untrusted.MaxName) // markets.yaml's, on --update
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("the source has no catalog %s: lola looked for %s", where, strings.Join(market.CatalogFiles(name), ", "))
	case 1:
		return found[0], nil
	}
	if add == nil {
		return "", fmt.Errorf("the source has %d catalogs %s, %s; set path: in markets.yaml to the one to read", len(found), where, andJoin(found))
	}
	var hints []string
	for _, f := range found {
		hints = append(hints, add(f))
	}
	return "", errs.WithHints(fmt.Errorf("the source has %d catalogs %s, %s; name the one to read with --path", len(found), where,
		andJoin(found)), hints...)
}

// ErrNoPythonCatalog is wrapped by the refusal of a tree that holds no catalog the Python lola
// would have read (pythonCatalog): lola cannot tell which file it read, so no command can say.
var ErrNoPythonCatalog = errors.New("no catalog the Python lola would read")

// pythonCatalog is the catalog file the Python lola read at the root of tree for the marketplace
// it named name ([P] models.py Marketplace._pick_marketplace_yaml): <name>.yml or .yaml, then
// marketplace.yml or .yaml, else the only .yml or .yaml file at the root but
// .pre-commit-config.yaml. The file must be a regular file, never a symbolic link (realPath).
func pythonCatalog(tree, name string) (string, error) {
	named := []string{name + ".yml", name + ".yaml", "marketplace.yml", "marketplace.yaml"}
	for _, f := range named {
		if !untrusted.IsPlain(f) || strings.ContainsAny(f, `/\`) {
			continue
		}
		fi, err := realPath(tree, f)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return "", err
		case fi.Mode().IsRegular():
			return f, nil
		default:
			return "", fmt.Errorf("%s is not a regular file", untrusted.Text(f, untrusted.MaxName))
		}
	}
	ents, err := os.ReadDir(tree)
	if err != nil {
		return "", untrusted.CleanErr(err)
	}
	var ymls []string
	for _, ent := range ents {
		n := ent.Name()
		if (strings.HasSuffix(n, ".yml") || strings.HasSuffix(n, ".yaml")) && n != ".pre-commit-config.yaml" {
			ymls = append(ymls, n)
		}
	}
	if len(ymls) == 1 {
		if !untrusted.IsPlain(ymls[0]) || strings.Contains(ymls[0], `\`) {
			return "", &noPythonCatalogError{"the only YAML file at the source's root has a name lola does not read"}
		}
		fi, err := realPath(tree, ymls[0])
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if err != nil || !fi.Mode().IsRegular() {
			return "", fmt.Errorf("%s is not a regular file", untrusted.Text(ymls[0], untrusted.MaxName))
		}
		return ymls[0], nil
	}
	which := fmt.Sprintf("%d YAML files at its root, %s", len(ymls), andJoin(shownAll(ymls)))
	if len(ymls) == 0 {
		which = "no YAML file at its root"
	}
	return "", &noPythonCatalogError{fmt.Sprintf("the source has %s, and none the Python lola would pick: %s or %s", which,
		strings.Join(shownAll(named[:3]), ", "), shown(named[3]))}
}

// noPythonCatalogError is pythonCatalog's refusal; it wraps ErrNoPythonCatalog.
type noPythonCatalogError struct{ msg string }

func (e *noPythonCatalogError) Error() string { return e.msg }
func (e *noPythonCatalogError) Unwrap() error { return ErrNoPythonCatalog }

// PythonCatalog fetches the tree of src, a marketplace source lola migrate translated from the
// Python lola's url, into a run directory of its own, and returns the catalog file the Python lola
// read there for the marketplace it named name (pythonCatalog). It writes nothing but the run
// directory, which it removes (Plan 6c §6.9).
func (e *Engine) PythonCatalog(ctx context.Context, src, name string) (_ string, err error) {
	ms, err := e.marketSource("", src, "", "", false, nil)
	if err != nil {
		return "", err
	}
	if ms.file {
		return "", fmt.Errorf("%s is a catalog file, not a tree", shown(src))
	}
	run, err := e.runDir()
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(run)) }()
	f, err := source.Fetch(ctx, ms.spec, run)
	if err != nil {
		return "", unhinted(err)
	}
	return pythonCatalog(f.Dir, name)
}

// realPath is the file at rel, a clean slash path under root ("" for root itself), refusing a
// symbolic link anywhere on the way or at the end (Plan 6c §6.2 step 3, §10).
func realPath(root, rel string) (fs.FileInfo, error) {
	cur := root
	fi, err := os.Lstat(cur)
	if rel == "" || err != nil {
		return fi, err
	}
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		if fi, err = os.Lstat(cur); err != nil {
			return nil, err
		}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			return nil, fmt.Errorf("%s is a symbolic link; lola never follows links in a marketplace", untrusted.Text(strings.Join(parts[:i+1], "/"), untrusted.MaxName))
		case i < len(parts)-1 && !fi.IsDir():
			return nil, fs.ErrNotExist
		}
	}
	return fi, nil
}

// fetchMarket fetches the marketplace at src into run and reads its catalog (Plan 6c §6.2 steps
// 2–4): a catalog file is downloaded, at most market.MaxCatalog bytes; a tree is fetched whole
// (never through the cache: a marketplace is not a module), so its relative entries can be
// checked against it, and the fetch refuses links as every fetch does. The catalog is read as a
// stranger's input (market.Parse); each relative entry's directory must be a real directory in
// the tree, else the entry is skipped. name is the marketplace's name ("" before the catalog
// names it), p the --path, python the Python lola's name of it (lola migrate's; "" otherwise),
// which without p finds the catalog as the Python lola did (pythonCatalog), and add builds the
// --add hint of a two-catalog refusal.
func (e *Engine) fetchMarket(ctx context.Context, src marketSource, name, p, python, run string, add func(string) string) (*fetchedMarket, error) {
	pin := market.Pin{Version: market.PinVersion, Source: src.loc, Ref: src.spec.Ref, Path: p, Absent: []string{}, FetchedAt: e.Now().UTC()}
	var tree, file string
	var data []byte
	if src.file {
		u, err := url.Parse(src.loc)
		if err != nil {
			return nil, err
		}
		// The URL's last element, decoded, is what source.json records: one its load would
		// refuse is refused here, before any network contact.
		file = path.Base(u.Path)
		if clean, err := market.CheckRelative(file); err != nil || clean != file || !untrusted.IsPlain(file) || market.FormatOf(file) == "" {
			return nil, fmt.Errorf("%s cannot be recorded as a catalog file name", untrusted.Text(file, untrusted.MaxName))
		}
		dl, err := source.FetchFile(ctx, src.loc, run, market.MaxCatalog)
		if err != nil {
			return nil, unhinted(err)
		}
		if data, err = fsx.ReadOwn(dl, market.MaxCatalog); err != nil {
			return nil, err
		}
	} else {
		f, err := source.Fetch(ctx, src.spec, run)
		if err != nil {
			return nil, unhinted(err)
		}
		tree, pin.Commit, pin.Root = f.Dir, f.Commit, f.Root
		if python != "" && p == "" {
			file, err = pythonCatalog(tree, python)
		} else {
			file, err = catalogIn(tree, name, p, add)
		}
		if err != nil {
			return nil, err
		}
		if data, err = fsx.ReadOwn(filepath.Join(tree, filepath.FromSlash(file)), market.MaxCatalog); err != nil {
			return nil, fmt.Errorf("%s: %w", untrusted.Text(file, untrusted.MaxName), untrusted.CleanErr(err))
		}
	}
	pin.File, pin.Digest = file, profile.Digest(data)
	cat, err := market.Parse(market.FormatOf(file), data)
	if err != nil {
		return nil, fmt.Errorf("the catalog %s %w", untrusted.Text(file, untrusted.MaxName), err)
	}
	if tree != "" {
		for _, dir := range market.RelativeDirs(cat, file) {
			rel := dir
			if rel == "." {
				rel = ""
			}
			if fi, err := realPath(tree, rel); err != nil || !fi.IsDir() {
				pin.Absent = append(pin.Absent, dir)
			}
		}
		// What a content list selects must be there as its kind needs: a skill a directory, a
		// command or an agent a file or a directory.
		selected := market.Selected(cat, file)
		for _, p := range slices.Sorted(maps.Keys(selected)) {
			fi, err := realPath(tree, p)
			if err != nil || !fi.IsDir() && (selected[p] == "skills" || !fi.Mode().IsRegular()) {
				pin.Absent = append(pin.Absent, p)
			}
		}
	}
	return &fetchedMarket{pin: pin, data: data, cat: market.Bind(cat, pin)}, nil
}

// writeMarket writes the cache of the marketplace name, its catalog and source.json, replacing any
// (writeDir). The caller holds the markets lock exclusively.
func (e *Engine) writeMarket(name string, f *fetchedMarket) error {
	pin, err := json.MarshalIndent(f.pin, "", "  ")
	if err != nil {
		return err
	}
	_, err = writeDir(e.Paths.MarketCache, name, []dirFile{{catalogName(market.FormatOf(f.pin.File)), f.data}, {marketPinFile, append(pin, '\n')}})
	return err
}

// skipSummary is the notice of a fetch's skipped entries, grouped by kind ("3 entries skipped: 2
// npm sources, 1 command source"), with the command that lists them when there is one to run.
func skipSummary(name string, cat market.Catalog, listed bool, display []string) *Notice {
	if len(cat.Skipped) == 0 {
		return nil
	}
	var kinds []string
	count := map[string]int{}
	for _, s := range cat.Skipped {
		if count[s.Kind] == 0 {
			kinds = append(kinds, s.Kind)
		}
		count[s.Kind]++
	}
	slices.SortStableFunc(kinds, func(a, b string) int { return count[b] - count[a] })
	var parts []string
	for _, k := range kinds {
		parts = append(parts, market.CountKind(count[k], k))
	}
	what := "1 entry skipped"
	if len(cat.Skipped) > 1 {
		what = fmt.Sprintf("%d entries skipped", len(cat.Skipped))
	}
	text := what + ": " + strings.Join(parts, ", ")
	if listed {
		text += " → " + MarketRun(append([]string{name}, display...)...)
	}
	return &Notice{Level: LevelWarn, Text: text}
}

// sameSkips reports whether two catalogs skip the same entries for the same reasons, in any order.
func sameSkips(a, b []market.Skip) bool {
	key := func(ss []market.Skip) []string {
		out := make([]string, 0, len(ss))
		for _, s := range ss {
			out = append(out, s.Name+"\x00"+s.Reason)
		}
		slices.Sort(out)
		return out
	}
	return slices.Equal(key(a), key(b))
}

// sameSource reports whether r has the source src at ref.
func sameSource(r market.Registration, src marketSource, ref string) bool {
	kind := src.spec.Kind
	if src.file {
		kind = source.KindGit // compared as text, as a URL is
	}
	return source.SameLocation(kind, r.Source, src.loc) && r.Ref == ref
}

// sameMarket reports whether r is the marketplace src at ref, under the --path p as typed ("" for
// none), looked up by name (Plan 6c §3.1: same source, ref and path): its catalog file is the one p
// names, or one the lookup in p (the root for none) could read, market.CatalogFiles(name).
func sameMarket(r market.Registration, src marketSource, ref, p, name string) bool {
	if !sameSource(r, src, ref) {
		return false
	}
	dir := path.Clean(p)
	if r.Path == dir || r.Path == p {
		return true
	}
	if dir == "." {
		dir = ""
	}
	return slices.ContainsFunc(market.CatalogFiles(name), func(f string) bool { return r.Path == path.Join(dir, f) })
}

// takenName refuses to add a marketplace as name, which r already is (Plan 6c §3.1, ruling
// R-6c-11): the same marketplace is coached to --update it; another is asked a free name on a
// terminal (rename), and off one refused with the --add of the first free name, never an --update
// of a marketplace whose source differs from the one typed. It returns the name to add as.
func takenName(reg market.Registry, r market.Registration, src marketSource, req MarketAddRequest) (string, error) {
	if sameMarket(r, src, req.Ref, req.Path, r.Name) {
		return "", alreadyThis(r.Name, req.DryRun, req.Display)
	}
	taken := func(n string) bool { return reg.Find(n) != nil }
	free := market.FreeName(r.Name, 2, taken)
	from := marketWhere(r.Source, r.Ref, "")
	if sameSource(r, src, req.Ref) {
		from = marketWhere(r.Source, r.Ref, r.Path) // the catalog file is all that differs
	}
	if req.Rename == nil || req.DryRun {
		err := fmt.Errorf("%s is already a marketplace, from %s", r.Name, from)
		if free == "" {
			return "", err
		}
		return "", errs.WithHints(err, MarketRun(addRun(free, req.Source, req.Ref, req.Path, req.DryRun, req.Display)...))
	}
	return askName(reg, MarketRename{Why: r.Name + " is already a marketplace, from " + from, Suggest: free}, req.Rename)
}

// askName asks rename for the name of a marketplace being added, from step, until the answer is
// a name no marketplace of reg has (Plan 6c §3.1): a refused answer is asked again, saying why.
func askName(reg market.Registry, step MarketRename, rename func(MarketRename) (string, error)) (string, error) {
	for {
		answer, err := rename(step)
		if err != nil {
			return "", err
		}
		answer = strings.TrimSpace(answer)
		err = market.ValidateName(answer)
		r := reg.Find(answer)
		switch {
		case err != nil:
			step.Problem = untrusted.Text(err.Error(), 2*untrusted.MaxName)
		case r != nil:
			step.Problem = answer + " is already a marketplace, from " + marketWhere(r.Source, r.Ref, "")
		default:
			return answer, nil
		}
	}
}

// alreadyThis refuses to add the marketplace name again, coaching its --update (Plan 6c §3.1),
// which keeps -n and display, the run's output flags.
func alreadyThis(name string, dry bool, display []string) error {
	args := []string{name, "--update"}
	if dry {
		args = append(args, "-n")
	}
	return errs.WithHints(fmt.Errorf("%s is already this marketplace", name), MarketRun(append(args, display...)...))
}

// AddMarket fetches a catalog and registers it (Plan 6c §3.1, §6.2): it asks nothing but a name on
// a collision (D4: a catalog installs and runs nothing, and you typed its source). The name, every
// source rule and a typed name that is already this marketplace are checked before any network
// contact; the source is fetched into the run's staging, its catalog found (by the name you typed)
// and read as a stranger's input; a name another marketplace has is then asked again or refused
// (takenName), so its hint can name the catalog file found. Then, under the exclusive markets
// lock, with the name checked again, the cache is written, then the registration, so a failed
// registration leaves a cache directory that listings ignore and the next --add replaces.
func (e *Engine) AddMarket(ctx context.Context, req MarketAddRequest) (_ *MarketResult, err error) {
	if req.Name != "" {
		if err := market.ValidateName(req.Name); err != nil {
			// Its lowercase form, when that is a name no marketplace has, is the run's likeliest
			// intent. markets.yaml is read without the lock: it only picks the hint.
			lower := strings.ToLower(req.Name)
			if lower == req.Name || market.ValidateName(lower) != nil {
				return nil, err
			}
			if reg, _, rerr := e.registry(); rerr != nil || reg.Find(lower) != nil {
				return nil, err
			}
			return nil, errs.WithHints(err, MarketRun(addRun(lower, req.Source, req.Ref, req.Path, req.DryRun, req.Display)...))
		}
	}
	if req.Path != "" {
		if _, err := market.CheckRelative(req.Path); err != nil {
			return nil, fmt.Errorf("--path %w", err)
		}
	}
	src, err := e.marketSource(req.Name, req.Source, req.Ref, req.Path, req.DryRun, req.Display)
	if err != nil {
		return nil, err
	}
	unlock, err := e.readMarkets(req.DryRun)
	if err != nil {
		return nil, err
	}
	defer func() { unlock() }()
	reg, _, err := e.registry()
	if err != nil {
		return nil, err
	}
	if r := reg.Find(req.Name); req.Name != "" && r != nil && sameMarket(*r, src, req.Ref, req.Path, req.Name) {
		return nil, alreadyThis(r.Name, req.DryRun, req.Display)
	}
	run, err := e.runDir()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(run)) }()
	// The catalog is looked up by the name you typed (<name>.yml), whatever name it is added as.
	two := func(file string) string {
		return MarketRun(addRun(req.Name, req.Source, req.Ref, file, req.DryRun, req.Display)...)
	}
	f, err := e.fetchMarket(ctx, src, req.Name, req.Path, req.PythonName, run, two)
	if err != nil {
		return nil, err
	}
	res := &MarketResult{Markets: []MarketRow{}, Changes: []MarketChange{}, Updates: []MarketOutcome{}, Notices: append([]Notice{}, e.sweepRuns()...), DryRun: req.DryRun}
	// A hint that adds the marketplace under another name names the catalog file found: another
	// name would look for its own <name>.yml too.
	hintPath := req.Path
	if !src.file && path.Clean(req.Path) != f.pin.File {
		hintPath = f.pin.File
	}
	name := req.Name
	if name == "" {
		if market.ValidateName(f.cat.Name) != nil {
			why := "the catalog names itself " + shown(f.cat.Name) + ", which cannot name a marketplace"
			slug := market.Slug(f.cat.Name)
			if r := reg.Find(slug); r != nil && sameMarket(*r, src, req.Ref, hintPath, slug) {
				return nil, alreadyThis(slug, req.DryRun, req.Display)
			}
			free := market.FreeName(slug, 1, func(n string) bool { return reg.Find(n) != nil })
			switch {
			case req.Rename != nil && !req.DryRun:
				// On a terminal, as for a taken name (R-6c-11): pre-filled with the free slug.
				if name, err = askName(reg, MarketRename{Why: why, Suggest: free}, req.Rename); err != nil {
					return nil, err
				}
			case free != "":
				return nil, errs.WithHints(errors.New(why+"; give a name before --add"),
					MarketRun(addRun(free, req.Source, req.Ref, hintPath, req.DryRun, req.Display)...))
			default:
				return nil, errors.New(why + "; give a name before --add")
			}
		} else {
			name = f.cat.Name
		}
	}
	if r := reg.Find(name); r != nil {
		// Another name would look for its own <name>.yml: the hint names the file found instead.
		again := req
		again.Path = hintPath
		if name, err = takenName(reg, *r, src, again); err != nil {
			return nil, err
		}
	}
	if !src.file {
		f.pin.Path = f.pin.File // the registration records the file found, so --update reads that one
	}
	if f.cat.Name != name {
		res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: "the catalog calls itself " + shown(f.cat.Name) + "; it is your marketplace " + name})
	}
	row := e.marketRow(market.Registration{Name: name, Source: f.pin.Source, Ref: f.pin.Ref, Path: f.pin.Path}, &cachedMarket{pin: f.pin, cat: f.cat})
	res.Markets = append(res.Markets, row)
	outcome := MarketOutcome{Market: name, Outcome: MarketWouldAdd, Hints: []string{}}
	if !req.DryRun {
		unlock()
		if unlock, err = lockAt(e.Paths.MarketsLock, true); err != nil {
			unlock = func() {}
			return nil, err
		}
		if err := sweepDir(e.Paths.MarketCache, market.ValidateName); err != nil {
			return nil, unhinted(err)
		}
		reg, b, err := e.registry()
		if err != nil {
			return nil, err
		}
		if r := reg.Find(name); r != nil {
			return nil, errs.WithHints(fmt.Errorf("%s was added by another lola while this one fetched it, from %s", name,
				marketWhere(r.Source, r.Ref, "")), MarketRun(append([]string{name}, req.Display...)...))
		}
		if err := e.writeMarket(name, f); err != nil {
			return nil, err
		}
		nb, err := market.AddRegistration(b, market.Registration{Name: name, Source: f.pin.Source, Ref: f.pin.Ref, Path: f.pin.Path})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", shown(e.Paths.Markets), err)
		}
		if err := writeRegistry(e.Paths.Markets, nb); err != nil {
			return nil, err
		}
		outcome.Outcome = MarketAdded
	}
	res.Updates = append(res.Updates, outcome)
	if n := skipSummary(name, f.cat, !req.DryRun, req.Display); n != nil {
		res.Notices = append(res.Notices, *n)
	}
	if !req.DryRun && len(f.cat.Entries) > 0 {
		res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: "browse: " + errs.ShellJoin(append([]string{"lola", "install", "@" + name}, req.Display...))})
	}
	return res, nil
}

// writeRegistry writes markets.yaml, b, atomically, following a symbolic link to the file it
// names, as config.yaml's writer does: the file is yours, and may live in your dotfiles.
func writeRegistry(file string, b []byte) error {
	dst := file
	if fi, err := os.Lstat(file); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		if dst, err = filepath.EvalSymlinks(file); err != nil {
			return fmt.Errorf("%s is a symbolic link lola cannot follow: %w", shown(file), untrusted.CleanErr(err))
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("could not write %s: %w", shown(dst), untrusted.CleanErr(err))
	}
	if err := fsx.WriteFileAtomic(dst, b, 0o600); err != nil {
		return fmt.Errorf("could not write %s: %w", shown(dst), untrusted.CleanErr(err))
	}
	return nil
}

// marketChanges is how the entries of the marketplace name changed from was (nil for none) to
// now, by entry name (Plan 6c §6.2): added, removed, and changed with the fields that differ.
func marketChanges(name string, was *market.Catalog, now market.Catalog) []MarketChange {
	old := map[string]market.Entry{}
	if was != nil {
		for _, en := range was.Entries {
			old[en.Name] = en
		}
	}
	out := []MarketChange{}
	seen := map[string]bool{}
	for _, en := range now.Entries {
		seen[en.Name] = true
		o, ok := old[en.Name]
		if !ok {
			out = append(out, MarketChange{Market: name, Entry: shown(en.Name), Change: "added", Fields: []string{}})
			continue
		}
		fields := []string{}
		for _, f := range [][3]string{{"source", o.Source.Source, en.Source.Source}, {"path", o.Source.Path, en.Source.Path},
			{"ref", o.Source.Ref, en.Source.Ref}, {"commit", o.Source.Commit, en.Source.Commit}, {"version", o.Version, en.Version}} {
			if f[1] != f[2] {
				fields = append(fields, f[0])
			}
		}
		if !reflect.DeepEqual(o.Components, en.Components) {
			fields = append(fields, "components")
		}
		if len(fields) > 0 {
			out = append(out, MarketChange{Market: name, Entry: shown(en.Name), Change: "changed", Fields: fields})
		}
	}
	if was != nil {
		for _, en := range was.Entries {
			if !seen[en.Name] {
				out = append(out, MarketChange{Market: name, Entry: shown(en.Name), Change: "removed", Fields: []string{}})
			}
		}
	}
	return out
}

// UpdateMarkets fetches the named marketplaces again, or every one with All, each at its
// registered source, ref and path, replaces its cached catalog and reports how its entries
// changed (Plan 6c §3.1, §6.2). It asks nothing. Names are checked before any network contact. A
// failure costs only that marketplace, whose cached catalog is kept (§9: a catalog that no longer
// parses keeps the old one); Ctrl+C stops the run, keeping what it wrote. The skipped entries'
// summary is a notice only when they differ from the cached catalog's, which --add or an earlier
// --update reported.
func (e *Engine) UpdateMarkets(ctx context.Context, req MarketUpdateRequest) (*MarketResult, error) {
	unlock, err := e.readMarkets(req.DryRun)
	if err != nil {
		return nil, err
	}
	defer func() { unlock() }()
	reg, _, err := e.registry()
	if err != nil {
		return nil, err
	}
	res := &MarketResult{Markets: []MarketRow{}, Changes: []MarketChange{}, Updates: []MarketOutcome{}, Notices: []Notice{}, DryRun: req.DryRun}
	var regs []market.Registration
	if req.All {
		regs = reg.Markets
	}
	for _, n := range req.Names {
		r := reg.Find(n)
		if r == nil {
			// The run with the close name in n's place, each name once.
			run := func(c string) []string {
				var out []string
				for _, m := range req.Names {
					if m == n {
						m = c
					}
					if !slices.Contains(out, m) {
						out = append(out, m)
					}
				}
				out = append(out, "--update")
				if req.DryRun {
					out = append(out, "-n")
				}
				return out
			}
			return nil, noMarket(reg, n, run, req.Display)
		}
		if !slices.ContainsFunc(regs, func(o market.Registration) bool { return o.Name == n }) {
			regs = append(regs, *r)
		}
	}
	if len(regs) == 0 {
		res.Notices = append(res.Notices, Notice{Level: LevelInfo, Text: "you have no marketplaces to update"})
		return res, nil
	}
	res.Notices = append(res.Notices, e.sweepRuns()...)
	stopped := false
	for _, r := range regs {
		out := MarketOutcome{Market: r.Name, Hints: []string{}}
		if stopped || ctx.Err() != nil {
			stopped, out.Outcome = true, MarketStopped
			res.Updates = append(res.Updates, out)
			continue
		}
		f, was, err := e.updateMarket(ctx, r, req.DryRun, &unlock)
		switch {
		case err != nil && ctx.Err() != nil:
			stopped, out.Outcome = true, MarketStopped
		case err != nil:
			out.Outcome, out.Err = MarketFailed, err
			out.Error, out.Hints = untrusted.CleanErr(err).Error(), append([]string{}, errs.Hints(err)...)
		default:
			res.Markets = append(res.Markets, e.marketRow(r, &cachedMarket{pin: f.pin, cat: f.cat}))
			var old *market.Catalog
			if was != nil {
				old = &was.cat
			}
			changes := marketChanges(r.Name, old, f.cat)
			res.Changes = append(res.Changes, changes...)
			switch {
			case req.DryRun:
				out.Outcome = MarketWouldUpdate
			case was != nil && was.pin.Digest == f.pin.Digest && was.pin.Commit == f.pin.Commit && len(changes) == 0:
				out.Outcome = MarketUpToDate
			default:
				out.Outcome = MarketUpdated
			}
			// The skipped entries were reported when they were added or last changed: an update
			// that leaves them as they were says nothing of them again.
			if n := skipSummary(r.Name, f.cat, !req.DryRun, req.Display); n != nil && (was == nil || !sameSkips(was.cat.Skipped, f.cat.Skipped)) {
				res.Notices = append(res.Notices, *n)
			}
		}
		res.Updates = append(res.Updates, out)
	}
	return res, nil
}

// updateMarket fetches r again and, unless dry, replaces its cache under the exclusive markets
// lock, taken only to check r is still registered as it was and write; unlock is the run's
// shared lock, which it gives back and takes again. It returns the fetch and the cached catalog it
// replaces (nil for none, or one that cannot be read).
func (e *Engine) updateMarket(ctx context.Context, r market.Registration, dry bool, unlock *func()) (_ *fetchedMarket, _ *cachedMarket, err error) {
	was, _ := e.loadMarket(r) // an unreadable cache is replaced, and every entry reads as added
	run, err := e.runDir()
	if err != nil {
		return nil, nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(run)) }()
	f, err := e.fetchMarket(ctx, registrySource(r), r.Name, r.Path, "", run, nil)
	if err != nil {
		return nil, nil, err
	}
	if dry {
		return f, was, nil
	}
	(*unlock)()
	write, err := lockAt(e.Paths.MarketsLock, true)
	if err != nil {
		*unlock = func() {}
		return nil, nil, err
	}
	err = func() error {
		defer write()
		if err := sweepDir(e.Paths.MarketCache, market.ValidateName); err != nil {
			return unhinted(err)
		}
		reg, _, err := e.registry()
		if err != nil {
			return err
		}
		if now := reg.Find(r.Name); now == nil || now.Source != r.Source || now.Ref != r.Ref || now.Path != r.Path {
			return fmt.Errorf("%s changed in markets.yaml while lola fetched it", r.Name)
		}
		return e.writeMarket(r.Name, f)
	}()
	if again, lerr := e.readMarkets(false); lerr != nil {
		*unlock = func() {}
		err = errors.Join(err, lerr)
	} else {
		*unlock = again
	}
	return f, was, err
}
