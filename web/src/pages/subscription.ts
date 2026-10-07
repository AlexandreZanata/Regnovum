/**
 * Member subscription and customer portal presentation (P54-T04).
 *
 * The account reads one projection and opens one hosted session,
 * rendering nothing it was not given. `subscriptionPresentation`
 * projects the lifecycle the local mirror carries — status,
 * product, market and the current period end — with the instant
 * rendered by `Intl` instead of shown as the RFC 3339 string the
 * contract carries, and absence rendered as absence: no
 * subscription is a state, never a failure. `portalView`
 * projects the hosted portal URL through the redirect guard the
 * checkout surface already owns (reused, not duplicated): only a
 * safe shape is followed, and the provider identifiers behind
 * the session never serialize, so none render either.
 *
 * Two refusals shape this module. Benefits follow the rules in
 * force, which the browser does not ratify and does not restate:
 * the page warns that the current and legacy benefits apply as
 * the rules declare, without inventing what they are. And no
 * card number, no credential and no token ever travels here —
 * the portal call carries at most the operation token, and the
 * answer carries only the URL. Every string comes from the
 * catalog the runtime serves; the component that renders a view
 * translates nothing and formats nothing.
 */
import { formatInstant } from "../i18n/formats.js";
import type { BillingPortal, BillingSubscription } from "../contracts/generated.js";
import type { Locale } from "../i18n/locale.js";
import type { Translator } from "../i18n/translator.js";
import { isSafeRedirectUrl } from "./checkout.js";

/** Everything the Member view renders for one projection. */
export interface SubscriptionView {
  readonly heading: string;
  readonly intro: string;
  readonly state: string;
  readonly period: string | null;
  readonly cancelNote: string | null;
  readonly benefitNote: string;
}

/** Everything the page renders for one portal session. */
export interface PortalView {
  readonly portalUrl: string | null;
  readonly note: string;
}

/**
 * subscriptionPresentation projects one Member projection for
 * one locale. Without a subscription only the absent sentence
 * renders; with one, the lifecycle renders verbatim — it is the
 * provider's vocabulary mirrored locally — beside the period
 * the mirror carries. A locale switch re-renders the same
 * instant; it never moves it.
 */
export function subscriptionPresentation(
  translator: Translator,
  locale: Locale,
  subscription: BillingSubscription,
): SubscriptionView {
  const benefitNote = translator.translate("wallet.subscription.benefit_note");
  if (!subscription.has_subscription) {
    return {
      heading: translator.translate("wallet.subscription.heading"),
      intro: translator.translate("wallet.subscription.intro"),
      state: translator.translate("wallet.subscription.absent"),
      period: null,
      cancelNote: null,
      benefitNote,
    };
  }
  const period =
    subscription.current_period_end === undefined || subscription.current_period_end === null
      ? null
      : translator.translate("wallet.subscription.period", {
          instant: formatInstant(locale, subscription.current_period_end, {
            dateStyle: "medium",
            timeStyle: "short",
          }),
        });
  return {
    heading: translator.translate("wallet.subscription.heading"),
    intro: translator.translate("wallet.subscription.intro"),
    state: translator.translate("wallet.subscription.state", {
      status: subscription.status ?? "unknown",
      product: subscription.product ?? "unknown",
      market: subscription.market ?? "unknown",
    }),
    period,
    cancelNote:
      subscription.cancel_at_period_end === true
        ? translator.translate("wallet.subscription.canceling")
        : null,
    benefitNote,
  };
}

/**
 * portalView projects one portal session for one locale. Only a
 * safe shape is followed; anything else renders the
 * unavailable sentence with no destination. The return trip
 * grants nothing by itself: the page re-reads the projection
 * instead of assuming a change.
 */
export function portalView(translator: Translator, portal: BillingPortal): PortalView {
  if (isSafeRedirectUrl(portal.portal_url)) {
    return {
      portalUrl: portal.portal_url,
      note: translator.translate("wallet.subscription.portal_note"),
    };
  }
  return {
    portalUrl: null,
    note: translator.translate("wallet.subscription.unavailable"),
  };
}

/**
 * subscriptionFailure translates a refusal by the server code
 * the backend really emits. Without a stored customer the
 * portal answers `no_billing_customer`; without a session both
 * answer `unauthorized`; an unavailable query or a fallen
 * provider fails without changing anything. Anything else
 * falls back to the generic sentence instead of inventing a
 * meaning.
 */
export function subscriptionFailure(translator: Translator, serverCode: string): string {
  switch (serverCode) {
    case "unauthorized":
      return translator.translate("wallet.subscription.failure_unauthorized");
    case "no_billing_customer":
      return translator.translate("wallet.subscription.failure_no_customer");
    case "rate_limited":
      return translator.translate("wallet.subscription.failure_rate_limited");
    default:
      return translator.translate("wallet.subscription.failure_generic");
  }
}
