/**
 * Tests of the public Arena document presentation (P52-T02).
 *
 * They run the real generated catalogs in both locales: the canonical
 * address parses `/d/{slug}` inside the contract bounds and never
 * claims the participation address, both links come from the
 * contract's paths, content renders byte-identical in the Arena's own
 * language with no hreflang, removed answers gone and the rest
 * not-found without echoing the address, and the realm exhibit is the
 * shared one — no rival page.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  documentAddresses,
  documentErrorView,
  documentSlugFromPath,
  documentView,
} from "../../src/pages/document.js";
import { arenaSlugFromPath } from "../../src/pages/participation.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type { PublicArena } from "../../src/contracts/generated.js";
import type { Locale } from "../../src/i18n/locale.js";

/** A translator holding exactly the namespace the document page renders from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["arenas"] });
}

const ARENA: PublicArena = {
  id: "018f6b2a-0000-7000-8000-000000000001",
  slug: "etica-das-maquinas",
  statement: "Máquinas podem ser responsáveis?",
  context: "Contexto oferecido pelo autor.",
  category: "philosophy",
  language: "pt-BR",
  status: "published",
  published_at: "2026-09-20T10:00:00Z",
  closes_at: "2026-12-20T10:00:00Z",
};

test("the canonical address parses inside the contract bounds", () => {
  assert.equal(documentSlugFromPath("/d/etica-das-maquinas"), "etica-das-maquinas");
  assert.equal(documentSlugFromPath("/d/abc"), "abc");
  assert.equal(documentSlugFromPath("/d/ab"), null, "below the contract minimum fetches nothing");
  assert.equal(documentSlugFromPath(`/d/${"a".repeat(81)}`), null, "above the contract maximum fetches nothing");
  assert.equal(documentSlugFromPath("/arenas/etica-das-maquinas"), null, "the participation address belongs elsewhere");
  assert.equal(documentSlugFromPath("/d/"), null);
  assert.equal(documentSlugFromPath("/"), null);
});

test("the participation parser still owns its address", () => {
  assert.equal(arenaSlugFromPath("/arenas/etica-das-maquinas"), "etica-das-maquinas");
  assert.equal(arenaSlugFromPath("/d/etica-das-maquinas"), null, "no rival parser claims the canonical address");
});

test("both links come from the contract paths with the slug encoded", () => {
  const addresses = documentAddresses("etica das maquinas/c");

  assert.equal(addresses.canonical, "/d/etica%20das%20maquinas%2Fc");
  assert.equal(addresses.participation, "/arenas/etica%20das%20maquinas%2Fc");
});

test("the document renders verbatim in the Arena own language", () => {
  const view = documentView(translatorOf("pt-BR"), "pt-BR", ARENA);

  assert.equal(view.title, "Máquinas podem ser responsáveis?");
  assert.equal(view.contentLanguage, "pt-BR");
  assert.equal(view.status, "Publicada");
  assert.equal(view.category, "philosophy", "the taxonomy is shown, never translated");
  assert.equal(view.context, "Contexto oferecido pelo autor.");
  assert.ok(!view.published.includes("2026-09-20T10:00:00Z"), "raw instant leaked");
  assert.ok(view.closes !== null && !view.closes.includes("2026-12-20"), "raw instant leaked");
  assert.equal(view.addresses.canonical, "/d/etica-das-maquinas");
  assert.equal(view.addresses.participation, "/arenas/etica-das-maquinas");
  assert.equal(view.realm.role, "status");
  assert.equal(view.realm.lines.length, 3);
  const serialized = JSON.stringify(view);
  assert.ok(!serialized.includes("hreflang"), "no hreflang without a declared translation");
});

test("the document renders in en-US with a missing context and no closing", () => {
  const open: PublicArena = { ...ARENA, language: "en-US", statement: "Can machines be responsible?", context: null, closes_at: null };
  const view = documentView(translatorOf("en-US"), "en-US", open);

  assert.equal(view.title, "Can machines be responsible?");
  assert.equal(view.contentLanguage, "en-US");
  assert.equal(view.status, "Published");
  assert.equal(view.context, null);
  assert.equal(view.closes, null);
  assert.equal(view.realm.lines[0]?.text, "Regnovum — the Kingdom");
});

test("removed answers gone and the rest not-found, never echoing the address", () => {
  const gone = documentErrorView(translatorOf("pt-BR"), 410);
  assert.equal(gone.title, "Arena removida");

  const missing = documentErrorView(translatorOf("pt-BR"), 404);
  assert.equal(missing.title, "Arena não encontrada");

  const leaked = `etica-das-maquinas${"?x=1"}`;
  assert.ok(!JSON.stringify(gone).includes(leaked));
  assert.ok(!JSON.stringify(missing).includes(leaked));
});

test("the view takes no session and branches on none", () => {
  const visitor = documentView(translatorOf("en-US"), "en-US", ARENA);
  const holder = documentView(translatorOf("en-US"), "en-US", ARENA);

  assert.deepEqual(holder, visitor);
});

test("switching the interface never translates the content language", () => {
  const foreign: PublicArena = {
    ...ARENA,
    slug: "can-machines-be-responsible",
    statement: "Can machines be responsible?",
    context: "Context offered by the author.",
    language: "en-US",
  };

  const read = documentView(translatorOf("pt-BR"), "pt-BR", foreign);
  assert.equal(read.contentLanguage, "en-US", "the document keeps the Arena language");
  assert.equal(read.title, "Can machines be responsible?", "the statement stays verbatim");
  assert.equal(read.context, "Context offered by the author.", "the context stays verbatim");
  assert.equal(read.status, "Publicada", "the chrome follows the interface");
  assert.ok(!read.published.includes("2026-09-20T10:00:00Z"), "raw instant leaked");

  const mirrored = documentView(translatorOf("en-US"), "en-US", ARENA);
  assert.equal(mirrored.contentLanguage, "pt-BR", "the document keeps the Arena language");
  assert.equal(mirrored.title, "Máquinas podem ser responsáveis?", "the statement stays verbatim");
  assert.equal(mirrored.status, "Published", "the chrome follows the interface");
});
