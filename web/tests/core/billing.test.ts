/**
 * Tests of the billing checkout client (P54-T03) against a fake
 * transport: the browser names only the minimal intent — market
 * and product — under one explicit idempotency key the core
 * replays across the single retry. Amount, currency, price and
 * return URLs resolve on the server; a repeated key resolves the
 * session already created instead of provisioning a second one.
 * No provider identifier ever serializes.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createBillingClient } from "../../src/core/clients/billing.js";
import {
  bodyOf,
  captureApiError,
  createTestContext,
  headerOf,
  jsonResponse,
  problemResponse,
} from "../support/harness.js";

const CHECKOUT = {
  intent_id: "intent-1",
  status: "open",
  redirect_url: "https://checkout.stripe.com/pay/cs_test_1",
  amount_minor: 990,
  currency: "BRL",
  market: "BR",
  product: "pass_1",
  replayed: false,
} as const;

test("the checkout posts the minimal intent under one explicit key", async () => {
  const context = createTestContext({ responder: () => jsonResponse(CHECKOUT) });

  const answer = await createBillingClient(context.core).checkout({
    market: "BR",
    product: "pass_1",
    idempotencyKey: "op-1",
  });

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/billing/checkout");
  assert.deepEqual(bodyOf(context.lastCall()), { market: "BR", product: "pass_1", idempotency_key: "op-1" });
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), "op-1");
  assert.equal(answer.amount_minor, 990);
  assert.equal(answer.currency, "BRL");
  assert.equal(answer.replayed, false);
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("the request carries no price and no destination", async () => {
  const context = createTestContext({ responder: () => jsonResponse(CHECKOUT) });

  await createBillingClient(context.core).checkout({ market: "BR", product: "pass_1", idempotencyKey: "op-2" });

  const body = bodyOf(context.lastCall()) as Record<string, unknown>;
  assert.deepEqual(Object.keys(body).sort(), ["idempotency_key", "market", "product"]);
  const serialized = JSON.stringify(body).toLowerCase();
  for (const marker of ["amount", "currency", "price", "redirect", "success", "cancel", "season"]) {
    assert.ok(!serialized.includes(marker), `server-resolved field leaked into the request: ${marker}`);
  }
});

test("a repeated key resolves the created session without a second charge", async () => {
  const context = createTestContext({
    responder: () => jsonResponse({ ...CHECKOUT, replayed: true }),
  });

  const answer = await createBillingClient(context.core).checkout({
    market: "BR",
    product: "pass_1",
    idempotencyKey: "op-same",
  });

  assert.equal(answer.replayed, true);
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), "op-same");
  assert.equal(context.calls.length, 1, "the client never invents a second purchase for one key");
});

test("an unknown product and an ineligible account fail without a session", async () => {
  for (const [status, code, kind] of [
    [400, "invalid_checkout", "validation"],
    [401, "unauthorized", "unauthorized"],
    [403, "not_eligible", "forbidden"],
  ] as const) {
    const context = createTestContext({ responder: () => problemResponse(status, code) });
    const failure = await captureApiError(() =>
      createBillingClient(context.core).checkout({ market: "BR", product: "no-such-offer", idempotencyKey: "op-3" }),
    );
    assert.equal(failure.code, code);
    assert.equal(failure.kind, kind);
    assert.equal(context.calls.length, 1, `${code}: no replay of a refused purchase`);
  }
});

test("a fallen provider fails without crediting anything", async () => {
  const context = createTestContext({ responder: () => problemResponse(500, "server_error") });

  const failure = await captureApiError(() =>
    createBillingClient(context.core).checkout({ market: "BR", product: "pass_1", idempotencyKey: "op-4" }),
  );

  assert.equal(failure.code, "server_error");
  assert.equal(context.calls.length, 1);
});
