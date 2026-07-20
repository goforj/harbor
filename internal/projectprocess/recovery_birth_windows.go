//go:build windows

package projectprocess

// hostProcessBirthToken returns the native Windows creation token stored as durable evidence.
func hostProcessBirthToken(persistedBirthToken string) string {
	return persistedBirthToken
}
