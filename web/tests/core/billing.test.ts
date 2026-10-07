/**
 * Tests of the billing client (P54-T03 checkout; P54-T04
 * subscription and portal) against a fake transport: the browser
 * names only the minimal intent — market and product — with the
 * operation token in the request body. The contract declares no
 * `Idempotency-Key` header parameter for these operations, so
 * the core sends none; every call is single-shot and the
 * store's own idempotency is what makes asking again with the
 * same token safe. Amount, currency, price and return URLs
 * resolve on the server; no card number, no credential and no
 * provider identifier ever travels.
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

const SUBSCRIPTION = {
  has_subscription: true,
  status: "active",
  product: "member_monthly",
  market: "BR",
  current_period_end: "2026-11-05T10:00:00Z",
  cancel_at_period_end: false,
} as const;

const PORTAL = { portal_url: "https://billing.stripe.com/session/bps_test_1" } as const;

test("the checkout posts the minimal intent with the token in the body", async () => {
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
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
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
  for (const marker of ["amount", "currency", "price", "redirect", "success", "cancel", "season", "card", "token"]) {
    assert.ok(!serialized.includes(marker), `server-resolved field leaked into the request: ${marker}`);
  }
});

test("asking again with the same token resolves without a second purchase", async () => {
  const context = createTestContext({
    responder: () => jsonResponse({ ...CHECKOUT, replayed: true }),
  });

  const answer = await createBillingClient(context.core).checkout({
    market: "BR",
    product: "pass_1",
    idempotencyKey: "op-same",
  });

  assert.equal(answer.replayed, true);
  assert.deepEqual(bodyOf(context.lastCall()), {
    market: "BR",
    product: "pass_1",
    idempotency_key: "op-same",
  });
  assert.equal(context.calls.length, 1, "one intent carries one token; the server replays it");
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

test("the subscription reads the Member projection with no provider identifiers", async () => {
  const context = createTestContext({ responder: () => jsonResponse(SUBSCRIPTION) });

  const projection = await createBillingClient(context.core).subscription();

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/billing/subscription");
  assert.equal(projection.has_subscription, true);
  assert.equal(projection.status, "active");
  assert.equal(context.lastCall().init.cache, "no-store");
  const serialized = JSON.stringify(projection).toLowerCase();
  for (const marker of ["customer", "sub_", "price_", "pi_", "card", "token"]) {
    assert.ok(!serialized.includes(marker), `provider identifier leaked: ${marker}`);
  }
});

test("absence is explicit: no subscription is a state, never a failure", async () => {
  const context = createTestContext({ responder: () => jsonResponse({ has_subscription: false }) });

  const projection = await createBillingClient(context.core).subscription();

  assert.equal(projection.has_subscription, false);
  assert.equal(context.calls.length, 1);
});

test("another account's portal answers not found without opening anything", async () => {
  const context = createTestContext({ responder: () => problemResponse(404, "no_billing_customer") });

  const failure = await captureApiError(() => createBillingClient(context.core).portal({ idempotencyKey: "op-5" }));

  assert.equal(failure.code, "no_billing_customer");
  assert.equal(failure.kind, "not_found");
  assert.deepEqual(bodyOf(context.lastCall()), { idempotency_key: "op-5" });
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(context.calls.length, 1);
});

test("the portal opens with only the portal URL and no card data", async () => {
  const context = createTestContext({ responder: () => jsonResponse(PORTAL) });

  const session = await createBillingClient(context.core).portal();

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/billing/portal");
  assert.deepEqual(bodyOf(context.lastCall()), {});
  assert.equal(session.portal_url, "https://billing.stripe.com/session/bps_test_1");
  assert.equal(context.lastCall().init.cache, "no-store");
});
