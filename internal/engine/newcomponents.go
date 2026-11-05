package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/store"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// maxNamed is how many components a notice names before it says how many more.
const maxNamed = 5

// installedSet is the components of the version of name that entry, its ledger entry at a scope,
// pins (cherry-pick design §3.3). known is false when nothing is installed or the cache no longer
// holds that version (a cleaned cache): nothing may then be called new. Any other failure to
// read the version is returned.
func (e *Engine) installedSet(name string, entry *ledger.Entry) (set []Component, known bool, err error) {
	if entry == nil {
		return nil, false, nil
	}
	id := versionID(entry.Digest)
	if id == "" {
		return nil, false, nil
	}
	v, err := e.Store.Load(name, id)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return components(v.Module), true, nil
}

// newComponentNotices names, in one info notice, the components v adds over the version entry
// installs at a scope that only the only: list in force leaves out (cherry-pick design §3.4): at
// most maxNamed, then how many more, with the run of the --unskip of those named. repick, the
// caller's, names the only: list's origin (x's) as output does and gives that run (args: the
// module, then the flags), keeping the scope's flags; it is asked only for a notice, and its error
// is returned. It names nothing when no only: list is in force, v is the installed version or the
// installed set is unknown, nor a rule while the module's rules are off. An installed version the
// cache holds but cannot read is a warning.
func (e *Engine) newComponentNotices(v store.Version, entry *ledger.Entry, x config.Exclusions, rulesOn bool,
	repick func(origin string, args ...string) (label, run string, err error)) ([]Notice, error) {
	if only, _ := x.Only(); entry == nil || entry.Digest == v.Meta.Digest || only == nil {
		return nil, nil
	}
	had, known, err := e.installedSet(v.Name, entry)
	if err != nil {
		return []Notice{{Level: LevelWarn, Text: fmt.Sprintf("%s: cannot tell which components are new: %s", v.Name, untrusted.CleanErr(err).Error())}}, nil
	}
	if !known {
		return nil, nil
	}
	var left []Component
	for _, c := range components(v.Module) {
		if !slices.Contains(had, c) && (rulesOn || c.Kind != config.KindRule) && x.LeftOutByOnly(c.Kind, c.Name) {
			left = append(left, c)
		}
	}
	if len(left) == 0 {
		return nil, nil
	}
	args := []string{v.Name}
	for _, c := range left[:min(len(left), maxNamed)] {
		args = append(args, "--unskip", c.String())
	}
	_, origin := x.Only()
	label, run, err := repick(origin, args...)
	if err != nil {
		return nil, err
	}
	return []Notice{{Level: LevelInfo, Text: fmt.Sprintf("%s adds %s, not installed (only: in %s) → %s", v.Name, namedList(left), label, run)}}, nil
}
