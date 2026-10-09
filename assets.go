// Package hive carries the shipped foragers, agents and workflows, so a
// binary built from this module runs with none of the three folders beside
// it. Each loader reads a folder on disk first and these trees last; the
// order per tree is in docs/specs/swarm.md § Discovery, runner.md §
// Agent personas and workflow.md § Workflow file.
package hive

import (
	"embed"
	"io/fs"
)

//go:embed foragers agents workflows
var shipped embed.FS

// Foragers, Agents and Workflows are the shipped trees, each rooted at its
// folder: Foragers holds optimist.md, not foragers/optimist.md.
var (
	Foragers  = tree("foragers")
	Agents    = tree("agents")
	Workflows = tree("workflows")
)

func tree(dir string) fs.FS {
	f, err := fs.Sub(shipped, dir)
	if err != nil {
		panic(err)
	}
	return f
}
