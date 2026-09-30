// Tests of the synthetic datasets against real PostgreSQL (P22-T04): the
// fourth validation of the task is that a dataset can be rebuilt from scratch
// in an empty database, and the only honest way to state it is to do it — an
// empty, migrated database, the dataset loaded through the product's own use
// cases and repositories, and every planned row read back afterwards.
//
// The recreation writes nothing of its own: accounts come from the identity
// repository, INK from the wallet credit use case, Arenas from the Arena
// repository, positions and arguments from the two use cases the participation
// surface composes. The single statement in this file is the one that proves
// the premise — that the database the recreation starts from is empty.
package testsupport_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	arenaspostgres "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/postgres"
	arenasapp "github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	argumentseligibility "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/eligibility"
	argumentspostgres "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/postgres"
	argumentswalletdebit "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/walletdebit"
	argumentsapp "github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	argumentsdomain "github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/argon2id"
	identitypostgres "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsupport"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
	"github.com/AlexandreZanata/Regnovum/internal/ports"
	positionseligibility "github.com/AlexandreZanata/Regnovum/internal/positions/adapters/eligibility"
	positionspostgres "github.com/AlexandreZanata/Regnovum/internal/positions/adapters/postgres"
	positionsapp "github.com/AlexandreZanata/Regnovum/internal/positions/application"
	positionsdomain "github.com/AlexandreZanata/Regnovum/internal/positions/domain"
	walletpostgres "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

// datasetSeed is the seed the recreation runs with. It is a literal, so a
// failure names the dataset that produced it and a rerun rebuilds exactly it.
const datasetSeed int64 = 20260923

// TestADatasetIsRebuiltFromScratchInAnEmptyDatabase is the fourth validation:
// every profile is loaded from nothing and verified row by row.
func TestADatasetIsRebuiltFromScratchInAnEmptyDatabase(t *testing.T) {
	for _, profile := range testsupport.Profiles() {
		profile := profile
		t.Run(string(profile), func(t *testing.T) {
			t.Parallel()

			builder := testsupport.NewWithSeed(t, datasetSeed)
			dataset, err := builder.Dataset(profile)
			if err != nil {
				t.Fatalf("Dataset(%s) error = %v", profile, err)
			}
			if findings := dataset.SensitiveFindings(); len(findings) != 0 {
				t.Fatalf("the dataset carries sensitive values: %v", findings)
			}

			database := dbtest.New(t)
			if before := countAccounts(t, database.Pool.Pool()); before != 0 {
				t.Fatalf("the disposable database already carries %d accounts, so the recreation would not start from nothing", before)
			}

			// The recreation runs an hour after the last instant the dataset
			// declares, which is the shape of a dataset: published in the past,
			// read now — and it is why positions and arguments are accepted.
			seed := newSeed(t, database.Pool.Pool(), builder.Now().Add(time.Hour))
			loaded := seed.load(t, dataset)
			seed.proveTheLoadLanded(t, dataset)
			seed.verify(t, dataset, loaded)
		})
	}
}

// proveTheLoadLanded is the control for the recreation: a load that wrote
// nothing would let the same address be created again, so the duplicate
// refusal is what separates "the dataset was rebuilt" from "the dataset was
// walked through".
func (s *seed) proveTheLoadLanded(t *testing.T, dataset *testsupport.Dataset) {
	t.Helper()
	email, err := identitydomain.ParseEmail(dataset.Accounts[0].Email)
	if err != nil {
		t.Fatalf("the first account of the dataset has no usable address: %v", err)
	}
	// The hash is irrelevant here and deliberately not a real one: the account
	// insert is what answers, and it answers before the credential is written.
	_, recreate := s.identity.CreateAccountWithPassword(context.Background(), email, "not-a-hash")
	if !errors.Is(recreate, identityapp.ErrDuplicateEmail) {
		t.Fatalf("creating %s again answered %v, want the duplicate refusal", dataset.Accounts[0].Email, recreate)
	}
}

// countAccounts answers how many accounts the database holds. It is the one
// statement of this file: it exists to prove that the recreation starts from an
// empty database, not to write or to judge the product.
func countAccounts(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var total int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM app.accounts").Scan(&total); err != nil {
		t.Fatalf("count accounts: %v", err)
	}
	return total
}

// seed is the composition the recreation runs on: the module adapters over one
// database, the repositories and use cases the participation surface composes,
// and one explicit clock.
type seed struct {
	pool      *pgxpool.Pool
	clock     ports.Clock
	identity  *identitypostgres.Repository
	wallet    *walletpostgres.Repository
	arenas    *arenaspostgres.Repository
	positions *positionspostgres.Repository
	arguments *argumentspostgres.Repository
}

// newSeed composes the adapters and the deterministic clock the use cases read.
func newSeed(t *testing.T, pool *pgxpool.Pool, instant time.Time) *seed {
	t.Helper()
	return &seed{
		pool:      pool,
		clock:     testsource.NewClock(instant),
		identity:  identitypostgres.NewRepository(pool),
		wallet:    walletpostgres.NewRepository(pool),
		arenas:    arenaspostgres.NewRepository(pool),
		positions: positionspostgres.NewRepository(pool),
		arguments: argumentspostgres.NewRepository(pool),
	}
}

// loadedDataset is what the recreation produced: the identifier the database
// assigned to every planned row, by the index of the plan.
type loadedDataset struct {
	accounts  []string
	arenas    []string
	arguments []string
}

// load rebuilds the whole dataset. Every write goes through the product: the
// credential is hashed by the delivered argon2id hasher, the account is created
// with it and confirmed, the INK comes from the wallet credit use case, the
// Arenas from the Arena repository, and the positions and arguments from the
// use cases the surface composes — so a relation the product refuses is a
// recreation that fails here instead of a state a suite would meet later.
//
// The register and verify use cases are deliberately not driven: the dataset
// declares a state, and the verification-token round trip is a journey the
// account suite drives itself. What the two adapter calls below produce is
// exactly the state that journey ends in.
func (s *seed) load(t *testing.T, dataset *testsupport.Dataset) loadedDataset {
	t.Helper()
	ctx := context.Background()

	hasher, err := argon2id.NewDefault(testsource.NewRandom(dataset.Seed))
	if err != nil {
		t.Fatalf("the password hasher refused the default parameters: %v", err)
	}
	// One hash per declared credential: the accounts that share a password
	// share its hash, and hashing it once per account would buy nothing.
	hashes := make([]string, 0, len(dataset.Credentials))
	for _, credential := range dataset.Credentials {
		hash, hashErr := hasher.HashPassword(credential.Password)
		if hashErr != nil {
			t.Fatalf("hash the credential %d: %v", credential.Index, hashErr)
		}
		hashes = append(hashes, hash)
	}

	credits := walletapp.NewCreditInkUseCase(s.wallet, s.clock)
	loaded := loadedDataset{
		accounts:  make([]string, 0, len(dataset.Accounts)),
		arenas:    make([]string, 0, len(dataset.Arenas)),
		arguments: make([]string, 0, len(dataset.Arguments)),
	}

	for _, account := range dataset.Accounts {
		email, parseErr := identitydomain.ParseEmail(account.Email)
		if parseErr != nil {
			t.Fatalf("account %d: %v", account.Index, parseErr)
		}
		created, createErr := s.identity.CreateAccountWithPassword(ctx, email, hashes[account.CredentialIndex])
		if createErr != nil {
			t.Fatalf("create account %d: %v", account.Index, createErr)
		}
		if verifyErr := s.identity.SetEmailVerified(ctx, created.ID(), account.VerifiedAt); verifyErr != nil {
			t.Fatalf("confirm account %d: %v", account.Index, verifyErr)
		}

		if _, creditErr := credits.Execute(ctx, walletapp.CreditInkCommand{
			AccountID:      created.ID().String(),
			Bucket:         walletdomain.BucketFree.String(),
			OperationType:  walletdomain.OperationCreditFree.String(),
			Amount:         account.Ink,
			Reference:      "dataset-" + string(dataset.Profile),
			IdempotencyKey: fmt.Sprintf("dataset-%s-credit-%02d", dataset.Profile, account.Index),
		}); creditErr != nil {
			t.Fatalf("credit account %d with %d INK: %v", account.Index, account.Ink, creditErr)
		}

		loaded.accounts = append(loaded.accounts, created.ID().String())
	}

	policy := arenasdomain.DefaultStatementPolicy()
	for _, arena := range dataset.Arenas {
		statement, parseErr := arenasdomain.ParseStatement(arena.Statement, policy)
		if parseErr != nil {
			t.Fatalf("arena %d statement: %v", arena.Index, parseErr)
		}
		context, parseErr := arenasdomain.ParseContext(
			"Cenário sintético de dataset: nenhuma pessoa, lugar ou fato real.", policy,
		)
		if parseErr != nil {
			t.Fatalf("arena %d context: %v", arena.Index, parseErr)
		}
		category, parseErr := arenasdomain.ParseCategory(arena.Category)
		if parseErr != nil {
			t.Fatalf("arena %d category: %v", arena.Index, parseErr)
		}
		language, parseErr := arenasdomain.ParseLanguage(arena.Language)
		if parseErr != nil {
			t.Fatalf("arena %d language: %v", arena.Index, parseErr)
		}
		slug, parseErr := arenasdomain.ParseSlug(arena.Slug)
		if parseErr != nil {
			t.Fatalf("arena %d slug: %v", arena.Index, parseErr)
		}

		draft, createErr := s.arenas.CreateArena(ctx, arenasapp.CreateArenaRequest{
			CreatorID: arenasdomain.CreatorID(loaded.accounts[arena.Creator]),
			Statement: statement,
			Context:   context,
			Category:  category,
			Language:  language,
		})
		if createErr != nil {
			t.Fatalf("draft arena %d: %v", arena.Index, createErr)
		}
		published, publishErr := s.arenas.PublishArenaDraft(
			ctx, draft.ID(), draft.CreatorID(), slug, arena.PublishedAt, draft.Version(),
		)
		if publishErr != nil {
			t.Fatalf("publish arena %d: %v", arena.Index, publishErr)
		}
		loaded.arenas = append(loaded.arenas, published.ID().String())
	}

	positionEligibility, err := positionseligibility.New(s.identity, s.arenas)
	if err != nil {
		t.Fatalf("the position eligibility bridge refused the readers: %v", err)
	}
	confirm := positionsapp.NewConfirmInitialPositionUseCase(s.positions, positionEligibility, positionEligibility, s.clock)
	for _, position := range dataset.Positions {
		arenaID, parseErr := positionsdomain.ParseArenaID(loaded.arenas[position.Arena])
		if parseErr != nil {
			t.Fatalf("position arena %d: %v", position.Arena, parseErr)
		}
		accountID, parseErr := positionsdomain.ParseAccountID(loaded.accounts[position.Account])
		if parseErr != nil {
			t.Fatalf("position account %d: %v", position.Account, parseErr)
		}
		if _, confirmErr := confirm.Execute(ctx, positionsapp.ConfirmInitialPositionCommand{
			AccountID: accountID.String(),
			ArenaID:   arenaID.String(),
			Position:  position.Position,
		}); confirmErr != nil {
			t.Fatalf("confirm the position of account %d in arena %d: %v", position.Account, position.Arena, confirmErr)
		}
	}

	argumentEligibility, err := argumentseligibility.New(s.identity, s.arenas)
	if err != nil {
		t.Fatalf("the argument eligibility bridge refused the readers: %v", err)
	}
	debits := argumentswalletdebit.New(walletapp.NewDebitInkUseCase(s.wallet, s.clock))
	publish := argumentsapp.NewPublishArgumentUseCase(
		s.arguments,
		argumentEligibility,
		argumentEligibility,
		debits,
		platformpg.NewTxManager(s.pool),
		text.GraphemeCount,
		argumentsdomain.DefaultReplyPolicy(),
		s.clock,
	)
	for index, argument := range dataset.Arguments {
		parentID := ""
		if argument.Parent >= 0 {
			parentID = loaded.arguments[argument.Parent]
		}
		sources := []argumentsapp.SourceCommand{{URL: argument.SourceURL, Description: argument.SourceNote}}
		result, publishErr := publish.Execute(ctx, argumentsapp.PublishArgumentCommand{
			AccountID:      loaded.accounts[argument.Author],
			ArenaID:        loaded.arenas[argument.Arena],
			ParentID:       parentID,
			Relation:       argument.Relation,
			Content:        argument.Content,
			Sources:        sources,
			IdempotencyKey: argument.Key,
		})
		if publishErr != nil {
			t.Fatalf("publish argument %d of arena %d: %v", index, argument.Arena, publishErr)
		}
		if result.Replayed {
			t.Fatalf("argument %d resolved an earlier attempt: the dataset carries a key it already used", index)
		}
		if got := int64(result.Argument.Content.GraphemeCost()); got != argument.InkCost {
			t.Fatalf("argument %d was charged %d INK and the dataset declares %d", index, got, argument.InkCost)
		}
		loaded.arguments = append(loaded.arguments, result.Argument.ID.String())
	}

	return loaded
}

// verify reads the state back row by row. The answer of a use case is not the
// proof that a row was written, so every planned relation is asked of the
// database through the adapter that owns it: the account is active, the Arena
// is public under its slug with the language it declares, the position is the
// one confirmed, the argument carries the content it was charged for, and the
// balance is the credit minus what the account published.
func (s *seed) verify(t *testing.T, dataset *testsupport.Dataset, loaded loadedDataset) {
	t.Helper()
	ctx := context.Background()

	for _, account := range dataset.Accounts {
		email, parseErr := identitydomain.ParseEmail(account.Email)
		if parseErr != nil {
			t.Fatalf("account %d: %v", account.Index, parseErr)
		}
		stored, err := s.identity.GetAccountByEmail(ctx, email)
		if err != nil {
			t.Fatalf("account %d was not readable back: %v", account.Index, err)
		}
		if stored.Status() != identitydomain.AccountStatusActive {
			t.Fatalf("account %d came back %s, want active", account.Index, stored.Status())
		}
		if stored.ID().String() != loaded.accounts[account.Index] {
			t.Fatalf("account %d answers identifier %s and the recreation recorded %s", account.Index, stored.ID(), loaded.accounts[account.Index])
		}
	}

	for _, arena := range dataset.Arenas {
		slug, parseErr := arenasdomain.ParseSlug(arena.Slug)
		if parseErr != nil {
			t.Fatalf("arena %d slug: %v", arena.Index, parseErr)
		}
		stored, err := s.arenas.GetPublicArenaBySlug(ctx, slug)
		if err != nil {
			t.Fatalf("arena %d is not public under its slug: %v", arena.Index, err)
		}
		if stored.Language().String() != arena.Language {
			t.Fatalf("arena %d is stored in %s, want %s", arena.Index, stored.Language(), arena.Language)
		}
		if stored.CreatorID().String() != loaded.accounts[arena.Creator] {
			t.Fatalf("arena %d is owned by %s, want the account %d", arena.Index, stored.CreatorID(), arena.Creator)
		}
	}

	for _, position := range dataset.Positions {
		arenaID, arenaErr := positionsdomain.ParseArenaID(loaded.arenas[position.Arena])
		if arenaErr != nil {
			t.Fatalf("position arena %d: %v", position.Arena, arenaErr)
		}
		accountID, accountErr := positionsdomain.ParseAccountID(loaded.accounts[position.Account])
		if accountErr != nil {
			t.Fatalf("position account %d: %v", position.Account, accountErr)
		}
		stored, err := s.positions.GetByAccountAndArena(ctx, arenaID, accountID)
		if err != nil {
			t.Fatalf("the position of account %d in arena %d was not readable back: %v", position.Account, position.Arena, err)
		}
		if stored.InitialPosition().String() != position.Position {
			t.Fatalf("the position of account %d in arena %d came back %s, want %s", position.Account, position.Arena, stored.InitialPosition(), position.Position)
		}
	}

	for index, argument := range dataset.Arguments {
		argumentID, parseErr := argumentsdomain.ParseArgumentID(loaded.arguments[index])
		if parseErr != nil {
			t.Fatalf("argument %d identifier: %v", index, parseErr)
		}
		stored, err := s.arguments.GetPublicArgument(ctx, argumentID)
		if err != nil {
			t.Fatalf("argument %d was not readable back: %v", index, err)
		}
		if stored.Content == nil {
			t.Fatalf("argument %d came back without its content", index)
		}
		if got := int64(stored.Content.GraphemeCost()); got != argument.InkCost {
			t.Fatalf("argument %d is stored with a cost of %d, want %d", index, got, argument.InkCost)
		}
		if stored.Content.String() != argument.Content {
			t.Fatalf("argument %d came back with different content", index)
		}
		if stored.ParentID.IsZero() != (argument.Parent < 0) {
			t.Fatalf("argument %d answers parent %s and the dataset declares %d", index, stored.ParentID, argument.Parent)
		}
	}

	spent := map[int]int64{}
	for _, argument := range dataset.Arguments {
		spent[argument.Author] += argument.InkCost
	}
	for _, account := range dataset.Accounts {
		balance, err := s.wallet.DerivedBalance(ctx, walletdomain.AccountID(loaded.accounts[account.Index]))
		if err != nil {
			t.Fatalf("the balance of account %d was not readable back: %v", account.Index, err)
		}
		if want := account.Ink - spent[account.Index]; balance.Free.Int64() != want {
			t.Fatalf("account %d holds %d free INK, want %d (%d credited, %d spent)", account.Index, balance.Free.Int64(), want, account.Ink, spent[account.Index])
		}
	}
}
