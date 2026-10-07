/**
 * Tests of the positions client (P53-T01) against a fake transport:
 * the public aggregate reads through the HTTP cache, the private
 * projection and its history stay under the account no-store policy,
 * and both transitions are single-shot calls — never retried, never
 * carrying an idempotency key. A lost answer is a failure the person
 * answers deliberately, never a replay the core invents. The private
 * history of one account is unreachable from any other, and a
 * visitor holds no projection at all.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createPositionsClient } from "../../src/core/clients/positions.js";
import {
  bodyOf,
  captureApiError,
  createTestContext,
  headerOf,
  jsonResponse,
  problemResponse,
} from "../support/harness.js";

const MINE = {
  arena_id: "018f6b2a-0000-7000-8000-000000000001",
  initial_position: "agree",
  current_position: "disagree",
  version: 2,
  created_at: "2026-10-02T10:00:00Z",
  updated_at: "2026-10-02T12:00:00Z",
} as const;

const AGGREGATE = {
  participants_total: 1234,
  suppressed: false,
  initial: { agree: 8, disagree: 6, undecided: 3 },
  current: { agree: 10, disagree: 5, undecided: 2 },
  checked_at: "2026-10-02T13:00:00Z",
} as const;

test("the aggregate speaks the public contract path through the HTTP cache", async () => {
  const context = createTestContext({ responder: () => jsonResponse(AGGREGATE) });

  const aggregate = await createPositionsClient(context.core).aggregate("018f6b2a-0000-7000-8000-000000000001");

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(
    context.lastCall().url,
    "https://arena.test/api/v1/arenas/018f6b2a-0000-7000-8000-000000000001/positions",
  );
  assert.equal(context.lastCall().init.cache, "default", "the public aggregate revalidates through the HTTP cache");
  assert.equal(aggregate.participants_total, 1234);
  assert.equal(aggregate.suppressed, false);
});

test("the private projection stays under the account no-store policy", async () => {
  const context = createTestContext({ responder: () => jsonResponse(MINE) });

  const mine = await createPositionsClient(context.core).mine("018f6b2a-0000-7000-8000-000000000001");

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(
    context.lastCall().url,
    "https://arena.test/api/v1/me/arenas/018f6b2a-0000-7000-8000-000000000001/position",
  );
  assert.equal(context.lastCall().init.cache, "no-store");
  assert.equal(mine.current_position, "disagree");
  assert.equal(mine.version, 2);
});

test("no confirmed position reads as not found, never as another account's", async () => {
  const context = createTestContext({ responder: () => problemResponse(404, "position_not_found") });

  const failure = await captureApiError(() => createPositionsClient(context.core).mine("arena-1"));

  assert.equal(failure.code, "position_not_found");
  assert.equal(failure.kind, "not_found");
});

test("a visitor holds no private projection", async () => {
  const context = createTestContext({ responder: () => problemResponse(401, "unauthorized") });

  const failure = await captureApiError(() => createPositionsClient(context.core).mine("arena-1"));

  assert.equal(failure.code, "unauthorized");
  assert.equal(failure.kind, "unauthorized");
  assert.equal(context.unauthorized.length, 1, "a 401 on a private read expires the session");
});

test("the first confirmation posts the contract body exactly once", async () => {
  const context = createTestContext({ responder: () => jsonResponse({ position: MINE, replayed: false }) });

  const answer = await createPositionsClient(context.core).confirm("arena-1", { position: "agree" });

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/arenas/arena-1/position");
  assert.deepEqual(bodyOf(context.lastCall()), { position: "agree" });
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(answer.replayed, false);
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("repeating the same value replays the recorded projection", async () => {
  const context = createTestContext({ responder: () => jsonResponse({ position: MINE, replayed: true }) });

  const answer = await createPositionsClient(context.core).confirm("arena-1", { position: "agree" });

  assert.equal(answer.replayed, true, "the replay resolves by state, without writing again");
  assert.equal(context.calls.length, 1);
});

test("a different first value conflicts as immutable history, never retried", async () => {
  const context = createTestContext({ responder: () => problemResponse(409, "initial_position_already_set") });

  const failure = await captureApiError(() => createPositionsClient(context.core).confirm("arena-1", { position: "disagree" }));

  assert.equal(failure.code, "initial_position_already_set");
  assert.equal(failure.kind, "conflict");
  assert.equal(failure.retryable, false);
  assert.equal(context.calls.length, 1, "immutable history must never replay");
});

test("a value outside the vocabulary fails as validation", async () => {
  const context = createTestContext({ responder: () => problemResponse(400, "position_invalid") });
  // The value arrives from outside the type system, as a tampered form would send it.
  const tampered = JSON.parse('{"position":"talvez"}') as { position: "agree" };

  const failure = await captureApiError(() => createPositionsClient(context.core).confirm("arena-1", tampered));

  assert.equal(failure.code, "position_invalid");
  assert.equal(failure.kind, "validation");
  assert.equal(context.calls.length, 1);
});

test("a closed arena accepts no first confirmation", async () => {
  const context = createTestContext({ responder: () => problemResponse(409, "arena_not_open") });

  const failure = await captureApiError(() => createPositionsClient(context.core).confirm("arena-1", { position: "agree" }));

  assert.equal(failure.code, "arena_not_open");
  assert.equal(failure.kind, "conflict");
  assert.equal(context.calls.length, 1);
});

test("a change posts the transitions path and answers the change identifier", async () => {
  const context = createTestContext({
    responder: () => jsonResponse({ change_id: "change-1", position: { ...MINE, current_position: "undecided", version: 3 } }, 201),
  });

  const answer = await createPositionsClient(context.core).change("arena-1", { position: "undecided" });

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/arenas/arena-1/position/changes");
  assert.deepEqual(bodyOf(context.lastCall()), { position: "undecided" });
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(answer.change_id, "change-1");
});

test("a concurrent change conflicts without a write and without a retry", async () => {
  const context = createTestContext({ responder: () => problemResponse(409, "version_conflict") });

  const failure = await captureApiError(() => createPositionsClient(context.core).change("arena-1", { position: "agree" }));

  assert.equal(failure.code, "version_conflict");
  assert.equal(failure.kind, "conflict");
  assert.equal(failure.retryable, false);
  assert.equal(context.calls.length, 1);
});

test("changing to the current position conflicts", async () => {
  const context = createTestContext({ responder: () => problemResponse(409, "position_same") });

  const failure = await captureApiError(() => createPositionsClient(context.core).change("arena-1", { position: "disagree" }));

  assert.equal(failure.code, "position_same");
  assert.equal(failure.kind, "conflict");
  assert.equal(context.calls.length, 1);
});

test("the private history reads newest first under no-store", async () => {
  const history = {
    items: [
      { change_id: "change-2", from_position: "disagree", to_position: "undecided", version: 3, changed_at: "2026-10-02T13:00:00Z" },
      { change_id: "change-1", from_position: "agree", to_position: "disagree", version: 2, changed_at: "2026-10-02T12:00:00Z" },
    ],
  };
  const context = createTestContext({ responder: () => jsonResponse(history) });

  const answer = await createPositionsClient(context.core).changes("arena-1");

  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/arenas/arena-1/position/changes");
  assert.equal(context.lastCall().init.cache, "no-store");
  assert.deepEqual(
    answer.items.map((item) => item.change_id),
    ["change-2", "change-1"],
  );
});
