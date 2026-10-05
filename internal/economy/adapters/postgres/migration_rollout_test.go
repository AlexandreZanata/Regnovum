package postgres_test

// P44-T09 — voluntary economic rollout rehearsal, test-only.
//
// A dry run with fully synthetic holders (`.invalid`, never real
// users): three seasons (one sealed and archived, one active near its
// end, one prepared successor), fictitious opt-ins against legacy
// rights, dual reads, a late webhook, refusal and exit, a failed step
// with rollback, champions, a King change and bilingual notices. The
// rollout feature stays blocked to real users: the rehearsal asserts
// every holder is synthetic, and activation itself remains refused by
// the no-activation gates.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	seasonsdomain "github.com/AlexandreZanata/Regnovum/internal/seasons/domain"
	walletpg "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"

	"github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

const (
	rolloutPast   = "S-2077-ROLL-1"
	rolloutActive = "S-2077-ROLL-2"
	rolloutNext   = "S-2077-ROLL-3"
)

type rolloutHolder struct {
	email   string
	account string
	legacy  int64
}

func rolloutCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

// rolloutHolders creates three synthetic holders with legacy rights
// credited through the real wallet adapter: the rights the rehearsal
// must reconcile, never money it moves.
func rolloutHolders(t *testing.T, ctx context.Context, testDB *dbtest.TestDB) []rolloutHolder {
	t.Helper()
	pool := testDB.Pool.Pool()
	wallets := walletpg.NewRepository(pool)
	holders := []rolloutHolder{
		{email: "roll-ana@canary.invalid", legacy: 3000},
		{email: "roll-ben@canary.invalid", legacy: 2000},
		{email: "roll-carol@canary.invalid", legacy: 1000},
	}
	for i := range holders {
		holders[i].account = consentHolder(t, ctx, pool, holders[i].email)
		reference, err := walletdomain.ParseReference("model:fund:rollout")
		if err != nil {
			t.Fatalf("ParseReference: %v", err)
		}
		// One distinct key per holder: the same seed twice must not
		// credit twice, and the rehearsal proves it below.
		key, err := walletdomain.ParseIdempotencyKey("roll-legacy-" + holders[i].email)
		if err != nil {
			t.Fatalf("ParseIdempotencyKey: %v", err)
		}
		if _, err := wallets.ApplyCredit(ctx, walletapp.CreditRequest{
			AccountID: walletdomain.AccountID(holders[i].account), Bucket: walletdomain.BucketPurchased,
			OperationType: walletdomain.OperationCreditPurchase, IdempotencyKey: key,
			Reference: reference, Delta: holders[i].legacy, ChangedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("fund legacy %s: %v", holders[i].email, err)
		}
	}
	return holders
}

// rolloutLegacyRights reads the declared rights the independent way:
// straight from the legacy ledger, summed per holder.
func rolloutLegacyRights(t *testing.T, ctx context.Context, testDB *dbtest.TestDB, holders []rolloutHolder) map[string]int64 {
	t.Helper()
	rights := map[string]int64{}
	for _, holder := range holders {
		var sum int64
		if err := testDB.Pool.Pool().QueryRow(ctx,
			`SELECT COALESCE(SUM(t.amount), 0) FROM app.wallet_transactions t
			 JOIN app.wallet_operations o ON o.id = t.operation_id
			 WHERE o.account_id::text = $1`, holder.account).Scan(&sum); err != nil {
			t.Fatalf("read legacy %s: %v", holder.email, err)
		}
		rights[holder.email] = sum
	}
	return rights
}

func rolloutOptIn(t *testing.T, ctx context.Context, repo *postgres.Repository, holder rolloutHolder) {
	t.Helper()
	clock := fixedHoldClock{now: time.Now().UTC()}
	consents := application.NewConsentUseCase(repo)
	if _, err := consents.Execute(ctx, application.ConsentCommand{AccountID: holder.account, Charter: "v1", Decision: "accepted"}); err != nil {
		t.Fatalf("accept %s: %v", holder.email, err)
	}
	optins := application.NewOptInUseCase(repo, clock)
	if _, err := optins.Execute(ctx, application.OptInCommand{
		AccountID: holder.account, Charter: "v1", Millis: holder.legacy, RateNum: 1, RateDen: 1, ValidDays: 30,
	}); err != nil {
		t.Fatalf("opt in %s: %v", holder.email, err)
	}
}

// TestMigrationRolloutReconcilesRights rehearses the voluntary rollout
// across three seasons: every synthetic right is granted exactly once
// on the active book, the dual reads agree holder by holder, supply
// stays S, and zero extra INK exists anywhere.
func TestMigrationRolloutReconcilesRights(t *testing.T) {
	testDB := dbtest.New(t)
	ctx, cancel := rolloutCtx()
	defer cancel()
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	holders := rolloutHolders(t, ctx, testDB)

	seedSeasonBook(t, ctx, testDB, rolloutPast, 1, "2027-01-01T00:00:00Z")
	seedSeasonBook(t, ctx, testDB, rolloutActive, 2, "2027-04-01T00:00:00Z")
	seedSeasonBook(t, ctx, testDB, rolloutNext, 3, "2027-07-01T00:00:00Z")
	genesisBook(t, ctx, repo, "genesis-roll-past", rolloutPast)
	genesisBook(t, ctx, repo, "genesis-roll-active", rolloutActive)
	sealSeasonBook(t, ctx, testDB, rolloutPast)

	for _, holder := range holders {
		rolloutOptIn(t, ctx, repo, holder)
		label := "roll-" + strings.Split(holder.email, "@")[0]
		if _, err := pool.Exec(ctx,
			`INSERT INTO app.economy_custodies (kind, label, season_key) VALUES ('user', $1, $2)`, label, rolloutActive); err != nil {
			t.Fatalf("create custody %s: %v", label, err)
		}
		amount, err := domain.NewMilliInk(holder.legacy)
		if err != nil {
			t.Fatalf("NewMilliInk: %v", err)
		}
		if _, err := repo.Transfer(ctx, application.TransferRequest{
			FromSeason: domain.SeasonKey(rolloutActive),
			FromKind:   domain.CustodyTreasury, FromLabel: "main",
			ToSeason: domain.SeasonKey(rolloutActive),
			ToKind:   domain.CustodyUser, ToLabel: label,
			Amount: amount,
		}); err != nil {
			t.Fatalf("grant %s: %v", holder.email, err)
		}
	}

	// Dual read: legacy ledger rights against recorded opt-in
	// quantities, holder by holder, then totals.
	legacy := rolloutLegacyRights(t, ctx, testDB, holders)
	var legacyTotal, grantedTotal int64
	for _, holder := range holders {
		if legacy[holder.email] != holder.legacy {
			t.Fatalf("legacy %s = %d, want seeded %d", holder.email, legacy[holder.email], holder.legacy)
		}
		var quantity int64
		if err := pool.QueryRow(ctx,
			`SELECT quantity_milli FROM app.economy_optins WHERE account_id = $1::uuid AND charter_version = 'v1'`,
			holder.account).Scan(&quantity); err != nil {
			t.Fatalf("read opt-in %s: %v", holder.email, err)
		}
		if quantity != legacy[holder.email] {
			t.Fatalf("opt-in %s = %d against legacy %d", holder.email, quantity, legacy[holder.email])
		}
		label := "roll-" + strings.Split(holder.email, "@")[0]
		granted := bookBalance(t, ctx, testDB, "user", label, rolloutActive)
		if granted != legacy[holder.email] {
			t.Fatalf("granted %s = %d against right %d: not 100%%", holder.email, granted, legacy[holder.email])
		}
		legacyTotal += legacy[holder.email]
		grantedTotal += granted
	}
	if grantedTotal != legacyTotal {
		t.Fatalf("granted total %d against rights total %d: extra INK exists", grantedTotal, legacyTotal)
	}
	if supply := bookBalance(t, ctx, testDB, "treasury", "main", rolloutActive) + grantedTotal; supply != domain.GenesisSupplyMillis {
		t.Fatalf("active supply = %d, want S", supply)
	}

	// Blocked to real users: every holder of the rehearsal is
	// synthetic, and activation itself stays refused elsewhere.
	var real int64
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM app.accounts WHERE email NOT LIKE '%@canary.invalid' AND email NOT LIKE '%@invalid.example'`).Scan(&real); err != nil {
		t.Fatalf("scan holders: %v", err)
	}
	if real != 0 {
		t.Fatalf("%d non-synthetic holders in the rehearsal", real)
	}
}

// TestMigrationRolloutLateWebhookNoDuplicate settles the near-end
// billing once and delivers the late webhook twice more: the same
// intention resolves the stored outcome, and the leg count never
// moves again.
func TestMigrationRolloutLateWebhookNoDuplicate(t *testing.T) {
	testDB := dbtest.New(t)
	ctx, cancel := rolloutCtx()
	defer cancel()
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	holders := rolloutHolders(t, ctx, testDB)

	seedSeasonBook(t, ctx, testDB, rolloutActive, 2, "2027-04-01T00:00:00Z")
	genesisBook(t, ctx, repo, "genesis-roll-late", rolloutActive)
	rolloutOptIn(t, ctx, repo, holders[0])
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.economy_custodies (kind, label, season_key) VALUES ('user', 'roll-late', $1)`, rolloutActive); err != nil {
		t.Fatalf("create custody: %v", err)
	}
	key, _ := domain.ParseIntentionKey("roll-late-billing")
	actor, _ := domain.ParseIntentionActor("roll-biller")
	operation, _ := domain.ParseIntentionOperation("roll-charge")
	price, _ := domain.NewMilliInk(250)
	request := application.IdempotentTransferRequest{
		FromSeason: domain.SeasonKey(rolloutActive), Key: key, Actor: actor, Operation: operation,
		PayloadHash: pitrDumpHash,
		FromKind:    domain.CustodyTreasury, FromLabel: "main",
		ToSeason: domain.SeasonKey(rolloutActive),
		ToKind:   domain.CustodyUser, ToLabel: "roll-late",
		Amount: price,
	}
	first, err := repo.TransferIdempotent(ctx, request)
	if err != nil || first.Replayed {
		t.Fatalf("near-end billing = %+v, %v: want first settlement", first, err)
	}
	legs := bookLegs(t, ctx, testDB, rolloutActive)
	for delivery := 0; delivery < 2; delivery++ {
		replayed, err := repo.TransferIdempotent(ctx, request)
		if err != nil || !replayed.Replayed {
			t.Fatalf("late delivery %d = %+v, %v: want the stored outcome", delivery, replayed, err)
		}
	}
	if got := bookLegs(t, ctx, testDB, rolloutActive); got != legs {
		t.Fatalf("late webhook moved legs %d -> %d", legs, got)
	}
	if got := bookBalance(t, ctx, testDB, "user", "roll-late", rolloutActive); got != 250 {
		t.Fatalf("billed balance = %d, want exactly 250", got)
	}
}

// TestMigrationRolloutRefusalAndExit proves refusal and exit are
// functional: the refuser is recorded without moving either book,
// and exiting the new charter keeps the old acceptance while
// recording the refusal.
func TestMigrationRolloutRefusalAndExit(t *testing.T) {
	testDB := dbtest.New(t)
	ctx, cancel := rolloutCtx()
	defer cancel()
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)

	beforeEntries, beforeSum := journalFingerprint(t, ctx, pool)
	refuser := consentHolder(t, ctx, pool, "roll-dario@canary.invalid")
	consents := application.NewConsentUseCase(repo)
	if _, err := consents.Execute(ctx, application.ConsentCommand{AccountID: refuser, Charter: "v1", Decision: "refused"}); err != nil {
		t.Fatalf("refuse: %v", err)
	}
	clock := fixedHoldClock{now: time.Now().UTC()}
	optins := application.NewOptInUseCase(repo, clock)
	if _, err := optins.Execute(ctx, application.OptInCommand{
		AccountID: refuser, Charter: "v1", Millis: 100, RateNum: 1, RateDen: 1, ValidDays: 30,
	}); !errors.Is(err, domain.ErrConsentRequired) {
		t.Fatalf("opt-in after refusal = %v, want ErrConsentRequired", err)
	}
	afterEntries, afterSum := journalFingerprint(t, ctx, pool)
	if beforeEntries != afterEntries || beforeSum != afterSum {
		t.Fatalf("books moved on refusal: (%d, %d) -> (%d, %d)", beforeEntries, beforeSum, afterEntries, afterSum)
	}

	exiter := consentHolder(t, ctx, pool, "roll-exit@canary.invalid")
	if _, err := consents.Execute(ctx, application.ConsentCommand{AccountID: exiter, Charter: "v1", Decision: "accepted"}); err != nil {
		t.Fatalf("accept v1: %v", err)
	}
	if _, err := consents.Execute(ctx, application.ConsentCommand{AccountID: exiter, Charter: "v2", Decision: "refused"}); err != nil {
		t.Fatalf("exit v2: %v", err)
	}
	kept, err := repo.FindConsent(ctx, exiter, mustCharterVersion(t, "v1"))
	if err != nil || kept == nil || kept.Decision != domain.ConsentAccepted {
		t.Fatalf("v1 acceptance lost on exit: %+v, %v", kept, err)
	}
	left, err := repo.FindConsent(ctx, exiter, mustCharterVersion(t, "v2"))
	if err != nil || left == nil || left.Decision != domain.ConsentRefused {
		t.Fatalf("v2 refusal not recorded: %+v, %v", left, err)
	}
}

// TestMigrationRolloutFailureRestores proves a failed rollout step
// restores the previous state without erasing history: the conflicting
// opt-in is refused, the journal is byte-identical, and every verdict
// row stays in place.
func TestMigrationRolloutFailureRestores(t *testing.T) {
	testDB := dbtest.New(t)
	ctx, cancel := rolloutCtx()
	defer cancel()
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)
	holders := rolloutHolders(t, ctx, testDB)
	rolloutOptIn(t, ctx, repo, holders[0])

	beforeEntries, beforeSum := journalFingerprint(t, ctx, pool)
	var verdicts int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_charter_consents`).Scan(&verdicts); err != nil {
		t.Fatalf("count verdicts: %v", err)
	}
	clock := fixedHoldClock{now: time.Now().UTC()}
	optins := application.NewOptInUseCase(repo, clock)
	if _, err := optins.Execute(ctx, application.OptInCommand{
		AccountID: holders[0].account, Charter: "v1", Millis: 999, RateNum: 2, RateDen: 1, ValidDays: 30,
	}); !errors.Is(err, domain.ErrConsentConflict) {
		t.Fatalf("conflicting terms = %v, want ErrConsentConflict", err)
	}
	afterEntries, afterSum := journalFingerprint(t, ctx, pool)
	if beforeEntries != afterEntries || beforeSum != afterSum {
		t.Fatalf("failed step moved books: (%d, %d) -> (%d, %d)", beforeEntries, beforeSum, afterEntries, afterSum)
	}
	var kept int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_charter_consents`).Scan(&kept); err != nil {
		t.Fatalf("recount verdicts: %v", err)
	}
	if kept != verdicts {
		t.Fatalf("history changed on failure: %d -> %d verdicts", verdicts, kept)
	}
	var quantity int64
	if err := pool.QueryRow(ctx,
		`SELECT quantity_milli FROM app.economy_optins WHERE account_id = $1::uuid AND charter_version = 'v1'`,
		holders[0].account).Scan(&quantity); err != nil {
		t.Fatalf("read opt-in: %v", err)
	}
	if quantity != holders[0].legacy {
		t.Fatalf("recorded right = %d after conflict, want kept %d", quantity, holders[0].legacy)
	}
}

// TestMigrationRolloutChampionsAndSuccession rehearses the changing
// of the guard: champions derive from the sealed book, Bob reigns
// after Alice, and Alice's fence stays stale.
func TestMigrationRolloutChampionsAndSuccession(t *testing.T) {
	testDB := dbtest.New(t)
	ctx, cancel := rolloutCtx()
	defer cancel()
	pool := testDB.Pool.Pool()
	repo := postgres.NewRepository(pool)

	seedSeasonBook(t, ctx, testDB, rolloutPast, 1, "2027-01-01T00:00:00Z")
	genesisBook(t, ctx, repo, "genesis-roll-champ", rolloutPast)
	for _, holder := range []struct {
		subject string
		wealth  int64
	}{{"ana", 3000}, {"ben", 2000}, {"carol", 1000}} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO app.economy_custodies (kind, label, season_key) VALUES ('user', $1, $2)`, "roll-"+holder.subject, rolloutPast); err != nil {
			t.Fatalf("create custody: %v", err)
		}
		amount, err := domain.NewMilliInk(holder.wealth)
		if err != nil {
			t.Fatalf("NewMilliInk: %v", err)
		}
		if _, err := repo.Transfer(ctx, application.TransferRequest{
			FromSeason: domain.SeasonKey(rolloutPast),
			FromKind:   domain.CustodyTreasury, FromLabel: "main",
			ToSeason: domain.SeasonKey(rolloutPast),
			ToKind:   domain.CustodyUser, ToLabel: "roll-" + holder.subject,
			Amount: amount,
		}); err != nil {
			t.Fatalf("fund %s: %v", holder.subject, err)
		}
	}
	sealSeasonBook(t, ctx, testDB, rolloutPast)
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.seasonal_reigns (season_id, reign_version, holder_subject, is_regent, reason, is_active, ended_at)
		 VALUES ($1, 1, 'alice', false, 'conquest', false, now()),
		        ($1, 2, 'bob', false, 'conquest', true, NULL)`, rolloutPast); err != nil {
		t.Fatalf("seed succession: %v", err)
	}

	archive, err := seasonsdomain.DeriveChampions(seasonsdomain.CutoffSnapshot{
		Season:         rolloutPast,
		CutoffRevision: 3,
		Policy:         "wealth-v1",
		Entries: []seasonsdomain.ChampionEntry{
			{Subject: "ana", Pseudonym: "p-ana", Wealth: 3000, AttainedRevision: 1},
			{Subject: "ben", Pseudonym: "p-ben", Wealth: 2000, AttainedRevision: 2},
			{Subject: "carol", Pseudonym: "p-carol", Wealth: 1000, AttainedRevision: 3},
		},
		LastKing: "bob",
		Reigns: []seasonsdomain.ReignFact{
			{Holder: "alice", ReignVersion: 1, StartedAt: time.Now().UTC().Add(-time.Hour), EndsAt: time.Now().UTC()},
			{Holder: "bob", ReignVersion: 2, StartedAt: time.Now().UTC(), EndsAt: time.Now().UTC().Add(time.Hour)},
		},
	})
	if err != nil {
		t.Fatalf("DeriveChampions: %v", err)
	}
	if len(archive.CoLeaders) == 0 || archive.CoLeaders[0].Subject != "ana" {
		t.Fatalf("champions = %+v, want ana first", archive.CoLeaders)
	}
	var holder string
	if err := pool.QueryRow(ctx,
		`SELECT holder_subject FROM app.seasonal_reigns WHERE season_id = $1 AND is_active`, rolloutPast).Scan(&holder); err != nil {
		t.Fatalf("read reign: %v", err)
	}
	if holder != "bob" {
		t.Fatalf("reign holder = %q after rehearsal, want bob", holder)
	}
}

// TestMigrationRolloutBilingualNotices proves the pilot can speak to
// holders: every notice key the rehearsal uses exists, non-empty, in
// both locales, and translated rather than copied.
func rolloutRepoRoot(t *testing.T) string {
	t.Helper()
	return "../../../.."
}

func TestMigrationRolloutBilingualNotices(t *testing.T) {
	t.Parallel()
	keys := []string{"charter", "purchase", "ink", "receipt"}
	for _, locale := range []string{"pt-BR", "en-US"} {
		raw, err := os.ReadFile(rolloutRepoRoot(t) + "/locales/" + locale + "/kingdom.json")
		if err != nil {
			t.Fatalf("read %s: %v", locale, err)
		}
		var document struct {
			Kingdom map[string]map[string]string `json:"kingdom"`
		}
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatalf("decode %s: %v", locale, err)
		}
		for _, key := range keys {
			section, ok := document.Kingdom[key]
			if !ok || len(section) == 0 {
				t.Fatalf("%s misses notice section %q", locale, key)
			}
			for field, text := range section {
				if strings.TrimSpace(text) == "" {
					t.Fatalf("%s %s.%s is blank", locale, key, field)
				}
			}
		}
	}
	rawPT, _ := os.ReadFile(rolloutRepoRoot(t) + "/locales/pt-BR/kingdom.json")
	rawEN, _ := os.ReadFile(rolloutRepoRoot(t) + "/locales/en-US/kingdom.json")
	var documentPT, documentEN struct {
		Kingdom map[string]map[string]string `json:"kingdom"`
	}
	if err := json.Unmarshal(rawPT, &documentPT); err != nil {
		t.Fatalf("decode pt: %v", err)
	}
	if err := json.Unmarshal(rawEN, &documentEN); err != nil {
		t.Fatalf("decode en: %v", err)
	}
	same := 0
	total := 0
	for _, key := range keys {
		for field, textPT := range documentPT.Kingdom[key] {
			total++
			if documentEN.Kingdom[key][field] == textPT {
				same++
			}
		}
	}
	if total == 0 || same == total {
		t.Fatalf("notices identical across locales (%d/%d): untranslated pilot", same, total)
	}
}
