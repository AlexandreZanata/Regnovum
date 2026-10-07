/**
 * Restricted jobs operator panel presentation (P55-T03).
 *
 * Operators read the queue health, list one dead-job page and
 * retry one dead job with a stated reason — with the
 * assignment and the second factor revalidated on the server
 * per call. What lives in this module, DOM-free so the Node
 * runner verifies it without a browser, are the four things
 * such a panel needs: the translated counts of one health
 * answer, the lifecycle columns of one dead row, the
 * confirmation that names the job before the manual retry, and
 * the transition the server committed. No job payload ever
 * serializes, so none renders; the system health stays inside
 * the restricted panel and is never published.
 *
 * Two refusals shape this module. Counts render grouped but
 * never derived: seconds stay seconds and the browser computes
 * no rate, no saturation and no verdict over them. And the
 * retry carries exactly the operator's reason — 1..200
 * characters, anything else dropped instead of sent — while
 * the job, the workload and the transition come from the row:
 * a duplicate answers 409 and the console reads it instead of
 * sending again. Every string comes from the catalog the
 * runtime serves; the component that renders a view translates
 * nothing and formats nothing.
 */
import { formatInstant, formatNumber } from "../i18n/formats.js";
import type { DeadJob, DeadJobPage, JobsQueueHealth, JobsRetry } from "../contracts/generated.js";
import type { Locale } from "../i18n/locale.js";
import type { Translator } from "../i18n/translator.js";

/** Dead-job page size the contract sizes: 1..200, defaulting to 50. */
const DEAD_LIMIT_MIN = 1;
const DEAD_LIMIT_MAX = 200;

/** Retry reason bounds the contract declares: 1..200 characters. */
const RETRY_REASON_MIN = 1;
const RETRY_REASON_MAX = 200;

/** Everything the panel renders for one health answer. */
export interface HealthView {
  readonly heading: string;
  readonly intro: string;
  readonly counts: string;
  readonly generated: string;
}

/** Everything the panel renders for one dead row. */
export interface DeadRowView {
  readonly line: string;
  readonly retryable: boolean;
}

/** Everything the panel renders to confirm one manual retry. */
export interface RetryConfirmView {
  readonly prompt: string;
  readonly reason: string;
}

/** Everything the panel renders for one committed retry. */
export interface RetryResultView {
  readonly line: string;
}

/**
 * healthView projects one queue health answer for one locale.
 * Counts and waits only — seven grouped integers with the
 * reading instant — and no verdict over them.
 */
export function healthView(translator: Translator, locale: Locale, health: JobsQueueHealth): HealthView {
  const queue = health.queue;
  const counts = [
    formatNumber(locale, queue.queued),
    formatNumber(locale, queue.leased),
    formatNumber(locale, queue.succeeded),
    formatNumber(locale, queue.dead),
    formatNumber(locale, queue.due_now),
    formatNumber(locale, queue.lag_seconds),
    formatNumber(locale, queue.oldest_dead_seconds),
  ].join(", ");
  return {
    heading: translator.translate("moderation.operator.heading"),
    intro: translator.translate("moderation.operator.intro"),
    counts: translator.translate("moderation.operator.counts", { counts }),
    generated: translator.translate("moderation.operator.generated", {
      instant: formatInstant(locale, health.generated_at, { dateStyle: "medium", timeStyle: "short" }),
    }),
  };
}

/**
 * deadLimit keeps the page size only inside the page the
 * contract sizes. Anything else is dropped instead of sent to
 * fail, and the default page applies.
 */
export function deadLimit(value: string | null): number | undefined {
  if (value === null || value === "") {
    return undefined;
  }
  const parsed = Number.parseInt(value, 10);
  if (!Number.isInteger(parsed) || parsed < DEAD_LIMIT_MIN || parsed > DEAD_LIMIT_MAX) {
    return undefined;
  }
  return parsed;
}

/**
 * deadRowView projects one dead job for one locale. Lifecycle
 * columns only — identifier, workload, attempts against the
 * maximum, age and the last error code — with the retry flag
 * beside the sentence. The payload is absent by construction,
 * so it never renders.
 */
export function deadRowView(translator: Translator, locale: Locale, job: DeadJob): DeadRowView {
  return {
    line: translator.translate("moderation.operator.dead_row", {
      id: job.job_id,
      type: job.type,
      attempts: formatNumber(locale, job.attempts),
      max: formatNumber(locale, job.max_attempts),
      age: formatNumber(locale, job.age_seconds),
      error: job.last_error_code ?? "-",
    }),
    retryable: job.retryable,
  };
}

/**
 * deadTotal projects the page envelope: total with the reading
 * instant. Oldest first is the server's order, kept as read.
 */
export function deadTotal(translator: Translator, locale: Locale, page: DeadJobPage): string {
  return translator.translate("moderation.operator.dead_total", {
    total: formatNumber(locale, page.total),
    instant: formatInstant(locale, page.generated_at, { dateStyle: "medium", timeStyle: "short" }),
  });
}

/**
 * isRetryReason keeps the operator justification only inside
 * the 1..200 characters the contract declares. Anything else
 * is dropped instead of sent to fail: the transition needs a
 * stated reason.
 */
export function isRetryReason(value: string): boolean {
  return value.length >= RETRY_REASON_MIN && value.length <= RETRY_REASON_MAX;
}

/**
 * retryConfirmView names the job before the manual retry. The
 * confirmation carries the identifier and the workload the row
 * named — never a payload, never a secret.
 */
export function retryConfirmView(translator: Translator, jobId: string, jobType: string, reason: string): RetryConfirmView {
  return {
    prompt: translator.translate("moderation.operator.retry_confirm", { id: jobId, type: jobType }),
    reason,
  };
}

/**
 * retryResultView projects one committed transition: the job
 * back as queued, with the workload the server moved.
 */
export function retryResultView(translator: Translator, retry: JobsRetry): RetryResultView {
  return {
    line: translator.translate("moderation.operator.retried", {
      id: retry.job_id,
      type: retry.type,
      state: retry.state,
    }),
  };
}

/**
 * jobsFailure translates a refusal by the server code the
 * backend really emits. Public and moderator accounts without
 * competence are denied alike; a stale second factor answers
 * step-up; a non-retryable workload and a missing reason are
 * refused before anything moves; a duplicate answers conflict
 * and the console reads it. Anything else falls back to the
 * generic sentence instead of inventing a meaning.
 */
export function jobsFailure(translator: Translator, serverCode: string): string {
  switch (serverCode) {
    case "invalid_limit":
    case "invalid_body":
    case "body_too_large":
    case "reason_required":
      return translator.translate("moderation.failure.invalid");
    case "unauthorized":
      return translator.translate("moderation.failure.unauthorized");
    case "forbidden":
    case "retry_not_allowed":
      return translator.translate("moderation.failure.forbidden");
    case "step_up_required":
      return translator.translate("moderation.failure.step_up");
    case "job_not_found":
      return translator.translate("moderation.failure.missing");
    case "job_not_dead":
      return translator.translate("moderation.failure.conflict");
    default:
      return translator.translate("moderation.failure.generic");
  }
}
