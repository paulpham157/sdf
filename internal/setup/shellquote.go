package setup

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
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

// numberLabelPattern is a label that URL parsers and inet_aton read as a
// number: decimal, octal (leading 0), or hex (0x...). A host whose last
// label is one of these is parsed as an IPv4 address, so it must be plain
// dotted decimal: 127.1, 0x7f.1, 0x7f000001, and 0177.0.0.1 all reach
// 127.0.0.1 but would not be recognised as loopback by sdf's own checks.
var numberLabelPattern = regexp.MustCompile(`^([0-9]+|0[xX][0-9A-Fa-f]*)$`)

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
		if !bracketed {
			if hint := ipv6PortHint(host); hint != "" {
				return "", fmt.Errorf("--host %q %s", host, hint)
			}
		}
		ip := net.ParseIP(inner)
		if ip == nil {
			if h, p, err := net.SplitHostPort(host); err == nil && h != "" {
				return "", fmt.Errorf("--host %q looks like host:port; %s", host, portHint(h, p))
			}
			if h, p, ok := strings.Cut(host, ":"); ok && h != "" && !strings.Contains(p, ":") {
				return "", fmt.Errorf("--host %q looks like host:port; %s", host, portHint(h, p))
			}
			return "", fmt.Errorf("--host %q is not a valid IPv6 address", host)
		}
		if v4 := embeddedIPv4(ip); v4 != nil {
			if v4.IsUnspecified() {
				// ::ffff:0.0.0.0 would otherwise be told to pass 0.0.0.0,
				// which is refused in turn.
				return "", unspecifiedErr(host)
			}
			// ::ffff:127.0.0.1 (IPv4-mapped) and ::127.0.0.1 (the deprecated
			// IPv4-compatible form) are not the IPv6 loopback ::1: the first is
			// the IPv4 host, the second routes nowhere useful, and loopback
			// checks disagree about them. Ask for the address itself.
			return "", fmt.Errorf("--host %q is an IPv4 address written as IPv6; pass --host %s (or ::1 for IPv6 loopback)", host, v4)
		}
		if ip.IsUnspecified() {
			return "", unspecifiedErr(host)
		}
		return inner, nil
	}
	if bracketed {
		return "", fmt.Errorf("--host %q: only an IPv6 address goes in brackets", host)
	}
	name := strings.TrimSuffix(host, ".")
	labels := strings.Split(name, ".")
	if last := labels[len(labels)-1]; numberLabelPattern.MatchString(last) {
		// Ends in a number, so resolvers and URL parsers read it as IPv4.
		// Only plain dotted decimal is accepted (net.ParseIP also refuses
		// leading zeros, which inet_aton reads as octal).
		ip := net.ParseIP(host)
		if ip == nil || ip.To4() == nil {
			return "", fmt.Errorf("--host %q is all numbers but not a dotted-decimal IPv4 address (short, hex, and octal forms such as 127.1, 0x7f.0.0.1, or 0177.0.0.1 are refused); write it as four decimal numbers, like 127.0.0.1", host)
		}
		if ip.IsUnspecified() {
			return "", unspecifiedErr(host)
		}
		return host, nil
	}
	// localhost. (any case) is written as localhost: a trailing-dot name
	// skips /etc/hosts on many resolvers and goes to DNS, where it need not
	// be loopback, and the git hook and token checks treat only localhost,
	// 127.0.0.1, and ::1 as loopback. Every writer then gets the same host.
	if strings.EqualFold(name, "localhost") {
		return "localhost", nil
	}
	// Any other fully qualified name may end in one dot (example.com.); it
	// is kept as given, since the dot is meaningful (no search domains) and
	// inert in a URL, a shell word, TOML, and JS.
	if name != "" && len(name) <= 253 && hostNamePattern.MatchString(name) {
		return host, nil
	}
	return "", fmt.Errorf("--host %q is not a host name, IPv4 address, or IPv6 address", host)
}

// ipv6PortHint reports a bare IPv6 literal whose last group reads as a port
// (::1:4318, fe80::1:4318): everything before the last colon is itself an
// IPv6 address and the last group is decimal, 2 to 5 digits, 1-65535. Such
// a string is a valid address, but far more often a host:port typed without
// brackets, so it is refused with both readings; the bracketed form
// ([fe80::1:4318]) is accepted as the address it spells.
func ipv6PortHint(host string) string {
	i := strings.LastIndex(host, ":")
	prefix, port := host[:i], host[i+1:]
	if strings.Contains(prefix, ".") && net.ParseIP(prefix) != nil && port != "" && !strings.Contains(port, ".") {
		// ::ffff:127.0.0.1:4318: a dotted quad ends an IPv6 address, so
		// this can only be host:port.
		return "looks like host:port; " + portHint(prefix, port)
	}
	if len(port) < 2 || len(port) > 5 || !allDigits(port) || port[0] == '0' {
		return ""
	}
	if n, err := strconv.Atoi(port); err != nil || n > 65535 {
		return ""
	}
	if ip := net.ParseIP(prefix); ip == nil || ip.To4() != nil || strings.Contains(prefix, ".") {
		return ""
	}
	if isUnspecified(prefix) {
		return fmt.Sprintf("looks like the unspecified address and a port, and an agent cannot send to %s; pass %s", prefix, unspecifiedArgs(port))
	}
	if _, err := checkHost("[" + prefix + "]"); err != nil {
		return "looks like host:port; " + portHint(prefix, port)
	}
	// Offer the other reading only when init would take it (::1:4318 in
	// brackets is ::0.1.67.24, an IPv4-compatible address, refused).
	alt := ""
	if _, err := checkHost("[" + host + "]"); err == nil {
		alt = fmt.Sprintf(", or write the address in brackets (--host '[%s]') if :%s is part of it", host, port)
	}
	return fmt.Sprintf("looks like an IPv6 address and a port; for the port pass --host '[%s]' --port %s%s", prefix, port, alt)
}

// portHint is the advice for --host HOST:PORT. The host it suggests is
// checked with checkHost first, so it never names one init refuses: the
// unspecified address gets 127.0.0.1, an IPv4 address written as IPv6 gets
// the IPv4 address, and any other refused host gets no suggestion. The
// port is echoed only when --port would accept it.
func portHint(host, port string) string {
	if isUnspecified(host) {
		return "the host is the unspecified address, which an agent cannot send to; pass " + unspecifiedArgs(port)
	}
	bare := strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	candidate := bare
	if strings.Contains(bare, ":") {
		candidate = "[" + bare + "]"
	}
	if norm, err := checkHost(candidate); err == nil {
		if !strings.Contains(norm, ":") {
			candidate = norm // localhost. is written as localhost
		}
	} else {
		ip := net.ParseIP(bare)
		v4 := net.IP(nil)
		if ip != nil {
			v4 = embeddedIPv4(ip)
		}
		if v4 == nil {
			return fmt.Sprintf("the host part %q is refused too; pass a host name, IPv4 address, or IPv6 address with --host, %s", bare, portText(port))
		}
		candidate = v4.String()
	}
	if strings.Contains(candidate, ":") {
		candidate = "'" + candidate + "'"
	}
	return "pass " + hostPortArgs(candidate, port)
}

// validPort is a port --port accepts: plain decimal 1-65535.
func validPort(port string) bool {
	if port == "" || len(port) > 5 || !allDigits(port) || port[0] == '0' {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}

// hostPortArgs is --host HOST --port PORT, one argv to paste, or the host
// alone and what --port needs when PORT is not one --port accepts.
func hostPortArgs(host, port string) string {
	if validPort(port) {
		return "--host " + host + " --port " + port
	}
	return "--host " + host + ", " + portText(port)
}

func portText(port string) string {
	if validPort(port) {
		return "and --port " + port
	}
	return "and the port as a number in 1-65535 with --port"
}

func unspecifiedArgs(port string) string {
	if validPort(port) {
		return "--host 127.0.0.1 --port " + port + " (or --host ::1, or this machine's address)"
	}
	return "--host 127.0.0.1 (or --host ::1, or this machine's address), " + portText(port)
}

// isUnspecified reports whether host (bracketed or not) is 0.0.0.0, ::, or
// an IPv4-mapped or -compatible form of 0.0.0.0.
func isUnspecified(host string) bool {
	ip := net.ParseIP(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"))
	if ip == nil {
		return false
	}
	if ip.IsUnspecified() {
		return true
	}
	v4 := embeddedIPv4(ip)
	return v4 != nil && v4.IsUnspecified()
}

func unspecifiedErr(host string) error {
	return fmt.Errorf("--host %q is the unspecified address, which sdf serve can bind but an agent cannot send to; pass --host 127.0.0.1 (or --host ::1, or this machine's address)", host)
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
