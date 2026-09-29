package application

import (
	"context"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
)

// DisguiseReviewView is one stored disguise review: the flagged gift
// with the closed reason, the minimized evidence hash, the reporter
// and the derived lifecycle status. Confirmations carry the
// contractual basis, the trail hash, the explicit charge owed and
// the appeal window; dismissals carry only the appeal window. No
// view ever moves value: flagging, contesting and resolving record,
// they never debit.
type DisguiseReviewView struct {
	ReviewID     string
	TransferKey  string
	Payer        string
	Payee        string
	AmountMill   int64
	Reason       domain.DisguiseReason
	EvidenceHash string
	Reporter     string
	Status       domain.DisguiseStatus
	Basis        string
	TrailHash    string
	ChargeMill   int64
	AppealUntil  time.Time
	PostedAt     time.Time
	Replayed     bool
}

// FlagDisguiseRequest is the validated signal for the repository
// port: the caller review token, the flagged gift scoped by payer
// and transfer key, the closed reason, the minimized evidence hash
// and the reporter. Amount, parties and kind resolve server-side
// from the settled gift, so a forged request can neither invent a
// transfer nor tax a present.
type FlagDisguiseRequest struct {
	ReviewKey    string
	TransferKey  string
	Payer        string
	Reason       string
	EvidenceHash string
	Reporter     string
}

// ContestDisguiseRequest names one challenge by the gift parties.
type ContestDisguiseRequest struct {
	ReviewKey   string
	TransferKey string
	Payer       string
	By          string
}

// ResolveDisguiseRequest names one competent outcome with the appeal
// window both outcomes owe and, for confirmations, the contractual
// basis and trail.
type ResolveDisguiseRequest struct {
	ReviewKey   string
	TransferKey string
	Payer       string
	Decision    string
	Basis       string
	TrailHash   string
	AppealUntil time.Time
	Now         time.Time
}

// DisguiseReviewRepository reviews suspected disguised trade with
// the rows it writes in one transaction: the flag, the contest and
// the terminal step commit with nothing else, or nothing is stored
// at all. No method moves ledger legs.
type DisguiseReviewRepository interface {
	// FlagDisguise signals one settled gift for review, keyed
	// idempotently by payer, transfer and review token. Replays
	// resolve the original flag untouched; divergent terms under
	// one key conflict; non-gifts refuse without writing.
	FlagDisguise(ctx context.Context, request FlagDisguiseRequest) (*DisguiseReviewView, error)
	// ContestDisguise records one payer-or-payee challenge on a
	// flagged review. Strangers refuse; terminal reviews never
	// reopen.
	ContestDisguise(ctx context.Context, request ContestDisguiseRequest) (*DisguiseReviewView, error)
	// ResolveDisguise settles one flagged or contested review
	// exactly once: dismiss clears a false positive with no
	// movement, confirm records the act with basis, trail,
	// explicit charge and appeal window, still with no debit.
	ResolveDisguise(ctx context.Context, request ResolveDisguiseRequest) (*DisguiseReviewView, error)
}

// FlagDisguiseUseCase signals one settled gift for review. It is an
// internal operation: no public surface calls it before activation.
type FlagDisguiseUseCase struct {
	reviews DisguiseReviewRepository
}

// NewFlagDisguiseUseCase creates an instance of FlagDisguiseUseCase,
// refusing incomplete composition.
func NewFlagDisguiseUseCase(reviews DisguiseReviewRepository) (*FlagDisguiseUseCase, error) {
	if reviews == nil {
		return nil, ErrInvalidTransferConfig
	}
	return &FlagDisguiseUseCase{reviews: reviews}, nil
}

// FlagDisguiseCommand names one signal: the caller review token, the
// flagged gift, the closed reason, the minimized evidence hash and
// the reporter.
type FlagDisguiseCommand struct {
	ReviewKey    string
	TransferKey  string
	Payer        string
	Reason       string
	EvidenceHash string
	Reporter     string
}

// Execute validates the signal envelope and flags it. Blank tokens,
// unknown reasons and non-hash evidence stop malformed calls before
// any store is touched; the gift itself resolves inside the
// repository transaction.
func (uc *FlagDisguiseUseCase) Execute(ctx context.Context, cmd FlagDisguiseCommand) (*DisguiseReviewView, error) {
	if strings.TrimSpace(cmd.ReviewKey) == "" || strings.TrimSpace(cmd.ReviewKey) != cmd.ReviewKey {
		return nil, domain.ErrInvalidDisguise
	}
	if strings.TrimSpace(cmd.TransferKey) == "" || strings.TrimSpace(cmd.TransferKey) != cmd.TransferKey {
		return nil, domain.ErrInvalidDisguise
	}
	if strings.TrimSpace(cmd.Payer) == "" || strings.TrimSpace(cmd.Payer) != cmd.Payer {
		return nil, domain.ErrInvalidDisguise
	}
	if _, err := domain.ParseDisguiseReason(cmd.Reason); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cmd.EvidenceHash) == "" {
		return nil, domain.ErrInvalidDisguise
	}
	if strings.TrimSpace(cmd.Reporter) == "" || strings.TrimSpace(cmd.Reporter) != cmd.Reporter {
		return nil, domain.ErrInvalidDisguise
	}
	return uc.reviews.FlagDisguise(ctx, FlagDisguiseRequest(cmd))
}

// ContestDisguiseUseCase records one party challenge.
type ContestDisguiseUseCase struct {
	reviews DisguiseReviewRepository
}

// NewContestDisguiseUseCase creates an instance of
// ContestDisguiseUseCase, refusing incomplete composition.
func NewContestDisguiseUseCase(reviews DisguiseReviewRepository) (*ContestDisguiseUseCase, error) {
	if reviews == nil {
		return nil, ErrInvalidTransferConfig
	}
	return &ContestDisguiseUseCase{reviews: reviews}, nil
}

// ContestDisguiseCommand names one challenge: the flagged gift and
// who speaks for it.
type ContestDisguiseCommand struct {
	ReviewKey   string
	TransferKey string
	Payer       string
	By          string
}

// Execute validates the challenge envelope and records it.
func (uc *ContestDisguiseUseCase) Execute(ctx context.Context, cmd ContestDisguiseCommand) (*DisguiseReviewView, error) {
	if strings.TrimSpace(cmd.ReviewKey) == "" || strings.TrimSpace(cmd.ReviewKey) != cmd.ReviewKey {
		return nil, domain.ErrInvalidDisguise
	}
	if strings.TrimSpace(cmd.TransferKey) == "" || strings.TrimSpace(cmd.TransferKey) != cmd.TransferKey {
		return nil, domain.ErrInvalidDisguise
	}
	if strings.TrimSpace(cmd.Payer) == "" || strings.TrimSpace(cmd.Payer) != cmd.Payer {
		return nil, domain.ErrInvalidDisguise
	}
	if strings.TrimSpace(cmd.By) == "" || strings.TrimSpace(cmd.By) != cmd.By {
		return nil, domain.ErrDisguiseNotParty
	}
	return uc.reviews.ContestDisguise(ctx, ContestDisguiseRequest(cmd))
}

// ResolveDisguiseUseCase settles one review exactly once.
type ResolveDisguiseUseCase struct {
	reviews DisguiseReviewRepository
}

// NewResolveDisguiseUseCase creates an instance of
// ResolveDisguiseUseCase, refusing incomplete composition.
func NewResolveDisguiseUseCase(reviews DisguiseReviewRepository) (*ResolveDisguiseUseCase, error) {
	if reviews == nil {
		return nil, ErrInvalidTransferConfig
	}
	return &ResolveDisguiseUseCase{reviews: reviews}, nil
}

// ResolveDisguiseCommand names one outcome: the flagged gift, the
// decision, the appeal window and, for confirmations, the basis and
// trail.
type ResolveDisguiseCommand struct {
	ReviewKey   string
	TransferKey string
	Payer       string
	Decision    string
	Basis       string
	TrailHash   string
	AppealUntil time.Time
	Now         time.Time
}

// Execute validates the outcome envelope and settles it. The
// decision parses before any store is touched; basis, trail and
// appeal resolve inside the repository with the sealed flag.
func (uc *ResolveDisguiseUseCase) Execute(ctx context.Context, cmd ResolveDisguiseCommand) (*DisguiseReviewView, error) {
	if strings.TrimSpace(cmd.ReviewKey) == "" || strings.TrimSpace(cmd.ReviewKey) != cmd.ReviewKey {
		return nil, domain.ErrInvalidDisguise
	}
	if strings.TrimSpace(cmd.TransferKey) == "" || strings.TrimSpace(cmd.TransferKey) != cmd.TransferKey {
		return nil, domain.ErrInvalidDisguise
	}
	if strings.TrimSpace(cmd.Payer) == "" || strings.TrimSpace(cmd.Payer) != cmd.Payer {
		return nil, domain.ErrInvalidDisguise
	}
	if _, err := domain.ParseDisguiseDecision(cmd.Decision); err != nil {
		return nil, err
	}
	if cmd.Now.IsZero() || cmd.AppealUntil.IsZero() || !cmd.AppealUntil.After(cmd.Now) {
		return nil, domain.ErrInvalidDisguise
	}
	return uc.reviews.ResolveDisguise(ctx, ResolveDisguiseRequest(cmd))
}
