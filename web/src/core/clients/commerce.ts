/**
 * Commerce staged client (P57-T03, harness-only).
 *
 * The two trade escrow reads of
 * `internal/commerce/adapters/http/openapi.fragment.json`: one owned
 * receipt with its tithe split and compensations, and the participant
 * extract with owned trade lines. Both paths, methods and bodies below
 * are the fragment's own: no transfer, funding, release or refund
 * endpoint exists in the fragment, so none is invented here — this
 * client holds reads only.
 *
 * Reads are plain GETs with an optional abort signal so a navigation
 * can cancel a slow answer; a cancelled answer is discarded and never
 * renders. The shared core already applies the account no-store policy
 * to every `/api/v1/me/*` path, so no cache directive is assembled
 * here. Path parameters travel encoded; query strings are never
 * invented.
 *
 * The client exists so the isolated harness (`tools/stagedharness`)
 * can exercise the real client against the real handlers. The
 * delivered process never mounts those handlers, so the production
 * composition holds no capability that would call them
 * (`web/src/core/staged.ts` and `web/src/pages/commerce.ts`): a
 * disabled page renders the honest unavailability view and sends
 * nothing.
 */
import type { TradeReceipt, TradeStatement } from "../../contracts/staged/commerce.js";
import type { HttpCore } from "../http.js";

const COMMERCE_PATH = "/api/v1/me/commerce";

/** The two trade escrow reads the harness may exercise. */
export interface CommerceClient {
  /** One owned receipt with tithe split and compensations. */
  readTradeReceipt(contractId: string, signal?: AbortSignal): Promise<TradeReceipt>;
  /** The participant extract with owned trade lines. */
  readTradeStatement(signal?: AbortSignal): Promise<TradeStatement>;
}

/** createCommerceClient binds the two trade reads to a shared core. */
export function createCommerceClient(core: HttpCore): CommerceClient {
  return {
    readTradeReceipt: (contractId: string, signal?: AbortSignal): Promise<TradeReceipt> =>
      core.request<TradeReceipt>({
        method: "GET",
        path: `${COMMERCE_PATH}/contracts/${encodeURIComponent(contractId)}`,
        ...(signal === undefined ? {} : { signal }),
      }),

    readTradeStatement: (signal?: AbortSignal): Promise<TradeStatement> =>
      core.request<TradeStatement>({
        method: "GET",
        path: `${COMMERCE_PATH}/statement`,
        ...(signal === undefined ? {} : { signal }),
      }),
  };
}
