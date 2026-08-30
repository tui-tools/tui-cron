package schedule

import (
	"strings"
	"testing"
)

// TestCalendarFromCron pins the translation. Every expected value was checked
// against `systemd-analyze calendar` on a Fedora 42 host: the point of a
// conversion is that the timer fires when the cron line did, and an expression
// systemd reads differently would be a silent change of schedule.
func TestCalendarFromCron(t *testing.T) {
	tests := map[string]string{
		"* * * * *":       "*-*-* *:*:00",
		"*/5 * * * *":     "*-*-* *:00/5:00",
		"0 * * * *":       "*-*-* *:00:00",
		"01 * * * *":      "*-*-* *:01:00",
		"0 3 * * *":       "*-*-* 03:00:00",
		"30 4 * * *":      "*-*-* 04:30:00",
		"0 0 1 * *":       "*-*-01 00:00:00",
		"0 0 1 1 *":       "*-01-01 00:00:00",
		"0 2,14 * * *":    "*-*-* 02,14:00:00",
		"0 6 * * 1":       "Mon *-*-* 06:00:00",
		"30 4 * * 1-5":    "Mon..Fri *-*-* 04:30:00",
		"0 0 * * 0":       "Sun *-*-* 00:00:00",
		"0 0 * * 7":       "Sun *-*-* 00:00:00",
		"0 6 * * mon,fri": "Mon,Fri *-*-* 06:00:00",
		"@daily":          "*-*-* 00:00:00",
		"@hourly":         "*-*-* *:00:00",
		"@monthly":        "*-*-01 00:00:00",
		"@weekly":         "Sun *-*-* 00:00:00",
	}
	for expression, want := range tests {
		got, err := CalendarFromCron(expression)
		if err != nil {
			t.Errorf("CalendarFromCron(%q) = %v", expression, err)
			continue
		}
		if got != want {
			t.Errorf("CalendarFromCron(%q) = %q, want %q", expression, got, want)
		}
	}
}

// TestConvertedCalendarReadsTheSame is the property that makes a conversion
// trustworthy without a systemd on the machine: the OnCalendar produced must
// describe the same schedule as the cron line it came from.
func TestConvertedCalendarReadsTheSame(t *testing.T) {
	for _, expression := range []string{
		"*/5 * * * *", "0 3 * * *", "30 4 * * 1-5", "0 0 1 * *", "0 * * * *",
		"0 2,14 * * *", "0 6 * * 1",
	} {
		calendar, err := CalendarFromCron(expression)
		if err != nil {
			t.Fatalf("CalendarFromCron(%q) = %v", expression, err)
		}
		cron, timer := DescribeCron(expression), DescribeCalendar(calendar)
		if cron == "" || timer == "" {
			t.Errorf("%q: one side has no reading (%q / %q)", expression, cron, timer)
			continue
		}
		if cron != timer {
			t.Errorf("%q converts to %q, which reads differently:\n  cron:  %s\n  timer: %s",
				expression, calendar, cron, timer)
		}
	}
}

// TestCalendarFromCronRefusesTheTwoThatCannotConvert is the important half.
// Producing something plausible for either of these would produce a timer that
// fires at different times from the cron line it replaced.
func TestCalendarFromCronRefusesTheTwoThatCannotConvert(t *testing.T) {
	// @reboot is not a calendar at all: systemd spells it OnBootSec=.
	_, err := CalendarFromCron("@reboot")
	if err == nil {
		t.Fatalf("@reboot was converted to a calendar")
	}
	if !strings.Contains(err.Error(), "OnBootSec") {
		t.Errorf("the refusal does not name the setting to use instead: %v", err)
	}

	// A day of the month AND a weekday: cron runs on either, systemd on both.
	_, err = CalendarFromCron("0 0 1 * 1")
	if err == nil {
		t.Fatalf("a line with both a day and a weekday was converted")
	}
	if !strings.Contains(err.Error(), "EITHER") {
		t.Errorf("the refusal does not explain cron's OR rule: %v", err)
	}

	// And nonsense stays nonsense.
	if _, err := CalendarFromCron("not a schedule"); err == nil {
		t.Errorf("a non-schedule was converted")
	}
}
