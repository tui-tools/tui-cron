// Package timers is the systemd half of tui-cron's backend, and one of the two
// places in the repository that starts a process.
//
// Everything about reaching the machine — resolving the binaries, applying the
// privilege prefix, bounding each call, turning a failure into one readable
// line — belongs to the kit runner. What is left here is the translation
// between systemd's output and the scheduler-neutral model in
// internal/schedule, and the assembly of the argv that a confirm dialog will
// show before it runs.
//
// The programs driven, each through its own runner:
//
//	systemctl        the timer list, the per-unit properties, the unit files,
//	                 and every control action
//	systemd-analyze  the calendar check and the next elapses
//	journalctl       a job's own log
//	install/mkdir    the two commands that put a file where it belongs
//	rm               removing a drop-in this tool wrote
//
// Two managers are read, not one. `systemctl` answers for the system, and
// `systemctl --user` for the calling user's own manager — a machine reached
// over a serial console has no user bus at all, and the model says the user
// timers were not read rather than that there are none.
package timers

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tui-tools/tui-cron/internal/schedule"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/runner"
)

// FeatureTimersJSON is `systemctl list-timers --output=json`, which arrived in
// systemd 250. It is the only version gate above the minimum this tool
// declares: without it the timer list is enumerated from `list-units` instead,
// which loses nothing because every field the screen shows is read from
// `systemctl show` either way.
const FeatureTimersJSON = "timers-json"

// UnitDir is where a timer this tool creates is written, and DropInDir is where
// a schedule change goes. Both are the local administrator's directory: a unit
// a distribution shipped in /usr/lib is never edited in place.
const (
	UnitDir    = "/etc/systemd/system"
	DropInName = "90-tui-cron.conf"
)

// Marker is the phrase every file this tool writes carries in its header, and
// the only thing that makes a unit one this tool will delete or re-point.
//
// It is a marker rather than a registry on purpose. A list of "units tui-cron
// created" kept in a state file would be wrong the moment somebody moved a
// machine, restored a backup or edited a unit by hand; the file itself saying
// who wrote it survives all three, and it is visible to anyone reading /etc
// with no tool at all.
const Marker = "Written by tui-cron"

// marker is Marker folded once, for a case-insensitive search.
var marker = strings.ToLower(Marker)

// DropInPathFor is the drop-in that overrides one timer's OnCalendar.
func DropInPathFor(unit string) string {
	return UnitDir + "/" + unit + ".d/" + DropInName
}

// UnitPathFor is where a unit this tool wrote lives.
func UnitPathFor(unit string) string { return UnitDir + "/" + unit }

// MarkedByTool reports whether a file's text carries the marker.
func MarkedByTool(text string) bool {
	return strings.Contains(strings.ToLower(text), marker)
}

// ToolWrote reports whether both files of a timer are ones this tool wrote,
// which is the one thing that makes a timer this tool will delete or re-point.
//
// Three facts have to hold, and no two of them are enough. The timer has to be
// in the system manager, because a user timer's files are in the account's own
// directory and not where this tool writes. Both its unit files have to be
// exactly the paths in UnitDir this tool would have written — a unit a
// distribution shipped in /usr/lib is not ours whatever its text says. And both
// have to carry the marker, so a unit somebody else wrote into the local
// administrator's directory is left alone too.
//
// The answer is read from disk rather than remembered, and a file that cannot
// be read is not ours: the refusal is the safe direction.
func ToolWrote(job schedule.Job) bool {
	if job.Kind != schedule.KindTimer || job.Unit == "" || job.Service == "" {
		return false
	}
	if checkUnit(job.Unit) != nil || checkUnit(job.Service) != nil {
		return false
	}
	if job.File != "" && job.File != UnitPathFor(job.Unit) {
		return false
	}
	return markedFile(UnitPathFor(job.Unit)) && markedFile(UnitPathFor(job.Service))
}

// markedFile reports whether the file at a path exists and carries the marker.
func markedFile(path string) bool {
	raw, err := os.ReadFile(path) //nolint:gosec // the path is UnitDir plus a checked unit name
	if err != nil {
		return false
	}
	return MarkedByTool(string(raw))
}

// NotOursReason says why a timer is not one this tool will remove or re-point,
// in the words the status line shows.
func NotOursReason(job schedule.Job) string {
	if job.Kind == schedule.KindUserTimer {
		return job.Unit + " lives in your own systemd directory, not " + UnitDir +
			", and this tool only removes what it wrote there"
	}
	return job.Unit + " was not written by this tool: its unit files carry no " +
		"\"" + Marker + "\" header, so removing them belongs to whoever put " +
		"them there — disabling it with D stops it"
}

// DropInDirFor is the directory that drop-in lives in.
func DropInDirFor(unit string) string { return UnitDir + "/" + unit + ".d" }

// searchPaths are the locations a non-root PATH commonly omits.
var searchPaths = map[string][]string{
	"systemctl":       {"/usr/bin/systemctl", "/bin/systemctl"},
	"systemd-analyze": {"/usr/bin/systemd-analyze", "/bin/systemd-analyze"},
	"journalctl":      {"/usr/bin/journalctl", "/bin/journalctl"},
	"install":         {"/usr/bin/install", "/bin/install"},
	"rm":              {"/usr/bin/rm", "/bin/rm"},
}

// installHint is appended to the "not found" error.
const installHint = "this machine has no systemd; " +
	"or use --demo to explore the UI"

// journalLines bounds how much of a job's log one read pulls back.
const journalLines = 200

// Backend drives systemd on the host.
type Backend struct {
	systemctl *runner.Runner
	analyze   *runner.Runner
	journal   *runner.Runner
	install   *runner.Runner
	remove    *runner.Runner

	// caps gates what only exists on a new enough systemd. It comes from the
	// manifest, so no version number is written into this file.
	caps compat.Caps
}

// New locates the binaries. sudoPrefix comes from the configuration
// ("sudo -n"); pass nil to run the commands directly.
//
// Every read here is unprivileged: `systemctl show`, `list-timers`, `cat` and
// `systemd-analyze calendar` all answer to any user, and so does `journalctl`
// for a unit whose log this user may see. Only an action escalates.
func New(sudoPrefix []string, caps compat.Caps) (*Backend, error) {
	b := &Backend{caps: caps}
	unprivileged := false
	for _, spec := range []struct {
		bin    string
		target **runner.Runner
		reads  *bool
	}{
		{"systemctl", &b.systemctl, &unprivileged},
		{"systemd-analyze", &b.analyze, &unprivileged},
		{"journalctl", &b.journal, &unprivileged},
		{"install", &b.install, nil},
		{"rm", &b.remove, nil},
	} {
		r, err := runner.New(runner.Options{
			Bin:             spec.bin,
			SearchPaths:     searchPaths[spec.bin],
			SudoPrefix:      sudoPrefix,
			InstallHint:     installHint,
			PrivilegedReads: spec.reads,
		})
		if err != nil {
			// Only systemctl is essential: without systemd-analyze there is no
			// calendar check and the form says so, and without journalctl the
			// detail screen says the log could not be read.
			if spec.bin == "systemctl" {
				return nil, err
			}
			continue
		}
		*spec.target = r
	}
	return b, nil
}

// Available reports whether systemd is on this host.
func Available() bool {
	return runner.Available("systemctl", searchPaths["systemctl"]...)
}

// Describe names the backend for the header.
func (b *Backend) Describe() string { return b.systemctl.Describe() }

// Preview renders the exact command line Run will execute.
func (b *Backend) Preview(cmd schedule.Command) string {
	if run := b.runnerFor(cmd); run != nil {
		return run.Preview(cmd)
	}
	return cmd.String()
}

// Owns reports whether this backend can run a command, which is how the
// composite backend routes a confirmed command back to the right half. It is
// "can", not "built": both halves resolve `install` to the same binary with the
// same privilege prefix, so whichever one runs it produces the same process,
// and a machine with no systemd at all can still have its cron files installed
// by the other half.
func (b *Backend) Owns(cmd schedule.Command) bool {
	return b.runnerFor(cmd) != nil
}

// runnerFor picks the runner that owns a command, by its argv[0].
func (b *Backend) runnerFor(cmd schedule.Command) *runner.Runner {
	if len(cmd.Argv) == 0 {
		return nil
	}
	switch cmd.Argv[0] {
	case "systemctl":
		return b.systemctl
	case "systemd-analyze":
		return b.analyze
	case "journalctl":
		return b.journal
	case "install":
		return b.install
	case "rm":
		return b.remove
	default:
		return nil
	}
}

// Run executes a previewed command.
func (b *Backend) Run(ctx context.Context, cmd schedule.Command) (string, error) {
	run := b.runnerFor(cmd)
	if run == nil {
		return "", fmt.Errorf("timers: %q is not available on this machine",
			firstArg(cmd))
	}
	return run.Run(ctx, cmd)
}

// firstArg names the binary a command wanted, for an error message.
func firstArg(cmd schedule.Command) string {
	if len(cmd.Argv) == 0 {
		return "(empty command)"
	}
	return cmd.Argv[0]
}

// Load reads every timer both managers know about.
//
// The read is layered and every layer may fail on its own: a machine with no
// user bus still reports its system timers, and says in the model why the user
// ones are missing. Only a systemctl that cannot answer at all is a failure,
// and even that is reported rather than returned — a machine with cron and no
// usable timers is a machine this tool still has something to show.
func (b *Backend) Load(ctx context.Context) ([]schedule.Job, schedule.TimerState) {
	state := schedule.TimerState{}
	var jobs []schedule.Job

	system, err := b.loadManager(ctx, false)
	if err != nil {
		state.Detail = runner.FirstLine(err.Error())
	} else {
		state.Available = true
		jobs = append(jobs, system...)
	}

	user, err := b.loadManager(ctx, true)
	if err != nil {
		state.UserDetail = runner.FirstLine(err.Error())
	} else {
		state.UserAvailable = true
		jobs = append(jobs, user...)
	}
	return jobs, state
}

// loadManager reads one manager's timers: the unit names, then the properties
// of each timer and of the service it activates.
func (b *Backend) loadManager(ctx context.Context, user bool) ([]schedule.Job, error) {
	units, err := b.listTimers(ctx, user)
	if err != nil {
		return nil, err
	}

	kind := schedule.KindTimer
	if user {
		kind = schedule.KindUserTimer
	}
	owner := "root"
	if user {
		owner = currentUser()
	}

	jobs := make([]schedule.Job, 0, len(units))
	for _, unit := range units {
		out, showErr := b.show(ctx, user, unit, timerProperties...)
		if showErr != nil {
			continue
		}
		job := JobFromTimer(ParseProperties(out), kind, owner)
		if job.ID == "" {
			continue
		}
		if job.Service != "" {
			if serviceOut, serviceErr := b.show(ctx, user, job.Service,
				serviceProperties...); serviceErr == nil {
				ApplyService(&job, ParseProperties(serviceOut))
			}
		}
		// Whether this tool wrote the timer is read once, here, so the actions
		// that depend on it do not each go back to the disk.
		job.ToolWritten = ToolWrote(job)
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// listTimers enumerates the timer units of one manager.
//
// Two lists are read and merged, because neither is complete on its own.
//
// The loaded timers come from `list-timers --output=json` when the running
// systemd has it, and from `list-units --type=timer` when it does not. The
// fallback loses nothing: the JSON carries the next and last elapse, and both
// of those are read again from `systemctl show` for every timer anyway,
// because the JSON does not carry the calendar expression and that is the
// column this tool is built around.
//
// The unit files on disk come from `list-unit-files --type=timer`. systemd
// loads no unit that nothing references, so a timer this tool has just written
// and not enabled is in none of the lists above — it would be on disk,
// correct, and invisible in the tool that wrote it, with no row to select and
// therefore no way to enable, run or delete it. Reading the unit files as well
// puts it on the list the moment it exists.
func (b *Backend) listTimers(ctx context.Context, user bool) ([]string, error) {
	loaded, err := b.listLoadedTimers(ctx, user)
	if err != nil {
		return nil, err
	}
	// The unit-file list is an addition, not a requirement: an old or odd
	// systemctl that refuses it leaves the loaded timers exactly as they were.
	argv := b.systemctlArgv(user, "list-unit-files", "--type=timer",
		"--no-legend", "--plain", "--no-pager")
	if out, fileErr := b.systemctl.Read(ctx, argv...); fileErr == nil {
		loaded = mergeUnits(loaded, ParseUnitFileList(out))
	}
	return loaded, nil
}

// listLoadedTimers enumerates the timer units the manager currently has loaded.
func (b *Backend) listLoadedTimers(ctx context.Context, user bool) ([]string, error) {
	if b.caps.Has(FeatureTimersJSON) {
		argv := b.systemctlArgv(user, "list-timers", "--all", "--output=json",
			"--no-pager")
		out, err := b.systemctl.Read(ctx, argv...)
		if err == nil {
			units, parseErr := ParseTimerListJSON(out)
			if parseErr == nil {
				return units, nil
			}
		} else {
			return nil, err
		}
	}

	argv := b.systemctlArgv(user, "list-units", "--type=timer", "--all",
		"--plain", "--no-legend", "--no-pager")
	out, err := b.systemctl.Read(ctx, argv...)
	if err != nil {
		return nil, err
	}
	return ParseTimerListText(out), nil
}

// mergeUnits appends the names of extra that first does not already have,
// keeping the order of the loaded list: the timers that are running stay where
// the reader last saw them, and anything only on disk lands after them.
func mergeUnits(first, extra []string) []string {
	seen := make(map[string]struct{}, len(first)+len(extra))
	for _, unit := range first {
		seen[unit] = struct{}{}
	}
	for _, unit := range extra {
		if _, ok := seen[unit]; ok {
			continue
		}
		seen[unit] = struct{}{}
		first = append(first, unit)
	}
	return first
}

// show reads one unit's properties.
func (b *Backend) show(ctx context.Context, user bool, unit string,
	properties ...string) (string, error) {
	argv := b.systemctlArgv(user, "show", unit)
	for _, property := range properties {
		argv = append(argv, "--property="+property)
	}
	return b.systemctl.Read(ctx, argv...)
}

// systemctlArgv builds a systemctl invocation for one manager.
func (b *Backend) systemctlArgv(user bool, args ...string) []string {
	argv := []string{"systemctl"}
	if user {
		argv = append(argv, "--user")
	}
	return append(argv, args...)
}

// Definition returns `systemctl cat` for a timer and the service it activates,
// which is the unit file plus every drop-in, in the order systemd merges them.
func (b *Backend) Definition(ctx context.Context, job schedule.Job) (string, error) {
	user := job.Kind == schedule.KindUserTimer
	timer, err := b.systemctl.Read(ctx,
		b.systemctlArgv(user, "cat", "--no-pager", job.Unit)...)
	if err != nil {
		return "", err
	}
	if job.Service == "" {
		return timer, nil
	}
	service, err := b.systemctl.Read(ctx,
		b.systemctlArgv(user, "cat", "--no-pager", job.Service)...)
	if err != nil {
		// A timer whose service has no unit file of its own is unusual but
		// legal, and the timer's own text is still worth showing.
		return timer, nil
	}
	return timer + "\n" + service, nil
}

// Journal returns the last lines of a job's log. The service is asked for, not
// the timer: the timer logs that it fired, and what the reader wants is what
// the job then did.
func (b *Backend) Journal(ctx context.Context, job schedule.Job,
	lines int) (string, error) {
	if b.journal == nil {
		return "", fmt.Errorf("timers: journalctl is not installed on this machine")
	}
	unit := job.Service
	if unit == "" {
		unit = job.Unit
	}
	argv := []string{"journalctl", "--no-pager", "-o", "short-iso",
		"-n", fmt.Sprint(clampLines(lines))}
	if job.Kind == schedule.KindUserTimer {
		argv = append(argv, "--user-unit", unit)
	} else {
		argv = append(argv, "-u", unit)
	}
	return b.journal.Read(ctx, argv...)
}

// clampLines keeps a journal read to a sane size.
func clampLines(lines int) int {
	if lines <= 0 || lines > 2000 {
		return journalLines
	}
	return lines
}

// Elapses returns the next n times a calendar expression fires, as systemd
// itself computes them. It is the answer to "is that really what I meant",
// and it is systemd's answer rather than this tool's.
func (b *Backend) Elapses(ctx context.Context, expression string,
	n int) ([]string, error) {
	cmd, err := BuildCalendar(expression, n)
	if err != nil {
		return nil, err
	}
	if b.analyze == nil {
		return nil, fmt.Errorf("timers: systemd-analyze is not installed on this machine")
	}
	out, err := b.analyze.Read(ctx, cmd.Argv...)
	if err != nil {
		return nil, fmt.Errorf("%s", runner.FirstLine(out))
	}
	return ParseElapses(out), nil
}

// CheckCalendar asks systemd whether an expression is a calendar it accepts,
// and returns the normalized form it printed.
func (b *Backend) CheckCalendar(ctx context.Context,
	expression string) (normalized string, err error) {
	cmd, err := BuildCalendar(expression, 1)
	if err != nil {
		return "", err
	}
	if b.analyze == nil {
		return "", fmt.Errorf("systemd-analyze is not installed, so the " +
			"expression could not be checked")
	}
	out, err := b.analyze.Read(ctx, cmd.Argv...)
	if err != nil {
		return "", fmt.Errorf("%s", runner.FirstLine(out))
	}
	return ParseNormalized(out), nil
}

// VerifyUnits asks systemd to parse staged unit files. It is a read: it prints
// what it thinks of them and exits, and it is the reason a unit systemd would
// refuse never reaches /etc.
func (b *Backend) VerifyUnits(ctx context.Context, paths ...string) (string, error) {
	cmd, err := BuildVerify(paths...)
	if err != nil {
		return "", err
	}
	if b.analyze == nil {
		return "", fmt.Errorf("systemd-analyze is not installed, so the units " +
			"could not be checked")
	}
	out, runErr := b.analyze.Read(ctx, cmd.Argv...)
	if runErr != nil {
		return out, fmt.Errorf("%s", runner.FirstLine(out))
	}
	return out, nil
}

// VerifyCommand renders the verify command line, for the confirm dialog.
func (b *Backend) VerifyCommand(paths ...string) string {
	cmd, err := BuildVerify(paths...)
	if err != nil {
		return ""
	}
	return b.Preview(cmd)
}

// CalendarCommand renders the calendar check command line.
func (b *Backend) CalendarCommand(expression string) string {
	cmd, err := BuildCalendar(expression, 1)
	if err != nil {
		return ""
	}
	return b.Preview(cmd)
}

// DropInContent returns what the drop-in for a unit says today, empty when
// there is none. It is read unprivileged: /etc/systemd/system is world
// readable on every distribution this tool targets.
func (b *Backend) DropInContent(unit string) string {
	raw, err := os.ReadFile(DropInPathFor(unit)) //nolint:gosec // the path is built from a checked unit name
	if err != nil {
		return ""
	}
	return string(raw)
}

// UnitContent returns what a unit file in UnitDir says today, empty when there
// is none. Like DropInContent it is read unprivileged: /etc/systemd/system is
// world readable on every distribution this tool targets, and the text is what
// the confirm dialog diffs away when the file is about to be removed.
func (b *Backend) UnitContent(unit string) string {
	if checkUnit(unit) != nil {
		return ""
	}
	raw, err := os.ReadFile(UnitPathFor(unit)) //nolint:gosec // the path is UnitDir plus a checked unit name
	if err != nil {
		return ""
	}
	return string(raw)
}

// currentUser names the account the tool runs as, for the owner column of a
// user timer.
func currentUser() string {
	for _, key := range []string{"SUDO_USER", "USER", "LOGNAME"} {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return "you"
}

// timerProperties are what one timer is read with. They are named explicitly
// rather than taking the whole property dump, so the fixture a test asserts on
// is the output of the command the tool really runs.
var timerProperties = []string{
	"Id", "Unit", "Description", "TimersCalendar", "TimersMonotonic",
	"NextElapseUSecRealtime", "LastTriggerUSec", "Persistent", "AccuracyUSec",
	"RandomizedDelayUSec", "LoadState", "ActiveState", "SubState",
	"UnitFileState", "FragmentPath",
}

// serviceProperties are what the activated unit is read with: what it runs,
// and how the last run ended.
var serviceProperties = []string{
	"Id", "Description", "Result", "ExecMainStatus", "ExecMainExitTimestamp",
	"ExecStart", "ActiveState", "SubState", "LoadState", "FragmentPath",
}

// SortJobs orders the timers the way the list shows them: the ones that failed
// first, because those are why anyone opened the tool, then by the next run,
// then by name.
func SortJobs(jobs []schedule.Job) {
	sort.SliceStable(jobs, func(i, j int) bool {
		if jobs[i].Failed() != jobs[j].Failed() {
			return jobs[i].Failed()
		}
		return jobs[i].Name < jobs[j].Name
	})
}

// unitNameRe is the set of characters a systemd unit name may contain. The
// name comes from the machine and ends up in an argv, so it is checked like
// every other value that makes that trip.
var unitNameRe = regexp.MustCompile(`^[A-Za-z0-9@._:\\-]{1,128}\.(timer|service)$`)

// baseNameRe is what a new timer may be called. It is deliberately narrower
// than what systemd accepts: this is a name a form produced, and a name with an
// escape in it is a name nobody typed on purpose.
var baseNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// checkUnit rejects a unit name that is not one.
func checkUnit(unit string) error {
	if !unitNameRe.MatchString(unit) {
		return fmt.Errorf("timers: %q is not a unit name", unit)
	}
	if strings.Contains(unit, "..") || strings.ContainsAny(unit, "/ ") {
		return fmt.Errorf("timers: %q is not a unit name", unit)
	}
	return nil
}

// Stamp names the moment a generated unit records, so a test and a screenshot
// get the same header every time.
func Stamp(now time.Time) string { return now.UTC().Format("2006-01-02 15:04:05 UTC") }
