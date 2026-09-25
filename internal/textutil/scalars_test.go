package textutil

import (
	"testing"
	"unicode/utf8"
)

func TestScalarPrefix(t *testing.T) {
	for _, text := range []string{"", "a", "汉字", "😀a", "e\u0301", "👩\u200d💻\r\n"} {
		for limit := 0; limit <= 8; limit++ {
			prefix, count, more := ScalarPrefix(text, limit)
			want := min(limit, utf8.RuneCountInString(text))
			if !utf8.ValidString(prefix) || count != want || utf8.RuneCountInString(prefix) != want || more != (want < utf8.RuneCountInString(text)) {
				t.Fatalf("%q %d => %q %d %v", text, limit, prefix, count, more)
			}
		}
	}
}
