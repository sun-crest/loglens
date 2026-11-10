package logreader

import (
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readAll(t *testing.T, r interface {
	Read([]byte) (int, error)
	Close() error
}, budget int64) ([]Line, int64, int64, error) {
	t.Helper()
	var lines []Line
	nl, nb, err := Iterate(context.Background(), r, budget, func(l Line) error {
		lines = append(lines, l)
		return nil
	})
	return lines, nl, nb, err
}

func TestIteratePlain(t *testing.T) {
	f := filepath.Join(t.TempDir(), "a.log")
	content := "line1\nline2\r\nline3\nno trailing newline"
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, err := OpenRead(f)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	lines, nl, _, err := readAll(t, rc, 0)
	if err != nil {
		t.Fatal(err)
	}
	if nl != 4 {
		t.Errorf("lines = %d, want 4", nl)
	}
	want := []string{"line1", "line2", "line3", "no trailing newline"}
	for i, w := range want {
		if got := string(lines[i].Raw); got != w {
			t.Errorf("line %d = %q, want %q", i, got, w)
		}
	}
}

func TestIterateGz(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "p.log")
	dst := filepath.Join(dir, "p.log.gz")
	content := "2026-11-10T12:00:00.000Z INFO one\n2026-11-10T12:00:01.000Z INFO two\n"
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	w := gzip.NewWriter(f)
	w.Write([]byte(content))
	w.Close()
	f.Close()

	rc, err := OpenRead(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	lines, nl, _, err := readAll(t, rc, 0)
	if err != nil {
		t.Fatal(err)
	}
	if nl != 2 || string(lines[1].Raw) != "2026-11-10T12:00:01.000Z INFO two" {
		t.Fatalf("gz read mismatch: %v", lines)
	}
}

func TestIterateBudget(t *testing.T) {
	f := filepath.Join(t.TempDir(), "b.log")
	if err := os.WriteFile(f, []byte("12345\n67890\nabcde\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, err := OpenRead(f)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	_, nb, err := Iterate(context.Background(), rc, 8, func(Line) error { return nil })
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("err = %v, want ErrBudgetExceeded (bytes %d)", err, nb)
	}
	if nb > 8+64<<10 { // allow slack for buffering
		t.Errorf("bytes account beyond sanity: %d", nb)
	}
	if nb < 6 {
		t.Errorf("bytes read = %d, want at least a chunk", nb)
	}
}

func TestIterateStopOnEmitError(t *testing.T) {
	f := filepath.Join(t.TempDir(), "s.log")
	if err := os.WriteFile(f, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, err := OpenRead(f)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	calls := 0
	_, _, err = Iterate(context.Background(), rc, 0, func(Line) error {
		calls++
		if calls == 2 {
			return errors.New("stop")
		}
		return nil
	})
	if err == nil || err.Error() != "stop" {
		t.Fatalf("err = %v", err)
	}
	if calls != 2 {
		t.Errorf("emit calls = %d, want 2", calls)
	}
}

func TestRotationSet(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "svc.log")
	os.WriteFile(base, []byte("new\n"), 0o644)
	os.WriteFile(base+".1", []byte("older\n"), 0o644)
	os.WriteFile(base+".2.gz", []byte("oldest\n"), 0o644)
	os.WriteFile(base+".3", []byte("regular but out of sequence\n"), 0o644)

	set := RotationSet(base)
	if len(set) != 4 {
		t.Fatalf("rotation set = %v, want 4 entries", set)
	}
	wantOrder := []string{".3", ".2.gz", ".1", ""}
	for i, suffix := range wantOrder {
		if !strings.HasSuffix(set[i], base+suffix) {
			t.Errorf("set[%d] = %q, want suffix %q", i, set[i], suffix)
		}
	}
	// The base file alone (newest) is last.
	if set[len(set)-1] != base {
		t.Errorf("newest should be last, got %v", set)
	}
}

func TestRotationSetGzSingle(t *testing.T) {
	dir := t.TempDir()
	onion := filepath.Join(dir, "x.log.2.gz")
	os.WriteFile(onion, []byte("gzip"), 0o644)
	set := RotationSet(onion)
	if len(set) != 1 || set[0] != onion {
		t.Fatalf("gz base rotation = %v, want [%q]", set, onion)
	}
}

func TestIsGzip(t *testing.T) {
	cases := map[string]bool{"a.log": false, "a.log.GZ": true, "a.gz": true, "a.log.3.gz": true}
	for p, want := range cases {
		if got := IsGzip(p); got != want {
			t.Errorf("IsGzip(%q) = %v, want %v", p, got, want)
		}
	}
}
