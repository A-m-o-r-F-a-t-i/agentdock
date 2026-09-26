package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Disposable loopback servers and synthetic credentials only.
func TestPostmergeControlRedirectDoesNotForwardAuthority(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			var requests, authorized atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("Authorization") != "" {
					authorized.Add(1)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"ok":true}`)
			}))
			defer target.Close()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL+"/unrelated", http.StatusTemporaryRedirect)
			}))
			defer origin.Close()
			client := newControlClient(resolvedControlOptions{controlOptions: controlOptions{endpoint: origin.URL, timeout: time.Second}, token: "synthetic-audit-token"}, io.Discard)
			_, err := client.request(context.Background(), method, "/internal/runtime/permissions", nil, map[string]any{"expected_revision": 1})
			if requests.Load() != 0 || authorized.Load() != 0 {
				t.Errorf("redirect crossed configured origin: requests=%d authorized=%d", requests.Load(), authorized.Load())
			}
			if err == nil {
				t.Error("redirect was reported as a successful Core response")
			}
		})
	}
}

func TestPostmergeStreamRedirectDoesNotForwardAuthority(t *testing.T) {
	var requests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "id: 1\ndata: {}\n\n")
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/stream", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	options := resolvedControlOptions{controlOptions: controlOptions{endpoint: origin.URL, timeout: time.Second}, token: "synthetic-audit-token"}
	client := newControlClient(options, io.Discard)
	cursor := ""
	received, retry, err := client.watchSSEOnce(context.Background(), "/internal/runtime/calls/stream", url.Values{}, io.Discard, options, &cursor)
	if requests.Load() != 0 || received || retry || err == nil {
		t.Fatalf("stream followed/retried redirect: requests=%d received=%v retry=%v err=%v", requests.Load(), received, retry, err)
	}
}

func TestPostmergeStreamBoundsMultilineEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		line := "data: " + strings.Repeat("x", 65536) + "\n"
		for index := 0; index < 34; index++ {
			if _, err := io.WriteString(w, line); err != nil {
				return
			}
		}
		_, _ = io.WriteString(w, "\n")
	}))
	defer server.Close()
	options := resolvedControlOptions{controlOptions: controlOptions{endpoint: server.URL, timeout: time.Second}}
	client := newControlClient(options, io.Discard)
	cursor := ""
	received, retry, err := client.watchSSEOnce(context.Background(), "/internal/runtime/calls/stream", url.Values{}, io.Discard, options, &cursor)
	if received || retry || err == nil {
		t.Fatalf("event exceeded 2 MiB but was emitted/retried: received=%v retry=%v err=%v", received, retry, err)
	}
}

func TestPostmergeWaitCancellationKeepsInterruptedExitCode(t *testing.T) {
	t.Setenv("AGENTDOCK_HOME", t.TempDir())
	for _, args := range [][]string{
		{"approval", "wait", "approval_fixture"},
		{"insertion", "wait", "conv_fixture", "ins_fixture"},
		{"call", "wait", "call_fixture"},
	} {
		t.Run(args[0], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			args = append(args, "--endpoint", "http://127.0.0.1:1", "--json")
			err := run(ctx, args, &bytes.Buffer{}, &bytes.Buffer{})
			if commandExitCode(err) != controlExitInterrupted {
				t.Fatalf("cancellation changed to exit %d: %v", commandExitCode(err), err)
			}
		})
	}
}
