package market

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/source"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// sshHostRE is a host name lola can allow: dot-separated ASCII labels (an internationalized name
// is given in its punycode form). Anything else (scheme, user, port, path, brackets) is not a name.
var sshHostRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// maxHostName is the longest DNS name.
const maxHostName = 253

// CheckSSHHost is host, lower-cased, if it can be allowed for catalog ssh sources (spec 2026-10-28
// D3, D4): a host name alone, never an IP address or localhost, which source.CheckRemote refuses
// for a catalog entry whatever you allow. The error says what is wrong.
func CheckSSHHost(host string) (string, error) {
	name := strings.ToLower(host)
	if !sshHostRE.MatchString(name) || len(name) > maxHostName {
		return "", fmt.Errorf("%s is not a host name; give the host name alone, without scheme, user, port, path or trailing dot (git.example.com)",
			untrusted.Text(host, untrusted.MaxName))
	}
	if err := source.CheckRemote("ssh://"+name, who); err != nil {
		if re, ok := errors.AsType[*source.RemoteError](err); ok {
			return "", errors.New(re.Reason)
		}
		return "", err
	}
	// CheckRemote refuses private and loopback addresses and any non-canonical numeric form but lets
	// a canonical public IPv4 address through as a name, so it is refused here.
	if _, err := netip.ParseAddr(name); err == nil {
		return "", fmt.Errorf("%s is an IP address; give the host's name", name)
	}
	return name, nil
}

// SSHHost is the allowlist key of an ssh source, loc: its host name lower-cased, without user,
// port, brackets or trailing dots. It is "" for a source that is not ssh.
func SSHHost(loc string) string {
	rest, ok := strings.CutPrefix(source.Host(loc), "ssh://")
	if !ok {
		return ""
	}
	if h, _, err := net.SplitHostPort(rest); err == nil {
		rest = h
	}
	return strings.TrimRight(strings.Trim(rest, "[]"), ".")
}

// NormalizeHosts is hosts sorted and without duplicates; never nil.
func NormalizeHosts(hosts []string) []string {
	out := slices.Clone(hosts)
	slices.Sort(out)
	return append([]string{}, slices.Compact(out)...)
}
