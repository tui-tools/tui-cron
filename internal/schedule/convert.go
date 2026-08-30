package schedule

import (
	"fmt"
	"strconv"
	"strings"
)

// CalendarFromCron translates a cron expression into the OnCalendar that fires
// at the same moments, and refuses when there is no such expression.
//
// The refusals are the useful part. Two cron expressions have no OnCalendar
// equivalent at all, and a converter that produced something plausible for
// them would produce a timer that runs at different times from the cron line it
// replaced — which is the one failure a conversion must not have:
//
//   - `@reboot` is not a calendar. systemd spells it OnBootSec=, which is a
//     different setting in the same section, and guessing a delay for it would
//     be inventing a schedule.
//   - a line with both a day of the month and a weekday runs on either in cron
//     and on both in systemd. `0 0 1 * 1` fires on the 1st and on every Monday
//     under cron, and only on a Monday the 1st under systemd. There is no
//     single OnCalendar for it; two of them would be needed, and this tool does
//     not silently split one job into two.
func CalendarFromCron(expression string) (string, error) {
	expression = strings.TrimSpace(expression)
	if strings.EqualFold(expression, "@reboot") {
		return "", fmt.Errorf(
			"@reboot has no OnCalendar: systemd spells it `OnBootSec=`, which " +
				"needs a delay you choose rather than one this tool invents")
	}
	if expanded, ok := cronMacros[strings.ToLower(expression)]; ok {
		expression = expanded
	}
	fields, err := cronFields(expression)
	if err != nil {
		return "", fmt.Errorf("%q is not a cron schedule: %w", expression, err)
	}
	minute, hour, dom, month, dow :=
		fields[0], fields[1], fields[2], fields[3], foldWeekdays(fields[4])

	if !dom.spansEverything() && !dow.spansEverything() {
		return "", fmt.Errorf(
			"this line sets both a day of the month and a weekday, and cron " +
				"runs it on EITHER while systemd would run it only when both " +
				"match — there is no single OnCalendar that means the same thing")
	}

	date := renderCalendarField(month, 2) + "-" + renderCalendarField(dom, 2)
	clock := renderCalendarField(hour, 2) + ":" +
		renderCalendarField(minute, 2) + ":00"

	out := "*-" + date + " " + clock
	if !dow.spansEverything() {
		out = renderWeekdays(dow) + " " + out
	}
	return out, nil
}

// renderCalendarField writes a field back out in systemd's syntax: `..` for a
// range where cron writes `-`, and every value zero-padded the way systemd's
// own normalized form pads them.
func renderCalendarField(f field, pad int) string {
	if f.any {
		return "*"
	}
	parts := make([]string, 0, len(f.items))
	for _, it := range f.items {
		var part string
		switch {
		// A stepped whole field is written from its first value, not from `*`.
		// systemd spells "every five minutes" `*:00/5:00` and rejects
		// `*:*/5:00` outright, which is the one place cron's syntax and its own
		// look alike and are not.
		case it.step > 0 && it.from == f.min && it.to == f.max:
			part = padded(it.from, pad)
		case it.hasTo && it.from == f.min && it.to == f.max:
			part = "*"
		case it.hasTo:
			part = padded(it.from, pad) + ".." + padded(it.to, pad)
		default:
			part = padded(it.from, pad)
		}
		if it.step > 0 {
			part += "/" + strconv.Itoa(it.step)
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ",")
}

// padded zero-pads a number to the width systemd prints.
func padded(value, width int) string {
	return fmt.Sprintf("%0*d", width, value)
}

// renderWeekdays writes a weekday field as the three-letter names systemd
// takes, as a span when the days are consecutive and as a list otherwise.
func renderWeekdays(dow field) string {
	values := dow.values()
	short := func(v int) string { return weekdayLabels[v%7][:3] }
	if len(values) > 2 && isRun(values) {
		return short(values[0]) + ".." + short(values[len(values)-1])
	}
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, short(v))
	}
	return strings.Join(parts, ",")
}
