/**
 * Arguments client (P18-T03; journey P18-T06; keys P50-T03; search P53-T02).
 *
 * Public reads stay cache-friendly. Every read takes an optional
 * caller signal so a page turn or a new search cancels the request it
 * replaces; an aborted answer is discarded, never rendered.
 * Publishing and replying carry the mandatory `Idempotency-Key` of
 * their backend-proven contract (`api/openapi.json`: publishArgument,
 * replyToArgument): the client names one key per attempt, the core
 * replays it across the single retry, and a replay answers the
 * recorded argument instead of charging again. The core never invents
 * the key — it only sends what this client supplies. Withdrawing is
 * a single-shot call with no key: the contract declares no
 * `Idempotency-Key` for it, and the server itself resolves a repeat
 * by state, answering the recorded withdrawal without erasing
 * anything else. A lost answer is re-read, never replayed blind.
 */
import type {
  Argument,
  ArgumentMutationResult,
  ArgumentPage,
  ArgumentPublishRequest,
  SearchArgumentPage,
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

/** Full-text search over published arguments; the query travels verbatim. */
export interface ArgumentSearchQuery {
  readonly q: string;
  readonly language?: string;
  readonly cursor?: string;
  readonly limit?: number;
}

/** Read and write operations the Arena argument list needs. */
export interface ArgumentsClient {
  list(arenaId: string, query: ArenaArgumentsQuery, signal?: AbortSignal): Promise<ArgumentPage>;
  get(argumentId: string, signal?: AbortSignal): Promise<Argument>;
  search(query: ArgumentSearchQuery, signal?: AbortSignal): Promise<SearchArgumentPage>;
  withdraw(argumentId: string): Promise<ArgumentMutationResult>;
  replies(argumentId: string, query?: ArgumentRepliesQuery): Promise<ArgumentPage>;
  publish(arenaId: string, input: ArgumentPublishRequest): Promise<ArgumentMutationResult>;
  reply(arenaId: string, argumentId: string, input: ArgumentPublishRequest): Promise<ArgumentMutationResult>;
}

const ARENAS_PATH = "/api/v1/arenas";
const ARGUMENTS_PATH = "/api/v1/arguments";
const SEARCH_PATH = "/api/v1/search";
const ME_ARENAS_PATH = "/api/v1/me/arenas";
const ME_ARGUMENTS_PATH = "/api/v1/me/arguments";

/** Two attempts: enough to survive a dropped response, never a hammer. */
const MUTATION_RETRY = { maxAttempts: 2 } as const;

/** One explicit key per mutation attempt; the core replays it on retry. */
function newMutationKey(): string {
  return globalThis.crypto.randomUUID();
}

/** createArgumentsClient binds the argument operations to a shared core. */
export function createArgumentsClient(core: HttpCore): ArgumentsClient {
  return {
    list: (arenaId: string, query: ArenaArgumentsQuery, signal?: AbortSignal): Promise<ArgumentPage> =>
      core.request<ArgumentPage>({
        method: "GET",
        path: `${ARENAS_PATH}/${encodeURIComponent(arenaId)}/arguments`,
        query: { relation: query.relation, cursor: query.cursor, limit: query.limit },
        ...(signal === undefined ? {} : { signal }),
      }),

    get: (argumentId: string, signal?: AbortSignal): Promise<Argument> =>
      core.request<Argument>({
        method: "GET",
        path: `${ARGUMENTS_PATH}/${encodeURIComponent(argumentId)}`,
        ...(signal === undefined ? {} : { signal }),
      }),

    search: (query: ArgumentSearchQuery, signal?: AbortSignal): Promise<SearchArgumentPage> =>
      core.request<SearchArgumentPage>({
        method: "GET",
        path: `${SEARCH_PATH}/arguments`,
        query: { q: query.q, language: query.language, cursor: query.cursor, limit: query.limit },
        ...(signal === undefined ? {} : { signal }),
      }),

    replies: (argumentId: string, query?: ArgumentRepliesQuery): Promise<ArgumentPage> =>
      core.request<ArgumentPage>({
        method: "GET",
        path: `${ARGUMENTS_PATH}/${encodeURIComponent(argumentId)}/replies`,
        ...(query === undefined ? {} : { query: { cursor: query.cursor, limit: query.limit } }),
      }),

    withdraw: (argumentId: string): Promise<ArgumentMutationResult> =>
      core.request<ArgumentMutationResult>({
        method: "POST",
        path: `${ME_ARGUMENTS_PATH}/${encodeURIComponent(argumentId)}/withdraw`,
        retry: false,
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
