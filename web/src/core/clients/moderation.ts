/**
 * Moderation client (P55-T01 filings; P55-T02 restricted workbench).
 *
 * Two single-shot writes and a restricted read set. `report` contests an
 * Arena, an argument or a profile under the closed reason
 * vocabulary with optional bounded context; `appeal` contests
 * one eligible sanction for its sanctioned owner inside the
 * appeal window, exactly one appeal per action. Both answers
 * stay private under the account no-store policy.
 *
 * Neither call carries an idempotency key and neither is
 * retried: `api/openapi.json` declares no `Idempotency-Key`
 * parameter for either, so the core must not invent one. The
 * server itself resolves a repeated filing by state, answering
 * the recorded identity with `replayed: true` instead of
 * writing again. A lost answer is re-read, never replayed
 * blind — and a report never removes content by itself, while
 * an appeal never promises a list or a state the API does not
 * provide. No tribunal, queue or decision endpoint is exposed
 * here; those stay on the restricted T02/T03 surfaces.
 */
import type {
  AttributionSignals,
  ModerationAppeal,
  ModerationAppealRequest,
  ModerationCasePage,
  ModerationClaim,
  ModerationDecision,
  ModerationDecisionRequest,
  ModerationReport,
  ModerationReportRequest,
} from "../../contracts/generated.js";
import type { HttpCore } from "../http.js";

/** Filters of the triage queue; the cursor travels opaque. */
export interface ModerationQueueQuery {
  readonly status?: string;
  readonly cursor?: string;
  readonly limit?: number;
}

/** Moderation filings the report and appeal journeys need. */
export interface ModerationClient {
  /**
   * Files one structured content report. Restricted evidence:
   * the reporter context never serializes back.
   */
  report(input: ModerationReportRequest): Promise<ModerationReport>;
  /**
   * Files one sanction appeal. Appellant context never
   * serializes back.
   */    appeal(input: ModerationAppealRequest): Promise<ModerationAppeal>;
  /**
   * One triage queue page for active moderators: routing only,
   * never restricted evidence. The role and the second factor
   * are revalidated on the server per call.
   */
  queue(query?: ModerationQueueQuery, signal?: AbortSignal): Promise<ModerationCasePage>;
  /**
   * Moves an open case (or an expired lease) under the calling
   * moderator with a fresh bounded lease. A held lease answers
   * 409; the server owns the lease, never the browser.
   */
  claim(id: string): Promise<ModerationClaim>;
  /**
   * Records one explicit sanction with rule, justification and
   * optional expiry. Severity is never derived: the measure
   * travels untouched and the justification never serializes
   * back.
   */
  decide(id: string, input: ModerationDecisionRequest): Promise<ModerationDecision>;
  /**
   * Advisory abuse signals of one author for the configured
   * window. Prevention data only: no score, no severity, no
   * weight and no automatic action — a human reviews and
   * decides. Never rendered on a public page.
   */
  signals(authorId: string, signal?: AbortSignal): Promise<AttributionSignals>;
}

const MODERATION_PATH = "/api/v1/me/moderation";
const CONSOLE_PATH = "/api/v1/moderation";

/** createModerationClient binds the filings to a shared core. */
export function createModerationClient(core: HttpCore): ModerationClient {
  return {
    report: (input: ModerationReportRequest): Promise<ModerationReport> =>
      core.request<ModerationReport>({
        method: "POST",
        path: `${MODERATION_PATH}/reports`,
        body: input,
        retry: false,
      }),

    appeal: (input: ModerationAppealRequest): Promise<ModerationAppeal> =>
      core.request<ModerationAppeal>({
        method: "POST",
        path: `${MODERATION_PATH}/appeals`,
        body: input,
        retry: false,
      }),

    queue: (query?: ModerationQueueQuery, signal?: AbortSignal): Promise<ModerationCasePage> =>
      core.request<ModerationCasePage>({
        method: "GET",
        path: `${CONSOLE_PATH}/cases`,
        ...(query === undefined ? {} : { query: { status: query.status, cursor: query.cursor, limit: query.limit } }),
        ...(signal === undefined ? {} : { signal }),
      }),

    claim: (id: string): Promise<ModerationClaim> =>
      core.request<ModerationClaim>({
        method: "POST",
        path: `${CONSOLE_PATH}/cases/${encodeURIComponent(id)}/claim`,
        retry: false,
      }),

    decide: (id: string, input: ModerationDecisionRequest): Promise<ModerationDecision> =>
      core.request<ModerationDecision>({
        method: "POST",
        path: `${CONSOLE_PATH}/cases/${encodeURIComponent(id)}/decisions`,
        body: input,
        retry: false,
      }),

    signals: (authorId: string, signal?: AbortSignal): Promise<AttributionSignals> =>
      core.request<AttributionSignals>({
        method: "GET",
        path: `${CONSOLE_PATH}/attribution-signals/${encodeURIComponent(authorId)}`,
        ...(signal === undefined ? {} : { signal }),
      }),
  };
}
