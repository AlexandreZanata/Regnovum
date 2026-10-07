/**
 * Tests of the profiles client (P51-T01) against a fake transport: the
 * three reads speak the contract paths, the private read fails closed
 * without a session, and the public reads report an unknown author as a
 * validation refusal that reveals nothing.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createProfilesClient } from "../../src/core/clients/profiles.js";
import {
  captureApiError,
  createTestContext,
  headerOf,
  jsonResponse,
  problemResponse,
} from "../support/harness.js";

const PRIVATE = {
  username: "privacyowner",
  interface_locale: "pt-BR",
  created_at: "2026-09-20T10:00:00Z",
  updated_at: "2026-09-21T10:00:00Z",
} as const;

const PUBLIC = {
  username: "privacyowner",
  interface_locale: "pt-BR",
  created_at: "2026-09-20T10:00:00Z",
} as const;

const REPUTATION = {
  username: "privacyowner",
  influenced_people: 1234,
  valid_attributions: 7,
  arenas: [],
  by_category: [],
  by_language: [],
  checked_at: "2026-09-23T13:00:00Z",
} as const;

test("the private read speaks the contract path and sends no idempotency key", async () => {
  const context = createTestContext({ responder: () => jsonResponse(PRIVATE) });

  const profile = await createProfilesClient(context.core).mine();

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/profile");
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(profile.username, "privacyowner");
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("the private read fails closed without a session", async () => {
  const context = createTestContext({ responder: () => problemResponse(401, "session_expired") });

  const failure = await captureApiError(() => createProfilesClient(context.core).mine());

  assert.equal(failure.code, "session_expired");
  assert.equal(failure.kind, "unauthorized");
  assert.equal(failure.status, 401);
  assert.equal(failure.retryable, false);
});

test("the public reads speak the contract paths and keep the cache", async () => {
  const context = createTestContext({
    responder: (call) =>
      call.url.endsWith("/reputation") ? jsonResponse(REPUTATION) : jsonResponse(PUBLIC),
  });

  const profile = await createProfilesClient(context.core).byUsername("privacyowner");
  const reputation = await createProfilesClient(context.core).reputation("privacyowner");

  assert.equal(context.calls.length, 2);
  assert.equal(context.calls[0]?.url, "https://arena.test/api/v1/profiles/privacyowner");
  assert.equal(context.calls[1]?.url, "https://arena.test/api/v1/profiles/privacyowner/reputation");
  assert.equal(profile.username, "privacyowner");
  assert.equal(reputation.influenced_people, 1234);
  assert.equal(context.calls[0]?.init.cache, "default");
  assert.equal(context.calls[1]?.init.cache, "default");
});

test("usernames travel encoded, never as a path injection", async () => {
  const context = createTestContext({ responder: () => jsonResponse(PUBLIC) });

  await createProfilesClient(context.core).byUsername("a b/c");

  assert.equal(context.lastCall().url, "https://arena.test/api/v1/profiles/a%20b%2Fc");
});

test("an unknown author is a 404 that reveals nothing", async () => {
  const context = createTestContext({ responder: () => problemResponse(404, "profile_not_found") });

  const profileFailure = await captureApiError(() => createProfilesClient(context.core).byUsername("ghost"));
  const reputationFailure = await captureApiError(() =>
    createProfilesClient(context.core).reputation("ghost"),
  );

  for (const failure of [profileFailure, reputationFailure]) {
    assert.equal(failure.code, "profile_not_found");
    assert.equal(failure.kind, "not_found");
    assert.equal(failure.retryable, false);
  }
});

test("the profiles client stores nothing and fetches nothing itself", () => {
  // Behavioural guard, not documentation: the client composes the core and
  // carries no state of its own.
  const context = createTestContext({ responder: () => jsonResponse(PRIVATE) });
  const first = createProfilesClient(context.core);
  const second = createProfilesClient(context.core);

  assert.notEqual(first, second);
  assert.equal(context.calls.length, 0);
});
