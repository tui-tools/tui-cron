package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/tui-tools/tui-cron/internal/crontab"
	"github.com/tui-tools/tui-cron/internal/timers"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/config"
	"github.com/tui-tools/tui-kit/report"
	"github.com/tui-tools/tui-kit/theme"
)

// runReport prints the block a bug report needs and exits. Everything generic
// — the kit version, the distribution, the kernel, the terminal, where the
// binary came from — is collected by the kit, so the whole family answers
// --report in the same shape. What this function adds is the part only
// tui-cron knows: that its backend is the machine's two schedulers at once,
// which of them is here, and what the same probe --check uses read off each.
//
// It never loads the schedules. --check is the flag that does that, and it
// walks every timer, every table and the journal; a report has to be cheap and
// has to work for a user whose machine is the bug. For the same reason a
// machine with neither scheduler still gets a report, with the selection error
// as one of its lines: "there is nothing here to drive" is a bug report, not a
// refusal.
func runReport(cfg config.Config, opts options, out io.Writer) error {
	palette, _ := theme.ResolvePalette()

	// The same probe --check and the header use. There is one version probe in
	// this tool and this is it.
	backendCompat := probeCompat(context.Background(), opts.demo)

	caps := capsFor(backendCompat, systemdBackend)
	var backendName, selectError string
	if backend, err := pickBackend(cfg, opts, caps); err != nil {
		selectError = err.Error()
	} else {
		backendName = backend.Name()
	}

	info := report.Info{
		Tool:    toolName,
		Version: version,
		Backend: backendName,
		// The backend is the host, and the host has no version: the two
		// schedulers under it carry their own, on a line of their own,
		// because a machine can have one of them, both, or neither.
		BackendDetail: "systemd and cron are versioned separately",
		Demo:          opts.demo,
		Sudo:          cfg.String(config.KeySudo, ""),
		Theme:         palette.Name,
	}
	if opts.demo {
		// The fake imitates a machine that has both schedulers, so a demo
		// report says which ones the session was really exercising rather
		// than leaving "demo" to stand for anything at all.
		info.Backend = "demo"
		info.BackendDetail = ""
		info.Extra = append(info.Extra, report.Field{
			Key: "demo backend", Value: "systemd and cron",
		})
	} else {
		info.Extra = append(info.Extra, report.Field{
			Key: "schedulers", Value: describeSchedulers(backendCompat),
		})
	}
	if selectError != "" {
		info.Extra = append(info.Extra, report.Field{
			Key: "backend error", Value: selectError,
		})
	}

	_, err := io.WriteString(out, report.Render(info))
	return err
}

// describeSchedulers renders both halves of the backend as one line: what the
// version probe read off systemd, and whether cron is installed at all. A
// report that named only the host leaves the reader guessing which scheduler
// the job under the bug belonged to, and that difference is most of what this
// tool does.
//
// cron declares no version command — cronie answers `crontab -V` and Debian's
// vixie cron answers nothing — so it is reported as present or absent rather
// than by a number nobody can rely on.
func describeSchedulers(results []compat.Result) string {
	parts := make([]string, 0, len(results)+1)
	for _, result := range results {
		switch {
		case result.Version != "":
			parts = append(parts, result.Backend+" "+result.Version)
		case result.Backend == "cron":
			parts = append(parts, "cron "+cronPresence())
		case result.Detail != "":
			parts = append(parts, result.Backend+" (version unknown: "+result.Detail+")")
		default:
			parts = append(parts, result.Backend+" (version unknown)")
		}
	}
	if len(results) == 0 {
		// No manifest, so nothing was probed. The two binaries can still be
		// looked for, and that is the fact a report is really after.
		parts = append(parts,
			"systemd "+presence(timers.Available()), "cron "+cronPresence())
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// cronPresence answers the question cron's missing version command leaves
// open, the way the backend itself answers it.
func cronPresence() string { return presence(crontab.Installed()) }

// presence renders a binary the tool looked for but could not version.
func presence(installed bool) string {
	if installed {
		return "installed (no version command)"
	}
	return "absent"
}

// reportUsage is the flag's one-line help, kept here next to what it prints.
var reportUsage = fmt.Sprintf(
	"print the versions and machine facts a bug report needs, then exit "+
		"(no UI, no privileges, nothing about you: paste it into a %s issue)",
	toolName)
