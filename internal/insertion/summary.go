package insertion

import (
	"strings"
	"unicode"
)

const SummaryRunes = 160

// Summarize is a display-only excerpt, never a replacement for Item.Text or an
// authenticated response addition. It makes no network or model calls.
func Summarize(text string) string {
	runes := make([]rune, 0, SummaryRunes+1)
	space := false
	for _, r := range text {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			space = len(runes) > 0
			continue
		}
		if space {
			runes = append(runes, ' ')
			space = false
		}
		runes = append(runes, r)
		if len(runes) > SummaryRunes {
			return strings.TrimSpace(string(runes[:SummaryRunes-1])) + "…"
		}
	}
	return string(runes)
}
