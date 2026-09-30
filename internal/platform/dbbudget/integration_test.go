package dbbudget_test

import (
	"context"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbbudget"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

var hotPathBudgets = map[string]dbbudget.Budget{
	"feed":    {Name: "feed", MaxQueries: 1, MaxLatency: 2 * time.Second},
	"arena":   {Name: "arena", MaxQueries: 1, MaxLatency: 2 * time.Second},
	"profile": {Name: "profile", MaxQueries: 1, MaxLatency: 2 * time.Second},
	"wallet":  {Name: "wallet", MaxQueries: 1, MaxLatency: 2 * time.Second},
}

func TestHotPathBudgetsCountRealPostgreSQLQueries(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	queries := platformpg.New(pool)
	setupCtx := context.Background()

	account, err := queries.CreateAccount(setupCtx, platformpg.CreateAccountParams{
		Email:  "query-budget@arena.example.com",
		Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.CreateProfile(setupCtx, platformpg.CreateProfileParams{
		AccountID:          account.ID,
		Username:           "QueryBudget",
		UsernameNormalized: "querybudget",
		InterfaceLocale:    "pt-BR",
	}); err != nil {
		t.Fatal(err)
	}
	if err := queries.EnsureWalletAccount(setupCtx, account.ID); err != nil {
		t.Fatal(err)
	}
	var arenaID string
	if err := pool.QueryRow(setupCtx, `
		INSERT INTO app.arenas (creator_id, slug, statement, category, language, status, published_at)
		VALUES ($1, 'query-budget-arena', 'Afirmação sintética de orçamento', 'technology', 'pt-BR', 'published', now())
		RETURNING id`, account.ID).Scan(&arenaID); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		query func(context.Context) error
	}{
		{
			name: "feed",
			query: func(ctx context.Context) error {
				rows, err := pool.Query(ctx, `
					SELECT id, slug, statement, category, language, status, published_at
					FROM app.arenas
					WHERE status IN ('published', 'closed', 'restricted')
					ORDER BY published_at DESC, id DESC
					LIMIT 21`)
				if err != nil {
					return err
				}
				defer rows.Close()
				for rows.Next() {
					var id, slug, statement, category, language, status string
					var publishedAt time.Time
					if err := rows.Scan(&id, &slug, &statement, &category, &language, &status, &publishedAt); err != nil {
						return err
					}
				}
				return rows.Err()
			},
		},
		{
			name: "arena",
			query: func(ctx context.Context) error {
				var statement string
				return pool.QueryRow(ctx, `SELECT statement FROM app.arenas WHERE id = $1`, arenaID).Scan(&statement)
			},
		},
		{
			name: "profile",
			query: func(ctx context.Context) error {
				row, err := queries.GetProfileByAccountID(ctx, account.ID)
				if err != nil {
					return err
				}
				if row.Username != "QueryBudget" {
					return &unexpectedValue{field: "profile.username", got: row.Username}
				}
				return nil
			},
		},
		{
			name: "wallet",
			query: func(ctx context.Context) error {
				_, err := queries.GetWalletAccount(ctx, account.ID)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tracker := dbbudget.NewTracker()
			ctx := dbbudget.WithTracker(context.Background(), tracker)
			if err := test.query(ctx); err != nil {
				t.Fatal(err)
			}
			if err := tracker.Assert(hotPathBudgets[test.name]); err != nil {
				t.Fatal(err)
			}
			if observation := tracker.Observation(); observation.Queries != 1 {
				t.Fatalf("observation = %+v, want exactly one database query", observation)
			}
		})
	}
}

type unexpectedValue struct {
	field string
	got   string
}

func (e *unexpectedValue) Error() string { return e.field + " = " + e.got }
