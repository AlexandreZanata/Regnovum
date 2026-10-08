/**
 * Tests of the commerce staged client (P57-T03) against a fake transport.
 *
 * The two trade escrow reads speak the paths their fragment declares,
 * and nothing else: this client holds reads only — no transfer,
 * funding, release or refund endpoint exists in the fragment, so none
 * is called here. Every call leaves with the account no-store policy
 * the core already owns for `/api/v1/me/*`, and a cancelled read
 * aborts instead of rendering late.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createCommerceClient } from "../../src/core/clients/commerce.js";
import {
  captureApiError,
  createTestContext,
  hangingResponse,
  headerOf,
  jsonResponse,
  problemResponse,
} from "../support/harness.js";

const RECEIPT = {
  contract_id: "00000000-0000-4000-8000-000000000002",
  contract_key: "contrato-1",
  escrow_transfer_id: "escrow-1",
  expires_at: "2026-11-05T00:00:00Z",
  gross_milli: 20000,
  net_milli: 18000,
  object: "serviço com recibo",
  posted_at: "2026-10-05T10:00:00Z",
  refunded_milli: 0,
  refunds: [],
  role: "buyer",
  status: "released",
  tithe_milli: 2000,
  title: "Recibo de comércio",
} as const;

const STATEMENT = {
  entries: [
    {
      contract_id: "00000000-0000-4000-8000-000000000002",
      contract_key: "contrato-1",
      gross_milli: 20000,
      posted_at: "2026-10-05T10:00:00Z",
      role: "buyer",
      status: "released",
    },
  ],
  title: "Extrato de comércio",
} as const;

test("one receipt reads the owned contract with its split", async () => {
  const context = createTestContext({ responder: () => jsonResponse(RECEIPT) });

  const receipt = await createCommerceClient(context.core).readTradeReceipt(
    "00000000-0000-4000-8000-000000000002",
  );

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(
    context.lastCall().url,
    "https://arena.test/api/v1/me/commerce/contracts/00000000-0000-4000-8000-000000000002",
  );
  assert.equal(receipt.gross_milli, 20000);
  assert.equal(receipt.tithe_milli, 2000);
  assert.equal(receipt.net_milli, 18000);
  assert.equal(context.lastCall().init.cache, "no-store");
  assert.equal(context.lastCall().init.body, undefined);
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
});

test("the statement reads the owned lines newest first", async () => {
  const context = createTestContext({ responder: () => jsonResponse(STATEMENT) });

  const statement = await createCommerceClient(context.core).readTradeStatement();

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/commerce/statement");
  assert.equal(statement.entries.length, 1);
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("the client holds reads only: no mutation method exists", async () => {
  const context = createTestContext({ responder: () => jsonResponse(RECEIPT) });
  const client = createCommerceClient(context.core);

  assert.deepEqual(Object.keys(client).sort(), ["readTradeReceipt", "readTradeStatement"]);
  for (const call of context.calls) {
    assert.equal(call.init.method, "GET", "reads only: no POST was ever assembled");
  }
});

test("anonymous and foreign contracts fail distinctly", async () => {
  for (const [status, code, kind] of [
    [401, "unauthorized", "unauthorized"],
    [404, "contract_unknown", "not_found"],
  ] as const) {
    const context = createTestContext({ responder: () => problemResponse(status, code) });
    const failure = await captureApiError(() =>
      createCommerceClient(context.core).readTradeReceipt("00000000-0000-4000-8000-000000000002"),
    );
    assert.equal(failure.code, code);
    assert.equal(failure.kind, kind);
    assert.equal(context.calls.length, 1, `${code}: no automatic second attempt`);
  }
});

test("a cancelled statement read aborts instead of rendering late", async () => {
  const controller = new AbortController();
  const context = createTestContext({ responder: (call) => hangingResponse(call) });

  const pending = captureApiError(() => createCommerceClient(context.core).readTradeStatement(controller.signal));
  controller.abort();
  const failure = await pending;

  assert.equal(failure.code, "aborted");
  assert.equal(failure.aborted, true, "an aborted answer is discarded, never rendered");
  assert.equal(failure.retryable, false);
  assert.equal(context.calls.length, 1);
});
