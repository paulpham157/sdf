package main

import (
	"os"

	"github.com/paulpham157/sdf/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
