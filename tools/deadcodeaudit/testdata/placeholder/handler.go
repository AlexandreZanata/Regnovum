// Package placeholder is the fixture of the panic family: the message says the
// work is not done. The scanner of this gate reads these files by name — the Go
// toolchain skips testdata in every ./... pattern — and requires each rule to
// refuse its own fixture.
package placeholder

// Dispatch is the shape the rule refuses: a branch that answers with a message
// saying it was never written.
func Dispatch(command string) string {
	switch command {
	case "open":
		return "open"
	default:
		panic("command is not implemented")
	}
}

// Resume is the same refusal with the other half of the vocabulary, and the one
// that would survive a reviewer skimming for `panic(`.
func Resume() {
	panic("resume is a stub: por enquanto nada acontece aqui")
}
