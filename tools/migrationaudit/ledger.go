// The ledgers: everything this audit refuses unless a human wrote it down
// (P20-T03).
//
// The phase asks for an audit of grants, locks, indexes, foreign keys, cascades
// and the rollback path. An audit that only printed what it found would be a
// report nobody has to act on, and an audit that refused every exception would
// be a gate nobody could pass. So the exceptions are here, one line each with
// the reason it exists, and the audit fails when reality stops matching them in
// either direction: a new destructive statement, a new cascade, a new blocking
// migration, a table that became writable or stopped being append-only all have
// to be written down — in review, by a person, before the gate goes green again.
//
// Nothing here is generated. Every entry was measured against the database the
// history produces and then justified from the migration that made the choice,
// and the falsification in the tests proves that moving one makes the audit
// refuse the schema it describes.
package main

// contractionLedger names every statement of a forward half that removes
// something *for good*, keyed by "version normalized-statement". The value is
// why the history is allowed to do it.
//
// It is empty, and that is a measurement rather than an omission: the 32
// migrations of this history drop 47 objects and recreate every one of them in
// the same file, because PostgreSQL cannot alter a trigger body or a CHECK
// constraint in place. `applyHistoryRules` still refuses a statement that
// removes something and is not listed here, and the report prints the 47
// replacements with their names, so the first real removal — a dropped column,
// a dropped table — arrives as a red gate and a review.
var contractionLedger = map[string]string{}

// blockingLedger names every version that holds a lock that an active reader
// blocks on — ACCESS EXCLUSIVE, in practice. Those are the migrations that need
// a window: while they wait, the application's readers wait with them. The
// value is why the migration cannot avoid it.
//
// Every entry below was observed, not guessed: the ladder runs each migration
// while another session holds ACCESS SHARE on every table of the schema, and
// the audit records the lock the migration waited for. `ALTER TABLE`, `CREATE
// INDEX` (without CONCURRENTLY) and `ADD CONSTRAINT` all take ACCESS EXCLUSIVE,
// so the migrations that touch an existing table need a window and the ones
// that only create new objects do not.
// The twelve below are the complete measured set, reproduced identically across
// repeated runs: the audit prints the lock and the relation it waited on, and a
// version that is not here has to be justified before the gate goes green
// again. The number is not "every migration that writes": it is every migration
// that needs ACCESS EXCLUSIVE on a table that already exists, which is what an
// active reader blocks.
var blockingLedger = map[int64]string{
	3:  "ALTER TABLE app.schema_metadata changes the owner of the version table: ownership takes ACCESS EXCLUSIVE, and the role model rewrites the grants of the whole schema in the same migration",
	7:  "ALTER TABLE app.profiles adds the timezone column and the CHECK that reads it: the profile of every account is locked for the duration",
	9:  "ALTER TABLE app.wallet_accounts adds the free-cycle anchor: the balance projection of every account is locked",
	10: "ALTER TABLE app.wallet_operations adds the administrative audit columns (actor_account_id, reason) and the CHECK over them: the append-only ledger is locked for the duration",
	16: "CREATE UNIQUE INDEX on app.arguments builds the idempotency key over the rows that already exist; CONCURRENTLY is not available to a migration that runs inside goose's transaction-per-migration",
	17: "ALTER TABLE app.arguments adds withdrawn_at and the CHECK that reads it: every argument is locked",
	18: "ALTER TABLE app.position_changes adds the attribution columns: the chain of every position is locked",
	19: "ALTER TABLE app.persuasion_attributions adds the moderation decision columns and the protect trigger",
	21: "ALTER TABLE app.wallet_operations replaces the CHECK that lists the operation types: a CHECK rewrite validates every existing row of the ledger",
	23: "ALTER TABLE app.admin_roles replaces the capability CHECK to admit the security role",
	24: "ALTER TABLE app.moderation_cases adds the claim lease columns and the partial index over them: the queue of open cases is locked",
	30: "ALTER TABLE app.sessions adds mfa_verified_at: every live session is locked for the duration of the statement",
}

// cascadeLedger names every foreign key that deletes its children with the
// parent, keyed by "child.fk-name". A cascade is a deletion nobody wrote: the
// runtime deletes one row and the database removes the others, so every one of
// them is named here with what it is allowed to take with it.
//
// Eleven cascades, and they have one shape: a row that belongs to an account
// and has no meaning without it. The two that are not about accounts are the
// projections of an arena, which are derived data and can be rebuilt. Every
// other foreign key of the schema is RESTRICT or NO ACTION, which means the
// database refuses the delete — the deliberate default of this schema: history
// (the ledgers, the audit trail, the arguments) is never deleted implicitly.
var cascadeLedger = map[string]string{
	"profiles.profiles_account_id_fkey":                                                 "the profile is the account's public face: it has no identity of its own and the deletion journey (migration 27) removes both together",
	"password_credentials.password_credentials_account_id_fkey":                         "a credential without its account authenticates nobody and is a secret that should not outlive it",
	"password_reset_tokens.password_reset_tokens_account_id_fkey":                       "a reset token for a deleted account is a live grant to a row that no longer exists",
	"email_verification_tokens.email_verification_tokens_account_id_fkey":               "a verification token for a deleted account would verify an address nobody owns",
	"sessions.sessions_account_id_fkey":                                                 "a session of a deleted account must not survive the account: it is the clearest reading of \"the account is gone\"",
	"account_mfa.account_mfa_account_id_fkey":                                           "second factors are part of the credential, and the same reason applies",
	"mfa_backup_codes.mfa_backup_codes_account_id_fkey":                                 "one-use codes are part of the credential, and the same reason applies",
	"username_history.username_history_account_id_fkey":                                 "the history of a name release is personal data of the account that held it",
	"communication_preferences.communication_preferences_account_id_fkey":               "a preference of a deleted account cannot be honoured and is not evidence",
	"communication_preference_history.communication_preference_history_account_id_fkey": "the preference history is the account's own trail, kept only as long as the account",
	"arena_public_stat_projections.arena_public_stat_projections_arena_id_fkey":         "the projection of an arena is derived data: it is rebuilt from the events it summarises (migration 31)",
	"account_key_credentials.account_key_credentials_account_id_fkey":                   "an account key credential without its account authenticates nobody and is a secret that should not outlive it",
}

// appendOnlyTables names the tables the runtime may only read and append to:
// the ledger of INK, the audit trail, the immutable chains and the migration
// history itself. A table in this list holds SELECT and INSERT and nothing
// else; a table outside it holds UPDATE as well. The list is what makes "the
// runtime cannot rewrite financial history" a gate instead of a promise.
var appendOnlyTables = map[string]string{
	"wallet_transactions":     "the ledger of INK: append-only by the plan, and the transaction is the immutable record a balance is derived from",
	"wallet_operations":       "the operations that move INK, with no UPDATE: a correction is a compensating operation (migration 21), never an edit",
	"position_changes":        "the chain of a position: each row is one transition, and the current position is the last row, never a rewritten one",
	"argument_sources":        "the sources an argument cites: a citation is evidence, so it is added and withdrawn (there is a withdrawn_at column) and never rewritten",
	"audit_events":            "the administrative audit trail: an event that can be edited is not evidence",
	"moderation_actions":      "what a moderator did, when and why: the moderation history is read to justify decisions and cannot be revised",
	"moderation_reports":      "what a user reported: the report is testimony and is closed by state, not by editing",
	"retention_runs":          "each run of the retention sweep is a record of a deletion, which is exactly what must not be erasable",
	"arena_pass_consumptions": "a consumed pass is the consumption of something paid for: it is the row that proves the pass was used",
}

// readOnlyTables names the tables the runtime may read and never write. They
// are reference data (seeded, changed only by a migration) and the schema's own
// metadata (written by the owner). A table here holds SELECT alone; the audit
// refuses a table that holds neither INSERT nor UPDATE nor DELETE unless it is
// classified here, so \"the runtime cannot write this\" is a decision that has to
// be written down rather than a privilege nobody got round to granting.
var readOnlyTables = map[string]string{
	"categories":      "the arena categories are seeded by the schema: the product has no feature that creates one, and a category is a product decision",
	"schema_metadata": "the migration history itself, written by the owner through goose; the runtime reads the applied version and never writes it",
}

// deletableTables names the tables the runtime may delete rows from. Everything
// else is refused: an application that can delete is an application whose bugs
// can destroy evidence, and the tables below are the ones whose lifecycle is a
// deletion — a session that ends, a token that is consumed, an account that is
// erased — or whose rows stop being true and are removed after the schema's own
// guard has had its say.
var deletableTables = map[string]string{"arenas": "a draft arena is deleted (db/queries/arenas.sql), and the account erasure deletes the arenas of the account (db/queries/account_deletion.sql); the trigger arenas_published_not_deletable refuses the delete once the arena is published, closed or restricted, so the privilege exists and the schema narrows it",
	"accounts":                         "the deletion journey (migration 27) erases the account row itself",
	"profiles":                         "the profile is deleted with its account",
	"sessions":                         "a session ends by deletion, on logout and on expiry",
	"password_credentials":             "the credential is deleted with the account",
	"password_reset_tokens":            "a consumed or expired token is deleted",
	"email_verification_tokens":        "a consumed or expired token is deleted",
	"mfa_backup_codes":                 "a backup code is deleted when it is used",
	"username_history":                 "the released name is forgotten with the account",
	"communication_preferences":        "the preference row is removed when the account is erased",
	"communication_preference_history": "the trail is removed with the account",
	"arena_relations":                  "the relations between arenas are curated: a relation is removed when it stops being true",
	"arena_public_stat_projections":    "a projection is rebuilt, so its rows are removed and rewritten by the rebuild",
	"transparency_stat_projections":    "the same rebuild, for the public transparency page",
}

// minSeedableTables is the floor of the dataset the audit writes before it
// measures whether an upgrade keeps it: at least this many tables of the schema
// must accept one row without domain knowledge. It is a ratchet, not a target —
// a schema change that makes the audit's data thinner fails the gate.
//
// The measured value is 2: `INSERT ... DEFAULT VALUES` succeeds on
// `app.categories` and `app.schema_metadata`, the two tables whose every column
// has a default or is generated. Every other table needs a domain value (an
// account, an arena, a decision), and inventing one here would mean a second
// copy of the domain in the audit. The floor is what the naive seed reaches, so
// a migration that makes the schema harder to fill — a new NOT NULL column
// without a default on a table the seed could fill — shows up as a red gate.
const minSeedableTables = 2

// ledgerSummary is the ledgers as the report prints them.
type ledgerSummary struct {
	Contractions []ledgerEntry
	Blocking     []ledgerEntry
	Cascades     []ledgerEntry
	AppendOnly   []ledgerEntry
	ReadOnly     []ledgerEntry
	Deletable    []ledgerEntry
	SeedFloor    int
}

type ledgerEntry struct {
	Key    string
	Reason string
}

func summarizeLedger() ledgerSummary {
	summary := ledgerSummary{SeedFloor: minSeedableTables}
	for key, reason := range contractionLedger {
		summary.Contractions = append(summary.Contractions, ledgerEntry{Key: key, Reason: reason})
	}
	for version, reason := range blockingLedger {
		summary.Blocking = append(summary.Blocking, ledgerEntry{Key: itoa(version), Reason: reason})
	}
	for key, reason := range cascadeLedger {
		summary.Cascades = append(summary.Cascades, ledgerEntry{Key: key, Reason: reason})
	}
	for key, reason := range appendOnlyTables {
		summary.AppendOnly = append(summary.AppendOnly, ledgerEntry{Key: key, Reason: reason})
	}
	for key, reason := range readOnlyTables {
		summary.ReadOnly = append(summary.ReadOnly, ledgerEntry{Key: key, Reason: reason})
	}
	for key, reason := range deletableTables {
		summary.Deletable = append(summary.Deletable, ledgerEntry{Key: key, Reason: reason})
	}
	sortEntries(summary.Contractions)
	sortEntries(summary.Blocking)
	sortEntries(summary.Cascades)
	sortEntries(summary.AppendOnly)
	sortEntries(summary.ReadOnly)
	sortEntries(summary.Deletable)
	return summary
}

// sortEntries orders the entries by key. The report is read by people who
// compare two runs, and an order that changes between runs makes that harder.
func sortEntries(entries []ledgerEntry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].Key < entries[j-1].Key; j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
}

func itoa(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	digits := make([]byte, 0, 20)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}
