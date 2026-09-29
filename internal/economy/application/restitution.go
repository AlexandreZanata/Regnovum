package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// RestituteCommand pays one third-party restitution for a platform
// error: the unique reference, the origin vault, the beneficiary
// account, the closed cause, the exact amount and the two governors.
// The purpose is never caller-chosen: every restitution settles as a
// compensation through the single disbursement port, so no parallel
// path can pay a third party. Fiat settlements never reach this port:
// they reconcile in the separate fiat books, and this use case moves
// INK only from available Treasury stock, never from escrow or another
// holder. Amounts stay explicit per act.
type RestituteCommand struct {
	Key         string
	Vault       string
	Beneficiary string
	Cause       string
	Millis      int64
	ApproverOne string
	ApproverTwo string
}

// RestituteResult is the settled restitution: the Treasury transfer
// that paid the beneficiary, the paid amount, the validated cause and
// whether the call replayed the original settlement.
type RestituteResult struct {
	TransferID string
	Paid       domain.MilliInk
	Cause      domain.RestitutionCause
	Replayed   bool
}

// RestituteUseCase settles one audited third-party restitution without
// creating value and without debiting escrow. It is an internal
// operation: no public surface calls it.
type RestituteUseCase struct {
	disbursements DisbursementRepository
}

// NewRestituteUseCase creates an instance of RestituteUseCase.
func NewRestituteUseCase(disbursements DisbursementRepository) *RestituteUseCase {
	return &RestituteUseCase{disbursements: disbursements}
}

// Execute validates the cause and settles the act through the single
// disbursement port as a compensation. A single approver twice, a
// beneficiary governing itself, any partial credential and any cause
// outside the three platform-error scenarios stop before the journal:
// reparations carry origin, beneficiary and unique reference, or
// nothing moves at all.
func (uc *RestituteUseCase) Execute(ctx context.Context, cmd RestituteCommand) (*RestituteResult, error) {
	act, err := validateGovernedAct(cmd.Key, cmd.Vault, cmd.Beneficiary, cmd.Millis, cmd.ApproverOne, cmd.ApproverTwo)
	if err != nil {
		return nil, err
	}
	cause, err := domain.ParseRestitutionCause(cmd.Cause)
	if err != nil {
		return nil, err
	}
	settled, err := uc.disbursements.Disburse(ctx, DisburseRequest{
		Key: act.key, Vault: act.vault, Beneficiary: act.beneficiary,
		Purpose:     domain.DisbursementCompensation,
		Amount:      act.amount,
		ApproverOne: act.approverOne, ApproverTwo: act.approverTwo,
	})
	if err != nil {
		return nil, err
	}
	return &RestituteResult{
		TransferID: settled.TransferID,
		Paid:       settled.Paid,
		Cause:      cause,
		Replayed:   settled.Replayed,
	}, nil
}
