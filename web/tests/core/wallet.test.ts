/**
 * Tests of the wallet client (P54-T01) against a fake transport:
 * the balance reads the derived buckets in integer INK units and
 * the statement reads one opaque cursor page — newest first — with
 * the reference verbatim and no receipt URL to follow. Both are
 * safe reads under the account no-store policy; a lost answer is
 * re-read, never replayed blind, and a stranger reads as
 * unauthorized.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createWalletClient } from "../../src/core/clients/wallet.js";
import {
  captureApiError,
  createTestContext,
  headerOf,
  jsonResponse,
  problemResponse,
} from "../support/harness.js";

const BALANCE = { balance_free: 3800, balance_purchased: 300 } as const;

const PAGE = {
  items: [
    {
      transaction_id: "tx-1",
      operation_id: "op-1",
      operation_type: "debit_argument",
      bucket: "FREE_INK",
      amount: -100,
      reference: "arena:arg-1",
      created_at: "2026-10-01T10:00:00Z",
    },
  ],
  next_cursor: "opaque-next",
} as const;

test("the balance reads the derived buckets as integers", async () => {
  const context = createTestContext({ responder: () => jsonResponse(BALANCE) });

  const balance = await createWalletClient(context.core).balance();

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/wallet");
  assert.equal(balance.balance_free, 3800);
  assert.equal(balance.balance_purchased, 300);
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("the statement reads one opaque cursor page with its reference", async () => {
  const context = createTestContext({ responder: () => jsonResponse(PAGE) });

  const page = await createWalletClient(context.core).statement({ cursor: "opaque-cursor", limit: 20 });

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(
    context.lastCall().url,
    "https://arena.test/api/v1/me/wallet/transactions?cursor=opaque-cursor&limit=20",
  );
  assert.equal(page.items.length, 1);
  assert.equal(page.items[0]?.reference, "arena:arg-1");
  assert.equal(page.next_cursor, "opaque-next");
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("the statement without a query reads the default page", async () => {
  const context = createTestContext({ responder: () => jsonResponse({ items: [], next_cursor: null }) });

  const page = await createWalletClient(context.core).statement();

  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/wallet/transactions");
  assert.deepEqual(page.items, []);
  assert.equal(page.next_cursor, null);
});

test("a stranger reads as unauthorized without a balance", async () => {
  const context = createTestContext({ responder: () => problemResponse(401, "unauthorized") });

  const failure = await captureApiError(() => createWalletClient(context.core).balance());

  assert.equal(failure.code, "unauthorized");
  assert.equal(failure.kind, "unauthorized");
  assert.equal(context.calls.length, 1);
});

test("a forged cursor and a bad page size fail without a balance", async () => {
  for (const [status, code] of [
    [400, "invalid_cursor"],
    [400, "invalid_limit"],
  ] as const) {
    const context = createTestContext({ responder: () => problemResponse(status, code) });
    const failure = await captureApiError(() => createWalletClient(context.core).statement({ cursor: "forged" }));
    assert.equal(failure.code, code);
    assert.equal(failure.kind, "validation");
    assert.equal(context.calls.length, 1, `${code}: no replay of a refused read`);
  }
});

test("reads carry no idempotency key and invent no legacy split", async () => {
  const context = createTestContext({ responder: () => jsonResponse(BALANCE) });

  await createWalletClient(context.core).balance();

  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.ok(!context.lastCall().url.includes("legacy"), "no legacy split travels");
  assert.ok(!context.lastCall().url.includes("season"), "no seasonal split travels");
});
