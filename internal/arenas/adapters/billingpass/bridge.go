// Package billingpass bridges the billing Arena Pass consumer to the arenas
// boundary (P07-T05/P08-T04): the arenas module depends on its own
// consumer-oriented port, and this adapter maps the requests, results and
// error vocabulary of the billing module. It is composed at bootstrap and
// never imported by arenas domain or application code.
package billingpass

import (
	"context"
	"errors"
	"fmt"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	billingapp "github.com/AlexandreZanata/Regnovum/internal/billing/application"
	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// Bridge implements the arenas ArenaPassConsumer port over the billing
// consumer.
type Bridge struct {
	passes billingapp.ArenaPassConsumer
	clock  application.Clock
}

var _ application.ArenaPassConsumer = (*Bridge)(nil)

// New creates the bridge over the billing consumer and the shared clock.
func New(passes billingapp.ArenaPassConsumer, clock application.Clock) *Bridge {
	return &Bridge{passes: passes, clock: clock}
}

// ConsumeArenaPass maps the arenas request into the billing consumption and
// translates the billing error vocabulary into the arenas one.
func (b *Bridge) ConsumeArenaPass(ctx context.Context, request application.PassConsumption) (*application.ConsumedPass, error) {
	arenaID, err := billingdomain.ParseArenaID(request.ArenaID)
	if err != nil {
		return nil, fmt.Errorf("billingpass: invalid arena id: %w", err)
	}

	result, err := b.passes.ConsumeArenaPass(ctx, billingapp.ConsumePassRequest{
		AccountID:  billingdomain.AccountID(request.AccountID),
		ArenaID:    arenaID,
		ConsumedAt: b.clock.Now(),
	})
	if err != nil {
		switch {
		case errors.Is(err, billingdomain.ErrNoPassAvailable):
			return nil, application.ErrNoPassAvailable
		case errors.Is(err, billingapp.ErrArenaAlreadyConsumed):
			return nil, application.ErrArenaAlreadyConsumed
		default:
			return nil, err
		}
	}

	return &application.ConsumedPass{
		LotID:     result.Lot.ID().String(),
		Remaining: result.Remaining,
		Replayed:  result.Replayed,
	}, nil
}
