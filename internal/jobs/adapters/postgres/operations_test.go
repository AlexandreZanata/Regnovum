package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	jobsrepo "github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/postgres"
	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	"github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// auditStub stands in for the audit trail: it can be told to fail, which is how
// the atomicity of an operator action is measured.
type auditStub struct {
	facts int
	err   error
}

func (a *auditStub) Record(context.Context, jobsapp.AdminFact) error {
	if a.err != nil {
		return a.err
	}
	a.facts++
	return nil
}

type operationsClock struct{ now time.Time }

func (c operationsClock) Now() time.Time { return c.now }

type operationsHarness struct {
	repo  *jobsrepo.Repository
	pool  *pgxpool.Pool
	audit *auditStub
	now   time.Time
	jobID string
}

// seedDeadJob stores one dead row with an explicit lifecycle instant, so the
// ages the operational view reports are measured against the injected clock and
// not against the wall clock of the machine running the test.
func seedDeadJob(t *testing.T, pool *pgxpool.Pool, state string, now time.Time) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"period": "2026-09"})
	if err != nil {
		t.Fatalf("encode parameters: %v", err)
	}
	// A lease is all-or-nothing in the schema, so a leased fixture carries one
	// that is still live: the point of these rows is the state, not the lease.
	var owner pgtype.Text
	var leasedUntil pgtype.Timestamptz
	if state == "leased" {
		owner = pgtype.Text{String: "seed-holder", Valid: true}
		leasedUntil = pgtype.Timestamptz{Time: now.Add(time.Hour), Valid: true}
	}
	var id pgtype.UUID
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO app.jobs (type, version, parameters, state, available_at, attempts, max_attempts,
		                      lease_owner, leased_until, last_error_code, last_error_detail,
		                      created_at, updated_at)
		VALUES ('retention_run', 1, $1::jsonb, $2, $3, 5, 5, $4, $5,
		        'JOB_HANDLER_ERROR', 'provider unavailable', $3, $3)
		RETURNING id`,
		string(body), state, now.Add(-3*time.Hour), owner, leasedUntil).Scan(&id); err != nil {
		t.Fatalf("seed %s job: %v", state, err)
	}
	return uuidFromPg(id)
}

func newOperationsHarness(t *testing.T, state string) *operationsHarness {
	t.Helper()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	return &operationsHarness{
		repo:  jobsrepo.NewRepository(pool),
		pool:  pool,
		audit: &auditStub{},
		now:   now,
		jobID: seedDeadJob(t, pool, state, now),
	}
}

func (h *operationsHarness) clock() operationsClock { return operationsClock{now: h.now} }

func (h *operationsHarness) retryUseCase(t *testing.T) *jobsapp.RetryJobUseCase {
	t.Helper()
	retry, err := jobsapp.NewRetryJobUseCase(h.repo, h.repo, h.audit, platformpg.NewTxManager(h.pool), h.clock())
	if err != nil {
		t.Fatalf("NewRetryJobUseCase() error = %v", err)
	}
	return retry
}

func (h *operationsHarness) jobState(t *testing.T) (state string, attempts int) {
	t.Helper()
	if err := h.pool.QueryRow(context.Background(),
		`SELECT state, attempts FROM app.jobs WHERE id = $1::uuid`, h.jobID).Scan(&state, &attempts); err != nil {
		t.Fatalf("read job: %v", err)
	}
	return state, attempts
}

func retryCommand(jobID string) jobsapp.RetryJobCommand {
	return jobsapp.RetryJobCommand{
		Actor:      "0191f0e0-0000-7000-8000-0000000000aa",
		SessionAge: time.Minute,
		JobID:      jobID,
		Reason:     "cause fixed",
	}
}

// TestRetryAndAuditCommitTogether is the atomicity of an operator action: when
// the trail cannot accept the fact, the requeue rolls back with it, so an action
// that would leave no trace cannot have happened.
func TestRetryAndAuditCommitTogether(t *testing.T) {
	built := newOperationsHarness(t, "dead")
	failure := errors.New("audit: unavailable")
	built.audit.err = failure
	if _, err := built.retryUseCase(t).Execute(context.Background(), retryCommand(built.jobID)); !errors.Is(err, failure) {
		t.Fatalf("Execute() error = %v, want the audit failure", err)
	}
	state, attempts := built.jobState(t)
	if state != "dead" {
		t.Errorf("state = %q, want the requeue rolled back", state)
	}
	if attempts != 5 {
		t.Errorf("attempts = %d, want the budget untouched by the rollback", attempts)
	}

	// With the trail healthy, the same call moves the row, renews the budget
	// and records exactly one fact.
	built.audit.err = nil
	result, err := built.retryUseCase(t).Execute(context.Background(), retryCommand(built.jobID))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.State != domain.StateQueued {
		t.Errorf("state = %q, want queued", result.State)
	}
	state, attempts = built.jobState(t)
	if state != "queued" || attempts != 0 {
		t.Errorf("state/attempts = %s/%d, want queued/0", state, attempts)
	}
	if built.audit.facts != 1 {
		t.Errorf("audit facts = %d, want one", built.audit.facts)
	}
}

// TestRetryRefusesAJobThatIsNotDeadAtTheDatabase keeps the state guard where it
// is authoritative: the statement, not the earlier read.
func TestRetryRefusesAJobThatIsNotDeadAtTheDatabase(t *testing.T) {
	for _, state := range []string{"queued", "leased", "succeeded"} {
		t.Run(state, func(t *testing.T) {
			built := newOperationsHarness(t, state)
			if _, err := built.retryUseCase(t).Execute(context.Background(), retryCommand(built.jobID)); !errors.Is(err, domain.ErrJobNotDead) {
				t.Errorf("Execute() error = %v, want ErrJobNotDead", err)
			}
			if built.audit.facts != 0 {
				t.Error("a refused retry was recorded")
			}
			after, _ := built.jobState(t)
			if after != state {
				t.Errorf("state = %q, want it untouched", after)
			}
		})
	}
}

// TestHealthMeasuresTheQueue is the read surface against real rows.
func TestHealthMeasuresTheQueue(t *testing.T) {
	built := newOperationsHarness(t, "dead")
	health, err := built.repo.QueueHealth(context.Background(), built.now)
	if err != nil {
		t.Fatalf("QueueHealth() error = %v", err)
	}
	if health.Dead != 1 {
		t.Errorf("dead = %d, want the seeded row", health.Dead)
	}
	if health.OldestDeadSeconds < 3*3600 {
		t.Errorf("oldest dead = %d, want at least the age of the seeded row", health.OldestDeadSeconds)
	}
	if health.LagSeconds != 0 {
		t.Errorf("lag = %d, want no wait with nothing due", health.LagSeconds)
	}

	// A due job that nobody claimed is the lag.
	if _, err := built.pool.Exec(context.Background(), `
		INSERT INTO app.jobs (type, version, parameters, state, available_at, created_at, updated_at)
		VALUES ('session_cleanup', 1, '{"period":"2026-09-18"}'::jsonb, 'queued', $1, $1, $1)`,
		built.now.Add(-45*time.Minute)); err != nil {
		t.Fatalf("seed due job: %v", err)
	}
	health, err = built.repo.QueueHealth(context.Background(), built.now)
	if err != nil {
		t.Fatalf("QueueHealth() error = %v", err)
	}
	if health.Queued != 1 || health.DueNow != 1 {
		t.Errorf("queue = %+v, want one due job", health)
	}
	if health.LagSeconds < 45*60 || health.LagSeconds > 46*60 {
		t.Errorf("lag = %d, want the 45 minutes the job has waited", health.LagSeconds)
	}
}

func TestDeadListingIsOrderedAndAged(t *testing.T) {
	built := newOperationsHarness(t, "dead")
	if _, err := built.pool.Exec(context.Background(), `
		INSERT INTO app.jobs (type, version, parameters, state, available_at, attempts, max_attempts,
		                      last_error_code, last_error_detail, created_at, updated_at)
		VALUES ('pass_expiry', 1, '{"period":"2026-09-01"}'::jsonb, 'dead', $1, 5, 5,
		        'JOB_HANDLER_ERROR', 'provider unavailable', $1, $1)`,
		built.now.Add(-time.Minute)); err != nil {
		t.Fatalf("seed newer dead job: %v", err)
	}
	items, err := built.repo.ListDeadJobs(context.Background(), 10, built.now)
	if err != nil {
		t.Fatalf("ListDeadJobs() error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want both dead rows", len(items))
	}
	if items[0].ID != built.jobID {
		t.Errorf("first item = %s, want the oldest dead job first", items[0].ID)
	}
	if items[0].AgeSeconds < items[1].AgeSeconds {
		t.Errorf("ages = %d and %d, want the oldest first", items[0].AgeSeconds, items[1].AgeSeconds)
	}
	if !items[0].Retryable || items[0].Type != domain.TypeRetentionRun {
		t.Errorf("item = %+v, want the allowlist decision", items[0])
	}

	// A page smaller than the queue returns exactly the page.
	items, err = built.repo.ListDeadJobs(context.Background(), 1, built.now)
	if err != nil {
		t.Fatalf("ListDeadJobs(1) error = %v", err)
	}
	if len(items) != 1 {
		t.Errorf("items = %d, want the requested page", len(items))
	}
}

func TestSessionAgeIsMeasuredFromTheStoredSession(t *testing.T) {
	built := newOperationsHarness(t, "dead")
	var accountID pgtype.UUID
	if err := built.pool.QueryRow(context.Background(), `
		INSERT INTO app.accounts (email, status) VALUES ('jobs-ops@example.com', 'active')
		RETURNING id`).Scan(&accountID); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	var sessionID pgtype.UUID
	if err := built.pool.QueryRow(context.Background(), `
		INSERT INTO app.sessions (account_id, token_hash, created_at, expires_at)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		accountID, []byte("jobs-ops-session-hash"),
		built.now.Add(-30*time.Minute), built.now.Add(time.Hour)).Scan(&sessionID); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	age, err := built.repo.SessionAgeAt(context.Background(), uuidFromPg(sessionID), built.now)
	if err != nil {
		t.Fatalf("SessionAgeAt() error = %v", err)
	}
	if age < 29*time.Minute || age > 31*time.Minute {
		t.Errorf("age = %s, want about thirty minutes", age)
	}
	if _, err := built.repo.SessionAgeAt(context.Background(), "not-a-uuid", built.now); !errors.Is(err, jobsapp.ErrUnknownSession) {
		t.Errorf("SessionAgeAt(malformed) error = %v, want ErrUnknownSession", err)
	}
	if _, err := built.repo.SessionAgeAt(context.Background(), "0191f0e0-0000-7000-8000-0000000000ff", built.now); !errors.Is(err, jobsapp.ErrUnknownSession) {
		t.Errorf("SessionAgeAt(unknown) error = %v, want ErrUnknownSession", err)
	}
}

func uuidFromPg(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return id.String()
}
