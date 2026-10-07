/**
 * Tests of the arena drafts client (P52-T03; publication and closure
 * P52-T05) against a fake transport: the list speaks the contract
 * path with the account cache policy, the creation posts exactly the
 * contract fields — never a price, a pass or any debit value — and
 * neither operation is retried nor carries an idempotency key. A lost
 * creation is a failure the person answers by asking again, never a
 * replay the core invents. Publication spends the single pass on the
 * server and closure ends the published Arena; both are single-shot
 * calls the core never replays, with the server itself resolving a
 * repeated call by state.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createDraftsClient } from "../../src/core/clients/drafts.js";
import {
  bodyOf,
  captureApiError,
  createTestContext,
  headerOf,
  jsonResponse,
  problemResponse,
} from "../support/harness.js";

const DRAFT = {
  id: "018f6b2a-0000-7000-8000-000000000001",
  slug: null,
  statement: "Máquinas podem ser responsáveis?",
  context: null,
  category: "philosophy",
  language: "pt-BR",
  status: "draft",
  version: 1,
  created_at: "2026-10-01T10:00:00Z",
  published_at: null,
  closes_at: null,
} as const;

test("the list speaks the contract path and keeps the session cache policy", async () => {
  const context = createTestContext({ responder: () => jsonResponse({ items: [DRAFT] }) });

  const list = await createDraftsClient(context.core).list();

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/arena-drafts");
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(list.items.length, 1);
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("the list fails closed without a session", async () => {
  const context = createTestContext({ responder: () => problemResponse(401, "unauthorized") });

  const failure = await captureApiError(() => createDraftsClient(context.core).list());

  assert.equal(failure.code, "unauthorized");
  assert.equal(failure.kind, "unauthorized");
});

test("creation posts the contract fields and nothing else", async () => {
  const context = createTestContext({ responder: () => jsonResponse(DRAFT, 201) });

  const answer = await createDraftsClient(context.core).create({
    statement: "Máquinas podem ser responsáveis?",
    category: "philosophy",
    language: "pt-BR",
  });

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/arena-drafts");
  assert.deepEqual(bodyOf(context.lastCall()), {
    statement: "Máquinas podem ser responsáveis?",
    category: "philosophy",
    language: "pt-BR",
  });
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(answer.status, "draft");
  assert.equal(answer.version, 1);
});

test("creation carries no debit value anywhere", async () => {
  const context = createTestContext({ responder: () => jsonResponse(DRAFT, 201) });

  await createDraftsClient(context.core).create({
    statement: "Máquinas podem ser responsáveis?",
    context: "Contexto oferecido pelo autor.",
    category: "philosophy",
    language: "pt-BR",
  });

  const serialized = JSON.stringify(bodyOf(context.lastCall()));
  for (const marker of ["price", "pass", "ink", "debit", "cost", "amount"]) {
    assert.ok(!serialized.toLowerCase().includes(marker), `debit marker leaked: ${marker}`);
  }
});

test("creation surfaces validation with its kind and never retries", async () => {
  for (const code of ["arena_statement_too_short", "arena_statement_too_long", "arena_invalid_category"] as const) {
    const context = createTestContext({ responder: () => problemResponse(400, code) });
    const failure = await captureApiError(() =>
      createDraftsClient(context.core).create({ statement: "curta", category: "philosophy", language: "pt-BR" }),
    );
    assert.equal(failure.code, code);
    assert.equal(failure.kind, "validation");
    assert.equal(failure.retryable, false);
    assert.equal(context.calls.length, 1, `${code}: no replay of a creation`);
  }
});

test("a lost creation is a failure, never a second draft by replay", async () => {
  const context = createTestContext({ responder: () => problemResponse(503, "unavailable") });

  const failure = await captureApiError(() =>
    createDraftsClient(context.core).create({
      statement: "Máquinas podem ser responsáveis?",
      category: "philosophy",
      language: "pt-BR",
    }),
  );

  assert.equal(context.calls.length, 1, "without an idempotent contract the core must not replay");
  assert.equal(failure.retryable, false);
});

test("reading addresses the opaque identifier and nothing else", async () => {
  const context = createTestContext({ responder: () => jsonResponse(DRAFT) });

  const answer = await createDraftsClient(context.core).get("018f6b2a-0000-7000-8000-000000000001");

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(
    context.lastCall().url,
    "https://arena.test/api/v1/me/arena-drafts/018f6b2a-0000-7000-8000-000000000001",
  );
  assert.equal(answer.version, 1);
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("another account reads exactly like a missing draft", async () => {
  const context = createTestContext({ responder: () => problemResponse(404, "arena_not_found") });

  const failure = await captureApiError(() => createDraftsClient(context.core).get("someone-elses-draft"));

  assert.equal(failure.code, "arena_not_found");
  assert.equal(failure.kind, "not_found");
});

test("updating sends the replacement with the version it read", async () => {
  const updated = { ...DRAFT, statement: "Nova formulação da controvérsia?", version: 2 };
  const context = createTestContext({ responder: () => jsonResponse(updated) });

  const answer = await createDraftsClient(context.core).update("018f6b2a-0000-7000-8000-000000000001", {
    statement: "Nova formulação da controvérsia?",
    category: "science",
    language: "pt-BR",
    expected_version: 1,
  });

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "PATCH");
  assert.equal(
    context.lastCall().url,
    "https://arena.test/api/v1/me/arena-drafts/018f6b2a-0000-7000-8000-000000000001",
  );
  assert.deepEqual(bodyOf(context.lastCall()), {
    statement: "Nova formulação da controvérsia?",
    category: "science",
    language: "pt-BR",
    expected_version: 1,
  });
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(answer.version, 2);
});

test("updating publishes nothing: the body carries no transition", async () => {
  const context = createTestContext({ responder: () => jsonResponse({ ...DRAFT, version: 2 }) });

  await createDraftsClient(context.core).update("draft-1", {
    statement: "Nova formulação da controvérsia?",
    category: "science",
    language: "pt-BR",
    expected_version: 1,
  });

  const keys = Object.keys(bodyOf(context.lastCall()) as Record<string, unknown>).sort();
  assert.deepEqual(keys, ["category", "expected_version", "language", "statement"]);
});

test("a stale version fails without a write and without a retry", async () => {
  const context = createTestContext({ responder: () => problemResponse(409, "version_conflict") });

  const failure = await captureApiError(() =>
    createDraftsClient(context.core).update("draft-1", {
      statement: "Nova formulação da controvérsia?",
      category: "science",
      language: "pt-BR",
      expected_version: 1,
    }),
  );

  assert.equal(failure.code, "version_conflict");
  assert.equal(failure.kind, "conflict");
  assert.equal(failure.retryable, false);
  assert.equal(context.calls.length, 1, "a conflicted replacement must never replay");
});

test("deleting answers no body and is never replayed", async () => {
  const context = createTestContext({ responder: () => new Response(null, { status: 204 }) });

  await createDraftsClient(context.core).remove("018f6b2a-0000-7000-8000-000000000001");

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "DELETE");
  assert.equal(
    context.lastCall().url,
    "https://arena.test/api/v1/me/arena-drafts/018f6b2a-0000-7000-8000-000000000001",
  );
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
});

test("deleting a missing or foreign draft reads as not found", async () => {
  const context = createTestContext({ responder: () => problemResponse(404, "arena_not_found") });

  const failure = await captureApiError(() => createDraftsClient(context.core).remove("gone-or-foreign"));

  assert.equal(failure.code, "arena_not_found");
  assert.equal(failure.kind, "not_found");
  assert.equal(context.calls.length, 1);
});

test("publication posts the publish subresource with no body at all", async () => {
  const published = { ...DRAFT, status: "published", slug: "maquinas-podem-ser-responsaveis-018f6b2a", version: 1 };
  const context = createTestContext({ responder: () => jsonResponse(published) });

  const answer = await createDraftsClient(context.core).publish("018f6b2a-0000-7000-8000-000000000001");

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(
    context.lastCall().url,
    "https://arena.test/api/v1/me/arena-drafts/018f6b2a-0000-7000-8000-000000000001/publish",
  );
  assert.equal(context.lastCall().init.body, undefined, "the publish transition carries no body: no price, no pass");
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(answer.status, "published");
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("publication without a pass fails and writes nothing", async () => {
  const context = createTestContext({ responder: () => problemResponse(409, "no_pass_available") });

  const failure = await captureApiError(() =>
    createDraftsClient(context.core).publish("018f6b2a-0000-7000-8000-000000000001"),
  );

  assert.equal(failure.code, "no_pass_available");
  assert.equal(failure.kind, "conflict");
  assert.equal(failure.retryable, false);
  assert.equal(context.calls.length, 1, "a refused publish must never replay");
});

test("publication of a missing or foreign draft reads as not found", async () => {
  const context = createTestContext({ responder: () => problemResponse(404, "arena_not_found") });

  const failure = await captureApiError(() => createDraftsClient(context.core).publish("gone-or-foreign"));

  assert.equal(failure.code, "arena_not_found");
  assert.equal(failure.kind, "not_found");
  assert.equal(context.calls.length, 1);
});

test("closure posts the owner close subresource with no body at all", async () => {
  const closed = { ...DRAFT, status: "closed", slug: "maquinas-podem-ser-responsaveis-018f6b2a", version: 1 };
  const context = createTestContext({ responder: () => jsonResponse(closed) });

  const answer = await createDraftsClient(context.core).close("018f6b2a-0000-7000-8000-000000000001");

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(
    context.lastCall().url,
    "https://arena.test/api/v1/me/arenas/018f6b2a-0000-7000-8000-000000000001/close",
  );
  assert.equal(context.lastCall().init.body, undefined, "the close transition carries no body");
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(answer.status, "closed");
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("closure answers the closed arena when it is already closed", async () => {
  const closed = { ...DRAFT, status: "closed", slug: "maquinas-podem-ser-responsaveis-018f6b2a", version: 1 };
  const context = createTestContext({ responder: () => jsonResponse(closed) });

  const answer = await createDraftsClient(context.core).close("018f6b2a-0000-7000-8000-000000000001");

  assert.equal(answer.status, "closed", "the replay resolves by state, without a second transition");
  assert.equal(context.calls.length, 1);
});

test("closure of a draft fails as an invalid transition, never retried", async () => {
  const context = createTestContext({ responder: () => problemResponse(409, "invalid_status_change") });

  const failure = await captureApiError(() => createDraftsClient(context.core).close("still-a-draft"));

  assert.equal(failure.code, "invalid_status_change");
  assert.equal(failure.kind, "conflict");
  assert.equal(failure.retryable, false);
  assert.equal(context.calls.length, 1);
});

test("closure by a stranger reads as not found", async () => {
  const context = createTestContext({ responder: () => problemResponse(404, "arena_not_found") });

  const failure = await captureApiError(() => createDraftsClient(context.core).close("someone-elses-arena"));

  assert.equal(failure.code, "arena_not_found");
  assert.equal(failure.kind, "not_found");
  assert.equal(context.calls.length, 1);
});

test("a rate limit is a failure the person answers, never a replay", async () => {
  const context = createTestContext({ responder: () => problemResponse(429, "rate_limited") });

  const failure = await captureApiError(() =>
    createDraftsClient(context.core).create({
      statement: "Máquinas podem ser responsáveis?",
      category: "philosophy",
      language: "pt-BR",
    }),
  );

  assert.equal(failure.kind, "rate_limited");
  assert.equal(context.calls.length, 1);
});
