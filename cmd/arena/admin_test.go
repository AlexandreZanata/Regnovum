package main

// The local administration command (P19-T09). What the tests pin: the command
// states what it needs and refuses without it, a change is never authorized by
// silence, and the full path — configuration, pool, composition, promotion and
// the trail — works against real PostgreSQL.

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

func TestRunAdminExplainsItselfWithoutASubcommand(t *testing.T) {
	stdout, _, err := runForTest(t, "admin")
	if err != nil {
		t.Fatalf("run admin: %v", err)
	}
	for _, expected := range []string{"arena admin bootstrap", "arena admin revoke", "reachable over HTTP"} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("usage does not mention %q:\n%s", expected, stdout)
		}
	}
}

func TestRunAdminRejectsUnknownSubcommandsAndOptions(t *testing.T) {
	_, _, err := runForTest(t, "admin", "grant")
	assertError(t, err, `unknown admin subcommand "grant"`)

	_, _, err = runForTest(t, "admin", "bootstrap", "--email", "first@arena.example.com", "--force")
	assertError(t, err, `unknown admin option "--force"`)
}

func TestRunAdminRequiresTheAddress(t *testing.T) {
	_, _, err := runForTest(t, "admin", "bootstrap")
	assertError(t, err, "requires --email")

	_, _, err = runForTest(t, "admin", "revoke", "--email", "   ")
	assertError(t, err, "requires --email")
}

func TestRunAdminRefusesWithoutConfirmation(t *testing.T) {
	// The test process has no answer to give on stdin, so whichever refusal
	// the command chooses, the invariant is the one that matters: silence
	// never authorizes a privilege change, and no assignment is written.
	t.Setenv("ARENA_DATABASE_URL", "")

	_, _, err := runForTest(t, "admin", "bootstrap", "--email", "first@arena.example.com")
	assertError(t, err, "refusing to bootstrap first@arena.example.com")
}

func TestRunAdminRequiresTheDatabase(t *testing.T) {
	t.Setenv("ARENA_DATABASE_URL", "")

	_, _, err := runForTest(t, "admin", "revoke", "--email", "first@arena.example.com", "--yes")
	assertError(t, err, "ARENA_DATABASE_URL is required for arena admin revoke")
}

func TestConfirmAddressAcceptsOnlyTheAddressItself(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "the address typed back", input: "first@arena.example.com\n", want: true},
		{name: "the address with surrounding space", input: "  first@arena.example.com  \n", want: true},
		{name: "another address", input: "second@arena.example.com\n", want: false},
		{name: "an empty line", input: "\n", want: false},
		{name: "nothing at all", input: "", want: false},
		{name: "a bare yes", input: "yes\n", want: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := confirmAddress(strings.NewReader(testCase.input), "bootstrap", "first@arena.example.com")
			if testCase.want && err != nil {
				t.Fatalf("err = %v, want the confirmation accepted", err)
			}
			if !testCase.want && err == nil {
				t.Fatal("err = nil, want the confirmation refused")
			}
		})
	}
}

func TestRunAdminBootstrapPromotesAndRecordsAgainstPostgreSQL(t *testing.T) {
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	t.Setenv("ARENA_DATABASE_URL", testDB.DSN)
	t.Setenv("ARENA_ENV", "test")

	const email = "cli-first-administrator@arena.example.com"
	accountID := seedCLIAdministrator(t, pool, email)

	stdout, _, err := runForTest(t, "admin", "bootstrap", "--email", email, "--yes")
	if err != nil {
		t.Fatalf("run admin bootstrap: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, accountID) || !strings.Contains(stdout, "first administrator") {
		t.Fatalf("stdout does not report the promotion:\n%s", stdout)
	}

	var role string
	var revokedAt *string
	if err := pool.QueryRow(context.Background(),
		`SELECT role, revoked_at::text FROM app.admin_roles WHERE account_id = $1`, accountID,
	).Scan(&role, &revokedAt); err != nil {
		t.Fatalf("read assignment: %v", err)
	}
	if role != "admin" || revokedAt != nil {
		t.Fatalf("assignment = %s revoked_at %v, want an active admin", role, revokedAt)
	}

	var action, reason string
	if err := pool.QueryRow(context.Background(),
		`SELECT action, reason_code FROM app.audit_events ORDER BY occurred_at DESC LIMIT 1`,
	).Scan(&action, &reason); err != nil {
		t.Fatalf("read the audit event: %v", err)
	}
	if action != "administration.role_granted" || reason != "administrative_bootstrap" {
		t.Fatalf("audit event = %s %s, want the recorded bootstrap", action, reason)
	}

	// Replaying the command is refused, and the trail keeps one fact: the
	// command is a bootstrap, not a way to grant the role again.
	_, _, err = runForTest(t, "admin", "bootstrap", "--email", email, "--yes")
	assertError(t, err, "already has an administrator")

	var facts int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM app.audit_events WHERE action = 'administration.role_granted'`,
	).Scan(&facts); err != nil {
		t.Fatalf("count audit events: %v", err)
	}
	if facts != 1 {
		t.Fatalf("recorded promotions = %d, want one", facts)
	}

	// The demotion is the reversal the phase's gate asks for, and it is
	// recorded in the same trail.
	stdout, _, err = runForTest(t, "admin", "revoke", "--email", email, "--yes")
	if err != nil {
		t.Fatalf("run admin revoke: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "bootstrap is available again") {
		t.Fatalf("stdout does not report the demotion:\n%s", stdout)
	}
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM app.audit_events WHERE action = 'administration.role_revoked'`,
	).Scan(&facts); err != nil {
		t.Fatalf("count demotions: %v", err)
	}
	if facts != 1 {
		t.Fatalf("recorded demotions = %d, want one", facts)
	}
}

// seedCLIAdministrator creates the account the command promotes: verified
// address, able to sign in, and holding a confirmed second factor.
func seedCLIAdministrator(t *testing.T, pool *pgxpool.Pool, email string) string {
	t.Helper()

	ctx := context.Background()
	var accountID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status, email_verified_at) VALUES ($1, 'active', now()) RETURNING id::text`,
		email,
	).Scan(&accountID); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.account_mfa (account_id, secret_sealed, confirmed_at, last_accepted_step) VALUES ($1, '\x00'::bytea, now(), 0)`,
		accountID,
	); err != nil {
		t.Fatalf("seed second factor: %v", err)
	}
	return accountID
}
