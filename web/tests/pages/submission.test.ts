/**
 * Tests of the submission guard of the account journey (P18-T05).
 *
 * The guard is the only behaviour the journey's module adds to the documents, so
 * its rules are asserted directly: a first submission always goes through, a
 * second one while the first is in flight never does, the busy state is complete
 * (region, submitter and the primitive that announces it) and a restored page is
 * usable again.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  BUSY_ATTRIBUTE,
  BUSY_ELEMENT,
  BUSY_REGION_ATTRIBUTES,
  BUSY_SUBMITTER_ATTRIBUTES,
  BUSY_SUBMITTER_MARKER,
  busyAttributes,
  idleAttributes,
  submissionStart,
} from "../../src/pages/submission.js";

test("the first submission of a form is always allowed", () => {
  assert.deepEqual(submissionStart(false), { allow: true });
});

test("a submission made while one is in flight is refused", () => {
  assert.deepEqual(submissionStart(true), { allow: false });
});

test("the busy state marks the region, the submitter and the primitive", () => {
  const state = busyAttributes();

  assert.deepEqual(Object.keys(state.region), [...BUSY_REGION_ATTRIBUTES]);
  assert.equal(state.region["aria-busy"], "true");

  assert.deepEqual(Object.keys(state.submitter).sort(), [...BUSY_SUBMITTER_ATTRIBUTES].sort());
  assert.equal(state.submitter["disabled"], "");
  assert.equal(state.submitter["aria-disabled"], "true");

  assert.deepEqual(Object.keys(state.busyElement), [BUSY_ATTRIBUTE]);
  assert.equal(state.busyElement[BUSY_ATTRIBUTE], "");
});

test("the busy state targets the element the primitives register", () => {
  assert.equal(BUSY_ELEMENT, "ga-busy");
});

test("the guard marks the control it disables, so a restore frees exactly it", () => {
  const busy = busyAttributes();
  const idle = idleAttributes();

  assert.equal(BUSY_SUBMITTER_MARKER, "data-ga-busy-submitter");
  assert.ok(BUSY_SUBMITTER_ATTRIBUTES.includes(BUSY_SUBMITTER_MARKER));
  assert.equal(busy.submitter[BUSY_SUBMITTER_MARKER], "");
  assert.equal(Object.hasOwn(idle.submitter, BUSY_SUBMITTER_MARKER), false);
});

test("the idle state clears every attribute the busy state set", () => {
  const busy = busyAttributes();
  const idle = idleAttributes();

  assert.deepEqual(idle.region, {});
  assert.deepEqual(idle.submitter, {});
  assert.deepEqual(idle.busyElement, {});

  for (const group of ["region", "submitter", "busyElement"] as const) {
    for (const attribute of Object.keys(busy[group])) {
      assert.equal(
        Object.hasOwn(idle[group], attribute),
        false,
        `the idle state must clear ${group}.${attribute}`,
      );
    }
  }
});
