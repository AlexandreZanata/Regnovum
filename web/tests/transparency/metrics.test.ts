/**
 * Tests of the staged transparency exhibit (P38-T08): translated
 * snapshots in pt-BR and en-US with identical integers, catalog
 * parity, hostile values kept literal, the pure row mapping and a
 * sealed UTC window no display timezone rewrites. Amounts travel as
 * integers until the translator groups them, so a translation never
 * enters a computation; the window arrives as UTC text from the API.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { messagePlaceholders, messages } from "../../src/i18n/generated.js";
import { formatInstant } from "../../src/i18n/formats.js";
import { createTranslator } from "../../src/i18n/translator.js";
import { metricsPresentation } from "../../src/components/transparency/model.js";

const WINDOW_START = "2026-12-28T00:00:00Z";
const WINDOW_END = "2027-01-04T00:00:00Z";

test("transparency metrics snapshots in pt-BR and en-US share integers", () => {
  const pt = createTranslator("pt-BR");
  const en = createTranslator("en-US");

  assert.equal(pt.translate("transparency.metrics.title"), "Migalhas e refluxo — agregado semanal");
  assert.equal(en.translate("transparency.metrics.title"), "Crumbs and reflux — weekly aggregate");

  assert.equal(
    pt.translate("transparency.metrics.supply", { total: 2750000 }),
    "Oferta total (S): 2.750.000 milésimos de INK",
  );
  assert.equal(
    en.translate("transparency.metrics.supply", { total: 2750000 }),
    "Total supply (S): 2,750,000 thousandths of INK",
  );

  assert.equal(
    pt.translate("transparency.metrics.crumbs", { total: 1000, heads: 5 }),
    "Migalhas concedidas: 1.000 milésimos de INK para 5 contas",
  );
  assert.equal(
    en.translate("transparency.metrics.crumbs", { total: 1000, heads: 5 }),
    "Crumbs granted: 1,000 thousandths of INK to 5 accounts",
  );

  assert.equal(
    pt.translate("transparency.metrics.r4", { epoch: "2026-W53", total: 1400 }),
    "Referência R4 da semana 2026-W53: 1.400 milésimos de INK",
  );
  assert.equal(
    en.translate("transparency.metrics.r4", { epoch: "2026-W53", total: 1400 }),
    "R4 reference for week 2026-W53: 1,400 thousandths of INK",
  );

  assert.equal(pt.translate("transparency.metrics.irr", { ratio: "200.0%" }), "Índice de refluxo real: 200.0%");
  assert.equal(en.translate("transparency.metrics.irr", { ratio: "200.0%" }), "Real reflux index: 200.0%");
  assert.equal(
    pt.translate("transparency.metrics.irr_unavailable"),
    "Índice de refluxo real: indisponível (sem saídas no período)",
  );
  assert.equal(
    en.translate("transparency.metrics.irr_unavailable"),
    "Real reflux index: unavailable (no outflows in the period)",
  );

  const utcWindow = (locale: "pt-BR" | "en-US"): string =>
    createTranslator(locale).translate("transparency.metrics.window", {
      start: formatInstant(locale, WINDOW_START, { timeZone: "UTC" }),
      end: formatInstant(locale, WINDOW_END, { timeZone: "UTC" }),
    });
  assert.equal(
    utcWindow("pt-BR"),
    `Janela selada: ${formatInstant("pt-BR", WINDOW_START, { timeZone: "UTC" })} – ${formatInstant("pt-BR", WINDOW_END, { timeZone: "UTC" })} (UTC)`,
  );
  assert.equal(
    utcWindow("en-US"),
    `Sealed window: ${formatInstant("en-US", WINDOW_START, { timeZone: "UTC" })} – ${formatInstant("en-US", WINDOW_END, { timeZone: "UTC" })} (UTC)`,
  );

  assert.equal(pt.translate("transparency.metrics.suppressed"), "Contagem baixa omitida para reduzir reidentificação");
  assert.equal(en.translate("transparency.metrics.suppressed"), "Low count withheld to reduce reidentification");
});

test("transparency metrics keys exist in both locales with the same placeholders", () => {
  const ptCatalog = messages["pt-BR"] ?? {};
  const enCatalog = messages["en-US"] ?? {};
  const keys = Object.keys(ptCatalog).filter((key) => key.startsWith("transparency.metrics."));
  assert.ok(keys.length >= 10, `expected the transparency metrics catalog, found ${keys.length} keys`);
  for (const key of keys) {
    const pt: string | undefined = ptCatalog[key];
    const en: string | undefined = enCatalog[key];
    assert.ok(en !== undefined, `transparency metrics key ${key} missing in en-US`);
    assert.ok(pt !== undefined && pt !== en, `transparency metrics key ${key} is not translated`);
  }
  const declared = new Set(keys);
  for (const key of Object.keys(messagePlaceholders)) {
    if (key.startsWith("transparency.metrics.") && !declared.has(key)) {
      throw new Error(`placeholder declaration without catalog key: ${key}`);
    }
  }
});

test("hostile values stay literal in transparency text", () => {
  const hostile = '{total}<img src=x onerror="alert(1)">';
  const untyped = createTranslator("pt-BR") as unknown as {
    translate(key: string, values?: Record<string, unknown>): string;
  };
  const rendered = untyped.translate("transparency.metrics.supply", { total: hostile });
  assert.equal(rendered, `Oferta total (S): ${hostile} milésimos de INK`);
});

test("metrics rows keep fixed order and skip empties", () => {
  const exhibit = metricsPresentation({
    title: "T",
    window: "W",
    supply: "S",
    treasury: "",
    circulation: "C",
    r4: "R",
    crumbs: "G",
    irr: "I",
  });
  assert.equal(exhibit.role, "status");
  assert.deepEqual(
    exhibit.lines.map((line) => line.text),
    ["T", "W", "S", "C", "R", "G", "I"],
  );
});

test("display timezone never rewrites the sealed window", () => {
  const utcReading = formatInstant("pt-BR", WINDOW_START, { timeZone: "UTC" });
  const saoPauloReading = formatInstant("pt-BR", WINDOW_START, { timeZone: "America/Sao_Paulo" });
  assert.notEqual(utcReading, saoPauloReading);
  assert.equal(new Date(WINDOW_START).getTime(), new Date(WINDOW_END).getTime() - 7 * 24 * 60 * 60 * 1000);
  assert.equal(utcReading, formatInstant("pt-BR", WINDOW_START, { timeZone: "UTC" }));
});
