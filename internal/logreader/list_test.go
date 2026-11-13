package logreader

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestListFilesSortByDefaultPath(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, mod time.Time) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("b.log", time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC))
	write("a.log", time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	write("c.log", time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))

	out, err := ListFiles(dir, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("len = %d, want 3", len(out))
	}
	if out[0].Path != "a.log" && filepath.Base(out[0].Path) != "a.log" {
		t.Errorf("default sort should be by path; first = %s", filepath.Base(out[0].Path))
	}
	if filepath.Base(out[2].Path) != "c.log" {
		t.Errorf("last by path should be c.log, got %s", filepath.Base(out[2].Path))
	}
}

func TestListFilesSortByTimeNewestFirst(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, mod time.Time) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	write("old.log", time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC))
	write("new.log", time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	write("mid.log", time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))

	out, err := ListFiles(dir, ListOptions{SortByTime: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("len = %d, want 3", len(out))
	}
	if filepath.Base(out[0].Path) != "new.log" {
		t.Errorf("newest first should be new.log, got %s", filepath.Base(out[0].Path))
	}
	if filepath.Base(out[1].Path) != "mid.log" {
		t.Errorf("second should be mid.log, got %s", filepath.Base(out[1].Path))
	}
	if filepath.Base(out[2].Path) != "old.log" {
		t.Errorf("last should be old.log, got %s", filepath.Base(out[2].Path))
	}
}