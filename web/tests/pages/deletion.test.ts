/**
 * Tests of the deletion page presentation (P51-T05).
 *
 * They run the real generated catalogs in both locales: the request
 * entry shows the rule's consequences with the explicit confirmation,
 * the record renders requested, canceled or executed exactly as the
 * contract statuses declare (no fourth state), instants are formatted
 * instead of raw, and failures name only the server codes the backend
 * really emits.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  deletionFailure,
  deletionRequestView,
  deletionStatusView,
} from "../../src/pages/deletion.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type { AccountDeletionRequest } from "../../src/contracts/generated.js";
import type { Locale } from "../../src/i18n/locale.js";

/** A translator holding exactly the namespace the deletion page renders from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["auth"] });
}

const REQUESTED: AccountDeletionRequest = {
  status: "requested",
  requested_at: "2026-10-01T10:00:00Z",
  executed_at: null,
  canceled_at: null,
};

const CANCELED: AccountDeletionRequest = {
  status: "canceled",
  requested_at: "2026-10-01T10:00:00Z",
  executed_at: null,
  canceled_at: "2026-10-02T10:00:00Z",
};

const EXECUTED: AccountDeletionRequest = {
  status: "executed",
  requested_at: "2026-10-01T10:00:00Z",
  executed_at: "2026-10-08T10:00:00Z",
  canceled_at: null,
};

test("the request entry shows the rule consequences with the explicit confirmation", () => {
  const view = deletionRequestView(translatorOf("pt-BR"));

  assert.equal(view.heading, "Excluir minha conta");
  assert.equal(view.consequences.length, 6);
  assert.ok(view.consequences.some((line) => line.includes("7 dias")), "the cooling-off must be named");
  assert.ok(
    view.consequences.some((line) => line.includes("Inquisição")),
    "the page must deny being a sanction",
  );
  assert.ok(view.confirm.includes("Confirmo"), "the confirmation must be explicit");
  assert.equal(view.submit, "Pedir exclusão");
});

test("the request entry renders in en-US", () => {
  const view = deletionRequestView(translatorOf("en-US"));

  assert.equal(view.heading, "Delete my account");
  assert.ok(view.consequences.some((line) => line.includes("not a sanction")), `unexpected: ${view.consequences}`);
});

test("a requested record offers the cancel inside the window", () => {
  const view = deletionStatusView(translatorOf("pt-BR"), "pt-BR", REQUESTED);

  assert.equal(view.state, "requested");
  if (view.state === "requested") {
    assert.equal(view.status, "Exclusão pedida: a conta cai em 7 dias, salvo cancelamento.");
    assert.ok(!view.requestedAt.includes("2026-10-01T10:00:00Z"), "raw instant leaked");
    assert.equal(view.cancelSubmit, "Cancelar exclusão");
    assert.ok(view.cancelNote.length > 0);
  } else {
    assert.ok(false, "a requested record must stay requested");
  }
});

test("canceled and executed records are terminal without a cancel", () => {
  const canceled = deletionStatusView(translatorOf("en-US"), "en-US", CANCELED);
  assert.equal(canceled.state, "canceled");
  assert.equal(canceled.status, "Deletion canceled: the account keeps standing.");
  assert.ok(!("cancelSubmit" in canceled), "a canceled record offers no cancel");

  const executed = deletionStatusView(translatorOf("en-US"), "en-US", EXECUTED);
  assert.equal(executed.state, "executed");
  assert.equal(executed.status, "Deletion executed: this account no longer authenticates.");
  assert.ok(!("cancelSubmit" in executed), "an executed record offers no cancel");
});

test("failures name the real server codes and fall back otherwise", () => {
  const translator = translatorOf("en-US");

  assert.ok(deletionFailure(translator, "deletion_request_not_found").includes("No request of yours"));
  assert.ok(deletionFailure(translator, "deletion_not_cancellable").includes("already ended"));
  assert.ok(deletionFailure(translator, "account_not_eligible").includes("cannot request deletion"));
  assert.equal(
    deletionFailure(translator, "deletion_expired"),
    deletionFailure(translator, "something-the-backend-never-emits"),
    "an unknown code must fall back to the generic sentence",
  );
});
