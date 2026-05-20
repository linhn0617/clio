package main

import (
	"fmt"
	"os"

	"github.com/linhn0617/clio/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "clio:", err)
		os.Exit(1)
	}
}
