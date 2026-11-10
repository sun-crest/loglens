package parser

import (
	"testing"
	"time"
)

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local)
	got, ok := ParseFlexible(s, now)
	if !ok {
		t.Fatalf("ParseFlexible(%q) failed", s)
	}
	return got
}

func TestParseFlexible(t *testing.T) {
	cases := []struct {
		in   string
		want time.Time
	}{
		{"2026-11-10T15:04:05Z", time.Date(2026, 11, 10, 15, 4, 5, 0, time.UTC)},
		{"2026-11-10T15:04:05.123Z", time.Date(2026, 11, 10, 15, 4, 5, 123e6, time.UTC)},
		{"2026-11-10T15:04:05+08:00", time.Date(2026, 11, 10, 7, 4, 5, 0, time.UTC)},
		{"2026-11-10 15:04:05", time.Date(2026, 11, 10, 15, 4, 5, 0, time.Local)},
		{"2026-11-10 15:04:05,123", time.Date(2026, 11, 10, 15, 4, 5, 123e6, time.Local)},
		{"2026/11/10 15:04:05", time.Date(2026, 11, 10, 15, 4, 5, 0, time.Local)},
		{"2026-11-10", time.Date(2026, 11, 10, 0, 0, 0, 0, time.Local)},
		{"15:04:05", time.Date(2026, 11, 10, 15, 4, 5, 0, time.Local)},
	}
	for _, c := range cases {
		got := mustParse(t, c.in)
		if !got.Equal(c.want) {
			t.Errorf("ParseFlexible(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseFlexibleRelativeAndNow(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local)
	got, ok := ParseFlexible("now", now)
	if !ok || !got.Equal(now) {
		t.Errorf("ParseFlexible(now) = %v, %v", got, ok)
	}
	got, ok = ParseFlexible("-2h", now)
	if !ok || !got.Equal(now.Add(-2*time.Hour)) {
		t.Errorf("ParseFlexible(-2h) = %v, %v", got, ok)
	}
	got, ok = ParseFlexible("-90s", now)
	if !ok || !got.Equal(now.Add(-90*time.Second)) {
		t.Errorf("ParseFlexible(-90s) = %v, %v", got, ok)
	}
	got, ok = ParseFlexible("-1.5h", now)
	if !ok || !got.Equal(now.Add(-90*time.Minute)) {
		t.Errorf("ParseFlexible(-1.5h) = %v, %v", got, ok)
	}
	for _, bad := range []string{"", "nonsense", "-ghost", "2026-13-99"} {
		if _, ok := ParseFlexible(bad, now); ok {
			t.Errorf("ParseFlexible(%q) unexpectedly parsed", bad)
		}
	}
}

func TestExtractLineTime(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local)
	d := func(y int, mo time.Month, day, h, m, s int, ns time.Duration, loc *time.Location) time.Time {
		return time.Date(y, mo, day, h, m, s, int(ns), loc)
	}
	cases := []struct {
		line string
		want time.Time
	}{
		{"2026-11-10T15:04:05.123Z INFO main started", d(2026, 11, 10, 15, 4, 5, 123e6, time.UTC)},
		{"2026-11-10T15:04:05Z ERROR boom", d(2026, 11, 10, 15, 4, 5, 0, time.UTC)},
		{"2026-11-10 15:04:05,123 [http-nio] ERROR at x.java:10", d(2026, 11, 10, 15, 4, 5, 123e6, time.Local)},
		{"2026/11/10 15:04:05 http: GET /", d(2026, 11, 10, 15, 4, 5, 0, time.Local)},
		{"[2026-11-10 15:04:05] DEBUG enter", d(2026, 11, 10, 15, 4, 5, 0, time.Local)},
		{"   2026-11-10 15:04:05,999 WARN slow", d(2026, 11, 10, 15, 4, 5, 999e6, time.Local)},
	}
	for _, c := range cases {
		got, ok := ExtractLineTime([]byte(c.line), now)
		if !ok {
			t.Errorf("ExtractLineTime(%q) failed", c.line)
			continue
		}
		if !got.UTC().Equal(c.want.UTC()) {
			t.Errorf("ExtractLineTime(%q) = %s absolute, want %s", c.line, got.UTC(), c.want.UTC())
		}
	}
}

func TestExtractLineTimeJSON(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local)
	lines := []string{
		`{"level":"error","@timestamp":"2026-11-10T15:04:05Z","msg":"kaboom"}`,
		`{"ts": 1766473445, "level": "info", "msg": "unix seconds"}`,
		`{"time": "2026-11-10T15:04:05Z"}`,
	}
	for _, line := range lines {
		if _, ok := ExtractLineTime([]byte(line), now); !ok {
			t.Errorf("JSON line %q: no time extracted", line)
		}
	}
	noTime := []string{
		"INFO plain line without timestamp",
		`{"level":"info"}`,
		"GET /health 200 5ms",
	}
	for _, line := range noTime {
		if _, ok := ExtractLineTime([]byte(line), now); ok {
			t.Errorf("line %q: unexpectedly extracted a time", line)
		}
	}
}

// TestExtractLineTimeNoFalsePositive verifies a line that merely contains a
// timestamp-looking token later in the line is not misread as a timestamp.
func TestExtractLineTimeNoFalsePositive(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local)
	line := "ERROR stock 2026-11-10 15:04:05 appears mid-line"
	got, ok := ExtractLineTime([]byte(line), now)
	if ok {
		t.Errorf("mid-line timestamp extracted: %v", got)
	}
}
