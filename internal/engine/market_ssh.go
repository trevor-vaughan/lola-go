package engine

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// What became of the marketplace of a --ssh-host --remove run (MarketOutcome.Outcome).
const (
	MarketRevoked     = "revoked"      // the hosts are no longer allowed; its cached catalog is read without them
	MarketWouldRevoke = "would_revoke" // a dry run: the hosts would no longer be allowed
)

// MarketSSHHost is one ssh host a fetched catalog names that you have not allowed, with the
// entries skipped for it.
type MarketSSHHost struct {
	Host    string
	Entries int
}

// MarketSSHStep asks which of a marketplace's ssh hosts to allow.
type MarketSSHStep struct {
	Market string
	Hosts  []MarketSSHHost // by host name
}

// MarketSSHRevokeRequest is lola market <name> --ssh-host <host>... --remove.
type MarketSSHRevokeRequest struct {
	Name   string
	Hosts  []string
	DryRun bool
	// Display is the run's output flags, which every command a hint names keeps.
	Display []string
}

// checkedHosts is flag, the --ssh-host values as typed, each a host name lola can allow
// (market.CheckSSHHost), sorted and without duplicates.
func checkedHosts(flag []string) ([]string, error) {
	out := make([]string, 0, len(flag))
	for _, h := range flag {
		name, err := market.CheckSSHHost(h)
		if err != nil {
			return nil, fmt.Errorf("--ssh-host %w", err)
		}
		out = append(out, name)
	}
	return market.NormalizeHosts(out), nil
}

// pendingSSH are the ssh hosts cat skips entries for, with the entries skipped for each, by host.
func pendingSSH(cat market.Catalog) []MarketSSHHost {
	count := map[string]int{}
	for _, s := range cat.Skipped {
		if s.Host != "" {
			count[s.Host]++
		}
	}
	out := make([]MarketSSHHost, 0, len(count))
	for _, h := range slices.Sorted(maps.Keys(count)) {
		out = append(out, MarketSSHHost{Host: h, Entries: count[h]})
	}
	return out
}

// sshFlags is an --ssh-host flag for each of hosts.
func sshFlags(hosts []string) []string {
	out := make([]string, 0, 2*len(hosts))
	for _, h := range hosts {
		out = append(out, "--ssh-host", h)
	}
	return out
}

// sshHint is the notice that offers run(pending), the lola market arguments that allow cat's
// pending hosts, nil for none.
func sshHint(cat market.Catalog, run func(pending []string) []string) *Notice {
	var pending []string
	for _, h := range pendingSSH(cat) {
		pending = append(pending, h.Host)
	}
	if len(pending) == 0 {
		return nil
	}
	return &Notice{Level: LevelWarn, Text: "to allow ssh hosts → " + MarketRun(run(pending)...)}
}

// allowedNotice says which of final, the hosts a run allows, stored did not: "would allow" in a
// dry run. nil when none is new.
func allowedNotice(stored, final []string, dry bool) *Notice {
	var added []string
	for _, h := range final {
		if !slices.Contains(stored, h) {
			added = append(added, shown(h))
		}
	}
	if len(added) == 0 {
		return nil
	}
	verb := "allowed"
	if dry {
		verb = "would allow"
	}
	return &Notice{Level: LevelInfo, Text: verb + " ssh hosts: " + strings.Join(added, ", ")}
}

// reparse reads f's catalog again under hosts: only the entries' ssh gate depends on them, so the
// pin's absent directories stand.
func reparse(f *fetchedMarket, hosts []string) error {
	cat, err := market.Parse(market.FormatOf(f.pin.File), f.data, hosts)
	if err != nil {
		return fmt.Errorf("the catalog %s %w", untrusted.Text(f.pin.File, untrusted.MaxName), err)
	}
	f.cat = market.Bind(cat, f.pin)
	return nil
}

// chooseSSH is the hosts a run allows: base (stored and --ssh-host) plus what pick ticks among
// the catalog's other ssh hosts. pick is nil off a terminal, and not asked in a dry run.
func chooseSSH(name string, f *fetchedMarket, base []string, dry bool, pick func(MarketSSHStep) ([]string, error)) ([]string, error) {
	hosts := market.NormalizeHosts(base)
	pending := pendingSSH(f.cat)
	if pick == nil || dry || len(pending) == 0 {
		return hosts, nil
	}
	ticked, err := pick(MarketSSHStep{Market: name, Hosts: pending})
	if err != nil {
		return nil, err
	}
	if len(ticked) == 0 {
		return hosts, nil
	}
	for _, h := range ticked {
		// A picker bug must never widen what you allow.
		if !slices.ContainsFunc(pending, func(p MarketSSHHost) bool { return p.Host == h }) {
			return nil, errors.New("the host picker returned a host it was not offered")
		}
	}
	hosts = market.NormalizeHosts(append(hosts, ticked...))
	if err := reparse(f, hosts); err != nil {
		return nil, err
	}
	return hosts, nil
}

// RevokeSSHHosts drops hosts from what the marketplace req.Name allows, under
// the exclusive markets lock, and reads its cached catalog again without them, reporting the
// entries that leave it; a dry run reports the same and writes nothing. A cached catalog that is
// missing or cannot be read is left to the next --update: only the registration is written.
// Installed modules keep their recorded sources.
func (e *Engine) RevokeSSHHosts(req MarketSSHRevokeRequest) (*MarketResult, error) {
	if len(req.Hosts) == 0 {
		return nil, errors.New("--remove with --ssh-host needs a host")
	}
	hosts, err := checkedHosts(req.Hosts)
	if err != nil {
		return nil, err
	}
	var unlock func()
	if req.DryRun {
		unlock, err = e.readMarkets(true)
	} else {
		unlock, err = lockAt(e.Paths.MarketsLock, true)
	}
	if err != nil {
		return nil, unhinted(err)
	}
	defer unlock()
	if !req.DryRun {
		if err := sweepDir(e.Paths.MarketCache, market.ValidateName); err != nil {
			return nil, unhinted(err)
		}
	}
	reg, b, err := e.registry()
	if err != nil {
		return nil, err
	}
	run := func(name string, hosts []string) []string {
		out := slices.Concat([]string{name}, sshFlags(hosts), []string{"--remove"})
		if req.DryRun {
			out = append(out, "-n")
		}
		return out
	}
	r := reg.Find(req.Name)
	if r == nil {
		return nil, noMarket(reg, req.Name, func(n string) []string { return run(n, hosts) }, req.Display)
	}
	var missing, kept []string
	for _, h := range hosts {
		if slices.Contains(r.SSHHosts, h) {
			kept = append(kept, h)
		} else {
			missing = append(missing, h)
		}
	}
	if len(missing) > 0 {
		what := "ssh host "
		if len(missing) > 1 {
			what = "ssh hosts "
		}
		msg := "marketplace " + r.Name + " does not allow " + what + strings.Join(shownAll(missing), ", ")
		if len(r.SSHHosts) == 0 {
			return nil, errs.WithHints(errors.New(msg+"; it allows none"), MarketRun(append([]string{r.Name}, req.Display...)...))
		}
		// The run with the hosts it does allow, else one run for each host it allows.
		var hints []string
		if len(kept) > 0 {
			hints = append(hints, MarketRun(append(run(r.Name, kept), req.Display...)...))
		} else {
			for _, h := range r.SSHHosts {
				hints = append(hints, MarketRun(append(run(r.Name, []string{h}), req.Display...)...))
			}
		}
		return nil, errs.WithHints(errors.New(msg+"; it allows: "+strings.Join(shownAll(r.SSHHosts), ", ")), hints...)
	}
	next := *r
	next.SSHHosts = slices.DeleteFunc(slices.Clone(r.SSHHosts), func(h string) bool { return slices.Contains(hosts, h) })
	res := &MarketResult{Markets: []MarketRow{}, Changes: []MarketChange{}, Notices: []Notice{}, DryRun: req.DryRun,
		Updates: []MarketOutcome{{Market: r.Name, Outcome: MarketWouldRevoke, Hints: []string{}}}}
	// An unreadable cache is replaced by the next --update, which reads it with the new hosts.
	if was, _ := e.loadMarket(*r); was != nil {
		if now, _ := e.loadMarket(next); now != nil {
			res.Changes = marketChanges(r.Name, &was.cat, now.cat)
		}
	}
	if req.DryRun {
		return res, nil
	}
	nb, err := market.SetSSHHosts(b, r.Name, next.SSHHosts)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", shown(e.Paths.Markets), err)
	}
	if err := writeRegistry(e.Paths.Markets, nb); err != nil {
		return nil, err
	}
	res.Updates[0].Outcome = MarketRevoked
	return res, nil
}
