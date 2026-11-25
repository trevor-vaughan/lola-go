package cli

import (
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/engine"
)

// checkWebURL refuses an address that is not a plain http(s) one: it must begin with http:// or
// https://, name a host, and hold only printable ASCII with no space, so that it is one argument
// to the browser's launcher and never an option, a file, another protocol or a line of control
// characters; it carries no userinfo, and its host is letters, digits, dots and hyphens, so no
// shell or quote character reaches the launcher inside the host. A catalog's text decides the
// address, so openBrowser and the picker both check it.
func checkWebURL(s string) error {
	if !strings.HasPrefix(s, "https://") && !strings.HasPrefix(s, "http://") {
		return errors.New("not an http(s) address")
	}
	for _, r := range s {
		if r <= ' ' || r > '~' {
			return errors.New("holds a space, a control or a non-ASCII character")
		}
	}
	u, err := url.Parse(s)
	switch {
	case err != nil || u.Hostname() == "":
		return errors.New("names no host")
	case u.User != nil:
		return errors.New("carries credentials")
	case !engine.PlainHost(u.Hostname()):
		return errors.New("has a host that is not a plain name")
	}
	return nil
}

// browserCommand is the command that opens address in the user's browser on goos: its name and
// arguments, address always the last one, a single argument. None goes through a shell.
func browserCommand(goos, address string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{address}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", address}
	default:
		return "xdg-open", []string{address}
	}
}

// openBrowser starts the user's browser on address, once checkWebURL accepts it, without waiting
// for it. Its input and output are the null device and, where detach can, it runs in a session of
// its own with no controlling terminal, so a text browser cannot take the picker's screen.
func openBrowser(address string) error {
	if err := checkWebURL(address); err != nil {
		return fmt.Errorf("refusing to open %q: %w", address, err)
	}
	name, args := browserCommand(runtime.GOOS, address)
	cmd := exec.Command(name, args...)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
