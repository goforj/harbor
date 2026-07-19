//go:build windows

package projectprocess

import "strings"

// environmentNameEqual follows Windows' case-insensitive environment-key semantics.
func environmentNameEqual(left, right string) bool {
	return strings.EqualFold(left, right)
}
