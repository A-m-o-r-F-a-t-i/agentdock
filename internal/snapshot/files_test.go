package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchSeesSameSizeSameTimestampContentChange(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "nested")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	files := NewTreeFiles(root, nil)
	defer files.Close()
	files.Add(root)
	files.Add(dir)
	before := files.Revision()
	if err := os.WriteFile(path, []byte("after!"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return files.Revision() != before })
	if err := files.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
}
func TestWatchDoesNotBlockReplacingMemberDirectory(t *testing.T) {
	root := t.TempDir()
	member := filepath.Join(root, "member")
	if err := os.MkdirAll(filepath.Join(member, "skills", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	files := NewTreeFiles(root, nil)
	defer files.Close()
	files.Add(root)
	files.Add(member)
	files.Add(filepath.Join(member, "skills", "nested"))
	if err := os.Rename(member, filepath.Join(root, "old")); err != nil {
		t.Fatalf("watch blocked atomic directory replacement: %v", err)
	}
}
func TestWatchClosesWithoutPendingReadLeak(t *testing.T) {
	for range 20 {
		files := NewTreeFiles(t.TempDir(), nil)
		done := make(chan struct{})
		go func() { files.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("pending watch did not cancel")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	files := NewFiles(nil)
	defer files.Close()
	if err := files.Sync(ctx); err != nil && err != context.Canceled {
		t.Fatal(err)
	}
}
