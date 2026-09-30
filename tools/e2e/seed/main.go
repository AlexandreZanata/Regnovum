// Command seed prepares the state the browser journeys of tools/e2e start
// from (P18-T07).
//
// Why a tool instead of a browser step: the journeys under test begin at a
// state no surface of the product can reach yet. An account can be registered
// and confirmed through the browser, but it cannot be given an INK balance
// without a purchase, and an Arena cannot be published at all — the publication
// is never sold through a page, and the JSON API that would draft it is not
// mounted in the server. Seeding those two facts here keeps the browser specs
// honest: they drive the pages, and this command owns the fixtures.
//
// It composes the same use cases and repositories the process composes. It
// writes no SQL of its own, so a change in the domain is a change in the seed,
// and it is test tooling: it is never part of the delivered application, never
// referenced by web/ and never imported by internal/.
//
// Usage:
//
//	e2e-seed account --email <address> --password <secret> [--ink <amount>]
//	e2e-seed arena   --slug <slug> --creator-email <address> [--statement <text>]
//
// Both commands read ARENA_DATABASE_URL from the environment and print a JSON
// document describing what they created.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/postgres"
	arenasapp "github.com/AlexandreZanata/Regnovum/internal/arenas/application"
	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/argon2id"
	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/fakeemail"
	identitypostgres "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbpool"
	"github.com/AlexandreZanata/Regnovum/internal/platform/logging"
	walletpostgres "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/postgres"
	walletapp "github.com/AlexandreZanata/Regnovum/internal/wallet/application"
	walletdomain "github.com/AlexandreZanata/Regnovum/internal/wallet/domain"
)

const usage = `e2e-seed prepares the state the browser journeys of tools/e2e start from.

Usage:

  e2e-seed account --email <address> --password <secret> [--ink <amount>]
  e2e-seed arena   --slug <slug> --creator-email <address> [--statement <text>]

Both commands read ARENA_DATABASE_URL from the environment, print a JSON
document describing what they created, and are refused in production.

The account command creates a registered, email-confirmed account and
optionally credits it INK, which is what publishing an argument charges.
The arena command creates the account of a creator when needed and publishes
one Arena whose statement comes from --statement.`

// defaultStatement satisfies the versioned statement policy of the Arena
// domain. It is synthetic text: the seed never carries real data.
const defaultStatement = "O debate público melhora quando os argumentos podem ser verificados."

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "e2e-seed:", err)
		os.Exit(1)
	}
}

// errUsage is the sentinel of a bad invocation: the usage text is printed on the
// output, and the error says what is missing without carrying the whole manual.
var errUsage = errors.New("invalid invocation")

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return fmt.Errorf("%w: a subcommand is required", errUsage)
	}

	switch args[0] {
	case "account":
		return runAccount(args[1:], stdout)
	case "arena":
		return runArena(args[1:], stdout)
	case "help", "-h", "-help", "--help":
		fmt.Fprintln(stdout, usage)
		return nil
	default:
		fmt.Fprint(stdout, usage)
		return fmt.Errorf("%w: unknown subcommand %q", errUsage, args[0])
	}
}

// runAccount creates one registered, confirmed account and, when asked, gives
// it the INK balance that a publication charges.
func runAccount(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("account", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	email := flags.String("email", "", "address of the account to create")
	password := flags.String("password", "", "password of the account")
	ink := flags.Int64("ink", 0, "INK to credit, in minor units (0 credits nothing)")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w\n\n%s", err, usage)
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("account takes no positional arguments (got %q)", flags.Args())
	}
	if strings.TrimSpace(*email) == "" {
		return errors.New("--email is required")
	}
	if len(*password) < 8 {
		return errors.New("--password must be at least 8 characters")
	}
	if *ink < 0 {
		return errors.New("--ink cannot be negative")
	}

	return withPool(func(ctx context.Context, session *seed) error {
		accountID, err := session.account(ctx, *email, *password)
		if err != nil {
			return err
		}
		if *ink > 0 {
			if err := session.credit(ctx, accountID, *ink); err != nil {
				return err
			}
		}
		return writeDocument(stdout, map[string]any{
			"account_id": accountID,
			"email":      *email,
			"ink":        *ink,
		})
	})
}

// runArena publishes one Arena owned by a creator account, creating that
// account when it does not exist yet.
func runArena(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("arena", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	slug := flags.String("slug", "", "slug the Arena is published under")
	creatorEmail := flags.String("creator-email", "", "address of the account that owns the Arena")
	statement := flags.String("statement", defaultStatement, "statement of the Arena")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w\n\n%s", err, usage)
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("arena takes no positional arguments (got %q)", flags.Args())
	}
	if strings.TrimSpace(*slug) == "" {
		return errors.New("--slug is required")
	}
	if strings.TrimSpace(*creatorEmail) == "" {
		return errors.New("--creator-email is required")
	}

	parsedSlug, err := arenasdomain.ParseSlug(*slug)
	if err != nil {
		return fmt.Errorf("--slug %q: %w", *slug, err)
	}
	parsedStatement, err := arenasdomain.ParseStatement(*statement, arenasdomain.DefaultStatementPolicy())
	if err != nil {
		return fmt.Errorf("--statement: %w", err)
	}
	// The category and language are the ones the migrations seed and the
	// catalogs render: the seed declares them instead of inventing a pair.
	category, err := arenasdomain.ParseCategory("technology")
	if err != nil {
		return err
	}
	language, err := arenasdomain.ParseLanguage("pt-BR")
	if err != nil {
		return err
	}

	return withPool(func(ctx context.Context, session *seed) error {
		creatorID, err := session.account(ctx, *creatorEmail, session.creatorPassword)
		if err != nil {
			return err
		}

		repository := postgres.NewRepository(session.pool.Pool())
		draft, err := repository.CreateArena(ctx, arenasapp.CreateArenaRequest{
			CreatorID: arenasdomain.CreatorID(creatorID),
			Statement: parsedStatement,
			Category:  category,
			Language:  language,
		})
		if err != nil {
			return fmt.Errorf("create the draft of %q: %w", *slug, err)
		}

		// Published through the repository, not through the use case: the use
		// case consumes a billing pass in the same transaction, and the
		// journeys under test are participation and account, not selling. The
		// bootstrap tests document the same choice.
		published, err := repository.PublishArenaDraft(
			ctx, draft.ID(), draft.CreatorID(), parsedSlug, session.clock.Now(), draft.Version(),
		)
		if err != nil {
			return fmt.Errorf("publish %q: %w", *slug, err)
		}

		return writeDocument(stdout, map[string]any{
			"arena_id":   published.ID().String(),
			"slug":       published.Slug().String(),
			"creator_id": creatorID,
		})
	})
}

// seed is the composition one seeding command runs on.
type seed struct {
	pool            *dbpool.Pool
	clock           clockseed.System
	random          clockseed.CryptoRandom
	logger          *slog.Logger
	creatorPassword string
}

// withPool loads the configuration, refuses production and runs the seeding
// over one pool. Every command goes through it so no subcommand can open a
// database of its own or skip the environment rule.
func withPool(action func(ctx context.Context, session *seed) error) error {
	cfg, err := config.Load(os.Environ())
	if err != nil {
		return err
	}
	if cfg.IsProduction() {
		return errors.New("refused in production: this command creates accounts and Arenas")
	}
	dsn := string(cfg.DatabaseURL().Unredacted())
	if dsn == "" {
		return errors.New("ARENA_DATABASE_URL is required (set it to the PostgreSQL DSN of the disposable database)")
	}

	logger := logging.New(os.Stderr, cfg.LogLevel())
	clock := clockseed.NewClock()
	random := clockseed.NewRandom()

	ctx := context.Background()
	pool, err := dbpool.New(ctx, dsn, dbpool.FromConfig(cfg), logger, clock)
	if err != nil {
		return fmt.Errorf("open the database pool: %w", err)
	}
	defer pool.Close()

	return action(ctx, &seed{
		pool:            pool,
		clock:           clock,
		random:          random,
		logger:          logger,
		creatorPassword: "e2e-seed-creator-password",
	})
}

// account registers an account, confirms its address with the token the
// registration issued and returns the identifier the database assigned to it.
// Running it twice for the same address returns the same account unchanged.
func (session *seed) account(ctx context.Context, email, password string) (string, error) {
	parsed, err := identitydomain.ParseEmail(email)
	if err != nil {
		return "", err
	}

	repository := identitypostgres.NewRepository(session.pool.Pool())
	hasher, err := argon2id.NewDefault(session.random)
	if err != nil {
		return "", fmt.Errorf("password hasher: %w", err)
	}

	// The in-memory sink is enough here: this command owns the process it
	// sends from, and it needs the token exactly once, to confirm the address.
	messages := fakeemail.NewSender()
	register := identityapp.NewRegisterAccountUseCase(
		repository, repository, hasher, messages, session.clock, session.random, identitydomain.DefaultVerificationPolicy(),
	)
	result, err := register.Execute(ctx, identityapp.RegisterAccountCommand{Email: email, Password: password})
	if err != nil {
		return "", fmt.Errorf("register %s: %w", email, err)
	}

	existing, err := repository.GetAccountByEmail(ctx, parsed)
	if err != nil {
		return "", fmt.Errorf("read back %s: %w", email, err)
	}
	if existing.Status() == identitydomain.AccountStatusActive {
		return existing.ID().String(), nil
	}

	token, found := messages.LastTokenForEmail(parsed)
	if !found {
		return "", fmt.Errorf("register %s issued no verification token, and the account is %s", email, existing.Status())
	}

	verify := identityapp.NewVerifyEmailUseCase(repository, repository, session.clock)
	if err := verify.Execute(ctx, identityapp.VerifyEmailCommand{Token: token}); err != nil {
		return "", fmt.Errorf("confirm %s: %w", email, err)
	}

	return result.AccountID, nil
}

// credit gives one account the INK that publishing an argument charges. The
// idempotency key is derived from the account, so seeding the same account
// twice does not double the balance.
func (session *seed) credit(ctx context.Context, accountID string, amount int64) error {
	credits := walletapp.NewCreditInkUseCase(walletpostgres.NewRepository(session.pool.Pool()), session.clock)
	_, err := credits.Execute(ctx, walletapp.CreditInkCommand{
		AccountID:      accountID,
		Bucket:         string(walletdomain.BucketFree),
		OperationType:  walletdomain.OperationCreditFree.String(),
		Amount:         amount,
		Reference:      "e2e-seed",
		IdempotencyKey: "e2e-seed-ink-" + accountID,
	})
	if err != nil {
		return fmt.Errorf("credit %d INK to %s: %w", amount, accountID, err)
	}
	return nil
}

func writeDocument(stdout io.Writer, document map[string]any) error {
	encoded, err := json.Marshal(document)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, string(encoded))
	return err
}
