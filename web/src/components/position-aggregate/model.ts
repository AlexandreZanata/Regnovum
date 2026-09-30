/**
 * DOM-free decisions of the public aggregate presentation (P18-T06 journey,
 * "Visitante entende uma Arena").
 *
 * The page fetches the public aggregate through the positions client and hands
 * the contract value here together with the text it translated from the
 * catalog. The custom element is a thin adapter over `aggregateView`: keeping
 * the shape, the row order and the suppression rule here means the Node runner
 * verifies them without a browser.
 *
 * Nothing in this module reads a catalog or formats a number. `AggregateText`
 * carries the translated strings and the count formatter, so one view renders
 * in whichever locale the page resolved, and the component never decides the
 * language of a document.
 */
import type { PositionAggregate, PositionDistribution } from "../../contracts/generated.js";

/** The positions of a distribution, in the order the page presents them. */
export type AggregatePosition = "agree" | "disagree" | "undecided";

/** Every position, in presentation order. */
export const AGGREGATE_POSITIONS: readonly AggregatePosition[] = ["agree", "disagree", "undecided"];

/** One distribution row: a translated label and its formatted count. */
export interface AggregateRow {
  readonly label: string;
  readonly count: string;
}

/** The translated text and formatting one aggregate rendering needs. */
export interface AggregateText {
  readonly heading: string;
  readonly total: string;
  readonly currentHeading: string;
  readonly initialHeading: string;
  readonly checked: string;
  readonly suppressedNote: string;
  readonly choices: Readonly<Record<AggregatePosition, string>>;
  /** Formats one count for the locale of the page. */
  readonly formatCount: (value: number) => string;
}

/** Everything the element renders for one aggregate. */
export interface AggregateView {
  readonly heading: string;
  /** The suppression note, or null when the aggregate was published. */
  readonly suppressedNote: string | null;
  /** The eligible total, or null when the aggregate was suppressed. */
  readonly total: string | null;
  readonly currentHeading: string | null;
  readonly initialHeading: string | null;
  readonly current: readonly AggregateRow[];
  readonly initial: readonly AggregateRow[];
  readonly checked: string;
}

/** rows projects one distribution into the presentation order. */
function rows(distribution: PositionDistribution, text: AggregateText): readonly AggregateRow[] {
  return AGGREGATE_POSITIONS.map((position) => ({
    label: text.choices[position],
    count: text.formatCount(distribution[position]),
  }));
}

/**
 * aggregateView mirrors the server's rendering of a revealed aggregate.
 *
 * A suppressed aggregate carries only the heading, the suppression note and
 * the derivation instant — never a count — because the whole point of the
 * suppression is that a small sample cannot be read back into positions. A
 * published one carries the total, both distributions and the instant, and the
 * distributions always list the three positions, including a zero, so every
 * Arena shows the reader the same rows.
 */
export function aggregateView(aggregate: PositionAggregate, text: AggregateText): AggregateView {
  if (aggregate.suppressed) {
    return {
      heading: text.heading,
      suppressedNote: text.suppressedNote,
      total: null,
      currentHeading: null,
      initialHeading: null,
      current: [],
      initial: [],
      checked: text.checked,
    };
  }
  return {
    heading: text.heading,
    suppressedNote: null,
    total: text.total,
    currentHeading: text.currentHeading,
    initialHeading: text.initialHeading,
    current: rows(aggregate.current, text),
    initial: rows(aggregate.initial, text),
    checked: text.checked,
  };
}
