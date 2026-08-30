package schedule

import (
	"strings"
	"testing"
	"time"
)

// TestDescribeCalendar pins the reading of every OnCalendar shape this tool
// meets.
//
// The expected forms are not invented: every one of them was run through
// `systemd-analyze calendar` on a Fedora 42 host and is the *normalized* string
// systemd printed, which is also what `systemctl show` reports in
// TimersCalendar. That is what makes one describer enough — the shorthands are
// normalized away before this code ever sees them, and the few a user might
// type into the form are mapped to the same normalized string.
func TestDescribeCalendar(t *testing.T) {
	tests := map[string]string{
		// The named shorthands, and the normalized form of each. Both spellings
		// must read the same, because a unit file says one and `systemctl show`
		// reports the other.
		"daily":              "Every day at 00:00",
		"*-*-* 00:00:00":     "Every day at 00:00",
		"hourly":             "Every hour, on the hour",
		"*-*-* *:00:00":      "Every hour, on the hour",
		"minutely":           "Every minute",
		"*-*-* *:*:00":       "Every minute",
		"weekly":             "Every Monday at 00:00",
		"Mon *-*-* 00:00:00": "Every Monday at 00:00",
		"monthly":            "On the 1st of every month at 00:00",
		"*-*-01 00:00:00":    "On the 1st of every month at 00:00",
		"yearly":             "On the 1st of January at 00:00",
		"*-01-01 00:00:00":   "On the 1st of January at 00:00",
		"quarterly":          "On the 1st of January, April, July and October at 00:00",
		"semiannually":       "On the 1st of January and July at 00:00",

		// Steps, spans and lists.
		"*-*-* *:00/5:00":         "Every 5 minutes",
		"*-*-* *:00/10:00":        "Every 10 minutes",
		"Mon..Fri *-*-* 09:00:00": "Monday to Friday at 09:00",
		"Mon,Fri *-*-* 06:15:00":  "Every Monday and Friday at 06:15",
		"*-*-* 02,14:00:00":       "Every day at 02:00 and 14:00",
		"*-*-01 04:30:00":         "On the 1st of every month at 04:30",

		// The idiom for "the first Saturday of the month": a weekday pinned
		// inside the first seven days.
		"Sat *-*-01..07 12:00:00": "On the first Saturday of the month at 12:00",

		// A date with no time is legal and means midnight, which is what
		// `systemd-analyze calendar '*-*-*'` normalizes it to.
		"*-*-*": "Every day at 00:00",
		"12:00": "Every day at 12:00",
	}
	for expression, want := range tests {
		if got := DescribeCalendar(expression); got != want {
			t.Errorf("DescribeCalendar(%q) =\n  %q\nwant\n  %q", expression, got, want)
		}
	}
}

// TestDescribeCalendarRefusesWhatItCannotRead: an empty reading is the honest
// answer, and the UI shows the expression alone rather than a guess.
func TestDescribeCalendarRefusesWhatItCannotRead(t *testing.T) {
	for _, expression := range []string{
		"", "nonsense here", "OnBootSec=10min", "99:00:00",
		"*-*-* 00:00:00 extra bits here",
	} {
		if got := DescribeCalendar(expression); got != "" {
			t.Errorf("DescribeCalendar(%q) = %q, want an empty reading",
				expression, got)
		}
	}
}

// TestDescribeCron pins the reading of the cron expressions a real machine
// carries. Two of them are lines from this Fedora host's own /etc/cron.d and
// /etc/anacrontab.
func TestDescribeCron(t *testing.T) {
	tests := map[string]string{
		"*/5 * * * *":  "Every 5 minutes",
		"*/15 * * * *": "Every 15 minutes",
		"* * * * *":    "Every minute",
		// /etc/cron.d/0hourly, as Fedora ships it.
		"01 * * * *":  "At 1 minute past every hour",
		"15 * * * *":  "At 15 minutes past every hour",
		"0 * * * *":   "Every hour, on the hour",
		"0 3 * * *":   "Every day at 03:00",
		"30 4 * * *":  "Every day at 04:30",
		"0 6 * * 1":   "Every Monday at 06:00",
		"0 6 * * mon": "Every Monday at 06:00",
		// cron accepts both 0 and 7 for Sunday, and they must not print as two
		// different days.
		"0 0 * * 0":    "Every Sunday at 00:00",
		"0 0 * * 7":    "Every Sunday at 00:00",
		"30 4 * * 1-5": "Monday to Friday at 04:30",
		"0 0 1 * *":    "On the 1st of every month at 00:00",
		"0 0 1 1 *":    "On the 1st of January at 00:00",
		"0 0 1 jan *":  "On the 1st of January at 00:00",
		"0 2,14 * * *": "Every day at 02:00 and 14:00",
		"0 0/6 * * *":  "Every 6 hours",

		// The macros.
		"@hourly":   "Every hour, on the hour",
		"@daily":    "Every day at 00:00",
		"@midnight": "Every day at 00:00",
		"@weekly":   "Every Sunday at 00:00",
		"@monthly":  "On the 1st of every month at 00:00",
		"@yearly":   "On the 1st of January at 00:00",
	}
	for expression, want := range tests {
		if got := DescribeCron(expression); got != want {
			t.Errorf("DescribeCron(%q) =\n  %q\nwant\n  %q", expression, got, want)
		}
	}
}

// TestDescribeCronNamesTheOrTrap: a line with both a day of the month and a
// weekday runs on either, not on both, and that is the single most commonly
// misread thing in a crontab. The reading has to say so.
func TestDescribeCronNamesTheOrTrap(t *testing.T) {
	got := DescribeCron("0 0 1 * 1")
	if !strings.Contains(got, "EITHER") {
		t.Errorf("DescribeCron(%q) = %q, want it to name cron's OR rule",
			"0 0 1 * 1", got)
	}
	// And a line with only one of the two must not carry the warning.
	if got := DescribeCron("0 0 1 * *"); strings.Contains(got, "EITHER") {
		t.Errorf("a line with no weekday carries the OR warning: %q", got)
	}
}

// TestDescribeCronReboot: @reboot has no clock in it at all, and saying "every
// day at 00:00" for it would be wrong in a way that matters.
func TestDescribeCronReboot(t *testing.T) {
	got := DescribeCron("@reboot")
	if !strings.Contains(got, "boot") {
		t.Errorf("DescribeCron(@reboot) = %q", got)
	}
}

func TestDescribeCronRefusesWhatItCannotRead(t *testing.T) {
	for _, expression := range []string{
		"", "* * * *", "* * * * * *", "@nope", "61 * * * *", "* 25 * * *",
		"* * 32 * *", "* * * 13 *", "* * * * 8", "a b c d e",
	} {
		if got := DescribeCron(expression); got != "" {
			t.Errorf("DescribeCron(%q) = %q, want an empty reading",
				expression, got)
		}
	}
}

// TestValidateCron is what a form and a table writer both call, and it has to
// agree with the describer: a line the describer cannot read must not reach a
// crontab.
func TestValidateCron(t *testing.T) {
	for _, good := range []string{
		"* * * * *", "*/5 * * * *", "0 3 * * *", "30 4 * * 1-5", "0 0 1 jan *",
		"@reboot", "@daily", "@WEEKLY",
	} {
		if err := ValidateCron(good); err != nil {
			t.Errorf("ValidateCron(%q) = %v, want it accepted", good, err)
		}
	}
	for _, bad := range []string{
		"", "* * * *", "61 * * * *", "@never", "* * * * 8", "*/0 * * * *",
	} {
		if err := ValidateCron(bad); err == nil {
			t.Errorf("ValidateCron(%q) accepted it", bad)
		}
	}
}

// TestCalendarInterval is what decides the Persistent warning: whether a timer
// fires daily or less often. It only has to be right about that threshold.
func TestCalendarInterval(t *testing.T) {
	tests := map[string]time.Duration{
		"*-*-* *:*:00":       time.Minute,
		"*-*-* *:00/5:00":    5 * time.Minute,
		"*-*-* *:00:00":      time.Hour,
		"*-*-* 00/6:00:00":   6 * time.Hour,
		"*-*-* 02,14:00:00":  time.Hour,
		"*-*-* 00:00:00":     24 * time.Hour,
		"Mon *-*-* 00:00:00": 24 * time.Hour,
		"*-*-01 04:30:00":    24 * time.Hour,
	}
	for expression, want := range tests {
		got, ok := CalendarInterval(expression)
		if !ok {
			t.Errorf("CalendarInterval(%q) could not be worked out", expression)
			continue
		}
		if got != want {
			t.Errorf("CalendarInterval(%q) = %s, want %s", expression, got, want)
		}
	}
	if _, ok := CalendarInterval("nonsense"); ok {
		t.Errorf("CalendarInterval read an expression that is not one")
	}
}

func TestCronInterval(t *testing.T) {
	tests := map[string]time.Duration{
		"* * * * *":   time.Minute,
		"*/5 * * * *": 5 * time.Minute,
		"0 * * * *":   time.Hour,
		"0 0/4 * * *": 4 * time.Hour,
		"0 3 * * *":   24 * time.Hour,
		"@daily":      24 * time.Hour,
	}
	for expression, want := range tests {
		got, ok := CronInterval(expression)
		if !ok || got != want {
			t.Errorf("CronInterval(%q) = %s (%v), want %s", expression, got, ok, want)
		}
	}
	// @reboot has no interval at all, and reporting one would be inventing it.
	if _, ok := CronInterval("@reboot"); ok {
		t.Errorf("CronInterval(@reboot) claimed an interval")
	}
}

// TestOrdinals covers the day-of-month suffixes, including the teens that every
// naive implementation gets wrong.
func TestOrdinals(t *testing.T) {
	tests := map[int]string{
		1: "1st", 2: "2nd", 3: "3rd", 4: "4th",
		11: "11th", 12: "12th", 13: "13th",
		21: "21st", 22: "22nd", 23: "23rd", 31: "31st",
	}
	for value, want := range tests {
		if got := ordinalLabel(value); got != want {
			t.Errorf("ordinalLabel(%d) = %q, want %q", value, got, want)
		}
	}
}
