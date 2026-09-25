package app

import (
	"context"
	"os"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
)

// Admission owns this credit before executing a handler. Ordinary progress
// cannot consume the capacity needed to persist its actual outcome.
func (r *Runtime) appendReservedExecution(reservation *activity.AppendReservation, event activity.Event) error {
	if reservation == nil {
		return r.appendExecution(event)
	}
	if event.OwnerInstance == "" {
		event.OwnerPID = os.Getpid()
		event.OwnerInstance = r.executionInstance
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := reservation.Append(ctx, event)
	return err
}
