package timers

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tui-tools/tui-cron/internal/schedule"
)

// This file builds every argv the systemd half of the tool can produce. They
// are functions of their arguments and nothing else — no clock, no filesystem,
// no process — so a test can assert on the exact command line the confirm
// dialog will show, and the dialog and the execution consume the same value.

// stagedRe accepts the staging path an install command copies from. It is a
// path this package built, and it is checked anyway.
var stagedRe = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

// FileMode is the mode a written unit or drop-in gets. Unit files are read by
// systemd as root and by anyone reading /etc, and carry no secret of their own.
const FileMode = "644"

// managerArgv prefixes `--user` for a timer in the calling user's own manager.
func managerArgv(job schedule.Job, args ...string) []string {
	argv := []string{"systemctl"}
	if job.Kind == schedule.KindUserTimer {
		argv = append(argv, "--user")
	}
	return append(argv, args...)
}

// scope names which manager a command applies to, for the description.
func scope(job schedule.Job) string {
	if job.Kind == schedule.KindUserTimer {
		return " in your own systemd manager"
	}
	return ""
}

// BuildControl is the shared shape of enable, disable, start and stop: one
// verb, one unit.
func BuildControl(job schedule.Job, verb string) (schedule.Command, error) {
	if !job.Kind.Systemd() {
		return schedule.Command{}, fmt.Errorf(
			"%s is a cron job, and systemctl %s does not apply to it", job.Name, verb)
	}
	if err := checkUnit(job.Unit); err != nil {
		return schedule.Command{}, err
	}
	descriptions := map[string]string{
		"enable":  "Make " + job.Unit + " start at boot",
		"disable": "Stop " + job.Unit + " starting at boot",
		"start":   "Load " + job.Unit + " now, so it starts counting down",
		"stop":    "Unload " + job.Unit + " now, so it stops counting down",
	}
	description, ok := descriptions[verb]
	if !ok {
		return schedule.Command{}, fmt.Errorf("timers: %q is not a control verb", verb)
	}
	return schedule.Command{
		Argv:        managerArgv(job, verb, job.Unit),
		Description: description + scope(job),
		// Disabling and stopping both mean the job stops happening, which is
		// the kind of change somebody wants to read twice.
		Destructive: verb == "disable" || verb == "stop",
	}, nil
}

// BuildRunNow triggers the job immediately.
//
// The *service* is started, not the timer. Starting the timer only arms it;
// what a reader means by "run it now" is the thing the timer would have run,
// and that is the unit named in the timer's Unit= property.
func BuildRunNow(job schedule.Job) (schedule.Command, error) {
	if !job.Kind.Systemd() {
		return schedule.Command{}, fmt.Errorf(
			"%s is a cron job; there is no unit to start", job.Name)
	}
	if job.Service == "" {
		return schedule.Command{}, fmt.Errorf(
			"%s activates no unit this tool could start", job.Unit)
	}
	if err := checkUnit(job.Service); err != nil {
		return schedule.Command{}, err
	}
	return schedule.Command{
		Argv: managerArgv(job, "start", job.Service),
		Description: "Run " + job.Service + " now, without waiting for " +
			job.Unit + scope(job),
		// It runs a job off its schedule, which for a backup or a package
		// update is not a read.
		Destructive: true,
	}, nil
}

// BuildDaemonReload makes systemd re-read the unit files from disk. It follows
// every write, because a unit file systemd has not read is a unit file that
// does nothing.
func BuildDaemonReload(job schedule.Job) schedule.Command {
	return schedule.Command{
		Argv:        managerArgv(job, "daemon-reload"),
		Description: "Re-read every unit file from disk" + scope(job),
	}
}

// BuildRestartTimer restarts a timer so it picks up its new schedule. A reload
// is not enough: the timer's next elapse was computed when it was armed.
func BuildRestartTimer(job schedule.Job) (schedule.Command, error) {
	if err := checkUnit(job.Unit); err != nil {
		return schedule.Command{}, err
	}
	return schedule.Command{
		Argv: managerArgv(job, "restart", job.Unit),
		Description: "Restart " + job.Unit +
			" so it re-arms on the new schedule" + scope(job),
	}, nil
}

// BuildCalendar asks systemd to parse a calendar expression and print its next
// elapses. It is a read, and it is what validates an OnCalendar before the user
// is asked to confirm anything.
func BuildCalendar(expression string, iterations int) (schedule.Command, error) {
	if err := CheckExpression(expression); err != nil {
		return schedule.Command{}, err
	}
	if iterations < 1 || iterations > 20 {
		iterations = 5
	}
	return schedule.Command{
		Argv: []string{"systemd-analyze", "calendar",
			fmt.Sprintf("--iterations=%d", iterations), expression},
		Description: "Ask systemd what " + expression + " means and when it fires next",
	}, nil
}

// BuildVerify asks systemd to parse staged unit files.
//
// It applies to whole units only. `systemd-analyze verify` decides what it is
// looking at from the file's suffix, and answers a drop-in `.conf` with
// "Failed to prepare filename: Invalid argument" — so a schedule change is
// checked with `systemd-analyze calendar` instead, which is the check that
// actually covers what changed.
func BuildVerify(paths ...string) (schedule.Command, error) {
	if len(paths) == 0 {
		return schedule.Command{}, fmt.Errorf("timers: nothing to verify")
	}
	for _, path := range paths {
		if !stagedRe.MatchString(path) || !strings.HasSuffix(path, ".timer") &&
			!strings.HasSuffix(path, ".service") {
			return schedule.Command{}, fmt.Errorf(
				"timers: %q is not a staged unit file", path)
		}
	}
	return schedule.Command{
		Argv:        append([]string{"systemd-analyze", "verify"}, paths...),
		Description: "Check the staged units with systemd's own parser",
	}, nil
}

// BuildMakeDropInDir creates the drop-in directory for a unit.
func BuildMakeDropInDir(unit string) (schedule.Command, error) {
	if err := checkUnit(unit); err != nil {
		return schedule.Command{}, err
	}
	dir := DropInDirFor(unit)
	return schedule.Command{
		Argv:        []string{"install", "-d", "-m", "755", dir},
		Description: "Create " + dir,
		Destructive: true,
	}, nil
}

// BuildInstall copies a staged file to its destination.
//
// `install` is used rather than `cp` because it sets the mode in the same call,
// so there is no window where the file is on disk with the wrong one.
func BuildInstall(tempPath, destination string) (schedule.Command, error) {
	if !stagedRe.MatchString(tempPath) {
		return schedule.Command{}, fmt.Errorf("timers: %q is not a staging path", tempPath)
	}
	if !strings.HasPrefix(destination, UnitDir+"/") ||
		strings.Contains(destination, "..") {
		return schedule.Command{}, fmt.Errorf(
			"timers: %q is not under %s", destination, UnitDir)
	}
	return schedule.Command{
		Argv:        []string{"install", "-m", FileMode, tempPath, destination},
		Description: "Install " + tempPath + " as " + destination,
		Destructive: true,
	}, nil
}

// BuildRemoveDropIn deletes the drop-in this tool wrote for a unit, which is
// what "put the schedule back" means.
func BuildRemoveDropIn(unit string) (schedule.Command, error) {
	if err := checkUnit(unit); err != nil {
		return schedule.Command{}, err
	}
	return schedule.Command{
		Argv:        []string{"rm", "-f", "--", DropInPathFor(unit)},
		Description: "Remove " + DropInPathFor(unit),
		Destructive: true,
	}, nil
}

// BuildDisableNow stops a unit and stops it starting at boot, in one call. It
// is the first half of removing a timer: a unit file deleted while systemd
// still has the unit loaded leaves a timer armed with no file behind it, which
// is the one state nobody can explain afterwards.
func BuildDisableNow(job schedule.Job) (schedule.Command, error) {
	if !job.Kind.Systemd() {
		return schedule.Command{}, fmt.Errorf(
			"%s is a cron job, and systemctl disable does not apply to it", job.Name)
	}
	if err := checkUnit(job.Unit); err != nil {
		return schedule.Command{}, err
	}
	return schedule.Command{
		Argv: managerArgv(job, "disable", "--now", job.Unit),
		Description: "Stop " + job.Unit + " now and stop it starting at boot" +
			scope(job),
		Destructive: true,
	}, nil
}

// BuildRemoveUnit deletes a unit file this tool wrote.
//
// The path is assembled here from a checked unit name rather than taken from
// the caller, and it can only ever be a file directly inside UnitDir — so this
// builder cannot be pointed at a unit a distribution shipped in /usr/lib, and
// cannot be pointed anywhere else at all.
func BuildRemoveUnit(unit string) (schedule.Command, error) {
	if err := checkUnit(unit); err != nil {
		return schedule.Command{}, err
	}
	path := UnitPathFor(unit)
	return schedule.Command{
		Argv:        []string{"rm", "-f", "--", path},
		Description: "Remove " + path,
		Destructive: true,
	}, nil
}

// expressionRe bounds what may be passed to systemd as a calendar expression.
// The value goes into an argv and then into a file in /etc, so the characters
// that would smuggle a second directive into that file — a newline, a `#`, a
// `[` — are refused here rather than after the write.
var expressionRe = regexp.MustCompile(`^[A-Za-z0-9 ,:.*/+-]{1,128}$`)

// CheckExpression rejects a calendar expression this tool will not pass on.
// It is a syntactic guard, not a calendar check: whether systemd accepts the
// expression is systemd's question, and `systemd-analyze calendar` asks it.
func CheckExpression(expression string) error {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return fmt.Errorf("timers: a schedule needs an OnCalendar expression")
	}
	if !expressionRe.MatchString(expression) {
		return fmt.Errorf(
			"timers: %q is not an OnCalendar expression — it may contain only "+
				"letters, digits, spaces and , : . * / + -", expression)
	}
	return nil
}

// dropInHeader is the banner every generated drop-in carries. It names the
// tool, and it states the one rule a reader of this file has to know.
func dropInHeader(setting string) string {
	return "# " + Marker + ", and rewritten whole on every change.\n" +
		"# The empty " + setting + "= below clears what the unit file set, so\n" +
		"# what follows replaces it rather than being added to it.\n"
}

// RenderDropIn produces the drop-in that replaces a timer's schedule.
//
// The empty `OnCalendar=` is the whole trick, and it is why this cannot be a
// one-line file. A drop-in that only says `OnCalendar=<new>` *adds* a schedule:
// systemd would then fire the timer on the old one as well, which is exactly
// the surprise this tool exists to prevent. Assigning the empty value first
// resets the list.
func RenderDropIn(expression string) (string, error) {
	if err := CheckExpression(expression); err != nil {
		return "", err
	}
	return dropInHeader("OnCalendar") + "\n[Timer]\nOnCalendar=\nOnCalendar=" +
		strings.TrimSpace(expression) + "\n", nil
}

// RenderExecDropIn produces the drop-in that replaces a service's ExecStart.
//
// The empty `ExecStart=` is the same trick and the same reason: ExecStart is a
// list too, and a drop-in that only names the new command would leave the
// service running both, one after the other. Assigning the empty value first
// resets the list.
func RenderExecDropIn(command string) (string, error) {
	if err := CheckExec(command); err != nil {
		return "", err
	}
	return dropInHeader("ExecStart") + "\n[Service]\nExecStart=\nExecStart=" +
		strings.TrimSpace(command) + "\n", nil
}

// UnitPathsFor is where a created or converted timer's two files go.
func UnitPathsFor(name string) (timerPath, servicePath string) {
	return UnitDir + "/" + name + ".timer", UnitDir + "/" + name + ".service"
}

// CheckName rejects a name a new timer may not be given.
func CheckName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("timers: a new timer needs a name")
	}
	name = strings.TrimSuffix(strings.TrimSuffix(name, ".timer"), ".service")
	if !baseNameRe.MatchString(name) {
		return fmt.Errorf(
			"timers: %q is not a name for a unit — use lower-case letters, "+
				"digits, - and _, starting with a letter or a digit", name)
	}
	return nil
}

// BaseName strips a .timer or .service suffix, so a form accepts either.
func BaseName(name string) string {
	name = strings.TrimSpace(name)
	return strings.TrimSuffix(strings.TrimSuffix(name, ".timer"), ".service")
}

// execRe bounds an ExecStart. It goes into a unit file, so a newline or a `[`
// would let a form write a second section.
var execRe = regexp.MustCompile(`^[^\n\r\[\]]{1,512}$`)

// userRe bounds the account a generated service runs as.
var userRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}\$?$`)

// CheckExec rejects a command a generated unit will not carry.
func CheckExec(command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return fmt.Errorf("timers: a timer needs a command to run")
	}
	if !execRe.MatchString(command) {
		return fmt.Errorf("timers: a command cannot contain a newline or a [ ]")
	}
	if !strings.HasPrefix(command, "/") {
		return fmt.Errorf(
			"timers: %q must be an absolute path — systemd runs a service with "+
				"no shell and no PATH of yours, so `%s` would not be found",
			command, strings.Fields(command)[0])
	}
	return nil
}

// CheckUser rejects an account a generated service will not run as.
func CheckUser(user string) error {
	if strings.TrimSpace(user) == "" {
		return nil
	}
	if !userRe.MatchString(user) {
		return fmt.Errorf("timers: %q is not an account name", user)
	}
	return nil
}

// RenderUnits produces the .service and .timer pair for a new timer.
//
// The service is Type=oneshot: it runs, it finishes, and the timer arms again.
// Anything else would leave systemd waiting for a process that already exited,
// and report the job as failed when it had not.
func RenderUnits(spec schedule.NewTimer, stamp string) (service, timer string, err error) {
	if err := CheckName(spec.Name); err != nil {
		return "", "", err
	}
	if err := CheckExec(spec.ExecStart); err != nil {
		return "", "", err
	}
	if err := CheckUser(spec.User); err != nil {
		return "", "", err
	}
	if err := CheckExpression(spec.Calendar); err != nil {
		return "", "", err
	}
	if err := checkDescription(spec.Description); err != nil {
		return "", "", err
	}

	name := BaseName(spec.Name)
	description := strings.TrimSpace(spec.Description)
	if description == "" {
		description = name
	}
	// The header is the ownership marker. It is what later tells the tool that
	// this pair of files is one it may remove or re-point, and it is what tells
	// a person reading /etc the same thing.
	header := "# " + Marker + " on " + stamp + ".\n"

	var serviceBody strings.Builder
	serviceBody.WriteString(header)
	serviceBody.WriteString("\n[Unit]\nDescription=" + description + "\n")
	serviceBody.WriteString("\n[Service]\nType=oneshot\n")
	if user := strings.TrimSpace(spec.User); user != "" {
		serviceBody.WriteString("User=" + user + "\n")
	}
	serviceBody.WriteString("ExecStart=" + strings.TrimSpace(spec.ExecStart) + "\n")

	var timerBody strings.Builder
	timerBody.WriteString(header)
	timerBody.WriteString("\n[Unit]\nDescription=" + description + " (timer)\n")
	timerBody.WriteString("\n[Timer]\nOnCalendar=" +
		strings.TrimSpace(spec.Calendar) + "\n")
	if spec.Persistent {
		timerBody.WriteString("Persistent=true\n")
	}
	timerBody.WriteString("Unit=" + name + ".service\n")
	timerBody.WriteString("\n[Install]\nWantedBy=timers.target\n")

	return serviceBody.String(), timerBody.String(), nil
}

// descriptionRe bounds a unit Description.
var descriptionRe = regexp.MustCompile(`^[^\n\r\[\]]{0,200}$`)

func checkDescription(description string) error {
	if !descriptionRe.MatchString(description) {
		return fmt.Errorf("timers: a description cannot contain a newline or a [ ]")
	}
	return nil
}
