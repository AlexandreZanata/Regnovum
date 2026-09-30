package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
)

// RefundLine is one proportional compensation behind a trade receipt,
// when the liquidated payment was refunded in whole or in part.
type RefundLine struct {
	RefundKey      string
	AmountMill     int64
	TitheReversal  int64
	ProviderShare  int64
	ObligationMill int64
	PostedAt       time.Time
}

// TradeReceipt is the stored formal trade behind one private receipt:
// the sealed gross with the floor(10%) tithe and the provider net,
// the derived escrow status, the instants and the identifiers of the
// participant side. Amounts are canonical integers in milliINK;
// counterparty is the other side of the same contract, never a third
// party. Unreleased escrow carries zero tithe and net: gifts never
// bear tithe and unreleased funds never split.
type TradeReceipt struct {
	ContractID           string
	ContractKey          string
	Role                 string
	Counterparty         string
	Object               string
	GrossMilli           int64
	TitheMilli           int64
	NetMilli             int64
	Status               domain.ContractStatus
	ExpiresAt            time.Time
	PostedAt             time.Time
	SettledAt            *time.Time
	EscrowTransferID     string
	SettlementTransferID *string
	Refunds              []RefundLine
	RefundedMilli        int64
}

// TradeStatementEntry is one formal trade line of the participant
// extract.
type TradeStatementEntry struct {
	ContractID  string
	ContractKey string
	Role        string
	GrossMilli  int64
	Status      domain.ContractStatus
	PostedAt    time.Time
}

// TradeStatement is the participant extract: owned trade lines in
// reverse posting order. Lines derive from the journal-backed
// contracts, never from stored sums; strangers' contracts never
// appear.
type TradeStatement struct {
	Entries []TradeStatementEntry
}

// TradeReceiptsRepository resolves owned trade receipts and extracts.
// Reads never mutate: another participant's contracts and unknown ids
// resolve to absence, never to a leak.
type TradeReceiptsRepository interface {
	// GetReceipt resolves one owned formal trade with its tithe
	// split and its compensations, if any.
	GetReceipt(ctx context.Context, account, id string) (*TradeReceipt, error)
	// Statement resolves the participant extract with owned lines
	// in reverse posting order.
	Statement(ctx context.Context, account string, limit int) (*TradeStatement, error)
}
