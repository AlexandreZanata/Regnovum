// Queue implements the restricted triage queue (P13-T07): moderators list
// cases with an optional status filter through opaque server-signed
// cursors. Queue items carry triage routing only: target, lifecycle,
// priority and claim holder. Reporter context, justifications and appeal
// contexts never enter the queue projection.
package application

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
)

// Queue pagination bounds fixed by the API conventions (limit defaults to
// 20, maximum 100).
const (
	DefaultQueueLimit = 20
	MaxQueueLimit     = 100
)

// queueCursorVersion prefixes every cursor payload so a future layout can
// be introduced without silently misreading old cursors.
const queueCursorVersion = "v1"

// minQueueCursorSecretLength is the minimum HMAC key size accepted for
// queue cursor signing (256 bits).
const minQueueCursorSecretLength = 32

// QueueCursorCodec encodes and verifies opaque, server-signed triage
// cursors: clients may pass them back verbatim, but a forged or corrupted
// cursor is rejected instead of being interpreted.
type QueueCursorCodec struct {
	secret []byte
}

// NewQueueCursorCodec builds the codec from the configured signing secret.
// Secrets shorter than 256 bits are refused.
func NewQueueCursorCodec(secret []byte) (*QueueCursorCodec, error) {
	if len(secret) < minQueueCursorSecretLength {
		return nil, ErrWeakQueueCursorSecret
	}
	copied := make([]byte, len(secret))
	copy(copied, secret)
	return &QueueCursorCodec{secret: copied}, nil
}

// QueuePosition is the decoded keyset position: the last entry already
// delivered to the caller.
type QueuePosition struct {
	CreatedAt time.Time
	CaseID    string
}

// Encode renders the signed cursor of the last delivered entry.
func (c *QueueCursorCodec) Encode(createdAt time.Time, caseID string) string {
	payload := strings.Join([]string{
		queueCursorVersion,
		createdAt.UTC().Format(time.RFC3339Nano),
		caseID,
	}, "|")

	signature := hmac.New(sha256.New, c.secret)
	signature.Write([]byte(payload))

	return base64.RawURLEncoding.EncodeToString([]byte(payload)) +
		"." + base64.RawURLEncoding.EncodeToString(signature.Sum(nil))
}

// Decode verifies the signature and decodes the keyset position. An empty
// cursor yields a nil position (first page); malformed, forged or
// version-mismatched cursors fail with ErrInvalidCursor instead of being
// reflected back.
func (c *QueueCursorCodec) Decode(raw string) (*QueuePosition, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}

	parts := strings.Split(trimmed, ".")
	if len(parts) != 2 {
		return nil, ErrInvalidCursor
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrInvalidCursor
	}
	signatureBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidCursor
	}

	expected := hmac.New(sha256.New, c.secret)
	expected.Write(payloadBytes)
	if !hmac.Equal(signatureBytes, expected.Sum(nil)) {
		return nil, ErrInvalidCursor
	}

	fields := strings.Split(string(payloadBytes), "|")
	if len(fields) != 3 || fields[0] != queueCursorVersion {
		return nil, ErrInvalidCursor
	}

	createdAt, err := time.Parse(time.RFC3339Nano, fields[1])
	if err != nil {
		return nil, ErrInvalidCursor
	}

	caseID := fields[2]
	if caseID == "" {
		return nil, ErrInvalidCursor
	}
	for i := 0; i < len(caseID); i++ {
		if caseID[i] < 0x21 || caseID[i] > 0x7e {
			return nil, ErrInvalidCursor
		}
	}

	return &QueuePosition{CreatedAt: createdAt.UTC(), CaseID: caseID}, nil
}

// QueueItem is one triage routing entry: target, lifecycle, priority and
// claim holder. Restricted evidence never enters this projection.
type QueueItem struct {
	CaseID    string
	Target    domain.TargetType
	TargetID  string
	Status    CaseStatus
	Priority  string
	CreatedAt time.Time
	ClaimedBy domain.AccountID
}

// CaseQueuePage is one page of the triage queue.
type CaseQueuePage struct {
	Items      []QueueItem
	NextCursor string
}

// CaseQueueRepository lists cases for triage. It reads routing only.
type CaseQueueRepository interface {
	// ListQueuePage returns up to limit entries strictly older than the
	// position (or the first page when after is nil), newest first. An
	// empty status filter lists every lifecycle.
	ListQueuePage(ctx context.Context, status string, after *QueuePosition, limit int) ([]QueueItem, error)
}

// SessionAgeDirectory resolves how long ago the session completed full
// authentication. The HTTP layer feeds the age into claim and decide
// use-cases so step-up freshness is evaluated on a server-observed
// instant, never on a client claim.
type SessionAgeDirectory interface {
	// SessionAgeAt returns how long ago the session authenticated, as of
	// now. Unknown sessions deny distinctly.
	SessionAgeAt(ctx context.Context, sessionID string, now time.Time) (time.Duration, error)

	// MFAVerifiedAt reports when the session last presented a second
	// factor, and whether it ever did. It is part of this port because
	// administrative capability is a property of the session and not only
	// of the account: an assignment held by an account whose session never
	// presented the factor is not administrative access (P16-T05).
	MFAVerifiedAt(ctx context.Context, sessionID string) (time.Time, bool, error)
}

// GetCaseQueueUseCase answers one triage queue page for an active
// moderator. Any active assignment may triage; fine-grained sanction
// authorization stays with claim and decide.
type GetCaseQueueUseCase struct {
	cases CaseQueueRepository
	roles RoleRepository
}

// NewGetCaseQueueUseCase builds the use case, refusing incomplete
// composition.
func NewGetCaseQueueUseCase(cases CaseQueueRepository, roles RoleRepository) (*GetCaseQueueUseCase, error) {
	if cases == nil || roles == nil {
		return nil, ErrInvalidQueueConfig
	}
	return &GetCaseQueueUseCase{cases: cases, roles: roles}, nil
}

// Execute validates the viewer, the filter and the cursor, then lists one
// page. Unknown viewers deny; revoked assignments deny even inside an
// otherwise active session.
func (uc *GetCaseQueueUseCase) Execute(ctx context.Context, viewer domain.AccountID, status, cursor string, limit int, codec *QueueCursorCodec) (*CaseQueuePage, error) {
	if viewer.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}
	if codec == nil {
		return nil, ErrInvalidQueueConfig
	}
	if status != "" && status != "open" && status != "under_review" && status != "decided" && status != "closed" {
		return nil, ErrInvalidQueueFilter
	}
	if limit <= 0 {
		limit = DefaultQueueLimit
	}
	if limit > MaxQueueLimit {
		limit = MaxQueueLimit
	}

	assignment, err := uc.roles.AssignmentFor(ctx, viewer)
	if err != nil {
		return nil, err
	}
	if assignment == nil {
		return nil, ErrNotAuthorized
	}
	if assignment.Revoked {
		return nil, ErrRoleRevoked
	}

	after, err := codec.Decode(cursor)
	if err != nil {
		return nil, err
	}

	items, err := uc.cases.ListQueuePage(ctx, status, after, limit+1)
	if err != nil {
		return nil, err
	}

	page := &CaseQueuePage{Items: make([]QueueItem, 0, limit)}
	for i, item := range items {
		if i == limit {
			// The lookahead row proves a next page exists; the cursor
			// positions after the last delivered entry.
			last := page.Items[len(page.Items)-1]
			page.NextCursor = codec.Encode(last.CreatedAt, last.CaseID)
			break
		}
		page.Items = append(page.Items, item)
	}
	return page, nil
}
