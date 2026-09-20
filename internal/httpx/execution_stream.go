package httpx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
)

func (h *activityHTTP) streamCalls(w http.ResponseWriter, r *http.Request, query activity.CallQuery) {
	query.Updates = true
	query.IncludeOutput = false
	query.Before = 0
	if cursor := r.Header.Get("Last-Event-ID"); cursor != "" {
		value, err := strconv.ParseUint(cursor, 10, 64)
		if err != nil {
			writeRuntimeAPIError(w, 400, "INVALID_CURSOR", "invalid Last-Event-ID")
			return
		}
		query.After = value
	}
	if h.streams.Add(1) > maxActivityStreams {
		h.streams.Add(-1)
		w.Header().Set("Retry-After", "5")
		writeRuntimeAPIError(w, 429, "STREAM_LIMIT", "too many execution streams")
		return
	}
	defer h.streams.Add(-1)
	store := h.runtime.ActivityJournal()
	page, err := store.Calls(r.Context(), query)
	if err != nil {
		executionError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	control := http.NewResponseController(w)
	send := func(kind string, id uint64, value any) bool {
		data, err := json.Marshal(value)
		if err != nil {
			return false
		}
		_ = control.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", id, kind, data)
		return err == nil && control.Flush() == nil
	}
	_ = control.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err = fmt.Fprint(w, "retry: 1000\n\n"); err != nil || control.Flush() != nil {
		return
	}
	poll := time.NewTicker(time.Second)
	heartbeat := time.NewTicker(15 * time.Second)
	defer poll.Stop()
	defer heartbeat.Stop()
	warnedGap := false
	for {
		if page.Reset {
			if !send("reset", page.LatestSeq, map[string]any{"reason": "journal cursor is ahead of the current service", "latest_seq": page.LatestSeq}) {
				return
			}
			query.After = page.LatestSeq
		}
		if page.Gap && !warnedGap {
			if !send("gap", query.After, map[string]any{"pruned_through": page.PrunedThrough, "warnings": page.Warnings, "reason": "retained history contains a gap"}) {
				return
			}
			warnedGap = true
		}
		for _, call := range page.Calls {
			if call.UpdatedSeq <= query.After {
				continue
			}
			if !send("call", call.UpdatedSeq, call) {
				return
			}
			query.After = call.UpdatedSeq
		}
		if page.NextSeq > query.After {
			query.After = page.NextSeq
			if !send("cursor", query.After, map[string]any{"seq": query.After}) {
				return
			}
		}
		if !page.HasMore {
			select {
			case <-r.Context().Done():
				return
			case <-store.Changed():
			case <-poll.C:
			case <-heartbeat.C:
				_ = control.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if _, err = fmt.Fprint(w, ": heartbeat\n\n"); err != nil || control.Flush() != nil {
					return
				}
			}
		}
		page, err = store.Calls(r.Context(), query)
		if err != nil {
			if r.Context().Err() == nil {
				send("warning", query.After, map[string]any{"reason": "execution storage could not be read; reconnect to retry"})
			}
			return
		}
	}
}
