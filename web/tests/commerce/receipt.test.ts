/**
 * Tests of the staged trade exhibit (P37-T07): translated snapshots
 * in pt-BR and en-US, catalog parity, hostile values kept literal
 * and the pure state mapping. Amounts travel as integers until the
 * translator groups them, so a translation never enters a
 * computation; dates arrive as RFC 3339 text from the API.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { messagePlaceholders, messages } from "../../src/i18n/generated.js";
import { formatInstant } from "../../src/i18n/formats.js";
import { createTranslator } from "../../src/i18n/translator.js";
import { tradePresentation } from "../../src/components/commerce/model.js";

const POSTED = "2026-09-29T12:00:00Z";

test("trade receipt snapshots in pt-BR and en-US", () => {
  const pt = createTranslator("pt-BR");
  const en = createTranslator("en-US");

  assert.equal(pt.translate("commerce.receipt.title"), "Recibo de comércio");
  assert.equal(en.translate("commerce.receipt.title"), "Trade receipt");

  assert.equal(
    pt.translate("commerce.receipt.gross", { total: 20000 }),
    "20.000 milésimos de INK (bruto)",
  );
  assert.equal(
    en.translate("commerce.receipt.gross", { total: 20000 }),
    "20,000 thousandths of INK (gross)",
  );
  assert.equal(
    pt.translate("commerce.receipt.tithe", { total: 2000 }),
    "2.000 milésimos de INK (dízimo)",
  );
  assert.equal(
    en.translate("commerce.receipt.tithe", { total: 2000 }),
    "2,000 thousandths of INK (tithe)",
  );
  assert.equal(
    pt.translate("commerce.receipt.net", { total: 18000 }),
    "18.000 milésimos de INK (líquido)",
  );
  assert.equal(
    en.translate("commerce.receipt.net", { total: 18000 }),
    "18,000 thousandths of INK (net)",
  );

  assert.equal(
    pt.translate("commerce.receipt.posted", { date: formatInstant("pt-BR", POSTED, { timeZone: "UTC" }) }),
    `Publicado em ${formatInstant("pt-BR", POSTED, { timeZone: "UTC" })}`,
  );
  assert.equal(
    en.translate("commerce.receipt.posted", { date: formatInstant("en-US", POSTED, { timeZone: "UTC" }) }),
    `Posted ${formatInstant("en-US", POSTED, { timeZone: "UTC" })}`,
  );

  assert.equal(pt.translate("commerce.statement.title"), "Extrato de comércio");
  assert.equal(en.translate("commerce.statement.title"), "Trade statement");
  assert.equal(pt.translate("commerce.failure.title"), "Recibo indisponível");
  assert.equal(en.translate("commerce.failure.title"), "Receipt unavailable");
});

test("commerce keys exist in both locales with the same placeholders", () => {
  const ptCatalog = messages["pt-BR"] ?? {};
  const enCatalog = messages["en-US"] ?? {};
  const keys = Object.keys(ptCatalog).filter((key) => key.startsWith("commerce."));
  assert.ok(keys.length >= 10, `expected the commerce catalog, found ${keys.length} keys`);
  for (const key of keys) {
    const pt: string | undefined = ptCatalog[key];
    const en: string | undefined = enCatalog[key];
    assert.ok(en !== undefined, `commerce key ${key} missing in en-US`);
    assert.ok(pt !== undefined && pt !== en, `commerce key ${key} is not translated`);
  }
  const declared = new Set(keys);
  for (const key of Object.keys(messagePlaceholders)) {
    if (key.startsWith("commerce.") && !declared.has(key)) {
      throw new Error(`placeholder declaration without catalog key: ${key}`);
    }
  }
});

test("hostile values stay literal in trade text", () => {
  const hostile = '{total}<img src=x onerror="alert(1)">';
  const untyped = createTranslator("pt-BR") as unknown as {
    translate(key: string, values?: Record<string, unknown>): string;
  };
  const rendered = untyped.translate("commerce.receipt.gross", { total: hostile });
  assert.equal(rendered, `${hostile} milésimos de INK (bruto)`);
});

test("trade states map to live regions without touching values", () => {
  const text = { title: "T", gross: "20.000", tithe: "2.000", net: "18.000", posted: "P", action: "C" };
  const pending = tradePresentation("pending", text);
  assert.equal(pending.role, "status");
  assert.deepEqual(pending.lines.map((line) => line.text), ["T", "20.000", "2.000", "18.000", "P", "C"]);

  const settled = tradePresentation("settled", { ...text, action: null });
  assert.equal(settled.role, "status");
  assert.equal(settled.lines.length, 5);

  const failed = tradePresentation("failed", { ...text, action: null });
  assert.equal(failed.role, "alert");
  assert.ok(failed.lines.every((line) => line.alert));
});
