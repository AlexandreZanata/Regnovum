package application_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/application"
	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

// fakeWebhookVerifier is a stub that accepts or rejects payloads based on the
// configured result.
type fakeWebhookVerifier struct {
	verifyResult error
}

func (f *fakeWebhookVerifier) Verify(_ []byte, _ string, _ string) error {
	return f.verifyResult
}

// fakeWebhookEventRepository is an in-memory implementation of the webhook
// event repository for unit testing.
type fakeWebhookEventRepository struct {
	mu     sync.Mutex
	events map[string]*application.WebhookEventRecord
}

func newFakeWebhookEventRepository() *fakeWebhookEventRepository {
	return &fakeWebhookEventRepository{
		events: make(map[string]*application.WebhookEventRecord),
	}
}

func (f *fakeWebhookEventRepository) ClaimEvent(_ context.Context, request application.ClaimWebhookEventRequest) (*application.ClaimWebhookEventResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	eventID := request.EventID.String()
	if existing, ok := f.events[eventID]; ok {
		// Replay: resolve the existing row.
		return &application.ClaimWebhookEventResult{
			Record:   *existing,
			Replayed: true,
		}, nil
	}

	// New event: insert it.
	now := time.Now().UTC()
	record := &application.WebhookEventRecord{
		ID:              fmt.Sprintf("local-%d", len(f.events)+1),
		EventID:         request.EventID,
		EventType:       request.EventType,
		Livemode:        request.Livemode,
		StripeCreatedAt: request.StripeCreatedAt,
		PayloadSHA256:   request.PayloadSHA256,
		PayloadBytes:    request.PayloadBytes,
		Status:          application.WebhookEventProcessing,
		Attempts:        1,
		ReceivedAt:      now,
	}
	f.events[eventID] = record
	return &application.ClaimWebhookEventResult{
		Record:   *record,
		Replayed: false,
	}, nil
}

func (f *fakeWebhookEventRepository) MarkProcessed(_ context.Context, eventID domain.WebhookEventID) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if record, ok := f.events[eventID.String()]; ok {
		record.Status = application.WebhookEventProcessed
		now := time.Now().UTC()
		record.ProcessedAt = &now
		return nil
	}
	return fmt.Errorf("event %s not found", eventID)
}

func (f *fakeWebhookEventRepository) MarkFailed(_ context.Context, eventID domain.WebhookEventID, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if record, ok := f.events[eventID.String()]; ok {
		record.Status = application.WebhookEventFailed
		record.LastError = reason
		return nil
	}
	return fmt.Errorf("event %s not found", eventID)
}

func (f *fakeWebhookEventRepository) MarkIgnored(_ context.Context, eventID domain.WebhookEventID) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if record, ok := f.events[eventID.String()]; ok {
		record.Status = application.WebhookEventIgnored
		now := time.Now().UTC()
		record.ProcessedAt = &now
		return nil
	}
	return fmt.Errorf("event %s not found", eventID)
}

// testCheckoutSessionBody builds a minimal checkout.session.completed webhook
// body with the given payment status.
func testCheckoutSessionBody(paymentStatus string) []byte {
	return []byte(fmt.Sprintf(`{
		"id": "evt_test123",
		"type": "checkout.session.completed",
		"livemode": false,
		"created": 1234567890,
		"data": {
			"object": {
				"id": "cs_test_session1",
				"status": "complete",
				"payment_status": "%s"
			}
		}
	}`, paymentStatus))
}

// testCheckoutSessionExpiredBody builds a checkout.session.expired webhook body.
func testCheckoutSessionExpiredBody() []byte {
	return []byte(`{
		"id": "evt_testexpired",
		"type": "checkout.session.expired",
		"livemode": false,
		"created": 1234567891,
		"data": {
			"object": {
				"id": "cs_test_expired1",
				"status": "expired",
				"payment_status": "unpaid"
			}
		}
	}`)
}

// testSubscriptionBody builds a customer.subscription.updated webhook body.
func testSubscriptionBody() []byte {
	return []byte(`{
		"id": "evt_testsub",
		"type": "customer.subscription.updated",
		"livemode": false,
		"created": 1234567892,
		"data": {
			"object": {
				"status": "active"
			}
		}
	}`)
}

// testUnknownEventBody builds a webhook body with an unknown event type.
func testUnknownEventBody() []byte {
	return []byte(`{
		"id": "evt_testunknown",
		"type": "invoice.payment_failed",
		"livemode": false,
		"created": 1234567893,
		"data": {
			"object": {}
		}
	}`)
}

func TestProcessWebhookRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	repo := newFakeWebhookEventRepository()
	useCase, err := application.NewProcessWebhookUseCase(application.ProcessWebhookDependencies{
		Verifier: &fakeWebhookVerifier{verifyResult: nil},
		Events:   repo,
		Clock:    &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewProcessWebhookUseCase error = %v", err)
	}

	// Build a body larger than 1 MiB.
	oversized := make([]byte, 1<<20+1)
	for i := range oversized {
		oversized[i] = 'a'
	}

	err = useCase.Execute(context.Background(), application.ProcessWebhookCommand{
		RawBody:         oversized,
		SignatureHeader: "t=123,v1=abc",
		TimestampHeader: "123",
	})
	if !errors.Is(err, application.ErrWebhookPayloadTooLarge) {
		t.Fatalf("error = %v, want ErrWebhookPayloadTooLarge", err)
	}
	if len(repo.events) != 0 {
		t.Fatal("an oversized body must not be persisted")
	}
}

func TestProcessWebhookRejectsInvalidSignature(t *testing.T) {
	t.Parallel()

	repo := newFakeWebhookEventRepository()
	useCase, err := application.NewProcessWebhookUseCase(application.ProcessWebhookDependencies{
		Verifier: &fakeWebhookVerifier{
			verifyResult: fmt.Errorf("%w: test reason", application.ErrWebhookSignatureInvalid),
		},
		Events: repo,
		Clock:  &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewProcessWebhookUseCase error = %v", err)
	}

	body := testCheckoutSessionBody("paid")
	err = useCase.Execute(context.Background(), application.ProcessWebhookCommand{
		RawBody:         body,
		SignatureHeader: "t=123,v1=invalid",
		TimestampHeader: "123",
	})
	if !errors.Is(err, application.ErrWebhookSignatureInvalid) {
		t.Fatalf("error = %v, want ErrWebhookSignatureInvalid", err)
	}
	if len(repo.events) != 0 {
		t.Fatal("an invalid signature must not be persisted")
	}
}

func TestProcessWebhookIsIdempotentOnReplay(t *testing.T) {
	t.Parallel()

	repo := newFakeWebhookEventRepository()
	useCase, err := application.NewProcessWebhookUseCase(application.ProcessWebhookDependencies{
		Verifier: &fakeWebhookVerifier{verifyResult: nil},
		Events:   repo,
		Clock:    &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewProcessWebhookUseCase error = %v", err)
	}

	body := testCheckoutSessionBody("paid")
	cmd := application.ProcessWebhookCommand{
		RawBody:         body,
		SignatureHeader: "t=123,v1=valid",
		TimestampHeader: "123",
	}

	// First delivery: processes the event.
	if err := useCase.Execute(context.Background(), cmd); err != nil {
		t.Fatalf("first Execute error = %v", err)
	}
	if len(repo.events) != 1 {
		t.Fatalf("events = %d, want 1", len(repo.events))
	}

	// Second delivery: replay, no second insert.
	if err := useCase.Execute(context.Background(), cmd); err != nil {
		t.Fatalf("second Execute error = %v", err)
	}
	if len(repo.events) != 1 {
		t.Fatalf("events after replay = %d, want 1", len(repo.events))
	}

	// The event was processed exactly once.
	record := repo.events["evt_test123"]
	if record.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", record.Attempts)
	}
	if record.Status != application.WebhookEventProcessed {
		t.Errorf("status = %s, want processed", record.Status)
	}
}

func TestProcessWebhookHandlesCheckoutSessionCompleted(t *testing.T) {
	t.Parallel()

	repo := newFakeWebhookEventRepository()
	useCase, err := application.NewProcessWebhookUseCase(application.ProcessWebhookDependencies{
		Verifier: &fakeWebhookVerifier{verifyResult: nil},
		Events:   repo,
		Clock:    &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewProcessWebhookUseCase error = %v", err)
	}

	body := testCheckoutSessionBody("paid")
	if err := useCase.Execute(context.Background(), application.ProcessWebhookCommand{
		RawBody:         body,
		SignatureHeader: "t=123,v1=valid",
		TimestampHeader: "123",
	}); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	record := repo.events["evt_test123"]
	if record == nil {
		t.Fatal("event not found")
	}
	if record.Status != application.WebhookEventProcessed {
		t.Errorf("status = %s, want processed", record.Status)
	}
	if record.EventType.String() != "checkout.session.completed" {
		t.Errorf("event type = %s", record.EventType)
	}
}

func TestProcessWebhookHandlesCheckoutSessionExpired(t *testing.T) {
	t.Parallel()

	repo := newFakeWebhookEventRepository()
	useCase, err := application.NewProcessWebhookUseCase(application.ProcessWebhookDependencies{
		Verifier: &fakeWebhookVerifier{verifyResult: nil},
		Events:   repo,
		Clock:    &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewProcessWebhookUseCase error = %v", err)
	}

	body := testCheckoutSessionExpiredBody()
	if err := useCase.Execute(context.Background(), application.ProcessWebhookCommand{
		RawBody:         body,
		SignatureHeader: "t=123,v1=valid",
		TimestampHeader: "123",
	}); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	record := repo.events["evt_testexpired"]
	if record == nil {
		t.Fatal("event not found")
	}
	if record.Status != application.WebhookEventProcessed {
		t.Errorf("status = %s, want processed", record.Status)
	}
}

func TestProcessWebhookHandlesSubscriptionEvent(t *testing.T) {
	t.Parallel()

	repo := newFakeWebhookEventRepository()
	useCase, err := application.NewProcessWebhookUseCase(application.ProcessWebhookDependencies{
		Verifier: &fakeWebhookVerifier{verifyResult: nil},
		Events:   repo,
		Clock:    &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewProcessWebhookUseCase error = %v", err)
	}

	body := testSubscriptionBody()
	if err := useCase.Execute(context.Background(), application.ProcessWebhookCommand{
		RawBody:         body,
		SignatureHeader: "t=123,v1=valid",
		TimestampHeader: "123",
	}); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	record := repo.events["evt_testsub"]
	if record == nil {
		t.Fatal("event not found")
	}
	if record.Status != application.WebhookEventProcessed {
		t.Errorf("status = %s, want processed", record.Status)
	}
}

func TestProcessWebhookIgnoresUnknownEventType(t *testing.T) {
	t.Parallel()

	repo := newFakeWebhookEventRepository()
	useCase, err := application.NewProcessWebhookUseCase(application.ProcessWebhookDependencies{
		Verifier: &fakeWebhookVerifier{verifyResult: nil},
		Events:   repo,
		Clock:    &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewProcessWebhookUseCase error = %v", err)
	}

	body := testUnknownEventBody()
	if err := useCase.Execute(context.Background(), application.ProcessWebhookCommand{
		RawBody:         body,
		SignatureHeader: "t=123,v1=valid",
		TimestampHeader: "123",
	}); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	record := repo.events["evt_testunknown"]
	if record == nil {
		t.Fatal("event not found")
	}
	if record.Status != application.WebhookEventProcessed {
		t.Errorf("status = %s, want processed", record.Status)
	}
}

func TestProcessWebhookRejectsUnpaidCheckoutSession(t *testing.T) {
	t.Parallel()

	repo := newFakeWebhookEventRepository()
	useCase, err := application.NewProcessWebhookUseCase(application.ProcessWebhookDependencies{
		Verifier: &fakeWebhookVerifier{verifyResult: nil},
		Events:   repo,
		Clock:    &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewProcessWebhookUseCase error = %v", err)
	}

	// A checkout.session.completed with unpaid payment status is malformed.
	body := testCheckoutSessionBody("unpaid")
	err = useCase.Execute(context.Background(), application.ProcessWebhookCommand{
		RawBody:         body,
		SignatureHeader: "t=123,v1=valid",
		TimestampHeader: "123",
	})
	if err == nil {
		t.Fatal("an unpaid checkout session must be rejected")
	}
	// The event should be marked as failed.
	record := repo.events["evt_test123"]
	if record == nil {
		t.Fatal("event not found")
	}
	if record.Status != application.WebhookEventFailed {
		t.Errorf("status = %s, want failed", record.Status)
	}
}

func TestProcessWebhookRequiresCoherentConfiguration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*application.ProcessWebhookDependencies)
	}{
		{name: "no verifier", mutate: func(d *application.ProcessWebhookDependencies) { d.Verifier = nil }},
		{name: "no events", mutate: func(d *application.ProcessWebhookDependencies) { d.Events = nil }},
		{name: "no clock", mutate: func(d *application.ProcessWebhookDependencies) { d.Clock = nil }},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			deps := application.ProcessWebhookDependencies{
				Verifier: &fakeWebhookVerifier{verifyResult: nil},
				Events:   newFakeWebhookEventRepository(),
				Clock:    &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
			}
			testCase.mutate(&deps)
			_, err := application.NewProcessWebhookUseCase(deps)
			if err == nil {
				t.Fatal("a refused configuration must not build a use case")
			}
		})
	}
}

func TestProcessWebhookSuccessPageNeverGrantsBenefit(t *testing.T) {
	t.Parallel()

	// This test proves the core invariant: a checkout.session.completed
	// event with an unpaid payment status is rejected. The success page of
	// a checkout session never grants benefit; only a verified webhook with
	// a settled payment status can settle an intent (THR-STRIPE-02).
	repo := newFakeWebhookEventRepository()
	useCase, err := application.NewProcessWebhookUseCase(application.ProcessWebhookDependencies{
		Verifier: &fakeWebhookVerifier{verifyResult: nil},
		Events:   repo,
		Clock:    &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewProcessWebhookUseCase error = %v", err)
	}

	// Unpaid checkout session completed: rejected.
	body := testCheckoutSessionBody("unpaid")
	err = useCase.Execute(context.Background(), application.ProcessWebhookCommand{
		RawBody:         body,
		SignatureHeader: "t=123,v1=valid",
		TimestampHeader: strconv.FormatInt(time.Now().Unix(), 10),
	})
	if err == nil {
		t.Fatal("an unpaid checkout.session.completed must be rejected")
	}

	// No payment was granted.
	record := repo.events["evt_test123"]
	if record == nil {
		t.Fatal("event not found")
	}
	if record.Status != application.WebhookEventFailed {
		t.Errorf("status = %s, want failed (no benefit granted)", record.Status)
	}
}

func TestProcessWebhookAppliesMemberEntitlements(t *testing.T) {
	t.Parallel()

	repo := newFakeWebhookEventRepository()
	catalog := testMemberCatalog(t)
	subs := newFakeSubscriptionRepository()
	customers := newFakeCustomerDirectory()
	customers.mapping["cus_user99"] = domain.AccountID("usr_account_99")
	passLots := &fakeMemberPassLots{}
	inker := &fakeMemberInker{}
	clock := &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}

	memberSettler, err := application.NewApplyMemberEntitlementsUseCase(application.MemberEntitlementsDependencies{
		Catalog:       catalog,
		Subscriptions: subs,
		Customers:     customers,
		PassLots:      passLots,
		Inker:         inker,
		Clock:         clock,
	})
	if err != nil {
		t.Fatalf("NewApplyMemberEntitlementsUseCase: %v", err)
	}

	useCase, err := application.NewProcessWebhookUseCase(application.ProcessWebhookDependencies{
		Verifier:      &fakeWebhookVerifier{verifyResult: nil},
		Events:        repo,
		MemberSettler: memberSettler,
		Clock:         clock,
	})
	if err != nil {
		t.Fatalf("NewProcessWebhookUseCase error = %v", err)
	}

	subBody := []byte(`{
		"id": "evt_subcreated1",
		"type": "customer.subscription.created",
		"livemode": false,
		"created": 1789740000,
		"data": {
			"object": {
				"id": "sub_member99",
				"customer": "cus_user99",
				"status": "active",
				"current_period_start": 1789740000,
				"current_period_end": 1792418400,
				"cancel_at_period_end": false,
				"items": {
					"data": [
						{
							"id": "si_123",
							"price": {
								"id": "price_1QbrMember"
							}
						}
					]
				}
			}
		}
	}`)

	if err := useCase.Execute(context.Background(), application.ProcessWebhookCommand{
		RawBody:         subBody,
		SignatureHeader: "t=123,v1=valid",
		TimestampHeader: strconv.FormatInt(time.Now().Unix(), 10),
	}); err != nil {
		t.Fatalf("Execute subscription webhook error = %v", err)
	}

	eventRec := repo.events["evt_subcreated1"]
	if eventRec == nil || eventRec.Status != application.WebhookEventProcessed {
		t.Fatalf("expected event processed, got %+v", eventRec)
	}

	// Verify subscription recorded
	subRec := subs.subs["sub_member99"]
	if subRec == nil {
		t.Fatal("expected subscription recorded in repository")
	}
	if subRec.Status != domain.SubscriptionActive {
		t.Errorf("subscription status = %v, want active", subRec.Status)
	}

	// Verify pass granted
	if len(passLots.grants) != 1 {
		t.Fatalf("expected 1 pass granted, got %d", len(passLots.grants))
	}
	if passLots.grants[0].Origin != domain.OriginMember || passLots.grants[0].Quantity.Int32() != 1 {
		t.Errorf("pass grant = %+v, want 1 member pass", passLots.grants[0])
	}

	// Verify INK granted: exactly 30k
	if len(inker.credits) != 1 {
		t.Fatalf("expected 1 ink credit, got %d", len(inker.credits))
	}
	if inker.credits[0].Amount != 30000 {
		t.Errorf("ink credit amount = %d, want 30000", inker.credits[0].Amount)
	}
}

func newBoundaryWebhookUseCase(t *testing.T, repo *fakeWebhookEventRepository) *application.ProcessWebhookUseCase {
	t.Helper()
	useCase, err := application.NewProcessWebhookUseCase(application.ProcessWebhookDependencies{
		Verifier: &fakeWebhookVerifier{verifyResult: nil},
		Events:   repo,
		Clock:    &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewProcessWebhookUseCase error = %v", err)
	}
	return useCase
}

func executeBoundaryWebhook(t *testing.T, useCase *application.ProcessWebhookUseCase, body string) error {
	t.Helper()
	return useCase.Execute(context.Background(), application.ProcessWebhookCommand{
		RawBody:         []byte(body),
		SignatureHeader: "t=123,v1=valid",
		TimestampHeader: "123",
	})
}

func TestProcessWebhookBoundaryBodySize(t *testing.T) {
	t.Parallel()

	// A body of exactly the maximum size is accepted; one byte more
	// refuses (mutation gate: process_webhook.go:112).
	base := `{"id":"evt_sizemax","type":"checkout.session.expired","livemode":false,"created":1234567890}`
	exact := base + strings.Repeat(" ", (1<<20)-len(base))
	if len(exact) != 1<<20 {
		t.Fatalf("padded body = %d bytes, want %d", len(exact), 1<<20)
	}
	repo := newFakeWebhookEventRepository()
	useCase := newBoundaryWebhookUseCase(t, repo)
	if err := executeBoundaryWebhook(t, useCase, exact); err != nil {
		t.Fatalf("exactly-max body rejected: %v", err)
	}
	if repo.events["evt_sizemax"].Status != application.WebhookEventProcessed {
		t.Fatal("exactly-max body must process")
	}
}

func TestProcessWebhookExtractionEdges(t *testing.T) {
	t.Parallel()

	// Keys at index zero resolve; values at the end of the body parse
	// without overrunning; an unterminated string refuses cleanly instead
	// of panicking (mutation gate: process_webhook.go:358,370,372,403,
	// 422,430,434).
	cases := []struct {
		name      string
		body      string
		processed bool
	}{
		{
			name:      "id at index zero",
			body:      `"id":"evt_edgeid01","type":"checkout.session.expired","livemode":false,"created":1234567890`,
			processed: true,
		},
		{
			name:      "livemode at index zero",
			body:      `"livemode":true,"id":"evt_edgebool","type":"checkout.session.expired","created":1234567890`,
			processed: true,
		},
		{
			name:      "created at index zero",
			body:      `"created":1234567890,"id":"evt_edgeint","type":"checkout.session.expired","livemode":false`,
			processed: true,
		},
		{
			name:      "integer at body end",
			body:      `{"id":"evt_edgeend","type":"checkout.session.expired","livemode":false,"created":1234567890`,
			processed: true,
		},
		{
			name:      "empty integer at body end",
			body:      `{"id":"evt_edgeempty","type":"checkout.session.expired","livemode":false,"created":`,
			processed: true,
		},
		{
			name:      "unterminated string refuses",
			body:      `{"id":"evt_edgeunterminated","type":"checkout.session.completed","livemode":false,"created":1234567890,"data":{"object":{"id":"cs_test_1","status":"complete","payment_status":"pai`,
			processed: false,
		},
		{
			name:      "trailing backslash refuses cleanly",
			body:      `{"id":"evt_edgebs\`,
			processed: false,
		},
		{
			name:      "leading escape refuses cleanly",
			body:      `{"id":"\"evt_edgelead","type":"checkout.session.expired","livemode":false,"created":1234567890}`,
			processed: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := newFakeWebhookEventRepository()
			useCase := newBoundaryWebhookUseCase(t, repo)
			err := executeBoundaryWebhook(t, useCase, tc.body)
			if tc.processed && err != nil {
				t.Fatalf("Execute error = %v, want processed", err)
			}
			if !tc.processed && err == nil {
				t.Fatal("Execute succeeded, want a clean refusal without panic")
			}
		})
	}
}

func TestProcessWebhookLargeLeadingPadding(t *testing.T) {
	t.Parallel()

	// Six hundred bytes of leading padding move every nested offset: the
	// nested slice must still resolve the object fields of a completed
	// checkout (mutation gate: process_webhook.go:465).
	body := strings.Repeat(" ", 600) + `{"id":"evt_pad01","type":"checkout.session.completed","livemode":false,"created":1234567890,"data":{"object":{"id":"cs_test_pad01","status":"complete","payment_status":"paid"}}}`
	repo := newFakeWebhookEventRepository()
	useCase := newBoundaryWebhookUseCase(t, repo)
	if err := executeBoundaryWebhook(t, useCase, body); err != nil {
		t.Fatalf("padded body rejected: %v", err)
	}
	if repo.events["evt_pad01"].Status != application.WebhookEventProcessed {
		t.Fatal("padded body must process")
	}
}

func TestProcessWebhookFailurePreservesOriginalError(t *testing.T) {
	t.Parallel()

	// A processing failure with a working event store returns the
	// processing error itself, not a wrapper about the mark that
	// succeeded (mutation gate: process_webhook.go:159).
	repo := newFakeWebhookEventRepository()
	useCase := newBoundaryWebhookUseCase(t, repo)
	err := executeBoundaryWebhook(t, useCase, string(testCheckoutSessionBody("unpaid")))
	if err == nil {
		t.Fatal("unpaid checkout must fail")
	}
	if !errors.Is(err, application.ErrWebhookPayloadMalformed) {
		t.Fatalf("error = %v, want the original malformed payload error", err)
	}
	if msg := err.Error(); len(msg) >= len("mark webhook event failed") && msg[:len("mark webhook event failed")] == "mark webhook event failed" {
		t.Fatalf("error = %q, must not wrap the successful mark", err)
	}
	if repo.events["evt_test123"].Status != application.WebhookEventFailed {
		t.Fatal("failed event must be marked failed for retry")
	}
}

func newMemberWebhookHarness(t *testing.T) (*fakeWebhookEventRepository, *fakeSubscriptionRepository, *application.ProcessWebhookUseCase) {
	t.Helper()
	repo := newFakeWebhookEventRepository()
	catalog := testMemberCatalog(t)
	subs := newFakeSubscriptionRepository()
	customers := newFakeCustomerDirectory()
	customers.mapping["cus_user99"] = domain.AccountID("usr_account_99")
	clock := &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
	memberSettler, err := application.NewApplyMemberEntitlementsUseCase(application.MemberEntitlementsDependencies{
		Catalog:       catalog,
		Subscriptions: subs,
		Customers:     customers,
		PassLots:      &fakeMemberPassLots{},
		Inker:         &fakeMemberInker{},
		Clock:         clock,
	})
	if err != nil {
		t.Fatalf("NewApplyMemberEntitlementsUseCase: %v", err)
	}
	useCase, err := application.NewProcessWebhookUseCase(application.ProcessWebhookDependencies{
		Verifier:      &fakeWebhookVerifier{verifyResult: nil},
		Events:        repo,
		MemberSettler: memberSettler,
		Clock:         clock,
	})
	if err != nil {
		t.Fatalf("NewProcessWebhookUseCase error = %v", err)
	}
	return repo, subs, useCase
}

func TestProcessWebhookSubscriptionPeriodBoundaries(t *testing.T) {
	t.Parallel()

	// Zero period bounds stay absent instead of becoming the epoch, and a
	// set cancellation instant is preserved (mutation gate:
	// process_webhook.go:568,573,584).
	bodies := []struct {
		name         string
		event        string
		status       string
		start        string
		end          string
		canceled     string
		wantStartNil bool
		wantEndNil   bool
		wantCanceled string
	}{
		{
			// Canceled subscriptions skip the period completeness check,
			// so zero bounds stay absent instead of becoming the epoch.
			name: "zero periods stay absent", event: "evt_subzero01",
			status: "canceled",
			start:  "0", end: "0", canceled: "0",
			wantStartNil: true, wantEndNil: true, wantCanceled: "now",
		},
		{
			name: "set cancellation preserved", event: "evt_subcancel01",
			status: "active",
			start:  "1789740000", end: "1792418400", canceled: "1789740000",
			wantStartNil: false, wantEndNil: false, wantCanceled: "1789740000",
		},
	}
	for _, tc := range bodies {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo, subs, useCase := newMemberWebhookHarness(t)
			body := `{"id":"` + tc.event + `","type":"customer.subscription.updated","livemode":false,"created":1789740000,` +
				`"data":{"object":{"id":"sub_member99","customer":"cus_user99","status":"` + tc.status + `",` +
				`"current_period_start":` + tc.start + `,"current_period_end":` + tc.end + `,"cancel_at_period_end":false,` +
				`"canceled_at":` + tc.canceled + `,"items":{"data":[{"id":"si_123","price":{"id":"price_1QbrMember"}}]}}}}`
			if err := executeBoundaryWebhook(t, useCase, body); err != nil {
				t.Fatalf("Execute error = %v", err)
			}
			rec := subs.subs["sub_member99"]
			if rec == nil {
				t.Fatal("subscription not recorded")
			}
			if (rec.CurrentPeriodStart == nil) != tc.wantStartNil {
				t.Errorf("CurrentPeriodStart nil = %v, want %v", rec.CurrentPeriodStart == nil, tc.wantStartNil)
			}
			if (rec.CurrentPeriodEnd == nil) != tc.wantEndNil {
				t.Errorf("CurrentPeriodEnd nil = %v, want %v", rec.CurrentPeriodEnd == nil, tc.wantEndNil)
			}
			switch tc.wantCanceled {
			case "":
				if rec.CanceledAt != nil {
					t.Errorf("CanceledAt = %v, want nil", rec.CanceledAt)
				}
			case "now":
				if rec.CanceledAt == nil || !rec.CanceledAt.Equal(time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)) {
					t.Errorf("CanceledAt = %v, want the clock instant", rec.CanceledAt)
				}
			default:
				if rec.CanceledAt == nil || rec.CanceledAt.Unix() != 1789740000 {
					t.Errorf("CanceledAt = %v, want 1789740000", rec.CanceledAt)
				}
			}
			_ = repo
		})
	}
}

func TestProcessWebhookSubscriptionDirectPrice(t *testing.T) {
	t.Parallel()

	// A direct "price": "price_..." resolves without the object form
	// (mutation gate: process_webhook.go:606).
	_, subs, useCase := newMemberWebhookHarness(t)
	body := `{"id":"evt_subdirect01","type":"customer.subscription.updated","livemode":false,"created":1789740000,` +
		`"data":{"object":{"id":"sub_member99","customer":"cus_user99","status":"active",` +
		`"current_period_start":1789740000,"current_period_end":1792418400,"cancel_at_period_end":false,` +
		`"items":{"data":[{"id":"si_123","price":"price_1QbrMember"}]}}}}`
	if err := executeBoundaryWebhook(t, useCase, body); err != nil {
		t.Fatalf("direct price Execute error = %v", err)
	}
	if subs.subs["sub_member99"] == nil {
		t.Fatal("direct-price subscription not recorded")
	}
}

func newRefundWebhookHarness(t *testing.T) (*fakeWebhookEventRepository, *fakeRefundJournal, *application.ProcessWebhookUseCase) {
	t.Helper()
	repo := newFakeWebhookEventRepository()
	catalog := mustRefundCatalog(t)
	intents := newFakeRefundIntents()
	intents.intents["cs_test_refundweb1"] = mustRefundIntent(t, "cs_test_refundweb1", "ink_10000")
	ledger := &fakeRefundLedger{balance: 10000}
	passes := newFakeRefundPasses()
	journal := newFakeRefundJournal()
	refundSettler := newRefundUseCase(t, catalog, intents, ledger, passes, journal)
	useCase, err := application.NewProcessWebhookUseCase(application.ProcessWebhookDependencies{
		Verifier:      &fakeWebhookVerifier{verifyResult: nil},
		Events:        repo,
		RefundSettler: refundSettler,
		Clock:         &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("NewProcessWebhookUseCase error = %v", err)
	}
	return repo, journal, useCase
}

func TestProcessWebhookRefundAmountBoundaries(t *testing.T) {
	t.Parallel()

	// An amount_refunded of one settles one minor unit without touching
	// the fallback (mutation gate: process_webhook.go:683,688).
	repo, journal, useCase := newRefundWebhookHarness(t)
	oneBody := `{"id":"evt_refundone01","type":"charge.refunded","livemode":false,"created":1789740000,` +
		`"data":{"object":{"id":"re_webhookone01","checkout_session":"cs_test_refundweb1","amount_refunded":1}}}`
	if err := executeBoundaryWebhook(t, useCase, oneBody); err != nil {
		t.Fatalf("unit refund rejected: %v", err)
	}
	trail, err := journal.ListRefundsByIntent(context.Background(), "intent-ink_10000")
	if err != nil {
		t.Fatalf("list refunds: %v", err)
	}
	if len(trail) != 1 {
		t.Fatalf("refund trail = %+v, want one row", trail)
	}
	_ = repo
}

func TestProcessWebhookPaddedNestedBodies(t *testing.T) {
	t.Parallel()

	// Leading padding moves every nested offset at once: the member and
	// refund parsers must still resolve their object fields (mutation
	// gate: process_webhook.go:523,653).
	t.Run("subscription", func(t *testing.T) {
		t.Parallel()
		_, subs, useCase := newMemberWebhookHarness(t)
		body := strings.Repeat(" ", 600) + `{"id":"evt_subpad01","type":"customer.subscription.updated","livemode":false,"created":1789740000,` +
			`"data":{"object":{"id":"sub_member99","customer":"cus_user99","status":"active",` +
			`"current_period_start":1789740000,"current_period_end":1792418400,"cancel_at_period_end":false,` +
			`"items":{"data":[{"id":"si_123","price":{"id":"price_1QbrMember"}}]}}}}`
		if err := executeBoundaryWebhook(t, useCase, body); err != nil {
			t.Fatalf("padded subscription rejected: %v", err)
		}
		if subs.subs["sub_member99"] == nil {
			t.Fatal("padded subscription not recorded")
		}
	})
	t.Run("refund", func(t *testing.T) {
		t.Parallel()
		_, journal, useCase := newRefundWebhookHarness(t)
		body := strings.Repeat(" ", 600) + `{"id":"evt_refundpad01","type":"charge.refunded","livemode":false,"created":1789740000,` +
			`"data":{"object":{"id":"re_webhookpad01","checkout_session":"cs_test_refundweb1","amount_refunded":990}}}`
		if err := executeBoundaryWebhook(t, useCase, body); err != nil {
			t.Fatalf("padded refund rejected: %v", err)
		}
		trail, err := journal.ListRefundsByIntent(context.Background(), "intent-ink_10000")
		if err != nil {
			t.Fatalf("list refunds: %v", err)
		}
		if len(trail) != 1 {
			t.Fatalf("refund trail = %+v, want one row", trail)
		}
	})
}

func TestProcessWebhookDataFirstLayoutsRefuseCleanly(t *testing.T) {
	t.Parallel()

	// A body starting with "data" whose nested object carries no shadow
	// identifier reaches the nested parsers with a zero data offset and
	// must refuse cleanly instead of panicking (mutation gate:
	// process_webhook.go:465,523,653).
	t.Run("checkout", func(t *testing.T) {
		t.Parallel()
		repo := newFakeWebhookEventRepository()
		useCase := newBoundaryWebhookUseCase(t, repo)
		body := `"data":{"object":{"status":"complete"}},"id":"evt_noidck","type":"checkout.session.completed","livemode":false,"created":1234567890`
		if err := executeBoundaryWebhook(t, useCase, body); err == nil {
			t.Fatal("data-first checkout without nested id succeeded, want refusal")
		}
	})
	t.Run("subscription", func(t *testing.T) {
		t.Parallel()
		_, _, useCase := newMemberWebhookHarness(t)
		body := `"data":{"object":{"status":"active"}},"id":"evt_noidsub","type":"customer.subscription.updated","livemode":false,"created":1789740000`
		if err := executeBoundaryWebhook(t, useCase, body); err == nil {
			t.Fatal("data-first subscription without nested id succeeded, want refusal")
		}
	})
	t.Run("refund", func(t *testing.T) {
		t.Parallel()
		_, _, useCase := newRefundWebhookHarness(t)
		body := `"data":{"object":{"checkout_session":"cs_test_refundweb1"}},"id":"evt_noidref","type":"charge.refunded","livemode":false,"created":1789740000`
		if err := executeBoundaryWebhook(t, useCase, body); err == nil {
			t.Fatal("data-first refund without nested id succeeded, want refusal")
		}
	})
}

func TestProcessWebhookEscapedIdentifiersRefuse(t *testing.T) {
	t.Parallel()

	// Escape sequences inside identifiers never decode to a processable
	// value: the refusal is identical however the backslash resolves
	// (mutation gate: process_webhook.go:372,373).
	bodies := []string{
		`{"id":"evt_esc\"01","type":"checkout.session.expired","livemode":false,"created":1234567890}`,
		`{"id":"evt_esc\\01","type":"checkout.session.expired","livemode":false,"created":1234567890}`,
		`{"id":"evt_esc\n01","type":"checkout.session.expired","livemode":false,"created":1234567890}`,
	}
	for _, body := range bodies {
		repo := newFakeWebhookEventRepository()
		useCase := newBoundaryWebhookUseCase(t, repo)
		if err := executeBoundaryWebhook(t, useCase, body); err == nil {
			t.Fatalf("escaped body %q succeeded, want refusal", body)
		}
	}
}
