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
	"slices"
	"strings"
	"time"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// gitTimeout covers one whole git fetch: ls-remote, clone or fetch, and the copy.
const gitTimeout = 5 * time.Minute

// scrubbedGitVars select or reconfigure a repository behind lola's back; when lola runs from
// a git hook they point at the user's own repository. They cover `git rev-parse
// --local-env-vars`, plus GIT_NAMESPACE and GIT_CEILING_DIRECTORIES. GIT_CONFIG_KEY_* and
// GIT_CONFIG_VALUE_* go too.
var scrubbedGitVars = map[string]bool{"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true, "GIT_OBJECT_DIRECTORY": true,
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_COMMON_DIR": true, "GIT_NAMESPACE": true, "GIT_CEILING_DIRECTORIES": true,
	"GIT_CONFIG_PARAMETERS": true, "GIT_CONFIG_COUNT": true, "GIT_CONFIG": true, "GIT_IMPLICIT_WORK_TREE": true,
	"GIT_GRAFT_FILE": true, "GIT_NO_REPLACE_OBJECTS": true, "GIT_REPLACE_REF_BASE": true, "GIT_PREFIX": true,
	"GIT_SHALLOW_FILE": true}

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
// command line, which outranks repository config. Line endings are never converted, so a
// checkout holds the bytes as committed on every machine (Plan 6a §5.2).
func GitCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	full := []string{"-c", "core.hooksPath=/dev/null", "-c", "protocol.ext.allow=never", "-c", "core.fsmonitor=false",
		"-c", "filter.lfs.smudge=", "-c", "filter.lfs.process=", "-c", "filter.lfs.required=false", "-c", "core.untrackedCache=false",
		"-c", "core.autocrlf=false"}
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
	wt, err := WorkTree(dir)
	return wt != "", err
}

// WorkTree is the nearest of dir and its parents that holds .git, the way git finds a
// repository, or "" when none does. It runs no git.
func WorkTree(dir string) (string, error) {
	for d := dir; ; d = filepath.Dir(d) {
		_, err := os.Lstat(filepath.Join(d, ".git"))
		if err == nil {
			return d, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if filepath.Dir(d) == d {
			return "", nil
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
	return runGitEnv(ctx, nil, dir, args...)
}

// runGitEnv is runGit with env added to the command's environment.
func runGitEnv(ctx context.Context, env []string, dir string, args ...string) ([]byte, error) {
	cmd := GitCommand(ctx, dir, args...)
	cmd.Env = append(cmd.Env, env...)
	stderr := &headWriter{limit: maxStderr + 1}
	cmd.Stderr = stderr
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return nil, errs.WithHints(errors.New("lola needs git for this source, and git is not on PATH"), "install git")
	}
	sub := args[0]
	if sub == "-c" && len(args) > 2 {
		sub = args[2] // after remoteArgs' config
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("git %s did not finish in time", sub) // fetchGit coaches it by transport
	}
	msg := strings.TrimSpace(stderr.String())
	if loc, ok := refusedRedirect(args, msg); ok {
		return nil, fmt.Errorf("%s redirects, and lola follows no redirect of a source a catalog names; the catalog must list its final URL",
			untrusted.Text(loc, untrusted.MaxName))
	}
	if denied := accessDenied(args, msg); denied != nil {
		return nil, denied
	}
	if msg != "" {
		err = fmt.Errorf("%w: %s", err, untrusted.Text(msg, maxStderr))
	}
	return nil, fmt.Errorf("git %s failed: %w", sub, err)
}

// AccessError is a repository git could not read for want of access: missing, private with no
// credentials git can use, or the credentials refused. It says which in lola's words, never with
// git's output, and its hints are commands; every source's fetch returns it as is.
type AccessError struct{ msg string }

func (e *AccessError) Error() string { return e.msg }

// notFoundRE is git's report of a repository its server says is not there, over https
// ("fatal: repository '<url>' not found"), and an ssh server's, as GitHub and GitLab word it
// ("ERROR: Repository not found.").
var notFoundRE = regexp.MustCompile(`(?i)\brepository\b.*\bnot found\b`)

// sshDeniedRE is ssh's report of a server that took none of the ways it offered to log in, which
// it lists: "Permission denied (publickey,password)".
var sshDeniedRE = regexp.MustCompile(`Permission denied \(([^)]*)\)`)

// accessDenied is the AccessError of a git command of args, which contacts the repository it names
// after "--", that failed with stderr; nil when stderr is no refusal of access. Only git's own
// "fatal:" lines and ssh's are read: a server's sideband ("remote:") is its to word, and could
// otherwise make any failure read as a missing repository. lola runs git with its prompts off,
// so a server asking for credentials git has none for fails as "could not read Username", and
// so does a missing repository on a server that hides which repositories exist. The hint is the
// same check in your own git, which may ask for credentials and keep them, following no redirect
// for a source a catalog named, as lola's own fetch does; there is none when the location cannot
// be printed whole.
func accessDenied(args []string, stderr string) error {
	k := slices.Index(args, "--")
	if k < 0 || k+1 >= len(args) {
		return nil
	}
	raw := args[k+1]
	var fatal, own []string // git's fatal lines, and every line but the server's sideband
	for line := range strings.Lines(stderr) {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "remote:"):
		case strings.HasPrefix(line, "fatal:"):
			fatal, own = append(fatal, line), append(own, line)
		default:
			own = append(own, line)
		}
	}
	has := func(lines []string, match func(string) bool) bool { return slices.ContainsFunc(lines, match) }
	loc := untrusted.Text(raw, untrusted.MaxName)
	var hints []string
	if len(raw) <= untrusted.MaxName && untrusted.IsPlain(raw) {
		check := []string{"git", "ls-remote", "--", raw}
		if len(args) > 1 && args[0] == "-c" && args[1] == "http.followRedirects=false" {
			check = append([]string{"git", "-c", "http.followRedirects=false"}, check[1:]...)
		}
		hints = []string{errs.ShellJoin(check)}
	}
	var denied *AccessError
	switch {
	case has(fatal, func(l string) bool { return strings.Contains(l, "could not read Username") }):
		denied = &AccessError{msg: loc + " was not found, or it is private and git has no credentials for it (lola never lets git ask for them)"}
	case has(fatal, notFoundRE.MatchString),
		has(own, notFoundRE.MatchString) && has(fatal, func(l string) bool { return strings.Contains(l, "Could not read from remote repository") }):
		denied = &AccessError{msg: loc + " was not found, or it is private and the credentials git has, if any, cannot read it"}
	case has(fatal, func(l string) bool { return strings.Contains(l, "Authentication failed") }):
		denied = &AccessError{msg: "git's credentials for " + loc + " were refused"}
	case has(own, sshDeniedRE.MatchString):
		denied = &AccessError{msg: "the ssh server for " + loc + " refused every key ssh offered, or ssh had none"}
		// ssh-add helps only a server that takes keys.
		if has(own, func(l string) bool {
			m := sshDeniedRE.FindStringSubmatch(l)
			return m != nil && strings.Contains(m[1], "publickey")
		}) {
			hints = append([]string{"ssh-add"}, hints...)
		}
	default:
		return nil
	}
	return errs.WithHints(denied, hints...)
}

// redirectRE is curl's report, through git, of a redirect it was told not to follow: any 3xx status.
var redirectRE = regexp.MustCompile(`returned error: 3[0-9][0-9]\b`)

// refusedRedirect is the location a git command of remoteArgs for a source a catalog named
// (http.followRedirects=false) failed on because the server redirected it, ok false otherwise.
func refusedRedirect(args []string, stderr string) (string, bool) {
	if len(args) < 2 || args[0] != "-c" || args[1] != "http.followRedirects=false" || !redirectRE.MatchString(stderr) {
		return "", false
	}
	if k := slices.Index(args, "--"); k >= 0 && k+1 < len(args) {
		return args[k+1], true
	}
	return "", false
}

// remoteArgs is args, a git command that contacts spec's repository, as lola runs it: for a
// source a stranger's catalog named, git follows no redirect, so a server cannot send the fetch
// to a host the address rule refuses (Plan 6c §10, ruling R-6c-13).
func remoteArgs(spec Spec, args ...string) []string {
	if spec.FromCatalog {
		return append([]string{"-c", "http.followRedirects=false"}, args...)
	}
	return args
}

// fetchGit clones a git source into scratch at its ref, then copies the module root of what
// is committed there (design §4).
func fetchGit(ctx context.Context, spec Spec, scratch, stagingParent string) (Fetched, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	execs, err := checkout(ctx, spec, scratch)
	if err != nil {
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
	dir, _, err := copyTree(ctx, root, stagingParent, listGit)
	if err != nil {
		return Fetched{}, err
	}
	rel, err := filepath.Rel(scratch, root)
	if err != nil {
		return Fetched{}, errors.Join(err, os.RemoveAll(dir))
	}
	return Fetched{Dir: dir, Name: name, Commit: strings.TrimSpace(string(head)), Executables: executablesUnder(execs, filepath.ToSlash(rel), dir),
		Root: fetchedRoot(rel)}, nil
}

// noConversion is $GIT_DIR/info/attributes for every checkout: it outranks the repository's
// .gitattributes, so no end-of-line conversion, filter program, ident expansion or encoding
// change applies, and a checkout holds the bytes as committed (Plan 6a §5.2).
const noConversion = "* -text -eol -filter -ident -working-tree-encoding\n"

// checkout puts spec's ref of its repository into the empty directory scratch: the default
// branch, the one branch or tag named ref, or the full commit SHA ref. It never guesses.
// Before anything is checked out it pins the attributes (noConversion) and reads the tree
// (checkTree); it returns the tree's executables.
func checkout(ctx context.Context, spec Spec, scratch string) ([]string, error) {
	if commit, ok := CommitRef(spec.Ref); ok {
		spec.Ref = commit
	}
	url, ref := spec.Location, spec.Ref
	clone := []string{"clone", "-q", "--depth", "1", "--no-recurse-submodules", "--no-checkout"}
	name, err := remoteRef(ctx, spec)
	switch {
	case err != nil:
		return nil, err
	case ref != "" && name == ref:
		return checkoutCommit(ctx, spec, scratch)
	case ref != "":
		clone = append(clone, "--branch="+ref)
	}
	if _, err := runGit(ctx, "", remoteArgs(spec, append(clone, "--", url, scratch)...)...); err != nil {
		return nil, err
	}
	execs, err := prepareCheckout(ctx, scratch, "HEAD", spec.Path)
	if err != nil {
		return nil, err
	}
	_, err = runGit(ctx, scratch, "reset", "-q", "--hard", "HEAD")
	return execs, err
}

// remoteRef names what spec's ref is on its repository: "" for the default branch (ref ""), ref
// itself for a full commit SHA, which is always that commit and never looked up as a branch or
// tag of that name (Plan 9 R-9-U10: a pin cannot be moved by pushing a branch named like it), or
// refs/heads/<ref> or refs/tags/<ref> for the one branch or tag of that name. It refuses a branch
// and a tag of one name, and any other ref. It never guesses.
func remoteRef(ctx context.Context, spec Spec) (string, error) {
	url, ref := spec.Location, spec.Ref
	if commit, ok := CommitRef(ref); ok {
		return commit, nil
	}
	if ref == "" {
		return "", nil
	}
	shown, shownRef := untrusted.Text(url, untrusted.MaxName), untrusted.Text(ref, untrusted.MaxName)
	out, err := runGit(ctx, "", remoteArgs(spec, "ls-remote", "--", url, "refs/heads/"+ref, "refs/tags/"+ref)...)
	if err != nil {
		return "", err
	}
	var branch, tag bool
	for line := range strings.Lines(string(out)) {
		_, name, _ := strings.Cut(strings.TrimSpace(line), "\t")
		branch = branch || name == "refs/heads/"+ref
		tag = tag || name == "refs/tags/"+ref
	}
	switch {
	case branch && tag:
		return "", errs.WithHints(fmt.Errorf("%s has both a branch and a tag named %s; lola does not guess which you mean", shown, shownRef),
			"pin the commit you want with --ref <full commit SHA>")
	case branch:
		return "refs/heads/" + ref, nil
	case tag:
		return "refs/tags/" + ref, nil
	}
	hints := []string{"list them: " + errs.ShellJoin([]string{"git", "ls-remote", "--heads", "--tags", shown})}
	if shortSHARE.MatchString(ref) {
		hints = append([]string{"use the full 40-character commit SHA"}, hints...)
	}
	return "", errs.WithHints(fmt.Errorf("%s has no branch or tag named %s", shown, shownRef), hints...)
}

// checkoutCommit fetches the one commit spec.Ref (a full SHA) into a new repository at scratch and
// checks it out at spec.Path, the module path, as for prepareCheckout. A checkout whose HEAD is
// any other commit is refused.
func checkoutCommit(ctx context.Context, spec Spec, scratch string) ([]string, error) {
	url, ref, sub := spec.Location, spec.Ref, spec.Path
	shown, shownRef := untrusted.Text(url, untrusted.MaxName), untrusted.Text(ref, untrusted.MaxName)
	format := "sha1"
	if len(ref) == 64 {
		format = "sha256"
	}
	if _, err := runGit(ctx, scratch, "init", "-q", "--object-format="+format); err != nil {
		return nil, err
	}
	_, err := runGit(ctx, scratch, remoteArgs(spec, "fetch", "-q", "--depth", "1", "--", url, ref)...)
	if errors.As(err, new(*AccessError)) {
		return nil, err
	}
	if err != nil {
		return nil, errs.WithHints(fmt.Errorf("%s did not serve commit %s; servers using git protocol v0 refuse a commit "+
			"that no branch or tag points to: %w", shown, shownRef, err), "use --ref with a branch or tag that contains the commit")
	}
	if err := tagObject(ctx, scratch, ref); err != nil {
		return nil, err
	}
	execs, err := prepareCheckout(ctx, scratch, "FETCH_HEAD", sub)
	if err != nil {
		return nil, err
	}
	if _, err := runGit(ctx, scratch, "checkout", "-q", "--detach", "FETCH_HEAD"); err != nil {
		return nil, err
	}
	head, err := runGit(ctx, scratch, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	if got := strings.TrimSpace(string(head)); got != ref {
		return nil, fmt.Errorf("%s served commit %s for %s; lola installs only the commit asked for", shown, untrusted.Text(got, untrusted.MaxName), shownRef)
	}
	return execs, nil
}

// prepareCheckout readies the repository at scratch for checking out rev: it writes
// noConversion as $GIT_DIR/info/attributes and returns checkTree's executables for the
// module path sub (the source's --path, "" for the whole tree).
func prepareCheckout(ctx context.Context, scratch, rev, sub string) ([]string, error) {
	info := filepath.Join(scratch, ".git", "info")
	if err := os.MkdirAll(info, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(info, "attributes"), []byte(noConversion), 0o600); err != nil {
		return nil, err
	}
	return checkTree(ctx, scratch, rev, sub)
}

// checkTree lists rev's tree. It refuses a symbolic link (mode 120000) inside the module path
// sub ("" or "." for the whole tree) or on the way to it, which a checkout on Windows without
// core.symlinks would write as a plain file the copy cannot tell from one; a link elsewhere in
// the repository never reaches the module and is allowed (Plan 6a §5.2). It returns
// the paths whose mode is 100755: the execute bits as committed, whatever the platform checks
// out. Gitlinks (160000) are skipped, as the copy skips them. A module path that is no
// directory of the tree, letter case included, is refused too: a case-insensitive file system
// would check out another case's directory as it, past the link check above.
func checkTree(ctx context.Context, repo, rev, sub string) ([]string, error) {
	out, err := runGit(ctx, repo, "ls-tree", "-r", "-z", "--full-tree", rev)
	if err != nil {
		return nil, err
	}
	mod := path.Clean(filepath.ToSlash(sub))
	var execs []string
	found, otherCase, exists := mod == "." || !filepath.IsLocal(sub), "", ""
	for _, rec := range strings.Split(string(out), "\x00") {
		meta, p, ok := strings.Cut(rec, "\t")
		if ok && !found {
			if strings.HasPrefix(p, mod+"/") {
				found = true
			} else if len(p) > len(mod) && p[len(mod)] == '/' && strings.EqualFold(p[:len(mod)], mod) {
				otherCase = p[:len(mod)]
			}
			// The deepest directory on the way to mod that the tree has, for MissingPathError.
			for dir := path.Dir(mod); dir != "." && len(dir) > len(exists); dir = path.Dir(dir) {
				if strings.HasPrefix(p, dir+"/") {
					exists = dir
					break
				}
			}
		}
		switch {
		case !ok:
		case strings.HasPrefix(meta, "120000 ") &&
			(mod == "." || p == mod || strings.HasPrefix(p, mod+"/") || strings.HasPrefix(mod, p+"/")):
			return nil, fmt.Errorf("%s: symbolic links are not allowed in module content; lola never follows links in a source",
				untrusted.Text(p, untrusted.MaxName))
		case strings.HasPrefix(meta, "100755 "):
			execs = append(execs, p)
		}
	}
	if !found {
		hint := pathHint
		if otherCase != "" {
			hint = "use its exact letter case: " + errs.ShellJoin([]string{"--path", untrusted.Text(otherCase, untrusted.MaxName)})
		}
		return nil, errs.WithHints(&MissingPathError{Path: sub, In: "source", Exists: exists}, hint)
	}
	return execs, nil
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

// tagObject refuses ref, fetched into the repository at scratch, when it names an annotated tag
// object rather than a commit (TagObjectError), which a checkout would peel to its commit.
func tagObject(ctx context.Context, scratch, ref string) error {
	kind, err := runGit(ctx, scratch, "cat-file", "-t", ref)
	if err != nil || strings.TrimSpace(string(kind)) != "tag" {
		return err
	}
	body, err := runGit(ctx, scratch, "cat-file", "tag", ref)
	if err != nil {
		return err
	}
	commit, err := runGit(ctx, scratch, "rev-parse", ref+"^{commit}")
	if err != nil {
		return err
	}
	name := ""
	for line := range strings.Lines(string(body)) {
		if t, ok := strings.CutPrefix(strings.TrimSpace(line), "tag "); ok {
			name = t
			break
		}
	}
	return &TagObjectError{Ref: ref, Tag: untrusted.Text(name, untrusted.MaxName), Commit: strings.TrimSpace(string(commit))}
}

// TagObjectError refuses a --ref that is the SHA of an annotated tag object, not a commit: Tag
// is the tag's name and Commit the commit it points to, which a run at that commit would fetch.
type TagObjectError struct{ Ref, Tag, Commit string }

func (e *TagObjectError) Error() string {
	return e.Ref + " is the annotated tag " + e.Tag + ", not a commit; it points to commit " + e.Commit
}
