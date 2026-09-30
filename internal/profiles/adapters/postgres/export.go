package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

var (
	_ application.PersonalExportRepository     = (*Repository)(nil)
	_ application.PersonalExportDataRepository = (*Repository)(nil)
	_ application.SessionAgeDirectory          = (*Repository)(nil)
)

// SessionAgeAt returns how long ago the session authenticated, as of now.
// Unknown or malformed session identifiers deny distinctly instead of
// being treated as fresh.
func (r *Repository) SessionAgeAt(ctx context.Context, sessionID string, now time.Time) (time.Duration, error) {
	sessionUUID, err := pgUUIDFromExportID(sessionID)
	if err != nil {
		return 0, application.ErrUnknownSession
	}
	createdAt, err := r.queries.GetSessionCreatedAt(ctx, sessionUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, application.ErrUnknownSession
		}
		return 0, fmt.Errorf("load session age: %w", err)
	}
	age := now.UTC().Sub(createdAt.Time.UTC())
	if age < 0 {
		age = 0
	}
	return age, nil
}

// RequestPersonalExport creates the active export or rotates its download
// capability, expiring a ready record past its window first.
func (r *Repository) RequestPersonalExport(ctx context.Context, request application.PersonalExportRequest) (*application.PersonalExportRecord, error) {
	accountUUID, err := pgUUIDFromAccountID(request.AccountID)
	if err != nil {
		return nil, application.ErrAccountNotEligible
	}

	// Expire stale links first: the partial unique index only covers active
	// records, so an expired ready row must leave it before the request can
	// create a fresh record instead of reviving the stale capability.
	if _, err := r.queries.ExpirePersonalExports(ctx, platformpg.ExpirePersonalExportsParams{
		AccountID: accountUUID,
		Now:       timestamptz(request.RequestedAt.UTC()),
	}); err != nil {
		return nil, fmt.Errorf("expire personal exports: %w", err)
	}

	row, err := r.queries.RequestPersonalExport(ctx, platformpg.RequestPersonalExportParams{
		AccountID:         accountUUID,
		DownloadTokenHash: request.DownloadTokenHash,
		MaxDownloads:      request.MaxDownloads,
		RequestedAt:       timestamptz(request.RequestedAt.UTC()),
	})
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, application.ErrAccountNotEligible
		}
		return nil, fmt.Errorf("request personal export: %w", err)
	}
	return mapPersonalExportRecord(row.ID, row.AccountID, row.Status, row.RequestedAt, row.GeneratedAt, row.ExpiresAt, row.DownloadCount, row.MaxDownloads, request.DownloadTokenHash), nil
}

// GetPersonalExportForGeneration resolves one record by id.
func (r *Repository) GetPersonalExportForGeneration(ctx context.Context, exportID string) (*application.PersonalExportRecord, error) {
	exportUUID, err := pgUUIDFromExportID(exportID)
	if err != nil {
		return nil, application.ErrExportNotFound
	}

	row, err := r.queries.GetPersonalExportForGeneration(ctx, exportUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrExportNotFound
		}
		return nil, fmt.Errorf("get personal export for generation: %w", err)
	}
	return mapPersonalExportRecord(row.ID, row.AccountID, row.Status, row.RequestedAt, row.GeneratedAt, row.ExpiresAt, row.DownloadCount, row.MaxDownloads, nil), nil
}

// MarkPersonalExportReady attaches the generated document once.
func (r *Repository) MarkPersonalExportReady(ctx context.Context, ready application.PersonalExportReady) (bool, error) {
	exportUUID, err := pgUUIDFromExportID(ready.ExportID)
	if err != nil {
		return false, application.ErrExportNotFound
	}

	rows, err := r.queries.MarkPersonalExportReady(ctx, platformpg.MarkPersonalExportReadyParams{
		ExportID:       exportUUID,
		GeneratedAt:    timestamptz(ready.GeneratedAt.UTC()),
		ExpiresAt:      timestamptz(ready.ExpiresAt.UTC()),
		Document:       string(ready.Document),
		DocumentSha256: ready.DocumentSHA256,
	})
	if err != nil {
		return false, fmt.Errorf("mark personal export ready: %w", err)
	}
	return rows == 1, nil
}

// GetPersonalExportDownloadGuard resolves the owner-scoped record; foreign
// owners and unknown identifiers are indistinguishable.
func (r *Repository) GetPersonalExportDownloadGuard(ctx context.Context, accountID domain.AccountID, exportID string) (*application.PersonalExportRecord, error) {
	accountUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return nil, application.ErrExportNotFound
	}
	exportUUID, err := pgUUIDFromExportID(exportID)
	if err != nil {
		return nil, application.ErrExportNotFound
	}

	row, err := r.queries.GetPersonalExportDownloadGuard(ctx, platformpg.GetPersonalExportDownloadGuardParams{
		ExportID:  exportUUID,
		AccountID: accountUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrExportNotFound
		}
		return nil, fmt.Errorf("get personal export guard: %w", err)
	}
	return mapPersonalExportRecord(row.ID, row.AccountID, row.Status, pgtype.Timestamptz{}, pgtype.Timestamptz{}, row.ExpiresAt, row.DownloadCount, row.MaxDownloads, row.DownloadTokenHash), nil
}

// ConsumePersonalExportDownload serves the document and consumes one unit
// of the budget atomically; every denial answers ErrExportUnavailable.
func (r *Repository) ConsumePersonalExportDownload(ctx context.Context, consumption application.PersonalExportConsumption) (*application.PersonalExportDownload, error) {
	accountUUID, err := pgUUIDFromAccountID(consumption.AccountID)
	if err != nil {
		return nil, application.ErrExportUnavailable
	}
	exportUUID, err := pgUUIDFromExportID(consumption.ExportID)
	if err != nil {
		return nil, application.ErrExportUnavailable
	}

	row, err := r.queries.ConsumePersonalExportDownload(ctx, platformpg.ConsumePersonalExportDownloadParams{
		ExportID:          exportUUID,
		AccountID:         accountUUID,
		DownloadTokenHash: consumption.DownloadTokenHash,
		ConsumedAt:        timestamptz(consumption.ConsumedAt.UTC()),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrExportUnavailable
		}
		return nil, fmt.Errorf("consume personal export download: %w", err)
	}
	if !row.Document.Valid || !row.DocumentSha256.Valid {
		return nil, application.ErrExportUnavailable
	}
	return &application.PersonalExportDownload{
		Document:       []byte(row.Document.String),
		DocumentSHA256: row.DocumentSha256.String,
	}, nil
}

// GetPersonalExportSections reads every personal category of one account.
// Each statement is scoped by the account id; the read mutates nothing.
func (r *Repository) GetPersonalExportSections(ctx context.Context, accountID domain.AccountID) (*application.PersonalExportSections, error) {
	accountUUID, err := pgUUIDFromAccountID(accountID)
	if err != nil {
		return nil, application.ErrAccountNotEligible
	}

	accountRow, err := r.queries.GetPersonalExportAccount(ctx, accountUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrAccountNotEligible
		}
		return nil, fmt.Errorf("get personal export account: %w", err)
	}

	sections := &application.PersonalExportSections{
		Account: application.PersonalExportAccount{
			ID:              uuidToString(accountRow.ID),
			Email:           accountRow.Email,
			Status:          accountRow.Status,
			EmailVerified:   accountRow.EmailVerifiedAt.Valid,
			CreatedAt:       exportTime(accountRow.CreatedAt),
			UsernameHistory: []application.PersonalExportUsername{},
			Sessions:        []application.PersonalExportSession{},
		},
		Positions:       []application.PersonalExportPosition{},
		PositionChanges: []application.PersonalExportPositionChange{},
		ArenaDrafts:     []application.PersonalExportArenaDraft{},
		Arguments:       []application.PersonalExportArgument{},
		Wallet:          application.PersonalExportWallet{Transactions: []application.PersonalExportWalletEntry{}},
		Passes: application.PersonalExportPasses{
			Lots:         []application.PersonalExportPassLot{},
			Consumptions: []application.PersonalExportPassConsumption{},
		},
		Billing: application.PersonalExportBilling{
			CheckoutIntents: []application.PersonalExportCheckoutIntent{},
			Subscriptions:   []application.PersonalExportSubscription{},
		},
	}
	if accountRow.Username.Valid {
		sections.Account.Profile = &application.PersonalExportProfile{
			Username:        accountRow.Username.String,
			InterfaceLocale: accountRow.InterfaceLocale.String,
			Timezone:        optionalString(accountRow.Timezone),
			CreatedAt:       exportTime(accountRow.ProfileCreatedAt),
			UpdatedAt:       exportTime(accountRow.ProfileUpdatedAt),
		}
	}
	if accountRow.MarketingOptIn.Valid {
		sections.Account.Preferences = &application.PersonalExportPreference{
			MarketingOptIn: accountRow.MarketingOptIn.Bool,
			UpdatedAt:      exportTime(accountRow.PreferencesUpdatedAt),
		}
	}

	if err := r.fillPersonalExportSections(ctx, accountUUID, sections); err != nil {
		return nil, err
	}
	return sections, nil
}

// fillPersonalExportSections loads every list category of one account.
func (r *Repository) fillPersonalExportSections(ctx context.Context, accountUUID pgtype.UUID, sections *application.PersonalExportSections) error {
	usernameRows, err := r.queries.ListPersonalExportUsernameHistory(ctx, accountUUID)
	if err != nil {
		return fmt.Errorf("list personal export username history: %w", err)
	}
	for _, row := range usernameRows {
		sections.Account.UsernameHistory = append(sections.Account.UsernameHistory, application.PersonalExportUsername{
			Username:  row.Username,
			ChangedAt: exportTime(row.ChangedAt),
		})
	}

	sessionRows, err := r.queries.ListPersonalExportSessions(ctx, accountUUID)
	if err != nil {
		return fmt.Errorf("list personal export sessions: %w", err)
	}
	for _, row := range sessionRows {
		sessionID := uuidToString(row.ID)
		if sessionID == "" {
			return errors.New("list personal export sessions: stored session has no id")
		}
		sections.Account.Sessions = append(sections.Account.Sessions, application.PersonalExportSession{
			ID:        sessionID,
			CreatedAt: exportTime(row.CreatedAt),
			ExpiresAt: exportTime(row.ExpiresAt),
			RevokedAt: exportTimePtr(row.RevokedAt),
		})
	}

	positionRows, err := r.queries.ListPersonalExportPositions(ctx, accountUUID)
	if err != nil {
		return fmt.Errorf("list personal export positions: %w", err)
	}
	for _, row := range positionRows {
		sections.Positions = append(sections.Positions, application.PersonalExportPosition{
			ArenaID:         uuidToString(row.ArenaID),
			ArenaSlug:       row.Slug.String,
			ArenaStatement:  row.Statement,
			InitialPosition: row.InitialPosition,
			CurrentPosition: row.CurrentPosition,
			Version:         row.Version,
			CreatedAt:       exportTime(row.CreatedAt),
			UpdatedAt:       exportTime(row.UpdatedAt),
		})
	}

	changeRows, err := r.queries.ListPersonalExportPositionChanges(ctx, accountUUID)
	if err != nil {
		return fmt.Errorf("list personal export position changes: %w", err)
	}
	for _, row := range changeRows {
		sections.PositionChanges = append(sections.PositionChanges, application.PersonalExportPositionChange{
			ArenaID:      uuidToString(row.ArenaID),
			FromPosition: row.FromPosition,
			ToPosition:   row.ToPosition,
			Version:      row.Version,
			ChangedAt:    exportTime(row.ChangedAt),
		})
	}

	draftRows, err := r.queries.ListPersonalExportArenaDrafts(ctx, accountUUID)
	if err != nil {
		return fmt.Errorf("list personal export arena drafts: %w", err)
	}
	for _, row := range draftRows {
		sections.ArenaDrafts = append(sections.ArenaDrafts, application.PersonalExportArenaDraft{
			ID:        uuidToString(row.ID),
			Statement: row.Statement,
			Context:   optionalString(row.Context),
			Category:  row.Category,
			Language:  row.Language,
			Version:   row.Version,
			CreatedAt: exportTime(row.CreatedAt),
		})
	}

	argumentRows, err := r.queries.ListPersonalExportArguments(ctx, accountUUID)
	if err != nil {
		return fmt.Errorf("list personal export arguments: %w", err)
	}
	argumentIDs := make([]pgtype.UUID, 0, len(argumentRows))
	for _, row := range argumentRows {
		argument := application.PersonalExportArgument{
			ID:          uuidToString(row.ID),
			ArenaID:     uuidToString(row.ArenaID),
			Relation:    row.Relation,
			Content:     row.Content,
			Status:      row.Status,
			CreatedAt:   exportTime(row.CreatedAt),
			WithdrawnAt: exportTimePtr(row.WithdrawnAt),
			Sources:     []application.PersonalExportSource{},
		}
		if parentID := uuidToString(row.ParentID); parentID != "" {
			argument.ParentID = &parentID
		}
		sections.Arguments = append(sections.Arguments, argument)
		argumentIDs = append(argumentIDs, row.ID)
	}
	if len(argumentIDs) > 0 {
		sourceRows, err := r.queries.ListPersonalExportArgumentSources(ctx, argumentIDs)
		if err != nil {
			return fmt.Errorf("list personal export sources: %w", err)
		}
		byArgument := make(map[string][]application.PersonalExportSource, len(argumentIDs))
		for _, row := range sourceRows {
			argumentID := uuidToString(row.ArgumentID)
			if argumentID == "" {
				return errors.New("list personal export sources: stored source has no argument")
			}
			byArgument[argumentID] = append(byArgument[argumentID], application.PersonalExportSource{
				URL:         row.Url,
				Description: optionalString(row.Description),
			})
		}
		for i := range sections.Arguments {
			if sources, ok := byArgument[sections.Arguments[i].ID]; ok {
				sections.Arguments[i].Sources = sources
			}
		}
	}

	walletRow, err := r.queries.GetPersonalExportWallet(ctx, accountUUID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("get personal export wallet: %w", err)
	}
	if err == nil {
		sections.Wallet.BalanceFree = walletRow.BalanceFree
		sections.Wallet.BalancePurchased = walletRow.BalancePurchased
	}

	transactionRows, err := r.queries.ListPersonalExportWalletTransactions(ctx, accountUUID)
	if err != nil {
		return fmt.Errorf("list personal export transactions: %w", err)
	}
	for _, row := range transactionRows {
		sections.Wallet.Transactions = append(sections.Wallet.Transactions, application.PersonalExportWalletEntry{
			Operation: row.OperationType,
			Bucket:    row.Bucket,
			Amount:    row.Amount,
			CreatedAt: exportTime(row.CreatedAt),
		})
	}

	lotRows, err := r.queries.ListPersonalExportPassLots(ctx, accountUUID)
	if err != nil {
		return fmt.Errorf("list personal export pass lots: %w", err)
	}
	for _, row := range lotRows {
		sections.Passes.Lots = append(sections.Passes.Lots, application.PersonalExportPassLot{
			Origin:    row.Origin,
			Quantity:  row.Quantity,
			Remaining: row.RemainingQuantity,
			ExpiresAt: exportTimePtr(row.ExpiresAt),
			CreatedAt: exportTime(row.CreatedAt),
		})
	}

	consumptionRows, err := r.queries.ListPersonalExportPassConsumptions(ctx, accountUUID)
	if err != nil {
		return fmt.Errorf("list personal export pass consumptions: %w", err)
	}
	for _, row := range consumptionRows {
		sections.Passes.Consumptions = append(sections.Passes.Consumptions, application.PersonalExportPassConsumption{
			ArenaID:    uuidToString(row.ArenaID),
			ConsumedAt: exportTime(row.ConsumedAt),
		})
	}

	intentRows, err := r.queries.ListPersonalExportCheckoutIntents(ctx, accountUUID)
	if err != nil {
		return fmt.Errorf("list personal export checkout intents: %w", err)
	}
	for _, row := range intentRows {
		sections.Billing.CheckoutIntents = append(sections.Billing.CheckoutIntents, application.PersonalExportCheckoutIntent{
			ProductID:   row.ProductID,
			Market:      row.Market,
			Currency:    row.Currency,
			AmountMinor: row.AmountMinor,
			Status:      row.Status,
			CreatedAt:   exportTime(row.CreatedAt),
			PaidAt:      exportTimePtr(row.PaidAt),
		})
	}

	subscriptionRows, err := r.queries.ListPersonalExportSubscriptions(ctx, accountUUID)
	if err != nil {
		return fmt.Errorf("list personal export subscriptions: %w", err)
	}
	for _, row := range subscriptionRows {
		sections.Billing.Subscriptions = append(sections.Billing.Subscriptions, application.PersonalExportSubscription{
			ProductID:          row.ProductID,
			Status:             row.Status,
			CurrentPeriodStart: exportTimePtr(row.CurrentPeriodStart),
			CurrentPeriodEnd:   exportTimePtr(row.CurrentPeriodEnd),
			CancelAtPeriodEnd:  row.CancelAtPeriodEnd,
			CreatedAt:          exportTime(row.CreatedAt),
			UpdatedAt:          exportTime(row.UpdatedAt),
		})
	}
	return nil
}

// mapPersonalExportRecord converts stored columns into the application
// record. Invalid identifiers are storage corruption and answer not found.
func mapPersonalExportRecord(
	id, accountID pgtype.UUID,
	status string,
	requestedAt, generatedAt, expiresAt pgtype.Timestamptz,
	downloadCount, maxDownloads int32,
	tokenHash []byte,
) *application.PersonalExportRecord {
	return &application.PersonalExportRecord{
		ID:                uuidToString(id),
		AccountID:         uuidToString(accountID),
		Status:            domain.ExportStatus(status),
		RequestedAt:       exportTime(requestedAt),
		GeneratedAt:       exportTimePtr(generatedAt),
		ExpiresAt:         exportTimePtr(expiresAt),
		DownloadCount:     downloadCount,
		MaxDownloads:      maxDownloads,
		DownloadTokenHash: tokenHash,
	}
}

// pgUUIDFromExportID parses one export identifier.
func pgUUIDFromExportID(exportID string) (pgtype.UUID, error) {
	var pgUUID pgtype.UUID
	if err := pgUUID.Scan(exportID); err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid export id format: %w", err)
	}
	return pgUUID, nil
}

// isForeignKeyViolation reports whether the error is a foreign key failure.
func isForeignKeyViolation(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && pgError.Code == "23503"
}

// exportTime renders a stored instant in UTC; the zero value stays zero.
func exportTime(instant pgtype.Timestamptz) time.Time {
	if !instant.Valid {
		return time.Time{}
	}
	return instant.Time.UTC()
}

// exportTimePtr renders an optional stored instant in UTC.
func exportTimePtr(instant pgtype.Timestamptz) *time.Time {
	if !instant.Valid {
		return nil
	}
	copied := instant.Time.UTC()
	return &copied
}

// optionalString renders an absent text as nil.
func optionalString(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	copied := value.String
	return &copied
}
