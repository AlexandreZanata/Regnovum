package application

import (
	"context"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/AlexandreZanata/Regnovum/internal/metering/domain"
)

// maxRefundReasonRunes bounds a refund reason: long enough for a
// human cause, short enough to stay out of log and index abuse.
const maxRefundReasonRunes = 280

// RefundCommand names one publication compensation: the caller
// operation token, the owning account, the original publication key
// and the human cause. Amount, endpoints and posting time resolve
// from the settled original and the database clock, never from the
// caller: a correction is always current, never backdated.
type RefundCommand struct {
	Key         string
	Account     string
	OriginalKey string
	Reason      string
}

// RefundRequest is the validated compensation for the repository
// port: the token, the owner, the original intention and the cause.
type RefundRequest struct {
	Key         domain.PublishKey
	Account     string
	OriginalKey domain.PublishKey
	Reason      string
}

// RefundResult is the settled compensation: the stored row, the
// reversing transfer, the compensated publication, the exact
// original amount, the database posted instant and whether the call
// replayed the original compensation.
type RefundResult struct {
	RefundID    string
	TransferID  string
	OriginalID  string
	AmountMilli int64
	PostedAt    time.Time
	Replayed    bool
}

// RefundRepository settles publication compensations with their
// reversing legs in one transaction: the refund row and the legs
// commit together, or nothing is stored at all.
type RefundRepository interface {
	// Refund compensates one settled publication in full keyed
	// idempotently by account and token. Replays resolve the
	// original compensation untouched; a second compensation of
	// the same publication refuses; unknown causes refuse without
	// writing.
	Refund(ctx context.Context, request RefundRequest) (*RefundResult, error)
}

// RefundUseCase compensates one erroneous publication in full. It is
// an internal operation: no public surface calls it.
type RefundUseCase struct {
	refunds RefundRepository
}

// NewRefundUseCase creates an instance of RefundUseCase, refusing
// incomplete composition.
func NewRefundUseCase(refunds RefundRepository) (*RefundUseCase, error) {
	if refunds == nil {
		return nil, ErrInvalidPublishConfig
	}
	return &RefundUseCase{refunds: refunds}, nil
}

// parseRefundReason validates the human cause: exact match, no
// control characters, bounded length.
func parseRefundReason(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", domain.ErrInvalidQuote
	}
	if utf8.RuneCountInString(raw) > maxRefundReasonRunes {
		return "", domain.ErrInvalidQuote
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", domain.ErrInvalidQuote
		}
	}
	return raw, nil
}

// Execute validates the compensation envelope and settles it. The
// token, the owner, the original key and the cause stop malformed
// calls before any store is touched; the amount, the reversed
// endpoints and the posting time resolve inside the repository
// transaction from the settled original and the database clock.
func (uc *RefundUseCase) Execute(ctx context.Context, cmd RefundCommand) (*RefundResult, error) {
	key, err := domain.ParsePublishKey(cmd.Key)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cmd.Account) == "" || strings.TrimSpace(cmd.Account) != cmd.Account {
		return nil, domain.ErrInvalidQuote
	}
	original, err := domain.ParsePublishKey(cmd.OriginalKey)
	if err != nil {
		return nil, err
	}
	reason, err := parseRefundReason(cmd.Reason)
	if err != nil {
		return nil, err
	}
	return uc.refunds.Refund(ctx, RefundRequest{
		Key: key, Account: cmd.Account, OriginalKey: original, Reason: reason,
	})
}
