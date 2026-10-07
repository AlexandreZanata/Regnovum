/**
 * Tests of the jobs operator panel (P55-T03).
 *
 * They run the real generated catalogs in both locales: the
 * health renders counts with no verdict, dead rows render
 * lifecycle columns with no payload, the retry confirms before
 * sending with a bounded reason, and failures name only the
 * server codes the backend really emits. Nothing here is
 * public: the system health stays inside the restricted panel.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  deadLimit,
  deadRowView,
  deadTotal,
  healthView,
  isRetryReason,
  jobsFailure,
  retryConfirmView,
  retryResultView,
} from "../../src/pages/operator.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type { DeadJob, DeadJobPage, JobsQueueHealth, JobsRetry } from "../../src/contracts/generated.js";
import type { Locale } from "../../src/i18n/locale.js";

/** A translator holding exactly the namespace the panel renders from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["moderation"] });
}

const HEALTH: JobsQueueHealth = {
  generated_at: "2026-10-05T10:00:00Z",
  queue: { queued: 4, leased: 1, succeeded: 90, dead: 2, due_now: 1, lag_seconds: 30, oldest_dead_seconds: 3600 },
};

const JOB: DeadJob = {
  job_id: "job-1",
  type: "email.send",
  version: 1,
  attempts: 5,
  max_attempts: 5,
  age_seconds: 3600,
  last_error_code: "E_TIMEOUT",
  retryable: true,
};

const PAGE: DeadJobPage = { generated_at: "2026-10-05T10:00:00Z", total: 2, items: [JOB] };

const RETRY: JobsRetry = { job_id: "job-1", type: "email.send", state: "queued" };

test("the health renders counts with no verdict and no raw instant", () => {
  const view = healthView(translatorOf("en-US"), "en-US", HEALTH);

  assert.ok(view.counts.includes("4, 1, 90, 2, 1, 30, 3,600"), `counts misread: ${view.counts}`);
  assert.ok(!view.generated.includes("2026-10-05T10:00:00Z"), "raw instant leaked");
  assert.ok(view.intro.includes("Nothing here is public"), "restriction notice missing");
  const serialized = JSON.stringify(view).toLowerCase();
  for (const marker of ["payload", "healthy", "critical", "saturated", "verdict"]) {
    assert.ok(!serialized.includes(marker), `derived marker leaked: ${marker}`);
  }
});

test("the dead limit stays inside 1..200", () => {
  assert.equal(deadLimit(null), undefined);
  assert.equal(deadLimit("50"), 50);
  assert.equal(deadLimit("0"), undefined);
  assert.equal(deadLimit("201"), undefined);
  assert.equal(deadLimit("abc"), undefined);
});

test("one dead row renders lifecycle columns with no payload", () => {
  const view = deadRowView(translatorOf("pt-BR"), "pt-BR", JOB);

  assert.ok(view.line.includes("job-1"), "identifier missing");
  assert.ok(view.line.includes("email.send"), "workload missing");
  assert.ok(view.line.includes("5/5"), "attempts missing");
  assert.equal(view.retryable, true);
  const serialized = JSON.stringify(view).toLowerCase();
  assert.ok(!serialized.includes("payload"), "job payload must never render");
});

test("the page envelope totals without reordering", () => {
  const total = deadTotal(translatorOf("en-US"), "en-US", PAGE);

  assert.ok(total.includes("2"), "total missing");
  assert.ok(!total.includes("2026-10-05T10:00:00Z"), "raw instant leaked");
});

test("the retry confirms the named job with a bounded reason", () => {
  assert.ok(isRetryReason("provider was down"));
  assert.ok(!isRetryReason(""), "an empty reason must never be sent");
  assert.ok(!isRetryReason("x".repeat(201)), "an overlong reason must never be sent");

  const confirm = retryConfirmView(translatorOf("en-US"), "job-1", "email.send", "provider was down");
  assert.equal(confirm.prompt, "Retry job-1 (email.send)?");
  assert.equal(confirm.reason, "provider was down");

  const result = retryResultView(translatorOf("pt-BR"), RETRY);
  assert.ok(result.line.includes("job-1"));
  assert.ok(result.line.includes("queued"));
});

test("operator denials name the real server codes and fall back otherwise", () => {
  const translator = translatorOf("en-US");

  assert.ok(jobsFailure(translator, "unauthorized").includes("Sign in"));
  assert.ok(jobsFailure(translator, "forbidden").includes("cannot do this"));
  assert.ok(jobsFailure(translator, "retry_not_allowed").includes("cannot do this"));
  assert.ok(jobsFailure(translator, "step_up_required").includes("again"));
  assert.ok(jobsFailure(translator, "job_not_found").includes("no longer exists"));
  assert.ok(jobsFailure(translator, "job_not_dead").includes("conflicts"));
  assert.ok(jobsFailure(translator, "reason_required").includes("not valid"));
  assert.equal(
    jobsFailure(translator, "something-the-backend-never-emits"),
    jobsFailure(translator, "unknown-code"),
    "an unknown code must fall back to the generic sentence",
  );
});
