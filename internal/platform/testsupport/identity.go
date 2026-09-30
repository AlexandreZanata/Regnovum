package testsupport

import (
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

// Synthetic addresses are the only ones a scenario carries: the reserved
// example domain cannot belong to a person, so no builder can smuggle a real
// address into a fixture (P22-T04 states the same rule for datasets).
//
// The default address is unique per call (owner-<token>@example.test) because
// the schema keeps a unique index on the lower-cased address: a scenario that
// builds two accounts — an author and a moderator, say — must not build one
// collision.
const (
	defaultEmailPrefix = "owner"
	defaultEmailDomain = "example.test"
	// defaultIP and defaultUserAgent are the session metadata of a scenario:
	// loopback and a self-describing agent, never a client of record.
	defaultIP        = "127.0.0.1"
	defaultUserAgent = "testsupport/scenario"
)

// AccountOption varies the account a builder makes. Only valid variation is
// expressible here; an invalid account is the business of invalid.go.
type AccountOption func(*accountSpec)

type accountSpec struct {
	email string
}

// WithAccountEmail builds the account for a specific synthetic address. The
// address is parsed by the domain like any other, so a malformed one is
// refused here and not later. An empty address means "keep the default",
// which is unique per call; the contravention of building an account without
// one lives in invalid.go.
func WithAccountEmail(email string) AccountOption {
	return func(spec *accountSpec) { spec.email = email }
}

// Account builds a registered account whose address is not yet confirmed —
// the state a journey begins in. It is the account the product's own
// constructor accepts: no field is set behind the aggregate's back.
func (b *Builder) Account(options ...AccountOption) (*identitydomain.Account, error) {
	b.t.Helper()
	spec := accountSpec{}
	for _, option := range options {
		option(&spec)
	}
	address := spec.email
	if address == "" {
		address = defaultEmailPrefix + "-" + b.shortToken() + "@" + defaultEmailDomain
	}
	email, err := identitydomain.ParseEmail(address)
	if err != nil {
		return nil, err
	}
	return identitydomain.NewAccount(identitydomain.AccountID(b.Identifier()), email, b.Now())
}

// VerifiedAccount builds the same account one step later: registered and with
// a confirmed address, which is the state that may hold a session, own an Arena
// or be credited.
func (b *Builder) VerifiedAccount(options ...AccountOption) (*identitydomain.Account, error) {
	b.t.Helper()
	account, err := b.Account(options...)
	if err != nil {
		return nil, err
	}
	if err := account.VerifyEmail(b.Now()); err != nil {
		return nil, err
	}
	return account, nil
}

// SuspendedAccount builds a verified account a moderator has suspended: the
// state every authorization scenario on the refusing side starts from.
func (b *Builder) SuspendedAccount(options ...AccountOption) (*identitydomain.Account, error) {
	b.t.Helper()
	account, err := b.VerifiedAccount(options...)
	if err != nil {
		return nil, err
	}
	if err := account.Suspend(b.Now()); err != nil {
		return nil, err
	}
	return account, nil
}

// DeletedAccount builds a verified account that has been erased, which is the
// state that must be indistinguishable from a stranger's.
func (b *Builder) DeletedAccount(options ...AccountOption) (*identitydomain.Account, error) {
	b.t.Helper()
	account, err := b.VerifiedAccount(options...)
	if err != nil {
		return nil, err
	}
	if err := account.MarkDeleted(b.Now()); err != nil {
		return nil, err
	}
	return account, nil
}

// SessionOption varies the session a builder makes.
type SessionOption func(*sessionSpec)

type sessionSpec struct {
	ipAddress string
	userAgent string
	policy    identitydomain.SessionPolicy
}

// WithSessionMetadata builds the session for a specific address and agent, for
// the scenario that reads them back.
func WithSessionMetadata(ipAddress, userAgent string) SessionOption {
	return func(spec *sessionSpec) {
		spec.ipAddress = ipAddress
		spec.userAgent = userAgent
	}
}

// WithSessionPolicy builds the session under a specific policy, for the
// scenario that crosses one of its bounds.
func WithSessionPolicy(policy identitydomain.SessionPolicy) SessionOption {
	return func(spec *sessionSpec) { spec.policy = policy }
}

// Session builds an active session for the account, with a token hash drawn
// from the scenario's own entropy stream: the hash is reproducible from the
// seed and belongs to no real token.
func (b *Builder) Session(account *identitydomain.Account, options ...SessionOption) (*identitydomain.Session, error) {
	b.t.Helper()
	if account == nil {
		return nil, identitydomain.ErrEmptyAccountID
	}
	spec := sessionSpec{
		ipAddress: defaultIP,
		userAgent: defaultUserAgent,
		policy:    identitydomain.DefaultSessionPolicy(),
	}
	for _, option := range options {
		option(&spec)
	}
	return identitydomain.NewSession(
		identitydomain.SessionID(b.Identifier()),
		account.ID(),
		b.tokenHash(),
		spec.ipAddress,
		spec.userAgent,
		b.Now(),
		spec.policy,
	)
}

// RevokedSession builds a session that has already been closed, which is what a
// scenario asserting that a closed session stays closed needs.
func (b *Builder) RevokedSession(account *identitydomain.Account, options ...SessionOption) (*identitydomain.Session, error) {
	b.t.Helper()
	session, err := b.Session(account, options...)
	if err != nil {
		return nil, err
	}
	if err := session.Revoke(b.Now()); err != nil {
		return nil, err
	}
	return session, nil
}

// tokenHash answers the bytes of one session token, taken from the scenario's
// stream. It is entropy, not a secret: the same seed answers the same bytes,
// which is what lets a scenario assert on a session it rebuilt.
func (b *Builder) tokenHash() []byte {
	hash := make([]byte, 32)
	written, err := b.Source().Read(hash)
	if err != nil || written != len(hash) {
		// The deterministic stream never fails and always fills: a source that
		// did either would be a defect in T02's package, not a scenario
		// decision, so the honest answer is to refuse rather than to continue
		// with a short hash.
		b.t.Fatalf("testsupport: the deterministic stream answered %d bytes: %v", written, err)
		return nil
	}
	return hash
}
