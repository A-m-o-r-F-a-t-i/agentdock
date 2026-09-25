package textutil

// ScalarPrefix counts Unicode scalar values, not UTF-8 bytes or UTF-16 code
// units. Input is valid UTF-8 (JSON and file readers enforce that contract).
// It scans at most limit+1 scalars and never allocates a full []rune copy.
func ScalarPrefix(text string, limit int) (prefix string, displayed int, more bool) {
	if limit < 0 {
		limit = 0
	}
	for index := range text {
		if displayed == limit {
			return text[:index], displayed, true
		}
		displayed++
	}
	return text, displayed, false
}
