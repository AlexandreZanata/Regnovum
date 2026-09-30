package postgres_test

// The write side of the administrative assignment schema (P19-T09), exercised
// against real PostgreSQL: the table lock that serializes the decision, the
// grant that revives without rewriting its origin, and the revocation that
// reports whether this call was the one that dated it. These are the properties
// the bootstrap's correctness rests on, and none of them can be proved against
// a fake: they are the database's behavior.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

const administrationTestEmail = "admin-bootstrap@arena.example.com"

func TestAdministrationWritesRefuseToRunOutsideATransaction(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	repo := postgres.NewRepository(testDB.Pool.Pool())

	// A lock released at the end of a statement would protect nothing, so
	// the adapter refuses a call that carries no transaction instead of
	// taking one that expires immediately.
	if err := repo.LockAssignments(ctx); !errors.Is(err, application.ErrAdministrationOutsideTransaction) {
		t.Fatalf("LockAssignments outside a transaction = %v, want %v", err, application.ErrAdministrationOutsideTransaction)
	}
}

func TestAdministrationGrantsRevivesAndRevokes(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)
	transactions := platformpg.NewTxManager(pool)

	candidate := mustModerationAccount(t, ctx, q, administrationTestEmail)
	candidateID := domain.AccountID(uuidString(candidate.ID))
	grantedAt := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)

	var active bool
	if err := transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := repo.LockAssignments(txCtx); err != nil {
			return err
		}
		before, err := repo.AnyActiveAssignment(txCtx)
		if err != nil {
			return err
		}
		if before {
			t.Fatal("the installation reported an administrator before the first grant")
		}
		record, err := repo.Grant(txCtx, application.RoleGrantRequest{
			AccountID: candidateID,
			Role:      domain.RoleAdmin,
			GrantedBy: candidateID,
			GrantedAt: grantedAt,
		})
		if err != nil {
			return err
		}
		if record.GrantedBy != candidateID || !record.GrantedAt.Equal(grantedAt) {
			t.Errorf("record = %+v, want the origin the grant stated", record)
		}
		active, err = repo.AnyActiveAssignment(txCtx)
		return err
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if !active {
		t.Fatal("the installation reported no administrator after the grant")
	}

	revokedAt := grantedAt.Add(time.Hour)
	revoked, err := repo.Revoke(ctx, candidateID, revokedAt)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if !revoked {
		t.Fatal("the first revocation of an active assignment reported false")
	}

	// The second demotion is not this call's fact: the instant the first one
	// recorded stays.
	again, err := repo.Revoke(ctx, candidateID, revokedAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	if again {
		t.Fatal("a second revocation of the same assignment reported true")
	}

	var revokedCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.admin_roles WHERE revoked_at IS NOT NULL`).Scan(&revokedCount); err != nil {
		t.Fatalf("count revoked assignments: %v", err)
	}
	if revokedCount != 1 {
		t.Fatalf("revoked assignments = %d, want one", revokedCount)
	}

	// Reviving keeps the origin of the assignment: the schema declares it
	// immutable, so a second grant must not rewrite who granted it or when.
	grantedAgainAt := revokedAt.Add(2 * time.Hour)
	if err := transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := repo.LockAssignments(txCtx); err != nil {
			return err
		}
		_, err := repo.Grant(txCtx, application.RoleGrantRequest{
			AccountID: candidateID,
			Role:      domain.RoleAdmin,
			GrantedBy: candidateID,
			GrantedAt: grantedAgainAt,
		})
		return err
	}); err != nil {
		t.Fatalf("revive: %v", err)
	}

	var grantor string
	var storedGrantedAt time.Time
	var storedRevokedAt *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT granted_by::text, granted_at, revoked_at FROM app.admin_roles WHERE account_id = $1`, candidate.ID,
	).Scan(&grantor, &storedGrantedAt, &storedRevokedAt); err != nil {
		t.Fatalf("read the assignment: %v", err)
	}
	if storedRevokedAt != nil {
		t.Fatalf("revoked_at = %s, want NULL after the revival", storedRevokedAt)
	}
	if !storedGrantedAt.Equal(grantedAt) {
		t.Fatalf("granted_at = %s, want the original %s", storedGrantedAt, grantedAt)
	}

	activeAfterRevival, err := repo.AnyActiveAssignment(ctx)
	if err != nil {
		t.Fatalf("AnyActiveAssignment: %v", err)
	}
	if !activeAfterRevival {
		t.Fatal("the revived assignment is not counted as active")
	}
}

func TestAdministrationSerializesConcurrentFirstGrants(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)
	transactions := platformpg.NewTxManager(pool)

	const candidates = 6
	accountIDs := make([]domain.AccountID, 0, candidates)
	for i := 0; i < candidates; i++ {
		account := mustModerationAccount(t, ctx, q, fmt.Sprintf("admin-race-%d@arena.example.com", i))
		accountIDs = append(accountIDs, domain.AccountID(uuidString(account.ID)))
	}

	// Every command does what the use case does: take the lock, ask whether
	// the installation has an administrator, and insert only if it does not.
	// The lock is what makes that decision and that write one step; without
	// it the table would end with several first administrators.
	var insertions int32
	var unexpected int32
	start := make(chan struct{})
	var waitGroup sync.WaitGroup
	for index, accountID := range accountIDs {
		waitGroup.Add(1)
		go func(index int, accountID domain.AccountID) {
			defer waitGroup.Done()
			<-start
			err := transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
				if err := repo.LockAssignments(txCtx); err != nil {
					return err
				}
				active, err := repo.AnyActiveAssignment(txCtx)
				if err != nil {
					return err
				}
				if active {
					return nil
				}
				if _, err := repo.Grant(txCtx, application.RoleGrantRequest{
					AccountID: accountID,
					Role:      domain.RoleAdmin,
					GrantedBy: accountID,
					GrantedAt: time.Date(2026, 9, 22, 10, 0, index, 0, time.UTC),
				}); err != nil {
					return err
				}
				atomic.AddInt32(&insertions, 1)
				return nil
			})
			if err != nil {
				atomic.AddInt32(&unexpected, 1)
			}
		}(index, accountID)
	}
	close(start)
	waitGroup.Wait()

	if unexpected != 0 {
		t.Fatalf("commands failed unexpectedly: %d", unexpected)
	}
	if insertions != 1 {
		t.Fatalf("insertions = %d, want exactly one first administrator", insertions)
	}

	var stored int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.admin_roles`).Scan(&stored); err != nil {
		t.Fatalf("count assignments: %v", err)
	}
	if stored != 1 {
		t.Fatalf("stored assignments = %d, want one", stored)
	}
}
