/**
 * Presentation of one privacy-safe season champions exhibit (P47-T08).
 *
 * The element is a thin DOM adapter over this module, which is why the
 * row order and the text wiring can be verified on Node without a
 * browser: every dynamic value arrives as an already translated string
 * or a pseudonym (server-redacted) and reaches the document through
 * `textContent` only. Exact wealth never reaches this shape: the HTTP
 * surface hides it, so the browser cannot leak the íntegra.
 */

/** The strings one champions exhibit renders, already translated. */
export interface SeasonChampionsText {
  readonly title: string;
  readonly richest: string;
  readonly lastKing: string;
  readonly leaders: readonly string[];
}

/** One rendered row. */
export interface SeasonChampionsLine {
  readonly text: string;
}

/** The wiring of one champions exhibit: a polite live region plus rows. */
export interface SeasonChampionsExhibit {
  readonly role: "status";
  readonly lines: readonly SeasonChampionsLine[];
}

/**
 * championsPresentation maps one redacted archive to its live region
 * and rows in fixed order: title, richest label, last-King label,
 * then one row per co-leader pseudonym.
 */
export function championsPresentation(text: SeasonChampionsText): SeasonChampionsExhibit {
  const lines: SeasonChampionsLine[] = [
    { text: text.title },
    { text: text.richest },
    { text: text.lastKing },
  ];
  for (const leader of text.leaders) {
    lines.push({ text: leader });
  }
  return { role: "status", lines };
}
