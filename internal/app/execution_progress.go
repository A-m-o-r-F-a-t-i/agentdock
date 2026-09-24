package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
)

type executionProgressFrame struct {
	text string
	at   time.Time
}
type executionProgress struct {
	mu       sync.Mutex
	runtime  *Runtime
	binding  activity.Binding
	name     string
	redactor activity.Redactor
	queue    chan executionProgressFrame
	done     chan struct{}
	cancel   context.CancelFunc
	closed   bool
	dropped  atomic.Uint64
}

// No worker is started for tools that never emit progress. A remote SDK receive
// loop only performs a bounded enqueue; journal I/O cannot block that loop.
func (r *Runtime) observeExecutionProgress(ctx context.Context, binding activity.Binding, name string, redactor activity.Redactor) (context.Context, func()) {
	if binding.ParentCallID != "" || binding.CallID == "" || binding.Visibility == "diagnostic" {
		return ctx, func() {}
	}
	recorder := &executionProgress{runtime: r, binding: binding, name: name, redactor: redactor}
	return activity.WithProgressSink(ctx, recorder.emit), recorder.finish
}
func (p *executionProgress) emit(value activity.Progress) {
	value.Message = strings.Clone(p.redactor.Text(value.Message, 4096))
	value.Server = strings.Clone(p.redactor.Text(value.Server, 256))
	value.Tool = strings.Clone(p.redactor.Text(value.Tool, 256))
	data, err := json.Marshal(value)
	if err != nil {
		p.dropped.Add(1)
		return
	}
	frame := executionProgressFrame{text: string(data) + "\n", at: time.Now().UTC()}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	if p.queue == nil {
		ctx, cancel := context.WithCancel(context.Background())
		p.cancel = cancel
		p.queue = make(chan executionProgressFrame, 32)
		p.done = make(chan struct{})
		go p.run(ctx)
	}
	select {
	case p.queue <- frame:
	default:
		p.dropped.Add(1)
	}
}
func (p *executionProgress) run(ctx context.Context) {
	defer close(p.done)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var buffer strings.Builder
	var stamp time.Time
	var reported uint64
	flush := func() {
		dropped := p.dropped.Load()
		omitted := dropped - reported
		if buffer.Len() == 0 && omitted == 0 {
			return
		}
		text := buffer.String()
		if omitted > 0 {
			text += fmt.Sprintf("[部分流式通知未保存：%d 条]\n", omitted)
		}
		buffer.Reset()
		event := activity.Event{Binding: p.binding, Kind: "tool.output", ToolName: p.name, CreatedAt: stamp, OutputPreview: text, StdoutTruncated: omitted > 0}
		audit, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		_, err := p.runtime.activity.Append(audit, event)
		cancel()
		if err != nil {
			p.dropped.Add(1)
			slog.Warn("Tool progress was not persisted", "call_id", p.binding.CallID)
		}
		reported = dropped
	}
	for {
		select {
		case <-ctx.Done():
			return
		case frame, open := <-p.queue:
			if !open {
				flush()
				return
			}
			if buffer.Len()+len(frame.text) > activity.MaxPreviewBytes {
				flush()
			}
			buffer.WriteString(frame.text)
			stamp = frame.at
		case <-ticker.C:
			flush()
		}
	}
}
func (p *executionProgress) finish() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	if p.queue == nil {
		p.mu.Unlock()
		return
	}
	close(p.queue)
	done, cancel := p.done, p.cancel
	p.mu.Unlock()
	defer cancel()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		cancel()
		slog.Warn("Tool progress flush exceeded its bounded cleanup budget", "call_id", p.binding.CallID)
	}
}
