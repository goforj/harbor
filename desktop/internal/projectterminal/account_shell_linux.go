//go:build linux

package projectterminal

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
)

// accountLoginShell returns username's shell from the Linux account database when present.
func accountLoginShell(username string) (string, bool, error) {
	password, err := os.Open("/etc/passwd")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("open local account database: %w", err)
	}
	defer func() { _ = password.Close() }()

	scanner := bufio.NewScanner(password)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ":")
		if len(fields) != 7 || fields[0] != username {
			continue
		}
		return fields[6], true, nil
	}
	if err := scanner.Err(); err != nil {
		return "", false, fmt.Errorf("read local account database: %w", err)
	}
	return "", false, nil
}
