//go:build !linux

package verify

import (
	"errors"
	"net"
	"os"
)

// Without Linux, a peer cannot be identified: every connection is refused,
// so a go test criterion cannot pass (and criteria cannot be isolated
// either; see isolate_other.go).
func peerPID(net.Conn) (int, error) { return 0, errors.New("peer identification needs Linux") }

func checkLauncher(int, string, int, int, bool) (string, error) {
	return "", errors.New("peer identification needs Linux")
}

func socketPair() (net.Conn, *os.File, error) { return nil, nil, errors.New("needs Linux") }

func sendFD(net.Conn, *os.File) error { return errors.New("needs Linux") }
