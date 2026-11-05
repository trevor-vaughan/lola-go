package market

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// who names a catalog entry in source.CheckRemote's refusals.
const who = source.CatalogEntry

// githubRepoRE is a Claude github source's repo: owner/name (Plan 6c §4.3).
var githubRepoRE = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

// sha256RE is an archive source's sha256, in either case.
var sha256RE = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// skip is why an entry is skipped: Kind for the fetch's grouped summary, the reason in full.
type skip struct{ kind, reason string }

func (s *skip) Error() string { return s.reason }

func skipped(kind, format string, args ...any) *skip {
	return &skip{kind: kind, reason: fmt.Sprintf(format, args...)}
}

// remoteSource checks loc, a network source a catalog names, from its text alone (Plan 6c §4.3,
// §10, rulings R-6c-10, R-6c-18): at most maxSource bytes; never ssh:// or git's scp form, which
// would use your ssh keys on a stranger's host, nor any scheme but https://; and the address rule
// of source.CheckRemote, which refuses a path, a file:// URL, localhost and addresses inside your
// network. It is never looked up on disk.
func remoteSource(field, loc string) *skip {
	if len(loc) > maxSource {
		return skipped("oversized entry", "%s is longer than %d bytes", field, maxSource)
	}
	host := source.Host(loc)
	switch scheme, _, _ := strings.Cut(host, "://"); {
	case scheme == "ssh":
		return skipped("ssh source", "ssh sources in a catalog are not supported: they connect to a stranger's host with your ssh keys")
	case host != "" && scheme != "https":
		return skipped("source that is not https", "%s uses %s://; a catalog entry's sources must use https://",
			untrusted.Text(loc, untrusted.MaxName), untrusted.Text(scheme, untrusted.MaxName))
	}
	if err := source.CheckRemote(loc, who); err != nil {
		// The reason alone: what to give instead is for the catalog's author, not for you.
		reason := err.Error()
		if re, ok := errors.AsType[*source.RemoteError](err); ok {
			reason = re.Reason
		}
		return skipped("local or private source", "%s", reason)
	}
	return nil
}

// pin maps a catalog's ref and sha to a manifest entry's ref and commit (Plan 6c §4.3, ruling
// R-6c-12): sha must be a full commit; a ref that is a full commit, in any case, becomes the
// commit, lower-cased (R-9-31), with no ref, so it is checked against the default branch like a sha; a sha and a full-commit ref that
// differ skip the entry.
func pin(ref, sha string) (string, string, *skip) {
	if sha != "" && !source.IsFullCommit(sha) {
		return "", "", skipped("malformed pin", "sha %s is not a full commit SHA (40 or 64 lower-case hex digits)", untrusted.Text(sha, untrusted.MaxName))
	}
	if commit, ok := source.CommitRef(ref); ok {
		if sha != "" && sha != commit {
			return "", "", skipped("malformed pin", "ref and sha disagree: ref %s, sha %s", commit, sha)
		}
		return "", commit, nil
	}
	return ref, sha, nil
}

// checked applies every recorded-source and fetcher rule to e's mapped source of kind (Plan 6c
// §4.3: source.Recorded.Check, then source.Validate); a refusal skips the entry with its reason.
func checked(e *Entry, kind source.Kind) *skip {
	r := e.Source
	if err := (source.Recorded{Source: r.Source, Ref: r.Ref, Path: r.Path, Commit: r.Commit}).Check(); err != nil {
		return skipped("unusable source", "%s", err.Error())
	}
	if err := source.Validate(source.Spec{Kind: kind, Location: r.Source, Ref: r.Ref, Path: r.Path}); err != nil {
		return skipped("unusable source", "%s", err.Error())
	}
	e.Kind = string(kind)
	return nil
}

// mapGit maps a git source at loc with its ref, sha and path, all from the catalog.
func mapGit(e *Entry, field, loc, ref, sha, sub string) *skip {
	if s := remoteSource(field, loc); s != nil {
		return s
	}
	if !source.IsGitForm(loc) {
		return skipped("unusable source", "%s is not a git repository URL", untrusted.Text(loc, untrusted.MaxName))
	}
	if sub != "" {
		clean, err := CheckRelative(sub)
		if err != nil {
			return skipped("path outside the source", "path %s", err.Error())
		}
		if clean != "." {
			sub = clean
		} else {
			sub = ""
		}
	}
	ref, commit, s := pin(ref, sha)
	if s != nil {
		return s
	}
	e.Source = manifest.Entry{Name: e.Name, Source: loc, Ref: ref, Path: sub, Commit: commit}
	return checked(e, source.KindGit)
}

// githubURL is the https URL of a GitHub repository given as owner/name, or a skip.
func githubURL(field, repo string) (string, *skip) {
	if !githubRepoRE.MatchString(repo) {
		return "", skipped("unusable source", "%s %s is not a GitHub owner/name", field, untrusted.Text(repo, untrusted.MaxName))
	}
	return "https://github.com/" + strings.TrimSuffix(repo, ".git") + ".git", nil
}

// sourceKinds are the keys lola reads in a Claude source object.
var sourceKinds = []string{"source", "repo", "url", "ref", "sha", "path", "sha256"}

// mapClaudeSource maps a Claude entry's source object, o (Plan 6c §4.3).
func mapClaudeSource(e *Entry, o *jval) *skip {
	if k := ambiguous(o, sourceKinds); k != "" {
		return skipped("ambiguous entry", "ambiguous key source.%s", untrusted.Text(k, untrusted.MaxName))
	}
	str := func(key string) (string, *skip) {
		v := o.get(key)
		switch {
		case v == nil:
			return "", nil
		case v.kind != 's':
			return "", skipped("malformed entry", "source.%s is not a string", key)
		}
		return v.str, nil
	}
	var vals [7]string
	for i, k := range sourceKinds {
		v, s := str(k)
		if s != nil {
			return s
		}
		vals[i] = v
	}
	kind, repo, loc, ref, sha, sub, sum := vals[0], vals[1], vals[2], vals[3], vals[4], vals[5], vals[6]
	switch {
	case kind == "github" && repo == "":
		return skipped("malformed entry", "source.repo is missing")
	case (kind == "url" || kind == "git-subdir" || kind == "archive") && loc == "":
		return skipped("malformed entry", "source.url is missing")
	}
	switch kind {
	case "github":
		u, s := githubURL("source.repo", repo)
		if s != nil {
			return s
		}
		return mapGit(e, "source.repo", u, ref, sha, "")
	case "url":
		return mapGit(e, "source.url", loc, ref, sha, "")
	case "git-subdir":
		if !strings.Contains(loc, "://") && !strings.Contains(loc, ":") && strings.Count(loc, "/") == 1 {
			u, s := githubURL("source.url", loc)
			if s != nil {
				return s
			}
			loc = u
		}
		if sub == "" {
			return skipped("malformed entry", "a git-subdir source needs a path")
		}
		return mapGit(e, "source.url", loc, ref, sha, sub)
	case "archive":
		if s := remoteSource("source.url", loc); s != nil {
			return s
		}
		if sum != "" && !sha256RE.MatchString(sum) {
			return skipped("malformed pin", "sha256 %s is not 64 hex digits", untrusted.Text(sum, untrusted.MaxName))
		}
		e.ArchiveSHA256 = strings.ToLower(sum)
		e.Source = manifest.Entry{Name: e.Name, Source: loc}
		return checked(e, source.KindArchive)
	case "npm":
		return skipped("npm source", "npm sources are not supported: installing one runs a package manager")
	case "command":
		return skipped("command source", "command sources run a program; lola never does")
	case "":
		return skipped("malformed entry", "source has no source kind")
	}
	return skipped("unsupported source", "%s sources are not supported", untrusted.Text(kind, untrusted.MaxName))
}

// mapRelative maps a Claude relative source, rel, given as a string (Plan 6c §4.3): "./dir" or
// "." from the marketplace root, or a bare name under metadata.pluginRoot (root, cleaned; "" for
// none). Bind gives it the marketplace's own source.
func mapRelative(e *Entry, rel, root string) *skip {
	if len(rel) > maxSource {
		return skipped("oversized entry", "source is longer than %d bytes", maxSource)
	}
	if rel != "." && !strings.HasPrefix(rel, "./") {
		if root == "" {
			return skipped("malformed entry", "relative source %s does not start with ./ (a bare name needs metadata.pluginRoot)",
				untrusted.Text(rel, untrusted.MaxName))
		}
		// Checked before it is joined, so an absolute or escaping name is refused, never rebased.
		if _, err := CheckRelative(rel); err != nil {
			return skipped("path outside the marketplace", "source %s", err.Error())
		}
		rel = root + "/" + rel
	}
	clean, err := CheckRelative(rel)
	if err != nil {
		return skipped("path outside the marketplace", "source %s", err.Error())
	}
	e.Relative = clean
	return nil
}

// mapPython maps a Python catalog's repository, ref and path (Plan 6c §4.3): a git source, or
// an https archive when the URL's path ends in an archive suffix.
func mapPython(e *Entry, repo, ref, sub string) *skip {
	if s := remoteSource("repository", repo); s != nil {
		return s
	}
	if source.IsGitForm(repo) {
		return mapGit(e, "repository", repo, ref, "", sub)
	}
	if ref != "" {
		return skipped("unusable source", "ref %s is given for an archive, which has no branches, tags or commits", untrusted.Text(ref, untrusted.MaxName))
	}
	if sub != "" {
		clean, err := CheckRelative(sub)
		if err != nil {
			return skipped("path outside the source", "path %s", err.Error())
		}
		if clean != "." {
			sub = clean
		} else {
			sub = ""
		}
	}
	e.Source = manifest.Entry{Name: e.Name, Source: repo, Path: sub}
	return checked(e, source.KindArchive)
}

// IsCatalogURL reports whether src, a marketplace's own source as typed, is a catalog file (Plan
// 6c §6.2 step 1): an https:// URL, or an http:// one, whose path ends in .json, .yml or .yaml.
// The fetcher's rules (plain http only on loopback) apply when it is downloaded.
func IsCatalogURL(src string) bool {
	u, err := url.Parse(src)
	if err != nil || u.Scheme != "https" && u.Scheme != "http" {
		return false
	}
	return FormatOf(u.Path) != ""
}

// Bind gives cat's relative entries the source of the marketplace whose pin is p (Plan 6c §4.3):
// its source, ref and commit, at the path joined to the catalog's root, under p.Root (the tree's
// place in the source, so the entry's --path reaches it there). A folder marketplace's
// entries are folder sources, unpinned; an archive's are pinned at install by their digest. A
// marketplace fetched as a single catalog file has no tree, so its relative entries are skipped,
// as Claude Code skips them; so are those whose directory the fetch did not find (p.Absent).
func Bind(cat Catalog, p Pin) Catalog {
	out := cat
	out.Entries, out.Skipped = []Entry{}, slices.Clone(cat.Skipped)
	kind := source.KindArchive
	switch {
	case source.IsFolderForm(p.Source):
		kind = source.KindFolder
	case source.IsGitForm(p.Source) && !IsCatalogURL(p.Source):
		kind = source.KindGit
	}
	for _, e := range cat.Entries {
		if e.Relative == "" {
			out.Entries = append(out.Entries, e)
			continue
		}
		dir := path.Join(Root(p.File), e.Relative)
		var s *skip
		switch {
		case IsCatalogURL(p.Source):
			s = skipped("relative source of a single-file marketplace", "a relative source needs the marketplace's files, and this marketplace is a single catalog file")
		case slices.Contains(p.Absent, dir):
			s = skipped("missing directory", "%s is not in the marketplace", untrusted.Text(dir, untrusted.MaxName))
		case absentComponent(e, dir, p.Absent) != nil:
			s = absentComponent(e, dir, p.Absent)
		default:
			if dir = path.Join(p.Root, dir); dir == "." {
				dir = ""
			}
			e.Source = manifest.Entry{Name: e.Name, Source: p.Source, Path: dir}
			if kind == source.KindGit {
				e.Source.Ref, e.Source.Commit = p.Ref, p.Commit
			}
			s = checked(&e, kind)
		}
		if s != nil {
			out.Skipped = append(out.Skipped, Skip{Name: e.Name, Reason: s.reason, Kind: s.kind})
			continue
		}
		out.Entries = append(out.Entries, e)
	}
	return out
}

// absentComponent is the skip of e, a relative entry at dir in the fetched tree, when a path its
// content lists select is among absent, the paths the fetch did not find as their kind needs.
func absentComponent(e Entry, dir string, absent []string) *skip {
	for _, c := range componentPaths(e) {
		if !slices.Contains(absent, path.Join(dir, c.path)) {
			continue
		}
		what := "a file or directory"
		if c.kind == "skills" {
			what = "a directory"
		}
		return skipped("missing component", "%s is not %s in the marketplace", untrusted.Text(c.path, untrusted.MaxName), what)
	}
	return nil
}

// componentPath is one path a relative entry's content lists select, with its list's name.
type componentPath struct{ kind, path string }

// componentPaths are the paths e's content lists select, in list order.
func componentPaths(e Entry) []componentPath {
	if e.Components == nil {
		return nil
	}
	var out []componentPath
	for _, l := range []struct {
		kind  string
		paths []string
	}{{"skills", e.Components.Skills}, {"commands", e.Components.Commands}, {"agents", e.Components.Agents}} {
		for _, p := range l.paths {
			out = append(out, componentPath{kind: l.kind, path: p})
		}
	}
	return out
}

// Selected maps each path, a slash path in the fetched tree, that a content list of a relative
// entry of cat, read from the catalog file at file, selects to its list's name ("skills",
// "commands" or "agents"): the engine checks each is there as that kind needs, a real directory
// for a skill, a real file or directory otherwise.
func Selected(cat Catalog, file string) map[string]string {
	out := map[string]string{}
	for _, e := range cat.Entries {
		dir := path.Join(Root(file), e.Relative)
		for _, c := range componentPaths(e) {
			out[path.Join(dir, c.path)] = c.kind
		}
	}
	return out
}

// RelativeDirs are the directories, slash paths in the fetched tree, that the relative entries of
// cat, read from the catalog file at file, name: the engine checks each is a real directory there.
func RelativeDirs(cat Catalog, file string) []string {
	var out []string
	for _, e := range cat.Entries {
		if e.Relative != "" {
			if dir := path.Join(Root(file), e.Relative); !slices.Contains(out, dir) {
				out = append(out, dir)
			}
		}
	}
	return out
}
