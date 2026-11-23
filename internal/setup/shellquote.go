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
// digits, and inner hyphens.
var hostNamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

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
		if ip := net.ParseIP(inner); ip != nil && ip.To4() == nil {
			return inner, nil
		}
		return "", fmt.Errorf("--host %q is not a valid IPv6 address", host)
	}
	if bracketed {
		return "", fmt.Errorf("--host %q: only an IPv6 address goes in brackets", host)
	}
	if net.ParseIP(host) != nil || (len(host) <= 253 && hostNamePattern.MatchString(host)) {
		return host, nil
	}
	return "", fmt.Errorf("--host %q is not a host name, IPv4 address, or IPv6 address", host)
}

// shQuote single-quotes value for a POSIX shell. An embedded single
// quote closes the quoting, is written as a backslash-escaped quote, and
// reopens it.
func shQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
