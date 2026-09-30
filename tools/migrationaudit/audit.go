// The migration lifecycle exercise (P20-T03).
//
// What this file runs, and why each step exists:
//
//   - **virgin**: a database built from scratch by the runner. It is the
//     reference: whatever the history produces, this is it.
//   - **ladder**: the history applied one migration at a time, with a reader
//     holding ACCESS SHARE on every table of the schema while each one runs.
//     That is how the audit answers the lock question — not by reading the SQL,
//     but by watching whether the migration waits for an active reader.
//   - **snapshot and upgrade**: after every version, a physical copy of the
//     database (structure, rows and history) is taken and rolled forward to
//     head with the remaining migrations. Every snapshot has to land on the
//     same database the virgin run produced, and no row is allowed to
//     disappear. This is the "upgrade from the snapshot of every version" and
//     the expand check: a column the snapshot had cannot vanish unless the
//     ledger says a migration removed it on purpose.
//   - **failure**: a migration that fails halfway (it creates a table and then
//     divides by zero) has to leave nothing behind — no table, no version row,
//     no changed shape — and the database it failed in has to remain
//     upgradable, which is the operational rollback: there is no `migrate
//     down` in the runner, so the recovery is rolling the process back and
//     rolling the schema forward.
//
// One property of the runner shapes this whole file: **it closes the database
// handle it is given**. goose's `Provider.Close` closes the handle, and
// `dbmigrate` calls it, while `cmd/arena/migrate.go` documents the opposite.
// The audit therefore opens one handle per runner call and one per query
// batch, and the deviation is recorded in `docs/MIGRATION_AUDIT.md` §7 with
// what it costs. Nothing here depends on a handle surviving a migration.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"testing/fstest"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbmigrate"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

// options is one run of the audit.
type options struct {
	// root is the repository root holding the migrations.
	root string
	// dsn is the administrative connection string of a throwaway cluster.
	dsn string
	// prefix names every database this run creates.
	prefix string
	// log receives progress, one line per step: a gate that only speaks at the
	// end cannot be watched while it runs.
	log io.Writer
}

// ladderStep is what one migration of the ladder cost and what it did.
type ladderStep struct {
	Version   int64
	Name      string
	Bytes     int
	AppliedIn time.Duration
	// Blocked is true when the migration waited for the reader's lock.
	Blocked bool
	// Waiting are the locks the migration waited for, as observed.
	Waiting []lockObservation
	// Seeded are the tables of the schema that received their first row at
	// this version.
	Seeded []string
	// Rows is the total row count after this version.
	Rows int64
	// Relations is the number of relations of the schema after this version.
	Relations int
}

// lockObservation is one lock a migration waited for while a reader was active.
type lockObservation struct {
	Mode   string
	Target string
}

// upgradeStep is one snapshot rolled forward to head.
type upgradeStep struct {
	Version       int64
	Rest          int
	UpgradedIn    time.Duration
	FingerprintOK bool
	// LostRows are tables whose rows disappeared during the upgrade.
	LostRows []string
	// GainedRows are tables whose rows grew, which a backfill does on purpose.
	GainedRows []string
	// Contractions are columns or relations the snapshot had that head does
	// not, unless the ledger names the migration that removed them.
	Contractions []string
	// VersionTableOK is true when the upgraded history lists every version
	// exactly once, applied, in ascending order.
	VersionTableOK bool
}

// failureStep is the simulated failure and the recovery that follows it.
type failureStep struct {
	Version int64
	// Error is what the runner reported.
	Error string
	// AppliedBeforeFailure is how many real migrations the failing run applied
	// before it reached the injected one. The failure is injected in the middle
	// of a real upgrade, not on a database that had nothing to do.
	AppliedBeforeFailure int
	// ProbeRecorded is true when the version table recorded the failed
	// migration, which must never happen.
	ProbeRecorded bool
	// Leftovers are the relations the failed migration created and that are
	// still there.
	Leftovers []string
	// Missing are the relations the reference database has and the failed one
	// lost.
	Missing []string
	// RowsLost is what disappeared with the rolled back migration.
	RowsLost []string
	// RecoverApplied is how many migrations the recovery had left to run, and
	// Recovered is whether the database then *is* the reference one.
	RecoverApplied int
	Recovered      bool
}

// reportData is everything the run measured.
type reportData struct {
	Root        string
	Commit      string
	Started     string
	Versions    int
	Ladder      []ladderStep
	Upgrades    []upgradeStep
	Failure     failureStep
	Virgin      *snapshot
	Findings    []string
	Advisories  []string
	Rules       []ruleResult
	Dataset     dataset
	Destructive []destructive
	Replaced    []destructive
	Declared    map[string]int64
	Ledger      ledgerSummary
	Duration    time.Duration
}

// dataset describes what the audit wrote into the databases, because "the data
// survived" means nothing without saying which data there was.
type dataset struct {
	Seeded     []string
	Unseedable map[string]string
	Rows       int64
	Floor      int
}

// audit is the run's state.
type audit struct {
	options
	cluster *cluster
	sources []source
	log     io.Writer
	// unseedable names the tables the naive seed could not fill, with the
	// error that stopped it. It is reported, never silently dropped.
	unseedable map[string]string
}

// measure performs the whole exercise and returns what it measured. Every
// failure that is an environment problem is an error; every failure that is a
// defect of the history is a finding, so the report says what is wrong instead
// of only that something is.
func measure(ctx context.Context, opts options) (*reportData, error) {
	if opts.log == nil {
		opts.log = io.Discard
	}
	sources, err := readSources(opts.root)
	if err != nil {
		return nil, err
	}
	embedded, err := dbmigrate.Versions()
	if err != nil {
		return nil, err
	}
	if len(embedded) != len(sources) {
		return nil, fmt.Errorf("the repository carries %d migration(s) and the runner embeds %d", len(sources), len(embedded))
	}
	for i, migration := range sources {
		if migration.Version != embedded[i] {
			return nil, fmt.Errorf("%s carries version %d and the runner embeds %d", migration.Path, migration.Version, embedded[i])
		}
	}

	started := time.Now()
	data := &reportData{
		Root:     opts.root,
		Started:  started.UTC().Format(time.RFC3339),
		Versions: len(sources),
		Dataset:  dataset{Floor: minSeedableTables},
	}

	exercise := &audit{options: opts, sources: sources, log: opts.log}
	exercise.cluster, err = openCluster(ctx, opts.dsn, opts.prefix, opts.log)
	if err != nil {
		return nil, err
	}
	defer exercise.cluster.close()

	if err := exercise.walkHistory(ctx, data); err != nil {
		return nil, err
	}
	data.Duration = time.Since(started)
	return data, nil
}

func (a *audit) walkHistory(ctx context.Context, data *reportData) error {
	a.stepf("cluster %s: %d migration(s) to walk", a.options.prefix, len(a.sources))
	scanned := scanDestructive(a.sources)
	data.Destructive = contractions(scanned)
	data.Replaced = replacements(scanned)
	data.Declared = declaredTables(a.sources)
	a.stepf("%d statement(s) that remove something (%d of them recreate what they drop, in place), %d table(s) declared by the history",
		len(scanned), len(data.Replaced), len(data.Declared))

	// 1. The reference: what the history produces on an empty database.
	virginName := a.cluster.name("virgin")
	if err := a.cluster.create(ctx, virginName, ""); err != nil {
		return err
	}
	started := time.Now()
	if _, err := a.runnerUp(ctx, virginName, a.log); err != nil {
		return fmt.Errorf("apply every migration to %s: %w", virginName, err)
	}
	a.stepf("%s: %d migration(s) applied from scratch in %s", virginName, len(a.sources), time.Since(started).Round(time.Millisecond))
	virgin, err := a.read(ctx, virginName)
	if err != nil {
		return err
	}
	data.Virgin = virgin
	reference := virgin.fingerprint()

	// 2. The ladder, with the snapshot and the upgrade of every version.
	ladderName := a.cluster.name("ladder")
	if err := a.cluster.create(ctx, ladderName, ""); err != nil {
		return err
	}
	seeded := map[string]bool{}
	failureVersion := a.sources[len(a.sources)/2].Version

	for index, migration := range a.sources {
		// The failure is injected into a database that is *behind* the version
		// it fails at, so the failure happens in the middle of a real upgrade
		// rather than on a database that had nothing left to do.
		failureName := ""
		if migration.Version == failureVersion {
			failureName = a.cluster.name("failure")
			if err := a.cluster.create(ctx, failureName, ladderName); err != nil {
				return err
			}
		}

		step, before, err := a.walkOne(ctx, ladderName, migration, seeded)
		if err != nil {
			return err
		}
		data.Ladder = append(data.Ladder, step)
		if step.Blocked {
			a.stepf("version %d waits for an active reader on %s", migration.Version, waitingList(step.Waiting))
		}
		if index == 0 {
			// The first migration creates the version table and the schema the
			// reader lock needs; from the second version on there is a snapshot
			// to upgrade.
			continue
		}

		upgradeName := a.cluster.name(fmt.Sprintf("upgrade_%02d", migration.Version))
		if err := a.cluster.create(ctx, upgradeName, ladderName); err != nil {
			return err
		}
		upgrade, err := a.rollForward(ctx, upgradeName, reference, before)
		if err != nil {
			return err
		}
		upgrade.Version = migration.Version
		upgrade.Rest = len(a.sources) - index - 1
		data.Upgrades = append(data.Upgrades, upgrade)

		if failureName != "" {
			failure, err := a.simulateFailure(ctx, failureName, failureVersion, virgin, before)
			if err != nil {
				return err
			}
			data.Failure = *failure
			if err := a.cluster.drop(failureName); err != nil {
				return err
			}
		}
		if err := a.cluster.drop(upgradeName); err != nil {
			return err
		}
	}

	data.Dataset.Seeded = sortedKeys(seeded)
	data.Dataset.Unseedable = a.unseedable
	if len(data.Ladder) != 0 {
		data.Dataset.Rows = data.Ladder[len(data.Ladder)-1].Rows
	}
	return nil
}

// runnerUp applies every pending migration to a database. The handle is opened
// and closed here because the runner closes it; see the file comment.
func (a *audit) runnerUp(ctx context.Context, name string, log io.Writer) (int, error) {
	db, err := a.cluster.open(ctx, name)
	if err != nil {
		return 0, err
	}
	applied, err := dbmigrate.Up(ctx, db, log)
	db.Close()
	return applied, err
}

// runnerUpTo applies the history up to and including one version.
func (a *audit) runnerUpTo(ctx context.Context, name string, version int64, log io.Writer) (int, error) {
	db, err := a.cluster.open(ctx, name)
	if err != nil {
		return 0, err
	}
	applied, err := dbmigrate.UpTo(ctx, db, version, log)
	db.Close()
	return applied, err
}

// read reads the catalog of one of the cluster's databases.
func (a *audit) read(ctx context.Context, name string) (*snapshot, error) {
	db, err := a.cluster.open(ctx, name)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return readSnapshot(ctx, db)
}

// walkOne applies one migration of the ladder while a reader holds ACCESS SHARE
// on every table of the schema, then seeds the tables that appeared and reads
// the result back. It returns the snapshot taken *before* the migration, which
// is what the expansion check compares against.
func (a *audit) walkOne(ctx context.Context, name string, migration source, seeded map[string]bool) (ladderStep, *snapshot, error) {
	step := ladderStep{Version: migration.Version, Name: migration.Name, Bytes: migration.Bytes}

	before := (*snapshot)(nil)
	db, err := a.cluster.open(ctx, name)
	if err != nil {
		return step, nil, err
	}
	if migration.Version != a.sources[0].Version {
		if before, err = readSnapshot(ctx, db); err != nil {
			db.Close()
			return step, nil, err
		}
	}
	db.Close()

	waiting, release, err := a.holdReader(ctx, name)
	if err != nil {
		return step, nil, err
	}
	started := time.Now()
	_, err = a.runnerUpTo(ctx, name, migration.Version, a.log)
	step.AppliedIn = time.Since(started)
	release()
	if err != nil {
		return step, nil, fmt.Errorf("apply %s: %w", migration.Path, err)
	}
	step.Waiting = waiting()
	// The migration waited exactly when a lock of the migrating session was
	// still not granted while the reader held the schema: that is the estimate
	// of the window the deployment needs.
	step.Blocked = len(step.Waiting) > 0

	if err := a.seed(ctx, name, seeded); err != nil {
		return step, nil, err
	}
	after, err := a.read(ctx, name)
	if err != nil {
		return step, nil, err
	}
	step.Seeded = newlySeeded(before, after, seeded)
	markSeeded(before, after, seeded)
	step.Relations = len(after.Relations)
	step.Rows = totalRows(after)
	if err := checkVersionTable(after, migration.Version, "the ladder"); err != nil {
		return step, nil, err
	}
	if before == nil {
		before = after
	}
	return step, before, nil
}

// rollForward upgrades a snapshot to head and compares the result with the
// reference database.
func (a *audit) rollForward(ctx context.Context, name, reference string, before *snapshot) (upgradeStep, error) {
	step := upgradeStep{}
	if _, err := a.runnerUp(ctx, name, a.log); err != nil {
		return step, fmt.Errorf("roll %s forward to head: %w", name, err)
	}
	after, err := a.read(ctx, name)
	if err != nil {
		return step, err
	}
	step.FingerprintOK = after.fingerprint() == reference
	step.VersionTableOK = versionTableIsComplete(after, a.sources)
	step.LostRows, step.GainedRows = compareRows(before.rowCounts(), after.rowCounts())
	step.Contractions = contractionsBetween(before.columnsOf(), after.columnsOf())
	return step, nil
}

// simulateFailure injects a migration that fails halfway into an upgrade that
// is still in progress, and then rolls the same database forward with the real
// history.
//
// The database is a copy of the ladder at the version *before* the one the probe
// fails at, so the failing run has real work to do: goose applies the remaining
// migrations and then reaches the injected one, which creates a table, inserts a
// row and divides by zero. What the audit then asks is not "nothing happened" —
// the migrations before the probe are supposed to have happened — but:
//
//   - the failed migration recorded no version, so it will be attempted again;
//   - it left no table and no row behind, so the attempt changed nothing;
//   - nothing the history removed was lost;
//   - the rows the dataset had written before are still there, and
//   - the database still rolls forward to the reference one, which is the only
//     operational rollback this runner has: `migrate down` does not exist.
func (a *audit) simulateFailure(ctx context.Context, name string, version int64, reference *snapshot, before *snapshot) (*failureStep, error) {
	step := &failureStep{Version: version}

	applied, err := a.applyProbe(ctx, name)
	if err == nil {
		return nil, fmt.Errorf("the injected migration that must fail was applied to %s", name)
	}
	step.Error = err.Error()
	step.AppliedBeforeFailure = applied

	afterFailure, err := a.read(ctx, name)
	if err != nil {
		return nil, err
	}
	step.ProbeRecorded = hasVersion(afterFailure.Version, probeVersion)
	step.Leftovers = relationsMissing(afterFailure, reference)
	step.Missing = relationsMissing(reference, afterFailure)
	step.RowsLost, _ = compareRows(before.rowCounts(), afterFailure.rowCounts())

	// The recovery: no `migrate down` exists, so the database must remain
	// upgradable with the history that is real.
	recovered, err := a.runnerUp(ctx, name, a.log)
	if err != nil {
		return nil, fmt.Errorf("recover %s after the failed migration: %w", name, err)
	}
	step.RecoverApplied = recovered
	final, err := a.read(ctx, name)
	if err != nil {
		return nil, err
	}
	step.Recovered = final.fingerprint() == reference.fingerprint()
	return step, nil
}

// applyProbe runs the injected failing migration through the same library, the
// same store and the same version table the runner uses, together with the real
// migrations that were still pending.
func (a *audit) applyProbe(ctx context.Context, name string) (int, error) {
	files := fstest.MapFS{}
	for _, migration := range a.sources {
		files[migration.Name] = &fstest.MapFile{Data: []byte("-- +goose Up\n" + migration.Up + "\n-- +goose Down\n")}
	}
	files[probeName] = &fstest.MapFile{Data: []byte(probeSQL)}
	store, err := database.NewStore(goose.DialectPostgres, dbmigrate.VersionTable)
	if err != nil {
		return 0, fmt.Errorf("build the version-table store: %w", err)
	}
	db, err := a.cluster.open(ctx, name)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectCustom, db, files, goose.WithStore(store), goose.WithVerbose(false))
	if err != nil {
		return 0, fmt.Errorf("build the provider of the failing migration: %w", err)
	}
	results, applyErr := provider.Up(ctx)
	return len(results), applyErr
}

// probeVersion and probeSQL are the injected migration. Its version is above
// every real one so goose finds it pending and applies it last, and it fails
// after a side effect: a migration that only failed before touching anything
// would not test the rollback.
const (
	probeVersion = 90001
	probeName    = "90001_migration_audit_failure_probe.sql"
	probeSQL     = `-- +goose Up
CREATE TABLE app.migration_audit_probe (id integer PRIMARY KEY);
INSERT INTO app.migration_audit_probe (id) VALUES (1);
SELECT 1 / 0;

-- +goose Down
DROP TABLE IF EXISTS app.migration_audit_probe;
`
)

// holdReader opens a session that holds ACCESS SHARE on every table of the
// schema — the lock a plain reader holds — and returns the observed waits plus
// the release. The wait is what makes the answer deterministic: a migration that
// needs ACCESS EXCLUSIVE cannot finish, `pg_locks` shows what it is waiting for,
// and the audit releases the reader so the migration completes for real.
func (a *audit) holdReader(ctx context.Context, name string) (func() []lockObservation, func(), error) {
	db, err := a.cluster.open(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	names, err := listRelations(ctx, db)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	qualified := make([]string, 0, len(names))
	for _, relation := range names {
		if relation.Kind == "S" {
			continue
		}
		qualified = append(qualified, quoteIdentifier(schemaName)+"."+quoteIdentifier(relation.Name))
	}
	if len(qualified) == 0 {
		// Nothing to read yet: the first migration creates the schema, and a
		// reader cannot hold a lock on a table that does not exist.
		db.Close()
		return func() []lockObservation { return nil }, func() {}, nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, "LOCK TABLE "+strings.Join(qualified, ", ")+" IN ACCESS SHARE MODE"); err != nil {
		tx.Rollback()
		db.Close()
		return nil, nil, fmt.Errorf("hold a reader lock on %s: %w", name, err)
	}

	var (
		mutex    sync.Mutex
		observed []lockObservation
		done     = make(chan struct{})
		once     sync.Once
	)
	// release is the single exit of the reader: it ends the transaction, closes
	// the handle and stops the poller. Both the migration finishing and the
	// wait being observed lead here, and it has to be idempotent — the poller
	// fires from its own goroutine while the gate is still applying the
	// migration. Leaving the handle open would keep a session on the database
	// and the next `CREATE DATABASE ... TEMPLATE` would refuse to copy from it.
	release := func() {
		once.Do(func() {
			close(done)
			_ = tx.Rollback()
			db.Close()
		})
	}

	go func() {
		poll, err := a.cluster.open(context.Background(), name)
		if err != nil {
			return
		}
		defer poll.Close()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				locks, err := waitingLocks(context.Background(), poll)
				if err != nil || len(locks) == 0 {
					continue
				}
				mutex.Lock()
				if len(observed) == 0 {
					observed = locks
				}
				mutex.Unlock()
				// The wait was observed; the migration may proceed. Releasing the
				// reader from here is what the gate asserts happened: the lock was
				// real, the migration was waiting for it, and the wait ended.
				release()
				return
			}
		}
	}()

	return func() []lockObservation {
		mutex.Lock()
		defer mutex.Unlock()
		return append([]lockObservation(nil), observed...)
	}, release, nil
}

// waitingLocks lists the locks of this database that are not granted, which is
// exactly what a migrating session waits for.
func waitingLocks(ctx context.Context, db *sql.DB) ([]lockObservation, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT l.mode, coalesce(n.nspname || '.' || c.relname, l.locktype)
		FROM pg_locks l
		LEFT JOIN pg_class c ON c.oid = l.relation
		LEFT JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE NOT l.granted
		  AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())
		ORDER BY l.mode`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	locks := make([]lockObservation, 0, 4)
	for rows.Next() {
		var lock lockObservation
		if err := rows.Scan(&lock.Mode, &lock.Target); err != nil {
			return nil, err
		}
		locks = append(locks, lock)
	}
	return locks, rows.Err()
}

// seed writes one row into every table that can take one without domain
// knowledge, and reports the tables that cannot. It is deliberately naive —
// `INSERT ... DEFAULT VALUES` — because a seed that knew the domain would be a
// second copy of the domain, and this audit is about the schema.
func (a *audit) seed(ctx context.Context, name string, seeded map[string]bool) error {
	db, err := a.cluster.open(ctx, name)
	if err != nil {
		return err
	}
	defer db.Close()

	relations, err := listRelations(ctx, db)
	if err != nil {
		return err
	}
	for _, relation := range relations {
		if relation.Kind == "S" || seeded[relation.Name] {
			continue
		}
		if relation.Kind != "r" && relation.Kind != "p" {
			// A view cannot be inserted into, and nothing in this history has
			// one: the audit reports it instead of pretending to seed it.
			a.stepf("relation %s is a %s and cannot be seeded", relation.Name, kindName(relation.Kind))
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		statement := "INSERT INTO " + quoteIdentifier(schemaName) + "." + quoteIdentifier(relation.Name) + " DEFAULT VALUES"
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			tx.Rollback()
			if a.unseedable == nil {
				a.unseedable = map[string]string{}
			}
			a.unseedable[relation.Name] = firstLine(err.Error())
			continue
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		seeded[relation.Name] = true
	}
	return nil
}

// newlySeeded and markSeeded keep the dataset honest across versions: a table
// is seeded once, when it first exists, and the report says at which version
// that happened.
func newlySeeded(before, after *snapshot, seeded map[string]bool) []string {
	newly := make([]string, 0)
	for _, relation := range after.Relations {
		if relation.Kind == "S" || seeded[relation.Name] {
			continue
		}
		if before != nil && len(rowsOf(before, relation.Name)) == 0 {
			newly = append(newly, relation.Name)
		}
	}
	sort.Strings(newly)
	return newly
}

func markSeeded(before, after *snapshot, seeded map[string]bool) {
	for _, relation := range after.Relations {
		if relation.Kind == "S" {
			continue
		}
		if relation.Rows > 0 || seeded[relation.Name] {
			seeded[relation.Name] = true
		}
	}
}

func rowsOf(snapshot *snapshot, name string) []string {
	for _, relation := range snapshot.Relations {
		if relation.Name == name {
			return []string{itoa(relation.Rows)}
		}
	}
	return nil
}

func checkVersionTable(snapshot *snapshot, version int64, where string) error {
	seen := map[int64]bool{}
	for _, row := range snapshot.Version {
		if !row.Applied {
			return fmt.Errorf("%s: version %d is recorded as not applied", where, row.Version)
		}
		if seen[row.Version] {
			return fmt.Errorf("%s: version %d appears twice", where, row.Version)
		}
		seen[row.Version] = true
	}
	if !seen[version] {
		return fmt.Errorf("%s: version %d is missing from the history", where, version)
	}
	return nil
}

func versionTableIsComplete(snapshot *snapshot, sources []source) bool {
	seen := map[int64]bool{}
	for _, row := range snapshot.Version {
		if !row.Applied {
			return false
		}
		if row.Version == 0 {
			// goose records the creation of the version table itself as
			// version 0; it is not a migration of the history.
			continue
		}
		if seen[row.Version] {
			return false
		}
		seen[row.Version] = true
	}
	if len(seen) != len(sources) {
		return false
	}
	for _, migration := range sources {
		if !seen[migration.Version] {
			return false
		}
	}
	return true
}

// contractionsBetween lists what the snapshot had and head does not.
func contractionsBetween(before, after map[string]bool) []string {
	missing := make([]string, 0)
	for name := range before {
		if !after[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

func compareRows(before, after map[string]int64) (lost, gained []string) {
	for name, count := range before {
		switch current := after[name]; {
		case current < count:
			lost = append(lost, fmt.Sprintf("%s (%d -> %d)", name, count, current))
		case current > count:
			gained = append(gained, fmt.Sprintf("%s (%d -> %d)", name, count, current))
		}
	}
	sort.Strings(lost)
	sort.Strings(gained)
	return lost, gained
}

// relationsMissing lists the relations of `left` that `right` does not have.
func relationsMissing(left, right *snapshot) []string {
	present := map[string]bool{}
	for _, relation := range right.Relations {
		present[relation.Name] = true
	}
	missing := make([]string, 0)
	for _, relation := range left.Relations {
		if !present[relation.Name] {
			missing = append(missing, relation.Name)
		}
	}
	sort.Strings(missing)
	return missing
}

func hasVersion(rows []versionRow, version int64) bool {
	for _, row := range rows {
		if row.Version == version {
			return true
		}
	}
	return false
}

func totalRows(snapshot *snapshot) int64 {
	total := int64(0)
	for _, relation := range snapshot.Relations {
		if relation.Kind == "S" {
			continue
		}
		total += relation.Rows
	}
	return total
}

func waitingList(locks []lockObservation) string {
	if len(locks) == 0 {
		return "an unobserved lock"
	}
	parts := make([]string, 0, len(locks))
	for _, lock := range locks {
		parts = append(parts, lock.Target+" "+lock.Mode)
	}
	return strings.Join(parts, ", ")
}

func (a *audit) stepf(format string, arguments ...any) {
	fmt.Fprintf(a.log, "migrationaudit: "+format+"\n", arguments...)
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func firstLine(text string) string {
	for index, character := range text {
		if character == '\n' {
			return text[:index]
		}
	}
	return text
}

func kindName(kind string) string {
	switch kind {
	case "r":
		return "table"
	case "p":
		return "partitioned table"
	case "v":
		return "view"
	case "m":
		return "materialized view"
	case "S":
		return "sequence"
	default:
		return kind
	}
}
