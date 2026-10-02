package support

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"
)

func TestAsIntCheckedConversion(t *testing.T) {
	limit := math.Ldexp(1, strconv.IntSize-1)
	for _, test := range []struct {
		name  string
		value any
		want  int
		ok    bool
	}{
		{"int", int(7), 7, true},
		{"int32", int32(7), 7, true},
		{"int64", int64(7), 7, true},
		{"float", float64(7), 7, true},
		{"number", json.Number("7"), 7, true},
		{"fraction", 1.5, 0, false},
		{"number fraction", json.Number("1.5"), 0, false},
		{"number overflow", json.Number("9223372036854775808"), 0, false},
		{"nan", math.NaN(), 0, false},
		{"infinity", math.Inf(1), 0, false},
		{"negative infinity", math.Inf(-1), 0, false},
		{"upper bound", limit, 0, false},
		{"below lower bound", math.Nextafter(-limit, math.Inf(-1)), 0, false},
		{"nil", nil, 0, false},
		{"string", "7", 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := AsInt(test.value)
			if ok != test.ok || ok && got != test.want {
				t.Fatalf("AsInt(%v) = %d, %v; want %d, %v", test.value, got, ok, test.want, test.ok)
			}
		})
	}
}
