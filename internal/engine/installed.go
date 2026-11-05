package engine

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// The codes of the findings lola update leaves a module as installed with (spec 2026-10-29 §5.1).
const (
	CodeNotInManifest = "not-in-manifest" // no layer lists the module
	CodeTargetsDiffer = "targets-differ"  // its entry asks for other targets than are installed
	CodeFetchFailed   = "fetch-failed"    // its source could not be fetched (spec §5.2)
	CodeSourceGone    = "source-gone"     // its source folder is gone, so it could not be checked
)

// CodeNativeExecutable is the finding that a module installs native executables.
const CodeNativeExecutable = "module/native-executable"

// fetchFailedError is a fetch of an entry's source that failed (network, auth, a ref or source
// that is gone): lola update leaves the module as installed (FetchesFailedError); every other run
// stops on it. Its message is the fetch's.
type fetchFailedError struct{ err error }

func (e *fetchFailedError) Error() string { return e.err.Error() }
func (e *fetchFailedError) Unwrap() error { return e.err }

// FetchesFailedError is a lola update that applied what it could and left Modules as installed
// because their fetch failed (spec 2026-10-29 §5.2), or Gone, because their source folder is gone
// and there was nothing to check them against: Sync returns it with the result.
type FetchesFailedError struct{ Modules, Gone []string }

func (e *FetchesFailedError) Error() string {
	var parts []string
	if len(e.Modules) > 0 {
		parts = append(parts, "could not fetch "+strings.Join(e.Modules, ", "))
	}
	if len(e.Gone) > 0 {
		parts = append(parts, strings.Join(e.Gone, ", ")+": source gone; not checked")
	}
	return strings.Join(parts, "; ") + "; left as installed"
}

// freshFor is cache_fresh_for: in config.yaml, one day when it does not say.
func (e *Engine) freshFor() (config.Age, error) {
	cfg, err := config.LoadConfig(e.Paths.Config, e.Paths.Home)
	if err != nil {
		return config.Age{}, err
	}
	if cfg.CacheFreshFor == (config.Age{}) {
		return config.Age{N: 1}, nil
	}
	return cfg.CacheFreshFor, nil
}

// fresh reports whether a head fetched at fetched is younger than age at now (spec 2026-10-29
// §4): never for an unknown time, for never or for the zero Age; a time after now (another
// machine's clock) is fresh.
func fresh(fetched, now time.Time, age config.Age) bool {
	if fetched.IsZero() || age.Never || age.N <= 0 {
		return false
	}
	return !age.Covers(int(max(now.Sub(fetched), 0) / (24 * time.Hour)))
}

// installedLimit is lola update's run (spec 2026-10-29 §5.1): the modules installed in sc (those
// named, when any) that a layer lists (want). Each installed module of the run no layer lists is
// left as installed with a finding whose hints are sync, which would remove it, or listing it
// again; a named module that is not installed is refused before anything is fetched, hinting its
// install, after the sync that installs it as a layer lists it, when one does. display is
// the run's output flags, which the hints keep.
func (e *Engine) installedLimit(sc Scope, want []Wanted, led *ledger.Ledger, named, display []string) (syncLimit, []target.Finding, error) {
	base := e.manifestName(sc)
	for _, name := range named {
		if led.Modules[name] != nil {
			continue
		}
		var hints []string
		if find(want, name) != nil {
			hints = append(hints, "install it as "+base+" lists it: "+e.SyncCommand(sc, append([]string{name}, display...)...))
		}
		hints = append(hints, "install it: "+e.Command(sc, "install", append([]string{name}, display...)...))
		return syncLimit{}, nil, errs.WithHints(fmt.Errorf("%s is not installed here", untrusted.Text(name, untrusted.MaxName)), hints...)
	}
	l := syncLimit{names: []string{}, only: true}
	var out []target.Finding
	for _, name := range sortedNames(led) {
		if len(named) > 0 && !slices.Contains(named, name) {
			continue
		}
		if find(want, name) == nil {
			out = append(out, target.Finding{Severity: target.SeverityWarn, Module: untrusted.Text(name, untrusted.MaxName), Kind: "module",
				Code: CodeNotInManifest, Text: "no layer lists it; it stays as installed",
				Hint: "remove it: " + e.SyncCommand(sc, append([]string{name}, display...)...) + "; or keep it: list it in " + base + " again"})
			continue
		}
		l.names = append(l.names, name)
	}
	return l, out, nil
}

// listedNotInstalled is lola update's notice of the entries a layer lists (want) that are not
// installed in sc, which it never installs (spec 2026-10-29 §5.1), naming the sync that installs
// them with flags; none when there are none. An installed module no layer lists is not one: its
// finding is installedLimit's.
func (e *Engine) listedNotInstalled(sc Scope, want []Wanted, led *ledger.Ledger, flags []string) []Notice {
	var names []string
	for _, w := range want {
		if led.Modules[w.Name] == nil {
			names = append(names, w.Name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	slices.Sort(names)
	what := fmt.Sprintf("%d modules are", len(names))
	if len(names) == 1 {
		what = "1 module is"
	}
	return []Notice{{Level: LevelInfo,
		Text: what + " listed but not installed: " + nameList(names) + " → " + e.SyncCommand(sc, append(names, flags...)...)}}
}

// resolveHead resolves a fresh entry of lola update from its ref's cache head, as an unpinned
// entry's is (resolveSync), pinning the head's version when it is not the entry's pin. done is
// false when the cache cannot serve it: no head, a git head from before commits were recorded,
// or a head whose version is gone; the ref is then fetched. A cached version is not published
// again: se.staged stays nil.
func (e *Engine) resolveHead(se *syncEntry, run string) (done bool, _ error) {
	name, ent := se.ent.Name, se.ent
	head, ok, err := e.Store.HeadFor(name, ent.Ref, sourceKey(se.spec))
	if err != nil || !ok || se.spec.Kind == source.KindGit && head.Commit == "" {
		return false, err
	}
	v, found, err := e.Store.Pinned(name, head.Meta.Digest, run)
	if err != nil || !found {
		return false, err
	}
	v.Commit, se.v = head.Commit, v
	if v.Commit != ent.Commit || v.Meta.Digest != ent.Digest {
		se.pin = &Pin{Module: name, Ref: shown(ent.Ref), Commit: v.Commit, Digest: v.Meta.Digest, PreviousDigest: ent.Digest, Reason: PinUpdate}
	}
	return true, nil
}
