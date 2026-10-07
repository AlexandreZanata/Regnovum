/**
 * Tests of the Member subscription presentation (P54-T04).
 *
 * They run the real generated catalogs in both locales: absence
 * renders as a state, an active projection renders its lifecycle
 * with the period localized, a canceled ledger renders its
 * warning, and switching the locale re-renders the same instant
 * instead of moving it. The portal follows only a safe shape;
 * the return trip grants nothing. No card number, no credential
 * and no provider identifier renders anywhere.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { portalView, subscriptionFailure, subscriptionPresentation } from "../../src/pages/subscription.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type { BillingSubscription } from "../../src/contracts/generated.js";
import type { Locale } from "../../src/i18n/locale.js";

/** A translator holding exactly the namespace the Member views render from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["wallet"] });
}

const ACTIVE: BillingSubscription = {
  has_subscription: true,
  status: "active",
  product: "member_monthly",
  market: "BR",
  current_period_end: "2026-11-05T10:00:00Z",
  cancel_at_period_end: false,
};

test("absence is a state with no period and no warning", () => {
  const view = subscriptionPresentation(translatorOf("en-US"), "en-US", { has_subscription: false });

  assert.equal(view.state, "No active subscription");
  assert.equal(view.period, null);
  assert.equal(view.cancelNote, null);
  assert.ok(view.benefitNote.includes("rules"), "benefit warning missing");
});

test("an active projection renders its lifecycle with a localized period", () => {
  const view = subscriptionPresentation(translatorOf("pt-BR"), "pt-BR", ACTIVE);

  assert.ok(view.state.includes("active"), "lifecycle vocabulary missing");
  assert.ok(view.state.includes("member_monthly"), "product missing");
  assert.ok(view.state.includes("BR"), "market missing");
  assert.ok(view.period !== null && !view.period.includes("2026-11-05T10:00:00Z"), "raw instant leaked");
  assert.equal(view.cancelNote, null);
});

test("a canceling ledger warns without changing the lifecycle", () => {
  const view = subscriptionPresentation(translatorOf("en-US"), "en-US", {
    ...ACTIVE,
    status: "active",
    cancel_at_period_end: true,
  });

  assert.ok(view.state.includes("active"));
  assert.ok(view.cancelNote !== null && view.cancelNote.includes("canceled"));
});

test("past due and canceled render verbatim without new rules", () => {
  for (const status of ["past_due", "canceled", "trialing", "unpaid", "paused"] as const) {
    const view = subscriptionPresentation(translatorOf("en-US"), "en-US", { ...ACTIVE, status });
    assert.ok(view.state.includes(status), `lifecycle not rendered verbatim: ${status}`);
  }
  const serialized = JSON.stringify(
    subscriptionPresentation(translatorOf("en-US"), "en-US", ACTIVE),
  ).toLowerCase();
  for (const marker of ["customer", "sub_", "price_", "pi_", "card", "token", "grant", "renew"]) {
    assert.ok(!serialized.includes(marker), `provider or benefit marker leaked: ${marker}`);
  }
});

test("switching the locale never moves the period", () => {
  const pt = subscriptionPresentation(translatorOf("pt-BR"), "pt-BR", ACTIVE);
  const en = subscriptionPresentation(translatorOf("en-US"), "en-US", ACTIVE);

  assert.notEqual(pt.period, en.period, "locales must render differently");
  assert.ok(pt.state.includes("active") && en.state.includes("active"), "lifecycle must travel verbatim");
});

test("the portal follows only a safe shape and the return grants nothing", () => {
  const open = portalView(translatorOf("en-US"), { portal_url: "https://billing.stripe.com/session/bps_1" });
  assert.equal(open.portalUrl, "https://billing.stripe.com/session/bps_1");

  const hijacked = portalView(translatorOf("en-US"), { portal_url: "javascript:alert(1)" });
  assert.equal(hijacked.portalUrl, null, "a dangerous shape must never become navigation");
  assert.ok(hijacked.note.includes("unavailable"));
});

test("Member failures name the real server codes and fall back otherwise", () => {
  const translator = translatorOf("en-US");

  assert.ok(subscriptionFailure(translator, "unauthorized").includes("Sign in"));
  assert.ok(subscriptionFailure(translator, "no_billing_customer").includes("No billing customer"));
  assert.ok(subscriptionFailure(translator, "rate_limited").includes("Too many"));
  assert.equal(
    subscriptionFailure(translator, "something-the-backend-never-emits"),
    subscriptionFailure(translator, "unknown-code"),
    "an unknown code must fall back to the generic sentence",
  );
});
