// Package deferred is the fixture of the untracked postponement: the marker is
// at the head of the comment and names nobody, which is the shape the rule
// refuses. The same file carries the shapes it accepts, so that a rule that
// stopped reading references would fail here instead of passing quietly; the
// comments that only discuss the vocabulary live under testdata/clean/deferred.
package deferred

// TODO: fix this later.
func untracked() {}

// TODO(soon): do it before the release.
func wrongReference() {}

// TODO(P23-T04) close the port before the next task.
func tracked() {}

// FIXME(#85): the retry loop duplicates the attempt.
func trackedIssue() {}

// TODO(  ): a reference of spaces is not a reference.
func blankReference() {}
