package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPostmergeStreamBudgetResetsBetweenEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for index := 1; index <= 40; index++ {
			if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", index, strings.Repeat("x", 65536)); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	options := resolvedControlOptions{controlOptions: controlOptions{endpoint: server.URL, timeout: time.Second}}
	client := newControlClient(options, io.Discard)
	cursor := ""
	received, retry, err := client.watchSSEOnce(context.Background(), "/stream", url.Values{}, io.Discard, options, &cursor)
	if !received || !retry || !errors.Is(err, io.EOF) || cursor != "40" {
		t.Fatalf("valid events shared an incorrect lifetime budget: received=%v retry=%v cursor=%s err=%v", received, retry, cursor, err)
	}
}

func TestPostmergeStreamOversizeNeverAdvancesPastLastCompleteEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "id: 7\ndata: {}\n\nid: 8\n")
		for index := 0; index < 34; index++ {
			if _, err := io.WriteString(w, "data: "+strings.Repeat("x", 65536)+"\n"); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	options := resolvedControlOptions{controlOptions: controlOptions{endpoint: server.URL, jsonLines: true}}
	client := newControlClient(options, io.Discard)
	var output bytes.Buffer
	cursor := ""
	received, retry, err := client.watchSSEOnce(context.Background(), "/stream", url.Values{}, &output, options, &cursor)
	if !received || retry || err == nil || cursor != "7" || bytes.Count(output.Bytes(), []byte{'\n'}) != 1 {
		t.Fatalf("incomplete event changed resume boundary: received=%v retry=%v cursor=%s err=%v", received, retry, cursor, err)
	}
}

func TestPostmergeWaitContextMachineCodes(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		code, stable := controlExitTimeout, "WAIT_TIMEOUT"
		if interrupted {
			cancel()
			ctx, cancel = context.WithCancel(context.Background())
			cancel()
			code, stable = controlExitInterrupted, "INTERRUPTED"
		}
		err := controlWaitContextError(ctx, "fixture timed out")
		cancel()
		var output bytes.Buffer
		_ = renderControlCommandError([]string{"--json"}, &output, err)
		var envelope struct {
			Code string `json:"code"`
			Exit int    `json:"exit_code"`
		}
		if decodeErr := json.Unmarshal(output.Bytes(), &envelope); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if commandExitCode(err) != code || envelope.Exit != code || envelope.Code != stable {
			t.Fatalf("process/machine error mismatch: %+v err=%v", envelope, err)
		}
	}
}
