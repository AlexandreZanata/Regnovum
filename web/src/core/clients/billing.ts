/**
 * Billing checkout client (P54-T03).
 *
 * One mutation with a backend-proven idempotent contract. The
 * browser names only the minimal intent — the market and the
 * product — while the amount, the currency, the price and the
 * return URLs are resolved by the server from the versioned
 * catalog and the allowlisted configuration. The client names
 * one explicit key per purchase attempt and the core replays it
 * across the single retry; a repeated key resolves the session
 * already created (`replayed: true`) instead of provisioning a
 * second one. The core never invents the key — it only sends
 * what this client supplies.
 *
 * The redirect URL and the priced amount travel only from the
 * server: the page navigates exclusively to a URL the answer
 * carried after proving it safe, and it formats exclusively the
 * amount the answer carried. No provider identifier ever
 * serializes, so none is read here either. Subscription and
 * portal stay on the T04 surface.
 */
import type { BillingCheckout } from "../../contracts/generated.js";
import type { HttpCore } from "../http.js";

/** The minimal purchase intent the browser may name. */
export interface BillingCheckoutInput {
  readonly market: string;
  readonly product: string;
  readonly idempotencyKey: string;
}

/** Billing writes the checkout journey needs. */
export interface BillingClient {
  /**
   * Creates one server-priced checkout for the owner's account.
   * A lost answer is answered by asking again with the same key
   * — which the server replays deliberately — never by buying
   * twice under a fresh key.
   */
  checkout(input: BillingCheckoutInput): Promise<BillingCheckout>;
}

const BILLING_PATH = "/api/v1/me/billing";

/** Two attempts: enough to survive a dropped response, never a hammer. */
const CHECKOUT_RETRY = { maxAttempts: 2 } as const;

/** One explicit key per purchase attempt; the core replays it on retry. */
export function newCheckoutKey(): string {
  return globalThis.crypto.randomUUID();
}

/** createBillingClient binds the checkout to a shared core. */
export function createBillingClient(core: HttpCore): BillingClient {
  return {
    checkout: (input: BillingCheckoutInput): Promise<BillingCheckout> =>
      core.request<BillingCheckout>({
        method: "POST",
        path: `${BILLING_PATH}/checkout`,
        body: { market: input.market, product: input.product, idempotency_key: input.idempotencyKey },
        retry: CHECKOUT_RETRY,
        idempotencyKey: input.idempotencyKey,
      }),
  };
}
