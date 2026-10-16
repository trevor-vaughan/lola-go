package source

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Recorded field limits (Plan 6a §4.3).
const (
	maxRecordedSource = 4096
	maxRecordedRef    = 256
)

// digestRE is the form of a content digest as the cache computes it.
var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Recorded is a source as project.yml, user.yml or lock.json records it. Those files may be
// someone else's, so their fields are checked when the file is loaded, before any of them can
// reach git or a path (Plan 6a §4.3).
type Recorded struct {
	Source, Ref, Path, Commit, Digest string
}

// Check applies the load-time rules, which need neither the file system nor git: every field
// prints as it is (untrusted.IsPlain); the source is non-empty, at most 4096 bytes, and not
// something git or ssh reads as an option or a remote helper; the ref is at most 256 bytes and
// not an option; the path stays inside the source; commit and digest are empty or full, and a
// commit is recorded only for a git source. Each error names the field.
func (r Recorded) Check() error {
	for _, f := range []struct{ name, value string }{{"source", r.Source}, {"ref", r.Ref}, {"path", r.Path}, {"commit", r.Commit}, {"digest", r.Digest}} {
		if !untrusted.IsPlain(f.value) {
			return fmt.Errorf("%s holds control or invisible characters", f.name)
		}
	}
	switch {
	case r.Source == "":
		return errors.New("source is empty")
	case len(r.Source) > maxRecordedSource:
		return fmt.Errorf("source is longer than %d bytes", maxRecordedSource)
	}
	if err := checkLocation(r.Source); err != nil {
		return err
	}
	if len(r.Ref) > maxRecordedRef {
		return fmt.Errorf("ref is longer than %d bytes", maxRecordedRef)
	}
	if err := checkRef(r.Ref); err != nil {
		return err
	}
	if r.Path != "" && !filepath.IsLocal(filepath.FromSlash(r.Path)) {
		return fmt.Errorf("path %q must be a relative path inside the source", untrusted.Text(r.Path, untrusted.MaxName))
	}
	if r.Commit != "" && !fullSHARE.MatchString(r.Commit) {
		return fmt.Errorf("commit %q is not a full commit SHA (40 or 64 lower-case hex digits)", untrusted.Text(r.Commit, untrusted.MaxName))
	}
	if r.Commit != "" && !IsGitForm(r.Source) {
		return errors.New("commit is recorded only for a git source; an archive or a folder is pinned by its digest alone")
	}
	if r.Digest != "" && !digestRE.MatchString(r.Digest) {
		return fmt.Errorf("digest %q is not sha256: and 64 hex digits", untrusted.Text(r.Digest, untrusted.MaxName))
	}
	return nil
}

// IsFullCommit reports whether s is a full sha1 or sha256 commit id.
func IsFullCommit(s string) bool { return fullSHARE.MatchString(s) }

// Host is where a recorded location is fetched from: the lower-cased scheme and host (with its
// port) of a URL, "ssh://" and the host for git's scp form, and "" for a local path or file://
// URL. A location that does not parse as a URL is its own host, so it never matches another.
func Host(loc string) string {
	if !strings.Contains(loc, "://") {
		if !scpRE.MatchString(loc) {
			return ""
		}
		host, _, _ := strings.Cut(loc, ":")
		return "ssh://" + strings.ToLower(host[strings.LastIndex(host, "@")+1:])
	}
	u, err := url.Parse(loc)
	switch {
	case err != nil:
		return loc
	case strings.EqualFold(u.Scheme, "file"):
		return ""
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

// IsFolderForm reports whether loc, as a file records it, can only name a local folder: it is
// not a URL, not git's scp form, and has no archive suffix. Parse decides what it is on disk.
func IsFolderForm(loc string) bool {
	return !strings.Contains(loc, "://") && !scpRE.MatchString(loc) && archiveSuffix(loc) == ""
}

// IsGitForm reports whether loc, as a file records it, can name a git source the way Parse
// classifies one: an ssh URL, an http(s) URL whose path has no archive suffix, or git's scp
// form. A local path or a file URL is a folder or an archive.
func IsGitForm(loc string) bool {
	if !strings.Contains(loc, "://") {
		return scpRE.MatchString(loc)
	}
	u, err := url.Parse(loc)
	if err != nil {
		return false
	}
	return u.Scheme == "ssh" || (u.Scheme == "https" || u.Scheme == "http") && archiveSuffix(u.Path) == ""
}

// checkRef refuses a ref git would read as an option.
func checkRef(ref string) error {
	if strings.HasPrefix(ref, "-") {
		return errs.WithHints(fmt.Errorf("ref %q starts with '-'", untrusted.Text(ref, untrusted.MaxName)),
			"a branch, tag or commit never starts with '-'")
	}
	return nil
}
