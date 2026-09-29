package application

import (
	"context"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// DisburseCommand pays one governed Treasury disbursement: the act,
// the origin vault, the beneficiary account, the allowlisted purpose,
// the exact amount and the two governors. Amounts and budgets stay
// explicit per act: no share, default or inferred approval lives here.
type DisburseCommand struct {
	Key         string
	Vault       string
	Beneficiary string
	Purpose     string
	Millis      int64
	ApproverOne string
	ApproverTwo string
}

// DisburseRequest is a validated disbursement for the repository port.
// Beneficiaries and governors travel as validated tokens; the adapter
// binds them to account rows.
type DisburseRequest struct {
	Key         domain.IntentionKey
	Vault       domain.TreasuryVault
	Beneficiary domain.IntentionActor
	Purpose     domain.DisbursementPurpose
	Amount      domain.MilliInk
	ApproverOne domain.IntentionActor
	ApproverTwo domain.IntentionActor
}

// DisburseResult is the settled disbursement: the Treasury transfer
// that paid the beneficiary and the paid amount, with replay marking
// the retries that resolved the original settlement.
type DisburseResult struct {
	TransferID string
	Paid       domain.MilliInk
	Replayed   bool
}

// DisbursementRepository settles governed disbursements in one
// transaction: the origin vault debits while the beneficiary credits,
// and the audit row names governors beside the legs, or nothing moves
// at all.
type DisbursementRepository interface {
	// Disburse settles one disbursement act, keyed idempotently by the
	// act itself. Replays resolve the original settlement untouched;
	// divergent terms under one act conflict instead of paying twice.
	Disburse(ctx context.Context, request DisburseRequest) (*DisburseResult, error)
}

// DisburseUseCase settles one governed Treasury disbursement without
// creating value. It is an internal operation: no public surface calls
// it.
type DisburseUseCase struct {
	disbursements DisbursementRepository
}

// NewDisburseUseCase creates an instance of DisburseUseCase.
func NewDisburseUseCase(disbursements DisbursementRepository) *DisburseUseCase {
	return &DisburseUseCase{disbursements: disbursements}
}

// Execute validates the governors and settles the act. A single
// approver twice, a beneficiary governing itself and any partial
// credential stop before the journal: irreversible moves need two
// whole governors besides the paid account.
func (uc *DisburseUseCase) Execute(ctx context.Context, cmd DisburseCommand) (*DisburseResult, error) {
	key, err := domain.ParseIntentionKey(cmd.Key)
	if err != nil {
		return nil, err
	}
	vault, err := domain.ParseTreasuryVault(cmd.Vault)
	if err != nil {
		return nil, err
	}
	beneficiary, err := domain.ParseIntentionActor(cmd.Beneficiary)
	if err != nil {
		return nil, err
	}
	purpose, err := domain.ParseDisbursementPurpose(cmd.Purpose)
	if err != nil {
		return nil, err
	}
	amount, err := domain.NewMilliInk(cmd.Millis)
	if err != nil {
		return nil, err
	}
	if amount.IsZero() {
		return nil, domain.ErrInvalidGrant
	}
	approverOne, err := domain.ParseIntentionActor(cmd.ApproverOne)
	if err != nil {
		return nil, err
	}
	approverTwo, err := domain.ParseIntentionActor(cmd.ApproverTwo)
	if err != nil {
		return nil, err
	}
	if approverOne == approverTwo {
		return nil, domain.ErrInvalidDisbursement
	}
	if beneficiary == approverOne || beneficiary == approverTwo {
		return nil, domain.ErrInvalidDisbursement
	}
	return uc.disbursements.Disburse(ctx, DisburseRequest{
		Key: key, Vault: vault, Beneficiary: beneficiary, Purpose: purpose,
		Amount: amount, ApproverOne: approverOne, ApproverTwo: approverTwo,
	})
}
