package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

func mustEmail(t *testing.T, raw string) domain.Email {
	t.Helper()
	e, err := domain.ParseEmail(raw)
	if err != nil {
		t.Fatalf("mustEmail(%q): %v", raw, err)
	}
	return e
}

func TestNewAccountCreation(t *testing.T) {
	email := mustEmail(t, "alice@example.com")
	now := time.Unix(1700000000, 0).UTC()

	// 1. Success case
	acc, err := domain.NewAccount("acc_0123456789", email, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if acc.ID() != "acc_0123456789" {
		t.Errorf("ID() = %q, want acc_0123456789", acc.ID())
	}
	if !acc.Email().Equals(email) {
		t.Errorf("Email() = %v, want %v", acc.Email(), email)
	}
	if acc.Status() != domain.AccountStatusPending {
		t.Errorf("Status() = %q, want pending", acc.Status())
	}
	if acc.IsVerified() {
		t.Error("new account should not be verified")
	}
	if acc.EmailVerifiedAt() != nil {
		t.Errorf("EmailVerifiedAt() = %v, want nil", acc.EmailVerifiedAt())
	}
	if !acc.CreatedAt().Equal(now) {
		t.Errorf("CreatedAt() = %v, want %v", acc.CreatedAt(), now)
	}
	if !acc.UpdatedAt().Equal(now) {
		t.Errorf("UpdatedAt() = %v, want %v", acc.UpdatedAt(), now)
	}

	// 2. Empty ID error
	_, err = domain.NewAccount("", email, now)
	if !errors.Is(err, domain.ErrEmptyAccountID) {
		t.Errorf("expected ErrEmptyAccountID, got %v", err)
	}

	// 3. Zero email error
	var zeroEmail domain.Email
	_, err = domain.NewAccount("acc_123", zeroEmail, now)
	if !errors.Is(err, domain.ErrEmptyEmail) {
		t.Errorf("expected ErrEmptyEmail, got %v", err)
	}
}

func TestAccountStateTransitionMatrix(t *testing.T) {
	email := mustEmail(t, "matrix@example.com")
	t0 := time.Unix(1700000000, 0).UTC()
	t1 := t0.Add(1 * time.Hour)

	// 1. Pending transitions
	t.Run("from Pending", func(t *testing.T) {
		// Pending -> Active (Allowed)
		acc, _ := domain.NewAccount("acc_p1", email, t0)
		if err := acc.VerifyEmail(t1); err != nil {
			t.Fatalf("Pending -> Active should be allowed: %v", err)
		}
		if acc.Status() != domain.AccountStatusActive {
			t.Errorf("Status = %v, want Active", acc.Status())
		}
		if acc.EmailVerifiedAt() == nil || !acc.EmailVerifiedAt().Equal(t1) {
			t.Errorf("EmailVerifiedAt = %v, want %v", acc.EmailVerifiedAt(), t1)
		}

		// Pending -> Suspended (Forbidden)
		acc, _ = domain.NewAccount("acc_p2", email, t0)
		if err := acc.Suspend(t1); !errors.Is(err, domain.ErrInvalidAccountTransition) {
			t.Errorf("Pending -> Suspended should be forbidden, got: %v", err)
		}

		// Pending -> Deleted (Allowed)
		acc, _ = domain.NewAccount("acc_p3", email, t0)
		if err := acc.MarkDeleted(t1); err != nil {
			t.Fatalf("Pending -> Deleted should be allowed: %v", err)
		}
		if acc.Status() != domain.AccountStatusDeleted {
			t.Errorf("Status = %v, want Deleted", acc.Status())
		}
	})

	// 2. Active transitions
	t.Run("from Active", func(t *testing.T) {
		newActive := func() *domain.Account {
			acc, _ := domain.NewAccount("acc_act", email, t0)
			_ = acc.VerifyEmail(t0)
			return acc
		}

		// Active -> Suspended (Allowed)
		acc := newActive()
		if err := acc.Suspend(t1); err != nil {
			t.Fatalf("Active -> Suspended should be allowed: %v", err)
		}
		if acc.Status() != domain.AccountStatusSuspended {
			t.Errorf("Status = %v, want Suspended", acc.Status())
		}

		// Active -> Deleted (Allowed)
		acc = newActive()
		if err := acc.MarkDeleted(t1); err != nil {
			t.Fatalf("Active -> Deleted should be allowed: %v", err)
		}
		if acc.Status() != domain.AccountStatusDeleted {
			t.Errorf("Status = %v, want Deleted", acc.Status())
		}

		// Active -> Active / VerifyEmail again (Forbidden: ErrAccountAlreadyVerified)
		acc = newActive()
		if err := acc.VerifyEmail(t1); !errors.Is(err, domain.ErrAccountAlreadyVerified) {
			t.Errorf("Active -> Active verify should return ErrAccountAlreadyVerified, got: %v", err)
		}

		// Active -> Unsuspend (Forbidden: not suspended)
		acc = newActive()
		if err := acc.Unsuspend(t1); !errors.Is(err, domain.ErrInvalidAccountTransition) {
			t.Errorf("Active -> Unsuspend should return ErrInvalidAccountTransition, got: %v", err)
		}
	})

	// 3. Suspended transitions
	t.Run("from Suspended", func(t *testing.T) {
		newSuspended := func() *domain.Account {
			acc, _ := domain.NewAccount("acc_susp", email, t0)
			_ = acc.VerifyEmail(t0)
			_ = acc.Suspend(t0)
			return acc
		}

		// Suspended -> Active (Allowed via Unsuspend)
		acc := newSuspended()
		if err := acc.Unsuspend(t1); err != nil {
			t.Fatalf("Suspended -> Active should be allowed: %v", err)
		}
		if acc.Status() != domain.AccountStatusActive {
			t.Errorf("Status = %v, want Active", acc.Status())
		}

		// Suspended -> Deleted (Allowed)
		acc = newSuspended()
		if err := acc.MarkDeleted(t1); err != nil {
			t.Fatalf("Suspended -> Deleted should be allowed: %v", err)
		}
		if acc.Status() != domain.AccountStatusDeleted {
			t.Errorf("Status = %v, want Deleted", acc.Status())
		}

		// Suspended -> VerifyEmail (Forbidden: ErrAccountSuspended)
		acc = newSuspended()
		if err := acc.VerifyEmail(t1); !errors.Is(err, domain.ErrAccountSuspended) {
			t.Errorf("Suspended -> VerifyEmail should return ErrAccountSuspended, got: %v", err)
		}

		// Suspended -> Suspend again (Forbidden)
		acc = newSuspended()
		if err := acc.Suspend(t1); !errors.Is(err, domain.ErrInvalidAccountTransition) {
			t.Errorf("Suspended -> Suspend should return ErrInvalidAccountTransition, got: %v", err)
		}
	})

	// 4. Deleted transitions (Terminal State: All Forbidden)
	t.Run("from Deleted (terminal state)", func(t *testing.T) {
		newDeleted := func() *domain.Account {
			acc, _ := domain.NewAccount("acc_del", email, t0)
			_ = acc.MarkDeleted(t0)
			return acc
		}

		// Deleted -> VerifyEmail
		acc := newDeleted()
		if err := acc.VerifyEmail(t1); !errors.Is(err, domain.ErrAccountDeleted) {
			t.Errorf("Deleted -> VerifyEmail should return ErrAccountDeleted, got: %v", err)
		}

		// Deleted -> Suspend
		acc = newDeleted()
		if err := acc.Suspend(t1); !errors.Is(err, domain.ErrAccountDeleted) {
			t.Errorf("Deleted -> Suspend should return ErrAccountDeleted, got: %v", err)
		}

		// Deleted -> Unsuspend
		acc = newDeleted()
		if err := acc.Unsuspend(t1); !errors.Is(err, domain.ErrAccountDeleted) {
			t.Errorf("Deleted -> Unsuspend should return ErrAccountDeleted, got: %v", err)
		}

		// Deleted -> MarkDeleted again
		acc = newDeleted()
		if err := acc.MarkDeleted(t1); !errors.Is(err, domain.ErrAccountDeleted) {
			t.Errorf("Deleted -> MarkDeleted should return ErrAccountDeleted, got: %v", err)
		}

		// Deleted -> ChangeEmail
		acc = newDeleted()
		newEmail := mustEmail(t, "new@example.com")
		if err := acc.ChangeEmail(newEmail, t1); !errors.Is(err, domain.ErrAccountDeleted) {
			t.Errorf("Deleted -> ChangeEmail should return ErrAccountDeleted, got: %v", err)
		}
	})
}

func TestAuthenticationAndVerificationEligibility(t *testing.T) {
	email := mustEmail(t, "eligibility@example.com")
	t0 := time.Unix(1700000000, 0).UTC()

	// 1. Pending
	accP, _ := domain.NewAccount("acc_p", email, t0)
	if accP.CanAuthenticate() {
		t.Error("Pending account must not be permitted to authenticate")
	}
	if !accP.IsEligibleForVerification() {
		t.Error("Pending account must be eligible for verification")
	}

	// 2. Active
	accA, _ := domain.NewAccount("acc_a", email, t0)
	_ = accA.VerifyEmail(t0)
	if !accA.CanAuthenticate() {
		t.Error("Active account must be permitted to authenticate")
	}
	if accA.IsEligibleForVerification() {
		t.Error("Active account must not be eligible for verification")
	}

	// 3. Suspended
	accS, _ := domain.NewAccount("acc_s", email, t0)
	_ = accS.VerifyEmail(t0)
	_ = accS.Suspend(t0)
	if accS.CanAuthenticate() {
		t.Error("Suspended account must not be permitted to authenticate")
	}
	if accS.IsEligibleForVerification() {
		t.Error("Suspended account must not be eligible for verification")
	}

	// 4. Deleted
	accD, _ := domain.NewAccount("acc_d", email, t0)
	_ = accD.MarkDeleted(t0)
	if accD.CanAuthenticate() {
		t.Error("Deleted account must not be permitted to authenticate")
	}
	if accD.IsEligibleForVerification() {
		t.Error("Deleted account must not be eligible for verification")
	}
}

func TestChangeEmail(t *testing.T) {
	email1 := mustEmail(t, "old@example.com")
	email2 := mustEmail(t, "new@example.com")
	t0 := time.Unix(1700000000, 0).UTC()
	t1 := t0.Add(2 * time.Hour)

	acc, _ := domain.NewAccount("acc_chg", email1, t0)
	_ = acc.VerifyEmail(t0)

	// Change email on Active account
	if err := acc.ChangeEmail(email2, t1); err != nil {
		t.Fatalf("change email failed: %v", err)
	}

	if !acc.Email().Equals(email2) {
		t.Errorf("Email() = %v, want %v", acc.Email(), email2)
	}
	if acc.Status() != domain.AccountStatusPending {
		t.Errorf("Status() = %v, want Pending after email change", acc.Status())
	}
	if acc.IsVerified() {
		t.Error("account must not be verified after email change")
	}
	if acc.EmailVerifiedAt() != nil {
		t.Error("EmailVerifiedAt must be reset to nil")
	}
}

func TestReconstituteAccount(t *testing.T) {
	email := mustEmail(t, "reconstitute@example.com")
	t0 := time.Unix(1700000000, 0).UTC()
	tVerified := t0.Add(30 * time.Minute)

	// Valid reconstitution
	acc, err := domain.ReconstituteAccount(
		"acc_rec_123",
		email,
		domain.AccountStatusActive,
		&tVerified,
		t0,
		tVerified,
	)
	if err != nil {
		t.Fatalf("reconstitute failed: %v", err)
	}
	if acc.ID() != "acc_rec_123" || acc.Status() != domain.AccountStatusActive || !acc.IsVerified() {
		t.Errorf("reconstituted account mismatch: %+v", acc)
	}

	// Invalid status
	_, err = domain.ReconstituteAccount("acc_rec_123", email, "invalid_status", nil, t0, t0)
	if !errors.Is(err, domain.ErrInvalidAccountStatus) {
		t.Errorf("expected ErrInvalidAccountStatus, got %v", err)
	}
}

func TestVerificationPolicy(t *testing.T) {
	policy := domain.DefaultVerificationPolicy()
	issuedAt := time.Unix(1700000000, 0).UTC()

	// 23 hours later: not expired
	if policy.IsExpired(issuedAt, issuedAt.Add(23*time.Hour)) {
		t.Error("token at 23h should not be expired")
	}
	// 24 hours + 1s later: expired
	if !policy.IsExpired(issuedAt, issuedAt.Add(24*time.Hour+1*time.Second)) {
		t.Error("token at 24h+1s should be expired")
	}

	email := mustEmail(t, "p@example.com")
	accPending, _ := domain.NewAccount("acc_p", email, issuedAt)
	if err := policy.CanIssueVerification(accPending); err != nil {
		t.Errorf("CanIssueVerification on pending should be nil, got: %v", err)
	}

	accActive, _ := domain.NewAccount("acc_a", email, issuedAt)
	_ = accActive.VerifyEmail(issuedAt)
	if err := policy.CanIssueVerification(accActive); !errors.Is(err, domain.ErrAccountAlreadyVerified) {
		t.Errorf("CanIssueVerification on active should return ErrAccountAlreadyVerified, got: %v", err)
	}
}

func TestPasswordResetPolicy(t *testing.T) {
	policy := domain.DefaultPasswordResetPolicy()
	issuedAt := time.Unix(1700000000, 0).UTC()

	// 14 minutes later: not expired
	if policy.IsExpired(issuedAt, issuedAt.Add(14*time.Minute)) {
		t.Error("reset token at 14m should not be expired")
	}
	// 15 minutes + 1s later: expired
	if !policy.IsExpired(issuedAt, issuedAt.Add(15*time.Minute+1*time.Second)) {
		t.Error("reset token at 15m+1s should be expired")
	}

	email := mustEmail(t, "reset@example.com")
	accActive, _ := domain.NewAccount("acc_a", email, issuedAt)
	_ = accActive.VerifyEmail(issuedAt)
	if err := policy.CanIssueReset(accActive); err != nil {
		t.Errorf("CanIssueReset on active should be nil, got: %v", err)
	}

	accSuspended, _ := domain.NewAccount("acc_s", email, issuedAt)
	_ = accSuspended.VerifyEmail(issuedAt)
	_ = accSuspended.Suspend(issuedAt)
	if err := policy.CanIssueReset(accSuspended); !errors.Is(err, domain.ErrAccountSuspended) {
		t.Errorf("CanIssueReset on suspended should return ErrAccountSuspended, got: %v", err)
	}
}

func TestSessionPolicy(t *testing.T) {
	policy := domain.DefaultSessionPolicy()
	createdAt := time.Unix(1700000000, 0).UTC()
	lastSeenAt := createdAt

	// Active after 1 hour
	now := createdAt.Add(1 * time.Hour)
	if policy.IsExpired(createdAt, lastSeenAt, now) {
		t.Error("session after 1h should not be expired")
	}

	// Inactive for 25 hours -> expired via idle timeout
	nowIdleExpired := lastSeenAt.Add(25 * time.Hour)
	if !policy.IsExpired(createdAt, lastSeenAt, nowIdleExpired) {
		t.Error("session after 25h idle should be expired")
	}

	// Kept active every hour for 15 days -> expired via absolute lifetime (14 days)
	lastSeenActive := createdAt.Add(15 * 24 * time.Hour)
	nowAbsoluteExpired := lastSeenActive
	if !policy.IsExpired(createdAt, lastSeenActive, nowAbsoluteExpired) {
		t.Error("session after 15 days total should be expired via absolute lifetime")
	}
}
