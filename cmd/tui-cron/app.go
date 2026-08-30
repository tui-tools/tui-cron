package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-cron/internal/jobs"
	"github.com/tui-tools/tui-cron/internal/schedule"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-kit/ui"
)

// screen is one of the four views the tool is made of. They are tabs rather
// than nested screens because they answer four separate questions about the
// same machine, and a reader arrives with one of them already in mind.
type screen int

const (
	// screenAll is the merged list, which is the reason the tool exists.
	screenAll screen = iota
	// screenTimers and screenCron are the same rows with the columns each
	// scheduler actually has: a timer has a next run and a Persistent setting,
	// a cron line has an owner and a file and a line number.
	screenTimers
	screenCron
	// screenSchedulers is what the two schedulers say about themselves.
	screenSchedulers
	screenCount
)

// title names a screen for the tab bar.
func (s screen) title() string {
	switch s {
	case screenTimers:
		return "timers"
	case screenCron:
		return "cron"
	case screenSchedulers:
		return "schedulers"
	default:
		return "all jobs"
	}
}

// mode is the dialog the app currently has open. Only one is open at a time,
// which keeps the update loop flat.
type mode int

const (
	modeBrowse mode = iota
	modeDetail
	modeConfirm
	modeFilter
	modePicker
	modeForm
	modeHelp
)

// app is the tui-cron Bubble Tea model.
type app struct {
	backend schedule.Backend
	theme   theme.Theme
	caps    schedule.Capabilities
	// backendCompat is what the version probes found, rendered in the header.
	backendCompat []compat.Result

	model schedule.Model

	// The rows left after the filter, per screen, in display order.
	rows [screenCount][]schedule.Job

	width, height int
	screen        screen
	// cursor and offset are per screen, so moving between tabs does not lose
	// the row the reader was on.
	cursor [screenCount]int
	offset [screenCount]int
	filter string

	// detail is what the per-row screen shows, loaded in the background because
	// all three of its parts are reads against the machine.
	detail       detailData
	detailOffset int

	mode    mode
	confirm ui.Confirm
	input   ui.Input
	picker  ui.Picker
	form    jobForm
	// pickerFor names the form field an open picker is filling.
	pickerFor string

	status     string
	statusKind ui.StatusKind
	loading    bool
	// loadFailed reports that the last Load returned an error, so the empty
	// state does not claim the machine simply has nothing scheduled.
	loadFailed bool
	// busy blocks input while a command runs.
	busy bool
}

// detailData is the per-row screen's content: the job it is about, and the
// three reads that fill it.
type detailData struct {
	job schedule.Job
	// definition is `systemctl cat` or the table the line lives in.
	definition string
	// journal is what the job's own log says.
	journal string
	// elapses are the next runs, for a timer.
	elapses []string
	// The reason each part is missing, when it is. They are shown rather than
	// swallowed: "cron publishes no future runs" is the answer to the question,
	// not an error.
	definitionErr string
	journalErr    string
	elapsesErr    string
	loading       bool
}

// loadedMsg carries the result of a Load.
type loadedMsg struct {
	model schedule.Model
	err   error
}

// detailMsg carries the result of the detail screen's three reads.
type detailMsg struct {
	// id is the job the reads were for, so a slow answer for a row the reader
	// has already left is discarded rather than shown under the wrong title.
	id     string
	detail detailData
}

// ranMsg carries the result of running a plan.
type ranMsg struct {
	// title is the plan's title, echoed in the status line.
	title  string
	output string
	err    error
}

// plan is what a confirm dialog is holding: one or more commands, run in
// order. Most actions are a single command; writing a drop-in is four, and all
// of them are shown before any runs.
type plan struct {
	title    string
	commands []schedule.Command
}

// newApp builds the model around a backend.
func newApp(backend schedule.Backend, th theme.Theme,
	backendCompat []compat.Result) *app {
	a := &app{
		backend:       backend,
		theme:         th,
		caps:          backend.Capabilities(),
		backendCompat: backendCompat,
		width:         80,
		height:        24,
		loading:       true,
	}
	if th.Warning != "" {
		a.setStatus(ui.StatusWarn, th.Warning)
	}
	return a
}

// Init starts the first load.
func (a *app) Init() tea.Cmd { return a.load() }

// load reads both schedulers in the background.
func (a *app) load() tea.Cmd {
	backend := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		model, err := backend.Load(ctx)
		return loadedMsg{model: model, err: err}
	}
}

// loadDetail reads the three things the per-row screen shows.
//
// They are read on opening the row rather than with the list, because each is a
// process of its own: doing them for every job on every reload would turn a
// list of two hundred timers into six hundred commands.
func (a *app) loadDetail(job schedule.Job) tea.Cmd {
	backend := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		detail := detailData{job: job}
		if definition, err := backend.Definition(ctx, job); err != nil {
			detail.definitionErr = err.Error()
		} else {
			detail.definition = definition
		}
		if journal, err := backend.Journal(ctx, job, journalLines); err != nil {
			detail.journalErr = err.Error()
		} else {
			detail.journal = journal
		}
		if elapses, err := backend.Elapses(ctx, job, jobs.Elapses); err != nil {
			detail.elapsesErr = err.Error()
		} else {
			detail.elapses = elapses
		}
		return detailMsg{id: job.ID, detail: detail}
	}
}

// journalLines is how much of a job's log the detail screen asks for. It is
// more than fits on a screen because the screen scrolls, and less than a
// journal holds because nobody reads a thousand lines in a dialog.
const journalLines = 120

// run executes a confirmed plan in the background, one command at a time,
// stopping at the first failure.
func (a *app) run(p plan) tea.Cmd {
	backend := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		var outputs []string
		for _, cmd := range p.commands {
			out, err := backend.Run(ctx, cmd)
			if err != nil {
				return ranMsg{title: p.title, output: out, err: err}
			}
			if trimmed := strings.TrimSpace(out); trimmed != "" {
				outputs = append(outputs, trimmed)
			}
		}
		return ranMsg{title: p.title, output: strings.Join(outputs, "; ")}
	}
}

// setStatus records a plain message for the status line.
func (a *app) setStatus(kind ui.StatusKind, message string) {
	a.status = message
	a.statusKind = kind
}

// setStatusf records a formatted message for the status line.
func (a *app) setStatusf(kind ui.StatusKind, format string, args ...any) {
	a.setStatus(kind, fmt.Sprintf(format, args...))
}

// Update is the main event loop.
func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.clampCursor()
		return a, nil

	case loadedMsg:
		a.loading = false
		if msg.err != nil {
			a.loadFailed = true
			a.setStatus(ui.StatusError, msg.err.Error())
			return a, nil
		}
		a.loadFailed = false
		a.model = msg.model
		a.applyFilter()
		return a, nil

	case detailMsg:
		if msg.id != a.detail.job.ID {
			// The reader moved on before the answer arrived.
			return a, nil
		}
		a.detail = msg.detail
		return a, nil

	case ranMsg:
		a.busy = false
		if msg.err != nil {
			a.setStatus(ui.StatusError, msg.err.Error())
			return a, a.load()
		}
		summary := strings.TrimSpace(msg.output)
		if summary == "" {
			summary = "done"
		}
		a.setStatusf(ui.StatusOK, "%s: %s", msg.title, firstLine(summary))
		a.loading = true
		return a, a.load()

	case tea.KeyMsg:
		return a.handleKey(msg)
	}

	// Anything else (cursor blink, …) only concerns an open text input.
	if a.mode == modeFilter {
		cmd, _ := a.input.Update(msg)
		return a, cmd
	}
	if a.mode == modeForm {
		return a, a.form.updateActive(msg)
	}
	return a, nil
}

// handleKey routes a key press to the open dialog, or to the current screen.
func (a *app) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ctrl+c always quits, even mid-dialog.
	if msg.Type == tea.KeyCtrlC {
		return a, tea.Quit
	}
	if a.busy {
		// A command is running: swallow input rather than queueing surprises.
		return a, nil
	}

	switch a.mode {
	case modeConfirm:
		return a.handleConfirm(msg)
	case modeFilter:
		return a.handleFilter(msg)
	case modePicker:
		return a.handlePicker(msg)
	case modeForm:
		return a.handleForm(msg)
	case modeHelp:
		a.mode = modeBrowse
		return a, nil
	case modeDetail:
		return a.handleDetailKey(msg)
	default:
		return a.handleBrowseKey(msg)
	}
}

// handleConfirm resolves the confirm dialog.
func (a *app) handleConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	a.confirm.Update(msg)
	if !a.confirm.Done {
		return a, nil
	}
	a.mode = modeBrowse
	confirmed := a.confirm.Confirmed
	pending, ok := a.confirm.Payload.(plan)
	a.confirm = ui.Confirm{}
	if !confirmed || !ok {
		a.setStatus(ui.StatusInfo, "cancelled")
		return a, nil
	}
	a.busy = true
	a.setStatusf(ui.StatusInfo, "running %s…", a.backend.Preview(pending.commands[0]))
	return a, a.run(pending)
}

// handleFilter resolves the filter prompt.
func (a *app) handleFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cmd, _ := a.input.Update(msg)
	if !a.input.Done {
		// Filter as the user types.
		a.filter = a.input.Value()
		a.applyFilter()
		return a, cmd
	}
	if a.input.Accepted {
		a.filter = a.input.Value()
	} else {
		a.filter = ""
	}
	a.applyFilter()
	a.mode = modeBrowse
	return a, nil
}

// handlePicker resolves the open picker, which serves the form's choice fields.
func (a *app) handlePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	a.picker.Update(msg)
	if !a.picker.Done {
		return a, nil
	}
	choice, accepted := a.picker.Selected(), a.picker.Accepted
	field := a.pickerFor
	a.picker, a.pickerFor = ui.Picker{}, ""
	if accepted {
		a.form.set(field, choice)
	}
	a.mode = modeForm
	return a, nil
}

// handleForm routes keys to the open form.
func (a *app) handleForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		a.mode = modeBrowse
		a.setStatus(ui.StatusInfo, "cancelled")
		return a, nil
	case "tab", "down":
		a.form.next()
		return a, nil
	case "shift+tab", "up":
		a.form.prev()
		return a, nil
	case "left":
		if a.form.activeIsChoice() {
			a.form.cycle(-1)
			return a, nil
		}
	case "right":
		if a.form.activeIsChoice() {
			a.form.cycle(1)
			return a, nil
		}
	case " ":
		// Space opens the list for a choice field. It is not enter, because
		// enter has to mean "review the change" from every field — a form whose
		// choice field could never submit would be a dead end.
		if a.form.activeIsChoice() {
			a.pickerFor = a.form.activeKey()
			a.picker = ui.NewPicker(a.form.activeLabel(),
				a.form.activeOptions(), a.form.activeValue())
			a.mode = modePicker
			return a, nil
		}
	case "enter":
		return a, a.submitForm()
	}
	cmd := a.form.updateActive(msg)
	// The reading under the schedule field is recomputed on every keystroke,
	// because watching the English change as you type is the whole point of
	// having it there.
	a.form.reread()
	return a, cmd
}

// submitForm builds the change the form describes, has the scheduler check it,
// and opens the confirm dialog with the check, the diff and the commands.
func (a *app) submitForm() tea.Cmd {
	write, err := a.buildForm()
	if err != nil {
		a.setStatus(ui.StatusError, err.Error())
		return nil
	}
	a.mode = modeConfirm
	title := a.form.title
	a.confirm = ui.Confirm{
		Title:   title,
		Body:    a.writeBody(write),
		Command: a.previewAll(write.Commands),
		Danger:  true,
		Payload: plan{title: title, commands: write.Commands},
	}
	return nil
}

// buildForm asks the backend for the plan the open form describes.
func (a *app) buildForm() (schedule.WritePlan, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch a.form.kind {
	case formEditSchedule:
		return a.backend.BuildSetSchedule(ctx, a.model, a.form.job,
			a.form.value(fieldSchedule))
	case formEditCron:
		job := a.form.job
		job.Command = a.form.value(fieldCommand)
		return a.backend.BuildSetSchedule(ctx, a.model, job,
			a.form.value(fieldSchedule))
	case formAddCron:
		// A new line is an edit of line 0, which is how the table writer spells
		// "append": the same code path, so an addition and a change produce the
		// same diff and the same command.
		job := a.form.job
		job.Line = 0
		job.Command = a.form.value(fieldCommand)
		return a.backend.BuildSetSchedule(ctx, a.model, job,
			a.form.value(fieldSchedule))
	default:
		return a.backend.BuildCreate(ctx, a.model, a.form.spec())
	}
}

// writeBody is what the confirm dialog says above the commands: whether the
// scheduler accepted the change, the caveat that applies to it, and the diff.
func (a *app) writeBody(write schedule.WritePlan) string {
	var parts []string
	if write.Validated {
		parts = append(parts, "✓ "+write.Validation)
	} else if write.Validation != "" {
		parts = append(parts, "! the check "+write.Validation)
	}
	if write.ValidationCommand != "" {
		parts = append(parts, "checked with: "+write.ValidationCommand)
	}
	if write.Warning != "" {
		parts = append(parts, write.Warning)
	}
	parts = append(parts, a.diffForDialog(write.Diff))
	return strings.Join(parts, "\n\n")
}

// previewAll renders every command of a plan, one per line, each with the
// prompt the dialog puts in front of the first one.
func (a *app) previewAll(commands []schedule.Command) string {
	previews := make([]string, 0, len(commands))
	for _, cmd := range commands {
		previews = append(previews, a.backend.Preview(cmd))
	}
	return strings.Join(previews, "\n$ ")
}

// handleBrowseKey handles a screen's own keys.
func (a *app) handleBrowseKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		return a, tea.Quit
	case "?":
		a.mode = modeHelp
	case "j", "down":
		a.moveCursor(1)
	case "k", "up":
		a.moveCursor(-1)
	case "g", "home":
		a.cursor[a.screen], a.offset[a.screen] = 0, 0
	case "G", "end":
		a.cursor[a.screen] = max(a.rowCount()-1, 0)
		a.clampCursor()
	case "pgdown", "ctrl+f":
		a.moveCursor(a.tableHeight())
	case "pgup", "ctrl+b":
		a.moveCursor(-a.tableHeight())
	case "tab", "l", "right":
		a.gotoScreen((a.screen + 1) % screenCount)
	case "shift+tab", "h", "left":
		a.gotoScreen((a.screen + screenCount - 1) % screenCount)
	case "1", "2", "3", "4":
		a.gotoScreen(screen(msg.String()[0] - '1'))
	case "/":
		a.input = ui.NewInput("Filter "+a.screen.title(), "any column…", a.filter)
		a.input.Help = "Matches any column of this screen. Empty clears the filter."
		a.mode = modeFilter
	case "enter":
		return a, a.openDetail()
	case "R", "ctrl+r":
		a.loading = true
		return a, a.load()
	default:
		return a, a.handleActionKey(msg)
	}
	return a, nil
}

// openDetail opens the per-row screen and starts its reads.
func (a *app) openDetail() tea.Cmd {
	job, ok := a.selected()
	if !ok {
		a.setStatus(ui.StatusWarn, "nothing selected")
		return nil
	}
	a.mode, a.detailOffset = modeDetail, 0
	a.detail = detailData{job: job, loading: true}
	return a.loadDetail(job)
}

// handleDetailKey handles the per-row screen. The action keys are the same ones
// the table offers, applied to the row on screen.
func (a *app) handleDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "backspace", "left":
		a.mode, a.detailOffset = modeBrowse, 0
		return a, nil
	case "?":
		a.mode = modeHelp
		return a, nil
	case "j", "down":
		a.detailOffset++
		return a, nil
	case "k", "up":
		a.detailOffset = max(a.detailOffset-1, 0)
		return a, nil
	case "g", "home":
		a.detailOffset = 0
		return a, nil
	case "pgdown", "ctrl+f":
		a.detailOffset += a.detailHeight()
		return a, nil
	case "pgup", "ctrl+b":
		a.detailOffset = max(a.detailOffset-a.detailHeight(), 0)
		return a, nil
	case "R", "ctrl+r":
		a.loading = true
		return a, a.load()
	default:
		return a, a.handleActionKey(msg)
	}
}

// handleActionKey handles the keys that mean the same thing on every screen.
func (a *app) handleActionKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "e":
		return a.openEditForm()
	case "a":
		return a.openAddForm()
	case "c":
		return a.openCreateForm()
	case "d":
		return a.confirmDelete()
	case "t":
		return a.confirmConvert()
	case "E":
		return a.confirmControl("Enable", a.backend.BuildEnable)
	case "D":
		return a.confirmControl("Disable", a.backend.BuildDisable)
	case "s":
		return a.confirmControl("Arm", a.backend.BuildStart)
	case "x":
		return a.confirmControl("Disarm", a.backend.BuildStop)
	case "n":
		return a.confirmRunNow()
	}
	return nil
}

// confirmControl builds one of the four timer control actions and opens the
// confirm dialog, or reports the builder's refusal in the status line.
func (a *app) confirmControl(verb string,
	build func(schedule.Job) (schedule.Command, error)) tea.Cmd {
	job, ok := a.selected()
	if !ok {
		a.setStatus(ui.StatusWarn, "nothing selected")
		return nil
	}
	if !a.caps.SupportsTimerControl {
		a.setStatus(ui.StatusWarn, "this machine has no systemd to drive")
		return nil
	}
	cmd, err := build(job)
	if err != nil {
		a.setStatus(ui.StatusWarn, err.Error())
		return nil
	}
	a.openConfirm(verb+" "+job.Name, cmd.Description+".", cmd)
	return nil
}

// confirmRunNow asks before running a job off its schedule.
func (a *app) confirmRunNow() tea.Cmd {
	job, ok := a.selected()
	if !ok {
		a.setStatus(ui.StatusWarn, "nothing selected")
		return nil
	}
	cmd, err := a.backend.BuildRunNow(job)
	if err != nil {
		a.setStatus(ui.StatusWarn, err.Error())
		return nil
	}
	a.openConfirm("Run "+job.Name+" now", cmd.Description+
		".\nIt runs as it would on its schedule, now, whatever else is "+
		"happening on this machine.", cmd)
	return nil
}

// confirmDelete asks before removing a cron line.
func (a *app) confirmDelete() tea.Cmd {
	job, ok := a.selected()
	if !ok {
		a.setStatus(ui.StatusWarn, "nothing selected")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	write, err := a.backend.BuildDelete(ctx, a.model, job)
	if err != nil {
		a.setStatus(ui.StatusWarn, err.Error())
		return nil
	}
	title := "Remove the cron line at " + job.Where()
	a.mode = modeConfirm
	a.confirm = ui.Confirm{
		Title:   title,
		Body:    a.writeBody(write),
		Command: a.previewAll(write.Commands),
		Danger:  true,
		Payload: plan{title: title, commands: write.Commands},
	}
	return nil
}

// confirmConvert asks before writing a timer generated from a cron line.
func (a *app) confirmConvert() tea.Cmd {
	job, ok := a.selected()
	if !ok {
		a.setStatus(ui.StatusWarn, "nothing selected")
		return nil
	}
	if !a.caps.SupportsConvert {
		a.setStatus(ui.StatusWarn, "this machine has no systemd to convert to")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	write, err := a.backend.BuildConvert(ctx, a.model, job)
	if err != nil {
		a.setStatus(ui.StatusWarn, err.Error())
		return nil
	}
	title := "Write a timer for " + job.Name
	a.mode = modeConfirm
	a.confirm = ui.Confirm{
		Title:   title,
		Body:    a.writeBody(write),
		Command: a.previewAll(write.Commands),
		Danger:  true,
		Payload: plan{title: title, commands: write.Commands},
	}
	return nil
}

// openEditForm opens the schedule editor for the selected job.
func (a *app) openEditForm() tea.Cmd {
	job, ok := a.selected()
	if !ok {
		a.setStatus(ui.StatusWarn, "nothing selected")
		return nil
	}
	switch {
	case job.Kind == schedule.KindAnacronDir:
		a.setStatusf(ui.StatusWarn,
			"%s has no schedule of its own: the directory it is in is the "+
				"schedule", job.Name)
		return nil
	case job.Kind.Systemd() && !a.caps.SupportsTimerEdit:
		a.setStatus(ui.StatusWarn, "this machine has no systemd to drive")
		return nil
	case job.Kind.Cron() && !a.caps.SupportsCronEdit:
		a.setStatus(ui.StatusWarn, "cron is not installed on this machine")
		return nil
	case job.Monotonic:
		a.setStatusf(ui.StatusWarn,
			"%s has no OnCalendar: it fires relative to an event, and that is "+
				"a change to the unit file itself", job.Unit)
		return nil
	}
	a.form = newEditForm(job, a.caps)
	a.mode = modeForm
	return nil
}

// openAddForm opens the editor for a new line in this account's crontab.
func (a *app) openAddForm() tea.Cmd {
	if !a.caps.SupportsCronEdit {
		a.setStatus(ui.StatusWarn, "cron is not installed on this machine")
		return nil
	}
	a.form = newAddForm(a.model.User)
	a.mode = modeForm
	return nil
}

// openCreateForm opens the editor for a new systemd timer.
func (a *app) openCreateForm() tea.Cmd {
	if !a.caps.SupportsTimerCreate {
		a.setStatus(ui.StatusWarn, "this machine has no systemd to drive")
		return nil
	}
	a.form = newCreateForm(a.caps)
	a.mode = modeForm
	return nil
}

// openConfirm shows one command and what it does.
func (a *app) openConfirm(title, body string, cmd schedule.Command) {
	a.mode = modeConfirm
	a.confirm = ui.Confirm{
		Title:   title,
		Body:    body,
		Command: a.backend.Preview(cmd),
		Danger:  cmd.Destructive,
		Payload: plan{title: title, commands: []schedule.Command{cmd}},
	}
}

// gotoScreen switches tabs, keeping the filter applied.
func (a *app) gotoScreen(next screen) {
	if next < 0 || next >= screenCount {
		return
	}
	a.screen = next
	a.clampCursor()
}

// applyFilter recomputes every screen's visible rows from the current filter.
func (a *app) applyFilter() {
	needle := strings.ToLower(a.filter)
	keep := func(haystack string) bool {
		return needle == "" || strings.Contains(strings.ToLower(haystack), needle)
	}

	for s := screen(0); s < screenCount; s++ {
		a.rows[s] = nil
	}
	for _, job := range a.model.Jobs {
		if !keep(job.Haystack()) {
			continue
		}
		a.rows[screenAll] = append(a.rows[screenAll], job)
		if job.Kind.Systemd() {
			a.rows[screenTimers] = append(a.rows[screenTimers], job)
			continue
		}
		a.rows[screenCron] = append(a.rows[screenCron], job)
	}
	a.clampCursor()
}

// rowCount is how many rows the current screen has after the filter.
func (a *app) rowCount() int {
	if a.screen == screenSchedulers {
		return len(a.schedulerRows())
	}
	return len(a.rows[a.screen])
}

// selected is the highlighted job, and whether the current screen has one. The
// schedulers screen has none: its rows are facts about a machine, not jobs.
func (a *app) selected() (schedule.Job, bool) {
	if a.screen == screenSchedulers {
		if a.mode == modeDetail && a.detail.job.ID != "" {
			return a.detail.job, true
		}
		return schedule.Job{}, false
	}
	rows := a.rows[a.screen]
	index := a.cursor[a.screen]
	if index < 0 || index >= len(rows) {
		return schedule.Job{}, false
	}
	return rows[index], true
}

// moveCursor moves the selection and keeps the viewport in sync.
func (a *app) moveCursor(delta int) {
	a.cursor[a.screen] += delta
	a.clampCursor()
}

// clampCursor keeps the cursor and the scroll offset of every screen in range.
func (a *app) clampCursor() {
	for s := screen(0); s < screenCount; s++ {
		count := a.countFor(s)
		if count == 0 {
			a.cursor[s], a.offset[s] = 0, 0
			continue
		}
		a.cursor[s] = min(max(a.cursor[s], 0), count-1)

		height := a.tableHeight()
		if a.cursor[s] < a.offset[s] {
			a.offset[s] = a.cursor[s]
		}
		if a.cursor[s] >= a.offset[s]+height {
			a.offset[s] = a.cursor[s] - height + 1
		}
		a.offset[s] = max(min(a.offset[s], max(count-height, 0)), 0)
	}
}

// countFor is rowCount for a screen that is not the current one.
func (a *app) countFor(s screen) int {
	current := a.screen
	a.screen = s
	count := a.rowCount()
	a.screen = current
	return count
}

// firstLine keeps status messages to one line.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
