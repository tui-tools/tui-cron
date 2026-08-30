package main

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-cron/internal/schedule"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-kit/ui"
)

// The form fields, named rather than numbered so the picker knows which one it
// is filling and so buildForm can read one back by name.
const (
	fieldSchedule    = "schedule"
	fieldCommand     = "command"
	fieldName        = "name"
	fieldUser        = "user"
	fieldPersistent  = "persistent"
	fieldDescription = "description"
)

// formKind is which of the four things a form is for. They share one struct
// because they share one shape — a list of fields with a live reading of the
// schedule under them — and four nearly identical structs would drift.
type formKind int

const (
	// formEditSchedule changes a timer's OnCalendar.
	formEditSchedule formKind = iota
	// formEditCron changes a cron line's schedule and command.
	formEditCron
	// formAddCron adds a line to this account's crontab.
	formAddCron
	// formCreate writes a new timer and its service.
	formCreate
)

// formField is one row of the form.
type formField struct {
	key   string
	label string
	help  string
	// options is the closed set of values for a choice field, nil for a text
	// field.
	options []string
	choice  int
	input   textinput.Model
}

// value is what the field holds.
func (f formField) value() string {
	if len(f.options) > 0 {
		if f.choice < 0 || f.choice >= len(f.options) {
			return ""
		}
		return f.options[f.choice]
	}
	return strings.TrimSpace(f.input.Value())
}

// jobForm is the editor for a schedule, a cron line or a new timer.
//
// Its distinguishing feature is the line under the fields: the schedule the
// user has typed, read back in English by the same describer the list uses,
// recomputed on every keystroke. Watching "Every day at 03:00" turn into "At 3
// minutes past every hour" as a space goes in the wrong place is the check this
// tool is for, and it happens before anything is built.
type jobForm struct {
	kind   formKind
	title  string
	fields []formField
	active int
	// job is the row the form is about, empty for a create.
	job schedule.Job
	// reading is the English for the schedule field as it stands, and
	// readingErr the reason there is none.
	reading    string
	readingErr string
	// destination names the file the change lands in, so nobody has to reach
	// the confirm dialog to find out where it is going.
	destination string
}

// newTextField builds a text field seeded with a value.
func newTextField(key, label, help, value, placeholder string) formField {
	input := textinput.New()
	input.CharLimit = 200
	input.Prompt = ""
	input.Placeholder = placeholder
	input.SetValue(value)
	return formField{key: key, label: label, help: help, input: input}
}

// newChoiceField builds a choice field positioned on a value.
func newChoiceField(key, label, help string, options []string, value string) formField {
	f := formField{key: key, label: label, help: help, options: options}
	for i, option := range options {
		if option == value {
			f.choice = i
		}
	}
	return f
}

// newEditForm builds the editor for an existing job's schedule.
func newEditForm(job schedule.Job, caps schedule.Capabilities) jobForm {
	f := jobForm{job: job, title: "Change when " + job.Name + " runs"}
	if job.Kind.Systemd() {
		f.kind = formEditSchedule
		f.destination = caps.DropInFor(job.Unit)
		f.fields = []formField{newTextField(fieldSchedule, "OnCalendar",
			"systemd's calendar syntax: daily, weekly, Mon..Fri 09:00, "+
				"*-*-01 04:30:00.",
			job.Schedule, job.Schedule)}
	} else {
		f.kind = formEditCron
		f.destination = job.Where()
		f.fields = []formField{
			newTextField(fieldSchedule, "Schedule",
				"Five cron fields — minute hour day month weekday — or a macro "+
					"like @daily or @reboot.",
				job.Schedule, job.Schedule),
			newTextField(fieldCommand, "Command",
				"cron runs this through /bin/sh, so a pipe or a redirection is "+
					"allowed here.",
				job.Command, job.Command),
		}
	}
	f.focusActive()
	f.reread()
	return f
}

// newAddForm builds the editor for a new line in this account's crontab.
func newAddForm(user string) jobForm {
	f := jobForm{
		kind:        formAddCron,
		title:       "Add a line to " + user + "'s crontab",
		destination: "your own crontab, replaced whole through `crontab <file>`",
		// The line belongs to this account's table, which has no user field:
		// the job carries the owner so the writer knows whose table to stage.
		job: schedule.Job{Kind: schedule.KindCrontab, Owner: user},
		fields: []formField{
			newTextField(fieldSchedule, "Schedule",
				"Five cron fields — minute hour day month weekday — or a macro "+
					"like @daily or @reboot.",
				"", "*/15 * * * *"),
			newTextField(fieldCommand, "Command",
				"cron runs this through /bin/sh, so a pipe or a redirection is "+
					"allowed here.",
				"", "/usr/local/bin/something"),
		},
	}
	f.focusActive()
	f.reread()
	return f
}

// newCreateForm builds the editor for a new systemd timer.
func newCreateForm(caps schedule.Capabilities) jobForm {
	f := jobForm{
		kind:        formCreate,
		title:       "Create a systemd timer",
		destination: caps.UnitDir + "/<name>.timer and .service",
		fields: []formField{
			newTextField(fieldName, "Name",
				"The unit name, without a suffix. Both files are named after it.",
				"", "nightly-backup"),
			newTextField(fieldSchedule, "OnCalendar",
				"systemd's calendar syntax: daily, weekly, Mon..Fri 09:00, "+
					"*-*-01 04:30:00.",
				"", "*-*-* 02:30:00"),
			newTextField(fieldCommand, "ExecStart",
				"An absolute path. systemd runs it with no shell and no PATH of "+
					"yours, so a pipe or a bare program name will not work.",
				"", "/usr/local/bin/backup --offsite"),
			newTextField(fieldUser, "User",
				"The account it runs as. Empty means root.", "", "root"),
			newChoiceField(fieldPersistent, "Persistent",
				"true catches up a run missed while the machine was off. For "+
					"anything daily or rarer, that is the setting you want.",
				[]string{"true", "false"}, "true"),
			newTextField(fieldDescription, "Description",
				"One line, shown by systemctl and by this tool.", "", ""),
		},
	}
	f.focusActive()
	f.reread()
	return f
}

// value returns one field by name.
func (f jobForm) value(key string) string {
	for _, field := range f.fields {
		if field.key == key {
			return field.value()
		}
	}
	return ""
}

// spec is the new timer the create form describes.
func (f jobForm) spec() schedule.NewTimer {
	return schedule.NewTimer{
		Name:        f.value(fieldName),
		Calendar:    f.value(fieldSchedule),
		ExecStart:   f.value(fieldCommand),
		User:        f.value(fieldUser),
		Persistent:  f.value(fieldPersistent) == "true",
		Description: f.value(fieldDescription),
	}
}

// reread recomputes the English under the fields from the schedule as it
// currently stands.
func (f *jobForm) reread() {
	expression := f.value(fieldSchedule)
	f.reading, f.readingErr = "", ""
	if strings.TrimSpace(expression) == "" {
		return
	}
	if f.cron() {
		if err := schedule.ValidateCron(expression); err != nil {
			f.readingErr = err.Error()
			return
		}
		f.reading = schedule.DescribeCron(expression)
		return
	}
	f.reading = schedule.DescribeCalendar(expression)
	if f.reading == "" {
		f.readingErr = "not a calendar expression this tool can read — " +
			"systemd will have the last word when you press enter"
	}
}

// cron reports whether the form's schedule field is a cron expression rather
// than an OnCalendar.
func (f jobForm) cron() bool {
	return f.kind == formEditCron || f.kind == formAddCron
}

// focusActive moves the text cursor to the active field when it is a text box.
func (f *jobForm) focusActive() {
	for i := range f.fields {
		if i == f.active && len(f.fields[i].options) == 0 {
			f.fields[i].input.Focus()
			continue
		}
		f.fields[i].input.Blur()
	}
}

// next moves to the following field, wrapping.
func (f *jobForm) next() {
	if len(f.fields) == 0 {
		return
	}
	f.active = (f.active + 1) % len(f.fields)
	f.focusActive()
}

// prev moves to the previous field, wrapping.
func (f *jobForm) prev() {
	if len(f.fields) == 0 {
		return
	}
	f.active = (f.active + len(f.fields) - 1) % len(f.fields)
	f.focusActive()
}

// activeIsChoice reports whether the active field is one the picker serves.
func (f jobForm) activeIsChoice() bool {
	return f.active < len(f.fields) && len(f.fields[f.active].options) > 0
}

// activeKey, activeLabel, activeOptions and activeValue expose the active field
// to the picker dialog.
func (f jobForm) activeKey() string {
	if f.active >= len(f.fields) {
		return ""
	}
	return f.fields[f.active].key
}

func (f jobForm) activeLabel() string {
	if f.active >= len(f.fields) {
		return ""
	}
	return f.fields[f.active].label
}

func (f jobForm) activeOptions() []string {
	if f.active >= len(f.fields) {
		return nil
	}
	return f.fields[f.active].options
}

func (f jobForm) activeValue() string {
	if f.active >= len(f.fields) {
		return ""
	}
	return f.fields[f.active].value()
}

// set applies a value chosen in the picker to a field.
func (f *jobForm) set(key, value string) {
	for i := range f.fields {
		if f.fields[i].key != key {
			continue
		}
		for j, option := range f.fields[i].options {
			if option == value {
				f.fields[i].choice = j
			}
		}
		return
	}
}

// cycle moves the active choice field one step.
func (f *jobForm) cycle(delta int) {
	if !f.activeIsChoice() {
		return
	}
	field := &f.fields[f.active]
	field.choice = (field.choice + delta + len(field.options)) % len(field.options)
}

// updateActive forwards a message to the active field when it is a text box.
func (f *jobForm) updateActive(msg tea.Msg) tea.Cmd {
	if f.active >= len(f.fields) || len(f.fields[f.active].options) > 0 {
		return nil
	}
	var cmd tea.Cmd
	f.fields[f.active].input, cmd = f.fields[f.active].input.Update(msg)
	return cmd
}

// view renders the form as a dialog.
func (f jobForm) view(t theme.Theme, width, height int) string {
	inner := min(max(width-8, 30), 76)
	labelWidth := 0
	for _, field := range f.fields {
		labelWidth = max(labelWidth, len(field.label))
	}
	labelWidth = min(labelWidth, max(inner-16, 8))
	valueWidth := max(inner-labelWidth-6, 10)

	lines := []string{t.Title.Render(ui.Truncate(f.title, inner-4)), ""}
	for i, field := range f.fields {
		label := t.Muted.Render(ui.Pad(ui.Truncate(field.label, labelWidth), labelWidth))
		var value string
		switch {
		case len(field.options) > 0:
			value = renderChoice(t, field.value(), i == f.active, valueWidth)
		case i == f.active:
			input := field.input
			input.Width = valueWidth - 2
			value = input.View()
		default:
			value = t.Base.Render(ui.Truncate(field.value(), valueWidth))
		}
		marker := "  "
		if i == f.active {
			marker = t.Accent.Render("> ")
		}
		lines = append(lines, marker+label+"  "+value)
	}

	// The reading, which is the reason this dialog is worth having.
	lines = append(lines, "")
	switch {
	case f.readingErr != "":
		lines = append(lines,
			t.Warn.Render(ui.Truncate("reads as: "+f.readingErr, inner-4)))
	case f.reading != "":
		for i, line := range wrapText("reads as: "+f.reading, inner-4) {
			style := t.OK
			if i > 0 {
				style = t.Muted
			}
			lines = append(lines, style.Render(line))
		}
	default:
		lines = append(lines, t.Muted.Render("reads as: —"))
	}

	if f.active < len(f.fields) && f.fields[f.active].help != "" {
		lines = append(lines, "")
		for _, line := range wrapText(f.fields[f.active].help, inner-4) {
			lines = append(lines, t.Muted.Render(line))
		}
	}

	lines = append(lines, "",
		t.Muted.Render(ui.Truncate("Written to "+f.destination, inner-4)),
		"",
		t.Key.Render("tab")+t.KeyDesc.Render(" next  ")+
			t.Key.Render("←/→")+t.KeyDesc.Render(" change  ")+
			t.Key.Render("space")+t.KeyDesc.Render(" list  ")+
			t.Key.Render("enter")+t.KeyDesc.Render(" review  ")+
			t.Key.Render("esc")+t.KeyDesc.Render(" cancel"))

	box := t.Dialog.Width(inner).Render(strings.Join(lines, "\n"))
	return placeCenter(box, width, height)
}

// renderChoice draws a choice field with its cycling arrows.
func renderChoice(t theme.Theme, value string, active bool, width int) string {
	value = ui.Truncate(value, width-4)
	if active {
		return t.Accent.Render("‹ ") + t.Base.Render(value) + t.Accent.Render(" ›")
	}
	return t.Base.Render("  " + value)
}

// wrapText breaks a sentence onto lines of at most width cells. It always
// returns at least one line, so a caller can index the first.
func wrapText(text string, width int) []string {
	if width <= 0 {
		return []string{""}
	}
	var lines []string
	current := ""
	for _, word := range strings.Fields(text) {
		switch {
		case current == "":
			current = word
		case len(current)+1+len(word) <= width:
			current += " " + word
		default:
			lines = append(lines, current)
			current = word
		}
	}
	if current != "" || len(lines) == 0 {
		lines = append(lines, current)
	}
	for i, line := range lines {
		lines[i] = ui.Truncate(line, width)
	}
	return lines
}
