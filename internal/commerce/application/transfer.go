package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
)

// TransferCommand names one voluntary transfer: the caller operation
// token, the business kind, the payer, the payee, the exact integer
// amount and the explicit consent reference. The payer is the
// authenticated caller: there is no separate actor field, so a
// caller can never debit another account by naming it elsewhere.
type TransferCommand struct {
	Key         string
	Kind        string
	Payer       string
	Payee       string
	AmountMilli int64
	ConsentRef  string
}

// TransferRequest is the validated transfer for the repository
// port: the token, the sealed kind, both parties, the amount, the
// consent reference and the payload seal telling a replay from a
// conflict. Custody mapping and ledger checks resolve inside the
// repository transaction.
type TransferRequest struct {
	Key         string
	Kind        domain.TransferKind
	Payer       string
	Payee       string
	AmountMilli int64
	ConsentRef  string
	PayloadHash string
}

// TransferResult is the settled transfer: the stored row, the
// journal transfer moving the exact amount and whether the call
// replayed the original settlement.
type TransferResult struct {
	TransferRowID string
	TransferID    string
	AmountMilli   int64
	Replayed      bool
}

// TransferRepository settles voluntary transfers with their ledger
// legs in one transaction: the commerce row and the legs commit
// together, or nothing is stored at all.
type TransferRepository interface {
	// Transfer settles one intention keyed idempotently by payer
	// and token. Replays resolve the original settlement
	// untouched; divergent terms under one key conflict instead of
	// paying twice; ineligible parties and uncovered balances
	// refuse without writing.
	Transfer(ctx context.Context, request TransferRequest) (*TransferResult, error)
}

// TransferUseCase settles one voluntary transfer between eligible
// accounts. It is an internal operation: no public surface calls it
// before activation.
type TransferUseCase struct {
	transfers TransferRepository
}

// NewTransferUseCase creates an instance of TransferUseCase,
// refusing incomplete composition.
func NewTransferUseCase(transfers TransferRepository) (*TransferUseCase, error) {
	if transfers == nil {
		return nil, ErrInvalidTransferConfig
	}
	return &TransferUseCase{transfers: transfers}, nil
}

// transferPayload binds the settled terms to the intention: any
// difference in kind, parties, amount or consent produces another
// seal, so reuse of a key with changed terms can never pass as a
// replay.
func transferPayload(kind domain.TransferKind, payer, payee string, amount int64, consent string) string {
	canonical := fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%s",
		kind.String(), payer, payee, amount, consent)
	digest := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(digest[:])
}

// Execute validates the transfer envelope and settles it. The
// token, the closed kind, both parties, a positive amount and an
// explicit consent reference stop malformed calls before any store
// is touched; self-payments refuse here, and every eligibility,
// limit and ledger fact resolves inside the repository.
func (uc *TransferUseCase) Execute(ctx context.Context, cmd TransferCommand) (*TransferResult, error) {
	if strings.TrimSpace(cmd.Key) == "" || strings.TrimSpace(cmd.Key) != cmd.Key {
		return nil, domain.ErrInvalidIntention
	}
	kind, err := domain.ParseTransferKind(cmd.Kind)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cmd.Payer) == "" || strings.TrimSpace(cmd.Payer) != cmd.Payer {
		return nil, domain.ErrInvalidIntention
	}
	if strings.TrimSpace(cmd.Payee) == "" || strings.TrimSpace(cmd.Payee) != cmd.Payee {
		return nil, domain.ErrInvalidIntention
	}
	if cmd.Payer == cmd.Payee {
		return nil, domain.ErrSelfTransfer
	}
	if cmd.AmountMilli <= 0 {
		return nil, domain.ErrInvalidIntention
	}
	if strings.TrimSpace(cmd.ConsentRef) == "" || strings.TrimSpace(cmd.ConsentRef) != cmd.ConsentRef {
		return nil, domain.ErrConsentRequired
	}
	return uc.transfers.Transfer(ctx, TransferRequest{
		Key: cmd.Key, Kind: kind, Payer: cmd.Payer, Payee: cmd.Payee,
		AmountMilli: cmd.AmountMilli, ConsentRef: strings.TrimSpace(cmd.ConsentRef),
		PayloadHash: transferPayload(kind, cmd.Payer, cmd.Payee, cmd.AmountMilli, strings.TrimSpace(cmd.ConsentRef)),
	})
}
