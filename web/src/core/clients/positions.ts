/**
 * Positions client (P18-T03; journey P18-T06).
 *
 * The aggregate is public; the participant's own position and its changes are
 * account-scoped and therefore never cached by the core.
 */
import type {
  PositionAggregate,
  PositionChangeHistory,
  PositionChangeRecord,
  PositionConfirmation,
  PositionRequest,
  PrivatePosition,
} from "../../contracts/generated.js";
import type { HttpCore } from "../http.js";

/** Operations the participation journey needs. */
export interface PositionsClient {
  /** Public aggregate of an Arena, never a per-person list. */
  aggregate(arenaId: string): Promise<PositionAggregate>;
  /** The caller's own position, 404 until the first confirmation. */
  mine(arenaId: string): Promise<PrivatePosition>;
  /** First confirmation of a position (wallet debit happens server-side). */
  confirm(arenaId: string, input: PositionRequest): Promise<PositionConfirmation>;
  /** A later change of position. */
  change(arenaId: string, input: PositionRequest): Promise<PositionChangeRecord>;
  /** The caller's change history. */
  changes(arenaId: string): Promise<PositionChangeHistory>;
}

const ARENAS_PATH = "/api/v1/arenas";
const ME_ARENAS_PATH = "/api/v1/me/arenas";

// Position transitions have no backend-proven idempotent contract (P50-T03):
// a replay is not safe by header alone, so mutations are never retried.
// A lost response surfaces as a failure the person can retry deliberately.

/** createPositionsClient binds the position operations to a shared core. */
export function createPositionsClient(core: HttpCore): PositionsClient {
  return {
    aggregate: (arenaId: string): Promise<PositionAggregate> =>
      core.request<PositionAggregate>({
        method: "GET",
        path: `${ARENAS_PATH}/${encodeURIComponent(arenaId)}/positions`,
      }),

    mine: (arenaId: string): Promise<PrivatePosition> =>
      core.request<PrivatePosition>({
        method: "GET",
        path: `${ME_ARENAS_PATH}/${encodeURIComponent(arenaId)}/position`,
      }),

    confirm: (arenaId: string, input: PositionRequest): Promise<PositionConfirmation> =>
      core.request<PositionConfirmation>({
        method: "POST",
        path: `${ME_ARENAS_PATH}/${encodeURIComponent(arenaId)}/position`,
        body: input,
        retry: false,
      }),

    change: (arenaId: string, input: PositionRequest): Promise<PositionChangeRecord> =>
      core.request<PositionChangeRecord>({
        method: "POST",
        path: `${ME_ARENAS_PATH}/${encodeURIComponent(arenaId)}/position/changes`,
        body: input,
        retry: false,
      }),

    changes: (arenaId: string): Promise<PositionChangeHistory> =>
      core.request<PositionChangeHistory>({
        method: "GET",
        path: `${ME_ARENAS_PATH}/${encodeURIComponent(arenaId)}/position/changes`,
      }),
  };
}
