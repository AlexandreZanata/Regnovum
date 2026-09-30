// The database side of the audit: provisioning databases, reading the catalog
// and turning it into something comparable (P20-T03).
//
// Every database the audit creates is disposable and named after the run, so
// the audit never touches the cluster's real data: the gate points it at a
// throwaway PostgreSQL, and the operator who points it elsewhere gets
// databases that say `arena_migaudit_` in their names and are dropped on every
// exit path.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbmigrate"
)

// runtimeRole is the role the application connects as. The grant rules are
// stated about this role, because it is the one that serves traffic.
const runtimeRole = "arena_app"

// schemaName is the application schema, as dbmigrate creates it.
const schemaName = "app"

// ownerRole owns every object of the schema after provisioning.
const ownerRole = "arena_owner"

// cluster provisions and drops the disposable databases of one audit run.
type cluster struct {
	admin    *sql.DB
	adminDSN string
	prefix   string
	created  []string
	log      io.Writer
}

// openCluster connects to the administrative database. The connection is the
// one that creates and drops databases, and it never carries the schema.
func openCluster(ctx context.Context, adminDSN, prefix string, log io.Writer) (*cluster, error) {
	if err := validateDSN(adminDSN); err != nil {
		return nil, err
	}
	admin, err := sql.Open(dbmigrate.DriverName, adminDSN)
	if err != nil {
		return nil, fmt.Errorf("open administrative connection: %w", err)
	}
	admin.SetMaxOpenConns(2)
	if err := admin.PingContext(ctx); err != nil {
		admin.Close()
		return nil, fmt.Errorf("reach PostgreSQL: %w", err)
	}
	return &cluster{admin: admin, adminDSN: adminDSN, prefix: prefix, log: log}, nil
}

// validateDSN refuses anything that is not a real PostgreSQL URL. SQLite and
// mocks are prohibited by the master plan, and a gate that audited one would be
// measuring the wrong engine's semantics.
func validateDSN(dsn string) error {
	lower := strings.ToLower(dsn)
	if strings.HasPrefix(lower, "sqlite") || strings.Contains(lower, ":memory:") {
		return fmt.Errorf("SQLite is prohibited; PostgreSQL is required")
	}
	if !strings.HasPrefix(lower, "postgres://") && !strings.HasPrefix(lower, "postgresql://") {
		return fmt.Errorf("unsupported DSN scheme: PostgreSQL is required")
	}
	return nil
}

func (c *cluster) name(suffix string) string {
	return c.prefix + "_" + suffix
}

// dsn builds the connection string of one database of the cluster.
func (c *cluster) dsn(name string) (string, error) {
	parsed, err := url.Parse(c.adminDSN)
	if err != nil {
		return "", fmt.Errorf("parse the administrative DSN: %w", err)
	}
	parsed.Path = "/" + name
	return parsed.String(), nil
}

// create makes an empty database, or a copy of another one.
func (c *cluster) create(ctx context.Context, name, template string) error {
	statement := "CREATE DATABASE " + quoteIdentifier(name)
	if template != "" {
		// TEMPLATE is a physical copy of a database that already carries the
		// state of that version — structure, rows and the migration history —
		// which is what a restored snapshot is. The logical dump and restore
		// of a real backup is a different exercise, measured by
		// `make backup-verify` (the P19-T04 gate), and the report says so.
		statement += " TEMPLATE " + quoteIdentifier(template)
	}
	if _, err := c.admin.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("create %s from %q: %w", name, template, err)
	}
	c.created = append(c.created, name)
	return nil
}

// open returns a handle on one of the cluster's databases.
func (c *cluster) open(ctx context.Context, name string) (*sql.DB, error) {
	dsn, err := c.dsn(name)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(dbmigrate.DriverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("reach %s: %w", name, err)
	}
	return db, nil
}

// drop removes one of the cluster's databases, disconnecting whatever is left
// connected to it. A database another session holds open cannot be dropped, and
// a served copy is exactly the state the audit leaves behind when it fails.
//
// It takes no context on purpose: it runs on the cleanup path of a failed or
// cancelled run, so the run's context would be the one thing it must not obey.
func (c *cluster) drop(name string) error {
	// The cleanup context is deliberately independent of the run's: a run that
	// failed or was cancelled must still be able to take its databases away.
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := c.admin.ExecContext(cleanupCtx,
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()",
		name); err != nil {
		return fmt.Errorf("disconnect %s: %w", name, err)
	}
	if _, err := c.admin.ExecContext(cleanupCtx, "DROP DATABASE IF EXISTS "+quoteIdentifier(name)+" WITH (FORCE)"); err != nil {
		return fmt.Errorf("drop %s: %w", name, err)
	}
	for i, created := range c.created {
		if created == name {
			c.created = append(c.created[:i], c.created[i+1:]...)
			break
		}
	}
	return nil
}

// close drops everything this run created.
func (c *cluster) close() {
	for _, name := range append([]string(nil), c.created...) {
		if err := c.drop(name); err != nil {
			fmt.Fprintf(c.log, "migrationaudit: cleanup: %v\n", err)
		}
	}
	c.admin.Close()
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
