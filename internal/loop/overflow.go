package loop

import (
	"regexp"

	"ki/internal/types"
)

// Overflow detection aligned with pi packages/ai/src/utils/overflow.ts:
// regex patterns matching context-overflow error messages from different
// providers, plus a non-overflow exclusion table (rate limiting, throttling)
// so those never trigger a compact-and-retry.

var overflowPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)prompt is too long`),                                                                        // Anthropic token overflow
	regexp.MustCompile(`(?i)request_too_large`),                                                                         // Anthropic request byte-size overflow (HTTP 413)
	regexp.MustCompile(`(?i)input is too long for requested model`),                                                     // Amazon Bedrock
	regexp.MustCompile(`(?i)exceeds the context window`),                                                                // OpenAI (Completions & Responses API)
	regexp.MustCompile(`(?i)exceeds (?:the )?(?:model'?s )?maximum context length(?: of [\d,]+ tokens?|\s*\([\d,]+\))`), // OpenAI-compatible proxies (LiteLLM)
	regexp.MustCompile(`(?i)input token count.*exceeds the maximum`),                                                    // Google (Gemini)
	regexp.MustCompile(`(?i)maximum prompt length is \d+`),                                                              // xAI (Grok)
	regexp.MustCompile(`(?i)reduce the length of the messages`),                                                         // Groq
	regexp.MustCompile(`(?i)maximum context length is \d+ tokens`),                                                      // OpenRouter (most backends)
	regexp.MustCompile(`(?i)exceeds (?:the )?maximum allowed input length of [\d,]+ tokens?`),                           // OpenRouter/Poolside
	regexp.MustCompile(`(?i)input \(\d+ tokens\) is longer than the model'?s context length \(\d+ tokens\)`),            // Together AI
	regexp.MustCompile(`(?i)exceeds the limit of \d+`),                                                                  // GitHub Copilot
	regexp.MustCompile(`(?i)exceeds the available context size`),                                                        // llama.cpp server
	regexp.MustCompile(`(?i)greater than the context length`),                                                           // LM Studio
	regexp.MustCompile(`(?i)context window exceeds limit`),                                                              // MiniMax
	regexp.MustCompile(`(?i)exceeded model token limit`),                                                                // Kimi For Coding
	regexp.MustCompile(`(?i)too large for model with \d+ maximum context length`),                                       // Mistral
	regexp.MustCompile(`(?i)prompt has [\d,]+ tokens?, but the configured context size is [\d,]+ tokens?`),              // DS4 server
	regexp.MustCompile(`(?i)model_context_window_exceeded`),                                                             // z.ai non-standard finish_reason surfaced as error text
	regexp.MustCompile(`(?i)prompt too long; exceeded (?:max )?context length`),                                         // Ollama explicit overflow error
	regexp.MustCompile(`(?i)range of input length should be`),                                                           // DashScope / Qwen Token Plan
	regexp.MustCompile(`(?i)context[_ ]length[_ ]exceeded`),                                                             // Generic fallback
	regexp.MustCompile(`(?i)too many tokens`),                                                                           // Generic fallback
	regexp.MustCompile(`(?i)token limit exceeded`),                                                                      // Generic fallback
	regexp.MustCompile(`(?i)^4(?:00|13)\s*(?:status code)?\s*\(no body\)`),                                              // Cerebras: 400/413 with no body
}

// Non-overflow errors that must never be treated as overflow even if they also
// match an overflow pattern (e.g. Bedrock throttling: "Too many tokens, please
// wait before trying again").
var nonOverflowPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^(Throttling error|Service unavailable):`), // AWS Bedrock non-overflow prefixes
	regexp.MustCompile(`(?i)^throttl`),                                 // ThrottlingException / throttling error (ki has no formatBedrockError pre-pass)
	regexp.MustCompile(`(?i)rate limit`),                               // Generic rate limiting
	regexp.MustCompile(`(?i)too many requests`),                        // Generic HTTP 429 style
}

// IsContextOverflow reports whether an assistant message is a context overflow
// error (stopReason "error" + matching provider message, minus exclusions).
// The silent-overflow and length-stop cases from pi are covered by the
// threshold check in maybeCompact via usage.Input+CacheRead.
func IsContextOverflow(m types.Message) bool {
	if m.StopReason != "error" || m.ErrorMessage == "" {
		return false
	}
	for _, p := range nonOverflowPatterns {
		if p.MatchString(m.ErrorMessage) {
			return false
		}
	}
	for _, p := range overflowPatterns {
		if p.MatchString(m.ErrorMessage) {
			return true
		}
	}
	return false
}
