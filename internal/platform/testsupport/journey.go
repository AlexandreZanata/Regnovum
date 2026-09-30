package testsupport

import (
	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	argumentsdomain "github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	moderationapp "github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	moderationdomain "github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
	positionsdomain "github.com/AlexandreZanata/Regnovum/internal/positions/domain"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// Wallet is the economy of one scenario: the credit that funds an account, the
// balance it produces and the spend plan a debit is decided with.
type Wallet struct {
	Credit     walletapp.CreditInkCommand
	Balance    walletdomain.Ink
	Allocation walletdomain.Allocation
}

// Moderation is the review side of one scenario: the report a moderator
// receives and the decision they record about it.
type Moderation struct {
	Report   moderationapp.FileReportCommand
	Decision Decision
}

// Journey is the minimal complete journey of the product, as the builders
// compose it: one author with a confirmed address and a session, an Arena they
// published, the position they confirmed on it, the argument they publish, the
// wallet that pays for that publication, the Arena Pass that entitles them to
// publish, the catalog the pass is sold from, the moderation case the
// publication can be reported in, and the queue that carries the work the
// journey schedules.
//
// It is deliberately a journey of *inputs*: composing it writes nothing, so a
// scenario can take it apart, replace one noun and assert on the rest. What it
// proves is that the nouns agree with one another — the same author, the same
// Arena, the same instant, the same identifiers — which is the way a set of
// builders fails.
type Journey struct {
	Author    *identitydomain.Account
	Moderator *identitydomain.Account
	Session   *identitydomain.Session
	Arena     *arenasdomain.Arena
	Position  positionsdomain.PositionChange
	Argument  Argument
	// ArgumentID is the identifier the storage layer will assign to the
	// argument above, so a scenario can name it before it exists.
	ArgumentID argumentsdomain.ArgumentID
	Wallet     Wallet
	// Entitlement is the Arena Pass the author publishes the Arena with.
	Entitlement *billingdomain.PassLot
	// Billing is the catalog the entitlement is sold from.
	Billing    *billingdomain.Catalog
	Moderation Moderation
	Job        jobsdomain.Job
}

// Journey builds the minimal complete journey. Every step goes through the
// builder of its noun, so a journey is exactly as valid as its parts, and the
// identifiers are threaded from one noun to the next: the Arena belongs to the
// author, the position and the argument are on that Arena, the report names the
// argument, and the wallet, the entitlement and the job belong to the author.
func (b *Builder) Journey() (Journey, error) {
	b.t.Helper()

	author, err := b.VerifiedAccount()
	if err != nil {
		return Journey{}, err
	}
	moderator, err := b.VerifiedAccount()
	if err != nil {
		return Journey{}, err
	}
	session, err := b.Session(author)
	if err != nil {
		return Journey{}, err
	}
	arena, err := b.ArenaPublished(author)
	if err != nil {
		return Journey{}, err
	}
	position, err := b.PositionChange(arena, author)
	if err != nil {
		return Journey{}, err
	}
	argument, err := b.Argument(arena, author)
	if err != nil {
		return Journey{}, err
	}
	argumentID, err := argumentsdomain.ParseArgumentID(b.Identifier())
	if err != nil {
		return Journey{}, err
	}

	credit, err := b.CreditInk(author, defaultInkAmount)
	if err != nil {
		return Journey{}, err
	}
	balance, err := b.Ink(credit.Amount)
	if err != nil {
		return Journey{}, err
	}
	allocation, err := b.Allocation(credit.Amount, 0)
	if err != nil {
		return Journey{}, err
	}
	entitlement, err := b.PassLot(author)
	if err != nil {
		return Journey{}, err
	}
	catalog, err := b.Catalog()
	if err != nil {
		return Journey{}, err
	}
	report, err := b.Report(moderator, defaultReportTarget, argumentID.String())
	if err != nil {
		return Journey{}, err
	}
	// The decision is recorded over the target the report names, which is the
	// pair a review queue hands a moderator.
	target, err := moderationdomain.ParseTargetType(report.Target)
	if err != nil {
		return Journey{}, err
	}
	decision, err := b.Decision(target, string(moderationdomain.ActionWarning))
	if err != nil {
		return Journey{}, err
	}
	job, err := b.Job(jobsdomain.TypeEmailDelivery)
	if err != nil {
		return Journey{}, err
	}

	return Journey{
		Author:      author,
		Moderator:   moderator,
		Session:     session,
		Arena:       arena,
		Position:    position,
		Argument:    argument,
		ArgumentID:  argumentID,
		Wallet:      Wallet{Credit: credit, Balance: balance, Allocation: allocation},
		Entitlement: entitlement,
		Billing:     catalog,
		Moderation:  Moderation{Report: report, Decision: decision},
		Job:         job,
	}, nil
}
