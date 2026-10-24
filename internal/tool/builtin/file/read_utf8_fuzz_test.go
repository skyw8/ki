package filetools

import (
	"math/rand"
	"testing"
	"unicode/utf8"
)

func TestStreamUTF8RandomChunking(t *testing.T) {
	random := rand.New(rand.NewSource(1))
	for i := 0; i < 5000; i++ {
		input := make([]byte, random.Intn(40))
		if i%2 == 0 {
			runes := make([]rune, random.Intn(20))
			for n := range runes {
				runes[n] = rune(random.Intn(0x110000))
			}
			input = []byte(string(runes))
		} else {
			_, _ = random.Read(input)
		}
		validator := streamUTF8{}
		valid := true
		for start := 0; start < len(input); {
			end := min(len(input), start+1+random.Intn(7))
			if !validator.valid(input[start:end]) {
				valid = false
				break
			}
			start = end
		}
		valid = valid && validator.count == 0
		if valid != utf8.Valid(input) {
			t.Fatalf("input=%x valid=%v", input, valid)
		}
	}
}
