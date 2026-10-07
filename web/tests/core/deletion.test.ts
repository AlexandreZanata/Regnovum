/**
 * Tests of the account deletion client (P51-T05) against a fake
 * transport: the three operations speak the contract paths and
 * methods, the request carries no body and no idempotency key even
 * though the server replays by account, cancel carries no reason the
 * server would have to hide, and the failures the task names — a
 * missing request, a terminal 409 and a foreign owner — surface with
 * their kinds.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createDeletionClient } from "../../src/core/clients/deletion.js";
import {
  captureApiError,
  createTestContext,
  headerOf,
  jsonResponse,
  problemResponse,
} from "../support/harness.js";

const REQUESTED = {
  status: "requested",
  requested_at: "2026-10-01T10:00:00Z",
  executed_at: null,
  canceled_at: null,
} as const;

const CANCELED = {
  status: "canceled",
  requested_at: "2026-10-01T10:00:00Z",
  executed_at: null,
  canceled_at: "2026-10-02T10:00:00Z",
} as const;

test("requesting posts without a body and answers the record", async () => {
  const context = createTestContext({ responder: () => jsonResponse(REQUESTED, 202) });

  const answer = await createDeletionClient(context.core).request();

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/deletion");
  assert.equal(context.lastCall().init.body, undefined, "the request carries no reason and no password");
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(answer.status, "requested");
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("a lost request is a failure the person answers by asking again", async () => {
  const context = createTestContext({ responder: () => problemResponse(503, "unavailable") });

  const failure = await captureApiError(() => createDeletionClient(context.core).request());

  assert.equal(context.calls.length, 1, "the server replays by account; the core must not");
  assert.equal(failure.retryable, false);
});

test("reading reflects the owner record", async () => {
  const context = createTestContext({ responder: () => jsonResponse(REQUESTED) });

  const answer = await createDeletionClient(context.core).status();

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/deletion");
  assert.equal(answer.status, "requested");
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("a missing request reads as not found", async () => {
  const context = createTestContext({ responder: () => problemResponse(404, "deletion_request_not_found") });

  const failure = await captureApiError(() => createDeletionClient(context.core).status());

  assert.equal(failure.code, "deletion_request_not_found");
  assert.equal(failure.kind, "not_found");
});

test("another account reads exactly like a missing one", async () => {
  const context = createTestContext({ responder: () => problemResponse(404, "deletion_request_not_found") });

  const statusFailure = await captureApiError(() => createDeletionClient(context.core).status());
  assert.equal(statusFailure.code, "deletion_request_not_found");

  const cancelFailure = await captureApiError(() => createDeletionClient(context.core).cancel());
  assert.equal(cancelFailure.code, "deletion_request_not_found");
  assert.equal(cancelFailure.kind, "not_found");
});

test("canceling posts without a reason and answers the canceled record", async () => {
  const context = createTestContext({ responder: () => jsonResponse(CANCELED) });

  const answer = await createDeletionClient(context.core).cancel();

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/deletion/cancel");
  assert.equal(context.lastCall().init.body, undefined, "the reason is restricted evidence: never collected");
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(answer.status, "canceled");
});

test("a terminal request refuses cancellation and stays terminal", async () => {
  const context = createTestContext({ responder: () => problemResponse(409, "deletion_not_cancellable") });

  const failure = await captureApiError(() => createDeletionClient(context.core).cancel());

  assert.equal(failure.code, "deletion_not_cancellable");
  assert.equal(failure.kind, "conflict");
  assert.equal(failure.retryable, false);
  assert.equal(context.calls.length, 1);
});

test("the operations fail closed without a session", async () => {
  const context = createTestContext({ responder: () => problemResponse(401, "unauthorized") });

  const failure = await captureApiError(() => createDeletionClient(context.core).status());

  assert.equal(failure.code, "unauthorized");
  assert.equal(failure.kind, "unauthorized");
});
