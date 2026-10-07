/**
 * Tests of the exports page presentation (P51-T04).
 *
 * They run the real generated catalogs in both locales: the request
 * entry names the fresh-authentication need, the job renders pending
 * or ready exactly as the contract statuses declare (no third state,
 * no polling), the file descriptor builds its name from the opaque
 * identifier with the declared media type and no server URL, and
 * failures name only the server codes the backend really emits.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  exportFailure,
  exportFileDescriptor,
  exportJobView,
  exportRequestView,
} from "../../src/pages/exports.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type { PersonalExportDocument, PersonalExportJob } from "../../src/contracts/generated.js";
import type { Locale } from "../../src/i18n/locale.js";

/** A translator holding exactly the namespace the exports page renders from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["auth"] });
}

const PENDING: PersonalExportJob = {
  export_id: "018f6b2a-0000-7000-8000-000000000001",
  status: "requested",
  download_token: "single-use-token",
};

const READY: PersonalExportJob = {
  export_id: "018f6b2a-0000-7000-8000-000000000001",
  status: "ready",
  download_token: "single-use-token",
};

const DOCUMENT = {
  schema_version: 1,
  generated_at: "2026-10-01T10:00:00Z",
  account: { email: "ada@example.test" },
  arena_drafts: [],
  arguments: [],
  billing: {},
  excluded_categories: ["security_restricted"],
  passes: { consumptions: [] },
  position_changes: [],
  positions: [],
  wallet: {},
} as unknown as PersonalExportDocument;

test("the request entry renders in pt-BR with the fresh-authentication note", () => {
  const view = exportRequestView(translatorOf("pt-BR"));

  assert.equal(view.heading, "Exportar meus dados");
  assert.equal(view.submit, "Pedir exportação");
  assert.ok(view.stepUpNote.length > 0, "the request must name the step-up need");
});

test("the request entry renders in en-US", () => {
  const view = exportRequestView(translatorOf("en-US"));

  assert.equal(view.heading, "Export my data");
  assert.equal(view.submit, "Request export");
});

test("a requested job is pending with no download and no third state", () => {
  const view = exportJobView(translatorOf("pt-BR"), PENDING);

  assert.equal(view.state, "pending");
  assert.equal(view.heading, "Exportação pedida");
  assert.ok(!("submit" in view), "a pending job offers no download");
});

test("a ready job downloads in en-US", () => {
  const view = exportJobView(translatorOf("en-US"), READY);

  assert.equal(view.state, "ready");
  assert.equal(view.heading, "Export ready");
  if (view.state === "ready") {
    assert.equal(view.submit, "Download file");
  } else {
    assert.ok(false, "a ready job must carry its submit");
  }
});

test("the file descriptor names the opaque identifier with the declared type", () => {
  const file = exportFileDescriptor(translatorOf("pt-BR"), READY, DOCUMENT);

  assert.equal(file.filename, "personal-export-018f6b2a-0000-7000-8000-000000000001.json");
  assert.equal(file.mediaType, "application/json");
  assert.deepEqual(JSON.parse(file.text), JSON.parse(JSON.stringify(DOCUMENT)));
  assert.ok(file.revokeNote.length > 0, "the descriptor must remind the component to revoke");
  const serialized = JSON.stringify(file);
  assert.ok(!serialized.includes("http"), "the descriptor carries no server URL");
  assert.ok(!serialized.includes("blob:"), "the descriptor carries no object URL");
});

test("failures name the real server codes and fall back otherwise", () => {
  const translator = translatorOf("en-US");

  assert.ok(exportFailure(translator, "step_up_required").includes("stale"));
  assert.ok(exportFailure(translator, "export_not_found").includes("no more"));
  assert.ok(exportFailure(translator, "invalid_export_token").includes("receipt"));
  assert.equal(
    exportFailure(translator, "export_expired"),
    exportFailure(translator, "something-the-backend-never-emits"),
    "an unknown code must fall back to the generic sentence",
  );
});
