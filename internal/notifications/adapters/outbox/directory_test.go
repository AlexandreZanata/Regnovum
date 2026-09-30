package outbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/adapters/outbox"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
	profilesapp "github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	profilesdomain "github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// fakeAccounts answers the identity read path the directory needs.
type fakeAccounts struct {
	account *identitydomain.Account
	err     error
}

func (f fakeAccounts) CreateAccountWithPassword(context.Context, identitydomain.Email, string) (*identitydomain.Account, error) {
	return nil, errors.New("fake accounts: CreateAccountWithPassword is not part of this test")
}

func (f fakeAccounts) GetAccountByEmail(context.Context, identitydomain.Email) (*identitydomain.Account, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.account, nil
}

func (f fakeAccounts) GetAccountByID(context.Context, identitydomain.AccountID) (*identitydomain.Account, error) {
	return nil, errors.New("fake accounts: GetAccountByID is not part of this test")
}

func (f fakeAccounts) SetEmailVerified(context.Context, identitydomain.AccountID, time.Time) error {
	return errors.New("fake accounts: SetEmailVerified is not part of this test")
}

// fakePreferences answers the profiles read path the directory needs.
type fakePreferences struct {
	preferences *profilesapp.CommunicationPreferences
	err         error
	asked       []string
}

func (f *fakePreferences) PreferencesFor(_ context.Context, accountID profilesdomain.AccountID) (*profilesapp.CommunicationPreferences, error) {
	f.asked = append(f.asked, string(accountID))
	if f.err != nil {
		return nil, f.err
	}
	return f.preferences, nil
}

func account(t *testing.T, id string) *identitydomain.Account {
	t.Helper()
	email := identityEmail(t, "ana.silva@example.com")
	built, err := identitydomain.NewAccount(identitydomain.AccountID(id), email, time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewAccount() error = %v", err)
	}
	return built
}

func TestDirectoryResolvesTheAccountAndItsPreference(t *testing.T) {
	preferences := &fakePreferences{}
	locale, err := profilesdomain.ParseLocale("en-US")
	if err != nil {
		t.Fatalf("ParseLocale() error = %v", err)
	}
	preferences.preferences = &profilesapp.CommunicationPreferences{InterfaceLocale: locale}
	directory, err := outbox.NewDirectory(fakeAccounts{account: account(t, "account-1")}, preferences)
	if err != nil {
		t.Fatalf("NewDirectory() error = %v", err)
	}
	ref, err := directory.AccountForAddress(context.Background(), "ana.silva@example.com")
	if err != nil {
		t.Fatalf("AccountForAddress() error = %v", err)
	}
	if ref.ID != "account-1" {
		t.Errorf("ID = %q, want the account identifier", ref.ID)
	}
	if ref.Locale != domain.LocaleAmericanEnglish {
		t.Errorf("Locale = %q, want en-US", ref.Locale)
	}
	if len(preferences.asked) != 1 || preferences.asked[0] != "account-1" {
		t.Errorf("preferences asked for %v, want the resolved account", preferences.asked)
	}
}

// TestDirectoryTreatsAbsenceAsNoPreference is the rule that keeps a locale from
// being invented: a missing profile or a missing row reads as "no preference",
// never as an error and never as a guessed language.
func TestDirectoryTreatsAbsenceAsNoPreference(t *testing.T) {
	for _, testCase := range []struct {
		name string
		pref *fakePreferences
	}{
		{"missing profile", &fakePreferences{err: profilesapp.ErrProfileNotFound}},
		{"no stored row", &fakePreferences{preferences: nil}},
		{"unknown address", &fakePreferences{}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			accounts := fakeAccounts{account: account(t, "account-1")}
			address := "ana.silva@example.com"
			if testCase.name == "unknown address" {
				accounts.err = identityapp.ErrAccountNotFound
			}
			directory, err := outbox.NewDirectory(accounts, testCase.pref)
			if err != nil {
				t.Fatalf("NewDirectory() error = %v", err)
			}
			ref, err := directory.AccountForAddress(context.Background(), address)
			if err != nil {
				t.Fatalf("AccountForAddress() error = %v", err)
			}
			if ref.Locale != "" {
				t.Errorf("Locale = %q, want no preference", ref.Locale)
			}
			if testCase.name == "unknown address" && ref.ID != "" {
				t.Errorf("ID = %q, want no account", ref.ID)
			}
		})
	}
}

func TestDirectoryDoesNotGuessForAMalformedAddress(t *testing.T) {
	preferences := &fakePreferences{}
	directory, err := outbox.NewDirectory(fakeAccounts{account: account(t, "account-1")}, preferences)
	if err != nil {
		t.Fatalf("NewDirectory() error = %v", err)
	}
	ref, err := directory.AccountForAddress(context.Background(), "not an address")
	if err != nil {
		t.Fatalf("AccountForAddress() error = %v", err)
	}
	if ref.ID != "" || ref.Locale != "" {
		t.Errorf("ref = %+v, want the zero reference", ref)
	}
	if len(preferences.asked) != 0 {
		t.Errorf("preferences asked for %v, want no lookup", preferences.asked)
	}
}

func TestDirectorySurfacesInfrastructureFailures(t *testing.T) {
	readFailure := errors.New("identity: read unavailable")
	preferenceFailure := errors.New("profiles: read unavailable")
	for _, testCase := range []struct {
		name       string
		accounts   fakeAccounts
		preference *fakePreferences
		want       error
	}{
		{"account read", fakeAccounts{err: readFailure}, &fakePreferences{}, readFailure},
		{"preference read", fakeAccounts{account: account(t, "account-1")}, &fakePreferences{err: preferenceFailure}, preferenceFailure},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			directory, err := outbox.NewDirectory(testCase.accounts, testCase.preference)
			if err != nil {
				t.Fatalf("NewDirectory() error = %v", err)
			}
			if _, err := directory.AccountForAddress(context.Background(), "ana.silva@example.com"); !errors.Is(err, testCase.want) {
				t.Errorf("AccountForAddress() error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestDirectoryRequiresBothReadPaths(t *testing.T) {
	if _, err := outbox.NewDirectory(nil, &fakePreferences{}); !errors.Is(err, domain.ErrMissingDependency) {
		t.Errorf("NewDirectory(nil, preferences) error = %v, want ErrMissingDependency", err)
	}
	if _, err := outbox.NewDirectory(fakeAccounts{}, nil); !errors.Is(err, domain.ErrMissingDependency) {
		t.Errorf("NewDirectory(accounts, nil) error = %v, want ErrMissingDependency", err)
	}
	var directory *outbox.Directory
	if _, err := directory.AccountForAddress(context.Background(), "ana@example.com"); !errors.Is(err, domain.ErrMissingDependency) {
		t.Errorf("nil directory error = %v, want ErrMissingDependency", err)
	}
}
