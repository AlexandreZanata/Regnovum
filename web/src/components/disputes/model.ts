/**
 * Presentation of one staged private case file (P39-T08).
 *
 * The element is a thin DOM adapter over this module, which is why
 * the vocabulary of states and the text wiring can be verified on
 * Node without a browser: every dynamic value arrives as an already
 * translated string (server-rendered or translator-composed) and
 * reaches the document through `textContent` only. Versions travel
 * as canonical text until the translator formats them, so a
 * translation never enters a computation. Proof digests and grounds
 * never reach this layer: counts and codes prove the file.
 */

/** The exhibit states of one private case file. */
export type DisputeExhibitState =
  | "proposed"
  | "open"
  | "decided"
  | "appealed";

/** The strings one dispute exhibit renders, already translated. */
export interface DisputeExhibitText {
  readonly title: string;
  readonly proposal: string;
  readonly parties: string;
  readonly decided: string;
  readonly action: string | null;
}

/** One rendered line with its live-region behavior. */
export interface DisputeLine {
  readonly text: string;
  readonly alert: boolean;
}

/** The wiring of one dispute exhibit: live region plus lines. */
export interface DisputeExhibit {
  readonly role: "status" | "alert";
  readonly lines: readonly DisputeLine[];
}

/**
 * disputePresentation maps one case state to its live region and
 * lines. Decided and appealed files interrupt (`alert`): the rite
 * produced an outcome the parties must not miss. Proposed and open
 * files wait their turn (`status`). Values pass through unchanged:
 * hostile text stays literal because the element writes it with
 * textContent.
 */
export function disputePresentation(
  state: DisputeExhibitState,
  text: DisputeExhibitText,
): DisputeExhibit {
  const lines: DisputeLine[] = [
    { text: text.title, alert: false },
    { text: text.proposal, alert: false },
    { text: text.parties, alert: false },
    { text: text.decided, alert: false },
  ];
  if (state === "decided" || state === "appealed") {
    const flagged = lines.map((line) => ({ text: line.text, alert: true }));
    if (text.action !== null) {
      return { role: "alert", lines: [...flagged, { text: text.action, alert: true }] };
    }
    return { role: "alert", lines: flagged };
  }
  if (text.action !== null) {
    return { role: "status", lines: [...lines, { text: text.action, alert: false }] };
  }
  return { role: "status", lines };
}
