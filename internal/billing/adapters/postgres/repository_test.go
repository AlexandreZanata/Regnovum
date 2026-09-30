package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

func mustBillingAccount(t *testing.T, ctx context.Context, q *platformpg.Queries, email string) platformpg.AppAccount {
	t.Helper()
	acc, err := q.CreateAccount(ctx, platformpg.CreateAccountParams{Email: email, Status: "active"})
	if err != nil {
		t.Fatalf("create account %s: %v", email, err)
	}
	return acc
}

func mustGrantRequest(
	t *testing.T,
	accountID domain.AccountID,
	origin domain.PassOrigin,
	quantity int32,
	reference string,
	expiresAt *time.Time,
) application.GrantPassLotRequest {
	t.Helper()
	parsedQuantity, err := domain.NewQuantity(quantity)
	if err != nil {
		t.Fatalf("NewQuantity(%d): %v", quantity, err)
	}
	parsedReference, err := domain.ParseReference(reference)
	if err != nil {
		t.Fatalf("ParseReference(%q): %v", reference, err)
	}
	return application.GrantPassLotRequest{
		AccountID: accountID,
		Origin:    origin,
		Quantity:  parsedQuantity,
		Reference: parsedReference,
		ExpiresAt: expiresAt,
		GrantedAt: time.Now().UTC(),
	}
}

func countPassLots(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID pgtype.UUID) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM app.arena_pass_lots WHERE account_id = $1", accountID).Scan(&count); err != nil {
		t.Fatalf("count pass lots: %v", err)
	}
	return count
}

func TestRepository_GrantPassLotPurchasesAndMember(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	acc := mustBillingAccount(t, ctx, q, "grant-purchase@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	purchase, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginPurchase, 5, "stripe:evt_purchase_1", nil))
	if err != nil {
		t.Fatalf("purchase grant error = %v", err)
	}
	if purchase.Replayed {
		t.Fatal("first purchase grant must not be a replay")
	}
	if purchase.Lot.Origin() != domain.OriginPurchase || purchase.Lot.Quantity().Int32() != 5 || purchase.Lot.Remaining() != 5 {
		t.Fatalf("purchase lot = %+v", purchase.Lot)
	}
	if purchase.Lot.ExpiresAt() != nil {
		t.Fatalf("bought passes do not expire, got %v", purchase.Lot.ExpiresAt())
	}

	periodEnd := time.Date(2026, 10, 1, 0, 0, 0, 0, time.FixedZone("BRT", -3*3600))
	member, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginMember, 1, "member:2026-09", &periodEnd))
	if err != nil {
		t.Fatalf("member grant error = %v", err)
	}
	if member.Lot.ExpiresAt() == nil {
		t.Fatal("member lot must expire at the end of its period")
	}
	if member.Lot.ExpiresAt().Location() != time.UTC || !member.Lot.ExpiresAt().Equal(periodEnd) {
		t.Fatalf("member expiration = %v, want %v in UTC", member.Lot.ExpiresAt(), periodEnd)
	}

	stored, err := q.GetArenaPassLotByGrant(ctx, platformpg.GetArenaPassLotByGrantParams{
		AccountID: acc.ID,
		Origin:    "PURCHASE",
		Reference: "stripe:evt_purchase_1",
	})
	if err != nil {
		t.Fatalf("load stored lot: %v", err)
	}
	if uuidString(stored.ID) != purchase.Lot.ID().String() {
		t.Fatalf("stored lot id = %q, want %q", uuidString(stored.ID), purchase.Lot.ID())
	}
	if countPassLots(t, ctx, pool, acc.ID) != 2 {
		t.Fatalf("lots = %d, want 2", countPassLots(t, ctx, pool, acc.ID))
	}
}

// TestRepository_GrantPassLotIsIdempotentAndImmutableExpiry proves retries
// never duplicate a grant and that a replayed payload can never rewrite the
// original expiration.
func TestRepository_GrantPassLotIsIdempotentAndImmutableExpiry(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	acc := mustBillingAccount(t, ctx, q, "grant-replay@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	originalEnd := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	first, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginMember, 1, "member:2026-09", &originalEnd))
	if err != nil {
		t.Fatalf("first grant error = %v", err)
	}

	// Retry with a drifted payload: different quantity and expiration under
	// the same key must resolve the original lot untouched.
	driftedEnd := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	retry, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginMember, 3, "member:2026-09", &driftedEnd))
	if err != nil {
		t.Fatalf("retry grant error = %v", err)
	}
	if !retry.Replayed {
		t.Fatal("retry must be a replay")
	}
	if retry.Lot.ID() != first.Lot.ID() {
		t.Fatalf("retry lot = %q, want original %q", retry.Lot.ID(), first.Lot.ID())
	}
	if retry.Lot.Quantity().Int32() != 1 || retry.Lot.Remaining() != 1 {
		t.Fatalf("retry rewrote the quantity: %+v", retry.Lot)
	}
	if retry.Lot.ExpiresAt() == nil || !retry.Lot.ExpiresAt().Equal(originalEnd) {
		t.Fatalf("retry rewrote the expiration: %v, want %v", retry.Lot.ExpiresAt(), originalEnd)
	}

	if countPassLots(t, ctx, pool, acc.ID) != 1 {
		t.Fatalf("lots after retry = %d, want exactly 1", countPassLots(t, ctx, pool, acc.ID))
	}
}

func TestRepository_GrantPassLotConcurrent(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t, dbtest.WithPoolLimits(20, 1))
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	acc := mustBillingAccount(t, ctx, q, "grant-concurrent@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))
	request := mustGrantRequest(t, accountID, domain.OriginPurchase, 2, "stripe:evt_concurrent", nil)

	const attempts = 20
	var (
		fresh    atomic.Int32
		replayed atomic.Int32
		wg       sync.WaitGroup
	)
	lotIDs := make([]string, attempts)
	errs := make([]error, attempts)

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			result, err := repo.GrantPassLot(ctx, request)
			if err != nil {
				errs[index] = err
				return
			}
			if result.Replayed {
				replayed.Add(1)
			} else {
				fresh.Add(1)
			}
			lotIDs[index] = result.Lot.ID().String()
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("attempt %d error = %v", i, err)
		}
	}
	if fresh.Load() != 1 || replayed.Load() != attempts-1 {
		t.Fatalf("fresh=%d replayed=%d, want 1/%d", fresh.Load(), replayed.Load(), attempts-1)
	}
	for i, lotID := range lotIDs {
		if lotID != lotIDs[0] {
			t.Fatalf("attempt %d resolved lot %q, want %q", i, lotID, lotIDs[0])
		}
	}
	if got := countPassLots(t, ctx, pool, acc.ID); got != 1 {
		t.Fatalf("lots = %d, want exactly 1", got)
	}
}

func TestRepository_GrantPassLotDistinctGrants(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	acc := mustBillingAccount(t, ctx, q, "grant-distinct@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	grants := []application.GrantPassLotRequest{
		mustGrantRequest(t, accountID, domain.OriginPurchase, 1, "stripe:evt_a", nil),
		mustGrantRequest(t, accountID, domain.OriginPurchase, 1, "stripe:evt_b", nil),
		mustGrantRequest(t, accountID, domain.OriginAdmin, 1, "stripe:evt_a", nil),
	}
	seen := map[string]bool{}
	for i, grant := range grants {
		result, err := repo.GrantPassLot(ctx, grant)
		if err != nil {
			t.Fatalf("grant %d error = %v", i, err)
		}
		if result.Replayed {
			t.Fatalf("grant %d unexpectedly replayed", i)
		}
		if seen[result.Lot.ID().String()] {
			t.Fatalf("grant %d reused a lot id", i)
		}
		seen[result.Lot.ID().String()] = true
	}
	if got := countPassLots(t, ctx, pool, acc.ID); got != len(grants) {
		t.Fatalf("lots = %d, want %d", got, len(grants))
	}
}

func TestRepository_GrantArenaPassesUseCaseEndToEnd(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	acc := mustBillingAccount(t, ctx, q, "grant-use-case@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	fixedNow := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	useCase := application.NewGrantArenaPassesUseCase(repo, fixedClock{now: fixedNow})

	command := application.GrantArenaPassesCommand{
		AccountID: accountID.String(),
		Origin:    "PURCHASE",
		Quantity:  3,
		Reference: "stripe:evt_use_case",
	}
	first, err := useCase.Execute(ctx, command)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if first.Replayed || first.Lot.Quantity().Int32() != 3 {
		t.Fatalf("first grant = %+v", first)
	}

	retry, err := useCase.Execute(ctx, command)
	if err != nil {
		t.Fatalf("retry Execute() error = %v", err)
	}
	if !retry.Replayed || retry.Lot.ID() != first.Lot.ID() {
		t.Fatalf("retry = %+v, want the original lot", retry)
	}
	if got := countPassLots(t, ctx, pool, acc.ID); got != 1 {
		t.Fatalf("lots = %d, want 1", got)
	}

	// Member grants require the period end.
	_, err = useCase.Execute(ctx, application.GrantArenaPassesCommand{
		AccountID: accountID.String(),
		Origin:    "MEMBER",
		Quantity:  1,
		Reference: "member:2026-09",
	})
	if !errors.Is(err, domain.ErrExpirationRequired) {
		t.Fatalf("member without expiration error = %v, want ErrExpirationRequired", err)
	}
}

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time { return c.now }

func mustArenaID(t *testing.T, index int) domain.ArenaID {
	t.Helper()
	arenaID, err := domain.ParseArenaID(fmt.Sprintf("00000000-0000-7000-8000-%012d", index))
	if err != nil {
		t.Fatalf("ParseArenaID(%d): %v", index, err)
	}
	return arenaID
}

func mustConsumeRequest(t *testing.T, accountID domain.AccountID, arena domain.ArenaID, at time.Time) application.ConsumePassRequest {
	t.Helper()
	return application.ConsumePassRequest{AccountID: accountID, ArenaID: arena, ConsumedAt: at}
}

func countPassConsumptions(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID pgtype.UUID) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM app.arena_pass_consumptions c
		JOIN app.arena_pass_lots l ON l.id = c.lot_id
		WHERE l.account_id = $1`, accountID).Scan(&count); err != nil {
		t.Fatalf("count pass consumptions: %v", err)
	}
	return count
}

func mustUUID(t *testing.T, raw string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		t.Fatalf("scan uuid %q: %v", raw, err)
	}
	return id
}

func mustLotRemaining(t *testing.T, ctx context.Context, q *platformpg.Queries, rawLotID string) int32 {
	t.Helper()
	lot, err := q.GetArenaPassLot(ctx, mustUUID(t, rawLotID))
	if err != nil {
		t.Fatalf("reload pass lot: %v", err)
	}
	return lot.RemainingQuantity
}

func TestRepository_ConsumeArenaPassSelectsNearestExpiration(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	now := time.Now().UTC()
	acc := mustBillingAccount(t, ctx, q, "consume-order@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	purchase, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginPurchase, 3, "stripe:evt_order_purchase", nil))
	if err != nil {
		t.Fatalf("purchase grant: %v", err)
	}
	laterEnd := now.Add(2 * time.Hour)
	later, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginMember, 1, "member:2026-09", &laterEnd))
	if err != nil {
		t.Fatalf("later grant: %v", err)
	}
	soonerEnd := now.Add(time.Hour)
	sooner, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginMember, 1, "member:2026-08", &soonerEnd))
	if err != nil {
		t.Fatalf("sooner grant: %v", err)
	}

	first, err := repo.ConsumeArenaPass(ctx, mustConsumeRequest(t, accountID, mustArenaID(t, 1), now))
	if err != nil {
		t.Fatalf("first consumption error = %v", err)
	}
	if first.Replayed || first.Lot.ID() != sooner.Lot.ID() {
		t.Fatalf("first consumption used %q (replayed=%v), want the nearest-expiring lot %q", first.Lot.ID(), first.Replayed, sooner.Lot.ID())
	}

	second, err := repo.ConsumeArenaPass(ctx, mustConsumeRequest(t, accountID, mustArenaID(t, 2), now))
	if err != nil {
		t.Fatalf("second consumption error = %v", err)
	}
	if second.Lot.ID() != later.Lot.ID() {
		t.Fatalf("second consumption used %q, want %q", second.Lot.ID(), later.Lot.ID())
	}

	third, err := repo.ConsumeArenaPass(ctx, mustConsumeRequest(t, accountID, mustArenaID(t, 3), now))
	if err != nil {
		t.Fatalf("third consumption error = %v", err)
	}
	if third.Lot.ID() != purchase.Lot.ID() {
		t.Fatalf("third consumption used %q, want the non-expiring lot %q", third.Lot.ID(), purchase.Lot.ID())
	}
	if third.Remaining != 2 {
		t.Fatalf("remaining after third consumption = %d, want 2", third.Remaining)
	}

	if got := mustLotRemaining(t, ctx, q, sooner.Lot.ID().String()); got != 0 {
		t.Errorf("sooner lot remaining = %d, want 0", got)
	}
	if got := countPassConsumptions(t, ctx, pool, acc.ID); got != 3 {
		t.Errorf("consumptions = %d, want 3", got)
	}
}

func TestRepository_ConsumeArenaPassExpiredLotNeverUsed(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	now := time.Now().UTC()
	acc := mustBillingAccount(t, ctx, q, "consume-expired@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	expiredEnd := now.Add(-time.Hour)
	expired, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginMember, 1, "member:expired", &expiredEnd))
	if err != nil {
		t.Fatalf("expired grant: %v", err)
	}
	exactlyNow := now
	boundary, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginMember, 1, "member:boundary", &exactlyNow))
	if err != nil {
		t.Fatalf("boundary grant: %v", err)
	}

	if _, err := repo.ConsumeArenaPass(ctx, mustConsumeRequest(t, accountID, mustArenaID(t, 10), now)); !errors.Is(err, domain.ErrNoPassAvailable) {
		t.Fatalf("expired-only consumption error = %v, want ErrNoPassAvailable", err)
	}
	if got := mustLotRemaining(t, ctx, q, expired.Lot.ID().String()); got != 1 {
		t.Errorf("expired lot remaining = %d, want 1 untouched", got)
	}
	if got := mustLotRemaining(t, ctx, q, boundary.Lot.ID().String()); got != 1 {
		t.Errorf("boundary lot remaining = %d, want 1 untouched", got)
	}
	if got := countPassConsumptions(t, ctx, pool, acc.ID); got != 0 {
		t.Fatalf("consumptions = %d, want 0", got)
	}

	// A valid lot becomes the only candidate.
	valid, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginPurchase, 1, "stripe:evt_valid", nil))
	if err != nil {
		t.Fatalf("valid grant: %v", err)
	}
	result, err := repo.ConsumeArenaPass(ctx, mustConsumeRequest(t, accountID, mustArenaID(t, 11), now))
	if err != nil {
		t.Fatalf("valid consumption error = %v", err)
	}
	if result.Lot.ID() != valid.Lot.ID() {
		t.Fatalf("valid consumption used %q, want %q", result.Lot.ID(), valid.Lot.ID())
	}
	if got := mustLotRemaining(t, ctx, q, expired.Lot.ID().String()); got != 1 {
		t.Errorf("expired lot was consumed: remaining = %d, want 1", got)
	}
}

func TestRepository_ConsumeArenaPassIsIdempotentPerArena(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	now := time.Now().UTC()
	acc := mustBillingAccount(t, ctx, q, "consume-replay@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	lot, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginPurchase, 2, "stripe:evt_replay_lot", nil))
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	request := mustConsumeRequest(t, accountID, mustArenaID(t, 20), now)
	first, err := repo.ConsumeArenaPass(ctx, request)
	if err != nil {
		t.Fatalf("first consumption error = %v", err)
	}
	if first.Replayed || first.Remaining != 1 {
		t.Fatalf("first consumption = %+v, want fresh with 1 remaining", first)
	}

	second, err := repo.ConsumeArenaPass(ctx, request)
	if err != nil {
		t.Fatalf("second consumption error = %v", err)
	}
	if !second.Replayed || second.Lot.ID() != lot.Lot.ID() {
		t.Fatalf("second consumption = %+v, want a replay of the original lot", second)
	}

	if got := mustLotRemaining(t, ctx, q, lot.Lot.ID().String()); got != 1 {
		t.Fatalf("lot remaining = %d, want 1 (consumed exactly once)", got)
	}
	if got := countPassConsumptions(t, ctx, pool, acc.ID); got != 1 {
		t.Fatalf("consumptions = %d, want 1", got)
	}
}

// TestRepository_ConsumeArenaPassConcurrentDifferentArenas is the P07-T03
// acceptance probe: twenty publications compete for a single pass and
// exactly one wins.
func TestRepository_ConsumeArenaPassConcurrentDifferentArenas(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t, dbtest.WithPoolLimits(20, 1))
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	now := time.Now().UTC()
	acc := mustBillingAccount(t, ctx, q, "consume-race@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	lot, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginPurchase, 1, "stripe:evt_race", nil))
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	const attempts = 20
	requests := make([]application.ConsumePassRequest, attempts)
	for i := range requests {
		requests[i] = mustConsumeRequest(t, accountID, mustArenaID(t, 100+i), now)
	}

	var (
		successes atomic.Int32
		noPass    atomic.Int32
		wg        sync.WaitGroup
	)
	unexpected := make([]error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := repo.ConsumeArenaPass(ctx, requests[index])
			switch {
			case err == nil:
				successes.Add(1)
			case errors.Is(err, domain.ErrNoPassAvailable):
				noPass.Add(1)
			default:
				unexpected[index] = err
			}
		}(i)
	}
	wg.Wait()

	for i, err := range unexpected {
		if err != nil {
			t.Fatalf("attempt %d unexpected error = %v", i, err)
		}
	}
	if successes.Load() != 1 {
		t.Fatalf("successes = %d, want exactly 1", successes.Load())
	}
	if noPass.Load() != attempts-1 {
		t.Fatalf("no-pass rejections = %d, want %d", noPass.Load(), attempts-1)
	}

	if got := mustLotRemaining(t, ctx, q, lot.Lot.ID().String()); got != 0 {
		t.Fatalf("lot remaining = %d, want 0", got)
	}
	if got := countPassConsumptions(t, ctx, pool, acc.ID); got != 1 {
		t.Fatalf("consumptions = %d, want exactly 1", got)
	}
}

// TestRepository_ConsumeArenaPassConcurrentSameArena proves the Arena
// uniqueness keeps concurrent publications of the same Arena idempotent:
// one fresh consumption and replays for the rest, with no pass lost.
func TestRepository_ConsumeArenaPassConcurrentSameArena(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t, dbtest.WithPoolLimits(4, 1))
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	now := time.Now().UTC()
	acc := mustBillingAccount(t, ctx, q, "consume-same-arena@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	firstLot, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginPurchase, 1, "stripe:evt_same_a", nil))
	if err != nil {
		t.Fatalf("first grant: %v", err)
	}
	secondLot, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginPurchase, 1, "stripe:evt_same_b", nil))
	if err != nil {
		t.Fatalf("second grant: %v", err)
	}

	request := mustConsumeRequest(t, accountID, mustArenaID(t, 200), now)

	const attempts = 4
	var (
		fresh    atomic.Int32
		replayed atomic.Int32
		wg       sync.WaitGroup
	)
	unexpected := make([]error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			result, err := repo.ConsumeArenaPass(ctx, request)
			if err != nil {
				unexpected[index] = err
				return
			}
			if result.Replayed {
				replayed.Add(1)
			} else {
				fresh.Add(1)
			}
		}(i)
	}
	wg.Wait()

	for i, err := range unexpected {
		if err != nil {
			t.Fatalf("attempt %d error = %v", i, err)
		}
	}
	if fresh.Load() != 1 || replayed.Load() != attempts-1 {
		t.Fatalf("fresh=%d replayed=%d, want 1/%d", fresh.Load(), replayed.Load(), attempts-1)
	}
	if got := countPassConsumptions(t, ctx, pool, acc.ID); got != 1 {
		t.Fatalf("consumptions = %d, want exactly 1", got)
	}

	remainingTotal := mustLotRemaining(t, ctx, q, firstLot.Lot.ID().String()) +
		mustLotRemaining(t, ctx, q, secondLot.Lot.ID().String())
	if remainingTotal != 1 {
		t.Fatalf("remaining across lots = %d, want 1 (exactly one pass consumed)", remainingTotal)
	}
}

func TestRepository_ConsumeArenaPassCrossAccountRefused(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	now := time.Now().UTC()
	owner := mustBillingAccount(t, ctx, q, "consume-owner@arena.example.com")
	intruder := mustBillingAccount(t, ctx, q, "consume-intruder@arena.example.com")
	ownerID := domain.AccountID(uuidString(owner.ID))
	intruderID := domain.AccountID(uuidString(intruder.ID))

	if _, err := repo.GrantPassLot(ctx, mustGrantRequest(t, ownerID, domain.OriginPurchase, 1, "stripe:evt_owner", nil)); err != nil {
		t.Fatalf("owner grant: %v", err)
	}
	intruderLot, err := repo.GrantPassLot(ctx, mustGrantRequest(t, intruderID, domain.OriginPurchase, 1, "stripe:evt_intruder", nil))
	if err != nil {
		t.Fatalf("intruder grant: %v", err)
	}

	arena := mustArenaID(t, 300)
	if _, err := repo.ConsumeArenaPass(ctx, mustConsumeRequest(t, ownerID, arena, now)); err != nil {
		t.Fatalf("owner consumption error = %v", err)
	}
	if _, err := repo.ConsumeArenaPass(ctx, mustConsumeRequest(t, intruderID, arena, now)); !errors.Is(err, application.ErrArenaAlreadyConsumed) {
		t.Fatalf("cross-account error = %v, want ErrArenaAlreadyConsumed", err)
	}

	if got := mustLotRemaining(t, ctx, q, intruderLot.Lot.ID().String()); got != 1 {
		t.Fatalf("intruder lot remaining = %d, want 1 untouched", got)
	}
	if got := countPassConsumptions(t, ctx, pool, intruder.ID); got != 0 {
		t.Fatalf("intruder consumptions = %d, want 0", got)
	}
}

func TestRepository_PassSummaryExcludesExpiredLotsAtBoundaries(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	now := time.Now().UTC()
	acc := mustBillingAccount(t, ctx, q, "summary-owner@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	expiredEnd := now.Add(-time.Hour)
	if _, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginMember, 2, "member:expired", &expiredEnd)); err != nil {
		t.Fatalf("expired grant: %v", err)
	}
	boundaryEnd := now
	if _, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginMember, 1, "member:boundary", &boundaryEnd)); err != nil {
		t.Fatalf("boundary grant: %v", err)
	}
	soonEnd := now.Add(time.Hour)
	if _, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginMember, 1, "member:soon", &soonEnd)); err != nil {
		t.Fatalf("soon grant: %v", err)
	}
	if _, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginPurchase, 3, "stripe:evt_summary", nil)); err != nil {
		t.Fatalf("purchase grant: %v", err)
	}
	consumedLot, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginPurchase, 1, "stripe:evt_summary_consumed", nil))
	if err != nil {
		t.Fatalf("consumed grant: %v", err)
	}
	if _, err := repo.ConsumeArenaPass(ctx, mustConsumeRequest(t, accountID, mustArenaID(t, 500), now)); err != nil {
		t.Fatalf("consume: %v", err)
	}
	_ = consumedLot

	useCase := application.NewGetArenaPassSummaryUseCase(repo, fixedClock{now: now})
	summary, err := useCase.Execute(ctx, accountID)
	if err != nil {
		t.Fatalf("summary error = %v", err)
	}
	if summary.AvailableTotal != 4 {
		t.Fatalf("AvailableTotal = %d, want 4 (1 expiring soon + 3 purchased)", summary.AvailableTotal)
	}
	if len(summary.Lots) != 5 {
		t.Fatalf("breakdown entries = %d, want 5 (expired lots stay in history)", len(summary.Lots))
	}

	flags := map[string]bool{}
	counted := map[string]bool{}
	for _, entry := range summary.Lots {
		flags[entry.Lot.Reference().String()] = entry.Expired
	}
	if !flags["member:expired"] {
		t.Error("expired lot must be flagged")
	}
	if !flags["member:boundary"] {
		t.Error("the expiration instant itself must already be expired")
	}
	if flags["member:soon"] || flags["stripe:evt_summary"] || flags["stripe:evt_summary_consumed"] {
		t.Error("non-expired lots must not be flagged")
	}
	for _, entry := range summary.Lots {
		counted[entry.Lot.Reference().String()] = !entry.Expired
	}
	if !counted["member:soon"] || !counted["stripe:evt_summary"] || counted["member:expired"] || counted["member:boundary"] {
		t.Errorf("available counting is wrong: %+v", counted)
	}
}

func TestRepository_ExpireJobIsIdempotentAndNeverMutatesLots(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	now := time.Now().UTC()
	acc := mustBillingAccount(t, ctx, q, "expiry-job@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	older := now.Add(-48 * time.Hour)
	if _, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginMember, 5, "member:expired-old", &older)); err != nil {
		t.Fatalf("older grant: %v", err)
	}
	recent := now.Add(-time.Hour)
	if _, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginMember, 1, "member:expired-recent", &recent)); err != nil {
		t.Fatalf("recent grant: %v", err)
	}
	// A fully consumed expired lot holds nothing and must not be reported.
	consumed, err := q.CreateArenaPassLot(ctx, platformpg.CreateArenaPassLotParams{
		AccountID:         acc.ID,
		Origin:            "MEMBER",
		Quantity:          1,
		RemainingQuantity: 0,
		ExpiresAt:         pgtype.Timestamptz{Time: older, Valid: true},
		Reference:         "member:expired-consumed",
	})
	if err != nil {
		t.Fatalf("consumed grant: %v", err)
	}
	// A valid lot is never part of the expiry report.
	future := now.Add(24 * time.Hour)
	if _, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginMember, 1, "member:future", &future)); err != nil {
		t.Fatalf("future grant: %v", err)
	}

	var beforeCount, beforeRemaining int
	if err := pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(sum(remaining_quantity), 0)::int
		FROM app.arena_pass_lots WHERE account_id = $1`, acc.ID).Scan(&beforeCount, &beforeRemaining); err != nil {
		t.Fatalf("snapshot before: %v", err)
	}

	job := application.NewExpireArenaPassLotsUseCase(repo, fixedClock{now: now})
	first, err := job.Execute(ctx)
	if err != nil {
		t.Fatalf("sweep error = %v", err)
	}
	if first.ExpiredPasses != 6 {
		t.Fatalf("ExpiredPasses = %d, want 6 (5 older + 1 recent)", first.ExpiredPasses)
	}
	references := map[string]int32{}
	for _, lot := range first.ExpiredLots {
		references[lot.Reference().String()] = lot.Remaining()
	}
	if references["member:expired-old"] != 5 || references["member:expired-recent"] != 1 {
		t.Fatalf("expired report = %+v", references)
	}
	if _, ok := references["member:expired-consumed"]; ok {
		t.Error("a fully consumed expired lot holds no passes and must not be reported")
	}
	if _, ok := references["member:future"]; ok {
		t.Error("a valid lot must never be reported as expired")
	}

	// Repeated sweep: same report, no writes anywhere.
	second, err := job.Execute(ctx)
	if err != nil {
		t.Fatalf("second sweep error = %v", err)
	}
	if second.ExpiredPasses != first.ExpiredPasses || len(second.ExpiredLots) != len(first.ExpiredLots) {
		t.Fatalf("repeated sweep diverged: %+v vs %+v", second, first)
	}

	var afterCount, afterRemaining int
	if err := pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(sum(remaining_quantity), 0)::int
		FROM app.arena_pass_lots WHERE account_id = $1`, acc.ID).Scan(&afterCount, &afterRemaining); err != nil {
		t.Fatalf("snapshot after: %v", err)
	}
	if afterCount != beforeCount || afterRemaining != beforeRemaining {
		t.Fatalf("sweep mutated lots: %d/%d -> %d/%d", beforeCount, beforeRemaining, afterCount, afterRemaining)
	}

	stored, err := q.GetArenaPassLot(ctx, consumed.ID)
	if err != nil {
		t.Fatalf("reload consumed lot: %v", err)
	}
	if stored.RemainingQuantity != 0 {
		t.Fatalf("consumed lot remaining = %d, want 0", stored.RemainingQuantity)
	}
}

func TestRepository_ConsumeArenaPassUseCaseEndToEnd(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	now := time.Now().UTC()
	acc := mustBillingAccount(t, ctx, q, "consume-use-case@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	periodEnd := now.Add(24 * time.Hour)
	if _, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginMember, 1, "member:2026-09", &periodEnd)); err != nil {
		t.Fatalf("grant: %v", err)
	}

	useCase := application.NewConsumeArenaPassUseCase(repo, fixedClock{now: now})
	command := application.ConsumeArenaPassCommand{AccountID: accountID.String(), ArenaID: mustArenaID(t, 400).String()}

	first, err := useCase.Execute(ctx, command)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if first.Replayed || first.Remaining != 0 {
		t.Fatalf("first consumption = %+v", first)
	}

	retry, err := useCase.Execute(ctx, command)
	if err != nil {
		t.Fatalf("retry Execute() error = %v", err)
	}
	if !retry.Replayed || retry.Lot.ID() != first.Lot.ID() {
		t.Fatalf("retry = %+v, want the original consumption", retry)
	}

	if _, err := useCase.Execute(ctx, application.ConsumeArenaPassCommand{
		AccountID: accountID.String(),
		ArenaID:   mustArenaID(t, 401).String(),
	}); !errors.Is(err, domain.ErrNoPassAvailable) {
		t.Fatalf("exhausted account error = %v, want ErrNoPassAvailable", err)
	}
}

// TestRepository_ConsumeArenaPassJoinsCallerTransaction is the P07-T05
// integration probe: the consumption joins the shared transaction, so a
// failed publication rolls the consumed pass back and a committed
// publication persists it.
func TestRepository_ConsumeArenaPassJoinsCallerTransaction(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	q := platformpg.New(pool)

	now := time.Now().UTC()
	acc := mustBillingAccount(t, ctx, q, "shared-tx@arena.example.com")
	accountID := domain.AccountID(uuidString(acc.ID))

	lot, err := repo.GrantPassLot(ctx, mustGrantRequest(t, accountID, domain.OriginPurchase, 1, "stripe:evt_shared_tx", nil))
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	arena := mustArenaID(t, 600)
	request := mustConsumeRequest(t, accountID, arena, now)

	manager := platformpg.NewTxManager(pool)
	publicationErr := errors.New("arena publication failed")

	// Rollback: the pass is consumed inside the shared transaction and the
	// publication fails afterwards; nothing may persist.
	err = manager.WithinTransaction(ctx, func(txCtx context.Context) error {
		if _, err := repo.ConsumeArenaPass(txCtx, request); err != nil {
			return err
		}
		return publicationErr
	})
	if !errors.Is(err, publicationErr) {
		t.Fatalf("WithinTransaction(rollback) error = %v, want the publication failure", err)
	}
	if got := mustLotRemaining(t, ctx, q, lot.Lot.ID().String()); got != 1 {
		t.Fatalf("lot remaining after rollback = %d, want 1 untouched", got)
	}
	if got := countPassConsumptions(t, ctx, pool, acc.ID); got != 0 {
		t.Fatalf("consumptions after rollback = %d, want 0", got)
	}

	// Commit: the same consumption inside a successful publication persists.
	if err := manager.WithinTransaction(ctx, func(txCtx context.Context) error {
		_, err := repo.ConsumeArenaPass(txCtx, request)
		return err
	}); err != nil {
		t.Fatalf("WithinTransaction(commit) error = %v", err)
	}
	if got := mustLotRemaining(t, ctx, q, lot.Lot.ID().String()); got != 0 {
		t.Fatalf("lot remaining after commit = %d, want 0", got)
	}
	if got := countPassConsumptions(t, ctx, pool, acc.ID); got != 1 {
		t.Fatalf("consumptions after commit = %d, want 1", got)
	}

	// A replay resolves the original consumption even inside a caller
	// transaction.
	if err := manager.WithinTransaction(ctx, func(txCtx context.Context) error {
		result, err := repo.ConsumeArenaPass(txCtx, request)
		if err != nil {
			return err
		}
		if !result.Replayed {
			return errors.New("expected a replayed consumption")
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTransaction(replay) error = %v", err)
	}
}
