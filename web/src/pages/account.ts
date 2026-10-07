/**
 * Account and public profile presentation (P51-T01).
 *
 * The page is a reader of three safe contracts and renders nothing it was
 * not given. `accountPresentation` projects the owner's private profile:
 * the username, the interface locale and the two instants the server
 * recorded — never an email, an identifier, a credential or any field the
 * contract does not carry, because the private document is exactly what a
 * shoulder-reader must not learn more from. `publicPresentation` projects
 * what any visitor may see, and `reputationPresentation` projects the
 * factual counts: influenced people and valid attributions formatted for
 * the resolved locale, with the derivation instant rendered by `Intl`
 * instead of shown as the RFC 3339 string the contract carries.
 *
 * There is no editor here and no ranking: the contract of these operations
 * is GET, the reputation document carries no score and no attributor
 * identity, and this module invents neither. Every string comes from the
 * catalog the runtime serves; the component that renders a view translates
 * nothing and formats nothing.
 */
import { formatInstant, formatNumber } from "../i18n/formats.js";
import type { PrivateProfile, ProfileReputation, PublicProfile } from "../contracts/generated.js";
import type { Locale } from "../i18n/locale.js";
import type { Translator } from "../i18n/translator.js";

/** Everything the account view renders for the owner's profile. */
export interface AccountView {
  readonly heading: string;
  readonly intro: string;
  readonly usernameLabel: string;
  readonly username: string;
  readonly localeLabel: string;
  readonly locale: string;
  readonly memberSince: string;
  readonly updatedAt: string;
}

/** Everything the public profile view renders for one author. */
export interface PublicProfileView {
  readonly heading: string;
  readonly intro: string;
  readonly usernameLabel: string;
  readonly username: string;
  readonly localeLabel: string;
  readonly locale: string;
  readonly memberSince: string;
}

/** Everything the reputation view renders: factual counts, never a rank. */
export interface ReputationView {
  readonly heading: string;
  readonly intro: string;
  readonly influencedPeople: string;
  readonly validAttributions: string;
  readonly checked: string;
}

/** accountPresentation projects the owner's private profile for one locale. */
export function accountPresentation(
  translator: Translator,
  locale: Locale,
  profile: PrivateProfile,
): AccountView {
  return {
    heading: translator.translate("auth.account.private_heading"),
    intro: translator.translate("auth.account.private_intro"),
    usernameLabel: translator.translate("auth.account.username_label"),
    username: profile.username,
    localeLabel: translator.translate("auth.account.locale_label"),
    locale: profile.interface_locale,
    memberSince: translator.translate("auth.account.member_since", {
      instant: formatInstant(locale, profile.created_at, { dateStyle: "medium", timeStyle: "short" }),
    }),
    updatedAt: translator.translate("auth.account.updated_at", {
      instant: formatInstant(locale, profile.updated_at, { dateStyle: "medium", timeStyle: "short" }),
    }),
  };
}

/** publicPresentation projects what any visitor may see of one author. */
export function publicPresentation(
  translator: Translator,
  locale: Locale,
  profile: PublicProfile,
): PublicProfileView {
  return {
    heading: translator.translate("auth.account.public_heading"),
    intro: translator.translate("auth.account.public_intro"),
    usernameLabel: translator.translate("auth.account.username_label"),
    username: profile.username,
    localeLabel: translator.translate("auth.account.locale_label"),
    locale: profile.interface_locale,
    memberSince: translator.translate("auth.account.member_since", {
      instant: formatInstant(locale, profile.created_at, { dateStyle: "medium", timeStyle: "short" }),
    }),
  };
}

/** reputationPresentation projects the factual counts of one author. */
export function reputationPresentation(
  translator: Translator,
  locale: Locale,
  reputation: ProfileReputation,
): ReputationView {
  return {
    heading: translator.translate("auth.account.reputation_heading"),
    intro: translator.translate("auth.account.reputation_intro"),
    influencedPeople: translator.translate("auth.account.influenced_people", {
      count: formatNumber(locale, reputation.influenced_people),
    }),
    validAttributions: translator.translate("auth.account.valid_attributions", {
      count: formatNumber(locale, reputation.valid_attributions),
    }),
    checked: translator.translate("auth.account.checked_at", {
      instant: formatInstant(locale, reputation.checked_at, { dateStyle: "medium", timeStyle: "short" }),
    }),
  };
}
