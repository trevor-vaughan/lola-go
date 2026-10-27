package source

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// sharedSpace is 100.64.0.0/10, the shared address space of carrier-grade NAT (RFC 6598), where
// overlay networks such as Tailscale put their hosts.
var sharedSpace = netip.MustParsePrefix("100.64.0.0/10")

// IPv6 prefixes CheckRemote reads beyond netip's classification: IPv4-compatible addresses
// (::/96) and NAT64's well-known prefix (64:ff9b::/96, RFC 6052), which embed an IPv4 address in
// their last 4 bytes; NAT64's local-use prefix (64:ff9b:1::/48, RFC 8215), which is a network's
// own; and the deprecated site-local fec0::/10, which some networks still route internally.
var (
	compatible = netip.MustParsePrefix("::/96")
	nat64      = netip.MustParsePrefix("64:ff9b::/96")
	nat64Local = netip.MustParsePrefix("64:ff9b:1::/48")
	siteLocal  = netip.MustParsePrefix("fec0::/10")
)

// endsInNumber reports whether host's last label, a trailing dot aside, is a number in decimal or
// 0x hex (WHATWG URL's "ends in a number"): such a host is an IPv4 address, in some form.
func endsInNumber(host string) bool {
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	last := labels[len(labels)-1]
	if hex, ok := strings.CutPrefix(last, "0x"); ok {
		return strings.Trim(hex, "0123456789abcdef") == ""
	}
	return last != "" && strings.Trim(last, "0123456789") == ""
}

// RemoteError is CheckRemote's refusal: Reason says what is wrong with the source, and the
// message adds what to give instead (instead), since no command lola could hint would fix it.
type RemoteError struct {
	Reason  string
	instead string
}

func (e *RemoteError) Error() string { return e.Reason + e.instead }

// refuseRemote is CheckRemote's refusal for the reason format and args give.
func refuseRemote(format string, args ...any) error {
	return &RemoteError{Reason: fmt.Sprintf(format, args...),
		instead: "; use the repository's https:// or ssh:// URL (or user@host:path), with its host name"}
}

// CheckRemote refuses a source that a stranger's file may not make lola fetch (Plan 6b D4, rulings
// R-6b3b-6, R-6b3b-9; Plan 6c §10): who names the file's kind in the message ("a remote profile",
// "a catalog entry"). It reads src's text alone, never the disk (ruling R-6b3b-5), and refuses a
// path or file:// URL, which is on this machine; any scheme but https:// and ssh:// (git's
// user@host:path is ssh), plain http:// included; localhost and *.localhost (RFC 6761); a host
// given as an IP address that is loopback, link-local, unspecified, private or carrier-grade NAT
// shared space, which would make that fetch a request inside your network; and an IP address in
// any form but the canonical one, which git and ssh would still connect to. A host name is
// allowed, and never resolved.
func CheckRemote(src, who string) error {
	shown := untrusted.Text(src, untrusted.MaxName)
	if strings.Contains(src, "://") {
		if _, err := url.Parse(src); err != nil {
			return refuseRemote("%s is not a URL lola can read", shown)
		}
	}
	host := Host(src)
	if host == "" {
		return refuseRemote("%s is on this machine; %s names only git repositories and https archives", shown, who)
	}
	scheme, rest, _ := strings.Cut(host, "://")
	if scheme != "https" && scheme != "ssh" {
		// The reason already names the schemes: what to give instead adds only the rest.
		return &RemoteError{Reason: fmt.Sprintf("%s uses %s://; %s's sources must use https:// or ssh://", shown,
			untrusted.Text(scheme, untrusted.MaxName), who), instead: " (or user@host:path), with its host name"}
	}
	// rest is the host and maybe a port, lower-cased, an IPv6 address in brackets.
	name := rest
	if h, _, err := net.SplitHostPort(rest); err == nil {
		name = h
	}
	// Trailing dots name the same host (an absolute name): localhost. and localhost.. are localhost.
	name = strings.TrimRight(strings.Trim(name, "[]"), ".")
	if name == "localhost" || strings.HasSuffix(name, ".localhost") {
		return refuseRemote("%s names localhost, this machine (RFC 6761); %s reaches hosts by name only, never inside your network",
			shown, who)
	}
	// A host whose last label is a number is an IP address to git (curl) and ssh, whatever form
	// it takes (WHATWG's ends-in-a-number): 2130706433, 0x7f.1 and 127.1 are 127.0.0.1. Only the
	// canonical form is read here, so any other is refused rather than taken for a name.
	if !strings.Contains(name, ":") && !endsInNumber(name) {
		return nil // a host name, never resolved
	}
	addr, err := netip.ParseAddr(name)
	if err != nil || addr.Is4() && addr.String() != name {
		return refuseRemote("%s names the host %s, which git and ssh read as an IP address that lola cannot check in that form; "+
			"%s reaches hosts by name only", shown, untrusted.Text(name, untrusted.MaxName), who)
	}
	addr = addr.Unmap()
	// An IPv6 address that embeds an IPv4 one, IPv4-compatible (::/96, deprecated) or NAT64's
	// well-known prefix (64:ff9b::/96, RFC 6052), reaches that IPv4 address: judge it instead.
	if addr.Is6() && !addr.IsLoopback() && !addr.IsUnspecified() && (compatible.Contains(addr) || nat64.Contains(addr)) {
		b := addr.As16()
		addr = netip.AddrFrom4([4]byte(b[12:]))
	}
	kind := ""
	switch {
	case addr.IsLoopback():
		kind = "loopback"
	case addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast():
		kind = "link-local"
	case addr.IsUnspecified():
		kind = "unspecified"
	case addr.IsPrivate():
		kind = "private"
	case sharedSpace.Contains(addr):
		kind = "shared (carrier-grade NAT)"
	case nat64Local.Contains(addr):
		kind = "private"
	case siteLocal.Contains(addr):
		kind = "site-local"
	default:
		return nil
	}
	return refuseRemote("%s names the %s address %s; %s reaches hosts by name only, never inside your network",
		shown, kind, addr, who)
}
