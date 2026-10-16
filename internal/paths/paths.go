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

	Config       string // ConfigHome/lola/config.yaml
	UserManifest string // ConfigHome/lola/user.yml
	UserLedger   string // StateHome/lola/lock.json
	UserLock     string // StateHome/lola/.lock
	Store        string // DataHome/lola/modules
	StoreLock    string // DataHome/lola/.lock
	PluginData   string // DataHome/lola/plugin-data
	Staging      string // DataHome/lola/.staging: same filesystem as Store, so renames never cross devices
	Tmp          string // CacheHome/lola/tmp: downloads
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
	p.UserManifest = filepath.Join(p.ConfigHome, "lola", "user.yml")
	p.UserLedger = filepath.Join(p.StateHome, "lola", "lock.json")
	p.UserLock = filepath.Join(p.StateHome, "lola", ".lock")
	p.Store = filepath.Join(p.DataHome, "lola", "modules")
	p.StoreLock = filepath.Join(p.DataHome, "lola", ".lock")
	p.PluginData = filepath.Join(p.DataHome, "lola", "plugin-data")
	p.Staging = filepath.Join(p.DataHome, "lola", ".staging")
	p.Tmp = filepath.Join(p.CacheHome, "lola", "tmp")
	return p, nil
}
