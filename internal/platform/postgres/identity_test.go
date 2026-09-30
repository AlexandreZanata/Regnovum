package postgres_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func sha256Bytes(data string) []byte {
	h := sha256.Sum256([]byte(data))
	return h[:]
}

func TestEmailCaseInsensitiveUniqueness(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	// 1. Create initial account with mixed-case email
	acc1, err := q.CreateAccount(ctx, postgres.CreateAccountParams{
		Email:  "TestUser@Arena.Example.Com",
		Status: "pending",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if !acc1.ID.Valid {
		t.Fatal("expected valid account ID")
	}

	// 2. Attempt to create second account with same email in lowercase
	_, err = q.CreateAccount(ctx, postgres.CreateAccountParams{
		Email:  "testuser@arena.example.com",
		Status: "pending",
	})
	if err == nil {
		t.Fatal("expected duplicate email error for lowercase variant, got nil")
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected *pgconn.PgError, got %T: %v", err, err)
	}
	if pgErr.Code != "23505" {
		t.Errorf("pgErr.Code = %q, want 23505 (unique_violation)", pgErr.Code)
	}

	// 3. Attempt to create third account with uppercase variant
	_, err = q.CreateAccount(ctx, postgres.CreateAccountParams{
		Email:  "TESTUSER@ARENA.EXAMPLE.COM",
		Status: "pending",
	})
	if err == nil {
		t.Fatal("expected duplicate email error for uppercase variant, got nil")
	}
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Errorf("expected 23505 unique_violation, got %v", err)
	}
}

func TestTokenAndSessionUniqueness(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	acc, err := q.CreateAccount(ctx, postgres.CreateAccountParams{
		Email:  "tokens@example.com",
		Status: "active",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	sharedHash := sha256Bytes("fixed-token-seed-12345")
	expiry := pgtype.Timestamptz{Time: time.Now().Add(1 * time.Hour), Valid: true}

	// 1. Email verification token uniqueness
	_, err = q.CreateEmailVerificationToken(ctx, postgres.CreateEmailVerificationTokenParams{
		AccountID: acc.ID,
		TokenHash: sharedHash,
		ExpiresAt: expiry,
	})
	if err != nil {
		t.Fatalf("first verification token: %v", err)
	}

	_, err = q.CreateEmailVerificationToken(ctx, postgres.CreateEmailVerificationTokenParams{
		AccountID: acc.ID,
		TokenHash: sharedHash,
		ExpiresAt: expiry,
	})
	if err == nil {
		t.Fatal("expected unique_violation for duplicate email verification token hash")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Errorf("expected 23505 unique_violation, got %v", err)
	}

	// 2. Password reset token uniqueness
	_, err = q.CreatePasswordResetToken(ctx, postgres.CreatePasswordResetTokenParams{
		AccountID: acc.ID,
		TokenHash: sharedHash,
		ExpiresAt: expiry,
	})
	if err != nil {
		t.Fatalf("first password reset token: %v", err)
	}

	_, err = q.CreatePasswordResetToken(ctx, postgres.CreatePasswordResetTokenParams{
		AccountID: acc.ID,
		TokenHash: sharedHash,
		ExpiresAt: expiry,
	})
	if err == nil {
		t.Fatal("expected unique_violation for duplicate password reset token hash")
	}
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Errorf("expected 23505 unique_violation, got %v", err)
	}

	// 3. Session token hash uniqueness
	_, err = q.CreateSession(ctx, postgres.CreateSessionParams{
		AccountID: acc.ID,
		TokenHash: sharedHash,
		ExpiresAt: expiry,
		IpAddress: pgtype.Text{String: "127.0.0.1", Valid: true},
		UserAgent: pgtype.Text{String: "TestBrowser/1.0", Valid: true},
	})
	if err != nil {
		t.Fatalf("first session: %v", err)
	}

	_, err = q.CreateSession(ctx, postgres.CreateSessionParams{
		AccountID: acc.ID,
		TokenHash: sharedHash,
		ExpiresAt: expiry,
		IpAddress: pgtype.Text{String: "127.0.0.1", Valid: true},
		UserAgent: pgtype.Text{String: "TestBrowser/1.0", Valid: true},
	})
	if err == nil {
		t.Fatal("expected unique_violation for duplicate session token hash")
	}
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Errorf("expected 23505 unique_violation, got %v", err)
	}
}

func TestForeignKeyConstraintsAndCascade(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	// Non-existent UUID
	nonExistentUUID := pgtype.UUID{
		Bytes: [16]byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b},
		Valid: true,
	}

	// 1. Password credential with invalid account_id must fail FK
	err := q.CreatePasswordCredential(ctx, postgres.CreatePasswordCredentialParams{
		AccountID:    nonExistentUUID,
		PasswordHash: "$argon2id$v=19$dummy",
		Algorithm:    "argon2id",
		Version:      1,
	})
	if err == nil {
		t.Fatal("expected foreign_key_violation for credentials with non-existent account, got nil")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Errorf("expected 23503 foreign_key_violation, got %v", err)
	}

	// 2. Session with invalid account_id must fail FK
	_, err = q.CreateSession(ctx, postgres.CreateSessionParams{
		AccountID: nonExistentUUID,
		TokenHash: sha256Bytes("orphan-session"),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(1 * time.Hour), Valid: true},
	})
	if err == nil {
		t.Fatal("expected foreign_key_violation for session with non-existent account, got nil")
	}
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Errorf("expected 23503 foreign_key_violation, got %v", err)
	}

	// 3. Test ON DELETE CASCADE: deleting an account cascades to all dependent rows
	acc, err := q.CreateAccount(ctx, postgres.CreateAccountParams{
		Email:  "cascade@example.com",
		Status: "active",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	expiry := pgtype.Timestamptz{Time: time.Now().Add(1 * time.Hour), Valid: true}

	if err := q.CreatePasswordCredential(ctx, postgres.CreatePasswordCredentialParams{
		AccountID:    acc.ID,
		PasswordHash: "$argon2id$v=19$valid",
		Algorithm:    "argon2id",
		Version:      1,
	}); err != nil {
		t.Fatalf("create cred: %v", err)
	}

	if _, err := q.CreateEmailVerificationToken(ctx, postgres.CreateEmailVerificationTokenParams{
		AccountID: acc.ID,
		TokenHash: sha256Bytes("verify-cascade"),
		ExpiresAt: expiry,
	}); err != nil {
		t.Fatalf("create verify token: %v", err)
	}

	if _, err := q.CreatePasswordResetToken(ctx, postgres.CreatePasswordResetTokenParams{
		AccountID: acc.ID,
		TokenHash: sha256Bytes("reset-cascade"),
		ExpiresAt: expiry,
	}); err != nil {
		t.Fatalf("create reset token: %v", err)
	}

	if _, err := q.CreateSession(ctx, postgres.CreateSessionParams{
		AccountID: acc.ID,
		TokenHash: sha256Bytes("session-cascade"),
		ExpiresAt: expiry,
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Verify all rows exist before delete
	var credCount, verifyCount, resetCount, sessionCount int
	_ = db.Pool.QueryRow(ctx, "SELECT count(*) FROM app.password_credentials WHERE account_id = $1", acc.ID).Scan(&credCount)
	_ = db.Pool.QueryRow(ctx, "SELECT count(*) FROM app.email_verification_tokens WHERE account_id = $1", acc.ID).Scan(&verifyCount)
	_ = db.Pool.QueryRow(ctx, "SELECT count(*) FROM app.password_reset_tokens WHERE account_id = $1", acc.ID).Scan(&resetCount)
	_ = db.Pool.QueryRow(ctx, "SELECT count(*) FROM app.sessions WHERE account_id = $1", acc.ID).Scan(&sessionCount)

	if credCount != 1 || verifyCount != 1 || resetCount != 1 || sessionCount != 1 {
		t.Fatalf("pre-delete counts mismatch: creds=%d verify=%d reset=%d session=%d", credCount, verifyCount, resetCount, sessionCount)
	}

	// Delete account directly
	_, err = db.Pool.Exec(ctx, "DELETE FROM app.accounts WHERE id = $1", acc.ID)
	if err != nil {
		t.Fatalf("delete account: %v", err)
	}

	// Verify all dependent rows cascaded to 0
	_ = db.Pool.QueryRow(ctx, "SELECT count(*) FROM app.password_credentials WHERE account_id = $1", acc.ID).Scan(&credCount)
	_ = db.Pool.QueryRow(ctx, "SELECT count(*) FROM app.email_verification_tokens WHERE account_id = $1", acc.ID).Scan(&verifyCount)
	_ = db.Pool.QueryRow(ctx, "SELECT count(*) FROM app.password_reset_tokens WHERE account_id = $1", acc.ID).Scan(&resetCount)
	_ = db.Pool.QueryRow(ctx, "SELECT count(*) FROM app.sessions WHERE account_id = $1", acc.ID).Scan(&sessionCount)

	if credCount != 0 || verifyCount != 0 || resetCount != 0 || sessionCount != 0 {
		t.Fatalf("post-delete cascade failed: creds=%d verify=%d reset=%d session=%d", credCount, verifyCount, resetCount, sessionCount)
	}
}

func TestExpiryAndUsageFiltering(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	acc, err := q.CreateAccount(ctx, postgres.CreateAccountParams{
		Email:  "expiry@example.com",
		Status: "pending",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	// 1. Expired email verification token (expired 1 hour ago)
	expiredHash := sha256Bytes("expired-token")
	_, err = q.CreateEmailVerificationToken(ctx, postgres.CreateEmailVerificationTokenParams{
		AccountID: acc.ID,
		TokenHash: expiredHash,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(-1 * time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatalf("create expired token: %v", err)
	}

	// Query for active token must return ErrNoRows
	_, err = q.GetActiveEmailVerificationToken(ctx, expiredHash)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected ErrNoRows for expired token, got %v", err)
	}

	// 2. Active email verification token (expires in 1 hour)
	activeHash := sha256Bytes("active-token")
	activeTok, err := q.CreateEmailVerificationToken(ctx, postgres.CreateEmailVerificationTokenParams{
		AccountID: acc.ID,
		TokenHash: activeHash,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(1 * time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatalf("create active token: %v", err)
	}

	foundTok, err := q.GetActiveEmailVerificationToken(ctx, activeHash)
	if err != nil {
		t.Fatalf("get active token: %v", err)
	}
	if !bytes.Equal(foundTok.TokenHash, activeHash) {
		t.Fatal("token hash mismatch")
	}

	// 3. Mark active token used
	rows, err := q.MarkEmailVerificationTokenUsed(ctx, activeTok.ID)
	if err != nil {
		t.Fatalf("mark token used: %v", err)
	}
	if rows != 1 {
		t.Fatalf("expected 1 row affected, got %d", rows)
	}

	// Subsequent lookup must return ErrNoRows because used_at IS NOT NULL
	_, err = q.GetActiveEmailVerificationToken(ctx, activeHash)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected ErrNoRows for used token, got %v", err)
	}

	// 4. Same pattern for password reset token
	resetExpiredHash := sha256Bytes("expired-reset")
	_, err = q.CreatePasswordResetToken(ctx, postgres.CreatePasswordResetTokenParams{
		AccountID: acc.ID,
		TokenHash: resetExpiredHash,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(-1 * time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatalf("create expired reset token: %v", err)
	}

	_, err = q.GetActivePasswordResetToken(ctx, resetExpiredHash)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected ErrNoRows for expired reset token, got %v", err)
	}
}

func TestSessionRevocationAndCleanup(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	acc, err := q.CreateAccount(ctx, postgres.CreateAccountParams{
		Email:  "sessions@example.com",
		Status: "active",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	tokenHash1 := sha256Bytes("session-token-1")
	tokenHash2 := sha256Bytes("session-token-2")
	tokenHash3 := sha256Bytes("session-token-3")
	futureExpiry := pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true}
	pastExpiry := pgtype.Timestamptz{Time: time.Now().Add(-24 * time.Hour), Valid: true}

	// 1. Create active session
	sess1, err := q.CreateSession(ctx, postgres.CreateSessionParams{
		AccountID: acc.ID,
		TokenHash: tokenHash1,
		ExpiresAt: futureExpiry,
		IpAddress: pgtype.Text{String: "10.0.0.1", Valid: true},
		UserAgent: pgtype.Text{String: "ArenaBrowser/2.0", Valid: true},
	})
	if err != nil {
		t.Fatalf("create session 1: %v", err)
	}
	if sess1.RevokedAt.Valid {
		t.Fatal("new session should have revoked_at NULL")
	}

	// Verify session is active
	activeSess, err := q.GetActiveSessionByTokenHash(ctx, tokenHash1)
	if err != nil {
		t.Fatalf("get active session 1: %v", err)
	}
	if activeSess.AccountID != acc.ID {
		t.Fatal("account ID mismatch")
	}

	// 2. Revoke session 1
	if err := q.RevokeSession(ctx, tokenHash1); err != nil {
		t.Fatalf("revoke session 1: %v", err)
	}

	// Active lookup must now fail with ErrNoRows
	_, err = q.GetActiveSessionByTokenHash(ctx, tokenHash1)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected ErrNoRows for revoked session, got %v", err)
	}

	// 3. Create session 2 and 3, then revoke all account sessions
	_, _ = q.CreateSession(ctx, postgres.CreateSessionParams{
		AccountID: acc.ID,
		TokenHash: tokenHash2,
		ExpiresAt: futureExpiry,
	})
	_, _ = q.CreateSession(ctx, postgres.CreateSessionParams{
		AccountID: acc.ID,
		TokenHash: tokenHash3,
		ExpiresAt: pastExpiry, // expired session
	})

	if err := q.RevokeAllAccountSessions(ctx, acc.ID); err != nil {
		t.Fatalf("revoke all sessions: %v", err)
	}

	// Session 2 must now be revoked
	_, err = q.GetActiveSessionByTokenHash(ctx, tokenHash2)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected ErrNoRows for revoked session 2, got %v", err)
	}

	// 4. Delete expired and revoked sessions
	deletedRows, err := q.DeleteExpiredSessions(ctx)
	if err != nil {
		t.Fatalf("delete expired sessions: %v", err)
	}
	if deletedRows < 3 {
		t.Errorf("deletedRows = %d, want >= 3", deletedRows)
	}
}

func TestAccountProfileDoesNotExposePasswordHash(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := postgres.New(db.Pool)

	// 1. Create account and password credentials
	acc, err := q.CreateAccount(ctx, postgres.CreateAccountParams{
		Email:  "profile-security@example.com",
		Status: "active",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	secretHash := "$argon2id$v=19$m=65536,t=3,p=4$super-sensitive-secret-hash"
	err = q.CreatePasswordCredential(ctx, postgres.CreatePasswordCredentialParams{
		AccountID:    acc.ID,
		PasswordHash: secretHash,
		Algorithm:    "argon2id",
		Version:      1,
	})
	if err != nil {
		t.Fatalf("create password credential: %v", err)
	}

	// 2. Fetch profile via GetAccountProfile
	profile, err := q.GetAccountProfile(ctx, acc.ID)
	if err != nil {
		t.Fatalf("get account profile: %v", err)
	}

	if profile.Email != "profile-security@example.com" {
		t.Errorf("profile.Email = %q, want profile-security@example.com", profile.Email)
	}

	// 3. Inspect AppAccount struct via reflection: ensure NO password field exists
	profileType := reflect.TypeOf(profile)
	for i := 0; i < profileType.NumField(); i++ {
		fieldName := profileType.Field(i).Name
		if fieldName == "PasswordHash" || fieldName == "Password" || fieldName == "Hash" || fieldName == "Credential" {
			t.Fatalf("SECURITY VIOLATION: AppAccount struct contains forbidden sensitive field %q", fieldName)
		}
	}

	// 4. Inspect table schema directly: ensure app.accounts table has NO password column
	rows, err := db.Pool.Query(ctx, `
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = 'app' AND table_name = 'accounts'
	`)
	if err != nil {
		t.Fatalf("query table columns: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatalf("scan col: %v", err)
		}
		if col == "password" || col == "password_hash" || col == "hash" {
			t.Fatalf("SECURITY VIOLATION: app.accounts table directly contains column %q", col)
		}
	}
}
