package engine

import (
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/source"
)

// treeForms is, for the hosts whose address for a directory at a ref is well known, the part of it
// between the repository and the ref.
var treeForms = map[string]string{"github.com": "/tree/", "gitlab.com": "/-/tree/"}

// sshWebHosts are the hosts whose ssh remotes (git@host:owner/repo) have the same repository at
// https://host/owner/repo; for any other host an ssh remote has no known web page.
var sshWebHosts = []string{"github.com", "gitlab.com", "codeberg.org", "bitbucket.org"}

var hostRE = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)

// PlainHost reports whether host is one a web address may name: letters, digits, dots and hyphens.
// It is the one rule for both the addresses the engine builds and the ones the CLI opens.
func PlainHost(host string) bool { return hostRE.MatchString(host) }

// scpRemote is git's scp form, user@host:path, as source.Host reads it.
var scpRemote = regexp.MustCompile(`^(?:[A-Za-z0-9._-]+@)?([A-Za-z0-9.-]+):([^/:].*)$`)

// webURL is the http(s) address of the page a browser reads spec's material at: the repository of
// a git source at an http(s) URL as it is, or, for an ssh remote of a host in sshWebHosts, at
// https; with spec's path at its ref when the host's tree address is well known. It is "" for a
// folder, an archive, a file URL, an ssh remote of any other host and a location that does not
// parse. The address never carries credentials, a query or a fragment, and its ref and path are
// escaped.
func webURL(spec source.Spec) string {
	if spec.Kind != source.KindGit {
		return ""
	}
	var scheme, host, hostname, repo string
	viaSSH := false
	if strings.Contains(spec.Location, "://") {
		u, err := url.Parse(spec.Location)
		if err != nil {
			return ""
		}
		scheme, host, hostname, repo = u.Scheme, u.Host, u.Hostname(), u.Path
		if viaSSH = scheme == "ssh"; viaSSH {
			scheme, host = "https", hostname
		}
	} else if m := scpRemote.FindStringSubmatch(spec.Location); m != nil {
		scheme, host, hostname, repo, viaSSH = "https", m[1], m[1], m[2], true
	}
	if (scheme != "https" && scheme != "http") || hostname == "" || viaSSH && !slices.Contains(sshWebHosts, strings.ToLower(hostname)) {
		return ""
	}
	if !PlainHost(hostname) {
		return ""
	}
	repo = strings.TrimSuffix(strings.Trim(repo, "/"), ".git")
	if repo == "" {
		return ""
	}
	out := scheme + "://" + strings.ToLower(host) + "/" + escapePath(repo)
	if tree, ok := treeForms[strings.ToLower(hostname)]; ok && spec.Path != "" {
		ref := spec.Ref
		if ref == "" {
			ref = "HEAD"
		}
		out += tree + escapePath(ref) + "/" + escapePath(spec.Path)
	}
	return out
}

// escapePath is p with each of its slash-separated segments escaped for a URL path.
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}
