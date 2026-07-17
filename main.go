package main

import (
	"os"

	"github.com/concord-consortium/cc-data-cli/cmd"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(cmd.Execute(version))
}
