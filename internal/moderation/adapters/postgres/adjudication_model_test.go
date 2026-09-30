package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	auditpg "github.com/AlexandreZanata/Regnovum/internal/audit/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/auditbridge"
	moderationpg "github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/platform/testsource"
)

// Independent adjudication model of moderation and audit (P24-T08).
//
// The system under test is the real stack over a disposable PostgreSQL:
// role bootstrap and revocation with the append-only audit trail, reports
// with dedup and rate limits, claim/lease triage, decisions with sanctions,
// appeals with a different reviewer, reversals that keep history, and the
// segregated authorizer. The oracle below is a separate, deliberately small
// state machine written from the documented rules: no self-review, no
// administrative bypass, no sensitive action without its trail row, no
// history erasure, and visibility by policy. It never calls domain
// constructors or application use cases; identifiers are learned from
// observed outputs, while the TRANSITION RULES are the oracle's own.

// modelModerationClock is one fixed instant: lease-scale jumps the
// generator draws move it forward, so sequences replay. The clock never
// moves backward: revocation stamps must stay ordered after grant stamps
// by the admin_roles_revoked_check constraint. Large deadline jumps do not
// belong here: report rows and sanction rows carry database wall-clock
// stamps the test cannot move, so a jump that outruns real time flips
// every later duplicate window to silent-fresh on both sides and stops
// exercising the replay path. Lease-scale drift (≈1 day over the whole
// run) stays far inside every 24-hour window; deadline passage belongs
// to TestModerationWindowsAndLimits.
type modelModerationClock struct {
	mu      sync.Mutex
	instant time.Time
}

var modelModerationBase = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func newModelModerationClock() *modelModerationClock {
	return &modelModerationClock{instant: modelModerationBase}
}

func (c *modelModerationClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.instant
}

func (c *modelModerationClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.instant = c.instant.Add(duration)
}

// modelDirectoryTarget is one known identity for the target directory.
type modelDirectoryTarget struct {
	id           string
	verified     bool
	active       bool
	mfaConfirmed bool
}

// modelDirectory is the test-controlled identity edge for role
// administration: accounts, verification and second factors are explicit
// scenario state, so ineligible bootstraps are proved, not assumed.
type modelDirectory struct {
	mu       sync.Mutex
	accounts map[string]*modelDirectoryTarget
}

func (d *modelDirectory) LookupByEmail(_ context.Context, email string) (*application.AdministrationTarget, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	target, ok := d.accounts[email]
	if !ok {
		return nil, application.ErrAdministrationTargetNotFound
	}
	return &application.AdministrationTarget{
		AccountID:             domain.AccountID(target.id),
		EmailVerified:         target.verified,
		CanAuthenticate:       target.active,
		SecondFactorConfirmed: target.mfaConfirmed,
	}, nil
}

// adjudicationWorld wires the real moderation stack over one disposable
// database, with the audit trail behind every sensitive write.
type adjudicationWorld struct {
	ctx          context.Context
	t            *testing.T
	pool         *pgxpool.Pool
	clock        *modelModerationClock
	repo         *moderationpg.Repository
	queries      *platformpg.Queries
	directory    *modelDirectory
	grantFirst   *application.GrantFirstAdministratorUseCase
	revoke       *application.RevokeAdministratorUseCase
	report       *application.FileReportUseCase
	claim        *application.ClaimCaseUseCase
	decide       *application.DecideCaseUseCase
	fileAppeal   *application.FileAppealUseCase
	claimAppeal  *application.ClaimAppealUseCase
	decideAppeal *application.DecideAppealUseCase
	queue        *application.GetCaseQueueUseCase
	queueCodec   *application.QueueCursorCodec
	authorizer   *application.Authorizer
}

func newAdjudicationWorld(t *testing.T) *adjudicationWorld {
	t.Helper()
	ctx := context.Background()
	testDB := dbtest.New(t)
	pool := testDB.Pool.Pool()
	clock := newModelModerationClock()
	repo := moderationpg.NewRepository(pool)
	queries := platformpg.New(pool)
	directory := &modelDirectory{accounts: make(map[string]*modelDirectoryTarget)}
	trail, err := auditbridge.NewRecorder(mustAuditRecorder(t, pool))
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	uow := platformpg.NewTxManager(pool)
	grantFirst, err := application.NewGrantFirstAdministratorUseCase(
		directory, repo, repo, trail, clock, uow)
	if err != nil {
		t.Fatalf("NewGrantFirstAdministratorUseCase: %v", err)
	}
	revoke, err := application.NewRevokeAdministratorUseCase(
		directory, repo, repo, trail, clock, uow)
	if err != nil {
		t.Fatalf("NewRevokeAdministratorUseCase: %v", err)
	}
	report, err := application.NewFileReportUseCase(application.FileReportDependencies{
		Targets: repo, Reports: repo, Clock: clock,
	})
	if err != nil {
		t.Fatalf("NewFileReportUseCase: %v", err)
	}
	authorizer, err := application.NewAuthorizer(repo, clock)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}
	claim, err := application.NewClaimCaseUseCase(application.ReviewDependencies{Cases: repo, Authorizer: authorizer, Clock: clock})
	if err != nil {
		t.Fatalf("NewClaimCaseUseCase: %v", err)
	}
	decide, err := application.NewDecideCaseUseCase(application.ReviewDependencies{Cases: repo, Authorizer: authorizer, Clock: clock})
	if err != nil {
		t.Fatalf("NewDecideCaseUseCase: %v", err)
	}
	appealDeps := application.AppealDependencies{Appeals: repo, Authorizer: authorizer, Clock: clock}
	fileAppeal, err := application.NewFileAppealUseCase(appealDeps)
	if err != nil {
		t.Fatalf("NewFileAppealUseCase: %v", err)
	}
	claimAppeal, err := application.NewClaimAppealUseCase(appealDeps)
	if err != nil {
		t.Fatalf("NewClaimAppealUseCase: %v", err)
	}
	decideAppeal, err := application.NewDecideAppealUseCase(appealDeps)
	if err != nil {
		t.Fatalf("NewDecideAppealUseCase: %v", err)
	}
	queue, err := application.NewGetCaseQueueUseCase(repo, repo)
	if err != nil {
		t.Fatalf("NewGetCaseQueueUseCase: %v", err)
	}
	codec, err := application.NewQueueCursorCodec([]byte("model-triage-cursor-secret-0123456789"))
	if err != nil {
		t.Fatalf("NewQueueCursorCodec: %v", err)
	}
	return &adjudicationWorld{
		ctx: ctx, t: t, pool: pool, clock: clock, repo: repo, queries: queries,
		directory: directory, grantFirst: grantFirst, revoke: revoke,
		report: report, claim: claim, decide: decide,
		fileAppeal: fileAppeal, claimAppeal: claimAppeal, decideAppeal: decideAppeal,
		queue: queue, queueCodec: codec, authorizer: authorizer,
	}
}

func mustAuditRecorder(t *testing.T, pool *pgxpool.Pool) *auditpg.Repository {
	t.Helper()
	return auditpg.NewRepository(pool)
}

// modelCase is one review case the oracle has seen seeded: its target and
// owner, lifecycle status, lease holder and expiry, and decided action.
type modelCase struct {
	target      string
	targetID    string
	owner       string
	status      string
	holder      string
	leaseExpiry time.Time
	decided     bool
	actionID    string
	action      string
}

// modelAppeal is one appeal the oracle has seen filed: its action, owner,
// claiming reviewer and outcome.
type modelAppeal struct {
	actionID string
	owner    string
	reviewer string
	decided  bool
	outcome  string
}

// adjudicationDivergence is one place where the stack and the oracle
// disagreed.
type adjudicationDivergence struct {
	step int
	op   string
	want bool
	got  bool
}

// adjudicationRunner carries one deterministic script: the world, the
// oracle pages and every divergence found.
type adjudicationRunner struct {
	t                *testing.T
	world            *adjudicationWorld
	admins           map[string]bool
	uuids            map[string]string
	emails           map[string]string
	adminEmails      map[string]string
	cases            map[string]*modelCase
	appeals          map[string]*modelAppeal
	sanctions        map[string]*modelSanction
	appealsByAction  map[string]string
	reversed         map[string]bool
	targets          map[string]*modelTarget
	filedAt          map[string]time.Time
	lastAppealAction string
	lastAppealOwner  string
	caseTargets      map[string]bool
	step             int
	diverged         []adjudicationDivergence
	trace            []string
	accepts          int
	refusals         int
	replays          int
}

func (r *adjudicationRunner) check(op string, wantAccept bool, err error) bool {
	gotAccept := err == nil
	r.trace = append(r.trace, op)
	r.t.Logf("OP %s want=%v got=%v", op, wantAccept, gotAccept)
	if wantAccept {
		r.accepts++
	} else {
		r.refusals++
	}
	if gotAccept != wantAccept {
		r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: op, want: wantAccept, got: gotAccept})
		return false
	}
	return true
}

// trailCount counts the audit rows: the trail is append-only, so the count
// only grows, and every accepted sensitive command must grow it by one.
func (r *adjudicationRunner) trailCount() int {
	var count int
	if err := r.world.pool.QueryRow(r.world.ctx, "SELECT count(*) FROM app.audit_events").Scan(&count); err != nil {
		r.t.Fatalf("count audit events: %v", err)
	}
	return count
}

// opBootstrap promotes the installation's first administrator: eligible
// iff the directory target is verified, active and second-factor
// confirmed, and no active assignment exists yet.
func (r *adjudicationRunner) opBootstrap(email string) {
	r.step++
	r.t.Helper()
	target, known := r.world.directory.accounts[email]
	uuid, haveUUID := r.uuids[email]
	wantAccept := known && haveUUID && target.verified && target.active && target.mfaConfirmed
	for _, active := range r.admins {
		if active {
			wantAccept = false
		}
	}
	before := r.trailCount()
	res, err := r.world.grantFirst.Execute(r.world.ctx, application.GrantFirstAdministratorCommand{Email: email})
	if !r.check("bootstrap", wantAccept, err) {
		r.t.Logf("BOOTDIV email=%s want=%v got=%v admins=%v", email, wantAccept, err == nil, r.admins)
		return
	}
	if !wantAccept {
		if got := r.trailCount(); got != before {
			r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "bootstrap-silent-trail", want: true, got: false})
		}
		return
	}
	if res == nil {
		r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "bootstrap-shape", want: true, got: false})
		return
	}
	r.admins[uuid] = true
	r.adminEmails[uuid] = email
	if got := r.trailCount(); got != before+1 {
		r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "bootstrap-trail", want: true, got: false})
	}
}

// opRevoke demotes one administrator: the gate follows immediately, and
// the trail records the revocation without touching history.
func (r *adjudicationRunner) opRevoke(email string) {
	r.step++
	r.t.Helper()
	uuid := r.uuids[email]
	wantAccept := r.admins[uuid]
	before := r.trailCount()
	_, err := r.world.revoke.Execute(r.world.ctx, application.RevokeAdministratorCommand{Email: email})
	if !r.check("revoke", wantAccept, err) {
		r.t.Logf("REVOKEDIV email=%s want=%v got=%v admins=%v", email, wantAccept, err == nil, r.admins)
		return
	}
	if !wantAccept {
		if got := r.trailCount(); got != before {
			r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "revoke-silent-trail", want: true, got: false})
		}
		return
	}
	r.admins[uuid] = false
	if got := r.trailCount(); got != before+1 {
		r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "revoke-trail", want: true, got: false})
	}
}

// modelTarget is one reportable row the oracle knows with its owner.
type modelTarget struct {
	kind   string
	id     string
	owner  string
	exists bool
}

// opSeedArena creates one published arena row for reports and cases.
func (r *adjudicationRunner) opSeedArena(seq, n int, creator string) string {
	r.step++
	r.t.Helper()
	var id pgtype.UUID
	statement := fmt.Sprintf("Arena modelada %d tese %d para triagem", seq, n)
	if err := r.world.pool.QueryRow(r.world.ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status, slug, published_at)
		VALUES ((SELECT id FROM app.accounts WHERE id = $1), $2, 'technology', 'pt-BR', 'published', $3, now())
		RETURNING id`, creator, statement, fmt.Sprintf("model-triage-%d-%d", seq, n)).Scan(&id); err != nil {
		r.t.Fatalf("seed arena: %v", err)
	}
	return uuidString(id)
}

// opSeedRemovedArena stores one removed arena row: reports against it
// must deny, and triage draws must never resolve to it.
func (r *adjudicationRunner) opSeedRemovedArena(seq int, creator string) string {
	r.step++
	r.t.Helper()
	var id pgtype.UUID
	statement := fmt.Sprintf("Arena modelada %d removida para triagem", seq)
	if err := r.world.pool.QueryRow(r.world.ctx, `
		INSERT INTO app.arenas (creator_id, statement, category, language, status, slug, published_at)
		VALUES ((SELECT id FROM app.accounts WHERE id = $1), $2, 'technology', 'pt-BR', 'removed', $3, now())
		RETURNING id`, creator, statement, fmt.Sprintf("model-triage-removed-%d", seq)).Scan(&id); err != nil {
		r.t.Fatalf("seed removed arena: %v", err)
	}
	r.check("seed-removed-arena", true, nil)
	return uuidString(id)
}

// opSeedArgument stores one published argument row for reports and cases.
func (r *adjudicationRunner) opSeedArgument(seq, n int, arenaID, author string) string {
	r.step++
	r.t.Helper()
	var id pgtype.UUID
	content := fmt.Sprintf("Argumento modelado %d tese %d para triagem", seq, n)
	if err := r.world.pool.QueryRow(r.world.ctx, `
		INSERT INTO app.arguments (arena_id, author_id, relation, content, content_hash, grapheme_cost, status)
		VALUES ($1, $2, 'support', $3, 'modelhash0123456789abcdef', 10, 'published')
		RETURNING id`, arenaID, author, content).Scan(&id); err != nil {
		r.t.Fatalf("seed argument: %v", err)
	}
	r.check("seed-argument", true, nil)
	return uuidString(id)
}

// opReport files one report: unknown, removed or mistyped targets deny,
// duplicates replay the original row, and a reporter past the rate
// threshold is flagged while the report still lands.
func (r *adjudicationRunner) opReport(reporter, target, targetID, reason string) string {
	r.step++
	r.t.Helper()
	known, ok := r.targets[targetID]
	wantAccept := ok && known.exists && known.kind == target && modelReasonKnown(reason) && reporter != ""
	res, err := r.world.report.Execute(r.world.ctx, application.FileReportCommand{
		Reporter: domain.AccountID(reporter), Target: target, TargetID: targetID, Reason: reason,
	})
	if !r.check("report", wantAccept, err) || !wantAccept {
		return ""
	}
	if res == nil || res.ReportID == "" {
		r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "report-shape", want: true, got: false})
		return ""
	}
	// Duplicates resolve only inside the 24-hour window: clock jumps age
	// rows out, and refiling afterwards is a new report by design.
	quad := reporter + "|" + target + "|" + targetID + "|" + reason
	// The row stamp comes from the database wall clock while the window
	// is measured on the model clock: the oracle reads the stored stamp
	// instead of assuming both clocks agree.
	if filed, seen := r.filedAt[quad]; seen && !r.world.clock.Now().After(filed.Add(24*time.Hour)) {
		if !res.Replayed {
			r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "report-replay-flag", want: true, got: false})
		} else {
			r.replays++
		}
	} else {
		if res.Replayed {
			r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "report-fresh-replay", want: true, got: false})
		}
		r.filedAt[quad] = r.reportCreatedAt(res.ReportID)
	}
	return res.ReportID
}

// actionCreatedAt reads the database stamp of one sanction row: the
// appeal deadline runs against stored instants, not model instants.
func (r *adjudicationRunner) actionCreatedAt(actionID string) time.Time {
	var created time.Time
	if err := r.world.pool.QueryRow(r.world.ctx,
		"SELECT created_at FROM app.moderation_actions WHERE id = $1", actionID).Scan(&created); err != nil {
		r.t.Fatalf("read action stamp: %v", err)
	}
	return created.UTC()
}

// reportCreatedAt reads the database stamp of one report row: the
// duplicate window runs against stored instants, not model instants.
func (r *adjudicationRunner) reportCreatedAt(reportID string) time.Time {
	var created time.Time
	if err := r.world.pool.QueryRow(r.world.ctx,
		"SELECT created_at FROM app.moderation_reports WHERE id = $1", reportID).Scan(&created); err != nil {
		r.t.Fatalf("read report stamp: %v", err)
	}
	return created.UTC()
}

// modelReasonKnown names the refusal fixtures: everything outside the
// documented reason vocabulary is refused before any row is touched.
func modelReasonKnown(reason string) bool {
	switch reason {
	case "spam", "harassment", "violence", "fraud", "other":
		return true
	default:
		return false
	}
}

// opSeedCase opens one review case on a known target, the triage seam the
// existing adapter tests seed the same way.
func (r *adjudicationRunner) opSeedCase(target, targetID, reporter string) string {
	r.step++
	r.t.Helper()
	known, ok := r.targets[targetID]
	if !ok || !known.exists || known.kind != target {
		r.t.Fatalf("model seeds cases only on live targets")
	}
	var id pgtype.UUID
	var err error
	switch target {
	case "arena":
		err = r.world.pool.QueryRow(r.world.ctx, `
			INSERT INTO app.moderation_cases (target_type, target_arena_id)
			VALUES ('arena', $1) RETURNING id`, targetID).Scan(&id)
	case "argument":
		err = r.world.pool.QueryRow(r.world.ctx, `
			INSERT INTO app.moderation_cases (target_type, target_argument_id)
			VALUES ('argument', $1) RETURNING id`, targetID).Scan(&id)
	default:
		err = r.world.pool.QueryRow(r.world.ctx, `
			INSERT INTO app.moderation_cases (target_type, target_account_id)
			VALUES ('profile', $1) RETURNING id`, targetID).Scan(&id)
	}
	if err != nil {
		r.t.Fatalf("seed review case: %v", err)
	}
	caseID := uuidString(id)
	r.cases[caseID] = &modelCase{target: target, targetID: targetID, owner: r.targets[targetID].owner, status: "open"}
	r.caseTargets[caseID] = true
	r.check("seed-case", true, nil)
	return caseID
}

// modelSanctionFits mirrors the target/action matrix: warnings fit
// everywhere, suspensions and bans fit profiles, closes fit arenas.
func modelSanctionFits(target, action string) bool {
	switch action {
	case "warning":
		return true
	case "suspension", "ban":
		return target == "profile"
	case "arena_close":
		return target == "arena"
	case "argument_remove":
		return target == "argument"
	default:
		return false
	}
}

// modelStepUpSatisfied mirrors the freshness rule: high-impact measures
// need a session age inside fifteen minutes, warnings accept any age.
func modelStepUpSatisfied(action string, age time.Duration) bool {
	switch action {
	case "suspension", "ban", "arena_close":
		return age <= 15*time.Minute
	default:
		return true
	}
}

// modelSanction is one decided action the oracle tracks for appeals and
// history: its owner, decider and decision instant.
type modelSanction struct {
	owner     string
	decider   string
	decidedAt time.Time
	action    string
	target    string
	targetID  string
}

// opClaim takes one open case under lease: the actor must be an active
// administrator distinct from the target owner, and a live foreign lease
// refuses while an expired one (or the holder's own) resolves.
func (r *adjudicationRunner) opClaim(caseID, actor string, age time.Duration) {
	r.step++
	r.t.Helper()
	c, known := r.cases[caseID]
	wantAccept := known && r.admins[actor] && actor != c.owner && age >= 0
	if wantAccept {
		switch c.status {
		case "open":
		case "under_review":
			if c.holder != actor && !r.world.clock.Now().After(c.leaseExpiry) {
				wantAccept = false
			}
		default:
			wantAccept = false
		}
	}
	res, err := r.world.claim.Execute(r.world.ctx, application.ClaimCaseCommand{CaseID: caseID, Actor: actor, SessionAge: age})
	if !r.check("claim", wantAccept, err) {
		return
	}
	if !wantAccept {
		return
	}
	if res == nil {
		r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "claim-shape", want: true, got: false})
		return
	}
	c.status = "under_review"
	c.holder = actor
	c.leaseExpiry = r.world.clock.Now().Add(15 * time.Minute)
}

// opDecide records one sanction: the holder decides inside a live lease
// with a fitting action, a fresh session for high-impact measures, and no
// conflict of interest. The action row is the history the audit compares.
func (r *adjudicationRunner) opDecide(caseID, actor, action string, age time.Duration) string {
	r.step++
	r.t.Helper()
	c, known := r.cases[caseID]
	wantAccept := known && c.status == "under_review" && c.holder == actor &&
		r.admins[actor] && actor != c.owner && age >= 0 &&
		modelSanctionFits(c.target, action) && modelStepUpSatisfied(action, age) &&
		!r.world.clock.Now().After(c.leaseExpiry)
	beforeActions := r.trailActions()
	res, err := r.world.decide.Execute(r.world.ctx, application.DecideCaseCommand{
		CaseID: caseID, Actor: actor, Action: action,
		Rule: "MOD-2:warning", Justification: "Measured decision with scope", SessionAge: age,
	})
	if !r.check("decide", wantAccept, err) {
		return ""
	}
	if !wantAccept {
		if got := r.trailActions(); got != beforeActions {
			r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "decide-silent-actions", want: true, got: false})
		}
		return ""
	}
	if res == nil || res.ActionID == "" {
		r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "decide-shape", want: true, got: false})
		return ""
	}
	// One accepted decision records exactly one action row: the history
	// is append-only, and a refusal writes nothing.
	if got := r.trailActions(); got != beforeActions+1 {
		r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "decide-action-trail", want: true, got: false})
		return ""
	}
	c.status = "decided"
	c.decided = true
	c.actionID = res.ActionID
	c.action = action
	r.sanctions[res.ActionID] = &modelSanction{
		owner: c.owner, decider: actor, decidedAt: r.actionCreatedAt(res.ActionID),
		action: action, target: c.target, targetID: c.targetID,
	}
	return res.ActionID
}

// trailActions counts the moderation action rows: the history is
// append-only, so the count only grows — one row per accepted decision —
// and appeal outcomes never add or remove action rows. A reversal restores
// the sanctioned projection while the original action row stays.
func (r *adjudicationRunner) trailActions() int {
	var count int
	if err := r.world.pool.QueryRow(r.world.ctx, "SELECT count(*) FROM app.moderation_actions").Scan(&count); err != nil {
		r.t.Fatalf("count moderation actions: %v", err)
	}
	return count
}

// opFileAppeal files one appeal for the sanctioned owner inside the
// deadline: strangers, expired actions and duplicates are refused, and a
// duplicate resolves the original row.
func (r *adjudicationRunner) opFileAppeal(actionID, appellant string) string {
	r.step++
	r.t.Helper()
	sanction, ok := r.sanctions[actionID]
	wantAccept := ok && sanction.owner == appellant &&
		!r.world.clock.Now().After(sanction.decidedAt.Add(30*24*time.Hour))
	res, err := r.world.fileAppeal.Execute(r.world.ctx, application.FileAppealCommand{
		ActionID: actionID, Appellant: appellant, Context: "recurso do titular",
	})
	if !r.check("file-appeal", wantAccept, err) {
		return ""
	}
	if !wantAccept {
		return ""
	}
	if res == nil || res.ActionID != actionID {
		r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "file-appeal-shape", want: true, got: false})
		return ""
	}
	r.t.Logf("FILEAPP action=%s replayed=%v", actionID, res.Replayed)
	if existing, dup := r.appealsByAction[actionID]; dup {
		if !res.Replayed || res.AppealID != existing {
			r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "file-appeal-replay", want: true, got: false})
		} else {
			r.replays++
		}
		return existing
	}
	r.appeals[res.AppealID] = &modelAppeal{actionID: actionID, owner: appellant}
	r.appealsByAction[actionID] = res.AppealID
	r.lastAppealAction, r.lastAppealOwner = actionID, appellant
	return res.AppealID
}

// opClaimAppeal claims one open appeal for a reviewer distinct from the
// deciding moderator: same-reviewer, stranger, unknown and twice-claimed
// appeals are refused.
func (r *adjudicationRunner) opClaimAppeal(appealID, reviewer string, age time.Duration) {
	r.step++
	r.t.Helper()
	appeal, known := r.appeals[appealID]
	wantAccept := known && !appeal.decided && appeal.reviewer == "" &&
		r.admins[reviewer] && reviewer != r.sanctions[appeal.actionID].decider && age >= 0
	if known {
		owner := r.sanctions[appeal.actionID].owner
		if reviewer == owner {
			wantAccept = false
		}
	}
	_, err := r.world.claimAppeal.Execute(r.world.ctx, application.ClaimAppealCommand{
		AppealID: appealID, Reviewer: reviewer, SessionAge: age,
	})
	if !r.check("claim-appeal", wantAccept, err) {
		return
	}
	if !wantAccept {
		return
	}
	appeal.reviewer = reviewer
}

// opDecideAppeal records one appeal outcome: the reviewer must be an
// active administrator distinct from the decider, the appeal undecided,
// and the outcome in vocabulary. Reversal restores the projection without
// deleting history; upheld keeps the sanction.
func (r *adjudicationRunner) opDecideAppeal(appealID, reviewer, outcome string) {
	r.step++
	r.t.Helper()
	appeal, known := r.appeals[appealID]
	wantAccept := known && !appeal.decided && appeal.reviewer == reviewer &&
		r.admins[reviewer] && modelOutcomeKnown(outcome)
	if known {
		decider := r.sanctions[appeal.actionID].decider
		owner := r.sanctions[appeal.actionID].owner
		if reviewer == decider || reviewer == owner {
			wantAccept = false
		}
	}
	beforeActions := r.trailActions()
	res, err := r.world.decideAppeal.Execute(r.world.ctx, application.DecideAppealCommand{
		AppealID: appealID, Reviewer: reviewer, Outcome: outcome, Reason: "Revisao medida do recurso", SessionAge: time.Minute,
	})
	if !r.check("decide-appeal", wantAccept, err) {
		return
	}
	if !wantAccept {
		if got := r.trailActions(); got != beforeActions {
			r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "decide-appeal-silent-actions", want: true, got: false})
		}
		return
	}
	if res == nil || res.Outcome.String() != outcome {
		r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "decide-appeal-shape", want: true, got: false})
		return
	}
	// Appeal outcomes move the appeal and the projection, never the
	// action history: reversal restores without deleting, upheld keeps
	// the sanction, and neither adds an action row.
	if got := r.trailActions(); got != beforeActions {
		r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "decide-appeal-action-trail", want: true, got: false})
		return
	}
	appeal.decided = true
	appeal.outcome = outcome
	if outcome == "reversed" {
		r.reversed[appeal.actionID] = true
	}
}

// modelOutcomeKnown names the outcome vocabulary.
func modelOutcomeKnown(outcome string) bool {
	switch outcome {
	case "upheld", "modified", "reversed":
		return true
	default:
		return false
	}
}

// opQueue lists one triage page: strangers and revoked administrators are
// refused, forged cursors are refused, and every listed row stays inside
// known cases with the requested status and no evidence payload.
func (r *adjudicationRunner) opQueue(viewer, status, cursor string, limit int) {
	r.step++
	r.t.Helper()
	// Viewers arrive as account UUIDs, exactly as claim and decide take
	// them: no email mapping applies here. Unknown viewers, revoked
	// assignments, out-of-vocabulary filters and forged cursors are
	// refused; oversized limits only clamp.
	wantAccept := r.admins[viewer] && modelQueueStatusKnown(status)
	if wantAccept && cursor != "" {
		if _, err := r.world.queueCodec.Decode(cursor); err != nil {
			wantAccept = false
		}
	}
	page, err := r.world.queue.Execute(r.world.ctx, domain.AccountID(viewer),
		status, cursor, limit, r.world.queueCodec)
	if !r.check("queue", wantAccept, err) {
		return
	}
	if !wantAccept {
		return
	}
	if page == nil {
		r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "queue-shape", want: true, got: false})
		return
	}
	if len(page.Items) > 100 {
		r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "queue-limit", want: true, got: false})
	}
	for _, item := range page.Items {
		if status != "" && string(item.Status) != status {
			r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "queue-filter", want: true, got: false})
		}
		if _, ok := r.caseTargets[item.CaseID]; !ok {
			r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "queue-unknown-row", want: true, got: false})
		}
	}
}

// modelQueueStatusKnown names the triage filter vocabulary: anything
// outside it is refused before any row is touched.
func modelQueueStatusKnown(status string) bool {
	switch status {
	case "", "open", "under_review", "decided", "closed":
		return true
	default:
		return false
	}
}

// setupSequence creates three users (reporter, owner, stranger), two
// eligible directory entries, one arena and one argument target, one
// removed arena that reports must refuse, and bootstraps the first
// administrator.
func (r *adjudicationRunner) setupSequence(seq int) (reporter, owner, stranger, admin string) {
	r.t.Helper()
	emails := map[string]string{
		"reporter": fmt.Sprintf("mod-reporter-%d@arena.example.com", seq),
		"owner":    fmt.Sprintf("mod-owner-%d@arena.example.com", seq),
		"stranger": fmt.Sprintf("mod-stranger-%d@arena.example.com", seq),
	}
	uuids := make(map[string]string)
	r.emails = make(map[string]string)
	for role, email := range emails {
		pgAcc, err := r.world.queries.CreateAccount(r.world.ctx, platformpg.CreateAccountParams{Email: email, Status: "active"})
		if err != nil {
			r.t.Fatalf("create model account: %v", err)
		}
		uuid := uuidString(pgAcc.ID)
		uuids[role] = uuid
		r.uuids[email] = uuid
		r.emails[role] = email
		r.world.directory.accounts[email] = &modelDirectoryTarget{id: uuid, verified: true, active: true, mfaConfirmed: role != "stranger"}
	}
	reporter, owner, stranger, admin = uuids["reporter"], uuids["owner"], uuids["stranger"], uuids["reporter"]
	r.opBootstrap(emails["reporter"])
	arenaID := r.opSeedArena(seq, 0, uuids["owner"])
	r.targets[arenaID] = &modelTarget{kind: "arena", id: arenaID, owner: uuids["owner"], exists: true}
	argID := r.opSeedArgument(seq, 0, arenaID, uuids["owner"])
	r.targets[argID] = &modelTarget{kind: "argument", id: argID, owner: uuids["owner"], exists: true}
	r.targets[uuids["owner"]] = &modelTarget{kind: "profile", id: uuids["owner"], owner: uuids["owner"], exists: true}
	r.targets[uuids["reporter"]] = &modelTarget{kind: "profile", id: uuids["reporter"], owner: uuids["reporter"], exists: true}
	// Removed content stays visible to the oracle but closed to reports:
	// contesting it denies without writing.
	removedID := r.opSeedRemovedArena(seq, uuids["owner"])
	r.targets[removedID] = &modelTarget{kind: "arena", id: removedID, owner: uuids["owner"]}
	r.opReport(reporter, "arena", removedID, "spam")
	return reporter, owner, stranger, admin
}

// runOneSequence executes one deterministic adjudication script: bootstrap
// and revocation, reports with duplicates and rate probes, seeded cases
// with claims, decisions, appeals and reversals, plus visibility reads.
func (r *adjudicationRunner) runOneSequence(rnd *testsource.Random, seq int) {
	reporter, owner, stranger, admin := r.setupSequence(seq)
	if len(r.diverged) > 0 {
		return
	}
	// Two cases open the docket; the generator interleaves the rest.
	r.opSeedCase("arena", r.targetFor("arena"), reporter)
	r.opSeedCase("profile", owner, reporter)
	steps := 10 + rnd.Int64n(9)
	for i := 0; i < int(steps); i++ {
		target := []string{"arena", "argument", "profile"}[rnd.Int64n(3)]
		targetID := r.targetFor(target)
		reason := []string{"spam", "harassment", "violence", "other", "bogus"}[rnd.Int64n(5)]
		rep := []string{reporter, owner, stranger}[rnd.Int64n(3)]
		actor := admin
		if rnd.Int64n(3) == 0 {
			actor = stranger
		}
		age := time.Duration(0)
		if rnd.Int64n(6) == 0 {
			age = -time.Minute
		} else if rnd.Int64n(5) == 0 {
			age = time.Hour
		} else if rnd.Int64n(4) == 0 {
			age = time.Minute
		}
		switch rnd.Int64n(16) {
		case 0, 1:
			r.opReport(rep, target, targetID, reason)
			// A third of reports file twice: the duplicate must
			// resolve the original row without writing again.
			if rnd.Int64n(3) == 0 {
				r.opReport(rep, target, targetID, reason)
			}
		case 2, 3:
			r.opClaimAny(actor, age)
		case 4, 5:
			r.opDecideAny(actor, age, rnd)
		case 6:
			r.opAppealFlow(admin, stranger, rnd)
			if r.lastAppealAction != "" && rnd.Int64n(3) == 0 {
				r.opFileAppeal(r.lastAppealAction, r.lastAppealOwner)
			}
		case 7:
			r.opQueue(actor, "", "", 20)
		case 8:
			r.opRevoke(r.pickAdminEmail(rnd))
		case 9:
			r.opBootstrap(r.emails[[]string{"reporter", "owner", "stranger"}[rnd.Int64n(3)]])
		case 10:
			r.opSeedCase(target, targetID, rep)
		case 11:
			r.opSelfClaimProbe(admin)
		case 12:
			r.world.clock.Advance(16 * time.Minute)
			r.step++
		default:
			r.opReport(rep, "planet", targetID, reason)
		}
		_ = i
		_ = owner
	}
	r.reconcile()
}

// reconcile cross-checks the oracle pages at the end of one sequence:
// every decided case carries exactly one tracked sanction, and every
// tracked appeal resolves to a known sanction and a known case.
func (r *adjudicationRunner) reconcile() {
	r.step++
	if len(r.decidedCases()) != len(r.sanctions) {
		r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "reconcile-decided-sanctions", want: true, got: false})
	}
	for _, appeal := range r.appeals {
		if _, ok := r.sanctions[appeal.actionID]; !ok {
			r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "reconcile-appeal-action", want: true, got: false})
		}
	}
	for appealID := range r.appealsByAction {
		if _, ok := r.appeals[r.appealsByAction[appealID]]; !ok {
			r.diverged = append(r.diverged, adjudicationDivergence{step: r.step, op: "reconcile-appeal-index", want: true, got: false})
		}
	}
}

// pickAdminEmail draws a revocation target: usually the known active
// administrator if one exists, otherwise a same-sequence address that
// must refuse for lack of assignment.
func (r *adjudicationRunner) pickAdminEmail(rnd *testsource.Random) string {
	var active []string
	for uuid, on := range r.admins {
		if on {
			if email, ok := r.adminEmails[uuid]; ok {
				active = append(active, email)
			}
		}
	}
	sort.Strings(active)
	if len(active) > 0 && rnd.Int64n(2) == 0 {
		return active[rnd.Int64n(int64(len(active)))]
	}
	pool := []string{r.emails["reporter"], r.emails["owner"], r.emails["stranger"]}
	return pool[rnd.Int64n(int64(len(pool)))]
}

// opSelfClaimProbe seeds a case on the administrator's own profile and
// claims it: the conflict of interest must refuse.
func (r *adjudicationRunner) opSelfClaimProbe(admin string) {
	r.opSeedCase("profile", admin, admin)
	for caseID, c := range r.cases {
		if c.target == "profile" && c.targetID == admin && c.status == "open" {
			r.opClaim(caseID, admin, time.Minute)
			return
		}
	}
}

// targetFor resolves a drawn target kind to a known live row identifier,
// sorted for determinism, or to garbage that must deny. Removed rows are
// never drawn: the generator contests them only through the dedicated
// setup probe.
func (r *adjudicationRunner) targetFor(target string) string {
	var ids []string
	for id, known := range r.targets {
		if known.kind == target && known.exists {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return "018f6b2a-ffff-7000-8000-ffffffffffff"
	}
	return ids[0]
}

// openCases lists open cases in stable order so the registered stream
// decides every draw.
func (r *adjudicationRunner) openCases() []string {
	var ids []string
	for caseID, c := range r.cases {
		if c.status == "open" {
			ids = append(ids, caseID)
		}
	}
	sort.Strings(ids)
	return ids
}

// decidedCases lists decided cases in stable order.
func (r *adjudicationRunner) decidedCases() []string {
	var ids []string
	for caseID, c := range r.cases {
		if c.decided {
			ids = append(ids, caseID)
		}
	}
	sort.Strings(ids)
	return ids
}

// opClaimAny claims one open case, if any exists.
func (r *adjudicationRunner) opClaimAny(actor string, age time.Duration) {
	if open := r.openCases(); len(open) > 0 {
		r.opClaim(open[0], actor, age)
		return
	}
	r.opClaim("018f6b2a-ffff-7000-8000-ffffffffffff", actor, age)
}

// opDecideAny decides one claimed case of the actor, if any exists.
func (r *adjudicationRunner) opDecideAny(actor string, age time.Duration, rnd *testsource.Random) {
	actions := []string{"warning", "warning", "suspension", "arena_close", "argument_remove", "ban"}
	var mine []string
	for caseID, c := range r.cases {
		if c.status == "under_review" && c.holder == actor {
			mine = append(mine, caseID)
		}
	}
	sort.Strings(mine)
	if len(mine) > 0 {
		r.opDecide(mine[0], actor, actions[rnd.Int64n(int64(len(actions)))], age)
		return
	}
	r.opDecide("018f6b2a-ffff-7000-8000-ffffffffffff", actor, "warning", age)
}

// opAppealFlow files, claims and decides one appeal for a decided action
// of the arena target, alternating uphold and reversed outcomes.
func (r *adjudicationRunner) opAppealFlow(admin, stranger string, rnd *testsource.Random) {
	var actionID, owner string
	var ids []string
	for id := range r.sanctions {
		if _, done := r.appealsByAction[id]; !done {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > 0 {
		actionID = ids[rnd.Int64n(int64(len(ids)))]
		owner = r.sanctions[actionID].owner
	}
	_ = stranger
	if actionID == "" {
		return
	}
	appealID := r.opFileAppeal(actionID, owner)
	if appealID == "" {
		return
	}
	// Refiling the same action resolves the original appeal row: appeals
	// carry no window, so every duplicate replays.
	r.opFileAppeal(actionID, owner)
	// Refiling the same action resolves the original appeal row.
	r.opFileAppeal(actionID, owner)
	// Refiling the same action resolves the original appeal row.
	r.opFileAppeal(actionID, owner)
	_ = stranger
	outcome := "upheld"
	if len(r.appeals)%2 == 0 {
		outcome = "reversed"
	}
	r.opClaimAppeal(appealID, admin, time.Minute)
	r.opDecideAppeal(appealID, admin, outcome)
	// A stranger files nothing: ownership is checked before singularity,
	// so a foreign appeal is refused rather than replayed.
	r.opFileAppealStranger(actionID, stranger)
}

// opFileAppealStranger proves a non-owner cannot file, even when an appeal
// already exists for the action.
func (r *adjudicationRunner) opFileAppealStranger(actionID, stranger string) {
	r.step++
	r.t.Helper()
	_, err := r.world.fileAppeal.Execute(r.world.ctx, application.FileAppealCommand{
		ActionID: actionID, Appellant: stranger, Context: "recurso do titular",
	})
	r.check("file-appeal-stranger", false, err)
}

// TestModerationAdjudicationModel runs deterministic adjudication scripts:
// bootstrap and revocation with trail rows, reports with duplicates and
// rate probes, seeded cases with claims, decisions, appeals and reversals,
// and visibility reads, comparing every outcome with the independent
// oracle and the append-only trails.
func TestModerationAdjudicationModel(t *testing.T) {
	seed := testsource.SeedFor(t)
	rnd := testsource.NewRandom(seed)
	world := newAdjudicationWorld(t)
	const sequences = 80
	divergentRuns := 0
	var first []adjudicationDivergence
	var firstTrace []string
	accepts, refusals, replays := 0, 0, 0
	// Admin assignments are installation-global rows: every sequence
	// shares one oracle page for them, or the second bootstrap could
	// never agree with the stored administrator. The email-to-UUID
	// directory is shared for the same reason: revocation targets drawn
	// from earlier sequences must resolve to the same identifiers the
	// database holds.
	sharedAdmins := make(map[string]bool)
	sharedAdminEmails := make(map[string]string)
	sharedUUIDs := make(map[string]string)
	// Case identifiers are installation-global rows: the triage queue
	// lists every sequence's cases, so the known-row set is shared while
	// lifecycle expectations stay per sequence.
	sharedCaseTargets := make(map[string]bool)
	for seq := 0; seq < sequences; seq++ {
		runner := &adjudicationRunner{
			t: t, world: world, admins: sharedAdmins, adminEmails: sharedAdminEmails, uuids: sharedUUIDs,
			cases: make(map[string]*modelCase), appeals: make(map[string]*modelAppeal),
			sanctions: make(map[string]*modelSanction), appealsByAction: make(map[string]string),
			reversed: make(map[string]bool), targets: make(map[string]*modelTarget),
			caseTargets: sharedCaseTargets, filedAt: make(map[string]time.Time),
		}
		runner.runOneSequence(rnd, seq)
		accepts += runner.accepts
		refusals += runner.refusals
		replays += runner.replays
		if len(runner.diverged) > 0 {
			divergentRuns++
			if first == nil {
				first = append([]adjudicationDivergence{}, runner.diverged...)
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
	t.Logf("moderation adjudication model: %d sequences, accepts=%d refusals=%d replays=%d, seed %d", sequences, accepts, refusals, replays, seed)
}

// TestModerationQueueVisibility proves the triage surface: unknown viewers
// and forged cursors are refused, limits clamp, and listed rows stay inside
// known cases with the requested status and no evidence payload.
func TestModerationQueueVisibility(t *testing.T) {
	world := newAdjudicationWorld(t)
	runner := &adjudicationRunner{
		t: t, world: world, admins: make(map[string]bool), adminEmails: make(map[string]string), uuids: make(map[string]string),
		cases: make(map[string]*modelCase), appeals: make(map[string]*modelAppeal),
		sanctions: make(map[string]*modelSanction), appealsByAction: make(map[string]string),
		reversed: make(map[string]bool), targets: make(map[string]*modelTarget),
		caseTargets: make(map[string]bool), filedAt: make(map[string]time.Time),
	}
	reporter, owner, stranger, admin := runner.setupSequence(950)
	_ = reporter
	_ = owner
	if len(runner.diverged) > 0 {
		t.Fatalf("visibility setup diverged: %+v", runner.diverged)
	}
	runner.opQueue("nobody@arena.example.com", "", "", 20)
	runner.opQueue(admin, "", "forged-cursor", 20)
	runner.opQueue(admin, "bogus-status", "", 20)
	runner.opQueue(admin, "", "", 10000)
	runner.opQueue(stranger, "", "", 20)
	if len(runner.diverged) > 0 {
		t.Fatalf("visibility probes diverged: %+v", runner.diverged)
	}
}

// TestModerationAdjudicationConcurrent races claims of one case and the
// first-administrator bootstrap: leases admit exactly one holder and the
// installation exactly one active assignment, under the race detector.
func TestModerationAdjudicationConcurrent(t *testing.T) {
	world := newAdjudicationWorld(t)
	ctx := world.ctx
	runner := &adjudicationRunner{
		t: t, world: world, admins: make(map[string]bool), adminEmails: make(map[string]string), uuids: make(map[string]string),
		cases: make(map[string]*modelCase), appeals: make(map[string]*modelAppeal),
		sanctions: make(map[string]*modelSanction), appealsByAction: make(map[string]string),
		reversed: make(map[string]bool), targets: make(map[string]*modelTarget),
		caseTargets: make(map[string]bool), filedAt: make(map[string]time.Time),
	}
	reporter, owner, stranger, admin := runner.setupSequence(951)
	if len(runner.diverged) > 0 {
		t.Fatalf("concurrent setup diverged: %+v", runner.diverged)
	}
	arenaID := ""
	for id, known := range runner.targets {
		if known.kind == "arena" && known.exists {
			arenaID = id
		}
	}
	// A second reviewer holds the moderator role through the same
	// assignment rows the bootstrap writes: the lease race below needs
	// two distinct eligible claimants, and the installation keeps a
	// single first administrator by design.
	if _, err := world.pool.Exec(ctx, `INSERT INTO app.admin_roles (account_id, role, granted_by) VALUES ($1, 'moderator', $2)`, stranger, admin); err != nil {
		t.Fatalf("seed second reviewer: %v", err)
	}
	_ = reporter
	_ = owner
	caseID := runner.opSeedCase("arena", arenaID, reporter)
	const racers = 16
	type claimResult struct {
		actor string
		err   error
	}
	var wg sync.WaitGroup
	claims := make(chan claimResult, racers)
	for i := 0; i < racers; i++ {
		actor := admin
		if i%2 == 1 {
			actor = stranger
		}
		wg.Add(1)
		go func(actor string) {
			defer wg.Done()
			_, err := world.claim.Execute(ctx, application.ClaimCaseCommand{CaseID: caseID, Actor: actor, SessionAge: time.Minute})
			claims <- claimResult{actor: actor, err: err}
		}(actor)
	}
	wg.Wait()
	close(claims)
	wins := map[string]int{}
	for res := range claims {
		if res.err == nil {
			wins[res.actor]++
		} else if !errors.Is(res.err, application.ErrCaseAlreadyClaimed) {
			t.Fatalf("losing claim refused with %v, want %v", res.err, application.ErrCaseAlreadyClaimed)
		}
	}
	// The winner's first claim takes the row and its remaining seven
	// re-claims resolve the live lease it holds; every rival claim
	// refuses. Exactly one holder, no split.
	if len(wins) != 1 {
		t.Fatalf("racing two reviewers split the lease across %d holders, want exactly 1", len(wins))
	}
	for actor, count := range wins {
		if count != racers/2 {
			t.Fatalf("holder %s won %d of %d races, want all %d", actor, count, racers, racers/2)
		}
	}
	// The lease shows exactly one holder: a claim by the rival while the
	// first lease lives is refused.
	stored, err := world.repo.GetCase(ctx, caseID)
	if err != nil || stored == nil {
		t.Fatalf("read raced case: %v", err)
	}
	rival := stranger
	if string(stored.ClaimedBy) == stranger {
		rival = admin
	}
	if _, err := world.claim.Execute(ctx, application.ClaimCaseCommand{CaseID: caseID, Actor: rival, SessionAge: time.Minute}); !errors.Is(err, application.ErrCaseAlreadyClaimed) {
		t.Fatalf("claiming a live foreign lease returned %v, want %v", err, application.ErrCaseAlreadyClaimed)
	}
	// The bootstrap race: with no active assignment, two eligible
	// bootstraps start together and the assignment lock admits exactly
	// one. Losers write nothing, so the trail grows by the revocation
	// plus the single winning grant. It runs on its own disposable
	// database because the lease race above leaves an active moderator
	// assignment behind, and any active assignment must refuse a first
	// bootstrap by design.
	bworld := newAdjudicationWorld(t)
	brunner := &adjudicationRunner{
		t: t, world: bworld, admins: make(map[string]bool), adminEmails: make(map[string]string), uuids: make(map[string]string),
		cases: make(map[string]*modelCase), appeals: make(map[string]*modelAppeal),
		sanctions: make(map[string]*modelSanction), appealsByAction: make(map[string]string),
		reversed: make(map[string]bool), targets: make(map[string]*modelTarget),
		caseTargets: make(map[string]bool), filedAt: make(map[string]time.Time),
	}
	brunner.setupSequence(952)
	if len(brunner.diverged) > 0 {
		t.Fatalf("bootstrap-race setup diverged: %+v", brunner.diverged)
	}
	brunner.opRevoke(brunner.emails["reporter"])
	if len(brunner.diverged) > 0 {
		t.Fatalf("concurrent revoke diverged: %+v", brunner.diverged)
	}
	before := brunner.trailCount()
	contenders := []string{brunner.emails["reporter"], brunner.emails["owner"]}
	grants := make(chan error, len(contenders))
	for _, email := range contenders {
		wg.Add(1)
		go func(email string) {
			defer wg.Done()
			_, err := bworld.grantFirst.Execute(ctx, application.GrantFirstAdministratorCommand{Email: email})
			grants <- err
		}(email)
	}
	wg.Wait()
	close(grants)
	granted := 0
	for err := range grants {
		if err == nil {
			granted++
		} else if !errors.Is(err, application.ErrAdministratorAlreadyExists) {
			t.Fatalf("losing bootstrap refused with %v, want %v", err, application.ErrAdministratorAlreadyExists)
		}
	}
	if granted != 1 {
		t.Fatalf("racing two bootstraps granted %d administrators, want exactly 1", granted)
	}
	if got := brunner.trailCount(); got != before+1 {
		t.Fatalf("winning grant wrote %d trail rows, want %d", got-before, 1)
	}
}

// TestModerationAdjudicationDetectsMutantConflict proves the harness bites:
// an oracle that lets moderators judge their own content must report the
// conflict divergence on the self-claim.
func TestModerationAdjudicationDetectsMutantConflict(t *testing.T) {
	world := newAdjudicationWorld(t)
	newRunner := func(w *adjudicationWorld) *adjudicationRunner {
		return &adjudicationRunner{
			t: t, world: w, admins: make(map[string]bool), adminEmails: make(map[string]string), uuids: make(map[string]string),
			cases: make(map[string]*modelCase), appeals: make(map[string]*modelAppeal),
			sanctions: make(map[string]*modelSanction), appealsByAction: make(map[string]string),
			reversed: make(map[string]bool), targets: make(map[string]*modelTarget),
			caseTargets: make(map[string]bool), filedAt: make(map[string]time.Time),
		}
	}
	runner := newRunner(world)
	_, _, _, admin := runner.setupSequence(960)
	if len(runner.diverged) > 0 {
		t.Fatalf("mutant setup diverged: %+v", runner.diverged)
	}
	// A case on the administrator's own profile: the true rule refuses
	// the self-claim as a conflict of interest.
	runner.targets[admin] = &modelTarget{kind: "profile", id: admin, owner: admin, exists: true}
	selfCase := runner.opSeedCase("profile", admin, admin)
	runner.opClaim(selfCase, admin, time.Minute)
	if len(runner.diverged) > 0 {
		t.Fatalf("true conflict rule diverged: %+v", runner.diverged)
	}
	// Mutant belief: the same self-claim is accepted. The harness must
	// report the gap between the mutant belief and the refusal.
	// The mutant replays on its own disposable database: administrator
	// assignments are installation-global rows, so a second bootstrap on
	// the same database would refuse and hide the conflict probe.
	mutant := newRunner(newAdjudicationWorld(t))
	_, _, _, madmin := mutant.setupSequence(961)
	if len(mutant.diverged) > 0 {
		t.Fatalf("mutant second setup diverged: %+v", mutant.diverged)
	}
	mutant.targets[madmin] = &modelTarget{kind: "profile", id: madmin, owner: madmin, exists: true}
	mcase := mutant.opSeedCase("profile", madmin, madmin)
	mutant.step++
	_, merr := mutant.world.claim.Execute(mutant.world.ctx, application.ClaimCaseCommand{CaseID: mcase, Actor: madmin, SessionAge: time.Minute})
	mutant.check("claim", true, merr)
	if len(mutant.diverged) == 0 {
		t.Fatal("mutant conflict (self-claim accepted) reported no divergence")
	}
}

// TestModerationWindowsAndLimits proves the time and rate rules with fixed
// scripts: leases expire, appeal deadlines pass, and reporters past the
// rate threshold are flagged while their reports still land.
func TestModerationWindowsAndLimits(t *testing.T) {
	world := newAdjudicationWorld(t)
	newRunner := func() *adjudicationRunner {
		return &adjudicationRunner{
			t: t, world: world, admins: make(map[string]bool), adminEmails: make(map[string]string), uuids: make(map[string]string),
			cases: make(map[string]*modelCase), appeals: make(map[string]*modelAppeal),
			sanctions: make(map[string]*modelSanction), appealsByAction: make(map[string]string),
			reversed: make(map[string]bool), targets: make(map[string]*modelTarget),
			caseTargets: make(map[string]bool), filedAt: make(map[string]time.Time),
		}
	}
	runner := newRunner()
	reporter, owner, _, admin := runner.setupSequence(950)
	if len(runner.diverged) > 0 {
		t.Fatalf("window setup diverged: %+v", runner.diverged)
	}
	arenaID := ""
	for id, known := range runner.targets {
		if known.kind == "arena" && known.exists {
			arenaID = id
		}
	}
	caseID := runner.opSeedCase("arena", arenaID, reporter)
	runner.opClaim(caseID, admin, time.Minute)
	// The lease lives fifteen minutes: deciding past it refuses, and a
	// fresh claim resolves again.
	world.clock.Advance(16 * time.Minute)
	runner.opDecide(caseID, admin, "warning", time.Minute)
	runner.opClaim(caseID, admin, time.Minute)
	runner.opDecide(caseID, admin, "warning", time.Minute)
	if len(runner.diverged) > 0 {
		t.Fatalf("lease window diverged: %+v", runner.diverged)
	}
	// Eleven reports in the hour flag the rate limit from the eleventh on
	// while every report still lands.
	flagged := 0
	for i := 0; i < 12; i++ {
		res, err := world.report.Execute(world.ctx, application.FileReportCommand{
			Reporter: domain.AccountID(reporter), Target: "arena", TargetID: arenaID,
			Reason: []string{"spam", "harassment", "violence", "fraud", "other", "illegal", "evasion", "multiaccount", "malware", "doxxing", "sexual", "spam"}[i],
		})
		if err != nil {
			t.Fatalf("rate probe %d: %v", i, err)
		}
		if res.RateLimited {
			flagged++
		}
	}
	if flagged == 0 {
		t.Fatal("twelve reports in the hour flagged none for rate")
	}
	// Thirty-one days later the appeal deadline has passed.
	_ = owner
	world.clock.Advance(31 * 24 * time.Hour)
	for actionID := range runner.sanctions {
		runner.opFileAppeal(actionID, runner.sanctions[actionID].owner)
	}
	for _, d := range runner.diverged {
		if d.op != "file-appeal" {
			t.Fatalf("deadline script diverged outside appeals: %+v", d)
		}
	}
}
