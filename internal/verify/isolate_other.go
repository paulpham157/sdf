//go:build !linux

package verify

import (
	"context"
	"errors"
	"os/exec"
)

func isolatedCommand(context.Context, string, []string, nsConfig) (*exec.Cmd, error) {
	return nil, errors.New("criterion isolation needs Linux namespaces")
}

func probeIsolation() (bool, string) {
	return false, "criterion isolation needs Linux namespaces"
}

func nsInitMain([]string) int { return 125 }

func launcherMain([]string) int { return 125 }
