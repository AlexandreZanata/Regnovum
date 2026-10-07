/**
 * Tests of the sessions client (P51-T02) against a fake transport: the
 * three operations speak the contract paths, methods and bodies, mutations
 * are never retried and never carry an idempotency key, and the failures
 * the task names — 401, 403, 404, 409 — surface with their kinds.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createSessionsClient } from "../../src/core/clients/sessions.js";
import {
  bodyOf,
  captureApiError,
  createTestContext,
  headerOf,
  jsonResponse,
  problemResponse,
} from "../support/harness.js";

const LIST = {
  sessions: [
    {
      id: "sess-current",
      created_at: "2026-09-20T10:00:00Z",
      last_seen_at: "2026-09-23T10:00:00Z",
      expires_at: "2026-10-04T10:00:00Z",
      ip_address: "203.0.113.7",
      user_agent: "TestBrowser/1.0",
      current: true,
    },
    {
      id: "sess-other",
      created_at: "2026-09-21T10:00:00Z",
      last_seen_at: "2026-09-22T10:00:00Z",
      expires_at: "2026-10-05T10:00:00Z",
      current: false,
    },
  ],
} as const;

test("the list speaks the contract path and keeps the session cache policy", async () => {
  const context = createTestContext({ responder: () => jsonResponse(LIST) });

  const list = await createSessionsClient(context.core).list();

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/sessions");
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(list.sessions.length, 2);
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("the list fails closed without a session", async () => {
  const context = createTestContext({ responder: () => problemResponse(401, "session_expired") });

  const failure = await captureApiError(() => createSessionsClient(context.core).list());

  assert.equal(failure.code, "session_expired");
  assert.equal(failure.kind, "unauthorized");
  assert.equal(failure.retryable, false);
});

test("rotation posts without a body and is never retried", async () => {
  const context = createTestContext({
    responder: () => jsonResponse({ status: "rotated", session_id: "sess-new" }),
  });

  const answer = await createSessionsClient(context.core).rotate();

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/sessions/rotation");
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(answer.status, "rotated");
});

test("a lost rotation response is a failure, never a replay", async () => {
  const context = createTestContext({ responder: () => problemResponse(503, "unavailable") });

  const failure = await captureApiError(() => createSessionsClient(context.core).rotate());

  assert.equal(context.calls.length, 1, "rotation without an idempotent contract must not retry");
  assert.equal(failure.retryable, false);
});

test("revocation posts the identifier and the password, and nothing else", async () => {
  const context = createTestContext({ responder: () => jsonResponse({ status: "revoked" }) });

  const answer = await createSessionsClient(context.core).revoke({
    session_id: "sess-other",
    password: "correct horse battery staple",
  });

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/sessions/revocation");
  assert.deepEqual(bodyOf(context.lastCall()), {
    session_id: "sess-other",
    password: "correct horse battery staple",
  });
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(answer.status, "revoked");
});

test("revocation surfaces 403, 404 and 409 with their kinds and never retries", async () => {
  for (const [status, code, kind] of [
    [403, "forbidden_action", "forbidden"],
    [404, "session_not_found", "not_found"],
    [409, "session_already_revoked", "conflict"],
  ] as const) {
    const context = createTestContext({ responder: () => problemResponse(status, code) });
    const failure = await captureApiError(() =>
      createSessionsClient(context.core).revoke({ session_id: "sess-other", password: "pw" }),
    );
    assert.equal(failure.code, code, `status ${status}`);
    assert.equal(failure.kind, kind, `status ${status}`);
    assert.equal(failure.retryable, false, `status ${status}`);
    assert.equal(context.calls.length, 1, `status ${status}: no replay of a critical mutation`);
  }
});

test("a foreign session is refused exactly like a missing one", async () => {
  const context = createTestContext({ responder: () => problemResponse(404, "session_not_found") });

  const failure = await captureApiError(() =>
    createSessionsClient(context.core).revoke({ session_id: "sess-of-another-account", password: "pw" }),
  );

  assert.equal(failure.status, 404);
  assert.equal(failure.kind, "not_found");
});
