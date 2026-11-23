//go:build !unix

package setup

import "os"

func qwenKeepOwner(*os.File, os.FileInfo) {}
