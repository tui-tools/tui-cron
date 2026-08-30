package main

import (
	"context"
	"regexp"
	"strings"
	"testing"

	tuicron "github.com/tui-tools/tui-cron"
	"github.com/tui-tools/tui-cron/internal/timers"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/manifest"
	"github.com/tui-tools/tui-kit/runner"
)

// backendNamed loads one manifest block the binary really reads.
func backendNamed(t *testing.T, name string) compat.Backend {
	t.Helper()
	m, err := manifest.Load(tuicron.ManifestJSON)
	if err != nil {
		t.Fatalf("the embedded manifest does not parse: %v", err)
	}
	if m.Name != toolName {
		t.Fatalf("manifest name = %q, want %q", m.Name, toolName)
	}
	b, ok := m.Backend(name)
	if !ok {
		t.Fatalf("the manifest declares no %q backend", name)
	}
	return b
}

// TestManifestDeclaresBothSchedulers. The tool is about two of them, and the
// compatibility block has to name both — including the one it can say no
// version for.
func TestManifestDeclaresBothSchedulers(t *testing.T) {
	systemd := backendNamed(t, systemdBackend)
	if systemd.Binary != "systemctl" {
		t.Errorf("binary = %q, want systemctl", systemd.Binary)
	}
	if systemd.Minimum != "245" {
		t.Errorf("minimum = %q, want 245", systemd.Minimum)
	}
	if len(systemd.VersionCommand) == 0 {
		t.Errorf("a backend with no version command cannot be probed")
	}

	cron := backendNamed(t, "cron")
	if cron.Binary != "crontab" {
		t.Errorf("binary = %q, want crontab", cron.Binary)
	}
	// The decision this test exists to hold: cron declares no version command.
	// cronie answers `crontab -V`; Debian's vixie cron has no such flag and no
	// sibling that prints one, so a version would show on Fedora and be blank
	// on Ubuntu. It is the same choice tui-users made for shadow-utils.
	if len(cron.VersionCommand) != 0 {
		t.Errorf("cron declares a version command (%v), which is not portable",
			cron.VersionCommand)
	}
	if len(cron.Notes) == 0 {
		t.Errorf("cron declares no notes, so nothing explains the missing version")
	}
}

// TestVersionRegexReadsRealOutput uses the `systemctl --version` banner as it
// really prints, which carries a second line full of digits that must not be
// mistaken for the version.
func TestVersionRegexReadsRealOutput(t *testing.T) {
	b := backendNamed(t, systemdBackend)
	tests := map[string]string{
		// Captured from the Fedora 42 host this tool was written on.
		"systemd 257 (257.13-1.fc42)\n+PAM +AUDIT +SELINUX": "257",
		// Ubuntu 24.04 and Debian 12.
		"systemd 255 (255.4-1ubuntu8.4)\n+PAM +AUDIT": "255",
		"systemd 252 (252.22-1~deb12u1)\n+PAM +AUDIT": "252",
		// The oldest release this tool claims to work with.
		"systemd 245 (245.4-4ubuntu3.24)\n+PAM +AUDIT": "245",
	}
	for output, want := range tests {
		if got := compat.ParseVersion(output, b.VersionRegex); got != want {
			t.Errorf("ParseVersion(%q) = %q, want %q", output, got, want)
		}
	}
}

// TestVersionProbeAgainstThisHost runs the real probe when systemctl is
// installed, which is the assertion that the manifest's command and regex still
// match what a machine prints.
//
// It asserts the shape rather than a number: this runs on whatever systemd the
// machine happens to carry, and pinning that would only mean the test breaks
// every time a CI image is refreshed.
func TestVersionProbeAgainstThisHost(t *testing.T) {
	b := backendNamed(t, systemdBackend)
	if !runner.Available(b.Binary, b.SearchPaths...) {
		t.Skip("no systemctl on this machine")
	}
	result := compat.Probe(context.Background(), b)
	if result.Version == "" {
		t.Fatalf("the probe read no version from this host: %s", result.Detail)
	}
	if !versionShape.MatchString(result.Version) {
		t.Errorf("the probe read %q, which is not a systemd version", result.Version)
	}
	if result.Status == compat.StatusUnknown {
		t.Errorf("a version that was read must not classify as unknown")
	}
}

// versionShape is what a systemd version looks like once the regex has had it.
var versionShape = regexp.MustCompile(`^[0-9]+$`)

// TestCronProbesWithoutAVersion is the other half of the decision above: a
// backend with no version command must probe cleanly, reporting its name and
// nothing else rather than failing.
func TestCronProbesWithoutAVersion(t *testing.T) {
	result := compat.Probe(context.Background(), backendNamed(t, "cron"))
	if result.Backend != "cron" {
		t.Errorf("backend = %q", result.Backend)
	}
	if result.Version != "" {
		t.Errorf("a backend with no version command reported %q", result.Version)
	}
	if result.Status != compat.StatusUnknown {
		t.Errorf("status = %v, want unknown", result.Status)
	}
	if result.Detail == "" {
		t.Errorf("nothing explains why there is no version")
	}
	// And with no version, every declared feature counts as present, so nothing
	// is hidden over a number nobody could read.
	if !result.Caps().Has("anything") {
		t.Errorf("an unprobed backend was treated as incapable")
	}
}

// TestFeatureGatesMatchTheReleases pins what the manifest claims:
// `systemctl list-timers --output=json` arrived in systemd 250.
func TestFeatureGatesMatchTheReleases(t *testing.T) {
	b := backendNamed(t, systemdBackend)
	tests := map[string]bool{
		"245": false, "249": false, "250": true, "252": true, "257": true,
	}
	for version, want := range tests {
		caps := compat.NewCaps(version, b.Features)
		if got := caps.Has(timers.FeatureTimersJSON); got != want {
			t.Errorf("systemd %s: %s = %v, want %v",
				version, timers.FeatureTimersJSON, got, want)
		}
	}
}

// TestUnknownVersionKeepsEveryFeature: a version the probe could not read must
// not hide a working view. The backend refuses in its own words instead.
func TestUnknownVersionKeepsEveryFeature(t *testing.T) {
	caps := compat.Result{}.Caps()
	if !caps.Has(timers.FeatureTimersJSON) {
		t.Errorf("an unprobed version must be treated as capable")
	}
}

// TestCapsForPicksTheRightBackend: the tool declares two, and only one of them
// gates a read path. Asking for a backend that was not probed must still answer
// capable rather than empty-handed.
func TestCapsForPicksTheRightBackend(t *testing.T) {
	results := []compat.Result{
		{Backend: "cron"},
		{Backend: systemdBackend},
	}
	if capsFor(results, systemdBackend).Has(timers.FeatureTimersJSON) != true {
		t.Errorf("the systemd caps were not found")
	}
	if !capsFor(results, "nothing-here").Has(timers.FeatureTimersJSON) {
		t.Errorf("an unknown backend must fall back to capable")
	}
}

// TestProbedKeepsOnlyWhatAnswered: the header shows a badge per scheduler that
// reported a version, and a badge for one that is not installed would be noise.
func TestProbedKeepsOnlyWhatAnswered(t *testing.T) {
	got := probed([]compat.Result{
		{Backend: systemdBackend, Version: "257"},
		{Backend: "cron"},
	})
	if len(got) != 1 || got[0].Backend != systemdBackend {
		t.Errorf("probed = %+v", got)
	}
}

func TestProbeInDemoModeReportsNothing(t *testing.T) {
	if got := probeCompat(context.Background(), true); len(got) != 0 {
		t.Errorf("--demo probed the host: %+v", got)
	}
}

func TestClassifiesVersionsAgainstTheMinimum(t *testing.T) {
	b := backendNamed(t, systemdBackend)
	tests := map[string]compat.Status{
		"244": compat.StatusBelowMinimum,
		"245": compat.StatusUntested,
		"257": compat.StatusUntested,
	}
	for version, want := range tests {
		result := compat.ProbeWith(context.Background(), b,
			func(context.Context, []string) (string, error) {
				return "systemd " + version + " (" + version + ".1-1)\n+PAM", nil
			})
		if result.Version != version {
			t.Errorf("probed version %q, want %q", result.Version, version)
		}
		// A version in the manifest's tested list would classify as tested; the
		// expectations above hold while that list is short, so they are skipped
		// for a version the evidence file already covers.
		if isTested(b, version) {
			continue
		}
		if result.Status != want {
			t.Errorf("systemd %s: status %v, want %v", version, result.Status, want)
		}
	}
}

// TestNotesCoverTheRanges: every caveat the README prints has to apply to some
// version, or it is documentation nobody will ever be shown.
func TestNotesCoverTheRanges(t *testing.T) {
	for _, name := range []string{systemdBackend, "cron"} {
		b := backendNamed(t, name)
		if len(b.Notes) == 0 {
			t.Errorf("%s declares no notes", name)
			continue
		}
		for _, note := range b.Notes {
			if strings.TrimSpace(note.Impact) == "" {
				t.Errorf("%s: note %q has no impact sentence", name, note.Range)
			}
			var matched bool
			for _, version := range []string{"1", "244", "245", "249", "250", "257"} {
				if compat.Match(version, note.Range) {
					matched = true
				}
			}
			if !matched {
				t.Errorf("%s: note %q applies to no version anyone runs",
					name, note.Range)
			}
		}
	}
}

// isTested reports whether the manifest already records a passing run.
func isTested(b compat.Backend, version string) bool {
	for _, tested := range b.Tested {
		if tested == version {
			return true
		}
	}
	return false
}
