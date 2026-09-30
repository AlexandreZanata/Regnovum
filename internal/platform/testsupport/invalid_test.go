package testsupport

import (
	"errors"
	"testing"

	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	argumentsdomain "github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	moderationdomain "github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
	positionsdomain "github.com/AlexandreZanata/Regnovum/internal/positions/domain"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// TestEveryInvalidInputIsRefused is what keeps the invalid surface honest. Each
// helper exists to show that a contravention the product refuses really is
// refused, and it must answer *that* refusal: a helper that returned nil, or a
// different error, would be a fixture that lies about the rule it names.
//
// The table is the complete list of deliberate contraventions in the package:
// if a helper is added without a case here, the promise of invalid.go ("the
// names tell a reader what the test is doing") stops being checked.
func TestEveryInvalidInputIsRefused(t *testing.T) {
	cases := []struct {
		name   string
		raise  func(*Builder) error
		expect error
	}{
		{"an account without an address", (*Builder).InvalidAccountWithoutAddress, identitydomain.ErrEmptyEmail},
		{"an account without an identifier", (*Builder).InvalidAccountWithoutIdentifier, identitydomain.ErrEmptyAccountID},
		{"a session without an account", (*Builder).InvalidSessionWithoutAccount, identitydomain.ErrEmptyAccountID},
		{"a session without a token", (*Builder).InvalidSessionWithoutToken, identitydomain.ErrEmptySessionToken},
		{"a draft carrying a public address", (*Builder).InvalidArenaDraftWithSlug, arenasdomain.ErrInvalidStatusChange},
		{"a published Arena with no address", (*Builder).InvalidArenaPublishedWithoutSlug, arenasdomain.ErrInvalidStatusChange},
		{"an Arena closing before its publication", (*Builder).InvalidArenaClosingBeforePublishing, arenasdomain.ErrInvalidCloseDate},
		{"an Arena that states nothing", (*Builder).InvalidArenaWithoutStatement, arenasdomain.ErrEmptyStatement},
		{"a position change that changes nothing", (*Builder).InvalidPositionChangeToTheSamePosition, positionsdomain.ErrSamePosition},
		{"an argument with no content", (*Builder).InvalidArgumentWithoutContent, argumentsdomain.ErrEmptyContent},
		{"an argument with no grapheme counter", (*Builder).InvalidArgumentWithoutGraphemeCounter, argumentsdomain.ErrMissingGraphemeCounter},
		{"a source with no address", (*Builder).InvalidArgumentSourceWithoutAddress, argumentsdomain.ErrEmptySourceURL},
		{"an argument with no retry key", (*Builder).InvalidArgumentWithoutIdempotencyKey, argumentsdomain.ErrEmptyIdempotencyKey},
		{"a negative INK quantity", (*Builder).InvalidWalletNegativeInk, walletdomain.ErrNegativeInk},
		{"a credit of nothing", (*Builder).InvalidWalletCreditOfNothing, walletdomain.ErrZeroAmount},
		{"a pass lot consumed beyond its grant", (*Builder).InvalidEntitlementOverConsumed, billingdomain.ErrInvalidRemaining},
		{"a product priced below zero", (*Builder).InvalidBillingPriceThatIsNegative, billingdomain.ErrInvalidMoney},
		{"a product identifier outside the vocabulary", (*Builder).InvalidBillingProductOutsideTheVocabulary, billingdomain.ErrInvalidProductID},
		{"a product priced in the wrong currency", (*Builder).InvalidBillingProductInTheWrongCurrency, billingdomain.ErrMarketCurrencyMismatch},
		{"a report without a reason", (*Builder).InvalidReportWithoutReason, moderationdomain.ErrInvalidReason},
		{"a report without a target", (*Builder).InvalidReportWithoutTarget, moderationdomain.ErrEmptyTargetID},
		{"a decision without a rule", (*Builder).InvalidDecisionWithoutRule, moderationdomain.ErrInvalidRule},
		{"a decision sanctioning the wrong target", (*Builder).InvalidDecisionSanctioningTheWrongTarget, moderationdomain.ErrTargetActionMismatch},
		{"a job without a payload", (*Builder).InvalidJobWithoutPayload, jobsdomain.ErrEmptyPayload},
		{"a job of an unknown type", (*Builder).InvalidJobOfUnknownType, jobsdomain.ErrUnknownJobType},
		{"a job leased without a lease", (*Builder).InvalidJobLeasedWithoutLease, jobsdomain.ErrLeaseIncoherent},
		{"a job whose payload is not an object", (*Builder).InvalidJobPayloadThatIsNotAnObject, jobsdomain.ErrPayloadNotObject},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.raise(New(t))
			if err == nil {
				t.Fatalf("the domain accepted %s, so the invalid helper no longer names a contravention", testCase.name)
			}
			if !errors.Is(err, testCase.expect) {
				t.Fatalf("%s answered %v; want %v — the helper must expose the rule it breaks", testCase.name, err, testCase.expect)
			}
		})
	}
}
