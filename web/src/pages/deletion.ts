/**
 * Deletion page presentation (P51-T05).
 *
 * The page walks the holder from the consequences to the confirmed
 * state, but it decides almost nothing: the server owns the request,
 * its window and every refusal. What lives here, DOM-free so the Node
 * runner verifies it without a browser, are the translated views each
 * step projects — the consequences the holder confirms explicitly, the
 * record the server confirmed, and the failures the backend really
 * emits.
 *
 * The consequences below restate the rule the contract declares: seven
 * days of cooling off, private rows removed, the stable identifier
 * kept with an opaque email that never authenticates again, retention
 * records preserved, public content intact with an unresolvable
 * author. This is the holder's own privacy request — not a sanction,
 * not an Inquisition — and it erases no obligation and touches no
 * right of appeal. The confirmation names those consequences and the
 * state after every mutation is re-read from the server, never
 * assumed: only the statuses the contract declares are rendered.
 */
import { formatInstant } from "../i18n/formats.js";
import type { AccountDeletionRequest } from "../contracts/generated.js";
import type { Locale } from "../i18n/locale.js";
import type { Translator } from "../i18n/translator.js";

/** Everything the page renders before any request exists. */
export interface DeletionRequestView {
  readonly heading: string;
  readonly intro: string;
  readonly consequences: readonly string[];
  readonly confirm: string;
  readonly submit: string;
}

/** deletionRequestView projects the consequences for one translator. */
export function deletionRequestView(translator: Translator): DeletionRequestView {
  return {
    heading: translator.translate("auth.deletion.heading"),
    intro: translator.translate("auth.deletion.intro"),
    consequences: [
      translator.translate("auth.deletion.cooling"),
      translator.translate("auth.deletion.removed"),
      translator.translate("auth.deletion.placeholder"),
      translator.translate("auth.deletion.preserved"),
      translator.translate("auth.deletion.public_intact"),
      translator.translate("auth.deletion.not_sanction"),
    ],
    confirm: translator.translate("auth.deletion.confirm"),
    submit: translator.translate("auth.deletion.request_submit"),
  };
}

/** What the page renders for a server-confirmed record. */
export type DeletionStatusView =
  | {
      readonly state: "requested";
      readonly status: string;
      readonly requestedAt: string;
      readonly cancelNote: string;
      readonly cancelSubmit: string;
    }
  | { readonly state: "canceled"; readonly status: string }
  | { readonly state: "executed"; readonly status: string };

/**
 * deletionStatusView projects one server record for one locale. Only
 * the statuses the contract declares are named; anything else is a
 * programming error, never a fourth state the page renders.
 */
export function deletionStatusView(
  translator: Translator,
  locale: Locale,
  record: AccountDeletionRequest,
): DeletionStatusView {
  if (record.status === "canceled") {
    return { state: "canceled", status: translator.translate("auth.deletion.status_canceled") };
  }
  if (record.status === "executed") {
    return { state: "executed", status: translator.translate("auth.deletion.status_executed") };
  }
  return {
    state: "requested",
    status: translator.translate("auth.deletion.status_requested"),
    requestedAt: translator.translate("auth.deletion.requested_at", {
      instant: formatInstant(locale, record.requested_at, { dateStyle: "medium", timeStyle: "short" }),
    }),
    cancelNote: translator.translate("auth.deletion.cancel_note"),
    cancelSubmit: translator.translate("auth.deletion.cancel_submit"),
  };
}

/**
 * deletionFailure translates a refusal by the server code the backend
 * really emits. Codes the backend never emits are not named here: they
 * fall back to the generic sentence instead of inventing a meaning.
 */
export function deletionFailure(translator: Translator, serverCode: string): string {
  switch (serverCode) {
    case "deletion_request_not_found":
      return translator.translate("auth.deletion.failure_missing");
    case "deletion_not_cancellable":
      return translator.translate("auth.deletion.failure_terminal");
    case "account_not_eligible":
      return translator.translate("auth.deletion.failure_ineligible");
    default:
      return translator.translate("auth.deletion.failure_generic");
  }
}
