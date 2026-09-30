package turnstile

import (
	"context"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/platform/apperr"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
)

// LocalFakeTokenPrefix is the beginning of every token Cloudflare's
// always-passing test widget produces. The fake accepts these and nothing
// else, so a local development flow that is wired correctly still presents a
// token, and a token that is not a challenge token at all is still refused.
const LocalFakeTokenPrefix = "XXXX.DUMMY.TOKEN."

// LocalFake is the verifier used when no secret is configured outside
// production.
//
// It exists because the alternative for local development is worse in both
// directions: a missing verifier would make the guarded routes either
// unprotected (so a developer never exercises the challenge path) or
// impossible to use (so a developer disables the guard by hand and ships that).
// The fake keeps the whole pipeline — extraction, single use, refusal shape,
// the fail policy's reach — in the path of every local request, and it is
// structurally unable to talk to the network: it has no HTTP client, no
// endpoint and no secret.
//
// It is not a weaker real verifier: it verifies nothing, which is exactly why
// New refuses to build it in production.
type LocalFake struct {
	redeemed *ReplayMemory
}

// NewLocalFake builds the temporary local verifier. Its clock is the system
// clock through the package that owns that effect, which is the honest source
// for a fake that stands in for a real provider: it is exercised by the
// requests a developer makes now, and single use is what it has to reproduce.
// A test that wants to cross the window injects a clock instead.
func NewLocalFake() *LocalFake {
	return &LocalFake{redeemed: NewReplayMemory(0, 0, clockseed.SystemClockNow)}
}

// Verify implements Verifier.
//
// A token that does not carry the test prefix is refused as invalid rather
// than accepted, so "the fake accepts anything" is not the behaviour a
// developer develops against. Single use applies here too, which is what makes
// the replay path visible locally instead of only in production.
func (fake *LocalFake) Verify(_ context.Context, verification Verification) error {
	if verification.Token == "" {
		return refusal(apperr.KindForbidden, CodeChallengeRequired, "a challenge token is required for this action")
	}
	if !strings.HasPrefix(verification.Token, LocalFakeTokenPrefix) {
		return refusal(apperr.KindForbidden, CodeChallengeInvalid, "the challenge token is not valid")
	}
	if !fake.redeemed.Redeem(verification.Token) {
		return refusal(apperr.KindForbidden, CodeChallengeReplayed, "the challenge token has already been used")
	}
	return nil
}
