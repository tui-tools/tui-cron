package jobs

import (
	"context"
	"strings"
	"testing"

	"github.com/tui-tools/tui-cron/internal/schedule"
	"github.com/tui-tools/tui-cron/internal/timers"
)

// ourTimer is a timer this tool wrote, which is the only kind the two actions
// in this file apply to.
var ourTimer = schedule.Job{
	Kind: schedule.KindTimer, Unit: "mirror-sync.timer",
	Service: "mirror-sync.service", Name: "mirror-sync.timer",
	Command: "/usr/local/bin/mirror-sync", ToolWritten: true,
}

// theirTimer is one a package installed: the same shape, without the marker.
var theirTimer = schedule.Job{
	Kind: schedule.KindTimer, Unit: "logrotate.timer",
	Service: "logrotate.service", Name: "logrotate.timer",
	Command: "/usr/sbin/logrotate",
}

// TestOnlyOurOwnUnitsCanBeChangedOrRemoved is the one gate on both actions that
// touch a unit rather than adding a file beside it. It is asserted on the two
// plan builders directly, because a refusal that only the UI enforces is a
// refusal one refactor away from being gone.
func TestOnlyOurOwnUnitsCanBeChangedOrRemoved(t *testing.T) {
	cronLine := schedule.Job{
		Kind: schedule.KindCrontab, Name: "ana · check-queue",
		Command: "/usr/local/bin/check-queue",
	}
	for name, job := range map[string]schedule.Job{
		"a unit a package installed": theirTimer,
		"a cron line":                cronLine,
	} {
		if _, err := deleteTimerPlan(job, "", "", nil); err == nil {
			t.Errorf("deleteTimerPlan built a plan for %s", name)
		}
		if err := checkOurs(job); err == nil {
			t.Errorf("checkOurs accepted %s", name)
		}
	}
	// And the refusal names the unit and what to do instead.
	err := checkOurs(theirTimer)
	if err == nil || !strings.Contains(err.Error(), "logrotate.timer") {
		t.Errorf("the refusal does not name the unit: %v", err)
	}
}

// TestDeletePlanUnloadsBeforeItRemoves pins the order of a deletion, which is
// the part that cannot be got wrong: systemd is told the unit is going before
// the files it reads it from are taken away.
func TestDeletePlanUnloadsBeforeItRemoves(t *testing.T) {
	plan, err := deleteTimerPlan(ourTimer, "[Timer]\n", "[Service]\n",
		[]string{ourTimer.Unit})
	if err != nil {
		t.Fatalf("deleteTimerPlan: %v", err)
	}
	want := []string{
		"systemctl disable --now mirror-sync.timer",
		"rm -f -- /etc/systemd/system/mirror-sync.timer",
		"rm -f -- /etc/systemd/system/mirror-sync.service",
		"rm -f -- /etc/systemd/system/mirror-sync.timer.d/90-tui-cron.conf",
		"systemctl daemon-reload",
	}
	if len(plan.Commands) != len(want) {
		t.Fatalf("the plan has %d commands, want %d", len(plan.Commands), len(want))
	}
	for i, line := range want {
		if got := plan.Commands[i].String(); got != line {
			t.Errorf("command %d = %q, want %q", i, got, line)
		}
	}
	// Both files are diffed away: what they said is nowhere else afterwards.
	for _, path := range []string{
		"/etc/systemd/system/mirror-sync.timer",
		"/etc/systemd/system/mirror-sync.service",
	} {
		if !strings.Contains(plan.Diff, path) {
			t.Errorf("the diff does not show %s going:\n%s", path, plan.Diff)
		}
	}
	if !strings.Contains(plan.Warning, "nothing to undo it with") {
		t.Errorf("the plan does not say the change is final: %q", plan.Warning)
	}
}

// TestAnUnchangedCommandIsNotAChange: a drop-in that would say exactly what the
// drop-in already says is refused, the same way an unchanged schedule is.
func TestAnUnchangedCommandIsNotAChange(t *testing.T) {
	content, err := timers.RenderExecDropIn("/usr/local/bin/mirror-sync")
	if err != nil {
		t.Fatalf("RenderExecDropIn: %v", err)
	}
	if _, err := execDropInPlan(ourTimer, "/usr/local/bin/mirror-sync",
		content); err == nil {
		t.Errorf("a drop-in identical to the one on disk was offered as a change")
	}
}

// TestTheFakeRefusesWhatTheRealBackendRefuses is the demo parity check for the
// two new actions: --demo must be able to reach every refusal the real machine
// has, or the sample machine is teaching the wrong thing.
func TestTheFakeRefusesWhatTheRealBackendRefuses(t *testing.T) {
	fake := NewFake()
	model, err := fake.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	var ours, theirs schedule.Job
	for _, job := range model.Jobs {
		switch {
		case job.ToolWritten:
			ours = job
		case job.Unit == "logrotate.timer":
			theirs = job
		}
	}
	if ours.Unit == "" || theirs.Unit == "" {
		t.Fatalf("the sample machine has no pair to compare")
	}

	if _, err := fake.BuildDelete(context.Background(), model, theirs); err == nil {
		t.Errorf("the demo deleted a unit a package installed")
	}
	if _, err := fake.BuildSetTimerCommand(context.Background(), theirs,
		"/bin/true"); err == nil {
		t.Errorf("the demo re-pointed a unit a package installed")
	}
	if _, err := fake.BuildSetTimerCommand(context.Background(), ours,
		"mirror-sync"); err == nil {
		t.Errorf("the demo accepted a command that is not an absolute path")
	}
	if _, err := fake.BuildDelete(context.Background(), model, ours); err != nil {
		t.Errorf("the demo cannot delete the timer it says this tool wrote: %v", err)
	}
}
