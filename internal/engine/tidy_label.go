package engine

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// envAssign is a NAME=value word, which never names a server.
var envAssign = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// serverLabel is the one thing human output says about what an MCP server runs (§11.1): the
// base name of its executable (an array command's first element; a string command's first word
// after any NAME=value words) unless that is NAME=value or starts with '-', or for a remote
// server the host of its URL; never arguments, environment, headers or any other part of a URL.
// A server with no such executable and no URL host has no label.
func serverLabel(value []byte) string {
	var v struct {
		Command any    `json:"command"`
		URL     string `json:"url"`
		HTTPURL string `json:"httpUrl"`
	}
	if json.Unmarshal(value, &v) != nil {
		return ""
	}
	var exe string
	switch c := v.Command.(type) {
	case string:
		// A shell-style string: leading NAME=value words set environment, the next names
		// what runs.
		for _, w := range strings.Fields(c) {
			if !envAssign.MatchString(w) {
				exe = w
				break
			}
		}
	case []any:
		// An argv: element 0 is what runs and every later element is an argument.
		if len(c) > 0 {
			exe, _ = c[0].(string)
		}
	}
	if exe != "" && !strings.HasPrefix(exe, "-") && !envAssign.MatchString(exe) {
		base := exe[strings.LastIndexAny(exe, `/\`)+1:]
		return untrusted.Text(base, untrusted.MaxName)
	}
	for _, raw := range []string{v.URL, v.HTTPURL} {
		u, err := url.Parse(raw)
		if err == nil && u.Scheme != "" && u.Host != "" && u.Hostname() != "" {
			return untrusted.Text(u.Hostname(), untrusted.MaxName)
		}
	}
	return ""
}
