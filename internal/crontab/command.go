package crontab

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tui-tools/tui-cron/internal/schedule"
)

// This file builds every argv the cron half of the tool can produce. They are
// functions of their arguments and nothing else — no clock, no filesystem, no
// process — so a test can assert on the exact command line the confirm dialog
// will show, and the dialog and the execution consume the same value.

// FileMode is the mode a written /etc/cron.d file gets. cron refuses a table
// that is group or world writable, and 644 is what every distribution ships.
const FileMode = "644"

// userRe is what an account name may look like. The name goes into an argv and
// into a table's user field, so it is checked at both ends.
var userRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}\$?$`)

// cronDNameRe is what cron accepts as a file name in /etc/cron.d.
//
// It is narrower than a file name, on purpose, and cron's own rule is the
// reason: run-parts and cron ignore any file whose name contains a dot, so a
// table saved as `backup.cron` is a table that silently never runs. The form
// refuses the name rather than writing a file nobody will read.
var cronDNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// stagedRe accepts the staging path a write copies from.
var stagedRe = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

// ValidCronDName reports whether a name is one cron will actually read.
func ValidCronDName(name string) bool { return cronDNameRe.MatchString(name) }

// validUser reports whether a name is an account name.
func validUser(name string) bool { return userRe.MatchString(name) }

// CheckUser rejects an account name this tool will not pass to cron.
func CheckUser(owner string) error {
	if !validUser(owner) {
		return fmt.Errorf("crontab: %q is not an account name", owner)
	}
	return nil
}

// UnderCronD reports whether a path is a file in /etc/cron.d, with no way out
// of it.
func UnderCronD(path string) bool {
	rest, found := strings.CutPrefix(path, CronDDir+"/")
	return found && ValidCronDName(rest)
}

// TablePathFor is where cron keeps one account's table. It is reported so the
// detail screen can name the file, and never opened: `crontab` is the only
// thing that reads or writes it.
func TablePathFor(owner string) string { return "/var/spool/cron/" + owner }

// CronDPathFor is where a named table in /etc/cron.d lives. A name cron would
// not read is returned as the empty string rather than as a path, so a caller
// cannot build a destination out of one.
func CronDPathFor(name string) string {
	if !ValidCronDName(name) {
		return ""
	}
	return CronDDir + "/" + name
}

// BuildListArgv is the read that returns one account's crontab. Reading your
// own needs no privileges; reading somebody else's needs root, and `crontab -u`
// is how cron itself spells that.
func BuildListArgv(owner, me string) ([]string, error) {
	if err := CheckUser(owner); err != nil {
		return nil, err
	}
	if owner == me {
		return []string{"crontab", "-l"}, nil
	}
	return []string{"crontab", "-u", owner, "-l"}, nil
}

// BuildInstallTable replaces one account's crontab with a staged file.
//
// `crontab <file>` is the only supported way to change a table. It runs cron's
// own parser, sets the ownership and mode cron requires, and signals the daemon
// to re-read — none of which happens if the spool file is written directly, and
// on Debian a hand-written spool file is simply ignored.
func BuildInstallTable(owner, me, tempPath string) (schedule.Command, error) {
	if err := CheckUser(owner); err != nil {
		return schedule.Command{}, err
	}
	if !stagedRe.MatchString(tempPath) {
		return schedule.Command{}, fmt.Errorf(
			"crontab: %q is not a staging path", tempPath)
	}
	argv := []string{"crontab"}
	whose := "your own crontab"
	if owner != me {
		argv = append(argv, "-u", owner)
		whose = owner + "'s crontab"
	}
	argv = append(argv, tempPath)
	return schedule.Command{
		Argv: argv,
		Description: "Replace " + whose + " with " + tempPath +
			", which cron parses before it accepts",
		Destructive: true,
	}, nil
}

// BuildInstallCronD copies a staged table into /etc/cron.d, or over
// /etc/crontab.
//
// `install` is used rather than `cp` because it sets the mode in the same call,
// so there is no window where the file is on disk with a mode cron would
// refuse.
func BuildInstallCronD(tempPath, destination string) (schedule.Command, error) {
	if !stagedRe.MatchString(tempPath) {
		return schedule.Command{}, fmt.Errorf(
			"crontab: %q is not a staging path", tempPath)
	}
	if destination != SystemCrontab && !UnderCronD(destination) {
		return schedule.Command{}, fmt.Errorf(
			"crontab: %q is neither %s nor a file in %s",
			destination, SystemCrontab, CronDDir)
	}
	return schedule.Command{
		Argv:        []string{"install", "-m", FileMode, tempPath, destination},
		Description: "Install " + tempPath + " as " + destination,
		Destructive: true,
	}, nil
}

// commandRe bounds a cron command. It goes into a table as the rest of a line,
// so a newline would smuggle in a second job and is refused; a `%` is refused
// too, because cron reads it as "end of command, standard input follows" and
// nobody types one meaning that.
var commandRe = regexp.MustCompile(`^[^\n\r%]{1,512}$`)

// CheckCommand rejects a command this tool will not write into a table.
func CheckCommand(command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return fmt.Errorf("crontab: a cron job needs a command to run")
	}
	if !commandRe.MatchString(command) {
		return fmt.Errorf(
			"crontab: a command cannot contain a newline, and a %% would end it " +
				"early — cron reads everything after one as standard input")
	}
	return nil
}

// CheckLine validates a whole cron line the way cron would.
//
// It is a Go check rather than a call to a validator, because there is no
// portable one. cronie ships `crontab -T`, which parses a file and reports;
// Debian's vixie cron has no equivalent flag. A tool that used `-T` would check
// its work on Fedora and not on Ubuntu, so the check lives here and runs the
// same everywhere: the schedule through the family's own parser, the user field
// where the format has one, and the command.
func CheckLine(expression, owner, command string, withUser bool) error {
	if err := schedule.ValidateCron(expression); err != nil {
		return err
	}
	if withUser {
		if err := CheckUser(owner); err != nil {
			return err
		}
	}
	return CheckCommand(command)
}

// RenderLine builds one line of a table.
func RenderLine(expression, owner, command string, withUser bool) (string, error) {
	if err := CheckLine(expression, owner, command, withUser); err != nil {
		return "", err
	}
	parts := []string{strings.TrimSpace(expression)}
	if withUser {
		parts = append(parts, owner)
	}
	return strings.Join(append(parts, strings.TrimSpace(command)), " "), nil
}

// header is the banner tui-cron writes above a table it created. An existing
// table is never given one: the file belongs to whoever wrote it, and a tool
// that stamped its name on somebody else's crontab would be claiming it.
const header = "# Written by tui-cron.\n"

// ReplaceLine produces the new text of a table with one line replaced, added or
// removed.
//
// The table is rewritten line by line rather than regenerated, so every
// comment, every blank line and every environment assignment above the job
// survives untouched — which matters more here than anywhere else in the tool,
// because a crontab's MAILTO= and PATH= lines are load-bearing and a
// regenerated table would silently drop them.
func ReplaceLine(existing string, line int, replacement string) (string, error) {
	lines := splitTable(existing)
	switch {
	case line == 0:
		// An addition. A table that does not end in a newline would otherwise
		// join the new line onto the last one.
		lines = append(lines, replacement)
	case line < 1 || line > len(lines):
		return "", fmt.Errorf("crontab: there is no line %d in this table", line)
	case replacement == "":
		lines = append(lines[:line-1:line-1], lines[line:]...)
	default:
		lines[line-1] = replacement
	}

	text := strings.Join(lines, "\n")
	if text != "" {
		text += "\n"
	}
	if existing == "" && text != "" {
		text = header + text
	}
	return text, nil
}

// splitTable splits a table into lines without the empty element a trailing
// newline produces.
func splitTable(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}
