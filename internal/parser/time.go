// Package parser detects log structure: timestamps, formats and (in a later
// iteration) stack traces. LogLens relies on per-line timestamp extraction for
// time-sliced queries, so extraction is anchored at the start of each line.
package parser

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// timeLayouts are tried, in order, when parsing a candidate timestamp
// substring. Layouts with an explicit zone come first, then bare timestamps
// (resolved in time.Local), then date-only and time-only forms.
var timeLayouts = []string{
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05.999999999Z0700",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05Z0700",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05,999999999",
	"2006-01-02 15:04:05",
	"2006/01/02 15:04:05.999999999",
	"2006/01/02 15:04:05",
	"2006-01-02",
	"15:04:05.999999999",
	"15:04:05",
}

// timestampCandidate matches a leading timestamp. Optional forms:
//
//	2026-11-10T15:04:05.123Z / +08:00 / +0800 / no zone
//	2026-11-10 15:04:05,123   (Java log4j)
//	2026/11/10 15:04:05       (Go log output)
//	2026-11-10                (date only)
//	15:04:05                  (time only, resolved to "now")
var timestampCandidate = regexp.MustCompile(
	`^(\d{4}[\/-]\d{2}[\/-]\d{2}(?:[ T]\d{2}:\d{2}:\d{2}(?:[.,]\d{1,9})?(?:Z|[+-]\d{2}:?\d{2})?)?|\d{2}:\d{2}:\d{2}[.,]?\d{0,9})`,
)

// jsonTimeKeys are probed (in order) inside single-line JSON log records.
var jsonTimeKeys = []string{
	"@timestamp", "timestamp", "time", "ts", "datetime", "time_local",
}

// ParseFlexible resolves a user-supplied time string. It accepts:
//
//	RFC3339:           2026-11-10T15:04:05Z / 2026-11-10T15:04:05+08:00
//	space separated:   2026-11-10 15:04:05,123
//	date only:         2026-11-10        (midnight, local time)
//	time only:         15:04:05          (today, local time)
//	relative:          -2h / -90s / -1d  (before now)
//	now
//
// Bare timestamps resolve in local time; zoned timestamps keep their zone.
// Output is comparable with ExtractLineTime results.
func ParseFlexible(s string, now time.Time) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	lower := strings.ToLower(s)
	if lower == "now" {
		return now, true
	}
	if strings.HasPrefix(lower, "-") {
		if t, ok := parseRelative(lower, now); ok {
			return t, true
		}
	}
	return parseLayoutsWithNow(s, time.Local, now)
}

// ExtractLineTime attempts to extract a timestamp from the start of a decoded
// log line. JSON records are sniffed for common timestamp keys before the
// anchored regex matchers run. Bare timestamps resolve in local time.
func ExtractLineTime(line []byte, now time.Time) (time.Time, bool) {
	s := strings.TrimSpace(string(line))

	if strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
		if t, ok := extractJSONTime(s); ok {
			return t, true
		}
	}

	// Brackets appear in two common arrangements:
	//   [2026-11-10 15:04:05] DEBUG ...   (timestamp inside the bracket)
	//   [worker-3] 2026-11-10 15:04:05 ... (timestamp after the bracket)
	if strings.HasPrefix(s, "[") {
		if t, ok := tryCandidate(strings.TrimPrefix(s, "["), now); ok {
			return t, true
		}
		if i := strings.IndexByte(s, ']'); i >= 0 {
			if t, ok := tryCandidate(strings.TrimSpace(s[i+1:]), now); ok {
				return t, true
			}
		}
		return time.Time{}, false
	}
	return tryCandidate(s, now)
}

// tryCandidate matches a leading timestamp candidate and hands it to the
// layout list, fixing up time-only strings with the reference date.
func tryCandidate(s string, now time.Time) (time.Time, bool) {
	m := timestampCandidate.FindString(s)
	if m == "" {
		return time.Time{}, false
	}
	return parseLayoutsWithNow(m, time.Local, now)
}

// parseLayoutsWithNow tries each layout, then patches date holes produced by
// time-only layouts (Go gives year 0 for them) with the reference date.
func parseLayoutsWithNow(candidate string, loc *time.Location, now time.Time) (time.Time, bool) {
	for _, l := range timeLayouts {
		if t, err := time.ParseInLocation(l, candidate, loc); err == nil {
			if t.Year() == 0 {
				return time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), loc), true
			}
			return t, true
		}
	}
	return time.Time{}, false
}

// parseLayouts tries each layout against candidate in the given location.
func parseLayouts(candidate string, loc *time.Location) (time.Time, bool) {
	for _, l := range timeLayouts {
		if t, err := time.ParseInLocation(l, candidate, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// extractJSONTime lightly parses a single-line JSON object and probes the
// common timestamp keys. Values may be strings or unix seconds/millis.
func extractJSONTime(s string) (time.Time, bool) {
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return time.Time{}, false
	}
	for _, key := range jsonTimeKeys {
		raw, ok := v[key]
		if !ok {
			continue
		}
		switch t := raw.(type) {
		case string:
			if parsed, ok := parseLayouts(t, time.Local); ok {
				return parsed, true
			}
			if ts, ok := unixFromNumberString(t); ok {
				return ts, true
			}
		case float64:
			if ts, ok := unixFromNumber(t); ok {
				return ts, true
			}
		case json.Number:
			if f, err := t.Float64(); err == nil {
				if ts, ok := unixFromNumber(f); ok {
					return ts, true
				}
			}
		}
	}
	return time.Time{}, false
}

func parseRelative(s string, now time.Time) (time.Time, bool) {
	body := s[1:] // strip '-'
	if body == "" {
		return time.Time{}, false
	}
	mult := float64(0)
	switch body[len(body)-1] {
	case 'd':
		mult = float64(24 * time.Hour)
		body = body[:len(body)-1]
	case 'h':
		mult = float64(time.Hour)
		body = body[:len(body)-1]
	case 'm':
		mult = float64(time.Minute)
		body = body[:len(body)-1]
	case 's':
		mult = float64(time.Second)
		body = body[:len(body)-1]
	default:
		return time.Time{}, false
	}
	n, err := strconv.ParseFloat(body, 64)
	if err != nil {
		return time.Time{}, false
	}
	return now.Add(-time.Duration(n * mult)), true
}

// unixFromNumber interprets a float as unix seconds (or millis when it is too
// large for seconds).
func unixFromNumber(f float64) (time.Time, bool) {
	if f <= 0 {
		return time.Time{}, false
	}
	if f > 1e12 { // milliseconds
		return time.UnixMilli(int64(f)), true
	}
	return time.Unix(int64(f), 0), true
}

// unixFromNumberString handles stringified unix timestamps inside JSON logs.
func unixFromNumberString(s string) (time.Time, bool) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return time.Time{}, false
	}
	return unixFromNumber(f)
}
