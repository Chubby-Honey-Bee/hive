package runner

import (
	"sort"
	"testing"
)

func TestFSRecorder_NilSafe(t *testing.T) {
	var f *FileSystemRecorder
	f.mark("/path/x")
	if got := f.TouchedPaths(); got != nil {
		t.Errorf("TouchedPaths(nil) = %v; want nil", got)
	}
}

func TestFSRecorder_MarkAndTouchedPaths(t *testing.T) {
	f := NewFSRecorder()
	f.mark("/a")
	f.mark("/b")
	f.mark("/a") // dedup via map
	got := f.TouchedPaths()
	sort.Strings(got)
	want := []string{"/a", "/b"}
	if len(got) != len(want) {
		t.Fatalf("len = %d; want 2", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q; want %q", i, got[i], want[i])
		}
	}
}

func TestFSRecorder_Reset(t *testing.T) {
	f := NewFSRecorder()
	f.mark("/a")
	f.Reset()
	if got := f.TouchedPaths(); len(got) != 0 {
		t.Errorf("after Reset: TouchedPaths = %v; want empty", got)
	}
	// Recorder is still usable after Reset.
	f.mark("/b")
	if got := f.TouchedPaths(); len(got) != 1 || got[0] != "/b" {
		t.Errorf("after Reset+mark: %v; want [/b]", got)
	}
}
