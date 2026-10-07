/**
 * Account deletion client (P51-T05).
 *
 * The holder reads their deletion request, starts the cooling-off
 * period, or cancels inside the window. The request answers 202 with
 * the record; a replay resolves the existing record and the server
 * marks it Idempotency-Replayed. That replay is by account, not by a
 * key the contract declares — the operation carries no `Idempotency-Key`
 * parameter in `api/openapi.json`, so this client sends none and never
 * retries: a lost 202 is a failure the person answers by asking again,
 * which the server replays deliberately instead of recording twice.
 * Canceling carries no body: the reason the contract accepts is
 * restricted evidence the server never serializes, so this journey
 * does not collect it. A terminal request (executed or already
 * canceled) refuses cancellation with 409, and another account's
 * request reads exactly like a missing one.
 */
import type { AccountDeletionRequest } from "../../contracts/generated.js";
import type { HttpCore } from "../http.js";

/** Account deletion operations the privacy journey needs. */
export interface DeletionClient {
  /**
   * Reads the holder's request: requested (cooling off), executed or
   * canceled. No request answers 404, exactly like another account's.
   */
  status(): Promise<AccountDeletionRequest>;
  /**
   * Starts the cooling-off period. Answers 202 with the record, fresh
   * or replayed.
   */
  request(): Promise<AccountDeletionRequest>;
  /**
   * Cancels inside the cooling-off window. A terminal request answers
   * 409 and stays terminal.
   */
  cancel(): Promise<AccountDeletionRequest>;
}

const DELETION_PATH = "/api/v1/me/deletion";

/** createDeletionClient binds the deletion operations to a shared core. */
export function createDeletionClient(core: HttpCore): DeletionClient {
  return {
    status: (): Promise<AccountDeletionRequest> =>
      core.request<AccountDeletionRequest>({ method: "GET", path: DELETION_PATH }),

    request: (): Promise<AccountDeletionRequest> =>
      core.request<AccountDeletionRequest>({
        method: "POST",
        path: DELETION_PATH,
        retry: false,
      }),

    cancel: (): Promise<AccountDeletionRequest> =>
      core.request<AccountDeletionRequest>({
        method: "POST",
        path: `${DELETION_PATH}/cancel`,
        retry: false,
      }),
  };
}
