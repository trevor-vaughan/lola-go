package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/config"
	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/ledger"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// pinsWrite is the write of pins.yaml a user-scope apply makes (spec 2026-10-29 §3.4), led being
// the ledger the apply leaves, names the modules the run resolved or changed, kept those of them
// whose version the run kept rather than chose, and man its other writes: under pins.lock, taken last, its next bytes are computed (nextPins) from planned, the
// file as the run read it to plan (a sync's pin source), else from the file read now. The write is
// nil when the bytes do not change, or when a file that does not exist would hold nothing; its
// Check refuses a file changed since that read, so a pin synced in while the run fetched and
// planned is never overwritten. The caller releases pins.lock with the returned unlock once the
// apply is done.
func (e *Engine) pinsWrite(planned *config.Pins, led *ledger.Ledger, names, kept []string, man []plan.ManifestWrite) (*plan.ManifestWrite, func(), []Notice, error) {
	unlock, err := config.Lock(e.Paths.PinsLock)
	if err != nil {
		return nil, nil, nil, err
	}
	pins := planned
	if pins == nil {
		if pins, err = config.LoadPins(e.Paths.Pins); err != nil {
			unlock()
			return nil, nil, nil, err
		}
	}
	data, notices, write, err := e.nextPins(pins, led, names, kept, man, nil)
	if err != nil {
		unlock()
		return nil, nil, nil, err
	}
	if !write {
		unlock()
		return nil, func() {}, notices, nil
	}
	dst, err := linkTarget(e.Paths.Pins)
	if err != nil {
		unlock()
		return nil, nil, nil, err
	}
	return &plan.ManifestWrite{Path: dst, Data: data, Check: pins.Current, KeepMode: true}, unlock, notices, nil
}

// nextPins is pins.yaml's next bytes (spec 2026-10-29 §3.4) and whether they are a write. The pin
// of each module in names that an entry of a profile of yours installed (led, the ledger the run
// leaves; nil sets none) is set, unless it is no pin pins.yaml can hold (a source on this
// machine), or the module is in kept (the run kept its version rather than chose it) and pins.yaml
// has a pin for its key: every other pin is kept as read, so a newer pin synced from another
// machine survives a run that does not resolve its module, or keeps the version installed. Every pin whose key no file of yours lists is dropped
// (ownKeys, man being the run's other writes), unless one of them does not load: then a warning.
// The remote profiles' records follow (remoteRecords, drop being those a --remove drops).
// Unchanged bytes are no write (dotfile churn), nor is a file that held nothing and would hold
// nothing.
func (e *Engine) nextPins(pins *config.Pins, led *ledger.Ledger, names, kept []string, man []plan.ManifestWrite, drop []string) ([]byte, []Notice, bool, error) {
	modules := map[string][]config.Pin{}
	for name, list := range pins.Modules {
		modules[name] = slices.Clone(list)
	}
	if led != nil {
		for _, name := range names {
			in := led.Modules[name]
			if in == nil || !ownFrom(in.From) {
				continue
			}
			pin := config.Pin{Source: in.Source, Ref: in.Ref, Path: in.Path, Commit: in.Commit, Digest: in.Digest}
			if pin.Check() != nil {
				continue
			}
			k := slices.IndexFunc(modules[name], func(o config.Pin) bool { return sameKey(o, pin) })
			switch {
			case k < 0:
				modules[name] = append(modules[name], pin)
			case !slices.Contains(kept, name):
				modules[name][k] = pin
			}
		}
	}
	var notices []Notice
	if keys, unread := e.ownKeys(man); unread == "" {
		for name, list := range modules {
			modules[name] = slices.DeleteFunc(list, func(pin config.Pin) bool {
				return !slices.ContainsFunc(keys[name], func(k config.Pin) bool { return sameKey(k, pin) })
			})
		}
	} else {
		notices = append(notices, Notice{Level: LevelWarn, Text: unread + " does not load, so pins.yaml keeps every pin it holds"})
	}
	remotes, err := e.remoteRecords(pins.Remotes, drop)
	if err != nil {
		return nil, nil, false, err
	}
	data, err := pins.Bytes(modules, remotes)
	if err != nil {
		return nil, nil, false, err
	}
	nothing := len(remotes) == 0 && len(pins.Modules) == 0 && len(pins.Remotes) == 0
	for _, list := range modules {
		nothing = nothing && len(list) == 0
	}
	return data, notices, !pins.Same(data) && !nothing, nil
}

// writePinsNow writes pins.yaml outside a scope's apply (spec 2026-10-29 §3.4: lola profile --add,
// --update and --remove record your remote profiles): only remotes: changes, as remoteRecords says
// without the profiles named in drop; modules: is kept as read. It runs under pins.lock, checks the
// file is unchanged since the read, writes atomically through a link, and writes nothing when the
// bytes do not change.
func (e *Engine) writePinsNow(drop []string) error {
	unlock, err := config.Lock(e.Paths.PinsLock)
	if err != nil {
		return err
	}
	defer unlock()
	pins, err := config.LoadPins(e.Paths.Pins)
	if err != nil {
		return &unreadPinsError{err}
	}
	remotes, err := e.remoteRecords(pins.Remotes, drop)
	if err != nil {
		return err
	}
	data, err := pins.Bytes(pins.Modules, remotes)
	if err != nil {
		return err
	}
	if pins.Same(data) || (len(remotes) == 0 && len(pins.Modules) == 0 && len(pins.Remotes) == 0) {
		return nil
	}
	if err := pins.Current(); err != nil {
		return err
	}
	dst, err := linkTarget(e.Paths.Pins)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	mode, err := fsx.KeptMode(dst, 0o600)
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(dst, data, mode)
}

// unreadPinsError is writePinsNow's refusal of a pins.yaml that does not load (config.LoadPins',
// which names the file and line), which every run that reads the file refuses until it is fixed.
type unreadPinsError struct{ error }

func (e *unreadPinsError) Unwrap() error { return e.error }

// unrecorded is the warning of an --add or --update that wrote the remote profiles names (done
// says so) but could not record them in pins.yaml (writePinsNow's err): a pins.yaml that does not
// load, where (its refusal's path:line), and that a sync records them once it is fixed by hand,
// since every sync refuses it until then; any other failure, that a sync records them once the
// file can be written.
func (e *Engine) unrecorded(done string, names []string, err error) Notice {
	them, sync, why := itThem(names), e.SyncCommand(Scope{Kind: target.User}), shown(untrusted.CleanErr(err).Error())
	if _, unread := errors.AsType[*unreadPinsError](err); unread {
		return Notice{Level: LevelWarn, Text: done + ", but pins.yaml does not load, so it does not record " + them + ": " + why + " → fix it by hand, then " +
			sync + " records " + them}
	}
	return Notice{Level: LevelWarn, Text: done + ", but pins.yaml could not record " + them + ": " + why + " → " + sync + " records " + them +
		" once pins.yaml can be written"}
}

// sameKey reports whether pins a and b are of one entry key: source, ref and path.
func sameKey(a, b config.Pin) bool { return a.Source == b.Source && a.Ref == b.Ref && a.Path == b.Path }

// linkTarget is the file a write of path goes to: the target of the link path is, else path.
func linkTarget(path string) (string, error) {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return path, nil
	case err != nil:
		return "", err
	case fi.Mode()&fs.ModeSymlink == 0:
		return path, nil
	}
	dst, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", errs.WithHints(fmt.Errorf("%s is a symlink lola cannot follow: %w", untrusted.Text(path, untrusted.MaxName), untrusted.CleanErr(err)),
			"point the link at a file, or remove it")
	}
	return dst, nil
}

// ownFrom reports whether lock.json's from names a profile of yours: default or user:<name>.
func ownFrom(from string) bool {
	q, ok := strings.CutPrefix(from, ledger.FromProfile(""))
	return ok && (q == profile.DefaultName || strings.HasPrefix(q, string(profile.KindUser)+":"))
}

// ownKeys is the entry keys (source, ref, path) every file of yours lists once the apply is done,
// by module: default.yml and profiles/*.yml as on disk or, for a file the apply writes (man, its
// writes, at a path or a link's target), as it will hold. unread names the file that does not
// load ("" when every one does): nothing is pruned then. An entry whose marketplace address is
// unresolved does not stop the file from counting (as Profile.holds reads it).
func (e *Engine) ownKeys(man []plan.ManifestWrite) (keys map[string][]config.Pin, unread string) {
	infos, _, _, err := profile.Visible(e.Paths, true)
	if err != nil {
		return nil, shown(e.Paths.UserProfiles)
	}
	keys = map[string][]config.Pin{}
	add := func(p *profile.Profile) {
		for _, ent := range p.Modules {
			keys[ent.Name] = append(keys[ent.Name], config.Pin{Source: ent.Source, Ref: ent.Ref, Path: ent.Path})
		}
	}
	loads := func(err error) bool {
		_, unresolved := errors.AsType[*manifest.UnresolvedError](err)
		return err == nil || unresolved
	}
	written := func(path string) int {
		dst := path // the file a write of it goes to: a link's target
		if real, err := filepath.EvalSymlinks(path); err == nil {
			dst = real
		}
		return slices.IndexFunc(man, func(w plan.ManifestWrite) bool { return w.Path == path || w.Path == dst })
	}
	for _, i := range infos {
		var p *profile.Profile
		if k := written(i.Path); k >= 0 {
			p, err = profile.Parse(i.Path, man[k].Data)
		} else {
			p, err = profile.Load(i.Path)
		}
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if !loads(err) {
			return nil, shown(i.Path)
		}
		add(p)
	}
	// A profile of yours the apply creates is in man alone.
	for _, w := range man {
		if filepath.Dir(w.Path) != e.Paths.UserProfiles || slices.ContainsFunc(infos, func(i profile.Info) bool { return i.Path == w.Path }) {
			continue
		}
		p, err := profile.Parse(w.Path, w.Data)
		if !loads(err) {
			return nil, shown(w.Path)
		}
		add(p)
	}
	return keys, ""
}

// remoteRecords is the remote profiles' records pins.yaml holds next (spec 2026-10-29 §3.4):
// those held, without the names in drop (a --remove), and each remote profile on disk recorded as
// its source.json says. A record is never dropped for want of a profile on disk: it restores it.
// A profile whose source.json does not read, or holds no record pins.yaml can keep (one today's
// host rules refuse), keeps the record held, if any: LoadPins would refuse the file otherwise.
func (e *Engine) remoteRecords(held map[string]config.Remote, drop []string) (map[string]config.Remote, error) {
	out := map[string]config.Remote{}
	for name, r := range held {
		if !slices.Contains(drop, name) {
			out[name] = r
		}
	}
	infos, _, err := profile.Remote(e.Paths)
	if err != nil {
		return nil, err
	}
	for _, i := range infos {
		if slices.Contains(drop, i.Name) {
			continue
		}
		o, err := profile.ReadOrigin(filepath.Dir(i.Path))
		if err != nil {
			continue
		}
		if r := (config.Remote{Source: o.Source, Ref: o.Ref, Path: o.Path, Commit: o.Commit, Digest: o.Digest}); r.Check() == nil {
			out[i.Name] = r
		}
	}
	return out, nil
}
