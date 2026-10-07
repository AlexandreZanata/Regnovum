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
