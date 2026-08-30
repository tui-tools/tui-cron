package schedule

import (
	"os"
	"path/filepath"
)

// Stage writes a pending file to a private temporary directory and returns its
// path.
//
// Staging first is what makes a change reviewable and what makes the check
// meaningful: the file the user approves is a file that already exists, that
// the scheduler's own parser has already read, and that the install command
// copies byte for byte. Nothing reaches /etc or a crontab until the confirmed
// commands run.
//
// The directory is created with MkdirTemp, so it is mode 0700 and owned by this
// process. That matters for the cron path in particular: `crontab <file>` reads
// the staged table as the invoking user, and a table sitting world-writable
// between the check and the install would be a table somebody else could swap.
func Stage(name, content string) (string, error) {
	dir, err := os.MkdirTemp("", "tui-cron-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
