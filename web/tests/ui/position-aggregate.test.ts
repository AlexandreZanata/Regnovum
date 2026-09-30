/**
 * Tests of the aggregate presentation model (P18-T06 journey, browser reveal).
 *
 * The custom element is a thin adapter over `aggregateView`, which is why the
 * suppression rule, the row order and the count formatting can be verified on
 * Node without a browser: the element applies exactly what is asserted here.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { AGGREGATE_POSITIONS, aggregateView } from "../../src/components/position-aggregate/model.js";
import type { AggregateText } from "../../src/components/position-aggregate/model.js";
import type { PositionAggregate } from "../../src/contracts/generated.js";

/** Text as the page translates it; the formatter is visible so it can be proven. */
const TEXT: AggregateText = {
  heading: "Resultado agregado",
  total: "Participantes elegíveis: 1.234",
  currentHeading: "Posição atual",
  initialHeading: "Posição inicial",
  checked: "Apurado em 23 de set. de 2026 10:00",
  suppressedNote: "A amostra é pequena demais para publicar o resultado.",
  choices: { agree: "A favor", disagree: "Contra", undecided: "Sem posição" },
  formatCount: (value: number): string => `#${String(value)}`,
};

/** A published aggregate of the contract shape. */
function published(overrides: Partial<PositionAggregate> = {}): PositionAggregate {
  return {
    checked_at: "2026-09-23T13:00:00Z",
    current: { agree: 10, disagree: 5, undecided: 2 },
    initial: { agree: 8, disagree: 6, undecided: 3 },
    participants_total: 17,
    suppressed: false,
    ...overrides,
  };
}

test("the presentation order is the order the page renders the positions", () => {
  assert.deepEqual([...AGGREGATE_POSITIONS], ["agree", "disagree", "undecided"]);
});

test("a published aggregate carries the total, both distributions and the instant", () => {
  const view = aggregateView(published(), TEXT);

  assert.equal(view.heading, TEXT.heading);
  assert.equal(view.suppressedNote, null);
  assert.equal(view.total, TEXT.total);
  assert.equal(view.currentHeading, TEXT.currentHeading);
  assert.equal(view.initialHeading, TEXT.initialHeading);
  assert.equal(view.checked, TEXT.checked);
  assert.deepEqual(view.current, [
    { label: "A favor", count: "#10" },
    { label: "Contra", count: "#5" },
    { label: "Sem posição", count: "#2" },
  ]);
  assert.deepEqual(view.initial, [
    { label: "A favor", count: "#8" },
    { label: "Contra", count: "#6" },
    { label: "Sem posição", count: "#3" },
  ]);
});

test("the row labels follow the presentation order, never the contract's key order", () => {
  const view = aggregateView(published(), TEXT);
  assert.deepEqual(
    view.current.map((row) => row.label),
    ["A favor", "Contra", "Sem posição"],
  );
  assert.deepEqual(
    view.initial.map((row) => row.label),
    ["A favor", "Contra", "Sem posição"],
  );
});

test("a zero count is still a row: every Arena shows the same three positions", () => {
  const view = aggregateView(published({ current: { agree: 0, disagree: 0, undecided: 0 } }), TEXT);

  assert.equal(view.current.length, 3);
  assert.deepEqual(
    view.current.map((row) => row.count),
    ["#0", "#0", "#0"],
  );
});

test("a suppressed aggregate carries no count and no distribution", () => {
  const view = aggregateView(published({ suppressed: true }), TEXT);

  assert.equal(view.heading, TEXT.heading);
  assert.equal(view.suppressedNote, TEXT.suppressedNote);
  assert.equal(view.total, null);
  assert.equal(view.currentHeading, null);
  assert.equal(view.initialHeading, null);
  assert.deepEqual(view.current, []);
  assert.deepEqual(view.initial, []);
  assert.equal(view.checked, TEXT.checked);
});

test("the counts reach the formatter exactly as the contract sends them", () => {
  const seen: number[] = [];
  const text: AggregateText = {
    ...TEXT,
    formatCount: (value: number): string => {
      seen.push(value);
      return String(value);
    },
  };

  aggregateView(published({ current: { agree: 1, disagree: 2, undecided: 3 }, initial: { agree: 4, disagree: 5, undecided: 6 } }), text);

  assert.deepEqual(seen, [1, 2, 3, 4, 5, 6]);
});

test("the model reads the contract value without mutating it", () => {
  const aggregate = published();
  const before = JSON.stringify(aggregate);

  aggregateView(aggregate, TEXT);
  aggregateView({ ...aggregate, suppressed: true }, TEXT);

  assert.equal(JSON.stringify(aggregate), before);
});

test("every rendered string is non-empty, so a broken catalog cannot render a hole", () => {
  for (const view of [aggregateView(published(), TEXT), aggregateView(published({ suppressed: true }), TEXT)]) {
    const strings = [
      view.heading,
      view.checked,
      ...(view.total === null ? [] : [view.total]),
      ...(view.suppressedNote === null ? [] : [view.suppressedNote]),
      ...(view.currentHeading === null ? [] : [view.currentHeading]),
      ...(view.initialHeading === null ? [] : [view.initialHeading]),
      ...view.current.flatMap((row) => [row.label, row.count]),
      ...view.initial.flatMap((row) => [row.label, row.count]),
    ];
    for (const value of strings) {
      assert.ok(value.trim().length > 0, `rendered an empty string: ${JSON.stringify(view)}`);
    }
  }
});
