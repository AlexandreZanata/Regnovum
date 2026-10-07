/**
 * Tests of the moderation filing presentation (P55-T01).
 *
 * They run the real generated catalogs in both locales: the
 * vocabularies stay closed, the receipts carry the real
 * identifier with no sensitive data, and failures name only
 * the server codes the backend really emits. Nothing here
 * lists, states or queues: there is no tribunal surface.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  REPORT_REASONS,
  REPORT_TARGETS,
  appealReceipt,
  isReportReason,
  isReportTarget,
  moderationFailure,
  reportReceipt,
} from "../../src/pages/moderation.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type { ModerationAppeal, ModerationReport } from "../../src/contracts/generated.js";
import type { Locale } from "../../src/i18n/locale.js";

/** A translator holding exactly the namespace the filing views render from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["moderation"] });
}

const REPORT: ModerationReport = {
  report_id: "rep-1",
  replayed: false,
  rate_limited: false,
  reports_in_window: 1,
};

const APPEAL: ModerationAppeal = { appeal_id: "apl-1", action_id: "act-1", replayed: false };

test("the vocabularies stay closed on the contract's list", () => {
  assert.deepEqual(REPORT_TARGETS, ["arena", "argument", "profile"]);
  assert.equal(REPORT_REASONS.length, 11);
  assert.ok(isReportTarget("argument"));
  assert.ok(isReportReason("harassment"));
  assert.ok(!isReportTarget("tribunal"), "no private tribunal target travels");
  assert.ok(!isReportTarget(""), "a blank target never becomes a request");
  assert.ok(!isReportReason("dislike"), "an open reason never becomes a request");
});

test("the report receipt carries the real identifier with no sensitive data", () => {
  const view = reportReceipt(translatorOf("pt-BR"), REPORT);

  assert.equal(view.filed, "Denúncia registrada: rep-1");
  assert.equal(view.rateNote, null);
  const serialized = JSON.stringify(view).toLowerCase();
  for (const marker of ["context", "email", "account", "queue", "tribunal", "decision"]) {
    assert.ok(!serialized.includes(marker), `restricted marker leaked: ${marker}`);
  }

  const en = reportReceipt(translatorOf("en-US"), REPORT);
  assert.equal(en.filed, "Report filed: rep-1");
});

test("a replay renders the recorded identity and a rate signal warns", () => {
  const replayed = reportReceipt(translatorOf("en-US"), {
    ...REPORT,
    replayed: true,
    rate_limited: true,
  });

  assert.equal(replayed.filed, "Report already filed: rep-1");
  assert.ok(replayed.rateNote !== null && replayed.rateNote.includes("Too many"));
});

test("the appeal receipt contests exactly one action", () => {
  const view = appealReceipt(translatorOf("en-US"), APPEAL);

  assert.equal(view.filed, "Appeal filed: apl-1");

  const replayed = appealReceipt(translatorOf("pt-BR"), { ...APPEAL, replayed: true });
  assert.equal(replayed.filed, "Recurso já registrado: apl-1");
});

test("filing failures name the real server codes and fall back otherwise", () => {
  const translator = translatorOf("en-US");

  assert.ok(moderationFailure(translator, "invalid_request").includes("not valid"));
  assert.ok(moderationFailure(translator, "unauthorized").includes("Sign in"));
  assert.ok(moderationFailure(translator, "forbidden").includes("cannot do this"));
  assert.ok(moderationFailure(translator, "step_up_required").includes("again"));
  assert.ok(moderationFailure(translator, "not_found").includes("no longer exists"));
  assert.ok(moderationFailure(translator, "conflict").includes("conflicts"));
  assert.equal(
    moderationFailure(translator, "something-the-backend-never-emits"),
    moderationFailure(translator, "unknown-code"),
    "an unknown code must fall back to the generic sentence",
  );
});
