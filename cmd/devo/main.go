package main

import (
	"fmt"
	"os"

	"github.com/ForestMars/devo/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "devo:", err)
		os.Exit(1)
	}
}
