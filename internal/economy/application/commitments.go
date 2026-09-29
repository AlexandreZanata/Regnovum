package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// CommitFundsCommand earmarks Treasury vault funds for one named
// obligation: the vault, the obligation purpose in free text, the exact
// amount and the deadline after which the commitment may expire.
// Future phases name their classes here — accepted sales, approved
// Crumbs, compensations, due payments — but no class is inferred: the
// purpose travels verbatim and every amount stays explicit.
type CommitFundsCommand struct {
	Vault     string
	Purpose   string
	Millis    int64
	ExpiresAt time.Time
}

// CommitFundsUseCase locks Treasury vault funds for one obligation
// through the holds machinery, with the owner fixed to the Treasury:
// user, escrow and third-party funds can never be committed as
// Treasury obligations. It is an internal operation: no public surface
// calls it.
type CommitFundsUseCase struct {
	holds HoldsRepository
	clock Clock
}

// NewCommitFundsUseCase creates an instance of CommitFundsUseCase.
func NewCommitFundsUseCase(holds HoldsRepository, clock Clock) *CommitFundsUseCase {
	return &CommitFundsUseCase{holds: holds, clock: clock}
}

// Execute validates the commitment and locks the amount out of the
// named Treasury vault. Only the closed vault vocabulary passes, so a
// holder account, an escrow label or a sixth spelling never reaches
// the journal.
func (uc *CommitFundsUseCase) Execute(ctx context.Context, cmd CommitFundsCommand) (*HoldView, error) {
	vault, err := domain.ParseTreasuryVault(cmd.Vault)
	if err != nil {
		return nil, err
	}
	purpose, err := domain.ParseHoldPurpose(cmd.Purpose)
	if err != nil {
		return nil, err
	}
	amount, err := domain.NewMilliInk(cmd.Millis)
	if err != nil {
		return nil, err
	}
	if amount.IsZero() {
		return nil, domain.ErrInvalidHold
	}
	if cmd.ExpiresAt.IsZero() || !cmd.ExpiresAt.After(uc.clock.Now()) {
		return nil, domain.ErrInvalidHold
	}
	return uc.holds.Reserve(ctx, domain.CustodyTreasury, vault.String(), purpose, amount, cmd.ExpiresAt.UTC())
}
