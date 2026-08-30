package crontab

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tui-tools/tui-cron/internal/schedule"
)

// The fixtures in testdata are a mixture, and each one says which it is.
// /etc/crontab, /etc/cron.d/0hourly, the cron journal and `crontab -V` are real
// captures from the Fedora 42 host this backend was written on, with the
// hostname in the journal rewritten. The two table fixtures are written by
// hand, because that host's own account has no crontab and a fixture nobody can
// read is worse than one that says where it came from.

// fixture reads one of them.
func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // the name is a literal in the tests above, and testdata is in the repository
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(raw)
}

// TestParseUserTable reads a user's crontab, which has no user field: every
// line runs as the account that owns the table.
func TestParseUserTable(t *testing.T) {
	jobs := ParseUserTable(fixture(t, "user-crontab.txt"), "ana",
		TablePathFor("ana"))

	// Three job lines. The environment assignments, the comments and the
	// commented-out job are not jobs and must not be listed as ones.
	if len(jobs) != 3 {
		t.Fatalf("read %d jobs, want 3: %+v", len(jobs), names(jobs))
	}
	for _, job := range jobs {
		if job.Kind != schedule.KindCrontab {
			t.Errorf("%s: kind = %q", job.Name, job.Kind)
		}
		if job.Owner != "ana" {
			t.Errorf("%s: owner = %q, want the table's account", job.Name, job.Owner)
		}
		if job.Line <= 0 {
			t.Errorf("%s: no line number, so an edit could not find it", job.Name)
		}
	}

	if jobs[0].Schedule != "*/5 * * * *" {
		t.Errorf("schedule = %q", jobs[0].Schedule)
	}
	if jobs[0].Command != "/usr/local/bin/check-queue" {
		t.Errorf("command = %q", jobs[0].Command)
	}
	if jobs[0].Explain != "Every 5 minutes" {
		t.Errorf("explain = %q", jobs[0].Explain)
	}
	// @reboot is a schedule with no clock in it, and it has to survive as one.
	if jobs[1].Schedule != "@reboot" {
		t.Errorf("the @reboot line parsed as %q", jobs[1].Schedule)
	}
	if !strings.Contains(jobs[1].NextNote, "boot") {
		t.Errorf("the @reboot line does not say when it runs: %q", jobs[1].NextNote)
	}
	// A command keeps its own spacing and its redirections: it is what cron
	// hands to /bin/sh, and rewriting it would change what runs.
	if !strings.Contains(jobs[2].Command, ">> /var/log/report.log 2>&1") {
		t.Errorf("the command lost its redirection: %q", jobs[2].Command)
	}
}

// TestParseSystemTable reads the format with the extra user field, which is
// what /etc/crontab and everything in /etc/cron.d carry.
func TestParseSystemTable(t *testing.T) {
	jobs := ParseSystemTable(fixture(t, "cron.d-tui.txt"),
		CronDDir+"/collect-metrics")
	if len(jobs) != 2 {
		t.Fatalf("read %d jobs, want 2: %+v", len(jobs), names(jobs))
	}
	if jobs[0].Owner != "root" || jobs[1].Owner != "backup" {
		t.Errorf("owners = %q, %q; want them read from the user field",
			jobs[0].Owner, jobs[1].Owner)
	}
	if jobs[0].Command != "/usr/local/bin/collect-metrics" {
		t.Errorf("the user field leaked into the command: %q", jobs[0].Command)
	}
	if jobs[0].Kind != schedule.KindCronD {
		t.Errorf("kind = %q", jobs[0].Kind)
	}
}

// TestParseFedorasOwnCronD reads the file every Fedora machine has, which is
// what actually runs /etc/cron.hourly there.
func TestParseFedorasOwnCronD(t *testing.T) {
	jobs := ParseSystemTable(fixture(t, "cron.d-0hourly.txt"), CronDDir+"/0hourly")
	if len(jobs) != 1 {
		t.Fatalf("read %d jobs, want 1: %+v", len(jobs), names(jobs))
	}
	job := jobs[0]
	if job.Schedule != "01 * * * *" || job.Owner != "root" {
		t.Errorf("job = %q as %q", job.Schedule, job.Owner)
	}
	if job.Explain != "At 1 minute past every hour" {
		t.Errorf("explain = %q", job.Explain)
	}
}

// TestStockEtcCrontabHasNoJobs: Fedora ships /etc/crontab as a comment block
// with no job lines at all, and a screen that listed its example line as a job
// would be listing something that does not run.
func TestStockEtcCrontabHasNoJobs(t *testing.T) {
	jobs := ParseSystemTable(fixture(t, "etc-crontab.txt"), SystemCrontab)
	if len(jobs) != 0 {
		t.Errorf("the stock /etc/crontab produced %d jobs: %+v", len(jobs), names(jobs))
	}
}

// TestAssignmentsAreNotJobs: a crontab's MAILTO= and PATH= lines are
// load-bearing and are not schedules. A line whose *command* sets a variable
// still is one.
func TestAssignmentsAreNotJobs(t *testing.T) {
	jobs := ParseUserTable("MAILTO=root\nPATH=/usr/bin\nSHELL=/bin/bash\n"+
		"0 3 * * * FOO=bar /usr/local/bin/thing\n", "ana", TablePathFor("ana"))
	if len(jobs) != 1 {
		t.Fatalf("read %d jobs, want 1: %+v", len(jobs), names(jobs))
	}
	if !strings.HasPrefix(jobs[0].Command, "FOO=bar ") {
		t.Errorf("the assignment in the command was lost: %q", jobs[0].Command)
	}
}

// TestMalformedLinesAreSkipped: a table can carry anything, and half-reading a
// line into a job with the wrong schedule would be worse than ignoring it.
func TestMalformedLinesAreSkipped(t *testing.T) {
	jobs := ParseUserTable("not a cron line at all\n"+
		"* * * *\n"+ // four fields
		"61 * * * * /bin/true\n"+ // a minute that does not exist
		"@never /bin/true\n"+ // a macro that does not exist
		"*/5 * * * *\n"+ // a schedule with no command
		"0 3 * * * /bin/true\n", "ana", TablePathFor("ana"))
	if len(jobs) != 1 {
		t.Fatalf("read %d jobs, want only the good one: %+v", len(jobs), names(jobs))
	}
	if jobs[0].Command != "/bin/true" {
		t.Errorf("command = %q", jobs[0].Command)
	}
}

// TestJobIDsAreStable is what keeps the cursor on the row it was on across a
// reload: two lines of the same table must not share an id.
func TestJobIDsAreStable(t *testing.T) {
	jobs := ParseUserTable(fixture(t, "user-crontab.txt"), "ana",
		TablePathFor("ana"))
	seen := map[string]bool{}
	for _, job := range jobs {
		if seen[job.ID] {
			t.Errorf("two jobs share the id %q", job.ID)
		}
		seen[job.ID] = true
	}
	again := ParseUserTable(fixture(t, "user-crontab.txt"), "ana",
		TablePathFor("ana"))
	for i := range jobs {
		if jobs[i].ID != again[i].ID {
			t.Errorf("the id changed between reads: %q then %q",
				jobs[i].ID, again[i].ID)
		}
	}
}

// TestParseCronLog reads the journal capture from this Fedora host: cronie
// writes `(user) CMD (command)` and, when the command returns, `CMDEND`.
func TestParseCronLog(t *testing.T) {
	lines := ParseCronLog(fixture(t, "journal-cron.txt"))
	if len(lines) == 0 {
		t.Fatalf("no lines were classified from the capture")
	}
	var starts, ends int
	for _, line := range lines {
		switch line.Kind {
		case LogStart:
			starts++
		case LogEnd:
			ends++
		}
		if line.Command == "" {
			t.Errorf("a classified line names no command: %q", line.Raw)
		}
		if line.Owner != "root" {
			t.Errorf("owner = %q on %q", line.Owner, line.Raw)
		}
		if line.When.IsZero() {
			t.Errorf("the timestamp did not parse: %q", line.Raw)
		}
	}
	if starts == 0 || ends == 0 {
		t.Errorf("read %d starts and %d ends; the capture has both", starts, ends)
	}
}

// TestApplyCronLogSaysOnlyWhatCronRecords is the honest half of the cron
// outcome. cron records that a command started, and cronie also that it
// returned; the exit status is nowhere, so "it ran" is the strongest thing that
// can be said.
func TestApplyCronLogSaysOnlyWhatCronRecords(t *testing.T) {
	jobs := []schedule.Job{
		{Kind: schedule.KindCronD, Owner: "root",
			Command: "run-parts /etc/cron.hourly"},
		{Kind: schedule.KindCrontab, Owner: "ana",
			Command: "/usr/local/bin/never-logged"},
	}
	ApplyCronLog(jobs, ParseCronLog(fixture(t, "journal-cron.txt")))

	if jobs[0].Outcome != schedule.OutcomeOK {
		t.Errorf("a job with a CMD and a CMDEND = %q (%s)",
			jobs[0].Outcome, jobs[0].OutcomeDetail)
	}
	if !strings.Contains(jobs[0].OutcomeDetail, "no exit status") &&
		!strings.Contains(jobs[0].OutcomeDetail, "records no exit status") {
		t.Errorf("the detail claims more than cron recorded: %q",
			jobs[0].OutcomeDetail)
	}
	if jobs[0].Last.IsZero() {
		t.Errorf("the last run was not read from the log")
	}
	// A job the log says nothing about stays unknown, which is not "it worked".
	if jobs[1].Outcome != schedule.OutcomeUnknown {
		t.Errorf("a job with no log line = %q", jobs[1].Outcome)
	}
}

// TestApplyCronLogWontCreditAnotherAccountsRun: cron's log is joined on the
// command text because there is no id to join on, so the account has to agree
// too.
func TestApplyCronLogWontCreditAnotherAccountsRun(t *testing.T) {
	jobs := []schedule.Job{
		{Kind: schedule.KindCrontab, Owner: "ana",
			Command: "run-parts /etc/cron.hourly"},
	}
	ApplyCronLog(jobs, ParseCronLog(fixture(t, "journal-cron.txt")))
	if !jobs[0].Last.IsZero() {
		t.Errorf("root's run was credited to ana's identical command")
	}
}

// TestApplyCronLogReadsAnError: when cron does log a problem, it is a failure
// and the row has to say so.
func TestApplyCronLogReadsAnError(t *testing.T) {
	log := "2026-08-30T04:30:00+0000 demo CROND[41]: (root) CMD (/usr/local/bin/report)\n" +
		"2026-08-30T04:30:00+0000 demo crond[40]: (root) (CRON) error (grandchild " +
		"#41 failed with exit status 1) (/usr/local/bin/report)\n"
	jobs := []schedule.Job{
		{Kind: schedule.KindCronD, Owner: "root", Command: "/usr/local/bin/report"},
	}
	ApplyCronLog(jobs, ParseCronLog(log))
	if jobs[0].Outcome != schedule.OutcomeFailed {
		t.Errorf("outcome = %q (%s), want failed",
			jobs[0].Outcome, jobs[0].OutcomeDetail)
	}
}

// TestJobFromAnacronDir covers the fifth kind: a script whose schedule is the
// directory it sits in, which nothing publishes a next run or a result for.
func TestJobFromAnacronDir(t *testing.T) {
	job := JobFromAnacronDir("/etc/cron.daily", "man-db")
	if job.Kind != schedule.KindAnacronDir {
		t.Errorf("kind = %q", job.Kind)
	}
	if job.Schedule != "@daily" {
		t.Errorf("schedule = %q", job.Schedule)
	}
	if !strings.Contains(job.Explain, "anacron") {
		t.Errorf("the explanation does not name what runs it: %q", job.Explain)
	}
	if job.File != "/etc/cron.daily/man-db" {
		t.Errorf("file = %q", job.File)
	}
	if job.Outcome != schedule.OutcomeUnknown {
		t.Errorf("outcome = %q; nothing records one per script", job.Outcome)
	}
}

// TestFilterLog keeps only the lines about one job, which is what the detail
// screen shows.
func TestFilterLog(t *testing.T) {
	job := schedule.Job{Kind: schedule.KindCronD, Owner: "root",
		Command: "run-parts /etc/cron.hourly"}
	out := FilterLog(fixture(t, "journal-cron.txt"), job, 100)
	if strings.TrimSpace(out) == "" {
		t.Fatalf("no lines were kept for a job the log covers")
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "cron.hourly") {
			t.Errorf("an unrelated line was kept: %q", line)
		}
	}
}

// TestCronieStartupLinesAreNotOutcomes.
//
// The first journal a cron machine has is the one this fixture holds: cronie
// announcing itself, and nothing else. Captured from the tui-lab Fedora 44
// guest on its first boot with cronie installed — the first time the family's
// crond path ran on a real machine rather than against a reconstruction.
//
// Every line in it names CRON and none of them is a job. A parser that keyed
// on the daemon's name rather than on the `CMD (` marker would read four
// outcomes out of a machine where nothing has run yet, and would stamp them on
// whichever job sorted first. The right answer is no log lines at all, and
// every job left unknown with a detail saying the log holds nothing for it.
func TestCronieStartupLinesAreNotOutcomes(t *testing.T) {
	raw := fixture(t, "journal-crond-fedora44-boot.txt")
	if !strings.Contains(raw, "(CRON) STARTUP") {
		t.Fatalf("the capture is not a cronie startup journal")
	}

	if lines := ParseCronLog(raw); len(lines) != 0 {
		t.Errorf("a journal with no job in it yielded %d log lines: %v",
			len(lines), lines)
	}

	jobs := []schedule.Job{
		{Kind: schedule.KindCronD, Owner: "root",
			Command: "run-parts /etc/cron.hourly"},
	}
	ApplyCronLog(jobs, ParseCronLog(raw))
	if jobs[0].Outcome != schedule.OutcomeUnknown {
		t.Errorf("a job cron has not run yet = %q (%s)",
			jobs[0].Outcome, jobs[0].OutcomeDetail)
	}
	if !jobs[0].Last.IsZero() {
		t.Errorf("a last run was invented from a startup line: %v", jobs[0].Last)
	}
}

// TestCronieVersionFixtureDocumentsWhyThereIsNoVersionCommand.
//
// cronie really does answer `crontab -V`, and this capture is its answer on
// Fedora 42. The manifest still declares no version command, because Debian's
// vixie cron has no such flag — a version on one distribution and a blank on
// the other is worse than a blank on both, and this test is where that decision
// is written down next to the evidence for it.
func TestCronieVersionFixtureDocumentsWhyThereIsNoVersionCommand(t *testing.T) {
	out := strings.TrimSpace(fixture(t, "crontab-version.txt"))
	if !strings.HasPrefix(out, "cronie ") {
		t.Errorf("the capture is not cronie's version banner: %q", out)
	}
}

// names summarises a job list for a failure message.
func names(jobs []schedule.Job) []string {
	out := make([]string, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, job.Schedule+" "+job.Command)
	}
	return out
}

// TestRunPartsScriptsOnAMachineWithNoCron.
//
// Omarchy Server 4.0.1 ships /etc/cron.hourly/snapper and no cron: no crontab
// binary, no /etc/crontab, no crond or cron unit. tui-lab found the tool
// reporting that machine as having no cron jobs at all, because Load returned
// before it walked the run-parts directories.
//
// A run-parts directory needs no daemon to be read, so the script is listed.
// What it must not be is active: nothing on that machine walks the directory,
// and the row has to say so rather than let the filesystem's convention stand
// in for a schedule.
func TestRunPartsScriptsOnAMachineWithNoCron(t *testing.T) {
	jobs := []schedule.Job{JobFromAnacronDir("/etc/cron.hourly", "snapper")}
	markUnrunnable(jobs, schedule.CronState{Installed: false})

	if jobs[0].Active || jobs[0].Enabled {
		t.Errorf("a script no daemon runs is reported as active")
	}
	if jobs[0].State != "installed" {
		t.Errorf("the file is on disk; State = %q", jobs[0].State)
	}
	if !strings.Contains(jobs[0].OutcomeDetail, "no cron on this machine") {
		t.Errorf("the row does not say why it never runs: %q",
			jobs[0].OutcomeDetail)
	}
	// Explain is what the schedule column shows, so it is the line that must
	// not read as a promise. The Schedule itself stays: @hourly is what the
	// directory means, and saying so is how the row explains what is wrong.
	if jobs[0].Schedule != "@hourly" {
		t.Errorf("the directory stopped meaning what it means: %q", jobs[0].Schedule)
	}
	if !strings.Contains(jobs[0].Explain, "no cron here") {
		t.Errorf("the schedule column still promises a run: %q", jobs[0].Explain)
	}

	// On a machine that does have cron, the same script is left alone.
	running := []schedule.Job{JobFromAnacronDir("/etc/cron.hourly", "0anacron")}
	markUnrunnable(running, schedule.CronState{Installed: true})
	if !running[0].Active {
		t.Errorf("a script on a cron machine was marked unrunnable")
	}
	if !strings.Contains(running[0].Explain, "Every hour") {
		t.Errorf("a script on a cron machine lost its schedule: %q",
			running[0].Explain)
	}
}
