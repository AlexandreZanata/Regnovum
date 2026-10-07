/**
 * Tests of the passes client (P54-T02) against a fake transport:
 * the summary reads the derived available total with its private
 * per-lot breakdown, and the history reads one opaque cursor page
 * — newest first — with the reference verbatim. Both are safe
 * reads under the account no-store policy; a reset is a re-read,
 * never a grant, and a stranger reads as unauthorized.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createPassesClient } from "../../src/core/clients/passes.js";
import {
  captureApiError,
  createTestContext,
  headerOf,
  jsonResponse,
  problemResponse,
} from "../support/harness.js";

const SUMMARY = {
  available_total: 2,
  checked_at: "2026-10-05T10:00:00Z",
  lots: [
    {
      origin: "MEMBER",
      quantity: 2,
      remaining: 2,
      expires_at: null,
      expired: false,
      created_at: "2026-10-01T10:00:00Z",
    },
  ],
} as const;

const PAGE = {
  items: [
    {
      consumption_id: "use-1",
      arena_id: "arena-1",
      origin: "MEMBER",
      reference: "arena:arena-1",
      consumed_at: "2026-10-04T10:00:00Z",
    },
  ],
  next_cursor: "opaque-next",
} as const;

test("the summary reads the derived total with its private lots", async () => {
  const context = createTestContext({ responder: () => jsonResponse(SUMMARY) });

  const summary = await createPassesClient(context.core).summary();

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/passes");
  assert.equal(summary.available_total, 2);
  assert.equal(summary.lots.length, 1);
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("the history reads one opaque cursor page with its reference", async () => {
  const context = createTestContext({ responder: () => jsonResponse(PAGE) });

  const page = await createPassesClient(context.core).history({ cursor: "opaque-cursor", limit: 20 });

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(
    context.lastCall().url,
    "https://arena.test/api/v1/me/passes/history?cursor=opaque-cursor&limit=20",
  );
  assert.equal(page.items.length, 1);
  assert.equal(page.items[0]?.reference, "arena:arena-1");
  assert.equal(page.next_cursor, "opaque-next");
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("the history without a query reads the default page", async () => {
  const context = createTestContext({ responder: () => jsonResponse({ items: [], next_cursor: null }) });

  const page = await createPassesClient(context.core).history();

  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/passes/history");
  assert.deepEqual(page.items, []);
  assert.equal(page.next_cursor, null);
});

test("a stranger reads as unauthorized without a summary", async () => {
  const context = createTestContext({ responder: () => problemResponse(401, "unauthorized") });

  const failure = await captureApiError(() => createPassesClient(context.core).summary());

  assert.equal(failure.code, "unauthorized");
  assert.equal(failure.kind, "unauthorized");
  assert.equal(context.calls.length, 1);
});

test("a forged cursor and a bad page size fail without a page", async () => {
  for (const [status, code] of [
    [400, "invalid_cursor"],
    [400, "invalid_limit"],
  ] as const) {
    const context = createTestContext({ responder: () => problemResponse(status, code) });
    const failure = await captureApiError(() => createPassesClient(context.core).history({ cursor: "forged" }));
    assert.equal(failure.code, code);
    assert.equal(failure.kind, "validation");
    assert.equal(context.calls.length, 1, `${code}: no replay of a refused read`);
  }
});

test("reads grant nothing and carry no idempotency key", async () => {
  const context = createTestContext({ responder: () => jsonResponse(SUMMARY) });

  await createPassesClient(context.core).summary();

  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(context.lastCall().init.method, "GET");
  assert.ok(!context.lastCall().url.includes("grant"), "no grant travels");
  assert.ok(!context.lastCall().url.includes("renew"), "no renewal travels");
});
