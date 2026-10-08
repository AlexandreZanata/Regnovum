/**
 * Tests of the staged feature pages (P56-T04).
 *
 * They run the real generated catalogs in both locales: a disabled
 * page renders the honest unavailability sentence of its own module
 * and sends no mutation, and only an enabled harness composition may
 * exercise the real staged client — through the fake transport here,
 * against the real handlers in `tools/stagedharness`. Manipulating a
 * DOM attribute, a capability string or a URL never enables the
 * backend: the gate reads the capabilities value alone, and requiring
 * a disabled feature throws instead of sending.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createStagedClient } from "../../src/core/clients/staged.js";
import { noStagedCapabilities, stagedCapabilities } from "../../src/core/staged.js";
import type { StagedFeature } from "../../src/core/staged.js";
import {
  StagedUnavailableError,
  requireStagedEnabled,
  stagedGate,
  stagedUnavailableView,
} from "../../src/pages/staged.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type { Locale } from "../../src/i18n/locale.js";
import { createTestContext, jsonResponse } from "../support/harness.js";

/** A translator holding exactly the namespaces the staged views render from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["seasons", "metering", "commerce", "disputes"] });
}

const FEATURES: readonly StagedFeature[] = ["seasons", "metering", "commerce", "disputes"];

const EXPECTED_PT: Readonly<Record<StagedFeature, string>> = {
  seasons: "Temporada indisponível",
  metering: "Cobrança indisponível",
  commerce: "Recibo indisponível",
  disputes: "Caso indisponível",
};

const EXPECTED_EN: Readonly<Record<StagedFeature, string>> = {
  seasons: "Season unavailable",
  metering: "Charge unavailable",
  commerce: "Receipt unavailable",
  disputes: "Case unavailable",
};

test("a disabled page renders honest unavailability and sends nothing", () => {
  const capabilities = noStagedCapabilities();

  for (const feature of FEATURES) {
    const gate = stagedGate(capabilities, translatorOf("pt-BR"), feature);
    assert.equal(gate.enabled, false);
    if (gate.view === null) {
      throw new Error(`${feature}: a disabled page must render its unavailability view`);
    }
    assert.equal(gate.view.feature, feature);
    assert.equal(gate.view.heading, EXPECTED_PT[feature]);
    assert.ok(gate.view.detail.length > 0, `${feature}: the detail sentence is missing`);

    assert.throws(() => requireStagedEnabled(capabilities, feature), StagedUnavailableError);
  }
});

test("the unavailability renders in both locales from the catalog", () => {
  for (const feature of FEATURES) {
    const pt = stagedUnavailableView(translatorOf("pt-BR"), feature);
    const en = stagedUnavailableView(translatorOf("en-US"), feature);

    assert.equal(pt.heading, EXPECTED_PT[feature]);
    assert.equal(en.heading, EXPECTED_EN[feature]);
    assert.notEqual(pt.detail, en.detail, `${feature}: the locales must not share one sentence`);
  }
});

test("a forged capability string never enables the backend", () => {
  const forged = stagedCapabilities([
    "Seasons",
    "SEASONS",
    " seasons ",
    "meter",
    "commerce;drop",
    "<img src=x>",
    "disputes\u0000",
  ]);

  for (const feature of FEATURES) {
    assert.equal(stagedGate(forged, translatorOf("pt-BR"), feature).enabled, false);
    assert.throws(() => requireStagedEnabled(forged, feature), StagedUnavailableError);
  }
});

test("an enabled harness composition exercises the real client once", async () => {
  const capabilities = stagedCapabilities(["seasons"]);
  const gate = stagedGate(capabilities, translatorOf("pt-BR"), "seasons");
  assert.equal(gate.enabled, true);
  assert.equal(gate.view, null);

  requireStagedEnabled(capabilities, "seasons");
  const context = createTestContext({
    responder: () =>
      jsonResponse({
        ends_at: "2026-11-05T00:00:00Z",
        ordinal: 2,
        season_key: "temporada-harness-b",
        starts_at: "2026-10-05T00:00:00Z",
        state: "active",
        title: "Temporada Harness B",
      }),
  });
  const season = await createStagedClient(context.core).readCurrentSeason();

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/seasons/current");
  assert.equal(season.season_key, "temporada-harness-b");
});

test("enabling one feature leaves the other three honestly unavailable", () => {
  const capabilities = stagedCapabilities(["disputes"]);

  const open = stagedGate(capabilities, translatorOf("en-US"), "disputes");
  assert.equal(open.enabled, true);

  for (const feature of ["seasons", "metering", "commerce"] as const) {
    const closed = stagedGate(capabilities, translatorOf("en-US"), feature);
    assert.equal(closed.enabled, false);
    if (closed.view === null) {
      throw new Error(`${feature}: a disabled page must render its unavailability view`);
    }
    assert.equal(closed.view.heading, EXPECTED_EN[feature]);
    try {
      requireStagedEnabled(capabilities, feature);
      throw new Error(`${feature}: a disabled page must refuse the backend`);
    } catch (error) {
      if (!(error instanceof StagedUnavailableError)) {
        throw new Error(`${feature}: refused with the wrong error`);
      }
      assert.equal(error.feature, feature);
    }
  }
});
