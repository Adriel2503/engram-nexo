package security

import (
	"encoding/json"
	"math"
	"testing"
)

func TestNexoProjectionIntegerValidation(t *testing.T) {
	for _, v := range []any{true, "7", 1.5, math.Inf(1), math.NaN(), json.Number("1.5"), json.Number("1e3"), json.Number("9223372036854775808")} {
		if _, ok, _ := nexoProjectInteger(v); ok {
			t.Errorf("tipo %T no debe aceptarse como BIGINT", v)
		}
	}
	for _, v := range []any{nil, int64(9007199254740993), json.Number("9223372036854775807"), json.Number("-9223372036854775808"), float64(7)} {
		if _, ok, _ := nexoProjectInteger(v); !ok {
			t.Errorf("tipo %T válido rechazado", v)
		}
	}
}
