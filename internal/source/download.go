package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/trevor-vaughan/lola-go/internal/errs"
)

// Download settings for archive URLs (design §5.1).
const (
	downloadTimeout = 2 * time.Minute // the whole request: about 2 MB/s for the largest body
	maxRedirects    = 5
)

// downloadHint coaches a download the server or the archive's size made fail.
const downloadHint = "download the archive yourself and install the file"

// download saves the body of a GET of rawURL to dest, refusing a body over limit bytes.
func download(ctx context.Context, rawURL, dest string, limit int64) (err error) {
	tr := http.DefaultTransport.(*http.Transport).Clone() // keeps the proxy settings
	tr.DisableCompression = true                          // a .tar.gz served as gzip stays gzip
	client := &http.Client{Transport: tr, Timeout: downloadTimeout, CheckRedirect: checkRedirect}
	shown := printable(rawURL, maxName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", shown, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		hints := errs.Hints(err) // checkRedirect's
		switch {
		case timedOut(err):
			hints = []string{"the server did not send the archive in time; " + downloadHint}
		case len(hints) == 0:
			hints = []string{"check the URL and your network"}
		}
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err // the *url.Error repeats the URL of the failing hop, query and all
		}
		return errs.WithHints(fmt.Errorf("downloading %s: %s", shown, printable(err.Error(), maxName)), hints...)
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	if resp.StatusCode != http.StatusOK {
		return errs.WithHints(fmt.Errorf("downloading %s: the server answered %s", shown, printable(resp.Status, maxName)),
			"check the URL, or "+downloadHint)
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, limit+1))
	if err := errors.Join(err, f.Close()); err != nil {
		err = fmt.Errorf("downloading %s: %w", shown, err)
		if timedOut(err) {
			return errs.WithHints(err, "the server did not send the archive in time; "+downloadHint)
		}
		return err
	}
	if n > limit {
		return errs.WithHints(fmt.Errorf("downloading %s: the archive is larger than %d bytes", shown, limit), downloadHint)
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
// anything else, nor to plain http off loopback.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return errs.WithHints(fmt.Errorf("stopped after %d redirects", maxRedirects), "use the archive's final URL, after its redirects")
	}
	if via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return errs.WithHints(errors.New("refusing a redirect from https to "+req.URL.Scheme), "use the archive's final https:// URL")
	}
	if req.URL.Scheme == "http" && !isLoopback(req.URL.Hostname()) {
		return errs.WithHints(errors.New("refusing a redirect to plain http on a host that is not loopback"),
			"use the archive's final https:// URL")
	}
	return nil
}
