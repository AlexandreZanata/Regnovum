/**
 * Sessions page presentation (P51-T02).
 *
 * The page lists the owner's sessions and lets the owner end one of them,
 * but it decides almost nothing: the server owns the list, the current
 * mark and every refusal. What lives here, DOM-free so the Node runner
 * verifies it without a browser, are the two things a rendering needs —
 * the translated view of one session row (allowlisted metadata only:
 * identifier, address, user agent and instants) and the revocation
 * decision (which target the confirmation names, and what ending the
 * current session means for the page that asked).
 *
 * No token or cookie ever reaches a view: identifiers are opaque, the
 * password travels only in the revocation request body the client sends,
 * and the page renders it nowhere. Ending the current session is not a
 * success the page celebrates — it is the end of the page's own
 * authority, answered by sending the person back to the sign-in entry
 * instead of looping on a session that no longer exists.
 */
import { formatInstant } from "../i18n/formats.js";
import type { SessionListResponse, SessionSummary } from "../contracts/generated.js";
import type { Locale } from "../i18n/locale.js";
import type { Translator } from "../i18n/translator.js";

/** Everything the page renders for one session row. */
export interface SessionRowView {
  /** Opaque identifier, never the token. Rendered so the confirmation names its target. */
  readonly id: string;
  /** "This session" or "Another session", from the server's current mark. */
  readonly kind: string;
  /** Whether this row is the session making the request. */
  readonly current: boolean;
  readonly lastSeen: string;
  readonly expiresAt: string;
  /** Client address the session recorded, or null when it recorded none. */
  readonly address: string | null;
  /** User agent the session presented, or null when it presented none. */
  readonly agent: string | null;
}

/** Everything the page renders for the session list. */
export interface SessionsView {
  readonly heading: string;
  readonly intro: string;
  readonly rows: readonly SessionRowView[];
  readonly empty: string | null;
}

/**
 * sessionRow projects one session for one locale. Only allowlisted
 * metadata travels: the contract carries no token, no hash and no device
 * identifier, and this projection adds none.
 */
export function sessionRow(
  translator: Translator,
  locale: Locale,
  session: SessionSummary,
): SessionRowView {
  return {
    id: session.id,
    kind: translator.translate(
      session.current ? "auth.sessions.current_label" : "auth.sessions.other_label",
    ),
    current: session.current,
    lastSeen: translator.translate("auth.sessions.last_seen", {
      instant: formatInstant(locale, session.last_seen_at, { dateStyle: "medium", timeStyle: "short" }),
    }),
    expiresAt: translator.translate("auth.sessions.expires_at", {
      instant: formatInstant(locale, session.expires_at, { dateStyle: "medium", timeStyle: "short" }),
    }),
    address: session.ip_address ?? null,
    agent: session.user_agent ?? null,
  };
}

/** sessionsPresentation projects the whole list for one locale. */
export function sessionsPresentation(
  translator: Translator,
  locale: Locale,
  list: SessionListResponse,
): SessionsView {
  const rows = list.sessions.map((session) => sessionRow(translator, locale, session));
  return {
    heading: translator.translate("auth.sessions.heading"),
    intro: translator.translate("auth.sessions.intro"),
    rows,
    empty: rows.length === 0 ? translator.translate("auth.sessions.empty") : null,
  };
}

/** What ending one session means for the page that asked. */
export type RevocationOutcome =
  | { readonly ended: "other"; readonly id: string }
  | { readonly ended: "current" };

/**
 * revocationOutcome answers what the page must do after the server
 * confirms a revocation: re-read the list when another session ended
 * (the confirmation is only proven by the server's next answer, never
 * assumed), or leave for the sign-in entry when the current one did —
 * never loop, never claim success on a dead session.
 */
export function revocationOutcome(targetId: string, currentId: string): RevocationOutcome {
  if (targetId === currentId) {
    return { ended: "current" };
  }
  return { ended: "other", id: targetId };
}

/**
 * revocationConfirmation is the sentence the page confirms with: it names
 * the opaque target and nothing else — no metadata, no token, no reason.
 */
export function revocationConfirmation(translator: Translator, targetId: string): string {
  return translator.translate("auth.sessions.revoke_confirm", { id: targetId });
}
