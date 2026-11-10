// Package samplepath locates the hand-written OTLP JSON fixtures.
package samplepath

import (
	"path/filepath"
	"runtime"
)

// Dir is tests/samples, the hand-written OTLP JSON fixtures.
func Dir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return filepath.Join("tests", "samples")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "tests", "samples")
}
