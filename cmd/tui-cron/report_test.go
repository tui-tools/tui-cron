package main

import (
	"strings"
	"testing"

	"github.com/tui-tools/tui-kit/compat"
)

// TestRunReportDemo checks the half of the block this tool owns. The kit's own
// tests cover the machine facts and the scrubbing; what has to be right here is
// that --demo says demo, that it names the schedulers the fake imitates, and
// that no scheduler was read to produce any of it.
func TestRunReportDemo(t *testing.T) {
	var out strings.Builder
	if err := runReport(baseConfig(), options{demo: true, report: true}, &out); err != nil {
		t.Fatalf("runReport: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"backend: demo\n",
		"mode: demo (sample data, the system was not read)\n",
		"demo backend: systemd and cron\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "schedulers:") {
		t.Errorf("a demo report must not claim to have probed the machine:\n%s", got)
	}
	if !strings.HasPrefix(got, toolName+" ") {
		t.Errorf("report should start with the tool name:\n%s", got)
	}
}

// TestRunReportLive is the privacy guard on the real path: the block a user
// pastes into a public issue must name no host, no account and no home path,
// on a machine that may or may not have either scheduler.
func TestRunReportLive(t *testing.T) {
	t.Setenv("HOME", "/home/somebody")
	t.Setenv("USER", "somebody")
	t.Setenv("HOSTNAME", "a-machine-nobody-should-read-about")

	var out strings.Builder
	if err := runReport(baseConfig(), options{report: true}, &out); err != nil {
		t.Fatalf("runReport: %v", err)
	}

	got := out.String()
	for _, forbidden := range []string{
		"somebody", "a-machine-nobody-should-read-about", "/home/",
	} {
		if strings.Contains(got, forbidden) {
			t.Errorf("the report leaked %q:\n%s", forbidden, got)
		}
	}
	// Either the schedulers were described or the machine has neither, and
	// then the selection error is the report. One of the two must be there.
	if !strings.Contains(got, "schedulers: ") {
		t.Errorf("a live report must describe the schedulers:\n%s", got)
	}
	if !strings.Contains(got, "mode: live\n") {
		t.Errorf("a live report must say so on the mode line:\n%s", got)
	}
}

// TestDescribeSchedulers renders both halves of the backend, which is what
// tells "systemd 257 is driving this" from "there is no cron on this machine".
func TestDescribeSchedulers(t *testing.T) {
	tests := []struct {
		name    string
		results []compat.Result
		want    string
	}{
		{
			name: "systemd carries a version",
			results: []compat.Result{
				{Backend: "systemd", Version: "257"},
				{Backend: "cron", Detail: "no version command is declared for this backend"},
			},
			// cron's half is read off this machine, so only systemd's is
			// asserted verbatim.
			want: "systemd 257, cron ",
		},
		{
			name: "a scheduler that could not be versioned says why",
			results: []compat.Result{
				{Backend: "systemd", Detail: "systemctl not found"},
			},
			want: "systemd (version unknown: systemctl not found)",
		},
		{
			name:    "a scheduler with no version and no reason",
			results: []compat.Result{{Backend: "systemd"}},
			want:    "systemd (version unknown)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeSchedulers(tc.results); !strings.HasPrefix(got, tc.want) {
				t.Errorf("describeSchedulers = %q, want it to start with %q", got, tc.want)
			}
		})
	}

	// Nothing probed at all still answers, by looking for the two binaries.
	if got := describeSchedulers(nil); !strings.Contains(got, "systemd ") ||
		!strings.Contains(got, "cron ") {
		t.Errorf("describeSchedulers(nil) = %q, want both schedulers named", got)
	}
}

// TestPresence separates "installed but it will not say what it is" from "not
// here", which is the only thing cron's line can be about.
func TestPresence(t *testing.T) {
	if got := presence(true); got != "installed (no version command)" {
		t.Errorf("presence(true) = %q", got)
	}
	if got := presence(false); got != "absent" {
		t.Errorf("presence(false) = %q", got)
	}
}
