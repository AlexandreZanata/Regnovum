package application

import (
	"context"
	"reflect"

	"github.com/AlexandreZanata/Regnovum/internal/crown/domain"
)

// RecordStore is the consumer port for the Royal Book: it keeps
// the append-only sequence of public royal facts. There is no
// update and no delete: implementations stay out of production
// wiring until the release gate, and tests stand in with a fake.
type RecordStore interface {
	// Append publishes one summary. Replaying the identical
	// summary returns nil; a divergent summary under a recorded
	// act refuses with domain.ErrTamperedAct; a correction
	// without a recorded original refuses with
	// domain.ErrDetachedCorrection.
	Append(ctx context.Context, record domain.PublicRecord) error
	// Find returns one recorded summary, or
	// domain.ErrUnrecordedAct when the act stays hidden.
	Find(ctx context.Context, act domain.ActID) (domain.PublicRecord, error)
	// List returns the recorded summaries in publication order.
	List(ctx context.Context) ([]domain.PublicRecord, error)
}

// recordStoreSurface is the exact method surface of the port:
// append, find and list. Anything else is a second ledger with
// rewrite power, not the Royal Book.
var recordStoreSurface = map[string]bool{"Append": true, "Find": true, "List": true}

// checkRecordStoreSurface proves the port never gains rewrite
// power: the gate fails closed if a method appears outside the
// append-only surface.
func checkRecordStoreSurface(store RecordStore) error {
	surface := reflect.TypeOf(store)
	for i := 0; i < surface.NumMethod(); i++ {
		if !recordStoreSurface[surface.Method(i).Name] {
			return domain.ErrInvalidAuthority
		}
	}
	return nil
}

// PublishCommand carries one sealed act with its optional appeal
// reference for publication. Raw tokens fail validation before
// the store is touched: shapeless requests never reach storage.
type PublishCommand struct {
	Act    domain.RoyalAct
	Appeal string
}

// PublishUseCase publishes privacy-safe royal records through
// the book port only. It summarizes the sealed act, appends the
// public summary once, and reads it back proving the ledger and
// the Royal Book cite the same act, book, reign and digest. It
// wires nothing: fakes stand in for the store until the release
// gate.
type PublishUseCase struct {
	records RecordStore
}

// NewPublishUseCase creates an instance of PublishUseCase.
func NewPublishUseCase(records RecordStore) *PublishUseCase {
	return &PublishUseCase{records: records}
}

// Execute publishes one royal record or refuses before any effect.
func (uc *PublishUseCase) Execute(ctx context.Context, cmd PublishCommand) (domain.PublicRecord, error) {
	if err := checkRecordStoreSurface(uc.records); err != nil {
		return domain.PublicRecord{}, err
	}
	record, err := domain.Summarize(cmd.Act, cmd.Appeal)
	if err != nil {
		return domain.PublicRecord{}, err
	}
	if err := uc.records.Append(ctx, record); err != nil {
		return domain.PublicRecord{}, err
	}
	stored, err := uc.records.Find(ctx, record.Act)
	if err != nil {
		return domain.PublicRecord{}, err
	}
	if stored.Digest != record.Digest || stored.Reign != record.Reign || stored.Season != record.Season {
		return domain.PublicRecord{}, domain.ErrTamperedAct
	}
	return stored, nil
}

// VerifyCoverage proves no executed act stays hidden: every act
// in the executed sequence must name a recorded summary.
func (uc *PublishUseCase) VerifyCoverage(ctx context.Context, executed []domain.ActID) error {
	listed, err := uc.records.List(ctx)
	if err != nil {
		return err
	}
	book := domain.RoyalBook{}
	for _, record := range listed {
		if err := book.Append(record); err != nil {
			return err
		}
	}
	return domain.AuditCoverage(executed, book)
}
