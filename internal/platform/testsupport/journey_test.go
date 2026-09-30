package testsupport

import (
	"testing"
	"time"

	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	moderationdomain "github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
	positionsdomain "github.com/AlexandreZanata/Regnovum/internal/positions/domain"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// TestTheMinimalJourneyComposes is the smoke test the task asks for: the ten
// nouns of a journey are built together, and each one references the right
// neighbour. It is the assertion that catches the failure a set of builders
// really has — every part valid on its own, one part pointing at the wrong
// account, Arena or instant — and it asks the domains themselves where the
// references point.
func TestTheMinimalJourneyComposes(t *testing.T) {
	builder := New(t)
	journey, err := builder.Journey()
	if err != nil {
		t.Fatalf("Journey: %v", err)
	}

	if journey.Author == nil || journey.Moderator == nil || journey.Arena == nil {
		t.Fatal("the journey is missing an author, a moderator or an Arena")
	}
	if journey.Author.ID() == journey.Moderator.ID() {
		t.Error("the author and the moderator are the same account, so a review would be a conflict of interest")
	}
	if journey.Author.Email().String() == journey.Moderator.Email().String() {
		t.Error("two accounts of the journey share an address, which the schema makes unique")
	}

	// Account and session.
	if !journey.Author.IsVerified() || !journey.Author.CanAuthenticate() {
		t.Error("the journey's author cannot authenticate, so no page of the journey would load")
	}
	if journey.Session.AccountID() != journey.Author.ID() {
		t.Error("the session belongs to an account other than the journey's author")
	}
	if journey.Session.IsRevoked() || journey.Session.IsExpired(builder.Now(), identitydomain.DefaultSessionPolicy()) {
		t.Error("the journey's session is closed at the instant the journey runs")
	}

	// Arena, position and argument, all on the same Arena and by the same author.
	if journey.Arena.CreatorID().String() != journey.Author.ID().String() {
		t.Error("the Arena belongs to an account other than the journey's author")
	}
	if err := journey.Arena.EnsureAcceptsParticipation(); err != nil {
		t.Fatalf("the journey's Arena does not accept participation: %v", err)
	}
	if journey.Position.ArenaID().String() != journey.Arena.ID().String() {
		t.Error("the confirmed position names another Arena")
	}
	if journey.Position.AccountID().String() != journey.Author.ID().String() {
		t.Error("the confirmed position belongs to another account")
	}
	if !journey.Position.ChangedAt().Equal(builder.Now()) {
		t.Error("the position was confirmed at an instant other than the journey's")
	}
	undecided, err := positionsdomain.ParsePosition(string(positionsdomain.PositionUndecided))
	if err != nil {
		t.Fatalf("ParsePosition: %v", err)
	}
	current, version, err := positionsdomain.DeriveCurrentPosition(undecided, []positionsdomain.PositionChange{journey.Position})
	if err != nil {
		t.Fatalf("DeriveCurrentPosition: %v", err)
	}
	if current.String() != string(positionsdomain.PositionAgree) || version != journey.Position.Version() {
		t.Errorf("the journey's position chain reconstitutes %s at version %d", current, version)
	}
	if journey.Argument.Command.ArenaID != journey.Arena.ID().String() || journey.Argument.Command.AccountID != journey.Author.ID().String() {
		t.Error("the argument is published on another Arena or by another account")
	}
	if journey.ArgumentID.IsZero() {
		t.Error("the journey names no identifier for the argument it publishes")
	}

	// Wallet, entitlement and billing, all belonging to the author.
	if journey.Wallet.Credit.AccountID != journey.Author.ID().String() {
		t.Error("the credit funds another account")
	}
	if journey.Wallet.Balance.Int64() != journey.Wallet.Credit.Amount {
		t.Error("the journey's balance does not match the credit it was built from")
	}
	if !journey.Wallet.Allocation.FromFree().Equals(journey.Wallet.Balance) {
		t.Error("the spend plan does not draw the balance from the free bucket")
	}
	if total, err := journey.Wallet.Allocation.Total(); err != nil || !total.Equals(journey.Wallet.Balance) {
		t.Errorf("the spend plan totals %v (%v); want the credited balance", total, err)
	}
	if _, err := walletdomain.ParseInk(journey.Wallet.Balance.String()); err != nil {
		t.Errorf("the balance is not a ledger quantity: %v", err)
	}
	if journey.Entitlement.AccountID().String() != journey.Author.ID().String() {
		t.Error("the Arena Pass belongs to another account")
	}
	if !journey.Entitlement.IsAvailable(builder.Now()) {
		t.Error("the Arena Pass cannot be consumed at the instant the journey runs, so the Arena could not be published")
	}
	products := journey.Billing.Products()
	if len(products) != 1 {
		t.Fatalf("the journey's catalog declares %d products; want one", len(products))
	}
	if products[0].Grant().Kind() != "ARENA_PASS" {
		t.Errorf("the catalog sells a %s grant; want the Arena Pass the entitlement consumes", products[0].Grant().Kind())
	}

	// Moderation: the report names the argument the journey publishes, and the
	// decision is recorded over the same target.
	if journey.Moderation.Report.Reporter.String() != journey.Moderator.ID().String() {
		t.Error("the report was filed by an account other than the journey's moderator")
	}
	if journey.Moderation.Report.TargetID != journey.ArgumentID.String() {
		t.Error("the report names another target than the argument the journey publishes")
	}
	target, err := moderationdomain.ParseTargetType(journey.Moderation.Report.Target)
	if err != nil {
		t.Fatalf("the report names an unknown target type: %v", err)
	}
	if !moderationdomain.SanctionAllowed(target, journey.Moderation.Decision.Action) {
		t.Error("the decision sanctions a target the policy does not allow")
	}

	// The queue carries the work the journey schedules, and the workload is one
	// the worker can resolve.
	if err := journey.Job.Validate(); err != nil {
		t.Errorf("the journey's job does not validate: %v", err)
	}
	if journey.Job.State != jobsdomain.StateQueued || !journey.Job.Type.IsValid() {
		t.Errorf("the journey's job is %s of type %s; want a queued, known workload", journey.Job.State, journey.Job.Type)
	}
	if journey.Job.CreatedAt.After(builder.Now()) {
		t.Error("the journey's job was created after the instant the journey runs")
	}

	// And the whole thing advances together: the scenario's clock is the only
	// time any part of the journey knows.
	before := builder.Now()
	builder.Advance(48 * time.Hour)
	if !builder.Now().After(before) {
		t.Error("advancing the scenario did not move its instant")
	}
	if !journey.Entitlement.IsAvailable(builder.Now()) {
		t.Error("a non-expiring pass stopped being available when the scenario advanced")
	}
}
