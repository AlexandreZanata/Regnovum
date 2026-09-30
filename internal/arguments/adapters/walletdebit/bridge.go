// Package walletdebit bridges the arguments publication charge to the
// wallet boundary (P10-T04): the arguments module depends on its own
// consumer-oriented port, and this adapter maps the request, the result and
// the error vocabulary of the wallet module. It is composed at bootstrap
// and never imported by arguments domain or application code.
package walletdebit

import (
	"context"
	"errors"

	"github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// DebitExecutor is the wallet capability the bridge consumes: the INK debit
// use case, which is idempotent by attempt key and joins a shared
// transaction when the context carries one.
type DebitExecutor interface {
	Execute(ctx context.Context, cmd walletapp.DebitInkCommand) (*walletapp.DebitResult, error)
}

// Bridge implements the arguments InkDebit port over the wallet debit use
// case.
type Bridge struct {
	debits DebitExecutor
}

var _ application.InkDebit = (*Bridge)(nil)

// New creates the bridge over the wallet debit executor.
func New(debits DebitExecutor) *Bridge {
	return &Bridge{debits: debits}
}

// Debit maps the publication charge into the wallet debit and translates
// the wallet error vocabulary into the arguments one.
func (b *Bridge) Debit(ctx context.Context, request application.InkDebitRequest) (application.InkDebitResult, error) {
	result, err := b.debits.Execute(ctx, walletapp.DebitInkCommand{
		AccountID:      request.AccountID,
		OperationType:  walletdomain.OperationDebitArgument.String(),
		Amount:         request.Amount,
		Reference:      request.Reference,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		if errors.Is(err, walletdomain.ErrInsufficientInk) {
			return application.InkDebitResult{}, application.ErrInsufficientInk
		}
		return application.InkDebitResult{}, err
	}
	return application.InkDebitResult{Replayed: result.Replayed}, nil
}
