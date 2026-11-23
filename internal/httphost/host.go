// Package httphost decides which Host headers a loopback listener may answer.
//
// A page can publish a name, then point that name at 127.0.0.1. The browser
// sends the attacker's name in Host and treats the response as same-origin.
// localhost, 127.0.0.1, and ::1 are the names a local client uses. The port
// may be omitted; when it is present it has to be the port the listener
// bound. This is not authentication. A client that can open the socket can
// still send one of the allowed names.
package httphost

import (
	"net"
	"strconv"
	"strings"
)

// Allowed reports whether hostHeader may be served on bindPort.
// localhost, 127.0.0.1, and ::1 are always allowed. names adds the address
// the process was told to bind, so a non-loopback --ui-addr keeps working.
// An empty port is allowed. A port that is present must be bindPort.
func Allowed(hostHeader, bindPort string, names ...string) bool {
	host, port, ok := split(hostHeader)
	if !ok {
		return false
	}
	if port != "" && !samePort(port, bindPort) {
		return false
	}
	if loopbackName(host) {
		return true
	}
	for _, name := range names {
		if sameHost(host, name) {
			return true
		}
	}
	return false
}

// Loopback reports whether a listen address is loopback. Unspecified
// addresses (0.0.0.0, ::) are not loopback: the operator bound the port
// beyond localhost.
func Loopback(host string) bool {
	host = normalize(host)
	if host == "" {
		return false
	}
	if loopbackName(host) {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func split(header string) (host, port string, ok bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", "", false
	}
	if h, p, err := net.SplitHostPort(header); err == nil {
		h = normalize(h)
		if h == "" {
			return "", "", false
		}
		return h, p, true
	}
	if ip := net.ParseIP(strings.Trim(header, "[]")); ip != nil {
		return ip.String(), "", true
	}
	if strings.Contains(header, ":") {
		return "", "", false
	}
	return strings.ToLower(header), "", true
}

func normalize(host string) string {
	host = strings.TrimSpace(host)
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return strings.ToLower(host)
}

func loopbackName(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func sameHost(a, b string) bool {
	b = normalize(b)
	return b != "" && a == b
}

func samePort(got, want string) bool {
	g, err1 := strconv.Atoi(got)
	w, err2 := strconv.Atoi(want)
	if err1 != nil || err2 != nil {
		return false
	}
	return g == w && g >= 0
}
