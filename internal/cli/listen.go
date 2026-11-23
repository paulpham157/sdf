package cli

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"syscall"
)

// serveListenError turns a listen failure from sdf serve into a message that
// names the port that is taken and the flag that moves it. Other errors keep
// their text.
func serveListenError(err error, httpPort, grpcPort int, uiAddr string) string {
	if !errors.Is(err, syscall.EADDRINUSE) {
		return "sdf serve: " + err.Error()
	}
	addr := ""
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Addr != nil {
		addr = opErr.Addr.String()
	}
	_, port, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		return "sdf serve: " + err.Error() + ". Another sdf serve may be running; stop it or choose other ports with --port, --grpc-port, and --ui-addr."
	}
	_, uiPort, _ := net.SplitHostPort(uiAddr)
	var what, fix string
	switch port {
	case strconv.Itoa(httpPort):
		what = "OTLP/HTTP"
		fix = "choose another with --port PORT, and point agents at it with sdf init <agent> --port PORT"
	case strconv.Itoa(grpcPort):
		what = "OTLP/gRPC"
		fix = "choose another with --grpc-port PORT, and point agents at it with sdf init <agent> --grpc-port PORT"
	case uiPort:
		what = "UI"
		fix = "choose another with --ui-addr 127.0.0.1:PORT, or run without the UI with --no-ui"
	default:
		return "sdf serve: " + err.Error() + ". Another sdf serve may be running; stop it or choose other ports with --port, --grpc-port, and --ui-addr."
	}
	return fmt.Sprintf("sdf serve: %s port %s (%s) is already in use. Another sdf serve or OTLP collector may be running there. Stop it, or %s.", what, port, addr, fix)
}
