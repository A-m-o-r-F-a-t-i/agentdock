package activity

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
)

func validateRawAppend(event Event) error {
	remaining := 1 << 20
	check := func(texts ...string) bool {
		for _, text := range texts {
			if len(text) > remaining {
				return false
			}
			remaining -= len(text)
		}
		return true
	}
	if !check(event.Label, event.Title, event.ParameterSummary, event.DisplayCommand, event.Summary, event.OutputPreview, event.StderrPreview, event.Workdir, event.LogicalPath, event.ResolvedPath, event.OwnerInstance, event.ApprovalID, event.RuleID, event.PermissionMode, event.ErrorCode, event.Source, event.SourceOwnerKey, event.Visibility, event.BindingQuality) {
		return errors.New("raw activity event exceeds preparation budget")
	}
	for _, payload := range []*Payload{event.Request, event.Response, event.OutputSource} {
		if payload != nil && !check(payload.Ref, payload.State, payload.Preview, payload.Reason) {
			return errors.New("raw activity payload exceeds preparation budget")
		}
	}
	if detail := event.FileEdit; detail != nil {
		if !check(detail.Action, detail.Path, detail.NewPath, detail.DiffPreview) {
			return errors.New("raw activity edit exceeds preparation budget")
		}
		for _, file := range detail.AffectedFiles[:min(len(detail.AffectedFiles), MaxRecordedAffectedFiles)] {
			if !check(file.Path, file.MoveTo, file.Operation) {
				return errors.New("raw activity files exceed preparation budget")
			}
		}
	}
	return nil
}

// Check before enqueueing: one oversized producer must not invalidate a batch
// that also contains other calls' admission or completion events.
func validatePreparedAppend(event Event) error {
	event.SchemaVersion = SchemaVersion
	event.Seq = math.MaxUint64
	event.EventID = "evt_" + strings.Repeat("f", 24)
	event.CreatedAt = time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if len(data)+1 > MaxEventBytes {
		return errors.New("activity event exceeds size limit")
	}
	return nil
}
