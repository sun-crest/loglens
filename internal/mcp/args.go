package mcp

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Lenient argument accessors. The MCP client sends a JSON object; numbers may
// arrive as json.Number or float64 depending on encoder, and lists may arrive
// as arrays or (occasionally) as a single scalar, so we accept both shapes.

func argString(args map[string]any, key string) (string, bool) {
	v, ok := args[key]
	if !ok {
		return "", false
	}
	switch t := v.(type) {
	case string:
		return t, true
	case json.Number:
		return t.String(), true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	default:
		return fmt.Sprint(t), true
	}
}

func argInt(args map[string]any, key string) (int, bool) {
	v, ok := args[key]
	if !ok {
		return 0, false
	}
	switch t := v.(type) {
	case float64:
		return int(t), t == float64(int(t))
	case int:
		return t, true
	case json.Number:
		n, err := t.Int64()
		return int(n), err == nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		return n, err == nil
	}
	return 0, false
}

func argBool(args map[string]any, key string) (bool, bool) {
	v, ok := args[key]
	if !ok {
		return false, false
	}
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "1", "yes", "on":
			return true, true
		case "false", "0", "no", "off":
			return false, true
		}
	}
	return false, false
}

// argStringList accepts either a JSON array of strings or a single string
// (treated as one element), matching the tolerant schemas we advertise.
func argStringList(args map[string]any, key string) ([]string, bool) {
	v, ok := args[key]
	if !ok {
		return nil, false
	}
	switch t := v.(type) {
	case []any:
		var out []string
		for _, e := range t {
			if s, ok := e.(string); ok {
				if strings.TrimSpace(s) != "" {
					out = append(out, s)
				}
			}
		}
		return out, true
	case string:
		if strings.TrimSpace(t) == "" {
			return nil, true
		}
		return []string{t}, true
	case []string:
		return t, true
	}
	return nil, true
}
