package postgres_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	transparencypg "github.com/AlexandreZanata/Regnovum/internal/transparency/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/transparency/application"
	"github.com/AlexandreZanata/Regnovum/internal/transparency/domain"
)

func TestMetricsReconstructFromSources(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := transparencypg.NewRepository(pool)
	q := platformpg.New(pool)

	windowStart := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	inWindow := windowStart.Add(time.Hour)
	beforeWindow := windowStart.Add(-48 * time.Hour)

	// Six eligible accounts author one argument each; two ineligible
	// accounts stay out of the eligible count.
	var authors []platformpg.AppAccount
	for i := 0; i < 6; i++ {
		acc, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{
			Email:  fmt.Sprintf("metrics-author-%d@arena.example.com", i),
			Status: "active",
		})
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		if _, err := q.SetEmailVerified(ctx, acc.ID); err != nil {
			t.Fatalf("verify account: %v", err)
		}
		authors = append(authors, acc)
	}
	pending, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{Email: "metrics-pending@arena.example.com", Status: "pending"})
	if err != nil {
		t.Fatalf("create pending account: %v", err)
	}
	_ = pending
	unverified, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{Email: "metrics-unverified@arena.example.com", Status: "active"})
	if err != nil {
		t.Fatalf("create unverified account: %v", err)
	}
	_ = unverified

	// Arenas: six published in window plus six of each closed lifecycle
	// state published before the window, plus one draft.
	var publishedArenas []pgtype.UUID
	for i := 0; i < 6; i++ {
		var id pgtype.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO app.arenas (creator_id, statement, category, language, status, slug, published_at)
			VALUES ($1, 'Metrics probe statement', 'technology', 'pt-BR', 'published', $2, $3)
			RETURNING id`, authors[i].ID, fmt.Sprintf("metrics-arena-%02d", i), inWindow).Scan(&id); err != nil {
			t.Fatalf("seed published arena: %v", err)
		}
		publishedArenas = append(publishedArenas, id)
	}
	for _, status := range []string{"closed", "restricted", "removed"} {
		for i := 0; i < 6; i++ {
			if _, err := pool.Exec(ctx, `
				INSERT INTO app.arenas (creator_id, statement, category, language, status, slug, published_at)
				VALUES ($1, 'Metrics probe statement', 'technology', 'pt-BR', $2, $3, $4)`,
				authors[0].ID, status, fmt.Sprintf("metrics-%s-%02d", status, i), beforeWindow); err != nil {
				t.Fatalf("seed %s arena: %v", status, err)
			}
		}
	}

	// Arguments: six published in window (one per author), one withdrawn
	// in window, one removed, six published before the window.
	var publishedArguments []pgtype.UUID
	for i := 0; i < 6; i++ {
		var id pgtype.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO app.arguments (arena_id, author_id, relation, content, content_hash, grapheme_cost, created_at)
			VALUES ($1, $2, 'support', 'Metrics probe argument content', 'metrics-hash', 10, $3)
			RETURNING id`, publishedArenas[i], authors[i].ID, inWindow).Scan(&id); err != nil {
			t.Fatalf("seed published argument: %v", err)
		}
		publishedArguments = append(publishedArguments, id)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.arguments (arena_id, author_id, relation, content, content_hash, grapheme_cost, status, created_at, withdrawn_at)
		VALUES ($1, $2, 'support', 'Metrics probe withdrawn', 'metrics-hash-w', 10, 'withdrawn', $3, $3)`,
		publishedArenas[0], authors[0].ID, inWindow); err != nil {
		t.Fatalf("seed withdrawn argument: %v", err)
	}

	// Position chain: one position row per author plus one change each.
	var changeIDs []pgtype.UUID
	for i := 0; i < 6; i++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO app.debate_positions (arena_id, account_id, initial_position, current_position, version)
			VALUES ($1, $2, 'agree', 'disagree', 2)`, publishedArenas[0], authors[i].ID); err != nil {
			t.Fatalf("seed debate position: %v", err)
		}
		var changeID pgtype.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO app.position_changes (arena_id, account_id, from_position, to_position, version, changed_at)
			VALUES ($1, $2, 'agree', 'disagree', 2, $3)
			RETURNING id`, publishedArenas[0], authors[i].ID, inWindow).Scan(&changeID); err != nil {
			t.Fatalf("seed position change: %v", err)
		}
		changeIDs = append(changeIDs, changeID)
	}

	// Attributions: six valid in window crediting the six arguments, plus
	// one invalidated in window with its decision triple.
	for i := 0; i < 6; i++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO app.persuasion_attributions (position_change_id, attributor_id, argument_id, created_at)
			VALUES ($1, $2, $3, $4)`, changeIDs[i], authors[i].ID, publishedArguments[i], inWindow); err != nil {
			t.Fatalf("seed attribution: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.position_changes (arena_id, account_id, from_position, to_position, version, changed_at)
		VALUES ($1, $2, 'disagree', 'undecided', 3, $3)`, publishedArenas[0], authors[1].ID, beforeWindow); err != nil {
		t.Fatalf("seed extra position change: %v", err)
	}
	var extraChangeID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		SELECT id FROM app.position_changes WHERE arena_id = $1 AND account_id = $2 AND version = 3`,
		publishedArenas[0], authors[1].ID).Scan(&extraChangeID); err != nil {
		t.Fatalf("reload extra change: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.persuasion_attributions
			(position_change_id, attributor_id, argument_id, status, created_at, invalidated_at, moderation_reason, moderated_by, moderated_at)
		VALUES ($1, $2, $3, 'invalid', $4, $4, 'farm probe', $2, $4)`,
		extraChangeID, authors[1].ID, publishedArguments[1], inWindow); err != nil {
		t.Fatalf("seed invalidated attribution: %v", err)
	}

	// INK ledger: six operations per kind with exact deltas.
	for i := 0; i < 6; i++ {
		if _, err := pool.Exec(ctx, `INSERT INTO app.wallet_accounts (account_id, balance_free, balance_purchased) VALUES ($1, 1000000, 1000000)
			ON CONFLICT (account_id) DO UPDATE SET balance_free = 1000000, balance_purchased = 1000000`, authors[i].ID); err != nil {
			t.Fatalf("seed wallet: %v", err)
		}
		seedLedgerLine(t, ctx, pool, authors[i].ID, "credit_free", "FREE_INK", 5000, i, 0, inWindow)
		seedLedgerLine(t, ctx, pool, authors[i].ID, "expire_free", "FREE_INK", -3000, i, 1, inWindow)
		seedLedgerLine(t, ctx, pool, authors[i].ID, "debit_argument", "FREE_INK", -100, i, 2, inWindow)
		seedLedgerLine(t, ctx, pool, authors[i].ID, "credit_purchase", "PURCHASED_INK", 10000, i, 3, inWindow)
		seedLedgerLine(t, ctx, pool, authors[i].ID, "debit_argument", "PURCHASED_INK", -500, i, 4, inWindow)
		seedLedgerLine(t, ctx, pool, authors[i].ID, "debit_refund", "PURCHASED_INK", -2000, i, 5, inWindow)
		seedLedgerLine(t, ctx, pool, authors[i].ID, "credit_admin", "PURCHASED_INK", 700, i, 6, inWindow)
	}

	// Passes: six purchased lots of two, six member lots of one, six
	// consumptions in window.
	for i := 0; i < 6; i++ {
		var lotID pgtype.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO app.arena_pass_lots (account_id, origin, quantity, remaining_quantity, reference, created_at)
			VALUES ($1, 'PURCHASE', 2, 2, $2, $3)
			RETURNING id`, authors[i].ID, fmt.Sprintf("metrics-purchase-%d", i), inWindow).Scan(&lotID); err != nil {
			t.Fatalf("seed purchase lot: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO app.arena_pass_lots (account_id, origin, quantity, remaining_quantity, reference, expires_at, created_at)
			VALUES ($1, 'MEMBER', 1, 1, $2, $3, $3)`, authors[i].ID, fmt.Sprintf("metrics-member-%d", i), inWindow); err != nil {
			t.Fatalf("seed member lot: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO app.arena_pass_consumptions (lot_id, arena_id, consumed_at)
			VALUES ($1, $2, $3)`, lotID, publishedArenas[i], inWindow); err != nil {
			t.Fatalf("seed consumption: %v", err)
		}
	}

	// Moderation: six reports, six cases with one action each, six appeals
	// all reversed in window.
	for i := 0; i < 6; i++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO app.moderation_reports (reporter_id, target_type, target_arena_id, reason, created_at)
			VALUES ($1, 'arena', $2, 'spam', $3)`, authors[i].ID, publishedArenas[i], inWindow); err != nil {
			t.Fatalf("seed report: %v", err)
		}
		var caseID pgtype.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO app.moderation_cases (target_type, target_arena_id, created_at)
			VALUES ('arena', $1, $2)
			RETURNING id`, publishedArenas[i], inWindow).Scan(&caseID); err != nil {
			t.Fatalf("seed case: %v", err)
		}
		var actionID pgtype.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO app.moderation_actions (case_id, action_type, actor_id, rule_applied, justification, created_at)
			VALUES ($1, 'warning', $2, 'MOD-2:warning', 'Metrics probe justification', $3)
			RETURNING id`, caseID, authors[0].ID, inWindow).Scan(&actionID); err != nil {
			t.Fatalf("seed action: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO app.moderation_appeals (action_id, appellant_id, context, status, reviewer_id, decision_reason, created_at, decided_at)
			VALUES ($1, $2, 'Metrics probe appeal context', 'reversed', $2, 'Metrics probe reversal', $3, $3)`,
			actionID, authors[i].ID, inWindow); err != nil {
			t.Fatalf("seed appeal: %v", err)
		}
	}

	raw, err := repo.RawCounts(ctx, windowStart, windowEnd)
	if err != nil {
		t.Fatalf("RawCounts: %v", err)
	}

	want := &application.RawCounts{
		EligibleAccounts: 6, ArenasPublished: 6,
		ArenasClosed: 6, ArenasRestricted: 6, ArenasRemoved: 6,
		ArgumentsPublished: 6, ArgumentsWithdrawn: 1,
		PositionChanges: 6, AttributionsValid: 6, AttributionsInvalidated: 1,
		InfluencedAuthors: 6,
		InkFreeGranted:    30000, InkFreeExpired: 18000, InkFreeConsumed: 600,
		InkPurchasedGranted: 60000, InkPurchasedConsumed: 3000,
		InkRefunded: 12000, InkAdminAdjusted: 4200,
		PassesPurchaseGranted: 12, PassesMemberGranted: 6, PassesConsumed: 6,
		ReportsFiled: 6, ActionsRecorded: 6, AppealsFiled: 6, AppealsReversed: 6,
	}
	if *raw != *want {
		t.Fatalf("raw counts = %+v, want %+v", raw, want)
	}

	// Reconstruction agrees: deriving twice from live sources yields the
	// identical snapshot.
	derive, err := application.NewDeriveMetricsUseCase(repo)
	if err != nil {
		t.Fatalf("NewDeriveMetricsUseCase: %v", err)
	}
	period, err := domain.NewPeriod(windowStart, windowEnd)
	if err != nil {
		t.Fatalf("NewPeriod: %v", err)
	}
	first, err := derive.Execute(ctx, period)
	if err != nil {
		t.Fatalf("first derive: %v", err)
	}
	second, err := derive.Execute(ctx, period)
	if err != nil {
		t.Fatalf("second derive: %v", err)
	}
	if *first != *second {
		t.Fatalf("snapshots diverged:\n%+v\n%+v", first, second)
	}
	if first.MethodologyVersion != domain.MethodologyVersion {
		t.Fatalf("version = %d, want %d", first.MethodologyVersion, domain.MethodologyVersion)
	}
	if first.ArenasPublished != 6 || first.InkFreeGranted != 30000 || first.AppealsReversed != 6 {
		t.Fatalf("snapshot = %+v, want reconstructed values", first)
	}
	if first.ArgumentsWithdrawn != 0 || first.AttributionsInvalidated != 0 {
		t.Fatalf("low counts must suppress to zero: %+v", first)
	}
}

func seedLedgerLine(t *testing.T, ctx context.Context, pool *pgxpool.Pool, account pgtype.UUID, operation, bucket string, amount int64, index, kind int, at time.Time) {
	t.Helper()
	var operationID pgtype.UUID
	reason, actor := any(nil), any(nil)
	if operation == "credit_admin" {
		reason, actor = "metrics probe adjustment", account
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.wallet_operations (account_id, operation_type, idempotency_key, reference, reason, actor_account_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id`, account, operation, fmt.Sprintf("metrics-ledger-%d-%d", index, kind), fmt.Sprintf("metrics-ledger-ref-%d", index), reason, actor).Scan(&operationID); err != nil {
		t.Fatalf("seed ledger operation: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.wallet_transactions (operation_id, bucket, amount, created_at)
		VALUES ($1, $2, $3, $4)`, operationID, bucket, amount, at); err != nil {
		t.Fatalf("seed ledger transaction: %v", err)
	}
}

func TestTransparencyQuerySelectsNoPrivateData(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(transparencyRepoRoot(t), "db", "queries", "transparency.sql"))
	if err != nil {
		t.Fatalf("read transparency.sql: %v", err)
	}
	// Strip line comments: the header documents the privacy property in
	// prose, while the scan below audits selected identifiers.
	var body strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		if index := strings.Index(line, "--"); index >= 0 {
			line = line[:index]
		}
		body.WriteString(line)
		body.WriteString("\n")
	}
	lowered := strings.ToLower(body.String())
	// email_verified_at is the eligibility bit, never selected data: allow
	// it before auditing raw email selections.
	eligibilityStripped := strings.ReplaceAll(lowered, "email_verified_at", "")
	for _, marker := range []string{".email", "stripe", "cus_", "cs_", "sub_", "pi_", "password", "token", "session", "ip_address", "user_agent"} {
		if strings.Contains(eligibilityStripped, marker) {
			t.Fatalf("transparency.sql selects forbidden marker %q", marker)
		}
	}
}

func transparencyRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", "..", "..", ".."))
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	if !strings.Contains(string(data), "module github.com/AlexandreZanata/Regnovum") {
		t.Fatalf("go.mod does not declare the arena module")
	}
	return root
}
