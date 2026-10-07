/**
 * Arguments client (P18-T03; journey P18-T06; keys P50-T03).
 *
 * Public reads stay cache-friendly. Publishing and replying carry the
 * mandatory `Idempotency-Key` of their backend-proven contract
 * (`api/openapi.json`: publishArgument, replyToArgument): the client names
 * one key per attempt, the core replays it across the single retry, and a
 * replay answers the recorded argument instead of charging again. The core
 * never invents the key — it only sends what this client supplies.
 */
import type {
  Argument,
  ArgumentMutationResult,
  ArgumentPage,
  ArgumentPublishRequest,
} from "../../contracts/generated.js";
import type { HttpCore } from "../http.js";

/** Filters of `GET /api/v1/arenas/{id}/arguments`; `relation` is required. */
export interface ArenaArgumentsQuery {
  readonly relation: string;
  readonly cursor?: string;
  readonly limit?: number;
}

/** Cursor page of replies. */
export interface ArgumentRepliesQuery {
  readonly cursor?: string;
  readonly limit?: number;
}

/** Read and write operations the Arena argument list needs. */
export interface ArgumentsClient {
  list(arenaId: string, query: ArenaArgumentsQuery): Promise<ArgumentPage>;
  get(argumentId: string): Promise<Argument>;
  replies(argumentId: string, query?: ArgumentRepliesQuery): Promise<ArgumentPage>;
  publish(arenaId: string, input: ArgumentPublishRequest): Promise<ArgumentMutationResult>;
  reply(arenaId: string, argumentId: string, input: ArgumentPublishRequest): Promise<ArgumentMutationResult>;
}

const ARENAS_PATH = "/api/v1/arenas";
const ARGUMENTS_PATH = "/api/v1/arguments";
const ME_ARENAS_PATH = "/api/v1/me/arenas";

/** Two attempts: enough to survive a dropped response, never a hammer. */
const MUTATION_RETRY = { maxAttempts: 2 } as const;

/** One explicit key per mutation attempt; the core replays it on retry. */
function newMutationKey(): string {
  return globalThis.crypto.randomUUID();
}

/** createArgumentsClient binds the argument operations to a shared core. */
export function createArgumentsClient(core: HttpCore): ArgumentsClient {
  return {
    list: (arenaId: string, query: ArenaArgumentsQuery): Promise<ArgumentPage> =>
      core.request<ArgumentPage>({
        method: "GET",
        path: `${ARENAS_PATH}/${encodeURIComponent(arenaId)}/arguments`,
        query: { relation: query.relation, cursor: query.cursor, limit: query.limit },
      }),

    get: (argumentId: string): Promise<Argument> =>
      core.request<Argument>({ method: "GET", path: `${ARGUMENTS_PATH}/${encodeURIComponent(argumentId)}` }),

    replies: (argumentId: string, query?: ArgumentRepliesQuery): Promise<ArgumentPage> =>
      core.request<ArgumentPage>({
        method: "GET",
        path: `${ARGUMENTS_PATH}/${encodeURIComponent(argumentId)}/replies`,
        ...(query === undefined ? {} : { query: { cursor: query.cursor, limit: query.limit } }),
      }),

    publish: (arenaId: string, input: ArgumentPublishRequest): Promise<ArgumentMutationResult> =>
      core.request<ArgumentMutationResult>({
        method: "POST",
        path: `${ME_ARENAS_PATH}/${encodeURIComponent(arenaId)}/arguments`,
        body: input,
        retry: MUTATION_RETRY,
        idempotencyKey: newMutationKey(),
      }),

    reply: (arenaId: string, argumentId: string, input: ArgumentPublishRequest): Promise<ArgumentMutationResult> =>
      core.request<ArgumentMutationResult>({
        method: "POST",
        path: `${ME_ARENAS_PATH}/${encodeURIComponent(arenaId)}/arguments/${encodeURIComponent(argumentId)}/replies`,
        body: input,
        retry: MUTATION_RETRY,
        idempotencyKey: newMutationKey(),
      }),
  };
}
