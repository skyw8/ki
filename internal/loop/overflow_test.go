package loop

import (
	"testing"

	"ki/internal/types"
)

func TestIsContextOverflow(t *testing.T) {
	cases := []struct {
		name string
		msg  types.Message
		want bool
	}{
		{"anthropic", types.Message{StopReason: "error", ErrorMessage: "prompt is too long: 213462 tokens > 200000 maximum"}, true},
		{"openai", types.Message{StopReason: "error", ErrorMessage: "This model's maximum context length is 131072 tokens. However, you requested about 200000 tokens"}, true},
		{"dashscope", types.Message{StopReason: "error", ErrorMessage: "Range of input length should be [1, 32768]"}, true},
		{"generic", types.Message{StopReason: "error", ErrorMessage: "context_length_exceeded"}, true},
		{"rate limit excluded", types.Message{StopReason: "error", ErrorMessage: "Rate limit reached: too many tokens, please wait before trying again"}, false},
		{"not error", types.Message{StopReason: "stop", ErrorMessage: "prompt is too long"}, false},
		{"unrelated error", types.Message{StopReason: "error", ErrorMessage: "invalid api key"}, false},
		{"bedrock throttle excluded", types.Message{StopReason: "error", ErrorMessage: "ThrottlingException: Too many tokens, please wait before trying again"}, false},
	}
	for _, c := range cases {
		if got := IsContextOverflow(c.msg); got != c.want {
			t.Errorf("%s: IsContextOverflow(%q) = %v, want %v", c.name, c.msg.ErrorMessage, got, c.want)
		}
	}
}
