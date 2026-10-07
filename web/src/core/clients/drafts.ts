/**
 * Arena drafts client (P52-T03; publication and closure P52-T05).
 *
 * The owner lists their private drafts and creates new ones. Creation
 * sends exactly the fields the contract declares — statement,
 * category, language and the optional context — and nothing else: in
 * particular no price, no pass and no debit value travels, because a
 * draft never consumes an Arena Pass and the browser never prices
 * anything. Both answers stay private under the account no-store
 * policy.
 *
 * Creation is never retried and carries no idempotency key. The
 * operation declares no `Idempotency-Key` parameter in
 * `api/openapi.json`, so the core must not invent one; a lost 201
 * surfaces as a failure the person answers by asking again, and the
 * single-flight guard of the drafts page — not a replay — is what
 * keeps a double submit from recording twice.
 *
 * Publication and closure close the same authorship journey on the
 * same opaque identifier. Publishing consumes exactly one Arena Pass
 * in the server transaction — the browser computes no price and
 * sends no payment — and without an available pass nothing is
 * written. Both transitions are single-shot calls, never retried
 * with no idempotency key: the contract declares no `Idempotency-Key`
 * parameter for either, and the server itself resolves a replay by
 * state, answering the already published or already closed Arena
 * without consuming or transitioning again. A lost answer is a
 * failure the person answers by reading again, never a replay the
 * core invents.
 */
import type {
  ArenaDraftRequest,
  ArenaDraftUpdateRequest,
  PrivateArena,
  PrivateArenaList,
} from "../../contracts/generated.js";
import type { HttpCore } from "../http.js";

/** Owner draft operations the authorship journey needs. */
export interface DraftsClient {
  /** The owner's private drafts, newest first. */
  list(): Promise<PrivateArenaList>;
  /** Creates one private draft from the contract fields alone. */
  create(input: ArenaDraftRequest): Promise<PrivateArena>;
  /**
   * Reads one owned draft by its opaque identifier. A missing draft
   * and another account's read exactly alike: not found.
   */
  get(id: string): Promise<PrivateArena>;
  /**
   * Replaces one draft under the optimistic version check. A stale
   * expected_version fails with version_conflict and no write happens;
   * the operation is never retried and carries no idempotency key, so
   * a lost response is a failure the person answers by reading again.
   */
  update(id: string, input: ArenaDraftUpdateRequest): Promise<PrivateArena>;
  /**
   * Discards one draft. Only drafts are deletable; the answer has no
   * body. Never retried: a lost 204 is re-read, never replayed blind.
   */
  remove(id: string): Promise<void>;
  /**
   * Publishes one draft, spending exactly one Arena Pass in the
   * server transaction. Without an available pass nothing is written
   * and the refusal names no_pass_available. A replay answers the
   * published Arena without spending again; the browser publishes
   * once and reads the answer, never assuming success.
   */
  publish(id: string): Promise<PrivateArena>;
  /**
   * Closes one published Arena of the same owner. Only the creator
   * may close and only a published Arena can be closed; a stranger
   * reads as not found and a draft as an invalid transition. A
   * replay answers the closed Arena without a second transition.
   */
  close(id: string): Promise<PrivateArena>;
}

const DRAFTS_PATH = "/api/v1/me/arena-drafts";
const OWN_ARENAS_PATH = "/api/v1/me/arenas";

/** createDraftsClient binds the draft operations to a shared core. */
export function createDraftsClient(core: HttpCore): DraftsClient {
  return {
    list: (): Promise<PrivateArenaList> =>
      core.request<PrivateArenaList>({ method: "GET", path: DRAFTS_PATH }),

    create: (input: ArenaDraftRequest): Promise<PrivateArena> =>
      core.request<PrivateArena>({
        method: "POST",
        path: DRAFTS_PATH,
        body: input,
        retry: false,
      }),

    get: (id: string): Promise<PrivateArena> =>
      core.request<PrivateArena>({ method: "GET", path: `${DRAFTS_PATH}/${encodeURIComponent(id)}` }),

    update: (id: string, input: ArenaDraftUpdateRequest): Promise<PrivateArena> =>
      core.request<PrivateArena>({
        method: "PATCH",
        path: `${DRAFTS_PATH}/${encodeURIComponent(id)}`,
        body: input,
        retry: false,
      }),

    remove: (id: string): Promise<void> =>
      core.request<void>({
        method: "DELETE",
        path: `${DRAFTS_PATH}/${encodeURIComponent(id)}`,
        retry: false,
      }),

    publish: (id: string): Promise<PrivateArena> =>
      core.request<PrivateArena>({
        method: "POST",
        path: `${DRAFTS_PATH}/${encodeURIComponent(id)}/publish`,
        retry: false,
      }),

    close: (id: string): Promise<PrivateArena> =>
      core.request<PrivateArena>({
        method: "POST",
        path: `${OWN_ARENAS_PATH}/${encodeURIComponent(id)}/close`,
        retry: false,
      }),
  };
}
