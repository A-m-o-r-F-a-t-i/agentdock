package insertion

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestInsertionSummaryIsBoundedDisplayOnly(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"", ""}, {"  한국어\n\t보충 지시  ", "한국어 보충 지시"}, {"a\x00b", "a b"},
		{strings.Repeat("🙂", SummaryRunes), strings.Repeat("🙂", SummaryRunes)},
		{strings.Repeat("한", SummaryRunes+1), strings.Repeat("한", SummaryRunes-1) + "…"},
	} {
		got := Summarize(test.input)
		if got != test.want || !utf8.ValidString(got) || utf8.RuneCountInString(got) > SummaryRunes {
			t.Fatalf("summary=%q want=%q", got, test.want)
		}
	}
}
func TestInsertionSummaryKeepsFullTextIdentityAndDeadline(t *testing.T) {
	store, now, target := fixture(t)
	text := "  원문 보존\n" + strings.Repeat("추가 지시 🙂 ", 80) + "\n  "
	item, err := store.Add(context.Background(), target, "summary_once", text)
	if err != nil {
		t.Fatal(err)
	}
	if item.Text != text || item.Summary != Summarize(text) || item.Summary == text {
		t.Fatal("summary replaced original")
	}
	same, err := store.Add(context.Background(), target, "summary_once", text)
	if err != nil || same.ID != item.ID || !same.ExpiresAt.Equal(item.ExpiresAt) {
		t.Fatalf("summary changed idempotency: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(store.root, "queue.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk diskState
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if len(disk.Items) != 1 || disk.Items[0].Text != text || disk.Items[0].Summary != "" {
		t.Fatal("original duplicated or changed in storage")
	}
	reloaded, err := New(store.root, "summary_restart", func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	list, err := reloaded.List(context.Background(), target.Owner, target.Conversation)
	if err != nil || len(list) != 1 || list[0].Summary != item.Summary || list[0].Text != text || !list[0].ExpiresAt.Equal(item.ExpiresAt) {
		t.Fatalf("reload changed message: %v", err)
	}
	reserved, err := reloaded.Reserve(context.Background(), target, "call_summary", *now)
	if err != nil || len(reserved) != 1 || reserved[0].Text != text {
		t.Fatal("preview was delivered instead of full text")
	}
	finished, err := reloaded.Finish(context.Background(), "call_summary", true)
	if err != nil || len(finished) != 1 || finished[0].Text != text {
		t.Fatal("attachment lost original")
	}
}
