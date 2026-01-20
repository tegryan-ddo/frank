package main

import (
	"os"

	"github.com/barff/frank/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
