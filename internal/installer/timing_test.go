package installer

import (
	"testing"
	"time"
)

func TestInstallTimingRecorderPersistsCompletedAndInterruptedSpans(t *testing.T) {
	base := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	current := base
	recorder := newInstallTimingRecorderWithClock(base, func() time.Time { return current })
	recorder.setReusedCachedPayload(true)

	recorder.begin(InstallStageVerify)
	current = current.Add(1250 * time.Millisecond)
	recorder.finishActive()
	recorder.begin(InstallStagePayloadWrite)
	current = current.Add(2 * time.Second)

	inFlight := recorder.snapshot()
	if inFlight.WallDurationMS != 3250 || !inFlight.ReusedCachedPayload || len(inFlight.Stages) != 2 {
		t.Fatalf("in-flight summary=%+v", inFlight)
	}
	if inFlight.Stages[0].DurationMS != 1250 || inFlight.Stages[0].CompletedAt == nil {
		t.Fatalf("verify span=%+v", inFlight.Stages[0])
	}
	if inFlight.Stages[1].CompletedAt != nil {
		t.Fatalf("payload span unexpectedly completed=%+v", inFlight.Stages[1])
	}

	recorder.complete()
	complete := recorder.snapshot()
	if complete.Stages[1].DurationMS != 2000 || complete.Stages[1].CompletedAt == nil {
		t.Fatalf("payload span=%+v", complete.Stages[1])
	}
}

func TestCloneTimingSummaryDetachesStageTimestamps(t *testing.T) {
	completed := time.Date(2026, 9, 25, 12, 0, 1, 0, time.UTC)
	original := &TimingSummary{Stages: []StageTiming{{Stage: InstallStageVerify, CompletedAt: &completed}}}
	clone := cloneTimingSummary(original)
	if clone == original || &clone.Stages[0] == &original.Stages[0] || clone.Stages[0].CompletedAt == original.Stages[0].CompletedAt {
		t.Fatal("timing clone shares mutable storage")
	}
	changed := completed.Add(time.Hour)
	clone.Stages[0].CompletedAt = &changed
	if !original.Stages[0].CompletedAt.Equal(completed) {
		t.Fatal("mutating clone changed original")
	}
}
