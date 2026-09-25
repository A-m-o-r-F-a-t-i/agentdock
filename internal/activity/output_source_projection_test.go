package activity

import "testing"

func TestOutputSourceProjectionRemainsVisibleWithoutEnvelope(t *testing.T) {
	call := &ExecutionCall{Status: "running"}
	source := &Payload{Ref: "retained-source", State: "complete", Bytes: 1234}
	applyCallExecutionFacts(call, Event{Kind: "call.payload", OutputSource: source})
	if !call.HasOutput || call.OutputSource == nil || call.OutputSource.Ref != source.Ref || call.Status != "running" {
		t.Fatal("retained source did not become visible independently of response or changed lifecycle")
	}
	source.Ref = "mutated-input"
	if call.OutputSource.Ref == source.Ref {
		t.Fatal("projection retained mutable input payload")
	}
	applyCallExecutionFacts(call, Event{Kind: "call.completed", Response: &Payload{State: "complete", Ref: "transport-envelope"}})
	if !call.HasOutput || call.OutputSource.Ref != "retained-source" {
		t.Fatal("transport envelope replaced original output source")
	}
}
