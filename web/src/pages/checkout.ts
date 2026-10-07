/**
 * Server-priced checkout presentation (P54-T03).
 *
 * The account buys only what the versioned catalog declares.
 * `MVP_OFFERS` is the browser's mirror of that catalog — twelve
 * market/product pairs, nothing seasonal — and anything outside
 * it is dropped instead of sent to fail. The price, the currency
 * and the redirect URL come exclusively from the checkout answer:
 * the amount renders through the integer minor-units formatter
 * (never a float), and the buyer is handed over exclusively to a
 * URL that passed `isSafeRedirectUrl`. The return and success
 * addresses are the deployment's allowlist, never fields the
 * browser builds — and coming back from the provider grants
 * nothing by itself: only the verified webhook settles an
 * intent, so the page re-reads the balance and the passes
 * instead of assuming success. A repeated key resolves the
 * session already created; the browser buys once per key and
 * reads the answer, never assuming a second purchase.
 *
 * Every string comes from the catalog the runtime serves; the
 * component that renders a view translates nothing and formats
 * nothing.
 */
import { formatCurrencyMinor } from "../i18n/formats.js";
import type { BillingCheckout } from "../contracts/generated.js";
import type { Locale } from "../i18n/locale.js";
import type { Translator } from "../i18n/translator.js";

/** One offer the MVP catalog declares. */
export interface MvpOffer {
  readonly market: string;
  readonly product: string;
}

/**
 * The MVP offers, mirrored from the versioned catalog
 * (`internal/billing/adapters/catalog/products.json`): three INK
 * packs, two pass packs and the monthly Member, in each of the
 * two commercial regions. Nothing seasonal is for sale.
 */
export const MVP_OFFERS: readonly MvpOffer[] = [
  { market: "BR", product: "ink_10000" },
  { market: "BR", product: "ink_40000" },
  { market: "BR", product: "ink_100000" },
  { market: "BR", product: "pass_1" },
  { market: "BR", product: "pass_5" },
  { market: "BR", product: "member_monthly" },
  { market: "INTERNATIONAL", product: "ink_10000" },
  { market: "INTERNATIONAL", product: "ink_40000" },
  { market: "INTERNATIONAL", product: "ink_100000" },
  { market: "INTERNATIONAL", product: "pass_1" },
  { market: "INTERNATIONAL", product: "pass_5" },
  { market: "INTERNATIONAL", product: "member_monthly" },
];

/** Longest redirect the page will follow, mirroring the server bound. */
const REDIRECT_MAX_LENGTH = 2048;

/**
 * isMvpOffer answers whether the pair is one the catalog
 * declares. Anything else is dropped instead of sent to fail.
 */
export function isMvpOffer(market: string, product: string): boolean {
  return MVP_OFFERS.some((offer) => offer.market === market && offer.product === product);
}

/**
 * isSafeRedirectUrl enforces the structural part of the redirect
 * contract: an absolute HTTPS URL with a host, without
 * credentials and without a fragment. Provider sessions are
 * never plain HTTP, so HTTP is refused alongside every other
 * non-HTTPS scheme. Which hosts may receive the buyer stays the
 * deployment's decision and the server's authority — the typed
 * configuration refuses a foreign return origin at boot and the
 * payment adapter refuses a malformed URL before opening a
 * socket — so this page additionally never navigates to a URL
 * that did not arrive in the checkout answer of the intent just
 * created: no query string, no hash and no user input ever
 * reaches navigation, which is what keeps a response URL from
 * becoming an open redirect through this page.
 */
export function isSafeRedirectUrl(raw: string): boolean {
  if (raw === "" || raw.length > REDIRECT_MAX_LENGTH) {
    return false;
  }
  let parsed: URL;
  try {
    parsed = new URL(raw);
  } catch {
    return false;
  }
  if (parsed.protocol !== "https:") {
    return false;
  }
  if (parsed.host === "") {
    return false;
  }
  if (parsed.username !== "" || parsed.password !== "") {
    return false;
  }
  if (parsed.hash !== "") {
    return false;
  }
  return true;
}

/** Everything the page renders for one priced checkout. */
export interface CheckoutView {
  readonly heading: string;
  readonly intro: string;
  readonly offer: string;
  readonly price: string;
  readonly note: string;
  readonly redirectUrl: string | null;
}

/**
 * checkoutView projects one checkout answer for one locale. The
 * offer names the intent the buyer chose; the price renders the
 * server's minor units in the server's currency. Only an open
 * session hands the buyer over: `created` and `open` carry the
 * redirect once it proves safe, `paid` carries the confirmation
 * note with no navigation, and `expired`/`failed` carry their
 * terminal note with no navigation either. An unsafe redirect is
 * never followed — it renders the unavailable sentence with no
 * destination.
 */
export function checkoutView(
  translator: Translator,
  locale: Locale,
  checkout: BillingCheckout,
): CheckoutView {
  const offer = translator.translate("wallet.checkout.offer", {
    product: checkout.product,
    market: checkout.market,
  });
  const price = translator.translate("wallet.checkout.price", {
    amount: formatCurrencyMinor(locale, checkout.amount_minor, checkout.currency),
  });
  switch (checkout.status) {
    case "created":
    case "open": {
      if (isSafeRedirectUrl(checkout.redirect_url)) {
        return {
          heading: translator.translate("wallet.checkout.heading"),
          intro: translator.translate("wallet.checkout.intro"),
          offer,
          price,
          note: translator.translate("wallet.checkout.redirect_note"),
          redirectUrl: checkout.redirect_url,
        };
      }
      return {
        heading: translator.translate("wallet.checkout.heading"),
        intro: translator.translate("wallet.checkout.intro"),
        offer,
        price,
        note: translator.translate("wallet.checkout.unavailable"),
        redirectUrl: null,
      };
    }
    case "paid":
      return {
        heading: translator.translate("wallet.checkout.heading"),
        intro: translator.translate("wallet.checkout.intro"),
        offer,
        price,
        note: translator.translate("wallet.checkout.paid_note"),
        redirectUrl: null,
      };
    case "expired":
      return {
        heading: translator.translate("wallet.checkout.heading"),
        intro: translator.translate("wallet.checkout.intro"),
        offer,
        price,
        note: translator.translate("wallet.checkout.expired_note"),
        redirectUrl: null,
      };
    case "failed":
      return {
        heading: translator.translate("wallet.checkout.heading"),
        intro: translator.translate("wallet.checkout.intro"),
        offer,
        price,
        note: translator.translate("wallet.checkout.failed_note"),
        redirectUrl: null,
      };
  }
}

/**
 * checkoutFailure translates a refusal by the server code the
 * backend really emits. Unknown products and regions, a
 * misused key and a disabled market all arrive as
 * `invalid_checkout`; eligibility arrives as `not_eligible`;
 * an unavailable checkout or a fallen provider arrives without
 * crediting anything. Anything else falls back to the generic
 * sentence instead of inventing a meaning.
 */
export function checkoutFailure(translator: Translator, serverCode: string): string {
  switch (serverCode) {
    case "invalid_checkout":
    case "invalid_body":
      return translator.translate("wallet.checkout.failure_invalid");
    case "unauthorized":
      return translator.translate("wallet.checkout.failure_unauthorized");
    case "not_eligible":
      return translator.translate("wallet.checkout.failure_forbidden");
    case "rate_limited":
      return translator.translate("wallet.checkout.failure_rate_limited");
    default:
      return translator.translate("wallet.checkout.failure_generic");
  }
}
