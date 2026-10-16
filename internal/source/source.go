// Package source parses source arguments (§7.2) and fetches module content into staging.
package source

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Kind is the type of a source.
type Kind string

// Source kinds.
const (
	KindFolder  Kind = "folder"  // a local directory
	KindArchive Kind = "archive" // a zip, tar or gzip tar file, local or at an http(s) URL
	KindGit     Kind = "git"     // a git repository at an https, ssh or scp-style URL
)

// AcceptedForms names the source forms this build understands; used in error hints.
const AcceptedForms = "a directory or archive (.zip, .tar, .tar.gz, .tgz) path or file:// URL, an https:// archive URL, " +
	"or a git URL (https://, ssh:// or user@host:path)"

// archiveSuffixes are the archive file name endings, matched case-insensitively.
var archiveSuffixes = []string{".zip", ".tar", ".tar.gz", ".tgz"}

// Spec is a parsed source. Location is an absolute path for folders and local archives, with
// symbolic links resolved when Parse made it, else a URL.
type Spec struct {
	Kind     Kind
	Location string
	Ref      string
	Path     string
}

// remoteHelperRE is git's <transport>::<address> syntax, which runs a git-remote-<transport>
// program. It is refused before a location is parsed as a URL.
var remoteHelperRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*::`)

// scpRE is git's scp-style [user@]host:path. The non-'/' after the colon keeps URLs and
// Windows drive paths out.
var scpRE = regexp.MustCompile(`^([A-Za-z0-9._-]+@)?[A-Za-z0-9.-]+:[^/:]`)

// scpCredentialsRE is scp form with a password: user:password@host:path. git would read
// "user" as the host and pass the rest, password included, to ssh as the path.
var scpCredentialsRE = regexp.MustCompile(`^[^@:/]+:[^@]*@[A-Za-z0-9.-]+:`)

// gitCredentialsHint coaches a git source that carries credentials, a query or a fragment.
const gitCredentialsHint = "remove the credentials, query or fragment; let a git credential helper or ssh-agent supply credentials " +
	"(git help credentials)"

// fullSHARE is a full sha1 or sha256 commit id.
var fullSHARE = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// schemeRE is the URL scheme grammar (RFC 3986 §3.1).
var schemeRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*$`)

// Parse classifies arg. ok is false when arg is not a source form at all (for example a
// bare module name); err is set when arg is a source form that is unusable or refused.
func Parse(arg, cwd string) (Spec, bool, error) {
	if err := checkLocation(arg); err != nil {
		return Spec{}, false, err
	}
	p, isURL := arg, strings.HasPrefix(arg, "file://")
	if isURL {
		u, err := parseURL(arg)
		if err != nil {
			return Spec{}, false, err
		}
		if err := checkURL(u, ""); err != nil {
			return Spec{}, false, err
		}
		if u.Host != "" && u.Host != "localhost" {
			return Spec{}, false, fmt.Errorf("%s: file URLs must not name a remote host", untrusted.Text(arg, untrusted.MaxName))
		}
		p = u.Path
	} else if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	fi, err := os.Stat(p)
	var spec Spec
	switch {
	case err == nil && fi.IsDir():
		spec = Spec{Kind: KindFolder}
	case err == nil && fi.Mode().IsRegular() && archiveSuffix(p) != "":
		spec = Spec{Kind: KindArchive}
	case err == nil:
		return Spec{}, false, errs.WithHints(fmt.Errorf("%s is a file, not a directory or an archive", untrusted.Text(arg, untrusted.MaxName)),
			"archive names end in "+archiveSuffixList())
	case isURL:
		return Spec{}, false, fmt.Errorf("%s: no such directory or archive", untrusted.Text(arg, untrusted.MaxName))
	case strings.Contains(arg, "://"):
		return parseRemote(arg)
	case scpRE.MatchString(arg):
		spec = Spec{Kind: KindGit, Location: arg}
	default:
		return Spec{}, false, nil
	}
	if spec.Kind != KindGit {
		// One form of a local location for every later comparison (Plan 6a §8).
		if spec.Location, err = filepath.EvalSymlinks(filepath.Clean(p)); err != nil {
			return Spec{}, false, fmt.Errorf("%s: %w", untrusted.Text(arg, untrusted.MaxName), untrusted.CleanErr(err))
		}
		if spec.Kind == KindArchive && archiveSuffix(spec.Location) != archiveSuffix(p) {
			// The suffix picks the extractor, so a link must not change it silently.
			return Spec{}, false, errs.WithHints(fmt.Errorf("%s is a link to %s, whose name does not end in the same archive suffix",
				untrusted.Text(arg, untrusted.MaxName), untrusted.Text(spec.Location, untrusted.MaxName)),
				errs.ShellJoin([]string{"lola", "install", untrusted.Text(spec.Location, untrusted.MaxName)}))
		}
	}
	return spec, true, validate(spec)
}

// SameLocation reports whether recorded, a location the cache or a manifest recorded for a
// source of kind, names the source at location, a Location Parse returned. A local folder or
// archive recorded before locations were canonicalized can name it through a symbolic link.
func SameLocation(kind Kind, recorded, location string) bool {
	if recorded == location {
		return true
	}
	if kind == KindGit || !filepath.IsAbs(recorded) {
		return false
	}
	canonical, err := filepath.EvalSymlinks(recorded)
	return err == nil && canonical == location
}

// parseRemote classifies a URL (§2 rows 4-6): an http(s) URL whose path ends in an archive
// suffix is an archive, any other http(s) or ssh URL is git.
func parseRemote(arg string) (Spec, bool, error) {
	u, err := parseURL(arg)
	if err != nil {
		return Spec{}, false, err
	}
	spec := Spec{Kind: KindGit, Location: arg}
	switch {
	case u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "ssh":
		return Spec{}, false, nil
	case u.Scheme != "ssh" && archiveSuffix(u.Path) != "":
		spec.Kind = KindArchive
	}
	return spec, true, validate(spec)
}

// parseURL parses raw. The error shows only raw's scheme: raw may hold a password or a query,
// and the bytes that make it unparseable can sit inside them. url.Parse's message is left out
// too, as it quotes raw.
func parseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errs.WithHints(fmt.Errorf("%s is not a valid URL", schemeOnly(raw, "the URL")),
			"check the URL for a typo: a port is a number, and % starts a two-digit hex escape such as %20")
	}
	return u, nil
}

// schemeOnly is raw's scheme and "://…" when raw starts with a valid scheme and "://", else
// fallback: text before a "://" that is not a scheme may be a password.
func schemeOnly(raw, fallback string) string {
	if scheme, _, ok := strings.Cut(raw, "://"); ok && schemeRE.MatchString(scheme) {
		return scheme + "://…"
	}
	return fallback
}

// displayURL is what may be shown of a parsed URL: never its user info, query or fragment.
// Without a scheme, the location is shown without the leading "://".
func displayURL(u *url.URL) string {
	if u.Scheme == "" {
		return untrusted.Text(u.Host+u.EscapedPath(), untrusted.MaxName)
	}
	return untrusted.Text(u.Scheme+"://"+u.Host+u.EscapedPath(), untrusted.MaxName)
}

// checkURL refuses what no source URL may carry (§2): a query or fragment, credentials, and
// plain http to anything but loopback. The location is written to lola.yml and the cache
// index, so a secret in it would be committed. A user name in an ssh URL is no secret and is
// allowed; a password never is. kind picks the hint.
func checkURL(u *url.URL, kind Kind) error {
	shown := displayURL(u)
	hint := "use a URL without credentials or query; for a private archive, download it and install the file"
	if kind == KindGit {
		hint = gitCredentialsHint
	}
	_, hasPassword := u.User.Password()
	switch {
	case u.User != nil && u.Scheme != "ssh", hasPassword:
		return errs.WithHints(fmt.Errorf("%s: a source URL must not carry credentials", shown), hint)
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return errs.WithHints(fmt.Errorf("%s: a source URL must not have a query string or fragment, which can carry secrets",
			shown), hint)
	case u.Scheme == "http" && !isLoopback(u.Hostname()):
		return errs.WithHints(fmt.Errorf("%s: plain http:// is allowed only for a loopback host", shown), "use https://")
	}
	return nil
}

// isLoopback reports whether host is literally localhost, an IPv4 address in 127.0.0.0/8 or
// ::1. Names are never resolved.
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && (a.Is4() && a.IsLoopback() || a == netip.IPv6Loopback())
}

// archiveSuffixList is archiveSuffixes for a hint: ".zip, .tar, .tar.gz or .tgz".
func archiveSuffixList() string {
	last := len(archiveSuffixes) - 1
	return strings.Join(archiveSuffixes[:last], ", ") + " or " + archiveSuffixes[last]
}

// archiveSuffix returns the archive suffix name ends in, lower-cased, or "".
func archiveSuffix(name string) string {
	lower := strings.ToLower(name)
	for _, s := range archiveSuffixes {
		if strings.HasSuffix(lower, s) {
			return s
		}
	}
	return ""
}

// validate refuses a Spec that must never reach a fetcher (design §2). Fetch calls it too:
// the engine builds Specs from lola.yml and the cache index, which may be someone else's.
func validate(spec Spec) error {
	if err := checkLocation(spec.Location); err != nil {
		return err
	}
	if spec.Kind == KindArchive && strings.HasPrefix(spec.Location, "//") {
		// A scheme-less URL ("//user:password@host/path"), never a path Parse produced: it is
		// not echoed, as its user info may hold a password.
		return errs.WithHints(errors.New("archive location starts with //, a URL without a scheme"),
			"give the archive's https:// URL or its absolute path")
	}
	if spec.Kind == KindGit {
		if err := validateGitLocation(spec.Location); err != nil {
			return err
		}
	} else if !filepath.IsAbs(spec.Location) {
		if spec.Kind != KindArchive {
			// A path is shown as given; a URL-shaped one may hold a password, so at most its scheme is.
			shown := untrusted.Text(spec.Location, untrusted.MaxName)
			if strings.Contains(spec.Location, "://") {
				shown = schemeOnly(spec.Location, "(not shown: it looks like a URL)")
			}
			return errs.WithHints(fmt.Errorf("%s source %s is not an absolute path", untrusted.Text(string(spec.Kind), untrusted.MaxName), shown),
				"give the absolute path of the "+untrusted.Text(string(spec.Kind), untrusted.MaxName))
		}
		u, err := parseURL(spec.Location)
		if err != nil {
			return err
		}
		if err := checkURL(u, spec.Kind); err != nil {
			return err
		}
		if u.Scheme != "https" && u.Scheme != "http" {
			return errs.WithHints(fmt.Errorf("archive %s is neither a file nor an https:// URL", displayURL(u)),
				"sources are "+AcceptedForms)
		}
	}
	if spec.Kind == KindArchive && archiveSuffix(spec.Location) == "" {
		return errs.WithHints(fmt.Errorf("archive %s does not end in an archive suffix", untrusted.Text(spec.Location, untrusted.MaxName)),
			"archive names end in "+archiveSuffixList())
	}
	if err := checkRef(spec.Ref); err != nil {
		return err
	}
	if spec.Kind == KindGit && spec.Ref != "" && !fullSHARE.MatchString(spec.Ref) {
		// A local check with no network access, so it needs no caller's deadline.
		if _, err := runGit(context.Background(), "", "check-ref-format", "--allow-onelevel", spec.Ref); err != nil {
			if len(errs.Hints(err)) > 0 { // git itself is missing
				return err
			}
			return errs.WithHints(fmt.Errorf("ref %q is not a valid branch or tag name", untrusted.Text(spec.Ref, untrusted.MaxName)),
				"--ref takes a branch name, a tag name or a full commit SHA")
		}
	}
	return nil
}

// validateGitLocation accepts an https, loopback http or ssh URL, or scp form, whose host
// and user name cannot be read as an ssh option. scp form never has a user name starting
// with '-': checkLocation refuses a location that starts with one.
func validateGitLocation(loc string) error {
	host, shown := "", untrusted.Text(loc, untrusted.MaxName)
	if !strings.Contains(loc, "://") && scpRE.MatchString(loc) {
		// The location is never echoed here: it is not a URL that can be shown without its secret.
		if scpCredentialsRE.MatchString(loc) {
			return errs.WithHints(errors.New("a git source must not carry credentials (user:password@host:path)"), gitCredentialsHint)
		}
		if strings.ContainsAny(loc, "?#") {
			return errs.WithHints(errors.New("a git source must not have a query string or fragment, which can carry secrets"),
				gitCredentialsHint)
		}
		hostPart, _, _ := strings.Cut(loc, ":")
		host = hostPart[strings.LastIndex(hostPart, "@")+1:]
	} else {
		u, err := parseURL(loc)
		if err != nil {
			return err
		}
		shown = displayURL(u)
		if u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "ssh" {
			return errs.WithHints(fmt.Errorf("git source %s is not an https://, ssh:// or user@host:path URL", shown),
				"sources are "+AcceptedForms)
		}
		if err := checkURL(u, KindGit); err != nil {
			return err
		}
		if strings.HasPrefix(u.User.Username(), "-") {
			// The user name is not shown: it is userinfo.
			return errs.WithHints(fmt.Errorf("git source %s: its user name starts with '-', which ssh would read as an option", shown),
				"give the user name ssh logs in as, such as ssh://git@host/path")
		}
		host = u.Hostname()
	}
	if host == "" || strings.HasPrefix(host, "-") {
		return fmt.Errorf("git source %s has no usable host", shown)
	}
	return nil
}

// checkLocation refuses an argument or location that a tool could read as an option, or that
// would make git run a remote helper.
func checkLocation(loc string) error {
	if strings.HasPrefix(loc, "-") {
		return errs.WithHints(fmt.Errorf("source %q starts with '-', which would be read as an option", untrusted.Text(loc, untrusted.MaxName)),
			"for a path, write "+errs.ShellJoin([]string{"./" + untrusted.Text(loc, untrusted.MaxName)}))
	}
	if remoteHelperRE.MatchString(loc) {
		return errs.WithHints(fmt.Errorf("source %q names a git remote helper (<transport>::<address>), which lola never runs",
			untrusted.Text(loc, untrusted.MaxName)), "sources are "+AcceptedForms)
	}
	if len(loc) >= 6 && strings.EqualFold(loc[:6], "git://") {
		// No location in the message: a git:// URL can carry a user name or a query.
		return errs.WithHints(errors.New("a git:// source is refused: the git:// protocol is unauthenticated and unencrypted"),
			"use the repository's https:// or ssh:// URL")
	}
	return nil
}
