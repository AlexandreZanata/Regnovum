package outbox

import (
	"context"
	"errors"
	"fmt"

	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/application"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
	profilesapp "github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	profilesdomain "github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// Directory answers the one question the notifications module asks about a
// recipient: which account is behind this address, and which locale did its
// owner choose.
//
// It composes two read paths that the module deliberately does not own —
// identity knows the accounts, profiles knows the preferences — and it is the
// reason notifications imports neither of them: one adapter translates both
// into the port the use case declares.
//
// The two reads are independent on purpose. An account always exists when a
// flow notifies it, but a profile may not: an account that never stored a
// preference is not an error, it reads as "no preference", and the caller
// falls back to the product default exactly as the interface does.
type Directory struct {
	accounts    identityapp.AccountRepository
	preferences profilesapp.CommunicationPreferencesReader
}

// NewDirectory wires the bridge. A nil dependency fails at construction: a
// directory that cannot answer would otherwise turn every notification into a
// silent default.
func NewDirectory(accounts identityapp.AccountRepository, preferences profilesapp.CommunicationPreferencesReader) (*Directory, error) {
	if accounts == nil || preferences == nil {
		return nil, domain.ErrMissingDependency
	}
	return &Directory{accounts: accounts, preferences: preferences}, nil
}

// AccountForAddress resolves the account and the locale it prefers.
//
// An address that is malformed or belongs to no account resolves to the zero
// AccountRef without error: the notification is still valid work, and refusing
// it would break a flow over a locale guess. A failure of either read is
// returned, because swallowing it would let an event commit with a job whose
// locale was never resolved.
func (d *Directory) AccountForAddress(ctx context.Context, address string) (application.AccountRef, error) {
	if d == nil || d.accounts == nil || d.preferences == nil {
		return application.AccountRef{}, domain.ErrMissingDependency
	}
	if ctx == nil {
		return application.AccountRef{}, errors.New("outbox: nil context")
	}
	parsed, err := identitydomain.ParseEmail(address)
	if err != nil {
		return application.AccountRef{}, nil
	}
	account, err := d.accounts.GetAccountByEmail(ctx, parsed)
	if err != nil {
		if errors.Is(err, identityapp.ErrAccountNotFound) {
			return application.AccountRef{}, nil
		}
		return application.AccountRef{}, fmt.Errorf("outbox: load account: %w", err)
	}
	ref := application.AccountRef{ID: account.ID().String()}
	if ref.ID == "" {
		return ref, nil
	}
	preferences, err := d.preferences.PreferencesFor(ctx, profilesdomain.AccountID(ref.ID))
	if err != nil {
		if errors.Is(err, profilesapp.ErrProfileNotFound) {
			return ref, nil
		}
		return application.AccountRef{}, fmt.Errorf("outbox: load preferences: %w", err)
	}
	if preferences == nil {
		return ref, nil
	}
	// The preference is a profiles value object; the notification domain
	// accepts only its own closed set, so an unsupported tag falls back
	// instead of travelling into a job payload.
	if locale, err := domain.ParseLocale(preferences.InterfaceLocale.String()); err == nil {
		ref.Locale = locale
	}
	return ref, nil
}
