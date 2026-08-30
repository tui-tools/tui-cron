package jobs

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tui-tools/tui-cron/internal/schedule"
	"github.com/tui-tools/tui-cron/internal/timers"
)

// SpecFromCron turns a cron line into the timer this tool would write for it.
//
// The translation has three halves and each can refuse: the schedule, through
// schedule.CalendarFromCron; the command, which has to be something systemd can
// run; and the name, which is derived from the command because a cron line has
// none of its own.
func SpecFromCron(job schedule.Job) (schedule.NewTimer, error) {
	calendar, err := schedule.CalendarFromCron(job.Schedule)
	if err != nil {
		return schedule.NewTimer{}, err
	}
	command, err := execFromCron(job.Command)
	if err != nil {
		return schedule.NewTimer{}, err
	}
	name := NameFromCommand(job.Command)
	if err := timers.CheckName(name); err != nil {
		return schedule.NewTimer{}, fmt.Errorf(
			"no unit name could be made from %q — create the timer with c and "+
				"give it one", job.Command)
	}
	owner := job.Owner
	if owner == "root" {
		owner = ""
	}
	return schedule.NewTimer{
		Name:      name,
		Calendar:  calendar,
		ExecStart: command,
		User:      owner,
		// A cron job that is late runs when the machine comes back, because
		// cron simply runs whatever matches the next minute it sees. Persistent
		// is the setting that keeps that true after the conversion.
		Persistent:  true,
		Description: "Converted from " + job.Where() + " by tui-cron",
	}, nil
}

// shellRe matches the constructs a cron command may use that systemd will not:
// cron runs its command through /bin/sh, and a service runs it directly.
var shellRe = regexp.MustCompile(`[|&;<>$` + "`" + `(){}*?~]`)

// execFromCron checks that a cron command is one a systemd service can run.
//
// The difference is the shell. cron hands the command to `/bin/sh -c`, so a
// pipe, a redirection or a `$VAR` in a crontab all work; systemd runs ExecStart
// itself, with no shell at all, and the same line would be passed to the
// program as literal arguments. Wrapping it in `/bin/sh -c` here would be a
// conversion that changes what runs, so the line is refused instead and the
// dialog says why.
func execFromCron(command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("this line runs nothing")
	}
	if shellRe.MatchString(command) {
		return "", fmt.Errorf(
			"this line uses the shell (%s), and a systemd service has none: "+
				"ExecStart is run directly, so a pipe or a redirection would be "+
				"passed to the program as text. Put the pipeline in a script and "+
				"convert that",
			strings.Join(shellRe.FindAllString(command, 3), " "))
	}
	if !strings.HasPrefix(command, "/") {
		return "", fmt.Errorf(
			"%q is not an absolute path, and systemd resolves nothing on your "+
				"PATH: give ExecStart the full path", strings.Fields(command)[0])
	}
	return command, nil
}

// nameCleanRe is everything a unit name may not carry.
var nameCleanRe = regexp.MustCompile(`[^a-z0-9_-]+`)

// NameFromCommand derives a unit name from a cron command: the program that
// runs, lower-cased, with a `tui-cron-` prefix so a generated unit is obvious
// in `systemctl list-timers` and cannot collide with a packaged one.
func NameFromCommand(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	base := fields[0]
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	base = nameCleanRe.ReplaceAllString(strings.ToLower(base), "-")
	base = strings.Trim(base, "-")
	if base == "" {
		return ""
	}
	if len(base) > 40 {
		base = base[:40]
	}
	return "tui-cron-" + base
}
