/**
 * Tests of the account page presentation (P51-T01).
 *
 * They run the real generated catalogs in both locales, so a key that
 * moves, a placeholder that changes or a locale that stops grouping
 * numbers fails here instead of on a page a person reads. The views carry
 * only allowlisted fields — username, locale and instants — and the raw
 * contract values (RFC 3339 instants, ungrouped counts) never reach the
 * reader.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  accountPresentation,
  publicPresentation,
  reputationPresentation,
} from "../../src/pages/account.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type { PrivateProfile, ProfileReputation, PublicProfile } from "../../src/contracts/generated.js";
import type { Locale } from "../../src/i18n/locale.js";

/** A translator holding exactly the namespaces the account views render from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["auth"] });
}

const PRIVATE: PrivateProfile = {
  username: "privacyowner",
  interface_locale: "pt-BR",
  created_at: "2026-09-20T10:00:00Z",
  updated_at: "2026-09-21T10:00:00Z",
};

const PUBLIC: PublicProfile = {
  username: "privacyowner",
  interface_locale: "pt-BR",
  created_at: "2026-09-20T10:00:00Z",
};

const REPUTATION: ProfileReputation = {
  username: "privacyowner",
  influenced_people: 1234,
  valid_attributions: 7,
  arenas: [],
  by_category: [],
  by_language: [],
  checked_at: "2026-09-23T13:00:00Z",
};

test("the private view renders in pt-BR without leaking anything beyond the contract", () => {
  const view = accountPresentation(translatorOf("pt-BR"), "pt-BR", PRIVATE);

  assert.equal(view.heading, "Sua conta");
  assert.equal(view.intro, "O que o servidor guarda sobre você nesta conta.");
  assert.equal(view.usernameLabel, "Nome de usuário");
  assert.equal(view.username, "privacyowner");
  assert.equal(view.locale, "pt-BR");
  assert.ok(!view.memberSince.includes(PRIVATE.created_at), "the instant must be formatted, never raw");
  assert.ok(!view.updatedAt.includes(PRIVATE.updated_at), "the instant must be formatted, never raw");
  assert.ok(!JSON.stringify(view).includes("@"), "no email address may travel in the view");
});

test("the private view renders in en-US", () => {
  const view = accountPresentation(translatorOf("en-US"), "en-US", PRIVATE);

  assert.equal(view.heading, "Your account");
  assert.equal(view.usernameLabel, "Username");
  assert.ok(!view.memberSince.includes(PRIVATE.created_at), "the instant must be formatted, never raw");
});

test("the public view carries only what any visitor may see", () => {
  for (const locale of ["pt-BR", "en-US"] as const) {
    const view = publicPresentation(translatorOf(locale), locale, PUBLIC);

    assert.deepEqual(Object.keys(view).sort(), [
      "heading",
      "intro",
      "locale",
      "localeLabel",
      "memberSince",
      "username",
      "usernameLabel",
    ]);
    assert.ok(!view.memberSince.includes(PUBLIC.created_at), `${locale}: raw instant leaked`);
  }
  assert.equal(publicPresentation(translatorOf("pt-BR"), "pt-BR", PUBLIC).heading, "Perfil público");
  assert.equal(publicPresentation(translatorOf("en-US"), "en-US", PUBLIC).heading, "Public profile");
});

test("the reputation view renders factual counts grouped for the locale, never a rank", () => {
  const pt = reputationPresentation(translatorOf("pt-BR"), "pt-BR", REPUTATION);
  const en = reputationPresentation(translatorOf("en-US"), "en-US", REPUTATION);

  assert.equal(pt.heading, "Reputação factual");
  assert.equal(pt.influencedPeople, "Pessoas influenciadas: 1.234");
  assert.equal(en.influencedPeople, "People influenced: 1,234");
  assert.equal(pt.validAttributions, "Atribuições válidas: 7");
  assert.ok(!pt.checked.includes(REPUTATION.checked_at), "the instant must be formatted, never raw");
  const serialized = JSON.stringify(pt);
  assert.ok(!serialized.includes("rank"), "no ranking may be invented");
  assert.ok(!serialized.includes("score"), "no score may be invented");
});
