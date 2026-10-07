/**
 * Tests of the MFA client (P51-T03) against a fake transport: the four
 * operations speak the contract paths, methods and bodies, none is
 * retried and none carries an idempotency key, and the failures the
 * task names — an invalid code, a spent code, a missing enrollment, a
 * refused elevation and a rate limit — surface with their kinds.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createMFAClient } from "../../src/core/clients/mfa.js";
import {
  bodyOf,
  captureApiError,
  createTestContext,
  headerOf,
  jsonResponse,
  problemResponse,
} from "../support/harness.js";

test("beginning posts without a body and returns the one-time secret", async () => {
  const context = createTestContext({
    responder: () => jsonResponse({ secret: "JBSWY3DPEHPK3PXP", uri: "otpauth://totp/regnum?secret=JBSWY3DPEHPK3PXP" }),
  });

  const answer = await createMFAClient(context.core).begin();

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/mfa/enrollment");
  assert.equal(context.lastCall().init.body, undefined, "begin carries no code and no password");
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(answer.secret, "JBSWY3DPEHPK3PXP");
});

test("beginning refuses a confirmed enrollment instead of replacing it", async () => {
  const context = createTestContext({ responder: () => problemResponse(409, "mfa_already_enrolled") });

  const failure = await captureApiError(() => createMFAClient(context.core).begin());

  assert.equal(failure.code, "mfa_already_enrolled");
  assert.equal(failure.kind, "conflict");
  assert.equal(failure.retryable, false);
  assert.equal(context.calls.length, 1);
});

test("confirming posts the code and returns the one-time recovery codes", async () => {
  const context = createTestContext({
    responder: () => jsonResponse({ backup_codes: ["r1-first", "r2-second"] }),
  });

  const answer = await createMFAClient(context.core).confirm({ code: "123456" });

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/mfa/enrollment/confirm");
  assert.deepEqual(bodyOf(context.lastCall()), { code: "123456" });
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.deepEqual(answer.backup_codes, ["r1-first", "r2-second"]);
});

test("confirming surfaces invalid, replayed and missing with their kinds and never retries", async () => {
  for (const [status, code, kind] of [
    [400, "invalid_json", "validation"],
    [403, "mfa_code_invalid", "forbidden"],
    [403, "mfa_code_replayed", "forbidden"],
    [404, "mfa_enrollment_missing", "not_found"],
  ] as const) {
    const context = createTestContext({ responder: () => problemResponse(status, code) });
    const failure = await captureApiError(() => createMFAClient(context.core).confirm({ code: "123456" }));
    assert.equal(failure.code, code, `status ${status}`);
    assert.equal(failure.kind, kind, `status ${status}`);
    assert.equal(failure.retryable, false, `status ${status}`);
    assert.equal(context.calls.length, 1, `status ${status}: a spent code must never replay`);
  }
});

test("stepping up posts the code and answers the elevation", async () => {
  const context = createTestContext({ responder: () => jsonResponse({ status: "elevated" }) });

  const answer = await createMFAClient(context.core).stepUp({ code: "654321" });

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/mfa/step-up");
  assert.deepEqual(bodyOf(context.lastCall()), { code: "654321" });
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(answer.status, "elevated");
});

test("a replayed step-up is a refusal, never a retry", async () => {
  const context = createTestContext({ responder: () => problemResponse(403, "mfa_code_replayed") });

  const failure = await captureApiError(() => createMFAClient(context.core).stepUp({ code: "654321" }));

  assert.equal(failure.code, "mfa_code_replayed");
  assert.equal(failure.kind, "forbidden");
  assert.equal(context.calls.length, 1, "the accepted step is spent: replaying it must fail once");
});

test("a lapsed elevation is a refusal the page reports", async () => {
  const context = createTestContext({ responder: () => problemResponse(403, "mfa_step_up_required") });

  const failure = await captureApiError(() => createMFAClient(context.core).stepUp({ code: "654321" }));

  assert.equal(failure.code, "mfa_step_up_required");
  assert.equal(failure.kind, "forbidden");
});

test("recovery spends one backup code and elevates", async () => {
  const context = createTestContext({ responder: () => jsonResponse({ status: "elevated" }) });

  const answer = await createMFAClient(context.core).recover({ code: "r1-first" });

  assert.equal(context.calls.length, 1);
  assert.equal(context.lastCall().init.method, "POST");
  assert.equal(context.lastCall().url, "https://arena.test/api/v1/me/mfa/recovery");
  assert.deepEqual(bodyOf(context.lastCall()), { code: "r1-first" });
  assert.equal(headerOf(context.lastCall(), "idempotency-key"), null);
  assert.equal(answer.status, "elevated");
});

test("a spent recovery code is refused and never replayed", async () => {
  const context = createTestContext({ responder: () => problemResponse(403, "mfa_code_replayed") });

  const failure = await captureApiError(() => createMFAClient(context.core).recover({ code: "r1-first" }));

  assert.equal(failure.code, "mfa_code_replayed");
  assert.equal(context.calls.length, 1);
});

test("a rate limit is a failure the person answers, never a replay", async () => {
  const context = createTestContext({ responder: () => problemResponse(429, "rate_limited") });

  const failure = await captureApiError(() => createMFAClient(context.core).stepUp({ code: "654321" }));

  assert.equal(failure.kind, "rate_limited");
  assert.equal(failure.retryable, false);
  assert.equal(context.calls.length, 1);
});

test("the operations fail closed without a session", async () => {
  const context = createTestContext({ responder: () => problemResponse(401, "session_expired") });

  const failure = await captureApiError(() => createMFAClient(context.core).begin());

  assert.equal(failure.code, "session_expired");
  assert.equal(failure.kind, "unauthorized");
});
