/**
 * Presentation of the realm vocabulary (P39-T03).
 *
 * Regnovum is the single Kingdom; an Arena is one debate instance
 * with no winner and no official truth. The element is a thin DOM
 * adapter over this module, which is why the row order and the text
 * wiring can be verified on Node without a browser: every dynamic
 * value arrives as an already translated string and reaches the
 * document through `textContent` only.
 */

/** The strings one realm exhibit renders, already translated. */
export interface RealmText {
  readonly kingdom: string;
  readonly arena: string;
  readonly note: string;
}

/** One rendered row. */
export interface RealmLine {
  readonly text: string;
}

/** The wiring of one realm exhibit: a polite live region plus rows. */
export interface RealmExhibit {
  readonly role: "status";
  readonly lines: readonly RealmLine[];
}

/**
 * realmPresentation maps the realm vocabulary to its live region and
 * rows in fixed order: kingdom, arena instance, outcome note. Empty
 * rows never render. Values pass through unchanged: hostile text
 * stays literal because the element writes it with textContent.
 */
export function realmPresentation(text: RealmText): RealmExhibit {
  const rows = [text.kingdom, text.arena, text.note];
  const lines = rows.filter((row) => row !== "").map((row) => ({ text: row }));
  return { role: "status", lines };
}
