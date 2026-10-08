/**
 * Tests of the domain clients (P18-T03; keys P50-T03): each operation must
 * speak the path, method, query and body the versioned contract declares.
 * Only mutations with a backend-proven idempotent contract (arguments
 * publish/reply, mandatory `Idempotency-Key` in `api/openapi.json`) arrive
 * with a key; position transitions have no such contract and stay
 * non-retryable, so they send none. Billing keys travel in the request
 * body (`idempotency_key`), never as a header the contract does not
 * declare, so they assert no header here.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createArgumentsClient } from "../../src/core/clients/arguments.js";
import { createAttributionsClient } from "../../src/core/clients/attributions.js";
import { createArenasClient } from "../../src/core/clients/arenas.js";
import { createAuthClient } from "../../src/core/clients/auth.js";
import { createPositionsClient } from "../../src/core/clients/positions.js";
import { createDeletionClient } from "../../src/core/clients/deletion.js";
import { createDraftsClient } from "../../src/core/clients/drafts.js";
import { createExportsClient } from "../../src/core/clients/exports.js";
import { createMFAClient } from "../../src/core/clients/mfa.js";
import { createProfilesClient } from "../../src/core/clients/profiles.js";
import { createSessionsClient } from "../../src/core/clients/sessions.js";
import { createModerationClient } from "../../src/core/clients/moderation.js";
import { createJobsClient } from "../../src/core/clients/jobs.js";
import { createTransparencyClient } from "../../src/core/clients/transparency.js";
import { createWalletClient } from "../../src/core/clients/wallet.js";
import { createPassesClient } from "../../src/core/clients/passes.js";
import { createBillingClient } from "../../src/core/clients/billing.js";
import { createSeasonsClient } from "../../src/core/clients/seasons.js";
import { createStagedClient } from "../../src/core/clients/staged.js";
import type { HttpCore } from "../../src/core/http.js";
import { bodyOf, captureApiError, createTestContext, headerOf, jsonResponse, problemResponse } from "../support/harness.js";
import { readPackageFile } from "../support/paths.js";

/** One expected call of a client operation. */
interface Expectation {
  readonly name: string;
  readonly run: (core: HttpCore) => Promise<unknown>;
  readonly method: string;
  readonly url: string;
  readonly body?: unknown;
  readonly idempotent: boolean;
}

const expectations: readonly Expectation[] = [
  {
    name: "auth register",
    run: (core) => createAuthClient(core).register({ email: "ada@example.test", password: "pw" }),
    method: "POST",
    url: "https://arena.test/api/v1/auth/register",
    body: { email: "ada@example.test", password: "pw" },
    idempotent: false,
  },
  {
    name: "auth verify",
    run: (core) => createAuthClient(core).verify("single-use-token"),
    method: "GET",
    url: "https://arena.test/api/v1/auth/verify?token=single-use-token",
    idempotent: false,
  },
  {
    name: "auth login",
    run: (core) => createAuthClient(core).login({ email: "ada@example.test", password: "pw" }),
    method: "POST",
    url: "https://arena.test/api/v1/auth/login",
    body: { email: "ada@example.test", password: "pw" },
    idempotent: false,
  },
  {
    name: "auth logout",
    run: (core) => createAuthClient(core).logout(),
    method: "POST",
    url: "https://arena.test/api/v1/auth/logout",
    idempotent: false,
  },
  {
    name: "auth password reset request",
    run: (core) => createAuthClient(core).requestPasswordReset({ email: "ada@example.test" }),
    method: "POST",
    url: "https://arena.test/api/v1/auth/password-reset/request",
    body: { email: "ada@example.test" },
    idempotent: false,
  },
  {
    name: "auth password reset confirm",
    run: (core) => createAuthClient(core).confirmPasswordReset({ token: "t", password: "pw" }),
    method: "POST",
    url: "https://arena.test/api/v1/auth/password-reset/confirm",
    body: { token: "t", password: "pw" },
    idempotent: false,
  },
  {
    name: "auth session",
    run: (core) => createAuthClient(core).session(),
    method: "GET",
    url: "https://arena.test/api/v1/me/profile",
    idempotent: false,
  },
  {
    name: "arenas feed with filters",
    run: (core) => createArenasClient(core).feed({ language: "pt-BR", limit: 20 }),
    method: "GET",
    url: "https://arena.test/api/v1/arenas?language=pt-BR&limit=20",
    idempotent: false,
  },
  {
    name: "arenas feed without filters",
    run: (core) => createArenasClient(core).feed(),
    method: "GET",
    url: "https://arena.test/api/v1/arenas",
    idempotent: false,
  },
  {
    name: "arenas by slug",
    run: (core) => createArenasClient(core).bySlug("a b/c"),
    method: "GET",
    url: "https://arena.test/api/v1/arenas/a%20b%2Fc",
    idempotent: false,
  },
  {
    name: "arenas search",
    run: (core) => createArenasClient(core).search({ q: "minds changed", cursor: "c1" }),
    method: "GET",
    url: "https://arena.test/api/v1/search/arenas?q=minds+changed&cursor=c1",
    idempotent: false,
  },
  {
    name: "positions aggregate",
    run: (core) => createPositionsClient(core).aggregate("arena-1"),
    method: "GET",
    url: "https://arena.test/api/v1/arenas/arena-1/positions",
    idempotent: false,
  },
  {
    name: "positions mine",
    run: (core) => createPositionsClient(core).mine("arena-1"),
    method: "GET",
    url: "https://arena.test/api/v1/me/arenas/arena-1/position",
    idempotent: false,
  },
  {
    name: "positions confirm",
    run: (core) => createPositionsClient(core).confirm("arena-1", { position: "agree" }),
    method: "POST",
    url: "https://arena.test/api/v1/me/arenas/arena-1/position",
    body: { position: "agree" },
    idempotent: false,
  },
  {
    name: "positions change",
    run: (core) => createPositionsClient(core).change("arena-1", { position: "disagree" }),
    method: "POST",
    url: "https://arena.test/api/v1/me/arenas/arena-1/position/changes",
    body: { position: "disagree" },
    idempotent: false,
  },
  {
    name: "positions change history",
    run: (core) => createPositionsClient(core).changes("arena-1"),
    method: "GET",
    url: "https://arena.test/api/v1/me/arenas/arena-1/position/changes",
    idempotent: false,
  },
  {
    name: "arguments list",
    run: (core) => createArgumentsClient(core).list("arena-1", { relation: "support", limit: 10 }),
    method: "GET",
    url: "https://arena.test/api/v1/arenas/arena-1/arguments?relation=support&limit=10",
    idempotent: false,
  },
  {
    name: "arguments get",
    run: (core) => createArgumentsClient(core).get("arg-1"),
    method: "GET",
    url: "https://arena.test/api/v1/arguments/arg-1",
    idempotent: false,
  },
  {
    name: "arguments replies without cursor",
    run: (core) => createArgumentsClient(core).replies("arg-1"),
    method: "GET",
    url: "https://arena.test/api/v1/arguments/arg-1/replies",
    idempotent: false,
  },
  {
    name: "arguments replies with cursor",
    run: (core) => createArgumentsClient(core).replies("arg-1", { cursor: "c2", limit: 5 }),
    method: "GET",
    url: "https://arena.test/api/v1/arguments/arg-1/replies?cursor=c2&limit=5",
    idempotent: false,
  },
  {
    name: "arguments publish",
    run: (core) => createArgumentsClient(core).publish("arena-1", { relation: "support", content: "because", sources: [] }),
    method: "POST",
    url: "https://arena.test/api/v1/me/arenas/arena-1/arguments",
    body: { relation: "support", content: "because", sources: [] },
    idempotent: true,
  },
  {
    name: "profiles mine",
    run: (core) => createProfilesClient(core).mine(),
    method: "GET",
    url: "https://arena.test/api/v1/me/profile",
    idempotent: false,
  },
  {
    name: "profiles by username",
    run: (core) => createProfilesClient(core).byUsername("ada"),
    method: "GET",
    url: "https://arena.test/api/v1/profiles/ada",
    idempotent: false,
  },
  {
    name: "profiles reputation",
    run: (core) => createProfilesClient(core).reputation("ada"),
    method: "GET",
    url: "https://arena.test/api/v1/profiles/ada/reputation",
    idempotent: false,
  },
  {
    name: "sessions list",
    run: (core) => createSessionsClient(core).list(),
    method: "GET",
    url: "https://arena.test/api/v1/me/sessions",
    idempotent: false,
  },
  {
    name: "sessions rotate",
    run: (core) => createSessionsClient(core).rotate(),
    method: "POST",
    url: "https://arena.test/api/v1/me/sessions/rotation",
    idempotent: false,
  },
  {
    name: "sessions revoke",
    run: (core) => createSessionsClient(core).revoke({ session_id: "sess-2", password: "pw" }),
    method: "POST",
    url: "https://arena.test/api/v1/me/sessions/revocation",
    body: { session_id: "sess-2", password: "pw" },
    idempotent: false,
  },
  {
    name: "mfa begin",
    run: (core) => createMFAClient(core).begin(),
    method: "POST",
    url: "https://arena.test/api/v1/me/mfa/enrollment",
    idempotent: false,
  },
  {
    name: "mfa confirm",
    run: (core) => createMFAClient(core).confirm({ code: "123456" }),
    method: "POST",
    url: "https://arena.test/api/v1/me/mfa/enrollment/confirm",
    body: { code: "123456" },
    idempotent: false,
  },
  {
    name: "mfa step-up",
    run: (core) => createMFAClient(core).stepUp({ code: "654321" }),
    method: "POST",
    url: "https://arena.test/api/v1/me/mfa/step-up",
    body: { code: "654321" },
    idempotent: false,
  },
  {
    name: "mfa recovery",
    run: (core) => createMFAClient(core).recover({ code: "r1-first" }),
    method: "POST",
    url: "https://arena.test/api/v1/me/mfa/recovery",
    body: { code: "r1-first" },
    idempotent: false,
  },
  {
    name: "exports request",
    run: (core) => createExportsClient(core).request(),
    method: "POST",
    url: "https://arena.test/api/v1/me/exports",
    idempotent: false,
  },
  {
    name: "exports download",
    run: (core) => createExportsClient(core).download({ id: "exp-1", token: "t" }),
    method: "GET",
    url: "https://arena.test/api/v1/me/exports/exp-1/download?token=t",
    idempotent: false,
  },
  {
    name: "deletion status",
    run: (core) => createDeletionClient(core).status(),
    method: "GET",
    url: "https://arena.test/api/v1/me/deletion",
    idempotent: false,
  },
  {
    name: "deletion request",
    run: (core) => createDeletionClient(core).request(),
    method: "POST",
    url: "https://arena.test/api/v1/me/deletion",
    idempotent: false,
  },
  {
    name: "deletion cancel",
    run: (core) => createDeletionClient(core).cancel(),
    method: "POST",
    url: "https://arena.test/api/v1/me/deletion/cancel",
    idempotent: false,
  },
  {
    name: "drafts list",
    run: (core) => createDraftsClient(core).list(),
    method: "GET",
    url: "https://arena.test/api/v1/me/arena-drafts",
    idempotent: false,
  },
  {
    name: "drafts create",
    run: (core) =>
      createDraftsClient(core).create({ statement: "Machines, responsible?", category: "philosophy", language: "en-US" }),
    method: "POST",
    url: "https://arena.test/api/v1/me/arena-drafts",
    body: { statement: "Machines, responsible?", category: "philosophy", language: "en-US" },
    idempotent: false,
  },
  {
    name: "drafts get",
    run: (core) => createDraftsClient(core).get("draft-1"),
    method: "GET",
    url: "https://arena.test/api/v1/me/arena-drafts/draft-1",
    idempotent: false,
  },
  {
    name: "drafts update",
    run: (core) =>
      createDraftsClient(core).update("draft-1", {
        statement: "Machines, responsible?",
        category: "philosophy",
        language: "en-US",
        expected_version: 1,
      }),
    method: "PATCH",
    url: "https://arena.test/api/v1/me/arena-drafts/draft-1",
    body: { statement: "Machines, responsible?", category: "philosophy", language: "en-US", expected_version: 1 },
    idempotent: false,
  },
  {
    name: "drafts remove",
    run: (core) => createDraftsClient(core).remove("draft-1"),
    method: "DELETE",
    url: "https://arena.test/api/v1/me/arena-drafts/draft-1",
    idempotent: false,
  },
  {
    name: "drafts publish",
    run: (core) => createDraftsClient(core).publish("draft-1"),
    method: "POST",
    url: "https://arena.test/api/v1/me/arena-drafts/draft-1/publish",
    idempotent: false,
  },
  {
    name: "drafts close",
    run: (core) => createDraftsClient(core).close("arena-1"),
    method: "POST",
    url: "https://arena.test/api/v1/me/arenas/arena-1/close",
    idempotent: false,
  },
  {
    name: "arenas export",
    run: (core) => createArenasClient(core).exportById("arena-1", { cursor: "c1", limit: 20 }),
    method: "GET",
    url: "https://arena.test/api/v1/arenas/arena-1/export?cursor=c1&limit=20",
    idempotent: false,
  },
  {
    name: "arguments search",
    run: (core) => createArgumentsClient(core).search({ q: "minds changed", language: "en-US" }),
    method: "GET",
    url: "https://arena.test/api/v1/search/arguments?q=minds+changed&language=en-US",
    idempotent: false,
  },
  {
    name: "arguments withdraw",
    run: (core) => createArgumentsClient(core).withdraw("arg-1"),
    method: "POST",
    url: "https://arena.test/api/v1/me/arguments/arg-1/withdraw",
    idempotent: false,
  },
  {
    name: "attributions record",
    run: (core) => createAttributionsClient(core).record("change-1", { argument_ids: ["arg-1"] }),
    method: "POST",
    url: "https://arena.test/api/v1/me/position-changes/change-1/attributions",
    body: { argument_ids: ["arg-1"] },
    idempotent: false,
  },
  {
    name: "attributions counts",
    run: (core) => createAttributionsClient(core).counts("arg-1"),
    method: "GET",
    url: "https://arena.test/api/v1/arguments/arg-1/attributions",
    idempotent: false,
  },
  {
    name: "moderation report",
    run: (core) =>
      createModerationClient(core).report({ target_type: "argument", target_id: "arg-1", reason: "spam" }),
    method: "POST",
    url: "https://arena.test/api/v1/me/moderation/reports",
    body: { target_type: "argument", target_id: "arg-1", reason: "spam" },
    idempotent: false,
  },
  {
    name: "moderation appeal",
    run: (core) => createModerationClient(core).appeal({ action_id: "act-1", context: "my case" }),
    method: "POST",
    url: "https://arena.test/api/v1/me/moderation/appeals",
    body: { action_id: "act-1", context: "my case" },
    idempotent: false,
  },
  {
    name: "moderation queue",
    run: (core) => createModerationClient(core).queue({ status: "open", limit: 20 }),
    method: "GET",
    url: "https://arena.test/api/v1/moderation/cases?status=open&limit=20",
    idempotent: false,
  },
  {
    name: "moderation claim",
    run: (core) => createModerationClient(core).claim("case-1"),
    method: "POST",
    url: "https://arena.test/api/v1/moderation/cases/case-1/claim",
    idempotent: false,
  },
  {
    name: "moderation decide",
    run: (core) =>
      createModerationClient(core).decide("case-1", { action: "warning", rule: "R1", justification: "why" }),
    method: "POST",
    url: "https://arena.test/api/v1/moderation/cases/case-1/decisions",
    body: { action: "warning", rule: "R1", justification: "why" },
    idempotent: false,
  },
  {
    name: "moderation signals",
    run: (core) => createModerationClient(core).signals("author-1"),
    method: "GET",
    url: "https://arena.test/api/v1/moderation/attribution-signals/author-1",
    idempotent: false,
  },
  {
    name: "jobs health",
    run: (core) => createJobsClient(core).health(),
    method: "GET",
    url: "https://arena.test/api/v1/admin/jobs/health",
    idempotent: false,
  },
  {
    name: "jobs dead",
    run: (core) => createJobsClient(core).dead(50),
    method: "GET",
    url: "https://arena.test/api/v1/admin/jobs/dead?limit=50",
    idempotent: false,
  },
  {
    name: "jobs retry",
    run: (core) => createJobsClient(core).retry("job-1", { reason: "provider was down" }),
    method: "POST",
    url: "https://arena.test/api/v1/admin/jobs/job-1/retry",
    body: { reason: "provider was down" },
    idempotent: false,
  },
  {
    name: "transparency metrics",
    run: (core) => createTransparencyClient(core).metrics(),
    method: "GET",
    url: "https://arena.test/api/v1/public/transparency",
    idempotent: false,
  },
  {
    name: "wallet balance",
    run: (core) => createWalletClient(core).balance(),
    method: "GET",
    url: "https://arena.test/api/v1/me/wallet",
    idempotent: false,
  },
  {
    name: "wallet statement",
    run: (core) => createWalletClient(core).statement({ cursor: "c1", limit: 20 }),
    method: "GET",
    url: "https://arena.test/api/v1/me/wallet/transactions?cursor=c1&limit=20",
    idempotent: false,
  },
  {
    name: "passes summary",
    run: (core) => createPassesClient(core).summary(),
    method: "GET",
    url: "https://arena.test/api/v1/me/passes",
    idempotent: false,
  },
  {
    name: "passes history",
    run: (core) => createPassesClient(core).history({ cursor: "c1", limit: 20 }),
    method: "GET",
    url: "https://arena.test/api/v1/me/passes/history?cursor=c1&limit=20",
    idempotent: false,
  },
  {
    name: "billing checkout",
    run: (core) =>
      createBillingClient(core).checkout({ market: "BR", product: "pass_1", idempotencyKey: "op-1" }),
    method: "POST",
    url: "https://arena.test/api/v1/me/billing/checkout",
    body: { market: "BR", product: "pass_1", idempotency_key: "op-1" },
    idempotent: false,
  },
  {
    name: "billing subscription",
    run: (core) => createBillingClient(core).subscription(),
    method: "GET",
    url: "https://arena.test/api/v1/me/billing/subscription",
    idempotent: false,
  },
  {
    name: "billing portal",
    run: (core) => createBillingClient(core).portal({ idempotencyKey: "op-1" }),
    method: "POST",
    url: "https://arena.test/api/v1/me/billing/portal",
    body: { idempotency_key: "op-1" },
    idempotent: false,
  },
  {
    name: "arguments reply",
    run: (core) => createArgumentsClient(core).reply("arena-1", "arg-1", { relation: "oppose", content: "reply", sources: [] }),
    method: "POST",
    url: "https://arena.test/api/v1/me/arenas/arena-1/arguments/arg-1/replies",
    body: { relation: "oppose", content: "reply", sources: [] },
    idempotent: true,
  },
  {
    name: "seasons readCurrentSeason",
    run: (core) => createSeasonsClient(core).readCurrentSeason(),
    method: "GET",
    url: "https://arena.test/api/v1/me/seasons/current",
    idempotent: false,
  },
  {
    name: "seasons readSeasonHistory",
    run: (core) => createSeasonsClient(core).readSeasonHistory(),
    method: "GET",
    url: "https://arena.test/api/v1/me/seasons/history",
    idempotent: false,
  },
  {
    name: "seasons readSeason",
    run: (core) => createSeasonsClient(core).readSeason("temporada-harness-b"),
    method: "GET",
    url: "https://arena.test/api/v1/me/seasons/temporada-harness-b",
    idempotent: false,
  },
  {
    name: "seasons readSeasonChampions",
    run: (core) => createSeasonsClient(core).readSeasonChampions("temporada-harness-b"),
    method: "GET",
    url: "https://arena.test/api/v1/me/seasons/temporada-harness-b/champions",
    idempotent: false,
  },
  {
    name: "staged readCurrentSeason",
    run: (core) => createStagedClient(core).readCurrentSeason(),
    method: "GET",
    url: "https://arena.test/api/v1/me/seasons/current",
    idempotent: false,
  },
  {
    name: "staged readSeasonHistory",
    run: (core) => createStagedClient(core).readSeasonHistory(),
    method: "GET",
    url: "https://arena.test/api/v1/me/seasons/history",
    idempotent: false,
  },
  {
    name: "staged readSeason",
    run: (core) => createStagedClient(core).readSeason("temporada-harness-b"),
    method: "GET",
    url: "https://arena.test/api/v1/me/seasons/temporada-harness-b",
    idempotent: false,
  },
  {
    name: "staged readSeasonChampions",
    run: (core) => createStagedClient(core).readSeasonChampions("temporada-harness-b"),
    method: "GET",
    url: "https://arena.test/api/v1/me/seasons/temporada-harness-b/champions",
    idempotent: false,
  },
  {
    name: "staged previewMeteringQuote",
    run: (core) => createStagedClient(core).previewMeteringQuote({ content: "texto final", service: "argument-publish" }),
    method: "POST",
    url: "https://arena.test/api/v1/me/metering/quotes",
    body: { content: "texto final", service: "argument-publish" },
    idempotent: false,
  },
  {
    name: "staged confirmMeteringPublication",
    run: (core) =>
      createStagedClient(core).confirmMeteringPublication({
        intention_key: "intencao-1",
        content: "texto final",
        service: "argument-publish",
      }),
    method: "POST",
    url: "https://arena.test/api/v1/me/metering/publications",
    body: { intention_key: "intencao-1", content: "texto final", service: "argument-publish" },
    idempotent: false,
  },
  {
    name: "staged readMeteringReceipt",
    run: (core) => createStagedClient(core).readMeteringReceipt("00000000-0000-4000-8000-000000000001"),
    method: "GET",
    url: "https://arena.test/api/v1/me/metering/publications/00000000-0000-4000-8000-000000000001",
    idempotent: false,
  },
  {
    name: "staged readMeteringStatement",
    run: (core) => createStagedClient(core).readMeteringStatement(),
    method: "GET",
    url: "https://arena.test/api/v1/me/metering/statement",
    idempotent: false,
  },
  {
    name: "staged readTradeReceipt",
    run: (core) => createStagedClient(core).readTradeReceipt("00000000-0000-4000-8000-000000000002"),
    method: "GET",
    url: "https://arena.test/api/v1/me/commerce/contracts/00000000-0000-4000-8000-000000000002",
    idempotent: false,
  },
  {
    name: "staged readTradeStatement",
    run: (core) => createStagedClient(core).readTradeStatement(),
    method: "GET",
    url: "https://arena.test/api/v1/me/commerce/statement",
    idempotent: false,
  },
  {
    name: "staged readPrivateCaseFile",
    run: (core) => createStagedClient(core).readPrivateCaseFile("caso-1"),
    method: "GET",
    url: "https://arena.test/api/v1/me/disputes/cases/caso-1",
    idempotent: false,
  },
  {
    name: "staged acceptPrivateCaseTerms",
    run: (core) => createStagedClient(core).acceptPrivateCaseTerms("caso-1"),
    method: "POST",
    url: "https://arena.test/api/v1/me/disputes/cases/caso-1/accepts",
    idempotent: false,
  },
  {
    name: "staged filePrivateCaseDefense",
    run: (core) => createStagedClient(core).filePrivateCaseDefense("caso-1", { digest: "resumo-1" }),
    method: "POST",
    url: "https://arena.test/api/v1/me/disputes/cases/caso-1/defenses",
    body: { digest: "resumo-1" },
    idempotent: false,
  },
  {
    name: "staged readPrivateCaseRuling",
    run: (core) => createStagedClient(core).readPrivateCaseRuling("caso-1"),
    method: "GET",
    url: "https://arena.test/api/v1/me/disputes/cases/caso-1/ruling",
    idempotent: false,
  },
  {
    name: "staged appealPrivateCaseRuling",
    run: (core) => createStagedClient(core).appealPrivateCaseRuling("caso-1", { reason: "recurso honesto" }),
    method: "POST",
    url: "https://arena.test/api/v1/me/disputes/cases/caso-1/appeals",
    body: { reason: "recurso honesto" },
    idempotent: false,
  },
];

for (const expectation of expectations) {
  test(`client ${expectation.name} calls the contract path`, async () => {
    const context = createTestContext({ responder: () => jsonResponse({ ok: true }) });

    await expectation.run(context.core);

    assert.equal(context.calls.length, 1);
    const call = context.lastCall();
    assert.equal(call.init.method, expectation.method);
    assert.equal(call.url, expectation.url);
    if (expectation.body !== undefined) {
      assert.deepEqual(bodyOf(call), expectation.body);
    }
    if (expectation.idempotent) {
      assert.notEqual(headerOf(call, "idempotency-key"), null, "mutations must be replayable");
    } else {
      assert.equal(headerOf(call, "idempotency-key"), null);
    }
  });
}

test("session lookups keep a 401 silent while account reads expire the session", async () => {
  const context = createTestContext({ responder: () => problemResponse(401, "session_expired") });

  const sessionFailure = await captureApiError(() => createAuthClient(context.core).session());
  assert.equal(sessionFailure.code, "session_expired");
  assert.equal(context.unauthorized.length, 0, "asking who I am is allowed to answer 401");

  const positionFailure = await captureApiError(() => createPositionsClient(context.core).mine("arena-1"));
  assert.equal(positionFailure.kind, "unauthorized");
  assert.equal(context.unauthorized.length, 1);
  assert.equal(context.unauthorized[0]?.path, "/api/v1/me/arenas/arena-1/position");
});

test("login answers 401 without expiring the session", async () => {
  const context = createTestContext({ responder: () => problemResponse(401, "invalid_credentials") });

  const failure = await captureApiError(() => createAuthClient(context.core).login({ email: "ada@example.test", password: "nope" }));
  assert.equal(failure.code, "invalid_credentials");
  assert.equal(failure.kind, "unauthorized");
  assert.equal(context.unauthorized.length, 0);
});

test("verify consumes the single-use token and reports the verified status", async () => {
  const context = createTestContext({ responder: () => jsonResponse({ status: "verified" }) });

  const answer = await createAuthClient(context.core).verify("single-use-token");
  assert.equal(answer.status, "verified");
  assert.equal(context.calls.length, 1);
  assert.equal(context.unauthorized.length, 0);
});

test("verify refuses a spent or unknown token as validation, never as a session event", async () => {
  const context = createTestContext({ responder: () => problemResponse(400, "invalid_token") });

  const failure = await captureApiError(() => createAuthClient(context.core).verify("spent-token"));
  assert.equal(failure.code, "invalid_token");
  assert.equal(failure.kind, "validation");
  assert.equal(failure.retryable, false);
  assert.equal(context.unauthorized.length, 0, "a refused token is not an expired session");
});

test("verify retries a spent rate-limit budget and then verifies", async () => {
  const context = createTestContext({
    responder: (_call, index) =>
      index < 2 ? problemResponse(429, "rate_limited", { "retry-after": "1" }) : jsonResponse({ status: "verified" }),
  });

  const answer = await createAuthClient(context.core).verify("single-use-token");
  assert.equal(answer.status, "verified");
  assert.equal(context.calls.length, 3, "a safe verification may be replayed after the budget returns");
  assert.equal(context.unauthorized.length, 0);
});

test("verify answers an unexpected 401 by expiring the session", async () => {
  const context = createTestContext({ responder: () => problemResponse(401, "session_expired") });

  const failure = await captureApiError(() => createAuthClient(context.core).verify("single-use-token"));
  assert.equal(failure.code, "session_expired");
  assert.equal(context.unauthorized.length, 1);
  assert.equal(context.unauthorized[0]?.path, "/api/v1/auth/verify");
});

test("the auth client stores no token and no password", () => {
  // Comments document the rule, so they are stripped before the scan — the
  // same precedent `tools/webaudit` uses before refusing a forbidden sink.
  // What is measured is the behaviour, not its documentation.
  const source = readPackageFile("src/core/clients/auth.ts")
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(/(^|\s)\/\/.*$/gm, "$1");

  for (const storage of ["localStorage", "sessionStorage", "document.cookie"]) {
    assert.ok(!source.includes(storage), `auth.ts must never reach ${storage}: the session lives in cookies the server sets`);
  }
});
