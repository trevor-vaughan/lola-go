package engine

import (
	"errors"
	"os"
	"slices"

	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/store"
)

// StagedRun is one lola install's staged run: its run directory and the
// versions the run fetched into it, prepared (store.Prepare) but not in the cache. The CLI opens
// one before the first picker and closes it when the command ends. InstallChoices and InstallAll
// given it fetch into it, and read what it holds as the cache will hold it once committed
// (Engine.origin). InstallAll commits the versions it installs when it applies.
//
// The run directory's lock (scratch) is the only lock it holds across the pickers: no other lola
// waits on it, and lola cache clean and the sweep of stale runs skip a directory whose lock is
// held. The markets, profiles, selections, scope and cache locks are taken by each call as before.
type StagedRun struct {
	dir  string
	done func() error
	// publish says the run stages nothing: what it fetches is committed to the cache at once, as
	// for a call given no staged run (lola migrate, lola info's plan, lola cache add).
	publish bool
	fetches []stagedFetch
	// mentions is what the run's pickers, review and apply read of each version's mentions (scans).
	mentions mentionReads
}

// stagedFetch is a version the run prepared; headIfAbsent says its commit must not move a head
// (a fetch by a catalog's commit pin), and committed that the apply put it in the cache.
type stagedFetch struct {
	st           store.Staged
	headIfAbsent bool
	committed    bool
}

// StageInstall opens an install's staged run; the caller closes it on every path.
func (e *Engine) StageInstall() (*StagedRun, error) {
	dir, done, err := e.runDir()
	if err != nil {
		return nil, err
	}
	return &StagedRun{dir: dir, done: done}, nil
}

// publishingRun is the run of a call given no staged run: what it fetches is cached at once.
func (e *Engine) publishingRun() (*StagedRun, error) {
	run, err := e.StageInstall()
	if err != nil {
		return nil, err
	}
	run.publish = true
	return run, nil
}

// Close removes the run's directory, with every version it did not commit, and releases its
// lock. Closing it again does nothing.
func (r *StagedRun) Close() error {
	done := r.done
	if done == nil {
		return nil
	}
	r.done = nil
	return done()
}

// add keeps st, prepared in the run's directory, and returns its version, read from the staged
// tree. A publishing run commits it at once instead, removes the staged tree, and returns the
// cache's version.
func (r *StagedRun) add(s *store.Store, st store.Staged, headIfAbsent bool) (_ store.Version, err error) {
	if r.publish {
		defer func() { err = errors.Join(err, os.RemoveAll(st.Version.ContentDir)) }()
		return s.Commit(st, headIfAbsent)
	}
	r.fetches = append(r.fetches, stagedFetch{st: st, headIfAbsent: headIfAbsent})
	return st.Version, nil
}

// version is the version id of name the run staged, read from its staged tree. A nil run holds
// nothing.
func (r *StagedRun) version(name, id string) (store.Version, bool) {
	if r == nil {
		return store.Version{}, false
	}
	for _, f := range r.fetches {
		if f.st.Version.Name == name && f.st.Version.ID == id {
			return f.st.Version, true
		}
	}
	return store.Version{}, false
}

// origin is name's cache index as it will be once run commits what it staged of name
// (store.Staged.Index): the cache's own for a nil run, or for one that staged nothing of name.
// found is false when neither the cache nor the run holds the name.
func (e *Engine) origin(run *StagedRun, name string) (store.Origin, bool, error) {
	o, found, err := e.Store.Origin(name)
	if err != nil || run == nil {
		return o, found, err
	}
	for _, f := range run.fetches {
		if f.committed || f.st.Version.Name != name {
			continue
		}
		own, ownFound, err := e.Store.OriginFor(name, f.st.Key())
		if err != nil {
			return o, found, err
		}
		o, found = f.st.Index(own, ownFound, f.headIfAbsent), true
	}
	return o, found, nil
}

// committing is the cache index checkName reads for name: as it will be once run commits what it
// staged of name (origin), or as the cache holds it now in a dry run, which commits nothing.
func (e *Engine) committing(run *StagedRun, dry bool, name string) func() (store.Origin, bool, error) {
	if dry {
		run = nil
	}
	return func() (store.Origin, bool, error) { return e.origin(run, name) }
}

// commit puts in the cache, once each and in the order the run staged them, the versions changes
// install: a change's module at the digest its lock.json entry records. A version no change
// installs stays out of the cache, and Close drops it. It returns the versions it committed,
// as the cache holds them, those before a failure too.
func (r *StagedRun) commit(s *store.Store, changes []plan.ModuleChange) ([]store.Version, error) {
	var committed []store.Version
	for i := range r.fetches {
		f := &r.fetches[i]
		v := f.st.Version
		installs := slices.ContainsFunc(changes, func(c plan.ModuleChange) bool {
			return c.Name == v.Name && c.Entry != nil && c.Entry.Digest == v.Meta.Digest
		})
		if f.committed || !installs {
			continue
		}
		v, err := s.Commit(f.st, f.headIfAbsent)
		if err != nil {
			return committed, err
		}
		f.committed = true
		committed = append(committed, v)
	}
	return committed, nil
}

// scans is the run's record of the mention scans done in it, which the pickers, the review's
// preview and the install share, so each version is read once; a nil run (a call that opens its
// own) gets a new one.
func (r *StagedRun) scans() mentionReads {
	if r == nil {
		return mentionReads{}
	}
	if r.mentions == nil {
		r.mentions = mentionReads{}
	}
	return r.mentions
}

// holds reports whether v is a version the run staged and has not committed: the cache does not
// hold it yet. A nil run holds nothing.
func (r *StagedRun) holds(v store.Version) bool {
	return r != nil && slices.ContainsFunc(r.fetches, func(f stagedFetch) bool {
		return !f.committed && f.st.Version.Name == v.Name && f.st.Version.ID == v.ID
	})
}
