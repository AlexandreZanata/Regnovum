package application

import (
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// ExportExcludedCategories documents, in machine-readable form, the
// categories intentionally left out of the personal export (docs/PRIVACY.md
// §1/§6): restricted security data, moderation evidence, provider
// identifiers and antifraud signals. The owner sees why a category is
// absent instead of an undocumented gap.
var ExportExcludedCategories = []string{
	"security_restricted",
	"moderation_evidence",
	"payment_provider_identifiers",
	"antifraud_signals",
}

// PersonalExportDocument is the versioned, machine-readable personal data
// export (P14-T05; docs/PRIVACY.md §4/§6, REQ-PRIV-01). It is served only
// to the authenticated owner and may carry private categories: email,
// authentication sessions (without device signals), individual positions
// and change history, drafts, financial history and billing state.
// Provider identifiers, webhook payloads, moderation evidence, IP
// addresses, device signals and secrets never enter this document.
type PersonalExportDocument struct {
	SchemaVersion      int       `json:"schema_version"`
	GeneratedAt        time.Time `json:"generated_at"`
	ExcludedCategories []string  `json:"excluded_categories"`
	PersonalExportSections
}

// PersonalExportSections is the owner's data grouped by category. The
// sections are exactly the categories docs/PRIVACY.md §1 lists as public by
// nature or private of the subject, resolved for one account.
type PersonalExportSections struct {
	Account         PersonalExportAccount          `json:"account"`
	Positions       []PersonalExportPosition       `json:"positions"`
	PositionChanges []PersonalExportPositionChange `json:"position_changes"`
	ArenaDrafts     []PersonalExportArenaDraft     `json:"arena_drafts"`
	Arguments       []PersonalExportArgument       `json:"arguments"`
	Wallet          PersonalExportWallet           `json:"wallet"`
	Passes          PersonalExportPasses           `json:"passes"`
	Billing         PersonalExportBilling          `json:"billing"`
}

// PersonalExportAccount is the account identity plus the optional profile
// preferences and the account's own sessions.
type PersonalExportAccount struct {
	ID              string                    `json:"id"`
	Email           string                    `json:"email"`
	Status          string                    `json:"status"`
	EmailVerified   bool                      `json:"email_verified"`
	CreatedAt       time.Time                 `json:"created_at"`
	Profile         *PersonalExportProfile    `json:"profile"`
	Preferences     *PersonalExportPreference `json:"preferences"`
	UsernameHistory []PersonalExportUsername  `json:"username_history"`
	Sessions        []PersonalExportSession   `json:"sessions"`
}

// PersonalExportProfile is the subject's private profile projection.
type PersonalExportProfile struct {
	Username        string    `json:"username"`
	InterfaceLocale string    `json:"interface_locale"`
	Timezone        *string   `json:"timezone"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// PersonalExportPreference is the explicit communication preference.
type PersonalExportPreference struct {
	MarketingOptIn bool      `json:"marketing_opt_in"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// PersonalExportUsername is one historical username entry.
type PersonalExportUsername struct {
	Username  string    `json:"username"`
	ChangedAt time.Time `json:"changed_at"`
}

// PersonalExportSession is one of the account's own sessions without device
// signals: the IP address and user agent are restricted security data.
type PersonalExportSession struct {
	ID        string     `json:"id"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at"`
}

// PersonalExportPosition is one individual position with the Arena context
// needed to understand it.
type PersonalExportPosition struct {
	ArenaID         string    `json:"arena_id"`
	ArenaSlug       string    `json:"arena_slug"`
	ArenaStatement  string    `json:"arena_statement"`
	InitialPosition string    `json:"initial_position"`
	CurrentPosition string    `json:"current_position"`
	Version         int32     `json:"version"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// PersonalExportPositionChange is one individual change of the history
// chain.
type PersonalExportPositionChange struct {
	ArenaID      string    `json:"arena_id"`
	FromPosition string    `json:"from_position"`
	ToPosition   string    `json:"to_position"`
	Version      int32     `json:"version"`
	ChangedAt    time.Time `json:"changed_at"`
}

// PersonalExportArenaDraft is one unpublished Arena draft.
type PersonalExportArenaDraft struct {
	ID        string    `json:"id"`
	Statement string    `json:"statement"`
	Context   *string   `json:"context"`
	Category  string    `json:"category"`
	Language  string    `json:"language"`
	Version   int32     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
}

// PersonalExportArgument is one argument or reply authored by the subject,
// published or withdrawn, with its sources.
type PersonalExportArgument struct {
	ID          string                 `json:"id"`
	ArenaID     string                 `json:"arena_id"`
	ParentID    *string                `json:"parent_id"`
	Relation    string                 `json:"relation"`
	Content     string                 `json:"content"`
	Status      string                 `json:"status"`
	CreatedAt   time.Time              `json:"created_at"`
	WithdrawnAt *time.Time             `json:"withdrawn_at"`
	Sources     []PersonalExportSource `json:"sources"`
}

// PersonalExportSource is one source supporting one argument.
type PersonalExportSource struct {
	URL         string  `json:"url"`
	Description *string `json:"description"`
}

// PersonalExportWallet is the INK balance projection plus the ledger.
type PersonalExportWallet struct {
	BalanceFree      int64                       `json:"balance_free"`
	BalancePurchased int64                       `json:"balance_purchased"`
	Transactions     []PersonalExportWalletEntry `json:"transactions"`
}

// PersonalExportWalletEntry is one signed INK ledger delta. The operation
// reference (which may embed provider identifiers) never serializes.
type PersonalExportWalletEntry struct {
	Operation string    `json:"operation"`
	Bucket    string    `json:"bucket"`
	Amount    int64     `json:"amount"`
	CreatedAt time.Time `json:"created_at"`
}

// PersonalExportPasses groups the Arena Pass lots and consumptions.
type PersonalExportPasses struct {
	Lots         []PersonalExportPassLot         `json:"lots"`
	Consumptions []PersonalExportPassConsumption `json:"consumptions"`
}

// PersonalExportPassLot is one Arena Pass lot without the grant reference.
type PersonalExportPassLot struct {
	Origin    string     `json:"origin"`
	Quantity  int32      `json:"quantity"`
	Remaining int32      `json:"remaining"`
	ExpiresAt *time.Time `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
}

// PersonalExportPassConsumption is one consumed Arena Pass.
type PersonalExportPassConsumption struct {
	ArenaID    string    `json:"arena_id"`
	ConsumedAt time.Time `json:"consumed_at"`
}

// PersonalExportBilling groups the local purchase and subscription history.
type PersonalExportBilling struct {
	CheckoutIntents []PersonalExportCheckoutIntent `json:"checkout_intents"`
	Subscriptions   []PersonalExportSubscription   `json:"subscriptions"`
}

// PersonalExportCheckoutIntent is one local purchase attempt; provider
// session and payment identifiers never serialize.
type PersonalExportCheckoutIntent struct {
	ProductID   string     `json:"product_id"`
	Market      string     `json:"market"`
	Currency    string     `json:"currency"`
	AmountMinor int64      `json:"amount_minor"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	PaidAt      *time.Time `json:"paid_at"`
}

// PersonalExportSubscription is one subscription state; provider and price
// identifiers never serialize.
type PersonalExportSubscription struct {
	ProductID          string     `json:"product_id"`
	Status             string     `json:"status"`
	CurrentPeriodStart *time.Time `json:"current_period_start"`
	CurrentPeriodEnd   *time.Time `json:"current_period_end"`
	CancelAtPeriodEnd  bool       `json:"cancel_at_period_end"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// BuildPersonalExportDocument wraps one account's sections in the versioned
// envelope. The builder never invents data: absent categories serialize as
// empty arrays or nulls, and the schema version is the pinned domain
// constant.
func BuildPersonalExportDocument(now time.Time, sections *PersonalExportSections) PersonalExportDocument {
	document := PersonalExportDocument{
		SchemaVersion:      domain.ExportSchemaVersion,
		GeneratedAt:        now.UTC(),
		ExcludedCategories: append([]string{}, ExportExcludedCategories...),
		PersonalExportSections: PersonalExportSections{
			Account: PersonalExportAccount{
				UsernameHistory: []PersonalExportUsername{},
				Sessions:        []PersonalExportSession{},
			},
			Positions:       []PersonalExportPosition{},
			PositionChanges: []PersonalExportPositionChange{},
			ArenaDrafts:     []PersonalExportArenaDraft{},
			Arguments:       []PersonalExportArgument{},
			Wallet:          PersonalExportWallet{Transactions: []PersonalExportWalletEntry{}},
			Passes: PersonalExportPasses{
				Lots:         []PersonalExportPassLot{},
				Consumptions: []PersonalExportPassConsumption{},
			},
			Billing: PersonalExportBilling{
				CheckoutIntents: []PersonalExportCheckoutIntent{},
				Subscriptions:   []PersonalExportSubscription{},
			},
		},
	}
	if sections == nil {
		return document
	}

	document.PersonalExportSections = *sections
	document.Account.UsernameHistory = orEmpty(document.Account.UsernameHistory)
	document.Account.Sessions = orEmpty(document.Account.Sessions)
	document.Positions = orEmpty(document.Positions)
	document.PositionChanges = orEmpty(document.PositionChanges)
	document.ArenaDrafts = orEmpty(document.ArenaDrafts)
	document.Arguments = orEmpty(document.Arguments)
	document.Wallet.Transactions = orEmpty(document.Wallet.Transactions)
	document.Passes.Lots = orEmpty(document.Passes.Lots)
	document.Passes.Consumptions = orEmpty(document.Passes.Consumptions)
	document.Billing.CheckoutIntents = orEmpty(document.Billing.CheckoutIntents)
	document.Billing.Subscriptions = orEmpty(document.Billing.Subscriptions)
	return document
}

// orEmpty keeps JSON arrays representable: a nil slice would serialize as
// null while an empty slice serializes as [], and clients must always see a
// list.
func orEmpty[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}
