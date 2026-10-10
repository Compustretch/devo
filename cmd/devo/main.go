package main

import (
	"os"

	"github.com/Compustretch/devo/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
