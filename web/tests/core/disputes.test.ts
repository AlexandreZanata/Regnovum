/**
 * Tests of the disputes staged client (P57-T04) against a fake transport.
 *
 * The five private case operations speak the paths, methods and bodies
 * their fragment declares, and nothing else: the file and the ruling
 * read back what the rite recorded, the acceptance records the
 * session party, the defense files one digest and the appeal contests
 * once with an explicit reason. Mutations are single-shot with no
 * `Idempotency-Key` header — no disputes POST contract declares one —
 * and every call leaves with the account no-store policy the core
 * already owns for `/api/v1/me/*`. A repeated acceptance answers
 * unchanged; a second appeal over the same ruling answers 409.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createDisputesClient } from "../../src/core/clients/disputes.js";
import {
  bodyOf,
  captureApiError,
  createTestContext,
  hangingResponse,
  headerOf,
  jsonResponse,
  problemResponse,
} from "../support/harness.js";

const FILE = {
  accepts: 1,
  appealed: false,
  defenses: 0,
  escrow_ref: "escrow-ponte",
  expires_at: "2026-11-05T00:00:00Z",
  key: "caso-proposta",
  kind: "arbitration",
  notices: [{ event: "proposal", title: "Proposta selada" }],
  object: "ponte sobre o rio",
  role: "claimant",
  status: "proposed",
  title: "Caso privado",
  value_milli: 20000,
  version: 1,
} as const;

const RULING = {
  appeal_due_at: "2026-11-12T00:00:00Z",
  appealed: false,
  award_milli: 10000,
  decided_at: "2026-10-05T10:00:00Z",
  decision_code: "uphold-claimant@hash-termo",
  key: "caso-sentenca",
  terms_hash: "hash-termo",
  title: "Sentença do rito",
  verdict: "uphold-claimant",
} as const;

test("one file reads version, consent and deadline with no verdict yet", async () => {
  const context = createTestContext({ responder: () => jsonResponse(FILE) });

  const file = await createDisputesClient(context.core).readPrivateCaseFile("caso-proposta");

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/disputes/cases/caso-proposta");
  assert.equal(file.version, 1);
  assert.equal(file.accepts, 1);
  assert.equal(context.lastCall().init.cache, "no-store");
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
});

test("the acceptance records the session party with no body", async () => {
  const context = createTestContext({ responder: () => jsonResponse({ ...FILE, accepts: 2, status: "open" }) });

  const file = await createDisputesClient(context.core).acceptPrivateCaseTerms("caso-proposta");

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/disputes/cases/caso-proposta/accepts");
  assert.equal(context.lastCall().init.body, undefined);
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(context.lastCall().init.cache, "no-store");
  assert.equal(file.accepts, 2);
});

test("the defense files one digest the document never echoes", async () => {
  const context = createTestContext({ responder: () => jsonResponse({ ...FILE, defenses: 1 }) });

  const file = await createDisputesClient(context.core).filePrivateCaseDefense("caso-jornada", {
    digest: "prova-requerente",
  });

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/disputes/cases/caso-jornada/defenses");
  assert.deepEqual(bodyOf(context.lastCall()), { digest: "prova-requerente" });
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(file.defenses, 1);
});

test("one ruling reads the stable verdict with its capped award", async () => {
  const context = createTestContext({ responder: () => jsonResponse(RULING) });

  const ruling = await createDisputesClient(context.core).readPrivateCaseRuling("caso-sentenca");

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/disputes/cases/caso-sentenca/ruling");
  assert.equal(ruling.verdict, "uphold-claimant");
  assert.equal(ruling.award_milli, 10000);
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("the appeal contests once with an explicit reason", async () => {
  const context = createTestContext({ responder: () => jsonResponse({ ...FILE, appealed: true }) });

  const file = await createDisputesClient(context.core).appealPrivateCaseRuling("caso-sentenca", {
    reason: "reexame do lote 7",
  });

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/disputes/cases/caso-sentenca/appeals");
  assert.deepEqual(bodyOf(context.lastCall()), { reason: "reexame do lote 7" });
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(file.appealed, true);
});

test("parties, strangers and duplicates fail distinctly", async () => {
  const failing = [
    { run: "file", status: 401, code: "unauthorized", kind: "unauthorized" },
    { run: "file", status: 404, code: "case_unknown", kind: "not_found" },
    { run: "accept", status: 403, code: "case_forbidden", kind: "forbidden" },
    { run: "accept", status: 409, code: "case_conflict", kind: "conflict" },
    { run: "defense", status: 409, code: "case_conflict", kind: "conflict" },
    { run: "appeal", status: 409, code: "case_conflict", kind: "conflict" },
    { run: "ruling", status: 404, code: "case_unknown", kind: "not_found" },
  ] as const;
  for (const target of failing) {
    const context = createTestContext({ responder: () => problemResponse(target.status, target.code) });
    const client = createDisputesClient(context.core);
    const failure = await captureApiError(() => {
      switch (target.run) {
        case "file":
          return client.readPrivateCaseFile("caso-1");
        case "accept":
          return client.acceptPrivateCaseTerms("caso-1");
        case "defense":
          return client.filePrivateCaseDefense("caso-1", { digest: "prova-1" });
        case "ruling":
          return client.readPrivateCaseRuling("caso-1");
        case "appeal":
          return client.appealPrivateCaseRuling("caso-1", { reason: "reexame" });
      }
    });
    assert.equal(failure.code, target.code);
    assert.equal(failure.kind, target.kind);
    assert.equal(context.calls.length, 1, `${target.code}: no automatic second attempt`);
  }
});

test("a cancelled file read aborts instead of rendering late", async () => {
  const controller = new AbortController();
  const context = createTestContext({ responder: (call) => hangingResponse(call) });

  const pending = captureApiError(() => createDisputesClient(context.core).readPrivateCaseFile("caso-1", controller.signal));
  controller.abort();
  const failure = await pending;

  assert.equal(failure.code, "aborted");
  assert.equal(failure.aborted, true, "an aborted answer is discarded, never rendered");
  assert.equal(failure.retryable, false);
  assert.equal(context.calls.length, 1);
});
