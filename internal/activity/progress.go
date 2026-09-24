package activity

import "context"

// Progress is request-correlated tool data. It never changes execution binding,
// permission, or the trusted user-insertion namespace.
type Progress struct {
	Server  string  `json:"server,omitempty"`
	Tool    string  `json:"tool,omitempty"`
	Message string  `json:"message,omitempty"`
	Current float64 `json:"progress"`
	Total   float64 `json:"total,omitempty"`
}
type ProgressSink func(Progress)
type progressKey struct{}

func WithProgressSink(ctx context.Context, sink ProgressSink) context.Context {
	return context.WithValue(ctx, progressKey{}, sink)
}
func ProgressSinkFromContext(ctx context.Context) ProgressSink {
	sink, _ := ctx.Value(progressKey{}).(ProgressSink)
	return sink
}
