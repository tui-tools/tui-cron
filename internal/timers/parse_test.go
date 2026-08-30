package timers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tui-tools/tui-cron/internal/schedule"
)

// The fixtures in testdata are real output, captured from the Fedora 42 host
// this backend was written on. Every command they came from — `systemctl
// list-timers`, `systemctl list-units`, `systemctl show`, `systemctl cat`,
// `systemd-analyze calendar` — answers to any user, so none of them had to be
// invented, and the hostname in the journal capture is the only thing that was
// changed.

// fixture reads one of them.
func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // the name is a literal in the tests above, and testdata is in the repository
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(raw)
}

// TestParseTimerListJSON reads the enumeration systemd 250 and newer can give.
func TestParseTimerListJSON(t *testing.T) {
	units, err := ParseTimerListJSON(fixture(t, "list-timers.json"))
	if err != nil {
		t.Fatalf("ParseTimerListJSON: %v", err)
	}
	if len(units) == 0 {
		t.Fatalf("no timers were read from the capture")
	}
	for _, unit := range units {
		if !strings.HasSuffix(unit, ".timer") {
			t.Errorf("%q is not a timer unit", unit)
		}
	}
	if !contains(units, "logrotate.timer") {
		t.Errorf("logrotate.timer is missing from %v", units)
	}
}

// TestParseTimerListJSONRefusesTheTextTable is what makes the fallback
// necessary rather than optional: below systemd 250 the flag does not exist and
// the output is a table, which must fail loudly and not half-parse.
func TestParseTimerListJSONRefusesTheTextTable(t *testing.T) {
	_, err := ParseTimerListJSON(fixture(t, "list-units-timer.txt"))
	if err == nil {
		t.Fatalf("the text table parsed as JSON")
	}
	if !strings.Contains(err.Error(), "250") {
		t.Errorf("the error does not name the version that has it: %v", err)
	}
}

// TestParseTimerListText is the fallback enumeration, which has to produce the
// same units as the JSON one on a machine that can do both.
func TestParseTimerListText(t *testing.T) {
	fromText := ParseTimerListText(fixture(t, "list-units-timer.txt"))
	if len(fromText) == 0 {
		t.Fatalf("no timers were read from the text table")
	}
	fromJSON, err := ParseTimerListJSON(fixture(t, "list-timers.json"))
	if err != nil {
		t.Fatalf("ParseTimerListJSON: %v", err)
	}
	// The two captures were taken seconds apart on the same machine, so every
	// unit the JSON lists must also be in the text table.
	for _, unit := range fromJSON {
		if !contains(fromText, unit) {
			t.Errorf("%q is in the JSON list and not in the text one", unit)
		}
	}
}

// TestJobFromTimer reads one real timer end to end: its calendar, its English,
// its next and last runs, and the Persistent setting the warning turns on.
func TestJobFromTimer(t *testing.T) {
	job := JobFromTimer(ParseProperties(fixture(t, "show-logrotate-timer.txt")),
		schedule.KindTimer, "root")

	if job.Unit != "logrotate.timer" || job.Service != "logrotate.service" {
		t.Errorf("unit = %q, activates %q", job.Unit, job.Service)
	}
	if job.ID != "timer:logrotate.timer" {
		t.Errorf("ID = %q", job.ID)
	}
	// systemd normalizes the unit file's `OnCalendar=daily` before it reports
	// it, which is why the describer only has to know one grammar.
	if job.Schedule != "*-*-* 00:00:00" {
		t.Errorf("schedule = %q, want the normalized calendar", job.Schedule)
	}
	if job.Explain != "Every day at 00:00" {
		t.Errorf("explain = %q", job.Explain)
	}
	if !job.PersistentKnown || !job.Persistent {
		t.Errorf("Persistent = %v (known %v), want true",
			job.Persistent, job.PersistentKnown)
	}
	if job.NeedsPersistent() {
		t.Errorf("a timer with Persistent=yes must not be warned about")
	}
	if job.Next.IsZero() || job.Last.IsZero() {
		t.Errorf("next = %v, last = %v; both are in the capture", job.Next, job.Last)
	}
	if !job.Enabled || !job.Active {
		t.Errorf("enabled = %v, active = %v", job.Enabled, job.Active)
	}
	if job.Monotonic {
		t.Errorf("a calendar timer was read as monotonic")
	}
	// The raw properties are kept so the detail screen can show what was read
	// rather than what was made of it.
	if !strings.Contains(job.Raw, "TimersCalendar=") {
		t.Errorf("the raw properties were not kept:\n%s", job.Raw)
	}
}

// TestJobFromMonotonicTimer covers the timers that have no calendar at all.
// They fire relative to an event, there is nothing for the describer to say,
// and the schedule editor has to refuse them rather than write a drop-in that
// would do nothing.
func TestJobFromMonotonicTimer(t *testing.T) {
	job := JobFromTimer(ParseProperties(fixture(t, "show-monotonic-timer.txt")),
		schedule.KindTimer, "root")
	if !job.Monotonic {
		t.Fatalf("a monotonic timer was not marked as one: %+v", job)
	}
	if job.Schedule == "" || job.Schedule == "—" {
		t.Errorf("the monotonic schedule was not reported: %q", job.Schedule)
	}
	if !strings.Contains(job.Explain, "OnCalendar") {
		t.Errorf("the explanation does not say why it cannot be edited: %q",
			job.Explain)
	}
	if job.NeedsPersistent() {
		t.Errorf("a monotonic timer cannot be judged on Persistent")
	}
}

// TestApplyServiceReadsTheOutcome folds the activated unit's properties in:
// what it runs, and how the last run ended.
func TestApplyServiceReadsTheOutcome(t *testing.T) {
	job := JobFromTimer(ParseProperties(fixture(t, "show-logrotate-timer.txt")),
		schedule.KindTimer, "root")
	ApplyService(&job, ParseProperties(fixture(t, "show-logrotate-service.txt")))

	if job.Outcome != schedule.OutcomeOK {
		t.Errorf("outcome = %q (%s), want ok", job.Outcome, job.OutcomeDetail)
	}
	// The command comes from ExecStart's argv[], not from its path=, so the
	// arguments are on screen too.
	if !strings.Contains(job.Command, "logrotate") ||
		!strings.Contains(job.Command, "logrotate.conf") {
		t.Errorf("command = %q, want the full argv", job.Command)
	}
}

// TestApplyServiceReadsAFailure covers the row the whole tool is for.
func TestApplyServiceReadsAFailure(t *testing.T) {
	job := schedule.Job{Service: "backup.service"}
	ApplyService(&job, ParseProperties(
		"Result=exit-code\nExecMainStatus=2\n"+
			"ExecStart={ path=/usr/local/bin/backup ; argv[]=/usr/local/bin/backup --offsite }\n"))
	if job.Outcome != schedule.OutcomeFailed {
		t.Fatalf("outcome = %q, want failed", job.Outcome)
	}
	if !strings.Contains(job.OutcomeDetail, "exit status 2") {
		t.Errorf("detail = %q, want systemd's own words", job.OutcomeDetail)
	}
}

// TestNeverRunStaysNeverRun is the trap in systemd's reporting: a service that
// has never been asked to do anything reports Result=success and status 0, and
// reading that as a pass would put a green tick on a job that has not run.
func TestNeverRunStaysNeverRun(t *testing.T) {
	job := JobFromTimer(ParseProperties(
		"Id=plocate-updatedb.timer\nUnit=plocate-updatedb.service\n"+
			"TimersCalendar={ OnCalendar=*-*-* 00:00:00 ; next_elapse=0 }\n"+
			"LastTriggerUSec=n/a\nPersistent=yes\nActiveState=active\n"+
			"UnitFileState=enabled\n"), schedule.KindTimer, "root")
	if job.Outcome != schedule.OutcomeNever {
		t.Fatalf("outcome = %q, want never", job.Outcome)
	}
	ApplyService(&job, ParseProperties("Result=success\nExecMainStatus=0\n"))
	if job.Outcome != schedule.OutcomeNever {
		t.Errorf("a never-run job was read as a pass: %q (%s)",
			job.Outcome, job.OutcomeDetail)
	}
}

// TestPersistentWarningOnlyAppliesToSlowTimers: a timer that fires every ten
// minutes does not care about a missed run, and warning about it would train
// people to ignore the column.
func TestPersistentWarningOnlyAppliesToSlowTimers(t *testing.T) {
	tests := map[string]bool{
		"*-*-* 00:00:00":     true,
		"*-*-* 04:00:00":     true,
		"Mon *-*-* 00:00:00": true,
		"*-*-* *:00/10:00":   false,
		"*-*-* *:00:00":      false,
		"*-*-* 02,14:00:00":  false,
	}
	for expression, want := range tests {
		job := schedule.Job{
			Kind: schedule.KindTimer, Schedule: expression,
			PersistentKnown: true, Persistent: false,
		}
		if got := job.NeedsPersistent(); got != want {
			t.Errorf("%q: NeedsPersistent = %v, want %v", expression, got, want)
		}
	}
	// And a timer that does carry it is never warned about, however slow.
	job := schedule.Job{
		Kind: schedule.KindTimer, Schedule: "*-*-* 00:00:00",
		PersistentKnown: true, Persistent: true,
	}
	if job.NeedsPersistent() {
		t.Errorf("a timer with Persistent=true was warned about")
	}
	// An unread value is not a false one.
	job.PersistentKnown, job.Persistent = false, false
	if job.NeedsPersistent() {
		t.Errorf("an unread Persistent was treated as false")
	}
}

// TestParseTimestamp reads systemd's own rendering back, including the
// sentinels it uses for "never".
func TestParseTimestamp(t *testing.T) {
	if ParseTimestamp("Sun 2026-08-30 00:09:19 -03").IsZero() {
		t.Errorf("a real timestamp did not parse")
	}
	if ParseTimestamp("Sun 2026-08-30 00:09:19 UTC").IsZero() {
		t.Errorf("a named zone did not parse")
	}
	for _, value := range []string{"", "n/a", "0", "not a date"} {
		if !ParseTimestamp(value).IsZero() {
			t.Errorf("ParseTimestamp(%q) produced a moment", value)
		}
	}
}

// TestParseCalendarOutput reads what `systemd-analyze calendar` printed, in
// both the shapes it prints: with an "Original form" line when the expression
// was a shorthand, and without one when it was already normalized.
func TestParseCalendarOutput(t *testing.T) {
	for _, name := range []string{
		"analyze-calendar.txt", "analyze-calendar-shorthand.txt",
	} {
		out := fixture(t, name)
		if got := ParseNormalized(out); got != "*-*-* 00:00:00" {
			t.Errorf("%s: normalized = %q", name, got)
		}
		elapses := ParseElapses(out)
		if len(elapses) != 5 {
			t.Errorf("%s: read %d elapses, want 5: %v", name, len(elapses), elapses)
		}
		for _, elapse := range elapses {
			if strings.TrimSpace(elapse) == "" {
				t.Errorf("%s: an elapse came back empty: %v", name, elapses)
			}
		}
	}
}

// TestUnescapeUnitName reverses the escaping that shows up in a generated unit
// name, and leaves an escape it cannot decode exactly as it was.
func TestUnescapeUnitName(t *testing.T) {
	tests := map[string]string{
		"logrotate.timer":                 "logrotate.timer",
		`dev-disk-by\x2ddiskseq-1.device`: "dev-disk-by-diskseq-1.device",
		`bad\xzz.timer`:                   `bad\xzz.timer`,
	}
	for input, want := range tests {
		if got := UnescapeUnitName(input); got != want {
			t.Errorf("UnescapeUnitName(%q) = %q, want %q", input, got, want)
		}
	}
}

// contains reports whether a slice holds a value.
func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
