package postgres_test

// P25-T01 — auditoria executável do manifesto do schema PostgreSQL.
//
// O teste extrai o catálogo do schema `app` de um banco descartável criado
// do zero (as 32 migrations via dbtest.New) e compara tabela a tabela com o
// manifesto versionado em testdata/schema-manifest.json: tabelas, colunas
// (tipo, nullability, default), CHECK, FK, UNIQUE, índices, owners, grants,
// RLS/políticas e comentários. Qualquer drift — manual ou de migration — é
// um finding; banco fresco coincide exatamente. Schemas temporários nunca
// entram no resultado (o filtro é `nspname = 'app'`, provado com uma TEMP
// TABLE ao vivo).
//
// Decisão documentada: constraints NOT NULL do PostgreSQL 18 (contype 'n')
// não são travadas nominalmente porque a nullability já é travada por
// coluna; uma coluna que ganha NOT NULL muda `nullable` e o diff acusa.
// O teste registra quantas foram excluídas para auditoria.

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const schemaManifestPath = "testdata/schema-manifest.json"

// Nomes travados para as falsificações ao vivo de TestSchemaManifestDetectsDrift,
// lidos do banco fresco gerado pelas migrations atuais.
const (
	// Índice de performance (não implementa constraint) para o DROP ao vivo.
	driftIndexTable = "app.arenas"
	driftIndexName  = "arenas_public_feed_idx"
	// Tabela sem INSERT para arena_app, para o GRANT excessivo ao vivo.
	driftGrantTable = "app.categories"
	driftGrantRole  = "arena_app"
	driftGrantPriv  = "INSERT"
	// FK simples para o DROP + ADD com definição diferente ao vivo.
	driftFKTable      = "app.arenas"
	driftFKName       = "arenas_creator_id_fkey"
	driftFKColumn     = "creator_id"
	driftFKReferences = "app.accounts(id)"
)

type manifestColumn struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
	Default  string `json:"default"`
	Comment  string `json:"comment"`
}

type manifestConstraint struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Definition string `json:"definition"`
}

type manifestIndex struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
	Valid      bool   `json:"valid"`
}

type manifestGrant struct {
	Grantee    string   `json:"grantee"`
	Privileges []string `json:"privileges"`
}

type manifestTable struct {
	Name        string               `json:"name"`
	Owner       string               `json:"owner"`
	Comment     string               `json:"comment"`
	RLSEnabled  bool                 `json:"rls_enabled"`
	RLSForced   bool                 `json:"rls_forced"`
	Policies    []string             `json:"policies"`
	Columns     []manifestColumn     `json:"columns"`
	Constraints []manifestConstraint `json:"constraints"`
	Indexes     []manifestIndex      `json:"indexes"`
	Grants      []manifestGrant      `json:"grants"`
}

type manifestSequence struct {
	Name  string `json:"name"`
	Owner string `json:"owner"`
}

type manifestColumnGrant struct {
	Table     string `json:"table"`
	Column    string `json:"column"`
	Grantee   string `json:"grantee"`
	Privilege string `json:"privilege"`
}

type schemaManifest struct {
	SchemaVersion int                   `json:"schema_version"`
	Tables        []manifestTable       `json:"tables"`
	Sequences     []manifestSequence    `json:"sequences"`
	ColumnGrants  []manifestColumnGrant `json:"column_grants"`
}

// schemaQuerier é o mínimo para extrair o catálogo de um pool ou de uma
// transação (as falsificações ao vivo leem dentro da tx que muta).
type schemaQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func loadSchemaManifest(t *testing.T) schemaManifest {
	t.Helper()
	raw, err := os.ReadFile(schemaManifestPath)
	if err != nil {
		t.Fatalf("read schema manifest: %v", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var manifest schemaManifest
	if err := decoder.Decode(&manifest); err != nil {
		t.Fatalf("decode schema manifest: %v", err)
	}
	if manifest.SchemaVersion != 1 {
		t.Fatalf("schema manifest version = %d, want 1", manifest.SchemaVersion)
	}
	return manifest
}

func snapshotTables(ctx context.Context, t *testing.T, q schemaQuerier) []manifestTable {
	t.Helper()
	rows, err := q.Query(ctx, `
		SELECT c.relname, c.relowner::regrole::text, COALESCE(obj_description(c.oid), ''),
		       c.relrowsecurity, c.relforcerowsecurity
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'app' AND c.relkind IN ('r', 'p')
		ORDER BY c.relname`)
	if err != nil {
		t.Fatalf("list app tables: %v", err)
	}
	defer rows.Close()
	var tables []manifestTable
	for rows.Next() {
		var table manifestTable
		if err := rows.Scan(&table.Name, &table.Owner, &table.Comment, &table.RLSEnabled, &table.RLSForced); err != nil {
			t.Fatalf("scan app table: %v", err)
		}
		table.Name = "app." + table.Name
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate app tables: %v", err)
	}
	return tables
}

func snapshotColumns(ctx context.Context, t *testing.T, q schemaQuerier) map[string][]manifestColumn {
	t.Helper()
	rows, err := q.Query(ctx, `
		SELECT c.relname, a.attname, format_type(a.atttypid, a.atttypmod),
		       a.attnotnull, COALESCE(pg_get_expr(d.adbin, d.adrelid), ''),
		       COALESCE(col_description(c.oid, a.attnum), '')
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_attrdef d ON d.adrelid = c.oid AND d.adnum = a.attnum
		WHERE n.nspname = 'app' AND c.relkind IN ('r', 'p')
		  AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY c.relname, a.attnum`)
	if err != nil {
		t.Fatalf("list app columns: %v", err)
	}
	defer rows.Close()
	columns := map[string][]manifestColumn{}
	for rows.Next() {
		var table string
		var column manifestColumn
		var notNull bool
		if err := rows.Scan(&table, &column.Name, &column.Type, &notNull, &column.Default, &column.Comment); err != nil {
			t.Fatalf("scan app column: %v", err)
		}
		column.Nullable = !notNull
		columns["app."+table] = append(columns["app."+table], column)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate app columns: %v", err)
	}
	return columns
}

func snapshotConstraints(ctx context.Context, t *testing.T, q schemaQuerier) map[string][]manifestConstraint {
	t.Helper()
	rows, err := q.Query(ctx, `
		SELECT c.relname, con.conname, con.contype, pg_get_constraintdef(con.oid, true)
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'app' AND con.contype IN ('c', 'f', 'p', 'u', 'x')
		ORDER BY c.relname, con.conname`)
	if err != nil {
		t.Fatalf("list app constraints: %v", err)
	}
	defer rows.Close()
	constraints := map[string][]manifestConstraint{}
	for rows.Next() {
		var table, name, kind, definition string
		if err := rows.Scan(&table, &name, &kind, &definition); err != nil {
			t.Fatalf("scan app constraint: %v", err)
		}
		constraints["app."+table] = append(constraints["app."+table], manifestConstraint{Name: name, Kind: kind, Definition: definition})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate app constraints: %v", err)
	}
	return constraints
}

func snapshotIndexes(ctx context.Context, t *testing.T, q schemaQuerier) map[string][]manifestIndex {
	t.Helper()
	rows, err := q.Query(ctx, `
		SELECT c.relname, ic.relname, pg_get_indexdef(ic.oid), i.indisvalid
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indrelid
		JOIN pg_class ic ON ic.oid = i.indexrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'app'
		ORDER BY c.relname, ic.relname`)
	if err != nil {
		t.Fatalf("list app indexes: %v", err)
	}
	defer rows.Close()
	indexes := map[string][]manifestIndex{}
	for rows.Next() {
		var table string
		var index manifestIndex
		if err := rows.Scan(&table, &index.Name, &index.Definition, &index.Valid); err != nil {
			t.Fatalf("scan app index: %v", err)
		}
		indexes["app."+table] = append(indexes["app."+table], index)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate app indexes: %v", err)
	}
	return indexes
}

func snapshotGrants(ctx context.Context, t *testing.T, q schemaQuerier) map[string][]manifestGrant {
	t.Helper()
	rows, err := q.Query(ctx, `
		SELECT table_name, grantee, privilege_type
		FROM information_schema.role_table_grants
		WHERE table_schema = 'app'
		ORDER BY table_name, grantee, privilege_type`)
	if err != nil {
		t.Fatalf("list app grants: %v", err)
	}
	defer rows.Close()
	privileges := map[string]map[string][]string{}
	for rows.Next() {
		var table, grantee, privilege string
		if err := rows.Scan(&table, &grantee, &privilege); err != nil {
			t.Fatalf("scan app grant: %v", err)
		}
		table = "app." + table
		if privileges[table] == nil {
			privileges[table] = map[string][]string{}
		}
		privileges[table][grantee] = append(privileges[table][grantee], privilege)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate app grants: %v", err)
	}
	grants := map[string][]manifestGrant{}
	for table, byGrantee := range privileges {
		for grantee, list := range byGrantee {
			grants[table] = append(grants[table], manifestGrant{Grantee: grantee, Privileges: list})
		}
		sort.Slice(grants[table], func(i, j int) bool { return grants[table][i].Grantee < grants[table][j].Grantee })
	}
	return grants
}

func snapshotPolicies(ctx context.Context, t *testing.T, q schemaQuerier) map[string][]string {
	t.Helper()
	rows, err := q.Query(ctx, `
		SELECT c.relname, p.polname, p.polcmd,
		       COALESCE(pg_get_expr(p.polqual, p.polrelid), ''),
		       COALESCE(pg_get_expr(p.polwithcheck, p.polrelid), ''),
		       COALESCE((SELECT string_agg(r.rolname, ',' ORDER BY r.rolname)
		                 FROM pg_roles r WHERE r.oid = ANY (p.polroles)), '')
		FROM pg_policy p
		JOIN pg_class c ON c.oid = p.polrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'app'
		ORDER BY c.relname, p.polname`)
	if err != nil {
		t.Fatalf("list app policies: %v", err)
	}
	defer rows.Close()
	policies := map[string][]string{}
	for rows.Next() {
		var table, name, cmd, qual, check, roles string
		if err := rows.Scan(&table, &name, &cmd, &qual, &check, &roles); err != nil {
			t.Fatalf("scan app policy: %v", err)
		}
		definition := "POLICY " + name + " " + cmd + " QUAL(" + qual + ") CHECK(" + check + ") TO " + roles
		policies["app."+table] = append(policies["app."+table], definition)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate app policies: %v", err)
	}
	return policies
}

func snapshotSequences(ctx context.Context, t *testing.T, q schemaQuerier) []manifestSequence {
	t.Helper()
	rows, err := q.Query(ctx, `
		SELECT c.relname, c.relowner::regrole::text
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'app' AND c.relkind = 'S'
		ORDER BY c.relname`)
	if err != nil {
		t.Fatalf("list app sequences: %v", err)
	}
	defer rows.Close()
	var sequences []manifestSequence
	for rows.Next() {
		var sequence manifestSequence
		if err := rows.Scan(&sequence.Name, &sequence.Owner); err != nil {
			t.Fatalf("scan app sequence: %v", err)
		}
		sequence.Name = "app." + sequence.Name
		sequences = append(sequences, sequence)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate app sequences: %v", err)
	}
	return sequences
}

func snapshotColumnGrants(ctx context.Context, t *testing.T, q schemaQuerier) []manifestColumnGrant {
	t.Helper()
	rows, err := q.Query(ctx, `
		SELECT table_name, column_name, grantee, privilege_type
		FROM information_schema.role_column_grants
		WHERE table_schema = 'app'
		ORDER BY table_name, column_name, grantee, privilege_type`)
	if err != nil {
		t.Fatalf("list app column grants: %v", err)
	}
	defer rows.Close()
	var grants []manifestColumnGrant
	for rows.Next() {
		var grant manifestColumnGrant
		if err := rows.Scan(&grant.Table, &grant.Column, &grant.Grantee, &grant.Privilege); err != nil {
			t.Fatalf("scan app column grant: %v", err)
		}
		grant.Table = "app." + grant.Table
		grants = append(grants, grant)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate app column grants: %v", err)
	}
	return grants
}

// snapshotSchema extrai o catálogo do schema app. Somente `nspname = 'app'`
// entra: tabelas temporárias vivem em schemas pg_temp_* e nunca aparecem.
func snapshotSchema(ctx context.Context, t *testing.T, q schemaQuerier) schemaManifest {
	t.Helper()
	tables := snapshotTables(ctx, t, q)
	columns := snapshotColumns(ctx, t, q)
	constraints := snapshotConstraints(ctx, t, q)
	indexes := snapshotIndexes(ctx, t, q)
	grants := snapshotGrants(ctx, t, q)
	policies := snapshotPolicies(ctx, t, q)
	for i := range tables {
		name := tables[i].Name
		tables[i].Columns = columns[name]
		tables[i].Constraints = constraints[name]
		tables[i].Indexes = indexes[name]
		tables[i].Grants = grants[name]
		tables[i].Policies = policies[name]
		if tables[i].Columns == nil {
			tables[i].Columns = []manifestColumn{}
		}
		if tables[i].Constraints == nil {
			tables[i].Constraints = []manifestConstraint{}
		}
		if tables[i].Indexes == nil {
			tables[i].Indexes = []manifestIndex{}
		}
		if tables[i].Grants == nil {
			tables[i].Grants = []manifestGrant{}
		}
		if tables[i].Policies == nil {
			tables[i].Policies = []string{}
		}
	}
	sequences := snapshotSequences(ctx, t, q)
	if sequences == nil {
		sequences = []manifestSequence{}
	}
	columnGrants := snapshotColumnGrants(ctx, t, q)
	if columnGrants == nil {
		columnGrants = []manifestColumnGrant{}
	}
	return schemaManifest{SchemaVersion: 1, Tables: tables, Sequences: sequences, ColumnGrants: columnGrants}
}

// countExcludedNotNullConstraints conta as constraints NOT NULL do PG18
// (contype 'n') que o manifesto cobre via columns.nullable em vez de
// travar nominalmente. É medição de auditoria, não gate.
func countExcludedNotNullConstraints(ctx context.Context, t *testing.T, q schemaQuerier) int {
	t.Helper()
	var count int
	if err := q.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'app' AND con.contype = 'n'`).Scan(&count); err != nil {
		t.Fatalf("count not-null constraints: %v", err)
	}
	return count
}

func diffSchemaManifest(want, got schemaManifest) []string {
	var findings []string
	wantTables := map[string]manifestTable{}
	for _, table := range want.Tables {
		wantTables[table.Name] = table
	}
	gotTables := map[string]manifestTable{}
	for _, table := range got.Tables {
		gotTables[table.Name] = table
	}
	for name := range wantTables {
		if _, ok := gotTables[name]; !ok {
			findings = append(findings, "missing-table "+name)
		}
	}
	for name := range gotTables {
		if _, ok := wantTables[name]; !ok {
			findings = append(findings, "unexpected-table "+name)
		}
	}
	for name, wantTable := range wantTables {
		gotTable, ok := gotTables[name]
		if !ok {
			continue
		}
		findings = append(findings, diffOneTable(wantTable, gotTable)...)
	}
	findings = append(findings, diffSequences(want.Sequences, got.Sequences)...)
	findings = append(findings, diffColumnGrants(want.ColumnGrants, got.ColumnGrants)...)
	sort.Strings(findings)
	return findings
}

func diffOneTable(want, got manifestTable) []string {
	var findings []string
	if want.Owner != got.Owner {
		findings = append(findings, "owner-changed "+want.Name+": want "+want.Owner+", got "+got.Owner)
	}
	if want.Comment != got.Comment {
		findings = append(findings, "comment-changed "+want.Name)
	}
	if want.RLSEnabled != got.RLSEnabled {
		findings = append(findings, "rls-changed "+want.Name)
	}
	if want.RLSForced != got.RLSForced {
		findings = append(findings, "rls-force-changed "+want.Name)
	}
	findings = append(findings, diffPolicies(want.Name, want.Policies, got.Policies)...)
	findings = append(findings, diffColumns(want.Name, want.Columns, got.Columns)...)
	findings = append(findings, diffConstraints(want.Name, want.Constraints, got.Constraints)...)
	findings = append(findings, diffIndexes(want.Name, want.Indexes, got.Indexes)...)
	findings = append(findings, diffGrants(want.Name, want.Grants, got.Grants)...)
	return findings
}

func diffPolicies(table string, want, got []string) []string {
	var findings []string
	wantSet := map[string]bool{}
	for _, policy := range want {
		wantSet[policy] = true
	}
	gotSet := map[string]bool{}
	for _, policy := range got {
		gotSet[policy] = true
	}
	for _, policy := range want {
		if !gotSet[policy] {
			findings = append(findings, "missing-policy "+table+" "+policy)
		}
	}
	for _, policy := range got {
		if !wantSet[policy] {
			findings = append(findings, "unexpected-policy "+table+" "+policy)
		}
	}
	return findings
}

func diffColumns(table string, want, got []manifestColumn) []string {
	var findings []string
	wantByName := map[string]manifestColumn{}
	for _, column := range want {
		wantByName[column.Name] = column
	}
	gotByName := map[string]manifestColumn{}
	for _, column := range got {
		gotByName[column.Name] = column
	}
	for name := range wantByName {
		if _, ok := gotByName[name]; !ok {
			findings = append(findings, "missing-column "+table+"."+name)
		}
	}
	for name := range gotByName {
		if _, ok := wantByName[name]; !ok {
			findings = append(findings, "unexpected-column "+table+"."+name)
		}
	}
	for name, wantColumn := range wantByName {
		gotColumn, ok := gotByName[name]
		if !ok {
			continue
		}
		qualified := table + "." + name
		if wantColumn.Type != gotColumn.Type {
			findings = append(findings, "column-type-changed "+qualified+": want "+wantColumn.Type+", got "+gotColumn.Type)
		}
		if wantColumn.Nullable != gotColumn.Nullable {
			findings = append(findings, "column-nullability-changed "+qualified)
		}
		if wantColumn.Default != gotColumn.Default {
			findings = append(findings, "column-default-changed "+qualified+": want "+wantColumn.Default+", got "+gotColumn.Default)
		}
		if wantColumn.Comment != gotColumn.Comment {
			findings = append(findings, "column-comment-changed "+qualified)
		}
	}
	return findings
}

func diffConstraints(table string, want, got []manifestConstraint) []string {
	var findings []string
	wantByName := map[string]manifestConstraint{}
	for _, constraint := range want {
		wantByName[constraint.Name] = constraint
	}
	gotByName := map[string]manifestConstraint{}
	for _, constraint := range got {
		gotByName[constraint.Name] = constraint
	}
	for name := range wantByName {
		if _, ok := gotByName[name]; !ok {
			findings = append(findings, "missing-constraint "+table+"."+name)
		}
	}
	for name := range gotByName {
		if _, ok := wantByName[name]; !ok {
			findings = append(findings, "unexpected-constraint "+table+"."+name)
		}
	}
	for name, wantConstraint := range wantByName {
		gotConstraint, ok := gotByName[name]
		if !ok {
			continue
		}
		if wantConstraint.Kind != gotConstraint.Kind || wantConstraint.Definition != gotConstraint.Definition {
			findings = append(findings, "altered-constraint "+table+"."+name+": want "+wantConstraint.Definition+", got "+gotConstraint.Definition)
		}
	}
	return findings
}

func diffIndexes(table string, want, got []manifestIndex) []string {
	var findings []string
	wantByName := map[string]manifestIndex{}
	for _, index := range want {
		wantByName[index.Name] = index
	}
	gotByName := map[string]manifestIndex{}
	for _, index := range got {
		gotByName[index.Name] = index
	}
	for name := range wantByName {
		if _, ok := gotByName[name]; !ok {
			findings = append(findings, "missing-index "+table+"."+name)
		}
	}
	for name := range gotByName {
		if _, ok := wantByName[name]; !ok {
			findings = append(findings, "unexpected-index "+table+"."+name)
		}
	}
	for name, wantIndex := range wantByName {
		gotIndex, ok := gotByName[name]
		if !ok {
			continue
		}
		if wantIndex.Definition != gotIndex.Definition || wantIndex.Valid != gotIndex.Valid {
			findings = append(findings, "altered-index "+table+"."+name+": want "+wantIndex.Definition+", got "+gotIndex.Definition)
		}
	}
	return findings
}

func diffGrants(table string, want, got []manifestGrant) []string {
	var findings []string
	wantByGrantee := map[string][]string{}
	for _, grant := range want {
		wantByGrantee[grant.Grantee] = grant.Privileges
	}
	gotByGrantee := map[string][]string{}
	for _, grant := range got {
		gotByGrantee[grant.Grantee] = grant.Privileges
	}
	grantees := map[string]bool{}
	for grantee := range wantByGrantee {
		grantees[grantee] = true
	}
	for grantee := range gotByGrantee {
		grantees[grantee] = true
	}
	for grantee := range grantees {
		wantSet := map[string]bool{}
		for _, privilege := range wantByGrantee[grantee] {
			wantSet[privilege] = true
		}
		gotSet := map[string]bool{}
		for _, privilege := range gotByGrantee[grantee] {
			gotSet[privilege] = true
		}
		for privilege := range gotSet {
			if !wantSet[privilege] {
				findings = append(findings, "excess-grant "+grantee+" "+privilege+" on "+table)
			}
		}
		for privilege := range wantSet {
			if !gotSet[privilege] {
				findings = append(findings, "missing-grant "+grantee+" "+privilege+" on "+table)
			}
		}
	}
	return findings
}

func diffSequences(want, got []manifestSequence) []string {
	var findings []string
	wantByName := map[string]manifestSequence{}
	for _, sequence := range want {
		wantByName[sequence.Name] = sequence
	}
	gotByName := map[string]manifestSequence{}
	for _, sequence := range got {
		gotByName[sequence.Name] = sequence
	}
	for name := range wantByName {
		if _, ok := gotByName[name]; !ok {
			findings = append(findings, "missing-sequence "+name)
		}
	}
	for name := range gotByName {
		if _, ok := wantByName[name]; !ok {
			findings = append(findings, "unexpected-sequence "+name)
		}
	}
	for name, wantSequence := range wantByName {
		if gotSequence, ok := gotByName[name]; ok && wantSequence.Owner != gotSequence.Owner {
			findings = append(findings, "sequence-owner-changed "+name)
		}
	}
	return findings
}

func diffColumnGrants(want, got []manifestColumnGrant) []string {
	var findings []string
	wantSet := map[string]bool{}
	for _, grant := range want {
		wantSet[grant.Table+"."+grant.Column+" "+grant.Grantee+" "+grant.Privilege] = true
	}
	gotSet := map[string]bool{}
	for _, grant := range got {
		gotSet[grant.Table+"."+grant.Column+" "+grant.Grantee+" "+grant.Privilege] = true
	}
	for key := range wantSet {
		if !gotSet[key] {
			findings = append(findings, "missing-column-grant "+key)
		}
	}
	for key := range gotSet {
		if !wantSet[key] {
			findings = append(findings, "excess-column-grant "+key)
		}
	}
	return findings
}

func failOnFindings(t *testing.T, context string, findings []string) {
	t.Helper()
	if len(findings) == 0 {
		return
	}
	shown := findings
	if len(shown) > 20 {
		shown = shown[:20]
	}
	t.Fatalf("%s: %d finding(s):\n%s", context, len(findings), strings.Join(shown, "\n"))
}

func TestSchemaManifestMatchesFreshDatabase(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	want := loadSchemaManifest(t)
	got := snapshotSchema(ctx, t, pool)
	failOnFindings(t, "fresh database drifts from testdata/schema-manifest.json", diffSchemaManifest(want, got))

	excluded := countExcludedNotNullConstraints(ctx, t, pool)
	t.Logf("not-null constraints covered by columns.nullable (not pinned nominally): %d", excluded)

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire dedicated connection: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "CREATE TEMP TABLE drift_probe_temp (id integer PRIMARY KEY)"); err != nil {
		t.Fatalf("create temp table: %v", err)
	}
	afterTemp := snapshotSchema(ctx, t, conn)
	failOnFindings(t, "temporary schema leaks into the snapshot", diffSchemaManifest(want, afterTemp))
}

func TestSchemaManifestDetectsDrift(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := db.Pool.Pool()
	baseline := snapshotSchema(ctx, t, pool)

	driftCase := func(t *testing.T, name, ddl string, wantRule string) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("%s: begin: %v", name, err)
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback(ctx)
			}
		}()
		if _, err := tx.Exec(ctx, ddl); err != nil {
			t.Fatalf("%s: apply drift: %v", name, err)
		}
		mutated := snapshotSchema(ctx, t, tx)
		findings := diffSchemaManifest(baseline, mutated)
		matched := false
		for _, finding := range findings {
			if strings.HasPrefix(finding, wantRule+" ") || finding == wantRule {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("%s: drift was not refused, findings:\n%s", name, strings.Join(findings, "\n"))
		}
	}

	t.Run("manual drift", func(t *testing.T) {
		driftCase(t, "manual drift", "ALTER TABLE app.arenas ADD COLUMN drift_probe text", "unexpected-column")
	})
	t.Run("missing index", func(t *testing.T) {
		driftCase(t, "missing index", "DROP INDEX app."+driftIndexName, "missing-index")
	})
	t.Run("excess grant", func(t *testing.T) {
		driftCase(t, "excess grant", "GRANT "+driftGrantPriv+" ON "+driftGrantTable+" TO "+driftGrantRole, "excess-grant")
	})
	t.Run("altered foreign key", func(t *testing.T) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("altered foreign key: begin: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, "ALTER TABLE "+driftFKTable+" DROP CONSTRAINT "+driftFKName); err != nil {
			t.Fatalf("altered foreign key: drop: %v", err)
		}
		add := "ALTER TABLE " + driftFKTable + " ADD CONSTRAINT " + driftFKName +
			" FOREIGN KEY (" + driftFKColumn + ") REFERENCES " + driftFKReferences + " ON DELETE CASCADE"
		if _, err := tx.Exec(ctx, add); err != nil {
			t.Fatalf("altered foreign key: re-add with ON DELETE CASCADE: %v", err)
		}
		mutated := snapshotSchema(ctx, t, tx)
		findings := diffSchemaManifest(baseline, mutated)
		matched := false
		for _, finding := range findings {
			if strings.HasPrefix(finding, "altered-constraint ") {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("altered foreign key: drift was not refused, findings:\n%s", strings.Join(findings, "\n"))
		}
	})
}
