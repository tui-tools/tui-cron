package timers

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tui-tools/tui-cron/internal/schedule"
)

// ParseProperties reads the `key=value` output of `systemctl show`.
//
// A property may repeat — a timer with two OnCalendar lines prints
// TimersCalendar twice — so the values are collected in order rather than
// overwritten, and the single-value accessor takes the first.
func ParseProperties(out string) Properties {
	properties := Properties{}
	for _, line := range strings.Split(out, "\n") {
		key, value, found := strings.Cut(strings.TrimRight(line, "\r"), "=")
		if !found {
			continue
		}
		properties[key] = append(properties[key], value)
	}
	return properties
}

// Properties is one unit's `systemctl show` output, parsed.
type Properties map[string][]string

// Get returns the first value of a property.
func (p Properties) Get(key string) string {
	if values := p[key]; len(values) > 0 {
		return values[0]
	}
	return ""
}

// All returns every value of a property, in the order systemd printed them.
func (p Properties) All(key string) []string { return p[key] }

// timerJSON is one entry of `systemctl list-timers --output=json`. Only the
// unit name is taken: everything else the screen shows is read again from
// `systemctl show`, which is where the calendar expression lives, and reading
// the same number twice from two sources is how a display disagrees with
// itself.
type timerJSON struct {
	Unit string `json:"unit"`
}

// ParseTimerListJSON reads the unit names out of
// `systemctl list-timers --all --output=json`.
func ParseTimerListJSON(out string) ([]string, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	var raw []timerJSON
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("timers: cannot read the timer list "+
			"(needs systemd 250 or newer for `list-timers --output=json`): %w", err)
	}
	units := make([]string, 0, len(raw))
	for _, entry := range raw {
		if entry.Unit == "" {
			continue
		}
		units = append(units, UnescapeUnitName(entry.Unit))
	}
	return units, nil
}

// ParseTimerListText reads the unit names out of
// `systemctl list-units --type=timer --all --plain --no-legend`.
//
// Only the first column is taken, and that is deliberate. The rest of that
// table is column-aligned against headers narrower than the data and has
// changed shape between systemd releases; the unit name is the one field whose
// position is a contract.
func ParseTimerListText(out string) []string {
	var units []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := UnescapeUnitName(strings.TrimPrefix(fields[0], "●"))
		if name == "" || !strings.HasSuffix(name, ".timer") {
			continue
		}
		units = append(units, name)
	}
	return units
}

// UnescapeUnitName reverses systemd's `\xNN` escaping, which shows up in the
// names of units generated from a path. An escape that does not parse is left
// exactly as it was: a name we cannot decode is still the name the user has to
// type.
func UnescapeUnitName(name string) string {
	if !strings.Contains(name, `\x`) {
		return name
	}
	var b strings.Builder
	b.Grow(len(name))
	for i := 0; i < len(name); {
		if name[i] == '\\' && i+3 < len(name) && name[i+1] == 'x' {
			if v, err := strconv.ParseUint(name[i+2:i+4], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 4
				continue
			}
		}
		b.WriteByte(name[i])
		i++
	}
	return b.String()
}

// JobFromTimer turns one timer's properties into a job.
func JobFromTimer(p Properties, kind schedule.Kind, owner string) schedule.Job {
	unit := p.Get("Id")
	if unit == "" {
		return schedule.Job{}
	}

	job := schedule.Job{
		ID:          string(kind) + ":" + unit,
		Name:        unit,
		Kind:        kind,
		Description: p.Get("Description"),
		Owner:       owner,
		Unit:        unit,
		Service:     p.Get("Unit"),
		File:        p.Get("FragmentPath"),
		State:       p.Get("ActiveState"),
		Active:      p.Get("ActiveState") == "active",
		Enabled:     strings.HasPrefix(p.Get("UnitFileState"), "enabled"),
		Raw:         strings.Join(rawTimerLines(p), "\n"),
	}

	calendars := CalendarsOf(p)
	monotonics := MonotonicsOf(p)
	switch {
	case len(calendars) > 0:
		job.Schedule = strings.Join(calendars, " and ")
		if len(calendars) == 1 {
			job.Explain = schedule.DescribeCalendar(calendars[0])
		}
	case len(monotonics) > 0:
		// A monotonic timer has no calendar at all: it fires a fixed time after
		// the boot, or after the unit it depends on last stopped. Saying so is
		// better than leaving the column blank, and neither the describer nor
		// the drop-in editor applies to it.
		job.Schedule = strings.Join(monotonics, " and ")
		job.Monotonic = true
		job.Explain = "Relative to an event, not to the clock: this timer has " +
			"no OnCalendar, so its schedule cannot be edited here"
	default:
		job.Schedule = "—"
		job.Monotonic = true
	}

	if value := p.Get("Persistent"); value != "" {
		job.Persistent, job.PersistentKnown = value == "yes", true
	}
	job.Next = ParseTimestamp(p.Get("NextElapseUSecRealtime"))
	job.Last = ParseTimestamp(p.Get("LastTriggerUSec"))
	if job.Next.IsZero() {
		job.NextNote = nextNote(p, job)
	}
	if job.Last.IsZero() {
		job.Outcome, job.OutcomeDetail = schedule.OutcomeNever,
			"this timer has not fired since the machine last booted"
	}
	return job
}

// nextNote explains a timer with no next run, which is never the same reason
// twice.
func nextNote(p Properties, job schedule.Job) string {
	switch {
	case p.Get("ActiveState") != "active":
		return "not armed: the timer unit is " + p.Get("ActiveState")
	case len(MonotonicsOf(p)) > 0:
		return "relative to an event that has not happened yet"
	default:
		_ = job
		return "systemd reports no next elapse for it"
	}
}

// rawTimerLines are the properties the detail screen shows verbatim, so a
// reader can see what was actually read rather than what was made of it.
func rawTimerLines(p Properties) []string {
	var lines []string
	for _, key := range []string{"TimersCalendar", "TimersMonotonic",
		"NextElapseUSecRealtime", "LastTriggerUSec", "Persistent",
		"AccuracyUSec", "RandomizedDelayUSec"} {
		for _, value := range p.All(key) {
			if value == "" {
				continue
			}
			lines = append(lines, key+"="+value)
		}
	}
	return lines
}

// CalendarsOf pulls the OnCalendar expressions out of the TimersCalendar
// property, which systemd prints as
// `{ OnCalendar=*-*-* 00:00:00 ; next_elapse=Mon 2026-08-31 00:00:00 -03 }`.
//
// The expression it prints is already normalized — a unit file saying
// `OnCalendar=daily` shows up here as `*-*-* 00:00:00` — which is why the
// describer only has to understand one grammar rather than every shorthand.
func CalendarsOf(p Properties) []string {
	var out []string
	for _, value := range p.All("TimersCalendar") {
		if expression, ok := braceField(value, "OnCalendar="); ok {
			out = append(out, expression)
		}
	}
	return out
}

// MonotonicsOf pulls the monotonic schedules out, which systemd prints as
// `{ OnBootUSec=10min ; next_elapse=0 }`.
func MonotonicsOf(p Properties) []string {
	var out []string
	for _, value := range p.All("TimersMonotonic") {
		body := strings.TrimSpace(strings.Trim(value, "{}"))
		for _, part := range strings.Split(body, ";") {
			part = strings.TrimSpace(part)
			if part == "" || strings.HasPrefix(part, "next_elapse=") {
				continue
			}
			out = append(out, part)
		}
	}
	return out
}

// braceField reads one `key=value` out of systemd's brace-delimited property
// syntax, where the fields are separated by ` ; ` and the value may itself
// contain spaces.
func braceField(value, key string) (string, bool) {
	body := strings.TrimSpace(strings.Trim(strings.TrimSpace(value), "{}"))
	for _, part := range strings.Split(body, ";") {
		part = strings.TrimSpace(part)
		if after, found := strings.CutPrefix(part, key); found {
			return strings.TrimSpace(after), true
		}
	}
	return "", false
}

// ApplyService folds the activated unit's properties into the job: what it
// runs, and how its last run ended.
func ApplyService(job *schedule.Job, p Properties) {
	if command, ok := braceField(p.Get("ExecStart"), "argv[]="); ok {
		job.Command = command
	} else if path, found := braceField(p.Get("ExecStart"), "path="); found {
		job.Command = path
	}
	if job.Description == "" {
		job.Description = p.Get("Description")
	}

	// A timer whose service has never run reports Result=success and status 0
	// too, which would read as a pass. LastTriggerUSec is what separates the
	// two, and the caller has already set OutcomeNever from it.
	if job.Outcome == schedule.OutcomeNever {
		return
	}
	result, status := p.Get("Result"), p.Get("ExecMainStatus")
	switch {
	case result == "" && status == "":
		job.Outcome = schedule.OutcomeUnknown
		job.OutcomeDetail = "systemd reports no result for " + job.Service
	case result == "success" && (status == "0" || status == ""):
		job.Outcome = schedule.OutcomeOK
		job.OutcomeDetail = "the last run exited 0"
	default:
		job.Outcome = schedule.OutcomeFailed
		job.OutcomeDetail = failureDetail(result, status)
	}
}

// failureDetail says what went wrong in systemd's own words.
func failureDetail(result, status string) string {
	detail := "Result=" + result
	if result == "" {
		detail = "the unit did not finish cleanly"
	}
	if status != "" && status != "0" {
		detail += ", exit status " + status
	}
	return detail
}

// ParseTimestamp reads the timestamps `systemctl show` prints, which are
// systemd's own rendering in the machine's local time —
// "Sun 2026-08-30 00:09:19 -03" — and the sentinels it uses for "never".
//
// Parsing it back rather than asking for the microsecond form is deliberate:
// the microsecond properties are the *monotonic* ones for a monotonic timer
// and mean nothing as a wall clock, while this string is the same one
// `systemctl status` shows, so a reader comparing the two sees one answer.
func ParseTimestamp(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" || value == "n/a" || value == "0" {
		return time.Time{}
	}
	for _, layout := range []string{
		"Mon 2006-01-02 15:04:05 -0700",
		"Mon 2006-01-02 15:04:05 MST",
		"Mon 2006-01-02 15:04:05",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// ParseNormalized reads the normalized form out of `systemd-analyze calendar`,
// which is the line systemd prints for what it understood the expression to be.
func ParseNormalized(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if after, found := strings.CutPrefix(strings.TrimSpace(line),
			"Normalized form:"); found {
			return strings.TrimSpace(after)
		}
	}
	return ""
}

// ParseElapses reads the next-run lines out of `systemd-analyze calendar
// --iterations=N`, which prints the first as "Next elapse:" and the rest as
// "Iteration #2:" and so on.
func ParseElapses(out string) []string {
	var elapses []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Next elapse:"):
			elapses = append(elapses,
				strings.TrimSpace(strings.TrimPrefix(line, "Next elapse:")))
		case strings.HasPrefix(line, "Iteration #"):
			_, value, found := strings.Cut(line, ":")
			if found {
				elapses = append(elapses, strings.TrimSpace(value))
			}
		}
	}
	return elapses
}
