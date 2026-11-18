package crontab

import (
	"strings"
	"testing"

	"github.com/tui-tools/tui-kit/runner"
)

// TestOwnTableRunsAsYou: your own crontab is replaced by running crontab as
// you. Found on a lab Fedora 44 machine: "Add a line to lab's crontab" ran
// `sudo -n crontab <file>`, crontab picked the table from the user it ran as,
// and the line landed in root's crontab, to run as root. Somebody else's table
// still goes through the escalation prefix, with -u naming whose.
func TestOwnTableRunsAsYou(t *testing.T) {
	sudo := []string{"/usr/bin/sudo", "-n"}
	b := &Backend{
		crontab:    &runner.Runner{Bin: "/usr/bin/crontab", Name: "crontab", Privilege: sudo},
		ownCrontab: &runner.Runner{Bin: "/usr/bin/crontab", Name: "crontab"},
	}
	staged := "/tmp/tui-cron-123/crontab"

	own, err := BuildInstallTable("lab", "lab", staged)
	if err != nil {
		t.Fatalf("own table: %v", err)
	}
	if got := b.Preview(own); strings.Contains(got, "sudo") ||
		got != "crontab "+staged {
		t.Errorf("own table previews %q, want crontab run as you", got)
	}

	other, err := BuildInstallTable("ana", "lab", staged)
	if err != nil {
		t.Fatalf("other table: %v", err)
	}
	got := b.Preview(other)
	if !strings.HasPrefix(got, "/usr/bin/sudo -n ") ||
		!strings.Contains(got, "crontab -u ana "+staged) {
		t.Errorf("another account's table previews %q, want it escalated with -u", got)
	}
}

func TestOwnTable(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want bool
	}{
		{[]string{"crontab", "/tmp/tui-cron-1/crontab"}, true},
		{[]string{"crontab", "-l"}, true},
		{[]string{"crontab", "-u", "ana", "-l"}, false},
		{[]string{"crontab", "-u", "ana", "/tmp/tui-cron-1/crontab"}, false},
		{[]string{"install", "-m", "0644", "a", "b"}, false},
		{nil, false},
	} {
		if got := OwnTable(tc.argv); got != tc.want {
			t.Errorf("OwnTable(%q) = %v, want %v", tc.argv, got, tc.want)
		}
	}
}
