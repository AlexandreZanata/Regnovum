// The local administration command (P19-T09).
//
// Promoting an administrator is the one act of the product that is not an HTTP
// request: it belongs to an operator on the host, who already holds the
// database DSN, and the process that performs it serves no page. This file is
// the operator's end of that act — the argument parsing and the confirmation —
// and it decides nothing about who may become an administrator: the rules live
// in the moderation use case, which refuses with prose the operator reads.
//
// Three properties are the point of the command, and each is enforced somewhere
// else so that it cannot be lost by editing this file:
//
//   - it is not reachable over the network. There is no route for either
//     subcommand; the capability is composed only here (see
//     internal/bootstrap/administration.go) and the guard in
//     cmd/arena/admin_test.go fails if an HTTP adapter ever names it.
//   - it is confirmed. The operator states the address and then, unless the
//     explicit --yes flag authorizes the change, types the same address back.
//     A promotion is irreversible from the operator's chair in one direction
//     only — the demotion exists, and it is audited by the same trail — and the
//     confirmation is what stops a shell history line from promoting a person
//     by accident.
//   - it is recorded. Every accepted transition writes its audit event in the
//     same transaction as the assignment, so a promotion the trail could not
//     record does not happen.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/bootstrap"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbpool"
	"github.com/AlexandreZanata/Regnovum/internal/platform/logging"
)

const administrationUsage = `arena admin administers the installation from the host.

Usage:

  arena admin bootstrap --email <address> [--yes]
  arena admin revoke    --email <address> [--yes]

bootstrap promotes an account to the installation's first administrator. It
exists so that an installation is not born without one, and it refuses once an
administrator holds an active assignment: the promotion is a bootstrap, not a
way to grant the role to anybody holding the database DSN.

revoke dates the revocation of an administrator's assignment, which is what
returns the installation to the state bootstrap requires.

Both require an address whose account exists, whose email was verified, that can
sign in and that already holds a confirmed second factor: the administrative
surface requires a session that presented one, so promoting an account without
it would create an administrator that cannot act.

Neither subcommand is reachable over HTTP. Without --yes the command asks the
operator to type the address back; --yes is the explicit flag for a run that has
no terminal, and it authorizes the change on its own.`

func runAdmin(args []string, stdout *os.File) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(stdout, administrationUsage)
		return nil
	}

	var subcommand string
	switch args[0] {
	case "bootstrap", "revoke":
		subcommand = args[0]
	default:
		return fmt.Errorf("unknown admin subcommand %q\n\n%s", args[0], administrationUsage)
	}

	address := ""
	authorized := false
	for i := 1; i < len(args); i++ {
		switch {
		case args[i] == "--email" && i+1 < len(args):
			i++
			address = args[i]
		case strings.HasPrefix(args[i], "--email="):
			address = strings.TrimPrefix(args[i], "--email=")
		case args[i] == "--yes":
			authorized = true
		default:
			return fmt.Errorf("unknown admin option %q\n\n%s", args[i], administrationUsage)
		}
	}
	if strings.TrimSpace(address) == "" {
		return fmt.Errorf("arena admin %s requires --email <address>\n\n%s", subcommand, administrationUsage)
	}

	// The confirmation is asked before the database is touched: a command
	// that connected and rolled back would still have opened a session on
	// the production database for a change nobody authorized.
	if err := confirmAdministration(stdout, os.Stdin, subcommand, address, authorized); err != nil {
		return err
	}

	cfg := config.MustLoad()
	dsn := string(cfg.DatabaseURL().Unredacted())
	if dsn == "" {
		return fmt.Errorf(
			"ARENA_DATABASE_URL is required for arena admin %s: the command acts on this installation's database and there is nothing to act on without it",
			subcommand,
		)
	}

	logger := logging.New(stdout, cfg.LogLevel())
	clock := clockseed.NewClock()
	pool, err := dbpool.New(context.Background(), dsn, dbpool.FromConfig(cfg), logger, clock)
	if err != nil {
		return fmt.Errorf("initialize database pool: %w", err)
	}
	defer pool.Close()

	administration, err := bootstrap.ComposeAdministration(bootstrap.Options{
		Env:    cfg.Env(),
		Logger: logger,
		Pool:   pool.Pool(),
		Clock:  clock,
	})
	if err != nil {
		return err
	}

	ctx := context.Background()
	switch subcommand {
	case "bootstrap":
		result, err := administration.GrantFirstAdministrator(ctx, address)
		if err != nil {
			return err
		}
		history := "it held no assignment before"
		if result.Revived {
			history = "it held a revoked assignment before, which this promotion revived"
		}
		fmt.Fprintf(
			stdout,
			"arena admin: account %s is now %s (%s); the installation has its first administrator\n",
			result.AccountID, result.Role, history,
		)
	default:
		result, err := administration.RevokeAdministrator(ctx, address)
		if err != nil {
			return err
		}
		fmt.Fprintf(
			stdout,
			"arena admin: %s no longer holds %s (revoked %s); bootstrap is available again\n",
			result.AccountID, result.Role, result.RevokedAt.UTC().Format("2006-01-02T15:04:05Z"),
		)
	}
	return nil
}

// confirmAdministration requires the operator to authorize the change.
//
// The typed address is the confirmation because it is the thing the operator
// means to change: a yes/no prompt is answered by reflex, and typing the address
// back forces the operator to read what they are about to promote. The
// non-interactive path exists because a deployment may have no terminal, and it
// must be asked for by name: silence never authorizes a privilege change.
func confirmAdministration(stdout, stdin *os.File, subcommand, address string, authorized bool) error {
	if authorized {
		fmt.Fprintf(stdout, "arena admin: %s authorized by --yes for %s\n", subcommand, address)
		return nil
	}

	if !isTerminal(stdin) {
		return fmt.Errorf(
			"arena admin: refusing to %s %s without confirmation; type the address back on a terminal, or pass --yes to authorize a run that has none",
			subcommand, address,
		)
	}

	fmt.Fprintf(stdout, "arena admin: type the address to confirm %s of %s:\n> ", subcommand, address)
	return confirmAddress(stdin, subcommand, address)
}

// confirmAddress reads one line and reports whether it authorizes the change.
// It is separate from the terminal gate so that what counts as a confirmation
// is testable without a terminal.
func confirmAddress(input io.Reader, subcommand, address string) error {
	typed, err := bufio.NewReader(input).ReadString('\n')
	if strings.TrimSpace(typed) == "" {
		if err != nil {
			return fmt.Errorf("arena admin: refusing to %s %s: nothing was typed to confirm it", subcommand, address)
		}
		return fmt.Errorf("arena admin: refusing to %s %s: an empty line does not confirm anything", subcommand, address)
	}
	if strings.TrimSpace(typed) != address {
		return fmt.Errorf("arena admin: refusing to %s %s: the confirmation did not match the address", subcommand, address)
	}
	return nil
}

// isTerminal reports whether the stream is a terminal. A pipe or a file is not
// one, and a question asked there would be read by whatever produced the input.
func isTerminal(file *os.File) bool {
	if file == nil {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
