/**
 * Arena passes client (P54-T02).
 *
 * Two safe reads and nothing else. `summary` is the owner's
 * derived pass projection — the available total plus the private
 * per-lot breakdown with the immutable expiration each lot
 * carries — and `history` is the owner's consumption page, newest
 * first, over an opaque server-signed cursor the client never
 * parses and never builds. Both answers stay private under the
 * account no-store policy (the core derives it from the `/me/`
 * prefix, like every `/api/v1/me/*` answer).
 *
 * There is no grant, renewal or purchase here on purpose: the
 * contract of these operations is GET, lots arrive with the
 * origin the server recorded (PURCHASE, MEMBER or ADMIN), and a
 * reset is a re-read — never a POST the backend never declared.
 * The MEMBER lots below are the projected Member state the task
 * names: they render exactly what the server derived, and the
 * browser invents no renewal for them.
 */
import type { ArenaPassHistory, ArenaPassSummary } from "../../contracts/generated.js";
import type { HttpCore } from "../http.js";

/** Cursor page the consumption history reads; the cursor travels opaque. */
export interface PassHistoryQuery {
  readonly cursor?: string;
  readonly limit?: number;
}

/** Pass reads the entitlement views need. */
export interface PassesClient {
  /** The owner's derived pass summary: 401 without a session. */
  summary(): Promise<ArenaPassSummary>;
  /** One consumption page of the owner: 401 without a session. */
  history(query?: PassHistoryQuery): Promise<ArenaPassHistory>;
}

const PASSES_PATH = "/api/v1/me/passes";
const HISTORY_PATH = "/api/v1/me/passes/history";

/** createPassesClient binds the pass reads to a shared core. */
export function createPassesClient(core: HttpCore): PassesClient {
  return {
    summary: (): Promise<ArenaPassSummary> =>
      core.request<ArenaPassSummary>({ method: "GET", path: PASSES_PATH }),

    history: (query?: PassHistoryQuery): Promise<ArenaPassHistory> =>
      core.request<ArenaPassHistory>({
        method: "GET",
        path: HISTORY_PATH,
        ...(query === undefined ? {} : { query: { cursor: query.cursor, limit: query.limit } }),
      }),
  };
}
