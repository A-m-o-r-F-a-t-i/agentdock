package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	controlStreamRetryMin        = 250 * time.Millisecond
	controlStreamRetryMax        = 5 * time.Second
	controlStreamMaxColdFailures = 8
)

type controlSSEEvent struct {
	ID    string `json:"id,omitempty"`
	Event string `json:"event,omitempty"`
	Data  any    `json:"data"`
}

// watchSSE reconnects after transient transport/server failures and resumes from
// the most recently emitted event ID. Authentication, validation and output
// failures are terminal; parent-context cancellation ends the watch cleanly.
func (c *controlClient) watchSSE(ctx context.Context, path string, query url.Values, output io.Writer, options resolvedControlOptions) error {
	lastEventID := strings.TrimSpace(query.Get("after"))
	backoff := controlStreamRetryMin
	coldFailures := 0
	for {
		received, retry, err := c.watchSSEOnce(ctx, path, query, output, options, &lastEventID)
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return &controlError{code: controlExitTimeout, stableCode: "STREAM_TIMEOUT", message: "事件订阅超时", cause: ctx.Err()}
			}
			return &controlError{code: controlExitInterrupted, stableCode: "INTERRUPTED", message: "事件订阅已中断", cause: ctx.Err()}
		}
		if !retry {
			return err
		}
		if c.verbose {
			message := "stream closed"
			if err != nil {
				message = err.Error()
			}
			fmt.Fprintf(c.stderr, "control stream reconnect: after=%s reason=%s\n", lastEventID, message)
		}
		if received {
			backoff = controlStreamRetryMin
			coldFailures = 0
		} else if backoff < controlStreamRetryMax {
			coldFailures++
			if coldFailures >= controlStreamMaxColdFailures {
				if err != nil {
					return err
				}
				return controlErrorf(controlExitService, "activity stream 连续 %d 次未建立可用事件流", coldFailures)
			}
			backoff *= 2
			if backoff > controlStreamRetryMax {
				backoff = controlStreamRetryMax
			}
		} else {
			coldFailures++
			if coldFailures >= controlStreamMaxColdFailures {
				if err != nil {
					return err
				}
				return controlErrorf(controlExitService, "activity stream 连续 %d 次未建立可用事件流", coldFailures)
			}
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return &controlError{code: controlExitTimeout, stableCode: "STREAM_TIMEOUT", message: "事件订阅超时", cause: ctx.Err()}
			}
			return &controlError{code: controlExitInterrupted, stableCode: "INTERRUPTED", message: "事件订阅已中断", cause: ctx.Err()}
		case <-timer.C:
		}
	}
}

func (c *controlClient) watchSSEOnce(ctx context.Context, path string, query url.Values, output io.Writer, options resolvedControlOptions, lastEventID *string) (bool, bool, error) {
	requestURL := c.endpoint + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return false, false, controlWrap(controlExitInvalid, "创建 SSE 请求失败", err)
	}
	request.Header.Set("Accept", "text/event-stream")
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	if strings.TrimSpace(*lastEventID) != "" {
		request.Header.Set("Last-Event-ID", strings.TrimSpace(*lastEventID))
	}
	if c.verbose {
		fmt.Fprintf(c.stderr, "control stream: GET %s%s after=%s\n", c.endpoint, path, strings.TrimSpace(*lastEventID))
	}
	// Streaming bodies are intentionally not bounded by the ordinary request
	// timeout. The parent context controls lifetime; the default transport still
	// applies its normal dial and TLS timeouts.
	streamClient := *c.http
	streamClient.Timeout = 0
	response, err := streamClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return false, false, nil
		}
		return false, true, controlWrap(controlExitService, "连接 activity stream 失败", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
		decoded := decodeControlHTTPError(response.StatusCode, data)
		retry := response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
		return false, retry, decoded
	}
	if contentType := strings.ToLower(response.Header.Get("Content-Type")); !strings.HasPrefix(contentType, "text/event-stream") {
		return false, false, controlErrorf(controlExitService, "activity stream Content-Type 无效: %s", contentType)
	}

	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64*1024), 2<<20)
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	current := controlSSEEvent{}
	dataLines := []string{}
	received := false
	flush := func() error {
		if current.ID == "" && current.Event == "" && len(dataLines) == 0 {
			return nil
		}
		raw := strings.Join(dataLines, "\n")
		var data any = raw
		if strings.TrimSpace(raw) != "" {
			decoder := json.NewDecoder(strings.NewReader(raw))
			decoder.UseNumber()
			var parsed any
			if err := decoder.Decode(&parsed); err == nil {
				data = parsed
			}
		}
		current.Data = data
		if !options.quiet {
			if options.jsonLines || options.jsonOutput {
				if err := encoder.Encode(current); err != nil {
					return controlWrap(controlExitService, "写入 SSE 输出失败", err)
				}
			} else {
				pretty, err := json.MarshalIndent(current, "", "  ")
				if err != nil {
					return controlWrap(controlExitService, "格式化 SSE 输出失败", err)
				}
				if _, err := fmt.Fprintln(output, string(pretty)); err != nil {
					return controlWrap(controlExitService, "写入 SSE 输出失败", err)
				}
			}
		}
		if current.ID != "" {
			*lastEventID = current.ID
		}
		received = true
		current = controlSSEEvent{}
		dataLines = dataLines[:0]
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return received, false, err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			current.ID = value
		case "event":
			current.Event = value
		case "data":
			dataLines = append(dataLines, value)
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return received, false, nil
		}
		return received, true, controlWrap(controlExitService, "读取 activity stream 失败", err)
	}
	if err := flush(); err != nil {
		return received, false, err
	}
	return received, true, io.EOF
}
