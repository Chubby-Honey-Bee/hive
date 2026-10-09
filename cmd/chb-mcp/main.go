// chb-mcp is HIVE's MCP server: `go install github.com/Chubby-Honey-Bee/hive/cmd/chb-mcp@latest`
// names the binary after this directory. The server itself is the mcp
// package in internal/mcp (docs/naming.md rule 5).
package main

import "github.com/Chubby-Honey-Bee/hive/internal/mcp"

// version is injected by the build (-X main.version=…); "dev" otherwise.
var version = "dev"

func main() {
	mcp.Main(version)
}
