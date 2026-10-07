/**
 * Jobs operator client (P55-T03).
 *
 * The restricted panel for the three jobs routes the P49
 * composition already mounts: the queue health, one dead-job
 * page and the manual retry of one dead job. Health and dead
 * reads carry counts and lifecycle columns only — no job
 * payload ever serializes, so none is read here either. The
 * retry states only the operator reason (1..200 characters);
 * the job, the workload and the transition come from the row,
 * and the transition with its audit record commits together on
 * the server. Every answer stays private under the account
 * no-store policy.
 *
 * The retry is single-shot with no idempotency key:
 * `api/openapi.json` declares no `Idempotency-Key` parameter
 * for it, so the core must not invent one. A repeated retry of
 * an already requeued job answers 409; the console confirms
 * before sending and reads the result — never an automatic
 * second attempt. No system health is published through this
 * client and no secret payload is displayed: there is nothing
 * secret to display.
 */
import type { DeadJobPage, JobsQueueHealth, JobsRetry, JobsRetryRequest } from "../../contracts/generated.js";
import type { HttpCore } from "../http.js";

/** Jobs operations the restricted operator panel needs. */
export interface JobsClient {
  /** Queue counts and waits at one instant: 401/403 without competence. */
  health(signal?: AbortSignal): Promise<JobsQueueHealth>;
  /** Up to `limit` dead jobs, oldest first: 401/403 without competence. */
  dead(limit?: number, signal?: AbortSignal): Promise<DeadJobPage>;
  /**
   * Returns one dead job to the queue under the stated reason.
   * Step-up is revalidated on the server; only allowlisted
   * workloads retry. A duplicate answers 409.
   */
  retry(id: string, input: JobsRetryRequest): Promise<JobsRetry>;
}

const ADMIN_JOBS_PATH = "/api/v1/admin/jobs";

/** createJobsClient binds the operator reads and the retry to a shared core. */
export function createJobsClient(core: HttpCore): JobsClient {
  return {
    health: (signal?: AbortSignal): Promise<JobsQueueHealth> =>
      core.request<JobsQueueHealth>({
        method: "GET",
        path: `${ADMIN_JOBS_PATH}/health`,
        ...(signal === undefined ? {} : { signal }),
      }),

    dead: (limit?: number, signal?: AbortSignal): Promise<DeadJobPage> =>
      core.request<DeadJobPage>({
        method: "GET",
        path: `${ADMIN_JOBS_PATH}/dead`,
        ...(limit === undefined ? {} : { query: { limit } }),
        ...(signal === undefined ? {} : { signal }),
      }),

    retry: (id: string, input: JobsRetryRequest): Promise<JobsRetry> =>
      core.request<JobsRetry>({
        method: "POST",
        path: `${ADMIN_JOBS_PATH}/${encodeURIComponent(id)}/retry`,
        body: input,
        retry: false,
      }),
  };
}
