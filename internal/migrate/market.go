package migrate

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/engine"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/market"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Marketplace statuses.
const (
	MarketRegister   = "register"           // planned
	MarketRegistered = "registered"         // done
	MarketAlready    = "already-registered" // you have it, from the same source
	MarketSkipped    = "not-registered"     // lola cannot, or must not, register it
	MarketFailed     = "failed"             // its --add failed
)

// Market is one of the Python lola's marketplaces as lola migrate registers it (Plan 6c §6.9):
// Name and URL as the Python lola recorded them; Market the name lola registers it as; Source and
// Path its url translated for lola market --add; Status what happens to it; Reason why it is not
// registered, and Hints the lola market --add that registers it, when one can. Every string has
// been through untrusted.Text.
type Market struct {
	Name   string   `json:"name"`
	URL    string   `json:"url"`
	Market string   `json:"market,omitempty"`
	Source string   `json:"source,omitempty"`
	Path   string   `json:"path,omitempty"`
	Status string   `json:"status"`
	Reason string   `json:"reason,omitempty"`
	Hints  []string `json:"hints"`

	src, path string // Source and Path as lola market --add takes them
	hintName  string // the name the hint adds it as, "" for no hint
	pyName    string // the Python lola's name, by which it found the catalog in a tree
	disabled  bool   // disabled in the Python lola
}

// lookup reports whether m's catalog file is the one the Python lola found in a tree, which
// only a fetch can tell: a tree source whose url named no file (Plan 6c §6.9).
func (m Market) lookup() bool { return m.path == "" && !market.IsCatalogURL(m.src) }

// planMarkets decides what lola migrate does with each of pys, the Python lola's marketplaces,
// given have, yours (Plan 6c §6.9). Nothing is fetched. A url lola cannot add (translate) is not
// registered, with its reason and no hint, since no form of it works. A name that is no
// marketplace name, or that one of yours has from another source, is not registered, with the
// --add of a free slug as the hint, as is one whose name an earlier one is registered or hinted
// as; one of yours from the same source (and path, when the url names one) is already
// registered. A disabled one is not registered, with its --add as the
// hint. Every other one is registered. A name one of them is registered or hinted as is taken
// for the others.
func planMarkets(pys []PyMarket, have []engine.MarketRow) []Market {
	// Every name the plan may register is claimed first, so no slug a hint gives takes it.
	planned := map[string]bool{}
	for _, py := range pys {
		planned[py.Name] = market.ValidateName(py.Name) == nil
	}
	taken := func(n string) bool {
		return planned[n] || slices.ContainsFunc(have, func(r engine.MarketRow) bool { return r.Name == n })
	}
	assigned := map[string]bool{} // the names an earlier one is registered, found registered, or hinted as
	out := []Market{}
	for _, py := range pys {
		m := Market{Name: untrusted.Text(py.Name, untrusted.MaxName), URL: shownURL(py.URL), Hints: []string{},
			pyName: py.Name, disabled: !py.Enabled}
		var why string
		m.src, m.path, why = translate(py.URL)
		m.Source, m.Path = untrusted.Text(m.src, untrusted.MaxName), untrusted.Text(m.path, untrusted.MaxName)
		if why != "" {
			m.Status, m.Reason = MarketSkipped, why
			out = append(out, m)
			continue
		}
		var reasons []string
		name := py.Name
		if err := market.ValidateName(name); err != nil {
			reasons = append(reasons, untrusted.CleanErr(err).Error())
			name = market.FreeName(market.Slug(name), 1, taken)
		} else if assigned[name] {
			reasons = append(reasons, "another of the Python lola's marketplaces has the name "+name)
			name = market.FreeName(name, 2, taken)
		} else if k := slices.IndexFunc(have, func(r engine.MarketRow) bool { return r.Name == name }); k >= 0 {
			// Without a path, --add records the catalog file it finds: any path is that one.
			if have[k].Source == m.src && (m.path == "" || have[k].Path == m.path) {
				m.Market, m.Status = name, MarketAlready
				assigned[name] = true
				out = append(out, m)
				continue
			}
			reasons = append(reasons, "you already have a marketplace "+name+", from "+have[k].Source)
			name = market.FreeName(name, 2, taken)
		}
		if !py.Enabled {
			reasons = append(reasons, "it is disabled in the Python lola")
		}
		if len(reasons) > 0 {
			m.Status, m.Reason, m.hintName = MarketSkipped, strings.Join(reasons, "; "), name
		} else {
			m.Market, m.Status = name, MarketRegister
		}
		planned[name] = name != ""
		assigned[name] = name != ""
		out = append(out, m)
	}
	return out
}

// shownURL is u, a Python marketplace url, as it may be shown (source.DisplayURL): never its user
// info, query or fragment, which can carry secrets; git+ is kept.
func shownURL(u string) string {
	rest, gitPlus := strings.CutPrefix(u, "git+")
	if gitPlus {
		return "git+" + source.DisplayURL(rest)
	}
	return source.DisplayURL(rest)
}

// translate is u, a Python marketplace url, as lola market --add takes it (Plan 6c §6.9, [P]
// models.py Marketplace.from_url): git+ is dropped; a git URL (git+, or http(s) ending in .git)
// gives its #fragment as the path, as the Python lola reads the catalog file there; git's scp
// form is git; another https:// URL is a catalog file, which lola reads only when its path ends
// in .json, .yml or .yaml; a file:// URL or absolute path is a folder, made absolute with its
// links resolved, and a file there (the Python lola records its catalog file's URI) is the
// folder's --path. why says why lola cannot add it, with no form that would work ("" when it
// can): plain http:// off loopback, a relative path, a path that is not there, another scheme.
func translate(u string) (src, path, why string) {
	rest, gitPlus := strings.CutPrefix(u, "git+")
	if source.IsSCP(rest) { // git's scp form, which takes no fragment
		// git would read the text before the first colon as the host, and pass the rest to ssh.
		if _, after, _ := strings.Cut(rest, ":"); strings.Contains(after, "@") {
			return "", "", "it reads as user:password@host, and a git source must not carry credentials"
		}
		if err := source.Validate(source.Spec{Kind: source.KindGit, Location: rest}); err != nil {
			return "", "", untrusted.CleanErr(err).Error()
		}
		return rest, "", ""
	}
	p, err := url.Parse(rest)
	if err != nil {
		return "", "", "it is not a URL lola can read"
	}
	switch {
	case p.Scheme == "http" && !source.IsLoopback(p.Hostname()):
		return "", "", "it uses plain http://, which lola allows only for a loopback host; the marketplace has no https:// URL lola knows of"
	case gitPlus && p.Scheme != "" || (p.Scheme == "https" || p.Scheme == "http") && strings.HasSuffix(strings.TrimRight(p.Path, "/"), ".git"):
		path = p.Fragment
		p.Fragment, p.RawFragment = "", ""
		// The rule --add applies: no credentials or query, which lola would record and show.
		if err := source.Validate(source.Spec{Kind: source.KindGit, Location: p.String()}); err != nil {
			return "", "", untrusted.CleanErr(err).Error()
		}
		return p.String(), path, ""
	case p.Scheme == "https" || p.Scheme == "http":
		if err := source.CheckURL(rest); err != nil {
			return "", "", untrusted.CleanErr(err).Error()
		}
		if !market.IsCatalogURL(rest) {
			return "", "", "lola reads a marketplace URL that is not a git repository (.git) as a catalog file only when its path ends in .json, .yml or .yaml"
		}
		return rest, "", ""
	case p.Scheme == "file":
		return local(p.Path)
	case p.Scheme == "" && filepath.IsAbs(rest):
		return local(rest)
	case p.Scheme == "":
		return "", "", "it is a relative path, and lola cannot tell what the Python lola resolved it against"
	}
	return "", "", "lola reads no marketplace from a " + untrusted.Text(p.Scheme, untrusted.MaxName) + ":// URL"
}

// local is the folder marketplace at p, an absolute path on this machine: the folder itself, or
// for a file the folder that holds it, with the file as its --path.
func local(p string) (src, path, why string) {
	fi, err := os.Stat(p)
	if err != nil {
		return "", "", "lola cannot read " + untrusted.Text(p, untrusted.MaxName) + ": " + untrusted.CleanErr(err).Error()
	}
	if !fi.IsDir() {
		p, path = filepath.Dir(p), filepath.Base(p)
	}
	if src, err = filepath.EvalSymlinks(p); err != nil {
		return "", "", "lola cannot read " + untrusted.Text(p, untrusted.MaxName) + ": " + untrusted.CleanErr(err).Error()
	}
	return src, path, ""
}

// addRun is the lola market --add that registers m as name, keeping display, the run's output
// flags (Plan 6c §11.3).
func (m Market) addRun(name string, display []string) string {
	args := []string{name, "--add", m.src}
	if m.path != "" {
		args = append(args, "--path", m.path)
	}
	return engine.MarketRun(append(args, display...)...)
}

// marketReport is ms as a preview reports them: each skipped one with its hint, keeping display,
// but for one whose catalog file only the run's fetch can tell (lookup), which says so instead.
func marketReport(ms []Market, display []string) []Market {
	out := slices.Clone(ms)
	for i, m := range out {
		out[i].Hints = []string{}
		switch {
		case m.hintName == "":
		case m.lookup():
			out[i].Reason += "; lola migrate gives the command that registers it once its run finds the catalog file the Python lola read"
		default:
			out[i].Hints = []string{m.addRun(m.hintName, display)}
		}
	}
	return out
}

// registerMarkets registers each of ms planned to be (Plan 6c §6.9), fetching its catalog
// (engine.AddMarket, which asks nothing), in a tree the file the Python lola read (PythonName). A
// failure is that marketplace's outcome, never the migration's: its reason, its own hints (a git
// ls-remote that shows what the server says), then the --add that registers it once the cause is
// fixed, but none when the tree holds no file the Python lola would read, which no command can
// name. A skipped one whose catalog file only a fetch tells is fetched for its hint, which names
// the file; none when it cannot be told.
func (r *runner) registerMarkets(ms []Market) []Market {
	out := slices.Clone(ms)
	for i, m := range out {
		out[i].Hints = []string{}
		switch {
		case m.Status == MarketSkipped && m.hintName != "" && m.lookup():
			file, err := r.e.PythonCatalog(r.ctx, m.src, m.pyName)
			if err != nil {
				out[i].Reason += "; lola cannot tell which catalog file the Python lola read: " + untrusted.CleanErr(err).Error()
				continue
			}
			m.path = file
			out[i].path, out[i].Path = file, untrusted.Text(file, untrusted.MaxName)
			out[i].Hints = []string{m.addRun(m.hintName, r.opts.Display)}
		case m.Status == MarketSkipped && m.hintName != "":
			out[i].Hints = []string{m.addRun(m.hintName, r.opts.Display)}
		case m.Status == MarketRegister:
			req := engine.MarketAddRequest{Name: m.Market, Source: m.src, Path: m.path, Display: r.opts.Display}
			if m.lookup() {
				req.PythonName = m.pyName
			}
			res, err := r.e.AddMarket(r.ctx, req)
			if err != nil {
				out[i].Status, out[i].Reason = MarketFailed, untrusted.CleanErr(err).Error()
				out[i].Hints = append([]string{}, errs.Hints(err)...)
				switch {
				case errors.Is(err, engine.ErrNoPythonCatalog):
					// No command can name the file.
				case m.lookup():
					// An --add would read lola's lookup, not the file the Python lola read: a rerun
					// of lola migrate, which keeps the home while this one failed, does.
					out[i].Hints = append(out[i].Hints, errs.ShellJoin(append([]string{"lola", "migrate"}, r.opts.Display...)))
				default:
					out[i].Hints = append(out[i].Hints, m.addRun(m.Market, r.opts.Display))
				}
				continue
			}
			out[i].Status = MarketRegistered
			if len(res.Markets) > 0 {
				out[i].Path = untrusted.Text(res.Markets[0].Path, untrusted.MaxName) // the catalog file it found
			}
		}
	}
	return out
}
