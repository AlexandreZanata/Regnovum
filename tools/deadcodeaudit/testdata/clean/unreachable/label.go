// Package unreachable is the clean half of the rule about code that cannot run,
// and the reason it is not a rule about statements below a return: a label is a
// jump target, so the statement after it can run. The shape is odd — a loop that
// jumps past its own end — but the reader can follow it, and refusing it would be
// refusing code the program executes.
package unreachable

// Loop jumps to the label below the terminator, and the label runs.
func Loop(limit int) int {
	total := 0
	for index := 0; index < limit; index++ {
		if index == 0 {
			goto done
		}
		total += index
	}
	return total
done:
	total++
	return total
}

// Labelled is the same decision without the loop: the label is the only thing
// between the return and the code after it.
func Labelled() int {
	goto last
last:
	return 1
}
