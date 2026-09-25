package app

import (
	"context"
	"errors"
	"testing"

	"github.com/uvwt/agentdock/internal/activity"
)

func TestExecutionCapacityRefusesBeforeDispatch(t *testing.T) {
	r := executionTestRuntime(t)
	ctx := scopeHost("capacity-refusal")
	capacity := r.activity.AppendStatistics().EventLimit
	occupied, err := r.activity.ReserveAppend(ctx, capacity)
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	called := false
	_, err = r.callObserved(ctx, ToolSpec{Name: "read_file", Handler: func(context.Context, *Runtime, map[string]any) (Result, error) { called = true; return Result{}, nil }}, map[string]any{"path": "ordinary.txt"})
	var failure *ToolError
	if called || !errors.As(err, &failure) || failure.Code != "ACTIVITY_CAPACITY" || failure.Details["executed"] != false {
		t.Fatalf("overloaded call ran: %v %v", called, err)
	}
}

func TestExecutionCapacityKeepsCompletionUnderNewSaturation(t *testing.T) {
	for _, panicHandler := range []bool{false, true} {
		r := executionTestRuntime(t)
		ctx := scopeHost("reserved-completion")
		var occupied *activity.AppendReservation
		defer func() { occupied.Close() }()
		spec := ToolSpec{Name: "read_file", Handler: func(context.Context, *Runtime, map[string]any) (Result, error) {
			stats := r.activity.AppendStatistics()
			var err error
			occupied, err = r.activity.ReserveAppend(context.Background(), stats.EventLimit-stats.ReservedEvents)
			if err != nil {
				return nil, err
			}
			if panicHandler {
				panic("fixture panic")
			}
			return Result{"content": "actual result"}, nil
		}}
		result, err := r.callObserved(ctx, spec, map[string]any{"path": "ordinary.txt"})
		callID := stringArg(result, "call_id")
		if panicHandler {
			var failure *ToolError
			if !errors.As(err, &failure) || failure.Code != "TOOL_PANIC" {
				t.Fatalf("panic not contained: %v", err)
			}
			callID = stringArg(failure.Details, "call_id")
		} else if err != nil || result["activity_warning"] != nil {
			t.Fatalf("reserved result lost: %v %v", result, err)
		}
		call, lookupErr := r.activity.Call(ctx, callID)
		if lookupErr != nil {
			t.Fatal(lookupErr)
		}
		expected := "succeeded"
		if panicHandler {
			expected = "unknown"
		}
		if call.Status != expected || call.CompletedAt == nil {
			t.Fatalf("wrong recorded outcome: %+v", call)
		}
		occupied.Close()
		if stats := r.activity.AppendStatistics(); stats.ReservedEvents != 0 {
			t.Fatalf("execution completion leaked: %+v", stats)
		}
	}
}

func TestManagementCapacityReservesFinalRecord(t *testing.T) {
	r := executionTestRuntime(t)
	ctx := activity.WithLocalManagement(context.Background())
	binding, err := r.localManagementStart(ctx, "management.fixture", activity.Binding{}, "capacity fixture")
	if err != nil {
		t.Fatal(err)
	}
	stats := r.activity.AppendStatistics()
	occupied, err := r.activity.ReserveAppend(ctx, stats.EventLimit-stats.ReservedEvents)
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	r.localManagementFinish(binding, "management.fixture", "succeeded", "actual management result")
	call, err := r.activity.Call(ctx, binding.CallID)
	if err != nil || call.Status != "succeeded" {
		t.Fatalf("management completion lost: %v", err)
	}
	if _, found := r.localCompletions.Load(binding.CallID); found {
		t.Fatal("management completion lease leaked")
	}
	occupied.Close()
	if r.activity.AppendStatistics().ReservedEvents != 0 {
		t.Fatal("management budget leaked")
	}
}
