package application

import (
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// validatedGovernors carries the parsed governors of one Treasury act
// shared by the disbursement and restitution use cases: the unique
// reference, the origin vault, the beneficiary, the exact amount and
// the two governors, all validated before any repository call.
type validatedGovernors struct {
	key         domain.IntentionKey
	vault       domain.TreasuryVault
	beneficiary domain.IntentionActor
	amount      domain.MilliInk
	approverOne domain.IntentionActor
	approverTwo domain.IntentionActor
}

// validateGovernedAct validates one governed Treasury act once for
// every use case that pays through the single disbursement port: a
// named reference, a vault in the closed vocabulary, a beneficiary, a
// positive amount and two whole governors besides the paid account.
// Irreversible moves need two governors: a single approver twice, a
// beneficiary governing itself and any partial credential stop before
// the journal.
func validateGovernedAct(key, vault, beneficiary string, millis int64, approverOne, approverTwo string) (validatedGovernors, error) {
	parsedKey, err := domain.ParseIntentionKey(key)
	if err != nil {
		return validatedGovernors{}, err
	}
	parsedVault, err := domain.ParseTreasuryVault(vault)
	if err != nil {
		return validatedGovernors{}, err
	}
	parsedBeneficiary, err := domain.ParseIntentionActor(beneficiary)
	if err != nil {
		return validatedGovernors{}, err
	}
	amount, err := domain.NewMilliInk(millis)
	if err != nil {
		return validatedGovernors{}, err
	}
	if amount.IsZero() {
		return validatedGovernors{}, domain.ErrInvalidGrant
	}
	parsedOne, err := domain.ParseIntentionActor(approverOne)
	if err != nil {
		return validatedGovernors{}, err
	}
	parsedTwo, err := domain.ParseIntentionActor(approverTwo)
	if err != nil {
		return validatedGovernors{}, err
	}
	if parsedOne == parsedTwo {
		return validatedGovernors{}, domain.ErrInvalidDisbursement
	}
	if parsedBeneficiary == parsedOne || parsedBeneficiary == parsedTwo {
		return validatedGovernors{}, domain.ErrInvalidDisbursement
	}
	return validatedGovernors{
		key:         parsedKey,
		vault:       parsedVault,
		beneficiary: parsedBeneficiary,
		amount:      amount,
		approverOne: parsedOne,
		approverTwo: parsedTwo,
	}, nil
}
