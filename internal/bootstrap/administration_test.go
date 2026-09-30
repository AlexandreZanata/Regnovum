package bootstrap_test

// The local administration of the installation (P19-T09): what the composition
// needs, what it does against real PostgreSQL, and the guard that keeps the
// capability off the network.
//
// The end-to-end cases run the composed object, not the use case: they prove
// the wiring — the identity adapter, the assignment adapter, the audit bridge
// and the transaction manager — and they read the rows the promotion left in
// app.admin_roles and app.audit_events. A promotion that the trail did not
// record is the failure this test exists to catch.

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/bootstrap"
	moderationapp "github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

func administrationLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestComposeAdministrationFailsClosedWithoutTheEdges(t *testing.T) {
	pool := dbtest.New(t).Pool.Pool()
	clock := clockseed.NewClock()
	logger := administrationLogger()

	cases := []struct {
		name    string
		options bootstrap.Options
		missing string
	}{
		{
			name:    "no logger",
			options: bootstrap.Options{Clock: clock, Pool: pool},
			missing: "logger",
		},
		{
			name:    "no clock",
			options: bootstrap.Options{Logger: logger, Pool: pool},
			missing: "clock",
		},
		{
			name:    "no pool",
			options: bootstrap.Options{Logger: logger, Clock: clock},
			missing: "postgres pool",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			administration, err := bootstrap.ComposeAdministration(testCase.options)
			if !errors.Is(err, bootstrap.ErrIncompleteComposition) {
				t.Fatalf("err = %v, want %v", err, bootstrap.ErrIncompleteComposition)
			}
			if !strings.Contains(err.Error(), testCase.missing) {
				t.Fatalf("err = %v, want it to name %q", err, testCase.missing)
			}
			if administration != nil {
				t.Fatal("a refused composition must not return an administration")
			}
		})
	}
}

func TestAdministrationPromotesDemotesAndRecordsAgainstPostgreSQL(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()

	qualified := seedAdministrationAccount(t, ctx, pool, "first-administrator@arena.example.com", true, true, true)
	administration := composeAdministration(t, pool)

	result, err := administration.GrantFirstAdministrator(ctx, "first-administrator@arena.example.com")
	if err != nil {
		t.Fatalf("GrantFirstAdministrator: %v", err)
	}
	if string(result.AccountID) != qualified || result.Role.String() != "admin" {
		t.Fatalf("result = %+v, want the seeded account promoted to admin", result)
	}

	assertStoredAssignment(t, ctx, pool, qualified, true)
	assertAuditEvent(t, ctx, pool, "administration.role_granted", qualified, map[string]string{
		"previous_status": "none",
		"new_status":      "admin",
		"rule":            "first_administrator",
	})

	// Replay: the installation now has an administrator, so the same command
	// is refused and nothing changes. This is what makes the command a
	// bootstrap rather than a way to grant the role.
	if _, err := administration.GrantFirstAdministrator(ctx, "first-administrator@arena.example.com"); !errors.Is(err, moderationapp.ErrAdministratorAlreadyExists) {
		t.Fatalf("replay err = %v, want %v", err, moderationapp.ErrAdministratorAlreadyExists)
	}
	assertAuditCount(t, ctx, pool, "administration.role_granted", 1)

	// The demotion is audited by the same trail and returns the installation
	// to the state the bootstrap requires.
	if _, err := administration.RevokeAdministrator(ctx, "first-administrator@arena.example.com"); err != nil {
		t.Fatalf("RevokeAdministrator: %v", err)
	}
	assertStoredAssignment(t, ctx, pool, qualified, false)
	assertAuditEvent(t, ctx, pool, "administration.role_revoked", qualified, map[string]string{
		"previous_status": "admin",
		"new_status":      "revoked",
	})

	if _, err := administration.RevokeAdministrator(ctx, "first-administrator@arena.example.com"); !errors.Is(err, moderationapp.ErrNoActiveAssignment) {
		t.Fatalf("replayed demotion err = %v, want %v", err, moderationapp.ErrNoActiveAssignment)
	}
	assertAuditCount(t, ctx, pool, "administration.role_revoked", 1)

	// With the assignment revoked the installation has no administrator
	// again, so the recovery path works: the same account is promoted and the
	// trail says the assignment it revived was revoked.
	revived, err := administration.GrantFirstAdministrator(ctx, "first-administrator@arena.example.com")
	if err != nil {
		t.Fatalf("second GrantFirstAdministrator: %v", err)
	}
	if !revived.Revived {
		t.Fatal("Revived = false, want true after a demotion")
	}
	assertStoredAssignment(t, ctx, pool, qualified, true)
	assertAuditEvent(t, ctx, pool, "administration.role_granted", qualified, map[string]string{
		"previous_status": "revoked",
		"rule":            "first_administrator",
	})
}

func TestAdministrationRefusesTargetsThatDoNotQualifyAgainstPostgreSQL(t *testing.T) {
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	administration := composeAdministration(t, pool)

	cases := []struct {
		name  string
		email string
		want  error
	}{
		{
			name:  "the address identifies no account",
			email: "nobody@arena.example.com",
			want:  moderationapp.ErrAdministrationTargetNotFound,
		},
		{
			name:  "the address is not an address",
			email: "not-an-address",
			want:  moderationapp.ErrInvalidAdministrationTarget,
		},
		{
			name:  "the email was never verified",
			email: "unverified@arena.example.com",
			want:  moderationapp.ErrAdministrationTargetEmailUnverified,
		},
		{
			name:  "the account cannot authenticate",
			email: "suspended@arena.example.com",
			want:  moderationapp.ErrAdministrationTargetNotActive,
		},
		{
			name:  "the account holds no confirmed second factor",
			email: "no-factor@arena.example.com",
			want:  moderationapp.ErrAdministrationTargetWithoutSecondFactor,
		},
	}

	seedAdministrationAccount(t, ctx, pool, "unverified@arena.example.com", false, true, true)
	seedAdministrationAccount(t, ctx, pool, "suspended@arena.example.com", true, false, true)
	seedAdministrationAccount(t, ctx, pool, "no-factor@arena.example.com", true, true, false)

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := administration.GrantFirstAdministrator(ctx, testCase.email)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("err = %v, want %v", err, testCase.want)
			}
			if result != nil {
				t.Fatalf("result = %+v, want nil", result)
			}
		})
	}

	var assignments int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.admin_roles`).Scan(&assignments); err != nil {
		t.Fatalf("count assignments: %v", err)
	}
	if assignments != 0 {
		t.Fatalf("assignments = %d, want none: every case above was refused before any write", assignments)
	}
	assertAuditCount(t, ctx, pool, "administration.role_granted", 0)
}

// administrationIdentifiers are the names that would make the capability
// reachable from a surface. The guard below fails if they appear in an HTTP or
// HTML adapter, which is the code-level form of "the command is not exposed
// over the network".
var administrationIdentifiers = []string{
	"NewGrantFirstAdministratorUseCase",
	"NewRevokeAdministratorUseCase",
	"GrantFirstAdministratorUseCase",
	"RevokeAdministratorUseCase",
	"ComposeAdministration",
}

func TestNoHTTPAdapterNamesTheAdministration(t *testing.T) {
	root := repositoryRoot(t)
	fileSet := token.NewFileSet()

	var violations []string
	for _, directory := range []string{filepath.Join(root, "internal"), filepath.Join(root, "cmd")} {
		walkErr := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			source, parseErr := parser.ParseFile(fileSet, path, nil, 0)
			if parseErr != nil {
				t.Errorf("parse %s: %v", path, parseErr)
				return nil
			}

			relative, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			slashPath := filepath.ToSlash(relative)
			if !strings.Contains(slashPath, "adapters/http") && !strings.Contains(slashPath, "adapters/html") {
				return nil
			}

			ast.Inspect(source, func(node ast.Node) bool {
				identifier, isIdentifier := node.(*ast.Ident)
				if !isIdentifier {
					return true
				}
				if containsString(administrationIdentifiers, identifier.Name) {
					violations = append(violations, slashPath+": "+identifier.Name)
				}
				return true
			})
			return nil
		})
		if walkErr != nil {
			t.Fatalf("walk %s: %v", directory, walkErr)
		}
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf(
			"the local administration is named by an HTTP surface, which would make promoting an administrator a request:\n%s",
			strings.Join(violations, "\n"),
		)
	}
}

func TestNoRegisteredRouteAdministersRoles(t *testing.T) {
	vocabulary := []string{"role", "administrator"}

	for _, route := range httpserver.RegisteredRoutes() {
		lowered := strings.ToLower(route.Path)
		for _, word := range vocabulary {
			if strings.Contains(lowered, word) {
				t.Fatalf(
					"route %s %s carries the administration vocabulary; the assignment of roles is not a request",
					route.Method, route.Path,
				)
			}
		}
	}
}

// composeAdministration builds the local administration over the test pool,
// with the clock the process would use.
func composeAdministration(t *testing.T, pool *pgxpool.Pool) *bootstrap.Administration {
	t.Helper()

	administration, err := bootstrap.ComposeAdministration(bootstrap.Options{
		Env:    config.EnvTest,
		Logger: administrationLogger(),
		Pool:   pool,
		Clock:  clockseed.NewClock(),
	})
	if err != nil {
		t.Fatalf("ComposeAdministration: %v", err)
	}
	return administration
}

// seedAdministrationAccount creates an account with the facts the promotion
// reads, directly in the schema: an account that verified its address (or not),
// that can sign in (or is suspended) and that holds a confirmed second factor
// (or holds none).
func seedAdministrationAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, email string, verified, active, confirmedFactor bool) string {
	t.Helper()

	status := "active"
	verifiedAt := "now()"
	if !active {
		status = "suspended"
	}
	if !verified {
		status = "pending"
		verifiedAt = "NULL"
	}

	var accountID string
	statement := `INSERT INTO app.accounts (email, status, email_verified_at) VALUES ($1, $2, ` + verifiedAt + `) RETURNING id::text`
	if err := pool.QueryRow(ctx, statement, email, status).Scan(&accountID); err != nil {
		t.Fatalf("seed account %s: %v", email, err)
	}

	if confirmedFactor {
		if _, err := pool.Exec(ctx,
			`INSERT INTO app.account_mfa (account_id, secret_sealed, confirmed_at, last_accepted_step) VALUES ($1, '\x00'::bytea, now(), 0)`,
			accountID,
		); err != nil {
			t.Fatalf("seed second factor %s: %v", email, err)
		}
	}
	return accountID
}

func assertStoredAssignment(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID string, active bool) {
	t.Helper()

	var role string
	var revokedAt *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT role, revoked_at FROM app.admin_roles WHERE account_id = $1`, accountID,
	).Scan(&role, &revokedAt); err != nil {
		t.Fatalf("read assignment: %v", err)
	}
	if role != "admin" {
		t.Fatalf("role = %q, want admin", role)
	}
	if active && revokedAt != nil {
		t.Fatalf("revoked_at = %s, want NULL for an active assignment", revokedAt)
	}
	if !active && revokedAt == nil {
		t.Fatal("revoked_at = NULL, want the demotion to date it")
	}
}

func assertAuditEvent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, action, accountID string, metadata map[string]string) {
	t.Helper()

	var targetType, targetID, reasonCode string
	var raw []byte
	if err := pool.QueryRow(ctx,
		`SELECT target_type, target_id, reason_code, metadata FROM app.audit_events WHERE action = $1 ORDER BY occurred_at DESC LIMIT 1`,
		action,
	).Scan(&targetType, &targetID, &reasonCode, &raw); err != nil {
		t.Fatalf("read the %s event: %v", action, err)
	}
	if targetType != "account" || targetID != accountID {
		t.Fatalf("target = %s %s, want account %s", targetType, targetID, accountID)
	}
	if reasonCode == "" {
		t.Fatal("reason_code is empty, which the trail refuses")
	}

	var stored map[string]string
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	for key, want := range metadata {
		if stored[key] != want {
			t.Fatalf("metadata[%q] = %q, want %q (all: %v)", key, stored[key], want, stored)
		}
	}
}

func assertAuditCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, action string, want int) {
	t.Helper()

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.audit_events WHERE action = $1`, action).Scan(&count); err != nil {
		t.Fatalf("count %s events: %v", action, err)
	}
	if count != want {
		t.Fatalf("%s events = %d, want %d", action, count, want)
	}
}

func containsString(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

// repositoryRoot walks up from the test's directory to the module root.
func repositoryRoot(t *testing.T) string {
	t.Helper()

	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("could not find the module root: no go.mod above the test directory")
		}
		directory = parent
	}
}
