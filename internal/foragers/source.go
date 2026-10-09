package foragers

import (
	"io/fs"
	"os"
	"slices"
	"strings"

	hive "github.com/Chubby-Honey-Bee/hive"
)

// Source is a forager tree: a directory on disk, or, when Dir is empty, the
// copy the binary carries.
type Source struct {
	FS  fs.FS
	Dir string
}

// Shipped reports whether the source is the copy the binary carries.
func (s Source) Shipped() bool { return s.Dir == "" }

// Load reads the source's foragers (LoadFS).
func (s Source) Load() ([]Forager, error) { return LoadFS(s.FS) }

// DirSource is the tree under dir, whatever it holds.
func DirSource(dir string) Source { return Source{FS: os.DirFS(dir), Dir: dir} }

// Resolve picks the forager tree. HIVE_FORAGERS_DIR, when set, is the
// tree, whatever it holds. Else the first of dirs holding a forager file (a
// *.md other than README.md) is. Else the copy the binary carries is.
func Resolve(dirs ...string) Source {
	if d := os.Getenv("HIVE_FORAGERS_DIR"); d != "" {
		return DirSource(d)
	}
	for _, d := range dirs {
		if hasForagerFile(d) {
			return DirSource(d)
		}
	}
	return Source{FS: hive.Foragers}
}

// hasForagerFile reports whether dir holds a forager-shaped markdown file
// (any *.md other than README.md). The real frontmatter parse happens in
// LoadFS; this is the pre-check Resolve picks a directory by.
func hasForagerFile(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(entries, isForagerShaped)
}

// isForagerShaped reports whether an entry is a markdown file, its suffix in
// any case, other than README.md.
func isForagerShaped(e fs.DirEntry) bool {
	name := e.Name()
	return !e.IsDir() && strings.HasSuffix(strings.ToLower(name), ".md") && !strings.EqualFold(name, "README.md")
}
