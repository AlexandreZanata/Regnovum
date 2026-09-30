package outbox_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/i18n"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	jobsapp "github.com/AlexandreZanata/Regnovum/internal/jobs/application"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/adapters/outbox"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/adapters/renderer"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/application"
	"github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
)

const knownCode = "K7QP-2M4Z-9RTX"

// --- fakes -----------------------------------------------------------------

// fakeQueue is the in-memory queue repository: it records every enqueue and
// resolves a repeated key the way the PostgreSQL adapter does.
type fakeQueue struct {
	mu       sync.Mutex
	records  []jobsapp.EnqueueRecord
	resolved map[string]*jobsdomain.Job
	nextID   int
	failWith error
}

func newFakeQueue() *fakeQueue {
	return &fakeQueue{resolved: make(map[string]*jobsdomain.Job)}
}

func (q *fakeQueue) Enqueue(_ context.Context, record jobsapp.EnqueueRecord) (*jobsdomain.Job, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.failWith != nil {
		return nil, false, q.failWith
	}
	if record.IdempotencyKey != "" {
		if existing, ok := q.resolved[record.IdempotencyKey]; ok {
			return existing, true, nil
		}
	}
	q.nextID++
	job := &jobsdomain.Job{
		ID:             fmt.Sprintf("0191f0e0-0000-7000-8000-%012d", q.nextID),
		Type:           record.Type,
		Version:        record.Version,
		Payload:        record.Payload,
		IdempotencyKey: record.IdempotencyKey,
		State:          jobsdomain.StateQueued,
		AvailableAt:    record.AvailableAt,
		MaxAttempts:    record.MaxAttempts,
		CreatedAt:      record.CreatedAt,
	}
	q.records = append(q.records, record)
	if record.IdempotencyKey != "" {
		q.resolved[record.IdempotencyKey] = job
	}
	return job, false, nil
}

func (q *fakeQueue) Lease(context.Context, string, time.Time, time.Time) (*jobsdomain.Job, error) {
	return nil, errors.New("fake queue: Lease is not part of this test")
}

func (q *fakeQueue) Complete(context.Context, string, string, time.Time) (*jobsdomain.Job, error) {
	return nil, errors.New("fake queue: Complete is not part of this test")
}

func (q *fakeQueue) Fail(context.Context, jobsdomain.Failure, string, string, time.Time, time.Time) (*jobsdomain.Job, error) {
	return nil, errors.New("fake queue: Fail is not part of this test")
}

func (q *fakeQueue) ReleaseExpiredLeases(context.Context, time.Time) (int, error) {
	return 0, errors.New("fake queue: ReleaseExpiredLeases is not part of this test")
}

func (q *fakeQueue) JobByID(context.Context, string) (*jobsdomain.Job, error) {
	return nil, errors.New("fake queue: JobByID is not part of this test")
}

func (q *fakeQueue) payloads() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]string, 0, len(q.records))
	for _, record := range q.records {
		out = append(out, string(record.Payload))
	}
	return out
}

type fakeClock struct{ now time.Time }

func (c fakeClock) Now() time.Time { return c.now }

// fakeDirectory answers the account question from a table.
type fakeDirectory struct {
	ref   application.AccountRef
	err   error
	asked []string
	mu    sync.Mutex
}

func (d *fakeDirectory) AccountForAddress(_ context.Context, address string) (application.AccountRef, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.asked = append(d.asked, address)
	if d.err != nil {
		return application.AccountRef{}, d.err
	}
	return d.ref, nil
}

func (d *fakeDirectory) askedFor() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.asked...)
}

// fakeSender implements the email port and records what it was asked to send.
type fakeSender struct {
	mu       sync.Mutex
	keys     []string
	messages []domain.Message
	err      error
}

func (s *fakeSender) Send(_ context.Context, message domain.Message) (application.Receipt, error) {
	if err := message.Validate(); err != nil {
		return application.Receipt{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// The key is recorded before the outcome: a failed attempt still reached
	// the provider with it, which is what makes the next one the same send.
	s.keys = append(s.keys, message.IdempotencyKey())
	if s.err != nil {
		return application.Receipt{}, s.err
	}
	s.messages = append(s.messages, message)
	return application.Receipt{ProviderID: fmt.Sprintf("provider-%d", len(s.keys))}, nil
}

func (s *fakeSender) deliveredKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.keys...)
}

type fakeRenderer struct {
	err error
}

func (r fakeRenderer) Render(templateID domain.TemplateID, locale domain.Locale, _ domain.TemplateValues) (domain.Body, error) {
	if r.err != nil {
		return domain.Body{}, r.err
	}
	return domain.Body{
		Subject: "subject " + locale.String(),
		Text:    "text " + templateID.String(),
		HTML:    "<p>html " + templateID.String() + "</p>",
	}, nil
}

// harness wires the whole adapter chain over the fakes.
type harness struct {
	queue     *fakeQueue
	enqueuer  *outbox.Enqueuer
	directory *fakeDirectory
	notifier  *application.Notifier
	handler   *outbox.Handler
	sender    *fakeSender
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	queue := newFakeQueue()
	enqueue, err := jobsapp.NewEnqueueUseCase(queue, fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("enqueue use case: %v", err)
	}
	enqueuer, err := outbox.NewEnqueuer(enqueue)
	if err != nil {
		t.Fatalf("NewEnqueuer() error = %v", err)
	}
	directory := &fakeDirectory{}
	notifier, err := application.NewNotifier(directory, enqueuer)
	if err != nil {
		t.Fatalf("NewNotifier() error = %v", err)
	}
	sender := &fakeSender{}
	deliverer, err := application.NewDeliverer(fakeRenderer{}, sender)
	if err != nil {
		t.Fatalf("NewDeliverer() error = %v", err)
	}
	handler, err := outbox.NewHandler(deliverer)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return &harness{queue: queue, enqueuer: enqueuer, directory: directory, notifier: notifier, handler: handler, sender: sender}
}

func verificationRequest(t *testing.T) application.NotificationRequest {
	t.Helper()
	values, err := domain.NewTemplateValues("", knownCode)
	if err != nil {
		t.Fatalf("NewTemplateValues() error = %v", err)
	}
	return application.NotificationRequest{
		Template:  domain.TemplateVerification,
		Recipient: "ana.silva@example.com",
		Values:    values,
	}
}

func identityEmail(t *testing.T, address string) identitydomain.Email {
	t.Helper()
	parsed, err := identitydomain.ParseEmail(address)
	if err != nil {
		t.Fatalf("ParseEmail(%q) error = %v", address, err)
	}
	return parsed
}

func jobFor(t *testing.T, payload string) *jobsdomain.Job {
	t.Helper()
	return &jobsdomain.Job{
		ID:      "0191f0e0-0000-7000-8000-000000000001",
		Type:    jobsdomain.TypeEmailDelivery,
		Version: outbox.PayloadVersion,
		Payload: []byte(payload),
		State:   jobsdomain.StateLeased,
	}
}

// --- the identity bridge ---------------------------------------------------

// TestSenderQueuesThePasswordChangeNoticeAnchoredOnTheChange proves the bridge
// the identity module uses to announce a password change (P16-T06): the notice
// carries no code, and the change identifier is what tells two changes apart —
// without it the second change of an account would be swallowed as a replay of
// the first.
func TestSenderQueuesThePasswordChangeNoticeAnchoredOnTheChange(t *testing.T) {
	built := newHarness(t)
	ctx := context.Background()
	address := identityEmail(t, "ana.silva@example.com")

	bridge, err := outbox.NewSender(built.notifier)
	if err != nil {
		t.Fatalf("NewSender() error = %v", err)
	}

	if err := bridge.SendPasswordChangedEmail(ctx, address, "reset-1"); err != nil {
		t.Fatalf("SendPasswordChangedEmail() error = %v", err)
	}
	if len(built.queue.records) != 1 {
		t.Fatalf("queued records = %d, want one per change", len(built.queue.records))
	}
	payload := built.queue.payloads()[0]
	if !strings.Contains(payload, domain.TemplatePasswordChanged.String()) {
		t.Fatalf("queued payload = %s, want the password change notice", payload)
	}
	if strings.Contains(payload, "\"code\":\"reset-1\"") {
		t.Fatalf("queued payload = %s, want the anchor out of the message", payload)
	}

	// A retry of the same change resolves the message it already queued.
	if err := bridge.SendPasswordChangedEmail(ctx, address, "reset-1"); err != nil {
		t.Fatalf("replayed SendPasswordChangedEmail() error = %v", err)
	}
	if len(built.queue.records) != 1 {
		t.Fatalf("queued records after a retry = %d, want the original one", len(built.queue.records))
	}

	// The next change is a new message, not a replay.
	if err := bridge.SendPasswordChangedEmail(ctx, address, "reset-2"); err != nil {
		t.Fatalf("second SendPasswordChangedEmail() error = %v", err)
	}
	if len(built.queue.records) != 2 {
		t.Fatalf("queued records after a second change = %d, want two distinct messages", len(built.queue.records))
	}

	// An anchor is required: a notice nobody anchored would be deduplicated
	// forever after the first change of the account.
	if err := bridge.SendPasswordChangedEmail(ctx, address, "  "); err == nil {
		t.Fatal("SendPasswordChangedEmail() without an anchor = nil, want a refusal")
	}
	if err := bridge.SendPasswordChangedEmail(ctx, address, "with space"); err == nil {
		t.Fatal("SendPasswordChangedEmail() with a separator in the anchor = nil, want a refusal")
	}
}

// --- the notifier ----------------------------------------------------------

// TestNotifierFreezesThePreferenceLocale proves the locale is decided at
// enqueue time and stored in the job, which is what lets a retry render in the
// language the event happened in even after the owner changes preference.
func TestNotifierFreezesThePreferenceLocale(t *testing.T) {
	built := newHarness(t)
	built.directory.ref = application.AccountRef{ID: "account-1", Locale: domain.LocaleAmericanEnglish}
	work, err := built.notifier.Notify(context.Background(), verificationRequest(t))
	if err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	if work.JobID == "" || work.Replayed {
		t.Errorf("work = %+v, want a created job", work)
	}
	payloads := built.queue.payloads()
	if len(payloads) != 1 {
		t.Fatalf("payloads = %d, want 1", len(payloads))
	}
	if !strings.Contains(payloads[0], `"locale":"en-US"`) {
		t.Errorf("payload = %s, want the frozen preference", payloads[0])
	}
}

func TestNotifierFallsBackToTheProductDefault(t *testing.T) {
	for _, testCase := range []struct {
		name string
		ref  application.AccountRef
	}{
		{"no account", application.AccountRef{}},
		{"account without a preference", application.AccountRef{ID: "account-1"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			built := newHarness(t)
			built.directory.ref = testCase.ref
			if _, err := built.notifier.Notify(context.Background(), verificationRequest(t)); err != nil {
				t.Fatalf("Notify() error = %v", err)
			}
			if payload := built.queue.payloads()[0]; !strings.Contains(payload, `"locale":"pt-BR"`) {
				t.Errorf("payload = %s, want the default locale", payload)
			}
			if answers := built.directory.askedFor(); len(answers) != 1 || answers[0] != "ana.silva@example.com" {
				t.Errorf("directory asked about %v, want the recipient", answers)
			}
		})
	}
}

func TestNotifierHonoursAnExplicitLocaleWithoutAskingTheDirectory(t *testing.T) {
	built := newHarness(t)
	locale := domain.LocaleAmericanEnglish
	request := verificationRequest(t)
	request.Locale = &locale
	if _, err := built.notifier.Notify(context.Background(), request); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	if answers := built.directory.askedFor(); len(answers) != 0 {
		t.Errorf("directory was asked %v, want no lookup for an explicit locale", answers)
	}
	if payload := built.queue.payloads()[0]; !strings.Contains(payload, `"locale":"en-US"`) {
		t.Errorf("payload = %s, want the explicit locale", payload)
	}
}

// TestNotifierRefusesBeforeQueueing is the "no job for a message that cannot
// be composed" rule: every refusal happens before a row exists.
func TestNotifierRefusesBeforeQueueing(t *testing.T) {
	values, err := domain.NewTemplateValues("", knownCode)
	if err != nil {
		t.Fatalf("NewTemplateValues() error = %v", err)
	}
	unsupported := domain.Locale("fr-FR")
	for _, testCase := range []struct {
		name    string
		mutate  func(*application.NotificationRequest)
		wantErr error
	}{
		{"unknown template", func(r *application.NotificationRequest) { r.Template = "marketing" }, domain.ErrUnsupportedTemplate},
		{"malformed recipient", func(r *application.NotificationRequest) { r.Recipient = "not an address" }, domain.ErrInvalidRecipient},
		{"empty code", func(r *application.NotificationRequest) { r.Values = domain.TemplateValues{} }, domain.ErrInvalidTemplateValue},
		{"unsupported explicit locale", func(r *application.NotificationRequest) { r.Locale = &unsupported }, domain.ErrUnsupportedLocale},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			built := newHarness(t)
			request := application.NotificationRequest{Template: domain.TemplateVerification, Recipient: "ana@example.com", Values: values}
			testCase.mutate(&request)
			if _, err := built.notifier.Notify(context.Background(), request); !errors.Is(err, testCase.wantErr) {
				t.Errorf("Notify() error = %v, want %v", err, testCase.wantErr)
			}
			if payloads := built.queue.payloads(); len(payloads) != 0 {
				t.Errorf("payloads = %d, want 0", len(payloads))
			}
		})
	}
}

// TestNotifierPropagatesADirectoryFailure is what keeps an event from
// committing with a job whose locale was never resolved: the caller fails and
// the transaction rolls back.
func TestNotifierPropagatesADirectoryFailure(t *testing.T) {
	built := newHarness(t)
	failure := errors.New("identity: read unavailable")
	built.directory.err = failure
	if _, err := built.notifier.Notify(context.Background(), verificationRequest(t)); !errors.Is(err, failure) {
		t.Errorf("Notify() error = %v, want the directory failure", err)
	}
	if payloads := built.queue.payloads(); len(payloads) != 0 {
		t.Errorf("payloads = %d, want 0", len(payloads))
	}
}

func TestNotifierRequiresBothPorts(t *testing.T) {
	if _, err := application.NewNotifier(nil, &outbox.Enqueuer{}); !errors.Is(err, domain.ErrMissingDependency) {
		t.Errorf("NewNotifier(nil, enqueuer) error = %v, want ErrMissingDependency", err)
	}
	if _, err := application.NewNotifier(&fakeDirectory{}, nil); !errors.Is(err, domain.ErrMissingDependency) {
		t.Errorf("NewNotifier(directory, nil) error = %v, want ErrMissingDependency", err)
	}
}

// --- retry does not duplicate the effect -----------------------------------

// TestRepeatedEventResolvesTheSameJob is the "retry não duplica efeito" rule
// at the queue: the same event never produces a second job, and a new code
// does, because a new code is a new event.
func TestRepeatedEventResolvesTheSameJob(t *testing.T) {
	built := newHarness(t)
	request := verificationRequest(t)
	first, err := built.notifier.Notify(context.Background(), request)
	if err != nil {
		t.Fatalf("first Notify() error = %v", err)
	}
	second, err := built.notifier.Notify(context.Background(), request)
	if err != nil {
		t.Fatalf("second Notify() error = %v", err)
	}
	if first.JobID != second.JobID {
		t.Errorf("job ids = %q and %q, want the same job", first.JobID, second.JobID)
	}
	if !second.Replayed {
		t.Error("second Notify() Replayed = false, want true")
	}
	if payloads := built.queue.payloads(); len(payloads) != 1 {
		t.Fatalf("payloads = %d, want 1", len(payloads))
	}

	// A fresh code is a fresh event: the user asked again and must receive
	// the code that is actually active.
	fresh, err := domain.NewTemplateValues("", "ZZZZ-1111-2222")
	if err != nil {
		t.Fatalf("NewTemplateValues() error = %v", err)
	}
	request.Values = fresh
	third, err := built.notifier.Notify(context.Background(), request)
	if err != nil {
		t.Fatalf("third Notify() error = %v", err)
	}
	if third.JobID == first.JobID || third.Replayed {
		t.Errorf("third work = %+v, want a new job", third)
	}
	if payloads := built.queue.payloads(); len(payloads) != 2 {
		t.Errorf("payloads = %d, want 2", len(payloads))
	}
}

// TestDeliveryKeyIsStableAcrossAttempts proves the provider sees one logical
// send per job even when the worker retries it.
func TestDeliveryKeyIsStableAcrossAttempts(t *testing.T) {
	built := newHarness(t)
	job := jobFor(t, `{"template":"verification","locale":"pt-BR","recipient":"ana@example.com","name":"","code":"`+knownCode+`"}`)
	if err := built.handler.Handle(context.Background(), job); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if err := built.handler.Handle(context.Background(), job); err != nil {
		t.Fatalf("retry Handle() error = %v", err)
	}
	keys := built.sender.deliveredKeys()
	if len(keys) != 2 {
		t.Fatalf("deliveries = %d, want 2", len(keys))
	}
	if keys[0] != keys[1] {
		t.Errorf("keys = %q and %q, want the same provider key", keys[0], keys[1])
	}
	if keys[0] != "job:"+job.ID {
		t.Errorf("key = %q, want it anchored on the job", keys[0])
	}
}

// --- the handler -----------------------------------------------------------

func TestHandlerDeliversTheFrozenLocale(t *testing.T) {
	built := newHarness(t)
	job := jobFor(t, `{"template":"password_reset","locale":"en-US","recipient":"ana@example.com","name":"Ana","code":"`+knownCode+`"}`)
	if err := built.handler.Handle(context.Background(), job); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	messages := built.sender.messages
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	if messages[0].Locale() != domain.LocaleAmericanEnglish || messages[0].Template() != domain.TemplatePasswordReset {
		t.Errorf("message = %s/%s, want the stored locale and template", messages[0].Locale(), messages[0].Template())
	}
	if messages[0].Recipient() != "ana@example.com" {
		t.Errorf("recipient = %q", messages[0].Recipient())
	}
}

// TestRetryAfterTheOwnerChangesLocaleKeepsTheOriginalLanguage is the property
// the standard asks for, proven end to end with the real renderer: the job
// carries the facts of the message, so the language is decided when the event
// happens and a retry after a preference change still speaks the language the
// event spoke.
func TestRetryAfterTheOwnerChangesLocaleKeepsTheOriginalLanguage(t *testing.T) {
	built := newHarness(t)
	// The event happens while the owner prefers English.
	built.directory.ref = application.AccountRef{ID: "account-1", Locale: domain.LocaleAmericanEnglish}
	if _, err := built.notifier.Notify(context.Background(), verificationRequest(t)); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	payloads := built.queue.payloads()
	if len(payloads) != 1 {
		t.Fatalf("payloads = %d, want 1", len(payloads))
	}
	payload := payloads[0]

	// The stored payload is the canonical locale and the parameters of the
	// message: never a rendered body, which is what lets a retry re-render
	// after a translator's fix.
	if strings.Contains(payload, "<") || strings.Contains(payload, "doctype") {
		t.Errorf("payload carries markup: %s", payload)
	}
	for _, want := range []string{`"template":"verification"`, `"locale":"en-US"`, `"code":"` + knownCode + `"`} {
		if !strings.Contains(payload, want) {
			t.Errorf("payload = %s, want %s", payload, want)
		}
	}

	// The owner then switches the interface to Portuguese.
	built.directory.ref = application.AccountRef{ID: "account-1", Locale: domain.LocaleBrazilianPortuguese}

	engine, err := renderer.NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer() error = %v", err)
	}
	deliverer, err := application.NewDeliverer(engine, built.sender)
	if err != nil {
		t.Fatalf("NewDeliverer() error = %v", err)
	}
	handler, err := outbox.NewHandler(deliverer)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}

	// Two attempts of the same job: the first delivery and the retry.
	job := jobFor(t, payload)
	for attempt := 1; attempt <= 2; attempt++ {
		if err := handler.Handle(context.Background(), job); err != nil {
			t.Fatalf("Handle() attempt %d error = %v", attempt, err)
		}
	}
	messages := built.sender.messages
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want the delivery and its retry", len(messages))
	}
	wantSubject, err := i18n.Format(domain.LocaleAmericanEnglish.String(), "email.verification.subject", nil)
	if err != nil {
		t.Fatalf("catalog lookup error = %v", err)
	}
	ptSubject, err := i18n.Format(domain.LocaleBrazilianPortuguese.String(), "email.verification.subject", nil)
	if err != nil {
		t.Fatalf("catalog lookup error = %v", err)
	}
	for index, message := range messages {
		if message.Locale() != domain.LocaleAmericanEnglish {
			t.Errorf("attempt %d locale = %s, want the frozen locale", index+1, message.Locale())
		}
		if message.Body().Subject != wantSubject {
			t.Errorf("attempt %d subject = %q, want the original language", index+1, message.Body().Subject)
		}
		if strings.Contains(message.Body().HTML, ptSubject) {
			t.Errorf("attempt %d was retargeted by the preference change", index+1)
		}
		if !strings.Contains(message.Body().HTML, `lang="en-US"`) {
			t.Errorf("attempt %d document language = %q, want the frozen locale", index+1, message.Body().HTML)
		}
	}

	// Nothing persisted changed either: the enqueue is the only write.
	if after := built.queue.payloads(); len(after) != 1 || after[0] != payload {
		t.Errorf("payloads after delivery = %v, want the frozen payload untouched", after)
	}
}

func TestHandlerRefusesPayloadsItCannotTrust(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		payload string
	}{
		{"empty", ``},
		{"not json", `{`},
		{"unknown template", `{"template":"marketing","locale":"pt-BR","recipient":"ana@example.com","code":"` + knownCode + `"}`},
		{"unknown locale", `{"template":"verification","locale":"fr-FR","recipient":"ana@example.com","code":"` + knownCode + `"}`},
		{"malformed recipient", `{"template":"verification","locale":"pt-BR","recipient":"nope","code":"` + knownCode + `"}`},
		{"missing code", `{"template":"verification","locale":"pt-BR","recipient":"ana@example.com","code":""}`},
		{"code with a space", `{"template":"verification","locale":"pt-BR","recipient":"ana@example.com","code":"a b"}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			built := newHarness(t)
			if err := built.handler.Handle(context.Background(), jobFor(t, testCase.payload)); err == nil {
				t.Error("Handle() error = nil, want a refusal")
			}
			if len(built.sender.deliveredKeys()) != 0 {
				t.Error("a refused payload reached the provider")
			}
		})
	}
}

func TestHandlerRefusesAJobThatIsNotItsOwn(t *testing.T) {
	built := newHarness(t)
	wrongType := jobFor(t, `{}`)
	wrongType.Type = jobsdomain.TypeSessionCleanup
	if err := built.handler.Handle(context.Background(), wrongType); err == nil {
		t.Error("Handle() error = nil, want a refusal for a foreign workload")
	}
	wrongVersion := jobFor(t, `{}`)
	wrongVersion.Version = outbox.PayloadVersion + 1
	if err := built.handler.Handle(context.Background(), wrongVersion); err == nil {
		t.Error("Handle() error = nil, want a refusal for a foreign version")
	}
	if err := built.handler.Handle(context.Background(), nil); err == nil {
		t.Error("Handle(nil) error = nil, want a refusal")
	}
}

func TestHandlerSurfacesTheProviderFailure(t *testing.T) {
	built := newHarness(t)
	built.sender.err = application.ErrProviderRateLimited
	err := built.handler.Handle(context.Background(), jobFor(t, `{"template":"verification","locale":"pt-BR","recipient":"ana@example.com","code":"`+knownCode+`"}`))
	if err == nil {
		t.Fatal("Handle() error = nil, want the provider failure")
	}
	if !application.IsRetryable(err) {
		t.Errorf("Handle() error = %v, want a retryable failure", err)
	}
}

func TestHandlerRegistersItsWorkload(t *testing.T) {
	built := newHarness(t)
	registry := jobsapp.NewHandlerMap()
	if err := built.handler.Register(registry); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	resolved, err := registry.Resolve(jobsdomain.TypeEmailDelivery, outbox.PayloadVersion)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved == nil {
		t.Fatal("Resolve() returned no handler")
	}
	if err := built.handler.Register(nil); !errors.Is(err, domain.ErrMissingDependency) {
		t.Errorf("Register(nil) error = %v, want ErrMissingDependency", err)
	}
}

func TestEnqueuerRequiresTheQueueAndAContext(t *testing.T) {
	if _, err := outbox.NewEnqueuer(nil); !errors.Is(err, domain.ErrMissingDependency) {
		t.Errorf("NewEnqueuer(nil) error = %v, want ErrMissingDependency", err)
	}
	built := newHarness(t)
	resolved := application.ResolvedNotification{
		Template:  domain.TemplateVerification,
		Recipient: "ana@example.com",
		Values:    domain.TemplateValues{Code: knownCode},
		Locale:    domain.LocaleDefault,
		EventKey:  domain.EventKey(domain.TemplateVerification, "ana@example.com", knownCode),
	}
	//lint:ignore SA1012 the explicit nil context is the failure under test
	if _, err := built.enqueuer.Enqueue(nil, resolved); err == nil {
		t.Error("Enqueue(nil) error = nil, want a refusal")
	}
}

func TestEnqueuerRefusesAnUnresolvedNotification(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		resolved   application.ResolvedNotification
		wantErr    error
		wantQueued bool
	}{
		{
			name:     "unknown template",
			resolved: application.ResolvedNotification{Template: "marketing", Locale: domain.LocaleDefault, Recipient: "ana@example.com", Values: domain.TemplateValues{Code: knownCode}, EventKey: "email:marketing:1"},
			wantErr:  domain.ErrUnsupportedTemplate,
		},
		{
			name:     "unknown locale",
			resolved: application.ResolvedNotification{Template: domain.TemplateVerification, Locale: "fr-FR", Recipient: "ana@example.com", Values: domain.TemplateValues{Code: knownCode}, EventKey: "email:verification:1"},
			wantErr:  domain.ErrUnsupportedLocale,
		},
		{
			name:     "malformed recipient",
			resolved: application.ResolvedNotification{Template: domain.TemplateVerification, Locale: domain.LocaleDefault, Recipient: "nope", Values: domain.TemplateValues{Code: knownCode}, EventKey: "email:verification:1"},
			wantErr:  domain.ErrInvalidRecipient,
		},
		{
			name:     "missing code",
			resolved: application.ResolvedNotification{Template: domain.TemplateVerification, Locale: domain.LocaleDefault, Recipient: "ana@example.com", EventKey: "email:verification:1"},
			wantErr:  domain.ErrInvalidTemplateValue,
		},
		{
			name:     "missing event key",
			resolved: application.ResolvedNotification{Template: domain.TemplateVerification, Locale: domain.LocaleDefault, Recipient: "ana@example.com", Values: domain.TemplateValues{Code: knownCode}},
			wantErr:  domain.ErrInvalidIdempotencyKey,
		},
		{
			name:     "event key outside the alphabet",
			resolved: application.ResolvedNotification{Template: domain.TemplateVerification, Locale: domain.LocaleDefault, Recipient: "ana@example.com", Values: domain.TemplateValues{Code: knownCode}, EventKey: "email:verification:1 2"},
			wantErr:  domain.ErrInvalidIdempotencyKey,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			queue := newFakeQueue()
			enqueue, err := jobsapp.NewEnqueueUseCase(queue, fakeClock{now: time.Now()})
			if err != nil {
				t.Fatalf("enqueue use case: %v", err)
			}
			enqueuer, err := outbox.NewEnqueuer(enqueue)
			if err != nil {
				t.Fatalf("NewEnqueuer() error = %v", err)
			}
			if _, err := enqueuer.Enqueue(context.Background(), testCase.resolved); !errors.Is(err, testCase.wantErr) {
				t.Errorf("Enqueue() error = %v, want %v", err, testCase.wantErr)
			}
			if payloads := queue.payloads(); len(payloads) != 0 {
				t.Errorf("payloads = %d, want 0", len(payloads))
			}
		})
	}
}

func TestEnqueuerStoresAQueueableJob(t *testing.T) {
	built := newHarness(t)
	if _, err := built.notifier.Notify(context.Background(), verificationRequest(t)); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	record := built.queue.records[0]
	if record.Type != jobsdomain.TypeEmailDelivery {
		t.Errorf("type = %q, want %q", record.Type, jobsdomain.TypeEmailDelivery)
	}
	if record.Version != outbox.PayloadVersion {
		t.Errorf("version = %d, want %d", record.Version, outbox.PayloadVersion)
	}
	if err := jobsdomain.ValidatePayload(record.Payload); err != nil {
		t.Errorf("payload is not queueable: %v", err)
	}
	if record.MaxAttempts != jobsdomain.DefaultMaxAttempts {
		t.Errorf("max attempts = %d, want the default budget", record.MaxAttempts)
	}
	// The stored payload carries the frozen facts and never a rendered body:
	// rendering happens in the worker.
	for _, fragment := range []string{"<p", "Confirme", "Olá"} {
		if strings.Contains(string(record.Payload), fragment) {
			t.Errorf("payload carries rendered content %q: %s", fragment, record.Payload)
		}
	}
}

func TestEnqueuerSurfacesTheQueueFailure(t *testing.T) {
	built := newHarness(t)
	failure := errors.New("queue: write unavailable")
	built.queue.failWith = failure
	if _, err := built.notifier.Notify(context.Background(), verificationRequest(t)); !errors.Is(err, failure) {
		t.Errorf("Notify() error = %v, want the queue failure", err)
	}
}

// --- the identity bridge ---------------------------------------------------

func TestBridgeQueuesBothIdentityFlows(t *testing.T) {
	built := newHarness(t)
	bridge, err := outbox.NewSender(built.notifier)
	if err != nil {
		t.Fatalf("NewSender() error = %v", err)
	}
	email := identityEmail(t, "ana.silva@example.com")
	if err := bridge.SendVerificationEmail(context.Background(), email, knownCode); err != nil {
		t.Fatalf("SendVerificationEmail() error = %v", err)
	}
	if err := bridge.SendPasswordResetEmail(context.Background(), email, "ZZZZ-1111-2222"); err != nil {
		t.Fatalf("SendPasswordResetEmail() error = %v", err)
	}
	payloads := built.queue.payloads()
	if len(payloads) != 2 {
		t.Fatalf("payloads = %d, want 2", len(payloads))
	}
	if !strings.Contains(payloads[0], `"template":"verification"`) || !strings.Contains(payloads[1], `"template":"password_reset"`) {
		t.Errorf("payloads = %v, want one job per flow", payloads)
	}
}

func TestBridgeRefusesAnInvalidCodeBeforeQueueing(t *testing.T) {
	built := newHarness(t)
	bridge, err := outbox.NewSender(built.notifier)
	if err != nil {
		t.Fatalf("NewSender() error = %v", err)
	}
	email := identityEmail(t, "ana.silva@example.com")
	if err := bridge.SendVerificationEmail(context.Background(), email, "code with spaces"); !errors.Is(err, domain.ErrInvalidTemplateValue) {
		t.Errorf("SendVerificationEmail() error = %v, want ErrInvalidTemplateValue", err)
	}
	if payloads := built.queue.payloads(); len(payloads) != 0 {
		t.Errorf("payloads = %d, want 0", len(payloads))
	}
	if err := bridge.SendVerificationEmail(context.Background(), identitydomain.Email{}, "code"); !errors.Is(err, domain.ErrInvalidRecipient) {
		t.Errorf("SendVerificationEmail(zero email) error = %v, want ErrInvalidRecipient", err)
	}
}

func TestBridgeRequiresTheNotifier(t *testing.T) {
	if _, err := outbox.NewSender(nil); !errors.Is(err, domain.ErrMissingDependency) {
		t.Errorf("NewSender(nil) error = %v, want ErrMissingDependency", err)
	}
}
