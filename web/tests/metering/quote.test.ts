/**
 * Tests of the staged metering exhibit (P36-T08): translated
 * snapshots in pt-BR and en-US, catalog parity, hostile values kept
 * literal and the pure state mapping. Amounts travel as integers
 * until the translator groups them, so a translation never enters a
 * computation; dates arrive as RFC 3339 text from the API.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { messagePlaceholders, messages } from "../../src/i18n/generated.js";
import { formatInstant } from "../../src/i18n/formats.js";
import { createTranslator } from "../../src/i18n/translator.js";
import { exhibitPresentation } from "../../src/components/metering/model.js";

const POSTED = "2026-09-29T12:00:00Z";

test("metering receipt snapshots in pt-BR and en-US", () => {
  const pt = createTranslator("pt-BR");
  const en = createTranslator("en-US");

  assert.equal(pt.translate("metering.receipt.title"), "Recibo de INK");
  assert.equal(en.translate("metering.receipt.title"), "INK receipt");

  assert.equal(
    pt.translate("metering.receipt.total", { total: 2750, units: 11 }),
    "2.750 milésimos de INK por 11 grafemas",
  );
  assert.equal(
    en.translate("metering.receipt.total", { total: 2750, units: 11 }),
    "2,750 thousandths of INK for 11 graphemes",
  );

  assert.equal(
    pt.translate("metering.receipt.posted", { date: formatInstant("pt-BR", POSTED, { timeZone: "UTC" }) }),
    `Publicado em ${formatInstant("pt-BR", POSTED, { timeZone: "UTC" })}`,
  );
  assert.equal(
    en.translate("metering.receipt.posted", { date: formatInstant("en-US", POSTED, { timeZone: "UTC" }) }),
    `Posted ${formatInstant("en-US", POSTED, { timeZone: "UTC" })}`,
  );

  assert.equal(pt.translate("metering.statement.balance", { total: 997250 }), "Saldo: 997.250 milésimos de INK");
  assert.equal(en.translate("metering.statement.balance", { total: 997250 }), "Balance: 997,250 thousandths of INK");
  assert.equal(pt.translate("metering.failure.title"), "Cobrança indisponível");
  assert.equal(en.translate("metering.failure.title"), "Charge unavailable");
});

test("metering keys exist in both locales with the same placeholders", () => {
  const ptCatalog = messages["pt-BR"] ?? {};
  const enCatalog = messages["en-US"] ?? {};
  const keys = Object.keys(ptCatalog).filter((key) => key.startsWith("metering."));
  assert.ok(keys.length >= 10, `expected the metering catalog, found ${keys.length} keys`);
  for (const key of keys) {
    const pt: string | undefined = ptCatalog[key];
    const en: string | undefined = enCatalog[key];
    assert.ok(en !== undefined, `metering key ${key} missing in en-US`);
    assert.ok(pt !== undefined && pt !== en, `metering key ${key} is not translated`);
  }
  const declared = new Set(keys);
  for (const key of Object.keys(messagePlaceholders)) {
    if (key.startsWith("metering.") && !declared.has(key)) {
      throw new Error(`placeholder declaration without catalog key: ${key}`);
    }
  }
});

test("hostile values stay literal in metering text", () => {
  const hostile = '{total}<img src=x onerror="alert(1)">';
  const untyped = createTranslator("pt-BR") as unknown as {
    translate(key: string, values?: Record<string, unknown>): string;
  };
  const rendered = untyped.translate("metering.receipt.total", { total: hostile, units: 11 });
  assert.equal(rendered, `${hostile} milésimos de INK por 11 grafemas`);
});

test("exhibit states map to live regions without touching values", () => {
  const text = { title: "T", total: "2.750", posted: "P", action: "C" };
  const preview = exhibitPresentation("preview", text);
  assert.equal(preview.role, "status");
  assert.deepEqual(preview.lines.map((line) => line.text), ["T", "2.750", "P", "C"]);

  const confirmed = exhibitPresentation("confirmed", { ...text, action: null });
  assert.equal(confirmed.role, "status");
  assert.equal(confirmed.lines.length, 3);

  const failed = exhibitPresentation("failed", { ...text, action: null });
  assert.equal(failed.role, "alert");
  assert.ok(failed.lines.every((line) => line.alert));
});
