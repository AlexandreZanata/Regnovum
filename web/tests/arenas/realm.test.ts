/**
 * Tests of the staged realm exhibit (P39-T03): translated snapshots
 * in pt-BR and en-US, catalog parity, hostile values kept literal
 * and the pure row mapping. Regnovum names the single Kingdom; an
 * Arena names one debate instance with no winner and no official
 * truth. Amounts never enter these strings, so the two locales share
 * no numbers to compare — parity is on keys and placeholders.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { messagePlaceholders, messages } from "../../src/i18n/generated.js";
import { createTranslator } from "../../src/i18n/translator.js";
import { realmPresentation } from "../../src/components/arenas/model.js";

test("realm snapshots in pt-BR and en-US", () => {
  const pt = createTranslator("pt-BR");
  const en = createTranslator("en-US");

  assert.equal(pt.translate("arenas.realm.kingdom"), "Regnovum — o Reino");
  assert.equal(en.translate("arenas.realm.kingdom"), "Regnovum — the Kingdom");

  assert.equal(pt.translate("arenas.realm.arena"), "Arena de debate — instância de controvérsia");
  assert.equal(en.translate("arenas.realm.arena"), "Debate arena — controversy instance");

  assert.equal(
    pt.translate("arenas.realm.no_official_outcome"),
    "Debates não têm vencedor nem verdade oficial",
  );
  assert.equal(
    en.translate("arenas.realm.no_official_outcome"),
    "Debates carry no winner and no official truth",
  );
});

test("realm keys exist in both locales with the same placeholders", () => {
  const ptCatalog = messages["pt-BR"] ?? {};
  const enCatalog = messages["en-US"] ?? {};
  const keys = Object.keys(ptCatalog).filter((key) => key.startsWith("arenas.realm."));
  assert.equal(keys.length, 3);
  for (const key of keys) {
    const pt: string | undefined = ptCatalog[key];
    const en: string | undefined = enCatalog[key];
    assert.ok(en !== undefined, `realm key ${key} missing in en-US`);
    assert.ok(pt !== undefined && pt !== en, `realm key ${key} is not translated`);
  }
  const declared = new Set(keys);
  for (const key of Object.keys(messagePlaceholders)) {
    if (key.startsWith("arenas.realm.") && !declared.has(key)) {
      throw new Error(`placeholder declaration without catalog key: ${key}`);
    }
  }
});

test("hostile values stay literal in realm text", () => {
  const hostile = 'Regnovum<img src=x onerror="alert(1)">';
  const exhibit = realmPresentation({ kingdom: hostile, arena: "A", note: "N" });
  assert.deepEqual(
    exhibit.lines.map((line) => line.text),
    [hostile, "A", "N"],
  );
});

test("realm rows keep fixed order and skip empties", () => {
  const exhibit = realmPresentation({ kingdom: "K", arena: "", note: "N" });
  assert.equal(exhibit.role, "status");
  assert.deepEqual(
    exhibit.lines.map((line) => line.text),
    ["K", "N"],
  );
});
