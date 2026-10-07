/**
 * Tests of the restricted moderation workbench (P55-T02).
 *
 * They run the real generated catalogs in both locales: the
 * queue filter stays allowlisted, rows render routing with
 * their holder, the closed actions travel untouched, and the
 * advisory signals render counts with no score, no severity
 * and no weight. The frontend grants no admin and no real
 * power: denials name only the server codes the backend really
 * emits.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  QUEUE_STATUSES,
  DECISION_ACTIONS,
  claimView,
  decisionView,
  isDecisionAction,
  isSignalKind,
  mergeQueueRows,
  queueCursor,
  queueLimit,
  queueRowView,
  queueStatus,
  signalView,
  signalsHead,
  workbenchFailure,
} from "../../src/pages/workbench.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type {
  AttributionSignal,
  AttributionSignals,
  ModerationCase,
  ModerationClaim,
  ModerationDecision,
} from "../../src/contracts/generated.js";
import type { Locale } from "../../src/i18n/locale.js";

/** A translator holding exactly the namespace the console renders from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["moderation"] });
}

const ROW: ModerationCase = {
  case_id: "case-1",
  target_type: "argument",
  target_id: "arg-1",
  status: "open",
  priority: "high",
  created_at: "2026-10-05T10:00:00Z",
};

const CLAIM: ModerationClaim = { case_id: "case-1", status: "under_review", claimed_by: "mod-1" };

const DECISION: ModerationDecision = { action_id: "act-9", case_id: "case-1", action: "warning" };

const SIGNAL: AttributionSignal = { kind: "reciprocity", counterpart_id: "author-2", mutual_events: 12 };

const ASSESSMENT: AttributionSignals = {
  author_id: "author-1",
  policy_version: "v3",
  window_seconds: 604800,
  checked_at: "2026-10-05T10:00:00Z",
  signals: [SIGNAL],
};

test("the queue filter stays allowlisted with an opaque cursor", () => {
  assert.deepEqual(QUEUE_STATUSES, ["open", "under_review", "decided", "closed"]);
  assert.equal(queueStatus(null), undefined);
  assert.equal(queueStatus(""), undefined);
  assert.equal(queueStatus("open"), "open");
  assert.equal(queueStatus("tribunal"), undefined, "no private tribunal filter travels");
  assert.equal(queueLimit(null), undefined);
  assert.equal(queueLimit("20"), 20);
  assert.equal(queueLimit("0"), undefined);
  assert.equal(queueLimit("101"), undefined);
  assert.equal(queueCursor(null), undefined);
  assert.equal(queueCursor("opaque"), "opaque");
});

test("one queue row renders routing with its holder", () => {
  const view = queueRowView(translatorOf("en-US"), "en-US", ROW);

  assert.ok(view.line.includes("argument"), "target vocabulary missing");
  assert.ok(view.line.includes("open"), "lifecycle missing");
  assert.ok(view.line.includes("high"), "priority missing");
  assert.ok(!view.line.includes("2026-10-05T10:00:00Z"), "raw instant leaked");
  assert.equal(view.holder, null);

  const claimed = queueRowView(translatorOf("pt-BR"), "pt-BR", { ...ROW, claimed_by: "mod-1" });
  assert.equal(claimed.holder, "mod-1");
  const serialized = JSON.stringify(claimed).toLowerCase();
  for (const marker of ["evidence", "justification", "context", "score"]) {
    assert.ok(!serialized.includes(marker), `restricted marker leaked: ${marker}`);
  }
});

test("the queue merge keeps one page honest", () => {
  const second: ModerationCase = { ...ROW, case_id: "case-2" };
  assert.deepEqual(
    mergeQueueRows([ROW], [ROW, second]).map((row) => row.case_id),
    ["case-1", "case-2"],
  );
});

test("the claim renders the server-owned lease without granting power", () => {
  const view = claimView(translatorOf("en-US"), CLAIM);

  assert.ok(view.line.includes("mod-1"), "holder missing");
  assert.ok(view.line.includes("under_review"), "lifecycle missing");
});

test("the closed actions travel untouched or not at all", () => {
  assert.equal(DECISION_ACTIONS.length, 11);
  assert.ok(isDecisionAction("warning"));
  assert.ok(isDecisionAction("preserve_legal"));
  assert.ok(!isDecisionAction("delete_everything"), "an invented measure never travels");

  const view = decisionView(translatorOf("en-US"), DECISION);
  assert.equal(view.line, "warning decision recorded: act-9");
});

test("one signal renders counts with no score and no severity", () => {
  const view = signalView(translatorOf("en-US"), "en-US", SIGNAL);

  assert.ok(view.line.includes("reciprocity"), "kind missing");
  assert.ok(view.line.includes("author-2"), "counterpart missing");
  assert.ok(view.line.includes("12"), "threshold counts missing");
  const serialized = JSON.stringify(view).toLowerCase();
  for (const marker of ["score", "severity", "weight", "automatic", "export", "metric"]) {
    assert.ok(!serialized.includes(marker), `automated marker leaked: ${marker}`);
  }
  assert.ok(isSignalKind("concentration"));
  assert.ok(!isSignalKind("verdict"), "an undeclared kind never renders");
});

test("the signals head names policy, window and instant", () => {
  const head = signalsHead(translatorOf("en-US"), "en-US", ASSESSMENT);

  assert.ok(head.includes("v3"), "policy revision missing");
  assert.ok(head.includes("604,800"), "window missing");
  assert.ok(!head.includes("2026-10-05T10:00:00Z"), "raw instant leaked");
});

test("workbench denials name the real server codes and fall back otherwise", () => {
  const translator = translatorOf("en-US");

  assert.ok(workbenchFailure(translator, "invalid_request").includes("not valid"));
  assert.ok(workbenchFailure(translator, "unauthorized").includes("Sign in"));
  assert.ok(workbenchFailure(translator, "forbidden").includes("cannot do this"));
  assert.ok(workbenchFailure(translator, "step_up_required").includes("again"));
  assert.ok(workbenchFailure(translator, "mfa_step_up_required").includes("again"));
  assert.ok(workbenchFailure(translator, "not_found").includes("no longer exists"));
  assert.ok(workbenchFailure(translator, "conflict").includes("conflicts"));
  assert.equal(
    workbenchFailure(translator, "something-the-backend-never-emits"),
    workbenchFailure(translator, "unknown-code"),
    "an unknown code must fall back to the generic sentence",
  );
});
