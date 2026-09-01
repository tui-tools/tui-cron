package main

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/tui-tools/tui-cron/internal/schedule"
	"github.com/tui-tools/tui-kit/ui"
)

// Layout constants: the rows the table cannot use.
const (
	headerLines = 2
	footerLines = 2
	// tabLines is the one row the tab bar takes.
	tabLines = 1
	// minTableHeight keeps at least one visible row on a very short terminal.
	minTableHeight = 1
)

// tableHeight is the number of rows that fit on screen.
func (a *app) tableHeight() int {
	// header + tabs + table header + footer + status line.
	return max(a.height-headerLines-tabLines-footerLines-2, minTableHeight)
}

// detailHeight is the number of detail lines that fit on screen.
func (a *app) detailHeight() int {
	return max(a.height-headerLines-tabLines-footerLines-1, minTableHeight)
}

// View renders the whole screen.
func (a *app) View() string {
	switch a.mode {
	case modeConfirm:
		return a.confirm.View(a.theme, a.width, a.height)
	case modeFilter, modeTyped:
		return a.input.View(a.theme, a.width, a.height)
	case modePicker:
		return a.picker.View(a.theme, a.width, a.height)
	case modeForm:
		return a.form.view(a.theme, a.width, a.height)
	case modeHelp:
		return placeCenter(
			ui.HelpScreen(a.theme, "tui-cron — keys", helpKeys(), a.width),
			a.width, a.height)
	case modeDetail:
		return a.detailView()
	}
	return a.browseView()
}

// placeCenter centers a rendered box in the terminal.
func placeCenter(box string, width, height int) string {
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// browseView renders a screen: header, tab bar, table, help bar, status.
func (a *app) browseView() string {
	header := a.headerView()
	tabs := a.tabsView()

	var body string
	switch {
	case a.loading && a.rowCount() == 0:
		body = ui.EmptyState(a.theme, "reading the schedulers…", a.width, a.tableHeight()+1)
	case a.rowCount() == 0 && a.filter != "":
		body = ui.EmptyState(a.theme, "nothing matches "+strconv.Quote(a.filter),
			a.width, a.tableHeight()+1)
	case a.rowCount() == 0 && a.loadFailed:
		body = ui.EmptyState(a.theme,
			"could not read the schedulers — see the message below",
			a.width, a.tableHeight()+1)
	case a.rowCount() == 0:
		body = ui.EmptyState(a.theme, a.emptyMessage(), a.width, a.tableHeight()+1)
	default:
		body = a.table()
	}

	help := ui.HelpBar(a.theme, a.shortHelpKeys(), a.width)
	status := ui.StatusLine(a.theme, a.statusKind, a.status, a.defaultStatus(), a.width)
	return strings.Join([]string{header, tabs, body, help, status}, "\n")
}

// emptyMessage is what a screen with no rows says, which is different on each.
func (a *app) emptyMessage() string {
	switch a.screen {
	case screenTimers:
		if !a.model.Timers.Available {
			return "systemd could not be read: " + orNone(a.model.Timers.Detail)
		}
		return "this machine has no systemd timers at all, which is unusual"
	case screenCron:
		if !a.model.Cron.Installed {
			return orNone(a.model.Cron.Detail)
		}
		return "cron is installed and every one of its tables is empty"
	case screenSchedulers:
		return "neither scheduler could be read"
	default:
		return "nothing is scheduled on this machine"
	}
}

// headerView renders the facts at the top of every screen.
func (a *app) headerView() string {
	t := a.theme

	facts := []ui.Fact{{Label: "jobs", Value: strconv.Itoa(len(a.model.Jobs))}}

	// What failed comes first, because it is why anyone opened the tool.
	if count := len(a.model.Failed()); count > 0 {
		style := t.Danger
		facts = append(facts, ui.Fact{Label: "failed",
			Value: strconv.Itoa(count), Style: &style})
	}
	if count := len(a.model.NeedPersistent()); count > 0 {
		style := t.Warn
		facts = append(facts, ui.Fact{Label: "no Persistent",
			Value: strconv.Itoa(count), Style: &style})
	}

	// Whether cron is here at all, which decides how much of this screen is
	// the whole picture.
	cronValue, cronStyle := "running", t.OK
	switch {
	case !a.model.Cron.Installed:
		cronValue, cronStyle = "not installed", t.Muted
	case !a.model.Cron.Active:
		cronValue, cronStyle = orNone(a.model.Cron.State), t.Danger
	}
	facts = append(facts, ui.Fact{Label: "cron", Value: cronValue, Style: &cronStyle})

	// The scheduler versions, when they were probed: quiet on a tested version,
	// coloured on one nobody has run against.
	for _, result := range probed(a.backendCompat) {
		facts = append(facts, ui.CompatFact(t, result))
	}

	subtitle := a.backend.Describe()
	if a.filter != "" {
		subtitle += "  ·  filter: " + a.filter
	}
	return ui.Header{Title: "tui-cron", Subtitle: subtitle, Facts: facts}.
		Render(t, a.width)
}

// tabsView renders the four screens as one row, with the current one accented.
func (a *app) tabsView() string {
	var parts []string
	for s := screen(0); s < screenCount; s++ {
		label := strconv.Itoa(int(s)+1) + " " + s.title()
		if s == a.screen {
			parts = append(parts, a.theme.Accent.Render("["+label+"]"))
			continue
		}
		parts = append(parts, a.theme.Muted.Render(" "+label+" "))
	}
	return a.theme.Footer.Width(a.width).Render(
		ui.Truncate(strings.Join(parts, " "), a.width-2))
}

// defaultStatus is the hint shown when there is no message to report.
func (a *app) defaultStatus() string {
	count := strconv.Itoa(a.rowCount())
	suffix := "  ·  tab to move  ·  ? for help"
	switch a.screen {
	case screenTimers:
		return count + " systemd timers  ·  e changes one" + suffix
	case screenCron:
		return count + " cron jobs  ·  a adds a line, t converts one" + suffix
	case screenSchedulers:
		return "what systemd and cron say about themselves" + suffix
	default:
		return count + " scheduled jobs  ·  enter opens one" + suffix
	}
}

// table renders the current screen's rows.
func (a *app) table() string {
	columns, rows, styles := a.tableData()
	return ui.Table{
		Columns:  columns,
		Rows:     rows,
		Styles:   styles,
		Selected: a.cursor[a.screen],
		Offset:   a.offset[a.screen],
		Height:   a.tableHeight(),
	}.Render(a.theme, a.width)
}

// tableData builds the columns, cells and row styles of the current screen.
// Every screen drops its widest columns first on a narrow terminal, which is
// what keeps a 40-column pane readable.
func (a *app) tableData() ([]ui.Column, [][]string, []*lipgloss.Style) {
	switch a.screen {
	case screenTimers:
		return a.timersTable()
	case screenCron:
		return a.cronTable()
	case screenSchedulers:
		return a.schedulersTable()
	default:
		return a.allTable()
	}
}

// allTable is the merged list: the name, which scheduler it belongs to, the
// schedule as written, when it runs next, and how it went last time.
func (a *app) allTable() ([]ui.Column, [][]string, []*lipgloss.Style) {
	columns := []ui.Column{
		{Title: "JOB", Width: 22, Flex: true},
		{Title: "KIND", Width: 11},
		{Title: "SCHEDULE", Width: 18, Flex: true},
	}
	showNext := a.width >= 70
	showLast := a.width >= 96
	if showNext {
		columns = append(columns, ui.Column{Title: "NEXT", Width: 12})
	}
	if showLast {
		columns = append(columns, ui.Column{Title: "LAST", Width: 12})
	}
	columns = append(columns, ui.Column{Title: "", Width: 6})

	rows, styles := a.buildRows(func(job schedule.Job) []string {
		row := []string{job.Name, string(job.Kind), job.Schedule}
		if showNext {
			row = append(row, relative(job.Next, job.NextNote == ""))
		}
		if showLast {
			row = append(row, relative(job.Last, true))
		}
		return append(row, outcomeMark(job.Outcome))
	})
	return columns, rows, styles
}

// timersTable is the systemd list, with the two columns only a timer has: what
// it activates, and whether a missed run is caught up.
func (a *app) timersTable() ([]ui.Column, [][]string, []*lipgloss.Style) {
	columns := []ui.Column{
		{Title: "TIMER", Width: 24, Flex: true},
		{Title: "ONCALENDAR", Width: 20, Flex: true},
	}
	showNext := a.width >= 64
	showPersistent := a.width >= 82
	showActivates := a.width >= 104
	if showNext {
		columns = append(columns, ui.Column{Title: "NEXT", Width: 12})
	}
	if showPersistent {
		columns = append(columns, ui.Column{Title: "PERSIST", Width: 8})
	}
	if showActivates {
		columns = append(columns, ui.Column{Title: "ACTIVATES", Width: 22, Flex: true})
	}
	columns = append(columns, ui.Column{Title: "", Width: 6})

	rows, styles := a.buildRows(func(job schedule.Job) []string {
		row := []string{job.Name, job.Schedule}
		if showNext {
			row = append(row, relative(job.Next, job.NextNote == ""))
		}
		if showPersistent {
			row = append(row, persistentCell(job))
		}
		if showActivates {
			row = append(row, orNone(job.Service))
		}
		return append(row, outcomeMark(job.Outcome))
	})
	return columns, rows, styles
}

// cronTable is the cron list, with the two columns only a cron job has: whose
// table it is in, and where.
func (a *app) cronTable() ([]ui.Column, [][]string, []*lipgloss.Style) {
	columns := []ui.Column{
		{Title: "JOB", Width: 20, Flex: true},
		{Title: "SCHEDULE", Width: 16},
		{Title: "USER", Width: 10},
	}
	showWhere := a.width >= 76
	showCommand := a.width >= 100
	if showWhere {
		columns = append(columns, ui.Column{Title: "WHERE", Width: 24, Flex: true})
	}
	if showCommand {
		columns = append(columns, ui.Column{Title: "COMMAND", Width: 24, Flex: true})
	}
	columns = append(columns, ui.Column{Title: "", Width: 6})

	rows, styles := a.buildRows(func(job schedule.Job) []string {
		row := []string{job.Name, job.Schedule, orNone(job.Owner)}
		if showWhere {
			row = append(row, orNone(job.Where()))
		}
		if showCommand {
			row = append(row, orNone(job.Command))
		}
		return append(row, outcomeMark(job.Outcome))
	})
	return columns, rows, styles
}

// buildRows renders the current screen's jobs through a cell builder, with the
// row style each one's outcome earns.
func (a *app) buildRows(cells func(schedule.Job) []string) ([][]string,
	[]*lipgloss.Style) {
	list := a.rows[a.screen]
	rows := make([][]string, 0, len(list))
	styles := make([]*lipgloss.Style, 0, len(list))
	for _, job := range list {
		rows = append(rows, cells(job))
		styles = append(styles, a.jobStyle(job))
	}
	return rows, styles
}

// schedulerRow is one line of the schedulers screen: a fact and its value.
type schedulerRow struct{ label, value string }

// schedulerRows flattens what the two schedulers say about themselves, so the
// screen scrolls and filters like the other three.
func (a *app) schedulerRows() []schedulerRow {
	counts := a.model.Counts()
	rows := []schedulerRow{
		{"systemd", systemdCell(a.model)},
		{"user bus", userBusCell(a.model)},
		{"cron", cronCell(a.model)},
	}
	if a.model.Cron.Unit != "" {
		rows = append(rows, schedulerRow{"cron unit",
			a.model.Cron.Unit + " is " + orNone(a.model.Cron.State) +
				", " + enabledWord(a.model.Cron.Enabled)})
	}
	rows = append(rows, schedulerRow{"crontab read", "the crontab of " +
		a.model.User + ", plus every table under /etc"})

	for _, kind := range schedule.Kinds {
		rows = append(rows, schedulerRow{string(kind),
			strconv.Itoa(counts[kind]) + " " + plural(counts[kind], "job", "jobs")})
	}

	for _, job := range a.model.Failed() {
		rows = append(rows, schedulerRow{"failed",
			job.Name + ": " + orNone(job.OutcomeDetail)})
	}
	for _, job := range a.model.NeedPersistent() {
		rows = append(rows, schedulerRow{"no Persistent",
			job.Name + " runs daily or rarer and skips a run missed while the " +
				"machine was off"})
	}
	if a.model.Timers.Detail != "" {
		rows = append(rows, schedulerRow{"note", a.model.Timers.Detail})
	}
	if a.model.Cron.Detail != "" {
		rows = append(rows, schedulerRow{"note", a.model.Cron.Detail})
	}
	for _, result := range a.backendCompat {
		rows = append(rows, schedulerRow{"version",
			result.Backend + " " + orNone(result.Version) + " (" +
				result.Status.String() + ")"})
	}
	return rows
}

// systemdCell says whether the system manager answered.
func systemdCell(model schedule.Model) string {
	if model.Timers.Available {
		return "answering; its timers are on screen"
	}
	return "not read: " + orNone(model.Timers.Detail)
}

// userBusCell says whether the calling account's own manager answered, which on
// a machine reached over a console it will not have.
func userBusCell(model schedule.Model) string {
	if model.Timers.UserAvailable {
		return "answering; " + model.User + "'s own timers are on screen"
	}
	if model.Timers.UserDetail != "" {
		return "not read: " + model.Timers.UserDetail
	}
	return "not read"
}

// cronCell says whether cron is installed and running.
func cronCell(model schedule.Model) string {
	switch {
	case !model.Cron.Installed:
		return orNone(model.Cron.Detail)
	case model.Cron.Active:
		return "installed and running"
	default:
		return "installed, but " + orNone(model.Cron.State)
	}
}

// enabledWord renders the boot state in words.
func enabledWord(enabled bool) string {
	if enabled {
		return "enabled at boot"
	}
	return "not enabled at boot"
}

// plural picks a word for a count.
func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}

// schedulersTable renders those rows.
func (a *app) schedulersTable() ([]ui.Column, [][]string, []*lipgloss.Style) {
	columns := []ui.Column{
		{Title: "", Width: 14},
		{Title: "", Width: 40, Flex: true},
	}
	entries := a.schedulerRows()
	rows := make([][]string, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, []string{entry.label, entry.value})
	}
	return columns, rows, nil
}

// outcomeMark is the one-glyph outcome column. It is a symbol rather than a
// word because the column has to survive a 40-column terminal, and it is backed
// by the color of the row for anyone who cannot tell them apart.
func outcomeMark(outcome schedule.Outcome) string {
	switch outcome {
	case schedule.OutcomeFailed:
		return "fail"
	case schedule.OutcomeOK:
		return "ok"
	case schedule.OutcomeNever:
		return "—"
	default:
		return "?"
	}
}

// persistentCell renders the Persistent setting, with the warning case marked:
// a daily timer without it silently skips a run, which is the failure nobody
// notices.
func persistentCell(job schedule.Job) string {
	switch {
	case !job.PersistentKnown:
		return "—"
	case job.Persistent:
		return "yes"
	case job.NeedsPersistent():
		return "no !"
	default:
		return "no"
	}
}

// jobStyle colors a row by what happened to it, so what is wrong stands out
// from what merely exists.
func (a *app) jobStyle(job schedule.Job) *lipgloss.Style {
	var style lipgloss.Style
	switch {
	case job.Failed():
		style = a.theme.Row.Foreground(a.theme.Danger.GetForeground())
	case job.NeedsPersistent():
		style = a.theme.Row.Foreground(a.theme.Warn.GetForeground())
	case !job.Enabled:
		style = a.theme.Row.Foreground(a.theme.Muted.GetForeground())
	case job.Outcome == schedule.OutcomeOK:
		style = a.theme.Row.Foreground(a.theme.OK.GetForeground())
	default:
		style = a.theme.Row
	}
	return &style
}

// relative renders a moment as how far away it is, which is what a schedule
// screen is actually asked. The absolute time is on the detail screen.
func relative(when time.Time, known bool) string {
	if when.IsZero() || !known {
		return "—"
	}
	delta := time.Until(when)
	future := delta >= 0
	if !future {
		delta = -delta
	}
	unit := shortDuration(delta)
	if future {
		return "in " + unit
	}
	return unit + " ago"
}

// shortDuration renders a duration in the one unit that fits a column.
func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "min"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	case d < 365*24*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + "d"
	default:
		return strconv.Itoa(int(d.Hours()/24/365)) + "y"
	}
}

// detailView renders the selected row in full.
func (a *app) detailView() string {
	header := a.headerView()
	tabs := a.tabsView()
	lines := a.detailLines()

	height := a.detailHeight()
	offset := min(a.detailOffset, max(len(lines)-height, 0))
	a.detailOffset = offset
	end := min(offset+height, len(lines))

	body := make([]string, 0, height)
	for _, line := range lines[offset:end] {
		body = append(body, a.theme.Row.Width(a.width).Render(
			ui.Truncate(line, a.width-2)))
	}
	for i := len(body); i < height; i++ {
		body = append(body, a.theme.Row.Width(a.width).Render(""))
	}

	help := ui.HelpBar(a.theme, a.shortHelpKeys(), a.width)
	position := strconv.Itoa(offset+1) + "–" + strconv.Itoa(end) +
		" of " + strconv.Itoa(len(lines)) + " lines  ·  esc to go back"
	status := ui.StatusLine(a.theme, a.statusKind, a.status, position, a.width)
	return strings.Join([]string{header, tabs,
		strings.Join(body, "\n"), help, status}, "\n")
}

// detailLines builds the detail screen's text: what the job is, what its
// schedule means, the file it is written in, the runs it has ahead of it, and
// what its log says.
func (a *app) detailLines() []string {
	job := a.detail.job
	if job.ID == "" {
		return []string{"(nothing selected)"}
	}

	lines := []string{
		job.Name,
		"",
		"  kind           " + string(job.Kind),
		"  schedule       " + job.Schedule,
	}
	if job.Explain != "" {
		lines = append(lines, "  reads as       "+job.Explain)
	} else {
		lines = append(lines, "  reads as       "+
			"(this tool cannot read that expression; the scheduler still can)")
	}
	lines = append(lines,
		"  runs           "+orNone(job.Command),
		"  as             "+orNone(job.Owner),
		"  written at     "+orNone(job.Where()),
		"  next run       "+absolute(job.Next, job.NextNote),
		"  last run       "+absolute(job.Last, "it has not run"),
		"  last result    "+orNone(string(job.Outcome))+
			resultDetail(job),
		"  enabled        "+yesNo(job.Enabled)+"   state "+orNone(job.State))
	if job.Kind.Systemd() {
		lines = append(lines, "  activates      "+orNone(job.Service),
			"  Persistent     "+persistentDetail(job))
	}
	if job.Description != "" {
		lines = append(lines, "", "  "+job.Description)
	}

	if a.detail.loading {
		lines = append(lines, "", "reading the unit, the log and the next runs…")
		return lines
	}

	lines = append(lines, a.elapsesSection()...)
	lines = append(lines, a.definitionSection()...)
	lines = append(lines, a.journalSection()...)
	lines = append(lines, "", a.actionHint(job))
	return lines
}

// elapsesSection is the next few runs, as the scheduler itself computes them.
func (a *app) elapsesSection() []string {
	lines := []string{"", "The next runs"}
	if a.detail.elapsesErr != "" {
		return append(lines, "  "+a.detail.elapsesErr)
	}
	if len(a.detail.elapses) == 0 {
		return append(lines, "  (none were computed)")
	}
	for _, elapse := range a.detail.elapses {
		lines = append(lines, "  "+elapse)
	}
	return append(lines,
		"  computed by systemd itself, not by this tool")
}

// definitionSection is the unit file, or the table the line lives in.
func (a *app) definitionSection() []string {
	lines := []string{"", "As it is written"}
	if a.detail.definitionErr != "" {
		return append(lines, "  "+a.detail.definitionErr)
	}
	for _, line := range strings.Split(
		strings.TrimSuffix(a.detail.definition, "\n"), "\n") {
		lines = append(lines, "  "+line)
	}
	return lines
}

// journalSection is what the job's own log says.
func (a *app) journalSection() []string {
	lines := []string{"", "What the log says"}
	if a.detail.journalErr != "" {
		return append(lines, "  "+a.detail.journalErr)
	}
	if strings.TrimSpace(a.detail.journal) == "" {
		if a.detail.job.Kind.Cron() {
			return append(lines,
				"  cron logged nothing for this job in the last week.",
				"  cron records that a command started, and cronie also that it",
				"  returned; neither records an exit status. Whatever the job",
				"  printed went to the mail cron sends, not to the journal.")
		}
		return append(lines, "  the journal has nothing for this unit")
	}
	for _, line := range strings.Split(
		strings.TrimSuffix(a.detail.journal, "\n"), "\n") {
		lines = append(lines, "  "+line)
	}
	return lines
}

// actionHint is the last line of the detail screen: what can be done to this
// particular job, which is not the same for a timer and a crontab line.
func (a *app) actionHint(job schedule.Job) string {
	switch {
	case job.Kind == schedule.KindAnacronDir:
		return "  the directory is the schedule: to change it, move the file"
	case job.Kind.Systemd() && job.ToolWritten:
		return "  e changes the schedule or the command · d removes both files · " +
			"E/D enable · s/x arm · n runs it now"
	case job.Kind.Systemd():
		return "  e changes the schedule · E/D enable · s/x arm · n runs it now"
	default:
		return "  e changes it · d removes it · t writes a timer for it · a adds one"
	}
}

// absolute renders a moment in full, or the reason there is none.
func absolute(when time.Time, note string) string {
	if when.IsZero() {
		return "— (" + orNone(note) + ")"
	}
	return when.Local().Format("Mon 2006-01-02 15:04:05 MST") +
		"   (" + relative(when, true) + ")"
}

// resultDetail appends the scheduler's own words for an outcome.
func resultDetail(job schedule.Job) string {
	if job.OutcomeDetail == "" {
		return ""
	}
	return " — " + job.OutcomeDetail
}

// persistentDetail says what the setting means for this timer, rather than
// repeating the value that is already in the column.
func persistentDetail(job schedule.Job) string {
	switch {
	case !job.PersistentKnown:
		return "— (systemd reported none)"
	case job.Persistent:
		return "true — a run missed while the machine was off is caught up"
	case job.NeedsPersistent():
		return "false — and this timer runs daily or rarer, so a run missed " +
			"while the machine was off is skipped, silently"
	default:
		return "false — it fires often enough that a missed run is along shortly"
	}
}

// yesNo renders a boolean in words.
func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

// orNone renders an empty value as a visible placeholder, so a blank line is
// never mistaken for a missing read.
func orNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

// dialogDiffLines is the most diff the confirm dialog will show. The kit's
// dialog does not scroll, so a diff longer than the terminal would push its own
// title and the command preview off the screen — and the command preview is the
// one thing that must never be missed.
const dialogDiffLines = 14

// diffForDialog trims a diff to what fits above the command preview, saying how
// much was left out.
func (a *app) diffForDialog(diff string) string {
	budget := max(min(a.height-16, dialogDiffLines), 4)
	lines := strings.Split(strings.TrimSuffix(diff, "\n"), "\n")
	if len(lines) <= budget {
		return diff
	}
	kept := append([]string{}, lines[:budget]...)
	return strings.Join(kept, "\n") + "\n… " +
		strconv.Itoa(len(lines)-budget) + " more diff lines"
}

// shortHelpKeys is the single-line hint bar, which changes with the screen
// because the keys that do anything change with it.
func (a *app) shortHelpKeys() []ui.KeyHint {
	hints := []ui.KeyHint{{Key: "tab", Desc: "screen"}, {Key: "enter", Desc: "detail"}}
	switch a.screen {
	case screenTimers:
		hints = append(hints,
			ui.KeyHint{Key: "e", Desc: "schedule"},
			ui.KeyHint{Key: "d", Desc: "delete ours"},
			ui.KeyHint{Key: "n", Desc: "run now"},
			ui.KeyHint{Key: "E/D", Desc: "enable"})
	case screenCron:
		hints = append(hints,
			ui.KeyHint{Key: "a", Desc: "add"},
			ui.KeyHint{Key: "e", Desc: "edit"},
			ui.KeyHint{Key: "d", Desc: "delete"},
			ui.KeyHint{Key: "t", Desc: "to timer"})
	case screenSchedulers:
		hints = append(hints, ui.KeyHint{Key: "c", Desc: "new timer"})
	default:
		hints = append(hints,
			ui.KeyHint{Key: "e", Desc: "schedule"},
			ui.KeyHint{Key: "c", Desc: "new timer"})
	}
	return append(hints,
		ui.KeyHint{Key: "/", Desc: "filter"},
		ui.KeyHint{Key: "?", Desc: "help"},
		ui.KeyHint{Key: "q", Desc: "quit"})
}

// helpKeys is the full key list shown on the help screen.
func helpKeys() []ui.KeyHint {
	return []ui.KeyHint{
		{Key: "tab / 1-4", Desc: "all jobs, timers, cron, schedulers"},
		{Key: "↑/k, ↓/j", Desc: "move the selection, or scroll the detail screen"},
		{Key: "g / G", Desc: "first / last row"},
		{Key: "pgup/pgdn", Desc: "scroll a page"},
		{Key: "enter", Desc: "open the selected job: its unit or table, the next runs, its log"},
		{Key: "esc", Desc: "leave the detail screen"},
		{Key: "/", Desc: "filter this screen (esc clears)"},
		{Key: "e", Desc: "change when the selected job runs — and what it runs, for a timer this tool wrote"},
		{Key: "a", Desc: "add a cron line: your own table, or as root another account's or /etc/cron.d"},
		{Key: "d", Desc: "remove the selected cron line, or a timer this tool wrote — both its files"},
		{Key: "c", Desc: "create a systemd timer and its service"},
		{Key: "t", Desc: "write a timer for the selected cron line, not enabled"},
		{Key: "E / D", Desc: "enable / disable the selected timer at boot"},
		{Key: "s / x", Desc: "arm / disarm the selected timer now"},
		{Key: "n", Desc: "run the selected job now, off its schedule"},
		{Key: "R", Desc: "re-read both schedulers"},
		{Key: "?", Desc: "this help"},
		{Key: "q", Desc: "quit"},
		{Key: "", Desc: ""},
		{Key: "note", Desc: "every change is previewed and confirmed first"},
		{Key: "note", Desc: "a timer's schedule is changed with a drop-in; its unit file is never rewritten"},
		{Key: "note", Desc: "only a timer whose files this tool wrote can be deleted or re-pointed; the rest belong to their packages"},
		{Key: "note", Desc: "deleting a timer asks for its name to be typed, because a removed unit file does not come back"},
		{Key: "note", Desc: "a crontab is replaced through `crontab <file>`, which is cron's own interface"},
	}
}
