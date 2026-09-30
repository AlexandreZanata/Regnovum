package postgres_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/exportjson"
	profilespg "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

const (
	exportAlphaEmail    = "personal-export-alpha@arena.example.com"
	exportBetaEmail     = "personal-export-beta@arena.example.com"
	exportAlphaUsername = "export-alpha"
	exportBetaUsername  = "export-beta"
	exportIPAddress     = "192.0.2.77"
	exportUserAgent     = "personal-export-probe-agent"
	exportWithdrawnText = "withdrawn personal argument content"
	exportRemovedText   = "removed personal argument content"
	exportAlphaArgument = "alpha public argument content"
	exportBetaArgument  = "beta public argument content"
	exportLotReference  = "purchase-lot-reference"
	exportOperationRef  = "wallet-operation-reference"
)

var exportBase = time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)

type exportFixedClock struct {
	now time.Time
}

func (c exportFixedClock) Now() time.Time { return c.now }

type personalExportHarness struct {
	repo     *profilespg.Repository
	request  *application.RequestPersonalExportUseCase
	generate *application.GeneratePersonalExportUseCase
	download *application.DownloadPersonalExportUseCase
	pool     *pgxpool.Pool
	alphaID  domain.AccountID
	betaID   domain.AccountID
	arenaID  pgtype.UUID
}

func setupPersonalExportHarness(t *testing.T) *personalExportHarness {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)
	repo := profilespg.NewRepository(pool)

	alpha := createEligibleAccount(t, ctx, q, exportAlphaEmail)
	beta := createEligibleAccount(t, ctx, q, exportBetaEmail)

	if err := seedPersonalExportProfile(t, ctx, pool, alpha.ID, exportAlphaUsername, "en-US", "America/Sao_Paulo"); err != nil {
		t.Fatalf("seed alpha profile: %v", err)
	}
	if err := seedPersonalExportProfile(t, ctx, pool, beta.ID, exportBetaUsername, "pt-BR", ""); err != nil {
		t.Fatalf("seed beta profile: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.communication_preferences (account_id, marketing_opt_in)
		VALUES ($1, true)`, alpha.ID); err != nil {
		t.Fatalf("seed alpha preferences: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.sessions (account_id, token_hash, expires_at, ip_address, user_agent)
		VALUES ($1, $2, $3, $4, $5)`,
		alpha.ID, []byte("alpha-session-token-hash-32-bytes!"), exportBase.Add(24*time.Hour), exportIPAddress, exportUserAgent); err != nil {
		t.Fatalf("seed alpha session: %v", err)
	}

	arenaID := seedPersonalExportArena(t, ctx, pool, alpha.ID, "alpha-personal-arena", "Alpha personal export statement")
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.debate_positions (arena_id, account_id, initial_position, current_position, version)
		VALUES ($1, $2, 'agree', 'disagree', 2)`, arenaID, alpha.ID); err != nil {
		t.Fatalf("seed alpha position: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.position_changes (arena_id, account_id, from_position, to_position, version, changed_at)
		VALUES ($1, $2, 'agree', 'disagree', 2, $3)`, arenaID, alpha.ID, exportBase); err != nil {
		t.Fatalf("seed alpha change: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status, version)
		VALUES ($1, 'Alpha private draft statement', 'technology', 'pt-BR', 'draft', 1)`, alpha.ID); err != nil {
		t.Fatalf("seed alpha draft: %v", err)
	}

	publishedArgument := seedPersonalExportArgument(t, ctx, pool, arenaID, alpha.ID, "support", exportAlphaArgument, "published")
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.argument_sources (argument_id, url, description)
		VALUES ($1, 'https://example.com/alpha-source', 'Alpha source description')`, publishedArgument); err != nil {
		t.Fatalf("seed alpha source: %v", err)
	}
	withdrawnArgument := seedPersonalExportArgument(t, ctx, pool, arenaID, alpha.ID, "oppose", exportWithdrawnText, "withdrawn")
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.argument_sources (argument_id, url, description)
		VALUES ($1, 'https://example.com/withdrawn-source', NULL)`, withdrawnArgument); err != nil {
		t.Fatalf("seed withdrawn source: %v", err)
	}
	seedPersonalExportArgument(t, ctx, pool, arenaID, alpha.ID, "context", exportRemovedText, "removed")

	if _, err := pool.Exec(ctx, `
		INSERT INTO app.wallet_accounts (account_id, balance_free, balance_purchased)
		VALUES ($1, 1234, 5678)`, alpha.ID); err != nil {
		t.Fatalf("seed alpha wallet: %v", err)
	}
	var operationID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.wallet_operations (account_id, operation_type, idempotency_key, reference)
		VALUES ($1, 'credit_purchase', 'alpha-operation-key', $2)
		RETURNING id`, alpha.ID, exportOperationRef).Scan(&operationID); err != nil {
		t.Fatalf("seed alpha operation: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.wallet_transactions (operation_id, bucket, amount, created_at)
		VALUES ($1, 'PURCHASED_INK', 5678, $2)`, operationID, exportBase); err != nil {
		t.Fatalf("seed alpha transaction: %v", err)
	}

	var lotID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arena_pass_lots (account_id, origin, quantity, remaining_quantity, reference, created_at)
		VALUES ($1, 'PURCHASE', 2, 1, $2, $3)
		RETURNING id`, alpha.ID, exportLotReference, exportBase).Scan(&lotID); err != nil {
		t.Fatalf("seed alpha lot: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.arena_pass_consumptions (lot_id, arena_id, consumed_at)
		VALUES ($1, $2, $3)`, lotID, arenaID, exportBase); err != nil {
		t.Fatalf("seed alpha consumption: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO app.stripe_customers (account_id, stripe_customer_id, livemode)
		VALUES ($1, 'cus_alphaSecret', false)`, alpha.ID); err != nil {
		t.Fatalf("seed alpha stripe customer: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.checkout_intents (
			account_id, market, product_id, catalog_version, currency, amount_minor,
			livemode, status, stripe_checkout_session_id, stripe_payment_intent_id, created_at, paid_at
		)
		VALUES ($1, 'BR', 'ink_10000', 1, 'BRL', 990, false, 'paid', 'cs_test_alphaSecret', 'pi_alphaSecret', $2, $2)`,
		alpha.ID, exportBase); err != nil {
		t.Fatalf("seed alpha checkout intent: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.subscriptions (
			account_id, stripe_subscription_id, status, livemode, market, product_id,
			catalog_version, stripe_price_id, current_period_start, current_period_end, created_at, updated_at
		)
		VALUES ($1, 'sub_alphaSecret', 'active', false, 'BR', 'member_monthly', 1, 'price_alphaSecret', $2, $3, $2, $2)`,
		alpha.ID, exportBase, exportBase.Add(30*24*time.Hour)); err != nil {
		t.Fatalf("seed alpha subscription: %v", err)
	}

	// Beta account data must never appear in an alpha export.
	betaArena := seedPersonalExportArena(t, ctx, pool, beta.ID, "beta-personal-arena", "Beta personal export statement")
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.debate_positions (arena_id, account_id, initial_position, current_position, version)
		VALUES ($1, $2, 'disagree', 'disagree', 1)`, betaArena, beta.ID); err != nil {
		t.Fatalf("seed beta position: %v", err)
	}
	seedPersonalExportArgument(t, ctx, pool, betaArena, beta.ID, "support", exportBetaArgument, "published")

	clock := exportFixedClock{now: exportBase}
	request, err := application.NewRequestPersonalExportUseCase(repo, &exportRandom{}, clock)
	if err != nil {
		t.Fatalf("NewRequestPersonalExportUseCase: %v", err)
	}
	generate, err := application.NewGeneratePersonalExportUseCase(repo, repo, exportjson.NewEncoder(), clock)
	if err != nil {
		t.Fatalf("NewGeneratePersonalExportUseCase: %v", err)
	}
	download, err := application.NewDownloadPersonalExportUseCase(repo, clock)
	if err != nil {
		t.Fatalf("NewDownloadPersonalExportUseCase: %v", err)
	}

	return &personalExportHarness{
		repo:     repo,
		request:  request,
		generate: generate,
		download: download,
		pool:     pool,
		alphaID:  domain.AccountID(uuidString(alpha.ID)),
		betaID:   domain.AccountID(uuidString(beta.ID)),
		arenaID:  arenaID,
	}
}

// exportRandom yields a different deterministic token on every call, so
// token rotation is observable without real entropy.
type exportRandom struct {
	counter byte
}

func (r *exportRandom) Read(buffer []byte) (int, error) {
	r.counter++
	for i := range buffer {
		buffer[i] = r.counter
	}
	return len(buffer), nil
}

func seedPersonalExportProfile(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID pgtype.UUID, username, locale, timezone string) error {
	t.Helper()
	var zone any
	if timezone != "" {
		zone = timezone
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO app.profiles (account_id, username, username_normalized, interface_locale, timezone)
		VALUES ($1, $2, lower($2), $3, $4)`, accountID, username, locale, zone)
	return err
}

func seedPersonalExportArena(t *testing.T, ctx context.Context, pool *pgxpool.Pool, creator pgtype.UUID, slug, statement string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status, slug, published_at)
		VALUES ($1, $2, 'technology', 'pt-BR', 'published', $3, $4)
		RETURNING id`, creator, statement, slug, exportBase).Scan(&id); err != nil {
		t.Fatalf("seed arena %s: %v", slug, err)
	}
	return id
}

func seedPersonalExportArgument(t *testing.T, ctx context.Context, pool *pgxpool.Pool, arenaID, authorID pgtype.UUID, relation, content, status string) pgtype.UUID {
	t.Helper()
	var withdrawnAt any
	if status == "withdrawn" {
		withdrawnAt = exportBase
	}
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arguments (arena_id, author_id, relation, content, content_hash, grapheme_cost, status, withdrawn_at)
		VALUES ($1, $2, $3, $4, 'personal-export-hash', 10, $5, $6)
		RETURNING id`, arenaID, authorID, relation, content, status, withdrawnAt).Scan(&id); err != nil {
		t.Fatalf("seed argument %s: %v", content, err)
	}
	return id
}

func generateAlphaExport(t *testing.T, harness *personalExportHarness) (string, string) {
	t.Helper()
	ctx := context.Background()
	requested, err := harness.request.Execute(ctx, harness.alphaID)
	if err != nil {
		t.Fatalf("request export: %v", err)
	}
	if _, err := harness.generate.Execute(ctx, requested.ExportID); err != nil {
		t.Fatalf("generate export: %v", err)
	}
	return requested.ExportID, requested.DownloadToken
}

func decodeExportedDocument(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode personal export: %v", err)
	}
	return document
}

func TestPersonalExportReconstructsOnlyTheOwnerData(t *testing.T) {
	harness := setupPersonalExportHarness(t)
	ctx := context.Background()
	exportID, token := generateAlphaExport(t, harness)

	downloaded, err := harness.download.Execute(ctx, application.DownloadPersonalExportCommand{
		AccountID: harness.alphaID, ExportID: exportID, Token: token,
	})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	sum := sha256.Sum256(downloaded.Document)
	if downloaded.DocumentSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("content hash does not match the served document")
	}

	document := decodeExportedDocument(t, downloaded.Document)
	if document["schema_version"] != float64(domain.ExportSchemaVersion) {
		t.Fatalf("schema_version = %v", document["schema_version"])
	}
	account, _ := document["account"].(map[string]any)
	if account["email"] != exportAlphaEmail || account["email_verified"] != true {
		t.Fatalf("account = %v", account)
	}
	profile, _ := account["profile"].(map[string]any)
	if profile["username"] != exportAlphaUsername || profile["timezone"] != "America/Sao_Paulo" {
		t.Fatalf("profile = %v", profile)
	}
	preferences, _ := account["preferences"].(map[string]any)
	if preferences["marketing_opt_in"] != true {
		t.Fatalf("preferences = %v", preferences)
	}
	if _, ok := account["sessions"].([]any); !ok {
		t.Fatalf("sessions = %v, want a list", account["sessions"])
	}

	serialized := string(downloaded.Document)
	for _, marker := range []string{
		exportAlphaArgument, exportWithdrawnText, "https://example.com/alpha-source",
		"Alpha personal export statement", "Alpha private draft statement",
		`"balance_free":1234`, `"balance_purchased":5678`,
		`"origin":"PURCHASE"`, `"quantity":2`, `"remaining":1`,
		`"product_id":"ink_10000"`, `"amount_minor":990`,
		`"product_id":"member_monthly"`, `"status":"active"`,
	} {
		if !strings.Contains(serialized, marker) {
			t.Fatalf("document is missing owner data %q", marker)
		}
	}

	// Restricted, provider and foreign data never crosses.
	for _, marker := range []string{
		exportBetaEmail, exportBetaUsername, exportBetaArgument, "beta-personal-arena",
		exportRemovedText, "removed personal argument content",
		exportIPAddress, exportUserAgent,
		exportLotReference, exportOperationRef,
		"cus_alphaSecret", "cs_test_alphaSecret", "pi_alphaSecret", "sub_alphaSecret", "price_alphaSecret",
		"session-token", "password",
	} {
		if strings.Contains(serialized, marker) {
			t.Fatalf("document leaks forbidden marker %q", marker)
		}
	}

	// The beta export is the mirror image of the isolation proof.
	betaRequested, err := harness.request.Execute(ctx, harness.betaID)
	if err != nil {
		t.Fatalf("request beta export: %v", err)
	}
	if _, err := harness.generate.Execute(ctx, betaRequested.ExportID); err != nil {
		t.Fatalf("generate beta export: %v", err)
	}
	betaDownloaded, err := harness.download.Execute(ctx, application.DownloadPersonalExportCommand{
		AccountID: harness.betaID, ExportID: betaRequested.ExportID, Token: betaRequested.DownloadToken,
	})
	if err != nil {
		t.Fatalf("download beta: %v", err)
	}
	betaSerialized := string(betaDownloaded.Document)
	if !strings.Contains(betaSerialized, exportBetaArgument) || strings.Contains(betaSerialized, exportAlphaArgument) {
		t.Fatal("beta export mixed account data")
	}
}

func TestPersonalExportDownloadIsSingleUseOwnerScopedAndExpiring(t *testing.T) {
	harness := setupPersonalExportHarness(t)
	ctx := context.Background()
	exportID, token := generateAlphaExport(t, harness)

	if _, err := harness.download.Execute(ctx, application.DownloadPersonalExportCommand{
		AccountID: harness.alphaID, ExportID: exportID, Token: token,
	}); err != nil {
		t.Fatalf("first download: %v", err)
	}
	if _, err := harness.download.Execute(ctx, application.DownloadPersonalExportCommand{
		AccountID: harness.alphaID, ExportID: exportID, Token: token,
	}); !errors.Is(err, application.ErrExportUnavailable) {
		t.Fatalf("replay error = %v, want ErrExportUnavailable", err)
	}
	if _, err := harness.download.Execute(ctx, application.DownloadPersonalExportCommand{
		AccountID: harness.betaID, ExportID: exportID, Token: token,
	}); !errors.Is(err, application.ErrExportNotFound) {
		t.Fatalf("foreign owner error = %v, want ErrExportNotFound", err)
	}

	// Expiring the link expires the capability even for a fresh request.
	freshExport, freshToken := generateAlphaExport(t, harness)
	lateClock := exportFixedClock{now: exportBase.Add(domain.ExportTTL + time.Hour)}
	late, err := application.NewDownloadPersonalExportUseCase(harness.repo, lateClock)
	if err != nil {
		t.Fatalf("NewDownloadPersonalExportUseCase: %v", err)
	}
	if _, err := late.Execute(ctx, application.DownloadPersonalExportCommand{
		AccountID: harness.alphaID, ExportID: freshExport, Token: freshToken,
	}); !errors.Is(err, application.ErrExportUnavailable) {
		t.Fatalf("expired link error = %v, want ErrExportUnavailable", err)
	}

	// Past its window the old record is expired and a new request receives a
	// brand-new record instead of reviving the stale capability.
	lateRequest, err := application.NewRequestPersonalExportUseCase(harness.repo, &exportRandom{}, lateClock)
	if err != nil {
		t.Fatalf("NewRequestPersonalExportUseCase: %v", err)
	}
	renewed, err := lateRequest.Execute(ctx, harness.alphaID)
	if err != nil {
		t.Fatalf("renew request: %v", err)
	}
	if renewed.ExportID == freshExport {
		t.Fatalf("expired export %s must not be revived", freshExport)
	}
	var oldStatus string
	if err := harness.pool.QueryRow(ctx, `SELECT status FROM app.data_exports WHERE id = $1`, mustExportUUID(t, freshExport)).Scan(&oldStatus); err != nil {
		t.Fatalf("load expired record: %v", err)
	}
	if oldStatus != string(domain.ExportStatusExpired) {
		t.Fatalf("old record status = %q, want expired", oldStatus)
	}
}

func mustExportUUID(t *testing.T, exportID string) pgtype.UUID {
	t.Helper()
	var parsed pgtype.UUID
	if err := parsed.Scan(exportID); err != nil {
		t.Fatalf("parse export id %q: %v", exportID, err)
	}
	return parsed
}

func TestPersonalExportReplayRotatesTokenAndGenerationIsIdempotent(t *testing.T) {
	harness := setupPersonalExportHarness(t)
	ctx := context.Background()
	exportID, token := generateAlphaExport(t, harness)

	replayed, err := harness.request.Execute(ctx, harness.alphaID)
	if err != nil {
		t.Fatalf("replay request: %v", err)
	}
	if replayed.ExportID != exportID || replayed.DownloadToken == token {
		t.Fatalf("replay must reuse the record and rotate the token: %+v", replayed)
	}
	if _, err := harness.download.Execute(ctx, application.DownloadPersonalExportCommand{
		AccountID: harness.alphaID, ExportID: exportID, Token: token,
	}); !errors.Is(err, application.ErrInvalidExportToken) {
		t.Fatalf("rotated token error = %v, want ErrInvalidExportToken", err)
	}

	regenerated, err := harness.generate.Execute(ctx, exportID)
	if err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	if !regenerated.Replayed {
		t.Fatal("generation of a ready export must replay")
	}
	if _, err := harness.download.Execute(ctx, application.DownloadPersonalExportCommand{
		AccountID: harness.alphaID, ExportID: exportID, Token: replayed.DownloadToken,
	}); err != nil {
		t.Fatalf("rotated token download: %v", err)
	}
}

func TestPersonalExportSchemaRetainsAndProtectsRecords(t *testing.T) {
	harness := setupPersonalExportHarness(t)
	ctx := context.Background()
	exportID, _ := generateAlphaExport(t, harness)

	var exportUUID pgtype.UUID
	if err := exportUUID.Scan(exportID); err != nil {
		t.Fatalf("parse export id: %v", err)
	}

	assertConstraint := func(operation, constraint string, err error) {
		t.Helper()
		var pgError *pgconn.PgError
		if !errors.As(err, &pgError) || pgError.Code != "23514" || pgError.ConstraintName != constraint {
			t.Fatalf("%s error = %v, want 23514/%s", operation, err, constraint)
		}
	}
	_, err := harness.pool.Exec(ctx, `DELETE FROM app.data_exports WHERE id = $1`, exportUUID)
	assertConstraint("delete", "data_exports_retained", err)

	_, err = harness.pool.Exec(ctx, `UPDATE app.data_exports SET document = '{"other":true}' WHERE id = $1`, exportUUID)
	assertConstraint("rewrite document", "data_exports_document_immutable", err)

	// A fresh record with invalid JSON is rejected by the validation
	// trigger, so a corrupt document can never be stored.
	_, err = harness.pool.Exec(ctx, `
		INSERT INTO app.data_exports (
			account_id, status, download_token_hash, generated_at, expires_at,
			document, document_sha256
		)
		VALUES (
			(SELECT account_id FROM app.data_exports WHERE id = $1),
			'ready', $2, now(), now() + interval '1 hour',
			'{not-json', repeat('0', 64)
		)`, exportUUID, make([]byte, 32))
	assertConstraint("invalid json", "data_exports_document_json", err)

	_, err = harness.pool.Exec(ctx, `
		INSERT INTO app.data_exports (account_id, download_token_hash)
		VALUES ((SELECT account_id FROM app.data_exports WHERE id = $1), $2)`,
		exportUUID, []byte("short"))
	var shortHashError *pgconn.PgError
	if !errors.As(err, &shortHashError) || shortHashError.Code != "23514" || shortHashError.ConstraintName != "data_exports_token_hash_check" {
		t.Fatalf("short token hash error = %v, want 23514/data_exports_token_hash_check", err)
	}

	// One active export per account: concurrent requests resolve to the
	// same record instead of accumulating jobs.
	replayID, _ := generateAlphaExport(t, harness)
	if replayID != exportID {
		t.Fatalf("second request created %s, want the active %s", replayID, exportID)
	}
}
