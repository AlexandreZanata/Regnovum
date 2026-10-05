/**
 * Presentation of honest disclosures and unavailable products (P43-T06).
 *
 * Verifies that the typed translator renders patent limits, product unavailabilities,
 * and SEO disclosures in both pt-BR and en-US without drift or promises of return.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createTranslator } from "../../src/i18n/translator.js";
import type { Locale } from "../../src/i18n/locale.js";

const locales: readonly Locale[] = ["pt-BR", "en-US"];

test("honest disclosures are translated in pt-BR and en-US with full key parity", () => {
  for (const locale of locales) {
    const translator = createTranslator(locale);

    assert.ok(translator.translate("kingdom.patent.title").length > 0);
    assert.ok(translator.translate("kingdom.patent.honorary_notice").length > 0);
    assert.ok(translator.translate("kingdom.patent.no_power").length > 0);
    assert.ok(translator.translate("kingdom.patent.season_bounded").length > 0);

    assert.ok(translator.translate("kingdom.bonds.title").length > 0);
    assert.ok(translator.translate("kingdom.bonds.unavailable_notice").length > 0);
    assert.ok(translator.translate("kingdom.bonds.no_yield").length > 0);

    assert.ok(translator.translate("kingdom.betting.title").length > 0);
    assert.ok(translator.translate("kingdom.betting.unavailable_notice").length > 0);
    assert.ok(translator.translate("kingdom.betting.no_wagers").length > 0);

    assert.ok(translator.translate("kingdom.disclosures.honest_limits").length > 0);
    assert.ok(translator.translate("kingdom.disclosures.no_financial_return").length > 0);
    assert.ok(translator.translate("kingdom.disclosures.seo_summary").length > 0);
  }
});

test("disclosures define non-authoritative limits for patents", () => {
  const pt = createTranslator("pt-BR");
  assert.match(pt.translate("kingdom.patent.honorary_notice").toLowerCase(), /honorífico/);
  assert.match(pt.translate("kingdom.patent.honorary_notice").toLowerCase(), /não concede poder/);
  assert.match(pt.translate("kingdom.patent.no_power").toLowerCase(), /não compram/);
  assert.match(pt.translate("kingdom.patent.season_bounded").toLowerCase(), /temporada/);

  const en = createTranslator("en-US");
  assert.match(en.translate("kingdom.patent.honorary_notice").toLowerCase(), /honorary/);
  assert.match(en.translate("kingdom.patent.honorary_notice").toLowerCase(), /grants no power/);
  assert.match(en.translate("kingdom.patent.no_power").toLowerCase(), /do not buy/);
  assert.match(en.translate("kingdom.patent.season_bounded").toLowerCase(), /season/);
});

test("disclosures state crown bonds and betting markets are unavailable", () => {
  const pt = createTranslator("pt-BR");
  assert.match(pt.translate("kingdom.bonds.unavailable_notice").toLowerCase(), /não estão disponíveis/);
  assert.match(pt.translate("kingdom.bonds.no_yield").toLowerCase(), /rendimento garantido/);
  assert.match(pt.translate("kingdom.bonds.no_yield").toLowerCase(), /promessa de retorno/);

  assert.match(pt.translate("kingdom.betting.unavailable_notice").toLowerCase(), /estritamente indisponíveis/);
  assert.match(pt.translate("kingdom.betting.no_wagers").toLowerCase(), /não oferece apostas ativas/);

  const en = createTranslator("en-US");
  assert.match(en.translate("kingdom.bonds.unavailable_notice").toLowerCase(), /unavailable/);
  assert.match(en.translate("kingdom.bonds.no_yield").toLowerCase(), /guaranteed yield/);
  assert.match(en.translate("kingdom.bonds.no_yield").toLowerCase(), /no promise of return/);

  assert.match(en.translate("kingdom.betting.unavailable_notice").toLowerCase(), /strictly unavailable/);
  assert.match(en.translate("kingdom.betting.no_wagers").toLowerCase(), /no active wagers/);
});

test("disclosures never carry promises of financial return or active betting", () => {
  const forbiddenPhrases = [
    "lucro garantido",
    "retorno garantido",
    "guaranteed return",
    "aposta ativa",
    "active bet",
    "active wager",
    "asset-backed",
  ];

  for (const locale of locales) {
    const translator = createTranslator(locale);
    const keys = [
      "kingdom.patent.honorary_notice",
      "kingdom.patent.no_power",
      "kingdom.bonds.unavailable_notice",
      "kingdom.betting.unavailable_notice",
      "kingdom.disclosures.honest_limits",
      "kingdom.disclosures.no_financial_return",
      "kingdom.disclosures.seo_summary",
    ] as const;

    for (const key of keys) {
      const rendered = translator.translate(key).toLowerCase();
      for (const phrase of forbiddenPhrases) {
        assert.ok(!rendered.includes(phrase), `locale ${locale} key ${key} carries forbidden phrase "${phrase}"`);
      }
    }
  }
});
