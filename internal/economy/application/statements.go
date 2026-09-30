package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// statementPageSize bounds one statement read: small enough to stay
// interactive, large enough to walk a journal in few pages.
const (
	minStatementLimit = 1
	maxStatementLimit = 100
)

// StatementCursor continues a paginated read: the journal instant and id
// after which the next page starts. The zero cursor opens the journal.
type StatementCursor struct {
	AfterAt time.Time
	AfterID string
}

// Encode renders the cursor for transport: empty for the journal start,
// otherwise instant and id joined once, so parsing stays total.
func (c StatementCursor) Encode() string {
	if c.AfterID == "" {
		return ""
	}
	return c.AfterAt.UTC().Format(time.RFC3339Nano) + "|" + c.AfterID
}

// ParseStatementCursor reads a transported cursor. Empty opens the
// journal; anything else must carry both halves, or the page is refused
// instead of guessed.
func ParseStatementCursor(raw string) (StatementCursor, error) {
	if raw == "" {
		return StatementCursor{}, nil
	}
	instant, id, found := strings.Cut(raw, "|")
	if !found || instant == "" || id == "" {
		return StatementCursor{}, domain.ErrInvalidStatement
	}
	at, err := time.Parse(time.RFC3339Nano, instant)
	if err != nil {
		return StatementCursor{}, domain.ErrInvalidStatement
	}
	return StatementCursor{AfterAt: at, AfterID: id}, nil
}

// StatementCommand reads one private custody statement page: the custody
// by kind and label, the caller the journal must belong to, the page size
// and the cursor to continue from.
type StatementCommand struct {
	Season          string
	Kind            string
	Label           string
	CallerAccountID string
	Limit           int
	Cursor          string
}

// StatementEntry is one journal leg in statement order.
type StatementEntry struct {
	EntryID    string
	TransferID string
	Direction  string
	Amount     domain.MilliInk
	RecordedAt time.Time
}

// StatementPage is one verified page: the custody balance derived from
// the whole journal (not from the page), the entries in journal order and
// the cursor for the next page, empty when the journal ends here.
type StatementPage struct {
	CustodyID  string
	Balance    domain.MilliInk
	Entries    []StatementEntry
	NextCursor string
}

// StatementRequest is a validated private read for the port.
type StatementRequest struct {
	Season          domain.SeasonKey
	Kind            domain.CustodyKind
	Label           string
	CallerAccountID string
	Limit           int
	Cursor          StatementCursor
}

// StatementRepository reads private statements and rebuilds the book.
type StatementRepository interface {
	// ReadStatement returns one page of the custody journal with the
	// balance derived from all its legs. Other holders, system
	// custodies and inactive owners are refused before any leg is read.
	ReadStatement(ctx context.Context, request StatementRequest) (*StatementPage, error)
	// RebuildAll re-derives every custody projection of one book
	// from the journal without editing it, returning one checkpoint
	// per custody.
	RebuildAll(ctx context.Context, season domain.SeasonKey) ([]CustodyProjection, error)
}

// ReadStatementUseCase validates and serves one private statement page.
// It is an internal operation: no public surface calls it.
type ReadStatementUseCase struct {
	statements StatementRepository
}

// NewReadStatementUseCase creates an instance of ReadStatementUseCase.
func NewReadStatementUseCase(statements StatementRepository) *ReadStatementUseCase {
	return &ReadStatementUseCase{statements: statements}
}

// Execute validates the private read and serves exactly one page.
func (uc *ReadStatementUseCase) Execute(ctx context.Context, cmd StatementCommand) (*StatementPage, error) {
	kind, err := domain.ParseCustodyKind(cmd.Kind)
	if err != nil {
		return nil, err
	}
	if cmd.Label == "" || cmd.CallerAccountID == "" {
		return nil, domain.ErrInvalidStatement
	}
	if cmd.Limit < minStatementLimit || cmd.Limit > maxStatementLimit {
		return nil, domain.ErrInvalidStatement
	}
	cursor, err := ParseStatementCursor(cmd.Cursor)
	if err != nil {
		return nil, err
	}
	season, err := domain.ParseSeasonKey(cmd.Season)
	if err != nil {
		return nil, err
	}
	return uc.statements.ReadStatement(ctx, StatementRequest{
		Season:          season,
		Kind:            kind,
		Label:           cmd.Label,
		CallerAccountID: cmd.CallerAccountID,
		Limit:           cmd.Limit,
		Cursor:          cursor,
	})
}

// CustodyProjection is the rebuilt state of one custody: its derived
// balance, leg count, journal tip and the digest binding them.
type CustodyProjection struct {
	CustodyID string
	Kind      domain.CustodyKind
	Label     string
	Balance   domain.MilliInk
	Entries   int64
	LastEntry string
	Digest    string
}

// SealProjection binds a rebuilt projection: the digest covers custody,
// balance, leg count and journal tip, so any tampering with a stored
// copy is detectable by recomputation.
func SealProjection(custodyID string, kind domain.CustodyKind, label string, balance domain.MilliInk, entries int64, lastEntry string) CustodyProjection {
	projection := CustodyProjection{
		CustodyID: custodyID,
		Kind:      kind,
		Label:     label,
		Balance:   balance,
		Entries:   entries,
		LastEntry: lastEntry,
	}
	projection.Digest = projection.digest()
	return projection
}

// Verify recomputes the digest and refuses a projection whose fields no
// longer agree: a checkpoint that fails here was edited or mixed up.
func (p CustodyProjection) Verify() error {
	if p.Digest == "" || p.digest() != p.Digest {
		return domain.ErrInvalidStatement
	}
	return nil
}

func (p CustodyProjection) digest() string {
	canonical := fmt.Sprintf("%s\x00%d\x00%d\x00%s", p.CustodyID, p.Balance.Millis(), p.Entries, p.LastEntry)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// RebuildUseCase re-derives every custody projection from the journal.
// It is an internal operation: no public surface calls it.
type RebuildUseCase struct {
	statements StatementRepository
}

// NewRebuildUseCase creates an instance of RebuildUseCase.
func NewRebuildUseCase(statements StatementRepository) *RebuildUseCase {
	return &RebuildUseCase{statements: statements}
}

// RebuildCommand names the season book one rebuild re-derives.
type RebuildCommand struct {
	Season string
}

// Execute rebuilds one book without editing the journal.
func (uc *RebuildUseCase) Execute(ctx context.Context, cmd RebuildCommand) ([]CustodyProjection, error) {
	season, err := domain.ParseSeasonKey(cmd.Season)
	if err != nil {
		return nil, err
	}
	return uc.statements.RebuildAll(ctx, season)
}
