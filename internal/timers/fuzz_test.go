package timers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tui-tools/tui-cron/internal/schedule"
)

// Everything this package reads is printed by a systemd that may be older or
// newer than the one this was written against: `systemctl show` properties,
// two shapes of timer list, and `systemd-analyze calendar`. Each parser turns
// that into a unit name the tool will pass to systemctl and a schedule it
// prints as fact, so each carries a fuzz target. `go test` replays every seed
// below on each commit, and
// `go test -run=^$ -fuzz=FuzzParseProperties ./internal/timers/` explores past
// them locally. See tui-kit/templates/FUZZING.md for the family rule.
//
// The seeds are the fixtures the table tests already use, plus the shapes a
// real capture never has: nothing, a lone separator, a truncated line.

// seed adds every named testdata file to the corpus.
func seed(f *testing.F, names ...string) {
	f.Helper()
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // the name is a literal in the tests, and testdata is in the repository
		if err != nil {
			f.Fatalf("read fixture %s: %v", name, err)
		}
		f.Add(string(raw))
	}
	f.Add("")
	f.Add("\n\n\n")
	f.Add("=")
	f.Add("{")
	f.Add(`\x`)
}

func FuzzParseProperties(f *testing.F) {
	seed(f, "show-logrotate-timer.txt", "show-logrotate-service.txt",
		"show-monotonic-timer.txt", "systemctl-version.txt")
	f.Fuzz(func(t *testing.T, out string) {
		properties := ParseProperties(out)
		for key, values := range properties {
			// A property is split at the first `=`, so a key can never carry
			// one, and the single-value accessor has to agree with the list.
			if strings.ContainsAny(key, "=\n") {
				t.Fatalf("property key carries a separator: %q", key)
			}
			if len(values) == 0 {
				t.Fatalf("property %q has an empty value list", key)
			}
			if got := properties.Get(key); got != values[0] {
				t.Fatalf("Get(%q) = %q, want the first value %q", key, got, values[0])
			}
		}

		// JobFromTimer is where these properties become the row the list
		// shows, so it belongs in the target: a job built from anything must
		// either be the zero job or be complete enough to render.
		job := JobFromTimer(properties, schedule.KindTimer, "root")
		if job.Name == "" {
			if job != (schedule.Job{}) {
				t.Fatalf("a job with no name is not the zero job: %+v", job)
			}
			return
		}
		switch {
		case job.Unit != job.Name:
			t.Fatalf("job names %q but its unit is %q", job.Name, job.Unit)
		case job.ID != string(schedule.KindTimer)+":"+job.Unit:
			t.Fatalf("job id %q does not name its kind and unit", job.ID)
		case job.Schedule == "":
			t.Fatalf("job %q has no schedule at all", job.ID)
		}
		// A monotonic timer has no calendar to describe; a calendar one is
		// described, and the describer must survive whatever systemd printed.
		for _, calendar := range CalendarsOf(properties) {
			_ = schedule.DescribeCalendar(calendar)
		}
	})
}

func FuzzParseTimerListJSON(f *testing.F) {
	seed(f, "list-timers.json")
	f.Add("[]")
	f.Add(`[{"unit":""}]`)
	f.Fuzz(func(t *testing.T, out string) {
		units, err := ParseTimerListJSON(out)
		if err != nil {
			// A list that did not parse is not a partial list: nothing here
			// may reach a systemctl argv.
			if len(units) != 0 {
				t.Fatalf("returned %d units alongside an error: %v", len(units), err)
			}
			return
		}
		for _, unit := range units {
			if unit == "" {
				t.Fatalf("returned a blank unit name from %q", out)
			}
		}
	})
}

func FuzzParseTimerListText(f *testing.F) {
	seed(f, "list-units-timer.txt")
	f.Add("● stale.timer loaded active waiting")
	f.Fuzz(func(t *testing.T, out string) {
		for _, unit := range ParseTimerListText(out) {
			// This list is the fallback for a systemd too old for the JSON
			// output, and every name in it is passed to `systemctl show`: only
			// timers, and never a blank.
			switch {
			case unit == "":
				t.Fatalf("returned a blank unit name")
			case !strings.HasSuffix(unit, ".timer"):
				t.Fatalf("returned %q, which is not a timer", unit)
			}
		}
	})
}

func FuzzParseUnitFileList(f *testing.F) {
	seed(f, "list-unit-files-timer.txt")
	seed(f, "list-unit-files-timer-user.txt")
	f.Add("weird@.timer static -")
	f.Add("aliased.timer alias -")
	f.Fuzz(func(t *testing.T, out string) {
		for _, unit := range ParseUnitFileList(out) {
			// Every name here is passed to `systemctl show`, so the same rule
			// applies as to the loaded lists, plus one: a template is not a
			// unit and must never reach that argv.
			switch {
			case unit == "":
				t.Fatalf("returned a blank unit name")
			case !strings.HasSuffix(unit, ".timer"):
				t.Fatalf("returned %q, which is not a timer", unit)
			case strings.Contains(unit, "@."):
				t.Fatalf("returned the template %q", unit)
			}
		}
	})
}

func FuzzParseTimestamp(f *testing.F) {
	f.Add("Sun 2026-08-30 00:09:19 -03")
	f.Add("Sun 2026-08-30 00:09:19 UTC")
	f.Add("Sun 2026-08-30 00:09:19")
	f.Add("n/a")
	f.Add("0")
	f.Add("")
	f.Add("  Mon 2026-08-31 00:00:00 -03  ")
	f.Fuzz(func(t *testing.T, value string) {
		parsed := ParseTimestamp(value)
		// The screen reads a zero time as "no next run", so the sentinels
		// systemd uses for "never" have to land on exactly that.
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || trimmed == "n/a" || trimmed == "0" {
			if !parsed.IsZero() {
				t.Fatalf("%q parsed to %s, want the zero time", value, parsed)
			}
			return
		}
		// systemd pads the field it prints, and the caller hands over
		// whatever `systemctl show` gave: surrounding space cannot change the
		// answer.
		if again := ParseTimestamp(trimmed); !again.Equal(parsed) {
			t.Fatalf("%q parsed to %s but %q parsed to %s", value, parsed,
				trimmed, again)
		}
	})
}

func FuzzParseNormalized(f *testing.F) {
	seed(f, "analyze-calendar.txt", "analyze-calendar-shorthand.txt")
	f.Add("Normalized form: *-*-* 00:00:00")
	f.Fuzz(func(t *testing.T, out string) {
		normalized := ParseNormalized(out)
		if normalized == "" {
			return
		}
		// What comes back is offered as the expression a unit file will carry,
		// so it is a trimmed piece of the output and nothing invented.
		switch {
		case normalized != strings.TrimSpace(normalized):
			t.Fatalf("normalized form kept its padding: %q", normalized)
		case !strings.Contains(out, normalized):
			t.Fatalf("normalized form %q is not in the output", normalized)
		}
		_ = schedule.DescribeCalendar(normalized)
	})
}

func FuzzParseElapses(f *testing.F) {
	seed(f, "analyze-calendar.txt", "analyze-calendar-shorthand.txt")
	f.Add("  Next elapse: Mon 2026-08-31 00:00:00 -03\nIteration #2: x")
	f.Fuzz(func(t *testing.T, out string) {
		for _, elapse := range ParseElapses(out) {
			// Each line is shown as a date the timer will fire, so it is a
			// trimmed piece of the output too.
			if elapse != strings.TrimSpace(elapse) {
				t.Fatalf("elapse kept its padding: %q", elapse)
			}
			if elapse != "" && !strings.Contains(out, elapse) {
				t.Fatalf("elapse %q is not in the output", elapse)
			}
		}
	})
}
