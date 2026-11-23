//go:build !unix

package setup

import "os"

func keepOwner(*os.File, string) {}

func hardLinks(string) uint64 { return 0 }
