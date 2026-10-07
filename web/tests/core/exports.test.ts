/**
 * Tests of the personal export client (P51-T04) against a fake
 * transport: the two operations speak the contract paths, methods,
 * query and bodies, neither is retried and neither carries an
 * idempotency key, and the failures the task names — a stale session,
 * a replayed or expired link, a forged token and a foreign owner —
 * surface with their kinds. No server-provided URL is ever honored:
 * the download address is built from the contract template alone.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createExportsClient } from "../../src/core/clients/exports.js";
import {
  captureApiError,
  createTestContext,
  headerOf,
  jsonResponse,
  problemResponse,
} from "../support/harness.js";

const JOB = {
  export_id: "018f6b2a-0000-7000-8000-000000000001",
  status: "requested",
  download_token: "single-use-token",
} as const;

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
} as const;

test("requesting posts without a body and returns the job with its one-time token", async () => {
  const context = createTestContext({ responder: () => jsonResponse(JOB, 202) });

  const job = await createExportsClient(context.core).request();

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/exports");
  assert.equal(context.lastCall().init.body, undefined, "the request carries no code and no password");
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(job.export_id, JOB.export_id);
  assert.equal(job.download_token, "single-use-token");
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("requesting with a stale session is refused, never served", async () => {
  const context = createTestContext({ responder: () => problemResponse(401, "step_up_required") });

  const failure = await captureApiError(() => createExportsClient(context.core).request());

  assert.equal(failure.code, "step_up_required");
  assert.equal(failure.kind, "unauthorized");
  assert.equal(failure.retryable, false);
});

test("requesting fails closed without a session", async () => {
  const context = createTestContext({ responder: () => problemResponse(401, "unauthorized") });

  const failure = await captureApiError(() => createExportsClient(context.core).request());

  assert.equal(failure.code, "unauthorized");
  assert.equal(failure.kind, "unauthorized");
});

test("downloading builds the address from the template, never from the server", async () => {
  const context = createTestContext({ responder: () => jsonResponse(DOCUMENT) });

  const document = await createExportsClient(context.core).download({
    id: "018f6b2a-0000-7000-8000-000000000001",
    token: "single-use-token",
  });

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "GET");
  assert.equal(
    context.lastCall().url,
    "https://arena.test/api/v1/me/exports/018f6b2a-0000-7000-8000-000000000001/download?token=single-use-token",
  );
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(document.schema_version, 1);
  assert.equal(context.lastCall().init.cache, "no-store");
});

test("downloading encodes an identifier that looks like a path", async () => {
  const context = createTestContext({ responder: () => jsonResponse(DOCUMENT) });

  await createExportsClient(context.core).download({ id: "a/b?c", token: "t" });

  assert.ok(
    context.lastCall().url.includes("/api/v1/me/exports/a%2Fb%3Fc/download?token=t"),
    `unexpected url: ${context.lastCall().url}`,
  );
});

test("a replayed, expired or foreign link is not found, never served", async () => {
  for (const id of ["018f6b2a-0000-7000-8000-000000000001", "018f6b2a-0000-7000-8000-00000000dead"]) {
    const context = createTestContext({ responder: () => problemResponse(404, "export_not_found") });
    const failure = await captureApiError(() =>
      createExportsClient(context.core).download({ id, token: "single-use-token" }),
    );
    assert.equal(failure.code, "export_not_found", `id ${id}`);
    assert.equal(failure.kind, "not_found", `id ${id}`);
    assert.equal(failure.retryable, false, `id ${id}: the single-use link must never replay`);
    assert.equal(context.calls.length, 1, `id ${id}`);
  }
});

test("a forged token is forbidden", async () => {
  const context = createTestContext({ responder: () => problemResponse(403, "invalid_export_token") });

  const failure = await captureApiError(() =>
    createExportsClient(context.core).download({ id: JOB.export_id, token: "forged-token" }),
  );

  assert.equal(failure.code, "invalid_export_token");
  assert.equal(failure.kind, "forbidden");
  assert.equal(context.calls.length, 1);
});

test("a lost download response is a failure, never a replay of the capability", async () => {
  const context = createTestContext({ responder: () => problemResponse(503, "unavailable") });

  const failure = await captureApiError(() =>
    createExportsClient(context.core).download({ id: JOB.export_id, token: "single-use-token" }),
  );

  assert.equal(context.calls.length, 1, "replaying a single-use link could burn it or mask an expiry");
  assert.equal(failure.retryable, false);
});

test("the job answer carries exactly the receipt the download needs", async () => {
  const context = createTestContext({ responder: () => jsonResponse(JOB, 202) });

  const job = await createExportsClient(context.core).request();

  assert.deepEqual(Object.keys(job).sort(), ["download_token", "export_id", "status"]);
});
