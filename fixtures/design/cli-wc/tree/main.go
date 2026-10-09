// Command tool is a small text utility. What it does is defined by run.
package main

import "os"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
