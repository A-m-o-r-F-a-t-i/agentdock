package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func Test113ClientConnectionEvidence(t *testing.T) {
	r := &Runtime{}
	state := func() string {
		value, err := r.RuntimeClientConnection(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return stringArg(value, "state")
	}
	if state() != "unobserved" {
		t.Fatal("fabricated initial authorization")
	}
	done := r.ObserveClientRequest("credential-digest-a", true)
	if state() != "client_connected" {
		t.Fatal("active authorized request not connected")
	}
	r.ObserveClientRequest("", false)()
	r.ObserveClientRequest("random-invalid-digest", false)()
	if state() != "client_connected" {
		t.Fatal("anonymous/unknown discovery downgraded a known active client")
	}
	done()
	done()
	if state() != "recently_connected" {
		t.Fatal("request completion lost recent connection")
	}
	r.connections.mu.Lock()
	r.connections.clients["credential-digest-a"].AuthorizedAt = time.Now().Add(-3 * time.Minute)
	r.connections.mu.Unlock()
	if state() != "authorized" {
		t.Fatal("idle authorized client incorrectly requires authentication")
	}
	r.ObserveClientRequest("credential-digest-a", false)()
	if state() != "reauthorization_required" {
		t.Fatal("known rejected credential did not require reauthorization")
	}
	done = r.ObserveClientRequest("credential-digest-a", true)
	if state() != "client_connected" {
		t.Fatal("successful authentication did not clear stale rejection")
	}
	done()
	result, _ := r.RuntimeClientConnection(context.Background())
	if strings.Contains(fmt.Sprint(result), "credential-digest-a") {
		t.Fatal("observation key leaked through status API")
	}
}
func Test113ClientConnectionBounded(t *testing.T) {
	r := &Runtime{}
	for i := 0; i < 300; i++ {
		r.ObserveClientRequest(fmt.Sprintf("digest-%d", i), true)()
	}
	r.connections.mu.Lock()
	count := len(r.connections.clients)
	r.connections.mu.Unlock()
	if count != 128 {
		t.Fatalf("unbounded client observation map: %d", count)
	}
}
