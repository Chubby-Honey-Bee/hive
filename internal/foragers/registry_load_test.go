package foragers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d; want 0", len(got))
	}
}

func TestLoad_PicksUpMDFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wiz.md"), []byte(`---
name: wiz
title: Forager
---
Body here.
`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("len = %d; want 1", len(got))
	}
	if got[0].Name != "wiz" {
		t.Errorf("Name = %q; want wiz", got[0].Name)
	}
}

func TestLoad_SkipsREADME(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(`---
name: wiz
---
body
`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("README should be skipped; got %d", len(got))
	}
}

func TestLoad_SkipsNonMD(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "something.txt"), []byte("not a forager"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("non-md file picked up: %d", len(got))
	}
}

func TestLoad_SkipsMalformedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.md"), []byte(`no frontmatter here, just text`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("malformed file should be skipped; got %d", len(got))
	}
}

func TestLoad_SkipsEmptyName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "noname.md"), []byte(`---
title: anonymous
---
body
`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty-name forager should be skipped")
	}
}

func TestLoad_SortsByName(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"zforager", "alpha", "mid"} {
		if err := os.WriteFile(filepath.Join(dir, n+".md"), []byte(`---
name: `+n+`
---
body
`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d; want 3", len(got))
	}
	if got[0].Name != "alpha" || got[1].Name != "mid" || got[2].Name != "zforager" {
		t.Errorf("sort order: %v", []string{got[0].Name, got[1].Name, got[2].Name})
	}
}

func TestLoad_SubdirectoriesIgnored(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "nested.md"), []byte(`---
name: nested
---
body
`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("subdirectory forager picked up: %v", got)
	}
}
