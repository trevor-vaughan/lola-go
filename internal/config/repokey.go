package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// repoKeyPrefix starts a projects key that names a project by its repository (RepoKey).
const repoKeyPrefix = "git:"

// repoKeyTimeout bounds the git commands RepoKey runs, which read only the work tree's config.
const repoKeyTimeout = 30 * time.Second

// defaultPorts are the URL schemes of a remote on another host, with each one's default port, which
// a key leaves out; any other remote (a local path, file://, a transport helper) has no host to
// name the repository by.
var defaultPorts = map[string]string{"https": "443", "http": "80", "ssh": "22", "git": "9418", "git+ssh": "22", "ssh+git": "22"}

// RepoKey is the projects key of selections.yaml for the project at root, a directory in a git
// work tree whose origin remote is on another host: git:<host>/<path>#<subdir>, the remote with
// its scheme, userinfo, default port, query, trailing / and .git dropped and its host lowercased, and
// root's directory below the work-tree top (. at the top). So every clone of one repository at
// one directory of it, and every spelling of its remote, is one key, and no credential the
// remote URL carries is ever written. ok is false, with no error, for a root that does not exist,
// outside a work tree, without git, with no origin, or with an origin that names no host or holds
// a character untrusted.Text would replace: the project keeps its path key (paths.ProjectKey). git
// builds each git command (source.GitCommand); the work tree's config is the user's project's, so
// untrusted. Any other failure is an error naming git's first line of stderr; for a repository git
// refuses as owned by someone else, its hint is the command that trusts it (safe.directory).
func RepoKey(root string, git func(ctx context.Context, dir string, args ...string) *exec.Cmd) (key string, ok bool, err error) {
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return "", false, nil // a project directory that is gone keeps its path key, as paths.ProjectKey does
	}
	wt, err := source.WorkTree(root)
	if err != nil {
		return "", false, fmt.Errorf("lola could not look for the git work tree of %s: %w", untrusted.Text(root, untrusted.MaxName), untrusted.CleanErr(err))
	}
	if wt == "" {
		return "", false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), repoKeyTimeout)
	defer cancel()
	prefix, err := git(ctx, root, "rev-parse", "--show-prefix").Output()
	if errors.Is(err, exec.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, gitError("the repository", root, wt, err)
	}
	remote, err := git(ctx, root, "config", "--get", "remote.origin.url").Output()
	if exit, isExit := errors.AsType[*exec.ExitError](err); isExit && exit.ExitCode() == 1 {
		return "", false, nil // no origin
	}
	if err != nil {
		return "", false, gitError("the origin remote", root, wt, err)
	}
	repo, ok := remoteRepo(strings.TrimSpace(string(remote)))
	if !ok {
		return "", false, nil
	}
	sub := strings.TrimSuffix(strings.TrimSpace(string(prefix)), "/")
	if sub == "" {
		sub = "."
	}
	key = repoKeyPrefix + repo + "#" + sub
	if !untrusted.IsPlain(key) {
		return "", false, nil
	}
	return key, true, nil
}

// gitError is the error of a git command that could not read what of the work tree wt, root's:
// the first line git wrote on stderr, safe to print. A repository git refuses as owned by someone
// else gets the command that trusts it as its hint.
func gitError(what, root, wt string, err error) error {
	reason := untrusted.CleanErr(err).Error()
	exit, isExit := errors.AsType[*exec.ExitError](err)
	if isExit && len(bytes.TrimSpace(exit.Stderr)) > 0 {
		first, _, _ := strings.Cut(strings.TrimSpace(string(exit.Stderr)), "\n")
		reason = untrusted.Text(strings.TrimSpace(first), untrusted.MaxName)
	}
	out := fmt.Errorf("git could not read %s of %s: %s", what, untrusted.Text(root, untrusted.MaxName), reason)
	if isExit && bytes.Contains(exit.Stderr, []byte("dubious ownership")) {
		return errs.WithHints(out, errs.ShellJoin([]string{"git", "config", "--global", "--add", "safe.directory", untrusted.Text(wt, untrusted.MaxName)}))
	}
	return out
}

// remoteRepo is <host>/<path> of a remote URL on another host (RepoKey), <host>:<port>/<path>
// for a port other than its scheme's default, an IPv6 host bracketed; ok is false for any other
// remote, and for one whose key would hold an @, which could be part of a credential. A URL has a
// scheme of defaultPorts; scp-style [user@]host:path has a colon before
// any slash, and a host longer than one letter (C:/… is a Windows path).
func remoteRepo(raw string) (string, bool) {
	var host, p string
	if scheme, _, found := strings.Cut(raw, "://"); found {
		port, known := defaultPorts[strings.ToLower(scheme)]
		if !known {
			return "", false
		}
		u, err := url.Parse(raw)
		if err != nil {
			return "", false
		}
		host, p = u.Hostname(), u.Path
		if strings.Contains(host, ":") {
			host = "[" + host + "]" // an IPv6 address, bracketed as in the URL
		}
		if u.Port() != "" && u.Port() != port {
			host += ":" + u.Port()
		}
	} else {
		before, after, found := strings.Cut(raw, ":")
		first, _, _ := strings.Cut(after, "/")
		if !found || strings.Contains(before, "/") || strings.HasPrefix(after, ":") || strings.Contains(first, "@") || strings.ContainsAny(before, "[]") {
			return "", false // a transport helper, user:token@host:path, or a bracketed host lola does not parse
		}
		if i := strings.LastIndex(before, "@"); i >= 0 {
			before = before[i+1:]
		}
		host, p = before, after
		if len(host) == 1 {
			return "", false
		}
	}
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	p = strings.Trim(path.Clean("/"+p), "/")
	repo := strings.ToLower(host) + "/" + p
	if host == "" || p == "" || strings.Contains(repo, "@") {
		return "", false // an @ left in the key could be the rest of a credential
	}
	return repo, true
}
