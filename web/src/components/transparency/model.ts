/**
 * Presentation of one weekly crumbs-and-reflux aggregate (P38-T08).
 *
 * The element is a thin DOM adapter over this module, which is why the
 * row order and the text wiring can be verified on Node without a
 * browser: every dynamic value arrives as an already translated string
 * (server-rendered or translator-composed) and reaches the document
 * through `textContent` only. Amounts travel as canonical integers
 * until the translator groups them, so a translation never enters a
 * computation; the sealed window travels as UTC text the display
 * timezone never rewrites.
 */

/** The strings one transparency exhibit renders, already translated. */
export interface TransparencyMetricsText {
  readonly title: string;
  readonly window: string;
  readonly supply: string;
  readonly treasury: string;
  readonly circulation: string;
  readonly r4: string;
  readonly crumbs: string;
  readonly irr: string;
}

/** One rendered row. */
export interface TransparencyLine {
  readonly text: string;
}

/** The wiring of one transparency exhibit: a polite live region plus rows. */
export interface TransparencyExhibit {
  readonly role: "status";
  readonly lines: readonly TransparencyLine[];
}

/**
 * metricsPresentation maps one aggregate to its live region and rows
 * in fixed order: title, sealed window, supply, treasury,
 * circulation, R4, crumbs, reflux index. Empty rows never render, so
 * a suppressed line is omitted instead of shown blank. Values pass
 * through unchanged: hostile text stays literal because the element
 * writes it with textContent.
 */
export function metricsPresentation(text: TransparencyMetricsText): TransparencyExhibit {
  const rows = [
    text.title,
    text.window,
    text.supply,
    text.treasury,
    text.circulation,
    text.r4,
    text.crumbs,
    text.irr,
  ];
  const lines = rows.filter((row) => row !== "").map((row) => ({ text: row }));
  return { role: "status", lines };
}
