package postgres_test

// P33-T05 — post-Genesis grant sourcing on real PostgreSQL.
//
// New monetary grants leave Treasury stock or do not happen: the guard
// behind every grant decision reads only, writes nothing, and answers
// legacy before Genesis, treasury while funded and unavailable on
// stockout. The tests prove on a disposable database that every answer
// leaves both journals byte-identical.

import (
	"context"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func grantsCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// TestGrantGuardReadsWithoutWriting proves every funding answer is a
// pure read: legacy before Genesis, treasury while funded, unavailable
// on stockout, with both journals identical before and after.
func TestGrantGuardReadsWithoutWriting(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	ctx, cancel := grantsCtx()
	defer cancel()

	repo := postgres.NewRepository(pool)
	guard := application.NewGrantGuardUseCase(repo)

	beforeEconomy, beforeLegacy := journalFingerprint(t, ctx, pool)
	source, err := guard.Execute(ctx, application.GrantCommand{Millis: 100})
	if err != nil || source != domain.GrantSourceLegacy {
		t.Fatalf("pre-genesis = %q, %v; want legacy, nil", source, err)
	}
	if afterEconomy, afterLegacy := journalFingerprint(t, ctx, pool); beforeEconomy != afterEconomy || beforeLegacy != afterLegacy {
		t.Fatalf("pre-genesis read moved the journals")
	}

	fundTreasury(t, ctx, repo)
	settledEconomy, settledLegacy := journalFingerprint(t, ctx, pool)
	if source, err := guard.Execute(ctx, application.GrantCommand{Millis: 100}); err != nil || source != domain.GrantSourceTreasury {
		t.Fatalf("funded = %q, %v; want treasury, nil", source, err)
	}
	if source, err := guard.Execute(ctx, application.GrantCommand{Millis: domain.GenesisSupplyMillis + 1}); err != nil || source != domain.GrantSourceUnavailable {
		t.Fatalf("over stock = %q, %v; want unavailable, nil", source, err)
	}
	if afterEconomy, afterLegacy := journalFingerprint(t, ctx, pool); settledEconomy != afterEconomy || settledLegacy != afterLegacy {
		t.Fatalf("guard reads moved the journals")
	}
}
