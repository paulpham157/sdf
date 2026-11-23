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
// Every agent, Copilot CLI included, uses these two; there is no per-agent
// copy.

// hostNamePattern is an RFC 1123 host name: dot-separated labels of letters,
// digits, and inner hyphens.
var hostNamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// numericHostPattern is a host whose dot-separated labels are all digits.
// Such a host must be a valid IPv4 address: 999.1.1.1 or 127.1 is not a host
// name, and resolvers read the short forms differently.
var numericHostPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*\.?$`)

// ValidateEndpoint is checkEndpoint for callers outside the package (sdf
// init checks --host and the ports with it before --check probes anything
// or a git hook is installed). Unlike Run it fills in no defaults, so a port
// of 0 (--port 0) is refused rather than read as "use the default".
func ValidateEndpoint(ep *Endpoint) error { return checkEndpoint(ep) }

// checkEndpoint validates ep in place. Host is a host name, an IPv4 address,
// or an IPv6 address, bare or in brackets (the brackets are dropped, since
// URLs add them). Ports are 1-65535.
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
	if strings.Contains(inner, ":") {
		ip := net.ParseIP(inner)
		if ip == nil {
			if h, p, err := net.SplitHostPort(host); err == nil && h != "" && allDigits(p) {
				return "", fmt.Errorf("--host %q looks like host:port; pass the host alone (--host %s) and the port with --port %s", host, h, p)
			}
			return "", fmt.Errorf("--host %q is not a valid IPv6 address", host)
		}
		if v4 := embeddedIPv4(ip); v4 != nil {
			// ::ffff:127.0.0.1 (IPv4-mapped) and ::127.0.0.1 (the deprecated
			// IPv4-compatible form) are not the IPv6 loopback ::1: the first is
			// the IPv4 host, the second routes nowhere useful, and loopback
			// checks disagree about them. Ask for the address itself.
			return "", fmt.Errorf("--host %q is an IPv4 address written as IPv6; pass --host %s (or ::1 for IPv6 loopback)", host, v4)
		}
		return inner, nil
	}
	if bracketed {
		return "", fmt.Errorf("--host %q: only an IPv6 address goes in brackets", host)
	}
	if numericHostPattern.MatchString(host) && net.ParseIP(host) == nil {
		return "", fmt.Errorf("--host %q is all numbers but not a valid IPv4 address", host)
	}
	if net.ParseIP(host) != nil {
		return host, nil
	}
	// A fully qualified name may end in one dot (example.com.); it is kept
	// as given. The dot is inert in a URL, a shell word, TOML, and JS.
	name := strings.TrimSuffix(host, ".")
	if name != "" && len(name) <= 253 && hostNamePattern.MatchString(name) {
		return host, nil
	}
	return "", fmt.Errorf("--host %q is not a host name, IPv4 address, or IPv6 address", host)
}

// embeddedIPv4 returns the IPv4 address inside an IPv4-mapped
// (::ffff:a.b.c.d) or IPv4-compatible (::a.b.c.d) IPv6 address, or nil.
// :: and ::1 are not IPv4-compatible.
func embeddedIPv4(ip net.IP) net.IP {
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	ip16 := ip.To16()
	if ip16 == nil {
		return nil
	}
	for _, b := range ip16[:12] {
		if b != 0 {
			return nil
		}
	}
	if ip16[12] == 0 && ip16[13] == 0 && ip16[14] == 0 && ip16[15] <= 1 {
		return nil
	}
	return net.IPv4(ip16[12], ip16[13], ip16[14], ip16[15]).To4()
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// shQuote single-quotes value for a POSIX shell. An embedded single
// quote closes the quoting, is written as a backslash-escaped quote, and
// reopens it.
func shQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
