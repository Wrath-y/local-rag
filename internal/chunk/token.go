package chunk

import "unicode/utf8"

// EstimateTokens estimates token count for mixed CJK/English text.
// Approximation: ~3 runes per token for Chinese/English mixed content.
func EstimateTokens(text string) int {
	n := utf8.RuneCountInString(text)
	if n == 0 {
		return 0
	}
	return n / 3
}
