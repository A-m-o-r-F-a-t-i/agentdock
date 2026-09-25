package activity

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"
)

// LocalizedText is optional presentation data produced by AgentDock, not a tool
// argument, execution identity, or a replacement for a stored result payload.
// The digest binds it to exactly one retained title/summary; stale metadata is
// dropped rather than translating an unrelated failure or user-provided text.
type LocalizedText struct {
	SchemaVersion int      `json:"schema_version"`
	Code          string   `json:"code"`
	Args          []string `json:"args"`
	TextHash      string   `json:"text_hash"`
}

func textDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
func NewLocalizedText(code, raw string, args ...string) *LocalizedText {
	value := &LocalizedText{SchemaVersion: 1, Code: code, Args: append([]string{}, args...), TextHash: textDigest(raw)}
	if !value.valid(raw) {
		return nil
	}
	return value
}
func (value *LocalizedText) valid(raw string) bool {
	if value == nil || value.SchemaVersion != 1 || len(value.Code) == 0 || len(value.Code) > 96 || len(value.Args) > 6 || value.TextHash != textDigest(raw) {
		return false
	}
	if value.Code != "permission.update" && value.Code != "permission.updated" && !strings.HasPrefix(value.Code, "tool.") {
		return false
	}
	for _, r := range value.Code {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_') {
			return false
		}
	}
	size := 0
	for _, arg := range value.Args {
		if !utf8.ValidString(arg) || len(arg) > 1024 {
			return false
		}
		size += len(arg)
	}
	return size <= 4096
}
func (value *LocalizedText) clone() *LocalizedText {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Args = append([]string{}, value.Args...)
	return &copy
}
func (r Redactor) localized(value *LocalizedText, original, sanitized string) *LocalizedText {
	if !value.valid(original) {
		return nil
	}
	copy := value.clone()
	for i, arg := range copy.Args {
		copy.Args[i] = r.Text(arg, 1024)
	}
	copy.TextHash = textDigest(sanitized)
	return copy
}
