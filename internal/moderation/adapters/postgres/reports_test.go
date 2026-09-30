package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

func TestReportsRoundTripDedupAndRate(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	reporter := mustModerationAccount(t, ctx, q, "mod-reports-reporter@arena.example.com")
	owner := mustModerationAccount(t, ctx, q, "mod-reports-owner@arena.example.com")
	reporterID := domain.AccountID(uuidString(reporter.ID))

	var arenaID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status)
		VALUES ($1, 'Reports probe statement', 'technology', 'pt-BR', 'draft')
		RETURNING id`, owner.ID).Scan(&arenaID); err != nil {
		t.Fatalf("seed arena: %v", err)
	}

	info, err := repo.DescribeArena(ctx, arenaID)
	if err != nil {
		t.Fatalf("DescribeArena: %v", err)
	}
	if !info.Exists || info.Removed {
		t.Fatalf("arena info = %+v, want existing draft", info)
	}

	stored, err := repo.Insert(ctx, application.InsertReportRequest{
		Reporter: reporterID,
		Target:   domain.TargetArena,
		TargetID: arenaID,
		Reason:   domain.ReasonSpam,
		Context:  "probe context",
	})
	if err != nil || stored.ID == "" {
		t.Fatalf("Insert: %+v, %v", stored, err)
	}

	duplicate, err := repo.FindDuplicate(ctx, reporterID, domain.TargetArena, arenaID, domain.ReasonSpam, time.Now().Add(-domain.DuplicateWindow))
	if err != nil || duplicate == nil || duplicate.ID != stored.ID {
		t.Fatalf("FindDuplicate = %+v, %v; want %s", duplicate, err, stored.ID)
	}
	other, err := repo.FindDuplicate(ctx, reporterID, domain.TargetArena, arenaID, domain.ReasonFraud, time.Now().Add(-domain.DuplicateWindow))
	if err != nil || other != nil {
		t.Fatalf("other reason duplicate = %+v, %v; want nil", other, err)
	}

	recent, err := repo.CountRecentByReporter(ctx, reporterID, time.Now().Add(-domain.RateWindow))
	if err != nil || recent != 1 {
		t.Fatalf("recent = %d, %v; want 1", recent, err)
	}

	// Volume never removes: the arena row is byte-identical afterwards.
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM app.arenas WHERE id = $1`, arenaID).Scan(&status); err != nil {
		t.Fatalf("reload arena: %v", err)
	}
	if status != "draft" {
		t.Fatalf("arena status = %s, want draft untouched", status)
	}
}
