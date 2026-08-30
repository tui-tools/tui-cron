package crontab

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tui-tools/tui-cron/internal/schedule"
)

// A crontab is a file this tool did not write, edited by hand for decades on
// machines nobody here has seen, and cron's journal is whatever cronie or
// vixie chose to print. Both arrive as bytes and leave as a job the list shows
// and a command the detail screen names, so both are fuzzed: `go test` replays
// every seed below on each commit, and
// `go test -run=^$ -fuzz=FuzzParseUserTable ./internal/crontab/` explores past
// them locally. See tui-kit/templates/FUZZING.md for the family rule.
//
// The seeds are the fixtures the table tests already use, so the corpus starts
// on real line shapes and mutates from there, plus the shapes a real capture
// never has: nothing, a lone separator, a truncated line.

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
	f.Add("#")
	f.Add("*")
	f.Add("@reboot")
	f.Add("* * * * *")
	f.Add("MAILTO=root\n0 3 * * * /usr/bin/x\n")
	f.Add("0 3 * * * root")
}

// checkJobs asserts what every reader of a parsed table is allowed to assume:
// each job carries an identity, a schedule cron itself would accept, and a
// command, and it came from a line that is really in the text.
//
// The describer and the cron-to-calendar converter run here too. They are what
// the parsed expression is fed to — the detail screen prints one, the "convert
// to a timer" path builds a unit out of the other — so a table this parser
// accepted must not be a table they choke on.
func checkJobs(t *testing.T, text string, jobs []schedule.Job, kind schedule.Kind, path string) {
	t.Helper()
	previous := 0
	for _, job := range jobs {
		switch {
		case job.Kind != kind:
			t.Fatalf("job %q has kind %q, want %q", job.ID, job.Kind, kind)
		case job.File != path:
			t.Fatalf("job %q claims file %q, want %q", job.ID, job.File, path)
		case job.ID == "" || job.Name == "":
			t.Fatalf("job with no identity: %+v", job)
		case job.Schedule == "" || job.Command == "":
			t.Fatalf("job %q has no schedule or no command: %+v", job.ID, job)
		case job.Line <= previous:
			t.Fatalf("job %q is on line %d, after line %d", job.ID, job.Line, previous)
		case !strings.Contains(text, job.Raw):
			t.Fatalf("job %q kept a raw line the input does not contain: %q",
				job.ID, job.Raw)
		}
		previous = job.Line

		if err := schedule.ValidateCron(job.Schedule); err != nil {
			t.Fatalf("job %q kept an expression cron would reject: %q: %v",
				job.ID, job.Schedule, err)
		}
		_ = schedule.DescribeCron(job.Schedule)
		if calendar, err := schedule.CalendarFromCron(job.Schedule); err == nil {
			if strings.TrimSpace(calendar) == "" {
				t.Fatalf("job %q converted to a blank calendar: %q",
					job.ID, job.Schedule)
			}
			_ = schedule.DescribeCalendar(calendar)
		}
	}
}

func FuzzParseUserTable(f *testing.F) {
	seed(f, "user-crontab.txt", "etc-crontab.txt")
	f.Fuzz(func(t *testing.T, text string) {
		jobs := ParseUserTable(text, "ana", TablePathFor("ana"))
		checkJobs(t, text, jobs, schedule.KindCrontab, TablePathFor("ana"))
		for _, job := range jobs {
			// A user table has no user field, so every line runs as the
			// account that owns it and nothing in the file may say otherwise.
			if job.Owner != "ana" {
				t.Fatalf("job %q is owned by %q, want the table's owner",
					job.ID, job.Owner)
			}
		}
	})
}

func FuzzParseSystemTable(f *testing.F) {
	seed(f, "etc-crontab.txt", "cron.d-0hourly.txt", "cron.d-tui.txt")
	f.Fuzz(func(t *testing.T, text string) {
		const path = "/etc/cron.d/fuzz"
		jobs := ParseSystemTable(text, path)
		checkJobs(t, text, jobs, schedule.KindCronD, path)
		for _, job := range jobs {
			// The owner comes out of the file's user field, and it is what a
			// `crontab -u` or a `su` would be built around: a name the file
			// invented has to be rejected at the parse, not later.
			if !validUser(job.Owner) {
				t.Fatalf("job %q took %q as an account name", job.ID, job.Owner)
			}
		}
	})
}

func FuzzParseCronLog(f *testing.F) {
	seed(f, "journal-cron.txt", "journal-crond-fedora44-boot.txt")
	f.Fuzz(func(t *testing.T, out string) {
		lines := ParseCronLog(out)
		for _, line := range lines {
			switch line.Kind {
			case LogStart, LogEnd, LogError:
			default:
				t.Fatalf("line classified as %q: %q", line.Kind, line.Raw)
			}
			if strings.TrimSpace(line.Raw) == "" {
				t.Fatalf("kept a blank line")
			}
			if !strings.Contains(out, line.Raw) {
				t.Fatalf("kept a line the input does not contain: %q", line.Raw)
			}
			// The owner is read out of cron's `(root)` prefix and is joined
			// against a job's owner, so it is an account name or nothing.
			if line.Owner != "" && !validUser(line.Owner) {
				t.Fatalf("line %q gave %q as an account name", line.Raw, line.Owner)
			}
		}

		// FilterLog is what the detail screen shows, and it selects from the
		// same lines: whatever it returns has to be lines that were really in
		// the log, and never more than the limit asked for.
		job := schedule.Job{Kind: schedule.KindCrontab, Owner: "root",
			Command: "/usr/bin/x"}
		kept := FilterLog(out, job, 10)
		if kept == "" {
			return
		}
		shown := strings.Split(kept, "\n")
		if len(shown) > 10 {
			t.Fatalf("asked for 10 lines, got %d", len(shown))
		}
		for _, line := range shown {
			if !strings.Contains(out, line) {
				t.Fatalf("showed a line the input does not contain: %q", line)
			}
		}
	})
}
