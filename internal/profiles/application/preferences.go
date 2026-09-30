package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// CommunicationPreferences is the explicit preferences projection consumed
// by the notifications module and by email jobs. It carries the interface
// locale so jobs freeze the locale at creation time and retries stay in the
// language the owner chose (I18N_STANDARD.md §5), plus the marketing opt-in.
// Essential/transactional messages are not optional and are not represented
// here.
type CommunicationPreferences struct {
	AccountID       domain.AccountID
	InterfaceLocale domain.Locale
	MarketingOptIn  bool
}

// CommunicationPreferencesReader is the stable query port consulted by the
// notifications module. Absence of an explicit opt-in resolves to the
// conservative default (false): reading preferences never opts anyone in.
type CommunicationPreferencesReader interface {
	// PreferencesFor returns the explicit preferences of the account, or
	// ErrProfileNotFound when the account has no profile.
	PreferencesFor(ctx context.Context, accountID domain.AccountID) (*CommunicationPreferences, error)
}

// CommunicationPreferencesRepository persists explicit preferences and their
// append-only audit trail. Opt-in is strictly explicit: the writer stores
// exactly the value it is given and appends the corresponding audit entry in
// the same transaction.
type CommunicationPreferencesRepository interface {
	CommunicationPreferencesReader

	// SetMarketingOptIn atomically upserts the explicit marketing consent and
	// appends the audit entry.
	SetMarketingOptIn(ctx context.Context, accountID domain.AccountID, optIn bool, changedAt time.Time) (*CommunicationPreferences, error)
}
