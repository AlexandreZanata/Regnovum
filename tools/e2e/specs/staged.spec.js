/**
 * The staged surfaces stay off the delivered server (P56-T02).
 *
 * The fifteen staged METHOD+path pairs (seasons 4, metering 4, commerce 2,
 * disputes 5) have real handlers in the isolated Go harness
 * (tools/stagedharness) but are never composed into the process the
 * journeys drive: no surface claims them, so the registry placeholder
 * answers. This spec is the browser-side half of the isolation guard —
 * if anyone mounts a staged handler by mistake, these calls stop being
 * the placeholder (a mounted handler answers JSON, never the bare
 * 404) and the journey fails.
 *
 * Anonymous calls: authentication lives inside the staged handlers, so
 * a session would prove nothing about mounting; the placeholder answers
 * either way.
 */
import { expect, test } from "@playwright/test";

const STAGED_GETS = [
  "/api/v1/me/seasons/current",
  "/api/v1/me/seasons/history",
  "/api/v1/me/seasons/temporada-qualquer",
  "/api/v1/me/seasons/temporada-qualquer/champions",
  "/api/v1/me/metering/publications/00000000-0000-4000-8000-000000000000",
  "/api/v1/me/metering/statement",
  "/api/v1/me/commerce/contracts/00000000-0000-4000-8000-000000000000",
  "/api/v1/me/commerce/statement",
  "/api/v1/me/disputes/cases/caso-qualquer",
  "/api/v1/me/disputes/cases/caso-qualquer/ruling",
];

const STAGED_POSTS = [
  { path: "/api/v1/me/metering/quotes", body: { content: "texto final", service: "argument-publish" } },
  {
    path: "/api/v1/me/metering/publications",
    body: { intention_key: "sonda-isolamento", content: "texto final", service: "argument-publish" },
  },
  { path: "/api/v1/me/disputes/cases/caso-qualquer/accepts", body: {} },
  { path: "/api/v1/me/disputes/cases/caso-qualquer/defenses", body: { digest: "sonda-isolamento" } },
  { path: "/api/v1/me/disputes/cases/caso-qualquer/appeals", body: { reason: "sonda de isolamento" } },
];

async function expectPlaceholder(request, method, path, options) {
  const response =
    method === "GET" ? await request.get(path) : await request.post(path, options);
  expect(response.status()).toBe(404);
  expect(response.headers()["content-type"]).toContain("text/plain");
  expect(await response.text()).toContain("404 page not found");
}

test("the fifteen staged pairs keep the unmounted placeholder", async ({ request }) => {
  for (const path of STAGED_GETS) {
    await expectPlaceholder(request, "GET", path);
  }
  for (const target of STAGED_POSTS) {
    await expectPlaceholder(request, "POST", target.path, { data: target.body });
  }
});
