package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// gitTimeout covers one whole git fetch: ls-remote, clone or fetch, and the copy.
const gitTimeout = 5 * time.Minute

// scrubbedGitVars select or reconfigure a repository behind lola's back; when lola runs from
// a git hook they point at the user's own repository. GIT_CONFIG_KEY_* and GIT_CONFIG_VALUE_*
// go too.
var scrubbedGitVars = map[string]bool{"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true, "GIT_OBJECT_DIRECTORY": true,
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_COMMON_DIR": true, "GIT_NAMESPACE": true, "GIT_CEILING_DIRECTORIES": true,
	"GIT_CONFIG_PARAMETERS": true, "GIT_CONFIG_COUNT": true}

// shortSHARE is an abbreviated commit id, which --ref does not accept.
var shortSHARE = regexp.MustCompile(`^[0-9a-f]{4,39}$`)

// gitEnv is environ for a git command lola runs on a source (design §4): prompts and
// user-chosen protocols off, optional locks off, and the repository-selecting variables
// removed. Everything else, credential helpers and the ssh agent included, passes through.
func gitEnv(environ []string) []string {
	out := make([]string, 0, len(environ)+3)
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if scrubbedGitVars[k] || strings.HasPrefix(k, "GIT_CONFIG_KEY_") || strings.HasPrefix(k, "GIT_CONFIG_VALUE_") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GIT_TERMINAL_PROMPT=0", "GIT_PROTOCOL_FROM_USER=0", "GIT_OPTIONAL_LOCKS=0")
}

// GitCommand builds a git invocation over an UNTRUSTED repository, in dir when dir is set: a
// source, or the user's project, whose repository a clone may have configured. Its config is
// attacker-controlled, so everything that makes git run a configured program (hooks,
// fsmonitor, the ext transport, LFS filters) or write to the repository is switched off on the
// command line, which outranks repository config.
func GitCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	full := []string{"-c", "core.hooksPath=/dev/null", "-c", "protocol.ext.allow=never", "-c", "core.fsmonitor=false",
		"-c", "filter.lfs.smudge=", "-c", "filter.lfs.process=", "-c", "filter.lfs.required=false", "-c", "core.untrackedCache=false"}
	if dir != "" {
		full = append(full, "-C", dir)
	}
	cmd := exec.CommandContext(ctx, "git", append(full, args...)...)
	cmd.Env = gitEnv(os.Environ())
	cmd.WaitDelay = 5 * time.Second // do not wait forever on children that keep the output open
	return cmd
}

// InWorkTree reports whether dir or a parent holds .git, the way git finds a repository.
func InWorkTree(dir string) (bool, error) {
	for d := dir; ; d = filepath.Dir(d) {
		_, err := os.Lstat(filepath.Join(d, ".git"))
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		if filepath.Dir(d) == d {
			return false, nil
		}
	}
}

// headWriter keeps the first limit bytes written to it and discards the rest, while still
// reporting every write as complete, so a child (or a remote's sideband "remote:" lines on
// git's stderr) cannot grow lola's memory or block on a full pipe.
type headWriter struct {
	limit int
	buf   bytes.Buffer
}

func (w *headWriter) Write(p []byte) (int, error) {
	if room := w.limit - w.buf.Len(); room > 0 {
		w.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (w *headWriter) String() string { return w.buf.String() }

// runGit runs a git command and returns its stdout. A failure carries git's stderr, made
// safe to print by untrusted.Text, and coaching for the usual causes.
func runGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := GitCommand(ctx, dir, args...)
	stderr := &headWriter{limit: maxStderr + 1}
	cmd.Stderr = stderr
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return nil, errs.WithHints(errors.New("lola needs git for this source, and git is not on PATH"), "install git")
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("git %s did not finish in time", args[0]) // fetchGit coaches it by transport
	}
	msg := strings.TrimSpace(stderr.String())
	if msg != "" {
		err = fmt.Errorf("%w: %s", err, untrusted.Text(msg, maxStderr))
	}
	err = fmt.Errorf("git %s failed: %w", args[0], err)
	if strings.Contains(msg, "Authentication failed") || strings.Contains(msg, "Permission denied") ||
		strings.Contains(msg, "could not read Username") {
		return nil, errs.WithHints(err, "for https, configure a git credential helper (git help credentials)",
			"for ssh, load your key into ssh-agent (ssh-add)")
	}
	return nil, err
}

// fetchGit clones a git source into scratch at its ref, then copies the module root of what
// is committed there (design §4).
func fetchGit(ctx context.Context, spec Spec, scratch, stagingParent string) (Fetched, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	if err := checkout(ctx, spec, scratch); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			hint := "the server did not answer in time: check your network and the URL, then try again"
			if strings.HasPrefix(strings.ToLower(spec.Location), "ssh://") || !strings.Contains(spec.Location, "://") { // ssh:// or scp form
				hint = "ssh may be waiting for a passphrase or a host key: load your key into ssh-agent and add the host to known_hosts"
			}
			err = errs.WithHints(err, hint)
		}
		return Fetched{}, err
	}
	head, err := runGit(ctx, scratch, "rev-parse", "HEAD")
	if err != nil {
		return Fetched{}, err
	}
	root, name, err := folderRoot(scratch, spec.Path, repoName(spec.Location))
	if err != nil {
		return Fetched{}, err
	}
	dir, err := copyTree(ctx, root, stagingParent, listGit)
	if err != nil {
		return Fetched{}, err
	}
	return Fetched{Dir: dir, Name: name, Commit: strings.TrimSpace(string(head))}, nil
}

// checkout puts spec's ref of its repository into the empty directory scratch: the default
// branch, the one branch or tag named ref, or the full commit SHA ref. It never guesses.
func checkout(ctx context.Context, spec Spec, scratch string) error {
	url, ref, shown, shownRef := spec.Location, spec.Ref, untrusted.Text(spec.Location, untrusted.MaxName), untrusted.Text(spec.Ref, untrusted.MaxName)
	clone := []string{"clone", "-q", "--depth", "1", "--no-recurse-submodules"}
	if ref == "" {
		_, err := runGit(ctx, "", append(clone, "--", url, scratch)...)
		return err
	}
	out, err := runGit(ctx, "", "ls-remote", "--", url, "refs/heads/"+ref, "refs/tags/"+ref)
	if err != nil {
		return err
	}
	var branch, tag bool
	for line := range strings.Lines(string(out)) {
		_, name, _ := strings.Cut(strings.TrimSpace(line), "\t")
		branch = branch || name == "refs/heads/"+ref
		tag = tag || name == "refs/tags/"+ref
	}
	switch {
	case branch && tag:
		return errs.WithHints(fmt.Errorf("%s has both a branch and a tag named %s; lola does not guess which you mean", shown, shownRef),
			"pin the commit you want with --ref <full commit SHA>")
	case branch || tag:
		_, err := runGit(ctx, "", append(clone, "--branch="+ref, "--", url, scratch)...)
		return err
	case fullSHARE.MatchString(ref):
		format := "sha1"
		if len(ref) == 64 {
			format = "sha256"
		}
		if _, err := runGit(ctx, scratch, "init", "-q", "--object-format="+format); err != nil {
			return err
		}
		if _, err := runGit(ctx, scratch, "fetch", "-q", "--depth", "1", "--", url, ref); err != nil {
			return errs.WithHints(fmt.Errorf("%s did not serve commit %s; servers using git protocol v0 refuse a commit "+
				"that no branch or tag points to: %w", shown, shownRef, err), "use --ref with a branch or tag that contains the commit")
		}
		_, err := runGit(ctx, scratch, "checkout", "-q", "--detach", "FETCH_HEAD")
		return err
	}
	hints := []string{"list them: " + errs.ShellJoin([]string{"git", "ls-remote", "--heads", "--tags", shown})}
	if shortSHARE.MatchString(ref) {
		hints = append([]string{"use the full 40-character commit SHA"}, hints...)
	}
	return errs.WithHints(fmt.Errorf("%s has no branch or tag named %s", shown, shownRef), hints...)
}

// repoName is the fallback module name of a clone's root: the location's last path segment
// without .git.
func repoName(loc string) string {
	_, p, _ := strings.Cut(loc, ":") // scp form: host:path
	if _, rest, ok := strings.Cut(loc, "://"); ok {
		_, p, _ = strings.Cut(rest, "/")
	}
	return strings.TrimSuffix(path.Base(strings.TrimRight(p, "/")), ".git")
}
