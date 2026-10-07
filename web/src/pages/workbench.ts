/**
 * Restricted moderation workbench presentation (P55-T02).
 *
 * Active moderators read the triage queue, claim one case under
 * a bounded lease and record one explicit decision — with the
 * role and the second factor revalidated on the server per
 * call. What lives in this module, DOM-free so the Node runner
 * verifies it without a browser, are the four things such a
 * console needs: the allowlisted reading of the lifecycle
 * filter, the translated view of one queue row with its claim
 * holder, the closed decision actions with the measure
 * traveling untouched, and the advisory signals with counts
 * only. Restricted evidence never serializes, so none renders;
 * a signal carries no score, no severity and no weight, and
 * nothing acts on one automatically — a human reviews and
 * decides.
 *
 * Two refusals shape this module. The frontend grants no admin
 * and no real power: without an active assignment the queue
 * answers forbidden, without a fresh second factor it answers
 * step-up, a held lease answers conflict and an expired lease
 * is reclaimed only by claiming again — never by editing a
 * lease the browser does not own. And every cursor travels
 * opaque while every amount-shaped count renders grouped but
 * never derived: basis points stay basis points, never a
 * percentage the server did not compute. Every string comes
 * from the catalog the runtime serves; the component that
 * renders a view translates nothing and formats nothing.
 */
import { formatInstant, formatNumber } from "../i18n/formats.js";
import type {
  AttributionSignal,
  AttributionSignals,
  ModerationCase,
  ModerationClaim,
  ModerationDecision,
} from "../contracts/generated.js";
import type { Locale } from "../i18n/locale.js";
import type { Translator } from "../i18n/translator.js";

/**
 * Lifecycle filter the contract declares for the queue.
 * Exported so the console and its tests share the one list.
 */
export const QUEUE_STATUSES: readonly string[] = ["open", "under_review", "decided", "closed"];

/**
 * Decision actions the contract declares. Severity is never
 * derived: the measure travels untouched. Exported so the
 * console and its tests share the one list.
 */
export const DECISION_ACTIONS: readonly string[] = [
  "no_action",
  "warning",
  "link_hide",
  "interaction_limit",
  "argument_remove",
  "arena_close",
  "attribution_invalidate",
  "position_invalidate",
  "suspension",
  "ban",
  "preserve_legal",
];

/** Signal kinds the restricted surface declares. */
const SIGNAL_KINDS: readonly string[] = ["reciprocity", "concentration", "rapid_alternation"];

/** Page size the contract sizes: 1..100, defaulting to 20. */
const QUEUE_LIMIT_MIN = 1;
const QUEUE_LIMIT_MAX = 100;

/** Everything the console renders for one queue row. */
export interface QueueRowView {
  readonly line: string;
  readonly holder: string | null;
}

/** Everything the console renders for one claim answer. */
export interface ClaimView {
  readonly line: string;
}

/** Everything the console renders for one recorded decision. */
export interface DecisionView {
  readonly line: string;
}

/** Everything the console renders for one advisory signal. */
export interface SignalView {
  readonly line: string;
}

/**
 * queueStatus keeps the lifecycle filter only inside the
 * contract's vocabulary. Anything else is dropped instead of
 * sent to fail: the canonical address holds what the server
 * accepts, and the empty filter lists every lifecycle.
 */
export function queueStatus(value: string | null): string | undefined {
  if (value === null || value === "") {
    return undefined;
  }
  return QUEUE_STATUSES.includes(value) ? value : undefined;
}

/**
 * queueLimit keeps the page size only inside the page the
 * contract sizes. Anything else is dropped instead of sent to
 * fail, and the default page applies.
 */
export function queueLimit(value: string | null): number | undefined {
  if (value === null || value === "") {
    return undefined;
  }
  const parsed = Number.parseInt(value, 10);
  if (!Number.isInteger(parsed) || parsed < QUEUE_LIMIT_MIN || parsed > QUEUE_LIMIT_MAX) {
    return undefined;
  }
  return parsed;
}

/**
 * queueCursor keeps the opaque cursor only when present. It is
 * never parsed and never built — only passed back.
 */
export function queueCursor(value: string | null): string | undefined {
  if (value === null || value === "") {
    return undefined;
  }
  return value;
}

/**
 * queueRowView projects one triage row for one locale. Target
 * kind, lifecycle and priority stay verbatim — the server's
 * stable routing vocabulary — with the instant rendered; the
 * claim holder travels beside the sentence when the server
 * names one.
 */
export function queueRowView(translator: Translator, locale: Locale, row: ModerationCase): QueueRowView {
  return {
    line: translator.translate("moderation.workbench.row", {
      target: row.target_type,
      status: row.status,
      priority: row.priority,
      instant: formatInstant(locale, row.created_at, { dateStyle: "medium", timeStyle: "short" }),
    }),
    holder: row.claimed_by ?? null,
  };
}

/**
 * mergeQueueRows keeps one queue page honest across reloads and
 * late answers: rows the console already holds keep their
 * place, newcomers append once, and duplicates collapse on the
 * stable case identifier.
 */
export function mergeQueueRows(
  existing: readonly ModerationCase[],
  incoming: readonly ModerationCase[],
): readonly ModerationCase[] {
  const seen = new Set(existing.map((row) => row.case_id));
  const merged: ModerationCase[] = [...existing];
  for (const row of incoming) {
    if (!seen.has(row.case_id)) {
      seen.add(row.case_id);
      merged.push(row);
    }
  }
  return merged;
}

/**
 * claimView projects one claim answer. The holder and the
 * lifecycle render verbatim; the lease itself stays where it
 * belongs — on the server, bounded and reclaimable only by
 * claiming again.
 */
export function claimView(translator: Translator, claim: ModerationClaim): ClaimView {
  return {
    line: translator.translate("moderation.workbench.claimed", {
      holder: claim.claimed_by,
      status: claim.status,
    }),
  };
}

/**
 * isDecisionAction keeps the sanction only inside the closed
 * actions. Anything else is dropped instead of sent to fail:
 * the measure travels untouched or not at all.
 */
export function isDecisionAction(value: string): boolean {
  return DECISION_ACTIONS.includes(value);
}

/**
 * decisionView projects one recorded decision. The action
 * renders verbatim with the recorded identifier; the
 * justification never serializes back, so it never renders.
 */
export function decisionView(translator: Translator, decision: ModerationDecision): DecisionView {
  return {
    line: translator.translate("moderation.workbench.decided", {
      action: decision.action,
      id: decision.action_id,
    }),
  };
}

/**
 * signalView projects one advisory signal for one locale. Kind
 * and counterpart stay verbatim with the kind-specific counts
 * the thresholds crossed — grouped integers, never derived —
 * and the counts that do not apply to the kind stay absent.
 * No score, no severity and no weight ever renders, because
 * none ever serializes.
 */
export function signalView(translator: Translator, locale: Locale, signal: AttributionSignal): SignalView {
  const counts: string[] = [];
  if (signal.mutual_events !== undefined) {
    counts.push(formatNumber(locale, signal.mutual_events));
  }
  if (signal.dominant_events !== undefined) {
    counts.push(formatNumber(locale, signal.dominant_events));
  }
  if (signal.share_basis_points !== undefined) {
    counts.push(formatNumber(locale, signal.share_basis_points));
  }
  if (signal.changes !== undefined) {
    counts.push(formatNumber(locale, signal.changes));
  }
  if (signal.reversals !== undefined) {
    counts.push(formatNumber(locale, signal.reversals));
  }
  return {
    line: translator.translate("moderation.workbench.signal", {
      kind: signal.kind,
      counterpart: signal.counterpart_id,
      counts: counts.join(", "),
    }),
  };
}

/**
 * signalsHead projects the assessment envelope: policy
 * revision, window and reading instant. Signals are
 * prevention data for the case at hand — never for a public
 * page, a metric or an export.
 */
export function signalsHead(translator: Translator, locale: Locale, assessment: AttributionSignals): string {
  return translator.translate("moderation.workbench.signals_head", {
    policy: assessment.policy_version,
    window: formatNumber(locale, assessment.window_seconds),
    instant: formatInstant(locale, assessment.checked_at, { dateStyle: "medium", timeStyle: "short" }),
  });
}

/**
 * isSignalKind keeps advisory rendering inside the declared
 * kinds. Anything else never reaches a sentence.
 */
export function isSignalKind(value: string): boolean {
  return SIGNAL_KINDS.includes(value);
}

/**
 * workbenchFailure translates a refusal by the server code the
 * backend really emits. Owner without a role and moderator
 * without step-up are denied without revealing which half
 * failed beyond their own sentence; a held lease and a second
 * decision conflict without a false success. Anything else
 * falls back to the generic sentence instead of inventing a
 * meaning.
 */
export function workbenchFailure(translator: Translator, serverCode: string): string {
  switch (serverCode) {
    case "invalid_request":
    case "invalid_body":
    case "invalid_limit":
    case "body_too_large":
      return translator.translate("moderation.failure.invalid");
    case "unauthorized":
      return translator.translate("moderation.failure.unauthorized");
    case "forbidden":
      return translator.translate("moderation.failure.forbidden");
    case "step_up_required":
    case "mfa_step_up_required":
      return translator.translate("moderation.failure.step_up");
    case "not_found":
      return translator.translate("moderation.failure.missing");
    case "conflict":
      return translator.translate("moderation.failure.conflict");
    default:
      return translator.translate("moderation.failure.generic");
  }
}
