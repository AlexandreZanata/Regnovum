package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/persuasion/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/persuasion/application"
	"github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// allowAttributionModerator authorizes every moderator; the counter is
// atomic because concurrent decisions share the same authorizer.
type allowAttributionModerator struct{ calls atomic.Int64 }

func (a *allowAttributionModerator) EnsureModerator(_ context.Context, _ domain.ModeratorID) error {
	a.calls.Add(1)
	return nil
}

type denyAttributionModerator struct{}

func (denyAttributionModerator) EnsureModerator(_ context.Context, _ domain.ModeratorID) error {
	return application.ErrNotAuthorized
}

type captureAttributionAudit struct {
	events []application.AttributionModerationEvent
}

func (a *captureAttributionAudit) RecordAttributionModeration(_ context.Context, event application.AttributionModerationEvent) error {
	a.events = append(a.events, event)
	return nil
}

// moderationRow is the retained row read as the source of truth: validity,
// the decision record and the links that moderation must never alter.
type moderationRow struct {
	status           string
	invalidatedAt    pgtype.Timestamptz
	reason           pgtype.Text
	moderatedBy      pgtype.UUID
	moderatedAt      pgtype.Timestamptz
	positionChangeID pgtype.UUID
	attributorID     pgtype.UUID
	argumentID       pgtype.UUID
	createdAt        pgtype.Timestamptz
}

func readModerationRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, attributionID pgtype.UUID) moderationRow {
	t.Helper()
	var row moderationRow
	if err := pool.QueryRow(ctx, `
		SELECT status, invalidated_at, moderation_reason, moderated_by, moderated_at,
		       position_change_id, attributor_id, argument_id, created_at
		FROM app.persuasion_attributions WHERE id = $1`, attributionID).Scan(
		&row.status, &row.invalidatedAt, &row.reason, &row.moderatedBy, &row.moderatedAt,
		&row.positionChangeID, &row.attributorID, &row.argumentID, &row.createdAt); err != nil {
		t.Fatalf("read attribution row: %v", err)
	}
	return row
}

// moderationHarness wires the recording and moderation use cases over one
// isolated database.
type moderationHarness struct {
	pool          *pgxpool.Pool
	repo          *postgres.Repository
	record        *application.RecordAttributionsUseCase
	invalidate    *application.InvalidateAttributionUseCase
	restore       *application.RestoreAttributionUseCase
	audit         *captureAttributionAudit
	authorizer    *allowAttributionModerator
	attributor    pgtype.UUID
	moderator     pgtype.UUID
	author        pgtype.UUID
	arena         pgtype.UUID
	mustModerator domain.ModeratorID
}

func newAttributionModerationHarness(t *testing.T) *moderationHarness {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	pool := db.Pool.Pool()
	q := platformpg.New(pool)

	attributor := mustPersuasionAccount(t, ctx, q, "persuasion-moderation-owner@arena.example.com")
	author := mustPersuasionAccount(t, ctx, q, "persuasion-moderation-author@arena.example.com")
	moderator := mustPersuasionAccount(t, ctx, q, "persuasion-moderator@arena.example.com")
	arena := mustPersuasionArena(t, ctx, pool, attributor, "persuasion-moderation-arena")

	repo := postgres.NewRepository(pool)
	uow := platformpg.NewTxManager(pool)
	audit := &captureAttributionAudit{}
	authorizer := &allowAttributionModerator{}
	clock := clockseed.NewClock()
	moderatorID, err := domain.ParseModeratorID(uuidText(moderator))
	if err != nil {
		t.Fatalf("ParseModeratorID: %v", err)
	}

	return &moderationHarness{
		pool:          pool,
		repo:          repo,
		record:        application.NewRecordAttributionsUseCase(repo, domain.DefaultEligibilityPolicy(), uow),
		invalidate:    application.NewInvalidateAttributionUseCase(repo, authorizer, audit, clock, uow),
		restore:       application.NewRestoreAttributionUseCase(repo, authorizer, audit, clock, uow),
		audit:         audit,
		authorizer:    authorizer,
		attributor:    attributor,
		moderator:     moderator,
		author:        author,
		arena:         arena,
		mustModerator: moderatorID,
	}
}

// seedAttribution records one attribution through the production recording
// path and returns the attribution identifier.
func (h *moderationHarness) seedAttribution(t *testing.T, ctx context.Context, changeID, argumentID pgtype.UUID) pgtype.UUID {
	t.Helper()
	if _, err := h.record.Execute(ctx, recordAttributionsCommand(h.attributor, changeID, argumentID)); err != nil {
		t.Fatalf("seed attribution: %v", err)
	}
	var id pgtype.UUID
	if err := h.pool.QueryRow(ctx, `
		SELECT id FROM app.persuasion_attributions
		WHERE position_change_id = $1 AND argument_id = $2`, changeID, argumentID).Scan(&id); err != nil {
		t.Fatalf("read seeded attribution: %v", err)
	}
	return id
}

func (h *moderationHarness) command(attributionID pgtype.UUID, reason string) application.ModerateAttributionCommand {
	return application.ModerateAttributionCommand{
		ActorAccountID: uuidText(h.moderator),
		AttributionID:  uuidText(attributionID),
		Reason:         reason,
	}
}

func TestAttributionModerationEndToEnd(t *testing.T) {
	ctx := context.Background()
	h := newAttributionModerationHarness(t)

	changeAt := time.Now().UTC().Add(-time.Minute)
	change := mustPersuasionChange(t, ctx, h.pool, h.arena, h.attributor, changeAt)
	argument := mustPersuasionArgument(t, ctx, h.pool, h.arena, h.author, "Argumento creditado sob decisão", "published", changeAt.Add(-time.Hour))
	attribution := h.seedAttribution(t, ctx, change, argument)
	before := readModerationRow(t, ctx, h.pool, attribution)
	if before.status != "valid" || before.reason.Valid || before.moderatedAt.Valid {
		t.Fatalf("fresh attribution = %+v, want valid without decision", before)
	}

	// Invalidation records the decision and keeps every link untouched.
	invalidated, err := h.invalidate.Execute(ctx, h.command(attribution, "atribuição fraudulenta confirmada no caso 42"))
	if err != nil {
		t.Fatalf("invalidate Execute() error = %v", err)
	}
	if invalidated.Replayed || invalidated.Attribution.IsValid() {
		t.Fatalf("invalidate result = %+v, want a fresh invalidation", invalidated)
	}
	if invalidated.Attribution.Status != domain.AttributionStatusInvalid {
		t.Fatalf("status = %q, want invalid", invalidated.Attribution.Status)
	}
	decision, ok := invalidated.Attribution.DecisionOf()
	if !ok || decision.Action != domain.ModerationActionInvalidate || !decision.Actor.Equals(h.mustModerator) {
		t.Fatalf("recorded decision = %+v", decision)
	}
	afterInvalidation := readModerationRow(t, ctx, h.pool, attribution)
	if afterInvalidation.status != "invalid" || !afterInvalidation.invalidatedAt.Valid {
		t.Fatalf("stored invalidation = %+v", afterInvalidation)
	}
	if afterInvalidation.reason.String != "atribuição fraudulenta confirmada no caso 42" || !afterInvalidation.moderatedBy.Valid {
		t.Fatalf("stored decision record = %+v", afterInvalidation)
	}
	if !afterInvalidation.moderatedAt.Time.Equal(afterInvalidation.invalidatedAt.Time) {
		t.Fatalf("moderated_at = %s, invalidated_at = %s, want the same decision instant",
			afterInvalidation.moderatedAt.Time, afterInvalidation.invalidatedAt.Time)
	}
	if afterInvalidation.positionChangeID != before.positionChangeID || afterInvalidation.attributorID != before.attributorID ||
		afterInvalidation.argumentID != before.argumentID || !afterInvalidation.createdAt.Time.Equal(before.createdAt.Time) {
		t.Fatalf("invalidation altered the retained facts: %+v", afterInvalidation)
	}
	if len(h.audit.events) != 1 || h.audit.events[0].Action != domain.ModerationActionInvalidate {
		t.Fatalf("audit events = %+v, want one invalidation", h.audit.events)
	}
	if h.audit.events[0].ActorAccountID.String() == uuidText(h.attributor) {
		t.Fatal("the audit event must not carry the attributor identity")
	}

	// The retry replays the recorded decision: same facts, no extra event.
	replay, err := h.invalidate.Execute(ctx, h.command(attribution, "segunda tentativa com outro motivo"))
	if err != nil {
		t.Fatalf("replayed invalidate Execute() error = %v", err)
	}
	if !replay.Replayed {
		t.Fatal("reapplying an existing invalidation must resolve as a replay")
	}
	afterReplay := readModerationRow(t, ctx, h.pool, attribution)
	if afterReplay.reason.String != afterInvalidation.reason.String || !afterReplay.moderatedAt.Time.Equal(afterInvalidation.moderatedAt.Time) {
		t.Fatalf("replay rewrote the decision: %+v", afterReplay)
	}
	if len(h.audit.events) != 1 {
		t.Fatalf("audit events = %d, want the replay to add none", len(h.audit.events))
	}

	// Restoration reverses the invalidation and records its own decision.
	restored, err := h.restore.Execute(ctx, h.command(attribution, "recurso aceito: atribuição legítima"))
	if err != nil {
		t.Fatalf("restore Execute() error = %v", err)
	}
	if restored.Replayed || !restored.Attribution.IsValid() {
		t.Fatalf("restore result = %+v, want a fresh restoration", restored)
	}
	afterRestore := readModerationRow(t, ctx, h.pool, attribution)
	if afterRestore.status != "valid" || afterRestore.invalidatedAt.Valid {
		t.Fatalf("stored restoration = %+v, want valid without instant", afterRestore)
	}
	if afterRestore.reason.String != "recurso aceito: atribuição legítima" {
		t.Fatalf("restoration must record its own reason: %+v", afterRestore)
	}
	if len(h.audit.events) != 2 || h.audit.events[1].Action != domain.ModerationActionRestore {
		t.Fatalf("audit events = %+v, want the restoration recorded", h.audit.events)
	}
	if restoredDecision, ok := restored.Attribution.DecisionOf(); !ok || restoredDecision.Action != domain.ModerationActionRestore {
		t.Fatalf("restoration decision = %+v", restoredDecision)
	}

	// A full cycle is repeatable and the attribution is never deleted.
	if _, err := h.invalidate.Execute(ctx, h.command(attribution, "reincidência confirmada após o recurso")); err != nil {
		t.Fatalf("second invalidation error = %v", err)
	}
	cycled := readModerationRow(t, ctx, h.pool, attribution)
	if cycled.status != "invalid" || cycled.reason.String != "reincidência confirmada após o recurso" {
		t.Fatalf("second cycle = %+v", cycled)
	}
	if !cycled.positionChangeID.Valid || !cycled.argumentID.Valid || !cycled.attributorID.Valid {
		t.Fatal("the attribution row must be retained with every link")
	}
	if len(h.audit.events) != 3 {
		t.Fatalf("audit events = %d, want three decisions", len(h.audit.events))
	}

	// An unknown attribution is not found; nothing is written or audited.
	unknown := pgtype.UUID{Bytes: [16]byte{0xde, 0xad, 0xbe, 0xef, 0x91, 0x92, 0x93, 0x94, 0x95, 0x96, 0x97, 0x98, 0x99, 0x9a, 0x9b, 0x9c}, Valid: true}
	result, err := h.invalidate.Execute(ctx, h.command(unknown, "alvo inexistente"))
	if !errors.Is(err, application.ErrAttributionNotFound) || result != nil {
		t.Fatalf("unknown attribution: result = %+v, error = %v", result, err)
	}
	if len(h.audit.events) != 3 {
		t.Fatalf("audit events = %d, want none for an unknown target", len(h.audit.events))
	}

	// Negative authorization: no change and no audit event.
	denied := application.NewInvalidateAttributionUseCase(h.repo, denyAttributionModerator{}, h.audit, clockseed.NewClock(), platformpg.NewTxManager(h.pool))
	if _, err := denied.Execute(ctx, h.command(attribution, "tentativa sem autorização")); !errors.Is(err, application.ErrNotAuthorized) {
		t.Fatalf("denied Execute() error = %v, want ErrNotAuthorized", err)
	}
	final := readModerationRow(t, ctx, h.pool, attribution)
	if final.reason.String != cycled.reason.String || !final.moderatedAt.Time.Equal(cycled.moderatedAt.Time) {
		t.Fatalf("denied moderation changed the row: %+v", final)
	}
	if len(h.audit.events) != 3 {
		t.Fatalf("audit events = %d, want none after a denial", len(h.audit.events))
	}
}

// TestAttributionModerationConcurrentDecisions proves the row lock: two
// moderators acting on the same attribution produce exactly one decision, the
// loser replays the recorded state and only one audit event is emitted.
func TestAttributionModerationConcurrentDecisions(t *testing.T) {
	ctx := context.Background()
	h := newAttributionModerationHarness(t)

	changeAt := time.Now().UTC().Add(-time.Minute)
	change := mustPersuasionChange(t, ctx, h.pool, h.arena, h.attributor, changeAt)
	argument := mustPersuasionArgument(t, ctx, h.pool, h.arena, h.author, "Argumento para decisão concorrente", "published", changeAt.Add(-time.Hour))
	attribution := h.seedAttribution(t, ctx, change, argument)

	reasons := []string{"decisão concorrente um", "decisão concorrente dois"}
	start := make(chan struct{})
	results := make(chan *application.AttributionModerationResult, len(reasons))
	errs := make(chan error, len(reasons))
	var waitGroup sync.WaitGroup
	for _, reason := range reasons {
		waitGroup.Add(1)
		go func(reason string) {
			defer waitGroup.Done()
			<-start
			result, err := h.invalidate.Execute(ctx, h.command(attribution, reason))
			results <- result
			errs <- err
		}(reason)
	}
	close(start)
	waitGroup.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent invalidate error = %v", err)
		}
	}
	applied := 0
	replayed := 0
	var winnerReason string
	for result := range results {
		if result.Replayed {
			replayed++
			continue
		}
		applied++
		winnerReason = result.Attribution.Decision.Reason.String()
	}
	if applied != 1 || replayed != 1 {
		t.Fatalf("applied = %d, replayed = %d, want exactly one of each", applied, replayed)
	}
	row := readModerationRow(t, ctx, h.pool, attribution)
	if row.status != "invalid" {
		t.Fatalf("status = %q, want invalid", row.status)
	}
	if row.reason.String != winnerReason {
		t.Fatalf("stored reason = %q, want the winner's %q", row.reason.String, winnerReason)
	}
	if len(h.audit.events) != 1 {
		t.Fatalf("audit events = %d, want one decision", len(h.audit.events))
	}
}

// probeAttributionAudit records the event through the transaction carried by
// the context: the probe row only survives when the decision commits.
type probeAttributionAudit struct {
	table string
	fail  bool
}

func (a *probeAttributionAudit) RecordAttributionModeration(ctx context.Context, event application.AttributionModerationEvent) error {
	tx, ok := platformpg.TxFromContext(ctx)
	if !ok {
		return errors.New("audit recorder must join the decision transaction")
	}
	if _, err := tx.Exec(ctx, "INSERT INTO "+a.table+" (attribution_id, action, reason) VALUES ($1, $2, $3)",
		event.AttributionID.String(), event.Action.String(), event.Reason.String()); err != nil {
		return err
	}
	if a.fail {
		return errors.New("audit trail unavailable")
	}
	return nil
}

// TestAttributionModerationAuditIsAtomic proves the decision and its audit
// record commit or roll back together: a failing recorder leaves the
// attribution valid and no audit residue.
func TestAttributionModerationAuditIsAtomic(t *testing.T) {
	ctx := context.Background()
	h := newAttributionModerationHarness(t)

	const probeTable = "app.attribution_audit_probe"
	if _, err := h.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS `+probeTable+` (
		attribution_id uuid, action text, reason text)`); err != nil {
		t.Fatalf("create probe table: %v", err)
	}

	changeAt := time.Now().UTC().Add(-time.Minute)
	change := mustPersuasionChange(t, ctx, h.pool, h.arena, h.attributor, changeAt)
	argument := mustPersuasionArgument(t, ctx, h.pool, h.arena, h.author, "Argumento para auditoria atômica", "published", changeAt.Add(-time.Hour))
	attribution := h.seedAttribution(t, ctx, change, argument)

	txManager := platformpg.NewTxManager(h.pool)
	clock := clockseed.NewClock()

	// A failing recorder rolls the decision back: the attribution stays valid
	// and the probe write disappears with the transaction.
	failing := application.NewInvalidateAttributionUseCase(h.repo, h.authorizer, &probeAttributionAudit{table: probeTable, fail: true}, clock, txManager)
	if _, err := failing.Execute(ctx, h.command(attribution, "decisão que falha na auditoria")); err == nil {
		t.Fatal("Execute() error = nil, want the recorder failure")
	}
	rolledBack := readModerationRow(t, ctx, h.pool, attribution)
	if rolledBack.status != "valid" || rolledBack.reason.Valid {
		t.Fatalf("rolled back row = %+v, want valid without decision", rolledBack)
	}
	var probeRows int
	if err := h.pool.QueryRow(ctx, "SELECT count(*) FROM "+probeTable).Scan(&probeRows); err != nil {
		t.Fatalf("count probe rows: %v", err)
	}
	if probeRows != 0 {
		t.Fatalf("probe rows = %d, want none after the rollback", probeRows)
	}

	// The same recorder succeeding commits both writes together.
	succeeding := application.NewInvalidateAttributionUseCase(h.repo, h.authorizer, &probeAttributionAudit{table: probeTable}, clock, txManager)
	result, err := succeeding.Execute(ctx, h.command(attribution, "decisão auditada com sucesso"))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Replayed || result.Attribution.Status != domain.AttributionStatusInvalid {
		t.Fatalf("result = %+v, want a fresh invalidation", result)
	}
	committed := readModerationRow(t, ctx, h.pool, attribution)
	if committed.status != "invalid" || committed.reason.String != "decisão auditada com sucesso" {
		t.Fatalf("committed row = %+v", committed)
	}
	if err := h.pool.QueryRow(ctx, "SELECT count(*) FROM "+probeTable).Scan(&probeRows); err != nil {
		t.Fatalf("count probe rows: %v", err)
	}
	if probeRows != 1 {
		t.Fatalf("probe rows = %d, want the committed audit record", probeRows)
	}

	// A missing target never reaches the recorder.
	unknown := pgtype.UUID{Bytes: [16]byte{0xde, 0xad, 0xbe, 0xef, 0x91, 0x92, 0x93, 0x94, 0x95, 0x96, 0x97, 0x98, 0x99, 0x9a, 0x9b, 0x9d}, Valid: true}
	if _, err := succeeding.Execute(ctx, h.command(unknown, "alvo inexistente")); !errors.Is(err, application.ErrAttributionNotFound) {
		t.Fatalf("unknown attribution error = %v, want ErrAttributionNotFound", err)
	}
	if err := h.pool.QueryRow(ctx, "SELECT count(*) FROM "+probeTable).Scan(&probeRows); err != nil {
		t.Fatalf("count probe rows: %v", err)
	}
	if probeRows != 1 {
		t.Fatalf("probe rows = %d, want no audit record for a missing target", probeRows)
	}
}

// TestAttributionModerationReconstruction rebuilds the validity projection
// from the retained rows and proves that the moderation read model reflects
// validity: invalidated attributions leave the valid totals, restored ones
// return, and no link is ever rewritten or deleted (BR §6, §11 inv. 9-10).
func TestAttributionModerationReconstruction(t *testing.T) {
	ctx := context.Background()
	h := newAttributionModerationHarness(t)

	changeAt := time.Now().UTC().Add(-time.Minute)
	firstChange := mustPersuasionChange(t, ctx, h.pool, h.arena, h.attributor, changeAt)
	secondChange := mustPersuasionChangeAt(t, ctx, h.pool, h.arena, h.attributor, "disagree", "undecided", 3, changeAt.Add(time.Minute))
	firstArgument := mustPersuasionArgument(t, ctx, h.pool, h.arena, h.author, "Argumento reconstruído um", "published", changeAt.Add(-time.Hour))
	secondArgument := mustPersuasionArgument(t, ctx, h.pool, h.arena, h.author, "Argumento reconstruído dois", "published", changeAt.Add(-time.Hour))
	thirdArgument := mustPersuasionArgument(t, ctx, h.pool, h.arena, h.author, "Argumento reconstruído três", "published", changeAt.Add(-time.Hour))

	firstAttribution := h.seedAttribution(t, ctx, firstChange, firstArgument)
	secondAttribution := h.seedAttribution(t, ctx, firstChange, secondArgument)
	thirdAttribution := h.seedAttribution(t, ctx, secondChange, thirdArgument)
	firstBefore := readModerationRow(t, ctx, h.pool, firstAttribution)
	thirdBefore := readModerationRow(t, ctx, h.pool, thirdAttribution)

	// Invalidate two attributions, restore one of them and invalidate it
	// again: the cycles must stay reconstructible from the retained rows.
	if _, err := h.invalidate.Execute(ctx, h.command(firstAttribution, "primeira invalidação do argumento um")); err != nil {
		t.Fatalf("invalidate first error = %v", err)
	}
	if _, err := h.invalidate.Execute(ctx, h.command(thirdAttribution, "primeira invalidação do argumento três")); err != nil {
		t.Fatalf("invalidate third error = %v", err)
	}
	if _, err := h.restore.Execute(ctx, h.command(thirdAttribution, "restauração do argumento três")); err != nil {
		t.Fatalf("restore third error = %v", err)
	}
	if _, err := h.invalidate.Execute(ctx, h.command(thirdAttribution, "segunda invalidação do argumento três")); err != nil {
		t.Fatalf("re-invalidate third error = %v", err)
	}

	// Source facts: the retained rows themselves.
	var rows int
	if err := h.pool.QueryRow(ctx, `
		SELECT count(*) FROM app.persuasion_attributions
		WHERE position_change_id IN ($1, $2)`, firstChange, secondChange).Scan(&rows); err != nil {
		t.Fatalf("count retained attributions: %v", err)
	}
	if rows != 3 {
		t.Fatalf("retained attributions = %d, want all three (nothing deleted)", rows)
	}

	sourceValid, err := validCountsByArgument(ctx, h.pool, firstArgument, secondArgument, thirdArgument)
	if err != nil {
		t.Fatalf("source counts: %v", err)
	}
	if sourceValid[uuidText(firstArgument)] != 0 || sourceValid[uuidText(secondArgument)] != 1 || sourceValid[uuidText(thirdArgument)] != 0 {
		t.Fatalf("source valid counts = %v, want only the untouched argument", sourceValid)
	}

	// The moderation read model projects the same validity as the retained
	// rows, including the newest decision of each cycle.
	projected, err := projectedValidCounts(ctx, h.repo, firstAttribution, secondAttribution, thirdAttribution)
	if err != nil {
		t.Fatalf("projected counts: %v", err)
	}
	if !equalCounts(projected, map[string]int{
		uuidText(firstArgument):  0,
		uuidText(secondArgument): 1,
		uuidText(thirdArgument):  0,
	}) {
		t.Fatalf("projected counts = %v, want the source facts", projected)
	}

	first := readModerationRow(t, ctx, h.pool, firstAttribution)
	if first.status != "invalid" || first.reason.String != "primeira invalidação do argumento um" {
		t.Fatalf("first projection = %+v", first)
	}
	third := readModerationRow(t, ctx, h.pool, thirdAttribution)
	if third.status != "invalid" || third.reason.String != "segunda invalidação do argumento três" {
		t.Fatalf("third projection = %+v, want the newest decision", third)
	}
	if !third.moderatedAt.Time.After(thirdBefore.moderatedAt.Time) {
		t.Fatalf("newest decision instant = %s, want it after the previous one", third.moderatedAt.Time)
	}
	if second := readModerationRow(t, ctx, h.pool, secondAttribution); second.status != "valid" || second.reason.Valid {
		t.Fatalf("untouched attribution = %+v, want valid without decision", second)
	}
	for _, probe := range []struct {
		name   string
		before moderationRow
		after  moderationRow
	}{
		{name: "first", before: firstBefore, after: first},
		{name: "third", before: thirdBefore, after: third},
	} {
		if probe.after.positionChangeID != probe.before.positionChangeID ||
			probe.after.attributorID != probe.before.attributorID ||
			probe.after.argumentID != probe.before.argumentID ||
			!probe.after.createdAt.Time.Equal(probe.before.createdAt.Time) {
			t.Fatalf("%s attribution links changed across moderation cycles", probe.name)
		}
	}

	// Reconstruction from the source query matches the projection once the
	// invalidated attribution is restored.
	if _, err := h.restore.Execute(ctx, h.command(firstAttribution, "recurso aceito no argumento um")); err != nil {
		t.Fatalf("restore first error = %v", err)
	}
	projected, err = projectedValidCounts(ctx, h.repo, firstAttribution, secondAttribution, thirdAttribution)
	if err != nil {
		t.Fatalf("projected counts after restore: %v", err)
	}
	sourceValid, err = validCountsByArgument(ctx, h.pool, firstArgument, secondArgument, thirdArgument)
	if err != nil {
		t.Fatalf("source counts after restore: %v", err)
	}
	rebuilt := map[string]int{
		uuidText(firstArgument):  sourceValid[uuidText(firstArgument)],
		uuidText(secondArgument): sourceValid[uuidText(secondArgument)],
		uuidText(thirdArgument):  sourceValid[uuidText(thirdArgument)],
	}
	if !equalCounts(projected, rebuilt) {
		t.Fatalf("projection = %v, source = %v, want them equal", projected, rebuilt)
	}
	if rebuilt[uuidText(firstArgument)] != 1 || rebuilt[uuidText(secondArgument)] != 1 || rebuilt[uuidText(thirdArgument)] != 0 {
		t.Fatalf("rebuilt valid counts = %v, want the restored argument back and three still invalid", rebuilt)
	}
}

// TestAttributionModerationKeepsTheRecordingSlot proves the recording
// projection keeps counting invalidated attributions: invalidation excludes
// an attribution from valid metrics but never frees a slot of the change, so
// the three-argument limit cannot be bypassed through moderation. A retry of
// the invalidated selection still replays the recorded set.
func TestAttributionModerationKeepsTheRecordingSlot(t *testing.T) {
	ctx := context.Background()
	h := newAttributionModerationHarness(t)

	changeAt := time.Now().UTC().Add(-time.Minute)
	change := mustPersuasionChange(t, ctx, h.pool, h.arena, h.attributor, changeAt)
	first := mustPersuasionArgument(t, ctx, h.pool, h.arena, h.author, "Slot um", "published", changeAt.Add(-time.Hour))
	second := mustPersuasionArgument(t, ctx, h.pool, h.arena, h.author, "Slot dois", "published", changeAt.Add(-time.Hour))
	third := mustPersuasionArgument(t, ctx, h.pool, h.arena, h.author, "Slot três", "published", changeAt.Add(-time.Hour))
	fourth := mustPersuasionArgument(t, ctx, h.pool, h.arena, h.author, "Slot quatro", "published", changeAt.Add(-time.Hour))

	if _, err := h.record.Execute(ctx, recordAttributionsCommand(h.attributor, change, first, second, third)); err != nil {
		t.Fatalf("record three attributions: %v", err)
	}
	var attribution pgtype.UUID
	if err := h.pool.QueryRow(ctx, `
		SELECT id FROM app.persuasion_attributions
		WHERE position_change_id = $1 AND argument_id = $2`, change, first).Scan(&attribution); err != nil {
		t.Fatalf("read seeded attribution: %v", err)
	}
	if _, err := h.invalidate.Execute(ctx, h.command(attribution, "atribuição invalidada para o teste de limite")); err != nil {
		t.Fatalf("invalidate error = %v", err)
	}

	// The invalidated argument still occupies its slot: a fourth argument is
	// refused exactly as before the invalidation.
	if _, err := h.record.Execute(ctx, recordAttributionsCommand(h.attributor, change, fourth)); !errors.Is(err, domain.ErrTooManyAttributions) {
		t.Fatalf("fourth argument error = %v, want ErrTooManyAttributions", err)
	}
	if count := attributionRowCount(t, ctx, h.pool, change); count != 3 {
		t.Fatalf("attributions = %d, want the three retained", count)
	}

	// Retrying the invalidated selection replays the recorded set.
	replayed, err := h.record.Execute(ctx, recordAttributionsCommand(h.attributor, change, first))
	if err != nil {
		t.Fatalf("retry of the invalidated selection error = %v", err)
	}
	if !replayed.Replayed || len(replayed.ArgumentIDs) != 3 {
		t.Fatalf("retry = %+v, want the recorded three replayed", replayed)
	}
}

// validCountsByArgument recomputes the valid projection directly from the
// retained rows: the source query of the reconstruction.
func validCountsByArgument(ctx context.Context, pool *pgxpool.Pool, argumentIDs ...pgtype.UUID) (map[string]int, error) {
	rows, err := pool.Query(ctx, `
		SELECT argument_id, count(*) FILTER (WHERE status = 'valid') AS valid_count
		FROM app.persuasion_attributions
		WHERE argument_id = ANY($1::uuid[])
		GROUP BY argument_id`, argumentIDs)
	if err != nil {
		return nil, fmt.Errorf("count valid attributions: %w", err)
	}
	defer rows.Close()

	counts := map[string]int{}
	for rows.Next() {
		var argumentID pgtype.UUID
		var count int
		if err := rows.Scan(&argumentID, &count); err != nil {
			return nil, fmt.Errorf("scan valid count: %w", err)
		}
		counts[uuidText(argumentID)] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate valid counts: %w", err)
	}
	return counts, nil
}

// projectedValidCounts derives the same counts through the production
// moderation read model.
func projectedValidCounts(ctx context.Context, repo *postgres.Repository, attributionIDs ...pgtype.UUID) (map[string]int, error) {
	counts := map[string]int{}
	for _, attributionID := range attributionIDs {
		parsed, err := domain.ParseAttributionID(uuidText(attributionID))
		if err != nil {
			return nil, fmt.Errorf("parse attribution id: %w", err)
		}
		attribution, err := repo.LockAttributionForModeration(ctx, parsed)
		if err != nil {
			return nil, fmt.Errorf("project attribution: %w", err)
		}
		key := attribution.ArgumentID.String()
		if _, ok := counts[key]; !ok {
			counts[key] = 0
		}
		if attribution.IsValid() {
			counts[key]++
		}
	}
	return counts, nil
}

func equalCounts(left, right map[string]int) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}
