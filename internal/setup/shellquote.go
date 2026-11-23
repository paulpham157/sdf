package setup

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// The --host and --port values end up inside files other programs run: a
// shell snippet the user sources (opencode.env), shell commands in agent hook
// settings (Claude Code, Codex), a TOML string (Codex config.toml), and
// JavaScript string literals (OpenCode plugin, Pi extension). checkEndpoint
// keeps them to characters that are inert in all of those, and shQuote
// quotes what is written for a shell. Git hooks already accept only a
// loopback host (hookLogsURL).
//
// PR #188 has the same checks as copilotEndpoint and shellQuote in
// internal/setup/copilot.go; they can be unified once both land.

// hostNamePattern is an RFC 1123 host name: dot-separated labels of letters,
// digits, and inner hyphens, optionally ending in the root dot of a fully
// qualified name (collector.lan.).
var hostNamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*\.?$`)

// numericHostPattern is a host whose labels are all digits. Such a host
// must be a valid dotted IPv4 address: 999.1.1.1 is not a name, and short
// forms such as 127.1 resolve differently between tools.
var numericHostPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*\.?$`)

// hostPortPattern is a host name or IPv4 address followed by :port, or a
// bracketed IPv6 address followed by :port.
var hostPortPattern = regexp.MustCompile(`^(\[[^\]]*\]|[^:\[\]]+):[0-9]+$`)

// CheckEndpoint validates --host, --port, and --grpc-port exactly as given:
// 0 is refused, not replaced by a default. The CLI calls it before it
// probes, prints, or writes anything, so an invalid value never reaches the
// terminal or a file raw. It returns ep with brackets dropped from an IPv6
// host.
func CheckEndpoint(ep Endpoint) (Endpoint, error) {
	err := checkEndpoint(&ep)
	return ep, err
}

// checkEndpoint validates ep in place. Host is a host name (a trailing root
// dot is allowed), an IPv4 address, or an IPv6 address, bare or in brackets
// (the brackets are dropped, since URLs add them). Ports are 1-65535.
// Messages quote the input with %q, so control characters and terminal
// escapes are shown escaped.
func checkEndpoint(ep *Endpoint) error {
	host, err := checkHost(ep.Host)
	if err != nil {
		return err
	}
	ep.Host = host
	if ep.HTTPPort < 1 || ep.HTTPPort > 65535 {
		return fmt.Errorf("--port %d is not in 1-65535", ep.HTTPPort)
	}
	if ep.GRPCPort < 1 || ep.GRPCPort > 65535 {
		return fmt.Errorf("--grpc-port %d is not in 1-65535", ep.GRPCPort)
	}
	return nil
}

func checkHost(host string) (string, error) {
	inner := host
	bracketed := strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]")
	if bracketed {
		inner = host[1 : len(host)-1]
	}
	if hostPortPattern.MatchString(host) {
		return "", fmt.Errorf("--host %q must not include a port; pass the port with --port", host)
	}
	if strings.Contains(inner, ":") {
		ip := net.ParseIP(inner)
		if ip == nil || ip.To4() != nil {
			return "", fmt.Errorf("--host %q is not a valid IPv6 address", host)
		}
		if isIPv4Compatible(ip) {
			return "", fmt.Errorf("--host %q is an IPv4-compatible IPv6 address, which is deprecated and not routed; use the IPv4 address", host)
		}
		return inner, nil
	}
	if bracketed {
		return "", fmt.Errorf("--host %q: only an IPv6 address goes in brackets", host)
	}
	if numericHostPattern.MatchString(host) {
		if ip := net.ParseIP(strings.TrimSuffix(host, ".")); ip != nil && ip.To4() != nil && !strings.HasSuffix(host, ".") {
			return host, nil
		}
		return "", fmt.Errorf("--host %q is all numbers but not a valid IPv4 address", host)
	}
	if net.ParseIP(host) != nil || (len(strings.TrimSuffix(host, ".")) <= 253 && hostNamePattern.MatchString(host)) {
		return host, nil
	}
	return "", fmt.Errorf("--host %q is not a host name, IPv4 address, or IPv6 address", host)
}

// isIPv4Compatible reports an RFC 4291 IPv4-compatible address (::a.b.c.d):
// the first 96 bits are zero, and it is neither :: nor ::1.
func isIPv4Compatible(ip net.IP) bool {
	ip16 := ip.To16()
	for _, b := range ip16[:12] {
		if b != 0 {
			return false
		}
	}
	return !ip.Equal(net.IPv6unspecified) && !ip.Equal(net.IPv6loopback)
}

// shQuote single-quotes value for a POSIX shell. An embedded single
// quote closes the quoting, is written as a backslash-escaped quote, and
// reopens it.
func shQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
