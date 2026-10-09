package workflow

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	hive "github.com/Chubby-Honey-Bee/hive"
)

// ResolveFile finds the workflow file arg names and returns a path on disk:
// arg itself when it exists; else workflows/<arg> under the working
// directory; else, when arg is a plain file name or workflows/<name>,
// workflows/<name> beside the binary; else the shipped workflows/<name> the
// binary carries, written to a temporary file that cleanup removes, with
// shipped true. A workflow found nowhere is an error naming arg.
func ResolveFile(arg string) (path string, shipped bool, cleanup func(), err error) {
	noop := func() {}
	if found, ok := localWorkflow(arg); ok {
		return found, false, noop, nil
	}
	notFound := fmt.Errorf("workflow not found: %s", arg)
	name, ok := shippedName(arg)
	if !ok {
		return "", false, noop, notFound
	}
	if beside, ok := besideBinary(name); ok {
		return beside, false, noop, nil
	}
	return carriedCopy(name, notFound)
}

// localWorkflow is arg itself when it exists, else workflows/<arg> under the
// working directory when that exists.
func localWorkflow(arg string) (string, bool) {
	if fileExists(arg) {
		return arg, true
	}
	alt := filepath.Join("workflows", arg)
	return alt, fileExists(alt)
}

// besideBinary is workflows/<name> beside the binary, when it exists.
func besideBinary(name string) (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	beside := filepath.Join(filepath.Dir(exe), "workflows", name)
	return beside, fileExists(beside)
}

// carriedCopy writes the carried workflows/<name> to a temporary file and
// returns its path, shipped, and the cleanup that removes it. A name the
// binary does not carry is notFound.
func carriedCopy(name string, notFound error) (path string, shipped bool, cleanup func(), err error) {
	noop := func() {}
	data, err := fs.ReadFile(hive.Workflows, name)
	if err != nil {
		return "", false, noop, notFound
	}
	dir, err := os.MkdirTemp("", "chb-workflows-")
	if err != nil {
		return "", false, noop, err
	}
	path = filepath.Join(dir, name)
	if err = os.WriteFile(path, data, 0o644); err != nil {
		_ = os.RemoveAll(dir)
		return "", false, noop, err
	}
	return path, true, func() { _ = os.RemoveAll(dir) }, nil
}

// shippedName is the file name arg names in the carried workflows/: arg as
// <name> or workflows/<name>, with no other directory in it.
func shippedName(arg string) (string, bool) {
	name := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(arg)), "workflows/")
	if name == "." || strings.Contains(name, "/") || !fs.ValidPath(name) {
		return "", false
	}
	return name, true
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
