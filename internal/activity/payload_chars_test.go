package activity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPayloadCharacterBudgetRoundTripAnd100000Multibyte(t *testing.T) {
	store := testStore(t, Options{})
	payload := store.CapturePayload(t.Context(), strings.Repeat("😀中文", 40000)+"e\u0301\r\n", "complete", NewRedactor())
	if payload.Ref == "" {
		t.Fatal(payload)
	}
	savePayloadCall(t, store, "call_scalar", payload)
	for _, limit := range []int{1, 999, 1000, 5001, 100000} {
		// Small budgets test the first page; full traversal below uses 100,000.
		page, err := store.ReadCallPayloadCharacters(t.Context(), "call_scalar", "response", 0, limit)
		if err != nil || page.ReturnedChars != limit || utf8.RuneCountInString(page.Text) != limit || page.Unit != "unicode_scalar" {
			t.Fatal(limit, page.ReturnedChars, err)
		}
		if page.NextOffset != int64(len(page.Text)) || !page.HasMore {
			t.Fatal("incorrect consumed byte range")
		}
		if limit == 100000 && page.NextOffset <= 262144 {
			t.Fatal("fixture did not exercise old byte cap", page.NextOffset)
		}
	}
	var reconstructed strings.Builder
	var offset int64
	for {
		page, err := store.ReadCallPayloadCharacters(t.Context(), "call_scalar", "response", offset, 100000)
		if err != nil {
			t.Fatal(err)
		}
		reconstructed.WriteString(page.Text)
		if !page.HasMore {
			break
		}
		if page.NextOffset <= offset {
			t.Fatal("non-progressing page")
		}
		offset = page.NextOffset
	}
	original, err := os.ReadFile(filepath.Join(store.root, "payloads", payload.Ref+".json"))
	if err != nil || reconstructed.String() != string(original) {
		t.Fatal("pagination dropped or repeated text", err)
	}
	for _, limit := range []int{0, -1, 100001} {
		if _, err := store.ReadCallPayloadCharacters(t.Context(), "call_scalar", "response", 0, limit); err == nil {
			t.Fatal("invalid character budget accepted", limit)
		}
	}
}
