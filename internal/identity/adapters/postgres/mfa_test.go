package postgres_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	identitypg "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

// The single-use guarantees of the second factor (P16-T05) are enforced by the
// statements, not by the order in which a use case happens to run, so they are
// proved here — against PostgreSQL — one statement at a time: the enrollment
// that refuses to be replaced once confirmed, the step that only moves forward,
// the recovery code that is spent exactly once, and the elevation that only
// lands on a session that still exists.

func TestRepository_MFAEnrollmentPersistence(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	repo := identitypg.NewRepository(testDB.Pool.Pool())

	email, err := domain.ParseEmail("mfa-enrollment@arena.example.com")
	if err != nil {
		t.Fatalf("parse email: %v", err)
	}
	account, err := repo.CreateAccountWithPassword(ctx, email, "$argon2id$v=19$m=8192,t=1,p=1$fakeSalt$fakeHash")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	// An account without a row has no enrollment: that is a state the caller
	// names, not a storage failure.
	if _, err := repo.GetMFAEnrollment(ctx, account.ID()); !errors.Is(err, application.ErrMFAEnrollmentMissing) {
		t.Fatalf("GetMFAEnrollment before enrollment = %v, want ErrMFAEnrollmentMissing", err)
	}

	pending, err := repo.UpsertPendingMFAEnrollment(ctx, account.ID(), []byte("sealed-secret-one"))
	if err != nil {
		t.Fatalf("upsert pending enrollment: %v", err)
	}
	if pending.ConfirmedAt != nil {
		t.Fatalf("a fresh enrollment is confirmed: %v", *pending.ConfirmedAt)
	}
	if pending.LastAcceptedStep != -1 {
		t.Fatalf("last accepted step = %d, want -1 (no step accepted yet)", pending.LastAcceptedStep)
	}

	// Restarting a pending enrollment is allowed: a user who lost the QR code
	// before confirming is not stuck, and the secret is replaced.
	restarted, err := repo.UpsertPendingMFAEnrollment(ctx, account.ID(), []byte("sealed-secret-two"))
	if err != nil {
		t.Fatalf("restart pending enrollment: %v", err)
	}
	if string(restarted.SecretSealed) != "sealed-secret-two" {
		t.Fatalf("restarted secret = %q, want the new secret", restarted.SecretSealed)
	}

	confirmedAt := time.Now().UTC().Truncate(time.Millisecond)
	confirmed, err := repo.ConfirmMFAEnrollment(ctx, account.ID(), confirmedAt, 41234)
	if err != nil {
		t.Fatalf("confirm enrollment: %v", err)
	}
	if !confirmed {
		t.Fatal("the first confirmation did not happen")
	}
	stored, err := repo.GetMFAEnrollment(ctx, account.ID())
	if err != nil {
		t.Fatalf("read confirmed enrollment: %v", err)
	}
	if stored.ConfirmedAt == nil || !stored.ConfirmedAt.Equal(confirmedAt) {
		t.Fatalf("stored confirmation = %v, want %v", stored.ConfirmedAt, confirmedAt)
	}
	if stored.LastAcceptedStep != 41234 {
		t.Fatalf("stored step = %d, want the confirming step 41234", stored.LastAcceptedStep)
	}

	// The confirmation is a claim: the second one does not happen.
	if again, err := repo.ConfirmMFAEnrollment(ctx, account.ID(), confirmedAt.Add(time.Minute), 41235); err != nil || again {
		t.Fatalf("second confirmation = (%v, %v), want (false, nil)", again, err)
	}

	// And a confirmed enrollment cannot be replaced by starting again: that is
	// what stops "begin enrollment" from being a way to substitute a factor.
	if _, err := repo.UpsertPendingMFAEnrollment(ctx, account.ID(), []byte("sealed-secret-three")); !errors.Is(err, application.ErrMFAAlreadyEnrolled) {
		t.Fatalf("upsert over a confirmed enrollment = %v, want ErrMFAAlreadyEnrolled", err)
	}
}

func TestRepository_MFAStepOnlyMovesForward(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	repo := identitypg.NewRepository(testDB.Pool.Pool())

	email, err := domain.ParseEmail("mfa-step@arena.example.com")
	if err != nil {
		t.Fatalf("parse email: %v", err)
	}
	account, err := repo.CreateAccountWithPassword(ctx, email, "$argon2id$v=19$m=8192,t=1,p=1$fakeSalt$fakeHash")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if _, err := repo.UpsertPendingMFAEnrollment(ctx, account.ID(), []byte("sealed-secret")); err != nil {
		t.Fatalf("upsert pending enrollment: %v", err)
	}

	// A pending enrollment rests on the sentinel -1, which no real step equals,
	// so the first acceptance moves the cursor off it and every attempt at or
	// below the cursor loses.
	if advanced, err := repo.AdvanceMFAVerifiedStep(ctx, account.ID(), -1); err != nil || advanced {
		t.Fatalf("advance to the sentinel step = (%v, %v), want (false, nil)", advanced, err)
	}
	if advanced, err := repo.AdvanceMFAVerifiedStep(ctx, account.ID(), 0); err != nil || !advanced {
		t.Fatalf("first real step = (%v, %v), want (true, nil)", advanced, err)
	}
	if advanced, err := repo.AdvanceMFAVerifiedStep(ctx, account.ID(), 1000); err != nil || !advanced {
		t.Fatalf("advance to step 1000 = (%v, %v), want (true, nil)", advanced, err)
	}
	if advanced, err := repo.AdvanceMFAVerifiedStep(ctx, account.ID(), 1000); err != nil || advanced {
		t.Fatalf("advance to the same step = (%v, %v), want (false, nil)", advanced, err)
	}
	if advanced, err := repo.AdvanceMFAVerifiedStep(ctx, account.ID(), 999); err != nil || advanced {
		t.Fatalf("advance backwards = (%v, %v), want (false, nil)", advanced, err)
	}
	if advanced, err := repo.AdvanceMFAVerifiedStep(ctx, account.ID(), 1001); err != nil || !advanced {
		t.Fatalf("advance one step forward = (%v, %v), want (true, nil)", advanced, err)
	}

	// An account with no enrollment row has no step to advance.
	otherEmail, err := domain.ParseEmail("mfa-step-other@arena.example.com")
	if err != nil {
		t.Fatalf("parse email: %v", err)
	}
	other, err := repo.CreateAccountWithPassword(ctx, otherEmail, "$argon2id$v=19$m=8192,t=1,p=1$fakeSalt$fakeHash")
	if err != nil {
		t.Fatalf("create second account: %v", err)
	}
	if advanced, err := repo.AdvanceMFAVerifiedStep(ctx, other.ID(), 10); err != nil || advanced {
		t.Fatalf("advance without an enrollment = (%v, %v), want (false, nil)", advanced, err)
	}
}

func TestRepository_MFABackupCodesAreSingleUse(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	repo := identitypg.NewRepository(testDB.Pool.Pool())

	email, err := domain.ParseEmail("mfa-codes@arena.example.com")
	if err != nil {
		t.Fatalf("parse email: %v", err)
	}
	account, err := repo.CreateAccountWithPassword(ctx, email, "$argon2id$v=19$m=8192,t=1,p=1$fakeSalt$fakeHash")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	hashes := []string{"$argon2id$hash-one", "$argon2id$hash-two", "$argon2id$hash-three"}
	if err := repo.ReplaceMFABackupCodes(ctx, account.ID(), hashes); err != nil {
		t.Fatalf("replace backup codes: %v", err)
	}
	codes, err := repo.ListUnusedMFABackupCodes(ctx, account.ID())
	if err != nil {
		t.Fatalf("list backup codes: %v", err)
	}
	if len(codes) != len(hashes) {
		t.Fatalf("unused codes = %d, want %d", len(codes), len(hashes))
	}
	for index, record := range codes {
		if record.CodeHash != hashes[index] {
			t.Fatalf("code %d hash = %q, want %q", index, record.CodeHash, hashes[index])
		}
		if record.ID == "" {
			t.Fatalf("code %d has no identifier", index)
		}
	}

	usedAt := time.Now().UTC().Truncate(time.Millisecond)
	spent, err := repo.ConsumeMFABackupCode(ctx, codes[0].ID, usedAt)
	if err != nil || !spent {
		t.Fatalf("first consumption = (%v, %v), want (true, nil)", spent, err)
	}
	if spentAgain, err := repo.ConsumeMFABackupCode(ctx, codes[0].ID, usedAt.Add(time.Second)); err != nil || spentAgain {
		t.Fatalf("second consumption = (%v, %v), want (false, nil)", spentAgain, err)
	}
	if spentUnknown, err := repo.ConsumeMFABackupCode(ctx, "00000000-0000-0000-0000-000000000000", usedAt); err != nil || spentUnknown {
		t.Fatalf("consuming an unknown code = (%v, %v), want (false, nil)", spentUnknown, err)
	}

	remaining, err := repo.ListUnusedMFABackupCodes(ctx, account.ID())
	if err != nil {
		t.Fatalf("list after consumption: %v", err)
	}
	if len(remaining) != len(hashes)-1 {
		t.Fatalf("unused codes after one consumption = %d, want %d", len(remaining), len(hashes)-1)
	}

	// Replacing the set is how a re-enrollment resets the recovery path: the
	// spent code is gone and only the new set remains.
	if err := repo.ReplaceMFABackupCodes(ctx, account.ID(), []string{"$argon2id$hash-new"}); err != nil {
		t.Fatalf("replace backup codes again: %v", err)
	}
	replaced, err := repo.ListUnusedMFABackupCodes(ctx, account.ID())
	if err != nil {
		t.Fatalf("list after replacement: %v", err)
	}
	if len(replaced) != 1 || replaced[0].CodeHash != "$argon2id$hash-new" {
		t.Fatalf("unused codes after replacement = %v, want exactly the new set", replaced)
	}
}

func TestRepository_MFASessionElevation(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	repo := identitypg.NewRepository(testDB.Pool.Pool())
	pool := testDB.Pool.Pool()

	email, err := domain.ParseEmail("mfa-session@arena.example.com")
	if err != nil {
		t.Fatalf("parse email: %v", err)
	}
	account, err := repo.CreateAccountWithPassword(ctx, email, "$argon2id$v=19$m=8192,t=1,p=1$fakeSalt$fakeHash")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	now := time.Now().UTC()
	liveHash := sha256.Sum256([]byte("mfa-session-live"))
	live, err := repo.CreateSession(ctx, account.ID(), liveHash[:], now.Add(time.Hour), "198.51.100.7", "ArenaClient/1.0")
	if err != nil {
		t.Fatalf("create live session: %v", err)
	}
	revokedHash := sha256.Sum256([]byte("mfa-session-revoked"))
	revoked, err := repo.CreateSession(ctx, account.ID(), revokedHash[:], now.Add(time.Hour), "198.51.100.8", "ArenaClient/1.0")
	if err != nil {
		t.Fatalf("create revoked session: %v", err)
	}
	if err := repo.RevokeSession(ctx, revokedHash[:]); err != nil {
		t.Fatalf("revoke session: %v", err)
	}

	verifiedAt := now.Truncate(time.Millisecond)
	elevated, err := repo.MarkSessionMFAVerified(ctx, live.ID(), verifiedAt)
	if err != nil || !elevated {
		t.Fatalf("elevate live session = (%v, %v), want (true, nil)", elevated, err)
	}

	var stored time.Time
	if err := pool.QueryRow(ctx, `SELECT mfa_verified_at FROM app.sessions WHERE id = $1`, live.ID().String()).Scan(&stored); err != nil {
		t.Fatalf("read elevation: %v", err)
	}
	if !stored.UTC().Equal(verifiedAt) {
		t.Fatalf("stored elevation = %v, want %v", stored.UTC(), verifiedAt)
	}

	// A revoked session cannot be elevated: revocation is what makes a stolen
	// session useless, and an elevation that ignored it would undo exactly that.
	if elevated, err := repo.MarkSessionMFAVerified(ctx, revoked.ID(), verifiedAt); err != nil || elevated {
		t.Fatalf("elevate revoked session = (%v, %v), want (false, nil)", elevated, err)
	}
	// A session that does not exist cannot be elevated either.
	if elevated, err := repo.MarkSessionMFAVerified(ctx, "00000000-0000-0000-0000-000000000000", verifiedAt); err != nil || elevated {
		t.Fatalf("elevate unknown session = (%v, %v), want (false, nil)", elevated, err)
	}
}
