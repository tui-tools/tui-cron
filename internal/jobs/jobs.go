// Package jobs joins the two halves of tui-cron into the one backend the UI
// talks to.
//
// It starts no process of its own. Every command it returns was built by
// internal/timers or internal/crontab, and running one is handed straight back
// to whichever of them can: that keeps the exec boundary at two packages, and
// it keeps this file about the only question it is really for — which
// scheduler owns the job under the cursor, and what that means for the action
// the user just pressed.
//
// The merge is the point of the tool. A machine's scheduled work is split
// across systemd and cron for historical reasons and nobody's benefit, and the
// two are asked different questions with different commands and answered in
// different formats. Here they are one list, sorted by what a reader wants
// first: the jobs that failed, then the ones about to run.
package jobs

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tui-tools/tui-cron/internal/crontab"
	"github.com/tui-tools/tui-cron/internal/schedule"
	"github.com/tui-tools/tui-cron/internal/timers"
	"github.com/tui-tools/tui-kit/compat"
)

// Elapses is how many future runs the detail screen asks systemd for.
const Elapses = 5

// Real drives the machine's real schedulers. It satisfies schedule.Backend.
type Real struct {
	// timers is nil on a machine without systemd, which is a machine where
	// every action that names a unit refuses in those words.
	timers *timers.Backend
	cron   *crontab.Backend
	// now names a generated unit's header. It is a field so a test and a
	// screenshot get the same file every time.
	now func() time.Time
}

// NewReal locates both halves. sudoPrefix comes from the configuration; caps
// comes from the systemd version probe, so no version number is written into
// the code.
//
// Neither half is required. A container with timers and no cron and a machine
// with cron and no systemd are both normal, and the model says which one it
// found rather than refusing to start — the only failure is a machine with
// neither, which has no scheduled jobs for this tool to be about.
func NewReal(sudoPrefix []string, caps compat.Caps) (*Real, error) {
	real := &Real{cron: crontab.New(sudoPrefix), now: time.Now}
	if timers.Available() {
		backend, err := timers.New(sudoPrefix, caps)
		if err != nil {
			return nil, err
		}
		real.timers = backend
	}
	if real.timers == nil && !crontab.Installed() {
		return nil, fmt.Errorf(
			"neither systemd nor cron is installed on this machine, so there " +
				"are no scheduled jobs to show; use --demo to explore the UI")
	}
	return real, nil
}

// Name identifies the backend. It is the one name the manifest keys the tool
// on; the two schedulers are named separately in the compatibility block.
func (r *Real) Name() string { return "host" }

// Describe names both halves for the header.
func (r *Real) Describe() string {
	parts := []string{"cron unavailable"}
	if r.cron != nil {
		parts = []string{r.cron.Describe()}
	}
	if r.timers != nil {
		parts = append([]string{r.timers.Describe()}, parts...)
	}
	return strings.Join(parts, " · ")
}

// Capabilities reports what this backend supports. The answers depend on the
// machine: without systemd there is nothing to enable, and without cron there
// is no table to write a line into.
func (r *Real) Capabilities() schedule.Capabilities {
	return schedule.Capabilities{
		SupportsTimerControl: r.timers != nil,
		SupportsTimerEdit:    r.timers != nil,
		SupportsTimerCreate:  r.timers != nil,
		SupportsCronEdit:     r.cron != nil,
		SupportsConvert:      r.timers != nil,
		DropInFor:            timers.DropInPathFor,
		UnitDir:              timers.UnitDir,
	}
}

// Preview renders the exact command line Run will execute. Every command goes
// through the runner of the half that owns it, so the preview carries the
// privilege prefix that binary will really be called with.
func (r *Real) Preview(cmd schedule.Command) string {
	if r.timers != nil && r.timers.Owns(cmd) {
		return r.timers.Preview(cmd)
	}
	if r.cron != nil && r.cron.Owns(cmd) {
		return r.cron.Preview(cmd)
	}
	return cmd.String()
}

// Run executes a previewed command through whichever half can.
//
// The two halves resolve `install` to the same binary with the same privilege
// prefix, so which of them runs it makes no difference to the process — and
// asking both means a cron file can still be installed on a machine with no
// systemd, and a unit on one with no cron.
func (r *Real) Run(ctx context.Context, cmd schedule.Command) (string, error) {
	if r.timers != nil && r.timers.Owns(cmd) {
		return r.timers.Run(ctx, cmd)
	}
	if r.cron != nil && r.cron.Owns(cmd) {
		return r.cron.Run(ctx, cmd)
	}
	return "", fmt.Errorf("jobs: %q is not available on this machine",
		firstArg(cmd))
}

// firstArg names the binary a command wanted, for an error message.
func firstArg(cmd schedule.Command) string {
	if len(cmd.Argv) == 0 {
		return "(empty command)"
	}
	return cmd.Argv[0]
}

// Load reads both schedulers.
func (r *Real) Load(ctx context.Context) (schedule.Model, error) {
	model := schedule.Model{Backend: r.Name(), User: crontab.CurrentUser()}

	if r.timers != nil {
		found, state := r.timers.Load(ctx)
		model.Jobs, model.Timers = found, state
	} else {
		model.Timers.Detail = "systemd is not installed on this machine"
	}
	if r.cron != nil {
		found, state := r.cron.Load(ctx)
		model.Jobs, model.Cron = append(model.Jobs, found...), state
	}

	if len(model.Jobs) == 0 && !model.Timers.Available && !model.Cron.Installed {
		return schedule.Model{}, fmt.Errorf(
			"jobs: neither systemd nor cron could be read: %s",
			strings.TrimSpace(model.Timers.Detail+" "+model.Cron.Detail))
	}
	Sort(model.Jobs)
	return model, nil
}

// Sort orders the merged list the way a reader wants to read it: what failed
// first, because that is why anyone opened the tool; then what runs soonest,
// because that is the next thing to happen; then the jobs with no computable
// next run, by name.
func Sort(list []schedule.Job) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Failed() != b.Failed() {
			return a.Failed()
		}
		if a.Next.IsZero() != b.Next.IsZero() {
			return !a.Next.IsZero()
		}
		if !a.Next.IsZero() && !a.Next.Equal(b.Next) {
			return a.Next.Before(b.Next)
		}
		return a.Name < b.Name
	})
}

// half returns the backend that owns a job, or an error naming why neither
// does.
func (r *Real) half(job schedule.Job) (systemd bool, err error) {
	switch {
	case job.Kind.Systemd() && r.timers == nil:
		return false, fmt.Errorf("systemd is not installed on this machine")
	case job.Kind.Systemd():
		return true, nil
	case r.cron == nil:
		return false, fmt.Errorf("cron is not installed on this machine")
	default:
		return false, nil
	}
}

// Definition returns the job's own text.
func (r *Real) Definition(ctx context.Context, job schedule.Job) (string, error) {
	systemd, err := r.half(job)
	if err != nil {
		return "", err
	}
	if systemd {
		return r.timers.Definition(ctx, job)
	}
	return r.cron.Definition(ctx, job)
}

// Journal returns the last log lines for a job.
func (r *Real) Journal(ctx context.Context, job schedule.Job,
	lines int) (string, error) {
	systemd, err := r.half(job)
	if err != nil {
		return "", err
	}
	if systemd {
		return r.timers.Journal(ctx, job, lines)
	}
	return r.cron.Journal(ctx, job, lines)
}

// Elapses returns the next n times a job's schedule fires.
//
// It is systemd's answer, and it is only available for a timer. cron computes
// nothing ahead of time and publishes nothing: for a cron line the schedule
// and its English are the whole answer, and claiming a date this tool worked
// out itself would be claiming cron agrees with it.
func (r *Real) Elapses(ctx context.Context, job schedule.Job,
	n int) ([]string, error) {
	if !job.Kind.Systemd() {
		return nil, fmt.Errorf(
			"cron publishes no future runs; the schedule and its reading above " +
				"are what there is")
	}
	if r.timers == nil {
		return nil, fmt.Errorf("systemd is not installed on this machine")
	}
	return r.timers.Elapses(ctx, job.Schedule, n)
}

// The five control actions, each of which applies to a timer only.
func (r *Real) BuildEnable(job schedule.Job) (schedule.Command, error) {
	return r.control(job, "enable")
}

func (r *Real) BuildDisable(job schedule.Job) (schedule.Command, error) {
	return r.control(job, "disable")
}

func (r *Real) BuildStart(job schedule.Job) (schedule.Command, error) {
	return r.control(job, "start")
}

func (r *Real) BuildStop(job schedule.Job) (schedule.Command, error) {
	return r.control(job, "stop")
}

// control is the shared guard: the job has to be a timer, and systemd has to be
// here.
func (r *Real) control(job schedule.Job, verb string) (schedule.Command, error) {
	if _, err := r.half(job); err != nil {
		return schedule.Command{}, err
	}
	return timers.BuildControl(job, verb)
}

// BuildRunNow starts the unit a timer would have started.
func (r *Real) BuildRunNow(job schedule.Job) (schedule.Command, error) {
	if _, err := r.half(job); err != nil {
		return schedule.Command{}, err
	}
	if !job.Kind.Systemd() {
		return schedule.Command{}, fmt.Errorf(
			"%s is a cron line: there is no unit to start, and running it now "+
				"means running its command yourself", job.Name)
	}
	return timers.BuildRunNow(job)
}

// BuildSetSchedule changes when a job runs.
func (r *Real) BuildSetSchedule(ctx context.Context, model schedule.Model,
	job schedule.Job, expression string) (schedule.WritePlan, error) {
	systemd, err := r.half(job)
	if err != nil {
		return schedule.WritePlan{}, err
	}
	if systemd {
		return r.setTimerSchedule(ctx, job, expression)
	}
	return r.rewriteCronLine(ctx, model, job, expression, job.Command)
}

// setTimerSchedule writes the drop-in that replaces a timer's OnCalendar.
//
// systemd is asked first, and the answer is on the dialog. `systemd-analyze
// calendar` is the check that matters here: it is the same parser the timer
// will be armed with, it says what the expression normalizes to, and it names
// the next runs — which is the question anybody editing a schedule actually
// has. `systemd-analyze verify` is not used, because it refuses a drop-in
// fragment outright; the pair of whole units a create writes is verified with
// it instead.
func (r *Real) setTimerSchedule(ctx context.Context, job schedule.Job,
	expression string) (schedule.WritePlan, error) {
	if job.Monotonic {
		return schedule.WritePlan{}, fmt.Errorf(
			"%s has no OnCalendar to change: it fires relative to an event, "+
				"and that is a change to the unit file itself", job.Unit)
	}
	content, err := timers.RenderDropIn(expression)
	if err != nil {
		return schedule.WritePlan{}, err
	}
	before := r.timers.DropInContent(job.Unit)
	if before == content {
		return schedule.WritePlan{}, fmt.Errorf("%s already says exactly this",
			timers.DropInPathFor(job.Unit))
	}

	path := timers.DropInPathFor(job.Unit)
	plan := schedule.WritePlan{
		Path:              path,
		Content:           content,
		Diff:              schedule.Diff(path, before, content),
		ValidationCommand: r.timers.CalendarCommand(expression),
		Warning:           timerWarning(job, expression),
	}
	normalized, checkErr := r.timers.CheckCalendar(ctx, expression)
	switch {
	case checkErr != nil:
		return schedule.WritePlan{}, fmt.Errorf("systemd refused %q: %w",
			expression, checkErr)
	case normalized != "":
		plan.Validated = true
		plan.Validation = "systemd reads it as " + normalized
	default:
		plan.Validation = "could not run: systemd-analyze printed no normalized form"
	}

	temp, err := schedule.Stage(timers.DropInName, content)
	if err != nil {
		return schedule.WritePlan{}, err
	}
	plan.TempPath = temp

	makeDir, err := timers.BuildMakeDropInDir(job.Unit)
	if err != nil {
		return schedule.WritePlan{}, err
	}
	installCmd, err := timers.BuildInstall(temp, path)
	if err != nil {
		return schedule.WritePlan{}, err
	}
	restart, err := timers.BuildRestartTimer(job)
	if err != nil {
		return schedule.WritePlan{}, err
	}
	plan.Commands = []schedule.Command{
		makeDir, installCmd, timers.BuildDaemonReload(job), restart,
	}
	return plan, nil
}

// timerWarning is what the confirm dialog must say about this particular change
// beyond the diff.
func timerWarning(job schedule.Job, expression string) string {
	var warnings []string
	if !job.Enabled {
		warnings = append(warnings,
			job.Unit+" is not enabled, so changing its schedule changes when it "+
				"would run rather than when it will. Enable it too, or this is a "+
				"note to your future self.")
	}
	if interval, ok := schedule.CalendarInterval(expression); ok &&
		interval >= 24*time.Hour && job.PersistentKnown && !job.Persistent {
		warnings = append(warnings,
			"This timer has no Persistent=true, so a run missed while the "+
				"machine was off is skipped rather than caught up. At a daily "+
				"schedule that is the difference between a job that runs and one "+
				"that quietly does not.")
	}
	return strings.Join(warnings, "\n\n")
}

// rewriteCronLine replaces one line of a table with a new schedule or command.
func (r *Real) rewriteCronLine(ctx context.Context, model schedule.Model,
	job schedule.Job, expression, command string) (schedule.WritePlan, error) {
	if job.Kind == schedule.KindAnacronDir {
		return schedule.WritePlan{}, fmt.Errorf(
			"%s is a script in %s: the directory is its schedule, so moving the "+
				"file is the change, not editing a line", job.Name, job.File)
	}
	withUser := job.Kind == schedule.KindCronD
	line, err := crontab.RenderLine(expression, job.Owner, command, withUser)
	if err != nil {
		return schedule.WritePlan{}, err
	}
	return r.writeCronTable(ctx, model, job, job.Line, line)
}

// writeCronTable stages a table with one line replaced, added or removed, and
// returns the plan that installs it.
func (r *Real) writeCronTable(ctx context.Context, model schedule.Model,
	job schedule.Job, line int, replacement string) (schedule.WritePlan, error) {
	before, path, err := r.tableFor(ctx, job)
	if err != nil {
		return schedule.WritePlan{}, err
	}
	content, err := crontab.ReplaceLine(before, line, replacement)
	if err != nil {
		return schedule.WritePlan{}, err
	}
	if content == before {
		return schedule.WritePlan{}, fmt.Errorf("%s already says exactly this", path)
	}

	plan := schedule.WritePlan{
		Path:    path,
		Content: content,
		Diff:    schedule.Diff(path, before, content),
		// There is no command on this machine that will check a crontab
		// portably, so the check is the family's own parser and the dialog says
		// so rather than claiming cron approved.
		Validated: true,
		Validation: "checked against tui-cron's own cron parser; cronie's " +
			"`crontab -T` exists but Debian's cron has no equivalent, so the " +
			"check is the same one on every machine",
		ValidationCommand: "",
		Warning:           cronWarning(model, job),
	}

	name := "crontab"
	if job.Kind == schedule.KindCronD {
		name = pathBase(path)
	}
	temp, err := schedule.Stage(name, content)
	if err != nil {
		return schedule.WritePlan{}, err
	}
	plan.TempPath = temp

	var install schedule.Command
	if job.Kind == schedule.KindCronD {
		install, err = crontab.BuildInstallCronD(temp, path)
	} else {
		install, err = crontab.BuildInstallTable(job.Owner, model.User, temp)
	}
	if err != nil {
		return schedule.WritePlan{}, err
	}
	plan.Commands = []schedule.Command{install}
	return plan, nil
}

// tableFor returns the text of the table a job lives in, and its path.
func (r *Real) tableFor(ctx context.Context, job schedule.Job) (text, path string,
	err error) {
	if job.Kind == schedule.KindCrontab {
		text, err = r.cron.ReadTableFor(ctx, job.Owner)
		return text, crontab.TablePathFor(job.Owner), err
	}
	return r.cron.ReadFile(job.File), job.File, nil
}

// cronWarning is what the confirm dialog must say about a cron change.
func cronWarning(model schedule.Model, job schedule.Job) string {
	var warnings []string
	if !model.Cron.Active && model.Cron.Unit != "" {
		warnings = append(warnings,
			model.Cron.Unit+" is "+model.Cron.State+", so nothing in this table "+
				"is running. The line will be correct and it will not fire.")
	}
	if job.Kind == schedule.KindCrontab && job.Owner != model.User {
		warnings = append(warnings,
			"This replaces "+job.Owner+"'s whole crontab, not one line of it. "+
				"The staged file is what that account will have.")
	}
	return strings.Join(warnings, "\n\n")
}

// BuildDelete removes a cron line.
func (r *Real) BuildDelete(ctx context.Context, model schedule.Model,
	job schedule.Job) (schedule.WritePlan, error) {
	if job.Kind.Systemd() {
		return schedule.WritePlan{}, fmt.Errorf(
			"%s is a systemd timer: disabling it stops it, and removing its "+
				"unit file is a job for the package that installed it", job.Unit)
	}
	if _, err := r.half(job); err != nil {
		return schedule.WritePlan{}, err
	}
	if job.Kind == schedule.KindAnacronDir {
		return schedule.WritePlan{}, fmt.Errorf(
			"%s is a script in %s: removing it means removing the file, which "+
				"is not something this tool does", job.Name, job.File)
	}
	return r.writeCronTable(ctx, model, job, job.Line, "")
}

// BuildConvert generates a .timer and .service pair from a cron line.
//
// The pair is written and deliberately not enabled, and the cron line is left
// exactly where it is. Two schedulers running the same job is worse than one
// running it in the old place: the conversion is a draft to read, enable and
// then remove the line behind, and doing any of that for the user would be
// doing the dangerous half first.
func (r *Real) BuildConvert(ctx context.Context, model schedule.Model,
	job schedule.Job) (schedule.WritePlan, error) {
	if r.timers == nil {
		return schedule.WritePlan{}, fmt.Errorf(
			"systemd is not installed on this machine, so there is nothing to " +
				"convert to")
	}
	if !job.Kind.Cron() || job.Kind == schedule.KindAnacronDir {
		return schedule.WritePlan{}, fmt.Errorf(
			"%s is not a cron line with a schedule of its own", job.Name)
	}
	spec, err := SpecFromCron(job)
	if err != nil {
		return schedule.WritePlan{}, err
	}
	plan, err := r.buildUnits(ctx, spec)
	if err != nil {
		return schedule.WritePlan{}, err
	}
	plan.Warning = "The cron line in " + job.Where() + " is left exactly as it " +
		"is, and the new timer is written but NOT enabled — otherwise the job " +
		"would run twice. Read the units, enable the timer, then delete the " +
		"cron line with d.\n\n" + plan.Warning
	_ = model
	return plan, nil
}

// BuildCreate writes a new timer and its service.
func (r *Real) BuildCreate(ctx context.Context, model schedule.Model,
	spec schedule.NewTimer) (schedule.WritePlan, error) {
	if r.timers == nil {
		return schedule.WritePlan{}, fmt.Errorf(
			"systemd is not installed on this machine")
	}
	_ = model
	return r.buildUnits(ctx, spec)
}

// buildUnits stages the .service and .timer pair, has systemd parse both, and
// returns the plan that installs them.
func (r *Real) buildUnits(ctx context.Context,
	spec schedule.NewTimer) (schedule.WritePlan, error) {
	service, timer, err := timers.RenderUnits(spec, timers.Stamp(r.now()))
	if err != nil {
		return schedule.WritePlan{}, err
	}
	name := timers.BaseName(spec.Name)
	timerPath, servicePath := timers.UnitPathsFor(name)

	stagedService, err := schedule.Stage(name+".service", service)
	if err != nil {
		return schedule.WritePlan{}, err
	}
	stagedTimer, err := schedule.Stage(name+".timer", timer)
	if err != nil {
		return schedule.WritePlan{}, err
	}

	plan := schedule.WritePlan{
		Path:     timerPath,
		Content:  timer,
		TempPath: stagedTimer,
		// Both files are new, so the diff is the two units in full. That is
		// exactly what a reader needs here: there is nothing to compare against
		// and everything to check.
		//
		// The timer comes first because it carries the schedule, and the dialog
		// trims a long diff from the bottom: when only half of the pair fits on
		// screen, the half worth reading is the one that says when this runs.
		Diff: schedule.Diff(timerPath, "", timer) +
			schedule.Diff(servicePath, "", service),
		ValidationCommand: r.timers.VerifyCommand(stagedTimer, stagedService),
	}
	out, verifyErr := r.timers.VerifyUnits(ctx, stagedTimer, stagedService)
	switch {
	case verifyErr != nil:
		return schedule.WritePlan{}, fmt.Errorf("systemd refused the units: %w",
			verifyErr)
	default:
		plan.Validated = true
		plan.Validation = "accepted by " + plan.ValidationCommand
		if strings.TrimSpace(out) != "" {
			plan.Validation += " with: " + strings.TrimSpace(out)
		}
	}

	installService, err := timers.BuildInstall(stagedService, servicePath)
	if err != nil {
		return schedule.WritePlan{}, err
	}
	installTimer, err := timers.BuildInstall(stagedTimer, timerPath)
	if err != nil {
		return schedule.WritePlan{}, err
	}
	plan.Commands = []schedule.Command{
		installService, installTimer,
		timers.BuildDaemonReload(schedule.Job{Kind: schedule.KindTimer}),
	}
	if !spec.Persistent {
		plan.Warning = "Without Persistent=true a run missed while the machine " +
			"was off is skipped rather than caught up."
	}
	return plan, nil
}

// pathBase is the file name of a path, used to name the staged copy after the
// file it will become.
func pathBase(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}
