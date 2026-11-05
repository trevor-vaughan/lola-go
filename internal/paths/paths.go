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
	UserProfiles   string   // ConfigHome/lola/profiles: the user's profiles and default.yml (Plan 6b §4.1)
	UserLedger     string   // StateHome/lola/lock.json
	UserLock       string   // StateHome/lola/.lock
	Locks          string   // StateHome/lola/locks: one lock file per project, outside the project's .lola/
	SelectionsLock string   // StateHome/lola/locks/selections.lock: taken before any scope lock
	Keep           string   // ConfigHome/lola/keep.yaml: what lola tidy never offers (Plan 6d §5)
	KeepLock       string   // StateHome/lola/locks/keep.lock: around keep.yaml's read-modify-write, after the scope lock (Plan 6d)
	ProfilesLock   string   // StateHome/lola/locks/profiles.lock: shared to read remote profiles, exclusive to write them; taken first (Plan 6b §10)
	Markets        string   // ConfigHome/lola/markets.yaml: your marketplaces (Plan 6c §5)
	MarketCache    string   // CacheHome/lola/markets: one directory per marketplace, holding its catalog and source.json
	MarketsLock    string   // StateHome/lola/locks/markets.lock: shared to read marketplaces, exclusive to write them; after the profiles lock (Plan 6c §5)
	Consent        string   // StateHome/lola/consent.json: install hook consent (§9.1)
	Trust          string   // StateHome/lola/trust.json: per project, the project.yml view the user trusted (Plan 6a §7)
	TrustLock      string   // StateHome/lola/locks/trust.lock: held around trust.json's read-modify-write, after every scope lock
	Trash          string   // StateHome/lola/trash: what lola migrate and lola tidy back up or move, one directory per run
	Store          string   // CacheHome/lola/modules: the cache
	StoreLock      string   // CacheHome/lola/.lock
	Staging        string   // CacheHome/lola/.staging: same filesystem as Store, so renames never cross devices
	Tmp            string   // CacheHome/lola/tmp: downloads
	UserModules    string   // DataHome/lola/user-modules: module copies at user scope
	RemoteProfiles string   // DataHome/lola/profiles: one directory per remote profile, holding profile.yml and source.json (Plan 6b §4.1, D3)
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
	p.UserProfiles = filepath.Join(p.ConfigHome, "lola", "profiles")
	p.UserLedger = filepath.Join(p.StateHome, "lola", "lock.json")
	p.UserLock = filepath.Join(p.StateHome, "lola", ".lock")
	p.Locks = filepath.Join(p.StateHome, "lola", "locks")
	p.SelectionsLock = filepath.Join(p.Locks, "selections.lock")
	p.Keep = filepath.Join(p.ConfigHome, "lola", "keep.yaml")
	p.KeepLock = filepath.Join(p.Locks, "keep.lock")
	p.ProfilesLock = filepath.Join(p.Locks, "profiles.lock")
	p.Markets = filepath.Join(p.ConfigHome, "lola", "markets.yaml")
	p.MarketCache = filepath.Join(p.CacheHome, "lola", "markets")
	p.MarketsLock = filepath.Join(p.Locks, "markets.lock")
	p.Consent = filepath.Join(p.StateHome, "lola", "consent.json")
	p.Trust = filepath.Join(p.StateHome, "lola", "trust.json")
	p.TrustLock = filepath.Join(p.Locks, "trust.lock")
	p.Trash = filepath.Join(p.StateHome, "lola", "trash")
	p.Store = filepath.Join(p.CacheHome, "lola", "modules")
	p.StoreLock = filepath.Join(p.CacheHome, "lola", ".lock")
	p.Staging = filepath.Join(p.CacheHome, "lola", ".staging")
	p.Tmp = filepath.Join(p.CacheHome, "lola", "tmp")
	p.UserModules = filepath.Join(p.DataHome, "lola", "user-modules")
	p.RemoteProfiles = filepath.Join(p.DataHome, "lola", "profiles")
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
