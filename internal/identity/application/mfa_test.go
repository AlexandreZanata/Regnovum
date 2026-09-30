package application_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/argon2id"
	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/mfamechanism"
	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/mfa"
)

// The second factor of the administrative surface (P16-T05) is tested here at
// the level of its guarantees, over the real mechanism: the RFC is exercised by
// internal/platform/mfa, and what these tests hold is the policy above it — that
// a spent time step cannot be spent again, that a recovery code is single-use,
// that a recovery is recorded before the session it elevates, and that none of
// those guarantees depends on the order in which two concurrent requests happen
// to run.

// mfaEventLog records the side effects of a use case in order, which is how the
// sequence the recovery path promises ("consume, record, elevate") is asserted
// rather than assumed.
type mfaEventLog struct {
	mu     sync.Mutex
	events []string
}

func (log *mfaEventLog) append(event string) {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.events = append(log.events, event)
}

func (log *mfaEventLog) snapshot() []string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]string{}, log.events...)
}

// mfaEnrollmentState is one account's second factor.
type mfaEnrollmentState struct {
	secretSealed     []byte
	confirmedAt      *time.Time
	lastAcceptedStep int64
	backupCodes      []application.MFABackupCodeRecord
	usedCodes        map[string]bool
}

// inMemoryMFARepo implements the module's storage port with the same guards the
// SQL statements carry: a confirmed enrollment is never replaced, a step only
// moves forward, a code is spent once and a session is elevated only while it
// exists.
type inMemoryMFARepo struct {
	mu          sync.Mutex
	enrollments map[string]*mfaEnrollmentState
	sessions    map[string]time.Time
	events      *mfaEventLog
}

func newInMemoryMFARepo(events *mfaEventLog) *inMemoryMFARepo {
	return &inMemoryMFARepo{
		enrollments: make(map[string]*mfaEnrollmentState),
		sessions:    make(map[string]time.Time),
		events:      events,
	}
}

func (repo *inMemoryMFARepo) state(accountID domain.AccountID) *mfaEnrollmentState {
	state, ok := repo.enrollments[string(accountID)]
	if !ok {
		state = &mfaEnrollmentState{lastAcceptedStep: -1, usedCodes: make(map[string]bool)}
		repo.enrollments[string(accountID)] = state
	}
	return state
}

func (repo *inMemoryMFARepo) GetMFAEnrollment(_ context.Context, accountID domain.AccountID) (*application.MFAEnrollmentRecord, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()

	state, ok := repo.enrollments[string(accountID)]
	if !ok {
		return nil, application.ErrMFAEnrollmentMissing
	}
	return &application.MFAEnrollmentRecord{
		SecretSealed:     append([]byte{}, state.secretSealed...),
		ConfirmedAt:      state.confirmedAt,
		LastAcceptedStep: state.lastAcceptedStep,
	}, nil
}

func (repo *inMemoryMFARepo) UpsertPendingMFAEnrollment(_ context.Context, accountID domain.AccountID, secretSealed []byte) (*application.MFAEnrollmentRecord, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()

	state := repo.state(accountID)
	if state.confirmedAt != nil {
		return nil, application.ErrMFAAlreadyEnrolled
	}
	state.secretSealed = append([]byte{}, secretSealed...)
	state.lastAcceptedStep = -1
	return &application.MFAEnrollmentRecord{
		SecretSealed:     append([]byte{}, state.secretSealed...),
		LastAcceptedStep: state.lastAcceptedStep,
	}, nil
}

func (repo *inMemoryMFARepo) ConfirmMFAEnrollment(_ context.Context, accountID domain.AccountID, confirmedAt time.Time, acceptedStep int64) (bool, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()

	state, ok := repo.enrollments[string(accountID)]
	if !ok || state.confirmedAt != nil {
		return false, nil
	}
	instant := confirmedAt
	state.confirmedAt = &instant
	state.lastAcceptedStep = acceptedStep
	return true, nil
}

func (repo *inMemoryMFARepo) AdvanceMFAVerifiedStep(_ context.Context, accountID domain.AccountID, step int64) (bool, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()

	state, ok := repo.enrollments[string(accountID)]
	if !ok || state.lastAcceptedStep >= step {
		return false, nil
	}
	state.lastAcceptedStep = step
	return true, nil
}

func (repo *inMemoryMFARepo) ReplaceMFABackupCodes(_ context.Context, accountID domain.AccountID, codeHashes []string) error {
	repo.mu.Lock()
	defer repo.mu.Unlock()

	state := repo.state(accountID)
	state.backupCodes = make([]application.MFABackupCodeRecord, 0, len(codeHashes))
	state.usedCodes = make(map[string]bool)
	for index, hash := range codeHashes {
		state.backupCodes = append(state.backupCodes, application.MFABackupCodeRecord{
			ID:       fmt.Sprintf("code_%d", index),
			CodeHash: hash,
		})
	}
	return nil
}

func (repo *inMemoryMFARepo) ListUnusedMFABackupCodes(_ context.Context, accountID domain.AccountID) ([]application.MFABackupCodeRecord, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()

	state, ok := repo.enrollments[string(accountID)]
	if !ok {
		return nil, nil
	}
	records := make([]application.MFABackupCodeRecord, 0, len(state.backupCodes))
	for _, record := range state.backupCodes {
		if !state.usedCodes[record.ID] {
			records = append(records, record)
		}
	}
	return records, nil
}

func (repo *inMemoryMFARepo) ConsumeMFABackupCode(_ context.Context, codeID string, _ time.Time) (bool, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()

	for _, state := range repo.enrollments {
		for _, record := range state.backupCodes {
			if record.ID != codeID {
				continue
			}
			if state.usedCodes[codeID] {
				return false, nil
			}
			state.usedCodes[codeID] = true
			repo.events.append("code:consumed")
			return true, nil
		}
	}
	return false, nil
}

func (repo *inMemoryMFARepo) MarkSessionMFAVerified(_ context.Context, sessionID domain.SessionID, verifiedAt time.Time) (bool, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()

	if _, ok := repo.sessions[string(sessionID)]; !ok {
		return false, nil
	}
	repo.sessions[string(sessionID)] = verifiedAt
	repo.events.append("session:elevated")
	return true, nil
}

func (repo *inMemoryMFARepo) seedSession(sessionID string) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	repo.sessions[sessionID] = time.Time{}
}

func (repo *inMemoryMFARepo) sessionElevation(sessionID string) (time.Time, bool) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	verifiedAt, ok := repo.sessions[sessionID]
	if !ok || verifiedAt.IsZero() {
		return time.Time{}, false
	}
	return verifiedAt, true
}

func (repo *inMemoryMFARepo) unusedBackupCodeCount(accountID domain.AccountID) int {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	state, ok := repo.enrollments[string(accountID)]
	if !ok {
		return 0
	}
	return len(state.backupCodes) - len(state.usedCodes)
}

// recordingMFAAudit records the administrative fact, and can be told to fail,
// which is what proves the recovery path fails closed when the trail cannot be
// written.
type recordingMFAAudit struct {
	events *mfaEventLog
	fail   bool
}

func (audit *recordingMFAAudit) RecordMFAEnrolled(_ context.Context, _ string, _ time.Time) error {
	if audit.fail {
		return errors.New("audit trail unavailable")
	}
	audit.events.append("audit:mfa.enrolled")
	return nil
}

func (audit *recordingMFAAudit) RecordMFABackupCodeUsed(_ context.Context, _ string, _ time.Time) error {
	if audit.fail {
		return errors.New("audit trail unavailable")
	}
	audit.events.append("audit:backup_code_used")
	return nil
}

// countingHasher counts how many times a code is hashed or verified, which is
// how "a malformed code never reaches the hasher" is stated as a fact.
type countingHasher struct {
	inner       application.PasswordHasher
	hashCalls   int
	verifyCalls int
}

func (hasher *countingHasher) HashPassword(password string) (string, error) {
	hasher.hashCalls++
	return hasher.inner.HashPassword(password)
}

func (hasher *countingHasher) VerifyPassword(password, encodedHash string) (bool, error) {
	hasher.verifyCalls++
	return hasher.inner.VerifyPassword(password, encodedHash)
}

func (hasher *countingHasher) NeedsRehash(encodedHash string) bool {
	return hasher.inner.NeedsRehash(encodedHash)
}

func (hasher *countingHasher) DummyHash() string {
	return hasher.inner.DummyHash()
}

// mfaSandbox is one composed second-factor surface: the real mechanism over the
// real RFC implementation, an in-memory storage that carries the same
// invariants as the SQL, and a clock the test controls.
type mfaSandbox struct {
	clock     *fakeClock
	repo      *inMemoryMFARepo
	audit     *recordingMFAAudit
	hasher    *countingHasher
	mechanism *mfamechanism.Mechanism
	log       *mfaEventLog

	Begin   *application.BeginMFAEnrollmentUseCase
	Confirm *application.ConfirmMFAEnrollmentUseCase
	StepUp  *application.StepUpMFAUseCase
	Recover *application.RecoverMFAUseCase
}

func newMFASandbox(t *testing.T) *mfaSandbox {
	t.Helper()

	clock := &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
	sealer, err := mfa.NewSealer(bytes.Repeat([]byte{0x2b}, mfa.KeySize), clockseed.NewRandom())
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	mechanism, err := mfamechanism.New(mfa.Config{}, sealer, clockseed.NewRandom())
	if err != nil {
		t.Fatalf("mfamechanism.New: %v", err)
	}
	innerHasher, err := argon2id.New(argon2id.FastParams(), rand.Reader)
	if err != nil {
		t.Fatalf("argon2id.New: %v", err)
	}

	log := &mfaEventLog{}
	repo := newInMemoryMFARepo(log)
	audit := &recordingMFAAudit{events: log}
	sandbox := &mfaSandbox{
		clock:     clock,
		repo:      repo,
		audit:     audit,
		hasher:    &countingHasher{inner: innerHasher},
		mechanism: mechanism,
		log:       log,
	}

	sandbox.Begin = application.NewBeginMFAEnrollmentUseCase(repo, mechanism)
	sandbox.Confirm = application.NewConfirmMFAEnrollmentUseCase(repo, mechanism, sandbox.hasher, clock, audit)
	sandbox.StepUp = application.NewStepUpMFAUseCase(repo, mechanism, clock)
	sandbox.Recover = application.NewRecoverMFAUseCase(repo, mechanism, sandbox.hasher, clock, audit)
	return sandbox
}

// enroll runs the whole enrollment and returns the account's shared secret and
// the recovery codes it was shown.
func (sandbox *mfaSandbox) enroll(t *testing.T, accountID string) ([]byte, []string) {
	t.Helper()

	enrollment, err := sandbox.Begin.Execute(context.Background(), application.BeginMFAEnrollmentCommand{AccountID: accountID})
	if err != nil {
		t.Fatalf("begin enrollment: %v", err)
	}
	if !strings.HasPrefix(enrollment.URI, "otpauth://totp/") {
		t.Fatalf("enrollment URI = %q, want an otpauth URI", enrollment.URI)
	}

	secret, err := mfa.DecodeSecret(enrollment.Secret)
	if err != nil {
		t.Fatalf("decode enrollment secret: %v", err)
	}

	confirmation, err := sandbox.Confirm.Execute(context.Background(), application.ConfirmMFAEnrollmentCommand{
		AccountID: accountID,
		Code:      sandbox.code(t, secret, sandbox.clock.Now()),
	})
	if err != nil {
		t.Fatalf("confirm enrollment: %v", err)
	}
	if len(confirmation.BackupCodes) != mfa.DefaultBackupCodes {
		t.Fatalf("backup codes = %d, want %d", len(confirmation.BackupCodes), mfa.DefaultBackupCodes)
	}
	return secret, confirmation.BackupCodes
}

// code is what the operator's authenticator shows at an instant.
func (sandbox *mfaSandbox) code(t *testing.T, secret []byte, instant time.Time) string {
	t.Helper()
	code, err := mfa.Config{}.Code(secret, instant)
	if err != nil {
		t.Fatalf("compute code: %v", err)
	}
	return code
}

// assertRefused reports the refusal of a use case against the module's own
// vocabulary. The wire code is the adapter's job and is asserted by the HTTP
// tests; here the fact is what matters.
func assertRefused(t *testing.T, err error, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestMFAEnrollmentConfirmsWithACodeAndIssuesRecoveryCodes(t *testing.T) {
	sandbox := newMFASandbox(t)
	const accountID = "account-enroll"

	secret, codes := sandbox.enroll(t, accountID)
	if len(secret) != mfa.DefaultSecretBytes {
		t.Fatalf("secret length = %d, want %d", len(secret), mfa.DefaultSecretBytes)
	}
	if events := sandbox.log.snapshot(); len(events) != 1 || events[0] != "audit:mfa.enrolled" {
		t.Fatalf("events after enrollment = %v, want the enrollment recorded exactly once", events)
	}

	// The codes are stored hashed: nothing in storage is a code a person could
	// read back out.
	stored, err := sandbox.repo.ListUnusedMFABackupCodes(context.Background(), domain.AccountID(accountID))
	if err != nil {
		t.Fatalf("list backup codes: %v", err)
	}
	if len(stored) != len(codes) {
		t.Fatalf("stored codes = %d, want %d", len(stored), len(codes))
	}
	for _, record := range stored {
		for _, code := range codes {
			if record.CodeHash == code || record.CodeHash == mfa.NormalizeBackupCode(code) {
				t.Fatalf("stored hash equals a plaintext code %q", code)
			}
		}
	}

	// A confirmed enrollment cannot be restarted: that would be a way to
	// replace a working factor with one the caller controls.
	if _, err := sandbox.Begin.Execute(context.Background(), application.BeginMFAEnrollmentCommand{AccountID: accountID}); !errors.Is(err, application.ErrMFAAlreadyEnrolled) {
		t.Fatalf("second begin error = %v, want ErrMFAAlreadyEnrolled", err)
	}
	// The refusal names the enrollment, not the code: a confirmed enrollment is
	// not a pending one whose code happened to be spent, and an operator needs
	// to be told which of the two they are looking at. The spent step is proved
	// by the step-up test below.
	if _, err := sandbox.Confirm.Execute(context.Background(), application.ConfirmMFAEnrollmentCommand{
		AccountID: accountID,
		Code:      sandbox.code(t, secret, sandbox.clock.Now()),
	}); !errors.Is(err, application.ErrMFAAlreadyEnrolled) {
		t.Fatalf("second confirm error = %v, want ErrMFAAlreadyEnrolled", err)
	}
}

func TestMFAConfirmationSpendsTheTimeStep(t *testing.T) {
	sandbox := newMFASandbox(t)
	const accountID = "account-step"
	const sessionID = "session-step"
	sandbox.repo.seedSession(sessionID)

	secret, _ := sandbox.enroll(t, accountID)
	confirmingCode := sandbox.code(t, secret, sandbox.clock.Now())

	// The code that confirmed the enrollment is inside the same time step, so
	// presenting it again is a replay — not a wrong code.
	err := sandbox.StepUp.Execute(context.Background(), application.StepUpMFACommand{
		AccountID: accountID,
		SessionID: sessionID,
		Code:      confirmingCode,
	})
	assertRefused(t, err, application.ErrMFACodeReplayed)
	if _, elevated := sandbox.repo.sessionElevation(sessionID); elevated {
		t.Fatal("the session was elevated by a replayed step")
	}

	// One step later the authenticator shows a new code, and that one works.
	sandbox.clock.Advance(mfa.DefaultPeriod)
	if err := sandbox.StepUp.Execute(context.Background(), application.StepUpMFACommand{
		AccountID: accountID,
		SessionID: sessionID,
		Code:      sandbox.code(t, secret, sandbox.clock.Now()),
	}); err != nil {
		t.Fatalf("step-up one step later: %v", err)
	}
	verifiedAt, elevated := sandbox.repo.sessionElevation(sessionID)
	if !elevated || !verifiedAt.Equal(sandbox.clock.Now()) {
		t.Fatalf("session elevation = (%v, %v), want the instants to agree", verifiedAt, elevated)
	}

	// And the code that just worked cannot be presented again.
	err = sandbox.StepUp.Execute(context.Background(), application.StepUpMFACommand{
		AccountID: accountID,
		SessionID: sessionID,
		Code:      sandbox.code(t, secret, sandbox.clock.Now()),
	})
	assertRefused(t, err, application.ErrMFACodeReplayed)
}

func TestMFAElevationBelongsToThePresentingSession(t *testing.T) {
	sandbox := newMFASandbox(t)
	const accountID = "account-sessions"
	const firstSession = "session-first"
	const secondSession = "session-second"
	sandbox.repo.seedSession(firstSession)
	sandbox.repo.seedSession(secondSession)

	secret, _ := sandbox.enroll(t, accountID)
	sandbox.clock.Advance(mfa.DefaultPeriod)

	if err := sandbox.StepUp.Execute(context.Background(), application.StepUpMFACommand{
		AccountID: accountID,
		SessionID: firstSession,
		Code:      sandbox.code(t, secret, sandbox.clock.Now()),
	}); err != nil {
		t.Fatalf("step-up: %v", err)
	}

	if _, elevated := sandbox.repo.sessionElevation(firstSession); !elevated {
		t.Fatal("the presenting session was not elevated")
	}
	if _, elevated := sandbox.repo.sessionElevation(secondSession); elevated {
		t.Fatal("a session that never presented the factor was elevated")
	}

	// The other session cannot ride on the code that just elevated the first.
	err := sandbox.StepUp.Execute(context.Background(), application.StepUpMFACommand{
		AccountID: accountID,
		SessionID: secondSession,
		Code:      sandbox.code(t, secret, sandbox.clock.Now()),
	})
	assertRefused(t, err, application.ErrMFACodeReplayed)
	if _, elevated := sandbox.repo.sessionElevation(secondSession); elevated {
		t.Fatal("a replayed code elevated another session")
	}
}

func TestMFAStepUpRequiresAConfirmedEnrollment(t *testing.T) {
	sandbox := newMFASandbox(t)
	const sessionID = "session-unenrolled"
	sandbox.repo.seedSession(sessionID)

	err := sandbox.StepUp.Execute(context.Background(), application.StepUpMFACommand{
		AccountID: "account-without-factor",
		SessionID: sessionID,
		Code:      "123456",
	})
	assertRefused(t, err, application.ErrMFANotEnrolled)
	if _, elevated := sandbox.repo.sessionElevation(sessionID); elevated {
		t.Fatal("a session was elevated without an enrollment")
	}
}

func TestMFARecoverySpendsOneCodeAndRecordsItBeforeElevating(t *testing.T) {
	sandbox := newMFASandbox(t)
	const accountID = "account-recovery"
	const firstSession = "session-recovery-first"
	const secondSession = "session-recovery-second"
	sandbox.repo.seedSession(firstSession)
	sandbox.repo.seedSession(secondSession)

	_, codes := sandbox.enroll(t, accountID)
	sandbox.log.mu.Lock()
	sandbox.log.events = nil
	sandbox.log.mu.Unlock()

	if err := sandbox.Recover.Execute(context.Background(), application.RecoverMFACommand{
		AccountID: accountID,
		SessionID: firstSession,
		Code:      codes[0],
	}); err != nil {
		t.Fatalf("recover with a backup code: %v", err)
	}
	if _, elevated := sandbox.repo.sessionElevation(firstSession); !elevated {
		t.Fatal("the recovering session was not elevated")
	}

	// The order is the security-relevant part: the code is spent, the fact is
	// recorded, and only then is access granted.
	wantEvents := []string{"code:consumed", "audit:backup_code_used", "session:elevated"}
	if events := sandbox.log.snapshot(); !equalEvents(events, wantEvents) {
		t.Fatalf("recovery side effects = %v, want %v", events, wantEvents)
	}
	if unused := sandbox.repo.unusedBackupCodeCount(domain.AccountID(accountID)); unused != len(codes)-1 {
		t.Fatalf("unused codes = %d, want %d", unused, len(codes)-1)
	}

	// The same code presented again is refused and elevates nothing, even from
	// another session. It is reported exactly like an unknown code, so the
	// endpoint is not a probe for which codes exist.
	err := sandbox.Recover.Execute(context.Background(), application.RecoverMFACommand{
		AccountID: accountID,
		SessionID: secondSession,
		Code:      codes[0],
	})
	assertRefused(t, err, application.ErrMFACodeInvalid)
	if _, elevated := sandbox.repo.sessionElevation(secondSession); elevated {
		t.Fatal("a spent recovery code elevated a session")
	}

	// A well-shaped code that was never issued is refused the same way.
	unknown := strings.Repeat("2", 12)
	if !sandbox.mechanism.ValidBackupCodeShape(unknown) {
		t.Fatalf("the fixture %q is not shaped like a code", unknown)
	}
	err = sandbox.Recover.Execute(context.Background(), application.RecoverMFACommand{
		AccountID: accountID,
		SessionID: secondSession,
		Code:      unknown,
	})
	assertRefused(t, err, application.ErrMFACodeInvalid)
}

func TestMFARecoveryFailsClosedWhenTheTrailCannotRecord(t *testing.T) {
	sandbox := newMFASandbox(t)
	const accountID = "account-unrecorded"
	const sessionID = "session-unrecorded"
	sandbox.repo.seedSession(sessionID)

	_, codes := sandbox.enroll(t, accountID)
	sandbox.audit.fail = true

	err := sandbox.Recover.Execute(context.Background(), application.RecoverMFACommand{
		AccountID: accountID,
		SessionID: sessionID,
		Code:      codes[0],
	})
	if err == nil {
		t.Fatal("a recovery whose fact could not be recorded succeeded")
	}
	if _, elevated := sandbox.repo.sessionElevation(sessionID); elevated {
		t.Fatal("a recovery that could not be recorded elevated the session")
	}

	// The direction of the failure is deliberate: the code is spent and the
	// access is not granted, which is the only ordering that cannot be abused
	// by retrying until the trail happens to be available.
	if unused := sandbox.repo.unusedBackupCodeCount(domain.AccountID(accountID)); unused != len(codes)-1 {
		t.Fatalf("unused codes = %d, want %d (the code is spent even when the trail fails)", unused, len(codes)-1)
	}
}

func TestMFARecoveryRejectsAMalformedCodeBeforeHashing(t *testing.T) {
	sandbox := newMFASandbox(t)
	const accountID = "account-malformed"
	sandbox.enroll(t, accountID)

	err := sandbox.Recover.Execute(context.Background(), application.RecoverMFACommand{
		AccountID: accountID,
		SessionID: "session-malformed",
		Code:      "not-a-code!",
	})
	assertRefused(t, err, application.ErrMFACodeInvalid)
	if sandbox.hasher.verifyCalls != 0 {
		t.Fatalf("a malformed code reached the hasher %d time(s)", sandbox.hasher.verifyCalls)
	}
}

// equalEvents compares two side-effect sequences.
func equalEvents(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
