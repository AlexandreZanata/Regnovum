// Package falsesuccess is the clean half of the silent success rule: the
// function is unfinished, says so, and refuses the work instead of answering as
// if it had done it. A body that returns an error is what an unfinished function
// owes its caller, and a gate that refused it would be a gate against returning
// errors.
package falsesuccess

// Refuse announces the same deferral the refused fixture announces and answers
// with the error that says the work is not there.
//
// TODO(P23-T04): wire the provider call.
func Refuse() error {
	return errNotReady
}

var errNotReady = &notReady{}

type notReady struct{}

func (e *notReady) Error() string { return "not ready" }
