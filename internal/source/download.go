package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/trevor-vaughan/lola-go/internal/errs"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Download settings for archive URLs (design §5.1).
const (
	downloadTimeout = 2 * time.Minute // the whole request: about 2 MB/s for the largest body
	maxRedirects    = 5
)

// downloaded names what a download fetches, for its messages. coach says its refusals carry
// advice as hints: a marketplace's hints are commands only (Plan 6c §11.3), and no command
// fetches a catalog some other way, so a catalog's carry none. who, when set, names the stranger's
// file that named the URL (CheckRemote's who): every redirect must pass CheckRemote too (Plan 6c
// §10, ruling R-6c-13).
type downloaded struct {
	noun  string
	coach bool
	who   string
}

// What lola downloads: a module's archive (design §5.1), and a marketplace's catalog file (Plan
// 6c §6.2).
var (
	archiveDownload = downloaded{noun: "archive", coach: true}
	catalogDownload = downloaded{noun: "catalog"}
	// An archive a catalog entry names: a marketplace's hints are commands only.
	catalogArchiveDownload = downloaded{noun: "archive", who: CatalogEntry}
)

// CatalogEntry names a marketplace's catalog entry in CheckRemote's refusals.
const CatalogEntry = "a catalog entry"

// downloadHint coaches an archive download the server or the archive's size made fail.
const downloadHint = "download the archive yourself and install the file"

// hints is hs when w coaches, else none.
func (w downloaded) hints(hs ...string) []string {
	if !w.coach {
		return nil
	}
	return hs
}

// download saves the body of a GET of rawURL, what w names, to dest, refusing a body over limit
// bytes.
func download(ctx context.Context, rawURL, dest string, limit int64, w downloaded) (err error) {
	tr := http.DefaultTransport.(*http.Transport).Clone() // keeps the proxy settings
	tr.DisableCompression = true                          // a .tar.gz served as gzip stays gzip
	client := &http.Client{Transport: tr, Timeout: downloadTimeout, CheckRedirect: w.checkRedirect}
	shown := untrusted.Text(rawURL, untrusted.MaxName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", shown, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		hints := errs.Hints(err) // checkRedirect's
		switch {
		case timedOut(err):
			hints = w.hints("the server did not send the archive in time; " + downloadHint)
		case len(hints) == 0:
			hints = w.hints("check the URL and your network")
		}
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err // the *url.Error repeats the URL of the failing hop, query and all
		}
		return errs.WithHints(fmt.Errorf("downloading %s: %s", shown, untrusted.Text(err.Error(), untrusted.MaxName)), hints...)
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	if resp.StatusCode != http.StatusOK {
		return errs.WithHints(fmt.Errorf("downloading %s: the server answered %s", shown, untrusted.Text(resp.Status, untrusted.MaxName)),
			w.hints("check the URL, or "+downloadHint)...)
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, limit+1))
	if err := errors.Join(err, f.Close()); err != nil {
		err = fmt.Errorf("downloading %s: %w", shown, err)
		if timedOut(err) {
			return errs.WithHints(err, w.hints("the server did not send the archive in time; "+downloadHint)...)
		}
		return err
	}
	if n > limit {
		return errs.WithHints(fmt.Errorf("downloading %s: the %s is larger than %d bytes", shown, w.noun, limit), w.hints(downloadHint)...)
	}
	return nil
}

// timedOut reports whether err, or an error it wraps, is a timeout: the client's own, or the
// caller's deadline.
func timedOut(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// checkRedirect follows at most maxRedirects redirects, and never one from https to
// anything else, nor to plain http off loopback; for a URL a stranger's file named (who), never
// one CheckRemote refuses. The target is named without its query, which may hold a secret.
func (w downloaded) checkRedirect(req *http.Request, via []*http.Request) error {
	if w.who != "" {
		to := (&url.URL{Scheme: req.URL.Scheme, Host: req.URL.Host, Path: req.URL.Path}).String()
		if err := CheckRemote(to, w.who); err != nil {
			return fmt.Errorf("refusing a redirect to %s: %s", untrusted.Text(to, untrusted.MaxName), err.Error())
		}
	}
	if len(via) > maxRedirects {
		return errs.WithHints(fmt.Errorf("stopped after %d redirects", maxRedirects), w.hints("use the archive's final URL, after its redirects")...)
	}
	if via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return errs.WithHints(errors.New("refusing a redirect from https to "+req.URL.Scheme), w.hints("use the archive's final https:// URL")...)
	}
	if req.URL.Scheme == "http" && !IsLoopback(req.URL.Hostname()) {
		return errs.WithHints(errors.New("refusing a redirect to plain http on a host that is not loopback"),
			w.hints("use the archive's final https:// URL")...)
	}
	return nil
}

// FetchFile downloads the file at rawURL, a marketplace's catalog (Plan 6c §6.2 step 1), into a
// new directory under stagingParent, refusing more than limit bytes, and returns its path. The
// URL follows the source URL rules (no credentials, query or fragment; plain http only on a
// loopback host); a redirect is followed at most five times, never from https to anything else
// nor to plain http off loopback, as for an archive.
func FetchFile(ctx context.Context, rawURL, stagingParent string, limit int64) (string, error) {
	u, err := parseURL(rawURL)
	if err != nil {
		return "", err
	}
	if err := checkURL(u, ""); err != nil {
		return "", err
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("%s is not an https:// URL", displayURL(u))
	}
	if err := os.MkdirAll(stagingParent, 0o700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(stagingParent, "file-*")
	if err != nil {
		return "", err
	}
	dest := filepath.Join(dir, "download")
	if err := download(ctx, rawURL, dest, limit, catalogDownload); err != nil {
		return "", errors.Join(err, os.RemoveAll(dir))
	}
	return dest, nil
}
