//go:build !unix

package setup

import "os"

func copilotKeepOwner(*os.File, string) {}

func copilotHardLinks(string) uint64 { return 0 }
