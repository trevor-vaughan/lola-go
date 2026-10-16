// Package paths resolves lola's XDG locations once, from an injected getenv.
package paths

import (
	"fmt"
	"path/filepath"
)

// Paths holds every machine-wide location lola uses. It is computed once at startup and
// passed down explicitly; there are no package-level path globals.
type Paths struct {
	Home, ConfigHome, CacheHome, StateHome, DataHome string

	Config         string   // ConfigHome/lola/config.yaml: settings (default_scope)
	Selections     string   // ConfigHome/lola/selections.yaml: components the user chose not to install
	UserManifest   string   // ConfigHome/lola/user.yml
	UserLedger     string   // StateHome/lola/lock.json
	UserLock       string   // StateHome/lola/.lock
	Locks          string   // StateHome/lola/locks: one lock file per project, outside the project's .lola/
	SelectionsLock string   // StateHome/lola/locks/selections.lock: taken before any scope lock
	Consent        string   // StateHome/lola/consent.json: install hook consent (§9.1)
	Store          string   // CacheHome/lola/modules: the cache
	StoreLock      string   // CacheHome/lola/.lock
	Staging        string   // CacheHome/lola/.staging: same filesystem as Store, so renames never cross devices
	Tmp            string   // CacheHome/lola/tmp: downloads
	UserModules    string   // DataHome/lola/user-modules: module copies at user scope
	PluginData     string   // DataHome/lola/plugin-data
	Probes         string   // CacheHome/lola/probes.json: assistant versions, per binary build
	Search         []string // absolute PATH entries, where lola looks for assistants' binaries
}

// Resolve computes Paths. An XDG variable that is unset, empty or relative falls back to
// its $HOME-based default on every OS (the XDG spec says relative values must be ignored).
func Resolve(getenv func(string) string) (Paths, error) {
	home := getenv("HOME")
	if home == "" || !filepath.IsAbs(home) {
		return Paths{}, fmt.Errorf("HOME must be set to an absolute path (got %q)", home)
	}
	home = filepath.Clean(home)
	xdg := func(key, def string) string {
		if v := getenv(key); v != "" && filepath.IsAbs(v) {
			return filepath.Clean(v)
		}
		return filepath.Join(home, def)
	}
	p := Paths{
		Home:       home,
		ConfigHome: xdg("XDG_CONFIG_HOME", ".config"),
		CacheHome:  xdg("XDG_CACHE_HOME", ".cache"),
		StateHome:  xdg("XDG_STATE_HOME", filepath.Join(".local", "state")),
		DataHome:   xdg("XDG_DATA_HOME", filepath.Join(".local", "share")),
	}
	p.Config = filepath.Join(p.ConfigHome, "lola", "config.yaml")
	p.Selections = filepath.Join(p.ConfigHome, "lola", "selections.yaml")
	p.UserManifest = filepath.Join(p.ConfigHome, "lola", "user.yml")
	p.UserLedger = filepath.Join(p.StateHome, "lola", "lock.json")
	p.UserLock = filepath.Join(p.StateHome, "lola", ".lock")
	p.Locks = filepath.Join(p.StateHome, "lola", "locks")
	p.SelectionsLock = filepath.Join(p.Locks, "selections.lock")
	p.Consent = filepath.Join(p.StateHome, "lola", "consent.json")
	p.Store = filepath.Join(p.CacheHome, "lola", "modules")
	p.StoreLock = filepath.Join(p.CacheHome, "lola", ".lock")
	p.Staging = filepath.Join(p.CacheHome, "lola", ".staging")
	p.Tmp = filepath.Join(p.CacheHome, "lola", "tmp")
	p.UserModules = filepath.Join(p.DataHome, "lola", "user-modules")
	p.PluginData = filepath.Join(p.DataHome, "lola", "plugin-data")
	p.Probes = filepath.Join(p.CacheHome, "lola", "probes.json")
	// Relative PATH entries are ignored, like Go's exec.ErrDot: a repository must not be able
	// to plant a binary that lola runs to read a version.
	for _, dir := range filepath.SplitList(getenv("PATH")) {
		if filepath.IsAbs(dir) {
			p.Search = append(p.Search, filepath.Clean(dir))
		}
	}
	return p, nil
}
