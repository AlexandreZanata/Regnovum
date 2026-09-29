package application

import "context"

import "time"

// Leg is one journal leg of a settlement or compensation:
// direction with its custody and exact amount.
type Leg struct {
	Direction string
	Kind      string
	Label     string
	Amount    int64
}

// Publication is the stored settlement behind one receipt: the
// sealed terms with the journal transfer and the database posted
// instant.
type Publication struct {
	ID           string
	IntentionKey string
	Account      string
	Service      string
	Version      int
	Units        int
	AmountMilli  int64
	ContentHash  string
	QuoteHash    string
	TransferID   string
	PostedAt     time.Time
}

// Refund is the stored compensation behind one receipt, when the
// publication was compensated.
type Refund struct {
	ID         string
	Key        string
	Amount     int64
	TransferID string
	PostedAt   time.Time
}

// Receipt binds one publication to its legs and its compensation,
// if any: the full evidence of one charge.
type Receipt struct {
	Publication Publication
	Legs        []Leg
	Refund      *Refund
}

// StatementEntry is one publication line of the owner extract.
type StatementEntry struct {
	Publication Publication
	Refunded    bool
}

// Statement is the owner extract: publication lines in reverse
// posting order with the current citizen balance. Balances derive
// from the journal, never from stored sums.
type Statement struct {
	Entries      []StatementEntry
	BalanceMilli int64
}

// ReceiptsRepository resolves owned receipts and extracts. Reads
// never mutate: another owner's rows and unknown ids resolve to
// absence, never to a leak.
type ReceiptsRepository interface {
	// GetReceipt resolves one owned publication with its legs and
	// its compensation, if any.
	GetReceipt(ctx context.Context, account, id string) (*Receipt, error)
	// Statement resolves the owner extract with the current
	// balance.
	Statement(ctx context.Context, account string, limit int) (*Statement, error)
}
