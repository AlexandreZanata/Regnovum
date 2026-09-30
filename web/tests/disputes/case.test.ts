/**
 * Tests of the staged private case exhibit (P39-T08): translated
 * snapshots in pt-BR and en-US, catalog parity, hostile values kept
 * literal and the pure state mapping. Versions travel as canonical
 * text until the translator formats them, so a translation never
 * enters a computation; dates arrive as RFC 3339 text from the API.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { messagePlaceholders, messages } from "../../src/i18n/generated.js";
import { formatInstant } from "../../src/i18n/formats.js";
import { createTranslator } from "../../src/i18n/translator.js";
import { disputePresentation } from "../../src/components/disputes/model.js";

const DECIDED = "2026-10-04T15:00:00Z";

test("dispute case snapshots in pt-BR and en-US", () => {
  const pt = createTranslator("pt-BR");
  const en = createTranslator("en-US");

  assert.equal(pt.translate("disputes.case.title"), "Caso privado");
  assert.equal(en.translate("disputes.case.title"), "Private case");

  assert.equal(
    pt.translate("disputes.case.proposal", { key: "caso-alpha", version: "1" }),
    "Proposta caso-alpha · versão 1",
  );
  assert.equal(
    en.translate("disputes.case.proposal", { key: "caso-alpha", version: "1" }),
    "Proposal caso-alpha · version 1",
  );

  assert.equal(
    pt.translate("disputes.case.accepted", { count: "2" }),
    "2 de 2 aceites",
  );
  assert.equal(
    en.translate("disputes.case.accepted", { count: "2" }),
    "2 of 2 acceptances",
  );

  assert.equal(
    pt.translate("disputes.notice.ruling", {}),
    "Sentença fundamentada",
  );
  assert.equal(
    en.translate("disputes.notice.ruling", {}),
    "Reasoned ruling",
  );

  assert.equal(
    pt.translate("disputes.case.decided", { date: formatInstant("pt-BR", DECIDED, { timeZone: "UTC" }) }),
    `Decidido em ${formatInstant("pt-BR", DECIDED, { timeZone: "UTC" })}`,
  );
  assert.equal(
    en.translate("disputes.case.decided", { date: formatInstant("en-US", DECIDED, { timeZone: "UTC" }) }),
    `Decided ${formatInstant("en-US", DECIDED, { timeZone: "UTC" })}`,
  );

  assert.equal(pt.translate("disputes.ruling.title"), "Sentença do rito");
  assert.equal(en.translate("disputes.ruling.title"), "Rite ruling");
  assert.equal(pt.translate("disputes.failure.title"), "Caso indisponível");
  assert.equal(en.translate("disputes.failure.title"), "Case unavailable");
});

test("disputes keys exist in both locales with the same placeholders", () => {
  const ptCatalog = messages["pt-BR"] ?? {};
  const enCatalog = messages["en-US"] ?? {};
  const keys = Object.keys(ptCatalog).filter((key) => key.startsWith("disputes."));
  assert.ok(keys.length >= 15, `expected the disputes catalog, found ${keys.length} keys`);
  for (const key of keys) {
    const pt: string | undefined = ptCatalog[key];
    const en: string | undefined = enCatalog[key];
    assert.ok(en !== undefined, `disputes key ${key} missing in en-US`);
    assert.ok(pt !== undefined && pt !== en, `disputes key ${key} is not translated`);
  }
  const declared = new Set(keys);
  for (const key of Object.keys(messagePlaceholders)) {
    if (key.startsWith("disputes.") && !declared.has(key)) {
      throw new Error(`placeholder declaration without catalog key: ${key}`);
    }
  }
});

test("hostile values stay literal in dispute text", () => {
  const hostile = 'caso-alpha<img src=x onerror="alert(1)">';
  const untyped = createTranslator("pt-BR") as unknown as {
    translate(key: string, values?: Record<string, unknown>): string;
  };
  const rendered = untyped.translate("disputes.case.proposal", { key: hostile, version: "1" });
  assert.equal(rendered, `Proposta ${hostile} · versão 1`);
});

test("dispute states map to live regions without touching values", () => {
  const text = { title: "T", proposal: "P", parties: "R", decided: "D", action: "C" };
  const proposed = disputePresentation("proposed", text);
  assert.equal(proposed.role, "status");
  assert.deepEqual(proposed.lines.map((line) => line.text), ["T", "P", "R", "D", "C"]);

  const open = disputePresentation("open", { ...text, action: null });
  assert.equal(open.role, "status");
  assert.equal(open.lines.length, 4);

  const decided = disputePresentation("decided", { ...text, action: null });
  assert.equal(decided.role, "alert");
  assert.ok(decided.lines.every((line) => line.alert));

  const appealed = disputePresentation("appealed", text);
  assert.equal(appealed.role, "alert");
  assert.equal(appealed.lines.length, 5);
});
