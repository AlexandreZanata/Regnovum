/**
 * MFA page presentation (P51-T03).
 *
 * The page walks the owner through enrollment, confirmation, step-up
 * and recovery, but it keeps almost nothing: the server owns the
 * pending secret, the spent steps and every refusal. What lives here,
 * DOM-free so the Node runner verifies it without a browser, are the
 * translated views each server answer projects — the enrollment shows
 * the secret of the begin answer, the confirmation shows the recovery
 * codes of the confirm answer, and neither value exists anywhere else:
 * no log, no cache, no persistence, and no second rendering after the
 * answer leaves the screen.
 *
 * The code field carries the keyboard the task requires — numeric entry
 * with the one-time-code autocomplete — without inventing any rule the
 * contract does not declare: no length, no alphabet, no format is
 * asserted here. Failures name only the server codes the backend
 * really emits; anything else falls back to the generic sentence.
 * Step-up elevates the calling session and nothing more: the notice
 * says which session was elevated and grants no role.
 */
import type {
  MFAConfirmResponse,
  MFAEnrollmentResponse,
} from "../contracts/generated.js";
import type { Translator } from "../i18n/translator.js";

/** Everything the page renders for a second factor code field. */
export interface CodeFieldView {
  readonly label: string;
  readonly hint: string;
  /** Numeric entry, without asserting any length or alphabet. */
  readonly inputMode: "numeric";
  /** Lets the platform offer the code it just received. */
  readonly autoComplete: "one-time-code";
}

/** The single code field every MFA step shares. */
export function codeField(translator: Translator): CodeFieldView {
  return {
    label: translator.translate("auth.mfa.code_label"),
    hint: translator.translate("auth.mfa.code_hint"),
    inputMode: "numeric",
    autoComplete: "one-time-code",
  };
}

/** Everything the page renders for the pending enrollment. */
export interface EnrollmentView {
  readonly heading: string;
  readonly intro: string;
  readonly beginNote: string;
  readonly secretLabel: string;
  /** The secret of the begin answer, shown exactly once. */
  readonly secret: string;
  readonly uriNote: string;
  /** The setup address of the begin answer, shown exactly once. */
  readonly uri: string;
  readonly field: CodeFieldView;
  readonly submit: string;
}

/**
 * enrollmentPresentation projects the begin answer for one translator.
 * The secret and the URI travel from that answer alone: this projection
 * stores nothing and offers no second reading.
 */
export function enrollmentPresentation(
  translator: Translator,
  enrollment: MFAEnrollmentResponse,
): EnrollmentView {
  return {
    heading: translator.translate("auth.mfa.heading"),
    intro: translator.translate("auth.mfa.intro"),
    beginNote: translator.translate("auth.mfa.begin_note"),
    secretLabel: translator.translate("auth.mfa.secret_label"),
    secret: enrollment.secret,
    uriNote: translator.translate("auth.mfa.uri_note"),
    uri: enrollment.uri,
    field: codeField(translator),
    submit: translator.translate("auth.mfa.confirm_submit"),
  };
}

/** Everything the page renders for the confirmed enrollment. */
export interface ConfirmationView {
  readonly heading: string;
  /** The codes of the confirm answer, shown exactly once. */
  readonly codes: readonly string[];
  readonly onceNote: string;
  readonly field: CodeFieldView;
  readonly stepUpNote: string;
  readonly stepUpSubmit: string;
  readonly recoverNote: string;
  readonly recoverSubmit: string;
}

/**
 * confirmationPresentation projects the confirm answer for one
 * translator. The recovery codes travel from that answer alone: this
 * projection stores nothing and offers no second reading.
 */
export function confirmationPresentation(
  translator: Translator,
  confirmation: MFAConfirmResponse,
): ConfirmationView {
  return {
    heading: translator.translate("auth.mfa.backup_heading"),
    codes: confirmation.backup_codes,
    onceNote: translator.translate("auth.mfa.backup_note"),
    field: codeField(translator),
    stepUpNote: translator.translate("auth.mfa.stepup_note"),
    stepUpSubmit: translator.translate("auth.mfa.stepup_submit"),
    recoverNote: translator.translate("auth.mfa.recover_note"),
    recoverSubmit: translator.translate("auth.mfa.recover_submit"),
  };
}

/**
 * elevationNotice is the sentence the page shows after the server
 * confirms an elevation: which path elevated the calling session, and
 * nothing the server did not say — no role, no other account.
 */
export function elevationNotice(translator: Translator, kind: "step-up" | "recovery"): string {
  return translator.translate(kind === "step-up" ? "auth.mfa.elevated_stepup" : "auth.mfa.elevated_recovery");
}

/**
 * mfaFailure translates a refusal by the server code the backend really
 * emits. Codes the backend never emits are not named here: they fall
 * back to the generic sentence instead of inventing a meaning.
 */
export function mfaFailure(translator: Translator, serverCode: string): string {
  switch (serverCode) {
    case "mfa_code_invalid":
      return translator.translate("auth.mfa.failure_invalid");
    case "mfa_code_replayed":
      return translator.translate("auth.mfa.failure_replayed");
    case "mfa_enrollment_missing":
      return translator.translate("auth.mfa.failure_missing");
    case "mfa_already_enrolled":
      return translator.translate("auth.mfa.failure_enrolled");
    case "mfa_not_enrolled":
      return translator.translate("auth.mfa.failure_not_enrolled");
    case "mfa_step_up_required":
      return translator.translate("auth.mfa.failure_step_required");
    default:
      return translator.translate("auth.mfa.failure_generic");
  }
}
