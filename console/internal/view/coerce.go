package view

import "strconv"

// The dynamic client decodes JSON numbers as int64 or float64 depending on the
// path, and CRD status fields are sometimes serialized as strings. These
// helpers coerce defensively so a type the API server happens to hand us never
// blanks out an otherwise-present field.

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		// Render integral floats without a trailing ".0" (byte counts etc.).
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return ""
	default:
		return ""
	}
}

func asInt(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case float64:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	default:
		return 0
	}
}

func asBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		b, _ := strconv.ParseBool(t)
		return b
	default:
		return false
	}
}
