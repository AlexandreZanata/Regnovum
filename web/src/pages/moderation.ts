/**
 * Moderation report and appeal presentation (P55-T01).
 *
 * The account files two structured petitions and renders
 * nothing it was not given. `reportReceipt` projects the filed
 * report identity — the opaque identifier plus the replay flag
 * the server resolved — and `appealReceipt` projects the filed
 * appeal identity with the action it contests. Reporter and
 * appellant contexts never serialize back, so none render
 * either: the receipt carries the real identifier and nothing
 * sensitive.
 *
 * Three refusals shape this module. The reason and target
 * vocabularies are the contract's, not the page's: eleven
 * closed reasons over three target kinds, and anything outside
 * them is dropped instead of sent to fail. A repeated filing
 * resolves the recorded identity instead of writing again, and
 * a double submit is answered by reading the receipt — never
 * by an automatic retry the core would have to invent a key
 * for. And no private tribunal is exposed: there is no list,
 * no state and no queue here, only the filing and its receipt.
 * Every string comes from the catalog the runtime serves; the
 * component that renders a view translates nothing and formats
 * nothing.
 */
import type { ModerationAppeal, ModerationReport } from "../contracts/generated.js";
import type { Translator } from "../i18n/translator.js";

/**
 * Target kinds the contract declares for a report. Exported so
 * later moderation surfaces share the one vocabulary.
 */
export const REPORT_TARGETS: readonly string[] = ["arena", "argument", "profile"];

/**
 * Closed reason vocabulary the contract declares for a report.
 * Exported so later moderation surfaces share the one list.
 */
export const REPORT_REASONS: readonly string[] = [
  "violence",
  "doxxing",
  "harassment",
  "sexual",
  "fraud",
  "spam",
  "illegal",
  "evasion",
  "multiaccount",
  "malware",
  "other",
];

/** Everything the page renders for one filed report. */
export interface ReportReceipt {
  readonly heading: string;
  readonly intro: string;
  readonly filed: string;
  readonly rateNote: string | null;
}

/** Everything the page renders for one filed appeal. */
export interface AppealReceipt {
  readonly heading: string;
  readonly intro: string;
  readonly filed: string;
}

/**
 * isReportTarget keeps the target kind only inside the closed
 * vocabulary. Anything else is dropped instead of sent to fail.
 */
export function isReportTarget(value: string): boolean {
  return REPORT_TARGETS.includes(value);
}

/**
 * isReportReason keeps the reason only inside the closed
 * vocabulary. Anything else is dropped instead of sent to fail.
 */
export function isReportReason(value: string): boolean {
  return REPORT_REASONS.includes(value);
}

/**
 * reportReceipt projects one filed report. A replay renders the
 * recorded identity with the replay sentence — the server's
 * resolution, not a second write — and a rate signal renders
 * its warning beside it.
 */
export function reportReceipt(translator: Translator, report: ModerationReport): ReportReceipt {
  return {
    heading: translator.translate("moderation.report.heading"),
    intro: translator.translate("moderation.report.intro"),
    filed: translator.translate(report.replayed ? "moderation.report.replayed" : "moderation.report.filed", {
      id: report.report_id,
    }),
    rateNote: report.rate_limited ? translator.translate("moderation.report.rate_note") : null,
  };
}

/**
 * appealReceipt projects one filed appeal. A replay renders the
 * recorded identity with the replay sentence — exactly one
 * appeal still contests the one action.
 */
export function appealReceipt(translator: Translator, appeal: ModerationAppeal): AppealReceipt {
  return {
    heading: translator.translate("moderation.appeal.heading"),
    intro: translator.translate("moderation.appeal.intro"),
    filed: translator.translate(appeal.replayed ? "moderation.appeal.replayed" : "moderation.appeal.filed", {
      id: appeal.appeal_id,
    }),
  };
}

/**
 * moderationFailure translates a refusal by the server code the
 * backend really emits. Denials name the rule, never the
 * content: restricted evidence is not reflected. Anything else
 * falls back to the generic sentence instead of inventing a
 * meaning.
 */
export function moderationFailure(translator: Translator, serverCode: string): string {
  switch (serverCode) {
    case "invalid_request":
    case "invalid_body":
    case "body_too_large":
      return translator.translate("moderation.failure.invalid");
    case "unauthorized":
      return translator.translate("moderation.failure.unauthorized");
    case "forbidden":
      return translator.translate("moderation.failure.forbidden");
    case "step_up_required":
      return translator.translate("moderation.failure.step_up");
    case "not_found":
      return translator.translate("moderation.failure.missing");
    case "conflict":
      return translator.translate("moderation.failure.conflict");
    default:
      return translator.translate("moderation.failure.generic");
  }
}
