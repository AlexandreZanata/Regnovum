// Package second is the other half of the duplication fixture of the complexity
// gate. Its copy of the shared block sits inside a function, indented one
// level deeper than the copy in first.go.
package second

// second exists to be refused, together with first.
func second(value int) int {
	if value < 0 {
		return 0
	}
	return wrap(value)
}

// wrap holds the copy, one indentation level deeper than fold.
func wrap(value int) int {
	step := 0
	step += value % 7
	step += value % 11
	step += value % 13
	step += value % 17
	step += value % 19
	step += value % 23
	step += value % 29
	step += value % 31
	step += value % 37
	step += value % 41
	step += value % 43
	step += value % 47
	step += value % 53
	step += value % 59
	step += value % 61
	step += value % 67
	step += value % 71
	step += value % 73
	step += value % 79
	step += value % 83
	step += value % 89
	return step
}
