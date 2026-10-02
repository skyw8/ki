package agent

import "testing"

func TestParseForkTurns(t *testing.T) {
	for _, test := range []struct {
		value string
		want  int
	}{
		{"", -1}, {" ALL ", -1}, {" none ", 0}, {"1", 1}, {" 2 ", 2},
	} {
		t.Run(test.value, func(t *testing.T) {
			got, err := ParseForkTurns(test.value)
			if err != nil || got != test.want {
				t.Fatalf("ParseForkTurns(%q) = %d, %v; want %d", test.value, got, err, test.want)
			}
		})
	}
	for _, value := range []string{"0", "-1", "1.5", "last", "99999999999999999999999999"} {
		if _, err := ParseForkTurns(value); err == nil {
			t.Errorf("ParseForkTurns(%q) accepted invalid input", value)
		}
	}
}
