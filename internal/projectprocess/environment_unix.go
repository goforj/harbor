//go:build !windows

package projectprocess

// environmentNameEqual preserves Unix's case-sensitive environment-key semantics.
func environmentNameEqual(left, right string) bool {
	return left == right
}
