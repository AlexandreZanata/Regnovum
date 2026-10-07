/**
 * Moderation filing client (P55-T01).
 *
 * Two single-shot writes and nothing else. `report` contests an
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
  ModerationAppeal,
  ModerationAppealRequest,
  ModerationReport,
  ModerationReportRequest,
} from "../../contracts/generated.js";
import type { HttpCore } from "../http.js";

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
   */
  appeal(input: ModerationAppealRequest): Promise<ModerationAppeal>;
}

const MODERATION_PATH = "/api/v1/me/moderation";

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
  };
}
