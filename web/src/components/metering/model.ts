/**
 * Presentation of one metering quote or receipt (P36-T08).
 *
 * The element is a thin DOM adapter over this module, which is why the
 * Vocabulary of states and the text wiring can be verified on Node
 * without a browser: every dynamic value arrives as an already
 * translated string (server-rendered or translator-composed) and
 * reaches the document through `textContent` only. Amounts travel as
 * canonical integers until the translator formats them, so a
 * translation never enters a computation.
 */

/** The exhibit states of one metering quote. */
export type MeteringExhibitState = "preview" | "confirmed" | "failed";

/** The strings one metering exhibit renders, already translated. */
export interface MeteringExhibitText {
  readonly title: string;
  readonly total: string;
  readonly posted: string;
  readonly action: string | null;
}

/** One rendered line with its live-region behavior. */
export interface MeteringLine {
  readonly text: string;
  readonly alert: boolean;
}

/** The wiring of one metering exhibit: live region plus lines. */
export interface MeteringExhibit {
  readonly role: "status" | "alert";
  readonly lines: readonly MeteringLine[];
}

/**
 * exhibitPresentation maps one metering state to its live region and
 * lines. Failures interrupt (`alert`); previews and confirmations
 * wait their turn (`status`). Values pass through unchanged: hostile
 * text stays literal because the element writes it with textContent.
 */
export function exhibitPresentation(
  state: MeteringExhibitState,
  text: MeteringExhibitText,
): MeteringExhibit {
  const lines: MeteringLine[] = [
    { text: text.title, alert: false },
    { text: text.total, alert: false },
    { text: text.posted, alert: false },
  ];
  if (state === "failed") {
    return { role: "alert", lines: lines.map((line) => ({ text: line.text, alert: true })) };
  }
  if (text.action !== null) {
    return { role: "status", lines: [...lines, { text: text.action, alert: false }] };
  }
  return { role: "status", lines };
}
