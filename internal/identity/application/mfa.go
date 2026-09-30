package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

// The second factor of the administrative surface (P16-T05).
//
// The shape is the module's usual one: the application layer owns what happens
// (who may enroll, what a confirmation means, when a session becomes elevated,
// what is recorded), and the mechanism is a port. The port is what keeps the
// RFC-level detail — HMAC truncation, clock skew, AES-GCM — outside the module
// that decides policy, and it is why internal/platform/mfa can be tested
// against the RFC's vectors without knowing anything about accounts.

// MFA mechanism errors, in the vocabulary of this module. The adapter that
// implements MFAMechanism translates the mechanism's own errors into these, so
// the use cases never depend on the platform package.
var (
	// ErrMFACodeInvalid means the presented code is not valid now.
	ErrMFACodeInvalid = errors.New("identity: the second factor code is not valid")
	// ErrMFACodeReplayed means the code was already spent. It is a distinct
	// fact from a wrong code and is reported as such.
	ErrMFACodeReplayed = errors.New("identity: this second factor code was already used")
	// ErrMFASecretUnavailable means the stored secret could not be opened: a
	// key that changed, a row that was tampered with, a secret moved between
	// accounts. It never means "no second factor".
	ErrMFASecretUnavailable = errors.New("identity: the stored second factor secret cannot be read")
	// ErrMFAEnrollmentMissing means the account has no confirmed enrollment.
	ErrMFAEnrollmentMissing = errors.New("identity: no confirmed second factor enrollment")
	// ErrMFAAlreadyEnrolled means the account already has one, and starting
	// again would be a way to reset somebody else's factor.
	ErrMFAAlreadyEnrolled = errors.New("identity: the account already has a confirmed second factor")
	// ErrMFAPendingEnrollmentMissing means a confirmation arrived with no
	// enrollment to confirm.
	ErrMFAPendingEnrollmentMissing = errors.New("identity: no pending second factor enrollment")
	// ErrMFANotEnrolled means the account has no confirmed enrollment, so it
	// cannot step up and is not administrative whatever it holds.
	ErrMFANotEnrolled = errors.New("identity: the account has no confirmed second factor")
	// ErrMFAAuthenticationRequired means the caller has no account or no live
	// session. It is one refusal for both, because the two are the same fact
	// to the caller: there is nothing to act on.
	ErrMFAAuthenticationRequired = errors.New("identity: authentication is required")
	// ErrMFAUnavailable means the mechanism could not answer - a key that
	// changed, entropy that failed, a stored value that cannot be trusted. It
	// never means "wrong code".
	ErrMFAUnavailable = errors.New("identity: the second factor is unavailable")
	// ErrMFARecoveryUnrecorded means a recovery happened that the trail could
	// not record, which is the one outcome the recovery path refuses.
	ErrMFARecoveryUnrecorded = errors.New("identity: the recovery could not be recorded")
)

// MFAEnrollmentRecord is the stored second factor of one account.
type MFAEnrollmentRecord struct {
	// SecretSealed is the ciphertext of the shared secret.
	SecretSealed []byte
	// ConfirmedAt is when a code proved the secret, nil while pending.
	ConfirmedAt *time.Time
	// LastAcceptedStep is the highest code step already spent; -1 before any.
	LastAcceptedStep int64
}

// MFABackupCodeRecord is one stored recovery code.
type MFABackupCodeRecord struct {
	ID       string
	CodeHash string
}

// MFARepository stores the second factor. Every method that can be raced by two
// concurrent requests reports whether *this* call was the one that changed the
// row: the single-use guarantees are enforced by the statements, not by the
// order in which the use case happens to run.
type MFARepository interface {
	// GetMFAEnrollment returns the enrollment of an account, or
	// ErrMFAEnrollmentMissing.
	GetMFAEnrollment(ctx context.Context, accountID domain.AccountID) (*MFAEnrollmentRecord, error)

	// UpsertPendingMFAEnrollment stores a new pending secret, or returns
	// ErrMFAAlreadyEnrolled when the account already confirmed one.
	UpsertPendingMFAEnrollment(ctx context.Context, accountID domain.AccountID, secretSealed []byte) (*MFAEnrollmentRecord, error)

	// ConfirmMFAEnrollment records the confirmation and the step the
	// confirming code spent. It returns false when the enrollment was
	// confirmed by somebody else in the meantime.
	ConfirmMFAEnrollment(ctx context.Context, accountID domain.AccountID, confirmedAt time.Time, acceptedStep int64) (bool, error)

	// AdvanceMFAVerifiedStep moves the spent step forward. It returns false
	// when another request already spent this step or a later one, which is
	// how a replayed code is refused under concurrency.
	AdvanceMFAVerifiedStep(ctx context.Context, accountID domain.AccountID, step int64) (bool, error)

	// ReplaceMFABackupCodes replaces the whole set of recovery codes.
	ReplaceMFABackupCodes(ctx context.Context, accountID domain.AccountID, codeHashes []string) error

	// ListUnusedMFABackupCodes returns the codes that can still be spent.
	ListUnusedMFABackupCodes(ctx context.Context, accountID domain.AccountID) ([]MFABackupCodeRecord, error)

	// ConsumeMFABackupCode spends one code. It returns false when the code was
	// already spent, which is the single-use guarantee.
	ConsumeMFABackupCode(ctx context.Context, codeID string, usedAt time.Time) (bool, error)

	// MarkSessionMFAVerified elevates one session. It returns false when the
	// session no longer exists or was revoked.
	MarkSessionMFAVerified(ctx context.Context, sessionID domain.SessionID, verifiedAt time.Time) (bool, error)
}

// MFAMechanism is the second factor's mechanism: the RFC-level operations, with
// no knowledge of accounts, sessions or HTTP. It is implemented by an adapter
// over internal/platform/mfa.
type MFAMechanism interface {
	// GenerateSecret creates a new shared secret.
	GenerateSecret() ([]byte, error)
	// EncodeSecret renders a secret the way an authenticator application
	// expects to receive it.
	EncodeSecret(secret []byte) string
	// EnrollmentURI renders the URI an authenticator application reads.
	EnrollmentURI(account string, secret []byte) (string, error)
	// VerifyCode checks a code against a secret and reports the step it spent.
	// It returns ErrMFACodeInvalid or ErrMFACodeReplayed, in this module's
	// vocabulary.
	VerifyCode(secret []byte, code string, now time.Time, lastAcceptedStep int64) (int64, error)
	// SealSecret protects a secret for storage, bound to the account it
	// belongs to.
	SealSecret(accountID string, secret []byte) ([]byte, error)
	// OpenSecret reverses SealSecret. It returns ErrMFASecretUnavailable when
	// the value cannot be trusted.
	OpenSecret(accountID string, sealed []byte) ([]byte, error)
	// GenerateBackupCodes creates one-time recovery codes in their display
	// form.
	GenerateBackupCodes() ([]string, error)
	// NormalizeBackupCode canonicalises a code a person retyped.
	NormalizeBackupCode(code string) string
	// ValidBackupCodeShape reports whether a normalized code has the shape of
	// a generated one, so a malformed value never reaches the hasher.
	ValidBackupCodeShape(code string) bool
}

// MFAAudit records the administrative facts of the second factor. Recovery is
// the fact that must be recorded: an operator who lost their authenticator and
// used a code is exactly the event a reviewer needs to see.
type MFAAudit interface {
	// RecordMFAEnrolled records that an account confirmed an enrollment.
	RecordMFAEnrolled(ctx context.Context, accountID string, occurredAt time.Time) error
	// RecordMFABackupCodeUsed records that a recovery code was spent.
	RecordMFABackupCodeUsed(ctx context.Context, accountID string, occurredAt time.Time) error
}

// BeginMFAEnrollmentCommand starts an enrollment for an authenticated account.
type BeginMFAEnrollmentCommand struct {
	AccountID string
}

// BeginMFAEnrollmentResult carries the material shown exactly once: the secret
// the authenticator needs, and the URI that encodes it.
type BeginMFAEnrollmentResult struct {
	Secret string
	URI    string
}

// BeginMFAEnrollmentUseCase starts an enrollment.
//
// A pending enrollment can be restarted (a user who lost the QR code before
// confirming is not stuck), while a confirmed one cannot: refusing there is
// what stops "start enrollment" from being a way to replace a working second
// factor with one the caller controls.
type BeginMFAEnrollmentUseCase struct {
	enrollments MFARepository
	mechanism   MFAMechanism
}

// NewBeginMFAEnrollmentUseCase constructs the use case.
func NewBeginMFAEnrollmentUseCase(enrollments MFARepository, mechanism MFAMechanism) *BeginMFAEnrollmentUseCase {
	return &BeginMFAEnrollmentUseCase{enrollments: enrollments, mechanism: mechanism}
}

// Execute begins the enrollment.
func (useCase *BeginMFAEnrollmentUseCase) Execute(ctx context.Context, command BeginMFAEnrollmentCommand) (*BeginMFAEnrollmentResult, error) {
	accountID := domain.AccountID(command.AccountID)
	if accountID.IsZero() {
		return nil, ErrMFAAuthenticationRequired
	}

	secret, err := useCase.mechanism.GenerateSecret()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMFAUnavailable, err)
	}
	sealed, err := useCase.mechanism.SealSecret(command.AccountID, secret)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMFAUnavailable, err)
	}

	if _, err := useCase.enrollments.UpsertPendingMFAEnrollment(ctx, accountID, sealed); err != nil {
		if errors.Is(err, ErrMFAAlreadyEnrolled) {
			return nil, ErrMFAAlreadyEnrolled
		}
		return nil, fmt.Errorf("store pending second factor: %w", err)
	}

	uri, err := useCase.mechanism.EnrollmentURI(command.AccountID, secret)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMFAUnavailable, err)
	}
	return &BeginMFAEnrollmentResult{Secret: useCase.mechanism.EncodeSecret(secret), URI: uri}, nil
}

// ConfirmMFAEnrollmentCommand confirms a pending enrollment with a code.
type ConfirmMFAEnrollmentCommand struct {
	AccountID string
	Code      string
}

// ConfirmMFAEnrollmentResult carries the recovery codes, shown exactly once.
type ConfirmMFAEnrollmentResult struct {
	BackupCodes []string
}

// ConfirmMFAEnrollmentUseCase confirms an enrollment: a code from the
// authenticator proves the secret was transferred, and the confirmation issues
// the recovery codes and spends the code that confirmed it.
type ConfirmMFAEnrollmentUseCase struct {
	enrollments MFARepository
	mechanism   MFAMechanism
	hasher      PasswordHasher
	clock       Clock
	audit       MFAAudit
}

// NewConfirmMFAEnrollmentUseCase constructs the use case.
func NewConfirmMFAEnrollmentUseCase(enrollments MFARepository, mechanism MFAMechanism, hasher PasswordHasher, clock Clock, audit MFAAudit) *ConfirmMFAEnrollmentUseCase {
	return &ConfirmMFAEnrollmentUseCase{enrollments: enrollments, mechanism: mechanism, hasher: hasher, clock: clock, audit: audit}
}

// Execute confirms the enrollment.
func (useCase *ConfirmMFAEnrollmentUseCase) Execute(ctx context.Context, command ConfirmMFAEnrollmentCommand) (*ConfirmMFAEnrollmentResult, error) {
	accountID := domain.AccountID(command.AccountID)
	if accountID.IsZero() {
		return nil, ErrMFAAuthenticationRequired
	}

	enrollment, err := useCase.enrollments.GetMFAEnrollment(ctx, accountID)
	if err != nil {
		if errors.Is(err, ErrMFAEnrollmentMissing) {
			return nil, ErrMFAEnrollmentMissing
		}
		return nil, fmt.Errorf("read second factor: %w", err)
	}
	if enrollment.ConfirmedAt != nil {
		return nil, ErrMFAAlreadyEnrolled
	}

	secret, err := useCase.mechanism.OpenSecret(command.AccountID, enrollment.SecretSealed)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMFAUnavailable, err)
	}

	now := useCase.clock.Now().UTC()
	step, err := useCase.mechanism.VerifyCode(secret, command.Code, now, enrollment.LastAcceptedStep)
	if err != nil {
		return nil, mfaRefusal(err)
	}

	confirmed, err := useCase.enrollments.ConfirmMFAEnrollment(ctx, accountID, now, step)
	if err != nil {
		return nil, fmt.Errorf("confirm second factor: %w", err)
	}
	if !confirmed {
		return nil, ErrMFAAlreadyEnrolled
	}

	codes, err := useCase.mechanism.GenerateBackupCodes()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMFAUnavailable, err)
	}
	hashes := make([]string, 0, len(codes))
	for _, code := range codes {
		hash, err := useCase.hasher.HashPassword(useCase.mechanism.NormalizeBackupCode(code))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrMFAUnavailable, err)
		}
		hashes = append(hashes, hash)
	}
	if err := useCase.enrollments.ReplaceMFABackupCodes(ctx, accountID, hashes); err != nil {
		return nil, fmt.Errorf("store recovery codes: %w", err)
	}

	// The enrollment is an administrative fact: an operator gaining a second
	// factor is what a reviewer needs to be able to find later. A failure to
	// record it is reported, but the enrollment itself is already committed
	// and is not rolled back: the code was spent, and telling the user the
	// enrollment failed would be a lie.
	if useCase.audit != nil {
		if err := useCase.audit.RecordMFAEnrolled(ctx, command.AccountID, now); err != nil {
			return nil, fmt.Errorf("record the enrollment: %w", err)
		}
	}

	return &ConfirmMFAEnrollmentResult{BackupCodes: codes}, nil
}

// StepUpMFACommand elevates a session with a code.
type StepUpMFACommand struct {
	AccountID string
	SessionID string
	Code      string
}

// StepUpMFAUseCase verifies the second factor and elevates the session that
// presented it.
//
// The elevation is a property of the session, not of the account: an account
// with a confirmed enrollment still has sessions that never presented it, and
// those are the sessions an administrative gate must refuse.
type StepUpMFAUseCase struct {
	enrollments MFARepository
	mechanism   MFAMechanism
	clock       Clock
}

// NewStepUpMFAUseCase constructs the use case.
func NewStepUpMFAUseCase(enrollments MFARepository, mechanism MFAMechanism, clock Clock) *StepUpMFAUseCase {
	return &StepUpMFAUseCase{enrollments: enrollments, mechanism: mechanism, clock: clock}
}

// Execute verifies the code and elevates the session.
func (useCase *StepUpMFAUseCase) Execute(ctx context.Context, command StepUpMFACommand) error {
	accountID := domain.AccountID(command.AccountID)
	sessionID := domain.SessionID(command.SessionID)
	if accountID.IsZero() || sessionID.IsZero() {
		return ErrMFAAuthenticationRequired
	}

	enrollment, err := useCase.enrollments.GetMFAEnrollment(ctx, accountID)
	if err != nil {
		if errors.Is(err, ErrMFAEnrollmentMissing) {
			return ErrMFANotEnrolled
		}
		return fmt.Errorf("read second factor: %w", err)
	}
	if enrollment.ConfirmedAt == nil {
		return ErrMFANotEnrolled
	}

	secret, err := useCase.mechanism.OpenSecret(command.AccountID, enrollment.SecretSealed)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMFAUnavailable, err)
	}

	now := useCase.clock.Now().UTC()
	step, err := useCase.mechanism.VerifyCode(secret, command.Code, now, enrollment.LastAcceptedStep)
	if err != nil {
		return mfaRefusal(err)
	}

	// Spending the step and elevating the session are two statements because
	// they are two facts: the code is spent whether or not the session was
	// still there. A step that a concurrent request already spent is a replay,
	// even though the code itself was valid a moment ago.
	advanced, err := useCase.enrollments.AdvanceMFAVerifiedStep(ctx, accountID, step)
	if err != nil {
		return fmt.Errorf("record the spent step: %w", err)
	}
	if !advanced {
		return ErrMFACodeReplayed
	}

	elevated, err := useCase.enrollments.MarkSessionMFAVerified(ctx, sessionID, now)
	if err != nil {
		return fmt.Errorf("elevate the session: %w", err)
	}
	if !elevated {
		return ErrMFAAuthenticationRequired
	}
	return nil
}

// RecoverMFACommand spends a one-time recovery code.
type RecoverMFACommand struct {
	AccountID string
	SessionID string
	Code      string
}

// RecoverMFAUseCase spends a backup code: the path of an operator whose
// authenticator is gone.
//
// The order of the last three steps is the security-relevant part. The code is
// consumed first, so a code cannot be spent twice even if the audit or the
// elevation fails; the audit is recorded before the session is elevated, so a
// recovery that cannot be recorded cannot succeed; and only then is the session
// elevated. A failure after the consumption spends the code without granting
// access, which is the direction that fails closed.
type RecoverMFAUseCase struct {
	enrollments MFARepository
	mechanism   MFAMechanism
	hasher      PasswordHasher
	clock       Clock
	audit       MFAAudit
}

// NewRecoverMFAUseCase constructs the use case.
func NewRecoverMFAUseCase(enrollments MFARepository, mechanism MFAMechanism, hasher PasswordHasher, clock Clock, audit MFAAudit) *RecoverMFAUseCase {
	return &RecoverMFAUseCase{enrollments: enrollments, mechanism: mechanism, hasher: hasher, clock: clock, audit: audit}
}

// Execute spends one recovery code and elevates the session.
func (useCase *RecoverMFAUseCase) Execute(ctx context.Context, command RecoverMFACommand) error {
	accountID := domain.AccountID(command.AccountID)
	sessionID := domain.SessionID(command.SessionID)
	if accountID.IsZero() || sessionID.IsZero() {
		return ErrMFAAuthenticationRequired
	}

	normalized := useCase.mechanism.NormalizeBackupCode(command.Code)
	if !useCase.mechanism.ValidBackupCodeShape(normalized) {
		// A value that cannot be a code is refused before any hashing: the
		// recovery path must not become an oracle for the stored hashes, and
		// hashing is the expensive part.
		return ErrMFACodeInvalid
	}

	codes, err := useCase.enrollments.ListUnusedMFABackupCodes(ctx, accountID)
	if err != nil {
		return fmt.Errorf("read recovery codes: %w", err)
	}

	matched := ""
	for _, record := range codes {
		ok, err := useCase.hasher.VerifyPassword(normalized, record.CodeHash)
		if err != nil {
			continue
		}
		if ok {
			matched = record.ID
			break
		}
	}
	if matched == "" {
		return ErrMFACodeInvalid
	}

	now := useCase.clock.Now().UTC()
	consumed, err := useCase.enrollments.ConsumeMFABackupCode(ctx, matched, now)
	if err != nil {
		return fmt.Errorf("spend the recovery code: %w", err)
	}
	if !consumed {
		// Another request spent the same code between the read and the update.
		// The refusal is the same as an unknown code: telling the two apart
		// would turn the endpoint into a probe for which codes exist.
		return ErrMFACodeInvalid
	}

	if useCase.audit == nil {
		// A recovery that cannot be attributed is exactly what the audit trail
		// exists to prevent, so the recovery fails closed instead of
		// succeeding silently.
		return ErrMFARecoveryUnrecorded
	}
	if err := useCase.audit.RecordMFABackupCodeUsed(ctx, command.AccountID, now); err != nil {
		return fmt.Errorf("%w: %v", ErrMFARecoveryUnrecorded, err)
	}

	elevated, err := useCase.enrollments.MarkSessionMFAVerified(ctx, sessionID, now)
	if err != nil {
		return fmt.Errorf("elevate the session: %w", err)
	}
	if !elevated {
		return ErrMFAAuthenticationRequired
	}
	return nil
}

// mfaRefusal translates a mechanism error into the module's stable problem.
func mfaRefusal(err error) error {
	switch {
	case errors.Is(err, ErrMFACodeReplayed):
		return ErrMFACodeReplayed
	case errors.Is(err, ErrMFACodeInvalid):
		return ErrMFACodeInvalid
	default:
		// A secret that cannot be read and a mechanism that cannot answer are
		// the same refusal to the caller: we could not verify, and the reason
		// belongs in the log rather than in the response.
		return fmt.Errorf("%w: %v", ErrMFAUnavailable, err)
	}
}
