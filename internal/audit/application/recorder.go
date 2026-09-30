// Package application defines the audit trail port: one append-only record
// per administrative fact. It depends only on the audit domain and the Go
// standard library.
package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/audit/domain"
)

// RecordResult is the outcome of recording one fact: the stable row
// identifier and whether the idempotency key resolved the original row.
type RecordResult struct {
	ID       string
	Replayed bool
}

// Recorder persists administrative audit events. Implementations insert
// only: updates and deletes are rejected by the database for every role.
// Records carrying an idempotency key resolve the original row on retry.
type Recorder interface {
	// Record validates and stores one fact.
	Record(ctx context.Context, event domain.AuditEvent) (*RecordResult, error)
}
