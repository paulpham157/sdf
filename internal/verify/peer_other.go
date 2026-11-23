//go:build !linux

package verify

import (
	"errors"
	"net"
)

// Without Linux, a peer cannot be identified: every connection is refused,
// so a go test criterion cannot pass (and criteria cannot be isolated
// either; see isolate_other.go).
func peerPID(net.Conn) (int, error) { return 0, errors.New("peer identification needs Linux") }

func checkTestBinary(int, int, bool) error { return errors.New("peer identification needs Linux") }
