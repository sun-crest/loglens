package logreader

import (
	"fmt"
	"os"
)

// maxRotationCount bounds how many numbered siblings we probe, so a lookup on
// a busy directory cannot scan unboundedly.
const maxRotationCount = 512

// RotationSet returns the files that form a rotation group for base, ordered
// oldest -> newest. It understands the common logrotate naming patterns:
//
//	base            (newest, current file)
//	base.gz
//	base.1, base.2, ...        (N larger = older)
//	base.1.gz, base.2.gz, ...
//
// A gz base is treated as a single-file set. Files that do not exist or are
// not regular are skipped. Callers must still whitelist-validate each returned
// path before opening it.
func RotationSet(base string) []string {
	if IsGzip(base) {
		if isRegular(base) {
			return []string{base}
		}
		return nil
	}

	var out []string
	// Numbered siblings, oldest first: probe from high N down to 1.
	for i := maxRotationCount; i >= 1; i-- {
		s := fmt.Sprintf("%s.%d", base, i)
		gz := s + ".gz"
		if isRegular(gz) {
			out = append(out, gz)
		}
		if isRegular(s) {
			out = append(out, s)
		}
	}
	if gz := base + ".gz"; isRegular(gz) {
		out = append(out, gz)
	}
	if isRegular(base) {
		out = append(out, base)
	}
	return out
}

func isRegular(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}
