/**
 * Tests of the server-priced checkout presentation (P54-T03).
 *
 * They run the real generated catalogs in both locales: only the
 * twelve catalog offers may be bought, the price renders the
 * server's minor units in the server's currency, an arbitrary
 * redirect is never followed, and returning from the provider —
 * once or twice — grants nothing by itself. Nothing seasonal is
 * for sale.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { MVP_OFFERS, checkoutFailure, checkoutView, isMvpOffer, isSafeRedirectUrl } from "../../src/pages/checkout.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type { BillingCheckout } from "../../src/contracts/generated.js";
import type { Locale } from "../../src/i18n/locale.js";

/** A translator holding exactly the namespace the checkout views render from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["wallet"] });
}

const OPEN_CHECKOUT: BillingCheckout = {
  intent_id: "intent-1",
  status: "open",
  redirect_url: "https://checkout.stripe.com/pay/cs_test_1",
  amount_minor: 990,
  currency: "BRL",
  market: "BR",
  product: "pass_1",
  replayed: false,
};

test("only the twelve catalog offers may be bought", () => {
  assert.equal(MVP_OFFERS.length, 12);
  assert.ok(isMvpOffer("BR", "pass_1"));
  assert.ok(isMvpOffer("INTERNATIONAL", "member_monthly"));
  assert.ok(!isMvpOffer("BR", "season_pass"), "a seasonal offer must never be sent");
  assert.ok(!isMvpOffer("BR", ""), "a blank product must never be sent");
  assert.ok(!isMvpOffer("MOON", "pass_1"), "an unknown market must never be sent");
  const serialized = JSON.stringify(MVP_OFFERS).toLowerCase();
  assert.ok(!serialized.includes("season"), "nothing seasonal is for sale");
});

test("the price renders the server's minor units, never a float", () => {
  const view = checkoutView(translatorOf("pt-BR"), "pt-BR", OPEN_CHECKOUT);

  assert.equal(view.offer, "pass_1 (BR)");
  assert.ok(view.price.includes("9,90"), `server price misread: ${view.price}`);
  assert.ok(!view.price.includes("990"), "minor units leaked unformatted");
  assert.equal(view.redirectUrl, "https://checkout.stripe.com/pay/cs_test_1");

  const usd = checkoutView(translatorOf("en-US"), "en-US", {
    ...OPEN_CHECKOUT,
    amount_minor: 199,
    currency: "USD",
    market: "INTERNATIONAL",
    product: "pass_1",
  });
  assert.ok(usd.price.includes("1.99"), `server price misread: ${usd.price}`);
});

test("a dangerous redirect shape is never followed", () => {
  // The browser owns the shape — HTTPS with a host, no
  // credentials, no fragment — while the host allowlist stays
  // the server's authority (boot-refused configuration plus a
  // gateway that refuses a malformed URL). A well-formed HTTPS
  // URL the server answered is followed; anything else is not.
  for (const raw of [
    "javascript:alert(1)",
    "http://checkout.stripe.com/pay/x",
    "https://user:pass@checkout.stripe.com/pay/x",
    "https://checkout.stripe.com/pay/x#fragment",
    "",
    "not-a-url",
    "/relative/path",
  ]) {
    assert.equal(isSafeRedirectUrl(raw), false, `unsafe redirect accepted: ${raw}`);
  }
  assert.equal(isSafeRedirectUrl("https://checkout.stripe.com/pay/cs_test_1"), true);

  const hijacked = checkoutView(translatorOf("en-US"), "en-US", {
    ...OPEN_CHECKOUT,
    redirect_url: "javascript:alert(1)",
  });
  assert.equal(hijacked.redirectUrl, null, "a dangerous response shape must never become navigation");
  assert.ok(hijacked.note.includes("unavailable"));
});

test("returning grants nothing: paid, expired and failed carry notes, not navigation", () => {
  const paid = checkoutView(translatorOf("en-US"), "en-US", { ...OPEN_CHECKOUT, status: "paid" });
  assert.equal(paid.redirectUrl, null);
  assert.ok(paid.note.includes("confirmation"), `paid must point at confirmation: ${paid.note}`);

  const expired = checkoutView(translatorOf("en-US"), "en-US", { ...OPEN_CHECKOUT, status: "expired" });
  assert.equal(expired.redirectUrl, null);
  assert.ok(expired.note.includes("expired"));

  const failed = checkoutView(translatorOf("pt-BR"), "pt-BR", { ...OPEN_CHECKOUT, status: "failed" });
  assert.equal(failed.redirectUrl, null);
  assert.ok(failed.note.includes("falhou"));
});

test("a replayed answer resolves the same session without a second purchase", () => {
  const replayed = checkoutView(translatorOf("en-US"), "en-US", { ...OPEN_CHECKOUT, replayed: true });
  assert.equal(replayed.redirectUrl, "https://checkout.stripe.com/pay/cs_test_1");
  assert.equal(replayed.offer, "pass_1 (BR)");
});

test("checkout failures name the real server codes and fall back otherwise", () => {
  const translator = translatorOf("en-US");

  assert.ok(checkoutFailure(translator, "invalid_checkout").includes("not valid"));
  assert.ok(checkoutFailure(translator, "invalid_body").includes("not valid"));
  assert.ok(checkoutFailure(translator, "unauthorized").includes("Sign in"));
  assert.ok(checkoutFailure(translator, "not_eligible").includes("cannot buy"));
  assert.ok(checkoutFailure(translator, "rate_limited").includes("Too many"));
  assert.equal(
    checkoutFailure(translator, "something-the-backend-never-emits"),
    checkoutFailure(translator, "unknown-code"),
    "an unknown code must fall back to the generic sentence",
  );
});
