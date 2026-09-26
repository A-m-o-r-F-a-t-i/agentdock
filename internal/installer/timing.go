package installer

import "time"

type installTimingRecorder struct {
	startedAt time.Time
	now       func() time.Time
	stages    []StageTiming
	active    int
	reused    bool
	completed bool
}

func newInstallTimingRecorder(startedAt time.Time) *installTimingRecorder {
	return newInstallTimingRecorderWithClock(startedAt, time.Now)
}

func newInstallTimingRecorderWithClock(startedAt time.Time, now func() time.Time) *installTimingRecorder {
	if now == nil {
		now = time.Now
	}
	if startedAt.IsZero() {
		startedAt = now().UTC()
	}
	return &installTimingRecorder{startedAt: startedAt.UTC(), now: now, active: -1}
}

func (recorder *installTimingRecorder) begin(stage InstallStage) {
	if recorder == nil || stage == "" || recorder.completed {
		return
	}
	recorder.finishActive()
	recorder.stages = append(recorder.stages, StageTiming{Stage: stage, StartedAt: recorder.now().UTC()})
	recorder.active = len(recorder.stages) - 1
}

func (recorder *installTimingRecorder) finishActive() {
	if recorder == nil || recorder.active < 0 || recorder.active >= len(recorder.stages) {
		return
	}
	span := &recorder.stages[recorder.active]
	if span.CompletedAt == nil {
		completed := recorder.now().UTC()
		if completed.Before(span.StartedAt) {
			completed = span.StartedAt
		}
		span.CompletedAt = &completed
		span.DurationMS = completed.Sub(span.StartedAt).Milliseconds()
	}
	recorder.active = -1
}

func (recorder *installTimingRecorder) setReusedCachedPayload(reused bool) {
	if recorder != nil {
		recorder.reused = reused
	}
}

func (recorder *installTimingRecorder) complete() {
	if recorder == nil || recorder.completed {
		return
	}
	recorder.finishActive()
	recorder.completed = true
}

func (recorder *installTimingRecorder) snapshot() *TimingSummary {
	if recorder == nil {
		return nil
	}
	now := recorder.now().UTC()
	if now.Before(recorder.startedAt) {
		now = recorder.startedAt
	}
	stages := make([]StageTiming, len(recorder.stages))
	copy(stages, recorder.stages)
	return &TimingSummary{
		WallDurationMS:      now.Sub(recorder.startedAt).Milliseconds(),
		ReusedCachedPayload: recorder.reused,
		Stages:              stages,
	}
}

func cloneTimingSummary(summary *TimingSummary) *TimingSummary {
	if summary == nil {
		return nil
	}
	clone := *summary
	clone.Stages = append([]StageTiming(nil), summary.Stages...)
	for i := range clone.Stages {
		if summary.Stages[i].CompletedAt != nil {
			completed := *summary.Stages[i].CompletedAt
			clone.Stages[i].CompletedAt = &completed
		}
	}
	return &clone
}
