package jobs_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	identityjobs "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/jobs"
	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
)

// countingPass records how many times the sweep ran and what it answered.
type countingPass struct {
	calls   int64
	revoked int64
	err     error
}

func (p *countingPass) Execute(context.Context) (int64, error) {
	atomic.AddInt64(&p.calls, 1)
	return p.revoked, p.err
} // scheduledJob is the row the scheduler queues for one period: the payload is
// the period, and the handler deliberately does not read it.
func scheduledJob(t *testing.T, jobType jobsdomain.JobType, version int) *jobsdomain.Job {
	t.Helper()
	return &jobsdomain.Job{
		ID:      "0191f0e0-0000-7000-8000-0000000000ff",
		Type:    jobType,
		Version: version,
		Payload: []byte(`{"period":"2026-09-18"}`),
		State:   jobsdomain.StateLeased,
	}
}

func TestCleanupHandlerRegistersTheScheduledWorkload(t *testing.T) {
	pass := &countingPass{}
	handler, err := identityjobs.NewCleanupHandler(pass)
	if err != nil {
		t.Fatalf("NewCleanupHandler: %v", err)
	}

	registry := jobsapp.NewHandlerMap()
	if err := handler.Register(registry); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// The workload and version the scheduler queues (P15-T05) must resolve to
	// this handler, or every period of it dies as an unknown type.
	resolved, err := registry.Resolve(jobsdomain.TypeSessionCleanup, identityjobs.CleanupPayloadVersion)
	if err != nil {
		t.Fatalf("Resolve(%s v%d): %v", jobsdomain.TypeSessionCleanup, identityjobs.CleanupPayloadVersion, err)
	}
	if resolved == nil {
		t.Fatal("the registered handler is nil")
	}
	if handler.Type() != jobsdomain.TypeSessionCleanup || handler.Version() != identityjobs.CleanupPayloadVersion {
		t.Fatalf("handler = (%s, v%d), want (%s, v%d)",
			handler.Type(), handler.Version(), jobsdomain.TypeSessionCleanup, identityjobs.CleanupPayloadVersion)
	}
}

func TestCleanupHandlerRunsTheSweepOnceAndReportsItsFailure(t *testing.T) {
	pass := &countingPass{revoked: 3}
	handler, err := identityjobs.NewCleanupHandler(pass)
	if err != nil {
		t.Fatalf("NewCleanupHandler: %v", err)
	}

	if err := handler.Handle(context.Background(), scheduledJob(t, jobsdomain.TypeSessionCleanup, identityjobs.CleanupPayloadVersion)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if calls := atomic.LoadInt64(&pass.calls); calls != 1 {
		t.Fatalf("sweeps = %d, want one per job", calls)
	}

	failing := &countingPass{err: errors.New("database is unreachable")}
	failingHandler, err := identityjobs.NewCleanupHandler(failing)
	if err != nil {
		t.Fatalf("NewCleanupHandler: %v", err)
	}
	if err := failingHandler.Handle(context.Background(), scheduledJob(t, jobsdomain.TypeSessionCleanup, identityjobs.CleanupPayloadVersion)); err == nil {
		t.Fatal("a failing sweep was reported as success: the worker would record the period as done")
	}
}

func TestCleanupHandlerRefusesAJobThatIsNotItsOwn(t *testing.T) {
	pass := &countingPass{}
	handler, err := identityjobs.NewCleanupHandler(pass)
	if err != nil {
		t.Fatalf("NewCleanupHandler: %v", err)
	}

	for _, foreign := range []*jobsdomain.Job{
		scheduledJob(t, jobsdomain.TypeEmailDelivery, identityjobs.CleanupPayloadVersion),
		scheduledJob(t, jobsdomain.TypeSessionCleanup, identityjobs.CleanupPayloadVersion+1),
	} {
		if err := handler.Handle(context.Background(), foreign); err == nil {
			t.Fatalf("Handle(%s v%d) = nil, want a refusal", foreign.Type, foreign.Version)
		}
	}
	if calls := atomic.LoadInt64(&pass.calls); calls != 0 {
		t.Fatalf("sweeps = %d, want none for a job that is not ours", calls)
	}
	if err := handler.Handle(context.Background(), nil); err == nil {
		t.Fatal("a nil job was accepted")
	}
}

func TestCleanupHandlerRefusesToBeBuiltWithoutAPass(t *testing.T) {
	if _, err := identityjobs.NewCleanupHandler(nil); !errors.Is(err, identityjobs.ErrMissingDependency) {
		t.Fatalf("NewCleanupHandler(nil) error = %v, want ErrMissingDependency", err)
	}
	handler, err := identityjobs.NewCleanupHandler(&countingPass{})
	if err != nil {
		t.Fatalf("NewCleanupHandler: %v", err)
	}
	if err := handler.Register(nil); !errors.Is(err, identityjobs.ErrMissingDependency) {
		t.Fatalf("Register(nil) error = %v, want ErrMissingDependency", err)
	}
}
