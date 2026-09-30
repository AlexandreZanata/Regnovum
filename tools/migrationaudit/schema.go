// Reading the shape of a database (P20-T03).
//
// A migration is correct when the database it produces is the database the
// history says it produces. Comparing that between two databases — one built
// from scratch, one upgraded from a snapshot — is only meaningful if "the
// database" is read the same way both times, so this file reads the catalog
// into one comparable structure: relations, columns, constraints, indexes,
// triggers, ownership and the privileges the runtime role really holds.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/platform/dbmigrate"
)

type column struct {
	Name    string
	Type    string
	Default string
	NotNull bool
}

type constraint struct {
	Name string
	// Kind is the one-letter catalog code: p (primary key), u (unique),
	// c (check), f (foreign key), x (exclusion).
	Kind       string
	Definition string
	// DeleteAction is the ON DELETE action of a foreign key, empty otherwise.
	DeleteAction string
	// Referenced is the table the foreign key points at, empty otherwise.
	Referenced string
}

type index struct {
	Name       string
	Definition string
	Valid      bool
	Ready      bool
	Unique     bool
	// Columns are the indexed columns in order; an expression is the empty
	// string, so an expression index never looks like it covers a column.
	Columns []string
}

type relation struct {
	Name  string
	Kind  string
	Owner string
	// OwnedBy is the table a sequence belongs to, empty for relations. The
	// runtime needs USAGE on the sequences of the tables it inserts into, and
	// on nothing else: ownership is what makes that a question with an answer.
	OwnedBy     string
	Columns     []column
	Constraints []constraint
	Indexes     []index
	Triggers    []string
	// Grants is what the runtime role effectively holds, which is the
	// question worth asking: an indirect grant through a group role is still
	// a privilege the application has.
	Grants map[string]bool
	// Rows is the exact count of rows, measured, never estimated from the
	// statistics collector.
	Rows int64
}

// snapshot is everything the audit compares between two databases.
type snapshot struct {
	Relations []relation
	// SchemaGrants is what the runtime role holds on the schema itself.
	SchemaGrants map[string]bool
	// Sequences are the sequences of the schema, with the runtime grants.
	Sequences []relation
	// PublicGrants are the privileges granted to PUBLIC, as
	// "relation:privilege" pairs. There should never be any.
	PublicGrants []string
	// Role is the attributes of the runtime role.
	Role roleAttributes
	// Version is the migration history as the version table records it.
	Version []versionRow
	// VersionTable is the relation that carries the history, when it exists.
	VersionTable *relation
}

type roleAttributes struct {
	Super      bool
	CreateDB   bool
	CreateRole bool
	CanLogin   bool
	BypassRLS  bool
	Replicate  bool
}

type versionRow struct {
	Version int64
	Applied bool
}

// tablePrivileges is the closed set of table privileges the audit judges. The
// order is the order the report prints.
var tablePrivileges = []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"}

// readSnapshot reads the catalog of a migrated database.
func readSnapshot(ctx context.Context, db *sql.DB) (*snapshot, error) {
	snap := &snapshot{
		SchemaGrants: map[string]bool{},
	}

	relations, err := readRelations(ctx, db)
	if err != nil {
		return nil, err
	}
	for i := range relations {
		relation := &relations[i]
		if relation.Kind == "S" {
			if err := readSequenceGrants(ctx, db, relation); err != nil {
				return nil, err
			}
			if err := readSequenceOwner(ctx, db, relation); err != nil {
				return nil, err
			}
			continue
		}
		if err := readColumns(ctx, db, relation); err != nil {
			return nil, err
		}
		if err := readConstraints(ctx, db, relation); err != nil {
			return nil, err
		}
		if err := readIndexes(ctx, db, relation); err != nil {
			return nil, err
		}
		if err := readTriggers(ctx, db, relation); err != nil {
			return nil, err
		}
		if err := readGrants(ctx, db, relation); err != nil {
			return nil, err
		}
		if err := readRowCount(ctx, db, relation); err != nil {
			return nil, err
		}
		if relation.Name == "schema_metadata" {
			copied := *relation
			snap.VersionTable = &copied
		}
	}

	for _, relation := range relations {
		if relation.Kind == "S" {
			snap.Sequences = append(snap.Sequences, relation)
		}
	}
	snap.Relations = relations

	for _, privilege := range []string{"USAGE", "CREATE"} {
		held, err := schemaPrivilege(ctx, db, privilege)
		if err != nil {
			return nil, err
		}
		snap.SchemaGrants[privilege] = held
	}

	public, err := readPublicGrants(ctx, db)
	if err != nil {
		return nil, err
	}
	snap.PublicGrants = public

	role, err := readRoleAttributes(ctx, db)
	if err != nil {
		return nil, err
	}
	snap.Role = role

	versions, err := readVersions(ctx, db)
	if err != nil {
		return nil, err
	}
	snap.Version = versions

	return snap, nil
}

func readRelations(ctx context.Context, db *sql.DB) ([]relation, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT c.relname, c.relkind::text, pg_get_userbyid(c.relowner)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'v', 'm', 'S')
		ORDER BY c.relname`, schemaName)
	if err != nil {
		return nil, fmt.Errorf("list relations: %w", err)
	}
	defer rows.Close()

	relations := make([]relation, 0, 64)
	for rows.Next() {
		var current relation
		if err := rows.Scan(&current.Name, &current.Kind, &current.Owner); err != nil {
			return nil, fmt.Errorf("read relation: %w", err)
		}
		current.Grants = map[string]bool{}
		relations = append(relations, current)
	}
	return relations, rows.Err()
}

// listRelations reads only the names and kinds of the schema's relations: the
// seed needs the list before it writes, and reading the whole catalog to get it
// would pay for the columns twice.
func listRelations(ctx context.Context, db *sql.DB) ([]relation, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT c.relname, c.relkind::text
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'v', 'm', 'S')
		ORDER BY c.relname`, schemaName)
	if err != nil {
		return nil, fmt.Errorf("list relations: %w", err)
	}
	defer rows.Close()

	relations := make([]relation, 0, 64)
	for rows.Next() {
		var current relation
		if err := rows.Scan(&current.Name, &current.Kind); err != nil {
			return nil, fmt.Errorf("read relation: %w", err)
		}
		relations = append(relations, current)
	}
	return relations, rows.Err()
}

func readColumns(ctx context.Context, db *sql.DB, relation *relation) error {
	rows, err := db.QueryContext(ctx, `
		SELECT a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull,
		       coalesce(pg_get_expr(d.adbin, d.adrelid), '')
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE n.nspname = $1 AND c.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`, schemaName, relation.Name)
	if err != nil {
		return fmt.Errorf("list columns of %s: %w", relation.Name, err)
	}
	defer rows.Close()

	for rows.Next() {
		var current column
		if err := rows.Scan(&current.Name, &current.Type, &current.NotNull, &current.Default); err != nil {
			return fmt.Errorf("read column of %s: %w", relation.Name, err)
		}
		relation.Columns = append(relation.Columns, current)
	}
	return rows.Err()
}

func readConstraints(ctx context.Context, db *sql.DB, relation *relation) error {
	rows, err := db.QueryContext(ctx, `
		SELECT con.conname, con.contype::text, pg_get_constraintdef(con.oid, true),
		       CASE con.contype WHEN 'f' THEN con.confdeltype::text ELSE '' END,
		       CASE con.contype WHEN 'f' THEN coalesce(nf.nspname || '.' || cf.relname, '') ELSE '' END
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_class cf ON cf.oid = con.confrelid
		LEFT JOIN pg_namespace nf ON nf.oid = cf.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2 AND con.contype IN ('p', 'u', 'c', 'f', 'x')
		ORDER BY con.conname`, schemaName, relation.Name)
	if err != nil {
		return fmt.Errorf("list constraints of %s: %w", relation.Name, err)
	}
	defer rows.Close()

	for rows.Next() {
		var current constraint
		if err := rows.Scan(&current.Name, &current.Kind, &current.Definition, &current.DeleteAction, &current.Referenced); err != nil {
			return fmt.Errorf("read constraint of %s: %w", relation.Name, err)
		}
		relation.Constraints = append(relation.Constraints, current)
	}
	return rows.Err()
}

func readIndexes(ctx context.Context, db *sql.DB, relation *relation) error {
	rows, err := db.QueryContext(ctx, `
		SELECT i.relname, pg_get_indexdef(i.oid), x.indisvalid, x.indisready, x.indisunique,
		       coalesce((
		           SELECT string_agg(coalesce(a.attname, ''), ',' ORDER BY k.ord)
		           FROM unnest(x.indkey::int2[]) WITH ORDINALITY AS k(attnum, ord)
		           LEFT JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum
		       ), '')
		FROM pg_index x
		JOIN pg_class i ON i.oid = x.indexrelid
		JOIN pg_class c ON c.oid = x.indrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2
		ORDER BY i.relname`, schemaName, relation.Name)
	if err != nil {
		return fmt.Errorf("list indexes of %s: %w", relation.Name, err)
	}
	defer rows.Close()

	for rows.Next() {
		var current index
		var columns string
		if err := rows.Scan(&current.Name, &current.Definition, &current.Valid, &current.Ready, &current.Unique, &columns); err != nil {
			return fmt.Errorf("read index of %s: %w", relation.Name, err)
		}
		if columns != "" {
			current.Columns = strings.Split(columns, ",")
		}
		relation.Indexes = append(relation.Indexes, current)
	}
	return rows.Err()
}

func readTriggers(ctx context.Context, db *sql.DB, relation *relation) error {
	rows, err := db.QueryContext(ctx, `
		SELECT t.tgname
		FROM pg_trigger t
		JOIN pg_class c ON c.oid = t.tgrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2 AND NOT t.tgisinternal
		ORDER BY t.tgname`, schemaName, relation.Name)
	if err != nil {
		return fmt.Errorf("list triggers of %s: %w", relation.Name, err)
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("read trigger of %s: %w", relation.Name, err)
		}
		relation.Triggers = append(relation.Triggers, name)
	}
	return rows.Err()
}

func readGrants(ctx context.Context, db *sql.DB, relation *relation) error {
	for _, privilege := range tablePrivileges {
		held, err := tablePrivilege(ctx, db, privilege, schemaName+"."+relation.Name)
		if err != nil {
			return err
		}
		relation.Grants[privilege] = held
	}
	return nil
}

func readSequenceGrants(ctx context.Context, db *sql.DB, relation *relation) error {
	for _, privilege := range []string{"USAGE", "SELECT", "UPDATE"} {
		var held bool
		query := fmt.Sprintf("SELECT has_sequence_privilege($1, $2, %s)", quoteLiteral(privilege))
		if err := db.QueryRowContext(ctx, query, runtimeRole, schemaName+"."+relation.Name).Scan(&held); err != nil {
			return fmt.Errorf("read sequence grants of %s: %w", relation.Name, err)
		}
		relation.Grants[privilege] = held
	}
	return nil
}

// readSequenceOwner finds the table a sequence belongs to: `serial` and
// `GENERATED ... AS IDENTITY` both leave a dependency that names it.
func readSequenceOwner(ctx context.Context, db *sql.DB, relation *relation) error {
	err := db.QueryRowContext(ctx, `
		SELECT coalesce(o.relname, '')
		FROM pg_class s
		JOIN pg_namespace n ON n.oid = s.relnamespace
		LEFT JOIN pg_depend d ON d.objid = s.oid AND d.deptype IN ('a', 'i')
		LEFT JOIN pg_class o ON o.oid = d.refobjid
		WHERE n.nspname = $1 AND s.relname = $2 AND s.relkind = 'S'
		LIMIT 1`, schemaName, relation.Name).Scan(&relation.OwnedBy)
	if err != nil {
		return fmt.Errorf("read the owner of sequence %s: %w", relation.Name, err)
	}
	return nil
}

func readRowCount(ctx context.Context, db *sql.DB, relation *relation) error {
	// An exact count, not pg_stat_user_tables: the whole point of the data
	// survival check is that no row disappeared, and an estimate cannot say.
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM "+quoteIdentifier(schemaName)+"."+quoteIdentifier(relation.Name)).Scan(&relation.Rows); err != nil {
		return fmt.Errorf("count %s: %w", relation.Name, err)
	}
	return nil
}

func tablePrivilege(ctx context.Context, db *sql.DB, privilege, qualified string) (bool, error) {
	var held bool
	if err := db.QueryRowContext(ctx,
		"SELECT has_table_privilege($1, $2, $3)", runtimeRole, qualified, privilege).Scan(&held); err != nil {
		return false, fmt.Errorf("read %s privilege on %s: %w", privilege, qualified, err)
	}
	return held, nil
}

func schemaPrivilege(ctx context.Context, db *sql.DB, privilege string) (bool, error) {
	var held bool
	if err := db.QueryRowContext(ctx,
		"SELECT has_schema_privilege($1, $2, $3)", runtimeRole, schemaName, privilege).Scan(&held); err != nil {
		return false, fmt.Errorf("read %s privilege on schema %s: %w", privilege, schemaName, err)
	}
	return held, nil
}

// readPublicGrants finds the privileges held by PUBLIC. Expanding the ACL is
// the only way to see them: the information_schema views do not report a
// pseudo-role.
func readPublicGrants(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT c.relname, g.privilege_type
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace,
		     aclexplode(coalesce(c.relacl, acldefault('r', c.relowner))) AS g
		WHERE n.nspname = $1 AND g.grantee = 0 AND c.relkind IN ('r', 'p', 'v', 'm', 'S')
		ORDER BY c.relname, g.privilege_type`, schemaName)
	if err != nil {
		return nil, fmt.Errorf("list PUBLIC grants: %w", err)
	}
	defer rows.Close()

	grants := make([]string, 0)
	for rows.Next() {
		var relation, privilege string
		if err := rows.Scan(&relation, &privilege); err != nil {
			return nil, fmt.Errorf("read PUBLIC grant: %w", err)
		}
		grants = append(grants, relation+":"+privilege)
	}
	return grants, rows.Err()
}

func readRoleAttributes(ctx context.Context, db *sql.DB) (roleAttributes, error) {
	var role roleAttributes
	err := db.QueryRowContext(ctx, `
		SELECT rolsuper, rolcreatedb, rolcreaterole, rolcanlogin, rolbypassrls, rolreplication
		FROM pg_roles WHERE rolname = $1`, runtimeRole).
		Scan(&role.Super, &role.CreateDB, &role.CreateRole, &role.CanLogin, &role.BypassRLS, &role.Replicate)
	if err != nil {
		return role, fmt.Errorf("read attributes of %s: %w", runtimeRole, err)
	}
	return role, nil
}

func readVersions(ctx context.Context, db *sql.DB) ([]versionRow, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT version_id, is_applied FROM "+dbmigrate.VersionTable+" ORDER BY version_id")
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dbmigrate.VersionTable, err)
	}
	defer rows.Close()

	versions := make([]versionRow, 0, 64)
	for rows.Next() {
		var row versionRow
		if err := rows.Scan(&row.Version, &row.Applied); err != nil {
			return nil, fmt.Errorf("read version row: %w", err)
		}
		versions = append(versions, row)
	}
	return versions, rows.Err()
}

// fingerprint renders the whole shape as one comparable text. Two databases
// whose fingerprints are equal have the same relations, columns, constraints,
// indexes, triggers, owners, runtime privileges and migration history; the row
// counts are deliberately outside it, because data survival is a separate
// question with its own answer.
func (s *snapshot) fingerprint() string {
	lines := make([]string, 0, 512)
	for _, privilege := range sortedPrivileges(s.SchemaGrants) {
		lines = append(lines, fmt.Sprintf("schema %s %s=%t", schemaName, privilege, s.SchemaGrants[privilege]))
	}
	for _, grant := range s.PublicGrants {
		lines = append(lines, "public "+grant)
	}
	lines = append(lines,
		fmt.Sprintf("role %s super=%t createdb=%t createrole=%t login=%t bypassrls=%t replication=%t",
			runtimeRole, s.Role.Super, s.Role.CreateDB, s.Role.CreateRole, s.Role.CanLogin, s.Role.BypassRLS, s.Role.Replicate))
	for _, relation := range s.Relations {
		lines = append(lines, fmt.Sprintf("relation %s kind=%s owner=%s", relation.Name, relation.Kind, relation.Owner))
		for _, privilege := range sortedPrivileges(relation.Grants) {
			lines = append(lines, fmt.Sprintf("  grant %s %s=%t", relation.Name, privilege, relation.Grants[privilege]))
		}
		for _, current := range relation.Columns {
			lines = append(lines, fmt.Sprintf("  column %s %s notnull=%t default=%s",
				current.Name, current.Type, current.NotNull, current.Default))
		}
		for _, current := range relation.Constraints {
			lines = append(lines, fmt.Sprintf("  constraint %s %s delete=%s -> %s %s",
				current.Name, current.Kind, current.DeleteAction, current.Referenced, current.Definition))
		}
		for _, current := range relation.Indexes {
			lines = append(lines, fmt.Sprintf("  index %s (%s) unique=%t valid=%t ready=%t",
				current.Name, strings.Join(current.Columns, ","), current.Unique, current.Valid, current.Ready))
		}
		for _, trigger := range relation.Triggers {
			lines = append(lines, "  trigger "+trigger)
		}
	}
	for _, row := range s.Version {
		lines = append(lines, fmt.Sprintf("version %d applied=%t", row.Version, row.Applied))
	}
	return strings.Join(lines, "\n")
}

func sortedPrivileges(grants map[string]bool) []string {
	names := make([]string, 0, len(grants))
	for name := range grants {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// columnsOf is the set of "relation.column" of one snapshot, used by the
// expansion check: a column the snapshot had that the upgraded database no
// longer has is a contraction, and the ledger has to have named it.
func (s *snapshot) columnsOf() map[string]bool {
	columns := map[string]bool{}
	for _, relation := range s.Relations {
		if relation.Kind == "S" {
			continue
		}
		columns[relation.Name] = true
		for _, current := range relation.Columns {
			columns[relation.Name+"."+current.Name] = true
		}
	}
	return columns
}

// rowCounts is the exact row count of every table that has rows.
func (s *snapshot) rowCounts() map[string]int64 {
	counts := map[string]int64{}
	for _, relation := range s.Relations {
		if relation.Kind == "S" || relation.Rows == 0 {
			continue
		}
		counts[relation.Name] = relation.Rows
	}
	return counts
}

func quoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
