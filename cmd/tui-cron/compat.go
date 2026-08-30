package main

import (
	"context"

	tuicron "github.com/tui-tools/tui-cron"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/manifest"
)

// probeCompat reads the version of every scheduler this tool drives.
//
// There are two, and they answer very differently. systemd prints a version
// from `systemctl --version` and has a read path that only exists above a known
// release, so its block carries a minimum and a feature. cron prints nothing at
// all that can be relied on: cronie's `crontab -V` says "cronie 1.7.2", and
// Debian's vixie `crontab` has no such flag and no sibling that does — so its
// block declares no version command, the same way tui-users declares none for
// shadow-utils, and the header shows cron without a number rather than showing
// one on Fedora and a blank on Ubuntu.
//
// What each is judged against — the minimum version, the versions the lab has
// actually run against, the caveats that apply to a range — comes from the
// repository's own tool.json, embedded in the binary, so there is no second
// copy of them in the code.
//
// It never fails. A manifest that cannot be parsed produces no results at all,
// and a scheduler this machine does not have produces one with an empty version
// and the reason: on a tool about scheduled jobs, "cron is not installed here"
// is an answer worth showing rather than an error.
func probeCompat(ctx context.Context, demo bool) []compat.Result {
	// --demo drives an in-memory machine; probing the real systemd on the host
	// would report a version that has nothing to do with what is on screen.
	if demo {
		return nil
	}
	m, err := manifest.Load(tuicron.ManifestJSON)
	if err != nil {
		return nil
	}
	results := make([]compat.Result, 0, len(m.Backends))
	for _, backend := range m.Backends {
		results = append(results, compat.Probe(ctx, backend))
	}
	return results
}

// capsFor is one backend's capability set, which is what gates a version-
// dependent read path.
//
// The zero Caps answers yes to everything, and that is the right default for a
// scheduler that was not probed: a backend that cannot do what was asked
// refuses in its own words, and that is a better message than a view hidden
// over an unreadable version string.
func capsFor(results []compat.Result, name string) compat.Caps {
	for _, result := range results {
		if result.Backend == name {
			return result.Caps()
		}
	}
	return compat.Result{}.Caps()
}

// probed keeps the backends that answered with a version, which are the ones
// this machine actually has and can say something about. It is what the header
// shows: a badge for a scheduler that is not installed would be noise, and the
// schedulers screen says so in words instead.
func probed(results []compat.Result) []compat.Result {
	kept := make([]compat.Result, 0, len(results))
	for _, result := range results {
		if result.Version != "" {
			kept = append(kept, result)
		}
	}
	return kept
}
