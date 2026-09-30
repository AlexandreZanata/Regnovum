package postgres_test

// P25-T04 — atomicidade das transações críticas com fault injection.
//
// Cada fluxo Q0 abaixo executa a operação real (casos de uso e adapters
// reais sobre PostgreSQL descartável) com uma falha controlada armada no
// port TxManager: FailBeforeCommit prova rollback total (nenhum estado
// parcial), FailAfterCommit prova que a incerteza pós-commit se resolve
// por idempotência (replay seguro) ou reconciliação (leitura determinística
// sem duplicação possível). Um banco dedicado por fluxo isola as fases:
// before roda primeiro e prova a limpeza antes do after prosseguir.
//
// Cobertura: wallet+argumentos (débito atômico), arenas+billing+entitlements
// (consumo de passe), posições (cadeia+projeção), moderação (grant+audit) e
// jobs (retry+audit).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	arenasbilling "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/billingpass"
	arenaspg "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/postgres"
	arenasapp "github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	argumentspg "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/postgres"
	argumentswallet "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/walletdebit"
	argumentsapp "github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	argumentsdomain "github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	auditpg "github.com/AlexandreZanata/Regnovum/internal/audit/adapters/postgres"
	billingpg "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/postgres"
	billingapp "github.com/AlexandreZanata/Regnovum/internal/billing/application"
	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	jobsaudit "github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/auditbridge"
	jobspg "github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/postgres"
	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	modaudit "github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/auditbridge"
	moderationpg "github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/postgres"
	moderationapp "github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	moderationdomain "github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
	positionspg "github.com/AlexandreZanata/Regnovum/internal/positions/adapters/postgres"
	positionsapp "github.com/AlexandreZanata/Regnovum/internal/positions/application"
	positionsdomain "github.com/AlexandreZanata/Regnovum/internal/positions/domain"
	walletpg "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// txAtomicClock is one fixed instant shared by funding and operations.
type txAtomicClock struct{ now time.Time }

func (c txAtomicClock) Now() time.Time { return c.now }

// txAtomicArgumentsAccounts is a test-controlled eligibility gate for
// argument publication: only registered accounts may act. Atomicity, not
// eligibility, is under test (P24 proved the gates); the stub keeps the
// focus on the transaction. Positions needs its own type because Go has no
// overloading: the two modules declare EnsureEligible on distinct AccountID
// types.
type txAtomicArgumentsAccounts struct{ active map[string]bool }

func (g *txAtomicArgumentsAccounts) EnsureEligible(_ context.Context, accountID argumentsdomain.AccountID) error {
	if !g.active[accountID.String()] {
		return errors.New("t04 stub: account not eligible")
	}
	return nil
}

type txAtomicPositionsAccounts struct{ active map[string]bool }

func (g *txAtomicPositionsAccounts) EnsureEligible(_ context.Context, accountID positionsdomain.AccountID) error {
	if !g.active[accountID.String()] {
		return errors.New("t04 stub: account not eligible")
	}
	return nil
}

// txAtomicArenasForArguments always accepts the seeded published arena.
type txAtomicArenasForArguments struct{}

func (txAtomicArenasForArguments) EnsureAcceptsArguments(_ context.Context, _ argumentsdomain.ArenaID) error {
	return nil
}

// txAtomicArenasForPositions always accepts the seeded published arena.
type txAtomicArenasForPositions struct{}

func (txAtomicArenasForPositions) EnsureAcceptsPositions(_ context.Context, _ positionsdomain.ArenaID) error {
	return nil
}

// txAtomicDirectory resolves any address to a pre-registered account: the
// grant flow, not the directory, is under test.
type txAtomicDirectory struct{ accountID moderationdomain.AccountID }

func (d *txAtomicDirectory) LookupByEmail(_ context.Context, _ string) (*moderationapp.AdministrationTarget, error) {
	return &moderationapp.AdministrationTarget{
		AccountID:             d.accountID,
		EmailVerified:         true,
		SecondFactorConfirmed: true,
		CanAuthenticate:       true,
	}, nil
}

// txAtomicSeedAccount inserts a minimal account and returns its identifier.
func txAtomicSeedAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, email, status string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ($1, $2) RETURNING id::text`, email, status).Scan(&id); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return id
}

func txAtomicCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		t.Fatalf("count state: %v", err)
	}
	return count
}

func assertInjectedFault(t *testing.T, context string, err error) {
	t.Helper()
	if !errors.Is(err, platformpg.ErrInjectedTxFault) {
		t.Fatalf("%s: error = %v, want the injected fault", context, err)
	}
}

func TestTxAtomicArgumentPublication(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := db.Pool.Pool()
	clock := txAtomicClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	now := clock.Now()

	argumentsRepo := argumentspg.NewRepository(pool)
	walletRepo := walletpg.NewRepository(pool)
	arenasRepo := arenaspg.NewRepository(pool)
	accounts := &txAtomicArgumentsAccounts{active: map[string]bool{}}
	uow := platformpg.NewTxManager(pool)
	publish := argumentsapp.NewPublishArgumentUseCase(
		argumentsRepo, accounts, txAtomicArenasForArguments{},
		argumentswallet.New(walletapp.NewDebitInkUseCase(walletRepo, clock)),
		uow, text.GraphemeCount, argumentsdomain.DefaultReplyPolicy(), clock)

	author := txAtomicSeedAccount(t, ctx, pool, "t04-arg@arena.example.com", "active")
	accounts.active[author] = true
	arena, err := arenasRepo.CreateArena(ctx, mustTxArenaRequest(t, author, "A arena t04 debate a tese com clareza"))
	if err != nil {
		t.Fatalf("seed draft: %v", err)
	}
	slug, err := arenasdomain.ParseSlug("t04-atomic-arena")
	if err != nil {
		t.Fatalf("ParseSlug: %v", err)
	}
	if _, err := arenasRepo.PublishArenaDraft(ctx, arena.ID(), arenasdomain.CreatorID(author), slug, now, arena.Version()); err != nil {
		t.Fatalf("seed publication: %v", err)
	}
	fundTxAtomicWallet(t, ctx, walletRepo, clock, author, 100000)

	command := argumentsapp.PublishArgumentCommand{
		AccountID: author, ArenaID: arena.ID().String(), Relation: "support",
		Content:        "Afirmação t04 para atomicidade transacional",
		IdempotencyKey: "t04-arg-1",
	}
	countArguments := func() int {
		return txAtomicCount(t, ctx, pool, `SELECT count(*) FROM app.arguments WHERE author_id = $1::uuid`, author)
	}
	walletSpent := func() int64 {
		balance, err := walletRepo.DerivedBalance(ctx, walletdomain.AccountID(author))
		if err != nil {
			t.Fatalf("derived balance: %v", err)
		}
		return 100000 - balance.Purchased.Int64()
	}

	_, err = publish.Execute(platformpg.WithTxFault(ctx, platformpg.TxFaultBeforeCommit), command)
	assertInjectedFault(t, "fail before commit", err)
	if got := countArguments(); got != 0 {
		t.Fatalf("arguments after refused commit = %d, want 0 (total rollback)", got)
	}
	if got := walletSpent(); got != 0 {
		t.Fatalf("wallet spent after refused commit = %d, want 0 (total rollback)", got)
	}

	_, err = publish.Execute(platformpg.WithTxFault(ctx, platformpg.TxFaultAfterCommit), command)
	assertInjectedFault(t, "fail after commit", err)
	if got := countArguments(); got != 1 {
		t.Fatalf("arguments after uncertain commit = %d, want exactly 1", got)
	}
	cost := walletSpent()
	if cost <= 0 {
		t.Fatalf("wallet spent after uncertain commit = %d, want the single publication charge", cost)
	}

	result, err := publish.Execute(ctx, command)
	if err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	if !result.Replayed {
		t.Fatalf("replay of the uncertain publish was executed again instead of resolving the first attempt")
	}
	if got := countArguments(); got != 1 {
		t.Fatalf("arguments after replay = %d, want exactly 1", got)
	}
	if got := walletSpent(); got != cost {
		t.Fatalf("wallet spent after replay = %d, want %d (no double debit)", got, cost)
	}
}

func mustTxArenaRequest(t *testing.T, creator, statement string) arenasapp.CreateArenaRequest {
	t.Helper()
	policy := arenasdomain.DefaultStatementPolicy()
	parsedStatement, err := arenasdomain.ParseStatement(statement, policy)
	if err != nil {
		t.Fatalf("ParseStatement: %v", err)
	}
	parsedContext, err := arenasdomain.ParseContext("Contexto t04", policy)
	if err != nil {
		t.Fatalf("ParseContext: %v", err)
	}
	category, err := arenasdomain.ParseCategory("technology")
	if err != nil {
		t.Fatalf("ParseCategory: %v", err)
	}
	language, err := arenasdomain.ParseLanguage("pt-BR")
	if err != nil {
		t.Fatalf("ParseLanguage: %v", err)
	}
	return arenasapp.CreateArenaRequest{
		CreatorID: arenasdomain.CreatorID(creator), Statement: parsedStatement,
		Context: parsedContext, Category: category, Language: language,
	}
}

func fundTxAtomicWallet(t *testing.T, ctx context.Context, wallet *walletpg.Repository, clock txAtomicClock, author string, amount int64) {
	t.Helper()
	key, err := walletdomain.ParseIdempotencyKey("t04-fund-" + author[:8])
	if err != nil {
		t.Fatalf("ParseIdempotencyKey: %v", err)
	}
	ref, err := walletdomain.ParseReference("model:fund:t04")
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	if _, err := wallet.ApplyCredit(ctx, walletapp.CreditRequest{
		AccountID: walletdomain.AccountID(author), Bucket: walletdomain.BucketPurchased,
		OperationType: walletdomain.OperationCreditPurchase, IdempotencyKey: key,
		Reference: ref, Delta: amount, ChangedAt: clock.Now(),
	}); err != nil {
		t.Fatalf("fund wallet: %v", err)
	}
}

func TestTxAtomicArenaPublication(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := db.Pool.Pool()
	clock := txAtomicClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}

	arenasRepo := arenaspg.NewRepository(pool)
	billingRepo := billingpg.NewRepository(pool)
	uow := platformpg.NewTxManager(pool)
	publish := arenasapp.NewPublishArenaUseCase(arenasRepo, arenasbilling.New(billingRepo, clock), uow, clock)

	creator := txAtomicSeedAccount(t, ctx, pool, "t04-arenas@arena.example.com", "active")
	reference, err := billingdomain.ParseReference("t04:grant:arena")
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	quantity, err := billingdomain.NewQuantity(1)
	if err != nil {
		t.Fatalf("NewQuantity: %v", err)
	}
	if _, err := billingRepo.GrantPassLot(ctx, billingapp.GrantPassLotRequest{
		AccountID: billingdomain.AccountID(creator), Origin: billingdomain.OriginAdmin,
		Quantity: quantity, Reference: reference, GrantedAt: clock.Now(),
	}); err != nil {
		t.Fatalf("grant pass lot: %v", err)
	}
	draft, err := arenasRepo.CreateArena(ctx, mustTxArenaRequest(t, creator, "A arena t04 debate a tese com clareza"))
	if err != nil {
		t.Fatalf("seed draft: %v", err)
	}
	command := arenasapp.PublishArenaCommand{AccountID: creator, ArenaID: draft.ID().String()}

	published := func() int {
		return txAtomicCount(t, ctx, pool, `SELECT count(*) FROM app.arenas WHERE creator_id = $1::uuid AND status = 'published'`, creator)
	}
	consumed := func() int {
		return txAtomicCount(t, ctx, pool, `SELECT count(*) FROM app.arena_pass_consumptions`)
	}

	_, err = publish.Execute(platformpg.WithTxFault(ctx, platformpg.TxFaultBeforeCommit), command)
	assertInjectedFault(t, "fail before commit", err)
	if got := published(); got != 0 {
		t.Fatalf("published arenas after refused commit = %d, want 0", got)
	}
	if got := consumed(); got != 0 {
		t.Fatalf("consumed passes after refused commit = %d, want 0", got)
	}

	_, err = publish.Execute(platformpg.WithTxFault(ctx, platformpg.TxFaultAfterCommit), command)
	assertInjectedFault(t, "fail after commit", err)
	if got := published(); got != 1 {
		t.Fatalf("published arenas after uncertain commit = %d, want exactly 1", got)
	}
	if got := consumed(); got != 1 {
		t.Fatalf("consumed passes after uncertain commit = %d, want exactly 1", got)
	}

	// Publication has no blind replay: publishing the same draft again is
	// refused by state. The uncertainty resolves by reconciliation — the
	// stored state is deterministic and admits no second consumption.
	stored, err := arenasRepo.GetArenaByID(ctx, draft.ID())
	if err != nil {
		t.Fatalf("reconcile published arena: %v", err)
	}
	if string(stored.Status()) != "published" {
		t.Fatalf("reconciled status = %q, want published", stored.Status())
	}
	if got := consumed(); got != 1 {
		t.Fatalf("consumed passes after reconciliation = %d, want exactly 1", got)
	}
}

func TestTxAtomicPositionChange(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := db.Pool.Pool()
	clock := txAtomicClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}

	positionsRepo := positionspg.NewRepository(pool)
	accounts := &txAtomicPositionsAccounts{active: map[string]bool{}}
	uow := platformpg.NewTxManager(pool)
	confirm := positionsapp.NewConfirmInitialPositionUseCase(positionsRepo, accounts, txAtomicArenasForPositions{}, clock)
	change := positionsapp.NewChangePositionUseCase(positionsRepo, txAtomicArenasForPositions{}, uow, clock)

	account := txAtomicSeedAccount(t, ctx, pool, "t04-pos@arena.example.com", "active")
	accounts.active[account] = true
	arena := parityArenaDraft(t, ctx, pool, account, "Afirmação t04 para posições")
	if _, err := confirm.Execute(ctx, positionsapp.ConfirmInitialPositionCommand{
		AccountID: account, ArenaID: arena, Position: "agree",
	}); err != nil {
		t.Fatalf("seed confirmation: %v", err)
	}
	command := positionsapp.ChangePositionCommand{AccountID: account, ArenaID: arena, Position: "disagree"}
	changes := func() int {
		return txAtomicCount(t, ctx, pool, `SELECT count(*) FROM app.position_changes WHERE arena_id = $1::uuid AND account_id = $2::uuid`, arena, account)
	}
	current := func() string {
		stored, err := positionsRepo.GetByAccountAndArena(ctx,
			mustTxPositionsArenaID(t, arena), mustTxPositionsAccountID(t, account))
		if err != nil {
			t.Fatalf("read projection: %v", err)
		}
		return stored.CurrentPosition().String()
	}

	_, err := change.Execute(platformpg.WithTxFault(ctx, platformpg.TxFaultBeforeCommit), command)
	assertInjectedFault(t, "fail before commit", err)
	if got := changes(); got != 0 {
		t.Fatalf("changes after refused commit = %d, want 0", got)
	}
	if got := current(); got != "agree" {
		t.Fatalf("projection after refused commit = %q, want agree", got)
	}

	_, err = change.Execute(platformpg.WithTxFault(ctx, platformpg.TxFaultAfterCommit), command)
	assertInjectedFault(t, "fail after commit", err)
	if got := changes(); got != 1 {
		t.Fatalf("changes after uncertain commit = %d, want exactly 1", got)
	}
	if got := current(); got != "disagree" {
		t.Fatalf("projection after uncertain commit = %q, want disagree", got)
	}

	// Repeating the same change is refused without writing: the projection
	// already holds the target, so the uncertainty resolves by refusal plus
	// a deterministic read — never a second row.
	_, err = change.Execute(ctx, command)
	if err == nil {
		t.Fatalf("repeated change was accepted, want a refusal without writes")
	}
	if got := changes(); got != 1 {
		t.Fatalf("changes after repeated change = %d, want exactly 1", got)
	}
	if got := current(); got != "disagree" {
		t.Fatalf("projection after repeated change = %q, want disagree", got)
	}
}

func mustTxPositionsArenaID(t *testing.T, id string) positionsdomain.ArenaID {
	t.Helper()
	arena, err := positionsdomain.ParseArenaID(id)
	if err != nil {
		t.Fatalf("ParseArenaID: %v", err)
	}
	return arena
}

func mustTxPositionsAccountID(t *testing.T, id string) positionsdomain.AccountID {
	t.Helper()
	account, err := positionsdomain.ParseAccountID(id)
	if err != nil {
		t.Fatalf("ParseAccountID: %v", err)
	}
	return account
}

func TestTxAtomicFirstAdministratorGrant(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := db.Pool.Pool()
	clock := txAtomicClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}

	account := txAtomicSeedAccount(t, ctx, pool, "t04-admin@arena.example.com", "active")
	roles := moderationpg.NewRepository(pool)
	audit, err := modaudit.NewRecorder(auditpg.NewRepository(pool))
	if err != nil {
		t.Fatalf("audit recorder: %v", err)
	}
	uow := platformpg.NewTxManager(pool)
	grant, err := moderationapp.NewGrantFirstAdministratorUseCase(
		&txAtomicDirectory{accountID: moderationdomain.AccountID(account)},
		roles, roles, audit, clock, uow,
	)
	if err != nil {
		t.Fatalf("grant use case: %v", err)
	}
	command := moderationapp.GrantFirstAdministratorCommand{Email: "t04-admin@arena.example.com"}
	assignments := func() int {
		return txAtomicCount(t, ctx, pool, `SELECT count(*) FROM app.admin_roles`)
	}
	trail := func() int {
		return txAtomicCount(t, ctx, pool, `SELECT count(*) FROM app.audit_events`)
	}

	_, err = grant.Execute(platformpg.WithTxFault(ctx, platformpg.TxFaultBeforeCommit), command)
	assertInjectedFault(t, "fail before commit", err)
	if got := assignments(); got != 0 {
		t.Fatalf("assignments after refused commit = %d, want 0", got)
	}
	if got := trail(); got != 0 {
		t.Fatalf("audit events after refused commit = %d, want 0", got)
	}

	_, err = grant.Execute(platformpg.WithTxFault(ctx, platformpg.TxFaultAfterCommit), command)
	assertInjectedFault(t, "fail after commit", err)
	if got := assignments(); got != 1 {
		t.Fatalf("assignments after uncertain commit = %d, want exactly 1", got)
	}
	if got := trail(); got != 1 {
		t.Fatalf("audit events after uncertain commit = %d, want exactly 1", got)
	}

	// A second grant is refused: an administrator already exists, so the
	// uncertainty resolves without a second row anywhere.
	_, err = grant.Execute(ctx, command)
	if !errors.Is(err, moderationapp.ErrAdministratorAlreadyExists) {
		t.Fatalf("second grant = %v, want ErrAdministratorAlreadyExists", err)
	}
	if got := assignments(); got != 1 {
		t.Fatalf("assignments after second grant = %d, want exactly 1", got)
	}
	if got := trail(); got != 1 {
		t.Fatalf("audit events after second grant = %d, want exactly 1", got)
	}
}

func TestTxAtomicDeadJobRetry(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := db.Pool.Pool()
	clock := txAtomicClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	now := clock.Now()

	repository := jobspg.NewRepository(pool)
	audit, err := jobsaudit.NewRecorder(auditpg.NewRepository(pool))
	if err != nil {
		t.Fatalf("audit recorder: %v", err)
	}
	uow := platformpg.NewTxManager(pool)
	retry, err := jobsapp.NewRetryJobUseCase(repository, repository, audit, uow, clock)
	if err != nil {
		t.Fatalf("retry use case: %v", err)
	}
	operator := txAtomicSeedAccount(t, ctx, pool, "t04-operator@arena.example.com", "active")
	var jobID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.jobs (type, version, parameters, state, available_at, attempts, max_attempts, created_at, updated_at)
		VALUES ('email_delivery', 1, '{}', 'dead', $1, 1, 1, $1, $1)
		RETURNING id::text`, now).Scan(&jobID); err != nil {
		t.Fatalf("seed dead job: %v", err)
	}
	command := jobsapp.RetryJobCommand{
		Actor: operator, SessionAge: 5 * time.Minute, JobID: jobID, Reason: "t04 fault-injection probe",
	}
	state := func() string {
		job, err := repository.JobByID(ctx, jobID)
		if err != nil {
			t.Fatalf("read job: %v", err)
		}
		return string(job.State)
	}
	trail := func() int {
		return txAtomicCount(t, ctx, pool, `SELECT count(*) FROM app.audit_events`)
	}

	_, err = retry.Execute(platformpg.WithTxFault(ctx, platformpg.TxFaultBeforeCommit), command)
	assertInjectedFault(t, "fail before commit", err)
	if got := state(); got != string(jobsdomain.StateDead) {
		t.Fatalf("job state after refused commit = %q, want dead", got)
	}
	if got := trail(); got != 0 {
		t.Fatalf("audit events after refused commit = %d, want 0", got)
	}

	_, err = retry.Execute(platformpg.WithTxFault(ctx, platformpg.TxFaultAfterCommit), command)
	assertInjectedFault(t, "fail after commit", err)
	if got := state(); got != string(jobsdomain.StateQueued) {
		t.Fatalf("job state after uncertain commit = %q, want queued", got)
	}
	if got := trail(); got != 1 {
		t.Fatalf("audit events after uncertain commit = %d, want exactly 1", got)
	}

	// The job is no longer dead, so the same command is refused without
	// writing: no second requeue, no second audit fact.
	_, err = retry.Execute(ctx, command)
	if !errors.Is(err, jobsdomain.ErrJobNotDead) {
		t.Fatalf("second retry = %v, want ErrJobNotDead", err)
	}
	if got := state(); got != string(jobsdomain.StateQueued) {
		t.Fatalf("job state after second retry = %q, want queued", got)
	}
	if got := trail(); got != 1 {
		t.Fatalf("audit events after second retry = %d, want exactly 1", got)
	}
}
