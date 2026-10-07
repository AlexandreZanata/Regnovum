/**
 * Tests of the arena drafts client (P52-T03) against a fake transport:
 * the list speaks the contract path with the account cache policy, the
 * creation posts exactly the contract fields — never a price, a pass
 * or any debit value — and neither operation is retried nor carries an
 * idempotency key. A lost creation is a failure the person answers by
 * asking again, never a replay the core invents.
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
