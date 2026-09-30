package turnstile

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
)

// Verifier is the port the guard depends on: it decides whether one challenge
// token proves a solved challenge for one action.
//
// It returns an error rather than a boolean because the answers are not
// interchangeable: a token that was replayed, a token minted for another
// action and a provider that timed out are three different refusals with three
// different codes, and the fail policy applies to only one of them.
type Verifier interface {
	// Verify redeems one token. A nil error means the token was valid, was
	// minted for the action it is being spent on, belongs to the configured
	// hostname, and had not been redeemed before.
	Verify(ctx context.Context, verification Verification) error
}

// Verification is one token presented for one action.
type Verification struct {
	// Token is the challenge token the client solved for.
	Token string
	// Action is the action the token is being spent on, from the policy
	// table. It is sent to the provider implicitly through the token's own
	// action and checked against it.
	Action Action
	// Client is the caller's network address, when one could be read. It is
	// advisory to the provider and never part of the decision here.
	Client string
}

// refusal builds one refusal problem: a stable code, the detail that carries
// no caller data, and the kind that decides the status.
func refusal(kind apperr.Kind, code, detail string) *apperr.Error {
	return apperr.New(kind, code, detail)
}

// IsUnavailable reports whether a verification error means the challenge could
// not be checked, as opposed to the challenge being refused. It is the single
// question the fail policy is about: a caller who presented a bad token is
// refused whatever the policy says, and only an unreachable provider is the
// operator's trade to make.
func IsUnavailable(err error) bool {
	var appError *apperr.Error
	if !errors.As(err, &appError) {
		return false
	}
	return appError.Code() == CodeChallengeUnavailable
}

// cloudflare is the verifier that talks to the provider.
type cloudflare struct {
	secret   string
	hostname string
	endpoint string
	timeout  time.Duration
	client   *http.Client
	redeemed *ReplayMemory
}

// newSiteverify builds the provider-backed verifier. It is only reachable
// through New, which has already refused the configurations that have no
// verifier at all.
func newSiteverify(configuration Config) *cloudflare {
	client := configuration.Client
	if client == nil {
		client = &http.Client{Timeout: configuration.timeout()}
	}

	return &cloudflare{
		secret:   configuration.SecretKey,
		hostname: configuration.Hostname,
		endpoint: configuration.endpoint(),
		timeout:  configuration.timeout(),
		client:   client,
		redeemed: NewReplayMemory(configuration.redeemedCapacity(), configuration.tokenLifetime(), configuration.Now),
	}
}

// siteverifyResponse is the provider's answer. Only the fields this package
// decides on are decoded; the rest of the document is ignored rather than
// trusted.
type siteverifyResponse struct {
	Success    bool     `json:"success"`
	ErrorCodes []string `json:"error-codes"`
	Hostname   string   `json:"hostname"`
	Action     string   `json:"action"`
}

// error classes of the provider's codes. The provider publishes a closed
// vocabulary; an unknown code is read as an invalid token, which is the
// conservative direction (the caller is refused rather than served).
const (
	classInvalid = iota
	classReplay
	classUnavailable
	classMisconfigured
)

// classify maps the provider's codes onto the four classes that have different
// consequences here.
func classify(codes []string) int {
	for _, code := range codes {
		switch code {
		case "timeout-or-duplicate":
			return classReplay
		case "internal-error":
			return classUnavailable
		case "bad-request", "missing-input-secret", "invalid-input-secret":
			return classMisconfigured
		}
	}
	// `missing-input-response` and `invalid-input-response` — and anything the
	// provider adds later — are the caller's problem.
	return classInvalid
}

// Verify implements Verifier.
//
// The token is claimed before the provider is asked, so a token is single-use
// even under concurrent replays and even when the provider is unreachable: the
// claim is atomic, and the loser of a race is told the token was replayed
// rather than being served. The cost of claiming first is that a transient
// provider failure spends a token the provider never saw; the client solves a
// new challenge, which is what a single-use token costs anyway.
func (verifier *cloudflare) Verify(ctx context.Context, verification Verification) error {
	if verification.Token == "" {
		return refusal(apperr.KindForbidden, CodeChallengeRequired, "a challenge token is required for this action")
	}
	if len(verification.Token) > MaxTokenBytes {
		return refusal(apperr.KindForbidden, CodeChallengeInvalid, "the challenge token is not valid")
	}
	if !verifier.redeemed.Redeem(verification.Token) {
		return refusal(apperr.KindForbidden, CodeChallengeReplayed, "the challenge token has already been used")
	}

	form := url.Values{}
	form.Set("secret", verifier.secret)
	form.Set("response", verification.Token)
	if verification.Client != "" {
		form.Set("remoteip", verification.Client)
	}

	// The deadline comes from the configuration, not from the client: a
	// client injected by a test has no timeout of its own, and a zero
	// deadline would cancel the call before it started.
	requestContext, cancel := context.WithTimeout(ctx, verifier.timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, verifier.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return unavailable("the challenge could not be verified").WithCause(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")

	response, err := verifier.client.Do(request)
	if err != nil {
		// A timeout, a refused connection and a TLS failure are the same
		// answer here: nobody told us whether the token is good.
		return unavailable("the challenge could not be verified").WithCause(err)
	}
	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return unavailable("the challenge could not be verified").WithCause(err)
	}
	if response.StatusCode != http.StatusOK {
		return unavailable(fmt.Sprintf("the challenge provider answered status %d", response.StatusCode))
	}

	var payload siteverifyResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		// An answer that cannot be parsed is an answer we do not have.
		return unavailable("the challenge could not be verified").WithCause(err)
	}

	if !payload.Success {
		switch classify(payload.ErrorCodes) {
		case classReplay:
			return refusal(apperr.KindForbidden, CodeChallengeReplayed, "the challenge token has already been used or has expired")
		case classUnavailable:
			return unavailable("the challenge could not be verified")
		case classMisconfigured:
			// The provider told us our own configuration is wrong. This is
			// never served through, whatever the fail policy says: a broken
			// secret is not an outage, it is a deployment that must be fixed.
			return refusal(apperr.KindInternal, CodeChallengeMisconfigured, "the challenge verification is misconfigured")
		default:
			return refusal(apperr.KindForbidden, CodeChallengeInvalid, "the challenge token is not valid")
		}
	}

	// The provider answers for the hostname the challenge was solved on. A
	// token solved for another site must not be spendable here.
	if !strings.EqualFold(payload.Hostname, verifier.hostname) {
		return refusal(apperr.KindForbidden, CodeChallengeHostnameMismatch, "the challenge was not solved for this site")
	}

	// And for the action it was minted for, so a token solved for the signup
	// widget cannot be spent on a publication.
	if payload.Action != string(verification.Action) {
		return refusal(apperr.KindForbidden, CodeChallengeActionMismatch, "the challenge was not solved for this action")
	}

	return nil
}

// unavailable is the one refusal the fail policy is about.
func unavailable(detail string) *apperr.Error {
	return refusal(apperr.KindInternal, CodeChallengeUnavailable, detail)
}

// ReplayMemory remembers which challenge tokens have been redeemed, so a
// token is single-use here and not only at the provider.
//
// Two properties are deliberate:
//
//   - it stores a SHA-256 fingerprint, never the token. A token is a
//     credential for as long as it lives, and a memory of credentials is a
//     liability that a memory of fingerprints is not;
//   - it is bounded in time and in size, exactly like the rate limiter's
//     buckets: entries expire after the lifetime a token could have had, and
//     the least recently redeemed are evicted at capacity. Eviction can only
//     forget a redemption (the provider's own single-use is the second line),
//     it can never invent one.
//
// The zero ReplayMemory is not usable; build one with NewReplayMemory.
type ReplayMemory struct {
	mu       sync.Mutex
	entries  map[string]*list.Element
	recency  *list.List
	capacity int
	lifetime time.Duration
	now      func() time.Time
}

// redeemedToken is one fingerprint and its position in the recency list.
type redeemedToken struct {
	fingerprint string
	redeemedAt  time.Time
}

// NewReplayMemory builds the memory. Non-positive values take the package
// defaults, and a nil clock means the system clock, read through the package
// that owns that effect — a replay window is a rule, and a rule that cannot be
// told what "now" is cannot be replayed in a test.
func NewReplayMemory(capacity int, lifetime time.Duration, now func() time.Time) *ReplayMemory {
	if capacity <= 0 {
		capacity = DefaultRedeemedCapacity
	}
	if lifetime <= 0 {
		lifetime = DefaultTokenLifetime
	}
	if now == nil {
		now = clockseed.SystemClockNow
	}

	return &ReplayMemory{
		entries:  make(map[string]*list.Element, capacity),
		recency:  list.New(),
		capacity: capacity,
		lifetime: lifetime,
		now:      now,
	}
}

// Redeem claims a token and reports whether it was still redeemable. It is one
// atomic operation on purpose: a check followed by a separate remember would
// let two concurrent replays of the same token both pass the check.
func (memory *ReplayMemory) Redeem(token string) bool {
	fingerprint := fingerprintOf(token)
	now := memory.now()

	memory.mu.Lock()
	defer memory.mu.Unlock()
	memory.sweepLocked(now)

	if element, exists := memory.entries[fingerprint]; exists {
		// Touching the entry keeps a token that is being replayed at the
		// front, where eviction will not reach it before it expires.
		memory.recency.MoveToFront(element)
		return false
	}

	element := memory.recency.PushFront(&redeemedToken{fingerprint: fingerprint, redeemedAt: now})
	memory.entries[fingerprint] = element
	for len(memory.entries) > memory.capacity {
		oldest := memory.recency.Back()
		if oldest == nil {
			break
		}
		memory.recency.Remove(oldest)
		delete(memory.entries, oldest.Value.(*redeemedToken).fingerprint)
	}
	return true
}

// Len reports how many fingerprints are held, which is the memory bound under
// test.
func (memory *ReplayMemory) Len() int {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	return len(memory.entries)
}

// sweepLocked drops fingerprints older than a token could have lived. The
// walk starts at the oldest entry and stops at the first live one, so the cost
// is proportional to what is dropped.
func (memory *ReplayMemory) sweepLocked(now time.Time) {
	for element := memory.recency.Back(); element != nil; {
		previous := element.Prev()
		held := element.Value.(*redeemedToken)
		if now.Sub(held.redeemedAt) < memory.lifetime {
			return
		}
		memory.recency.Remove(element)
		delete(memory.entries, held.fingerprint)
		element = previous
	}
}

// fingerprintOf hashes a token with a domain separator, so a fingerprint can
// never collide with the digest of the same bytes used elsewhere.
func fingerprintOf(token string) string {
	digest := sha256.Sum256([]byte("arena.turnstile.token\x00" + token))
	return hex.EncodeToString(digest[:])
}
