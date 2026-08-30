package schedule

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// This file turns a schedule expression into a sentence a person can check.
//
// It is the reason the tool exists. `17 3 * * 1` and `*-*-01..07 12:00:00` are
// both perfectly precise and neither is readable, and the mistakes people make
// with them — a job that runs every minute of one hour instead of once, a
// "monthly" backup that fires on the first of every month *and* every Monday —
// are mistakes of reading, not of typing. So the expression is never rewritten
// for display; the English sits next to it, and the reader compares the two.
//
// Both grammars are handled by one parser over a "field": a comma-separated
// list of numbers, ranges and steps, or `*`. cron writes a range `1-5` and
// systemd writes it `1..5`; that separator is the only difference between them
// at this level, and it is a parameter.
//
// Nothing here shells out. Validating an OnCalendar expression against systemd
// itself is a different question with a different answer — `systemd-analyze
// calendar` is what the backend runs for that, and for the next elapses — and
// this describer never claims to be that check. A form asks both: this one so
// the user can read what they typed, and systemd's so the machine agrees.

// item is one element of a field: a single value, a range, or either with a
// step.
type item struct {
	from, to int
	// hasTo reports a range rather than a single value.
	hasTo bool
	// step is the stride, 0 when none was written.
	step int
}

// field is one position of an expression: the minutes, the hours, the weekdays.
type field struct {
	// any reports the bare `*`, which matches every value.
	any bool
	// items are the written elements, in the order they were written.
	items []item
	// text is the field exactly as it appeared, for a fallback message.
	text string
	// min and max bound the field, so `*` can be enumerated.
	min, max int
}

// parseField reads one field. rangeSep is "-" for cron and ".." for systemd.
func parseField(text, rangeSep string, min, max int, names map[string]int) (field, error) {
	f := field{text: text, min: min, max: max}
	if text == "" {
		return f, fmt.Errorf("empty field")
	}
	for _, part := range strings.Split(text, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return f, fmt.Errorf("empty element in %q", text)
		}

		var it item
		body := part
		if slash := strings.LastIndex(part, "/"); slash >= 0 {
			step, err := strconv.Atoi(part[slash+1:])
			if err != nil || step <= 0 {
				return f, fmt.Errorf("%q is not a step", part[slash+1:])
			}
			it.step = step
			body = part[:slash]
		}

		switch body {
		case "*":
			it.from, it.to, it.hasTo = min, max, true
			// A bare `*` with no step is the whole field; only then does the
			// field itself count as "any", because `*/5` is not every value.
			if it.step == 0 && len(strings.Split(text, ",")) == 1 {
				f.any = true
			}
		default:
			from, to, hasTo, err := parseRange(body, rangeSep, min, max, names)
			if err != nil {
				return f, err
			}
			it.from, it.to, it.hasTo = from, to, hasTo
			// `0/6` is a start and a stride, not a single value: both cron and
			// systemd read it as "from 0 to the end of the field, every 6".
			// Leaving it as one value would make `0 0/6 * * *` look like a job
			// that runs once a day at midnight.
			if it.step > 0 && !hasTo {
				it.to = max
			}
		}
		f.items = append(f.items, it)
	}
	return f, nil
}

// parseRange reads `5`, `1-5` or `1..5`, with names resolved.
func parseRange(body, rangeSep string, min, max int,
	names map[string]int) (from, to int, hasTo bool, err error) {
	if idx := strings.Index(body, rangeSep); idx > 0 {
		from, err = parseValue(body[:idx], min, max, names)
		if err != nil {
			return 0, 0, false, err
		}
		to, err = parseValue(body[idx+len(rangeSep):], min, max, names)
		if err != nil {
			return 0, 0, false, err
		}
		return from, to, true, nil
	}
	from, err = parseValue(body, min, max, names)
	if err != nil {
		return 0, 0, false, err
	}
	return from, from, false, nil
}

// parseValue reads one number or name and checks it against the field's range.
func parseValue(text string, min, max int, names map[string]int) (int, error) {
	text = strings.TrimSpace(text)
	if value, ok := names[strings.ToLower(text)]; ok {
		return value, nil
	}
	value, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("%q is not a value this field takes", text)
	}
	if value < min || value > max {
		return 0, fmt.Errorf("%d is outside %d-%d", value, min, max)
	}
	return value, nil
}

// values enumerates everything the field matches, sorted and deduplicated.
func (f field) values() []int {
	seen := map[int]bool{}
	var out []int
	for _, it := range f.items {
		step := max(it.step, 1)
		for v := it.from; v <= it.to; v += step {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	sort.Ints(out)
	return out
}

// step reports the stride of a field written as a single step element
// (`*/5`, `00/5`, `0-59/5`), and whether it is one.
func (f field) step() (int, bool) {
	if len(f.items) != 1 || f.items[0].step == 0 {
		return 0, false
	}
	return f.items[0].step, true
}

// fixed reports the single value a field pins, when it pins exactly one.
func (f field) fixed() (int, bool) {
	if f.any {
		return 0, false
	}
	values := f.values()
	if len(values) != 1 {
		return 0, false
	}
	return values[0], true
}

// isZero reports a field pinned to its own minimum, which for seconds and
// minutes is the "on the dot" case that needs no words of its own.
func (f field) isZero() bool {
	value, ok := f.fixed()
	return ok && value == f.min
}

// spansEverything reports a field that matches every value it could, whether it
// was written `*` or spelled out.
func (f field) spansEverything() bool {
	return f.any || len(f.values()) == f.max-f.min+1
}

// The month and weekday names both grammars accept.
var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

// weekdayNames maps every spelling either scheduler accepts onto Go's own
// numbering, where Sunday is 0. cron also accepts 7 for Sunday, which is why
// the field runs 0-7 and is folded below.
var weekdayNames = map[string]int{
	"sun": 0, "sunday": 0, "mon": 1, "monday": 1, "tue": 2, "tuesday": 2,
	"wed": 3, "wednesday": 3, "thu": 4, "thursday": 4, "fri": 5, "friday": 5,
	"sat": 6, "saturday": 6,
}

// monthLabels and weekdayLabels are what the sentences print.
var monthLabels = []string{"", "January", "February", "March", "April", "May",
	"June", "July", "August", "September", "October", "November", "December"}

var weekdayLabels = []string{"Sunday", "Monday", "Tuesday", "Wednesday",
	"Thursday", "Friday", "Saturday"}

// cronMacros are the `@` shorthands cron accepts, mapped to the five-field
// expression they stand for. `@reboot` has no expression at all and is handled
// separately.
var cronMacros = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

// calendarMacros are systemd's named schedules, mapped to the normalized form
// `systemd-analyze calendar` prints for each. They are written out rather than
// computed so the describer sees the same string for `daily` that it sees for a
// timer systemd already normalized — which is what `systemctl show` reports.
var calendarMacros = map[string]string{
	"minutely":      "*-*-* *:*:00",
	"hourly":        "*-*-* *:00:00",
	"daily":         "*-*-* 00:00:00",
	"monthly":       "*-*-01 00:00:00",
	"weekly":        "Mon *-*-* 00:00:00",
	"yearly":        "*-01-01 00:00:00",
	"annually":      "*-01-01 00:00:00",
	"quarterly":     "*-01,04,07,10-01 00:00:00",
	"semiannually":  "*-01,07-01 00:00:00",
	"semi-annually": "*-01,07-01 00:00:00",
}

// DescribeCron turns a five-field cron expression, or one of the `@` macros,
// into English. It returns an empty string for anything it cannot read, which
// the UI shows as the expression alone rather than as a guess.
func DescribeCron(expression string) string {
	expression = strings.TrimSpace(expression)
	if strings.EqualFold(expression, "@reboot") {
		return "At boot, once, and never again until the next one"
	}
	if expanded, ok := cronMacros[strings.ToLower(expression)]; ok {
		expression = expanded
	}

	parts, err := cronFields(expression)
	if err != nil {
		return ""
	}
	minute, hour, dom, month, dow := parts[0], parts[1], parts[2], parts[3], parts[4]

	sentence := phrase(minute, hour, dom, month, foldWeekdays(dow))
	if sentence == "" {
		return ""
	}
	// cron's one genuine trap: when both the day-of-month and the day-of-week
	// are restricted, it runs on either, not on both. A "first Monday of the
	// month" written that way fires on the 1st and on every Monday.
	if !dom.spansEverything() && !dow.spansEverything() {
		sentence += ". Both a day of the month and a weekday are set, and cron " +
			"runs the job on EITHER of them, not only when they coincide"
	}
	return sentence
}

// cronFields splits and parses the five fields.
func cronFields(expression string) ([]field, error) {
	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return nil, fmt.Errorf("a cron schedule has 5 fields, not %d", len(fields))
	}
	specs := []struct {
		min, max int
		names    map[string]int
	}{
		{0, 59, nil}, {0, 23, nil}, {1, 31, nil}, {1, 12, monthNames}, {0, 7, weekdayNames},
	}
	out := make([]field, 0, 5)
	for i, text := range fields {
		f, err := parseField(text, "-", specs[i].min, specs[i].max, specs[i].names)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// foldWeekdays collapses cron's two spellings of Sunday onto one, so 0 and 7
// do not print as two different days.
func foldWeekdays(f field) field {
	if f.max != 7 {
		return f
	}
	f.max = 6
	for i := range f.items {
		// A bare 7 is Sunday, so it folds onto 0 at both ends of a
		// single-value item. Only the end of a real range moves to 6: `1-7`
		// means Monday through Sunday, and clamping its start would turn it
		// into the whole week.
		if !f.items[i].hasTo {
			if f.items[i].from == 7 {
				f.items[i].from, f.items[i].to = 0, 0
			}
			continue
		}
		if f.items[i].from == 7 {
			f.items[i].from = 0
		}
		if f.items[i].to == 7 {
			f.items[i].to = 6
		}
	}
	return f
}

// DescribeCalendar turns a systemd OnCalendar expression into English. It
// accepts the named shorthands and the normalized `[DOW ]Y-M-D H:M:S` form that
// `systemctl show` reports, and returns an empty string for anything else.
func DescribeCalendar(expression string) string {
	dow, date, clock, ok := calendarParts(expression)
	if !ok {
		return ""
	}
	// A calendar expression's seconds field has no cron equivalent, so it is
	// folded into the time phrase separately; everything else lines up.
	sentence := phrase(clock[1], clock[0], date[2], date[1], dow)
	if sentence == "" {
		return ""
	}
	if second, fixed := clock[2].fixed(); fixed && second != 0 {
		sentence += fmt.Sprintf(" and %d seconds", second)
	} else if !clock[2].isZero() && !clock[2].spansEverything() {
		sentence += ", at seconds " + list(clock[2].values(), plainLabel)
	}
	if year, fixed := date[0].fixed(); fixed {
		sentence += fmt.Sprintf(", in %d only", year)
	}
	return sentence
}

// calendarParts splits an OnCalendar expression into its weekday field, its
// three date fields and its three clock fields.
func calendarParts(expression string) (dow field, date, clock [3]field, ok bool) {
	expression = strings.TrimSpace(expression)
	if expanded, found := calendarMacros[strings.ToLower(expression)]; found {
		expression = expanded
	}

	tokens := strings.Fields(expression)
	if len(tokens) == 0 || len(tokens) > 3 {
		return dow, date, clock, false
	}
	// The weekday prefix is optional, and is the only token that carries no
	// separator of its own.
	dow = field{any: true, min: 0, max: 6, text: "*"}
	if !strings.ContainsAny(tokens[0], "-:") {
		parsed, err := parseField(tokens[0], "..", 0, 6, weekdayNames)
		if err != nil {
			return dow, date, clock, false
		}
		dow, tokens = parsed, tokens[1:]
	}

	// What is left is a date, a time, or both. A single token is whichever one
	// it looks like, so `12:00:00` and `*-*-01` are each complete on their own.
	dateText, clockText := "", ""
	for _, token := range tokens {
		switch {
		case strings.Contains(token, ":"):
			clockText = token
		case strings.Contains(token, "-"):
			dateText = token
		default:
			return dow, date, clock, false
		}
	}
	if dateText == "" {
		dateText = "*-*-*"
	}
	if clockText == "" {
		clockText = "00:00:00"
	}

	dateFields := strings.Split(dateText, "-")
	if len(dateFields) != 3 {
		return dow, date, clock, false
	}
	clockFields := strings.Split(clockText, ":")
	if len(clockFields) == 2 {
		clockFields = append(clockFields, "00")
	}
	if len(clockFields) != 3 {
		return dow, date, clock, false
	}

	bounds := [3][2]int{{1970, 2200}, {1, 12}, {1, 31}}
	for i, text := range dateFields {
		var names map[string]int
		if i == 1 {
			names = monthNames
		}
		parsed, err := parseField(text, "..", bounds[i][0], bounds[i][1], names)
		if err != nil {
			return dow, date, clock, false
		}
		date[i] = parsed
	}
	clockBounds := [3][2]int{{0, 23}, {0, 59}, {0, 59}}
	for i, text := range clockFields {
		parsed, err := parseField(text, "..", clockBounds[i][0], clockBounds[i][1], nil)
		if err != nil {
			return dow, date, clock, false
		}
		clock[i] = parsed
	}
	return dow, date, clock, true
}

// phrase assembles the sentence both grammars share: when in the day, and on
// which days.
func phrase(minute, hour, dom, month, dow field) string {
	day := dayPhrase(dom, month, dow)
	if day == "" {
		return ""
	}

	// Sub-daily schedules read better as an interval than as a time, and the
	// day clause becomes a qualifier rather than the subject.
	if hour.spansEverything() {
		var head string
		switch {
		case minute.spansEverything():
			head = "Every minute"
		case func() bool { _, ok := minute.step(); return ok }():
			step, _ := minute.step()
			head = fmt.Sprintf("Every %d minutes", step)
		case minute.isZero():
			head = "Every hour, on the hour"
		default:
			head = "At " + list(minute.values(), minutesPastLabel) + " past every hour"
		}
		if day == "Every day" {
			return head
		}
		return head + ", " + lowerFirst(day)
	}

	if step, ok := hour.step(); ok && minute.isZero() {
		head := fmt.Sprintf("Every %d hours", step)
		if day == "Every day" {
			return head
		}
		return head + ", " + lowerFirst(day)
	}

	times := clockTimes(hour, minute)
	if times == "" {
		return ""
	}
	return day + " at " + times
}

// clockTimes renders the hour and minute fields as `HH:MM` times. It refuses
// anything that would produce a long list, because a sentence naming forty
// times is not a sentence anybody reads.
func clockTimes(hour, minute field) string {
	hours, minutes := hour.values(), minute.values()
	if len(hours) == 0 || len(minutes) == 0 || len(hours)*len(minutes) > 6 {
		return ""
	}
	var out []string
	for _, h := range hours {
		for _, m := range minutes {
			out = append(out, fmt.Sprintf("%02d:%02d", h, m))
		}
	}
	sort.Strings(out)
	return join(out)
}

// dayPhrase says which days the job runs on, as the subject of the sentence.
func dayPhrase(dom, month, dow field) string {
	everyDay := dom.spansEverything() && month.spansEverything()
	everyWeekday := dow.spansEverything()

	switch {
	case everyDay && everyWeekday:
		return "Every day"

	case everyDay:
		return weekdayPhrase(dow)

	case !everyWeekday && isFirstWeek(dom) && month.spansEverything():
		// The systemd idiom for "the first Monday of the month": a weekday
		// pinned inside the first seven days.
		return "On the first " + list(dow.values(), weekdayLabel) + " of the month"

	case !everyWeekday:
		return weekdayPhrase(dow) + " that also falls " + lowerFirst(datePhrase(dom, month))

	default:
		return datePhrase(dom, month)
	}
}

// weekdayPhrase renders a weekday field: a run of days as a span, anything else
// as a list.
func weekdayPhrase(dow field) string {
	values := dow.values()
	if len(values) == 0 {
		return ""
	}
	if len(values) > 2 && isRun(values) {
		return weekdayLabels[values[0]] + " to " + weekdayLabels[values[len(values)-1]]
	}
	return "Every " + list(values, weekdayLabel)
}

// datePhrase renders the day-of-month and month fields.
func datePhrase(dom, month field) string {
	switch {
	case dom.spansEverything() && !month.spansEverything():
		return "Every day of " + list(month.values(), monthLabel)
	case month.spansEverything():
		return "On the " + list(dom.values(), ordinalLabel) + " of every month"
	default:
		return "On the " + list(dom.values(), ordinalLabel) + " of " +
			list(month.values(), monthLabel)
	}
}

// isFirstWeek reports a day-of-month field pinned to the first seven days,
// which is how both schedulers spell "the first <weekday> of the month".
func isFirstWeek(dom field) bool {
	values := dom.values()
	if len(values) != 7 {
		return false
	}
	for i, v := range values {
		if v != i+1 {
			return false
		}
	}
	return true
}

// isRun reports consecutive values, which read better as a span than as a list.
func isRun(values []int) bool {
	for i := 1; i < len(values); i++ {
		if values[i] != values[i-1]+1 {
			return false
		}
	}
	return true
}

// The label functions each field type prints its values with.
func weekdayLabel(v int) string { return weekdayLabels[v%7] }
func monthLabel(v int) string   { return monthLabels[v] }
func plainLabel(v int) string   { return strconv.Itoa(v) }

// minutesPastLabel renders a minute for the "past every hour" phrasing, where
// the singular matters: "at 1 minute past", not "at 1 minutes past".
func minutesPastLabel(v int) string {
	if v == 1 {
		return "1 minute"
	}
	return strconv.Itoa(v) + " minutes"
}

// ordinalLabel renders a day of the month as "1st", "22nd", "3rd".
func ordinalLabel(v int) string {
	suffix := "th"
	if v%100 < 11 || v%100 > 13 {
		switch v % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(v) + suffix
}

// list renders values through a label function and joins them as English.
func list(values []int, label func(int) string) string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, label(v))
	}
	return join(out)
}

// join is the English comma list: "a", "a and b", "a, b and c".
func join(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}

// lowerFirst lowercases the first letter, for a clause that has become a
// qualifier rather than the start of a sentence.
func lowerFirst(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// CronInterval is the shortest gap between two runs of a cron expression, and
// whether it could be worked out at all.
//
// It is deliberately coarse. The only question anyone asks of it is whether a
// job runs several times a day or once a day at most, and computing the exact
// minimum gap of `0 0 1,15 * *` would be precision nobody uses.
func CronInterval(expression string) (time.Duration, bool) {
	expression = strings.TrimSpace(expression)
	if strings.EqualFold(expression, "@reboot") {
		return 0, false
	}
	if expanded, ok := cronMacros[strings.ToLower(expression)]; ok {
		expression = expanded
	}
	fields, err := cronFields(expression)
	if err != nil {
		return 0, false
	}
	return interval(fields[0], fields[1], fields[2], fields[3], foldWeekdays(fields[4])), true
}

// CalendarInterval is the same for an OnCalendar expression.
func CalendarInterval(expression string) (time.Duration, bool) {
	dow, date, clock, ok := calendarParts(expression)
	if !ok {
		return 0, false
	}
	if clock[2].spansEverything() {
		return time.Second, true
	}
	return interval(clock[1], clock[0], date[2], date[1], dow), true
}

// interval is the shared coarse minimum: a minute or a step of minutes when
// the hour is unrestricted, an hour or a step of hours when the day is, and a
// day otherwise.
func interval(minute, hour, dom, month, dow field) time.Duration {
	if hour.spansEverything() {
		if minute.spansEverything() {
			return time.Minute
		}
		if step, ok := minute.step(); ok {
			return time.Duration(step) * time.Minute
		}
		if len(minute.values()) > 1 {
			return time.Minute
		}
		return time.Hour
	}
	if step, ok := hour.step(); ok {
		return time.Duration(step) * time.Hour
	}
	if len(hour.values()) > 1 {
		return time.Hour
	}
	_ = dom
	_ = month
	_ = dow
	return 24 * time.Hour
}

// ValidateCron checks a cron line's schedule the way cron itself would, because
// there is no portable command that will.
//
// cronie ships `crontab -T`, which parses a file and says whether it is good.
// Debian's vixie cron does not: it has no such flag, and no version flag
// either. A tool that relied on `-T` would validate on Fedora and silently skip
// the check on Ubuntu, which is worse than not having one — so the check is
// here, in Go, and runs identically everywhere.
func ValidateCron(expression string) error {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return fmt.Errorf("a cron schedule cannot be empty")
	}
	if strings.HasPrefix(expression, "@") {
		if strings.EqualFold(expression, "@reboot") {
			return nil
		}
		if _, ok := cronMacros[strings.ToLower(expression)]; ok {
			return nil
		}
		return fmt.Errorf("%q is not a cron macro (@reboot, @hourly, @daily, "+
			"@weekly, @monthly, @yearly)", expression)
	}
	if _, err := cronFields(expression); err != nil {
		return fmt.Errorf("%q is not a cron schedule: %w", expression, err)
	}
	return nil
}
