package foragers

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load reads the foragers under dir: LoadFS over the directory. A missing
// directory yields an empty slice (not nil) and no error, so callers can
// treat "no foragers" as a usage problem they surface themselves.
func Load(dir string) ([]Forager, error) {
	return LoadFS(os.DirFS(dir))
}

// LoadFS reads every *.md file at the root of fsys, parses the frontmatter,
// and returns the foragers sorted by Name. Files without valid frontmatter
// are skipped silently — the README.md and other non-forager markdowns
// in the tree are ignored. A tree that does not exist yields an empty
// slice and no error.
func LoadFS(fsys fs.FS) ([]Forager, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return emptyIfMissing(err)
	}
	out := loadEntries(fsys, entries)
	sortByName(out)
	return out, nil
}

// emptyIfMissing is LoadFS's result for a tree it cannot read: no foragers
// when the tree does not exist, else the error.
func emptyIfMissing(err error) ([]Forager, error) {
	if errors.Is(err, fs.ErrNotExist) {
		return []Forager{}, nil
	}
	return nil, fmt.Errorf("read foragers dir: %w", err)
}

// loadEntries parses the forager files among entries, in name order.
//
// Files are read in name order, so the first file wins a duplicate name.
// Keeping both let `--foragers <name>` pick the first and a preset the
// last. The key ignores case, as ByName does, so `name: Optimist` cannot
// load beside `name: optimist`.
func loadEntries(fsys fs.FS, entries []fs.DirEntry) []Forager {
	out := make([]Forager, 0, len(entries))
	seen := map[string]bool{}
	for _, e := range entries {
		w, ok := loadEntry(fsys, e)
		if !ok || seen[strings.ToLower(w.Name)] {
			continue
		}
		seen[strings.ToLower(w.Name)] = true
		out = append(out, w)
	}
	return out
}

// loadEntry parses one directory entry when it is a forager file.
func loadEntry(fsys fs.FS, e fs.DirEntry) (Forager, bool) {
	if !isForagerEntry(e) {
		return Forager{}, false
	}
	return parsedForager(fsys, e.Name())
}

// isForagerEntry reports whether an entry is a *.md file other than the
// README, which is documentation for humans, not a forager the swarm should
// run.
func isForagerEntry(e fs.DirEntry) bool {
	return !e.IsDir() && strings.HasSuffix(e.Name(), ".md") && !strings.EqualFold(e.Name(), "README.md")
}

// parsedForager parses and normalises one forager file. A malformed file,
// one with no name, and one whose archetype or bonds do not validate are
// all skipped: naming one is then an unknown forager. The soft failure
// keeps a malformed file from breaking `chb ask`; logging it would pollute
// the stdout of a command whose job is a clean answer.
func parsedForager(fsys fs.FS, name string) (Forager, bool) {
	w, err := parseForagerFile(fsys, name)
	if err != nil || w.Name == "" {
		return Forager{}, false
	}
	if err := w.NormalizeArchetype(); err != nil {
		return Forager{}, false
	}
	return w, true
}

// parseForagerFile reads one forager file from its tree and returns its
// parsed Forager. Frontmatter must be at the top, between the first two
// `---` markers.
func parseForagerFile(fsys fs.FS, name string) (Forager, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return Forager{}, err
	}
	defer f.Close()

	frontmatter, body, err := splitFrontmatter(f)
	if err != nil {
		return Forager{}, err
	}
	return decodeForager(name, frontmatter, body)
}

// splitFrontmatter reads a forager file into its frontmatter and its body.
func splitFrontmatter(r io.Reader) (string, string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1 MB max line for big personas

	s := &frontmatterSplit{}
	for sc.Scan() {
		if err := s.line(sc.Text()); err != nil {
			return "", "", err
		}
	}
	if err := sc.Err(); err != nil {
		return "", "", fmt.Errorf("scan: %w", err)
	}
	return s.result()
}

// The states of a frontmatterSplit.
const (
	awaitingFrontmatter = iota // waiting for the opening ---
	inFrontmatter              // between the two markers
	inBody                     // after the closing ---
)

// frontmatterSplit is splitFrontmatter's progress through the file.
type frontmatterSplit struct {
	state          int
	frontmatter    strings.Builder
	body           strings.Builder
	sawOpenMarker  bool
	sawCloseMarker bool
}

// line reads one line of the file.
func (s *frontmatterSplit) line(l string) error {
	switch s.state {
	case awaitingFrontmatter:
		return s.opening(l)
	case inFrontmatter:
		s.frontmatterLine(l)
	case inBody:
		s.body.WriteString(l)
		s.body.WriteByte('\n')
	}
	return nil
}

// opening reads the first line, which must open the frontmatter.
func (s *frontmatterSplit) opening(l string) error {
	if strings.TrimSpace(l) != "---" {
		// No leading frontmatter → not a forager file.
		return errors.New("no frontmatter")
	}
	s.state = inFrontmatter
	s.sawOpenMarker = true
	return nil
}

// frontmatterLine reads a line inside the frontmatter, or its closing
// marker.
func (s *frontmatterSplit) frontmatterLine(l string) {
	if strings.TrimSpace(l) == "---" {
		s.state = inBody
		s.sawCloseMarker = true
		return
	}
	s.frontmatter.WriteString(l)
	s.frontmatter.WriteByte('\n')
}

// result is the frontmatter and the body, once both markers were read.
func (s *frontmatterSplit) result() (string, string, error) {
	if !s.sawOpenMarker || !s.sawCloseMarker {
		return "", "", errors.New("incomplete frontmatter")
	}
	return s.frontmatter.String(), s.body.String(), nil
}

// decodeForager decodes a forager's frontmatter and attaches its body.
func decodeForager(name, frontmatter, body string) (Forager, error) {
	var w Forager
	if err := yaml.Unmarshal([]byte(frontmatter), &w); err != nil {
		return Forager{}, fmt.Errorf("parse frontmatter: %w", err)
	}
	w.OutputSchema, w.ContractErr = parseContract([]byte(frontmatter))
	w.CounterBias, w.CounterBiasWhenAbsent = parseCounterBias([]byte(frontmatter))
	w.Body = strings.TrimLeft(body, "\n")
	w.File = name
	return w, nil
}
