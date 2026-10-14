package security

// NX-07: BIGINT numérico, validado antes de conservarlo en la evidencia.
import (
	"encoding/json"
	"strconv"
)

func nexoProjectInteger(v any) (any, bool, bool) {
	switch n := v.(type) {
	case nil:
		return nil, true, false
	case int:
		return int64(n), true, false
	case int64:
		return n, true, false
	case json.Number:
		i, err := n.Int64()
		return i, err == nil, false
	case float64:
		i, err := strconv.ParseInt(strconv.FormatFloat(n, 'f', -1, 64), 10, 64)
		return i, err == nil, false
	}
	return nil, false, false
}
