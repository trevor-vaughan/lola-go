package market

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"go.yaml.in/yaml/v3"

	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
	"github.com/trevor-vaughan/lola-go/internal/yamldoc"
)

// KnownVersion is the known.yaml schema version this binary reads.
const KnownVersion = 1

// knownSeed is the list of marketplaces lola ships, read-only data parsed on demand.
//
//go:embed known.yaml
var knownSeed []byte

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// KnownMarket is a marketplace the lola maintainers curated, pinned to Commit of the git
// repository Source: ID is the name it is enabled as, Path the catalog's folder or file in the
// repository ("" for the root), Curated the day the pin was reviewed (YYYY-MM-DD) and Reason why
// it is listed, one line.
type KnownMarket struct {
	ID      string `yaml:"id" json:"id"`
	Source  string `yaml:"source" json:"source"`
	Path    string `yaml:"path" json:"path"`
	Commit  string `yaml:"commit" json:"commit"`
	Curated string `yaml:"curated" json:"curated"`
	Reason  string `yaml:"reason" json:"reason"`
}

// Known is the marketplaces lola ships, validated by LoadKnown, never nil.
func Known() ([]KnownMarket, error) { return LoadKnown(knownSeed) }

// LoadKnown reads b, a known-marketplaces seed: one YAML document with yamldoc.Decode's guards,
// version KnownVersion, and known keys only. Every entry is checked, and the first that is not
// sound refuses the whole seed, naming it as markets[<index>].<field>: its id follows ValidateName
// and is given once, its source is an https:// git repository without credentials, its path (when
// given) stays inside the repository, its commit is 40 lowercase hex digits, its curated day is
// YYYY-MM-DD and its reason is one line of printable text.
func LoadKnown(b []byte) ([]KnownMarket, error) {
	d, err := yamldoc.Decode(b)
	if err != nil {
		return nil, fmt.Errorf("known marketplaces: %w", err)
	}
	root := yamldoc.Root(d)
	if root == nil || root.Kind != yaml.MappingNode {
		return nil, errors.New("known marketplaces: not a YAML mapping of version and markets")
	}
	var doc struct {
		Version int           `yaml:"version" json:"version"`
		Markets []KnownMarket `yaml:"markets" json:"markets"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("known marketplaces: %s", untrusted.Text(err.Error(), 2*untrusted.MaxName))
	}
	if doc.Version != KnownVersion {
		return nil, fmt.Errorf("known marketplaces: version %d is not %d", doc.Version, KnownVersion)
	}
	out := []KnownMarket{}
	seen := map[string]bool{}
	for i, m := range doc.Markets {
		if field, err := m.check(); err != nil {
			return nil, fmt.Errorf("known marketplaces: markets[%d].%s: %w", i, field, err)
		}
		if seen[m.ID] {
			return nil, fmt.Errorf("known marketplaces: markets[%d].id: %s is listed twice", i, m.ID)
		}
		seen[m.ID] = true
		out = append(out, m)
	}
	return out, nil
}

// check reports the first field of m that is not sound, and why.
func (m KnownMarket) check() (string, error) {
	if err := ValidateName(m.ID); err != nil {
		return "id", err
	}
	if err := checkKnownSource(m.Source); err != nil {
		return "source", err
	}
	if m.Path != "" {
		if _, err := CheckRelative(m.Path); err != nil {
			return "path", err
		}
	}
	if !commitPattern.MatchString(m.Commit) {
		return "commit", errors.New("is not a full commit: give 40 lowercase hex digits, never a branch or tag")
	}
	if _, err := time.Parse(time.DateOnly, m.Curated); err != nil {
		return "curated", errors.New("is not a day: give YYYY-MM-DD")
	}
	if m.Reason == "" {
		return "reason", errors.New("is empty: say why the marketplace is listed")
	}
	if len(m.Reason) > 2*untrusted.MaxName || strings.ContainsFunc(m.Reason, func(r rune) bool { return !unicode.IsPrint(r) }) {
		return "reason", errors.New("must be one line of printable text")
	}
	return "", nil
}

// checkKnownSource refuses src unless it is an https:// URL without credentials that
// source.Recorded accepts and that is not a catalog file. That it names a repository and not an
// archive is the engine's to tell: enabling refuses the --ref on any other kind of source.
func checkKnownSource(src string) error {
	u, err := url.Parse(src)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("must be an https:// git repository URL without credentials")
	}
	if IsCatalogURL(src) {
		return errors.New("is a catalog file; a pin needs a git repository")
	}
	if err := (source.Recorded{Source: src}).Check(); err != nil {
		return errors.New(untrusted.Text(err.Error(), 2*untrusted.MaxName))
	}
	return nil
}
