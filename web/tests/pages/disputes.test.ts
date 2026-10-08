/**
 * Tests of the disputes staged presentation (P57-T04).
 *
 * They run the real generated catalogs in both locales: one file shows
 * version, consent, deadline and authorized notices through the
 * `disputes/case` exhibit; digests and reasons are carried but never
 * echoed; the ruling shows the stable verdict with its capped award;
 * the appeal lives only inside its window; and failures name only
 * the server codes the backend really emits. This is the private
 * rite, not the P55 moderation console, and no AI judges here. A
 * disabled capability renders unavailability and sends nothing.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import type { PrivateCaseFile, PrivateCaseRuling } from "../../src/contracts/staged/disputes.js";
import { noStagedCapabilities, stagedCapabilities } from "../../src/core/staged.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type { Locale } from "../../src/i18n/locale.js";
import {
  caseExhibitState,
  caseFileView,
  disputesFailure,
  disputesGate,
  isAppealLive,
  isAppealReason,
  isDefenseDigest,
  noticeEvent,
  requireDisputesEnabled,
  rulingView,
} from "../../src/pages/disputes.js";
import { StagedUnavailableError } from "../../src/pages/staged.js";

/** A translator holding exactly the namespace the page renders from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["disputes"] });
}

const FILE: PrivateCaseFile = {
  accepts: 1,
  appealed: false,
  defenses: 0,
  escrow_ref: "escrow-ponte",
  expires_at: "2026-11-05T00:00:00Z",
  key: "caso-proposta",
  kind: "arbitration",
  notices: [
    { event: "proposal", title: "Proposta selada" },
    { event: "accept", title: "Aceite registrado" },
    { event: "defense", title: "Defesa apresentada" },
    { event: "ruling", title: "Sentença fundamentada" },
    { event: "appeal", title: "Recurso aberto" },
  ],
  object: "ponte sobre o rio",
  role: "claimant",
  status: "proposed",
  title: "Caso privado",
  value_milli: 20000,
  version: 1,
};

const RULING: PrivateCaseRuling = {
  appeal_due_at: "2026-11-12T00:00:00Z",
  appealed: false,
  award_milli: 10000,
  decided_at: "2026-10-05T10:00:00Z",
  decision_code: "uphold-claimant@hash-termo",
  key: "caso-sentenca",
  terms_hash: "hash-termo",
  title: "Sentença do rito",
  verdict: "uphold-claimant",
};

const NOW = new Date("2026-10-20T00:00:00Z");

test("one file shows version, consent and deadline through the exhibit", () => {
  for (const locale of ["pt-BR", "en-US"] as const) {
    const view = caseFileView(translatorOf(locale), locale, FILE, NOW);

    assert.ok(view.proposal.includes("caso-proposta"), "proposal key missing");
    assert.ok(view.proposal.includes("1"), "version missing");
    assert.ok(view.consent.includes("1"), "consent count missing");
    assert.ok(view.defenses.includes("0"), "defense count missing");
    assert.ok(!view.expires.includes("2026-11-05T00:00:00Z"), "raw instant leaked");
    assert.equal(view.decided, null, "no verdict before any ruling");
    assert.equal(view.notices.length, 5, "the five lifecycle notices missing");
    assert.equal(view.exhibit.role, "status");
    assert.equal(view.exhibit.lines.length, 5, "exhibit keeps title, proposal, parties, decided and action");
  }
});

test("the exhibit wiring follows verdict and appeal, never a fifth state", () => {
  assert.equal(caseExhibitState(FILE), "proposed");
  assert.equal(caseExhibitState({ ...FILE, status: "open" }), "open");
  assert.equal(caseExhibitState({ ...FILE, verdict: "uphold-claimant" }), "decided");
  assert.equal(caseExhibitState({ ...FILE, verdict: "uphold-claimant", appealed: true }), "appealed");
  assert.equal(caseExhibitState({ ...FILE, status: "something-undecidable" }), "proposed");

  const decided = caseFileView(translatorOf("en-US"), "en-US", { ...FILE, verdict: "uphold-claimant" }, NOW);
  assert.equal(decided.exhibit.role, "alert", "a decided file must interrupt");
});

test("digests and reasons travel but never render back", () => {
  const digest = "prova-requerente-ultra-secreta";
  const reason = "reexame do lote 7 pelo motivo pessoal";
  assert.equal(isDefenseDigest(digest), true);
  assert.equal(isDefenseDigest("   "), false, "an empty digest is dropped instead of sent");
  assert.equal(isAppealReason(reason), true);
  assert.equal(isAppealReason("  "), false, "an empty reason is dropped instead of sent");

  const file = caseFileView(translatorOf("pt-BR"), "pt-BR", FILE, NOW);
  const ruling = rulingView(translatorOf("pt-BR"), "pt-BR", RULING);
  const serialized = JSON.stringify({ file, ruling });
  assert.ok(!serialized.includes(digest), "defense digest must never echo");
  assert.ok(!serialized.includes(reason), "appeal reason must never echo");
});

test("the ruling shows the stable verdict with its capped award", () => {
  const view = rulingView(translatorOf("pt-BR"), "pt-BR", RULING);

  assert.ok(view.verdict.includes("uphold-claimant"), "verdict missing");
  assert.ok(view.award.includes("10.000") || view.award.includes("10,000"), `award misread: ${view.award}`);
  assert.ok(!view.decided.includes("2026-10-05T10:00:00Z"), "raw instant leaked");
  assert.equal(view.appealed, false);
  assert.equal(view.termsHash, "hash-termo");
  assert.ok(RULING.decision_code.includes(RULING.terms_hash), "decision must bind verdict to sealed terms");
});

test("the appeal lives only inside its window: deadline plus one refuses", () => {
  assert.equal(isAppealLive(RULING.appeal_due_at, new Date("2026-11-11T00:00:00Z")), true);
  assert.equal(isAppealLive(RULING.appeal_due_at, NOW), true);
  assert.equal(isAppealLive(RULING.appeal_due_at, new Date(RULING.appeal_due_at)), false);
  assert.equal(
    isAppealLive(RULING.appeal_due_at, new Date(new Date(RULING.appeal_due_at).getTime() + 86_400_000)),
    false,
    "deadline plus one day ends the recurso",
  );

  const live = caseFileView(
    translatorOf("pt-BR"),
    "pt-BR",
    { ...FILE, verdict: "uphold-claimant", decided_at: "2026-10-05T10:00:00Z", appeal_due_at: RULING.appeal_due_at },
    NOW,
  );
  assert.ok(live.exhibit.lines.some((line) => line.text.includes("recurso")), "live appeal names its action");

  const lapsed = caseFileView(
    translatorOf("pt-BR"),
    "pt-BR",
    { ...FILE, verdict: "uphold-claimant", decided_at: "2026-10-05T10:00:00Z", appeal_due_at: RULING.appeal_due_at },
    new Date("2026-12-01T00:00:00Z"),
  );
  assert.ok(
    !lapsed.exhibit.lines.some((line) => line.text.includes("Abrir recurso")),
    "a lapsed window offers no appeal action",
  );
});

test("unknown notice events never invent a sixth lifecycle step", () => {
  assert.equal(noticeEvent("proposal"), "proposal");
  assert.equal(noticeEvent("tribunal-inventado"), null);

  const view = caseFileView(
    translatorOf("pt-BR"),
    "pt-BR",
    { ...FILE, notices: [...FILE.notices, { event: "tribunal-inventado", title: "Tribunal inventado" }] },
    NOW,
  );
  assert.equal(view.notices.length, 5, "unknown events are dropped instead of rendered");
});

test("this is the private rite, not the moderation console, and no AI judges", () => {
  const file = caseFileView(translatorOf("en-US"), "en-US", FILE, NOW);
  const serialized = JSON.stringify(file).toLowerCase();
  for (const marker of ["moderation", "triage", "queue", "claim decision", "sanction", "suspend", "ai judge", "model verdict"]) {
    assert.ok(!serialized.includes(marker), `rite leaked ${marker}`);
  }
});

test("disputes denials name the real server codes and fall back otherwise", () => {
  const translator = translatorOf("en-US");

  assert.ok(disputesFailure(translator, "unauthorized").includes("Sign in"));
  assert.ok(disputesFailure(translator, "case_unknown").includes("does not exist"));
  assert.ok(disputesFailure(translator, "case_forbidden").includes("named party"));
  assert.ok(disputesFailure(translator, "case_conflict").includes("refused"));
  assert.ok(disputesFailure(translator, "case_invalid").includes("not valid"));
  assert.ok(disputesFailure(translator, "invalid_json").includes("not valid"));
  assert.equal(
    disputesFailure(translator, "something-the-backend-never-emits"),
    disputesFailure(translator, "unknown-code"),
    "an unknown code must fall back to the generic sentence",
  );
});

test("a disabled capability renders unavailability and sends nothing", () => {
  const translator = translatorOf("pt-BR");

  const off = disputesGate(noStagedCapabilities(), translator);
  assert.equal(off.enabled, false);
  assert.ok((off.view?.heading ?? "").includes("indisponível"), "unavailability heading missing");

  const on = disputesGate(stagedCapabilities(["disputes"]), translator);
  assert.equal(on.enabled, true);
  assert.equal(on.view, null);

  assert.throws(() => requireDisputesEnabled(noStagedCapabilities()), StagedUnavailableError);
  requireDisputesEnabled(stagedCapabilities(["disputes"]));
});
