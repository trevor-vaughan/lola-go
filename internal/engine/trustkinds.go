package engine

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/trust"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// TrustKinds is a set of the kinds of trust --trust grants (Plan 9 §3.2, R-9-U2), one bit per
// kind, in the canonical order output and hints write them.
type TrustKinds uint8

// The kinds of trust. Entries and profiles are recorded in trust.json; scripts, hooks and runtime
// hooks in consent.json, by digest (R-9-3).
const (
	TrustEntries      TrustKinds = 1 << iota // changed entries and top-level targets of project.yml
	TrustProfiles                            // project profiles, a profile's replacement, a remote profile --add or --update shows
	TrustScripts                             // a project's pre- and post-install scripts (Plan 9 §5)
	TrustHooks                               // module install hooks
	TrustRuntimeHooks                        // module runtime hooks

	// TrustAll is every kind; --trust=all means the kinds of it the command takes.
	TrustAll = TrustEntries | TrustProfiles | TrustScripts | TrustHooks | TrustRuntimeHooks
	// TrustBare is what --trust with no value grants: the kinds that never run code.
	TrustBare = TrustEntries | TrustProfiles
)

// TrustBareValue is the value pflag gives --trust when it is written with no value
// (NoOptDefVal), which ParseTrust takes for bare --trust: TrustBare within the command's kinds. It
// holds a NUL byte, which no command-line argument can, so no typed --trust= value is taken for
// it (S-M1): --trust=entries,profiles is those two kinds, written.
const TrustBareValue = "\x00bare"

// trustKindNames is the name of each kind, in canonical order.
func trustKindNames() []string {
	return []string{"entries", "profiles", "scripts", "hooks", "runtime-hooks"}
}

// List is each kind of k on its own, in canonical order.
func (k TrustKinds) List() []TrustKinds {
	var out []TrustKinds
	for i := range trustKindNames() {
		if bit := TrustKinds(1) << i; k&bit != 0 {
			out = append(out, bit)
		}
	}
	return out
}

// String is k's kinds by name, comma-separated in canonical order: what --trust= takes.
func (k TrustKinds) String() string {
	names := trustKindNames()
	var out []string
	for i := range names {
		if k&(TrustKinds(1)<<i) != 0 {
			out = append(out, names[i])
		}
	}
	return strings.Join(out, ",")
}

// Arg is the --trust argument that grants exactly k, its kinds by name, never all (§3.2): a hint
// names the kinds it grants; "" for none.
func (k TrustKinds) Arg() string {
	if k == 0 {
		return ""
	}
	return "--trust=" + k.String()
}

// trustArg reports whether f, a flag of a run a hint repeats, is --trust in any form.
func trustArg(f string) bool { return f == "--trust" || strings.HasPrefix(f, "--trust=") }

// Covers reports whether k has every kind of o.
func (k TrustKinds) Covers(o TrustKinds) bool { return k&o == o }

// CommandTrust is the kinds a command can ask (Plan 9 §3.2, R-9-1): command is the command after
// lola, "profile --add" for that action of profile. A command that asks none has none.
func CommandTrust(command string) TrustKinds {
	switch command {
	case "sync", "profile":
		return TrustAll
	case "profile --add":
		return TrustProfiles
	case "install", "render", "migrate":
		return TrustScripts | TrustHooks | TrustRuntimeHooks
	}
	return 0
}

// trustKindNamed is the kind arg names, all being the command's kinds (takes); false when arg names
// none.
func trustKindNamed(arg string, takes TrustKinds) (TrustKinds, bool) {
	if arg == "all" {
		return takes, true
	}
	if i := slices.Index(trustKindNames(), arg); i >= 0 {
		return TrustKinds(1) << i, true
	}
	return 0, false
}

// TrustArgument reports whether arg, a positional argument of command, names kinds of trust: one,
// all, or a comma list of them, in any case. Written after a bare --trust it was meant as its
// value, which pflag gives only after = (R-9-2). It returns the kinds arg names.
func TrustArgument(command, arg string) (TrustKinds, bool) {
	var k TrustKinds
	for _, part := range strings.Split(strings.ToLower(arg), ",") {
		named, ok := trustKindNamed(part, CommandTrust(command))
		if !ok {
			return 0, false
		}
		k |= named
	}
	return k, true
}

// TrustFlagError is a --trust value command cannot take (Plan 9 §3.3). Fix is the kinds the run a
// hint offers writes in its --trust, 0 for the run without --trust; HasFix is false when no run
// works, and no hint is given.
type TrustFlagError struct {
	msg    string
	Fix    TrustKinds
	HasFix bool
}

func (e *TrustFlagError) Error() string { return e.msg }

// trustKindList is the kinds by name for a message, all included: "entries, profiles, …, all".
func trustKindList() string { return strings.Join(append(trustKindNames(), "all"), ", ") }

// ParseTrust is the kinds --trust grants command, given values, each occurrence of the flag as
// pflag set it (Plan 9 §3.2, §3.3): a comma list of kinds, all for the command's kinds, or
// TrustBareValue for bare --trust, which is TrustBare within the command's kinds. An unknown or
// empty kind, a kind written that the command never asks, and a bare --trust where the command
// asks neither entries nor profiles are refused, before any network contact or write.
func ParseTrust(command string, values []string) (TrustKinds, error) {
	takes := CommandTrust(command)
	var bare, written TrustKinds
	bareGiven := false
	for i, v := range values {
		if v == TrustBareValue {
			bare, bareGiven = bare|TrustBare&takes, true
			continue
		}
		if v == "" {
			// The run with bare --trust where the command takes entries (§3.3), the other values kept.
			fix := slices.Delete(slices.Clone(values), i, i+1)
			k, err := ParseTrust(command, append(fix, TrustBareValue))
			return 0, &TrustFlagError{msg: "--trust= names no kind; kinds are " + trustKindList(), Fix: k,
				HasFix: err == nil && takes&TrustEntries != 0}
		}
		for _, name := range strings.Split(v, ",") {
			k, ok := trustKindNamed(name, takes)
			if !ok {
				return 0, unknownKind(command, values, i, name)
			}
			written |= k
		}
	}
	refused := written &^ takes
	if bareGiven && bare == 0 {
		refused |= TrustBare // a bare --trust, where the command asks neither kind it means (CR5)
	}
	if refused == 0 {
		return written | bare, nil
	}
	var names []string
	for _, k := range refused.List() {
		names = append(names, k.String())
	}
	or := strings.Join(names, " or ")
	if len(names) > 2 {
		or = strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
	}
	left := (written | bare) &^ refused
	return 0, &TrustFlagError{msg: fmt.Sprintf("lola %s never asks for %s trust; it takes --trust=%s", command, or, takes),
		Fix: left, HasFix: true}
}

// unknownKind refuses name, a kind of values[i] that is none: the hint's run has the closest kind
// the command takes in its place, within edit distance 2 (never all, A6), or, for an empty one (a
// stray comma), none in its place, when that run parses.
func unknownKind(command string, values []string, i int, name string) error {
	err := &TrustFlagError{msg: fmt.Sprintf("--trust: %q is not a kind of trust; kinds are %s", untrusted.Text(name, untrusted.MaxName), trustKindList())}
	best, dist := "", 3
	for _, k := range CommandTrust(command).List() {
		if d := market.Distance(strings.ToLower(name), k.String()); d < dist && name != "" {
			best, dist = k.String(), d
		}
	}
	if best == "" && name != "" {
		return err
	}
	parts := strings.Split(values[i], ",")
	if j := slices.Index(parts, name); best != "" {
		parts[j] = best
	} else {
		parts = slices.Delete(parts, j, j+1)
	}
	fixed := slices.Clone(values)
	fixed[i] = strings.Join(parts, ",")
	if k, perr := ParseTrust(command, fixed); perr == nil {
		err.Fix, err.HasFix = k, true
	}
	return err
}

// Consent changes: what the record holds for an item a run's trust covers.
const (
	ConsentNew     = "new"     // nothing is recorded for it
	ConsentChanged = "changed" // another digest or view is recorded
)

// Consent is one item a run's --trust kinds cover (Plan 9 §3.4), as the receipt and the trusted
// JSON key show it: its kind by name, the item (author text through untrusted.Text) and whether
// it is new or changed.
type Consent struct {
	Kind   string `json:"kind"`
	Item   string `json:"item"`
	Change string `json:"change"`
}

// grant is a consent the run's --trust gives, written to consent.json only after the receipt
// (R-9-3, A2).
type grant struct{ key, digest string }

// cover adds item, of kind, to what the run's --trust covers, with the consent.json grant it
// records once the receipt is answered (none for entries and profiles, whose trust the run records
// with what it writes, and in a dry run).
func (r *Result) cover(kind TrustKinds, item, change string, g *grant) {
	r.Trusted = append(r.Trusted, Consent{Kind: kind.String(), Item: item, Change: change})
	if g != nil && !r.DryRun {
		r.grants = append(r.grants, *g)
	}
}

// confirmTrusted passes what the run's --trust covers to opts.Receipt, once, before anything is
// written (§3.4 step 3), in canonical kind order; a No is ErrDeclined. Only then are the run's
// consent.json grants recorded, the flag's and the terminal's yes answers alike (R-9-3, A2, A23):
// with nothing covered, the yes answers are recorded here, just before the first write. A dry run
// asks nothing and records nothing; its Trusted is sorted too.
func (e *Engine) confirmTrusted(opts Options, res *Result) error {
	slices.SortStableFunc(res.Trusted, func(a, b Consent) int {
		return slices.Index(trustKindNames(), a.Kind) - slices.Index(trustKindNames(), b.Kind)
	})
	if opts.DryRun {
		return nil
	}
	if opts.Batch != nil {
		opts.Batch.add(res.Trusted, res.grants)
		res.grants = nil
		return ErrBatched
	}
	if opts.Expect != nil {
		if !opts.Expect.holds(res.Trusted, res.grants) {
			return ErrTrustChanged
		}
	} else if opts.Receipt != nil && len(res.Trusted) > 0 {
		ok, err := opts.Receipt(slices.Clone(res.Trusted))
		if err != nil {
			return err
		}
		if !ok {
			return ErrDeclined
		}
	}
	for _, g := range res.grants {
		if err := e.Consent.Grant(g.key, g.digest); err != nil {
			return err
		}
	}
	res.grants, res.recorded = nil, recordedItems(res.Trusted)
	return nil
}

// recordedItems is those of items whose grant consent.json records once the receipt is answered:
// every kind but entries and profiles, which trust.json records with what the run writes.
func recordedItems(items []Consent) []Consent {
	out := []Consent{}
	for _, c := range items {
		if c.Kind != TrustEntries.String() && c.Kind != TrustProfiles.String() {
			out = append(out, c)
		}
	}
	return out
}

// TrustedError is a run that failed after its consent.json grants were written (A22, S-I3):
// Trusted lists them, so a --json consumer learns what the failed run recorded.
type TrustedError struct {
	Err     error
	Trusted []Consent
}

func (e *TrustedError) Error() string { return e.Err.Error() }
func (e *TrustedError) Unwrap() error { return e.Err }

// JSONFields is the wrapped error's fields, if any, and trusted.
func (e *TrustedError) JSONFields() map[string]any {
	fields := map[string]any{}
	if f, ok := errors.AsType[interface {
		error
		JSONFields() map[string]any
	}](e.Err); ok {
		maps.Copy(fields, f.JSONFields())
	}
	fields["trusted"] = e.Trusted
	return fields
}

// afterGrants is err, the failure of a run that may have written grants, carrying them
// (TrustedError) when it did.
func (r *Result) afterGrants(err error) error {
	if err == nil || len(r.recorded) == 0 {
		return err
	}
	return &TrustedError{Err: err, Trusted: r.recorded}
}

// ErrBatched is a run stopped at its receipt because its covered items and grants went to a
// TrustBatch: nothing was asked, written or recorded.
var ErrBatched = errors.New("collected for the command's one receipt")

// TrustBatch collects, for a command that runs the engine more than once before it writes (sync
// --scope both, profile --update, migrate), every run's covered items, each once, and grants, so
// the command shows one receipt (R-9-5, A23): a run given one in Options.Batch stops at its receipt
// with ErrBatched. Given in Options.Expect, it is what each run's grants are checked against before
// that run records them (R-9-16).
type TrustBatch struct {
	Items  []Consent
	grants []grant
}

func (b *TrustBatch) add(items []Consent, grants []grant) {
	for _, c := range items {
		if !slices.Contains(b.Items, c) {
			b.Items = append(b.Items, c)
		}
	}
	b.grants = append(b.grants, grants...)
}

// holds reports whether a run's grants are each one the batch holds, by key and digest, and its
// entries and profiles items each one the batch's receipt showed (their grants are what the run
// writes, checked by its own trust record).
func (b *TrustBatch) holds(items []Consent, grants []grant) bool {
	for _, g := range grants {
		if !slices.Contains(b.grants, g) {
			return false
		}
	}
	for _, c := range items {
		if (c.Kind == TrustEntries.String() || c.Kind == TrustProfiles.String()) && !slices.Contains(b.Items, c) {
			return false
		}
	}
	return true
}

// ErrTrustChanged is a run of a batch whose grants are not those its receipt showed: content moved
// between the receipt and the run, which wrote nothing of its own.
var ErrTrustChanged = errors.New("what --trust covers changed after you answered its receipt; nothing more was written")

// consentChange is ConsentChanged when consent.json records some digest for key, else ConsentNew.
func (e *Engine) consentChange(key string) (string, error) {
	ok, err := e.Consent.Recorded(key)
	if err != nil || !ok {
		return ConsentNew, err
	}
	return ConsentChanged, nil
}

// kind is the kind of trust a change of a sync's report needs: profiles for a project profile's
// entry, entries for project.yml's (§3.4 step 1).
func (c TrustChange) kind() TrustKinds {
	if c.Profile != "" {
		return TrustProfiles
	}
	return TrustEntries
}

// kinds is the kinds of trust rep's changes need: entries for project.yml's entries and its
// top-level targets, profiles for a project profile's entries and a replacement.
func (rep TrustReport) kinds() TrustKinds {
	var k TrustKinds
	if rep.Targets != nil {
		k |= TrustEntries
	}
	if len(rep.Replacements) > 0 {
		k |= TrustProfiles
	}
	for _, c := range rep.Changes {
		k |= c.kind()
	}
	return k
}

// split is rep's changes of the kinds k covers, which go to the receipt, and the others, which
// the user is asked about, each a report of its own.
func (rep TrustReport) split(k TrustKinds) (covered, asked TrustReport) {
	covered, asked = rep, rep
	covered.Changes, asked.Changes = []TrustChange{}, []TrustChange{}
	covered.Replacements, asked.Replacements = []Replacement{}, []Replacement{}
	for _, c := range rep.Changes {
		if k.Covers(c.kind()) {
			covered.Changes = append(covered.Changes, c)
		} else {
			asked.Changes = append(asked.Changes, c)
		}
	}
	if k.Covers(TrustEntries) {
		asked.Targets = nil
	} else {
		covered.Targets = nil
	}
	if k.Covers(TrustProfiles) {
		covered.Replacements = rep.Replacements
	} else {
		asked.Replacements = rep.Replacements
	}
	return covered, asked
}

// consents is rep's changes as the receipt shows them: an entry by its module, a project
// profile's as "<module> in <profile>", the top-level targets as "targets" and a replacement as
// "<profile> replacing <profile>"; new when the record holds nothing for it.
func (rep TrustReport) consents() []Consent {
	change := func(added bool) string {
		if added || rep.Status == trust.StatusNew {
			return ConsentNew
		}
		return ConsentChanged
	}
	var out []Consent
	if rep.Targets != nil {
		out = append(out, Consent{Kind: TrustEntries.String(), Item: "targets", Change: change(false)})
	}
	for _, c := range rep.Changes {
		item := c.Module
		if c.Profile != "" {
			item += " in " + c.Profile
		}
		out = append(out, Consent{Kind: c.kind().String(), Item: item, Change: change(c.Kind == trust.ChangeAdded)})
	}
	for _, r := range rep.Replacements {
		out = append(out, Consent{Kind: TrustProfiles.String(), Item: r.Profile + " replacing " + r.Replaces, Change: ConsentChanged})
	}
	return out
}

// neededTrust is the kinds of trust a sync needs for what it knows before anything is fetched: the
// changes of early, the check of the files as they are, the replacements of an activation, and
// the pins and folder content of its entries, each of project.yml (entries) or a project profile
// (profiles).
func neededTrust(early trust.Report, repl *replacing, entries []*syncEntry) TrustKinds {
	var k TrustKinds
	if early.Targets != nil {
		k |= TrustEntries
	}
	for _, c := range early.Changes {
		k |= TrustChange{Change: c}.kind()
	}
	if repl != nil {
		k |= TrustProfiles
	}
	for _, se := range entries {
		switch {
		case !se.folderChanged && !needsPin(se):
		case se.key != "":
			k |= TrustProfiles
		default:
			k |= TrustEntries
		}
	}
	return k
}

// hookNeeds is TrustHooks when an entry whose content was read declares a pre-install hook
// without a recorded consent for its digest, and the run would run it: its version is not the
// one lock.json records as installed (led).
func (e *Engine) hookNeeds(entries []*syncEntry, led *ledger.Ledger) (TrustKinds, error) {
	for _, se := range entries {
		m := se.v.Module
		if se.v.ContentDir == "" || m == nil || !slices.ContainsFunc(m.DeclaredHooks, func(h module.Hook) bool { return h.Event == PreInstall }) {
			continue
		}
		if in := led.Modules[se.ent.Name]; in != nil && in.Digest == se.v.Meta.Digest {
			continue
		}
		_, digest, err := hookScripts(se.v.ContentDir, m.DeclaredHooks)
		if err != nil {
			return 0, err
		}
		ok, err := e.Consent.Granted(se.ent.Name, digest)
		if err != nil {
			return 0, err
		}
		if !ok {
			return TrustHooks, nil
		}
	}
	return 0, nil
}
