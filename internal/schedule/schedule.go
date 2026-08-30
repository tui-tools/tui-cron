// Package schedule defines the backend-agnostic model tui-cron renders and the
// interface every scheduler implementation satisfies. The UI knows only these
// types: it never builds a systemctl, crontab or journalctl argv itself.
// Mutations are Command values produced by the backend, shown in a preview
// dialog and only then executed.
//
// The model has one shape for two very different schedulers. A systemd timer
// and a crontab line answer the same four questions — what runs, when, when did
// it last run, and did it work — and the whole point of this tool is that they
// are asked once. Where the two genuinely differ, the model says so in a field
// rather than in a second type: Kind names which scheduler a job came from,
// Schedule carries the expression in that scheduler's own syntax, and the
// actions a job accepts come from the backend's Capabilities.
package schedule

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/tui-tools/tui-kit/runner"
)

// Command is a single privileged invocation the user is about to run. Argv
// excludes any privilege wrapper: the backend adds it when previewing and when
// executing.
//
// It is an alias rather than a type of its own, so a backend hands the very
// value the confirm dialog displayed straight to the kit runner, with no
// conversion in between. That identity is what makes the preview a promise.
type Command = runner.Command

// Kind is which scheduler a job belongs to. It is a string rather than an enum
// so `--check` reports a word a script can grep for.
type Kind string

// The five kinds of scheduled job this tool reads. They are separate rather
// than folded into "timer" and "cron" because the actions differ: a system
// timer is enabled with `systemctl enable`, a user timer with
// `systemctl --user enable`, a crontab line by rewriting one user's crontab,
// a /etc/cron.d file by installing a file, and a script in cron.daily by
// nothing this tool will do at all.
const (
	// KindTimer is a systemd timer in the system manager.
	KindTimer Kind = "timer"
	// KindUserTimer is a systemd timer in the calling user's own manager.
	KindUserTimer Kind = "user-timer"
	// KindCrontab is one line of a user's crontab.
	KindCrontab Kind = "crontab"
	// KindCronD is one line of /etc/crontab or a file in /etc/cron.d. Both
	// carry the extra user field, which is what separates them from a user
	// crontab.
	KindCronD Kind = "cron.d"
	// KindAnacronDir is an executable dropped in /etc/cron.hourly,
	// /etc/cron.daily, /etc/cron.weekly or /etc/cron.monthly. It has no
	// schedule expression of its own: the directory is the schedule.
	KindAnacronDir Kind = "anacron-dir"
)

// Kinds is the order the kind filter cycles through and the order `--check`
// reports the counts in.
var Kinds = []Kind{KindTimer, KindUserTimer, KindCrontab, KindCronD, KindAnacronDir}

// Systemd reports whether a kind is driven through systemd rather than cron.
func (k Kind) Systemd() bool { return k == KindTimer || k == KindUserTimer }

// Cron reports whether a kind comes from a cron table.
func (k Kind) Cron() bool {
	return k == KindCrontab || k == KindCronD || k == KindAnacronDir
}

// Outcome is how a job's last run ended.
type Outcome string

// The four outcomes. OutcomeUnknown is the zero value and means the source
// could not say — which is the honest answer for a cron job whose journal this
// user cannot read, and is not the same as "it worked".
const (
	OutcomeUnknown Outcome = ""
	// OutcomeOK is a last run that finished successfully.
	OutcomeOK Outcome = "ok"
	// OutcomeFailed is a last run that did not.
	OutcomeFailed Outcome = "failed"
	// OutcomeNever means the job has not run yet.
	OutcomeNever Outcome = "never"
)

// Job is one scheduled job, whichever scheduler owns it.
type Job struct {
	// ID identifies the job across a reload, so the cursor stays on the row it
	// was on. For a timer it is the unit name; for a cron line it is the file
	// and the line number, which is what changes when the line moves.
	ID string
	// Name is what the list shows: the timer unit, or the command's first word
	// qualified by its owner.
	Name string
	// Kind is the scheduler this job belongs to.
	Kind Kind
	// Description is the unit's Description, empty for a cron line.
	Description string

	// Schedule is the expression exactly as the scheduler reports it:
	// "*-*-* 00:00:00" for a timer, "*/5 * * * *" or "@reboot" for cron. It is
	// never rewritten for display — the whole point of the column is that it is
	// the string in the file.
	Schedule string
	// Explain is that expression in English, from Describe. Empty when the
	// expression is one this tool cannot explain, which is a reason to show the
	// expression alone rather than a guess.
	Explain string
	// Command is what the job runs: the service's ExecStart, or the command
	// half of the cron line.
	Command string
	// Owner is the account the job runs as.
	Owner string

	// Unit and Service are the timer unit and the unit it activates, empty for
	// a cron job.
	Unit    string
	Service string
	// File is where the job is written — the timer's fragment path, or the
	// crontab file — and Line is the 1-based line inside it, 0 when the job is
	// the whole file.
	File string
	Line int

	// Next is when the job runs next, and Last when it last ran. Either is the
	// zero time when it is not known, and NextNote says why when Next is.
	Next     time.Time
	Last     time.Time
	NextNote string

	// Outcome is how the last run ended, and OutcomeDetail is the scheduler's
	// own words for it ("exit status 2", "the journal has no line for it").
	Outcome       Outcome
	OutcomeDetail string

	// Enabled reports that the job runs at all without anyone starting it: a
	// timer whose unit file is enabled, or a cron line that is not commented
	// out. Active reports that the timer unit is loaded and waiting.
	Enabled bool
	Active  bool
	// State is the scheduler's own word: a timer's ActiveState, or "installed"
	// for a cron line.
	State string

	// Monotonic marks a timer with no OnCalendar at all: it fires a fixed time
	// after the boot, or after the unit it watches last stopped. There is no
	// calendar expression to explain and none to edit, and the actions that
	// would say otherwise refuse in those words.
	Monotonic bool

	// Persistent is the timer's Persistent= setting, and PersistentKnown
	// reports whether it was read at all. The pair matters because false and
	// unknown mean different things: a daily timer with Persistent=no silently
	// skips a run on a machine that was asleep, and that is worth warning
	// about, while an unread value is not.
	Persistent      bool
	PersistentKnown bool

	// Raw is the line or property the job was parsed from, shown on the detail
	// screen so a reader can see what was actually read.
	Raw string
}

// Failed reports that the last run did not succeed.
func (j Job) Failed() bool { return j.Outcome == OutcomeFailed }

// NeedsPersistent reports a timer that fires daily or less often and does not
// carry Persistent=true.
//
// It is a warning rather than a finding. Without Persistent, systemd does not
// remember that a run was missed, so a laptop asleep at 00:00 simply skips its
// daily job and nothing anywhere says so — which is the failure mode people
// discover months later, through a backup that was never taken. A timer that
// fires every few minutes does not care: the next one is along shortly.
func (j Job) NeedsPersistent() bool {
	if !j.Kind.Systemd() || !j.PersistentKnown || j.Persistent {
		return false
	}
	interval, ok := CalendarInterval(j.Schedule)
	return ok && interval >= 24*time.Hour
}

// Where names the place a reader would go to change the job.
func (j Job) Where() string {
	if j.File == "" {
		return ""
	}
	if j.Line <= 0 {
		return j.File
	}
	return j.File + ":" + strconv.Itoa(j.Line)
}

// CronState is what the machine says about cron itself: whether it is
// installed, and whether it is running.
//
// It is reported rather than assumed because the answer differs by
// distribution and by image. Ubuntu ships `cron`, Fedora ships `cronie` and
// calls the unit `crond`, and a minimal server image may ship neither — on a
// machine with only timers, "cron: not installed" is the correct thing for the
// screen to say, not an empty list nobody can interpret.
type CronState struct {
	// Installed reports that a crontab binary was found.
	Installed bool
	// Unit is the service unit that runs the daemon ("crond", "cron"), empty
	// when no unit was found.
	Unit string
	// Active and Enabled are that unit's state.
	Active  bool
	Enabled bool
	// State is systemd's ActiveState for it.
	State string
	// Detail explains the state in one sentence, and is what the screen shows
	// when Installed is false.
	Detail string
}

// TimerState is what the machine says about systemd's own scheduler.
type TimerState struct {
	// Available reports that the system manager answered.
	Available bool
	// UserAvailable reports that the calling user's own manager answered. A
	// machine reached over a serial console or a `sudo -i` shell has no user
	// bus, and the honest answer there is that the user timers were not read.
	UserAvailable bool
	// Detail and UserDetail explain each, when there is something to explain.
	Detail     string
	UserDetail string
}

// Model is the whole picture tui-cron renders.
type Model struct {
	// Backend names the implementation that produced this model.
	Backend string
	// Jobs are every scheduled job found, in display order.
	Jobs []Job
	// Timers and Cron are what the two schedulers themselves say.
	Timers TimerState
	Cron   CronState
	// User is the account whose crontab was read, which is the account the
	// tool is running as.
	User string
}

// Job returns one job by id.
func (m Model) Job(id string) (Job, bool) {
	for _, job := range m.Jobs {
		if job.ID == id {
			return job, true
		}
	}
	return Job{}, false
}

// Counts is how many jobs of each kind were found, keyed by kind.
func (m Model) Counts() map[Kind]int {
	counts := map[Kind]int{}
	for _, job := range m.Jobs {
		counts[job.Kind]++
	}
	return counts
}

// Failed are the jobs whose last run did not succeed.
func (m Model) Failed() []Job {
	var out []Job
	for _, job := range m.Jobs {
		if job.Failed() {
			out = append(out, job)
		}
	}
	return out
}

// NeedPersistent are the timers that fire daily or less often without
// Persistent=true.
func (m Model) NeedPersistent() []Job {
	var out []Job
	for _, job := range m.Jobs {
		if job.NeedsPersistent() {
			out = append(out, job)
		}
	}
	return out
}

// NewTimer is the form's answer: a timer to create from nothing.
type NewTimer struct {
	// Name is the unit name without a suffix ("nightly-backup").
	Name string
	// Calendar is the OnCalendar expression.
	Calendar string
	// ExecStart is the command the service runs.
	ExecStart string
	// User is the account it runs as, empty for root.
	User string
	// Persistent writes Persistent=true, which is the default the form offers.
	Persistent bool
	// Description is the unit's Description line.
	Description string
}

// Capabilities tells the UI what a backend supports, so the key map is built
// from the backend rather than hardcoded.
type Capabilities struct {
	// SupportsTimerControl reports that a timer can be enabled, disabled,
	// started, stopped and triggered.
	SupportsTimerControl bool
	// SupportsTimerEdit reports that a timer's OnCalendar can be changed
	// through a drop-in.
	SupportsTimerEdit bool
	// SupportsTimerCreate reports that a new timer can be written.
	SupportsTimerCreate bool
	// SupportsCronEdit reports that a cron line can be added, changed or
	// removed.
	SupportsCronEdit bool
	// SupportsConvert reports that a cron line can be turned into a timer.
	SupportsConvert bool

	// DropInFor renders the drop-in path a timer's schedule change is written
	// to, so the form can name the file before anything is built.
	DropInFor func(unit string) string
	// UnitDir is where a created timer is written.
	UnitDir string
}

// WritePlan is a change the user is about to make: what the file will look
// like, how that differs from what is there now, whether the scheduler accepted
// it, and the exact commands that apply it.
type WritePlan struct {
	// Path is the destination file.
	Path string
	// Content is the text that will be installed.
	Content string
	// Diff is the unified diff against the current file, empty when nothing
	// would change.
	Diff string
	// TempPath is the staging file the install command copies from.
	TempPath string
	// Validation is what the scheduler's own parser said about the staged
	// content, and ValidationCommand is the command line that asked. The check
	// runs before the user is asked to confirm, because a schedule the
	// scheduler will not accept is not something to discover after the file is
	// in /etc.
	Validation        string
	ValidationCommand string
	// Validated reports that the check ran and passed. False with an empty
	// Validation means the check could not run at all.
	Validated bool
	// Warning is a caveat the confirm dialog must show.
	Warning string
	// Commands are run in order, and are what the confirm dialog shows.
	Commands []Command
}

// Backend is the boundary between the UI and the machine. Load reads state;
// the Build* methods turn user intent into previewable Commands; Run executes a
// Command the user confirmed. Nothing else may mutate the system.
type Backend interface {
	// Name is the backend identifier ("host").
	Name() string
	// Describe is the one-line summary shown in the header.
	Describe() string
	// Capabilities reports what this backend supports.
	Capabilities() Capabilities

	// Preview renders the exact command line Run will execute, privilege
	// wrapper included. This is the text shown in the confirm dialog.
	Preview(cmd Command) string

	// Load reads every scheduled job on the machine.
	Load(ctx context.Context) (Model, error)
	// Run executes a previously previewed command.
	Run(ctx context.Context, cmd Command) (string, error)

	// Definition returns the job's own text: `systemctl cat` for a timer, the
	// crontab line with the file around it for a cron job.
	Definition(ctx context.Context, job Job) (string, error)
	// Journal returns the last n log lines for a job.
	Journal(ctx context.Context, job Job, lines int) (string, error)
	// Elapses returns the next n times the schedule fires, as the scheduler
	// itself computes them.
	Elapses(ctx context.Context, job Job, n int) ([]string, error)

	// The timer control actions. Each returns an error naming the reason when
	// the job is not one it applies to, which the UI turns into a hint.
	BuildEnable(job Job) (Command, error)
	BuildDisable(job Job) (Command, error)
	BuildStart(job Job) (Command, error)
	BuildStop(job Job) (Command, error)
	BuildRunNow(job Job) (Command, error)

	// BuildSetSchedule changes when a job runs: a drop-in for a timer, a
	// rewritten line for a cron job.
	BuildSetSchedule(ctx context.Context, model Model, job Job, expression string) (WritePlan, error)
	// BuildDelete removes a cron line.
	BuildDelete(ctx context.Context, model Model, job Job) (WritePlan, error)
	// BuildConvert generates a .timer and .service pair from a cron line. The
	// pair is written but deliberately not enabled: two schedulers running the
	// same command is worse than neither.
	BuildConvert(ctx context.Context, model Model, job Job) (WritePlan, error)
	// BuildCreate writes a new timer and its service.
	BuildCreate(ctx context.Context, model Model, spec NewTimer) (WritePlan, error)
}

// Haystack is the text the filter matches a job against.
func (j Job) Haystack() string {
	return strings.Join([]string{
		j.Name, string(j.Kind), j.Schedule, j.Explain, j.Command, j.Owner,
		j.Unit, j.Service, j.Where(), j.Description, string(j.Outcome), j.State,
	}, " ")
}
