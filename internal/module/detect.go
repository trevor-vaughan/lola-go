package module

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// adapter converts one plugin format into a lola manifest (Plan 5b design §4). It runs only on
// fetched content, in cache add and check, and its result is written down by Normalize, so a
// cached module never needs it again.
type adapter interface {
	// manifest is the file, relative to the module root, whose presence selects the adapter.
	manifest() string
	// read converts the format's manifest in dir. ok is false when the file is not this
	// format's manifest after all; warnings say why, and detection moves on.
	read(dir string) (lf *lolaFile, warnings []string, ok bool, err error)
}

// adapters lists the format adapters in detection precedence (ADR #243 §4).
func adapters() []adapter { return []adapter{agentPlugins{}, claudePlugin{}} }

// FormatManifests lists every manifest Detect reads: the lola manifests, then each adapter's.
func FormatManifests() []string {
	out := ManifestNames()
	for _, a := range adapters() {
		out = append(out, a.manifest())
	}
	return out
}

// Detect reads the module rooted at freshly fetched content: from its lola manifest, else the
// first plugin manifest an adapter accepts, else its native layout (Plan 5b design §4). Every
// lower-precedence manifest present is recorded as shadowed, with a warning.
func Detect(dir, fallbackName string, known []string) (*Module, error) {
	return detect(dir, fallbackName, known, adapters())
}

// detect is Detect with the adapters given; without any it is Load.
func detect(dir, fallbackName string, known []string, as []adapter) (*Module, error) {
	m := &Module{Format: "native", Name: fallbackName}
	lf, err := readLolaFile(dir, m)
	if err != nil {
		return nil, err
	}
	for _, a := range as {
		switch _, err := os.Lstat(filepath.Join(dir, a.manifest())); {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return nil, ioFail(a.manifest(), err)
		}
		if lf != nil {
			m.Shadowed = append(m.Shadowed, a.manifest())
			m.Warnings = append(m.Warnings, ShadowWarning(m.Manifest, a.manifest()))
			continue
		}
		got, warnings, ok, err := a.read(dir)
		if err != nil {
			return nil, err
		}
		m.Warnings = append(m.Warnings, warnings...)
		if ok {
			lf, m.Manifest = got, a.manifest()
		}
	}
	if lf != nil {
		if err := m.apply(lf); err != nil {
			return nil, err
		}
	}
	return build(dir, m, known)
}
