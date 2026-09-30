package application_test

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/mfamechanism"
	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/mfa"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
)

// Independent reference model of the identity lifecycles (P24-T03).
//
// The system under test is the real application code (register, issue,
// verify, login, authenticate, rotate, logout, reset, MFA) wired to the
// in-memory fakes of this package. The oracle below is a separate,
// deliberately small state machine written from the documented rules: it
// never calls domain constructors, domain transitions or application use
// cases. It predicts allow/deny from its own state, the runner compares
// every outcome, and the observable state (account statuses, session
// liveness, elevation, audit counts) is reconciled after every command.
//
// What the oracle tracks, per account: lifecycle status, current password,
// live and dead single-use tokens, live and dead sessions with the instants
// needed to recompute expiry, and the MFA phase with spent codes. Token and
// session VALUES are learned from observed system outputs (issued emails,
// login results) the way a model test learns allocated identifiers; the
// TRANSITION RULES are the oracle's own.

// modelStatus is the oracle's private lifecycle vocabulary. It mirrors the
// documented states without importing their behavior.
type modelStatus string

const (
	modelPending   modelStatus = "pending"
	modelActive    modelStatus = "active"
	modelSuspended modelStatus = "suspended"
	modelDeleted   modelStatus = "deleted"
)

// modelToken is one single-use token the oracle has seen issued. Liveness
// is membership: live tables hold what the system may still accept, dead
// tables what replay probes draw from.
type modelToken struct {
	ownerEmail string
	expiresAt  time.Time
}

// modelSession is one session the oracle has seen issued.
type modelSession struct {
	ownerEmail      string
	revoked         bool
	createdAt       time.Time
	lastSeenAt      time.Time
	storedExpiresAt time.Time
}

// modelSlot is everything the oracle believes about one account identity,
// keyed by its current email.
type modelSlot struct {
	exists   bool
	id       string
	status   modelStatus
	password string
	email    string
}

// modelOracle is the independent reference state.
type modelOracle struct {
	slots      map[string]*modelSlot
	verifyLive map[string]*modelToken
	verifyDead map[string]bool
	resetLive  map[string]*modelToken
	resetDead  map[string]bool
	sessions   map[string]*modelSession
	authGrants int
	now        func() time.Time
	sessionPol domain.SessionPolicy
	verifyLife time.Duration
	resetLife  time.Duration
}

// newModelOracle builds an oracle over one clock. Lifetimes are read from
// the delivered policies: values travel, behavior does not.
func newModelOracle(now func() time.Time) *modelOracle {
	return &modelOracle{
		slots:      make(map[string]*modelSlot),
		verifyLive: make(map[string]*modelToken),
		verifyDead: make(map[string]bool),
		resetLive:  make(map[string]*modelToken),
		resetDead:  make(map[string]bool),
		sessions:   make(map[string]*modelSession),
		now:        now,
		sessionPol: domain.DefaultSessionPolicy(),
		verifyLife: domain.DefaultVerificationPolicy().TokenLifetime,
		resetLife:  domain.DefaultPasswordResetPolicy().TokenLifetime,
	}
}

// modelExpired recomputes session expiry from tracked instants, the same
// question SessionPolicy.IsExpired answers, derived here from the policy
// values and the documented rule (24h idle or 14d absolute or stored
// deadline, whichever bites first).
func (o *modelOracle) modelExpired(s *modelSession) bool {
	now := o.now()
	idleCut := s.lastSeenAt.Add(o.sessionPol.IdleTimeout)
	absoluteCut := s.createdAt.Add(o.sessionPol.AbsoluteLifetime)
	deadline := idleCut
	if absoluteCut.Before(deadline) {
		deadline = absoluteCut
	}
	if !now.After(deadline) && !now.After(s.storedExpiresAt) {
		return false
	}
	return true
}

// modelWorld wires the real use cases to in-memory fakes and one explicit
// clock. Passwords are instant (fake hasher); randomness is the registered
// deterministic stream, so a seed replays the whole world.
type modelWorld struct {
	ctx        context.Context
	clock      *fakeClock
	sender     *memoryEmailSender
	accounts   *inMemoryAccountRepo
	verifyRepo *inMemoryTokenRepo
	resetRepo  *inMemoryResetTokenRepo
	sessions   *inMemorySessionRepo
	creds      *inMemoryCredentialRepo
	register   *application.RegisterAccountUseCase
	issue      *application.IssueEmailVerificationUseCase
	verify     *application.VerifyEmailUseCase
	login      *application.LoginUseCase
	auth       *application.AuthenticateSessionUseCase
	rotate     *application.RotateSessionUseCase
	logout     *application.LogoutUseCase
	logoutAll  *application.LogoutAllUseCase
	reqReset   *application.RequestPasswordResetUseCase
	doneReset  *application.CompletePasswordResetUseCase
}

// newModelWorld builds one fresh world deterministically from a seed.
func newModelWorld(seed int64) *modelWorld {
	clock := &fakeClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	rnd := testsource.NewRandom(seed)
	sender := &memoryEmailSender{}
	accounts := newInMemoryAccountRepo()
	verifyRepo := newInMemoryTokenRepo()
	resetRepo := newInMemoryResetTokenRepo()
	sessions := newInMemorySessionRepo()
	creds := newInMemoryCredentialRepo()
	hasher := fakePasswordHasher{}
	sessionPolicy := domain.DefaultSessionPolicy()
	return &modelWorld{
		ctx:        context.Background(),
		clock:      clock,
		sender:     sender,
		accounts:   accounts,
		verifyRepo: verifyRepo,
		resetRepo:  resetRepo,
		sessions:   sessions,
		creds:      creds,
		register:   application.NewRegisterAccountUseCase(accounts, verifyRepo, hasher, sender, clock, rnd, domain.DefaultVerificationPolicy()),
		issue:      application.NewIssueEmailVerificationUseCase(accounts, verifyRepo, sender, clock, rnd, domain.DefaultVerificationPolicy()),
		verify:     application.NewVerifyEmailUseCase(accounts, verifyRepo, clock),
		login:      application.NewLoginUseCase(accounts, creds, sessions, hasher, clock, rnd, sessionPolicy),
		auth:       application.NewAuthenticateSessionUseCase(accounts, sessions, clock, sessionPolicy, application.DefaultTouchThreshold),
		rotate:     application.NewRotateSessionUseCase(accounts, sessions, clock, rnd, sessionPolicy),
		logout:     application.NewLogoutUseCase(sessions),
		logoutAll:  application.NewLogoutAllUseCase(sessions),
		reqReset:   application.NewRequestPasswordResetUseCase(accounts, resetRepo, sender, clock, rnd, domain.DefaultPasswordResetPolicy()),
		doneReset:  application.NewCompletePasswordResetUseCase(accounts, creds, resetRepo, verifyRepo, sessions, hasher, sender, clock),
	}
}

// modelEmails returns the sender outbox length, the only observable that
// tells issuance apart from suppressed anti-enumeration answers.
func (w *modelWorld) mailCount() int {
	w.sender.mu.Lock()
	defer w.sender.mu.Unlock()
	return len(w.sender.emails)
}

// modelLastToken returns the most recently emitted raw token.
func (w *modelWorld) lastToken() string {
	return w.sender.LastToken()
}

// modelAccountStatus reads the stored lifecycle status of one account.
func (w *modelWorld) accountStatus(t *testing.T, id string) modelStatus {
	t.Helper()
	acc, err := w.accounts.GetAccountByID(w.ctx, domain.AccountID(id))
	if err != nil {
		t.Fatalf("model world lost account %s: %v", id, err)
	}
	switch acc.Status() {
	case domain.AccountStatusPending:
		return modelPending
	case domain.AccountStatusActive:
		return modelActive
	case domain.AccountStatusSuspended:
		return modelSuspended
	case domain.AccountStatusDeleted:
		return modelDeleted
	default:
		t.Fatalf("account %s carries an unknown status %q", id, acc.Status())
		return ""
	}
}

// modelDivergence is one place where the system and the oracle disagreed.
type modelDivergence struct {
	step int
	op   string
	want bool
	got  bool
}

// modelRunner carries one deterministic sequence: the world, the oracle,
// the drawn commands and every divergence found.
type modelRunner struct {
	t        *testing.T
	world    *modelWorld
	oracle   *modelOracle
	step     int
	diverged []modelDivergence
	trace    []string
	ghostSeq int
	// retired marks addresses a changeEmail vacated. Re-registering one
	// would mint a colliding derived identifier in the in-memory store
	// (production mints UUIDs), so registration redirects them instead.
	retired map[string]bool
}

// modelStrongPassword is the fixed strong credential per identity. Fixed
// values keep sequences replayable: randomness selects commands, never the
// secrets that decide them.
func modelStrongPassword(email string) string { return "StrongPass-" + email + "-12345!" }

// check compares one outcome with the oracle prediction and records the
// divergence instead of stopping: one red sequence must show every place it
// disagrees, and the caller fails the test when the list is not empty.
func (r *modelRunner) check(op string, wantAccept bool, err error) bool {
	r.t.Helper()
	gotAccept := err == nil
	r.trace = append(r.trace, op)
	if gotAccept != wantAccept {
		r.diverged = append(r.diverged, modelDivergence{step: r.step, op: op, want: wantAccept, got: gotAccept})
		return false
	}
	return true
}

// syncAccounts reconciles every tracked lifecycle status with the stored
// rows: the comparison of observed state after every command.
func (r *modelRunner) syncAccounts() {
	r.t.Helper()
	for email, slot := range r.oracle.slots {
		if !slot.exists {
			continue
		}
		if got := r.world.accountStatus(r.t, slot.id); got != slot.status {
			r.diverged = append(r.diverged, modelDivergence{step: r.step, op: "status:" + email, want: true, got: false})
		}
	}
}

// syncVerifyIssuance learns a verification token the system just emitted.
// Issuance is observed (the outbox grew); the rule it obeys — pending
// accounts rotate exactly one live token — stays the oracle's own.
func (r *modelRunner) syncVerifyIssuance(email string) {
	r.t.Helper()
	for token, tok := range r.oracle.verifyLive {
		if tok.ownerEmail == email {
			r.oracle.verifyDead[token] = true
			delete(r.oracle.verifyLive, token)
		}
	}
	raw := r.world.lastToken()
	r.oracle.verifyLive[raw] = &modelToken{ownerEmail: email, expiresAt: r.world.clock.Now().Add(r.oracle.verifyLife)}
}

// syncResetIssuance learns a fresh recovery token the same way.
func (r *modelRunner) syncResetIssuance(email string) {
	r.t.Helper()
	for token, tok := range r.oracle.resetLive {
		if tok.ownerEmail == email {
			r.oracle.resetDead[token] = true
			delete(r.oracle.resetLive, token)
		}
	}
	raw := r.world.lastToken()
	r.oracle.resetLive[raw] = &modelToken{ownerEmail: email, expiresAt: r.world.clock.Now().Add(r.oracle.resetLife)}
}

// renameOwner moves every tracked token and session of one identity to its
// fresh address after a changeEmail: the account is the same, only the key
// the oracle files it under changed.
func (o *modelOracle) renameOwner(old, fresh string) {
	for _, tok := range o.verifyLive {
		if tok.ownerEmail == old {
			tok.ownerEmail = fresh
		}
	}
	for _, tok := range o.resetLive {
		if tok.ownerEmail == old {
			tok.ownerEmail = fresh
		}
	}
	for _, sess := range o.sessions {
		if sess.ownerEmail == old {
			sess.ownerEmail = fresh
		}
	}
}

// opRegister predicts and runs one registration. Duplicates succeed without
// creating: only a pending duplicate re-emits, which the outbox proves.
func (r *modelRunner) opRegister(email, password string) {
	r.step++
	// A vacated address would collide on the store's derived identifier,
	// so registration transparently targets a live identity instead. The
	// redirect itself is deterministic and identical on replay.
	if _, known := r.oracle.slots[email]; !known && r.retired[email] {
		email = "ghost@arena.local"
		if _, known := r.oracle.slots[email]; !known && r.retired[email] {
			for k := range r.oracle.slots {
				email = k
				break
			}
		}
	}
	_, known := r.oracle.slots[email]
	wantAccept := modelEmailLooksValid(email) && len(password) >= 8
	before := r.world.mailCount()
	res, err := r.world.register.Execute(r.world.ctx, application.RegisterAccountCommand{Email: email, Password: password})
	_ = res
	if !r.check("register:"+email, wantAccept, err) {
		return
	}
	if !wantAccept {
		return
	}
	if !known {
		id := ""
		if res != nil {
			id = res.AccountID
		}
		r.oracle.slots[email] = &modelSlot{exists: true, id: id, status: modelPending, password: password, email: email}
		r.world.creds.credentials[id] = &application.PasswordCredentialRecord{AccountID: domain.AccountID(id), PasswordHash: "hashed_" + password, Algorithm: "argon2id", Version: 1}
	}
	after := r.world.mailCount()
	slotNow := r.oracle.slots[email]
	if slotNow.status == modelPending && after != before+1 {
		r.diverged = append(r.diverged, modelDivergence{step: r.step, op: "register-mail:" + email, want: true, got: false})
		return
	}
	if slotNow.status == modelPending {
		r.syncVerifyIssuance(email)
	} else if after != before {
		r.diverged = append(r.diverged, modelDivergence{step: r.step, op: "register-silence:" + email, want: true, got: false})
	}
	r.syncAccounts()
}

// modelEmailLooksValid is the generator's contract, not the parser: every
// address the generator marks valid already registered successfully once,
// and every address it marks invalid is a fixed refusal fixture.
func modelEmailLooksValid(email string) bool {
	switch email {
	case "", "   ", "not-an-email", "user@.com", "@invalid.com":
		return false
	default:
		return true
	}
}

// opIssue predicts and runs one verification issuance. Garbage addresses
// are refused outright; every valid address answers nil by design
// (anti-enumeration), so the oracle watches the outbox instead of the error.
func (r *modelRunner) opIssue(email string) {
	r.step++
	r.trace = append(r.trace, "issue:"+email)
	if !modelEmailLooksValid(email) {
		err := r.world.issue.Execute(r.world.ctx, application.IssueEmailVerificationCommand{Email: email})
		r.check("issue-garbage:"+email, false, err)
		return
	}
	slot, known := r.oracle.slots[email]
	wantMail := known && slot.status == modelPending
	before := r.world.mailCount()
	err := r.world.issue.Execute(r.world.ctx, application.IssueEmailVerificationCommand{Email: email})
	if err != nil {
		r.check("issue:"+email, true, err)
		return
	}
	after := r.world.mailCount()
	if wantMail {
		if after != before+1 {
			r.diverged = append(r.diverged, modelDivergence{step: r.step, op: "issue-mail:" + email, want: true, got: false})
			return
		}
		r.syncVerifyIssuance(email)
	} else if after != before {
		r.diverged = append(r.diverged, modelDivergence{step: r.step, op: "issue-silence:" + email, want: true, got: false})
	}
	r.syncAccounts()
}

// opVerify predicts and runs one verification with the given token.
func (r *modelRunner) opVerify(raw string) {
	r.step++
	wantAccept := false
	if tok, ok := r.oracle.verifyLive[raw]; ok {
		slot, known := r.oracle.slots[tok.ownerEmail]
		if !known {
			r.t.Fatalf("oracle orphaned a live token of %q", tok.ownerEmail)
		}
		if !r.world.clock.Now().After(tok.expiresAt) && slot.status == modelPending {
			wantAccept = true
		}
	}
	err := r.world.verify.Execute(r.world.ctx, application.VerifyEmailCommand{Token: raw})
	if !r.check("verify", wantAccept, err) {
		return
	}
	if wantAccept {
		tok := r.oracle.verifyLive[raw]
		slot := r.oracle.slots[tok.ownerEmail]
		slot.status = modelActive
		delete(r.oracle.verifyLive, raw)
		r.oracle.verifyDead[raw] = true
	}
	r.syncAccounts()
}

// opLogin predicts and runs one login, auditing the grant: an accepted
// login must name an eligible account and mint a live session.
func (r *modelRunner) opLogin(email, password string) {
	r.step++
	slot, known := r.oracle.slots[email]
	wantAccept := known && slot.password == password && slot.status == modelActive
	res, err := r.world.login.Execute(r.world.ctx, application.LoginCommand{Email: email, Password: password, IPAddress: "203.0.113.10", UserAgent: "ModelClient/1.0"})
	if !r.check("login:"+email, wantAccept, err) {
		return
	}
	if !wantAccept {
		return
	}
	if res == nil || res.Session == nil || res.RawToken == "" {
		r.diverged = append(r.diverged, modelDivergence{step: r.step, op: "login-shape:" + email, want: true, got: false})
		return
	}
	// The grant audit: eligibility and freshness at the granting instant.
	if slot.status != modelActive {
		r.diverged = append(r.diverged, modelDivergence{step: r.step, op: "login-grant-eligibility:" + email, want: true, got: false})
	}
	r.oracle.sessions[res.RawToken] = &modelSession{
		ownerEmail:      email,
		createdAt:       res.Session.CreatedAt(),
		lastSeenAt:      res.Session.LastSeenAt(),
		storedExpiresAt: res.Session.ExpiresAt(),
	}
	r.oracle.authGrants++
	r.syncAccounts()
}

// tokenOf selects a session token by role: a live one, a dead one, garbage,
// or a live one owned by the other identity (cross-talk probe).
func (r *modelRunner) tokenOf(rnd *testsource.Random, email string, cross bool) string {
	live := []string{}
	dead := []string{}
	for raw, sess := range r.oracle.sessions {
		if sess.ownerEmail == email == cross {
			continue
		}
		if sess.revoked || r.oracle.modelExpired(sess) {
			dead = append(dead, raw)
		} else {
			live = append(live, raw)
		}
	}
	switch rnd.Int64n(4) {
	case 0:
		if len(live) > 0 {
			return live[rnd.Int64n(int64(len(live)))]
		}
		return "not-a-token"
	case 1:
		if len(dead) > 0 {
			return dead[rnd.Int64n(int64(len(dead)))]
		}
		return "not-a-token"
	default:
		return "not-a-token"
	}
}

// opAuthenticate predicts and runs one session authentication, mirroring
// the touch rule so expiry keeps agreeing after throttled writes.
func (r *modelRunner) opAuthenticate(raw string) {
	r.step++
	sess, known := r.oracle.sessions[raw]
	wantAccept := false
	if known && !sess.revoked && !r.oracle.modelExpired(sess) {
		if slot, ok := r.oracle.slots[sess.ownerEmail]; ok && slot.status == modelActive {
			wantAccept = true
		}
	}
	res, err := r.world.auth.Execute(r.world.ctx, application.AuthenticateSessionCommand{RawToken: raw})
	if !r.check("authenticate", wantAccept, err) {
		return
	}
	if !wantAccept {
		return
	}
	if res == nil || res.Session == nil {
		r.diverged = append(r.diverged, modelDivergence{step: r.step, op: "authenticate-shape", want: true, got: false})
		return
	}
	if slot := r.oracle.slots[sess.ownerEmail]; slot.status != modelActive {
		r.diverged = append(r.diverged, modelDivergence{step: r.step, op: "authenticate-grant-eligibility", want: true, got: false})
	}
	now := r.world.clock.Now()
	if !now.Before(sess.lastSeenAt.Add(application.DefaultTouchThreshold)) {
		sess.lastSeenAt = now
		sess.storedExpiresAt = res.Session.ExpiresAt()
	}
	r.oracle.authGrants++
	r.syncAccounts()
}

// opRotate predicts and runs one rotation: the old token must die and the
// fresh one must be live, so replaying the old token is the next probe.
func (r *modelRunner) opRotate(raw string) {
	r.step++
	sess, known := r.oracle.sessions[raw]
	wantAccept := false
	if known && !sess.revoked && !r.oracle.modelExpired(sess) {
		if slot, ok := r.oracle.slots[sess.ownerEmail]; ok && slot.status == modelActive {
			wantAccept = true
		}
	}
	res, err := r.world.rotate.Execute(r.world.ctx, application.RotateSessionCommand{CurrentRawToken: raw, IPAddress: "203.0.113.10", UserAgent: "ModelClient/1.0"})
	if !r.check("rotate", wantAccept, err) {
		return
	}
	if !wantAccept {
		return
	}
	if res == nil || res.RawToken == "" || res.RawToken == raw {
		r.diverged = append(r.diverged, modelDivergence{step: r.step, op: "rotate-shape", want: true, got: false})
		return
	}
	sess.revoked = true
	r.oracle.sessions[res.RawToken] = &modelSession{
		ownerEmail:      sess.ownerEmail,
		createdAt:       res.Session.CreatedAt(),
		lastSeenAt:      res.Session.LastSeenAt(),
		storedExpiresAt: res.Session.ExpiresAt(),
	}
	r.syncAccounts()
}

// opLogout ends one session by token; logout is idempotent by design, so
// the oracle always accepts and marks what it knew.
func (r *modelRunner) opLogout(raw string) {
	r.step++
	err := r.world.logout.Execute(r.world.ctx, application.LogoutCommand{RawToken: raw})
	if !r.check("logout", true, err) {
		return
	}
	if sess, ok := r.oracle.sessions[raw]; ok {
		sess.revoked = true
	}
	r.syncAccounts()
}

// opLogoutAll ends every live session of one identity.
func (r *modelRunner) opLogoutAll(email string) {
	r.step++
	slot, known := r.oracle.slots[email]
	if !known {
		return
	}
	err := r.world.logoutAll.Execute(r.world.ctx, application.LogoutAllCommand{AccountID: domain.AccountID(slot.id)})
	if !r.check("logout-all:"+email, true, err) {
		return
	}
	for _, sess := range r.oracle.sessions {
		if sess.ownerEmail == email {
			sess.revoked = true
		}
	}
	r.syncAccounts()
}

// opRequestReset predicts issuance the way opIssue does: silence unless the
// account is pending or active.
func (r *modelRunner) opRequestReset(email string) {
	r.step++
	r.trace = append(r.trace, "request-reset:"+email)
	slot, known := r.oracle.slots[email]
	wantMail := known && (slot.status == modelPending || slot.status == modelActive)
	before := r.world.mailCount()
	err := r.world.reqReset.Execute(r.world.ctx, application.RequestPasswordResetCommand{Email: email})
	if err != nil && modelEmailLooksValid(email) {
		r.check("request-reset:"+email, true, err)
		return
	}
	after := r.world.mailCount()
	if wantMail {
		if after != before+1 {
			r.diverged = append(r.diverged, modelDivergence{step: r.step, op: "request-reset-mail:" + email, want: true, got: false})
			return
		}
		r.syncResetIssuance(email)
	} else if after != before {
		r.diverged = append(r.diverged, modelDivergence{step: r.step, op: "request-reset-silence:" + email, want: true, got: false})
	}
	r.syncAccounts()
}

// resetPasswordFor completes a reset with a fixed fresh secret per
// sequence, so the oracle always knows the current password.
func (r *modelRunner) resetPasswordFor(email string) string { return "ResetStrong-" + email + "-999!" }

// opCompleteReset predicts and runs one recovery: on success every session
// dies, every token of the account dies, and the password moves. A weak
// replacement is refused before any lookup, leaving a live token live.
func (r *modelRunner) opCompleteReset(raw string, weak bool) {
	r.step++
	r.trace = append(r.trace, "complete-reset")
	wantAccept := false
	var owner *modelSlot
	var ownerEmail string
	if tok, ok := r.oracle.resetLive[raw]; ok {
		slot, known := r.oracle.slots[tok.ownerEmail]
		if !known {
			r.t.Fatalf("oracle orphaned a live reset token of %q", tok.ownerEmail)
		}
		if !weak && !r.world.clock.Now().After(tok.expiresAt) && slot.status != modelSuspended && slot.status != modelDeleted {
			wantAccept = true
			owner = slot
			ownerEmail = tok.ownerEmail
		}
	}
	newPw := "short"
	if !weak {
		if wantAccept {
			newPw = r.resetPasswordFor(ownerEmail)
		} else {
			newPw = "ResetStrong-fallback-999!"
		}
	}
	err := r.world.doneReset.Execute(r.world.ctx, application.CompletePasswordResetCommand{Token: raw, NewPassword: newPw})
	if !r.check("complete-reset", wantAccept, err) {
		return
	}
	if !wantAccept {
		return
	}
	owner.password = newPw
	delete(r.oracle.resetLive, raw)
	r.oracle.resetDead[raw] = true
	for token, tok := range r.oracle.resetLive {
		if tok.ownerEmail == ownerEmail {
			r.oracle.resetDead[token] = true
			delete(r.oracle.resetLive, token)
		}
	}
	for token, tok := range r.oracle.verifyLive {
		if tok.ownerEmail == ownerEmail {
			r.oracle.verifyDead[token] = true
			delete(r.oracle.verifyLive, token)
		}
	}
	for _, sess := range r.oracle.sessions {
		if sess.ownerEmail == ownerEmail {
			sess.revoked = true
		}
	}
	r.syncAccounts()
}

// opLifecycle drives moderation-owned transitions through the domain object
// the store holds, mirroring each rule independently.
func (r *modelRunner) opLifecycle(email string, op int64) {
	r.step++
	slot, known := r.oracle.slots[email]
	if !known {
		return
	}
	acc, err := r.world.accounts.GetAccountByID(r.world.ctx, domain.AccountID(slot.id))
	if err != nil {
		r.t.Fatalf("model world lost account %s: %v", slot.id, err)
	}
	now := r.world.clock.Now()
	var wantAccept bool
	var execErr error
	switch op {
	case 0:
		wantAccept = slot.status == modelActive
		execErr = acc.Suspend(now)
		if wantAccept {
			slot.status = modelSuspended
		}
	case 1:
		wantAccept = slot.status == modelSuspended
		execErr = acc.Unsuspend(now)
		if wantAccept {
			slot.status = modelActive
		}
	case 2:
		wantAccept = slot.status != modelDeleted
		execErr = acc.MarkDeleted(now)
		if wantAccept {
			slot.status = modelDeleted
		}
	default:
		fresh := fmt.Sprintf("model-changed-%d@arena.local", r.ghostSeq)
		r.ghostSeq++
		wantAccept = slot.status != modelDeleted && slot.status != modelSuspended
		execErr = acc.ChangeEmail(mustModelEmail(r.t, fresh), now)
		if wantAccept {
			delete(r.oracle.slots, email)
			slot.email = fresh
			// A new address is unverified by construction: the domain
			// returns the account to Pending, and the oracle follows.
			slot.status = modelPending
			r.oracle.slots[fresh] = slot
			r.oracle.renameOwner(email, fresh)
			if r.retired == nil {
				r.retired = make(map[string]bool)
			}
			r.retired[email] = true
		}
	}
	r.check("lifecycle", wantAccept, execErr)
	r.syncAccounts()
}

// mustModelEmail parses a generated address the generator built valid by
// construction; a failure here is a broken generator, not a refusal.
func mustModelEmail(t *testing.T, raw string) domain.Email {
	t.Helper()
	email, err := domain.ParseEmail(raw)
	if err != nil {
		t.Fatalf("model generator built an invalid email %q: %v", raw, err)
	}
	return email
}

// liveVerifyTokens lists the slot's live verification tokens.
func (r *modelRunner) liveVerifyTokens(email string) []string {
	out := []string{}
	for raw, tok := range r.oracle.verifyLive {
		if tok.ownerEmail == email {
			out = append(out, raw)
		}
	}
	return out
}

// liveResetTokens lists the slot's live recovery tokens.
func (r *modelRunner) liveResetTokens(email string) []string {
	out := []string{}
	for raw, tok := range r.oracle.resetLive {
		if tok.ownerEmail == email {
			out = append(out, raw)
		}
	}
	return out
}

// deadStrings lists dead tokens of one table for replay probes.
func deadStrings(dead map[string]bool) []string {
	out := []string{}
	for raw := range dead {
		out = append(out, raw)
	}
	return out
}

var modelGarbageTokens = []string{"not-a-token", "", "  "}

var modelGarbageEmails = []string{"", "   ", "not-an-email", "user@.com", "@invalid.com"}

// pickEmail draws the next address: usually a known slot, sometimes the
// ghost, rarely a fixed refusal fixture.
func (r *modelRunner) pickEmail(rnd *testsource.Random, emails []string) string {
	switch rnd.Int64n(10) {
	case 0, 1:
		return "ghost@arena.local"
	case 2:
		return modelGarbageEmails[rnd.Int64n(int64(len(modelGarbageEmails)))]
	default:
		return emails[rnd.Int64n(int64(len(emails)))]
	}
}

// pickVerifyToken draws a token by role: live, replayed, or garbage.
func (r *modelRunner) pickVerifyToken(rnd *testsource.Random, email string) string {
	live := r.liveVerifyTokens(email)
	dead := deadStrings(r.oracle.verifyDead)
	switch rnd.Int64n(4) {
	case 0, 1:
		if len(live) > 0 {
			return live[rnd.Int64n(int64(len(live)))]
		}
		return modelGarbageTokens[rnd.Int64n(int64(len(modelGarbageTokens)))]
	case 2:
		if len(dead) > 0 {
			return dead[rnd.Int64n(int64(len(dead)))]
		}
		return modelGarbageTokens[rnd.Int64n(int64(len(modelGarbageTokens)))]
	default:
		return modelGarbageTokens[rnd.Int64n(int64(len(modelGarbageTokens)))]
	}
}

// pickResetToken draws a recovery token the same way.
func (r *modelRunner) pickResetToken(rnd *testsource.Random, email string) string {
	live := r.liveResetTokens(email)
	dead := deadStrings(r.oracle.resetDead)
	switch rnd.Int64n(4) {
	case 0, 1:
		if len(live) > 0 {
			return live[rnd.Int64n(int64(len(live)))]
		}
		return modelGarbageTokens[rnd.Int64n(int64(len(modelGarbageTokens)))]
	case 2:
		if len(dead) > 0 {
			return dead[rnd.Int64n(int64(len(dead)))]
		}
		return modelGarbageTokens[rnd.Int64n(int64(len(modelGarbageTokens)))]
	default:
		return modelGarbageTokens[rnd.Int64n(int64(len(modelGarbageTokens)))]
	}
}

// runOneSequence executes one deterministic command list and returns its
// divergences. Clocks only move forward and every draw comes from the
// registered stream, so a seed replays the sequence exactly.
func (r *modelRunner) runOneSequence(rnd *testsource.Random, emails []string) {
	steps := 6 + rnd.Int64n(9)
	for i := int64(0); i < steps; i++ {
		email := r.pickEmail(rnd, emails)
		switch rnd.Int64n(100) {
		case 0, 1, 2, 3, 4, 5, 6, 7:
			pw := modelStrongPassword(email)
			if rnd.Int64n(6) == 0 {
				pw = "short"
			}
			r.opRegister(email, pw)
		case 8, 9, 10, 11, 12, 13, 14, 15:
			r.opIssue(email)
		case 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27:
			r.opVerify(r.pickVerifyToken(rnd, email))
		case 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41:
			pw := modelStrongPassword(email)
			if slot, ok := r.oracle.slots[email]; ok {
				pw = slot.password
			}
			if rnd.Int64n(5) == 0 {
				pw = "WrongPassword-000!"
			}
			r.opLogin(email, pw)
		case 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53:
			r.opAuthenticate(r.tokenOf(rnd, email, rnd.Int64n(6) == 0))
		case 54, 55, 56, 57, 58, 59, 60, 61:
			r.opRotate(r.tokenOf(rnd, email, rnd.Int64n(6) == 0))
		case 62, 63, 64, 65, 66:
			r.opLogout(r.tokenOf(rnd, email, false))
		case 67, 68, 69:
			r.opLogoutAll(email)
		case 70, 71, 72, 73, 74, 75, 76, 77:
			r.opRequestReset(email)
		case 78, 79, 80, 81, 82, 83, 84, 85:
			r.opCompleteReset(r.pickResetToken(rnd, email), rnd.Int64n(5) == 0)
		case 86, 87, 88, 89, 90, 91:
			r.opLifecycle(email, rnd.Int64n(4))
		default:
			switch rnd.Int64n(5) {
			case 1:
				r.world.clock.Advance(time.Minute)
			case 2:
				r.world.clock.Advance(20 * time.Minute)
			case 3:
				r.world.clock.Advance(25 * time.Hour)
			case 4:
				r.world.clock.Advance(15 * 24 * time.Hour)
			}
			r.step++
			r.trace = append(r.trace, "advance")
			r.syncAccounts()
		}
	}
}

// TestIdentityAccountSessionModel runs thousands of deterministic command
// sequences against the real use cases and compares every outcome and every
// observed lifecycle status with the independent oracle.
func TestIdentityAccountSessionModel(t *testing.T) {
	seed := testsource.SeedFor(t)
	rnd := testsource.NewRandom(seed)
	const sequences = 3000
	emails := []string{"model-a@arena.local", "model-b@arena.local"}
	totalSteps := 0
	totalGrants := 0
	var firstDivergences []modelDivergence
	var firstTrace []string
	divergentRuns := 0
	for seq := 0; seq < sequences; seq++ {
		world := newModelWorld(seed + int64(seq))
		oracle := newModelOracle(world.clock.Now)
		runner := &modelRunner{t: t, world: world, oracle: oracle}
		for _, email := range emails {
			runner.opRegister(email, modelStrongPassword(email))
		}
		if len(runner.diverged) > 0 {
			t.Fatalf("sequence %d: setup diverged: %+v", seq, runner.diverged[:1])
		}
		runner.runOneSequence(rnd, emails)
		totalSteps += runner.step
		totalGrants += oracle.authGrants
		if len(runner.diverged) > 0 {
			divergentRuns++
			if firstDivergences == nil {
				firstDivergences = append([]modelDivergence{}, runner.diverged...)
				if len(firstDivergences) > 5 {
					firstDivergences = firstDivergences[:5]
				}
				firstTrace = append([]string{}, runner.trace...)
				if len(firstTrace) > 10 {
					firstTrace = firstTrace[len(firstTrace)-10:]
				}
			}
		}
	}
	if totalGrants == 0 {
		t.Fatal("no authentication was ever granted: the sequences never exercised the positive paths")
	}
	if divergentRuns > 0 {
		t.Fatalf("%d of %d sequences diverged, first: %+v trace: %v", divergentRuns, sequences, firstDivergences, firstTrace)
	}
	t.Logf("identity model: %d sequences, %d steps, %d granted authentications, seed %d", sequences, totalSteps, totalGrants, seed)
}

// TestIdentitySingleUseReplaySequential proves replayed single-use tokens
// stay single outside the generator: one verify and one reset, each used
// twice, with the second use refused and nothing else moved.
func TestIdentitySingleUseReplaySequential(t *testing.T) {
	world := newModelWorld(testsource.SeedFor(t))
	oracle := newModelOracle(world.clock.Now)
	runner := &modelRunner{t: t, world: world, oracle: oracle}
	const email = "replay@arena.local"
	runner.opRegister(email, modelStrongPassword(email))
	runner.opVerify(runner.pickVerifyToken(testsource.NewRandom(1), email))
	if len(runner.diverged) > 0 {
		t.Fatalf("setup diverged: %+v", runner.diverged[:1])
	}
	live := runner.liveVerifyTokens(email)
	if len(live) != 1 {
		t.Fatalf("want exactly one live verification token, got %d", len(live))
	}
	runner.opVerify(live[0])
	runner.opRequestReset(email)
	resets := runner.liveResetTokens(email)
	if len(resets) != 1 {
		t.Fatalf("want exactly one live reset token, got %d", len(resets))
	}
	runner.opCompleteReset(resets[0], false)
	// Both tokens are now spent: replaying either must be refused and must
	// move nothing.
	before := len(runner.diverged)
	runner.opVerify(live[0])
	runner.opCompleteReset(resets[0], false)
	if len(runner.diverged) != before {
		t.Fatalf("replayed single-use tokens were accepted: %+v", runner.diverged[before:])
	}
	runner.syncAccounts()
	if len(runner.diverged) != before {
		t.Fatalf("replay moved observed state: %+v", runner.diverged[before:])
	}
}

// TestIdentitySingleUseConcurrent races one fresh reset token across
// goroutines: the CAS-guarded store admits exactly one winner, so single-use
// survives concurrency. Verify/rotate races are enforced by the SQL
// statements in production; the in-memory fakes they stand in for carry the
// same guards only where the port promises them (consume returns false when
// raced), and the reset path is that promise.
func TestIdentitySingleUseConcurrent(t *testing.T) {
	world := newModelWorld(testsource.SeedFor(t))
	const email = "race@arena.local"
	ctx := context.Background()
	reg, err := world.register.Execute(ctx, application.RegisterAccountCommand{Email: email, Password: modelStrongPassword(email)})
	if err != nil {
		t.Fatalf("setup register: %v", err)
	}
	world.creds.credentials[reg.AccountID] = &application.PasswordCredentialRecord{AccountID: domain.AccountID(reg.AccountID), PasswordHash: "hashed_" + modelStrongPassword(email), Algorithm: "argon2id", Version: 1}
	if err := world.reqReset.Execute(ctx, application.RequestPasswordResetCommand{Email: email}); err != nil {
		t.Fatalf("setup request reset: %v", err)
	}
	token := world.lastToken()
	if token == "" {
		t.Fatal("setup emitted no reset token")
	}
	const racers = 24
	var wg sync.WaitGroup
	wins := make(chan bool, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := world.doneReset.Execute(ctx, application.CompletePasswordResetCommand{Token: token, NewPassword: fmt.Sprintf("RacerStrong-%d-12345!", i)})
			wins <- err == nil
		}(i)
	}
	wg.Wait()
	close(wins)
	succeeded := 0
	for win := range wins {
		if win {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("racing one reset token admitted %d winners, want exactly 1", succeeded)
	}
}

// TestIdentityModelDetectsMutatedTransition proves the harness bites: the
// same fixed script run against a mutant oracle that believes suspended
// accounts authenticate must report the login divergence, while the true
// oracle stays silent.
func TestIdentityModelDetectsMutatedTransition(t *testing.T) {
	script := func(r *modelRunner, mutant bool) {
		const email = "mutant@arena.local"
		r.opRegister(email, modelStrongPassword(email))
		tokens := r.liveVerifyTokens(email)
		if len(tokens) == 1 {
			r.opVerify(tokens[0])
		}
		r.opLifecycle(email, 0)
		if mutant {
			// Mutant: suspended accounts authenticate. The runner records
			// the SUT refusal as a divergence from the mutant belief.
			r.step++
			_, err := r.world.login.Execute(r.world.ctx, application.LoginCommand{Email: email, Password: modelStrongPassword(email)})
			r.check("login:"+email, true, err)
			return
		}
		r.opLogin(email, modelStrongPassword(email))
	}
	world := newModelWorld(7)
	oracle := newModelOracle(world.clock.Now)
	clean := &modelRunner{t: t, world: world, oracle: oracle}
	script(clean, false)
	if len(clean.diverged) > 0 {
		t.Fatalf("true oracle diverged on the fixed script: %+v", clean.diverged)
	}
	world2 := newModelWorld(7)
	oracle2 := newModelOracle(world2.clock.Now)
	mutant := &modelRunner{t: t, world: world2, oracle: oracle2}
	script(mutant, true)
	if len(mutant.diverged) == 0 {
		t.Fatal("mutant oracle (suspended authenticates) reported no divergence: the harness would not catch the flipped transition")
	}
	foundLogin := false
	for _, d := range mutant.diverged {
		if d.op == "login:mutant@arena.local" && d.want && !d.got {
			foundLogin = true
		}
	}
	if !foundLogin {
		t.Fatalf("mutant divergences %+v do not name the flipped login transition", mutant.diverged)
	}
}

// modelMFAAudit counts the administrative facts the recovery path must
// record. A recovery that cannot be recorded fails closed in production;
// here the counts are reconciled after every command.
type modelMFAAudit struct {
	mu        sync.Mutex
	enrolled  int
	recovered int
}

func (a *modelMFAAudit) RecordMFAEnrolled(_ context.Context, _ string, _ time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.enrolled++
	return nil
}

func (a *modelMFAAudit) RecordMFABackupCodeUsed(_ context.Context, _ string, _ time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recovered++
	return nil
}

func (a *modelMFAAudit) snapshot() (int, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.enrolled, a.recovered
}

// modelMFAOracle is the independent lifecycle of one second factor: no
// pending secret, a pending one, or a confirmed one, with the spent codes
// and the elevated sessions observed so far.
type modelMFAOracle struct {
	phase    string
	spent    map[string]bool
	backups  map[string]bool
	elevated map[string]bool
}

func newModelMFAOracle() *modelMFAOracle {
	return &modelMFAOracle{phase: "none", spent: make(map[string]bool), backups: make(map[string]bool), elevated: make(map[string]bool)}
}

// modelMFAWorld wires the real MFA use cases to the in-memory repo and the
// real mechanism under one explicit clock.
type modelMFAWorld struct {
	ctx       context.Context
	clock     *fakeClock
	repo      *inMemoryMFARepo
	audit     *modelMFAAudit
	mechanism *mfamechanism.Mechanism
	begin     *application.BeginMFAEnrollmentUseCase
	confirm   *application.ConfirmMFAEnrollmentUseCase
	stepUp    *application.StepUpMFAUseCase
	recover   *application.RecoverMFAUseCase
}

func newModelMFAWorld(clock *fakeClock, hasher application.PasswordHasher) *modelMFAWorld {
	log := &mfaEventLog{}
	repo := newInMemoryMFARepo(log)
	audit := &modelMFAAudit{}
	sealer, err := mfa.NewSealer(bytes.Repeat([]byte{0x2b}, mfa.KeySize), clockseed.NewRandom())
	if err != nil {
		panic(err)
	}
	mechanism, err := mfamechanism.New(mfa.Config{}, sealer, clockseed.NewRandom())
	if err != nil {
		panic(err)
	}
	return &modelMFAWorld{
		ctx:       context.Background(),
		clock:     clock,
		repo:      repo,
		audit:     audit,
		mechanism: mechanism,
		begin:     application.NewBeginMFAEnrollmentUseCase(repo, mechanism),
		confirm:   application.NewConfirmMFAEnrollmentUseCase(repo, mechanism, hasher, clock, audit),
		stepUp:    application.NewStepUpMFAUseCase(repo, mechanism, clock),
		recover:   application.NewRecoverMFAUseCase(repo, mechanism, hasher, clock, audit),
	}
}

// TestIdentityMFAModel runs hundreds of deterministic second-factor
// sequences against the real MFA use cases: enrollment can restart while
// pending but never replace a confirmed factor, codes are single-use across
// confirm/step-up/recovery, and elevation is a property of the session that
// presented the proof.
// mfaAccepts counts the positive paths one sequence exercised. The model
// test refuses a run where any of them stayed at zero: a model that never
// confirms, steps up or recovers proves refusals only.
type mfaAccepts struct {
	confirms int
	stepUps  int
	recovers int
}

func TestIdentityMFAModel(t *testing.T) {
	seed := testsource.SeedFor(t)
	rnd := testsource.NewRandom(seed)
	const sequences = 300
	divergentRuns := 0
	var first []modelDivergence
	total := mfaAccepts{}
	for seq := 0; seq < sequences; seq++ {
		diverged, accepts := runOneMFASequence(t, rnd, seed+int64(seq))
		total.confirms += accepts.confirms
		total.stepUps += accepts.stepUps
		total.recovers += accepts.recovers
		if len(diverged) > 0 {
			divergentRuns++
			if first == nil {
				first = append([]modelDivergence{}, diverged...)
				if len(first) > 5 {
					first = first[:5]
				}
			}
		}
	}
	if divergentRuns > 0 {
		t.Fatalf("%d of %d MFA sequences diverged, first: %+v", divergentRuns, sequences, first)
	}
	if total.confirms == 0 || total.stepUps == 0 || total.recovers == 0 {
		t.Fatalf("MFA model never exercised a positive path: %+v", total)
	}
	t.Logf("identity MFA model: %d sequences, confirms=%d stepUps=%d recovers=%d, seed %d", sequences, total.confirms, total.stepUps, total.recovers, seed)
}

// runOneMFASequence executes one deterministic second-factor script: the
// account and session come from the real identity flows, then enrollment,
// confirmation, step-up and recovery run against the oracle.
func runOneMFASequence(t *testing.T, rnd *testsource.Random, seed int64) ([]modelDivergence, mfaAccepts) {
	t.Helper()
	var diverged []modelDivergence
	var accepts mfaAccepts

	idWorld := newModelWorld(seed)
	oracle := newModelOracle(idWorld.clock.Now)
	idRunner := &modelRunner{t: t, world: idWorld, oracle: oracle}
	email := fmt.Sprintf("mfa-%d@arena.local", seed)
	idRunner.opRegister(email, modelStrongPassword(email))
	tokens := idRunner.liveVerifyTokens(email)
	if len(tokens) == 1 {
		idRunner.opVerify(tokens[0])
	}
	idRunner.opLogin(email, modelStrongPassword(email))
	if len(idRunner.diverged) > 0 {
		return append(diverged, modelDivergence{op: "mfa-setup", want: true, got: false}), accepts
	}
	var sessionID domain.SessionID
	for raw := range oracle.sessions {
		loginRes, err := idWorld.auth.Execute(idWorld.ctx, application.AuthenticateSessionCommand{RawToken: raw})
		if err == nil && loginRes != nil && loginRes.Session != nil {
			sessionID = loginRes.Session.ID()
			break
		}
	}
	if sessionID.IsZero() {
		return append(diverged, modelDivergence{op: "mfa-setup-session", want: true, got: false}), accepts
	}

	mfaWorld := newModelMFAWorld(idWorld.clock, fakePasswordHasher{})
	mfaWorld.repo.seedSession(string(sessionID))
	driver := &mfaDriver{
		t:           t,
		rnd:         rnd,
		clock:       idWorld.clock,
		world:       mfaWorld,
		oracle:      newModelMFAOracle(),
		accountID:   oracle.slots[email].id,
		sessionID:   string(sessionID),
		backupSpent: make(map[string]bool),
	}
	steps := 4 + rnd.Int64n(5)
	for i := int64(0); i < steps; i++ {
		switch rnd.Int64n(12) {
		case 0, 1:
			driver.begin()
		case 2, 3:
			driver.confirmFresh()
		case 4:
			driver.confirmGarbage()
		case 5, 6:
			driver.stepUpFresh()
		case 7:
			driver.stepUpReplay()
		case 8, 9:
			driver.recoverFresh()
		case 10:
			driver.recoverSpent()
		default:
			driver.advance()
		}
	}
	return driver.diverged, driver.accepts
}

// mfaDriver carries one second-factor script: the world, the oracle and
// the observed materials (current secret, issued backups, last code).
type mfaDriver struct {
	t           *testing.T
	rnd         *testsource.Random
	clock       *fakeClock
	world       *modelMFAWorld
	oracle      *modelMFAOracle
	accountID   string
	sessionID   string
	secret      []byte
	backups     []string
	backupSpent map[string]bool
	lastCode    string
	diverged    []modelDivergence
	accepts     mfaAccepts
}

func (d *mfaDriver) fail(op string, want, got bool) {
	d.diverged = append(d.diverged, modelDivergence{op: op, want: want, got: got})
}

func (d *mfaDriver) freshCode() string {
	d.t.Helper()
	code, err := mfa.Config{}.Code(d.secret, d.clock.Now())
	if err != nil {
		d.t.Fatalf("mfa code generation failed: %v", err)
	}
	return code
}

func (d *mfaDriver) reconcile(op string) {
	d.t.Helper()
	_, elevated := d.world.repo.sessionElevation(d.sessionID)
	if elevated != d.oracle.elevated[d.sessionID] {
		d.fail(op+":elevation", d.oracle.elevated[d.sessionID], elevated)
	}
}

func (d *mfaDriver) unusedBackup() string {
	for _, code := range d.backups {
		if !d.backupSpent[code] {
			return code
		}
	}
	return ""
}

func (d *mfaDriver) spentBackup() string {
	for code, spent := range d.backupSpent {
		if spent {
			return code
		}
	}
	return ""
}

func (d *mfaDriver) begin() {
	res, err := d.world.begin.Execute(d.world.ctx, application.BeginMFAEnrollmentCommand{AccountID: d.accountID})
	want := d.oracle.phase != "confirmed"
	if (err == nil) != want {
		d.fail("mfa-begin", want, err == nil)
		return
	}
	if !want {
		return
	}
	d.oracle.phase = "pending"
	decoded, derr := mfa.DecodeSecret(res.Secret)
	if derr != nil {
		d.t.Fatalf("decode of issued secret failed: %v", derr)
	}
	d.secret = decoded
}

func (d *mfaDriver) confirmFresh() {
	if len(d.secret) == 0 {
		return
	}
	code := d.freshCode()
	// The step is spent only when verification is reached: a refusal
	// before VerifyCode (no enrollment, already confirmed) spends nothing,
	// and the oracle must not claim otherwise, or a later presentation of
	// the same code inside the skew window diverges.
	_, wasSpent := d.oracle.spent[code]
	reached := d.oracle.phase == "pending"
	res, err := d.world.confirm.Execute(d.world.ctx, application.ConfirmMFAEnrollmentCommand{AccountID: d.accountID, Code: code})
	want := reached && !wasSpent
	if (err == nil) != want {
		d.fail("mfa-confirm", want, err == nil)
		return
	}
	if reached {
		d.oracle.spent[code] = true
		d.lastCode = code
		// A verified step never comes back: moving past it keeps every
		// later fresh code distinct, the way the product tests advance a
		// full period between spends. Refusals before VerifyCode spend
		// nothing and move nothing.
		d.clock.Advance(31 * time.Second)
	}
	if !want {
		return
	}
	d.oracle.phase = "confirmed"
	d.accepts.confirms++
	d.backups = append([]string{}, res.BackupCodes...)
	if len(d.backups) != mfa.DefaultBackupCodes {
		d.fail("mfa-confirm-codes", true, false)
	}
	if enrolled, _ := d.world.audit.snapshot(); enrolled != 1 {
		d.fail("mfa-confirm-audit", true, false)
	}
}

func (d *mfaDriver) confirmGarbage() {
	_, err := d.world.confirm.Execute(d.world.ctx, application.ConfirmMFAEnrollmentCommand{AccountID: d.accountID, Code: "abcdef"})
	if err == nil {
		d.fail("mfa-confirm-garbage", false, true)
	}
}

func (d *mfaDriver) stepUpFresh() {
	if len(d.secret) == 0 {
		return
	}
	code := d.freshCode()
	_, wasSpent := d.oracle.spent[code]
	reached := d.oracle.phase == "confirmed"
	err := d.world.stepUp.Execute(d.world.ctx, application.StepUpMFACommand{AccountID: d.accountID, SessionID: d.sessionID, Code: code})
	want := reached && !wasSpent
	if (err == nil) != want {
		d.fail("mfa-stepup", want, err == nil)
		return
	}
	if reached {
		d.oracle.spent[code] = true
		d.lastCode = code
		d.clock.Advance(31 * time.Second)
	}
	if want {
		d.oracle.elevated[d.sessionID] = true
		d.accepts.stepUps++
	}
	d.reconcile("mfa-stepup")
}

func (d *mfaDriver) stepUpReplay() {
	if d.lastCode == "" {
		return
	}
	err := d.world.stepUp.Execute(d.world.ctx, application.StepUpMFACommand{AccountID: d.accountID, SessionID: d.sessionID, Code: d.lastCode})
	if err == nil {
		d.fail("mfa-stepup-replay", false, true)
	}
	d.reconcile("mfa-stepup-replay")
}

func (d *mfaDriver) recoverFresh() {
	code := d.unusedBackup()
	if code == "" {
		return
	}
	_, recoveredBefore := d.world.audit.snapshot()
	err := d.world.recover.Execute(d.world.ctx, application.RecoverMFACommand{AccountID: d.accountID, SessionID: d.sessionID, Code: code})
	want := d.oracle.phase == "confirmed"
	if (err == nil) != want {
		d.fail("mfa-recover", want, err == nil)
		return
	}
	d.backupSpent[code] = true
	if !want {
		return
	}
	d.oracle.elevated[d.sessionID] = true
	d.accepts.recovers++
	if _, recoveredAfter := d.world.audit.snapshot(); recoveredAfter != recoveredBefore+1 {
		d.fail("mfa-recover-audit", true, false)
	}
	d.reconcile("mfa-recover")
}

func (d *mfaDriver) recoverSpent() {
	code := d.spentBackup()
	if code == "" {
		code = "not-a-code"
	}
	err := d.world.recover.Execute(d.world.ctx, application.RecoverMFACommand{AccountID: d.accountID, SessionID: d.sessionID, Code: code})
	if err == nil {
		d.fail("mfa-recover-spent", false, true)
	}
	d.reconcile("mfa-recover-spent")
}

func (d *mfaDriver) advance() {
	switch d.rnd.Int64n(4) {
	case 1:
		d.clock.Advance(31 * time.Second)
	case 2:
		d.clock.Advance(time.Hour)
	}
}
