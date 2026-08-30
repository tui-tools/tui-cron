package crontab

import (
	"strings"
	"testing"
)

// TestListArgv pins the read that returns a table. Your own crontab needs no
// privileges and no `-u`; somebody else's needs both, and cron's own flag is
// how that is spelled.
func TestListArgv(t *testing.T) {
	own, err := BuildListArgv("ana", "ana")
	if err != nil {
		t.Fatalf("BuildListArgv: %v", err)
	}
	if got := strings.Join(own, " "); got != "crontab -l" {
		t.Errorf("reading your own table = %q", got)
	}

	other, err := BuildListArgv("backup", "ana")
	if err != nil {
		t.Fatalf("BuildListArgv: %v", err)
	}
	if got := strings.Join(other, " "); got != "crontab -u backup -l" {
		t.Errorf("reading another account's table = %q", got)
	}

	for _, owner := range []string{"", "ana; id", "../root", "A", "1user"} {
		if _, err := BuildListArgv(owner, "ana"); err == nil {
			t.Errorf("BuildListArgv accepted %q as an account", owner)
		}
	}
}

// TestInstallTableArgv pins the write.
//
// `crontab <file>` is the only supported way to change a table: it runs cron's
// own parser, sets the ownership and mode cron requires, and signals the daemon.
// Writing the spool file directly does none of that, and on Debian the result
// is simply ignored.
func TestInstallTableArgv(t *testing.T) {
	cmd, err := BuildInstallTable("ana", "ana", "/tmp/tui-cron-1/crontab")
	if err != nil {
		t.Fatalf("BuildInstallTable: %v", err)
	}
	if got := cmd.String(); got != "crontab /tmp/tui-cron-1/crontab" {
		t.Errorf("replacing your own table = %q", got)
	}
	if !cmd.Destructive {
		t.Errorf("replacing a whole crontab must be painted as dangerous")
	}

	cmd, err = BuildInstallTable("backup", "ana", "/tmp/tui-cron-1/crontab")
	if err != nil {
		t.Fatalf("BuildInstallTable: %v", err)
	}
	if got := cmd.String(); got != "crontab -u backup /tmp/tui-cron-1/crontab" {
		t.Errorf("replacing another account's table = %q", got)
	}

	for _, path := range []string{"", "crontab", "/tmp/x;id", "../../etc/passwd"} {
		if _, err := BuildInstallTable("ana", "ana", path); err == nil {
			t.Errorf("BuildInstallTable accepted %q as a staging path", path)
		}
	}
}

// TestInstallCronDRefusesAnythingElse: the destination is checked here, not
// trusted from a caller.
func TestInstallCronDRefusesAnythingElse(t *testing.T) {
	cmd, err := BuildInstallCronD("/tmp/tui-cron-1/0hourly", "/etc/cron.d/0hourly")
	if err != nil {
		t.Fatalf("BuildInstallCronD: %v", err)
	}
	want := "install -m 644 /tmp/tui-cron-1/0hourly /etc/cron.d/0hourly"
	if got := cmd.String(); got != want {
		t.Errorf("BuildInstallCronD = %q, want %q", got, want)
	}
	if _, err := BuildInstallCronD("/tmp/tui-cron-1/crontab", SystemCrontab); err != nil {
		t.Errorf("/etc/crontab was refused: %v", err)
	}
	for _, destination := range []string{
		"/etc/passwd", "/etc/cron.d/../../passwd", "/etc/cron.d/sub/dir",
		"/etc/cron.daily/x", "cron.d/x",
	} {
		if _, err := BuildInstallCronD("/tmp/tui-cron-1/x", destination); err == nil {
			t.Errorf("BuildInstallCronD accepted %q", destination)
		}
	}
}

// TestCronDNamesFollowCronsOwnRule: cron and run-parts ignore a file in
// /etc/cron.d whose name contains a dot, so a table saved as `backup.cron`
// silently never runs. The form refuses the name rather than writing it.
func TestCronDNamesFollowCronsOwnRule(t *testing.T) {
	for _, name := range []string{"0hourly", "collect-metrics", "tui_cron"} {
		if !ValidCronDName(name) {
			t.Errorf("ValidCronDName(%q) = false", name)
		}
	}
	for _, name := range []string{"backup.cron", "x.conf", "", "a/b", "a b"} {
		if ValidCronDName(name) {
			t.Errorf("ValidCronDName(%q) = true", name)
		}
	}
}

// TestCheckCommandRefusesWhatWouldChangeTheLine: a command goes into a table as
// the rest of a line. A newline would add a second job, and a `%` ends the
// command early — cron reads everything after one as the job's standard input,
// which is the sort of thing nobody discovers until the job has silently done
// half its work for a month.
func TestCheckCommandRefusesWhatWouldChangeTheLine(t *testing.T) {
	for _, command := range []string{
		"", "  ", "/bin/true\n0 3 * * * /bin/false", "/usr/bin/date +%Y",
	} {
		if err := CheckCommand(command); err == nil {
			t.Errorf("CheckCommand accepted %q", command)
		}
	}
	// A pipeline and a redirection are fine: cron runs the command through
	// /bin/sh, so both mean what they look like.
	for _, command := range []string{
		"/usr/local/bin/backup", "/bin/sh -c 'a | b'",
		"/usr/local/bin/report >> /var/log/report.log 2>&1",
	} {
		if err := CheckCommand(command); err != nil {
			t.Errorf("CheckCommand(%q) = %v", command, err)
		}
	}
}

// TestRenderLine builds a line in each of the two formats.
func TestRenderLine(t *testing.T) {
	own, err := RenderLine("*/5 * * * *", "ana", "/usr/local/bin/check", false)
	if err != nil {
		t.Fatalf("RenderLine: %v", err)
	}
	if own != "*/5 * * * * /usr/local/bin/check" {
		t.Errorf("a user table line = %q, want no user field", own)
	}
	system, err := RenderLine("*/5 * * * *", "root", "/usr/local/bin/check", true)
	if err != nil {
		t.Fatalf("RenderLine: %v", err)
	}
	if system != "*/5 * * * * root /usr/local/bin/check" {
		t.Errorf("a system table line = %q, want the user field", system)
	}
	// The schedule goes through the family's own parser, which is the same one
	// the describer uses: a line the tool cannot read is a line it will not
	// write.
	if _, err := RenderLine("61 * * * *", "ana", "/bin/true", false); err == nil {
		t.Errorf("RenderLine accepted a schedule that is not one")
	}
}

// TestReplaceLineKeepsEverythingElse is the property that matters most in this
// file. A crontab's MAILTO= and PATH= lines are load-bearing, and a table
// rewritten from the jobs alone would silently drop them.
func TestReplaceLineKeepsEverythingElse(t *testing.T) {
	before := "SHELL=/bin/bash\nMAILTO=ana\n\n# The watchdog.\n" +
		"*/5 * * * * /usr/local/bin/check-queue\n@reboot /usr/local/bin/warm\n"

	after, err := ReplaceLine(before, 5, "*/10 * * * * /usr/local/bin/check-queue")
	if err != nil {
		t.Fatalf("ReplaceLine: %v", err)
	}
	for _, want := range []string{
		"SHELL=/bin/bash", "MAILTO=ana", "# The watchdog.",
		"*/10 * * * * /usr/local/bin/check-queue",
		"@reboot /usr/local/bin/warm",
	} {
		if !strings.Contains(after, want) {
			t.Errorf("the rewritten table lost %q:\n%s", want, after)
		}
	}
	if strings.Contains(after, "*/5 * * * *") {
		t.Errorf("the old line survived:\n%s", after)
	}
}

// TestReplaceLineAddsAndRemoves covers the other two edits, which are the same
// code path: line 0 appends, an empty replacement removes.
func TestReplaceLineAddsAndRemoves(t *testing.T) {
	before := "MAILTO=ana\n*/5 * * * * /usr/local/bin/check-queue\n"

	added, err := ReplaceLine(before, 0, "0 3 * * * /usr/local/bin/nightly")
	if err != nil {
		t.Fatalf("ReplaceLine: %v", err)
	}
	if !strings.HasSuffix(added, "0 3 * * * /usr/local/bin/nightly\n") {
		t.Errorf("the new line was not appended:\n%q", added)
	}
	if !strings.Contains(added, "*/5 * * * *") {
		t.Errorf("appending dropped the existing job:\n%s", added)
	}

	removed, err := ReplaceLine(before, 2, "")
	if err != nil {
		t.Fatalf("ReplaceLine: %v", err)
	}
	if strings.Contains(removed, "check-queue") {
		t.Errorf("the line was not removed:\n%s", removed)
	}
	if !strings.Contains(removed, "MAILTO=ana") {
		t.Errorf("removing a job dropped the environment:\n%s", removed)
	}

	if _, err := ReplaceLine(before, 99, "x"); err == nil {
		t.Errorf("ReplaceLine accepted a line that is not in the table")
	}
}

// TestReplaceLineOnAnEmptyTable: a first line has to end up in a file cron will
// accept, ending in a newline, and it gets the banner since the tool made the
// file.
func TestReplaceLineOnAnEmptyTable(t *testing.T) {
	after, err := ReplaceLine("", 0, "0 3 * * * /usr/local/bin/nightly")
	if err != nil {
		t.Fatalf("ReplaceLine: %v", err)
	}
	if !strings.HasPrefix(after, "# Written by tui-cron") {
		t.Errorf("a table this tool created carries no banner:\n%s", after)
	}
	if !strings.HasSuffix(after, "\n") {
		t.Errorf("the table does not end in a newline: %q", after)
	}
	// And an existing table never gets one: the file belongs to whoever wrote
	// it, and stamping a tool's name on somebody's crontab would be claiming it.
	after, err = ReplaceLine("MAILTO=ana\n", 0, "0 3 * * * /bin/true")
	if err != nil {
		t.Fatalf("ReplaceLine: %v", err)
	}
	if strings.Contains(after, "Written by tui-cron") {
		t.Errorf("an existing table was stamped:\n%s", after)
	}
}

// TestCheckLineIsTheOnlyValidatorThereIs documents the portability decision:
// cronie ships `crontab -T` and Debian's cron ships nothing equivalent, so the
// check is this one, in Go, on every machine.
func TestCheckLineIsTheOnlyValidatorThereIs(t *testing.T) {
	if err := CheckLine("*/5 * * * *", "root", "/bin/true", true); err != nil {
		t.Errorf("a good system-table line was refused: %v", err)
	}
	if err := CheckLine("*/5 * * * *", "not an account", "/bin/true", true); err == nil {
		t.Errorf("a bad user field was accepted")
	}
	// A user table has no user field, so the owner is not checked against the
	// line — it decides which table is written, not what goes in it.
	if err := CheckLine("@daily", "", "/bin/true", false); err != nil {
		t.Errorf("a good user-table line was refused: %v", err)
	}
}

// TestUnderCronD is the guard the install destination check leans on.
func TestUnderCronD(t *testing.T) {
	if !UnderCronD("/etc/cron.d/0hourly") {
		t.Errorf("a real cron.d file was rejected")
	}
	for _, path := range []string{
		"/etc/cron.d", "/etc/cron.d/", "/etc/cron.d/a/b", "/etc/cron.d/../passwd",
		"/etc/crontab", "/etc/cron.daily/x",
	} {
		if UnderCronD(path) {
			t.Errorf("UnderCronD(%q) = true", path)
		}
	}
}
