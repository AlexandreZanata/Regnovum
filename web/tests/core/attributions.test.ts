/**
 * Tests of the attributions client (P53-T05) against a fake transport:
 * the recording posts exactly the eligible identifiers the page
 * offered — never an invented one — as a single-shot call with no
 * idempotency key, and the public counts read through the HTTP cache
 * carrying counts only. A lost answer is re-read, never replayed
 * blind; a foreign change stays indistinguishable from a missing
 * one.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createAttributionsClient } from "../../src/core/clients/attributions.js";
import {
  bodyOf,
  captureApiError,
  createTestContext,
  headerOf,
  jsonResponse,
  problemResponse,
} from "../support/harness.js";

const METRICS = {
  valid_attributions: 1,
  distinct_people: 1,
  checked_at: "2026-10-03T10:00:00Z",
} as const;

test("the recording posts the eligible identifiers exactly once", async () => {
  const context = createTestContext({
    responder: () => jsonResponse({ argument_ids: ["arg-1"], replayed: false }, 201),
  });

  const answer = await createAttributionsClient(context.core).record("change-1", { argument_ids: ["arg-1"] });

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/position-changes/change-1/attributions");
  assert.deepEqual(bodyOf(context.lastCall()), { argument_ids: ["arg-1"] });
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.deepEqual(answer.argument_ids, ["arg-1"]);
  assert.equal(answer.replayed, false);
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("an empty selection is a valid skip, never a fake attribution", async () => {
  const context = createTestContext({
    responder: () => jsonResponse({ argument_ids: [], replayed: false }, 201),
  });

  const answer = await createAttributionsClient(context.core).record("change-1", { argument_ids: [] });

  assert.deepEqual(bodyOf(context.lastCall()), { argument_ids: [] });
  assert.deepEqual(answer.argument_ids, []);
});

test("a repeated selection resolves the recorded set without writing again", async () => {
  const context = createTestContext({
    responder: () => jsonResponse({ argument_ids: ["arg-1"], replayed: true }),
  });

  const answer = await createAttributionsClient(context.core).record("change-1", { argument_ids: ["arg-1"] });

  assert.equal(answer.replayed, true);
  assert.equal(context.calls.length, 1, "the client never replays a recording by itself");
});

test("a foreign change stays indistinguishable from a missing one", async () => {
  const context = createTestContext({ responder: () => problemResponse(404, "change_not_found") });

  const failure = await captureApiError(() =>
    createAttributionsClient(context.core).record("someone-elses-change", { argument_ids: ["arg-1"] }),
  );

  assert.equal(failure.code, "change_not_found");
  assert.equal(failure.kind, "not_found");
  assert.equal(context.calls.length, 1);
});

test("crediting your own argument is refused without a write", async () => {
  const context = createTestContext({ responder: () => problemResponse(400, "persuasion_self_attribution") });

  const failure = await captureApiError(() =>
    createAttributionsClient(context.core).record("change-1", { argument_ids: ["mine"] }),
  );

  assert.equal(failure.code, "persuasion_self_attribution");
  assert.equal(failure.kind, "validation");
  assert.equal(failure.retryable, false);
  assert.equal(context.calls.length, 1);
});

test("crediting twice and crossing arenas are refused without a write", async () => {
  for (const code of ["persuasion_duplicate_attribution", "persuasion_cross_arena_argument"] as const) {
    const context = createTestContext({ responder: () => problemResponse(400, code) });
    const failure = await captureApiError(() =>
      createAttributionsClient(context.core).record("change-1", { argument_ids: ["arg-9"] }),
    );
    assert.equal(failure.code, code);
    assert.equal(failure.kind, "validation");
    assert.equal(context.calls.length, 1, `${code}: no replay of a refused recording`);
  }
});

test("the public counts read through the HTTP cache carrying counts only", async () => {
  const context = createTestContext({ responder: () => jsonResponse(METRICS) });

  const metrics = await createAttributionsClient(context.core).counts("arg-1");

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/arguments/arg-1/attributions");
  assert.equal(context.lastCall().init.cache, "default");
  assert.equal(metrics.valid_attributions, 1);
  assert.equal(metrics.distinct_people, 1);
});

test("the counts carry no identity to render", async () => {
  const context = createTestContext({ responder: () => jsonResponse(METRICS) });

  const metrics = await createAttributionsClient(context.core).counts("arg-1");

  const keys = Object.keys(metrics).sort();
  assert.deepEqual(keys, ["checked_at", "distinct_people", "valid_attributions"]);
});

test("counts of an unknown argument read as not found", async () => {
  const context = createTestContext({ responder: () => problemResponse(404, "argument_not_found") });

  const failure = await captureApiError(() => createAttributionsClient(context.core).counts("gone"));

  assert.equal(failure.code, "argument_not_found");
  assert.equal(failure.kind, "not_found");
});
