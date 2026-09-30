// Package deferred is the clean half of the postponement rule, and it pins the
// decision the rule makes: the marker has to open the comment. These are the
// comments that explain the convention, and reading them as promises would make
// the gate refuse the documents that carry the vocabulary — the mistake this
// fixture exists to keep from coming back.
package deferred

// Documented explains the rule the requirement audit of P20-T02 records for the
// same words. The code markers are matched in upper case only, because the
// markers are conventions of source code, while a Portuguese matrix writes the
// ordinary word "todo" — "todo requisito" — without postponing anything.
//
// The paragraph above mentions the markers and postpones nothing, and no line of
// it opens with one, which is exactly the shape this fixture proves stays green.
// A comment whose line opens with the marker is read as a deferral even inside a
// paragraph: the rule errs towards refusing, and the cost of the mistake is a
// reworded sentence.
func Documented() {}

// Inline keeps the sentence after the marker on the same line. See the marker in
// the middle of this line: no line here opens with it, so nothing is refused.
func Inline() {}
