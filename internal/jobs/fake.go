package jobs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tui-tools/tui-cron/internal/crontab"
	"github.com/tui-tools/tui-cron/internal/schedule"
	"github.com/tui-tools/tui-cron/internal/timers"
	"github.com/tui-tools/tui-kit/runner"
)

// demoNow fixes the sample machine's clock, so the times on screen, in the
// README's frames and in a test are the same times every run.
var demoNow = time.Date(2026, 8, 30, 9, 41, 0, 0, time.UTC)

// demoUser is the account the sample machine's crontab belongs to.
const demoUser = "ana"

// Fake is an in-memory machine carrying both schedulers. It backs --demo and
// the tests: every key works, every command is built and previewed exactly as
// the real backend builds it, and nothing reaches the system.
//
// The commands are recorded rather than run, and a hook applies to the
// in-memory model the change the real command would have made — so disabling a
// timer in --demo really does change its row, and the argv the confirm dialog
// displayed is the argv a test can assert on.
type Fake struct {
	model schedule.Model
	run   *runner.Fake
	// staged is the pending file content, keyed by the path it will become.
	// --demo writes no file at all, so the "staging directory" is this map.
	staged map[string]string
	// tables is the sample machine's cron tables, keyed by path.
	tables map[string]string
}

// NewFake builds the sample machine: six timers, one of which failed last
// night and one of which will skip a run the first time the machine is off at
// four in the morning; four cron lines, one of them at boot; and a script in
// /etc/cron.daily that nothing records anything about.
func NewFake() *Fake {
	f := &Fake{staged: map[string]string{}}
	f.run = &runner.Fake{Prefix: "sudo -n", Hook: f.apply}
	f.reset()
	return f
}

// reset builds the sample state. It is a function rather than a literal so
// --demo starts from the same machine every time, however it was left.
func (f *Fake) reset() {
	f.tables = map[string]string{
		crontab.TablePathFor(demoUser): demoUserTable,
		crontab.SystemCrontab:          demoSystemCrontab,
		crontab.CronDDir + "/0hourly":  demoCronD,
	}
	f.model = schedule.Model{
		Backend: "host",
		User:    demoUser,
		Timers: schedule.TimerState{
			Available: true, UserAvailable: true,
		},
		Cron: schedule.CronState{
			Installed: true, Unit: "crond.service", Active: true,
			Enabled: true, State: "active",
		},
	}
	f.rebuild()
}

// rebuild recomputes the job list from the sample timers and the sample cron
// tables, which is what a reload does after a change.
func (f *Fake) rebuild() {
	list := append([]schedule.Job(nil), demoTimers()...)
	for _, path := range []string{
		crontab.TablePathFor(demoUser), crontab.SystemCrontab,
		crontab.CronDDir + "/0hourly",
	} {
		text := f.tables[path]
		if text == "" {
			continue
		}
		if path == crontab.TablePathFor(demoUser) {
			list = append(list, crontab.ParseUserTable(text, demoUser, path)...)
			continue
		}
		list = append(list, crontab.ParseSystemTable(text, path)...)
	}
	list = append(list, crontab.JobFromAnacronDir("/etc/cron.daily", "man-db"))

	// The demo's cron log goes through the same parser a real machine's does,
	// so the outcome column on a cron row is computed rather than written down.
	crontab.ApplyCronLog(list, crontab.ParseCronLog(demoCronLog))
	Sort(list)
	f.model.Jobs = list
}

// demoUserTable is the sample account's crontab, with the environment lines a
// real one carries above the jobs — they are what a rewritten table has to keep.
const demoUserTable = `SHELL=/bin/bash
MAILTO=ana

# Queue watchdog, added when the March incident happened.
*/5 * * * * /usr/local/bin/check-queue
@reboot /usr/local/bin/warm-cache
`

// demoSystemCrontab is /etc/crontab, which carries a user field.
const demoSystemCrontab = `SHELL=/bin/bash
PATH=/sbin:/bin:/usr/sbin:/usr/bin
MAILTO=root

# *  *  *  *  * user-name  command to be executed
30 4 * * 1-5 root /usr/local/bin/report --daily >> /var/log/report.log 2>&1
`

// demoCronD is the file every Fedora machine has, which is what actually runs
// /etc/cron.hourly there.
const demoCronD = `# Run the hourly jobs
SHELL=/bin/bash
PATH=/sbin:/bin:/usr/sbin:/usr/bin
MAILTO=root
01 * * * * root run-parts /etc/cron.hourly
`

// demoCronLog is what cron logged overnight. It is written in cronie's format,
// CMD and CMDEND, because that pairing is the only thing either implementation
// records about a job finishing — and the report job's error line is what a
// failure looks like when cron notices one at all.
const demoCronLog = `2026-08-30T09:35:00+0000 demo CROND[8811]: (ana) CMD (/usr/local/bin/check-queue)
2026-08-30T09:35:01+0000 demo CROND[8811]: (ana) CMDEND (/usr/local/bin/check-queue)
2026-08-30T09:40:00+0000 demo CROND[8907]: (ana) CMD (/usr/local/bin/check-queue)
2026-08-30T09:40:02+0000 demo CROND[8907]: (ana) CMDEND (/usr/local/bin/check-queue)
2026-08-30T09:01:00+0000 demo CROND[8402]: (root) CMD (run-parts /etc/cron.hourly)
2026-08-30T09:01:00+0000 demo CROND[8401]: (root) CMDEND (run-parts /etc/cron.hourly)
2026-08-30T04:30:00+0000 demo CROND[4120]: (root) CMD (/usr/local/bin/report --daily)
`

// demoTimers is the sample machine's six timers, built the way the real backend
// builds one: from the properties `systemctl show` prints, through the same
// parser, so --demo exercises that code rather than a shortcut around it.
func demoTimers() []schedule.Job {
	specs := []struct {
		properties string
		service    string
		kind       schedule.Kind
		owner      string
	}{
		{demoLogrotate, demoLogrotateService, schedule.KindTimer, "root"},
		{demoFstrim, demoFstrimService, schedule.KindTimer, "root"},
		{demoBackup, demoBackupService, schedule.KindTimer, "root"},
		{demoMirror, demoMirrorService, schedule.KindTimer, "root"},
		{demoPlocate, demoPlocateService, schedule.KindTimer, "root"},
		{demoSyncNotes, demoSyncNotesService, schedule.KindUserTimer, demoUser},
	}
	out := make([]schedule.Job, 0, len(specs))
	for _, spec := range specs {
		job := timers.JobFromTimer(timers.ParseProperties(spec.properties),
			spec.kind, spec.owner)
		timers.ApplyService(&job, timers.ParseProperties(spec.service))
		out = append(out, job)
	}
	return out
}

// The sample timers, written as `systemctl show` prints them.
const (
	demoLogrotate = `Unit=logrotate.service
TimersCalendar={ OnCalendar=*-*-* 00:00:00 ; next_elapse=Mon 2026-08-31 00:00:00 UTC }
NextElapseUSecRealtime=Mon 2026-08-31 00:14:26 UTC
LastTriggerUSec=Sun 2026-08-30 00:09:19 UTC
AccuracyUSec=1min
RandomizedDelayUSec=1h
Persistent=yes
Id=logrotate.timer
Description=Daily rotation of log files
LoadState=loaded
ActiveState=active
SubState=waiting
FragmentPath=/usr/lib/systemd/system/logrotate.timer
UnitFileState=enabled
`
	demoLogrotateService = `Result=success
ExecMainStatus=0
ExecMainExitTimestamp=Sun 2026-08-30 00:09:19 UTC
ExecStart={ path=/usr/sbin/logrotate ; argv[]=/usr/sbin/logrotate /etc/logrotate.conf ; ignore_errors=no }
Id=logrotate.service
Description=Rotate log files
LoadState=loaded
ActiveState=inactive
SubState=dead
FragmentPath=/usr/lib/systemd/system/logrotate.service
`

	demoFstrim = `Unit=fstrim.service
TimersCalendar={ OnCalendar=Mon *-*-* 00:00:00 ; next_elapse=Mon 2026-08-31 00:00:00 UTC }
NextElapseUSecRealtime=Mon 2026-08-31 00:52:16 UTC
LastTriggerUSec=Mon 2026-08-24 01:11:03 UTC
AccuracyUSec=1h
RandomizedDelayUSec=6h
Persistent=yes
Id=fstrim.timer
Description=Discard unused filesystem blocks once a week
LoadState=loaded
ActiveState=active
SubState=waiting
FragmentPath=/usr/lib/systemd/system/fstrim.timer
UnitFileState=enabled
`
	demoFstrimService = `Result=success
ExecMainStatus=0
ExecMainExitTimestamp=Mon 2026-08-24 01:11:09 UTC
ExecStart={ path=/usr/sbin/fstrim ; argv[]=/usr/sbin/fstrim --listed-in /etc/fstab:/proc/self/mountinfo --verbose --quiet-unsupported ; ignore_errors=no }
Id=fstrim.service
Description=Discard unused blocks on filesystems from /etc/fstab
LoadState=loaded
ActiveState=inactive
SubState=dead
FragmentPath=/usr/lib/systemd/system/fstrim.service
`

	// The one that failed. Result=exit-code with a non-zero ExecMainStatus is
	// exactly what a backup that could not reach its destination leaves behind,
	// and nothing on the machine says so until somebody looks.
	demoBackup = `Unit=backup.service
TimersCalendar={ OnCalendar=*-*-* 02:30:00 ; next_elapse=Mon 2026-08-31 02:30:00 UTC }
NextElapseUSecRealtime=Mon 2026-08-31 02:30:00 UTC
LastTriggerUSec=Sun 2026-08-30 02:30:00 UTC
AccuracyUSec=1min
RandomizedDelayUSec=0
Persistent=yes
Id=backup.timer
Description=Nightly offsite backup
LoadState=loaded
ActiveState=active
SubState=waiting
FragmentPath=/etc/systemd/system/backup.timer
UnitFileState=enabled
`
	demoBackupService = `Result=exit-code
ExecMainStatus=2
ExecMainExitTimestamp=Sun 2026-08-30 02:31:44 UTC
ExecStart={ path=/usr/local/bin/backup ; argv[]=/usr/local/bin/backup --offsite ; ignore_errors=no }
Id=backup.service
Description=Nightly offsite backup
LoadState=loaded
ActiveState=failed
SubState=failed
FragmentPath=/etc/systemd/system/backup.service
`

	// The one with no Persistent. It is daily, so the first morning the machine
	// is off at four it silently does not run — which is what the check warns
	// about and what the header counts.
	demoMirror = `Unit=mirror-sync.service
TimersCalendar={ OnCalendar=*-*-* 04:00:00 ; next_elapse=Mon 2026-08-31 04:00:00 UTC }
NextElapseUSecRealtime=Mon 2026-08-31 04:00:00 UTC
LastTriggerUSec=Sun 2026-08-30 04:00:00 UTC
AccuracyUSec=1min
RandomizedDelayUSec=0
Persistent=no
Id=mirror-sync.timer
Description=Refresh the package mirror
LoadState=loaded
ActiveState=active
SubState=waiting
FragmentPath=/etc/systemd/system/mirror-sync.timer
UnitFileState=enabled
`
	demoMirrorService = `Result=success
ExecMainStatus=0
ExecMainExitTimestamp=Sun 2026-08-30 04:06:31 UTC
ExecStart={ path=/usr/local/bin/mirror-sync ; argv[]=/usr/local/bin/mirror-sync ; ignore_errors=no }
Id=mirror-sync.service
Description=Refresh the package mirror
LoadState=loaded
ActiveState=inactive
SubState=dead
FragmentPath=/etc/systemd/system/mirror-sync.service
`

	// Installed, enabled and never fired: a timer whose service reports
	// Result=success purely because it has never been asked to do anything.
	demoPlocate = `Unit=plocate-updatedb.service
TimersCalendar={ OnCalendar=*-*-* 00:00:00 ; next_elapse=Mon 2026-08-31 00:00:00 UTC }
NextElapseUSecRealtime=Mon 2026-08-31 00:22:09 UTC
LastTriggerUSec=n/a
AccuracyUSec=1min
RandomizedDelayUSec=6h
Persistent=yes
Id=plocate-updatedb.timer
Description=Update the plocate database daily
LoadState=loaded
ActiveState=active
SubState=waiting
FragmentPath=/usr/lib/systemd/system/plocate-updatedb.timer
UnitFileState=enabled
`
	demoPlocateService = `Result=success
ExecMainStatus=
ExecStart={ path=/usr/bin/updatedb ; argv[]=/usr/bin/updatedb ; ignore_errors=no }
Id=plocate-updatedb.service
Description=Update the plocate database
LoadState=loaded
ActiveState=inactive
SubState=dead
FragmentPath=/usr/lib/systemd/system/plocate-updatedb.service
`

	// A timer in the account's own manager rather than the system one, which is
	// where a person's own scheduled work belongs and where nobody looks.
	demoSyncNotes = `Unit=sync-notes.service
TimersCalendar={ OnCalendar=*-*-* *:00/10:00 ; next_elapse=Sun 2026-08-30 09:50:00 UTC }
NextElapseUSecRealtime=Sun 2026-08-30 09:50:00 UTC
LastTriggerUSec=Sun 2026-08-30 09:40:00 UTC
AccuracyUSec=1min
RandomizedDelayUSec=0
Persistent=no
Id=sync-notes.timer
Description=Sync the notes directory
LoadState=loaded
ActiveState=active
SubState=waiting
FragmentPath=/home/ana/.config/systemd/user/sync-notes.timer
UnitFileState=enabled
`
	demoSyncNotesService = `Result=success
ExecMainStatus=0
ExecMainExitTimestamp=Sun 2026-08-30 09:40:04 UTC
ExecStart={ path=/usr/local/bin/sync-notes ; argv[]=/usr/local/bin/sync-notes ; ignore_errors=no }
Id=sync-notes.service
Description=Sync the notes directory
LoadState=loaded
ActiveState=inactive
SubState=dead
FragmentPath=/home/ana/.config/systemd/user/sync-notes.service
`
)

// Name identifies the backend. It is the real backend's name, because --demo
// shows what the real one would show.
func (f *Fake) Name() string { return "host" }

// Describe says plainly that nothing here is real.
func (f *Fake) Describe() string { return "demo (in-memory sample machine)" }

// Capabilities reports the same capabilities as a machine with both schedulers.
func (f *Fake) Capabilities() schedule.Capabilities {
	return schedule.Capabilities{
		SupportsTimerControl: true,
		SupportsTimerEdit:    true,
		SupportsTimerCreate:  true,
		SupportsCronEdit:     true,
		SupportsConvert:      true,
		DropInFor:            timers.DropInPathFor,
		UnitDir:              timers.UnitDir,
	}
}

// Preview renders the command line the real backend would run.
func (f *Fake) Preview(cmd schedule.Command) string { return f.run.Preview(cmd) }

// Load returns the sample machine.
func (f *Fake) Load(context.Context) (schedule.Model, error) { return f.model, nil }

// Run records the command and applies its effect to the sample machine.
func (f *Fake) Run(ctx context.Context, cmd schedule.Command) (string, error) {
	return f.run.Run(ctx, cmd)
}

// Ran exposes the recorded commands, which is what a test asserts on.
func (f *Fake) Ran() []schedule.Command { return f.run.Ran }

// apply is the hook the fake runner calls: it makes to the in-memory machine
// the change the real command would have made, so the demo stays coherent as
// keys are pressed.
func (f *Fake) apply(cmd schedule.Command) (string, error) {
	argv := cmd.Argv
	if len(argv) < 2 {
		return "", nil
	}
	switch argv[0] {
	case "systemctl":
		f.applySystemctl(argv)
	case "crontab":
		f.applyCrontab(argv)
	case "install":
		f.applyInstall(argv)
	}
	return "", nil
}

// applySystemctl mirrors a control verb onto the sample machine.
func (f *Fake) applySystemctl(argv []string) {
	rest := argv[1:]
	if len(rest) > 0 && rest[0] == "--user" {
		rest = rest[1:]
	}
	if len(rest) < 2 {
		return
	}
	verb, unit := rest[0], rest[1]
	for i := range f.model.Jobs {
		job := &f.model.Jobs[i]
		if job.Unit != unit && job.Service != unit {
			continue
		}
		switch verb {
		case "enable":
			job.Enabled = true
		case "disable":
			job.Enabled = false
		case "start":
			if job.Unit == unit {
				job.Active, job.State = true, "active"
				continue
			}
			// Starting the service is "run now": the job ran, and on the sample
			// machine it worked.
			job.Last = demoNow
			job.Outcome, job.OutcomeDetail =
				schedule.OutcomeOK, "the last run exited 0"
		case "stop":
			if job.Unit == unit {
				job.Active, job.State = false, "inactive"
				job.Next, job.NextNote = time.Time{},
					"not armed: the timer unit is inactive"
			}
		}
	}
}

// applyCrontab replaces a table on the sample machine, the way `crontab <file>`
// replaces a real one.
func (f *Fake) applyCrontab(argv []string) {
	owner := demoUser
	rest := argv[1:]
	if len(rest) > 1 && rest[0] == "-u" {
		owner, rest = rest[1], rest[2:]
	}
	if len(rest) != 1 {
		return
	}
	path := crontab.TablePathFor(owner)
	if content, ok := f.staged[path]; ok {
		f.tables[path] = content
		f.rebuild()
	}
}

// applyInstall applies a staged file to the sample machine: a cron table, or a
// unit that the demo records without pretending systemd loaded it.
func (f *Fake) applyInstall(argv []string) {
	destination := argv[len(argv)-1]
	content, ok := f.staged[destination]
	if !ok {
		return
	}
	if destination == crontab.SystemCrontab || crontab.UnderCronD(destination) {
		f.tables[destination] = content
		f.rebuild()
		return
	}
	// A drop-in: the sample timer picks up the new schedule the way a real one
	// does after the daemon-reload and the restart that follow.
	for i := range f.model.Jobs {
		job := &f.model.Jobs[i]
		if job.Unit == "" || destination != timers.DropInPathFor(job.Unit) {
			continue
		}
		for _, line := range strings.Split(content, "\n") {
			if expression, found := strings.CutPrefix(line, "OnCalendar="); found &&
				strings.TrimSpace(expression) != "" {
				job.Schedule = strings.TrimSpace(expression)
				job.Explain = schedule.DescribeCalendar(job.Schedule)
			}
		}
	}
}

// Definition returns the sample job's own text.
func (f *Fake) Definition(_ context.Context, job schedule.Job) (string, error) {
	if job.Kind.Systemd() {
		return "# " + job.File + "\n[Unit]\nDescription=" + job.Description +
			"\n\n[Timer]\nOnCalendar=" + job.Schedule + "\n" +
			persistentLine(job) + "Unit=" + job.Service +
			"\n\n[Install]\nWantedBy=timers.target\n", nil
	}
	if text, ok := f.tables[job.File]; ok {
		return "# " + job.File + "\n" + text, nil
	}
	return "# " + job.File + "\n" + job.Raw + "\n", nil
}

// persistentLine renders the setting the demo's unit text shows for it.
func persistentLine(job schedule.Job) string {
	if !job.PersistentKnown {
		return ""
	}
	if job.Persistent {
		return "Persistent=true\n"
	}
	return "Persistent=false\n"
}

// Journal returns the sample machine's log for a job.
func (f *Fake) Journal(_ context.Context, job schedule.Job,
	lines int) (string, error) {
	if job.Kind.Cron() {
		return crontab.FilterLog(demoCronLog, job, lines), nil
	}
	if job.Failed() {
		return "2026-08-30T02:30:00+0000 demo backup[4411]: starting offsite backup\n" +
			"2026-08-30T02:31:44+0000 demo backup[4411]: ssh: connect to host " +
			"backup.example port 22: Connection timed out\n" +
			"2026-08-30T02:31:44+0000 demo systemd[1]: backup.service: Main " +
			"process exited, code=exited, status=2/INVALIDARGUMENT\n" +
			"2026-08-30T02:31:44+0000 demo systemd[1]: backup.service: Failed " +
			"with result 'exit-code'.\n", nil
	}
	return "2026-08-30T00:09:19+0000 demo systemd[1]: Starting " + job.Service +
		"…\n2026-08-30T00:09:19+0000 demo systemd[1]: " + job.Service +
		": Deactivated successfully.\n", nil
}

// Elapses returns the next runs of a sample timer's schedule.
//
// The real backend asks systemd; the sample machine has no systemd to ask, so
// the answer is written down — and only for the two shapes the demo carries, so
// the demo never claims to have computed something it did not.
func (f *Fake) Elapses(_ context.Context, job schedule.Job,
	n int) ([]string, error) {
	if !job.Kind.Systemd() {
		return nil, fmt.Errorf(
			"cron publishes no future runs; the schedule and its reading above " +
				"are what there is")
	}
	interval, ok := schedule.CalendarInterval(job.Schedule)
	if !ok || interval <= 0 {
		return nil, fmt.Errorf("the sample machine has no systemd to ask")
	}
	next := job.Next
	if next.IsZero() {
		next = demoNow.Add(interval)
	}
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, next.Add(time.Duration(i)*interval).
			Format("Mon 2006-01-02 15:04:05 UTC"))
	}
	return out, nil
}

// The control actions, built by exactly the same builders the real backend
// uses.
func (f *Fake) BuildEnable(job schedule.Job) (schedule.Command, error) {
	return timers.BuildControl(job, "enable")
}

func (f *Fake) BuildDisable(job schedule.Job) (schedule.Command, error) {
	return timers.BuildControl(job, "disable")
}

func (f *Fake) BuildStart(job schedule.Job) (schedule.Command, error) {
	return timers.BuildControl(job, "start")
}

func (f *Fake) BuildStop(job schedule.Job) (schedule.Command, error) {
	return timers.BuildControl(job, "stop")
}

func (f *Fake) BuildRunNow(job schedule.Job) (schedule.Command, error) {
	if !job.Kind.Systemd() {
		return schedule.Command{}, fmt.Errorf(
			"%s is a cron line: there is no unit to start, and running it now "+
				"means running its command yourself", job.Name)
	}
	return timers.BuildRunNow(job)
}

// BuildSetSchedule stages the change in memory and returns the same plan the
// real backend returns — the same diff, and the same commands.
func (f *Fake) BuildSetSchedule(_ context.Context, model schedule.Model,
	job schedule.Job, expression string) (schedule.WritePlan, error) {
	if job.Kind.Systemd() {
		return f.setTimerSchedule(job, expression)
	}
	return f.rewriteCronLine(model, job, expression)
}

// setTimerSchedule stages the drop-in for a sample timer.
func (f *Fake) setTimerSchedule(job schedule.Job,
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
	path := timers.DropInPathFor(job.Unit)
	before := f.staged[path]
	if before == content {
		return schedule.WritePlan{}, fmt.Errorf("%s already says exactly this", path)
	}
	// The sample machine has no systemd to ask, so the expression is checked
	// against the family's own parser and the dialog reports that check under
	// the command line the real backend would have run.
	if schedule.DescribeCalendar(expression) == "" {
		return schedule.WritePlan{}, fmt.Errorf(
			"%q is not an OnCalendar expression this tool can read", expression)
	}

	temp := "/tmp/tui-cron/" + timers.DropInName
	f.staged[path] = content

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
	calendar, err := timers.BuildCalendar(expression, 1)
	if err != nil {
		return schedule.WritePlan{}, err
	}
	return schedule.WritePlan{
		Path:              path,
		Content:           content,
		Diff:              schedule.Diff(path, before, content),
		TempPath:          temp,
		Validated:         true,
		Validation:        "systemd reads it as " + expression,
		ValidationCommand: f.Preview(calendar),
		Warning:           timerWarning(job, expression),
		Commands: []schedule.Command{
			makeDir, installCmd, timers.BuildDaemonReload(job), restart,
		},
	}, nil
}

// rewriteCronLine stages a rewritten table for the sample machine.
func (f *Fake) rewriteCronLine(model schedule.Model, job schedule.Job,
	expression string) (schedule.WritePlan, error) {
	if job.Kind == schedule.KindAnacronDir {
		return schedule.WritePlan{}, fmt.Errorf(
			"%s is a script in %s: the directory is its schedule, so moving the "+
				"file is the change, not editing a line", job.Name, job.File)
	}
	withUser := job.Kind == schedule.KindCronD
	line, err := crontab.RenderLine(expression, job.Owner, job.Command, withUser)
	if err != nil {
		return schedule.WritePlan{}, err
	}
	return f.writeCronTable(model, job, job.Line, line)
}

// writeCronTable stages a table with one line replaced, added or removed.
//
// Which table that is comes from the job the same way the real backend works it
// out: a user's crontab is named by its owner, because a line being *added* has
// no file of its own yet.
func (f *Fake) writeCronTable(model schedule.Model, job schedule.Job,
	line int, replacement string) (schedule.WritePlan, error) {
	path := job.File
	if job.Kind == schedule.KindCrontab {
		path = crontab.TablePathFor(job.Owner)
	}
	before := f.tables[path]
	content, err := crontab.ReplaceLine(before, line, replacement)
	if err != nil {
		return schedule.WritePlan{}, err
	}
	if content == before {
		return schedule.WritePlan{}, fmt.Errorf("%s already says exactly this", path)
	}
	f.staged[path] = content

	temp := "/tmp/tui-cron/" + pathBase(path)
	var install schedule.Command
	if job.Kind == schedule.KindCronD {
		install, err = crontab.BuildInstallCronD(temp, path)
	} else {
		install, err = crontab.BuildInstallTable(job.Owner, model.User, temp)
	}
	if err != nil {
		return schedule.WritePlan{}, err
	}
	return schedule.WritePlan{
		Path:      path,
		Content:   content,
		Diff:      schedule.Diff(path, before, content),
		TempPath:  temp,
		Validated: true,
		Validation: "checked against tui-cron's own cron parser; cronie's " +
			"`crontab -T` exists but Debian's cron has no equivalent, so the " +
			"check is the same one on every machine",
		Warning:  cronWarning(model, job),
		Commands: []schedule.Command{install},
	}, nil
}

// BuildDelete removes a cron line from the sample machine.
func (f *Fake) BuildDelete(_ context.Context, model schedule.Model,
	job schedule.Job) (schedule.WritePlan, error) {
	if job.Kind.Systemd() {
		return schedule.WritePlan{}, fmt.Errorf(
			"%s is a systemd timer: disabling it stops it, and removing its "+
				"unit file is a job for the package that installed it", job.Unit)
	}
	if job.Kind == schedule.KindAnacronDir {
		return schedule.WritePlan{}, fmt.Errorf(
			"%s is a script in %s: removing it means removing the file, which "+
				"is not something this tool does", job.Name, job.File)
	}
	return f.writeCronTable(model, job, job.Line, "")
}

// BuildConvert generates a timer from a sample cron line.
func (f *Fake) BuildConvert(ctx context.Context, model schedule.Model,
	job schedule.Job) (schedule.WritePlan, error) {
	if !job.Kind.Cron() || job.Kind == schedule.KindAnacronDir {
		return schedule.WritePlan{}, fmt.Errorf(
			"%s is not a cron line with a schedule of its own", job.Name)
	}
	spec, err := SpecFromCron(job)
	if err != nil {
		return schedule.WritePlan{}, err
	}
	plan, err := f.BuildCreate(ctx, model, spec)
	if err != nil {
		return schedule.WritePlan{}, err
	}
	plan.Warning = "The cron line in " + job.Where() + " is left exactly as it " +
		"is, and the new timer is written but NOT enabled — otherwise the job " +
		"would run twice. Read the units, enable the timer, then delete the " +
		"cron line with d.\n\n" + plan.Warning
	return plan, nil
}

// BuildCreate writes a new timer on the sample machine.
func (f *Fake) BuildCreate(_ context.Context, _ schedule.Model,
	spec schedule.NewTimer) (schedule.WritePlan, error) {
	service, timer, err := timers.RenderUnits(spec, timers.Stamp(demoNow))
	if err != nil {
		return schedule.WritePlan{}, err
	}
	name := timers.BaseName(spec.Name)
	timerPath, servicePath := timers.UnitPathsFor(name)
	stagedTimer := "/tmp/tui-cron/" + name + ".timer"
	stagedService := "/tmp/tui-cron/" + name + ".service"

	installService, err := timers.BuildInstall(stagedService, servicePath)
	if err != nil {
		return schedule.WritePlan{}, err
	}
	installTimer, err := timers.BuildInstall(stagedTimer, timerPath)
	if err != nil {
		return schedule.WritePlan{}, err
	}
	verify, err := timers.BuildVerify(stagedTimer, stagedService)
	if err != nil {
		return schedule.WritePlan{}, err
	}
	plan := schedule.WritePlan{
		Path:     timerPath,
		Content:  timer,
		TempPath: stagedTimer,
		Diff: schedule.Diff(timerPath, "", timer) +
			schedule.Diff(servicePath, "", service),
		Validated:         true,
		Validation:        "accepted by " + f.Preview(verify),
		ValidationCommand: f.Preview(verify),
		Commands: []schedule.Command{
			installService, installTimer,
			timers.BuildDaemonReload(schedule.Job{Kind: schedule.KindTimer}),
		},
	}
	if !spec.Persistent {
		plan.Warning = "Without Persistent=true a run missed while the machine " +
			"was off is skipped rather than caught up."
	}
	return plan, nil
}
