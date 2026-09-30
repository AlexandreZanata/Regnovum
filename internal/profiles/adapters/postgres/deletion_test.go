package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	auditpg "github.com/AlexandreZanata/Regnovum/internal/audit/adapters/postgres"
	identityargon "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/argon2id"
	identitypg "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	profilespg "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

const deletionPassword = "DeletionProbePass123!"

const (
	deletionAccountEmail = "deletion-probe@arena.example.com"
	deletionOtherEmail   = "deletion-other@arena.example.com"
	deletionUsername     = "deletion-probe"
	deletionOtherName    = "deletion-other"
	deletionArgument     = "public argument that survives the deletion"
	deletionReason       = "holder changed their mind"
)

var deletionInstant = time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)

type deletionClock struct {
	now time.Time
}

func (c deletionClock) Now() time.Time { return c.now }

type deletionHarness struct {
	repo     *profilespg.Repository
	request  *application.RequestDeletionUseCase
	status   *application.GetDeletionStatusUseCase
	cancel   *application.CancelDeletionUseCase
	execute  *application.ExecuteDueDeletionsUseCase
	login    *identityapp.LoginUseCase
	pool     *pgxpool.Pool
	account  pgtype.UUID
	other    pgtype.UUID
	arena    pgtype.UUID
	argument pgtype.UUID
}

func setupDeletionHarness(t *testing.T, now time.Time) *deletionHarness {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)
	repo := profilespg.NewRepository(pool)
	audit := auditpg.NewRepository(pool)

	account := createEligibleAccount(t, ctx, q, deletionAccountEmail)
	other := createEligibleAccount(t, ctx, q, deletionOtherEmail)

	seedPersonalExportProfile(t, ctx, pool, account.ID, deletionUsername, "pt-BR", "")
	seedPersonalExportProfile(t, ctx, pool, other.ID, deletionOtherName, "en-US", "")
	hasher, err := identityargon.NewDefault(clockseed.NewRandom())
	if err != nil {
		t.Fatalf("argon2id.NewDefault: %v", err)
	}
	passwordHash, err := hasher.HashPassword(deletionPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO app.password_credentials (account_id, password_hash) VALUES ($1, $2)`, account.ID, passwordHash); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO app.sessions (account_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		account.ID, []byte("deletion-session-token-hash-32-bytes"), now.Add(24*time.Hour)); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO app.email_verification_tokens (account_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		account.ID, []byte("deletion-verification-token-hash-32"), now.Add(time.Hour)); err != nil {
		t.Fatalf("seed verification token: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO app.password_reset_tokens (account_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		account.ID, []byte("deletion-reset-token-hash-32-bytes"), now.Add(time.Hour)); err != nil {
		t.Fatalf("seed reset token: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.communication_preferences (account_id, marketing_opt_in)
		VALUES ($1, true)`, account.ID); err != nil {
		t.Fatalf("seed preferences: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.communication_preference_history (account_id, marketing_opt_in)
		VALUES ($1, true), ($1, false)`, account.ID); err != nil {
		t.Fatalf("seed preference history: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.wallet_accounts (account_id, balance_free, balance_purchased)
		VALUES ($1, 5000, 2000)`, account.ID); err != nil {
		t.Fatalf("seed wallet: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.arena_pass_lots (account_id, origin, quantity, remaining_quantity, reference)
		VALUES ($1, 'PURCHASE', 1, 1, 'deletion-pass-reference')`, account.ID); err != nil {
		t.Fatalf("seed pass lot: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.data_exports (
			account_id, status, download_token_hash, generated_at, expires_at, document, document_sha256
		)
		VALUES ($1, 'ready', $2, $3, $4, '{"account":{"email":"deletion-probe@arena.example.com"}}', repeat('a', 64))`,
		account.ID, make([]byte, 32), now, now.Add(24*time.Hour)); err != nil {
		t.Fatalf("seed personal export: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.admin_roles (account_id, role, granted_by)
		VALUES ($1, 'moderator', $2)`, account.ID, other.ID); err != nil {
		t.Fatalf("seed admin role: %v", err)
	}

	var draftID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status, version)
		VALUES ($1, 'Deletion private draft statement', 'technology', 'pt-BR', 'draft', 1)
		RETURNING id`, account.ID).Scan(&draftID); err != nil {
		t.Fatalf("seed draft: %v", err)
	}
	arena := seedPersonalExportArena(t, ctx, pool, account.ID, "deletion-arena", "Deletion probe statement")
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.arena_relations (arena_id, related_arena_id, relation)
		VALUES ($1, $2, 'SUPERSEDES')`, arena, draftID); err != nil {
		t.Fatalf("seed draft relation: %v", err)
	}
	argument := seedPersonalExportArgument(t, ctx, pool, arena, account.ID, "support", deletionArgument, "published")
	// A reply in the same thread keeps the thread integrity proof: it is
	// authored by the other account and must survive untouched.
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.arguments (arena_id, author_id, parent_id, relation, content, content_hash, grapheme_cost)
		VALUES ($1, $2, $3, 'oppose', 'reply from the other author', 'deletion-other-hash', 10)`,
		arena, other.ID, argument); err != nil {
		t.Fatalf("seed reply: %v", err)
	}

	identityRepo := identitypg.NewRepository(pool)
	login := identityapp.NewLoginUseCase(
		identityRepo, identityRepo, identityRepo, hasher,
		clockseed.NewClock(), clockseed.NewRandom(), identitydomain.DefaultSessionPolicy(),
	)

	clock := deletionClock{now: now}
	uow := platformpg.NewTxManager(pool)
	request, err := application.NewRequestDeletionUseCase(repo, audit, uow, clock)
	if err != nil {
		t.Fatalf("NewRequestDeletionUseCase: %v", err)
	}
	status, err := application.NewGetDeletionStatusUseCase(repo)
	if err != nil {
		t.Fatalf("NewGetDeletionStatusUseCase: %v", err)
	}
	cancel, err := application.NewCancelDeletionUseCase(repo, audit, uow, clock)
	if err != nil {
		t.Fatalf("NewCancelDeletionUseCase: %v", err)
	}
	execute, err := application.NewExecuteDueDeletionsUseCase(repo, audit, uow, clock)
	if err != nil {
		t.Fatalf("NewExecuteDueDeletionsUseCase: %v", err)
	}

	return &deletionHarness{
		repo: repo, request: request, status: status, cancel: cancel, execute: execute, login: login,
		pool: pool, account: account.ID, other: other.ID, arena: arena, argument: argument,
	}
}

func TestAccountDeletionJourneyAnonymizesAndPreservesObligations(t *testing.T) {
	harness := setupDeletionHarness(t, deletionInstant)
	ctx := context.Background()

	accountID := domain.AccountID(uuidString(harness.account))
	if _, err := harness.login.Execute(ctx, identityapp.LoginCommand{Email: deletionAccountEmail, Password: deletionPassword}); err != nil {
		t.Fatalf("login before deletion must succeed: %v", err)
	}
	outcome, err := harness.request.Execute(ctx, application.RequestDeletionCommand{AccountID: accountID})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if outcome.Replayed || outcome.Request.Status != domain.DeletionStatusRequested {
		t.Fatalf("outcome = %+v", outcome)
	}

	// Replay keeps one active record and the original requested instant.
	replayed, err := harness.request.Execute(ctx, application.RequestDeletionCommand{AccountID: accountID})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replayed.Replayed || replayed.Request.ID != outcome.Request.ID {
		t.Fatalf("replay = %+v", replayed)
	}

	// Inside the cooldown nothing executes.
	inside, err := harness.execute.Execute(ctx)
	if err != nil {
		t.Fatalf("execute inside cooldown: %v", err)
	}
	if inside.Executed != 0 {
		t.Fatalf("cooldown executed %d, want 0", inside.Executed)
	}

	// Past the cooldown the workflow anonymizes.
	lateClock := deletionClock{now: deletionInstant.Add(domain.DeletionCooldown + time.Minute)}
	lateExecute, err := application.NewExecuteDueDeletionsUseCase(harness.repo, auditpg.NewRepository(harness.pool), platformpg.NewTxManager(harness.pool), lateClock)
	if err != nil {
		t.Fatalf("NewExecuteDueDeletionsUseCase: %v", err)
	}
	done, err := lateExecute.Execute(ctx)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if done.Executed != 1 {
		t.Fatalf("executed %d, want 1", done.Executed)
	}

	// The account row survives with an opaque placeholder email and the
	// terminal deleted status; the original address is gone.
	var email, status string
	if err := harness.pool.QueryRow(ctx, `SELECT email, status FROM app.accounts WHERE id = $1`, harness.account).Scan(&email, &status); err != nil {
		t.Fatalf("load account: %v", err)
	}
	if status != "deleted" || !strings.HasPrefix(email, "deleted+") || strings.Contains(email, deletionAccountEmail) {
		t.Fatalf("account = (%s, %s)", email, status)
	}

	// Private rows are purged: profile, username history, credential,
	// tokens, sessions, explicit preferences and unpublished drafts.
	for table, query := range map[string]string{
		"profiles":                         `SELECT count(*) FROM app.profiles WHERE account_id = $1`,
		"username_history":                 `SELECT count(*) FROM app.username_history WHERE account_id = $1`,
		"password_credentials":             `SELECT count(*) FROM app.password_credentials WHERE account_id = $1`,
		"email_verification_tokens":        `SELECT count(*) FROM app.email_verification_tokens WHERE account_id = $1`,
		"password_reset_tokens":            `SELECT count(*) FROM app.password_reset_tokens WHERE account_id = $1`,
		"sessions":                         `SELECT count(*) FROM app.sessions WHERE account_id = $1`,
		"communication_preferences":        `SELECT count(*) FROM app.communication_preferences WHERE account_id = $1`,
		"communication_preference_history": `SELECT count(*) FROM app.communication_preference_history WHERE account_id = $1`,
		"draft_arenas":                     `SELECT count(*) FROM app.arenas WHERE creator_id = $1 AND status = 'draft'`,
		"draft_relations":                  `SELECT count(*) FROM app.arena_relations ar JOIN app.arenas a ON a.id = ar.arena_id WHERE a.creator_id = $1 AND a.status = 'draft'`,
	} {
		var count int64
		if err := harness.pool.QueryRow(ctx, query, harness.account).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s kept %d rows after deletion", table, count)
		}
	}

	// Public content survives with its stable author id; the other author's
	// reply in the thread is untouched.
	var argumentAuthor pgtype.UUID
	if err := harness.pool.QueryRow(ctx, `SELECT author_id FROM app.arguments WHERE id = $1`, harness.argument).Scan(&argumentAuthor); err != nil {
		t.Fatalf("load argument: %v", err)
	}
	if uuidString(argumentAuthor) != uuidString(harness.account) {
		t.Fatalf("argument author changed to %s", uuidString(argumentAuthor))
	}
	var replies int64
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM app.arguments WHERE parent_id = $1`, harness.argument).Scan(&replies); err != nil {
		t.Fatalf("count replies: %v", err)
	}
	if replies != 1 {
		t.Fatalf("thread lost replies: %d", replies)
	}
	var publishedArenas int64
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM app.arenas WHERE creator_id = $1 AND status = 'published'`, harness.account).Scan(&publishedArenas); err != nil {
		t.Fatalf("count published arenas: %v", err)
	}
	if publishedArenas != 1 {
		t.Fatalf("published arenas = %d, want the Arena preserved", publishedArenas)
	}

	// Personal export documents are purged: the private copy never outlives
	// the account, while the retained record keeps its retention evidence.
	var exportStatus string
	var exportDocument *string
	if err := harness.pool.QueryRow(ctx, `
		SELECT status, document FROM app.data_exports WHERE account_id = $1`, harness.account).Scan(&exportStatus, &exportDocument); err != nil {
		t.Fatalf("load personal export: %v", err)
	}
	if exportDocument != nil || exportStatus != "expired" {
		t.Fatalf("personal export = (%s, %v), want expired and purged", exportStatus, exportDocument)
	}

	// Administrative roles are revoked but the assignment row survives.
	var roleRevoked *time.Time
	if err := harness.pool.QueryRow(ctx, `SELECT revoked_at FROM app.admin_roles WHERE account_id = $1`, harness.account).Scan(&roleRevoked); err != nil {
		t.Fatalf("load admin role: %v", err)
	}
	if roleRevoked == nil {
		t.Fatal("admin role must be revoked after deletion")
	}

	// Billing and ledger obligations survive untouched.
	var freeBalance, purchasedBalance int64
	if err := harness.pool.QueryRow(ctx, `SELECT balance_free, balance_purchased FROM app.wallet_accounts WHERE account_id = $1`, harness.account).Scan(&freeBalance, &purchasedBalance); err != nil {
		t.Fatalf("load wallet: %v", err)
	}
	if freeBalance != 5000 || purchasedBalance != 2000 {
		t.Fatalf("wallet = (%d, %d), want (5000, 2000)", freeBalance, purchasedBalance)
	}
	var lots int64
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM app.arena_pass_lots WHERE account_id = $1`, harness.account).Scan(&lots); err != nil {
		t.Fatalf("count pass lots: %v", err)
	}
	if lots != 1 {
		t.Fatalf("pass lots = %d, want 1", lots)
	}

	// The audit trail recorded request and execution.
	var actions []string
	rows, err := harness.pool.Query(ctx, `SELECT action FROM app.audit_events WHERE target_id = $1 ORDER BY occurred_at, id`, uuidString(harness.account))
	if err != nil {
		t.Fatalf("load audit: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			t.Fatalf("scan audit: %v", err)
		}
		actions = append(actions, action)
	}
	if len(actions) != 2 || actions[0] != "account.deletion_requested" || actions[1] != "account.deletion_executed" {
		t.Fatalf("audit actions = %v", actions)
	}

	// The holder can no longer authenticate: the identity login flow refuses
	// the deleted account with the original credentials.
	if _, err := harness.login.Execute(ctx, identityapp.LoginCommand{
		Email:    deletionAccountEmail,
		Password: deletionPassword,
	}); !errors.Is(err, identityapp.ErrInvalidCredentials) {
		t.Fatalf("login after deletion error = %v, want ErrInvalidCredentials", err)
	}
	var remaining int64
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM app.sessions WHERE account_id = $1`, harness.account).Scan(&remaining); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if remaining != 0 {
		t.Fatal("deleted account kept a session")
	}

	// The status endpoint reports the terminal record.
	resolved, err := harness.status.Execute(ctx, accountID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if resolved.Status != domain.DeletionStatusExecuted || resolved.ExecutedAt == nil {
		t.Fatalf("status = %+v", resolved)
	}

	// Idempotence: a replayed workflow run executes nothing.
	again, err := lateExecute.Execute(ctx)
	if err != nil {
		t.Fatalf("replay execute: %v", err)
	}
	if again.Executed != 0 {
		t.Fatalf("replayed run executed %d, want 0", again.Executed)
	}
}

func TestAccountDeletionCancelRestartsJourney(t *testing.T) {
	harness := setupDeletionHarness(t, deletionInstant)
	ctx := context.Background()
	accountID := domain.AccountID(uuidString(harness.account))

	first, err := harness.request.Execute(ctx, application.RequestDeletionCommand{AccountID: accountID})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	canceled, err := harness.cancel.Execute(ctx, application.CancelDeletionCommand{AccountID: accountID, Reason: deletionReason})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if canceled.Status != domain.DeletionStatusCanceled {
		t.Fatalf("canceled = %+v", canceled)
	}
	if _, err := harness.cancel.Execute(ctx, application.CancelDeletionCommand{AccountID: accountID}); !errors.Is(err, application.ErrDeletionNotCancellable) {
		t.Fatalf("second cancel error = %v, want ErrDeletionNotCancellable", err)
	}

	// The cancel reason is restricted evidence: stored, never public.
	var reason string
	if err := harness.pool.QueryRow(ctx, `SELECT cancel_reason FROM app.account_deletion_requests WHERE id = $1`, mustDeletionUUID(t, first.Request.ID)).Scan(&reason); err != nil {
		t.Fatalf("load cancel reason: %v", err)
	}
	if reason != deletionReason {
		t.Fatalf("reason = %q, want the holder reason", reason)
	}

	// A fresh request starts a new record and a new cooldown.
	restarted, err := harness.request.Execute(ctx, application.RequestDeletionCommand{AccountID: accountID})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if restarted.Replayed || restarted.Request.ID == first.Request.ID {
		t.Fatalf("restart = %+v, want a new record", restarted)
	}

	// Execution past the new cooldown succeeds and the old record stays
	// canceled: the history is retained.
	lateClock := deletionClock{now: deletionInstant.Add(2*domain.DeletionCooldown + time.Minute)}
	lateExecute, err := application.NewExecuteDueDeletionsUseCase(harness.repo, auditpg.NewRepository(harness.pool), platformpg.NewTxManager(harness.pool), lateClock)
	if err != nil {
		t.Fatalf("NewExecuteDueDeletionsUseCase: %v", err)
	}
	done, err := lateExecute.Execute(ctx)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if done.Executed != 1 {
		t.Fatalf("executed %d, want 1", done.Executed)
	}
	var oldStatus string
	if err := harness.pool.QueryRow(ctx, `SELECT status FROM app.account_deletion_requests WHERE id = $1`, mustDeletionUUID(t, first.Request.ID)).Scan(&oldStatus); err != nil {
		t.Fatalf("load old record: %v", err)
	}
	if oldStatus != string(domain.DeletionStatusCanceled) {
		t.Fatalf("old record status = %q, want canceled", oldStatus)
	}
}

func TestAccountDeletionSchemaRetainsAndProtectsRecords(t *testing.T) {
	harness := setupDeletionHarness(t, deletionInstant)
	ctx := context.Background()
	accountID := domain.AccountID(uuidString(harness.account))

	outcome, err := harness.request.Execute(ctx, application.RequestDeletionCommand{AccountID: accountID})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	requestUUID := mustDeletionUUID(t, outcome.Request.ID)

	assertConstraint := func(operation, constraint string, err error) {
		t.Helper()
		var pgError *pgconn.PgError
		if !errors.As(err, &pgError) || pgError.Code != "23514" || pgError.ConstraintName != constraint {
			t.Fatalf("%s error = %v, want 23514/%s", operation, err, constraint)
		}
	}
	_, err = harness.pool.Exec(ctx, `DELETE FROM app.account_deletion_requests WHERE id = $1`, requestUUID)
	assertConstraint("delete", "account_deletion_requests_retained", err)

	_, err = harness.pool.Exec(ctx, `UPDATE app.account_deletion_requests SET requested_at = now() WHERE id = $1`, requestUUID)
	assertConstraint("rewrite provenance", "account_deletion_requests_immutable", err)

	_, err = harness.pool.Exec(ctx, `
		INSERT INTO app.account_deletion_requests (account_id, status, requested_at, updated_at)
		VALUES ($1, 'requested', now(), now())`, harness.account)
	var uniqueError *pgconn.PgError
	if !errors.As(err, &uniqueError) || uniqueError.Code != "23505" {
		t.Fatalf("duplicate active request error = %v, want 23505", err)
	}

	// Terminal records are immutable: after cancellation, a direct update
	// that moves the status is rejected by the guard trigger.
	if _, err := harness.pool.Exec(ctx, `
		UPDATE app.account_deletion_requests
		SET status = 'canceled', canceled_at = now(), cancel_reason = 'probe reason', updated_at = now()
		WHERE id = $1`, requestUUID); err != nil {
		t.Fatalf("cancel probe: %v", err)
	}
	_, err = harness.pool.Exec(ctx, `
		UPDATE app.account_deletion_requests
		SET status = 'executed', executed_at = now(), updated_at = now()
		WHERE id = $1`, requestUUID)
	assertConstraint("terminal transition", "account_deletion_requests_terminal", err)
}

func mustDeletionUUID(t *testing.T, id string) pgtype.UUID {
	t.Helper()
	var parsed pgtype.UUID
	if err := parsed.Scan(id); err != nil {
		t.Fatalf("parse deletion id %q: %v", id, err)
	}
	return parsed
}
