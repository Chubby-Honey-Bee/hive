// chb is HIVE's command: `go install github.com/Chubby-Honey-Bee/hive/cmd/chb@latest`
// names the binary after this directory. The command itself is the cli
// package in internal/cli (docs/naming.md rule 5).
package main

import "github.com/Chubby-Honey-Bee/hive/internal/cli"

// version is injected by the build (-X main.version=…); "dev" otherwise.
var version = "dev"

func main() {
	cli.Main(version)
}
