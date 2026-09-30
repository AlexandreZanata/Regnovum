/**
 * Tests of the instant rendering of the Arena page (P18-T06 journey).
 *
 * The entry module only applies what `timeText` returns, so the three cases —
 * the machine value alone, a catalog sentence with the machine value in it,
 * and anything the page must not touch — are verified here without a browser.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { timeText } from "../../src/pages/instants.js";

const INSTANT = "2026-09-23T13:00:00Z";

test("an element showing the machine value alone gets the locale rendering", () => {
  const pt = timeText("pt-BR", INSTANT, INSTANT);
  const en = timeText("en-US", INSTANT, INSTANT);

  assert.notEqual(pt, null);
  assert.notEqual(en, null);
  assert.notEqual(pt, INSTANT);
  assert.notEqual(en, INSTANT);
  assert.ok(pt?.includes("2026"), `expected the year in the pt-BR rendering, got ${String(pt)}`);
  assert.ok(en?.includes("2026"), `expected the year in the en-US rendering, got ${String(en)}`);
  assert.notEqual(pt, en, "the two locales must not render the same text");
});

test("a catalog sentence keeps its words and only the machine value is replaced", () => {
  const visible = `Publicada em ${INSTANT}`;
  const text = timeText("pt-BR", INSTANT, visible);

  assert.notEqual(text, null);
  assert.ok(text?.startsWith("Publicada em "), `expected the sentence to be preserved, got ${String(text)}`);
  assert.ok(!text?.includes(INSTANT), "the raw instant must not survive in the rendered text");
});

test("an empty element still receives the locale rendering", () => {
  const text = timeText("pt-BR", INSTANT, "");

  assert.notEqual(text, null);
  assert.ok(!text?.includes(INSTANT));
});

test("surrounding whitespace does not stop the rendering", () => {
  const text = timeText("pt-BR", `  ${INSTANT}  `, `  Publicada em ${INSTANT}  `);

  assert.notEqual(text, null);
  assert.ok(!text?.includes(INSTANT));
});

test("an element whose text does not carry the machine value is left alone", () => {
  assert.equal(timeText("pt-BR", INSTANT, "Publicada recentemente"), null);
  assert.equal(timeText("pt-BR", INSTANT, "2026-01-01T00:00:00Z"), null);
});

test("a missing or unparseable machine value is refused, never guessed", () => {
  for (const datetime of ["", "   ", "not-an-instant", "23/09/2026", "2026-13-45T99:00:00Z"]) {
    assert.equal(timeText("pt-BR", datetime, INSTANT), null, `timeText(${JSON.stringify(datetime)}) must refuse`);
    assert.equal(timeText("en-US", datetime, `Publicada em ${datetime}`), null, `timeText(${JSON.stringify(datetime)}) must refuse`);
  }
});

test("the refused value is never echoed back into the text", () => {
  const hostile = "<img src=x onerror=alert(1)>";
  assert.equal(timeText("pt-BR", hostile, hostile), null);
});
