package desktopruntime

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTailServiceLogFileReturnsRequestedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentdock.err.log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := tailServiceLogFile(context.Background(), path, 2, false, &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "three\nfour\n"; got != want {
		t.Fatalf("tail output = %q, want %q", got, want)
	}
}

func TestLastLogLinesHandlesMissingFinalNewline(t *testing.T) {
	if got, want := string(lastLogLines([]byte("one\ntwo\nthree"), 2)), "two\nthree"; got != want {
		t.Fatalf("lastLogLines = %q, want %q", got, want)
	}
}

func TestTailServiceLogFileRejectsUnboundedRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentdock.err.log")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tailServiceLogFile(context.Background(), path, 10001, false, &bytes.Buffer{}); err == nil {
		t.Fatal("expected line bound error")
	}
}
