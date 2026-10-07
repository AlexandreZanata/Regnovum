/**
 * Attributions client (P53-T05).
 *
 * Two operations close the persuasion loop: the owner records which
 * eligible arguments one of their position changes credits, and
 * anyone reads the public counts of one argument. The recording is a
 * single-shot call with no idempotency key — the contract declares no
 * `Idempotency-Key` for it — and the server itself resolves a
 * repeated selection by state, answering the recorded set without
 * writing again. A lost answer is re-read, never replayed blind.
 *
 * Only identifiers the server listed travel: the change identifier
 * the change recording returned and the argument identifiers the
 * page offered. Nothing is invented here — a foreign change stays
 * indistinguishable from a missing one, and the page drops any
 * identifier outside the eligible set instead of sending it to
 * fail. An empty selection is a valid skip the server records as
 * such; it never creates a fake attribution. The public counts
 * carry counts only: no attributor identity ever serializes, and
 * the administrative signals stay on the moderator surface (P55).
 */
import type {
  ArgumentAttributionMetrics,
  AttributionRecordRequest,
  AttributionRecordResult,
} from "../../contracts/generated.js";
import type { HttpCore } from "../http.js";

/** Operations the attribution journey needs. */
export interface AttributionsClient {
  /**
   * Records the eligible arguments one owned position change
   * credits. Re-read the change afterwards: the answer is the
   * recorded set, and a replay resolves it without writing again.
   */
  record(changeId: string, input: AttributionRecordRequest): Promise<AttributionRecordResult>;
  /** Public counts of one argument: valid credits and crediting people. */
  counts(argumentId: string): Promise<ArgumentAttributionMetrics>;
}

const ME_CHANGES_PATH = "/api/v1/me/position-changes";
const ARGUMENTS_PATH = "/api/v1/arguments";

/** createAttributionsClient binds the attribution operations to a shared core. */
export function createAttributionsClient(core: HttpCore): AttributionsClient {
  return {
    record: (changeId: string, input: AttributionRecordRequest): Promise<AttributionRecordResult> =>
      core.request<AttributionRecordResult>({
        method: "POST",
        path: `${ME_CHANGES_PATH}/${encodeURIComponent(changeId)}/attributions`,
        body: input,
        retry: false,
      }),

    counts: (argumentId: string): Promise<ArgumentAttributionMetrics> =>
      core.request<ArgumentAttributionMetrics>({
        method: "GET",
        path: `${ARGUMENTS_PATH}/${encodeURIComponent(argumentId)}/attributions`,
      }),
  };
}
