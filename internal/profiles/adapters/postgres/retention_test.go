package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	profilespg "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// retentionBase is the instant the retention job runs at, so every boundary
// is exact and reproducible: the cutoff of each class is base minus the
// declared window.
var retentionBase = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

type retentionClock struct {
	now time.Time
}

func (c retentionClock) Now() time.Time { return c.now }

type retentionHarness struct {
	repo    *profilespg.Repository
	useCase *application.EnforceRetentionUseCase
	pool    *pgxpool.Pool
	alpha   pgtype.UUID
	beta    pgtype.UUID
	admin   pgtype.UUID
}

func setupRetentionHarness(t *testing.T, now time.Time) *retentionHarness {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)

	alpha := createEligibleAccount(t, ctx, q, "retention-alpha@arena.example.com")
	beta := createEligibleAccount(t, ctx, q, "retention-beta@arena.example.com")
	admin := createEligibleAccount(t, ctx, q, "retention-admin@arena.example.com")

	repo := profilespg.NewRepository(pool)
	useCase, err := application.NewEnforceRetentionUseCase(repo, platformpg.NewTxManager(pool), retentionClock{now: now})
	if err != nil {
		t.Fatalf("NewEnforceRetentionUseCase: %v", err)
	}
	return &retentionHarness{repo: repo, useCase: useCase, pool: pool, alpha: alpha.ID, beta: beta.ID, admin: admin.ID}
}

func seedRetentionToken(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table, hash string, account pgtype.UUID, createdAt, terminalAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO `+table+` (account_id, token_hash, created_at, expires_at, used_at)
		VALUES ($1, $2, $3, $4, $5)`,
		account, []byte(hash), createdAt, terminalAt, terminalAt); err != nil {
		t.Fatalf("seed %s token: %v", table, err)
	}
}

func seedRetentionSession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, hash string, account pgtype.UUID, createdAt time.Time, revokedAt, expiresAt *time.Time, ipAddress, userAgent string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.sessions (account_id, token_hash, created_at, expires_at, revoked_at, ip_address, user_agent)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id`,
		account, []byte(hash), createdAt, expiresAt, revokedAt, nullableText(ipAddress), nullableText(userAgent)).Scan(&id); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return id
}

func seedRetentionExport(t *testing.T, ctx context.Context, pool *pgxpool.Pool, account pgtype.UUID, status string, requestedAt time.Time, generatedAt, expiresAt *time.Time, document string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	var documentValue, hashValue any
	if document != "" {
		documentValue = document
		hashValue = strings.Repeat("ab", 32)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.data_exports (
			account_id, status, download_token_hash, requested_at, generated_at, expires_at, document, document_sha256
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id`,
		account, status, []byte("retention-download-token-hash-32"), requestedAt, generatedAt, expiresAt, documentValue, hashValue).Scan(&id); err != nil {
		t.Fatalf("seed export: %v", err)
	}
	return id
}

func seedRetentionAuditEvent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, actor pgtype.UUID, occurredAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.audit_events (occurred_at, actor_id, action, target_type, target_id, reason_code)
		VALUES ($1, $2, 'retention.probe', 'account', $3, 'probe')`, occurredAt, actor, uuidString(actor)); err != nil {
		t.Fatalf("seed audit event: %v", err)
	}
}

func seedRetentionStripeEvent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, eventID string, receivedAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.stripe_events (
			stripe_event_id, event_type, livemode, stripe_created_at, payload_sha256, payload_bytes, status, received_at
		)
		VALUES ($1, 'customer.subscription.updated', false, $2, $3, 10, 'received', $2)`,
		eventID, receivedAt, strings.Repeat("cd", 32)); err != nil {
		t.Fatalf("seed stripe event: %v", err)
	}
}

func seedRetentionHold(t *testing.T, ctx context.Context, pool *pgxpool.Pool, class string, account, placedBy pgtype.UUID, reason string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.retention_holds (data_class, account_id, reason_code, placed_by, placed_at)
		VALUES ($1, $2, $3, $4, $5)`, class, nullableRetentionUUID(account), reason, placedBy, retentionBase.Add(-time.Hour)); err != nil {
		t.Fatalf("seed retention hold: %v", err)
	}
}

func retentionRun(t *testing.T, summary *application.RetentionSummary, class domain.RetentionClass) application.RetentionRun {
	t.Helper()
	for _, run := range summary.Runs {
		if run.Class == class {
			return run
		}
	}
	t.Fatalf("summary has no run for %s", class)
	return application.RetentionRun{}
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// nullableRetentionUUID renders an absent account as SQL NULL, mirroring the
// optional subject of a hold.
func nullableRetentionUUID(id pgtype.UUID) any {
	if !id.Valid {
		return nil
	}
	return id
}

func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, args ...any) int32 {
	t.Helper()
	var count int32
	if err := pool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return count
}

// TestRetentionEnforcesPolicyAgainstPostgreSQL proves the whole policy runs
// against the real schema: tokens and sessions past their window are purged,
// prevention references are stripped, expired export documents are purged
// while the record survives, and the trail and billing rows are counted as
// retained instead of being touched.
func TestRetentionEnforcesPolicyAgainstPostgreSQL(t *testing.T) {
	ctx := context.Background()
	h := setupRetentionHarness(t, retentionBase)

	tokenWindow := domain.RetentionTokensWindow
	sessionWindow := domain.RetentionSessionsWindow
	abuseWindow := domain.RetentionAbuseSignalsWindow
	exportWindow := domain.RetentionExportsWindow

	// Tokens: two past the window, one inside it, one exactly at the
	// boundary (inclusive) and one a second inside the boundary.
	seedRetentionToken(t, ctx, h.pool, "app.email_verification_tokens", "tok-alpha-old", h.alpha, retentionBase.Add(-90*24*time.Hour), retentionBase.Add(-tokenWindow-time.Minute))
	seedRetentionToken(t, ctx, h.pool, "app.email_verification_tokens", "tok-beta-old", h.beta, retentionBase.Add(-90*24*time.Hour), retentionBase.Add(-tokenWindow-time.Minute))
	seedRetentionToken(t, ctx, h.pool, "app.email_verification_tokens", "tok-alpha-boundary", h.alpha, retentionBase.Add(-90*24*time.Hour), retentionBase.Add(-tokenWindow))
	seedRetentionToken(t, ctx, h.pool, "app.email_verification_tokens", "tok-alpha-inside", h.alpha, retentionBase.Add(-90*24*time.Hour), retentionBase.Add(-tokenWindow+time.Second))
	seedRetentionToken(t, ctx, h.pool, "app.password_reset_tokens", "tok-alpha-reset", h.alpha, retentionBase.Add(-90*24*time.Hour), retentionBase.Add(-tokenWindow-time.Minute))

	// Sessions: one per account past the session window, one past the
	// prevention window only, and one active.
	revokedOld := retentionBase.Add(-sessionWindow - time.Minute)
	expiresOld := retentionBase.Add(-sessionWindow - time.Minute)
	alphaOld := seedRetentionSession(t, ctx, h.pool, "sess-alpha-old", h.alpha, retentionBase.Add(-120*24*time.Hour), &revokedOld, &expiresOld, "203.0.113.10", "retention-probe/1.0")
	betaOld := seedRetentionSession(t, ctx, h.pool, "sess-beta-old", h.beta, retentionBase.Add(-120*24*time.Hour), &revokedOld, &expiresOld, "203.0.113.11", "retention-probe/1.0")
	stale := seedRetentionSession(t, ctx, h.pool, "sess-alpha-stale", h.alpha, retentionBase.Add(-30*24*time.Hour), nil, timePtr(retentionBase.Add(-abuseWindow-time.Hour)), "203.0.113.12", "retention-probe/1.0")
	active := seedRetentionSession(t, ctx, h.pool, "sess-alpha-active", h.alpha, retentionBase.Add(-time.Hour), nil, timePtr(retentionBase.Add(24*time.Hour)), "203.0.113.13", "retention-probe/1.0")

	// Exports: a ready document past the window per account and an
	// abandoned request.
	generatedOld := retentionBase.Add(-72 * time.Hour)
	expiresOldExport := retentionBase.Add(-exportWindow - time.Hour)
	alphaExport := seedRetentionExport(t, ctx, h.pool, h.alpha, "ready", retentionBase.Add(-73*time.Hour), &generatedOld, &expiresOldExport, `{"schema_version":1}`)
	betaExport := seedRetentionExport(t, ctx, h.pool, h.beta, "ready", retentionBase.Add(-73*time.Hour), &generatedOld, &expiresOldExport, `{"schema_version":1}`)
	abandoned := seedRetentionExport(t, ctx, h.pool, h.admin, "requested", retentionBase.Add(-exportWindow-time.Hour), nil, nil, "")

	seedRetentionAuditEvent(t, ctx, h.pool, h.admin, retentionBase.Add(-time.Hour))
	seedRetentionStripeEvent(t, ctx, h.pool, "evt_retentionProbe1", retentionBase.Add(-time.Hour))

	// Legal holds: the tokens and exports of beta, and the whole sessions
	// class. The prevention class is deliberately not held.
	seedRetentionHold(t, ctx, h.pool, "tokens", h.beta, h.admin, "litigation_hold")
	seedRetentionHold(t, ctx, h.pool, "exports", h.beta, h.admin, "litigation_hold")
	seedRetentionHold(t, ctx, h.pool, "sessions", pgtype.UUID{}, h.admin, "regulator_request")

	summary, err := h.useCase.Execute(ctx)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(summary.Runs) != 6 {
		t.Fatalf("summary has %d runs, want 6", len(summary.Runs))
	}

	// Tokens: the two old rows of alpha (verification and recovery) plus
	// the boundary row are purged; beta's row is preserved by its hold and
	// the row inside the window survives.
	tokensRun := retentionRun(t, summary, domain.RetentionClassTokens)
	if tokensRun.Counts.Purged != 3 || tokensRun.Counts.Held != 1 {
		t.Fatalf("tokens purged = %d held = %d, want 3 and 1", tokensRun.Counts.Purged, tokensRun.Counts.Held)
	}
	if got := countRows(t, ctx, h.pool, `SELECT count(*)::integer FROM app.email_verification_tokens`); got != 2 {
		t.Errorf("verification tokens left = %d, want 2 (beta's held row and the in-window row)", got)
	}
	if got := countRows(t, ctx, h.pool, `SELECT count(*)::integer FROM app.password_reset_tokens`); got != 0 {
		t.Errorf("recovery tokens left = %d, want 0", got)
	}

	// Sessions: the class-wide hold preserves both old rows, so nothing is
	// purged and both are counted as held.
	sessionsRun := retentionRun(t, summary, domain.RetentionClassSessions)
	if sessionsRun.Counts.Purged != 0 || sessionsRun.Counts.Held != 2 {
		t.Fatalf("sessions purged = %d held = %d, want 0 and 2", sessionsRun.Counts.Purged, sessionsRun.Counts.Held)
	}
	if got := countRows(t, ctx, h.pool, `SELECT count(*)::integer FROM app.sessions`); got != 4 {
		t.Errorf("sessions left = %d, want 4", got)
	}

	// Prevention references: the sessions hold is another class, so the
	// references of every terminal session are stripped even though the
	// rows survive (three terminal sessions, one of them inside the session
	// window); the active session keeps its references.
	abuseRun := retentionRun(t, summary, domain.RetentionClassAbuseSignals)
	if abuseRun.Counts.Anonymized != 3 {
		t.Fatalf("prevention references anonymized = %d, want 3", abuseRun.Counts.Anonymized)
	}
	for _, id := range []pgtype.UUID{alphaOld, betaOld, stale} {
		var ip, agent pgtype.Text
		if err := h.pool.QueryRow(ctx, `SELECT ip_address, user_agent FROM app.sessions WHERE id = $1`, id).Scan(&ip, &agent); err != nil {
			t.Fatalf("read anonymized session: %v", err)
		}
		if ip.Valid || agent.Valid {
			t.Errorf("terminal session %s kept restricted references", uuidString(id))
		}
	}
	var activeIP pgtype.Text
	if err := h.pool.QueryRow(ctx, `SELECT ip_address FROM app.sessions WHERE id = $1`, active).Scan(&activeIP); err != nil {
		t.Fatalf("read active session: %v", err)
	}
	if !activeIP.Valid {
		t.Error("an active session must keep its client reference")
	}

	// Exports: the abandoned request and alpha's expired document are
	// purged, beta's document survives under its hold, and every record
	// survives.
	exportsRun := retentionRun(t, summary, domain.RetentionClassExports)
	if exportsRun.Counts.Purged != 2 || exportsRun.Counts.Held != 1 {
		t.Fatalf("exports purged = %d held = %d, want 2 and 1", exportsRun.Counts.Purged, exportsRun.Counts.Held)
	}
	for _, probe := range []struct {
		id           pgtype.UUID
		wantStatus   string
		wantDocument bool
	}{
		{alphaExport, "expired", false},
		{betaExport, "ready", true},
		{abandoned, "expired", false},
	} {
		var status string
		var document pgtype.Text
		if err := h.pool.QueryRow(ctx, `SELECT status, document FROM app.data_exports WHERE id = $1`, probe.id).Scan(&status, &document); err != nil {
			t.Fatalf("read export: %v", err)
		}
		if status != probe.wantStatus || document.Valid != probe.wantDocument {
			t.Errorf("export %s = (%s, document=%v), want (%s, document=%v)", uuidString(probe.id), status, document.Valid, probe.wantStatus, probe.wantDocument)
		}
	}

	// The trail and the billing rows are counted as retained, never touched.
	auditRun := retentionRun(t, summary, domain.RetentionClassReferentialLogs)
	if auditRun.Counts.Retained != 1 || auditRun.Counts.Purged != 0 {
		t.Errorf("referential logs retained = %d purged = %d, want 1 and 0", auditRun.Counts.Retained, auditRun.Counts.Purged)
	}
	if got := countRows(t, ctx, h.pool, `SELECT count(*)::integer FROM app.audit_events`); got != 1 {
		t.Errorf("audit events left = %d, want the trail untouched", got)
	}
	billingRun := retentionRun(t, summary, domain.RetentionClassBilling)
	if billingRun.Counts.Retained != 1 || billingRun.Counts.Purged != 0 {
		t.Errorf("billing retained = %d purged = %d, want 1 and 0", billingRun.Counts.Retained, billingRun.Counts.Purged)
	}
	if got := countRows(t, ctx, h.pool, `SELECT count(*)::integer FROM app.stripe_events`); got != 1 {
		t.Errorf("stripe events left = %d, want the billing record untouched", got)
	}
}

// TestRetentionBoundaryIsExactAgainstPostgreSQL proves the SQL boundary: a
// record terminal exactly at the cutoff is purged, one second later is not.
func TestRetentionBoundaryIsExactAgainstPostgreSQL(t *testing.T) {
	ctx := context.Background()
	h := setupRetentionHarness(t, retentionBase)

	cutoff := retentionBase.Add(-domain.RetentionTokensWindow)
	seedRetentionToken(t, ctx, h.pool, "app.email_verification_tokens", "tok-boundary", h.alpha, retentionBase.Add(-90*24*time.Hour), cutoff)
	seedRetentionToken(t, ctx, h.pool, "app.email_verification_tokens", "tok-after-boundary", h.alpha, retentionBase.Add(-90*24*time.Hour), cutoff.Add(time.Second))
	seedRetentionToken(t, ctx, h.pool, "app.email_verification_tokens", "tok-before-boundary", h.alpha, retentionBase.Add(-90*24*time.Hour), cutoff.Add(-time.Second))

	summary, err := h.useCase.Execute(ctx)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if run := retentionRun(t, summary, domain.RetentionClassTokens); run.Counts.Purged != 2 {
		t.Fatalf("tokens purged = %d, want 2 (at the cutoff and before it)", run.Counts.Purged)
	}
	if run := retentionRun(t, summary, domain.RetentionClassTokens); run.CutoffAt == nil || !run.CutoffAt.Equal(cutoff) {
		t.Fatalf("tokens cutoff = %v, want %s", run.CutoffAt, cutoff)
	}
	var survivors []string
	rows, err := h.pool.Query(ctx, `SELECT encode(token_hash, 'escape') FROM app.email_verification_tokens`)
	if err != nil {
		t.Fatalf("list surviving tokens: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			t.Fatalf("scan surviving token: %v", err)
		}
		survivors = append(survivors, hash)
	}
	if len(survivors) != 1 || survivors[0] != "tok-after-boundary" {
		t.Fatalf("survivors = %v, want only the token one second inside the boundary", survivors)
	}
}

// TestRetentionIsIdempotentAgainstPostgreSQL proves a replayed run records
// nothing twice, reports the recorded outcome, and finds nothing left to
// purge when it runs again later.
func TestRetentionIsIdempotentAgainstPostgreSQL(t *testing.T) {
	ctx := context.Background()
	h := setupRetentionHarness(t, retentionBase)

	seedRetentionToken(t, ctx, h.pool, "app.email_verification_tokens", "tok-once", h.alpha, retentionBase.Add(-90*24*time.Hour), retentionBase.Add(-40*24*time.Hour))
	seedRetentionAuditEvent(t, ctx, h.pool, h.admin, retentionBase.Add(-time.Hour))

	first, err := h.useCase.Execute(ctx)
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if run := retentionRun(t, first, domain.RetentionClassTokens); run.Counts.Purged != 1 || run.Replayed {
		t.Fatalf("first tokens run = %+v, want one purge and no replay", run)
	}

	second, err := h.useCase.Execute(ctx)
	if err != nil {
		t.Fatalf("replayed Execute: %v", err)
	}
	for _, run := range second.Runs {
		if !run.Replayed {
			t.Fatalf("%s must report a replay", run.Class)
		}
	}
	if got := countRows(t, ctx, h.pool, `SELECT count(*)::integer FROM app.retention_runs`); got != 6 {
		t.Fatalf("ledger has %d rows after a replay, want 6", got)
	}
	if run := retentionRun(t, second, domain.RetentionClassTokens); run.Counts.Purged != 1 {
		t.Errorf("a replay must report the recorded outcome, got %+v", run.Counts)
	}

	// A later run records a new outcome per class and purges nothing.
	later := setupLaterExecution(t, h, retentionBase.Add(time.Minute))
	third, err := later.Execute(ctx)
	if err != nil {
		t.Fatalf("later Execute: %v", err)
	}
	if got := countRows(t, ctx, h.pool, `SELECT count(*)::integer FROM app.retention_runs`); got != 12 {
		t.Fatalf("ledger has %d rows after a later run, want 12", got)
	}
	for _, class := range []domain.RetentionClass{
		domain.RetentionClassTokens, domain.RetentionClassSessions, domain.RetentionClassExports,
	} {
		run := retentionRun(t, third, class)
		if run.Replayed || run.Counts.Purged != 0 {
			t.Errorf("%s = replayed %v purged %d, want a fresh zero run", class, run.Replayed, run.Counts.Purged)
		}
	}
}

// setupLaterExecution builds a second job over the same database and holds,
// running at a later instant.
func setupLaterExecution(t *testing.T, h *retentionHarness, now time.Time) *application.EnforceRetentionUseCase {
	t.Helper()
	useCase, err := application.NewEnforceRetentionUseCase(h.repo, platformpg.NewTxManager(h.pool), retentionClock{now: now})
	if err != nil {
		t.Fatalf("NewEnforceRetentionUseCase: %v", err)
	}
	return useCase
}

// TestRetentionRefusesReleaseOfHeldExportUntilTheHoldEnds proves a released
// hold stops preserving data: after the release the next run purges what the
// hold was protecting.
func TestRetentionRefusesReleaseOfHeldExportUntilTheHoldEnds(t *testing.T) {
	ctx := context.Background()
	h := setupRetentionHarness(t, retentionBase)

	generated := retentionBase.Add(-72 * time.Hour)
	expires := retentionBase.Add(-domain.RetentionExportsWindow - time.Hour)
	exportID := seedRetentionExport(t, ctx, h.pool, h.alpha, "ready", retentionBase.Add(-73*time.Hour), &generated, &expires, `{"schema_version":1}`)
	seedRetentionHold(t, ctx, h.pool, "exports", h.alpha, h.admin, "litigation_hold")

	held, err := h.useCase.Execute(ctx)
	if err != nil {
		t.Fatalf("held Execute: %v", err)
	}
	if run := retentionRun(t, held, domain.RetentionClassExports); run.Counts.Held != 1 || run.Counts.Purged != 0 {
		t.Fatalf("exports held run = %+v, want one held record", run.Counts)
	}

	if _, err := h.pool.Exec(ctx, `
		UPDATE app.retention_holds
		SET released_at = $1, release_reason_code = 'matter_closed'
		WHERE data_class = 'exports' AND account_id = $2`, retentionBase, h.alpha); err != nil {
		t.Fatalf("release hold: %v", err)
	}

	later := setupLaterExecution(t, h, retentionBase.Add(time.Minute))
	released, err := later.Execute(ctx)
	if err != nil {
		t.Fatalf("released Execute: %v", err)
	}
	if run := retentionRun(t, released, domain.RetentionClassExports); run.Counts.Purged != 1 || run.Counts.Held != 0 {
		t.Fatalf("exports released run = %+v, want one purge and no hold", run.Counts)
	}
	var status string
	var document pgtype.Text
	if err := h.pool.QueryRow(ctx, `SELECT status, document FROM app.data_exports WHERE id = $1`, exportID).Scan(&status, &document); err != nil {
		t.Fatalf("read export after release: %v", err)
	}
	if status != "expired" || document.Valid {
		t.Fatalf("export after release = (%s, document=%v), want the document purged", status, document.Valid)
	}
}

func timePtr(instant time.Time) *time.Time {
	return &instant
}
