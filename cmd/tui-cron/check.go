package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/tui-tools/tui-cron/internal/schedule"
	"github.com/tui-tools/tui-kit/compat"
)

// checkTimeout bounds the read. Loading the model shells out to systemctl once
// per timer, to crontab once per account and to journalctl for cron's log, and
// a machine with two hundred timers must not hang a non-interactive check
// forever.
const checkTimeout = 60 * time.Second

// jobReport is one job flattened into the fields a shell script can assert on
// without walking the model.
type jobReport struct {
	Name     string           `json:"name"`
	Kind     schedule.Kind    `json:"kind"`
	Schedule string           `json:"schedule"`
	Explain  string           `json:"explain,omitempty"`
	Outcome  schedule.Outcome `json:"outcome"`
	// Detail is the scheduler's own words for that outcome.
	Detail  string `json:"detail,omitempty"`
	Enabled bool   `json:"enabled"`
	// Where is the file and line the job is written at.
	Where string `json:"where,omitempty"`
}

// checkReport is what --check prints: the counts per kind, the jobs worth
// looking at, what each scheduler itself is doing, and the model in full.
//
// It is a report of the read path only. --check never builds and never runs a
// mutation: the whole point is that it is safe to run anywhere, including in CI
// against a production-shaped machine.
type checkReport struct {
	Tool    string `json:"tool"`
	Version string `json:"version"`
	Backend string `json:"backend"`
	// Describe is the backend's own one-line summary, which is where the demo
	// backend says it is a demo.
	Describe string `json:"describe"`

	// Jobs is how many scheduled jobs were found in total, and Counts breaks
	// that down by kind — the numbers a smoke test asserts on to tell a
	// timers-only machine from one that also runs cron.
	Jobs   int            `json:"jobs"`
	Counts map[string]int `json:"counts"`

	// Failed are the jobs whose last run did not succeed, and NeedPersistent
	// the timers that fire daily or less often without Persistent=true. Both
	// are reported rather than asserted: a failing backup is a fact about the
	// machine, not a failure of the read path.
	Failed          []jobReport `json:"failed"`
	NeedPersistent  []jobReport `json:"needPersistent"`
	FailedCount     int         `json:"failedCount"`
	PersistentCount int         `json:"persistentWarnings"`

	// Timers and Cron are what each scheduler said about itself.
	TimersAvailable     bool   `json:"timersAvailable"`
	UserTimersAvailable bool   `json:"userTimersAvailable"`
	TimersDetail        string `json:"timersDetail,omitempty"`
	// CronInstalled and CronRunning are the two facts a smoke test has to be
	// able to assert coherently: Ubuntu ships cron, Fedora Cloud ships cronie,
	// and a minimal server image ships neither.
	CronInstalled bool   `json:"cronInstalled"`
	CronRunning   bool   `json:"cronRunning"`
	CronUnit      string `json:"cronUnit,omitempty"`
	CronDetail    string `json:"cronDetail,omitempty"`

	// Compat is what the version probes found, one entry per declared
	// scheduler. A backend with no version command reports its name and no
	// version, which is the honest answer for cron.
	Compat []compat.Result `json:"compat"`
	// Model is the parsed state in full.
	Model schedule.Model `json:"model"`
}

// runCheck exercises the backend's real read path and prints what it parsed as
// JSON. It returns an error when neither scheduler can be read, which main
// turns into a non-zero exit — so a caller can treat the exit code alone as the
// verdict.
//
// A machine with no cron at all is not a failure: the timers are listed, cron
// reports itself as absent with a reason, and the counts say so. That is the
// read path working, and it is what the smoke test asserts on Omarchy Server.
func runCheck(backend schedule.Backend, backendCompat []compat.Result,
	out io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()

	model, err := backend.Load(ctx)
	if err != nil {
		return fmt.Errorf("%s backend read failed: %w", backend.Name(), err)
	}

	report := checkReport{
		Tool:                toolName,
		Version:             version,
		Backend:             backend.Name(),
		Describe:            backend.Describe(),
		Jobs:                len(model.Jobs),
		Counts:              map[string]int{},
		TimersAvailable:     model.Timers.Available,
		UserTimersAvailable: model.Timers.UserAvailable,
		TimersDetail:        model.Timers.Detail,
		CronInstalled:       model.Cron.Installed,
		CronRunning:         model.Cron.Active,
		CronUnit:            model.Cron.Unit,
		CronDetail:          model.Cron.Detail,
		Compat:              backendCompat,
		Model:               model,
	}

	// Every kind is reported, including the ones with none, so a script can
	// assert on a zero rather than on a key that may or may not be there.
	counts := model.Counts()
	for _, kind := range schedule.Kinds {
		report.Counts[string(kind)] = counts[kind]
	}
	for _, job := range model.Failed() {
		report.Failed = append(report.Failed, flatten(job))
	}
	for _, job := range model.NeedPersistent() {
		report.NeedPersistent = append(report.NeedPersistent, flatten(job))
	}
	report.FailedCount = len(report.Failed)
	report.PersistentCount = len(report.NeedPersistent)

	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

// flatten reduces a job to the fields the report carries.
func flatten(job schedule.Job) jobReport {
	return jobReport{
		Name:     job.Name,
		Kind:     job.Kind,
		Schedule: job.Schedule,
		Explain:  job.Explain,
		Outcome:  job.Outcome,
		Detail:   job.OutcomeDetail,
		Enabled:  job.Enabled,
		Where:    job.Where(),
	}
}
