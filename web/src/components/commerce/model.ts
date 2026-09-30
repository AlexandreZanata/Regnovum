/**
 * Presentation of one staged trade receipt (P37-T07).
 *
 * The element is a thin DOM adapter over this module, which is why the
 * vocabulary of states and the text wiring can be verified on Node
 * without a browser: every dynamic value arrives as an already
 * translated string (server-rendered or translator-composed) and
 * reaches the document through `textContent` only. Amounts travel as
 * canonical integers until the translator formats them, so a
 * translation never enters a computation.
 */

/** The exhibit states of one trade receipt. */
export type TradeExhibitState = "pending" | "settled" | "failed";

/** The strings one trade exhibit renders, already translated. */
export interface TradeExhibitText {
  readonly title: string;
  readonly gross: string;
  readonly tithe: string;
  readonly net: string;
  readonly posted: string;
  readonly action: string | null;
}

/** One rendered line with its live-region behavior. */
export interface TradeLine {
  readonly text: string;
  readonly alert: boolean;
}

/** The wiring of one trade exhibit: live region plus lines. */
export interface TradeExhibit {
  readonly role: "status" | "alert";
  readonly lines: readonly TradeLine[];
}

/**
 * tradePresentation maps one trade state to its live region and
 * lines. Failures interrupt (`alert`); pending and settled receipts
 * wait their turn (`status`). Values pass through unchanged: hostile
 * text stays literal because the element writes it with textContent.
 */
export function tradePresentation(
  state: TradeExhibitState,
  text: TradeExhibitText,
): TradeExhibit {
  const lines: TradeLine[] = [
    { text: text.title, alert: false },
    { text: text.gross, alert: false },
    { text: text.tithe, alert: false },
    { text: text.net, alert: false },
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
