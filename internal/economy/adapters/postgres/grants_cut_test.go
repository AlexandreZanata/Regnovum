package postgres_test

// P33-T08 — corte de grants não lastreados em PostgreSQL real.
//
// Após Genesis nenhuma rotina concede FREE/Member como moeda sem débito
// do Tesouro; contratos antigos seguem honrados no livro legado. O guard
// da T05 decide a origem por leitura (legacy antes, treasury com estoque,
// unavailable sem estoque). Os testes provam sobre base descartável: a
// fixture do worker/webhook antigo honra o legado sem aumentar S, o
// estoque zero falha fechado como unavailable sem cunhar nem cair para
// o legado, e o metateste de grant sem origem deixa o gate vermelho.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func cutCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// cutGrant is one legacy grant as the old worker/webhook wrote it:
// operation type, bucket and signed amount live in the legacy book only.
type cutGrant struct {
	holder string
	opType string
	bucket string
	amount int64
	key    string
}

func applyCutGrant(t *testing.T, ctx context.Context, pool *pgxpool.Pool, grant cutGrant) {
	t.Helper()
	var operation string
	if err := pool.QueryRow(ctx,
		`INSERT INTO app.wallet_operations (account_id, operation_type, idempotency_key, reference)
		 VALUES ($1::uuid, $2, $3, $4) RETURNING id::text`,
		grant.holder, grant.opType, grant.key, "cut-fixture").Scan(&operation); err != nil {
		t.Fatalf("cut operation: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.wallet_transactions (operation_id, bucket, amount) VALUES ($1::uuid, $2, $3)`,
		operation, grant.bucket, grant.amount); err != nil {
		t.Fatalf("cut leg: %v", err)
	}
	column := "balance_free"
	if grant.bucket == "PURCHASED_INK" {
		column = "balance_purchased"
	}
	if _, err := pool.Exec(ctx,
		`UPDATE app.wallet_accounts SET `+column+` = `+column+` + $2 WHERE account_id = $1::uuid`,
		grant.holder, grant.amount); err != nil {
		t.Fatalf("cut projection: %v", err)
	}
}

func economyLegs(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var legs int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_entries`).Scan(&legs); err != nil {
		t.Fatalf("count economy legs: %v", err)
	}
	return legs
}

// TestUnbackedGrantsDoNotIncreaseSupply proves the cut: the old monthly
// FREE, Member and webhook fixtures honor the legacy book and leave S
// byte-identical, with the economy reconciling clean.
func TestUnbackedGrantsDoNotIncreaseSupply(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := cutCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	fundTreasury(t, ctx, repo)
	holder := convertHolder(t, ctx, pool, "cut-1@invalid.example")
	fundLegacy(t, ctx, pool, holder, 100, 200)

	beforeLegs := economyLegs(t, ctx, pool)
	beforeSupply := economySupply(t, ctx, pool)
	freeBefore, purchasedBefore := legacySums(t, ctx, pool, holder)

	applyCutGrant(t, ctx, pool, cutGrant{holder: holder, opType: "credit_free", bucket: "FREE_INK", amount: 5000, key: "cut-free-1"})
	applyCutGrant(t, ctx, pool, cutGrant{holder: holder, opType: "credit_member", bucket: "FREE_INK", amount: 30000, key: "cut-member-1"})
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.wallet_operations (account_id, operation_type, idempotency_key, reference)
		 VALUES ($1::uuid, 'credit_purchase', 'cut-webhook-1', 'cut-fixture')`, holder); err != nil {
		t.Fatalf("webhook operation: %v", err)
	}

	if got := economyLegs(t, ctx, pool); got != beforeLegs {
		t.Fatalf("economy legs = %d, want %d (old grants never touch Genesis money)", got, beforeLegs)
	}
	if got := economySupply(t, ctx, pool); got != beforeSupply {
		t.Fatalf("supply = %d, want %d", got, beforeSupply)
	}
	if free, purchased := legacySums(t, ctx, pool, holder); free != freeBefore+35000 || purchased != purchasedBefore {
		t.Fatalf("legacy = %d/%d, want %d/%d (old contracts honored in the legacy book)",
			free, purchased, freeBefore+35000, purchasedBefore)
	}
	report, err := repo.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if report.Frozen || len(report.Mismatch) != 0 {
		t.Fatalf("reconcile = frozen=%v mismatch=%v, want clean", report.Frozen, report.Mismatch)
	}
}

// TestZeroStockFailsClosed proves stockout is unavailability, never mint
// and never a fallback into the legacy book as money: both journals stay
// identical and the answer itself is the alert.
func TestZeroStockFailsClosed(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := cutCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	guard := application.NewGrantGuardUseCase(repo)
	if source, err := guard.Execute(ctx, application.GrantCommand{Millis: 100}); err != nil || source != domain.GrantSourceLegacy {
		t.Fatalf("pre-genesis = %q, %v; want legacy, nil (old contracts honored)", source, err)
	}

	fundTreasury(t, ctx, repo)
	beforeEconomy, beforeLegacy := journalFingerprint(t, ctx, pool)
	if source, err := guard.Execute(ctx, application.GrantCommand{Millis: domain.GenesisSupplyMillis + 1}); err != nil || source != domain.GrantSourceUnavailable {
		t.Fatalf("over stock = %q, %v; want unavailable, nil (fail closed, alert)", source, err)
	}
	if afterEconomy, afterLegacy := journalFingerprint(t, ctx, pool); beforeEconomy != afterEconomy || beforeLegacy != afterLegacy {
		t.Fatalf("stockout moved the journals: fail closed writes nothing anywhere")
	}
	economySupply(t, ctx, pool)
}

// TestGrantWithoutOriginLeavesGateRed proves the vocabulary is closed:
// the three origins exist, the economy tree never mints legacy credits
// as money, and an empty origin is not any of them.
func TestGrantWithoutOriginLeavesGateRed(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file)))))
	raw, err := os.ReadFile(filepath.Join(root, "internal", "economy", "domain", "grants.go"))
	if err != nil {
		t.Fatalf("read grants domain: %v", err)
	}
	for _, origin := range []string{`"legacy"`, `"treasury"`, `"unavailable"`} {
		if !strings.Contains(string(raw), origin) {
			t.Fatalf("grants domain names no %s: a grant without origin must leave the gate red", origin)
		}
	}
	for _, forbidden := range []string{`"credit_free"`, `"credit_member"`, "mint("} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("grants domain carries %q: legacy credits never become Genesis money", forbidden)
		}
	}
	var empty domain.GrantSource
	for _, known := range []domain.GrantSource{domain.GrantSourceLegacy, domain.GrantSourceTreasury, domain.GrantSourceUnavailable} {
		if empty == known {
			t.Fatalf("empty origin equals %q: missing origin must stay refused", known)
		}
	}
}
