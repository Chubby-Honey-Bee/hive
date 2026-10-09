package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// `chb validate --json` prints its summary as one JSON object on stdout, with
// the progress log on stderr, so a command node can read the counts and the
// paths; the exit status still says whether every assertion passed.
func TestValidateReport_JSON(t *testing.T) {
	dir := t.TempDir()
	logFile, err := os.Create(filepath.Join(dir, "self-validate.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	for _, failures := range [][]string{nil, {"guard full pipeline", "chb replay-behavior"}} {
		var stdout bytes.Buffer
		r := &validateRunner{
			workspaceDir: dir,
			dbPath:       filepath.Join(dir, "hive.db"),
			asJSON:       true,
			stdout:       &stdout,
			stderr:       io.Discard,
			logFile:      logFile,
			pass:         90,
			fail:         len(failures),
			warns:        1,
			failures:     failures,
		}
		err := r.report()
		if (err != nil) != (len(failures) > 0) {
			t.Fatalf("failures %q: report error %v, want an error iff an assertion failed", failures, err)
		}
		var got struct {
			Passed   int      `json:"passed"`
			Failed   int      `json:"failed"`
			Warnings int      `json:"warnings"`
			Failures []string `json:"failures"`
			Log      string   `json:"log"`
			DB       string   `json:"db"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatalf("stdout %q is not one JSON object: %v", stdout.String(), err)
		}
		wantFailures := failures
		if wantFailures == nil {
			wantFailures = []string{}
		}
		if got.Passed != r.pass || got.Failed != r.fail || got.Warnings != r.warns || !reflect.DeepEqual(got.Failures, wantFailures) {
			t.Fatalf("summary = %+v, want the runner's counts %d/%d/%d and failures %q", got, r.pass, r.fail, r.warns, wantFailures)
		}
		if got.Log != logFile.Name() || got.DB != r.dbPath {
			t.Fatalf("paths = %s, %s; want %s, %s", got.Log, got.DB, logFile.Name(), r.dbPath)
		}
	}
}
