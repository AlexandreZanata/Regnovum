// Package duplicated is a fixture of the complexity gate: the two files of
// this directory carry the same block, written with different indentation,
// and the gate has to see one copy and not two of them.
package duplicated

// first exists to be refused, together with second.
func first(value int) int {
	return fold(value)
}

// fold is the copy. second/deep carries the same lines one level deeper,
// which the gate has to normalize away.
func fold(value int) int {
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
