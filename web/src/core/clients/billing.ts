/**
 * Billing client (P54-T03 checkout; P54-T04 subscription and portal).
 *
 * The browser names only what the contract lets it name, and every
 * answer stays private under the account no-store policy. `checkout`
 * carries the minimal intent — market and product — with the
 * operation token in the request body: `api/openapi.json` declares
 * no `Idempotency-Key` header parameter for it, so the core must
 * not invent one. The call is single-shot; the store's own
 * idempotency is what makes asking again with the same token safe,
 * resolving the session already created (`replayed: true`)
 * instead of provisioning a second one. A lost answer is
 * answered by asking again with the same token — which the
 * server replays deliberately — never by buying twice under a
 * fresh key.
 *
 * `subscription` is the Member projection read: lifecycle and
 * period only, no provider identifier ever serializes.
 * `portal` opens the hosted customer portal with at most the
 * optional operation token in the body — again no header, again
 * single-shot — and returns only the portal URL. No card number,
 * no token and no provider identifier travels in either
 * direction. Subscription and portal stay out of the checkout
 * journey and the checkout stays out of theirs.
 */
import type { BillingCheckout, BillingPortal, BillingSubscription } from "../../contracts/generated.js";
import type { HttpCore } from "../http.js";

/** The minimal purchase intent the browser may name. */
export interface BillingCheckoutInput {
  readonly market: string;
  readonly product: string;
  readonly idempotencyKey: string;
}

/** Optional operation token for an idempotent portal opening. */
export interface BillingPortalInput {
  readonly idempotencyKey?: string;
}

/** Billing operations the purchase and Member journeys need. */
export interface BillingClient {
  /**
   * Creates one server-priced checkout for the owner's account.
   * Amount, currency, price and return URLs resolve on the
   * server; a repeated token resolves the created session.
   */
  checkout(input: BillingCheckoutInput): Promise<BillingCheckout>;
  /** The owner's Member projection: 401 without a session. */
  subscription(): Promise<BillingSubscription>;
  /**
   * Opens the hosted customer portal for the owner's stored
   * customer: 404 without one, 401 without a session. Returns
   * only the portal URL.
   */
  portal(input?: BillingPortalInput): Promise<BillingPortal>;
}

const BILLING_PATH = "/api/v1/me/billing";

/** One explicit key per purchase attempt, carried in the body. */
export function newCheckoutKey(): string {
  return globalThis.crypto.randomUUID();
}

/** createBillingClient binds the billing operations to a shared core. */
export function createBillingClient(core: HttpCore): BillingClient {
  return {
    checkout: (input: BillingCheckoutInput): Promise<BillingCheckout> =>
      core.request<BillingCheckout>({
        method: "POST",
        path: `${BILLING_PATH}/checkout`,
        body: { market: input.market, product: input.product, idempotency_key: input.idempotencyKey },
        retry: false,
      }),

    subscription: (): Promise<BillingSubscription> =>
      core.request<BillingSubscription>({ method: "GET", path: `${BILLING_PATH}/subscription` }),

    portal: (input?: BillingPortalInput): Promise<BillingPortal> =>
      core.request<BillingPortal>({
        method: "POST",
        path: `${BILLING_PATH}/portal`,
        ...(input?.idempotencyKey === undefined ? { body: {} } : { body: { idempotency_key: input.idempotencyKey } }),
        retry: false,
      }),
  };
}
