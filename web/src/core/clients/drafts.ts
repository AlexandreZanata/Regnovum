/**
 * Arena drafts client (P52-T03).
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
 */
import type { ArenaDraftRequest, PrivateArena, PrivateArenaList } from "../../contracts/generated.js";
import type { HttpCore } from "../http.js";

/** Owner draft operations the authorship journey needs. */
export interface DraftsClient {
  /** The owner's private drafts, newest first. */
  list(): Promise<PrivateArenaList>;
  /** Creates one private draft from the contract fields alone. */
  create(input: ArenaDraftRequest): Promise<PrivateArena>;
}

const DRAFTS_PATH = "/api/v1/me/arena-drafts";

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
  };
}
