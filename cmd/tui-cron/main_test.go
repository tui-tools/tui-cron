package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/tui-tools/tui-cron/internal/jobs"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/config"
)

// baseConfig is the configuration as it stands before the flags are folded in.
func baseConfig() config.Config {
	return config.Config{Tool: toolName, Values: defaults()}
}

func TestParseFlags(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = devNull.Close() }()

	opts, err := parseFlags([]string{"--demo", "--theme", "/t/colors.toml"}, devNull)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !opts.demo || opts.themePath != "/t/colors.toml" {
		t.Errorf("opts = %+v", opts)
	}
	if opts.sudoSet {
		t.Error("sudoSet should be false when -sudo is absent")
	}
}

func TestApplyOverrides(t *testing.T) {
	cfg := baseConfig()
	applyOverrides(&cfg, options{themePath: "/t/colors.toml"})
	if got := cfg.Theme(); got != "/t/colors.toml" {
		t.Errorf("Theme() = %q", got)
	}
	// An untouched -sudo must not clear the configured prefix.
	if got := cfg.String(config.KeySudo, ""); got != "sudo -n" {
		t.Errorf("sudo = %q, want the config value", got)
	}

	// An explicit empty -sudo disables escalation.
	cfg = baseConfig()
	applyOverrides(&cfg, options{sudoSet: true, sudo: ""})
	if got := cfg.String(config.KeySudo, "unset"); got != "" {
		t.Errorf("sudo = %q, want empty", got)
	}
	if got := cfg.SudoPrefix(); got != nil {
		t.Errorf("SudoPrefix = %q, want nil", got)
	}
}

func TestDefaultsCoverEveryFlag(t *testing.T) {
	// Every key a flag can override must be declared, otherwise the environment
	// layer silently skips it.
	for _, key := range []string{config.KeySudo, config.KeyTheme} {
		if _, ok := defaults()[key]; !ok {
			t.Errorf("defaults() is missing %q", key)
		}
	}
}

func TestPickBackendDemo(t *testing.T) {
	backend, err := pickBackend(baseConfig(), options{demo: true}, compat.Caps{})
	if err != nil {
		t.Fatalf("pickBackend: %v", err)
	}
	if !strings.Contains(backend.Describe(), "demo") {
		t.Errorf("Describe = %q, want it to say it is a demo", backend.Describe())
	}
}

// TestCheckReportsTheState covers the contract the smoke test depends on: the
// counts per kind, the failures, the persistent warnings and what each
// scheduler said about itself, all where a shell script can grep for them
// without walking the model.
func TestCheckReportsTheState(t *testing.T) {
	backend, err := pickBackend(baseConfig(), options{demo: true}, compat.Caps{})
	if err != nil {
		t.Fatalf("pickBackend: %v", err)
	}
	var out bytes.Buffer
	if err := runCheck(backend, nil, &out); err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	for _, want := range []string{
		`"tool": "tui-cron"`,
		`"backend": "host"`,
		// The sample machine is the one the README describes.
		`"jobs": 11`,
		`"timer": 5`,
		`"user-timer": 1`,
		`"crontab": 2`,
		`"cron.d": 2`,
		`"anacron-dir": 1`,
		`"failedCount": 1`,
		`"persistentWarnings": 1`,
		`"timersAvailable": true`,
		`"cronInstalled": true`,
		`"cronRunning": true`,
		// The failure is named, with the scheduler's own words for it.
		`"name": "backup.timer"`,
		`"outcome": "failed"`,
		// And the reading is in the report, because a report of schedules that
		// only carried the expressions would be as unreadable as the crontab.
		`"explain": "Every day at 02:30"`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("--check output is missing %s", want)
		}
	}
}

// TestCheckReportsEveryKindEvenTheEmptyOnes: a script asserting on a count
// needs a zero to assert on, not a key that may or may not be there.
func TestCheckReportsEveryKindEvenTheEmptyOnes(t *testing.T) {
	backend := jobs.NewFake()
	var out bytes.Buffer
	if err := runCheck(backend, nil, &out); err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	for _, kind := range []string{
		"timer", "user-timer", "crontab", "cron.d", "anacron-dir",
	} {
		if !strings.Contains(out.String(), `"`+kind+`":`) {
			t.Errorf("--check does not report a count for %q", kind)
		}
	}
}

// TestCheckRunsNothing: --check exists to be safe to run anywhere, including in
// CI against a production-shaped machine, so it must not run a single command
// through the backend.
func TestCheckRunsNothing(t *testing.T) {
	backend := jobs.NewFake()
	var out bytes.Buffer
	if err := runCheck(backend, nil, &out); err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	if ran := backend.Ran(); len(ran) != 0 {
		t.Errorf("--check ran %d commands: %v", len(ran), ran)
	}
	// And it prints no command line: one in the output would mean it had built
	// one.
	for _, forbidden := range []string{
		"systemctl enable", "systemctl start", "crontab /", "install -m",
	} {
		if strings.Contains(out.String(), forbidden) {
			t.Errorf("--check printed a mutation: %q", forbidden)
		}
	}
}
