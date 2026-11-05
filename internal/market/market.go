// Package market reads marketplaces (Plan 6c §4–§6.1): your registration file, markets.yaml,
// and the catalogs it names, in Claude Code's marketplace.json format or the Python lola's YAML,
// adapted into one model. A catalog is a stranger's input: every source it names is classified
// from its text alone, so this package has no file system and no network (depguard denies it os
// and io/fs); the engine reads and writes the files and fetches.
package market

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// The catalog formats (Plan 6c §4.1, §4.2).
const (
	FormatClaude = "claude"    // Claude Code's marketplace.json
	FormatYAML   = "lola-yaml" // the Python lola's catalog
)

// Limits on a catalog (Plan 6c §4.4): over the first three refuses the whole catalog, over the
// others skips only that entry.
const (
	MaxCatalog     = 8 << 20 // bytes of a catalog file, applied before decoding
	maxEntries     = 4096
	maxDepth       = 64 // JSON nesting
	maxSource      = 4096
	maxDescription = 1024 // a catalog's
	// An entry's description: real catalogs hold ones over 1024 bytes (ruling R-6c1-6).
	maxEntryDescription = 4096
	maxText             = untrusted.MaxName // a catalog's name, owner and version, and an entry's version
	// What decoding may allocate, which the byte cap alone does not bound (Plan 6c §10): JSON
	// values, YAML bytes (a node costs about 120 bytes decoded) and YAML nodes. Over these refuses
	// the whole catalog.
	maxValues      = 1 << 18
	maxYAMLCatalog = 512 << 10
	maxNodes       = 1 << 17
	maxTags        = 64
	maxTag         = 64
)

// Catalog is a marketplace's catalog in lola's model. Every string is as the catalog wrote it,
// untrusted: it reaches output only through untrusted.Text.
type Catalog struct {
	Format                            string // FormatClaude or FormatYAML
	Name, Description, Version, Owner string
	Entries                           []Entry
	Skipped                           []Skip
}

// Skip is an entry lola does not offer, and why. Name is "" when the entry had no usable name.
// Kind is the short, countable form of the reason ("npm source") that a fetch's summary groups.
type Skip struct {
	Name, Reason, Kind string
}

// kindPlurals is each Skip.Kind for a count of more than one.
var kindPlurals = map[string]string{
	"ambiguous entry":                              "ambiguous entries",
	"command source":                               "command sources",
	"component list on another repository":         "component lists on another repository",
	"entry declaring components":                   "entries declaring components",
	"entry with headers":                           "entries with headers",
	"entry with headersHelper":                     "entries with headersHelper",
	"invalid name":                                 "invalid names",
	"local or private source":                      "local or private sources",
	"malformed entry":                              "malformed entries",
	"malformed pin":                                "malformed pins",
	"missing component":                            "missing components",
	"missing directory":                            "missing directories",
	"name listed twice":                            "names listed twice",
	"npm source":                                   "npm sources",
	"oversized entry":                              "oversized entries",
	"path outside the marketplace":                 "paths outside the marketplace",
	"path outside the plugin":                      "paths outside the plugin",
	"path outside the source":                      "paths outside the source",
	"relative source of a single-file marketplace": "relative sources of a single-file marketplace",
	"source that is not https":                     "sources that are not https",
	"ssh source":                                   "ssh sources",
	"unsupported source":                           "unsupported sources",
	"unusable source":                              "unusable sources",
}

// CountKind is n skipped entries of kind, a Skip.Kind, for a summary: "1 npm source", "2 npm
// sources".
func CountKind(n int, kind string) string {
	if p, ok := kindPlurals[kind]; ok && n != 1 {
		kind = p
	}
	return fmt.Sprintf("%d %s", n, kind)
}

// Entry is one module a catalog offers. Source is where it comes from, as a manifest entry
// records it: its name, source, ref, path and commit, never a digest. Relative is the directory
// of a Claude relative source inside the marketplace's tree; Source is empty until Bind maps it
// to the marketplace's own source. Kind is the source kind ("git", "archive", "folder"), set with
// Source. Tags holds the catalog's tags, keywords and category, for search.
type Entry struct {
	Name                 string
	Description, Version string
	Tags                 []string
	Source               manifest.Entry
	Kind                 string
	Relative             string
	ArchiveSHA256        string   // a Claude archive source's sha256, lower-cased
	Notices              []string // what lola does not do for it: dependencies, install scripts
	Dependencies         []string // the names a Claude entry's dependencies give, as written: lola installs none
	// Components, when set, is what a relative entry's own lists select from its directory: the
	// module is those skills, commands and agents alone (user decision, 2026-10-21).
	Components *Components
}

// Components are the paths, cleaned and relative to a relative entry's directory, that its
// catalog lists as its skills, commands and agents: a skill is a directory, a command or an agent
// a file or a directory. Every fetch of the entry's source selects them (source.Components).
type Components = source.Components

// Spec is where lola fetches e from (Plan 6c §6.4): its source, with the catalog's ref and never
// its commit, which is checked against that ref; an archive's sha256, verified before extraction;
// and, unless the entry is relative (the marketplace's own source, which you typed), the rules of
// a source a stranger named (source.Spec.FromCatalog).
func (e Entry) Spec() source.Spec {
	return source.Spec{Kind: source.Kind(e.Kind), Location: e.Source.Source, Ref: e.Source.Ref, Path: e.Source.Path, SHA256: e.ArchiveSHA256,
		FromCatalog: e.Relative == "", Components: e.Components}
}

// verbs are the Python lola's `lola market` subcommands (Plan 6c D8), which none of your
// marketplaces can be named.
var verbs = []string{"add", "ls", "list", "rm", "remove", "update", "set", "show", "search"}

// Verbs is the Python lola's `lola market` subcommands, a copy (Plan 6c D8).
func Verbs() []string { return slices.Clone(verbs) }

// ValidateName refuses a name no marketplace can have: one that fails the module-name rule, and
// one of Verbs (Plan 6c §4.4).
func ValidateName(name string) error {
	if err := module.ValidateName("marketplace", name); err != nil {
		return err
	}
	if slices.Contains(verbs, name) {
		return fmt.Errorf("marketplace name %s is reserved: it is the Python lola's lola market %s, so it cannot name a marketplace", name, name)
	}
	return nil
}

// Slug is s made into a marketplace name, "" when nothing of it can be: lower-cased, every run
// of other characters a '-', starting with a letter or digit, at most 64 bytes, no "..".
func Slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
			dash = r == '-'
		case !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	out := b.String()
	for strings.Contains(out, "..") {
		out = strings.ReplaceAll(out, "..", ".")
	}
	out = strings.TrimLeft(out, "._-")
	if len(out) > 64 {
		out = out[:64]
	}
	out = strings.TrimRight(out, "._-")
	if module.ValidateName("marketplace", out) != nil {
		return ""
	}
	return out
}

// FreeName is the first of base, base-2, base-3, … that a marketplace can be named and taken
// says is free; start 2 skips base itself. "" when base is "" or none of the first thousand is.
func FreeName(base string, start int, taken func(string) bool) string {
	if base == "" {
		return ""
	}
	for i := start; i < start+1000; i++ {
		name := base
		if i > 1 {
			suffix := fmt.Sprintf("-%d", i)
			name = strings.TrimRight(base[:min(len(base), 64-len(suffix))], "._-") + suffix
		}
		if ValidateName(name) == nil && !taken(name) {
			return name
		}
	}
	return ""
}

// CatalogFiles is where lola looks for a catalog at a marketplace's root, in order (Plan 6c §6.2
// step 3; the Python lola's lookup minus its single-YAML guess): Claude Code's file, then the
// Python lola's, <name>.yml and .yaml for the marketplace's name (none when name is ""), then
// marketplace.yml and .yaml.
func CatalogFiles(name string) []string {
	out := []string{".claude-plugin/marketplace.json"}
	if name != "" {
		out = append(out, name+".yml", name+".yaml")
	}
	return append(out, "marketplace.yml", "marketplace.yaml")
}

// FormatOf is the format of a catalog file by its name: FormatClaude for .json, FormatYAML for
// .yml and .yaml, "" for anything else.
func FormatOf(file string) string {
	switch strings.ToLower(path.Ext(file)) {
	case ".json":
		return FormatClaude
	case ".yml", ".yaml":
		return FormatYAML
	}
	return ""
}

// FormatName is how output names a format.
func FormatName(format string) string {
	if format == FormatClaude {
		return "Claude marketplace.json"
	}
	return "Python lola YAML"
}

// Root is the marketplace root of the catalog at file, a slash path in the fetched tree: the
// directory holding .claude-plugin/ for Claude Code's file, else the file's directory; "" for the
// tree's root. Claude relative sources are joined to it.
func Root(file string) string {
	dir := path.Dir(file)
	if path.Base(dir) == ".claude-plugin" {
		dir = path.Dir(dir)
	}
	if dir == "." {
		return ""
	}
	return dir
}

// CheckRelative refuses a relative path a stranger's file names inside a marketplace (Plan 6c
// §4.4): it must be slash-separated, relative, with no ".." element and no backslash. It
// returns the path cleaned, "." for the root itself.
func CheckRelative(p string) (string, error) {
	shown := untrusted.Text(p, untrusted.MaxName)
	switch {
	case p == "":
		return "", errors.New("is empty")
	case strings.Contains(p, `\`):
		return "", fmt.Errorf("%s holds a backslash; paths in a marketplace are slash-separated", shown)
	case strings.HasPrefix(p, "/"), len(p) > 1 && p[1] == ':':
		return "", fmt.Errorf("%s is absolute; it must be relative to the marketplace", shown)
	case slices.Contains(strings.Split(p, "/"), ".."):
		return "", fmt.Errorf("%s leaves the marketplace (..)", shown)
	}
	return path.Clean(p), nil
}
