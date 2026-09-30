package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	auditpg "github.com/AlexandreZanata/Regnovum/internal/audit/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/exportjson"
	profilespg "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// Independent privacy lifecycle model (P24-T09).
//
// The system under test is the real stack over a disposable PostgreSQL:
// communication preferences with their audit trail, the personal export
// pipeline (request, generate, download) with the real JSON encoder, the
// account deletion state machine (request, cancel, execute) with its audit
// evidence, and the retention job with its ledger. The oracle below is a
// separate, deliberately small state machine written from the documented
// rules: exports carry an allowlisted document resolved for one owner only,
// deletion purges private rows while obligations (wallet, passes, public
// arguments, audit, ledger) survive, two accounts never receive each
// other's data, and repeated jobs resolve instead of duplicating. It never
// calls domain constructors or application use cases; identifiers and
// tokens are learned from observed outputs, while the TRANSITION RULES are
// the oracle's own.
//
// Field fates after each transition (enforced by probes, not prose):
//   - request/cancel deletion: nothing moves except the request row and one
//     audit event; every private field stays where it is.
//   - execute deletion: profile, username history, credentials, preferences
//     and their history, sessions, tokens, drafts and draft relations are
//     removed; export documents are nulled while their records survive as
//     expired evidence; the account row keeps its stable identifier with an
//     opaque placeholder email and the deleted status. Wallet balances,
//     pass lots, published arguments, audit events, the retention ledger
//     and referral history never move.
//   - retention enforce: terminal tokens, sessions and expired export
//     documents past their window are purged, prevention references are
//     stripped, held rows are never touched, and the trail and billing
//     rows are only counted. A rerun at the same instant replays the
//     ledger instead of duplicating it.

// modelPrivacyBase is the fixed instant every world starts at. All stamps
// the test controls (seeds, requests, generations, jumps) derive from the
// model clock, never from the database wall clock, so windows stay
// comparable across sequences. The clock only moves forward: revocation
// and cooldown ordering depend on it.
var modelPrivacyBase = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

// modelPrivacyClock is the mutable shared clock of one world.
type modelPrivacyClock struct {
	mu      sync.Mutex
	instant time.Time
}

func newModelPrivacyClock() *modelPrivacyClock {
	return &modelPrivacyClock{instant: modelPrivacyBase}
}

func (c *modelPrivacyClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.instant
}

func (c *modelPrivacyClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.instant = c.instant.Add(duration)
}

// privacyWorld wires the real profiles stack over one disposable database.
type privacyWorld struct {
	ctx         context.Context
	t           *testing.T
	pool        *pgxpool.Pool
	queries     *platformpg.Queries
	repo        *profilespg.Repository
	clock       *modelPrivacyClock
	prefsUpdate *application.UpdateCommunicationPreferencesUseCase
	prefsGet    *application.GetCommunicationPreferencesUseCase
	expRequest  *application.RequestPersonalExportUseCase
	expGenerate *application.GeneratePersonalExportUseCase
	expDownload *application.DownloadPersonalExportUseCase
	delRequest  *application.RequestDeletionUseCase
	delStatus   *application.GetDeletionStatusUseCase
	delCancel   *application.CancelDeletionUseCase
	delExecute  *application.ExecuteDueDeletionsUseCase
	retain      *application.EnforceRetentionUseCase
}

func newPrivacyWorld(t *testing.T, random *testsource.Random) *privacyWorld {
	t.Helper()
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	clock := newModelPrivacyClock()
	repo := profilespg.NewRepository(pool)
	audit := auditpg.NewRepository(pool)
	uow := platformpg.NewTxManager(pool)
	must := func(uc any, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("compose privacy stack: %v", err)
		}
		_ = uc
	}
	prefsUpdate := application.NewUpdateCommunicationPreferencesUseCase(repo, repo, clock)
	prefsGet := application.NewGetCommunicationPreferencesUseCase(repo)
	expRequest, err := application.NewRequestPersonalExportUseCase(repo, random, clock)
	must(expRequest, err)
	expGenerate, err := application.NewGeneratePersonalExportUseCase(repo, repo, exportjson.NewEncoder(), clock)
	must(expGenerate, err)
	expDownload, err := application.NewDownloadPersonalExportUseCase(repo, clock)
	must(expDownload, err)
	delRequest, err := application.NewRequestDeletionUseCase(repo, audit, uow, clock)
	must(delRequest, err)
	delStatus, err := application.NewGetDeletionStatusUseCase(repo)
	must(delStatus, err)
	delCancel, err := application.NewCancelDeletionUseCase(repo, audit, uow, clock)
	must(delCancel, err)
	delExecute, err := application.NewExecuteDueDeletionsUseCase(repo, audit, uow, clock)
	must(delExecute, err)
	retain, err := application.NewEnforceRetentionUseCase(repo, uow, clock)
	must(retain, err)
	return &privacyWorld{
		ctx: ctx, t: t, pool: pool, queries: platformpg.New(pool), repo: repo,
		clock: clock, prefsUpdate: prefsUpdate, prefsGet: prefsGet,
		expRequest: expRequest, expGenerate: expGenerate, expDownload: expDownload,
		delRequest: delRequest, delStatus: delStatus, delCancel: delCancel,
		delExecute: delExecute, retain: retain,
	}
}

// modelExport is one export record the oracle tracks for its owner.
type modelExport struct {
	id          string
	status      string
	token       string
	generatedAt time.Time
	expiresAt   time.Time
	downloads   int32
}

// modelSubject is one account the oracle tracks with its privacy state.
type modelSubject struct {
	email    string
	username string
	uuid     string
	emailNow string
	// anonymized latches on the first execution and never resets: a
	// fresh request starts a new lifecycle but resurrects neither the
	// profile nor the eligibility of the deleted account.
	anonymized   bool
	prefsHasRow  bool
	prefsOptIn   bool
	export       *modelExport
	deletion     string
	deletionID   string
	requestedAt  time.Time
	auditKeys    map[string]bool
	walletFree   int64
	walletBought int64
	argCount     int
}

func (s *modelSubject) audit(action string) int {
	if s.auditKeys == nil {
		s.auditKeys = make(map[string]bool)
	}
	s.auditKeys[action] = true
	return len(s.auditKeys)
}

// privacyLedger is the oracle state shared by every sequence of one run:
// subjects, exports and the audit total are installation-global rows.
// The trail deduplicates by idempotency key (account and action), so the
// oracle counts the key set, never the calls.
type privacyLedger struct {
	subjects   map[string]*modelSubject
	exports    map[string]*modelSubject
	auditKeys  map[string]bool
	auditTotal int
}

func (l *privacyLedger) audit(account, action string) {
	if l.auditKeys == nil {
		l.auditKeys = make(map[string]bool)
	}
	l.auditKeys[account+"|"+action] = true
	l.auditTotal = len(l.auditKeys)
}

func (r *privacyRunner) diverge(op string, want, got bool) {
	r.t.Logf("DEBUGDIVERGE step=%d op=%s want=%v got=%v", r.step, op, want, got)
	r.diverged = append(r.diverged, privacyDivergence{step: r.step, op: op, want: want, got: got})
}

// privacyDivergence is one place where the stack and the oracle disagreed.
type privacyDivergence struct {
	step int
	op   string
	want bool
	got  bool
}

// privacyRunner carries one deterministic script: the world, the shared
// ledger, the sequence-local subjects and every divergence found.
type privacyRunner struct {
	t        *testing.T
	world    *privacyWorld
	ledger   *privacyLedger
	local    []*modelSubject
	step     int
	diverged []privacyDivergence
	trace    []string
	accepts  int
	refusals int
	replays  int
}

func (r *privacyRunner) check(op string, wantAccept bool, err error) bool {
	gotAccept := err == nil
	r.trace = append(r.trace, op)
	r.t.Logf("OP %s want=%v got=%v", op, wantAccept, gotAccept)
	if wantAccept {
		r.accepts++
	} else {
		r.refusals++
	}
	if gotAccept != wantAccept {
		r.diverged = append(r.diverged, privacyDivergence{step: r.step, op: op, want: wantAccept, got: gotAccept})
		return false
	}
	return true
}

// auditCount counts the administrative trail rows: append-only evidence
// that deletions extend and never shrink.
func (r *privacyRunner) auditCount() int {
	return int(countRows(r.t, r.world.ctx, r.world.pool, `SELECT count(*)::integer FROM app.audit_events`))
}

// modelExportAllowlist is the exact top-level vocabulary a downloaded
// document may carry. Anything else is a leak, not a feature.
func modelExportAllowlist() map[string]bool {
	return map[string]bool{
		"schema_version": true, "generated_at": true, "excluded_categories": true,
		"account": true, "positions": true, "position_changes": true,
		"arena_drafts": true, "arguments": true, "wallet": true,
		"passes": true, "billing": true,
	}
}

// opPrefs sets the explicit marketing consent: anonymized accounts are
// refused (eligibility needs an active account), anything else is stored
// exactly as given.
func (r *privacyRunner) opPrefs(subj *modelSubject, optIn bool) {
	r.step++
	r.t.Helper()
	wantAccept := !subj.anonymized
	historyBefore := countRows(r.t, r.world.ctx, r.world.pool,
		`SELECT count(*)::integer FROM app.communication_preference_history WHERE account_id = $1`, subj.uuid)
	res, err := r.world.prefsUpdate.Execute(r.world.ctx, application.SetMarketingOptInCommand{AccountID: subj.uuid, OptIn: optIn})
	if !r.check("prefs", wantAccept, err) {
		return
	}
	if !wantAccept {
		return
	}
	if res == nil || res.MarketingOptIn != optIn || string(res.AccountID) != subj.uuid {
		r.diverge("prefs-shape", true, false)
		return
	}
	// Setting the current value again is an idempotent no-op: the
	// returned projection matches and no audit noise is appended. The
	// read joins the profile with a conservative default, so the very
	// first set of false is already a no-op that writes nothing.
	current := false
	if subj.prefsHasRow {
		current = subj.prefsOptIn
	}
	historyAfter := countRows(r.t, r.world.ctx, r.world.pool,
		`SELECT count(*)::integer FROM app.communication_preference_history WHERE account_id = $1`, subj.uuid)
	if current == optIn {
		if historyAfter != historyBefore {
			r.diverge("prefs-noop-history", true, false)
		}
		return
	}
	if historyAfter != historyBefore+1 {
		r.diverge("prefs-history", true, false)
		return
	}
	subj.prefsHasRow = true
	subj.prefsOptIn = optIn
}

// opGetPrefs reads the consent back: the read joins the profile with a
// conservative default, so it answers until the deletion removes the
// rows, and never opts anyone in.
func (r *privacyRunner) opGetPrefs(subj *modelSubject) {
	r.step++
	r.t.Helper()
	wantAccept := !subj.anonymized
	wantValue := false
	if subj.prefsHasRow {
		wantValue = subj.prefsOptIn
	}
	res, err := r.world.prefsGet.Execute(r.world.ctx, domain.AccountID(subj.uuid))
	if !r.check("get-prefs", wantAccept, err) {
		return
	}
	if !wantAccept {
		return
	}
	if res == nil || res.MarketingOptIn != wantValue || string(res.AccountID) != subj.uuid {
		r.diverge("get-prefs-shape", true, false)
	}
}

// opRequestExport requests the subject's export: always accepted for a
// known account. A fresh row starts a new lifecycle; an active row rotates
// the capability, resets the budget and keeps its status.
func (r *privacyRunner) opRequestExport(subj *modelSubject) {
	r.step++
	r.t.Helper()
	res, err := r.world.expRequest.Execute(r.world.ctx, domain.AccountID(subj.uuid))
	if !r.check("request-export", true, err) {
		return
	}
	if res == nil || res.ExportID == "" || res.DownloadToken == "" {
		r.diverge("request-export-shape", true, false)
		return
	}
	if prev := subj.export; prev != nil && prev.id == res.ExportID {
		if string(res.Status) != prev.status {
			r.diverge("request-export-status", true, false)
			return
		}
		prev.token = res.DownloadToken
		prev.downloads = 0
		return
	}
	if prev := subj.export; prev != nil {
		delete(r.ledger.exports, prev.id)
	}
	subj.export = &modelExport{id: res.ExportID, status: string(res.Status), token: res.DownloadToken}
	r.ledger.exports[res.ExportID] = subj
}

// opGenerateExport builds the document: requested records generate once,
// ready records replay the recorded outcome, anything else refuses.
func (r *privacyRunner) opGenerateExport(exportID string) {
	r.step++
	r.t.Helper()
	subj, known := r.ledger.exports[exportID]
	wantAccept := known && (subj.export.status == "requested" || subj.export.status == "ready")
	res, err := r.world.expGenerate.Execute(r.world.ctx, exportID)
	if !r.check("generate-export", wantAccept, err) {
		return
	}
	if !wantAccept {
		return
	}
	if res == nil || res.ExportID != exportID {
		r.diverge("generate-export-shape", true, false)
		return
	}
	if subj.export.status == "ready" {
		if !res.Replayed {
			r.diverge("generate-export-replay-flag", true, false)
		} else {
			r.replays++
		}
		return
	}
	if res.Replayed || res.Status != domain.ExportStatusReady {
		r.diverge("generate-export-fresh", true, false)
		return
	}
	now := r.world.clock.Now()
	subj.export.status = "ready"
	subj.export.generatedAt = now
	subj.export.expiresAt = now.Add(domain.ExportTTL)
}

// opDownloadExport serves the document to its owner: the token, the owner,
// the window and the budget must all match. Every served document is
// allowlisted and carries the owner's data only.
func (r *privacyRunner) opDownloadExport(owner *modelSubject, exportID, token string) {
	r.step++
	r.t.Helper()
	subj, known := r.ledger.exports[exportID]
	wantAccept := known && subj == owner && token == subj.export.token &&
		subj.export.status == "ready" && !r.world.clock.Now().After(subj.export.expiresAt) &&
		subj.export.downloads < domain.ExportMaxDownloads
	res, err := r.world.expDownload.Execute(r.world.ctx, application.DownloadPersonalExportCommand{
		AccountID: domain.AccountID(owner.uuid), ExportID: exportID, Token: token,
	})
	if !r.check("download-export", wantAccept, err) {
		return
	}
	if !wantAccept {
		return
	}
	if res == nil || len(res.Document) == 0 || res.ExportID != exportID {
		r.diverge("download-export-shape", true, false)
		return
	}
	subj.export.downloads++
	r.checkExportDocument(owner, res.Document)
}

// checkExportDocument enforces the allowlist on one served document: the
// exact top-level vocabulary, the published excluded categories, the
// owner's current identifiers, and no other subject's data anywhere.
func (r *privacyRunner) checkExportDocument(owner *modelSubject, document []byte) {
	r.t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(document, &decoded); err != nil {
		r.diverge("download-export-json", true, false)
		return
	}
	allow := modelExportAllowlist()
	for key := range decoded {
		if !allow[key] {
			r.diverge("download-export-allowlist", true, false)
			return
		}
	}
	excluded, _ := decoded["excluded_categories"].([]any)
	if len(excluded) != len(application.ExportExcludedCategories) {
		r.diverge("download-export-excluded", true, false)
		return
	}
	raw := string(document)
	if !strings.Contains(raw, owner.emailNow) {
		r.diverge("download-export-owner", true, false)
		return
	}
	if !owner.anonymized && !strings.Contains(raw, owner.username) {
		r.diverge("download-export-username", true, false)
		return
	}
	for _, other := range r.ledger.subjects {
		if other == owner {
			continue
		}
		// Values match quoted: sequence siblings share prefixes
		// (priv-beta-5 is a prefix of priv-beta-54), so a bare
		// substring would accuse the owner's own identifiers.
		for _, needle := range []string{other.email, other.emailNow, other.username, other.uuid} {
			if needle != "" && strings.Contains(raw, "\""+needle+"\"") {
				r.diverge("download-export-cross-account", true, false)
				return
			}
		}
	}
	account, _ := decoded["account"].(map[string]any)
	if account == nil {
		r.diverge("download-export-account", true, false)
		return
	}
	if sessions, ok := account["sessions"].([]any); ok {
		for _, entry := range sessions {
			if fields, ok := entry.(map[string]any); ok {
				if _, bad := fields["ip_address"]; bad {
					r.diverge("download-export-device-signal", true, false)
					return
				}
				if _, bad := fields["user_agent"]; bad {
					r.diverge("download-export-device-signal", true, false)
					return
				}
			}
		}
	}
}

// opRequestDeletion requests the subject's deletion: always accepted for a
// known account. A second request inside the lifecycle replays the active
// record; after a terminal record a fresh lifecycle starts.
func (r *privacyRunner) opRequestDeletion(subj *modelSubject) {
	r.step++
	r.t.Helper()
	before := r.auditCount()
	outcome, err := r.world.delRequest.Execute(r.world.ctx, application.RequestDeletionCommand{AccountID: domain.AccountID(subj.uuid)})
	if !r.check("request-deletion", true, err) {
		return
	}
	if outcome == nil {
		r.diverge("request-deletion-shape", true, false)
		return
	}
	if outcome.Replayed {
		if subj.deletion != "requested" || outcome.Request.ID != subj.deletionID {
			r.diverge("request-deletion-replay", true, false)
			return
		}
		r.replays++
		if got := r.auditCount(); got != before {
			r.diverge("request-deletion-replay-audit", true, false)
		}
		return
	}
	if !outcome.Request.RequestedAt.Equal(r.world.clock.Now()) {
		r.diverge("request-deletion-stamp", true, false)
		return
	}
	subj.deletion = "requested"
	subj.deletionID = outcome.Request.ID
	subj.requestedAt = outcome.Request.RequestedAt
	freshAudit := !r.ledger.auditKeys[subj.uuid+"|account.deletion_requested"]
	subj.audit("account.deletion_requested")
	r.ledger.audit(subj.uuid, "account.deletion_requested")
	if want, got := before+boolToInt(freshAudit), r.auditCount(); got != want {
		r.diverge("request-deletion-audit", true, false)
	}
}

// boolToInt renders an expected audit delta: a repeated lifecycle reuses
// the trail's idempotency key and writes nothing new.
func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// opCancelDeletion aborts the request inside its cooldown window: terminal
// records, foreign windows and missing records refuse.
func (r *privacyRunner) opCancelDeletion(subj *modelSubject, reason string) {
	r.step++
	r.t.Helper()
	wantAccept := subj.deletion == "requested" &&
		r.world.clock.Now().Before(subj.requestedAt.Add(domain.DeletionCooldown))
	before := r.auditCount()
	res, err := r.world.delCancel.Execute(r.world.ctx, application.CancelDeletionCommand{
		AccountID: domain.AccountID(subj.uuid), Reason: reason,
	})
	if !r.check("cancel-deletion", wantAccept, err) {
		return
	}
	if !wantAccept {
		if got := r.auditCount(); got != before {
			r.diverge("cancel-deletion-silent-audit", true, false)
		}
		return
	}
	if res == nil || string(res.Status) != "canceled" {
		r.diverge("cancel-deletion-shape", true, false)
		return
	}
	subj.deletion = "canceled"
	freshAudit := !r.ledger.auditKeys[subj.uuid+"|account.deletion_canceled"]
	subj.audit("account.deletion_canceled")
	r.ledger.audit(subj.uuid, "account.deletion_canceled")
	if want, got := before+boolToInt(freshAudit), r.auditCount(); got != want {
		r.diverge("cancel-deletion-audit", true, false)
	}
}

// opDeletionStatus reads the lifecycle back: the stored record always
// matches the oracle page.
func (r *privacyRunner) opDeletionStatus(subj *modelSubject) {
	r.step++
	r.t.Helper()
	wantAccept := subj.deletion != "none"
	res, err := r.world.delStatus.Execute(r.world.ctx, domain.AccountID(subj.uuid))
	if !r.check("deletion-status", wantAccept, err) {
		return
	}
	if !wantAccept {
		return
	}
	if res == nil || string(res.Status) != subj.deletion || res.ID != subj.deletionID {
		r.diverge("deletion-status-shape", true, false)
	}
}

// opExecuteDue runs the deletion job: every requested record past its
// cooldown is anonymized exactly once, and a rerun finds nothing left.
func (r *privacyRunner) opExecuteDue() {
	r.step++
	r.t.Helper()
	var due []*modelSubject
	for _, subj := range r.ledger.subjects {
		if subj.deletion == "requested" && !r.world.clock.Now().Before(subj.requestedAt.Add(domain.DeletionCooldown)) {
			due = append(due, subj)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].uuid < due[j].uuid })
	before := r.auditCount()
	beforeExports := make(map[string][]modelExportRow)
	for _, subj := range due {
		beforeExports[subj.uuid] = r.listExportRows(subj.uuid)
	}
	res, err := r.world.delExecute.Execute(r.world.ctx)
	if !r.check("execute-due", true, err) {
		return
	}
	if res == nil || res.Executed != len(due) {
		r.diverge("execute-due-count", true, false)
		return
	}
	newKeys := 0
	for _, subj := range due {
		subj.deletion = "executed"
		subj.emailNow = "deleted+" + subj.uuid + "@deleted.invalid"
		subj.anonymized = true
		subj.prefsHasRow = false
		if _, seen := r.ledger.auditKeys[subj.uuid+"|account.deletion_executed"]; !seen {
			newKeys++
		}
		subj.audit("account.deletion_executed")
		if subj.export != nil {
			subj.export.status = "expired"
		}
		r.ledger.audit(subj.uuid, "account.deletion_executed")
	}
	// The oracle page moves for every executed subject before any probe
	// reads: probes are read-only, so a probe divergence never desyncs
	// the remaining subjects from what the job really executed.
	for _, subj := range due {
		r.checkDeletionEffects(subj, beforeExports[subj.uuid])
		if len(r.diverged) > 0 {
			return
		}
	}
	if got := r.auditCount(); got != before+newKeys {
		r.diverge("execute-due-audit", true, false)
	}
}

// modelExportRow is one stored export row for the before/after probe.
type modelExportRow struct {
	id      string
	status  string
	docNull bool
}

// listExportRows reads every export row of one account.
func (r *privacyRunner) listExportRows(account string) []modelExportRow {
	rows, err := r.world.pool.Query(r.world.ctx,
		`SELECT id, status, document IS NULL FROM app.data_exports WHERE account_id = $1 ORDER BY id`, account)
	if err != nil {
		r.t.Fatalf("list export rows: %v", err)
	}
	defer rows.Close()
	var out []modelExportRow
	for rows.Next() {
		var row modelExportRow
		var id pgtype.UUID
		if err := rows.Scan(&id, &row.status, &row.docNull); err != nil {
			r.t.Fatalf("scan export row: %v", err)
		}
		row.id = uuidString(id)
		out = append(out, row)
	}
	return out
}

// checkDeletionEffects enforces the field fates of one executed deletion:
// private rows are gone, the account is an opaque placeholder, every
// obligation (wallet, passes, public arguments, trail, ledger) survives
// byte-for-byte, and every export active at execution is expired with its
// bytes purged. Rows already expired before keep whatever the retention
// job has not purged yet: their bytes are retention's duty, not
// deletion's.
func (r *privacyRunner) checkDeletionEffects(subj *modelSubject, before []modelExportRow) {
	r.t.Helper()
	ctx := r.world.ctx
	pool := r.world.pool
	gone := map[string]string{
		"profile":            `SELECT count(*)::integer FROM app.profiles WHERE account_id = $1`,
		"username-history":   `SELECT count(*)::integer FROM app.username_history WHERE account_id = $1`,
		"credentials":        `SELECT count(*)::integer FROM app.password_credentials WHERE account_id = $1`,
		"preferences":        `SELECT count(*)::integer FROM app.communication_preferences WHERE account_id = $1`,
		"preference-history": `SELECT count(*)::integer FROM app.communication_preference_history WHERE account_id = $1`,
		"sessions":           `SELECT count(*)::integer FROM app.sessions WHERE account_id = $1`,
		"verify-tokens":      `SELECT count(*)::integer FROM app.email_verification_tokens WHERE account_id = $1`,
		"reset-tokens":       `SELECT count(*)::integer FROM app.password_reset_tokens WHERE account_id = $1`,
		"drafts":             `SELECT count(*)::integer FROM app.arenas WHERE creator_id = $1 AND status = 'draft'`,
	}
	tables := make([]string, 0, len(gone))
	for table := range gone {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		if got := countRows(r.t, ctx, pool, gone[table], subj.uuid); got != 0 {
			r.diverged = append(r.diverged, privacyDivergence{step: r.step, op: "deletion-purges-" + table, want: true, got: false})
			return
		}
	}
	var email, status string
	var verifiedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT email, status, email_verified_at FROM app.accounts WHERE id = $1`, subj.uuid).Scan(&email, &status, &verifiedAt); err != nil {
		r.t.Fatalf("read anonymized account: %v", err)
	}
	if email != subj.emailNow || status != "deleted" || verifiedAt != nil {
		r.diverge("deletion-placeholder", true, false)
		return
	}
	var free, bought int64
	if err := pool.QueryRow(ctx, `SELECT balance_free, balance_purchased FROM app.wallet_accounts WHERE account_id = $1`, subj.uuid).Scan(&free, &bought); err != nil {
		r.diverge("deletion-wallet-missing", true, false)
		return
	}
	if free != subj.walletFree || bought != subj.walletBought {
		r.diverge("deletion-wallet-changed", true, false)
		return
	}
	kept := map[string]string{
		"pass-lots":   `SELECT count(*)::integer FROM app.arena_pass_lots WHERE account_id = $1`,
		"arguments":   `SELECT count(*)::integer FROM app.arguments WHERE author_id = $1`,
		"ledger-ops":  `SELECT count(*)::integer FROM app.wallet_operations WHERE account_id = $1`,
		"ledger-txs":  `SELECT count(*)::integer FROM app.wallet_transactions tx JOIN app.wallet_operations op ON op.id = tx.operation_id WHERE op.account_id = $1`,
		"audit-trail": `SELECT count(*)::integer FROM app.audit_events WHERE target_type = 'account' AND target_id = $1`,
	}
	for _, table := range []string{"pass-lots", "arguments", "ledger-ops", "ledger-txs", "audit-trail"} {
		want := int32(0)
		switch table {
		case "pass-lots":
			want = 1
		case "arguments":
			want = int32(subj.argCount)
		case "audit-trail":
			want = int32(len(subj.auditKeys))
		}
		if got := countRows(r.t, ctx, pool, kept[table], subj.uuid); got != want {
			r.diverged = append(r.diverged, privacyDivergence{step: r.step, op: "deletion-keeps-" + table, want: true, got: false})
			return
		}
	}
	after := r.listExportRows(subj.uuid)
	byID := make(map[string]modelExportRow, len(after))
	for _, row := range after {
		if row.status == "requested" || row.status == "ready" {
			r.diverge("deletion-export-active", true, false)
			return
		}
		byID[row.id] = row
	}
	for _, row := range before {
		if row.status == "requested" || row.status == "ready" {
			got, ok := byID[row.id]
			if !ok || got.status != "expired" || !got.docNull {
				r.diverge("deletion-export-bytes", true, false)
				return
			}
		}
	}
}

// opEnforceRetention runs the retention job twice at the same instant: the
// second run must replay every ledger entry with the recorded outcome.
// Retained classes never shrink below the oracle baselines. The op opens a
// fresh instant first: rerunning a past instant replays its ledger by
// design, and the oracle asserts fresh work here.
func (r *privacyRunner) opEnforceRetention() {
	r.step++
	r.t.Helper()
	r.world.clock.Advance(time.Hour)
	first, err := r.world.retain.Execute(r.world.ctx)
	if !r.check("retention", true, err) || first == nil {
		return
	}
	second, err := r.world.retain.Execute(r.world.ctx)
	if !r.check("retention-replay", true, err) || second == nil {
		return
	}
	if len(first.Runs) != len(domain.RetentionSchedules()) || len(second.Runs) != len(domain.RetentionSchedules()) {
		r.diverge("retention-classes", true, false)
		return
	}
	for i, run := range second.Runs {
		first := first.Runs[i]
		if !run.Replayed {
			r.diverge("retention-replay-flag", true, false)
			return
		}
		// The replay resolves the recorded outcome: same identity and
		// same counts the first run recorded, never a second outcome.
		if run.ID != first.ID || run.Class != first.Class || run.Counts != first.Counts {
			r.diverge("retention-replay-identity", true, false)
			return
		}
	}
	for _, run := range first.Runs {
		switch run.Class {
		case domain.RetentionClassReferentialLogs:
			if run.Counts.Retained != int32(r.ledger.auditTotal) {
				r.diverge("retention-audit-baseline", true, false)
				return
			}
		case domain.RetentionClassBilling:
			if run.Counts.Retained != 0 {
				r.diverge("retention-billing-baseline", true, false)
				return
			}
		}
	}
	r.replays += len(second.Runs)
}

// setupSequence creates two subjects with eligible accounts, profiles,
// one published arena and argument each, one draft each, wallet balances,
// one due and one fresh token each, and sessions across the retention
// windows.
func (r *privacyRunner) setupSequence(seq int) {
	r.t.Helper()
	now := r.world.clock.Now()
	for _, role := range []string{"alpha", "beta"} {
		email := fmt.Sprintf("priv-%s-%d@arena.example.com", role, seq)
		username := fmt.Sprintf("priv-%s-%d", role, seq)
		acc := createEligibleAccount(r.t, r.world.ctx, r.world.queries, email)
		uuid := uuidString(acc.ID)
		if err := seedPersonalExportProfile(r.t, r.world.ctx, r.world.pool, acc.ID, username, "pt-BR", ""); err != nil {
			r.t.Fatalf("seed profile: %v", err)
		}
		arena := seedPersonalExportArena(r.t, r.world.ctx, r.world.pool, acc.ID,
			fmt.Sprintf("priv-arena-%s-%d", role, seq), fmt.Sprintf("Arena privada %s tese %d", role, seq))
		_ = arena
		seedPersonalExportArgument(r.t, r.world.ctx, r.world.pool, arena, acc.ID, "support",
			fmt.Sprintf("Argumento privado %s tese %d", role, seq), "published")
		var draftID pgtype.UUID
		if err := r.world.pool.QueryRow(r.world.ctx, `
			INSERT INTO app.arenas (creator_id, statement, category, language, status, version)
			VALUES ($1, $2, 'technology', 'pt-BR', 'draft', 1)
			RETURNING id`, acc.ID, fmt.Sprintf("Rascunho privado %s tese %d", role, seq)).Scan(&draftID); err != nil {
			r.t.Fatalf("seed draft: %v", err)
		}
		_ = draftID
		if _, err := r.world.pool.Exec(r.world.ctx, `
			INSERT INTO app.wallet_accounts (account_id, balance_free, balance_purchased)
			VALUES ($1, 5000, 2000)`, acc.ID); err != nil {
			r.t.Fatalf("seed wallet: %v", err)
		}
		if _, err := r.world.pool.Exec(r.world.ctx, `
			INSERT INTO app.arena_pass_lots (account_id, origin, quantity, remaining_quantity, reference)
			VALUES ($1, 'PURCHASE', 1, 1, $2)`, acc.ID, fmt.Sprintf("priv-pass-%s-%d", role, seq)); err != nil {
			r.t.Fatalf("seed pass lot: %v", err)
		}
		due := now.Add(-31 * 24 * time.Hour)
		fresh := now.Add(time.Hour)
		for _, seed := range []struct {
			hash string
			when time.Time
		}{
			{fmt.Sprintf("priv-due-token-%s-%d-32bytes!!", role, seq), due},
			{fmt.Sprintf("priv-fresh-token-%s-%d-32byte!", role, seq), fresh},
		} {
			if _, err := r.world.pool.Exec(r.world.ctx, `
				INSERT INTO app.email_verification_tokens (account_id, token_hash, created_at, expires_at, used_at)
				VALUES ($1, $2, $3, $4, $5)`,
				acc.ID, []byte(seed.hash), seed.when, seed.when, seed.when); err != nil {
				r.t.Fatalf("seed token: %v", err)
			}
		}
		seedPrivacySession(r.t, r.world.ctx, r.world.pool, acc.ID, privacySessionSeed{
			hash:     fmt.Sprintf("priv-purge-session-%s-%d-32bytes!", role, seq),
			terminal: due, expires: due,
		})
		seedPrivacySession(r.t, r.world.ctx, r.world.pool, acc.ID, privacySessionSeed{
			hash:     fmt.Sprintf("priv-abuse-session-%s-%d-32bytes!!", role, seq),
			terminal: now.Add(-8 * 24 * time.Hour), expires: now.Add(24 * time.Hour),
			ip: "192.0.2.44", agent: "privacy-model-agent",
		})
		subj := &modelSubject{
			email: email, username: username, uuid: uuid, emailNow: email,
			deletion: "none", walletFree: 5000, walletBought: 2000, argCount: 1,
		}
		r.ledger.subjects[uuid] = subj
		r.local = append(r.local, subj)
	}
	r.check("setup", true, nil)
}

// privacySessionSeed groups one session seed: the terminal instant that
// arms the retention windows and the optional prevention references.
type privacySessionSeed struct {
	hash     string
	terminal time.Time
	expires  time.Time
	ip       string
	agent    string
}

// seedPrivacySession stores one session with explicit terminal instants and
// optional prevention references.
func seedPrivacySession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, account pgtype.UUID, seed privacySessionSeed) {
	t.Helper()
	var revoked any
	if !seed.terminal.IsZero() {
		revoked = seed.terminal
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.sessions (account_id, token_hash, created_at, expires_at, revoked_at, ip_address, user_agent)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		account, []byte(seed.hash), seed.terminal, seed.expires, revoked, nullableText(seed.ip), nullableText(seed.agent)); err != nil {
		t.Fatalf("seed privacy session: %v", err)
	}
}

// runOneSequence executes one deterministic privacy script: consent flips,
// export journeys with forgery probes, deletion lifecycles with cooldown
// jumps, retention reruns and refusal probes.
func (r *privacyRunner) runOneSequence(rnd *testsource.Random) {
	reporter := r.local[0]
	other := r.local[1]
	steps := 10 + rnd.Int64n(9)
	for i := 0; i < int(steps); i++ {
		subj := reporter
		if rnd.Int64n(2) == 0 {
			subj = other
		}
		peer := other
		if subj == other {
			peer = reporter
		}
		switch rnd.Int64n(16) {
		case 0:
			r.opPrefs(subj, rnd.Int64n(2) == 0)
		case 1:
			r.opGetPrefs(subj)
		case 2:
			r.opRequestExport(subj)
			if rnd.Int64n(3) == 0 {
				r.opRequestExport(subj)
			}
		case 3:
			if subj.export != nil {
				r.opGenerateExport(subj.export.id)
				if rnd.Int64n(2) == 0 {
					r.opGenerateExport(subj.export.id)
				}
			} else {
				r.opGenerateExport("018f6b2a-ffff-7000-8000-ffffffffffff")
			}
		case 4:
			if subj.export != nil {
				switch rnd.Int64n(3) {
				case 0:
					r.opDownloadExport(subj, subj.export.id, subj.export.token)
				case 1:
					r.opDownloadExport(subj, subj.export.id, "forged-token")
				default:
					r.opDownloadExport(peer, subj.export.id, subj.export.token)
				}
			} else {
				r.opDownloadExport(subj, "018f6b2a-ffff-7000-8000-ffffffffffff", "forged-token")
			}
		case 5:
			r.opRequestDeletion(subj)
			if rnd.Int64n(3) == 0 {
				r.opRequestDeletion(subj)
			}
		case 6:
			r.opCancelDeletion(subj, "holder changed their mind")
		case 7:
			r.opDeletionStatus(subj)
		case 8:
			r.opExecuteDue()
		case 9:
			r.opEnforceRetention()
		case 10:
			r.world.clock.Advance(8 * 24 * time.Hour)
			r.step++
		case 11:
			r.world.clock.Advance(26 * time.Hour)
			r.step++
		case 12:
			r.opPrefs(subj, !subj.prefsOptIn)
		case 13:
			r.opGenerateExport("018f6b2a-ffff-7000-8000-ffffffffffff")
		default:
			r.opPrefs(peer, rnd.Int64n(2) == 0)
		}
		_ = i
		if len(r.diverged) > 0 {
			return
		}
	}
	r.reconcile()
}

// reconcile re-reads every local lifecycle at the end of one sequence: the
// stored deletion, consent and export records always match the oracle page.
func (r *privacyRunner) reconcile() {
	r.step++
	for _, subj := range r.local {
		res, err := r.world.delStatus.Execute(r.world.ctx, domain.AccountID(subj.uuid))
		if subj.deletion == "none" {
			if err == nil {
				r.diverge("reconcile-deletion-absent", true, false)
				return
			}
			continue
		}
		if err != nil || string(res.Status) != subj.deletion || res.ID != subj.deletionID {
			r.diverge("reconcile-deletion", true, false)
			return
		}
		prefs, err := r.world.prefsGet.Execute(r.world.ctx, domain.AccountID(subj.uuid))
		wantPrefs := !subj.anonymized
		if (err == nil) != wantPrefs {
			r.diverge("reconcile-prefs", true, false)
			return
		}
		wantValue := false
		if subj.prefsHasRow {
			wantValue = subj.prefsOptIn
		}
		if wantPrefs && prefs.MarketingOptIn != wantValue {
			r.diverge("reconcile-prefs-value", true, false)
			return
		}
	}
	if got := r.auditCount(); got != r.ledger.auditTotal {
		r.diverge("reconcile-audit", true, false)
	}
}

// TestPrivacyLifecycleModel runs deterministic privacy scripts: consent,
// export journeys with forgery probes, deletion lifecycles with cooldown
// jumps and retention reruns, comparing every outcome with the independent
// oracle and the append-only trails.
func TestPrivacyLifecycleModel(t *testing.T) {
	seed := testsource.SeedFor(t)
	rnd := testsource.NewRandom(seed)
	world := newPrivacyWorld(t, rnd)
	const sequences = 60
	divergentRuns := 0
	var first []privacyDivergence
	var firstTrace []string
	accepts, refusals, replays := 0, 0, 0
	ledger := &privacyLedger{subjects: make(map[string]*modelSubject), exports: make(map[string]*modelSubject)}
	for seq := 0; seq < sequences; seq++ {
		_ = seq
		runner := &privacyRunner{t: t, world: world, ledger: ledger}
		runner.setupSequence(seq)
		runner.runOneSequence(rnd)
		accepts += runner.accepts
		refusals += runner.refusals
		replays += runner.replays
		if len(runner.diverged) > 0 {
			divergentRuns++
			if first == nil {
				first = append([]privacyDivergence{}, runner.diverged...)
				if len(first) > 5 {
					first = first[:5]
				}
				firstTrace = append([]string{}, runner.trace...)
				if len(firstTrace) > 10 {
					firstTrace = firstTrace[len(firstTrace)-10:]
				}
			}
		}
	}
	if accepts == 0 || refusals == 0 || replays == 0 {
		t.Fatalf("model never exercised a full path: accepts=%d refusals=%d replays=%d", accepts, refusals, replays)
	}
	if divergentRuns > 0 {
		t.Fatalf("%d of %d sequences diverged, first: %+v trace: %v", divergentRuns, sequences, first, firstTrace)
	}
	t.Logf("privacy lifecycle model: %d sequences, accepts=%d refusals=%d replays=%d, seed %d", sequences, accepts, refusals, replays, seed)
}

// TestPrivacyDeletionCooldownAndEffects proves the fixed deletion journey:
// request, replay, cancel inside the window, no early execution, execution
// past the cooldown with the exact field fates, rerun idempotence and the
// refusal of terminal cancels.
func TestPrivacyDeletionCooldownAndEffects(t *testing.T) {
	world := newPrivacyWorld(t, testsource.NewRandom(9001))
	ledger := &privacyLedger{subjects: make(map[string]*modelSubject), exports: make(map[string]*modelSubject)}
	runner := &privacyRunner{t: t, world: world, ledger: ledger}
	runner.setupSequence(910)
	if len(runner.diverged) > 0 {
		t.Fatalf("deletion setup diverged: %+v", runner.diverged)
	}
	alpha, beta := runner.local[0], runner.local[1]
	runner.opRequestDeletion(alpha)
	runner.opRequestDeletion(alpha)
	if len(runner.diverged) > 0 {
		t.Fatalf("deletion request diverged: %+v", runner.diverged)
	}
	if alpha.deletion != "requested" {
		t.Fatalf("alpha deletion = %s, want requested", alpha.deletion)
	}
	runner.opCancelDeletion(alpha, "holder changed their mind")
	runner.opExecuteDue()
	if len(runner.diverged) > 0 {
		t.Fatalf("cancel path diverged: %+v", runner.diverged)
	}
	if alpha.deletion != "canceled" {
		t.Fatalf("alpha deletion = %s, want canceled", alpha.deletion)
	}
	runner.opRequestDeletion(beta)
	world.clock.Advance(8 * 24 * time.Hour)
	runner.opExecuteDue()
	if len(runner.diverged) > 0 {
		t.Fatalf("execution path diverged: %+v", runner.diverged)
	}
	if beta.deletion != "executed" || alpha.deletion != "canceled" {
		t.Fatalf("alpha = %s beta = %s, want canceled/executed", alpha.deletion, beta.deletion)
	}
	// A rerun finds nothing left, and terminal records refuse cancels.
	runner.opExecuteDue()
	runner.opCancelDeletion(beta, "too late")
	runner.opCancelDeletion(alpha, "already canceled")
	if len(runner.diverged) > 0 {
		t.Fatalf("terminal path diverged: %+v", runner.diverged)
	}
	// Consent and downloads stop at the placeholder: the account is gone
	// as a subject but intact as an identifier.
	runner.opPrefs(beta, true)
	runner.opGetPrefs(beta)
	if len(runner.diverged) > 0 {
		t.Fatalf("post-execution consent diverged: %+v", runner.diverged)
	}
}

// TestPrivacyExportExpiryAndBudget proves the fixed export journey: the
// single-download budget, the 24-hour window and the owner binding.
func TestPrivacyExportExpiryAndBudget(t *testing.T) {
	world := newPrivacyWorld(t, testsource.NewRandom(9002))
	ledger := &privacyLedger{subjects: make(map[string]*modelSubject), exports: make(map[string]*modelSubject)}
	runner := &privacyRunner{t: t, world: world, ledger: ledger}
	runner.setupSequence(920)
	if len(runner.diverged) > 0 {
		t.Fatalf("export setup diverged: %+v", runner.diverged)
	}
	alpha, beta := runner.local[0], runner.local[1]
	runner.opRequestExport(alpha)
	runner.opGenerateExport(alpha.export.id)
	runner.opDownloadExport(alpha, alpha.export.id, alpha.export.token)
	// The budget holds exactly one download: a second serve refuses, and
	// so does a foreign owner or a forged token.
	runner.opDownloadExport(alpha, alpha.export.id, alpha.export.token)
	runner.opDownloadExport(beta, alpha.export.id, alpha.export.token)
	runner.opDownloadExport(alpha, alpha.export.id, "forged-token")
	if len(runner.diverged) > 0 {
		t.Fatalf("budget path diverged: %+v", runner.diverged)
	}
	// A fresh capability restores exactly one download; past the window
	// the link is dead even with the right token.
	runner.opRequestExport(alpha)
	runner.opDownloadExport(alpha, alpha.export.id, alpha.export.token)
	world.clock.Advance(25 * time.Hour)
	runner.opDownloadExport(alpha, alpha.export.id, alpha.export.token)
	if len(runner.diverged) > 0 {
		t.Fatalf("expiry path diverged: %+v", runner.diverged)
	}
}

// TestPrivacyRetentionHoldsAndLedger proves the exact retention counts:
// terminal rows past their window purge, prevention references strip,
// held rows survive, the trail and billing rows are only counted, and a
// rerun replays the ledger.
func TestPrivacyRetentionHoldsAndLedger(t *testing.T) {
	world := newPrivacyWorld(t, testsource.NewRandom(9003))
	ctx := world.ctx
	now := world.clock.Now()
	mkAccount := func(email string) pgtype.UUID {
		acc := createEligibleAccount(t, ctx, world.queries, email)
		return acc.ID
	}
	alpha := mkAccount("priv-hold-alpha@arena.example.com")
	beta := mkAccount("priv-hold-beta@arena.example.com")
	seedToken := func(account pgtype.UUID, hash string, when time.Time) {
		if _, err := world.pool.Exec(ctx, `
			INSERT INTO app.email_verification_tokens (account_id, token_hash, created_at, expires_at, used_at)
			VALUES ($1, $2, $3, $4, $5)`, account, []byte(hash), when, when, when); err != nil {
			t.Fatalf("seed hold token: %v", err)
		}
	}
	due := now.Add(-31 * 24 * time.Hour)
	seedToken(alpha, "hold-alpha-due-token-32bytes!!!!", due)
	seedToken(beta, "hold-beta-due-token-32bytes!!!!!", due)
	seedPrivacySession(t, ctx, world.pool, alpha, privacySessionSeed{
		hash: "hold-alpha-purge-session-32bytes!", terminal: due, expires: due,
	})
	seedPrivacySession(t, ctx, world.pool, beta, privacySessionSeed{
		hash: "hold-beta-purge-session-32bytes!!", terminal: due, expires: due,
	})
	seedPrivacySession(t, ctx, world.pool, alpha, privacySessionSeed{
		hash: "hold-alpha-abuse-session-32bytes!!", terminal: now.Add(-8 * 24 * time.Hour),
		expires: now.Add(24 * time.Hour), ip: "192.0.2.45", agent: "privacy-hold-agent",
	})
	seedPrivacySession(t, ctx, world.pool, beta, privacySessionSeed{
		hash: "hold-beta-abuse-session-32bytes!!!", terminal: now.Add(-8 * 24 * time.Hour),
		expires: now.Add(24 * time.Hour), ip: "192.0.2.46", agent: "privacy-hold-agent",
	})
	placeHold := func(class, account string, reason string) {
		var accountValue any
		if account != "" {
			accountValue = account
		}
		if _, err := world.pool.Exec(ctx, `
			INSERT INTO app.retention_holds (data_class, account_id, reason_code, placed_by)
			VALUES ($1, $2, $3, $4)`, class, accountValue, reason, uuidString(alpha)); err != nil {
			t.Fatalf("place hold %s: %v", class, err)
		}
	}
	betaID := uuidString(beta)
	placeHold("tokens", "", "litigation-whole-class")
	placeHold("sessions", betaID, "dispute-beta")
	placeHold("abuse_signals", betaID, "dispute-beta")
	summary, err := world.retain.Execute(ctx)
	if err != nil {
		t.Fatalf("enforce with holds: %v", err)
	}
	counts := map[string]application.RetentionCounts{}
	for _, run := range summary.Runs {
		counts[string(run.Class)] = run.Counts
	}
	if counts["tokens"] != (application.RetentionCounts{Held: 2}) {
		t.Fatalf("tokens with class hold = %+v, want 2 held", counts["tokens"])
	}
	if counts["sessions"] != (application.RetentionCounts{Purged: 1, Held: 1}) {
		t.Fatalf("sessions with account hold = %+v, want 1 purged 1 held", counts["sessions"])
	}
	if counts["abuse_signals"] != (application.RetentionCounts{Anonymized: 1, Held: 1}) {
		t.Fatalf("abuse signals with account hold = %+v, want 1 anonymized 1 held", counts["abuse_signals"])
	}
	if counts["referential_logs"].Purged != 0 || counts["referential_logs"].Anonymized != 0 {
		t.Fatalf("trail class purged or anonymized: %+v", counts["referential_logs"])
	}
	if counts["billing"].Purged != 0 || counts["billing"].Anonymized != 0 {
		t.Fatalf("billing class purged or anonymized: %+v", counts["billing"])
	}
	// Releasing the class hold lets the second run purge what the first
	// preserved; a third run at the same instant replays everything.
	// Holds live on the database wall clock (placed_at defaults to
	// now()): the release must too, or the release-order check refuses
	// a release dated before the placement.
	if _, err := world.pool.Exec(ctx, `
		UPDATE app.retention_holds SET released_at = now(), release_reason_code = 'dispute-settled'
		WHERE data_class = 'tokens' AND released_at IS NULL`); err != nil {
		t.Fatalf("release hold: %v", err)
	}
	world.clock.Advance(time.Hour)
	second, err := world.retain.Execute(ctx)
	if err != nil {
		t.Fatalf("enforce after release: %v", err)
	}
	for _, run := range second.Runs {
		if run.Replayed {
			t.Fatalf("class %s replayed at a new instant", run.Class)
		}
		if run.Class == domain.RetentionClassTokens && run.Counts.Purged != 2 {
			t.Fatalf("tokens after release = %+v, want 2 purged", run.Counts)
		}
	}
	third, err := world.retain.Execute(ctx)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	// A rerun at the same instant replays the ledger: the recorded
	// identity and the recorded outcome come back, never a second
	// outcome over already-purged rows.
	byClass := map[domain.RetentionClass]application.RetentionRun{}
	for _, run := range second.Runs {
		byClass[run.Class] = run
	}
	for _, run := range third.Runs {
		want, ok := byClass[run.Class]
		if !ok || !run.Replayed || run.ID != want.ID || run.Counts != want.Counts {
			t.Fatalf("class %s rerun = %+v, want replay of %+v", run.Class, run, want)
		}
	}
}

// TestPrivacyLifecycleConcurrent races deletion requests, export
// generations and due executions: one writer wins each race and every
// loser resolves the winner instead of duplicating it.
func TestPrivacyLifecycleConcurrent(t *testing.T) {
	world := newPrivacyWorld(t, testsource.NewRandom(9004))
	ctx := world.ctx
	ledger := &privacyLedger{subjects: make(map[string]*modelSubject), exports: make(map[string]*modelSubject)}
	runner := &privacyRunner{t: t, world: world, ledger: ledger}
	runner.setupSequence(930)
	if len(runner.diverged) > 0 {
		t.Fatalf("concurrent setup diverged: %+v", runner.diverged)
	}
	alpha := runner.local[0]
	accountID := domain.AccountID(alpha.uuid)
	const racers = 16
	var wg sync.WaitGroup
	type outcome struct {
		replayed bool
		err      error
	}
	requests := make(chan outcome, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := world.delRequest.Execute(ctx, application.RequestDeletionCommand{AccountID: accountID})
			if err != nil {
				requests <- outcome{err: err}
				return
			}
			requests <- outcome{replayed: res.Replayed}
		}()
	}
	wg.Wait()
	close(requests)
	fresh, replayed := 0, 0
	for res := range requests {
		if res.err != nil {
			t.Fatalf("concurrent deletion request: %v", res.err)
		}
		if res.replayed {
			replayed++
		} else {
			fresh++
		}
	}
	if fresh != 1 || replayed != racers-1 {
		t.Fatalf("racing deletion requests: fresh=%d replayed=%d, want 1/%d", fresh, replayed, racers-1)
	}
	expRes, err := world.expRequest.Execute(ctx, accountID)
	if err != nil {
		t.Fatalf("request export: %v", err)
	}
	generations := make(chan outcome, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := world.expGenerate.Execute(ctx, expRes.ExportID)
			if err != nil {
				generations <- outcome{err: err}
				return
			}
			generations <- outcome{replayed: res.Replayed}
		}()
	}
	wg.Wait()
	close(generations)
	built, dup := 0, 0
	for res := range generations {
		if res.err != nil {
			t.Fatalf("concurrent export generation: %v", res.err)
		}
		if res.replayed {
			dup++
		} else {
			built++
		}
	}
	if built != 1 || dup != racers-1 {
		t.Fatalf("racing export generations: built=%d replayed=%d, want 1/%d", built, dup, racers-1)
	}
	world.clock.Advance(8 * 24 * time.Hour)
	executions := make(chan int, 2)
	execErrs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := world.delExecute.Execute(ctx)
			if err != nil {
				execErrs <- err
				return
			}
			executions <- res.Executed
		}()
	}
	wg.Wait()
	close(executions)
	close(execErrs)
	for err := range execErrs {
		t.Fatalf("concurrent due execution: %v", err)
	}
	total := 0
	for executed := range executions {
		total += executed
	}
	if total != 1 {
		t.Fatalf("racing due executions anonymized %d accounts, want exactly 1", total)
	}
}

// TestPrivacyLifecycleDetectsMutantLeak proves the harness bites: an oracle
// that lets a foreign owner download must report the divergence on the
// cross-account serve.
func TestPrivacyLifecycleDetectsMutantLeak(t *testing.T) {
	world := newPrivacyWorld(t, testsource.NewRandom(9005))
	ledger := &privacyLedger{subjects: make(map[string]*modelSubject), exports: make(map[string]*modelSubject)}
	runner := &privacyRunner{t: t, world: world, ledger: ledger}
	runner.setupSequence(940)
	if len(runner.diverged) > 0 {
		t.Fatalf("mutant setup diverged: %+v", runner.diverged)
	}
	alpha, beta := runner.local[0], runner.local[1]
	runner.opRequestExport(alpha)
	runner.opGenerateExport(alpha.export.id)
	if len(runner.diverged) > 0 {
		t.Fatalf("mutant export diverged: %+v", runner.diverged)
	}
	// Mutant belief: the foreign owner downloads successfully. The
	// harness must report the gap between the belief and the refusal.
	runner.step++
	_, merr := world.expDownload.Execute(world.ctx, application.DownloadPersonalExportCommand{
		AccountID: domain.AccountID(beta.uuid), ExportID: alpha.export.id, Token: alpha.export.token,
	})
	runner.check("download-export", true, merr)
	if len(runner.diverged) == 0 {
		t.Fatal("mutant leak (foreign download accepted) reported no divergence")
	}
}
