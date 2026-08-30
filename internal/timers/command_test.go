package timers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tui-tools/tui-cron/internal/schedule"
	"github.com/tui-tools/tui-kit/runner"
)

// systemTimer and userTimer are the two jobs the builders are exercised
// against: one in the system manager, one in the caller's own.
var systemTimer = schedule.Job{
	Kind: schedule.KindTimer, Unit: "backup.timer", Service: "backup.service",
	Name: "backup.timer",
}

var userTimer = schedule.Job{
	Kind: schedule.KindUserTimer, Unit: "sync-notes.timer",
	Service: "sync-notes.service", Name: "sync-notes.timer",
}

// TestControlArgv pins the exact command line of every control action, for both
// managers. It is the family's central promise reduced to its smallest form:
// the argv is a function of the job and the verb, and nothing else.
func TestControlArgv(t *testing.T) {
	tests := []struct {
		job  schedule.Job
		verb string
		want string
	}{
		{systemTimer, "enable", "systemctl enable backup.timer"},
		{systemTimer, "disable", "systemctl disable backup.timer"},
		{systemTimer, "start", "systemctl start backup.timer"},
		{systemTimer, "stop", "systemctl stop backup.timer"},
		// The `--user` goes before the verb, which is where systemctl wants it.
		{userTimer, "enable", "systemctl --user enable sync-notes.timer"},
		{userTimer, "stop", "systemctl --user stop sync-notes.timer"},
	}
	for _, test := range tests {
		cmd, err := BuildControl(test.job, test.verb)
		if err != nil {
			t.Errorf("BuildControl(%s, %s) = %v", test.job.Unit, test.verb, err)
			continue
		}
		if got := cmd.String(); got != test.want {
			t.Errorf("BuildControl(%s, %s) = %q, want %q",
				test.job.Unit, test.verb, got, test.want)
		}
	}
}

// TestControlMarksWhatStopsAJob: disabling and stopping both mean the job stops
// happening, which is the kind of change the dialog paints in the danger
// colour.
func TestControlMarksWhatStopsAJob(t *testing.T) {
	for verb, want := range map[string]bool{
		"enable": false, "start": false, "disable": true, "stop": true,
	} {
		cmd, err := BuildControl(systemTimer, verb)
		if err != nil {
			t.Fatalf("BuildControl(%s) = %v", verb, err)
		}
		if cmd.Destructive != want {
			t.Errorf("%s: Destructive = %v, want %v", verb, cmd.Destructive, want)
		}
	}
}

// TestControlRefusesACronJob: systemctl has nothing to say about a crontab
// line, and the refusal is what the UI turns into a hint.
func TestControlRefusesACronJob(t *testing.T) {
	cron := schedule.Job{Kind: schedule.KindCrontab, Name: "ana · check-queue"}
	if _, err := BuildControl(cron, "enable"); err == nil {
		t.Errorf("a cron job was given a systemctl command")
	}
	if _, err := BuildRunNow(cron); err == nil {
		t.Errorf("a cron job was given a systemctl start")
	}
}

// TestRunNowStartsTheServiceNotTheTimer is the distinction that makes the key
// mean what it says. Starting the timer only arms it; what a reader means by
// "run it now" is the unit the timer would have started.
func TestRunNowStartsTheServiceNotTheTimer(t *testing.T) {
	cmd, err := BuildRunNow(systemTimer)
	if err != nil {
		t.Fatalf("BuildRunNow: %v", err)
	}
	if got := cmd.String(); got != "systemctl start backup.service" {
		t.Errorf("BuildRunNow = %q, want it to start the service", got)
	}
	if !cmd.Destructive {
		t.Errorf("running a job off its schedule must be painted as dangerous")
	}
}

// TestUnitNamesAreChecked: a unit name comes from the machine and ends up in an
// argv, so anything that is not one is refused before it gets there.
func TestUnitNamesAreChecked(t *testing.T) {
	for _, unit := range []string{
		"", "backup", "backup.timer extra", "../../etc/passwd.timer",
		"/etc/systemd/system/x.timer", "a b.timer", "x.socket",
	} {
		job := schedule.Job{Kind: schedule.KindTimer, Unit: unit, Service: unit}
		if _, err := BuildControl(job, "enable"); err == nil {
			t.Errorf("BuildControl accepted %q as a unit name", unit)
		}
	}
}

// TestCalendarArgv pins the read that validates a schedule and computes its
// next runs.
func TestCalendarArgv(t *testing.T) {
	cmd, err := BuildCalendar("*-*-* 03:00:00", 5)
	if err != nil {
		t.Fatalf("BuildCalendar: %v", err)
	}
	want := "systemd-analyze calendar --iterations=5 *-*-* 03:00:00"
	if got := cmd.String(); got != want {
		t.Errorf("BuildCalendar = %q, want %q", got, want)
	}
	if cmd.Destructive {
		t.Errorf("asking systemd what an expression means is a read")
	}
}

// TestCheckExpressionRefusesWhatWouldReachAFile: the value goes into an argv
// and then into /etc, so a newline or a `#` — either of which would smuggle a
// second directive into the drop-in — is refused here.
func TestCheckExpressionRefusesWhatWouldReachAFile(t *testing.T) {
	for _, expression := range []string{
		"", "  ", "daily\nOnCalendar=hourly", "daily # comment",
		"daily\r\n[Service]", "daily; rm -rf /", "$(id)",
	} {
		if err := CheckExpression(expression); err == nil {
			t.Errorf("CheckExpression accepted %q", expression)
		}
	}
	for _, expression := range []string{
		"daily", "*-*-* 03:00:00", "Mon..Fri *-*-* 09:00:00", "*-*-* *:00/5:00",
	} {
		if err := CheckExpression(expression); err != nil {
			t.Errorf("CheckExpression(%q) = %v", expression, err)
		}
	}
}

// TestRenderDropInResetsTheSchedule is the whole reason the drop-in is three
// lines rather than one.
//
// systemd *adds* to a list-valued setting when a drop-in assigns it, so a file
// saying only `OnCalendar=<new>` leaves the old schedule in place and the timer
// fires on both. The empty assignment first is what clears it.
func TestRenderDropInResetsTheSchedule(t *testing.T) {
	content, err := RenderDropIn("*-*-* 03:00:00")
	if err != nil {
		t.Fatalf("RenderDropIn: %v", err)
	}
	if !strings.Contains(content, "[Timer]") {
		t.Errorf("the drop-in has no [Timer] section:\n%s", content)
	}
	if !strings.Contains(content, "OnCalendar=\nOnCalendar=*-*-* 03:00:00") {
		t.Errorf("the drop-in does not clear the old schedule first:\n%s", content)
	}
	if _, err := RenderDropIn("daily\nExecStart=/bin/sh"); err == nil {
		t.Errorf("RenderDropIn accepted a second directive")
	}
}

// TestDropInPath pins where a schedule change lands: under /etc, in the unit's
// own drop-in directory, never over the unit file a distribution shipped.
func TestDropInPath(t *testing.T) {
	if got := DropInPathFor("backup.timer"); got !=
		"/etc/systemd/system/backup.timer.d/90-tui-cron.conf" {
		t.Errorf("DropInPathFor = %q", got)
	}
	if got := DropInDirFor("backup.timer"); got !=
		"/etc/systemd/system/backup.timer.d" {
		t.Errorf("DropInDirFor = %q", got)
	}
}

// TestInstallRefusesADestinationOutsideTheUnitDirectory: the one command that
// writes to /etc has a destination this package checks, not a parameter it
// trusts.
func TestInstallRefusesADestinationOutsideTheUnitDirectory(t *testing.T) {
	for _, destination := range []string{
		"/etc/passwd", "/etc/systemd/system/../../passwd",
		"/usr/lib/systemd/system/backup.timer", "relative.timer",
	} {
		if _, err := BuildInstall("/tmp/tui-cron-1/backup.timer", destination); err == nil {
			t.Errorf("BuildInstall accepted %q", destination)
		}
	}
	cmd, err := BuildInstall("/tmp/tui-cron-1/backup.timer",
		"/etc/systemd/system/backup.timer")
	if err != nil {
		t.Fatalf("BuildInstall: %v", err)
	}
	want := "install -m 644 /tmp/tui-cron-1/backup.timer /etc/systemd/system/backup.timer"
	if got := cmd.String(); got != want {
		t.Errorf("BuildInstall = %q, want %q", got, want)
	}
}

// TestRenderUnits pins what a generated timer and service say, and what they
// refuse to say.
func TestRenderUnits(t *testing.T) {
	service, timer, err := RenderUnits(schedule.NewTimer{
		Name:        "nightly-backup",
		Calendar:    "*-*-* 02:30:00",
		ExecStart:   "/usr/local/bin/backup --offsite",
		User:        "backup",
		Persistent:  true,
		Description: "Nightly offsite backup",
	}, "2026-08-30 09:41:00 UTC")
	if err != nil {
		t.Fatalf("RenderUnits: %v", err)
	}

	// Type=oneshot, because anything else leaves systemd waiting for a process
	// that already exited and calling the job failed when it was not.
	for _, want := range []string{
		"[Service]", "Type=oneshot", "User=backup",
		"ExecStart=/usr/local/bin/backup --offsite",
	} {
		if !strings.Contains(service, want) {
			t.Errorf("the service is missing %q:\n%s", want, service)
		}
	}
	for _, want := range []string{
		"[Timer]", "OnCalendar=*-*-* 02:30:00", "Persistent=true",
		"Unit=nightly-backup.service", "[Install]", "WantedBy=timers.target",
	} {
		if !strings.Contains(timer, want) {
			t.Errorf("the timer is missing %q:\n%s", want, timer)
		}
	}
	// Persistent is written only when it was asked for, so its absence in the
	// file means what the form said.
	_, plain, err := RenderUnits(schedule.NewTimer{
		Name: "x", Calendar: "daily", ExecStart: "/bin/true",
	}, "now")
	if err != nil {
		t.Fatalf("RenderUnits: %v", err)
	}
	if strings.Contains(plain, "Persistent") {
		t.Errorf("Persistent was written when it was not asked for:\n%s", plain)
	}
}

// TestRenderUnitsRefusesWhatSystemdCannotRun covers the three checks a form
// cannot skip: a name that is not one, a command systemd could not find, and
// anything that would open a second section in the file.
func TestRenderUnitsRefusesWhatSystemdCannotRun(t *testing.T) {
	base := schedule.NewTimer{
		Name: "ok-name", Calendar: "daily", ExecStart: "/bin/true",
	}
	tests := map[string]schedule.NewTimer{
		"no name":            {Calendar: "daily", ExecStart: "/bin/true"},
		"a path as a name":   {Name: "../x", Calendar: "daily", ExecStart: "/bin/true"},
		"an upper-case name": {Name: "Backup", Calendar: "daily", ExecStart: "/bin/true"},
		"no command":         {Name: "ok-name", Calendar: "daily"},
		// systemd runs ExecStart itself, with no shell and no PATH of yours.
		"a bare program name": {Name: "ok-name", Calendar: "daily", ExecStart: "backup"},
		"a second section": {Name: "ok-name", Calendar: "daily",
			ExecStart: "/bin/true\n[Service]\nUser=root"},
		"an account that is not one": {Name: "ok-name", Calendar: "daily",
			ExecStart: "/bin/true", User: "root; rm -rf /"},
		"a schedule that is not one": {Name: "ok-name", Calendar: "",
			ExecStart: "/bin/true"},
	}
	for name, spec := range tests {
		if _, _, err := RenderUnits(spec, "now"); err == nil {
			t.Errorf("RenderUnits accepted %s", name)
		}
	}
	if _, _, err := RenderUnits(base, "now"); err != nil {
		t.Errorf("RenderUnits refused a good spec: %v", err)
	}
}

// TestBaseNameAcceptsEitherSuffix: a form may be given "backup", "backup.timer"
// or "backup.service" and has to mean the same unit.
func TestBaseNameAcceptsEitherSuffix(t *testing.T) {
	for _, name := range []string{"backup", "backup.timer", "backup.service"} {
		if got := BaseName(name); got != "backup" {
			t.Errorf("BaseName(%q) = %q", name, got)
		}
		if err := CheckName(name); err != nil {
			t.Errorf("CheckName(%q) = %v", name, err)
		}
	}
}

// TestVerifyTakesWholeUnitsOnly documents the limit that shaped the design:
// `systemd-analyze verify` refuses a drop-in `.conf` outright, so a schedule
// change is checked with `systemd-analyze calendar` instead.
func TestVerifyTakesWholeUnitsOnly(t *testing.T) {
	if _, err := BuildVerify("/tmp/tui-cron-1/90-tui-cron.conf"); err == nil {
		t.Errorf("BuildVerify accepted a drop-in fragment")
	}
	cmd, err := BuildVerify("/tmp/tui-cron-1/x.timer", "/tmp/tui-cron-1/x.service")
	if err != nil {
		t.Fatalf("BuildVerify: %v", err)
	}
	want := "systemd-analyze verify /tmp/tui-cron-1/x.timer /tmp/tui-cron-1/x.service"
	if got := cmd.String(); got != want {
		t.Errorf("BuildVerify = %q, want %q", got, want)
	}
}

// TestGeneratedCalendarsAreAcceptedBySystemd runs the expressions this tool
// generates past the parser that will actually be armed with them.
//
// It is the assertion the unit tests cannot make on their own: `*:*/5:00` reads
// like a perfectly good step and systemd rejects it, and only systemd can say
// so. It skips where systemd-analyze is not installed, which is every machine
// this tool is not for.
func TestGeneratedCalendarsAreAcceptedBySystemd(t *testing.T) {
	if !runner.Available("systemd-analyze", searchPaths["systemd-analyze"]...) {
		t.Skip("no systemd-analyze on this machine")
	}
	unprivileged := false
	run, err := runner.New(runner.Options{
		Bin:             "systemd-analyze",
		SearchPaths:     searchPaths["systemd-analyze"],
		PrivilegedReads: &unprivileged,
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Skipf("systemd-analyze could not be resolved: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, cron := range []string{
		"* * * * *", "*/5 * * * *", "0 * * * *", "01 * * * *", "0 3 * * *",
		"30 4 * * *", "0 0 1 * *", "0 0 1 1 *", "0 2,14 * * *", "0 6 * * 1",
		"30 4 * * 1-5", "0 0 * * 0", "0 0/6 * * *", "@daily", "@hourly",
		"@monthly", "@weekly",
	} {
		calendar, convertErr := schedule.CalendarFromCron(cron)
		if convertErr != nil {
			t.Errorf("CalendarFromCron(%q) = %v", cron, convertErr)
			continue
		}
		out, readErr := run.Read(ctx, "systemd-analyze", "calendar", calendar)
		if readErr != nil {
			t.Errorf("%q converts to %q, which systemd refuses: %s",
				cron, calendar, runner.FirstLine(out))
			continue
		}
		if ParseNormalized(out) == "" {
			t.Errorf("%q converts to %q, which systemd did not normalize:\n%s",
				cron, calendar, out)
		}
	}
}
