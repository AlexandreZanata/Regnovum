/**
 * Tests of the MFA page presentation (P51-T03).
 *
 * They run the real generated catalogs in both locales: the enrollment
 * shows the secret of the begin answer, the confirmation shows the
 * recovery codes of the confirm answer, the code field carries the
 * numeric keyboard with the one-time-code autocomplete, failures name
 * only the server codes the backend really emits, and the elevation
 * notice denies — never grants — any role.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  codeField,
  confirmationPresentation,
  elevationNotice,
  enrollmentPresentation,
  mfaFailure,
} from "../../src/pages/mfa.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type { MFAConfirmResponse, MFAEnrollmentResponse } from "../../src/contracts/generated.js";
import type { Locale } from "../../src/i18n/locale.js";

/** A translator holding exactly the namespace the MFA page renders from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["auth"] });
}

const ENROLLMENT: MFAEnrollmentResponse = {
  secret: "JBSWY3DPEHPK3PXP",
  uri: "otpauth://totp/regnum?secret=JBSWY3DPEHPK3PXP",
};

const CONFIRMATION: MFAConfirmResponse = {
  backup_codes: ["r1-first", "r2-second", "r3-third"],
};

test("the enrollment shows the one-time secret from the begin answer", () => {
  const view = enrollmentPresentation(translatorOf("pt-BR"), ENROLLMENT);

  assert.equal(view.heading, "Segundo fator");
  assert.equal(view.secret, "JBSWY3DPEHPK3PXP");
  assert.equal(view.uri, "otpauth://totp/regnum?secret=JBSWY3DPEHPK3PXP");
  assert.equal(view.submit, "Confirmar ativação");
  assert.equal(view.field.inputMode, "numeric");
  assert.equal(view.field.autoComplete, "one-time-code");
  const serialized = JSON.stringify(view);
  assert.ok(!serialized.includes("backup"), "the enrollment must not mention recovery codes");
  assert.ok(!serialized.includes("password"), "the enrollment must not mention passwords");
});

test("the enrollment renders in en-US", () => {
  const view = enrollmentPresentation(translatorOf("en-US"), ENROLLMENT);

  assert.equal(view.heading, "Second factor");
  assert.equal(view.field.label, "Authenticator code");
  assert.equal(view.secret, "JBSWY3DPEHPK3PXP");
});

test("the confirmation shows the one-time codes and denies any role", () => {
  const view = confirmationPresentation(translatorOf("pt-BR"), CONFIRMATION);

  assert.equal(view.heading, "Códigos de recuperação");
  assert.deepEqual(view.codes, ["r1-first", "r2-second", "r3-third"]);
  assert.ok(view.onceNote.length > 0, "the one-time warning must travel with the codes");
  assert.ok(view.stepUpNote.includes("somente"), "step-up elevates only the calling session");
  assert.ok(
    view.stepUpNote.includes("Não concede cargo"),
    `step-up must deny granting a role: ${view.stepUpNote}`,
  );
});

test("the confirmation denies any role in en-US", () => {
  const view = confirmationPresentation(translatorOf("en-US"), CONFIRMATION);

  assert.ok(view.stepUpNote.includes("no role"), `unexpected step-up note: ${view.stepUpNote}`);
  assert.equal(view.recoverSubmit, "Use a recovery code");
});

test("the elevation notice names the path and grants nothing", () => {
  const stepUp = elevationNotice(translatorOf("pt-BR"), "step-up");
  const recovery = elevationNotice(translatorOf("pt-BR"), "recovery");

  assert.ok(stepUp.includes("elevada"), `unexpected notice: ${stepUp}`);
  assert.ok(recovery.includes("recuperação"), `unexpected notice: ${recovery}`);
  assert.ok(!stepUp.includes("cargo"), "step-up grants no role");
  assert.ok(!recovery.includes("cargo"), "recovery grants no role");
});

test("failures name the real server codes and fall back otherwise", () => {
  const translator = translatorOf("en-US");

  assert.equal(mfaFailure(translator, "mfa_code_invalid"), "That code does not check out. Try the current code.");
  const replayed = mfaFailure(translator, "mfa_code_replayed");
  assert.ok(replayed.includes("already used"), `unexpected replayed: ${replayed}`);
  assert.ok(mfaFailure(translator, "mfa_enrollment_missing").includes("No pending enrollment"));
  assert.ok(mfaFailure(translator, "mfa_already_enrolled").includes("never replaces"));
  assert.ok(mfaFailure(translator, "mfa_not_enrolled").includes("no confirmed second factor"));
  assert.ok(mfaFailure(translator, "mfa_step_up_required").includes("lapsed"));
  assert.equal(
    mfaFailure(translator, "mfa_code_expired"),
    mfaFailure(translator, "something-the-backend-never-emits"),
    "an unknown code must fall back to the generic sentence",
  );
});

test("the code field is numeric with the one-time-code autocomplete", () => {
  for (const locale of ["pt-BR", "en-US"] as const) {
    const field = codeField(translatorOf(locale));
    assert.equal(field.inputMode, "numeric");
    assert.equal(field.autoComplete, "one-time-code");
    assert.ok(field.label.length > 0, locale);
    assert.ok(field.hint.length > 0, locale);
  }
});
