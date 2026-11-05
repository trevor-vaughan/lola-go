package profile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/fsx"
	"github.com/trevor-vaughan/lola-go/internal/manifest"
	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
	"github.com/trevor-vaughan/lola-go/internal/yamldoc"
)

// The files of a remote profile's directory, <paths.RemoteProfiles>/<name>/ (Plan 6b §4.2), and
// the directory of a source that holds its profiles.
const (
	RemoteFile = "profile.yml" // the profile as fetched, byte for byte
	OriginFile = "source.json" // where it came from and its pin (Origin)
	SourceDir  = "profiles"    // a source's profiles: <root or --path>/profiles/<name>.yml
)

// OriginVersion is the source.json schema version this binary reads and writes.
const OriginVersion = 1

// maxOrigin bounds source.json, which is read whole.
const maxOrigin = 64 << 10

// Origin is a remote profile's source.json (Plan 6b §4.2): the source, ref and --path it was
// fetched from, the commit a git source resolved to and the digest of profile.yml. The user's
// consent at --add or --update covers exactly this pin (§8.2, §19 S1).
type Origin struct {
	Version int    `json:"version"`
	Source  string `json:"source"`
	Ref     string `json:"ref"`
	Path    string `json:"path"`
	Commit  string `json:"commit"`
	Digest  string `json:"digest"`
}

// Check refuses an origin lola would not have written: a newer version, a field that fails the
// recorded-source rules (source.Recorded.Check), a source on this machine (D4,
// source.CheckRemote), and a pin that is missing its digest, or its commit for a git source.
func (o Origin) Check() error {
	switch {
	case o.Version > OriginVersion:
		return errs.WithHints(fmt.Errorf("written by a newer lola (version %d)", o.Version), "upgrade lola")
	case o.Version != OriginVersion:
		return fmt.Errorf("version %d is not %d", o.Version, OriginVersion)
	}
	if err := (source.Recorded{Source: o.Source, Ref: o.Ref, Path: o.Path, Commit: o.Commit, Digest: o.Digest}).Check(); err != nil {
		return err
	}
	if err := source.CheckRemote(o.Source, RemoteWho); err != nil {
		return err
	}
	switch {
	case o.Digest == "":
		return errors.New("digest is missing")
	case source.IsGitForm(o.Source) && o.Commit == "":
		return errors.New("commit is missing")
	}
	return nil
}

// rewriteHint coaches a remote profile's file that lola wrote and that no longer reads; the engine
// adds the commands.
const rewriteHint = "lola wrote it when you added the profile; remove the profile and add it again"

// RemoteWho names a remote profile in source.CheckRemote's refusals.
const RemoteWho = "a remote profile"

// Digest is the digest a remote profile's pin records for its file's bytes b.
func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Remote is the remote profiles lola keeps in p.RemoteProfiles (Plan 6b §4.1), in name order,
// from the directory's listing alone: each directory whose name follows the module-name rules,
// with the count of entries whose names do not. A name starting with "." is a directory lola is
// writing or removing (§19 R11), never a profile, and is not counted; so is anything that is not
// a directory, a symbolic link included. A directory holding more than MaxFiles entries is
// refused, naming it; a missing one holds none.
func Remote(p paths.Paths) ([]Info, int, error) {
	dir := p.RemoteProfiles
	des, err := readDir(dir)
	if err != nil || des == nil {
		return []Info{}, 0, err
	}
	out := []Info{}
	invalid := 0
	for _, de := range des {
		name := de.Name()
		switch {
		case strings.HasPrefix(name, "."), !de.IsDir():
		case module.ValidateName("profile", name) != nil:
			invalid++
		default:
			out = append(out, Info{Name: name, Kind: KindRemote, Path: filepath.Join(dir, name, RemoteFile)})
		}
	}
	slices.SortFunc(out, func(a, b Info) int { return strings.Compare(a.Name, b.Name) })
	return out, invalid, nil
}

// LoadRemote reads the remote profile at path, <name>/profile.yml under paths.RemoteProfiles,
// once (Plan 6b §4.2, §6.1), with its origin: the directory must be a directory and the files
// regular files, none of them a symbolic link (§19 S3); source.json must check (Origin.Check);
// the file must hold the bytes whose digest it records, which the user consented to (§19 S1);
// and the profile must load as a remote one (Fetched's rules). Every refusal names the file.
func LoadRemote(path string) (*Profile, Origin, error) {
	dir := filepath.Dir(path)
	const hint = rewriteHint
	if fi, err := os.Lstat(dir); err != nil || fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, Origin{}, fmt.Errorf("%s: %w", path, fs.ErrNotExist)
		}
		return nil, Origin{}, errs.WithHints(errors.Join(fmt.Errorf("%s is not a directory lola wrote", untrusted.Text(dir, untrusted.MaxName)),
			untrusted.CleanErr(err)), hint)
	}
	o, err := ReadOrigin(dir)
	if err != nil {
		return nil, Origin{}, err
	}
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return nil, Origin{}, errs.WithHints(fmt.Errorf("%s is a symbolic link; lola wrote a file there", untrusted.Text(path, untrusted.MaxName)), hint)
	}
	d, b, err := yamldoc.ReadNoFollow(path, Version)
	switch {
	case err != nil:
		return nil, Origin{}, errs.WithHints(errors.New(err.Error()), hint)
	case b == nil:
		return nil, Origin{}, errs.WithHints(fmt.Errorf("%s is missing", untrusted.Text(path, untrusted.MaxName)), hint)
	case Digest(b) != o.Digest:
		return nil, Origin{}, errs.WithHints(fmt.Errorf("%s changed since you added it: its digest is not the one %s records",
			untrusted.Text(path, untrusted.MaxName), OriginFile), hint)
	}
	p, err := parse(path, d, b, hint, true)
	return p, o, err
}

// ReadOrigin reads source.json in dir, a remote profile's directory (Plan 6b §4.2): a regular
// file of at most 64 KiB, never a symbolic link, every key known, its fields checked
// (Origin.Check).
func ReadOrigin(dir string) (Origin, error) {
	path := filepath.Join(dir, OriginFile)
	shown := untrusted.Text(path, untrusted.MaxName)
	const hint = rewriteHint
	b, err := fsx.ReadOwn(path, maxOrigin)
	if err != nil {
		return Origin{}, errs.WithHints(fmt.Errorf("%s: %w", shown, untrusted.CleanErr(err)), hint)
	}
	var o Origin
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return Origin{}, errs.WithHints(fmt.Errorf("%s: %s", shown, untrusted.Text(err.Error(), 2*untrusted.MaxName)), hint)
	}
	if err := o.Check(); err != nil {
		return Origin{}, errs.WithHints(fmt.Errorf("%s: %w", shown, err), hint)
	}
	return o, nil
}

// FetchedNames is the profiles in dir, the profiles/ directory of a source lola fetched (Plan 6b
// §3.2 --add), in name order, with the count of files whose names are not valid profile names
// (§19 S7), as list reads a profiles directory. dir must be a real directory (§19 S3).
func FetchedNames(dir string) ([]string, int, error) {
	if err := realDir(dir); err != nil {
		return nil, 0, err
	}
	names, invalid, err := list(dir)
	if names == nil {
		names = []string{}
	}
	return names, invalid, err
}

// Fetched reads <name>.yml from dir, the profiles/ directory of a source lola fetched into
// staging (Plan 6b §4.2), as untrusted input: dir must be a real directory and the file a regular
// one, neither a symbolic link, before anything is read (§19 S3); the file loads as a remote
// profile, whose every entry is pinned and names a remote source (§19 S1, D4) and which may not
// extend another. Messages name it profiles/<name>.yml. It returns the bytes read, whose digest
// the pin records.
func Fetched(dir, name string) (*Profile, []byte, error) {
	if err := module.ValidateName("profile", name); err != nil {
		return nil, nil, err
	}
	if err := realDir(dir); err != nil {
		return nil, nil, err
	}
	rel := filepath.Join(SourceDir, name+".yml")
	shown := untrusted.Text(filepath.ToSlash(rel), untrusted.MaxName)
	path := filepath.Join(dir, name+".yml")
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil, errs.WithHints(fmt.Errorf("the source has no profile %s (%s)", untrusted.Text(name, untrusted.MaxName), shown),
			"see the profiles it has: leave out the name, on a terminal")
	case err != nil:
		return nil, nil, fmt.Errorf("%s: %w", shown, untrusted.CleanErr(err))
	case fi.Mode()&fs.ModeSymlink != 0:
		return nil, nil, fmt.Errorf("%s: symbolic links are not allowed in a profile's source; lola never follows links in a source", shown)
	case !fi.Mode().IsRegular():
		return nil, nil, fmt.Errorf("%s is not a regular file", shown)
	}
	const hint = "ask the profile's author to fix it; lola did not add it"
	d, b, err := yamldoc.ReadNoFollow(path, Version)
	if err != nil {
		return nil, nil, errs.WithHints(errors.New(strings.ReplaceAll(err.Error(), path, filepath.ToSlash(rel))), hint)
	}
	if b == nil {
		b = []byte{}
	}
	p, err := parse(filepath.ToSlash(rel), d, b, hint, true)
	if err != nil {
		return nil, nil, err
	}
	return p, b, nil
}

// realDir refuses dir, a fetched source's profiles/, unless it is a directory and not a symbolic
// link (§19 S3).
func realDir(dir string) error {
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return errs.WithHints(fmt.Errorf("the source has no %s/ directory", SourceDir),
			"a profile source holds profiles/<name>.yml at its root, or under --path")
	case err != nil:
		return fmt.Errorf("%s: %w", SourceDir, untrusted.CleanErr(err))
	case fi.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("%s: symbolic links are not allowed in a profile's source; lola never follows links in a source", SourceDir)
	case !fi.IsDir():
		return fmt.Errorf("%s is not a directory", SourceDir)
	}
	return nil
}

// checkRemote applies the rules of a remote profile's entries (Plan 6b §4.2, §19 S1, D4) to e:
// a remote source, and a pin, its commit for a git source and its digest.
func checkRemote(e manifest.Entry) error {
	if err := source.CheckRemote(e.Source, RemoteWho); err != nil {
		return err
	}
	switch {
	case e.Digest == "" || source.IsGitForm(e.Source) && e.Commit == "":
		what := "its digest"
		if source.IsGitForm(e.Source) {
			what = "its commit and digest"
		}
		return errs.WithHints(fmt.Errorf("it is not pinned; every entry of a remote profile carries %s", what),
			"pin it as project.yml pins it: "+strings.TrimPrefix(what, "its "))
	}
	return nil
}
