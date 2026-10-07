/**
 * Tests of the moderation filing client (P55-T01) against a fake
 * transport: the report carries the closed reason vocabulary
 * with optional bounded context, and the appeal carries the
 * action with its context. Both are single-shot calls with no
 * idempotency key — the contract declares none — and the server
 * itself resolves a repeated filing by state. A lost answer is
 * re-read, never replayed blind; restricted evidence never
 * serializes back.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createModerationClient } from "../../src/core/clients/moderation.js";
import type { HttpCore } from "../../src/core/http.js";
import {
  bodyOf,
  captureApiError,
  createTestContext,
  headerOf,
  jsonResponse,
  problemResponse,
} from "../support/harness.js";

const REPORT = {
  report_id: "rep-1",
  replayed: false,
  rate_limited: false,
  reports_in_window: 1,
} as const;

const APPEAL = { appeal_id: "apl-1", action_id: "act-1", replayed: false } as const;

test("the report posts the closed vocabulary exactly once", async () => {
  const context = createTestContext({ responder: () => jsonResponse(REPORT) });

  const answer = await createModerationClient(context.core).report({
    target_type: "argument",
    target_id: "arg-1",
    reason: "harassment",
    context: "bounded context",
  });

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/moderation/reports");
  assert.deepEqual(bodyOf(context.lastCall()), {
    target_type: "argument",
    target_id: "arg-1",
    reason: "harassment",
    context: "bounded context",
  });
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(answer.report_id, "rep-1");
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("the appeal contests one action with its context", async () => {
  const context = createTestContext({ responder: () => jsonResponse(APPEAL) });

  const answer = await createModerationClient(context.core).appeal({ action_id: "act-1", context: "my case" });

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/moderation/appeals");
  assert.deepEqual(bodyOf(context.lastCall()), { action_id: "act-1", context: "my case" });
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(answer.appeal_id, "apl-1");
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("a repeated filing resolves the recorded identity without writing again", async () => {
  const context = createTestContext({ responder: () => jsonResponse({ ...REPORT, replayed: true }) });

  const answer = await createModerationClient(context.core).report({
    target_type: "argument",
    target_id: "arg-1",
    reason: "spam",
  });

  assert.equal(answer.replayed, true);
  assert.equal(context.calls.length, 1, "the client never replays a filing by itself");
});

test("a missing target and another account's sanction fail distinctly", async () => {
  for (const [status, code, kind] of [
    [404, "not_found", "not_found"],
    [403, "forbidden", "forbidden"],
    [409, "conflict", "conflict"],
  ] as const) {
    const context = createTestContext({ responder: () => problemResponse(status, code) });
    const failure = await captureApiError(() =>
      createModerationClient(context.core).report({ target_type: "arena", target_id: "gone", reason: "spam" }),
    );
    assert.equal(failure.code, code);
    assert.equal(failure.kind, kind);
    assert.equal(context.calls.length, 1, `${code}: no replay of a refused filing`);
  }
});

test("a stranger files as unauthorized and an invalid reason as validation", async () => {
  const anonymous = createTestContext({ responder: () => problemResponse(401, "unauthorized") });
  const anonymousFailure = await captureApiError(() =>
    createModerationClient(anonymous.core).appeal({ action_id: "act-1", context: "mine" }),
  );
  assert.equal(anonymousFailure.code, "unauthorized");
  assert.equal(anonymousFailure.kind, "unauthorized");

  const invalid = createTestContext({ responder: () => problemResponse(400, "invalid_request") });
  const invalidFailure = await captureApiError(() =>
    createModerationClient(invalid.core).appeal({ action_id: "act-1", context: "mine" }),
  );
  assert.equal(invalidFailure.code, "invalid_request");
  assert.equal(invalidFailure.kind, "validation");
});

const QUEUE_PAGE = {
  items: [
    {
      case_id: "case-1",
      target_type: "argument",
      target_id: "arg-1",
      status: "open",
      priority: "high",
      created_at: "2026-10-05T10:00:00Z",
    },
  ],
  next_cursor: "opaque-next",
} as const;

const CLAIM = { case_id: "case-1", status: "under_review", claimed_by: "mod-1" } as const;

const DECISION = { action_id: "act-9", case_id: "case-1", action: "warning" } as const;

const SIGNALS = {
  author_id: "author-1",
  policy_version: "v3",
  window_seconds: 604800,
  checked_at: "2026-10-05T10:00:00Z",
  signals: [{ kind: "reciprocity", counterpart_id: "author-2", mutual_events: 12 }],
} as const;

test("the queue reads one routing page under the private cache policy", async () => {
  const context = createTestContext({ responder: () => jsonResponse(QUEUE_PAGE) });

  const page = await createModerationClient(context.core).queue({ status: "open", limit: 20 });

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/moderation/cases?status=open&limit=20");
  assert.equal(page.items.length, 1);
  assert.equal(page.next_cursor, "opaque-next");
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("an owner without a role reads the queue as forbidden", async () => {
  const context = createTestContext({ responder: () => problemResponse(403, "forbidden") });

  const failure = await captureApiError(() => createModerationClient(context.core).queue());

  assert.equal(failure.code, "forbidden");
  assert.equal(failure.kind, "forbidden");
  assert.equal(context.calls.length, 1);
});

test("a moderator without step-up claims as unauthorized", async () => {
  const context = createTestContext({ responder: () => problemResponse(401, "step_up_required") });

  const failure = await captureApiError(() => createModerationClient(context.core).claim("case-1"));

  assert.equal(failure.code, "step_up_required");
  assert.equal(failure.kind, "unauthorized");
  assert.equal(context.calls.length, 1);
});

test("the claim takes the case under a server-owned lease", async () => {
  const context = createTestContext({ responder: () => jsonResponse(CLAIM) });

  const claim = await createModerationClient(context.core).claim("case-1");

  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/moderation/cases/case-1/claim");
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(claim.claimed_by, "mod-1");
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("a held lease and a second decision conflict without a false success", async () => {
  for (const run of [
    (core: HttpCore) => createModerationClient(core).claim("case-1"),
    (core: HttpCore) =>
      createModerationClient(core).decide("case-1", { action: "warning", rule: "R1", justification: "why" }),
  ]) {
    const context = createTestContext({ responder: () => problemResponse(409, "conflict") });
    const failure = await captureApiError(() => run(context.core));
    assert.equal(failure.code, "conflict");
    assert.equal(failure.kind, "conflict");
    assert.equal(context.calls.length, 1);
  }
});

test("the decision records the untouched measure without its justification", async () => {
  const context = createTestContext({ responder: () => jsonResponse(DECISION) });

  const decision = await createModerationClient(context.core).decide("case-1", {
    action: "warning",
    rule: "R1",
    justification: "why",
  });

  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/moderation/cases/case-1/decisions");
  assert.deepEqual(bodyOf(context.lastCall()), { action: "warning", rule: "R1", justification: "why" });
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(decision.action_id, "act-9");
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("the signals read prevention data with counts and no score", async () => {
  const context = createTestContext({ responder: () => jsonResponse(SIGNALS) });

  const assessment = await createModerationClient(context.core).signals("author-1");

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/moderation/attribution-signals/author-1");
  assert.equal(assessment.signals.length, 1);
  assert.equal(context.lastCall().init.cache, "no-store");
  const serialized = JSON.stringify(assessment).toLowerCase();
  for (const marker of ["score", "severity", "weight"]) {
    assert.ok(!serialized.includes(marker), `automated marker leaked: ${marker}`);
  }
});
