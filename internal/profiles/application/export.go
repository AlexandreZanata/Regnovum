package application

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// PersonalExportRecord is the owner-scoped state of one export job. The
// download token hash never leaves the persistence boundary: callers only
// ever compare against it.
type PersonalExportRecord struct {
	ID                string
	AccountID         string
	Status            domain.ExportStatus
	RequestedAt       time.Time
	GeneratedAt       *time.Time
	ExpiresAt         *time.Time
	DownloadCount     int32
	MaxDownloads      int32
	DownloadTokenHash []byte
}

// PersonalExportRequest is one creation or replay of the account's active
// export, with the freshly rotated download capability.
type PersonalExportRequest struct {
	AccountID         domain.AccountID
	DownloadTokenHash []byte
	MaxDownloads      int32
	RequestedAt       time.Time
}

// PersonalExportReady carries the generated document once. The content hash
// covers the exact bytes the encoder produced and the adapter will serve.
type PersonalExportReady struct {
	ExportID       string
	Document       []byte
	DocumentSHA256 string
	GeneratedAt    time.Time
	ExpiresAt      time.Time
}

// PersonalExportEncoder renders the versioned export document to its
// machine-readable bytes. Serialization is an outbound concern, so the
// application declares the port and an adapter implements it.
type PersonalExportEncoder interface {
	EncodePersonalExport(document PersonalExportDocument) ([]byte, error)
}

// PersonalExportConsumption is one atomic download attempt: it must match
// the owner, the token and the availability window in a single statement.
type PersonalExportConsumption struct {
	AccountID         domain.AccountID
	ExportID          string
	DownloadTokenHash []byte
	ConsumedAt        time.Time
}

// PersonalExportDownload is the served document and its content hash.
type PersonalExportDownload struct {
	Document       []byte
	DocumentSHA256 string
}

// PersonalExportDataRepository reads every personal category of one
// account as approved read-only projections. Unknown accounts answer
// ErrAccountNotEligible; the read mutates nothing.
type PersonalExportDataRepository interface {
	GetPersonalExportSections(ctx context.Context, accountID domain.AccountID) (*PersonalExportSections, error)
}

// SessionAgeDirectory resolves how long ago the calling session
// authenticated, from the server-observed session store, never from a
// client claim. Unknown or malformed session identifiers answer
// ErrUnknownSession.
type SessionAgeDirectory interface {
	SessionAgeAt(ctx context.Context, sessionID string, now time.Time) (time.Duration, error)
}

// PersonalExportRepository persists export records and enforces the
// owner-scoped download rules atomically.
type PersonalExportRepository interface {
	// RequestPersonalExport creates the active export or rotates the
	// capability of the existing active one, expiring a ready record past
	// its window first.
	RequestPersonalExport(ctx context.Context, request PersonalExportRequest) (*PersonalExportRecord, error)

	// GetPersonalExportForGeneration resolves one record by id. Unknown
	// identifiers answer ErrExportNotFound.
	GetPersonalExportForGeneration(ctx context.Context, exportID string) (*PersonalExportRecord, error)

	// MarkPersonalExportReady attaches the generated document once. updated
	// is false when another writer won the race: the caller resolves the
	// replay instead of overwriting.
	MarkPersonalExportReady(ctx context.Context, ready PersonalExportReady) (updated bool, err error)

	// GetPersonalExportDownloadGuard resolves the owner-scoped record;
	// unknown exports and foreign owners are indistinguishable and answer
	// ErrExportNotFound.
	GetPersonalExportDownloadGuard(ctx context.Context, accountID domain.AccountID, exportID string) (*PersonalExportRecord, error)

	// ConsumePersonalExportDownload serves the document and consumes one
	// unit of the budget in one conditional statement. A missing, foreign,
	// unready, expired, exhausted or token-mismatched record answers
	// ErrExportUnavailable.
	ConsumePersonalExportDownload(ctx context.Context, consumption PersonalExportConsumption) (*PersonalExportDownload, error)
}

// RequestedPersonalExport is the outcome of requesting an export: the
// record identity plus the raw capability, returned exactly once.
type RequestedPersonalExport struct {
	ExportID      string
	Status        domain.ExportStatus
	DownloadToken string
}

// RequestPersonalExportUseCase creates or replays the account's active
// export and issues a fresh single-use download capability. The caller must
// have authenticated recently (step-up), which the transport enforces from
// the server-observed session age.
type RequestPersonalExportUseCase struct {
	exports PersonalExportRepository
	random  Random
	clock   Clock
}

// NewRequestPersonalExportUseCase builds the use case, refusing incomplete
// composition.
func NewRequestPersonalExportUseCase(exports PersonalExportRepository, random Random, clock Clock) (*RequestPersonalExportUseCase, error) {
	if exports == nil || random == nil || clock == nil {
		return nil, ErrInvalidExportConfig
	}
	return &RequestPersonalExportUseCase{exports: exports, random: random, clock: clock}, nil
}

// Execute requests the export of one account. The raw token is generated
// with 256 bits of entropy and only its SHA-256 is persisted.
func (uc *RequestPersonalExportUseCase) Execute(ctx context.Context, accountID domain.AccountID) (*RequestedPersonalExport, error) {
	if accountID == "" {
		return nil, domain.ErrEmptyAccountID
	}

	tokenBytes := make([]byte, domain.ExportTokenBytes)
	if _, err := uc.random.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate personal export token: %w", err)
	}
	rawToken := base64.RawURLEncoding.EncodeToString(tokenBytes)
	tokenHash := sha256.Sum256([]byte(rawToken))

	record, err := uc.exports.RequestPersonalExport(ctx, PersonalExportRequest{
		AccountID:         accountID,
		DownloadTokenHash: tokenHash[:],
		MaxDownloads:      domain.ExportMaxDownloads,
		RequestedAt:       uc.clock.Now().UTC(),
	})
	if err != nil {
		return nil, err
	}
	return &RequestedPersonalExport{
		ExportID:      record.ID,
		Status:        record.Status,
		DownloadToken: rawToken,
	}, nil
}

// GeneratedPersonalExport is the outcome of one generation attempt;
// Replayed marks a document that was already recorded by an earlier run.
type GeneratedPersonalExport struct {
	ExportID string
	Status   domain.ExportStatus
	Replayed bool
}

// GeneratePersonalExportUseCase builds the machine-readable document of one
// requested export and records it with its content hash and expiry. It is
// idempotent: an already ready record answers Replayed without rebuilding.
type GeneratePersonalExportUseCase struct {
	exports PersonalExportRepository
	data    PersonalExportDataRepository
	encoder PersonalExportEncoder
	clock   Clock
}

// NewGeneratePersonalExportUseCase builds the use case, refusing incomplete
// composition.
func NewGeneratePersonalExportUseCase(exports PersonalExportRepository, data PersonalExportDataRepository, encoder PersonalExportEncoder, clock Clock) (*GeneratePersonalExportUseCase, error) {
	if exports == nil || data == nil || encoder == nil || clock == nil {
		return nil, ErrInvalidExportConfig
	}
	return &GeneratePersonalExportUseCase{exports: exports, data: data, encoder: encoder, clock: clock}, nil
}

// Execute generates one export record.
func (uc *GeneratePersonalExportUseCase) Execute(ctx context.Context, exportID string) (*GeneratedPersonalExport, error) {
	id := strings.TrimSpace(exportID)
	if id == "" {
		return nil, ErrExportNotFound
	}

	record, err := uc.exports.GetPersonalExportForGeneration(ctx, id)
	if err != nil {
		return nil, err
	}
	switch record.Status {
	case domain.ExportStatusReady:
		return &GeneratedPersonalExport{ExportID: record.ID, Status: domain.ExportStatusReady, Replayed: true}, nil
	case domain.ExportStatusRequested:
	default:
		return nil, ErrExportUnavailable
	}

	sections, err := uc.data.GetPersonalExportSections(ctx, domain.AccountID(record.AccountID))
	if err != nil {
		return nil, err
	}

	now := uc.clock.Now().UTC()
	document := BuildPersonalExportDocument(now, sections)
	encoded, err := uc.encoder.EncodePersonalExport(document)
	if err != nil {
		return nil, fmt.Errorf("encode personal export document: %w", err)
	}
	sum := sha256.Sum256(encoded)

	updated, err := uc.exports.MarkPersonalExportReady(ctx, PersonalExportReady{
		ExportID:       record.ID,
		Document:       encoded,
		DocumentSHA256: fmt.Sprintf("%x", sum),
		GeneratedAt:    now,
		ExpiresAt:      now.Add(domain.ExportTTL),
	})
	if err != nil {
		return nil, err
	}
	return &GeneratedPersonalExport{
		ExportID: record.ID,
		Status:   domain.ExportStatusReady,
		Replayed: !updated,
	}, nil
}

// DownloadPersonalExportCommand addresses one owner-scoped download.
type DownloadPersonalExportCommand struct {
	AccountID domain.AccountID
	ExportID  string
	Token     string
}

// DownloadedPersonalExport is one served document with its content hash.
type DownloadedPersonalExport struct {
	ExportID       string
	Document       []byte
	DocumentSHA256 string
}

// DownloadPersonalExportUseCase serves one personal export to its owner.
// Ownership, token validity, expiry and the download budget are all
// enforced; exhaustion answers the same unavailable error as expiry, so a
// replayed link never serves twice.
type DownloadPersonalExportUseCase struct {
	exports PersonalExportRepository
	clock   Clock
}

// NewDownloadPersonalExportUseCase builds the use case, refusing incomplete
// composition.
func NewDownloadPersonalExportUseCase(exports PersonalExportRepository, clock Clock) (*DownloadPersonalExportUseCase, error) {
	if exports == nil || clock == nil {
		return nil, ErrInvalidExportConfig
	}
	return &DownloadPersonalExportUseCase{exports: exports, clock: clock}, nil
}

// Execute resolves one download.
func (uc *DownloadPersonalExportUseCase) Execute(ctx context.Context, command DownloadPersonalExportCommand) (*DownloadedPersonalExport, error) {
	if command.AccountID == "" {
		return nil, domain.ErrEmptyAccountID
	}
	exportID := strings.TrimSpace(command.ExportID)
	token := strings.TrimSpace(command.Token)
	if exportID == "" {
		return nil, ErrExportNotFound
	}
	if token == "" {
		return nil, ErrInvalidExportToken
	}

	guard, err := uc.exports.GetPersonalExportDownloadGuard(ctx, command.AccountID, exportID)
	if err != nil {
		return nil, err
	}

	tokenHash := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(tokenHash[:], guard.DownloadTokenHash) != 1 {
		return nil, ErrInvalidExportToken
	}

	now := uc.clock.Now().UTC()
	var expiresAt time.Time
	if guard.ExpiresAt != nil {
		expiresAt = guard.ExpiresAt.UTC()
	}
	if !domain.ExportAvailable(guard.Status, expiresAt, guard.DownloadCount, guard.MaxDownloads, now) {
		return nil, ErrExportUnavailable
	}

	download, err := uc.exports.ConsumePersonalExportDownload(ctx, PersonalExportConsumption{
		AccountID:         command.AccountID,
		ExportID:          guard.ID,
		DownloadTokenHash: tokenHash[:],
		ConsumedAt:        now,
	})
	if err != nil {
		return nil, err
	}
	return &DownloadedPersonalExport{
		ExportID:       guard.ID,
		Document:       download.Document,
		DocumentSHA256: download.DocumentSHA256,
	}, nil
}
