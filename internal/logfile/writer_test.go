package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendExisting(t *testing.T) {
	p := filepath.Join(t.TempDir(), "loglens.log")
	if err := os.WriteFile(p, []byte("pre-existing-data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := New(p, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("new-line\n")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(b), "pre-existing-data") || !strings.Contains(string(b), "new-line") {
		t.Fatalf("should append to existing file, got %q", b)
	}
}

func TestRotateOnSize(t *testing.T) {
	p := filepath.Join(t.TempDir(), "loglens.log")
	w, err := New(p, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	line := func(i int) string { return fmt.Sprintf("line-%d-%s\n", i, strings.Repeat("x", 20)) }
	for i := 0; i < 5; i++ {
		if _, err := w.Write([]byte(line(i))); err != nil {
			t.Fatal(err)
		}
	}

	cur, _ := os.ReadFile(p)
	bak, _ := os.ReadFile(p + ".1")
	bak2, _ := os.ReadFile(p + ".2")
	// live holds only the newest write; .1 the previous chunk; .2 the oldest.
	if !strings.Contains(string(cur), "line-4") || strings.Contains(string(cur), "line-3") {
		t.Fatalf("live file should hold only the newest chunk, got %q", cur)
	}
	if !strings.Contains(string(bak), "line-3") || !strings.Contains(string(bak), "line-2") {
		t.Fatalf(".1 should hold the previous chunk, got %q", bak)
	}
	if !strings.Contains(string(bak2), "line-1") {
		t.Fatalf(".2 should hold the oldest chunk, got %q", bak2)
	}
}

func TestRotateKeepsOnlyKeepCopies(t *testing.T) {
	p := filepath.Join(t.TempDir(), "loglens.log")
	w, err := New(p, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	for i := 0; i < 10; i++ { // small budget forces many rotations
		if _, err := w.Write([]byte("0123456789abcdef\n")); err != nil { // 17 bytes
			t.Fatal(err)
		}
	}

	for _, n := range []int{1, 2, 3} {
		if _, err := os.Stat(fmt.Sprintf("%s.%d", p, n)); err != nil {
			t.Fatalf("expected backup .%d, err=%v", n, err)
		}
	}
	if _, err := os.Stat(fmt.Sprintf("%s.%d", p, 4)); err == nil {
		t.Fatalf("backup count exceeded DefaultKeep=3")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("live file must exist, err=%v", err)
	}
}