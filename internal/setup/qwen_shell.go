package setup

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// qwenCheckEndpoint and qwenShQuote keep `sdf init qwen` safe on its own:
// the endpoint goes into settings.json and into export lines the user may
// paste into a shell. PR #195 adds checkEndpoint and shQuote in
// internal/setup/shellquote.go and PR #188 has copilotEndpoint and
// shellQuote; these can be unified once those land. The names are
// qwen-prefixed so the branches do not clash.

var qwenHostName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// qwenCheckEndpoint accepts an RFC 1123 host name, an IPv4 address, or an
// IPv6 address, bare or in brackets (the brackets are dropped, because
// base() adds them), and an HTTP port in 1-65535. Qwen Code only uses the
// HTTP port, so the gRPC port is not checked here.
func qwenCheckEndpoint(ep Endpoint) (Endpoint, error) {
	host := ep.Host
	bracketed := strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]")
	inner := host
	if bracketed {
		inner = host[1 : len(host)-1]
	}
	switch {
	case strings.Contains(inner, ":"):
		if ip := net.ParseIP(inner); ip == nil || ip.To4() != nil || strings.Contains(inner, "%") {
			return ep, fmt.Errorf("--host %q is not a valid IPv6 address", host)
		}
		host = inner
	case bracketed:
		return ep, fmt.Errorf("--host %q: only an IPv6 address goes in brackets", host)
	case net.ParseIP(host) != nil:
	case len(host) <= 253 && qwenHostName.MatchString(host):
	default:
		return ep, fmt.Errorf("--host %q is not a host name, IPv4 address, or IPv6 address", host)
	}
	if ep.HTTPPort < 1 || ep.HTTPPort > 65535 {
		return ep, fmt.Errorf("--port %d is not in 1-65535", ep.HTTPPort)
	}
	ep.Host = host
	return ep, nil
}

// qwenShQuote single-quotes value for a POSIX shell. An embedded single
// quote closes the quoting, is written as a backslash-escaped quote, and
// reopens it.
func qwenShQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
