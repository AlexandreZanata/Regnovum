package administrationtarget_test

// The adapter that answers the moderation module's administrative question
// (P19-T09). It translates and decides nothing, so what these tests pin is the
// translation: which identity fact becomes which moderation fact, and which
// failures are facts instead of errors.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/administrationtarget"
	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	moderationapp "github.com/AlexandreZanata/Regnovum/internal/moderation/application"
)

const targetAccount identitydomain.AccountID = "0192f3a0-0000-7000-8000-00000000tgt1"

type fakeAccounts struct {
	account *identitydomain.Account
	err     error
}

func (f *fakeAccounts) GetAccountByEmail(_ context.Context, _ identitydomain.Email) (*identitydomain.Account, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.account, nil
}

type fakeEnrollments struct {
	enrollment *identityapp.MFAEnrollmentRecord
	err        error
}

func (f *fakeEnrollments) GetMFAEnrollment(_ context.Context, _ identitydomain.AccountID) (*identityapp.MFAEnrollmentRecord, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.enrollment, nil
}

// account builds an identity account with the facts the adapter reads. The
// status is passed as the domain's own constructor expects it.
func account(t *testing.T, status identitydomain.AccountStatus, verified bool) *identitydomain.Account {
	t.Helper()

	var verifiedAt *time.Time
	if verified {
		instant := time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)
		verifiedAt = &instant
	}
	address, err := identitydomain.ParseEmail("first@arena.example.com")
	if err != nil {
		t.Fatalf("ParseEmail: %v", err)
	}
	built, err := identitydomain.ReconstituteAccount(
		targetAccount,
		address,
		status,
		verifiedAt,
		time.Date(2026, 9, 19, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("ReconstituteAccount: %v", err)
	}
	return built
}

func TestLookupByEmailReportsTheFactsAPromotionIsDecidedOn(t *testing.T) {
	confirmedAt := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	directory, err := administrationtarget.NewDirectory(
		&fakeAccounts{account: account(t, identitydomain.AccountStatusActive, true)},
		&fakeEnrollments{enrollment: &identityapp.MFAEnrollmentRecord{ConfirmedAt: &confirmedAt}},
	)
	if err != nil {
		t.Fatalf("NewDirectory: %v", err)
	}

	target, err := directory.LookupByEmail(context.Background(), "first@arena.example.com")
	if err != nil {
		t.Fatalf("LookupByEmail: %v", err)
	}
	if string(target.AccountID) != string(targetAccount) {
		t.Fatalf("AccountID = %q, want %q", target.AccountID, targetAccount)
	}
	if !target.EmailVerified || !target.SecondFactorConfirmed || !target.CanAuthenticate {
		t.Fatalf("target = %+v, want every requirement met", target)
	}
}

func TestLookupByEmailReportsEachUnmetRequirementAsAFact(t *testing.T) {
	confirmedAt := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)

	cases := []struct {
		name        string
		accounts    *fakeAccounts
		enrollments *fakeEnrollments
		expect      func(*testing.T, *moderationapp.AdministrationTarget)
	}{
		{
			name:        "a pending address is reported as unverified",
			accounts:    &fakeAccounts{account: account(t, identitydomain.AccountStatusPending, false)},
			enrollments: &fakeEnrollments{enrollment: &identityapp.MFAEnrollmentRecord{ConfirmedAt: &confirmedAt}},
			expect: func(t *testing.T, target *moderationapp.AdministrationTarget) {
				if target.EmailVerified || target.CanAuthenticate {
					t.Fatalf("target = %+v, want an unverified account that cannot sign in", target)
				}
			},
		},
		{
			name:        "a suspended account keeps its verified address and cannot sign in",
			accounts:    &fakeAccounts{account: account(t, identitydomain.AccountStatusSuspended, true)},
			enrollments: &fakeEnrollments{enrollment: &identityapp.MFAEnrollmentRecord{ConfirmedAt: &confirmedAt}},
			expect: func(t *testing.T, target *moderationapp.AdministrationTarget) {
				if !target.EmailVerified || target.CanAuthenticate {
					t.Fatalf("target = %+v, want the verified address and no ability to sign in", target)
				}
			},
		},
		{
			name:        "a pending enrollment is reported as no confirmed factor",
			accounts:    &fakeAccounts{account: account(t, identitydomain.AccountStatusActive, true)},
			enrollments: &fakeEnrollments{enrollment: &identityapp.MFAEnrollmentRecord{}},
			expect: func(t *testing.T, target *moderationapp.AdministrationTarget) {
				if target.SecondFactorConfirmed {
					t.Fatalf("target = %+v, want no confirmed second factor", target)
				}
			},
		},
		{
			name:        "a missing enrollment is a fact, not a failure",
			accounts:    &fakeAccounts{account: account(t, identitydomain.AccountStatusActive, true)},
			enrollments: &fakeEnrollments{err: identityapp.ErrMFAEnrollmentMissing},
			expect: func(t *testing.T, target *moderationapp.AdministrationTarget) {
				if target.SecondFactorConfirmed {
					t.Fatalf("target = %+v, want no confirmed second factor", target)
				}
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			directory, err := administrationtarget.NewDirectory(testCase.accounts, testCase.enrollments)
			if err != nil {
				t.Fatalf("NewDirectory: %v", err)
			}
			target, err := directory.LookupByEmail(context.Background(), "first@arena.example.com")
			if err != nil {
				t.Fatalf("LookupByEmail: %v", err)
			}
			testCase.expect(t, target)
		})
	}
}

func TestLookupByEmailRefusesInTheModerationVocabulary(t *testing.T) {
	directory, err := administrationtarget.NewDirectory(
		&fakeAccounts{err: identityapp.ErrAccountNotFound},
		&fakeEnrollments{},
	)
	if err != nil {
		t.Fatalf("NewDirectory: %v", err)
	}

	if _, err := directory.LookupByEmail(context.Background(), "nobody@arena.example.com"); !errors.Is(err, moderationapp.ErrAdministrationTargetNotFound) {
		t.Fatalf("err = %v, want %v", err, moderationapp.ErrAdministrationTargetNotFound)
	}

	if _, err := directory.LookupByEmail(context.Background(), "not an address"); !errors.Is(err, moderationapp.ErrInvalidAdministrationTarget) {
		t.Fatalf("err = %v, want %v", err, moderationapp.ErrInvalidAdministrationTarget)
	}
}

func TestLookupByEmailDoesNotTurnAFailedReadIntoAnAbsentFact(t *testing.T) {
	directory, err := administrationtarget.NewDirectory(
		&fakeAccounts{account: account(t, identitydomain.AccountStatusActive, true)},
		&fakeEnrollments{err: errors.New("the second factor store is unavailable")},
	)
	if err != nil {
		t.Fatalf("NewDirectory: %v", err)
	}

	_, err = directory.LookupByEmail(context.Background(), "first@arena.example.com")
	if err == nil {
		t.Fatal("expected the unreadable second factor to surface")
	}
	if errors.Is(err, moderationapp.ErrAdministrationTargetWithoutSecondFactor) {
		t.Fatal("an unreadable factor was reported as an absent factor")
	}
}

func TestLookupByEmailRefusesAnAccountTheRepositoryDidNotReturn(t *testing.T) {
	directory, err := administrationtarget.NewDirectory(&fakeAccounts{}, &fakeEnrollments{})
	if err != nil {
		t.Fatalf("NewDirectory: %v", err)
	}

	if _, err := directory.LookupByEmail(context.Background(), "first@arena.example.com"); !errors.Is(err, moderationapp.ErrAdministrationTargetNotFound) {
		t.Fatalf("err = %v, want %v", err, moderationapp.ErrAdministrationTargetNotFound)
	}
}

func TestNewDirectoryFailsClosed(t *testing.T) {
	if _, err := administrationtarget.NewDirectory(nil, &fakeEnrollments{}); !errors.Is(err, administrationtarget.ErrIncompleteComposition) {
		t.Fatalf("err = %v, want %v", err, administrationtarget.ErrIncompleteComposition)
	}
	if _, err := administrationtarget.NewDirectory(&fakeAccounts{}, nil); !errors.Is(err, administrationtarget.ErrIncompleteComposition) {
		t.Fatalf("err = %v, want %v", err, administrationtarget.ErrIncompleteComposition)
	}
}
