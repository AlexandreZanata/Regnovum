package application

import (
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// validatedEnds carries the parsed transfer endpoints shared by the plain
// and the idempotent use cases: kinds, labels and the exact amount, all
// validated before any repository call.
type validatedEnds struct {
	fromKind  domain.CustodyKind
	fromLabel string
	toKind    domain.CustodyKind
	toLabel   string
	amount    domain.MilliInk
}

// validateTransferEnds validates the transfer endpoints once for every
// use case that moves value: known kinds, named labels, distinct
// custodies, a source allowed to spend and a positive amount. Invalid
// commands never touch storage.
func validateTransferEnds(fromKind, fromLabel, toKind, toLabel string, millis int64) (validatedEnds, error) {
	parsedFrom, err := domain.ParseCustodyKind(fromKind)
	if err != nil {
		return validatedEnds{}, err
	}
	parsedTo, err := domain.ParseCustodyKind(toKind)
	if err != nil {
		return validatedEnds{}, err
	}
	if fromLabel == "" || toLabel == "" {
		return validatedEnds{}, domain.ErrUnknownCustody
	}
	if parsedFrom == parsedTo && fromLabel == toLabel {
		return validatedEnds{}, domain.ErrSameCustody
	}
	if !parsedFrom.CanSpend() {
		return validatedEnds{}, domain.ErrUnauthorizedCustody
	}
	amount, err := domain.NewMilliInk(millis)
	if err != nil {
		return validatedEnds{}, err
	}
	if amount.IsZero() {
		return validatedEnds{}, domain.ErrInvalidMilliInk
	}
	return validatedEnds{
		fromKind:  parsedFrom,
		fromLabel: fromLabel,
		toKind:    parsedTo,
		toLabel:   toLabel,
		amount:    amount,
	}, nil
}
