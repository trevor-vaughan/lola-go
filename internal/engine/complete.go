package engine

import (
	"cmp"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/profile"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/trash"
)

// The candidates shell completion offers. Like SyncNames and ProfileNames, each reads local files
// only, and no more than it needs: it writes nothing, runs no git and touches no network.

// ComponentNames is the kind/name of every component of the cached modules named, for --skip,
// --only and --unskip: each module's current version's, in picker order, each once. A name the
// cache does not hold, or cannot load, adds none.
func (e *Engine) ComponentNames(modules []string) []string {
	var out []string
	for _, name := range modules {
		v, _, err := e.Current(name)
		if err != nil {
			continue
		}
		for _, c := range components(v.Module) {
			if id := c.String(); !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	return out
}

// InstalledVersions is the version of each module lock.json records as installed in sc, as --at
// takes it: a git module's commit, else its content digest.
func (e *Engine) InstalledVersions(sc Scope) (map[string]string, error) {
	led, err := e.Ledger(sc)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for name, ent := range led.Modules {
		out[name] = cmp.Or(ent.Commit, ent.Digest)
	}
	return out, nil
}

// MoveNames is the modules lola profile [<from>] --move can move in sc: those of from when it names
// a profile of yours or a remote one (user:go, remote:go or a bare name, yours first), in its
// order; else SyncNames', every module the scope has. A project's profile is never read: it is
// committed, and completion parses no file a command has not checked (SyncNames).
func (e *Engine) MoveNames(sc Scope, from string) ([]string, error) {
	p, err := e.ownProfile(sc, from)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return e.SyncNames(sc)
	}
	out := make([]string, 0, len(p.Modules))
	for _, m := range p.Modules {
		out = append(out, m.Name)
	}
	return out, nil
}

// ownProfile is the profile of yours or the remote one name names in sc (MoveNames), read under
// the profiles lock as SyncNames takes it; nil when it names none.
func (e *Engine) ownProfile(sc Scope, name string) (*profile.Profile, error) {
	kind, bare, qualified := strings.Cut(name, ":")
	if !qualified {
		kind, bare = "", name
	}
	if bare == "" || kind != "" && kind != string(profile.KindUser) && kind != string(profile.KindRemote) {
		return nil, nil
	}
	unlock, err := e.readProfiles(true)
	if err != nil {
		return nil, err
	}
	defer unlock()
	named := func(i profile.Info) bool { return i.Name == bare }
	if kind != string(profile.KindRemote) {
		own, _, _, err := profile.Visible(e.Paths, sc.Kind == target.User)
		if err != nil {
			return nil, err
		}
		if k := slices.IndexFunc(own, named); k >= 0 {
			return profile.Load(own[k].Path)
		}
	}
	if kind != string(profile.KindUser) {
		remote, _, err := profile.Remote(e.Paths)
		if err != nil {
			return nil, err
		}
		if k := slices.IndexFunc(remote, named); k >= 0 {
			p, _, err := profile.LoadRemote(remote[k].Path)
			return p, err
		}
	}
	return nil, nil
}

// TrashName is a trash run as completion offers it: its id, and the command that made it.
type TrashName struct {
	ID, Command string
}

// TrashNames is the trash's runs, newest first, as ListTrash orders them, read without measuring a
// file; a run lola cannot read is left out.
func (e *Engine) TrashNames() ([]TrashName, error) {
	ds, err := os.ReadDir(e.Paths.Trash)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var runs []trash.Run
	for _, d := range ds {
		if _, read, err := e.openTrashRun(d.Name(), nil); err == nil {
			runs = append(runs, read)
		}
	}
	slices.SortFunc(runs, func(a, b trash.Run) int { return cmp.Or(b.Time.Compare(a.Time), strings.Compare(b.ID, a.ID)) })
	out := make([]TrashName, 0, len(runs))
	for _, r := range runs {
		out = append(out, TrashName{ID: r.ID, Command: r.Command})
	}
	return out, nil
}

// TrashOriginals is where each entry of the run id names was, as its manifest records it.
func (e *Engine) TrashOriginals(id string) ([]string, error) {
	_, read, err := e.openTrashRun(id, nil)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(read.Entries))
	for _, en := range read.Entries {
		out = append(out, en.Original)
	}
	return out, nil
}
