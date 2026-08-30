// Package crontab is the cron half of tui-cron's backend, and one of the two
// places in the repository that starts a process.
//
// Everything about reaching the machine — resolving the binaries, applying the
// privilege prefix, bounding each call, turning a failure into one readable
// line — belongs to the kit runner. What is left here is the translation
// between cron's files and the scheduler-neutral model in internal/schedule,
// and the assembly of the argv that a confirm dialog will show before it runs.
//
// The programs driven, each through its own runner:
//
//	crontab      reading and replacing a user's crontab, the only supported
//	             way to touch one
//	install      putting a file into /etc/cron.d
//	journalctl   what cron logged about a job
//	systemctl    whether the cron daemon is installed and running
//
// A user's crontab is never edited as a file. /var/spool/cron is cron's own
// directory, and writing into it behind cron's back skips the syntax check and,
// on Debian, the permission and ownership rules cron enforces — `crontab <file>`
// is the interface, and it is the one this package uses.
//
// There is no portable version to report. cronie's `crontab -V` prints
// "cronie 1.7.2"; Debian's vixie cron has no such flag and no other program in
// the package prints a version either. A manifest entry that worked on Fedora
// and reported nothing on Ubuntu would be worse than none, so the backend
// declares no version command at all and the header shows cron without one.
package crontab

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tui-tools/tui-cron/internal/schedule"
	"github.com/tui-tools/tui-kit/runner"
)

// The files and directories cron reads, in the order this package reads them.
const (
	// SystemCrontab is the machine-wide table, which carries a user field.
	SystemCrontab = "/etc/crontab"
	// CronDDir is where packages drop their own tables, same format.
	CronDDir = "/etc/cron.d"
)

// AnacronDirs are the run-parts directories, whose name is the whole schedule.
var AnacronDirs = []string{
	"/etc/cron.hourly", "/etc/cron.daily", "/etc/cron.weekly", "/etc/cron.monthly",
}

// SpoolDirs are where the per-user crontabs live: Fedora and Arch use the
// first, Debian and Ubuntu the second. The directory is listed only to learn
// *which* users have one — the tables themselves are always read back through
// `crontab -u`, never by opening the file.
var SpoolDirs = []string{"/var/spool/cron", "/var/spool/cron/crontabs"}

// unitCandidates are the names the cron daemon's unit goes by: `crond` on
// Fedora and Arch, `cron` on Debian and Ubuntu. Which one this machine uses is
// read, not assumed.
var unitCandidates = []string{"crond", "cron"}

// journalUnits are the units cron's log is read from. Both names are passed
// because journalctl is happy to be given a unit that has never logged
// anything, and a machine can carry either.
var journalUnits = []string{"cron", "crond"}

// journalLines bounds how much of cron's log one read pulls back. A busy
// machine running a job every minute writes two lines a minute, so a week is
// a lot; what the screen needs is the most recent line per job.
const journalLines = "3000"

// journalSince is how far back the outcome read looks. A job that has not run
// in a week is a job whose last run is not on this screen, and saying so is
// better than reading a month of journal to find out.
const journalSince = "-7d"

// searchPaths are the locations a non-root PATH commonly omits.
var searchPaths = map[string][]string{
	"crontab":    {"/usr/bin/crontab", "/bin/crontab"},
	"install":    {"/usr/bin/install", "/bin/install"},
	"journalctl": {"/usr/bin/journalctl", "/bin/journalctl"},
	"systemctl":  {"/usr/bin/systemctl", "/bin/systemctl"},
}

// installHint is appended to the "not found" error.
const installHint = "install the cronie or cron package; " +
	"or use --demo to explore the UI"

// Backend drives cron on the host.
type Backend struct {
	crontab   *runner.Runner
	install   *runner.Runner
	journal   *runner.Runner
	systemctl *runner.Runner
}

// New locates the binaries. sudoPrefix comes from the configuration
// ("sudo -n"); pass nil to run the commands directly.
//
// Reading your own crontab needs no privileges and neither does reading
// /etc/crontab or /etc/cron.d, which are world readable everywhere. Listing
// which *other* users have a crontab does, and that read is allowed to fail:
// the model then says only your own table was read.
func New(sudoPrefix []string) *Backend {
	b := &Backend{}
	unprivileged := false
	for _, spec := range []struct {
		bin    string
		target **runner.Runner
		reads  *bool
	}{
		// `crontab -l` reads the caller's own table, which needs nothing; only
		// `crontab -u` does, and that one is a confirmed action.
		{"crontab", &b.crontab, &unprivileged},
		{"install", &b.install, nil},
		{"journalctl", &b.journal, &unprivileged},
		{"systemctl", &b.systemctl, &unprivileged},
	} {
		r, err := runner.New(runner.Options{
			Bin:             spec.bin,
			SearchPaths:     searchPaths[spec.bin],
			SudoPrefix:      sudoPrefix,
			InstallHint:     installHint,
			PrivilegedReads: spec.reads,
		})
		if err != nil {
			// Nothing here is essential. A machine with no cron at all is a
			// normal machine — Omarchy Server is one — and the screen says so
			// rather than refusing to start.
			continue
		}
		*spec.target = r
	}
	return b
}

// Installed reports whether cron is on this host.
func Installed() bool {
	return runner.Available("crontab", searchPaths["crontab"]...)
}

// Describe names the backend for the header.
func (b *Backend) Describe() string {
	if b.crontab == nil {
		return "cron (not installed)"
	}
	return b.crontab.Describe()
}

// Preview renders the exact command line Run will execute.
func (b *Backend) Preview(cmd schedule.Command) string {
	if run := b.runnerFor(cmd); run != nil {
		return run.Preview(cmd)
	}
	return cmd.String()
}

// Owns reports whether this backend can run a command.
func (b *Backend) Owns(cmd schedule.Command) bool { return b.runnerFor(cmd) != nil }

// runnerFor picks the runner that owns a command, by its argv[0]. Only the two
// programs this package *builds* commands for are routed: it reads through
// journalctl and systemctl, but it never asks the user to confirm one.
func (b *Backend) runnerFor(cmd schedule.Command) *runner.Runner {
	if len(cmd.Argv) == 0 {
		return nil
	}
	switch cmd.Argv[0] {
	case "crontab":
		return b.crontab
	case "install":
		return b.install
	default:
		return nil
	}
}

// Run executes a previewed command.
func (b *Backend) Run(ctx context.Context, cmd schedule.Command) (string, error) {
	run := b.runnerFor(cmd)
	if run == nil {
		return "", fmt.Errorf("crontab: %q is not available on this machine",
			cmd.Argv[0])
	}
	return run.Run(ctx, cmd)
}

// Load reads every cron job on the machine and what cron itself is doing.
func (b *Backend) Load(ctx context.Context) ([]schedule.Job, schedule.CronState) {
	state := b.daemonState(ctx)
	if b.crontab == nil && !fileExists(SystemCrontab) {
		return nil, state
	}

	var jobs []schedule.Job
	jobs = append(jobs, b.userTables(ctx)...)
	jobs = append(jobs, b.systemTables()...)
	jobs = append(jobs, b.anacronDirs()...)

	b.applyOutcomes(ctx, jobs)
	return jobs, state
}

// daemonState reports whether cron is installed and whether it is running.
func (b *Backend) daemonState(ctx context.Context) schedule.CronState {
	state := schedule.CronState{Installed: Installed()}
	if !state.Installed {
		state.Detail = "no crontab command on this machine: cron is not " +
			"installed, and every scheduled job here is a systemd timer"
		return state
	}
	if b.systemctl == nil {
		state.Detail = "cron is installed; without systemctl there is no way " +
			"to say whether its daemon is running"
		return state
	}
	for _, unit := range unitCandidates {
		out, err := b.systemctl.Read(ctx, "systemctl", "show", unit+".service",
			"--property=LoadState", "--property=ActiveState",
			"--property=UnitFileState")
		if err != nil {
			continue
		}
		properties := parseUnitProperties(out)
		if properties["LoadState"] == "not-found" {
			continue
		}
		state.Unit = unit + ".service"
		state.State = properties["ActiveState"]
		state.Active = state.State == "active"
		state.Enabled = strings.HasPrefix(properties["UnitFileState"], "enabled")
		if !state.Active {
			state.Detail = state.Unit + " is " + state.State +
				", so nothing in these tables is running"
		}
		return state
	}
	state.Detail = "cron is installed, but no crond or cron unit was found; " +
		"the daemon may be started some other way"
	return state
}

// parseUnitProperties reads the `key=value` output of `systemctl show`.
func parseUnitProperties(out string) map[string]string {
	properties := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if found {
			properties[key] = value
		}
	}
	return properties
}

// userTables reads the per-user crontabs: this user's always, and everybody
// else's when the spool directory can be listed, which means running as root.
func (b *Backend) userTables(ctx context.Context) []schedule.Job {
	if b.crontab == nil {
		return nil
	}
	var jobs []schedule.Job
	for _, owner := range b.tableOwners() {
		out, err := b.readTable(ctx, owner)
		if err != nil {
			continue
		}
		jobs = append(jobs, ParseUserTable(out, owner, TablePathFor(owner))...)
	}
	return jobs
}

// tableOwners is whose crontabs are read: this account, plus every other one
// the spool directory names when it can be listed.
func (b *Backend) tableOwners() []string {
	me := CurrentUser()
	owners := []string{me}
	seen := map[string]bool{me: true}
	for _, dir := range SpoolDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || seen[name] || !validUser(name) {
				continue
			}
			seen[name] = true
			owners = append(owners, name)
		}
	}
	sort.Strings(owners[1:])
	return owners
}

// readTable reads one account's crontab through cron's own interface.
func (b *Backend) readTable(ctx context.Context, owner string) (string, error) {
	argv, err := BuildListArgv(owner, CurrentUser())
	if err != nil {
		return "", err
	}
	out, err := b.crontab.Read(ctx, argv...)
	if err != nil {
		// "no crontab for <user>" is not a failure, it is an empty table.
		if strings.Contains(out, "no crontab for") {
			return "", nil
		}
		return "", err
	}
	return out, nil
}

// systemTables reads /etc/crontab and everything in /etc/cron.d. Both are
// world readable on every distribution this tool targets, so they are read
// directly rather than through an escalated command.
func (b *Backend) systemTables() []schedule.Job {
	var jobs []schedule.Job
	if raw, err := os.ReadFile(SystemCrontab); err == nil {
		jobs = append(jobs, ParseSystemTable(string(raw), SystemCrontab)...)
	}

	entries, err := os.ReadDir(CronDDir)
	if err != nil {
		return jobs
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !ValidCronDName(entry.Name()) {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(CronDDir, name)
		raw, readErr := os.ReadFile(path) //nolint:gosec // the path is a name cron itself would accept, inside /etc/cron.d
		if readErr != nil {
			continue
		}
		jobs = append(jobs, ParseSystemTable(string(raw), path)...)
	}
	return jobs
}

// anacronDirs lists the executables in the run-parts directories.
//
// They are reported and never edited. The schedule is the directory, the
// ordering is run-parts', and on most distributions anacron rather than cron is
// what actually runs them — a form that offered to "change the schedule" of a
// script in /etc/cron.daily would be offering to move the file, which is a
// thing to do with mv and not with a confirm dialog.
func (b *Backend) anacronDirs() []schedule.Job {
	var jobs []schedule.Job
	for _, dir := range AnacronDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		var names []string
		for _, entry := range entries {
			if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			names = append(names, entry.Name())
		}
		sort.Strings(names)
		for _, name := range names {
			jobs = append(jobs, JobFromAnacronDir(dir, name))
		}
	}
	return jobs
}

// applyOutcomes folds cron's own log into the jobs: when each last started, and
// whatever cron was willing to say about how it ended.
func (b *Backend) applyOutcomes(ctx context.Context, jobs []schedule.Job) {
	if b.journal == nil || len(jobs) == 0 {
		return
	}
	argv := []string{"journalctl"}
	for _, unit := range journalUnits {
		argv = append(argv, "-u", unit)
	}
	argv = append(argv, "--no-pager", "-o", "short-iso",
		"-n", journalLines, "--since", journalSince)
	out, err := b.journal.Read(ctx, argv...)
	if err != nil {
		for i := range jobs {
			jobs[i].OutcomeDetail = "cron's log could not be read: " +
				runner.FirstLine(err.Error())
		}
		return
	}
	ApplyCronLog(jobs, ParseCronLog(out))
}

// Definition returns the file a job is written in, with the job's own line
// marked. For a user's crontab that is `crontab -l`, because the file itself is
// cron's private business and reading it directly is not the interface.
func (b *Backend) Definition(ctx context.Context, job schedule.Job) (string, error) {
	switch job.Kind {
	case schedule.KindCrontab:
		if b.crontab == nil {
			return "", fmt.Errorf("crontab: cron is not installed on this machine")
		}
		argv, err := BuildListArgv(job.Owner, CurrentUser())
		if err != nil {
			return "", err
		}
		out, readErr := b.crontab.Read(ctx, argv...)
		if readErr != nil {
			return "", readErr
		}
		return "# " + strings.Join(argv, " ") + "\n" + out, nil
	case schedule.KindCronD, schedule.KindAnacronDir:
		raw, err := os.ReadFile(job.File) //nolint:gosec // the path came from this package's own directory walk
		if err != nil {
			return "", err
		}
		return "# " + job.File + "\n" + string(raw), nil
	default:
		return "", fmt.Errorf("crontab: %s is not a cron job", job.Name)
	}
}

// Journal returns what cron logged about one job.
//
// It is cron's log filtered to the lines naming this job's command, not the
// job's own output: cron does not keep that anywhere. What a job printed goes
// to the mail cron sends, or to wherever the command was told to write it, and
// the detail screen says so rather than pretending the journal has it.
func (b *Backend) Journal(ctx context.Context, job schedule.Job,
	lines int) (string, error) {
	if b.journal == nil {
		return "", fmt.Errorf("crontab: journalctl is not installed on this machine")
	}
	argv := []string{"journalctl"}
	for _, unit := range journalUnits {
		argv = append(argv, "-u", unit)
	}
	argv = append(argv, "--no-pager", "-o", "short-iso",
		"-n", journalLines, "--since", journalSince)
	out, err := b.journal.Read(ctx, argv...)
	if err != nil {
		return "", err
	}
	return FilterLog(out, job, lines), nil
}

// ReadTableFor returns one account's crontab as it stands, which is what an
// edit is staged against.
func (b *Backend) ReadTableFor(ctx context.Context, owner string) (string, error) {
	if b.crontab == nil {
		return "", fmt.Errorf("crontab: cron is not installed on this machine")
	}
	return b.readTable(ctx, owner)
}

// ReadFile returns a cron.d file as it stands.
func (b *Backend) ReadFile(path string) string {
	if !UnderCronD(path) && path != SystemCrontab {
		return ""
	}
	raw, err := os.ReadFile(path) //nolint:gosec // the path is checked against /etc/cron.d and /etc/crontab
	if err != nil {
		return ""
	}
	return string(raw)
}

// CurrentUser names the account the tool runs as, which is whose crontab
// `crontab -l` reads.
//
// SUDO_USER is deliberately *not* consulted. Under `sudo tui-cron` the crontab
// being read really is root's, and reporting it as somebody else's would put
// the wrong name on every row and send an edit to the wrong table.
func CurrentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	for _, key := range []string{"USER", "LOGNAME"} {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return "root"
}

// fileExists reports whether a path is there at all.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
