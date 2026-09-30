// Package falsesuccess is the fixture of the silent success: the function
// announces that it does not do the work and returns as if it had. The honest
// shape — an unfinished function that returns an error — lives under
// testdata/clean/falsesuccess, because refusing it would turn this rule into a
// rule against returning errors.
package falsesuccess

// Charge documents itself with the placeholder vocabulary in its doc comment and
// returns a value the caller reads as "it worked".
//
// The provider call is not implemented yet.
func Charge() error {
	return nil
}

// Subscribe carries the marker inside the body, which is the second place this
// gate reads: the comment a function documents itself with, and a comment the
// body holds.
func Subscribe() (bool, error) {
	// this is a placeholder for the real subscription
	return true, nil
}

// Tracked is the deferral the plan does track: the marker names the task that
// resolves it and the gate prints it — and the body is still a silent success,
// so the false success is refused whether or not the deferral is honest.
//
// TODO(P23-T04): wire the provider call.
func Tracked() error {
	return nil
}
