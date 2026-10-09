package quota

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// lookup follows a dotted path through decoded JSON; a numeric part indexes an
// array. An empty path is the value itself; a null is missing.
func lookup(v any, path string) (any, bool) {
	if path == "" {
		return v, v != nil
	}
	for _, part := range strings.Split(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			v = x[part]
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			v = x[i]
		default:
			return nil, false
		}
		if v == nil {
			return nil, false
		}
	}
	return v, true
}

func lookupNum(v any, path string) (float64, bool) {
	x, ok := lookup(v, path)
	if !ok {
		return 0, false
	}
	return toNum(x)
}

// toNum accepts a number or a numeric string, as these APIs are not consistent
// about which they use.
func toNum(v any) (float64, bool) {
	var s string
	switch x := v.(type) {
	case json.Number:
		s = x.String()
	case string:
		s = strings.TrimSpace(x)
	case float64:
		return x, true
	case int64:
		return float64(x), true
	default:
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

// text renders a JSON or TOML scalar for comparison and display; numbers lose
// any trailing zeros, so 3, 3.0 and "3" read alike.
func text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	}
	if f, ok := toNum(v); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}
