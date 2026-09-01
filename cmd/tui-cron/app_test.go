package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-cron/internal/jobs"
	"github.com/tui-tools/tui-cron/internal/schedule"
	"github.com/tui-tools/tui-kit/theme"
)

// newTestApp builds an app on the sample machine, sized like a normal terminal
// and already loaded.
func newTestApp(t *testing.T) (*app, *jobs.Fake) {
	t.Helper()
	backend := jobs.NewFake()
	a := newApp(backend, theme.New(), nil)
	a.width, a.height = 110, 34
	drain(t, a, a.Init())
	return a, backend
}

// drain runs a tea.Cmd and feeds its message back into the model, which is what
// the Bubble Tea runtime does. It is how a test exercises a load.
func drain(t *testing.T, a *app, cmd tea.Cmd) {
	t.Helper()
	for range 4 {
		if cmd == nil {
			return
		}
		msg := cmd()
		if msg == nil {
			return
		}
		_, cmd = a.Update(msg)
	}
}

// press sends one key and returns the command it produced.
func press(a *app, key string) tea.Cmd {
	var msg tea.KeyMsg
	switch key {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		msg = tea.KeyMsg{Type: tea.KeyTab}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	_, cmd := a.Update(msg)
	return cmd
}

// gotoScreen moves to a tab by its number key.
func gotoScreen(t *testing.T, a *app, s screen) {
	t.Helper()
	drain(t, a, press(a, string(rune('1'+int(s))))) //nolint:gosec // s is one of a handful of screens, so the sum is always a digit
	if a.screen != s {
		t.Fatalf("did not reach the %s screen", s.title())
	}
}

// selectJob moves the cursor to a job by name on the screen it belongs to.
func selectJob(t *testing.T, a *app, s screen, name string) schedule.Job {
	t.Helper()
	gotoScreen(t, a, s)
	for i, job := range a.rows[s] {
		if job.Name == name {
			a.cursor[s] = i
			return job
		}
	}
	t.Fatalf("no job named %q on the %s screen", name, s.title())
	return schedule.Job{}
}

// TestLoadsTheSampleMachine covers what the README describes: six timers, four
// cron lines, one script in /etc/cron.daily, and among them one job that failed
// last night and one daily timer that will skip a run.
func TestLoadsTheSampleMachine(t *testing.T) {
	a, _ := newTestApp(t)

	counts := a.model.Counts()
	if got := counts[schedule.KindTimer] + counts[schedule.KindUserTimer]; got != 6 {
		t.Errorf("read %d timers, want 6", got)
	}
	if got := counts[schedule.KindUserTimer]; got != 1 {
		t.Errorf("read %d user timers, want 1", got)
	}
	if got := counts[schedule.KindCrontab] + counts[schedule.KindCronD]; got != 4 {
		t.Errorf("read %d cron lines, want 4", got)
	}
	if got := counts[schedule.KindAnacronDir]; got != 1 {
		t.Errorf("read %d run-parts scripts, want 1", got)
	}
	if got := len(a.model.Failed()); got != 1 {
		t.Errorf("%d jobs failed, want the one backup", got)
	}
	if got := len(a.model.NeedPersistent()); got != 1 {
		t.Errorf("%d timers want Persistent, want the one mirror-sync", got)
	}

	// The failing job sorts first, because it is why anyone opened the tool.
	if a.rows[screenAll][0].Name != "backup.timer" {
		t.Errorf("first row = %q, want the failure", a.rows[screenAll][0].Name)
	}
	// And the sample machine carries an @reboot line, which has no clock in it
	// and must survive as itself.
	if !strings.Contains(a.View(), "backup.timer") {
		t.Errorf("the list is missing from the first frame")
	}
}

// TestEveryCronLineIsRead: the sample crontab has environment lines, a comment
// and a commented-out job above its three real ones, and none of those is a job.
func TestTheSampleCronLinesAreWhatTheTablesSay(t *testing.T) {
	a, _ := newTestApp(t)
	gotoScreen(t, a, screenCron)

	var reboot bool
	for _, job := range a.rows[screenCron] {
		if job.Schedule == "@reboot" {
			reboot = true
		}
		if strings.HasPrefix(job.Schedule, "MAILTO") ||
			strings.HasPrefix(job.Schedule, "SHELL") {
			t.Errorf("an environment line was listed as a job: %+v", job)
		}
	}
	if !reboot {
		t.Errorf("the @reboot line is missing from the cron screen")
	}
}

// TestActionsPreviewExactlyWhatTheyRun is the family's central promise, as a
// test: for every action key, the command line in the confirm dialog is the
// command line the backend is then asked to run.
func TestActionsPreviewExactlyWhatTheyRun(t *testing.T) {
	tests := []struct {
		name string
		key  string
		job  string
		on   screen
		want string
	}{
		{"enable a timer", "E", "backup.timer", screenTimers,
			"sudo -n systemctl enable backup.timer"},
		{"disable a timer", "D", "backup.timer", screenTimers,
			"sudo -n systemctl disable backup.timer"},
		{"arm a timer", "s", "backup.timer", screenTimers,
			"sudo -n systemctl start backup.timer"},
		{"disarm a timer", "x", "backup.timer", screenTimers,
			"sudo -n systemctl stop backup.timer"},
		// Run now starts the *service*: arming the timer would not run
		// anything.
		{"run a timer now", "n", "backup.timer", screenTimers,
			"sudo -n systemctl start backup.service"},
		// A user timer's commands carry --user, and it goes before the verb.
		{"enable a user timer", "E", "sync-notes.timer", screenTimers,
			"sudo -n systemctl --user enable sync-notes.timer"},
	}
	for _, test := range tests {
		a, backend := newTestApp(t)
		selectJob(t, a, test.on, test.job)

		drain(t, a, press(a, test.key))
		if a.mode != modeConfirm {
			t.Fatalf("%s: no confirm dialog opened (status: %s)", test.name, a.status)
		}
		if a.confirm.Command != test.want {
			t.Errorf("%s: previewed %q, want %q", test.name, a.confirm.Command, test.want)
		}

		drain(t, a, press(a, "y"))
		ran := backend.Ran()
		if len(ran) != 1 {
			t.Fatalf("%s: ran %d commands, want 1", test.name, len(ran))
		}
		if got := backend.Preview(ran[0]); got != test.want {
			t.Errorf("%s: ran %q, want the previewed %q", test.name, got, test.want)
		}
	}
}

func TestCancellingRunsNothing(t *testing.T) {
	a, backend := newTestApp(t)
	selectJob(t, a, screenTimers, "backup.timer")
	drain(t, a, press(a, "E"))
	drain(t, a, press(a, "n"))

	if len(backend.Ran()) != 0 {
		t.Errorf("answering no ran %d commands", len(backend.Ran()))
	}
	if a.status != "cancelled" {
		t.Errorf("status = %q, want cancelled", a.status)
	}
}

// TestEditingATimerWritesADropInAndRestartsIt covers the action the systemd
// half is built around: the drop-in clears the old schedule, systemd checks the
// expression, and the four commands that apply it are all on screen before any
// of them runs.
func TestEditingATimerWritesADropInAndRestartsIt(t *testing.T) {
	a, backend := newTestApp(t)
	selectJob(t, a, screenTimers, "backup.timer")

	drain(t, a, press(a, "e"))
	if a.mode != modeForm {
		t.Fatalf("e did not open the editor (status: %s)", a.status)
	}
	a.form.fields[0].input.SetValue("*-*-* 03:15:00")
	a.form.reread()
	// The reading under the field is the whole reason the dialog exists.
	if a.form.reading != "Every day at 03:15" {
		t.Errorf("the form does not read the schedule back: %q (%s)",
			a.form.reading, a.form.readingErr)
	}
	drain(t, a, press(a, "enter"))

	if a.mode != modeConfirm {
		t.Fatalf("the form did not open a confirm dialog (status: %s)", a.status)
	}
	// The diff has to show the empty OnCalendar= that clears the old schedule,
	// because that line is the difference between replacing a schedule and
	// adding a second one.
	if !strings.Contains(a.confirm.Body, "+OnCalendar=\n") ||
		!strings.Contains(a.confirm.Body, "+OnCalendar=*-*-* 03:15:00") {
		t.Errorf("the confirm dialog does not show the change:\n%s", a.confirm.Body)
	}
	if !strings.Contains(a.confirm.Body, "systemd-analyze calendar") {
		t.Errorf("the dialog does not report the check:\n%s", a.confirm.Body)
	}

	lines := strings.Split(a.confirm.Command, "\n")
	if len(lines) != 4 {
		t.Fatalf("previewed %d command lines, want 4:\n%s",
			len(lines), a.confirm.Command)
	}
	for i, want := range []string{
		"install -d -m 755 /etc/systemd/system/backup.timer.d",
		"install -m 644 /tmp/tui-cron/90-tui-cron.conf " +
			"/etc/systemd/system/backup.timer.d/90-tui-cron.conf",
		"systemctl daemon-reload",
		"systemctl restart backup.timer",
	} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("command %d = %q, want it to contain %q", i, lines[i], want)
		}
	}

	drain(t, a, press(a, "y"))
	if got := len(backend.Ran()); got != 4 {
		t.Fatalf("ran %d commands, want 4", got)
	}
	// And the change is what the machine now reports.
	for _, job := range a.model.Jobs {
		if job.Unit == "backup.timer" && job.Schedule != "*-*-* 03:15:00" {
			t.Errorf("after the write the timer still says %q", job.Schedule)
		}
	}
}

// TestTheUnitFileIsNeverRewritten is the rule that keeps a packaged timer safe:
// a schedule change writes one drop-in, and no key sequence produces a command
// that touches the unit file itself.
func TestTheUnitFileIsNeverRewritten(t *testing.T) {
	a, backend := newTestApp(t)
	selectJob(t, a, screenTimers, "logrotate.timer")
	drain(t, a, press(a, "e"))
	a.form.fields[0].input.SetValue("*-*-* 05:00:00")
	drain(t, a, press(a, "enter"))
	drain(t, a, press(a, "y"))

	for _, cmd := range backend.Ran() {
		line := cmd.String()
		if strings.Contains(line, "/usr/lib/systemd/system") {
			t.Errorf("a command touched a packaged unit file: %q", line)
		}
		if strings.Contains(line, "/etc/systemd/system/logrotate.timer ") ||
			strings.HasSuffix(line, "/etc/systemd/system/logrotate.timer") {
			t.Errorf("a command wrote the unit file itself: %q", line)
		}
	}
}

// TestEditingACronLineReplacesTheWholeTable: `crontab <file>` is cron's own
// interface and it takes a whole table, so the diff has to show that the rest
// of the file survived.
func TestEditingACronLineReplacesTheWholeTable(t *testing.T) {
	a, backend := newTestApp(t)
	job := selectJob(t, a, screenCron, "ana · check-queue")
	if job.Schedule != "*/5 * * * *" {
		t.Fatalf("the sample line is %q", job.Schedule)
	}

	drain(t, a, press(a, "e"))
	if a.mode != modeForm {
		t.Fatalf("e did not open the editor (status: %s)", a.status)
	}
	a.form.fields[0].input.SetValue("*/10 * * * *")
	a.form.reread()
	if a.form.reading != "Every 10 minutes" {
		t.Errorf("the form reads the cron line as %q", a.form.reading)
	}
	drain(t, a, press(a, "enter"))
	if a.mode != modeConfirm {
		t.Fatalf("the form did not open a confirm dialog (status: %s)", a.status)
	}
	if !strings.Contains(a.confirm.Body, "+*/10 * * * * /usr/local/bin/check-queue") {
		t.Errorf("the diff does not show the new line:\n%s", a.confirm.Body)
	}
	if a.confirm.Command != "sudo -n crontab /tmp/tui-cron/ana" {
		t.Errorf("previewed %q", a.confirm.Command)
	}

	drain(t, a, press(a, "y"))
	if len(backend.Ran()) != 1 {
		t.Fatalf("ran %d commands, want the one crontab", len(backend.Ran()))
	}
	// The environment lines above the jobs are load-bearing and must survive.
	for _, job := range a.model.Jobs {
		if strings.HasPrefix(job.Schedule, "MAILTO") {
			t.Errorf("an environment line became a job: %+v", job)
		}
	}
	var found bool
	for _, job := range a.model.Jobs {
		if job.Schedule == "*/10 * * * *" {
			found = true
		}
	}
	if !found {
		t.Errorf("the rewritten table does not carry the new schedule")
	}
}

// TestDeletingACronLineKeepsTheRest.
func TestDeletingACronLineKeepsTheRest(t *testing.T) {
	a, backend := newTestApp(t)
	selectJob(t, a, screenCron, "ana · check-queue")

	drain(t, a, press(a, "d"))
	if a.mode != modeConfirm {
		t.Fatalf("d did not open a confirm dialog (status: %s)", a.status)
	}
	if !strings.Contains(a.confirm.Body, "-*/5 * * * * /usr/local/bin/check-queue") {
		t.Errorf("the diff does not show the line going:\n%s", a.confirm.Body)
	}
	drain(t, a, press(a, "y"))

	for _, job := range a.model.Jobs {
		if job.Command == "/usr/local/bin/check-queue" {
			t.Errorf("the line survived the delete")
		}
	}
	var warm bool
	for _, job := range a.model.Jobs {
		if job.Schedule == "@reboot" {
			warm = true
		}
	}
	if !warm {
		t.Errorf("deleting one line took the @reboot line with it")
	}
	_ = backend
}

// TestDeletingATimerIsRefusedWithAReason: a unit file belongs to whatever
// installed it, and "disable" is what stopping a timer means.
func TestDeletingATimerIsRefusedWithAReason(t *testing.T) {
	a, backend := newTestApp(t)
	selectJob(t, a, screenTimers, "backup.timer")
	drain(t, a, press(a, "d"))

	if a.mode == modeConfirm {
		t.Fatalf("a dialog opened for deleting a unit file")
	}
	if !strings.Contains(a.status, "disabling") {
		t.Errorf("status = %q, want it to name what to do instead", a.status)
	}
	if len(backend.Ran()) != 0 {
		t.Errorf("a command ran anyway")
	}
}

// TestConvertingACronLineWritesAnUnenabledTimer is the conversion's whole
// safety property: two schedulers running the same job is worse than one
// running it in the old place, so the units are written and nothing is enabled
// and the cron line is left alone.
func TestConvertingACronLineWritesAnUnenabledTimer(t *testing.T) {
	a, backend := newTestApp(t)
	selectJob(t, a, screenCron, "ana · check-queue")

	drain(t, a, press(a, "t"))
	if a.mode != modeConfirm {
		t.Fatalf("t did not open a confirm dialog (status: %s)", a.status)
	}
	if !strings.Contains(a.confirm.Body, "NOT enabled") {
		t.Errorf("the dialog does not say the timer is left disabled:\n%s",
			a.confirm.Body)
	}
	// The generated OnCalendar has to mean what the cron line meant.
	if !strings.Contains(a.confirm.Body, "OnCalendar=*-*-* *:00/5:00") {
		t.Errorf("the generated timer does not carry the converted schedule:\n%s",
			a.confirm.Body)
	}
	// Two whole units is more diff than a dialog can hold, so it is trimmed
	// from the bottom and says how much was left out. What must survive the
	// trim is the timer, because that is the half carrying the schedule.
	if !strings.Contains(a.confirm.Body, "more diff lines") {
		t.Errorf("a diff longer than the dialog was not trimmed:\n%s", a.confirm.Body)
	}
	if strings.Index(a.confirm.Body, ".timer") >
		strings.Index(a.confirm.Body, ".service") {
		t.Errorf("the service is diffed above the timer:\n%s", a.confirm.Body)
	}

	drain(t, a, press(a, "y"))
	for _, cmd := range backend.Ran() {
		line := cmd.String()
		if strings.Contains(line, "enable") {
			t.Errorf("the conversion enabled the timer: %q", line)
		}
		if strings.HasPrefix(line, "crontab") {
			t.Errorf("the conversion touched the crontab: %q", line)
		}
	}
}

// TestConversionRefusesAShellCommand: cron runs its command through /bin/sh and
// a systemd service has no shell at all, so a pipeline cannot be converted
// without changing what runs.
func TestConversionRefusesAShellCommand(t *testing.T) {
	a, backend := newTestApp(t)
	// The report line ends in a redirection, which is exactly the case.
	selectJob(t, a, screenCron, "root · report")
	drain(t, a, press(a, "t"))

	if a.mode == modeConfirm {
		t.Fatalf("a shell command was converted anyway")
	}
	if !strings.Contains(a.status, "shell") {
		t.Errorf("status = %q, want it to name the reason", a.status)
	}
	if len(backend.Ran()) != 0 {
		t.Errorf("a command ran anyway")
	}
}

// TestConversionRefusesReboot: @reboot is not a calendar, and systemd spells it
// with a different setting entirely.
func TestConversionRefusesReboot(t *testing.T) {
	a, _ := newTestApp(t)
	selectJob(t, a, screenCron, "ana · warm-cache")
	drain(t, a, press(a, "t"))

	if a.mode == modeConfirm {
		t.Fatalf("@reboot was converted to a calendar")
	}
	if !strings.Contains(a.status, "OnBootSec") {
		t.Errorf("status = %q, want it to name the setting to use", a.status)
	}
}

// TestAddingACronLineAppendsToYourOwnTable.
func TestAddingACronLineAppendsToYourOwnTable(t *testing.T) {
	a, backend := newTestApp(t)
	gotoScreen(t, a, screenCron)
	drain(t, a, press(a, "a"))
	if a.mode != modeForm {
		t.Fatalf("a did not open the editor (status: %s)", a.status)
	}
	setText(t, &a.form, fieldSchedule, "0 3 * * *")
	setText(t, &a.form, fieldCommand, "/usr/local/bin/nightly")
	drain(t, a, press(a, "enter"))

	if a.mode != modeConfirm {
		t.Fatalf("the form did not open a confirm dialog (status: %s)", a.status)
	}
	if !strings.Contains(a.confirm.Body, "+0 3 * * * /usr/local/bin/nightly") {
		t.Errorf("the diff does not show the new line:\n%s", a.confirm.Body)
	}
	drain(t, a, press(a, "y"))

	var found bool
	for _, job := range a.model.Jobs {
		if job.Command == "/usr/local/bin/nightly" {
			found = true
		}
	}
	if !found {
		t.Errorf("the line was not added to the sample table")
	}
	_ = backend
}

// TestCreatingATimerWritesBothUnits.
func TestCreatingATimerWritesBothUnits(t *testing.T) {
	a, backend := newTestApp(t)
	drain(t, a, press(a, "c"))
	if a.mode != modeForm {
		t.Fatalf("c did not open the editor (status: %s)", a.status)
	}
	a.form.set(fieldPersistent, "true")
	for key, value := range map[string]string{
		fieldName:    "nightly-backup",
		fieldCommand: "/usr/local/bin/backup --offsite",
	} {
		setText(t, &a.form, key, value)
	}
	setText(t, &a.form, fieldSchedule, "*-*-* 02:30:00")
	drain(t, a, press(a, "enter"))

	if a.mode != modeConfirm {
		t.Fatalf("the form did not open a confirm dialog (status: %s)", a.status)
	}
	lines := strings.Split(a.confirm.Command, "\n")
	if len(lines) != 3 {
		t.Fatalf("previewed %d commands, want the two installs and a reload:\n%s",
			len(lines), a.confirm.Command)
	}
	if !strings.Contains(lines[0], "/etc/systemd/system/nightly-backup.service") ||
		!strings.Contains(lines[1], "/etc/systemd/system/nightly-backup.timer") ||
		!strings.Contains(lines[2], "daemon-reload") {
		t.Errorf("previewed commands = %q", a.confirm.Command)
	}
	// The service is written before the timer, so systemd never sees a timer
	// pointing at a unit that is not there yet.
	if strings.Index(a.confirm.Command, ".service") >
		strings.Index(a.confirm.Command, ".timer") {
		t.Errorf("the timer is installed before its service:\n%s", a.confirm.Command)
	}
	drain(t, a, press(a, "y"))
	if len(backend.Ran()) != 3 {
		t.Errorf("ran %d commands", len(backend.Ran()))
	}
}

// TestCreateFormRefusesACommandSystemdCouldNotRun: systemd runs ExecStart with
// no shell and no PATH of yours.
func TestCreateFormRefusesACommandSystemdCouldNotRun(t *testing.T) {
	a, backend := newTestApp(t)
	drain(t, a, press(a, "c"))
	setText(t, &a.form, fieldName, "nightly-backup")
	setText(t, &a.form, fieldSchedule, "*-*-* 02:30:00")
	setText(t, &a.form, fieldCommand, "backup --offsite")
	drain(t, a, press(a, "enter"))

	if a.mode == modeConfirm {
		t.Fatalf("the form accepted a bare program name")
	}
	if !strings.Contains(a.status, "absolute path") {
		t.Errorf("status = %q", a.status)
	}
	if len(backend.Ran()) != 0 {
		t.Errorf("a command ran anyway")
	}
}

// TestEditorRefusesAScheduleTheSchedulerWouldNot: the form and the writer share
// one validator, so the form cannot approve something the writer refuses.
func TestEditorRefusesAScheduleTheSchedulerWouldNot(t *testing.T) {
	a, backend := newTestApp(t)
	selectJob(t, a, screenCron, "ana · check-queue")
	drain(t, a, press(a, "e"))
	a.form.fields[0].input.SetValue("61 * * * *")
	a.form.reread()
	if a.form.readingErr == "" {
		t.Errorf("the form read a schedule that is not one as valid")
	}
	drain(t, a, press(a, "enter"))

	if a.mode == modeConfirm {
		t.Errorf("the form accepted a schedule cron would refuse")
	}
	if a.status == "" {
		t.Errorf("the form refused silently")
	}
	if len(backend.Ran()) != 0 {
		t.Errorf("a command ran anyway")
	}
}

// TestEditingAMonotonicTimerIsRefused: a timer with no OnCalendar has nothing a
// drop-in could change, and writing one that did nothing would be worse than
// saying so.
func TestEditingAMonotonicTimerIsRefused(t *testing.T) {
	a, _ := newTestApp(t)
	gotoScreen(t, a, screenTimers)
	// The sample machine has no monotonic timer, so one is put on the model
	// directly: this is about the guard, not about the sample data.
	a.model.Jobs = append(a.model.Jobs, schedule.Job{
		ID: "timer:apt-daily.timer", Name: "apt-daily.timer",
		Kind: schedule.KindTimer, Unit: "apt-daily.timer",
		Schedule: "OnBootUSec=10min", Monotonic: true,
	})
	a.applyFilter()
	selectJob(t, a, screenTimers, "apt-daily.timer")
	drain(t, a, press(a, "e"))

	if a.mode == modeForm {
		t.Fatalf("the editor opened on a timer with no OnCalendar")
	}
	if !strings.Contains(a.status, "OnCalendar") {
		t.Errorf("status = %q", a.status)
	}
}

// TestEditingARunPartsScriptIsRefused: the directory is the schedule, so the
// change is moving the file and that is not a confirm dialog.
func TestEditingARunPartsScriptIsRefused(t *testing.T) {
	a, _ := newTestApp(t)
	selectJob(t, a, screenCron, "cron.daily · man-db")
	drain(t, a, press(a, "e"))
	if a.mode == modeForm {
		t.Fatalf("the editor opened on a run-parts script")
	}
	if !strings.Contains(a.status, "directory") {
		t.Errorf("status = %q", a.status)
	}
}

// TestDetailOpensOnEveryScreen: enter must open something everywhere, because a
// row a reader cannot open is a row whose truncated cells are all they get.
func TestDetailOpensOnEveryScreen(t *testing.T) {
	for _, s := range []screen{screenAll, screenTimers, screenCron} {
		a, _ := newTestApp(t)
		gotoScreen(t, a, s)
		drain(t, a, press(a, "enter"))
		if a.mode != modeDetail {
			t.Fatalf("%s: enter opened nothing (status: %s)", s.title(), a.status)
		}
		lines := a.detailLines()
		if len(lines) < 10 {
			t.Errorf("%s: the detail screen is %d lines", s.title(), len(lines))
		}
		drain(t, a, press(a, "esc"))
		if a.mode != modeBrowse {
			t.Errorf("%s: esc did not return to the table", s.title())
		}
	}
}

// TestDetailShowsTheReadingTheUnitAndTheLog is what the per-row screen is for.
func TestDetailShowsTheReadingTheUnitAndTheLog(t *testing.T) {
	a, _ := newTestApp(t)
	selectJob(t, a, screenTimers, "backup.timer")
	drain(t, a, press(a, "enter"))

	view := strings.Join(a.detailLines(), "\n")
	for _, want := range []string{
		"backup.timer",
		"Every day at 02:30", // the reading
		"The next runs",      // computed by the scheduler
		"As it is written",   // the unit
		"What the log says",  // the journal
		"exit status 2",      // why it failed
		"backup.service",     // what it activates
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the detail screen is missing %q:\n%s", want, view)
		}
	}
}

// TestCronDetailSaysWhatCronDoesNotRecord: a screen that showed "ok" for a cron
// job would be claiming something cron never said.
func TestCronDetailSaysWhatCronDoesNotRecord(t *testing.T) {
	a, _ := newTestApp(t)
	selectJob(t, a, screenCron, "ana · warm-cache")
	drain(t, a, press(a, "enter"))

	view := strings.Join(a.detailLines(), "\n")
	if !strings.Contains(view, "cron publishes no future runs") {
		t.Errorf("the detail screen claims a next run for a cron line:\n%s", view)
	}
}

// TestFilterMatchesEveryScreen.
func TestFilterMatchesEveryScreen(t *testing.T) {
	a, _ := newTestApp(t)

	a.filter = "backup.timer"
	a.applyFilter()
	if len(a.rows[screenAll]) != 1 || len(a.rows[screenTimers]) != 1 {
		t.Errorf("the timer filter matched %d and %d rows, want 1 each",
			len(a.rows[screenAll]), len(a.rows[screenTimers]))
	}
	if len(a.rows[screenCron]) != 0 {
		t.Errorf("a timer matched on the cron screen")
	}

	// The filter reaches the English too, which is what makes it useful: nobody
	// remembers which timer is the one that runs at half past two.
	a.filter = "Every day at 02:30"
	a.applyFilter()
	if len(a.rows[screenAll]) != 1 {
		t.Errorf("filtering on the reading matched %d rows", len(a.rows[screenAll]))
	}

	a.filter = "nothing here"
	a.applyFilter()
	if len(a.rows[screenAll])+len(a.rows[screenTimers])+len(a.rows[screenCron]) != 0 {
		t.Errorf("a filter that matches nothing kept rows")
	}
}

// TestRendersAtEveryWidth is the responsive contract: from a narrow pane to a
// wide screen, no frame may wrap, because a wrapped row desynchronises Bubble
// Tea's line accounting and every frame after it lands in the wrong place.
func TestRendersAtEveryWidth(t *testing.T) {
	for width := 40; width <= 200; width += 4 {
		a, _ := newTestApp(t)
		a.width, a.height = width, 24
		a.clampCursor()

		for s := screen(0); s < screenCount; s++ {
			a.screen = s
			a.mode = modeBrowse
			checkWidth(t, a, s.title(), width)
		}

		a.screen = screenTimers
		a.mode = modeDetail
		a.detail = detailData{job: a.rows[screenTimers][0]}
		checkWidth(t, a, "detail", width)

		a.mode = modeHelp
		checkWidth(t, a, "help", width)

		a.mode = modeForm
		a.form = newEditForm(a.rows[screenTimers][0], a.caps)
		checkWidth(t, a, "edit form", width)

		a.form = newCreateForm(a.caps)
		checkWidth(t, a, "create form", width)

		a.form = newAddForm("ana", a.caps)
		checkWidth(t, a, "add form", width)

		// The add form with a target chosen carries two more fields, and the
		// longest destination line the tool ever renders.
		a.form.set(fieldTarget, targetCronD)
		a.form.retarget()
		checkWidth(t, a, "add form on /etc/cron.d", width)

		// And the prompt that stands in front of a deletion, which carries the
		// longest help line.
		a.mode = modeBrowse
		selectJob(t, a, screenTimers, "mirror-sync.timer")
		drain(t, a, press(a, "d"))
		if a.mode != modeTyped {
			t.Fatalf("d on a timer this tool wrote did not ask for the name")
		}
		checkWidth(t, a, "typed delete prompt", width)
	}
}

// checkWidth renders the current frame and fails when a line overflows.
func checkWidth(t *testing.T, a *app, name string, width int) {
	t.Helper()
	for i, line := range strings.Split(a.View(), "\n") {
		if got := lineWidth(line); got > width {
			t.Fatalf("%s at %d cols: line %d is %d cells wide",
				name, width, i, got)
		}
	}
}

// lineWidth measures a rendered line, ignoring the ANSI escapes the theme adds.
func lineWidth(line string) int {
	width, inEscape := 0, false
	for _, r := range line {
		switch {
		case r == 0x1b:
			inEscape = true
		case inEscape && (r == 'm' || r == 'K' || r == 'H'):
			inEscape = false
		case inEscape:
		default:
			width++
		}
	}
	return width
}

func TestBusyStateSwallowsInput(t *testing.T) {
	a, backend := newTestApp(t)
	selectJob(t, a, screenTimers, "backup.timer")
	a.busy = true
	drain(t, a, press(a, "E"))
	if a.mode != modeBrowse || len(backend.Ran()) != 0 {
		t.Errorf("a key pressed while a command runs must be ignored")
	}
}

// setText fills a text field of the open form by name.
func setText(t *testing.T, form *jobForm, key, value string) {
	t.Helper()
	for i := range form.fields {
		if form.fields[i].key == key {
			form.fields[i].input.SetValue(value)
			form.reread()
			return
		}
	}
	t.Fatalf("the form has no field named %q", key)
}

// TestDeletingOurOwnTimerAsksForTheNameFirst is the whole shape of the delete
// path: the unit's name has to be typed before the commands are even shown, and
// what then runs is the unit unloaded, both files removed, and a daemon-reload.
func TestDeletingOurOwnTimerAsksForTheNameFirst(t *testing.T) {
	a, backend := newTestApp(t)
	selectJob(t, a, screenTimers, "mirror-sync.timer")

	drain(t, a, press(a, "d"))
	if a.mode != modeTyped {
		t.Fatalf("d did not ask for the name (status: %s)", a.status)
	}
	if len(backend.Ran()) != 0 {
		t.Fatalf("a command ran before anything was confirmed")
	}

	drain(t, a, press(a, "mirror-sync.timer"))
	drain(t, a, press(a, "enter"))
	if a.mode != modeConfirm {
		t.Fatalf("the typed name did not open the dialog (status: %s)", a.status)
	}
	want := []string{
		"systemctl disable --now mirror-sync.timer",
		"rm -f -- /etc/systemd/system/mirror-sync.timer",
		"rm -f -- /etc/systemd/system/mirror-sync.service",
		"systemctl daemon-reload",
	}
	for _, line := range want {
		if !strings.Contains(a.confirm.Command, line) {
			t.Errorf("the preview is missing %q:\n%s", line, a.confirm.Command)
		}
	}
	// Both files are diffed away, because what they said is nowhere else once
	// this runs.
	for _, unit := range []string{"mirror-sync.timer", "mirror-sync.service"} {
		if !strings.Contains(a.confirm.Body, unit) {
			t.Errorf("the diff does not show %s going:\n%s", unit, a.confirm.Body)
		}
	}

	drain(t, a, press(a, "y"))
	for _, job := range a.model.Jobs {
		if job.Unit == "mirror-sync.timer" {
			t.Errorf("the timer is still on the machine after the delete ran")
		}
	}
}

// TestATypoDeletesNothing: the prompt is a gate, not a formality.
func TestATypoDeletesNothing(t *testing.T) {
	a, backend := newTestApp(t)
	selectJob(t, a, screenTimers, "mirror-sync.timer")
	drain(t, a, press(a, "d"))

	drain(t, a, press(a, "mirror-sync"))
	drain(t, a, press(a, "enter"))
	if a.mode == modeConfirm {
		t.Fatalf("a name that was not the unit's opened the dialog")
	}
	if !strings.Contains(a.status, "mirror-sync.timer") {
		t.Errorf("status = %q, want it to say what was expected", a.status)
	}
	if len(backend.Ran()) != 0 {
		t.Errorf("a command ran anyway")
	}
}

// TestChangingWhatOurOwnTimerRuns writes a drop-in on the service, not on the
// timer: ExecStart is the service's setting, and the empty assignment in front
// of it is what stops systemd running the old command as well.
func TestChangingWhatOurOwnTimerRuns(t *testing.T) {
	a, backend := newTestApp(t)
	selectJob(t, a, screenTimers, "mirror-sync.timer")

	drain(t, a, press(a, "e"))
	if a.mode != modeForm {
		t.Fatalf("e did not open the editor (status: %s)", a.status)
	}
	if !a.form.has(fieldCommand) {
		t.Fatalf("the editor for a timer this tool wrote has no command field")
	}
	setText(t, &a.form, fieldCommand, "/usr/local/bin/mirror-sync --quiet")
	drain(t, a, press(a, "enter"))

	if a.mode != modeConfirm {
		t.Fatalf("the form did not open a confirm dialog (status: %s)", a.status)
	}
	if !strings.Contains(a.confirm.Body,
		"+ExecStart=\n+ExecStart=/usr/local/bin/mirror-sync --quiet") {
		t.Errorf("the drop-in does not clear the old command first:\n%s",
			a.confirm.Body)
	}
	if !strings.Contains(a.confirm.Command,
		"/etc/systemd/system/mirror-sync.service.d/90-tui-cron.conf") {
		t.Errorf("the change was not written to the service's drop-in:\n%s",
			a.confirm.Command)
	}
	drain(t, a, press(a, "y"))

	for _, cmd := range backend.Ran() {
		if strings.Contains(cmd.String(), "restart") {
			t.Errorf("a command change restarted the timer: %q", cmd.String())
		}
	}
	for _, job := range a.model.Jobs {
		if job.Unit == "mirror-sync.timer" &&
			job.Command != "/usr/local/bin/mirror-sync --quiet" {
			t.Errorf("the sample timer still runs %q", job.Command)
		}
	}
}

// TestATimerWeDidNotWriteOffersNoCommandField: re-pointing a unit a package
// installed is a change that package's next update would silently undo, so the
// field is absent rather than present and refused.
func TestATimerWeDidNotWriteOffersNoCommandField(t *testing.T) {
	a, _ := newTestApp(t)
	selectJob(t, a, screenTimers, "logrotate.timer")
	drain(t, a, press(a, "e"))
	if a.mode != modeForm {
		t.Fatalf("e did not open the editor (status: %s)", a.status)
	}
	if a.form.has(fieldCommand) {
		t.Errorf("the editor offers to re-point a unit a package installed")
	}
}

// TestTheScheduleAndTheCommandAreTwoChanges: they are two drop-ins on two
// units, and the dialog reviews one file.
func TestTheScheduleAndTheCommandAreTwoChanges(t *testing.T) {
	a, backend := newTestApp(t)
	selectJob(t, a, screenTimers, "mirror-sync.timer")
	drain(t, a, press(a, "e"))

	setText(t, &a.form, fieldSchedule, "*-*-* 05:00:00")
	setText(t, &a.form, fieldCommand, "/usr/local/bin/mirror-sync --quiet")
	drain(t, a, press(a, "enter"))

	if a.mode == modeConfirm {
		t.Fatalf("both files were changed behind one diff")
	}
	if !strings.Contains(a.status, "one at a time") {
		t.Errorf("status = %q, want it to say why", a.status)
	}
	if len(backend.Ran()) != 0 {
		t.Errorf("a command ran anyway")
	}
}

// TestAddingToAnotherAccountsTable goes through cron's own interface for
// somebody else's table, which is `crontab -u`.
func TestAddingToAnotherAccountsTable(t *testing.T) {
	a, backend := newTestApp(t)
	gotoScreen(t, a, screenCron)
	drain(t, a, press(a, "a"))
	if !a.form.has(fieldTarget) {
		t.Fatalf("the add form offers no target on a machine that has all three")
	}

	a.form.set(fieldTarget, targetUserTable)
	a.form.retarget()
	setText(t, &a.form, fieldUser, "root")
	setText(t, &a.form, fieldSchedule, "0 4 * * *")
	setText(t, &a.form, fieldCommand, "/usr/local/bin/rotate")
	drain(t, a, press(a, "enter"))

	if a.mode != modeConfirm {
		t.Fatalf("the form did not open a confirm dialog (status: %s)", a.status)
	}
	if !strings.Contains(a.confirm.Command, "crontab -u root ") {
		t.Errorf("the table was not replaced through crontab -u:\n%s",
			a.confirm.Command)
	}
	// Replacing somebody else's whole table is the caveat that has to be said
	// out loud.
	if !strings.Contains(a.confirm.Body, "whole crontab") {
		t.Errorf("the dialog does not say whose table is replaced:\n%s",
			a.confirm.Body)
	}
	drain(t, a, press(a, "y"))
	if len(backend.Ran()) == 0 {
		t.Fatalf("nothing ran after the confirmation")
	}
	var found bool
	for _, job := range a.model.Jobs {
		if job.Command == "/usr/local/bin/rotate" && job.Owner == "root" {
			found = true
		}
	}
	if !found {
		t.Errorf("the line is not in root's table after the reload")
	}
}

// TestAddingToCronD installs a file rather than replacing a table, and the line
// carries the extra user field that format has.
func TestAddingToCronD(t *testing.T) {
	a, _ := newTestApp(t)
	gotoScreen(t, a, screenCron)
	drain(t, a, press(a, "a"))

	a.form.set(fieldTarget, targetCronD)
	a.form.retarget()
	setText(t, &a.form, fieldFile, "nightly-report")
	setText(t, &a.form, fieldUser, "root")
	setText(t, &a.form, fieldSchedule, "0 5 * * *")
	setText(t, &a.form, fieldCommand, "/usr/local/bin/report")
	drain(t, a, press(a, "enter"))

	if a.mode != modeConfirm {
		t.Fatalf("the form did not open a confirm dialog (status: %s)", a.status)
	}
	if !strings.Contains(a.confirm.Command,
		"install -m 644 /tmp/tui-cron/nightly-report /etc/cron.d/nightly-report") {
		t.Errorf("the file was not installed into /etc/cron.d:\n%s",
			a.confirm.Command)
	}
	if !strings.Contains(a.confirm.Body, "+0 5 * * * root /usr/local/bin/report") {
		t.Errorf("the line carries no user field:\n%s", a.confirm.Body)
	}
}

// TestCronDRefusesANameCronWouldIgnore: cron ignores a file in /etc/cron.d
// whose name contains a dot, so a table saved as backup.cron is a table that
// silently never runs.
func TestCronDRefusesANameCronWouldIgnore(t *testing.T) {
	a, backend := newTestApp(t)
	gotoScreen(t, a, screenCron)
	drain(t, a, press(a, "a"))

	a.form.set(fieldTarget, targetCronD)
	a.form.retarget()
	setText(t, &a.form, fieldFile, "backup.cron")
	setText(t, &a.form, fieldUser, "root")
	setText(t, &a.form, fieldSchedule, "0 5 * * *")
	setText(t, &a.form, fieldCommand, "/usr/local/bin/report")
	drain(t, a, press(a, "enter"))

	if a.mode == modeConfirm {
		t.Fatalf("a file name cron ignores was accepted")
	}
	if !strings.Contains(a.status, "never run") {
		t.Errorf("status = %q, want it to say what would happen", a.status)
	}
	if len(backend.Ran()) != 0 {
		t.Errorf("a command ran anyway")
	}
}

// TestWithoutRootTheAddFormHasNoTarget: `crontab -u` and a write into
// /etc/cron.d are refused to everybody else, so offering them would be offering
// a dialog that ends in a permission error.
func TestWithoutRootTheAddFormHasNoTarget(t *testing.T) {
	caps := jobs.NewFake().Capabilities()
	caps.SupportsAnyTable = false
	form := newAddForm("ana", caps)
	if form.has(fieldTarget) {
		t.Errorf("a non-root add form offers tables it cannot write")
	}
	if !strings.Contains(form.title, "ana") {
		t.Errorf("form title = %q, want it to name this account's table", form.title)
	}
}
