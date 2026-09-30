package testsupport

import (
	"errors"
	"strings"
	"testing"
	"time"

	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	argumentsdomain "github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	moderationdomain "github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
	positionsdomain "github.com/AlexandreZanata/Regnovum/internal/positions/domain"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
	"github.com/jackc/pgx/v5/pgtype"
)

// TestEveryBuilderAnswersAValidDefault is the first promise of the package: the
// nouns come out in the state the product accepts, without the caller naming
// anything. Every assertion below is the domain's own question about its own
// value, not a comparison against a literal this test invented.
func TestEveryBuilderAnswersAValidDefault(t *testing.T) {
	builder := New(t)

	pending, err := builder.Account()
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if pending.Status() != identitydomain.AccountStatusPending || pending.IsVerified() {
		t.Errorf("Account = %s, verified %v; want a registered account with an unconfirmed address", pending.Status(), pending.IsVerified())
	}
	if !pending.IsEligibleForVerification() {
		t.Error("Account cannot confirm its address, so no journey could start from it")
	}

	verified, err := builder.VerifiedAccount()
	if err != nil {
		t.Fatalf("VerifiedAccount: %v", err)
	}
	if !verified.IsVerified() || !verified.CanAuthenticate() {
		t.Errorf("VerifiedAccount = verified %v, can authenticate %v; want both", verified.IsVerified(), verified.CanAuthenticate())
	}
	if _, err := builder.SuspendedAccount(); err != nil {
		t.Fatalf("SuspendedAccount: %v", err)
	}
	if _, err := builder.DeletedAccount(); err != nil {
		t.Fatalf("DeletedAccount: %v", err)
	}

	session, err := builder.Session(verified)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if session.IsRevoked() || session.IsExpired(builder.Now(), identitydomain.DefaultSessionPolicy()) {
		t.Error("Session is revoked or already expired, so it would authenticate nobody")
	}
	revoked, err := builder.RevokedSession(verified)
	if err != nil {
		t.Fatalf("RevokedSession: %v", err)
	}
	if !revoked.IsRevoked() {
		t.Error("RevokedSession is not revoked")
	}

	draft, err := builder.ArenaDraft(verified)
	if err != nil {
		t.Fatalf("ArenaDraft: %v", err)
	}
	if !draft.IsDraft() || !draft.Slug().IsZero() || draft.PublishedAt() != nil {
		t.Error("ArenaDraft carries a public address or a publication instant; a draft has neither")
	}

	published, err := builder.ArenaPublished(verified, WithArenaClosingIn(72*time.Hour))
	if err != nil {
		t.Fatalf("ArenaPublished: %v", err)
	}
	if published.IsDraft() || published.Slug().IsZero() || published.PublishedAt() == nil {
		t.Error("ArenaPublished has no address or no publication instant")
	}
	if !published.AcceptsParticipation() {
		t.Error("ArenaPublished refuses participation, so no journey could join it")
	}
	if err := published.EnsureAcceptsParticipation(); err != nil {
		t.Errorf("EnsureAcceptsParticipation: %v", err)
	}
	if closesAt := published.ClosesAt(); closesAt == nil || !closesAt.After(*published.PublishedAt()) {
		t.Error("ArenaPublished closes before it was published")
	}

	closed, err := builder.ClosedArena(verified)
	if err != nil {
		t.Fatalf("ClosedArena: %v", err)
	}
	if closed.AcceptsParticipation() {
		t.Error("ClosedArena still accepts participation")
	}

	change, err := builder.PositionChange(published, verified)
	if err != nil {
		t.Fatalf("PositionChange: %v", err)
	}
	undecided, err := positionsdomain.ParsePosition(string(positionsdomain.PositionUndecided))
	if err != nil {
		t.Fatalf("ParsePosition: %v", err)
	}
	current, version, err := positionsdomain.DeriveCurrentPosition(undecided, []positionsdomain.PositionChange{change})
	if err != nil {
		t.Fatalf("DeriveCurrentPosition: %v", err)
	}
	if current.String() != string(positionsdomain.PositionAgree) || version != change.Version() {
		t.Errorf("reconstituted position = %s at version %d; want agree at %d", current, version, change.Version())
	}

	argument, err := builder.Argument(published, verified)
	if err != nil {
		t.Fatalf("Argument: %v", err)
	}
	if argument.Content.IsZero() || !argument.Relation.IsSupported() || argument.IdempotencyKey.IsZero() {
		t.Error("Argument carries an empty content, an unsupported relation or no retry key")
	}
	if len(argument.Sources) != 1 || argument.Sources[0].IsZero() {
		t.Error("Argument carries no parsed source, though the default cites one")
	}
	if argument.Command.ArenaID != published.ID().String() || argument.Command.AccountID != verified.ID().String() {
		t.Error("Argument names an Arena or an author other than the ones it was built for")
	}
	if argument.Command.Content != argument.Content.String() || len(argument.Command.Sources) != len(argument.Sources) {
		t.Error("Argument command and its parsed values disagree")
	}
	if _, err := argumentsdomain.ParseIdempotencyKey(argument.Command.IdempotencyKey); err != nil {
		t.Errorf("the command carries an unparsable retry key: %v", err)
	}

	ink, err := builder.Ink(defaultInkAmount)
	if err != nil {
		t.Fatalf("Ink: %v", err)
	}
	if ink.Int64() != defaultInkAmount {
		t.Errorf("Ink = %d; want %d", ink.Int64(), defaultInkAmount)
	}
	allocation, err := builder.Allocation(600, 400)
	if err != nil {
		t.Fatalf("Allocation: %v", err)
	}
	if total, totalErr := allocation.Total(); totalErr != nil || total.Int64() != 1000 {
		t.Errorf("Allocation total = %v (%v); want 1000", total, totalErr)
	}
	credit, err := builder.CreditInk(verified, defaultInkAmount)
	if err != nil {
		t.Fatalf("CreditInk: %v", err)
	}
	if _, err := walletdomain.ParseBucket(credit.Bucket); err != nil {
		t.Error("the credit names a bucket the wallet refuses")
	}
	if _, err := walletdomain.ParseOperationType(credit.OperationType); err != nil {
		t.Error("the credit names an operation type the wallet refuses")
	}
	if _, err := walletdomain.ParseReference(credit.Reference); err != nil {
		t.Error("the credit carries a reference the wallet refuses")
	}
	if _, err := walletdomain.ParseIdempotencyKey(credit.IdempotencyKey); err != nil {
		t.Error("the credit carries a retry key the wallet refuses")
	}

	entitlement, err := builder.PassLot(verified)
	if err != nil {
		t.Fatalf("PassLot: %v", err)
	}
	if !entitlement.IsAvailable(builder.Now()) || entitlement.Remaining() != defaultPassQuantity {
		t.Errorf("PassLot available %v with %d remaining; want an unused pass", entitlement.IsAvailable(builder.Now()), entitlement.Remaining())
	}
	expiring, err := builder.PassLot(verified, WithPassExpiry(builder.Now().Add(time.Hour)))
	if err != nil {
		t.Fatalf("PassLot with an expiry: %v", err)
	}
	if expiring.IsAvailable(builder.Now().Add(2 * time.Hour)) {
		t.Error("a pass built to expire is still available after its instant")
	}

	catalog, err := builder.Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if catalog.Version() != 1 || len(catalog.Products()) != 1 {
		t.Fatalf("Catalog = version %d with %d products; want one product", catalog.Version(), len(catalog.Products()))
	}
	product := catalog.Products()[0]
	if err := product.Validate(); err != nil {
		t.Errorf("the catalog product is not sellable: %v", err)
	}
	if product.Currency() != product.Amount().Currency() {
		t.Error("the catalog product is priced in a currency other than the market charges")
	}
	if product.Grant().Kind() != billingdomain.GrantKindArenaPass {
		t.Errorf("the catalog product grants %s; want an Arena Pass", product.Grant().Kind())
	}

	report, err := builder.Report(verified, defaultReportTarget, builder.Identifier())
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if _, err := moderationdomain.ParseTargetType(report.Target); err != nil {
		t.Error("the report names a target type the moderation policy refuses")
	}
	if _, err := moderationdomain.ParseReason(report.Reason); err != nil {
		t.Error("the report names a reason the moderation policy refuses")
	}
	if !strings.Contains(report.Context, "sinté") {
		t.Error("the report context is not the synthetic one, so a scenario might carry reading text")
	}

	decision, err := builder.Decision(moderationdomain.TargetArgument, string(moderationdomain.ActionWarning))
	if err != nil {
		t.Fatalf("Decision: %v", err)
	}
	if _, err := moderationdomain.ParseAction(string(decision.Action)); err != nil {
		t.Error("the decision names an action the moderation policy refuses")
	}
	if decision.Rule == "" || decision.Justification == "" {
		t.Error("the decision cites no rule or carries no justification, which the audit trail requires of it")
	}

	job, err := builder.Job(jobsdomain.TypeEmailDelivery)
	if err != nil {
		t.Fatalf("Job: %v", err)
	}
	if err := job.Validate(); err != nil {
		t.Errorf("the queued job does not validate: %v", err)
	}
	if job.State != jobsdomain.StateQueued || job.MaxAttempts != jobsdomain.DefaultMaxAttempts {
		t.Errorf("Job = %s with %d attempts; want queued with the default budget", job.State, job.MaxAttempts)
	}
	if !job.Type.IsValid() {
		t.Errorf("the job names %q, which is outside the workload vocabulary", job.Type)
	}
	if err := jobsdomain.ValidatePayload(job.Payload); err != nil {
		t.Errorf("the job payload is not one the queue accepts: %v", err)
	}
}

// TestTheBuildersShareNoState is the second promise: two values never alias the
// same memory, so mutating what one call answered cannot change what another
// answered — in this test or in a test that runs after it.
func TestTheBuildersShareNoState(t *testing.T) {
	builder := New(t)

	author, err := builder.VerifiedAccount()
	if err != nil {
		t.Fatalf("VerifiedAccount: %v", err)
	}
	arena, err := builder.ArenaPublished(author)
	if err != nil {
		t.Fatalf("ArenaPublished: %v", err)
	}

	first, err := builder.Argument(arena, author)
	if err != nil {
		t.Fatalf("Argument: %v", err)
	}
	second, err := builder.Argument(arena, author)
	if err != nil {
		t.Fatalf("Argument: %v", err)
	}
	if first.Command.IdempotencyKey == second.Command.IdempotencyKey {
		t.Error("two arguments share a retry key, so the product would answer one with a replay")
	}
	if !first.Content.Equals(second.Content) {
		t.Error("two arguments of the same scenario carry different content, though neither overrode one")
	}

	first.Command.Sources[0].URL = "https://example.test/mutated"
	first.Sources[0] = argumentsdomain.Source{}
	if second.Command.Sources[0].URL == "https://example.test/mutated" {
		t.Error("the two argument commands share their source slice")
	}
	if second.Sources[0].IsZero() {
		t.Error("the two parsed source slices share their backing array")
	}

	firstAccount, err := builder.Account()
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	secondAccount, err := builder.Account()
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if firstAccount.ID() == secondAccount.ID() {
		t.Error("two accounts share an identifier")
	}
	if firstAccount.Email().IsZero() {
		t.Error("the built account carries no address")
	}
}

// TestTheSameSeedRebuildsTheSameScenario is what makes a failure replayable:
// two scenarios of one seed are the same scenario, and a different seed is a
// different one — different identifiers and a different instant, not only
// different bytes.
func TestTheSameSeedRebuildsTheSameScenario(t *testing.T) {
	t.Setenv(testsource.SeedVariable, "4242")
	first, err := New(t).Journey()
	if err != nil {
		t.Fatalf("Journey: %v", err)
	}
	replayed, err := New(t).Journey()
	if err != nil {
		t.Fatalf("Journey: %v", err)
	}

	if first.Author.ID() != replayed.Author.ID() || first.Session.ID() != replayed.Session.ID() {
		t.Error("the same seed built two different accounts or sessions")
	}
	if first.Arena.ID() != replayed.Arena.ID() {
		t.Error("the same seed built two different Arenas")
	}
	if first.Arena.PublishedAt() == nil || replayed.Arena.PublishedAt() == nil || !first.Arena.PublishedAt().Equal(*replayed.Arena.PublishedAt()) {
		t.Error("the same seed published at two different instants")
	}
	if first.Argument.Command.Content != replayed.Argument.Command.Content {
		t.Error("the same seed built two different arguments")
	}
	if first.Wallet.Credit.IdempotencyKey != replayed.Wallet.Credit.IdempotencyKey {
		t.Error("the same seed built two different credit commands")
	}
	if first.Job.ID != replayed.Job.ID {
		t.Error("the same seed built two different jobs")
	}

	t.Setenv(testsource.SeedVariable, "4243")
	other, err := New(t).Journey()
	if err != nil {
		t.Fatalf("Journey: %v", err)
	}
	if other.Author.ID() == first.Author.ID() || other.Job.ID == first.Job.ID {
		t.Error("a different seed rebuilt the same identifiers")
	}
	if other.Arena.PublishedAt() == nil || first.Arena.PublishedAt() == nil || other.Arena.PublishedAt().Equal(*first.Arena.PublishedAt()) {
		t.Error("a different seed ran at the same instant, so the seed does not decide the scenario")
	}
}

// TestIdentifiersHaveTheShapeTheStorageLayerRequires keeps the builders from
// answering values the domain accepts and the database refuses: every table of
// the schema keys on a `uuid` generated by the database, and the adapters parse
// identifiers with pgtype.UUID. The proof is the adapter's own parser.
func TestIdentifiersHaveTheShapeTheStorageLayerRequires(t *testing.T) {
	builder := New(t)

	first, second := builder.Identifier(), builder.Identifier()
	for _, identifier := range []string{first, second} {
		var parsed pgtype.UUID
		if err := parsed.Scan(identifier); err != nil || !parsed.Valid {
			t.Fatalf("Identifier() = %q, which the storage layer refuses: %v", identifier, err)
		}
		if len(identifier) != 36 || strings.Count(identifier, "-") != 4 {
			t.Errorf("identifier %q is not in canonical form", identifier)
		}
		// Version 7 is the time-ordered generation the schema uses: the nibble
		// after the second dash carries it.
		if identifier[14] != '7' {
			t.Errorf("identifier %q is not a version 7 UUID, so it does not order by creation", identifier)
		}
	}
	if first == second {
		t.Error("two identifiers are equal, so two rows would collide")
	}

	account, err := builder.Account()
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	var accountUUID pgtype.UUID
	if err := accountUUID.Scan(account.ID().String()); err != nil {
		t.Errorf("the account identifier %q is not storable: %v", account.ID(), err)
	}
}

// TestThePositionChangeAdvancesTheVersion guards the invariant of the position
// aggregate that a hand-written fixture gets wrong most often: the confirmed
// initial position is version one, so a change is version two or later — and
// the domain refuses anything that does not advance it.
func TestThePositionChangeAdvancesTheVersion(t *testing.T) {
	builder := New(t)
	author, err := builder.VerifiedAccount()
	if err != nil {
		t.Fatalf("VerifiedAccount: %v", err)
	}
	arena, err := builder.ArenaPublished(author)
	if err != nil {
		t.Fatalf("ArenaPublished: %v", err)
	}
	change, err := builder.PositionChange(arena, author)
	if err != nil {
		t.Fatalf("PositionChange: %v", err)
	}
	if change.Version() != 2 {
		t.Errorf("PositionChange version = %d; want 2, since the confirmed initial position is version 1", change.Version())
	}
	if !change.ChangedAt().Equal(builder.Now()) {
		t.Error("PositionChange happened at an instant other than the scenario's")
	}
	undecided, err := positionsdomain.ParsePosition(string(positionsdomain.PositionUndecided))
	if err != nil {
		t.Fatalf("ParsePosition: %v", err)
	}
	if _, _, err := positionsdomain.DeriveCurrentPosition(undecided, []positionsdomain.PositionChange{change}); err != nil {
		t.Errorf("the chain the builder answered is not contiguous: %v", err)
	}

	agreed, err := positionsdomain.ParsePosition(string(positionsdomain.PositionAgree))
	if err != nil {
		t.Fatalf("ParsePosition: %v", err)
	}
	arenaID, err := positionsdomain.ParseArenaID(arena.ID().String())
	if err != nil {
		t.Fatalf("ParseArenaID: %v", err)
	}
	accountID, err := positionsdomain.ParseAccountID(author.ID().String())
	if err != nil {
		t.Fatalf("ParseAccountID: %v", err)
	}
	if _, err := positionsdomain.NewPositionChange(arenaID, accountID, agreed, agreed, 2, builder.Now()); !errors.Is(err, positionsdomain.ErrSamePosition) {
		t.Errorf("a change that changes nothing answered %v; want ErrSamePosition", err)
	}
	disagreed, err := positionsdomain.ParsePosition(string(positionsdomain.PositionDisagree))
	if err != nil {
		t.Fatalf("ParsePosition: %v", err)
	}
	if _, err := positionsdomain.NewPositionChange(arenaID, accountID, agreed, disagreed, 1, builder.Now()); !errors.Is(err, positionsdomain.ErrInvalidVersion) {
		t.Errorf("a change that does not advance the version answered %v; want ErrInvalidVersion", err)
	}
}

// TestTheArenaDraftCannotCarryAnAddress is the transition rule the builders rely
// on: the publication is what gives an Arena its address, so a draft has none.
func TestTheArenaDraftCannotCarryAnAddress(t *testing.T) {
	builder := New(t)
	author, err := builder.VerifiedAccount()
	if err != nil {
		t.Fatalf("VerifiedAccount: %v", err)
	}
	draft, err := builder.ArenaDraft(author)
	if err != nil {
		t.Fatalf("ArenaDraft: %v", err)
	}
	slug, err := arenasdomain.ParseSlug(namedSlug)
	if err != nil {
		t.Fatalf("ParseSlug: %v", err)
	}
	if err := draft.Publish(slug, builder.Now()); err != nil {
		t.Fatalf("publishing a valid draft: %v", err)
	}
	if draft.Slug() != slug || draft.PublishedAt() == nil || draft.IsDraft() {
		t.Error("a published Arena does not carry its address and instant")
	}
	if err := draft.Publish(slug, builder.Now()); !errors.Is(err, arenasdomain.ErrInvalidStatusChange) {
		t.Errorf("publishing twice answered %v; want ErrInvalidStatusChange", err)
	}
}

// TestTheDefaultsDoNotCollideWithThemselves guards the two unique indexes a
// scenario meets first: an account carries a unique address and an Arena a
// unique slug, so a builder whose default were a constant would answer two rows
// the database refuses — and the failure would appear in the suite, far from the
// fixture that caused it.
func TestTheDefaultsDoNotCollideWithThemselves(t *testing.T) {
	builder := New(t)

	first, err := builder.VerifiedAccount()
	if err != nil {
		t.Fatalf("VerifiedAccount: %v", err)
	}
	second, err := builder.VerifiedAccount()
	if err != nil {
		t.Fatalf("VerifiedAccount: %v", err)
	}
	if first.Email().String() == second.Email().String() {
		t.Errorf("two accounts share the address %s, which the schema makes unique", first.Email())
	}
	if !strings.HasSuffix(first.Email().String(), "@example.test") {
		t.Errorf("the default address %s is not under the reserved example domain", first.Email())
	}
	firstArena, err := builder.ArenaPublished(first)
	if err != nil {
		t.Fatalf("ArenaPublished: %v", err)
	}
	secondArena, err := builder.ArenaPublished(second)
	if err != nil {
		t.Fatalf("ArenaPublished: %v", err)
	}
	if firstArena.Slug().String() == secondArena.Slug().String() {
		t.Errorf("two Arenas share the slug %s, which the schema makes unique", firstArena.Slug())
	}

	// And a caller that names a value still gets it: the default is a default,
	// not a rule.
	chosen, err := builder.ArenaPublished(first, WithArenaSlug(namedSlug))
	if err != nil {
		t.Fatalf("ArenaPublished with a named slug: %v", err)
	}
	if chosen.Slug().String() != namedSlug {
		t.Errorf("ArenaPublished answered the slug %s; want the one it was given", chosen.Slug())
	}
	addressed, err := builder.Account(WithAccountEmail("owner-escolhido@example.test"))
	if err != nil {
		t.Fatalf("Account with a named address: %v", err)
	}
	if addressed.Email().String() != "owner-escolhido@example.test" {
		t.Errorf("Account answered the address %s; want the one it was given", addressed.Email())
	}
}
