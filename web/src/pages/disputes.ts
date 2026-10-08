/**
 * Disputes staged presentation (P57-T04, harness-only).
 *
 * The five private case operations show one owned file — version,
 * consent, deadline, authorized proofs and notices — and its stable
 * ruling with the single previsto recurso. What lives in this module,
 * DOM-free so the Node runner verifies it without a browser, are the
 * translated views over the staged documents plus the guards that
 * keep the browser honest: the file reuses `disputePresentation`
 * (the `disputes/case` exhibit the component already mounts),
 * defense digests and appeal reasons are counted and carried but
 * never echoed, the ruling renders the stable verdict with the
 * capped award the server sealed, and a second appeal answers 409
 * instead of reopening anything.
 *
 * This is the private rite between named parties, not the P55
 * moderation console: no report is filed here, no queue is claimed
 * and no platform sanction is decided — and no AI judges either.
 * The page reads what the rite recorded; it never classifies
 * evidence, never binds a verdict to terms and never infers a
 * judgment. Dates render through `Intl` in UTC and amounts are
 * integers that never pass through binary floating point. The
 * surface stays staged: the production composition enables no
 * capability, so the gate renders the honest unavailability view and
 * the page sends nothing. Enabling the capability still grants no
 * authorization: the server decides authentication, party, deadline
 * and duplication on every call. Every string comes from the catalog
 * the runtime serves; the component that renders a view translates
 * nothing and formats nothing itself.
 */
import { disputePresentation } from "../components/disputes/model.js";
import type { DisputeExhibit, DisputeExhibitState } from "../components/disputes/model.js";
import type { PrivateCaseFile, PrivateCaseRuling } from "../contracts/staged/disputes.js";
import { isStagedEnabled } from "../core/staged.js";
import type { StagedCapabilities } from "../core/staged.js";
import { formatInstant, formatNumber, instantOf } from "../i18n/formats.js";
import type { Locale } from "../i18n/locale.js";
import type { Translator } from "../i18n/translator.js";
import { requireStagedEnabled, stagedUnavailableView } from "./staged.js";
import type { StagedUnavailableView } from "./staged.js";

/** Notice events the fragment declares for one case file. */
export const CASE_NOTICE_EVENTS: readonly string[] = ["proposal", "accept", "defense", "ruling", "appeal"];

/**
 * caseExhibitState maps one file to its exhibit wiring: appealed files
 * interrupt, decided files interrupt, open files wait, and anything
 * else — including the sealed proposal — waits as proposed. The
 * mapping mirrors the `disputes/case` component's own default, so an
 * unknown status never invents a fifth wiring.
 */
export function caseExhibitState(file: PrivateCaseFile): DisputeExhibitState {
  if (file.appealed) {
    return "appealed";
  }
  if (file.verdict !== undefined) {
    return "decided";
  }
  if (file.status === "open") {
    return "open";
  }
  return "proposed";
}

/**
 * noticeEvent keeps the lifecycle event only inside the closed
 * vocabulary. Anything else is dropped instead of rendered, so the
 * page never invents a sixth lifecycle step.
 */
export function noticeEvent(value: string): string | null {
  return (CASE_NOTICE_EVENTS as readonly string[]).includes(value) ? value : null;
}

/**
 * isAppealLive answers whether `now` is still before the appeal
 * deadline. Reaching it ends the recurso: the browser never extends
 * it and a late appeal answers 409. An unparseable instant throws
 * instead of rendering a guessed window.
 */
export function isAppealLive(appealDueAt: string, now: Date | number): boolean {
  const due = instantOf(appealDueAt).getTime();
  const at = now instanceof Date ? now.getTime() : now;
  return at < due;
}

/**
 * isAppealReason keeps the recurso only when it states something: an
 * empty reason is dropped instead of sent, and the length travels
 * untouched — the fragment declares no bound, so the page invents
 * none.
 */
export function isAppealReason(value: string): boolean {
  return value.trim() !== "";
}

/**
 * isDefenseDigest keeps the defense exhibit only when it carries a
 * digest: an empty digest is dropped instead of sent, and the digest
 * itself is never rendered back — counts prove the defenses.
 */
export function isDefenseDigest(value: string): boolean {
  return value.trim() !== "";
}

/** Everything the page renders for one owned case file. */
export interface PrivateCaseFileView {
  readonly heading: string;
  readonly proposal: string;
  readonly consent: string;
  readonly defenses: string;
  readonly expires: string;
  readonly decided: string | null;
  readonly appealDue: string | null;
  readonly notices: readonly string[];
  readonly exhibit: DisputeExhibit;
  readonly acceptAction: string;
  readonly appealAction: string;
}

/** Everything the page renders for one stable ruling. */
export interface PrivateCaseRulingView {
  readonly heading: string;
  readonly verdict: string;
  readonly award: string;
  readonly decided: string;
  readonly appealDue: string;
  readonly appealed: boolean;
  /** Terms hash binding the verdict to the sealed terms, verbatim. */
  readonly termsHash: string;
}

/** caseFileView projects one owned file for one locale in UTC. */
export function caseFileView(
  translator: Translator,
  locale: Locale,
  file: PrivateCaseFile,
  now: Date | number,
): PrivateCaseFileView {
  const state = caseExhibitState(file);
  const parties = translator.translate("disputes.case.accepted", {
    count: formatNumber(locale, file.accepts),
  });
  const decided =
    file.decided_at === undefined
      ? translator.translate("disputes.case.expires", {
          date: formatInstant(locale, file.expires_at, {
            dateStyle: "medium",
            timeStyle: "short",
            timeZone: "UTC",
          }),
        })
      : translator.translate("disputes.case.decided", {
          date: formatInstant(locale, file.decided_at, {
            dateStyle: "medium",
            timeStyle: "short",
            timeZone: "UTC",
          }),
        });
  const action =
    state === "decided" && file.appeal_due_at !== undefined && isAppealLive(file.appeal_due_at, now)
      ? translator.translate("disputes.case.appeal_action")
      : state === "proposed"
        ? translator.translate("disputes.case.accept_action")
        : null;
  const exhibit = disputePresentation(state, {
    title: file.title,
    proposal: translator.translate("disputes.case.proposal", {
      key: file.key,
      version: formatNumber(locale, file.version),
    }),
    parties,
    decided,
    action,
  });
  return {
    heading: translator.translate("disputes.case.title"),
    proposal: translator.translate("disputes.case.proposal", {
      key: file.key,
      version: formatNumber(locale, file.version),
    }),
    consent: parties,
    defenses: translator.translate("disputes.case.defenses", {
      count: formatNumber(locale, file.defenses),
    }),
    expires: translator.translate("disputes.case.expires", {
      date: formatInstant(locale, file.expires_at, {
        dateStyle: "medium",
        timeStyle: "short",
        timeZone: "UTC",
      }),
    }),
    decided: file.decided_at === undefined ? null : decided,
    appealDue:
      file.appeal_due_at === undefined
        ? null
        : translator.translate("disputes.case.appeal_due", {
            date: formatInstant(locale, file.appeal_due_at, {
              dateStyle: "medium",
              timeStyle: "short",
              timeZone: "UTC",
            }),
          }),
    notices: file.notices.flatMap((notice) => {
      const event = noticeEvent(notice.event);
      if (event === null) {
        return [];
      }
      const label =
        event === "proposal"
          ? translator.translate("disputes.notice.proposal")
          : event === "accept"
            ? translator.translate("disputes.notice.accept")
            : event === "defense"
              ? translator.translate("disputes.notice.defense")
              : event === "ruling"
                ? translator.translate("disputes.notice.ruling")
                : translator.translate("disputes.notice.appeal");
      return [
        translator.translate("disputes.notice.line", { event: label, title: notice.title }),
      ];
    }),
    exhibit,
    acceptAction: translator.translate("disputes.case.accept_action"),
    appealAction: translator.translate("disputes.case.appeal_action"),
  };
}

/** rulingView projects one stable ruling for one locale in UTC. */
export function rulingView(
  translator: Translator,
  locale: Locale,
  ruling: PrivateCaseRuling,
): PrivateCaseRulingView {
  return {
    heading: translator.translate("disputes.ruling.title"),
    verdict: translator.translate("disputes.ruling.verdict", { code: ruling.verdict }),
    award: formatNumber(locale, assertSafeAward(ruling.award_milli)),
    decided: translator.translate("disputes.case.decided", {
      date: formatInstant(locale, ruling.decided_at, {
        dateStyle: "medium",
        timeStyle: "short",
        timeZone: "UTC",
      }),
    }),
    appealDue: translator.translate("disputes.case.appeal_due", {
      date: formatInstant(locale, ruling.appeal_due_at, {
        dateStyle: "medium",
        timeStyle: "short",
        timeZone: "UTC",
      }),
    }),
    appealed: ruling.appealed,
    termsHash: ruling.terms_hash,
  };
}

/**
 * assertSafeAward refuses anything that is not an exact integer
 * amount. JSON carries every quantity as a `Number`, so a value
 * outside the safe range — or a fractional one — throws instead of
 * rendering a rounded award.
 */
export function assertSafeAward(value: number | bigint): number | bigint {
  if (typeof value === "bigint") {
    return value;
  }
  if (!Number.isSafeInteger(value)) {
    throw new TypeError(`award thousandths must be a safe integer, received ${String(value)}`);
  }
  return value;
}

/**
 * disputesFailure translates a refusal by the server code the backend
 * really emits. A missing session refuses with `unauthorized`, a
 * foreign or unknown case with `case_unknown`, a stranger's move with
 * `case_forbidden`, a past-deadline or duplicate move with
 * `case_conflict` and a malformed request with `case_invalid`.
 * Anything else falls back to the generic sentence instead of
 * inventing a meaning.
 */
export function disputesFailure(translator: Translator, serverCode: string): string {
  switch (serverCode) {
    case "unauthorized":
      return translator.translate("disputes.failure.unauthorized");
    case "case_unknown":
      return translator.translate("disputes.failure.missing");
    case "case_forbidden":
      return translator.translate("disputes.failure.forbidden");
    case "case_conflict":
      return translator.translate("disputes.failure.conflict");
    case "case_invalid":
    case "invalid_json":
      return translator.translate("disputes.failure.invalid");
    default:
      return translator.translate("disputes.failure.generic");
  }
}

/**
 * disputesGate answers the staged disputes page: an enabled capability
 * returns no view and the caller may read, a disabled one returns the
 * honest unavailability view and the caller sends nothing. The gate
 * reads only the capabilities value — never the DOM, the URL or
 * storage.
 */
export function disputesGate(
  capabilities: StagedCapabilities,
  translator: Translator,
): { readonly enabled: boolean; readonly view: StagedUnavailableView | null } {
  if (isStagedEnabled(capabilities, "disputes")) {
    return { enabled: true, view: null };
  }
  return { enabled: false, view: stagedUnavailableView(translator, "disputes") };
}

/**
 * requireDisputesEnabled guards every disputes call: a disabled page
 * throws instead of sending. The throw carries the feature so the
 * caller renders the same unavailability view it would have rendered
 * without calling.
 */
export function requireDisputesEnabled(capabilities: StagedCapabilities): void {
  requireStagedEnabled(capabilities, "disputes");
}
