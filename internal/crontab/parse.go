package crontab

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tui-tools/tui-cron/internal/schedule"
)

// ParseUserTable reads one account's crontab, which has no user field: every
// line runs as the account that owns the table.
func ParseUserTable(text, owner, path string) []schedule.Job {
	return parseTable(text, schedule.KindCrontab, owner, path, false)
}

// ParseSystemTable reads /etc/crontab or a file from /etc/cron.d, which carry
// an extra user field between the schedule and the command.
func ParseSystemTable(text, path string) []schedule.Job {
	return parseTable(text, schedule.KindCronD, "", path, true)
}

// parseTable walks a table's lines.
//
// Comments, blank lines and environment assignments are skipped rather than
// reported. They are not jobs, and a screen that listed `MAILTO=root` as
// something with a schedule would be listing something that has none — but the
// detail screen shows the whole file, so nothing is hidden.
func parseTable(text string, kind schedule.Kind, owner, path string,
	withUser bool) []schedule.Job {
	var jobs []schedule.Job
	for i, raw := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if isAssignment(line) {
			continue
		}
		job, ok := ParseLine(line, kind, owner, path, i+1, withUser)
		if !ok {
			continue
		}
		jobs = append(jobs, job)
	}
	return jobs
}

// isAssignment reports a `NAME=value` environment line, which cron allows above
// the jobs and which is not one.
//
// The `=` has to come before any whitespace: `0 3 * * * FOO=bar /usr/bin/x` is a
// job whose command sets a variable, not an assignment.
func isAssignment(line string) bool {
	equals := strings.IndexByte(line, '=')
	if equals <= 0 {
		return false
	}
	name := strings.TrimSpace(line[:equals])
	return name != "" && !strings.ContainsAny(name, " \t")
}

// ParseLine reads one job line into a job, and reports whether it was one.
//
// The schedule is either an `@` macro, which is a single token, or five
// whitespace-separated fields. Everything after that is the user (in a system
// table) and then the command, which keeps its own spacing.
func ParseLine(line string, kind schedule.Kind, owner, path string, number int,
	withUser bool) (schedule.Job, bool) {
	expression, rest, ok := splitSchedule(line)
	if !ok {
		return schedule.Job{}, false
	}
	if withUser {
		field, remainder, found := cutField(rest)
		if !found || !validUser(field) {
			return schedule.Job{}, false
		}
		owner, rest = field, remainder
	}
	command := strings.TrimSpace(rest)
	if command == "" {
		return schedule.Job{}, false
	}
	if schedule.ValidateCron(expression) != nil {
		return schedule.Job{}, false
	}

	job := schedule.Job{
		ID:       string(kind) + ":" + path + ":" + strconv.Itoa(number),
		Name:     jobName(command, owner),
		Kind:     kind,
		Schedule: expression,
		Explain:  schedule.DescribeCron(expression),
		Command:  command,
		Owner:    owner,
		File:     path,
		Line:     number,
		Enabled:  true,
		Active:   true,
		State:    "installed",
		Raw:      line,
	}
	if strings.EqualFold(expression, "@reboot") {
		job.NextNote = "at the next boot, and cron does not compute a date for it"
	} else {
		job.NextNote = "cron does not publish a next run; the schedule is the answer"
	}
	job.Outcome = schedule.OutcomeUnknown
	job.OutcomeDetail = "cron's log has no line for this job in the last week"
	return job, true
}

// splitSchedule separates the schedule from the rest of the line.
func splitSchedule(line string) (expression, rest string, ok bool) {
	if strings.HasPrefix(line, "@") {
		expression, rest, _ = cutField(line)
		return expression, rest, rest != ""
	}
	for range 5 {
		var field string
		field, line, ok = cutField(line)
		if !ok {
			return "", "", false
		}
		if expression == "" {
			expression = field
			continue
		}
		expression += " " + field
	}
	return expression, line, line != ""
}

// cutField takes the first whitespace-separated field off a line, returning it
// and everything after the whitespace that followed it.
func cutField(line string) (field, rest string, ok bool) {
	line = strings.TrimLeft(line, " \t")
	if line == "" {
		return "", "", false
	}
	end := strings.IndexAny(line, " \t")
	if end < 0 {
		return line, "", true
	}
	return line[:end], strings.TrimLeft(line[end:], " \t"), true
}

// jobName is what the list shows for a cron line: the program it runs, with the
// account in front of it.
//
// The whole command would be a column nothing else fits beside — a cron line is
// routinely two hundred characters of redirections — and the command is on the
// detail screen and in the filter either way.
func jobName(command, owner string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return owner
	}
	program := filepath.Base(fields[0])
	// A line that starts with an environment assignment or an interpreter says
	// more about itself in its second word.
	if len(fields) > 1 && (strings.Contains(fields[0], "=") ||
		program == "sh" || program == "bash" || program == "env") {
		program += " " + filepath.Base(strings.TrimPrefix(fields[1], "-c"))
	}
	if owner == "" {
		return program
	}
	return owner + " · " + program
}

// runPartsSchedules is what each run-parts directory means, and how often it
// runs. The words are anacron's, because on a machine with anacron installed —
// which is every Fedora and Debian desktop — anacron rather than cron is what
// actually runs these, and it catches up a missed day rather than skipping it.
var runPartsSchedules = map[string]struct{ schedule, explain string }{
	"/etc/cron.hourly":  {"@hourly", "Every hour, run by cron through /etc/cron.d/0hourly on Fedora and by run-parts on Debian"},
	"/etc/cron.daily":   {"@daily", "Once a day, run by anacron where it is installed, which catches up a day the machine was off rather than skipping it"},
	"/etc/cron.weekly":  {"@weekly", "Once a week, run by anacron where it is installed"},
	"/etc/cron.monthly": {"@monthly", "Once a month, run by anacron where it is installed"},
}

// JobFromAnacronDir turns one executable in a run-parts directory into a job.
func JobFromAnacronDir(dir, name string) schedule.Job {
	path := filepath.Join(dir, name)
	spec := runPartsSchedules[dir]
	return schedule.Job{
		ID:       string(schedule.KindAnacronDir) + ":" + path,
		Name:     filepath.Base(dir) + " · " + name,
		Kind:     schedule.KindAnacronDir,
		Schedule: spec.schedule,
		Explain:  spec.explain,
		Command:  path,
		Owner:    "root",
		File:     path,
		Enabled:  true,
		Active:   true,
		State:    "installed",
		NextNote: "the directory is the schedule; nothing publishes a next run",
		Outcome:  schedule.OutcomeUnknown,
		OutcomeDetail: "run-parts runs this with the rest of " +
			filepath.Base(dir) + " and records nothing per script",
		Raw: path,
	}
}

// LogLine is one line of cron's own log, classified.
type LogLine struct {
	// When is the timestamp journalctl printed, zero when it did not parse.
	When time.Time
	// Owner is the account in cron's `(root)` prefix.
	Owner string
	// Kind is "start" for a CMD line, "end" for cronie's CMDEND, and "error"
	// for a line cron logged as a problem.
	Kind string
	// Command is the command cron named, empty on a line that names none.
	Command string
	// Raw is the line itself.
	Raw string
}

// The kinds a log line can carry.
const (
	LogStart = "start"
	LogEnd   = "end"
	LogError = "error"
)

// ParseCronLog classifies the lines cron wrote.
//
// The formats are cronie's and vixie's, which agree on the part that matters:
// `(user) CMD (the command)`. cronie adds a `CMDEND` line when the command
// returns, and that pairing is the only thing in either implementation that
// says a job finished — neither records an exit status anywhere.
func ParseCronLog(out string) []LogLine {
	var lines []LogLine
	for _, raw := range strings.Split(out, "\n") {
		raw = strings.TrimRight(raw, "\r")
		if strings.TrimSpace(raw) == "" {
			continue
		}
		line := LogLine{Raw: raw, When: parseJournalTime(raw)}
		switch {
		case strings.Contains(raw, " CMDEND ("):
			line.Kind, line.Command = LogEnd, parenPayload(raw, " CMDEND (")
		case strings.Contains(raw, " CMD ("):
			line.Kind, line.Command = LogStart, parenPayload(raw, " CMD (")
		case strings.Contains(raw, "(CRON) error") ||
			strings.Contains(raw, "(CRON) ERROR"):
			line.Kind = LogError
		default:
			continue
		}
		line.Owner = ownerOf(raw)
		lines = append(lines, line)
	}
	return lines
}

// parseJournalTime reads the leading timestamp of a `-o short-iso` line.
func parseJournalTime(raw string) time.Time {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return time.Time{}
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05-0700", "2006-01-02T15:04:05-07:00",
		"2006-01-02T15:04:05Z07:00",
	} {
		if parsed, err := time.Parse(layout, fields[0]); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// parenPayload returns what is inside the parentheses that follow a marker.
func parenPayload(raw, marker string) string {
	_, rest, found := strings.Cut(raw, marker)
	if !found {
		return ""
	}
	if end := strings.LastIndexByte(rest, ')'); end >= 0 {
		return rest[:end]
	}
	return rest
}

// ownerOf reads the account out of cron's `(root)` prefix, which sits between
// the pid and the marker.
func ownerOf(raw string) string {
	_, rest, found := strings.Cut(raw, "]: (")
	if !found {
		return ""
	}
	owner, _, found := strings.Cut(rest, ")")
	if !found || !validUser(owner) {
		return ""
	}
	return owner
}

// ApplyCronLog folds cron's log into the jobs: when each last started, and
// whatever cron was willing to say about how it ended.
//
// What it can say is limited, and the model says so rather than guessing. cron
// records that a command started, and cronie also records that it returned; the
// exit status is nowhere. A job whose command failed every night for a month
// looks exactly like one that worked, unless the command itself said otherwise
// — which is why the detail screen names the mail cron would have sent.
func ApplyCronLog(jobs []schedule.Job, lines []LogLine) {
	for i := range jobs {
		job := &jobs[i]
		if !job.Kind.Cron() {
			continue
		}
		var started, ended *LogLine
		failed := false
		for j := range lines {
			line := &lines[j]
			if !logMatches(*line, *job) {
				continue
			}
			switch line.Kind {
			case LogStart:
				started = line
			case LogEnd:
				ended = line
			case LogError:
				failed = true
			}
		}
		if started == nil {
			continue
		}
		job.Last = started.When
		switch {
		case failed:
			job.Outcome = schedule.OutcomeFailed
			job.OutcomeDetail = "cron logged an error for this job"
		case ended != nil:
			job.Outcome = schedule.OutcomeOK
			job.OutcomeDetail = "cron logged the command starting and returning; " +
				"it records no exit status, so this means it ran, not that it worked"
		default:
			job.Outcome = schedule.OutcomeUnknown
			job.OutcomeDetail = "cron logged the command starting and nothing since"
		}
	}
}

// logMatches reports whether a log line is about a job.
//
// The join is on the command text, which is what cron prints, because there is
// no id to join on: cron has no notion of a job that outlives the line it is
// written on. The account has to agree too, so root's copy of a command is not
// credited to a user's.
func logMatches(line LogLine, job schedule.Job) bool {
	if line.Command == "" {
		return strings.Contains(line.Raw, job.Command)
	}
	if line.Owner != "" && job.Owner != "" && line.Owner != job.Owner {
		return false
	}
	return strings.TrimSpace(line.Command) == strings.TrimSpace(job.Command)
}

// FilterLog keeps the lines of cron's log that name one job, newest last, and
// at most the requested number.
func FilterLog(out string, job schedule.Job, limit int) string {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var kept []string
	for _, line := range ParseCronLog(out) {
		if logMatches(line, job) {
			kept = append(kept, line.Raw)
		}
	}
	if len(kept) > limit {
		kept = kept[len(kept)-limit:]
	}
	return strings.Join(kept, "\n")
}

// SortJobs orders the cron jobs the way the list shows them: by the file they
// live in, then by the line inside it, so a table reads top to bottom.
func SortJobs(jobs []schedule.Job) {
	sort.SliceStable(jobs, func(i, j int) bool {
		if jobs[i].File != jobs[j].File {
			return jobs[i].File < jobs[j].File
		}
		return jobs[i].Line < jobs[j].Line
	})
}
