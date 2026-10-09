package cli

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// ─── chb ingest-findings (ingest-findings.py) ───────────────

func newIngestFindingsCmd() *cobra.Command {
	var findingsDir string
	cmd := &cobra.Command{
		Use:   "ingest-findings",
		Short: "Bulk-load JSON finding files from the findings/ directory beside the database",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Beside the database by default, so a per-project workspace
			// (HIVE_DB_PATH=workspace/<project>/hive.db) reads its
			// own findings/.
			return ingestFindingsDir(cmp.Or(findingsDir, filepath.Join(filepath.Dir(dbPath), "findings")))
		},
	}
	cmd.Flags().StringVar(&findingsDir, "dir", "", "directory of finding JSON files (default: findings/ beside the database)")
	return cmd
}

// ingestFindingsDir loads every .json file in dir: findings, gaps and
// followups, one object or an array of them per file. A directory that does
// not read holds no files to load.
func ingestFindingsDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Printf("No finding files found in %s\n", dir)
		return nil
	}
	var t findingFileTally
	for _, e := range entries {
		t.ingestFile(dir, e.Name())
	}
	fmt.Printf("Ingested %d items from %d files (%d errors)\n", t.total, t.files, t.errs)
	if t.errs > 0 {
		return fmt.Errorf("%d errors ingesting %s", t.errs, dir)
	}
	return nil
}

// findingFileTally counts the files ingest-findings read, the items it
// wrote, and the errors on the way.
type findingFileTally struct {
	total int
	errs  int
	files int
}

// ingestFile loads name in dir when it is a .json file.
func (t *findingFileTally) ingestFile(dir, name string) {
	if !strings.HasSuffix(name, ".json") {
		return
	}
	t.files++
	items, ok := t.readItems(dir, name)
	if !ok {
		return
	}
	for _, item := range items {
		t.record(name, ingestFindingItem(item))
	}
}

// readItems reads the items of a finding file, counting an error when it
// does not read or parse.
func (t *findingFileTally) readItems(dir, name string) ([]map[string]any, bool) {
	content, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		fmt.Printf("Error reading %s: %v\n", name, err)
		t.errs++
		return nil, false
	}
	items, err := parseFindingItems(content)
	if err != nil {
		fmt.Printf("Error parsing %s: %v\n", name, err)
		t.errs++
		return nil, false
	}
	return items, true
}

// record counts one item's write, printing the error of one that failed.
func (t *findingFileTally) record(name string, err error) {
	if err != nil {
		fmt.Printf("Error processing %s: %v\n", name, err)
		t.errs++
		return
	}
	t.total++
}

// parseFindingItems reads a finding file's JSON: an array of objects or a
// single object. A file that is neither returns the error of reading it as
// an array.
func parseFindingItems(content []byte) ([]map[string]any, error) {
	var items []map[string]any
	err := json.Unmarshal(content, &items)
	if err == nil {
		return items, nil
	}
	var single map[string]any
	if json.Unmarshal(content, &single) != nil {
		return nil, err
	}
	return []map[string]any{single}, nil
}

// ingestFindingItem writes one item by its _type, a finding when it names
// none, and refuses an unknown _type.
func ingestFindingItem(item map[string]any) error {
	itemType, _ := item["_type"].(string)
	itemType = cmp.Or(itemType, "finding")
	delete(item, "_type")
	switch itemType {
	case "finding":
		b, _ := json.Marshal(item)
		_, err := ingestFinding(b)
		return err
	case "gap":
		return ingestGap(item)
	case "followup":
		return ingestFollowup(item)
	}
	return fmt.Errorf("unknown _type %q (want finding, gap or followup)", itemType)
}
