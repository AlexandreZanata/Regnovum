package testsupport

import (
	"time"

	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	argumentsdomain "github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	moderationdomain "github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
	positionsdomain "github.com/AlexandreZanata/Regnovum/internal/positions/domain"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// The deliberately invalid inputs of the builders (P22-T03: "overrides
// inválidos devem ser explícitos e separados").
//
// Every function here answers with the *domain's own refusal* and never with a
// value: the invalid input exists in this file, on purpose, and the product is
// what judges it. Two consequences follow, and both are the reason the file
// exists instead of a `WithInvalid...` option on the ordinary surface:
//
//   - a scenario cannot hold an invalid entity, so a fixture that looks like
//     the product's data and is not becomes impossible;
//   - a review that finds an "Invalid..." call in a test knows exactly what the
//     test is doing, and a search for the prefix lists every deliberate
//     contravention in the tree.
//
// The names state the invariant each one breaks, not the mechanics of breaking
// it: a reader of the call site learns what the scenario is about.
//
// Each of them is exercised by invalid_test.go against the error it is supposed
// to raise, so a helper that stopped being refused fails the suite instead of
// silently becoming a valid fixture.

// InvalidAccountWithoutAddress registers an account with an empty address.
//
// It builds the account directly rather than asking the builder for one with an
// empty option: on the ordinary surface an empty address means "keep the
// default", so the contravention has to be named and made here.
func (b *Builder) InvalidAccountWithoutAddress() error {
	b.t.Helper()
	_, err := identitydomain.NewAccount(identitydomain.AccountID(b.Identifier()), identitydomain.Email{}, b.Now())
	return err
}

// InvalidAccountWithoutIdentifier builds an account with no identifier, which
// is the state a repository write must never receive.
func (b *Builder) InvalidAccountWithoutIdentifier() error {
	b.t.Helper()
	email, err := identitydomain.ParseEmail(defaultEmailPrefix + "@" + defaultEmailDomain)
	if err != nil {
		return err
	}
	_, err = identitydomain.NewAccount("", email, b.Now())
	return err
}

// InvalidSessionWithoutAccount opens a session for no account.
func (b *Builder) InvalidSessionWithoutAccount() error {
	b.t.Helper()
	_, err := identitydomain.NewSession(
		identitydomain.SessionID(b.Identifier()), "", b.tokenHash(),
		defaultIP, defaultUserAgent, b.Now(), identitydomain.DefaultSessionPolicy(),
	)
	return err
}

// InvalidSessionWithoutToken opens a session that carries no token hash, which
// is a session nobody could present.
func (b *Builder) InvalidSessionWithoutToken() error {
	b.t.Helper()
	_, err := identitydomain.NewSession(
		identitydomain.SessionID(b.Identifier()), identitydomain.AccountID(b.Identifier()), nil,
		defaultIP, defaultUserAgent, b.Now(), identitydomain.DefaultSessionPolicy(),
	)
	return err
}

// InvalidArenaDraftWithSlug stores a draft that already carries the address of
// a published Arena, which the aggregate refuses: a draft has no public address.
func (b *Builder) InvalidArenaDraftWithSlug() error {
	b.t.Helper()
	values, err := b.arenaSpec()
	if err != nil {
		return err
	}
	slug, err := arenasdomain.ParseSlug(namedSlug)
	if err != nil {
		return err
	}
	_, err = arenasdomain.ReconstituteArena(
		arenasdomain.ArenaID(b.Identifier()), arenasdomain.CreatorID(b.Identifier()),
		values.parsed.statement, values.parsed.context, values.parsed.category, values.parsed.language,
		arenasdomain.ArenaStatusDraft, slug, 1, b.Now(), nil, nil,
	)
	return err
}

// InvalidArenaPublishedWithoutSlug stores a published Arena with no public
// address, which no reader could reach.
func (b *Builder) InvalidArenaPublishedWithoutSlug() error {
	b.t.Helper()
	values, err := b.arenaSpec()
	if err != nil {
		return err
	}
	publishedAt := b.Now()
	_, err = arenasdomain.ReconstituteArena(
		arenasdomain.ArenaID(b.Identifier()), arenasdomain.CreatorID(b.Identifier()),
		values.parsed.statement, values.parsed.context, values.parsed.category, values.parsed.language,
		arenasdomain.ArenaStatusPublished, arenasdomain.Slug{}, 1, b.Now(), &publishedAt, nil,
	)
	return err
}

// InvalidArenaClosingBeforePublishing schedules the close of an Arena in the
// past of its own publication.
func (b *Builder) InvalidArenaClosingBeforePublishing() error {
	b.t.Helper()
	values, err := b.arenaSpec()
	if err != nil {
		return err
	}
	slug, err := arenasdomain.ParseSlug(namedSlug)
	if err != nil {
		return err
	}
	publishedAt := b.Now()
	closesAt := publishedAt.Add(-time.Hour)
	_, err = arenasdomain.ReconstituteArena(
		arenasdomain.ArenaID(b.Identifier()), arenasdomain.CreatorID(b.Identifier()),
		values.parsed.statement, values.parsed.context, values.parsed.category, values.parsed.language,
		arenasdomain.ArenaStatusPublished, slug, 1, b.Now(), &publishedAt, &closesAt,
	)
	return err
}

// InvalidArenaWithoutStatement stores an Arena that states nothing, which is
// not a debate anyone could take a position on.
func (b *Builder) InvalidArenaWithoutStatement() error {
	b.t.Helper()
	values, err := b.arenaSpec()
	if err != nil {
		return err
	}
	_, err = arenasdomain.ReconstituteArena(
		arenasdomain.ArenaID(b.Identifier()), arenasdomain.CreatorID(b.Identifier()),
		arenasdomain.Statement{}, values.parsed.context, values.parsed.category, values.parsed.language,
		arenasdomain.ArenaStatusDraft, arenasdomain.Slug{}, 1, b.Now(), nil, nil,
	)
	return err
}

// InvalidPositionChangeToTheSamePosition records a change that changes nothing,
// which would put a line in the chain that no reader could explain.
func (b *Builder) InvalidPositionChangeToTheSamePosition() error {
	b.t.Helper()
	agreed, err := positionsdomain.ParsePosition(string(positionsdomain.PositionAgree))
	if err != nil {
		return err
	}
	arenaID, err := positionsdomain.ParseArenaID(b.Identifier())
	if err != nil {
		return err
	}
	accountID, err := positionsdomain.ParseAccountID(b.Identifier())
	if err != nil {
		return err
	}
	_, err = positionsdomain.NewPositionChange(arenaID, accountID, agreed, agreed, 2, b.Now())
	return err
}

// InvalidArgumentWithoutContent publishes an argument whose text is empty or
// whitespace only.
func (b *Builder) InvalidArgumentWithoutContent() error {
	b.t.Helper()
	_, err := argumentsdomain.ParseContent("   ", text.GraphemeCount)
	return err
}

// InvalidArgumentWithoutGraphemeCounter publishes an argument without the
// counter that measures its cost, which is a publication the product would have
// to price blindly.
func (b *Builder) InvalidArgumentWithoutGraphemeCounter() error {
	b.t.Helper()
	_, err := argumentsdomain.ParseContent(defaultArgumentContent, nil)
	return err
}

// InvalidArgumentSourceWithoutAddress cites a source that carries no address.
func (b *Builder) InvalidArgumentSourceWithoutAddress() error {
	b.t.Helper()
	_, err := argumentsdomain.ParseSource("", defaultSourceSummary)
	return err
}

// InvalidArgumentWithoutIdempotencyKey publishes an argument with no retry key,
// which makes a repeated submission a second publication.
func (b *Builder) InvalidArgumentWithoutIdempotencyKey() error {
	b.t.Helper()
	_, err := argumentsdomain.ParseIdempotencyKey("")
	return err
}

// InvalidWalletNegativeInk builds a negative INK quantity.
func (b *Builder) InvalidWalletNegativeInk() error {
	b.t.Helper()
	_, err := walletdomain.NewInk(-1)
	return err
}

// InvalidWalletCreditOfNothing credits an amount of zero, which is a ledger
// line without a movement.
func (b *Builder) InvalidWalletCreditOfNothing() error {
	b.t.Helper()
	account, err := b.VerifiedAccount()
	if err != nil {
		return err
	}
	_, err = b.CreditInk(account, 0)
	return err
}

// InvalidEntitlementOverConsumed stores a pass lot that has consumed more
// passes than it was ever granted.
func (b *Builder) InvalidEntitlementOverConsumed() error {
	b.t.Helper()
	quantity, err := billingdomain.NewQuantity(1)
	if err != nil {
		return err
	}
	reference, err := billingdomain.ParseReference(defaultReference)
	if err != nil {
		return err
	}
	_, err = billingdomain.ReconstitutePassLot(
		billingdomain.LotID(b.Identifier()), billingdomain.AccountID(b.Identifier()),
		billingdomain.OriginPurchase, quantity, 2, nil, reference, b.Now(),
	)
	return err
}

// InvalidBillingPriceThatIsNegative prices a product below zero.
//
// Zero is a valid amount — the domain accepts it and the catalog has legitimate
// free products — so the contravention here is the negative one, which no
// catalog may carry.
func (b *Builder) InvalidBillingPriceThatIsNegative() error {
	b.t.Helper()
	_, err := billingdomain.NewMoney(-1, billingdomain.CurrencyBRL)
	return err
}

// InvalidBillingProductOutsideTheVocabulary names a product with characters the
// catalog does not admit.
func (b *Builder) InvalidBillingProductOutsideTheVocabulary() error {
	b.t.Helper()
	_, err := billingdomain.ParseProductID("Arena Pass!")
	return err
}

// InvalidBillingProductInTheWrongCurrency prices a Brazilian product in the
// currency of the other market.
func (b *Builder) InvalidBillingProductInTheWrongCurrency() error {
	b.t.Helper()
	amount, err := billingdomain.NewMoney(defaultAmountMinorUnits, billingdomain.CurrencyUSD)
	if err != nil {
		return err
	}
	productID, err := billingdomain.ParseProductID(defaultProductID)
	if err != nil {
		return err
	}
	grant, err := billingdomain.NewArenaPassGrant(defaultPassQuantity)
	if err != nil {
		return err
	}
	_, err = billingdomain.NewProduct(billingdomain.MarketBrazil, productID, amount, grant, "")
	return err
}

// InvalidReportWithoutReason files a report under a reason outside the closed
// vocabulary.
func (b *Builder) InvalidReportWithoutReason() error {
	b.t.Helper()
	account, err := b.VerifiedAccount()
	if err != nil {
		return err
	}
	_, err = b.Report(account, defaultReportTarget, b.Identifier(), WithReportReason("not-a-reason"))
	return err
}

// InvalidReportWithoutTarget files a report that names no target.
func (b *Builder) InvalidReportWithoutTarget() error {
	b.t.Helper()
	account, err := b.VerifiedAccount()
	if err != nil {
		return err
	}
	_, err = b.Report(account, defaultReportTarget, "   ")
	return err
}

// InvalidDecisionWithoutRule decides a case without citing the rule it applies,
// which is the state the audit trail exists to prevent.
func (b *Builder) InvalidDecisionWithoutRule() error {
	b.t.Helper()
	_, err := b.Decision(moderationdomain.TargetProfile, string(moderationdomain.ActionWarning), WithDecisionRule(""))
	return err
}

// InvalidDecisionSanctioningTheWrongTarget suspends an Arena, which the policy
// only allows for a profile.
func (b *Builder) InvalidDecisionSanctioningTheWrongTarget() error {
	b.t.Helper()
	_, err := b.Decision(moderationdomain.TargetArena, string(moderationdomain.ActionSuspension))
	return err
}

// InvalidJobWithoutPayload queues work that carries no parameters, which no
// handler could run.
func (b *Builder) InvalidJobWithoutPayload() error {
	b.t.Helper()
	job := jobsdomain.Job{
		ID:          b.Identifier(),
		Type:        jobsdomain.TypeEmailDelivery,
		Version:     1,
		State:       jobsdomain.StateQueued,
		AvailableAt: b.Now(),
		MaxAttempts: jobsdomain.DefaultMaxAttempts,
		CreatedAt:   b.Now(),
		UpdatedAt:   b.Now(),
	}
	return job.Validate()
}

// InvalidJobOfUnknownType queues a workload outside the closed vocabulary,
// which the worker could not resolve to a handler.
func (b *Builder) InvalidJobOfUnknownType() error {
	b.t.Helper()
	_, err := b.Job(jobsdomain.JobType("not_a_job"))
	return err
}

// InvalidJobLeasedWithoutLease marks work as leased while carrying no lease,
// which is the incoherence that hides a job from every worker.
func (b *Builder) InvalidJobLeasedWithoutLease() error {
	b.t.Helper()
	job, err := b.Job(jobsdomain.TypeEmailDelivery)
	if err != nil {
		return err
	}
	job.State = jobsdomain.StateLeased
	return job.Validate()
}

// InvalidJobPayloadThatIsNotAnObject queues a payload the queue cannot read as
// parameters.
func (b *Builder) InvalidJobPayloadThatIsNotAnObject() error {
	b.t.Helper()
	job := jobsdomain.Job{
		ID:          b.Identifier(),
		Type:        jobsdomain.TypeEmailDelivery,
		Version:     1,
		Payload:     []byte(`["not","an","object"]`),
		State:       jobsdomain.StateQueued,
		AvailableAt: b.Now(),
		MaxAttempts: jobsdomain.DefaultMaxAttempts,
		CreatedAt:   b.Now(),
		UpdatedAt:   b.Now(),
	}
	return job.Validate()
}
