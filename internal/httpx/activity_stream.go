package httpx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
)

func (h *activityHTTP) stream(w http.ResponseWriter, r *http.Request, query activity.Query) {
	if h.streams.Add(1) > maxActivityStreams {
		h.streams.Add(-1)
		w.Header().Set("Retry-After", "5")
		writeRuntimeAPIError(w, http.StatusTooManyRequests, "STREAM_LIMIT", "too many activity streams")
		return
	}
	defer h.streams.Add(-1)
	store := h.runtime.ActivityJournal()
	page, err := store.Query(r.Context(), query)
	if err != nil {
		writeRuntimeAPIHandlerError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	write := func(kind string, seq uint64, payload any) bool {
		data, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", seq, kind, data); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err = fmt.Fprint(w, "retry: 1000\n\n"); err != nil || controller.Flush() != nil {
		return
	}
	poll := time.NewTicker(time.Second)
	heartbeat := time.NewTicker(15 * time.Second)
	defer poll.Stop()
	defer heartbeat.Stop()
	for {
		if page.Gap {
			if !write("gap", page.PrunedThrough, map[string]any{"pruned_through": page.PrunedThrough, "reason": "older activity was removed by retention"}) {
				return
			}
			query.After = page.PrunedThrough
		}
		if query.After > page.LatestSeq {
			if !write("reset", page.LatestSeq, map[string]any{"latest_seq": page.LatestSeq, "reason": "the requested cursor is ahead of this journal"}) {
				return
			}
			query.After = page.LatestSeq
			page.NextSeq = page.LatestSeq
		}
		for _, event := range page.Events {
			if event.Seq <= query.After {
				continue
			}
			if !write("activity", event.Seq, event) {
				return
			}
			query.After = event.Seq
		}
		if page.NextSeq > query.After {
			query.After = page.NextSeq
			if !write("cursor", query.After, map[string]any{"seq": query.After}) {
				return
			}
		}
		if len(page.Warnings) > 0 {
			if !write("warning", query.After, map[string]any{"warnings": page.Warnings}) {
				return
			}
		}
		if !page.HasMore {
			changed := store.Changed()
			select {
			case <-r.Context().Done():
				return
			case <-changed:
			case <-poll.C:
			case <-heartbeat.C:
				_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if _, err = fmt.Fprint(w, ": heartbeat\n\n"); err != nil || controller.Flush() != nil {
					return
				}
			}
		}
		page, err = store.Query(r.Context(), query)
		if err != nil {
			if r.Context().Err() == nil {
				write("warning", query.After, map[string]any{"warnings": []string{"activity storage could not be read; reconnect to retry"}})
			}
			return
		}
	}
}
