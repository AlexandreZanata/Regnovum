/**
 * Tests of the staged season champions exhibit (P47-T08): catalog
 * parity pt-BR/en-US, co-leader order, hostile pseudonyms kept literal
 * and the pure row mapping. Exact wealth never reaches the exhibit:
 * the server redacts it, so the browser cannot leak the íntegra.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { messages } from "../../src/i18n/generated.js";
import { createTranslator } from "../../src/i18n/translator.js";
import { championsPresentation } from "../../src/components/seasons/model.js";

test("champions catalogs resolve in pt-BR and en-US", () => {
  const ptCatalog = messages["pt-BR"] ?? {};
  const enCatalog = messages["en-US"] ?? {};
  assert.ok(ptCatalog["seasons.champions.title"] !== undefined);
  assert.ok(enCatalog["seasons.champions.title"] !== undefined);
  const pt = createTranslator("pt-BR");
  const en = createTranslator("en-US");
  assert.equal(pt.translate("seasons.champions.richest"), "Mais rico no corte");
  assert.equal(en.translate("seasons.champions.richest"), "Richest at cutoff");
  assert.equal(pt.translate("seasons.champions.last_king"), "Último Rei");
  assert.equal(en.translate("seasons.champions.last_king"), "Last King");
});

test("champions rows keep fixed order with co-leaders", () => {
  const exhibit = championsPresentation({
    title: "Histórico de temporadas",
    richest: "Mais rico no corte",
    lastKing: "Último Rei",
    leaders: ["coruja-azul", "lobo-cinza"],
  });
  assert.equal(exhibit.role, "status");
  assert.deepEqual(
    exhibit.lines.map((line) => line.text),
    ["Histórico de temporadas", "Mais rico no corte", "Último Rei", "coruja-azul", "lobo-cinza"],
  );
});

test("hostile pseudonyms stay literal for textContent wiring", () => {
  const hostile = "coruja<script>alert(1)</script>";
  const exhibit = championsPresentation({
    title: "t",
    richest: "r",
    lastKing: "k",
    leaders: [hostile],
  });
  assert.equal(exhibit.lines[3]?.text, hostile);
});
